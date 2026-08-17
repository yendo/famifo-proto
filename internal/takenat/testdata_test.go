package takenat

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeJPEGWithEXIF はDateTimeOriginalを持つ最小のJPEGを書き出す。
// TIFFブロックを手で組み立て、APP1セグメントとしてSOI直後に差し込んでいる。
func writeJPEGWithEXIF(t *testing.T, dir, name string, when time.Time) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var body bytes.Buffer
	require.NoError(t, jpeg.Encode(&body, img, nil))

	le := binary.LittleEndian
	var tiff bytes.Buffer
	tiff.WriteString("II")              // リトルエンディアン
	binary.Write(&tiff, le, uint16(42)) // TIFFマジック
	binary.Write(&tiff, le, uint32(8))  // IFD0のオフセット

	binary.Write(&tiff, le, uint16(1))      // IFD0: エントリ1件
	binary.Write(&tiff, le, uint16(0x8769)) // ExifIFDPointer
	binary.Write(&tiff, le, uint16(4))      // LONG
	binary.Write(&tiff, le, uint32(1))
	binary.Write(&tiff, le, uint32(26)) // ExifIFDのオフセット
	binary.Write(&tiff, le, uint32(0))  // 次のIFDなし

	binary.Write(&tiff, le, uint16(1))      // ExifIFD: エントリ1件
	binary.Write(&tiff, le, uint16(0x9003)) // DateTimeOriginal
	binary.Write(&tiff, le, uint16(2))      // ASCII
	binary.Write(&tiff, le, uint32(20))
	binary.Write(&tiff, le, uint32(44)) // 値のオフセット
	binary.Write(&tiff, le, uint32(0))  // 次のIFDなし

	tiff.WriteString(when.Format("2006:01:02 15:04:05"))
	tiff.WriteByte(0)

	var app1 bytes.Buffer
	app1.Write([]byte{0xFF, 0xE1})
	binary.Write(&app1, binary.BigEndian, uint16(2+6+tiff.Len()))
	app1.WriteString("Exif\x00\x00")
	app1.Write(tiff.Bytes())

	out := append([]byte{0xFF, 0xD8}, app1.Bytes()...)
	out = append(out, body.Bytes()[2:]...) // SOIを除いた残りを連結

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, out, 0o644))
	return path
}

// writeJPEGWithoutEXIF はEXIFを持たないJPEGを書き出す。
func writeJPEGWithoutEXIF(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}
