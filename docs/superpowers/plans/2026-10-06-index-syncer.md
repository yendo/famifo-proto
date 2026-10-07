# スキャンと監視を1本のループにまとめる 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `internal/index` の `Scanner` と `Watcher` を1本のループを持つ `Syncer` にまとめ、入口をまたぐ状態と判断の持ち主を1つにする。

**Architecture:** `Indexer` は1枚を同期的に適用するだけに戻す。`Syncer` のループが唯一の書き手として、監視側とスキャン側の保留、取り込み中のパスと墓標、スキャン1回の進み具合を持つ。判断は goroutine を持たない `syncState` に置き、木の走査は別の goroutine で行って背圧付きのチャネルでループへ送る。

**Tech Stack:** Go、`github.com/fsnotify/fsnotify` v1.10.1、`modernc.org/sqlite`（`internal/store` 経由）

**Spec:** [docs/superpowers/specs/2026-10-05-index-syncer-design.md](../specs/2026-10-05-index-syncer-design.md)

## Global Constraints

- `internal/index` のコードコメントとテストのコメントは日本語（既存ファイルに合わせる）。識別子、ログのメッセージ、テストの失敗メッセージは英語
- コミットメッセージは英語の要約1行のみ。本文とトレーラーは付けない。type は `feat:` / `fix:` / `refactor:` / `test:` / `docs:` から選ぶ
- ブランチは `feature-index-concurrency-roles`（既にこのブランチ上にある）
- テストは公開APIだけで書く。`internal/index` のテストは外部テストパッケージ `index_test`。テスト専用の口は `export_test.go` に置く
- テストの実行は `make unit-test-race`。単体のテストは `go test -race -run '<名前>' ./internal/index/`
- ブラウザテストはビルドタグ付き。コンパイルの確認は `go vet -tags browser ./internal/web/`
- 振る舞いの変更は TDD。失敗するテストを先に書き、失敗を確かめてから実装する
- スキーマは変えない

## ファイル構成（Task 3 以降）

| ファイル | 責務 |
|---|---|
| `internal/index/indexer.go` | `Indexer`。1枚を同期的に適用する（`indexFile` / `removeFile` / `removeTree` / `purgeGone`）。並行について何も知らない |
| `internal/index/syncer.go` | `Syncer` の型、`NewSyncer`、`Run` のループ、`Scan`、ワーカーの起動と結果の処理 |
| `internal/index/syncstate.go` | `syncState`。ループの状態と「次に何をするか」の判断。チャネルにも fsnotify にもファイルシステムにも触らない |
| `internal/index/watch.go` | fsnotify のイベントの処理、監視の張り外し、部分木の走査 |
| `internal/index/scan.go` | `Stats`、全体の走査（`scanWalk`）、`isUnder` / `isUnderAny` |
| `internal/index/export_test.go` | テスト専用の口 |

---

### Task 1: ディレクトリを移動したら配下の監視を外す（問題1）

今の `Watcher` の上で直す。Task 3 でこのコードは `Syncer` に移るが、テストはそのまま引き継がれる。

**Files:**
- Modify: `internal/index/watch.go`（`handleEvent` の Remove / Rename の分岐、`unwatchUnder` を追加）
- Test: `internal/index/watch_test.go`

**Interfaces:**
- Consumes: `isUnder(root, path string) bool`（`scan.go`）、`fsnotify.Watcher.WatchList() []string`、`fsnotify.Watcher.Remove(name string) error`
- Produces: `func (w *Watcher) unwatchUnder(path string)`

- [ ] **Step 1: 失敗するテストを書く**

`internal/index/watch_test.go` の `TestWatcherRemovesRowsWhenDirectoryRenamedWithinTree` の直後に足す。

```go
// fsnotify は監視を inode で持ち、張った時点のパスで覚えている。移動した
// ディレクトリの子の監視を外さずに移動先で張り直すと、inotify が同じ監視を返し、
// 移動先の配下のイベントが移動元のパスで届く。取り込みは存在しないパスを
// 開こうとして失敗し、新しい写真が載らない。
func TestWatcherFollowsADirectoryRenamedWithItsSubdirectories(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	startWatcher(t, f)

	album := filepath.Join(f.root, "album")
	require.NoError(t, os.MkdirAll(filepath.Join(album, "sub"), 0o755))
	time.Sleep(50 * time.Millisecond) // 監視登録を待つ

	renamed := filepath.Join(f.root, "album2")
	require.NoError(t, os.Rename(album, renamed))
	time.Sleep(50 * time.Millisecond) // 移動先の監視登録と中身の走査を待つ

	// 移動のあとに置く。走査はもう終わっているので、拾えるのは監視だけである。
	writeTestJPEG(t, filepath.Join(renamed, "sub"), "a.jpg", 40, 20)

	requireCount(t, f, 1)
	paths, err := f.st.AllPaths(context.Background())
	require.NoError(t, err)
	_, ok := paths[filepath.Join(renamed, "sub", "a.jpg")]
	require.True(t, ok, "the row is under the new path")
}
```

- [ ] **Step 2: 失敗を確かめる**

Run: `go test -race -run 'TestWatcherFollowsADirectoryRenamedWithItsSubdirectories' ./internal/index/`
Expected: FAIL。`the count never reached 1`

- [ ] **Step 3: 実装する**

`internal/index/watch.go` の `handleEvent` で、`removeTree` の呼び出しの直後（`if w.ix.busy() {` の前）に足す。

```go
		// 消えたのがディレクトリなら、その配下に張ってあった監視を外す。
		w.unwatchUnder(ev.Name)
```

同じファイルの `addTree` の直前に足す。

```go
// unwatchUnder は path とその配下に張ってある監視を外す。
//
// fsnotify は監視を inode で持ち、張った時点のパスで覚えている。ディレクトリが
// 移動すると、移動したディレクトリ自身の監視は IN_MOVE_SELF で外れるが、その子の
// 監視は古いパスのまま残る。移動先で張り直しても inotify が同じ監視を返すため
// パスは更新されず、移動先の配下のイベントが移動元のパスで届く。ルートの外へ
// 移した場合は、ルートの外の変更が移動元のパスとして届き続ける。
//
// 削除のイベントのたびに監視の一覧をなめる。比較は張ってあるディレクトリの数だけの
// 文字列の前方一致である。
//
// 既に外れている監視を外そうとすると ErrNonExistentWatch が返るが、外したい
// だけなので見ない。
func (w *Watcher) unwatchUnder(path string) {
	for _, p := range w.fsw.WatchList() {
		if isUnder(path, p) {
			_ = w.fsw.Remove(p)
		}
	}
}
```

- [ ] **Step 4: 通ることを確かめる**

Run: `go test -race -run 'TestWatcher' ./internal/index/`
Expected: PASS

- [ ] **Step 5: 全体を通す**

Run: `gofmt -l . && make vet && make unit-test-race`
Expected: `gofmt -l` が空、vet と test が通る

- [ ] **Step 6: コミット**

```bash
git add internal/index/watch.go internal/index/watch_test.go
git commit -m "fix: Drop the watches under a directory that was moved"
```

---

### Task 2: ルートが消えても行を消さない（問題2）

**Files:**
- Modify: `internal/index/watch.go`（`handleEvent` の Remove / Rename の分岐の先頭、`isRoot` を追加）
- Test: `internal/index/watch_test.go`

**Interfaces:**
- Consumes: `Watcher.requestScan()`
- Produces: `func (w *Watcher) isRoot(path string) bool`

- [ ] **Step 1: 失敗するテストを書く**

`internal/index/watch_test.go` の Task 1 のテストの直後に足す。

```go
// ルートそのものが消えたとき、配下の行を消さない。スキャンが「空に見える
// ルート」の配下を消さないのと同じ扱いにそろえる。消してしまうと、戻したときに
// 全部を取り込み直し、サムネイルを作り直すことになる。
func TestWatcherKeepsRowsWhenARootIsRenamed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	startWatcher(t, f)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	requireCount(t, f, 1)

	require.NoError(t, os.Rename(f.root, f.root+".away"))

	// 減らないことの確認なので、イベントが処理されるのを待ってから数える。
	time.Sleep(3 * testDebounce)
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n, "the items under a root that disappeared are kept")
}
```

- [ ] **Step 2: 失敗を確かめる**

Run: `go test -race -run 'TestWatcherKeepsRowsWhenARootIsRenamed' ./internal/index/`
Expected: FAIL。`the items under a root that disappeared are kept`（期待 1、実際 0）

- [ ] **Step 3: 実装する**

`internal/index/watch.go` の `handleEvent` の `case ev.Has(fsnotify.Remove), ev.Has(fsnotify.Rename):` の直後、既存のコメントより前に足す。

```go
		if w.isRoot(ev.Name) {
			// ルートそのものが消えた。名前の変更か削除かは区別できない。スキャンが
			// 「空に見えるルート」の配下を消さないのと同じ理由で、ここでも消さない。
			// 消すと、戻したときに全部を取り込み直すことになる。扱いはスキャンの
			// 判断にそろえ、走査を前倒しする。
			w.log.Warn("a root disappeared; keeping its items", "root", ev.Name)
			w.requestScan()
			return
		}
```

同じファイルの `unwatchUnder` の直前に足す。

