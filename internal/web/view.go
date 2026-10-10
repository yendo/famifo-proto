package web

import (
	"net/http"

	"github.com/yendo/famifo-proto/internal/imagefmt"
)

// indexView は index.html の入力。tilesViewを埋め込むので
// {{template "tiles" .}} にそのまま渡せる。
type indexView struct {
	tilesView
	Total     int
	ChunkSize int
	// DayGroups は日ごとの件数。html/template が <script> の中でJSONにして埋め込む。
	DayGroups []dayGroup
	// OpenIndex は開いた状態で表示する1件の通し番号。noOpenItem なら閉じたまま。
	OpenIndex int
	// AuthEnabled は認証が有効かどうか。無効ならログアウトのボタンを出さない。
	AuthEnabled bool
}

// tilesView は tiles.html の入力。
type tilesView struct {
	Media []mediaView
}

// mediaView は1件分のテンプレート入力。
type mediaView struct {
	ID       string
	PageURL  string // その1件だけを開くURL。タイルのリンク先
	ThumbURL string
	FullURL  string
	Date     string // "2006-01-02"。ローカル時刻。クライアントが日の区切りに使う
	IsVideo  bool   // タイルに再生の印を出すか、フルビューを <video> にするか
}

// dayGroup は埋め込む DayGroups の1要素。
// 転送量を抑えるためJSONのキー名を短くしている（数千日ぶんになりうる）。
type dayGroup struct {
	Date  string `json:"d"` // "2006-01-02"
	Count int    `json:"n"` // その日の件数
}

// buildIndexView はトップページの入力を組み立てる。
// 先頭の塊を埋めておくので、開いた直後に灰色の画面が出ない。
func (h *Handler) buildIndexView(r *http.Request, openIndex int) (indexView, error) {
	tiles, err := h.buildTilesView(r, 0, h.chunkSize)
	if err != nil {
		return indexView{}, err
	}
	total, err := h.store.Count(r.Context())
	if err != nil {
		return indexView{}, err
	}
	days, err := h.store.DayGroups(r.Context())
	if err != nil {
		return indexView{}, err
	}
	dayGroups := make([]dayGroup, 0, len(days))
	for _, d := range days {
		dayGroups = append(dayGroups, dayGroup{Date: d.Date, Count: d.Count})
	}

	return indexView{
		tilesView: tiles, Total: total, ChunkSize: h.chunkSize,
		DayGroups: dayGroups, OpenIndex: openIndex,
		AuthEnabled: h.auth != nil,
	}, nil
}

// buildTilesView はオフセット指定で1窓枠分を組み立てる。
func (h *Handler) buildTilesView(r *http.Request, offset, limit int) (tilesView, error) {
	items, err := h.store.ListRange(r.Context(), offset, limit)
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
			FullURL:  "/full/" + m.ID(),
			ThumbURL: "/thumb/" + m.ID(),
			Date:     m.TakenAt().Format("2006-01-02"),
			IsVideo:  imagefmt.IsVideo(m.Path()),
		})
	}
	return v, nil
}
