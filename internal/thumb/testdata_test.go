package thumb

import (
	"bytes"
	"encoding/binary"
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

// writeJPEGWithOrientation は IFD0 に Orientation タグだけを持つJPEGを書き出す。
// orientation が 0 のときはEXIFを一切付けない。
//
// TIFFブロックを手で組み立ててAPP1としてSOI直後に差し込む。
// internal/takenat/testdata_test.go と同じ手口。
func writeJPEGWithOrientation(t *testing.T, dir, name string, img image.Image, orientation uint16) string {
	t.Helper()

	var body bytes.Buffer
	require.NoError(t, jpeg.Encode(&body, img, &jpeg.Options{Quality: 100}))

	out := body.Bytes()
	if orientation != 0 {
		le := binary.LittleEndian
		var tiff bytes.Buffer
		tiff.WriteString("II")              // リトルエンディアン
		binary.Write(&tiff, le, uint16(42)) // TIFFマジック
		binary.Write(&tiff, le, uint32(8))  // IFD0のオフセット

		binary.Write(&tiff, le, uint16(1))      // IFD0: エントリ1件
		binary.Write(&tiff, le, uint16(0x0112)) // Orientation
		binary.Write(&tiff, le, uint16(3))      // SHORT
		binary.Write(&tiff, le, uint32(1))      // 個数
		binary.Write(&tiff, le, orientation)    // 4バイトの値欄に直接埋める
		binary.Write(&tiff, le, uint16(0))      // 値欄の余り
		binary.Write(&tiff, le, uint32(0))      // 次のIFDなし

		var app1 bytes.Buffer
		app1.Write([]byte{0xFF, 0xE1})
		binary.Write(&app1, binary.BigEndian, uint16(2+6+tiff.Len()))
		app1.WriteString("Exif\x00\x00")
		app1.Write(tiff.Bytes())

		out = append([]byte{0xFF, 0xD8}, app1.Bytes()...)
		out = append(out, body.Bytes()[2:]...) // SOIを除いた残りを連結
	}

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, out, 0o644))
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
