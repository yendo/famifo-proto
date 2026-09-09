# 動画対応 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `.mp4` と `.mov` を写真と同じ1件としてギャラリーに載せ、Synology が作った
サムネイルと H.264 変換版を借りて表示・再生できるようにする。

**Architecture:** famifo は動画をデコードできない（cgo 無しでは実用デコーダが無い）ので、
自分では絵も変換版も作らない。一覧のタイルは `@eaDir/<動画名>/SYNOPHOTO_THUMB_M.jpg` を
借り、拡大表示は HEVC の原本ではなく `SYNOPHOTO_FILM_H.mp4`（H.264）があればそれを配る。
借りられなければ絵の無いタイルに再生の印を重ね、原本を配って再生失敗時に文を出す。
撮影日時は新パッケージ `internal/index/videometa` がコンテナの箱をたどって読む。

**Tech Stack:** Go 1.27.1、標準ライブラリのみ（新しい依存は追加しない）、SQLite
(modernc.org/sqlite)、chromedp（ブラウザテスト）、testify。

**Spec:** `docs/superpowers/specs/2026-09-09-video-support-design.md`

## Global Constraints

- **cgo を使わない。** ビルドは `CGO_ENABLED=0 go build -o famifo-proto .`
- **外部プロセスを呼ばない。** ffmpeg も ffprobe も使わない
- **新しい依存を追加しない。** `videometa` は標準ライブラリだけで書く
- **`@eaDir` は読むだけ。** 書き込みも削除もしない
- **対応拡張子は `.mp4` と `.mov` の2つだけ。** `.m4v` `.3gp` `.avi` `.webm` は足さない
- **借りるフィルムの名前は `SYNOPHOTO_FILM_H.mp4` だけ。** 他の名前は推測で並べない
- **再生時間は DB に持たない。** `<video controls>` がブラウザ側で表示する
- **スキーマ移行は書かない。** 表名を変えたら DB を消して作り直す
- **コメントは日本語。** 既存ファイルの流儀に合わせる
- **コミットメッセージは英語の1行。** 本文もトレーラーも付けない
- **ブランチ名は `feature-` で始めハイフンで区切る**
- **main へ直接コミットしない**

## ファイル構成

**Phase 1（ブランチ `feature-rename-photo-to-media`）**

| ファイル | 責務 |
|---|---|
| `internal/media/media.go`（旧 `internal/photo/photo.go`） | 1件の型と安定ID |
| `internal/media/takenat.go` | 撮影日時の決め方（変更なし、パッケージ名のみ） |
| `internal/store/store.go` | 表名 `media`、`scanMedia` |
| `internal/web/*.go` | `mediaView`、`lookupMedia`、`handleFile`、`/file/{id}` |
| `internal/thumb/thumb.go`、`internal/index/*` | 型名の追随 |

**Phase 2（ブランチ `feature-video-support`）**

| ファイル | 責務 |
|---|---|
| `internal/index/videometa/videometa.go` | 新規。コンテナの箱をたどって撮影日時を読む |
| `internal/index/videometa/videometa_test.go` | 新規。箱ツリーを手で組んだテスト |
| `internal/imagefmt/imagefmt.go` | `.mp4`/`.mov` の行と `IsVideo` |
| `internal/synology/synology.go` | `FilmPath` と `HasFilm` |
| `internal/thumb/thumb.go` | `LargePath` の動画枝 |
| `internal/index/indexer.go` | `IsVideo` で `videometa` と `exif` を切り替える |
| `internal/web/view.go` | `mediaView.IsVideo` |
| `internal/web/templates/tiles.html` | `data-video` |
| `internal/web/templates/gallery.html` | `<video>`、`.lb-error`、助数詞 |
| `internal/web/static/app.css` | 再生の印、動画時の `.lb-nav` の逃がし |
| `internal/web/static/app.js` | `isVideoAt`、拡大表示の切り替え、後始末 |
| `README.md`、`main.go` | 対応形式の記述 |

---

# Phase 1: `photo` → `media` の改名

ブランチ `feature-rename-photo-to-media`（設計文書のコミット 9af0f72 が既にある）。
振る舞いは1つも変えない。

### Task 1: `internal/photo` を `internal/media` に改名する

**Files:**
- Rename: `internal/photo/` → `internal/media/`、`photo.go` → `media.go`、`photo_test.go` → `media_test.go`
- Modify: `internal/store/store.go`、`internal/store/store_test.go`
- Modify: `internal/web/handlers.go`、`internal/web/view.go`、`internal/web/server.go`
- Modify: `internal/web/handlers_test.go`、`internal/web/gallery_test.go`、`internal/web/browser_test.go`
- Modify: `internal/thumb/thumb.go`、`internal/thumb/thumb_test.go`、`internal/thumb/paths_test.go`
- Modify: `internal/index/indexer.go`、`internal/index/indexer_test.go`
- Modify: `internal/index/exif/exif.go`、`internal/index/exif/exif_test.go`（コメントの言及のみ）

**Interfaces:**
- Consumes: なし
- Produces: `media.Media`、`media.New(path string, fi fs.FileInfo, exifTakenAt time.Time) media.Media`、
  `media.Restore(path string, takenAt, modTime time.Time) media.Media`、`media.IDFor(path string) string`、
  `(media.Media).ID/Path/TakenAt/ModTime`。DB の表 `media`、索引 `idx_media_order`。
  配信 URL `/file/{id}`。`web.mediaView`

**改名の対応表（これがすべて。これ以外は変えない）**

| 変更前 | 変更後 |
|---|---|
| ディレクトリ `internal/photo` | `internal/media` |
| ファイル `photo.go` / `photo_test.go` | `media.go` / `media_test.go` |
| パッケージ `photo` / `photo_test` | `media` / `media_test` |
| 型 `photo.Photo` | `media.Media` |
| SQL の表 `photos` | `media` |
| 索引 `idx_photos_order` | `idx_media_order` |
| `store.scanPhoto` | `store.scanMedia` |
| `web.photoView` | `web.mediaView` |
| `web.lookupPhoto` | `web.lookupMedia` |
| `web.handlePhoto` | `web.handleFile` |
| `web.noOpenPhoto` | `web.noOpenItem` |
| ルート `GET /photo/{id}` | `GET /file/{id}` |
| `view.go` の `"/photo/" + p.ID()` | `"/file/" + m.ID()` |
| レシーバ/引数の `p photo.Photo` | `m media.Media` |

**テスト内のローカルなヘルパー名は変えない。** `photoOf`（thumb）、`photoAt`（store）、
`addPhoto`（thumb/web）などはそのままにする。改名の差分を型名と公開名に絞り、
レビューできる大きさに保つため。

**コメントの扱い:** 型名・表名・URL を名指ししている箇所だけ直す。「写真」という語が
概念を指している箇所は Phase 1 では触らない（動画も含むようになるのは Phase 2 で、
そのとき該当タスクが直す）。

- [ ] **Step 1: ブランチを確認する**

```bash
git rev-parse --abbrev-ref HEAD   # feature-rename-photo-to-media であること
git status --short                # docs 以外に変更が無いこと
```

- [ ] **Step 2: 改名前のテストが通ることを確かめる**

Run: `make unit-test`
Expected: PASS（改名の前後で結果が変わらないことを確認するための基準）

- [ ] **Step 3: ディレクトリとファイルを移動する**

```bash
git mv internal/photo internal/media
git mv internal/media/photo.go internal/media/media.go
git mv internal/media/photo_test.go internal/media/media_test.go
```

- [ ] **Step 4: 識別子を一括で置換する**

```bash
files=$(git ls-files '*.go')
sed -i \
  -e 's/internal\/photo/internal\/media/g' \
  -e 's/\bphoto\.Photo\b/media.Media/g' \
  -e 's/\bphoto\.New\b/media.New/g' \
  -e 's/\bphoto\.Restore\b/media.Restore/g' \
  -e 's/\bphoto\.IDFor\b/media.IDFor/g' \
  -e 's/^package photo$/package media/' \
  -e 's/^package photo_test$/package media_test/' \
  -e 's/\bscanPhoto\b/scanMedia/g' \
  -e 's/\bphotoView\b/mediaView/g' \
  -e 's/\blookupPhoto\b/lookupMedia/g' \
  -e 's/\bhandlePhoto\b/handleFile/g' \
  -e 's/\bnoOpenPhoto\b/noOpenItem/g' \
  $files
```

`sed` はここまで。表名・URL・型の宣言・コメントは次のステップで手で直す。機械的な
置換で壊れやすいためである。

- [ ] **Step 5: `internal/media/media.go` の型宣言とコメントを直す**

`Photo` 型を `Media` に改める。パッケージコメントの1行目も直す。

