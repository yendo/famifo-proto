# タイルを JSON で返し、マークアップを JS に寄せる 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `/tiles` を JSON にし、初回ページへのタイルの埋め込みをやめ、タイル・カード・ラベルのマークアップをすべて `gallery.js` が DOM API で組み立てるようにする。

**Architecture:** 先に `gallery.js` を「文字列を組み立てる」から「要素を組み立てる」に変える（Task 1）。この時点ではサーバーは HTML 断片のままで、`chunk.js` が断片から `Tile` の各フィールドを読む。次にサーバーを JSON に切り替え、`chunk.js` は応答をそのまま `Tile` として持つ（Task 2）。最後に文書を直す（Task 3）。

**Tech Stack:** Go（`net/http`、`encoding/json`、`html/template`）、vanilla JS（ES モジュール、JSDoc + `tsc` の型検査）、同梱の Idiomorph 0.8.0、chromedp（ブラウザテスト）

**Spec:** [docs/superpowers/specs/2026-10-13-json-tiles-design.md](../specs/2026-10-13-json-tiles-design.md)

## Global Constraints

- コードコメントとテストのコメントは日本語（既存ファイルに合わせる）。識別子、ログのメッセージ、テストの失敗メッセージは英語
- コミットメッセージは英語の要約1行のみ。本文とトレーラーは付けない。type は `feat:` / `fix:` / `refactor:` / `test:` / `docs:` から選ぶ
- ブランチは `feature-json-tiles`（既にこのブランチ上にある）
- テストは公開APIだけで書く。`internal/web` のテストは外部テストパッケージ `web_test`。Go のテストが見るのは HTTP の応答だけ
- 単体テスト: `make unit-test-race`。1つだけなら `go test -race -run '<名前>' ./internal/web/`
- ブラウザテスト: `make browser-test`（Docker の `chromedp/headless-shell` を使う）。1つだけなら `FAMIFO_BROWSER_TESTS=required go test -tags browser -run '<名前>' ./internal/web/`。コンパイルの確認だけなら `go vet -tags browser ./internal/web/`
- JS の型検査: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`（リポジトリのルートで）
- JS では HTML の文字列を組み立てない。要素は `createElement`、属性はプロパティへの代入、文言は `textContent` で作る（エスケープを不要にするため）
- CSP は変えない（`style-src 'unsafe-inline'` も残す）
- JS が無効なときの代わりの表示（`<noscript>` など）は足さない
- `/tiles` の応答の JSON のキーは `id` / `pageUrl` / `fullUrl` / `thumbUrl` / `date` / `video`。0件なら `[]`
- 公開している `Chunks` の 8 つ（`tileCount` / `chunkOf` / `hasChunk` / `tileAt` / `loadChunk` / `loadTile` / `prefetch` / `abandonOutside`）は変えない

## ファイル構成

| ファイル | 変更 |
|---|---|
| `internal/web/static/gallery.js` | `cardHTML` を `cardEl` に、`tileEl` を新設。`render` は要素の配列を Idiomorph に渡す（Task 1） |
| `internal/web/static/chunk.js` | `Tile` の形を JSON と同じにする（Task 1）。`/tiles` を `res.json()` で読み、`parseTiles`・`loadEmbeddedChunk` を消す（Task 2） |
| `internal/web/view.go` | `tilesView` を消し、`mediaView` に JSON タグを付ける。`buildTilesView` を `listMedia` にし、`buildIndexView` はタイルを引かない（Task 2） |
| `internal/web/web.go` | `handleTiles` が JSON を返す（Task 2）。CSP のコメントの `cardHTML` を直す（Task 1） |
| `internal/web/templates/index.html` | `#window` を空にする（Task 2） |
| `internal/web/templates/tiles.html` | 削除（Task 2） |
| `internal/web/web_test.go` | `/tiles` を JSON として読むテストに移す（Task 2） |
| `internal/web/browser_test.go` | `data-full` の照合を `href` に（Task 1）。平らなグリッドが出ないことのテストを足し、埋め込み前提のコメントを直す（Task 2） |
| `docs/design.md`、`docs/web-structure.md`、2 つの古い spec | Task 3 |

---

### Task 1: カードとタイルを DOM API で組み立てる

サーバーは HTML 断片のまま。`chunk.js` は断片から `Tile` の各フィールドを読むようにし、`gallery.js` はそれを使って要素を組み立てる。挙動は変わらない（既存のブラウザテストが守る）。タイル要素から `data-full` と `data-date` が消えるので、それに頼っているブラウザテストを先に直す。

**Files:**
- Modify: `internal/web/static/chunk.js`（`Tile` の typedef と `parseTiles`）
- Modify: `internal/web/static/gallery.js`（`render`、`cardHTML` → `cardEl`、`tileEl` を新設）
- Modify: `internal/web/web.go`（CSP のコメント）
- Modify: `internal/web/browser_test.go`（`TestTilesSurviveAPlainScroll`、`TestBigDayTakesWholeRowsAndIsLabelled` のコメント）

