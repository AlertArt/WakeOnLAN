#!/usr/bin/env bash
# E7 一键验收：按 docs/E7-contract.md 第八节逐项执行 7-1 ~ 7-8。
#
# 用法：
#   cd deploy
#   cp .env.example .env && ${EDITOR:-vi} .env      # 改掉口令
#   ./verify-e7.sh
#
# 退出码：0 = 全部通过（SKIP 不算失败），1 = 存在失败项或环境不满足。
# 注意：本脚本会执行 `docker compose down -v` 验证"数据确实在卷上"，**会清空测试数据**，
# 只应在测试环境运行。

set -uo pipefail

cd "$(dirname "$0")" || exit 1

IMAGE="wol:1.0.0"
CONTAINER="wold"

PASS=0
FAIL=0
SKIP=0
declare -a SUMMARY

green() { printf '\033[32m%s\033[0m' "$1"; }
red() { printf '\033[31m%s\033[0m' "$1"; }
yellow() { printf '\033[33m%s\033[0m' "$1"; }

ok() { PASS=$((PASS + 1)); SUMMARY+=("PASS  $1"); printf '  %s  %s\n' "$(green PASS)" "$1"; }
bad() { FAIL=$((FAIL + 1)); SUMMARY+=("FAIL  $1"); printf '  %s  %s\n' "$(red FAIL)" "$1"; }
skipped() { SKIP=$((SKIP + 1)); SUMMARY+=("SKIP  $1"); printf '  %s  %s\n' "$(yellow SKIP)" "$1"; }

section() { printf '\n\033[1m== %s ==\033[0m\n' "$1"; }

die() {
    printf '\n%s %s\n' "$(red '[中止]')" "$1"
    exit 1
}

# ---------------------------------------------------------------- 前置检查

section "前置检查"

command -v docker >/dev/null 2>&1 || die "未找到 docker 命令。本环节的 7-1~7-8 必须有 Docker。"
docker info >/dev/null 2>&1 || die "Docker 守护进程不可用（docker info 失败）。"
docker compose version >/dev/null 2>&1 || die "未安装 docker compose 插件（docker compose version 失败）。"

[ -f .env ] || die ".env 不存在。先执行：cp .env.example .env 并把 WOL_ADMIN_PASSWORD 改成真实口令。"

set -a
# shellcheck disable=SC1091
. ./.env
set +a

[ -n "${WOL_ADMIN_PASSWORD:-}" ] || die ".env 中 WOL_ADMIN_PASSWORD 为空。"
case "$WOL_ADMIN_PASSWORD" in
    change-me*) die ".env 里还是占位口令，先改成真实口令再验收（否则 7-5 的口令检索没有意义）。" ;;
esac

BASE="http://127.0.0.1:${WOL_PORT:-8080}"
DEVICE_NAME="E7-验收-$(date +%H%M%S)"

printf '  镜像名：%s\n' "$IMAGE"
printf '  接口地址：%s\n' "$BASE"
printf '  测试设备名：%s\n' "$DEVICE_NAME"

wait_health() {
    local i state
    for i in $(seq 1 60); do
        state=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$CONTAINER" 2>/dev/null || echo missing)
        case "$state" in
            healthy) printf '%s' "$state"; return 0 ;;
            unhealthy) printf '%s' "$state"; return 1 ;;
        esac
        sleep 2
    done
    printf 'timeout'
    return 1
}

http_code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

login_token() {
    curl -s -X POST "$BASE/api/login" \
        -H 'Content-Type: application/json' \
        --data-binary "{\"password\":\"$WOL_ADMIN_PASSWORD\"}" \
        | sed -n 's/.*"token":"\([^"]*\)".*/\1/p'
}

# ---------------------------------------------------------------- 7-1

section "7-1 干净环境一条命令启动，健康检查转健康"

if docker compose up -d --build >/tmp/e7-up.log 2>&1; then
    printf '  docker compose up -d --build 成功\n'
else
    tail -20 /tmp/e7-up.log
    bad "7-1 构建或启动失败（见上面日志），后续项不再执行"
    docker compose down -v >/dev/null 2>&1
    exit 1
fi

