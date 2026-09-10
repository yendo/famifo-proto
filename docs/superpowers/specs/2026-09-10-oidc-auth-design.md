# OIDC による認証

`2026-09-10-dsm-auth-design.md` と同じ問題を、別の方式で解く案である。あちらは
DSM Web API に資格情報を問い合わせる。こちらは OpenID Connect で認証そのものを
Identity Provider に委ねる。どちらを採るかはまだ決めていない。両方を残して比べる。

## 解決する問題

前案と同じ。famifo は認証を持たず、URL を知っている人は家族の写真を全部見られる。
`docs/design.md` の「LAN内の信頼されたネットワーク前提」は、認証を書かないことの
言い換えだった。famifo 独自のパスワードを作るのは、家族が覚えるものを増やすので採らない。

## 前案との違い

前案では、famifo がログイン画面を出し、入力されたパスワードを受け取って DSM に転送する。
本案では famifo はログイン画面もパスワードも持たない。利用者を IdP へ送り、IdP が
認証した結果だけを受け取る。

これにより前案から**消える**もの。

- ログイン画面のテンプレートとフォーム処理
- パスワードと OTP の受け渡し、`ErrOTPRequired` などのエラー翻訳
- DSM のエラーコード表とその実機確認
- `SYNO.API.Auth` のクライアントと、後始末の logout 呼び出し
- DSM の自動ブロックがコンテナ IP を遮断するリスクと、その復旧手順
- 平文のパスワードが famifo のプロセスを通ること

**増える**もの。

- OIDC クライアント（discovery、認可へのリダイレクト、state/nonce/PKCE、callback、
  コード交換、ID トークンの検証）。ただし `github.com/coreos/go-oidc/v3` と
  `golang.org/x/oauth2` に任せられる
- 依存が 2 つ増える
- IdP 側にクライアントを登録する運用
- famifo が自分の外部 URL を知る必要（`redirect_uri` は絶対 URL）
- コンテナに CA 証明書が要る（後述）
- LAN 内での名前解決の上書き（後述）

famifo が書くコードは前案より減り、複雑さはコードから設定と運用へ移る。

## 実機で確認したこと

2026-09-10 に DSM（DS918、`192.168.1.2`）で Synology SSO Server を導入し、OIDC を
有効にして discovery を取得した。結論として **`go-oidc` にそのまま任せられる**。

```
issuer                          https://shirotae.synology.me/webman/sso
authorization_endpoint          .../webman/sso/SSOOauth.cgi
token_endpoint                  .../webman/sso/SSOAccessToken.cgi
userinfo_endpoint               .../webman/sso/SSOUserInfo.cgi
jwks_uri                        .../webman/sso/openid-jwks.json
code_challenge_methods_supported  S256, plain
grant_types_supported           authorization_code, implicit
response_types_supported        code, code id_token, id_token, id_token token
id_token_signing_alg_values      RS256
token_endpoint_auth_methods      client_secret_basic, client_secret_post
scopes_supported                email, groups, openid
claims_supported                aud, email, exp, groups, iat, iss, sub, username
```

discovery は issuer 直下の `/.well-known/openid-configuration` にあり、仕様どおりの
配置である。`oidc.NewProvider(ctx, issuer)` が細工なしで動く。JWKS も取得でき、
RS256 の鍵が1本入っていた。

**標準から外れる点が2つある。**

1. ユーザー名の claim は **`username`** で、標準の `preferred_username` ではない
2. **`profile` スコープが無い**。要求できるのは `openid`、`email`、`groups`

**`groups` claim が最初から入る。** 本案では使わないが、グループで絞りたくなったときに
追加の API 呼び出しが要らないことは記録しておく。

**エンドポイントは 443 番でのみ提供される。** 5001 番の同じパスは `not enabled` を返す。
これが後述の到達性の条件を決める。

## 目標

