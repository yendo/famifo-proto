package index_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/index"
)

// testDebounce は本番（defaultDebounce）の2秒を待たずに済ませるための値。
// 待ち時間そのものが結果を変えるわけではなく、短くしてもテストの内容は変わらない。
// 本番と同じ2秒にすると、取り込みを待つテストがそれぞれ2〜3秒、増えないことを
// 確かめるテストが6秒かかり、パッケージ全体で数十秒になる。
const testDebounce = 100 * time.Millisecond

// startWatcher はWatcherをバックグラウンドで動かし、停止まで面倒を見る。
func startWatcher(t *testing.T, f *fixture) *index.Watcher {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := index.NewWatcher(f.ix, log)
	require.NoError(t, err)
	w.SetDebounce(testDebounce)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		require.NoError(t, w.Close())
	})
	time.Sleep(50 * time.Millisecond) // 監視の登録が終わるのを待つ
	return w
}

// requireCount はDBの枚数が期待値になるまで待つ。
func requireCount(t *testing.T, f *fixture, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		n, err := f.st.Count(context.Background())
		return err == nil && n == want
	}, 3*time.Second, 25*time.Millisecond, "枚数が %d にならなかった", want)
}

func TestWatcherIndexesNewFile(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	requireCount(t, f, 1)
}

func TestWatcherIgnoresNonPhotos(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	require.NoError(t, os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("x"), 0o644))
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	requireCount(t, f, 1)
}

func TestWatcherRemovesDeletedFile(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	requireCount(t, f, 1)

	require.NoError(t, os.Remove(path))

	requireCount(t, f, 0)
}

func TestWatcherPicksUpNewSubdirectory(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	sub := filepath.Join(f.root, "2020")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	time.Sleep(50 * time.Millisecond) // 監視登録を待つ
	writeTestJPEG(t, sub, "a.jpg", 40, 20)

	requireCount(t, f, 1)
}

func TestWatcherPicksUpDirectoryMovedInWholesale(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	// 監視外で中身を作ってからディレクトリごと移動する。
	// 個別のCREATEイベントは飛ばないので、新規ディレクトリの中身を
	// 自前で走査できていないと取りこぼす。
	staging := filepath.Join(t.TempDir(), "album")
	require.NoError(t, os.MkdirAll(staging, 0o755))
	writeTestJPEG(t, staging, "a.jpg", 40, 20)
	writeTestJPEG(t, staging, "b.jpg", 40, 20)
	require.NoError(t, os.Rename(staging, filepath.Join(f.root, "album")))

	requireCount(t, f, 2)
}

func TestWatcherRemovesRowsWhenDirectoryRenamedWithinTree(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	album := filepath.Join(f.root, "album")
	require.NoError(t, os.MkdirAll(album, 0o755))
	time.Sleep(50 * time.Millisecond) // 監視登録を待つ
	writeTestJPEG(t, album, "a.jpg", 40, 20)
	writeTestJPEG(t, album, "b.jpg", 40, 20)
	requireCount(t, f, 2)

	// ディレクトリ内でのリネーム: 子ファイルには個別イベントが来ない。
	// RemoveTreeでの前方一致削除が無いと、古いパスの行が残ったまま
	// 新しいパスの行が二重に増える。
	renamed := filepath.Join(f.root, "album2")
	require.NoError(t, os.Rename(album, renamed))
	time.Sleep(50 * time.Millisecond) // 監視登録を待つ
	// 新しい場所は監視外で作られたのと同様、Createの一括取り込みで拾われる。
	requireCount(t, f, 2)

	paths, err := f.st.AllPaths(context.Background())
	require.NoError(t, err)
	for p := range paths {
		require.Contains(t, p, "album2", "旧パス album の行が残ってはいけない: %s", p)
	}
}