```go
// Package media はインデックス上の1件を表す。型と安定ID（media.go）、
// 撮影日時の決め方（takenat.go）。
//
// IDの導出規則はここにしかない。組み立ては New と Restore を通す。
// 呼び出し側が同じ式を書き直すと規則が二重化するため。
//
// 対応する形式の表は internal/imagefmt が、表示用に派生した画像の置き場所と
// 選択は internal/thumb が持つ。I/Oは一切行わない。
package media

// Media はインデックス上の1件。インデックスの1行に対応し、ファイルが
// 差し替わって中身もmtimeも変わっても、同じパスであれば同じ1件として追跡される。
type Media struct {
	id      string    // パスから導出した安定ID。URLに露出させる
	path    string    // ディスク上の絶対パス
	takenAt time.Time // EXIF撮影日時、無ければmtime
	modTime time.Time // ファイルのmtime。再スキャン時の変更検知に使う
}
```

`New` と `Restore` の戻り値の型、`Photo{}` のゼロ値リテラル、メソッドのレシーバ
`(p Photo)` を `(m Media)` に直す。

- [ ] **Step 6: `internal/store/store.go` の表名を直す**

`schema` 定数、`upsertSQL`、`probeReadable`、`GetByID`、`DeleteByPath`、
`DeleteByPathPrefix`、`ListRange`、`RankOf`、`AllPaths`、`Count`、`DayGroups` の
SQL に出てくる `photos` を `media` に、`idx_photos_order` を `idx_media_order` に直す。