- DSM のアカウントで famifo にログインできる。ログインは DSM の画面が行う
- 一度ログインしたら Cookie で入れる。famifo の再起動をまたいで有効
- 認証を有効にするかは起動時の設定。無効なら今までどおり誰でも見られる
- famifo はパスワードを一切扱わない
- IdP を Synology に固定しない。標準的な OIDC の IdP なら差し替えられる

## 目標としないこと

- **認可**。IdP が認証した人は全員 famifo を見られる
- **RP-initiated logout**。discovery に `end_session_endpoint` が無いので実現できない。
  famifo からログアウトしても DSM のセッションは残る
- **個別セッションの失効**。署名付き Cookie を選んだ帰結
- **famifo 自身の TLS 終端**。HTTPS は DSM のリバースプロキシに任せる
- **`internal/synology` の改名**。前案では `dsmauth` を兄弟に並べるために
  `synology/eadir` へ移す計画だったが、本案に `dsmauth` は無い。単独のパッケージを
  置き場だけのディレクトリに入れる意味はないので、`internal/synology` はそのまま残す

## 設計

### パッケージ境界

- `internal/oidcauth`（新規）— `go-oidc` と `oauth2` を包み、famifo が必要とする2つの
  操作だけを公開する。ライブラリと同じ名前にしないのは、import したときにどちらの話を
  しているか読めるようにするため
- `internal/session`（新規）— 署名付き Cookie の組み立てと検証。OIDC も HTTP も知らない
- `internal/web/auth.go`（新規）— ハンドラと認証ミドルウェア

`internal/synology` は触らない。

### OIDC クライアント

```go
// Identity は認証できた利用者。
type Identity struct {
	Subject  string   // sub。安定した識別子。鍵にするならこれ
	Username string   // username claim。表示とログに使う
	Email    string   // 空のことがある
	Groups   []string // 本案では使わない。記録のみ
}

// AuthURL は IdP の認可エンドポイントへ送る URL を組み立てる。
func (c *Client) AuthURL(state, nonce, pkceVerifier string) string

// Exchange は callback で受けた code をトークンに交換し、ID トークンを検証して
// 利用者を返す。nonce の一致もここで確かめる。
func (c *Client) Exchange(ctx context.Context, code, pkceVerifier, nonce string) (Identity, error)
```

`New` は `oidc.NewProvider` で discovery を引く。起動時に1回だけ行い、IdP に届かなければ
起動を止める。認証すると宣言しておいて黙って無認証で配信するより、起動しないほうがよい。

要求するスコープは `openid email groups`。`profile` は SSO Server に無いので要求しない。
`email` と `groups` は本案では使わないが、`username` claim がどのスコープに紐づくかは
文書化されていない。提供される3つをすべて要求しておき、実機で通したあとに減らせるかを
確かめる。減らせるなら `openid` だけにする。
ID トークンの検証は `go-oidc` に任せる（署名、`iss`、`aud`、`exp`、`iat`）。`nonce` の
一致だけは呼び出し側で確かめる。

`username` は標準の claim ではないので、`IDToken.Claims` に独自の構造体を渡して取り出す。
IdP を差し替えたときにここが空になる可能性があるため、**空なら `sub` で代用する**。

### 認可コードフローの往復

リダイレクトから callback までの間、`state`、`nonce`、PKCE の verifier、戻り先の
`next` を保持する必要がある。サーバー側に状態を持たず、**短命の署名付き Cookie**に
入れる。セッションと同じ `session.Codec` で署名し、有効期限は 10 分。callback で
使い切ったら消す。

PKCE の verifier を Cookie に置いても差し支えない。PKCE が防ぐのは第三者による
認可コードの横取りで、Cookie を読めるのは利用者本人だからである。`state` と `nonce` に
必要なのは改竄されないことであり、秘匿ではない。したがって署名だけでよく、暗号化は要らない。

`code_challenge_method` は **S256**。`plain` も提供されているが選ばない。

### セッション

`session.Codec` は署名鍵を持ち、任意の文字列に署名する。

