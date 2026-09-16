# scs によるサーバー側セッション 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 自前の HMAC 署名付き Cookie をやめ、`github.com/alexedwards/scs` によるサーバー側セッション（SQLite 保管）に載せ替える。

**Architecture:** `internal/session` は `sessions.db` を開いて `*scs.SessionManager` を組み立てる係になる。Cookie に載るのはトークンだけで、利用者名もログイン往復の一時状態（state / nonce / verifier / next）も同じ1つのセッションに入る。`internal/web` は `LoadAndSave` を `/static/` 以外に掛け、`Put` / `PopString` / `RenewToken` / `Destroy` / `SetDeadline` を直に呼ぶ。

**Tech Stack:** Go 1.27、`github.com/alexedwards/scs/v2`、`github.com/alexedwards/scs/sqlite3store`、`modernc.org/sqlite`（cgo 不要）、`testify/require`

**Spec:** `docs/superpowers/specs/2026-09-16-scs-sessions-design.md`

## Global Constraints

- リポジトリに入るものは英語で書く。ただし**このリポジトリの Go のコメントと docs は日本語**である（既存ファイルに合わせる。`.github/` 配下だけ英語）。コミットメッセージは英語で要約1行のみ、本文とトレーラーは書かない。
- テストは公開 API だけを通す。`internal/web` と `internal/session` のテストは外部テストパッケージ（`package web_test` / `package session_test`）である。
- ビルドは cgo 無し。`CGO_ENABLED=0 go build` が通り続けること。`sqlite3store` の `go.mod` は `github.com/mattn/go-sqlite3` を要求するが、`sqlite3store` のパッケージ自身はそれを import しない（テストだけが使う）ので、実際にリンクされてはならない。
- セッションの有効期限は 30 日固定。スライディングにしない（`IdleTimeout` は設定しない）。
- ログイン往復の制限は 10 分。
- Cookie 名は `famifo_session`。属性は `HttpOnly`、`SameSite=Lax`、`Path=/`、外部 URL が https のときだけ `Secure`。
- スキーマ移行は書かない。テーブルは `CREATE TABLE IF NOT EXISTS` で作る。
- 各タスクの終わりで `go build ./...` と `go test ./...` が通ること。途中でビルドが壊れる順序にしない。

---

### Task 1: `internal/session` に scs の Store を足す

既存の `Codec` 一式はこのタスクでは消さない。消すと `main.go` と `internal/web` が壊れるので、削除は Task 5 でまとめて行う。ここでの成果物は「scs + `sqlite3store` が modernc ドライバで動く」ことの確認そのものである。`sqlite3store` の SQL は `$1` 形式のプレースホルダを使い、これは mattn ドライバ前提の書き方なので、最初に確かめる。

**Files:**
- Create: `internal/session/store.go`
- Test: `internal/session/store_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: なし
- Produces:
  - `session.Open(dbPath string, secure bool, log *slog.Logger) (*session.Store, error)`
  - `(*session.Store).Manager() *scs.SessionManager`
  - `(*session.Store).Close() error`
  - `session.CookieName` = `"famifo_session"`、`session.Lifetime` = 30日

- [ ] **Step 1: 依存を足す**

```bash
go get github.com/alexedwards/scs/v2@latest
go get github.com/alexedwards/scs/sqlite3store@latest
```

- [ ] **Step 2: 失敗するテストを書く**

`internal/session/store_test.go` を新規に作る。既存の `session_test.go` は触らない。

```go
package session_test

// scsのセッションがsessions.dbに保管され、Cookieのトークンで取り出せることを
// 確かめる。sqlite3storeのSQLは "$1" 形式のプレースホルダを使っていて、これは
// mattnドライバ前提の書き方である。cgo不要のmodernドライバでも通ることを、
// ここで最初に固定する。
import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/session"
)

func openStore(t *testing.T) *session.Store {
	t.Helper()
	// 親ディレクトリが無い場所を指す。Openが作ることもここで確かめる。
	path := filepath.Join(t.TempDir(), "sub", "sessions.db")
	st, err := session.Open(path, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// serve は1リクエストをLoadAndSaveに通し、応答を返す。cookies を渡すと
// そのCookieを載せる。
func serve(t *testing.T, m *scs.SessionManager, h http.HandlerFunc, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	m.LoadAndSave(h).ServeHTTP(rec, req)
	return rec.Result()
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestSessionDataSurvivesARoundTrip(t *testing.T) {
	m := openStore(t).Manager()

	put := serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		m.Put(r.Context(), "user", "yendo")
	})
	c := cookieNamed(put, session.CookieName)
	require.NotNil(t, c, "a session cookie must be issued")
	require.True(t, c.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, c.SameSite)
	require.False(t, c.Secure, "secure was false")

	var got string
	serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		got = m.GetString(r.Context(), "user")
	}, c)
	require.Equal(t, "yendo", got)
}

