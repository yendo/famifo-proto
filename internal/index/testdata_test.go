package index_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeTestJPEG は指定サイズのJPEGを書き出してそのパスを返す。
func writeTestJPEG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

// writeJPEGWithOrientation は IFD0 に Orientation タグだけを持つJPEGを書き出す。
// TIFFブロックを手で組み立ててAPP1としてSOI直後に差し込む。
// internal/index/exif/testdata_test.go と同じ手口。
func writeJPEGWithOrientation(t *testing.T, dir, name string, w, h int, orientation uint16) string {
	t.Helper()

	var body bytes.Buffer
	require.NoError(t, jpeg.Encode(&body, image.NewRGBA(image.Rect(0, 0, w, h)), nil))

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

	out := append([]byte{0xFF, 0xD8}, app1.Bytes()...)
	out = append(out, body.Bytes()[2:]...) // SOIを除いた残りを連結

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, out, 0o644))
	return path
}

// decodeThumbConfig はサムネイルの寸法を読む。
func decodeThumbConfig(t *testing.T, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	require.NoError(t, err)
	return cfg
}
