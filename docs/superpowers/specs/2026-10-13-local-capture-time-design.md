# 撮影日時を瞬間ではなく現地の時計の値で持つ

issue #44。

## 経緯

`media.taken_at` は Unix 秒、つまり瞬間を持っている。しかし使い道は並び順、
`DayGroups`、タイルの `data-date` の3つだけで、瞬間として比べている箇所は無い。
日はスキーマに無く、読み出しのたびに `time.Local` で導いている。

これが次の3つの問題を生んでいる。

1. 時差タグの無い EXIF は、`assumeLocal` がサーバの TZ を仮定して瞬間を作っている。
   仮定で作った瞬間と本物の瞬間が同じ列に並び、区別できない
2. 時差タグのある写真は、本物の瞬間をサーバの日で切るので撮影地の日とずれる。
   パリで 3/1 21:00 に撮った Pixel 7a の写真は、TZ=Asia/Tokyo では 03-02 に入る
3. `DayGroups` が全行を Go 側に読んで日に直している

## 設計

撮影日時は、撮影地の時計の値を文字列（`"2006-01-02T15:04:05"`）で持つ。
辞書順が時系列順と一致するので、そのまま並びに使える。日はその先頭10文字で、
索引を張った列として持つ。

時差と出どころ（EXIF / コンテナ / mtime）は持たない。読む機能が無いうちに
値を通す経路を作っても、正しさをテストで縛れないため。必要になったら列を足して
DB を作り直す。

### 読み取り側

`exif` と `videometa` は今までどおり `time.Time` を返す。ただし意味は「その
Location で見た時計の値が撮影地の時刻」に変わり、瞬間としては扱わない。
専用の型は作らない。型を作っても値は imagemeta の `time.Time` から作るしかなく、
時計の値を取り出す箇所が `media.New` から読み取り側へ移るだけで、守られる範囲が
`New` に渡すまでの1行に過ぎないため。

- `exif`: `assumeLocal` を消し、`ex.SelectedDate()` をそのまま返す。時差タグが
  無ければ文字盤の値に UTC のラベルが付いたもの、あれば撮影地の固定ゾーンになる。
  どちらも時計の値は撮影地の時刻である
- `videometa`: QuickTime の文字盤の分岐で `time.Local` へ付け替える処理を消し、
  UTC のラベルのまま返す。Apple の `creationdate` は撮影地の時差のまま。
  qt 以外の mp4 の `mvhd` は UTC の瞬間で、`time.Unix` が `time.Local` にするので、
  サーバの TZ の時計の値になる

### media

```go
type Media struct {
	path         string
	takenAtLocal string    // "2006-01-02T15:04:05"。撮影地の時計の値
	modTime      time.Time
}

func New(path string, takenAt, modTime time.Time) (Media, error)
func Restore(path, takenAtLocal string, modTime time.Time) Media

func (m Media) TakenAtLocal() string // "2006-01-02T15:04:05"
func (m Media) Day() string          // "2006-01-02"。TakenAtLocal の先頭10文字
```

- `New` は `takenAt` をその Location のまま整形する。ゼロ値なら `modTime` を使う
- `TakenAt()` は消す
- `Day()` は `Media` に別に持たせず `takenAtLocal` から切り出す。DB に書く2つの値が
  ずれる経路をこの1行だけにするため

### store

```sql
CREATE TABLE IF NOT EXISTS media (
    id             TEXT PRIMARY KEY,
    path           TEXT NOT NULL UNIQUE,
    taken_at_local TEXT NOT NULL,
    mod_time       INTEGER NOT NULL,
    day            TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_order ON media(taken_at_local DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_media_day   ON media(day);
```

- `Upsert` は `TakenAtLocal()` と `Day()` の両方を書く
- `ListRange` と `RankOf` は `taken_at` を `taken_at_local` に置き換える
- `DayGroups` は `SELECT day, COUNT(*) FROM media GROUP BY day ORDER BY day DESC`。
  `idx_media_day` で covering になり一時B-treeは出ない（issue の実測で 100,000 行
  39 ms → 13 ms）。Go 側で畳む処理と、`strftime` についての不正確なコメントは消す
- 公開 API は変わらない

列名を `taken_at` のまま使い回さないのは、古い DB を確実に断るためである。
`probeReadable` は `selectCols` を選ぶだけなので、`taken_at` が INTEGER のまま
残っていても通り、epoch の数字が文字列として読まれてしまう。

### web

タイルの `data-date` は `m.Day()` にする。

## 影響

- 列が変わるので DB は作り直しになる（移行は書かない）
- サーバの TZ の仮定が残るのは、mtime を使うファイル（EXIF も動画メタも無いもの）と
  qt 以外の mp4（Pixel 7a の動画など）である。Pixel 7a の動画には時差が無く、
  手がかりは GPS（`©xyz`）だけで、タイムゾーンを引くには境界データの依存が要る。
  今回は扱わない
- 時差タグの無い写真の日は、今もこれからもカメラの時計の値で決まる。結果は変わらない

## テスト

公開 API だけで書く。

- media: `FixedZone(+1)` の 2026-03-01 21:00 を `New` に渡すと、`TakenAtLocal()` が
  `2026-03-01T21:00:00`、`Day()` が `2026-03-01` になる（`time.Local` によらない）。
  UTC のラベルの値は時計の値のまま入る。ゼロ値なら mtime になる
- exif / videometa: `assumeLocal` と `time.Local` への付け替えを前提にした期待値を、
  時計の値で比べる形に直す
- store: `time.Local` を書き換える日の境目のテストは消す。`Restore` で文字列を入れて
  並び・`RankOf`・`DayGroups` を確かめる。`taken_at INTEGER` の古い DB を `Open` が
  断ることを確かめる
- web: `time.Local` を書き換えるテストを、タイルの `data-date` が `Day()` と一致する
  ことを確かめる形にする
- `main_test.go` の tzdata の埋め込みの確認は残す。mtime と mp4 は今も `time.Local`
  を使う
