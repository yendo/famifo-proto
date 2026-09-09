// famifo-proto はローカルディスク上の写真をインデックス化し、
// LAN内のブラウザからギャラリーとして閲覧できるようにする。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	// 日付の切り出しは time.Local に依存する。zoneinfo を持たない環境
	// （scratchコンテナなど）では TZ を設定しても UTC に落ちてしまうため、
	// タイムゾーンデータベースをバイナリに埋め込む。約400KB増える。
	_ "time/tzdata"

	"github.com/yendo/famifo-proto/internal/config"
	"github.com/yendo/famifo-proto/internal/index"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
	"github.com/yendo/famifo-proto/internal/web"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run は起動から停止までを担う。プロセスに属するもの（シグナル、コマンドライン
// 引数、標準出力）は main から引数で受け取り、run 自身は os を直接読まない。
// テストから引数と出力を差し替えて呼べるようにするためである。
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	log := slog.New(slog.NewTextHandler(stderr, nil))

	cfg, showVersion, err := parseArgs(args, stderr)
	if err != nil {
		return err
	}
	if showVersion {
		fmt.Fprintln(stdout, "famifo-proto", versionString())
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	// 常駐プロセスなので、どのビルドが動いているかはログでしか確認できない。
	// timezone を出すのは、TZ の渡し忘れが静かに UTC になるため。
	// 誤ったまま本番のインデックスを作ると、全件やり直しになる。
	log.Info("starting", "version", versionString(),
		"timezone", startupTimezone(time.Now()),
		"dirs", cfg.PhotoDirs, "data", cfg.DataDir, "addr", cfg.Addr,
		"scan-workers", cfg.ScanWorkers, "scan-interval", cfg.ScanInterval)
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	// サムネイルの置き場は取り込みと配信の両方が使う。設定値を2経路に配ると
	// ずれても気づけないので、ここで1つだけ組み立てて両方へ渡す。
	thumbs, err := thumb.NewProvider(cfg.ThumbDir())
	if err != nil {
		return err
	}

	srv, err := web.NewServer(st, thumbs, log)
	if err != nil {
		return err
	}

	// シグナルの捕捉は main の役目。ここでは渡された ctx から cancel を派生させ、
	// HTTPの失敗や終了処理から取り込みを止められるようにする。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// スキャンの完了を待たずに配信を始める。大量の写真でも、
	// インデックスができた分から順に見られるほうがよい。
	// listenErrChは1要素バッファ: ListenAndServeの失敗をrunの戻り値まで伝え、
	// プロセスが異常終了時に0で終了しないようにする。
	listenErrCh := make(chan error, 1)
	httpSrv := &http.Server{Addr: cfg.Addr, Handler: srv.Handler()}
	go func() {
		log.Info("starting HTTP server", "addr", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server stopped", "err", err)
			listenErrCh <- err
			cancel()
		}
	}()

	ix := index.New(cfg.PhotoDirs, st, thumbs, cfg.ScanWorkers, log)

	// スキャンより先に監視を張る。逆にすると、スキャンが走査を終えてから監視が
	// 張られるまでの間に置かれた写真を、どちらも拾えない。数千枚でスキャンが
	// 数分かかる構成では、その窓のあいだの変更が次の起動まで反映されなくなる。
	// NewWatcher が戻った時点で監視は張れている。
	watcher, err := index.NewWatcher(ix, log)
	if err != nil {
		return err
	}
	defer watcher.Close()
	log.Info("watching for changes", "dirs", cfg.PhotoDirs)

	// 取り込みを走らせる goroutine の終了を待ってから store を閉じる。待たずに
	// 閉じると、あとから Upsert するワーカーが閉じたDBに書きに行く。
	// defer の順序で st.Close() より先に走る。
	var indexers sync.WaitGroup
	defer func() {
		cancel() // 監視とスキャンに終わるよう伝える
		indexers.Wait()
	}()

	indexers.Add(1)
	go func() {
		defer indexers.Done()
		if err := watcher.Run(ctx); err != nil {
			log.Error("watcher stopped", "err", err)
		}
	}()

	// スキャンは間隔をおいて繰り返す。1回目は起動直後に走り、止まっていた間の
	// 変更を取り戻す。2回目以降は監視の取りこぼしを回復する。
	indexers.Add(1)
	go func() {
		defer indexers.Done()
		ix.RunScans(ctx, cfg.ScanInterval, watcher.ScanRequests())
	}()

	// ListenAndServeの失敗はcancel()経由でctx.Done()も閉じるため、どちらが
	// 先に見えるかは決まらない。両方をselectで待ち、失敗はrunの戻り値まで伝える。
	var listenErr error
	select {
	case <-ctx.Done():
	case err := <-listenErrCh:
		listenErr = err
	}
	log.Info("shutting down")

	return shutdownHTTP(httpSrv, listenErrCh, listenErr)
}

