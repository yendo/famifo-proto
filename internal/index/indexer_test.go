package index

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

type fixture struct {
	ix   *Indexer
	st   *store.Store
	gen  *thumb.Generator
	root string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "photos")
	require.NoError(t, os.MkdirAll(root, 0o755))

	st, err := store.Open(filepath.Join(base, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	gen, err := thumb.NewGenerator(filepath.Join(base, "thumbs"), 100)
	require.NoError(t, err)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{ix: New([]string{root}, st, gen, log), st: st, gen: gen, root: root}
}

// newFixtureRoots は複数のルートを持つ fixture を作る。roots[0] が f.root。
func newFixtureRoots(t *testing.T, names ...string) (*fixture, []string) {
	t.Helper()
	base := t.TempDir()

	roots := make([]string, 0, len(names))
	for _, n := range names {
		r := filepath.Join(base, n)
		require.NoError(t, os.MkdirAll(r, 0o755))
		roots = append(roots, r)
	}

	st, err := store.Open(filepath.Join(base, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	gen, err := thumb.NewGenerator(filepath.Join(base, "thumbs"), 100)
	require.NoError(t, err)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{ix: New(roots, st, gen, log), st: st, gen: gen, root: roots[0]}, roots
}

func TestIndexFileStoresRasterPhotoWithThumb(t *testing.T) {
	f := newFixture(t)
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), photo.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, path, got.Path)
	require.Equal(t, ".jpg", got.Ext)
	require.Equal(t, photo.ThumbFamifo, got.ThumbSource)
	require.FileExists(t, f.gen.Path(got.ID))
}

func TestIndexFileStoresHEICWithoutThumb(t *testing.T) {
	f := newFixture(t)
	// HEICはデコードしない方針なので、中身が画像でなくても登録される
	path := filepath.Join(f.root, "a.heic")
	require.NoError(t, os.WriteFile(path, []byte("not decodable by go"), 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), photo.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, photo.ThumbNone, got.ThumbSource, "HEICはデコードできない")
	require.NoFileExists(t, f.gen.Path(got.ID))
}

func TestIndexFileIgnoresUnsupportedExtensions(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "a.mp4")
	require.NoError(t, os.WriteFile(path, []byte("video"), 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path), "対象外はエラーではない")

	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestIndexFileIgnoresDirectories(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.root, "sub.jpg") // 拡張子付きディレクトリという嫌がらせ
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.NoError(t, f.ix.IndexFile(context.Background(), dir))

	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestIndexFileRejectsBrokenRasterImage(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "broken.jpg")
	require.NoError(t, os.WriteFile(path, []byte("not an image"), 0o644))

	err := f.ix.IndexFile(context.Background(), path)

	require.Error(t, err, "壊れた画像は登録せずエラーを返す")
	n, cerr := f.st.Count(context.Background())
	require.NoError(t, cerr)
	require.Equal(t, 0, n)
}

func TestRemoveFileDeletesRowAndThumb(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)
	require.NoError(t, f.ix.IndexFile(ctx, path))
	thumbPath := f.gen.Path(photo.IDFor(path))
	require.FileExists(t, thumbPath)

	require.NoError(t, f.ix.RemoveFile(ctx, path))

	require.NoFileExists(t, thumbPath)
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestRemoveFileIsQuietForUnknownPath(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.ix.RemoveFile(context.Background(), filepath.Join(f.root, "never.jpg")))
}

// writeSynoThumb は srcPath の写真用のサムネイルを @eaDir に置く。
func writeSynoThumb(t *testing.T, srcPath string) string {
	t.Helper()
	out := synology.ThumbPath(srcPath)
	writeTestJPEG(t, filepath.Dir(out), filepath.Base(out), 20, 10)
	return out
}

func TestIndexFileBorrowsTheSynologyThumbnail(t *testing.T) {
	f := newFixture(t)
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)
	writeSynoThumb(t, path)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), photo.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, photo.ThumbSyno, got.ThumbSource)
	require.NoFileExists(t, f.gen.Path(got.ID), "借りられるなら自前では作らない")
}

// HEICはGoでデコードできないが、Synologyのサムネイルがあれば一覧に出せる。
func TestIndexFileBorrowsTheSynologyThumbnailForHEIC(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "a.heic")
	require.NoError(t, os.WriteFile(path, []byte("not decodable by go"), 0o644))
	writeSynoThumb(t, path)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), photo.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, photo.ThumbSyno, got.ThumbSource)
}

// DSM 7.3 がHEICのデコードに失敗すると .fail だけが残る。famifoも作れないので
// サムネイル無しのまま原本を配信する。.fail を置き換えるのはfamifoの仕事ではない。
func TestIndexFileLeavesHEICWithoutThumbWhenOnlyAFailMarkerIsThere(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "a.heic")
	require.NoError(t, os.WriteFile(path, []byte("not decodable by go"), 0o644))
	fail := filepath.Join(filepath.Dir(synology.ThumbPath(path)), "SYNOPHOTO_THUMB_M.fail")
	require.NoError(t, os.MkdirAll(filepath.Dir(fail), 0o755))
	require.NoError(t, os.WriteFile(fail, nil, 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), photo.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, photo.ThumbNone, got.ThumbSource)
}

// famifoはSynology Photosの領域に書き込まない。消しもしない。
func TestRemoveFileKeepsTheSynologyThumbnail(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)
	synoThumb := writeSynoThumb(t, path)
	require.NoError(t, f.ix.IndexFile(ctx, path))

	require.NoError(t, f.ix.RemoveFile(ctx, path))

	require.FileExists(t, synoThumb, "@eaDir には触れない")
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}
