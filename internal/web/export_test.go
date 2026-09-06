package web

// SetChunkSize は塊の大きさを上書きする。テスト専用で、本番のビルドには含まれない。
//
// テストは本番の defaultChunkSize より小さい値を使う。塊の境界を跨ぐ挙動を
// 確かめるには塊が2つ以上要るが、本番の大きさのままだと写真を何百枚も用意
// することになるため。速さのための上書きではなく、値そのものがテストの前提を作る。
//
// ハンドラは別goroutineから chunkSize を読むので、Handler() を配信に出す前に
// のみ呼ぶこと。
func (s *Server) SetChunkSize(n int) { s.chunkSize = n }
