# ブラウザテスト強化 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `internal/web/browser_test.go` の8テストのうち、退行を注入しても FAIL しない5本に検出力を与え、Docker 不在時に同パッケージの非ブラウザテスト25本が静かに消える問題を止める。

**Architecture:** 新しいテストは足さない。既存テストのアサーションを、実装が壊れたときに落ちるものへ置き換える。各タスクは「テストを直す → **意図的に app.js / app.css を壊して FAIL することを実測する** → 元に戻して PASS することを実測する → コミット」の順で進む。ミューテーション検証を通らない修正は完成とみなさない。

**Tech Stack:** Go 1.25, chromedp v0.14.2, chromedp/headless-shell (Docker), testify

**Spec:** `docs/superpowers/specs/2026-08-25-browser-test-review-findings.md`

## Global Constraints

- 応答・コメント・エラーメッセージはすべて**日本語**で書く。
- `CGO_ENABLED=0` を維持する。`-race` は使えない（cgo が要るため）。
- **ホストの Chrome は絶対に起動しない。** `chromedp.NewContext` を単独で使わず、必ず既存の `allocCtx`（`chromedp.NewRemoteAllocator` 経由）を親にする。新しいタブは既存の `newTab(t)` ヘルパで作る。
- `internal/web/static/app.js` と `app.css` は**製品コードであり、このタスク群では恒久的に変更しない**。ミューテーション検証で一時的に壊すのは構わないが、各タスクのコミット前に必ず `git checkout -- internal/web/static/` で戻し、`git status --short` が `?? .claude/` のみであることを確認する。
- 既存の5つの固定依存（go.mod）に手を触れない。新しい依存を追加しない。
- テストの実行コマンドは `CGO_ENABLED=0 go test -tags browser ./internal/web/ -run '<名前>' -v -count=1`。
- ミューテーション検証の結果が期待と違ったら、**テストを緩めずに実装者へ報告して止まる**。「落ちないので閾値を下げる」は禁止。

## 現状の把握（実装者向け前提知識）

- `internal/web/templates/gallery.html` は、サーバが最初の `pageSize`（テストでは `testPageSize`=60）枚のタイルを `#window` の**中に直接描画して返す**。つまり JS が一切動かなくても `#window .tile` は 60 枚存在する。
- `internal/web/static/app.js` は3つの IIFE（`famifo` = 仮想スクロール、lightbox、scrubber）。`famifo` は末尾で `{total, chunkSize, urlAt, ensureChunk, scroller, maxScroll, render, pastedIndex}` を返し `window.famifo` になる。
- `seedCorpus` は `p0000.jpg`〜`p0199.jpg` を作り、`takenAt = base - i日` を与える。ギャラリーは新しい順なので、**通し番号 i がそのまま並び順**になる。
- 写真の URL は `handlers.go` の `buildRange` が `"/photo/" + p.ID` と組み立てる。`p.ID` は `store.IDFor(path)`。
- タイルは `<a class="tile" href="..." data-full="..."><img src="..." loading="lazy"></a>`。
- `.scrubber` は `width: 32px; right: 0` で、`opacity:0; pointer-events:none`。`.visible` が付くと `pointer-events:auto`。

---

### Task 1: Docker 不在時に非ブラウザテストを巻き込まない

**Files:**
- Modify: `internal/web/browser_test.go:67-142`（`TestMain` と `setupBrowserEnv`）
- Modify: `README.md`（ブラウザテストの実行方法の節）

**Interfaces:**
- Produces: `requireBrowser(t *testing.T)` — 以降の全タスクが各テストの冒頭で呼ぶ。ブラウザ環境が無ければそのテストだけを `t.Skip()` する。
- Produces: パッケージ変数 `browserReady bool` と `browserSkipReason string`。

- [ ] **Step 1: 現状を再現して記録する**

まず壊れていることを自分の目で見る。

```bash
CGO_ENABLED=0 go test -tags browser -c -o /tmp/web.test ./internal/web/
PATH=/nonexistent /tmp/web.test -test.v -test.count=1; echo "EXIT=$?"
```

期待（＝現状のバグ）: `SKIP: dockerが見つからない...` の1行だけが出て `EXIT=0`。
`=== RUN` が**1行も出ない**ことを確認する。`grep -c '=== RUN'` が 0 になるはず。

- [ ] **Step 2: パッケージ変数と requireBrowser を追加する**

`var allocCtx context.Context` の宣言の隣に足す。

```go
// browserReady はブラウザ環境（Docker上のheadless-shellとテスト用アプリ）を
// 用意できたか。TestMain が設定し、requireBrowser が読む。
var browserReady bool

// browserSkipReason は用意できなかった理由。requireBrowser が表示する。
var browserSkipReason string
```

そして `waitForDebugger` の直前あたりに:

```go
// requireBrowser はブラウザ環境が無ければ、このテストだけをスキップする。
//
// 判定を TestMain で行って os.Exit(0) すると、ブラウザテストだけでなく
// 同じパッケージの非ブラウザテスト（gallery_test.go / handlers_test.go /
// static_test.go の計25本）まで実行されないまま ok と表示されてしまう。
// CIが緑になるので誰も気づかない。判定は必ず各テストで行う。
//
// CI では FAMIFO_BROWSER_TESTS=required を立てる。ブラウザテストが
// 環境の都合で黙って消えることを防ぐため、スキップを失敗として扱う。
func requireBrowser(t *testing.T) {
	t.Helper()
	if browserReady {
		return
	}
	if os.Getenv("FAMIFO_BROWSER_TESTS") == "required" {
		t.Fatalf("ブラウザテスト環境を用意できませんでした（FAMIFO_BROWSER_TESTS=required のためスキップせず失敗させます）: %s", browserSkipReason)
	}
	t.Skipf("ブラウザテスト環境が無いためスキップします: %s", browserSkipReason)
}
```

- [ ] **Step 3: TestMain から os.Exit(0) を取り除く**

現在の `TestMain` の前半を差し替える。`m.Run()` は**常に**呼ばれるようにする。

```go
func TestMain(m *testing.M) {
	cleanup, ready := setupBrowserEnv()
	browserReady = ready

	code := 1
	func() {
		// パニックが起きてもコンテナと一時ディレクトリの後始末は必ず行う。
		// ready が false でも cleanup は安全な no-op / 部分片付けになっている。
		defer cleanup()
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintln(os.Stderr, "browser_test: パニックを捕捉しました:", r)
				code = 1
			}
		}()
		code = m.Run()
	}()
	os.Exit(code)
}
```

- [ ] **Step 4: setupBrowserEnv の失敗理由を変数に載せる**

`fmt.Println("SKIP: ...")` を全て `browserSkipReason` への代入に置き換える。5箇所ある。

```go
func setupBrowserEnv() (cleanup func(), ok bool) {
	noop := func() {}

	if _, err := exec.LookPath("docker"); err != nil {
		browserSkipReason = fmt.Sprintf("dockerが見つかりません: %v", err)
		return noop, false
	}

	if err := exec.Command("docker", "image", "inspect", dockerImage).Run(); err != nil {
		browserSkipReason = fmt.Sprintf("イメージ %s がありません（`docker pull %s` してから再実行してください）", dockerImage, dockerImage)
		return noop, false
	}

	// クラッシュした前回実行の残骸があれば先に片付ける。
	_ = exec.Command("docker", "rm", "-f", containerName).Run()

	runOut, err := exec.Command("docker", "run", "-d", "--rm", "--network", "host",
		"--name", containerName, dockerImage).CombinedOutput()
	if err != nil {
		browserSkipReason = fmt.Sprintf("docker run に失敗しました: %v: %s", err, runOut)
		return noop, false
	}
	stopContainer := func() { _ = exec.Command("docker", "stop", containerName).Run() }

	if !waitForDebugger(20 * time.Second) {
		browserSkipReason = "headless-shellの起動待ちがタイムアウトしました"
		stopContainer()
		return noop, false
	}

	tempDir, srv, closeStore, err := startTestApp()
	if err != nil {
		browserSkipReason = fmt.Sprintf("テスト用アプリの起動に失敗しました: %v", err)
		stopContainer()
		return noop, false
	}

	ctx, cancelAlloc := chromedp.NewRemoteAllocator(context.Background(), "http://127.0.0.1:9222/")
	allocCtx = ctx
	baseURL = srv.URL

	cleanup = func() {
		cancelAlloc()
		srv.Close()
		closeStore()
		_ = os.RemoveAll(tempDir)
		stopContainer()
	}
	return cleanup, true
}
```