func TestWatcherHandlesFileRenameWithinTree(t *testing.T) {
	// Remove/RenameでRemoveTreeも呼ぶようになったため、ファイルのリネームでも
	// RemoveFileとRemoveTreeの両方が呼ばれる。該当の無い方は静かにno-opであることを確認する。
	f := newFixture(t)
	startWatcher(t, f)
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	requireCount(t, f, 1)

	require.NoError(t, os.Rename(path, filepath.Join(f.root, "renamed.jpg")))

	time.Sleep(150 * time.Millisecond)
	requireCount(t, f, 1) // 消えたのはa.jpgの行のみ。renamed.jpgはCreateで拾われて1件のまま
}

func TestWatcherDebouncesRepeatedWrites(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)
	path := filepath.Join(f.root, "a.jpg")

	// 少しずつ書き足す＝コピー中を模す。最終的に有効なJPEGになる。
	require.NoError(t, os.WriteFile(path, []byte("partial"), 0o644))
	time.Sleep(20 * time.Millisecond)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	requireCount(t, f, 1)
}

func TestWatcherIgnoresSynologyThumbnailsCreatedLater(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	writeTestJPEG(t, f.root, "IMG_0001.jpg", 40, 20)
	requireCount(t, f, 1)

	// Synologyは写真が置かれた後にサムネイルを作る。取り込むと1枚が複数枚に見える。
	writeTestJPEG(t, filepath.Join(f.root, "@eaDir", "IMG_0001.jpg"),
		"SYNOPHOTO_THUMB_M.jpg", 20, 10)

	// 増えないことの確認なのでdebounceの経過を待ってから数える。
	time.Sleep(3 * testDebounce)
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n, "サムネイルを取り込まないこと")
}

func TestWatcherSkipsSynologyDirsInMovedDirectory(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	// ディレクトリごと移動した場合、中身には個別のイベントが来ないため
	// enqueueTree が自前で走査する。そこにも除外が要る。
	staging := filepath.Join(t.TempDir(), "album")
	writeTestJPEG(t, staging, "IMG_0001.jpg", 40, 20)
	writeTestJPEG(t, filepath.Join(staging, "@eaDir", "IMG_0001.jpg"),
		"SYNOPHOTO_THUMB_M.jpg", 20, 10)
	require.NoError(t, os.Rename(staging, filepath.Join(f.root, "album")))

	requireCount(t, f, 1)
	time.Sleep(3 * testDebounce)
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n, "移動後に遅れて取り込まれないこと")
}

// mkfifoPhoto は取り込みを途中で止められる「写真」を作る。名前付きパイプは
// 拡張子の上では写真なので取り込みの対象になり、読み手は書き手が現れるまで
// open(2) で止まる。取り込みの進み方を実時間の当て推量なしに操れる。
func mkfifoPhoto(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, syscall.Mkfifo(path, 0o600))
	return path
}

// waitForIndexing は path の取り込みが読み取りを始めるまで待ち、書き込み側を返す。
// FIFOのopen(2)は反対側が開くまで返らないため、返ってきたこと自体が
// 「取り込みがこのファイルを開いた」ことの証拠になる。返ったファイルを開いたままに
// しておけば、読み手はデータ待ちで止まり続ける。
func waitForIndexing(t *testing.T, path string) *os.File {
	t.Helper()
	opened := make(chan *os.File, 1)
	go func() {
		if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			opened <- w
		}
	}()
	select {
	case w := <-opened:
		return w
	case <-time.After(3 * time.Second):
		t.Fatalf("取り込みが %s を読み始めなかった", path)
		return nil
	}
}