**Interfaces:**
- Produces: `Tile` は `{ id, pageUrl, fullUrl, thumbUrl, date, video }`（`html` は無い）。Task 2 はサーバーの JSON をこの形で返し、`chunk.js` はそれをそのまま持つ
- Produces: タイル要素の属性は `class="tile"`・`id="t-<id>"`・`href`・（動画なら）`data-video="1"`、それに `render` が書く `data-i`。`data-full`・`data-date` は無い

- [ ] **Step 1: ブラウザテストの写真の照合を `href` に替える**

`internal/web/browser_test.go` の `TestTilesSurviveAPlainScroll` の前のコメントを次のように直す（最後の段落だけ）。

```go
// 同一性の目印にはDOM要素のエキスパンドプロパティを使う。属性ではないので
// HTMLの文字列には現れず、要素が作り直されれば必ず消える。写真の照合には
// href（写真ごとに一意なURL）を使う。id属性の付け方にテストが依存しない
// ようにするため。
```

`markJS` の中の

```js
		window.__before = tiles.map((t) => t.dataset.full);
```

を

```js
		window.__before = tiles.map((t) => t.getAttribute('href'));
```

に、`countJS` の中の

```js
			if (!before.has(t.dataset.full)) continue;
```

を

```js
			if (!before.has(t.getAttribute('href'))) continue;
```

に替える。

`TestBigDayTakesWholeRowsAndIsLabelled` の

```go
	// ラベルはタイルの data-date から作る。テスト写真の先頭の日と一致すること。
```

を

```go
	// ラベルはタイルの日付から作る。テスト写真の先頭の日と一致すること。
```

に替える。

- [ ] **Step 2: ブラウザテストが今のコードのまま通ることを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestTilesSurviveAPlainScroll|TestBigDayTakesWholeRowsAndIsLabelled' ./internal/web/`
Expected: PASS（`href` は今もタイルにあるので、照合の手がかりを替えただけで結果は変わらない）

- [ ] **Step 3: `chunk.js` の `Tile` を最終形にする**

`internal/web/static/chunk.js` の `Tile` の typedef を次に置き換える。

```js
/**
 * タイル1枚。
 * @typedef {object} Tile
 * @property {string} id 写真のID
 * @property {string} pageUrl その1件のページのURL（/item/<id>）
 * @property {string} fullUrl フルビューのURL（/full/<id>）
 * @property {string} thumbUrl サムネイルのURL（/thumb/<id>）
 * @property {string} date 撮影日（"2026-02-08"）
 * @property {boolean} video 動画か
 */
```

`parseTiles` を次に置き換える（コメントも）。

```js
	// サーバが返したHTML断片を、タイル1枚ずつに割る。取得時に1回だけパースする。
	// id 属性の "t-" の後ろが写真のID、data-full 属性が原本のURL、data-date 属性が
	// 日付、data-video 属性が動画かどうか、href がその1件のページ（ライトボックスを
	// 開いたときにアドレス欄へ出すURL）、<img> の src がサムネイルのURL。
	/**
	 * @param {string} html
	 * @returns {Tile[]}
	 */
	function parseTiles(html) {
		const tmp = document.createElement("div");
		tmp.innerHTML = html;
		/** @type {NodeListOf<HTMLElement>} */
		const tiles = tmp.querySelectorAll(".tile");
		return [...tiles].map((a) => ({
			id: a.id.replace(/^t-/, ""),
			pageUrl: a.getAttribute("href"),
			fullUrl: a.dataset.full,
			thumbUrl: a.querySelector("img").getAttribute("src"),
			date: a.dataset.date,
			video: a.dataset.video === "1",
		}));
	}
```

- [ ] **Step 4: `gallery.js` に `tileEl` と `cardEl` を書き、`cardHTML` を消す**

`internal/web/static/gallery.js` の先頭の typedef に1行足す。

```js
/** @typedef {import("./layout.js").Layout} Layout */
/** @typedef {import("./layout.js").CardRowRange} CardRowRange */
/** @typedef {import("./chunk.js").Tile} Tile */
```

`cardHTML` 関数（その前のコメントごと）を、次の2つの関数に置き換える。

```js
	// 1枚のカード。占める列数はレイアウトが決め、ラベルの文言はタイル自身の
	// 日付から作る。DayGroups が古くても、ラベルはそのカードに実際に
	// 写っている日を指す。
	//
	// HTMLの文字列は作らず、要素として組み立てる。値がHTMLとして解釈されない
	// ので、エスケープが要らない。
	/**
	 * @param {CardRowRange} range
	 * @param {number} from
	 * @param {number} to
	 * @returns {HTMLElement | null} 1枚でも未取得なら null
	 */
	function cardEl(range, from, to) {
		const tiles = [];
		for (let i = from; i < to; i++) {
			const t = chunks.tileAt(i);
			if (!t) return null;
			tiles.push(tileEl(t));
		}
		const card = document.createElement("div");
		card.className = "daycard";
		card.id = `d-${range.card.d}`;
		card.style.gridColumn = `span ${range.card.span}`;
		card.style.gridTemplateColumns = `repeat(${range.card.span},1fr)`;
		// 段の途中から貼るとき（大きい日をスクロールしている最中）はラベルを落とす
		if (range.firstRow === 0) {
			const label = document.createElement("div");
			label.className = "daylabel";
			label.textContent = formatDay(chunks.tileAt(from).date);
			card.append(label);
		}
		card.append(...tiles);
		return card;
	}

	// タイル1枚。id は貼り替えを差分で当てるときの対応付けに使う。これが無いと
	// 位置で照合され、1行スクロールしただけで全タイルの src が書き換わる。
	// data-video はCSSが再生の印を重ねるのに使う。
	/**
	 * @param {Tile} t
	 * @returns {HTMLAnchorElement}
	 */
	function tileEl(t) {
		const a = document.createElement("a");
		a.className = "tile";
		a.id = `t-${t.id}`;
		a.href = t.pageUrl;
		if (t.video) a.dataset.video = "1";
		const img = document.createElement("img");
		img.src = t.thumbUrl;
		img.alt = "";
		img.loading = "lazy";
		img.decoding = "async";
		a.append(img);
		return a;
	}
```

