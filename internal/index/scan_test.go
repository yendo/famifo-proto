package index_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/index"
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
	thumbPath := f.thumbPath(t, path)
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

func TestFullScanDoesNotPurgeWhenRootAppearsEmpty(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pathA := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	pathB := writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	_, err := f.ix.FullScan(ctx)
	require.NoError(t, err)
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	thumbA := f.thumbPath(t, pathA)
	thumbB := f.thumbPath(t, pathB)
	require.FileExists(t, thumbA)
	require.FileExists(t, thumbB)

	// ドライブが未マウントで中身が空に見えるケースを模す：ファイルだけ消してルートは残す
	require.NoError(t, os.Remove(pathA))
	require.NoError(t, os.Remove(pathB))

	stats, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 0, stats.Removed, "走査結果が空のときはインデックスを消さない")
	n2, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n2, "未マウントの可能性があるため既存の登録は残す")
	require.FileExists(t, thumbA, "サムネイルも残る")
	require.FileExists(t, thumbB)
}

func TestFullScanIndexesEveryRoot(t *testing.T) {
	f, roots := newFixtureRoots(t, "alice", "bob")
	ctx := context.Background()
	writeTestJPEG(t, roots[0], "a.jpg", 40, 20)
	writeTestJPEG(t, roots[1], "b.jpg", 40, 20)

	stats, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 2, stats.Indexed, "すべてのルートを走査すること")
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

// 片方のドライブが未マウントで空に見えるとき、そのルートの写真だけを守る。
//
// 削除の判定が全ルート合計だと、生きているルートに写真がある限りガードが
// 発動せず、空に見えたルートの写真が消える。復旧には数時間の再インデックスが
// 要るので、ルート単位で判定する。
func TestFullScanDoesNotPurgeTheRootThatAppearsEmpty(t *testing.T) {
	f, roots := newFixtureRoots(t, "alice", "bob")
	ctx := context.Background()
	gone := writeTestJPEG(t, roots[0], "a.jpg", 40, 20)
	writeTestJPEG(t, roots[1], "b.jpg", 40, 20)
	_, err := f.ix.FullScan(ctx)
	require.NoError(t, err)
	thumbGone := f.thumbPath(t, gone)
	require.FileExists(t, thumbGone)

	// aliceのドライブが未マウントになった状況を模す：中身だけ消してルートは残す
	require.NoError(t, os.Remove(gone))

	stats, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 0, stats.Removed,
		"空に見えるルートの写真は消さない（bobに写真が残っていても）")
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.FileExists(t, thumbGone, "サムネイルも残る")
}

// 引数からルートが外れたら、その配下の写真はインデックスから消す。
// インデックスは「いま指定されているもの」に従う。
func TestFullScanRemovesPhotosOutsideEveryRoot(t *testing.T) {
	f, roots := newFixtureRoots(t, "alice", "bob")
	ctx := context.Background()
	writeTestJPEG(t, roots[0], "a.jpg", 40, 20)
	dropped := writeTestJPEG(t, roots[1], "b.jpg", 40, 20)
	_, err := f.ix.FullScan(ctx)
	require.NoError(t, err)

	// bob を引数から外して起動し直した状況を模す
	f2, err := index.New(roots[:1], f.st, f.thumbDir, 100, f.log)
	require.NoError(t, err)

	stats, err := f2.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, stats.Removed, "どのルートの配下でもない写真は消す")
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.NoFileExists(t, f.thumbPath(t, dropped), "サムネイルも消える")
}

// ルートのパスごと消えている（ボリュームが外れた等）ときは、そのルートを
// 飛ばして他のルートの処理を続ける。1つの外付けドライブが外れただけで
// 走査全体が止まると、生きているルートの更新まで反映されなくなる。
func TestFullScanSkipsAnUnreadableRootAndContinues(t *testing.T) {
	f, roots := newFixtureRoots(t, "alice", "bob")
	ctx := context.Background()
	writeTestJPEG(t, roots[0], "a.jpg", 40, 20)
	kept := writeTestJPEG(t, roots[1], "b.jpg", 40, 20)
	_, err := f.ix.FullScan(ctx)
	require.NoError(t, err)

	// aliceのルートごと消す
	require.NoError(t, os.RemoveAll(roots[0]))
	// bobに新しい写真を足す
	writeTestJPEG(t, roots[1], "c.jpg", 40, 20)

	stats, err := f.ix.FullScan(ctx)

	require.NoError(t, err, "読めないルートがあっても走査全体は失敗しない")
	require.Equal(t, 1, stats.Indexed, "生きているルートの新しい写真は取り込む")
	require.Equal(t, 0, stats.Removed, "読めないルートの写真は消さない")
	require.FileExists(t, f.thumbPath(t, kept))
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

func TestFullScanSkipsSynologyMetadataDirs(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "IMG_0001.jpg", 40, 20)
	// Synologyは写真1枚につき @eaDir/<ファイル名>/SYNOPHOTO_THUMB_*.jpg を作る。
	// 拡張子だけでは本物と区別が付かず、1枚が4枚に見える。
	thumbs := filepath.Join(f.root, "@eaDir", "IMG_0001.jpg")
	writeTestJPEG(t, thumbs, "SYNOPHOTO_THUMB_SM.jpg", 10, 5)
	writeTestJPEG(t, thumbs, "SYNOPHOTO_THUMB_M.jpg", 20, 10)
	writeTestJPEG(t, thumbs, "SYNOPHOTO_THUMB_XL.jpg", 80, 40)
	// ゴミ箱に残った写真も復活させない。
	writeTestJPEG(t, filepath.Join(f.root, "#recycle"), "deleted.jpg", 40, 20)

	stats, err := f.ix.FullScan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed, "本物の1枚だけを取り込む")
	require.Equal(t, 0, stats.Skipped, "除外はスキップとして数えない")

	paths, err := f.st.AllPaths(context.Background())
	require.NoError(t, err)
	require.Len(t, paths, 1)
	_, ok := paths[filepath.Join(f.root, "IMG_0001.jpg")]
	require.True(t, ok, "本物が残っていること")
}
