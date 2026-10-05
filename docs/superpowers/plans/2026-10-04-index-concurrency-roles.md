# 取り込みの同時実行における役の再編 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `internal/index` の同時実行の役を3つに分け、共有するものを受動的な枠だけにして、行為者（`indexer`）を取り込みの入口ごとに持たせる。

**Architecture:** 公開型 `Indexer` を `Catalog`（索引そのもの。`roots` / `st` / `thumbs` / `slots`）に改名し、入口ごとの行為者を非公開の `indexer`（`cat` と `wg` を持つ）にする。`jobs` と `indexFunc` は `indexer` に吸収して削除する。振る舞いは変えない。

**Tech Stack:** Go（`sync.WaitGroup`、バッファ付きチャネルによるセマフォ）、`fsnotify`、`modernc.org/sqlite`

**Spec:** [docs/superpowers/specs/2026-10-04-index-concurrency-roles-design.md](../specs/2026-10-04-index-concurrency-roles-design.md)

## Global Constraints

- 振る舞いを変えない。既存テストは名前の追従だけで通るのが期待値。新しいテストは足さない
- リポジトリに入る文字列は既存ファイルに合わせる。`internal/index` のコメントは日本語
- `sem` / `jobs` / `executor` / 「持ち場」という語をコードとコメントから消し、`slots` =「枠」に統一する
- 公開するのは `Catalog` / `Scanner` / `Watcher` / `Stats` とそのメソッドのみ。`indexer` と `slots` は非公開
- コミットメッセージは英語の要約1行のみ。本文とトレーラーは付けない。`refactor:` を使う
- ブランチは `feature-index-concurrency-roles`（既にこのブランチ上にある）
- テストは `make unit-test-race`。ブラウザテストはビルドタグ付きなので、コンパイルの確認は `go vet -tags browser ./internal/web/` で行う

**このリファクタに TDD は適用しない。** 新しい振る舞いが無いので失敗するテストを先に書く対象が存在しない。安全網は既存のテストで、各タスクの最後に全部を走らせる。

---

### Task 1: `Indexer` を `Catalog` に改名する

**Files:**
- Rename: `internal/index/indexer.go` → `internal/index/catalog.go`
- Modify: `internal/index/catalog.go`（型・コンストラクタ・レシーバ名）
- Modify: `internal/index/export_test.go:28,34,39`（レシーバ）
- Modify: `internal/index/scan.go:24,25,33,43,44,117`
- Modify: `internal/index/watch.go:42,65`
- Modify: `main.go:141`
- Modify: `internal/index/indexer_test.go:23,83,85,109,111`
- Modify: `internal/index/scan_test.go:293,295`
- Modify: `internal/web/browser_test.go:354`

**Interfaces:**
- Consumes: なし（最初のタスク）
- Produces: `index.Catalog` 型、`index.NewCatalog(roots []string, st *store.Store, thumbs *thumb.Provider, workers int, log *slog.Logger) *Catalog`、レシーバ名 `c *Catalog`、非公開メソッド `c.indexFile` / `c.removeFile` / `c.removeTree` / `c.busy` / `c.newJobs`

- [ ] **Step 1: ファイルを改名する**

```bash
git mv internal/index/indexer.go internal/index/catalog.go
```

- [ ] **Step 2: 型・コンストラクタ・レシーバを書き換える**

`internal/index/catalog.go` の先頭部分をこうする。`workers` フィールドと `sem` は Task 2 で触るのでここでは残す。

