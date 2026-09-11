package oidcauth_test

// 偽のIdPを立てて認可コードフローを確かめる。IDトークンはテスト内で作ったRSA鍵で
// 署名する。正常系だけでなく「通ってはいけないもの」を通さないことを固定するのが
// 主眼である。検証の大半はgo-oidcに委ねているが、依存の入れ替えやアップグレードが
// 検証を静かに緩めても気づけるように、拒否されるべき経路をここでピン留めする。

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/oidcauth"
)

// idp はテスト用のIdP。既定では正しく振る舞い、フィールドを差し替えると壊れた
// 応答を返す。
type idp struct {
	srv            *httptest.Server
	key            *rsa.PrivateKey
	claims         map[string]any              // 上書きしたいclaim
	alg            string                      // IDトークンのヘッダに載せるalg
	sign           func(signing string) string // 署名の作り方。既定はRS256
	issuerOverride string                      // discoveryが名乗るissuer。空ならi.srv.URL
	tokenErrStatus int                         // 0なら既定の400/invalid_grantを返す
	tokenErrBody   []byte                      // tokenErrStatusとあわせて使う
	endSession     string                      // discoveryに載せるend_session_endpoint。空なら載せない
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
		doc := map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": i.srv.URL + "/authorize",
			"token_endpoint":         i.srv.URL + "/token",
			"jwks_uri":               i.srv.URL + "/jwks",
		}
		if i.endSession != "" {
			doc["end_session_endpoint"] = i.endSession
		}
		writeJSON(w, doc)
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
			if i.tokenErrStatus != 0 {
				w.WriteHeader(i.tokenErrStatus)
				_, _ = w.Write(i.tokenErrBody)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalid_grant"})
			return
		}
		// x/oauth2 は既定でまず HTTP Basic を試す。Synology SSO Server は
		// client_secret_basic と client_secret_post の両方を広告しているので、
		// 偽物も両方受ける。
		id := r.Form.Get("client_id")
		if u, _, ok := r.BasicAuth(); ok && u != "" {
			id = u
		}
		writeJSON(w, map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 180,
			"id_token": i.idToken(t, id),
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
	// nonceの照合はgo-oidcに移していない、famifo自身のコードである。ここでの
	// アサーションを緩めると、この防御が別の理由で通っていても気づけなくなる。
	require.ErrorContains(t, err, "the id_token nonce does not match the one we sent")
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

func TestExchangeRejectsEmptySubject(t *testing.T) {
	// go-oidc はsubが空でも拒否しない。空のままだと誰のものでもない利用者として
	// セッションを張ってしまうので、famifo側で弾く。
	i := newIDP(t)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)
	i.claims["nonce"] = p.Nonce
	i.claims["sub"] = ""

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.ErrorContains(t, err, "the id_token carries no subject")
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

// TestExchangeReportsAProviderRefusal はIdPがトークン交換に応答したうえで拒んだ
// ときに、ステータスと本文（error_descriptionを含む）が取り出せることを固定する。
// 実機のインシデントでは "server_error" とだけ言われ、本文を捨てていたせいで
// 原因の手がかりが残らなかった。
func TestExchangeReportsAProviderRefusal(t *testing.T) {
	i := newIDP(t)
	i.tokenErrStatus = http.StatusBadRequest
	i.tokenErrBody = []byte(`{"error":"server_error","error_description":"upstream hiccup"}`)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)

	_, err = c.Exchange(context.Background(), "wrong-code", p)
	require.Error(t, err)
	require.ErrorIs(t, err, oidcauth.ErrProviderRefused)

	var perr *oidcauth.ProviderError
	require.ErrorAs(t, err, &perr)
	require.Equal(t, http.StatusBadRequest, perr.StatusCode)
	require.Contains(t, perr.Body, "upstream hiccup")
}

// TestExchangeTruncatesAHugeProviderBody は本文を1KiBに切り詰めることを固定する。
// 相手が暴れても、ログが埋まらないようにするため。
func TestExchangeTruncatesAHugeProviderBody(t *testing.T) {
	i := newIDP(t)
	i.tokenErrStatus = http.StatusInternalServerError
	i.tokenErrBody = bytes.Repeat([]byte("a"), 10*1024)
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)

	_, err = c.Exchange(context.Background(), "wrong-code", p)
	var perr *oidcauth.ProviderError
	require.ErrorAs(t, err, &perr)
	require.LessOrEqual(t, len(perr.Body), 1024)
}

// TestExchangeReportsAnUnreachableProvider は「IdPに届かない」と「IdPに拒まれた」
// を区別できることを固定する。discoveryが済んだあとにサーバーを落とし、
// トークンエンドポイントへの接続そのものを失敗させる。
func TestExchangeReportsAnUnreachableProvider(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)
	i.srv.Close()
	p, err := oidcauth.NewParams()
	require.NoError(t, err)

	_, err = c.Exchange(context.Background(), "good-code", p)
	require.Error(t, err)
	require.NotErrorIs(t, err, oidcauth.ErrProviderRefused)
	var perr *oidcauth.ProviderError
	require.False(t, errors.As(err, &perr))
}

