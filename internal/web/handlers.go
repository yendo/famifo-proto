package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/store"
)

// noOpenItem は「開いた写真は無い」ことを表す通し番号。
const noOpenItem = -1

// handleGallery はギャラリーのトップページを返す。
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	s.renderGallery(w, r, noOpenItem)
}

// handleItem は写真ごとのURLを受け、その写真を開いた状態のギャラリーを返す。
// 返すHTMLは / と同じで、違いは「どれを開くか」を表す通し番号だけである。
//
// 消えた写真のURLを共有されることは普通に起きる。404にすると行き止まりになるので、
// ギャラリーへ送る。
func (s *Server) handleItem(w http.ResponseWriter, r *http.Request) {
	rank, err := s.st.RankOf(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderGallery(w, r, rank)
}

// renderGallery はギャラリーのHTMLを組み立てて返す。openIndex は開いた状態で
// 表示する写真の通し番号で、noOpenItem なら閉じたまま開く。
// 先頭の塊を埋めた状態で返すので、開いた直後に灰色の画面が出ない。
func (s *Server) renderGallery(w http.ResponseWriter, r *http.Request, openIndex int) {
	tiles, err := s.buildRange(r, 0, s.chunkSize)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	total, err := s.st.Count(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	days, err := s.st.DayGroups(r.Context())
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
	view := galleryView{
		tilesView: tiles, Total: total, ChunkSize: s.chunkSize,
		DayGroups: template.JS(raw), OpenIndex: openIndex,
		AuthEnabled: s.auth != nil,
	}
	if err := s.tmpl.ExecuteTemplate(w, "gallery", view); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		s.log.Error("failed to render the gallery template", "err", err)
		return
	}
}

// handleTiles は仮想スクロール用のHTML断片を返す。
// 初回ページと同じテンプレートを使い、マークアップを1箇所に保つ。
func (s *Server) handleTiles(w http.ResponseWriter, r *http.Request) {
	offset, limit, err := parseWindow(r, s.chunkSize)
	if err != nil {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	tiles, err := s.buildRange(r, offset, limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "tiles", tiles); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		s.log.Error("failed to render the tiles template", "err", err)
		return
	}
}

// handleThumb は一覧のタイルを配信する。どのファイルを出すかは thumb が決める。
// 出せる絵が無ければプレースホルダに差し替えるので、404にはならない。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookupMedia(w, r)
	if !ok {
		return
	}
	path, contentType, ok := s.thumbs.SmallPath(p)
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
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookupMedia(w, r)
	if !ok {
		return
	}
	path, contentType := s.thumbs.LargePath(p)
	// ServeFileは拡張子からMIMEを引くがHEIC/HEIFを知らない。
	// 先に設定しておけばServeContentは上書きしない。
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, path)
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

// lookupMedia はURLのIDから写真を引く。
// パスではなくIDを経由することで、インデックスに無いファイルは配信できない。
func (s *Server) lookupMedia(w http.ResponseWriter, r *http.Request) (media.Media, bool) {
	p, err := s.st.GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return media.Media{}, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return media.Media{}, false
	}
	return p, true
}
