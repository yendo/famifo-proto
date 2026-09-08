package index

import (
	"context"
	"time"
)

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

// IndexFile は indexFile を公開する。テスト専用で、本番のビルドには含まれない。
//
// 本番でインデックスの更新を呼ぶのはパッケージ内のスキャンと監視だけで、外から
// 使う口は要らない。テストは監視ループを回さずに1件分の反映だけを確かめたいため、
// ここで名前を与える。removeFile と removeTree も同じ理由による。
func (ix *Indexer) IndexFile(ctx context.Context, path string) error {
	return ix.indexFile(ctx, path)
}

// RemoveFile は removeFile を公開する。テスト専用で、本番のビルドには含まれない。
func (ix *Indexer) RemoveFile(ctx context.Context, path string) error {
	return ix.removeFile(ctx, path)
}

// RemoveTree は removeTree を公開する。テスト専用で、本番のビルドには含まれない。
func (ix *Indexer) RemoveTree(ctx context.Context, dir string) error {
	return ix.removeTree(ctx, dir)
}
