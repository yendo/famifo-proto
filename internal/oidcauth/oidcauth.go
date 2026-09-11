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
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// httpTimeout はIdPへの1回の往復に許す時間。
const httpTimeout = 20 * time.Second

// maxProviderErrorBody はProviderError.Bodyに残す上限。相手が暴れてもログが
// 埋まらないように切り詰める。
const maxProviderErrorBody = 1024

// ErrProviderRefused はIdPがトークン交換そのものには応答したが、その交換を
// 拒んだことを表す番兵。呼び出し側は errors.Is で「IdPに届かない」場合と
// 区別できる。
var ErrProviderRefused = errors.New("the identity provider refused the exchange")

// ProviderError はErrProviderRefusedを満たす。IdPの応答（HTTPステータスと本文）を
// 保持しており、呼び出し側はログに残せる。本文は1KiBに切り詰めてある。
type ProviderError struct {
	StatusCode int
	Body       string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("the identity provider refused the exchange: status %d, body %q", e.StatusCode, e.Body)
}

// Is はErrProviderRefusedとの errors.Is 判定を成立させる。
func (e *ProviderError) Is(target error) bool {
	return target == ErrProviderRefused
}

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
	// Synology SSO Server は client_secret_basic と client_secret_post の両方を
	// 広告しているが、実機で認可コードフローを1往復させて確かめたのは
	// client_secret_post のほうだけである。x/oauth2 の既定 AuthStyleAutoDetect は
	// まず HTTP Basic を試すので、放っておくと本番の最初のログインが一度も
	// 検証していない経路を通ることになる。検証済みの方式に固定する。
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
		// x/oauth2 はトークンエンドポイントが応答したうえで拒んだ場合、
		// *oauth2.RetrieveError を返す。IdPに届いていないのか、届いたうえで
		// 拒まれたのかは呼び出し側が知りたいことが違う（「まだ試して良いか」
		// 「何が悪かったのか」）ので、ここで区別できる形にして返す。
		var rErr *oauth2.RetrieveError
		if errors.As(err, &rErr) {
			status := 0
			if rErr.Response != nil {
				status = rErr.Response.StatusCode
			}
			return Identity{}, fmt.Errorf("cannot exchange the authorization code: %w",
				&ProviderError{StatusCode: status, Body: c.sanitizeProviderBody(rErr.Body)})
		}
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
	// go-oidc は sub が空でも拒否しない。空のままセッションを張ると、
	// 誰のものでもない利用者としてログインを許してしまうので、ここで弾く。
	if idToken.Subject == "" {
		return Identity{}, fmt.Errorf("the id_token carries no subject")
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

// sanitizeProviderBody はProviderErrorに載せる前にIdPの応答本文を安全にする。
//
// client_secret_post を固定しているため（New内のコメントを見よ）、client secret は
// すべての交換リクエストのPOSTボディに載っている。リクエストをそのまま読み返す
// ような（珍しくない）IdPのエラー応答は、secretをそっくり含みうる。先に置換して
// から切り詰める。順序を逆にすると、切り詰めの境目でsecretが分断され、
// ReplaceAllが後半だけになった破片を見つけられずログに残ってしまう。
//
// x/oauth2 はPOSTボディを application/x-www-form-urlencoded で組み立てるので、
// secretは url.Values.Encode() と同じ規則でパーセントエンコードされた形でも
// リクエストに載っている。secretがbase64由来だと "+" や "/" や "=" を含むのが
// 普通で、リテラルの置換だけではこの形を取りこぼす。url.QueryEscapeは
// url.Values.Encode() と同じエンコードを作るので、両方の形を置換する。
// エンコードしても変わらない（16進数などの）secretで同じ置換を二度走らせない
// よう、一致するときはスキップする。
//
// 置換のあとに切り詰めるので、その時点でマルチバイト文字の途中を切ることがある。
// string()は不正なUTF-8でも失敗しないが、slogはそれを出力時にU+FFFDへ置き換える
// ため見た目が壊れる。strings.ToValidUTF8で有効な境界まで戻す。
func (c *Client) sanitizeProviderBody(body []byte) string {
	s := string(body)
	if secret := c.oauth.ClientSecret; secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
		if encoded := url.QueryEscape(secret); encoded != secret {
			s = strings.ReplaceAll(s, encoded, "[redacted]")
		}
	}
	if len(s) > maxProviderErrorBody {
		s = strings.ToValidUTF8(s[:maxProviderErrorBody], "")
	}
	return s
}

func randomString() (string, error) {
	// state と nonce に使う。PKCEのverifierはoauth2.GenerateVerifier()が作る。
	b := make([]byte, 32) // base64urlで43文字。
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cannot generate a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
