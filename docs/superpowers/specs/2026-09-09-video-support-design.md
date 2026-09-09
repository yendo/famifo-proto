# 動画への対応

## 解決する問題

スマホは写真と動画を同じように撮る。撮った直後の記憶では両者は地続きなのに、famifo は
写真しか載せないので、同じ日の同じ場面が一覧から抜け落ちる。README も「Video is out of
scope.」と明記している。

動画を載せるうえでの障害は3つある。

**1. famifo は動画から1フレームも取り出せない。** cgo を使わない方針なので実用になる
デコーダが無く、これは HEVC に限らず H.264 でも同じである。写真の HEIC では
Synology のサムネイルを借りて回避したが、動画にも同じ手が使えるかは確かめていなかった。

**2. 原本を配信しても再生できない端末がある。** Pixel も iPhone も HEVC を吐く。
HEVC を再生できるかは端末にハードウェアデコーダがあるかで決まり、Android の実機は
たいてい持ち、Linux のデスクトップ Chrome はたいてい持たない。famifo を見るのは
Android と PC を含むので、原本だけでは見られない端末が出る。

**3. 撮影日時の規約が写真と違う。** 静止画の EXIF は機種によらずローカル時刻だが、
動画のコンテナはそうではない。同じ Pixel 7a が、写真ではローカル時刻を書き、動画では
UTC を書く。EXIF と同じつもりで読むと動画だけ9時間ずれ、日ごとの区切りで別の日に並ぶ。

## 目標

- `.mp4` と `.mov` をインデックスに載せ、写真と同じ1件として `taken_at` 順に混ぜる
- 一覧のタイルに絵を出す。借りられるなら Synology のサムネイルを借りる
- 借りられないときも「ここに動画がある」と分かる形で出す
- 拡大表示で再生する。ブラウザ互換の変換版が借りられるならそれを配信する
- 撮影日時を機種ごとの規約に従って正しく読む
- 外部プロセスへの依存を増やさない（ffmpeg を呼ばない）

## 目標としないこと

- **自前でのサムネイル生成と変換。** デコーダが無いので原理的にできない。ffmpeg を
  呼べば可能だが、README が掲げる単一バイナリの約束を崩す。借りられない動画には絵を
  出さないほうを選ぶ。
- **再生できない端末への救済。** HEVC の原本しか無く変換版も借りられない動画は、
  Safari 以外では再生できない。変換も、ブラウザに描かせた1フレームの回収もしない。
- **再生時間のサーバ側での取得と表示。** `<video controls>` がブラウザ自身の UI で
  表示する。famifo が `mvhd` から取る必要も、DB に持つ必要もない。
- **`.webm` `.avi` `.3gp` などへの対応。** 実データが `.mp4` と `.mov` の2つなので、
  実物を見てから足す。
- **位置情報からのタイムゾーン解決。** Pixel の動画は `udta/©xyz` に GPS を持つので、
  座標からタイムゾーンを引けば海外で撮った動画も現地時刻で並べられる。それでも今回は
  入れない。効く範囲が狭いためである。iPhone は `com.apple.quicktime.creationdate` に
  実際の時差を持つので GPS を使わなくても正しく、S95 は座標を持たない。**GPS が効くのは
  「Pixel 系の mp4 を、サーバと違うタイムゾーンで撮ったとき」に限られる。** 一方で費用は
  小さくない。座標からタイムゾーン名を得るには境界ポリゴンの埋め込みデータが要り
  （`tzf` など）、`time.LoadLocation` のための tzdata も要る。どちらも純 Go なので cgo の
  柱は崩れないが、バイナリは太る。しかも GPS の無い動画のために `mvhd` をサーバの
  タイムゾーンで読む枝は結局残るので、動画によって振る舞いが変わることになる。
  経度を15で割る近似は、中国・インド・スペインなどで外れるため採らない。
  海外で撮った動画が実際に並びを乱してから、実害を見て足す。

## 調査で分かったこと

設計の根拠になった実測値を残す。対象は手元の2本と、DSM の `@eaDir` 一式である。

**Pixel 7a `PXL_20260909_093855665.TS.mp4`**（15.9MB）
`ftyp` は `isom`。映像 `hvc1`(HEVC)、音声 `mp4a`(AAC)、6.4秒。`mdat` が先で `moov` が
末尾にあり、`mdat` は `size==1` の64bit拡張サイズを使う。`mvhd` の creation_time は
`2026-09-09 09:39:06`。`udta` には `©xyz`（GPS `+35.7546+139.6270`）だけ、`meta/keys` には
`com.android.capture.fps`・`com.android.model`・`com.android.manufacturer` だけで、
**時差の情報はどこにも無い**。