- [ ] **Step 5: 8テストすべての冒頭に requireBrowser(t) を足す**

各 `func TestXxx(t *testing.T) {` の直後の行に `requireBrowser(t)` を入れる。対象は8本:
`TestGridAlignmentPastFirstChunk`, `TestInitialRenderFillsViewport`,
`TestScrollPositionSurvivesReload`, `TestScrollAnchoredOnResize`,
`TestNoRepaintOnPlainScroll`, `TestLightboxCrossesChunkBoundary`,
`TestScrubberReachesBothEnds`, `TestTileTapOpensLightbox`。

漏れがないことを確認する:

```bash
grep -c 'requireBrowser(t)' internal/web/browser_test.go   # 8 + 定義1 = 9
```

- [ ] **Step 6: Docker 不在で非ブラウザテストが走ることを実測する**

```bash
CGO_ENABLED=0 go test -tags browser -c -o /tmp/web.test ./internal/web/
PATH=/nonexistent /tmp/web.test -test.v -test.count=1 2>&1 | tee /tmp/out.txt | tail -3
echo "EXIT=$?"
grep -c '=== RUN' /tmp/out.txt
grep -c -- '--- SKIP' /tmp/out.txt
grep -c -- '--- PASS' /tmp/out.txt
```

期待: `=== RUN` が 33 行、`--- SKIP` が 8 行、`--- PASS` が 25 行、EXIT=0。

- [ ] **Step 7: required モードが失敗することを実測する**

```bash
PATH=/nonexistent FAMIFO_BROWSER_TESTS=required /tmp/web.test -test.v -test.count=1 2>&1 | tail -5
echo "EXIT=$?"
```

期待: 8本が `--- FAIL`、25本は `--- PASS`、EXIT=1。
FAIL のメッセージに `dockerが見つかりません` が含まれること。

- [ ] **Step 8: Docker がある通常実行で 8/8 通ることを確認する**

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -v -count=1 2>&1 | grep -E '^(--- |ok|FAIL)'
```

期待: 33本すべて `--- PASS`。SKIP が1本も無いこと。

- [ ] **Step 9: タグ無しの通常実行に影響が無いことを確認する**

```bash
CGO_ENABLED=0 go test ./... 2>&1 | tail -10
CGO_ENABLED=0 go vet ./... && gofmt -l .
```

期待: 全パッケージ ok、vet 無言、`gofmt -l` の出力が空。

- [ ] **Step 10: README を更新する**

ブラウザテストの節に、CI での使い方を1〜2行足す。

```markdown
CI など、環境が無いことを黙認したくない場面では `FAMIFO_BROWSER_TESTS=required`
を立てる。Docker やイメージが用意できていない場合、スキップではなく失敗になる。
```

- [ ] **Step 11: コミット**

```bash
git add internal/web/browser_test.go README.md
git commit -m "test: skip browser tests per-test instead of exiting TestMain

Without Docker, TestMain exited before m.Run(), so the package's 25
non-browser tests never ran and the package still reported ok.

Moves the environment check into requireBrowser(t). FAMIFO_BROWSER_TESTS=required
turns the skip into a failure for CI."
```

---

### Task 2: TestInitialRenderFillsViewport に検出力を与える

**Files:**
- Modify: `internal/web/browser_test.go`（`TestInitialRenderFillsViewport`)

**Interfaces:**
- Consumes: `requireBrowser(t)`（Task 1）
- Produces: なし

**このタスクが潰す欠陥（spec F2, F7）:** 現在のテストは `#window` 内の最後のタイルの下端がビューポート高を超えることだけを見る。サーバが最初の60枚を HTML に埋め込むため、**app.js を丸ごと無効化しても PASS する**。しかも余裕は 77px しかなく、正しい実装のまま壊れやすい。

- [ ] **Step 1: 先に壊して、現在のテストが素通りすることを実測する**

`app.js` の全体を無効化する。

```bash
cp internal/web/static/app.js /tmp/app.js.bak
{ echo 'if (true) { /* MUTATION */ } else {'; cat /tmp/app.js.bak; echo '}'; } > internal/web/static/app.js
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestInitialRenderFillsViewport -v -count=1 2>&1 | tail -4
```

期待（＝現状のバグ）: **PASS**。これを確認してから直す。

`app.js` を戻す: `cp /tmp/app.js.bak internal/web/static/app.js`

- [ ] **Step 2: テストを書き換える**

JS が動いたことに依存する量（貼り付けられたタイル枚数）と、実際にデコードされた画像の枚数を見る。マージンは行高から導出する。

```go
// --- Task: TestInitialRenderFillsViewport ---
//
// 読み込み直後に、可視範囲が写真で埋まっているかを見る。
//
// 注意: サーバは gallery.html の中で最初の1塊（testPageSize枚）を #window に
// 直接描画して返す。そのため「タイルが存在する」「下端がビューポートを超える」
// だけを見ると、app.js が一切動かなくても通ってしまう。仮想スクロールが
// 実際に働いて塊を追加で貼ったこと（tileCount > testPageSize）まで見る。
func TestInitialRenderFillsViewport(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// 可視範囲の先頭行のサムネイルがデコードされるまで待つ。
	// タイルの寸法は CSS の aspect-ratio で決まるため、srcが404でも
	// レイアウトは成立してしまう。naturalWidth で実際の描画を確かめる。
	const decodedInViewJS = `(() => {
		const vh = window.innerHeight;
		return [...document.querySelectorAll('#window .tile img')].filter((img) => {
			const r = img.getBoundingClientRect();
			return r.bottom > 0 && r.top < vh && img.naturalWidth > 0;
		}).length;
	})()`

	var res struct {
		LastBottom float64 `json:"lastBottom"`
		ViewportH  float64 `json:"viewportH"`
		RowH       float64 `json:"rowH"`
		Cols       int     `json:"cols"`
		TileCount  int     `json:"tileCount"`
	}
	measureJS := `(() => {
		const win = document.querySelector('#window');
		const tiles = [...win.querySelectorAll('.tile')];
		const r = tiles[0].getBoundingClientRect();
		const gap = parseFloat(getComputedStyle(win).rowGap) || 0;
		return {
			lastBottom: tiles[tiles.length - 1].getBoundingClientRect().bottom,
			viewportH: window.innerHeight,
			rowH: r.height + gap,
			cols: getComputedStyle(win).gridTemplateColumns.split(' ').filter(Boolean).length,
			tileCount: tiles.length,
		};
	})()`

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 2000),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
	)
	require.NoError(t, err)

	// 仮想スクロールが初回の描画を終えるまで待つ。固定sleepにしない。
	// Poll のエラーをそのまま require.NoError に渡すと、失敗時のメッセージが
	// "waiting for function failed: timeout" だけになり、何枚あったのかが
	// 分からない。エラーは受け取っておき、実測してから報告する。
	pollErr := chromedp.Run(rctx, chromedp.Poll(
		fmt.Sprintf(`document.querySelectorAll('#window .tile').length > %d`, testPageSize),
		nil, chromedp.WithPollingTimeout(10*time.Second)))

	err = chromedp.Run(rctx, chromedp.Evaluate(measureJS, &res))
	require.NoError(t, err)
	require.NotZero(t, res.Cols, "列数を取得できなかった")
	require.Greater(t, res.RowH, 0.0, "行の高さを取得できなかった")

	// app.js が動いていなければ、#window にはサーバが埋めた1塊しか無い。
	require.NoErrorf(t, pollErr,
		"#windowのタイルが%d枚のまま増えない。サーバが埋めた最初の1塊(%d枚)のままで、"+
			"仮想スクロールが追加の塊を貼っていない（app.jsが動いていない疑い）",
		res.TileCount, testPageSize)
	require.Greater(t, res.TileCount, testPageSize)

	// 「ぎりぎり超えている」を成功とみなすと、ビューポート高やCSSの変更で
	// 正しい実装のまま落ちる。1行分の余裕を要求する。
	require.GreaterOrEqualf(t, res.LastBottom, res.ViewportH+res.RowH,
		"読み込み直後、最後のタイルの下端(%.0f)がビューポート高(%.0f)+1行(%.0f)に届いていない（空白帯がある）",
		res.LastBottom, res.ViewportH, res.RowH)

	var decoded int
	err = chromedp.Run(rctx,
		chromedp.Poll(fmt.Sprintf(`%s >= %d`, decodedInViewJS, res.Cols), nil,
			chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(decodedInViewJS, &decoded),
	)
	require.NoErrorf(t, err, "可視範囲のサムネイルが1行分(%d枚)もデコードされなかった（画像が表示されていない）", res.Cols)
	require.GreaterOrEqual(t, decoded, res.Cols)
}
```