```go
// isRoot は path がルートそのものかを返す。
func (w *Watcher) isRoot(path string) bool {
	path = filepath.Clean(path)
	for _, root := range w.ix.roots {
		if filepath.Clean(root) == path {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 通ることを確かめる**

Run: `go test -race -run 'TestWatcher' ./internal/index/`
Expected: PASS

- [ ] **Step 5: 全体を通す**

Run: `gofmt -l . && make vet && make unit-test-race`
Expected: `gofmt -l` が空、vet と test が通る

- [ ] **Step 6: コミット**

```bash
git add internal/index/watch.go internal/index/watch_test.go
git commit -m "fix: Keep the items of a root that disappeared while watching"
```

---

### Task 3: `Scanner` と `Watcher` を `Syncer` にまとめる（問題4・5）

この Task が再編の本体である。`Indexer` から枠と起動を外し、`Syncer` のループに移す。ディレクトリの Create で木を歩く処理（`addTree` / `enqueueTree`）は、この Task ではループの中に残す（Task 4 で外へ出す）。

**Files:**
- Modify: `internal/index/indexer.go`（`slots` 一式、`startIndex` / `tryStartIndex` / `run` / `busy` を削除、`New` から `workers` を外す、`purgeGone` を追加）
- Create: `internal/index/syncstate.go`
- Create: `internal/index/syncer.go`
- Modify: `internal/index/watch.go`（全面的に書き換え）
- Modify: `internal/index/scan.go`（全面的に書き換え）
- Modify: `internal/index/export_test.go`
- Modify: `internal/index/indexer_test.go`、`internal/index/scan_test.go`、`internal/index/watch_test.go`
- Modify: `internal/web/browser_test.go:350-356`
- Modify: `main.go:140-180`
- Modify: `docs/superpowers/specs/2026-10-05-index-syncer-design.md`（`DisableAutoScan` を interval 0 に置き換える）

**Interfaces:**
- Consumes: `isUnder` / `isUnderAny`（`scan.go`、そのまま残す）、Task 1 の `unwatchUnder`、Task 2 の `isRoot`（どちらも `Syncer` のメソッドに移す）
- Produces:
  - `func New(roots []string, st *store.Store, thumbs *thumb.Provider, log *slog.Logger) *Indexer`
  - `func (ix *Indexer) purgeGone(ctx context.Context, path string) (bool, error)`
  - `func NewSyncer(ix *Indexer, workers int, interval time.Duration, log *slog.Logger) (*Syncer, error)`
  - `func (sy *Syncer) Run(ctx context.Context) error`、`func (sy *Syncer) Scan(ctx context.Context) (Stats, error)`、`func (sy *Syncer) Close() error`
  - `syncState` とそのメソッド（`watch` / `scanHasRoom` / `queueScanTask` / `requestScan` / `walkFinished` / `finishedScan` / `next` / `finish` / `forget` / `dropWaiters`）
  - `type task struct{ kind taskKind; path string; fromScan bool }`、`taskIndex` / `taskPurge`
  - `type scanMsg struct{ task task; done bool; unchanged, skipped int; err error }`
  - `func newScanWalk(ix *Indexer, log *slog.Logger, out chan<- scanMsg) *scanWalk`、`func (w *scanWalk) run(ctx context.Context)`
  - `func (sy *Syncer) addTree(root string) error`、`func (sy *Syncer) enqueueTree(st *syncState, root string)`
  - テスト専用: `func (sy *Syncer) SetDebounce(d time.Duration)`、`func (sy *Syncer) InjectWatchError(err error)`

- [ ] **Step 1: 設計書を直す**

`docs/superpowers/specs/2026-10-05-index-syncer-design.md` を3箇所直す。ブラウザテスト（`internal/web`）も `Scan` の件数を見ており、`export_test.go` の口は他のパッケージから呼べないため、自動の走査を止める手段を公開の振る舞いにする。

「公開API」の節の `Run` の項目を置き換える。

```markdown
- `Run` は起動直後に1回目の全体の走査を始め、以後 interval ごとに繰り返す。interval は
  1回の完了から次の開始までの間隔（今と同じ）。interval が0以下なら自動では走査せず、
  走査は `Scan` の呼び出しと fsnotify の溢れでだけ起きる。テストと、1回だけ走査したい
  使い方のためにある。本番の設定は `config.Validate` が正の値に限っている
```

「テスト専用の口（`export_test.go`）」の節の `DisableAutoScan()` の項目を消す。

「引き換えになるもの」の節の「テスト専用の口が1つ増える（`DisableAutoScan`）」の行を消す。

「削除の仕事」の節の2段落目を置き換える。

```markdown
ワーカーは消す前にそのパスを Lstat し、ファイルがあれば消さずに戻る。走査の後で
作り直されたファイルの行を消さないため。確かめてから消すまでの間に作られた場合は、
その Create が監視側の保留に入り、排他により削除の完了後に取り込まれるので行は戻る。
どのルートの配下でもないパスは、ファイルがあっても確かめずに消す。インデックスは
「いま指定されているルート」に従うためである。
```

- [ ] **Step 2: テストの土台を書き換える**

`internal/index/indexer_test.go` の `fixture` から `sc` を外し、`workers` を足す。

```go
type fixture struct {
	ix       *index.Indexer
	workers  int
	st       *store.Store
	thumbs   *thumb.Provider
	root     string
	thumbDir string
	log      *slog.Logger
}
```

`newFixtureWorkers` の末尾を置き換える。

```go
	ix := index.New([]string{root}, st, thumbs, log)

	return &fixture{ix: ix, workers: workers,
		st: st, thumbs: thumbs, root: root, thumbDir: thumbDir, log: log}
}
```

`newFixtureRoots` の末尾を置き換える。

```go
	ix := index.New(roots, st, thumbs, log)

	return &fixture{ix: ix, workers: 4,
		st: st, thumbs: thumbs, root: roots[0], thumbDir: thumbDir, log: log}, roots
}
```

`newFixtureRoots` の直後に足す。

```go
// scan は全体の走査を1回だけ走らせて結果を返す。
//
// 走らせるたびに Syncer を作り、終わったら止める。走査と走査の間にした変更を
// 監視に拾わせないためで、監視のない走査だけの振る舞いを確かめられる。
// interval を0にするので、Run が自分で走査を始めることはない。
func (f *fixture) scan(t *testing.T, ctx context.Context) (index.Stats, error) {
	t.Helper()
	return scanWith(t, ctx, f.ix, f.workers, f.log)
}

// scanWith は ix で全体の走査を1回だけ走らせて結果を返す。
func scanWith(t *testing.T, ctx context.Context, ix *index.Indexer, workers int, log *slog.Logger) (index.Stats, error) {
	t.Helper()
	sy, err := index.NewSyncer(ix, workers, 0, log)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sy.Run(runCtx)
	}()
	defer func() {
		cancel()
		<-done
		require.NoError(t, sy.Close())
	}()
	return sy.Scan(ctx)
}
```

`TestNewPanicsOnWorkersBelowOne` を置き換える。

```go
func TestNewSyncerPanicsOnWorkersBelowOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, workers := range []int{0, -1} {
		require.PanicsWithValue(t,
			fmt.Sprintf("index: workers must be 1 or greater: %d", workers),
			func() { _, _ = index.NewSyncer(f.ix, workers, 0, f.log) })
	}
}
```

`indexer_test.go` の import から `time` が使われなくなったら消す（`go vet` が教える）。

`internal/index/watch_test.go` の `startWatcher` を置き換える。

```go
// startSyncer は Syncer をバックグラウンドで動かし、停止まで面倒を見る。
// interval は0なので、全体の走査は Scan の呼び出しと溢れでだけ起きる。
func startSyncer(t *testing.T, f *fixture) *index.Syncer {
	t.Helper()
	return startSyncerWith(t, f, 0, testDebounce)
}

// startSyncerWith は間隔と debounce を指定して Syncer を動かす。
func startSyncerWith(t *testing.T, f *fixture, interval, debounce time.Duration) *index.Syncer {
	t.Helper()
	sy, err := index.NewSyncer(f.ix, f.workers, interval, f.log)
	require.NoError(t, err)
	sy.SetDebounce(debounce)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sy.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		require.NoError(t, sy.Close())
	})
	time.Sleep(50 * time.Millisecond) // 監視の登録が終わるのを待つ
	return sy
}
```

機械的な置き換えをする。

```bash
sed -i 's/TestWatcher/TestWatch/; s/startWatcher(t, f)/startSyncer(t, f)/' internal/index/watch_test.go
sed -i 's/f\.sc\.Scan(\(ctx\|context\.Background()\))/f.scan(t, \1)/' internal/index/scan_test.go
```

- [ ] **Step 3: 前提が変わったテストを書き換える**

`internal/index/watch_test.go`:

`TestWatchWatchesRootsBeforeRunStarts` を置き換える。

```go
func TestWatchWatchesRootsBeforeRunStarts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// interval を0にして、Run が自分で走査しないようにする。拾えるのは監視だけになる。
	sy, err := index.NewSyncer(f.ix, f.workers, 0, f.log)
	require.NoError(t, err)
	sy.SetDebounce(testDebounce)

	// NewSyncer が戻った時点で監視が張れていること。起動時は「監視を張る →
	// スキャン」の順にするので、Run の開始を待ってから張るのでは間に合わない。
	// スキャンが走査を終えたあとに置かれた写真を、どちらも拾えなくなる。
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sy.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		require.NoError(t, sy.Close())
	})

	requireCount(t, f, 1)
}
```

`TestWatchAsksForAScanWhenAFileDisappearsWhileBeingIndexed` と `TestWatchDoesNotAskForAScanWhenNothingIsBeingIndexed` を消す。前倒しの要求という仕組みが無くなり、取り込み中に消えた写真は入口によらず墓標で取り消す。それを見るのは下の `TestScanLeavesNoRowForAFileMovedWhileBeingIndexed`。

`TestWatchAsksForAScanOnEventOverflow` と `TestWatchDoesNotAskForAScanOnOtherWatchErrors` を置き換える。

```go
// 溢れは、変更を取りこぼしたと確実に分かる唯一の合図である。落ちたぶんを
// 取り戻せるのは全体の走査だけなので、走らせる。
func TestWatchScansAfterAnEventOverflow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// 監視を張る前に置くので、イベントは来ない。拾えるのは走査だけである。
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	sy := startSyncer(t, f)

	sy.InjectWatchError(fsnotify.ErrEventOverflow)

	requireCount(t, f, 1)
}

func TestWatchDoesNotScanOnOtherWatchErrors(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	sy := startSyncer(t, f)

	sy.InjectWatchError(errors.New("another watch failure"))

	time.Sleep(3 * testDebounce)
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n, "an error other than an overflow must not trigger a scan")
}
```

`watch_test.go` の import の `io` と `log/slog` が使われなくなったら消す。

`internal/index/scan_test.go`:

`TestScanRemovesMediaOutsideEveryRoot` の2行を置き換える。

```go
	// bob を引数から外して起動し直した状況を模す
	ix2 := index.New(roots[:1], f.st, f.thumbs, f.log)

	stats, err := scanWith(t, ctx, ix2, 4, f.log)