```go
// Catalog はディスク上の写真とSQLiteの索引そのものを表す。1枚の取り込み手順と、
// どこを見てどこに書くか、そして同時に取り込める枠を持つ。索引する主体ではなく、
// それは入口ごとに持つ indexer である。
type Catalog struct {
	roots   []string
	st      *store.Store
	thumbs  *thumb.Provider
	workers int
	// sem は取り込みの持ち場。容量が同時に取り込む枚数の上限で、スキャンも監視も
	// ここから1つ取る。入口ごとに持つと合計が上限の2倍になるため、1本を共有する。
	sem chan struct{}
	log *slog.Logger
}

// NewCatalog はCatalogを作る。rootsは写真を収集するルートディレクトリ。
//
// thumbs は配信側と共有する。同じ置き場所を指す設定値を2経路に配ると、
// ずれても誰も気づけないため、組み立てたものを1つ受け取る。
//
// workers は同時に取り込む枚数。1未満は1として扱う。適正値はCPU数とストレージの
// 待ち時間で決まり、NASとローカルで違うため設定から来る。
//
// スキャンも監視も同じ持ち場から1つ取るので、この上限は取り込みの入口に
// よらず効く。ディレクトリごと移動された場合、監視にも一度に数百件が来る。
func NewCatalog(roots []string, st *store.Store, thumbs *thumb.Provider, workers int, log *slog.Logger) *Catalog {
	if workers < 1 {
		workers = 1
	}
	return &Catalog{
		roots:   roots,
		st:      st,
		thumbs:  thumbs,
		workers: workers,
		sem:     make(chan struct{}, workers),
		log:     log,
	}
}
```

同じファイルの残りのメソッドのレシーバを `(ix *Indexer)` から `(c *Catalog)` に変え、本体の `ix.` を `c.` にする。対象は `busy`、`newJobs`、`indexFile`、`removeFile`、`removeTree`。

- [ ] **Step 3: `export_test.go` のレシーバを揃える**

```go
func (c *Catalog) IndexFile(ctx context.Context, path string) error {
	return c.indexFile(ctx, path)
}

func (c *Catalog) RemoveFile(ctx context.Context, path string) error {
	return c.removeFile(ctx, path)
}

func (c *Catalog) RemoveTree(ctx context.Context, dir string) error {
	return c.removeTree(ctx, dir)
}
```

- [ ] **Step 4: パッケージ内の参照を直す**

`scan.go` と `watch.go` の `*Indexer` を `*Catalog` にする。フィールド名（`ix`）は Task 4 で直すので、ここでは型だけを変える。

```go
// scan.go:33, 117
	ix *Catalog
// scan.go:43
func NewScanner(ix *Catalog, interval time.Duration, kicks <-chan struct{}, log *slog.Logger) *Scanner {
// watch.go:42
	ix *Catalog
// watch.go:65
func NewWatcher(ix *Catalog, log *slog.Logger) (*Watcher, error) {
```

`scan.go:24-26` の `Scanner` の型コメントも直す。

```go
// Scanner は Catalog に全体の突き合わせを繰り返させる。取り込みそのものは
// Catalog と入口ごとの indexer が担い、Scanner はそれを起こす2つの経路のうちの
// 1つである（もう1つが Watcher）。
```

- [ ] **Step 5: 呼び出し側を直す**

```go
// main.go:141
	cat := index.NewCatalog(cfg.MediaDirs, st, thumbs, cfg.ScanWorkers, log)
// main.go:147, 173 の ix を cat に差し替える
	watcher, err := index.NewWatcher(cat, log)
	scanner := index.NewScanner(cat, cfg.ScanInterval, watcher.ScanRequests(), log)
```

テスト側も同じ要領で差し替える。`indexer_test.go:23` のフィールドは `ix *index.Indexer` → `cat *index.Catalog` にし、`fixture` を使っている箇所（`f.ix` → `f.cat`）を追う。`internal/web/browser_test.go:354-355` と `scan_test.go:293-295` の局所変数も `ix` → `cat` にする。

- [ ] **Step 6: 整形と静的検査を通す**

```bash
gofmt -l . && go build ./... && make vet && go vet -tags browser ./internal/web/
```

Expected: 何も出力されない（`gofmt -l` が空、vet がエラー無し）

- [ ] **Step 7: テストを走らせる**

```bash
make unit-test-race
```

