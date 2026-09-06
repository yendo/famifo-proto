# 派生画像の所有権の再編

## 経緯

`2026-09-02-package-boundaries-design.md` でパッケージ境界を再編し、配信規則を
`photo` に集めた。この文書はその結果を、当時の判断に縛られずに読み直したものである。

前回の再編は「配信規則を1箇所で読めるようにする」という目標を達成した。一方で、
その1箇所として `photo` を選んだこと、`thumb` を `index/` の下に置いたことが、
今になって効いている不具合を生んでいる。以下は前回の結論の一部を覆す提案である。

## 解決する問題

### 実害のある3つ

**1. サムネイルの「版」を2箇所が独立に決めている**

`IndexFile` は `os.Stat` した `fi.ModTime()` を `photo.New` に渡し、DBの `mod_time` になる
（`internal/index/indexer.go:52,69`）。一方 `thumb.generate` は自分で開いたファイルの
`f.Stat()` から出力ファイル名を決める（`internal/index/thumb/thumb.go:115-119`）。

同じ「版」という鍵を2回、別々に取得している。取り込み中に原本が書き換わると、DBが持つ
名前と実ファイル名がずれる。このとき `HasThumb()` は true のままなので一覧は
`/thumb/{id}` を指し続け、原本へのフォールバックも働かず、404のタイルが残る。

**2. 出どころ（`thumb_source`）が原本のmtimeでしか再評価されない**

`FullScan` はmtimeが変わらなければ `Unchanged` で飛ばす（`internal/index/scan.go:73`）。
watcherは `@eaDir` を監視対象に加えず、イベントも捨てる
（`internal/index/watcher.go:76,159`）。

したがって、取り込み後にSynologyがMを生成しても `thumb_source` は `famifo` のまま、
`@eaDir` を削除しても `eadir` のまま404になる。`thumb_source` は「@eaDir の状態」から
導かれる値なのに、その入力がキャッシュキーに含まれていない。

**3. HEIC かつ `@eaDir` なし が黙って登録される**

`ResolveSource` はこの組み合わせで `ThumbNone, nil` を返す
（`internal/index/thumb/thumb.go:90-91`）。`IndexFile` はこれをエラーとして扱わないので
登録され、一覧は `/photo/{id}` にフォールバックし、`FullPath` も原本の `.heic` を返す。
Safari以外では割れたタイルになる。

`IndexFile` のコメントが宣言する方針（「壊れた画像を登録すると一覧に読み込めない
`<img>` が並ぶため」登録しない）と実装が食い違っており、意図しない第3の状態になっている。

### 構造的な問題

**4. 依存が逆流している。** `internal/index/thumb` が、自分の置き場所のレイアウト規則
（シャーディング・版の命名・拡張子）を上位の `photo` に預けている
（`photo.FamifoThumbDir` / `photo.FamifoThumbPath`）。

**5. `web` が `thumbDir` を知っている。** 配信側が欲しいのは「そのIDのサムネイルのバイト列」
だけだが、`Server.thumbDir` を持ちパスを組み立てている。同じ `cfg.ThumbDir()` が main から
Indexer と Server の2経路に配られ、ずれても誰も気づけない。

**6. 「Synologyから借りる」が2パッケージに割れている。** 借りるか決めるのは `thumb`
（`synology.HasThumbM`）、借りたものを配信するのは `photo`（`ThumbMPath` / `FullPath`）。
「MがあればXLもある」という推論が `synology.go:34-35` と `photo.go:130-131` に二重に書かれている。

**7. 同じ判定が2つのメソッドになっている。** `HasThumb()`（`web/view.go:55`）と
`ThumbPath()` の `ok`（`web/handlers.go:103`）が同じ条件を別の入口で持つ。

**8. `HasFamifoThumb()` は要らない。** `Provider.Remove(id)` は自分の置き場しか触らないので、
eadir由来の写真に呼んでも無害である。この分岐は `ReadDir` 1回を省くためだけに、
「所有権」という概念を `photo` に持ち込んでいる（`index/indexer.go:83,102`）。

**9. `supportedExts` に関心が2つ同居している。** `IsSupportedFile`（走査対象か = `index` の問い）と
`IsDecodableFile`（自前で作れるか = `thumb` の問い）が同じ表に載り、`photo` に置かれている
ため、index・thumb・web が全部 `photo` に依存する星型になっている。

## 根本にある3つの判断

