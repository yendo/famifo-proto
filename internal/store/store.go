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

// Store はSQLiteインデックスへのアクセスを提供する。
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS media (
    id        TEXT PRIMARY KEY,
    path      TEXT NOT NULL UNIQUE,
    taken_at  INTEGER NOT NULL,
    mod_time  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_media_order ON media(taken_at DESC, id DESC);
`

// Open はDBを開き、スキーマを作成する。親ディレクトリが無ければ作る。
// WALを有効にしてスキャン中の書き込みと配信中の読み取りを並行させる。
func Open(dbPath string) (*Store, error) {
	// SQLiteは親ディレクトリを作らない。無いまま開くと sql.Open は遅延接続なので
	// 成功し、db.Ping() が "unable to open database file" で落ちる。原因の読めない
	// エラーになるうえ、呼び出し順への暗黙の依存を残すのでここで作る。
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create the database directory: %w", err)
	}

	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
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

// probeReadable はアプリが読む列をひと通り選んで、DBが実際に読めることを
// 確かめる。行の有無は問わないので LIMIT 1 で足り、値も取り出さない。
func probeReadable(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, ` + selectCols + ` FROM media LIMIT 1`)
	if err != nil {
		return fmt.Errorf("cannot read the database: %w", err)
	}
	defer rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cannot read the database: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

const upsertSQL = `
INSERT INTO media (id, path, taken_at, mod_time)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    path      = excluded.path,
    taken_at  = excluded.taken_at,
    mod_time  = excluded.mod_time`

// Upsert は1件を登録または更新する。
func (s *Store) Upsert(ctx context.Context, p media.Media) error {
	_, err := s.db.ExecContext(ctx, upsertSQL,
		p.ID(), p.Path(), p.TakenAt().Unix(), p.ModTime().Unix())
	if err != nil {
		return fmt.Errorf("cannot save the media (%s): %w", p.Path(), err)
	}
	return nil
}

// idは読まない。パスから導ける値なので、復元は media.Restore に任せる。
const selectCols = `path, taken_at, mod_time`

// GetByID はIDで1件を引く。見つからない場合は ErrNotFound を返す。
func (s *Store) GetByID(ctx context.Context, id string) (media.Media, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM media WHERE id = ?`, id)
	p, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Media{}, ErrNotFound
	}
	if err != nil {
		return media.Media{}, fmt.Errorf("cannot get the media: %w", err)
	}
	return p, nil
}

// DeleteByPath はパスで1件を削除し、削除した行を返す。
// 呼び出し側は返った行のIDでサムネイルを消す。
// 該当が無い場合は ok=false を返し、エラーにはしない。
func (s *Store) DeleteByPath(ctx context.Context, path string) (media.Media, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM media WHERE path = ? RETURNING `+selectCols, path)
	p, err := scanMedia(row)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Media{}, false, nil
	}
	if err != nil {
		return media.Media{}, false, fmt.Errorf("cannot delete the media (%s): %w", path, err)
	}
	return p, true, nil
}