```go
func (c *Codec) Sign(payload string, expiry time.Time) string
func (c *Codec) Verify(value string, now time.Time) (payload string, ok bool)
```

ログイン後のセッションは payload に利用者名を入れ、往復の一時状態は payload に
`state` / `nonce` / verifier / `next` をまとめた JSON を入れる。用途を Codec に
知らせないことで、Codec は鍵と時刻だけを相手にする小さな部品のままでいられる。

Cookie の値は次の形にする。

```
base64url("<payload>\n<失効時刻のUnix秒>") + "." + base64url(HMAC-SHA256(鍵, 前半))
```

検証は前半から署名を作り直し `hmac.Equal` で比べる。`==` で比べると、一致する接頭辞の
長さが実行時間に出る。

**署名鍵**はデータディレクトリの `session.key` に置く。無ければ起動時に `crypto/rand` で
32 バイトを作り、`0600` で書く。再起動をまたいでセッションが生き残るのはこのファイルの
おかげである。消して再起動すれば全端末が一斉にログアウトする。個別の失効ができない
構えなので、これが唯一の一括失効手段になる。

**属性**は `HttpOnly`、`SameSite=Lax`、`Path=/`、そして外部 URL が `https` のとき `Secure`。

**有効期限は 30 日固定**。スライディングにすると毎リクエストで `Set-Cookie` を出すか、
残り時間を見て再発行する分岐が要る。`defaultChunkSize` と同じく利用者が変える設定にはしない。

**ログアウト**は `POST /logout` で Cookie を消す。famifo のセッションだけが消え、
DSM のセッションは残る。同じブラウザで再度ログインすると、DSM の画面を経ずに戻ってくる。
これは RP-initiated logout が無いことの帰結である。

### 経路とミドルウェア

| 経路 | 役割 |
|---|---|
| `GET /login` | `state`/`nonce`/verifier を作り、一時 Cookie を置いて IdP へリダイレクト |
| `GET /auth/callback` | `state` を照合し、code を交換し、セッション Cookie を発行して `next` へ |
| `POST /logout` | セッション Cookie を消す |

未認証のときの応答は、要求されたものによって分ける。

| 経路 | 未認証のとき |
|---|---|
| `/`、`/item/{id}` | `/login?next=…` へリダイレクト |
| `/tiles`、`/thumb/{id}`、`/file/{id}` | 401 |
| `/login`、`/auth/callback`、`/static/` | 素通し |

`/tiles` は `fetch` で取りに行く。ここで HTML を返すと JSON の解釈が壊れ、画面には
何も出ないまま原因も読めない。401 を受けたクライアントはページを再読み込みし、
リダイレクトでログインに向かう。`/thumb` と `/file` は `<img>` と `<video>` が読むので、
リダイレクトしても意味がない。

`next` は先頭が `/` で、かつ `//` で始まらないものだけ受け付ける。`//evil.example` は
プロトコル相対 URL として別サイトへのリダイレクトになる。それ以外は `/` に落とす。

`/auth/callback` で `state` が合わない、Cookie が無い、期限切れのときは、エラーを
返して `/login` をやり直させる。ここを黙って通すと CSRF になる。

`web.NewServer` は認証をまとめた `*Auth` を受け取り、**nil なら認証しない**。
`-oidc-issuer` を渡したときだけ認証が有効になり、渡さなければ今までどおり動く。
開発機やテストで IdP 無しに動かせる必要があるためである。

```go
// Auth は認証の手段をまとめる。nil を渡すと認証しない。
type Auth struct {
	OIDC       *oidcauth.Client
	Session    *session.Codec
	ExternalURL string // redirect_uri の組み立てに使う
	Secure     bool    // Cookie に Secure を付けるか
}
```

ログアウトの導線は `gallery.html` の `header.topbar` に置く。`POST /logout` する
小さなボタンで、認証が無効なときは出さない。`galleryView` に真偽値を1つ足す。

### 到達性の条件