Expected: すべて ok。`internal/index` と `internal/web` が通ること

- [ ] **Step 8: コミット**

```bash
git add -A internal/index internal/web main.go
git commit -m "refactor: Rename Indexer to Catalog"
```

---

### Task 2: `sem` を `slots` にし、`workers` フィールドを落とす

**Files:**
- Modify: `internal/index/catalog.go`（`sem` → `slots`、`workers` フィールド削除、コメントの語彙）
- Modify: `internal/index/jobs.go`（`sem` を参照する4箇所とコメント）
- Modify: `internal/index/watch.go:75`（`ix.workers` → `cap(ix.slots)`）
- Modify: `internal/index/scan_test.go:411` 付近のコメント（「持ち場」が残っていれば）

**Interfaces:**
- Consumes: Task 1 の `Catalog`
- Produces: `Catalog.slots chan struct{}`（容量が上限）、`func (c *Catalog) busy() bool`。`Catalog.workers` フィールドは存在しない

- [ ] **Step 1: `Catalog` の枠を書き換える**

```go
type Catalog struct {
	roots  []string
	st     *store.Store
	thumbs *thumb.Provider
	// slots は同時に取り込める枠。容量が上限（workers）で、入っているぶんが
	// 使用中。スキャンも監視も同じ枠から1つ取るので、上限は入口によらず効く。
	// 入口ごとに持つと合計が上限の2倍になるため、1本を共有する。
	slots chan struct{}
	log   *slog.Logger
}
```

`NewCatalog` から `workers: workers,` を消し、`slots: make(chan struct{}, workers),` にする。引数の `workers` と1未満の丸めはそのまま残す（設定から来る値の意味は変わらない）。

型コメントとコンストラクタのコメントの「持ち場」を「枠」に直す。

- [ ] **Step 2: `busy` を直す**

```go
// busy は取り込みが1件でも走っているかを返す。誰が出した仕事かは区別しない。
//
// 取り込みの最中に写真が消えると、ワーカーが後から Upsert して存在しない
// パスの行が残りうる。その気配を監視が知るために使う。
//
// 枠は done を返し終えるまで空かないので、埋まっている枠があることは
// 「まだ Upsert していないワーカーが居る」ことと一致する。
func (c *Catalog) busy() bool { return len(c.slots) > 0 }
```

`newJobs` も `slots` を渡す形に直す。

```go
func (c *Catalog) newJobs() *jobs { return &jobs{slots: c.slots, run: c.indexFile} }
```

- [ ] **Step 3: `jobs.go` の参照を直す**

フィールド名を `sem` から `slots` にし、4箇所の参照（`submit` の送信、`trySubmit` の `select`、`start` の `defer` 受信、型コメント）を書き換える。コメントの「持ち場」も「枠」にする。

```go
type jobs struct {
	slots chan struct{} // Catalog の枠。入口をまたいで共有する
	run   indexFunc
	wg    sync.WaitGroup
}
```

- [ ] **Step 4: `watch.go` の容量を枠から取る**

```go
// watch.go:75
		results:  make(chan indexResult, cap(ix.slots)),
```

`results` の容量が同時取り込み数と一致する理由はこのフィールドのコメント（`watch.go:44-47`）に既にある。`cap(ix.slots)` にすることで、上限を2箇所に書かずに済む。

- [ ] **Step 5: 「持ち場」が残っていないことを確かめる**

```bash
grep -rn "持ち場\|\bsem\b\|executor" --include=*.go internal/ main.go
```

Expected: 何も出ない（`_busy_timeout` のような無関係な一致は対象外。`internal/store` と `internal/session` の DSN は触らない）

- [ ] **Step 6: 整形・検査・テスト**

```bash
gofmt -l . && go build ./... && make vet && make unit-test-race
```

Expected: `gofmt -l` が空、vet と test が通る

- [ ] **Step 7: コミット**

