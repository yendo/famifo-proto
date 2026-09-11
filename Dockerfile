FROM golang:1.27 AS build
WORKDIR /src

# 依存を先に取る。ソースを変えただけでダウンロードし直さないため。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 版は go build が .git から読んで埋める。そのために .dockerignore で .git を
# 除いていない（追跡ファイルを除くと +dirty が付くので、そちらも除いていない）。
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /famifo .

FROM scratch

COPY --from=build /famifo /famifo

# famifo は IdP の discovery・JWKS・トークンエンドポイントを HTTPS で叩く。
# scratch には CA が無いので、ビルド段のものを持ち込まないと証明書を検証できず、
# すべてのログインが x509 のエラーで落ちる。認証を使わない構成でも害はない。
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# 日付の切り出しは time.Local に依存する。タイムゾーンデータベースはバイナリに
# 埋め込んであるが、TZ を渡さないと /etc/localtime を探しに行って失敗し、UTC に
# 落ちる。そのまま初回インデックスを作ると全件が誤った日付で固定される。
# 起動ログの timezone= で確認できる。
ENV TZ=Asia/Tokyo

# 実行ユーザー。写真は :ro でマウントするので読み取りしか許していないが、
# :ro を書き忘れたときの二段目の守りとして非rootで動かす。root だと
# 書き忘れた瞬間に写真の共有フォルダへの全権を持つ。
#
# Docker はユーザー名では動かせない。scratch には /etc/passwd が無いので
# uid/gid の数値で指定する。65534:65534 は nobody:nogroup の慣例値で、特定の
# 環境に紐づかない。ホスト側のアカウントに合わせたければ実行時に渡す。
# 再ビルドは要らない:
#
#   docker run --user 1000:1000 ...
#
# 65536 以上は選ばない。userns-remap や rootless Docker では subuid の割り当てが
# 既定で 65536 個（コンテナ内 uid 0..65535）しかなく、範囲外の uid は起動時に
# 解決できずに落ちる。
USER 65534:65534

# HEALTHCHECK は付けない。scratch にはシェルも curl も無いので、exec 形式で
# 動かすには famifo 自身にヘルスチェック用のフラグを実装することになる。
# しかも Docker は unhealthy なコンテナを再起動しない（それは Swarm の機能）。
# 得られるのは docker ps や DSM の画面での表示だけなので、割に合わない。
#checkov:skip=CKV_DOCKER_2:scratch has no shell; needs an app-side flag
# Trivy の DS-0026 は .trivyignore で抑止している。ここに書いても効かない。

EXPOSE 8080

# 既定はマウントだけで動く形。写真は /photos の下に、データは /data に置く。
#
#   ssh nas 'sudo mkdir -p /volume1/famifo/data && sudo chown 65534:65534 /volume1/famifo/data'
#
#   docker run -d --restart unless-stopped -p 8080:8080 \
#     -v /volume1/photo:/photos:ro \
#     -v /volume1/famifo/data:/data \
#     ghcr.io/yendo/famifo-proto
#
# bind mount ではホスト側の所有者がそのまま適用される。コンテナのuidで書ける
# ようにしておかないと起動時に落ちる。named volume と違い所有者は継承されない。
#
# 写真の置き場所が複数あるなら、-v 1つにつき -dir 1つを明示する。
#
#   docker run -d --restart unless-stopped -p 8080:8080 \
#     -v /volume1/photo:/photos/main:ro \
#     -v /mnt/usb:/photos/usb:ro \
#     -v /volume1/famifo/data:/data \
#     ghcr.io/yendo/famifo-proto -dir /photos/main:/photos/usb -data /data
#
# /data のマウントは省略できない。省くとDBとサムネイルがコンテナの書き込み層に
# 置かれ、イメージ更新でコンテナを作り直した時点で消える。DSMのGUIでの更新手順は
# コンテナの作り直しそのものなので、更新のたびに数時間の再インデックスになる。
# しかも初回の動作確認では気づけない。
#
# 分ける基準は「別々にマウントが外れうるか」。削除ガードはルート単位で働き、
# 空に見えるルートの写真を消さずに残す。既定の -dir /photos ひとつでは、その下の
# マウントが1つ外れても /photos 全体は空にならないため、ガードが働かずに
# そのぶんの写真がインデックスから消える。逆に同じマウントの中を細かく分けても、
# まとめて出入りするのでガードの観点では意味がない。
ENTRYPOINT ["/famifo"]
CMD ["-dir", "/photos", "-data", "/data"]
