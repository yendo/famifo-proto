//go:build browser

// app.jsは今まで一度もブラウザで実行されたことが無い。既存のGoテストは
// 配信されるファイルに特定の文字列が含まれるかしか見ておらず、仮想スクロール・
// ライトボックス・日付スクラバーの実際の挙動は検証されていなかった。
// このファイルはDockerコンテナ上のヘッドレスChromeを chromedp で操作し、
// 実際にJavaScriptを走らせて確認する。
//
// 前提:
//   - ホストのChromeは絶対に起動しない（chromedp.NewContext単独ではなく、
//     必ず chromedp.NewRemoteAllocator でコンテナ内Chromeに接続する）
//   - コンテナは chromedp/headless-shell を --network host で起動する。
//     このイメージは既定で 0.0.0.0:9223 でheadless-shellを起動し、
//     socatで 127.0.0.1:9222 へ転送するようになっている。
//   - 写真は毎回生成する。ユーザーの実ライブラリには依存しない
//     （実行環境によって結果が変わるとCIで再現できないため）。
//
// 実行方法は README.md を参照。
package web

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/require"

	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

const (
	// containerName は使い回すコンテナの固定名。前回異常終了した残骸を
	// 掃除できるように固定にしている。
	containerName   = "famifo-browser-test-chrome"
	dockerImage     = "chromedp/headless-shell:latest"
	debugVersionURL = "http://127.0.0.1:9222/json/version"

	// testPhotoCount / testPageSize: 60枚の塊を複数跨ぐのに十分な枚数。
	testPhotoCount = 200
	testPageSize   = 60
)

// allocCtx はコンテナ内Chromeに接続したchromedpのアロケータcontext。
// 各テストはこれを親にタブ(chromedp.NewContext)を作る。ブラウザ本体は
// 全テストで1つを使い回す（コンテナ起動コストを1回に抑えるため）。
var allocCtx context.Context

// baseURL はテスト用に起動したアプリのURL（例: http://127.0.0.1:54321）。
var baseURL string

// testPhotoDir は seedCorpus が写真を書いたディレクトリ。
// 期待される写真の並びを再現するために使う。
var testPhotoDir string

// browserReady はブラウザ環境（Docker上のheadless-shellとテスト用アプリ）を
// 用意できたか。TestMain が設定し、requireBrowser が読む。
var browserReady bool

// browserSkipReason は用意できなかった理由。requireBrowser が表示する。
var browserSkipReason string

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

// setupBrowserEnv はDockerでheadless-shellを起動し、テスト用アプリも
// 起動する。Dockerが使えない/失敗した場合は ok=false を返す。
// この場合 cleanup はそれまでに確保したものだけを片付ける安全な関数。
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