- [ ] **Step 3: 直したテストが正しい実装で通ることを確認する**

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestInitialRenderFillsViewport -v -count=1 2>&1 | tail -4
```

期待: PASS。

- [ ] **Step 4: ミューテーション検証① app.js 全体を無効化**

```bash
cp internal/web/static/app.js /tmp/app.js.bak
{ echo 'if (true) { /* MUTATION */ } else {'; cat /tmp/app.js.bak; echo '}'; } > internal/web/static/app.js
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestInitialRenderFillsViewport -v -count=1 2>&1 | tail -8
cp /tmp/app.js.bak internal/web/static/app.js
```

期待: **FAIL**。メッセージに「仮想スクロールが追加の塊を貼っていない」が含まれること。

- [ ] **Step 5: ミューテーション検証② サムネイルを全て 404 にする**

`internal/web/templates/items.html` の `src="{{.ThumbURL}}"` を `src="/thumb/does-not-exist"` に一時的に書き換える。

```bash
cp internal/web/templates/items.html /tmp/items.html.bak
sed -i 's|src="{{.ThumbURL}}"|src="/thumb/does-not-exist"|' internal/web/templates/items.html
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestInitialRenderFillsViewport -v -count=1 2>&1 | tail -8
cp /tmp/items.html.bak internal/web/templates/items.html
```

期待: **FAIL**。メッセージに「デコードされなかった」が含まれること。

- [ ] **Step 6: 作業ツリーが元に戻っていることを確認してコミット**

```bash
git status --short   # ?? .claude/ 以外に internal/web/browser_test.go の M だけ
git diff --stat -- internal/web/static internal/web/templates   # 空であること
CGO_ENABLED=0 go vet ./... && gofmt -l .
git add internal/web/browser_test.go
git commit -m "test: assert the viewport test depends on app.js running

The server renders the first chunk into #window itself, so the old
assertion passed with JavaScript disabled entirely.

Requires more tiles than one chunk, a full row of margin, and thumbnails
that actually decoded."
```

---

### Task 3: TestNoRepaintOnPlainScroll を貼り替え回数の計測に変える

**Files:**
- Modify: `internal/web/browser_test.go`（`TestNoRepaintOnPlainScroll`)

**Interfaces:**
- Consumes: `requireBrowser(t)`（Task 1）
- Produces: なし

**このタスクが潰す欠陥（spec F3）:** `onResize` の no-change ガード（app.js:149-151）を削除しても PASS する。**このテストが存在する唯一の理由であるバグを検出できていない。** 原因は観測点で、印付けが塊境界の跨ぎが落ち着いた後に行われ、その後の `scrollTop += 40`（1行 226px に対して）は貼り付け範囲を変えないため ResizeObserver のループの入口に届かない。3つの固定 Sleep は何も起きないのを待っているだけ。

**方針:** 印が残るかではなく、`#window` の子要素が何回作り直されたかを `MutationObserver` で数える。観測はスクロールを**始める前**に仕掛け、塊境界の跨ぎそのものを観測範囲に入れる。app.js（製品コード）には手を入れない。

- [ ] **Step 1: 先に壊して、現在のテストが素通りすることを実測する**

```bash
cp internal/web/static/app.js /tmp/app.js.bak
grep -n 'cols === prevCols && rowH === prevRowH' internal/web/static/app.js   # 149 のはず
sed -i '149,151d' internal/web/static/app.js
sed -n '145,152p' internal/web/static/app.js   # ガードが消えたことを目視
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestNoRepaintOnPlainScroll -v -count=1 2>&1 | tail -4
```

期待（＝現状のバグ）: **PASS**。

`app.js` は**戻さずそのまま次へ**（Step 3 で計測に使う）。

- [ ] **Step 2: 計測用のテストを書く（まず閾値を決めずに数だけ出す）**

```go
// --- Task: TestNoRepaintOnPlainScroll ---
//
// #window はグリッドの行数が貼り付け枚数で変わるため、塊境界を跨ぐたびに
// ResizeObserver が発火する。以前はこれを無条件に render() へつなげており、
// 列数もタイル高も変わっていないのに DOM を丸ごと張り替えていた
// （画像の再読み込み・スクロールのカクつきの原因）。
//
// 「貼り付け済みのタイルに印を付け、スクロール後も残っているか」では
// この不具合を検出できない。印を付けられるのは貼り替えが落ち着いた後で、
// その後の小さなスクロールは貼り付け範囲を変えないため、
// ResizeObserver のフィードバックループの入口に届かないから（実測）。
// ここでは MutationObserver で #window の作り直し回数そのものを数える。
func TestNoRepaintOnPlainScroll(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// スクロールを始める前に観測を仕掛ける。塊境界を跨ぐ貼り替えそのものを
	// 数えたいので、跨いだ後に仕掛けるのでは遅い。
	const installObserverJS = `(() => {
		window.__repaints = 0;
		const win = document.querySelector('#window');
		new MutationObserver((records) => {
			for (const r of records) {
				// render() は innerHTML を差し替えるので、必ず removedNodes を伴う。
				// タイルの追加のみ（あり得ないが）を貼り替えと数えないため区別する。
				if (r.removedNodes.length > 0) { window.__repaints++; break; }
			}
		}).observe(win, { childList: true });
	})()`

	// スクロールが落ち着いたか（一定時間 __repaints が増えていないか）を見る。
	// 固定 sleep で「もう終わっただろう」と決め打ちしない。
	const settledJS = `(() => {
		const now = window.__repaints;
		if (window.__lastSeen === now) { return (window.__stable = (window.__stable || 0) + 1) >= 3; }
		window.__lastSeen = now;
		window.__stable = 0;
		return false;
	})()`

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(installObserverJS, nil),
		// 塊境界を2つ跨ぐところまでスクロールする。
		chromedp.Evaluate(scrollToPhotoJS(testPageSize*2+30), nil),
		chromedp.Poll(fmt.Sprintf(`famifo.pastedIndex() >= %d`, testPageSize), nil,
			chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Poll(settledJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err)

	var afterScroll int
	err = chromedp.Run(rctx, chromedp.Evaluate(`window.__repaints`, &afterScroll))
	require.NoError(t, err)
	t.Logf("PROBE: 塊境界を跨いだ後の貼り替え回数=%d", afterScroll)

	// 同じ貼り付け範囲内にとどまる、ごく普通のスクロールを1行分行う。
	const scrollOneRowJS = `(() => {
		const win = document.querySelector('#window');
		const r = win.querySelector('.tile').getBoundingClientRect();
		const gap = parseFloat(getComputedStyle(win).rowGap) || 0;
		famifo.scroller.scrollTop += r.height + gap;
	})()`
	err = chromedp.Run(rctx,
		chromedp.Evaluate(`window.__lastSeen = -1; window.__stable = 0;`, nil),
		chromedp.Evaluate(scrollOneRowJS, nil),
		chromedp.Poll(settledJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err)

	var afterPlain int
	err = chromedp.Run(rctx, chromedp.Evaluate(`window.__repaints`, &afterPlain))
	require.NoError(t, err)
	t.Logf("PROBE: 通常スクロール後の貼り替え回数=%d", afterPlain)

	// TODO(Step 4で確定): 閾値を入れる
}
```