```bash
git add -A internal/index
git commit -m "refactor: Name the ingest limit slots instead of sem"
```

---

### Task 3: `jobs` を `indexer` にする

**Files:**
- Rename: `internal/index/jobs.go` → `internal/index/indexer.go`
- Modify: `internal/index/indexer.go`（型名・フィールド・`indexFunc` 削除）
- Modify: `internal/index/catalog.go`（`newJobs` → `newIndexer`）
- Modify: `internal/index/scan.go:94`、`internal/index/watch.go:77`（生成箇所）

**Interfaces:**
- Consumes: Task 2 の `Catalog.slots`、`Catalog.indexFile`
- Produces: `type indexer struct { cat *Catalog; wg sync.WaitGroup }`、`func (c *Catalog) newIndexer() *indexer`、`func (ix *indexer) submit(ctx context.Context, path string, done func(error))`、`func (ix *indexer) trySubmit(ctx context.Context, path string, done func(error)) bool`、`func (ix *indexer) wait()`。`jobs` と `indexFunc` は存在しない

- [ ] **Step 1: ファイルを改名する**

```bash
git mv internal/index/jobs.go internal/index/indexer.go
```

- [ ] **Step 2: 型を書き換える**

`internal/index/indexer.go` を全面的にこうする。

```go
package index

import (
	"context"
	"sync"
)

// indexer は入口の代わりに取り込みを走らせる。スキャンも監視も1つずつ持ち、
// 枠は Catalog と共有するが、完了を待つ相手は自分が出したぶんに閉じる。
//
// 待ち方だけは入口ごとに逆になる。走査は枠が空くまで待ってよく、むしろ待たないと
// 数千件のパスを先に溜め込んでしまう。監視ループは決して待てない。待った時間は
// そのまま fsnotify のイベントを読まない時間になり、カーネルのキューが溢れれば
// 変更そのものを取りこぼす。submit と trySubmit はこの非対称のためにある。
type indexer struct {
	cat *Catalog
	wg  sync.WaitGroup
}

// submit は枠が空くまで待ってから1枚の取り込みを始める。
// done は取り込みが終わったワーカーの上で呼ばれる。
func (ix *indexer) submit(ctx context.Context, path string, done func(error)) {
	ix.cat.slots <- struct{}{}
	ix.start(ctx, path, done)
}

// trySubmit は枠が空いていれば取り込みを始め、埋まっていれば false を返す。
// 呼び出し側をブロックしない。渡せなかったパスは呼び出し側が持ったままにして、
// 空いてから渡し直す。
func (ix *indexer) trySubmit(ctx context.Context, path string, done func(error)) bool {
	select {
	case ix.cat.slots <- struct{}{}:
	default:
		return false
	}
	ix.start(ctx, path, done)
	return true
}

// start は枠を確保済みの前提で1枚の取り込みを走らせる。
//
// 枠を空けるのは done を返したあとである。順序を逆にすると、通知を渡し終える
// 前に次の1枚が走り出せてしまい、通知の待ち行列が同時取り込み数を超えて伸びうる。
// 呼び出し側が容量をワーカー数で見積もれるよう、ここで閉じておく。
func (ix *indexer) start(ctx context.Context, path string, done func(error)) {
	ix.wg.Add(1)
	go func() {
		defer ix.wg.Done()
		defer func() { <-ix.cat.slots }()
		done(ix.cat.indexFile(ctx, path))
	}()
}

// wait は自分が出した取り込みがすべて終わるまで待つ。
// 他の入口が出したぶんは待たない。
func (ix *indexer) wait() { ix.wg.Wait() }
```

`indexFunc` は使う側が無くなるので消える。`cat` を持つので取り込む関数を渡す間接が要らない。

- [ ] **Step 3: `Catalog` の生成メソッドを直す**

```go
// newIndexer は取り込みを出す入口ごとに1つ作る。枠は共有し、完了を待つ関門だけ
// が入口ごとに分かれる。
func (c *Catalog) newIndexer() *indexer { return &indexer{cat: c} }
```