// waitForDebugger はheadless-shellのDevToolsエンドポイントが応答するまで
// ポーリングする。固定sleepで「起動しただろう」と決め打ちしない。
func waitForDebugger(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(debugVersionURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// startTestApp は生成した写真一式でストアを満たし、main.goと同じ部品
// （store・thumb・web）をプロセス内で直接組み立ててアプリを起動する。
// ビルド済みバイナリを別プロセスで動かす方式ではなく直接呼ぶ方式を選んだのは、
// EXIF抽出やfsnotify監視（index パッケージ）を経由する必要が無く、
// テストデータを直接ストアへ流し込めるほうが速く決定的だから。
func startTestApp() (tempDir string, srv *httptest.Server, closeStore func(), err error) {
	tempDir, err = os.MkdirTemp("", "famifo-browser-*")
	if err != nil {
		return "", nil, nil, err
	}

	photoDir := filepath.Join(tempDir, "photos")
	testPhotoDir = photoDir
	thumbDir := filepath.Join(tempDir, "thumbs")
	if err := os.MkdirAll(photoDir, 0o755); err != nil {
		return tempDir, nil, nil, err
	}

	st, err := store.Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		return tempDir, nil, nil, err
	}

	gen, err := thumb.NewGenerator(thumbDir, 480)
	if err != nil {
		st.Close()
		return tempDir, nil, nil, err
	}

	if err := seedCorpus(st, gen, photoDir, testPhotoCount); err != nil {
		st.Close()
		return tempDir, nil, nil, err
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	webSrv, err := NewServer(st, thumbDir, testPageSize, log)
	if err != nil {
		st.Close()
		return tempDir, nil, nil, err
	}

	srv = httptest.NewServer(webSrv.Handler())
	return tempDir, srv, func() { st.Close() }, nil
}

// seedCorpus はn枚の実画像ファイルを生成してストアに登録する。
// ユーザーの実ライブラリを読むと実行環境ごとに結果が変わりCIで再現できない
// ため、写真は常にこの場で作る。撮影日時は1日ずつずらすので、
// 通し番号iがそのままギャラリー上の並び順(新しい順)に対応する。
func seedCorpus(st *store.Store, gen *thumb.Generator, photoDir string, n int) error {
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)

	for i := 0; i < n; i++ {
		name := fmt.Sprintf("p%04d.jpg", i)
		path := filepath.Join(photoDir, name)
		if err := writeTestJPEG(path, i); err != nil {
			return fmt.Errorf("テスト画像を書けません (%s): %w", name, err)
		}

		id := store.IDFor(path)
		hasThumb := gen.Generate(path, id) == nil

		takenAt := base.Add(-time.Duration(i) * 24 * time.Hour)
		fi, statErr := os.Stat(path)
		if statErr != nil {
			return fmt.Errorf("テスト画像を統計できません (%s): %w", name, statErr)
		}
		p := store.Photo{
			ID: id, Path: path, TakenAt: takenAt, ModTime: takenAt,
			Size: fi.Size(), Ext: ".jpg", HasThumb: hasThumb,
		}
		if err := st.Upsert(ctx, p); err != nil {
			return fmt.Errorf("写真を登録できません (%s): %w", name, err)
		}
	}
	return nil
}

// writeTestJPEG はi番目の写真用に、色だけが違う小さな正方形JPEGを作る。
func writeTestJPEG(path string, i int) error {
	const side = 32
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	c := color.RGBA{R: uint8(i * 37 % 256), G: uint8(i * 53 % 256), B: uint8(i * 97 % 256), A: 255}
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 80})
}

// newTab はブラウザに新しいタブ(target)を作る。1コンテナを全テストで
// 使い回しつつ、テスト同士のDOM/状態は分離するため毎回新規タブにする。
func newTab(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(t.Logf))
	t.Cleanup(cancel)
	return ctx
}

// waitForTiles は最初のタイルが描画されるまで待つ最初のアクション列。
func waitForTiles(timeout time.Duration) chromedp.Action {
	return chromedp.Poll(`document.querySelectorAll('#window .tile').length > 0`, nil,
		chromedp.WithPollingTimeout(timeout))
}

// scrollToPhotoJS は写真indexが可視範囲付近に来るまでスクロールするJS式を返す。
// 単純に maxScroll() の割合で決めると、OVERSCAN_ROWS(4行)分だけ手前で
// 描画範囲が計算されるため、狙った塊にわずかに届かないことがある
// （実測）。タイルの実測高さから行を逆算し、狙った写真indexの行の
// 先頭へ直接スクロールすることで、狭い際どい境界を避ける。
func scrollToPhotoJS(index int) string {
	return fmt.Sprintf(`(() => {
		const win = document.querySelector('#window');
		const tile = win.querySelector('.tile');
		const r = tile.getBoundingClientRect();
		const gap = parseFloat(getComputedStyle(win).rowGap) || 0;
		const rowH = r.height + gap;
		const cols = getComputedStyle(win).gridTemplateColumns.split(' ').filter(Boolean).length;
		famifo.scroller.scrollTop = Math.floor(%d / cols) * rowH;
	})()`, index)
}

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

// rect はDOM要素の getBoundingClientRect() を受け取るための入れ物。
type rect struct {
	Left, Top, Right, Bottom, Width, Height float64
}

const rectJS = `(() => {
	const b = (%s).getBoundingClientRect();
	return {Left:b.left, Top:b.top, Right:b.right, Bottom:b.bottom, Width:b.width, Height:b.height};
})()`

