# ---- 构建阶段（目标平台 linux/amd64）----
FROM golang:1.23-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS=-mod=mod
COPY go.mod ./
COPY main.go config.go http_util.go speed.go api.go isp.go node.go ./
COPY web ./web
RUN go build -trimpath -ldflags "-s -w" -o /out/speedtest .

# ---- 运行阶段 ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app \
    && mkdir -p /data \
    && chown -R app:app /data
WORKDIR /app
COPY --from=build /out/speedtest /usr/local/bin/speedtest
USER app
ENV DATA_DIR=/data \
    HTTP_HOST=127.0.0.1 \
    HTTP_PORT=3333 \
    TZ=Asia/Shanghai
EXPOSE 3333
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=4s --start-period=5s --retries=3 \
    CMD wget -q -O- "http://127.0.0.1:${HTTP_PORT:-3333}/healthz" >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/speedtest"]
