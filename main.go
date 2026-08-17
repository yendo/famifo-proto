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
	"syscall"
	"time"

	"github.com/yendo/famifo-proto/internal/config"
	"github.com/yendo/famifo-proto/internal/index"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
	"github.com/yendo/famifo-proto/internal/web"
)

// pageSize は一覧1ページあたりの枚数。
const pageSize = 60

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
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("データディレクトリを作れません: %w", err)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	gen, err := thumb.NewGenerator(cfg.ThumbDir(), cfg.ThumbSize)
	if err != nil {
		return err
	}

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

	ix := index.New(cfg.PhotoDir, st, gen, log)

	// fsnotifyは停止中の変更を検知できないので、起動のたびに実態と突き合わせる。
	log.Info("フルスキャンを開始", "dir", cfg.PhotoDir)
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
		log.Info("変更の監視を開始", "dir", cfg.PhotoDir)
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
