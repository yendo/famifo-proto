# バージョン番号の一本化

## 解決する問題

版の埋め込み経路が2本あり、出力形式が3種類ある。

| 経路 | 埋め方 | `-version` の出力 |
|---|---|---|
| `make build` / 素の `go build` | Goの自動VCSスタンプ | `01234567 (2026-08-26T11:50:11Z)` |
| Docker | `--build-arg VERSION` → `-ldflags -X main.version` | CI経由で `a4272a5`、渡し忘れると `dev` |
| `.git` もフラグも無い | なし | `unknown` |

同じ「版」を指しているのに桁数が違い（VCSスタンプは8桁、CIは7桁）、片方には時刻が付き、
もう片方には付かない。読み手はまずどちらの経路でビルドされたかを当てる必要がある。

複雑さの出どころは5つある。

1. **`.dockerignore` が `.git` を除いている。** コンテナの中で `go build` が走るのに
   `.git` が無いため、Goの自動VCSスタンプが効かない。その穴を埋めるために
   `--build-arg VERSION` と `-ldflags -X main.version` と `var version string` と
   `formatVersion` の分岐が要る。Dockerfile と README にその事情の説明も要る
2. **短縮SHAの桁数が揃っていない。** `formatVersion` は8桁に切り、`docker.yml` は
   `${GITHUB_SHA::7}` で7桁に切る
3. **CIが短縮SHAを二重に作っている。** `docker.yml` の "Get short SHA" ステップと、
   `metadata-action` の `type=sha` が同じものを別々に計算している
4. **OCIラベルの `org.opencontainers.image.revision` に短縮SHAを入れている。**
   仕様上はフルSHAが入る場所である
5. **コードが存在しない仕組みを前提にしている。** `main.go` のコメントは
   「リリースビルドで `-ldflags` で埋める」と言うが、gitタグもGitHub Releaseも無く、
   その「リリースビルド」はどこにも無い

同時に、配布物はghcrの `latest` イメージだけで、バイナリを取得する手段が無い。

## 目標

- 版の埋め込み経路を1本にする。どのビルド方法でも同じ規則で同じ形式の版が入る
- 版を渡し忘れて `dev` や `unknown` になる経路を無くす
- `docker build .` が引数なし・単体で、正しい版のイメージを作れる状態を保つ
- タグを打つとバイナリとイメージが揃って出る形にする

## 目標としないこと

- **`linux/amd64` 以外への対応。** 配置先はNASの1台だけで、動作確認していない成果物を
  増やす理由が無い。goreleaser の設定で後から足せる
- **goreleaser にイメージを作らせること。** goreleaser のDockerビルドはビルド済み
  バイナリを `COPY` するコンテキストで走るため、ソースからビルドする Dockerfile を
  再利用できない。採るなら `COPY` 専用の2本目の Dockerfile を持つことになり、
  `USER`・`TZ`・ラベル・運用コメントが2箇所に分かれる。`docker build .` 単体が
  動くことを優先し、イメージはリリースワークフローが `docker build` で作る
- **main への push でイメージを配布すること。** ghcr に出るのはタグを打ったときだけに
  する。ブランチのpushでは `docker build` の成否だけを見る
- **semverの自動採番。** タグは手で打つ
- **バージョン表示のパッケージ切り出し。** `2026-09-02-package-boundaries-design.md` で
  見送ったとおり、`main.go` に置いたままにする

## 設計

### 版の決め方

Goの自動VCSスタンプだけを使う。`-ldflags -X` も `--build-arg VERSION` も持たない。
`go build` は `.git` があればモジュール自身のバージョンを埋めるので、
`debug.ReadBuildInfo().Main.Version` を読むだけでよい。

| ビルド | `-version` の出力 |
|---|---|
| タグ `v0.1.0` 上・作業ツリーがきれい | `v0.1.0` |
| タグから進んだコミット | `v0.1.1-0.20260905095503-153d347e4f14` |
| タグを打つ前 | `v0.0.0-20260905095503-153d347e4f14` |
| 未コミット・未追跡のファイルがある | 上記 + `+dirty` |
| `.git` の無い場所（tarballからのビルド等） | `unknown` |

