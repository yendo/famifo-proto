# 撮影者で一覧を絞り込む 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一覧を撮影者（`owner_uid`）で絞り込めるようにする。撮影者は複数選べ、PC は左のサイドバー、スマホは下から出るシートで選ぶ。

**Architecture:** store に条件を表す `Filter{Owners []uint32}` を足し、`Count`・`DayGroups`・`ListRange`・`RankOf` が同じ WHERE 句で絞る（Task 1）。web は `?owner=` を `Filter` に読み、一覧・チャンク・個別表示の URL をすべて絞り込み込みでサーバーが作る（Task 2）。JS はサーバーから受け取った URL でチャンクを取り、個別表示を閉じる（Task 3）。最後に絞り込みのパネルを足す。中身はリンクだけで、画面幅で CSS がサイドバーとシートを切り替える（Task 4）。文書を直す（Task 5）。

**Tech Stack:** Go（`net/http`、`html/template`、`net/url`、`slices`、modernc.org/sqlite）、vanilla JS（ES モジュール、JSDoc + `tsc` の型検査）、chromedp（ブラウザテスト）

**Spec:** [docs/superpowers/specs/2026-11-01-owner-filter-design.md](../specs/2026-11-01-owner-filter-design.md)

## Global Constraints

- コードコメントとテストのコメントは日本語（既存ファイルに合わせる）。識別子、ログのメッセージ、エラーメッセージ、テストの失敗メッセージ、`docs/web-structure.md` は英語
- 実機の個人情報（実名、アカウント名、実際の uid、NAS のパス）をコード・テスト・文書に書かない。例の uid は `1000` / `1001` / `0`、名前は `owner.Name` が返すものをそのまま使い、テストに書き込まない
- コミットメッセージは英語の要約1行のみ。本文とトレーラーは付けない。type は `feat:` / `fix:` / `refactor:` / `test:` / `docs:` から選ぶ
- ブランチは `feature-owner-filter`（既にこのブランチ上にある）
- テストは公開APIだけで書く。外部テストパッケージ（`store_test` / `web_test` / `index_test`）。テストのためだけの公開や `export_test.go` への追加はしない
- 単体テスト: `make unit-test-race`。1つだけなら `go test -race -run '<名前>' ./internal/<pkg>/`
- ブラウザテスト: `make browser-test`。1つだけなら `FAMIFO_BROWSER_TESTS=required go test -tags browser -run '<名前>' ./internal/web/`。コンパイルの確認だけなら `go vet -tags browser ./internal/web/`
- JS の型検査: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`（リポジトリのルートで）
- `Filter` は呼び出し側が構造体リテラルで組み立てる。`WithOwner` のようなメソッド、`NewFilter`、絞り込んだ見え方を返す型は作らない
- 列は変えない。スキーマ移行は書かない。索引は `CREATE INDEX IF NOT EXISTS` で足す
- `chown` には追従しない。走査の飛ばし条件と watcher は変えない
- URL の規則は Go に置く。JS は絞り込みのクエリを組み立てない（chunk.js がチャンクの番号を足すだけ）
- JS では HTML の文字列を組み立てない
- CSP の値は変えない
- 宣言の並びは入口が先。ハンドラはルートの順、補助はその後ろに出てくる順。型とメソッドの間に補助を挟まない
- 共有のテストヘルパーは `internal/web/helper_test.go`（単数形）に置く。1つのテストファイルだけが使うものはそのファイルに置く

## ファイル構成

| ファイル | 変更 |
|---|---|
| `internal/store/store.go` | `Filter`、`where`、4メソッドの引数、`Owners`、索引2つ |
| `internal/store/store_test.go` | 絞り込みのテスト。既存の呼び出しに `store.Filter{}`。古いDBのテストの表に `owner_uid` |
| `internal/index/scan_test.go` / `watch_test.go` / `index_test.go` | `Count` の呼び出しに `store.Filter{}`（追従のみ） |
| `internal/web/filter.go`（新規） | `filterFrom`、`filterURL`、`toggleOwner`、`ownerChoices` |
| `internal/web/view.go` | `indexView` の URL と撮影者の節、`buildIndexView` と `listMedia` が `Filter` を取る |
| `internal/web/web.go` | ハンドラが `filterFrom` を通す。`renderIndex` が `Filter` を取る |
| `internal/web/web_test.go` | 絞り込みのテスト |
| `internal/web/helper_test.go`（新規） | `twoNamedOwners`（web_test と browser_test が共有） |
| `internal/web/templates/index.html` | `data-tiles`、`data-home`、`.page`、`#filters`、`.filter-toggle` |
| `internal/web/static/chunk.js` | `data-tiles` からチャンクの URL を作る |
| `internal/web/static/lightbox.js` | 閉じるときに `data-home` へ戻す |
| `internal/web/static/filters.js`（新規） | シートの開け閉め |
| `internal/web/static/app.js` | `filters.js` を import |
| `internal/web/static/app.css` | サイドバー、シート、絞り込みボタン |
| `internal/web/browser_test.go` | 絞り込みのブラウザテスト。`Count` の呼び出しに `store.Filter{}` |
| `docs/web-structure.md` / `docs/design.md` | `?owner=`、`data-tiles` / `data-home`、`filters.js`、絞り込みのパネル |

---

### Task 1: store で絞り込む

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`
- 追従のみ: `internal/index/scan_test.go`、`internal/index/watch_test.go`、`internal/index/index_test.go`、`internal/web/view.go`、`internal/web/web.go`、`internal/web/browser_test.go`

**Interfaces:**
- Produces:
  - `type store.Filter struct { Owners []uint32 }`（ゼロ値は普段の一覧）
  - `func (s *Store) Count(ctx context.Context, f Filter) (int, error)`
  - `func (s *Store) DayGroups(ctx context.Context, f Filter) ([]DayGroup, error)`
  - `func (s *Store) ListRange(ctx context.Context, f Filter, offset, limit int) ([]media.Media, error)`
  - `func (s *Store) RankOf(ctx context.Context, f Filter, id string) (int, error)`（`f` に当てはまらなければ `ErrNotFound`）
  - `func (s *Store) Owners(ctx context.Context) ([]uint32, error)`（写真のある uid、重複なし、小さい順）

- [ ] **Step 1: 既存の呼び出しを新しいシグネチャに直す**

先にシグネチャだけ変える。`internal/store/store.go` の4メソッドに `f Filter` を足し（中身はまだ使わない）、`Filter` 型を置く。

```go
// Filter は一覧の絞り込み。各フィールドは1つの条件で、指定したものをすべて満たす
// 写真に絞る。ゼロ値は普段の一覧である。
//
// 呼び出し側は構造体リテラルで一度に組み立てる。条件は URL に書かれた組をまとめて
// 掛けるもので、1つずつ積んでいくものではない。
type Filter struct {
	// Owners は持ち主の uid。どれかが持ち主の写真に絞る。空なら持ち主で絞らない。
	// 「指定なし」を空で表すので、uid 0（root）と取り違えない。
	Owners []uint32
}
```

`Filter` は `DayGroup` 型の前（`Count` の前）ではなく、`ErrNotFound` と `Store` の間に置く。メソッドの引数で最初に出てくる型なので、読む人が先に目にする位置にする。

シグネチャ:

```go
func (s *Store) ListRange(ctx context.Context, f Filter, offset, limit int) ([]media.Media, error)
func (s *Store) RankOf(ctx context.Context, f Filter, id string) (int, error)
func (s *Store) Count(ctx context.Context, f Filter) (int, error)
func (s *Store) DayGroups(ctx context.Context, f Filter) ([]DayGroup, error)
```

呼び出し側を機械的に直す（リポジトリのルートで）。

```bash
sed -i \
  -e 's/\.Count(ctx)/.Count(ctx, store.Filter{})/g' \
  -e 's/\.Count(context\.Background())/.Count(context.Background(), store.Filter{})/g' \
  -e 's/\.DayGroups(ctx)/.DayGroups(ctx, store.Filter{})/g' \
  -e 's/\.DayGroups(context\.Background())/.DayGroups(context.Background(), store.Filter{})/g' \
  -e 's/\.ListRange(ctx, /.ListRange(ctx, store.Filter{}, /g' \
  -e 's/\.ListRange(context\.Background(), /.ListRange(context.Background(), store.Filter{}, /g' \
  -e 's/\.RankOf(ctx, /.RankOf(ctx, store.Filter{}, /g' \
  -e 's/\.RankOf(context\.Background(), /.RankOf(context.Background(), store.Filter{}, /g' \
  internal/store/store_test.go \
  internal/index/scan_test.go internal/index/watch_test.go internal/index/index_test.go \
  internal/web/browser_test.go
