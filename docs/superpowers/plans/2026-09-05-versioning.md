# バージョン番号の一本化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 版の埋め込み経路を Go の自動VCSスタンプ1本にし、タグを打つとバイナリとイメージが揃って出るようにする。

**Architecture:** `-ldflags -X` と `--build-arg VERSION` を全廃し、`debug.ReadBuildInfo().Main.Version` だけを読む。Dockerビルドでも版が埋まるよう `.dockerignore` から `.git` を外す（同時に、追跡ファイルの除外もやめる。除外すると git がツリーを汚れていると見なし `+dirty` が付くため）。リリースは goreleaser がバイナリと GitHub Release を、ワークフローが `docker build` でイメージを担当する。

**Tech Stack:** Go 1.27 / Docker (マルチステージ, `FROM scratch`) / goreleaser v2 / GitHub Actions

**Spec:** `docs/superpowers/specs/2026-09-05-versioning-design.md`

## Global Constraints

- 対象プラットフォームは `linux/amd64` のみ
- 版は `-ldflags -X` で渡さない。`-ldflags` は `-s -w` だけ
- `.dockerignore` で追跡ファイルを除外しない（`git ls-files` に出るものは1つも書かない）
- goreleaser に Docker イメージを作らせない（`.goreleaser.yaml` に `dockers:` を置かない）
- `docker build .` は引数なし・単体で成功すること
- コミットメッセージは英語、要約1行のみ、本文なし
- リポジトリに入る文言は、そのファイルの既存の言語に合わせる（Goのコメントと `docs/` は日本語、`.github/` と `README.md` は英語）
- **Task 4 のワークフローファイルは、着手前に利用者の明示の承認を取ること**

---

### Task 1: 版を `Main.Version` から読む

**Files:**
- Modify: `main.go:28-77`（`var version` と `formatVersion` と `versionString`）
- Modify: `main_test.go:1-10`（import）, `main_test.go:37-72`（版のテスト4本）
- Modify: `README.md:71-73`（起動ログの例）

**Interfaces:**
- Consumes: なし
- Produces: `formatVersion(v string) string` — `debug.BuildInfo.Main.Version` を表示用に整える。`versionString() string` は据え置き（`run()` から呼ばれる）

- [ ] **Step 1: 失敗するテストを書く**

`main_test.go` の版のテスト4本（`TestFormatVersionPrefersTheOverride` /
`TestFormatVersionUsesTheEmbeddedRevision` / `TestFormatVersionMarksADirtyTree` /
`TestFormatVersionFallsBackWhenNothingIsEmbedded`）を、次の2本に置き換える。

```go
// go build は .git からタグとコミットを読んで版を埋める。埋まった値を
// そのまま見せる。組み立て直すと桁数や書式が経路ごとにずれる。
func TestFormatVersionPassesThroughTheStampedVersion(t *testing.T) {
	require.Equal(t, "v0.1.0", formatVersion("v0.1.0"))

	// タグから進んだコミットは擬似バージョンになる。未コミットの変更が
	// 混ざっていれば +dirty が付き、手元のどのコミットとも一致しない
	// バイナリを見分けられる。
	require.Equal(t, "v0.1.1-0.20260905095503-153d347e4f14+dirty",
		formatVersion("v0.1.1-0.20260905095503-153d347e4f14+dirty"))
}

// .git の無い場所でビルドすると版は "(devel)" になる。そのまま出しても
// 読み手には何も伝わらないので unknown に落とす。
func TestFormatVersionFallsBackWhenNothingIsStamped(t *testing.T) {
	require.Equal(t, "unknown", formatVersion("(devel)"))
	require.Equal(t, "unknown", formatVersion(""))
}
```

`main_test.go` の import から `"runtime/debug"` を消す（`debug.BuildSetting` を
使うテストが無くなるため）。

- [ ] **Step 2: 失敗することを確かめる**

Run: `go test -run TestFormatVersion ./... 2>&1 | head -20`
Expected: コンパイルエラー。`too many arguments in call to formatVersion`
（旧 `formatVersion` は引数2つのため）

- [ ] **Step 3: 実装する**

`main.go` の `var version string` を削除し、`formatVersion` と `versionString` を
次の内容に置き換える（`28行目`のコメントごと差し替える）。