```

`TestScanDoesNotWaitForTheWatchersIndexing` を置き換える。

```go
func TestScanDoesNotWaitForTheWatchersIndexing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// 監視を張る前に置くのでCreateのイベントは飛ばない。この1枚はスキャンだけが拾う。
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	sy := startSyncer(t, f)

	// 監視の側に終わらない取り込みを1件持たせる。
	stuck := mkfifoMedia(t, f.root, "stuck.heic")
	w := waitForIndexing(t, stuck)
	// 取り込みを解く係を先に登録する。書き手を閉じればEOFが渡り、中身の検査が
	// 「写真ではない」と判断して取り込みは終わる。ここで登録しておかないと、
	// 検証に失敗して途中で終わったときに停止が取り込みを待って固まる。
	t.Cleanup(func() { _ = w.Close() })

	// ルートの外へ移す。スキャンの走査はこれを見つけないので、
	// 走っているのはスキャンが自分では出していない仕事だけになる。
	moved := filepath.Join(t.TempDir(), "stuck.heic")
	require.NoError(t, os.Rename(stuck, moved))

	type scanResult struct {
		stats index.Stats
		err   error
	}
	done := make(chan scanResult, 1)
	go func() {
		stats, err := sy.Scan(context.Background())
		done <- scanResult{stats, err}
	}()

	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.Equal(t, 1, r.stats.Indexed)
	case <-time.After(2 * time.Second):
		t.Fatal("Scan is waiting for the watcher to finish indexing")
	}
}
```

`TestScannerRunKeepsReconcilingOnItsInterval` を置き換える。

```go
func TestSyncerRunKeepsReconcilingOnItsInterval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	// debounce を1時間にして、監視からは取り込ませない。
	// 拾えるのは間隔ごとの走査だけになる。
	startSyncerWith(t, f, 50*time.Millisecond, time.Hour)

	requireCount(t, f, 1)

	writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	requireCount(t, f, 2)
}
```

`TestGhostRowFromAScanIsReclaimedByTheNextScan` を次で置き換える。幽霊行は「次のスキャンで回収される」のではなく、もう生まれない。

```go
// スキャンが取り込んでいる最中に写真が消えても、行を残さない。墓標は
// 取り込みを出したのが監視でもスキャンでも立つ。
func TestScanLeavesNoRowForAFileMovedWhileBeingIndexed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// ルートを空にしない。1枚も見つからないルートの配下は purge が見送るため。
	writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	album := filepath.Join(f.root, "album")
	require.NoError(t, os.MkdirAll(album, 0o755))
	src := mkfifoMedia(t, album, "a.heic")

	// 監視の前から在るファイルにはCreateのイベントが飛ばない。
	// この2枚を取り込むのはスキャンだけである。
	sy := startSyncer(t, f)

	type scanResult struct {
		stats index.Stats
		err   error
	}
	scanDone := make(chan scanResult, 1)
	go func() {
		stats, err := sy.Scan(context.Background())
		scanDone <- scanResult{stats, err}
	}()
	fw := waitForIndexing(t, src)
	t.Cleanup(func() { _ = fw.Close() })

	// 取り込みの最中にディレクトリごとルートの外へ移す。行が生まれるのは
	// 取り込みの完了時なので、この時点の削除は空振りする。
	require.NoError(t, os.Rename(album, filepath.Join(t.TempDir(), "album")))
	time.Sleep(50 * time.Millisecond) // 削除が取り込みの完了より先に処理される順序を作る

	// 中身の検査がこのFIFOの最初の読み手なので、署名を流してから閉じる。空のまま
	// 閉じると「写真ではない」と判断され、行そのものが生まれない。移動済みの
	// パスを開き直す後続の読み手は、存在しないパスとして空振りする。
	_, err := fw.Write(testHEIC())
	require.NoError(t, err)
	require.NoError(t, fw.Close())

	r := <-scanDone
	require.NoError(t, r.err)
	require.Equal(t, 1, r.stats.Indexed, "the moved file is not counted as indexed")
	requireCount(t, f, 1)
	paths, err := f.st.AllPaths(context.Background())
	require.NoError(t, err)
	_, ok := paths[filepath.Join(f.root, "b.jpg")]
	require.True(t, ok, "only b.jpg remains")
}
```

`TestScannerRunIsBroughtForwardByARequest` を消す。前倒しの要求は `Scan` の呼び出しそのものになり、`Scan` を使うすべてのテストがそれを通る。

- [ ] **Step 4: 新しい振る舞いのテストを書く**

`internal/index/scan_test.go` の末尾に足す。

```go
// 監視の取り込みは、スキャンの取り込みより先に走る。枠が空いたとき、監視側の
// 保留にあるものから渡す。逆だと、DBを作り直した直後のような長いスキャンの間、
// 新しく置いた写真がスキャンの終わりまで載らない。
func TestScanYieldsToWatchedChanges(t *testing.T) {
	t.Parallel()
	f := newFixtureWorkers(t, 1)
	first := mkfifoMedia(t, f.root, "s1.heic")
	second := mkfifoMedia(t, f.root, "s2.heic")
	sy := startSyncer(t, f)

	scanDone := make(chan error, 1)
	go func() {
		_, err := sy.Scan(context.Background())
		scanDone <- err
	}()

	// 1つしかないワーカーをスキャンの1枚目で塞ぐ。2枚目はスキャン側の保留で待つ。
	w1 := waitForIndexing(t, first)
	t.Cleanup(func() { _ = w1.Close() })

	// 監視に新しい写真を拾わせ、debounce が過ぎるまで待つ。
	writeTestJPEG(t, f.root, "w.jpg", 40, 20)
	time.Sleep(3 * testDebounce)

	// 1枚目を通す。
	_, err := w1.Write(testHEIC())
	require.NoError(t, err)
	require.NoError(t, w1.Close())
	serveFifo(t, first, testHEIC())

	// 次にワーカーへ渡るのが監視側の w.jpg なら、s2.heic を待たずに載る。
	// スキャン側の s2.heic が先なら、ワーカーはその open で止まり、w.jpg は載らない。
	requireCount(t, f, 2)

	w2 := waitForIndexing(t, second)
	_, err = w2.Write(testHEIC())
	require.NoError(t, err)
	require.NoError(t, w2.Close())
	serveFifo(t, second, testHEIC())
	require.NoError(t, <-scanDone)
	requireCount(t, f, 3)
}

// 同じパスを2つのワーカーが同時に取り込まない。版の違う同じ写真を同時に取り込むと、
// 古い版のワーカーが新しい版のサムネイルを消しうる。取り込み中のパスは保留に
// 残し、終わってから渡す。
func TestScanAndWatchDoNotIndexAPathTwiceAtOnce(t *testing.T) {
	t.Parallel()
	f := newFixtureWorkers(t, 2)
	path := mkfifoMedia(t, f.root, "a.jpg")
	sy := startSyncer(t, f)

	scanDone := make(chan error, 1)
	go func() {
		_, err := sy.Scan(context.Background())
		scanDone <- err
	}()
	fw := waitForIndexing(t, path)
	t.Cleanup(func() { _ = fw.Close() })

	// 取り込み中のパスを、普通のJPEGで置き換える。監視には Create として届く。
	staging := writeTestJPEG(t, t.TempDir(), "a.jpg", 40, 20)
	require.NoError(t, os.Rename(staging, path))
	time.Sleep(3 * testDebounce)

	// ワーカーは2つあり1つは空いているが、同じパスはスキャンが取り込み中なので
	// 渡さない。渡していれば、置き換えた普通のJPEGがすぐに載る。
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n, "the watcher waits for the scan to finish the same path")

	// 止めていた取り込みを通す。中身の検査はFIFOから読み、そのあとの読み取りは
	// 置き換えたファイルを開く。
	_, err = fw.Write(testJPEG(t, 40, 20))
	require.NoError(t, err)
	require.NoError(t, fw.Close())
	require.NoError(t, <-scanDone)
	requireCount(t, f, 1)
}

// スキャンは歩いている間に見つけられなかったパスを消しにくるが、消す前に
// 同じ名前で置き直されていたら、その行は消さない。
func TestScanKeepsTheRowOfAFileRecreatedBeforeThePurge(t *testing.T) {
	t.Parallel()
	f := newFixtureWorkers(t, 1)
	ctx := context.Background()

	// 監視の無い間に消えた写真を作る。行だけが残る。
	gone := writeTestJPEG(t, f.root, "gone.jpg", 40, 20)
	require.NoError(t, f.ix.IndexFile(ctx, gone))
	require.NoError(t, os.Remove(gone))
	// 1つしかないワーカーを塞ぐ写真。ルートを空にしない役も兼ねる。
	stuck := mkfifoMedia(t, f.root, "stuck.heic")

	sy := startSyncer(t, f)
	type scanResult struct {
		stats index.Stats
		err   error
	}
	scanDone := make(chan scanResult, 1)
	go func() {
		stats, err := sy.Scan(ctx)
		scanDone <- scanResult{stats, err}
	}()
	fw := waitForIndexing(t, stuck)
	t.Cleanup(func() { _ = fw.Close() })

	// スキャンは gone.jpg を見つけられず、それを消す仕事を保留に積んでいる。
	// その間に同じ名前で写真が置き直され、監視が拾って debounce が過ぎる。
	writeTestJPEG(t, f.root, "gone.jpg", 40, 20)
	time.Sleep(3 * testDebounce)

	_, err := fw.Write(testHEIC())
	require.NoError(t, err)
	require.NoError(t, fw.Close())
	serveFifo(t, stuck, testHEIC())

	r := <-scanDone
	require.NoError(t, r.err)
	require.Equal(t, 0, r.stats.Removed, "the file is there again, so its row is not removed")
	requireCount(t, f, 2)
}
```

`internal/web/browser_test.go` の `indexAll` を置き換える。

```go
// indexAll は本番と同じ取り込み経路でコーパスをインデックスに載せる。
// 手でMediaを組むと、Mediaの構造が変わるたびにブラウザテストが巻き添えになる。
//
// interval を0にして、Run が自分で走査を始めないようにする。そうしないと
// Scan が数えるのは起動直後の1回のあとの2回目になり、Indexed が0になる。
func indexAll(st *store.Store, mediaDir string, thumbs *thumb.Provider) (index.Stats, error) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ix := index.New([]string{mediaDir}, st, thumbs, log)
	sy, err := index.NewSyncer(ix, 4, 0, log)
	if err != nil {
		return index.Stats{}, err
	}
	defer sy.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = sy.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	return sy.Scan(ctx)
}
```

- [ ] **Step 5: 失敗を確かめる**

Run: `go vet ./internal/index/`
Expected: FAIL。`undefined: index.NewSyncer`、`undefined: index.Syncer`、`too many arguments in call to index.New` など

- [ ] **Step 6: `Indexer` から枠を外す**

`internal/index/indexer.go`:

- import から `sync` を消し、`errors` と `io/fs` を足す
- `Indexer` の型コメントとフィールドを置き換える

```go
// Indexer は写真1枚の変更をインデックスとサムネイルに反映する。どこを見てどこに
// 書くかを持つ。インデックスのデータ自体は store にある。
//
// どれも同期的で、並行について何も知らない。いつ、いくつ同時に走らせるかは
// Syncer が決める。
type Indexer struct {
	roots  []string
	store  *store.Store
	thumbs *thumb.Provider
	log    *slog.Logger
}

