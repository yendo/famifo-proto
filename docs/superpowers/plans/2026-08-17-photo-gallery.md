# famifo 写真ギャラリー 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ローカルディスク上の写真を自動でインデックス化し、LAN内のスマホ/タブレットのブラウザから1つのフラットなギャラリーとして閲覧できる単一バイナリのGoアプリを作る。

**Architecture:** 単一プロセスが3つの役割を持つ。(1) 起動時のフルスキャンで写真ディレクトリを走査しSQLiteのインデックスと同期、(2) その後fsnotifyで変更を追従、(3) 同時にHTTPサーバーとしてサーバーサイドレンダリングしたHTMLを配信する。ブラウザ側はhtmxがサーバーの返すHTML断片を差し込むことで無限スクロールを実現し、ライトボックスとスワイプのみvanilla JSで書く。画像の実体はディスク上のファイル、SQLiteはメタデータのインデックスに徹する。

**Tech Stack:** Go 1.25 / `modernc.org/sqlite`（pure Go, cgo不要）/ `github.com/fsnotify/fsnotify` / `github.com/evanoberholster/imagemeta`（EXIF, HEIF対応）/ `golang.org/x/image`（WebPデコード + リサイズ）/ `html/template` + `embed` / htmx 2.0.7

**Spec:** `docs/design.md`

## Global Constraints

これらは全タスクの要件に暗黙に含まれる。

- **モジュールパス:** `github.com/yendo/famifo-proto`
- **Goバージョン:** `go.mod` の go ディレクティブは `1.25.0`（`modernc.org/sqlite v1.56.0` の要求）。ローカルのGoが1.24系でもtoolchain自動ダウンロードでビルドできることを確認済み。
- **cgo禁止:** すべてのビルド・テストは `CGO_ENABLED=0` で通ること。単一バイナリ配布のため。
- **依存バージョン（検証済み）:**
  - `github.com/evanoberholster/imagemeta v1.0.0`
  - `github.com/fsnotify/fsnotify v1.10.1`
  - `golang.org/x/image v0.45.0`
  - `modernc.org/sqlite v1.56.0`
  - `github.com/stretchr/testify v1.10.0`（テスト専用）
- **対象拡張子:** `.jpg .jpeg .png .gif .webp`（サムネイル生成する = KindRaster）、`.heic .heif`（サムネイル生成しない = KindOpaque）。判定は拡張子ベース・大文字小文字を区別しない。
- **動画は対象外。** 拡張子リストに追加しないこと。
- **並び順:** `taken_at DESC, id DESC` で固定。`taken_at` はEXIF撮影日時、無ければファイルのmtime。
- **削除はハードデリート:** DBレコードとサムネイルキャッシュの両方を消す。
- **エラー方針:** 個々のファイルの破損・権限エラーはログに残してスキップし、スキャン全体は継続する。プロセスを止めてよいのは設定エラーとDBオープン失敗のみ。
- **認証・TLSなし。** LAN内利用前提。ログイン画面やHTTPSの実装を足さないこと。
- **既存コードは無視する。** `cmd/`, `main.go`, `go.mod`, `go.sum`, ビルド済みバイナリ `famifo-proto` はTask 1で削除して作り直す。
- **ログ:** 標準の `log/slog` を使う。`slog.Logger` は各コンポーネントにコンストラクタ経由で渡す。
- **テスト:** `github.com/stretchr/testify/require` を使う。実行は `CGO_ENABLED=0 go test ./...`。

---

### Task 1: プロジェクト基盤とCLI設定

既存のcobraベースの雛形を捨て、モジュールを作り直し、コマンドライン引数の解析と検証を実装する。

**Files:**
- Delete: `cmd/root.go`, `cmd/root_test.go`, `cmd/scan.go`, `cmd/scan_test.go`, `cmd/version.go`, `cmd/version_test.go`, `go.mod`, `go.sum`, `famifo-proto`
- Create: `go.mod`（`go mod init` で生成）
- Create: `internal/config/config.go`
- Create: `main.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: なし（最初のタスク）
- Produces:
  - `config.Config` — フィールド `PhotoDir string`, `DataDir string`, `Addr string`, `ThumbSize int`
  - `config.Parse(args []string, stderr io.Writer) (Config, error)`
  - `(Config) Validate() error`
  - `(Config) DBPath() string`, `(Config) ThumbDir() string`

- [ ] **Step 1: 既存の雛形を削除してモジュールを作り直す**

```bash
cd /home/yendo/ghq/github.com/yendo/famifo-proto
git rm -r --cached cmd 2>/dev/null || true
rm -rf cmd go.mod go.sum main.go famifo-proto
go mod init github.com/yendo/famifo-proto
```

- [ ] **Step 2: 依存を追加する**

```bash
go get github.com/evanoberholster/imagemeta@v1.0.0
go get github.com/fsnotify/fsnotify@v1.10.1
go get golang.org/x/image@v0.45.0
go get modernc.org/sqlite@v1.56.0
go get github.com/stretchr/testify@v1.10.0
```

`go.mod` の go ディレクティブが `1.25.0` に上がることを確認する（`modernc.org/sqlite` の要求）。

- [ ] **Step 3: 失敗するテストを書く**

`internal/config/config_test.go`:

```go
package config

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUsesDefaults(t *testing.T) {
	dir := t.TempDir()

	got, err := Parse([]string{"-dir", dir}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, dir, got.PhotoDir)
	require.Equal(t, "./famifo-data", got.DataDir)
	require.Equal(t, ":8080", got.Addr)
	require.Equal(t, 480, got.ThumbSize)
}

func TestParseOverridesEveryFlag(t *testing.T) {
	dir := t.TempDir()

	got, err := Parse([]string{
		"-dir", dir, "-data", "/var/famifo", "-addr", "192.168.1.10:9000", "-thumb", "320",
	}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, "/var/famifo", got.DataDir)
	require.Equal(t, "192.168.1.10:9000", got.Addr)
	require.Equal(t, 320, got.ThumbSize)
}

func TestParseRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	tests := map[string][]string{
		"dirが未指定":         {},
		"dirが存在しない":       {"-dir", filepath.Join(dir, "nope")},
		"dirがディレクトリではない": {"-dir", file},
		"thumbが小さすぎる":     {"-dir", dir, "-thumb", "0"},
		"thumbが大きすぎる":     {"-dir", dir, "-thumb", "4097"},
		"addrが空":          {"-dir", dir, "-addr", ""},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(args, io.Discard)
			require.Error(t, err)
		})
	}
}

func TestDerivedPaths(t *testing.T) {
	c := Config{DataDir: "/var/famifo"}

	require.Equal(t, "/var/famifo/famifo.db", c.DBPath())
	require.Equal(t, "/var/famifo/thumbs", c.ThumbDir())
}
```

- [ ] **Step 4: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/config/ -v`
Expected: コンパイルエラー `undefined: Parse`

- [ ] **Step 5: 実装する**

`internal/config/config.go`:

```go
// Package config はコマンドライン引数の解析と検証を担う。
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Config はアプリの実行時設定。すべてコマンドライン引数から与えられる。
type Config struct {
	PhotoDir  string // 写真を収集するルートディレクトリ
	DataDir   string // DBとサムネイルキャッシュの置き場
	Addr      string // HTTPの待ち受けアドレス
	ThumbSize int    // サムネイルの長辺ピクセル数
}

// Parse は引数を解析して検証済みのConfigを返す。argsにはプログラム名を含めない。
func Parse(args []string, stderr io.Writer) (Config, error) {
	fs := flag.NewFlagSet("famifo", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var c Config
	fs.StringVar(&c.PhotoDir, "dir", "", "写真を収集するディレクトリ (必須)")
	fs.StringVar(&c.DataDir, "data", "./famifo-data", "DBとサムネイルキャッシュの保存先")
	fs.StringVar(&c.Addr, "addr", ":8080", "HTTPの待ち受けアドレス")
	fs.IntVar(&c.ThumbSize, "thumb", 480, "サムネイルの長辺ピクセル数")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return c, c.Validate()
}

// Validate は設定の不備を報告する。ここでのエラーは起動を中止させる。
func (c Config) Validate() error {
	if c.PhotoDir == "" {
		return errors.New("-dir は必須です")
	}
	fi, err := os.Stat(c.PhotoDir)
	if err != nil {
		return fmt.Errorf("-dir を読めません: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("-dir はディレクトリではありません: %s", c.PhotoDir)
	}
	if c.ThumbSize < 1 || c.ThumbSize > 4096 {
		return fmt.Errorf("-thumb は 1..4096 で指定してください: %d", c.ThumbSize)
	}
	if c.Addr == "" {
		return errors.New("-addr は必須です")
	}
	return nil
}

// DBPath はSQLiteファイルのパスを返す。
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "famifo.db") }

// ThumbDir はサムネイルキャッシュのルートを返す。
func (c Config) ThumbDir() string { return filepath.Join(c.DataDir, "thumbs") }
```

`main.go`（この時点では設定を表示するだけ。Task 12で本結線する）:

```go
package main

import (
	"fmt"
	"os"

	"github.com/yendo/famifo-proto/internal/config"
)

func main() {
	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("dir=%s data=%s addr=%s thumb=%d\n",
		cfg.PhotoDir, cfg.DataDir, cfg.Addr, cfg.ThumbSize)
}
```

- [ ] **Step 6: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/config/ -v && CGO_ENABLED=0 go build ./...`
Expected: 全テストPASS、ビルド成功

- [ ] **Step 7: `.gitignore` にデータディレクトリとバイナリを追加する**

`.gitignore` の末尾に追記:

```
# famifo
/famifo-proto
/famifo-data/
```

- [ ] **Step 8: コミット**

```bash
git add -A
git commit -m "feat: モジュールを作り直しCLI設定を実装"
```

---

### Task 2: 対応フォーマットの判定

拡張子から「インデックス対象か」「サムネイルを作れるか」を判定する小さなドメインパッケージ。以降のほぼ全タスクが依存する。

**Files:**
- Create: `internal/photo/photo.go`
- Test: `internal/photo/photo_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `photo.Kind` — `photo.KindUnsupported`, `photo.KindRaster`, `photo.KindOpaque`
  - `photo.KindOf(name string) Kind`
  - `photo.IsSupported(name string) bool`
  - `photo.ContentType(name string) string`

- [ ] **Step 1: 失敗するテストを書く**

`internal/photo/photo_test.go`:

```go
package photo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKindOf(t *testing.T) {
	tests := map[string]Kind{
		"a.jpg":            KindRaster,
		"a.jpeg":           KindRaster,
		"a.png":            KindRaster,
		"a.gif":            KindRaster,
		"a.webp":           KindRaster,
		"A.JPG":            KindRaster, // 大文字小文字を区別しない
		"a.heic":           KindOpaque, // デコードせず素のまま配信する
		"a.HEIF":           KindOpaque,
		"a.mp4":            KindUnsupported, // 動画は対象外
		"a.mov":            KindUnsupported,
		"a.txt":            KindUnsupported,
		"noext":            KindUnsupported,
		"/photos/2020/b.png": KindRaster, // フルパスでも拡張子で判定する
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want, KindOf(name))
		})
	}
}

func TestIsSupported(t *testing.T) {
	require.True(t, IsSupported("a.jpg"))
	require.True(t, IsSupported("a.heic"))
	require.False(t, IsSupported("a.mp4"))
}

func TestContentType(t *testing.T) {
	tests := map[string]string{
		"a.jpg":  "image/jpeg",
		"a.jpeg": "image/jpeg",
		"a.png":  "image/png",
		"a.gif":  "image/gif",
		"a.webp": "image/webp",
		"a.heic": "image/heic",
		"a.heif": "image/heif",
		"a.txt":  "application/octet-stream",
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want, ContentType(name))
		})
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/photo/ -v`
Expected: コンパイルエラー `undefined: KindOf`

