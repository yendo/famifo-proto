# 全画面表示の骨格 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 全画面表示に、タップで出し入れする帯（戻るボタン、前後のボタン、左下の撮影者と日時）を作り、動画の標準コントロールと連動させる。

**Architecture:** `/info/{id}` が撮影日時も返す（Task 1）。ライトボックスは左下に撮影者と日時の2行を出し、日時の書式は一覧と共有する `datefmt.js` が持つ（Task 2）。`chrome` クラスで帯を出し入れし、戻るボタンと PC 用の前後ボタンを置く（Task 3）。動画の `controls` を帯に連動させ、左下の2行が標準コントロールと重なるときだけ上げる（Task 4）。

**Tech Stack:** Go（`net/http`、`encoding/json`）、vanilla JS（ES モジュール、JSDoc + `tsc` の型検査）、CSS、chromedp（ブラウザテスト）

**Spec:** [docs/superpowers/specs/2026-10-15-lightbox-chrome-design.md](../specs/2026-10-15-lightbox-chrome-design.md)

## Global Constraints

- コードコメントとテストのコメントは日本語（既存ファイルに合わせる）。識別子、ログのメッセージ、テストの失敗メッセージ、README と `docs/web-structure.md` は英語
- コミットメッセージは英語の要約1行のみ。本文とトレーラーは付けない。type は `feat:` / `fix:` / `refactor:` / `test:` / `docs:` から選ぶ
- ブランチは `feature-photo-owner`（既にこのブランチ上にある）
- テストは公開APIだけで書く。外部テストパッケージ（`web_test`）。テストのためだけの公開や `export_test.go` への追加はしない。JS もテストのためだけに export しない
- 単体テスト: `make unit-test-race`。1つだけなら `go test -race -run '<名前>' ./internal/web/`
- ブラウザテスト: `make browser-test`。1つだけなら `FAMIFO_BROWSER_TESTS=required go test -tags browser -run '<名前>' ./internal/web/`。コンパイルの確認だけなら `go vet -tags browser ./internal/web/`
- JS の型検査: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`（リポジトリのルートで）
- 日時は日本語の書式で出す（「2020年9月13日 21:28」、今年なら年を省く）。ロケール対応はしない
- 左下の文字には背景も影も付けない。ボタン（戻る、前後）だけに丸い半透明の黒い背景を付ける
- 帯の出し入れにアニメーションは付けない
- 動画のコントロールは自前で作らない。ブラウザ標準の `controls` を使う
- 押しても何も起きないボタン（お気に入り、情報、メニュー）は置かない
- JS では HTML の文字列を組み立てない。文言は `textContent` で入れる
- CSP の値は変えない
- 宣言の並びは入口が先。補助はその後ろに出てくる順

## ファイル構成

| ファイル | 変更 |
|---|---|
| `internal/web/view.go` | `infoView.TakenAt` |
| `internal/web/web.go` | `handleInfo` が `TakenAt` を入れる |
| `internal/web/web_test.go` | `/info` のテスト |
| `internal/web/static/datefmt.js` | 新規。`formatDay`、`formatDateTime` |
| `internal/web/static/gallery.js` | `formatDay` を `datefmt.js` から使う |
| `internal/web/static/lightbox.js` | `showInfo`、`setChrome`、`placeCaption`、クリックの振り分け |
| `internal/web/templates/index.html` | `.lb-top`、`.lb-back`、`.lb-caption`、`.lb-when`、`chrome` クラス |
| `internal/web/static/app.css` | `.lb-caption`、`.lb-top`、ボタン、出し入れ、`.raised` |
| `internal/web/browser_test.go` | `startLightboxServer` と新しいテスト |
| `docs/web-structure.md` / `docs/design.md` | `/info` の中身、モジュールの依存、閉じる操作 |

---

### Task 1: `/info/{id}` に撮影日時を足す

**Files:**
- Modify: `internal/web/view.go`（`infoView`）
- Modify: `internal/web/web.go`（`handleInfo`）
- Modify: `internal/web/web_test.go`（`TestInfoNamesTheOwner`、`TestInfoOmitsTheOwnerWithoutAName`、新しいテスト）
- Modify: `docs/web-structure.md`、`docs/design.md`

**Interfaces:**
- Produces: `/info/{id}` の応答 `{"owner"?: string, "takenAt": string}`。`takenAt` は `"2006-01-02T15:04:05"`（撮影地の時刻、時差なし）で、常にある

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/web_test.go` の `TestInfoOmitsTheOwnerWithoutAName` の後ろに足す。

```go
// 撮影日時は撮影地の時刻の文字列をそのまま載せる。クライアントは時差を扱わず、
// 数字に分解して表示する。
func TestInfoCarriesTheTakenAt(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addOwnedBy(t, "a.jpg", 65534)

	rec := doGet(t, f.h, "/info/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		TakenAt string `json:"takenAt"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&got))
	require.Equal(t, m.TakenAtLocal(), got.TakenAt)
}
```

既存の2本も直す。`TestInfoNamesTheOwner` は `DisallowUnknownFields` で読むので、受ける構造体にキーを足す。

```go
	var got struct {
		Owner   string `json:"owner"`
		TakenAt string `json:"takenAt"`
	}
