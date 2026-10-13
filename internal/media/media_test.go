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

func TestNewUsesTheCaptureTimeWhenPresent(t *testing.T) {
	t.Parallel()
	takenAt := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)

	m, err := media.New("/items/a.jpg", takenAt, testModTime)

	require.NoError(t, err)
	require.True(t, m.TakenAt().Equal(takenAt), "the capture time wins: got=%v", m.TakenAt())
}

func TestNewFallsBackToModTimeWithoutACaptureTime(t *testing.T) {
	t.Parallel()
	m, err := media.New("/items/a.jpg", time.Time{}, testModTime)

	require.NoError(t, err)
	require.True(t, m.TakenAt().Equal(testModTime), "a item with no capture time still stays in the gallery")
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
	takenAt := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)

	m := media.Restore(path, takenAt, testModTime)

	require.Equal(t, media.IDFor(path), m.ID())
	require.Equal(t, path, m.Path())
	require.True(t, m.TakenAt().Equal(takenAt))
	require.True(t, m.ModTime().Equal(testModTime))
}
