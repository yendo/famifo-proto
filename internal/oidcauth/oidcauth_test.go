package oidcauth_test

// 偽のIdPを立てて認可コードフローを確かめる。IDトークンはテスト内で作ったRSA鍵で
// 署名する。手書きの検証を選んだので、正常系だけでなく「通ってはいけないもの」を
// 通さないことを固定するのが主眼である。

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/oidcauth"
)

// idp はテスト用のIdP。既定では正しく振る舞い、フィールドを差し替えると壊れた
// 応答を返す。
type idp struct {
	srv            *httptest.Server
	key            *rsa.PrivateKey
	claims         map[string]any // 上書きしたいclaim
	alg            string         // IDトークンのヘッダに載せるalg
	sign           func(signing string) string // 署名の作り方。既定はRS256
	issuerOverride string                       // discoveryが名乗るissuer。空ならi.srv.URL
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	i := &idp{key: key, claims: map[string]any{}, alg: "RS256"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := i.srv.URL
		if i.issuerOverride != "" {
			issuer = i.issuerOverride
		}
		writeJSON(w, map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": i.srv.URL + "/authorize",
			"token_endpoint":         i.srv.URL + "/token",
			"jwks_uri":               i.srv.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "test",
			"n": base64.RawURLEncoding.EncodeToString(i.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(i.key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if r.Form.Get("code") != "good-code" {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalid_grant"})
			return
		}
		writeJSON(w, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 180,
			"id_token": i.idToken(t, r.Form.Get("client_id")),
		})
	})
	i.srv = httptest.NewServer(mux)
	t.Cleanup(i.srv.Close)
	return i
}

func (i *idp) idToken(t *testing.T, aud string) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss": i.srv.URL, "aud": aud, "sub": "yendo", "username": "yendo",
		"email": "yendo@example.invalid", "groups": []string{"users"},
		"iat": now.Unix(), "exp": now.Add(3 * time.Minute).Unix(),
	}
	for k, v := range i.claims {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	hdr := seg(t, map[string]any{"alg": i.alg, "typ": "JWT", "kid": "test"})
	body := seg(t, claims)
	signing := hdr + "." + body
	if i.sign != nil {
		return signing + "." + i.sign(signing)
	}
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func seg(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newClient(t *testing.T, i *idp) *oidcauth.Client {
	t.Helper()
	c, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: i.srv.URL, ClientID: "famifo", ClientSecret: "s3cret",
		RedirectURI: "https://famifo.example.invalid/auth/callback",
	})
	require.NoError(t, err)
	return c
}

func TestExchangeReturnsIdentity(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce

	id, err := c.Exchange(context.Background(), "good-code", p)
	require.NoError(t, err)
	require.Equal(t, "yendo", id.Subject)
	require.Equal(t, "yendo", id.Username)
	require.Equal(t, "yendo@example.invalid", id.Email)
	require.Equal(t, []string{"users"}, id.Groups)
}

func TestAuthURLCarriesTheFlowParameters(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	p := oidcauth.Params{State: "st", Nonce: "no", Verifier: "ve"}

	u, err := url.Parse(c.AuthURL(p))
	require.NoError(t, err)
	q := u.Query()
	require.Equal(t, "famifo", q.Get("client_id"))
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "openid email groups", q.Get("scope"))
	require.Equal(t, "st", q.Get("state"))
	require.Equal(t, "no", q.Get("nonce"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(t, q.Get("code_challenge"))
	require.NotEqual(t, "ve", q.Get("code_challenge"), "the verifier itself must not be sent")
}

func TestExchangeRejectsNonceMismatch(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = "someone-elses-nonce"

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.Error(t, err)
}

func TestExchangeRejectsAnotherSigningKey(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce
	// JWKSで配る鍵とは別の鍵で署名する。
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	i.sign = func(signing string) string {
		sum := sha256.Sum256([]byte(signing))
		sig, err := rsa.SignPKCS1v15(rand.Reader, other, crypto.SHA256, sum[:])
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(sig)
	}

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.Error(t, err)
}

func TestExchangeRejectsAlgNone(t *testing.T) {
	// alg=none で署名を空にしたトークンを受け入れると、中身を好きに書けてしまう。
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce
	i.alg = "none"
	i.sign = func(string) string { return "" }

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.Error(t, err)
}

func TestExchangeRejectsHMACSignedWithThePublicKey(t *testing.T) {
	// algを信じると、公開鍵をHMACの共有鍵として使わされる。公開鍵は誰でも取れるので
	// 誰でも偽造できることになる。
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce
	i.alg = "HS256"
	i.sign = func(signing string) string {
		return hmacSHA256(t, i.key.N.Bytes(), signing)
	}

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.Error(t, err)
}

func TestExchangeRejectsExpired(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce
	i.claims["exp"] = time.Now().Add(-time.Minute).Unix()

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.Error(t, err)
}

func TestExchangeRejectsAnotherAudience(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce
	i.claims["aud"] = "someone-else"

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.Error(t, err)
}

func TestExchangeRejectsAnotherIssuer(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce
	i.claims["iss"] = "https://evil.example.invalid"

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.Error(t, err)
}

func TestExchangeFallsBackToSubWhenUsernameIsAbsent(t *testing.T) {
	// username は標準のclaimではない。別のIdPに差し替えたときに空になりうる。
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce
	i.claims["username"] = nil

	id, err := c.Exchange(context.Background(), "good-code", p)
	require.NoError(t, err)
	require.Equal(t, "yendo", id.Username)
}

func TestExchangeReportsATokenEndpointError(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)

	_, err = c.Exchange(context.Background(), "wrong-code", p)
	require.Error(t, err)
}

func TestNewFailsWhenTheIssuerDoesNotMatch(t *testing.T) {
	i := newIDP(t)
	i.issuerOverride = "https://evil.example.invalid/sso"
	_, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: i.srv.URL, ClientID: "famifo", ClientSecret: "s",
		RedirectURI: "https://famifo.example.invalid/auth/callback",
	})
	require.ErrorContains(t, err, "the issuer does not match")
}

func TestNewFailsWhenDiscoveryIsUnreachable(t *testing.T) {
	i := newIDP(t)
	_, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: i.srv.URL + "/elsewhere", ClientID: "famifo", ClientSecret: "s",
		RedirectURI: "https://famifo.example.invalid/auth/callback",
	})
	require.Error(t, err)
}

func TestNewParamsAreUnpredictable(t *testing.T) {
	a, err := oidcauth.NewParams()
	require.NoError(t, err)
	b, err := oidcauth.NewParams()
	require.NoError(t, err)
	require.NotEqual(t, a.State, b.State)
	require.NotEqual(t, a.Nonce, b.Nonce)
	require.NotEqual(t, a.Verifier, b.Verifier)
	require.GreaterOrEqual(t, len(a.Verifier), 43, "PKCE requires at least 43 characters")
}

func hmacSHA256(t *testing.T, key []byte, msg string) string {
	t.Helper()
	// テスト内でだけ使う。実装側はHMACを一切使わない。
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
