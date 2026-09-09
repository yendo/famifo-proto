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
package web_test

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/web"

	"github.com/yendo/famifo-proto/internal/index"
	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

const (
	// containerName は使い回すコンテナの固定名。前回異常終了した残骸を
	// 掃除できるように固定にしている。
	containerName   = "famifo-browser-test-chrome"
	dockerImage     = "chromedp/headless-shell:latest"
	debugVersionURL = "http://127.0.0.1:9222/json/version"

	testChunkSize = 60

	// deepScrollIndex は「複数の塊を跨いだ、十分に奥」の位置。
	// 塊を2つ跨いだうえで、境界のちょうど上に止まらないよう半端に足す。
	deepScrollIndex = testChunkSize*2 + 30
)

// testDayCounts は日ごとの枚数(新しい順)。1枚の日・数枚の日・列数を超える
// 大きい日を混ぜる。日ごとの枚数がそのままレイアウトを決めるため、以前の
// 「1日1枚」のテスト写真では横並びも行占有も検証できない。
// デスクトップ幅(1600px)は7列なので、8枚以上の日が行を占有する側になる。
var testDayCounts = []int{
	20, 1, 3, 1, 9, 2, 1, 5, 14, 1,
	1, 4, 30, 2, 1, 7, 1, 3, 25, 1,
	6, 1, 2, 11, 1, 1, 4, 18, 3, 1,
	20,
}

// testPhotoCount は testDayCounts の合計(200枚)。
var testPhotoCount = func() int {
	n := 0
	for _, c := range testDayCounts {
		n += c
	}
	return n
}()

// dayOfPhoto は通し番号iの写真が属する日を "2006-01-02" で返す。
func dayOfPhoto(i int) string {
	seen := 0
	for d, c := range testDayCounts {
		if i < seen+c {
			return newestDay.AddDate(0, 0, -d).Format("2006-01-02")
		}
		seen += c
	}
	return ""
}

// dayStartIndex は d 日目(0起点、新しい順)の先頭写真の通し番号。
func dayStartIndex(d int) int {
	n := 0
	for k := 0; k < d; k++ {
		n += testDayCounts[k]
	}
	return n
}

// newestDay は最も新しい日。prepareTestPhotos と上記2つが共有する。
var newestDay = time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)

// テスト写真の用意自体が壊れると、他のブラウザテストが理由不明で落ちる。先に押さえる。
func TestPhotosHaveMixedDaySizes(t *testing.T) {
	require.Equal(t, 200, testPhotoCount)
	small, big, single := 0, 0, 0
	for _, c := range testDayCounts {
		switch {
		case c == 1:
			single++
		case c <= 7: // デスクトップ(1600px)は7列。ここまでが横に並ぶ側
			small++
		default:
			big++
		}
	}
	require.Greater(t, single, 2, "a day with a single photo is needed, to check side-by-side days")
	require.Greater(t, small, 2, "a day with a few photos is needed")
	require.Greater(t, big, 2, "a day with more photos than columns is needed, to check a day taking a whole row")
	require.Equal(t, 0, dayStartIndex(0))
	require.Equal(t, testDayCounts[0], dayStartIndex(1))
	require.Equal(t, dayOfPhoto(0), dayOfPhoto(testDayCounts[0]-1), "falls on the same day")
	require.NotEqual(t, dayOfPhoto(0), dayOfPhoto(testDayCounts[0]), "the next one is another day")
}

// allocCtx はコンテナ内Chromeに接続したchromedpのアロケータcontext。
// 各テストはこれを親にタブ(chromedp.NewContext)を作る。ブラウザ本体は
// 全テストで1つを使い回す（コンテナ起動コストを1回に抑えるため）。
var allocCtx context.Context

// baseURL はテスト用に起動したアプリのURL（例: http://127.0.0.1:54321）。
var baseURL string

// testPhotoDir は prepareTestPhotos が写真を書いたディレクトリ。
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
				fmt.Fprintln(os.Stderr, "browser_test: recovered from a panic:", r)
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
		browserSkipReason = fmt.Sprintf("docker not found: %v", err)
		return noop, false
	}

	if err := exec.Command("docker", "image", "inspect", dockerImage).Run(); err != nil {
		browserSkipReason = fmt.Sprintf("no %s image (run `docker pull %s` and try again)", dockerImage, dockerImage)
		return noop, false
	}

	// クラッシュした前回実行の残骸があれば先に片付ける。
	_ = exec.Command("docker", "rm", "-f", containerName).Run()

	// /dev/shm はDocker既定の64MBでは足りない。このスイートでChromeが要求する
	// 共有メモリのピークは実測227MBあり、64MBだと天井に張り付く。確保に失敗した
	// 瞬間が net::ERR_INSUFFICIENT_RESOURCES、間に合わなかった瞬間が待ちの
	// タイムアウトになって、変更と無関係にテストが落ちる。
	// 実測の失敗率: 16MB/32MBは8回中8回、既定の64MBは27回中1回、2GBは10回中0回。
	// タイムアウト値の問題ではない。通常0.5秒で終わるテストが40秒の予算を
	// 使い切って落ちるので、値を伸ばしても停止している時間が伸びるだけ。
	runOut, err := exec.Command("docker", "run", "-d", "--rm", "--network", "host",
		"--shm-size", "2g", "--name", containerName, dockerImage).CombinedOutput()
	if err != nil {
		browserSkipReason = fmt.Sprintf("docker run failed: %v: %s", err, runOut)
		return noop, false
	}
	stopContainer := func() { _ = exec.Command("docker", "stop", containerName).Run() }

	if !waitForDebugger(20 * time.Second) {
		browserSkipReason = "timed out waiting for headless-shell to come up"
		stopContainer()
		return noop, false
	}

	tempDir, srv, closeStore, err := startTestApp()
	if err != nil {
		browserSkipReason = fmt.Sprintf("cannot start the app under test: %v", err)
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
		t.Fatalf("no environment for the browser tests; failing instead of skipping because FAMIFO_BROWSER_TESTS=required: %s", browserSkipReason)
	}
	t.Skipf("skipping: no environment for the browser tests: %s", browserSkipReason)
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
	if err := os.MkdirAll(photoDir, 0o755); err != nil {
		return tempDir, nil, nil, err
	}
	thumbs, err := thumb.NewProvider(filepath.Join(tempDir, "thumbs"))
	if err != nil {
		return tempDir, nil, nil, err
	}

	st, err := store.Open(filepath.Join(tempDir, "test.db"))
	if err != nil {
		return tempDir, nil, nil, err
	}

	if err := prepareTestPhotos(st, photoDir, thumbs); err != nil {
		st.Close()
		return tempDir, nil, nil, err
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	webSrv, err := web.NewServer(st, thumbs, log)
	if err != nil {
		st.Close()
		return tempDir, nil, nil, err
	}
	webSrv.SetChunkSize(testChunkSize)

	srv = httptest.NewServer(webSrv.Handler())
	return tempDir, srv, func() { st.Close() }, nil
}

// prepareTestPhotos は testDayCounts のとおりに実画像ファイルを生成して登録する。
// ユーザーの実ライブラリを読むと実行環境ごとに結果が変わりCIで再現できない
// ため、写真は常にこの場で作る。1日の中では撮影時刻を1分ずつ古くするので、
// 通し番号iがそのままギャラリー上の並び順(新しい順)に対応する。
func prepareTestPhotos(st *store.Store, photoDir string, thumbs *thumb.Provider) error {
	i := 0
	for d, count := range testDayCounts {
		for k := 0; k < count; k++ {
			name := fmt.Sprintf("p%04d.jpg", i)
			path := filepath.Join(photoDir, name)
			// 分単位で戻す。最大30枚なので日をまたがない。
			takenAt := newestDay.AddDate(0, 0, -d).Add(-time.Duration(k) * time.Minute)
			if err := writeTestPhoto(path, i, takenAt); err != nil {
				return err
			}
			i++
		}
	}
	if _, err := indexAll(st, photoDir, thumbs); err != nil {
		return err
	}
	return nil
}

// writeTestPhoto はテスト画像を書き、撮影日時をmtimeに焼く。
// テスト画像はEXIFを持たないので、取り込み時の撮影日時はmtimeから決まる。
func writeTestPhoto(path string, i int, takenAt time.Time) error {
	if err := writeTestJPEG(path, i); err != nil {
		return fmt.Errorf("cannot write the test image (%s): %w", path, err)
	}
	if err := os.Chtimes(path, takenAt, takenAt); err != nil {
		return fmt.Errorf("cannot set the times on the test image (%s): %w", path, err)
	}
	return nil
}

