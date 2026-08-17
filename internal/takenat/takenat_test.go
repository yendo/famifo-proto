package takenat

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveUsesEXIFWhenPresent(t *testing.T) {
	dir := t.TempDir()
	want := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	path := writeJPEGWithEXIF(t, dir, "a.jpg", want)
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	require.True(t, got.Equal(want), "EXIFの撮影日時を優先する: got=%v want=%v", got, want)
}

func TestResolveFallsBackToModTimeWhenNoEXIF(t *testing.T) {
	dir := t.TempDir()
	path := writeJPEGWithoutEXIF(t, dir, "a.jpg")
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	require.True(t, got.Equal(mtime))
}

func TestResolveFallsBackForNonEXIFFormats(t *testing.T) {
	dir := t.TempDir()
	// GIFやWebPはEXIFを持たない。デコードが失敗してもmtimeで必ず決まること。
	path := filepath.Join(dir, "a.gif")
	require.NoError(t, os.WriteFile(path, []byte("GIF89a not really a gif"), 0o644))
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	require.True(t, got.Equal(mtime))
}

func TestResolveFallsBackWhenFileMissing(t *testing.T) {
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(filepath.Join(t.TempDir(), "nope.jpg"), mtime)

	require.True(t, got.Equal(mtime))
}
