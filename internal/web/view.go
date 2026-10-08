package web

import (
	"html/template"
	"net/http"

	"github.com/yendo/famifo-proto/internal/imagefmt"
)

// mediaView は1件分のテンプレート入力。
type mediaView struct {
	ID       string
	PageURL  string // その1件だけを開くURL。タイルのリンク先
	ThumbURL string
	FullURL  string
	Date     string // "2006-01-02"。ローカル時刻。クライアントが日の区切りに使う
	IsVideo  bool   // タイルに再生の印を出すか、拡大表示を <video> にするか
}

// tilesView は tiles.html の入力。
type tilesView struct {
	Media []mediaView
}

// dayView は埋め込む日ごとの表の1要素。
// 転送量を抑えるためJSONのキー名を短くしている（数千日ぶんになりうる）。
type dayView struct {
	Date  string `json:"d"` // "2006-01-02"
	Count int    `json:"n"` // その日の件数
}

// indexView は index.html の入力。tilesViewを埋め込むので
// {{template "tiles" .}} にそのまま渡せる。
type indexView struct {
	tilesView
	Total     int
	ChunkSize int
	// DayGroups は日ごとの枚数のJSON配列。html/template に再エスケープさせず
	// そのまま出すため template.JS で渡す。中身は日付と数値だけなので
	// "</script>" は構造上現れない。
	DayGroups template.JS
	// OpenIndex は開いた状態で表示する1件の通し番号。noOpenItem なら閉じたまま。
	OpenIndex int
	// AuthEnabled は認証が有効かどうか。無効ならログアウトのボタンを出さない。
	AuthEnabled bool
}

// buildRange はオフセット指定で1窓枠分を組み立てる。
func (a *app) buildRange(r *http.Request, offset, limit int) (tilesView, error) {
	items, err := a.store.ListRange(r.Context(), offset, limit)
	if err != nil {
		return tilesView{}, err
	}

	// タイルのURLは出どころによらず /thumb/ である。どのファイルを出すかは
	// ハンドラが調べるので、一覧の組み立てではファイルシステムを叩かない。
	v := tilesView{Media: make([]mediaView, 0, len(items))}
	for _, m := range items {
		v.Media = append(v.Media, mediaView{
			ID:       m.ID(),
			PageURL:  "/item/" + m.ID(),
			FullURL:  "/file/" + m.ID(),
			ThumbURL: "/thumb/" + m.ID(),
			Date:     m.TakenAt().Format("2006-01-02"),
			IsVideo:  imagefmt.IsVideo(m.Path()),
		})
	}
	return v, nil
}
