package index

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testDebounce = 100 * time.Millisecond

// startWatcher はWatcherをバックグラウンドで動かし、停止まで面倒を見る。
func startWatcher(t *testing.T, f *fixture) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := NewWatcher(f.ix, log, testDebounce)
	require.NoError(t, err)

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
