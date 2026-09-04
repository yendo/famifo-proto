package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
)

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
	days, err := s.st.DayGroups(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dv := make([]dayView, 0, len(days))
	for _, d := range days {
		dv = append(dv, dayView{D: d.Date, N: d.Count})
	}
	raw, err := json.Marshal(dv)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := galleryView{
		itemsView: items, Total: total, ChunkSize: s.pageSize,
		DayGroups: template.JS(raw),
	}
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

// handleThumb はサムネイルを配信する。どのファイルを出すかは photo が決める。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	path, ok := p.ThumbPath(s.thumbDir)
	if !ok {
		// 借りるものも作れるものも無い写真。原本を使うべき。
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

// handlePhoto は拡大表示用の画像を配信する。
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	// ServeFileは拡張子からMIMEを引くがHEIC/HEIFを知らない。
	// 先に設定しておけばServeContentは上書きしない。
	w.Header().Set("Content-Type", p.ContentType())
	http.ServeFile(w, r, p.FullPath())
}

// lookup はURLのIDから写真を引く。
// パスではなくIDを経由することで、インデックスに無いファイルは配信できない。
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (photo.Photo, bool) {
	p, err := s.st.GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return photo.Photo{}, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return photo.Photo{}, false
	}
	return p, true
}
