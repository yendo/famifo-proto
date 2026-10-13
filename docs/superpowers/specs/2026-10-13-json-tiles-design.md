# タイルを JSON で返し、マークアップを JS に寄せる

issue #61 の対応。

## 経緯

タイル（`<a class="tile"><img></a>`）は Go テンプレート `tiles.html` が作り、
`/tiles?chunk=<ci>` はその HTML 断片を返していた。初回ページも先頭チャンクを
同じテンプレートで `#window` に埋め込んでいた。日ごとのカードと日付ラベルは
`gallery.js` の `cardHTML` が作る。

HTML 断片を選んだ理由は 2 つだった。

1. マークアップを Go テンプレートの 1 箇所に置く（`docs/design.md`、
   `2026-08-20-virtual-scroll-design.md`）
2. JS が無効でも一覧が見える。代わりに、JS がカードへ組み直すまでの
   「1 フレームのちらつき」を受け入れる（`2026-08-25-day-sections-design.md`）

どちらも効いていなかった。

- カードとラベルのマークアップは JS が持っており、すでに 2 箇所に分かれている。
  JS はテンプレートの `data-date`・`data-full`・`href` を読むので、属性の構造が
  暗黙の取り決めになっていた。
- JS が無効なときに見えるのは先頭の 120 枚だけで、続きは出ない。写真を開くのも
  ライトボックスの JS なので、実質的に使えない。
- ちらつきは 1 フレームではなかった。2026-10-12 の実測で、平らなグリッドが
  PC で約 70 ms、スマホ相当で 260〜370 ms 見えていた（issue #61）。その間に
  押すと別の写真が開くおそれがある。

## 設計

サーバーはデータだけを JSON で返し、マークアップはすべて JS が持つ。初回ページに
タイルは埋め込まず、先頭チャンクも他のチャンクと同じく `/tiles` で取る。

### サーバー（Go）

```go
type indexView struct {
	Total       int
	ChunkSize   int
	DayGroups   []dayGroup
	OpenIndex   int
	AuthEnabled bool
}

// mediaView は /tiles の応答の 1 要素。
type mediaView struct {
	ID       string `json:"id"`
	PageURL  string `json:"pageUrl"`
	FullURL  string `json:"fullUrl"`
	ThumbURL string `json:"thumbUrl"`
	Date     string `json:"date"`
	IsVideo  bool   `json:"isVideo"`
}
```

- `/tiles?chunk=<ci>` は `Content-Type: application/json` で `[]mediaView` を返す。
  0 件なら `[]`。チャンク番号の検証（不正なら 400）は変えない。
- URL はサーバーが組み立てて送る。ID だけを送ると、URL の規則が Go と JS の
  2 箇所に分かれる。
- `dayGroup` のキーは短いまま（`d`/`n`）にする。数千日ぶん埋め込むため。
  タイルは 1 回 120 件なので読みやすさを取る。
- `buildTilesView` は `[]mediaView` を返す `listMedia(r, offset, limit)` にし、
  `handleTiles` だけが使う。`buildIndexView` はタイルを引かない。
- `tiles.html` と `tilesView` を削除する。`index.html` の `#window` は空で返す。
- CSP は変えない。`/tiles` の fetch は既に `connect-src` で許している。

### クライアント（JS）

`chunk.js`

```js
/**
 * タイル1枚。サーバが返した JSON の要素そのもの。
 * @typedef {object} Tile
 * @property {string} id
 * @property {string} pageUrl その1件のページのURL（/item/<id>）
 * @property {string} fullUrl フルビューのURL（/full/<id>）
 * @property {string} thumbUrl サムネイルのURL（/thumb/<id>）
 * @property {string} date 撮影日（"2026-02-08"）
 * @property {boolean} isVideo 動画か
 */
```

- `request` は `res.json()` の結果をそのまま `loaded` に入れる。
- `parseTiles`、`loadEmbeddedChunk`、`Tile.html` を削除する。`#window` も見なくなる。
- 公開している `Chunks` の 8 つは変えない。`lightbox.js` は `fullUrl`・`pageUrl`・
  `isVideo` しか使わない。

`gallery.js`

- `cardHTML`（文字列）を `cardEl`（要素。未取得が 1 枚でもあれば `null`）にする。
  タイルは `tileEl(t)` が、ラベルは `textContent` で組み立てる。カードの
  `grid-column`・`grid-template-columns` は `el.style` に代入する。
- `render` はカード要素の配列を `Idiomorph.morph(win, cards, { morphStyle: "innerHTML" })`
  に渡す。同梱の Idiomorph は要素の配列を受け取れる。対応付けの id（`t-<id>`、
  `d-<日付>`）は変えない。
- タイル要素に置くのは `class`・`id`・`href`・`data-video`（CSS の再生の印）・
  `data-i`（lightbox が読む）だけにする。`data-full`・`data-date` は JS が `Tile`
  から読むので置かない。

### エスケープ

`html/template` の自動エスケープが無くなる。HTML の文字列を作らず、DOM API
（`createElement`、プロパティへの代入、`textContent`）で組み立てる。値が HTML と
して解釈されないので、エスケープ自体が要らず、書き忘れる余地も無い。

これまで `cardHTML` が DayGroups の日付をエスケープせずに `id` 属性へ入れていた
件も、これで無くなる。