- [ ] **Step 4: 生成箇所を直す**

```go
// scan.go:94（フィールド名は Task 4 で直す）
		jobs:        ix.newIndexer(),
// watch.go:77
		jobs:     ix.newIndexer(),
```

- [ ] **Step 5: 整形・検査・テスト**

```bash
gofmt -l . && go build ./... && make vet && make unit-test-race
```

Expected: `gofmt -l` が空、vet と test が通る

- [ ] **Step 6: コミット**

```bash
git add -A internal/index
git commit -m "refactor: Make the per-entry agent an indexer instead of jobs"
```

---

### Task 4: 入口のフィールド名を役に合わせる

**Files:**
- Modify: `internal/index/scan.go`（`Scanner.ix` → `cat`、`scanPass.ix` → `cat`、`scanPass.jobs` → `ix`、参照全箇所）
- Modify: `internal/index/watch.go`（`Watcher.ix` → `cat`、`Watcher.jobs` → `ix`、参照全箇所）
- Modify: `internal/index/scan_test.go`、`internal/index/watch_test.go`（パッケージ内テストが触っていれば）

**Interfaces:**
- Consumes: Task 3 の `indexer`、Task 1 の `Catalog`
- Produces: `Scanner{cat *Catalog, ...}`、`scanPass{cat *Catalog, ix *indexer, ...}`、`Watcher{cat *Catalog, ix *indexer, ...}`

- [ ] **Step 1: `scan.go` を直す**

`Scanner.ix` と `scanPass.ix` を `cat` に、`scanPass.jobs` を `ix` にする。影響する行は `scan.go:33,43,44,56,86-96,117,121,144,146,228,254,272`。

```go
type Scanner struct {
	cat      *Catalog
	interval time.Duration
	...
}

func NewScanner(cat *Catalog, interval time.Duration, kicks <-chan struct{}, log *slog.Logger) *Scanner {
	return &Scanner{cat: cat, interval: interval, kicks: kicks, log: log}
}

type scanPass struct {
	cat *Catalog
	log *slog.Logger
	// ix はこのスキャンの行為者。監視が同時に走るため、完了を待つ相手を自分が
	// 出したぶんに限る。
	ix *indexer
	...
}
```

`Scan` の冒頭はこうなる。

```go
	cat := sc.cat
	known, err := cat.st.AllPaths(ctx)
	if err != nil {
		return Stats{}, err
	}
	s := &scanPass{
		cat:         cat,
		log:         sc.log,
		ix:          cat.newIndexer(),
		known:       known,
		foundByRoot: make(map[string]int, len(cat.roots)),
	}
```

残りは `s.ix.roots` → `s.cat.roots`、`s.ix.removeFile` → `s.cat.removeFile`、`s.jobs.wait()` → `s.ix.wait()`、`s.jobs.submit(...)` → `s.ix.submit(...)`、`sc.ix.roots` → `sc.cat.roots`。

- [ ] **Step 2: `watch.go` を直す**

`Watcher.ix` を `cat` に、`Watcher.jobs` を `ix` にする。影響する行は `watch.go:42,53-55,65,71,75,77,109,135,176,179,182,228,242`。

```go
type Watcher struct {
	cat      *Catalog
	fsw      *fsnotify.Watcher
	...
	// ix は監視の行為者。スキャンが同時に走るため、停止時に待つ相手を自分が
	// 出したぶんに限る。
	ix *indexer
	...
}
```

参照は `w.ix.removeFile` → `w.cat.removeFile`、`w.ix.removeTree` → `w.cat.removeTree`、`w.ix.busy()` → `w.cat.busy()`、`w.ix.roots` → `w.cat.roots`、`w.jobs.wait()` → `w.ix.wait()`、`w.jobs.trySubmit(...)` → `w.ix.trySubmit(...)`、`make(chan indexResult, cap(ix.slots))` → `cap(cat.slots)`。

