// famifo-proto はローカルディスク上の写真をインデックス化し、
// LAN内のブラウザからギャラリーとして閲覧できるようにする。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	// 日付の切り出しは time.Local に依存する。zoneinfo を持たない環境
	// （scratchコンテナなど）では TZ を設定しても UTC に落ちてしまうため、
	// タイムゾーンデータベースをバイナリに埋め込む。約400KB増える。
	_ "time/tzdata"

	"github.com/yendo/famifo-proto/internal/config"
	"github.com/yendo/famifo-proto/internal/index"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/web"
)

// version はリリースビルドで -ldflags で埋める。
var version string

// formatVersion は表示用のバージョン文字列を組み立てる。
//
// override が空でなければそれを使う（リリースビルドで -ldflags で埋める）。
// 空なら go build が自動で埋めるVCS情報から組み立てるので、フラグを付け
// 忘れても版が消えない。ただし .git の無い場所でビルドするとVCS情報自体が
// 付かないため（Dockerのマルチステージ等）、その場合は override が要る。
func formatVersion(override string, settings []debug.BuildSetting) string {
	if override != "" {
		return override
	}

	var rev, when string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	if rev == "" {
		return "unknown"
	}
	if len(rev) > 8 {
		rev = rev[:8]
	}
	// 未コミットの変更が混ざったビルドは、手元のどのコミットとも一致しない。
	if dirty {
		rev += "-dirty"
	}
	if when == "" {
		return rev
	}
	return rev + " (" + when + ")"
}

// versionString は実行中のバイナリのバージョンを返す。
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return formatVersion(version, nil)
	}
	return formatVersion(version, bi.Settings)
}

// startupTimezone は起動ログに載せるタイムゾーンの表記を返す。
//
// Location の名前だけでは足りない。/etc/localtime を読んだだけの環境では
// 名前が "Local" になり、JSTなのかUTCなのか読み取れないため、時差も出す。
func startupTimezone(t time.Time) string {
	return t.Format("MST-07:00")
}

// pageSize は一覧1ページあたりの枚数。
const pageSize = 60

// thumbSize はサムネイルの長辺ピクセル数。
//
// 一覧のタイルは正方形で object-fit: cover のため、実際に効くのは短辺
// （3:2の写真なら320px）である。設定可能にしていたが、利用者が変える場面が
// 無いうえ、変えても既存のサムネイルは作り直されず「設定できるのに効かない」
// フラグになっていたため定数にした。値を変えたときはデータディレクトリごと
// 削除して作り直すこと。
const thumbSize = 480

// watchDebounce はファイル書き込みが落ち着いたと判断するまでの待ち時間。
// コピー途中のファイルをデコードしに行かないための猶予。
const watchDebounce = 2 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if errors.Is(err, config.ErrVersionRequested) {
		fmt.Println("famifo-proto", versionString())
		return nil
	}
	if err != nil {
		return err
	}
	// 常駐プロセスなので、どのビルドが動いているかはログでしか確認できない。
	// timezone を出すのは、TZ の渡し忘れが静かに UTC になるため。
	// 誤ったまま本番のインデックスを作ると、全件やり直しになる。
	log.Info("起動", "version", versionString(),
		"timezone", startupTimezone(time.Now()),
		"dirs", cfg.PhotoDirs, "data", cfg.DataDir, "addr", cfg.Addr)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("データディレクトリを作れません: %w", err)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	srv, err := web.NewServer(st, cfg.ThumbDir(), pageSize, log)
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

	ix, err := index.New(cfg.PhotoDirs, st, cfg.ThumbDir(), thumbSize, log)
	if err != nil {
		return err
	}

	// fsnotifyは停止中の変更を検知できないので、起動のたびに実態と突き合わせる。
	log.Info("フルスキャンを開始", "dirs", cfg.PhotoDirs)
	stats, err := ix.FullScan(ctx)
	if err != nil && ctx.Err() == nil {
		return err
	}
	if ctx.Err() == nil {
		log.Info("フルスキャンが完了",
			"indexed", stats.Indexed, "unchanged", stats.Unchanged,
			"removed", stats.Removed, "skipped", stats.Skipped)

		watcher, err := index.NewWatcher(ix, log, watchDebounce)
		if err != nil {
			return err
		}
		defer watcher.Close()
		go func() {
			if err := watcher.Run(ctx); err != nil {
				log.Error("監視が停止しました", "err", err)
			}
		}()
		log.Info("変更の監視を開始", "dirs", cfg.PhotoDirs)
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