Docker でも `make build` でも goreleaser でも同じ経路を通り、同じ値になる。

### `main.go` と `main_test.go`

`var version string` を削除する。`formatVersion` は `vcs.revision` / `vcs.time` /
`vcs.modified` の組み立てをやめ、`Main.Version` をそのまま返す。

```go
// versionString は実行中のバイナリのバージョンを返す。
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return formatVersion(bi.Main.Version)
}

// formatVersion は go build が埋めた版を表示用に整える。
//
// go build は .git からタグとコミットを読んで版を埋める。.git の無い場所で
// ビルドすると "(devel)" になり、版として読めない。
func formatVersion(v string) string {
	if v == "" || v == "(devel)" {
		return "unknown"
	}
	return v
}
```

テストは4本から2本に減る。

- `formatVersion` が埋まった版をそのまま返すこと
- `.git` の無いビルド（`"(devel)"` と `""`）で `unknown` に落ちること

`vcs.modified` を自前で見なくなるが、`+dirty` は `Main.Version` に含まれるので
「未コミットの変更が混ざったバイナリを見分けられる」性質は失われない。

### `.dockerignore`

`.git` を送る。**追跡ファイルは1つも除外しない。** 除外すると git が「削除された」と
見なし、全ビルドの版に `+dirty` が付く（実測、下記）。除外してよいのは未追跡のものだけ。

```
# .git を送るので、追跡ファイルは除外しない。除くと git がツリーを汚れて
# いると見なし、版に +dirty が付く。
# famifo-data は写真1万枚規模で数百MBになるため除く。
.claude
.superpowers
.idea
.vscode
dist
famifo-data
famifo-proto
```

`docs` `*.md` `.dockerignore` `Dockerfile` は追跡ファイルなので除外をやめる。

ビルドコンテキストは 496K から `.git` のぶん増える。履歴はパックすると 496KiB なので、
`actions/checkout` が取得した状態でも `git gc` 済みの手元でも約1.5Mに収まる。
`git gc` が一度も走っていないワーキングコピーではオブジェクトがバラのまま残るため
16Mになる（このリポジトリの現状がそれで、1448個が未パック）。

### `Dockerfile`

マルチステージのまま。消えるのは3箇所。

- ビルド段と実行段の `ARG VERSION=dev`
- `-ldflags="-s -w -X main.version=${VERSION}"` → `-ldflags="-s -w"`
- `LABEL org.opencontainers.image.revision="${VERSION}"`

`revision` ラベルはCIで `metadata-action` がフルSHAで付ける。ローカルの
`docker build .` には付かないが、版はバイナリ自身が持っている。
`.git` を除いている事情を説明していたコメント（`ARG VERSION` の直前）も削除する。

`.git` を送ってもイメージには影響しない。最終段は `FROM scratch` から
`COPY --from=build /famifo /famifo` するだけなので、`.git` を含むビルド段の
レイヤは捨てられる。

### goreleaser

バイナリとGitHub Releaseだけを担当する。`dockers:` セクションは持たない。

`.goreleaser.yaml`:

```yaml
version: 2
project_name: famifo-proto
builds:
  - env: [CGO_ENABLED=0]
    goos: [linux]
    goarch: [amd64]
    flags: [-trimpath]
    ldflags: [-s -w] # -X は使わない。版はVCSスタンプから入る
archives:
  - formats: [tar.gz]
checksum: {}
changelog:
  use: github
```

`.gitignore` に `dist/` を足す。goreleaser はビルド前に `dist/` を作るので、
無視しないと未追跡ディレクトリとして扱われ、リリースバイナリ全部に `+dirty` が付く。

### ワークフロー

**`release.yml`（新規）** — `v*` のタグpushで起動する。

