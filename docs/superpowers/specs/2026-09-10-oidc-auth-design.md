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
  コード交換、ID トークンの検証）。ただし検証そのものは `go-oidc` に委ねる（後述）
- 依存が2つ増える。`github.com/coreos/go-oidc/v3` と `golang.org/x/oauth2`
- IdP 側にクライアントを登録する運用
- famifo が自分の外部 URL を知る必要（`redirect_uri` は絶対 URL）
- コンテナに CA 証明書が要る（後述）
- LAN 内での名前解決の上書き（後述）

famifo が書くコードは前案より減り、複雑さはコードから設定と運用へ移る。

## 実機で確認したこと

2026-09-10 に DSM（DS918、`192.168.1.2`）で Synology SSO Server を導入し、OIDC を
有効にして、**認可コードフローを実際に1往復させた**。使い捨てのクライアントを書いて、
ブラウザで DSM にログインし、claim を取得するところまで通している。結論として
**標準的な OIDC として扱える**。

### discovery

```
issuer                            https://shirotae.synology.me:5001/webman/sso
authorization_endpoint            .../webman/sso/SSOOauth.cgi
token_endpoint                    .../webman/sso/SSOAccessToken.cgi
userinfo_endpoint                 .../webman/sso/SSOUserInfo.cgi
jwks_uri                          .../webman/sso/openid-jwks.json
code_challenge_methods_supported  S256, plain
grant_types_supported             authorization_code, implicit
response_types_supported          code, code id_token, id_token, id_token token
id_token_signing_alg_values       RS256
token_endpoint_auth_methods       client_secret_basic, client_secret_post
scopes_supported                  email, groups, openid
claims_supported                  aud, email, exp, groups, iat, iss, sub, username
```

discovery は issuer 直下の `/.well-known/openid-configuration` にあり、仕様どおりの
配置である。JWKS も取得でき、RS256 の鍵が1本入っていた。

### issuer にはポート番号を含める

**SSO Server の「サーバー URL」にはポートを書く必要がある。** これを省くと issuer が
443 番になり、そこでは動かない。443 番で待っているのは DSM 本体ではなく静的配信の
既定サイトで、GET は通るが **POST が nginx に 405 で拒否される**（`/webapi/entry.cgi`
でも同じ）。したがってトークン交換が必ず失敗する。authorization_endpoint も 443 番では
実体がなく、`location.replace` で 5001 番へ飛ばす JavaScript を返すだけである。
ブラウザは追随するがサーバー間の POST は追随しないので、この壊れ方は見つけにくい。

サーバー URL を `https://shirotae.synology.me:5001` にすると、広告される URL がすべて
5001 番になり、実体と一致する。リバースプロキシも DSM のポート変更も要らない。

### 一往復させて分かったこと

| 項目 | 結果 |
|---|---|
| PKCE | S256 で通る |
| クライアント認証 | `client_secret_post` で通る |
| ID トークン | RS256、JWKS の鍵で署名を検証できた |
| `nonce` | 要求した値がそのまま入る |
| トークンの寿命 | `expires_in=180`（3分） |

取得した claim。

```json
{
  "iss": "https://shirotae.synology.me:5001/webman/sso",
  "aud": "<client_id>",
  "sub": "yendo",
  "username": "yendo",
  "email": "<DSM アカウントに設定したアドレス>",
  "groups": ["users"],
  "auth_time": 1789031843,
  "iat": 1789031843,
  "exp": 1789032023,
  "nonce": "<要求した値>"
}
```

**標準から外れる点が3つある。**

1. ユーザー名の claim は **`username`** で、標準の `preferred_username` ではない
2. **`profile` スコープが無い**。要求できるのは `openid`、`email`、`groups`
3. **`sub` が不透明な識別子ではなく、ユーザー名そのもの**である。したがって DSM で
   アカウント名を変更すると `sub` も変わる。「安定した識別子」として扱えない

3 は本案では実害が小さい。認可を持たず（全員通す）、famifo は `sub` を鍵にして何かを
保存するわけではなく、誰のセッションかを示すために持つだけだからである。将来ユーザーごとの
状態を持つようになったら、この前提を見直す必要がある。

**`groups` claim が実際に入った**（`["users"]`）。本案では使わないが、グループで絞りたく
なったときに追加の API 呼び出しが要らないことは確認できている。

**`email` は DSM アカウントに設定があれば入る。** 設定していないアカウントでは空になるので、
鍵には使えない。

### 標準ライブラリだけでも書けるが、検証はライブラリに任せる

