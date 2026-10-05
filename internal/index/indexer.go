// Package index はディスク上の写真・動画とSQLiteインデックスを同期させる。
// 起動時のスキャンとfsnotifyによる追従の両方をここで担う。
//
// 1件を取り込む手順のうち、撮影日時の読み取りは exif（静止画）と videometa（動画）が
// 担う。どちらも取り込み時にしか使わないのでサブパッケージに置く。
//
// サムネイルの調達は internal/thumb が担う。こちらは配信側とも共有するため、
// 呼び出し側が組み立てたものを受け取る。
package index

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/index/exif"
	"github.com/yendo/famifo-proto/internal/index/videometa"
	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// Indexer は写真1枚の変更をインデックスとサムネイルに反映する。どこを見てどこに
// 書くかと、同時に取り込める枠を持つ。インデックスのデータ自体は store にある。
//
// 取り込みを起こす入口は Scanner と Watcher の2つで、どちらもこれを1つ共有する。
// 上限が入口によらず効くのはそのためである。
type Indexer struct {
	roots  []string
	store  *store.Store
	thumbs *thumb.Provider
	// slots は同時に取り込める枠。スキャンも監視も同じ枠から1つ取るので、上限は
	// 入口によらず効く。入口ごとに持つと合計が上限の2倍になるため、1つを共有する。
	slots slots
	log   *slog.Logger
}

// New はIndexerを作る。rootsは写真を収集するルートディレクトリ。
//
// thumbs は配信側と共有する。同じ置き場所を指す設定値を2経路に配ると、
// ずれても誰も気づけないため、組み立てたものを1つ受け取る。
//
// workers は同時に取り込む枚数。1未満は1として扱う。適正値はCPU数とストレージの
// 待ち時間で決まるため設定から来る。
//
// スキャンも監視も同じ枠から1つ取るので、この上限は取り込みの入口に
// よらず効く。ディレクトリごと移動された場合、監視にも一度に数百件が来る。
func New(roots []string, st *store.Store, thumbs *thumb.Provider, workers int, log *slog.Logger) *Indexer {
	if workers < 1 {
		workers = 1
	}
	return &Indexer{
		roots:  roots,
		store:  st,
		thumbs: thumbs,
		slots:  newSlots(workers),
		log:    log,
	}
}

// indexFile は1ファイルをインデックスに反映する。
//
// 対象外の拡張子とディレクトリは黙って無視する（エラーではない）。
// 自前で作るしかないファイルでサムネイルを作れなかった場合はエラーを返し、
// DBには登録しない。壊れた画像を登録すると一覧に読み込めない <img> が並ぶため。
func (ix *Indexer) indexFile(ctx context.Context, path string) error {
	if !imagefmt.IsSupported(path) {
		return nil
	}
	// Lstat で見る。Stat はリンクを追うので、写真ディレクトリに置かれた
	// シンボリックリンクを、その先にあるファイルとして取り込んでしまう。
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cannot stat the file: %w", err)
	}
	if fi.IsDir() {
		return nil
	}
	// シンボリックリンクは載せない。
	//
	// 載せると配信できてしまう。自前でサムネイルを作る形式（.jpg など）は
	// デコードに失敗して indexFile がエラーで終わるので載らないが、
	// .heic や .mp4 は「自前では作らない」形式なので Prepare が何もせずに
	// 成功し、行が入る。すると /file/{id} は借りるものが無いぶん原本の
	// パス――つまりリンクそのもの――を ServeFile に渡し、リンクを追った
	// 先の中身がブラウザへ出ていく。-data が -dir の外にあることは
	// config.Validate が確かめているが、リンクの先までは縛れないため、
	// 写真の共有フォルダに1本置くだけで sessions.db が読めることになる。
	//
	// ディレクトリへのリンクは、この判定が無くても降りられない。走査も監視も
	// filepath.WalkDir を使っており、WalkDir はリンクを追わないためである。
	// 通常ファイル以外をまとめて落とさないのは、FIFOやデバイスファイルが
	// 同じ危険を持たないからである。指す先が無いので、読めるのはそのファイル
	// 自身でしかない。
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}

	// 撮影日時の出どころは写真と動画で違う。静止画のEXIFは機種によらず時差を
	// 持たないローカル時刻だが、動画のコンテナは機種によって規約が割れるので、
	// videometa がブランドごとに解釈を変える。どちらもここで1回だけ読む。
	//
	// 向きは動画では常に1でよい。回転はブラウザが tkhd の行列を見て自分で当て、
	// 借りるサムネイルはSynologyが回転済みで書いている。famifoが動画の画素に
	// 触ることは無いので、向きを持ち回る意味がない。
	var takenAt time.Time
	orientation := uint16(1)
	if imagefmt.IsVideo(path) {
		takenAt = videometa.Read(path).TakenAt
	} else {
		meta := exif.Read(path)
		takenAt, orientation = meta.TakenAt, meta.Orientation
	}

	// Mediaを先に組み立てる。ModTime が原本の版であり、thumb はそれを見て出力の
	// 名前を決める。ここで確定させておけば、インデックスに載る版とサムネイルの
	// 名前に入る版が食い違いようがない。
	m := media.New(path, fi, takenAt)
	if err := ix.thumbs.Prepare(m, orientation); err != nil {
		return err
	}
	return ix.store.Upsert(ctx, m)
}