```go
// formatVersion は go build が埋めた版を表示用に整える。
//
// go build は .git からタグとコミットを読み、モジュール自身のバージョンを
// 埋める。タグ上でビルドすれば "v0.1.0"、途中のコミットなら擬似バージョン、
// 未コミットの変更があれば "+dirty" が付く。.git の無い場所でビルドすると
// "(devel)" になり、版として読めない。
func formatVersion(v string) string {
	if v == "" || v == "(devel)" {
		return "unknown"
	}
	return v
}

// versionString は実行中のバイナリのバージョンを返す。
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return formatVersion(bi.Main.Version)
}
```

`main.go` の `runtime/debug` の import は `debug.ReadBuildInfo` で使い続けるので残す。

- [ ] **Step 4: テストが通ることを確かめる**

Run: `make unit-test`
Expected: PASS（`ok github.com/yendo/famifo-proto`）

- [ ] **Step 5: 実際の出力を確かめる**

Run: `make build && ./famifo-proto -version`
Expected: `famifo-proto v0.0.0-<日付>-<12桁SHA>` の形。まだタグが無いので
`v0.0.0-` で始まる。手元に未追跡ファイルがあれば末尾に `+dirty` が付く（正常）

- [ ] **Step 6: `README.md` の起動ログの例を直す**

72行目を置き換える。

```
msg=起動 version="v0.1.0" timezone=JST+09:00 dirs=[/photos] ...
```

62行目の `-version` の行の下（`### Timezone` の直前）に、版の読み方を1行足す。

```markdown
The version comes from the build itself: a tagged build reports the tag, any
other build reports a pseudo-version carrying the commit, and uncommitted
changes add `+dirty`.
```

- [ ] **Step 7: 静的解析を通す**

Run: `make vet && staticcheck ./...`
Expected: 出力なし

- [ ] **Step 8: コミット**

```bash
git add main.go main_test.go README.md
git commit -m "refactor: take the version straight from the build info"
```

---

### Task 2: Docker ビルドの中で版を埋める

**Files:**
- Modify: `.dockerignore`（全体）
- Modify: `Dockerfile:9-16`（`ARG VERSION` とビルドコマンド）, `Dockerfile:18-19`（実行段の `ARG VERSION`）, `Dockerfile` の `LABEL org.opencontainers.image.revision` 行
- Modify: `README.md:82-87`（Docker のビルド手順）

**Interfaces:**
- Consumes: Task 1 の `versionString()`（`-version` の出力）
- Produces: 引数なしで正しい版を埋める `docker build .`

- [ ] **Step 1: `.dockerignore` を書き換える**

全体を次の内容にする。`docs` `*.md` `.dockerignore` `Dockerfile` は追跡ファイル
なので除外をやめる。

```
# .git を送るので、追跡ファイルは1つも除外しない。除くと git が「削除された」と
# 見なし、版に +dirty が付く。版は go build が .git から埋める。
# famifo-data は写真1万枚規模で数百MBになるため除く。
.claude
.superpowers
.idea
.vscode
dist
famifo-data
famifo-proto
```

- [ ] **Step 2: 除外したものが追跡ファイルでないことを確かめる**

Run:

```bash
for p in .claude .superpowers .idea .vscode dist famifo-data famifo-proto; do
  printf '%s: ' "$p"; git ls-files "$p" | wc -l
done
```

Expected: すべて `0`。1つでも `0` 以外なら、そのファイルを除外してはいけない
（版に `+dirty` が付く）

- [ ] **Step 3: `Dockerfile` から版の受け渡しを外す**

ビルド段の次の6行を、

```dockerfile
# .git はビルド文脈から除いてあるので、Goが自動で埋め込むVCS情報は入らない。
# 版はここで渡す。省略すると -version が "dev" になる。
#   docker build --build-arg VERSION=$(git rev-parse --short HEAD) ...
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /famifo .
```

こう置き換える。

```dockerfile
# 版は go build が .git から読んで埋める。そのために .dockerignore で .git を
# 除いていない（追跡ファイルを除くと +dirty が付くので、そちらも除いていない）。
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /famifo .
```

実行段の `ARG VERSION=dev`（`FROM scratch` の直後の行）を削除する。

`LABEL org.opencontainers.image.revision="${VERSION}"` の行を削除する。
`source` と `description` のラベルは残す。CI では metadata-action が
`revision` をフルSHAで付ける。

- [ ] **Step 4: イメージをビルドする**

Run: `docker build -t famifo-ci .`
Expected: 成功。`golang:1.27` の取得から始まるので数分かかる

- [ ] **Step 5: 版が埋まっていることを確かめる**

