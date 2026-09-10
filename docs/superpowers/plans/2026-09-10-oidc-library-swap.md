# ID トークン検証を go-oidc に移す 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `internal/oidcauth` の手書きの OIDC 実装を `go-oidc` + `x/oauth2` に置き換える。公開している型と関数の形は変えないので、`internal/web` から先は触らない。

**Architecture:** `oidc.NewProvider` が discovery と issuer の一致確認を、`IDTokenVerifier` が署名・`iss`・`aud`・`exp` を、`x/oauth2` が PKCE とコード交換を担う。famifo に残るのは `nonce` の照合と claim の取り出しだけになる。

**Tech Stack:** Go 1.27、`github.com/coreos/go-oidc/v3`、`golang.org/x/oauth2`

**Spec:** `docs/superpowers/specs/2026-09-10-oidc-auth-design.md`

## 経緯

`docs/superpowers/plans/2026-09-10-oidc-auth.md` で実装したとき、依存を増やさない方針から
標準ライブラリだけで書いた。レビューで、issuer すり替えの防御を検証しているはずのテストが
別の理由（discovery の 404）で通っていたことが分かった。防御のコードは正しかったが、
一度も実行されていなかった。この種の取りこぼしを構造的に減らすため、検証をライブラリに移す。

Cookie の署名（`internal/session`）は自前のまま残す。用途ごとに鍵を導出する形になっており、
`gorilla/securecookie` が Cookie 名を MAC に含めて得ているのと同じ性質を既に持っているためである。

## Global Constraints

- **公開している型と関数の形を変えない。** `Config`、`Identity`、`Params`、`New`、`NewParams`、`AuthURL`、`Exchange` の名前と引数と戻り値はそのまま。`internal/web` は1行も変えない
- **`alg` は RS256 に固定する。** discovery が広告する集合に委ねず、`oidc.Config.SupportedSigningAlgs` で明示する
- **`nonce` は呼び出し側で照合する。** `IDTokenVerifier.Verify` は nonce を見ないと明記されている
- 要求するスコープは `openid email groups`
- `username` が空なら `sub` で代用する
- 新しい依存は `github.com/coreos/go-oidc/v3` と `golang.org/x/oauth2` の2つだけ
- テストは外部テストパッケージ（`package oidcauth_test`）から公開 API だけで書く
- Go のコメントは日本語
- コミットメッセージは英語の要約1行のみ。本文もトレーラーも書かない

---

### Task 1: `internal/oidcauth` を go-oidc + x/oauth2 に置き換える

**Files:**
- Modify: `internal/oidcauth/oidcauth.go`（全面書き換え）
- Modify: `internal/oidcauth/oidcauth_test.go`（偽 IdP は残し、期待するエラー文言などを合わせる）
- Modify: `go.mod`、`go.sum`

**Interfaces:**
- Consumes: なし
- Produces: 変更なし。`internal/web` の `Provider` インターフェースを引き続き満たす

- [ ] **Step 1: 依存を取る**

Run:
```bash
go get github.com/coreos/go-oidc/v3@v3.21.0 golang.org/x/oauth2@v0.37.0
```
Expected: `go.mod` に2行増える（`github.com/go-jose/go-jose/v4` が indirect で入る）

- [ ] **Step 2: 実装を差し替える**

`internal/oidcauth/oidcauth.go` を次の内容にする。既存の `randomString` は `state` と
`nonce` のために残す。PKCE の verifier は `oauth2.GenerateVerifier()` に任せる。

```go
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
	return &Client{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURI,
			Endpoint:     provider.Endpoint(),
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
```

`randomString` は既存のものをそのまま残す。使わなくなった `challenge`、`verifyIDToken`、
`publicKey`、`getJSON`、`audienceHas`、`decodeSegment`、`discovery` 型は削除する。

- [ ] **Step 3: 偽 IdP がクライアント認証の両方式を受けるようにする**

`x/oauth2` の既定は `AuthStyleAutoDetect` で、まず HTTP Basic を試す。いまの偽 IdP は
`r.Form.Get("client_id")` だけを見て `aud` に echo しているので、Basic で来ると空になり、
`aud` 不一致で落ちる。**これは実装の問題ではなくテストの前提の問題である。**