// DeleteByPathPrefix はディレクトリ配下の登録をまとめて削除し、削除した行を返す。
// prefixにセパレータを1つ補ってから前方一致させるため、"album" が
// "album2" のような兄弟ディレクトリを巻き込むことはない。
//
// 前方一致は LIKE ではなく範囲比較で書く。LIKE の前方一致の最適化は
// ESCAPE 句があると効かず、削除1回ごとに media の全行を舐めることになる。
// path は UNIQUE なので暗黙の索引があり、範囲比較ならそれが使われる。
func (s *Store) DeleteByPathPrefix(ctx context.Context, prefix string) ([]media.Media, error) {
	dirPrefix := prefix
	if !strings.HasSuffix(dirPrefix, string(filepath.Separator)) {
		dirPrefix += string(filepath.Separator)
	}

	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM media WHERE path >= ? AND path < ? RETURNING `+selectCols,
		dirPrefix, upperBound(dirPrefix))
	if err != nil {
		return nil, fmt.Errorf("cannot delete the media under the directory (%s): %w", prefix, err)
	}
	defer rows.Close()

	var out []media.Media
	for rows.Next() {
		p, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("cannot read the deletion result: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// upperBound は前方一致の上限を返す。末尾のバイトを1つ進めた値は、
// prefixで始まるどの文字列よりも大きい最小の値になる。TEXTの既定の照合順序は
// BINARYなので、バイト単位で進めれば比較と食い違わない。
// prefixはセパレータで終わっているため、末尾が0xFFで桁上がりすることはない。
func upperBound(prefix string) string {
	b := []byte(prefix)
	b[len(b)-1]++
	return string(b)
}

// ListRange は撮影日時の新しい順で offset 番目から limit 件を返す。
// 仮想スクロールは任意の位置へ飛ぶため、カーソルではなくオフセットで引く。
func (s *Store) ListRange(ctx context.Context, offset, limit int) ([]media.Media, error) {
	if offset < 0 {
		return nil, fmt.Errorf("offset must be 0 or greater: %d", offset)
	}
	if limit < 0 {
		return nil, fmt.Errorf("limit must be 0 or greater: %d", limit)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM media
		 ORDER BY taken_at DESC, id DESC
		 LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("cannot list the media: %w", err)
	}
	defer rows.Close()

	var out []media.Media
	for rows.Next() {
		p, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("cannot read the media list: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RankOf は一覧の並びでその1件が何番目かを返す（先頭が0）。1件ごとのURLから
// 開くとき、クライアントは通し番号で位置を決めるため、IDから番号へ引き直す経路が要る。
// 該当が無い場合は ErrNotFound を返す。
//
// 並びは ListRange と同じ taken_at DESC, id DESC でなければならない。順序式が
// 二重になるが、片方だけ変えるとURLが別の1件の位置を指すため、
// TestRankOfLocatesThePhotoInListRange が両者の一致を縛っている。
func (s *Store) RankOf(ctx context.Context, id string) (int, error) {
	var takenAt int64
	err := s.db.QueryRowContext(ctx, `SELECT taken_at FROM media WHERE id = ?`, id).Scan(&takenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("cannot get the media: %w", err)
	}

	// 自分より前に並ぶ行を数える。OFFSET で数えるのと違い、途中の行を読まずに
	// 索引だけで答えが出る。
	var rank int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media
		 WHERE taken_at > ? OR (taken_at = ? AND id > ?)`,
		takenAt, takenAt, id).Scan(&rank)
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

// Count は登録件数を返す。
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media`).Scan(&n); err != nil {
		return 0, fmt.Errorf("cannot count the media: %w", err)
	}
	return n, nil
}

// DayGroup は、その日に撮った件数。一覧の区切りに使う。
type DayGroup struct {
	Date  string // "2006-01-02" 形式。ローカル時刻で判定する
	Count int
}

// DayGroups は日ごとの件数を新しい順に返す。一覧の区切りとスクラバーの目盛りに使う。
//
// SQLの strftime は UTC で日を切るため使わない。ローカルで未明に撮ったものが
// 前日に分類されてしまう。Go 側で time.Local に変換して数える。
func (s *Store) DayGroups(ctx context.Context) ([]DayGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT taken_at FROM media ORDER BY taken_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("cannot get the capture dates: %w", err)
	}
	defer rows.Close()

	var out []DayGroup
	for rows.Next() {
		var takenAt int64
		if err := rows.Scan(&takenAt); err != nil {
			return nil, fmt.Errorf("cannot read the capture dates: %w", err)
		}
		day := time.Unix(takenAt, 0).Format("2006-01-02")
		if len(out) > 0 && out[len(out)-1].Date == day {
			out[len(out)-1].Count++
			continue
		}
		out = append(out, DayGroup{Date: day, Count: 1})
	}
	return out, rows.Err()
}

func scanMedia(row interface{ Scan(...any) error }) (media.Media, error) {
	var path string
	var takenAt, modTime int64
	if err := row.Scan(&path, &takenAt, &modTime); err != nil {
		return media.Media{}, err
	}
	return media.Restore(path, time.Unix(takenAt, 0), time.Unix(modTime, 0)), nil
}