- [ ] **Step 3: 実装する**

`internal/photo/photo.go`:

```go
// Package photo は写真ファイルの分類を担う。判定は拡張子のみに基づき、
// ファイルの中身は読まない（fsnotifyの大量イベントを軽く捌くため）。
package photo

import (
	"path/filepath"
	"strings"
)

// Kind は写真ファイルの扱い方を表す。
type Kind int

const (
	// KindUnsupported はインデックス対象外のファイル。
	KindUnsupported Kind = iota
	// KindRaster はGoでデコードでき、サムネイルを生成するファイル。
	KindRaster
	// KindOpaque はインデックスはするがデコードせず、原本をそのまま配信するファイル。
	// HEIC/HEIFが該当する。Safari以外では表示できないが、設計上それを許容している。
	KindOpaque
)

var extKinds = map[string]Kind{
	".jpg":  KindRaster,
	".jpeg": KindRaster,
	".png":  KindRaster,
	".gif":  KindRaster,
	".webp": KindRaster,
	".heic": KindOpaque,
	".heif": KindOpaque,
}

var extTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".heic": "image/heic",
	".heif": "image/heif",
}

func ext(name string) string { return strings.ToLower(filepath.Ext(name)) }

// KindOf はファイル名の拡張子から扱い方を判定する。
func KindOf(name string) Kind { return extKinds[ext(name)] }

// IsSupported はインデックス対象にすべきファイルかを報告する。
func IsSupported(name string) bool { return KindOf(name) != KindUnsupported }

// ContentType は原本配信時に使うMIMEタイプを返す。
// HEIC/HEIFはGoの mime パッケージが知らないため自前で持つ。
func ContentType(name string) string {
	if t, ok := extTypes[ext(name)]; ok {
		return t
	}
	return "application/octet-stream"
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/photo/ -v`
Expected: 全テストPASS

- [ ] **Step 5: コミット**

```bash
git add internal/photo
git commit -m "feat: 拡張子ベースの対応フォーマット判定を追加"
```

---

### Task 3: SQLiteインデックスストア

写真メタデータの永続化層。スキーマ、Upsert、削除、カーソルページネーションを実装する。

**Files:**
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `store.Photo{ID, Path string; TakenAt, ModTime time.Time; Size int64; Ext string; HasThumb bool}`
  - `store.Cursor{TakenAt time.Time; ID string; Set bool}`
  - `store.IDFor(path string) string` — sha256(path)の先頭32桁hex
  - `store.ErrNotFound`
  - `store.Open(dbPath string) (*Store, error)`
  - `(*Store) Close() error`
  - `(*Store) Upsert(ctx context.Context, p Photo) error`
  - `(*Store) GetByID(ctx context.Context, id string) (Photo, error)`
  - `(*Store) DeleteByPath(ctx context.Context, path string) (Photo, bool, error)`
  - `(*Store) ListPage(ctx context.Context, cur Cursor, limit int) ([]Photo, error)`
  - `(*Store) AllPaths(ctx context.Context) (map[string]int64, error)` — path→mod_time(unix)
  - `(*Store) Count(ctx context.Context) (int, error)`

**設計メモ:** DSNでWALを有効にする。スキャン中の書き込みとHTTPの読み取りが並行するため必須。カーソルは行値比較を使わず `taken_at < ? OR (taken_at = ? AND id < ?)` と展開して書く（可読性と移植性のため）。

- [ ] **Step 1: 失敗するテストを書く**

`internal/store/store_test.go`:

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func photoAt(path string, takenAt time.Time) Photo {
	return Photo{
		ID:       IDFor(path),
		Path:     path,
		TakenAt:  takenAt,
		ModTime:  takenAt,
		Size:     1234,
		Ext:      ".jpg",
		HasThumb: true,
	}
}

func TestIDForIsStableAndDistinct(t *testing.T) {
	a := IDFor("/photos/a.jpg")

	require.Len(t, a, 32)
	require.Equal(t, a, IDFor("/photos/a.jpg"))
	require.NotEqual(t, a, IDFor("/photos/b.jpg"))
}

func TestOpenEnablesWAL(t *testing.T) {
	s := openTestStore(t)

	var mode string
	require.NoError(t, s.db.QueryRow("PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "wal", mode)
}

func TestUpsertThenGetByID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	want := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))

	require.NoError(t, s.Upsert(ctx, want))
	got, err := s.GetByID(ctx, want.ID)

	require.NoError(t, err)
	require.Equal(t, want.Path, got.Path)
	require.Equal(t, want.TakenAt.Unix(), got.TakenAt.Unix())
	require.Equal(t, want.Size, got.Size)
	require.True(t, got.HasThumb)
}

func TestUpsertReplacesExistingRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))
	require.NoError(t, s.Upsert(ctx, p))

	p.TakenAt = time.Unix(1700000000, 0)
	p.HasThumb = false
	require.NoError(t, s.Upsert(ctx, p))

	got, err := s.GetByID(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1700000000), got.TakenAt.Unix())
	require.False(t, got.HasThumb)

	n, err := s.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestGetByIDMissingReturnsErrNotFound(t *testing.T) {
	s := openTestStore(t)

	_, err := s.GetByID(context.Background(), "deadbeef")

	require.ErrorIs(t, err, ErrNotFound)
}

func TestDeleteByPath(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))
	require.NoError(t, s.Upsert(ctx, p))

	got, ok, err := s.DeleteByPath(ctx, p.Path)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, p.ID, got.ID)
	require.True(t, got.HasThumb) // サムネイル削除の判断に使う

	_, ok, err = s.DeleteByPath(ctx, p.Path)
	require.NoError(t, err)
	require.False(t, ok, "2回目の削除は見つからないと報告する")
}

func TestListPagePaginatesNewestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 撮影日時が新しい順に c, b, a になるよう投入する
	for i, name := range []string{"a", "b", "c"} {
		p := photoAt("/photos/"+name+".jpg", time.Unix(int64(1600000000+i), 0))
		require.NoError(t, s.Upsert(ctx, p))
	}

	first, err := s.ListPage(ctx, Cursor{}, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, "/photos/c.jpg", first[0].Path)
	require.Equal(t, "/photos/b.jpg", first[1].Path)

	last := first[len(first)-1]
	second, err := s.ListPage(ctx, Cursor{TakenAt: last.TakenAt, ID: last.ID, Set: true}, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, "/photos/a.jpg", second[0].Path)
}

func TestListPageBreaksTiesByID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	same := time.Unix(1600000000, 0)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, s.Upsert(ctx, photoAt("/photos/"+name+".jpg", same)))
	}

	// 撮影日時が全て同じでも、ページをまたいで重複や欠落が起きないこと
	var seen []string
	cur := Cursor{}
	for range 3 {
		page, err := s.ListPage(ctx, cur, 1)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		seen = append(seen, page[0].Path)
		cur = Cursor{TakenAt: page[0].TakenAt, ID: page[0].ID, Set: true}
	}

	require.ElementsMatch(t, []string{"/photos/a.jpg", "/photos/b.jpg", "/photos/c.jpg"}, seen)
}

func TestAllPaths(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))
	p.ModTime = time.Unix(1650000000, 0)
	require.NoError(t, s.Upsert(ctx, p))

	got, err := s.AllPaths(ctx)

	require.NoError(t, err)
	require.Equal(t, map[string]int64{"/photos/a.jpg": 1650000000}, got)
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/store/ -v`
Expected: コンパイルエラー `undefined: Open`

- [ ] **Step 3: 実装する**

`internal/store/store.go`:

```go
// Package store は写真メタデータのSQLiteインデックスを提供する。
// 画像の実体はファイルシステム上にあり、ここではパスとメタデータだけを持つ。
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure Goのsqliteドライバ。cgo不要。
)

// ErrNotFound は該当する写真が無いことを表す。
var ErrNotFound = errors.New("photo not found")

// Photo はインデックス上の1枚の写真。
type Photo struct {
	ID       string    // パスから導出した安定ID。URLに露出させる
	Path     string    // ディスク上の絶対パス
	TakenAt  time.Time // EXIF撮影日時、無ければmtime
	ModTime  time.Time // ファイルのmtime。再スキャン時の変更検知に使う
	Size     int64
	Ext      string // 小文字の拡張子（"." 込み）
	HasThumb bool   // サムネイルキャッシュが存在するか
}

// Cursor はページネーションの位置。Setがfalseなら先頭ページ。
type Cursor struct {
	TakenAt time.Time
	ID      string
	Set     bool
}

// IDFor はパスから安定したIDを導出する。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func IDFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}

// Store はSQLiteインデックスへのアクセスを提供する。
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS photos (
    id        TEXT PRIMARY KEY,
    path      TEXT NOT NULL UNIQUE,
    taken_at  INTEGER NOT NULL,
    mod_time  INTEGER NOT NULL,
    size      INTEGER NOT NULL,
    ext       TEXT NOT NULL,
    has_thumb INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_photos_order ON photos(taken_at DESC, id DESC);
`

// Open はDBを開き、スキーマを作成する。
// WALを有効にしてスキャン中の書き込みと配信中の読み取りを並行させる。
func Open(dbPath string) (*Store, error) {
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("DBを開けません: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("DBに接続できません: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("スキーマを作成できません: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const upsertSQL = `
INSERT INTO photos (id, path, taken_at, mod_time, size, ext, has_thumb)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    path      = excluded.path,
    taken_at  = excluded.taken_at,
    mod_time  = excluded.mod_time,
    size      = excluded.size,
    ext       = excluded.ext,
    has_thumb = excluded.has_thumb`

// Upsert は写真を登録または更新する。
func (s *Store) Upsert(ctx context.Context, p Photo) error {
	_, err := s.db.ExecContext(ctx, upsertSQL,
		p.ID, p.Path, p.TakenAt.Unix(), p.ModTime.Unix(), p.Size, p.Ext, p.HasThumb)
	if err != nil {
		return fmt.Errorf("写真を保存できません (%s): %w", p.Path, err)
	}
	return nil
}

const selectCols = `id, path, taken_at, mod_time, size, ext, has_thumb`

func scanPhoto(row interface{ Scan(...any) error }) (Photo, error) {
	var p Photo
	var takenAt, modTime int64
	if err := row.Scan(&p.ID, &p.Path, &takenAt, &modTime, &p.Size, &p.Ext, &p.HasThumb); err != nil {
		return Photo{}, err
	}
	p.TakenAt = time.Unix(takenAt, 0)
	p.ModTime = time.Unix(modTime, 0)
	return p, nil
}

// GetByID はIDで写真を引く。見つからない場合は ErrNotFound を返す。
func (s *Store) GetByID(ctx context.Context, id string) (Photo, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM photos WHERE id = ?`, id)
	p, err := scanPhoto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	if err != nil {
		return Photo{}, fmt.Errorf("写真を取得できません: %w", err)
	}
	return p, nil
}