// New はIndexerを作る。rootsは写真を収集するルートディレクトリ。
//
// thumbs は配信側と共有する。同じ置き場所を指す設定値を2経路に配ると、
// ずれても誰も気づけないため、組み立てたものを1つ受け取る。
func New(roots []string, st *store.Store, thumbs *thumb.Provider, log *slog.Logger) *Indexer {
	return &Indexer{
		roots:  roots,
		store:  st,
		thumbs: thumbs,
		log:    log,
	}
}
```

- `startIndex`、`tryStartIndex`、`run`、`busy`、`slots` 型とそのメソッド（`newSlots` / `take` / `tryTake` / `release` / `busy` / `cap`）をすべて消す
- `removeTree` の直後に足す

```go
// purgeGone は path にファイルが無ければ、インデックスとサムネイルから消す。
// 行を消したかどうかを返す。
//
// スキャンは歩いている間に見つけられなかったパスを消しにくるが、その判定と
// この削除の間には時間が空く。その間に同じ名前で置き直されたファイルの行を
// 消さないよう、消す直前に確かめる。確かめてから消すまでの間に置かれた場合は、
// その Create を監視が拾い、同じパスの仕事は同時に走らないので、この削除の
// あとに取り込まれて行が戻る。
//
// どのルートの配下でもないパスは確かめずに消す。インデックスは「いま指定されて
// いるルート」に従うので、ファイルがあっても載せておく理由がない。
func (ix *Indexer) purgeGone(ctx context.Context, path string) (bool, error) {
	if isUnderAny(ix.roots, path) {
		_, err := os.Lstat(path)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("cannot stat the file: %w", err)
		}
	}
	if err := ix.removeFile(ctx, path); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 7: `syncState` を書く**

`internal/index/syncstate.go` を作る。

```go
package index

import "time"

// taskKind はワーカーに渡す仕事の種類。
type taskKind int

const (
	// taskIndex は1枚を取り込む。
	taskIndex taskKind = iota
	// taskPurge は全体の走査が見つけられなかった1枚を、無いことを確かめてから消す。
	taskPurge
)

// task はワーカーに渡す仕事1件。
type task struct {
	kind taskKind
	path string
	// fromScan は全体の走査が出した仕事であることを表す。スキャン1回の集計と
	// 完了の判定に入る。
	fromScan bool
}

// taskResult はワーカーから返る、終わった仕事1件。
type taskResult struct {
	task task
	err  error
	// purged は taskPurge が行を実際に消したことを表す。
	purged bool
}

// scanOutcome は Scan の呼び出し元に返す、スキャン1回の結果。
type scanOutcome struct {
	stats Stats
	err   error
}

// scanProgress は走っている全体の走査1回の進み具合。
type scanProgress struct {
	started time.Time
	// walking は走査の goroutine がまだ歩いていることを表す。
	walking bool
	// pending はこの走査が送った仕事のうち、まだ終わっていないものの数。
	// 保留にあるものと実行中のものの両方を数える。
	pending int
	stats   Stats
	err     error
	// waiters はこの1回の結果を待つ Scan の呼び出し元。
	waiters []chan scanOutcome
}

// syncState は Syncer のループが持つ状態と、次に何をするかの判断である。
//
// goroutine を持たず、チャネルにも fsnotify にもファイルシステムにも触らない。
// ループの goroutine だけが触るので、ロックは要らない。
type syncState struct {
	workers  int
	debounce time.Duration

	// watched は監視側の保留。パス → 最後にイベントを受けた時刻。
	watched map[string]time.Time
	// scanned はスキャン側の保留。全体の走査が送った順に並ぶ。
	scanned []task
	// inflight は実行中の仕事のパス。値は墓標で、実行中にそのパスが消えたら true。
	inflight map[string]bool

	// scan は走っている全体の走査。無ければ nil。
	scan *scanProgress
	// rescan は走査中に次の全体の走査が要求されたことを表す。要求は1つにまとまる。
	rescan bool
	// nextWaiters は次の全体の走査の結果を待つ Scan の呼び出し元。
	nextWaiters []chan scanOutcome
}

func newSyncState(workers int, debounce time.Duration) *syncState {
	return &syncState{
		workers:  workers,
		debounce: debounce,
		watched:  make(map[string]time.Time),
		inflight: make(map[string]bool),
	}
}

// watch は監視側の保留にパスを積む。既にあれば時刻が新しくなり、debounce が延びる。
func (s *syncState) watch(path string, now time.Time) { s.watched[path] = now }

// scanHasRoom は全体の走査から仕事を受け取ってよいかを返す。
//
// スキャン側の保留は workers 件までにする。すぐ渡せる仕事なので、ワーカーが
// 空いた時点で次があれば足りる。それ以上受け取ると、走査が先に走って数千件の
// パスを溜め込む。
func (s *syncState) scanHasRoom() bool { return len(s.scanned) < s.workers }

// queueScanTask は全体の走査が送った仕事をスキャン側の保留に積む。
func (s *syncState) queueScanTask(t task) {
	s.scanned = append(s.scanned, t)
	s.scan.pending++
}

// requestScan は全体の走査を要求する。新しく始めてよければ true を返す。
//
// 走っている最中なら、終わったあとにもう1回走らせる印を立てて false を返す。
// 走っている1回は要求より前に歩き始めているので、要求の時点のディスクを反映
// している保証が無いため。
//
// reply は結果を待つ Scan の呼び出し元で、nil なら誰も待っていない。
func (s *syncState) requestScan(reply chan scanOutcome) bool {
	if reply != nil {
		s.nextWaiters = append(s.nextWaiters, reply)
	}
	if s.scan != nil {
		s.rescan = true
		return false
	}
	s.beginScan()
	return true
}

func (s *syncState) beginScan() {
	s.scan = &scanProgress{walking: true, waiters: s.nextWaiters}
	s.nextWaiters = nil
	s.rescan = false
}

// walkFinished は走査の goroutine が歩き終えたことを記録する。
func (s *syncState) walkFinished(unchanged, skipped int, err error) {
	s.scan.walking = false
	s.scan.stats.Unchanged += unchanged
	s.scan.stats.Skipped += skipped
	s.scan.err = err
}

// finishedScan は全体の走査が完了していれば、それを状態から外して返す。
// 完了していなければ nil を返す。
//
// 完了とは、歩き終えていて、送った仕事がすべて終わっていることである。
// 走査中にもう1回の要求が来ていれば、続く1回をここで始め、again を true にする。
func (s *syncState) finishedScan() (done *scanProgress, again bool) {
	if s.scan == nil || s.scan.walking || s.scan.pending > 0 {
		return nil, false
	}
	done = s.scan
	s.scan = nil
	if s.rescan {
		s.beginScan()
		return done, true
	}
	return done, false
}

// next は次にワーカーへ渡す仕事を選び、実行中に移して返す。
// 渡せるものが無ければ false を返す。
//
// 監視側を先に見る。新しく置かれた写真が、長いスキャンの終わりまで待たされない
// ようにするため。
//
// 実行中のパスは渡さず、保留に残す。同じ写真を2つのワーカーが同時に取り込むと、
// 古い版のワーカーが新しい版のサムネイルを消しうる。実行中に届いた変更は保留の
// 時刻を延ばすだけなので、終わったあとにもう一度取り込まれる。
func (s *syncState) next(now time.Time) (task, bool) {
	if len(s.inflight) >= s.workers {
		return task{}, false
	}
	for path, last := range s.watched {
		if now.Sub(last) < s.debounce {
			continue
		}
		if _, busy := s.inflight[path]; busy {
			continue
		}
		delete(s.watched, path)
		s.inflight[path] = false
		return task{kind: taskIndex, path: path}, true
	}
	for i, t := range s.scanned {
		if _, busy := s.inflight[t.path]; busy {
			continue
		}
		s.scanned = append(s.scanned[:i], s.scanned[i+1:]...)
		s.inflight[t.path] = false
		return t, true
	}
	return task{}, false
}

// finish は終わった仕事を実行中から外し、全体の走査が出したものなら集計に入れる。
// 実行中にパスが消えていた取り込みなら true を返す。呼び出し側は、今しがた
// 入った行を取り消す。
//
// stopping は停止の最中であることを表す。中断で落ちたぶんは失敗として数えない。
func (s *syncState) finish(r taskResult, stopping bool) (undo bool) {
	removed := s.inflight[r.task.path]
	delete(s.inflight, r.task.path)
	undo = removed && r.task.kind == taskIndex
	if !r.task.fromScan || s.scan == nil {
		return undo
	}
	s.scan.pending--
	switch {
	case removed:
		// 行は取り消すか、もう無い。消したのはスキャンではないので Removed でもない。
	case r.task.kind == taskIndex && r.err == nil:
		s.scan.stats.Indexed++
	case stopping:
	case r.task.kind == taskIndex:
		s.scan.stats.Skipped++
	case r.err == nil && r.purged:
		s.scan.stats.Removed++
	}
	return undo
}

// forget は p が消えたことを反映する。p 配下の保留を捨て、実行中のものに墓標を
// 立てる。墓標は取り込みを出したのが監視でもスキャンでも立つ。
//
// 捨てたスキャン側の仕事はどこにも数えず、完了の判定がそれを待たないように
// 送った数から引く。
func (s *syncState) forget(p string) {
	for path := range s.watched {
		if isUnder(p, path) {
			delete(s.watched, path)
		}
	}
	kept := s.scanned[:0]
	for _, t := range s.scanned {
		if isUnder(p, t.path) {
			s.scan.pending--
			continue
		}
		kept = append(kept, t)
	}
	s.scanned = kept
	for path := range s.inflight {
		if isUnder(p, path) {
			s.inflight[path] = true
		}
	}
}

// dropWaiters は結果を待っている Scan の呼び出し元をすべて返し、状態から外す。
// 停止するときに、待っている側へ返すために使う。
func (s *syncState) dropWaiters() []chan scanOutcome {
	waiters := s.nextWaiters
	s.nextWaiters = nil
	if s.scan != nil {
		waiters = append(waiters, s.scan.waiters...)
		s.scan.waiters = nil
	}
	return waiters
}
```

- [ ] **Step 8: 全体の走査を書き換える**

`internal/index/scan.go` を全面的に置き換える。

