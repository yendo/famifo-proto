# HEICの拡大表示 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** HEICを拡大表示したとき、Synologyが `@eaDir` に持つ `SYNOPHOTO_THUMB_XL.jpg` を原本の代わりに配信し、Safari以外のブラウザでも画像が見えるようにする。

**Architecture:** 一覧のサムネイルは既に `@eaDir` の `SYNOPHOTO_THUMB_M.jpg` を借りている。同じ発想を拡大表示に広げ、`handlePhoto` に「HEIC かつ `thumb_source = eadir` ならXLを配信する」分岐を1つ足す。XLの有無はstatもせずカラムにも持たず、`thumb_source` から導く。DBスキーマ・テンプレート・`app.js` は変更しない。

**Tech Stack:** Go 1.25、標準ライブラリのみ（`net/http` の `ServeFile`）。新しい依存は追加しない。テストは `testify/require`。

**Spec:** `docs/superpowers/specs/2026-09-02-heic-full-view-design.md`

## Global Constraints

- ブランチは `feature-heic-full-view`。作業開始時点で既にこのブランチにいる
- 新しい依存を追加しない。`go.mod` は変更しない
- `CGO_ENABLED=0` で単一バイナリとしてビルドできること（`make build`）
- `@eaDir` は読むだけ。書き込みも削除も新規作成もしない（テストの一時ディレクトリを除く）
- コード・コメント・設計文書は日本語。README と `.github/` 配下は英語
- コミットメッセージは英語の要約1行のみ。本文なし、`Co-Authored-By:` と `Claude-Session:` トレーラーは付けない
- 全タスクの完了条件に `make vet` と `make unit-test` が通ることを含む

---

### Task 1: `SynoPath` を `SynoThumbPath` に改名する

M用とXL用のパス関数が並ぶと `SynoPath` がどちらを指すのか名前から分からなくなる。XLを足す前に、先に名前を明確にしておく。振る舞いは一切変えない純粋なリネーム。

**Files:**
- Modify: `internal/thumb/thumb.go:55-58`（定義）, `internal/thumb/thumb.go:67`（`HasSyno` 内の呼び出し）
- Modify: `internal/web/handlers.go:163`
- Test: `internal/thumb/thumb_test.go:59,62,68,83,93`
- Test: `internal/index/indexer_test.go:154,192`
- Test: `internal/web/handlers_test.go:61`

**Interfaces:**
- Consumes: なし
- Produces: `thumb.SynoThumbPath(srcPath string) string` — 旧 `thumb.SynoPath` と同一の戻り値。Task 2 以降はこの名前を使う

- [ ] **Step 1: 定義を改名する**

`internal/thumb/thumb.go` の該当箇所を次のように書き換える。

```go
// SynoThumbPath はSynologyがsrcPathの写真用に持つ一覧用サムネイルのパスを返す。
// 実在するとは限らない。あるかどうかは HasSyno で確かめる。
func SynoThumbPath(srcPath string) string {
	return filepath.Join(filepath.Dir(srcPath), eaDir, filepath.Base(srcPath), synoThumbName)
}
```

`HasSyno` の中の `os.Stat(SynoPath(srcPath))` も `os.Stat(SynoThumbPath(srcPath))` にする。

- [ ] **Step 2: ビルドを壊して残りの呼び出し元を洗い出す**

Run: `go build ./...`
Expected: FAIL。`internal/web/handlers.go:163` で `undefined: thumb.SynoPath`

- [ ] **Step 3: 呼び出し元を直す**

`internal/web/handlers.go:163` を `http.ServeFile(w, r, thumb.SynoThumbPath(p.Path))` にする。

Run: `go build ./...`
Expected: PASS（出力なし）

- [ ] **Step 4: テスト側の呼び出しを直す**

`internal/thumb/thumb_test.go` の5箇所（59行のテスト関数名、62・68・83・93行の呼び出し）、`internal/index/indexer_test.go` の2箇所（154・192行）、`internal/web/handlers_test.go` の1箇所（61行）を `SynoThumbPath` にする。テスト関数名も合わせる。

