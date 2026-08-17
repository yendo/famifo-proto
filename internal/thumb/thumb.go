// Package thumb は一覧表示用のサムネイルを生成・管理する。
// 出力は常にJPEG。HEICはデコードしない方針のためここには来ない。
package thumb

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io/fs"
	"os"
	"path/filepath"

	_ "image/gif" // image.Decode にGIFを登録する
	_ "image/png" // image.Decode にPNGを登録する

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // image.Decode にWebPを登録する（デコードのみ）
)

// jpegQuality はサムネイルの画質。一覧表示に十分で、かつ十分軽い値。
const jpegQuality = 82

// Generator はサムネイルキャッシュを管理する。
type Generator struct {
	dir  string
	size int // 長辺の最大ピクセル数
}

// NewGenerator はキャッシュディレクトリを用意してGeneratorを返す。
func NewGenerator(dir string, size int) (*Generator, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("サムネイルディレクトリを作れません: %w", err)
	}
	return &Generator{dir: dir, size: size}, nil
}

// RelPath はキャッシュ内の相対パスを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func RelPath(id string) string { return filepath.Join(id[:2], id+".jpg") }

// Path はサムネイルの絶対パスを返す。
func (g *Generator) Path(id string) string { return filepath.Join(g.dir, RelPath(id)) }

// Generate は srcPath の画像からサムネイルを作る。
// デコードできないファイルはエラーを返し、キャッシュには何も残さない。
func (g *Generator) Generate(srcPath, id string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("画像を開けません: %w", err)
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return fmt.Errorf("画像をデコードできません: %w", err)
	}

	out := g.Path(id)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("サムネイルの保存先を作れません: %w", err)
	}

	// 一時ファイルに書いてからrenameする。生成途中のファイルをHTTPハンドラが
	// 掴んでしまわないようにするため。
	tmp, err := os.CreateTemp(filepath.Dir(out), ".tmp-*")
	if err != nil {
		return fmt.Errorf("一時ファイルを作れません: %w", err)
	}
	defer os.Remove(tmp.Name()) // renameが成功していれば消す対象は無い

	if err := jpeg.Encode(tmp, scaleToFit(src, g.size), &jpeg.Options{Quality: jpegQuality}); err != nil {
		tmp.Close()
		return fmt.Errorf("サムネイルを書き出せません: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("一時ファイルを閉じられません: %w", err)
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return fmt.Errorf("サムネイルを配置できません: %w", err)
	}
	return nil
}

// Remove はサムネイルを削除する。存在しない場合はエラーにしない。
func (g *Generator) Remove(id string) error {
	if err := os.Remove(g.Path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("サムネイルを削除できません: %w", err)
	}
	return nil
}

// scaleToFit は長辺が max 以下になるよう縮小する。元より大きくは引き伸ばさない。
func scaleToFit(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	if w >= h {
		w, h = max, h*max/w
	} else {
		w, h = w*max/h, max
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}