```go
package index

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/synology"
)

// Stats はスキャンの結果。
type Stats struct {
	Indexed   int // 新規登録または更新した枚数
	Unchanged int // mtimeが変わらず再処理しなかった枚数
	Removed   int // ディスクから消えていたためインデックスから消した枚数
	Skipped   int // 破損・権限エラーで飛ばした枚数
}

// scanMsg は全体の走査からループへ送るもの。仕事1件か、歩き終えた報告のどちらか。
type scanMsg struct {
	task task
	// done は歩き終えたことを表す。以降この走査から仕事は来ない。
	done bool
	// 以下は done のときだけ意味を持つ。走査の側で数えたぶんである。
	unchanged, skipped int
	err                error
}

// scanWalk は全体の走査1回ぶんの帳簿。走査の goroutine だけが触る。
//
// 走査は仕事をループへ送るだけで、取り込みも削除も自分ではしない。取り込みは
// ワーカーが行い、どれを先に走らせるかはループが決める。
type scanWalk struct {
	ix  *Indexer
	log *slog.Logger
	out chan<- scanMsg

	// registered は登録済みのパスとそのmtime。走査で見つけたぶんを消し込み、
	// 残ったものが削除されたファイルになる。
	registered map[string]int64
	// foundByRoot はルートごとの発見数。空/未マウントかどうかをルート単位で判定する
	// ために使う。合計で数えると、生きているルートに写真がある限りガードが
	// 発動しない。
	foundByRoot        map[string]int
	unchanged, skipped int
}

func newScanWalk(ix *Indexer, log *slog.Logger, out chan<- scanMsg) *scanWalk {
	return &scanWalk{ix: ix, log: log, out: out, foundByRoot: make(map[string]int, len(ix.roots))}
}

// run は全体を1回歩いて仕事を送り、最後に歩き終えたことを報告する。
//
// fsnotifyはアプリが停止していた間の変更を検知できないため、起動のたびにこれを
// 実行して整合性を取り直す。個々のファイルのエラーは記録して走査を続け、
// コンテキストのキャンセルだけが全体を中断させる。
func (w *scanWalk) run(ctx context.Context) {
	err := w.walkAll(ctx)
	if err == nil {
		w.purge(ctx)
	}
	w.send(ctx, scanMsg{done: true, unchanged: w.unchanged, skipped: w.skipped, err: err})
}

// send はループへ1つ送る。ループが受け取るまで待つ。この待ちがスキャンへの背圧で
// ある。ctx が終わったら送らずに false を返す。
func (w *scanWalk) send(ctx context.Context, m scanMsg) bool {
	select {
	case w.out <- m:
		return true
	case <-ctx.Done():
		return false
	}
}

// walkAll はすべてのルートを走査する。
func (w *scanWalk) walkAll(ctx context.Context) error {
	registered, err := w.ix.store.AllPaths(ctx)
	if err != nil {
		return err
	}
	w.registered = registered

	for _, root := range w.ix.roots {
		if err := w.walk(ctx, root); err != nil {
			if ctx.Err() != nil {
				return err
			}
			// ルート自体を読めない（ボリュームが外れた等）。1つのドライブが
			// 外れただけで走査全体を止めると、生きているルートの更新まで
			// 反映されなくなる。このルートは foundByRoot が0のままなので、配下の
			// 削除は purge のガードが自動的に見送る。
			w.log.Warn("skipped an unreadable root", "root", root, "err", err)
		}
	}
	return nil
}

// walk は1つのルート以下を走査する。
//
// 走査自体は直列のままにする。registered の消し込みも foundByRoot の計上も、共有する
// マップの上での帳簿づけであり、並行にしても速くならないのに壊れる余地だけが
// 増える。時間を食う1枚の取り込みだけをループ経由でワーカーに出す。
func (w *scanWalk) walk(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			if path == root {
				// ルート自体が読めない場合は「中身が空だった」と区別できないため
				// 削除フェーズに進まず、走査全体を中断する。
				return err
			}
			// 読めないディレクトリやファイルは飛ばす（権限エラーなど）
			w.log.Warn("skipped while walking", "path", path, "err", err)
			w.skipped++
			return nil
		}
		if d.IsDir() {
			if synology.IsManagedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !imagefmt.IsSupported(path) {
			return nil
		}
		// シンボリックリンクは写真として数えない。取り込みは indexFile が
		// 断るので、ここで落とさなくても行は入らないが、数えると「見つけた
		// のに入らなかった」ぶんが Indexed に混ざり、空のルートを判定する
		// foundByRoot も嵩上げされる。
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		w.foundByRoot[root]++

		fi, err := d.Info()
		if err != nil {
			w.log.Warn("skipped, cannot stat the file", "path", path, "err", err)
			w.skipped++
			return nil
		}

		// 見つかったパスは消し込む。走査後に残ったものが削除されたファイル。
		modTime, wasRegistered := w.registered[path]
		delete(w.registered, path)
		if wasRegistered && modTime == fi.ModTime().Unix() {
			w.unchanged++
			return nil
		}

		if !w.send(ctx, scanMsg{task: task{kind: taskIndex, path: path, fromScan: true}}) {
			return ctx.Err()
		}
		return nil
	})
}

// purge は走査で見つからなかった写真を消す仕事を送る。
//
// ループの中では消さない。数千件を消すことがあり、その間イベントを読めなくなる。
func (w *scanWalk) purge(ctx context.Context) {
	empty := w.emptyRoots()

	guarded := 0
	for path := range w.registered {
		if isUnderAny(empty, path) {
			guarded++
			continue
		}
		if !w.send(ctx, scanMsg{task: task{kind: taskPurge, path: path, fromScan: true}}) {
			return
		}
	}
	if guarded > 0 {
		w.log.Warn("skipped deletions because a root scanned empty",
			"roots", empty, "remaining", guarded)
	}
}

// emptyRoots は1枚も見つからなかったルートを返す。
//
// そのルートは、ドライブが未マウントで「たまたま空に見える」のか、本当に全部
// 消されたのかを区別できない。安全側に倒して、配下の削除を見送るために使う。
func (w *scanWalk) emptyRoots() []string {
	var empty []string
	for _, root := range w.ix.roots {
		if w.foundByRoot[root] == 0 {
			empty = append(empty, root)
		}
	}
	return empty
}

// isUnderAny は path がいずれかのルート配下にあるかを返す。
func isUnderAny(roots []string, path string) bool {
	for _, root := range roots {
		if isUnder(root, path) {
			return true
		}
	}
	return false
}

// isUnder は path が root 配下にあるかを返す。
// セパレータを1つ補ってから前方一致させるため、"/a" が "/ab" を巻き込まない。
func isUnder(root, path string) bool {
	if path == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(path, root)
}
```

- [ ] **Step 9: `Syncer` を書く**

`internal/index/syncer.go` を作る。