**Canon PowerShot S95 `MVI_0010.MOV`**（72.8MB）
`ftyp` は `qt  `。映像 `avc1`(H.264)、音声 `sowt`(PCM)、29.9秒、`moov` は先頭。
`mvhd` の creation_time は `2011-01-11 20:05:54`。`udta/CNTH` に Canon 独自の JPEG が
埋まっており、その EXIF の `DateTimeOriginal` が `2011:01:11 20:05:54` で `mvhd` と一致する。

**`@eaDir/PXL_20260909_093855665.TS.mp4/` の中身**
`SYNOPHOTO_THUMB_SM.jpg`(240x427)、`SYNOPHOTO_THUMB_M.jpg`(320x569, 15.9KB)、
`SYNOPHOTO_THUMB_XL.jpg`(1080x1920, 294KB)、`SYNOPHOTO_FILM_H.mp4`(3.6MB)、
`SYNOPHOTO_FILM.fail`(0B)、`SYNOPHOTO_THUMB_ORIG.fail`(0B)、`SYNOINDEX_MEDIA_INFO`、
`SYNOINDEX_VIDEO_METADATA`。
`SYNOPHOTO_FILM_H.mp4` は `avc1`(H.264) + AAC、1280x720、6.4秒。**原本の HEVC と違い
どの端末でも再生できる。**
`SYNOINDEX_MEDIA_INFO` には DSM が読んだ撮影日時 `2026-09-09 18:39:06`、コーデック
`hevc`/`aac_lc`、寸法 `1080 1920`、回転 `90` が入っている。

**そこから導ける2つの事実**

- `mvhd` の `09:39:06` と DSM の `18:39:06` の差はちょうど9時間。**Pixel の `mvhd` は UTC** である。
- S95 の `mvhd` は同じファイル内の EXIF `DateTimeOriginal` と一致する。EXIF は必ず
  壁掛け時計の時刻なので、**S95 の `mvhd` はローカル時刻**である。

**写真側の EXIF（対比のため）**
Pixel 7a は `DateTimeOriginal` がローカル時刻で `OffsetTimeOriginal` に `+09:00` を持つ
（ファイル名のほうが UTC）。iPhone 8 Plus と Canon EOS Kiss X2 はローカル時刻でオフセットを
持たない。**静止画は機種によらずローカル時刻**であり、いま famifo が時差を持たない値として
扱っているのは全機種に対して正しい。

**サムネイルと変換は別の工程である。** 上のディレクトリには `SYNOPHOTO_THUMB_M.jpg` が
あるのに `SYNOPHOTO_FILM.fail` がある。片方だけ成功しうるので、写真で使っている
「M があれば XL もある」という導出は動画では使えない。

**確かめていないこと。** iPhone の `.mov` を1本も入手できていない。`ftyp` が `qt  ` で
ありながら `mvhd` は UTC、時差は `com.apple.quicktime.creationdate` に入る、というのは
一般論であって実測ではない。

## 設計

### 「動画かどうか」は MIME から導く

`imagefmt` の表に `.mp4` と `.mov` を足す。`decodable` はどちらも偽である。

```go
".mp4": {"video/mp4", false},
".mov": {"video/quicktime", false},
```

判定は列を足さず導出する。同じ事実を表の2箇所に置かないためで、`media` が ID を
パスから導き直しているのと同じ理由である。

```go
func IsVideo(name string) bool {
	return strings.HasPrefix(supportedExts[ext(name)].mime, "video/")
}
```

対応外の拡張子では `mime` が空文字なので偽になる。

**「ブラウザが表示できるか」は表に足さない。** HEVC を再生できるかは端末次第で、
サーバには答えられない問いだからである。答えを持つのをやめ、「借りられる変換版が
あればそれを配信する」に置き換える。`thumb.SmallPath` のコメントが予告していた
「`IsDecodable` を browser 判定に流用している」という負債は、動画では
`IsDecodable` が偽になって自動的にプレースホルダへ落ちるため、書き換えずに済む。

### 撮影日時は `internal/index/videometa` が読む

`internal/index/exif` の隣に置く。契約も同じで、失敗せず、パニックせず、必ず値を返す。

```go
type Meta struct {
	TakenAt time.Time // 読めなければゼロ値
}

func Read(path string) Meta
```