上の症状はほぼ全部、次の3つから派生している。

**A. `photo` を「1枚の写真」ではなく「パスから導ける規則の置き場」として定義した。**

`internal/photo/photo.go:6` が「パスから導ける値の規則はここにしかない」と宣言している。
この軸で切ると、パスから計算できるものは何でも入ってくる。ID、置き場所のレイアウト、
XLへの差し替え、MIME。結果として `photo` は全パッケージが依存するハブになり、本来
持つべきでない依存（`synology`）を抱えた。

分割の軸が「何から導けるか」であって「何が一緒に変わるか」ではなかった。サムネイルの
命名規則が変わったときに一緒に直すべきなのは `thumb` であって `photo` ではない。

**B. `thumb` を「取り込み時にしか使わない」と判断して `index/` の下に置いた**
（`internal/index/indexer.go:4-6`）。

この前提が誤りだった。サムネイル置き場には書き手（`index`）と読み手（`web`）の2人がいる。
配信側から見えなくなったので、足りなくなった半分（読むためのパス組み立て）を `photo` に
押し上げた。問題4・5はこの直接の帰結である。2人の利用者がいる状態は、片方の中に
入れてはいけない。

**C. 「この写真のサムネイル」の所有者が決まっていない。**

事実が3箇所に割れている。

| どこ | 何を主張するか |
|---|---|
| DBの `thumb_source` | どこにあるか（取り込み時点のスナップショット） |
| `thumbDir` 配下の実ファイル | 実際に何があるか |
| `@eaDir` | 何を借りられるか |

これらを突き合わせる場所がなく、鮮度の鍵は3つのうち1つの入力（原本のmtime）しか
含んでいない。問題1・2・3は全部これである。キーを取り違えたキャッシュ、という
よくある壊れ方をしている。

## 目標

- 派生画像（一覧用・拡大用、自前生成・借用を問わず）の所有者を1パッケージにする
- 版（mtime）を1回だけ取得し、下へ渡す
- `photo` を id / path / takenAt / modTime / size だけの素の型に戻す
- 依存を一方向にし、`web` から配置設定（`thumbDir`）を消す

## 目標としないこと

- **レイヤードアーキテクチャの導入**。前回の判断を維持する。規模に対して間接層が
  見合わない
- **投機的な抽象化**。出どころのプラグイン機構は、実在する出どころが1つの段階では作らない
- **フロントエンド**。`app.js` / `app.css` / テンプレートは触らない
- **性能改善それ自体**。下記(3)はopenの回数を減らすが、目的は版の一貫性であって
  高速化ではない。速度を目的にするなら先に実測を取る

## 設計

### 変更後のパッケージ構成

| パッケージ | 責務 | 依存 |
|---|---|---|
| `config` | 引数の解析・検証（現状維持） | なし |
| `imagefmt` **(新)** | 対応拡張子の表。MIMEとデコード可否 | なし |
| `synology` | `@eaDir` の規約（現状維持） | なし |
| `photo` | インデックスの1行。id / path / takenAt / modTime / size | なし |
| `exif` | EXIFの読み取り（現状維持、置き場所は要検討） | なし |
| `thumb` **(移動)** | 派生画像の唯一の所有者。生成・掃除・配信パスの決定 | `photo` `imagefmt` `synology` |
| `store` | SQLite。`photo.Photo` を読み書きする入れ物 | `photo` |
| `index` | ディスクとインデックスの同期 | `photo` `store` `thumb` `exif` `imagefmt` |
| `web` | HTTP配信のみ | `photo` `store` `thumb` |

依存が全部下向きになる。`photo` は `synology` を知らなくなる。

### (1) `internal/index/thumb` を `internal/thumb` へ出す

一覧用（M相当）だけでなく拡大用（XL相当）も同じパッケージの関心にする。どちらも
「原本から導いた別の画像」であり、「Synologyから借りられるなら借りる」という方針は
両者に共通する。問題6で割れているものが、まとめれば消える。

**(5) を先に済ませておく必要がある。** 拡大用のMIMEは `photo` の非公開の `supportedExts`
から引いており（`internal/photo/photo.go:145`）、`FullPath` を移すと `ContentType` は
`Photo` のメソッドとして成立しなくなる。表が `imagefmt` に出ていれば素直に移せる。

