// Package store は写真と動画のメタデータのSQLiteインデックスを提供する。
// 実体はファイルシステム上にあり、ここではパスとメタデータだけを持つ。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yendo/famifo-proto/internal/media"
	_ "modernc.org/sqlite" // pure Goのsqliteドライバ。cgo不要。
)

// ErrNotFound は該当する1件が無いことを表す。
var ErrNotFound = errors.New("media not found")

// Filter は一覧の絞り込み。各フィールドは1つの条件で、指定したものをすべて満たす
// 写真に絞る。ゼロ値は普段の一覧である。
//
// 呼び出し側は構造体リテラルで一度に組み立てる。条件は URL に書かれた組をまとめて
// 掛けるもので、1つずつ積んでいくものではない。
type Filter struct {
	// Kinds は種類。どれかの種類に絞る。空なら種類で絞らない。
	Kinds []media.Kind

	// Owners は持ち主の uid。どれかが持ち主の写真に絞る。空なら持ち主で絞らない。
	// 「指定なし」を空で表すので、uid 0（root）と取り違えない。
	Owners []uint32
}

// Store はSQLiteインデックスへのアクセスを提供する。
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS media (
    id             TEXT PRIMARY KEY,
    path           TEXT NOT NULL UNIQUE,
    owner_uid      INTEGER NOT NULL,
    taken_at_local TEXT NOT NULL,
    mod_time       INTEGER NOT NULL,
    day            TEXT NOT NULL,
    kind           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_order ON media(taken_at_local DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_media_day ON media(day);
`

// Open はDBを開き、スキーマを作成する。親ディレクトリが無ければ作る。
// WALを有効にしてスキャン中の書き込みと配信中の読み取りを並行させる。
func Open(dbPath string) (*Store, error) {
	// SQLiteは親ディレクトリを作らない。無いまま開くと sql.Open は遅延接続なので
	// 成功し、db.Ping() が "unable to open database file" で落ちる。原因の読めない
	// エラーになるうえ、呼び出し順への暗黙の依存を残すのでここで作る。
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("cannot create the database directory: %w", err)
	}

	dsn := dbPath + "?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cannot open the database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot connect to the database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot create the schema: %w", err)
	}
	// スキーマを作れたことと読めることは別である。CREATE ... IF NOT EXISTS は
	// 既にある表と索引に触れないので、列を変えた古いDBが残っていると、ここまで
	// 成功したうえで最初の読み取りで落ちる。移行は書かず作り直す運用なので、
	// その取り違えは起こる。配信を始めてから気づくのでは遅い。
	if err := probeReadable(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// probeReadable はアプリが使う列をひと通り選んで、DBが実際に読めることを
// 確かめる。行の有無は問わないので LIMIT 1 で足り、値も取り出さない。day と kind は
// 復元には読まないが、一覧の区切りと絞り込みで使うので一緒に確かめる。
func probeReadable(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, ` + selectCols + `, day, kind FROM media LIMIT 1`)
	if err != nil {
		return fmt.Errorf("cannot read the database: %w", err)
	}
	rows.Close()
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

const upsertSQL = `
INSERT INTO media (id, path, owner_uid, taken_at_local, mod_time, day, kind)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    path           = excluded.path,
    owner_uid      = excluded.owner_uid,
    taken_at_local = excluded.taken_at_local,
    mod_time       = excluded.mod_time,
    day            = excluded.day,
    kind           = excluded.kind`

// Upsert は1件を登録または更新する。
func (s *Store) Upsert(ctx context.Context, m media.Media) error {
	_, err := s.db.ExecContext(ctx, upsertSQL,
		m.ID(), m.Path(), int64(m.OwnerUID()), m.TakenAtLocal(), m.ModTime().Unix(), m.Day(), string(m.Kind()))
	if err != nil {
		return fmt.Errorf("cannot save the media (%s): %w", m.Path(), err)
	}
	return nil
}

// GetByID はIDで1件を引く。見つからない場合は ErrNotFound を返す。
func (s *Store) GetByID(ctx context.Context, id string) (media.Media, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM media WHERE id = ?`, id)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Media{}, ErrNotFound
	}
	if err != nil {
		return media.Media{}, fmt.Errorf("cannot get the media: %w", err)
	}
	return m, nil
}

// DeleteByPath はパスで1件を削除し、削除した行を返す。
// 呼び出し側は返った行のIDでサムネイルを消す。
// 該当が無い場合は ok=false を返し、エラーにはしない。
func (s *Store) DeleteByPath(ctx context.Context, path string) (media.Media, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM media WHERE path = ? RETURNING `+selectCols, path)
	m, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Media{}, false, nil
	}
	if err != nil {
		return media.Media{}, false, fmt.Errorf("cannot delete the media (%s): %w", path, err)
	}
	return m, true, nil
}

