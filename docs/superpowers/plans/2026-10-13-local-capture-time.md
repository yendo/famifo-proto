# 撮影日時を現地の時計の値で持つ 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 撮影日時を Unix 秒の瞬間ではなく、撮影地の時計の値の文字列 `taken_at_local` と、その日 `day` で持つ。

**Architecture:** `exif` と `videometa` は `time.Time` を返したまま、意味を「その Location で見た時計の値」に変える。`media.New` がそれを `"2006-01-02T15:04:05"` に整形して持ち、`Day()` は先頭10文字を返す。store は2列を書き、`DayGroups` は `day` の索引で SQL に束ねさせる。

**Tech Stack:** Go、`modernc.org/sqlite`、testify

**Spec:** `docs/superpowers/specs/2026-10-13-local-capture-time-design.md`

## Global Constraints

- 時差と出どころの列は持たない
- 撮影日時のための専用の型は作らない
- `Media` のメソッド名は `TakenAtLocal()` と `Day()`。`TakenAt()` は消す
- 列の並びは `id, path, taken_at_local, mod_time, day`
- スキーマ移行は書かない。DB は作り直す
- テストは公開 API だけで書く。新規は外部テストパッケージ
- `time.Local` を書き換えるテストは `t.Parallel()` を呼ばない
- コメントは周囲に合わせて日本語、コミットメッセージは英語1行
- ブランチは `feature-local-capture-time`

---

### Task 1: Media と store を現地の時計の値に切り替える

`media` の公開 API を変えるとビルドが store・web・テストまで一度に壊れるので、1つのタスクで切り替える。

**Files:**
- Modify: `internal/media/media.go`
- Modify: `internal/store/store.go`
- Modify: `internal/web/view.go:88`
- Modify: `docs/design.md:74-84, 137`
- Modify: `docs/superpowers/specs/2026-10-13-local-capture-time-design.md`（古い DB を断る箇所の記述）
- Test: `internal/media/media_test.go`
- Test: `internal/store/store_test.go`
- Test: `internal/web/web_test.go`
- Test: `internal/web/browser_test.go:2297-2300`
- Test: `internal/thumb/thumb_test.go:27, 241`

**Interfaces:**
- Consumes: なし。今の読み取り側も時計の値は撮影地の時刻を保っている（`assumeLocal` は時計の値を変えずに Location だけ付け替える）ので、先に切り替えても日は変わらない
- Produces:
  - `func media.New(path string, takenAt, modTime time.Time) (media.Media, error)`
  - `func media.Restore(path, takenAtLocal string, modTime time.Time) media.Media`
  - `func (m media.Media) TakenAtLocal() string` — `"2006-01-02T15:04:05"`
  - `func (m media.Media) Day() string` — `"2006-01-02"`
  - store の公開 API は変わらない

- [ ] **Step 1: media のテストを書く**

`internal/media/media_test.go` の `TestNewUsesTheCaptureTimeWhenPresent`、`TestNewFallsBackToModTimeWithoutACaptureTime`、`TestRestoreKeepsTheStoredValues` を次に置き換える:

```go
// issue #44 のパリの例。時差タグのある写真は撮影地の時計の値で入り、
// サーバのタイムゾーンによらず撮影地の日になる。
func TestNewKeepsTheLocalTimeWhereItWasShot(t *testing.T) {
	t.Parallel()
	paris := time.FixedZone("CET", 60*60)
	takenAt := time.Date(2026, 3, 1, 21, 0, 0, 0, paris)

	m, err := media.New("/items/a.jpg", takenAt, testModTime)

	require.NoError(t, err)
	require.Equal(t, "2026-03-01T21:00:00", m.TakenAtLocal())
	require.Equal(t, "2026-03-01", m.Day())
}

// 時差の分からない値は UTC のラベル付きで届く。ラベルは無視して時計の値を使う。
func TestNewKeepsTheDialTimeOfAnUnknownOffset(t *testing.T) {
	t.Parallel()
	takenAt := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)

	m, err := media.New("/items/a.jpg", takenAt, testModTime)

	require.NoError(t, err)
	require.Equal(t, "2021-03-04T05:06:07", m.TakenAtLocal())
}

func TestNewFallsBackToModTimeWithoutACaptureTime(t *testing.T) {
	t.Parallel()
	m, err := media.New("/items/a.jpg", time.Time{}, testModTime)

	require.NoError(t, err)
	// mtimeは瞬間なので、サーバの時刻で見た時計の値になる
	require.Equal(t, testModTime.Local().Format("2006-01-02T15:04:05"), m.TakenAtLocal(),
		"a item with no capture time still stays in the gallery")
}

func TestRestoreKeepsTheStoredValues(t *testing.T) {
	t.Parallel()
	const path = "/items/a.jpg"

	m := media.Restore(path, "2021-03-04T05:06:07", testModTime)

	require.Equal(t, media.IDFor(path), m.ID())
	require.Equal(t, path, m.Path())
	require.Equal(t, "2021-03-04T05:06:07", m.TakenAtLocal())
	require.Equal(t, "2021-03-04", m.Day())
	require.True(t, m.ModTime().Equal(testModTime))
}
```

- [ ] **Step 2: テストが落ちることを確かめる**

Run: `go test ./internal/media/`
Expected: コンパイルエラー（`TakenAtLocal`、`Day` が無い、`Restore` の引数型が違う）

- [ ] **Step 3: media を書き換える**

`internal/media/media.go`:

```go
// localLayout は TakenAtLocal の書式。時差を持たず、辞書順が時系列順と一致する。
const localLayout = "2006-01-02T15:04:05"

type Media struct {
	path         string    // ディスク上の絶対パス
	takenAtLocal string    // 撮影地の時計の値（localLayout）。読み取れなければmtime
	modTime      time.Time // ファイルのmtime。再スキャン時の変更検知に使う
}

// New はインデックスに載せる1件を組み立てる。IsCandidate を満たさない
// パスはエラーにする。
//
// takenAt はファイルから読み取った撮影日時。写真ならEXIF、動画ならコンテナから
// 読み取り側が解釈を済ませた値である。瞬間としては扱わず、その Location で見た
// 時計の値だけを使う。時差の分からない値は UTC のラベル付きで、時差の分かる値は
// 撮影地の固定ゾーンで届くが、どちらも時計の値が撮影地の時刻である。
//
// ゼロ値は「読み取れなかった」ことを表し、mtimeで代替される。撮影日時が取れない
// ファイル（スクリーンショット、GIF、WebP、EXIFを削ぎ落とされた画像、コンテナの
// 壊れた動画）は普通に存在し、それらを一覧から落とさないため。mtimeは瞬間なので、
// サーバの time.Local で見た時計の値になる。
//
// サムネイルの調達より先に組み立てる。ModTime が原本の版であり、
// サムネイルの置き場所はその版から決まるため。
func New(path string, takenAt, modTime time.Time) (Media, error) {
	if !IsCandidate(path) {
		return Media{}, fmt.Errorf("%s cannot be indexed", path)
	}
	if takenAt.IsZero() {
		takenAt = modTime.Local()
	}
	return Media{
		path:         path,
		takenAtLocal: takenAt.Format(localLayout),
		modTime:      modTime,
	}, nil
}

// Restore はインデックスに保存済みの1件を組み立て直す。store が読み出しに使う。
//
// 条件は確かめない。インデックスの行は New を通ったものだけである。
func Restore(path, takenAtLocal string, modTime time.Time) Media {
	return Media{
		path:         path,
		takenAtLocal: takenAtLocal,
		modTime:      modTime,
	}
}
```

`TakenAt()` を消し、`Path()` の後ろに次を置く:

```go
// TakenAtLocal は撮影地の時計の値を "2006-01-02T15:04:05" で返す。時差は持たない。
// ファイルから読み取れなければmtimeをサーバの時刻で見た値。
func (m Media) TakenAtLocal() string { return m.takenAtLocal }

// Day は撮った日を "2006-01-02" で返す。一覧の区切りに使う。
//
// TakenAtLocal から切り出す。インデックスには両方を書くので、別に持つと
// 2つがずれる経路ができる。
func (m Media) Day() string { return m.takenAtLocal[:len("2006-01-02")] }
```

