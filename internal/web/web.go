// Package web はギャラリーのHTTP配信を担う。
// テンプレートと静的ファイルはバイナリに埋め込む（単一バイナリ配布のため）。
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

//go:embed templates static
var assets embed.FS

// noPreview は出せる絵が無い写真のタイルに配るプレースホルダ。
//
//go:embed static/no-preview.svg
var noPreview []byte

// DefaultChunkSize は仮想スクロールが1回に取る塊の枚数。
//
// 先頭の1塊は初回HTMLに埋め込む。クライアントは範囲を覆う塊が揃うまで
// 描かないので、この値が「開いた画面 + overscan 4行」に届かないと、開いた
// 直後に取得を1往復待つことになる。1920x950・列幅200pxで先頭に要るのは
// 76枚（実データ4497枚で計測）で、60では足りていなかった。
//
// 利用者が変えられる設定ではない。表示の寸法を変えたときは測り直すこと。
const DefaultChunkSize = 120

// contentSecurityPolicy はすべての応答に載せる方針。
//
// default-src を 'none' にして、必要なものだけを足す。ギャラリーが読むのは
// /static/ のCSSとJS、/thumb/ と /file/ の画像と動画、それに /tiles の fetch
// だけで、外部から取るものは1つも無い（idiomorph も同梱してある）。
// connect-src が要るのは、仮想スクロールが /tiles を fetch するためである。
//
// script-src に 'unsafe-inline' は入れない。ここが厳しくあることが、この方針を
// 入れる理由そのものである。index.html にインラインスクリプトは無く、
// <script type="application/json" id="daygroups"> は実行されないデータブロック
// なので、この制限に引っかからない。
//
// style-src だけ 'unsafe-inline' を許す。gallery.js が組み立てる日カードが
// style 属性で grid-column と grid-template-columns を持っており（cardHTML を
// 見よ）、マークアップ中の style 属性は style-src-attr の対象になるため、
// 許さないと属性ごと無視されてレイアウトが崩れる。span の数だけクラスを
// 用意すれば外せるが、XSSを止めるのは script-src のほうなので、レイアウトの
// 中心を書き換えてまで得るものは少ない。
// JSからの spacer.style.height のようなCSSOM経由の代入はCSPの対象外なので、
// こちらは関係しない。
//
// frame-ancestors はクリックジャッキング対策。form-action と base-uri は、
// 万一マークアップを注入されたときに、送信先やURLの解決先を外へ向けられない
// ようにするためのもの。
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self'; " +
	"media-src 'self'; " +
	"connect-src 'self'; " +
	"form-action 'self'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'"

// noOpenItem は「開いた写真は無い」ことを表す通し番号。
const noOpenItem = -1

// NewHandler はルーティング済みのハンドラを返す。
//
// thumbs は取り込み側と共有する。配信するファイルの選択はすべてそこが決めるので、
// このパッケージはサムネイルの置き場所を知らない。
//
// auth に nil を渡すと認証しない。開発機やテストでIdPを立てずに動かせるようにするため
// であり、既定の構成でもある。
//
// chunkSize は仮想スクロールが1回に取る塊の枚数で、本番は DefaultChunkSize を渡す。
// テストは塊の境界を跨ぐ挙動を少ない写真で確かめるため、小さい値を渡す。
//
// セッションのミドルウェア（LoadAndSave）は /static/ には掛けない。静的ファイルは
// セッションを読まないし、掛けると応答に Vary: Cookie が付く。
func NewHandler(st *store.Store, thumbs *thumb.Provider, auth *Auth, chunkSize int, log *slog.Logger) (http.Handler, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("cannot load the templates: %w", err)
	}
	h := &handlers{store: st, tmpl: tmpl, thumbs: thumbs, chunkSize: chunkSize, auth: auth, log: log}

	mux := http.NewServeMux()

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // embedの内容は固定なので、ここで失敗するならビルドの不備
	}
	// 未認証でもCSSは当たるようにする。ログイン前の画面が崩れる意味がない。
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	routes := http.NewServeMux()
	routes.HandleFunc("GET /{$}", h.handleIndex)
	routes.HandleFunc("GET /item/{id}", h.handleItem)
	routes.HandleFunc("GET /tiles", h.handleTiles)
	routes.HandleFunc("GET /thumb/{id}", h.handleThumb)
	routes.HandleFunc("GET /file/{id}", h.handleFile)

	if auth == nil {
		mux.Handle("/", routes)
	} else {
		session := http.NewServeMux()
		session.Handle("/", auth.requireSignIn(routes))
		auth.addRoutes(session)
		mux.Handle("/", auth.sessions.LoadAndSave(session))
	}
	return securityHeaders(mux), nil
}