// DeleteByPathPrefix はディレクトリ配下の登録をまとめて削除し、削除した行を返す。
// 範囲は dirRange が決める。
//
// 前方一致は LIKE ではなく範囲比較で書く。LIKE の前方一致の最適化は
// ESCAPE 句があると効かず、削除1回ごとに media の全行を舐めることになる。
// path は UNIQUE なので暗黙の索引があり、範囲比較ならそれが使われる。
//
// 空のprefixは拒む。補うと "/" になり、絶対パスの全行が範囲に入るため、
// インデックスが丸ごと消える。
func (s *Store) DeleteByPathPrefix(ctx context.Context, prefix string) ([]media.Media, error) {
	if prefix == "" {
		return nil, errors.New("prefix must not be empty")
	}
	lo, hi := dirRange(prefix)

	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM media WHERE path >= ? AND path < ? RETURNING `+selectCols,
		lo, hi)
	if err != nil {
		return nil, fmt.Errorf("cannot delete the media under the directory (%s): %w", prefix, err)
	}
	defer rows.Close()

	var out []media.Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("cannot read the deletion result: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// dirRange はディレクトリ配下を前方一致で引くための下限と上限を返す。
// セパレータを1つ補ってから上限を作るため、"album" が "album2" のような
// 兄弟ディレクトリを巻き込むことはない。
//
// 上限は末尾のバイトを1つ進めた値で、下限で始まるどの文字列よりも大きい最小の値に
// なる。TEXTの既定の照合順序はBINARYなので、バイト単位で進めれば比較と食い違わない。
// 補ったあとの末尾は必ずセパレータなので、0xFFで桁上がりすることはない。
//
// 補う処理と上限の計算を1つにまとめてあるのは、「末尾がセパレータである」という
// 前提を関数の外に置かないためである。分けると呼び出し側の規約になり、守られて
// いるかどうかがコードから見えなくなる。
func dirRange(prefix string) (lo, hi string) {
	lo = prefix
	if !strings.HasSuffix(lo, string(filepath.Separator)) {
		lo += string(filepath.Separator)
	}
	b := []byte(lo)
	b[len(b)-1]++
	return lo, string(b)
}

// ListRange は撮影日時の新しい順で offset 番目から limit 件を返す。
// 仮想スクロールは任意の位置へ飛ぶため、カーソルではなくオフセットで引く。
func (s *Store) ListRange(ctx context.Context, f Filter, offset, limit int) ([]media.Media, error) {
	if offset < 0 {
		return nil, fmt.Errorf("offset must be 0 or greater: %d", offset)
	}
	if limit < 0 {
		return nil, fmt.Errorf("limit must be 0 or greater: %d", limit)
	}

	where, args := f.where()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM media
		 WHERE `+where+`
		 ORDER BY taken_at_local DESC, id DESC
		 LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, fmt.Errorf("cannot list the media: %w", err)
	}
	defer rows.Close()

	var out []media.Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("cannot read the media list: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RankOf は一覧の並びでその1件が何番目かを返す（先頭が0）。1件ごとのURLから
// 開くとき、クライアントは通し番号で位置を決めるため、IDから番号へ引き直す経路が要る。
// 該当が無い場合は ErrNotFound を返す。f に当てはまらない写真も ErrNotFound である。
//
// 並びは ListRange と同じ taken_at_local DESC, id DESC でなければならない。順序式が
// 二重になるが、片方だけ変えるとURLが別の1件の位置を指すため、
// TestRankOfLocatesTheItemInListRange が両者の一致を縛っている。
func (s *Store) RankOf(ctx context.Context, f Filter, id string) (int, error) {
	where, args := f.where()
	var takenAtLocal string
	err := s.db.QueryRowContext(ctx,
		`SELECT taken_at_local FROM media WHERE id = ? AND (`+where+`)`,
		append([]any{id}, args...)...).Scan(&takenAtLocal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("cannot get the media: %w", err)
	}

	// 自分より前に並ぶ行を数える。OFFSET で数えるのと違い、途中の行を読まずに
	// 索引だけで答えが出る。持ち主や種類で絞るとその列を見るために行も読むが、
	// 写真の数からして問題にならないので、それらを含む索引は持たない。
	var rank int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media
		 WHERE (`+where+`) AND (taken_at_local > ? OR (taken_at_local = ? AND id > ?))`,
		append(args, takenAtLocal, takenAtLocal, id)...).Scan(&rank)
	if err != nil {
		return 0, fmt.Errorf("cannot count the media before it: %w", err)
	}
	return rank, nil
}