func TestAnExpiredSessionIsNotFound(t *testing.T) {
	m := openStore(t).Manager()

	put := serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		m.Put(r.Context(), "user", "yendo")
		m.SetDeadline(r.Context(), time.Now().Add(-time.Minute))
	})
	c := cookieNamed(put, session.CookieName)
	require.NotNil(t, c)

	var got string
	serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		got = m.GetString(r.Context(), "user")
	}, c)
	require.Empty(t, got, "an expired session must not be readable")
}

func TestDestroyRemovesTheSession(t *testing.T) {
	m := openStore(t).Manager()

	put := serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		m.Put(r.Context(), "user", "yendo")
	})
	c := cookieNamed(put, session.CookieName)
	require.NotNil(t, c)

	serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, m.Destroy(r.Context()))
	}, c)

	var got string
	serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		got = m.GetString(r.Context(), "user")
	}, c)
	require.Empty(t, got, "a destroyed session must not come back")
}

func TestSecureFollowsTheArgument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := session.Open(path, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	resp := serve(t, st.Manager(), func(w http.ResponseWriter, r *http.Request) {
		st.Manager().Put(r.Context(), "user", "yendo")
	})
	require.True(t, cookieNamed(resp, session.CookieName).Secure)
}

func TestTheDatabaseCanBeReopened(t *testing.T) {
	// 掃除ゴルーチンが止まっていることは公開APIからは観測できない。Closeが
	// エラー無く戻り、同じファイルを開き直せることまでを固定する。
	path := filepath.Join(t.TempDir(), "sessions.db")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	first, err := session.Open(path, false, log)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := session.Open(path, false, log)
	require.NoError(t, err)
	require.NoError(t, second.Close())
}
```

- [ ] **Step 3: 落ちることを確かめる**

Run: `go test ./internal/session/ -run 'RoundTrip|Expired|Destroy|Secure|Reopened' -v`
Expected: コンパイルエラー（`undefined: session.Open`, `session.Store`, `session.CookieName`）

- [ ] **Step 4: 実装する**

`internal/session/store.go` を新規に作る。

```go
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
```

- [ ] **Step 5: テストが通ることを確かめる**

Run: `go test ./internal/session/ -v`
Expected: PASS（既存の `Codec` のテストも通ったまま）

ここで `$1` プレースホルダが modernc ドライバで通るかが分かる。**通らなかった場合**は `sqlite3store` を使わず、同じ4メソッド（`Find(token string) ([]byte, bool, error)` / `Commit(token string, b []byte, expiry time.Time) error` / `Delete(token string) error` / `All() (map[string][]byte, error)`）を `?` プレースホルダで実装した非公開の型を `internal/session/store.go` に書き、`mgr.Store` にそれを差す。SQL は `sqlite3store` と同じ（`SELECT data FROM sessions WHERE token = ? AND julianday('now') < expiry`、`REPLACE INTO sessions (token, data, expiry) VALUES (?, ?, julianday(?))`、`DELETE FROM sessions WHERE token = ?`）。この場合 `Close` は `StopCleanup` の代わりに自前の掃除ゴルーチンを止める。

- [ ] **Step 6: cgo 無しでビルドできることを確かめる**

Run: `CGO_ENABLED=0 go build ./... && go vet ./...`
Expected: 成功。`go.mod` の require に `github.com/mattn/go-sqlite3` が**現れない**こと（`sqlite3store` のテストだけが使うため）。現れていたら `go mod tidy` を実行する。

- [ ] **Step 7: コミット**

```bash
git add go.mod go.sum internal/session/store.go internal/session/store_test.go
git commit -m "feat: Add a SQLite-backed scs session store"
```

---

### Task 2: `config` にセッションDBのパスを足す

**Files:**
- Modify: `internal/config/config.go:159`
- Test: `internal/config/config_test.go:177-180`

**Interfaces:**
- Consumes: なし
- Produces: `(config.Config).SessionDBPath() string` → `<DataDir>/sessions.db`

- [ ] **Step 1: 失敗するテストを書く**

`internal/config/config_test.go` の `TestSessionKeyPathSitsInTheDataDir` の下に足す。既存のテストはまだ消さない（`SessionKeyPath` は Task 5 で消す）。

```go
func TestSessionDBPathSitsInTheDataDir(t *testing.T) {
	c := validConfig(t)
	require.Equal(t, filepath.Join(c.DataDir, "sessions.db"), c.SessionDBPath())
}
```

- [ ] **Step 2: 落ちることを確かめる**

Run: `go test ./internal/config/ -run SessionDBPath -v`
Expected: コンパイルエラー（`c.SessionDBPath undefined`）

- [ ] **Step 3: 実装する**

`internal/config/config.go` の `SessionKeyPath` の下に足す。

```go
// SessionDBPath はセッションDBの置き場を返す。写真のインデックス（famifo.db）
// とは別のファイルにする。インデックスを作り直してもログインが残るようにするため。
func (c Config) SessionDBPath() string { return filepath.Join(c.DataDir, "sessions.db") }
```

- [ ] **Step 4: テストが通ることを確かめる**

Run: `go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 5: コミット**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: Add the session database path to the config"
```

---

### Task 3: `internal/web` と `main.go` を scs に載せ替える

`web.Auth` のフィールドが変わるので、`internal/web` の実装・テスト・`main.go` は同じコミットで揃える。ビルドが壊れた状態を残さない。

**Files:**
- Modify: `internal/web/auth.go`（全面）
- Modify: `internal/web/server.go:37-44`（`Server` のフィールド）, `:56-73`（`NewServer`）, `:76-105`（`Handler`）
- Modify: `main.go:85-101`
- Test: `internal/web/auth_test.go`, `internal/web/oidc_browser_test.go:181-186`

**Interfaces:**
- Consumes: `session.Open` / `(*session.Store).Manager` / `(*session.Store).Close` / `session.CookieName`（Task 1）、`(config.Config).SessionDBPath`（Task 2）
- Produces:
  - `web.Auth{OIDC Provider; Sessions *scs.SessionManager; ExternalURL string}`（`Key []byte` と `Secure bool` は消える）
  - `(*Server).callbackError(w http.ResponseWriter, r *http.Request, message string, status int)`（`r` が増える）

- [ ] **Step 1: テストを新しい形に書き換える**

`internal/web/auth_test.go` を次のように直す。まだ実装が無いので落ちる。

`newAuthFixture` の `key := make([]byte, session.KeyLen)` を消し、セッションStoreを作る形にする。

```go
type authFixture struct {
	h    http.Handler
	prov *fakeProvider
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	return newAuthFixtureWith(t, &fakeProvider{identity: oidcauth.Identity{Subject: "yendo", Username: "yendo"}}, false)
}

