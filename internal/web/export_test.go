package web

import (
	"log/slog"
	"net/http"

	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// NewHandlerWithChunkSize は塊の大きさを n にして NewHandler と同じものを返す。
// テスト専用で、本番のビルドには含まれない。
//
// テストは本番の defaultChunkSize より小さい値を使う。塊の境界を跨ぐ挙動を
// 確かめるには塊が2つ以上要るが、本番の大きさのままだと写真を何百枚も用意
// することになるため。速さのための上書きではなく、値そのものがテストの前提を作る。
func NewHandlerWithChunkSize(st *store.Store, thumbs *thumb.Provider, auth *Auth, log *slog.Logger, n int) (http.Handler, error) {
	a, err := newApp(st, thumbs, auth, log)
	if err != nil {
		return nil, err
	}
	a.chunkSize = n
	return a.handler(), nil
}
