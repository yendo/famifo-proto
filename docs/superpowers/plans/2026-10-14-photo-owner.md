# 写真ごとに撮影者を表示する 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 取り込み時にファイルの所有者の uid を DB に記録し、全画面表示の上端にその人の名前を出す。

**Architecture:** `media.Media` が uid を持ち、`store` が `owner_uid` 列に書き、`index` が `Lstat` の結果から取る（Task 1）。web は `GET /info/{id}` で1件分の情報を JSON で返し、uid を `os/user.LookupId` で名前に変える。`/tiles` の各要素には `infoUrl` だけを足す（Task 2）。ライトボックスは写真を送るたびに `infoUrl` を取り、名前を上端に出す（Task 3）。最後に起動ログと文書を直す（Task 4）。

**Tech Stack:** Go（`net/http`、`encoding/json`、`os/user`、`syscall`、modernc.org/sqlite）、vanilla JS（ES モジュール、JSDoc + `tsc` の型検査）、chromedp（ブラウザテスト）

**Spec:** [docs/superpowers/specs/2026-10-14-photo-owner-design.md](../specs/2026-10-14-photo-owner-design.md)

## Global Constraints

- コードコメントとテストのコメントは日本語（既存ファイルに合わせる）。識別子、ログのメッセージ、テストの失敗メッセージ、README と `docs/web-structure.md` は英語
- 実機の個人情報（実名、アカウント名、実際の uid、NAS のパス）をコード・テスト・文書に書かない。例の uid は `1000`、例の名前は架空のものにする
- コミットメッセージは英語の要約1行のみ。本文とトレーラーは付けない。type は `feat:` / `fix:` / `refactor:` / `test:` / `docs:` から選ぶ
- ブランチは `feature-photo-owner`（既にこのブランチ上にある）
- テストは公開APIだけで書く。外部テストパッケージ（`media_test` / `store_test` / `index_test` / `web_test`）。テストのためだけの公開や `export_test.go` への追加はしない
- 単体テスト: `make unit-test-race`。1つだけなら `go test -race -run '<名前>' ./internal/<pkg>/`
- ブラウザテスト: `make browser-test`。1つだけなら `FAMIFO_BROWSER_TESTS=required go test -tags browser -run '<名前>' ./internal/web/`。コンパイルの確認だけなら `go vet -tags browser ./internal/web/`
- JS の型検査: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`（リポジトリのルートで）
- `New` と `Restore` の引数、DB の列はどちらも `path` の次に所有者を置く
- スキーマ移行は書かない。列が増えるので既存の DB は作り直す
- 所有者の変更（`chown`）には追従しない。走査の飛ばし条件と watcher は変えない
- JS では HTML の文字列を組み立てない。文言は `textContent` で入れる
- CSP の値は変えない（`connect-src 'self'` で `/info/` の fetch も通る）
- 宣言の並びは入口が先。ハンドラはルートの順、補助はその後ろに出てくる順

## ファイル構成

| ファイル | 変更 |
|---|---|
| `internal/media/media.go` | `ownerUID` フィールド、`New`/`Restore` の引数、`OwnerUID()` |
| `internal/media/media_test.go` | 所有者のテスト。既存の呼び出しに引数を足す |
| `internal/store/store.go` | `owner_uid` 列、`upsertSQL`、`selectCols`、`scanMedia` |
| `internal/store/store_test.go` | 往復のテスト。`mediaAt` と `Restore` の呼び出し |
| `internal/index/index.go` | `Lstat` の結果から uid を取って `media.New` に渡す |
| `internal/index/index_test.go` | 所有者が記録されるテスト |
| `internal/index/helper_test.go` | `idOf` の `media.New` に引数を足す |
| `internal/thumb/thumb_test.go` | `media.New` に引数を足す |
| `internal/web/view.go` | `mediaView.InfoURL`、`infoView` |
| `internal/web/web.go` | ルート `GET /info/{id}`、`handleInfo`、`ownerName`、`nobodyUID`、CSP のコメント |
| `internal/web/login.go` | `isDataPath` に `/info/` |
| `internal/web/web_test.go` | `/info` のテスト、`tile.InfoURL`、`addMedia` の `media.New` |
| `internal/web/login_test.go` | 401 の経路に `/info/abc` |
| `internal/web/static/chunk.js` | `Tile` の `infoUrl` |
| `internal/web/static/lightbox.js` | 名前の取得と表示 |
| `internal/web/templates/index.html` | `<p class="lb-owner" hidden>` |
| `internal/web/static/app.css` | `.lb-owner` |
| `internal/web/browser_test.go` | 名前の表示のテスト。`media.New` に引数を足す |
| `main.go` | `/etc/passwd` を読めないときの起動ログ |
| `README.md` / `Dockerfile` | `/etc/passwd` のマウント、撮影者の表示の説明 |
| `docs/web-structure.md` / `docs/design.md` | ルートと `/info` の追記 |

---

### Task 1: 所有者の uid を取り込んで DB に記録する

**Files:**
- Modify: `internal/media/media.go`
- Modify: `internal/store/store.go`
- Modify: `internal/index/index.go:84-145`
- Test: `internal/media/media_test.go`、`internal/store/store_test.go`、`internal/index/index_test.go`
- 引数の追従のみ: `internal/index/helper_test.go`、`internal/thumb/thumb_test.go`、`internal/web/web_test.go`、`internal/web/browser_test.go`

**Interfaces:**
- Produces:
  - `func media.New(path string, ownerUID uint32, takenAt, modTime time.Time) (media.Media, error)`
  - `func media.Restore(path string, ownerUID uint32, takenAtLocal string, modTime time.Time) media.Media`
  - `func (m media.Media) OwnerUID() uint32`
  - `store.GetByID` / `ListRange` などが返す `Media` の `OwnerUID()` は DB の `owner_uid`

- [ ] **Step 1: media のテストを書く**

`internal/media/media_test.go` の `TestRestoreKeepsTheStoredValues` を次に置き換え、その前に `TestNewKeepsTheOwner` を足す。

```go
// 所有者はそのまま持つ。撮影者の判別に使う。
func TestNewKeepsTheOwner(t *testing.T) {
	t.Parallel()

	m, err := media.New("/items/a.jpg", 1000, time.Time{}, testModTime)

	require.NoError(t, err)
	require.Equal(t, uint32(1000), m.OwnerUID())
}

