package photo_test

import (
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/photo"
)

func TestIDForIsStableAndDistinct(t *testing.T) {
	t.Parallel()
	a := photo.IDFor("/photos/a.jpg")

	require.Len(t, a, 32)
	require.Equal(t, a, photo.IDFor("/photos/a.jpg"))
	require.NotEqual(t, a, photo.IDFor("/photos/b.jpg"))
}

// fakeFileInfo は New が読む ModTime だけを持つ fs.FileInfo。
// 他のメソッドが呼ばれたら、埋め込んだ nil で落ちるので気づける。
type fakeFileInfo struct {
	fs.FileInfo
	modTime time.Time
}

func (f fakeFileInfo) ModTime() time.Time { return f.modTime }

var testModTime = time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

func TestNewFillsTheFieldsFromThePathAndFileInfo(t *testing.T) {
	t.Parallel()
	const path = "/photos/A.JPG"

	p := photo.New(path, fakeFileInfo{modTime: testModTime}, time.Time{})

	require.Equal(t, photo.IDFor(path), p.ID(), "IDはパスから導く")
	require.Equal(t, path, p.Path())
	require.True(t, p.ModTime().Equal(testModTime))
}
