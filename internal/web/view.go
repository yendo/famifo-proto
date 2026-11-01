package web

import (
	"net/http"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/store"
)

// indexView は index.html の入力。タイルは持たない。一覧の中身はクライアントが
// /tiles から取り、カードに組んでから貼る。
type indexView struct {
	Total     int
	ChunkSize int
	// TilesURL はチャンクを取る /tiles の URL。絞り込みを含む。chunk.js は
	// チャンクの番号だけを足す。
	TilesURL string
	// HomeURL は個別表示を閉じたときに戻る一覧の URL。絞り込みを含む。
	HomeURL string
	// DayGroups は日ごとの件数。html/template が <script> の中でJSONにして埋め込む。
	DayGroups []dayGroup
	// OpenIndex は開いた状態で表示する1件の通し番号。noOpenItem なら閉じたまま。
	OpenIndex int
	// Filtered は条件が1つでも掛かっているか。狭い画面の絞り込みボタンの色に使う。
	Filtered bool
	// Owners はパネルの撮影者の節の項目。名前順。空なら節もパネルも出さない。
	Owners []ownerChoice
	// AuthEnabled は認証が有効かどうか。無効ならログアウトのボタンを出さない。
	AuthEnabled bool
}

// mediaView は /tiles の応答の1要素。クライアント（chunk.js の Tile）との取り決め。
// URL はここで組み立てて送る。ID だけを送ると、URL の規則が Go と JS の2箇所に分かれる。
type mediaView struct {
	ID       string `json:"id"`
	PageURL  string `json:"pageUrl"` // その1件だけを開くURL。絞り込みを引き継ぐ。タイルのリンク先
	FullURL  string `json:"fullUrl"`
	ThumbURL string `json:"thumbUrl"`
	InfoURL  string `json:"infoUrl"` // 個別表示に重ねる情報（/info/<id>）
	Date     string `json:"date"`    // "2006-01-02"。撮影地の日。クライアントが日の区切りに使う
	IsVideo  bool   `json:"isVideo"` // タイルに再生の印を出すか、個別表示を <video> にするか
}

// dayGroup は埋め込む DayGroups の1要素。
// 転送量を抑えるためJSONのキー名を短くしている（数千日ぶんになりうる）。
type dayGroup struct {
	Date  string `json:"d"` // "2006-01-02"
	Count int    `json:"n"` // その日の件数
}

// infoView は /info/{id} の応答。個別表示に重ねる1件分の情報で、一覧では
// 使わないので /tiles には載せない。出せない項目はキーごと省く。
type infoView struct {
	Owner   string `json:"owner,omitempty"` // 撮影者の名前
	TakenAt string `json:"takenAt"`         // "2006-01-02T15:04:05"。撮影地の時刻で、時差は持たない
}

// buildIndexView はトップページの入力を組み立てる。f の条件で絞る。
func (h *Handler) buildIndexView(r *http.Request, f store.Filter, openIndex int) (indexView, error) {
	total, err := h.store.Count(r.Context(), f)
	if err != nil {
		return indexView{}, err
	}
	days, err := h.store.DayGroups(r.Context(), f)
	if err != nil {
		return indexView{}, err
	}
	dayGroups := make([]dayGroup, 0, len(days))
	for _, d := range days {
		dayGroups = append(dayGroups, dayGroup{Date: d.Date, Count: d.Count})
	}
	owners, err := h.ownerChoices(r, f)
	if err != nil {
		return indexView{}, err
	}

	return indexView{
		Total: total, ChunkSize: h.chunkSize,
		TilesURL: filterURL("/tiles", f), HomeURL: filterURL("/", f),
		DayGroups: dayGroups, OpenIndex: openIndex,
		Filtered:    len(f.Owners) > 0,
		Owners:      owners,
		AuthEnabled: h.auth != nil,
	}, nil
}

// listMedia はオフセット指定で1チャンク分を組み立てる。0件でも nil ではなく
// 空のスライスを返すので、JSONは null ではなく [] になる。
//
// 写真のページの URL は絞り込みを引き継ぐ。個別表示で送っても閉じても、
// 選んだ人の写真の中に留まる。
func (h *Handler) listMedia(r *http.Request, f store.Filter, offset, limit int) ([]mediaView, error) {
	items, err := h.store.ListRange(r.Context(), f, offset, limit)
	if err != nil {
		return nil, err
	}

	// タイルのURLは出どころによらず /thumb/ である。どのファイルを出すかは
	// ハンドラが調べるので、一覧の組み立てではファイルシステムを叩かない。
	out := make([]mediaView, 0, len(items))
	for _, m := range items {
		out = append(out, mediaView{
			ID:       m.ID(),
			PageURL:  filterURL("/item/"+m.ID(), f),
			FullURL:  "/full/" + m.ID(),
			ThumbURL: "/thumb/" + m.ID(),
			InfoURL:  "/info/" + m.ID(),
			Date:     m.Day(),
			IsVideo:  imagefmt.IsVideo(m.Path()),
		})
	}
	return out, nil
}