func TestRestoreKeepsTheStoredValues(t *testing.T) {
	t.Parallel()
	const path = "/items/a.jpg"

	m := media.Restore(path, 1000, "2021-03-04T05:06:07", testModTime)

	require.Equal(t, path, m.Path())
	require.Equal(t, uint32(1000), m.OwnerUID())
	require.Equal(t, "2021-03-04T05:06:07", m.TakenAtLocal())
	require.Equal(t, "2021-03-04", m.Day())
	require.True(t, m.ModTime().Equal(testModTime))
}
```

同じファイルの残りの `media.New(` の呼び出し（14、16、18、53、67、79、87、103 行目）は、パスの次に `0, ` を入れる。例: `media.New("/items/a.jpg", 0, time.Time{}, testModTime)`。所有者はこれらのテストの関心ではない。

- [ ] **Step 2: store のテストを書く**

`internal/store/store_test.go` の `mediaAt` を次に置き換える。

```go
// testOwnerUID はテストの行に入れる所有者。撮影日時とmtimeのどちらとも違う値にする。
const testOwnerUID uint32 = 1000

// mtimeは撮影日時とずらす。SELECTの列順とScanの並びが入れ替わったとき、
// 同じ値だと往復で気づけない。
//
// New を通すのは、撮影地の時刻の整形を store のテストで書き直さないため。
func mediaAt(t *testing.T, path string, takenAt time.Time) media.Media {
	t.Helper()
	m, err := media.New(path, testOwnerUID, takenAt, takenAt.Add(time.Hour))
	require.NoError(t, err)
	return m
}
```

`TestAllPaths` の `media.Restore("/items/a.jpg", "2020-09-13T12:26:40", ...)` は `media.Restore("/items/a.jpg", testOwnerUID, "2020-09-13T12:26:40", ...)` にする。

`TestAllPaths` の前に足す。

```go
// 所有者は列として往復する。撮影者の名前は配信のときにここから引く。
func TestOwnerUIDSurvivesTheRoundTrip(t *testing.T) {
	t.Parallel()
	s := openTestStore(t)
	ctx := context.Background()
	m := mediaAt(t, "/items/a.jpg", time.Unix(1600000000, 0))
	require.NoError(t, s.Upsert(ctx, m))

	got, err := s.GetByID(ctx, m.ID())

	require.NoError(t, err)
	require.Equal(t, testOwnerUID, got.OwnerUID())
}
```

- [ ] **Step 3: index のテストを書く**

`internal/index/index_test.go` の `TestScanStoresRasterPhotoWithThumb` の後に足す。`os` は既に import されている。

```go
// 所有者はファイルの uid から取る。テストが書いたファイルの所有者はテストを
// 走らせている自分である。別の uid へ chown するには root が要るので扱わない。
func TestScanRecordsTheFileOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)

	require.NoError(t, f.sc.ScanOnce(context.Background()))

	got, err := f.st.GetByID(context.Background(), idOf(t, path))
	require.NoError(t, err)
	require.Equal(t, uint32(os.Getuid()), got.OwnerUID())
}
```

`internal/index/helper_test.go` の `idOf` は `media.New(path, 0, time.Time{}, time.Time{})` にする。ID はパスだけから決まる。

- [ ] **Step 4: テストが失敗することを確かめる**

Run: `go test ./internal/media/ ./internal/store/ ./internal/index/`
Expected: コンパイルエラー（`too many arguments in call to media.New`、`m.OwnerUID undefined`）

- [ ] **Step 5: media を実装する**

`internal/media/media.go`:

`Media` を次にする。

```go
type Media struct {
	path         string    // ディスク上の絶対パス
	ownerUID     uint32    // ファイルの所有者のuid。撮影者の判別に使う
	takenAtLocal string    // 撮影地の時刻（layoutWithoutTZ）。読み取れなければmtime
	modTime      time.Time // ファイルのmtime。再スキャン時の変更検知に使う
}
```

`New` のコメントの「サムネイルの調達より先に組み立てる。」の段落の前に次を足し、シグネチャと本体を変える。

```go
// ownerUID はファイルの所有者。famifo は所有者を撮影者とみなす。名前ではなく
// uid を持つのは、名前を配信のときに引くためである。アカウント側で名前を
// 変えても、インデックスを作り直さずに済む。
```

```go
func New(path string, ownerUID uint32, takenAt, modTime time.Time) (Media, error) {
	if !IsCandidate(path) {
		return Media{}, fmt.Errorf("%s cannot be indexed", path)
	}
	if takenAt.IsZero() {
		takenAt = modTime.Local()
	}
	return Media{
		path:         path,
		ownerUID:     ownerUID,
		takenAtLocal: takenAt.Format(layoutWithoutTZ),
		modTime:      modTime,
	}, nil
}
```

```go
func Restore(path string, ownerUID uint32, takenAtLocal string, modTime time.Time) Media {
	return Media{
		path:         path,
		ownerUID:     ownerUID,
		takenAtLocal: takenAtLocal,
		modTime:      modTime,
	}
}
```

`Path()` の後に足す。

```go
// OwnerUID はファイルの所有者のuidを返す。撮影者の判別に使う。
// 取り込んだ時点の値で、後から chown しても追従しない。chown は mtime を
// 変えないので、再スキャンが変更として拾わないためである。
func (m Media) OwnerUID() uint32 { return m.ownerUID }
```

- [ ] **Step 6: store を実装する**

`internal/store/store.go`:

`schema` の表に列を足す。

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
`
```

```go
const upsertSQL = `
INSERT INTO media (id, path, owner_uid, taken_at_local, mod_time, day)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    path           = excluded.path,
    owner_uid      = excluded.owner_uid,
    taken_at_local = excluded.taken_at_local,
    mod_time       = excluded.mod_time,
    day            = excluded.day`