// serveFifo は path に読み手が現れるたびにJPEGを流し込む係を置く。
// 1枚の取り込みは原本を2度開く。EXIFの読み取りとサムネイルの生成である。
// どちらの open(2) にも応じる必要があるうえ、1度目の読み手が閉じる時刻は
// こちらから見えないので、回数を数えずに応じ続ける。
//
// 取り込みが最後まで通ればDBに行が増える。呼び出し側はそれを待つことで、
// 係を片付けてよい時点を実時間の当て推量なしに知れる。
func serveFifo(t *testing.T, path string, data []byte) {
	t.Helper()
	stop := make(chan struct{})
	go func() {
		for {
			w, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				return
			}
			w.Write(data) // 読み手が途中で閉じればEPIPEになる。応じるのが仕事なので見ない
			w.Close()
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		// 読み手を待って止まっている係を返らせる。非ブロッキングなら
		// 書き手がいなくても読み取り側を開ける。
		if r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			r.Close()
		}
	})
}

func TestWatcherKeepsHandlingEventsWhileAPhotoIsStuck(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	// 1枚の取り込みが終わらない状態を作る。取り込みを監視ループの中で
	// 直列に走らせていると、ここでループごと止まり、以降のイベントを読めない。
	stuck := mkfifoPhoto(t, f.root, "stuck.jpg")
	w := waitForIndexing(t, stuck)

	writeTestJPEG(t, f.root, "b.jpg", 40, 20)

	requireCount(t, f, 1) // 止まっている1枚に巻き込まれない

	// 止めていた取り込みを最後まで通してから監視を止める。
	require.NoError(t, w.Close())
	serveFifo(t, stuck, testJPEG(t, 40, 20))
	requireCount(t, f, 2)
}

func TestWatcherIndexesUpToWorkersInParallel(t *testing.T) {
	f := newFixtureWorkers(t, 2)
	startWatcher(t, f)

	a := mkfifoPhoto(t, f.root, "a.jpg")
	b := mkfifoPhoto(t, f.root, "b.jpg")

	// 2枚が同時に読まれるまで待つ。1枚ずつしか取り込まないなら、先に開いた
	// ほうを閉じていない以上、もう一方のopenは返らない。
	wa := waitForIndexing(t, a)
	wb := waitForIndexing(t, b)

	// 2枚とも最後まで通してから監視を止める。
	require.NoError(t, wa.Close())
	require.NoError(t, wb.Close())
	serveFifo(t, a, testJPEG(t, 40, 20))
	serveFifo(t, b, testJPEG(t, 40, 20))
	requireCount(t, f, 2)
}

func TestWatcherLeavesNoRowForAPhotoMovedWhileBeingIndexed(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	// HEICは自前でサムネイルを作らないので原本を1度しか開かない。EXIFの読み取りで
	// 止めれば、取り込みの途中という状態を保ったまま写真を動かせる。
	src := mkfifoPhoto(t, f.root, "a.heic")
	w := waitForIndexing(t, src)

	// 取り込み中に別名へ移す。行が生まれるのは取り込みの完了時なので、
	// このときの削除は空振りする。
	moved := filepath.Join(f.root, "moved.heic")
	require.NoError(t, os.Rename(src, moved))
	time.Sleep(50 * time.Millisecond) // 削除が取り込みの完了より先に処理される順序を作る

	require.NoError(t, w.Close())
	serveFifo(t, moved, testJPEG(t, 40, 20))

	// 移動先の1枚だけが残る。遅れて増えないことの確認なので待ってから数える。
	time.Sleep(3 * testDebounce)
	requireCount(t, f, 1)

	paths, err := f.st.AllPaths(context.Background())
	require.NoError(t, err)
	for p := range paths {
		require.Contains(t, p, "moved.heic", "消えたパスの行が残ってはいけない: %s", p)
	}
}

