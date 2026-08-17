package web

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// handleGallery はギャラリーのトップページを返す。Task 10で実装する。
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// handleItems は無限スクロール用のHTML断片を返す。Task 10で実装する。
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
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
