package index_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/index"
)

func TestScanIndexesNestedPhotos(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	writeTestJPEG(t, filepath.Join(f.root, "2020"), "b.jpg", 40, 20)
	writeTestJPEG(t, filepath.Join(f.root, "2020", "trip"), "c.jpg", 40, 20)

	stats, err := f.ix.Scan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 3, stats.Indexed, "サブディレクトリも再帰的に走査する")
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

func TestScanIgnoresNonPhotos(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "clip.mp4"), []byte("x"), 0o644))

	stats, err := f.ix.Scan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed)
	require.Equal(t, 0, stats.Skipped, "対象外拡張子はスキップとして数えない")
}

func TestScanSkipsBrokenFilesAndContinues(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "good1.jpg", 40, 20)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "broken.jpg"), []byte("nope"), 0o644))
	writeTestJPEG(t, f.root, "good2.jpg", 40, 20)

	stats, err := f.ix.Scan(context.Background())

	require.NoError(t, err, "1ファイルの破損で全体を止めない")
	require.Equal(t, 2, stats.Indexed)
	require.Equal(t, 1, stats.Skipped)
}

func TestScanSkipsUnchangedFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	first, err := f.ix.Scan(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, first.Indexed)

	second, err := f.ix.Scan(ctx)

	require.NoError(t, err)
	require.Equal(t, 0, second.Indexed)
	require.Equal(t, 1, second.Unchanged, "mtimeが同じなら再インデックスしない")
}

func TestScanReindexesModifiedFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	_, err := f.ix.Scan(ctx)
	require.NoError(t, err)

	// 内容とmtimeを変える
	writeTestJPEG(t, f.root, "a.jpg", 80, 40)
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, future, future))

	stats, err := f.ix.Scan(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed)
	require.Equal(t, 0, stats.Unchanged)
}

func TestScanRemovesDeletedPhotos(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	_, err := f.ix.Scan(ctx)
	require.NoError(t, err)
	thumbPath := f.thumbPath(t, path)
	require.FileExists(t, thumbPath)

	// アプリ停止中に消されたことを模す
	require.NoError(t, os.Remove(path))

	stats, err := f.ix.Scan(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, stats.Removed)
	require.NoFileExists(t, thumbPath, "サムネイルもハードデリートする")
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestScanStopsOnCancelledContext(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.ix.Scan(ctx)

	require.ErrorIs(t, err, context.Canceled)
}

func TestScanDoesNotPurgeWhenRootAppearsEmpty(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pathA := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	pathB := writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	_, err := f.ix.Scan(ctx)
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

	stats, err := f.ix.Scan(ctx)

	require.NoError(t, err)
	require.Equal(t, 0, stats.Removed, "走査結果が空のときはインデックスを消さない")
	n2, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n2, "未マウントの可能性があるため既存の登録は残す")
	require.FileExists(t, thumbA, "サムネイルも残る")
	require.FileExists(t, thumbB)
}

