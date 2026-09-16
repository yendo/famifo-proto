package session_test

// scsのセッションがsessions.dbに保管され、Cookieのトークンで取り出せることを
// 確かめる。sqlite3storeのSQLは "$1" 形式のプレースホルダを使っていて、これは
// mattnドライバ前提の書き方である。cgo不要のmodernc.org/sqliteでも通ることを、
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

// serve は1リクエストをLoadAndSaveに通し、応答を返す。cookiesを渡すとそれを載せる。
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

	put := serve(t, m, func(_ http.ResponseWriter, r *http.Request) {
		m.Put(r.Context(), "user", "yendo")
	})
	c := cookieNamed(put, session.CookieName)
	require.NotNil(t, c, "a session cookie must be issued")
	require.True(t, c.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, c.SameSite)
	require.False(t, c.Secure, "secure was false")

	var got string
	serve(t, m, func(_ http.ResponseWriter, r *http.Request) {
		got = m.GetString(r.Context(), "user")
	}, c)
	require.Equal(t, "yendo", got)
}

func TestAnExpiredSessionIsNotFound(t *testing.T) {
	m := openStore(t).Manager()

	put := serve(t, m, func(_ http.ResponseWriter, r *http.Request) {
		m.Put(r.Context(), "user", "yendo")
		m.SetDeadline(r.Context(), time.Now().Add(-time.Minute))
	})
	c := cookieNamed(put, session.CookieName)
	require.NotNil(t, c)

	var got string
	serve(t, m, func(_ http.ResponseWriter, r *http.Request) {
		got = m.GetString(r.Context(), "user")
	}, c)
	require.Empty(t, got, "an expired session must not be readable")
}

func TestDestroyRemovesTheSession(t *testing.T) {
	m := openStore(t).Manager()

	put := serve(t, m, func(_ http.ResponseWriter, r *http.Request) {
		m.Put(r.Context(), "user", "yendo")
	})
	c := cookieNamed(put, session.CookieName)
	require.NotNil(t, c)

	serve(t, m, func(_ http.ResponseWriter, r *http.Request) {
		require.NoError(t, m.Destroy(r.Context()))
	}, c)

	var got string
	serve(t, m, func(_ http.ResponseWriter, r *http.Request) {
		got = m.GetString(r.Context(), "user")
	}, c)
	require.Empty(t, got, "a destroyed session must not come back")
}

func TestSecureFollowsTheArgument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := session.Open(path, true, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	resp := serve(t, st.Manager(), func(_ http.ResponseWriter, r *http.Request) {
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