// indexAll は本番と同じ取り込み経路でコーパスをインデックスに載せる。
// 手でPhotoを組むと、Photoの構造が変わるたびにブラウザテストが巻き添えになる。
func indexAll(st *store.Store, photoDir string, thumbs *thumb.Provider) (index.Stats, error) {
	ix := index.New([]string{photoDir}, st, thumbs, 4,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return ix.Scan(context.Background())
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

// scrollToPhotoJS は写真indexが可視範囲の先頭に来るまでスクロールするJS式を返す。
//
// 以前はタイルの実測高から行を逆算していたが、行の高さが日ごとに変わるため
// 成立しない。実装が公開している「通し番号 → y」をそのまま呼ぶ。テストが
// レイアウト規則を写経すると、実装と一緒に間違えても気づけない。
// yForIndex が返すのはレイアウト座標なので、toDocY で文書座標に戻す。
func scrollToPhotoJS(index int) string {
	return fmt.Sprintf(`(() => {
		const L = famifo.current();
		famifo.scroller.scrollTop = famifo.toDocY(famifo.yForIndex(L, %d));
	})()`, index)
}

// expectedPhotoURLs はギャラリーの並び順どおりの原寸URLをn件返す。
//
// prepareTestPhotos は p0000.jpg から順に、testDayCounts のとおり日をまたぎながら
// takenAt を古くしていく（同じ日の中では1分ずつ）。日をまたぐタイミングは
// 一定ではないが、通し番号の順序自体は常に撮影時刻の新しい順と一致するので、
// 通し番号がそのまま並び順になる。サーバの ListRange を呼ばずにここで
// 組み立てるのは、クライアント側のオフセット計算をサーバと独立に検証する
// ため。両方が同じ計算を共有すると、ずれが打ち消し合って見えなくなる。
func expectedPhotoURLs(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		path := filepath.Join(testPhotoDir, fmt.Sprintf("p%04d.jpg", i))
		out[i] = "/file/" + media.IDFor(path)
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

// --- Task: TestInitialRenderFillsViewport ---
//
// 読み込み直後に、可視範囲が写真で埋まっているかを見る。
//
// 注意: サーバは gallery.html の中で最初の1塊（testChunkSize枚）を #window に
// 直接描画して返す。そのため「タイルが存在する」「下端がビューポートを超える」
// だけを見ると、app.js が一切動かなくても通ってしまう。仮想スクロールが
// 実際に働いて塊を追加で貼ったこと（tileCount > testChunkSize）まで見る。
func TestInitialRenderFillsViewport(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + 塊追加の貼り付け待ち(Poll 10s) +
	//   可視範囲のサムネイルデコード待ち(Poll 10s) = 30s。
	// これらは同じrctxを共有しているため、外側が短いと合計より先に
	// 力尽きて「context deadline exceeded」という診断不能な失敗になる
	// （実測）。Evaluateなど残りの実行時間の余裕を見て40秒とする。
	rctx, cancel := context.WithTimeout(ctx, 40*time.Second)
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
		fmt.Sprintf(`document.querySelectorAll('#window .tile').length > %d`, testChunkSize),
		nil, chromedp.WithPollingTimeout(10*time.Second)))

	err = chromedp.Run(rctx, chromedp.Evaluate(measureJS, &res))
	require.NoError(t, err)
	require.NotZero(t, res.Cols, "could not read the column count")
	require.Greater(t, res.RowH, 0.0, "could not read the row height")

	// app.js が動いていなければ、#window にはサーバが埋めた1塊しか無い。
	require.NoErrorf(t, pollErr,
		"the tile count in #window stays at %d. It is still the first chunk (%d tiles) the "+
			"server filled in, so virtual scrolling is pasting no further chunks; app.js looks dead",
		res.TileCount, testChunkSize)
	require.Greater(t, res.TileCount, testChunkSize)

	// 「ぎりぎり超えている」を成功とみなすと、ビューポート高やCSSの変更で
	// 正しい実装のまま落ちる。1行分の余裕を要求する。
	require.GreaterOrEqualf(t, res.LastBottom, res.ViewportH+res.RowH,
		"right after load the bottom of the last tile (%.0f) does not reach the viewport height (%.0f) plus a row (%.0f); there is a blank band",
		res.LastBottom, res.ViewportH, res.RowH)

	// Pollが失敗したときにも実測デコード枚数を報告したいので、エラーは
	// 受け取っておき、値はPollの後に読み直す（上のpollErrと同じ形）。
	decodedPollErr := chromedp.Run(rctx, chromedp.Poll(
		fmt.Sprintf(`%s >= %d`, decodedInViewJS, res.Cols), nil,
		chromedp.WithPollingTimeout(10*time.Second)))
	var decoded int
	err = chromedp.Run(rctx, chromedp.Evaluate(decodedInViewJS, &decoded))
	require.NoError(t, err)
	require.NoErrorf(t, decodedPollErr,
		"only %d thumbnails in view are decoded, one row (%d) expected; the images look unrendered",
		decoded, res.Cols)
	require.GreaterOrEqual(t, decoded, res.Cols)
}

// --- Task: TestScrollPositionSurvivesReload ---
//
// 復元先を掛け算で求めると、ここが落ちる。かつては
// Math.floor(topIndex / cols) * rowH で「何行目か」を出していたが、
// 行の高さは日ごとに変わる（1枚の日も、行を占有する大きい日もある）ため
// この式は成立しない。復元先は yForIndex で通し番号から引く。
//
// アンカーに貼り付け範囲の先頭（かつての pastedIndex / pasted.from）を
// 使うのも同じ理由で駄目である。貼り付けは OVERSCAN のぶん可視範囲より
// 手前から始まるので、先頭を復元先にすると数行ぶん上に着地する。実装が
// 公開するのは範囲（pastedRange）であって先頭ではない。
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
		chromedp.Evaluate(scrollToPhotoJS(deepScrollIndex), nil),
		chromedp.Poll(`famifo.pastedRange().from > 0`, nil, chromedp.WithPollingTimeout(10*time.Second)),
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
		"the scroll position after the reload is off by %.1fpx "+
			"(restored=%.1fpx before=%.1fpx tolerance=±%.1fpx, half a row). "+
			"Either the restore is a row out, or history.scrollRestoration is not taking effect",
		scrollDiff, scrollGeom.ScrollTop, scrollBefore, scrollGeom.RowH/2)

	// リロード後の列数を測る。可視範囲に何枚あるべきかはここから決まる。
	var cols int
	err = chromedp.Run(rctx, chromedp.Evaluate(
		`getComputedStyle(document.querySelector('#window')).gridTemplateColumns.split(' ').filter(Boolean).length`, &cols))
	require.NoError(t, err)
	require.NotZero(t, cols, "could not read the column count after the reload")

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
		"only %d tiles intersect the viewport after the reload (%d expected, %d columns x 2 rows). "+
			"The translateY that lines the window up looks ineffective",
		intersecting, wantIntersecting, cols)
}