`/token` ハンドラで、フォームと Basic の両方から client_id を読むようにする。

```go
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		// x/oauth2 は既定でまず HTTP Basic を試す。Synology SSO Server は
		// client_secret_basic と client_secret_post の両方を広告しているので、
		// 偽物も両方受ける。
		id := r.Form.Get("client_id")
		if u, _, ok := r.BasicAuth(); ok && u != "" {
			id = u
		}
		...
		"id_token": i.idToken(t, id),
	})
```

- [ ] **Step 4: テストの期待値を合わせる**

テストの構造と狙いは変えない。変えるのは、エラーの出どころが変わったことによる文言だけ。

- `TestNewFailsWhenTheIssuerDoesNotMatch` — `require.ErrorContains` の文字列を、
  `go-oidc` が返す文言に合わせる。`oidc: issuer did not match` を含む。
  **文字列を消して `require.Error` だけにしない。** それをやると、この防御が
  また別の理由で通るテストに戻る
- `TestExchangeRejectsAlgNone` と `TestExchangeRejectsHMACSignedWithThePublicKey` —
  `SupportedSigningAlgs` を RS256 に固定してあるので拒否される。エラー文言の主張は
  緩めてよいが、テストは残す。依存を差し替えたときに気づけるのはこの2本だけである
- `TestExchangeRejectsNonceMismatch` — 文言はこちらの実装のままなので変わらない
- `TestAuthURLCarriesTheFlowParameters` — `oauth2` が組み立てる URL でも
  `client_id`、`response_type=code`、`scope`、`state`、`nonce`、
  `code_challenge_method=S256`、`code_challenge` が載ることを引き続き確かめる。
  `code_challenge` が verifier そのものでないことの主張も残す
- `TestNewParamsAreUnpredictable` — verifier は `oauth2.GenerateVerifier()` が作る。
  長さの下限（43文字）の主張はそのまま通るはず

- [ ] **Step 5: テストを走らせる**

Run: `go test ./internal/oidcauth/... -v`
Expected: すべて PASS。落ちたテストがあれば、期待値が古いのか実装が違うのかを切り分けて報告する

- [ ] **Step 6: 全体を確かめる**

Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`
Expected: `gofmt -l` は `internal/index/videometa/videometa_test.go`（このブランチ以前からのもの）だけ。それ以外は成功

- [ ] **Step 7: ブラウザの一往復も通す**

`internal/web/oidc_browser_test.go` は実 `oidcauth.Client` を実 `web.Server` に通すので、
差し替えが本当に噛み合っているかはここで分かる。**このテストは1行も変えないこと。**
変えなければならなくなったら、それは公開 API が変わってしまった証拠なので報告する。

Run: `make browser-test`（`Makefile` に無ければ `go test -tags browser ./internal/web/ -run OIDC -v`）
Expected: `TestOIDCRoundTripReachesTheGallery` が PASS

- [ ] **Step 8: コミット**

```bash
git add go.mod go.sum internal/oidcauth
git commit -m "refactor: Verify ID tokens with go-oidc instead of by hand"
```

---

### Task 2: 前の計画の制約を現状に合わせる

`docs/superpowers/plans/2026-09-10-oidc-auth.md` の Global Constraints は「新しい依存を
入れない。`go.mod` は変更しない」と書いており、いまは事実でない。実行済みの計画なので
中身は履歴として残すが、読んだ人が現在の制約と取り違えないようにする。

**Files:**
- Modify: `docs/superpowers/plans/2026-09-10-oidc-auth.md`

- [ ] **Step 1: 冒頭に注記を足す**

`**Spec:**` の行の直後に置く。

```markdown
> **注記（2026-09-10）:** この計画は実行済みだが、Global Constraints の「新しい依存を
> 入れない」はもう有効ではない。ID トークンの検証は `go-oidc` + `x/oauth2` に移した。
> 経緯と現在の設計は `docs/superpowers/plans/2026-09-10-oidc-library-swap.md` と spec を見よ。
```

- [ ] **Step 2: コミット**

```bash
git add docs/superpowers/plans/2026-09-10-oidc-auth.md
git commit -m "docs: Note that the no-dependency constraint no longer holds"
```
