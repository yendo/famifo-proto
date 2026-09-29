// Package oidcauth は OpenID Connect の認可コードフローを扱う。
//
// discovery と ID トークンの検証は go-oidc に、PKCE とコード交換は x/oauth2 に任せる。
// famifo に残るのは nonce の照合と subject の取り出しだけである。
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

// maxProviderErrorField はProviderErrorの各項目に残す上限。相手が暴れてもログが
// 埋まらないように切り詰める。
const maxProviderErrorField = 1024

// ErrProviderRefused はIdPがトークン交換そのものには応答したが、その交換を
// 拒んだことを表す番兵。呼び出し側は errors.Is で「IdPに届かない」場合と
// 区別できる。
var ErrProviderRefused = errors.New("the identity provider refused the exchange")

// ProviderError はErrProviderRefusedを満たす。IdPの応答を保持しており、呼び出し側は
// ログに残せる。CodeとDescriptionはRFC 6749 5.2が定めるerrorとerror_descriptionで、
// x/oauth2 がパースしたものをそのまま持つ。応答がこの形でなければ（IdPの手前にいる
// プロキシがHTMLのエラーページを返した場合など）両方とも空で、StatusCodeだけが残る。
// それでも「IdPに届かない」との区別は付く。各項目は1KiBに切り詰めてある。
type ProviderError struct {
	StatusCode  int
	Code        string
	Description string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("the identity provider refused the exchange: status %d, error %q, description %q",
		e.StatusCode, e.Code, e.Description)
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
//
// 呼び出し側が実際に使う値だけを持つ。
type Identity struct {
	// Subject はIdPにおける利用者の識別子（IDトークンの sub）。仕様が一意性と
	// 不変性を保証するのはこれだけなので、famifoが利用者を指すときに使う。
	// 人が読む前提の値ではなく、UUIDであることも多い。
	Subject string
	// IDToken は検証済みのIDトークンそのもの。RP-Initiated Logout の
	// id_token_hint に渡すために保持する。仕様は、これを付けずに
	// post_logout_redirect_uri だけを送った場合、IdPは戻り先へ
	// リダイレクトしてはならないと定めている。
	IDToken string
}

// Params は認可の往復のあいだ保持する値。callbackまで持ち越す。
type Params struct {
	State    string
	Nonce    string
	Verifier string // PKCEのcode_verifier
}

// Client はIdPと話す。New が discovery を引いた時点で不変になる。
type Client struct {
	oauth              *oauth2.Config
	verifier           *oidc.IDTokenVerifier
	endSessionEndpoint string // RP-Initiated Logout の宛先。discoveryに無ければ空
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
	// end_session_endpoint は RP-Initiated Logout の仕様が足すメタデータで、
	// go-oidcが型付きフィールドで公開しているのはCoreの分だけである。生の
	// メタデータから自分で拾う。
	var metadata struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return nil, fmt.Errorf("cannot read the OIDC discovery document: %w", err)
	}
	return &Client{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURI,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID},
		},
		verifier:           provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		endSessionEndpoint: metadata.EndSessionEndpoint,
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
				&ProviderError{
					StatusCode:  status,
					Code:        clampProviderErrorField(rErr.ErrorCode),
					Description: clampProviderErrorField(rErr.ErrorDescription),
				})
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
	return Identity{Subject: idToken.Subject, IDToken: raw}, nil
}

// LogoutURL はRP-Initiated Logoutの宛先を組み立てる。IdPが discovery で
// end_session_endpoint を広告していなければ false を返す。呼び出し側は
// これで「この IdP は対応している／していない」を文字列を見ずに判別できる。
//
// idTokenHint はサインインしたときに受け取ったIDトークンそのもの。仕様上の
// 位置づけはRECOMMENDEDだが、これを伴わずに post_logout_redirect_uri を
// 送った場合、IdPは戻り先へリダイレクトしてはならないと定められている。
// つまり省略すると、サインアウトはできても/signed-outに帰ってこない。
//
// 期限切れでも構わない。仕様は、aud が指すRPにセッションがある（あった）
// 限り、expを過ぎたIDトークンも受け入れるべきだとしている。famifoの
// セッションは30日あり、IDトークンはとうに切れているのが普通である。
//
// 空なら載せない。この項目を保存する前に発行された古いセッションが該当する。
func (c *Client) LogoutURL(postLogoutRedirectURI, idTokenHint string) (string, bool) {
	if c.endSessionEndpoint == "" {
		return "", false
	}
	u, err := url.Parse(c.endSessionEndpoint)
	if err != nil {
		return "", false
	}
	q := u.Query()
	q.Set("post_logout_redirect_uri", postLogoutRedirectURI)
	q.Set("client_id", c.oauth.ClientID)
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	u.RawQuery = q.Encode()
	return u.String(), true
}

// clampProviderErrorField はIdPが返した文字列をログに載せる長さに収める。
//
// error_description はIdPが書く自由文で、長さに上限がない。切り詰めるとマルチバイト
// 文字の途中で切ることがあり、string自体は不正なUTF-8でも壊れないが、slogが出力時に
// U+FFFDへ置き換えるため見た目が崩れる。strings.ToValidUTF8で有効な境界まで戻す。
func clampProviderErrorField(s string) string {
	if len(s) > maxProviderErrorField {
		s = strings.ToValidUTF8(s[:maxProviderErrorField], "")
	}
	return s
}

// randomString は state と nonce に使う推測できない値を作る。crypto/rand の
// 32バイトを base64url にした43文字を返す。
func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cannot generate a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