```

`internal/index/scan_test.go` と `internal/index/watch_test.go` は `store` を import していないので、import の塊に `"github.com/yendo/famifo-proto/internal/store"` を足す（既存の `internal/...` の import と並べる）。

web 側の4箇所は、Task 2 で置き換えるまでゼロ値を渡す。

- `internal/web/view.go:50` → `h.store.Count(r.Context(), store.Filter{})`
- `internal/web/view.go:54` → `h.store.DayGroups(r.Context(), store.Filter{})`
- `internal/web/view.go:73` → `h.store.ListRange(r.Context(), store.Filter{}, offset, limit)`
- `internal/web/web.go:155` → `h.store.RankOf(r.Context(), store.Filter{}, r.PathValue("id"))`

`view.go` の import に `"github.com/yendo/famifo-proto/internal/store"` を足す。

Run: `go vet ./... && go vet -tags browser ./internal/web/`
Expected: エラー無し

Run: `go test -race ./...`
Expected: `TestOpenRejectsADatabaseWhoseColumnsHaveChanged` を含めすべて PASS（まだ索引を足していない）

- [ ] **Step 2: 絞り込みのテストを書く**

`internal/store/store_test.go` の `TestDayGroupsTotalMatchesCountAndListRange` の後ろ（`TestOpenRejectsADatabaseWhoseColumnsHaveChanged` の前）に足す。

```go
// ownedAt は持ち主と撮影日時を指定して1件を作る。mediaAt と同じく mtime は撮影日時と
// ずらす。
func ownedAt(t *testing.T, path string, uid uint32, takenAt time.Time) media.Media {
	t.Helper()
	m, err := media.New(path, uid, takenAt, takenAt.Add(time.Hour))
	require.NoError(t, err)
	return m
}

// seedThreeOwners は3人分の写真を2日に分けて入れる。新しい順に
// a1(1000), b1(1001), r1(0) が 2026-02-08、a2(1000), b2(1001) が 2026-02-03。
// root の uid 0 を混ぜて、「指定なし」と取り違えないことも一緒に縛る。
func seedThreeOwners(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	for _, m := range []media.Media{
		ownedAt(t, "/items/a1.jpg", 1000, time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)),
		ownedAt(t, "/items/b1.jpg", 1001, time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC)),
		ownedAt(t, "/items/r1.jpg", 0, time.Date(2026, 2, 8, 10, 0, 0, 0, time.UTC)),
		ownedAt(t, "/items/a2.jpg", 1000, time.Date(2026, 2, 3, 12, 0, 0, 0, time.UTC)),
		ownedAt(t, "/items/b2.jpg", 1001, time.Date(2026, 2, 3, 11, 0, 0, 0, time.UTC)),
	} {
		require.NoError(t, s.Upsert(ctx, m))
	}
}

// 総枚数・日ごとの並び・チャンクの3つが同じ条件で絞られること。どれか1つでも
// 条件を落とすと、一覧の高さと中身がずれる。
func TestFilterByOwners(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		f     store.Filter
		paths []string
		days  []store.DayGroup
	}{
		{
			"no filter",
			store.Filter{},
			[]string{"/items/a1.jpg", "/items/b1.jpg", "/items/r1.jpg", "/items/a2.jpg", "/items/b2.jpg"},
			[]store.DayGroup{{Date: "2026-02-08", Count: 3}, {Date: "2026-02-03", Count: 2}},
		},
		{
			"one owner",
			store.Filter{Owners: []uint32{1000}},
			[]string{"/items/a1.jpg", "/items/a2.jpg"},
			[]store.DayGroup{{Date: "2026-02-08", Count: 1}, {Date: "2026-02-03", Count: 1}},
		},
		{
			"two owners",
			store.Filter{Owners: []uint32{1000, 1001}},
			[]string{"/items/a1.jpg", "/items/b1.jpg", "/items/a2.jpg", "/items/b2.jpg"},
			[]store.DayGroup{{Date: "2026-02-08", Count: 2}, {Date: "2026-02-03", Count: 2}},
		},
		{
			"root",
			store.Filter{Owners: []uint32{0}},
			[]string{"/items/r1.jpg"},
			[]store.DayGroup{{Date: "2026-02-08", Count: 1}},
		},
		{
			"an owner without photos",
			store.Filter{Owners: []uint32{4242}},
			nil,
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openTestStore(t)
			ctx := context.Background()
			seedThreeOwners(t, s)

			n, err := s.Count(ctx, tc.f)
			require.NoError(t, err)
			require.Equal(t, len(tc.paths), n)

			items, err := s.ListRange(ctx, tc.f, 0, 10)
			require.NoError(t, err)
			var paths []string
			for _, m := range items {
				paths = append(paths, m.Path())
			}
			require.Equal(t, tc.paths, paths)

			days, err := s.DayGroups(ctx, tc.f)
			require.NoError(t, err)
			require.Equal(t, tc.days, days)
		})
	}
}

// 絞り込んだ一覧の中でも、RankOf の位置は ListRange の並びと一致しなければならない。
// /item/{id}?owner= から開いたとき、クライアントはこの番号で飛ぶ。
func TestRankOfLocatesTheItemInAFilteredListRange(t *testing.T) {
	t.Parallel()
	for _, f := range []store.Filter{
		{Owners: []uint32{1000}},
		{Owners: []uint32{1000, 1001}},
	} {
		t.Run(fmt.Sprint(f.Owners), func(t *testing.T) {
			t.Parallel()
			s := openTestStore(t)
			ctx := context.Background()
			seedThreeOwners(t, s)

			items, err := s.ListRange(ctx, f, 0, 10)
			require.NoError(t, err)
			for want, m := range items {
				rank, err := s.RankOf(ctx, f, m.ID())
				require.NoError(t, err)
				require.Equal(t, want, rank, "the rank of %s", m.Path())
			}
		})
	}
}

// 選んでいない人の写真は、絞り込んだ一覧には無い。/item/{id} は一覧へ送り直す。
func TestRankOfOutsideTheFilterReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	seedThreeOwners(t, s)
	other := ownedAt(t, "/items/b1.jpg", 1001, time.Date(2026, 2, 8, 11, 0, 0, 0, time.UTC))

	_, err := s.RankOf(ctx, store.Filter{Owners: []uint32{1000}}, other.ID())

	require.ErrorIs(t, err, store.ErrNotFound)
}

// 撮影者の選択肢の元。写真のある uid だけを、重複なしで小さい順に返す。
func TestOwnersListsTheUIDsWithPhotos(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	seedThreeOwners(t, s)

	got, err := s.Owners(context.Background())

	require.NoError(t, err)
	require.Equal(t, []uint32{0, 1000, 1001}, got)
}