- [ ] **Step 4: media のテストが通ることを確かめる**

Run: `go test ./internal/media/`
Expected: PASS

- [ ] **Step 5: store のテストを直す**

`internal/store/store_test.go`:

`mediaAt` は `media.New` を通す。時計の値の整形を store のテストで書き直さないため:

```go
// mtimeは撮影日時とずらす。SELECTの列順とScanの並びが入れ替わったとき、
// 同じ値だと往復で気づけない。
func mediaAt(t *testing.T, path string, takenAt time.Time) media.Media {
	t.Helper()
	m, err := media.New(path, takenAt, takenAt.Add(time.Hour))
	require.NoError(t, err)
	return m
}
```

呼び出しはすべて `mediaAt(t, ...)` にする。`TestDeleteByPathPrefixTreatsWildcardsAsLiterals` のサブテストでは内側の `t` を渡す。

`TestUpsertThenGetByID` と `TestUpsertReplacesExistingRow` の比較:

```go
	require.Equal(t, want.TakenAtLocal(), got.TakenAtLocal())
	require.Equal(t, want.Day(), got.Day())
```

```go
	require.Equal(t, m.TakenAtLocal(), got.TakenAtLocal())
```

`TestAllPaths` の組み立て:

```go
	m := media.Restore("/items/a.jpg", "2020-09-13T12:26:40", time.Unix(1650000000, 0))
```

`TestDayGroupsCountsEachDay` は `time.Local` を `time.UTC` に替える（時計の値だけが効くので Location はどれでもよい）。

`TestDayGroupsUsesLocalTime` を次に置き換える。日は撮影地の時計の値で決まり、サーバの時差を通らないことを押さえる:

```go
// 日は撮影地の時計の値で決まる。パリで夜に撮った写真は、サーバが JST でも
// その日に入る。瞬間をサーバの日で切ると翌日に回る。
func TestDayGroupsUsesTheDayWhereItWasShot(t *testing.T) {
	// time.Local を書き換えるので t.Parallel() を呼ばない。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })
	s := openTestStore(t)
	ctx := context.Background()
	paris := time.FixedZone("CET", 60*60)
	require.NoError(t, s.Upsert(ctx, mediaAt(t, "/items/a.jpg", time.Date(2026, 3, 1, 21, 0, 0, 0, paris))))

	got, err := s.DayGroups(ctx)

	require.NoError(t, err)
	require.Equal(t, []store.DayGroup{{Date: "2026-03-01", Count: 1}}, got,
		"cutting the instant in JST would give 2026-03-02")
}
```

`TestDayGroupsTotalMatchesCountAndListRange` は `base` の `time.Local` を `time.UTC` に、末尾の比較を次にする:

```go
			require.Equal(t, g.Date, all[offset].Day(),
				"the item at offset=%d should be from %s", offset, g.Date)
```

`TestOpenRejectsADatabaseWhoseColumnsHaveChanged` の古い表に `day` を足す。`idx_media_day` の作成が通り、読み取り確認まで届くようにするため:

```go
	// taken_at_local を別名にした古い世代のDB。索引の名前と day は今と同じなので、
	// CREATE INDEX IF NOT EXISTS は作り直さず、スキーマ作成は素通りする。
	_, err = db.Exec(`
CREATE TABLE media (
    id       TEXT PRIMARY KEY,
    path     TEXT NOT NULL UNIQUE,
    shot_at  TEXT NOT NULL,
    mod_time INTEGER NOT NULL,
    day      TEXT NOT NULL
);
CREATE INDEX idx_media_order ON media(shot_at DESC, id DESC);`)
```

その後ろに、epoch で持っていた1つ前の世代を断るテストを足す:

```go
// 撮影日時を Unix 秒で持っていた世代のDB。taken_at が INTEGER のまま残っていても
// 読み進めず、作り直しを求める。
func TestOpenRejectsADatabaseThatStoresTheCaptureTimeAsAnInstant(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "epoch.db")

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`
CREATE TABLE media (
    id        TEXT PRIMARY KEY,
    path      TEXT NOT NULL UNIQUE,
    taken_at  INTEGER NOT NULL,
    mod_time  INTEGER NOT NULL
);
CREATE INDEX idx_media_order ON media(taken_at DESC, id DESC);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = store.Open(path)

	require.Error(t, err, "the database has to be rebuilt")
}
```

- [ ] **Step 6: store のテストが落ちることを確かめる**

Run: `go test ./internal/store/`
Expected: コンパイルエラー（`store.go` が `m.TakenAt()` と `media.Restore` の旧シグネチャを使っている）

- [ ] **Step 7: store を書き換える**

`internal/store/store.go`:

```go
const schema = `
CREATE TABLE IF NOT EXISTS media (
    id             TEXT PRIMARY KEY,
    path           TEXT NOT NULL UNIQUE,
    taken_at_local TEXT NOT NULL,
    mod_time       INTEGER NOT NULL,
    day            TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_order ON media(taken_at_local DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_media_day ON media(day);
`
```

```go
const upsertSQL = `
INSERT INTO media (id, path, taken_at_local, mod_time, day)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    path           = excluded.path,
    taken_at_local = excluded.taken_at_local,
    mod_time       = excluded.mod_time,
    day            = excluded.day`