注意: `a.href = t.pageUrl` は属性に `/item/<id>` をそのまま書く（プロパティで読むと絶対URLになるが、属性は代入した文字列のまま）。`img.src` も同じ。

- [ ] **Step 5: `render` を要素の配列で貼るように変える**

`render` の中の、貼る内容を組み立てる部分を次のように替える。

変更前:

```js
		// 貼る内容が前回と同じなら、HTMLの組み立ても差分の計算も省く。
		const key = `${from}:${to}:${L.cols}`;
		if (key === renderedKey) return;

		// 先に組み立て、1枚でも欠けていたら renderedKey を据え置いたまま抜ける。
		// 確定を先にすると、欠けたまま貼った状態がキャッシュされて直らない。
		const parts = [];
		for (const range of w.ranges) {
			const rangeFrom = range.from;
			const rangeTo = Math.min(to, range.to);
			if (rangeFrom >= rangeTo) continue;
			const html = cardHTML(range, rangeFrom, rangeTo);
			if (!html) return;
			parts.push(html);
		}
		if (parts.length === 0) return;
```

変更後:

```js
		// 貼る内容が前回と同じなら、要素の組み立ても差分の計算も省く。
		const key = `${from}:${to}:${L.cols}`;
		if (key === renderedKey) return;

		// 先に組み立て、1枚でも欠けていたら renderedKey を据え置いたまま抜ける。
		// 確定を先にすると、欠けたまま貼った状態がキャッシュされて直らない。
		const cards = [];
		for (const range of w.ranges) {
			const rangeFrom = range.from;
			const rangeTo = Math.min(to, range.to);
			if (rangeFrom >= rangeTo) continue;
			const card = cardEl(range, rangeFrom, rangeTo);
			if (!card) return;
			cards.push(card);
		}
		if (cards.length === 0) return;
```

同じ関数の

```js
		Idiomorph.morph(win, parts.join(""), { morphStyle: "innerHTML" });
```

を

```js
		Idiomorph.morph(win, cards, { morphStyle: "innerHTML" });
```

に替える。その直前のコメント（「全置換ではなく差分で当てる。…タイルとカードの id 属性が対応付けの手がかりになる。」）はそのまま残す。

- [ ] **Step 6: CSP のコメントを直す**

`internal/web/web.go` の `contentSecurityPolicy` の前のコメントで

```go
// style-src だけ 'unsafe-inline' を許す。gallery.js が組み立てる日カードが
// style 属性で grid-column と grid-template-columns を持っており（cardHTML を
// 見よ）、マークアップ中の style 属性は style-src-attr の対象になるため、
// 許さないと属性ごと無視されてレイアウトが崩れる。span の数だけクラスを
```

を

```go
// style-src だけ 'unsafe-inline' を許す。gallery.js が組み立てる日カードが
// style 属性で grid-column と grid-template-columns を持っており（cardEl を
// 見よ）、貼り替えのとき Idiomorph が既存の要素へ setAttribute("style", …) で
// 写す。これは style-src-attr の対象になるため、許さないと属性ごと無視されて
// レイアウトが崩れる。span の数だけクラスを
```

に替える（続く行はそのまま）。

- [ ] **Step 7: 型検査**

Run（リポジトリのルートで）: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`
Expected: エラーなし

- [ ] **Step 8: ブラウザテストを全部通す**

Run: `make browser-test`
Expected: PASS。特に `TestTilesSurviveAPlainScroll`（要素の使い回し）、`TestNoRepaintOnPlainScroll`、`TestBigDayTakesWholeRowsAndIsLabelled`（ラベル）、`TestLightboxSwitchesBetweenImageAndVideo`（`data-video`）、`TestCardPositionsMatchTheLayout`（`grid-column`）。

- [ ] **Step 9: 単体テストを通す**

Run: `make unit-test-race`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/web/static/chunk.js internal/web/static/gallery.js internal/web/web.go internal/web/browser_test.go
git commit -m "refactor: Build day cards and tiles as elements instead of HTML strings"
```

---

### Task 2: `/tiles` を JSON にし、先頭チャンクの埋め込みをやめる

