package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yendo/famifo-proto/internal/store"
)

func TestFullScanIndexesNestedPhotos(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	writeTestJPEG(t, filepath.Join(f.root, "2020"), "b.jpg", 40, 20)
	writeTestJPEG(t, filepath.Join(f.root, "2020", "trip"), "c.jpg", 40, 20)

	stats, err := f.ix.FullScan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 3, stats.Indexed, "サブディレクトリも再帰的に走査する")
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

func TestFullScanIgnoresNonPhotos(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "clip.mp4"), []byte("x"), 0o644))

	stats, err := f.ix.FullScan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed)
	require.Equal(t, 0, stats.Skipped, "対象外拡張子はスキップとして数えない")
}

func TestFullScanSkipsBrokenFilesAndContinues(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "good1.jpg", 40, 20)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "broken.jpg"), []byte("nope"), 0o644))
	writeTestJPEG(t, f.root, "good2.jpg", 40, 20)

	stats, err := f.ix.FullScan(context.Background())

	require.NoError(t, err, "1ファイルの破損で全体を止めない")
	require.Equal(t, 2, stats.Indexed)
	require.Equal(t, 1, stats.Skipped)
}

func TestFullScanSkipsUnchangedFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	first, err := f.ix.FullScan(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, first.Indexed)

	second, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 0, second.Indexed)
	require.Equal(t, 1, second.Unchanged, "mtimeが同じなら再インデックスしない")
}

func TestFullScanReindexesModifiedFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	_, err := f.ix.FullScan(ctx)
	require.NoError(t, err)

	// 内容とmtimeを変える
	writeTestJPEG(t, f.root, "a.jpg", 80, 40)
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, future, future))

	stats, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed)
	require.Equal(t, 0, stats.Unchanged)
}

func TestFullScanRemovesDeletedPhotos(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	_, err := f.ix.FullScan(ctx)
	require.NoError(t, err)
	thumbPath := f.gen.Path(store.IDFor(path))
	require.FileExists(t, thumbPath)

	// アプリ停止中に消されたことを模す
	require.NoError(t, os.Remove(path))

	stats, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, stats.Removed)
	require.NoFileExists(t, thumbPath, "サムネイルもハードデリートする")
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestFullScanStopsOnCancelledContext(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.ix.FullScan(ctx)

	require.ErrorIs(t, err, context.Canceled)
}
