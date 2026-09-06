// Package web はギャラリーのHTTP配信を担う。
// テンプレートと静的ファイルはバイナリに埋め込む（単一バイナリ配布のため）。
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

//go:embed templates static
var assets embed.FS

// defaultChunkSize は仮想スクロールが1回に取る塊の枚数。
//
// 先頭の1塊は初回HTMLに埋め込む。クライアントは範囲を覆う塊が揃うまで
// 描かないので、この値が「開いた画面 + overscan 4行」に届かないと、開いた
// 直後に取得を1往復待つことになる。1920x950・列幅200pxで先頭に要るのは
// 76枚（実データ4497枚で計測）で、60では足りていなかった。
//
// 利用者が変えられる設定ではない。表示の寸法を変えたときは測り直すこと。
const defaultChunkSize = 120

// Server はギャラリーのHTTPハンドラ群を保持する。
type Server struct {
	st        *store.Store
	tmpl      *template.Template
	thumbs    *thumb.Provider
	chunkSize int
	log       *slog.Logger
}

// NewServer はテンプレートを読み込んでServerを作る。
// 塊の大きさは defaultChunkSize に任せる。利用者が変えられる設定ではない。
//
// thumbs は取り込み側と共有する。配信するファイルの選択はすべてそこが決めるので、
// サーバーはサムネイルの置き場所を知らない。
func NewServer(st *store.Store, thumbs *thumb.Provider, log *slog.Logger) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("テンプレートを読み込めません: %w", err)
	}
	return &Server{st: st, tmpl: tmpl, thumbs: thumbs, chunkSize: defaultChunkSize, log: log}, nil
}

// Handler はルーティング済みのハンドラを返す。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // embedの内容は固定なので、ここで失敗するならビルドの不備
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	mux.HandleFunc("GET /{$}", s.handleGallery)
	mux.HandleFunc("GET /items", s.handleItems)
	mux.HandleFunc("GET /thumb/{id}", s.handleThumb)
	mux.HandleFunc("GET /photo/{id}", s.handlePhoto)
	return mux
}
