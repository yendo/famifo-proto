// Package index はディスク上の写真とSQLiteインデックスを同期させる。
// 起動時のフルスキャンとfsnotifyによる追従の両方をここで担う。
//
// 1枚を取り込む手順のうち、重いものはサブパッケージに置く。
// exif がEXIFを読み、thumb がサムネイルを調達する。どちらも取り込み時にしか
// 使わないので、internal/photo と横並びにはしない。
package index

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/yendo/famifo-proto/internal/index/exif"
	"github.com/yendo/famifo-proto/internal/index/thumb"
	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
)

// Indexer は1ファイル単位でインデックスを更新する。
type Indexer struct {
	roots         []string
	st            *store.Store
	thumbProvider *thumb.Provider
	log           *slog.Logger
}

// New はIndexerを作る。rootsは写真を収集するルートディレクトリ、
// thumbDir は生成したサムネイルの置き場所、thumbSize は長辺の最大ピクセル数。
//
// サムネイルの供給者はここで組み立てる。呼び出し側にとってサムネイルは
// インデックス作成の副産物であって、単体で持ち回る道具ではない。
func New(roots []string, st *store.Store, thumbDir string, thumbSize int, log *slog.Logger) (*Indexer, error) {
	thumbProvider, err := thumb.NewProvider(thumbDir, thumbSize)
	if err != nil {
		return nil, err
	}
	return &Indexer{roots: roots, st: st, thumbProvider: thumbProvider, log: log}, nil
}

// IndexFile は1ファイルをインデックスに反映する。
//
// 対象外の拡張子とディレクトリは黙って無視する（エラーではない）。
// 自前で作るしかないファイルでサムネイルを作れなかった場合はエラーを返し、
// DBには登録しない。壊れた画像を登録すると一覧に読み込めない <img> が並ぶため。
func (ix *Indexer) IndexFile(ctx context.Context, path string) error {
	if !photo.IsSupportedFile(path) {
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

	p := photo.New(path, fi, m.TakenAt)
	if err := ix.thumbProvider.ResolveSource(&p, m.Orientation); err != nil {
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
	if p.HasFamifoThumb() {
		if err := ix.thumbProvider.Remove(p.ID()); err != nil {
			// DBからは消えているので、サムネイルの消し残しは致命的ではない
			ix.log.Warn("サムネイルの削除に失敗", "id", p.ID(), "err", err)
		}
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
		if !p.HasFamifoThumb() {
			continue
		}
		if err := ix.thumbProvider.Remove(p.ID()); err != nil {
			// DBからは消えているので、サムネイルの消し残しは致命的ではない
			ix.log.Warn("サムネイルの削除に失敗", "id", p.ID(), "err", err)
		}
	}
	return nil
}