```go
const schema = `
CREATE TABLE IF NOT EXISTS media (
    id        TEXT PRIMARY KEY,
    path      TEXT NOT NULL UNIQUE,
    taken_at  INTEGER NOT NULL,
    mod_time  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_order ON media(taken_at DESC, id DESC);
`
```

- [ ] **Step 7: 配信URLを `/file/{id}` に直す**

`internal/web/server.go`:

```go
	mux.HandleFunc("GET /file/{id}", s.handleFile)
```

`internal/web/view.go`:

```go
			FullURL:  "/file/" + m.ID(),
```

テストの中の `"/photo/"` も `"/file/"` に直す（`gallery_test.go`、`handlers_test.go`、
`browser_test.go`）。

```bash
sed -i 's|"/photo/|"/file/|g; s|/photo/|/file/|g' $(git ls-files '*_test.go')
```

- [ ] **Step 8: ビルドと vet を通す**

Run: `go build ./... && make vet`
Expected: エラーなし。残った `photo` の参照はここで露見する

```bash
grep -rn '\bphoto\b' --include=*.go . | grep -v '写真'
```
Expected: 出力なし（識別子としての `photo` が残っていないこと）

- [ ] **Step 9: テストを走らせる**

Run: `make unit-test`
Expected: PASS。Step 2 と同じ結果になること

- [ ] **Step 10: コミットする**

```bash
git add -A
git commit -m "refactor: Rename the photo package to media"
```

---

# Phase 2: 動画対応

ブランチ `feature-video-support`。PR 1 がマージされたら main から切る。
待たずに進めるなら `feature-rename-photo-to-media` から切る。

```bash
git checkout -b feature-video-support
```

### Task 2: `videometa` がコンテナから撮影日時を読む

**Files:**
- Create: `internal/index/videometa/videometa.go`
- Test: `internal/index/videometa/videometa_test.go`

**Interfaces:**
- Consumes: なし（標準ライブラリのみ）
- Produces: `videometa.Meta{TakenAt time.Time}`、`videometa.Read(path string) videometa.Meta`

**時差の規約（spec の「調査で分かったこと」より）**
- `ftyp` が `qt  ` でない（`isom` 等）→ `mvhd` は UTC。Pixel で実測
- `ftyp` が `qt  ` で `com.apple.quicktime.creationdate` がある → その時差付きの値。iPhone（未検証）
- `ftyp` が `qt  ` でキーが無い → `mvhd` は壁掛け時計の時刻。Canon S95 で実測

- [ ] **Step 1: 失敗するテストを書く**

`internal/index/videometa/videometa_test.go` を作る。

```go
package videometa_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/index/videometa"
)

// epochOffset は1904-01-01から1970-01-01までの秒数。
const epochOffset = 2082844800

// bx は1つの箱を組み立てる。テストが読むのはヘッダとペイロードだけなので、
// 32bitサイズで足りる。
func bx(typ string, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

// ftyp はメジャーブランドと互換ブランド1つを持つ ftyp を作る。
func ftyp(major, compat string) []byte {
	b := make([]byte, 0, 12)
	b = append(b, major...)
	b = append(b, 0, 0, 2, 0) // minor version
	b = append(b, compat...)
	return bx("ftyp", b)
}

// mvhd は version 0 の mvhd を作る。creation_time は1904年起点の秒。
func mvhd(raw uint32) []byte {
	p := make([]byte, 100)
	binary.BigEndian.PutUint32(p[4:8], raw)
	return bx("mvhd", p)
}

// raw1904 は「その壁掛け時計の時刻をUTCとみなした値」を1904年起点に直す。
func raw1904(y int, mo time.Month, d, h, mi, s int) uint32 {
	return uint32(time.Date(y, mo, d, h, mi, s, 0, time.UTC).Unix() + epochOffset)
}

// mdat64 は size==1 の64bit拡張サイズを使う mdat を作る。
func mdat64(payload int) []byte {
	out := make([]byte, 16+payload)
	binary.BigEndian.PutUint32(out[:4], 1)
	copy(out[4:8], "mdat")
	binary.BigEndian.PutUint64(out[8:16], uint64(16+payload))
	return out
}

// appleMeta は com.apple.quicktime.creationdate を持つ QuickTime 形式の meta を作る。
// QuickTimeの meta はFullBoxではなく、ペイロードが直接 hdlr から始まる。
func appleMeta(value string) []byte {
	hdlr := bx("hdlr", make([]byte, 24))

	name := "com.apple.quicktime.creationdate"
	entry := make([]byte, 8, 8+len(name))
	binary.BigEndian.PutUint32(entry[:4], uint32(8+len(name)))
	copy(entry[4:8], "mdta")
	entry = append(entry, name...)

	keysBody := make([]byte, 8)
	binary.BigEndian.PutUint32(keysBody[4:8], 1) // entry_count
	keys := bx("keys", keysBody, entry)

	data := bx("data", []byte{0, 0, 0, 1, 0, 0, 0, 0}, []byte(value))
	item := bx("\x00\x00\x00\x01", data) // ilst の項目名は keys の1始まりの索引
	ilst := bx("ilst", item)

	return bx("meta", hdlr, keys, ilst)
}

func write(t *testing.T, name string, parts ...[]byte) string {
	t.Helper()
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, b, 0o644))
	return path
}

// isom は仕様どおり mvhd に UTC を書く。Pixel 7a がこれである。
func TestReadTreatsIsoBrandAsUTC(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mp4",
		ftyp("isom", "mp41"),
		bx("moov", mvhd(raw1904(2026, 9, 9, 9, 39, 6))),
	)
	got := videometa.Read(path).TakenAt
	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// QuickTimeブランドでAppleのキーが無ければ、mvhd は壁掛け時計の時刻である。
// Canon PowerShot S95 がこれで、同じファイル内のEXIFと一致することを確認済み。
func TestReadTreatsQuickTimeWithoutAppleKeyAsLocalTime(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mov",
		ftyp("qt  ", "qt  "),
		bx("moov", mvhd(raw1904(2011, 1, 11, 20, 5, 54))),
	)
	got := videometa.Read(path).TakenAt
	require.True(t, got.Equal(time.Date(2011, 1, 11, 20, 5, 54, 0, time.Local)), "got %v", got)
}

// Appleのキーがあればそれがmvhdよりも優先される。時差を持つ唯一の出どころである。
func TestReadPrefersTheAppleCreationDate(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mov",
		ftyp("qt  ", "qt  "),
		bx("moov",
			mvhd(raw1904(2026, 9, 9, 9, 39, 6)),
			appleMeta("2026-09-09T18:39:06+0900"),
		),
	)
	got := videometa.Read(path).TakenAt
	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// moov が末尾にあり mdat が64bit拡張サイズでも読めること。Pixel の mp4 がこの配置で、
// mdat をサイズで飛ばせないと moov に到達しない。
func TestReadFindsMoovAfterA64BitMdat(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mp4",
		ftyp("isom", "mp41"),
		mdat64(4096),
		bx("moov", mvhd(raw1904(2026, 9, 9, 9, 39, 6))),
	)
	got := videometa.Read(path).TakenAt
	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// 読めなかったときはゼロ値を返す。呼び出し側がmtimeに落ちる。
func TestReadReturnsZeroWhenNothingIsReadable(t *testing.T) {
	t.Parallel()
	tests := map[string][]byte{
		"creation_timeが0": append(ftyp("isom", "mp41"), bx("moov", mvhd(0))...),
		"moovが無い":         ftyp("isom", "mp41"),
		"ftypが無い":         bx("moov", mvhd(0)),
		"途中で切れている":        append(ftyp("isom", "mp41"), []byte{0, 0, 1, 0, 'm', 'o'}...),
		"空":              {},
		"箱ではない":          []byte("this is not a container at all"),
		"サイズが過小":         append(ftyp("isom", "mp41"), []byte{0, 0, 0, 2, 'm', 'o', 'o', 'v'}...),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := write(t, "a.mp4", body)
			require.True(t, videometa.Read(path).TakenAt.IsZero())
		})
	}
}

// 開けないパスでも失敗せずゼロ値を返す。
func TestReadReturnsZeroForAMissingFile(t *testing.T) {
	t.Parallel()
	require.True(t, videometa.Read(filepath.Join(t.TempDir(), "nope.mp4")).TakenAt.IsZero())
}
```

- [ ] **Step 2: テストが失敗することを確かめる**

Run: `go test ./internal/index/videometa/ -v`
Expected: FAIL。`no required module provides package .../internal/index/videometa`

- [ ] **Step 3: `videometa.go` を書く**

`internal/index/videometa/videometa.go` を作る。

```go
// Package videometa は動画のコンテナ（ISO base media file format / QuickTime）から
// 撮影日時を読む。読めなくても失敗せず、パニックもしない。
//
// exif と対になる位置に置いてあるが、読む相手が違う。静止画のEXIFは機種によらず
// 時差を持たないローカル時刻だが、動画は機種によって規約が割れる。同じ Pixel 7a が、
// 写真ではEXIFにローカル時刻を書き、動画では mvhd に UTC を書く。
//
// 規約の見分け方は ftyp のブランドである。
//
//   - qt 以外（isom等）  : mvhd は UTC。Pixel 7a で実測
//   - qt + Appleのキー   : com.apple.quicktime.creationdate が時差を持つ。iPhone（未検証）
//   - qt + キー無し      : mvhd は壁掛け時計の時刻。Canon PowerShot S95 で実測
//
// 外部ライブラリは使わない。長時間動くデーモンなので、壊れた入力で落ちないことを
// 自分で保証する。
package videometa

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"time"
)

// Meta は動画1本から読み取れた情報。読めなかった項目は既定値になる。
type Meta struct {
	// TakenAt は撮影日時。読めなければゼロ値。
	// 呼び出し側はゼロ値をmtimeに落とす（EXIFが無い写真と同じ扱い）。
	TakenAt time.Time
}

// epochOffset は1904-01-01から1970-01-01までの秒数。コンテナの時刻はこの起点で入る。
const epochOffset = 2082844800

// qtBrand はQuickTimeを表すブランド。末尾の2つは空白である。
const qtBrand = "qt  "

// appleCreationDateKey はAppleが時差付きの撮影日時を入れるキー。
const appleCreationDateKey = "com.apple.quicktime.creationdate"

// 壊れた入力で無限に歩き回らないための上限。
const (
	maxDepth    = 8
	maxBoxes    = 4096
	maxMoovSize = 32 << 20 // moovが32MBを超えるファイルは相手にしない
	maxFtypSize = 1024
	maxKeys     = 256
	maxDataSize = 4096
)

// Read は path のコンテナから撮影日時を読む。
//
// 「Readは絶対に失敗しない」という契約を、あらゆる入力に対して構造的に保証する
// ためのガード。imagemeta を使う exif と違い自前のパーサだが、境界の読み違いで
// パニックしうる点は同じである。
func Read(path string) (m Meta) {
	defer func() {
		if recover() != nil {
			m = Meta{}
		}
	}()

	f, err := os.Open(path)
	if err != nil {
		return Meta{}
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return Meta{}
	}

	var quicktime bool
	var creation uint64
	var appleDate string

	walk(f, 0, fi.Size(), 0, func(b box) bool {
		switch b.typ {
		case "ftyp":
			quicktime = isQuickTime(f, b)
		case "moov":
			if b.end-b.start <= maxMoovSize {
				creation, appleDate = readMoov(f, b)
			}
			return false // moovを読み終えたら以降は要らない
		}
		return true
	})

	return Meta{TakenAt: resolve(quicktime, creation, appleDate)}
}

// box は1つの箱のペイロードの範囲。
type box struct {
	typ        string
	start, end int64
}

// walk は [start,end) の直下にある箱を順に fn へ渡す。fn が false を返すと打ち切る。
//
// 壊れた入力ではその場で打ち切る。エラーを返さないのは、途中まで読めた値を
// 捨てないためである（mvhd の後ろが壊れていても撮影日時は取れている）。
func walk(r io.ReaderAt, start, end int64, depth int, fn func(box) bool) {
	if depth > maxDepth {
		return
	}
	pos := start
	for n := 0; pos+8 <= end && n < maxBoxes; n++ {
		var hdr [8]byte
		if _, err := r.ReadAt(hdr[:], pos); err != nil {
			return
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		head := int64(8)

		switch size {
		case 1:
			// 64bit拡張サイズ。mdat が4GBを超えなくても使われる（Pixelの mp4 がそう）。
			var ext [8]byte
			if _, err := r.ReadAt(ext[:], pos+8); err != nil {
				return
			}
			size = int64(binary.BigEndian.Uint64(ext[:]))
			head = 16
		case 0:
			size = end - pos // 最後の箱は「残り全部」を意味する
		}
		if size < head || pos+size > end {
			return // 壊れている
		}
		if !fn(box{typ: typ, start: pos + head, end: pos + size}) {
			return
		}
		pos += size
	}
}

// isQuickTime は ftyp のメジャーブランドか互換ブランドに qt が含まれるかを返す。
func isQuickTime(r io.ReaderAt, b box) bool {
	n := b.end - b.start
	if n < 8 || n > maxFtypSize {
		return false
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, b.start); err != nil {
		return false
	}
	if string(buf[:4]) == qtBrand {
		return true
	}
	// 先頭4バイトがメジャーブランド、次の4バイトがマイナーバージョン、以降が互換ブランド。
	for i := 8; i+4 <= len(buf); i += 4 {
		if string(buf[i:i+4]) == qtBrand {
			return true
		}
	}
	return false
}

// readMoov は moov から mvhd の creation_time と Apple のキーの値を拾う。
//
// meta は moov の直下にある場合と udta の下にある場合の両方があるため、両方を見る。
func readMoov(r io.ReaderAt, moov box) (creation uint64, appleDate string) {
	walk(r, moov.start, moov.end, 1, func(b box) bool {
		switch b.typ {
		case "mvhd":
			creation = readCreationTime(r, b)
		case "meta":
			if s := readAppleDate(r, b); s != "" {
				appleDate = s
			}
		case "udta":
			walk(r, b.start, b.end, 2, func(c box) bool {
				if c.typ == "meta" {
					if s := readAppleDate(r, c); s != "" {
						appleDate = s
					}
				}
				return true
			})
		}
		return true
	})
	return creation, appleDate
}

// readCreationTime は mvhd の creation_time を返す。
// version 0 は32bit、version 1 は64bit。
func readCreationTime(r io.ReaderAt, b box) uint64 {
	var head [4]byte
	if _, err := r.ReadAt(head[:], b.start); err != nil {
		return 0
	}
	switch head[0] {
	case 0:
		var v [4]byte
		if _, err := r.ReadAt(v[:], b.start+4); err != nil {
			return 0
		}
		return uint64(binary.BigEndian.Uint32(v[:]))
	case 1:
		var v [8]byte
		if _, err := r.ReadAt(v[:], b.start+4); err != nil {
			return 0
		}
		return binary.BigEndian.Uint64(v[:])
	}
	return 0
}

// metaPayloadStart は meta のペイロードが始まる位置を返す。
//
// ISOの meta はFullBoxでversion/flagsの4バイトを持つが、QuickTimeの meta は持たず
// 直接 hdlr から始まる。Pixel の mp4 は isom ブランドなのにQuickTime形式で書いていた
// ため、ブランドでは判別できない。最初の子が hdlr であることを手掛かりにする。
func metaPayloadStart(r io.ReaderAt, b box) int64 {
	var buf [12]byte
	if _, err := r.ReadAt(buf[:], b.start); err != nil {
		return b.start
	}
	if string(buf[4:8]) == "hdlr" {
		return b.start
	}
	return b.start + 4
}

// readAppleDate は meta から com.apple.quicktime.creationdate の値を返す。
// 無ければ空文字。
func readAppleDate(r io.ReaderAt, meta box) string {
	var keys []string
	var ilst box
	var haveIlst bool

	walk(r, metaPayloadStart(r, meta), meta.end, 3, func(b box) bool {
		switch b.typ {
		case "keys":
			keys = readKeys(r, b)
		case "ilst":
			ilst, haveIlst = b, true
		}
		return true
	})
	if !haveIlst {
		return ""
	}

	// ilst の項目名は4バイトの整数で、keys の1始まりの索引を指す。
	want := -1
	for i, k := range keys {
		if k == appleCreationDateKey {
			want = i + 1
			break
		}
	}
	if want < 0 {
		return ""
	}

	var out string
	walk(r, ilst.start, ilst.end, 4, func(b box) bool {
		if len(b.typ) != 4 || int(binary.BigEndian.Uint32([]byte(b.typ))) != want {
			return true
		}
		out = readDataString(r, b)
		return false
	})
	return out
}

// readKeys は keys のエントリ名を並び順に返す。
func readKeys(r io.ReaderAt, b box) []string {
	var head [8]byte
	if _, err := r.ReadAt(head[:], b.start); err != nil {
		return nil
	}
	n := int(binary.BigEndian.Uint32(head[4:8]))
	if n <= 0 || n > maxKeys {
		return nil
	}

	out := make([]string, 0, n)
	pos := b.start + 8
	for i := 0; i < n && pos+8 <= b.end; i++ {
		var eh [8]byte
		if _, err := r.ReadAt(eh[:], pos); err != nil {
			return out
		}
		size := int64(binary.BigEndian.Uint32(eh[:4]))
		if size < 8 || pos+size > b.end {
			return out
		}
		name := make([]byte, size-8)
		if _, err := r.ReadAt(name, pos+8); err != nil {
			return out
		}
		out = append(out, string(name))
		pos += size
	}
	return out
}

// readDataString は ilst の項目が持つ data の中身を文字列として返す。
// data の先頭8バイトは型と言語なので読み飛ばす。
func readDataString(r io.ReaderAt, item box) string {
	var out string
	walk(r, item.start, item.end, 5, func(b box) bool {
		if b.typ != "data" {
			return true
		}
		n := b.end - b.start
		if n <= 8 || n > maxDataSize {
			return false
		}
		buf := make([]byte, n-8)
		if _, err := r.ReadAt(buf, b.start+8); err != nil {
			return false
		}
		out = string(buf)
		return false
	})
	return out
}

// resolve は読み取った材料から撮影日時を決める。
func resolve(quicktime bool, creation uint64, appleDate string) time.Time {
	if t, ok := parseAppleDate(appleDate); ok {
		return t
	}
	if creation == 0 {
		return time.Time{}
	}
	secs := int64(creation) - epochOffset
	if secs < 0 {
		return time.Time{} // 1970年より前は壊れた値として捨てる
	}
	if quicktime {
		// QuickTimeの古い機種は壁掛け時計の時刻を書く。UTCとして読むと時差ぶんずれる。
		u := time.Unix(secs, 0).UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(), 0, time.Local)
	}
	return time.Unix(secs, 0)
}

// parseAppleDate は "2026-09-09T18:39:06+0900" 形式を解く。
// 時差の書き方は機種で揺れるので、コロンの有無とZの両方を受ける。
func parseAppleDate(s string) (time.Time, bool) {
	s = strings.TrimRight(s, "\x00")
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil && t.Unix() >= 0 {
			return t, true
		}
	}
	return time.Time{}, false
}
```

- [ ] **Step 4: テストが通ることを確かめる**

Run: `go test ./internal/index/videometa/ -v`
Expected: PASS（すべてのケース）

- [ ] **Step 5: 手元の実ファイルで確かめる**

合成データだけでは規約の読み違いに気づけない。実ファイルで確かめる。一時的な
テストを置いて走らせ、確認したら消す（実ファイルはリポジトリに入れない）。

```bash
cat > internal/index/videometa/manual_check_test.go <<'EOF'
package videometa_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yendo/famifo-proto/internal/index/videometa"
)

func TestManualRealFiles(t *testing.T) {
	for _, name := range []string{"PXL_20260909_093855665.TS.mp4", "MVI_0010.MOV"} {
		p := filepath.Join(os.Getenv("HOME"), "Pictures", name)
		t.Log(name, videometa.Read(p).TakenAt)
	}
}
EOF
go test ./internal/index/videometa/ -run TestManualRealFiles -v
rm internal/index/videometa/manual_check_test.go
```

Expected（JST の環境の場合）:

```
PXL_20260909_093855665.TS.mp4 2026-09-09 18:39:06 +0900 JST
MVI_0010.MOV 2011-01-11 20:05:54 +0900 JST
```

Pixel が `09:39:06` と出たら UTC の変換が抜けている。S95 が `2011-01-12 05:05:54` と
出たら QuickTime の枝に入っていない。

- [ ] **Step 6: コミットする**

```bash
git add internal/index/videometa
git commit -m "feat: Read the capture time from video containers"
```

### Task 3: `synology` が変換済み動画のパスを持つ

**Files:**
- Modify: `internal/synology/synology.go`
- Test: `internal/synology/synology_test.go`

**Interfaces:**
- Consumes: `entryPath`（既存の非公開関数）
- Produces: `synology.FilmPath(srcPath string) string`、`synology.HasFilm(srcPath string) bool`

- [ ] **Step 1: 失敗するテストを書く**

`internal/synology/synology_test.go` に足す。

```go
func TestFilmPathPointsInsideEaDir(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		filepath.Join("/photos", "@eaDir", "clip.mp4", "SYNOPHOTO_FILM_H.mp4"),
		synology.FilmPath("/photos/clip.mp4"))
}

// DSMは変換に失敗すると0バイトの .fail を置く。サムネイルと違って変換は
// 別の工程なので、サムネイルの有無からは導けず自分で確かめる必要がある。
func TestHasFilmRequiresARegularNonEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "clip.mp4")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o644))

	require.False(t, synology.HasFilm(src), "無いとき")

	entry := filepath.Join(dir, "@eaDir", "clip.mp4")
	require.NoError(t, os.MkdirAll(entry, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(entry, "SYNOPHOTO_FILM_H.mp4"), nil, 0o644))
	require.False(t, synology.HasFilm(src), "0バイトのとき")

	require.NoError(t, os.WriteFile(filepath.Join(entry, "SYNOPHOTO_FILM_H.mp4"), []byte("mp4"), 0o644))
	require.True(t, synology.HasFilm(src), "中身があるとき")
}
```

- [ ] **Step 2: テストが失敗することを確かめる**

Run: `go test ./internal/synology/ -run TestFilm -v`
Expected: FAIL。`undefined: synology.FilmPath`

- [ ] **Step 3: 実装する**

`internal/synology/synology.go` の `thumbXLName` の下に足す。

```go
// filmName はブラウザ互換のために借りる変換済み動画のファイル名。
//
// Pixel も iPhone も HEVC を吐くが、HEVCを再生できるかは端末にハードウェア
// デコーダがあるかで決まる。DSMが作るこのH.264版（実測1280x720・3.6MB、原本は
// HEVCで15.9MB）を配ればどの端末でも再生できる。HEICで SYNOPHOTO_THUMB_XL.jpg を
// 借りているのと同じ構えである。
//
// 他の解像度の変換版がある可能性はあるが、実物を1本しか見ていないので推測で
// 名前を並べない。この名前が無ければ借りるのをやめて原本に落ちる。
const filmName = "SYNOPHOTO_FILM_H.mp4"

// FilmPath はSynologyがsrcPathの動画から作った変換版のパスを返す。
// 実在するとは限らない。あるかどうかは HasFilm で確かめる。
func FilmPath(srcPath string) string { return entryPath(srcPath, filmName) }

// HasFilm は借りられる変換版があるかを報告する。
//
// サムネイルの有無からは導けない。実測した @eaDir には SYNOPHOTO_THUMB_M.jpg が
// あるのに SYNOPHOTO_FILM.fail があり、サムネイル生成と動画変換が別の工程で
// 片方だけ失敗しうることが分かる。DSMは失敗すると0バイトの .fail を置くので、
// 中身があることまで確かめる。
func HasFilm(srcPath string) bool {
	fi, err := os.Stat(FilmPath(srcPath))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}
```

- [ ] **Step 4: テストが通ることを確かめる**

Run: `go test ./internal/synology/ -v`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/synology
git commit -m "feat: Locate the transcoded video Synology keeps in @eaDir"
```

### Task 4: `imagefmt` が動画を対応形式として認める

このタスクで動画がインデックスに載り始める。`SmallPath` は既に `HasThumbM` を
先に見るので、`@eaDir` があるライブラリではこの時点でタイルに絵が出る。

**Files:**
- Modify: `internal/imagefmt/imagefmt.go`
- Test: `internal/imagefmt/imagefmt_test.go`
- Modify: `internal/index/scan_test.go`（`TestScanIgnoresNonPhotos`）
- Modify: `internal/index/indexer_test.go`（`TestIndexFileIgnoresUnsupportedExtensions`）

**Interfaces:**
- Consumes: なし
- Produces: `imagefmt.IsVideo(name string) bool`。`.mp4`/`.mov` に対する
  `IsSupported`=true、`IsDecodable`=false、`ContentType`=`video/mp4`/`video/quicktime`

- [ ] **Step 1: 失敗するテストを書く**

`internal/imagefmt/imagefmt_test.go` の表に3つ目の問いを足し、動画の行を直す。

```go
// 拡張子ごとに3つの問いへの答えを固定する。「インデックスに載せるか」
// 「自前でサムネイルを作れるか」「動画か」は独立している。HEICは載せるが作れず、
// 動画は載せるが作れず、しかも動画である。
func TestSupportedDecodableAndVideoByExtension(t *testing.T) {
	t.Parallel()
	tests := map[string]struct{ supported, decodable, video bool }{
		"a.jpg":              {true, true, false},
		"a.jpeg":             {true, true, false},
		"a.png":              {true, true, false},
		"a.gif":              {true, true, false},
		"a.webp":             {true, true, false},
		"A.JPG":              {true, true, false},   // 大文字小文字を区別しない
		"a.heic":             {true, false, false},  // 載せるが、デコードは @eaDir 頼み
		"a.HEIF":             {true, false, false},
		"a.mp4":              {true, false, true},   // 載せるが、絵は借りるしかない
		"a.MOV":              {true, false, true},
		"a.avi":              {false, false, false}, // 実物を見るまで足さない
		"a.webm":             {false, false, false},
		"a.txt":              {false, false, false},
		"noext":              {false, false, false},
		"/photos/2020/b.png": {true, true, false},
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want.supported, imagefmt.IsSupported(name), "IsSupported")
			require.Equal(t, want.decodable, imagefmt.IsDecodable(name), "IsDecodable")
			require.Equal(t, want.video, imagefmt.IsVideo(name), "IsVideo")
		})
	}
}
```

`TestContentType` の表にも足す。

```go
		"a.mp4": "video/mp4",
		"a.mov": "video/quicktime",