確認に使ったクライアントは Go の標準ライブラリだけで書いた。discovery の取得、
state/nonce/PKCE の生成、コード交換、JWKS の取得、RS256 署名の検証、`iss`/`aud`/`exp`/
`nonce` の検証まで含めて 250 行程度である。技術的には依存は必須ではない。

**それでも ID トークンの検証は `go-oidc` に委ねる。** 一度この方針で実装してレビューを
通したところ、issuer すり替えの防御を検証しているはずのテストが、別の理由（discovery の
404）で通っていた。防御のコードは正しかったが、一度も実行されていなかった。
`oidc.NewProvider` は discovery が名乗る issuer の一致確認を内部に持つので、この防御を
自分で書く必要も、そのテストを書き損なう機会も無くなる。

一般化すると、プローブが一往復通ったことは実現可能性の証拠であって、失敗経路の正しさの
証拠ではない。セキュリティのコードが住んでいるのは失敗経路のほうで、そこは実績のある
実装に任せるほうが安い。JWKS の鍵ローテーション、`aud` の配列形式、クロックスキュー、
JWKS の `use`/`alg` による鍵の選別といった、レビューが「いつか」に回した項目も、
まとめてライブラリの側に移る。

**Cookie の署名は自前のまま残す。** こちらは `internal/session` が用途ごとに鍵を導出する
形になっており（後述）、`gorilla/securecookie` が Cookie 名を MAC に含めて得ているのと
同じ性質を既に持っている。置き換えても得るものが薄く、候補となるライブラリは
最終リリースが 2023 年 10 月で2年動いていない。

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

- `internal/oidcauth`（新規）— `go-oidc` と `x/oauth2` を包み、famifo が必要とする操作だけを
  公開する。ライブラリと同じ名前にしないのは、import したときにどちらの話をしているか
  読めるようにするため
- `internal/session`（新規）— 署名付き Cookie の組み立てと検証。OIDC も HTTP も知らない
- `internal/web/auth.go`（新規）— ハンドラと認証ミドルウェア

`internal/synology` は触らない。

### OIDC クライアント

```go
// Identity は認証できた利用者。
type Identity struct {
	Subject  string   // sub。この IdP ではユーザー名そのもので、不透明ではない
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
`NewProvider` は discovery が名乗る issuer と設定した issuer の一致も確かめるので、
その防御を自分で書く必要はない。

ID トークンの検証は `provider.Verifier(&oidc.Config{ClientID: …})` が返す
`IDTokenVerifier` に任せる。署名、`iss`、`aud`、`exp`/`nbf` を見る。

**`nonce` だけは対象外である。** `IDTokenVerifier.Verify` は nonce を検証しないと
明記されており、呼び出し側の責任になる。`idToken.Nonce` を自分で比べること。ここを
忘れると、認可コードを横取りした攻撃者の再生を防げなくなる。

PKCE は `x/oauth2` の `GenerateVerifier` / `S256ChallengeOption` / `VerifierOption` を
使う。`plain` は選ばない。

要求するスコープは `openid email groups`。`profile` は SSO Server に無いので要求しない。
`email` と `groups` は本案では使わないが、`username` claim がどのスコープに紐づくかは
文書化されていない。提供される3つをすべて要求しておき、実機で通したあとに減らせるかを
確かめる。減らせるなら `openid` だけにする。

`username` は標準の claim ではないので、`IDToken.Claims` に独自の構造体を渡して取り出す。
IdP を差し替えたときにここが空になる可能性があるため、**空なら `sub` で代用する**。

### 認可コードフローの往復

リダイレクトから callback までの間、`state`、`nonce`、PKCE の verifier、戻り先の
`next` を保持する必要がある。サーバー側に状態を持たず、**短命の署名付き Cookie**に
入れる。有効期限は 10 分で、callback で使い切ったら消す。署名にはセッションとは
**別の鍵**を使う（後述）。

PKCE の verifier を Cookie に置いても差し支えない。PKCE が防ぐのは第三者による
認可コードの横取りで、Cookie を読めるのは利用者本人だからである。`state` と `nonce` に
必要なのは改竄されないことであり、秘匿ではない。したがって署名だけでよく、暗号化は要らない。

`code_challenge_method` は **S256**。`plain` も提供されているが選ばない。

### セッション

`session.Codec` は署名鍵と**用途**を持ち、任意の文字列に署名する。

```go
func NewCodec(key []byte, purpose string) (*Codec, error)
func (c *Codec) Sign(payload string, expiry time.Time) string
func (c *Codec) Verify(value string, now time.Time) (payload string, ok bool)
```

ログイン後のセッションは payload に利用者名を入れ、往復の一時状態は payload に
`state` / `nonce` / verifier / `next` をまとめた JSON を入れる。

**用途ごとに鍵を分けるのは、分けないと認証が丸ごと迂回できるからである。** 当初の設計は
1つの `Codec` で両方に署名していた。payload には自分が何であるかを示すものが無いので、
`/login` が匿名の訪問者に渡す一時 Cookie の値を、名前だけ `famifo_session` に変えて
送り返すと検証が通り、資格情報も IdP との往復も無しにギャラリーが開く。実装のレビューで
実際に再現した。

`NewCodec` は `HMAC-SHA256(マスター鍵, 用途)` で用途ごとの部分鍵を導出する。導出した鍵は
互いに無関係なので、ある用途で署名した値が別の用途で通ることが表現できなくなる。
`web.Auth` が受け取るのは `Codec` ではなくマスター鍵そのもので、2つの `Codec`
（用途 `"session"` と `"flow"`）は `web` の内部で導出する。呼び出し側が同じ `Codec` を
両方に渡す余地を残さないためである。

Cookie の値は次の形にする。

```
base64url("<失効時刻のUnix秒>\n<payload>") + "." + base64url(HMAC-SHA256(用途ごとの鍵, 前半))
```

失効時刻を先に置くのは、payload に改行が混ざっても最初の1つで切り出せるようにするため
である（一時状態は JSON を載せる）。

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
	OIDC   Provider // *oidcauth.Client が満たす。テストが偽物を渡せるようにする
	Key    []byte   // 署名のマスター鍵。用途ごとの Codec は web が導出する
	Secure bool     // Cookie に Secure を付けるか
}
```

