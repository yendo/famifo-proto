package index

import "time"

// SetDebounce は静穏時間を上書きする。テスト専用で、本番のビルドには含まれない。
//
// 本番の2秒を待つとテストが実時間で待たされるだけになる。Run は別goroutineから
// debounce を読むため、Run を開始する前にのみ呼ぶこと。
func (w *Watcher) SetDebounce(d time.Duration) { w.debounce = d }

// InjectWatchError は監視のエラー経路にエラーを1つ流し込む。テスト専用で、
// 本番のビルドには含まれない。
//
// fsnotify のエラーは実際に起こすのが難しい。ErrEventOverflow はカーネルの
// inotify キュー（既定16,384件）を溢れさせないと届かず、それを読まずに
// 溜めきる必要がある。Run が受け取ってからの振る舞いだけを見るために、
// 経路の入口に直接置く。
func (w *Watcher) InjectWatchError(err error) { w.fsw.Errors <- err }