```go
func TestSynoThumbPathPointsAtTheMediumThumbnail(t *testing.T) {
	require.Equal(t,
		"/photos/2026-08-16/@eaDir/IMG_0428.HEIC/SYNOPHOTO_THUMB_M.jpg",
		SynoThumbPath("/photos/2026-08-16/IMG_0428.HEIC"))
}
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `make vet && make unit-test`
Expected: PASS。リネームだけなので、変更前と同じテストが同じ数だけ通る

- [ ] **Step 6: コミット**

```bash
git add internal/thumb/thumb.go internal/thumb/thumb_test.go internal/web/handlers.go internal/web/handlers_test.go internal/index/indexer_test.go
git commit -m "refactor: rename SynoPath to SynoThumbPath"
```

---

### Task 2: `SynoLargePath` を足す

拡大表示に借りるXLのパスを組み立てる関数。ここではまだ誰も呼ばない。

**Files:**
- Modify: `internal/thumb/thumb.go:47-58`
- Test: `internal/thumb/thumb_test.go`（Task 1 で直した `TestSynoThumbPathPointsAtTheMediumThumbnail` の直後に追加）

**Interfaces:**
- Consumes: `thumb.SynoThumbPath(srcPath string) string`（Task 1）
- Produces: `thumb.SynoLargePath(srcPath string) string` — `<写真のディレクトリ>/@eaDir/<写真のファイル名>/SYNOPHOTO_THUMB_XL.jpg` を返す。実在確認はしない。Task 3 が使う

- [ ] **Step 1: 失敗するテストを書く**

`internal/thumb/thumb_test.go` に追加する。

```go
func TestSynoLargePathPointsAtTheXLThumbnail(t *testing.T) {
	require.Equal(t,
		"/photos/2026-08-16/@eaDir/IMG_0428.HEIC/SYNOPHOTO_THUMB_XL.jpg",
		SynoLargePath("/photos/2026-08-16/IMG_0428.HEIC"))
}
```

- [ ] **Step 2: 失敗することを確認する**

Run: `go test ./internal/thumb/ -run TestSynoLargePath -v`
Expected: FAIL。`undefined: SynoLargePath` でコンパイルが通らない

- [ ] **Step 3: 実装する**

`internal/thumb/thumb.go` の定数とパス関数のあたりを次のように書き換える。M用とXL用で `@eaDir` の中の同じディレクトリを指すので、組み立てを共通の非公開関数に寄せる。

```go
// synoThumbName は一覧に借りるサムネイルのファイル名。Mは短辺320px（4:3なら
// 長辺427px）で、famifo自身が作る長辺480pxよりわずかに小さい。
const synoThumbName = "SYNOPHOTO_THUMB_M.jpg"

// synoLargeName は拡大表示に借りるJPEGのファイル名。長辺1707px・約1MBで、
// 一覧には過大だが1枚だけ見せる場面では妥当な大きさになる。
const synoLargeName = "SYNOPHOTO_THUMB_XL.jpg"

// synoPath は @eaDir の中の1ファイルのパスを組み立てる。
func synoPath(srcPath, name string) string {
	return filepath.Join(filepath.Dir(srcPath), eaDir, filepath.Base(srcPath), name)
}

// SynoThumbPath はSynologyがsrcPathの写真用に持つ一覧用サムネイルのパスを返す。
// 実在するとは限らない。あるかどうかは HasSyno で確かめる。
func SynoThumbPath(srcPath string) string { return synoPath(srcPath, synoThumbName) }

