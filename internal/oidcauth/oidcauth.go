// Package oidcauth は OpenID Connect の認可コードフローを扱う。
//
// 標準ライブラリだけで書く。手書きのJWT検証は落とし穴が多いので、安全側に倒す
// 制約を実装に埋め込んである。
//
//   - 署名アルゴリズムはRS256に固定する。ヘッダのalgを見て検証方法を選ばない。
//     algを信じると、署名なし（alg=none）や、公開鍵をHMACの鍵に使わせる取り違えを
//     受け入れてしまう。
//   - JWKSはログインのたびに取得する。鍵の入れ替えに自動で追随でき、キャッシュの
//     無効化を書かずに済む。セッションは30日なのでログインは稀である。
//   - iss、aud、exp、nonce をすべて確かめる。1つでも欠けたら失敗させる。
package oidcauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// scopes は要求する権限。profile はSynology SSO Serverに無いので要求しない。
// email と groups は現時点では使わないが、username claim がどのスコープに紐づくかが
// 文書化されていないため、提供される3つをすべて要求する。
const scopes = "openid email groups"

// httpTimeout はIdPへの1回の往復に許す時間。
const httpTimeout = 20 * time.Second

// Config はクライアントの設定。すべて起動時に決まる。
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

// Identity は認証できた利用者。
type Identity struct {
	Subject  string   // sub。IdPによっては不透明でなくユーザー名そのものである
	Username string   // 表示とログに使う。空ならSubjectで代用する
	Email    string   // 設定していないアカウントでは空になる
	Groups   []string // 本案では使わない
}

// Params は認可の往復のあいだ保持する値。callbackまで持ち越す。
type Params struct {
	State    string
	Nonce    string
	Verifier string // PKCEのcode_verifier
}

// Client はIdPと話す。New が discovery を引いた時点で不変になる。
type Client struct {
	cfg      Config
	authURL  string
	tokenURL string
	jwksURL  string
	http     *http.Client
}

type discovery struct {
	Issuer   string `json:"issuer"`
	AuthURL  string `json:"authorization_endpoint"`
	TokenURL string `json:"token_endpoint"`
	JWKSURL  string `json:"jwks_uri"`
}

