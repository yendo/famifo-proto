# パッケージ境界の再編

## 解決する問題

今後の開発に向けて見通しを良くしたい。現状の痛みは2つある。

**1つの変更が複数パッケージに波及する。**

直近のHEIC拡大表示（`2026-09-02-heic-full-view-design.md`）は `store`（`ThumbSource`）・
`index`（出どころの判定）・`thumb`（パス生成）・`web`（配信の分岐）の4つに触れた。
「この写真の画素がディスクのどこにあるか」という1つの知識が散っているためである。

配信規則は本来この表1枚に収まる。

| ThumbSource | 一覧 | 拡大 |
|---|---|---|
| `famifo` | キャッシュ `<id>.jpg` | 原本 |
| `eadir` | `@eaDir/SYNOPHOTO_THUMB_M.jpg` | `KindOpaque` なら `@eaDir/SYNOPHOTO_THUMB_XL.jpg`、他は原本 |
| なし | 無し（一覧も原本URLを使う） | 原本 |

ところが実際には、行が `web.handleThumb` と `web.handlePhoto` と `web.buildRange` の3箇所に
分かれ、パスの組み立ては `thumb`、分類は `photo`、enumは `store` にある。表を読むには
4ファイルを横断する必要がある。

Synologyの知識も同様に3箇所へ散っている。`thumb`（`@eaDir` のパス組み立てと存在確認）、
`index/scan.go`（`excludedDirs` の `@eaDir` と `#recycle`）、`web`（どのファイルを配信するか）。

**ファイル・パッケージが肥大化している。**

`web` はルーティング・HTTPハンドラ・ビューモデル構築・画像ファイル配信を1パッケージで
兼ねており、`handlers.go` を読んでも役割が一目で取れない。

## 目標

- 配信規則を1箇所で読めるようにする
- Synologyの知識を1パッケージに閉じ込め、DSMの仕様が変わったとき触る場所を1つにする
- 永続化パッケージがドメインの語彙を定義している状態を解消する
- 配信規則をHTTPサーバー抜きで単体テストできるようにする

## 目標としないこと

- **振る舞いを変えること**。これは純粋なリファクタリングである。DBスキーマも `thumb_source`
  の値（`"famifo"` `"eadir"`）も変えないので、既存の `famifo-data` はそのまま使える。移行処理は無い
- **レイヤードアーキテクチャの導入**。`internal/domain` / `internal/usecase` /
  `internal/adapter/*` への再配置は採らない。Go側は約2,500行、ハンドラは1本50行程度で、
  usecase層を挟んでも通過するだけの層になる。Goの慣習（インターフェースは利用する側が
  必要最小限だけ定義する）とも噛み合わず、全ファイルが動くため差分が巨大になる。
  「見通しを良くする」という目的に対して間接層が増えるぶん逆方向に働く
- **投機的なインターフェースの導入**。差し替える相手が実在しないポートは作らない
- **フロントエンド**。`app.js`（645行）・`app.css`・テンプレートは触らない。
  `app.js` の肥大化は事実だが、原因がGo側の境界の問題とは別なので別件とする
- **EXIFの二重パース解消**。`takenat.Resolve` と `thumb.orientationOf` は同じファイルを
  別々に開いて同じEXIFを2回パースしている（下記「既知の制約」）。性能という振る舞いが
  変わるため今回の対象外
- **`main.go` のバージョン表示の切り出し**。境界の話と無関係なので見送る

## 設計

### 変更後のパッケージ構成

| パッケージ | 責務 | 依存 |
|---|---|---|
| `config` | 引数の解析・検証（現状維持） | なし |
| `synology` **(新)** | `@eaDir` の規約。パス組み立て・存在確認・除外ディレクトリ名 | なし |
| `photo` | 写真1枚について答えられること全部（型・分類・ID・画像パス） | `synology` |
| `takenat` | 撮影日時の決定（現状維持） | なし |
| `thumb` | サムネイル**生成**のみ | `photo` |
| `store` | SQLite。`photo.Photo` を読み書きする入れ物 | `photo` |
| `index` | ディスクとインデックスの同期 | `photo` `takenat` `synology` `thumb` `store` |
| `web` | HTTP配信のみ | `photo` `store` |

`web` のimportが5つから2つに減り、`store → web` の型依存が切れる。循環は無い
（`thumb → photo → synology` の一方向）。

