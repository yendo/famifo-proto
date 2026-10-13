package media_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/media"
)

func TestIDForIsStableAndDistinct(t *testing.T) {
	t.Parallel()
	a := media.IDFor("/items/a.jpg")

	require.Len(t, a, 32)
	require.Equal(t, a, media.IDFor("/items/a.jpg"))
	require.NotEqual(t, a, media.IDFor("/items/b.jpg"))
}

func TestIsCandidate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"/items/a.jpg", true},
		{"/items/A.JPG", true},
		{"/items/clip.mov", true},
		{"/items/notes.txt", false},
		{"/items/noext", false},
		// Synologyが写真の隣に作るサムネイル。拡張子は写真と同じ
		{"/items/@eaDir/a.jpg/SYNOPHOTO_THUMB_M.jpg", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, media.IsCandidate(tc.path))
		})
	}
}

var testModTime = time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

func TestNewFillsTheFields(t *testing.T) {
	t.Parallel()
	const path = "/items/A.JPG"

	m, err := media.New(path, time.Time{}, testModTime)

	require.NoError(t, err)
	require.Equal(t, media.IDFor(path), m.ID(), "the ID is derived from the path")
	require.Equal(t, path, m.Path())
	require.True(t, m.ModTime().Equal(testModTime))
}

// issue #44 のパリの例。時差タグのある写真は撮影地の時計の値で入り、
// サーバのタイムゾーンによらず撮影地の日になる。
func TestNewKeepsTheLocalTimeWhereItWasShot(t *testing.T) {
	t.Parallel()
	paris := time.FixedZone("CET", 60*60)
	takenAt := time.Date(2026, 3, 1, 21, 0, 0, 0, paris)

	m, err := media.New("/items/a.jpg", takenAt, testModTime)

	require.NoError(t, err)
	require.Equal(t, "2026-03-01T21:00:00", m.TakenAtLocal())
	require.Equal(t, "2026-03-01", m.Day())
}

// 時差の分からない値は UTC のラベル付きで届く。ラベルは無視して時計の値を使う。
func TestNewKeepsTheDialTimeOfAnUnknownOffset(t *testing.T) {
	t.Parallel()
	takenAt := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)

	m, err := media.New("/items/a.jpg", takenAt, testModTime)

	require.NoError(t, err)
	require.Equal(t, "2021-03-04T05:06:07", m.TakenAtLocal())
}

func TestNewFallsBackToModTimeWithoutACaptureTime(t *testing.T) {
	t.Parallel()
	m, err := media.New("/items/a.jpg", time.Time{}, testModTime)

	require.NoError(t, err)
	// mtimeは瞬間なので、サーバの時刻で見た時計の値になる
	require.Equal(t, testModTime.Local().Format("2006-01-02T15:04:05"), m.TakenAtLocal(),
		"a item with no capture time still stays in the gallery")
}

func TestNewRejectsAPathThatIsNotACandidate(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/items/notes.txt",
		"/items/@eaDir/a.jpg/SYNOPHOTO_THUMB_M.jpg",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			_, err := media.New(path, time.Time{}, testModTime)

			require.Error(t, err)
		})
	}
}

func TestRestoreKeepsTheStoredValues(t *testing.T) {
	t.Parallel()
	const path = "/items/a.jpg"

	m := media.Restore(path, "2021-03-04T05:06:07", testModTime)

	require.Equal(t, media.IDFor(path), m.ID())
	require.Equal(t, path, m.Path())
	require.Equal(t, "2021-03-04T05:06:07", m.TakenAtLocal())
	require.Equal(t, "2021-03-04", m.Day())
	require.True(t, m.ModTime().Equal(testModTime))
}