// New は discovery を引いてClientを組み立てる。
// 起動時に1回だけ呼ぶ。IdPに届かなければエラーを返し、呼び出し側は起動を止める。
func New(ctx context.Context, cfg Config) (*Client, error) {
	c := &Client{cfg: cfg, http: &http.Client{Timeout: httpTimeout}}
	var d discovery
	if err := c.getJSON(ctx, strings.TrimSuffix(cfg.Issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
		return nil, fmt.Errorf("cannot read the OIDC discovery document: %w", err)
	}
	// 取りに行った先が名乗るissuerと、設定したissuerが一致しない場合は信用しない。
	if d.Issuer != cfg.Issuer {
		return nil, fmt.Errorf("the issuer does not match: %q was configured, %q was advertised", cfg.Issuer, d.Issuer)
	}
	if d.AuthURL == "" || d.TokenURL == "" || d.JWKSURL == "" {
		return nil, fmt.Errorf("the discovery document is missing an endpoint")
	}
	c.authURL, c.tokenURL, c.jwksURL = d.AuthURL, d.TokenURL, d.JWKSURL
	return c, nil
}

// NewParams は往復に使う値を作る。
func NewParams() (Params, error) {
	var p Params
	for _, dst := range []*string{&p.State, &p.Nonce, &p.Verifier} {
		v, err := randomString()
		if err != nil {
			return Params{}, err
		}
		*dst = v
	}
	return p, nil
}

// AuthURL はIdPの認可エンドポイントへ送るURLを組み立てる。
func (c *Client) AuthURL(p Params) string {
	q := url.Values{
		"client_id":             {c.cfg.ClientID},
		"response_type":         {"code"},
		"scope":                 {scopes},
		"redirect_uri":          {c.cfg.RedirectURI},
		"state":                 {p.State},
		"nonce":                 {p.Nonce},
		"code_challenge":        {challenge(p.Verifier)},
		"code_challenge_method": {"S256"},
	}
	return c.authURL + "?" + q.Encode()
}

// Exchange は認可コードをトークンに交換し、IDトークンを検証して利用者を返す。
func (c *Client) Exchange(ctx context.Context, code string, p Params) (Identity, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURI},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"code_verifier": {p.Verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("cannot reach the token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Identity{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("the token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return Identity{}, fmt.Errorf("the token response is not JSON: %w", err)
	}
	if tok.IDToken == "" {
		return Identity{}, fmt.Errorf("the token response carries no id_token")
	}
	return c.verify(ctx, tok.IDToken, p.Nonce)
}

// verify はIDトークンの署名とclaimを確かめる。
func (c *Client) verify(ctx context.Context, raw, nonce string) (Identity, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Identity{}, fmt.Errorf("the id_token is not a JWT")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return Identity{}, err
	}
	// algを見て分岐しない。RS256でなければここで捨てる。
	if hdr.Alg != "RS256" {
		return Identity{}, fmt.Errorf("the id_token is signed with %q, only RS256 is accepted", hdr.Alg)
	}
	pub, err := c.publicKey(ctx, hdr.Kid)
	if err != nil {
		return Identity{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Identity{}, fmt.Errorf("the id_token signature is not base64url: %w", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return Identity{}, fmt.Errorf("the id_token signature does not verify: %w", err)
	}

	var claims struct {
		Iss      string   `json:"iss"`
		Aud      any      `json:"aud"`
		Exp      int64    `json:"exp"`
		Nonce    string   `json:"nonce"`
		Sub      string   `json:"sub"`
		Username string   `json:"username"`
		Email    string   `json:"email"`
		Groups   []string `json:"groups"`
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return Identity{}, err
	}
	if claims.Iss != c.cfg.Issuer {
		return Identity{}, fmt.Errorf("the id_token issuer is %q, want %q", claims.Iss, c.cfg.Issuer)
	}
	if !audienceHas(claims.Aud, c.cfg.ClientID) {
		return Identity{}, fmt.Errorf("the id_token is not addressed to this client")
	}
	if claims.Exp == 0 || !time.Now().Before(time.Unix(claims.Exp, 0)) {
		return Identity{}, fmt.Errorf("the id_token has expired")
	}
	if claims.Nonce != nonce {
		return Identity{}, fmt.Errorf("the id_token nonce does not match the one we sent")
	}
	if claims.Sub == "" {
		return Identity{}, fmt.Errorf("the id_token carries no subject")
	}
	name := claims.Username
	if name == "" {
		// username は標準のclaimではない。別のIdPでは無いことがある。
		name = claims.Sub
	}
	return Identity{Subject: claims.Sub, Username: name, Email: claims.Email, Groups: claims.Groups}, nil
}

// publicKey はJWKSを取って、kidに合うRSA公開鍵を返す。
// キャッシュしない。鍵の入れ替えに追随するためと、無効化を書かずに済ませるため。
func (c *Client) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := c.getJSON(ctx, c.jwksURL, &jwks); err != nil {
		return nil, fmt.Errorf("cannot read the JWKS: %w", err)
	}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || (kid != "" && k.Kid != kid) {
			continue
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("the JWKS modulus is not base64url: %w", err)
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("the JWKS exponent is not base64url: %w", err)
		}
		if len(eb) > 8 {
			return nil, fmt.Errorf("the JWKS exponent is too large")
		}
		buf := make([]byte, 8)
		copy(buf[8-len(eb):], eb)
		return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(binary.BigEndian.Uint64(buf))}, nil
	}
	return nil, fmt.Errorf("the JWKS has no RSA key for kid %q", kid)
}

func (c *Client) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", u, resp.StatusCode)
	}
	return json.Unmarshal(b, v)
}

// audienceHas は aud が文字列でも配列でも扱えるようにする。
func audienceHas(aud any, want string) bool {
	switch a := aud.(type) {
	case string:
		return a == want
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return fmt.Errorf("the id_token segment is not base64url: %w", err)
	}
	return json.Unmarshal(b, v)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomString() (string, error) {
	b := make([]byte, 32) // base64urlで43文字。PKCEの下限を満たす。
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cannot generate a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