```go
package index

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// defaultDebounce は最後の書き込みイベントから実際にインデックスするまでの
// 待ち時間。コピー途中のファイルをデコードしに行かないための猶予。
const defaultDebounce = 2 * time.Second

// Syncer はディスク上の写真とインデックスを同期させる。fsnotify で差分を追い、
// 間隔をおいて全体を突き合わせ直す。
//
// 入口をまたぐ状態と判断（保留、取り込み中のパスと墓標、スキャン1回の進み具合、
// どれを先に取り込むか）は、Run のループの goroutine だけが持つ。判断そのものは
// syncState にあり、ループは入力を受けてそれを呼び、返った指示を実行する。
//
// ループは写真の取り込みも全体の走査も自分では走らせない。どちらも所要時間が
// 写真の枚数やサイズに依存し、その間 fsnotify のイベントを読めなくなるため。
// 読めない時間が延びるとカーネルのキューが溢れ、変更そのものを取りこぼす。
// 取り込みはワーカーの goroutine、全体の走査は走査の goroutine が担う。
//
// fsnotify は取りこぼす。キューが溢れたことは ErrEventOverflow で分かるが、
// max_user_watches を使い切って監視を張れなかったディレクトリのように、
// 取りこぼしたことを知る手立てが無い経路もある。間隔ごとの全体の走査が、
// 検知できたかどうかによらず整合性を戻す。
type Syncer struct {
	ix       *Indexer
	fsw      *fsnotify.Watcher
	log      *slog.Logger
	workers  int
	interval time.Duration
	debounce time.Duration

	// results は終わった仕事の受け口。実行中の仕事は workers 件を超えないので、
	// 容量をそれに合わせればワーカーがここで止まることはない。
	results chan taskResult
	// scanFound は全体の走査がループへ仕事を送る口。容量を持たない。背圧は
	// ループが受け取るかどうかで掛ける。
	scanFound chan scanMsg
	// scanReqs は Scan の呼び出しを受ける口。
	scanReqs chan chan scanOutcome

	// tasks は走っているワーカー、walks は走っている走査。停止時に待つ。
	tasks sync.WaitGroup
	walks sync.WaitGroup
}

// NewSyncer はSyncerを作り、ルート以下を監視対象に加える。
//
// workers は取り込みを同時に走らせるワーカーの数。1未満なら panic する。適正値は
// CPU数とストレージの待ち時間の両方で決まるので、呼び出し側が決める。0本の
// ワーカーは「並行しない」ではなく、何も取り込まない壊れた値である。1に読み替えも
// しない。読み替えると、渡した上限と実際に走る本数が食い違ったまま動く。
//
// interval は全体の走査の1回が終わってから次を始めるまでの間隔。0以下なら自動では
// 走査せず、走査は Scan の呼び出しと fsnotify の溢れでだけ起きる。
//
// 監視を張るのを Run まで遅らせない。起動時は「監視を張る → スキャン」の順に
// することで、スキャンが走査を終えたあとに置かれた写真を監視が拾う。Run の
// 開始を待ってから張ると、その順序が呼び出し側から保証できなくなる。
func NewSyncer(ix *Indexer, workers int, interval time.Duration, log *slog.Logger) (*Syncer, error) {
	if workers < 1 {
		panic(fmt.Sprintf("index: workers must be 1 or greater: %d", workers))
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("cannot start watching: %w", err)
	}
	sy := &Syncer{
		ix:        ix,
		fsw:       fsw,
		log:       log,
		workers:   workers,
		interval:  interval,
		debounce:  defaultDebounce,
		results:   make(chan taskResult, workers),
		scanFound: make(chan scanMsg),
		scanReqs:  make(chan chan scanOutcome),
	}
	if err := sy.addRoots(); err != nil {
		fsw.Close()
		return nil, err
	}
	return sy, nil
}

func (sy *Syncer) Close() error { return sy.fsw.Close() }

// Scan は全体の走査を1回要求し、その結果を待って返す。
//
// Run が動いていることが前提で、動いていなければ ctx が終わるまで返らない。
// 走査が走っている最中に呼ばれたら、それが終わったあとの1回を待つ。走っている
// 1回は呼んだ時点より前に歩き始めているので、呼んだ時点のディスクを反映している
// 保証が無いため。
//
// ctx が縛るのは待つ時間だけで、走査そのものは Run の ctx で動く。
func (sy *Syncer) Scan(ctx context.Context) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	reply := make(chan scanOutcome, 1)
	select {
	case sy.scanReqs <- reply:
	case <-ctx.Done():
		return Stats{}, ctx.Err()
	}
	select {
	case o := <-reply:
		return o.stats, o.err
	case <-ctx.Done():
		return Stats{}, ctx.Err()
	}
}

// Run は ctx が終わるまで同期を続ける。
//
// interval が正なら、待たずに1回目の全体の走査を始める。アプリが止まっていた間の
// 変更も fsnotify は検知できないため、起動直後の1回目こそ必要になる。
func (sy *Syncer) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	st := newSyncState(sy.workers, sy.debounce)
	tick := time.NewTicker(sy.debounce / 2)
	defer tick.Stop()

	var nextScan <-chan time.Time
	if sy.interval > 0 {
		sy.requestScan(ctx, st, nil)
	}

	for {
		// 全体の走査からは、スキャン側の保留に空きがあるときだけ受け取る。
		// 受け取らない間、走査の goroutine は送信で待たされる。ループは待たない。
		var found <-chan scanMsg
		if st.scanHasRoom() {
			found = sy.scanFound
		}

		select {
		case <-ctx.Done():
			sy.stop(cancel, st)
			return nil

		case ev, ok := <-sy.fsw.Events:
			if !ok {
				sy.stop(cancel, st)
				return nil
			}
			sy.handleEvent(ctx, st, ev)

		case err, ok := <-sy.fsw.Errors:
			if !ok {
				sy.stop(cancel, st)
				return nil
			}
			sy.log.Warn("watch error", "err", err)
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				// カーネルのキューが溢れた。落ちたイベントは二度と来ないので、
				// 取り戻せるのは全体の走査だけである。溢れは連続して届くが、
				// 要求は1回にまとまる。
				sy.requestScan(ctx, st, nil)
			}

		case m := <-found:
			if m.done {
				st.walkFinished(m.unchanged, m.skipped, m.err)
			} else {
				st.queueScanTask(m.task)
			}

		case r := <-sy.results:
			sy.finish(ctx, st, r)

		case reply := <-sy.scanReqs:
			sy.requestScan(ctx, st, reply)

		case <-nextScan:
			nextScan = nil
			sy.requestScan(ctx, st, nil)

		case <-tick.C:
		}

		sy.dispatch(ctx, st, time.Now())
		if p, again := st.finishedScan(); p != nil {
			sy.report(p)
			switch {
			case again:
				sy.startWalk(ctx, st)
			case sy.interval > 0:
				nextScan = time.After(sy.interval)
			}
		}
	}
}

// requestScan は全体の走査を要求し、新しく始めるなら走査の goroutine を起こす。
func (sy *Syncer) requestScan(ctx context.Context, st *syncState, reply chan scanOutcome) {
	if st.requestScan(reply) {
		sy.startWalk(ctx, st)
	}
}

// startWalk は st が始めた全体の走査を、走査の goroutine で歩かせる。
func (sy *Syncer) startWalk(ctx context.Context, st *syncState) {
	st.scan.started = time.Now()
	// 大量の写真では1回目に時間がかかる。開始も残さないと、走査中なのか
	// 止まっているのかがログから読めない。
	sy.log.Info("scan started", "dirs", sy.ix.roots)
	w := newScanWalk(sy.ix, sy.log, sy.scanFound)
	sy.walks.Add(1)
	go func() {
		defer sy.walks.Done()
		w.run(ctx)
	}()
}

// report は完了した全体の走査の結果をログに出し、待っている呼び出し元に返す。
func (sy *Syncer) report(p *scanProgress) {
	if p.err != nil {
		sy.log.Warn("scan failed", "err", p.err)
	} else {
		sy.log.Info("scan finished",
			"elapsed", time.Since(p.started).Round(time.Millisecond),
			"indexed", p.stats.Indexed, "unchanged", p.stats.Unchanged,
			"removed", p.stats.Removed, "skipped", p.stats.Skipped)
	}
	for _, reply := range p.waiters {
		reply <- scanOutcome{stats: p.stats, err: p.err}
	}
}

// dispatch は渡せる仕事を、ワーカーが埋まるまで渡す。
func (sy *Syncer) dispatch(ctx context.Context, st *syncState, now time.Time) {
	for {
		t, ok := st.next(now)
		if !ok {
			return
		}
		sy.start(ctx, t)
	}
}

// start は仕事1件をワーカーの goroutine で走らせる。終わるのを待たずに返る。
//
// 結果は results で返す。実行中から外すのはループが結果を受けたときなので、
// 結果を渡し終える前に次の仕事が走り出すことはない。
func (sy *Syncer) start(ctx context.Context, t task) {
	sy.tasks.Add(1)
	go func() {
		defer sy.tasks.Done()
		r := taskResult{task: t}
		switch t.kind {
		case taskIndex:
			r.err = sy.ix.indexFile(ctx, t.path)
		case taskPurge:
			r.purged, r.err = sy.ix.purgeGone(ctx, t.path)
		}
		sy.results <- r
	}()
}

// finish は終わった仕事1件を反映する。
func (sy *Syncer) finish(ctx context.Context, st *syncState, r taskResult) {
	stopping := ctx.Err() != nil
	if st.finish(r, stopping) {
		// 取り込んでいる間に消えていた。今しがた入った行を取り消す。
		if err := sy.ix.removeFile(ctx, r.task.path); err != nil {
			sy.log.Warn("failed to apply a deletion", "path", r.task.path, "err", err)
		}
		return
	}
	switch {
	case r.err != nil && stopping:
		// 中断で落ちたぶんは記録しない。停止のたびに身に覚えのない警告が出る。
	case r.err != nil && r.task.kind == taskPurge:
		sy.log.Warn("failed to apply a deletion", "path", r.task.path, "err", r.err)
	case r.err != nil:
		sy.log.Warn("skipped indexing", "path", r.task.path, "err", r.err)
	case r.task.kind == taskIndex && !r.task.fromScan:
		sy.log.Info("index updated", "path", r.task.path)
	}
}

// stop は走査とワーカーの終了を待ち、結果を待っている呼び出し元に返す。
//
// 待たずに戻ると、呼び出し側が Close やDBの後始末に進んだあとでワーカーが
// 書き込むことになる。走査は ctx で止まるので、待つ前に取り消す。Events が
// 閉じて戻る場合は呼び出し側の ctx がまだ生きているため、ここで取り消さないと
// 走査が送信で待ち続ける。
func (sy *Syncer) stop(cancel context.CancelFunc, st *syncState) {
	cancel()
	sy.walks.Wait()
	sy.tasks.Wait()
	for _, reply := range st.dropWaiters() {
		reply <- scanOutcome{err: context.Canceled}
	}
}
```

- [ ] **Step 10: 監視の処理を書き換える**

`internal/index/watch.go` を全面的に置き換える。Task 1 の `unwatchUnder` と Task 2 の `isRoot` はここへ移る。

```go
package index

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/synology"
)

// handleEvent は1つのfsnotifyイベントを処理する。
//
// ディレクトリ配下の走査（addTree と enqueueTree）はまだループの中で行う。
func (sy *Syncer) handleEvent(ctx context.Context, st *syncState, ev fsnotify.Event) {
	if synology.InManagedDir(ev.Name) {
		return
	}
	switch {
	case ev.Has(fsnotify.Remove), ev.Has(fsnotify.Rename):
		sy.handleGone(ctx, st, ev.Name)

	case ev.Has(fsnotify.Create):
		fi, err := os.Stat(ev.Name)
		if err != nil {
			return // すぐ消された等。何もしない
		}
		if !fi.IsDir() {
			if imagefmt.IsSupported(ev.Name) {
				st.watch(ev.Name, time.Now())
			}
			return
		}
		// 新しいディレクトリ: 監視に加えたうえで、既に入っている中身も拾う。
		// ディレクトリごとmvされた場合、中のファイルには個別のイベントが来ない。
		if err := sy.addTree(ev.Name); err != nil {
			sy.log.Warn("failed to add a watch", "path", ev.Name, "err", err)
		}
		sy.enqueueTree(st, ev.Name)

	case ev.Has(fsnotify.Write):
		if imagefmt.IsSupported(ev.Name) {
			st.watch(ev.Name, time.Now())
		}
	}
}

// handleGone は path が消えたことを反映する。
//
// Renameは「この名前から消えた」を意味する。移動先は別途Createで届く。
// この時点では消えたのがファイルかディレクトリか os.Stat では判別できないため
// 両方消す。該当しない方は何もマッチせずno-opになるだけなので安全。
// ディレクトリの場合、中の個々のファイルにはイベントが来ない
// （mv album ../elsewhere や mv album album2 のケース）ので、
// removeTreeで配下の行をパスの前方一致でまとめて消す。
func (sy *Syncer) handleGone(ctx context.Context, st *syncState, path string) {
	if sy.isRoot(path) {
		// ルートそのものが消えた。名前の変更か削除かは区別できない。スキャンが
		// 「空に見えるルート」の配下を消さないのと同じ理由で、ここでも消さない。
		// 消すと、戻したときに全部を取り込み直すことになる。扱いはスキャンの
		// 判断にそろえ、全体の走査を要求する。
		sy.log.Warn("a root disappeared; keeping its items", "root", path)
		sy.requestScan(ctx, st, nil)
		return
	}
	// 配下の保留を捨て、取り込み中のものに墓標を立てる。行が生まれるのは
	// 取り込みの完了時なので、ここで消しても空振りする。完了を受けてから消す。
	st.forget(path)
	if err := sy.ix.removeFile(ctx, path); err != nil {
		sy.log.Warn("failed to apply a deletion", "path", path, "err", err)
	}
	if err := sy.ix.removeTree(ctx, path); err != nil {
		sy.log.Warn("failed to apply the deletion of a directory", "path", path, "err", err)
	}
	// 消えたのがディレクトリなら、その配下に張ってあった監視を外す。
	sy.unwatchUnder(path)
}

// isRoot は path がルートそのものかを返す。
func (sy *Syncer) isRoot(path string) bool {
	path = filepath.Clean(path)
	for _, root := range sy.ix.roots {
		if filepath.Clean(root) == path {
			return true
		}
	}
	return false
}

// unwatchUnder は path とその配下に張ってある監視を外す。
//
// fsnotify は監視を inode で持ち、張った時点のパスで覚えている。ディレクトリが
// 移動すると、移動したディレクトリ自身の監視は IN_MOVE_SELF で外れるが、その子の
// 監視は古いパスのまま残る。移動先で張り直しても inotify が同じ監視を返すため
// パスは更新されず、移動先の配下のイベントが移動元のパスで届く。ルートの外へ
// 移した場合は、ルートの外の変更が移動元のパスとして届き続ける。
//
// 削除のイベントのたびに監視の一覧をなめる。比較は張ってあるディレクトリの数だけの
// 文字列の前方一致である。
//
// 既に外れている監視を外そうとすると ErrNonExistentWatch が返るが、外したい
// だけなので見ない。
func (sy *Syncer) unwatchUnder(path string) {
	for _, p := range sy.fsw.WatchList() {
		if isUnder(path, p) {
			_ = sy.fsw.Remove(p)
		}
	}
}

// addRoots はすべてのルート以下を監視対象に加える。
func (sy *Syncer) addRoots() error {
	for _, root := range sy.ix.roots {
		if err := sy.addTree(root); err != nil {
			return err
		}
	}
	return nil
}

// addTree は root 以下の全ディレクトリを監視対象に加える。
// fsnotifyは再帰監視をしないため自前で降りていく。
func (sy *Syncer) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			sy.log.Warn("skipped a watch", "path", path, "err", err)
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		// 中のイベントはどのみち無視するので、監視枠を消費しない。
		// inotifyは再帰監視をしないぶん1ディレクトリ=1枠で、@eaDirは
		// 写真1枚につき1つできる。max_user_watches(既定8192)を容易に超える。
		if synology.IsManagedDir(d.Name()) {
			return fs.SkipDir
		}
		if err := sy.fsw.Add(path); err != nil {
			sy.log.Warn("cannot add a watch", "path", path, "err", err)
		}
		return nil
	})
}

// enqueueTree は root 以下の対象ファイルを監視側の保留に積む。
func (sy *Syncer) enqueueTree(st *syncState, root string) {
	now := time.Now()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if synology.IsManagedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !imagefmt.IsSupported(path) {
			return nil
		}
		st.watch(path, now)
		return nil
	})
}
```