// SynoLargePath はSynologyがsrcPathの写真用に持つ拡大表示用JPEGのパスを返す。
// SynoThumbPath と同じディレクトリを指す。存在は確かめない。MとXLは同じ生成器が
// 一緒に書くため、Mがあることを確かめてあればXLもあるものとして扱う。
func SynoLargePath(srcPath string) string { return synoPath(srcPath, synoLargeName) }
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/thumb/ -v`
Expected: PASS。`TestSynoLargePathPointsAtTheXLThumbnail` を含め、パッケージ内の全テストが通る

- [ ] **Step 5: 全体を確認する**

Run: `make vet && make unit-test`
Expected: PASS

- [ ] **Step 6: コミット**

```bash
git add internal/thumb/thumb.go internal/thumb/thumb_test.go
git commit -m "feat: add a path helper for Synology's XL thumbnail"
```

---

### Task 3: HEICの拡大表示でXLを配信する

この計画の本体。`handlePhoto` に分岐を足し、HEICかつ `@eaDir` から借りている写真だけXLに差し替える。

**Files:**
- Modify: `internal/web/handlers.go:170-180`
- Modify: `internal/photo/photo.go:18-19`（`KindOpaque` のコメント）
- Test: `internal/web/handlers_test.go:56-62`（`addPhoto` の拡張）, `internal/web/handlers_test.go:118-127`（既存テストの補強）, 末尾に新規2本

**Interfaces:**
- Consumes: `thumb.SynoLargePath(srcPath string) string`（Task 2）, `photo.KindOf(name string) photo.Kind`, `photo.KindOpaque`, `store.ThumbSyno`
- Produces: なし（HTTPの振る舞いのみ）

- [ ] **Step 1: テスト用の写真にXLも置くようにする**

`internal/web/handlers_test.go` の `addPhoto` の `switch` を書き換える。`ThumbSyno` の写真はMとXLの両方を持つ、という実環境の前提をフィクスチャに写す。

```go
	switch src {
	case store.ThumbFamifo:
		writeFileAt(t, thumb.CachePath(f.thumbDir, p.ID), "thumb-"+name)
	case store.ThumbSyno:
		writeFileAt(t, thumb.SynoThumbPath(path), "eadir-"+name)
		writeFileAt(t, thumb.SynoLargePath(path), "eadir-xl-"+name)
	}
```

- [ ] **Step 2: 失敗するテストを2本書く**

`internal/web/handlers_test.go` の末尾に追加する。

```go
func TestServeHEICBorrowsTheLargeThumbFromEaDir(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), store.ThumbSyno)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-xl-a.heic", rec.Body.String(),
		"HEICの原本はSafari以外で表示できないのでXLを代わりに配信する")
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"),
		"配信するのはJPEGなので拡張子からMIMEが引ける")
}

func TestServeOriginalForRasterEvenWithEaDir(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), store.ThumbSyno)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String(),
		"元から表示できる形式は原本のフル解像度を出す。借用は見えないものの代替に限る")
}
```

- [ ] **Step 3: 失敗することを確認する**

Run: `go test ./internal/web/ -run 'TestServeHEICBorrowsTheLargeThumbFromEaDir|TestServeOriginalForRasterEvenWithEaDir' -v`
Expected: `TestServeHEICBorrowsTheLargeThumbFromEaDir` が FAIL（本文が `original-a.heic`、Content-Type が `image/heic`）。`TestServeOriginalForRasterEvenWithEaDir` は最初から PASS（変更前の挙動でも正しいので、退行よけとして置く）

- [ ] **Step 4: `handlePhoto` に分岐を足す**

`internal/web/handlers.go` の `handlePhoto` を次のように書き換える。

```go
// handlePhoto は拡大表示用の画像を配信する。
//
// HEICはSafari以外のブラウザが表示できない。@eaDir から借りているなら原本ではなく
// SynologyのXL（長辺1707px）を配信する。thumb_source が eadir であればMがあり、
// MとXLは同じ生成器が一緒に書くので、XLの存在はそこから導ける。
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if photo.KindOf(p.Path) == photo.KindOpaque && p.ThumbSource == store.ThumbSyno {
		// 拡張子が .jpg なのでServeFileがContent-Typeを引ける。
		http.ServeFile(w, r, thumb.SynoLargePath(p.Path))
		return
	}
	// ServeFileは拡張子からMIMEを引くがHEIC/HEIFを知らない。
	// 先に設定しておけばServeContentは上書きしない。
	w.Header().Set("Content-Type", photo.ContentType(p.Path))
	http.ServeFile(w, r, p.Path)
}
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `go test ./internal/web/ -v`
Expected: PASS。新規2本に加え、`TestServeOriginalSetsHEICContentType`（借りるものが無いHEICは原本）と `TestServeThumbFromEaDir` が通ったままであること

