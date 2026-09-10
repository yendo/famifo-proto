# DSM のアカウントによる認証

## 解決する問題

famifo は認証を持たない。`docs/design.md` は「LAN内の信頼されたネットワーク前提」と
書いてきたが、これは前提というより、認証を書かないことの言い換えだった。実際の LAN には
来客の端末も、家族以外の同居人も、勝手に繋がる機器も入る。いま famifo の URL を知って
いる人は、家族の写真を全部、最初の1枚から見られる。

かといって famifo 独自のパスワードを作るのは筋が悪い。家族が覚えるものが1つ増え、
変更・失念・端末ごとの入力を famifo が抱え込むことになる。

一方で DSM のアカウントは既にある。既に作られ、既に管理され、家族はそれで NAS に
入っている。famifo が自前の資格情報を持つ理由はない。

## 目標

- DSM のアカウントとパスワードで famifo にログインできる
- 一度ログインしたら Cookie で入れる。famifo の再起動をまたいで有効
- 認証を有効にするかは起動時の設定。無効なら今までどおり誰でも見られる
- famifo は DSM のパスワードを保存しない
- 2 段階認証を有効にしたアカウントでもログインできる

## 目標としないこと

- **認可**。DSM にログインできる人は全員 famifo を見られる。誰を通すかの制御は持たない。
  アクセス制御は DSM のユーザー管理に一本化する
- **SSO**。DSM にログイン済みでも famifo のログイン画面は出る
- **famifo 自身の TLS 終端**。HTTPS は DSM のリバースプロキシに任せる
- **ログイン失敗のレート制限**。DSM の自動ブロックに任せる（「引き換えになるもの」を見よ）
- **個別セッションの失効**。署名付き Cookie を選んだ帰結
- **DSM の他の API を使うこと**。資格情報の検証だけを行う

## 選んだ方式と、選ばなかった方式

DSM の認証機構を使う道は3つある。

**1. SSO Server（OIDC）**。Package Center の SSO Server パッケージで DSM を OpenID
Connect の IdP にし、famifo を OIDC クライアントにする。ログインは DSM の画面が行い、
パスワードは famifo を通らない。2FA も自動的に効き、DSM の自動ブロックは本来の送信元
IP に対して効く。3つのうち最も堅い。

採らなかった理由は重さである。SSO Server のエンドポイントは HTTPS 前提で、DSM を
どのホスト名で開くかを固定し、自己署名証明書ならブラウザと famifo の両方に信頼させる
手当てが要る。`redirect_uri` に `http://…:8080/…` を許すかも DSM のバージョン次第で、
許されなければ famifo 側にも TLS が必要になる。加えて famifo にリダイレクト、state/nonce、
コード交換、JWKS の取得と JWT の署名検証が入る。得られる堅さに対して、この規模の
ギャラリーには釣り合わない。

**2. リバースプロキシ + DSM のセッション Cookie**。famifo を DSM と同じホスト名で
見せると、ブラウザは DSM のセッション Cookie（`id`）を famifo にも送る（Cookie は
ホスト名とパスで分かれ、ポートでは分かれない）。その sid を DSM の API に問い合わせて
検証すれば、ログイン画面も要らず素通りで入れる。実装量は最小になる。

採らなかった理由は2つある。sid を検証する公式の API が無く、認証が要る API を sid 付きで
呼んで成功するかを見る、という未文書の振る舞いに依存すること。そして未ログインの人を
DSM のログイン画面へ送っても、DSM は元の場所へ戻す仕組みを持たないため、ログイン後に
利用者が自分で famifo を開き直すことになること。Synology Photos がこれを自然に行えるのは
DSM のパッケージとしてアプリケーションポータルに登録されているからで、外部アプリには
借りられない。

**3. DSM Web API での資格情報の検証（採用）**。famifo が自前のログイン画面を出し、
入力されたアカウントとパスワードをサーバ側から `SYNO.API.Auth` に転送して、正しいかを
DSM に判定させる。追加パッケージが要らず、DSM のアカウントと 2FA の設定をそのまま
使え、実装量は3つの中間に収まる。

