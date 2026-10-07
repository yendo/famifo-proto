package index

import (
	"context"
	"time"
)

// SetDebounce は静穏時間を上書きする。テスト専用で、本番のビルドには含まれない。
//
// 本番の2秒を待つとテストが実時間で待たされるだけになる。Run は別goroutineから
// debounce を読むため、Run を開始する前にのみ呼ぶこと。
//
// NewWatcher の引数で受け取る形にはしない。2秒は「コピー途中のファイルを
// デコードしに行かない」という監視側の事情で決まる値で、呼び出し側（main）が
// 選ぶものではない。引数にすると、その理由を知らない側に値を持たせることになる。
// 値の置き場所を監視の側に保つため、テストだけがここから縮める。
func (w *Watcher) SetDebounce(d time.Duration) { w.debounce = d }

// InjectWatchError は監視のエラー経路にエラーを1つ流し込む。テスト専用で、
// 本番のビルドには含まれない。
//
// ErrEventOverflow は実際に起こせる。NewWatcher のあと Run の前にファイルを
// 書けば、イベントが読まれずにカーネルのキューが溢れる。ただ、確実に溢れる数を
// 決めるには、テストがキューの上限（max_queued_events）と fsnotify の読み取り
// バッファの大きさ（内部の定数で、先読みのぶんキューが空く）を知る必要がある。
// どちらもこのパッケージの振る舞いではないので、テストに持ち込まない。Run が
// 受け取ってからの振る舞いだけを見るために、経路の入口に直接置く。
func (w *Watcher) InjectWatchError(err error) { w.fsw.Errors <- err }

// ScanOnce はスキャンを1回だけ走らせ、終わるまで待つ。テスト専用で、本番の
// ビルドには含まれない。
//
// 本番のスキャンは Run が繰り返すだけで、1回ぶんを外から呼ぶ口は要らない。
// ただ Run は1回の終わりを外に知らせないため、「載せない」「消さない」といった
// 起きないことを確かめるテストが書けない。Run で書くと固定の待ち時間に頼る
// ことになり、遅いうえに、スキャンが待ち時間を超えたときに誤って通る。戻った
// 時点で1回が終わっていることだけが要るので、scanOnce に名前を与える。
func (sc *Scanner) ScanOnce(ctx context.Context) error {
	_, err := newScanOnce(sc.ix, sc.slots, sc.log).scanAllRoots(ctx)
	return err
}
