# パッケージ境界の再編 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 散っている「写真の画素がディスクのどこにあるか」という知識と Synology 固有の知識をそれぞれ1箇所に集め、`web` を HTTP 配信だけの層にする。

**Architecture:** 新しいレイヤーは導入しない。既存の8パッケージのうち `internal/synology` を1つ足し、`store` にあるドメイン型（`Photo` / `ThumbSource` / `IDFor`）と `thumb` にあるパス規則（`CachePath` / `Syno*`）を、それぞれ `photo` と `synology` へ移すだけ。配信規則は `photo.ThumbPath` / `photo.FullPath` の2関数に集約する。

**Tech Stack:** Go（標準ライブラリ中心）、`modernc.org/sqlite`、`stretchr/testify`、`chromedp`（ブラウザテスト）

**Spec:** `docs/superpowers/specs/2026-09-02-package-boundaries-design.md`

## Global Constraints

- **振る舞いを一切変えない。** これは純粋なリファクタリングである
- **DBスキーマを変えない。** `thumb_source` の値（`"famifo"` `"eadir"`）もそのまま。既存の `famifo-data` が動き続けること
- **既存テストのアサーションを書き換えない。** 変えてよいのは import と識別子の参照だけ。期待値そのものを書き換える必要が生じたら、それは設計の誤りを示す信号なので、進めずに報告する
- **新しい依存を足さない。** `CGO_ENABLED=0` の単一バイナリという前提を崩さない
- **コード内のコメントと識別子の説明は日本語。** 既存ファイルに合わせる
- **コミットメッセージは英語、要約1行のみ。** 本文と `Co-Authored-By:` / `Claude-Session:` トレーラーは付けない
- **ブランチは `feature-package-boundaries`。** 既に切ってあり、spec のコミット `42f9e8b` が入っている
- **`internal/index` のロジック、`internal/config`、`internal/takenat`、テンプレート、`app.js`、`app.css` は触らない**

### 検証コマンド

| 目的 | コマンド |
|---|---|
| 単体テスト | `go test ./...` |
| vet | `go vet ./...` |
| **ブラウザテストのコンパイル確認** | `go vet -tags browser ./internal/web/` |
| ブラウザテスト（最終確認のみ） | `make browser-test` |

`internal/web/browser_test.go` には `//go:build browser` が付いているため、**通常の `go test ./...` ではコンパイルすらされない**。識別子を移す作業では見落としの温床になるので、識別子に触れた各タスクで必ず `go vet -tags browser ./internal/web/` を走らせること。

---

## File Structure

| ファイル | 責務 | タスク |
|---|---|---|
| `internal/synology/synology.go` | **新規。** `@eaDir` の規約（パス組み立て・存在確認・管理用ディレクトリ名） | 1, 2 |
| `internal/synology/synology_test.go` | **新規。** 上記のテスト | 1, 2 |
| `internal/photo/photo.go` | ドメイン型（`Photo` `ThumbSource`）・分類（`Kind`）・ID・画像パス | 3, 4, 5 |
| `internal/photo/photo_test.go` | 上記のテスト | 3, 4, 5 |
| `internal/thumb/thumb.go` | サムネイル**生成**のみ | 1, 4 |
| `internal/store/store.go` | SQLite。`photo.Photo` を読み書きする入れ物 | 3 |
| `internal/index/scan.go` | フルスキャン。除外判定は `synology` に委譲 | 2 |
| `internal/index/watch.go` | fsnotify 監視。除外判定は `synology` に委譲 | 2 |
| `internal/index/indexer.go` | 1ファイル単位のインデックス更新 | 1, 3 |
| `internal/web/handlers.go` | HTTPハンドラのみ | 1, 3, 4, 5, 6 |
| `internal/web/view.go` | **新規。** ビューモデルの構築 | 6 |

---

## Task 1: `internal/synology` を切り出す

**Files:**
- Create: `internal/synology/synology.go`
- Create: `internal/synology/synology_test.go`
- Modify: `internal/thumb/thumb.go`（パッケージコメント、および44〜82行あたりの `eaDir` / `synoThumbName` / `synoLargeName` / `synoPath` / `SynoThumbPath` / `SynoLargePath` / `HasSyno` を削除）
- Modify: `internal/thumb/thumb_test.go:59-102`（移した5つのテストを削除）
- Modify: `internal/index/indexer.go:63`
- Modify: `internal/index/indexer_test.go:154,192`
- Modify: `internal/web/handlers.go:163,182`
- Modify: `internal/web/handlers_test.go:61-62`

**Interfaces:**
- Consumes: なし（最初のタスク）
- Produces:
  - `synology.ThumbPath(srcPath string) string`
  - `synology.LargePath(srcPath string) string`
  - `synology.HasThumb(srcPath string) bool`

- [ ] **Step 1: 新しいパッケージのテストを書く**

`internal/synology/synology_test.go` を新規作成する。移設元（`internal/thumb/thumb_test.go:59-102`）は `writeImage` で本物の JPEG を書いていたが、`HasThumb` は中身を見ず「通常ファイルかつサイズが0でない」ことだけを見るので、ただのバイト列で足りる。画像エンコードへの依存をこのパッケージに持ち込まない。