// --- Task: TestGridAlignmentPastFirstChunk ---
//
// 塊(testPageSize枚)を跨いで貼り付けたとき、先頭タイルのgrid-column-startを補正
// しないと横方向にずれる不具合があった。この不具合は列数がtestPageSizeの約数の
// ときは起きない（どの塊境界でも列0から始まるため）。スマホ・タブレットの幅は
// すべてtestPageSizeを割り切る列数になり、デスクトップ幅（1600px, 7列）だけが
// 割り切らない。
func TestGridAlignmentPastFirstChunk(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var cols, chunkSize int
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(`getComputedStyle(document.querySelector('#window')).gridTemplateColumns.split(' ').filter(Boolean).length`, &cols),
		// data-chunk はサーバの pageSize から描画される。ここが食い違うと
		// クライアントが計算するオフセットが全てずれるため、実際に一致を確認する。
		chromedp.Evaluate(`famifo.chunkSize`, &chunkSize),
	)
	require.NoError(t, err)
	require.NotZero(t, cols, "列数を取得できなかった")
	require.Equal(t, testPageSize, chunkSize, "サーバとクライアントで塊のサイズが食い違っている")

	// ガードはGo側の定数(testPageSize)ではなく、ブラウザが実際に保持している
	// chunkSizeを基準にする。こちらが実態を反映した値だから。
	if chunkSize%cols == 0 {
		t.Fatalf("このビューポートの列数(%d)は塊サイズ(%d)の約数のため、この不具合は再現しない条件です。"+
			"ビューポート幅を変更してください", cols, chunkSize)
	}
	t.Logf("列数=%d（塊サイズ%dの約数ではない）", cols, chunkSize)

	// 第2〜3の塊に入るところまでスクロールする（写真150番付近）。
	err = chromedp.Run(rctx,
		chromedp.Evaluate(scrollToPhotoJS(150), nil),
		chromedp.Poll(fmt.Sprintf(`famifo.pastedIndex() >= %d`, chunkSize), nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err)

	var res struct {
		PastedIndex     int    `json:"pastedIndex"`
		Cols            int    `json:"cols"`
		GridColumnStart string `json:"gridColumnStart"`
		DistinctLefts   int    `json:"distinctLefts"`
	}
	measureJS := `(() => {
		const win = document.querySelector('#window');
		const tiles = [...win.querySelectorAll('.tile')];
		const first = win.firstElementChild;
		const lefts = new Set(tiles.map((t) => Math.round(t.getBoundingClientRect().left)));
		return {
			pastedIndex: famifo.pastedIndex(),
			cols: getComputedStyle(win).gridTemplateColumns.split(' ').filter(Boolean).length,
			gridColumnStart: getComputedStyle(first).gridColumnStart,
			distinctLefts: lefts.size,
		};
	})()`
	err = chromedp.Run(rctx, chromedp.Evaluate(measureJS, &res))
	require.NoError(t, err)
	require.NotZero(t, res.Cols)

	wantStart := (res.PastedIndex % res.Cols) + 1
	gotStart, convErr := strconv.Atoi(res.GridColumnStart)
	require.NoError(t, convErr, "grid-column-startが数値でない: %q", res.GridColumnStart)
	require.Equal(t, wantStart, gotStart,
		"先頭タイルのgrid-column-startがずれている: pastedIndex=%d cols=%d", res.PastedIndex, res.Cols)
	require.Equal(t, res.Cols, res.DistinctLefts,
		"タイルの左端の座標が列数どおりに揃っていない（横方向のずれ）: cols=%d distinctLefts=%d",
		res.Cols, res.DistinctLefts)
}

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