ログアウトの導線は `gallery.html` の `header.topbar` に置く。`POST /logout` する
小さなボタンで、認証が無効なときは出さない。`galleryView` に真偽値を1つ足す。

### 到達性の条件

本案は前案に無い前提を2つ持ち込む。どちらも実機で構築し、動作を確認した。

**1. LAN 内で `shirotae.synology.me` が `192.168.1.2` に解決される必要がある。**

この名前は DDNS で登録した公開 IP を指す。ポートを開けていないので、LAN 内から
その名前で NAS には届かない。ブラウザ（認可へのリダイレクト）も famifo（discovery、
JWKS、トークン交換）も、この名前でこのホストに届かなければならない。

実際に機能した構成は次のとおり。

1. DSM に **DNS Server パッケージ**を入れ、「解決」で転送（フォワーダー）を有効にする。
   これを飛ばすと DSM が持つゾーン以外を答えられず、LAN の名前解決が壊れる
2. `shirotae.synology.me` のマスターゾーンを作り、apex の A レコードで `192.168.1.2` を返す
3. ホームゲートウェイ（NTT RV-230NE）の**ローカルドメイン設定**で、ドメイン名に
   `shirotae.synology.me`、プライマリ DNS に **DSM の IPv6 アドレス**を指定する

3 で IPv6 を使うのは好みではなく強制である。**この欄は IPv4 アドレスを受け付けない。**
`192.168.1.2` も `8.8.8.8` も「アドレスが入力範囲外です」で弾かれる。エラーメッセージは
値の範囲の問題に見えるが、実際はアドレスの種別の問題である。同じ挙動が同型機
（RV-440NE）でも報告されている。

この形の利点は、DHCP を一切触らずに済み、影響範囲が `shirotae.synology.me` の1ドメインに
閉じることである。DSM が止まっても他の名前解決はホームゲートウェイが答え続ける。
DSM を LAN 全体の DHCP/DNS にする案も検討したが、ホームゲートウェイが IPv6 の広告（RA）を
出し続ける以上、IPv6 対応の端末が DSM の配る DNS を無視しうるため採らなかった。

**弱点。** ローカルドメイン設定に書くのは DSM の IPv6 アドレスそのものである。その後半
（インターフェース ID）は MAC から作られるので変わらないが、**前半のプレフィックスは
回線側から配られる**ので変わりうる。フレッツでは何年も変わらないことが多く、機器の再起動や
停電では変わらないが、ホームゲートウェイの交換・初期化、回線の引き直し、事業者の変更で
変わる。

変わったときの症状は「**famifo にだけログインできない。他のサイトは普通に見られる**」で
ある。原因に辿り着きにくいので、確認場所をここに書き残す。ホームゲートウェイのローカル
ドメイン設定のプライマリ DNS が、DSM の現在の IPv6 アドレスと一致しているかを見る。

