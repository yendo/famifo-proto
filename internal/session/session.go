// Package session はログインセッションの保管を担う。
//
// OIDCもHTTPのルーティングも知らない。セッションの置き場（sessions.db）を開き、
// scsのマネージャを組み立てて渡すだけである。何を載せるかは呼び出し側が決める。
package session

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
	_ "modernc.org/sqlite" // pure Goのsqliteドライバ。cgo不要。
)

// Lifetime はログイン状態が続く長さ。
//
// 使うたびに延ばすスライディング方式にすると、リクエストごとに Set-Cookie を出すか、
// 残り時間を見て再発行する分岐が要る。家族が30日ごとに1回入れ直す程度なら固定で足りる。
// 利用者が変えられる設定ではない。
const Lifetime = 30 * 24 * time.Hour

// CookieName はセッショントークンを載せるCookieの名前。
const CookieName = "famifo_session"

// schema はセッションの表。scs/sqlite3store が読み書きする形に合わせてある。
// expiry は julianday の実数で、sqlite3store が自分で入れる。
const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    token  TEXT PRIMARY KEY,
    data   BLOB NOT NULL,
    expiry REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions(expiry);
`

// Store はセッションのDBと、それを使うscsのマネージャを保持する。
//
// 写真のインデックス（famifo.db）とは別のファイルに置く。このリポジトリは
// スキーマ移行を書かず、列を変えたらDBを消して作り直す運用なので、同居させると
// インデックスを作り直すたびに全端末がログアウトすることになる。分けておけば、
// 逆に「全端末を一斉に切る」はこのファイルを消すだけで済む。
type Store struct {
	db      *sql.DB
	backing *sqlite3store.SQLite3Store
	mgr     *scs.SessionManager
}

// Open はセッションDBを開き、scsのマネージャを組み立てる。親ディレクトリが
// 無ければ作る。secure はCookieに Secure を付けるかで、外部URLがhttpsのときだけ真。
func Open(dbPath string, secure bool, log *slog.Logger) (*Store, error) {
	// SQLiteは親ディレクトリを作らない。無いまま開くと sql.Open は遅延接続なので
	// 成功し、db.Ping() が "unable to open database file" で落ちる。
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create the session database directory: %w", err)
	}
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cannot open the session database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot connect to the session database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot create the session schema: %w", err)
	}

	backing := sqlite3store.New(db)
	mgr := scs.New()
	mgr.Store = backing
	mgr.Lifetime = Lifetime
	// IdleTimeout は設定しない。スライディングにしないため。
	mgr.Cookie.Name = CookieName
	mgr.Cookie.Path = "/"
	mgr.Cookie.HttpOnly = true
	mgr.Cookie.SameSite = http.SameSiteLaxMode
	mgr.Cookie.Secure = secure
	// 既定のErrorFuncはGoの標準loggerに書く。famifoのログはslogに寄せてあるので、
	// storeが落ちたときだけ別の経路に出ると調べにくい。
	mgr.ErrorFunc = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Error("cannot load or save the session", "err", err, "path", r.URL.Path)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	return &Store{db: db, backing: backing, mgr: mgr}, nil
}

// Manager はscsのマネージャを返す。呼び出し側が Put / PopString / RenewToken /
// Destroy を直に呼ぶ。全部を包み直すファサードは意味がないので作らない。
func (s *Store) Manager() *scs.SessionManager { return s.mgr }

// Close は掃除ゴルーチンを止めてからDBを閉じる。
// sqlite3store.New は5分ごとに期限切れを消すゴルーチンを起こす。止めないと
// Storeがガベージコレクトされない。
func (s *Store) Close() error {
	s.backing.StopCleanup()
	return s.db.Close()
}