// AllPaths は登録済みの全パスとそのmtimeを返す。スキャンでの差分検出に使う。
func (s *Store) AllPaths(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, mod_time FROM media`)
	if err != nil {
		return nil, fmt.Errorf("cannot list the paths: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var path string
		var modTime int64
		if err := rows.Scan(&path, &modTime); err != nil {
			return nil, fmt.Errorf("cannot read the path list: %w", err)
		}
		out[path] = modTime
	}
	return out, rows.Err()
}

// Count は f に当てはまる件数を返す。
func (s *Store) Count(ctx context.Context, f Filter) (int, error) {
	where, args := f.where()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media WHERE `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("cannot count the media: %w", err)
	}
	return n, nil
}

// DayGroup は、その日に撮った件数。一覧の区切りに使う。
type DayGroup struct {
	Date  string // "2006-01-02" 形式。撮影地の日
	Count int
}

// DayGroups は日ごとの件数を新しい順に返す。一覧の区切りとスクラバーの目盛りに使う。
//
// day に索引があるので、SQLiteは索引を順に読むだけで束ね、一時B-treeに積み直さない。
func (s *Store) DayGroups(ctx context.Context, f Filter) ([]DayGroup, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, COUNT(*) FROM media WHERE `+where+` GROUP BY day ORDER BY day DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("cannot get the capture dates: %w", err)
	}
	defer rows.Close()

	var out []DayGroup
	for rows.Next() {
		var g DayGroup
		if err := rows.Scan(&g.Date, &g.Count); err != nil {
			return nil, fmt.Errorf("cannot read the capture dates: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Owners は写真のある持ち主の uid を、重複なしで小さい順に返す。撮影者の選択肢の
// 元にする。名前を引くのは配信側である。
func (s *Store) Owners(ctx context.Context) ([]uint32, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT owner_uid FROM media ORDER BY owner_uid`)
	if err != nil {
		return nil, fmt.Errorf("cannot list the owners: %w", err)
	}
	defer rows.Close()

	var out []uint32
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, fmt.Errorf("cannot read the owners: %w", err)
		}
		out = append(out, uint32(uid))
	}
	return out, rows.Err()
}

// selectCols は scanMedia の Scan と列順を合わせるための並び。idは含めない。
// パスから導ける値なので、復元は media.Restore に任せる。day も同じく
// taken_at_local から、kind もパスから導けるので含めない。
const selectCols = `path, owner_uid, taken_at_local, mod_time`

// scanMedia は1行を復元する。引数の並びは selectCols と一致していなければならない。
func scanMedia(row interface{ Scan(...any) error }) (media.Media, error) {
	var path, takenAtLocal string
	var ownerUID uint32
	var modTime int64
	if err := row.Scan(&path, &ownerUID, &takenAtLocal, &modTime); err != nil {
		return media.Media{}, err
	}
	return media.Restore(path, ownerUID, takenAtLocal, time.Unix(modTime, 0)), nil
}

// where は f の条件を WHERE 句の中身とその引数にする。条件が無ければ常に真の "1" を
// 返す。条件はすべてここで組み立て、4つの読み取りがずれないようにする。
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	if len(f.Kinds) > 0 {
		conds = append(conds, "kind IN (?"+strings.Repeat(", ?", len(f.Kinds)-1)+")")
		for _, k := range f.Kinds {
			args = append(args, string(k))
		}
	}
	if len(f.Owners) > 0 {
		conds = append(conds, "owner_uid IN (?"+strings.Repeat(", ?", len(f.Owners)-1)+")")
		for _, uid := range f.Owners {
			args = append(args, int64(uid))
		}
	}
	if len(conds) == 0 {
		return "1", nil
	}
	return strings.Join(conds, " AND "), args
}
