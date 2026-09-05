package exif_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/index/exif"
)

func TestReadReturnsTheEXIFDate(t *testing.T) {
	dir := t.TempDir()
	when := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	path := writeJPEGWithEXIF(t, dir, "a.jpg", when)

	got := exif.Read(path)

	require.True(t, got.TakenAt.Equal(when), "got=%v", got.TakenAt)
	// 時差を持たないDateTimeOriginalを imagemeta がUTCとして返すことを固定する。
	// internal/index/takenat はこの Location が time.UTC そのものかどうかで、
	// 撮影地の時刻として読み直すかを判別している。
	require.Equal(t, time.UTC, got.TakenAt.Location())
}

func TestReadKeepsTheEXIFOffset(t *testing.T) {
	dir := t.TempDir()
	// ベルリン(+02:00)で正午に撮った写真
	path := writeJPEGWithEXIFOffset(t, dir, "a.jpg",
		time.Date(2023, 8, 4, 12, 0, 0, 0, time.UTC), "+02:00")

	got := exif.Read(path)

	_, off := got.TakenAt.Zone()
	require.Equal(t, 2*60*60, off, "時差が書かれていればそれを保つ: got=%v", got.TakenAt)
	require.Equal(t, 12, got.TakenAt.Hour(), "現地の12時のまま: got=%v", got.TakenAt)
}

func TestReadReturnsZeroDateWithoutEXIF(t *testing.T) {
	dir := t.TempDir()
	path := writeJPEGWithoutEXIF(t, dir, "a.jpg")

	got := exif.Read(path)

	require.True(t, got.TakenAt.IsZero(), "撮影日時が無いことを呼び出し側に伝える")
	require.Equal(t, uint16(1), got.Orientation, "回転不要")
}

func TestReadReturnsTheOrientation(t *testing.T) {
	for _, o := range []uint16{1, 2, 3, 4, 5, 6, 7, 8} {
		t.Run(string(rune('0'+o)), func(t *testing.T) {
			path := writeJPEGWithOrientation(t, t.TempDir(), "a.jpg", o)

			require.Equal(t, o, exif.Read(path).Orientation)
		})
	}
}

func TestReadFallsBackForUnreadableFiles(t *testing.T) {
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
