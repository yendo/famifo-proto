package photo_test

import (
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/photo"
)

func TestIDForIsStableAndDistinct(t *testing.T) {
	a := photo.IDFor("/photos/a.jpg")

	require.Len(t, a, 32)
	require.Equal(t, a, photo.IDFor("/photos/a.jpg"))
	require.NotEqual(t, a, photo.IDFor("/photos/b.jpg"))
}

// fakeFileInfo は New が読む ModTime と Size だけを持つ fs.FileInfo。
// 他のメソッドが呼ばれたら、埋め込んだ nil で落ちるので気づける。
type fakeFileInfo struct {
	fs.FileInfo
	modTime time.Time
	size    int64
}

func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) Size() int64        { return f.size }

var testModTime = time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

func TestNewFillsTheFieldsFromThePathAndFileInfo(t *testing.T) {
	const path = "/photos/A.JPG"

	p := photo.New(path, fakeFileInfo{modTime: testModTime, size: 1234}, time.Time{})

	require.Equal(t, photo.IDFor(path), p.ID(), "IDはパスから導く")
	require.Equal(t, path, p.Path())
	require.Equal(t, int64(1234), p.Size())
	require.True(t, p.ModTime().Equal(testModTime))
}
