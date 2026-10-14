package media_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/media"
)

// IDはパスだけから決まる。撮影日時やmtimeが変わっても同じ1件として追跡される。
func TestIDIsStableAndDistinct(t *testing.T) {
	t.Parallel()
	a, err := media.New("/items/a.jpg", 0, time.Time{}, testModTime)
	require.NoError(t, err)
	again, err := media.New("/items/a.jpg", 0, time.Time{}, testModTime.Add(time.Hour))
	require.NoError(t, err)
	b, err := media.New("/items/b.jpg", 0, time.Time{}, testModTime)
	require.NoError(t, err)

	require.Len(t, a.ID(), 32)
	require.Equal(t, a.ID(), again.ID())
	require.NotEqual(t, a.ID(), b.ID())
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

	m, err := media.New(path, 0, time.Time{}, testModTime)

	require.NoError(t, err)
	require.Equal(t, path, m.Path())
	require.True(t, m.ModTime().Equal(testModTime))
}

// issue #44 のパリの例。時差タグのある写真は撮影地の時刻で入り、
// サーバのタイムゾーンによらず撮影地の日になる。
func TestNewKeepsTheLocalTimeWhereItWasShot(t *testing.T) {
	t.Parallel()
	paris := time.FixedZone("CET", 60*60)
	takenAt := time.Date(2026, 3, 1, 21, 0, 0, 0, paris)

	m, err := media.New("/items/a.jpg", 0, takenAt, testModTime)

	require.NoError(t, err)
	require.Equal(t, "2026-03-01T21:00:00", m.TakenAtLocal())
	require.Equal(t, "2026-03-01", m.Day())
}

// 時差の分からない値は UTC のラベル付きで届く。ラベルは無視して撮影地の時刻として使う。
func TestNewKeepsTheLocalTimeOfAnUnknownOffset(t *testing.T) {
	t.Parallel()
	takenAt := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)

	m, err := media.New("/items/a.jpg", 0, takenAt, testModTime)

	require.NoError(t, err)
	require.Equal(t, "2021-03-04T05:06:07", m.TakenAtLocal())
}

func TestNewFallsBackToModTimeWithoutACaptureTime(t *testing.T) {
	t.Parallel()
	m, err := media.New("/items/a.jpg", 0, time.Time{}, testModTime)

	require.NoError(t, err)
	// mtimeは瞬間なので、サーバの time.Local の時刻になる
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
			_, err := media.New(path, 0, time.Time{}, testModTime)

			require.Error(t, err)
		})
	}
}

// 所有者はそのまま持つ。撮影者の判別に使う。
func TestNewKeepsTheOwner(t *testing.T) {
	t.Parallel()

	m, err := media.New("/items/a.jpg", 1000, time.Time{}, testModTime)

	require.NoError(t, err)
	require.Equal(t, uint32(1000), m.OwnerUID())
}

func TestRestoreKeepsTheStoredValues(t *testing.T) {
	t.Parallel()
	const path = "/items/a.jpg"

	m := media.Restore(path, 1000, "2021-03-04T05:06:07", testModTime)

	require.Equal(t, path, m.Path())
	require.Equal(t, uint32(1000), m.OwnerUID())
	require.Equal(t, "2021-03-04T05:06:07", m.TakenAtLocal())
	require.Equal(t, "2021-03-04", m.Day())
	require.True(t, m.ModTime().Equal(testModTime))
}
