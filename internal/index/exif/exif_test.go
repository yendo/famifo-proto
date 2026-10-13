package exif_test

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
	"github.com/yendo/famifo-proto/internal/index/exif"
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

// writeJPEGWithEXIFOffset は DateTimeOriginal に加えて OffsetTimeOriginal を
// 持つJPEGを書き出す。時差を明示している写真（新しめのiPhone等）を再現する。
//
// バイト配置:
//
//	 0 ヘッダ "II" + マジック42 + IFD0オフセット8
//	 8 IFD0    : エントリ1件(ExifIFDPointer) + 次IFDなし → 26で終わる
//	26 ExifIFD : エントリ2件 + 次IFDなし → 56で終わる
//	56 DateTimeOriginal の値 (20バイト)
//	76 OffsetTimeOriginal の値 (7バイト)
func writeJPEGWithEXIFOffset(t *testing.T, dir, name string, when time.Time, offset string) string {
	t.Helper()
	require.Len(t, offset, 6, `the offset has to be given as "+09:00"`)

	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var body bytes.Buffer
	require.NoError(t, jpeg.Encode(&body, img, nil))

	le := binary.LittleEndian
	var tiff bytes.Buffer
	tiff.WriteString("II")
	binary.Write(&tiff, le, uint16(42))
	binary.Write(&tiff, le, uint32(8))

	binary.Write(&tiff, le, uint16(1))      // IFD0: エントリ1件
	binary.Write(&tiff, le, uint16(0x8769)) // ExifIFDPointer
	binary.Write(&tiff, le, uint16(4))      // LONG
	binary.Write(&tiff, le, uint32(1))
	binary.Write(&tiff, le, uint32(26))
	binary.Write(&tiff, le, uint32(0))

	binary.Write(&tiff, le, uint16(2))      // ExifIFD: エントリ2件
	binary.Write(&tiff, le, uint16(0x9003)) // DateTimeOriginal
	binary.Write(&tiff, le, uint16(2))      // ASCII
	binary.Write(&tiff, le, uint32(20))
	binary.Write(&tiff, le, uint32(56))
	binary.Write(&tiff, le, uint16(0x9011)) // OffsetTimeOriginal
	binary.Write(&tiff, le, uint16(2))      // ASCII
	binary.Write(&tiff, le, uint32(7))
	binary.Write(&tiff, le, uint32(76))
	binary.Write(&tiff, le, uint32(0)) // 次のIFDなし

	tiff.WriteString(when.Format("2006:01:02 15:04:05"))
	tiff.WriteByte(0)
	tiff.WriteString(offset)
	tiff.WriteByte(0)

	var app1 bytes.Buffer
	app1.Write([]byte{0xFF, 0xE1})
	binary.Write(&app1, binary.BigEndian, uint16(2+6+tiff.Len()))
	app1.WriteString("Exif\x00\x00")
	app1.Write(tiff.Bytes())

	out := append([]byte{0xFF, 0xD8}, app1.Bytes()...)
	out = append(out, body.Bytes()[2:]...)

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, out, 0o644))
	return path
}

// writeJPEGWithOrientation は IFD0 に Orientation タグだけを持つJPEGを書き出す。
// orientation が 0 のときはEXIFを一切付けない。
func writeJPEGWithOrientation(t *testing.T, dir, name string, orientation uint16) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var body bytes.Buffer
	require.NoError(t, jpeg.Encode(&body, img, nil))

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

func TestReadReturnsTheEXIFDate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeJPEGWithEXIF(t, dir, "a.jpg", time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC))

	got := exif.Read(path)

	// 時計の値で比べる。TakenAt の Location は時差タグの有無を映すだけで、
	// 瞬間としては使わない。
	require.Equal(t, "2021-03-04 05:06:07", got.TakenAt.Format("2006-01-02 15:04:05"))
}

// EXIFのDateTimeOriginalは時差を持たない。サーバのタイムゾーンで解釈し直すと、
// 文字盤の値がそのタイムゾーンの瞬間にすり替わる。時計の値はそのまま返す。
func TestReadKeepsTheDialTimeWhenTheEXIFDateHasNoOffset(t *testing.T) {
	// time.Local はプロセス全体で1つしかない。書き換えるテストが並列に走ると、
	// 同時に走っている他のテストの時刻解釈まで巻き添えで変わる。実際 -race が
	// 競合として検出する。このテストは t.Parallel() を呼ばない。
	// TZ=UTC の環境では付け替えても時計の値が変わらず、回帰を検出できないため
	// JST にする。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })
	path := writeJPEGWithEXIF(t, t.TempDir(), "a.jpg", time.Date(2025, 5, 24, 9, 8, 31, 0, time.UTC))

	got := exif.Read(path)

	require.Equal(t, "2025-05-24 09:08:31", got.TakenAt.Format("2006-01-02 15:04:05"),
		"the dial time is kept as it is: got=%v", got.TakenAt)
}

// OffsetTimeOriginal がある写真も、撮影地の時計の値をそのまま返す。
// サーバの時差で見直すと、旅行先の夜に撮った写真が日本の翌日に回る。
func TestReadKeepsTheEXIFOffset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// ベルリン(+02:00)で正午に撮った写真
	path := writeJPEGWithEXIFOffset(t, dir, "a.jpg",
		time.Date(2023, 8, 4, 12, 0, 0, 0, time.UTC), "+02:00")

	got := exif.Read(path)

	require.Equal(t, "2023-08-04 12:00:00", got.TakenAt.Format("2006-01-02 15:04:05"),
		"still noon where it was shot: got=%v", got.TakenAt)
}

func TestReadReturnsZeroDateWithoutEXIF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeJPEGWithoutEXIF(t, dir, "a.jpg")

	got := exif.Read(path)

	require.True(t, got.TakenAt.IsZero(), "tells the caller there is no capture time")
	require.Equal(t, uint16(1), got.Orientation, "no rotation needed")
}

func TestReadReturnsTheOrientation(t *testing.T) {
	t.Parallel()
	for _, o := range []uint16{1, 2, 3, 4, 5, 6, 7, 8} {
		t.Run(string(rune('0'+o)), func(t *testing.T) {
			path := writeJPEGWithOrientation(t, t.TempDir(), "a.jpg", o)

			require.Equal(t, o, exif.Read(path).Orientation)
		})
	}
}

func TestReadFallsBackForUnreadableFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// GIFやWebPはEXIFを持たない。デコードが失敗しても既定値で返ること。
	broken := filepath.Join(dir, "a.gif")
	require.NoError(t, os.WriteFile(broken, []byte("GIF89a not really a gif"), 0o644))

	for _, path := range []string{broken, filepath.Join(dir, "nope.jpg")} {
		got := exif.Read(path)

		require.True(t, got.TakenAt.IsZero(), "path=%s", path)
		require.Equal(t, uint16(1), got.Orientation, "path=%s", path)
	}
}
