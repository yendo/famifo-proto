// Package web はギャラリーのHTTP配信を担う。
// テンプレートと静的ファイルはバイナリに埋め込む（単一バイナリ配布のため）。
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

//go:embed templates static
var assets embed.FS

// noPreview は出せる絵が無い写真のタイルに配るプレースホルダ。
//
//go:embed static/no-preview.svg
var noPreview []byte

// defaultChunkSize は仮想スクロールが1回に取る塊の枚数。
//
// 先頭の1塊は初回HTMLに埋め込む。クライアントは範囲を覆う塊が揃うまで
// 描かないので、この値が「開いた画面 + overscan 4行」に届かないと、開いた
// 直後に取得を1往復待つことになる。1920x950・列幅200pxで先頭に要るのは
// 76枚（実データ4497枚で計測）で、60では足りていなかった。
//
// 利用者が変えられる設定ではない。表示の寸法を変えたときは測り直すこと。
const defaultChunkSize = 120

// contentSecurityPolicy はすべての応答に載せる方針。
//
// default-src を 'none' にして、必要なものだけを足す。ギャラリーが読むのは
// /static/ のCSSとJS、/thumb/ と /file/ の画像と動画、それに /tiles の fetch
// だけで、外部から取るものは1つも無い（idiomorph も同梱してある）。
// connect-src が要るのは、仮想スクロールが /tiles を fetch するためである。
//
// script-src に 'unsafe-inline' は入れない。ここが厳しくあることが、この方針を
// 入れる理由そのものである。gallery.html にインラインスクリプトは無く、
// <script type="application/json" id="daygroups"> は実行されないデータブロック
// なので、この制限に引っかからない。
//
// style-src だけ 'unsafe-inline' を許す。app.js が組み立てる日カードが
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

// Server はギャラリーのHTTPハンドラ群を保持する。
type Server struct {
	st        *store.Store
	tmpl      *template.Template
	thumbs    *thumb.Provider
	chunkSize int
	auth      *Auth // nil なら認証しない
	log       *slog.Logger
}

// NewServer はテンプレートを読み込んでServerを作る。
// 塊の大きさは defaultChunkSize に任せる。利用者が変えられる設定ではない。
//
// thumbs は取り込み側と共有する。配信するファイルの選択はすべてそこが決めるので、
// サーバーはサムネイルの置き場所を知らない。
//
// auth に nil を渡すと認証しない。開発機やテストでIdPを立てずに動かせるようにするため
// であり、既定の構成でもある。
func NewServer(st *store.Store, thumbs *thumb.Provider, auth *Auth, log *slog.Logger) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("cannot load the templates: %w", err)
	}
	return &Server{st: st, tmpl: tmpl, thumbs: thumbs, chunkSize: defaultChunkSize, auth: auth, log: log}, nil
}

// Handler はルーティング済みのハンドラを返す。
//
// セッションのミドルウェア（LoadAndSave）は /static/ には掛けない。静的ファイルは
// セッションを読まないし、掛けると応答に Vary: Cookie が付く。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // embedの内容は固定なので、ここで失敗するならビルドの不備
	}
	// 未認証でもCSSは当たるようにする。ログイン前の画面が崩れる意味がない。
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", s.handleGallery)
	protected.HandleFunc("GET /item/{id}", s.handleItem)
	protected.HandleFunc("GET /tiles", s.handleTiles)
	protected.HandleFunc("GET /thumb/{id}", s.handleThumb)
	protected.HandleFunc("GET /file/{id}", s.handleFile)

	inner := http.NewServeMux()
	inner.Handle("/", s.authenticate(protected))
	if s.auth == nil {
		mux.Handle("/", inner)
		return securityHeaders(mux)
	}
	inner.HandleFunc("GET /login", s.handleLogin)
	inner.HandleFunc("GET /auth/callback", s.handleCallback)
	inner.HandleFunc("POST /logout", s.handleLogout)
	// RP-Initiated LogoutでIdPが戻ってくる先。/logout自身がend_session_endpoint
	// を持たないIdPのとき案内ページとして返すのもここ。
	inner.HandleFunc("GET /signed-out", s.handleSignedOut)
	mux.Handle("/", s.auth.Sessions.LoadAndSave(inner))
	return securityHeaders(mux)
}
