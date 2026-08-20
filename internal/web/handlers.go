package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// photoView は1枚分のテンプレート入力。
type photoView struct {
	ID       string
	ThumbURL string
	FullURL  string
}

// cursorView は次ページを指すカーソル。
type cursorView struct {
	TakenAt int64
	ID      string
}

// itemsView は items.html の入力。
type itemsView struct {
	Photos []photoView
	Next   *cursorView
}

// galleryView は gallery.html の入力。itemsViewを埋め込むので
// {{template "items" .}} にそのまま渡せる。
type galleryView struct {
	itemsView
	Total     int
	ChunkSize int
}

// monthView は /dates の1要素。転送量を抑えるためキー名を短くしている。
type monthView struct {
	M string `json:"m"` // "2006-01"
	O int    `json:"o"` // その月が始まるオフセット
}

// buildPage は1ページ分を組み立てる。次ページの有無を知るために
// pageSize+1 件取得し、余った1件は描画せず「続きがある」印としてだけ使う。
func (s *Server) buildPage(r *http.Request, cur store.Cursor) (itemsView, error) {
	photos, err := s.st.ListPage(r.Context(), cur, s.pageSize+1)
	if err != nil {
		return itemsView{}, err
	}

	var v itemsView
	if len(photos) > s.pageSize {
		photos = photos[:s.pageSize]
		last := photos[len(photos)-1]
		v.Next = &cursorView{TakenAt: last.TakenAt.Unix(), ID: last.ID}
	}

	v.Photos = make([]photoView, 0, len(photos))
	for _, p := range photos {
		pv := photoView{ID: p.ID, FullURL: "/photo/" + p.ID, ThumbURL: "/photo/" + p.ID}
		if p.HasThumb {
			pv.ThumbURL = "/thumb/" + p.ID
		}
		v.Photos = append(v.Photos, pv)
	}
	return v, nil
}

// buildRange はオフセット指定で1窓枠分を組み立てる。
func (s *Server) buildRange(r *http.Request, offset, limit int) (itemsView, error) {
	photos, err := s.st.ListRange(r.Context(), offset, limit)
	if err != nil {
		return itemsView{}, err
	}

	v := itemsView{Photos: make([]photoView, 0, len(photos))}
	for _, p := range photos {
		pv := photoView{ID: p.ID, FullURL: "/photo/" + p.ID, ThumbURL: "/photo/" + p.ID}
		if p.HasThumb {
			pv.ThumbURL = "/thumb/" + p.ID
		}
		v.Photos = append(v.Photos, pv)
	}
	return v, nil
}

// parseWindow はクエリから窓枠の範囲を読む。省略時は先頭から pageSize 件。
func parseWindow(r *http.Request, defaultLimit int) (offset, limit int, err error) {
	limit = defaultLimit
	q := r.URL.Query()

	if raw := q.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("offset が不正です: %q", raw)
		}
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return 0, 0, fmt.Errorf("limit が不正です: %q", raw)
		}
	}
	return offset, limit, nil
}

// parseCursor はクエリからカーソルを読む。パラメータが無ければ先頭ページ。
func parseCursor(r *http.Request) (store.Cursor, error) {
	raw := r.URL.Query().Get("t")
	id := r.URL.Query().Get("id")
	if raw == "" && id == "" {
		return store.Cursor{}, nil
	}
	at, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return store.Cursor{}, err
	}
	return store.Cursor{TakenAt: time.Unix(at, 0), ID: id, Set: true}, nil
}

// handleGallery はギャラリーのトップページを返す。
// 先頭の塊を埋めた状態で返すので、開いた直後に灰色の画面が出ない。
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	items, err := s.buildRange(r, 0, s.pageSize)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	total, err := s.st.Count(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := galleryView{itemsView: items, Total: total, ChunkSize: s.pageSize}
	if err := s.tmpl.ExecuteTemplate(w, "gallery", view); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		s.log.Error("gallery テンプレートの描画に失敗", "err", err)
		return
	}
}

// handleItems は仮想スクロール用のHTML断片を返す。
// 初回ページと同じテンプレートを使い、マークアップを1箇所に保つ。
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	offset, limit, err := parseWindow(r, s.pageSize)
	if err != nil {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	items, err := s.buildRange(r, offset, limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "items", items); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		s.log.Error("items テンプレートの描画に失敗", "err", err)
		return
	}
}

// handleDates は月ごとの開始位置を返す。日付スクラバーの目盛りに使う。
// ここだけJSONなのは、マークアップではなくデータだから。
func (s *Server) handleDates(w http.ResponseWriter, r *http.Request) {
	months, err := s.st.MonthOffsets(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	out := make([]monthView, 0, len(months))
	for _, m := range months {
		out = append(out, monthView{M: m.Month, O: m.Offset})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.log.Error("dates の書き出しに失敗", "err", err)
	}
}

// handleThumb はサムネイルを配信する。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if !p.HasThumb {
		// HEICなどサムネイルを作らないフォーマット。原本を使うべき。
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.thumbDir, thumb.RelPath(p.ID)))
}

// handlePhoto は原本を配信する。
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	// ServeFileは拡張子からMIMEを引くがHEIC/HEIFを知らない。
	// 先に設定しておけばServeContentは上書きしない。
	w.Header().Set("Content-Type", photo.ContentType(p.Path))
	http.ServeFile(w, r, p.Path)
}

// lookup はURLのIDから写真を引く。
// パスではなくIDを経由することで、インデックスに無いファイルは配信できない。
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (store.Photo, bool) {
	p, err := s.st.GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return store.Photo{}, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return store.Photo{}, false
	}
	return p, true
}
