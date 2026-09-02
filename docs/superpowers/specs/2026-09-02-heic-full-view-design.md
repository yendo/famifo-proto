# HEICの拡大表示

## 解決する問題

一覧のタイルをタップして拡大したとき、HEICの写真がSafari以外のブラウザで表示できない。AndroidのChromeでもPCのブラウザでも、ライトボックスが空になる。

一覧のサムネイルは既に解決している。Synologyが `@eaDir` に持つ `SYNOPHOTO_THUMB_M.jpg` を借りることで、famifo自身がHEICをデコードできなくても一覧には出せる（`docs/design.md` の「Synologyのサムネイルを借りる」）。

残っているのは原本の配信経路だけで、`/photo/{id}` は拡張子から `image/heic` を付けて原本をそのまま返している。当初の設計はこれを明示的に許容し、「HEICのサーバー側サムネイル生成・JPEG変換配信」を将来の拡張候補に置いていた。本設計はその判断を覆す。

## 目標

- HEICを拡大表示したとき、Android・PCのブラウザで画像が見える
- デコードライブラリを増やさない。`CGO_ENABLED=0` の単一バイナリという前提を崩さない
- `@eaDir` は読むだけ、という既存の約束を守る

## 目標としないこと

- **借りられないHEICを見えるようにすること**。`@eaDir` にサムネイルが無いHEIC（synophoto-thumb を回す前の新着、Synology以外から来たファイル）は、これまで通り原本を配信する。見えないままだが、そのために自前のデコーダを抱える対価は目的に見合わない
- **原本を取り出す手段**。「オリジナルをダウンロードしたい」という要求は現時点で無い

## 設計

### 借りるものを、サムネイルから表示用画像へ広げる

`SYNOPHOTO_THUMB_XL.jpg`（長辺1707px、約1MB）を拡大表示に使う。一覧に使っているMと同じディレクトリにあり、同じ生成器が書いたものである。

`handlePhoto` に分岐を1つ足す。HEIC/HEIFであり、かつ `thumb_source = eadir` なら、原本ではなくXLを配信する。

```go
if photo.KindOf(p.Path) == photo.KindOpaque && p.ThumbSource == store.ThumbSyno {
    http.ServeFile(w, r, thumb.SynoLargePath(p.Path))
    return
}
w.Header().Set("Content-Type", photo.ContentType(p.Path))
http.ServeFile(w, r, p.Path)
```

判定を `KindOpaque` に紐づけるので、JPEGなど元から見える写真は引き続き原本のフル解像度が出る。借用は「見えないものの代替」に限る。

XLの拡張子が `.jpg` なので `ServeFile` が `image/jpeg` を自分で付ける。HEICのときだけ自前でContent-Typeを立てている理由は、この経路では消える。

### XLの有無を、記録も確認もしない

`thumb_source = eadir` は既に「この写真は `@eaDir` にサムネイルを持っている」という事実である。MとXLは常に一緒に作られるので、この値からXLの存在を導ける。

配信時に `os.Stat` する案と、インデックス時に判定して `full_source` カラムに持つ案を検討して、どちらも採らなかった。

- **配信時にstatする** — スキーマ変更が要らず、あとから synophoto-thumb を回せば再インデックスなしで効く。しかし既存の設計は「出どころはインデックス時に決め、配信はDBを引くだけ」で統一されており（`thumb.HasSyno` がインデックス時に払っているstatがそれ）、配信時に判断を移すとその規則が崩れる
- **カラムを足す** — 既存の `thumb_source` と対称になるが、スキーマ変更は既存DBのマイグレーションか作り直しを呼ぶ。作り直しは数時間の再インデックスを意味する。得られるのは1回のstatの節約だけで、割に合わない

導出なら、statもカラムも増やさず、既存の規則もそのまま保てる。

### 変更する場所

- `internal/thumb/thumb.go` — `synoLargeName` 定数と `SynoLargePath` を追加。既存の `SynoPath` は `SynoThumbPath` に改名する。M用とXL用が並ぶと、どちらを指すのか名前から分からなくなるため
- `internal/web/handlers.go` — `handlePhoto` の分岐
- `internal/photo/photo.go` — `KindOpaque` のコメントが事実と合わなくなるので書き換える

`app.js`・テンプレート・`store`・`index` は触らない。一覧のURL（`FullURL` は `/photo/{id}` のまま）もDBのスキーマも変わらない。

## 失敗の仕方

MがあるのにXLが無い写真に当たると `ServeFile` が404を返し、ライトボックスが空になる。変更前はそこに「表示できない原本」が出ていたので、利用者から見て見えないことに変わりはない。

この設計は「MとXLは一緒に作られる」という前提と引き換えに、statとスキーマ変更の両方を避けている。前提が崩れる状況（MだけをコピーしてXLを消す運用など）が現れたら、配信時にstatする案に戻す。

## 引き換えになるもの

**Safariでは画質が下がる。** 変更前は原本のHEIC（4032×3024など）がそのまま出ていたが、変更後は全ブラウザ一律でXL（長辺1707px）になる。iPhone・iPadでは画面解像度的に差が出ないが、MacのSafariで全画面表示すると眠くなる。

`Accept` ヘッダを見てSafariにだけ原本を返す分岐は可能だが、`Vary` の管理が増え、ブラウザごとに挙動が変わる。一律XLを選ぶ。PCでは「何も見えない」から「見える」への変化であり、差し引きは明確に改善であるため。

## テスト

`webFixture.addPhoto` は現状Mしか置いていないので、`ThumbSyno` のときにXLも書くよう拡張する。その上で `internal/web/handlers_test.go` に3本:

- HEIC + `ThumbSyno` → XLの中身が返り、`Content-Type: image/jpeg`
- HEIC + `ThumbNone` → 原本が返り、`image/heic`（既存の挙動が壊れていないこと）
- JPEG + `ThumbSyno` → 原本が返る（借用はHEIC限定であること）

`internal/thumb/thumb_test.go` に `SynoLargePath` のパス組み立てを1本。

実物での確認が1つ残る。**SynologyのXLがEXIFの回転を焼き込み済みかどうか**。Mは一覧で正しい向きに出ているので同じはずだが、縦位置のHEICを1枚、実機のブラウザで開いて確かめる。焼き込まれていなければ、借りたJPEGを回転させる必要が出てサムネイル生成と同じ問題に戻るので、その時点で再設計する。

## ドキュメント

- `docs/design.md` — 「対象メディア」の HEIC の行を書き換え、「将来の拡張候補（初期スコープ外）」から該当行を外す。導出を選んだ理由と、その前提を残す
- `README.md` — 「対応フォーマット」表の HEIC の行と、「Synologyのサムネイル」の節