**Files:**
- Modify: `internal/web/web_test.go`
- Modify: `internal/web/browser_test.go`
- Modify: `internal/web/view.go`
- Modify: `internal/web/web.go`（`handleTiles` と import）
- Modify: `internal/web/templates/index.html`
- Delete: `internal/web/templates/tiles.html`
- Modify: `internal/web/static/chunk.js`

**Interfaces:**
- Consumes: Task 1 の `Tile` の形 `{ id, pageUrl, fullUrl, thumbUrl, date, video }`
- Produces: `GET /tiles?chunk=<ci>` → `200`、`Content-Type: application/json`、本文は `Tile` の配列（0件なら `[]`）。`GET /` の `#window` は空

- [ ] **Step 1: `/tiles` を JSON として読むテストヘルパーを書く**

`internal/web/web_test.go` の `doGet` の直後に足す。

```go
// tile は /tiles の応答の1要素。クライアント（chunk.js の Tile）との取り決め
// そのものなので、サーバーの型を使わずにここで書き起こす。
type tile struct {
	ID       string `json:"id"`
	PageURL  string `json:"pageUrl"`
	FullURL  string `json:"fullUrl"`
	ThumbURL string `json:"thumbUrl"`
	Date     string `json:"date"`
	Video    bool   `json:"video"`
}

// getTiles は target（/tiles?...）を取り、応答をタイルの配列として読む。
// 取り決めに無いキーが混じっていたら失敗させる。
func getTiles(t *testing.T, h http.Handler, target string) []tile {
	t.Helper()
	rec := doGet(t, h, target)
	require.Equal(t, http.StatusOK, rec.Code)
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	var out []tile
	require.NoError(t, dec.Decode(&out))
	return out
}

// tileIDs はタイルの並びをIDの並びにする。
func tileIDs(tiles []tile) []string {
	ids := make([]string, len(tiles))
	for i, tl := range tiles {
		ids[i] = tl.ID
	}
	return ids
}
```

- [ ] **Step 2: 初回ページと `/tiles` のテストを書き換える**

`internal/web/web_test.go` で、次のテストを**削除**する。

- `TestGalleryRendersTiles`
- `TestGalleryPointsEveryTileAtThumb`
- `TestGalleryOrdersNewestFirst`
- `TestTilesReturnsFragmentOnly`
- `TestGalleryTagsFirstChunkWithDates`
- `TestGalleryUsesTheBorrowedThumbForHEIC`
- `TestTilesLinkToTheItemPage`
- `TestGalleryMarksVideoTiles`

`TestGalleryEmbedsTotalAndFirstChunk` を次に置き換える。

```go
// 初回ページはタイルを持たない。一覧の中身はクライアントが /tiles から取り、
// カードに組んでから貼る。平らなタイルを先に返すと、組み直すまでの間
// カードに入っていない並びが見えてしまう。
func TestGalleryShipsTheTotalButNoTiles(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	for i := range 3 {
		f.addMedia(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), famifoThumb)
	}

	rec := doGet(t, f.h, "/")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	require.Contains(t, body, `data-total="3"`)
	require.Contains(t, body, `id="spacer"`)
	require.Contains(t, body, `id="window"`)
	require.NotContains(t, body, `class="tile"`, "tiles come from /tiles, not the page")
}
```

`TestTilesReturnsRequestedChunk`・`TestTilesDefaultsToFirstWindow`・`TestTilesTagsEachTileWithTheDayWhereItWasShot` を次に置き換え、あわせて新しいテストを足す（`TestTilesRejectsBadChunk` はそのまま残す）。

