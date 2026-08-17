package web

import (
	"errors"
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
	Total int
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
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	items, err := s.buildPage(r, store.Cursor{})
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
	if err := s.tmpl.ExecuteTemplate(w, "gallery", galleryView{itemsView: items, Total: total}); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		s.log.Error("gallery テンプレートの描画に失敗", "err", err)
		return
	}
}

// handleItems は無限スクロール用のHTML断片を返す。
// 初回ページと同じテンプレートを使うことで、マークアップの二重管理を避ける。
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	cur, err := parseCursor(r)
	if err != nil {
		http.Error(w, "bad cursor", http.StatusBadRequest)
		return
	}
	items, err := s.buildPage(r, cur)
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