- [ ] **Step 3: 壊れた実装での回数を測る（app.js はまだガード無しのまま）**

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestNoRepaintOnPlainScroll -v -count=1 2>&1 | grep PROBE
```

出た2つの数字を書き留める。ここでは**必ず PASS する**（まだ閾値が無いため）。

- [ ] **Step 4: 正しい実装での回数を測る**

```bash
cp /tmp/app.js.bak internal/web/static/app.js
git diff --stat -- internal/web/static   # 空であること
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestNoRepaintOnPlainScroll -v -count=1 2>&1 | grep PROBE
```

出た2つの数字を書き留める。

**確認:** 正しい実装のほうが、壊れた実装より回数が**明確に少ない**こと。参考値としてレビュー時の実測は 3 対 7 だった（別の計測方法なので一致しなくてよい）。もし差が1以下しか無い、あるいは正しい実装のほうが多いなら、**閾値を捻り出さずにここで止まって報告する。** 観測方法が間違っている。

- [ ] **Step 5: 閾値を確定して TODO を置き換える**

Step 4 の正しい実装の値を `wantPlain`、Step 3 の壊れた実装の値を `gotBuggy` として、その**中間**を閾値にする。Step 2 の `TODO` を置き換える:

```go
	// 通常スクロールは貼り付け範囲を変えないので、追加の貼り替えは起きてはならない。
	// 差が出る仕組み: ガードが無いと ResizeObserver → onResize → render() が
	// #window の高さを変え、それがまた ResizeObserver を呼ぶループに入る。
	require.Equalf(t, afterScroll, afterPlain,
		"貼り付け範囲を変えない1行分のスクロールでDOMが貼り替えられた: スクロール前=%d 後=%d。"+
			"onResize の「列数もタイル高も変わっていなければ何もしない」ガードが効いていない疑い",
		afterScroll, afterPlain)

	// 塊境界の跨ぎ自体でも、必要な回数を大きく超えて貼り替えていないこと。
	require.LessOrEqualf(t, afterScroll, <閾値>,
		"塊境界を跨ぐスクロールで%d回も貼り替えている（ResizeObserverのフィードバックループの疑い）",
		afterScroll)
```

`<閾値>` は Step 4 の実測値そのもの（ちょうど）にする。壊れた実装の値がそれを超えることを Step 6 で確かめる。

なお `require.Equal(afterScroll, afterPlain)` のほうが本命のアサーションで、こちらが単独でバグを殺せるならそれで十分。Step 6 でどちらが効いたかを確認する。

- [ ] **Step 6: ミューテーション検証**

```bash
cp internal/web/static/app.js /tmp/app.js.bak
grep -n 'cols === prevCols && rowH === prevRowH' internal/web/static/app.js
sed -i '<その行>,<その行+2>d' internal/web/static/app.js
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestNoRepaintOnPlainScroll -v -count=1 2>&1 | tail -12
cp /tmp/app.js.bak internal/web/static/app.js
```

期待: **FAIL**。メッセージから「no-change ガードが効いていない」と読み取れること。

- [ ] **Step 7: 正しい実装で 3回連続 PASS することを確認する**

貼り替え回数の計測はタイミングに依存しうる。flaky でないことを確かめる。

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestNoRepaintOnPlainScroll -count=3 -v 2>&1 | grep -E 'PROBE|--- '
```

期待: 3回とも PASS し、PROBE の数字が毎回同じであること。数字がばらつくなら報告して止まる。

- [ ] **Step 8: コミット**

```bash
git status --short
git diff --stat -- internal/web/static internal/web/templates   # 空であること
CGO_ENABLED=0 go vet ./... && gofmt -l .
git add internal/web/browser_test.go
git commit -m "test: count repaints instead of checking for surviving markers

Removing the onResize no-change guard left this test passing: the markers
were applied after the chunk boundary settled, and the follow-up scroll was
too small to re-enter the ResizeObserver loop.

A MutationObserver on #window now counts rebuilds across the boundary
crossing itself, and polling replaces the three fixed sleeps."
```

---

### Task 4: TestScrollPositionSurvivesReload のアサーションを幾何から導出する

**Files:**
- Modify: `internal/web/browser_test.go`（`TestScrollPositionSurvivesReload`)

**Interfaces:**
- Consumes: `requireBrowser(t)`（Task 1）
- Produces: なし

**このタスクが潰す欠陥（spec F4）:** `intersecting > 0` が緩すぎ、`translateY` を常に 0 にする全面崩壊でも、端に食い込んだ4枚で PASS する。末尾の `require.Greater(t, intersecting, 0, ...)` は直前の `Poll` が同じ条件を保証するため**到達不能**。`Math.abs(scrollTop - before) < 200` の 200 は1行（226px）に近く、1行分のずれを見逃す。

- [ ] **Step 1: 先に壊して、現在のテストが素通りすることを実測する**

`app.js` の `translateY` を常に 0 にする。

```bash
cp internal/web/static/app.js /tmp/app.js.bak
grep -n 'translateY' internal/web/static/app.js
```

見つかった `win.style.transform = \`translateY(${...}px)\`` の形の行を、`win.style.transform = 'translateY(0px)';` に置き換える。

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestScrollPositionSurvivesReload -v -count=1 2>&1 | tail -4
cp /tmp/app.js.bak internal/web/static/app.js
```

期待（＝現状のバグ）: **PASS**。

- [ ] **Step 2: 許容差と下限を幾何から導出するように書き換える**

`pollScrollJS` と末尾のアサーションを差し替える。行高は Poll の中で毎回測る（リロード後の DOM で測る必要があるため）。

```go
	// 許容差は1行分の半分。固定値(200px)だと1行(実測226px)とほぼ同じで、
	// 「1行まるごとずれて復元された」を見逃す。
	pollScrollJS := fmt.Sprintf(`(() => {
		const win = document.querySelector('#window');
		const tile = win.querySelector('.tile');
		if (!tile) { return false; }
		const gap = parseFloat(getComputedStyle(win).rowGap) || 0;
		const rowH = tile.getBoundingClientRect().height + gap;
		return Math.abs(famifo.scroller.scrollTop - %s) < rowH / 2;
	})()`, strconv.FormatFloat(scrollBefore, 'f', -1, 64))
