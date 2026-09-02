FROM golang:1.27 AS build
WORKDIR /src

# 依存を先に取る。ソースを変えただけでダウンロードし直さないため。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# .git はビルド文脈から除いてあるので、Goが自動で埋め込むVCS情報は入らない。
# 版はここで渡す。省略すると -version が "dev" になる。
#   docker build --build-arg VERSION=$(git rev-parse --short HEAD) ...
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /famifo .

FROM scratch
ARG VERSION=dev

# 実行ユーザー。写真は :ro でマウントするので読み取りしか許していないが、
# :ro を書き忘れたときの二段目の守りとして非rootで動かす。root だと
# 書き忘れた瞬間に写真の共有フォルダへの全権を持つ。
#
# Docker はユーザー名では動かせない。scratch には /etc/passwd が無いので
# uid/gid の数値で指定する。既定値は配置先の NAS で作った famifo ユーザーに
# 合わせてある。別の環境で使うなら:
#
#   ssh nas 'id famifo'            → uid/gid を調べる
#   docker run --user 1000:1000 ... → 実行時に指定（再ビルド不要）
#   docker build --build-arg UID=1000 --build-arg GID=1000 ...  → 焼き込む
ARG UID=1029
ARG GID=100

COPY --from=build /famifo /famifo

# 日付の切り出しは time.Local に依存する。タイムゾーンデータベースはバイナリに
# 埋め込んであるが、TZ を渡さないと /etc/localtime を探しに行って失敗し、UTC に
# 落ちる。そのまま初回インデックスを作ると全件が誤った日付で固定される。
# 起動ログの timezone= で確認できる。
ENV TZ=Asia/Tokyo

LABEL org.opencontainers.image.source="https://github.com/yendo/famifo-proto"
LABEL org.opencontainers.image.revision="${VERSION}"
LABEL org.opencontainers.image.description="Photo gallery for a home LAN"

USER ${UID}:${GID}

# HEALTHCHECK は付けない。scratch にはシェルも curl も無いので、exec 形式で
# 動かすには famifo 自身にヘルスチェック用のフラグを実装することになる。
# しかも Docker は unhealthy なコンテナを再起動しない（それは Swarm の機能）。
# 得られるのは docker ps や DSM の画面での表示だけなので、割に合わない。
#checkov:skip=CKV_DOCKER_2:scratch has no shell; needs an app-side flag
# Trivy の DS-0026 は .trivyignore で抑止している。ここに書いても効かない。

EXPOSE 8080

# 既定はマウントだけで動く形。写真は /photos の下に、データは /data に置く。
#
#   ssh nas 'sudo mkdir -p /volume1/famifo/data && sudo chown 1029:100 /volume1/famifo/data'
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