```

- [ ] **Step 2: テストが失敗することを確かめる**

Run: `go test ./internal/imagefmt/ -v`
Expected: FAIL。`undefined: imagefmt.IsVideo`

- [ ] **Step 3: 表に2行足し、`IsVideo` を導出で書く**

`internal/imagefmt/imagefmt.go` の `supportedExts` に足す。

```go
	".heic": {"image/heic", false},
	".heif": {"image/heif", false},
	// 動画は decodable=false。cgo無しでは1フレームも取り出せないので、一覧の絵は
	// @eaDir から借りるしかない。借りられなければ配信側がプレースホルダに落とす。
	".mp4": {"video/mp4", false},
	".mov": {"video/quicktime", false},
```

パッケージコメントの「画像形式の表」を「扱う形式の表」に直し、`IsVideo` を足す。

```go
// IsVideo は動画かを報告する。表示が <img> か <video> かを決め、タイルに再生の印を
// 出すかを決める。
//
// 列を足さずMIMEから導く。同じ事実を表の2箇所に置くと片方だけ直す事故が起きる。
// 対象外の拡張子では mime が空文字なので偽になる。
func IsVideo(name string) bool {
	return strings.HasPrefix(supportedExts[ext(name)].mime, "video/")
}
```

- [ ] **Step 4: テストが通ることを確かめる**

Run: `go test ./internal/imagefmt/ -v`
Expected: PASS

- [ ] **Step 5: 動画が対象外である前提に立っていたテストを直す**

`internal/index/scan_test.go` の `TestScanIgnoresNonPhotos` から `clip.mp4` を外し、
まだ対象外の拡張子に替える。

```go
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "clip.avi"), []byte("x"), 0o644))
```

`internal/index/indexer_test.go` の `TestIndexFileIgnoresUnsupportedExtensions` も同様。

```go
	path := filepath.Join(f.root, "a.avi")
	require.NoError(t, os.WriteFile(path, []byte("video"), 0o644))
