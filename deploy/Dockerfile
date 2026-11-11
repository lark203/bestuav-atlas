# atlas-sync 的镜像：多阶段构建，最终只留一个静态二进制。
#
# 为什么 CGO_ENABLED=0 能成立：SQLite 用的是 modernc.org/sqlite（纯 Go 实现），
# 所以不需要 gcc，也不需要 alpine 里的 libc 兼容层。
FROM golang:1.27-alpine AS build

WORKDIR /src
# 先只拷依赖清单：改业务代码时这一层还能命中缓存，不用重下模块。
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/atlas-sync ./cmd/atlas-sync

FROM alpine:3.22

# 非 root 运行：同步服务只读写 /data，没有别的事要做。
RUN adduser -D -u 10001 atlas && mkdir -p /data && chown atlas /data
COPY --from=build /out/atlas-sync /usr/local/bin/atlas-sync

USER atlas
WORKDIR /data
EXPOSE 7420
ENV ATLAS_SYNC_DB=/data/atlas-sync.db \
    ATLAS_SYNC_LISTEN=:7420
ENTRYPOINT ["/usr/local/bin/atlas-sync"]
