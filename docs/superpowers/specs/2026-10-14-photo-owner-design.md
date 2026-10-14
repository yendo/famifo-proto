# 写真ごとに撮影者を表示する

## 目的

全画面表示（ライトボックス）で、その写真を誰が撮ったかを分かるようにする。
撮影者はファイルの所有者（Unix の uid）で判別する。

将来は次も予定している。今回の設計はこれらを足しやすい形にするが、実装はしない。

- ユーザーでの絞り込み（uid を DB に持つ理由）
- EXIF の項目（カメラ、レンズ、露出など）を情報として並べる（`/info/{id}` を分ける理由）
- スター（利用者が書き込むデータ。DB を作り直す運用と衝突するので別に検討する）

## 前提の確認

所有者が撮影者を表すかを、2026-10-14 に実機で確かめた。

- Synology Photos の共有スペースに入った写真は、誰が上げても
  `SynologyMoments`（サービス用アカウント）の所有になる。所有者では判別できない。
- 以前の機械から uid を保ったままコピーした写真は、DSM のアカウントに無い uid の
  所有になっていた。今のアカウントへ `chown` して揃えた。

DSM の `/etc/passwd` は GECOS（5 番目の項目）に人の読める名前を持つ。

## 設計

### データ層（media / store / index）

`media.Media` に所有者の uid を持たせる。引数の位置は `path` の次。

```go
func New(path string, ownerUID uint32, takenAt, modTime time.Time) (Media, error)
func Restore(path string, ownerUID uint32, takenAtLocal string, modTime time.Time) Media
func (m Media) OwnerUID() uint32
```

名前は `UID` にしない。`ID()` の隣に並ぶと「unique ID」とも読める。

名前ではなく uid を持つ。名前は配信のときに引くので、DSM 側で説明を書き換えても
DB を作り直さずに済む。

`store` の `media` 表に `owner_uid INTEGER NOT NULL` を `path` の次に足し、
`upsertSQL` と `selectCols` を対応させる。`probeReadable` は `selectCols` を
読むので、古い DB は起動時に落ちる。移行は書かず、DB を作り直す。

`index.indexFile` は既にある `os.Lstat` の結果から uid を取る
（`fi.Sys().(*syscall.Stat_t).Uid`）。追加の I/O は無い。`syscall.Stat_t` は
Linux 前提だが、ビルド対象は goreleaser も CI も Linux だけである。

所有者の変更には追従しない。`chown` は mtime を変えないので、走査は
mod_time が同じ行を飛ばし（`scan.go` の `wasRegistered && modTime == ...`）、
watcher は Chmod を見ていない。所有者を直したら DB を作り直す。絞り込みを
作るときには、別の人の一覧に写真が混ざるので改めて検討する。

### 名前の解決と `GET /info/{id}`（web）

名前は要求のたびに `os/user.LookupId` で引き、キャッシュしない。
`CGO_ENABLED=0` では標準ライブラリの純 Go 実装が `/etc/passwd` を読む。

- 表示名は `User.Name`（GECOS）。空なら `User.Username`。
- 引けない場合と uid 65534（nobody）は名前を出さない。userns-remap や
  rootless Docker では範囲外の uid が 65534 に潰れ、`/etc/passwd` に nobody の
  行があると「nobody」と出てしまうため。
- 名前を引く処理は web パッケージの非公開関数にする。

`GET /info/{id}` は他のルートと同じく認証の内側に置く。

```json
{"owner": "Alice Example"}
```

- 名前が無いときは `owner` を省く（`omitempty`）。EXIF の項目は後でここに足す。
- id が無ければ 404。`/item/{id}` と違い JS からの取得なので、リダイレクトしない。

`/tiles` の各要素（`mediaView`）に `infoUrl` を足す。JS は URL を組み立てず、
`pageUrl`・`fullUrl`・`thumbUrl` と同じくサーバーから受け取る。撮影者の名前
そのものは `/tiles` に載せない。一覧では使わない情報を全件分運ばないため。

起動時に `/etc/passwd` を読めなければ、`main` で Info を 1 行出す
（`owner names unavailable`）。マウントし忘れに気づけるようにする。

README と Dockerfile のコメントに `-v /etc/passwd:/etc/passwd:ro` を足す。
`/etc/passwd` にパスワードのハッシュは無い（`/etc/shadow` 側）。

### 画面（lightbox）

`#lightbox` に `<p class="lb-owner" hidden></p>` を足す。

`show()` の流れ:

1. 写真を切り替えた時点で名前を消して隠す。前の写真の名前を次に残さない。
2. 写真の表示は今までどおり先に行い、`tile.infoUrl` は待たずに裏で取る。
3. 既存の `requestSeq` で世代を見て、追い越された応答は捨てる。
4. `owner` があれば出し、無ければ隠したまま。通信の失敗や 404 も隠したままで、
   エラーは出さない。

`close()` でも隠す。

見た目は上端の中央に小さな文字で、半透明の背景を敷く。下端は動画の再生
コントロールと重なる。`pointer-events: none` を付け、タップやスワイプを
下の写真と前後ボタンへ通す。

## テスト

いずれも公開 API 経由で書く。

- `store`: `OwnerUID` を入れた `Media` が `GetByID` で同じ値に戻る。
- `index`: テストで作ったファイルを取り込み、行の `OwnerUID()` が
  `os.Getuid()` と一致する。別の uid への `chown` は root が要るので扱わない。
- `web`（`httptest` で `Handler` を叩く）
  - `os.Getuid()` の行 → `owner` が `user.LookupId` で引いた名前と一致する
  - uid 65534 の行 → `owner` が無い
  - `/etc/passwd` に無い uid の行 → `owner` が無い（引けない uid をテスト内で探す）
  - 存在しない id → 404
  - `/tiles` の各要素に `infoUrl` がある
- ブラウザテスト
  - 名前が引ける uid の写真を開くと名前が出る
  - 引けない uid の写真を開くと名前の欄が隠れている
  - 名前のある写真から無い写真へ送ると、前の名前が残らない

## 運用

- 列が増えるので DB を作り直す。
- `chown` は DB を作り直す前に済ませる。後から変えると、もう一度作り直すまで
  古い uid が残る。
- コンテナに `/etc/passwd` を `:ro` でマウントする。しないと名前は出ない。
