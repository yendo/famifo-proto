// Package index はディスク上の写真とSQLiteインデックスを同期させる。
// 起動時のフルスキャンとfsnotifyによる追従の両方をここで担う。
package index

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/takenat"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// Indexer は1ファイル単位でインデックスを更新する。
type Indexer struct {
	root string
	st   *store.Store
	gen  *thumb.Generator
	log  *slog.Logger
}

// New はIndexerを作る。rootは写真を収集するルートディレクトリ。
func New(root string, st *store.Store, gen *thumb.Generator, log *slog.Logger) *Indexer {
	return &Indexer{root: root, st: st, gen: gen, log: log}
}

// IndexFile は1ファイルをインデックスに反映する。
//
// 対象外の拡張子とディレクトリは黙って無視する（エラーではない）。
// KindRasterでサムネイルを作れなかった場合はエラーを返し、DBには登録しない。
// 壊れた画像を登録すると一覧に読み込めない <img> が並んでしまうため。
func (ix *Indexer) IndexFile(ctx context.Context, path string) error {
	kind := photo.KindOf(path)
	if kind == photo.KindUnsupported {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("ファイル情報を取得できません: %w", err)
	}
	if fi.IsDir() {
		return nil
	}

	p := store.Photo{
		ID:      store.IDFor(path),
		Path:    path,
		ModTime: fi.ModTime(),
		Size:    fi.Size(),
		Ext:     strings.ToLower(filepath.Ext(path)),
		TakenAt: takenat.Resolve(path, fi.ModTime()),
	}

	if kind == photo.KindRaster {
		if err := ix.gen.Generate(path, p.ID); err != nil {
			return err
		}
		p.HasThumb = true
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
	if p.HasThumb {
		if err := ix.gen.Remove(p.ID); err != nil {
			// DBからは消えているので、キャッシュの消し残しは致命的ではない
			ix.log.Warn("サムネイルの削除に失敗", "id", p.ID, "err", err)
		}
	}
	return nil
}

// RemoveTree はdir配下に登録されている写真をまとめてインデックスとサムネイル
// キャッシュから消す。ディレクトリのリネーム/移動はfsnotifyでは子ファイルごとの
// イベントが来ないため、パスの前方一致で一括削除する必要がある。
// 該当が無いパスに対しては何もしない。
func (ix *Indexer) RemoveTree(ctx context.Context, dir string) error {
	photos, err := ix.st.DeleteByPathPrefix(ctx, dir)
	if err != nil {
		return err
	}
	for _, p := range photos {
		if !p.HasThumb {
			continue
		}
		if err := ix.gen.Remove(p.ID); err != nil {
			// DBからは消えているので、キャッシュの消し残しは致命的ではない
			ix.log.Warn("サムネイルの削除に失敗", "id", p.ID, "err", err)
		}
	}
	return nil
}