引き換えは、パスワードが famifo のプロセスを通ることと、DSM から見たログイン失敗の
送信元が常に famifo になることである。

## 設計

### パッケージ境界

`internal/synology` を置き場だけのディレクトリに変え、2つのパッケージを対等に並べる。

- `internal/synology/eadir` — 既存の `internal/synology` の中身をそのまま移す。
  Synology のソフトが写真ディレクトリの中に作る実体（`@eaDir` の派生物と `#recycle`）の
  配置を扱う。使うのは取り込み側（`index`、`thumb`）
- `internal/synology/dsmauth` — DSM Web API で資格情報を検証する。使うのは配信側
  （`web`、`main`）

2つはコードも型も共有せず、本番コードの呼び出し元も重ならない。したがって一方を
もう一方の下に置かない。（`internal/web/handlers_test.go` が `@eaDir` のパスに
フィクスチャを置くために import しているが、これはテストの都合で、`web` 本体は
`eadir` に依存しない。）`synology/` 自体にはパッケージを持たせず、親を作らないことで依存関係を
含意させない。

`internal/session` — 署名付き Cookie の組み立てと検証。DSM も HTTP も知らない。

移動は認証と無関係な機械的変更なので、**単独のコミット**で先に済ませる。認証のコミットに
混ぜると、差分から設計判断が読めなくなる。

### DSM への問い合わせ

`dsmauth.Client` は DSM のベース URL と `http.Client` を持ち、次の1つだけを公開する。

```go
// Login はアカウントとパスワード（2段階認証が有効なら otp も）を DSM に問い合わせ、
// 正しければ nil を返す。
func (c *Client) Login(ctx context.Context, account, passwd, otp string) error
```

`POST {base}/webapi/entry.cgi` に form-encoded で送る。パラメータは
`api=SYNO.API.Auth`、`version=6`、`method=login`、`account`、`passwd`、`session=famifo`、
`format=sid`、`otp` が空でなければ `otp_code`。

GET でも通るが POST 固定にする。GET はパスワードをクエリ文字列に載せるため、DSM の
アクセスログと途中のプロキシログに平文で残る。famifo 自身のログにも、アカウント名は
出してもパスワードと OTP は出さない。

応答は成功が `{"success":true,"data":{"sid":"…"}}`、失敗が
`{"success":false,"error":{"code":N}}`。コードを型付きのエラーに翻訳して返し、
DSM のコードは外に漏らさない。

| DSM のコード | famifo のエラー |
|---|---|
| 400 | `ErrBadCredentials` |
| 403 | `ErrOTPRequired` |
| 404 | `ErrOTPInvalid` |
| その他 | 汎用のエラー |

**この割り当ては実機で確認してから確定する。** コードの意味は DSM のバージョンで差が
あり、取り違えると 2 段階認証の案内を出すべき場面で「パスワードが違います」が出る。
確認を段取りの最初に置く理由である。

**sid は捨てる。** famifo は自前のセッションを発行するので保持する必要がない。ただし
捨てるだけだと DSM 側にログインセッションが積み上がるので、検証に成功した直後に
`method=logout&session=famifo&_sid=…` を呼んで畳む。famifo は DSM から見て
「パスワードが正しいか聞きに来ただけの客」として振る舞う。

**ユーザー名は問い合わせない。** DSM が返すのは sid だけである。「全員通す」方針では
ユーザー名は Cookie の中身（誰のセッションかを示す）とログにしか使わず、画面には出さない
ので、追加の API を呼ばず、送信されたアカウント名をそのまま採る。

`http.Client` には 10 秒のタイムアウトを置く。DSM が落ちているときにログイン画面が
固まらないようにするため。

### セッション

`session.Codec` は署名鍵を持ち、2つだけを公開する。