**パッケージ名は `thumb` のままにする。** 拡大用も扱うので実態と合わなくなるかと考えたが、
Synology自身がXLを `SYNOPHOTO_THUMB_XL.jpg` と名付けている。このドメインの語彙ではXLも
サムネイルの一種であり、借りてくる相手がそう呼んでいるものを、こちらで言い換える理由はない。
改名の差分も出ない。型名も `Provider` のままにする。`Store` にすると `index` と `web` の
どちらでも `store.Store` と並ぶことになる。

```go
package thumb

func (pv *Provider) Ensure(p photo.Photo, orientation uint16) error // 取り込み時
func (pv *Provider) SmallPath(p photo.Photo) (string, bool)         // 一覧用。無ければ ok=false
func (pv *Provider) LargePath(p photo.Photo) (path, contentType string)
func (pv *Provider) Remove(id string) error
```

**バイト列ではなくパスを返す。** `web` は `http.ServeFile` を使い続ける
（`internal/web/handlers.go:109,121`）。`io.ReadCloser` を返す形にすると、`ServeFile` が
処理しているRangeリクエスト（206）と `If-Modified-Since`（304）が失われ、拡大表示のたびに
数MBを再ダウンロードすることになる。

問題5の本質は「配置設定が2経路に配られている」ことなので、`web` が `string` ではなく
`*thumb.Provider` を持てば解消する。パスを隠すことまでは要らない。

**パス選択が純粋関数のままになる。** 前回の再編は `photo.ThumbPath` / `FullPath` を
「Photoを入れるとパスが返るだけの関数」にして、`httptest` と実ファイルなしで
テストできる状態を作った（`photo_test.go` の `TestThumbPathBySource` 他4本）。
`SmallPath` / `LargePath` はI/Oを持たないので、テーブルドリブンのまま `thumb` へ移せる。
ここをI/Oつきの関数に畳むと、その資産を失う。

`photo` から次の7つが消える。

`FamifoThumbDir` / `FamifoThumbPath` / `ThumbPath` / `HasThumb` / `HasFamifoThumb` /
`FullPath` / `ContentType`

`ThumbSource` は残る。`store` が列として永続化し `photo.Restore` が復元しているため、
消せるのは列を落とす (4) の時点である。ただし上の7つが `photo → synology` の唯一の
理由なので、その依存は (1) で切れる。

`web` からは `thumbDir` が消える。あわせて `main.go` で `Provider` を1つ作り、
`index.New` と `web.NewServer` の両方へ渡す。ここを直さないと `cfg.ThumbDir()` が
2経路に配られたまま残り、問題5は解消しない。

問題4・5・6・7・8がここで片付く。

**前回「`photo` に置く」と判断した理由への回答。** 前回の文書は「`web` の中に置くと、
将来キャッシュ検証やプルーニングのCLIを作ったときに `web` の内部を覗きに行くことになる」
と書いた。この懸念は正しい。ただし答えは `photo` ではなく、`web` でも `photo` でもない
第三の場所である。CLIも `thumb` を使えばよい。

**前回「`photo → synology` は新しい漏れではない」と判断した理由への回答。** 前回は
「`photo` はすでに `ThumbSyno` というenum値でSynologyの存在を知っているから一貫している」
とした。しかし、そのenumを `photo` が持っていること自体が問題の一部だった。既にそう
なっていることは、そのままでよい理由にならない。


### (2) `thumb_source` 列を捨て、配信時に解決する

一覧のタイルは常に `/thumb/{id}` を指し、ハンドラが famifo製 → `@eaDir` → 原本 の順に
落ちていく。取り込み時点の判断をDBにキャッシュするのをやめれば、`@eaDir` が後から生えても
消えても自動的に追従する。

これで問題2が消え、問題7（判定の二重化）はそもそも判定が1つになるので消え、
`ThumbSource` をDBの永続化形式として持つ問題も消える。

スキーマ変更はDBを作り直す運用なので、列を落とすコストはゼロである。