```go
package synology

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeFile は親ディレクトリごとファイルを書く。
// HasThumb は中身を見ず、通常ファイルかつサイズが0でないことだけを見るので、
// 画像として妥当である必要はない。
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestThumbPathPointsAtTheMediumThumbnail(t *testing.T) {
	require.Equal(t,
		"/photos/2026-08-16/@eaDir/IMG_0428.HEIC/SYNOPHOTO_THUMB_M.jpg",
		ThumbPath("/photos/2026-08-16/IMG_0428.HEIC"))
}

func TestLargePathPointsAtTheXLThumbnail(t *testing.T) {
	require.Equal(t,
		"/photos/2026-08-16/@eaDir/IMG_0428.HEIC/SYNOPHOTO_THUMB_XL.jpg",
		LargePath("/photos/2026-08-16/IMG_0428.HEIC"))
}

func TestHasThumbFindsTheThumbnailSynologyLeftBehind(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original") // 中身は問わない。存在だけを見る
	writeFile(t, ThumbPath(src), "borrowed")

	require.True(t, HasThumb(src))
}

func TestHasThumbIsFalseWithoutEaDir(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.jpg")
	writeFile(t, src, "original")

	require.False(t, HasThumb(src))
}

// DSM 7.3 はHEICをデコードできず、0バイトの .fail を置く。.jpg は作られない。
func TestHasThumbIsFalseWhenOnlyAFailMarkerIsThere(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original")
	writeFile(t, filepath.Join(filepath.Dir(ThumbPath(src)), "SYNOPHOTO_THUMB_M.fail"), "")

	require.False(t, HasThumb(src))
}

// 手で消したあとに0バイトの .jpg が残るような状況。配信すると壊れた <img> になる。
func TestHasThumbIsFalseForAnEmptyThumbnail(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original")
	writeFile(t, ThumbPath(src), "")

	require.False(t, HasThumb(src))
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/synology/`
Expected: FAIL — `undefined: ThumbPath` などのコンパイルエラー

- [ ] **Step 3: `internal/synology/synology.go` を作る**

中身は `internal/thumb/thumb.go` の44〜82行あたりからそのまま移す。ロジックは1文字も変えない。変えるのは名前だけ（`SynoThumbPath` → `ThumbPath`、`SynoLargePath` → `LargePath`、`HasSyno` → `HasThumb`、`synoPath` → `entryPath`）。パッケージ名がすでに `synology` なので、`Syno` 接頭辞は重複になる。

```go
// Package synology はSynology NASが写真の隣に作る @eaDir の規約を扱う。
// パスの組み立てと存在確認だけで、ファイルの中身は読まない。
//
// @eaDir は読むだけ。famifoはSynology Photosの領域に書き込みも削除もしない。
package synology

import (
	"os"
	"path/filepath"
)

// eaDir はSynologyがサムネイルなどを置く管理用ディレクトリの名前。
const eaDir = "@eaDir"

// thumbName は一覧に借りるサムネイルのファイル名。Mは短辺320px（4:3なら
// 長辺427px）で、famifo自身が作る長辺480pxよりわずかに小さい。
const thumbName = "SYNOPHOTO_THUMB_M.jpg"

// largeName は拡大表示に借りるJPEGのファイル名。長辺1707px・約1MBで、
// 一覧には過大だが1枚だけ見せる場面では妥当な大きさになる。
const largeName = "SYNOPHOTO_THUMB_XL.jpg"

// entryPath は @eaDir の中の1ファイルのパスを組み立てる。
func entryPath(srcPath, name string) string {
	return filepath.Join(filepath.Dir(srcPath), eaDir, filepath.Base(srcPath), name)
}

// ThumbPath はSynologyがsrcPathの写真用に持つ一覧用サムネイルのパスを返す。
// 実在するとは限らない。あるかどうかは HasThumb で確かめる。
func ThumbPath(srcPath string) string { return entryPath(srcPath, thumbName) }

// LargePath はSynologyがsrcPathの写真用に持つ拡大表示用JPEGのパスを返す。
// ThumbPath と同じディレクトリを指す。存在は確かめない。MとXLは同じ生成器が
// 一緒に書くため、Mがあることを確かめてあればXLもあるものとして扱う。
func LargePath(srcPath string) string { return entryPath(srcPath, largeName) }

// HasThumb は借りられるサムネイルがあるかを報告する。
//
// DSM 7.3 はHEICをデコードできず、.jpg の代わりに0バイトの .fail を置く。拡張子が
// 違うので存在確認だけで弾けるが、手で消したあとに空の .jpg が残るような状況も
// あるため、通常ファイルかつ中身があることまで見る。
func HasThumb(srcPath string) bool {
	fi, err := os.Stat(ThumbPath(srcPath))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/synology/`
Expected: PASS（6テスト）

- [ ] **Step 5: `internal/thumb` から移設元を削除する**

`internal/thumb/thumb.go` から次を削除する: `eaDir`、`synoThumbName`、`synoLargeName`、`synoPath`、`SynoThumbPath`、`SynoLargePath`、`HasSyno`。`CachePath` と `Generator` 一式は残す（`CachePath` はタスク4で移す）。

あわせてパッケージコメントを、生成だけを担う内容に書き換える。

```go
// Package thumb は一覧表示用のサムネイルを生成する。出力は常にJPEG。
//
// 生成にHEICは来ない（自前ではデコードしない方針）。Synologyが @eaDir に持つ
// サムネイルを借りる経路は internal/synology が扱う。
package thumb
```

`internal/thumb/thumb_test.go:59-102` の5つのテスト（`TestSynoThumbPathPointsAtTheMediumThumbnail`、`TestSynoLargePathPointsAtTheXLThumbnail`、`TestHasSynoFindsTheThumbnailSynologyLeftBehind`、`TestHasSynoIsFalseWithoutEaDir`、`TestHasSynoIsFalseWhenOnlyAFailMarkerIsThere`、`TestHasSynoIsFalseForAnEmptyThumbnail`）を削除する。

- [ ] **Step 6: 呼び出し側を差し替える**

4ファイル、6箇所。

`internal/index/indexer.go:63`
```go
	case synology.HasThumb(path):
```

`internal/web/handlers.go:163`
```go
		http.ServeFile(w, r, synology.ThumbPath(p.Path))
```

`internal/web/handlers.go:182`
```go
		http.ServeFile(w, r, synology.LargePath(p.Path))
```

`internal/index/indexer_test.go:154`
```go
	out := synology.ThumbPath(srcPath)
```

