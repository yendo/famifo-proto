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
	"time"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/index/exif"
	"github.com/yendo/famifo-proto/internal/index/videometa"
	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// Indexer は1ファイル単位でインデックスを更新する。
type Indexer struct {
	roots    []string
	st       *store.Store
	thumbs   *thumb.Provider
	workers  int
	executor *executor
	log      *slog.Logger
}

// New はIndexerを作る。rootsは写真を収集するルートディレクトリ。
//
// thumbs は配信側と共有する。同じ置き場所を指す設定値を2経路に配ると、
// ずれても誰も気づけないため、組み立てたものを1つ受け取る。
//
// workers は同時に取り込む枚数。1未満は1として扱う。適正値はCPU数とストレージの
// 待ち時間で決まり、NASとローカルで違うため設定から来る。
//
// スキャンも監視も同じ executor を通すので、この上限は取り込みの入口に
// よらず効く。ディレクトリごと移動された場合、監視にも一度に数百件が来る。
func New(roots []string, st *store.Store, thumbs *thumb.Provider, workers int, log *slog.Logger) *Indexer {
	if workers < 1 {
		workers = 1
	}
	ix := &Indexer{roots: roots, st: st, thumbs: thumbs, workers: workers, log: log}
	ix.executor = newExecutor(ix)
	return ix
}

// indexing は取り込みが1件でも走っているかを返す。誰が出した仕事かは区別しない。
//
// 取り込みの最中に写真が消えると、ワーカーが後から Upsert して存在しない
// パスの行が残りうる。その気配を監視が知るために使う。
func (ix *Indexer) indexing() bool { return ix.executor.busy() }

// indexFile は1ファイルをインデックスに反映する。
//
// 対象外の拡張子とディレクトリは黙って無視する（エラーではない）。
// 自前で作るしかないファイルでサムネイルを作れなかった場合はエラーを返し、
// DBには登録しない。壊れた画像を登録すると一覧に読み込めない <img> が並ぶため。
func (ix *Indexer) indexFile(ctx context.Context, path string) error {
	if !imagefmt.IsSupported(path) {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot stat the file: %w", err)
	}
	if fi.IsDir() {
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
	return ix.st.Upsert(ctx, m)
}

// removeFile はインデックスとサムネイルの両方から写真を消す。
// 未登録のパスに対しては何もしない。
func (ix *Indexer) removeFile(ctx context.Context, path string) error {
	p, ok, err := ix.st.DeleteByPath(ctx, path)
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

// removeTree はdir配下に登録されている写真を、まとめてインデックスと
// サムネイルの置き場から消す。ディレクトリのリネーム/移動はfsnotifyでは子ファイルごとの
// イベントが来ないため、パスの前方一致で一括削除する必要がある。
// 該当が無いパスに対しては何もしない。
func (ix *Indexer) removeTree(ctx context.Context, dir string) error {
	photos, err := ix.st.DeleteByPathPrefix(ctx, dir)
	if err != nil {
		return err
	}
	for _, p := range photos {
		if err := ix.thumbs.Remove(p.ID()); err != nil {
			// DBからは消えているので、サムネイルの消し残しは致命的ではない
			ix.log.Warn("failed to delete the thumbnail", "id", p.ID(), "err", err)
		}
	}
	return nil
}