- `actions/checkout` は `fetch-depth: 0`。既定の浅いcloneではタグが取れず、
  版が擬似バージョンになる
- `goreleaser release --clean` でバイナリとGitHub Releaseを作る
- 続けて `docker build .` し、`metadata-action` の `type=ref,event=tag`（`v1.2.3`）と
  `type=raw,value=latest` で ghcr に push する
- 権限は `contents: write`（Release作成）と `packages: write`（ghcr）

**`docker.yml`（縮小）** — ブランチpushで `docker build` するだけにする。
`metadata-action`・login・`build-args`・`push:`・キャッシュ設定が消え、47行から15行程度になる。
ghcr には何も出ないので `latest` の意味は変わらない。

**`go.yml`** — 変更しない。

### タグ運用

main上のコミットに `v0.1.0` から手で打つ。

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

作業ツリーが汚れているとリリースバイナリに `+dirty` が付くので、タグを打つ前に
`git status` を確認する（goreleaser 自身も汚れたツリーでのリリースを拒否する）。

### README

- `docker build --build-arg VERSION=$(git rev-parse --short HEAD)` を
  `docker build -t famifo .` に置き換える
- 「`.git` はビルド文脈から除いてあるので `VERSION` を渡す」という段落を削除する
- 起動ログの例を新しい形式（`version="v0.1.0"`）に更新する
- GitHub Releases からバイナリを取得する手順を追記する

## 検証したこと

Go 1.27.1 で実測した。

**VCSスタンプの挙動**（`bi.Main.Version`）

| 状況 | 値 |
|---|---|
| タグ `v1.2.3` 上・クリーン | `v1.2.3` |
| タグから1コミット先 | `v1.2.4-0.20260905101335-47a53c8e1416` |
| タグなし | `v0.0.0-20260905101320-106a091565aa` |
| 未追跡ファイルあり | 上記 + `+dirty` |
| `.git` なし | `(devel)` |

`-ldflags` を付けても `Main.Version` は変わらない（`-X` は別の変数を書くだけ）。

**ビルドコンテキストごとの版**（このリポジトリのソースを実際にビルドした）

| コンテキスト | サイズ | 埋まる版 |
|---|---|---|
| 今の `.dockerignore`（`.git` を除外） | 496K | `(devel)` |
| `.git` を送り、追跡ファイルは除外しない | 16M | `v0.0.0-20260905095503-153d347e4f14` |
| `.git` を送るが `docs`/`*.md` は除外したまま | 15M | `...153d347e4f14+dirty` |

サイズは未パックのワーキングコピーで測った値である。`--no-local` でクローンし直すと
`.git` は 620K（pack 496KiB）になり、コンテキストは約1.5Mになる。

3行目が、追跡ファイルを除外してはいけない理由である。

## 既知の制約

- **手元の `make build` は `+dirty` になりやすい。** Goは未追跡ファイルも「汚れ」と
  数えるため、`.claude/` や `.idea/` があるだけで付く。`.dockerignore` で除外される
  Dockerビルドのほうがきれいな版になる、という逆転が起きる。`.gitignore` に
  `.claude/` `.idea/` `.vscode/` を足せば解消するが、この設計の範囲外とする
- **`COPY . .` 層のキャッシュがコミットのたびに外れる。** `.git` が変わるため。
  ソースを変えれば同じ層は外れるので、実際に増える再ビルドは「ソースを変えずに
  コミットだけ進めた場合」に限られる
- **BuildKitのキャッシュエクスポートには `.git` が入る。** 新しい `docker.yml` は
  キャッシュ設定を持たないので、この設計では発生しない
- **タグを打つまで版は `v0.0.0-...` である。** semverらしい版が出るのは最初のタグ以降

## 移行

DBスキーマもデータ形式も変わらないので、既存の `famifo-data` はそのまま使える。
配置先のNASでは、次にイメージを更新した時点で `-version` の表示形式が変わる。
