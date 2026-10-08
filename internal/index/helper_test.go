package index_test

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// testJPEG は指定サイズのJPEGのバイト列を返す。
func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil))
	return buf.Bytes()
}

// writeTestJPEG は指定サイズのJPEGを書き出してそのパスを返す。
func writeTestJPEG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	buf := bytes.NewBuffer(testJPEG(t, w, h))
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

// testHEIC は ftyp の箱だけを持つ最小のHEICのバイト列を返す。中身の画素は無い。
// famifoはHEICをデコードしないので、取り込みに要るのは署名だけである。
func testHEIC() []byte {
	return append([]byte{0, 0, 0, 0x10}, []byte("ftypheic\x00\x00\x00\x00")...)
}

// testMP4Stub は ftyp の箱だけを持つmp4のバイト列を返す。署名はあるが moov が
// 無いので、撮影日時は読めない。「コンテナとして壊れている動画」を作るのに使う。
func testMP4Stub() []byte {
	return append([]byte{0, 0, 0, 0x10}, []byte("ftypisom\x00\x00\x00\x00")...)
}