// DeleteByPath はパスで写真を削除し、削除した行を返す。
// 呼び出し側はサムネイルを消すかどうかの判断に HasThumb を使う。
// 該当が無い場合は ok=false を返し、エラーにはしない。
func (s *Store) DeleteByPath(ctx context.Context, path string) (Photo, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM photos WHERE path = ? RETURNING `+selectCols, path)
	p, err := scanPhoto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, false, nil
	}
	if err != nil {
		return Photo{}, false, fmt.Errorf("写真を削除できません (%s): %w", path, err)
	}
	return p, true, nil
}

// ListPage は撮影日時の新しい順に1ページ分を返す。
// 同一撮影日時の写真が並んでもページ境界で重複・欠落しないよう、IDを第2キーにする。
func (s *Store) ListPage(ctx context.Context, cur Cursor, limit int) ([]Photo, error) {
	query := `SELECT ` + selectCols + ` FROM photos ORDER BY taken_at DESC, id DESC LIMIT ?`
	args := []any{limit}
	if cur.Set {
		query = `SELECT ` + selectCols + ` FROM photos
		         WHERE taken_at < ? OR (taken_at = ? AND id < ?)
		         ORDER BY taken_at DESC, id DESC LIMIT ?`
		at := cur.TakenAt.Unix()
		args = []any{at, at, cur.ID, limit}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("一覧を取得できません: %w", err)
	}
	defer rows.Close()

	var out []Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("一覧を読めません: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllPaths は登録済みの全パスとそのmtimeを返す。フルスキャンでの差分検出に使う。
func (s *Store) AllPaths(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, mod_time FROM photos`)
	if err != nil {
		return nil, fmt.Errorf("パス一覧を取得できません: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var path string
		var modTime int64
		if err := rows.Scan(&path, &modTime); err != nil {
			return nil, fmt.Errorf("パス一覧を読めません: %w", err)
		}
		out[path] = modTime
	}
	return out, rows.Err()
}

// Count は登録枚数を返す。
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM photos`).Scan(&n); err != nil {
		return 0, fmt.Errorf("枚数を取得できません: %w", err)
	}
	return n, nil
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/store/ -v`
Expected: 全テストPASS

- [ ] **Step 5: コミット**

```bash
git add internal/store
git commit -m "feat: SQLiteインデックスストアを追加"
```

---

### Task 4: 撮影日時の解決

EXIFの撮影日時を読み、取れなければファイルのmtimeにフォールバックする。

**Files:**
- Create: `internal/takenat/takenat.go`
- Test: `internal/takenat/takenat_test.go`
- Test helper: `internal/takenat/testdata_test.go`

**Interfaces:**
- Consumes: なし
- Produces: `takenat.Resolve(path string, modTime time.Time) time.Time`

**設計メモ:** `imagemeta.Decode` はフォーマットを自動判別し、JPEG/PNG/TIFF/HEIF/AVIF/RAWに対応する。GIFとWebPはEXIFを持たないためエラーになるが、その場合はmtimeにフォールバックするので問題ない。`SelectedDate()` は DateTimeOriginal → CreateDate → ModifyDate の順で最初に見つかったものを返し、どれも無ければゼロ値を返す。**エラーは一切呼び出し側に返さない** — 撮影日時が読めないことは正常系であり、必ず何らかの日時が決まることを保証する。

- [ ] **Step 1: テスト用のEXIF付きJPEG生成ヘルパーを書く**

`internal/takenat/testdata_test.go`:

```go
package takenat

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeJPEGWithEXIF はDateTimeOriginalを持つ最小のJPEGを書き出す。
// TIFFブロックを手で組み立て、APP1セグメントとしてSOI直後に差し込んでいる。
func writeJPEGWithEXIF(t *testing.T, dir, name string, when time.Time) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	var body bytes.Buffer
	require.NoError(t, jpeg.Encode(&body, img, nil))

	le := binary.LittleEndian
	var tiff bytes.Buffer
	tiff.WriteString("II")                  // リトルエンディアン
	binary.Write(&tiff, le, uint16(42))     // TIFFマジック
	binary.Write(&tiff, le, uint32(8))      // IFD0のオフセット

	binary.Write(&tiff, le, uint16(1))      // IFD0: エントリ1件
	binary.Write(&tiff, le, uint16(0x8769)) // ExifIFDPointer
	binary.Write(&tiff, le, uint16(4))      // LONG
	binary.Write(&tiff, le, uint32(1))
	binary.Write(&tiff, le, uint32(26))     // ExifIFDのオフセット
	binary.Write(&tiff, le, uint32(0))      // 次のIFDなし

	binary.Write(&tiff, le, uint16(1))      // ExifIFD: エントリ1件
	binary.Write(&tiff, le, uint16(0x9003)) // DateTimeOriginal
	binary.Write(&tiff, le, uint16(2))      // ASCII
	binary.Write(&tiff, le, uint32(20))
	binary.Write(&tiff, le, uint32(44))     // 値のオフセット
	binary.Write(&tiff, le, uint32(0))      // 次のIFDなし

	tiff.WriteString(when.Format("2006:01:02 15:04:05"))
	tiff.WriteByte(0)

	var app1 bytes.Buffer
	app1.Write([]byte{0xFF, 0xE1})
	binary.Write(&app1, binary.BigEndian, uint16(2+6+tiff.Len()))
	app1.WriteString("Exif\x00\x00")
	app1.Write(tiff.Bytes())

	out := append([]byte{0xFF, 0xD8}, app1.Bytes()...)
	out = append(out, body.Bytes()[2:]...) // SOIを除いた残りを連結

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, out, 0o644))
	return path
}

// writeJPEGWithoutEXIF はEXIFを持たないJPEGを書き出す。
func writeJPEGWithoutEXIF(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}
```

- [ ] **Step 2: 失敗するテストを書く**

`internal/takenat/takenat_test.go`:

```go
package takenat

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveUsesEXIFWhenPresent(t *testing.T) {
	dir := t.TempDir()
	want := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	path := writeJPEGWithEXIF(t, dir, "a.jpg", want)
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	require.True(t, got.Equal(want), "EXIFの撮影日時を優先する: got=%v want=%v", got, want)
}

func TestResolveFallsBackToModTimeWhenNoEXIF(t *testing.T) {
	dir := t.TempDir()
	path := writeJPEGWithoutEXIF(t, dir, "a.jpg")
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	require.True(t, got.Equal(mtime))
}

func TestResolveFallsBackForNonEXIFFormats(t *testing.T) {
	dir := t.TempDir()
	// GIFやWebPはEXIFを持たない。デコードが失敗してもmtimeで必ず決まること。
	path := filepath.Join(dir, "a.gif")
	require.NoError(t, os.WriteFile(path, []byte("GIF89a not really a gif"), 0o644))
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	require.True(t, got.Equal(mtime))
}

func TestResolveFallsBackWhenFileMissing(t *testing.T) {
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(filepath.Join(t.TempDir(), "nope.jpg"), mtime)

	require.True(t, got.Equal(mtime))
}
```

- [ ] **Step 3: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/takenat/ -v`
Expected: コンパイルエラー `undefined: Resolve`

- [ ] **Step 4: 実装する**

`internal/takenat/takenat.go`:

```go
// Package takenat は写真の「撮影日時」を決める。
// EXIFがあればそれを、無ければファイルのmtimeを使う。
package takenat

import (
	"os"
	"time"

	"github.com/evanoberholster/imagemeta"
)

// Resolve は写真の撮影日時を返す。
//
// EXIFの読み取りに失敗しても呼び出し側にエラーは返さない。撮影日時が取れない
// ファイル（スクリーンショット、GIF、WebP、EXIFを削ぎ落とされた画像）は普通に
// 存在し、それらを一覧から落とさないために必ずmodTimeで代替する。
func Resolve(path string, modTime time.Time) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return modTime
	}
	defer f.Close()

	ex, err := imagemeta.Decode(f)
	if err != nil {
		return modTime
	}
	// SelectedDate は DateTimeOriginal → CreateDate → ModifyDate の順に探し、
	// どれも無ければゼロ値を返す。
	if d := ex.SelectedDate(); !d.IsZero() {
		return d
	}
	return modTime
}
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/takenat/ -v`
Expected: 全テストPASS（特に `TestResolveUsesEXIFWhenPresent` が `2021-03-04T05:06:07Z` を返すこと）

- [ ] **Step 6: コミット**

```bash
git add internal/takenat
git commit -m "feat: EXIF撮影日時の解決とmtimeフォールバックを追加"
```

---

### Task 5: サムネイル生成

デコード可能な画像から長辺固定のJPEGサムネイルを作り、シャーディングしたキャッシュディレクトリに保存する。

**Files:**
- Create: `internal/thumb/thumb.go`
- Test: `internal/thumb/thumb_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `thumb.RelPath(id string) string` — `"ab/abcdef....jpg"`
  - `thumb.NewGenerator(dir string, size int) (*Generator, error)`
  - `(*Generator) Path(id string) string`
  - `(*Generator) Generate(srcPath, id string) error`
  - `(*Generator) Remove(id string) error`

**設計メモ:** キャッシュはIDの先頭2文字でサブディレクトリに分割する（1ディレクトリに数万ファイルが並ぶのを避けるため）。書き込みは一時ファイル + `os.Rename` で原子的に行う。生成中の半端なファイルをHTTPハンドラが配信してしまうのを防ぐため。HEICはそもそも呼ばれない（Task 6のIndexerが `KindOpaque` を除外する）。

- [ ] **Step 1: 失敗するテストを書く**

`internal/thumb/thumb_test.go`:

```go
package thumb

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const testID = "abcdef0123456789abcdef0123456789"

func writeImage(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	switch filepath.Ext(name) {
	case ".png":
		require.NoError(t, png.Encode(&buf, img))
	case ".gif":
		require.NoError(t, gif.Encode(&buf, img, nil))
	default:
		require.NoError(t, jpeg.Encode(&buf, img, nil))
	}
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

func newTestGenerator(t *testing.T, size int) *Generator {
	t.Helper()
	g, err := NewGenerator(filepath.Join(t.TempDir(), "thumbs"), size)
	require.NoError(t, err)
	return g
}

func decodeThumb(t *testing.T, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	require.NoError(t, err)
	require.Equal(t, "jpeg", format, "サムネイルは常にJPEGで書き出す")
	return cfg
}

func TestRelPathShardsByFirstTwoChars(t *testing.T) {
	require.Equal(t, filepath.Join("ab", testID+".jpg"), RelPath(testID))
}

func TestGenerateScalesLandscapeByLongEdge(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)

	require.NoError(t, g.Generate(src, testID))

	cfg := decodeThumb(t, g.Path(testID))
	require.Equal(t, 100, cfg.Width)
	require.Equal(t, 50, cfg.Height, "アスペクト比を保つ")
}

func TestGenerateScalesPortraitByLongEdge(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 200, 400)

	require.NoError(t, g.Generate(src, testID))

	cfg := decodeThumb(t, g.Path(testID))
	require.Equal(t, 50, cfg.Width)
	require.Equal(t, 100, cfg.Height)
}

func TestGenerateDoesNotUpscale(t *testing.T) {
	g := newTestGenerator(t, 500)
	src := writeImage(t, t.TempDir(), "a.jpg", 40, 20)

	require.NoError(t, g.Generate(src, testID))

	cfg := decodeThumb(t, g.Path(testID))
	require.Equal(t, 40, cfg.Width)
	require.Equal(t, 20, cfg.Height)
}

func TestGenerateAcceptsPNGAndGIF(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.png", "a.gif"} {
		t.Run(name, func(t *testing.T) {
			g := newTestGenerator(t, 100)
			src := writeImage(t, dir, name, 400, 200)

			require.NoError(t, g.Generate(src, testID))

			require.Equal(t, 100, decodeThumb(t, g.Path(testID)).Width)
		})
	}
}

func TestGenerateFailsOnUndecodableFile(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := filepath.Join(t.TempDir(), "broken.jpg")
	require.NoError(t, os.WriteFile(src, []byte("this is not an image"), 0o644))

	err := g.Generate(src, testID)

	require.Error(t, err)
	require.NoFileExists(t, g.Path(testID), "失敗時に中途半端なファイルを残さない")
}

func TestGenerateFailsOnMissingFile(t *testing.T) {
	g := newTestGenerator(t, 100)

	require.Error(t, g.Generate(filepath.Join(t.TempDir(), "nope.jpg"), testID))
}

func TestRemove(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)
	require.NoError(t, g.Generate(src, testID))

	require.NoError(t, g.Remove(testID))

	require.NoFileExists(t, g.Path(testID))
	require.NoError(t, g.Remove(testID), "存在しないサムネイルの削除はエラーにしない")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/thumb/ -v`