```

末尾（`chromedp.Poll(intersectCountJS+" > 0", ...)` 以降）を差し替える。

```go
	// リロード後の列数を測る。可視範囲に何枚あるべきかはここから決まる。
	var cols int
	err = chromedp.Run(rctx, chromedp.Evaluate(
		`getComputedStyle(document.querySelector('#window')).gridTemplateColumns.split(' ').filter(Boolean).length`, &cols))
	require.NoError(t, err)
	require.NotZero(t, cols, "リロード後に列数を取得できなかった")

	// 「1枚でも交差していれば良い」では、窓枠の位置合わせが完全に壊れて
	// 画面が空白になっても、端に食い込んだ数枚で通ってしまう（実測）。
	// 1600x900・7列なら本来35枚前後が交差する。最低でも2行分を要求する。
	wantIntersecting := cols * 2

	// スクロール位置の復元と、その位置の塊の取得・描画は別のタイミングで
	// 起きる（描画側は非同期fetch待ち）。条件が真になるまで待ちきる。
	// Poll が失敗したときにも枚数を報告したいので、値は Poll の後に読み直す。
	pollErr := chromedp.Run(rctx, chromedp.Poll(
		fmt.Sprintf(`%s >= %d`, intersectCountJS, wantIntersecting), nil,
		chromedp.WithPollingTimeout(10*time.Second)))

	var intersecting int
	err = chromedp.Run(rctx, chromedp.Evaluate(intersectCountJS, &intersecting))
	require.NoError(t, err)
	require.NoErrorf(t, pollErr,
		"リロード後、可視範囲と交差するタイルが%d枚しかない（%d枚=%d列×2行を期待）。"+
			"窓枠の位置合わせ(translateY)が効いていない疑い",
		intersecting, wantIntersecting, cols)
```

**注意:** 元の末尾にあった `require.Greater(t, intersecting, 0, ...)` は削除する（到達不能なデッドコードだった）。上の書き方では Poll の失敗を握って値を読み直してからアサートするので、メッセージが必ず表示される。

- [ ] **Step 3: 正しい実装で通ることを確認する**

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestScrollPositionSurvivesReload -v -count=1 2>&1 | tail -4
```

期待: PASS。

- [ ] **Step 4: ミューテーション検証① translateY を常に 0 に**

Step 1 と同じ変更を入れて実行する。

期待: **FAIL**。メッセージに交差枚数の実数と「translateY が効いていない疑い」が出ること。
`waiting for function failed: timeout` だけで終わっていないこと。

- [ ] **Step 5: ミューテーション検証② 復元位置を1行ずらす**

`app.js` の復元処理で `scroller.scrollTop` に1行分足す退行を入れる。
（`onResize` 内の `scroller.scrollTop = Math.floor(topIndex / cols) * rowH;` を
`... * rowH + rowH;` にする。リロード後の復元にも効く経路であることを実行して確かめる。
もしこの経路がリロード時に走らないなら、代わりに `famifo` の初期化直後に
`scroller.scrollTop += rowH;` を1回入れる形でよい。）

期待: **FAIL**。旧実装の許容差 200px では通っていたずれが検出されること。

もし FAIL しなければ、許容差の導出が効いていない。閾値を弄らず報告して止まる。

- [ ] **Step 6: 作業ツリーを戻してコミット**

```bash
cp /tmp/app.js.bak internal/web/static/app.js
git status --short
git diff --stat -- internal/web/static   # 空であること
CGO_ENABLED=0 go vet ./... && gofmt -l .
git add internal/web/browser_test.go
git commit -m "test: derive the reload assertions from the grid geometry

Pinning translateY to 0 blanks the gallery, yet intersecting > 0 still
passed on the few tiles clipping the edge, and the trailing assertion was
unreachable behind an identical Poll.

Requires two rows of intersecting tiles and halves the tolerance to half a
row height."
```

---

### Task 5: TestLightboxCrossesChunkBoundary を期待列との完全一致にする

**Files:**
- Modify: `internal/web/browser_test.go`（`TestLightboxCrossesChunkBoundary`、`startTestApp`、パッケージ変数）

**Interfaces:**
- Consumes: `requireBrowser(t)`（Task 1）
- Produces: パッケージ変数 `testPhotoDir string` — シードした写真の置き場。期待される写真の並びを再現するのに使う。

**このタスクが潰す欠陥（spec F5, F7）:** `src !== prev` しか見ないため、**2塊目以降で常に1枚ずれた写真を返す** off-by-one で PASS する。`len(distinct) >= 20` は steps=130 と無関係の魔法の数字で、2枚を交互に出しても通る。原寸画像が 404 でも通る。

**方針:** `seedCorpus` の生成順から期待される ID 列を Go 側で再現し、観測した src 列と完全一致で突き合わせる。サーバの `ListRange` を呼ばずに再現することで、独立した検証になる。

- [ ] **Step 1: 先に壊して、現在のテストが素通りすることを実測する**

`app.js:77` の `return entry.urls[i % chunkSize] ?? null;` を、2塊目以降だけ1枚ずらす形にする。

```bash
cp internal/web/static/app.js /tmp/app.js.bak
grep -n 'entry.urls\[i % chunkSize\]' internal/web/static/app.js   # 77 のはず
```

その行を次に置き換える:

```js
    const off = i % chunkSize; // MUTATION: 2塊目以降で1枚ずらす
    return entry.urls[i >= chunkSize ? (off + 1) % chunkSize : off] ?? null;
```

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestLightboxCrossesChunkBoundary -v -count=1 2>&1 | tail -4
cp /tmp/app.js.bak internal/web/static/app.js
```

期待（＝現状のバグ）: **PASS**。

- [ ] **Step 2: 写真の置き場をパッケージ変数として公開する**

`var baseURL string` の隣に:

```go
// testPhotoDir は seedCorpus が写真を書いたディレクトリ。
// 期待される写真の並びを再現するために使う。
var testPhotoDir string
```

`startTestApp` の中、`photoDir := filepath.Join(tempDir, "photos")` の直後に `testPhotoDir = photoDir` を足す。

- [ ] **Step 3: 期待される URL 列を返すヘルパを足す**

`scrollToPhotoJS` の隣に置く。

```go
// expectedPhotoURLs はギャラリーの並び順どおりの原寸URLをn件返す。
//
// seedCorpus は p0000.jpg から順に takenAt を1日ずつ古くしていくので、
// 通し番号がそのまま並び順（新しい順）になる。サーバの ListRange を
// 呼ばずにここで組み立てるのは、クライアント側のオフセット計算を
// サーバと独立に検証するため。両方が同じ計算を共有すると、
// ずれが打ち消し合って見えなくなる。
func expectedPhotoURLs(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		path := filepath.Join(testPhotoDir, fmt.Sprintf("p%04d.jpg", i))
		out[i] = "/photo/" + store.IDFor(path)
	}
	return out
}
```

- [ ] **Step 4: テストを書き換える**

```go
// --- Task: TestLightboxCrossesChunkBoundary ---
//
// ライトボックスの送りが塊(testPageSize枚)の境界で止まらないこと、
// そして境界の継ぎ目で写真がずれないことを見る。
//
// 「srcが前回と変わったか」だけでは、2塊目以降で常に1枚ずれた写真を
// 返す off-by-one を検出できない（実測でPASSした）。期待される並びと
// 完全一致で突き合わせる。
func TestLightboxCrossesChunkBoundary(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// 塊の境界を2回（testPageSize番目・testPageSize*2番目）跨ぐのに十分な回数。
	steps := testPageSize*2 + 10
	want := expectedPhotoURLs(steps + 1)

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(5*time.Second)),
	)
	require.NoError(t, err)

	// 原寸画像が実際にデコードされたことまで見る。srcが404でもライトボックスは
	// 開いてしまうため、開いたかどうかだけでは表示を保証できない。
	const lbDecodedJS = `(() => { const i = document.querySelector('#lightbox img'); return i.complete && i.naturalWidth > 0; })()`
	err = chromedp.Run(rctx, chromedp.Poll(lbDecodedJS, nil, chromedp.WithPollingTimeout(10*time.Second)))
	require.NoError(t, err, "ライトボックスの原寸画像がデコードされなかった（画像が表示されていない）")

	// getAttribute('src') は絶対URLに解決されないので、テンプレートが書いた
	// 相対パスとそのまま突き合わせられる。
	const srcAttrJS = `document.querySelector('#lightbox img').getAttribute('src')`

	var got string
	err = chromedp.Run(rctx, chromedp.Evaluate(srcAttrJS, &got))
	require.NoError(t, err)
	require.Equalf(t, want[0], got,
		"最初に開いた写真が先頭(0番)ではない: got=%s want=%s", got, want[0])

	prev := got
	for i := 1; i <= steps; i++ {
		pollExpr := fmt.Sprintf(`%s !== %s`, srcAttrJS, strconv.Quote(prev))
		err = chromedp.Run(rctx,
			chromedp.KeyEvent(kb.ArrowRight),
			chromedp.Poll(pollExpr, nil, chromedp.WithPollingTimeout(5*time.Second)),
			chromedp.Evaluate(srcAttrJS, &got),
		)
		require.NoErrorf(t, err,
			"%d枚目でsrcが変化しなかった（塊の境界=%dで止まっている疑い）: prev=%s",
			i, testPageSize, prev)
		require.Equalf(t, want[i], got,
			"%d枚目の写真が期待と違う（塊の境界=%dでの継ぎ目のずれの疑い）: got=%s want=%s",
			i, testPageSize, got, want[i])
		prev = got
	}
}
```

- [ ] **Step 5: 正しい実装で通ることを確認する**

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestLightboxCrossesChunkBoundary -v -count=1 2>&1 | tail -6
```

