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
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// photoView は1枚分のテンプレート入力。
type photoView struct {
	ID       string
	ThumbURL string
	FullURL  string
	Date     string // "2006-01-02"。ローカル時刻。クライアントが日の区切りに使う
}

// itemsView は items.html の入力。
type itemsView struct {
	Photos []photoView
}

// dayView は埋め込む日ごとの表の1要素。
// 転送量を抑えるためキー名を短くしている（数千日ぶんになりうる）。
type dayView struct {
	D string `json:"d"` // "2006-01-02"
	N int    `json:"n"` // その日の枚数
}

// galleryView は gallery.html の入力。itemsViewを埋め込むので
// {{template "items" .}} にそのまま渡せる。
type galleryView struct {
	itemsView
	Total     int
	ChunkSize int
	// DayGroups は日ごとの枚数のJSON配列。html/template に再エスケープさせず
	// そのまま出すため template.JS で渡す。中身は日付と数値だけなので
	// "</script>" は構造上現れない。
	DayGroups template.JS
}

// buildRange はオフセット指定で1窓枠分を組み立てる。
func (s *Server) buildRange(r *http.Request, offset, limit int) (itemsView, error) {
	photos, err := s.st.ListRange(r.Context(), offset, limit)
	if err != nil {
		return itemsView{}, err
	}

	v := itemsView{Photos: make([]photoView, 0, len(photos))}
	for _, p := range photos {
		pv := photoView{
			ID:       p.ID,
			FullURL:  "/photo/" + p.ID,
			ThumbURL: "/photo/" + p.ID,
			Date:     p.TakenAt.Format("2006-01-02"),
		}
		if p.ThumbSource != store.ThumbNone {
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

// handleThumb はサムネイルを配信する。出どころによって置き場所が違う。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	switch p.ThumbSource {
	case store.ThumbFamifo:
		http.ServeFile(w, r, thumb.CachePath(s.thumbDir, p.ID))
	case store.ThumbSyno:
		http.ServeFile(w, r, synology.ThumbPath(p.Path))
	default:
		// 借りるものも作れるものも無い写真。原本を使うべき。
		http.NotFound(w, r)
	}
}

// handlePhoto は拡大表示用の画像を配信する。
//
// HEICはSafari以外のブラウザが表示できない。@eaDir から借りているなら原本ではなく
// SynologyのXL（長辺1707px）を配信する。thumb_source が eadir であればMがあり、
// MとXLは同じ生成器が一緒に書くので、XLの存在はそこから導ける。
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if photo.KindOf(p.Path) == photo.KindOpaque && p.ThumbSource == store.ThumbSyno {
		// 拡張子が .jpg なのでServeFileがContent-Typeを引ける。
		http.ServeFile(w, r, synology.LargePath(p.Path))
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