// --- Task: TestScrollAnchoredOnResize ---
func TestScrollAnchoredOnResize(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + 初回貼り付け待ち(Poll 10s) +
	//   リサイズ後の列数変化待ち(Poll 10s) = 30s。
	// これらは同じrctxを共有しているため、外側が短いと合計より先に
	// 力尽きて「context deadline exceeded」という診断不能な失敗になる
	// （実測）。Evaluateなど残りの実行時間の余裕を見て40秒とする。
	rctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(scrollToPhotoJS(deepScrollIndex), nil),
		chromedp.Poll(`famifo.pastedRange().from > 0`, nil, chromedp.WithPollingTimeout(10*time.Second)),
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
		return { cols, topIndex: famifo.pastedRange().from + within };
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
	// Pollが失敗したときにも実測値(before/after)を報告したいので、エラーは
	// 受け取っておき、値はPollの後に読み直す（他のpollErrと同じ形）。
	// 素のrequire.NoErrorのままにすると、Pollがタイムアウトしたとき
	// "waiting for function failed: timeout" だけで落ち、それを説明する
	// はずの「列数が変わらなかった」というメッセージには到達しない。
	// 逆にそこへ到達している時点でPollは成功しており、その下の
	// NotEqualは恒真式になってしまう（実測。F4/F8と同型の欠陥だった）。
	colsPollErr := chromedp.Run(rctx, chromedp.Poll(colsChangedJS, nil, chromedp.WithPollingTimeout(10*time.Second)))
	var after snapshot
	err = chromedp.Run(rctx, chromedp.Evaluate(topIndexJS, &after))
	require.NoError(t, err)
	require.NoErrorf(t, colsPollErr,
		"the column count did not change across the resize, so the premise does not hold: before=%d after=%d",
		before.Cols, after.Cols)

	maxCols := before.Cols
	if after.Cols > maxCols {
		maxCols = after.Cols
	}
	diff := after.TopIndex - before.TopIndex
	if diff < 0 {
		diff = -diff
	}
	require.LessOrEqual(t, diff, maxCols,
		"the index of the photo at the top of the screen moved by more than a row across the resize: before=%d(cols=%d) after=%d(cols=%d)",
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
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + 貼り付け範囲静定待ち(Poll 10s) +
	//   貼り替え静定待ち(Poll 10s)×2（塊境界越えの後と、通常スクロール後の
	//   2箇所） = 40s。
	// これらは同じrctxを共有しているため、外側が短いと合計より先に
	// 力尽きて「context deadline exceeded」という診断不能な失敗になる
	// （実測）。Evaluateなど残りの実行時間の余裕を見て60秒とする。
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// スクロールを始める前に観測を仕掛ける。塊境界を跨ぐ貼り替えそのものを
	// 数えたいので、跨いだ後に仕掛けるのでは遅い。
	const installObserverJS = `(() => {
		window.__repaints = 0;
		const win = document.querySelector('#window');
		new MutationObserver((records) => {
			for (const r of records) {
				// 貼り付け範囲が進むと、#window の直下から日カードが出入りする。
				// タイルの追加のみ（あり得ないが）を貼り替えと数えないため区別する。
				// break しているので、1回のコールバックに複数回分の render の記録が
				// まとまって届いた場合は1回としか数えない。つまりこの数は下限。
				if (r.removedNodes.length > 0) { window.__repaints++; break; }
			}
		}).observe(win, { childList: true });
	})()`

	// 貼り付け範囲が動かなくなったことを静定とみなす。
	//
	// 以前は「貼り付けタイル数 == total - chunkSize」で静定を判定していたが、
	// この等式は貼り付け窓が最終行まで届くことに依存しており、テスト写真や
	// viewport を変えると無言のPollタイムアウトで落ちる（元のコメント参照）。
	// 総枚数から逆算するのをやめれば、その脆さごと消える。
	// 「範囲が変わらない」だけでは、render()が塊の取得待ちで早期returnして
	// pastedが初期値(from=0)のまま固まっているケースを区別できない。
	// chromedp.Pollの既定間隔(約16ms)では3回連続で約64msしか経っておらず、
	// 混雑したDocker上ではdeepScrollIndex(150番)ぶんのfetchがそれより
	// 長くかかりうる。そうなると「塊境界を2つ跨ぐ」という前提が満たされない
	// まま先へ進み、遅れて届いた描画が再描画計測の窓に入り込んで、
	// 見当違いの再描画回数の失敗として現れる（実測）。範囲が実際に
	// 最初の塊を超えて進んだこと（from >= chunkSize）も併せて要求する。
	const rangeSettledJS = `(() => {
		const r = famifo.pastedRange();
		const k = r.from + ':' + r.to;
		if (window.__lastRange !== k) {
			window.__lastRange = k;
			window.__rangeStable = 0;
			return false;
		}
		return r.from >= famifo.chunkSize && (window.__rangeStable = (window.__rangeStable || 0) + 1) >= 3;
	})()`

	// スクロールが落ち着いたか（__repaintsが3フレーム連続で増えていないか、
	// 60fpsなら約50ms）を見る。固定 sleep で「もう終わっただろう」と決め打ちしない。
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
		chromedp.Evaluate(scrollToPhotoJS(deepScrollIndex), nil),
		chromedp.Poll(rangeSettledJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err)

	err = chromedp.Run(rctx, chromedp.Poll(settledJS, nil, chromedp.WithPollingTimeout(10*time.Second)))
	require.NoError(t, err)

	var afterScroll int
	err = chromedp.Run(rctx, chromedp.Evaluate(`window.__repaints`, &afterScroll))
	require.NoError(t, err)
	t.Logf("PROBE: repaints after crossing a chunk boundary=%d", afterScroll)

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
		chromedp.Evaluate(`famifo.pastedRange().from`, &pastedBefore),
		chromedp.Evaluate(`window.__lastSeen = -1; window.__stable = 0;`, nil),
		chromedp.Evaluate(scrollOneRowJS, nil),
		chromedp.Poll(settledJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(`famifo.pastedRange().from`, &pastedAfter),
	)
	require.NoError(t, err)

	var afterPlain int
	err = chromedp.Run(rctx, chromedp.Evaluate(`window.__repaints`, &afterPlain))
	require.NoError(t, err)
	t.Logf("PROBE: repaints after a plain scroll=%d (paste start %d->%d)", afterPlain, pastedBefore, pastedAfter)

	// 前提の確認。この1行で貼り付け範囲が1塊ぶん進んでいなければ、検証したい
	// 「塊境界を跨ぐ貼り替え」に到達していない。到達しなければガードの有無で
	// 差が出ないため、黙って合格させずにここで落とす。
	require.NotEqualf(t, pastedBefore, pastedAfter,
		"a one-row scroll left the pasted range unchanged (paste start still %d). "+
			"No chunk boundary was crossed, so the premise of this test does not hold",
		pastedBefore)

	// ここが本命の検出器。貼り替えが起きてよいのは、貼り付ける内容が
	// 変わったときだけ。範囲が1塊ぶん進んだのだから、貼り替えはちょうど1回。
	//
	// 限界: render() が innerHTML の全置換をやめて差分適用になったため、
	// 「同じ内容での貼り直し」はDOMを何も変えず、この計測器には現れない。
	// つまり下のガードの回帰は、いまはここでは捕まらない（差分適用では
	// 貼り直しても実害がほぼ無いので、そのぶん危険度も下がっている）。
	// 捕まえたければ、data-i の書き戻し（render の最後で必ず起きる）を
	// attributeFilter で観測する形に変える必要がある。
	// 絶対回数ではなく「前後の差」を見るので、下の合体（後述）の影響を受けない。
	// ガードが無いときの余分な貼り替えは ResizeObserver 経由で、
	// レンダリングのステップ境界を跨いで別のコールバックとして配送されるため、
	// 合体して1回に潰れることがない。
	require.Equalf(t, afterScroll+1, afterPlain,
		"a one-row scroll repainted the DOM even though the content to paste did not change: "+
			"before=%d after=%d want=%d (paste start %d->%d). "+
			"The onResize guard that does nothing while columns and tile height are unchanged looks ineffective",
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
		"a scroll across a chunk boundary repainted %d times; repaints are running away, "+
			"which looks like a ResizeObserver feedback loop",
		afterScroll)
}

// --- Task: TestTilesSurviveAPlainScroll ---
//
// 1行スクロールしたあとも、引き続き見えている写真のタイルが「同じDOM要素」の
// まま残っているかを見る。
//
// iPhone / iPad で、スクロール中に写真の表示領域全体が一瞬真っ黒になる問題の
// 再発防止テスト。原因は render() が可視範囲のDOMを毎回まるごと作り直して
// いたこと。WebKitは内容と寸法が同時に変わった合成レイヤーのバッキングストアを
// 捨てて描き直すため、再ペイントが間に合わないフレームで背景色(--bg)が露出する。
//
// 症状そのもの（黒フレーム）はBlinkでは再現しない。Blinkは新しいタイルの
// ラスタライズが終わるまで古い内容を描き続けるためである。したがってヘッドレス
// Chromeで観測できるのは症状ではなく原因のほうで、このテストは「残っている写真の
// 要素が使い回されているか」だけを見る。
//
// 同一性の目印にはDOM要素のエキスパンドプロパティを使う。属性ではないので
// HTMLの文字列には現れず、要素が作り直されれば必ず消える。写真の照合には
// data-full（写真ごとに一意なURL）を使う。id属性の付け方にテストが依存しない
// ようにするため。
func TestTilesSurviveAPlainScroll(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + 貼り付け範囲の静定待ち(Poll 10s) +
	//   スクロール後の静定待ち(Poll 10s) = 30s。
	// Evaluateなど残りの実行時間の余裕を見て60秒とする。
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// 深い位置へ飛ばした直後は塊の取得が続くため、範囲が落ち着く前に目印を
	// 付けても意味がない。範囲が3回連続で変わらないことを静定とみなす。
	// 最初の塊を超えて進んだこと(from >= chunkSize)も併せて要求する。
	// 取得待ちで render() が早期returnし、pastedが初期値のまま固まっている
	// ケースと区別できないため。
	const rangeSettledJS = `(() => {
		const r = famifo.pastedRange();
		const k = r.from + ':' + r.to;
		if (window.__k !== k) { window.__k = k; window.__n = 0; return false; }
		return r.from >= famifo.chunkSize && (window.__n = (window.__n || 0) + 1) >= 3;
	})()`

	// いま貼られているタイルに目印を付け、写っている写真を控える。
	const markJS = `(() => {
		const tiles = [...document.querySelectorAll('#window .tile')];
		window.__before = tiles.map((t) => t.dataset.full);
		for (const t of tiles) t.__kept = true;
		window.__pastedFrom = famifo.pastedRange().from;
		return tiles.length;
	})()`

	const scrollOneRowJS = `(() => {
		const win = document.querySelector('#window');
		const r = win.querySelector('.tile').getBoundingClientRect();
		const gap = parseFloat(getComputedStyle(win).rowGap) || 0;
		famifo.scroller.scrollTop += r.height + gap;
	})()`

	// 貼り付け範囲が実際に動き、そのうえで落ち着くまで待つ。動いたことまで
	// 見ないと、まだ貼り替えが起きていない状態で使い回しを数えてしまい、
	// 実装が壊れていても全件が「残っている」と出て素通りする。
	const movedAndSettledJS = `(() => {
		const r = famifo.pastedRange();
		if (r.from === window.__pastedFrom) return false;
		const k = r.from + ':' + r.to;
		if (window.__k2 !== k) { window.__k2 = k; window.__n2 = 0; return false; }
		return (window.__n2 = (window.__n2 || 0) + 1) >= 3;
	})()`

	// スクロール後も見えている写真について、要素が使い回されたかを数える。
	const countJS = `(() => {
		const before = new Set(window.__before);
		const tiles = [...document.querySelectorAll('#window .tile')];
		let common = 0;
		let reused = 0;
		for (const t of tiles) {
			if (!before.has(t.dataset.full)) continue;
			common++;
			if (t.__kept) reused++;
		}
		return { Common: common, Reused: reused, Total: tiles.length };
	})()`

	var marked int
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(scrollToPhotoJS(deepScrollIndex), nil),
		chromedp.Poll(rangeSettledJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(markJS, &marked),
	)
	require.NoError(t, err)
	require.Greater(t, marked, 0, "no tile is pasted at the point the markers go on")

	var got struct {
		Common, Reused, Total int
	}
	err = chromedp.Run(rctx,
		chromedp.Evaluate(scrollOneRowJS, nil),
		chromedp.Poll(movedAndSettledJS, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(countJS, &got),
	)
	require.NoError(t, err)
	t.Logf("PROBE: marked tiles=%d tiles after the scroll=%d "+
		"of which photos still in view=%d elements kept=%d",
		marked, got.Total, got.Common, got.Reused)

	// 前提の確認。1行進んだだけなら大半の写真は見えたままのはず。ここが0なら
	// 貼り付け範囲が丸ごと入れ替わっており、使い回しの有無を問う場面に
	// 到達していない。
	require.Greater(t, got.Common, 0,
		"a one-row scroll left no photo still in view, so the premise of this test does not hold")

	require.Equalf(t, got.Common, got.Reused,
		"of the %d photos still in view only %d kept their DOM element. "+
			"render() looks like it rebuilds the DOM for the visible range, "+
			"which is what makes the photos flash black on WebKit",
		got.Common, got.Reused)
}

// --- Task: TestLightboxCrossesChunkBoundary ---
//
// ライトボックスの送りが塊(testChunkSize枚)の境界で止まらないこと、
// そして境界の継ぎ目で写真がずれないことを見る。
//
// 「srcが前回と変わったか」だけでは、2塊目以降で常に1枚ずれた写真を
// 返す off-by-one を検出できない（実測でPASSした）。期待される並びと
// 完全一致で突き合わせる。
//
// 現在位置の持ち方をDOMに戻すと、ここが落ちる。かつては表示中の写真を
// タイル一覧から探して数えていた（tiles().indexOf や
// parentElement.querySelectorAll による数え上げ）が、その方式は捨てた。
// 仮想スクロールは可視範囲の外のタイルを捨てるので、送りが塊の境界を
// 跨いだ時点で「一覧の中の位置」が意味を失う。日ごとのカードに入って
// からは、親要素を数える方式も同様に成立しない。現在位置は通し番号で
// 持ち、URLは urlAt で引く。
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

	// 塊の境界を2回（testChunkSize番目・testChunkSize*2番目）跨ぐのに十分な回数。
	steps := testChunkSize*2 + 10
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
	require.NoError(t, err, "the full-size image in the lightbox was never decoded, so nothing is displayed")

	// getAttribute('src') は絶対URLに解決されないので、テンプレートが書いた
	// 相対パスとそのまま突き合わせられる。
	const srcAttrJS = `document.querySelector('#lightbox img').getAttribute('src')`

	var got string
	err = chromedp.Run(rctx, chromedp.Evaluate(srcAttrJS, &got))
	require.NoError(t, err)
	require.Equalf(t, want[0], got,
		"the first photo opened is not the first one (index 0): got=%s want=%s", got, want[0])

	prev := got
	for i := 1; i <= steps; i++ {
		pollExpr := fmt.Sprintf(`%s !== %s`, srcAttrJS, strconv.Quote(prev))
		err = chromedp.Run(rctx,
			chromedp.KeyEvent(kb.ArrowRight),
			chromedp.Poll(pollExpr, nil, chromedp.WithPollingTimeout(5*time.Second)),
			chromedp.Evaluate(srcAttrJS, &got),
		)
		require.NoErrorf(t, err,
			"src did not change at photo %d; it looks stuck at the chunk boundary of %d: prev=%s",
			i, testChunkSize, prev)
		require.Equalf(t, want[i], got,
			"photo %d is not the expected one; the seam at the chunk boundary of %d looks off: got=%s want=%s",
			i, testChunkSize, got, want[i])
		prev = got
	}
}

// swipeActions は #lightbox の上を1本指でなぞるアクション列を返す。
//
// CDPの決まりで touchEnd は点を持てず、離した位置は直前の touchMove が
// 持っていた座標から決まる。app.js は touchmove を購読していないが、
// touchend で読む changedTouches の座標をここで決めるために3回に分けて送る。
func swipeActions(fromX, fromY, toX, toY float64) []chromedp.Action {
	at := func(x, y float64) []*input.TouchPoint {
		return []*input.TouchPoint{{X: x, Y: y}}
	}
	return []chromedp.Action{
		input.DispatchTouchEvent(input.TouchStart, at(fromX, fromY)),
		input.DispatchTouchEvent(input.TouchMove, at(toX, toY)),
		input.DispatchTouchEvent(input.TouchEnd, []*input.TouchPoint{}),
	}
}

// --- Task: TestLightboxSwipeNavigatesAndCloses ---
//
// スワイプはこのスイートで唯一タッチイベントを使うテストである。他の操作は
// マウス(input.DispatchMouseEvent)とキーボード(kb.ArrowRight)で足りるが、
// 左右送りと下スワイプで閉じる経路だけは touchstart/touchend でしか通らない。
// これが無いと、app.js のスワイプ実装が丸ごと消えても何も落ちない。
//
// 移動量は app.js の閾値（SWIPE_X=50px, SWIPE_Y=80px）を確実に超え、かつ
// 主たる軸の移動が他方より大きくなるようにとる。実装は「横が閾値超えかつ
// 縦より大きい」で送り、「下が閾値超えかつ横より大きい」で閉じるため、
// 斜めに振るとどちらとも解釈されうる。
func TestLightboxSwipeNavigatesAndCloses(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + ライトボックスが開く待ち(Poll 5s) +
	//   スワイプ後のsrc変化待ち(Poll 5s)×2 + 閉じる待ち(Poll 5s) = 30s。
	// これらは同じrctxを共有しているため、外側が短いと合計より先に力尽きて
	// 「context deadline exceeded」という診断不能な失敗になる。Evaluateなど
	// 残りの実行時間の余裕を見て、他のテストと揃えて60秒とする。
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	want := expectedPhotoURLs(2)

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(800, 1000),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
	)
	require.NoError(t, err)

	const srcAttrJS = `document.querySelector('#lightbox img').getAttribute('src')`

	var got string
	err = chromedp.Run(rctx, chromedp.Evaluate(srcAttrJS, &got))
	require.NoError(t, err)
	require.Equalf(t, want[0], got,
		"the first photo opened is not the first one (index 0): got=%s want=%s", got, want[0])

	// 左スワイプ = 次の写真。
	swipeTo := func(t *testing.T, fromX, fromY, toX, toY float64, msg string) {
		t.Helper()
		prev := got
		require.NoError(t, chromedp.Run(rctx, swipeActions(fromX, fromY, toX, toY)...))
		err := chromedp.Run(rctx, chromedp.Poll(
			fmt.Sprintf(`%s !== %s`, srcAttrJS, strconv.Quote(prev)), nil,
			chromedp.WithPollingTimeout(5*time.Second)))
		require.NoErrorf(t, err, "%s (src stayed at %s)", msg, prev)
		require.NoError(t, chromedp.Run(rctx, chromedp.Evaluate(srcAttrJS, &got)))
	}

	swipeTo(t, 400, 500, 200, 500, "a swipe left did not move to the next photo")
	require.Equalf(t, want[1], got,
		"a swipe left did not land on the next photo: got=%s want=%s", got, want[1])

	// 右スワイプ = 前の写真。往復して元の写真に戻ることまで見る。
	// 送りと戻りで別々の分岐を通るので、片方だけ壊れていても気づける。
	swipeTo(t, 200, 500, 400, 500, "a swipe right did not move back to the previous photo")
	require.Equalf(t, want[0], got,
		"a swipe right did not land back on the original photo: got=%s want=%s", got, want[0])

	// 下スワイプ = 閉じる。閉じるのは hidden 属性なので、srcではなくそちらを見る。
	require.NoError(t, chromedp.Run(rctx, swipeActions(400, 300, 400, 500)...))
	err = chromedp.Run(rctx, chromedp.Poll(`document.querySelector('#lightbox').hidden`, nil,
		chromedp.WithPollingTimeout(5*time.Second)))
	require.NoError(t, err, "a swipe down did not close the lightbox")
}

