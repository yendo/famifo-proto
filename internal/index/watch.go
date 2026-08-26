package index

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/yendo/famifo-proto/internal/photo"
)

// Watcher はfsnotifyでディレクトリツリーを監視し、変更をインデックスに反映する。
type Watcher struct {
	ix       *Indexer
	fsw      *fsnotify.Watcher
	log      *slog.Logger
	debounce time.Duration
}

// NewWatcher はWatcherを作る。debounceは最後の書き込みイベントから実際に
// インデックスするまでの待ち時間。ファイルのコピー中に読み込むのを避ける。
func NewWatcher(ix *Indexer, log *slog.Logger, debounce time.Duration) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("監視を開始できません: %w", err)
	}
	return &Watcher{ix: ix, fsw: fsw, log: log, debounce: debounce}, nil
}

func (w *Watcher) Close() error { return w.fsw.Close() }

// Run はコンテキストがキャンセルされるまで監視を続ける。
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.addTree(w.ix.root); err != nil {
		return err
	}

	// path -> 最後にイベントを受けた時刻
	pending := make(map[string]time.Time)
	tick := time.NewTicker(w.debounce / 2)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			w.handle(ctx, ev, pending)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Warn("監視エラー", "err", err)

		case now := <-tick.C:
			w.flush(ctx, pending, now)
		}
	}
}

// handle は1つのfsnotifyイベントを処理する。
func (w *Watcher) handle(ctx context.Context, ev fsnotify.Event, pending map[string]time.Time) {
	switch {
	case ev.Has(fsnotify.Remove), ev.Has(fsnotify.Rename):
		// Renameは「この名前から消えた」を意味する。移動先は別途Createで届く。
		// この時点では消えたのがファイルかディレクトリか os.Stat では判別できないため
		// 両方呼ぶ。該当しない方は何もマッチせずno-opになるだけなので安全。
		// ディレクトリの場合、中の個々のファイルにはイベントが来ない
		//（mv album ../elsewhere や mv album album2 のケース）ので、
		// RemoveTreeで配下の行をパスの前方一致でまとめて消す。
		delete(pending, ev.Name)
		if err := w.ix.RemoveFile(ctx, ev.Name); err != nil {
			w.log.Warn("削除の反映に失敗", "path", ev.Name, "err", err)
		}
		if err := w.ix.RemoveTree(ctx, ev.Name); err != nil {
			w.log.Warn("ディレクトリ配下の削除の反映に失敗", "path", ev.Name, "err", err)
		}

	case ev.Has(fsnotify.Create):
		fi, err := os.Stat(ev.Name)
		if err != nil {
			return // すぐ消された等。何もしない
		}
		if !fi.IsDir() {
			if photo.IsSupported(ev.Name) {
				pending[ev.Name] = time.Now()
			}
			return
		}
		// 新しいディレクトリ: 監視に加えたうえで、既に入っている中身も拾う。
		// ディレクトリごとmvされた場合、中のファイルには個別のイベントが来ない。
		if err := w.addTree(ev.Name); err != nil {
			w.log.Warn("監視対象の追加に失敗", "path", ev.Name, "err", err)
		}
		w.enqueueTree(ev.Name, pending)

	case ev.Has(fsnotify.Write):
		if photo.IsSupported(ev.Name) {
			pending[ev.Name] = time.Now()
		}
	}
}

// flush はdebounce時間が経過した保留中のファイルをインデックスする。
func (w *Watcher) flush(ctx context.Context, pending map[string]time.Time, now time.Time) {
	for path, last := range pending {
		if now.Sub(last) < w.debounce {
			continue
		}
		delete(pending, path)
		if err := w.ix.IndexFile(ctx, path); err != nil {
			w.log.Warn("インデックスをスキップ", "path", path, "err", err)
			continue
		}
		w.log.Info("インデックスを更新", "path", path)
	}
}

// addTree は root 以下の全ディレクトリを監視対象に加える。
// fsnotifyは再帰監視をしないため自前で降りていく。
func (w *Watcher) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			w.log.Warn("監視対象をスキップ", "path", path, "err", err)
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if err := w.fsw.Add(path); err != nil {
			w.log.Warn("監視対象を追加できません", "path", path, "err", err)
		}
		return nil
	})
}

// enqueueTree は root 以下の対象ファイルを保留キューに積む。
func (w *Watcher) enqueueTree(root string, pending map[string]time.Time) {
	now := time.Now()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !photo.IsSupported(path) {
			return nil
		}
		pending[path] = now
		return nil
	})
}
