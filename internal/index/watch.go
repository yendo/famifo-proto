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
	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/synology"
)

// defaultDebounce は最後の書き込みイベントから実際にインデックスするまでの
// 待ち時間。コピー途中のファイルをデコードしに行かないための猶予。
const defaultDebounce = 2 * time.Second

// result は終わった取り込み1件。ワーカーから監視ループへ返す。
type result struct {
	path string
	err  error
}

// Watcher はfsnotifyでディレクトリツリーを監視し、変更をインデックスに反映する。
//
// Run のループはイベントの受信と帳簿づけだけを行い、写真の取り込みそのものは
// 決して自分では走らせない。取り込みには原本のデコードと縮小が含まれ、1枚で
// 数百ミリ秒かかる。ループの中で待てばその間 fsnotify のイベントを読めず、
// カーネルのキューが溢れて変更を取りこぼす。
type Watcher struct {
	ix       *Indexer
	fsw      *fsnotify.Watcher
	log      *slog.Logger
	debounce time.Duration
	// done は取り込みの完了通知。ワーカーは通知を渡し終えるまで持ち場を空けない
	// ので、渡し待ちがワーカー数を超えることはない。容量をそれに合わせておけば
	// ワーカーがここで止まらず、停止時に完了を待つ側と睨み合うこともない。
	done chan result
	// inflight は取り込み中のパスと、その最中に消えたかどうか。同じ写真を2つの
	// ワーカーに渡さないためと、取り込みの完了と削除がすれ違うのを防ぐためにある。
	inflight map[string]bool
}

// NewWatcher はWatcherを作る。
func NewWatcher(ix *Indexer, log *slog.Logger) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("監視を開始できません: %w", err)
	}
	return &Watcher{
		ix:       ix,
		fsw:      fsw,
		log:      log,
		debounce: defaultDebounce,
		done:     make(chan result, ix.workers),
		inflight: make(map[string]bool),
	}, nil
}

func (w *Watcher) Close() error { return w.fsw.Close() }

// Run はコンテキストがキャンセルされるまで監視を続ける。
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.addRoots(); err != nil {
		return err
	}

	// path -> 最後にイベントを受けた時刻
	pending := make(map[string]time.Time)
	tick := time.NewTicker(w.debounce / 2)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			// 走っている取り込みの完了まで待つ。待たずに戻ると、呼び出し側が
			// Close やDBの後始末に進んだあとでワーカーが書き込むことになる。
			w.ix.executor.wait()
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

		case r := <-w.done:
			removed := w.inflight[r.path]
			delete(w.inflight, r.path)
			if removed {
				// 取り込んでいる間に消えていた。今しがた入った行を取り消す。
				if err := w.ix.RemoveFile(ctx, r.path); err != nil {
					w.log.Warn("削除の反映に失敗", "path", r.path, "err", err)
				}
				break
			}
			if r.err != nil {
				w.log.Warn("インデックスをスキップ", "path", r.path, "err", r.err)
				break
			}
			w.log.Info("インデックスを更新", "path", r.path)

		case now := <-tick.C:
			w.flush(ctx, pending, now)
		}
	}
}

// handle は1つのfsnotifyイベントを処理する。
func (w *Watcher) handle(ctx context.Context, ev fsnotify.Event, pending map[string]time.Time) {
	if synology.InManagedDir(ev.Name) {
		return
	}
	switch {
	case ev.Has(fsnotify.Remove), ev.Has(fsnotify.Rename):
		// Renameは「この名前から消えた」を意味する。移動先は別途Createで届く。
		// この時点では消えたのがファイルかディレクトリか os.Stat では判別できないため
		// 両方呼ぶ。該当しない方は何もマッチせずno-opになるだけなので安全。
		// ディレクトリの場合、中の個々のファイルにはイベントが来ない
		//（mv album ../elsewhere や mv album album2 のケース）ので、
		// RemoveTreeで配下の行をパスの前方一致でまとめて消す。
		delete(pending, ev.Name)
		if _, ok := w.inflight[ev.Name]; ok {
			// 取り込み中に消えた写真。行が生まれるのは取り込みの完了時なので、
			// ここで消しても空振りする。完了を受けてから消す。
			w.inflight[ev.Name] = true
		}
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
			if imagefmt.IsSupported(ev.Name) {
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
		if imagefmt.IsSupported(ev.Name) {
			pending[ev.Name] = time.Now()
		}
	}
}

// flush はdebounce時間が経過した保留中のファイルをワーカーに渡す。
// 取り込みの完了は待たず、結果は w.done で受ける。
func (w *Watcher) flush(ctx context.Context, pending map[string]time.Time, now time.Time) {
	for path, last := range pending {
		if now.Sub(last) < w.debounce {
			continue
		}
		if _, ok := w.inflight[path]; ok {
			// 取り込み中に書き換えられた写真。完了を待ってから渡し直す。
			continue
		}
		if !w.ix.executor.trySubmit(ctx, path, func(err error) {
			w.done <- result{path: path, err: err}
		}) {
			// ワーカーが全部埋まっている。残りは保留のままにして次のtickで渡す。
			// ここで空くのを待つと、その間イベントを読めなくなる。
			return
		}
		delete(pending, path)
		w.inflight[path] = false
	}
}

// addRoots はすべてのルート以下を監視対象に加える。
func (w *Watcher) addRoots() error {
	for _, root := range w.ix.roots {
		if err := w.addTree(root); err != nil {
			return err
		}
	}
	return nil
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
		// 中のイベントはどのみち無視するので、監視枠を消費しない。
		// inotifyは再帰監視をしないぶん1ディレクトリ=1枠で、@eaDirは
		// 写真1枚につき1つできる。max_user_watches(既定8192)を容易に超える。
		if synology.IsManagedDir(d.Name()) {
			return fs.SkipDir
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
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if synology.IsManagedDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !imagefmt.IsSupported(path) {
			return nil
		}
		pending[path] = now
		return nil
	})
}
