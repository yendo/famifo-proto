FROM golang:1.25 AS build
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

COPY --from=build /famifo /famifo

# 日付の切り出しは time.Local に依存する。タイムゾーンデータベースはバイナリに
# 埋め込んであるが、TZ を渡さないと /etc/localtime を探しに行って失敗し、UTC に
# 落ちる。そのまま初回インデックスを作ると全件が誤った日付で固定される。
# 起動ログの timezone= で確認できる。
ENV TZ=Asia/Tokyo

LABEL org.opencontainers.image.source="https://github.com/yendo/famifo-proto"
LABEL org.opencontainers.image.revision="${VERSION}"
LABEL org.opencontainers.image.description="Photo gallery for a home LAN"

EXPOSE 8080

# 既定はマウントだけで動く形。写真は /photos の下に、データは /data に置く。
#
#   docker run -d --restart unless-stopped -p 8080:8080 \
#     -v /volume1/photo:/photos:ro \
#     -v /volume1/famifo/data:/data \
#     ghcr.io/yendo/famifo-proto

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
