// Package store は写真メタデータのSQLiteインデックスを提供する。
// 画像の実体はファイルシステム上にあり、ここではパスとメタデータだけを持つ。
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

	"github.com/yendo/famifo-proto/internal/photo"
	_ "modernc.org/sqlite" // pure Goのsqliteドライバ。cgo不要。
)

// ErrNotFound は該当する写真が無いことを表す。
var ErrNotFound = errors.New("photo not found")

// Store はSQLiteインデックスへのアクセスを提供する。
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS photos (
    id        TEXT PRIMARY KEY,
    path      TEXT NOT NULL UNIQUE,
    taken_at  INTEGER NOT NULL,
    mod_time  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_photos_order ON photos(taken_at DESC, id DESC);
`

// Open はDBを開き、スキーマを作成する。親ディレクトリが無ければ作る。
// WALを有効にしてスキャン中の書き込みと配信中の読み取りを並行させる。
func Open(dbPath string) (*Store, error) {
	// SQLiteは親ディレクトリを作らない。無いまま開くと sql.Open は遅延接続なので
	// 成功し、db.Ping() が "unable to open database file" で落ちる。原因の読めない
	// エラーになるうえ、呼び出し順への暗黙の依存を残すのでここで作る。
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("DBディレクトリを作れません: %w", err)
	}

	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("DBを開けません: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("DBに接続できません: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("スキーマを作成できません: %w", err)
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
	rows, err := db.Query(`SELECT id, ` + selectCols + ` FROM photos LIMIT 1`)
	if err != nil {
		return fmt.Errorf("DBを読めません: %w", err)
	}
	defer rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("DBを読めません: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

const upsertSQL = `
INSERT INTO photos (id, path, taken_at, mod_time)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    path      = excluded.path,
    taken_at  = excluded.taken_at,
    mod_time  = excluded.mod_time`

// Upsert は写真を登録または更新する。
func (s *Store) Upsert(ctx context.Context, p photo.Photo) error {
	_, err := s.db.ExecContext(ctx, upsertSQL,
		p.ID(), p.Path(), p.TakenAt().Unix(), p.ModTime().Unix())
	if err != nil {
		return fmt.Errorf("写真を保存できません (%s): %w", p.Path(), err)
	}
	return nil
}

// idは読まない。パスから導ける値なので、復元は photo.Restore に任せる。
const selectCols = `path, taken_at, mod_time`

func scanPhoto(row interface{ Scan(...any) error }) (photo.Photo, error) {
	var path string
	var takenAt, modTime int64
	if err := row.Scan(&path, &takenAt, &modTime); err != nil {
		return photo.Photo{}, err
	}
	return photo.Restore(path, time.Unix(takenAt, 0), time.Unix(modTime, 0)), nil
}

// GetByID はIDで写真を引く。見つからない場合は ErrNotFound を返す。
func (s *Store) GetByID(ctx context.Context, id string) (photo.Photo, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM photos WHERE id = ?`, id)
	p, err := scanPhoto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return photo.Photo{}, ErrNotFound
	}
	if err != nil {
		return photo.Photo{}, fmt.Errorf("写真を取得できません: %w", err)
	}
	return p, nil
}

// DeleteByPath はパスで写真を削除し、削除した行を返す。
// 呼び出し側は返った行のIDでサムネイルを消す。
// 該当が無い場合は ok=false を返し、エラーにはしない。
func (s *Store) DeleteByPath(ctx context.Context, path string) (photo.Photo, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM photos WHERE path = ? RETURNING `+selectCols, path)
	p, err := scanPhoto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return photo.Photo{}, false, nil
	}
	if err != nil {
		return photo.Photo{}, false, fmt.Errorf("写真を削除できません (%s): %w", path, err)
	}
	return p, true, nil
}

// DeleteByPathPrefix はディレクトリ配下の写真をまとめて削除し、削除した行を返す。
// prefixにセパレータを1つ補ってから前方一致させるため、"album" が
// "album2" のような兄弟ディレクトリを巻き込むことはない。
//
// 前方一致は LIKE ではなく範囲比較で書く。LIKE の前方一致の最適化は
// ESCAPE 句があると効かず、削除1回ごとに photos の全行を舐めることになる。
// path は UNIQUE なので暗黙の索引があり、範囲比較ならそれが使われる。
func (s *Store) DeleteByPathPrefix(ctx context.Context, prefix string) ([]photo.Photo, error) {
	dirPrefix := prefix
	if !strings.HasSuffix(dirPrefix, string(filepath.Separator)) {
		dirPrefix += string(filepath.Separator)
	}

	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM photos WHERE path >= ? AND path < ? RETURNING `+selectCols,
		dirPrefix, upperBound(dirPrefix))
	if err != nil {
		return nil, fmt.Errorf("ディレクトリ配下の写真を削除できません (%s): %w", prefix, err)
	}
	defer rows.Close()

	var out []photo.Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("削除結果を読めません: %w", err)
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
func (s *Store) ListRange(ctx context.Context, offset, limit int) ([]photo.Photo, error) {
	if offset < 0 {
		return nil, fmt.Errorf("offset は0以上で指定してください: %d", offset)
	}
	if limit < 0 {
		return nil, fmt.Errorf("limit は0以上で指定してください: %d", limit)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM photos
		 ORDER BY taken_at DESC, id DESC
		 LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("一覧を取得できません: %w", err)
	}
	defer rows.Close()

	var out []photo.Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("一覧を読めません: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllPaths は登録済みの全パスとそのmtimeを返す。スキャンでの差分検出に使う。
func (s *Store) AllPaths(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, mod_time FROM photos`)
	if err != nil {
		return nil, fmt.Errorf("パス一覧を取得できません: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var path string
		var modTime int64
		if err := rows.Scan(&path, &modTime); err != nil {
			return nil, fmt.Errorf("パス一覧を読めません: %w", err)
		}
		out[path] = modTime
	}
	return out, rows.Err()
}

// Count は登録枚数を返す。
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM photos`).Scan(&n); err != nil {
		return 0, fmt.Errorf("枚数を取得できません: %w", err)
	}
	return n, nil
}

// DayGroup は、その日に撮った写真の枚数。一覧の区切りに使う。
type DayGroup struct {
	Date  string // "2006-01-02" 形式。ローカル時刻で判定する
	Count int
}

// DayGroups は日ごとの枚数を新しい順に返す。一覧の区切りとスクラバーの目盛りに使う。
//
// SQLの strftime は UTC で日を切るため使わない。ローカルで未明に撮った写真が
// 前日に分類されてしまう。Go 側で time.Local に変換して数える。
func (s *Store) DayGroups(ctx context.Context) ([]DayGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT taken_at FROM photos ORDER BY taken_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("撮影日時を取得できません: %w", err)
	}
	defer rows.Close()

	var out []DayGroup
	for rows.Next() {
		var takenAt int64
		if err := rows.Scan(&takenAt); err != nil {
			return nil, fmt.Errorf("撮影日時を読めません: %w", err)
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
