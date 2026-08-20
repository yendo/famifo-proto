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
)

//go:embed templates static
var assets embed.FS

// Server はギャラリーのHTTPハンドラ群を保持する。
type Server struct {
	st       *store.Store
	tmpl     *template.Template
	thumbDir string
	pageSize int
	log      *slog.Logger
}

// NewServer はテンプレートを読み込んでServerを作る。
// pageSize は一覧1ページあたりの枚数。
func NewServer(st *store.Store, thumbDir string, pageSize int, log *slog.Logger) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("テンプレートを読み込めません: %w", err)
	}
	return &Server{st: st, tmpl: tmpl, thumbDir: thumbDir, pageSize: pageSize, log: log}, nil
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
	mux.HandleFunc("GET /dates", s.handleDates)
	mux.HandleFunc("GET /thumb/{id}", s.handleThumb)
	mux.HandleFunc("GET /photo/{id}", s.handlePhoto)
	return mux
}