// newAuthFixtureWith はIdPの偽物とSecureの設定を選べる版。個別に組み立てていた
// テストがいくつもあったので、1つにまとめる。
func newAuthFixtureWith(t *testing.T, prov *fakeProvider, secure bool) *authFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/famifo.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	thumbs, err := thumb.NewProvider(dir + "/thumbs")
	require.NoError(t, err)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.Open(dir+"/sessions.db", secure, log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })

	srv, err := web.NewServer(st, thumbs,
		&web.Auth{OIDC: prov, Sessions: sessions.Manager(), ExternalURL: "https://famifo.example.invalid"},
		log)
	require.NoError(t, err)
	return &authFixture{h: srv.Handler(), prov: prov}
}
```

`TestLoginRedirectsToTheProvider` の最後の1行を、往復用Cookieではなくセッション Cookie を見る形に変える。

```go
	require.NotNil(t, cookieNamed(resp, "famifo_session"), "the login flow must be carried in a session")
```

`TestLoginFlowCookieDoesNotAuthenticate` は `Codec` が無くなって意味を失うので、**丸ごと消す**。代わりのテストは Task 4 で足す。

`TestCallbackIssuesASessionAndReturnsToNext` / `TestCallbackRejectsAStateMismatch` / `TestCallbackRejectsAMissingFlowCookie` / `TestCallbackReportsAnUnreachableProvider` と、IdP が拒んだ場合のテストは、`flow := cookieNamed(start, "famifo_oidc")` を `flow := cookieNamed(start, "famifo_session")` に置き換える。Cookie は1本になったので、`/auth/callback` にはこのCookieを渡す。`TestCallbackRejectsAMissingFlowCookie` は名前を `TestCallbackRejectsAMissingSession` に変え、Cookie を渡さない形は今のままでよい。

`TestCallbackIssuesASessionAndReturnsToNext` には、トークンが往復の前後で変わることを足す。

```go
	sess := cookieNamed(resp, "famifo_session")
	require.NotNil(t, sess)
	require.True(t, sess.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, sess.SameSite)
	require.NotEqual(t, flow.Value, sess.Value, "the token must be renewed when signing in")