```

そのうえで、動画が載るようになったことを固定するテストを `indexer_test.go` に足す。

```go
// 動画は自前でサムネイルを作れないが、インデックスには載る。壊れていることは
// デコードして初めて分かるので、載せる前に判定する手段が無い。
func TestIndexFileIndexesVideosWithoutAThumbnail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := filepath.Join(f.root, "clip.mp4")
	require.NoError(t, os.WriteFile(path, []byte("not a real container"), 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), media.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, path, got.Path())
	require.Empty(t, f.generatedThumbs(t), "famifo never generates a thumbnail for a video")
}
```

- [ ] **Step 6: すべてのテストが通ることを確かめる**

Run: `make unit-test`
Expected: PASS

- [ ] **Step 7: コミットする**

```bash
git add internal/imagefmt internal/index
git commit -m "feat: Index mp4 and mov files alongside photos"
```

### Task 5: `@eaDir` の中の変換済み動画を取り込まないことを固定する

既存のガード（`IsManagedDir` / `InManagedDir`）が動画で決定的になる。無ければ
`SYNOPHOTO_FILM_H.mp4` が1件ずつ独立した動画として一覧に並ぶ。**コードは変えない。
テストだけを足す。**

**Files:**
- Test: `internal/index/scan_test.go`
- Test: `internal/index/watch_test.go`

**Interfaces:**
- Consumes: `imagefmt.IsVideo`（Task 4）
- Produces: なし

- [ ] **Step 1: 走査の側のテストを書く**

`internal/index/scan_test.go` に足す。

```go
// Synologyは動画の隣に SYNOPHOTO_FILM_H.mp4（H.264への変換版）を置く。拡張子は
// 対応形式そのものなので、@eaDir を降りないガードが無ければ動画1本につき偽物が
// 1件並ぶ。
func TestScanIgnoresTheTranscodedVideoInEaDir(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "clip.mp4"), []byte("x"), 0o644))

	entry := filepath.Join(f.root, "@eaDir", "clip.mp4")
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "SYNOPHOTO_FILM_H.mp4"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "SYNOPHOTO_THUMB_M.jpg"), []byte("x"), 0o644))

	stats, err := f.ix.Scan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed, "only the original is indexed")
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
```

- [ ] **Step 2: 監視の側のテストを書く**

`internal/index/watch_test.go` に足す。fsnotify のイベントは個々のパスで届くため、
走査とは別の経路のガードが要る。既存の監視テストの待ち方（ヘルパー名と待ち条件）に
そろえること。

```go
// fsnotify のイベントは @eaDir の中のファイルについても届く。走査とは別の経路なので
// 別に確かめる。既存の TestWatcherIgnoresSynologyThumbnailsCreatedLater の動画版である。
func TestWatcherIgnoresTheTranscodedVideoInEaDir(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	startWatcher(t, f)

	entry := filepath.Join(f.root, "@eaDir", "clip.mp4")
	require.NoError(t, os.MkdirAll(entry, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entry, "SYNOPHOTO_FILM_H.mp4"), []byte("x"), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(f.root, "clip.mp4"), []byte("x"), 0o644))

	requireCount(t, f, 1) // 原本だけが載る
}
```

**書き込み途中の取り込みには新しいテストを足さない。** 監視が最後の Write から2秒待つ
性質は拡張子に依らず、既存の `TestWatcherDebouncesRepeatedWrites` が既に固定している。
動画で効きが強くなるのは転送が長いからで、規則そのものは変わらない。

- [ ] **Step 3: テストが通ることを確かめる**

Run: `go test ./internal/index/ -run EaDir -v`
Expected: PASS（既存のガードが効いているので、実装を足さずに通る）

失敗した場合はガードが効いていないということなので、`synology.IsManagedDir` と
`InManagedDir` の呼ばれ方を確認する。

- [ ] **Step 4: コミットする**

```bash
git add internal/index
git commit -m "test: Pin that @eaDir transcodes never enter the index"
```

### Task 6: 取り込みが動画の撮影日時を読む

**Files:**
- Modify: `internal/index/indexer.go`
- Test: `internal/index/indexer_test.go`

**Interfaces:**
- Consumes: `videometa.Read`（Task 2）、`imagefmt.IsVideo`（Task 4）
- Produces: なし（`indexFile` の内部）

- [ ] **Step 1: 失敗するテストを書く**

`internal/index/indexer_test.go` に足す。Task 2 のテストヘルパーと同じ手口で最小の
mp4 を組み立てる。`internal/index/testdata_test.go` に置く。

```go
// writeTestMP4 は mvhd だけを持つ最小の mp4 を書き出す。ftyp は isom なので
// creation_time は UTC として読まれる。
func writeTestMP4(t *testing.T, dir, name string, when time.Time) string {
	t.Helper()

	bx := func(typ string, parts ...[]byte) []byte {
		var body []byte
		for _, p := range parts {
			body = append(body, p...)
		}
		out := make([]byte, 8, 8+len(body))
		binary.BigEndian.PutUint32(out[:4], uint32(8+len(body)))
		copy(out[4:8], typ)
		return append(out, body...)
	}

	ftyp := bx("ftyp", []byte("isom"), []byte{0, 0, 2, 0}, []byte("mp41"))
	mvhdBody := make([]byte, 100)
	binary.BigEndian.PutUint32(mvhdBody[4:8], uint32(when.Unix()+2082844800))
	moov := bx("moov", bx("mvhd", mvhdBody))

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, append(ftyp, moov...), 0o644))
	return path
}
```

`indexer_test.go` に本体のテストを足す。

```go
// 動画の撮影日時はEXIFではなくコンテナから来る。mtimeとは違う値になることで、
// videometa が実際に読まれていることが分かる。
func TestIndexFileReadsTheCaptureTimeFromTheContainer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	want := time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)
	path := writeTestMP4(t, f.root, "clip.mp4", want)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), media.IDFor(path))
	require.NoError(t, err)
	require.True(t, got.TakenAt().Equal(want), "got %v", got.TakenAt())
}

