package web_test

// 認証の振り分けを確かめる。IdPは偽物を渡すので、この試験はネットワークに出ない。

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/oidcauth"
	"github.com/yendo/famifo-proto/internal/session"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
	"github.com/yendo/famifo-proto/internal/web"
)

// fakeProvider は認可の往復を演じる。AuthURLは呼ばれた引数を控え、Exchangeは
// 決めた結果を返す。
type fakeProvider struct {
	lastParams oidcauth.Params
	identity   oidcauth.Identity
	err        error
	// endSessionEndpoint はRP-Initiated Logoutの宛先。空なら「対応していない」を
	// 表し、LogoutURLはfalseを返す。実機のSynology SSO Serverはこちらにあたる。
	endSessionEndpoint string
}

func (f *fakeProvider) AuthURL(p oidcauth.Params) string {
	f.lastParams = p
	return "https://idp.example.invalid/authorize?state=" + url.QueryEscape(p.State)
}

func (f *fakeProvider) Exchange(_ context.Context, _ string, _ oidcauth.Params) (oidcauth.Identity, error) {
	return f.identity, f.err
}

func (f *fakeProvider) LogoutURL(postLogoutRedirectURI string) (string, bool) {
	if f.endSessionEndpoint == "" {
		return "", false
	}
	v := url.Values{}
	v.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	v.Set("client_id", "famifo")
	return f.endSessionEndpoint + "?" + v.Encode(), true
}