```go
func TestTilesReturnsJSON(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/tiles?chunk=0")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

// 0件でも null ではなく空の配列を返す。クライアントはそのまま配列として扱う。
func TestTilesReturnsAnEmptyArrayPastTheEnd(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)

	rec := doGet(t, f.h, "/tiles?chunk=0")

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `[]`, rec.Body.String())
}

// 1件ぶんの中身。URL はサーバーが組み立てて送る。ID だけにすると、URL の規則が
// Go と JS の2箇所に分かれる。リンク先は画像そのものではなく写真のページである。
// 新しいタブで開く操作や、リンクのコピーが意味のあるURLを返すようにするため。
func TestTilesDescribeEachItem(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.jpg", time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC), famifoThumb)

	got := getTiles(t, f.h, "/tiles?chunk=0")

	require.Equal(t, []tile{{
		ID:       m.ID(),
		PageURL:  "/item/" + m.ID(),
		FullURL:  "/full/" + m.ID(),
		ThumbURL: "/thumb/" + m.ID(),
		Date:     "2026-02-08",
		Video:    false,
	}}, got)
}

// タイルのURLは出どころによらず /thumb/ である。どのファイルを出すかは配信時に
// 決まるので、一覧を組み立てた時点の状態を焼き付けない。
func TestTilesPointEveryItemAtThumb(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind thumbKind
	}{
		{"nothing to show yet; the handler falls back to the original", noThumb},
		{"a HEIC that can borrow from @eaDir", eadirThumb},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newWebFixture(t, 10)
			m := f.addMedia(t, "a.heic", time.Unix(1600000000, 0), tc.kind)

			got := getTiles(t, f.h, "/tiles?chunk=0")

			require.Len(t, got, 1)
			require.Equal(t, "/thumb/"+m.ID(), got[0].ThumbURL)
		})
	}
}

func TestTilesOrderNewestFirst(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	old := f.addMedia(t, "old.jpg", time.Unix(1600000000, 0), famifoThumb)
	recent := f.addMedia(t, "new.jpg", time.Unix(1700000000, 0), famifoThumb)

	got := getTiles(t, f.h, "/tiles?chunk=0")

	require.Equal(t, []string{recent.ID(), old.ID()}, tileIDs(got),
		"ordered by capture time, newest first")
}

// 動画かどうかはタイルごとに送る。lightbox.js がフルビューの切り替えに使い、
// CSSが再生の印を重ねるのに使う。
func TestTilesMarkVideos(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	still := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)
	video := f.addMedia(t, "clip.mp4", time.Unix(1600000100, 0), noThumb)

	got := getTiles(t, f.h, "/tiles?chunk=0")

	require.Equal(t, []string{video.ID(), still.ID()}, tileIDs(got))
	require.True(t, got[0].Video)
	require.False(t, got[1].Video)
}

func TestTilesReturnsRequestedChunk(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 2)
	var ids []string
	for i := range 5 {
		m := f.addMedia(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), famifoThumb)
		ids = append(ids, m.ID())
	}

	got := getTiles(t, f.h, "/tiles?chunk=1")

	// 新しい順は p4,p3,p2,p1,p0 なので、2枚ずつのチャンクの1番目は p2,p1
	require.Equal(t, []string{ids[2], ids[1]}, tileIDs(got))
}

func TestTilesDefaultsToFirstWindow(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	got := getTiles(t, f.h, "/tiles")

	require.Equal(t, []string{m.ID()}, tileIDs(got))
}

// タイルの日は撮影地の日である。パリで夜に撮った写真は、サーバが JST でも
// その日のタイルになる。
func TestTilesTagsEachTileWithTheDayWhereItWasShot(t *testing.T) {
	// time.Local はプロセス全体で1つしかない。書き換えるテストが並列に走ると、
	// 同時に走っている他のテストの時刻解釈まで巻き添えで変わる。実際 -race が
	// 競合として検出する。このテストは t.Parallel() を呼ばない。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })
	f := newWebFixture(t, 60)
	paris := time.FixedZone("CET", 60*60)
	f.addMedia(t, "a.jpg", time.Date(2026, 3, 1, 21, 0, 0, 0, paris), famifoThumb)

	got := getTiles(t, f.h, "/tiles?chunk=0")

	require.Len(t, got, 1)
	require.Equal(t, "2026-03-01", got[0].Date,
		"cutting the instant in JST would give 2026-03-02")
}
```

`strings` の import がまだ他で使われていることを確かめる（`embeddedDayGroups` などが使う）。

- [ ] **Step 3: Go のテストが失敗することを確かめる**

Run: `go test -race -run 'TestGallery|TestTiles' ./internal/web/`
Expected: FAIL。`TestTilesReturnsJSON`（Content-Type が `text/html`）、`getTiles` を使うテスト（`invalid character '<'`）、`TestGalleryShipsTheTotalButNoTiles`（`class="tile"` が入っている）。

- [ ] **Step 4: 平らなグリッドが描かれないことのブラウザテストを書く**

`internal/web/browser_test.go` の import に `"github.com/chromedp/cdproto/page"` を足す（`input`・`runtime` の並びに）。`TestInitialRenderFillsViewport` の直前に次を足す。

```go
// --- Task: TestNoFlatGridBeforeCards ---
//
// 読み込みから最初のカードが描かれるまで、カードに入っていないタイルが
// 描かれないこと。かつては初回HTMLに先頭のチャンクを平らなタイルの並びとして
// 埋め込み、JSがカードに組み直していた。その間は、ほとんどのタイルが後で
// 別の位置へ動く並びが見えており、実測でPC約70ms、スマホ相当で260〜370ms
// 続いた（issue #61）。その間に押すと別の写真が開く。
//
// 見るのはフレームごとの状態なので、requestAnimationFrame で数える。
// ページのスクリプトより先に動かすため、文書の生成時に差し込む。
func TestNoFlatGridBeforeCards(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// 最初のカードが現れたフレームで止まる。それまでに #window の直下に
	// .tile があったフレームの数を __flatFrames に数える。
	const countFlatFramesJS = `
		window.__flatFrames = 0;
		window.__cardSeen = false;
		(function frame() {
			if (document.querySelector('#window .daycard')) { window.__cardSeen = true; return; }
			if (document.querySelector('#window > .tile')) window.__flatFrames++;
			requestAnimationFrame(frame);
		})();`

	var res struct {
		FlatFrames int  `json:"flatFrames"`
		CardSeen   bool `json:"cardSeen"`
	}
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(countFlatFramesJS).Do(ctx)
			return err
		}),
		chromedp.Navigate(baseURL),
		waitForGallery(10*time.Second),
		chromedp.Poll(`window.__cardSeen`, nil, chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`({ flatFrames: window.__flatFrames, cardSeen: window.__cardSeen })`, &res),
	)
	require.NoError(t, err)

	require.True(t, res.CardSeen, "no day card was ever drawn")
	require.Zerof(t, res.FlatFrames,
		"tiles outside any day card were drawn in %d frames before the first card", res.FlatFrames)
}
```