```

`Upsert` の引数:

```go
	_, err := s.db.ExecContext(ctx, upsertSQL,
		m.ID(), m.Path(), int64(m.OwnerUID()), m.TakenAtLocal(), m.ModTime().Unix(), m.Day())
```

`int64` に変えて渡すのは、ドライバが符号なしの型を受け付けるかに頼らないためである。

```go
const selectCols = `path, owner_uid, taken_at_local, mod_time`

// scanMedia は1行を復元する。引数の並びは selectCols と一致していなければならない。
func scanMedia(row interface{ Scan(...any) error }) (media.Media, error) {
	var path, takenAtLocal string
	var ownerUID uint32
	var modTime int64
	if err := row.Scan(&path, &ownerUID, &takenAtLocal, &modTime); err != nil {
		return media.Media{}, err
	}
	return media.Restore(path, ownerUID, takenAtLocal, time.Unix(modTime, 0)), nil
}
```

`probeReadable` は `selectCols` を読むので変えなくてよい。列の無い古い DB は起動時に `cannot read the database` で落ちる。

- [ ] **Step 7: index を実装する**

`internal/index/index.go` の import に `"syscall"` を足す。`indexFile` の `m, err := media.New(path, takenAt, fi.ModTime())` を次にする。

```go
	// 所有者は撮影者の判別に使う。Lstat の結果に入っているので読み足しは無い。
	// syscall.Stat_t は Linux のもので、ビルド対象は Linux だけである。
	ownerUID := fi.Sys().(*syscall.Stat_t).Uid

	// Mediaを先に組み立てる。ModTime が原本の版であり、thumb はそれを見て出力の
	// 名前を決める。ここで確定させておけば、インデックスに載る版とサムネイルの
	// 名前に入る版が食い違いようがない。
	m, err := media.New(path, ownerUID, takenAt, fi.ModTime())