### `internal/synology` を切り出す

`@eaDir` に関する知識をすべてここへ集める。純粋な文字列操作と `os.Stat` だけで、
画像ライブラリには依存しない。

```go
package synology

func ThumbPath(srcPath string) string  // SYNOPHOTO_THUMB_M.jpg  (旧 thumb.SynoThumbPath)
func LargePath(srcPath string) string  // SYNOPHOTO_THUMB_XL.jpg (旧 thumb.SynoLargePath)
func HasThumb(srcPath string) bool     // 借りられるか            (旧 thumb.HasSyno)

func IsManagedDir(name string) bool    // @eaDir / #recycle       (旧 index.excludedDirs)
func InManagedDir(path string) bool    //                         (旧 index.inExcludedDir)
```

除外ディレクトリ名（`@eaDir` `#recycle`）が `index/scan.go` にあるのは、走査の都合で
そこに書かれただけで、知識としてはSynologyのものである。移すことで、DSMの仕様変更で
触る場所が1つになる。

### ドメイン型を `store` から `photo` へ移す

`Photo` / `ThumbSource` / `IDFor` を `store` から `photo` へ移す。

現状 `web` も `index` も、型を名指すためだけに永続化パッケージをimportしている。
`Photo` の形を変えるとDBパッケージを編集することになり、依存の向きが実態と逆である。
移動後、`store` は `photo.Photo` を読み書きするだけの入れ物になる。

`ErrNotFound` と `DayGroup` は `store` に残す。どちらもクエリの結果に属する型で、
クエリと同じ場所にあるのがGoの慣習に沿う。

### 画像パスの表を `photo` に置く

`thumb.CachePath` を `photo` へ移し、配信規則の2関数を新設する。

```go
package photo

// ThumbPath は一覧に出すサムネイルのパスを返す。無ければ ok=false。
// cacheDir は配置ごとに変わる実行時設定なので引数で受ける。
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
// HEICはSafari以外で表示できないので、借りられるならSynologyのXLに差し替える。
func FullPath(p Photo) string {
	if KindOf(p.Path) == KindOpaque && p.ThumbSource == ThumbSyno {
		return synology.LargePath(p.Path)
	}
	return p.Path
}
```

**なぜ `photo` なのか。** 「この写真の画素がディスクのどこにあるか」はこのアプリの
中心的な事実であって、HTTP配信の都合ではない。`web` の中に置くと、将来キャッシュの
検証やプルーニングのCLIを作ったときに `web` の内部を覗きに行くことになる。

**依存は逆流しない。** パス生成が必要とするのは `path/filepath` だけで、デコーダは
要らない。`thumb.CachePath` が `golang.org/x/image/webp` や `imagemeta` を引きずるのは、
たまたま `Generator` と同じパッケージに同居しているからにすぎない。`CachePath` を
`photo` へ移せば、`photo` は軽いままである。

**`photo → synology` は新しい漏れではない。** `photo` はすでに `ThumbSyno` という
enum値でSynologyの存在を知っている。ドメインがNASの名前を知っている状態は既に
成立しており、パス規約への依存はそれと一貫している。

**MIMEの分岐が消える。** `FullPath` の戻り値がパスだけになるのは、XLのファイル名が
`SYNOPHOTO_THUMB_XL.jpg` で拡張子が `.jpg` だからである。呼び出し側は
`photo.ContentType(photo.FullPath(p))` を引けばよく、`image/jpeg` を明示的に
返す分岐が要らない。

**`cacheDir` を引数で受ける理由。** `Photo` はDBの行から作られる値で、キャッシュの
置き場所を知らない。`ThumbSource` は写真の属性だが、キャッシュのルートは `-data` から
来る配置設定である。値オブジェクトに配置設定を持たせず、呼び出し側から渡す。

### `web` を薄くする

配信の分岐が `photo` へ出るため、ハンドラは引いたパスを `ServeFile` に渡すだけになる。

