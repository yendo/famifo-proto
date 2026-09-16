# セッションを scs に載せ替える

`2026-09-10-oidc-auth-design.md` の「セッション」節を置き換える案である。認証の方式
（OIDC で IdP に委ねる）は変えない。変えるのは、認証した結果をブラウザとのあいだで
どう持ち回るかだけである。

## 解決する問題

今のセッションは自前の HMAC 署名付き Cookie である。`internal/session` が鍵を持ち、
`利用者名 + 失効時刻` に署名して `famifo_session` に載せる。ログインの往復に要る
state / nonce / PKCE verifier も、同じ仕組みの別鍵で `famifo_oidc` に載せている。
サーバーは何も覚えない。

この構えには、設計時に承知のうえで受け入れた帰結が2つある。

- **個別の失効ができない。** サーバーが発行済みの Cookie を知らないので、特定の端末
  だけログアウトさせる手段がない。`session.key` を消して全端末を一斉に切るのが唯一の
  失効手段である。
- **ログアウトが端末をまたがない。** `/logout` はその端末の Cookie を消すだけで、
  同じ利用者の他の端末は生き続ける。共有端末を置いたまま帰った、という状況で効かない。

加えて、暗号のコードを自分で持っている。署名、定数時間比較、用途ごとの鍵導出、鍵
ファイルの生成とパーミッション。どれも正しく書けてはいるが、書かずに済むなら書かない
ほうがよい種類のコードである。

`github.com/alexedwards/scs` はサーバー側セッションの標準的な実装で、Cookie には
トークンだけを載せ、中身は差し替え可能な store に置く。これに載せ替えると、上の2つの
帰結が消え、自前の暗号コードと鍵ファイルの運用が丸ごと無くなる。

## 目標

- セッションの中身をサーバー側（SQLite）に持ち、Cookie はトークンだけにする
- ログアウトでサーバー側の状態を消す。消えた Cookie は送り直しても通らない
- ログインの往復の一時状態も同じ仕組みに載せ、Cookie を1本にする
- 自前の HMAC コード（`internal/session` の現在の中身）と `session.key` を無くす
- 利用者から見た振る舞い（30日で切れる、10分でログインの往復が切れる、
  RP-Initiated Logout）は変えない

## 目標としないこと

- **端末ごとの失効の画面やコマンドを作る。** 可能にはなるが、作らない。必要になって
  から足す。
- **セッションの有効期限を利用者が設定できるようにする。** 30日固定のままとする。
- **スライディング期限。** 使うたびに延ばす方式にはしない。リクエストごとに
  `Set-Cookie` を出すか、残り時間を見て再発行する分岐が要る。家族が30日ごとに1回
  入れ直す程度なら固定で足りる。
- **既存ログインの引き継ぎ。** 移行コードは書かない（後述）。

## 設計

### 保存先

`-data` の下に `sessions.db` を別ファイルで置く。`famifo.db`（写真のインデックス）
とは分ける。

このリポジトリはスキーマ移行を書かず、列を変えたら DB を消して作り直す運用である。
セッションを `famifo.db` に同居させると、インデックスのスキーマをいじるたびに全端末が
ログアウトすることになる。写真のインデックスとセッションは寿命の違うデータで、片方の
都合でもう片方が消えるのは筋が悪い。別ファイルなら、インデックスを作り直してもログインは
残り、逆に全端末を一斉に切りたいときは `sessions.db` を消せばよい。今の
「`session.key` を消す」という手順がそのまま置き換わる。

`scs` の既定である memstore は採らない。プロセス内のメモリなので、famifo を再起動する
たびに全端末がログアウトする。今の署名 Cookie は再起動をまたいで生き延びるので、
明確な後退になる。

### `internal/session`

パッケージは残し、中身を入れ替える。`Codec`、`Sign`、`Verify`、`KeyLen`、
`LoadOrCreateKey` はすべて消える。

```go
type Store struct { /* *sql.DB、*sqlite3store.SQLite3Store、*scs.SessionManager */ }

func Open(dbPath string, secure bool, log *slog.Logger) (*Store, error)
func (s *Store) Manager() *scs.SessionManager
func (s *Store) Close() error
```

`Open` は `internal/store.Open` と同じ要領で、親ディレクトリを作り、同じ DSN
（`busy_timeout(5000)` / `journal_mode(WAL)` / `synchronous(NORMAL)`）で開き、
スキーマを作る。

```sql
CREATE TABLE IF NOT EXISTS sessions (
    token  TEXT PRIMARY KEY,
    data   BLOB NOT NULL,
    expiry REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions(expiry);
```

`scs.SessionManager` の設定は次のとおり。