// --- Task: TestScrollPositionSurvivesReload ---
func TestScrollPositionSurvivesReload(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + 初回貼り付け待ち(Poll 10s) +
	//   履歴書き込み猶予のSleep(0.5s) + リロード後描画待ち(waitForTiles 10s) +
	//   スクロール復元待ち(Poll 10s) + 交差枚数待ち(Poll 10s) = 50.5s。
	// これらは同じrctxを共有しているため、外側が短いと合計より先に
	// 力尽きて「context deadline exceeded」という診断不能な失敗になる
	// （実測）。Evaluate・Reloadなど残りの実行時間の余裕を見て60秒とする。
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(scrollToPhotoJS(150), nil),
		chromedp.Poll(`famifo.pastedIndex() > 0`, nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err)

	var scrollBefore float64
	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollBefore))
	require.NoError(t, err)
	require.Greater(t, scrollBefore, 0.0)

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
	const intersectCountJS = `(() => {
		const vh = window.innerHeight;
		return [...document.querySelectorAll('#window .tile')].filter((t) => {
			const r = t.getBoundingClientRect();
			return r.bottom > 0 && r.top < vh;
		}).length;
	})()`

	// Chromeがスクロール位置を履歴エントリに書き込むまでの猶予。
	// 直後にreloadすると復元されないことがある（実測）。
	err = chromedp.Run(rctx, chromedp.Sleep(500*time.Millisecond))
	require.NoError(t, err)

	// Reloadは必ず単独のRun呼び出しにする。ナビゲーションを跨いだ直後の
	// アクションを同じRun呼び出しに混ぜると、古い実行コンテキストに
	// 束縛されたままタイムアウトまで固まることがあった（実測）。
	err = chromedp.Run(rctx, chromedp.Reload())
	require.NoError(t, err)

	err = chromedp.Run(rctx, waitForTiles(10*time.Second))
	require.NoError(t, err)

	// ブラウザのスクロール位置復元が効くまで待つ（history.scrollRestoration既定）。
	// Pollが失敗したときにも実測値を報告したいので、値はPollの後に読み直す
	// （下のintersectCountJSと同じ形。ここを素のrequire.NoErrorのままにすると、
	// 原因不明の"waiting for function failed: timeout"だけで落ちる
	// 到達不能寸前のアサーションになってしまう＝spec F4と同型の欠陥）。
	scrollPollErr := chromedp.Run(rctx, chromedp.Poll(pollScrollJS, nil, chromedp.WithPollingTimeout(10*time.Second)))

	var scrollGeom struct {
		ScrollTop float64 `json:"scrollTop"`
		RowH      float64 `json:"rowH"`
	}
	err = chromedp.Run(rctx, chromedp.Evaluate(`(() => {
		const win = document.querySelector('#window');
		const tile = win.querySelector('.tile');
		const gap = parseFloat(getComputedStyle(win).rowGap) || 0;
		const rowH = tile ? tile.getBoundingClientRect().height + gap : 0;
		return { scrollTop: famifo.scroller.scrollTop, rowH };
	})()`, &scrollGeom))
	require.NoError(t, err)

	scrollDiff := scrollGeom.ScrollTop - scrollBefore
	if scrollDiff < 0 {
		scrollDiff = -scrollDiff
	}
	require.NoErrorf(t, scrollPollErr,
		"リロード後のスクロール位置が復元前と%.1fpxずれている"+
			"（復元後=%.1fpx 復元前=%.1fpx 許容差=±%.1fpx=半行）。"+
			"復元処理が1行分ずれている、またはhistory.scrollRestorationが効いていない疑い",
		scrollDiff, scrollGeom.ScrollTop, scrollBefore, scrollGeom.RowH/2)

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
}