`exif` を拡張しないのは、あのパッケージの存在理由が imagemeta のパニックを1箇所に
閉じ込めることだからである。`videometa` は外部ライブラリを使わず自分で箱をたどる。

読み方は次のとおり。

1. 先頭から箱をたどり、`ftyp` を読む。メジャーブランドが `qt  ` であるか、互換ブランドの
   並びに `qt  ` が含まれていれば QuickTime とみなす
2. `mdat` はサイズで飛ばす。`size==1` の64bit拡張サイズを必ず扱う。これを外すと
   `moov` が末尾にある Pixel の mp4 に永久に到達しない
3. `moov/mvhd` の creation_time を取る（1904-01-01 起点の秒。version 0 は32bit、version 1 は64bit）
4. `qt  ` なら `moov/meta` と `moov/udta/meta` の両方で `keys`+`ilst` を探し、
   `com.apple.quicktime.creationdate`（`2026-09-09T18:39:06+0900` 形式）を読む
5. 解釈は、`qt  ` でなければ `mvhd` を UTC、`qt  ` で Apple のキーがあればその時差付きの値、
   `qt  ` でキーが無ければ `mvhd` を `time.Local` の壁掛け時計として組み立てる

creation_time が 0 の場合と、解釈の結果が1970年より前になる場合はゼロ値を返す。
呼び出し側が mtime に落ちるので、写真で EXIF が読めなかったときと同じ扱いになる。

取り込み側は `indexFile` に分岐が1つ増えるだけである。`imagefmt.IsVideo(path)` が真なら
`videometa.Read`、偽なら従来どおり `exif.Read` を呼ぶ。**動画の向きは常に1として扱う。**
回転はブラウザが `tkhd` の行列を見て自分で当てるうえ、借りるサムネイルは Synology が
回転済みで書いている（実測 320x569 の縦長）。famifo が動画の画素に触ることは無いので、
向きを持ち回る意味がない。

壊れた入力は普通に存在する。サイズが過小な箱、深すぎる入れ子、途中で切れたファイル。
`exif.Read` と同じく `recover` で囲み、入れ子の深さ・箱の個数・`moov` のサイズに上限を置く。
長時間動くデーモンなので、ここで落ちると HTTP サーバごと道連れになる。

### サムネイルと再生するファイルは借りる

**一覧のタイルは既存コードのまま動く。** `synology` はパスを組んで `os.Stat` するだけで
拡張子も中身も見ないため、`HasThumbM` に動画のパスを渡せば
`@eaDir/<動画名>/SYNOPHOTO_THUMB_M.jpg` が見つかる。借りられない動画は `IsDecodable` が
偽なので `SmallPath` が `ok=false` に落ち、配信側がプレースホルダを出す。

`thumb.Prepare` も変更しない。動画では `IsDecodable` が偽なので、「借りられるなら借りる、
駄目なら何も残さない」という既存の分岐にそのまま乗る。famifo が動画に対して書くものは
何も無く、`@eaDir` は読むだけという約束も変わらない。

**拡大表示は `LargePath` に動画の枝を足す。** 動画では静止画の XL ではなく
`SYNOPHOTO_FILM_H.mp4` を返す。`synology` に2つ足す。

```go
const filmName = "SYNOPHOTO_FILM_H.mp4"

func FilmPath(srcPath string) string
func HasFilm(srcPath string) bool // 通常ファイルかつサイズ>0
```

**FILM は自分で `stat` する。** 写真の XL は「M があれば XL もある」で存在確認を
省いているが、動画ではサムネイル生成と動画変換が別の工程で、実測でも片方だけ
失敗していた。DSM は失敗すると0バイトの `.fail` を置くので、サイズを見るガードがそのまま効く。

**見るのは `SYNOPHOTO_FILM_H.mp4` だけにする。** 他の解像度の変換版がある可能性はあるが、
実物を1本しか見ていない以上、見ていない名前を推測で並べない。他の名前しか無い動画では
借りるのをやめて原本に落ちるだけで、壊れ方は穏やかである。

**出どころは DB に持たない。** DSM の動画変換は撮影から遅れて走るため、配信のたびに
`stat` する既存の方針が動画ではとくに効く。変換ができた時点から自動的に配信が切り替わり、
再取り込みは要らない。

`http.ServeFile` は `ServeContent` 経由で Range に答えるので、シークのための変更は要らない。

### 画面

タイルには `data-video` を付け、印を CSS だけで重ねる。

