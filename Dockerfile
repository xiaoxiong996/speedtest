# ---- 构建阶段（支持 amd64 / arm64，构建时按目标平台自动选择）----
# 版本写死到小版本：Go 的 1.26.11 修了 CVE-2026-97033~97043（cgo 构建期 RCE）
# 与 CVE-2026-97054（net/http 的 MaxBytesReader 并发写头崩溃，本项目上传/面板都用到了）。
# 用 golang:1.26-alpine 这种浮动 tag 会随上游悄悄变化，出问题时无法复现，也不利于审计。
FROM golang:1.26.11-alpine AS build
WORKDIR /src
# TARGETARCH 由 Buildx 注入；普通 docker build 时默认 amd64
ARG TARGETARCH=amd64
ENV CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH}
# 依赖：只有 ACME（Let's Encrypt 自动证书）用到的 x/crypto。
# 先单独拷 go.mod/go.sum 拉依赖，源码改动不会让这层缓存失效。
COPY go.mod go.sum ./
RUN go mod download
COPY main.go config.go http_util.go speed.go api.go isp.go node.go node_acmedns.go node_certstore.go node_ddns.go node_panel.go node_panelcfg.go ./
COPY web ./web
RUN go build -trimpath -ldflags "-s -w" -o /out/speedtest .

# ---- 运行阶段 ----
# Alpine 3.20 已于 2026-04-01 结束支持（EOL），不再接收安全更新：
# 镜像里的 musl 1.2.5-r3 就缺 CVE-2026-107891（regcomp 整数溢出，CVSS 8.1）的补丁。
# 3.24 是当前受支持的最新稳定分支（支持到 2028-06），musl 已是 1.2.6-r3（含修复）。
FROM alpine:3.24
# ca-certificates：ACME 访问 Let's Encrypt 时要校验对端证书；tzdata：日志/报告时区
# 先 apk upgrade：基础镜像发布时冻结的包（如 zlib 1.3.2-r1）会落后于仓库，
# 不升级就会带着已修复的 CVE 一直跑，构建时顺手补齐
RUN apk upgrade --no-cache \
    && apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app \
    && mkdir -p /data /certs \
    && chown -R app:app /data /certs
WORKDIR /app
COPY --from=build /out/speedtest /usr/local/bin/speedtest
USER app
# 注意：这里不再写死 HTTP_HOST。主控模式代码里默认 127.0.0.1（只给反代用），
# 节点模式默认 0.0.0.0（要对外），两者各取所需，避免 docker run 时踩坑。
# 用桥接网络时，compose 里要把 HTTP_HOST 显式设成 0.0.0.0，否则容器外的反代连不上。
ENV DATA_DIR=/data \
    HTTP_PORT=3333 \
    TZ=Asia/Shanghai
# 只声明主控实际用到的端口。节点模式是 host 网络，端口由 NODE_PORT 决定，
# 不需要（也不应该）在这里 EXPOSE 443/8443，否则面板上会显示一堆"没绑定"的端口，误导排查。
EXPOSE 3333
VOLUME ["/data"]
# 健康检查：先 http（明文模式），再 https（自签/自带证书），最后只探 TCP（ACME 模式首次握手要现签证书，
# 容器内没有域名 SNI，wget 会失败，只能退化成端口连通性检查）
HEALTHCHECK --interval=30s --timeout=4s --start-period=5s --retries=3 \
    CMD wget -q -O- "http://127.0.0.1:${HTTP_PORT:-3333}/healthz" >/dev/null 2>&1 || wget -q --no-check-certificate -O- "https://127.0.0.1:${HTTP_PORT:-3333}/healthz" >/dev/null 2>&1 || nc -z 127.0.0.1 "${HTTP_PORT:-3333}" >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/speedtest"]
