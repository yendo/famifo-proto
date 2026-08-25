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

	var scrollBefore float64
	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollBefore))
	require.NoError(t, err)
	require.Greater(t, scrollBefore, 0.0)

	pollScrollJS := fmt.Sprintf(`Math.abs(famifo.scroller.scrollTop - %s) < 200`,
		strconv.FormatFloat(scrollBefore, 'f', -1, 64))
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
	err = chromedp.Run(rctx, chromedp.Poll(pollScrollJS, nil, chromedp.WithPollingTimeout(10*time.Second)))
	require.NoError(t, err)

	// スクロール位置の復元と、その位置に該当する塊の取得・描画は別のタイミングで
	// 起きる（描画側は非同期fetch待ち）。intersectCountJS自体が真になるまで
	// 待つことで、この競合を待ちきる。
	err = chromedp.Run(rctx, chromedp.Poll(intersectCountJS+" > 0", nil, chromedp.WithPollingTimeout(10*time.Second)))
	require.NoError(t, err)

	var intersecting int
	err = chromedp.Run(rctx, chromedp.Evaluate(intersectCountJS, &intersecting))
	require.NoError(t, err)
	require.Greater(t, intersecting, 0,
		"リロード後、可視範囲と交差するタイルが#windowに1枚も無い（空白のまま）")
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
// #windowはグリッドの行数が貼り付け枚数で変わるため、塊境界を跨ぐたびに
// ResizeObserverが発火する。以前はこれを無条件にrender()へつなげており、
// 列数もタイル高も変わっていないのにDOMを丸ごと張り替えていた
// （画像の再読み込み・スクロールのカクつきの原因）。ここでは、塊境界を跨いだ
// 直後にタイルへ印を付け、その後の通常スクロールで印が残るかを見る。
func TestNoRepaintOnPlainScroll(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		// 塊境界を跨ぐ、正当な貼り替えを1回起こす。
		chromedp.Evaluate(scrollToPhotoJS(150), nil),
		chromedp.Poll(`famifo.pastedIndex() > 0`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		// レイアウトが落ち着き、ResizeObserverが発火する猶予を与える。
		chromedp.Sleep(300*time.Millisecond),
		// 現在貼られているタイルに印を付ける。
		chromedp.Evaluate(`[...document.querySelectorAll('#window .tile')].forEach((t) => { t.dataset.probe = 'stamped'; })`, nil),
		// 不具合があれば、ここでResizeObserver由来の張り直しが起きる。
		chromedp.Sleep(500*time.Millisecond),
		// 同じ貼り付け範囲内にとどまる、ごく普通のスクロール。
		chromedp.Evaluate(`famifo.scroller.scrollTop += 40`, nil),
		chromedp.Sleep(300*time.Millisecond),
	)
	require.NoError(t, err)

	var res struct {
		Total   int `json:"total"`
		Stamped int `json:"stamped"`
	}
	err = chromedp.Run(rctx, chromedp.Evaluate(`(() => {
		const tiles = [...document.querySelectorAll('#window .tile')];
		return { total: tiles.length, stamped: tiles.filter((t) => t.dataset.probe === 'stamped').length };
	})()`, &res))
	require.NoError(t, err)

	require.Greater(t, res.Total, 0)
	require.Equal(t, res.Total, res.Stamped,
		"通常スクロールでDOMが再構築された（印の消えたタイルがある）: total=%d stamped=%d",
		res.Total, res.Stamped)
}

// --- Task: TestLightboxCrossesChunkBoundary ---
func TestLightboxCrossesChunkBoundary(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(5*time.Second)),
	)
	require.NoError(t, err)

	var firstSrc string
	err = chromedp.Run(rctx, chromedp.Evaluate(`document.querySelector('#lightbox img').src`, &firstSrc))
	require.NoError(t, err)
	require.NotEmpty(t, firstSrc)

	distinct := map[string]bool{firstSrc: true}
	prev := firstSrc
	// 塊(testPageSize枚)の境界を2回（testPageSize番目・testPageSize*2番目）
	// 跨ぐのに十分な回数。
	steps := testPageSize*2 + 10
	for i := 0; i < steps; i++ {
		var src string
		pollExpr := fmt.Sprintf(`document.querySelector('#lightbox img').src !== %s`, strconv.Quote(prev))
		err = chromedp.Run(rctx,
			chromedp.KeyEvent(kb.ArrowRight),
			chromedp.Poll(pollExpr, nil, chromedp.WithPollingTimeout(5*time.Second)),
			chromedp.Evaluate(`document.querySelector('#lightbox img').src`, &src),
		)
		require.NoErrorf(t, err, "ステップ%dでsrcが変化しなかった（境界での停止の疑い）: prev=%s", i, prev)
		distinct[src] = true
		prev = src
	}

	require.GreaterOrEqualf(t, len(distinct), 20,
		"%d回送っても20種類以上のsrcにならなかった: got=%d", steps, len(distinct))
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