```html
<a class="tile" id="t-{{.ID}}" href="{{.PageURL}}" data-full="{{.FullURL}}" data-date="{{.Date}}"
   {{if .IsVideo}}data-video="1"{{end}}>
  <img src="{{.ThumbURL}}" alt="" loading="lazy" decoding="async">
</a>
```

`.tile[data-video]::after` で描く。タイルごとに要素を足すと、数千件の仮想スクロールで
貼り替えるノードが増えるためである。

**借りられなかった動画に専用のプレースホルダは作らない。** 印はタイル側で常に重なるので、
下地は写真と同じ「絵が無い」印で足りる。印が「動画である」ことを、下地が「絵が無い」ことを
語る。`serveNoPreview` は変更しない。

拡大表示は `#lightbox` に `<video>` を並べて置き、`hidden` で出し分ける。

```html
<div id="lightbox" class="lightbox" hidden>
  <img alt="">
  <video controls playsinline preload="metadata" hidden></video>
  <p class="lb-error" hidden>この端末では再生できません</p>
  ...
</div>
```

要素を都度作らないのは、既存の `show()` が捕まえた要素の `src` を差し替える構造だからである。
`app.js` の変更は2箇所で、チャンク解析に `video: a.dataset.video === "1"` を足し、
`pageAt` と同じ形の同期関数 `isVideoAt(i)` を公開する。`show()` は `await urlAt(i)` の後に
呼ぶので、その時点で塊は取得済みである。

**切り替えと close で `pause()`・`src` の除去・`load()` を行う。** 忘れると次の写真を
開いても前の動画の音が鳴り続け、裏でダウンロードも走り続ける。

**自動再生はしない。** 一覧をめくっていて突然音が鳴るのを避ける。`playsinline` は
iOS Safari が全画面を乗っ取らないようにするため。

**再生に失敗したら短い文を出す。** HEVC を出せない端末では要素は作られたまま再生に
失敗し、画面が真っ黒になる。`error` を拾って `.lb-error` を出す。ダウンロードリンクは出さない。

仮想スクロール・日付の区切り・スクラバーは変更しない。ヘッダの `{{.Total}} 枚` は
`件` に、`aria-label` の「前の写真 / 次の写真」は「前へ / 次へ」に直す。

### 語彙とスキーマ

`photo` は写真を指す名前のまま動画を運ぶことになる。`internal/photo` を
`internal/media` に、`Photo` を `Media` に、表を `media` に、`/photo/{id}` を
`/file/{id}` に改める。ページ URL が既に `/item/{id}` である点も、語彙が写真から
離れていることを示している。

スキーマの変更は名前だけで、列は4本のまま変わらない。

```sql
CREATE TABLE IF NOT EXISTS media (
    id       TEXT PRIMARY KEY,
    path     TEXT NOT NULL UNIQUE,
    taken_at INTEGER NOT NULL,
    mod_time INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_order ON media(taken_at DESC, id DESC);
```

移行は書かず DB を作り直す。`CREATE TABLE IF NOT EXISTS` は既存の `photos` に触らないので、
古い DB のままだと空の一覧になる。それは `probeReadable` が起動時に捕まえる。
**サムネイルの置き場は作り直さなくてよい。** 名前は ID（パスの sha256）と mtime から
決まり、どちらも変わらない。

### 走査と監視は変更しない

コードは変えないが、動画で初めて重みの出る性質が2つあるのでテストで固定する。

**`@eaDir` を降りないガードが決定的になる。** `IsManagedDir` と `InManagedDir` が無ければ、
`.mp4` を対応拡張子に足した瞬間に `SYNOPHOTO_FILM_H.mp4` が独立した動画として一覧に並ぶ。
動画1本につき偽物が1本増える。

**書き込み途中の取り込みは debounce が防ぐ。** 監視は最後の Write から2秒待つ。16MB の
転送中は Write が続くので完了後に1回だけ取り込まれる。2秒以上途切れて途中で取り込まれても、
続きの Write でキューに入り直すので自己修復する。`moov` が末尾にある mp4 では、
不完全なファイルから撮影日時が取れないぶんこの性質が効く。

### 変更する場所

