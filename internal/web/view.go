package web

import (
	"html/template"
	"net/http"

	"github.com/yendo/famifo-proto/internal/photo"
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
		if _, ok := photo.ThumbPath(p, s.thumbDir); ok {
			pv.ThumbURL = "/thumb/" + p.ID
		}
		v.Photos = append(v.Photos, pv)
	}
	return v, nil
}
