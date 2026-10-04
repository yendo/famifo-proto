# 取り込みの同時実行における役の再編

## 経緯

`33e554b feat: index photos in parallel while watching` で、監視ループから重い仕事を
追い出すために `executor` を導入した。`2026-09-07-index-remaining-issues.md` の課題7は
「`Scan` と `Watcher` が同じ `executor` を共有しているが同時実行の規約が無い」を挙げ、
`wait()` を「自分が出した仕事だけ」に閉じるか、入口ごとに `executor` を持つかの
二択を残していた。前者を選び、`jobs` という単位を切って `wait()` を閉じた。

その結果が再編前の形である。読み直すと、名前が概念を指していない。

- `executor` は行為者名なのに共有されていて、`Indexer` と対等な登場人物に見える
- `jobs` は複数形だが単数の `job` がどこにも無く、存在理由（`wait()` の対象を閉じる）が
  名前から読めない
- `sem` は「channel をセマフォとして使う」という実装を言っており、意味（同時取り込みの
  枠）を言っていない。コメントの「持ち場」という比喩が併走し、語彙が2本ある

名前を付け替えようとすると行き詰まる。`sem` と `wg` を「全体」と「その下位」として
命名しようとしても成立しない。この2つは包含関係にない。同じ goroutine の集合を、
別々の目的で2回数えている。`sem` は資源の上限、`wg` は完了の関門であり、直交している。

行き詰まりの本当の原因は登場人物の数だった。入口ごとの単位に型を与えると、それは
「取り込む人」としか名乗れず、共有側と同じ仕事を名乗る2人目になる。読み手は
`Scanner` / `Watcher` / 共有側 / 入口ごと の4者の関係を一度に把握できない。

関門は `sync.WaitGroup` そのもので足りる。入口がそれを1本持てば、型は要らない。

## 解決する問題

### 1. 共有された行為者に `wait()` を置けない

`w.ix.executor.wait()` は「取り込んでいるものに、終わったか聞く」形で自然に読める。
だが行為者が共有されているため、この呼び出しは相手の仕事の完了まで待つ。スキャンと
監視が同時に走ると、監視はイベントを拾って仕事を足し続けるので返れない（課題7の症状）。

再編前はこれを避けるために `jobs` を挟んでいた。自然に読める形を禁じて、代わりに
名前の付かない単位を導入している。

### 2. 入口ごとの単位が4人目の登場人物になる

`wait()` を閉じるために、関門は入口ごとに持たざるを得ない。そこに型を与えると、
`Scanner` / `Watcher` / 共有側 に続く4人目になり、しかも共有側と同じ「取り込む」を
名乗る。どちらがループを持ち、どちらが仕事を知っているのかが名前から読めない。

### 3. 語彙が2本ある

同じものを「持ち場」と `sem`、別の場所では「枠」と呼んでいる。`executor` と `jobs` も
同じ事柄を2つの語で指している。

## 守る不変条件

再編の前後で変えない。設計はこの6つから導かれる。

1. 監視ループは、所要時間が写真の枚数やサイズに依存する仕事を自分では走らせない
   （`33e554b` の不変条件）
2. 同時に取り込む枚数の上限は全体で1つ。`workers` は CPU とストレージの予算なので、
   入口ごとに持つと合計が2倍になる
3. 入口は「自分が出したぶんだけ」完了を待てる。相手まで待つと、相手が仕事を足し
   続ける限り返れない
4. 投入の作法は入口ごとに逆。走査は枠が空くまで待つ（走査への背圧が要る）、監視は
   待てない（待った時間がイベントを読まない時間になり、カーネルのキューが溢れる）
5. 「どこかで取り込みが走っているか」を監視が聞ける。取り込み中に消えた写真を
   スキャンに回収させるため、入口をまたいだ問いになる
6. 完了通知はワーカーの上で渡し、渡し終えてから枠を返す。順序を逆にすると通知の
   待ち行列が同時取り込み数を超えて伸びうる

不変条件2と5は**全体で1つの状態**を要求し、3は**入口ごとの状態**を要求する。
カウンタが2つ必要であることは動かない。決められるのは、それぞれをどこに置くかだけ。

## 目標