- [ ] **Step 11: テスト専用の口を移す**

`internal/index/export_test.go` の `SetDebounce` と `InjectWatchError` のレシーバを `Syncer` にする。コメントの「Run は別goroutineから debounce を読むため」はそのまま残す。

```go
func (sy *Syncer) SetDebounce(d time.Duration) { sy.debounce = d }
```

```go
func (sy *Syncer) InjectWatchError(err error) { sy.fsw.Errors <- err }
```

- [ ] **Step 12: `main.go` を直す**

`main.go` の「// この下の監視とスキャンが、写真をDBに取り込むのに使う。」のコメントから、スキャンの goroutine を起こすところ（`scanner.Run(ctx)` を呼ぶ goroutine の `}()`）まで（今の140行目から178行目）を置き換える。その下の `listenErr` の select からは変えない。

```go
	// この下の同期が、写真をDBに取り込むのに使う。
	ix := index.New(cfg.MediaDirs, st, thumbs, log)

	// NewSyncer が戻った時点で監視は張れている。Run の1回目の走査はその後に
	// 始まるので、走査が歩き終えたあとに置かれた写真は監視が拾う。逆にすると、
	// その間に置かれた写真をどちらも拾えない。数千枚で走査が数分かかる構成では、
	// その窓のあいだの変更が次の走査まで反映されなくなる。
	// 1回目は起動直後に走り、止まっていた間の変更を取り戻す。2回目以降は監視の
	// 取りこぼしを回復する。
	syncer, err := index.NewSyncer(ix, cfg.ScanWorkers, cfg.ScanInterval, log)
	if err != nil {
		return err
	}
	defer syncer.Close()
	log.Info("watching for changes", "dirs", cfg.MediaDirs)

	// 取り込みを走らせる goroutine の終了を待ってから store を閉じる。待たずに
	// 閉じると、あとから Upsert するワーカーが閉じたDBに書きに行く。
	// defer の順序で st.Close() より先に走る。
	var wg sync.WaitGroup
	defer func() {
		cancel() // 同期に終わるよう伝える
		wg.Wait()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := syncer.Run(ctx); err != nil {
			log.Error("syncer stopped", "err", err)
		}
	}()
```

`main.go` の `-scan-workers` のヘルプ（`"how many files are taken in at once, both by the scan and by the watcher"`）は意味が変わらないのでそのまま残す。

- [ ] **Step 13: 古い名前が残っていないことを確かめる**

Run: `grep -rn "NewScanner\|NewWatcher\|ScanRequests\|startIndex\|tryStartIndex\|\.busy()\|newSlots\|startWatcher\|f\.sc\." --include=*.go .`
Expected: 何も出ない

- [ ] **Step 14: 通ることを確かめる**

Run: `gofmt -l . && go build ./... && make vet && go vet -tags browser ./internal/web/ && make unit-test-race`
Expected: `gofmt -l` が空、build と vet が通り、test がすべて ok

新しいテストが新しい振る舞いで通っていることを個別にも見る。

Run: `go test -race -count=3 -run 'TestScanYieldsToWatchedChanges|TestScanAndWatchDoNotIndexAPathTwiceAtOnce|TestScanKeepsTheRowOfAFileRecreatedBeforeThePurge|TestScanLeavesNoRowForAFileMovedWhileBeingIndexed|TestWatchScansAfterAnEventOverflow' ./internal/index/`
Expected: PASS（3回とも）

- [ ] **Step 15: コミット**

```bash
git add -A internal/index internal/web/browser_test.go main.go docs/superpowers/specs/2026-10-05-index-syncer-design.md
git commit -m "refactor: Merge the scanner and the watcher into one syncer loop"
```

---

### Task 4: ディレクトリの Create で歩くのをループの外へ出す（監査メモの課題2）

**Files:**
- Modify: `internal/index/syncstate.go`（監視側の保留に「部分木の走査が積んだ」印と件数を持たせる）
- Modify: `internal/index/syncer.go`（`treeFound` チャネル、ループでの受け取り、`maxWalkBacklog`）
- Modify: `internal/index/watch.go`（Create の分岐で部分木の走査を起こす。`enqueueTree` を消し、`startTreeWalk` を足す）
- Modify: `docs/superpowers/specs/2026-10-05-index-syncer-design.md`（テストの表から課題2の行を外す）

**Interfaces:**
- Consumes: Task 3 の `syncState.watch` / `next` / `forget`、`Syncer.walks`
- Produces: `const maxWalkBacklog = 1024`、`func (s *syncState) watchFromTree(path string, now time.Time)`、`func (s *syncState) treeHasRoom() bool`、`func (sy *Syncer) startTreeWalk(ctx context.Context, dir string)`、`Syncer.treeFound chan string`

**この Task では新しいテストを足さない。** 変わるのは「木をどの goroutine で歩くか」だけで、公開APIから観測できる結果（ディレクトリを持ち込んだら中身が載る、`@eaDir` を拾わない、移動したディレクトリを追う）は変わらない。ループが止まらないことは時間でしか観測できず、決定的なテストにならない。安全網は既存の `TestWatchPicksUpNewSubdirectory`、`TestWatchPicksUpDirectoryMovedInWholesale`、`TestWatchRemovesRowsWhenDirectoryRenamedWithinTree`、`TestWatchSkipsSynologyDirsInMovedDirectory`、`TestWatchFollowsADirectoryRenamedWithItsSubdirectories`。

- [ ] **Step 1: 監視側の保留に印を持たせる**

`internal/index/syncstate.go` の `syncState` の `watched` を置き換え、`fromTree` を足す。

```go
	// watched は監視側の保留。パス → 最後にイベントを受けた時刻と、積んだのが
	// 部分木の走査かどうか。
	watched map[string]watchedEntry
	// fromTree は watched のうち部分木の走査が積んだものの数。部分木の走査への
	// 背圧に使う。
	fromTree int
```

型を足す。

```go
// watchedEntry は監視側の保留の1件。
type watchedEntry struct {
	at time.Time
	// fromTree は部分木の走査が積んだことを表す。同じパスにイベントが届いたら
	// イベントのものとして扱い直す。
	fromTree bool
}
```

`newSyncState` の `watched: make(map[string]time.Time),` を `watched: make(map[string]watchedEntry),` にする。

`watch` を置き換え、`watchFromTree` / `treeHasRoom` / `unwatch` を足す。

```go
// watch は監視側の保留にパスを積む。既にあれば時刻が新しくなり、debounce が延びる。
func (s *syncState) watch(path string, now time.Time) {
	s.unwatch(path)
	s.watched[path] = watchedEntry{at: now}
}

// watchFromTree は部分木の走査が見つけたパスを監視側の保留に積む。
// 既にイベントで積まれていれば、そちらを優先して何もしない。
func (s *syncState) watchFromTree(path string, now time.Time) {
	if _, ok := s.watched[path]; ok {
		return
	}
	s.watched[path] = watchedEntry{at: now, fromTree: true}
	s.fromTree++
}

// treeHasRoom は部分木の走査から受け取ってよいかを返す。
//
// 部分木の走査が積んだものは debounce を待ってから渡すので、上限がそのまま
// 「debounce あたりに流せる件数」になる。fsnotify のイベントが積むぶんは数えない。
// イベントは読まないと取りこぼすので、上限で止められない。
func (s *syncState) treeHasRoom() bool { return s.fromTree < maxWalkBacklog }

// unwatch は監視側の保留からパスを外す。
func (s *syncState) unwatch(path string) {
	if e, ok := s.watched[path]; ok && e.fromTree {
		s.fromTree--
	}
	delete(s.watched, path)
}
```

`next` の監視側のループを置き換える。

```go
	for path, e := range s.watched {
		if now.Sub(e.at) < s.debounce {
			continue
		}
		if _, busy := s.inflight[path]; busy {
			continue
		}
		s.unwatch(path)
		s.inflight[path] = false
		return task{kind: taskIndex, path: path}, true
	}
```

`forget` の監視側のループを置き換える。

```go
	for path := range s.watched {
		if isUnder(p, path) {
			s.unwatch(path)
		}
	}
```

- [ ] **Step 2: ループで受け取る**

`internal/index/syncer.go` の `defaultDebounce` の直後に足す。

```go
// maxWalkBacklog は部分木の走査が監視側の保留に積んでよい件数の上限。
//
// 部分木の走査が積んだものは debounce の2秒を待ってから渡すので、この上限が
// そのまま「2秒あたりに流せる件数」になる。512件/秒は取り込みの速さ（1枚で
// 数百ミリ秒 × workers）より十分大きく、ここが律速にならない。上限が無いと、
// 巨大なディレクトリを持ち込んだときにパスの数だけ保留が膨らむ。
const maxWalkBacklog = 1024
```

`Syncer` の `scanFound` の直後にフィールドを足す。

```go
	// treeFound は部分木の走査がループへ見つけたパスを送る口。容量を持たない。
	treeFound chan string
```

`NewSyncer` の `scanFound: make(chan scanMsg),` の直後に `treeFound: make(chan string),` を足す。

`Run` の `found` を決めるところの直後に足す。

```go
		var tree <-chan string
		if st.treeHasRoom() {
			tree = sy.treeFound
		}
```

`Run` の `select` の `case m := <-found:` の直前に足す。

```go
		case path := <-tree:
			st.watchFromTree(path, time.Now())
```

`Syncer` の型コメントの「取り込みはワーカーの goroutine、全体の走査は走査の goroutine が担う。」を「取り込みはワーカーの goroutine、木の走査は走査の goroutine が担う。」にする。

- [ ] **Step 3: Create で部分木の走査を起こす**

`internal/index/watch.go` の `handleEvent` の Create のディレクトリの分岐を置き換える。

```go
		// 新しいディレクトリ: 監視に加えたうえで、既に入っている中身も拾う。
		// ディレクトリごとmvされた場合、中のファイルには個別のイベントが来ない。
		// 歩くのはループの外で行う。所要時間が項目数に比例し、その間イベントを
		// 読めなくなるため。
		sy.startTreeWalk(ctx, ev.Name)
```