| 項目 | 値 |
| --- | --- |
| `Lifetime` | 30日（今の `sessionTTL` と同じ） |
| `IdleTimeout` | 設定しない（スライディングにしないため） |
| `Cookie.Name` | `famifo_session` |
| `Cookie.HttpOnly` | true |
| `Cookie.SameSite` | Lax |
| `Cookie.Secure` | `Open` の引数。外部 URL が https のときだけ真 |
| `Cookie.Persist` | true（既定のまま） |
| `ErrorFunc` | 渡された `*slog.Logger` に吐いて500を返すものに差し替える |

`ErrorFunc` を差し替えるのは、既定が Go の標準 logger に書くためである。famifo の
ログは `slog` に統一してあり、store が落ちたときだけ別の経路に出るのは調べにくい。

`sqlite3store.New` は5分ごとに期限切れを消す掃除ゴルーチンを起こす。`Close` で
`StopCleanup` を呼んでから `db.Close` する。

`Manager()` は `*scs.SessionManager` をそのまま返す。`internal/web` は `Put` /
`PopString` / `GetString` / `RenewToken` / `Destroy` / `SetDeadline` を直に呼ぶので、
全部を包み直すファサードは意味がない。`web` が `scs` に依存することは受け入れる。

### プレースホルダの確認

`sqlite3store` の SQL は `$1` 形式のプレースホルダを使う。これは mattn ドライバを
前提にした書き方である。SQLite 自体は `$1` を名前付きパラメータとして解釈するので
cgo 不要の modernc ドライバでも通るはずだが、確証はない。

実装ではここを最初に確かめる。`Find` / `Commit` / `Delete` が modernc ドライバで
往復することを示すテストを先に書く。通らなければ `sqlite3store` は使わず、
`scs.Store` インターフェース（`Find` / `Commit` / `Delete`、任意で `All`）を満たす
store を自前で書く。4メソッドなので差し替えは小さい。

### 往復の一時状態も同じセッションに載せる

`famifo_oidc` は無くなる。ログインの往復に要る値は、匿名セッションのデータとして
持つ。セッションに入れるのは次の5キーで、値はすべて文字列である。

| キー | 意味 |
| --- | --- |
| `user` | ログイン済みの利用者名 |
| `state` | CSRF 対策の state |
| `nonce` | ID トークンの nonce |
| `verifier` | PKCE の verifier |
| `next` | ログイン後の戻り先 |

10分という往復の制限は、`scs` 自身の期限で表す。`/login` で `SetDeadline(now + 10分)`
を置くと、放置されたログイン試行は10分で期限切れになり、掃除ゴルーチンが行を消す。
`RenewToken` は新しいトークンを発行すると同時に期限を `now + Lifetime` に引き直す
ので、callback で呼べばそこから30日になる。今の `flowTTL` と `sessionTTL` の使い分けが、
そのまま1つの仕組みに乗る。

### 経路とミドルウェア

`LoadAndSave` は `/static/` には掛けない。セッションを読む意味がないうえ、
`Vary: Cookie` が付く。`Handler()` の入れ子を組み替える。

```
mux
├── GET /static/ → 埋め込みファイル（LoadAndSave の外）
└── /           → LoadAndSave(inner)
                  inner
                  ├── GET  /login
                  ├── GET  /auth/callback
                  ├── POST /logout
                  ├── GET  /signed-out
                  └── /    → authenticate(protected)
```

`auth == nil`（認証しない構成）のときは `LoadAndSave` ごと掛けない。

### ハンドラ

- **`currentUser`** — Cookie を読む代わりに `Sessions.GetString(ctx, "user")`。
- **`/login`** — まず `Destroy` する。ログインの開始は「今のセッションを捨てて入り
  直す」操作なので、すでにログイン済みの端末で `/login` を開いたら古いセッションは
  破棄される。セッション固定への備えも兼ねる。そのうえで `state` / `nonce` /
  `verifier` / `next` を `Put` し、`SetDeadline(now + flowTTL)` を置く。`flowTTL`
  は今のまま `auth.go` に残す。
- **`/auth/callback`** — 5キーを `PopString` で取り出す。読んだ時点で消えるので、
  往復の残骸が30日居座らない。state の照合、`error`、`code` の検査は今と同じ順で
  行う。Cookie が無い、または期限切れだった場合は `state` が空文字で返るので、今と
  同じ「有効期限切れです、やり直してください」に落ちる。交換に成功したら
  `RenewToken` してから `Put("user", …)`。
- **`callbackError`** — `clearCookie(flowCookie)` の代わりに `Destroy`。
- **`/logout`** — `Destroy` ひとつで DB の行が消え、Cookie も落ちる。その後の
  RP-Initiated Logout の分岐は今のままである。

### 消えるもの

`internal/web` から次が消える。

- `web.Auth` の `Key []byte` と `Secure bool`（`Secure` は Cookie の設定として
  `session.Open` へ移る）
- `Server` の `sessionCodec` / `flowCodec`
- `setCookie` / `clearCookie`
- `sessionCookie` / `flowCookie` の定数
- `flowState` 構造体と、その JSON への詰め替え