// securityHeaders はすべての応答に同じ守りを載せる。
//
// nosniff が効くのは /thumb/ と /file/ である。どちらもディスク上のファイルの
// 中身を、拡張子だけから決めたMIMEタイプで配る。写真のディレクトリにHTMLの
// 中身を持つ .jpg が置かれても、いまのブラウザは image/* と宣言された応答を
// HTMLへ格上げして解釈しないが、その挙動に頼らずに済ませる。
//
// 認証の内側と外側の両方に載せたいので、いちばん外側の mux を包む。/static/ も
// 通る。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// handlers はHTTPハンドラが使う依存をまとめる。ハンドラはこの型のメソッドで、
// 認証の経路は Auth が持つ。
type handlers struct {
	store     *store.Store
	tmpl      *template.Template
	thumbs    *thumb.Provider
	chunkSize int
	auth      *Auth // nil なら認証しない
	log       *slog.Logger
}

// handleIndex はギャラリーのトップページを返す。
func (h *handlers) handleIndex(w http.ResponseWriter, r *http.Request) {
	h.renderIndex(w, r, noOpenItem)
}

// handleItem は写真ごとのURLを受け、その写真を開いた状態のギャラリーを返す。
// 返すHTMLは / と同じで、違いは「どれを開くか」を表す通し番号だけである。
//
// 消えた写真のURLを共有されることは普通に起きる。404にすると行き止まりになるので、
// ギャラリーへ送る。
func (h *handlers) handleItem(w http.ResponseWriter, r *http.Request) {
	rank, err := h.store.RankOf(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.renderIndex(w, r, rank)
}

// handleTiles は仮想スクロール用のHTML断片を返す。
// 初回ページと同じテンプレートを使い、マークアップを1箇所に保つ。
func (h *handlers) handleTiles(w http.ResponseWriter, r *http.Request) {
	offset, limit, err := parseWindow(r, h.chunkSize)
	if err != nil {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	tiles, err := h.buildRange(r, offset, limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, "tiles", tiles); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		h.log.Error("failed to render the tiles template", "err", err)
		return
	}
}

// handleThumb は一覧のタイルを配信する。どのファイルを出すかは thumb が決める。
// 出せる絵が無ければプレースホルダに差し替えるので、404にはならない。
func (h *handlers) handleThumb(w http.ResponseWriter, r *http.Request) {
	m, ok := h.lookupMedia(w, r)
	if !ok {
		return
	}
	path, contentType, ok := h.thumbs.Path(m)
	if !ok {
		serveNoPreview(w)
		return
	}
	// ServeFileは拡張子からMIMEを引くがHEIC/HEIFを知らない。
	// 先に設定しておけばServeContentは上書きしない。
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, path)
}

// handleFile は拡大表示用の画像を配信する。
func (h *handlers) handleFile(w http.ResponseWriter, r *http.Request) {
	m, ok := h.lookupMedia(w, r)
	if !ok {
		return
	}
	path, contentType := fullViewPath(m)
	// ServeFileは拡張子からMIMEを引くがHEIC/HEIFを知らない。
	// 先に設定しておけばServeContentは上書きしない。
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, path)
}

