package web

// SetChunkSize は塊の大きさを上書きする。テスト専用で、本番のビルドには含まれない。
//
// テストは本番の defaultChunkSize より小さい値を使う。塊の境界を跨ぐ挙動を
// 確かめるには塊が2つ以上要るが、本番の大きさのままだと写真を何百枚も用意
// することになるため。ハンドラは要求のたびに chunkSize を読むので、要求を
// 流し始める前にのみ呼ぶこと。
//
// NewHandler の引数で受け取る形にはしない。120は表示の寸法から測って決めた
// ギャラリー側の事情で、呼び出し側（main）が選ぶものではない。
func (h *Handler) SetChunkSize(n int) { h.chunkSize = n }
