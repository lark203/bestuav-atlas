# 只装预编译的静态二进制：不在服务器上重编，镜像构建 3 秒。
#
# 前提：先用 build.sh 交叉编译出 atlas-sync（GOOS=linux GOARCH=amd64 CGO_ENABLED=0）。
FROM alpine:3.22

# 注意：compose 里会用 `user:` 覆盖成宿主 uid；这里的 atlas(10001) 只是镜像默认值。
RUN adduser -D -u 10001 atlas && mkdir -p /data && chown atlas /data

COPY atlas-sync /usr/local/bin/atlas-sync
USER atlas
WORKDIR /data
EXPOSE 7420
ENTRYPOINT ["/usr/local/bin/atlas-sync"]