// --- Task: TestScrubberReachesBothEnds ---
//
// つまみの位置から日を引くのに比例計算を使うと、ここが落ちる。かつては
// frac * famifo.total で「全体の何割の位置か」から通し番号を出していたが、
// 行の高さが日ごとに変わった時点でこの比例関係は成立しない。位置から日を
// 引くのは dayAtY の仕事である。
//
// 日ごとの表もサーバに聞きに行かない。かつての /dates エンドポイントは
// 廃止し、ページに埋め込んだ daygroups を読む。
func TestScrubberReachesBothEnds(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + スクラバー表示待ち(Poll 5s)×2 +
	//   ドラッグ後の静定待ち(Poll 5s)×2 = 30s。
	// これらは同じrctxを共有しているため、外側が短いと合計より先に
	// 力尽きて「context deadline exceeded」という診断不能な失敗になる
	// （Task 7 の教訓）。Evaluate・マウスイベントなど残りの実行時間の
	// 余裕を見て45秒とする。
	rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
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
		require.NoError(t, err, "the scroll position never settled after the drag")
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
		"dragging to the bottom leaves scrollTop (%.0f) short of maxScroll (%.0f)", scrollTop, maxScroll)

	// 上端までドラッグする。
	showScrubber(`famifo.maxScroll() - 50`)
	r = getScrubberRect()
	x = r.Left + r.Width/2
	drag(x, r.Bottom-5, r.Top+5)

	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollTop))
	require.NoError(t, err)
	require.Lessf(t, scrollTop, maxScroll*0.05+5,
		"dragging to the top leaves scrollTop (%.0f) short of 0", scrollTop)
}

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
//
// タップ点導出の根拠（Step 1 実測）:
// #scrubber は初期HTMLでは hidden 属性付き（display:none）だが、app.jsが
// ロード直後に bar.hidden = false を実行するため、このテストが計測する時点
// （タイル描画済み・visibleクラス消滅後）では既に display:none ではない
// （実測: display="block"）。よって getBoundingClientRect() をそのまま使える。
// また、ブリーフが想定していた「window.innerWidth - width」でscrubLeftを
// 組み立てる方式は、縦スクロールバーが出る環境で実際のフレーム右端とずれる
// （当時の実測: window.innerWidth=800 に対し #scrubber の右端は785で、
// document.documentElement.clientWidth=785 と一致）。その後 app.css で純正
// スクロールバーを消したため、現在はどちらも800で一致する（実測）。計算で
// 組み立てる方式に戻すと同じ罠を踏むので、#scrubber の
// getBoundingClientRect() を直接使う。
func TestTileTapOpensLightbox(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	// このRun群の中でタイムアウト付きに待つ箇所の合計:
	//   初回描画待ち(waitForTiles 10s) + スクラバー一時表示の消滅待ち(Poll 5s) +
	//   タップ後のライトボックスが開く待ち(Poll 3s) = 18s。
	// これらは同じrctxを共有しているため、外側が短いと合計より先に
	// 力尽きて「context deadline exceeded」という診断不能な失敗になる
	// （実測）。Evaluateなど残りの実行時間の余裕を見て、他のテストと
	// 揃えて30秒とする。
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	// #scrubberはこの時点で hidden(display:none) ではないことをStep 1で確認済み
	// なので、getBoundingClientRect() をそのまま使う（実際にpointer-eventsを
	// 持つ範囲もこの矩形と一致する）。
	var m struct {
		Tile  rect `json:"tile"`
		Scrub rect `json:"scrub"`
	}
	measureJS := `(() => {
		const win = document.querySelector('#window');
		const cols = getComputedStyle(win).gridTemplateColumns.split(' ').filter(Boolean).length;
		const tb = win.querySelectorAll('.tile')[cols - 1].getBoundingClientRect();
		const sb = document.querySelector('#scrubber').getBoundingClientRect();
		const toRect = (b) => ({Left:b.left, Top:b.top, Right:b.right, Bottom:b.bottom, Width:b.width, Height:b.height});
		return { tile: toRect(tb), scrub: toRect(sb) };
	})()`

	var scrollBefore float64
	err = chromedp.Run(rctx,
		chromedp.Evaluate(measureJS, &m),
		chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollBefore),
	)
	require.NoError(t, err)
	require.Greater(t, m.Tile.Width, 0.0, "could not read the rect of the rightmost tile")
	require.Greater(t, m.Scrub.Width, 0.0, "could not read the rect of the scrubber band; it looks measured while still display:none")

	// タイルとスクラバー帯の重なり。ここが空なら、そもそもこの不具合は
	// 再現しない条件なので、静かに通さず前提条件の不成立として止める。
	overlapLeft := m.Tile.Left
	if m.Scrub.Left > overlapLeft {
		overlapLeft = m.Scrub.Left
	}
	overlapRight := m.Tile.Right
	if m.Scrub.Right < overlapRight {
		overlapRight = m.Scrub.Right
	}
	overlapTop := m.Tile.Top
	if m.Scrub.Top > overlapTop {
		overlapTop = m.Scrub.Top
	}
	overlapBottom := m.Tile.Bottom
	if m.Scrub.Bottom < overlapBottom {
		overlapBottom = m.Scrub.Bottom
	}
	if overlapRight-overlapLeft <= 0 || overlapBottom-overlapTop <= 0 {
		t.Fatalf("the rightmost tile (x:%.0f-%.0f, y:%.0f-%.0f) and the scrubber band (x:%.0f-%.0f, y:%.0f-%.0f) "+
			"do not overlap, so this bug cannot reproduce under these conditions. "+
			"Check the viewport width, or the width/top of .scrubber",
			m.Tile.Left, m.Tile.Right, m.Tile.Top, m.Tile.Bottom,
			m.Scrub.Left, m.Scrub.Right, m.Scrub.Top, m.Scrub.Bottom)
	}
	t.Logf("overlap width=%.1fpx overlap height=%.1fpx (tile right edge %.0f, band left edge %.0f)",
		overlapRight-overlapLeft, overlapBottom-overlapTop, m.Tile.Right, m.Scrub.Left)

	x := (overlapLeft + overlapRight) / 2
	y := (overlapTop + overlapBottom) / 2

	err = chromedp.Run(rctx, chromedp.MouseClickXY(x, y))
	require.NoError(t, err)

	pollErr := chromedp.Run(rctx, chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil,
		chromedp.WithPollingTimeout(3*time.Second)))
	require.NoErrorf(t, pollErr,
		"tapping the tile at (x=%.0f, y=%.0f), where the scrubber band overlaps it, did not open "+
			"the lightbox; the scrubber looks like it is taking the tap", x, y)

	var scrollAfter float64
	err = chromedp.Run(rctx, chromedp.Evaluate(`famifo.scroller.scrollTop`, &scrollAfter))
	require.NoError(t, err)
	require.Equal(t, scrollBefore, scrollAfter, "the scroll position changed, so the scrubber seeked")
}