- [ ] **Step 5: ブラウザテストが失敗することを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestNoFlatGridBeforeCards' ./internal/web/`
Expected: FAIL（`tiles outside any day card were drawn in N frames`。N は 1 以上）

- [ ] **Step 6: サーバーの型と組み立てを書き換える**

`internal/web/view.go` の `indexView`・`tilesView`・`mediaView` を次に置き換える（`dayGroup` はそのまま）。

```go
// indexView は index.html の入力。タイルは持たない。一覧の中身はクライアントが
// /tiles から取り、カードに組んでから貼る。
type indexView struct {
	Total     int
	ChunkSize int
	// DayGroups は日ごとの件数。html/template が <script> の中でJSONにして埋め込む。
	DayGroups []dayGroup
	// OpenIndex は開いた状態で表示する1件の通し番号。noOpenItem なら閉じたまま。
	OpenIndex int
	// AuthEnabled は認証が有効かどうか。無効ならログアウトのボタンを出さない。
	AuthEnabled bool
}

// mediaView は /tiles の応答の1要素。クライアント（chunk.js の Tile）との取り決め。
// URL はここで組み立てて送る。ID だけを送ると、URL の規則が Go と JS の2箇所に分かれる。
type mediaView struct {
	ID       string `json:"id"`
	PageURL  string `json:"pageUrl"`  // その1件だけを開くURL。タイルのリンク先
	FullURL  string `json:"fullUrl"`
	ThumbURL string `json:"thumbUrl"`
	Date     string `json:"date"`  // "2006-01-02"。撮影地の日。クライアントが日の区切りに使う
	Video    bool   `json:"video"` // タイルに再生の印を出すか、フルビューを <video> にするか
}
```

`buildIndexView` を次に置き換える。

```go
// buildIndexView はトップページの入力を組み立てる。
func (h *Handler) buildIndexView(r *http.Request, openIndex int) (indexView, error) {
	total, err := h.store.Count(r.Context())
	if err != nil {
		return indexView{}, err
	}
	days, err := h.store.DayGroups(r.Context())
	if err != nil {
		return indexView{}, err
	}
	dayGroups := make([]dayGroup, 0, len(days))
	for _, d := range days {
		dayGroups = append(dayGroups, dayGroup{Date: d.Date, Count: d.Count})
	}

	return indexView{
		Total: total, ChunkSize: h.chunkSize,
		DayGroups: dayGroups, OpenIndex: openIndex,
		AuthEnabled: h.auth != nil,
	}, nil
}
```

`buildTilesView` を次に置き換える。

```go
// listMedia はオフセット指定で1チャンク分を組み立てる。0件でも nil ではなく
// 空のスライスを返すので、JSONは null ではなく [] になる。
func (h *Handler) listMedia(r *http.Request, offset, limit int) ([]mediaView, error) {
	items, err := h.store.ListRange(r.Context(), offset, limit)
	if err != nil {
		return nil, err
	}

	// タイルのURLは出どころによらず /thumb/ である。どのファイルを出すかは
	// ハンドラが調べるので、一覧の組み立てではファイルシステムを叩かない。
	out := make([]mediaView, 0, len(items))
	for _, m := range items {
		out = append(out, mediaView{
			ID:       m.ID(),
			PageURL:  "/item/" + m.ID(),
			FullURL:  "/full/" + m.ID(),
			ThumbURL: "/thumb/" + m.ID(),
			Date:     m.Day(),
			Video:    imagefmt.IsVideo(m.Path()),
		})
	}
	return out, nil
}
```

`gofmt` が struct タグの桁をそろえるので、`gofmt -w internal/web/view.go` を通す。

- [ ] **Step 7: `handleTiles` を JSON にする**

`internal/web/web.go` の import に `"encoding/json"` を足す（`"embed"` の次、アルファベット順）。`handleTiles` を、前のコメントごと次に置き換える。

```go
// handleTiles は仮想スクロール用に、1チャンク分の写真をJSONで返す。
// マークアップはクライアントが組み立てる。
//
// 受け取るのはチャンクの番号だけで、1チャンクの枚数はサーバーが決める。クライアントが
// 件数を指定できると、大きな値1つで全件を引いて描かせられる。
func (h *Handler) handleTiles(w http.ResponseWriter, r *http.Request) {
	chunk, err := parseChunk(r)
	if err != nil {
		http.Error(w, "bad chunk", http.StatusBadRequest)
		return
	}
	tiles, err := h.listMedia(r, chunk*h.chunkSize, h.chunkSize)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(tiles); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		h.log.Error("failed to encode the tiles", "err", err)
	}
}
```

同じファイルの CSP のコメントに `/tiles` の fetch についての記述（「connect-src が要るのは、仮想スクロールが /tiles を fetch するためである。」）があるが、内容は今も正しいので変えない。

