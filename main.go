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

// parseArgs はコマンドライン引数を解析して検証済みの設定を返す。
// argsにはプログラム名を含めない。2つ目の戻り値は -version が指定されたことを表す。
func parseArgs(args []string, stderr io.Writer) (config.Config, bool, error) {
	fs := flag.NewFlagSet("famifo", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var c config.Config
	var dirs string
	fs.StringVar(&dirs, "dir", "",
		fmt.Sprintf("写真を収集するディレクトリ (必須)。%q で区切って複数指定できる",
			string(filepath.ListSeparator)))
	fs.StringVar(&c.DataDir, "data", "./famifo-data", "DBとサムネイルの保存先")
	fs.StringVar(&c.Addr, "addr", ":8080", "HTTPの待ち受けアドレス")
	// 適正値はCPU数とストレージの待ち時間の両方で決まる。NASでは読み込み待ちが
	// 効くので、CPU数が最善とは限らない。実機で詰められるようフラグにしてある。
	fs.IntVar(&c.ScanWorkers, "scan-workers", defaultScanWorkers(),
		"同時に取り込む枚数（スキャンとfsnotifyの追従に共通）")
	// fsnotify は取りこぼす。溢れたことは検知できるが、監視枠を使い切って
	// 監視を張れなかったディレクトリのように、取りこぼしたと知る手立てが無い
	// 経路もある。定期的に突き合わせ直せば、検知の可否によらず整合性が戻る。
	fs.DurationVar(&c.ScanInterval, "scan-interval", time.Hour,
		"インデックスをディスクの実態と突き合わせ直す間隔")
	showVersion := fs.Bool("version", false, "バージョンを表示して終了する")

	if err := fs.Parse(args); err != nil {
		return config.Config{}, false, err
	}
	// バージョンを表示するだけなので -dir は要らない。検証まで進めない。
	if *showVersion {
		return config.Config{}, true, nil
	}
	// 空文字を SplitList に渡すと [""] ではなく [] が返るので、
	// 「未指定」は Validate 側の「1つ以上必須」で捕まる。
	c.PhotoDirs = filepath.SplitList(dirs)
	return c, false, c.Validate()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, showVersion, err := parseArgs(os.Args[1:], os.Stderr)
	if err != nil {
		return err
	}
	if showVersion {
		fmt.Println("famifo-proto", versionString())
		return nil
	}
	// 常駐プロセスなので、どのビルドが動いているかはログでしか確認できない。
	// timezone を出すのは、TZ の渡し忘れが静かに UTC になるため。
	// 誤ったまま本番のインデックスを作ると、全件やり直しになる。
	log.Info("起動", "version", versionString(),
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// スキャンの完了を待たずに配信を始める。大量の写真でも、
	// インデックスができた分から順に見られるほうがよい。
	// serveErrは1要素バッファ: ListenAndServeの失敗をrunの戻り値まで伝え、
	// プロセスが異常終了時に0で終了しないようにする。
	serveErr := make(chan error, 1)
	httpSrv := &http.Server{Addr: cfg.Addr, Handler: srv.Handler()}
	go func() {
		log.Info("HTTPサーバーを開始", "addr", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTPサーバーが停止しました", "err", err)
			serveErr <- err
			stop()
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
	log.Info("変更の監視を開始", "dirs", cfg.PhotoDirs)

	// 取り込みを走らせる goroutine の終了を待ってから store を閉じる。待たずに
	// 閉じると、あとから Upsert するワーカーが閉じたDBに書きに行く。
	// defer の順序で st.Close() より先に走る。
	var indexers sync.WaitGroup
	defer func() {
		stop() // 監視と定期スキャンに終わるよう伝える
		indexers.Wait()
	}()

	indexers.Add(1)
	go func() {
		defer indexers.Done()
		if err := watcher.Run(ctx); err != nil {
			log.Error("監視が停止しました", "err", err)
		}
	}()

	// fsnotifyは停止中の変更を検知できないので、起動のたびに実態と突き合わせる。
	// 1回目はここで同期に走らせる。失敗したら起動を止めるためである。
	log.Info("スキャンを開始", "dirs", cfg.PhotoDirs)
	// 所要時間も出す。取り込みの重さを変える変更をしたとき、前後を突き合わせられる
	// 記録がログにしか残らないため。
	scanStart := time.Now()
	stats, err := ix.Scan(ctx)
	if err != nil && ctx.Err() == nil {
		return err
	}
	if ctx.Err() == nil {
		log.Info("スキャンが完了",
			"elapsed", time.Since(scanStart).Round(time.Millisecond),
			"indexed", stats.Indexed, "unchanged", stats.Unchanged,
			"removed", stats.Removed, "skipped", stats.Skipped)

		// 2回目以降は間隔をおいて繰り返す。監視の取りこぼしはこれで回復する。
		indexers.Add(1)
		go func() {
			defer indexers.Done()
			ix.RunScans(ctx, cfg.ScanInterval, watcher.ScanRequests())
		}()
		log.Info("定期スキャンを開始", "interval", cfg.ScanInterval)
	}

	// ListenAndServeの失敗はstop()経由でctx.Done()も閉じるため、どちらが
	// 先に見えるかは決まらない。両方をselectで待ち、失敗はrunの戻り値まで伝える。
	var listenErr error
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		listenErr = err
	}
	log.Info("シャットダウンします")

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutErr := httpSrv.Shutdown(shutCtx)

	if listenErr == nil {
		// ctx.Done()側が先に選ばれていた場合に備えて、取りこぼしが無いか確認する。
		select {
		case err := <-serveErr:
			listenErr = err
		default:
		}
	}
	if listenErr != nil {
		return listenErr
	}
	return shutErr
}