// --- Task: TestScrollAnchoredOnResize ---
func TestScrollAnchoredOnResize(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(scrollToPhotoJS(150), nil),
		chromedp.Poll(`famifo.pastedIndex() > 0`, nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err)

	const topIndexJS = `(() => {
		const win = document.querySelector('#window');
		const cols = getComputedStyle(win).gridTemplateColumns.split(' ').filter(Boolean).length;
		const tiles = [...win.querySelectorAll('.tile')];
		let within = 0;
		for (let i = 0; i < tiles.length; i++) {
			const r = tiles[i].getBoundingClientRect();
			if (r.bottom > 0) { within = i; break; }
		}
		return { cols, topIndex: famifo.pastedIndex() + within };
	})()`

	type snapshot struct {
		Cols     int `json:"cols"`
		TopIndex int `json:"topIndex"`
	}
	var before snapshot
	err = chromedp.Run(rctx, chromedp.Evaluate(topIndexJS, &before))
	require.NoError(t, err)
	require.NotZero(t, before.Cols)

	// 列数が変わる幅（スマホ幅のブレークポイント）へリサイズする。
	err = chromedp.Run(rctx, chromedp.EmulateViewport(375, 900))
	require.NoError(t, err)

	colsChangedJS := fmt.Sprintf(
		`getComputedStyle(document.querySelector('#window')).gridTemplateColumns.split(' ').filter(Boolean).length !== %d`,
		before.Cols)
	var after snapshot
	err = chromedp.Run(rctx,
		chromedp.Poll(colsChangedJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(topIndexJS, &after),
	)
	require.NoError(t, err)
	require.NotEqual(t, before.Cols, after.Cols, "リサイズしても列数が変わらなかった（前提条件を満たしていない）")

	maxCols := before.Cols
	if after.Cols > maxCols {
		maxCols = after.Cols
	}
	diff := after.TopIndex - before.TopIndex
	if diff < 0 {
		diff = -diff
	}
	require.LessOrEqual(t, diff, maxCols,
		"リサイズ前後で画面上端に見えていた写真の通し番号が1行分を超えてずれた: before=%d(cols=%d) after=%d(cols=%d)",
		before.TopIndex, before.Cols, after.TopIndex, after.Cols)
}

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
				// break しているので、1回のコールバックに複数回分の render の記録が
				// まとまって届いた場合は1回としか数えない。つまりこの数は下限。
				if (r.removedNodes.length > 0) { window.__repaints++; break; }
			}
		}).observe(win, { childList: true });
	})()`

	// 位相1の終状態。「一定時間 __repaints が増えていない」だけを静定とみなすと、
	// 塊の到着が遅れたときに塊1つ分だけ貼られた状態で先へ進んでしまう。すると
	// 続く1行のスクロールが塊境界を跨がず、このテスト全体が空虚になる（実測）。
	// 写真 testPageSize*2+30 番へスクロールした後に render() が貼るのは
	// 塊1以降のすべて（先頭の塊0だけが可視範囲から外れる）なので、
	// 貼られるタイル数は total - chunkSize で決まる。
	const allChunksPastedJS = `document.querySelectorAll('#window .tile').length === famifo.total - famifo.chunkSize`

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
		chromedp.Poll(allChunksPastedJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Poll(settledJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err)

	var afterScroll int
	err = chromedp.Run(rctx, chromedp.Evaluate(`window.__repaints`, &afterScroll))
	require.NoError(t, err)
	t.Logf("PROBE: 塊境界を跨いだ後の貼り替え回数=%d", afterScroll)

	// ごく普通のスクロールを1行分行う。この位置・この列数では、この1行で
	// 貼り付け範囲が1塊ぶん進む（下の NotEqual で前提として確認する）。
	const scrollOneRowJS = `(() => {
		const win = document.querySelector('#window');
		const r = win.querySelector('.tile').getBoundingClientRect();
		const gap = parseFloat(getComputedStyle(win).rowGap) || 0;
		famifo.scroller.scrollTop += r.height + gap;
	})()`
	var pastedBefore, pastedAfter int
	err = chromedp.Run(rctx,
		chromedp.Evaluate(`famifo.pastedIndex()`, &pastedBefore),
		chromedp.Evaluate(`window.__lastSeen = -1; window.__stable = 0;`, nil),
		chromedp.Evaluate(scrollOneRowJS, nil),
		chromedp.Poll(settledJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(`famifo.pastedIndex()`, &pastedAfter),
	)
	require.NoError(t, err)

	var afterPlain int
	err = chromedp.Run(rctx, chromedp.Evaluate(`window.__repaints`, &afterPlain))
	require.NoError(t, err)
	t.Logf("PROBE: 通常スクロール後の貼り替え回数=%d（貼り付け先頭 %d→%d）", afterPlain, pastedBefore, pastedAfter)

	// 前提の確認。この1行で貼り付け範囲が1塊ぶん進んでいなければ、検証したい
	// 「塊境界を跨ぐ貼り替え」に到達していない。到達しなければガードの有無で
	// 差が出ないため、黙って合格させずにここで落とす。
	require.NotEqualf(t, pastedBefore, pastedAfter,
		"1行分のスクロールで貼り付け範囲が変わらなかった（貼り付け先頭=%d のまま）。"+
			"塊境界を跨いでおらず、このテストの前提が崩れている",
		pastedBefore)

	// ここが本命の検出器。貼り替えが起きてよいのは、貼り付ける内容が
	// 変わったときだけ。範囲が1塊ぶん進んだのだから、貼り替えはちょうど1回。
	// 同じ内容の貼り直しが挟まれば2回以上になる。
	// 絶対回数ではなく「前後の差」を見るので、下の合体（後述）の影響を受けない。
	// ガードが無いときの余分な貼り替えは ResizeObserver 経由で、
	// レンダリングのステップ境界を跨いで別のコールバックとして配送されるため、
	// 合体して1回に潰れることがない。
	require.Equalf(t, afterScroll+1, afterPlain,
		"1行分のスクロールで、貼り付ける内容が変わっていないのにDOMが貼り替えられた: "+
			"スクロール前=%d 後=%d 期待=%d（貼り付け先頭 %d→%d）。"+
			"onResize の「列数もタイル高も変わっていなければ何もしない」ガードが効いていない疑い",
		afterScroll, afterPlain, afterScroll+1, pastedBefore, pastedAfter)

	// こちらは主検出器ではなく、貼り替えの暴走を捕まえるためのゆるい健全性
	// チェック。afterScroll は決定的な値ではない（MutationObserver の
	// コールバック合体により実際の render 回数の下限にしかならず、
	// スクロールバーの出入りによる正当な貼り替えも混ざる）ので、
	// 厳密な上限を課すと正しい実装でも落ちる。
	// 実測: 正しい実装を遅延なし・30ms・400ms で25回ずつ、計75回動かして
	// 2が30回、3が44回、4が1回。観測最大の倍の8を上限にした。
	// ガードを外したときにこの上限に引っかかるとは限らないが、それでよい。
	// その場合は上の相対不変条件が落とす。
	const repaintRunawayCeiling = 8
	require.LessOrEqualf(t, afterScroll, repaintRunawayCeiling,
		"塊境界を跨ぐスクロールで%d回も貼り替えている（貼り替えの暴走。"+
			"ResizeObserverのフィードバックループの疑い）",
		afterScroll)
}

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
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + ライトボックスが開く待ち(Poll 5s) +
	//   原寸画像デコード待ち(Poll 10s) = 25s。
	// ループ本体(steps=130回、各Poll上限5s)は理論上の最大では650sにも
	// なるが、実測では130ステップ全体が数秒で終わる（キー送りのたびに
	// 塊取得済みのURLを引くだけで、境界をまたぐ2回だけ先読み済みの
	// 次塊を使う。ネットワーク待ちはほぼ発生しない）。全ステップが
	// 揃って5秒ぎりぎりまでかかる状況は実装が壊れている場合であり、
	// その場合はループ内のrequireが最初の失敗でテストを止めるため、
	// 650s分の予算を常時確保する必要はない。だが数ステップが負荷で
	// 5秒近くかかっても「context deadline exceeded」に化けて
	// カスタムメッセージが読めなくならないよう、実測(数秒)の10倍以上
	// の余裕を見て90秒とする。
	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
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

// --- Task: TestScrubberReachesBothEnds ---
func TestScrubberReachesBothEnds(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
	)
	require.NoError(t, err)

	getScrubberRect := func() rect {
		t.Helper()
		var r rect
		err := chromedp.Run(rctx, chromedp.Evaluate(fmt.Sprintf(rectJS, `document.querySelector('#scrubber')`), &r))
		require.NoError(t, err)
		return r
	}

	// スクラバーは触られていない間は pointer-events:none で隠れている。
	// スクロールで一時的に visible にしてから掴む（実際のユーザー操作と同じ経路）。
	showScrubber := func(scrollTopExpr string) {
		t.Helper()
		err := chromedp.Run(rctx,
			chromedp.Evaluate(`famifo.scroller.scrollTop = `+scrollTopExpr, nil),
			chromedp.Poll(`document.querySelector('#scrubber').classList.contains('visible')`, nil,
				chromedp.WithPollingTimeout(5*time.Second)),
		)
		require.NoError(t, err)
	}

	drag := func(x, y0, y1 float64) {
		t.Helper()
		press := input.DispatchMouseEvent(input.MousePressed, x, y0).WithButton(input.Left).WithClickCount(1)
		move := input.DispatchMouseEvent(input.MouseMoved, x, y1).WithButton(input.Left).WithButtons(1)
		release := input.DispatchMouseEvent(input.MouseReleased, x, y1).WithButton(input.Left).WithClickCount(1)
		err := chromedp.Run(rctx, press, move, release)
		require.NoError(t, err)
	}

	var maxScroll float64
	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.maxScroll()`, &maxScroll))
	require.NoError(t, err)
	require.Greater(t, maxScroll, 0.0)

	// 下端までドラッグする。
	showScrubber(`50`)
	r := getScrubberRect()
	x := r.Left + r.Width/2
	drag(x, r.Top+5, r.Bottom-5)

	var scrollTop float64
	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollTop))
	require.NoError(t, err)
	require.InDeltaf(t, maxScroll, scrollTop, maxScroll*0.05+5,
		"下端へのドラッグでscrollTop(%.0f)がmaxScroll(%.0f)付近まで届かない", scrollTop, maxScroll)

	// 上端までドラッグする。
	showScrubber(`famifo.maxScroll() - 50`)
	r = getScrubberRect()
	x = r.Left + r.Width/2
	drag(x, r.Bottom-5, r.Top+5)

	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollTop))
	require.NoError(t, err)
	require.Lessf(t, scrollTop, maxScroll*0.05+5,
		"上端へのドラッグでscrollTop(%.0f)が0付近まで戻らない", scrollTop)
}