func TestWatcherLeavesNoRowForADirectoryMovedWhileBeingIndexed(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	album := filepath.Join(f.root, "album")
	require.NoError(t, os.MkdirAll(album, 0o755))
	time.Sleep(50 * time.Millisecond) // 監視登録を待つ

	// 単体の移動と同じ仕掛け。止められる写真をディレクトリの中に置く。
	src := mkfifoPhoto(t, album, "a.heic")
	w := waitForIndexing(t, src)

	// 取り込み中にディレクトリごと移す。イベントのパスは album で、
	// 取り込み中として控えてあるのは album/a.heic である。
	moved := filepath.Join(f.root, "moved")
	require.NoError(t, os.Rename(album, moved))
	time.Sleep(50 * time.Millisecond) // 移動が取り込みの完了より先に処理される順序を作る

	require.NoError(t, w.Close())
	serveFifo(t, filepath.Join(moved, "a.heic"), testJPEG(t, 40, 20))

	requireCount(t, f, 1)
	time.Sleep(3 * testDebounce) // 遅れて移動元の行が増えないこと
	requireCount(t, f, 1)

	paths, err := f.st.AllPaths(context.Background())
	require.NoError(t, err)
	for p := range paths {
		require.Contains(t, p, "moved", "移動元のパスの行が残ってはいけない: %s", p)
	}
}

func TestWatcherWatchesRootsBeforeRunStarts(t *testing.T) {
	f := newFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := index.NewWatcher(f.ix, log)
	require.NoError(t, err)
	w.SetDebounce(testDebounce)

	// NewWatcher が戻った時点で監視が張れていること。起動時は「監視を張る →
	// スキャン」の順にするので、Run の開始を待ってから張るのでは間に合わない。
	// スキャンが走査を終えたあとに置かれた写真を、どちらも拾えなくなる。
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		require.NoError(t, w.Close())
	})

	requireCount(t, f, 1)
}

func TestWatcherAsksForAScanWhenAPhotoDisappearsWhileBeingIndexed(t *testing.T) {
	f := newFixture(t)
	w := startWatcher(t, f)

	// 取り込みの最中に消えた写真は、ワーカーが後から Upsert して存在しない
	// パスの行を残しうる。スキャンだけがそれを回収できるので、次の1回を前倒す。
	src := mkfifoPhoto(t, f.root, "a.heic")
	fw := waitForIndexing(t, src)
	t.Cleanup(func() { _ = fw.Close() })

	// ルートの外へ移す。移動先のCreateを拾わせないため。
	require.NoError(t, os.Rename(src, filepath.Join(t.TempDir(), "moved.heic")))

	select {
	case <-w.ScanRequests():
	case <-time.After(2 * time.Second):
		t.Fatal("スキャンの前倒しを要求しなかった")
	}
}

func TestWatcherDoesNotAskForAScanWhenNothingIsBeingIndexed(t *testing.T) {
	f := newFixture(t)
	w := startWatcher(t, f)
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	requireCount(t, f, 1)
	time.Sleep(100 * time.Millisecond) // ワーカーが持ち場を空けるのを待つ

	// 取り込みが走っていない間の削除は、監視だけで正しく反映できる。
	require.NoError(t, os.Remove(path))
	requireCount(t, f, 0)

	select {
	case <-w.ScanRequests():
		t.Fatal("取り込みが走っていないのにスキャンを要求した")
	default:
	}
}

func TestWatcherAsksForAScanOnEventOverflow(t *testing.T) {
	f := newFixture(t)
	w := startWatcher(t, f)

	// 溢れは、変更を取りこぼしたと確実に分かる唯一の合図である。
	// 落ちたぶんを取り戻せるのはスキャンだけなので、次の1回を前倒す。
	w.InjectWatchError(fsnotify.ErrEventOverflow)

	select {
	case <-w.ScanRequests():
	case <-time.After(2 * time.Second):
		t.Fatal("溢れを検知してもスキャンを要求しなかった")
	}
}

func TestWatcherDoesNotAskForAScanOnOtherWatchErrors(t *testing.T) {
	f := newFixture(t)
	w := startWatcher(t, f)

	w.InjectWatchError(errors.New("監視の別の失敗"))

	time.Sleep(100 * time.Millisecond)
	select {
	case <-w.ScanRequests():
		t.Fatal("溢れ以外のエラーで走査をやり直してはいけない")
	default:
	}
}