`handleEvent` の型コメントの「ディレクトリ配下の走査（addTree と enqueueTree）はまだループの中で行う。」の行を消す。

`enqueueTree` を消し、代わりに足す。

```go
// startTreeWalk は dir 以下を走査の goroutine で歩く。ディレクトリには監視を張り、
// 対象のファイルはループへ送って監視側の保留に積ませる。
//
// ディレクトリごとに「監視を張る → 中身を読む」の順になる（WalkDir はディレクトリ
// 自身を渡してから中身を読む）。張ったあとに置かれたファイルは監視が拾い、張る
// 前から在ったファイルはこの走査が拾う。
//
// 監視を張れなかった数は、ディレクトリごとではなく1行にまとめて出す。監視枠が
// 尽きた環境で、持ち込むたびに数千行が出るのを避けるため。
func (sy *Syncer) startTreeWalk(ctx context.Context, dir string) {
	sy.walks.Add(1)
	go func() {
		defer sy.walks.Done()
		unwatched := 0
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if synology.IsManagedDir(d.Name()) {
					return fs.SkipDir
				}
				if err := sy.fsw.Add(path); err != nil {
					unwatched++
				}
				return nil
			}
			if !imagefmt.IsSupported(path) {
				return nil
			}
			select {
			case sy.treeFound <- path:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if unwatched > 0 {
			sy.log.Warn("cannot add some watches", "dir", dir, "failed", unwatched)
		}
	}()
}
```

- [ ] **Step 4: 設計書のテストの表を直す**

`docs/superpowers/specs/2026-10-05-index-syncer-design.md` の「テスト」の節の表から「課題2」の行を消し、表の直後に足す。

```markdown
課題2（ディレクトリの Create でループが止まらないこと）には新しいテストを足さない。
変わるのは木をどの goroutine で歩くかだけで、公開APIから見える結果は変わらない。
ループが止まらないことは時間でしか観測できず、決定的なテストにならない。
ディレクトリの持ち込み・移動・`@eaDir` の除外を見る既存のテストが安全網になる。
```

- [ ] **Step 5: 通ることを確かめる**

Run: `gofmt -l . && make vet && make unit-test-race`
Expected: `gofmt -l` が空、vet と test が通る

Run: `go test -race -count=5 -run 'TestWatch' ./internal/index/`
Expected: PASS（5回とも）

- [ ] **Step 6: コミット**

```bash
git add internal/index/syncstate.go internal/index/syncer.go internal/index/watch.go docs/superpowers/specs/2026-10-05-index-syncer-design.md
git commit -m "refactor: Walk a new directory outside the syncer loop"
```

---

### Task 5: 全体の走査で監視を張り直す（問題3）

**Files:**
- Modify: `internal/index/scan.go`（`scanWalk` に `fsw` を持たせ、歩いたディレクトリに監視を張る）
- Modify: `internal/index/syncer.go`（`newScanWalk` の呼び出し）
- Test: `internal/index/watch_test.go`

**Interfaces:**
- Consumes: Task 3 の `scanWalk`、`Syncer.startWalk`
- Produces: `func newScanWalk(ix *Indexer, fsw *fsnotify.Watcher, log *slog.Logger, out chan<- scanMsg) *scanWalk`

- [ ] **Step 1: 失敗するテストを書く**

`internal/index/watch_test.go` の `TestWatchKeepsRowsWhenARootIsRenamed` の直後に足す。

```go
// 監視が外れても、全体の走査が張り直す。外れる経路は、溢れている間に作られた
// ディレクトリ、監視枠が尽きて張れなかったディレクトリ、消えて戻ってきたルートと
// いくつもあり、どれも外れたことを知らせてこない。
func TestScanWatchesARootThatCameBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	sy := startSyncer(t, f)
	_, err := sy.Scan(ctx)
	require.NoError(t, err)
	requireCount(t, f, 1)

	// ルートを外して戻す。外した時点でルートの監視は外れる。
	away := f.root + ".away"
	require.NoError(t, os.Rename(f.root, away))
	time.Sleep(3 * testDebounce)
	require.NoError(t, os.Rename(away, f.root))

	_, err = sy.Scan(ctx)
	require.NoError(t, err)

	// 走査のあとに置く。拾えるのは監視だけである。
	writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	requireCount(t, f, 2)
}
```

- [ ] **Step 2: 失敗を確かめる**

Run: `go test -race -run 'TestScanWatchesARootThatCameBack' ./internal/index/`
Expected: FAIL。`the count never reached 2`

- [ ] **Step 3: 実装する**

`internal/index/scan.go`:

import に `"github.com/fsnotify/fsnotify"` を足す。

`scanWalk` の `ix` の直後にフィールドを足し、`skipped` の行に `unwatched` を足す。

```go
	fsw *fsnotify.Watcher
```

```go
	foundByRoot                   map[string]int
	unchanged, skipped, unwatched int
```

`newScanWalk` を置き換える。

```go
func newScanWalk(ix *Indexer, fsw *fsnotify.Watcher, log *slog.Logger, out chan<- scanMsg) *scanWalk {
	return &scanWalk{ix: ix, fsw: fsw, log: log, out: out, foundByRoot: make(map[string]int, len(ix.roots))}
}
```

`walk` のディレクトリの分岐を置き換える。

```go
		if d.IsDir() {
			if synology.IsManagedDir(d.Name()) {
				return fs.SkipDir
			}
			// 歩いたディレクトリには監視を張り直す。Add は既に張ってあれば何も
			// 変えない。溢れている間に作られたディレクトリ、監視枠が尽きて張れな
			// かったディレクトリ、消えて戻ってきたルートは、外れたことを知らせて
			// こないので、ここで戻すしかない。
			if err := w.fsw.Add(path); err != nil {
				w.unwatched++
			}
			return nil
		}
```

`walkAll` の `return nil` の直前に足す。

```go
	if w.unwatched > 0 {
		// ディレクトリごとに出すと、監視枠が尽きた環境で走査のたびに数千行になる。
		w.log.Warn("cannot add some watches", "failed", w.unwatched)
	}
```

`internal/index/syncer.go` の `startWalk` の `newScanWalk(sy.ix, sy.log, sy.scanFound)` を `newScanWalk(sy.ix, sy.fsw, sy.log, sy.scanFound)` にする。

- [ ] **Step 4: 通ることを確かめる**

Run: `go test -race -count=3 -run 'TestScanWatchesARootThatCameBack' ./internal/index/`
Expected: PASS（3回とも）

- [ ] **Step 5: 全体を通す**

Run: `gofmt -l . && make vet && make unit-test-race`
Expected: `gofmt -l` が空、vet と test が通る

- [ ] **Step 6: コミット**

```bash
git add internal/index/scan.go internal/index/syncer.go internal/index/watch_test.go
git commit -m "feat: Restore the watches on every directory a scan walks"
```

---

### Task 6: 監査メモを現状に合わせる

**Files:**
- Modify: `docs/superpowers/plans/2026-09-07-index-remaining-issues.md`

**Interfaces:**
- Consumes: Task 1〜5 の名前
- Produces: なし（ドキュメントのみ）

- [ ] **Step 1: 冒頭の表を直す**

`## 経緯と、この文書が前提とする不変条件` の節で、「これを担うのが `internal/index/indexer.go` の `Indexer.startIndex` / `Indexer.tryStartIndex` である。」から表の終わりまでを置き換える。

```markdown
これを担うのが `internal/index/syncer.go` の `Syncer` である（2026-10-06 の再編。
`docs/superpowers/specs/2026-10-05-index-syncer-design.md`）。

| 名前 | 場所 | 役割 |
|---|---|---|
| `Indexer` | `indexer.go` | 1枚を同期的に適用する。並行について何も知らない |
| `Syncer` | `syncer.go` | ループ。入口をまたぐ状態と判断の唯一の持ち主。取り込みはワーカーへ、木の走査は走査の goroutine へ出す |
| `syncState` | `syncstate.go` | ループの状態（監視側とスキャン側の保留、取り込み中のパスと墓標、スキャン1回の進み具合）と、次に何をするかの判断 |
| `scanWalk` | `scan.go` | 全体の走査1回の帳簿。見つけた仕事を背圧付きのチャネルでループへ送る |
```

- [ ] **Step 2: 課題2に決着を書く**

`## 課題2: ディレクトリの Create がループ内でフルウォークを2回する` の見出しの直後に足す。

```markdown
**解決済み（2026-10-06）。** `2026-10-05-index-syncer-design.md` で、ディレクトリの
Create を受けたら部分木を走査の goroutine で歩く形にした。見つけたパスは背圧付きの
チャネルでループへ送り、監視側の保留に積む（上限 `maxWalkBacklog`）。以下は当時の
記録である。
```

- [ ] **Step 3: 課題7の節を直す**

`## 課題7: 取り込みの実行単位の共有に同時実行の規約が無い` の節の「**解決済み（2026-10-04）。**」の段落の直後に足す。

```markdown
2026-10-06 の再編で、取り込みを出す入口そのものが `Syncer` の1つになった。枠
（`slots`）と入口ごとの `sync.WaitGroup` は無くなり、同時に走る数はループが数える。
```

同じ節の末尾の「あわせて `pending` に上限が無いことも見ておく。」の段落を置き換える。

```markdown
`pending` に上限が無い件は、巨大なディレクトリを持ち込んだ場合については
2026-10-06 に解消した（部分木の走査に上限 `maxWalkBacklog`）。fsnotify のイベントが
積むぶんには今も上限が無い。イベントは読まないと取りこぼすので、上限で止められない。
```

- [ ] **Step 4: 古い名前が残っていないことを確かめる**

Run: `grep -n "startIndex\|tryStartIndex\|ScanRequests\|NewWatcher\|NewScanner" docs/superpowers/plans/2026-09-07-index-remaining-issues.md`
Expected: 「以下は当時の記録である」と書いた節の中にだけ残る。それ以外に出たら直す

- [ ] **Step 5: コミット**

```bash
git add docs/superpowers/plans/2026-09-07-index-remaining-issues.md
git commit -m "docs: Update the index audit notes for the syncer"
```

---

## 完了の確認

- [ ] `make build`、`make vet`、`make unit-test-race` が通る
- [ ] `go vet -tags browser ./internal/web/` が通る（ブラウザテストの実行は任意）
- [ ] `grep -rn "NewScanner\|NewWatcher\|ScanRequests\|startIndex\|newSlots" --include=*.go .` が空
- [ ] `internal/index` の公開物が `Indexer` / `New` / `Syncer` / `NewSyncer` / `Stats` と、`Syncer` の `Run` / `Scan` / `Close` だけであること（`go doc ./internal/index` で確かめる）