```go
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

`buildRange` の `if p.ThumbSource != store.ThumbNone` も
`if _, ok := photo.ThumbPath(p, s.thumbDir); ok` になり、enumを直接見なくなる。

あわせて `handlers.go` からビューモデルを `view.go` へ分ける
（`photoView` `itemsView` `galleryView` `dayView` `buildRange`）。
残る `handlers.go` はHTTPハンドラだけになる。

### `thumb` は生成専用になる

`Generator`・`Generate`・`Remove`・`Path`・画素処理（`scaleToFit` `applyOrientation`
`orientationOf` `sourcePixel`）が残る。`Generator.Path` は `photo.CachePath` を使う。
重い画像ライブラリ（`imagemeta`・`golang.org/x/image`）の依存はこのパッケージに閉じる。

### `takenat` は現状のまま

利用箇所は `index/indexer.go` の1つだけだが、分けたままにする。
「`Resolve` は絶対に失敗しない」（EXIFが壊れていてもpanicせずmtimeに落ちる）という
不変条件を、`recover` を含む小さな面積に閉じ込めてテストしているためである。

`photo` へ畳まない理由は2つ。`imagemeta` を使うため、`Photo` 型を使いたいだけの
`store` までEXIFパーサを引きずる。加えて、再編後の `photo` は「すでに分かっている
メタデータから答えを出す」I/Oのない層であるのに対し、`takenat` はファイルを開いて
読んでメタデータを作る取り込み側である。

## 引き換えになるもの

- **パッケージが1つ増える**（`synology`）。`@eaDir` の規約を1箇所に閉じる対価として受け入れる
- **`photo` が増える**。型・分類・ID・パスで約120行になる。「写真1枚について答えられること」
  という一貫した括りなので許容する
- **HEIC拡大表示のような変更で触るパッケージ数は減らない。** 従来は `thumb` + `web` の2つ、
  再編後は `synology` + `photo` の2つである。得られるのは「規則を読む場所が1つになること」と
  「HTTPなしでテストできること」であって、変更範囲の縮小ではない
- **`photo` がSynologyの名前を知り続ける。** ドメインを完全にベンダー中立にするには
  出どころを抽象化したプラグイン機構が要るが、実在する出どころが1つしかない段階では
  投機的な抽象化になる

## テスト

各ステップ単独で `go test ./...` が緑になる順で進める。

1. `internal/synology` を切り出す（`thumb` の `Syno*` と `index` の `excludedDirs` を移す）
2. ドメイン型を `store` → `photo` へ移す（`Photo` `ThumbSource` `IDFor`）
3. `CachePath` を `thumb` → `photo` へ移し、`ThumbPath` / `FullPath` を新設して `web` から呼ぶ
4. `web` を薄くする（分岐除去、`view.go` 分割）

### 既存テスト

import先だけを変え、アサーションは変えない。振る舞いが変わらないことの検証がこのリファクタの
安全網なので、テストの期待値を書き換える必要が生じたら、それは設計の誤りを示す信号として扱う。

### 新しく足すもの

`photo.ThumbPath` / `photo.FullPath` のテーブルドリブンテスト。`ThumbSource` 3種 ×
`Kind` 2種の全組み合わせを網羅する。これまで同じ検証には `httptest` + `Store` + 実ファイルが
必要だったが、`photo.Photo` を入れてパスが返るだけの純粋な関数になるため不要になる。

`TestGridTracksAreSharedByWindowAndProbe` のような既存の構造テストは影響を受けない。

### ブラウザテスト

無変更で通ることを確認する。失敗を見たらまず再実行して再現性を確かめる
（元から2割程度落ちる）。

## 既知の制約

`takenat.Resolve` と `thumb` の `orientationOf` は、同じファイルを別々に開いて同じEXIFを
2回パースしている。

```go
// index/indexer.go
TakenAt: takenat.Resolve(path, fi.ModTime()),  // 1回目: open + imagemeta.Decode
...
ix.gen.Generate(path, p.ID)                    // 2回目: open + imagemeta.Decode
```

両者とも同じ理由（`imagemeta` がISOBMFFとPNGのパスを `recover` で囲っていない）で
`recover` ガードを持っており、同じ問題への対処が2箇所にある。初回フルスキャンは
4,495枚で37分（NASでは数時間）かかるため、1枚あたり2回のopenとEXIFパースは
無視できない可能性がある。

「ファイルを1回開いて撮影日時とOrientationをまとめて返す」形への統合は、性能という
振る舞いを変えるため今回の対象外とする。着手するなら先にフルスキャンの実測を取り、
効果を確かめてから判断する。

## ドキュメント

`docs/design.md` は方針を記した文書であり、パッケージ構成には言及していない。
本再編は振る舞いを変えないため、更新は不要である。