// --- Task: レイアウト計算 ---
//
// 貪欲詰めと二分探索は純粋関数だが、この repo にはJSを単体テストする手段が
// 無い（Node を足さない方針のため）。実ブラウザ上で関数を直接呼んで検証する。

// layoutEntry は famifo.layout が返す entries の1要素。
type layoutEntry struct {
	D     string  `json:"d"`
	Y     float64 `json:"y"`
	H     float64 `json:"h"`
	Start int     `json:"start"`
	N     int     `json:"n"`
	Span  int     `json:"span"`
	Col   int     `json:"col"`
	Rows  int     `json:"rows"`
}

type layoutResult struct {
	Entries []layoutEntry `json:"entries"`
	Height  float64       `json:"height"`
}

// evalLayout は famifo.layout をブラウザ上で呼ぶ。
// tileH=100, labelH=20, gap=4 に固定して、期待値を手計算できるようにする。
func evalLayout(t *testing.T, ctx context.Context, groupsJSON string, cols int) layoutResult {
	t.Helper()
	var got layoutResult
	err := chromedp.Run(ctx, chromedp.Evaluate(
		fmt.Sprintf(`famifo.layout(%s, %d, 100, 20, 4)`, groupsJSON, cols), &got))
	require.NoError(t, err)
	return got
}

func TestLayoutPacksDaysThatFitOneRow(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	// 列数6。1枚 + 4枚 = 5列で同じストライプに載り、次の3枚は入らず次へ。
	got := evalLayout(t, ctx,
		`[{d:"2026-02-08",n:1},{d:"2026-02-03",n:4},{d:"2026-01-20",n:3}]`, 6)

	require.Len(t, got.Entries, 3)

	require.Equal(t, 0, got.Entries[0].Col)
	require.Equal(t, 1, got.Entries[0].Span)
	require.Equal(t, float64(0), got.Entries[0].Y)
	require.Equal(t, 0, got.Entries[0].Start)

	require.Equal(t, 1, got.Entries[1].Col, "sits to the right of the single-photo day")
	require.Equal(t, 4, got.Entries[1].Span)
	require.Equal(t, float64(0), got.Entries[1].Y, "the same stripe, so the y matches")
	require.Equal(t, 1, got.Entries[1].Start)

	// 3枚目は残り1列に4列は載らないので次のストライプ。
	// ストライプ高 = labelH(20) + gap(4) + tileH(100) = 124。次のy = 124 + gap(4) = 128
	require.Equal(t, 0, got.Entries[2].Col, "does not fit, so it starts the next stripe")
	require.Equal(t, float64(128), got.Entries[2].Y)
	require.Equal(t, 5, got.Entries[2].Start)

	require.Equal(t, float64(128+124), got.Height)
}

func TestLayoutGivesWholeRowsToBigDays(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	// 列数6。13枚は6列を占め、3段(6+6+1)になる。
	got := evalLayout(t, ctx, `[{d:"2026-02-08",n:13},{d:"2026-02-03",n:2}]`, 6)

	require.Len(t, got.Entries, 2)
	require.Equal(t, 6, got.Entries[0].Span, "a day with more photos than columns takes the whole row")
	require.Equal(t, 3, got.Entries[0].Rows)
	// h = 20 + 4 + 3*100 + 2*4 = 332
	require.Equal(t, float64(332), got.Entries[0].H)

	require.Equal(t, 0, got.Entries[1].Col, "whatever follows a full-row day always starts the next stripe")
	require.Equal(t, float64(332+4), got.Entries[1].Y)
	require.Equal(t, 13, got.Entries[1].Start)
}

func TestLayoutHandlesEmptyLibrary(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	got := evalLayout(t, ctx, `[]`, 6)

	require.Empty(t, got.Entries)
	require.Equal(t, float64(0), got.Height, "empty gives a height of 0, never NaN")
}

func TestLayoutLookupsAgreeWithEntries(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	// yForIndex と dayAtY が entries と食い違わないこと。
	// 実装が二分探索なので、境界（各グループの先頭・末尾）を総当たりで確かめる。
	var mismatches []string
	err = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const groups = [{d:"2026-02-08",n:1},{d:"2026-02-03",n:4},
		                {d:"2026-01-20",n:13},{d:"2026-01-05",n:2}];
		const L = famifo.layout(groups, 6, 100, 20, 4);
		const bad = [];
		for (const e of L.entries) {
			for (const i of [e.start, e.start + e.n - 1]) {
				const row = Math.floor((i - e.start) / e.span);
				const want = e.y + L.labelH + L.gap + row * (L.tileH + L.gap);
				const got = famifo.yForIndex(L, i);
				if (got !== want) bad.push('yForIndex(' + i + ')=' + got + ' want=' + want);
				// 詰めた行では複数の日が同じ y を共有するので、dayAtY は
				// その行のどれか1つしか返せない。「同じ行の日を返すこと」
				// までが約束できる範囲。
				const d = famifo.dayAtY(L, want);
				const hit = L.entries.find((x) => x.d === d);
				if (!hit || hit.y !== e.y) {
					bad.push('dayAtY(' + want + ')=' + d + ' は y=' + e.y + ' の行に無い');
				}
			}
		}
		return bad;
	})()`, &mismatches))
	require.NoError(t, err)
	require.Empty(t, mismatches, "the binary search disagrees with entries")
}

func TestVisibleWindowClipsBigDaysToRows(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	// 100枚の日（6列で17段）の途中だけを切り出せること。
	// 段が始まるのはラベル(20)+gap(4)の下なので、r段目の上端は 24+r*104。
	// 4段目(r=3)の上端は 24+312=336。
	//
	// 探索の起点を段の境界ちょうど(336)にしないのは、そこだと
	// (336-24)/104 が割り切れてしまい、floor を ceil に変えても
	// r0 が動かず、切り捨ての誤りを検出できなくなるため。
	// 436 なら 412/104=3.96 で、ceil にすると r0 が 4 になって落ちる。
	var got struct {
		From   int     `json:"from"`
		To     int     `json:"to"`
		PasteY float64 `json:"pasteY"`
		Pieces int     `json:"pieces"`
	}
	err = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const L = famifo.layout([{d:"2026-02-08",n:100}], 6, 100, 20, 4);
		const w = famifo.visibleWindow(L, 436, 436 + 200);
		return {from: w.from, to: w.to, pasteY: w.pasteY, pieces: w.pieces.length};
	})()`, &got))
	require.NoError(t, err)

	require.Equal(t, 1, got.Pieces)
	require.Equal(t, 18, got.From, "the start of the fourth row = 3*6")
	require.Equal(t, float64(336), got.PasteY, "the paste lands on the top edge of the row it cut out")
	require.Greater(t, got.To, got.From)
	require.Less(t, got.To, 100, "cuts out only what is visible, not all 100")
}

