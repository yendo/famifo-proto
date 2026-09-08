package index

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/synology"
)

// Stats はスキャンの結果。
type Stats struct {
	Indexed   int // 新規登録または更新した枚数
	Unchanged int // mtimeが変わらず再処理しなかった枚数
	Removed   int // ディスクから消えていたためインデックスから消した枚数
	Skipped   int // 破損・権限エラーで飛ばした枚数
}

// scanner は1回のスキャンが持ち回る帳簿。走査・集計・削除の3フェーズが同じ
// マップを見るため、フェーズをメソッドに割ってもこれらが共有され続ける。
//
// 1回のスキャンごとに作って捨てる。Scan の外には出ない。
type scanner struct {
	ix *Indexer
	// jobs はこのスキャンが出した取り込みの集まり。監視が同時に走るため、
	// 完了を待つ相手を自分が出したぶんに限る。
	jobs *jobs

	// known は登録済みのパスとそのmtime。走査で見つけたぶんを消し込み、
	// 残ったものが削除されたファイルになる。
	known map[string]int64
	// foundByRoot はルートごとの発見数。空/未マウントかどうかをルート単位で判定する
	// ために使う。合計で数えると、生きているルートに写真がある限りガードが
	// 発動しない。
	foundByRoot map[string]int
	// stats は走査側だけが書く。ワーカー側の集計は下の indexed/failed に分けてある。
	stats Stats

	// ワーカーは stats を直接触らない。走査側も Unchanged と Skipped を数えており、
	// 同じ構造体を両側から書くと、片方だけロックを忘れたときに気づけないため。
	mu              sync.Mutex
	indexed, failed int
}

// Scan はルートディレクトリを走査してインデックスをディスクの実態に合わせる。
//
// fsnotifyはアプリが停止していた間の変更を検知できないため、起動のたびにこれを
// 実行して整合性を取り直す。個々のファイルのエラーは記録して走査を続け、
// コンテキストのキャンセルだけが全体を中断させる。
func (ix *Indexer) Scan(ctx context.Context) (Stats, error) {
	known, err := ix.st.AllPaths(ctx)
	if err != nil {
		return Stats{}, err
	}
	s := &scanner{
		ix:          ix,
		jobs:        ix.executor.newJobs(),
		known:       known,
		foundByRoot: make(map[string]int, len(ix.roots)),
	}

	walkErr := s.walkAll(ctx)

	// 取り込みの完了を待ったあとなので、ワーカーの書き込みはすべて見えている。
	s.stats.Indexed = s.indexed
	s.stats.Skipped += s.failed
	if walkErr != nil {
		return s.stats, walkErr
	}

	s.purge(ctx)
	return s.stats, nil
}

// walkAll はすべてのルートを走査する。
//
// 戻る前に、中断であってもワーカーの完了まで待つ。待たずに戻ると、まだ動いて
// いるワーカーが indexed を書いている最中の値を呼び出し側が読むことになる。
func (s *scanner) walkAll(ctx context.Context) error {
	defer s.jobs.wait()

	for _, root := range s.ix.roots {
		if err := s.walk(ctx, root); err != nil {
			if ctx.Err() != nil {
				return err
			}
			// ルート自体を読めない（ボリュームが外れた等）。1つのドライブが
			// 外れただけで走査全体を止めると、生きているルートの更新まで
			// 反映されなくなる。このルートは foundByRoot が0のままなので、配下の
			// 削除は purge のガードが自動的に見送る。
			s.ix.log.Warn("ルートを読めないため飛ばした", "root", root, "err", err)
		}
	}
	return nil
}

// walk は1つのルート以下を走査する。
//
// 走査自体は直列のままにする。known の消し込みも foundByRoot の計上も、共有する
// マップの上での帳簿づけであり、並行にしても速くならないのに壊れる余地だけが
// 増える。時間を食う1枚の取り込みだけを submit でワーカーに出す。
func (s *scanner) walk(ctx context.Context, root string) error {
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
			s.ix.log.Warn("走査をスキップ", "path", path, "err", err)
			s.stats.Skipped++
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
		s.foundByRoot[root]++

		fi, err := d.Info()
		if err != nil {
			s.ix.log.Warn("ファイル情報を取得できずスキップ", "path", path, "err", err)
			s.stats.Skipped++
			return nil
		}

		// 見つかったパスは消し込む。走査後に残ったものが削除されたファイル。
		modTime, wasKnown := s.known[path]
		delete(s.known, path)
		if wasKnown && modTime == fi.ModTime().Unix() {
			s.stats.Unchanged++
			return nil
		}

		s.submit(ctx, path)
		return nil
	})
}

