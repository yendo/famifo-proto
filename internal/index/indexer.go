// Package index はディスク上の写真とSQLiteインデックスを同期させる。
// 起動時のフルスキャンとfsnotifyによる追従の両方をここで担う。
//
// 1枚を取り込む手順のうち、EXIFの読み取りは exif が担う。取り込み時にしか
// 使わないのでサブパッケージに置く。
//
// サムネイルの調達は internal/thumb が担う。こちらは配信側とも共有するため、
// 呼び出し側が組み立てたものを受け取る。
package index

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/index/exif"
	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// Indexer は1ファイル単位でインデックスを更新する。
type Indexer struct {
	roots  []string
	st     *store.Store
	thumbs *thumb.Provider
	log    *slog.Logger
}

// New はIndexerを作る。rootsは写真を収集するルートディレクトリ。
//
// thumbs は配信側と共有する。同じ置き場所を指す設定値を2経路に配ると、
// ずれても誰も気づけないため、組み立てたものを1つ受け取る。
func New(roots []string, st *store.Store, thumbs *thumb.Provider, log *slog.Logger) *Indexer {
	return &Indexer{roots: roots, st: st, thumbs: thumbs, log: log}
}

// IndexFile は1ファイルをインデックスに反映する。
//
// 対象外の拡張子とディレクトリは黙って無視する（エラーではない）。
// 自前で作るしかないファイルでサムネイルを作れなかった場合はエラーを返し、
// DBには登録しない。壊れた画像を登録すると一覧に読み込めない <img> が並ぶため。
func (ix *Indexer) IndexFile(ctx context.Context, path string) error {
	if !imagefmt.IsSupported(path) {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("ファイル情報を取得できません: %w", err)
	}
	if fi.IsDir() {
		return nil
	}

	// EXIFはここで1回だけ読む。撮影日時とサムネイルの向きの両方がこの1回で
	// 決まるので、写真1枚につきEXIFのパースは1回で済む。
	m := exif.Read(path)

	// Photoを先に組み立てる。ModTime が原本の版であり、thumb はそれを見て出力の
	// 名前を決める。ここで確定させておけば、インデックスに載る版とサムネイルの
	// 名前に入る版が食い違いようがない。
	p := photo.New(path, fi, m.TakenAt)
	if err := ix.thumbs.Prepare(p, m.Orientation); err != nil {
		return err
	}
	return ix.st.Upsert(ctx, p)
}

// RemoveFile はインデックスとサムネイルの両方から写真を消す。
// 未登録のパスに対しては何もしない。
func (ix *Indexer) RemoveFile(ctx context.Context, path string) error {
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
		ix.log.Warn("サムネイルの削除に失敗", "id", p.ID(), "err", err)
	}
	return nil
}

// RemoveTree はdir配下に登録されている写真を、まとめてインデックスと
// サムネイルの置き場から消す。ディレクトリのリネーム/移動はfsnotifyでは子ファイルごとの
// イベントが来ないため、パスの前方一致で一括削除する必要がある。
// 該当が無いパスに対しては何もしない。
func (ix *Indexer) RemoveTree(ctx context.Context, dir string) error {
	photos, err := ix.st.DeleteByPathPrefix(ctx, dir)
	if err != nil {
		return err
	}
	for _, p := range photos {
		if err := ix.thumbs.Remove(p.ID()); err != nil {
			// DBからは消えているので、サムネイルの消し残しは致命的ではない
			ix.log.Warn("サムネイルの削除に失敗", "id", p.ID(), "err", err)
		}
	}
	return nil
}