// レイアウトが計算した位置と、ブラウザが実際に置いた位置が一致すること。
//
// これが今回いちばん壊れやすく、しかも壊れても静かに壊れる（写真が微妙に
// 違う場所に出るだけで、例外も空白も出ない）。CSS Grid の自動配置が貪欲
// 詰めと同じ規則であるという前提そのものを、実測で確かめる。
//
// 列位置を自前で補正する方式に戻すと、ここが落ちる。かつては塊単位で
// タイルを貼り、貼り始めの列がずれるぶんを gridColumnStart で押し込んで
// いた。日ごとのカードを導入した時点で、貼る単位（塊）と並べる単位（日）が
// 一致しなくなり、この補正は意味を持たなくなった。配置はGridの自動配置に
// 任せ、テストはその結果がレイアウトの計算と一致するかだけを見る。
func TestCardPositionsMatchTheLayout(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)

	var mismatches []string
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(`(() => {
			const L = famifo.current();
			const spacerTop = document.querySelector('#spacer').getBoundingClientRect().top;
			const bad = [];
			for (const card of document.querySelectorAll('.daycard')) {
				const first = card.querySelector('.tile');
				if (!first) continue;
				const i = Number(first.dataset.i);
				// このカードが属するエントリを通し番号から引く
				const e = L.entries.find((x) => i >= x.start && i < x.start + x.n);
				if (!e) { bad.push('entry not found for i=' + i); continue; }
				const wantY = famifo.yForIndex(L, i);
				const gotY = card.getBoundingClientRect().top - spacerTop
					+ (card.querySelector('.daylabel') ? L.labelH + L.gap : 0);
				if (Math.abs(gotY - wantY) > 1) {
					bad.push('i=' + i + ' y got=' + gotY.toFixed(1) + ' want=' + wantY.toFixed(1));
				}
				const wantW = e.span * L.tileH + (e.span - 1) * L.gap;
				const gotW = card.getBoundingClientRect().width;
				if (Math.abs(gotW - wantW) > 1) {
					bad.push('i=' + i + ' width got=' + gotW.toFixed(1) + ' want=' + wantW.toFixed(1));
				}
			}
			return bad;
		})()`, &mismatches),
	)
	require.NoError(t, err)
	require.Empty(t, mismatches,
		"the JS calculation disagrees with where the browser actually laid the tiles out, over pastedRange")
}

// 1行に収まる日が実際に横に並ぶこと。
func TestSmallDaysSitSideBySide(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)

	var sharedRows int
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(`(() => {
			// 上端が同じカードが2枚以上あるストライプの数
			const tops = new Map();
			for (const c of document.querySelectorAll('.daycard')) {
				const t = Math.round(c.getBoundingClientRect().top);
				tops.set(t, (tops.get(t) || 0) + 1);
			}
			return [...tops.values()].filter((n) => n >= 2).length;
		})()`, &sharedRows),
	)
	require.NoError(t, err)
	require.Greater(t, sharedRows, 0,
		"days that fit on one row are not sitting side by side; check that the test photos include days of one and of a few photos")
}

// 列数を超える日が行を占有し、ラベルがその日を指すこと。
func TestBigDayTakesWholeRowsAndIsLabelled(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)

	// testDayCounts[0] は20枚。1600px(7列)では行を占有する。
	var res struct {
		Span  int    `json:"span"`
		Label string `json:"label"`
		Full  bool   `json:"full"`
	}
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(`(() => {
			const L = famifo.current();
			const card = document.querySelector('.daycard');
			const win = document.querySelector('#window');
			return {
				span: L.entries[0].span,
				label: card.querySelector('.daylabel').textContent,
				full: Math.abs(card.getBoundingClientRect().width
				               - win.getBoundingClientRect().width) < 1,
			};
		})()`, &res),
	)
	require.NoError(t, err)

	require.Equal(t, 7, res.Span, "1600px is 7 columns, and a day of 20 photos takes all of them")
	require.True(t, res.Full, "a day that takes the row spans the full width of the grid")

	// ラベルはタイルの data-date から作る。テスト写真の先頭の日と一致すること。
	day := dayOfPhoto(0) // "2026-01-01"
	parts := strings.Split(day, "-")
	wantMonth, wantDay := strings.TrimLeft(parts[1], "0"), strings.TrimLeft(parts[2], "0")
	require.Contains(t, res.Label, wantMonth+"月"+wantDay+"日",
		"the label does not match the date of the first photo")
}

// --- Task: TestScrubberLabelMatchesTheTopPhoto ---
//
// TestScrubberReachesBothEnds はフラクション0と1、つまり座標系の誤りが
// 打ち消し合う特異点でしかスクラバーを検証していない。実際にこのブランチの
// 早い段階で見つかった座標バグは、途中位置を誰も見ていなかったから
// 気づかれずに残っていた。ここでは中間位置までドラッグし、表示される
// ラベルの月が、可視範囲の先頭に実際に来た写真の月と一致することを見る。
//
// テスト写真は先頭の日(2026-01-01, 20枚)だけが1月で、残り(testDayCounts[1:])は
// すべて2025年12月に収まる（newestDay.AddDate(0,0,-d)がd>=1で前年12月に
// 入るため）。月境界は「先頭の日を過ぎた直後」の1箇所しかないので、
// スクラバーの中間（frac=0.5）まで下げれば、1行に複数の日が同居しても
// 月をまたぐ心配がない。
// 注意: これはドラッグとラベル表示の経路を通すテストであって、座標変換の
// 正しさは検出できない。月単位で比べており、このテスト写真の月境界は文書の
// 先頭にしか無いため、48px程度のずれでは答えが変わらない（実測で確認済み）。
// 座標変換そのものは TestScrollMapsIntoLayoutSpace が押さえている。
func TestScrubberLabelMatchesTheTopPhoto(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
	)
	require.NoError(t, err)

	// スクラバーを一時表示させてから掴む（TestScrubberReachesBothEndsと同じ経路）。
	err = chromedp.Run(rctx,
		chromedp.Evaluate(`famifo.scroller.scrollTop = 50`, nil),
		chromedp.Poll(`document.querySelector('#scrubber').classList.contains('visible')`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
	)
	require.NoError(t, err)

	var r rect
	err = chromedp.Run(rctx, chromedp.Evaluate(fmt.Sprintf(rectJS, `document.querySelector('#scrubber')`), &r))
	require.NoError(t, err)

	x := r.Left + r.Width/2
	y0 := r.Top + 5
	yMid := r.Top + r.Height*0.5

	// TestScrubberReachesBothEnds と同じ技法(input.DispatchMouseEvent による
	// press/move/release)を使う。ただしラベルは pointerup (release) で
	// 隠れてしまうため、release は状態を読み取った後まで遅らせる。
	press := input.DispatchMouseEvent(input.MousePressed, x, y0).WithButton(input.Left).WithClickCount(1)
	move := input.DispatchMouseEvent(input.MouseMoved, x, yMid).WithButton(input.Left).WithButtons(1)
	err = chromedp.Run(rctx, press, move)
	require.NoError(t, err)

	settleJS := `(() => {
		const now = famifo.scroller.scrollTop;
		if (window.__lastScroll === now) { return true; }
		window.__lastScroll = now;
		return false;
	})()`
	err = chromedp.Run(rctx,
		chromedp.Evaluate(`window.__lastScroll = -1;`, nil),
		chromedp.Poll(settleJS, nil, chromedp.WithPollingTimeout(5*time.Second)))
	require.NoError(t, err, "the scroll position never settled after the drag")

	// 貼り付け(塊のfetchを挟む非同期処理)が新しいスクロール位置に追いつくまで待つ。
	const anyTileInViewJS = `(() => {
		const vh = window.innerHeight;
		return [...document.querySelectorAll('#window .tile')].some((t) => {
			const rc = t.getBoundingClientRect();
			return rc.bottom > 0 && rc.top < vh;
		});
	})()`
	pollErr := chromedp.Run(rctx, chromedp.Poll(anyTileInViewJS, nil, chromedp.WithPollingTimeout(10*time.Second)))

	var res struct {
		Label  string `json:"label"`
		Hidden bool   `json:"hidden"`
		TopI   int    `json:"topI"`
	}
	err = chromedp.Run(rctx, chromedp.Evaluate(`(() => {
		const label = document.querySelector('#scrubber .scrub-label');
		const vh = window.innerHeight;
		let topI = -1, topTop = Infinity;
		for (const tile of document.querySelectorAll('#window .tile')) {
			const rc = tile.getBoundingClientRect();
			if (rc.bottom > 0 && rc.top < vh && rc.top < topTop) {
				topTop = rc.top;
				topI = Number(tile.dataset.i);
			}
		}
		return { label: label.textContent, hidden: label.hidden, topI };
	})()`, &res))
	require.NoError(t, err)

	// 読み取りは終わったので、掴んでいた指を離す。
	release := input.DispatchMouseEvent(input.MouseReleased, x, yMid).WithButton(input.Left).WithClickCount(1)
	err = chromedp.Run(rctx, release)
	require.NoError(t, err)

	require.NoErrorf(t, pollErr,
		"no tile appeared in the viewport after the drag; the pasting looks behind")
	require.False(t, res.Hidden, "the label should be visible while dragging")
	require.NotEqual(t, -1, res.TopI, "no first tile found in the viewport")

	day := dayOfPhoto(res.TopI)
	require.NotEmpty(t, day, "cannot tell which day the first tile (i=%d) belongs to", res.TopI)
	parts := strings.Split(day, "-")
	wantMonth := strings.TrimLeft(parts[1], "0")
	t.Logf("PROBE: label=%q topI=%d day=%s wantMonth=%s", res.Label, res.TopI, day, wantMonth)
	require.Containsf(t, res.Label, wantMonth+"月",
		"the month on the scrubber label (%q) does not match the month of the first photo in view (i=%d, day=%s)",
		res.Label, res.TopI, day)
}