`internal/index/indexer_test.go:192`
```go
	fail := filepath.Join(filepath.Dir(synology.ThumbPath(path)), "SYNOPHOTO_THUMB_M.fail")
```

`internal/web/handlers_test.go:61-62`
```go
		writeFileAt(t, synology.ThumbPath(path), "eadir-"+name)
		writeFileAt(t, synology.LargePath(path), "eadir-xl-"+name)
```

各ファイルに `"github.com/yendo/famifo-proto/internal/synology"` の import を足し、使わなくなった `thumb` の import があれば消す（`internal/web/handlers.go` は `thumb.CachePath` をまだ使うので残る）。

- [ ] **Step 7: 全体が通ることを確認する**

Run: `go build ./... && go vet ./... && go vet -tags browser ./internal/web/ && go test ./...`
Expected: すべて PASS。`browser_test.go` はこのタスクでは `Syno*` を参照していないが、以降のタスクの習慣として毎回確認する。

- [ ] **Step 8: コミット**

```bash
git add internal/synology internal/thumb internal/index internal/web
git commit -m "refactor: move the @eaDir path rules into an internal/synology package"
```

---

## Task 2: 除外ディレクトリ名を `index` から `synology` へ移す

`@eaDir` と `#recycle` は Synology が作るディレクトリであり、`index/scan.go` にあるのは走査の都合でそこに書かれただけである。

**Files:**
- Modify: `internal/synology/synology.go`（追記）
- Modify: `internal/synology/synology_test.go`（追記）
- Modify: `internal/index/scan.go:20-40`（`excludedDirs` と `inExcludedDir` を削除）、`internal/index/scan.go:74`
- Modify: `internal/index/watch.go:72,155,173`

**Interfaces:**
- Consumes: `synology.ThumbPath` など（タスク1）
- Produces:
  - `synology.IsManagedDir(name string) bool`
  - `synology.InManagedDir(path string) bool`

- [ ] **Step 1: 失敗するテストを書く**

`internal/synology/synology_test.go` の末尾に追記する。

```go
func TestIsManagedDirCoversSynologysOwnDirectories(t *testing.T) {
	require.True(t, IsManagedDir("@eaDir"))
	require.True(t, IsManagedDir("#recycle"))
	require.False(t, IsManagedDir("2026-08-16"))
}

// 走査は fs.SkipDir で降りずに済むが、fsnotify のイベントは個々のパスで
// 届くため、途中に挟まっているかを見る必要がある。
func TestInManagedDirFindsTheDirectoryAnywhereInThePath(t *testing.T) {
	require.True(t, InManagedDir("/photos/@eaDir/IMG_0001.jpg/SYNOPHOTO_THUMB_M.jpg"))
	require.True(t, InManagedDir("/photos/#recycle/deleted.jpg"))
	require.False(t, InManagedDir("/photos/2026-08-16/IMG_0001.jpg"))
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/synology/`
Expected: FAIL — `undefined: IsManagedDir`

- [ ] **Step 3: `internal/synology/synology.go` に追記する**

`internal/index/scan.go:20-40` からそのまま移す。ロジックは変えない。

```go
// managedDirs はSynologyが写真ディレクトリの中に作る管理用ディレクトリ。
// 中身は写真と同じ拡張子を持つが写真ではないので、降りると1枚が複数枚に見える。
var managedDirs = map[string]bool{
	// 写真1枚につき @eaDir/<ファイル名>/SYNOPHOTO_THUMB_*.jpg を作る。
	// EXIFが無いためmtimeに落ち、生成した日に大量の重複が積み上がる。
	eaDir: true,
	// Synologyのゴミ箱。削除した写真が一覧に復活する。
	"#recycle": true,
}

// IsManagedDir はディレクトリ名が走査対象外かを報告する。
func IsManagedDir(name string) bool { return managedDirs[name] }

// InManagedDir はパスの途中に管理用ディレクトリが挟まっているかを報告する。
// 走査は fs.SkipDir で降りずに済むが、fsnotify のイベントは個々のパスで
// 届くためこちらで判定する必要がある。
func InManagedDir(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if managedDirs[part] {
			return true
		}
	}
	return false
}
```