Expected: コンパイルエラー `undefined: NewGenerator`

- [ ] **Step 3: 実装する**

`internal/thumb/thumb.go`:

```go
// Package thumb は一覧表示用のサムネイルを生成・管理する。
// 出力は常にJPEG。HEICはデコードしない方針のためここには来ない。
package thumb

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io/fs"
	"os"
	"path/filepath"

	_ "image/gif"  // image.Decode にGIFを登録する
	_ "image/png"  // image.Decode にPNGを登録する

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // image.Decode にWebPを登録する（デコードのみ）
)

// jpegQuality はサムネイルの画質。一覧表示に十分で、かつ十分軽い値。
const jpegQuality = 82

// Generator はサムネイルキャッシュを管理する。
type Generator struct {
	dir  string
	size int // 長辺の最大ピクセル数
}

// NewGenerator はキャッシュディレクトリを用意してGeneratorを返す。
func NewGenerator(dir string, size int) (*Generator, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("サムネイルディレクトリを作れません: %w", err)
	}
	return &Generator{dir: dir, size: size}, nil
}

// RelPath はキャッシュ内の相対パスを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func RelPath(id string) string { return filepath.Join(id[:2], id+".jpg") }

// Path はサムネイルの絶対パスを返す。
func (g *Generator) Path(id string) string { return filepath.Join(g.dir, RelPath(id)) }

// Generate は srcPath の画像からサムネイルを作る。
// デコードできないファイルはエラーを返し、キャッシュには何も残さない。
func (g *Generator) Generate(srcPath, id string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("画像を開けません: %w", err)
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return fmt.Errorf("画像をデコードできません: %w", err)
	}

	out := g.Path(id)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return fmt.Errorf("サムネイルの保存先を作れません: %w", err)
	}

	// 一時ファイルに書いてからrenameする。生成途中のファイルをHTTPハンドラが
	// 掴んでしまわないようにするため。
	tmp, err := os.CreateTemp(filepath.Dir(out), ".tmp-*")
	if err != nil {
		return fmt.Errorf("一時ファイルを作れません: %w", err)
	}
	defer os.Remove(tmp.Name()) // renameが成功していれば消す対象は無い

	if err := jpeg.Encode(tmp, scaleToFit(src, g.size), &jpeg.Options{Quality: jpegQuality}); err != nil {
		tmp.Close()
		return fmt.Errorf("サムネイルを書き出せません: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("一時ファイルを閉じられません: %w", err)
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return fmt.Errorf("サムネイルを配置できません: %w", err)
	}
	return nil
}

// Remove はサムネイルを削除する。存在しない場合はエラーにしない。
func (g *Generator) Remove(id string) error {
	if err := os.Remove(g.Path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("サムネイルを削除できません: %w", err)
	}
	return nil
}

// scaleToFit は長辺が max 以下になるよう縮小する。元より大きくは引き伸ばさない。
func scaleToFit(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	if w >= h {
		w, h = max, h*max/w
	} else {
		w, h = w*max/h, max
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/thumb/ -v`
Expected: 全テストPASS

- [ ] **Step 5: コミット**

```bash
git add internal/thumb
git commit -m "feat: サムネイル生成を追加"
```

---

### Task 6: インデクサ（1ファイル単位の登録・削除）

Task 2〜5を結線し、「1つのファイルパスをインデックスに反映する」という単位の処理を作る。フルスキャン（Task 7）とfsnotify監視（Task 8）が共通で使う。

**Files:**
- Create: `internal/index/indexer.go`
- Test: `internal/index/indexer_test.go`

**Interfaces:**
- Consumes: `photo.KindOf`, `photo.KindUnsupported`, `photo.KindRaster`, `store.Photo`, `store.IDFor`, `(*store.Store).Upsert`, `(*store.Store).DeleteByPath`, `takenat.Resolve`, `(*thumb.Generator).Generate`, `(*thumb.Generator).Remove`
- Produces:
  - `index.New(root string, st *store.Store, gen *thumb.Generator, log *slog.Logger) *Indexer`
  - `(*Indexer) IndexFile(ctx context.Context, path string) error`
  - `(*Indexer) RemoveFile(ctx context.Context, path string) error`

**設計メモ:** `KindRaster` のサムネイル生成に失敗したら **インデックスもしない**（エラーを返す）。壊れた画像を登録すると一覧に読み込めない `<img>` が並ぶため。呼び出し側がログを出してスキップする。対象外拡張子は「エラーではない、単に無視する」ので `nil` を返す。

- [ ] **Step 1: 失敗するテストを書く**

`internal/index/indexer_test.go`:

```go
package index

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

type fixture struct {
	ix    *Indexer
	st    *store.Store
	gen   *thumb.Generator
	root  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "photos")
	require.NoError(t, os.MkdirAll(root, 0o755))

	st, err := store.Open(filepath.Join(base, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	gen, err := thumb.NewGenerator(filepath.Join(base, "thumbs"), 100)
	require.NoError(t, err)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{ix: New(root, st, gen, log), st: st, gen: gen, root: root}
}

func TestIndexFileStoresRasterPhotoWithThumb(t *testing.T) {
	f := newFixture(t)
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), store.IDFor(path))
	require.NoError(t, err)
	require.Equal(t, path, got.Path)
	require.Equal(t, ".jpg", got.Ext)
	require.True(t, got.HasThumb)
	require.FileExists(t, f.gen.Path(got.ID))
}

func TestIndexFileStoresHEICWithoutThumb(t *testing.T) {
	f := newFixture(t)
	// HEICはデコードしない方針なので、中身が画像でなくても登録される
	path := filepath.Join(f.root, "a.heic")
	require.NoError(t, os.WriteFile(path, []byte("not decodable by go"), 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path))

	got, err := f.st.GetByID(context.Background(), store.IDFor(path))
	require.NoError(t, err)
	require.False(t, got.HasThumb, "HEICはサムネイルを作らない")
	require.NoFileExists(t, f.gen.Path(got.ID))
}

func TestIndexFileIgnoresUnsupportedExtensions(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "a.mp4")
	require.NoError(t, os.WriteFile(path, []byte("video"), 0o644))

	require.NoError(t, f.ix.IndexFile(context.Background(), path), "対象外はエラーではない")

	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestIndexFileIgnoresDirectories(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.root, "sub.jpg") // 拡張子付きディレクトリという嫌がらせ
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.NoError(t, f.ix.IndexFile(context.Background(), dir))

	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestIndexFileRejectsBrokenRasterImage(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "broken.jpg")
	require.NoError(t, os.WriteFile(path, []byte("not an image"), 0o644))

	err := f.ix.IndexFile(context.Background(), path)

	require.Error(t, err, "壊れた画像は登録せずエラーを返す")
	n, cerr := f.st.Count(context.Background())
	require.NoError(t, cerr)
	require.Equal(t, 0, n)
}

func TestRemoveFileDeletesRowAndThumb(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 400, 200)
	require.NoError(t, f.ix.IndexFile(ctx, path))
	thumbPath := f.gen.Path(store.IDFor(path))
	require.FileExists(t, thumbPath)

	require.NoError(t, f.ix.RemoveFile(ctx, path))

	require.NoFileExists(t, thumbPath)
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

func TestRemoveFileIsQuietForUnknownPath(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.ix.RemoveFile(context.Background(), filepath.Join(f.root, "never.jpg")))
}
```

`internal/index/testdata_test.go`（テスト用のJPEG生成ヘルパー。Task 7・8のテストでも使う）:

```go
package index

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeTestJPEG は指定サイズのJPEGを書き出してそのパスを返す。
func writeTestJPEG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/index/ -v`
Expected: コンパイルエラー `undefined: New`

- [ ] **Step 3: 実装する**

`internal/index/indexer.go`:

```go
// Package index はディスク上の写真とSQLiteインデックスを同期させる。
// 起動時のフルスキャンとfsnotifyによる追従の両方をここで担う。
package index

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/takenat"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// Indexer は1ファイル単位でインデックスを更新する。
type Indexer struct {
	root string
	st   *store.Store
	gen  *thumb.Generator
	log  *slog.Logger
}

// New はIndexerを作る。rootは写真を収集するルートディレクトリ。
func New(root string, st *store.Store, gen *thumb.Generator, log *slog.Logger) *Indexer {
	return &Indexer{root: root, st: st, gen: gen, log: log}
}

// IndexFile は1ファイルをインデックスに反映する。
//
// 対象外の拡張子とディレクトリは黙って無視する（エラーではない）。
// KindRasterでサムネイルを作れなかった場合はエラーを返し、DBには登録しない。
// 壊れた画像を登録すると一覧に読み込めない <img> が並んでしまうため。
func (ix *Indexer) IndexFile(ctx context.Context, path string) error {
	kind := photo.KindOf(path)
	if kind == photo.KindUnsupported {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("ファイル情報を取得できません: %w", err)
	}
	if fi.IsDir() {
		return nil
	}

	p := store.Photo{
		ID:      store.IDFor(path),
		Path:    path,
		ModTime: fi.ModTime(),
		Size:    fi.Size(),
		Ext:     strings.ToLower(filepath.Ext(path)),
		TakenAt: takenat.Resolve(path, fi.ModTime()),
	}

	if kind == photo.KindRaster {
		if err := ix.gen.Generate(path, p.ID); err != nil {
			return err
		}
		p.HasThumb = true
	}

	return ix.st.Upsert(ctx, p)
}

// RemoveFile はインデックスとサムネイルの両方から写真を消す。
// 未登録のパスに対しては何もしない。
func (ix *Indexer) RemoveFile(ctx context.Context, path string) error {
	p, ok, err := ix.st.DeleteByPath(ctx, path)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if p.HasThumb {
		if err := ix.gen.Remove(p.ID); err != nil {
			// DBからは消えているので、キャッシュの消し残しは致命的ではない
			ix.log.Warn("サムネイルの削除に失敗", "id", p.ID, "err", err)
		}
	}
	return nil
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/index/ -v`
Expected: 全テストPASS

- [ ] **Step 5: コミット**

```bash
git add internal/index
git commit -m "feat: 1ファイル単位のインデクサを追加"
```

---

### Task 7: フルスキャン

起動時にルートディレクトリを走査し、追加・更新・削除をインデックスに反映する。

**Files:**
- Create: `internal/index/scan.go`
- Test: `internal/index/scan_test.go`