// TestExchangeTruncatesOnARuneBoundary は、1024バイト目がマルチバイト文字の
// 途中に落ちるときでも、保存される本文が有効なUTF-8であることを固定する。
// バイト単位で機械的に切り詰めると不正なUTF-8になり、slogが出力時にU+FFFDへ
// 置き換えて見た目が壊れる。
func TestExchangeTruncatesOnARuneBoundary(t *testing.T) {
	i := newIDP(t)
	i.tokenErrStatus = http.StatusInternalServerError
	// "a" を1023バイト並べたあとに3バイトの"あ"を置くと、1024バイト目
	// （0始まりでindex 1023）は"あ"の先頭バイトに落ちる。
	body := append(bytes.Repeat([]byte("a"), 1023), []byte("あ")...)
	body = append(body, bytes.Repeat([]byte("b"), 100)...)
	i.tokenErrBody = body
	c := newClient(t, i)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)

	_, err = c.Exchange(context.Background(), "wrong-code", p)
	var perr *oidcauth.ProviderError
	require.ErrorAs(t, err, &perr)
	require.True(t, utf8.ValidString(perr.Body), "truncated body must be valid UTF-8")
	require.LessOrEqual(t, len(perr.Body), 1024)
}

// TestExchangeRedactsTheClientSecretFromTheProviderBody は、IdPがリクエストを
// そのまま読み返すエラー応答を返したときに、client secretがProviderErrorへ
// そのまま流れ込まないことを固定する。client_secret_post を固定しているため、
// secretはすべての交換リクエストのPOSTボディに載っている。
func TestExchangeRedactsTheClientSecretFromTheProviderBody(t *testing.T) {
	i := newIDP(t)
	i.tokenErrStatus = http.StatusBadRequest
	i.tokenErrBody = []byte(`{"error":"server_error","echo":"client_secret=s3cret&code=wrong-code"}`)
	c := newClient(t, i) // newClientは ClientSecret: "s3cret" で組み立てる
	p, err := oidcauth.NewParams()
	require.NoError(t, err)

	_, err = c.Exchange(context.Background(), "wrong-code", p)
	var perr *oidcauth.ProviderError
	require.ErrorAs(t, err, &perr)
	require.NotContains(t, perr.Body, "s3cret")
	require.Contains(t, perr.Body, "[redacted]")
}

// TestExchangeRedactsAURLEncodedClientSecret は、secretがform-reservedな文字
// （"+" "/" "="）を含むときに、x/oauth2 がPOSTボディで使うパーセントエンコード
// 形でもリダクションが効くことを固定する。base64由来のsecretはこれらの文字を
// 含むのが普通で、リテラル一致だけでは取りこぼす。
func TestExchangeRedactsAURLEncodedClientSecret(t *testing.T) {
	i := newIDP(t)
	secret := "se+cr/et="
	encoded := url.QueryEscape(secret)
	require.NotEqual(t, secret, encoded, "the secret must actually need encoding for this test to mean anything")
	i.tokenErrStatus = http.StatusBadRequest
	i.tokenErrBody = []byte(`{"error":"server_error","echo":"client_secret=` + encoded + `&code=wrong-code"}`)
	c, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: i.srv.URL, ClientID: "famifo", ClientSecret: secret,
		RedirectURI: "https://famifo.example.invalid/auth/callback",
	})
	require.NoError(t, err)
	p, err := oidcauth.NewParams()
	require.NoError(t, err)

	_, err = c.Exchange(context.Background(), "wrong-code", p)
	var perr *oidcauth.ProviderError
	require.ErrorAs(t, err, &perr)
	require.NotContains(t, perr.Body, secret)
	require.NotContains(t, perr.Body, encoded)
	require.Contains(t, perr.Body, "[redacted]")
}

func TestNewFailsWhenTheIssuerDoesNotMatch(t *testing.T) {
	i := newIDP(t)
	i.issuerOverride = "https://evil.example.invalid/sso"
	_, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: i.srv.URL, ClientID: "famifo", ClientSecret: "s",
		RedirectURI: "https://famifo.example.invalid/auth/callback",
	})
	require.ErrorContains(t, err, "did not match the issuer URL returned by provider")
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

// TestLogoutURLReportsUnsupportedWithoutAnEndSessionEndpoint はdiscoveryに
// end_session_endpointが無いIdP（実機ではSynology SSO Server）で、
// LogoutURLがfalseを返すことを固定する。文字列を空にするだけでは、
// 呼び出し側が「対応していない」のか「たまたま空文字が来た」のか区別できない。
func TestLogoutURLReportsUnsupportedWithoutAnEndSessionEndpoint(t *testing.T) {
	i := newIDP(t)
	c := newClient(t, i)

	u, ok := c.LogoutURL("https://famifo.example.invalid/signed-out")
	require.False(t, ok)
	require.Empty(t, u)
}

// TestLogoutURLBuildsTheExpectedURL はdiscoveryにend_session_endpointが
// あるとき、post_logout_redirect_uriとclient_idを添えたURLを組み立てることを
// 固定する。id_token_hintは載せない（famifoは検証後の生IDトークンを保持
// しないため。詳細はspec参照）。
func TestLogoutURLBuildsTheExpectedURL(t *testing.T) {
	i := newIDP(t)
	i.endSession = i.srv.URL + "/end-session"
	c := newClient(t, i)

	u, ok := c.LogoutURL("https://famifo.example.invalid/signed-out")
	require.True(t, ok)

	parsed, err := url.Parse(u)
	require.NoError(t, err)
	require.Equal(t, i.srv.URL+"/end-session", parsed.Scheme+"://"+parsed.Host+parsed.Path)
	q := parsed.Query()
	require.Equal(t, "https://famifo.example.invalid/signed-out", q.Get("post_logout_redirect_uri"))
	require.Equal(t, "famifo", q.Get("client_id"))
}

func hmacSHA256(t *testing.T, key []byte, msg string) string {
	t.Helper()
	// テスト内でだけ使う。実装側はHMACを一切使わない。
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