期待: PASS。

もし「最初に開いた写真が先頭(0番)ではない」で落ちるなら、`seedCorpus` の並びについての前提が違う。**テストの期待値を実測に合わせて書き換えるのではなく**、`store.ListRange` が返す順序を調べて原因を突き止め、報告する。

- [ ] **Step 6: ミューテーション検証① 2塊目以降で1枚ずらす**

Step 1 と同じ変更を入れて実行する。

期待: **FAIL**。`61枚目の写真が期待と違う` のように、**境界のちょうど次**で落ちること。

- [ ] **Step 7: ミューテーション検証② 境界で止まる**

`app.js:77` を `return entry.urls[i] ?? null;` にする。

期待: **FAIL**。メッセージに「塊の境界=60で止まっている疑い」が出ること。

- [ ] **Step 8: ミューテーション検証③ 原寸URLを404に**

`internal/web/templates/items.html` の `data-full="{{.FullURL}}"` を `data-full="/photo/does-not-exist"` にする。

期待: **FAIL**。ただし落ちる箇所は「デコードされなかった」でも「最初に開いた写真が先頭ではない」でもよい。**PASS しないこと**が要件。

- [ ] **Step 9: 作業ツリーを戻してコミット**

```bash
cp /tmp/app.js.bak internal/web/static/app.js
git checkout -- internal/web/templates/items.html
git status --short
git diff --stat -- internal/web/static internal/web/templates   # 空であること
CGO_ENABLED=0 go vet ./... && gofmt -l .
git add internal/web/browser_test.go
git commit -m "test: match the lightbox against the expected photo order

Checking only that src changed let an off-by-one past the first chunk
pass, and the >= 20 distinct threshold was unrelated to the 130 steps taken.

Rebuilds the expected order from the seeded filenames and compares each
step, and waits for the full-size image to decode."
```

---

### Task 6: TestTileTapOpensLightbox のタップ座標をスクラバーとの重なりから導出する

**Files:**
- Modify: `internal/web/browser_test.go`（`TestTileTapOpensLightbox`)

**Interfaces:**
- Consumes: `requireBrowser(t)`（Task 1）
- Produces: なし

**このタスクが潰す欠陥（spec F6）:** タップ点 `r.Right - r.Width*0.1` はタイル基準で、スクラバー帯の位置と無関係。実測の余裕は 8.9px しかなく、バグを残したまま帯幅を 8px にするとタップ点が帯を外れて PASS した。CSS の `width` や breakpoint を変えただけで、fail も skip もせずに無意味化する。

**方針:** `TestGridAlignmentPastFirstChunk` が `chunkSize % cols == 0` でやっているのと同じ前提ガードを入れる。タップ点はスクラバー矩形と右端タイル矩形の**重なりの中点**にし、重なりが無ければ `t.Fatalf` で止める。

- [ ] **Step 1: 現在の余裕がどれだけ薄いかを実測する**

テストに一時的な `t.Logf` を足して、タイル矩形とスクラバー矩形の関係を出す。
（スクラバーは `hidden` かつ `opacity:0` だが、`getBoundingClientRect()` は取れる。
`[hidden]` は `display:none` なので、取れるかどうかを実行して確かめること。取れない場合は
`getComputedStyle(document.querySelector('.scrubber')).width` と `right` から矩形を組み立てる。）

- [ ] **Step 2: テストを書き換える**

```go
// --- Task: TestTileTapOpensLightbox ---
//
// 修正前は、スクラバーの目に見えない帯が pointer-events:auto のままで、
// 右端の列のタイルのうち帯と重なる部分をタップするとギャラリーのシークとして
// 奪われていた。この帯は「触られていないときは隠れている」のが正しい挙動なので、
// ページ読み込み直後の一時表示が終わるのを待ってから検証する。
//
// タップ点は必ずスクラバー帯と重なる位置にする。タイル幅の割合で決め打ちすると、
// 帯の幅やCSSのブレークポイントが変わったときに、バグが残ったままタップ点が
// 帯を外れて素通りする（実測。帯を32px→8pxにしたらバグ入りでPASSした）。
func TestTileTapOpensLightbox(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(800, 1000),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Poll(`!document.querySelector('#scrubber').classList.contains('visible')`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
	)
	require.NoError(t, err)

	// 右端の列のタイルと、スクラバー帯の矩形を同時に測る。
	// スクラバーは hidden(display:none) のことがあるため、CSSの指定値から
	// 帯の矩形を組み立てる。実際に pointer-events を持つ範囲はこの矩形。
	var m struct {
		Tile         rect    `json:"tile"`
		ScrubLeft    float64 `json:"scrubLeft"`
		ScrubRight   float64 `json:"scrubRight"`
		ScrubTop     float64 `json:"scrubTop"`
		ScrubBottom  float64 `json:"scrubBottom"`
	}
	measureJS := `(() => {
		const win = document.querySelector('#window');
		const cols = getComputedStyle(win).gridTemplateColumns.split(' ').filter(Boolean).length;
		const b = win.querySelectorAll('.tile')[cols - 1].getBoundingClientRect();
		const sc = document.querySelector('#scrubber');
		const cs = getComputedStyle(sc);
		const w = parseFloat(cs.width) || 0;
		const top = parseFloat(cs.top) || 0;
		return {
			tile: {Left:b.left, Top:b.top, Right:b.right, Bottom:b.bottom, Width:b.width, Height:b.height},
			scrubLeft: window.innerWidth - w,
			scrubRight: window.innerWidth,
			scrubTop: top,
			scrubBottom: window.innerHeight,
		};
	})()`

	var scrollBefore float64
	err = chromedp.Run(rctx,
		chromedp.Evaluate(measureJS, &m),
		chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollBefore),
	)
	require.NoError(t, err)
	require.Greater(t, m.Tile.Width, 0.0, "右端タイルの矩形が取得できなかった")

	// タイルとスクラバー帯の重なり。ここが空なら、そもそもこの不具合は
	// 再現しない条件なので、静かに通さず前提条件の不成立として止める。
	overlapLeft := m.Tile.Left
	if m.ScrubLeft > overlapLeft {
		overlapLeft = m.ScrubLeft
	}
	overlapRight := m.Tile.Right
	if m.ScrubRight < overlapRight {
		overlapRight = m.ScrubRight
	}
	if overlapRight-overlapLeft <= 0 {
		t.Fatalf("右端タイル(%.0f〜%.0f)とスクラバー帯(%.0f〜%.0f)が重なっていないため、"+
			"この不具合は再現しない条件です。ビューポート幅か .scrubber の width を確認してください",
			m.Tile.Left, m.Tile.Right, m.ScrubLeft, m.ScrubRight)
	}
	t.Logf("重なり幅=%.1fpx（タイル右端%.0f, 帯左端%.0f）", overlapRight-overlapLeft, m.Tile.Right, m.ScrubLeft)

	x := (overlapLeft + overlapRight) / 2
	y := m.Tile.Top + m.Tile.Height/2
	// yが帯の縦範囲からも外れていないことを確かめる。帯は --topbar-h から下だけ。
	require.Greaterf(t, y, m.ScrubTop,
		"タップ点のy(%.0f)がスクラバー帯の上端(%.0f)より上にあり、帯と重ならない", y, m.ScrubTop)

	err = chromedp.Run(rctx, chromedp.MouseClickXY(x, y))
	require.NoError(t, err)

	err = chromedp.Run(rctx, chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil,
		chromedp.WithPollingTimeout(3*time.Second)))
	require.NoErrorf(t, err,
		"スクラバー帯と重なる位置(x=%.0f, y=%.0f)のタイルをタップしてもライトボックスが開かなかった"+
			"（スクラバーに奪われている疑い）", x, y)

	var scrollAfter float64
	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollAfter))
	require.NoError(t, err)
	require.Equal(t, scrollBefore, scrollAfter, "スクロール位置が変化した（スクラバーのシークが発生した）")
}
```