```go
func (c *Codec) Issue(user string, expiry time.Time) string
func (c *Codec) Verify(value string, now time.Time) (user string, ok bool)
```

時刻を引数で受けるので、期限切れの挙動を実時間に依存せずテストできる。

Cookie の値は次の形にする。

```
base64url("<ユーザー名>\n<失効時刻のUnix秒>") + "." + base64url(HMAC-SHA256(鍵, 前半))
```

検証は前半から署名を作り直し `hmac.Equal` で比べる。`==` で比べると、一致する接頭辞の
長さが実行時間に出る。中身は誰でも読めるが改竄はできない、という素直な形である。

**署名鍵**はデータディレクトリの `session.key` に置く。無ければ起動時に `crypto/rand` で
32 バイトを作り、`0600` で書く。再起動をまたいでセッションが生き残るのはこのファイルの
おかげである。逆に、このファイルを消して再起動すれば全端末が一斉にログアウトする。
個別の失効ができない構えなので、これが唯一の一括失効手段になる。

**属性**は `HttpOnly`（JS から読む必要がない）、`SameSite=Lax`、`Path=/`、そして
`-behind-tls-proxy` が指定されているときだけ `Secure`。

**有効期限は 30 日固定**。使うたびに延ばすスライディング方式にすると、リクエストごとに
`Set-Cookie` を出すか、残り時間を見て再発行する条件分岐が要る。30 日ごとに1回入れ直す
程度なら固定で足りる。`defaultChunkSize` と同じく、利用者が変えられる設定にはしない。

**ログアウト**は `POST /logout` で Cookie を `MaxAge=-1` にして消す。署名付き Cookie を
選んだ帰結として、これはその端末の Cookie を消すだけで、他の端末のセッションは生き続ける。

### ミドルウェアの分岐

`web.NewServer` は認証をまとめた `*Auth` を受け取り、**nil なら認証しない**。
`-dsm-url` を渡したときだけ認証が有効になり、渡さなければ今までどおり動く。開発機や
テストで DSM 無しに動かせる必要があるためである。

```go
// Auth は認証の手段をまとめる。nil を渡すと認証しない。
type Auth struct {
	Verifier Verifier        // 資格情報の検証（テストでは偽物を渡す）
	Session  *session.Codec
	Secure   bool            // Cookie に Secure を付けるか
}

// Verifier は資格情報が正しいかを判定する。dsmauth.Client がこれを満たす。
type Verifier interface {
	Login(ctx context.Context, account, passwd, otp string) error
}
```

未認証のときの応答は、要求されたものによって分ける。

| 経路 | 未認証のとき |
|---|---|
| `/`、`/item/{id}` | `/login?next=…` へリダイレクト |
| `/tiles`、`/thumb/{id}`、`/file/{id}` | 401 |
| `/login`、`/static/` | 素通し |

`/tiles` は `fetch` で取りに行く。ここでログイン画面の HTML を返すと JSON の解釈が
壊れ、画面には何も出ないまま原因も読めない。401 を受けたクライアントはページを
再読み込みし、リダイレクトでログイン画面に着く。`/thumb` と `/file` は `<img>` と
`<video>` が読むので、リダイレクトしても意味がない。

`next` は先頭が `/` で、かつ `//` で始まらないものだけ受け付ける。`//evil.example` は
プロトコル相対 URL として別サイトへのリダイレクトになる。それ以外は `/` に落とす。

### ログイン画面と 2 段階認証

**1つの画面**にアカウント名・パスワード・OTP（任意）を並べる。DSM 自身は
パスワードとコードを2画面に分けるが、famifo が真似ると1画面目のパスワードを2画面目まで
持ち越す必要があり、hidden フィールドに平文で埋めるか、サーバにログイン途中の状態を
持つか、Cookie に入れる（HMAC は改竄防止であって秘匿ではないので暗号化が要る）ことに
なる。どれもパスワードを1リクエストの寿命に閉じ込めるという性質を捨てる。