// Upsert は1件を登録または更新する。
func (s *Store) Upsert(ctx context.Context, m media.Media) error {
	_, err := s.db.ExecContext(ctx, upsertSQL,
		m.ID(), m.Path(), m.TakenAtLocal(), m.ModTime().Unix(), m.Day())
```

`ListRange` の `ORDER BY` を `taken_at_local DESC, id DESC` に、`RankOf` のコメントと本体を次にする:

```go
// 並びは ListRange と同じ taken_at_local DESC, id DESC でなければならない。順序式が
// 二重になるが、片方だけ変えるとURLが別の1件の位置を指すため、
// TestRankOfLocatesTheItemInListRange が両者の一致を縛っている。
func (s *Store) RankOf(ctx context.Context, id string) (int, error) {
	var takenAtLocal string
	err := s.db.QueryRowContext(ctx, `SELECT taken_at_local FROM media WHERE id = ?`, id).Scan(&takenAtLocal)
	...
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media
		 WHERE taken_at_local > ? OR (taken_at_local = ? AND id > ?)`,
		takenAtLocal, takenAtLocal, id).Scan(&rank)
```

`DayGroup` と `DayGroups`:

```go
// DayGroup は、その日に撮った件数。一覧の区切りに使う。
type DayGroup struct {
	Date  string // "2006-01-02" 形式。撮影地の日
	Count int
}

// DayGroups は日ごとの件数を新しい順に返す。一覧の区切りとスクラバーの目盛りに使う。
//
// day に索引があるので、SQLiteは索引を順に読むだけで束ね、一時B-treeに積み直さない。
func (s *Store) DayGroups(ctx context.Context) ([]DayGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, COUNT(*) FROM media GROUP BY day ORDER BY day DESC`)
	if err != nil {
		return nil, fmt.Errorf("cannot get the capture dates: %w", err)
	}
	defer rows.Close()

	var out []DayGroup
	for rows.Next() {
		var g DayGroup
		if err := rows.Scan(&g.Date, &g.Count); err != nil {
			return nil, fmt.Errorf("cannot read the capture dates: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
```

`selectCols` と `scanMedia`:

```go
const selectCols = `path, taken_at_local, mod_time`

// scanMedia は1行を復元する。引数の並びは selectCols と一致していなければならない。
func scanMedia(row interface{ Scan(...any) error }) (media.Media, error) {
	var path, takenAtLocal string
	var modTime int64
	if err := row.Scan(&path, &takenAtLocal, &modTime); err != nil {
		return media.Media{}, err
	}
	return media.Restore(path, takenAtLocal, time.Unix(modTime, 0)), nil
}
```

`selectCols` のコメントの「idは含めない」の段落はそのまま残す。`day` も含めない（`Day()` は `taken_at_local` から導ける）ことを1行足す。

- [ ] **Step 8: 索引が covering で使われることを確かめる（コミットしない）**

`internal/store/plan_scratch_test.go` を一時的に置く:

```go
package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/store"
)

func TestScratchDayGroupsPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT day, COUNT(*) FROM media GROUP BY day ORDER BY day DESC`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		t.Log(detail)
	}
}
```

Run: `go test ./internal/store/ -run TestScratchDayGroupsPlan -v`
Expected: `SCAN media USING COVERING INDEX idx_media_day` が出て、`USE TEMP B-TREE` が出ない。出たら止めて報告する。

確かめたらファイルを消す: `rm internal/store/plan_scratch_test.go`

- [ ] **Step 9: store のテストが通ることを確かめる**

Run: `go test ./internal/store/`
Expected: PASS

- [ ] **Step 10: web と残りの呼び出しを直す**

`internal/web/view.go`:

```go
			Date:     m.Day(),
```

`internal/web/web_test.go` の `addMedia` は `media.New` を通す（原本は `f.mediaDir` 配下で候補を満たす）:

```go
	m, err := media.New(path, takenAt, takenAt)
	require.NoError(t, err)
	require.NoError(t, f.st.Upsert(context.Background(), m))
```

`TestGalleryEmbedsDayGroups`、`TestDatesEndpointIsGone`、`TestGalleryTagsFirstChunkWithDates` の `time.Local` は `time.UTC` に替える。

`TestTilesTagsEachTileWithLocalDate` を次に置き換える:

```go
// タイルの日は撮影地の日である。パリで夜に撮った写真は、サーバが JST でも
// その日のタイルになる。
func TestTilesTagsEachTileWithTheDayWhereItWasShot(t *testing.T) {
	// time.Local を書き換えるので t.Parallel() を呼ばない。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })
	f := newWebFixture(t, 60)
	paris := time.FixedZone("CET", 60*60)
	f.addMedia(t, "a.jpg", time.Date(2026, 3, 1, 21, 0, 0, 0, paris), famifoThumb)

	body := doGet(t, f.h, "/tiles?chunk=0").Body.String()

	require.Contains(t, body, `data-date="2026-03-01"`,
		"cutting the instant in JST would give 2026-03-02")
}
```

`internal/web/browser_test.go:2297-2300`:

```go
	video, err := media.New(videoPath, time.Unix(1600000100, 0), time.Unix(1600000100, 0))
	require.NoError(t, err)
	require.NoError(t, st.Upsert(bg, video))
	still, err := media.New(mediaPath, time.Unix(1600000000, 0), time.Unix(1600000000, 0))
	require.NoError(t, err)
	require.NoError(t, st.Upsert(bg, still))
```

（周囲の変数名と衝突したら合わせて変える）

`internal/thumb/thumb_test.go` の `mediaOf`:

```go
	m, err := media.New(src, fi.ModTime(), fi.ModTime())
	require.NoError(t, err)
	return m
```

`thumb_test.go:241`:

```go
	m, err := media.New(missing, time.Unix(1600000000, 0), time.Unix(1600000000, 0))
	require.NoError(t, err)
```

- [ ] **Step 11: 全体のテストを走らせる**

Run: `go vet ./... && go vet -tags browser ./... && make unit-test-race`
Expected: PASS

Run: `make browser-test`
Expected: PASS

- [ ] **Step 12: ドキュメントを直す**

`docs/design.md` の「撮影日時の時差」の項を次に置き換える:

```markdown
- **撮影日時の時差**: 撮影日時は瞬間ではなく、撮影地の時計の値（`"2006-01-02T15:04:05"`）で
  持つ。並びと日ごとの区切りの軸は「撮った日」であり、瞬間として比べる箇所は無い
  - EXIFのDateTimeOriginalは時差を持たない。時計の値をそのまま使い、サーバの
    タイムゾーンで瞬間に直さない
  - `OffsetTimeOriginal` を持つ写真も時計の値を使う。瞬間をサーバの日で切ると、
    旅行先の夜に撮った写真が翌日に回る
  - 日（`day`）は時計の値の先頭10文字で、索引を張った列として持つ
  - サーバのタイムゾーンが効くのは、時計の値が分からないものだけである。mtimeに
    落ちたファイルと、`mvhd` に UTC の瞬間しか持たない mp4（Pixel 7a の動画など）。
    そのためタイムゾーンデータベースをバイナリに埋め込み、`TZ` の設定を必須とする
```

同じファイルの137行目は `assumeLocal`（時差）を例から外す:

```markdown
  - これまで非公開関数を直接テストした例は無い。`sourcePixel`（回転）も
    `dirContains`（パス包含）も公開API経由で覆えており、
```

spec（`docs/superpowers/specs/2026-10-13-local-capture-time-design.md`）の「列名を `taken_at` のまま使い回さないのは」の段落を、実際の挙動に合わせて直す:

```markdown
古い DB は `Open` が断る。`taken_at INTEGER` の世代は `day` を持たないので、
`idx_media_day` の作成で止まる。列名を `taken_at` のまま使い回さないのは、
`day` が揃っている DB でも `probeReadable` で止めるためである。使い回すと、
INTEGER の列が文字列として読まれて通ってしまう。
```

- [ ] **Step 13: コミット**

```bash
git add internal/media internal/store internal/web internal/thumb docs
git commit -m "feat: Store the capture time as local civil time with its day"
```

---

### Task 2: 読み取り側の付け替えを消す

**Files:**
- Modify: `internal/index/exif/exif.go`
- Modify: `internal/index/videometa/videometa.go`
- Test: `internal/index/exif/exif_test.go`
- Test: `internal/index/videometa/videometa_test.go`

**Interfaces:**
- Consumes: Task 1 の `media.New`（Location を見ずに時計の値だけを使う）
- Produces: `exif.Meta.TakenAt` と `videometa.Meta.TakenAt` は「その Location で見た時計の値が撮影地の時刻」。型は `time.Time` のまま

- [ ] **Step 1: exif のテストを時計の値で比べる形に直す**

`internal/index/exif/exif_test.go` の `TestReadReturnsTheEXIFDate` のコメントを直し、`useJST` と `TestReadAssumesLocalWhenTheEXIFDateHasNoOffset` を次に置き換える。`TestReadKeepsTheEXIFOffset` は `useJST` を外して時計の値で比べる。

```go
func TestReadReturnsTheEXIFDate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writeJPEGWithEXIF(t, dir, "a.jpg", time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC))

	got := exif.Read(path)

	// 時計の値で比べる。TakenAt の Location は時差タグの有無を映すだけで、
	// 瞬間としては使わない。
	require.Equal(t, "2021-03-04 05:06:07", got.TakenAt.Format("2006-01-02 15:04:05"))
}

// EXIFのDateTimeOriginalは時差を持たない。サーバのタイムゾーンで解釈し直すと、
// 文字盤の値がそのタイムゾーンの瞬間にすり替わる。時計の値はそのまま返す。
func TestReadKeepsTheDialTimeWhenTheEXIFDateHasNoOffset(t *testing.T) {
	// time.Local を書き換えるので t.Parallel() を呼ばない。TZ=UTC の環境では
	// 付け替えても時計の値が変わらず、回帰を検出できないため。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })
	path := writeJPEGWithEXIF(t, t.TempDir(), "a.jpg", time.Date(2025, 5, 24, 9, 8, 31, 0, time.UTC))

	got := exif.Read(path)

	require.Equal(t, "2025-05-24 09:08:31", got.TakenAt.Format("2006-01-02 15:04:05"),
		"the dial time is kept as it is: got=%v", got.TakenAt)
}

// OffsetTimeOriginal がある写真も、撮影地の時計の値をそのまま返す。
// サーバの時差で見直すと、パリの夜に撮った写真が日本の翌日に回る。
func TestReadKeepsTheEXIFOffset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// ベルリン(+02:00)で正午に撮った写真
	path := writeJPEGWithEXIFOffset(t, dir, "a.jpg",
		time.Date(2023, 8, 4, 12, 0, 0, 0, time.UTC), "+02:00")

	got := exif.Read(path)

	require.Equal(t, "2023-08-04 12:00:00", got.TakenAt.Format("2006-01-02 15:04:05"),
		"still noon where it was shot: got=%v", got.TakenAt)
}
```

- [ ] **Step 2: 今の実装でも通ることを確かめる**

このタスクは時計の値を変えない整理である。Location の付け替えを消すだけで、Task 1 の `media.New` は Location を見ないので、外から見える挙動は変わらない。テストは「時計の値が保たれる」ことを、付け替えの前後で縛る。

Run: `go test ./internal/index/exif/ -v`
Expected: PASS

- [ ] **Step 3: `assumeLocal` を消す**

`internal/index/exif/exif.go`:

```go
// Meta は写真1枚から読み取れたEXIF。読めなかった項目は既定値になる。
type Meta struct {
	// TakenAt はEXIFの撮影日時。持たない写真ではゼロ値。
	// 時計の値（Location で見た年月日と時刻）が撮影地の時刻であり、瞬間としては
	// 扱わない。Location は時差タグがあれば撮影地の固定ゾーン、無ければ UTC だが、
	// 後者の UTC は「時差が分からない」ことの代わりで、本物の UTC ではない。
	TakenAt time.Time
	// Orientation は表示時の向き。1..8以外だった場合は回転不要の1にする。
	Orientation uint16
}
```

`Read` の中は次のとおり:

```go
	m = unknown
	// SelectedDate は DateTimeOriginal → CreateDate → ModifyDate の順に探し、
	// どれも無ければゼロ値を返す。
	m.TakenAt = ex.SelectedDate()
```

`assumeLocal` 関数とそのコメントを丸ごと削除する。

- [ ] **Step 4: videometa のテストを直す**

`internal/index/videometa/videometa_test.go` の QuickTime の2つのテストの比較を時計の値にする:

```go
	require.Equal(t, "2011-01-11 20:05:54", got.Format("2006-01-02 15:04:05"), "got %v", got)
```

（`TestReadTreatsQuickTimeWithoutAppleKeyAsLocalTime` と `TestReadDetectsQuickTimeFromACompatibleBrand` の両方。テスト名の `AsLocalTime` は `AsDialTime` に改名する）

他のテストは瞬間で比べており、今回の変更で瞬間は変わらないのでそのままでよい。

- [ ] **Step 5: QuickTime の分岐の付け替えを消す**

`internal/index/videometa/videometa.go` の `Meta` と `resolve`:

```go
// Meta は動画1本から読み取れた情報。読めなかった項目は既定値になる。
type Meta struct {
	// TakenAt は撮影日時。読めなければゼロ値。
	// exif と同じく、時計の値（Location で見た年月日と時刻）が撮影地の時刻である。
	// 呼び出し側はゼロ値をmtimeに落とす（EXIFが無い写真と同じ扱い）。
	TakenAt time.Time
}
```

```go
	if quicktime {
		// QuickTimeの古い機種は文字盤の時刻を書く。時差は分からないので、
		// EXIF と同じく UTC のラベルのまま時計の値として返す。
		return time.Unix(secs, 0).UTC()
	}
	// それ以外は UTC の瞬間で、撮影地の時差はファイルに無い。サーバの
	// time.Local で見た時計の値になる（time.Unix が Local を付ける）。
	return time.Unix(secs, 0)
```

パッケージコメントの一覧は変えない（規約の説明として今も正しい）。

- [ ] **Step 6: テストが通ることを確かめる**

Run: `go vet ./... && make unit-test-race`
Expected: PASS

- [ ] **Step 7: コミット**

```bash
git add internal/index/exif internal/index/videometa
git commit -m "refactor: Return the capture time as the dial time without reinterpreting it"
```

---

## 実装後に伝えること

- 既存の DB は作り直しが要る（`Open` が断る）。DB ファイルを消して再スキャンする