```

（既存の「Mediaを先に組み立てる。」のコメントはそのまま残し、その前に所有者の2行を置く。）

- [ ] **Step 8: 残りの呼び出しを直す**

- `internal/thumb/thumb_test.go` 27 行目と 243 行目: パスの次に `0, ` を入れる
- `internal/web/web_test.go` の `addMedia`（80 行目付近）:

  ```go
  	// 原本はテストが書いたので、所有者はテストを走らせている自分である。
  	m, err := media.New(path, uint32(os.Getuid()), takenAt, takenAt)
  ```
- `internal/web/web_test.go` 565 行目付近の `unindexed`: `media.New(filepath.Join(f.mediaDir, "never.jpg"), 0, time.Time{}, time.Time{})`
- `internal/web/browser_test.go` 467 行目付近（`expectedMediaURLs`、ID だけを使う）: `media.New(path, 0, time.Time{}, time.Time{})`
- `internal/web/browser_test.go` 2344 行目と 2347 行目付近（`TestLightboxSwitchesBetweenImageAndVideo`）: パスの次に `uint32(os.Getuid()), ` を入れる

- [ ] **Step 9: テストが通ることを確かめる**

Run: `go vet ./... && go vet -tags browser ./internal/web/ && make unit-test-race`
Expected: すべて PASS

- [ ] **Step 10: コミット**

```bash
git add internal/media internal/store internal/index internal/thumb/thumb_test.go internal/web/web_test.go internal/web/browser_test.go
git commit -m "feat: Record the owner uid of each file in the index"
```

---

### Task 2: `GET /info/{id}` で撮影者の名前を返す

**Files:**
- Modify: `internal/web/view.go`
- Modify: `internal/web/web.go`
- Modify: `internal/web/login.go:257-264`
- Test: `internal/web/web_test.go`、`internal/web/login_test.go:165`

**Interfaces:**
- Consumes: `media.New(path, ownerUID, takenAt, modTime)`、`Media.OwnerUID()`（Task 1）
- Produces:
  - `GET /info/{id}` → `200 application/json` `{"owner":"<name>"}`。名前が無ければ `{}`。id が無ければ 404
  - `/tiles` の各要素に `"infoUrl": "/info/<id>"`

- [ ] **Step 1: テストを書く**

`internal/web/web_test.go`:

import に `"errors"`、`"os/user"`、`"strconv"` を足す。

`type tile struct` に `InfoURL string `json:"infoUrl"`` を `ThumbURL` の後に足し、`TestTilesDescribeEachItem` の期待値に `InfoURL: "/info/" + m.ID(),` を `ThumbURL` の後に足す。

`TestServeThumb` の前（`doGet` の後）に足す。

```go
// addOwnedBy は所有者を指定してDBの行だけを用意する。/info/{id} はDBの行しか
// 見ないので原本は置かない。他人の uid のファイルは root でないと作れない。
func (f *webFixture) addOwnedBy(t *testing.T, name string, uid uint32) media.Media {
	t.Helper()
	takenAt := time.Unix(1600000000, 0)
	m, err := media.New(filepath.Join(f.mediaDir, name), uid, takenAt, takenAt)
	require.NoError(t, err)
	require.NoError(t, f.st.Upsert(context.Background(), m))
	return m
}

// unknownUID はアカウントの無い uid を返す。どの uid が空いているかは環境で
// 変わるので、決め打ちせずに探す。
func unknownUID(t *testing.T) uint32 {
	t.Helper()
	for uid := uint32(2_000_000_000); uid < 2_000_000_100; uid++ {
		_, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
		var unknown user.UnknownUserIdError
		if errors.As(err, &unknown) {
			return uid
		}
	}
	t.Fatal("no uid without an account was found")
	return 0
}

// 撮影者の名前はアカウントのフルネーム（GECOS）で、空ならアカウント名にする。
func TestInfoNamesTheOwner(t *testing.T) {
	t.Parallel()
	uid := os.Getuid()
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		t.Skipf("the test user has no account entry: %v", err)
	}
	want := u.Name
	if want == "" {
		want = u.Username
	}
	f := newWebFixture(t, 10)
	m := f.addOwnedBy(t, "a.jpg", uint32(uid))

	rec := doGet(t, f.h, "/info/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var got struct {
		Owner string `json:"owner"`
	}
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&got))
	require.Equal(t, want, got.Owner)
}

