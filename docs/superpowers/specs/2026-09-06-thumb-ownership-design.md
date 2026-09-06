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
- `photo` を id / path / takenAt / modTime だけの素の型に戻す
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
| `photo` | インデックスの1行。id / path / takenAt / modTime | なし |
| `index/exif` | EXIFの読み取り（現状維持） | なし |
| `thumb` **(移動)** | 派生画像の唯一の所有者。生成・掃除・配信パスの決定 | `photo` `imagefmt` `synology` |
| `store` | SQLite。`photo.Photo` を読み書きする入れ物 | `photo` |
| `index` | ディスクとインデックスの同期 | `photo` `store` `thumb` `index/exif` `imagefmt` |
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

一覧のタイルは常に `/thumb/{id}` を指し、ハンドラが `@eaDir` → famifo製 → 原本 の順に
落ちていく。取り込み時点の判断をDBにキャッシュするのをやめれば、`@eaDir` が後から生えても
消えても自動的に追従する。

**探索順は `@eaDir` が先。** 実際のライブラリでは、HEICもJPEGもほぼ全てに
`SYNOPHOTO_THUMB_M.jpg` がある。取り込み時に借りる側を優先しているので famifo の置き場は
ほぼ空で、そちらを先に見ると大半のタイルで空振りしてから `@eaDir` を見ることになる。

Synologyを使わない利用者ではこの順が裏目に出て、空振り1回ぶん余計に stat する。
拡張子（`imagefmt.IsDecodable`）で順序を変えれば両方を1回にできるが、まずは
固定順で始める。

これで問題2が消え、問題7（判定の二重化）はそもそも判定が1つになるので消え、
`ThumbSource` をDBの永続化形式として持つ問題も消える。

スキーマ変更はDBを作り直す運用なので、列を落とすコストはゼロである。

配信時のstatは1タイルあたり1〜2回増える。増えるのは空振りする経路の1回だけで、
`http.ServeFile` はどのみち配信するファイルを stat する。写真も `-data` も同じマシンの
ローカルディスクにある（`docs/design.md` の「ネットワークディスクは非対応」）ので、
測るほどの差にはならない。

一覧の組み立て（`buildRange`）では確認しない。タイルのURLは出どころによらず
`/thumb/{id}` なので、実際に要求されたタイルの分しか stat しない。

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

HEIC + `@eaDir` なしが、`IndexFile` の宣言する方針に対する事故的な第3の状態になっている
（問題3）。**登録したうえで、UIにプレースホルダを出す**ほうに倒す。

登録しない案は採らない。`IndexFile` のコメントが言う「壊れた画像を登録しない」は、
デコードを試みて失敗したファイルの話である。HEICは壊れておらず、famifoが読まない形式
というだけで、同じ結末に落とすのは筋が違う。`docs/design.md` の「ファイルサーバー側が
真実の情報源であり、インデックスはそれに追従させる」にも反する。加えて、登録しないと
`FullScan` の `Unchanged` 判定に載らないので次の再起動まで拾い直されず、取り込んだ写真が
数日ギャラリーに現れないことがある。

`SmallPath` が ok=false を返し、`/thumb/` がプレースホルダのSVGを配る。404にして
`<img>` を壊さないのは、貼り替えのたびに失敗を検知し直す仕掛けをクライアントに
持たせないためである。タイルは idiomorph が同じ要素のまま貼り替えるので、JSが付けた
印は貼り替えで消え、画像の再読み込みも起きない。

「ブラウザが表示できるか」の判定には `imagefmt.IsDecodable` を使う。いまの対応表では
「famifoがデコードできる形式」と一致しているためだが、別の問いである。Goがデコード
できないがブラウザは表示できる形式（AVIFなど）を足すときは分ける必要がある。

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

### `internal/index/exif` は動かさない

`thumb` を `index` の下から出したのは、書き手（`index`）と読み手（`web`）の2人が
いたからである。同じ問いを `exif` に当てると、利用者は `index` だけである。