- [ ] **Step 8: テンプレートを直す**

`internal/web/templates/index.html` の

```html
  <div id="spacer">
    <div id="window">
{{template "tiles" .}}
    </div>
    <div id="layoutprobe" aria-hidden="true"></div>
  </div>
```

を

```html
  <div id="spacer">
    <div id="window"></div>
    <div id="layoutprobe" aria-hidden="true"></div>
  </div>
```

に替える。`internal/web/templates/tiles.html` を削除する。

```bash
git rm internal/web/templates/tiles.html
```

- [ ] **Step 9: Go のテストが通ることを確かめる**

Run: `go test -race -run 'TestGallery|TestTiles|TestItem' ./internal/web/`
Expected: PASS

- [ ] **Step 10: `chunk.js` を JSON に切り替える**

`internal/web/static/chunk.js` を次のように直す。

`Tile` の typedef の1行目を

```js
 * タイル1枚。サーバが /tiles で返した JSON の要素そのもの。
```

にする（フィールドは Task 1 のまま）。

即時関数の冒頭

```js
	/** @type {HTMLElement | null} */
	const root = document.querySelector("#gallery");
	const win = document.querySelector("#window");
	if (!root || !win) return null;
```

を

```js
	/** @type {HTMLElement | null} */
	const root = document.querySelector("#gallery");
	if (!root) return null;
```

にする。

`request` の中の

```js
			if (!res.ok) throw new Error(`tiles ${res.status}`);
			const html = await res.text();
			const tiles = parseTiles(html);
			loaded.set(ci, tiles);
			return tiles;
```

を

```js
			if (!res.ok) throw new Error(`tiles ${res.status}`);
			/** @type {Tile[]} */
			const tiles = await res.json();
			loaded.set(ci, tiles);
			return tiles;
```

にする。

`parseTiles` 関数（前のコメントごと）、`loadEmbeddedChunk` 関数（前のコメントごと）、その呼び出し `loadEmbeddedChunk();` を削除する。先頭チャンクは、他のチャンクと同じく `render` が `loadChunk(0)` で取りに行く。

- [ ] **Step 11: 埋め込みを前提にしたブラウザテストのコメントを直す**

`internal/web/browser_test.go` の `TestInitialRenderFillsViewport` の前のコメント

```go
// 注意: サーバは index.html の中で最初の1チャンク（testChunkSize枚）を #window に
// 直接描画して返す。そのため「タイルが存在する」「下端がビューポートを超える」
// だけを見ると、gallery.js が一切動かなくても通ってしまう。仮想スクロールが
// 実際に働いてチャンクを追加で貼ったこと（tileCount > testChunkSize）まで見る。
```

を

```go
// 1チャンク（testChunkSize枚）だけではこのビューポートを埋めきれない。
// 「タイルが存在する」「下端がビューポートを超える」に加えて、仮想スクロールが
// 2つ目以降のチャンクも取って貼ったこと（tileCount > testChunkSize）まで見る。
```

に替える。同じテストの中の

```go
	// gallery.js が動いていなければ、#window にはサーバが埋めた1チャンクしか無い。
	require.NoErrorf(t, pollErr,
		"the tile count in #window stays at %d. It is still the first chunk (%d tiles) the "+
			"server filled in, so virtual scrolling is pasting no further chunks; gallery.js looks dead",
		res.TileCount, testChunkSize)
```

を

```go
	// 先頭のチャンクより先を取っていなければ、#window は1チャンクぶんで止まる。
	require.NoErrorf(t, pollErr,
		"the tile count in #window stays at %d, no more than the first chunk (%d tiles); "+
			"virtual scrolling is pasting no further chunks",
		res.TileCount, testChunkSize)
```

に替える。

- [ ] **Step 12: 型検査**

Run（リポジトリのルートで）: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`
Expected: エラーなし

- [ ] **Step 13: ブラウザテストを全部通す**

Run: `make browser-test`
Expected: PASS。`TestNoFlatGridBeforeCards` が通るようになる。

先頭のチャンクも `/tiles` で取るようになったので、次の2つは初回表示までの時間が延びる。落ちたらタイムアウトの予算を見直す（実装を疑う前に、待ちの合計がコメントの計算と合っているかを確かめる）。

- `TestFailedChunkFetchIsRetried`: チャンク0の1回目も500で落ちるので、初回表示が `failedFetchDelay`（0.5秒）と取り直しの待ち（`RETRY_MS`、3秒）のぶん遅れる。`waitForGallery(30*time.Second)` に収まる見込み
- `startStallGallery` を使うテスト（`TestLongScrollDoesNotStallOnSlowServer`）: チャンク0も直列化と遅延の対象になる

- [ ] **Step 14: 単体テストを通す**

Run: `make unit-test-race`
Expected: PASS

- [ ] **Step 15: Commit**

```bash
git add -A internal/web
git commit -m "feat: Return tiles as JSON and stop embedding the first chunk"
```

---

### Task 3: 文書を直す

**Files:**
- Modify: `docs/design.md`
- Modify: `docs/web-structure.md`
- Modify: `docs/superpowers/specs/2026-08-20-virtual-scroll-design.md`
- Modify: `docs/superpowers/specs/2026-08-25-day-sections-design.md`

- [ ] **Step 1: `docs/design.md` の境界の節を書き換える**

「実装技術」の

```markdown
- **バックエンド/フロントエンド境界**: HTMLフラグメントパターン
  - サーバが一覧をHTML断片として返し、クライアントがDOMに差し込む
  - マークアップの管理場所を Go テンプレート側に一元化し、二重実装を避ける
  - 当初は htmx で無限スクロールを実現していたが、仮想スクロールへの移行に伴い
    htmx は撤去した（経緯は docs/superpowers/specs/2026-08-20-virtual-scroll-design.md）
  - ただし日ごとのカードと日付ラベルの markup はクライアント側が持つ。
    どの日が横に並ぶかは列数（画面幅）で決まり、サーバーはレイアウト済みの
    HTMLを返せないため。列数をサーバーに送る案は、レイアウト規則がGoとJSに
    二重実装されるので採らなかった（経緯は
    docs/superpowers/specs/2026-08-25-day-sections-design.md）