// removeFile はインデックスとサムネイルの両方から写真を消す。
// 未登録のパスに対しては何もしない。
func (ix *Indexer) removeFile(ctx context.Context, path string) error {
	p, ok, err := ix.store.DeleteByPath(ctx, path)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	// 出どころは見ない。Remove は自分の置き場しか触らないので、@eaDir から
	// 借りていた写真に対しては何も消さずに終わる。
	if err := ix.thumbs.Remove(p.ID()); err != nil {
		// DBからは消えているので、サムネイルの消し残しは致命的ではない
		ix.log.Warn("failed to delete the thumbnail", "id", p.ID(), "err", err)
	}
	return nil
}

// removeTree はdir配下に登録されている写真と動画を、まとめてインデックスと
// サムネイルの置き場から消す。ディレクトリのリネーム/移動はfsnotifyでは子ファイルごとの
// イベントが来ないため、パスの前方一致で一括削除する必要がある。
// 該当が無いパスに対しては何もしない。
func (ix *Indexer) removeTree(ctx context.Context, dir string) error {
	items, err := ix.store.DeleteByPathPrefix(ctx, dir)
	if err != nil {
		return err
	}
	for _, p := range items {
		if err := ix.thumbs.Remove(p.ID()); err != nil {
			// DBからは消えているので、サムネイルの消し残しは致命的ではない
			ix.log.Warn("failed to delete the thumbnail", "id", p.ID(), "err", err)
		}
	}
	return nil
}

// startIndex は枠が空くまで待ってから1枚の取り込みを始める。終わるのを待たずに返る。
//
// wg は呼び出し側の関門である。渡したものが「自分が出したぶん」になり、
// wg.Wait() は他の入口が出した取り込みを待たない。スキャンと監視が同時に走るため、
// 相手の完了まで待つと、相手が仕事を足し続ける限り返れなくなる。Add はこの中で
// 行うので、呼び出し側に作法は残らない。
//
// done は取り込みが終わったワーカーの上で呼ばれる。
func (ix *Indexer) startIndex(ctx context.Context, wg *sync.WaitGroup, path string, done func(error)) {
	ix.slots.take()
	ix.run(ctx, wg, path, done)
}

// tryStartIndex は枠が空いていれば取り込みを始め、埋まっていれば false を返す。
// 呼び出し側をブロックしない。渡せなかったパスは呼び出し側が持ったままにして、
// 空いてから渡し直す。
//
// 待つ版と待たない版が要るのは、入口で作法が逆になるためである。走査は枠が空くまで
// 待ってよく、むしろ待たないと数千件のパスを先に溜め込んでしまう。監視ループは
// 決して待てない。待った時間はそのまま fsnotify のイベントを読まない時間になり、
// カーネルのキューが溢れれば変更そのものを取りこぼす。
func (ix *Indexer) tryStartIndex(ctx context.Context, wg *sync.WaitGroup, path string, done func(error)) bool {
	if !ix.slots.tryTake() {
		return false
	}
	ix.run(ctx, wg, path, done)
	return true
}

// run は枠を確保済みの前提で1枚の取り込みを走らせる。
//
// 枠を空けるのは done を返したあとである。順序を逆にすると、通知を渡し終える
// 前に次の1枚が走り出せてしまい、通知の待ち行列が同時取り込み数を超えて伸びうる。
// 呼び出し側が容量をワーカー数で見積もれるよう、ここで閉じておく。
//
// defer は LIFO なので枠の返却が wg.Done() より先に走る。wg.Wait() が返った時点で
// 枠も空いていることが、busy() の意味を支えている。
func (ix *Indexer) run(ctx context.Context, wg *sync.WaitGroup, path string, done func(error)) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer ix.slots.release()
		done(ix.indexFile(ctx, path))
	}()
}

// busy は取り込みが1件でも走っているかを返す。誰が出した仕事かは区別しない。
//
// 取り込みの最中に写真が消えると、ワーカーが後から Upsert して存在しない
// パスの行が残りうる。その気配を監視が知るために使う。
//
// 枠は done を返し終えるまで空かないので、埋まっている枠があることは
// 「まだ Upsert していないワーカーが居る」ことと一致する。
func (ix *Indexer) busy() bool { return ix.slots.busy() }

// slots は同時に取り込める枠。容量が上限で、入っているぶんが使用中である。
// 中身はチャネル1本なので値で持ち回る。
//
// 枠を返すのは取り込みの完了通知を渡し終えたあとである（run を見よ）。この順序が
// あるので、使用中の枠があることは「まだ Upsert していないワーカーが居る」ことと
// 一致し、busy の答えが意味を持つ。
type slots struct{ ch chan struct{} }

// newSlots は上限 n の枠を作る。
func newSlots(n int) slots { return slots{ch: make(chan struct{}, n)} }

// take は枠が空くまで待って1つ取る。
func (s slots) take() { s.ch <- struct{}{} }

// tryTake は枠が空いていれば1つ取り、埋まっていれば false を返す。待たない。
func (s slots) tryTake() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// release は取った枠を1つ返す。
func (s slots) release() { <-s.ch }

// busy は使用中の枠が1つでもあるかを返す。
func (s slots) busy() bool { return len(s.ch) > 0 }

// cap は上限を返す。
func (s slots) cap() int { return cap(s.ch) }