// 文書のスクロール位置とレイアウト座標の変換を直接押さえる。
//
// これは描画の窓・スクラバー・リサイズ復元が共有する1本の橋で、
// このブランチ中に2回壊れた。ところが間接的には検出できない。
// オーバースキャンが4行(1600px幅で816px)あるので、48pxのずれは
// 描画結果に出ないまま吸収されてしまう。だから橋そのものを測る。
func TestScrollMapsIntoLayoutSpace(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)

	var got struct {
		Converted float64 `json:"converted"`
		Expected  float64 `json:"expected"`
		SpacerTop float64 `json:"spacerTop"`
	}
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(scrollToPhotoJS(deepScrollIndex), nil),
		chromedp.Evaluate(fmt.Sprintf(`(() => {
			const L = famifo.current();
			const spacer = document.querySelector('#spacer');
			return {
				converted: famifo.toLayoutY(famifo.scroller.scrollTop),
				expected: famifo.yForIndex(L, %d),
				spacerTop: spacer.getBoundingClientRect().top + famifo.scroller.scrollTop,
			};
		})()`, deepScrollIndex), &got),
	)
	require.NoError(t, err)

	require.Greater(t, got.SpacerTop, 1.0,
		"with #spacer at the very top of the document this conversion converts nothing, "+
			"which makes the test meaningless. Check that the top bar takes up space")
	require.InDelta(t, got.Expected, got.Converted, 1.0,
		"the scroll position converted to layout coordinates does not match the top of that photo's row")
}

// --- Task: TestLongScrollDoesNotStallOnSlowServer ---

// 長いスクロールの再現に使う写真。共有の200枚は塊が4つしかなく、
// 「通り過ぎた塊の取得が滞留する」状況そのものを作れないため別に用意する。
// 塊の数(= manyPhotoCount / stallChunkSize = 60)が、下まで降りる間に
// 積み上がる取得要求の上限になる。
const (
	manyPhotoCount = 1200
	manyPerDay     = 20
	stallChunkSize = 20

	// stallItemDelay は /items 1本あたりの応答時間。スキャンでCPUが
	// 埋まったNASを模す。直列化と併せて「1本ずつ、250msかけて捌く」になる。
	// 全60塊で15秒ぶん。下まで降りるスクロール自体は5秒ほどなので、
	// 通り過ぎた塊を取り続ける限り、着いた先の塊はその後ろで待たされる。
	stallItemDelay = 250 * time.Millisecond

	// stallCatchUp は手を止めてから一番古い写真が貼られるまでの許容時間。
	// 止まった場所に必要な塊は5つ前後(可視範囲+オーバースキャン+先読み)で
	// 1.3秒ほど。残りの塊を捌き終わるのを待つ側とはっきり分かれる値。
	stallCatchUp = 4 * time.Second
)

// prepareManyTestPhotos は manyPhotoCount 枚を manyPerDay 枚ずつの日に分けて登録する。
func prepareManyTestPhotos(st *store.Store, photoDir string, thumbs *thumb.Provider) error {
	for i := 0; i < manyPhotoCount; i++ {
		path := filepath.Join(photoDir, fmt.Sprintf("s%05d.jpg", i))
		takenAt := newestDay.AddDate(0, 0, -(i / manyPerDay)).
			Add(-time.Duration(i%manyPerDay) * time.Minute)
		if err := writeTestPhoto(path, i, takenAt); err != nil {
			return err
		}
	}
	stats, err := indexAll(st, photoDir, thumbs)
	if err != nil {
		return err
	}
	// 滞留の再現には全件が載っている必要がある。1枚でも欠けると本数が変わる。
	if stats.Indexed != manyPhotoCount {
		return fmt.Errorf("too few photos were indexed: %d/%d", stats.Indexed, manyPhotoCount)
	}
	return nil
}