// submit は1枚の取り込みをワーカーに出す。
//
// ワーカーに出すのは取り込み（EXIFの読み取りとサムネイルの生成）だけである。
// 大半の時間は原本のデコードと縮小で、写真ごとに独立しているため。
//
// 持ち場が埋まっていればここで待つ。走査だけが先に走って数千件のパスを
// 溜め込むことがない。
func (s *scanner) submit(ctx context.Context, path string) {
	s.jobs.submit(ctx, path, func(err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case err == nil:
			s.indexed++
		case ctx.Err() != nil:
			// 中断で落ちたぶんを破損として数えない。Ctrl-Cのたびに身に
			// 覚えのないスキップ件数が出ることになるため、記録もしない。
		default:
			s.ix.log.Warn("インデックスをスキップ", "path", path, "err", err)
			s.failed++
		}
	})
}

// purge は走査で見つからなかった写真をインデックスから消す。
func (s *scanner) purge(ctx context.Context) {
	empty := s.emptyRoots()

	guarded := 0
	for path := range s.known {
		if isUnderAny(empty, path) {
			guarded++
			continue
		}
		if err := s.ix.RemoveFile(ctx, path); err != nil {
			s.ix.log.Warn("削除の反映に失敗", "path", path, "err", err)
			continue
		}
		s.stats.Removed++
	}
	if guarded > 0 {
		s.ix.log.Warn("走査結果が空のルートがあるため削除をスキップした",
			"roots", empty, "remaining", guarded)
	}
}

// emptyRoots は1枚も見つからなかったルートを返す。
//
// そのルートは、ドライブが未マウントで「たまたま空に見える」のか、本当に全部
// 消されたのかを区別できない。安全側に倒して、配下の削除を見送るために使う。
func (s *scanner) emptyRoots() []string {
	var empty []string
	for _, root := range s.ix.roots {
		if s.foundByRoot[root] == 0 {
			empty = append(empty, root)
		}
	}
	return empty
}

// isUnderAny は path がいずれかのルート配下にあるかを返す。
func isUnderAny(roots []string, path string) bool {
	for _, root := range roots {
		if isUnder(root, path) {
			return true
		}
	}
	return false
}

// isUnder は path が root 配下にあるかを返す。
// セパレータを1つ補ってから前方一致させるため、"/a" が "/ab" を巻き込まない。
func isUnder(root, path string) bool {
	if path == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(path, root)
}

// RunScans は interval ごとにスキャンを繰り返す。ctx がキャンセルされるまで戻らない。
//
// fsnotify は取りこぼす。キューが溢れたことは ErrEventOverflow で分かるが、
// max_user_watches を使い切って監視を張れなかったディレクトリのように、
// 取りこぼしたことを知る手立てが無い経路もある。繰り返し突き合わせ直せば、
// 検知できたかどうかによらず整合性が戻る。
//
// kicks は待ちを切り上げる要求である。スキャンの本数は増えず、次の1回が早まる
// だけになる。ループが逐次なのでスキャンが重なることはなく、「今走っているか」を
// 記録する必要もない。nil を渡せば時間だけで回る。
//
// 待たずに始める。アプリが止まっていた間の変更も fsnotify は検知できないため、
// 起動直後の1回目こそ必要になる。1回目を特別扱いせず、同じループの最初の回として
// 走らせる。
func (ix *Indexer) RunScans(ctx context.Context, interval time.Duration, kicks <-chan struct{}) {
	for {
		// 大量の写真では1回目に時間がかかる。開始も残さないと、走査中なのか
		// 止まっているのかがログから読めない。
		ix.log.Info("スキャンを開始", "dirs", ix.roots)
		start := time.Now()
		stats, err := ix.Scan(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			ix.log.Warn("スキャンに失敗", "err", err)
		} else {
			ix.log.Info("スキャンが完了",
				"elapsed", time.Since(start).Round(time.Millisecond),
				"indexed", stats.Indexed, "unchanged", stats.Unchanged,
				"removed", stats.Removed, "skipped", stats.Skipped)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-kicks:
		}
	}
}