- [ ] **Step 6: 借りるものが無いHEICのテストを補強する**

`internal/web/handlers_test.go:118` の `TestServeOriginalSetsHEICContentType` は Content-Type しか見ていない。原本そのものが返っていることも明示する。

```go
func TestServeOriginalSetsHEICContentType(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), store.ThumbNone)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.heic", rec.Body.String(),
		"借りるものが無いHEICは従来どおり原本を配信する")
	require.Equal(t, "image/heic", rec.Header().Get("Content-Type"),
		"Goのmimeパッケージが知らないので自前で設定する")
}
```

Run: `go test ./internal/web/ -run TestServeOriginalSetsHEICContentType -v`
Expected: PASS

- [ ] **Step 7: `KindOpaque` のコメントを事実に合わせる**

`internal/photo/photo.go:18-19` を書き換える。「Safari以外では表示できないが、設計上それを許容している」はもう成り立たない。

```go
	// KindOpaque はインデックスはするがデコードせず、原本をそのまま配信するファイル。
	// HEIC/HEIFが該当する。原本はSafari以外では表示できないため、@eaDir から
	// 借りられる場合に限り、配信時にSynologyのJPEGへ差し替える（internal/web）。
	KindOpaque
```

- [ ] **Step 8: 全体を確認する**

Run: `make vet && make unit-test && make build`
Expected: すべて PASS。`make build` は `CGO_ENABLED=0` で通ること

- [ ] **Step 9: コミット**

```bash
git add internal/web/handlers.go internal/web/handlers_test.go internal/photo/photo.go
git commit -m "feat: serve Synology's XL thumbnail for HEIC so it shows outside Safari"
```

---

### Task 4: ドキュメントを更新する

`docs/design.md` は「HEICは変換せず原本を配信する」を設計判断として明記し、今回の対応を「将来の拡張候補（初期スコープ外）」に置いている。実装が入った以上、両方とも事実と食い違う。

**Files:**
- Modify: `docs/design.md:37-39`（対象メディアのHEICの項）, `docs/design.md:44-47` あたり（借りるサムネイルの項に追記）, `docs/design.md:124`（将来の拡張候補から削除）
- Modify: `README.md:17`（対応フォーマット表）, `README.md:20-27`（Synology thumbnails の節）

**Interfaces:**
- Consumes: なし
- Produces: なし

- [ ] **Step 1: `docs/design.md` のHEICの項を書き換える**

37〜39行の3行（`- **HEIC**: ...` と `- **サムネイル生成**: ...` とその子項目）のうち、HEICの行を次で置き換える。サムネイル生成の行はそのまま残す（自前生成しない方針は変わっていない）。

```markdown
- **HEIC**: 自前ではデコードしない。原本はSafari以外のブラウザで表示できないため、
  `@eaDir` から借りられる場合は拡大表示に `SYNOPHOTO_THUMB_XL.jpg` を代わりに配信する
  （経緯は docs/superpowers/specs/2026-09-02-heic-full-view-design.md）
  - 借りられないHEICは従来どおり原本を配信する。見えないままだが、そのために自前の
    デコーダを抱える対価は目的に見合わない
```

- [ ] **Step 2: 「Synologyのサムネイルを借りる」の項にXLを足す**

「Mを使う。短辺320px……XLは長辺1707px・約1MBあり一覧には過大」の子項目の直後に、次の子項目を挿入する。