Run: `docker run --rm famifo-ci -version`
Expected: `famifo-proto v0.0.0-<日付>-<12桁SHA>`。`dev` でも `unknown` でも
`(devel)` でもないこと。`+dirty` が付いた場合は `.dockerignore` に書き漏らした
未追跡ファイルがあるので、`git status --porcelain` で探して足す

- [ ] **Step 6: イメージの中身が増えていないことを確かめる**

Run: `docker image inspect famifo-ci --format '{{.Size}}'`
Expected: 16000000 前後（15MB程度）。最終段は `FROM scratch` にバイナリを
`COPY` するだけなので、`.git` を送ってもイメージには入らない

- [ ] **Step 7: `README.md` の Docker のビルド手順を直す**

`## Docker` の節にある `docker build --build-arg VERSION=...` のコードブロックと、
その下の「`.git` is kept out of the build context ...」の段落を置き換える
（Task 1 で行がずれているので行番号ではなく本文で探す）。

````markdown
```bash
docker build -t famifo .
```

`.git` is part of the build context, so `go build` stamps the version by itself —
nothing has to be passed in.
````

- [ ] **Step 8: コミット**

```bash
git add .dockerignore Dockerfile README.md
git commit -m "build: let go build stamp the version inside the image"
```

---

### Task 3: goreleaser でリリースバイナリを作る

**Files:**
- Create: `.goreleaser.yaml`
- Modify: `.gitignore`（末尾に `dist/`）

**Interfaces:**
- Consumes: Task 1 の版の読み方（goreleaser も `-X` を使わない）
- Produces: `dist/` の下のバイナリ（ディレクトリ名は goreleaser の命名規則に従うので決め打ちしない）と `dist/*.tar.gz`。Task 4 のワークフローが `goreleaser release --clean` で呼ぶ

- [ ] **Step 1: `.gitignore` に `dist/` を足す**

`# famifo` の節（末尾）に1行足す。

```
# goreleaser の出力。無視しないと未追跡ディレクトリとして数えられ、
# リリースバイナリ全部の版に +dirty が付く。
/dist/
```

- [ ] **Step 2: `.goreleaser.yaml` を作る**

```yaml
---
version: 2
project_name: famifo-proto

builds:
  - env:
      - CGO_ENABLED=0
    goos:
      - linux
    goarch:
      - amd64
    flags:
      - -trimpath
    # -X は使わない。版は go build が .git から埋める。
    ldflags:
      - -s -w

archives:
  - formats:
      - tar.gz

checksum: {}

changelog:
  use: github
```

- [ ] **Step 3: goreleaser を入れる**

Run:

```bash
go install github.com/goreleaser/goreleaser/v2@latest
asdf reshim golang
goreleaser --version
```

Expected: バージョンが表示される。`asdf reshim` が無い環境なら
`$(go env GOPATH)/bin/goreleaser` を直接叩く

- [ ] **Step 4: 設定が妥当か確かめる**

Run: `goreleaser check`
Expected: `1 configuration file(s) validated`

- [ ] **Step 5: タグを打たずにビルドできることを確かめる**

Run: `goreleaser build --snapshot --clean && ls dist/*/famifo-proto`
Expected: 成功し、`dist/` の下にバイナリが1つできる（ディレクトリ名は
`famifo-proto_linux_amd64_v1` のような goreleaser の命名になる）

- [ ] **Step 6: リリースバイナリに `+dirty` が付かないことを確かめる**

Run: `go version -m dist/*/famifo-proto | grep -m1 '^	mod'`
Expected: `mod github.com/yendo/famifo-proto v0.0.0-<日付>-<12桁SHA>` で
終わり、`+dirty` が付いていないこと。付いていたら Step 1 の `.gitignore` が
効いていない（`git status --porcelain` に `dist/` が出るはず）

- [ ] **Step 7: 後始末**

Run: `rm -rf dist`

- [ ] **Step 8: コミット**

```bash
git add .gitignore .goreleaser.yaml
git commit -m "build: add goreleaser for release binaries"
```

---

### Task 4: タグでバイナリとイメージを出す

**着手前に、利用者からワークフローファイルの変更について明示の承認を取ること。**
承認が無いうちは `.github/workflows/` の下に書き込まない。

**Files:**
- Create: `.github/workflows/release.yml`
- Modify: `.github/workflows/docker.yml`（全体を縮小）
- Modify: `README.md`（`## Build` の節の後ろに `## Releases` を新設）