// startStallGallery はスキャン中のNASを模したギャラリーを起動する。
// CPUが埋まって同時に1本しか捌けない状態を、/items を直列化したうえで
// 1本あたり stallItemDelay かけることで作る。サムネイルは遅くしない
// （滞留の実測では29秒のうち /items が273本、/thumb が10本だった）。
func startStallGallery(t *testing.T) (url string, itemsSeen, itemsDropped *int64) {
	t.Helper()

	tempDir := t.TempDir()
	photoDir := filepath.Join(tempDir, "photos")
	require.NoError(t, os.MkdirAll(photoDir, 0o755))
	thumbs, err := thumb.NewProvider(filepath.Join(tempDir, "thumbs"))
	require.NoError(t, err)

	st, err := store.Open(filepath.Join(tempDir, "stall.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	require.NoError(t, prepareManyTestPhotos(st, photoDir, thumbs))

	webSrv, err := web.NewServer(st, thumbs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	webSrv.SetChunkSize(stallChunkSize)

	var items, dropped int64
	h := webSrv.Handler()
	gate := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tiles" {
			atomic.AddInt64(&items, 1)
			gate <- struct{}{}
			defer func() { <-gate }()
			// ブラウザが取得をやめた要求はここで捨てる。実物の handleTiles も
			// ListRange に r.Context() を渡しており、切断された要求は
			// DBまで行かずに終わる。捨てないハーネスにすると、ブラウザが何を
			// 諦めてもサーバは全部を挽き続け、この不具合の再現にならない。
			select {
			case <-time.After(stallItemDelay):
			case <-r.Context().Done():
				atomic.AddInt64(&dropped, 1)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv.URL, &items, &dropped
}

// 一番古い写真まで降りたあと、画面がそこに追いつくこと。
//
// render() は通過中の位置の塊を fetchChunk() で取りに行く。これが中断
// されないと、止まった場所の塊が「通り過ぎただけの塊」の後ろに並ぶ。
// 塊が1つでも欠けている間 render() は貼らずに帰るため、#window は
// 画面外に置かれた前回の内容を保持したまま、ビューポートには何も出ない。
// 配信が遅いほど滞留は長く、スクロールした距離に比例して伸びる。
func TestLongScrollDoesNotStallOnSlowServer(t *testing.T) {
	requireBrowser(t)

	url, itemsSeen, itemsDropped := startStallGallery(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()

	var height int
	require.NoError(t, chromedp.Run(rctx,
		chromedp.EmulateViewport(800, 600),
		chromedp.Navigate(url),
		waitForTiles(30*time.Second),
		chromedp.Evaluate(`document.querySelector('#spacer').offsetHeight`, &height),
	))
	require.Greater(t, height, 0, "could not read the layout height")

	// ホイールで下まで降りる操作。1回で飛ばすと通り過ぎる塊が無く、
	// この不具合自体が起きない（スクラバーで飛ぶと1秒で着く）。
	const steps = 200
	for i := 1; i <= steps; i++ {
		require.NoError(t, chromedp.Run(rctx,
			chromedp.Evaluate(fmt.Sprintf(`document.scrollingElement.scrollTop=%d`, height*i/steps), nil),
			chromedp.Sleep(20*time.Millisecond)))
	}
	duringScroll := atomic.LoadInt64(itemsSeen)

	// 手を止めてから、一番古い写真が実際に貼られるまでを測る。
	wantFrom := manyPhotoCount - stallChunkSize*3
	start := time.Now()
	pollErr := chromedp.Run(rctx, chromedp.Poll(
		fmt.Sprintf(`famifo.pastedRange().from >= %d`, wantFrom),
		nil, chromedp.WithPollingTimeout(120*time.Second)))
	elapsed := time.Since(start)

	var pasted struct {
		From int `json:"from"`
		To   int `json:"to"`
	}
	require.NoError(t, chromedp.Run(rctx, chromedp.Evaluate(`famifo.pastedRange()`, &pasted)))

	t.Logf("caught up in %v: %d /items requests during the scroll, %d in total (%d of them dropped by the browser, %d chunks in all) pasted=%d..%d",
		elapsed.Round(time.Millisecond), duringScroll, atomic.LoadInt64(itemsSeen),
		atomic.LoadInt64(itemsDropped), manyPhotoCount/stallChunkSize, pasted.From, pasted.To)

	require.NoErrorf(t, pollErr,
		"it ended with the oldest photo never pasted: pasted=%d..%d (want from>=%d) /items requests=%d",
		pasted.From, pasted.To, wantFrom, atomic.LoadInt64(itemsSeen))
	require.Lessf(t, elapsed, stallCatchUp,
		"the screen took %v to catch up after the scrolling stopped (allowed %v). "+
			"Requests for the chunks scrolled past are not being aborted, so the chunk where it stopped queues up behind them. "+
			"%d /items requests during the scroll, %d in total by the time it caught up (%d chunks in all)",
		elapsed.Round(time.Millisecond), stallCatchUp,
		duringScroll, atomic.LoadInt64(itemsSeen), manyPhotoCount/stallChunkSize)
}

// --- Task: TestLightboxFetchSurvivesAGridRender ---

// awaitPromise は Evaluate に await させる。Promiseを返す式をそのまま
// 評価すると、解決を待たずに空の結果が返る。
func awaitPromise(p *runtime.EvaluateParams) *runtime.EvaluateParams {
	return p.WithAwaitPromise(true)
}

// ライトボックスが取りに行った塊を、一覧の貼り替えで切らないこと。
//
// 仮想スクロールではDOMに可視範囲のタイルしか無いため、ライトボックスは
// 全体の通し番号で動く。つまり一覧の可視範囲とは無関係な位置の塊を取りに行く。
// これを abandonChunksOutside が「可視範囲の外だから」と切ると、送った先の
// 写真がいつまでも出ない。切ってよいのは一覧の貼り替えが始めた取得だけ。
func TestLightboxFetchSurvivesAGridRender(t *testing.T) {
	requireBrowser(t)

	url, _, _ := startStallGallery(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	// 一覧は先頭を映したまま、そこから最も遠い写真をライトボックスの経路
	// (urlAt) で取りに行き、その取得中に一覧を貼り替える。配信が遅いので
	// render() の時点で取得は必ずまだ終わっていない。
	const js = `(async () => {
		const p = famifo.urlAt(famifo.total - 1);
		famifo.render();
		try {
			return (await p) ? "resolved" : "url was null";
		} catch (e) {
			return "aborted: " + e.name;
		}
	})()`

	var got string
	require.NoError(t, chromedp.Run(rctx,
		chromedp.EmulateViewport(800, 600),
		chromedp.Navigate(url),
		waitForTiles(30*time.Second),
		chromedp.Evaluate(js, &got, awaitPromise),
	))

	require.Equalf(t, "resolved", got,
		"could not resolve the URL of the oldest photo (%s); repainting the gallery is aborting the lightbox's fetch as well",
		got)
}

// --- Task: 写真ごとのURL ---
//
// 共有されたURLを直接開くと、その写真でライトボックスが開くこと。先頭の塊の
// 外にある写真を選ぶ。サーバーが埋めた通し番号から仮想スクロールが任意の位置へ
// 飛べていなければ、ここで別の写真が出るか、そもそも開かない。
func TestPhotoURLOpensTheLightbox(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	const target = testChunkSize + 40 // 初回HTMLに埋まっていない位置
	want := expectedPhotoURLs(target + 1)[target]
	id := strings.TrimPrefix(want, "/file/")

	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL+"/item/"+id),
		waitForTiles(10*time.Second),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err, "the lightbox never opened from the photo URL")

	var got string
	err = chromedp.Run(rctx, chromedp.Evaluate(
		`document.querySelector('#lightbox img').getAttribute('src')`, &got))
	require.NoError(t, err)
	require.Equal(t, want, got, "the photo URL opened a different photo")
}

// タイルから開いたときもURLが写真のものになり、戻るボタンで閉じてギャラリーへ
// 戻ること。ページ遷移ではなく履歴だけを積んでいることを、遷移すれば消える
// 目印で確かめる。遷移してしまうと一覧のスクロール位置が失われる。
func TestBackClosesTheLightbox(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	want := expectedPhotoURLs(1)[0]
	id := strings.TrimPrefix(want, "/file/")

	var path string
	var survived bool
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(`window.__noReload = true`, nil),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(`location.pathname`, &path),
		chromedp.Evaluate(`window.__noReload === true`, &survived),
	)
	require.NoError(t, err, "the lightbox never opened from a tile")
	require.Equal(t, "/item/"+id, path, "the URL does not name the open photo")
	require.True(t, survived, "the page reloaded instead of only pushing history")

	err = chromedp.Run(rctx,
		chromedp.NavigateBack(),
		chromedp.Poll(`document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(`location.pathname`, &path),
	)
	require.NoError(t, err, "the back button did not close the lightbox")
	require.Equal(t, "/", path)
}

// 送りではURLだけ差し替え、履歴には積まないこと。積むと、めくった枚数だけ
// 戻るボタンを押さないとギャラリーへ帰れなくなる。
func TestArrowKeysReplaceTheURLWithoutStackingHistory(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	urls := expectedPhotoURLs(4)
	third := strings.TrimPrefix(urls[3], "/file/")

	var path string
	err := chromedp.Run(rctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Click(`#window .tile`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(10*time.Second)),
	)
	require.NoError(t, err)

	for i := 1; i <= 3; i++ {
		want := strings.Replace(urls[i], "/file/", "/item/", 1)
		err = chromedp.Run(rctx,
			chromedp.KeyEvent(kb.ArrowRight),
			chromedp.Poll(fmt.Sprintf(`location.pathname === %s`, strconv.Quote(want)), nil,
				chromedp.WithPollingTimeout(10*time.Second)),
		)
		require.NoErrorf(t, err, "the URL did not follow the swipe to photo %d", i)
	}

	err = chromedp.Run(rctx, chromedp.Evaluate(`location.pathname`, &path))
	require.NoError(t, err)
	require.Equal(t, "/item/"+third, path)

	err = chromedp.Run(rctx,
		chromedp.NavigateBack(),
		chromedp.Poll(`document.querySelector('#lightbox').hidden`, nil, chromedp.WithPollingTimeout(10*time.Second)),
		chromedp.Evaluate(`location.pathname`, &path),
	)
	require.NoError(t, err, "one press of back did not return to the gallery")
	require.Equal(t, "/", path, "the arrows stacked history entries")
}

// 動画を開くと <img> ではなく <video> に切り替わり、離れると src が外れる。外さないと
// 裏でダウンロードが続き、次の写真を開いても前の動画の音が鳴り続ける。
//
// 再生そのものは確かめない。ヘッドレスのChromeはハードウェアデコーダを持たずHEVCを
// 再生できないうえ、H.264の検証用動画も ffmpeg 無しには作れないためである。
//
// 共有の200枚のコーパスには足さない。日ごとの枚数と通し番号に多くのテストが乗って
// いるので、1件足すだけで無関係なテストが落ちる。自前の小さなサーバーを立て、
// 共有のChromeだけを借りる。
func TestLightboxSwitchesBetweenImageAndVideo(t *testing.T) {
	requireBrowser(t)

	dir := t.TempDir()
	mediaDir := filepath.Join(dir, "media")
	require.NoError(t, os.MkdirAll(mediaDir, 0o755))

	st, err := store.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbs, err := thumb.NewProvider(filepath.Join(dir, "thumbs"))
	require.NoError(t, err)

	// 中身は問わない。原本が再生できるかではなく、要素が切り替わるかを見る。
	videoPath := filepath.Join(mediaDir, "clip.mp4")
	require.NoError(t, os.WriteFile(videoPath, []byte("not a real container"), 0o644))

	photoPath := filepath.Join(mediaDir, "a.jpg")
	pf, err := os.Create(photoPath)
	require.NoError(t, err)
	require.NoError(t, jpeg.Encode(pf, image.NewRGBA(image.Rect(0, 0, 40, 20)), nil))
	require.NoError(t, pf.Close())

	// 動画のほうを新しくして先頭に並べる。
	bg := context.Background()
	require.NoError(t, st.Upsert(bg,
		media.Restore(videoPath, time.Unix(1600000100, 0), time.Unix(1600000100, 0))))
	require.NoError(t, st.Upsert(bg,
		media.Restore(photoPath, time.Unix(1600000000, 0), time.Unix(1600000000, 0))))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	webSrv, err := web.NewServer(st, thumbs, log)
	require.NoError(t, err)
	srv := httptest.NewServer(webSrv.Handler())
	t.Cleanup(srv.Close)

	rctx, cancel := context.WithTimeout(newTab(t), 30*time.Second)
	defer cancel()

	const stateJS = `(() => {
		const v = document.querySelector('#lightbox video');
		const i = document.querySelector('#lightbox img');
		return { vHidden: v.hidden, iHidden: i.hidden, src: v.getAttribute('src') || '' };
	})()`

	var got struct {
		VHidden bool   `json:"vHidden"`
		IHidden bool   `json:"iHidden"`
		Src     string `json:"src"`
	}

	err = chromedp.Run(rctx,
		chromedp.EmulateViewport(1200, 900),
		chromedp.Navigate(srv.URL),
		waitForTiles(10*time.Second),
		chromedp.Click(`#window .tile[data-video]`, chromedp.NodeVisible),
		chromedp.Poll(`!document.querySelector('#lightbox').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Poll(`!document.querySelector('#lightbox video').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(stateJS, &got),
	)
	require.NoError(t, err)
	require.False(t, got.VHidden, "the video element is shown for a video")
	require.True(t, got.IHidden, "the image element is hidden for a video")
	require.True(t, strings.HasPrefix(got.Src, "/file/"), "got %q", got.Src)

	// 次（写真）へ送ると入れ替わり、動画の src は外れる。
	err = chromedp.Run(rctx,
		chromedp.KeyEvent(kb.ArrowRight),
		chromedp.Poll(`document.querySelector('#lightbox video').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(stateJS, &got),
	)
	require.NoError(t, err)
	require.True(t, got.VHidden)
	require.False(t, got.IHidden)
	require.Empty(t, got.Src, "the video source is dropped when leaving the video")

	// 閉じても外れたまま。
	err = chromedp.Run(rctx,
		chromedp.KeyEvent(kb.Escape),
		chromedp.Poll(`document.querySelector('#lightbox').hidden`, nil,
			chromedp.WithPollingTimeout(5*time.Second)),
		chromedp.Evaluate(stateJS, &got),
	)
	require.NoError(t, err)
	require.Empty(t, got.Src)
}