```markdown
  - 拡大表示にはXLを使う。一覧には過大な長辺1707pxも、1枚だけ見せる場面では妥当な
    大きさになる。XLの有無は記録も確認もしない。`thumb_source` が `eadir` ならMがあり、
    MとXLは同じ生成器が一緒に書くので、そこから導ける
```

- [ ] **Step 3: 「将来の拡張候補」から該当行を削除する**

`docs/design.md:124` の `- HEICのサーバー側サムネイル生成・JPEG変換配信` を削除する。JPEG変換配信は実装され、サーバー側サムネイル生成は「借りる」で代替されたため、候補として残す意味がない。

- [ ] **Step 4: `README.md` の対応フォーマット表を直す**

17行目を置き換える。

```markdown
| `.heic` `.heif` | borrowed from Synology if one is there | Synology's large JPEG is served in place of the original, so these display outside Safari too. With nothing to borrow the original is served as-is and only Safari shows it |
```

- [ ] **Step 5: `README.md` の Synology thumbnails の節に段落を足す**

「Nothing is copied, decoding is skipped entirely, ...」の段落と「`@eaDir` is only ever read.」の行のあいだに挿入する。

```markdown
Enlarging a HEIC serves `SYNOPHOTO_THUMB_XL.jpg` (1707px on the long edge) from that same
directory instead of the original. famifo cannot decode HEIC and no browser but Safari will
display it, so the borrowed JPEG is what makes those photos viewable on Android and on a PC.
Safari gives up some resolution in exchange. A HEIC with nothing to borrow still gets its
original.
```

- [ ] **Step 6: 差分を読み返す**

Run: `git diff`
Expected: `docs/design.md` と `README.md` だけが変わっている。design.md に「変換せず原本を配信する」「Safari以外で表示できなくても許容する」という記述が残っていないこと

Run: `grep -n "許容" docs/design.md`
Expected: HEICについての「許容」の記述が出てこない

- [ ] **Step 7: コミット**

```bash
git add docs/design.md README.md
git commit -m "docs: record that HEIC now borrows Synology's XL for the full view"
```

---

### Task 5: 実機で確認する

自動テストはパスの組み立てとHTTPの分岐までしか見ていない。SynologyのXLがEXIFの回転を焼き込み済みかどうかは、実物のJPEGを見ないと分からない。焼き込まれていなければ縦位置の写真が横倒しで出るので、その場合は設計に戻る。

**Files:**
- なし（コード変更なし）

**Interfaces:**
- Consumes: Task 3 の成果物
- Produces: なし

- [ ] **Step 1: ビルドして写真のあるマシンで起動する**

```bash
make build
./famifo-proto -dir /path/to/photos -data /path/to/data
```

- [ ] **Step 2: PCのブラウザ（Chrome か Firefox）で HEIC を開く**

一覧から HEIC のタイルをタップして拡大する。
Expected: これまで空だったライトボックスに画像が出る

- [ ] **Step 3: Androidで同じことを確認する**

同じLAN上のAndroid端末から `http://<ホスト>:8080` を開き、HEICを拡大する。
Expected: 画像が出る

- [ ] **Step 4: 縦位置のHEICで向きを確認する**

縦に構えて撮った HEIC を1枚選んで拡大する。
Expected: 縦向きのまま表示される。横倒しや上下逆になっていたら、XLに回転が焼き込まれていないということなので、**そこで止めて報告する**。借りたJPEGを配信時に回転させる必要が出て、サムネイル生成で避けたはずのデコードに戻るため、設計をやり直す判断が要る

- [ ] **Step 5: 借りるものが無いHEICの挙動を確認する**

`@eaDir` にサムネイルが無い HEIC があれば、それを拡大する。無ければこの手順は飛ばす。
Expected: 変更前と同じく表示されない（原本が配信されている）。500 や 404 でページが壊れることはない

- [ ] **Step 6: 結果を報告する**

確認できたこと・できなかったことをそのまま伝える。Step 4 に問題があった場合はここで作業を止める。
