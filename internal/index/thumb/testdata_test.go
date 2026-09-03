package thumb

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// markerJPEG は向きの検証用に、左上の四分割だけを赤で塗った画像を返す。
// 縦横の長さを変えてあるので、回転で寸法が入れ替わったかどうかも見分けられる。
func markerJPEG(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{A: 255} // 黒
			if x < w/2 && y < h/2 {
				c = color.RGBA{R: 255, A: 255} // 左上だけ赤
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// writeJPEGImage は与えた画像をJPEGとして書き出す。
// 書き出したものの画素を見て検証するので、劣化を抑えるため最高品質で書く。
func writeJPEGImage(t *testing.T, dir, name string, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

// isRed はJPEGの劣化を見込んで、その画素が赤かどうかを大雑把に判定する。
func isRed(t *testing.T, img image.Image, x, y int) bool {
	t.Helper()
	r, g, b, _ := img.At(x, y).RGBA()
	return r>>8 > 128 && g>>8 < 128 && b>>8 < 128
}

func decodeThumbImage(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	img, err := jpeg.Decode(f)
	require.NoError(t, err)
	return img
}