本案は前案に無い前提を2つ持ち込む。どちらも実機の確認で判明した。

**1. `shirotae.synology.me` を LAN 内で `192.168.1.2` に解決させる必要がある。**
この名前は公開 IP を指し、ポートを開けていないので LAN 内からは届かない。そして
OIDC のエンドポイントは 443 番でのみ提供される。ブラウザ（認可へのリダイレクト）も
famifo（discovery、JWKS、トークン交換）も、この名前でこのホストに届かなければならない。

ルーターの DNS に静的エントリを足すのが LAN 全体に効いて簡単である。famifo の
コンテナには `docker run --add-host shirotae.synology.me:192.168.1.2` を渡す。

**2. コンテナに CA 証明書が要る。** famifo の Dockerfile は `FROM scratch` で
バイナリしか入れていない（`Dockerfile:15-17`）。CA バンドルが無いので、いまの famifo は
どんな TLS 証明書も検証できない。これまで外向きの HTTPS 通信が無かったため問題に
ならなかった。本案では famifo 自身が JWKS とトークンエンドポイントを取りに行くので、
ビルド段から CA バンドルを持ち込む。

```dockerfile
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
```

検証を無効化する選択肢は作らない。平文より安全に見えて実際はそうでない構成を
選べるようにしない。

### famifo をどの URL で見せるか

DSM のリバースプロキシで HTTPS を終端する。famifo は HTTP のまま待ち受ける。
証明書は DSM が持つ Let's Encrypt のもの（`shirotae.synology.me` と
`*.shirotae.synology.me` の両方を含む）をそのまま使えるので、famifo は証明書を知らない。

**`https://shirotae.synology.me:8443` を推奨する。** 443 番は DSM が使っているため
別のポートを充てる。名前が1つで済むので、DNS の上書きも1件で足りる。

ワイルドカード証明書があるので `https://famifo.shirotae.synology.me` のように
サブドメインで分ける構成も選べる（DSM のリバースプロキシはホスト名でも振り分けられる）。
URL からポートが消える代わりに、DNS の上書きが2件になる。好みで選べるが、
本 spec は前者を前提に書く。

### 設定

| フラグ | 既定 | 意味 |
|---|---|---|
| `-oidc-issuer` | 空 | IdP の issuer。空なら認証しない |
| `-oidc-client-id` | 空 | IdP に登録したクライアント ID |
| `-external-url` | 空 | famifo が外から見える URL。`redirect_uri` の組み立てに使う |

クライアントシークレットはフラグで渡さない。コマンドライン引数は同じホストの誰からでも
`/proc` で読める。環境変数 `FAMIFO_OIDC_CLIENT_SECRET` から読む。

`Config.Validate` は、`-oidc-issuer` が指定されたら `-oidc-client-id`、`-external-url`、
シークレットの環境変数がすべて揃っていることを確かめ、欠けていたら落とす。`-external-url` は
絶対 URL で scheme が `http` か `https` であることを確かめる。この scheme が `https` の
ときに Cookie へ `Secure` を付ける。ヘッダからは推測しない。

`redirect_uri` は `-external-url` + `/auth/callback`。この値を SSO Server の
アプリケーション登録にも同じ文字列で入れる必要がある。

`session.key` の場所は `DBPath()` / `ThumbDir()` と同じく `config` が組み立てる。

### 変更する場所

- `internal/oidcauth/`（新規）
- `internal/session/`（新規）
- `internal/web/auth.go`（新規）
- `internal/web/server.go` — `NewServer` が `*Auth` を受け取る。`Handler` が包む
- `internal/web/view.go` — `galleryView` に認証の有無を足す
- `internal/web/templates/gallery.html` — ログアウトのボタン
- `internal/config/config.go` — `OIDCIssuer`、`OIDCClientID`、`OIDCClientSecret`、
  `ExternalURL`、`SessionKeyPath()`、検証