// 名前を出せないときはキーごと省く。クライアントは owner が無ければ欄を隠す。
//
// nobody は /etc/passwd に行があっても出さない。userns-remap や rootless Docker では
// 範囲外の uid がこれに潰れて見え、誰の写真か分からないのに「nobody」と出てしまう。
func TestInfoOmitsTheOwnerWithoutAName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		uid  uint32
	}{
		{"nobody", 65534},
		{"no account", unknownUID(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newWebFixture(t, 10)
			m := f.addOwnedBy(t, "a.jpg", tc.uid)

			rec := doGet(t, f.h, "/info/"+m.ID())

			require.Equal(t, http.StatusOK, rec.Code)
			require.JSONEq(t, `{}`, rec.Body.String())
		})
	}
}

// JSから取るので、/item/{id} と違ってギャラリーへは送らない。
func TestInfoNotFoundForUnknownID(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)

	rec := doGet(t, f.h, "/info/deadbeef")

	require.Equal(t, http.StatusNotFound, rec.Code)
}
```

`internal/web/login_test.go` の `TestDataPathsReturn401WhenNotSignedIn` の経路に `"/info/abc"` を足す。

```go
	for _, path := range []string{"/tiles?chunk=0", "/thumb/abc", "/full/abc", "/info/abc"} {
```

- [ ] **Step 2: テストが失敗することを確かめる**

Run: `go test -race -run 'TestInfo|TestTilesDescribeEachItem|TestDataPathsReturn401WhenNotSignedIn' ./internal/web/`
Expected: FAIL。`TestInfo*` は 404、`TestTilesDescribeEachItem` は `infoUrl` が無い、`TestDataPathsReturn401WhenNotSignedIn` は `/info/abc` が 302

- [ ] **Step 3: view.go を実装する**

`mediaView` に `ThumbURL` の後で足す。

```go
	InfoURL  string `json:"infoUrl"` // 全画面表示に重ねる情報（/info/<id>）
```

`listMedia` の組み立てに `InfoURL:  "/info/" + m.ID(),` を `ThumbURL` の後に足す。

`dayGroup` の後に足す。

```go
// infoView は /info/{id} の応答。全画面表示に重ねる1件分の情報で、一覧では
// 使わないので /tiles には載せない。出せない項目はキーごと省く。
type infoView struct {
	Owner string `json:"owner,omitempty"` // 撮影者の名前
}
```

- [ ] **Step 4: web.go を実装する**

import に `"encoding/json"` が既にあることを確かめ、`"os/user"` を足す（`strconv` は既にある）。

`noOpenItem` の後に足す。

```go
// nobodyUID は nobody の uid。所有者がこれなら名前を出さない。userns-remap や
// rootless Docker では、範囲外の uid がこれに潰れて見える。誰の写真か分からない
// のに、/etc/passwd に nobody の行があると「nobody」と出てしまう。
const nobodyUID = 65534
```

ルート表の `GET /full/{id}` の後に足す。

```go
	routes.HandleFunc("GET /info/{id}", h.handleInfo)
```

`handleFull` の後に足す。

```go
// handleInfo は全画面表示に重ねる1件分の情報をJSONで返す。一覧では使わない
// 情報をここに分け、/tiles のチャンクを軽く保つ。
func (h *Handler) handleInfo(w http.ResponseWriter, r *http.Request) {
	m, ok := h.lookupMedia(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(infoView{Owner: ownerName(m.OwnerUID())}); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		h.log.Error("failed to encode the info", "err", err)
	}
}
```

ファイルの末尾（`fullViewPath` の後）に足す。

```go
// ownerName は所有者の uid から表示する名前を引く。出せる名前が無ければ空を返す。
//
// 要求のたびに引き、覚えておかない。引くのは開いた1件分だけで、アカウント側で
// 名前を変えれば次に開いたときから反映される。cgo を使わないビルドでは os/user が
// /etc/passwd を自分で読む。scratch のイメージには /etc/passwd が無いので、ホストの
// ものをマウントしない限り名前は出ない。
//
// フルネーム（GECOS）を先に使い、空ならアカウント名にする。
func ownerName(uid uint32) string {
	if uid == nobodyUID {
		return ""
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Username
}
```

CSP のコメント（`contentSecurityPolicy` の前）の2箇所を直す。

```go
// default-src を 'none' にして、必要なものだけを足す。ギャラリーが読むのは
// /static/ のCSSとJS、/thumb/ と /full/ の画像と動画、それに /tiles と /info/ の
// fetch だけで、外部から取るものは1つも無い（idiomorph も同梱してある）。
// connect-src が要るのは、仮想スクロールが /tiles を、ライトボックスが /info/ を
// fetch するためである。
```

- [ ] **Step 5: login.go を実装する**

```go
// isDataPath はページではなくデータを取りに来た経路を見分ける。
//
// /tiles は chunk.js が、/info/ は lightbox.js が fetch し、/thumb/ と /full/ は
// <img> と <video> が読む。ここに挙げる経路は Handler のルート表と一対一である。
// データを返す経路を足したらここにも足すこと。忘れると、その経路だけ未認証時に
// 白い画面になる。
func isDataPath(p string) bool {
	return p == "/tiles" || strings.HasPrefix(p, "/thumb/") || strings.HasPrefix(p, "/full/") ||
		strings.HasPrefix(p, "/info/")
}
```

`requireSignIn` の中のコメント「/thumb と /full は <img> と <video> が読むので」はそのままでよい。

- [ ] **Step 6: テストが通ることを確かめる**

Run: `go vet ./... && make unit-test-race`
Expected: すべて PASS

- [ ] **Step 7: コミット**

```bash
git add internal/web/view.go internal/web/web.go internal/web/login.go internal/web/web_test.go internal/web/login_test.go
git commit -m "feat: Serve the owner's name for each item at /info/{id}"
```

---

### Task 3: 全画面表示の上端に撮影者の名前を出す

**Files:**
- Modify: `internal/web/templates/index.html:44-54`
- Modify: `internal/web/static/lightbox.js`
- Modify: `internal/web/static/chunk.js:29-35`
- Modify: `internal/web/static/app.css`（`.lb-error` の後）
- Test: `internal/web/browser_test.go`

**Interfaces:**
- Consumes: `/tiles` の `infoUrl`、`GET /info/{id}` の `{"owner"?: string}`（Task 2）

- [ ] **Step 1: ブラウザテストを書く**

`internal/web/browser_test.go` の `TestLightboxSwitchesBetweenImageAndVideo` の後に足す。`encoding/json` を import に足す。

```go
// 写真を開くと撮影者の名前が上端に出て、名前の無い写真へ送ると消えること。
// 前の写真の名前が残ると、別の人が撮ったように見える。
//
// 共有の200枚のコーパスには足さない。TestLightboxSwitchesBetweenImageAndVideo と
// 同じく、自前の小さなサーバーを立てて共有のChromeだけを借りる。
func TestLightboxShowsTheOwner(t *testing.T) {
	requireBrowser(t)

	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	require.NoError(t, os.MkdirAll(mediaDir, 0o755))

	st, err := store.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbs, err := thumb.NewProvider(filepath.Join(dir, "thumbs"))
	require.NoError(t, err)

	// 名前のある写真を新しくして先頭に並べ、その次に名前の無い写真（nobody）を置く。
	bg := context.Background()
	var ids []string
	for _, p := range []struct {
		name    string
		uid     uint32
		takenAt time.Time
	}{
		{"named.jpg", uint32(os.Getuid()), time.Unix(1600000100, 0)},
		{"nameless.jpg", 65534, time.Unix(1600000000, 0)},
	} {
		path := filepath.Join(mediaDir, p.name)
		require.NoError(t, writeTestJPEG(path, len(ids)))
		m, err := media.New(path, p.uid, p.takenAt, p.takenAt)
		require.NoError(t, err)
		require.NoError(t, st.Upsert(bg, m))
		ids = append(ids, m.ID())
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := web.NewHandler(st, thumbs, nil, log)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// 期待する名前は /info から取る。名前の引き方は web_test が確かめており、
	// ここで見るのは、届いた名前が画面に出るかどうかである。
	resp, err := http.Get(srv.URL + "/info/" + ids[0])
	require.NoError(t, err)
	var info struct {
		Owner string `json:"owner"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
	resp.Body.Close()
	if info.Owner == "" {
		t.Skip("the test user has no account entry, so there is no name to show")
	}

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	const ownerJS = `(() => {
		const o = document.querySelector('#lightbox .lb-owner');
		return { hidden: o.hidden, text: o.textContent };
	})()`
	var got struct {
		Hidden bool   `json:"hidden"`
		Text   string `json:"text"`
	}

	err = chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(srv.URL),
		waitForGallery(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox .lb-owner').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(ownerJS, &got),
	)
	require.NoError(t, err, "the owner's name never appeared")
	require.Equal(t, info.Owner, got.Text)

	// 名前の無い写真へ送る。/info の応答が届いて処理されるまで待ってから見る。
	// 先に見ると、送った直後に隠しただけの状態で通ってしまう。
	var settled bool
	err = chromedp.Run(rctx,
		chromedp.KeyEvent(kb.ArrowRight),
		chromedp.Poll(fmt.Sprintf(
			`performance.getEntriesByType('resource').some(e => e.name.endsWith('/info/%s'))`, ids[1]),
			nil, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`new Promise(r => setTimeout(() => r(true), 200))`, &settled, awaitPromise),
		chromedp.Evaluate(ownerJS, &got),
	)
	require.NoError(t, err)
	require.True(t, got.Hidden, "the name of the previous photo is left on screen")
	require.Empty(t, got.Text)
}
```

- [ ] **Step 2: テストが失敗することを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestLightboxShowsTheOwner' ./internal/web/`
Expected: FAIL。`.lb-owner` が無いので `Poll` が JS のエラーかタイムアウトで落ちる

- [ ] **Step 3: マークアップを足す**

`internal/web/templates/index.html` の `<p class="lb-error" hidden>…</p>` の後に足す。

```html
  {{/* 撮影者の名前。写真を送るたびに /info から取り直す。出せる名前が無ければ隠したまま。 */}}
  <p class="lb-owner" hidden></p>
```

- [ ] **Step 4: Tile の型を直す**

`internal/web/static/chunk.js` の `Tile` の `thumbUrl` の後に足す。

```js
 * @property {string} infoUrl 全画面表示に重ねる情報のURL（/info/<id>）
```

- [ ] **Step 5: lightbox.js を実装する**

`errBox` の宣言の後に足す。

```js
	/** @type {HTMLElement} */
	const owner = box.querySelector(".lb-owner");
```

`show()` の `errBox.hidden = true;` の後に足す。

```js
		// 名前は写真より遅れて届く。前の写真の名前を次の写真に残さない。
		owner.hidden = true;
		owner.textContent = "";
		showOwner(tile.infoUrl, mySeq);
```

`stopVideo()` の後に足す。

```js
	// showOwner は撮影者の名前を取って出す。写真の表示は待たせない。
	// 待っている間に別の写真へ送られたか閉じられたら、届いた名前は捨てる。
	// 取れなかったときは隠したままにする。名前が無くても写真は見られる。
	/**
	 * @param {string} url
	 * @param {number} seq
	 */
	async function showOwner(url, seq) {
		try {
			const res = await fetch(url);
			if (!res.ok) return;
			/** @type {{ owner?: string }} */
			const info = await res.json();
			if (seq !== requestSeq || !info.owner) return;
			owner.textContent = info.owner;
			owner.hidden = false;
		} catch {
			// 通信の失敗。隠したままにする
		}
	}
```

`close()` の `errBox.hidden = true;` の後に足す。

```js
		owner.hidden = true;
		owner.textContent = "";
```

- [ ] **Step 6: CSS を足す**

`internal/web/static/app.css` の `.lb-error { … }` の後に足す。

```css
/* 撮影者の名前。下端は動画の再生コントロールと重なるので上端に置く。明るい
   写真でも読めるよう背景を敷く。押しても下の写真と前後送りに届くようにする。 */
.lb-owner {
	position: absolute;
	top: max(12px, env(safe-area-inset-top));
	left: 50%;
	transform: translateX(-50%);
	margin: 0;
	padding: 0.2rem 0.6rem;
	border-radius: 999px;
	background: rgba(0, 0, 0, 0.5);
	color: rgba(255, 255, 255, 0.9);
	font-size: 0.85rem;
	white-space: nowrap;
	pointer-events: none;
}
```

`display` を指定しないので、`hidden` 属性がそのまま効く（`.lb-error` と同じ）。

- [ ] **Step 7: テストと型検査が通ることを確かめる**

Run: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json && make browser-test`
Expected: 型エラーなし。ブラウザテストはすべて PASS

- [ ] **Step 8: コミット**

```bash
git add internal/web/templates/index.html internal/web/static/chunk.js internal/web/static/lightbox.js internal/web/static/app.css internal/web/browser_test.go
git commit -m "feat: Show who took the photo at the top of the full view"
```

---

### Task 4: 起動ログと文書

**Files:**
- Modify: `main.go:66-69`
- Modify: `README.md`（`## Docker` の `### Running it`、`## Using the gallery`）
- Modify: `Dockerfile`（末尾の実行例のコメント）
- Modify: `docs/web-structure.md`、`docs/design.md`

- [ ] **Step 1: 起動ログを足す**

`main.go` の `log.Info("starting", …)` の直後に足す。`os` は既に import されている。

```go
	// 撮影者の名前は /etc/passwd から引く。scratch のイメージには無いので、
	// ホストのものをマウントし忘れると、名前が黙って出なくなる。ここで知らせる。
	if f, err := os.Open("/etc/passwd"); err != nil {
		log.Info("owner names unavailable, mount /etc/passwd to show who took each photo", "err", err)
	} else {
		f.Close()
	}
```

パスが固定なので自動テストは書かない。Step 5 で手で確かめる。

- [ ] **Step 2: README を直す**

`### Running it` の最初の例に `/etc/passwd` のマウントを足す。

```bash
docker run -d --restart unless-stopped -p 8080:8080 \
  -v /volume1/photo:/photos:ro \
  -v /volume1/famifo/data:/data \
  -v /etc/passwd:/etc/passwd:ro \
  famifo
```

`### The `/data` mount is not optional` の前に節を足す。

```markdown
### Showing who took each photo

famifo treats the owner of each file as the person who took it, and shows that
account's name at the top of the full view. The name comes from `/etc/passwd`: the
full name (the GECOS field) if it is set, otherwise the account name. A `scratch`
image has no `/etc/passwd`, so mount the host's read-only as above. Without it no
names are shown, and the startup log says so.

The owner is recorded when a file is indexed. `chown` does not change the
modification time, so famifo does not notice a later change of owner; delete the
database to pick it up. Files owned by uid 65534 (`nobody`) show no name — that is
also what userns-remap and rootless Docker turn owners outside their range into.
```

`## Using the gallery` の箇条書きの最後に足す。

```markdown
- The full view shows who took the photo at the top — see "Showing who took each photo" above
```

- [ ] **Step 3: Dockerfile のコメントを直す**

末尾の1つ目の実行例を次にする。

```dockerfile
#   docker run -d --restart unless-stopped -p 8080:8080 \
#     -v /volume1/photo:/photos:ro \
#     -v /volume1/famifo/data:/data \
#     -v /etc/passwd:/etc/passwd:ro \
#     ghcr.io/yendo/famifo-proto
#
# /etc/passwd は撮影者の名前を引くために読む。ファイルの所有者の uid を名前に
# 変える。省いても動くが、名前は出ない。
```

- [ ] **Step 4: 設計の文書を直す**

`docs/web-structure.md`:

ルート表の `GET /full/{id}` の行の後に足す。

```
  GET /info/{id}   handleInfo  ----> lookupMedia -> ownerName -> JSON
```

`[]mediaView` の項目を直し、その後に `infoView` を足す。

```
[]mediaView (JSON from /tiles)     view.go
  {id, pageUrl, fullUrl, thumbUrl, infoUrl, date, isVideo}

infoView (JSON from /info/{id})    view.go
  {owner?}
```

クライアントの節の `chunk.js --> fetch /tiles?chunk` の後に足す。

```
lightbox.js --> fetch /info/{id}
```

`docs/design.md` の「バックエンド/フロントエンド境界」の `/tiles` の項目の後に足す。

```markdown
  - `/info/{id}` は全画面表示に重ねる1件分の情報（今は撮影者の名前）をJSONで返す。
    一覧では使わない情報を /tiles に載せず、チャンクを軽く保つ。撮影者はファイルの
    所有者で、名前は配信のときに /etc/passwd から引く（経緯は
    docs/superpowers/specs/2026-10-14-photo-owner-design.md）
```

- [ ] **Step 5: 起動ログを手で確かめる**

Run:

```bash
make build-image
docker run -d --name famifo-logcheck --tmpfs /data:uid=65534 \
  -v "$PWD/internal/web/static:/photos:ro" ghcr.io/yendo/famifo-proto
docker logs famifo-logcheck 2>&1 | head -3
docker rm -f famifo-logcheck
```

Expected: 2 行目に `owner names unavailable, mount /etc/passwd ...` が出る。

`-v /etc/passwd:/etc/passwd:ro` を足して同じことをすると、その行が出ない。

- [ ] **Step 6: 全体のテスト**

Run: `go vet ./... && make unit-test-race`
Expected: すべて PASS

- [ ] **Step 7: コミット**

```bash
git add main.go README.md Dockerfile docs/web-structure.md docs/design.md
git commit -m "feat: Log at startup when owner names cannot be looked up, and document the mount"
```