2 段階認証を使っていない今は OTP 欄を空のまま送るだけでよく、将来有効にしたら同じ画面で
埋める。

エラーは同じフォームを再表示して上に1行出す。

| 結果 | 表示 |
|---|---|
| `ErrBadCredentials` | アカウント名かパスワードが違います |
| `ErrOTPRequired` | このアカウントは 2 段階認証が有効です。コードを入力してください |
| `ErrOTPInvalid` | コードが違います |
| 接続失敗 | DSM に接続できません |

どちらが違うかは言わない。`ErrOTPRequired` に当たった人はパスワードを入れ直すことに
なるが、OTP 欄を空で送った人だけが通る道なので許容する。

`templates/login.html` を足す。既存と同じ `app.css` を使い、`app.js` と
`idiomorph.min.js` は読まない（ギャラリーが無いので要らない）。文言は既存の UI に
合わせて日本語。`static/` は素通しなので未認証でも CSS は当たる。

ログアウトの導線は `gallery.html` の `header.topbar` に置く。`POST /logout` する小さな
ボタンで、認証が無効なときは出さない。そのために `galleryView` に真偽値を1つ足す。

**CSRF**。`SameSite=Lax` は cross-site の POST に Cookie を送らないので `/logout` は
守られる。ログインフォーム自体はトークンを持たない。

### 通信の保護

ブラウザと famifo の間には DSM のパスワードが流れる。認証を入れる前の famifo は
パスワードを一切扱っていなかったので、これは新しく作る弱点である。`docs/design.md` の
「通信: HTTPのみ（TLS証明書の運用コストに見合わないため）」という判断は、パスワードが
無かった時代のものなので、ここで見直す。

famifo に TLS は実装しない。**DSM のリバースプロキシに終端させる。** ログインポータルで
`https://nas:8443` → `http://localhost:8080` の転送を作れば、証明書は DSM が既に
持っているものをそのまま使え、更新も DSM が行う。famifo は証明書を知らない。

famifo から DSM への経路（`http://<DSM のホスト>:5000`）は同じ機械の中で完結し、
機外に出ない。ここを覗ける立場はホストの root、すなわち DSM 自身なので、HTTP で
差し支えない。HTTPS を指定した場合は通常どおり証明書を検証する。**検証を無効化する
フラグは作らない。** 平文より安全に見えて実際はそうでない構成を、選べるようにしない。

### 設定

追加するフラグは2つ。

| フラグ | 既定 | 意味 |
|---|---|---|
| `-dsm-url` | 空 | DSM のベース URL。空なら認証しない |
| `-behind-tls-proxy` | false | Cookie に `Secure` を付ける |

`Config.Validate` は `-dsm-url` が URL として解釈でき scheme が `http` か `https` で
あることを確かめる。`-dsm-url` 無しで `-behind-tls-proxy` を渡したらエラーにする。
認証が無ければ Cookie も無く、そのフラグは何もしない。黙って無視すると「付けたのに
効かない」に気づけないので、`-scan-workers` に 0 を渡したときと同じく落とす。

`session.key` の場所は `DBPath()` / `ThumbDir()` と同じく `config` が組み立てる。

### 変更する場所

- `internal/synology/` → `internal/synology/eadir/`（移動のみ。import 元は本番コードが
  `index/scan.go`、`index/watch.go`、`thumb/thumb.go`、テストが
  `index/indexer_test.go`、`thumb/thumb_test.go`、`thumb/paths_test.go`、
  `web/handlers_test.go`、それに移動する `synology_test.go` 自身）
- `internal/synology/dsmauth/`（新規）
- `internal/session/`（新規）
- `internal/web/auth.go`（新規）— ログイン画面のハンドラとミドルウェア
- `internal/web/server.go` — `NewServer` が `*Auth` を受け取る。`Handler` が包む
- `internal/web/view.go` — `galleryView` に認証の有無を足す
- `internal/web/templates/login.html`（新規）、`gallery.html` — ログアウトのボタン
- `internal/config/config.go` — `DSMURL`、`BehindTLSProxy`、`SessionKeyPath()`、検証
- `main.go` — フラグの追加と組み立て

