package index

import "fmt"

// Slots は同時に取り込める枠。容量が上限で、取っている数が使用中である。
//
// スキャンと監視が1つを共有する。入口ごとに持つと、両方が同時に走ったときの合計が
// 上限の2倍になる。上限はCPUとストレージの予算なので、入口によらず1つで効かせる。
//
// 何も走らせない受け身の枠である。goroutine を起こすのも、取り込みを呼ぶのも入口が
// 自分で行う。
type Slots struct{ ch chan struct{} }

// NewSlots は上限 n の枠を作る。n は取り込みを同時に走らせる数で、1未満なら panic する。
// 適正値はCPU数とストレージの待ち時間の両方で決まるので、呼び出し側が決める。
//
// 0個の枠は「並行しない」ではなく、acquire が永久に返らなくなる壊れた値である。
// 存在してはいけない値なので、作れた振りをしない。1未満を1に読み替えもしない。
// 読み替えると、渡した上限と実際に走る数が食い違ったまま動き、気づく手立てが無くなる。
func NewSlots(n int) *Slots {
	if n < 1 {
		panic(fmt.Sprintf("index: slots must be 1 or greater: %d", n))
	}
	return &Slots{ch: make(chan struct{}, n)}
}

// acquire は枠が空くまで待って1つ取る。
func (s *Slots) acquire() { s.ch <- struct{}{} }

// tryAcquire は枠が空いていれば1つ取り、埋まっていれば false を返す。待たない。
func (s *Slots) tryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// release は取った枠を1つ返す。
func (s *Slots) release() { <-s.ch }

// busy は使用中の枠が1つでもあるかを返す。誰が取ったかは区別しない。
//
// 入口はどちらも、取り込みの結果を渡し終えてから枠を返す。だから使用中の枠が
// あることは「まだ Upsert を終えていない取り込みがある」ことと一致する。
func (s *Slots) busy() bool { return len(s.ch) > 0 }

// size は上限を返す。
func (s *Slots) size() int { return cap(s.ch) }