- [ ] **Step 3: 読み味を確かめる**

```bash
grep -n "w\.ix\.\|s\.ix\.\|w\.cat\.\|s\.cat\." internal/index/scan.go internal/index/watch.go
```

Expected: `ix` に付くのは `submit` / `trySubmit` / `wait` の3つだけ、`cat` に付くのは `roots` / `st` / `removeFile` / `removeTree` / `busy` / `newIndexer` だけ。混ざっていたら直す

- [ ] **Step 4: 整形・検査・テスト**

```bash
gofmt -l . && go build ./... && make vet && go vet -tags browser ./internal/web/ && make unit-test-race
```

Expected: `gofmt -l` が空、vet と test が通る

- [ ] **Step 5: コミット**

```bash
git add -A internal/index
git commit -m "refactor: Name the entry-point fields cat and ix"
```

---

### Task 5: 監査メモを現状に合わせる

**Files:**
- Modify: `docs/superpowers/plans/2026-09-07-index-remaining-issues.md`

**Interfaces:**
- Consumes: Task 1〜4 の新しい名前
- Produces: なし（ドキュメントのみ）

- [ ] **Step 1: 冒頭の表と本文の名前を直す**

`executor` を指していた記述を新しい名前に置き換える。対象は `20,24,25,26,62,161,164,299,313,319,320,321,328` 行目付近。表はこうなる。

```markdown
| `Catalog` | `catalog.go` | 索引そのもの。1枚の手順と、どこを見てどこに書くか、同時取り込みの枠（`slots`） |
| `indexer` | `indexer.go` | 入口ごとの行為者。枠は共有し、完了を待つ相手は自分が出したぶんだけ |
| `submit` | `indexer.go` | 枠が空くまで待つ。`Scan` 用（走査への背圧） |
| `trySubmit` | `indexer.go` | 待たずに false を返す。`Watcher` 用（待つと取りこぼす） |
```

- [ ] **Step 2: 課題7に決着を書く**

`## 課題7: executor の共有に同時実行の規約が無い` の節に、解決済みであることと参照先を書き足す。見出しは `## 課題7: 取り込みの実行単位の共有に同時実行の規約が無い` に改める。

```markdown
**解決済み（2026-10-04）。** `2026-10-04-index-concurrency-roles-design.md` で
行為者を入口ごとの `indexer` にし、`wait()` が自分が出した取り込みだけを待つ形に
した。共有するのは `Catalog` の枠（`slots`）だけで、これは誰も `wait` しない。
課題5で順序を変えて同時実行が常態になっても、互いの完了を待ち合わない。
```

あわせて同じ節の末尾に残っている「`pending` に上限が無い」はこの再編では触っていないので、そのまま残す。

- [ ] **Step 3: 残った古い名前を探す**

```bash
grep -rn "executor\|\bjobs\b" docs/ --include=*.md
```

Expected: `2026-09-07-index-remaining-issues.md` に残っていない。他の spec にこの語が出る場合は、その文書が書かれた時点の記録なので触らない

- [ ] **Step 4: コミット**

```bash
git add docs/superpowers/plans/2026-09-07-index-remaining-issues.md
git commit -m "docs: Update the index audit notes for the new roles"
```

---

## 完了の確認

- [ ] `make build`、`make vet`、`make unit-test-race` が通る
- [ ] `go vet -tags browser ./internal/web/` が通る（ブラウザテストの実行は任意）
- [ ] `grep -rn "Indexer\|executor\|\bjobs\b\|持ち場\|\bsem\b" --include=*.go .` が空（`internal/store` と `internal/session` の `_busy_timeout` を除く）
- [ ] `internal/index` の公開物が `Catalog` / `NewCatalog` / `Scanner` / `NewScanner` / `Watcher` / `NewWatcher` / `Stats` とそれらのメソッドだけであること