- 登場人物を増やさない。`Indexer` / `Scanner` / `Watcher` の3つで読めること
- 枠に触るコードを1箇所に閉じる
- 不変条件6を守るコードと、それに依拠する `busy()` を同じ型に置く
- 語彙を1本にする（「持ち場」をやめ、枠 = `slots`、関門 = `wg`）

## 目標としないこと

- 振る舞いの変更。既存のテストは名前の追従だけで通るのが期待値である
- `2026-09-07-index-remaining-issues.md` の他の課題。課題1〜6はこの再編では触らない
- 枠の一般化（取り込み以外の仕事を通す）。課題2の「ループの中で歩く」を直すときに決める
- 削除（`removeFile` / `removeTree`）を枠で守ること。これは根1の範囲で、別途

## 設計

登場人物は3つのまま。入口ごとの型は作らない。

| 名前 | 公開 | 役 |
|---|---|---|
| `Indexer` | ○ | 1枚の変更をインデックスとサムネイルに反映する。枠を1つ持つ |
| `Scanner` | ○ | 入口1。定期的に全体を突き合わせる |
| `Watcher` | ○ | 入口2。fsnotify で差分を追う |
| `slots` | × | 同時に取り込める枠。`Indexer` のフィールド |

### `Indexer`（`indexer.go`）

メソッドは3つの層に分かれ、ファイル内もこの順に並べる。

```go
type Indexer struct {
	roots  []string
	st     *store.Store
	thumbs *thumb.Provider
	slots  slots
	log    *slog.Logger
}

func New(roots []string, st *store.Store, thumbs *thumb.Provider, workers int, log *slog.Logger) *Indexer

// 1枚に適用する（同期。同時実行を何も知らない）
func (ix *Indexer) indexFile(ctx context.Context, path string) error
func (ix *Indexer) removeFile(ctx context.Context, path string) error
func (ix *Indexer) removeTree(ctx context.Context, dir string) error

// 枠を取ってから取り込みを始める（非同期。終わるのを待たずに返る）
func (ix *Indexer) startIndex(ctx context.Context, wg *sync.WaitGroup, path string, done func(error))
func (ix *Indexer) tryStartIndex(ctx context.Context, wg *sync.WaitGroup, path string, done func(error)) bool
func (ix *Indexer) run(ctx context.Context, wg *sync.WaitGroup, path string, done func(error))

// 枠の使用状況
func (ix *Indexer) busy() bool
```

`startIndex` は `indexFile` の包みである。足しているものは3つだけ。

| 足すもの | 誰のため |
|---|---|
| 枠の取得と返却 | 不変条件2 |
| goroutine | 不変条件1 |
| `wg` と `done` | 不変条件3 |

`indexFile` は同期で、同時実行を何も知らない。`startIndex` は中身を知らない。向きは
一方通行で、逆は無い。

**なぜ「1枚の適用」と「枠」が同じ型にあるのか。** 包みが両方に触るからである。枠を
別の共有オブジェクトに出すと、包みの置き場が無くなる。パッケージ関数にすると引数が
6つになり、`slots` のメソッドにすると `slots` が goroutine とコールバックを知って
`executor` が復活し、入口の中に書くと不変条件6の順序が2箇所に複製される。包みは
メソッドなので、両方を持つ型に属するしかない。`sql.DB`（プールと `Query`）や
`http.Client`（並行度と `Do`）と同じ組み方である。

### `slots`（`indexer.go` の末尾）

```go
type slots struct{ ch chan struct{} }

func newSlots(n int) slots
func (s slots) take()
func (s slots) tryTake() bool
func (s slots) release()
func (s slots) busy() bool
func (s slots) cap() int
```

チャネル操作を1つの型に閉じる。`Indexer` の本体から `ch <- struct{}{}` や `len` が
消え、`ix.slots.take()` / `ix.slots.release()` になる。型コメントに「枠を返すのは
完了通知を渡し終えたあと（`run` を見よ）」と書き、`busy()` の意味がその順序に依って
いることを資源側からも辿れるようにする。

### 入口（`scan.go` / `watch.go`）

関門は素の `sync.WaitGroup`。型を与えない。

```go
type scanPass struct {
	ix *Indexer
	wg sync.WaitGroup   // このスキャンが出した取り込みの関門
	...
}
defer s.wg.Wait()
s.ix.startIndex(ctx, &s.wg, path, func(err error) { ... })

type Watcher struct {
	ix *Indexer
	wg sync.WaitGroup   // 監視が出した取り込みの関門
	...
}
w.wg.Wait()                             // 停止時
w.ix.tryStartIndex(ctx, &w.wg, path, func(err error) { ... })
if w.ix.busy() { w.requestScan() }      // 誰のぶんでもいいから走っているか
```

