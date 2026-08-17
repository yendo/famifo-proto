package index

import (
	"context"
	"io/fs"
	"path/filepath"

	"github.com/yendo/famifo-proto/internal/photo"
)

// Stats はフルスキャンの結果。
type Stats struct {
	Indexed   int // 新規登録または更新した枚数
	Unchanged int // mtimeが変わらず再処理しなかった枚数
	Removed   int // ディスクから消えていたためインデックスから消した枚数
	Skipped   int // 破損・権限エラーで飛ばした枚数
}

// FullScan はルートディレクトリを走査してインデックスをディスクの実態に合わせる。
//
// fsnotifyはアプリが停止していた間の変更を検知できないため、起動のたびにこれを
// 実行して整合性を取り直す。個々のファイルのエラーは記録して走査を続け、
// コンテキストのキャンセルだけが全体を中断させる。
func (ix *Indexer) FullScan(ctx context.Context) (Stats, error) {
	known, err := ix.st.AllPaths(ctx)
	if err != nil {
		return Stats{}, err
	}

	var st Stats
	walkErr := filepath.WalkDir(ix.root, func(path string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// 読めないディレクトリやファイルは飛ばす（権限エラーなど）
			ix.log.Warn("走査をスキップ", "path", path, "err", err)
			st.Skipped++
			return nil
		}
		if d.IsDir() || !photo.IsSupported(path) {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			ix.log.Warn("ファイル情報を取得できずスキップ", "path", path, "err", err)
			st.Skipped++
			return nil
		}

		// 見つかったパスは消し込む。走査後に残ったものが削除されたファイル。
		modTime, wasKnown := known[path]
		delete(known, path)
		if wasKnown && modTime == fi.ModTime().Unix() {
			st.Unchanged++
			return nil
		}

		if err := ix.IndexFile(ctx, path); err != nil {
			ix.log.Warn("インデックスをスキップ", "path", path, "err", err)
			st.Skipped++
			return nil
		}
		st.Indexed++
		return nil
	})
	if walkErr != nil {
		return st, walkErr
	}

	for path := range known {
		if err := ix.RemoveFile(ctx, path); err != nil {
			ix.log.Warn("削除の反映に失敗", "path", path, "err", err)
			continue
		}
		st.Removed++
	}
	return st, nil
}