func TestOwnersEmptyStore(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)

	got, err := s.Owners(context.Background())

	require.NoError(t, err)
	require.Empty(t, got)
}
```

- [ ] **Step 3: テストが落ちることを確かめる**

Run: `go test -race -run 'TestFilterByOwners|TestRankOf|TestOwners' ./internal/store/`
Expected: コンパイルエラー `s.Owners undefined`

- [ ] **Step 4: 実装する**

`internal/store/store.go` の `schema` に索引を2つ足す。

```go
const schema = `
CREATE TABLE IF NOT EXISTS media (
    id             TEXT PRIMARY KEY,
    path           TEXT NOT NULL UNIQUE,
    owner_uid      INTEGER NOT NULL,
    taken_at_local TEXT NOT NULL,
    mod_time       INTEGER NOT NULL,
    day            TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_order ON media(taken_at_local DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_media_day ON media(day);
CREATE INDEX IF NOT EXISTS idx_media_owner_order ON media(owner_uid, taken_at_local DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_media_owner_day ON media(owner_uid, day);
`
```

`schema` の直前にコメントを足す。

```go
// idx_media_owner_order と idx_media_owner_day は、1人に絞った一覧のための索引。
// 1人なら RankOf も索引だけで数えられる。2人以上を選ぶと並び順は idx_media_order
// から読み、uid で振り分ける。
```

`ListRange`:

```go
func (s *Store) ListRange(ctx context.Context, f Filter, offset, limit int) ([]media.Media, error) {
	if offset < 0 {
		return nil, fmt.Errorf("offset must be 0 or greater: %d", offset)
	}
	if limit < 0 {
		return nil, fmt.Errorf("limit must be 0 or greater: %d", limit)
	}

	where, args := f.where()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM media
		 WHERE `+where+`
		 ORDER BY taken_at_local DESC, id DESC
		 LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	// 以下は今のまま
```

`RankOf`（doc コメントの1段落目の後ろに「f に当てはまらない写真も ErrNotFound である。」を足す）:

```go
func (s *Store) RankOf(ctx context.Context, f Filter, id string) (int, error) {
	where, args := f.where()
	var takenAtLocal string
	err := s.db.QueryRowContext(ctx,
		`SELECT taken_at_local FROM media WHERE id = ? AND (`+where+`)`,
		append([]any{id}, args...)...).Scan(&takenAtLocal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("cannot get the media: %w", err)
	}

	// 自分より前に並ぶ行を数える。OFFSET で数えるのと違い、途中の行を読まずに
	// 索引だけで答えが出る。
	var rank int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media
		 WHERE (`+where+`) AND (taken_at_local > ? OR (taken_at_local = ? AND id > ?))`,
		append(args, takenAtLocal, takenAtLocal, id)...).Scan(&rank)
	if err != nil {
		return 0, fmt.Errorf("cannot count the media before it: %w", err)
	}
	return rank, nil
}
```

`Count`:

```go
// Count は f に当てはまる件数を返す。
func (s *Store) Count(ctx context.Context, f Filter) (int, error) {
	where, args := f.where()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("cannot count the media: %w", err)
	}
	return n, nil
}
```

`DayGroups`（doc コメントの「day に索引があるので」の段落に「1人に絞ったときは idx_media_owner_day を同じように読む。」を足す）:

```go
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, COUNT(*) FROM media WHERE `+where+` GROUP BY day ORDER BY day DESC`, args...)
```

`DayGroups` の後ろ（`selectCols` の前）に `Owners` を置く。

```go
// Owners は写真のある持ち主の uid を、重複なしで小さい順に返す。撮影者の選択肢の
// 元にする。名前を引くのは配信側である。
func (s *Store) Owners(ctx context.Context) ([]uint32, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT owner_uid FROM media ORDER BY owner_uid`)
	if err != nil {
		return nil, fmt.Errorf("cannot list the owners: %w", err)
	}
	defer rows.Close()

	var out []uint32
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("cannot read the owners: %w", err)
		}
		out = append(out, uint32(uid))
	}
	return out, rows.Err()
}
```

ファイルの末尾（`scanMedia` の後ろ）に `where` を置く。

```go
// where は f の条件を WHERE 句の中身とその引数にする。条件が無ければ常に真の "1" を
// 返す。条件はすべてここで組み立て、4つの読み取りがずれないようにする。
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	if len(f.Owners) > 0 {
		conds = append(conds, "owner_uid IN (?"+strings.Repeat(", ?", len(f.Owners)-1)+")")
		for _, uid := range f.Owners {
			args = append(args, int64(uid))
		}
	}
	if len(conds) == 0 {
		return "1", nil
	}
	return strings.Join(conds, " AND "), args
}
```

- [ ] **Step 5: 古いDBのテストを直す**

索引が `owner_uid` を参照するので、`owner_uid` の無い古い DB では `CREATE INDEX` が先に落ち、エラーが "cannot create the schema" になる。`TestOpenRejectsADatabaseWhoseColumnsHaveChanged` は "cannot read the database" を見ているので、古い DB の表に `owner_uid` を足し、テストの狙い（列の名前が変わった DB を読み取りで弾く）を保つ。

`internal/store/store_test.go` の当該テストの `CREATE TABLE` を次にする。コメントの「索引の名前と day は今と同じなので」を「索引の名前と day・owner_uid は今と同じなので」に直す。

```go
	_, err = db.Exec(`
CREATE TABLE media (
    id        TEXT PRIMARY KEY,
    path      TEXT NOT NULL UNIQUE,
    owner_uid INTEGER NOT NULL,
    shot_at   TEXT NOT NULL,
    mod_time  INTEGER NOT NULL,
    day       TEXT NOT NULL
);
CREATE INDEX idx_media_order ON media(shot_at DESC, id DESC);`)
```

`TestOpenRejectsADatabaseThatStoresTheCaptureTimeAsAnInstant` は `require.Error` だけを見るので、そのままで通る。

- [ ] **Step 6: テストが通ることを確かめる**

Run: `go test -race ./internal/store/ ./internal/index/ ./internal/web/`
Expected: PASS

Run: `go vet ./... && go vet -tags browser ./internal/web/`
Expected: エラー無し

- [ ] **Step 7: コミット**

```bash
git add internal/store/ internal/index/scan_test.go internal/index/watch_test.go internal/index/index_test.go internal/web/view.go internal/web/web.go internal/web/browser_test.go
git commit -m "feat: Filter the media list by owner in the store"
```

---

### Task 2: web が `?owner=` で絞り込み、URL に条件を引き継ぐ

**Files:**
- Create: `internal/web/filter.go`
- Modify: `internal/web/view.go`、`internal/web/web.go`、`internal/web/templates/index.html`
- Test: `internal/web/web_test.go`

**Interfaces:**
- Consumes: Task 1 の `store.Filter`、`Count`・`DayGroups`・`ListRange`・`RankOf`
- Produces（パッケージ内）:
  - `func filterFrom(r *http.Request) (store.Filter, error)`（重複を落として小さい順）
  - `func filterURL(path string, f store.Filter) string`
  - `func toggleOwner(f store.Filter, uid uint32) store.Filter`
  - `indexView.TilesURL`、`indexView.HomeURL`（テンプレートの `data-tiles`、`data-home`）
  - `/tiles` の `pageUrl` は `filterURL("/item/"+id, f)`

- [ ] **Step 1: テストを書く**

`internal/web/web_test.go` の `TestGalleryOpensNoItem` の後ろ（`doGet` の前）に足す。`addOwnedBy` は撮影日時が同じ行を作る。並びは id の降順になるので、順序を見るテストでは `getTiles` の結果から ID を読む。

```go
// ?owner= で絞ると、件数・日ごとの並び・チャンクが選んだ人の分になる。
func TestGalleryFiltersByOwner(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	f.addOwnedBy(t, "a1.jpg", 1000)
	f.addOwnedBy(t, "a2.jpg", 1000)
	f.addOwnedBy(t, "b1.jpg", 1001)
	f.addOwnedBy(t, "c1.jpg", 1002)

	body := doGet(t, f.h, "/?owner=1000&owner=1001").Body.String()

	require.Contains(t, body, `data-total="3"`)
	got := embeddedDayGroups(t, body)
	require.Len(t, got, 1)
	require.Equal(t, 3, got[0].Count)
}

func TestTilesFilterByOwner(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	a := f.addOwnedBy(t, "a.jpg", 1000)
	f.addOwnedBy(t, "b.jpg", 1001)

	got := getTiles(t, f.h, "/tiles?chunk=0&owner=1000")

	require.Equal(t, []string{a.ID()}, tileIDs(got))
}

// タイルのリンク先は絞り込みを引き継ぐ。個別表示で前後に送っても、閉じても、
// 選んだ人の写真の中に留まる。値は重複を落として小さい順に書くので、同じ条件の
// 組は同じ URL になる。
func TestTilesCarryTheFilterInThePageURL(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	a := f.addOwnedBy(t, "a.jpg", 1000)

	got := getTiles(t, f.h, "/tiles?owner=1001&owner=1000&owner=1000")

	require.Len(t, got, 1)
	require.Equal(t, "/item/"+a.ID()+"?owner=1000&owner=1001", got[0].PageURL)
}

// クライアントがチャンクを取る URL と、個別表示を閉じて戻る URL はサーバーが
// 絞り込み込みで埋める。JS に絞り込みのクエリを組み立てさせない。
func TestGalleryShipsTheFilteredURLs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		target, tiles, home string
	}{
		{"/", `data-tiles="/tiles"`, `data-home="/"`},
		{"/?owner=1000", `data-tiles="/tiles?owner=1000"`, `data-home="/?owner=1000"`},
		{"/?owner=1001&owner=1000", `data-tiles="/tiles?owner=1000&amp;owner=1001"`, `data-home="/?owner=1000&amp;owner=1001"`},
	} {
		t.Run(tc.target, func(t *testing.T) {
			t.Parallel()
			f := newWebFixture(t, 10)

			body := doGet(t, f.h, tc.target).Body.String()

			require.Contains(t, body, tc.tiles)
			require.Contains(t, body, tc.home)
		})
	}
}

func TestItemOpensTheFilteredGalleryAtThatItem(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	a := f.addOwnedBy(t, "a.jpg", 1000)
	f.addOwnedBy(t, "b.jpg", 1001)

	rec := doGet(t, f.h, "/item/"+a.ID()+"?owner=1000")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `data-open="0"`)
	require.Contains(t, body, `data-total="1"`)
}

// 選んでいない人の写真は絞り込んだ一覧に無いので、その一覧へ送る。
func TestItemOutsideTheFilterRedirectsToTheFilteredGallery(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	b := f.addOwnedBy(t, "b.jpg", 1001)

	rec := doGet(t, f.h, "/item/"+b.ID()+"?owner=1000")

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/?owner=1000", rec.Header().Get("Location"))
}

func TestFilterRejectsABadOwner(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addOwnedBy(t, "a.jpg", 1000)
	for _, target := range []string{
		"/?owner=abc",
		"/?owner=1000&owner=-1",
		"/?owner=4294967296", // uint32 に収まらない
		"/tiles?owner=abc",
		"/item/" + m.ID() + "?owner=abc",
	} {
		t.Run(target, func(t *testing.T) {
			require.Equal(t, http.StatusBadRequest, doGet(t, f.h, target).Code)
		})
	}
}
```

- [ ] **Step 2: テストが落ちることを確かめる**

Run: `go test -race -run 'TestGalleryFiltersByOwner|TestTilesFilterByOwner|TestTilesCarryTheFilterInThePageURL|TestGalleryShipsTheFilteredURLs|TestItemOpensTheFilteredGalleryAtThatItem|TestItemOutsideTheFilter|TestFilterRejectsABadOwner' ./internal/web/`
Expected: FAIL（`data-total="4"`、`data-tiles` が無い、400 にならない など）

- [ ] **Step 3: `filter.go` を書く**

```go
package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/yendo/famifo-proto/internal/store"
)

// filterFrom は要求のクエリから一覧の絞り込みを読む。?owner= は何度でも書け、
// 選んだ誰かが撮った写真に絞る。どれか1つでも uid として読めなければエラーにする。
//
// 値は重複を落として小さい順に並べる。filterURL はこの並びのまま書くので、同じ
// 条件の組は同じ URL になる。
func filterFrom(r *http.Request) (store.Filter, error) {
	var f store.Filter
	for _, raw := range r.URL.Query()["owner"] {
		uid, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return store.Filter{}, fmt.Errorf("invalid owner: %q", raw)
		}
		f.Owners = append(f.Owners, uint32(uid))
	}
	slices.Sort(f.Owners)
	f.Owners = slices.Compact(f.Owners)
	return f, nil
}

// filterURL は path に f の条件をクエリとして付けた URL を返す。条件が無ければ
// path のまま。絞り込みを引き継ぐ URL は、すべてここで作る。JS は条件の組み立て方を
// 知らない。
func filterURL(path string, f store.Filter) string {
	if len(f.Owners) == 0 {
		return path
	}
	q := url.Values{}
	for _, uid := range f.Owners {
		q.Add("owner", strconv.FormatUint(uint64(uid), 10))
	}
	return path + "?" + q.Encode()
}
```

`toggleOwner` は Task 4 で足す（ここではまだ使わない）。

- [ ] **Step 4: ハンドラと表示を直す**

`internal/web/web.go`:

```go
// handleIndex はギャラリーのトップページを返す。
func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	f, err := filterFrom(r)
	if err != nil {
		http.Error(w, "bad filter", http.StatusBadRequest)
		return
	}
	h.renderIndex(w, r, f, noOpenItem)
}
```

`handleItem` の doc コメントの最後の段落を「消えた写真や、絞り込みに当てはまらない写真のURLを共有されることは普通に起きる。404にすると行き止まりになるので、絞り込んだままのギャラリーへ送る。」にする。

```go
func (h *Handler) handleItem(w http.ResponseWriter, r *http.Request) {
	f, err := filterFrom(r)
	if err != nil {
		http.Error(w, "bad filter", http.StatusBadRequest)
		return
	}
	rank, err := h.store.RankOf(r.Context(), f, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Redirect(w, r, filterURL("/", f), http.StatusFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.renderIndex(w, r, f, rank)
}
```

`handleTiles`（`parseChunk` の後ろ）:

```go
	f, err := filterFrom(r)
	if err != nil {
		http.Error(w, "bad filter", http.StatusBadRequest)
		return
	}
	tiles, err := h.listMedia(r, f, chunk*h.chunkSize, h.chunkSize)
```

`renderIndex` は `f store.Filter` を受け取り、`h.buildIndexView(r, f, openIndex)` を呼ぶ。doc コメントに「f は一覧の絞り込みで、件数・日ごとの並び・URL のすべてに掛ける。」を足す。

```go
func (h *Handler) renderIndex(w http.ResponseWriter, r *http.Request, f store.Filter, openIndex int) {
	view, err := h.buildIndexView(r, f, openIndex)
```

`internal/web/view.go` の `indexView` に足す（`ChunkSize` の後ろ）:

```go
	// TilesURL はチャンクを取る /tiles の URL。絞り込みを含む。chunk.js は
	// チャンクの番号だけを足す。
	TilesURL string
	// HomeURL は個別表示を閉じたときに戻る一覧の URL。絞り込みを含む。
	HomeURL string
```

`buildIndexView` と `listMedia`:

```go
// buildIndexView はトップページの入力を組み立てる。f の条件で絞る。
func (h *Handler) buildIndexView(r *http.Request, f store.Filter, openIndex int) (indexView, error) {
	total, err := h.store.Count(r.Context(), f)
	if err != nil {
		return indexView{}, err
	}
	days, err := h.store.DayGroups(r.Context(), f)
	// ...（dayGroups の詰め替えは今のまま）
	return indexView{
		Total: total, ChunkSize: h.chunkSize,
		TilesURL: filterURL("/tiles", f), HomeURL: filterURL("/", f),
		DayGroups: dayGroups, OpenIndex: openIndex,
		AuthEnabled: h.auth != nil,
	}, nil
}

// listMedia はオフセット指定で1チャンク分を組み立てる。0件でも nil ではなく
// 空のスライスを返すので、JSONは null ではなく [] になる。
//
// 写真のページの URL は絞り込みを引き継ぐ。個別表示で送っても閉じても、
// 選んだ人の写真の中に留まる。
func (h *Handler) listMedia(r *http.Request, f store.Filter, offset, limit int) ([]mediaView, error) {
	items, err := h.store.ListRange(r.Context(), f, offset, limit)
	// ...
			PageURL:  filterURL("/item/"+m.ID(), f),
```

`mediaView.PageURL` のコメントを「その1件だけを開くURL。絞り込みを引き継ぐ。タイルのリンク先」にする。

`internal/web/templates/index.html` の `#gallery` に属性を足す。

```html
<main id="gallery" class="gallery" data-total="{{.Total}}" data-chunk="{{.ChunkSize}}" data-tiles="{{.TilesURL}}" data-home="{{.HomeURL}}" data-open="{{.OpenIndex}}">
```

- [ ] **Step 5: テストが通ることを確かめる**

Run: `go test -race ./internal/web/`
Expected: PASS（既存の `TestTilesDescribeEachItem` の `PageURL` は絞り込み無しなので `/item/<id>` のまま）

Run: `go vet ./... && go vet -tags browser ./internal/web/`
Expected: エラー無し

- [ ] **Step 6: コミット**

```bash
git add internal/web/filter.go internal/web/view.go internal/web/web.go internal/web/templates/index.html internal/web/web_test.go
git commit -m "feat: Filter the gallery by owner through the query"
```

---

### Task 3: JS がサーバーから受け取った URL で動く

**Files:**
- Modify: `internal/web/static/chunk.js:46-50,138`、`internal/web/static/lightbox.js:18-21,282`
- Test: `internal/web/browser_test.go`

**Interfaces:**
- Consumes: Task 2 の `#gallery` の `data-tiles`、`data-home`、絞り込みを含む `pageUrl`

- [ ] **Step 1: ブラウザテストを書く**

`internal/web/browser_test.go` の `TestLightboxBackButtonCloses` の後ろに足す。uid 1000 と 1001 の写真を交互に並べ、絞り込んだ一覧から送ると間の人の写真を飛ばすことを見る。名前は要らないので、`/etc/passwd` に無い uid でよい。

```go
// 絞り込んだ一覧のチャンクと送り先は、選んだ人の写真だけである。新しい順に
// a1(1000), b1(1001), a2(1000) と並べる。1000 で絞ると a1 の次は a2 になる。
func TestLightboxStaysInsideTheFilter(t *testing.T) {
	requireBrowser(t)

	url, ids := startLightboxServer(t,
		lightboxItem{"a1.jpg", 1000, time.Unix(1600000200, 0)},
		lightboxItem{"b1.jpg", 1001, time.Unix(1600000100, 0)},
		lightboxItem{"a2.jpg", 1000, time.Unix(1600000000, 0)},
	)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	var tiles int
	var src string
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(url+"/?owner=1000"),
		waitForGallery(10*time.Second),
		chromedp.Evaluate(`document.querySelectorAll('#window .tile').length`, &tiles),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.KeyEvent(kb.ArrowRight),
		chromedp.Poll(fmt.Sprintf(`location.pathname === %s`, strconv.Quote("/item/"+ids[2])), nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`document.querySelector('#lightbox img').getAttribute('src')`, &src),
	)
	require.NoError(t, err, "the lightbox did not move to the next photo of the owner")
	require.Equal(t, 2, tiles, "the gallery shows only the photos of the owner")
	require.Equal(t, "/full/"+ids[2], src)

	var search string
	err = chromedp.Run(rctx, chromedp.Evaluate(`location.search`, &search))
	require.NoError(t, err)
	require.Equal(t, "?owner=1000", search, "the item URL keeps the filter")
}

// 写真の URL を直接開いたときは戻る先の履歴が無いので、閉じると URL を一覧に
// 書き直す。そのとき絞り込みを落とさない。
func TestClosingADirectlyOpenedItemKeepsTheFilter(t *testing.T) {
	requireBrowser(t)

	url, ids := startLightboxServer(t,
		lightboxItem{"a1.jpg", 1000, time.Unix(1600000200, 0)},
		lightboxItem{"b1.jpg", 1001, time.Unix(1600000100, 0)},
	)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	var here string
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(url+"/item/"+ids[0]+"?owner=1000"),
		waitForGallery(10*time.Second),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Click(`#lightbox .lb-back`, chromedp.NodeVisible),
		chromedp.Poll(`document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`location.pathname + location.search`, &here),
	)
	require.NoError(t, err, "the back button did not close the lightbox")
	require.Equal(t, "/?owner=1000", here)
}
```

- [ ] **Step 2: テストが落ちることを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestLightboxStaysInsideTheFilter|TestClosingADirectlyOpenedItemKeepsTheFilter' ./internal/web/`
Expected: FAIL。chunk.js が `/tiles?chunk=0` を絞り込み無しで取るので、並ぶのは a1 と b1 になり、送った先が b1 になって `location.pathname` の Poll がタイムアウトする。2つ目は `here` が `/` になる

- [ ] **Step 3: chunk.js を直す**

`chunkSize` を読む行の後ろに足す。

```js
	// /tiles の URL はサーバーが絞り込み込みで埋める。ここではチャンクの番号だけを
	// 足す。絞り込みのクエリを JS で組み立てると、URL の規則が Go と2箇所に分かれる。
	const tilesUrl = root.dataset.tiles ?? "/tiles";
```

`request` の fetch を次にする。

```js
		const job = (async () => {
			const url = new URL(tilesUrl, location.href);
			url.searchParams.set("chunk", String(ci));
			const res = await fetch(url, {
				signal: controller.signal,
			});
```

- [ ] **Step 4: lightbox.js を直す**

`box` を取る行の後ろ（`if (!box || ...)` の前）に足す。

```js
	/** @type {HTMLElement | null} */
	const root = document.querySelector("#gallery");
	// 閉じたときに戻る一覧の URL。サーバーが絞り込み込みで埋める。
	const home = root?.dataset.home ?? "/";
```

`requestClose` の `history.replaceState(null, "", "/");` を `history.replaceState(null, "", home);` にし、関数のコメントの「URLをギャラリーに直してから」を「URLを絞り込んだままのギャラリーに直してから」にする。

- [ ] **Step 5: テストが通ることを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestLightbox|TestClosingADirectlyOpenedItemKeepsTheFilter|TestItemURLOpensTheLightbox|TestBackClosesTheLightbox|TestArrowKeys' ./internal/web/`
Expected: PASS

Run: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`
Expected: エラー無し

- [ ] **Step 6: コミット**

```bash
git add internal/web/static/chunk.js internal/web/static/lightbox.js internal/web/browser_test.go
git commit -m "feat: Keep the owner filter in chunk fetches and when closing the item view"
```

---

### Task 4: 絞り込みのパネル（PC はサイドバー、スマホはシート）

**Files:**
- Modify: `internal/web/filter.go`、`internal/web/view.go`、`internal/web/templates/index.html`、`internal/web/static/app.css`、`internal/web/static/app.js`
- Create: `internal/web/static/filters.js`、`internal/web/helper_test.go`
- Test: `internal/web/web_test.go`、`internal/web/browser_test.go`

**Interfaces:**
- Consumes: Task 1 の `store.Owners`、Task 2 の `filterFrom`・`filterURL`
- Produces（パッケージ内）:
  - `func toggleOwner(f store.Filter, uid uint32) store.Filter`
  - `type ownerChoice struct { Name, URL string; Selected bool }`
  - `func (h *Handler) ownerChoices(r *http.Request, f store.Filter) ([]ownerChoice, error)`
  - `indexView.Filtered`、`indexView.Owners`、`indexView.EveryOwner`、`indexView.EveryOwnerURL`
- テストヘルパー: `func twoNamedOwners(t *testing.T) (me, root uint32)`（`helper_test.go`）

- [ ] **Step 1: 共有のテストヘルパーを置く**

`internal/web/helper_test.go`:

```go
package web_test

import (
	"os"
	"testing"

	"github.com/yendo/famifo-proto/internal/owner"
)

// twoNamedOwners は名前の引ける uid を2つ返す。テストを走らせている自分と root で
// ある。撮影者の節には名前の引ける人しか並ばないので、2人を並べるテストに使う。
// 自分が root のときや、どちらかに名前が無い環境では skip する。
func twoNamedOwners(t *testing.T) (me, root uint32) {
	t.Helper()
	me = uint32(os.Getuid())
	if me == 0 {
		t.Skip("running as root, so there is only one named owner")
	}
	if owner.Name(me) == "" || owner.Name(0) == "" {
		t.Skip("the test user or root has no account entry, so there are no names to list")
	}
	return me, 0
}
```

- [ ] **Step 2: web のテストを書く**

`internal/web/web_test.go` の Task 2 で足したテストの後ろに足す。import に `"html/template"` を足す（`template.HTMLEscapeString` を使う）。

```go
// ownerLink はパネルの撮影者の項目のマークアップ。テンプレートと同じ形で書く。
func ownerLink(url, name string, selected bool) string {
	class := ""
	if selected {
		class = ` class="selected"`
	}
	return `<a href="` + template.HTMLEscapeString(url) + `"` + class + `>` + template.HTMLEscapeString(name) + `</a>`
}

// 撮影者の節には名前の引ける人だけが名前順に並ぶ。各項目はその人を条件に
// 入れる（選んでいれば外す）URL へのリンクで、選んでいる人に印が付く。
func TestPanelListsTheNamedOwners(t *testing.T) {
	t.Parallel()
	me, root := twoNamedOwners(t)
	f := newWebFixture(t, 10)
	f.addOwnedBy(t, "mine.jpg", me)
	f.addOwnedBy(t, "roots.jpg", root)
	f.addOwnedBy(t, "nobodys.jpg", 65534)

	meQ := fmt.Sprint(me)
	body := doGet(t, f.h, "/?owner="+meQ).Body.String()

	// 選んでいる自分を押すと外れて全員、root を押すと2人になる。
	mine := ownerLink("/", owner.Name(me), true)
	roots := ownerLink("/?owner=0&owner="+meQ, owner.Name(root), false)
	require.Contains(t, body, mine)
	require.Contains(t, body, roots)
	first, second := mine, roots
	if owner.Name(root) < owner.Name(me) {
		first, second = roots, mine
	}
	require.Less(t, strings.Index(body, first), strings.Index(body, second), "ordered by name")
	// テンプレートは各項目を <li><a href= で始める（Step 5）。
	require.Equal(t, 3, strings.Count(body, `<li><a href=`), "everyone and the two named owners; nobody is not listed")
	require.Contains(t, body, ownerLink("/", "全員", false))
}

// 誰も選んでいなければ「全員」に印が付く。
func TestPanelMarksEveryoneWithoutAFilter(t *testing.T) {
	t.Parallel()
	me, _ := twoNamedOwners(t)
	f := newWebFixture(t, 10)
	f.addOwnedBy(t, "mine.jpg", me)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, ownerLink("/", "全員", true))
	require.Contains(t, body, ownerLink("/?owner="+fmt.Sprint(me), owner.Name(me), false))
	require.NotContains(t, body, `filter-toggle active`)
}

// 条件が掛かっていれば絞り込みボタンが目立つ色になる。
func TestPanelToggleShowsTheFilterIsOn(t *testing.T) {
	t.Parallel()
	me, _ := twoNamedOwners(t)
	f := newWebFixture(t, 10)
	f.addOwnedBy(t, "mine.jpg", me)

	body := doGet(t, f.h, "/?owner="+fmt.Sprint(me)).Body.String()

	require.Contains(t, body, `filter-toggle active`)
}

// 名前の引ける撮影者が1人もいなければ（/etc/passwd をマウントしていないなど）、
// 節が無いのでパネルもボタンも出さない。
func TestPanelIsAbsentWithoutNamedOwners(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	f.addOwnedBy(t, "nobodys.jpg", 65534)

	body := doGet(t, f.h, "/").Body.String()

	require.NotContains(t, body, `id="filters"`)
	require.NotContains(t, body, `filter-toggle`)
}
```

- [ ] **Step 3: テストが落ちることを確かめる**

Run: `go test -race -run 'TestPanel' ./internal/web/`
Expected: FAIL（`id="filters"` も項目も無い）。名前の無い環境では SKIP になるが、`TestPanelIsAbsentWithoutNamedOwners` は PASS する（まだパネルが無いので）

- [ ] **Step 4: 撮影者の節の組み立てを書く**

`internal/web/filter.go` の `filterURL` の後ろに足す。import に `"strings"` と `"github.com/yendo/famifo-proto/internal/owner"` を足す。

```go
// ownerChoice はパネルの撮影者の節の1項目。
type ownerChoice struct {
	Name     string
	URL      string // この人を条件に入れた（選んでいれば外した）一覧の URL
	Selected bool
}

// ownerChoices は撮影者の節の項目を名前順に返す。写真のある uid のうち、名前の
// 引ける人だけを並べる。名前の無い人の写真は「全員」でだけ見える。
func (h *Handler) ownerChoices(r *http.Request, f store.Filter) ([]ownerChoice, error) {
	uids, err := h.store.Owners(r.Context())
	if err != nil {
		return nil, err
	}
	var out []ownerChoice
	for _, uid := range uids {
		name := owner.Name(uid)
		if name == "" {
			continue
		}
		_, selected := slices.BinarySearch(f.Owners, uid)
		out = append(out, ownerChoice{
			Name:     name,
			URL:      filterURL("/", toggleOwner(f, uid)),
			Selected: selected,
		})
	}
	// 同じ名前の人がいても順が揺れないよう、uid の順を保ったまま並べ替える。
	slices.SortStableFunc(out, func(a, b ownerChoice) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// toggleOwner は f の撮影者の条件で uid を入れ替えたものを返す。選んでいれば外し、
// 選んでいなければ入れる。並びは小さい順のまま保つ。f は変えない。
func toggleOwner(f store.Filter, uid uint32) store.Filter {
	owners := slices.Clone(f.Owners)
	if i, found := slices.BinarySearch(owners, uid); found {
		owners = slices.Delete(owners, i, i+1)
	} else {
		owners = slices.Insert(owners, i, uid)
	}
	f.Owners = owners
	return f
}
```

`internal/web/view.go` の `indexView` に足す（`AuthEnabled` の前）。

```go
	// Filtered は条件が1つでも掛かっているか。狭い画面の絞り込みボタンの色に使う。
	Filtered bool
	// Owners はパネルの撮影者の節の項目。名前順。空なら節もパネルも出さない。
	Owners []ownerChoice
	// EveryOwner は撮影者で絞っていないか。「全員」に印を付ける。
	EveryOwner bool
	// EveryOwnerURL は「全員」の URL。撮影者の条件だけを空にした一覧である。
	EveryOwnerURL string
```

`buildIndexView` で組み立てる（`days` を詰め替えた後ろ）。

```go
	owners, err := h.ownerChoices(r, f)
	if err != nil {
		return indexView{}, err
	}
	everyOwner := f
	everyOwner.Owners = nil
```

```go
	return indexView{
		Total: total, ChunkSize: h.chunkSize,
		TilesURL: filterURL("/tiles", f), HomeURL: filterURL("/", f),
		DayGroups: dayGroups, OpenIndex: openIndex,
		Filtered: len(f.Owners) > 0,
		Owners: owners, EveryOwner: len(f.Owners) == 0, EveryOwnerURL: filterURL("/", everyOwner),
		AuthEnabled: h.auth != nil,
	}, nil
```

- [ ] **Step 5: テンプレートを書く**

`internal/web/templates/index.html` の `<header class="topbar">` を次にする。

```html
<header class="topbar">
  <h1>famifo</h1>
  <span class="count">{{.Total}} 件</span>
  {{if .AuthEnabled}}
  <form class="logout" method="post" action="/logout">
    <button type="submit">Log out</button>
  </form>
  {{end}}
  {{/* 狭い画面でだけ出る。押すと #filters をシートとして開け閉めする
       （filters.js）。条件が掛かっている間は active で目立たせる。 */}}
  {{if .Owners}}
  <button class="filter-toggle{{if .Filtered}} active{{end}}" type="button" aria-controls="filters" aria-expanded="false">絞り込み</button>
  {{end}}
</header>
```

`<main id="gallery" ...>` から `</main>` までを `.page` で包み、その先頭にパネルを置く。

```html
<div class="page">
  {{/* 絞り込みのパネル。広い画面では左のサイドバー、狭い画面では下から出る
       シートになる（app.css）。項目はどれもサーバーが作った URL へのリンクで、
       選ぶとページごと読み直す。節は条件ごとに並べる。 */}}
  {{if .Owners}}
  <nav id="filters" class="filters" aria-label="絞り込み">
    <section class="filter-section">
      <h2>撮影者</h2>
      <ul>
        <li><a href="{{.EveryOwnerURL}}"{{if .EveryOwner}} class="selected"{{end}}>全員</a></li>
        {{range .Owners}}<li><a href="{{.URL}}"{{if .Selected}} class="selected"{{end}}>{{.Name}}</a></li>
        {{end}}
      </ul>
    </section>
  </nav>
  {{end}}
  <main id="gallery" ...>（今のまま）</main>
</div>
```

- [ ] **Step 6: web のテストが通ることを確かめる**

Run: `go test -race ./internal/web/`
Expected: PASS（名前の無い環境では `TestPanelListsTheNamedOwners` などが SKIP）

- [ ] **Step 7: ブラウザテストを書く**

`internal/web/browser_test.go` の Task 3 で足したテストの後ろに足す。

```go
// filterPanelJS はパネルとボタンが表示されているかを読む。
const filterPanelJS = `(() => {
	const shown = (el) => !!el && getComputedStyle(el).display !== 'none';
	const btn = document.querySelector('.filter-toggle');
	return {
		panel: shown(document.querySelector('#filters')),
		toggle: shown(btn),
		active: !!btn && btn.classList.contains('active'),
	};
})()`

type filterPanelState struct {
	Panel  bool `json:"panel"`
	Toggle bool `json:"toggle"`
	Active bool `json:"active"`
}

// clickOwnerJS は撮影者の節で name の項目を押す JS 式を返す。
func clickOwnerJS(name string) string {
	return fmt.Sprintf(`[...document.querySelectorAll('#filters a')].find(a => a.textContent === %s).click()`,
		strconv.Quote(name))
}

// 広い画面ではパネルが左に常に出ていて、ボタンは出ない。項目を押すとその人の
// 写真だけの一覧になる。
func TestFilterSidebarOnWideScreens(t *testing.T) {
	requireBrowser(t)
	me, root := twoNamedOwners(t)

	url, _ := startLightboxServer(t,
		lightboxItem{"mine1.jpg", me, time.Unix(1600000300, 0)},
		lightboxItem{"roots.jpg", root, time.Unix(1600000200, 0)},
		lightboxItem{"mine2.jpg", me, time.Unix(1600000100, 0)},
	)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	var before, after filterPanelState
	var tiles int
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1400, 900),
		chromedp.Navigate(url),
		waitForGallery(10*time.Second),
		chromedp.Evaluate(filterPanelJS, &before),
		chromedp.Evaluate(clickOwnerJS(owner.Name(root)), nil),
		chromedp.Poll(`location.search === "?owner=0"`, nil, chromedp.WithPollingTimeout(5*time.Second)),
		waitForGallery(10*time.Second),
		chromedp.Evaluate(`document.querySelectorAll('#window .tile').length`, &tiles),
		chromedp.Evaluate(filterPanelJS, &after),
	)
	require.NoError(t, err)
	require.True(t, before.Panel, "the sidebar is always shown on wide screens")
	require.False(t, before.Toggle, "the toggle is only for narrow screens")
	require.Equal(t, 1, tiles, "only the photo of root is listed")
	require.True(t, after.Panel)
}

// 狭い画面ではパネルは隠れていて、ボタンで開く。項目を押すとその人の一覧になり、
// 絞り込み中はボタンが目立つ色になる。
func TestFilterSheetOnNarrowScreens(t *testing.T) {
	requireBrowser(t)
	me, root := twoNamedOwners(t)

	url, _ := startLightboxServer(t,
		lightboxItem{"mine.jpg", me, time.Unix(1600000200, 0)},
		lightboxItem{"roots.jpg", root, time.Unix(1600000100, 0)},
	)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	var closed, opened, filtered filterPanelState
	var tiles int
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(400, 800),
		chromedp.Navigate(url),
		waitForGallery(10*time.Second),
		chromedp.Evaluate(filterPanelJS, &closed),
		chromedp.Click(`.filter-toggle`, chromedp.NodeVisible),
		chromedp.Evaluate(filterPanelJS, &opened),
		chromedp.Evaluate(clickOwnerJS(owner.Name(me)), nil),
		chromedp.Poll(fmt.Sprintf(`location.search === %s`, strconv.Quote("?owner="+fmt.Sprint(me))), nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		waitForGallery(10*time.Second),
		chromedp.Evaluate(`document.querySelectorAll('#window .tile').length`, &tiles),
		chromedp.Evaluate(filterPanelJS, &filtered),
	)
	require.NoError(t, err)
	require.False(t, closed.Panel, "the sheet starts closed")
	require.True(t, closed.Toggle)
	require.False(t, closed.Active, "nothing is filtered yet")
	require.True(t, opened.Panel, "the toggle opens the sheet")
	require.Equal(t, 1, tiles)
	require.False(t, filtered.Panel, "the new page starts with the sheet closed")
	require.True(t, filtered.Active, "the toggle shows that a filter is on")
}
```

import に `"github.com/yendo/famifo-proto/internal/owner"` を足す。

- [ ] **Step 8: ブラウザテストが落ちることを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestFilterSidebarOnWideScreens|TestFilterSheetOnNarrowScreens' ./internal/web/`
Expected: FAIL（CSS が無いのでパネルが狭い画面でも出ている、ボタンを押しても何も起きない など）

- [ ] **Step 9: filters.js を書いて読み込む**

`internal/web/static/filters.js`:

```js
// @ts-check

// 絞り込みのパネルを、狭い画面でシートとして開け閉めする。広い画面ではボタンが
// 出ず、パネルは CSS がサイドバーとして常に出す。項目はただのリンクなので、
// 選んだ後の処理はここには無い。

(() => {
	/** @type {HTMLElement | null} */
	const btn = document.querySelector(".filter-toggle");
	/** @type {HTMLElement | null} */
	const panel = document.querySelector("#filters");
	if (!btn || !panel) return;

	btn.addEventListener("click", () => {
		const open = panel.classList.toggle("open");
		btn.setAttribute("aria-expanded", String(open));
	});
})();
```

`internal/web/static/app.js` に足し、コメントを直す。

```js
// 入口。index.html が読むのはこのファイルだけで、各機能は import した
// 時点で動き出す。ライトボックスとスクラバーも gallery.js を import するが、
// それに頼らずギャラリーの本体であることをここで明示する。
import "./gallery.js";
import "./lightbox.js";
import "./scrubber.js";
import "./filters.js";
```

- [ ] **Step 10: CSS を書く**

`internal/web/static/app.css` の `.count` と `.logout` を直す。右寄せをボタンの有無によらず効かせるため、押し出す役を枚数表示の右側に移す。

```css
/* .topbar は flex row。枚数表示の右に余白を全部取り、ログアウトと絞り込み
   ボタンを右端へ押し出す。どちらが出ていなくても右寄せが崩れない。 */
.count {
	font-size: 0.8rem;
	opacity: 0.6;
	margin-right: auto;
}

/* 指定しないとブラウザ既定の淡色ボタンが暗い上部バーの上で枚数表示に
   ぶつかって浮く。 */
.logout button,
.filter-toggle {
	border: 1px solid rgba(255, 255, 255, 0.3);
	border-radius: 4px;
	background: transparent;
	color: var(--fg);
	font: inherit;
	font-size: 0.8rem;
	padding: 4px 10px;
	cursor: pointer;
}
.logout button:hover,
.filter-toggle:hover {
	background: rgba(255, 255, 255, 0.1);
}
/* 条件が掛かっている間は反転させる。何が掛かっているかはシートを開けば分かる。 */
.filter-toggle.active {
	background: var(--fg);
	color: var(--bg);
}
```

既存の `.logout { margin-left: auto; }` と、`.logout button` / `.logout button:hover` の単独の定義は消す（上にまとめた）。

`.gallery` の定義の前に、パネルの定義を足す。

```css
/* 絞り込みのパネル。狭い画面では画面の下に固定したシートで、普段は隠す。
   z-index はスクラバー(6)より上、個別表示(10)より下。 */
.filters {
	display: none;
	position: fixed;
	left: 0;
	right: 0;
	bottom: 0;
	z-index: 7;
	max-height: 60vh;
	overflow-y: auto;
	padding: 12px 16px calc(12px + env(safe-area-inset-bottom));
	background: #222;
	border-radius: 12px 12px 0 0;
	box-shadow: 0 -4px 12px rgba(0, 0, 0, 0.5);
}
.filters.open {
	display: block;
}
.filter-section h2 {
	margin: 0 0 4px;
	font-size: 0.8rem;
	font-weight: normal;
	opacity: 0.6;
}
.filter-section ul {
	margin: 0 0 12px;
	padding: 0;
	list-style: none;
}
.filter-section a {
	display: block;
	padding: 8px 0;
	color: var(--fg);
	text-decoration: none;
}
.filter-section a.selected {
	font-weight: bold;
}
.filter-section a.selected::before {
	content: "✓ ";
}
```

ファイル末尾の `@media (min-width: 1100px)` の塊の後ろに、広い画面の定義を足す。境目はタイルの列幅を変える境目と同じにする。

```css
/* 広い画面ではパネルを左のサイドバーとして常に出す。トップバーの下に
   貼り付けて、一覧をスクロールしても残す。一覧は残りの幅に収まり、列数は
   gallery.js が #layoutprobe から測り直す。 */
@media (min-width: 1100px) {
	.page {
		display: flex;
		align-items: flex-start;
	}
	.page > .gallery {
		flex: 1;
		min-width: 0;
	}
	.filters {
		display: block;
		position: sticky;
		top: var(--topbar-h);
		flex: none;
		width: 200px;
		max-height: calc(100vh - var(--topbar-h));
		background: transparent;
		border-radius: 0;
		box-shadow: none;
	}
	.filter-toggle {
		display: none;
	}
}
```

- [ ] **Step 11: テストが通ることを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestFilter' ./internal/web/`
Expected: PASS（名前の無い環境では SKIP）

Run: `make browser-test`
Expected: PASS。共有の200枚のコーパスはテストを走らせている自分の所有なので、名前が引ければ 1600px などの広い画面でサイドバーが出る。列数は各テストが `#window` の計算結果から読んでいるので、幅が狭くなっても期待値は追従する

Run: `make unit-test-race && npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`
Expected: PASS、型エラー無し

- [ ] **Step 12: コミット**

```bash
git add internal/web/filter.go internal/web/view.go internal/web/templates/index.html internal/web/static/app.css internal/web/static/app.js internal/web/static/filters.js internal/web/helper_test.go internal/web/web_test.go internal/web/browser_test.go
git commit -m "feat: Add the filter panel with the owner section"
```

---

### Task 5: 文書を直す

**Files:**
- Modify: `docs/web-structure.md`、`docs/design.md`

- [ ] **Step 1: web-structure.md を直す**

routes の図を次にする。

```
routes (methods of Handler)                               web.go
  GET /{$}         handleIndex --+
  GET /item/{id}   handleItem  --+-> filterFrom -> renderIndex -> buildIndexView -> tmpl "index"
  GET /tiles       handleTiles ----> parseChunk, filterFrom -> listMedia -> JSON
  GET /thumb/{id}  handleThumb ----> lookupMedia -> thumb.Provider
                                                    or serveNoPreview
  GET /full/{id}   handleFull  ----> lookupMedia -> fullViewPath
  GET /info/{id}   handleInfo  ----> lookupMedia -> owner.Name -> JSON

filter.go
  filterFrom (?owner= -> store.Filter), filterURL (store.Filter -> query),
  toggleOwner, ownerChoices (store.Owners -> owner.Name)
```

View models の `indexView` を次にする。

```
indexView                          view.go
  +-- Total, ChunkSize
  +-- TilesURL, HomeURL (carry the filter)
  +-- DayGroups ([]dayGroup as JSON)
  +-- OpenIndex, AuthEnabled
  +-- Filtered, Owners ([]ownerChoice), EveryOwner, EveryOwnerURL
```

`[]mediaView` の行の後ろに注記を足す。

```
  pageUrl carries the filter (/item/{id}?owner=...)
```

Client の図を次にする。

```
index.html
  +-- app.js (entry)
        +-- gallery.js  --> chunk.js, datefmt.js, layout.js, idiomorph.esm.js
        +-- lightbox.js --> gallery.js, chunk.js, datefmt.js
        +-- scrubber.js --> gallery.js, chunk.js, layout.js
        +-- filters.js  (opens #filters as a sheet on narrow screens)

chunk.js --> fetch data-tiles + chunk
lightbox.js --> fetch /info/{id}; closes to data-home

<img>, <video> --> /thumb/{id}, /full/{id}
```

- [ ] **Step 2: design.md を直す**

`docs/design.md` の「バックエンド/フロントエンド境界」の箇条（`/info/{id}` の項目の後ろ）に足す。

```markdown
  - 一覧の絞り込みは URL のクエリ（`?owner=<uid>` を繰り返す）で持つ。チャンクの
    取得先（`data-tiles`）、個別表示を閉じて戻る先（`data-home`）、タイルの
    `pageUrl`、絞り込みのパネルのリンクは、どれもサーバーが条件込みで作る。JS は
    条件の組み立て方を知らない。パネルは PC で左のサイドバー、スマホで下から出る
    シートになる（経緯は docs/superpowers/specs/2026-11-01-owner-filter-design.md）
```

- [ ] **Step 3: コミット**

```bash
git add docs/web-structure.md docs/design.md
git commit -m "docs: Describe the owner filter"
```