// parseArgs はコマンドライン引数を解析して設定を返す。解析だけを担い、
// 値の妥当性は見ない。
// argsにはプログラム名を含めない。2つ目の戻り値は -version が指定されたことを表す。
func parseArgs(args []string, stderr io.Writer) (config.Config, bool, error) {
	fs := flag.NewFlagSet("famifo", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var c config.Config
	var dirs string
	fs.StringVar(&dirs, "dir", "",
		fmt.Sprintf("directories to collect photos from (required); %q separates several",
			string(filepath.ListSeparator)))
	fs.StringVar(&c.DataDir, "data", "./famifo-data", "where the database and generated thumbnails are stored")
	fs.StringVar(&c.Addr, "addr", ":8080", "HTTP listen address")
	// 適正値はCPU数とストレージの待ち時間の両方で決まる。NASでは読み込み待ちが
	// 効くので、CPU数が最善とは限らない。実機で詰められるようフラグにしてある。
	fs.IntVar(&c.ScanWorkers, "scan-workers", defaultScanWorkers(),
		"how many photos are taken in at once, both by the scan and by the watcher")
	// fsnotify は取りこぼす。溢れたことは検知できるが、監視枠を使い切って
	// 監視を張れなかったディレクトリのように、取りこぼしたと知る手立てが無い
	// 経路もある。定期的に突き合わせ直せば、検知の可否によらず整合性が戻る。
	//
	// 1日に1回で足りる。溢れは検知した時点でスキャンを前倒すし、書き込みが
	// 続いているファイルは Write イベントで積み直されるため、間隔を待たずに
	// 復帰する。この待ちが効くのは、一時的なIOエラーで落ちた1枚の取り直しと、
	// 取りこぼしに気づけなかったときのズレだけである。詰めても得るものが
	// 少ないわりに、決して読めないファイルのデコードを繰り返すことになる。
	fs.DurationVar(&c.ScanInterval, "scan-interval", 24*time.Hour,
		"how often the index is reconciled with what is on disk")
	showVersion := fs.Bool("version", false, "print the build version and exit")

	if err := fs.Parse(args); err != nil {
		return config.Config{}, false, err
	}
	// 空文字を SplitList に渡すと [""] ではなく [] が返る。
	c.PhotoDirs = filepath.SplitList(dirs)
	return c, *showVersion, nil
}

// defaultScanWorkers は取り込みの既定の並行数を返す。
//
// CPUを全部使うと同じマシンの他の仕事とHTTPの応答を圧迫するので、半分に留める。
// 1未満にはしない。足りなければ -scan-workers で上げられる。
func defaultScanWorkers() int {
	if n := runtime.NumCPU() / 2; n > 1 {
		return n
	}
	return 1
}

// versionString は実行中のバイナリのバージョンを返す。
//
// go build は .git からタグとコミットを読み、モジュール自身のバージョンを
// 埋める。タグ上でビルドすれば "v0.1.0"、途中のコミットなら擬似バージョン、
// 未コミットの変更があれば "+dirty" が付く。.git の無い場所でビルドすると
// Go 自身の印である "(devel)" になる。書き換えずそのまま出す。
// go version -m の表示と一致するほうが、突き合わせるときに迷わない。
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" {
		return "unknown"
	}
	return bi.Main.Version
}

// startupTimezone は起動ログに載せるタイムゾーンの表記を返す。
//
// Location の名前だけでは足りない。/etc/localtime を読んだだけの環境では
// 名前が "Local" になり、JSTなのかUTCなのか読み取れないため、時差も出す。
func startupTimezone(t time.Time) string {
	return t.Format("MST-07:00")
}

// shutdownHTTP は待ち受けを猶予付きで止め、待ち受けの失敗と停止の失敗を
// 1つのエラーにまとめる。listenErrは停止を待つ前に受け取っていた失敗で、
// 受け取っていなければnilが渡る。
func shutdownHTTP(httpSrv *http.Server, listenErrCh <-chan error, listenErr error) error {
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutErr := httpSrv.Shutdown(shutCtx)

	if listenErr == nil {
		// ctx.Done()側が先に選ばれていた場合に備えて、取りこぼしが無いか確認する。
		select {
		case err := <-listenErrCh:
			listenErr = err
		default:
		}
	}
	if listenErr != nil {
		return listenErr
	}
	return shutErr
}