if health=$(wait_health); then
    ok "7-1 健康检查 healthy（docker inspect .State.Health.Status=$health）"
else
    docker compose logs --tail 30
    bad "7-1 健康检查未转 healthy（实际：$health）"
fi

case "$(docker inspect -f '{{.Config.User}}' "$IMAGE" 2>/dev/null)" in
    10001:10001) ok "附加：镜像以非 root 运行（Config.User=10001:10001）" ;;
    *) bad "附加：镜像未声明非 root 用户（Config.User=$(docker inspect -f '{{.Config.User}}' "$IMAGE" 2>/dev/null)）" ;;
esac

if docker run --rm --entrypoint sh "$IMAGE" -c 'command -v ping' >/dev/null 2>&1; then
    ok "附加：运行层内含 ping（E3 的 ping 类校验依赖它）"
else
    bad "附加：运行层内没有 ping，ping 类校验会直接判 failed"
fi

# ---------------------------------------------------------------- 7-2

section "7-2 重启容器后数据仍在（卷挂载生效）"

TOKEN=$(login_token)
if [ -n "$TOKEN" ]; then
    ok "附加：登录成功（令牌 ${#TOKEN} 字符）"
else
    bad "附加：登录失败，后续接口项无法验证"
fi

CREATE_CODE=$(http_code -X POST "$BASE/api/devices" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$DEVICE_NAME\",\"mac\":\"AA:BB:CC:DD:EE:77\",\"broadcast_addr\":\"127.0.0.1\",\"broadcast_port\":9,\"probe_type\":\"none\",\"probe_value\":\"\"}")

if [ "$CREATE_CODE" = "201" ]; then
    ok "附加：创建设备返回 201"
else
    bad "附加：创建设备返回 $CREATE_CODE（期望 201）"
fi

docker compose restart >/dev/null 2>&1
wait_health >/dev/null

if curl -s "$BASE/api/devices" -H "Authorization: Bearer $TOKEN" | grep -qF "$DEVICE_NAME"; then
    ok "7-2 重启后设备仍在（$DEVICE_NAME 可查到）"
else
    bad "7-2 重启后设备丢失，卷未生效或数据写进了容器层"
fi

# ---------------------------------------------------------------- 7-3

section "7-3 删除容器后重建数据仍在；删卷后数据消失（反证）"

docker compose down >/dev/null 2>&1
docker compose up -d >/dev/null 2>&1
wait_health >/dev/null

if curl -s "$BASE/api/devices" -H "Authorization: Bearer $TOKEN" | grep -qF "$DEVICE_NAME"; then
    ok "7-3a 删除并重建容器后设备仍在（数据在卷上，不在容器层）"
else
    bad "7-3a 重建容器后设备丢失"
fi

docker compose down -v >/dev/null 2>&1
docker compose up -d >/dev/null 2>&1
wait_health >/dev/null

TOKEN=$(login_token)
COUNT=$(curl -s "$BASE/api/devices" -H "Authorization: Bearer $TOKEN" | grep -o '"id"' | wc -l | tr -d ' ')
if [ "$COUNT" = "0" ]; then
    ok "7-3b 删除卷后设备清零（反证数据确实在 wol-data 卷上，而非别处）"
else
    bad "7-3b 删除卷后仍有 $COUNT 台设备，数据落点不受控"
fi

# ---------------------------------------------------------------- 7-4

section "7-4 镜像体积与工具链残留"

SIZE_BYTES=$(docker image inspect -f '{{.Size}}' "$IMAGE" 2>/dev/null | tr -d '\r')
if [ -n "$SIZE_BYTES" ]; then
    SIZE_MB=$((SIZE_BYTES / 1024 / 1024))
    printf '  镜像体积：%s MB（%s 字节，来源 docker image inspect --format {{.Size}}）\n' "$SIZE_MB" "$SIZE_BYTES"
    if [ "$SIZE_MB" -le 200 ]; then
        ok "7-4 体积 ${SIZE_MB}MB ≤ 200MB"
    else
        bad "7-4 体积 ${SIZE_MB}MB 超过 200MB"
    fi
else
    bad "7-4 读不到镜像体积（镜像 $IMAGE 不存在？）"
fi