**Interfaces:**
- Consumes: `(*Indexer).IndexFile`, `(*Indexer).RemoveFile`, `(*store.Store).AllPaths`
- Produces:
  - `index.Stats{Indexed, Unchanged, Removed, Skipped int}`
  - `(*Indexer) FullScan(ctx context.Context) (Stats, error)`

**設計メモ:** 事前に `AllPaths` でDB上のパス→mtimeを取得し、走査しながら消し込む。走査後に残ったパスは「DBにはあるがディスクに無い」= 削除されたファイルなので、インデックスから消す。mtimeが変わっていないファイルは再インデックスしない（サムネイル再生成を避けるため）。個々のファイルのエラーは `Skipped` に数えて走査を続ける。

- [ ] **Step 1: 失敗するテストを書く**

`internal/index/scan_test.go`:

```go
package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yendo/famifo-proto/internal/store"
)

func TestFullScanIndexesNestedPhotos(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	writeTestJPEG(t, filepath.Join(f.root, "2020"), "b.jpg", 40, 20)
	writeTestJPEG(t, filepath.Join(f.root, "2020", "trip"), "c.jpg", 40, 20)

	stats, err := f.ix.FullScan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 3, stats.Indexed, "サブディレクトリも再帰的に走査する")
	n, err := f.st.Count(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

func TestFullScanIgnoresNonPhotos(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "clip.mp4"), []byte("x"), 0o644))

	stats, err := f.ix.FullScan(context.Background())

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed)
	require.Equal(t, 0, stats.Skipped, "対象外拡張子はスキップとして数えない")
}

func TestFullScanSkipsBrokenFilesAndContinues(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "good1.jpg", 40, 20)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "broken.jpg"), []byte("nope"), 0o644))
	writeTestJPEG(t, f.root, "good2.jpg", 40, 20)

	stats, err := f.ix.FullScan(context.Background())

	require.NoError(t, err, "1ファイルの破損で全体を止めない")
	require.Equal(t, 2, stats.Indexed)
	require.Equal(t, 1, stats.Skipped)
}

func TestFullScanSkipsUnchangedFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	first, err := f.ix.FullScan(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, first.Indexed)

	second, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 0, second.Indexed)
	require.Equal(t, 1, second.Unchanged, "mtimeが同じなら再インデックスしない")
}

func TestFullScanReindexesModifiedFiles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	_, err := f.ix.FullScan(ctx)
	require.NoError(t, err)

	// 内容とmtimeを変える
	writeTestJPEG(t, f.root, "a.jpg", 80, 40)
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, future, future))

	stats, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed)
	require.Equal(t, 0, stats.Unchanged)
}

func TestFullScanRemovesDeletedPhotos(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	writeTestJPEG(t, f.root, "b.jpg", 40, 20)
	_, err := f.ix.FullScan(ctx)
	require.NoError(t, err)
	thumbPath := f.gen.Path(store.IDFor(path))
	require.FileExists(t, thumbPath)

	// アプリ停止中に消されたことを模す
	require.NoError(t, os.Remove(path))

	stats, err := f.ix.FullScan(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, stats.Removed)
	require.NoFileExists(t, thumbPath, "サムネイルもハードデリートする")
	n, err := f.st.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestFullScanStopsOnCancelledContext(t *testing.T) {
	f := newFixture(t)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.ix.FullScan(ctx)

	require.ErrorIs(t, err, context.Canceled)
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/index/ -run FullScan -v`
Expected: コンパイルエラー `f.ix.FullScan undefined`

- [ ] **Step 3: 実装する**

`internal/index/scan.go`:

```go
package index

import (
	"context"
	"io/fs"
	"path/filepath"

	"github.com/yendo/famifo-proto/internal/photo"
)

// Stats はフルスキャンの結果。
type Stats struct {
	Indexed   int // 新規登録または更新した枚数
	Unchanged int // mtimeが変わらず再処理しなかった枚数
	Removed   int // ディスクから消えていたためインデックスから消した枚数
	Skipped   int // 破損・権限エラーで飛ばした枚数
}

// FullScan はルートディレクトリを走査してインデックスをディスクの実態に合わせる。
//
// fsnotifyはアプリが停止していた間の変更を検知できないため、起動のたびにこれを
// 実行して整合性を取り直す。個々のファイルのエラーは記録して走査を続け、
// コンテキストのキャンセルだけが全体を中断させる。
func (ix *Indexer) FullScan(ctx context.Context) (Stats, error) {
	known, err := ix.st.AllPaths(ctx)
	if err != nil {
		return Stats{}, err
	}

	var st Stats
	walkErr := filepath.WalkDir(ix.root, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// 読めないディレクトリやファイルは飛ばす（権限エラーなど）
			ix.log.Warn("走査をスキップ", "path", path, "err", err)
			st.Skipped++
			return nil
		}
		if d.IsDir() || !photo.IsSupported(path) {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			ix.log.Warn("ファイル情報を取得できずスキップ", "path", path, "err", err)
			st.Skipped++
			return nil
		}

		// 見つかったパスは消し込む。走査後に残ったものが削除されたファイル。
		modTime, wasKnown := known[path]
		delete(known, path)
		if wasKnown && modTime == fi.ModTime().Unix() {
			st.Unchanged++
			return nil
		}

		if err := ix.IndexFile(ctx, path); err != nil {
			ix.log.Warn("インデックスをスキップ", "path", path, "err", err)
			st.Skipped++
			return nil
		}
		st.Indexed++
		return nil
	})
	if walkErr != nil {
		return st, walkErr
	}

	for path := range known {
		if err := ix.RemoveFile(ctx, path); err != nil {
			ix.log.Warn("削除の反映に失敗", "path", path, "err", err)
			continue
		}
		st.Removed++
	}
	return st, nil
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/index/ -v`
Expected: 全テストPASS

- [ ] **Step 5: コミット**

```bash
git add internal/index
git commit -m "feat: 起動時フルスキャンを追加"
```

---

### Task 8: fsnotifyによる変更追従

ディレクトリツリーを再帰的に監視し、写真の追加・更新・削除をインデックスに反映する。

**Files:**
- Create: `internal/index/watch.go`
- Test: `internal/index/watch_test.go`

**Interfaces:**
- Consumes: `(*Indexer).IndexFile`, `(*Indexer).RemoveFile`
- Produces:
  - `index.NewWatcher(ix *Indexer, log *slog.Logger, debounce time.Duration) (*Watcher, error)`
  - `(*Watcher) Run(ctx context.Context) error`
  - `(*Watcher) Close() error`

**設計メモ:**
- fsnotifyは再帰監視をしないので、起動時に全ディレクトリを `Add` し、新しいディレクトリが作られたら都度追加する。
- ファイルのコピー中は `WRITE` が何度も飛ぶ。書きかけのファイルをデコードして失敗しないよう、最後のイベントから `debounce` 経過してから処理する。
- ディレクトリごと `mv` されると、監視を登録する前に中身のファイルが揃ってしまい個別のイベントが来ない。そのため新規ディレクトリを検知したら中身も走査して処理キューに積む。

- [ ] **Step 1: 失敗するテストを書く**

`internal/index/watch_test.go`:

```go
package index

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testDebounce = 100 * time.Millisecond

// startWatcher はWatcherをバックグラウンドで動かし、停止まで面倒を見る。
func startWatcher(t *testing.T, f *fixture) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := NewWatcher(f.ix, log, testDebounce)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		require.NoError(t, w.Close())
	})
	time.Sleep(50 * time.Millisecond) // 監視の登録が終わるのを待つ
}

// requireCount はDBの枚数が期待値になるまで待つ。
func requireCount(t *testing.T, f *fixture, want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		n, err := f.st.Count(context.Background())
		return err == nil && n == want
	}, 3*time.Second, 25*time.Millisecond, "枚数が %d にならなかった", want)
}

func TestWatcherIndexesNewFile(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	requireCount(t, f, 1)
}

func TestWatcherIgnoresNonPhotos(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	require.NoError(t, os.WriteFile(filepath.Join(f.root, "notes.txt"), []byte("x"), 0o644))
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	requireCount(t, f, 1)
}

func TestWatcherRemovesDeletedFile(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)
	path := writeTestJPEG(t, f.root, "a.jpg", 40, 20)
	requireCount(t, f, 1)

	require.NoError(t, os.Remove(path))

	requireCount(t, f, 0)
}

func TestWatcherPicksUpNewSubdirectory(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	sub := filepath.Join(f.root, "2020")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	time.Sleep(50 * time.Millisecond) // 監視登録を待つ
	writeTestJPEG(t, sub, "a.jpg", 40, 20)

	requireCount(t, f, 1)
}

func TestWatcherPicksUpDirectoryMovedInWholesale(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)

	// 監視外で中身を作ってからディレクトリごと移動する。
	// 個別のCREATEイベントは飛ばないので、新規ディレクトリの中身を
	// 自前で走査できていないと取りこぼす。
	staging := filepath.Join(t.TempDir(), "album")
	require.NoError(t, os.MkdirAll(staging, 0o755))
	writeTestJPEG(t, staging, "a.jpg", 40, 20)
	writeTestJPEG(t, staging, "b.jpg", 40, 20)
	require.NoError(t, os.Rename(staging, filepath.Join(f.root, "album")))

	requireCount(t, f, 2)
}

func TestWatcherDebouncesRepeatedWrites(t *testing.T) {
	f := newFixture(t)
	startWatcher(t, f)
	path := filepath.Join(f.root, "a.jpg")

	// 少しずつ書き足す＝コピー中を模す。最終的に有効なJPEGになる。
	require.NoError(t, os.WriteFile(path, []byte("partial"), 0o644))
	time.Sleep(20 * time.Millisecond)
	writeTestJPEG(t, f.root, "a.jpg", 40, 20)

	requireCount(t, f, 1)
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/index/ -run Watcher -v`
Expected: コンパイルエラー `undefined: NewWatcher`

- [ ] **Step 3: 実装する**

`internal/index/watch.go`:

```go
package index

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher はfsnotifyでディレクトリツリーを監視し、変更をインデックスに反映する。
type Watcher struct {
	ix       *Indexer
	fsw      *fsnotify.Watcher
	log      *slog.Logger
	debounce time.Duration
}

// NewWatcher はWatcherを作る。debounceは最後の書き込みイベントから実際に
// インデックスするまでの待ち時間。ファイルのコピー中に読み込むのを避ける。
func NewWatcher(ix *Indexer, log *slog.Logger, debounce time.Duration) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("監視を開始できません: %w", err)
	}
	return &Watcher{ix: ix, fsw: fsw, log: log, debounce: debounce}, nil
}

func (w *Watcher) Close() error { return w.fsw.Close() }

// Run はコンテキストがキャンセルされるまで監視を続ける。
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.addTree(w.ix.root); err != nil {
		return err
	}

	// path -> 最後にイベントを受けた時刻
	pending := make(map[string]time.Time)
	tick := time.NewTicker(w.debounce / 2)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handle(ctx, ev, pending)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("監視エラー", "err", err)

		case now := <-tick.C:
			w.flush(ctx, pending, now)
		}
	}
}

// handle は1つのfsnotifyイベントを処理する。
func (w *Watcher) handle(ctx context.Context, ev fsnotify.Event, pending map[string]time.Time) {
	switch {
	case ev.Has(fsnotify.Remove), ev.Has(fsnotify.Rename):
		// Renameは「この名前から消えた」を意味する。移動先は別途Createで届く。
		delete(pending, ev.Name)
		if err := w.ix.RemoveFile(ctx, ev.Name); err != nil {
			w.log.Warn("削除の反映に失敗", "path", ev.Name, "err", err)
		}

	case ev.Has(fsnotify.Create):
		fi, err := os.Stat(ev.Name)
		if err != nil {
			return // すぐ消された等。何もしない
		}
		if !fi.IsDir() {
			pending[ev.Name] = time.Now()
			return
		}
		// 新しいディレクトリ: 監視に加えたうえで、既に入っている中身も拾う。
		// ディレクトリごとmvされた場合、中のファイルには個別のイベントが来ない。
		if err := w.addTree(ev.Name); err != nil {
			w.log.Warn("監視対象の追加に失敗", "path", ev.Name, "err", err)
		}
		w.enqueueTree(ev.Name, pending)

	case ev.Has(fsnotify.Write):
		pending[ev.Name] = time.Now()
	}
}

// flush はdebounce時間が経過した保留中のファイルをインデックスする。
func (w *Watcher) flush(ctx context.Context, pending map[string]time.Time, now time.Time) {
	for path, last := range pending {
		if now.Sub(last) < w.debounce {
			continue
		}
		delete(pending, path)
		if err := w.ix.IndexFile(ctx, path); err != nil {
			w.log.Warn("インデックスをスキップ", "path", path, "err", err)
			continue
		}
		w.log.Info("インデックスを更新", "path", path)
	}
}

// addTree は root 以下の全ディレクトリを監視対象に加える。
// fsnotifyは再帰監視をしないため自前で降りていく。
func (w *Watcher) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			w.log.Warn("監視対象をスキップ", "path", path, "err", err)
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if err := w.fsw.Add(path); err != nil {
			w.log.Warn("監視対象を追加できません", "path", path, "err", err)
		}
		return nil
	})
}

// enqueueTree は root 以下の全ファイルを保留キューに積む。
func (w *Watcher) enqueueTree(root string, pending map[string]time.Time) {
	now := time.Now()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		pending[path] = now
		return nil
	})
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/index/ -v -race`
Expected: 全テストPASS（`-race` でデータ競合が無いことも確認する）

- [ ] **Step 5: コミット**

```bash
git add internal/index
git commit -m "feat: fsnotifyによる変更追従を追加"
```

---

### Task 9: Webサーバー基盤と画像配信

HTTPルーティングと、サムネイル・原本の配信を実装する。一覧HTMLはTask 10で足す。

**Files:**
- Create: `internal/web/server.go`
- Create: `internal/web/handlers.go`
- Create: `internal/web/static/htmx.min.js`（ダウンロード。Task 10で使う）
- Test: `internal/web/handlers_test.go`

**Interfaces:**
- Consumes: `(*store.Store).GetByID`, `(*store.Store).ListPage`, `(*store.Store).Count`, `store.ErrNotFound`, `photo.ContentType`, `thumb.RelPath`
- Produces:
  - `web.NewServer(st *store.Store, thumbDir string, pageSize int) (*Server, error)`
  - `(*Server) Handler() http.Handler`

**設計メモ:** URLにはIDしか出さず、原本の配信は必ずDBを引いてから行う。これによりパストラバーサルが構造的に成立しない（インデックスに載っていないファイルは配信できない）。HEIC/HEIFは `http.ServeFile` がMIMEを判定できないため、事前に `Content-Type` を明示する（`ServeContent` は既に設定済みのヘッダを上書きしない）。

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/handlers_test.go`:

```go
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

type webFixture struct {
	h        http.Handler
	st       *store.Store
	thumbDir string
	photoDir string
}

func newWebFixture(t *testing.T, pageSize int) *webFixture {
	t.Helper()
	base := t.TempDir()
	st, err := store.Open(filepath.Join(base, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbDir := filepath.Join(base, "thumbs")
	require.NoError(t, os.MkdirAll(thumbDir, 0o755))
	photoDir := filepath.Join(base, "photos")
	require.NoError(t, os.MkdirAll(photoDir, 0o755))

	srv, err := NewServer(st, thumbDir, pageSize)
	require.NoError(t, err)
	return &webFixture{h: srv.Handler(), st: st, thumbDir: thumbDir, photoDir: photoDir}
}

// addPhoto は原本ファイルとDB行を用意する。hasThumbならサムネイルも置く。
func (f *webFixture) addPhoto(t *testing.T, name string, takenAt time.Time, hasThumb bool) store.Photo {
	t.Helper()
	path := filepath.Join(f.photoDir, name)
	require.NoError(t, os.WriteFile(path, []byte("original-"+name), 0o644))

	p := store.Photo{
		ID: store.IDFor(path), Path: path, TakenAt: takenAt, ModTime: takenAt,
		Size: 10, Ext: filepath.Ext(name), HasThumb: hasThumb,
	}
	require.NoError(t, f.st.Upsert(context.Background(), p))

	if hasThumb {
		tp := filepath.Join(f.thumbDir, thumb.RelPath(p.ID))
		require.NoError(t, os.MkdirAll(filepath.Dir(tp), 0o755))
		require.NoError(t, os.WriteFile(tp, []byte("thumb-"+name), 0o644))
	}
	return p
}

func do(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestServeThumb(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	rec := do(t, f.h, "/thumb/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "thumb-a.jpg", rec.Body.String())
}

func TestServeThumbNotFoundForUnknownID(t *testing.T) {
	f := newWebFixture(t, 10)

	rec := do(t, f.h, "/thumb/deadbeef")

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeThumbNotFoundWhenPhotoHasNone(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), false)

	rec := do(t, f.h, "/thumb/"+p.ID)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeOriginal(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String())
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

func TestServeOriginalSetsHEICContentType(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), false)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/heic", rec.Header().Get("Content-Type"),
		"Goのmimeパッケージが知らないので自前で設定する")
}

func TestServeOriginalNotFoundForUnknownID(t *testing.T) {
	f := newWebFixture(t, 10)

	rec := do(t, f.h, "/photo/deadbeef")

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnindexedPathsAreNotReachable(t *testing.T) {
	f := newWebFixture(t, 10)
	// パスではなくIDでしか引けないため、traversalは構造的に成立しない
	for _, target := range []string{
		"/photo/../../etc/passwd",
		"/thumb/..%2f..%2fetc%2fpasswd",
		"/photo/" + store.IDFor("/etc/passwd"),
	} {
		t.Run(target, func(t *testing.T) {
			rec := do(t, f.h, target)
			require.NotEqual(t, http.StatusOK, rec.Code)
		})
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: コンパイルエラー `undefined: NewServer`

- [ ] **Step 3: 実装する**

`internal/web/server.go`:

```go
// Package web はギャラリーのHTTP配信を担う。
// テンプレートと静的ファイルはバイナリに埋め込む（単一バイナリ配布のため）。
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/yendo/famifo-proto/internal/store"
)

//go:embed templates static
var assets embed.FS

// Server はギャラリーのHTTPハンドラ群を保持する。
type Server struct {
	st       *store.Store
	tmpl     *template.Template
	thumbDir string
	pageSize int
}

// NewServer はテンプレートを読み込んでServerを作る。
// pageSize は一覧1ページあたりの枚数。
func NewServer(st *store.Store, thumbDir string, pageSize int) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("テンプレートを読み込めません: %w", err)
	}
	return &Server{st: st, tmpl: tmpl, thumbDir: thumbDir, pageSize: pageSize}, nil
}

// Handler はルーティング済みのハンドラを返す。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // embedの内容は固定なので、ここで失敗するならビルドの不備
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	mux.HandleFunc("GET /{$}", s.handleGallery)
	mux.HandleFunc("GET /items", s.handleItems)
	mux.HandleFunc("GET /thumb/{id}", s.handleThumb)
	mux.HandleFunc("GET /photo/{id}", s.handlePhoto)
	return mux
}
```

`internal/web/handlers.go`（この時点では `handleGallery` と `handleItems` は空の実装。Task 10で埋める）:

```go
package web

import (
	"errors"
	"net/http"
	"path/filepath"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// handleGallery はギャラリーのトップページを返す。Task 10で実装する。
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// handleItems は無限スクロール用のHTML断片を返す。Task 10で実装する。
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// handleThumb はサムネイルを配信する。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if !p.HasThumb {
		// HEICなどサムネイルを作らないフォーマット。原本を使うべき。
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.thumbDir, thumb.RelPath(p.ID)))
}

// handlePhoto は原本を配信する。
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	// ServeFileは拡張子からMIMEを引くがHEIC/HEIFを知らない。
	// 先に設定しておけばServeContentは上書きしない。
	w.Header().Set("Content-Type", photo.ContentType(p.Path))
	http.ServeFile(w, r, p.Path)
}

// lookup はURLのIDから写真を引く。
// パスではなくIDを経由することで、インデックスに無いファイルは配信できない。
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (store.Photo, bool) {
	p, err := s.st.GetByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return store.Photo{}, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return store.Photo{}, false
	}
	return p, true
}
```

- [ ] **Step 4: embedが要求するディレクトリを用意する**

```bash
mkdir -p internal/web/templates internal/web/static
# htmxはここで取得する。go:embed はドットファイル（.gitkeep等）を除外するため、
# static/ に実体のあるファイルが1つも無いと
# "cannot embed directory static: contains no embeddable files" でビルドが落ちる。
curl -fsSL https://unpkg.com/htmx.org@2.0.7/dist/htmx.min.js -o internal/web/static/htmx.min.js
printf '{{define "placeholder"}}{{end}}\n' > internal/web/templates/placeholder.html
ls -l internal/web/static/htmx.min.js
```

Expected: htmx.min.js が約50KBで取得できる。
`templates/placeholder.html` は `template.ParseFS` が最低1ファイルを要求するためのつなぎで、Task 10で実物に置き換える。

- [ ] **Step 5: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全テストPASS

- [ ] **Step 6: コミット**

```bash
git add internal/web
git commit -m "feat: Webサーバー基盤と画像配信を追加"
```

---

### Task 10: ギャラリー一覧とhtmx無限スクロール

サーバーサイドでギャラリーHTMLをレンダリングし、スクロール末尾でHTML断片を追加読み込みする。

**Files:**
- Modify: `internal/web/handlers.go`（`handleGallery` と `handleItems` を実装）
- Create: `internal/web/templates/gallery.html`
- Create: `internal/web/templates/items.html`
- Delete: `internal/web/templates/placeholder.html`
- Test: `internal/web/gallery_test.go`

**Interfaces:**
- Consumes: `(*store.Store).ListPage`, `(*store.Store).Count`, `store.Cursor`
- Produces: `GET /` と `GET /items?t=<unix>&id=<id>` のHTMLレスポンス

**設計メモ:** 一覧のマークアップは `items.html` の1箇所だけに置き、初回ページも追加読み込みも同じテンプレートを使う。これがHTMLフラグメントパターンの要点で、JSON APIにしてJS側で組み立てると同じマークアップを二重に持つことになる。次ページの有無は `pageSize+1` 件取得して判定する。

- [ ] **Step 1: htmxが配置済みであることを確認する**

htmx本体はTask 9 Step 4で取得済み。ここでは存在だけ確認する。

```bash
ls -l internal/web/static/htmx.min.js
```

Expected: 約50KBのファイルがある（無ければTask 9 Step 4のcurlを実行する）

- [ ] **Step 2: 失敗するテストを書く**

`internal/web/gallery_test.go`:

```go
package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGalleryRendersTiles(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	rec := do(t, f.h, "/")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	require.Contains(t, body, `src="/thumb/`+p.ID+`"`)
	require.Contains(t, body, `data-full="/photo/`+p.ID+`"`)
	require.Contains(t, body, "/static/htmx.min.js")
}

func TestGalleryUsesOriginalAsThumbForHEIC(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), false)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `src="/photo/`+p.ID+`"`,
		"サムネイルが無いフォーマットは原本を直接使う")
	require.NotContains(t, body, "/thumb/"+p.ID)
}

func TestGalleryOrdersNewestFirst(t *testing.T) {
	f := newWebFixture(t, 10)
	old := f.addPhoto(t, "old.jpg", time.Unix(1600000000, 0), true)
	recent := f.addPhoto(t, "new.jpg", time.Unix(1700000000, 0), true)

	body := do(t, f.h, "/").Body.String()

	require.Less(t, strings.Index(body, recent.ID), strings.Index(body, old.ID),
		"撮影日時の新しい順に並べる")
}

func TestGalleryEmitsSentinelWhenMorePagesExist(t *testing.T) {
	f := newWebFixture(t, 1)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)
	last := f.addPhoto(t, "b.jpg", time.Unix(1700000000, 0), true)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `hx-trigger="revealed"`)
	// html/template は属性内のリテラルな & をエスケープしない（検証済み）
	require.Contains(t, body, "/items?t=1700000000&id="+last.ID,
		"次ページのカーソルは最後に描画した写真を指す")
}

func TestGalleryOmitsSentinelOnLastPage(t *testing.T) {
	f := newWebFixture(t, 10)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	body := do(t, f.h, "/").Body.String()

	require.NotContains(t, body, "hx-trigger")
}

func TestItemsReturnsFragmentOnly(t *testing.T) {
	f := newWebFixture(t, 1)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)
	last := f.addPhoto(t, "b.jpg", time.Unix(1700000000, 0), true)

	rec := do(t, f.h, "/items?t=1700000000&id="+last.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<html", "断片なので完全なページを返さない")
	require.NotContains(t, body, "<body")
	require.Contains(t, body, "/photo/")
}

func TestItemsRejectsBadCursor(t *testing.T) {
	f := newWebFixture(t, 10)

	rec := do(t, f.h, "/items?t=notanumber&id=abc")

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestItemsWithoutCursorReturnsFirstPage(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	body := do(t, f.h, "/items").Body.String()

	require.Contains(t, body, p.ID)
}
```

- [ ] **Step 3: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run Gallery -v`
Expected: FAIL（501 Not Implemented が返る）

- [ ] **Step 4: テンプレートを作る**

`internal/web/templates/items.html`:

```html
{{define "items"}}
{{range .Photos}}
<a class="tile" href="{{.FullURL}}" data-full="{{.FullURL}}">
  <img src="{{.ThumbURL}}" alt="" loading="lazy" decoding="async">
</a>
{{end}}
{{if .Next}}
<div class="sentinel"
     hx-get="/items?t={{.Next.TakenAt}}&id={{.Next.ID}}"
     hx-trigger="revealed"
     hx-swap="outerHTML"></div>
{{end}}
{{end}}
```

`internal/web/templates/gallery.html`:

```html
{{define "gallery"}}<!doctype html>
<html lang="ja">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>famifo</title>
<link rel="stylesheet" href="/static/app.css">
<script src="/static/htmx.min.js" defer></script>
<script src="/static/app.js" defer></script>
</head>
<body>
<header class="topbar">
  <h1>famifo</h1>
  <span class="count">{{.Total}} 枚</span>
</header>

<main id="gallery" class="gallery">
{{template "items" .}}
</main>

<div id="lightbox" class="lightbox" hidden>
  <img alt="">
  <button class="lb-nav lb-prev" type="button" aria-label="前の写真">‹</button>
  <button class="lb-nav lb-next" type="button" aria-label="次の写真">›</button>
</div>
</body>
</html>
{{end}}
```

- [ ] **Step 5: ハンドラを実装する**

`internal/web/handlers.go` の `handleGallery` と `handleItems` を置き換え、ファイル冒頭に必要な import を足す:

```go
package web

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// photoView は1枚分のテンプレート入力。
type photoView struct {
	ID       string
	ThumbURL string
	FullURL  string
}

// cursorView は次ページを指すカーソル。
type cursorView struct {
	TakenAt int64
	ID      string
}

// itemsView は items.html の入力。
type itemsView struct {
	Photos []photoView
	Next   *cursorView
}

// galleryView は gallery.html の入力。itemsViewを埋め込むので
// {{template "items" .}} にそのまま渡せる。
type galleryView struct {
	itemsView
	Total int
}

// buildPage は1ページ分を組み立てる。次ページの有無を知るために
// pageSize+1 件取得し、余った1件は描画せず「続きがある」印としてだけ使う。
func (s *Server) buildPage(r *http.Request, cur store.Cursor) (itemsView, error) {
	photos, err := s.st.ListPage(r.Context(), cur, s.pageSize+1)
	if err != nil {
		return itemsView{}, err
	}

	var v itemsView
	if len(photos) > s.pageSize {
		photos = photos[:s.pageSize]
		last := photos[len(photos)-1]
		v.Next = &cursorView{TakenAt: last.TakenAt.Unix(), ID: last.ID}
	}

	v.Photos = make([]photoView, 0, len(photos))
	for _, p := range photos {
		pv := photoView{ID: p.ID, FullURL: "/photo/" + p.ID, ThumbURL: "/photo/" + p.ID}
		if p.HasThumb {
			pv.ThumbURL = "/thumb/" + p.ID
		}
		v.Photos = append(v.Photos, pv)
	}
	return v, nil
}

// parseCursor はクエリからカーソルを読む。パラメータが無ければ先頭ページ。
func parseCursor(r *http.Request) (store.Cursor, error) {
	raw := r.URL.Query().Get("t")
	id := r.URL.Query().Get("id")
	if raw == "" && id == "" {
		return store.Cursor{}, nil
	}
	at, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return store.Cursor{}, err
	}
	return store.Cursor{TakenAt: time.Unix(at, 0), ID: id, Set: true}, nil
}

// handleGallery はギャラリーのトップページを返す。
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	items, err := s.buildPage(r, store.Cursor{})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	total, err := s.st.Count(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "gallery", galleryView{itemsView: items, Total: total}); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残すのみ。
		return
	}
}

// handleItems は無限スクロール用のHTML断片を返す。
// 初回ページと同じテンプレートを使うことで、マークアップの二重管理を避ける。
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	cur, err := parseCursor(r)
	if err != nil {
		http.Error(w, "bad cursor", http.StatusBadRequest)
		return
	}
	items, err := s.buildPage(r, cur)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "items", items); err != nil {
		return
	}
}
```

- [ ] **Step 6: つなぎのテンプレートを消す**

```bash
rm internal/web/templates/placeholder.html
```

- [ ] **Step 7: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全テストPASS

- [ ] **Step 8: コミット**

```bash
git add internal/web
git commit -m "feat: ギャラリー一覧とhtmx無限スクロールを追加"
```

---

### Task 11: ライトボックス・スワイプ・レスポンシブCSS

スマホ/タブレットでの閲覧体験を仕上げる。タップで拡大、スワイプで送り、下スワイプで閉じる。

**Files:**
- Create: `internal/web/static/app.css`
- Create: `internal/web/static/app.js`
- Test: `internal/web/static_test.go`

**Interfaces:**
- Consumes: `gallery.html` が出力する `#gallery`, `.tile[data-full]`, `#lightbox`, `.lb-prev`, `.lb-next`
- Produces: なし（ブラウザ側のみ）