## 失敗の仕方

- **DSM が落ちている** — 新規のログインはできず、ログイン画面に「DSM に接続できません」と
  出る。既にログイン済みの人は見続けられる。毎回 DSM に問い合わせないので、DSM の
  Web サービスが再起動している間もギャラリーは動く
- **`session.key` が読めない・書けない** — 起動を止める。認証すると宣言しておいて
  黙って無認証で配信するより、起動しないほうがよい
- **鍵が変わった** — 全員のCookieが検証に落ち、ログイン画面へ送られる。意図した
  一括ログアウトの手段でもある
- **Cookie の期限切れ** — ページはリダイレクト、`/tiles` などは 401。クライアントは
  再読み込みしてログイン画面に着く
- **DSM の自動ブロックに掛かった** — famifo コンテナの IP が遮断され、正しいパスワードを
  入れても誰もログインできない。既にログイン済みの人だけが見られる状態になる。
  復旧は DSM の管理画面でブロックを解除する操作

## 引き換えになるもの

いずれも承知のうえで選んだ。

- **ログイン失敗のレート制限を持たない。** DSM の自動ブロックに任せる。その自動ブロックは
  送信元 IP 単位なので、famifo 経由の失敗はすべて famifo コンテナの IP に集まる。
  打ち間違いが続くと famifo からの認証が丸ごと止まり、症状からは原因が読めない。
  復旧手順を README に書くことで補う
- **個別のセッションを失効できない。** 漏れた Cookie は期限が切れるまで有効である。
  鍵を作り直して全員をログアウトさせる以外の手段はない
- **ログイン CSRF を防がない。** 理屈としては存在するが、家族向けギャラリーで守る価値に
  見合わない
- **認可を持たない。** DSM にアカウントがある人は全員 famifo を見られる。DSM 側で
  アカウントを増やすことが、そのまま famifo の閲覧許可になる
- **パスワードが famifo のプロセスを通る。** 保存はしないが、平文で受け取って DSM に
  転送する

## テスト

外部テストパッケージから公開 API だけで書く。

- `dsmauth` — `httptest.Server` で DSM を偽装する。成功、400、403、404、接続失敗、
  壊れた JSON。実機で確認した応答をそのままテストデータにする。sid の logout を
  呼んでいることも偽サーバ側で確かめる
- `session` — 発行して検証できること、期限切れ、1バイト改竄、別の鍵、壊れた形式。
  鍵と時刻を注入するので実時間に依存しない
- `web` — `Auth.Verifier` に偽物を渡す。未認証時のリダイレクトと 401 の出し分け、
  `next` の検証（`//evil.example` が `/` に落ちる）、ログイン成功で Cookie が出ること、
  ログアウトで消えること。既存の `handlers_test` は `Auth` に nil を渡して今までどおり通る
- `browser_test` — ログインしてギャラリーが出るまでを1本
- `main_test` — フラグの解析と `Validate` の分岐

## ドキュメント

- `README.md`（英語）— 認証の節を足す。`-dsm-url` の使い方、DSM のリバースプロキシで
  HTTPS を終端する手順、`session.key` を消して再起動すると全端末がログアウトすること、
  自動ブロックに掛かったときの症状と復旧手順。現行の
  「Neither authentication nor HTTPS is implemented」も直す
- `docs/design.md`（日本語）— 「認証: なし」「通信: HTTPのみ」を書き換える

## 段取り

1. `internal/synology` → `internal/synology/eadir` の移動（単独コミット）
2. 実機で `SYNO.API.Auth` の応答を確認し、エラーコードの割り当てを確定する
3. `internal/synology/dsmauth`
4. `internal/session`
5. `internal/web` のミドルウェアとログイン画面
6. `config` と `main` の配線、ドキュメント
