package index

import (
	"context"
	"sync"
)

// executor は取り込みの実行を1箇所に集める。スキャンも監視もここを通すので、
// 同時に走る枚数の上限は入口によらず workers ひとつで決まる。
//
// 待ち方だけは入口ごとに逆になる。走査は持ち場が空くまで待ってよく、むしろ
// 待たないと数千件のパスを先に溜め込んでしまう。監視ループは決して待てない。
// 待った時間はそのまま fsnotify のイベントを読まない時間になり、カーネルの
// キューが溢れれば変更そのものを取りこぼす。submit と trySubmit はこの
// 非対称のためにある。
type executor struct {
	ix  *Indexer
	sem chan struct{} // 持ち場。容量が同時に取り込む枚数の上限
	wg  sync.WaitGroup
}

func newExecutor(ix *Indexer) *executor {
	return &executor{ix: ix, sem: make(chan struct{}, ix.workers)}
}

// submit は持ち場が空くまで待ってから1枚の取り込みを始める。
// done は取り込みが終わったワーカーの上で呼ばれる。
func (e *executor) submit(ctx context.Context, path string, done func(error)) {
	e.sem <- struct{}{}
	e.start(ctx, path, done)
}

// trySubmit は持ち場が空いていれば取り込みを始め、埋まっていれば false を返す。
// 呼び出し側をブロックしない。渡せなかったパスは呼び出し側が持ったままにして、
// 空いてから渡し直す。
func (e *executor) trySubmit(ctx context.Context, path string, done func(error)) bool {
	select {
	case e.sem <- struct{}{}:
	default:
		return false
	}
	e.start(ctx, path, done)
	return true
}

// start は持ち場を確保済みの前提で1枚の取り込みを走らせる。
//
// 持ち場を空けるのは done を返したあとである。順序を逆にすると、通知を渡し
// 終える前に次の1枚が走り出せてしまい、通知の待ち行列が同時取り込み数を超えて
// 伸びうる。呼び出し側が容量をワーカー数で見積もれるよう、ここで閉じておく。
func (e *executor) start(ctx context.Context, path string, done func(error)) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() { <-e.sem }()
		done(e.ix.IndexFile(ctx, path))
	}()
}

// wait は走っている取り込みがすべて終わるまで待つ。
func (e *executor) wait() { e.wg.Wait() }