func TestScanIndexesEveryRoot(t *testing.T) {
	f, roots := newFixtureRoots(t, "alice", "bob")
	ctx := context.Background()
	writeTestJPEG(t, roots[0], "a.jpg", 40, 20)
	writeTestJPEG(t, roots[1], "b.jpg", 40, 20)

	stats, err := f.ix.Scan(ctx)

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
func TestScanDoesNotPurgeTheRootThatAppearsEmpty(t *testing.T) {
	f, roots := newFixtureRoots(t, "alice", "bob")
	ctx := context.Background()
	gone := writeTestJPEG(t, roots[0], "a.jpg", 40, 20)
	writeTestJPEG(t, roots[1], "b.jpg", 40, 20)
	_, err := f.ix.Scan(ctx)
	require.NoError(t, err)
	thumbGone := f.thumbPath(t, gone)
	require.FileExists(t, thumbGone)

	// aliceのドライブが未マウントになった状況を模す：中身だけ消してルートは残す
	require.NoError(t, os.Remove(gone))

	stats, err := f.ix.Scan(ctx)

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
func TestScanRemovesPhotosOutsideEveryRoot(t *testing.T) {
	f, roots := newFixtureRoots(t, "alice", "bob")
	ctx := context.Background()
	writeTestJPEG(t, roots[0], "a.jpg", 40, 20)
	dropped := writeTestJPEG(t, roots[1], "b.jpg", 40, 20)
	_, err := f.ix.Scan(ctx)
	require.NoError(t, err)

	// bob を引数から外して起動し直した状況を模す
	f2 := index.New(roots[:1], f.st, f.thumbs, 4, f.log)

	stats, err := f2.Scan(ctx)

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
func TestScanSkipsAnUnreadableRootAndContinues(t *testing.T) {
	f, roots := newFixtureRoots(t, "alice", "bob")
	ctx := context.Background()
	writeTestJPEG(t, roots[0], "a.jpg", 40, 20)
	kept := writeTestJPEG(t, roots[1], "b.jpg", 40, 20)
	_, err := f.ix.Scan(ctx)
	require.NoError(t, err)

	// aliceのルートごと消す
	require.NoError(t, os.RemoveAll(roots[0]))
	// bobに新しい写真を足す
	writeTestJPEG(t, roots[1], "c.jpg", 40, 20)

	stats, err := f.ix.Scan(ctx)

	require.NoError(t, err, "読めないルートがあっても走査全体は失敗しない")
	require.Equal(t, 1, stats.Indexed, "生きているルートの新しい写真は取り込む")
	require.Equal(t, 0, stats.Removed, "読めないルートの写真は消さない")
	require.FileExists(t, f.thumbPath(t, kept))
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

func TestScanSkipsSynologyMetadataDirs(t *testing.T) {
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

	stats, err := f.ix.Scan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed, "本物の1枚だけを取り込む")
	require.Equal(t, 0, stats.Skipped, "除外はスキップとして数えない")

	paths, err := f.st.AllPaths(context.Background())
	require.NoError(t, err)
	require.Len(t, paths, 1)
	_, ok := paths[filepath.Join(f.root, "IMG_0001.jpg")]
	require.True(t, ok, "本物が残っていること")
}

// 並行してサムネイルを作っても取りこぼしが出ないことを確かめる。ワーカーの完了を
// 待たずに走査を終えると Indexed が実際より少なくなり、Stats の更新の競合は
// -race で現れる。1枚ずつでは同時に走る窓が開かないので、まとまった枚数を置く。
func TestScanIndexesEveryPhotoWithConcurrentWorkers(t *testing.T) {
	f := newFixtureWorkers(t, 8)
	const n = 64
	for i := range n {
		writeTestJPEG(t, f.root, fmt.Sprintf("p%02d.jpg", i), 40, 20)
	}

	stats, err := f.ix.Scan(context.Background())

	require.NoError(t, err)
	require.Equal(t, n, stats.Indexed)
	require.Equal(t, 0, stats.Skipped)
	count, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, n, count)
}

// ワーカー数1でも走査は成立する。並行化の面倒を避けたい環境のための逃げ道であり、
// ここが壊れると設定で回避する手段が無くなる。
func TestScanWorksWithASingleWorker(t *testing.T) {
	f := newFixtureWorkers(t, 1)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	writeTestJPEG(t, f.root, "b.jpg", 40, 20)

	stats, err := f.ix.Scan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, stats.Indexed)
}

func TestScanDoesNotWaitForTheWatchersIndexing(t *testing.T) {
	f := newFixture(t)

	// 監視を張る前に置くのでCreateのイベントは飛ばない。この1枚はスキャンだけが拾う。
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	startWatcher(t, f)

	// 監視の側に終わらない取り込みを1件持たせる。
	stuck := mkfifoPhoto(t, f.root, "stuck.heic")
	w := waitForIndexing(t, stuck)
	// 取り込みを解く係を先に登録する。HEICは自前でサムネイルを作らないので、
	// 書き手を閉じてEOFを返すだけで取り込みは進む。ここで登録しておかないと、
	// 検証に失敗して途中で終わったときに監視の停止が取り込みを待って固まる。
	t.Cleanup(func() { _ = w.Close() })

	// ルートの外へ移す。スキャンの走査はこれを見つけないので、
	// executor に残るのはスキャンが自分では出していない仕事だけになる。
	moved := filepath.Join(t.TempDir(), "stuck.heic")
	require.NoError(t, os.Rename(stuck, moved))

	type scanResult struct {
		stats index.Stats
		err   error
	}
	done := make(chan scanResult, 1)
	go func() {
		stats, err := f.ix.Scan(context.Background())
		done <- scanResult{stats, err}
	}()

	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.Equal(t, 1, r.stats.Indexed)
	case <-time.After(2 * time.Second):
		t.Fatal("Scan が監視の側の取り込みの完了まで待っている")
	}
}

func TestRunScansKeepsReconcilingOnItsInterval(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.ix.RunScans(ctx, 50*time.Millisecond, nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	requireCount(t, f, 1)

	// 監視は張っていないので、この1枚を拾えるのは定期スキャンだけである。
	writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	requireCount(t, f, 2)
}

func TestGhostRowFromAScanIsReclaimedByTheEarlyScan(t *testing.T) {
	f := newFixture(t)
	// ルートを空にしない。1枚も見つからないルートの配下は purge が見送るため。
	writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	album := filepath.Join(f.root, "album")
	require.NoError(t, os.MkdirAll(album, 0o755))
	src := mkfifoPhoto(t, album, "a.heic")

	// 監視の前から在るファイルにはCreateのイベントが飛ばない。
	// この2枚を取り込むのはスキャンだけである。
	w := startWatcher(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// 起動時のスキャン。main.go と同じく同期で1回走らせる。
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		_, _ = f.ix.Scan(ctx)
	}()
	fw := waitForIndexing(t, src)
	t.Cleanup(func() { _ = fw.Close() })

	// 取り込みの最中にディレクトリごとルートの外へ移す。行が生まれるのは
	// 取り込みの完了時なので、この時点の削除は空振りする。
	require.NoError(t, os.Rename(album, filepath.Join(t.TempDir(), "album")))
	time.Sleep(50 * time.Millisecond) // 削除が取り込みの完了より先に処理される順序を作る

	require.NoError(t, fw.Close())
	<-scanDone
	requireCount(t, f, 2) // b.jpg と、存在しない album/a.heic の幽霊行

	// スキャンのループを始める。定期実行は1時間後なので、削除の時点で積まれた
	// 前倒しの要求が効かなければ幽霊行は残り続ける。
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		f.ix.RunScans(ctx, time.Hour, w.ScanRequests())
	}()
	t.Cleanup(func() { cancel(); <-loopDone })

	requireCount(t, f, 1)
}