**Interfaces:**
- Consumes: Task 2 の引数なし `docker build .`、Task 3 の `.goreleaser.yaml`
- Produces: `v*` タグの push で GitHub Release（`tar.gz` + チェックサム）と
  `ghcr.io/yendo/famifo-proto:<タグ>` / `:latest`

- [ ] **Step 1: `release.yml` を作る**

```yaml
---
name: Release

on: # yamllint disable-line rule:truthy
  push:
    tags:
      - "v*"

permissions: {}

jobs:
  release:
    name: Release
    runs-on: ubuntu-latest

    permissions:
      # contents to create the release, packages to push the image
      contents: write
      packages: write

    steps:
      - name: Checkout code
        uses: actions/checkout@v7 # zizmor: ignore[unpinned-uses]
        with:
          # go build reads the tag from the history; a shallow clone has none
          fetch-depth: 0
          persist-credentials: false

      - name: Set up Go
        uses: actions/setup-go@v7 # zizmor: ignore[unpinned-uses]
        with:
          go-version-file: go.mod

      - name: Run GoReleaser
        uses: goreleaser/goreleaser-action@f06c13b6b1a9625abc9e6e439d9c05a8f2190e94 # v7
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract metadata
        id: meta
        uses: docker/metadata-action@dc802804100637a589fabce1cb79ff13a1411302 # v6.2.0
        with:
          images: ghcr.io/${{ github.repository }}
          tags: |
            type=ref,event=tag
            type=raw,value=latest

      - name: Login to GitHub Container Registry
        uses: docker/login-action@dbcb813823bdd20940b903addbd779551569679f # v4.6.0
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push
        uses: docker/build-push-action@53b7df96c91f9c12dcc8a07bcb9ccacbed38856a # v7.3.0
        with:
          context: .
          push: true
          provenance: false
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
```

- [ ] **Step 2: `docker.yml` をビルド検証だけに縮める**

全体を次の内容に置き換える。metadata-action・短縮SHAの計算・login・
`build-args`・`push:`・キャッシュ設定がすべて消える。

```yaml
---
name: Docker image

on: # yamllint disable-line rule:truthy
  push:
    branches:
      - "**"

permissions: {}

jobs:
  docker:
    name: Docker image
    runs-on: ubuntu-latest

    permissions:
      contents: read

    steps:
      - name: Checkout code
        uses: actions/checkout@v7 # zizmor: ignore[unpinned-uses]
        with:
          persist-credentials: false

      # The image is published only from a tag, by release.yml. This just
      # keeps a broken Dockerfile from reaching that point unnoticed.
      - name: Build
        run: docker build -t famifo-proto:ci .
```

- [ ] **Step 3: ワークフローを静的に検査する**

Run: `actionlint .github/workflows/release.yml .github/workflows/docker.yml`
Expected: 出力なし

- [ ] **Step 4: YAML の書式を確かめる**

Run: `yamllint .github/workflows/release.yml .github/workflows/docker.yml`
Expected: 出力なし。`yamllint` が入っていなければこのステップは飛ばす
（CI の super-linter が同じ検査をする）

- [ ] **Step 5: `README.md` に取得手順を書く**

`## Build` の節（`CGO_ENABLED=0 go build` のコードブロック）の直後、
`## Usage` の直前に節を足す。

````markdown
## Releases

Tagged builds are published to [GitHub Releases](https://github.com/yendo/famifo-proto/releases)
as a `linux/amd64` tarball, and to `ghcr.io/yendo/famifo-proto` under both the tag and
`latest`. Untagged commits publish nothing; build them yourself.

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

Check `git status` first — goreleaser refuses to release from a dirty tree, and a stray
untracked file would otherwise stamp the binaries `+dirty`.
````

- [ ] **Step 6: コミット**

```bash
git add .github/workflows/release.yml .github/workflows/docker.yml README.md
git commit -m "ci: release binaries and the image on a tag"
```

- [ ] **Step 7: push して CI が緑になることを確かめる**

Run: `git push -u origin feature-versioning`
Expected: `Go` `Lint` `Docker image` の3つが通る。`Release` はタグを
打っていないので起動しない

---

## 残る手作業

計画の外だが、この変更が効いていることを最後に確かめる手順。

- main へマージしたあと `git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0` で
  最初のリリースを出し、GitHub Release に `tar.gz` が付き、
  `docker run --rm ghcr.io/yendo/famifo-proto:v0.1.0 -version` が `famifo-proto v0.1.0`
  と表示することを確かめる
- NAS のイメージを更新し、起動ログの `version=` が `v0.1.0` になっていることを確かめる
