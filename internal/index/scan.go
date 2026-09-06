package index

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/synology"
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
	// ルートごとの発見数。空/未マウントかどうかをルート単位で判定するために使う。
	// 合計で数えると、生きているルートに写真がある限りガードが発動しない。
	found := make(map[string]int, len(ix.roots))

	// 1枚の取り込み（EXIFの読み取りとサムネイルの生成）だけをワーカーに出す。
	// 大半の時間は原本のデコードと縮小で、写真ごとに独立しているため。
	//
	// walk 自体は直列のままにする。known の消し込みも found の計上も、共有する
	// マップの上での帳簿づけであり、並行にしても速くならないのに壊れる余地だけ
	// が増える。
	//
	// sem はワーカーの持ち場で、埋まっていれば walk がそこで待つ。走査だけが
	// 先に走って数千件のパスを溜め込むことがない。
	sem := make(chan struct{}, ix.workers)
	var wg sync.WaitGroup
	// ワーカーは st を直接触らない。walk 側も Unchanged と Skipped を数えており、
	// 同じ構造体を両側から書くと、片方だけロックを忘れたときに気づけないため。
	var mu sync.Mutex
	var indexed, failed int

	dispatch := func(path string) {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			err := ix.IndexFile(ctx, path)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				indexed++
			case ctx.Err() != nil:
				// 中断で落ちたぶんを破損として数えない。Ctrl-Cのたびに身に
				// 覚えのないスキップ件数が出ることになるため、記録もしない。
			default:
				ix.log.Warn("インデックスをスキップ", "path", path, "err", err)
				failed++
			}
		}()
	}

	walk := func(root string) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if path == root {
					// ルート自体が読めない場合は「中身が空だった」と区別できないため
					// 削除フェーズに進まず、走査全体を中断する。
					return err
				}
				// 読めないディレクトリやファイルは飛ばす（権限エラーなど）
				ix.log.Warn("走査をスキップ", "path", path, "err", err)
				st.Skipped++
				return nil
			}
			if d.IsDir() {
				if synology.IsManagedDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if !imagefmt.IsSupported(path) {
				return nil
			}
			found[root]++

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

			dispatch(path)
			return nil
		})
	}

	// 走査を終えたら、中断であってもワーカーの完了まで待つ。待たずに戻ると、
	// まだ動いているワーカーが indexed を書いている最中の値を返すことになる。
	walkErr := func() error {
		defer wg.Wait()
		for _, root := range ix.roots {
			if err := walk(root); err != nil {
				if ctx.Err() != nil {
					return err
				}
				// ルート自体を読めない（ボリュームが外れた等）。1つのドライブが
				// 外れただけで走査全体を止めると、生きているルートの更新まで
				// 反映されなくなる。このルートは found が0のままなので、配下の
				// 削除は下のガードが自動的に見送る。
				ix.log.Warn("ルートを読めないため飛ばした", "root", root, "err", err)
			}
		}
		return nil
	}()

	// wg.Wait のあとなので、ワーカーの書き込みはすべて見えている。
	st.Indexed = indexed
	st.Skipped += failed
	if walkErr != nil {
		return st, walkErr
	}

	// 1件も見つからなかったルートは、ドライブが未マウントで「たまたま空に
	// 見える」のか、本当に全部消されたのかを区別できない。安全側に倒して、
	// そのルート配下の削除を見送る。
	var empty []string
	for _, root := range ix.roots {
		if found[root] == 0 {
			empty = append(empty, root)
		}
	}

	guarded := 0
	for path := range known {
		if underAny(empty, path) {
			guarded++
			continue
		}
		if err := ix.RemoveFile(ctx, path); err != nil {
			ix.log.Warn("削除の反映に失敗", "path", path, "err", err)
			continue
		}
		st.Removed++
	}
	if guarded > 0 {
		ix.log.Warn("走査結果が空のルートがあるため削除をスキップした",
			"roots", empty, "remaining", guarded)
	}
	return st, nil
}

// underAny は path がいずれかのルート配下にあるかを返す。
func underAny(roots []string, path string) bool {
	for _, root := range roots {
		if under(root, path) {
			return true
		}
	}
	return false
}

// under は path が root 配下にあるかを返す。
// セパレータを1つ補ってから前方一致させるため、"/a" が "/ab" を巻き込まない。
func under(root, path string) bool {
	if path == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(path, root)
}
