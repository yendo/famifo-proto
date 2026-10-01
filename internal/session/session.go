// Package session はログインセッションの保管を担う。
//
// OIDCもHTTPのルーティングも知らない。セッションの置き場（sessions.db）を開き、
// それを読み書きするscsのマネージャを返すだけである。何を載せるかは呼び出し側が決める。
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

// lifetime はログイン状態が続く長さ。
//
// 使うたびに延ばすスライディング方式にすると、リクエストごとに Set-Cookie を出すか、
// 残り時間を見て再発行する分岐が要る。家族が30日ごとに1回入れ直す程度なら固定で足りる。
// 利用者が変えられる設定ではない。
const lifetime = 30 * 24 * time.Hour

// cookieName はセッショントークンを載せるCookieの名前。
const cookieName = "famifo_session"

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

// Manager はfamifoの設定が入ったscsのマネージャである。scs.SessionManager を
// 埋め込んであるので、呼び出し側は Put / PopString / RenewToken / Destroy を
// 直に呼ぶ。包み直すファサードは意味がないので作らない。
//
// 埋め込んだマネージャが読み書きするDBは、写真のインデックス（famifo.db）とは
// 別のファイルに置く。このリポジトリはスキーマ移行を書かず、列を変えたらDBを
// 消して作り直す運用なので、同居させるとインデックスを作り直すたびに全端末が
// ログアウトすることになる。分けておけば、逆に「全端末を一斉に切る」は
// このファイルを消すだけで済む。
type Manager struct {
	*scs.SessionManager
	db      *sql.DB
	backing *sqlite3store.SQLite3Store
}

// New はセッションDBを開き、scsのマネージャを組み立てる。親ディレクトリが
// 無ければ作る。secure はCookieに Secure を付けるかで、外部URLがhttpsのときだけ真。
func New(dbPath string, secure bool, log *slog.Logger) (*Manager, error) {
	// SQLiteは親ディレクトリを作らない。無いまま開くと sql.Open は遅延接続なので
	// 成功し、db.Ping() が "unable to open database file" で落ちる。
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("cannot create the session database directory: %w", err)
	}
	dsn := dbPath + "?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL"
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
	mgr.Lifetime = lifetime
	// IdleTimeout は設定しない。スライディングにしないため。
	mgr.Cookie.Name = cookieName
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
	return &Manager{SessionManager: mgr, db: db, backing: backing}, nil
}

// Close は掃除ゴルーチンを止めてからDBを閉じる。
// sqlite3store.New は5分ごとに期限切れを消すゴルーチンを起こす。止めないと
// Managerがガベージコレクトされない。
func (m *Manager) Close() error {
	m.backing.StopCleanup()
	return m.db.Close()
}
