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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/index/exif"
	"github.com/yendo/famifo-proto/internal/index/videometa"
	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// Indexer は写真1枚の変更をインデックスとサムネイルに反映する。どこを見てどこに
// 書くかを持つ。インデックスのデータ自体は store にある。
//
// どれも同期的で、並行について何も知らない。いつ、いくつ同時に走らせるかは
// 取り込みを起こす入口（Scanner と Watcher）が Slots を使って決める。
type Indexer struct {
	roots  []string
	store  *store.Store
	thumbs *thumb.Provider
	log    *slog.Logger
}

// NewIndexer はIndexerを作る。rootsは写真を収集するルートディレクトリ。
//
// thumbs は配信側と共有する。同じ置き場所を指す設定値を2経路に配ると、
// ずれても誰も気づけないため、組み立てたものを1つ受け取る。
func NewIndexer(roots []string, st *store.Store, thumbs *thumb.Provider, log *slog.Logger) *Indexer {
	return &Indexer{
		roots:  roots,
		store:  st,
		thumbs: thumbs,
		log:    log,
	}
}

// indexFile は1ファイルをインデックスに反映する。載せられなかったときはエラーを
// 返し、そのパスの行とサムネイルを残さない。
//
// 渡すのは、パスだけで決まる条件を入口が確かめたものに限る。拡張子が対応形式で
// あること、Synologyの管理用ディレクトリの下に無いことである。入口はどのみち
// これで絞っている（枠や保留を写真でないものに使わないため）ので、ここでは
// 見直さない。中身の照合はこの前提に依っている。対応外の拡張子では照合の相手が
// application/octet-stream になり、どんな中身も通ってしまう。
//
// ここで見るのはファイルの今の状態で決まる条件である。入口が見てから読むまでの
// 間に変わりうるので、読む直前にしか確かめられない。
//
// 自前で作るしかないファイルでサムネイルを作れなかった場合もエラーにする。壊れた
// 画像を登録すると一覧に読み込めない <img> が並ぶため。
func (ix *Indexer) indexFile(ctx context.Context, path string) (err error) {
	// 断ったら行を消す。登録済みのパスが取り込めない状態に変わったとき、行を
	// 消せるのはここしかない。走査は見つけた時点で消し込んでおり、監視には
	// rename で被せられたときの Remove が来ない。残すと、照合を通っていない
	// 中身が元のIDのまま配信される。
	//
	// 中断で落ちたものは、断ったのではないので消さない。消すと次の起動で
	// 全部を取り込み直すことになる。
	defer func() {
		if err != nil && ctx.Err() == nil {
			err = errors.Join(err, ix.removeFile(ctx, path))
		}
	}()

	// Lstat で見る。Stat はリンクを追うので、写真ディレクトリに置かれた
	// シンボリックリンクを、その先にあるファイルとして取り込んでしまう。
	//
	// ディレクトリかどうかは見ない。入口はディレクトリを渡さず、読むまでの間に
	// 置き換わったとしても、下の中身の読み取りが失敗する。
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cannot stat the file: %w", err)
	}
	// シンボリックリンクは載せない。
	//
	// 載せると配信できてしまう。行が入れば /file/{id} は原本のパス――つまり
	// リンクそのもの――を ServeFile に渡し、リンクを追った先の中身がブラウザへ
	// 出ていく。-data が -dir の外にあることは config.Validate が確かめているが、
	// リンクの先までは縛れないため、写真の共有フォルダに1本置くだけで
	// sessions.db が読めることになる。
	//
	// ディレクトリへのリンクは、この判定が無くても降りられない。走査も監視も
	// filepath.WalkDir を使っており、WalkDir はリンクを追わないためである。
	// 通常ファイル以外をまとめて落とさないのは、FIFOやデバイスファイルが
	// 同じ危険を持たないからである。指す先が無いので、読めるのはそのファイル
	// 自身でしかない。
	if fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("the file is a symbolic link")
	}

	// 中身が拡張子のとおりかを見る。拡張子だけを根拠に行を作ると、名前を
	// a.heic に変えただけのファイルにIDが振られ、配信される。デコードする形式なら
	// thumb が弾くと思いがちだが、@eaDir から借りられる場合も、その版のサムネイルが
	// 既にある場合も原本は開かれない。どの形式にも裏付けが要る。
	//
	// 拡張子からのMIMEタイプと中身から判定したMIMEタイプを突き合わせる。判定は
	// mimetype に任せる。mimetype の型は木になっていて、3GPやHEICはMP4の子として
	// 出てくるので、親まで遡って比べる。
	detected, err := mimetype.DetectFile(path)
	if err != nil {
		return fmt.Errorf("cannot read the file: %w", err)
	}
	want := imagefmt.ContentType(path)
	t := detected
	for t != nil && !t.Is(want) {
		t = t.Parent()
	}
	if t == nil {
		return fmt.Errorf("the content of %s is %s, not what its extension says", path, detected)
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
	m, ok, err := ix.store.DeleteByPath(ctx, path)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	// 出どころは見ない。RemoveFamifoThumbs は自分の置き場しか触らないので、@eaDir から
	// 借りていた写真に対しては何も消さずに終わる。
	if err := ix.thumbs.RemoveFamifoThumbs(m.ID()); err != nil {
		// DBからは消えているので、サムネイルの消し残しは致命的ではない
		ix.log.Warn("failed to delete the thumbnail", "id", m.ID(), "err", err)
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
	for _, m := range items {
		if err := ix.thumbs.RemoveFamifoThumbs(m.ID()); err != nil {
			// DBからは消えているので、サムネイルの消し残しは致命的ではない
			ix.log.Warn("failed to delete the thumbnail", "id", m.ID(), "err", err)
		}
	}
	return nil
}
