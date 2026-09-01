// Package store は写真メタデータのSQLiteインデックスを提供する。
// 画像の実体はファイルシステム上にあり、ここではパスとメタデータだけを持つ。
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Goのsqliteドライバ。cgo不要。
)

// ErrNotFound は該当する写真が無いことを表す。
var ErrNotFound = errors.New("photo not found")

// Photo はインデックス上の1枚の写真。
type Photo struct {
	ID      string    // パスから導出した安定ID。URLに露出させる
	Path    string    // ディスク上の絶対パス
	TakenAt time.Time // EXIF撮影日時、無ければmtime
	ModTime time.Time // ファイルのmtime。再スキャン時の変更検知に使う
	Size    int64
	Ext     string // 小文字の拡張子（"." 込み）
	// ThumbSource はサムネイルの出どころ。消してよいのは自前で作ったものだけなので、
	// 「あるか」ではなく「どこにあるか」を持つ。
	ThumbSource ThumbSource
}

// ThumbSource はサムネイルの出どころ。
type ThumbSource string

const (
	ThumbNone   ThumbSource = ""       // サムネイルが無い
	ThumbFamifo ThumbSource = "famifo" // famifoが生成し、サムネイルキャッシュに置いたもの
	ThumbSyno   ThumbSource = "eadir"  // Synologyが @eaDir に持っているもの。読むだけで書き換えない
)

// IDFor はパスから安定したIDを導出する。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func IDFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}

// Store はSQLiteインデックスへのアクセスを提供する。
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS photos (
    id        TEXT PRIMARY KEY,
    path      TEXT NOT NULL UNIQUE,
    taken_at  INTEGER NOT NULL,
    mod_time  INTEGER NOT NULL,
    size      INTEGER NOT NULL,
    ext       TEXT NOT NULL,
    thumb_source TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_photos_order ON photos(taken_at DESC, id DESC);
`

// Open はDBを開き、スキーマを作成する。
// WALを有効にしてスキャン中の書き込みと配信中の読み取りを並行させる。
func Open(dbPath string) (*Store, error) {
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const upsertSQL = `
INSERT INTO photos (id, path, taken_at, mod_time, size, ext, thumb_source)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    path      = excluded.path,
    taken_at  = excluded.taken_at,
    mod_time  = excluded.mod_time,
    size      = excluded.size,
    ext       = excluded.ext,
    thumb_source = excluded.thumb_source`

// Upsert は写真を登録または更新する。
func (s *Store) Upsert(ctx context.Context, p Photo) error {
	_, err := s.db.ExecContext(ctx, upsertSQL,
		p.ID, p.Path, p.TakenAt.Unix(), p.ModTime.Unix(), p.Size, p.Ext, p.ThumbSource)
	if err != nil {
		return fmt.Errorf("写真を保存できません (%s): %w", p.Path, err)
	}
	return nil
}

const selectCols = `id, path, taken_at, mod_time, size, ext, thumb_source`

func scanPhoto(row interface{ Scan(...any) error }) (Photo, error) {
	var p Photo
	var takenAt, modTime int64
	if err := row.Scan(&p.ID, &p.Path, &takenAt, &modTime, &p.Size, &p.Ext, &p.ThumbSource); err != nil {
		return Photo{}, err
	}
	p.TakenAt = time.Unix(takenAt, 0)
	p.ModTime = time.Unix(modTime, 0)
	return p, nil
}

// GetByID はIDで写真を引く。見つからない場合は ErrNotFound を返す。
func (s *Store) GetByID(ctx context.Context, id string) (Photo, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM photos WHERE id = ?`, id)
	p, err := scanPhoto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	if err != nil {
		return Photo{}, fmt.Errorf("写真を取得できません: %w", err)
	}
	return p, nil
}

// DeleteByPath はパスで写真を削除し、削除した行を返す。
// 呼び出し側はサムネイルを消すかどうかの判断に ThumbSource を使う。
// 該当が無い場合は ok=false を返し、エラーにはしない。
func (s *Store) DeleteByPath(ctx context.Context, path string) (Photo, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`DELETE FROM photos WHERE path = ? RETURNING `+selectCols, path)
	p, err := scanPhoto(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Photo{}, false, nil
	}
	if err != nil {
		return Photo{}, false, fmt.Errorf("写真を削除できません (%s): %w", path, err)
	}
	return p, true, nil
}

// DeleteByPathPrefix はディレクトリ配下の写真をまとめて削除し、削除した行を返す。
// prefixにセパレータを1つ補ってから前方一致させるため、"album" が
// "album2" のような兄弟ディレクトリを巻き込むことはない。
func (s *Store) DeleteByPathPrefix(ctx context.Context, prefix string) ([]Photo, error) {
	dirPrefix := prefix
	if !strings.HasSuffix(dirPrefix, string(filepath.Separator)) {
		dirPrefix += string(filepath.Separator)
	}
	// LIKEのワイルドカード（% _）をエスケープしたうえで前方一致させる。
	escaped := likeEscaper.Replace(dirPrefix)

	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM photos WHERE path LIKE ? ESCAPE '\' RETURNING `+selectCols,
		escaped+"%")
	if err != nil {
		return nil, fmt.Errorf("ディレクトリ配下の写真を削除できません (%s): %w", prefix, err)
	}
	defer rows.Close()

	var out []Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("削除結果を読めません: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// ListRange は撮影日時の新しい順で offset 番目から limit 件を返す。
// 仮想スクロールは任意の位置へ飛ぶため、カーソルではなくオフセットで引く。
func (s *Store) ListRange(ctx context.Context, offset, limit int) ([]Photo, error) {
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

	var out []Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("一覧を読めません: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AllPaths は登録済みの全パスとそのmtimeを返す。フルスキャンでの差分検出に使う。
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
		var at int64
		if err := rows.Scan(&at); err != nil {
			return nil, fmt.Errorf("撮影日時を読めません: %w", err)
		}
		d := time.Unix(at, 0).Format("2006-01-02")
		if len(out) > 0 && out[len(out)-1].Date == d {
			out[len(out)-1].Count++
			continue
		}
		out = append(out, DayGroup{Date: d, Count: 1})
	}
	return out, rows.Err()
}
