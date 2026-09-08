package main

import (
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 日付の切り出しは time.Local に依存する（撮影日時の解釈も、日ごとの区切りも）。
// Goは実行時に /etc/localtime か /usr/share/zoneinfo からタイムゾーンを解決するが、
// 配布先の scratch コンテナにはどちらも無い。TZ=Asia/Tokyo を設定しても、名前を
// 引くデータが無いため UTC にフォールバックする（実測で確認済み）。
// tzdata を埋め込むと、バイナリ単体で名前を解決できる。
//
// この性質は zoneinfo の無い環境でしか現れないため、手元のマシンで走る単体
// テストでは実挙動を確認できない。ここでは import が消えていないことだけを
// 保証する。実挙動の確認は scratch コンテナで行う（README参照）。
func TestEmbedsTimezoneDatabase(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, parser.ImportsOnly)
	require.NoError(t, err)

	var paths []string
	for _, imp := range f.Imports {
		paths = append(paths, imp.Path.Value)
	}

	require.Contains(t, paths, `"time/tzdata"`,
		"the timezone database has to be embedded in the binary, or time.Local "+
			"falls back to UTC in a container with no zoneinfo")
}

// TZを渡し忘れたコンテナは黙ってUTCで動き、そのまま本番のインデックスを
// 作ると全件に誤った値が焼き付く。起動ログで気づけるようにする。
//
// Location の名前だけでは足りない。/etc/localtime を読んだだけの環境では
// 名前が "Local" になり、JSTなのかUTCなのか読み取れない（実測で確認）。
func TestStartupTimezoneDistinguishesZonesWithTheSameName(t *testing.T) {
	t.Parallel()
	jst := time.Date(2026, 8, 26, 12, 0, 0, 0, time.FixedZone("Local", 9*60*60))
	utc := time.Date(2026, 8, 26, 12, 0, 0, 0, time.FixedZone("Local", 0))

	require.NotEqual(t, startupTimezone(utc), startupTimezone(jst),
		"the offset tells them apart even when the Location names match")
	require.Contains(t, startupTimezone(jst), "+09:00")
}

func TestParseArgsUsesDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	got, _, err := parseArgs([]string{"-dir", dir}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, []string{dir}, got.PhotoDirs)
	require.Equal(t, "./famifo-data", got.DataDir)
	require.Equal(t, ":8080", got.Addr)
	require.Equal(t, max(runtime.NumCPU()/2, 1), got.ScanWorkers,
		"half the CPUs by default: parallel indexing out of the box without using the machine up")
	require.Equal(t, time.Hour, got.ScanInterval,
		"consistency returns within an hour by default even when the watcher misses something")
}

func TestParseArgsOverridesEveryFlag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	got, _, err := parseArgs([]string{
		"-dir", dir, "-data", "/var/famifo", "-addr", "192.168.1.10:9000",
		"-scan-workers", "3", "-scan-interval", "10m",
	}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, "/var/famifo", got.DataDir)
	require.Equal(t, "192.168.1.10:9000", got.Addr)
	require.Equal(t, 3, got.ScanWorkers)
	require.Equal(t, 10*time.Minute, got.ScanInterval)
}

func TestParseArgsSplitsDirOnTheListSeparator(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()

	got, _, err := parseArgs([]string{"-dir", a + string(filepath.ListSeparator) + b}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, []string{a, b}, got.PhotoDirs)
}

// parseArgs は -version を報告するだけで、表示も検証も呼び出し側に任せる。
func TestParseArgsReportsTheVersionFlag(t *testing.T) {
	t.Parallel()
	_, showVersion, err := parseArgs([]string{"-version"}, io.Discard)

	require.NoError(t, err)
	require.True(t, showVersion)
}

// ':' を含むパスを渡すと分割で壊れる。なぜそうなったか読めるエラーにする。
func TestRunExplainsHowDirWasSplit(t *testing.T) {
	t.Parallel()
	err := run(context.Background(), []string{"-dir", "/no/such/2024:05:24"},
		io.Discard, io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "2024",
		"shows the split, so it is clear the separator cut the path")
}

// run は起動から停止までの配線である。取り込みや配信の中身はそれぞれの
// パッケージのテストが見ているので、ここで確かめるのは配線とライフサイクル、
// つまり起動して応答し、合図で止まることだけにする。写真ディレクトリを空に
// してあるのはそのため。取り込むものが無くても配信は始まる。
func TestRunServesUntilContextIsCancelled(t *testing.T) {
	t.Parallel()
	args := runArgs(t, freeAddr(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- run(ctx, args, io.Discard, io.Discard) }()

	waitForReady(t, addrOf(args))
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "a signalled stop exits cleanly")
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after the ctx was cancelled")
	}
}

// ListenAndServe の失敗を握りつぶすと、待ち受けできていないのに終了コード0で
// 終わる。監視下では「起動して即正常終了した」ようにしか見えず、原因に
// たどり着けない。ctxをキャンセルしていないのに戻ること、その戻り値が
// エラーであることを確かめる。
func TestRunReportsListenFailure(t *testing.T) {
	t.Parallel()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()

	args := runArgs(t, busy.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- run(ctx, args, io.Discard, io.Discard) }()

	select {
	case err := <-done:
		require.Error(t, err, "a listen failure is carried through to run's return value")
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after the listen failed")
	}
}

// -dir を渡していないのに成功する。-version が設定の検証まで進まないことと、
// バージョンが標準出力に出ることの両方をここで押さえる。
func TestRunVersionPrintsToStdout(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer

	err := run(context.Background(), []string{"-version"}, &stdout, io.Discard)

	require.NoError(t, err)
	require.Contains(t, stdout.String(), "famifo-proto")
	require.Contains(t, stdout.String(), versionString())
}

func TestRunRejectsInvalidArgs(t *testing.T) {
	t.Parallel()
	err := run(context.Background(), nil, io.Discard, io.Discard)

	require.Error(t, err, "it does not start without -dir")
}

// runArgs は run に渡す最小の引数を組み立てる。-data を -dir の下に置くと run が
// 弾くので、一時ディレクトリの下に並べて作る。-data 自体は store と thumb が
// 作るので用意しない。
func runArgs(t *testing.T, addr string) []string {
	t.Helper()

	root := t.TempDir()
	photos := filepath.Join(root, "photos")
	require.NoError(t, os.Mkdir(photos, 0o755))

	return []string{
		"-dir", photos,
		"-data", filepath.Join(root, "data"),
		"-addr", addr,
	}
}

// addrOf は runArgs が組み立てた引数から -addr の値を取り出す。
func addrOf(args []string) string {
	for i, a := range args {
		if a == "-addr" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// freeAddr は空いているアドレスを返す。ポート0で開いて実際に割り当てられた
// 値を読み、すぐ閉じる。閉じてから run が開くまでの隙に他が取る可能性は
// 残るが、待ち受けアドレスを外から与える設計である以上ここは避けられない。
func freeAddr(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())

	return addr
}

// waitForReady はギャラリーが応答するまで待つ。run はスキャンの完了を待たずに
// 配信を始めるので、起動できたかどうかは200が返るかどうかでしか分からない。
func waitForReady(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never responded", addr)
}