```
$ go list -deps ./internal/thumb | grep famifo
internal/imagefmt
internal/photo
internal/synology
internal/thumb          ← exif は無い
```

`thumb` が受け取るのは `orientation uint16` という値であって、`exif` パッケージには
依存していない。`photo` や `thumb` のコメントに `internal/index/exif` が出てくるのは
出どころを説明しているだけである。

したがって `index` のパッケージコメントが言う「取り込み時にしか使わないものは
サブパッケージに置く」のとおりで、動かす理由がない。

**動かす条件**は `index` 以外がEXIFを読みたくなったときで、現実的な引き金は拡大表示に
撮影情報（カメラ・レンズ・絞り）を出す機能である。そのとき `web` が読み手になるので、
`thumb` と同じ理由で `internal/exif` へ出す。

`open` を1回に減らす件はこの判断を変えない。`exif.Read` の引数が `path` から `*os.File`
に変わるだけで、呼ぶのは `index` のままである。

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

各ステップ単独で `go test ./...` が緑になる順で進めた。番号は上の設計の項番とは一致しない。

1. `imagefmt` を切り出す（(5)。(1)がMIMEの表を必要とするため先に置く）
2. `index/thumb` を `internal/thumb` へ出し、`SmallPath` / `LargePath` を生やして
   `web` から `photo.ThumbPath` / `FullPath` の呼び出しを移す（(1)）
3. `thumb_source` を落として配信時に解決する（(2)）
4. HEICの穴を塞ぐ（(4)）

**(3) は独立したステップにならなかった。** `thumb_source` が消えると `photo.New` を先に
組み立てられるようになり、`Prepare` が `p.ModTime()` から出力の名前を決めるため、
版を2回取る余地が構造的に無くなる。3番目に着手した時点で既に直っていた。

**`open` を1回にまとめる案は採らない。** 当初は (3) のうち性能の部分として残していた。
`exif` と `thumb` が同じファイルを別々に開いているので、`index` が1回開いて `*os.File` を
両方へ渡せば1回減る、という案である。実測したところ削る価値が無かった。

205枚（`~/Pictures` から22枚おきに抽出、平均3.70MB、8コア、strace無し）:

| | 所要時間 | 1枚あたり |
|---|---:|---:|
| DBもサムネイルも無い（真の初回） | 1m43.163s | **503 ms** |
| DB有り・変更無し（`Unchanged` 経路） | 1 ms | 4.9 µs |
| DBを消してサムネイルは残す | 31 ms | **151 µs** |

3番目が、`os.Stat` ・EXIFパース・`@eaDir` の確認・`ReadDir`・SQLite書き込みを全部やって
151µsである。初回との差3300倍は、まるごとデコード・縮小・エンコードの時間である。
削れるのは503msのうち `open` 1回ぶん、数µsにすぎない。

加えて、`@eaDir` から借りる写真では `Prepare` が `generate` を呼ばないので、2回目の
`open` はそもそも存在しない。実際のライブラリではほぼ全てがこちらに当たる。

なお 503ms × 4497枚 = 37.7分で、`thumb.generate` のコメントにある「4,495枚で37分」と
一致する。

この数字が示す本当の余地は並列化である。初回スキャンは1コアで走っており、8コアを
使えれば桁が変わる。ただし本文書の範囲外なので、事実として記すにとどめる。

既存テストはimport先だけを変え、アサーションは変えないのが原則。ただし3番目と4番目は
振る舞いを変えるため、その2つに限りテストの期待値も動いた。

ブラウザテストは無変更で通ることを確認する。失敗を見たらまず再実行して再現性を
確かめる（元から2割程度落ちる）。

## ドキュメント

`docs/design.md` はパッケージ構成に言及していないため、(1)(3)(5)では更新は不要である。
(2)と(4)は振る舞いが変わるので、採用した場合は「対象メディア」節と「表示」節を見直す。