`internal/session` からは `Codec` 一式と `LoadOrCreateKey` が消える。
`internal/config` の `SessionKeyPath()` は `SessionDBPath()`（`<data>/sessions.db`）
になる。

### 起動

`main.go` の `session.LoadOrCreateKey(cfg.SessionKeyPath())` が
`session.Open(cfg.SessionDBPath(), cfg.CookieSecure(), log)` になり、`defer Close()`
が付く。`web.Auth` には `Sessions: st.Manager()` を渡す。

### 既存ログインの扱い

移行コードは書かない。現行の署名付き `famifo_session` は新方式ではただの未知トークン
で、`Find` が空振りして未認証になり、ログイン画面に落ちる。入れ替えた時点で全端末が
1回ログインし直す。`session.key` は読まれなくなるだけなので、消してよい。

## 失敗の仕方

- **`sessions.db` を開けない、スキーマを作れない** — 起動を止める。認証すると宣言
  しておいて認証できない状態で配信を始めない。今の `session.key` と同じ方針である。
- **実行中に store が落ちた（ファイルが消えた、ディスクが一杯）** — `ErrorFunc` が
  `slog` に記録して500を返す。認証されていない利用者を通してしまうよりよい。
- **掃除ゴルーチンの失敗** — `sqlite3store` は標準 logger に書いて動き続ける。期限
  切れの行が残るが、`Find` は `julianday('now') < expiry` で弾くので、認証には影響
  しない。行が溜まるだけである。
- **`sessions.db` を手で消された** — 全端末がログアウトする。これは失効の手段として
  意図した振る舞いでもある。

## 引き換えになるもの

- **認証済みの全リクエストが `sessions.db` の SELECT を1回伴う。** タイル1塊120枚の
  `<img>` が同時に来るので読み取りは束になる。WAL の主キー検索なので実害は無いはず
  だが、署名 Cookie が計算だけで完結していたのに比べれば増える。
- **`Vary: Cookie` が `/thumb` と `/file` の応答にも付く。** ブラウザの private
  cache には効くので表示には影響しない。前段に共有キャッシュを置く構成では効き方が
  変わる。
- **依存が2つ増える。** `github.com/alexedwards/scs/v2` と
  `github.com/alexedwards/scs/sqlite3store`。
- **データディレクトリにファイルが1つ増える。** `session.key` が消えて
  `sessions.db`（と WAL の副産物）が増えるので、差し引きでは増える。

## テスト

`internal/session` のテストは全面的に書き直す。公開 API だけを通す。

- `Open` → `Manager` を通した保存と取り出しの往復（`$1` プレースホルダが modernc
  ドライバで通ることがここで分かる）
- 期限切れのセッションが取り出せないこと
- `Destroy` したセッションが取り出せないこと
- `Close` がエラー無く戻り、同じファイルを開き直せること（掃除ゴルーチンが
  止まっていることは公開 API からは観測できないので、そこまでは固定しない）
- 親ディレクトリが無くても `Open` できること

`internal/web` のテストは `web.Auth` を組む箇所が `session.Open(t.TempDir()...)` に
替わる。`auth_test.go` の「往復用の署名済み値がそのままセッション Cookie として通らない」
テストは `Codec` が無くなって意味を失うので、次に置き換える。

- `/login` が発行した匿名セッションの Cookie では保護ページに入れない
- **ログアウトのあと、同じ Cookie を送り直しても入れない** — 署名 Cookie 方式では
  書けなかったテストである。サーバー側失効に移った意味がそのまま現れる

`oidc_browser_test.go` の実ブラウザ1往復は、`web.Auth` の組み立て以外はそのままで
通るはずである。

## ドキュメント

- **README** — `session.key` の説明を `sessions.db` に書き換える。「個別のセッション
  は失効できない」「`session.key` を消して再起動すると全端末がログアウトする」は事実
  でなくなるので直す。一斉ログアウトは `sessions.db` を消す操作になり、再起動は要らない。
  入れ替え時に全端末が1回ログインし直すこと、`session.key` は消してよいことを書く。
- **`2026-09-10-oidc-auth-design.md`** — 「セッション」節に、この文書で置き換わった
  旨を追記する。本文は当時の判断の記録として残す。
- **`docs/superpowers/plans/` 配下** — 履歴なので触らない。

## 段取り

1. `sqlite3store` が modernc ドライバで動くことをテストで確かめる
2. `internal/session` を入れ替える（`Open` / `Manager` / `Close`）
3. `internal/config` の `SessionKeyPath` → `SessionDBPath`
4. `internal/web` を載せ替える（`Auth`、`Handler` の入れ子、5つのハンドラ）
5. `main.go` の組み立て
6. `internal/web` のテストを置き換え、失効のテストを足す
7. README と既存 spec の追記
