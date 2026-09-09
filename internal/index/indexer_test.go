package index_test

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/index"

	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

type fixture struct {
	ix       *index.Indexer
	st       *store.Store
	thumbs   *thumb.Provider
	root     string
	thumbDir string
	log      *slog.Logger
}

// generatedThumbs は自前で生成したサムネイルをすべて返す。
//
// パスを組み立てるのではなく置き場の中を数える。置き場の名前の付け方（IDによる
// 分割と、名前に入る元画像の版）はthumbの取り決めであり、取り込み側のテストが
// 知っていると、名前を変えただけでこちらが巻き添えになる。
//
// 借りたサムネイルは写真の隣の @eaDir に置かれるので、ここに入るのは自前の
// ものだけである。件数で見るため「余計なものを作っていない」ことまで言える。
func (f *fixture) generatedThumbs(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(f.thumbDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

// onlyGeneratedThumb は生成物がちょうど1つであることを確かめ、そのパスを返す。
func (f *fixture) onlyGeneratedThumb(t *testing.T) string {
	t.Helper()
	got := f.generatedThumbs(t)
	require.Len(t, got, 1)
	return got[0]
}

// newFixture は既定のワーカー数で fixture を作る。1より大きいのは、
// 既存のテストをそのまま並行経路に通して等価性を確かめるためである。
func newFixture(t *testing.T) *fixture { return newFixtureWorkers(t, 4) }

// newFixtureWorkers はワーカー数を指定して fixture を作る。
func newFixtureWorkers(t *testing.T, workers int) *fixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "photos")
	require.NoError(t, os.MkdirAll(root, 0o755))

	st, err := store.Open(filepath.Join(base, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbDir := filepath.Join(base, "thumbs")
	thumbs, err := thumb.NewProvider(thumbDir)
	require.NoError(t, err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ix := index.New([]string{root}, st, thumbs, workers, log)

	return &fixture{ix: ix, st: st, thumbs: thumbs, root: root, thumbDir: thumbDir, log: log}
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

	thumbDir := filepath.Join(base, "thumbs")
	thumbs, err := thumb.NewProvider(thumbDir)
	require.NoError(t, err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ix := index.New(roots, st, thumbs, 4, log)

	return &fixture{ix: ix, st: st, thumbs: thumbs, root: roots[0], thumbDir: thumbDir, log: log}, roots
}

func TestIndexFileStoresRasterPhotoWithThumb(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), media.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, path, got.Path())
	require.Len(t, f.generatedThumbs(t), 1, "nothing to borrow, so it makes its own")
}

// TestIndexFileAppliesTheEXIFOrientationToTheThumbnail はEXIFから読んだ向きが
// サムネイル生成まで届いていることを確かめる。読み取り(internal/index/exif)と
// 適用(internal/thumb)は別パッケージなので、繋ぎ違えても双方のテストは通る。
func TestIndexFileAppliesTheEXIFOrientationToTheThumbnail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// 縮小されない小ささにして、向きの適用が寸法にそのまま出るようにする。
	path := writeJPEGWithOrientation(t, f.root, "a.jpg", 16, 8, 6)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	cfg := decodeThumbConfig(t, f.onlyGeneratedThumb(t))
	require.Equal(t, 8, cfg.Width, "Orientation=6 swaps width and height")
	require.Equal(t, 16, cfg.Height)
}

func TestIndexFileStoresHEICWithoutThumb(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// HEICはデコードしない方針なので、中身が画像でなくても登録される
	path := filepath.Join(f.root, "a.heic")
	require.NoError(t, os.WriteFile(path, []byte("not decodable by go"), 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), media.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, path, got.Path(), "indexed even with no thumbnail")
	require.Empty(t, f.generatedThumbs(t), "HEIC cannot be decoded")
}

func TestIndexFileIgnoresUnsupportedExtensions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := filepath.Join(f.root, "a.mp4")
	require.NoError(t, os.WriteFile(path, []byte("video"), 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path), "an unsupported file is not an error")

	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestIndexFileIgnoresDirectories(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := filepath.Join(f.root, "sub.jpg") // 拡張子付きディレクトリという嫌がらせ
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.NoError(t, f.ix.IndexFile(context.Background(), dir))

	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestIndexFileRejectsBrokenRasterImage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := filepath.Join(f.root, "broken.jpg")
	require.NoError(t, os.WriteFile(path, []byte("not an image"), 0o644))

	err := f.ix.IndexFile(context.Background(), path)

	require.Error(t, err, "a broken image is not stored and returns an error")
	n, cerr := f.st.Count(context.Background())
	require.NoError(t, cerr)
	require.Equal(t, 0, n)
}

func TestRemoveFileDeletesRowAndThumb(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)
	require.NoError(t, f.ix.IndexFile(ctx, path))
	require.Len(t, f.generatedThumbs(t), 1)

	require.NoError(t, f.ix.RemoveFile(ctx, path))

	require.Empty(t, f.generatedThumbs(t))
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestRemoveFileIsQuietForUnknownPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	require.NoError(t, f.ix.RemoveFile(context.Background(), filepath.Join(f.root, "never.jpg")))
}

