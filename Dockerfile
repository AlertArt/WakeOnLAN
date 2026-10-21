# 多阶段构建：编译阶段带工具链，运行层只留二进制与运行时依赖。
# 镜像规格与限制见 docs/E7-contract.md。

FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS build

WORKDIR /src

# 先只拷贝依赖清单，改代码时不必重下模块。
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# buildx 注入的目标平台；CGO 关闭后 modernc.org/sqlite 可静态编译到任意架构。
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/wold ./cmd/wold

# 运行层：iputils-ping 供 E3 的 ping 类校验（缺失时该类型校验会直接判失败），
# curl 只供 HEALTHCHECK 使用。
FROM debian:bookworm-slim

# 默认用官方源。deb.debian.org 在国内网络下常返回 502（代理拦截或链路超时），
# 此时用 --build-arg APT_MIRROR=mirrors.aliyun.com 切换，不必改文件。
# bookworm 的 apt 源是 deb822 格式的 /etc/apt/sources.list.d/debian.sources，
# 没有 /etc/apt/sources.list；这里对两种布局都做替换，兼容将来的基础镜像。
ARG APT_MIRROR=""
RUN if [ -n "$APT_MIRROR" ]; then \
        for f in /etc/apt/sources.list /etc/apt/sources.list.d/*.sources; do \
            [ -f "$f" ] && sed -i "s|deb.debian.org|$APT_MIRROR|g" "$f"; \
        done; \
    fi \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        iputils-ping \
        curl \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# 固定 uid/gid：bind mount 时宿主可据此对齐；命名卷空卷也按它初始化。
RUN groupadd --gid 10001 wol \
    && useradd --uid 10001 --gid 10001 --shell /usr/sbin/nologin --no-create-home wol \
    && mkdir -p /app /data \
    && chown 10001:10001 /data

WORKDIR /app

COPY --from=build /out/wold /app/wold
COPY --chown=10001:10001 web/ /app/web/

# 只预设路径与端口；口令必须由运行时注入，绝不能出现在镜像层里。
ENV WOL_DATA_DIR=/data \
    WOL_PORT=8080

VOLUME ["/data"]
EXPOSE 8080

# 入口脚本负责：root 启动 → 修正 /data 属主 → setpriv 降权到 10001 运行。
# 因此这里不再声明 USER 10001：容器必须以 root 起步才能自愈权限，
# 业务进程的实际 uid 仍是 10001（见 docker top 的 UID 列）。
# --chmod 需要 BuildKit（Docker Desktop 自带）；纯旧版构建器请改用 RUN chmod。
COPY --chmod=0755 docker/entrypoint.sh /usr/local/bin/entrypoint.sh

# 健康检查只回答"进程在、DB 连得上"：不鉴权、不写库、不触发探测。
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS "http://127.0.0.1:${WOL_PORT}/api/health" || exit 1

# exec 形式：入口脚本最终 exec 到 wold，PID 1 直接收到 SIGTERM，走 main.go 的优雅关闭。
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