- [ ] **Step 3: 正しい実装で通ることを確認する**

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -run TestTileTapOpensLightbox -v -count=1 2>&1 | tail -6
```

期待: PASS。ログに「重なり幅=」が出ること。

`t.Fatalf` の「重なっていない」で止まる場合は、800px 幅での列数と帯幅を確認する。ビューポート幅を変えるか、CSS の実測値の読み方を直す（テストを緩めるのではなく）。

- [ ] **Step 4: ミューテーション検証① 元のバグを戻す**

```bash
cp internal/web/static/app.css /tmp/app.css.bak
sed -i 's|^\.scrubber\.visible { opacity: 1; pointer-events: auto; }|.scrubber.visible { opacity: 1; }|' internal/web/static/app.css
grep -n 'pointer-events' internal/web/static/app.css
```

`.scrubber { ... pointer-events: none; }` の `pointer-events: none;` を削除する（＝常に auto）。

期待: **FAIL**。メッセージに座標が出ること。

- [ ] **Step 5: ミューテーション検証② バグを残したまま帯幅を 8px にする**

Step 4 の状態のまま、`.scrubber` の `width: 32px` を `width: 8px` にする。

期待: **依然として FAIL**。以前はここで PASS してしまっていた。
もし `t.Fatalf`（重なっていない）で止まるなら、それも受け入れてよい（静かに通さないことが要件）。ただし `--- PASS` になってはならない。

- [ ] **Step 6: 作業ツリーを戻してコミット**

```bash
cp /tmp/app.css.bak internal/web/static/app.css
git status --short
git diff --stat -- internal/web/static   # 空であること
CGO_ENABLED=0 go vet ./... && gofmt -l .
git add internal/web/browser_test.go
git commit -m "test: aim the tap at the scrubber overlap, not a fixed fraction

The tap point sat 8.9px inside the strip, so narrowing the strip to 8px let
the test pass with the pointer-events bug still present.

Computes the overlap between the rightmost tile and the strip, taps its
midpoint, and fails loudly when there is no overlap to test."
```

---

### Task 7: 恒真式の削除と残った魔法の数字の導出化

**Files:**
- Modify: `internal/web/browser_test.go`（`TestGridAlignmentPastFirstChunk`、`TestScrubberReachesBothEnds`)

**Interfaces:**
- Consumes: `requireBrowser(t)`（Task 1）
- Produces: なし

**このタスクが潰す欠陥（spec F8）:** `require.Equal(t, res.Cols, res.DistinctLefts, "横方向のずれ")` は恒真式で、バグ注入下でも成立する（実測）。CSS Grid は `gridColumnStart` に関係なく必ず cols 本のトラック上に置くため、タイルが cols 枚以上あれば常に真。また `scrollToPhotoJS(150)` の 150 は `testPageSize` と無関係の数字。`TestScrubberReachesBothEnds` は `drag()` 直後に待たずに `scrollTop` を読む。

- [ ] **Step 1: 恒真式であることを自分で確かめる**

`TestGridAlignmentPastFirstChunk` に `t.Logf("cols=%d distinctLefts=%d", res.Cols, res.DistinctLefts)` を足し、
`gridColumnStart` の補正を削除した状態で実行する。

```bash
cp internal/web/static/app.js /tmp/app.js.bak
grep -n 'gridColumnStart' internal/web/static/app.js
```

`firstTile.style.gridColumnStart = ...` の行を削除して実行。

期待: `cols=7 distinctLefts=7` のまま（＝この式は何も検出していない）。
テスト自体は `wantStart` のアサーションで FAIL する。

`app.js` を戻す。

- [ ] **Step 2: 恒真式を、先頭タイルの実際の左端座標の検査に置き換える**

`measureJS` に先頭タイルの left と列トラックの left 一覧を足す。

```go
	var res struct {
		PastedIndex     int       `json:"pastedIndex"`
		Cols            int       `json:"cols"`
		GridColumnStart string    `json:"gridColumnStart"`
		FirstLeft       float64   `json:"firstLeft"`
		ColumnLefts     []float64 `json:"columnLefts"`
	}
	measureJS := `(() => {
		const win = document.querySelector('#window');
		const tiles = [...win.querySelectorAll('.tile')];
		const first = win.firstElementChild;
		const cols = getComputedStyle(win).gridTemplateColumns.split(' ').filter(Boolean).length;
		// 列トラックの左端座標。DOM順の「2行目」を切り出すやり方は使えない。
		// 先頭タイルが列0以外から始まると、その区間は行境界を跨ぎ、
		// 列トラックが回転した順序で並んでしまうため（まさにこのテストが
		// 検証している状況）。全タイルの左端を重複排除して昇順に並べる。
		const lefts = [...new Set(tiles.map((t) => Math.round(t.getBoundingClientRect().left)))]
			.sort((a, b) => a - b);
		return {
			pastedIndex: famifo.pastedIndex(),
			cols,
			gridColumnStart: getComputedStyle(first).gridColumnStart,
			firstLeft: Math.round(first.getBoundingClientRect().left),
			columnLefts: lefts,
		};
	})()`
```

そして末尾の恒真式を差し替える。

```go
	// 旧: require.Equal(t, res.Cols, res.DistinctLefts, ...) は恒真式だった。
	// CSS Grid は grid-column-start に関わらず必ず cols 本のトラック上に置くため、
	// タイルが cols 枚以上あれば「左端の種類数 == 列数」は常に成立する（実測）。
	// 先頭タイルが「期待した列のトラック」に実際に載っているかを見る。
	require.Lenf(t, res.ColumnLefts, res.Cols,
		"列トラックの左端座標を%d本ぶん取得できなかった: %v", res.Cols, res.ColumnLefts)
	wantLeft := res.ColumnLefts[wantStart-1]
	require.InDeltaf(t, wantLeft, res.FirstLeft, 1.0,
		"先頭タイルが期待した列のトラックに載っていない（横方向のずれ）: "+
			"got=%.0f want=%.0f pastedIndex=%d cols=%d 列トラック=%v",
		res.FirstLeft, wantLeft, res.PastedIndex, res.Cols, res.ColumnLefts)