```

を

```markdown
- **バックエンド/フロントエンド境界**: サーバはデータをJSONで返し、一覧のマークアップは
  すべてクライアントが持つ
  - `/tiles` は1チャンク分の写真をJSONで返す。URLはサーバが組み立てて送り、
    URLの規則をGoとJSの2箇所に分けない
  - タイル・日ごとのカード・日付ラベルは gallery.js が DOM API で組み立てる。
    HTMLの文字列を作らないので、エスケープが要らない
  - どの日が横に並ぶかは列数（画面幅）で決まり、サーバーはレイアウト済みの
    HTMLを返せない。列数をサーバーに送る案は、レイアウト規則がGoとJSに
    二重実装されるので採らなかった（経緯は
    docs/superpowers/specs/2026-08-25-day-sections-design.md）
  - 初回ページにタイルは埋め込まない。先頭のチャンクも /tiles で取る。埋め込んだ
    平らなタイルをカードに組み直す間、並びが動いて見えていたため。JSが無効だと
    一覧は出ない（経緯と実測は docs/superpowers/specs/2026-10-13-json-tiles-design.md）
  - 当初は htmx で無限スクロールを実現していたが、仮想スクロールへの移行に伴い
    htmx は撤去した（経緯は docs/superpowers/specs/2026-08-20-virtual-scroll-design.md）
```

に替える。同じファイルの

```markdown
  - 一覧の断片を返す内部エンドポイントは `/items` から `/tiles` に改めた。
```

を

```markdown
  - 一覧の中身を返す内部エンドポイントは `/items` から `/tiles` に改めた。
```

に替える。「動的サーバーサイドHTMLレンダリング + vanilla JS」の行は、ページの骨組みは今もサーバーが描くので変えない。

- [ ] **Step 2: `docs/web-structure.md` の図を直す**

```
  GET /tiles       handleTiles ----> parseWindow -> buildTilesView -> tmpl "tiles"
```

を

```
  GET /tiles       handleTiles ----> parseChunk -> listMedia -> JSON
```

に替える。

```
indexView                          view.go
  +-- tilesView (embedded)
  |     +-- []mediaView {ID, PageURL, ThumbURL, FullURL, Date, IsVideo}
  +-- Total, ChunkSize
  +-- DayGroups ([]dayGroup as JSON)
  +-- OpenIndex, AuthEnabled

templates/index.html  {{define "index"}} -> {{template "tiles" .}}
templates/tiles.html  {{define "tiles"}}
```

を

```
indexView                          view.go
  +-- Total, ChunkSize
  +-- DayGroups ([]dayGroup as JSON)
  +-- OpenIndex, AuthEnabled

[]mediaView (JSON from /tiles)     view.go
  {id, pageUrl, fullUrl, thumbUrl, date, video}

templates/index.html  {{define "index"}}
```

に替える。

- [ ] **Step 3: 古い spec に追記する**

`docs/superpowers/specs/2026-08-20-virtual-scroll-design.md` の1行目（見出し）の直後に空行を挟んで足す。

```markdown
> 2026-10-13 追記: 一覧を HTML 断片で返す方式は `2026-10-13-json-tiles-design.md` で
> JSON に置き換えた。マークアップはすべてクライアントが持つ。本文は当時の判断の記録として残す。
```

`docs/superpowers/specs/2026-08-25-day-sections-design.md` の1行目（見出し）の直後に空行を挟んで足す。

```markdown
> 2026-10-13 追記: 「初回表示」で手放さなかった、平らなタイルを埋め込む方式は
> `2026-10-13-json-tiles-design.md` でやめた。組み直しが1フレームでは済んでいなかった
> ため。タイルの markup も Go テンプレートからクライアントへ移した。本文は当時の判断の記録として残す。
```

- [ ] **Step 4: Commit**

```bash
git add docs/design.md docs/web-structure.md docs/superpowers/specs/2026-08-20-virtual-scroll-design.md docs/superpowers/specs/2026-08-25-day-sections-design.md
git commit -m "docs: Describe the JSON tiles and retire the HTML fragment rationale"
```