`wg` を渡すことが「これは自分が出したぶんだと数えてくれ」の表明になる。`Add` は
`run` の中で行うので、呼び出し側に作法は残らない。入口が持つ参照は `ix` 1つだけである。

### 完了通知と枠の返却

```go
func (ix *Indexer) run(ctx context.Context, wg *sync.WaitGroup, path string, done func(error)) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer ix.slots.release()
		done(ix.indexFile(ctx, path))
	}()
}
```

`defer` は LIFO なので枠の返却が `wg.Done()` より先に走る。`wg.Wait()` が返った時点で
枠も空いていることが、`busy()` の意味を支えている。この順序は不変条件6であり、守る
コードは `run` にしかない。枠と関門の両方に触る唯一の場所だからである。

## 却下した案

### 共有側を `Catalog` と呼ぶ

`-er` で終わらないので入口と同格に見えない、という理由で一度採った。だが名前が中身を
説明しない。インデックスのデータは `store.Store`（`media` テーブル）にあり、この型は
持っていない。`Catalog` は「この型が維持しているもの」を指しており、`Index` も同じ嘘を
つく。加えて写真アプリの世界では Lightroom のカタログのように `Catalog` が**DBファイル
自体**を指す語として定着しているため、積極的に誤読される。

### 共有側を `Writer` と呼ぶ

Lucene の `IndexWriter` を Go の流儀で縮めた形として一度採った。だが「インデックスを
書く」はこのリポジトリの語彙ではなく（コードは一貫して「取り込む」「反映する」）、
サムネイルの生成と削除も担っていることを取りこぼす。Go では `io.Writer` を連想させる
問題もある。

### 入口ごとの型を作る（`jobs` / `indexer`）

`wait()` の宿として一度作った。だが共有側と同じ仕事を名乗る4人目になり、どちらが
ループを持つのかが読めなくなる。`sync.WaitGroup` が既に関門なので、宿は要らない。

### `Indexer` に `take` / `tryTake` / `release` を置く

枠に触るのを `Indexer` の中だけにする案。`slots` 型を入れた今は不要で、足すと公開型の
メソッドの半分がセマフォの API になる。

### 枠を `Indexer` の外の共有オブジェクトにする

包みの置き場が無くなる（上の「なぜ同じ型にあるのか」）。

### パッケージ名を `catalog` にする

パッケージの主題は活動（突き合わせを繰り返してインデックスを保つこと）であり、目録では
ない。`index/exif` と `index/videometa` が下に置かれている理由も「取り込み時にしか
使わない」であって、目録の一部だからではない。

## 引き換えになるもの

- `*sync.WaitGroup` を引数で渡す形は Go であまり見ない。呼ぶのはパッケージ内の2箇所
  だけで、`Add` は内側にある。これを消す形は「関門を宿す型を作る」で、それが4人目の
  登場人物になる問題そのものである
- `Indexer` のメソッドに層が2つ並ぶ（適用と起動）。包みが両方に触るため避けられない
- `Indexer` が同時実行の予算を持つ。不変条件5がこれを強制する

## 将来これを崩す条件

- 取り込みの入口が3つ目に増えたとき。`wg` を渡す箇所が増え、渡し間違いの余地が実際に
  効いてくる
- 枠を取り込み以外の仕事にも通すようになったとき（課題2に着手するとき）。予算が
  `Indexer` 固有でなくなるので、資源を外に出す形が正しくなる

## テスト

既存のテストが名前の追従だけで通ることを確認する。`go test -race ./...` を通す。
新しいテストは足さない。公開される振る舞いが変わらず、`slots` と `run` は非公開で
あり、そこを直接触るテストは書かない方針のためである。

同時実行の性質は既存のテストが押さえている。

- `TestScanDoesNotWaitForTheWatchersIndexing`（`scan_test.go`）— 不変条件3
- `waitForIndexing` を使う監視側のテスト群 — 不変条件1と4
- 取り込み中の削除がスキャンの前倒しを要求すること、およびアイドル時に要求しないこと
  （`watch_test.go`）— 不変条件5