```

- [ ] **Step 3: `scrollToPhotoJS(150)` の 150 を導出する**

`TestGridAlignmentPastFirstChunk`、`TestScrollPositionSurvivesReload`、
`TestScrollAnchoredOnResize` の3箇所で使われている。共通の定数を足す。

`testPageSize` の宣言の下に:

```go
	// deepScrollIndex は「複数の塊を跨いだ、十分に奥」の位置。
	// 塊を2つ跨いだうえで、境界のちょうど上に止まらないよう半端に足す。
	deepScrollIndex = testPageSize*2 + 30
```

3箇所の `scrollToPhotoJS(150)` を `scrollToPhotoJS(deepScrollIndex)` にする。
（Task 3 で `TestNoRepaintOnPlainScroll` に書いた `testPageSize*2+30` もこの定数に置き換える。）

- [ ] **Step 4: TestScrubberReachesBothEnds のドラッグ後に待つ**

`drag()` の直後で `scrollTop` を読む前に、値が落ち着くまで待つ。
`drag` ヘルパの末尾に足す:

```go
	drag := func(x, y0, y1 float64) {
		t.Helper()
		press := input.DispatchMouseEvent(input.MousePressed, x, y0).WithButton(input.Left).WithClickCount(1)
		move := input.DispatchMouseEvent(input.MouseMoved, x, y1).WithButton(input.Left).WithButtons(1)
		release := input.DispatchMouseEvent(input.MouseReleased, x, y1).WithButton(input.Left).WithClickCount(1)
		err := chromedp.Run(rctx, press, move, release)
		require.NoError(t, err)

		// seek が requestAnimationFrame でスロットルされるようになっても
		// 壊れないよう、scrollTop が2フレーム連続で同じ値になるまで待つ。
		settleJS := `(() => {
			const now = famifo.scroller.scrollTop;
			if (window.__lastScroll === now) { return true; }
			window.__lastScroll = now;
			return false;
		})()`
		err = chromedp.Run(rctx,
			chromedp.Evaluate(`window.__lastScroll = -1;`, nil),
			chromedp.Poll(settleJS, nil, chromedp.WithPollingTimeout(5*time.Second)))
		require.NoError(t, err, "ドラッグ後にスクロール位置が落ち着かなかった")
	}
```

- [ ] **Step 5: 全8テストが通ることを確認する**

```bash
CGO_ENABLED=0 go test -tags browser ./internal/web/ -v -count=1 2>&1 | grep -E '^(--- |ok|FAIL)'
```

期待: 33本すべて `--- PASS`。

- [ ] **Step 6: ミューテーション検証 gridColumnStart を off-by-one に**

`app.js` の `firstTile.style.gridColumnStart = (pastedFrom % cols) + 1;` を `+ 2;` にする。

期待: **FAIL**。新しいアサーション（「期待した列のトラックに載っていない」）が
実際の座標を報告して落ちること。

- [ ] **Step 7: 作業ツリーを戻してコミット**

```bash
cp /tmp/app.js.bak internal/web/static/app.js
git status --short
git diff --stat -- internal/web/static   # 空であること
CGO_ENABLED=0 go vet ./... && gofmt -l .
git add internal/web/browser_test.go
git commit -m "test: replace the tautological grid check and derive the constants

cols == distinctLefts held under every mutation: grid always lays tiles on
cols tracks regardless of grid-column-start.

Checks the first tile's left against the expected column track, names the
scroll depth after the chunk size, and waits for the scrubber drag to settle."
```

---

### Task 8: 全体の再検証と所見の記録

**Files:**
- Modify: `docs/superpowers/specs/2026-08-25-browser-test-review-findings.md`（結果の追記）

**Interfaces:**
- Consumes: Task 1〜7 のすべて
- Produces: なし

- [ ] **Step 1: 全ミューテーションを通しで再実行する**

spec の「検証済みの事実」の表 A〜H を、修正後のテストに対して全て流し直す。
1件ずつ、注入 → 実行 → 結果を記録 → `git checkout -- internal/web/static internal/web/templates` で復元。

| # | 注入 | 修正後の期待 |
|---|---|---|
| A | app.js 全体を無効化 | TestInitialRenderFillsViewport が FAIL |
| B | onResize の no-change ガード削除 | TestNoRepaintOnPlainScroll が FAIL |
| C | translateY を常に 0 | TestScrollPositionSurvivesReload が FAIL |
| D | 2塊目以降で1枚ずれる | TestLightboxCrossesChunkBoundary が FAIL |
| E | pointer-events バグ + 帯幅8px | TestTileTapOpensLightbox が FAIL または Fatalf |
| F | サムネイルURLを404に | 少なくとも TestInitialRenderFillsViewport が FAIL |
| G | 原寸URLを404に | TestLightboxCrossesChunkBoundary が FAIL |
| H | docker を PATH から外す | 非ブラウザ25本が PASS、ブラウザ8本が SKIP、EXIT=0 |

**1つでも期待どおりにならなければ、そのタスクに戻る。**

- [ ] **Step 2: 最終確認**

```bash
CGO_ENABLED=0 go test ./... 2>&1 | tail -10
CGO_ENABLED=0 go test -tags browser ./internal/web/ -count=1 -v 2>&1 | grep -cE '^--- PASS'
CGO_ENABLED=0 go vet ./... && gofmt -l .
git status --short
docker ps -a --filter name=famifo-browser-test-chrome    # 残骸が無いこと
```

期待: 全パッケージ ok、`--- PASS` が 33、vet 無言、`gofmt -l` が空、
`git status` は `?? .claude/` のみ、コンテナの残骸なし。

- [ ] **Step 3: spec に結果を追記してコミット**

`docs/superpowers/specs/2026-08-25-browser-test-review-findings.md` の末尾に
「## 修正後の再検証（YYYY-MM-DD）」節を足し、Step 1 の表を実測結果で埋める。

```bash
git add docs/superpowers/specs/2026-08-25-browser-test-review-findings.md
git commit -m "docs: record the post-fix mutation results"
```

---

## 自己レビュー

**Spec 網羅:**

| Spec 要件 | タスク |
|---|---|
| F1 Docker 不在時の全面スキップ | Task 1 |
| F2 JS に依存しない viewport テスト | Task 2 |
| F3 no-change ガードを検出しない | Task 3 |
| F4 intersecting > 0 が弱い / 到達不能なアサーション / 200px | Task 4 |
| F5 写真の同一性を見ていない | Task 5 |
| F6 タップ座標のハードコード | Task 6 |
| F7 画像がデコードされたかを見ていない | Task 2（サムネイル）、Task 5（原寸） |
| F8 恒真式・魔法の数字・drag 後の待ち | Task 7 |
| スコープ外（月ラベル、タッチ、コンテナ名の動的化） | 対象外と明記済み |

**型・名前の整合:** `requireBrowser`（Task 1 で定義、2〜7 で使用）、`browserReady` / `browserSkipReason`（Task 1）、`testPhotoDir` / `expectedPhotoURLs`（Task 5）、`deepScrollIndex`（Task 7 で定義し、Task 3 が書いた `testPageSize*2+30` を置き換える）。既存の `rect` 型、`rectJS`、`newTab`、`waitForTiles`、`scrollToPhotoJS` はそのまま使う。

**既知の未確定点（実装者が実測で埋める）:**

1. **Task 3 Step 5 の閾値** — 正しい実装での貼り替え回数を実測してから決める。計画で数字を先に決めない。差が付かなければ観測方法が誤りなので、閾値を捻り出さずに止まって報告する。
2. **Task 4 Step 5 のミューテーション経路** — 「復元位置を1行ずらす」をどこに入れると再現するかは実装を読んで決める。候補を2つ挙げてある。
3. **Task 6 Step 1** — `.scrubber` が `hidden`（`display:none`）のとき `getBoundingClientRect()` が 0 を返すか。返すなら `getComputedStyle` から組み立てる（本文の `measureJS` はその前提で書いてある）が、実測で確かめること。