**コンテナからの名前解決。** DSM 上の Docker はホストの解決器を使うので、上の構成が
効いていれば追加の手当ては要らないはずである。ただし確認していない。起動時に IdP へ
届かなければ `docker run --add-host shirotae.synology.me:192.168.1.2` で固定する。

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

**証明書について。** DSM の証明書は Synology DDNS のホスト名に対して Let's Encrypt で
取得した。DDNS の登録画面にある証明書取得のチェックボックスを使うと、所有確認が
Synology の DNS 側で行われるため、**ポートを1つも開けずに正規の証明書が手に入る**。
コントロールパネルの証明書画面から取る経路は HTTP-01 になり、ポート 80 の開放が要る。
SAN には `shirotae.synology.me` と `*.shirotae.synology.me` の両方が入った。

### famifo をどの URL で見せるか

DSM のリバースプロキシで HTTPS を終端する。famifo は HTTP のまま待ち受ける。
証明書は DSM が持つ Let's Encrypt のものをそのまま使えるので、famifo は証明書を知らない。

**`https://shirotae.synology.me:8443` を推奨する。** 5001 番は DSM 本体、443 番は DSM の
既定サイトが使っているため、別のポートを充てる。DNS のゾーンに追加のレコードが要らない。

ワイルドカード証明書があるので `https://famifo.shirotae.synology.me` のようにサブドメインで
分ける構成も選べる。DSM のゾーンには `famifo` の A レコードを既に置いてあり、証明書も
覆っている。URL からポートが消える代わりに、443 番で既定サイトより先にリバースプロキシへ
振り分けられるかを確かめる必要がある。未確認なので、本 spec は前者を前提に書く。

### 設定

| フラグ | 既定 | 意味 |
|---|---|---|
| `-oidc-issuer` | 空 | IdP の issuer。空なら認証しない。ポート番号を含める |
| `-oidc-client-id` | 空 | IdP に登録したクライアント ID |
| `-external-url` | 空 | famifo が外から見える URL。`redirect_uri` の組み立てに使う |

クライアントシークレットはフラグで渡さない。コマンドライン引数は同じホストの誰からでも
`/proc` で読める。環境変数 `FAMIFO_OIDC_CLIENT_SECRET` から読む。

`-oidc-issuer` にはポート番号が要る。Synology SSO Server の場合は
`https://shirotae.synology.me:5001/webman/sso` である。省くと discovery は引けるが
トークン交換が 405 で失敗する（「実機で確認したこと」を見よ）。

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

`go.mod` に2つ増える。`github.com/coreos/go-oidc/v3` と `golang.org/x/oauth2`
（推移的に `github.com/go-jose/go-jose/v4` が入る）。Cookie の署名は自前のままなので
`internal/session` は変わらない。

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
- **依存が2つ増える。** `go-oidc` と `x/oauth2`。当初は標準ライブラリだけで書いたが、
  issuer すり替えの防御が未テストのまま通っていたことを受けて、検証はライブラリに移した
- **LAN 内の名前解決が、回線由来の IPv6 アドレスに依存する。** 詳細は「到達性の条件」に
  書いた。プレフィックスが変われば `shirotae.synology.me` だけが引けなくなり、
  famifo にログインできなくなる

## テスト

外部テストパッケージから公開 API だけで書く。

- `oidcauth` — `httptest.Server` で偽の IdP を立てる。discovery、JWKS、トークン
  エンドポイントを返し、テスト内で生成した RSA 鍵で ID トークンに署名する。正常系、
  `nonce` 不一致、署名が別鍵、期限切れ、`aud` 違い、`iss` 違い、`username` が空のとき
  `sub` に落ちること、issuer すり替えを拒否すること。加えて **`alg` が RS256 以外の
  ID トークンを拒否すること**（`none` と HMAC の両方）。
  検証そのものは `go-oidc` の担当になったので、これらの狙いは「ライブラリが正しいこと」の
  再確認ではなく、**私たちの配線が正しいこと**である。とくに `nonce` は
  `IDTokenVerifier.Verify` の対象外で呼び出し側の責任なので、`nonce` 不一致のテストは
  外せない。依存を差し替えたときに気づけるのも、この種のテストだけである
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
  `redirect_uri` を一致させること、issuer にポートを含めること、フラグと環境変数、
  LAN 内の名前解決（DNS Server パッケージのゾーンと、ホームゲートウェイのローカル
  ドメイン設定に DSM の IPv6 アドレスを入れること）、`--add-host`、DSM のリバース
  プロキシの設定、`session.key` を消して再起動すると全端末がログアウトすること、
  IPv6 プレフィックスが変わったときの症状と確認場所。現行の
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