// コンテナから読めない動画はmtimeに落ちる。EXIFの無い写真と同じ扱いである。
func TestIndexFileFallsBackToModTimeForAnUnreadableVideo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := filepath.Join(f.root, "clip.mp4")
	require.NoError(t, os.WriteFile(path, []byte("not a container"), 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	got, err := f.st.GetByID(context.Background(), media.IDFor(path))
	require.NoError(t, err)
	require.True(t, got.TakenAt().Equal(fi.ModTime()), "got %v", got.TakenAt())
}
```

- [ ] **Step 2: テストが失敗することを確かめる**

Run: `go test ./internal/index/ -run CaptureTimeFromTheContainer -v`
Expected: FAIL。mtime が入っているので `TakenAt` が一致しない

- [ ] **Step 3: `indexFile` に分岐を足す**

`internal/index/indexer.go` の `exif.Read` の呼び出しを置き換える。

```go
	// 撮影日時の出どころは写真と動画で違う。静止画のEXIFは時差を持たない
	// ローカル時刻だが、動画のコンテナは機種によって規約が割れるので、
	// videometa がブランドごとに解釈を変える。
	//
	// 向きは動画では常に1でよい。回転はブラウザが tkhd の行列を見て自分で当て、
	// 借りるサムネイルはSynologyが回転済みで書いている。famifoが動画の画素に
	// 触ることは無い。
	var takenAt time.Time
	orientation := uint16(1)
	if imagefmt.IsVideo(path) {
		takenAt = videometa.Read(path).TakenAt
	} else {
		m := exif.Read(path)
		takenAt, orientation = m.TakenAt, m.Orientation
	}

	// Mediaを先に組み立てる。ModTime が原本の版であり、thumb はそれを見て出力の
	// 名前を決める。ここで確定させておけば、インデックスに載る版とサムネイルの
	// 名前に入る版が食い違いようがない。
	md := media.New(path, fi, takenAt)
	if err := ix.thumbs.Prepare(md, orientation); err != nil {
		return err
	}
	return ix.st.Upsert(ctx, md)
```

import に `"time"` と `"github.com/yendo/famifo-proto/internal/index/videometa"` を足す。

- [ ] **Step 4: テストが通ることを確かめる**

Run: `go test ./internal/index/ -v`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/index
git commit -m "feat: Take the capture time of videos from the container"
```

### Task 7: 拡大表示が変換済み動画を配る

**Files:**
- Modify: `internal/thumb/thumb.go`
- Test: `internal/thumb/paths_test.go`

**Interfaces:**
- Consumes: `synology.FilmPath`/`HasFilm`（Task 3）、`imagefmt.IsVideo`（Task 4）
- Produces: `LargePath` が動画で変換版または原本を返す

- [ ] **Step 1: 失敗するテストを書く**

`internal/thumb/paths_test.go` に足す。既存の `newPathFixture` / `addPhoto` /
`writeFileAt` をそのまま使う（`addPhoto` は原本を置いて1件を返すだけで、中身が
画像である必要はない）。

```go
// HEVCの原本はハードウェアデコーダを持たない端末で再生できない。Synologyが作った
// H.264版があるならそれを配る。HEICで XL を借りているのと同じ構えである。
func TestLargePathBorrowsTheTranscodedVideo(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mp4")
	film := synology.FilmPath(m.Path())
	writeFileAt(t, film, "h264")

	path, ct := f.pv.LargePath(m)

	require.Equal(t, film, path)
	require.Equal(t, "video/mp4", ct)
}

// 借りるものが無ければ原本に落ちる。再生できるかは端末次第になる。
func TestLargePathFallsBackToTheOriginalVideo(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mov")

	path, ct := f.pv.LargePath(m)

	require.Equal(t, m.Path(), path)
	require.Equal(t, "video/quicktime", ct)
}

// 0バイトの SYNOPHOTO_FILM.fail しか無いときは借りない。DSMは変換に失敗すると
// これを置く。サムネイルの有無からは導けないので、フィルムは自分で確かめている。
func TestLargePathIgnoresAFailedTranscode(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mp4")
	writeFileAt(t, synology.ThumbMPath(m.Path()), "eadir m")
	writeFileAt(t, synology.FilmPath(m.Path()), "")

	path, _ := f.pv.LargePath(m)

	require.Equal(t, m.Path(), path)
}

// 動画のタイルは借りたサムネイルになる。写真と同じ経路が拡張子を見ずに効く。
func TestSmallPathBorrowsTheVideoThumbnail(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mp4")
	thumbM := synology.ThumbMPath(m.Path())
	writeFileAt(t, thumbM, "eadir m")

	path, ct, ok := f.pv.SmallPath(m)

	require.True(t, ok)
	require.Equal(t, thumbM, path)
	require.Equal(t, "image/jpeg", ct)
}

// 借りるものが無い動画には出せる絵が無い。原本を出しても再生はされないので、
// 配信側がプレースホルダに差し替える。
func TestSmallPathHasNothingForAVideoWithoutABorrowedThumbnail(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mp4")

	_, _, ok := f.pv.SmallPath(m)

	require.False(t, ok)
}
```

- [ ] **Step 2: テストが失敗することを確かめる**

Run: `go test ./internal/thumb/ -run LargePathBorrowsTheTranscodedVideo -v`
Expected: FAIL。原本のパスが返る

- [ ] **Step 3: `LargePath` に動画の枝を足す**

`internal/thumb/thumb.go` の `LargePath` を差し替える。

```go
// LargePath は拡大表示に配信するファイルのパスと、そのMIMEタイプを返す。
//
// 借りるものが2種類ある。HEICはSafari以外が表示できないので、@eaDir から
// 借りられるなら原本ではなくSynologyのXL（長辺1707px）を返す。動画はHEVCが
// 端末によって再生できないので、Synologyが作ったH.264版（SYNOPHOTO_FILM_H.mp4）を返す。
//
// 写真では「MとXLは同じ生成器が一緒に書く」ためXLの存在を確かめていないが、
// 動画では成り立たない。サムネイル生成と動画変換は別の工程で、実測した @eaDir にも
// SYNOPHOTO_THUMB_M.jpg があるのに SYNOPHOTO_FILM.fail があった。だからフィルムは
// 自分で確かめる。
//
// 借りたファイルの拡張子からMIMEを引き直すので、原本がHEICでもMOVでも、
// 呼び出し側は返った値をそのまま使える。
func (pv *Provider) LargePath(m media.Media) (path, contentType string) {
	path = m.Path()
	switch {
	case imagefmt.IsVideo(path):
		if synology.HasFilm(path) {
			path = synology.FilmPath(path)
		}
	case imagefmt.IsSupported(path) && !imagefmt.IsDecodable(path) && synology.HasThumbM(path):
		path = synology.ThumbXLPath(path)
	}
	return path, imagefmt.ContentType(path)
}
```

- [ ] **Step 4: テストが通ることを確かめる**

Run: `go test ./internal/thumb/ -v`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/thumb
git commit -m "feat: Serve the transcoded video when Synology has one"
```

### Task 8: タイルに動画の印を出す

**Files:**
- Modify: `internal/web/view.go`
- Modify: `internal/web/templates/tiles.html`
- Modify: `internal/web/templates/gallery.html`（助数詞のみ）
- Modify: `internal/web/static/app.css`
- Test: `internal/web/gallery_test.go`

**Interfaces:**
- Consumes: `imagefmt.IsVideo`（Task 4）
- Produces: `web.mediaView.IsVideo bool`、タイルの `data-video="1"` 属性

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/gallery_test.go` に足す。

```go
// タイルが動画かどうかはHTMLに出る。app.js が拡大表示の切り替えに使い、
// CSSが再生の印を重ねるのに使う。
func TestGalleryMarksVideoTiles(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	photo := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)
	video := f.addPhoto(t, "clip.mp4", time.Unix(1600000100, 0), noThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `id="t-`+video.ID()+`" href="/item/`+video.ID()+`"`)
	require.Regexp(t, `id="t-`+video.ID()+`"[^>]*data-video="1"`, body)
	require.NotRegexp(t, `id="t-`+photo.ID()+`"[^>]*data-video`, body)
}
```

- [ ] **Step 2: テストが失敗することを確かめる**

Run: `go test ./internal/web/ -run TestGalleryMarksVideoTiles -v`
Expected: FAIL。`data-video` が出力に無い

- [ ] **Step 3: `mediaView` に項目を足す**

`internal/web/view.go`:

```go
// mediaView は1件分のテンプレート入力。
type mediaView struct {
	ID       string
	PageURL  string // その1件だけを開くURL。タイルのリンク先
	ThumbURL string
	FullURL  string
	Date     string // "2006-01-02"。ローカル時刻。クライアントが日の区切りに使う
	IsVideo  bool   // 再生の印を出すか、拡大表示を <video> にするか
}
```

`buildRange` の組み立てに足す。判定は拡張子だけなので、ここでもファイルシステムは叩かない。

```go
		v.Photos = append(v.Photos, mediaView{
			ID:       m.ID(),
			PageURL:  "/item/" + m.ID(),
			FullURL:  "/file/" + m.ID(),
			ThumbURL: "/thumb/" + m.ID(),
			Date:     m.TakenAt().Format("2006-01-02"),
			IsVideo:  imagefmt.IsVideo(m.Path()),
		})
```

import に `"github.com/yendo/famifo-proto/internal/imagefmt"` を足す。

- [ ] **Step 4: テンプレートに属性を足す**

`internal/web/templates/tiles.html`:

```html
{{define "tiles"}}
{{/* id は貼り替えを差分で当てるときの対応付けに使う。これが無いと位置で
     照合され、1行スクロールしただけで全タイルの src が書き換わる。 */}}
{{range .Photos}}
<a class="tile" id="t-{{.ID}}" href="{{.PageURL}}" data-full="{{.FullURL}}" data-date="{{.Date}}"{{if .IsVideo}} data-video="1"{{end}}>
  <img src="{{.ThumbURL}}" alt="" loading="lazy" decoding="async">
</a>
{{end}}
{{end}}
```

- [ ] **Step 5: 助数詞を直す**

`internal/web/templates/gallery.html` のヘッダ。動画が混ざるので「枚」は使えない。

```html
  <span class="count">{{.Total}} 件</span>
```

- [ ] **Step 6: 再生の印をCSSで描く**

`internal/web/static/app.css` の `.tile img` の下に足す。

```css
/* 動画のタイルには再生の印を重ねる。借りたサムネイルがあってもなくても同じ印が
   乗るので、「動画である」ことは常に伝わる。数千件を貼り替えるので要素は足さず、
   擬似要素だけで描く。 */
.tile[data-video] {
	position: relative;
}
.tile[data-video]::before {
	content: "";
	position: absolute;
	right: 6px;
	bottom: 6px;
	width: 22px;
	height: 22px;
	border-radius: 50%;
	background: rgba(0, 0, 0, 0.55);
}
.tile[data-video]::after {
	content: "";
	position: absolute;
	right: 13px;
	bottom: 12px;
	border-style: solid;
	border-width: 5px 0 5px 8px;
	border-color: transparent transparent transparent #fff;
}
```

- [ ] **Step 7: テストが通ることを確かめる**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 8: コミットする**

```bash
git add internal/web
git commit -m "feat: Mark video tiles in the gallery"
```

### Task 9: 拡大表示で動画を再生する

**Files:**
- Modify: `internal/web/templates/gallery.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/app.css`

**Interfaces:**
- Consumes: タイルの `data-video`（Task 8）、`/file/{id}`（Task 1）
- Produces: `famifo.isVideoAt(i) bool`

- [ ] **Step 1: ライトボックスに `<video>` と失敗の文を足す**

`internal/web/templates/gallery.html`:

```html
<div id="lightbox" class="lightbox" hidden>
  <img alt="">
  {{/* 自動再生はしない。一覧をめくっていて突然音が鳴るのを避ける。
       playsinline は iOS Safari が全画面を乗っ取らないようにするため。 */}}
  <video controls playsinline preload="metadata" hidden></video>
  {{/* HEVCを出せない端末では要素は作られたまま再生に失敗し、画面が真っ黒になる。
       何が起きたのか分かるようにする。 */}}
  <p class="lb-error" hidden>この端末では再生できません</p>
  <button class="lb-nav lb-prev" type="button" aria-label="前へ">‹</button>
  <button class="lb-nav lb-next" type="button" aria-label="次へ">›</button>
</div>
```

- [ ] **Step 2: `app.js` のチャンク解析に動画の印を足す**

`parseTiles` のコメントと戻り値。

```js
	// サーバが返したHTML断片を、タイル1枚ずつに割る。取得時に1回だけパースし、
	// 以降はここから必要な範囲を切り出して組み立てる。data-full 属性が原本のURL、
	// data-date 属性が日付、data-video 属性が動画かどうか、href がその1件のページ
	// （ライトボックスを開いたときにアドレス欄へ出すURL）。
	function parseTiles(html) {
		const tmp = document.createElement("div");
		tmp.innerHTML = html;
		return [...tmp.querySelectorAll(".tile")].map((a) => ({
			html: a.outerHTML,
			url: a.dataset.full,
			page: a.getAttribute("href"),
			date: a.dataset.date,
			video: a.dataset.video === "1",
		}));
	}
```

- [ ] **Step 3: `isVideoAt` を公開する**

`pageAt` の下に足し、返す object にも加える。

```js
	// その1件が動画か。取得済みの塊からしか引けないので、表示中のものに
	// 対してだけ使う。urlAt を待った後なら塊は必ず揃っている。
	function isVideoAt(i) {
		return tileAt(i)?.video === true;
	}
```

```js
		urlAt,
		pageAt,
		isVideoAt,
```

- [ ] **Step 4: ライトボックスを写真と動画で出し分ける**

`const img = box.querySelector("img");` の下に足す。

```js
	const vid = box.querySelector("video");
	const errBox = box.querySelector(".lb-error");

	// stopVideo は再生を止めて読み込みも捨てる。src を外すだけではブラウザが
	// 取得を続けるので load() まで呼ぶ。これを忘れると次の写真を開いても
	// 前の動画の音が鳴り続ける。
	function stopVideo() {
		vid.pause();
		vid.removeAttribute("src");
		vid.load();
	}
```

`show()` の `img.src = url;` を置き換える。

```js
		idx = i;
		errBox.hidden = true;
		if (famifo.isVideoAt(i)) {
			img.hidden = true;
			img.removeAttribute("src");
			vid.pause(); // 動画から動画へ送るとき、前の音が重ならないように
			vid.src = url;
			vid.hidden = false;
			box.classList.add("playing");
		} else {
			stopVideo();
			vid.hidden = true;
			img.src = url;
			img.hidden = false;
			box.classList.remove("playing");
		}
```

`close()` に足す。

```js
	function close() {
		box.hidden = true;
		img.removeAttribute("src");
		stopVideo();
		vid.hidden = true;
		errBox.hidden = true;
		box.classList.remove("playing");
		document.body.classList.remove("locked");
		idx = -1;
		requestSeq++; // 閉じた後に届く古い解決を破棄する
		pushed = false;
	}
```

再生失敗を拾う。

```js
	vid.addEventListener("error", () => {
		if (!vid.hidden) errBox.hidden = false;
	});
```

- [ ] **Step 5: 操作が再生と喧嘩しないようにする**

`<video>` の上のクリックとスワイプは、いまのままだと閉じたり送ったりしてしまう。
再生・一時停止・シークができなくなるので、動画の上では拾わない。

`box` のクリックハンドラの先頭に足す。

```js
	box.addEventListener("click", (e) => {
		// 再生コントロールの操作を閉じる動作と取り違えない
		if (e.target.closest("video")) return;
		if (e.target.closest(".lb-prev")) {
```

`touchstart` に足す。

```js
		(e) => {
			// 2本指はピンチズーム。ブラウザに任せる
			tracking = e.touches.length === 1;
			// シークバーのドラッグを左右スワイプと取り違えない
			if (e.target.closest("video")) tracking = false;
			if (!tracking) return;
```

- [ ] **Step 6: 動画のときは前後送りの当たり判定を下から逃がす**

`.lb-nav` は左右22%を上下いっぱいに覆うため、そのままだと再生コントロールの
左右の端が押せない。`internal/web/static/app.css` に足す。

```css
.lightbox video {
	max-width: 100%;
	max-height: 100%;
}
/* 動画のときは再生コントロールの帯を前後送りで覆わない */
.lightbox.playing .lb-nav {
	bottom: 4rem;
}
```

- [ ] **Step 7: 手で確かめる**

```bash
go build -o famifo-proto . && ./famifo-proto -dir ~/Pictures
```

ブラウザで開き、次を確かめる。動画のタイルに印が出ている。開くと `<video>` が
出てコントロールが押せる。前後に送ると写真と動画が入れ替わっても音が残らない。
閉じても音が残らない。確かめたら `famifo-proto` は消す。

- [ ] **Step 8: コミットする**

```bash
git add internal/web
git commit -m "feat: Play videos in the lightbox"
```

### Task 10: ブラウザテストで切り替えを固定する

**Files:**
- Modify: `internal/web/browser_test.go`

**Interfaces:**
- Consumes: Task 8 と Task 9 の成果
- Produces: なし

**限界:** `chromedp/headless-shell` はハードウェアデコーダを持たずHEVCを再生できない。
H.264 の検証用動画も用意できない（有効な箱ツリーは手で組めても、映像フレームは
ffmpeg 無しには作れない）。**再生そのものは検証しない。** 要素の切り替えと後始末までを
固定する。

**共有コーパスには足さない。** `TestMain` が用意する200枚は日ごとの枚数と通し番号に
多くのテストが乗っており（`testPhotoCount`、`testDayCounts`、`expectedPhotoURLs`）、
1件足すだけで無関係なテストが落ちる。このテストは自前の小さなサーバーを立て、
共有の Chrome（`newTab`）だけを借りる。

- [ ] **Step 1: テストを書く**

`internal/web/browser_test.go` に足す。

```go
// 動画を開くと <img> ではなく <video> に切り替わり、離れると src が外れる。
// 外さないと裏でダウンロードが続き、次の写真を開いても音が鳴り続ける。
//
// 再生そのものは確かめない。ヘッドレスのChromeはHEVCを再生できず、H.264の
// 検証用動画も ffmpeg 無しには作れないためである。
func TestLightboxSwitchesBetweenImageAndVideo(t *testing.T) {
	requireBrowser(t)

	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	require.NoError(t, os.MkdirAll(mediaDir, 0o755))

	st, err := store.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbs, err := thumb.NewProvider(filepath.Join(dir, "thumbs"))
	require.NoError(t, err)

	// 中身は問わない。原本が配られるかではなく、要素が切り替わるかを見る。
	videoPath := filepath.Join(mediaDir, "clip.mp4")
	require.NoError(t, os.WriteFile(videoPath, []byte("not a real container"), 0o644))

	var jpg bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 40, 20)), nil))
	photoPath := filepath.Join(mediaDir, "a.jpg")
	require.NoError(t, os.WriteFile(photoPath, jpg.Bytes(), 0o644))

	// 動画のほうを新しくして先頭に並べる。
	ctxBG := context.Background()
	require.NoError(t, st.Upsert(ctxBG,
		media.Restore(videoPath, time.Unix(1600000100, 0), time.Unix(1600000100, 0))))
	require.NoError(t, st.Upsert(ctxBG,
		media.Restore(photoPath, time.Unix(1600000000, 0), time.Unix(1600000000, 0))))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	webSrv, err := web.NewServer(st, thumbs, log)
	require.NoError(t, err)
	srv := httptest.NewServer(webSrv.Handler())
	t.Cleanup(srv.Close)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	const stateJS = `(() => {
		const v = document.querySelector('#lightbox video');
		const i = document.querySelector('#lightbox img');
		return { vHidden: v.hidden, iHidden: i.hidden, src: v.getAttribute('src') || '' };
	})()`

	var got struct {
		VHidden bool   `json:"vHidden"`
		IHidden bool   `json:"iHidden"`
		Src     string `json:"src"`
	}

	err = chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(srv.URL),
		waitForTiles(10*time.Second),
		chromedp.Click(`#window .tile[data-video]`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Poll(`!document.querySelector('#lightbox video').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(stateJS, &got),
	)
	require.NoError(t, err)
	require.False(t, got.VHidden, "the video element is shown for a video")
	require.True(t, got.IHidden, "the image element is hidden for a video")
	require.True(t, strings.HasPrefix(got.Src, "/file/"), "got %q", got.Src)

	// 次（写真）へ送ると入れ替わり、動画の src は外れる。
	err = chromedp.Run(rctx,
		chromedp.KeyEvent(kb.ArrowRight),
		chromedp.Poll(`document.querySelector('#lightbox video').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(stateJS, &got),
	)
	require.NoError(t, err)
	require.True(t, got.VHidden)
	require.False(t, got.IHidden)
	require.Empty(t, got.Src, "the video source is dropped when leaving the video")

	// 閉じても外れたまま。
	err = chromedp.Run(rctx,
		chromedp.KeyEvent(kb.Escape),
		chromedp.Poll(`document.querySelector('#lightbox').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(stateJS, &got),
	)
	require.NoError(t, err)
	require.Empty(t, got.Src)
}
```

import に `bytes`、`image`、`image/jpeg`、`net/http/httptest`、`io`、`log/slog`、
`github.com/yendo/famifo-proto/internal/media`、`.../internal/store`、`.../internal/thumb`、
`.../internal/web` が要る（多くは既にある）。

- [ ] **Step 2: ブラウザテストを走らせる**

Run: `make browser-test`
Expected: PASS

Docker が要る。`chromedp/headless-shell` を `--network host` と `--shm-size 2g` で
起動する既存の仕掛けに乗る。

- [ ] **Step 3: コミットする**

```bash
git add internal/web
git commit -m "test: Pin the lightbox switch between image and video"
```

### Task 11: ドキュメントを直す

**Files:**
- Modify: `README.md`
- Modify: `main.go`

**Interfaces:**
- Consumes: なし
- Produces: なし

- [ ] **Step 1: README の対応形式を直す**

「Video is out of scope.」の行を削除し、表に2行足す。

```markdown
| Extension | Thumbnail | Notes |
|---|---|---|
| `.jpg` `.jpeg` `.png` `.gif` `.webp` | generated | |
| `.heic` `.heif` | borrowed from Synology if one is there | ... |
| `.mp4` `.mov` | borrowed from Synology if one is there | famifo never decodes video. With nothing to borrow the tile carries a play mark and no picture |
```

`### Synology thumbnails` の節に段落を足す。

