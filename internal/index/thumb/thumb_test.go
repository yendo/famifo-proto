package thumb

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testID = "abcdef0123456789abcdef0123456789"

func writeImage(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	switch filepath.Ext(name) {
	case ".png":
		require.NoError(t, png.Encode(&buf, img))
	case ".gif":
		require.NoError(t, gif.Encode(&buf, img, nil))
	default:
		require.NoError(t, jpeg.Encode(&buf, img, nil))
	}
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

func newTestGenerator(t *testing.T, size int) *Generator {
	t.Helper()
	g, err := NewGenerator(filepath.Join(t.TempDir(), "thumbs"), size)
	require.NoError(t, err)
	return g
}

func decodeThumb(t *testing.T, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	require.NoError(t, err)
	require.Equal(t, "jpeg", format, "サムネイルは常にJPEGで書き出す")
	return cfg
}

func TestGenerateScalesLandscapeByLongEdge(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)

	require.NoError(t, g.Generate(src, testID, 1))

	cfg := decodeThumb(t, g.path(testID))
	require.Equal(t, 100, cfg.Width)
	require.Equal(t, 50, cfg.Height, "アスペクト比を保つ")
}

func TestGenerateScalesPortraitByLongEdge(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 200, 400)

	require.NoError(t, g.Generate(src, testID, 1))

	cfg := decodeThumb(t, g.path(testID))
	require.Equal(t, 50, cfg.Width)
	require.Equal(t, 100, cfg.Height)
}

func TestGenerateDoesNotUpscale(t *testing.T) {
	g := newTestGenerator(t, 500)
	src := writeImage(t, t.TempDir(), "a.jpg", 40, 20)

	require.NoError(t, g.Generate(src, testID, 1))

	cfg := decodeThumb(t, g.path(testID))
	require.Equal(t, 40, cfg.Width)
	require.Equal(t, 20, cfg.Height)
}

func TestGenerateAcceptsPNGAndGIF(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.png", "a.gif"} {
		t.Run(name, func(t *testing.T) {
			g := newTestGenerator(t, 100)
			src := writeImage(t, dir, name, 400, 200)

			require.NoError(t, g.Generate(src, testID, 1))

			require.Equal(t, 100, decodeThumb(t, g.path(testID)).Width)
		})
	}
}

func TestGenerateFailsOnUndecodableFile(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := filepath.Join(t.TempDir(), "broken.jpg")
	require.NoError(t, os.WriteFile(src, []byte("this is not an image"), 0o644))

	err := g.Generate(src, testID, 1)

	require.Error(t, err)
	require.NoFileExists(t, g.path(testID), "失敗時に中途半端なファイルを残さない")
}

func TestGenerateFailsOnMissingFile(t *testing.T) {
	g := newTestGenerator(t, 100)

	require.Error(t, g.Generate(filepath.Join(t.TempDir(), "nope.jpg"), testID, 1))
}

func TestRemove(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)
	require.NoError(t, g.Generate(src, testID, 1))

	require.NoError(t, g.Remove(testID))

	require.NoFileExists(t, g.path(testID))
	require.NoError(t, g.Remove(testID), "存在しないサムネイルの削除はエラーにしない")
}

