package index

import "time"

// SetDebounce は静穏時間を上書きする。テスト専用で、本番のビルドには含まれない。
//
// 本番の2秒を待つとテストが実時間で待たされるだけになる。Run は別goroutineから
// debounce を読むため、Run を開始する前にのみ呼ぶこと。
func (w *Watcher) SetDebounce(d time.Duration) { w.debounce = d }