- `main.go` — フラグの追加と組み立て
- `Dockerfile` — CA バンドルの取り込み
- `go.mod` — `go-oidc` と `oauth2`

## 失敗の仕方

- **IdP に届かない（起動時）** — 起動を止める。discovery を引けない状態で認証すると
  宣言はできない
- **IdP に届かない（運用中）** — 新規のログインはできない。既にログイン済みの人は
  見続けられる。famifo はセッションの検証に IdP を使わないため
- **名前を解決できない** — 上と同じ症状になる。DNS の上書きが外れたときにここへ来る
- **CA が無い** — すべてのログインが証明書の検証エラーで落ちる。Dockerfile の変更を
  忘れるとこうなる
- **`state` が合わない・一時 Cookie が無い** — `/login` からやり直させる。Cookie を
  消した、10 分以上放置した、別のタブで始めた、のいずれか
- **`session.key` が読めない・書けない** — 起動を止める
- **鍵が変わった** — 全員のCookieが検証に落ち、ログインへ送られる。意図した一括
  ログアウトの手段でもある
- **時計のずれ** — ID トークンの `exp` / `iat` の検証に落ちる。NAS とコンテナは
  同じ時計を見るので、IdP が同じ機械にいる限り起きにくい

## 引き換えになるもの

- **認可を持たない。** IdP にアカウントがある人は全員 famifo を見られる
- **個別のセッションを失効できない。** 漏れた Cookie は期限が切れるまで有効である
- **famifo からログアウトしても DSM のセッションは残る。** `end_session_endpoint` が
  無いため。同じブラウザからは即座に入り直せる
- **運用の前提が増える。** LAN 内の DNS の上書き、IdP へのクライアント登録、
  DSM のリバースプロキシ、DDNS と証明書。前案はこれらを必要としない
- **IdP が単一障害点になる。** SSO Server が壊れると新規のログインができない
- **依存が2つ増える。** `go-oidc` と `oauth2`

## テスト

外部テストパッケージから公開 API だけで書く。

- `oidcauth` — `httptest.Server` で偽の IdP を立てる。discovery、JWKS、トークン
  エンドポイントを返し、テスト内で生成した RSA 鍵で ID トークンに署名する。正常系、
  `nonce` 不一致、署名が別鍵、期限切れ、`aud` 違い、`username` が空のとき `sub` に
  落ちること
- `session` — 署名して検証できること、期限切れ、1バイト改竄、別の鍵、壊れた形式。
  鍵と時刻を注入するので実時間に依存しない
- `web` — 未認証時のリダイレクトと 401 の出し分け、`next` の検証
  （`//evil.example` が `/` に落ちる）、`/auth/callback` が `state` 不一致を弾くこと、
  一時 Cookie が無いときにやり直させること、ログアウトで Cookie が消えること。
  既存の `handlers_test` は `Auth` に nil を渡して今までどおり通る
- `browser_test` — 偽の IdP を立てて、リダイレクトからギャラリー表示までを1本
- `main_test` — フラグの解析と `Validate` の分岐（シークレットの環境変数を含む）

## ドキュメント

- `README.md`（英語）— 認証の節を足す。SSO Server にアプリケーションを登録する手順、
  `redirect_uri` を一致させること、フラグと環境変数、LAN 内で DDNS 名を NAS の IP へ
  向ける必要があること、`--add-host`、DSM のリバースプロキシの設定、`session.key` を
  消して再起動すると全端末がログアウトすること。現行の
  「Neither authentication nor HTTPS is implemented」も直す
- `docs/design.md`（日本語）— 「認証: なし」「通信: HTTPのみ」を書き換える

## 段取り

1. `Dockerfile` に CA バンドルを取り込む（単独で確認できる）
2. `internal/session`
3. `internal/oidcauth`（偽 IdP のテストを先に作る）
4. `internal/web` のミドルウェアとハンドラ
5. `config` と `main` の配線
6. SSO Server にアプリケーションを登録し、実機で通す
7. ドキュメント