**設計メモ:** タイルのクリックは `document` への委譲で捕まえる。htmxが後から差し込んだタイルにもイベントを付け直さずに済むため。ライトボックスの `touch-action: pinch-zoom` により、ピンチズームはブラウザに任せつつ左右スワイプは自前で拾える。

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/static_test.go`:

```go
package web

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaticAssetsAreServed(t *testing.T) {
	f := newWebFixture(t, 10)
	tests := map[string]string{
		"/static/app.css":      "text/css",
		"/static/app.js":       "text/javascript",
		"/static/htmx.min.js":  "text/javascript",
	}
	for target, wantType := range tests {
		t.Run(target, func(t *testing.T) {
			rec := do(t, f.h, target)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), wantType)
			require.NotEmpty(t, rec.Body.String())
		})
	}
}

func TestAppJSWiresLightbox(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	// gallery.html が出力するフックと食い違っていないこと
	require.Contains(t, body, "#lightbox")
	require.Contains(t, body, "data-full")
	require.Contains(t, body, "touchstart", "スワイプ操作を実装している")
}

func TestAppCSSIsResponsive(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.css").Body.String()

	require.Contains(t, body, "@media", "画面幅に応じて列数を変える")
	require.Contains(t, body, "grid-template-columns")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run Static -v`
Expected: FAIL（404 が返る）

- [ ] **Step 3: CSSを書く**

`internal/web/static/app.css`:

```css
:root {
  --bg: #111;
  --fg: #eee;
  --tile-bg: #222;
  --gap: 4px;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--bg);
  color: var(--fg);
  font-family: system-ui, -apple-system, "Hiragino Kaku Gothic ProN", sans-serif;
  -webkit-text-size-adjust: 100%;
}

/* ライトボックス表示中に背後の一覧がスクロールしないようにする */
body.locked { overflow: hidden; }

.topbar {
  position: sticky;
  top: 0;
  z-index: 5;
  display: flex;
  align-items: baseline;
  gap: 12px;
  padding: 12px 16px;
  padding-top: max(12px, env(safe-area-inset-top));
  background: rgba(17, 17, 17, 0.9);
  backdrop-filter: blur(8px);
}
.topbar h1 { margin: 0; font-size: 1rem; letter-spacing: 0.08em; }
.count { font-size: 0.8rem; opacity: 0.6; }

.gallery {
  display: grid;
  gap: var(--gap);
  padding: var(--gap);
  grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
}
@media (min-width: 700px) {
  .gallery { grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); }
}
@media (min-width: 1100px) {
  .gallery { grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); }
}

.tile {
  display: block;
  aspect-ratio: 1;
  overflow: hidden;
  border-radius: 2px;
  background: var(--tile-bg);
}
.tile img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
}

/* htmxが次ページを読みに行くトリガー。見た目は持たせない */
.sentinel { grid-column: 1 / -1; height: 1px; }

.lightbox {
  position: fixed;
  inset: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #000;
  /* ピンチズームはブラウザに任せ、左右スワイプは自前で拾う */
  touch-action: pinch-zoom;
}
.lightbox[hidden] { display: none; }
.lightbox img {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
}

