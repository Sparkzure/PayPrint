# 多阶段构建。前端与 Go 编译固定在构建机的原生平台（--platform=$BUILDPLATFORM），
# 只交叉编译出目标平台的二进制（CGO 始终关闭、依赖纯 Go），因此 buildx 出多架构镜像
# 不需要 QEMU 里跑 npm / go，amd64 / arm64 / armv7 都快。
#
# 前端不再由本仓库构建：从 PayPrint-Frontend 的 GitHub Release 下载打好包的 dist
# （WEB_VERSION=latest 或指定 tag，如 v0.1.2）。本地构建：docker build -t payprint .
# 运行时行为见 docker-compose.yml 与 entrypoint.sh；交付物仍是同一个单二进制，
# Docker 只是它的运行封装，配置模型不变（payprint.conf，首启自动生成）。

# ---- 1. 前端产物（从 PayPrint-Frontend 的 Release 下载）---------------------
FROM --platform=$BUILDPLATFORM alpine:3.20 AS webdist
ARG WEB_VERSION=latest
RUN apk add --no-cache curl tar && \
    if [ "$WEB_VERSION" = "latest" ]; then \
      TAG="$(curl -fsSL https://api.github.com/repos/Sparkzure/PayPrint-Frontend/releases/latest \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"; \
    else \
      TAG="$WEB_VERSION"; \
    fi && \
    [ -n "$TAG" ] || { echo "error: cannot resolve a PayPrint-Frontend release (set WEB_VERSION?)" >&2; exit 1; } && \
    echo "web dist: $TAG" && \
    curl -fsSL -o /tmp/web.tar.gz \
      "https://github.com/Sparkzure/PayPrint-Frontend/releases/download/${TAG}/payprint-web-${TAG}.tar.gz" && \
    mkdir -p /webdist && tar -xzf /tmp/web.tar.gz -C /webdist

# ---- 2. 后端（在构建机上交叉编译目标平台）----------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS backend
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILT_AT=unknown
WORKDIR /app/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
# 前端产物放进 go:embed 目录（与本地 scripts/build.sh 的取产物步骤等价）。
COPY --from=webdist /webdist/ /app/server/internal/webui/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION -X main.gitCommit=$GIT_COMMIT -X main.builtAt=$BUILT_AT" \
    -o /out/payprint-server ./cmd/server

# ---- 3. 运行时 -------------------------------------------------------------
# ca-certificates 给官方查单的 HTTPS；tzdata 给"在线时段"的本地时间判定（TZ 运行时注入）。
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 1001 payprint && \
    adduser -D -u 1001 -G payprint payprint && \
    mkdir -p /opt/payprint/data && \
    chown -R payprint:payprint /opt/payprint
WORKDIR /opt/payprint
COPY --from=backend --chown=payprint:payprint /out/payprint-server ./payprint-server
COPY --chmod=755 entrypoint.sh /entrypoint.sh
USER payprint
ENV UMASK=022
# 配置与数据都在这个卷里（entrypoint 会把配置软链进去），备份它 = 备份整站。
VOLUME /opt/payprint/data
EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
