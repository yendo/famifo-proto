//go:build browser

// spec (docs/superpowers/specs/2026-09-10-oidc-auth-design.md, テスト節) は
// 「偽の IdP を立てて、リダイレクトからギャラリー表示までを1本」通すブラウザテストを
// 求めている。auth_test.go の fakeProvider は internal/web.Provider を直接満たす
// スタブで、oidcauth.Client を経由しない。oidcauth_test.go は逆に internal/web を
// 一切通さない。したがって Provider インターフェース、Params が一時Cookieの
// JSON を往復すること、Identity.Username がセッションのpayloadになることは、
// これまでどこにもテストされていなかった。このファイルは、本物の oidcauth.Client を
// 本物の web.Server に対して動かし、ブラウザで実際にリダイレクトを辿らせて
// その境目を通す。
package web_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/oidcauth"
	"github.com/yendo/famifo-proto/internal/session"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
	"github.com/yendo/famifo-proto/internal/web"
)

// fakeIDP はブラウザ越しの認可コードフローを1往復させるための最小のIdP。
// ログイン画面は無く、/authorize は照会なしに即座にcallbackへリダイレクトする。
// このテストの関心はfamifo側の配線であって、IdPのUIではないためである。
// ID トークンの署名手法は internal/oidcauth/oidcauth_test.go の idp と同じだが、
// パッケージが違うので呼び回せず、ここに小さくコピーしてある。
type fakeIDP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	lastNonce string // 直近の /authorize が受け取ったnonce。/token が id_token に載せる
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	i := &fakeIDP{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeOIDCJSON(w, map[string]any{
			"issuer":                 i.srv.URL,
			"authorization_endpoint": i.srv.URL + "/authorize",
			"token_endpoint":         i.srv.URL + "/token",
			"jwks_uri":               i.srv.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		writeOIDCJSON(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
			"n": base64.RawURLEncoding.EncodeToString(i.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(i.key.E)).Bytes()),
		}}})
	})
	// /authorize はログイン画面を持たない。受け取ったstateとnonceをそのまま
	// 使い、決め打ちのcodeを1つ発行してredirect_uriへ即座に送り返す。
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		i.lastNonce = q.Get("nonce")
		u, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "bad redirect_uri", http.StatusBadRequest)
			return
		}
		rq := u.Query()
		rq.Set("code", "good-code")
		rq.Set("state", q.Get("state"))
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("code") != "good-code" {
			w.WriteHeader(http.StatusBadRequest)
			writeOIDCJSON(w, map[string]any{"error": "invalid_grant"})
			return
		}
		writeOIDCJSON(w, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 180,
			"id_token": i.idToken(t, r.Form.Get("client_id")),
		})
	})
	i.srv = httptest.NewServer(mux)
	t.Cleanup(i.srv.Close)
	return i
}

func (i *fakeIDP) idToken(t *testing.T, aud string) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss": i.srv.URL, "aud": aud, "sub": "yendo", "username": "yendo",
		"email": "yendo@example.invalid", "groups": []string{"users"},
		"iat": now.Unix(), "exp": now.Add(3 * time.Minute).Unix(),
		"nonce": i.lastNonce,
	}
	hdr := oidcSeg(t, map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test"})
	body := oidcSeg(t, claims)
	signing := hdr + "." + body
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func oidcSeg(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeOIDCJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// newOIDCTestApp は偽のIdPに対して本物のoidcauth.Clientとweb.Serverを組み立て、
// famifoのURLを返す。web.NewServerがredirect_uriとしてfamifo自身のURLを必要と
// するため、httptest.NewServerでハンドラを渡す前にポートを確保しておく
// （net.Listenで先にポートを取り、httptest.NewUnstartedServerへ差し込む）。
func newOIDCTestApp(t *testing.T) (famifoURL string) {
	t.Helper()

	idp := newFakeIDP(t)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	famifoURL = "http://" + l.Addr().String()

	client, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: idp.srv.URL, ClientID: "famifo", ClientSecret: "s3cret",
		RedirectURI: famifoURL + "/auth/callback",
	})
	require.NoError(t, err)

	dir := t.TempDir()
	photoDir := filepath.Join(dir, "photos")
	require.NoError(t, os.MkdirAll(photoDir, 0o755))
	thumbs, err := thumb.NewProvider(filepath.Join(dir, "thumbs"))
	require.NoError(t, err)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, writeTestPhoto(filepath.Join(photoDir, "p0000.jpg"), 0, time.Now()))
	_, err = indexAll(st, photoDir, thumbs)
	require.NoError(t, err)

	key := make([]byte, session.KeyLen)
	_, err = rand.Read(key)
	require.NoError(t, err)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	webSrv, err := web.NewServer(st, thumbs, &web.Auth{OIDC: client, Key: key}, log)
	require.NoError(t, err)

	ts := httptest.NewUnstartedServer(webSrv.Handler())
	_ = ts.Listener.Close()
	ts.Listener = l
	ts.Start()
	t.Cleanup(ts.Close)

	return famifoURL
}

// TestOIDCRoundTripReachesTheGallery は「/ を開く → 未認証なので /login →
// 偽IdPの /authorize → famifoの /auth/callback → セッションが張られて / に
// 戻る → タイルが描画される」を実ブラウザで1本通す。
func TestOIDCRoundTripReachesTheGallery(t *testing.T) {
	requireBrowser(t)
	famifoURL := newOIDCTestApp(t)

	ctx := newTab(t)
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	require.NoError(t, chromedp.Run(rctx,
		chromedp.Navigate(famifoURL+"/"),
		waitForTiles(10*time.Second),
	))

	var loc string
	require.NoError(t, chromedp.Run(rctx, chromedp.Location(&loc)))
	require.Equal(t, famifoURL+"/", loc, "the browser must have landed back on the gallery, not stuck on /login or /auth/callback")
}
