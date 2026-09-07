package index

import (
	"context"
	"sync"
)

// executor は取り込みの実行を1箇所に集める。スキャンも監視もここを通すので、
// 同時に走る枚数の上限は入口によらず workers ひとつで決まる。
//
// 取り込みを出す入口は jobs を1つ取る。持ち場（sem）は executor が持ったままな
// ので上限は共有されるが、完了を待つ相手は jobs ごとに分かれる。スキャンと監視
// が同時に走るため、片方が相手の仕事の完了まで待つと、相手が仕事を足し続ける限り
// 返れなくなる。
type executor struct {
	ix  *Indexer
	sem chan struct{} // 持ち場。容量が同時に取り込む枚数の上限
}

func newExecutor(ix *Indexer) *executor {
	return &executor{ix: ix, sem: make(chan struct{}, ix.workers)}
}

// busy は取り込みが1件でも走っているかを返す。
//
// 持ち場は done を返し終えるまで空かないので、埋まっている持ち場があることは
// 「まだ Upsert していないワーカーが居る」ことと一致する。
func (e *executor) busy() bool { return len(e.sem) > 0 }

// jobs は1つの入口が出した取り込みの集まり。
//
// この単位は容量を持たない。同時に走る枚数の上限は executor の持ち場が決めて
// おり、jobs を分けても増えない。分けてあるのは完了を待つ相手だけである。
//
// 待ち方だけは入口ごとに逆になる。走査は持ち場が空くまで待ってよく、むしろ
// 待たないと数千件のパスを先に溜め込んでしまう。監視ループは決して待てない。
// 待った時間はそのまま fsnotify のイベントを読まない時間になり、カーネルの
// キューが溢れれば変更そのものを取りこぼす。submit と trySubmit はこの
// 非対称のためにある。
type jobs struct {
	e  *executor
	wg sync.WaitGroup
}

// newJobs は取り込みを出す入口ごとに1つ作る。
func (e *executor) newJobs() *jobs { return &jobs{e: e} }

// submit は持ち場が空くまで待ってから1枚の取り込みを始める。
// done は取り込みが終わったワーカーの上で呼ばれる。
func (j *jobs) submit(ctx context.Context, path string, done func(error)) {
	j.e.sem <- struct{}{}
	j.start(ctx, path, done)
}

// trySubmit は持ち場が空いていれば取り込みを始め、埋まっていれば false を返す。
// 呼び出し側をブロックしない。渡せなかったパスは呼び出し側が持ったままにして、
// 空いてから渡し直す。
func (j *jobs) trySubmit(ctx context.Context, path string, done func(error)) bool {
	select {
	case j.e.sem <- struct{}{}:
	default:
		return false
	}
	j.start(ctx, path, done)
	return true
}

// start は持ち場を確保済みの前提で1枚の取り込みを走らせる。
//
// 持ち場を空けるのは done を返したあとである。順序を逆にすると、通知を渡し
// 終える前に次の1枚が走り出せてしまい、通知の待ち行列が同時取り込み数を超えて
// 伸びうる。呼び出し側が容量をワーカー数で見積もれるよう、ここで閉じておく。
func (j *jobs) start(ctx context.Context, path string, done func(error)) {
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		defer func() { <-j.e.sem }()
		done(j.e.ix.IndexFile(ctx, path))
	}()
}

// wait はこの jobs に出した取り込みがすべて終わるまで待つ。
// 他の入口が出したぶんは待たない。
func (j *jobs) wait() { j.wg.Wait() }
