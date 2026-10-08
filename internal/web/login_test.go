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

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/oidc"
	"github.com/yendo/famifo-proto/internal/session"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
	"github.com/yendo/famifo-proto/internal/web"
)

// cookieName は発行されるCookieの名前。実装側の定数を参照せず、ここに書き写して
// ある。共有すると名前を変えたときに両方が同時に変わって黙って通るが、名前が
// 変わると全端末がログアウトするので落ちてほしい。
const cookieName = "famifo_session"

// fakeProvider は認可の往復を演じる。AuthURLは呼ばれた引数を控え、Exchangeは
// 決めた結果を返す。
type fakeProvider struct {
	lastParams oidc.Params
	identity   oidc.Identity
	err        error
	// endSessionEndpoint はRP-Initiated Logoutの宛先。空なら「対応していない」を
	// 表し、LogoutURLはfalseを返す。実機のSynology SSO Serverはこちらにあたる。
	endSessionEndpoint string
}

func (f *fakeProvider) AuthURL(p oidc.Params) string {
	f.lastParams = p
	return "https://idp.example.invalid/authorize?state=" + url.QueryEscape(p.State)
}

func (f *fakeProvider) Exchange(_ context.Context, _ string, _ oidc.Params) (oidc.Identity, error) {
	return f.identity, f.err
}

func (f *fakeProvider) LogoutURL(postLogoutRedirectURI, idTokenHint string) (string, bool) {
	if f.endSessionEndpoint == "" {
		return "", false
	}
	v := url.Values{}
	v.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	v.Set("client_id", "famifo")
	if idTokenHint != "" {
		v.Set("id_token_hint", idTokenHint)
	}
	return f.endSessionEndpoint + "?" + v.Encode(), true
}

type authFixture struct {
	h    http.Handler
	prov *fakeProvider
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()
	return newAuthFixtureWith(t, &fakeProvider{identity: oidc.Identity{Subject: "yendo"}}, false)
}

// newAuthFixtureWith はIdPの偽物とSecureの設定を選べる版。
func newAuthFixtureWith(t *testing.T, prov *fakeProvider, secure bool) *authFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/famifo.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	thumbs, err := thumb.NewProvider(dir + "/thumbs")
	require.NoError(t, err)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.New(dir+"/sessions.db", secure, log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sessions.Close() })

	h, err := web.NewHandler(st, thumbs,
		web.NewAuth(prov, sessions, "https://famifo.example.invalid", log),
		web.DefaultChunkSize, log)
	require.NoError(t, err)
	return &authFixture{h: h, prov: prov}
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

func post(t *testing.T, h http.Handler, path string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// signIn は往復を1周して、ログイン済みのCookieを返す。
func signIn(t *testing.T, f *authFixture) *http.Cookie {
	t.Helper()
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, cookieName)
	require.NotNil(t, flow)
	cb := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	sess := cookieNamed(cb, cookieName)
	require.NotNil(t, sess)
	return sess
}

func cookieNamed(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
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

// TestDataPathsReturn401WhenNotSignedIn は isDataPath が本物の経路と揃って
// いることを固定する。ここでHTMLを返すと、fetchしているクライアント側でJSONの
// 解釈が壊れ、画面には何も出ないまま原因も読めなくなる。データを返す経路を
// 足したときにここが落ちるのが望ましい。
func TestDataPathsReturn401WhenNotSignedIn(t *testing.T) {
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
	require.NotNil(t, cookieNamed(resp, cookieName), "the login flow must be carried in a session")
}

func TestCallbackIssuesASessionAndReturnsToNext(t *testing.T) {
	f := newAuthFixture(t)

	start := get(t, f.h, "/login?next=/item/abc")
	flow := cookieNamed(start, cookieName)
	require.NotNil(t, flow)

	resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/item/abc", resp.Header.Get("Location"))

	sess := cookieNamed(resp, cookieName)
	require.NotNil(t, sess)
	require.True(t, sess.HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, sess.SameSite)
	require.NotEqual(t, flow.Value, sess.Value, "the token must be renewed when signing in")

	// 発行されたセッションでギャラリーが開ける。
	page := get(t, f.h, "/", sess)
	require.Equal(t, http.StatusOK, page.StatusCode)
	require.Contains(t, bodyOf(t, page), "/logout", "the logout button must be shown")
}

func TestCallbackRejectsAStateMismatch(t *testing.T) {
	f := newAuthFixture(t)
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, cookieName)

	resp := get(t, f.h, "/auth/callback?code=good&state=not-the-one", flow)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	// セッションは発行されない。Cookieは出るが、失敗した往復を捨てるための
	// 期限切れの指示である。
	cleared := cookieNamed(resp, cookieName)
	require.NotNil(t, cleared)
	require.Less(t, cleared.MaxAge, 0, "no session may be issued on a state mismatch")
}