// --- Task: TestTileTapOpensLightbox ---
//
// 修正前は、スクラバーの目に見えない32px幅の帯が pointer-events:auto の
// ままだったため、右端の列のタイルの右5分の1をタップするとギャラリーの
// シークとして奪われてしまっていた（帯がタイル幅の約22%を覆っていたため）。
// この帯は「触られていないときは隠れている」のが正しい挙動なので、
// ページ読み込み直後の一時表示（1.5秒）が終わるのを待ってから検証する。
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

	var r rect
	var scrollBefore float64
	err = chromedp.Run(rctx,
		chromedp.Evaluate(`(() => {
			const win = document.querySelector('#window');
			const cols = getComputedStyle(win).gridTemplateColumns.split(' ').filter(Boolean).length;
			const tiles = [...win.querySelectorAll('.tile')];
			const b = tiles[cols - 1].getBoundingClientRect();
			return {Left:b.left, Top:b.top, Right:b.right, Bottom:b.bottom, Width:b.width, Height:b.height};
		})()`, &r),
		chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollBefore),
	)
	require.NoError(t, err)
	require.Greater(t, r.Width, 0.0, "右端タイルの矩形が取得できなかった")

	x := r.Right - r.Width*0.1 // 右5分の1のさらに中央寄り
	y := r.Top + r.Height/2

	err = chromedp.Run(rctx, chromedp.MouseClickXY(x, y))
	require.NoError(t, err)

	err = chromedp.Run(rctx, chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil,
		chromedp.WithPollingTimeout(3*time.Second)))
	require.NoError(t, err,
		"右端タイルの右5分の1をタップしてもライトボックスが開かなかった（スクラバーに奪われている疑い）")

	var scrollAfter float64
	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollAfter))
	require.NoError(t, err)
	require.Equal(t, scrollBefore, scrollAfter, "スクロール位置が変化した（スクラバーのシークが発生した）")
}