- `internal/imagefmt` — 表に2行、`IsVideo` を追加
- `internal/index/videometa` — 新規
- `internal/index/indexer.go` — `IsVideo` で `videometa` と `exif` を切り替える分岐
- `internal/synology` — `FilmPath` と `HasFilm` を追加
- `internal/thumb` — `LargePath` に動画の枝
- `internal/web/view.go` — `mediaView.IsVideo`
- `internal/web/templates/tiles.html` — `data-video`
- `internal/web/templates/gallery.html` — `<video>`、`.lb-error`、助数詞
- `internal/web/static/app.css` — 動画の印
- `internal/web/static/app.js` — `isVideoAt`、`show()` の切り替え、後始末
- `internal/store` — 表名と識別子
- `internal/media`（旧 `internal/photo`）— 改名
- `README.md`

## 失敗の仕方

- **HEVC の原本しか無い動画は、再生できない端末で見られない。** `.lb-error` が出る。
  DSM が変換を作れば自動的に解消する。
- **`SYNOPHOTO_FILM_H.mp4` 以外の名前しか無い動画は原本に落ちる。** 上と同じ見え方になる。
- **iPhone の `.mov` で Apple のキーが無ければ、撮影日時が9時間ずれる。** `mvhd` を
  ローカル時刻として読むためである。iPhone の動画を1本入手した時点で確かめて直す。
- **`videometa` が何も読めない動画は mtime に落ちる。** 写真で EXIF が読めない場合と同じ。
- **壊れた動画も一覧に出る。** `indexFile` は「自前で作れないのにサムネイルを作れなかった
  写真」を登録しないが、動画はその枝に入らない。壊れているかはデコードして初めて分かり、
  famifo にデコーダが無い以上、登録前に判定できない。黙って消えるより、絵の無いタイルと
  再生失敗の文で分かるほうを選ぶ。
- **海外で撮った動画は現地時刻からずれる。** 写真は EXIF が現地の壁掛け時計なので現地時刻で
  並ぶが、動画はサーバのタイムゾーンで変換される。同じ旅行の写真と動画が食い違う。

## 引き換えになるもの

- 借りられない動画のタイルは絵が無い。Synology のサムネイルに依存した見栄えになる。
- 表示専用の情報を DB に持たない方針を通したので、再生時間はブラウザの `controls` に頼る。
- iPhone の分岐が未検証のまま入る。

## テスト

- `videometa` — 最小の箱ツリーをコードで組む（`exif` のテストが TIFF を手で組むのと同じ流儀）。
  `isom` で UTC、`qt` で Apple キーあり、`qt` でキー無し、`moov` が `mdat` の後ろ、
  64bit サイズの `mdat`、creation_time が 0、途中で切れた入力、`ftyp` が無い入力、深すぎる入れ子
- `imagefmt` — `.mp4`/`.mov` が「載せる・自前では作れない・動画である」になること
- `synology` — `FilmPath` の組み立て、`HasFilm` が0バイトの `.fail` を偽にすること
- `thumb` — 動画で `SmallPath` が `SYNOPHOTO_THUMB_M.jpg`、借りられなければ `ok=false`。
  `LargePath` が `FILM_H` があればそれ、無ければ原本
- `index` — `@eaDir` 配下の `SYNOPHOTO_FILM_H.mp4` を取り込まないこと（走査と fsnotify の両方）。
  動画が写真と同じ `taken_at` 順に混ざること
- `web`（browser_test）— タイルに `data-video` と印が付くこと、拡大表示が `<img>` から
  `<video>` に切り替わること、閉じたときに `src` が外れること

**再生そのものは自動テストで検証しない。** `chromedp/headless-shell` はハードウェア
デコーダを持たず HEVC を再生できない。H.264 の検証用動画も用意できない — 有効な箱ツリーは
手で組めても、映像フレームは ffmpeg 無しには作れないためである。

**手で確かめること。** 手元の Pixel と S95 で撮影日時が `2026-09-09 18:39:06` と
`2011-01-11 20:05:54` になること。`@eaDir` 一式を置いた状態でタイルが
`SYNOPHOTO_THUMB_M.jpg` になり、拡大表示が `SYNOPHOTO_FILM_H.mp4` を返すこと。

## ドキュメント

README の「Video is out of scope.」を削除し、対応形式の表に `.mp4` と `.mov` の行を足す。
サムネイルは Synology から借りること、拡大表示は `SYNOPHOTO_FILM_H.mp4` があればそれを
配信すること、原本が HEVC の場合は端末次第で再生できないことを書く。

## 段取り

PR を2本に分ける。

1. `feature-rename-photo-to-media` — `internal/photo` → `internal/media` の改名だけ。
   表名と URL も含め、振る舞いは変えない
2. `feature-video-support` — 動画対応の本体

分ける理由は、機械的な改名の差分に動画対応の本質が埋もれないようにするためである。