TOOLS=$(docker run --rm --entrypoint sh "$IMAGE" -c 'command -v go gcc make git 2>/dev/null' 2>/dev/null)
if [ -z "$TOOLS" ]; then
    ok "7-4 无编译工具链残留（go/gcc/make/git 均不在镜像内）"
else
    bad "7-4 镜像内残留工具链：$TOOLS"
fi

# ---------------------------------------------------------------- 7-5

section "7-5 镜像内无 .env、密钥、源码仓库"

LEFTOVER=$(docker run --rm --entrypoint sh "$IMAGE" -c \
    'find / -xdev \( -name ".env" -o -name ".env.*" -o -name ".git" -o -name "*.db" -o -name "go.mod" -o -name "*.go" \) 2>/dev/null | head -5' 2>/dev/null)
if [ -z "$LEFTOVER" ]; then
    ok "7-5 镜像内无 .env / .git / 数据库 / Go 源码"
else
    bad "7-5 镜像内命中残留：$(printf '%s' "$LEFTOVER" | tr '\n' ' ')"
fi

HISTORY_HITS=$(docker history --no-trunc --format '{{.CreatedBy}}' "$IMAGE" 2>/dev/null | grep -cF -- "$WOL_ADMIN_PASSWORD" || true)
ENV_HITS=$(docker inspect -f '{{json .Config.Env}}' "$IMAGE" 2>/dev/null | grep -cF -- "$WOL_ADMIN_PASSWORD" || true)
if [ "${HISTORY_HITS:-0}" = "0" ] && [ "${ENV_HITS:-0}" = "0" ]; then
    ok "7-5 口令明文在镜像层与镜像环境变量中命中 0 次"
else
    bad "7-5 口令明文命中：docker history=$HISTORY_HITS 次，Config.Env=$ENV_HITS 次"
fi

# ---------------------------------------------------------------- 7-6

section "7-6 arm64 构建"

if docker buildx version >/dev/null 2>&1; then
    if docker buildx build --platform linux/arm64 -f ../Dockerfile .. >/tmp/e7-arm64.log 2>&1; then
        ok "7-6 linux/arm64 交叉构建成功（未在 arm64 实机启动）"
    else
        tail -15 /tmp/e7-arm64.log
        bad "7-6 linux/arm64 构建失败（见上面日志）"
    fi
else
    skipped "7-6 本机无 docker buildx，无法验证 arm64 构建"
fi

# ---------------------------------------------------------------- 7-7

section "7-7 编排文件只含一个服务"

SERVICES=$(docker compose config --services 2>/dev/null)
SERVICE_COUNT=$(printf '%s\n' "$SERVICES" | grep -c . || true)
printf '  服务列表：%s\n' "$(printf '%s' "$SERVICES" | tr '\n' ' ')"
if [ "$SERVICE_COUNT" = "1" ]; then
    ok "7-7 服务数为 1（无数据库、缓存、反向代理容器）"
else
    bad "7-7 服务数为 $SERVICE_COUNT，超出单容器交付范围"
fi

# ---------------------------------------------------------------- 7-8

section "7-8 网络模式（广播能否发往物理网段的前提）"

NETMODE=$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$CONTAINER" 2>/dev/null)
if [ "$NETMODE" = "host" ]; then
    ok "7-8 容器以 host 网络运行（HostConfig.NetworkMode=host）"
else
    bad "7-8 网络模式为 $NETMODE，广播包到不了物理网段，唤醒会静默失败"
fi

if docker inspect -f '{{json .HostConfig.PortBindings}}' "$CONTAINER" 2>/dev/null | grep -q 'null'; then
    ok "附加：host 网络下未做端口映射（直接占用宿主端口）"
fi

# ---------------------------------------------------------------- 汇总

section "汇总"

printf '  PASS %d / FAIL %d / SKIP %d\n\n' "$PASS" "$FAIL" "$SKIP"
for line in "${SUMMARY[@]}"; do
    printf '  %s\n' "$line"
done

printf '\n容器仍在运行（数据为空库）。清理：docker compose down -v\n'
printf '真实开机验证（7-8 的最终确认）需真实目标机，归 E8 端到端演练。\n'

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