```

`TestLogoutRedirectsToTheProviderWhenSupported` は自前の組み立てをやめて `newAuthFixtureWith` を使う。`famifo_oidc` はもう無いので、その Cookie を足す行と、末尾の `clearedFlow` の3行を消す。

```go
func TestLogoutRedirectsToTheProviderWhenSupported(t *testing.T) {
	prov := &fakeProvider{
		identity:           oidcauth.Identity{Subject: "yendo", Username: "yendo"},
		endSessionEndpoint: "https://idp.example.invalid:5001/webman/logout.cgi",
	}
	f := newAuthFixtureWith(t, prov, false)

	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_session")
	cb := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(prov.lastParams.State), flow)
	sess := cookieNamed(cb, "famifo_session")

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	resp := rec.Result()

	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "idp.example.invalid:5001", loc.Host)
	require.Equal(t, "/webman/logout.cgi", loc.Path)
	require.Equal(t, "https://famifo.example.invalid/signed-out", loc.Query().Get("post_logout_redirect_uri"))
	require.Equal(t, "famifo", loc.Query().Get("client_id"))

	cleared := cookieNamed(resp, "famifo_session")
	require.NotNil(t, cleared)
	require.Less(t, cleared.MaxAge, 0, "the session cookie must be told to expire")
}
```

`TestSecureAttributeFollowsTheSetting` も fixture を使う形に直す。

```go
func TestSecureAttributeFollowsTheSetting(t *testing.T) {
	f := newAuthFixtureWith(t, &fakeProvider{identity: oidcauth.Identity{Subject: "yendo", Username: "yendo"}}, true)

	resp := get(t, f.h, "/login")
	require.True(t, cookieNamed(resp, "famifo_session").Secure)
}
```

`TestAnExpiredSessionIsRefused` は `Codec` で偽の期限切れCookieを作れなくなるので、知らないトークンを送る形に変える。サーバー側にセッションが無い、という同じ状況を突く。

```go
// TestAnUnknownTokenIsRefused はサーバー側に無いトークンを送っても認証されない
// ことを固定する。期限切れも、消されたセッションも、偽造も、サーバーから見れば
// すべて「その行が無い」に落ちる。
func TestAnUnknownTokenIsRefused(t *testing.T) {
	f := newAuthFixture(t)
	stale := &http.Cookie{Name: "famifo_session", Value: "PTgYqBpEB4gGAWRpPfSLQYCFXQGm6vVX7ptHdOLh0kM"}

	resp := get(t, f.h, "/", stale)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/login")
}
```

`internal/web/oidc_browser_test.go:181-186` も直す。

```go
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.Open(filepath.Join(dir, "sessions.db"), false, log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })

	webSrv, err := web.NewServer(st, thumbs, &web.Auth{OIDC: client, Sessions: sessions.Manager()}, log)
	require.NoError(t, err)
```

`crypto/rand` の import が他で使われていなければ落とす。

- [ ] **Step 2: 落ちることを確かめる**

Run: `go test ./internal/web/ -v`
Expected: コンパイルエラー（`unknown field Sessions in struct literal of type web.Auth`）

- [ ] **Step 3: `web.Auth` と `Server` を直す**

`internal/web/auth.go` の `sessionTTL` 定数と `sessionCookie` / `flowCookie` 定数、`flowState` 構造体を消し、`Auth` を次にする。`flowTTL` は残す。

```go
// flowTTL は認可の往復に許す時間。ログイン画面を開いたまま放置した場合の上限になる。
// scsの期限としてセッションに載せるので、放置された往復はこの時間で行ごと消える。
const flowTTL = 10 * time.Minute