配信時のstatが1タイルあたり1〜2回増える。1チャンク120枚なら240回。ローカルディスクでは
無視できるが、NAS越しの構成では実測が要る。重ければ代案(2')として、`FullScan` の
`Unchanged` 判定に `@eaDir` のMの有無を加える形でも問題2は塞げる（列は残る）。

### (3) 版を1回だけ取り、下へ渡す

`IndexFile` の `os.Stat` と `thumb.generate` の `f.Stat()` が独立している（問題1）のをやめる。
`index` が1回statして、`photo.New` と `thumb.Ensure` の両方へ同じ `fs.FileInfo` を渡す。
`thumb` は自分でstatし直さない。

さらに踏み込むなら、`index` がファイルを1回開いて `*os.File` を `exif` と `thumb` の
両方へ渡す。EXIF読み・stat・デコードが同じ版を見ることが構造的に保証され、写真1枚あたりの
openが3回から1回になる。

これは前回の文書が「既知の制約」として対象外にしたEXIF二重パースの解消と同じ変更である。
前回は「性能という振る舞いが変わる」ため見送ったが、今回は版の一貫性という正しさの
問題として扱う。着手するなら前回と同じ条件で、先にフルスキャンの実測を取る。

### (4) 「一覧に出せない写真」を明示的な状態にする

`ResolveSource` が HEIC + `@eaDir` なしで `ThumbNone, nil` を返すのは、`IndexFile` が
宣言する方針に対する事故的な第3の状態である（問題3）。どちらかに倒す。

- **登録しない。** 宣言通り。ただしHEICが黙って一覧から消えるので、ログには残す
- **登録して、UIはプレースホルダを出す。** `Ensure` が「作れなかった」を戻り値で明示する

後者を推す。写真が存在するのに一覧から消えるのは、原因の追いにくい壊れ方である。

### (5) `supportedExts` を `internal/imagefmt` へ出す

`IsSupportedFile`（走査対象か = `index` の問い）と `IsDecodableFile`（自前で作れるか =
`thumb` の問い）は別の問いである。表を1つに保つ判断は正しいので、表ごと葉の
パッケージへ出して両者から引く。`photo` に置く根拠は「パスから導ける」だけで、それが
根本原因Aそのものだった。

`photo.ContentType` の中身もここへ移す。`thumb.LargePath` が返すMIMEはこの表から引くため、
(1) より先に済ませておく必要がある。

```go
package imagefmt

func IsSupported(name string) bool  // 走査対象か      (旧 photo.IsSupportedFile)
func IsDecodable(name string) bool  // 自前で作れるか  (旧 photo.IsDecodableFile)
func ContentType(name string) string
```

## 未決の判断

- **(2) と (2') のどちらを採るか。** NAS越しでのタイル表示のstatコストを測ってから決める
- **(4) の2択。** プレースホルダを出すならフロントエンドに手が入る
- **`exif` の置き場所。** `index/exif` のままでよいか、`thumb` が向きを必要とする以上
  同じ階層へ出すか

## 引き換えになるもの

- **パッケージが1つ増える**（`imagefmt`）。`thumb` は移動なので純増ではない
- **(2) を採ると配信のたびにファイルシステムを叩く。** 取り込み時に確定させておく現在の
  形のほうが配信は速い。正しさと引き換えである
- **前回の文書の結論を一部覆す。** `2026-09-02-package-boundaries-design.md` は歴史として
  残し、この文書から参照する。前回の目標（配信規則を1箇所で読む）は否定しない。1箇所として
  選んだ場所を変えるだけである
- **`thumb` が大きくなる。** 生成・掃除・借用・配信で200行を超える。ただし
  「派生画像について答えられること全部」という一貫した括りである

## 移行の順序

各ステップ単独で `go test ./...` が緑になる順で進める。番号は上の設計の項番とは一致しない。

1. `imagefmt` を切り出す（(5)。(1)がMIMEの表を必要とするため先に置く）
2. `index/thumb` を `internal/thumb` へ出し、`SmallPath` / `LargePath` を生やして
   `web` から `photo.ThumbPath` / `FullPath` の呼び出しを移す（(1)）
3. 版を1回だけ取る形に直す（(3)）
4. `thumb_source` を落とす（(2)。2の後でないと差分が読めない）
5. HEICの穴を塞ぐ（(4)。独立なのでいつでもよい）

既存テストはimport先だけを変え、アサーションは変えない。ただし(2)と(4)は振る舞いを
変えるため、その2つに限りテストの期待値も動く。

ブラウザテストは無変更で通ることを確認する。失敗を見たらまず再実行して再現性を
確かめる（元から2割程度落ちる）。

## ドキュメント

`docs/design.md` はパッケージ構成に言及していないため、(1)(3)(5)では更新は不要である。
(2)と(4)は振る舞いが変わるので、採用した場合は「対象メディア」節と「表示」節を見直す。