```

`TestInfoOmitsTheOwnerWithoutAName` は応答全体を比べているので、期待値に `takenAt` を入れる。コメントも直す。

```go
// 名前を出せないときは owner のキーごと省く。クライアントは owner が無ければ欄を隠す。
// 名前が出ないことが環境によらず決まっている nobody で確かめる。
func TestInfoOmitsTheOwnerWithoutAName(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addOwnedBy(t, "a.jpg", 65534)

	rec := doGet(t, f.h, "/info/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"takenAt": "`+m.TakenAtLocal()+`"}`, rec.Body.String())
}
```

- [ ] **Step 2: 失敗を確かめる**

Run: `go test -race -run 'TestInfo' ./internal/web/`
Expected: `TestInfoCarriesTheTakenAt` と `TestInfoOmitsTheOwnerWithoutAName` が FAIL（`takenAt` が無い）。`TestInfoNamesTheOwner` は通るかスキップ。

- [ ] **Step 3: 実装する**

`internal/web/view.go` の `infoView`:

```go
// infoView は /info/{id} の応答。全画面表示に重ねる1件分の情報で、一覧では
// 使わないので /tiles には載せない。出せない項目はキーごと省く。
type infoView struct {
	Owner   string `json:"owner,omitempty"` // 撮影者の名前
	TakenAt string `json:"takenAt"`         // "2006-01-02T15:04:05"。撮影地の時刻で、時差は持たない
}
```

`internal/web/web.go` の `handleInfo` の Encode:

```go
	v := infoView{Owner: owner.Name(m.OwnerUID()), TakenAt: m.TakenAtLocal()}
	if err := json.NewEncoder(w).Encode(v); err != nil {
```

- [ ] **Step 4: 通ることを確かめる**

Run: `go test -race -run 'TestInfo' ./internal/web/`
Expected: PASS

- [ ] **Step 5: 文書を直す**

`docs/web-structure.md`:

```
infoView (JSON from /info/{id})    view.go
  {owner?, takenAt}
```

`docs/design.md` の `/info/{id}` の項:

```
  - `/info/{id}` は全画面表示に重ねる1件分の情報（撮影者の名前と撮影日時）を
    JSONで返す。一覧では使わない情報を /tiles に載せず、チャンクを軽く保つ。撮影者はファイルの
```

（続く行はそのまま。行の折り返しは前後に合わせて整える）

- [ ] **Step 6: 全体を確かめてコミットする**

Run: `make unit-test-race`
Expected: PASS

```bash
git add internal/web/view.go internal/web/web.go internal/web/web_test.go docs/web-structure.md docs/design.md
git commit -m "feat: Serve the capture time from /info"
```

---

### Task 2: 左下に撮影者と日時の2行を出す

**Files:**
- Create: `internal/web/static/datefmt.js`
- Modify: `internal/web/static/gallery.js`（`formatDay` を消して import する）
- Modify: `internal/web/static/lightbox.js`（`showOwner` → `showInfo`、`clearInfo`）
- Modify: `internal/web/templates/index.html`（`.lb-caption`）
- Modify: `internal/web/static/app.css`（`.lb-owner` → `.lb-caption`）
- Modify: `internal/web/browser_test.go`（`startLightboxServer`、既存2本の書き換え、新しいテスト）
- Modify: `docs/web-structure.md`

**Interfaces:**
- Consumes: Task 1 の `/info/{id}` の `takenAt`
- Produces:
  - `datefmt.js` の `export function formatDay(d: string): string` と `export function formatDateTime(t: string): string`
  - DOM: `#lightbox .lb-caption` の中に `p.lb-owner`（1行目）と `p.lb-when`（2行目）
  - lightbox.js の内部関数 `showInfo(url, seq)`（Task 4 が末尾に `placeCaption()` を足す）
  - browser_test.go の `lightboxItem` と `startLightboxServer(t, items ...lightboxItem) (string, []string)`

- [ ] **Step 1: テスト用のサーバーを共有のヘルパーにする**

`TestLightboxSwitchesBetweenImageAndVideo` と `TestLightboxShowsTheOwner` はどちらも、DB（store）とサムネイルとハンドラを自前で組み立てている。新しいテストも同じものが要るので、`TestLightboxSwitchesBetweenImageAndVideo` の直前にヘルパーを置く。

```go
// lightboxItem は startLightboxServer に並べる1件。
type lightboxItem struct {
	name    string // ファイル名。.mp4 なら動画、それ以外は写真
	uid     uint32
	takenAt time.Time
}

// startLightboxServer は items だけを持つ小さなサーバーを立て、そのURLと、
// items と同じ順のIDを返す。共有の200枚のコーパスには足さず、共有のChromeだけを
// 借りるテストのためのもの。一覧は新しい順に並ぶ。
//
// 写真は小さなJPEG、動画は中身の無いファイルにする。動画の中身を問わないのは、
// 要素の切り替えや大きさを見るテストだからである。中身の無い動画は読み込みに
// 失敗し、既定の 300×150 で表示される。
func startLightboxServer(t *testing.T, items ...lightboxItem) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	require.NoError(t, os.MkdirAll(mediaDir, 0o755))

	st, err := store.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbs, err := thumb.NewProvider(filepath.Join(dir, "thumbs"))
	require.NoError(t, err)

	bg := context.Background()
	ids := make([]string, 0, len(items))
	for i, it := range items {
		path := filepath.Join(mediaDir, it.name)
		if strings.HasSuffix(it.name, ".mp4") {
			require.NoError(t, os.WriteFile(path, []byte("not a real container"), 0o644))
		} else {
			require.NoError(t, writeTestJPEG(path, i))
		}
		m, err := media.New(path, it.uid, it.takenAt, it.takenAt)
		require.NoError(t, err)
		require.NoError(t, st.Upsert(bg, m))
		ids = append(ids, m.ID())
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := web.NewHandler(st, thumbs, nil, log)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, ids
}
```

既存の2本の組み立て部分をヘルパーに置き換える。

`TestLightboxSwitchesBetweenImageAndVideo`（`requireBrowser(t)` から `t.Cleanup(srv.Close)` までを置き換え。以降の `srv.URL` は `url` にする）:

```go
	requireBrowser(t)

	// 動画のほうを新しくして先頭に並べる。
	uid := uint32(os.Getuid())
	url, _ := startLightboxServer(t,
		lightboxItem{"clip.mp4", uid, time.Unix(1600000100, 0)},
		lightboxItem{"a.jpg", uid, time.Unix(1600000000, 0)},
	)
```

`TestLightboxShowsTheOwner`（`requireBrowser(t)` から `t.Cleanup(srv.Close)` までを置き換え。以降の `srv.URL` は `url` にする）。あわせて冒頭のコメントの「上端に出て」を「左下に出て」に、「同じく、自前の小さなサーバーを立てて」を「同じく startLightboxServer で小さなサーバーを立てて」に直す。

```go
	requireBrowser(t)

	// 名前のある写真を新しくして先頭に並べ、その次に名前の無い写真（nobody）を置く。
	url, ids := startLightboxServer(t,
		lightboxItem{"named.jpg", uint32(os.Getuid()), time.Unix(1600000100, 0)},
		lightboxItem{"nameless.jpg", 65534, time.Unix(1600000000, 0)},
	)
```

使わなくなった import（`image`、`image/jpeg` など）があれば消す。

Run: `go vet -tags browser ./internal/web/`
Expected: エラーなし

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestLightboxSwitchesBetweenImageAndVideo|TestLightboxShowsTheOwner' ./internal/web/`
Expected: PASS（振る舞いは変えていない）

- [ ] **Step 2: 失敗するテストを書く**

`TestLightboxShowsTheOwner` の後ろに足す。

```go
// 全画面の左下の2行目に撮影日時が出ること。撮影地の時刻をそのまま出し、
// ブラウザの時差で動かさない。
//
// 過去の年の写真にする。今年の写真は年を省くので、実行した年で期待値が変わる。
func TestLightboxShowsWhenItWasTaken(t *testing.T) {
	requireBrowser(t)

	// UTC のラベルの値も撮影地の時刻として扱われ、"2020-09-13T21:28:05" で記録される。
	url, _ := startLightboxServer(t,
		lightboxItem{"a.jpg", 65534, time.Date(2020, 9, 13, 21, 28, 5, 0, time.UTC)},
	)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	var got string
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(url),
		waitForGallery(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox .lb-when').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`document.querySelector('#lightbox .lb-when').textContent`, &got),
	)
	require.NoError(t, err, "the capture time never appeared")
	require.Equal(t, "2020年9月13日 21:28", got)
}
```

- [ ] **Step 3: 失敗を確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestLightboxShowsWhenItWasTaken' ./internal/web/`
Expected: FAIL（`.lb-when` が無いので Poll が例外かタイムアウト）

- [ ] **Step 4: 日時の書式を datefmt.js に置く**

`internal/web/static/datefmt.js` を作る。`formatDay` は `gallery.js` から本文ごと移す。

```js
// @ts-check

// 日付と日時の表示用の書式。一覧の日付ラベルと全画面の日時で同じ規則を使う。
// 文字列は Date に通さず、数字に分解して使う。時差の無い文字列をブラウザに
// 解釈させると、書式によっては UTC として扱われてずれる。

// "2026-02-08" → "2026年2月8日"。今年なら年を省く。
// 一覧では最狭の1列(CSSの最小値110px)に収めるため、これ以上長い表記にはしない。
/**
 * @param {string} d
 * @returns {string}
 */
export function formatDay(d) {
	if (!d) return "";
	const [y, m, day] = d.split("-");
	const head = Number(y) === new Date().getFullYear() ? "" : `${y}年`;
	return `${head}${Number(m)}月${Number(day)}日`;
}

// "2026-02-08T09:05:30" → "2026年2月8日 9:05"。日付の部分は formatDay と同じ。
/**
 * @param {string} t
 * @returns {string}
 */
export function formatDateTime(t) {
	if (!t) return "";
	const [day, time] = t.split("T");
	const [hh, mm] = time.split(":");
	return `${formatDay(day)} ${Number(hh)}:${mm}`;
}
```

`internal/web/static/gallery.js` から `formatDay` の関数（コメントごと）を消し、import を足す。

```js
import { chunks } from "./chunk.js";
import { formatDay } from "./datefmt.js";
import { Idiomorph } from "./idiomorph.esm.js";
import { layout, windowAt, yForIndex } from "./layout.js";
```

- [ ] **Step 5: 左下の2行を HTML と CSS に作る**

`internal/web/templates/index.html` の `.lb-owner` の行（コメントごと）を置き換える。

```html
  {{/* 左下の2行。1行目が撮影者、2行目が撮影日時。写真を送るたびに /info から
       取り直す。出せない行は隠したまま。 */}}
  <div class="lb-caption">
    <p class="lb-owner" hidden></p>
    <p class="lb-when" hidden></p>
  </div>
```

`internal/web/static/app.css` の `.lb-owner` のブロック（コメントごと）を置き換える。

```css
/* 左下の2行（撮影者と撮影日時）。押しても下の写真と前後送りに届くようにする。 */
.lb-caption {
	position: absolute;
	left: max(12px, env(safe-area-inset-left));
	bottom: max(12px, env(safe-area-inset-bottom));
	color: rgba(255, 255, 255, 0.9);
	font-size: 0.85rem;
	line-height: 1.4;
	pointer-events: none;
}
.lb-caption p {
	margin: 0;
	white-space: nowrap;
}
```

- [ ] **Step 6: lightbox.js で2行を出す**

import に足す。

```js
import { chunks } from "./chunk.js";
import { formatDateTime } from "./datefmt.js";
import { gallery } from "./gallery.js";
```

`owner` の宣言の後ろに足す。

```js
	/** @type {HTMLElement} */
	const when = box.querySelector(".lb-when");
```

`show()` の中の次の4行を

```js
		// 名前は写真より遅れて届く。前の写真の名前を次の写真に残さない。
		owner.hidden = true;
		owner.textContent = "";
		showOwner(tile.infoUrl, mySeq);
```

次に置き換える。

```js
		// 情報は写真より遅れて届く。前の写真の情報を次の写真に残さない。
		clearInfo();
		showInfo(tile.infoUrl, mySeq);
```

`showOwner` を `showInfo` に置き換え、その後ろに `clearInfo` を置く。

```js
	// showInfo は左下の2行（撮影者と撮影日時）を取って出す。写真の表示は待たせない。
	// 待っている間に別の写真へ送られたか閉じられたら、届いた情報は捨てる。
	// 取れなかったときは隠したままにする。情報が無くても写真は見られる。
	/**
	 * @param {string} url
	 * @param {number} seq
	 */
	async function showInfo(url, seq) {
		try {
			const res = await fetch(url);
			if (!res.ok) return;
			/** @type {{ owner?: string, takenAt: string }} */
			const info = await res.json();
			if (seq !== requestSeq) return;
			if (info.owner) {
				owner.textContent = info.owner;
				owner.hidden = false;
			}
			when.textContent = formatDateTime(info.takenAt);
			when.hidden = !when.textContent;
		} catch {
			// 通信の失敗。隠したままにする
		}
	}

	function clearInfo() {
		owner.hidden = true;
		owner.textContent = "";
		when.hidden = true;
		when.textContent = "";
	}
```

`close()` の中の

```js
		owner.hidden = true;
		owner.textContent = "";
```

を `clearInfo();` に置き換える。

- [ ] **Step 7: 通ることを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestLightboxShowsWhenItWasTaken|TestLightboxShowsTheOwner|TestScrubberLabelMatchesTheTopItem|TestBigDayTakesWholeRowsAndIsLabelled' ./internal/web/`
Expected: PASS（後ろの2本は一覧の日付ラベルが `datefmt.js` に移っても変わらないことの確認）

Run: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`
Expected: エラーなし

- [ ] **Step 8: 文書を直す**

`docs/web-structure.md` の Client の図:

```
index.html
  +-- app.js (entry)
        +-- gallery.js  --> chunk.js, datefmt.js, layout.js, idiomorph.esm.js
        +-- lightbox.js --> gallery.js, chunk.js, datefmt.js
        +-- scrubber.js --> gallery.js, chunk.js, layout.js
```

- [ ] **Step 9: 全体を確かめてコミットする**

Run: `make browser-test`
Expected: PASS

```bash
git add internal/web/static/datefmt.js internal/web/static/gallery.js internal/web/static/lightbox.js internal/web/templates/index.html internal/web/static/app.css internal/web/browser_test.go docs/web-structure.md
git commit -m "feat: Show the owner and capture time at the bottom left of the lightbox"
```

---

### Task 3: 帯の出し入れと、戻るボタン・前後のボタン

**Files:**
- Modify: `internal/web/templates/index.html`（`chrome` クラス、`.lb-top`、`.lb-back`）
- Modify: `internal/web/static/app.css`（出し入れ、`.lb-top`、ボタン。古い `.lb-nav` 関連を消す）
- Modify: `internal/web/static/lightbox.js`（`setChrome`、`onBoxClick`、`open`、`playing` クラスを消す）
- Modify: `internal/web/browser_test.go`（新しいテスト3本）
- Modify: `docs/design.md`

**Interfaces:**
- Consumes: Task 2 の `startLightboxServer`、`.lb-caption`
- Produces:
  - DOM: `#lightbox` の `chrome` クラス（帯が出ているか）、`#lightbox .lb-top .lb-back`
  - lightbox.js の内部関数 `setChrome(on: boolean)`（Task 4 が中身を足す）
  - `onBoxClick` の振り分けの順: `.lb-back` → `.lb-prev` → `.lb-next` → `video` → それ以外（Task 4 が `video` の分岐を変える）

- [ ] **Step 1: 失敗するテストを書く**

`TestLightboxShowsWhenItWasTaken` の後ろに足す。

```go
// chromeJS は帯の状態を読むJS式。checkVisibility は祖先の display: none も見るので、
// 帯（.lb-top）が隠れていれば中の戻るボタンも見えないと判定される。
const chromeJS = `(() => {
	const b = document.querySelector('#lightbox');
	return {
		chrome: b.classList.contains('chrome'),
		back: b.querySelector('.lb-back').checkVisibility(),
		caption: b.querySelector('.lb-caption').checkVisibility(),
	};
})()`

type chromeState struct {
	Chrome  bool `json:"chrome"`
	Back    bool `json:"back"`
	Caption bool `json:"caption"`
}

// 写真をクリックするたびに帯（戻るボタン、左下の2行）が消えたり出たりすること。
// 帯を消したまま送れば消えたままで、写真をめくり続けられること。
func TestLightboxClickTogglesTheChrome(t *testing.T) {
	requireBrowser(t)

	url, ids := startLightboxServer(t,
		lightboxItem{"a.jpg", 65534, time.Unix(1600000100, 0)},
		lightboxItem{"b.jpg", 65534, time.Unix(1600000000, 0)},
	)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	var got chromeState
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(url),
		waitForGallery(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(chromeJS, &got),
	)
	require.NoError(t, err)
	require.Equal(t, chromeState{Chrome: true, Back: true, Caption: true}, got, "the lightbox should open with the chrome shown")

	// 画面の中央（写真）を押す。前後のボタンは左右の端にあるので当たらない。
	err = chromedp.Run(rctx,
		chromedp.MouseClickXY(600, 450),
		chromedp.Evaluate(chromeJS, &got),
	)
	require.NoError(t, err)
	require.Equal(t, chromeState{}, got, "a click on the photo should hide the chrome")

	err = chromedp.Run(rctx,
		chromedp.KeyEvent(kb.ArrowRight),
		chromedp.Poll(fmt.Sprintf(`location.pathname === %s`, strconv.Quote("/item/"+ids[1])), nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(chromeJS, &got),
	)
	require.NoError(t, err)
	require.Equal(t, chromeState{}, got, "moving to the next photo should keep the chrome hidden")

	err = chromedp.Run(rctx,
		chromedp.MouseClickXY(600, 450),
		chromedp.Evaluate(chromeJS, &got),
	)
	require.NoError(t, err)
	require.Equal(t, chromeState{Chrome: true, Back: true, Caption: true}, got, "a second click should bring the chrome back")
}

// 戻るボタンで閉じ、URLが一覧に戻ること。写真のクリックは閉じる操作ではなくなったので、
// マウスで閉じる経路はこのボタンになる。
func TestLightboxBackButtonCloses(t *testing.T) {
	requireBrowser(t)

	url, _ := startLightboxServer(t, lightboxItem{"a.jpg", 65534, time.Unix(1600000000, 0)})

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	var path string
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(url),
		waitForGallery(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Click(`#lightbox .lb-back`, chromedp.NodeVisible),
		chromedp.Poll(`document.querySelector('#lightbox').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`location.pathname`, &path),
	)
	require.NoError(t, err, "the back button did not close the lightbox")
	require.Equal(t, "/", path)
}

// マウスで操作する端末では前後のボタンが見えていて、押すと送れること。
// タッチ端末で出さないことは、ヘッドレスChromeが (hover: hover) を名乗るので
// ここでは確かめられない。
func TestLightboxNavButtonsOnPointerDevices(t *testing.T) {
	requireBrowser(t)

	url, ids := startLightboxServer(t,
		lightboxItem{"a.jpg", 65534, time.Unix(1600000100, 0)},
		lightboxItem{"b.jpg", 65534, time.Unix(1600000000, 0)},
	)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	var hover, visible bool
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(url),
		waitForGallery(10*time.Second),
		chromedp.Evaluate(`matchMedia('(hover: hover)').matches`, &hover),
	)
	require.NoError(t, err)
	require.True(t, hover, "the browser does not report (hover: hover), so the premise of this test does not hold")

	err = chromedp.Run(rctx,
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(`document.querySelector('#lightbox .lb-next').checkVisibility()`, &visible),
	)
	require.NoError(t, err)
	require.True(t, visible, "the next button is not shown")

	err = chromedp.Run(rctx,
		chromedp.Click(`#lightbox .lb-next`, chromedp.NodeVisible),
		chromedp.Poll(fmt.Sprintf(`location.pathname === %s`, strconv.Quote("/item/"+ids[1])), nil,
			chromedp.WithPollingTimeout(5*time.Second)),
	)
	require.NoError(t, err, "the next button did not move to the next photo")
}
```

- [ ] **Step 2: 失敗を確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestLightboxClickTogglesTheChrome|TestLightboxBackButtonCloses|TestLightboxNavButtonsOnPointerDevices' ./internal/web/`
Expected: 3本とも FAIL（`chrome` クラスも `.lb-back` も無い。前後のボタンは今は透明な領域なので、`checkVisibility()` は true でも、`TestLightboxNavButtonsOnPointerDevices` は前提か押下のどこかで落ちうる。落ちなくてもよい）

- [ ] **Step 3: HTML に帯を足す**

`internal/web/templates/index.html` の lightbox の開始タグに `chrome` を足し、`.lb-error` の後ろ（`.lb-caption` の前）に上の帯を置く。

```html
<div id="lightbox" class="lightbox chrome" hidden>
```

```html
  {{/* 上の帯。ボタンは左端の戻るだけ。お気に入り・情報・メニューは機能ができたら
       右端に足す。 */}}
  <div class="lb-top">
    <button class="lb-back" type="button" aria-label="戻る">←</button>
  </div>
```

- [ ] **Step 4: CSS を書く**

`internal/web/static/app.css` の `.lb-nav` から `@media (hover: hover) { .lb-nav:hover … }` まで（`.lb-nav`、`.lb-prev`、`.lb-next`、`.lightbox.playing .lb-nav` とそのコメント、`.lb-nav:hover` の media ブロック）を消し、`.lb-caption` の前に次を置く。

```css
/* 帯（上のボタン、左下の2行、前後のボタン）。写真のタップで出し入れする。 */
.lightbox:not(.chrome) :is(.lb-top, .lb-caption, .lb-nav) {
	display: none;
}

/* 上の帯。帯そのものは押せず、空いた所を押すと下の写真に届く。ボタンだけを押せる。 */
.lb-top {
	position: absolute;
	top: 0;
	left: 0;
	right: 0;
	display: flex;
	padding: max(12px, env(safe-area-inset-top)) max(12px, env(safe-area-inset-right)) 0
		max(12px, env(safe-area-inset-left));
	pointer-events: none;
}
.lb-top button {
	pointer-events: auto;
}

/* 帯のボタン。明るい写真の上でも見えるよう、丸い半透明の背景を付ける。 */
.lb-back,
.lb-nav {
	width: 44px;
	height: 44px;
	padding: 0;
	border: 0;
	border-radius: 50%;
	background: rgba(0, 0, 0, 0.5);
	color: rgba(255, 255, 255, 0.9);
	font-size: 1.5rem;
	line-height: 44px;
	cursor: pointer;
}

/* 前後のボタンは PC だけに出す。タッチ端末は左右スワイプで送り、写真のどこを
   タップしても帯の出し入れになる。 */
.lb-nav {
	position: absolute;
	top: 50%;
	transform: translateY(-50%);
	display: none;
}
@media (hover: hover) {
	.lb-nav {
		display: block;
	}
}
.lb-prev {
	left: max(12px, env(safe-area-inset-left));
}
.lb-next {
	right: max(12px, env(safe-area-inset-right));
}
```

- [ ] **Step 5: lightbox.js でクリックを振り分ける**

`onBoxClick` を置き換える。

```js
	/** @param {MouseEvent} e */
	function onBoxClick(e) {
		const target = /** @type {Element} */ (e.target);
		if (target.closest(".lb-back")) {
			requestClose();
			return;
		}
		if (target.closest(".lb-prev")) {
			show(idx - 1, "replace");
			return;
		}
		if (target.closest(".lb-next")) {
			show(idx + 1, "replace");
			return;
		}
		// 再生コントロールの操作を、帯の出し入れと取り違えない
		if (target.closest("video")) return;
		setChrome(!box.classList.contains("chrome"));
	}
```

`open()` で帯を出してから見せる。

```js
	async function open(i, mode) {
		await show(i, mode);
		if (idx < 0) return;
		// 開くときは必ず帯を出す。送りでは前の状態のままにする。
		setChrome(true);
		box.hidden = false;
		document.body.classList.add("locked");
	}
```

`stopVideo` の後ろに `setChrome` を置く。

```js
	// setChrome は帯（上のボタン、左下の2行、前後のボタン）を出すか消すかを決める。
	/** @param {boolean} on */
	function setChrome(on) {
		box.classList.toggle("chrome", on);
	}
```

`playing` クラスは使う CSS が無くなったので、`show()` の `box.classList.add("playing");` と `box.classList.remove("playing");`、`close()` の `box.classList.remove("playing");` を消す。

ファイル冒頭のコメントにある「閉じる操作は自分で閉じずに history.back() を呼び」は変えない。

- [ ] **Step 6: 通ることを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestLightboxClickTogglesTheChrome|TestLightboxBackButtonCloses|TestLightboxNavButtonsOnPointerDevices' ./internal/web/`
Expected: PASS

Run: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`
Expected: エラーなし

- [ ] **Step 7: 文書を直す**

`docs/design.md` の「写真ごとのURL」の項:

```
  - 閉じる操作（戻るボタン・下スワイプ・Esc）は自分で閉じず `history.back()` を
```

- [ ] **Step 8: 全体を確かめてコミットする**

Run: `make browser-test`
Expected: PASS

```bash
git add internal/web/templates/index.html internal/web/static/app.css internal/web/static/lightbox.js internal/web/browser_test.go docs/design.md
git commit -m "feat: Toggle the lightbox chrome on tap and add a back button"
```

---

### Task 4: 動画の標準コントロールを帯に連動させる

**Files:**
- Modify: `internal/web/templates/index.html`（`<video>` の `controls` を外す）
- Modify: `internal/web/static/app.css`（`.lb-caption.raised`）
- Modify: `internal/web/static/lightbox.js`（`setChrome`、`placeCaption`、`showInfo`、`onBoxClick`、`ResizeObserver`）
- Modify: `internal/web/browser_test.go`（新しいテスト2本）

**Interfaces:**
- Consumes: Task 3 の `setChrome`、`onBoxClick`。Task 2 の `showInfo`、`.lb-caption`、`startLightboxServer`
- Produces: `.lb-caption.raised`（標準コントロールと重なるときに付く）

- [ ] **Step 1: 失敗するテストを書く**

`TestLightboxNavButtonsOnPointerDevices` の後ろに足す。

```go
// 動画の標準コントロールが帯と一緒に出入りすること。帯を消すと controls が外れ、
// 動画を押すと帯が戻って controls も付く。controls が付いている間の動画のクリックは
// ブラウザに任せるので、ここでは押さない。
func TestVideoControlsFollowTheChrome(t *testing.T) {
	requireBrowser(t)

	url, _ := startLightboxServer(t, lightboxItem{"clip.mp4", 65534, time.Unix(1600000000, 0)})

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	const controlsJS = `document.querySelector('#lightbox video').controls`
	var controls bool
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(url),
		waitForGallery(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox video').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(controlsJS, &controls),
	)
	require.NoError(t, err)
	require.True(t, controls, "the video should open with its controls")

	// 中身の無い動画は 300×150 で中央に出る。上寄りの黒い余白を押す。戻るボタンは左上の隅にある。
	err = chromedp.Run(rctx,
		chromedp.MouseClickXY(600, 100),
		chromedp.Evaluate(controlsJS, &controls),
	)
	require.NoError(t, err)
	require.False(t, controls, "hiding the chrome should hide the controls")

	var chrome bool
	err = chromedp.Run(rctx,
		chromedp.MouseClickXY(600, 450),
		chromedp.Evaluate(controlsJS, &controls),
		chromedp.Evaluate(`document.querySelector('#lightbox').classList.contains('chrome')`, &chrome),
	)
	require.NoError(t, err)
	require.True(t, chrome, "a click on the video without controls should bring the chrome back")
	require.True(t, controls, "the controls should come back with the chrome")
}

// 左下の2行は、動画の標準コントロールと重なるときだけ上がること。コントロールは
// 画面の下端ではなく動画の下端に描かれるので、動画が中ほどで終わる画面では
// 重ならず、上げると浮いて見える。
//
// 中身の無い動画は 300×150 で出る。1200×900 では中央で終わり、300×150 では
// 画面いっぱいになって下端まで届く。画面の大きさを変えたときに判定し直すことも、
// あわせて確かめる。
func TestCaptionRisesOnlyOverVideoControls(t *testing.T) {
	requireBrowser(t)

	url, _ := startLightboxServer(t, lightboxItem{"clip.mp4", 65534, time.Unix(1600000000, 0)})

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	const raisedJS = `document.querySelector('#lightbox .lb-caption').classList.contains('raised')`
	var raised bool
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(url),
		waitForGallery(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox .lb-when').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(raisedJS, &raised),
	)
	require.NoError(t, err)
	require.False(t, raised, "the caption rose although the video ends far above it")

	err = chromedp.Run(rctx,
		chromedp.EmulateViewport(300, 150),
		chromedp.Poll(raisedJS, nil, chromedp.WithPollingTimeout(5*time.Second)),
	)
	require.NoError(t, err, "the caption did not rise when the video reached the bottom edge")
}
```

- [ ] **Step 2: 失敗を確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestVideoControlsFollowTheChrome|TestCaptionRisesOnlyOverVideoControls' ./internal/web/`
Expected: 2本とも FAIL（`controls` は帯を消しても付いたまま。`raised` は付かない）

- [ ] **Step 3: HTML と CSS**

`internal/web/templates/index.html` の `<video>` から `controls` を外し、直前のコメントに1文足す。

```html
  {{/* 自動再生はしない。一覧をめくっていて突然音が鳴るのを避ける。
       playsinline は iOS Safari が全画面を乗っ取らないようにするため。
       controls は帯と一緒に lightbox.js が付け外しする。 */}}
  <video playsinline preload="metadata" hidden></video>
```

`internal/web/static/app.css` の `.lb-caption p` の後ろに足す。

```css
/* 動画の標準コントロールと重なるときだけ上げる。4rem は lightbox.js の
   CONTROLS_H と揃える。 */
.lb-caption.raised {
	bottom: calc(4rem + env(safe-area-inset-bottom));
}
```

- [ ] **Step 4: lightbox.js**

`when` の宣言の後ろに足す。

```js
	/** @type {HTMLElement} */
	const caption = box.querySelector(".lb-caption");
	// 標準コントロールの高さの目安(px)。app.css の .lb-caption.raised の 4rem と揃える。
	// 実際の高さはブラウザによって少し違う。
	const CONTROLS_H = 64;
```

`setChrome` を置き換え、その後ろに `placeCaption` を置く。

```js
	// setChrome は帯（上のボタン、左下の2行、前後のボタン）を出すか消すかを決める。
	// 動画の標準コントロールも帯の一部として一緒に出し入れする。
	/** @param {boolean} on */
	function setChrome(on) {
		box.classList.toggle("chrome", on);
		vid.controls = on;
		placeCaption();
	}

	// placeCaption は左下の2行が動画の標準コントロールと重なるときだけ上げる。
	// コントロールは画面の下端ではなく動画の下端に描かれる。横長の動画を縦の画面で
	// 見ると動画は中ほどで終わり、文字とは重ならない。常に上げると浮いて見える。
	function placeCaption() {
		caption.classList.remove("raised");
		if (vid.hidden || !vid.controls) return;
		const c = caption.getBoundingClientRect();
		const v = vid.getBoundingClientRect();
		const hit = c.bottom > v.bottom - CONTROLS_H && c.top < v.bottom
			&& c.left < v.right && c.right > v.left;
		caption.classList.toggle("raised", hit);
	}
```

`showInfo` の中で、2行を入れた後（`when.hidden = !when.textContent;` の次の行）に足す。

```js
			placeCaption();
```

`onBoxClick` の動画の分岐を置き換える。

```js
		// controls があるときはブラウザに任せる（PCは再生/一時停止）。無いときは
		// 帯を消しているので、出す。
		if (target.closest("video")) {
			if (!vid.controls) setChrome(true);
			return;
		}
```

イベントの登録（`box.addEventListener("touchend", …)` の後ろ）に足す。

```js
	// 動画の大きさはメタデータが届いてから決まり、画面を回すと lightbox の大きさが
	// 変わる。どちらでも重なりが変わるので判定し直す。
	const resize = new ResizeObserver(placeCaption);
	resize.observe(box);
	resize.observe(vid);
```

- [ ] **Step 5: 通ることを確かめる**

Run: `FAMIFO_BROWSER_TESTS=required go test -tags browser -run 'TestVideoControlsFollowTheChrome|TestCaptionRisesOnlyOverVideoControls|TestLightboxSwitchesBetweenImageAndVideo' ./internal/web/`
Expected: PASS

Run: `npx --yes -p "typescript@$(jq -r .devDependencies.typescript package.json)" tsc -p jsconfig.json`
Expected: エラーなし

- [ ] **Step 6: 全体を確かめてコミットする**

Run: `make unit-test-race && make browser-test`
Expected: PASS

```bash
git add internal/web/templates/index.html internal/web/static/app.css internal/web/static/lightbox.js internal/web/browser_test.go
git commit -m "feat: Tie the video controls to the lightbox chrome"
```