```markdown
Videos borrow twice. The tile comes from `SYNOPHOTO_THUMB_M.jpg` like a photo's, and
playing one serves `SYNOPHOTO_FILM_H.mp4` — Synology's H.264 transcode — instead of the
original. Phones record HEVC, which only plays where the device has a hardware decoder,
so the transcode is what makes a video watchable on Android and on a PC. A video with no
transcode to borrow gets its original, and whether it plays is up to the device.
```

`## Features` の1行目「Indexes photos」を「Indexes photos and videos」に直す。

- [ ] **Step 2: フラグの説明を直す**

`main.go` の `-dir` の説明。

```go
		fmt.Sprintf("directories to collect photos and videos from (required); %q separates several",
```

`-batch` の説明の「how many photos are taken in at once」も「how many files」に直す。

- [ ] **Step 3: 確かめる**

Run: `go build ./... && ./famifo-proto -h`
Expected: 説明文が更新されている。確かめたら `famifo-proto` は消す

```bash
grep -n "out of scope" README.md
```
Expected: 出力なし

- [ ] **Step 4: コミットする**

```bash
git add README.md main.go
git commit -m "docs: Describe video support"
```

---

## 完了時の確認

- [ ] `make vet` が通る
- [ ] `make unit-test` が通る
- [ ] `make unit-test-race` が通る
- [ ] `make browser-test` が通る
- [ ] `CGO_ENABLED=0 go build -o famifo-proto .` が通る
- [ ] `go.mod` に新しい依存が増えていない
- [ ] **DBを作り直す必要があることを伝える。** 表名が `photos` から `media` に
      変わったので、古いDBのままでは空の一覧になる（`probeReadable` が起動時に
      落とすので気づける）。サムネイルの置き場は作り直さなくてよい