採らなかった案は 2 つ。自動でエスケープするタグ付きテンプレートは、自前で書くか
ライブラリを同梱することになり、属性と本文で規則も違う。値ごとに手でエスケープ
する方法は、値を足すたびに書き忘れる危険が残る。

`style-src 'unsafe-inline'` は残す。Idiomorph は既存の要素へ属性を写すとき
`setAttribute("style", …)` を使い、これは CSP の対象になる。外すかどうかはこの
変更の範囲外とする。

### JS が無効なとき

一覧は空になる（上部バーと件数だけが出る）。`<noscript>` の案内も置かない。
famifo を見るのは家族の端末の標準ブラウザで、JS を切って使う前提が無い。

## 実測

2026-10-13、main（0126a97）と使い捨ての試作 2 つで計測した。

- 写真：`~/Pictures` の 4,499 枚
- ブラウザ：Docker 内の `chromedp/headless-shell`（Chrome 151）
- 条件：PC（1280×800、localhost）と、スマホ相当（390×844、DPR 3、CPU 1/4、
  往復 20 ms）。それぞれキャッシュ無し（cold）と、同じタブで 1 度読んだ後（warm）
- 回数：各条件 7 回の中央値。各条件の最初の 1 巡は捨てた
- 時刻：ページを開いてからの ms。`requestAnimationFrame` で毎フレーム、
  `.daycard` の有無と、画面内のタイルの `<img>` が読み込み済みか
  （`complete && naturalWidth > 0`）を見た

「カード」はカードが最初に描かれたフレーム、「埋まる」は画面内のタイルがすべて
読み込み済みになったフレーム。

| 条件 | HTML 埋め込み（main） | JSON 埋め込み | 埋め込まずに `/tiles` で取る |
|---|---|---|---|
| PC cold：カード／埋まる | 72 / 72 | 42 / 92 | 47 / 97 |
| PC warm：カード／埋まる | 70 / 70 | 36 / 36 | 41 / 41 |
| スマホ相当 cold：カード／埋まる | 291 / 291 | 151 / 269 | 184 / 301 |
| スマホ相当 warm：カード／埋まる | 209 / 209 | 129 / 129 | 166 / 166 |

- main では、カードより前に平らなグリッドが描かれていた（PC で 12 ms、
  スマホ相当で 67 ms から）。JSON の 2 つでは描かれない。
- カードは main より早く出る。main は 120 枚を DOM として描いてから組み直すため
  と見ている。
- issue #61 で心配していた「サムネイルの取得が遅れる」は、キャッシュ無しで
  埋まるまでが main と ±30 ms の差に収まった。代わりに、カードが出てから埋まる
  までの間（PC 約 50 ms、スマホ相当 約 120 ms）は、カードの中のタイルが灰色で
  見える。位置はずれない。
- 埋め込みをやめると、JSON 埋め込みよりカードが PC で約 5 ms、スマホ相当で
  約 35 ms 遅れる。JS が動き出してから `/tiles` を 1 往復待つぶんである。
  それでも main より早く、家庭内 LAN の往復は設定した 20 ms と同程度か短い。
  先頭だけ別経路で渡す特例（`FirstChunk`、`#firstchunk`、埋め込みを `loaded` に
  入れる処理）が消えるので、こちらを採る。
- スマホ相当は Chrome のエミュレーションで、実機の Safari ではない。

## テスト

`web_test.go` は HTTP の応答だけを見る。

- 初回ページでタイルの中身を見ていたテスト（`TestGalleryRendersTiles`、
  `PointsEveryTileAtThumb`、`OrdersNewestFirst`、`UsesTheBorrowedThumbForHEIC`、
  `MarksVideoTiles`、`TestTilesLinkToTheItemPage`）は、`/tiles` の JSON を
  デコードして `thumbUrl`・`fullUrl`・`pageUrl`・`isVideo`・順序を見る形に移し、
  名前を `TestTiles…` にそろえる。
- `TestGalleryEmbedsTotalAndFirstChunk` は、総数と `#spacer`・`#window` があり、
  タイルが入っていないことを見る。
- `TestGalleryTagsFirstChunkWithDates` は削除する。日付は
  `TestTilesTagsEachTileWithTheDayWhereItWasShot` が `/tiles` で見る。
- `TestTilesReturnsFragmentOnly` は、`Content-Type` が JSON であることを見るテストに
  置き換える。
- 他の `/tiles` のテストは、文字列で探す代わりにデコードして比べる。

`browser_test.go`

- 写真の照合に `data-full` を使っている `TestTilesSurviveAPlainScroll` は `href` に
  替える。`href` も写真ごとに一意で、id 属性の付け方に依存しない。
- サーバーが先頭チャンクを `#window` に描いて返す前提のコメント
  （`TestInitialRenderFillsViewport` など）を直す。検査の中身はそのまま使える。
- 平らなグリッドが描かれないことを確かめるテストを足す。読み込みから
  `requestAnimationFrame` で毎フレーム見て、`.daycard` の外に `.tile` がある
  フレームが無いこと。

## 文書

- `docs/design.md` の「バックエンド/フロントエンド境界」を書き換える。
- `docs/web-structure.md` の図から `tiles.html`・`tilesView` を消す。
- `2026-08-20-virtual-scroll-design.md` と `2026-08-25-day-sections-design.md` には、
  この spec で置き換えた旨を追記する。本文は当時の判断の記録として残す。