// TestGenerateAppliesOrientation は、渡されたOrientationがサムネイルの
// 画素に実際に適用されることを確かめる。
//
// image.Decode はEXIFを見ずに生の画素をそのまま返し、jpeg.Encode はEXIFを
// 一切書き出さない。したがってサムネイル生成時に回転を適用しないと、回転情報
// はどこにも残らず失われる。実測では手元の4,495枚中1,230枚(27.4%)が
// Orientation 6/8 で、その全てが横倒しになっていた。
//
// 値が実際のEXIFから来ることは internal/index/exif と internal/index が押さえる。
//
// 元画像は 16x8（横長）で左上の四分割だけが赤。回転後にその赤がどの隅へ
// 来るかで、寸法の入れ替えだけでなく画素が本当に動いたかまで見分けられる。
func TestGenerateAppliesOrientation(t *testing.T) {
	tests := []struct {
		name        string
		orientation uint16
		wantW       int
		wantH       int
		markerLeft  bool // 赤が左半分にあるか
		markerTop   bool // 赤が上半分にあるか
	}{
		{"0 不明", 0, 16, 8, true, true},
		{"1 そのまま", 1, 16, 8, true, true},
		{"2 左右反転", 2, 16, 8, false, true},
		{"3 180度", 3, 16, 8, false, false},
		{"4 上下反転", 4, 16, 8, true, false},
		{"5 転置", 5, 8, 16, true, true},
		{"6 90度右", 6, 8, 16, false, true},
		{"7 逆転置", 7, 8, 16, false, false},
		{"8 270度右", 8, 8, 16, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newTestGenerator(t, 100) // 16x8は縮小されないので画素を直接見られる
			src := writeJPEGImage(t, t.TempDir(), "a.jpg", markerJPEG(16, 8))

			require.NoError(t, g.Generate(src, testID, tt.orientation))

			img := decodeThumbImage(t, g.path(testID))
			b := img.Bounds()
			require.Equalf(t, tt.wantW, b.Dx(),
				"Orientation=%d のサムネイルの幅。縦横が入れ替わっていない疑い（実際 %dx%d）",
				tt.orientation, b.Dx(), b.Dy())
			require.Equalf(t, tt.wantH, b.Dy(),
				"Orientation=%d のサムネイルの高さ（実際 %dx%d）", tt.orientation, b.Dx(), b.Dy())

			// 赤があるべき四分割の中心と、その対角の中心を見る。
			markX, markY := quadrantCenter(b.Dx(), b.Dy(), tt.markerLeft, tt.markerTop)
			oppX, oppY := quadrantCenter(b.Dx(), b.Dy(), !tt.markerLeft, !tt.markerTop)

			require.Truef(t, isRed(t, img, markX, markY),
				"Orientation=%d: 赤は%s%sの隅に来るはずだが (%d,%d) が赤くない。"+
					"寸法だけ入れ替えて画素を回していない疑い",
				tt.orientation, side(tt.markerTop, "上", "下"), side(tt.markerLeft, "左", "右"), markX, markY)
			require.Falsef(t, isRed(t, img, oppX, oppY),
				"Orientation=%d: 対角 (%d,%d) が赤い。回転の向きが逆の疑い",
				tt.orientation, oppX, oppY)
		})
	}
}

func quadrantCenter(w, h int, left, top bool) (int, int) {
	x, y := w*3/4, h*3/4
	if left {
		x = w / 4
	}
	if top {
		y = h / 4
	}
	return x, y
}

func side(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// DBを作り直すたびに全サムネイルを作り直すと、4,495枚で37分（NASなら数時間）
// かかる。写真が変わっていないなら既存のものをそのまま使う。
func TestGenerateSkipsWhenTheThumbnailIsUpToDate(t *testing.T) {
	g := newTestGenerator(t, 100)
	dir := t.TempDir()
	src := writeImage(t, dir, "a.jpg", 400, 200)
	require.NoError(t, g.Generate(src, testID, 1))

	// 中身を見分けられる印を置き、元ファイルより新しくする
	marker := []byte("not a real thumbnail")
	require.NoError(t, os.WriteFile(g.path(testID), marker, 0o644))
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(g.path(testID), future, future))

	require.NoError(t, g.Generate(src, testID, 1))

	got, err := os.ReadFile(g.path(testID))
	require.NoError(t, err)
	require.Equal(t, marker, got, "元ファイルが変わっていなければ作り直さない")
}

// 写真が差し替えられたらサムネイルは古い。mtimeで判定する。
func TestGenerateRebuildsWhenTheSourceIsNewer(t *testing.T) {
	g := newTestGenerator(t, 100)
	dir := t.TempDir()
	src := writeImage(t, dir, "a.jpg", 400, 200)
	require.NoError(t, g.Generate(src, testID, 1))
	require.NoError(t, os.WriteFile(g.path(testID), []byte("stale"), 0o644))

	// 元ファイルをサムネイルより新しくする
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(src, future, future))

	require.NoError(t, g.Generate(src, testID, 1))

	cfg := decodeThumb(t, g.path(testID))
	require.Equal(t, 100, cfg.Width, "元が新しければ作り直す")
}
