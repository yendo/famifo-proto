package web

import (
	"html/template"
	"net/http"
)

// mediaView は1枚分のテンプレート入力。
type mediaView struct {
	ID       string
	PageURL  string // その写真だけを開くURL。タイルのリンク先
	ThumbURL string
	FullURL  string
	Date     string // "2006-01-02"。ローカル時刻。クライアントが日の区切りに使う
}

// tilesView は tiles.html の入力。
type tilesView struct {
	Photos []mediaView
}

// dayView は埋め込む日ごとの表の1要素。
// 転送量を抑えるためJSONのキー名を短くしている（数千日ぶんになりうる）。
type dayView struct {
	Date  string `json:"d"` // "2006-01-02"
	Count int    `json:"n"` // その日の枚数
}

// galleryView は gallery.html の入力。tilesViewを埋め込むので
// {{template "tiles" .}} にそのまま渡せる。
type galleryView struct {
	tilesView
	Total     int
	ChunkSize int
	// DayGroups は日ごとの枚数のJSON配列。html/template に再エスケープさせず
	// そのまま出すため template.JS で渡す。中身は日付と数値だけなので
	// "</script>" は構造上現れない。
	DayGroups template.JS
	// OpenIndex は開いた状態で表示する写真の通し番号。noOpenItem なら閉じたまま。
	OpenIndex int
}

// buildRange はオフセット指定で1窓枠分を組み立てる。
func (s *Server) buildRange(r *http.Request, offset, limit int) (tilesView, error) {
	photos, err := s.st.ListRange(r.Context(), offset, limit)
	if err != nil {
		return tilesView{}, err
	}

	// タイルのURLは出どころによらず /thumb/ である。どのファイルを出すかは
	// ハンドラが調べるので、一覧の組み立てではファイルシステムを叩かない。
	v := tilesView{Photos: make([]mediaView, 0, len(photos))}
	for _, p := range photos {
		v.Photos = append(v.Photos, mediaView{
			ID:       p.ID(),
			PageURL:  "/item/" + p.ID(),
			FullURL:  "/file/" + p.ID(),
			ThumbURL: "/thumb/" + p.ID(),
			Date:     p.TakenAt().Format("2006-01-02"),
		})
	}
	return v, nil
}