// renderIndex はトップページのHTMLを組み立てて返す。openIndex は開いた状態で
// 表示する写真の通し番号で、noOpenItem なら閉じたまま開く。
// 先頭の塊を埋めた状態で返すので、開いた直後に灰色の画面が出ない。
func (h *handlers) renderIndex(w http.ResponseWriter, r *http.Request, openIndex int) {
	tiles, err := h.buildRange(r, 0, h.chunkSize)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	total, err := h.store.Count(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	days, err := h.store.DayGroups(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dayViews := make([]dayView, 0, len(days))
	for _, d := range days {
		dayViews = append(dayViews, dayView{Date: d.Date, Count: d.Count})
	}
	raw, err := json.Marshal(dayViews)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := indexView{
		tilesView: tiles, Total: total, ChunkSize: h.chunkSize,
		DayGroups: template.JS(raw), OpenIndex: openIndex,
		AuthEnabled: h.auth != nil,
	}
	if err := h.tmpl.ExecuteTemplate(w, "index", view); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		h.log.Error("failed to render the index template", "err", err)
		return
	}
}

// parseWindow はクエリから窓枠の範囲を読む。省略時は先頭から chunkSize 件。
func parseWindow(r *http.Request, defaultLimit int) (offset, limit int, err error) {
	limit = defaultLimit
	q := r.URL.Query()

	if raw := q.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("invalid offset: %q", raw)
		}
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return 0, 0, fmt.Errorf("invalid limit: %q", raw)
		}
	}
	return offset, limit, nil
}

// lookupMedia はURLのIDから写真を引く。
// パスではなくIDを経由することで、インデックスに無いファイルは配信できない。
func (h *handlers) lookupMedia(w http.ResponseWriter, r *http.Request) (media.Media, bool) {
	m, err := h.store.GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return media.Media{}, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return media.Media{}, false
	}
	return m, true
}

// serveNoPreview は出せる絵が無いときのプレースホルダを配る。
//
// 404にして <img> を壊すのではなく画像を配るのは、貼り替えのたびに失敗を検知し
// 直す仕掛けをクライアントに持たせないためである。タイルは idiomorph が同じ要素の
// まま貼り替えるので、JSが付けた印は貼り替えで消え、再読み込みも起きない。
//
// キャッシュさせないのは、DSMが後からサムネイルを作ったときに次の表示で
// 本物へ切り替わるようにするため。
func serveNoPreview(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(noPreview)
}

// fullViewPath は拡大表示に配信するファイルのパスと、そのMIMEタイプを返す。
//
// 借りるものが2種類ある。HEICはSafari以外のブラウザが表示できないので、@eaDir から
// 借りられるなら原本ではなくSynologyのXL（長辺1707px）を返す。動画はHEVCが端末に
// よって再生できないので、Synologyが作ったH.264版（SYNOPHOTO_FILM_H.mp4）を返す。
//
// 動画を先に見るのは、静止画のXLを掴ませないためである。動画にもMとXLは作られるので、
// 順序を逆にすると再生する場面で1枚の静止画が配られる。
//
// 写真では「MとXLは同じ生成器が一緒に書く」としてXLの存在を確かめていないが、動画では
// その導出が成り立たない。サムネイル生成と動画変換は別の工程で、実測した @eaDir にも
// SYNOPHOTO_THUMB_M.jpg があるのに SYNOPHOTO_FILM.fail があった。だからフィルムは
// HasFilm で自分で確かめる。
//
// 借りたXLは .jpg、借りたフィルムは .mp4 なので、選ばれたパスからMIMEを引き直せる。
// 呼び出し側が引き直さずに済むよう、ここで一緒に返す。
func fullViewPath(m media.Media) (path, contentType string) {
	path = m.Path()
	switch {
	case imagefmt.IsVideo(path):
		if synology.HasFilm(path) {
			path = synology.FilmPath(path)
		}
	case imagefmt.IsSupported(path) && !imagefmt.IsDecodable(path) && synology.HasThumbM(path):
		path = synology.ThumbXLPath(path)
	}
	return path, imagefmt.ContentType(path)
}
