// Package thumb は一覧表示用のサムネイルを生成する。出力は常にJPEG。
//
// 生成にHEICは来ない（自前ではデコードしない方針）。Synologyが @eaDir に持つ
// サムネイルを借りる経路は internal/synology が扱う。
package thumb

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	_ "image/gif" // image.Decode にGIFを登録する
	_ "image/png" // image.Decode にPNGを登録する

	"github.com/evanoberholster/imagemeta"
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

// CachePath は自前で生成したサムネイルのパスを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func CachePath(dir, id string) string { return filepath.Join(dir, id[:2], id+".jpg") }

// Path はサムネイルの絶対パスを返す。
func (g *Generator) Path(id string) string { return CachePath(g.dir, id) }

// Generate は srcPath の画像からサムネイルを作る。
// デコードできないファイルはエラーを返し、キャッシュには何も残さない。
//
// 既にサムネイルがあり、元の写真より新しければ何もしない。DBを作り直すたびに
// 全件を作り直すと4,495枚で37分（NASなら数時間）かかるが、その大半は中身の
// 変わらないサムネイルの再生成である。
func (g *Generator) Generate(srcPath, id string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("画像を開けません: %w", err)
	}
	defer f.Close()

	if fi, err := f.Stat(); err == nil && g.isFresh(id, fi.ModTime()) {
		return nil
	}

	// 画素より先にEXIFを読む。image.Decode はEXIFを見ずに生の画素を返し、
	// jpeg.Encode はEXIFを書き出さないため、ここで回転を適用しないと向きの
	// 情報はサムネイルから完全に失われる。
	orientation := orientationOf(f)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("画像を読み直せません: %w", err)
	}

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

	// 縮小してから回転する。長辺基準の縮小なので順序で結果の寸法は変わらないが、
	// 4032x3024ではなく480x360を回すぶん安く済む。
	dst := applyOrientation(scaleToFit(src, g.size), orientation)
	if err := jpeg.Encode(tmp, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
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

// isFresh は既存のサムネイルが元の写真より新しいかを返す。
//
// 出力は一時ファイルへ書いてからrenameしているので、中途半端な内容が
// 残っていることはない。存在して新しければ、そのまま使ってよい。
func (g *Generator) isFresh(id string, srcModTime time.Time) bool {
	fi, err := os.Stat(g.Path(id))
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return fi.ModTime().After(srcModTime)
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

// orientationOf はEXIFのOrientationを返す。読めない場合や値が範囲外の場合は
// 1（回転不要）を返す。EXIFを持たない画像は普通に存在するのでエラーにしない。
//
// imagemeta はISOBMFFとPNGのパスをrecoverで囲っていない。サムネイル生成の
// 失敗はその写真をインデックスから落とすため、パニックでデーモンごと落ちる
// のは避ける。internal/takenat と同じ理由のガード。
func orientationOf(r io.ReadSeeker) (o uint16) {
	defer func() {
		if recover() != nil {
			o = 1
		}
	}()
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 1
	}
	ex, err := imagemeta.Decode(r)
	if err != nil {
		return 1
	}
	if v := uint16(ex.IFD0.Orientation); v >= 1 && v <= 8 {
		return v
	}
	return 1
}

// applyOrientation はEXIFのOrientationに従って画素を並べ替える。
func applyOrientation(src image.Image, o uint16) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w // 5..8 は縦横が入れ替わる
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			sx, sy := sourcePixel(x, y, w, h, o)
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// sourcePixel は表示後の(x,y)に対応する元画像の座標を返す。w,hは元画像の寸法。
//
// EXIFのOrientationは「元データの0行目/0列目が表示上のどこへ来るか」を表す。
// 6なら Right-Top、つまり0行目が右端・0列目が上端に来る（右90度回転）。
func sourcePixel(x, y, w, h int, o uint16) (int, int) {
	switch o {
	case 2: // Top-Right: 左右反転
		return w - 1 - x, y
	case 3: // Bottom-Right: 180度
		return w - 1 - x, h - 1 - y
	case 4: // Bottom-Left: 上下反転
		return x, h - 1 - y
	case 5: // Left-Top: 転置
		return y, x
	case 6: // Right-Top: 右90度
		return y, h - 1 - x
	case 7: // Right-Bottom: 逆転置
		return w - 1 - y, h - 1 - x
	case 8: // Left-Bottom: 右270度
		return w - 1 - y, x
	}
	return x, y
}
