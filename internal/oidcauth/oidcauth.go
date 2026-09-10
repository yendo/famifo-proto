// Package oidcauth は OpenID Connect の認可コードフローを扱う。
//
// discovery と ID トークンの検証は go-oidc に、PKCE とコード交換は x/oauth2 に任せる。
// famifo に残るのは nonce の照合と claim の取り出しだけである。
//
// 当初は標準ライブラリだけで書いていた。動いてはいたが、issuer すり替えの防御を
// 検証しているはずのテストが別の理由で通っていたことがレビューで分かり、
// 失敗経路の正しさは実績のある実装に任せるほうが安いと判断した。
package oidcauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

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
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// New は discovery を引いてClientを組み立てる。
//
// 起動時に1回だけ呼ぶ。IdPに届かなければエラーを返し、呼び出し側は起動を止める。
// oidc.NewProvider は discovery が名乗る issuer と設定した issuer の一致も確かめるので、
// その防御をこちらで書く必要はない。
func New(ctx context.Context, cfg Config) (*Client, error) {
	// 既定のクライアントは待ち時間の上限を持たない。落ちたIdPに繋ぎに行ったまま
	// 起動が止まらないよう、明示する。
	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: httpTimeout})
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("cannot read the OIDC discovery document: %w", err)
	}
	endpoint := provider.Endpoint()
	// x/oauth2 の既定 AuthStyleAutoDetect はまず HTTP Basic を試す。それ自体は
	// client_secret_basic を広告する IdP には通るが、成否を HTTP ステータスでしか
	// 判定しないため、client_id をフォームでしか読まない IdP に対しては 200 が
	// 返って自動検出が成功したと誤解し、aud が空の id_token を検証で落とすまで
	// 気づけない。Synology SSO Server は client_secret_post も広告しているので、
	// ここで明示して揺れを無くす。
	endpoint.AuthStyle = oauth2.AuthStyleInParams
	return &Client{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURI,
			Endpoint:     endpoint,
			// profile は Synology SSO Server に無いので要求しない。email と groups は
			// 今は使わないが、username claim がどのスコープに紐づくかが文書化されて
			// いないため、提供される3つをすべて要求する。
			Scopes: []string{oidc.ScopeOpenID, "email", "groups"},
		},
		// 受け入れる署名アルゴリズムを discovery が広告する集合に委ねない。IdPが将来
		// 弱いものを広告し始めても、こちらが受け入れる範囲は変わらないようにする。
		verifier: provider.Verifier(&oidc.Config{
			ClientID:             cfg.ClientID,
			SupportedSigningAlgs: []string{oidc.RS256},
		}),
	}, nil
}

// NewParams は往復に使う値を作る。
func NewParams() (Params, error) {
	state, err := randomString()
	if err != nil {
		return Params{}, err
	}
	nonce, err := randomString()
	if err != nil {
		return Params{}, err
	}
	return Params{State: state, Nonce: nonce, Verifier: oauth2.GenerateVerifier()}, nil
}

// AuthURL はIdPの認可エンドポイントへ送るURLを組み立てる。
func (c *Client) AuthURL(p Params) string {
	return c.oauth.AuthCodeURL(p.State, oidc.Nonce(p.Nonce), oauth2.S256ChallengeOption(p.Verifier))
}

// Exchange は認可コードをトークンに交換し、IDトークンを検証して利用者を返す。
func (c *Client) Exchange(ctx context.Context, code string, p Params) (Identity, error) {
	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: httpTimeout})
	tok, err := c.oauth.Exchange(ctx, code, oauth2.VerifierOption(p.Verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("cannot exchange the authorization code: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, fmt.Errorf("the token response carries no id_token")
	}
	idToken, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("the id_token does not verify: %w", err)
	}
	// Verify は nonce を見ない。仕様上ここは呼び出し側の責任である。忘れると
	// 認可コードを横取りした攻撃者の再生を防げなくなる。
	if idToken.Nonce != p.Nonce {
		return Identity{}, fmt.Errorf("the id_token nonce does not match the one we sent")
	}
	var claims struct {
		Username string   `json:"username"`
		Email    string   `json:"email"`
		Groups   []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("cannot read the id_token claims: %w", err)
	}
	name := claims.Username
	if name == "" {
		// username は標準のclaimではない。別のIdPでは無いことがある。
		name = idToken.Subject
	}
	return Identity{
		Subject: idToken.Subject, Username: name, Email: claims.Email, Groups: claims.Groups,
	}, nil
}

func randomString() (string, error) {
	b := make([]byte, 32) // base64urlで43文字。PKCEの下限を満たす。
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cannot generate a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