import に `"strings"` を足す。

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/synology/`
Expected: PASS（8テスト）

- [ ] **Step 5: `internal/index` の移設元を削除して差し替える**

`internal/index/scan.go` から `excludedDirs` の var 宣言（20〜28行あたり）と `inExcludedDir` 関数（30〜40行あたり）を削除する。

`internal/index/scan.go:74`
```go
			if d.IsDir() {
				if synology.IsManagedDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
```

`internal/index/watch.go:72`
```go
	if synology.InManagedDir(ev.Name) {
		return
	}
```

`internal/index/watch.go:155`（`addRoots` の中。監視枠を消費しないための判定）
```go
		if synology.IsManagedDir(d.Name()) {
			return fs.SkipDir
		}
```

`internal/index/watch.go:173`（`enqueueTree` の中）
```go
		if d.IsDir() {
			if synology.IsManagedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
```

両ファイルに `"github.com/yendo/famifo-proto/internal/synology"` の import を足す。`internal/index/scan.go` は `strings` を `under()` でまだ使うので消さないこと。

- [ ] **Step 6: 全体が通ることを確認する**

Run: `go build ./... && go vet ./... && go vet -tags browser ./internal/web/ && go test ./...`
Expected: すべて PASS。`internal/index/scan_test.go:251-258` と `internal/index/watch_test.go:167,185` にある `@eaDir` / `#recycle` の除外テストが引き続き通ること（これが移設が正しいことの証拠になる）。

- [ ] **Step 7: コミット**

```bash
git add internal/synology internal/index
git commit -m "refactor: move the Synology managed directory names into internal/synology"
```

---

## Task 3: ドメイン型を `store` から `photo` へ移す

**Files:**
- Modify: `internal/photo/photo.go`（`Photo` / `ThumbSource` / `IDFor` を追記）
- Modify: `internal/store/store.go`（同じものを削除し、シグネチャを `photo.Photo` に変更）
- Modify: `internal/store/store_test.go`、`internal/index/indexer.go`、`internal/index/indexer_test.go`、`internal/index/scan_test.go`、`internal/web/handlers.go`、`internal/web/handlers_test.go`、`internal/web/gallery_test.go`、`internal/web/browser_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `photo.Photo`（フィールド: `ID string` / `Path string` / `TakenAt time.Time` / `ModTime time.Time` / `Size int64` / `Ext string` / `ThumbSource ThumbSource`）
  - `photo.ThumbSource`（`photo.ThumbNone` = `""`、`photo.ThumbFamifo` = `"famifo"`、`photo.ThumbSyno` = `"eadir"`）
  - `photo.IDFor(path string) string`
  - `store` 側は `Upsert(ctx, photo.Photo)` / `GetByID(ctx, id) (photo.Photo, error)` / `DeleteByPath(ctx, path) (photo.Photo, bool, error)` / `DeleteByPathPrefix(ctx, prefix) ([]photo.Photo, error)` / `ListRange(ctx, offset, limit) ([]photo.Photo, error)` に変わる
  - `store.ErrNotFound` と `store.DayGroup` は `store` に残る

- [ ] **Step 1: `internal/photo/photo.go` に型を移す**

`internal/store/store.go:27-50` からそのままコピーする。定数の文字列値（`""` `"famifo"` `"eadir"`）は DB に入っている値なので絶対に変えない。

```go
// Photo はインデックス上の1枚の写真。
type Photo struct {
	ID      string    // パスから導出した安定ID。URLに露出させる
	Path    string    // ディスク上の絶対パス
	TakenAt time.Time // EXIF撮影日時、無ければmtime
	ModTime time.Time // ファイルのmtime。再スキャン時の変更検知に使う
	Size    int64
	Ext     string // 小文字の拡張子（"." 込み）
	// ThumbSource はサムネイルの出どころ。消してよいのは自前で作ったものだけなので、
	// 「あるか」ではなく「どこにあるか」を持つ。
	ThumbSource ThumbSource
}

// ThumbSource はサムネイルの出どころ。
type ThumbSource string

const (
	ThumbNone   ThumbSource = ""       // サムネイルが無い
	ThumbFamifo ThumbSource = "famifo" // famifoが生成し、サムネイルキャッシュに置いたもの
	ThumbSyno   ThumbSource = "eadir"  // Synologyが @eaDir に持っているもの。読むだけで書き換えない
)

// IDFor はパスから安定したIDを導出する。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func IDFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}
```

import に `"crypto/sha256"`、`"encoding/hex"`、`"time"` を足す。

あわせてパッケージコメントを更新する。

```go
// Package photo は写真1枚について答えられることをまとめる。
// インデックス上の型、拡張子による分類、安定ID、そして配信する画像ファイルのパス。
//
// 分類は拡張子のみに基づき、ファイルの中身は読まない
// （fsnotifyの大量イベントを軽く捌くため）。
package photo
```

- [ ] **Step 2: `internal/store/store.go` を書き換える**

移設元（27〜50行あたりの `Photo` / `ThumbSource` / 定数 / `IDFor`）を削除し、`"github.com/yendo/famifo-proto/internal/photo"` を import する。`ErrNotFound`、`DayGroup`、`DayGroups`、`Count`、`AllPaths` はそのまま残す。

シグネチャと戻り値を差し替える。

```go
func (s *Store) Upsert(ctx context.Context, p photo.Photo) error

func scanPhoto(row interface{ Scan(...any) error }) (photo.Photo, error) {
	var p photo.Photo
	var takenAt, modTime int64
	if err := row.Scan(&p.ID, &p.Path, &takenAt, &modTime, &p.Size, &p.Ext, &p.ThumbSource); err != nil {
		return photo.Photo{}, err
	}
	p.TakenAt = time.Unix(takenAt, 0)
	p.ModTime = time.Unix(modTime, 0)
	return p, nil
}

func (s *Store) GetByID(ctx context.Context, id string) (photo.Photo, error)
func (s *Store) DeleteByPath(ctx context.Context, path string) (photo.Photo, bool, error)
func (s *Store) DeleteByPathPrefix(ctx context.Context, prefix string) ([]photo.Photo, error)
func (s *Store) ListRange(ctx context.Context, offset, limit int) ([]photo.Photo, error)
```

各関数の本体にある `Photo{}` / `var out []Photo` も `photo.Photo{}` / `var out []photo.Photo` に変える。`GetByID` / `DeleteByPath` / `DeleteByPathPrefix` / `ListRange` の早期 return にある `Photo{}` を見落とさないこと。

- [ ] **Step 3: 呼び出し側を機械的に置換する**

`internal/store` 以外のパッケージでは、次の置換をかける。

```bash
grep -rln "store\.\(Photo\|ThumbSource\|ThumbNone\|ThumbFamifo\|ThumbSyno\|IDFor\)\b" \
  --include=*.go internal/index internal/web \
  | xargs sed -i \
    -e 's/store\.Photo\b/photo.Photo/g' \
    -e 's/store\.ThumbSource\b/photo.ThumbSource/g' \
    -e 's/store\.ThumbNone\b/photo.ThumbNone/g' \
    -e 's/store\.ThumbFamifo\b/photo.ThumbFamifo/g' \
    -e 's/store\.ThumbSyno\b/photo.ThumbSyno/g' \
    -e 's/store\.IDFor\b/photo.IDFor/g'
```

対象は `internal/index/indexer.go`、`internal/index/indexer_test.go`、`internal/index/scan_test.go`、`internal/web/handlers.go`、`internal/web/handlers_test.go`、`internal/web/gallery_test.go`、`internal/web/browser_test.go`。

`internal/store/store_test.go` は `store` パッケージの中にあるため、参照が `store.` 接頭辞なしの裸（`Photo{...}`、`ThumbFamifo`、`IDFor(...)`）になっている。ここは置換が効かないので手で直す。

```bash
grep -n "\bPhoto{\|\bThumbNone\b\|\bThumbFamifo\b\|\bThumbSyno\b\|\bIDFor(" internal/store/store_test.go
```

で出た箇所を `photo.Photo{` / `photo.ThumbFamifo` / `photo.IDFor(` のように直し、import を足す。**`DayGroup` と `ErrNotFound` は `store` に残るので触らない。**

- [ ] **Step 4: import を整える**

置換したファイルすべてに `"github.com/yendo/famifo-proto/internal/photo"` を足す。`internal/web/handlers.go` と `internal/index/indexer.go` と `internal/index/scan.go` は既に import 済みなので重複させないこと。`store` の import は、クエリ呼び出しや `store.ErrNotFound` で引き続き使うファイルでは残す。

Run: `gofmt -l ./internal ./main.go`
Expected: 何も出力されない（出たファイルは `gofmt -w` で整形する）

- [ ] **Step 5: コンパイルとテストを確認する**

Run: `go build ./... && go vet ./... && go vet -tags browser ./internal/web/ && go test ./...`
Expected: すべて PASS。

`go vet -tags browser` を飛ばさないこと。`internal/web/browser_test.go` は `store.Photo`（2箇所）・`store.IDFor`（3箇所）・`store.ThumbFamifo`（2箇所）・`store.ThumbNone`（1箇所）を参照しており、**通常のビルドでは検出されない**。

- [ ] **Step 6: DBの互換性を確認する**

型の移動で `thumb_source` の値が変わっていないことを確かめる。

Run: `grep -n 'ThumbSource = "' internal/photo/photo.go`
Expected: `""`、`"famifo"`、`"eadir"` の3つ。spec の「DBスキーマも `thumb_source` の値も変えない」という制約の確認。

- [ ] **Step 7: コミット**

```bash
git add internal/photo internal/store internal/index internal/web
git commit -m "refactor: move the Photo entity out of the store package"
```

---

## Task 4: `CachePath` を `thumb` から `photo` へ移す

キャッシュの配置規則は生成の実装詳細ではなくレイアウト規則であり、配信側も同じ規則を必要とする。`photo` に置けば、重い画像ライブラリを引かずにパスだけを引ける。

**Files:**
- Modify: `internal/photo/photo.go`（`CachePath` を追記）
- Modify: `internal/photo/photo_test.go`（`testID` と移設したテストを追記）
- Modify: `internal/thumb/thumb.go:57`（`CachePath` を削除）、`:84`（`Generator.Path`）
- Modify: `internal/thumb/thumb_test.go:54-57`（`TestCachePathShardsByFirstTwoChars` を削除）
- Modify: `internal/web/handlers.go:161`、`internal/web/handlers_test.go:59`

**Interfaces:**
- Consumes: `photo.Photo`（タスク3）
- Produces: `photo.CachePath(dir, id string) string`

- [ ] **Step 1: 失敗するテストを書く**

`internal/photo/photo_test.go` の末尾に追記する。`testID` はこの後タスク5でも使う。

```go
// testID は32文字のダミーID。IDFor の出力と同じ形（16進32文字）にしてある。
const testID = "abcdef0123456789abcdef0123456789"

func TestCachePathShardsByFirstTwoChars(t *testing.T) {
	require.Equal(t, filepath.Join("/data/thumbs", "ab", testID+".jpg"),
		CachePath("/data/thumbs", testID))
}
```

`internal/photo/photo_test.go` の import に `"path/filepath"` と `"github.com/stretchr/testify/require"` が無ければ足す。

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/photo/`
Expected: FAIL — `undefined: CachePath`

- [ ] **Step 3: `internal/photo/photo.go` に移す**

```go
// CachePath は自前で生成したサムネイルのパスを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func CachePath(dir, id string) string { return filepath.Join(dir, id[:2], id+".jpg") }
```

`internal/photo/photo.go` は既に `"path/filepath"` を import している。

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/photo/`
Expected: PASS

- [ ] **Step 5: 移設元を削除して呼び出し側を差し替える**

`internal/thumb/thumb.go:57` の `CachePath` を削除し、84行の `Generator.Path` を差し替える。

```go
// Path はサムネイルの絶対パスを返す。
func (g *Generator) Path(id string) string { return photo.CachePath(g.dir, id) }
```

`internal/thumb/thumb.go` の import に `"github.com/yendo/famifo-proto/internal/photo"` を足す。循環参照にはならない（`photo` は `thumb` を import しない）。

`internal/thumb/thumb_test.go:54-57` の `TestCachePathShardsByFirstTwoChars` と、17行あたりの `testID` の const 宣言を削除する（`testID` が `thumb_test.go` の他のテストで使われていないか `grep -n "testID" internal/thumb/` で確認し、使われていれば const は残す）。

`internal/web/handlers.go:161`
```go
		http.ServeFile(w, r, photo.CachePath(s.thumbDir, p.ID))
```

`internal/web/handlers_test.go:59`
```go
		writeFileAt(t, photo.CachePath(f.thumbDir, p.ID), "thumb-"+name)
```

これで `internal/web` は `thumb` を使わなくなる。両ファイルから `thumb` の import を消す。

- [ ] **Step 6: 全体が通ることを確認する**

Run: `go build ./... && go vet ./... && go vet -tags browser ./internal/web/ && go test ./...`
Expected: すべて PASS

Run: `grep -rn "internal/thumb" --include=*.go internal/web/`
Expected: 何も出力されない（`web` から `thumb` への依存が切れたことの確認）

- [ ] **Step 7: コミット**

```bash
git add internal/photo internal/thumb internal/web
git commit -m "refactor: move the thumbnail cache layout rule into the photo package"
```

---

## Task 5: `photo.ThumbPath` / `photo.FullPath` を新設して `web` から使う

配信規則の表が `web.handleThumb` / `web.handlePhoto` / `web.buildRange` の3箇所に分かれている状態を、2つの関数に集約する。これが今回の再編の本丸である。

**Files:**
- Modify: `internal/photo/photo.go`（`ThumbPath` / `FullPath` を追記）
- Modify: `internal/photo/photo_test.go`（テーブルドリブンテストを追記）
- Modify: `internal/web/handlers.go`（`handleThumb` / `handlePhoto` / `buildRange`）

**Interfaces:**
- Consumes: `photo.Photo`（タスク3）、`photo.CachePath`（タスク4）、`synology.ThumbPath` / `synology.LargePath`（タスク1）
- Produces:
  - `photo.ThumbPath(p Photo, cacheDir string) (path string, ok bool)`
  - `photo.FullPath(p Photo) string`

- [ ] **Step 1: 失敗するテストを書く**

`internal/photo/photo_test.go` の末尾に追記する。`FullPath` のほうは `ThumbSource` 3種 × `Kind` 2種の6通りを全部並べる。spec が要求している網羅である。

```go
func TestThumbPathBySource(t *testing.T) {
	const cacheDir = "/data/thumbs"
	tests := []struct {
		name string
		p    Photo
		want string
		ok   bool
	}{
		{
			name: "自前で生成したものはキャッシュから引く",
			p:    Photo{ID: testID, Path: "/photos/a.jpg", ThumbSource: ThumbFamifo},
			want: filepath.Join(cacheDir, "ab", testID+".jpg"),
			ok:   true,
		},
		{
			name: "借りたものは @eaDir から引く",
			p:    Photo{ID: testID, Path: "/photos/a.heic", ThumbSource: ThumbSyno},
			want: "/photos/@eaDir/a.heic/SYNOPHOTO_THUMB_M.jpg",
			ok:   true,
		},
		{
			name: "借りるものも作れるものも無ければ ok=false",
			p:    Photo{ID: testID, Path: "/photos/a.heic", ThumbSource: ThumbNone},
			want: "",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ThumbPath(tt.p, cacheDir)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// XLに差し替えるのは「HEICで、かつ借りている」ときだけ。
// 他の5通りはすべて原本を配信する。
func TestFullPathSwapsInTheXLOnlyForBorrowedOpaquePhotos(t *testing.T) {
	tests := []struct {
		name string
		p    Photo
		want string
	}{
		{
			name: "HEIC + 借りている → SynologyのXL",
			p:    Photo{Path: "/photos/a.heic", ThumbSource: ThumbSyno},
			want: "/photos/@eaDir/a.heic/SYNOPHOTO_THUMB_XL.jpg",
		},
		{
			name: "HEIC + 借りていない → 原本（Safariでしか見えないが他に出せるものが無い）",
			p:    Photo{Path: "/photos/a.heic", ThumbSource: ThumbNone},
			want: "/photos/a.heic",
		},
		{
			name: "HEIC + 自前生成 → 原本（HEICは自前生成しないので実際には起きない）",
			p:    Photo{Path: "/photos/a.heic", ThumbSource: ThumbFamifo},
			want: "/photos/a.heic",
		},
		{
			name: "JPEG + 借りている → 原本（借りるのは一覧用だけ）",
			p:    Photo{Path: "/photos/a.jpg", ThumbSource: ThumbSyno},
			want: "/photos/a.jpg",
		},
		{
			name: "JPEG + 自前生成 → 原本",
			p:    Photo{Path: "/photos/a.jpg", ThumbSource: ThumbFamifo},
			want: "/photos/a.jpg",
		},
		{
			name: "JPEG + サムネイル無し → 原本",
			p:    Photo{Path: "/photos/a.jpg", ThumbSource: ThumbNone},
			want: "/photos/a.jpg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, FullPath(tt.p))
		})
	}
}

// FullPath の戻り値からMIMEが引けることが、ハンドラ側で分岐を持たずに済む根拠。
func TestContentTypeOfTheBorrowedXLIsJPEG(t *testing.T) {
	p := Photo{Path: "/photos/a.heic", ThumbSource: ThumbSyno}
	require.Equal(t, "image/jpeg", ContentType(FullPath(p)))
}

func TestContentTypeOfAnUnborrowedHEICIsHEIC(t *testing.T) {
	p := Photo{Path: "/photos/a.heic", ThumbSource: ThumbNone}
	require.Equal(t, "image/heic", ContentType(FullPath(p)))
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/photo/`
Expected: FAIL — `undefined: ThumbPath`、`undefined: FullPath`

- [ ] **Step 3: `internal/photo/photo.go` に実装する**

```go
// ThumbPath は一覧に出すサムネイルのパスを返す。無ければ ok=false。
//
// cacheDir は -data から来る配置設定で、写真そのものの属性ではないため引数で受ける。
func ThumbPath(p Photo, cacheDir string) (string, bool) {
	switch p.ThumbSource {
	case ThumbFamifo:
		return CachePath(cacheDir, p.ID), true
	case ThumbSyno:
		return synology.ThumbPath(p.Path), true
	}
	return "", false
}

// FullPath は拡大表示に配信するファイルのパスを返す。
//
// HEICはSafari以外のブラウザが表示できない。@eaDir から借りているなら原本ではなく
// SynologyのXL（長辺1707px）を返す。thumb_source が eadir であればMがあり、MとXLは
// 同じ生成器が一緒に書くので、XLの存在はそこから導ける。
//
// 戻り値がパスだけで済むのは、XLのファイル名が .jpg で終わるためである。
// 呼び出し側は ContentType(FullPath(p)) でMIMEを引けばよく、分岐を持たなくてよい。
func FullPath(p Photo) string {
	if KindOf(p.Path) == KindOpaque && p.ThumbSource == ThumbSyno {
		return synology.LargePath(p.Path)
	}
	return p.Path
}
```

import に `"github.com/yendo/famifo-proto/internal/synology"` を足す。循環参照にはならない（`synology` は何も import しない）。

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/photo/ -v -run 'TestThumbPath|TestFullPath|TestContentType'`
Expected: PASS（サブテストを含めて13件）

- [ ] **Step 5: `internal/web/handlers.go` を差し替える**

`handleThumb`（158〜169行あたり）を丸ごと置き換える。

```go
// handleThumb はサムネイルを配信する。どのファイルを出すかは photo が決める。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	path, ok := photo.ThumbPath(p, s.thumbDir)
	if !ok {
		// 借りるものも作れるものも無い写真。原本を使うべき。
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
```

`handlePhoto`（171〜188行あたり）を丸ごと置き換える。

```go
// handlePhoto は拡大表示用の画像を配信する。
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	path := photo.FullPath(p)
	// ServeFileは拡張子からMIMEを引くがHEIC/HEIFを知らない。
	// 先に設定しておけばServeContentは上書きしない。
	w.Header().Set("Content-Type", photo.ContentType(path))
	http.ServeFile(w, r, path)
}
```

`buildRange` の中の `ThumbSource` を直接見ている箇所を置き換える。

```go
		if _, ok := photo.ThumbPath(p, s.thumbDir); ok {
			pv.ThumbURL = "/thumb/" + p.ID
		}
```

`buildRange` は `s` をレシーバに持っているので `s.thumbDir` が使える。

- [ ] **Step 6: 全体が通ることを確認する**

Run: `go build ./... && go vet ./... && go vet -tags browser ./internal/web/ && go test ./...`
Expected: すべて PASS。特に `internal/web/handlers_test.go` の `store.ThumbSyno` 系のテスト（HEICの拡大でXLが出ること、サムネイルが無ければ404になること）が**アサーションを一切変えずに**通ること。ここが振る舞い不変の証拠になる。

Run: `grep -n "ThumbSource" internal/web/handlers.go`
Expected: 何も出力されない（`web` が出どころの enum を直接見なくなったことの確認）

- [ ] **Step 7: コミット**

```bash
git add internal/photo internal/web
git commit -m "refactor: gather the image delivery rules into photo.ThumbPath and photo.FullPath"
```

---

## Task 6: `web` のビューモデルを `view.go` へ分ける

**Files:**
- Create: `internal/web/view.go`
- Modify: `internal/web/handlers.go`

**Interfaces:**
- Consumes: `photo.ThumbPath`（タスク5）
- Produces: なし（パッケージ内の移動のみ。すべて非公開）

- [ ] **Step 1: `internal/web/view.go` を作る**

`internal/web/handlers.go` の先頭部分（`photoView`・`itemsView`・`dayView`・`galleryView` の型宣言と `buildRange` メソッド）を、コメントごとそのまま新しいファイルへ移す。**中身は1文字も変えない。**

```go
package web

import (
	"html/template"
	"net/http"

	"github.com/yendo/famifo-proto/internal/photo"
)

// photoView は1枚分のテンプレート入力。
type photoView struct {
	ID       string
	ThumbURL string
	FullURL  string
	Date     string // "2006-01-02"。ローカル時刻。クライアントが日の区切りに使う
}

// itemsView は items.html の入力。
type itemsView struct {
	Photos []photoView
}

// dayView は埋め込む日ごとの表の1要素。
// 転送量を抑えるためキー名を短くしている（数千日ぶんになりうる）。
type dayView struct {
	D string `json:"d"` // "2006-01-02"
	N int    `json:"n"` // その日の枚数
}

// galleryView は gallery.html の入力。itemsViewを埋め込むので
// {{template "items" .}} にそのまま渡せる。
type galleryView struct {
	itemsView
	Total     int
	ChunkSize int
	// DayGroups は日ごとの枚数のJSON配列。html/template に再エスケープさせず
	// そのまま出すため template.JS で渡す。中身は日付と数値だけなので
	// "</script>" は構造上現れない。
	DayGroups template.JS
}

// buildRange はオフセット指定で1窓枠分を組み立てる。
func (s *Server) buildRange(r *http.Request, offset, limit int) (itemsView, error) {
	photos, err := s.st.ListRange(r.Context(), offset, limit)
	if err != nil {
		return itemsView{}, err
	}

	v := itemsView{Photos: make([]photoView, 0, len(photos))}
	for _, p := range photos {
		pv := photoView{
			ID:       p.ID,
			FullURL:  "/photo/" + p.ID,
			ThumbURL: "/photo/" + p.ID,
			Date:     p.TakenAt.Format("2006-01-02"),
		}
		if _, ok := photo.ThumbPath(p, s.thumbDir); ok {
			pv.ThumbURL = "/thumb/" + p.ID
		}
		v.Photos = append(v.Photos, pv)
	}
	return v, nil
}
```

- [ ] **Step 2: `internal/web/handlers.go` から移した分を削除する**

削除するのは Step 1 に挙げた4つの型と `buildRange` だけ。`parseWindow` は HTTP のクエリ解析なので `handlers.go` に残す。残るのは `parseWindow`・`handleGallery`・`handleItems`・`handleThumb`・`handlePhoto`・`lookup`。

`handlers.go` の import から、使わなくなったもの（`html/template` など）を消す。`handleGallery` が `template.JS` を使っているため `html/template` は残る可能性がある。`gofmt` ではなく実際のコンパイルで判断すること。

- [ ] **Step 3: 整形とコンパイルを確認する**

Run: `gofmt -l ./internal && go build ./... && go vet ./... && go vet -tags browser ./internal/web/`
Expected: `gofmt -l` は何も出力せず、他はエラーなし

- [ ] **Step 4: テストを走らせる**

Run: `go test ./...`
Expected: すべて PASS。パッケージ内でファイルを移しただけなので、テストは1つも変わらない。

- [ ] **Step 5: コミット**

```bash
git add internal/web
git commit -m "refactor: split the gallery view models out of the web handlers"
```

---

## Task 7: 最終確認

**Files:** なし（検証のみ）

- [ ] **Step 1: 依存関係が spec どおりになっていることを確認する**

Run:
```bash
go list -deps -f '{{.ImportPath}}' ./... | grep famifo-proto
for p in config synology photo takenat thumb store index web; do
  printf '%-10s -> ' "$p"
  go list -f '{{join .Imports "\n"}}' ./internal/$p \
    | grep famifo-proto | sed 's|.*/internal/||' | tr '\n' ' '
  echo
done
```

Expected: spec の表と一致すること。

```
config     ->
synology   ->
photo      -> synology
takenat    ->
thumb      -> photo
store      -> photo
index      -> photo store synology takenat thumb
web        -> photo store
```

`web` が `thumb` と `synology` を import していないこと、`store` が `photo` だけを見ていることが今回の再編の到達点である。ずれていたら、どのタスクの差し替えが漏れたかを特定して直す。

- [ ] **Step 2: ビルドとテストを通しで走らせる**

Run: `make build && make vet && make unit-test`
Expected: すべて成功

- [ ] **Step 3: ブラウザテストを走らせる**

Run: `make browser-test`
Expected: PASS

**落ちた場合はまず同じコマンドをもう一度走らせること。** このリポジトリのブラウザテストは元から2割程度落ちるので、1回の失敗は変更が原因である証拠にならない。再実行しても同じテストが落ちるなら、そこで初めて変更を疑う。

- [ ] **Step 4: 振る舞いが変わっていないことを確認する**

Run: `git diff --stat 42f9e8b..HEAD -- internal/`
Expected: テストファイルの変更が import と識別子の参照だけであること。

Run:
```bash
git diff 42f9e8b..HEAD -- '*_test.go' | grep '^[-+]' | grep -i 'require\.\|assert\.' \
  | grep -v '^[-+][-+]' | sed 's/[a-z]*\.\(Photo\|ThumbSource\|ThumbNone\|ThumbFamifo\|ThumbSyno\|IDFor\|CachePath\|ThumbPath\|LargePath\|HasThumb\|HasSyno\|SynoThumbPath\|SynoLargePath\)/X/g' \
  | sort | uniq -c | sort -rn | head -40
```

Expected: 出てくる差分が、`-` と `+` で対になっている（識別子だけが変わった）行に限られること。片側にしか無いアサーション行が残っていたら、期待値そのものを書き換えてしまっている。Global Constraints 違反なので、進めずに報告する。

Run: `grep -n 'ThumbSource = "' internal/photo/photo.go`
Expected: `""`、`"famifo"`、`"eadir"`

- [ ] **Step 5: 実際のデータで起動する（任意だが推奨）**

既存の `famifo-data` をそのまま使って起動し、再インデックスが走らないことを確かめる。DBスキーマと `thumb_source` の値が不変であることの実地確認になる。

```bash
./famifo-proto -dir <写真ディレクトリ> -data ./famifo-data -addr :8080
```

Expected: 起動ログの「フルスキャンが完了」で `indexed=0` に近く `unchanged` が既存の枚数と一致すること。`indexed` が全件になっていたら、`thumb_source` の値かパスの導出が変わっている。

---

## Self-Review

**Spec coverage:**

| spec の項目 | 対応タスク |
|---|---|
| `internal/synology` を切り出す（パス関数） | Task 1 |
| 除外ディレクトリ名を `synology` へ | Task 2 |
| ドメイン型を `store` → `photo` | Task 3 |
| `ErrNotFound` と `DayGroup` は `store` に残す | Task 3 / Step 2 |
| `CachePath` を `thumb` → `photo` | Task 4 |
| `photo.ThumbPath` / `photo.FullPath` を新設 | Task 5 |
| MIMEの分岐を消す | Task 5 / Step 5 |
| `buildRange` が enum を直接見なくなる | Task 5 / Step 5 |
| `web` を `view.go` に分割 | Task 6 |
| `thumb` は生成専用になる | Task 1 / Step 5、Task 4 / Step 5 |
| `takenat` は現状のまま | 触れるタスクなし（意図的） |
| 依存関係が spec の表と一致 | Task 7 / Step 1 |
| 既存テストのアサーションを変えない | Task 7 / Step 4 |
| DBスキーマ・`thumb_source` の値が不変 | Task 3 / Step 6、Task 7 / Step 4-5 |
| ブラウザテストが無変更で通る | Task 7 / Step 3 |
| EXIF二重パースは対象外 | 触れるタスクなし（意図的） |
| `main.go` のバージョン表示切り出しは見送り | 触れるタスクなし（意図的） |

**Type consistency:** `photo.ThumbPath(p Photo, cacheDir string) (string, bool)` は Task 5 で定義し、Task 5 Step 5 と Task 6 Step 1 で同じシグネチャで呼んでいる。`synology.ThumbPath(srcPath string) string` は名前が同じだがパッケージが違い、引数も戻り値も別物である点に注意（`photo.ThumbPath` は `Photo` と `cacheDir` を取って `(string, bool)`、`synology.ThumbPath` は `srcPath` を取って `string`）。`photo.FullPath` は `synology.LargePath` を呼ぶ。`photo.CachePath(dir, id string) string` は Task 4 で定義し、Task 5 の `photo.ThumbPath` の中と `internal/thumb` の `Generator.Path` から呼ばれる。

**Placeholder scan:** 実行できない指示なし。すべてのコードステップに実際のコードが入っている。