// writeSynoThumb は srcPath の写真用のサムネイルを @eaDir に置く。
func writeSynoThumb(t *testing.T, srcPath string) string {
	t.Helper()
	out := synology.ThumbMPath(srcPath)
	writeTestJPEG(t, filepath.Dir(out), filepath.Base(out), 20, 10)
	return out
}

func TestIndexFileBorrowsTheSynologyThumbnail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)
	writeSynoThumb(t, path)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), media.IDFor(path))
	require.NoError(t, err)
	require.Empty(t, f.generatedThumbs(t), "makes none of its own when it can borrow")
	small, _, _ := f.thumbs.SmallPath(got)
	require.Equal(t, synology.ThumbMPath(path), small, "the gallery shows the borrowed one")
}

// HEICはGoでデコードできないが、Synologyのサムネイルがあれば一覧に出せる。
func TestIndexFileBorrowsTheSynologyThumbnailForHEIC(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := filepath.Join(f.root, "a.heic")
	require.NoError(t, os.WriteFile(path, []byte("not decodable by go"), 0o644))
	writeSynoThumb(t, path)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), media.IDFor(path))
	require.NoError(t, err)
	small, _, _ := f.thumbs.SmallPath(got)
	require.Equal(t, synology.ThumbMPath(path), small,
		"borrowing puts it in the gallery even when it cannot be decoded")
}

// DSM 7.3 がHEICのデコードに失敗すると .fail だけが残る。famifoも作れないので
// サムネイル無しのまま原本を配信する。.fail を置き換えるのはfamifoの仕事ではない。
func TestIndexFileLeavesHEICWithoutThumbWhenOnlyAFailMarkerIsThere(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := filepath.Join(f.root, "a.heic")
	require.NoError(t, os.WriteFile(path, []byte("not decodable by go"), 0o644))
	fail := filepath.Join(filepath.Dir(synology.ThumbMPath(path)), "SYNOPHOTO_THUMB_M.fail")
	require.NoError(t, os.MkdirAll(filepath.Dir(fail), 0o755))
	require.NoError(t, os.WriteFile(fail, nil, 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), media.IDFor(path))
	require.NoError(t, err)
	require.Empty(t, f.generatedThumbs(t))
	_, _, ok := f.thumbs.SmallPath(got)
	require.False(t, ok, "with only .fail there is nothing to show in the gallery")
}

// famifoはSynology Photosの領域に書き込まない。消しもしない。
func TestRemoveFileKeepsTheSynologyThumbnail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)
	synoThumb := writeSynoThumb(t, path)
	require.NoError(t, f.ix.IndexFile(ctx, path))

	require.NoError(t, f.ix.RemoveFile(ctx, path))

	require.FileExists(t, synoThumb, "@eaDir is never touched")
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}