// Auth は認証の手段をまとめる。NewServer に nil を渡すと認証しない。
type Auth struct {
	OIDC Provider
	// Sessions はセッションの保管と持ち回りを担う。Cookieの名前も属性も
	// ここに設定してある（internal/session が組み立てる）。
	Sessions *scs.SessionManager
	// ExternalURL はfamifoが外から見えるURL。RP-Initiated Logoutの
	// post_logout_redirect_uriを組み立てるのに使う。
	ExternalURL string
}
```

セッションに入れるキーを定数にする。

```go
// セッションに載せるキー。userはログイン済みの利用者名、残りは認可の往復の
// あいだだけ持ち越す値である。往復の値はcallbackでPopStringして取り出すので、
// 済んだ往復の残骸がセッションに残らない。
const (
	keyUser     = "user"
	keyState    = "state"
	keyNonce    = "nonce"
	keyVerifier = "verifier"
	keyNext     = "next"
)
```

`internal/web/server.go` の `Server` から `sessionCodec` と `flowCodec` を消し、`NewServer` から `session.NewCodec` を呼ぶ2ブロックを消す（`internal/session` の import も落ちる）。

```go
type Server struct {
	st        *store.Store
	tmpl      *template.Template
	thumbs    *thumb.Provider
	chunkSize int
	auth      *Auth // nil なら認証しない
	log       *slog.Logger
}
```

```go
func NewServer(st *store.Store, thumbs *thumb.Provider, auth *Auth, log *slog.Logger) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("cannot load the templates: %w", err)
	}
	return &Server{st: st, tmpl: tmpl, thumbs: thumbs, chunkSize: defaultChunkSize, auth: auth, log: log}, nil
}
```

`NewServer` のコメントのうち、用途ごとにCodecを導出する話（`auth.Key からは用途ごとに…` の段落）は事実でなくなるので消す。

- [ ] **Step 4: `Handler` の入れ子を組み替える**

`LoadAndSave` は `/static/` には掛けない。セッションを読む意味がないうえ、`Vary: Cookie` が付く。

```go
// Handler はルーティング済みのハンドラを返す。
//
// セッションのミドルウェア（LoadAndSave）は /static/ の外側には掛けない。
// 静的ファイルはセッションを読まないし、掛けると応答に Vary: Cookie が付く。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err) // embedの内容は固定なので、ここで失敗するならビルドの不備
	}
	// 未認証でもCSSは当たるようにする。ログイン前の画面が崩れる意味がない。
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", s.handleGallery)
	protected.HandleFunc("GET /item/{id}", s.handleItem)
	protected.HandleFunc("GET /tiles", s.handleTiles)
	protected.HandleFunc("GET /thumb/{id}", s.handleThumb)
	protected.HandleFunc("GET /file/{id}", s.handleFile)

	inner := http.NewServeMux()
	inner.Handle("/", s.authenticate(protected))
	if s.auth == nil {
		mux.Handle("/", inner)
		return mux
	}
	inner.HandleFunc("GET /login", s.handleLogin)
	inner.HandleFunc("GET /auth/callback", s.handleCallback)
	inner.HandleFunc("POST /logout", s.handleLogout)
	// RP-Initiated LogoutでIdPが戻ってくる先。/logout自身がend_session_endpoint
	// を持たないIdPのとき案内ページとして返すのもここ。
	inner.HandleFunc("GET /signed-out", s.handleSignedOut)
	mux.Handle("/", s.auth.Sessions.LoadAndSave(inner))
	return mux
}
```

- [ ] **Step 5: ハンドラを載せ替える**

`currentUser` はセッションから読む。

```go
// currentUser はセッションから利用者名を取り出す。無ければ空を返す。
// セッションが無い、期限切れ、ログアウト済み、偽のトークンは、すべてここでは
// 「userが入っていない」に落ちる。
func (s *Server) currentUser(r *http.Request) string {
	return s.auth.Sessions.GetString(r.Context(), keyUser)
}
```

`handleLogin` は古いセッションを捨ててから往復の値を置く。

```go
// handleLogin は認可の往復を始める。
//
// 先にDestroyするのは、ログインの開始が「今のセッションを捨てて入り直す」操作
// だからである。すでにログイン済みの端末で /login を開いた場合も、古いセッションは
// ここで破棄される。セッション固定への備えも兼ねる。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	p, err := oidcauth.NewParams()
	if err != nil {
		s.log.Error("cannot start the login flow", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	if err := s.auth.Sessions.Destroy(ctx); err != nil {
		s.log.Error("cannot start the login flow", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auth.Sessions.Put(ctx, keyState, p.State)
	s.auth.Sessions.Put(ctx, keyNonce, p.Nonce)
	s.auth.Sessions.Put(ctx, keyVerifier, p.Verifier)
	s.auth.Sessions.Put(ctx, keyNext, safeNext(r.URL.Query().Get("next")))
	// 往復が終わるまでの短い期限にする。callbackのRenewTokenが30日に引き直すので、
	// 放置されたログイン試行だけがここで期限切れになる。
	s.auth.Sessions.SetDeadline(ctx, time.Now().Add(flowTTL))
	http.Redirect(w, r, s.auth.OIDC.AuthURL(p), http.StatusFound)
}
```

`handleCallback` は `PopString` で取り出す。JSON の詰め替えと、その解釈失敗の分岐は消える。

```go
// handleCallback は認可コードを受け取ってセッションを発行する。
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Popで取り出す。往復に使った値は用済みなので、セッションに残さない。
	state := s.auth.Sessions.PopString(ctx, keyState)
	nonce := s.auth.Sessions.PopString(ctx, keyNonce)
	verifier := s.auth.Sessions.PopString(ctx, keyVerifier)
	next := s.auth.Sessions.PopString(ctx, keyNext)
	// Cookieが無い、期限切れ、すでに使い切った往復は、どれもstateが空になる。
	if state == "" {
		s.callbackError(w, r, "the login attempt has expired, please start again", http.StatusBadRequest)
		return
	}
	// stateが合わないものを通すとCSRFになる。
	if q := r.URL.Query().Get("state"); q != state {
		s.callbackError(w, r, "the login attempt does not match, please start again", http.StatusBadRequest)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.log.Warn("the identity provider refused the login", "err", e)
		s.callbackError(w, r, "the identity provider refused the login", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.callbackError(w, r, "the identity provider returned no code", http.StatusBadRequest)
		return
	}

	id, err := s.auth.OIDC.Exchange(ctx, code, oidcauth.Params{State: state, Nonce: nonce, Verifier: verifier})
	if err != nil {
		// 「IdPに届かない」と「IdPが応答したうえで拒んだ」は原因の調べ方が違う。
		// 実機のインシデントでは両方が同じ「IdPに到達できない」に丸められ、
		// 調査が違う方向へ進んだ。ProviderErrorが載っていれば後者である。
		var perr *oidcauth.ProviderError
		if errors.As(err, &perr) {
			s.log.Error("the identity provider refused the login", "status", perr.StatusCode, "body", perr.Body)
			// 502はここでは正しくない。502は上流から届いた応答が不正なときの
			// もので、ここでは上流は普通に応答し、そのうえで拒んだだけである。
			// 503は「今は無理だが、また試して良い」を表す。実際に効くことが
			// 多い（インシデントはどれも再試行で通っている）。
			s.callbackError(w, r, "the identity provider refused the sign-in, please try again", http.StatusServiceUnavailable)
			return
		}
		// famifo は動いているがIdPに届かなかった、という区別を残す。
		s.log.Error("cannot complete the login", "err", err)
		s.callbackError(w, r, "cannot reach the identity provider", http.StatusBadGateway)
		return
	}
	// トークンを振り直してからログイン済みにする。往復のあいだ使っていたトークンを
	// そのまま昇格させない（セッション固定への備え）。期限もここで30日に戻る。
	if err := s.auth.Sessions.RenewToken(ctx); err != nil {
		s.log.Error("cannot issue the session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.auth.Sessions.Put(ctx, keyUser, id.Username)
	s.log.Info("signed in", "user", id.Username)
	http.Redirect(w, r, safeNext(next), http.StatusFound)
}
```

`handleLogout` は `Destroy` ひとつになる。長いコメントのうち「署名付きCookieを選んだ帰結として、他の端末のセッションは生き続ける」の1文は事実でなくなるので直す。残り（`/` へリダイレクトしてはいけない理由、RP-Initiated Logout）はそのまま残す。

```go
// handleLogout はこの端末のセッションを捨てる。Destroyがサーバー側の行を消すので、
// 同じCookieを送り直しても通らない。他の端末のセッションは別の行なので残る。
//
// 消したあとに "/" へリダイレクトしてはいけない。（以下、既存のコメントをそのまま残す）
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Sessions.Destroy(r.Context()); err != nil {
		s.log.Error("cannot sign out", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if u, ok := s.auth.OIDC.LogoutURL(s.signedOutURL()); ok {
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	s.renderSignedOut(w)
}
```

`callbackError` は `r` を取り、Cookieを消す代わりにセッションを破棄する。

```go
// callbackError は失敗した /auth/callback を、/login へのリンク付きの小さな
// HTMLページで終える。素のtext/plainな400だとURLバーを手で書き換えるしか
// 戻る手段がない。ここでセッションも破棄する。残すと、失敗した往復のあとも
// 中途半端な状態が最長flowTTLぶん生き、「やり直してください」が実際にはやり直し
// にならない。
func (s *Server) callbackError(w http.ResponseWriter, r *http.Request, message string, status int) {
	if err := s.auth.Sessions.Destroy(r.Context()); err != nil {
		s.log.Error("cannot discard the failed login attempt", "err", err)
	}
	s.writeHTMLPage(w, status, "Sign-in failed",
		fmt.Sprintf(`<p>%s</p><p><a href="/login">Try signing in again</a></p>`, html.EscapeString(message)))
}
```

`setCookie` と `clearCookie` を消す。`encoding/json` と `internal/session` の import も落ちる。`github.com/alexedwards/scs/v2` を足す。

- [ ] **Step 6: `main.go` を直す**

`main.go:85-101` の `key, err := session.LoadOrCreateKey(...)` のブロックを差し替える。

```go
	var auth *web.Auth
	if cfg.OIDCIssuer != "" {
		sessions, err := session.Open(cfg.SessionDBPath(), cfg.CookieSecure(), log)
		if err != nil {
			return err
		}
		defer sessions.Close()
		oidcClient, err := oidcauth.New(ctx, oidcauth.Config{
			Issuer:       cfg.OIDCIssuer,
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
			RedirectURI:  cfg.RedirectURI(),
		})
		if err != nil {
			return err
		}
		auth = &web.Auth{OIDC: oidcClient, Sessions: sessions.Manager(), ExternalURL: cfg.ExternalURL}
		log.Info("authentication is on", "issuer", cfg.OIDCIssuer, "redirect", cfg.RedirectURI())
	} else {
		log.Warn("authentication is off, anyone who can reach this address can see the photos")
	}
```

- [ ] **Step 7: テストが通ることを確かめる**

Run: `go build ./... && go vet ./... && go test ./... -shuffle=on`
Expected: PASS

- [ ] **Step 8: ブラウザの1往復を通す**

Run: `make browser-test`
Expected: PASS（`/` → `/login` → 偽IdP → `/auth/callback` → ギャラリー）

- [ ] **Step 9: コミット**

```bash
git add internal/web main.go
git commit -m "feat: Move sessions onto scs with the login flow in the session"
```

---

### Task 4: サーバー側失効のテストを足す

署名付きCookie方式では書けなかった2本を足す。載せ替えの意味がそのまま現れる箇所なので、実装が終わったあとに別コミットで固定する。

**Files:**
- Test: `internal/web/auth_test.go`

**Interfaces:**
- Consumes: Task 3 の `newAuthFixture` / `get` / `cookieNamed`
- Produces: なし

- [ ] **Step 1: テストを書く**

```go
// TestSignOutRevokesTheSessionOnTheServer はログアウトのあと、同じCookieを
// 送り直しても入れないことを固定する。署名付きCookieだった頃はサーバーが
// 発行済みの値を知らなかったので、これは書けなかった。
func TestSignOutRevokesTheSessionOnTheServer(t *testing.T) {
	f := newAuthFixture(t)

	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_session")
	cb := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	sess := cookieNamed(cb, "famifo_session")
	require.NotNil(t, sess)
	require.Equal(t, http.StatusOK, get(t, f.h, "/", sess).StatusCode)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(sess)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Result().StatusCode)

	// 消えたCookieを手元に持っている端末が送り直しても通らない。
	resp := get(t, f.h, "/", sess)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/login")
}

// TestAFlowSessionDoesNotAuthenticate はログインを始めただけのセッションでは
// 保護された画面に入れないことを固定する。/login は匿名のセッションを作って
// state と nonce と verifier を載せるが、その時点ではまだ誰でもない。
func TestAFlowSessionDoesNotAuthenticate(t *testing.T) {
	f := newAuthFixture(t)

	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_session")
	require.NotNil(t, flow)

	resp := get(t, f.h, "/", flow)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/login")
}
```

- [ ] **Step 2: 通ることを確かめる**

Run: `go test ./internal/web/ -run 'RevokesTheSession|FlowSessionDoesNotAuthenticate' -v`
Expected: PASS（実装は Task 3 で済んでいる）

落ちた場合は実装の不備である。テストを緩めず、Task 3 のハンドラを直す。

- [ ] **Step 3: コミット**

```bash
git add internal/web/auth_test.go
git commit -m "test: Pin that signing out revokes the session on the server"
```

---

### Task 5: 古い署名Cookieの仕組みを消す

ここまでで参照が無くなっているので、まとめて消す。

**Files:**
- Delete: `internal/session/session.go`, `internal/session/session_test.go`
- Modify: `internal/config/config.go:159`（`SessionKeyPath` を消す）, `internal/config/config_test.go:177-180`（そのテストを消す）

**Interfaces:**
- Consumes: なし
- Produces: `session.Codec` / `session.NewCodec` / `session.Sign` / `session.Verify` / `session.KeyLen` / `session.LoadOrCreateKey` / `config.SessionKeyPath` が無くなる

- [ ] **Step 1: 参照が残っていないことを確かめる**

Run: `grep -rn "KeyLen\|NewCodec\|LoadOrCreateKey\|SessionKeyPath\|famifo_oidc" --include='*.go' .`
Expected: `internal/session/session.go`、`internal/session/session_test.go`、`internal/config/config.go`、`internal/config/config_test.go` 以外に出ない。他に出たら先にそこを直す。

- [ ] **Step 2: 消す**

```bash
git rm internal/session/session.go internal/session/session_test.go
```

`internal/config/config.go` から次を消す。

```go
// SessionKeyPath はセッションの署名鍵の置き場を返す。
func (c Config) SessionKeyPath() string { return filepath.Join(c.DataDir, "session.key") }
```

`internal/config/config_test.go` から `TestSessionKeyPathSitsInTheDataDir` を消す。

- [ ] **Step 3: 通ることを確かめる**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./... -shuffle=on`
Expected: PASS

- [ ] **Step 4: パッケージのコメントを直す**

`internal/session/store.go` の先頭に、パッケージのコメントを移す（`session.go` が消えて行き場が無くなるため）。

```go
// Package session はログインセッションの保管を担う。
//
// OIDCもHTTPのルーティングも知らない。セッションの置き場（sessions.db）を開き、
// scsのマネージャを組み立てて渡すだけである。何を載せるかは呼び出し側が決める。
package session
```

- [ ] **Step 5: コミット**

```bash
git add -A internal/session internal/config
git commit -m "refactor: Drop the signed cookie session and its signing key"
```

---

### Task 6: ドキュメントを直す

**Files:**
- Modify: `README.md:186-197`（`### Sessions` の節）
- Modify: `docs/superpowers/specs/2026-09-10-oidc-auth-design.md:245`（`### セッション` 節の冒頭）

**Interfaces:**
- Consumes: なし
- Produces: なし

- [ ] **Step 1: README の `### Sessions` を書き換える**

現在の文面は「署名付きCookie」「`session.key`」「個別のセッションは失効できない」を前提にしている。次に差し替える。RP-Initiated Logout の段落（`Signing out uses the standard OpenID Connect mechanism...`）はそのまま残す。

```markdown
### Sessions

After a successful login famifo issues a session of its own and stops asking the
provider. The cookie carries nothing but a token; the username lives in
`<data>/sessions.db`. Sessions last 30 days and survive restarts, because that file
does.

Signing out ends the session on the server, so the cookie is useless afterwards even
on a device that kept it. Deleting `sessions.db` signs every device out at once, and
no restart is needed.

Neither is a substitute for revoking access at the provider: a device whose provider
session is still alive is signed straight back in on its next visit without being
asked for anything, confirmed on hardware by a fresh sign-in appearing in the log
seconds after a restart. Real revocation lives at the provider — disable the account,
or end its sessions there.

Upgrading from a version that used signed cookies signs everyone out once: the old
cookies name no session on the server. `<data>/session.key` is no longer read and can
be deleted.
```

- [ ] **Step 2: 既存の spec に追記する**

`docs/superpowers/specs/2026-09-10-oidc-auth-design.md` の `### セッション` の見出しの直後に、1段落だけ足す。本文は当時の判断の記録として残す。

```markdown
> **この節は `2026-09-16-scs-sessions-design.md` で置き換わった。** 署名付き Cookie は
> やめ、セッションは `scs` によるサーバー側の保管（`<data>/sessions.db`）に移した。
> 以下は当時の設計の記録である。とくに「個別のセッションは失効できない」は、
> 現在の実装には当てはまらない。
```

- [ ] **Step 3: 事実でなくなった記述が他に残っていないか探す**

Run: `grep -rn "session.key\|signed cookie\|署名付きCookie\|署名鍵" README.md docs/superpowers/specs/`
Expected: `docs/superpowers/specs/` の古い2つ（`2026-09-10-dsm-auth-design.md` と `2026-09-10-oidc-auth-design.md`）の本文にだけ残る。README には残らない。`docs/superpowers/plans/` 配下は履歴なので触らない。

- [ ] **Step 4: コミット**

```bash
git add README.md docs/superpowers/specs/2026-09-10-oidc-auth-design.md
git commit -m "docs: Describe server-side sessions instead of signed cookies"
```

---

## 完了の確認

- [ ] `CGO_ENABLED=0 make build` が通る
- [ ] `make vet` が通る
- [ ] `make unit-test-race` が通る
- [ ] `make browser-test` が通る
- [ ] `go.mod` の require に `github.com/mattn/go-sqlite3` が無い
- [ ] `grep -rn "famifo_oidc\|session.key" --include='*.go' .` が何も返さない