.lb-nav {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 22%;
  border: 0;
  background: transparent;
  color: transparent;
  font-size: 2rem;
  cursor: pointer;
}
.lb-prev { left: 0; }
.lb-next { right: 0; }
/* タッチ端末では矢印を出さない（スワイプで操作するため） */
@media (hover: hover) {
  .lb-nav:hover { color: rgba(255, 255, 255, 0.7); }
}
```

- [ ] **Step 4: JSを書く**

`internal/web/static/app.js`:

```js
// ギャラリーのライトボックス。htmxが後から差し込むタイルにも効くよう、
// クリックはdocumentへの委譲で捕まえる。
(() => {
  const box = document.getElementById('lightbox');
  if (!box) return;

  const img = box.querySelector('img');
  const SWIPE_X = 50;  // 左右送りとみなす最小移動量(px)
  const SWIPE_Y = 80;  // 下スワイプで閉じる最小移動量(px)

  let urls = [];
  let idx = -1;

  const tiles = () => Array.from(document.querySelectorAll('#gallery .tile'));

  function open(i) {
    urls = tiles().map((a) => a.dataset.full);
    if (i < 0 || i >= urls.length) return;
    idx = i;
    img.src = urls[idx];
    box.hidden = false;
    document.body.classList.add('locked');
  }

  function close() {
    box.hidden = true;
    img.removeAttribute('src');
    document.body.classList.remove('locked');
  }

  function step(delta) {
    const next = idx + delta;
    if (next < 0 || next >= urls.length) return;
    idx = next;
    img.src = urls[idx];
  }

  document.addEventListener('click', (e) => {
    const tile = e.target.closest('#gallery .tile');
    if (!tile) return;
    e.preventDefault();
    open(tiles().indexOf(tile));
  });

  box.addEventListener('click', (e) => {
    if (e.target.closest('.lb-prev')) { step(-1); return; }
    if (e.target.closest('.lb-next')) { step(1); return; }
    close();
  });

  document.addEventListener('keydown', (e) => {
    if (box.hidden) return;
    if (e.key === 'Escape') close();
    else if (e.key === 'ArrowRight') step(1);
    else if (e.key === 'ArrowLeft') step(-1);
  });

  let startX = 0;
  let startY = 0;
  let tracking = false;

  box.addEventListener('touchstart', (e) => {
    // 2本指はピンチズーム。ブラウザに任せる
    tracking = e.touches.length === 1;
    if (!tracking) return;
    startX = e.touches[0].clientX;
    startY = e.touches[0].clientY;
  }, { passive: true });

  box.addEventListener('touchend', (e) => {
    if (!tracking) return;
    tracking = false;
    const t = e.changedTouches[0];
    const dx = t.clientX - startX;
    const dy = t.clientY - startY;

    if (Math.abs(dx) > SWIPE_X && Math.abs(dx) > Math.abs(dy)) {
      step(dx < 0 ? 1 : -1);
    } else if (dy > SWIPE_Y && Math.abs(dy) > Math.abs(dx)) {
      close();
    }
  }, { passive: true });
})();
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全テストPASS

- [ ] **Step 6: コミット**

```bash
git add internal/web
git commit -m "feat: ライトボックスとスワイプ操作を追加"
```

---

### Task 12: 全体の結線とグレースフルシャットダウン

`main.go` で全コンポーネントを組み立て、実際に動くアプリにする。

**Files:**
- Modify: `main.go`（Task 1の仮実装を置き換える）
- Create: `README.md`（上書き）

**Interfaces:**
- Consumes: `config.Parse`, `store.Open`, `thumb.NewGenerator`, `index.New`, `(*Indexer).FullScan`, `index.NewWatcher`, `(*Watcher).Run`, `(*Watcher).Close`, `web.NewServer`, `(*Server).Handler`
- Produces: 動作する `famifo-proto` バイナリ

**設計メモ:** HTTPサーバーはフルスキャンを待たずに起動する。数万枚のスキャンに数分かかっても、その間ギャラリーは（まだ少ないが）見られるほうがよい。監視はフルスキャン完了後に始める（設計方針どおり）。スキャン中に起きた変更は取りこぼすが、次回起動時のフルスキャンで回収される。

- [ ] **Step 1: main.goを実装する**

```go
// famifo-proto はローカルディスク上の写真をインデックス化し、
// LAN内のブラウザからギャラリーとして閲覧できるようにする。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yendo/famifo-proto/internal/config"
	"github.com/yendo/famifo-proto/internal/index"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
	"github.com/yendo/famifo-proto/internal/web"
)

// pageSize は一覧1ページあたりの枚数。
const pageSize = 60

// watchDebounce はファイル書き込みが落ち着いたと判断するまでの待ち時間。
// コピー途中のファイルをデコードしに行かないための猶予。
const watchDebounce = 2 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("データディレクトリを作れません: %w", err)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	gen, err := thumb.NewGenerator(cfg.ThumbDir(), cfg.ThumbSize)
	if err != nil {
		return err
	}

	srv, err := web.NewServer(st, cfg.ThumbDir(), pageSize)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// スキャンの完了を待たずに配信を始める。大量の写真でも、
	// インデックスができた分から順に見られるほうがよい。
	httpSrv := &http.Server{Addr: cfg.Addr, Handler: srv.Handler()}
	go func() {
		log.Info("HTTPサーバーを開始", "addr", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTPサーバーが停止しました", "err", err)
			stop()
		}
	}()

	ix := index.New(cfg.PhotoDir, st, gen, log)

	// fsnotifyは停止中の変更を検知できないので、起動のたびに実態と突き合わせる。
	log.Info("フルスキャンを開始", "dir", cfg.PhotoDir)
	stats, err := ix.FullScan(ctx)
	if err != nil && ctx.Err() == nil {
		return err
	}
	log.Info("フルスキャンが完了",
		"indexed", stats.Indexed, "unchanged", stats.Unchanged,
		"removed", stats.Removed, "skipped", stats.Skipped)

	if ctx.Err() == nil {
		watcher, err := index.NewWatcher(ix, log, watchDebounce)
		if err != nil {
			return err
		}
		defer watcher.Close()
		go func() {
			if err := watcher.Run(ctx); err != nil {
				log.Error("監視が停止しました", "err", err)
			}
		}()
		log.Info("変更の監視を開始", "dir", cfg.PhotoDir)
	}

	<-ctx.Done()
	log.Info("シャットダウンします")

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutCtx)
}
```

- [ ] **Step 2: ビルドと全テストを通す**

Run: `CGO_ENABLED=0 go build -o famifo-proto . && CGO_ENABLED=0 go test ./... && go vet ./...`
Expected: ビルド成功、全テストPASS、vet警告なし

- [ ] **Step 3: 手で動かして確認する**

```bash
# 手元の写真を数枚コピーして動作確認用のディレクトリを作る。
# サブディレクトリも走査されること、フラットな一覧になることを確認するため階層を作る。
mkdir -p /tmp/famifo-photos/2020
cp ~/Pictures/*.jpg /tmp/famifo-photos/ 2>/dev/null || \
  echo "手元の写真を /tmp/famifo-photos/ にコピーしてから続行する"
cp ~/Pictures/*.jpg /tmp/famifo-photos/2020/ 2>/dev/null || true

./famifo-proto -dir /tmp/famifo-photos -data /tmp/famifo-data -addr :8080
```

別のターミナルで確認する:

```bash
curl -s localhost:8080 | head -30          # ギャラリーHTMLが返る
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/static/app.css   # 200
```

さらに以下を目視で確認する:
1. ブラウザで `http://localhost:8080` を開き、写真が新しい順に並ぶこと
2. 起動したまま `/tmp/famifo-photos/` に写真を1枚コピーし、数秒後にリロードすると増えていること
3. その写真を削除してリロードすると消えていること
4. スマホの実機（同一LAN）から `http://<PCのIP>:8080` を開き、タップで拡大・左右スワイプで送れること
5. `Ctrl-C` で速やかに終了すること

- [ ] **Step 4: READMEを書く**

`README.md`:

```markdown
# famifo-proto

ローカルディスク上の写真を収集し、LAN内のブラウザから一覧できるツール。

## 特徴

- 写真をインデックス化して、フォルダ階層を無視した1つのギャラリーとして表示する
- 並び順はEXIFの撮影日時（無ければファイルの更新日時）
- 起動時にフルスキャン、その後は fsnotify で変更を自動追従する
- 単一バイナリ。cgo不要、外部のDBサーバーも不要

## 対応フォーマット

| 拡張子 | サムネイル | 備考 |
|---|---|---|
| `.jpg` `.jpeg` `.png` `.gif` `.webp` | 生成する | |
| `.heic` `.heif` | 生成しない | 原本をそのまま配信する。Safari以外のブラウザでは表示できない |

動画は対象外。

## ビルド

```bash
CGO_ENABLED=0 go build -o famifo-proto .
```

## 使い方

```bash
./famifo-proto -dir /path/to/photos
```

| フラグ | 既定値 | 説明 |
|---|---|---|
| `-dir` | (必須) | 写真を収集するディレクトリ |
| `-data` | `./famifo-data` | DBとサムネイルキャッシュの保存先 |
| `-addr` | `:8080` | HTTPの待ち受けアドレス |
| `-thumb` | `480` | サムネイルの長辺ピクセル数 |

## 制約

- **ローカルディスク専用。** fsnotify はネットワークファイルシステム（NFS/SMB）の
  変更通知を受け取れないため、対象ディレクトリはローカルにマウントされている必要がある。
- **LAN内での利用を前提とする。** 認証もHTTPSも実装していない。外出先から使う場合は
  ポート開放ではなくVPN（Tailscale等）で自宅LANに接続すること。

## 設計

設計判断とその理由は [docs/design.md](docs/design.md) を参照。
```

- [ ] **Step 5: コミット**

```bash
git add -A
git commit -m "feat: 全体を結線してアプリとして動作させる"
```

---

## 実装後の確認

全タスク完了後、以下がすべて通ること:

```bash
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go test ./... -race
go vet ./...
gofmt -l .          # 出力が空であること
```

## 設計方針との対応

| `docs/design.md` の決定 | 実装箇所 |
|---|---|
| ローカルディスクをファイルシステムとして読む | Task 7 `FullScan`（`filepath.WalkDir`） |
| fsnotifyによるリアルタイム監視 | Task 8 `Watcher` |
| 起動時フルスキャン → 監視へ切替 | Task 12 `run()` |
| 削除はハードデリート | Task 6 `RemoveFile`、Task 7 削除の消し込み |
| 破損ファイルはスキップしてログ記録 | Task 7 `Stats.Skipped`、Task 8 `flush` |
| SQLiteに事前インデックス | Task 3 `store` |
| サムネイルはファイルシステム、DBはパスのみ | Task 5 `thumb`、Task 3 `has_thumb` 列 |
| JPEG/PNG/GIF/WebP/HEIC対応 | Task 2 `extKinds` |
| HEICは変換せず原本配信 | Task 2 `KindOpaque`、Task 9 `handlePhoto` |
| HEICはサムネイル生成しない | Task 6 `IndexFile`（`KindRaster` のみ生成） |
| 動画は対象外 | Task 2 `extKinds` に含めない |
| フラットな1ギャラリー | Task 10 `handleGallery`（階層を持たない一覧） |
| EXIF撮影日時、無ければmtime | Task 4 `takenat.Resolve` |
| 複数ブラウザ対応・スマホ/タブレット中心 | Task 11 `app.css`（レスポンシブ）、`app.js`（タッチ） |
| PWA化しない | manifest・Service Worker を作らない |
| Go単一バイナリ | Global Constraints（`CGO_ENABLED=0`） |
| サーバーサイドHTMLレンダリング + vanilla JS | Task 10 `html/template`、Task 11 `app.js` |
| HTMLフラグメントパターン + htmx | Task 10 `items.html` を初回・追加で共用 |
| LAN内のみ・認証なし・HTTPのみ | 認証もTLSも実装しない。`-addr` で bind 先を絞れる |
| 単一バイナリを直接実行 | Task 12、systemd unit は作らない |
| コマンドライン引数で設定 | Task 1 `config.Parse` |