func TestCallbackRejectsAMissingSession(t *testing.T) {
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
	flow := cookieNamed(start, cookieName)

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
		&oidc.ProviderError{StatusCode: http.StatusBadRequest, Code: "server_error"})
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, cookieName)

	resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	body := bodyOf(t, resp)
	require.Contains(t, body, "refused")
	require.NotContains(t, body, "cannot reach")
	require.Contains(t, body, `href="/login"`)
}

// TestCallbackFailureDiscardsTheSession は失敗した往復のあとにやり直しても、
// 中途半端な状態がそのまま10分残らないことを固定する。残ると「やり直し」が
// 実際にはやり直しにならない。
func TestCallbackFailureDiscardsTheSession(t *testing.T) {
	f := newAuthFixture(t)
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, cookieName)

	resp := get(t, f.h, "/auth/callback?code=good&state=not-the-one", flow)
	cleared := cookieNamed(resp, cookieName)
	require.NotNil(t, cleared, "the session must be reset on failure")
	require.Less(t, cleared.MaxAge, 0, "the session cookie must be told to expire")

	// 捨てられたことをサーバー側でも確かめる。同じCookieでやり直しても、
	// 往復の値はもう残っていない。
	again := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	require.Equal(t, http.StatusBadRequest, again.StatusCode)
}

// TestCallbackFailureGivesAWayBack はエラー画面に /login への導線があることを
// 固定する。別タブが一時Cookieを上書きしてここに来るのは日常的に起きる。
func TestCallbackFailureGivesAWayBack(t *testing.T) {
	f := newAuthFixture(t)
	start := get(t, f.h, "/login")
	flow := cookieNamed(start, cookieName)

	resp := get(t, f.h, "/auth/callback?code=good&state=not-the-one", flow)
	require.Contains(t, bodyOf(t, resp), `href="/login"`)
}

func TestNextMustBeALocalPath(t *testing.T) {
	// "//evil.example" はプロトコル相対URLで、別サイトへのリダイレクトになる。
	f := newAuthFixture(t)

	for _, next := range []string{"//evil.example", "https://evil.example", "http://evil.example/x", `/\evil.example`, `/\/evil.example`, "/\t/evil.example", "/\n/evil.example"} {
		start := get(t, f.h, "/login?next="+url.QueryEscape(next))
		flow := cookieNamed(start, cookieName)
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
	flow := cookieNamed(start, cookieName)
	resp := get(t, f.h, "/auth/callback?code=good&state="+url.QueryEscape(f.prov.lastParams.State), flow)
	require.Equal(t, next, resp.Header.Get("Location"))
}

// TestLogoutClearsTheSession は /logout がセッションを破棄し、かつ保護された
// 領域へリダイレクトしないことを固定する。/ へ戻すと、IdP側のセッションが
// まだ生きている場合に /login からそのまま再ログインが走ってしまい、
// サインアウトが見た目上何もしていないことになる。
func TestLogoutClearsTheSession(t *testing.T) {
	f := newAuthFixture(t)
	sess := signIn(t, f)

	resp := post(t, f.h, "/logout", sess)
	require.Equal(t, http.StatusOK, resp.StatusCode, "logout must not redirect back into the protected area")

	cleared := cookieNamed(resp, cookieName)
	require.NotNil(t, cleared)
	require.Less(t, cleared.MaxAge, 0, "the session cookie must be told to expire")

	require.Contains(t, bodyOf(t, resp), `href="/login"`, "the page must offer a way to sign in again")
}

// TestLogoutRedirectsToTheProviderWhenSupported は、IdP が discovery で
// end_session_endpoint を広告しているとき、/logout がfamifo自身のCookieを
// 消したうえでそちらへ302することを固定する。post_logout_redirect_uriが
// famifoの/signed-outを指し、client_idが載っていることまで確かめる。
// リダイレクトが起きたことだけでは、宛先を取り違えても気づけない。
func TestLogoutRedirectsToTheProviderWhenSupported(t *testing.T) {
	prov := &fakeProvider{
		identity:           oidc.Identity{Subject: "yendo"},
		endSessionEndpoint: "https://idp.example.invalid:5001/webman/logout.cgi",
	}
	f := newAuthFixtureWith(t, prov, false)
	sess := signIn(t, f)

	resp := post(t, f.h, "/logout", sess)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "idp.example.invalid:5001", loc.Host)
	require.Equal(t, "/webman/logout.cgi", loc.Path)
	require.Equal(t, "https://famifo.example.invalid/signed-out", loc.Query().Get("post_logout_redirect_uri"))
	require.Equal(t, "famifo", loc.Query().Get("client_id"))

	cleared := cookieNamed(resp, cookieName)
	require.NotNil(t, cleared)
	require.Less(t, cleared.MaxAge, 0, "the session cookie must be told to expire")
}

