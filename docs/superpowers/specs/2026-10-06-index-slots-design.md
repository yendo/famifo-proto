# 取り込みの枠を Indexer から外す

## 経緯

`2026-10-04-index-concurrency-roles-design.md` で、取り込みの枠（`slots`）を `Indexer` に
持たせ、`startIndex` / `tryStartIndex` が枠を取って goroutine を起こす形にした。
「枠を取って goroutine を起こす包みが、適用と枠の両方に触るので同じ型に置くしかない」
という理由だった。

見直すと、これは `Indexer` に2つの仕事（1枚の適用と、同時実行の枠）を持たせている
ことの言い換えだった。呼び出しの形も素直でない。`startIndex(ctx, wg, path, done)` は
`*sync.WaitGroup` と完了のコールバックを受け取り、呼ぶ側が関門の作法を知っていないと
使えない。`slots` 型はチャネル1本を包むために6つのメソッドを持つ。

`sync.WaitGroup.Go`（Go 1.25）を使えば、包みは入口ごとの数行になり、置き場が要らない。

## 設計

- `Indexer` は1枚を同期的に適用するだけにする（`indexFile` / `removeFile` / `removeTree`）。
  並行について何も知らない
- 枠は独立した公開型 `Slots` にする。何も走らせない受け身の枠で、`Indexer` を知らない

```go
type Slots struct{ ch chan struct{} }

func NewSlots(n int) *Slots       // n < 1 なら panic
func (s *Slots) acquire()         // 空くまで待つ（スキャン）
func (s *Slots) tryAcquire() bool // 待たない（監視）
func (s *Slots) release()
func (s *Slots) busy() bool       // 使用中の枠があるか
func (s *Slots) size() int        // 容量

func New(roots []string, st *store.Store, thumbs *thumb.Provider, log *slog.Logger) *Indexer
func NewScanner(ix *Indexer, slots *Slots, interval time.Duration, kicks <-chan struct{}, log *slog.Logger) *Scanner
func NewWatcher(ix *Indexer, slots *Slots, log *slog.Logger) (*Watcher, error)
```

- `main.go` が `NewSlots(cfg.ScanWorkers)` を1つ作り、`NewScanner` と `NewWatcher` に渡す。
  上限をスキャンと監視で共有することが、組み立てる場所に見える
- goroutine は入口が自分で起こす。枠を取り、`wg.Go` の中で `defer slots.release()` を
  先頭に置き、取り込みと結果の受け渡しを書く

## 引き換え

goroutine を起こす数行と「結果を渡し終えてから枠を返す」順序が、スキャンと監視の
2か所に並ぶ。10-04 ではこの重複を避けるために包みを `Indexer` に置いた。ここでは
`Indexer` の責務を1つにすることを優先し、重複を受け入れる。

executor（a5a4dd9 で消した）とは違う。executor は `Indexer` を参照して取り込みを走らせる
主体だった。`Slots` は何も走らせない。

## 範囲の外

`busy()` による入口をまたいだスキャンの前倒しは残す。取り込み中のパスを誰が持つかは
別の問題として扱う。

## テスト

振る舞いは変えない。既存のテストを新しい形に合わせて通す。`workers` が1未満で
panic するテストは `NewSlots` のテストにする。新しいテストは足さない。