type authFixture struct {
	h    http.Handler
	prov *fakeProvider
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/famifo.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	thumbs, err := thumb.NewProvider(dir + "/thumbs")
	require.NoError(t, err)

	key := make([]byte, session.KeyLen)

	prov := &fakeProvider{identity: oidcauth.Identity{Subject: "yendo", Username: "yendo"}}
	srv, err := web.NewServer(st, thumbs,
		&web.Auth{OIDC: prov, Key: key, ExternalURL: "https://famifo.example.invalid"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return &authFixture{h: srv.Handler(), prov: prov}
}

func get(t *testing.T, h http.Handler, path string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestPagesRedirectToLoginWhenNotSignedIn(t *testing.T) {
	f := newAuthFixture(t)

	for _, path := range []string{"/", "/item/abc"} {
		resp := get(t, f.h, path)
		require.Equal(t, http.StatusFound, resp.StatusCode, "path %s", path)
		loc, err := url.Parse(resp.Header.Get("Location"))
		require.NoError(t, err)
		require.Equal(t, "/login", loc.Path)
		require.Equal(t, path, loc.Query().Get("next"))
	}
}

func TestDataPathsReturn401WhenNotSignedIn(t *testing.T) {
	// ここでHTMLを返すと、fetchしているクライアント側でJSONの解釈が壊れ、
	// 画面には何も出ないまま原因も読めなくなる。
	f := newAuthFixture(t)

	for _, path := range []string{"/tiles?offset=0&limit=1", "/thumb/abc", "/file/abc"} {
		resp := get(t, f.h, path)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "path %s", path)
	}
}

func TestStaticIsServedWithoutSigningIn(t *testing.T) {
	f := newAuthFixture(t)

	resp := get(t, f.h, "/static/app.css")
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestLoginRedirectsToTheProvider(t *testing.T) {
	f := newAuthFixture(t)

	resp := get(t, f.h, "/login?next=/item/abc")
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "idp.example.invalid")
	require.NotEmpty(t, f.prov.lastParams.State)
	require.NotEmpty(t, f.prov.lastParams.Nonce)
	require.NotEmpty(t, f.prov.lastParams.Verifier)
	require.NotNil(t, cookieNamed(resp, "famifo_oidc"), "the flow state must be carried in a cookie")
}

// TestLoginFlowCookieDoesNotAuthenticate はログイン往復用Cookieをそのまま
// セッションCookieとして送り返しても認証されないことを固定する。用途ごとに
// 別のCodecを導出していないと、往復用の署名済み値がそのままセッションとして
// 通ってしまう。
func TestLoginFlowCookieDoesNotAuthenticate(t *testing.T) {
	f := newAuthFixture(t)

	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_oidc")
	require.NotNil(t, flow)

	forged := &http.Cookie{Name: "famifo_session", Value: flow.Value}
	resp := get(t, f.h, "/", forged)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/login")
}

func TestCallbackIssuesASessionAndReturnsToNext(t *testing.T) {
	f := newAuthFixture(t)

	start := get(t, f.h, "/login?next=/item/abc")
	flow := cookieNamed(start, "famifo_oidc")
	require.NotNil(t, flow)

	resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/item/abc", resp.Header.Get("Location"))

	sess := cookieNamed(resp, "famifo_session")
	require.NotNil(t, sess)
	require.True(t, sess.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, sess.SameSite)

	// 発行されたセッションでギャラリーが開ける。
	page := get(t, f.h, "/", sess)
	require.Equal(t, http.StatusOK, page.StatusCode)
}

func TestCallbackRejectsAStateMismatch(t *testing.T) {
	f := newAuthFixture(t)
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_oidc")

	resp := get(t, f.h, "/auth/callback?code=good&state=not-the-one", flow)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Nil(t, cookieNamed(resp, "famifo_session"))
}

func TestCallbackRejectsAMissingFlowCookie(t *testing.T) {
	f := newAuthFixture(t)
	_ = get(t, f.h, "/login")

	resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestCallbackReportsAnUnreachableProvider はIdPに届かなかった場合、従来どおり
// 502と「IdPに到達できない」を返すことを固定する。
func TestCallbackReportsAnUnreachableProvider(t *testing.T) {
	f := newAuthFixture(t)
	f.prov.err = context.DeadlineExceeded
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_oidc")

	resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	require.Contains(t, bodyOf(t, resp), "cannot reach")
}

// TestCallbackReportsAProviderRefusal はIdPが応答したうえで交換を拒んだ場合、
// 「到達できない」ではなく拒まれた旨を伝え、502は使わないことを固定する。
// 実機のインシデントでは両方とも「IdPに到達できない」と表示され、原因調査を
// 誤った方向に導いた。
func TestCallbackReportsAProviderRefusal(t *testing.T) {
	f := newAuthFixture(t)
	f.prov.err = fmt.Errorf("cannot exchange the authorization code: %w",
		&oidcauth.ProviderError{StatusCode: http.StatusBadRequest, Body: `{"error":"server_error"}`})
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_oidc")

	resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	body := bodyOf(t, resp)
	require.Contains(t, body, "refused")
	require.NotContains(t, body, "cannot reach")
	require.Contains(t, body, `href="/login"`)
}

// TestCallbackFailureClearsTheFlowCookie は失敗した往復のあとにやり直しても、
// 古い一時Cookieがそのまま10分残らないことを固定する。残ると「やり直し」が
// 実際にはやり直しにならない。
func TestCallbackFailureClearsTheFlowCookie(t *testing.T) {
	f := newAuthFixture(t)
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_oidc")

	resp := get(t, f.h, "/auth/callback?code=good&state=not-the-one", flow)
	cleared := cookieNamed(resp, "famifo_oidc")
	require.NotNil(t, cleared, "the flow cookie must be reset on failure")
	require.Less(t, cleared.MaxAge, 0, "the flow cookie must be told to expire")
}

// TestCallbackFailureGivesAWayBack はエラー画面に /login への導線があることを
// 固定する。別タブが一時Cookieを上書きしてここに来るのは日常的に起きる。
func TestCallbackFailureGivesAWayBack(t *testing.T) {
	f := newAuthFixture(t)
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_oidc")

	resp := get(t, f.h, "/auth/callback?code=good&state=not-the-one", flow)
	require.Contains(t, bodyOf(t, resp), `href="/login"`)
}

func TestNextMustBeALocalPath(t *testing.T) {
	// "//evil.example" はプロトコル相対URLで、別サイトへのリダイレクトになる。
	f := newAuthFixture(t)

	for _, next := range []string{"//evil.example", "https://evil.example", "http://evil.example/x", `/\evil.example`, `/\/evil.example`, "/\t/evil.example", "/\n/evil.example"} {
		start := get(t, f.h, "/login?next="+url.QueryEscape(next))
		flow := cookieNamed(start, "famifo_oidc")
		resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
		require.Equal(t, "/", resp.Header.Get("Location"), "next %q must be refused", next)
	}
}

// TestNextKeepsALegitimatePath は正当なパスがそのまま戻り先として使われる
// ことを固定する。拒否だけを確かめても、safeNextが過剰に締め付けて通常の
// 遷移まで壊していないかは分からない。
func TestNextKeepsALegitimatePath(t *testing.T) {
	f := newAuthFixture(t)

	next := "/item/abc123"
	start := get(t, f.h, "/login?next="+url.QueryEscape(next))
	flow := cookieNamed(start, "famifo_oidc")
	resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	require.Equal(t, next, resp.Header.Get("Location"))
}

// TestLogoutClearsTheSession は /logout がセッションと往復用Cookieの両方を
// 破棄し、かつ保護された領域へリダイレクトしないことを固定する。/ へ戻すと、
// IdP側のセッションがまだ生きている場合に /login からそのまま再ログインが
// 走ってしまい、サインアウトが見た目上何もしていないことになる。
func TestLogoutClearsTheSession(t *testing.T) {
	f := newAuthFixture(t)
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, "famifo_oidc")
	cb := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	sess := cookieNamed(cb, "famifo_session")

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(sess)
	req.AddCookie(flow)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	resp := rec.Result()

	require.Equal(t, http.StatusOK, resp.StatusCode, "logout must not redirect back into the protected area")

	cleared := cookieNamed(resp, "famifo_session")
	require.NotNil(t, cleared)
	require.Less(t, cleared.MaxAge, 0, "the session cookie must be told to expire")

	clearedFlow := cookieNamed(resp, "famifo_oidc")
	require.NotNil(t, clearedFlow)
	require.Less(t, clearedFlow.MaxAge, 0, "the flow cookie must be told to expire")

	require.Contains(t, bodyOf(t, resp), `href="/login"`, "the page must offer a way to sign in again")
}

// TestLogoutRedirectsToTheProviderWhenSupported は、IdP が discovery で
// end_session_endpoint を広告しているとき、/logout がfamifo自身のCookieを
// 消したうえでそちらへ302することを固定する。post_logout_redirect_uriが
// famifoの/signed-outを指し、client_idが載っていることまで確かめる。
// リダイレクトが起きたことだけでは、宛先を取り違えても気づけない。
func TestLogoutRedirectsToTheProviderWhenSupported(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/famifo.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	thumbs, err := thumb.NewProvider(dir + "/thumbs")
	require.NoError(t, err)
	prov := &fakeProvider{
		identity:           oidcauth.Identity{Subject: "yendo", Username: "yendo"},
		endSessionEndpoint: "https://idp.example.invalid:5001/webman/logout.cgi",
	}
	srv, err := web.NewServer(st, thumbs,
		&web.Auth{OIDC: prov, Key: make([]byte, session.KeyLen), ExternalURL: "https://famifo.example.invalid"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	h := srv.Handler()

	start := get(t, h, "/login")
	flow := cookieNamed(start, "famifo_oidc")
	cb := get(t, h, "/auth/callback?code=good&state="+url.QueryEscape(prov.lastParams.State), flow)
	sess := cookieNamed(cb, "famifo_session")

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(sess)
	req.AddCookie(flow)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
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

	clearedFlow := cookieNamed(resp, "famifo_oidc")
	require.NotNil(t, clearedFlow)
	require.Less(t, clearedFlow.MaxAge, 0, "the flow cookie must be told to expire")
}

// TestSignedOutRendersWithoutASession は GET /signed-out がミドルウェアの
// 外側にあり、セッションが無くても表示できることを固定する。RP-Initiated
// Logoutでは、IdP側のセッションを終えたあとブラウザがここへ戻ってくるが、
// その時点でfamifo自身のCookieはすでに/logoutが消している。
func TestSignedOutRendersWithoutASession(t *testing.T) {
	f := newAuthFixture(t)

	resp := get(t, f.h, "/signed-out")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := bodyOf(t, resp)
	require.Contains(t, body, `href="/login"`)
	// ログイン画面側でもサインアウトが要る旨と、DSMを条件付きで名指しする
	// 案内が消えないことを固定する。famifoは特定のIdPに依存しないので、
	// DSMは「使っている場合」の条件としてのみ出てよい。
	require.Contains(t, body, "also sign out on the login screen you originally used")
	require.Contains(t, body, "if that's DSM, sign out of DSM")
}

func TestSecureAttributeFollowsTheSetting(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/famifo.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	thumbs, err := thumb.NewProvider(dir + "/thumbs")
	require.NoError(t, err)
	prov := &fakeProvider{identity: oidcauth.Identity{Subject: "yendo", Username: "yendo"}}
	srv, err := web.NewServer(st, thumbs,
		&web.Auth{OIDC: prov, Key: make([]byte, session.KeyLen), Secure: true},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	h := srv.Handler()

	resp := get(t, h, "/login")
	require.True(t, cookieNamed(resp, "famifo_oidc").Secure)
}

func TestAnExpiredSessionIsRefused(t *testing.T) {
	f := newAuthFixture(t)
	codec, err := session.NewCodec(make([]byte, session.KeyLen), "session")
	require.NoError(t, err)
	stale := &http.Cookie{Name: "famifo_session", Value: codec.Sign("yendo", time.Now().Add(-time.Minute))}

	resp := get(t, f.h, "/", stale)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/login")
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// 認証を無効にした構成は今までどおり動く。既存のテストが nil を渡しているが、
// 振る舞いをここでも1本固定しておく。
func TestWithoutAuthEverythingIsOpen(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/famifo.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	thumbs, err := thumb.NewProvider(dir + "/thumbs")
	require.NoError(t, err)
	srv, err := web.NewServer(st, thumbs, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	resp := get(t, srv.Handler(), "/")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, bodyOf(t, resp), "/logout", "the logout button must not be shown")
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