// TestLogoutCarriesTheIDTokenHint は、サインインしたときのIDトークンが
// /logout のリダイレクト先に id_token_hint として載ることを固定する。
//
// セッションを破棄する前に取り出せているかの確認でもある。Destroyを先に走らせる
// 実装ではここが空になって落ちる。仕様上、hintの無いログアウトではIdPは
// post_logout_redirect_uri を尊重しなくてよいので、空で送ることは
// 「サインアウトはできるが/signed-outに帰ってこない」を意味する。
func TestLogoutCarriesTheIDTokenHint(t *testing.T) {
	prov := &fakeProvider{
		identity: oidc.Identity{
			Subject: "yendo", IDToken: "header.payload.signature",
		},
		endSessionEndpoint: "https://idp.example.invalid:5001/webman/logout.cgi",
	}
	f := newAuthFixtureWith(t, prov, false)
	sess := signIn(t, f)

	resp := post(t, f.h, "/logout", sess)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "header.payload.signature", loc.Query().Get("id_token_hint"))
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
	// 認証の内側を通らないページにもCSSが当たる。
	require.Contains(t, body, `href="/static/app.css"`)
}

func TestSecureAttributeFollowsTheSetting(t *testing.T) {
	f := newAuthFixtureWith(t, &fakeProvider{identity: oidc.Identity{Subject: "yendo"}}, true)

	resp := get(t, f.h, "/login")
	require.True(t, cookieNamed(resp, cookieName).Secure)
}

// TestAnUnknownTokenIsRefused はサーバー側に無いトークンを送っても認証されない
// ことを固定する。期限切れも、消されたセッションも、偽造も、サーバーから見れば
// すべて「その行が無い」に落ちる。
func TestAnUnknownTokenIsRefused(t *testing.T) {
	f := newAuthFixture(t)
	stale := &http.Cookie{Name: cookieName, Value: "PTgYqBpEB4gGAWRpPfSLQYCFXQGm6vVX7ptHdOLh0kM"}

	resp := get(t, f.h, "/", stale)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/login")
}

// TestSignOutRevokesTheSessionOnTheServer はログアウトのあと、同じCookieを
// 送り直しても入れないことを固定する。署名付きCookieだった頃はサーバーが
// 発行済みの値を知らなかったので、これは書けなかった。
func TestSignOutRevokesTheSessionOnTheServer(t *testing.T) {
	f := newAuthFixture(t)
	sess := signIn(t, f)
	require.Equal(t, http.StatusOK, get(t, f.h, "/", sess).StatusCode)

	require.Equal(t, http.StatusOK, post(t, f.h, "/logout", sess).StatusCode)

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
	flow := cookieNamed(start, cookieName)
	require.NotNil(t, flow)

	resp := get(t, f.h, "/", flow)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Location"), "/login")
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
	h, err := web.NewHandler(st, thumbs, nil, web.DefaultChunkSize, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	resp := get(t, h, "/")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, bodyOf(t, resp), "/logout", "the logout button must not be shown")
}
