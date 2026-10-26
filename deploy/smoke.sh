#!/usr/bin/env bash
# 对「已经跑起来的」服务做一键冒烟验收：不构建、不清数据、不需要 Docker。
#
# 与 verify-e7.sh 的分工：
#   verify-e7.sh  验镜像与编排（会 docker compose down -v 清卷，只能在构建机跑）
#   smoke.sh      验部署后的服务（健康、登录、增删设备、发唤醒包、在线校验），
#                 全程只碰自己建的临时设备，跑完删除，不碰既有数据。
#
# 用法：
#   ./smoke.sh --host 192.168.1.10                              # 健康 + 登录 + 设备列表
#   ./smoke.sh --host 192.168.1.10 --mac AA:BB:CC:DD:EE:FF      # 再加：建/删设备 + 发唤醒包
#   ./smoke.sh --host 192.168.1.10 --mac AA:BB:CC:DD:EE:FF --probe tcp:192.168.1.20:445
#   ./smoke.sh --host 192.168.1.10 --mac AA:BB:CC:DD:EE:FF --probe ping:192.168.1.20
#   ./smoke.sh --host 192.168.1.10 --mac AA:BB:CC:DD:EE:FF --probe heartbeat   # 验心跳链路
#
# 口令取自 --password 或环境变量 WOL_ADMIN_PASSWORD；不写入任何文件。
# 退出码：0 = 全部通过，1 = 有失败项。

set -uo pipefail

HOST=""
PORT="8080"
PASSWORD="${WOL_ADMIN_PASSWORD:-}"
MAC=""
BROADCAST=""
PROBE="none"
CHECK_WAIT=150

CURL=(curl -sS --noproxy '*' --max-time 15)

PASS=0
FAIL=0
declare -a SUMMARY
TMP_ID=""

green() { printf '\033[32m%s\033[0m' "$1"; }
red() { printf '\033[31m%s\033[0m' "$1"; }
section() { printf '\n\033[1m== %s ==\033[0m\n' "$1"; }
ok() { PASS=$((PASS + 1)); SUMMARY+=("PASS  $1"); printf '  %s  %s\n' "$(green PASS)" "$1"; }
bad() { FAIL=$((FAIL + 1)); SUMMARY+=("FAIL  $1"); printf '  %s  %s\n' "$(red FAIL)" "$1"; }
die() { printf '\n%s %s\n' "$(red '[中止]')" "$1"; exit 1; }

usage() { sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
    case "$1" in
        --host)      HOST="${2:-}"; shift 2 ;;
        --host=*)    HOST="${1#*=}"; shift ;;
        --port)      PORT="${2:-}"; shift 2 ;;
        --port=*)    PORT="${1#*=}"; shift ;;
        --password)  PASSWORD="${2:-}"; shift 2 ;;
        --password=*) PASSWORD="${1#*=}"; shift ;;
        --mac)       MAC="${2:-}"; shift 2 ;;
        --mac=*)     MAC="${1#*=}"; shift ;;
        --broadcast) BROADCAST="${2:-}"; shift 2 ;;
        --broadcast=*) BROADCAST="${1#*=}"; shift ;;
        --probe)     PROBE="${2:-}"; shift 2 ;;
        --probe=*)   PROBE="${1#*=}"; shift ;;
        --wait)      CHECK_WAIT="${2:-}"; shift 2 ;;
        --wait=*)    CHECK_WAIT="${1#*=}"; shift ;;
        -h|--help)   usage; exit 0 ;;
        *) echo "未知参数：$1" >&2; usage; exit 2 ;;
    esac
done

[ -n "$HOST" ] || die "必须指定 --host（NAS 或服务器的局域网 IP）"
command -v curl >/dev/null 2>&1 || die "找不到 curl。Windows 可用 Git Bash 自带的 curl。"
[ -n "$PASSWORD" ] || die "口令未提供：用 --password 或先 export WOL_ADMIN_PASSWORD=..."

# 去掉可能的 http:// 前缀与结尾斜杠，允许多种写法
HOST="${HOST#http://}"; HOST="${HOST#https://}"; HOST="${HOST%%/*}"
case "$HOST" in
    *:*) BASE="http://$HOST" ;;          # 已带端口（如 192.168.1.10:8080）
    *)   BASE="http://$HOST:$PORT" ;;
esac

# 由 host 推 /24 广播地址：192.168.1.10 -> 192.168.1.255
if [ -z "$BROADCAST" ] && [ -n "$MAC" ]; then
    BROADCAST="$(printf '%s' "$HOST" | awk -F. 'NF==4{print $1"."$2"."$3".255"}')"
fi

json_str() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p" | head -1; }
json_num() { sed -n "s/.*\"$1\":\([0-9]*\).*/\1/p" | head -1; }

cleanup() {
    if [ -n "$TMP_ID" ]; then
        "${CURL[@]}" -o /dev/null -X DELETE "$BASE/api/devices/$TMP_ID" -H "Authorization: Bearer $TOKEN"
    fi
}
TOKEN=""
trap cleanup EXIT

printf '  服务：%s\n' "$BASE"
[ -n "$MAC" ] && printf '  设备：%s  广播：%s  校验：%s\n' "$MAC" "${BROADCAST:-未指定}" "$PROBE"

# ---------------------------------------------------------------- 1 健康

section "1 健康与数据库连通"

HEALTH_BODY="$("${CURL[@]}" "$BASE/api/health")"
CODE="$(printf '%s' "$HEALTH_BODY" | grep -c '"status":"ok"')"
if [ "$CODE" = "1" ]; then
    ok "健康检查 /api/health 返回 ok（$HEALTH_BODY）"
else
    bad "健康检查异常：${HEALTH_BODY:-无响应}"
    printf '\n  服务没起来或地址不对。先看容器日志： docker logs wold --tail 30\n'
    exit 1
fi

# ---------------------------------------------------------------- 2 登录

section "2 登录与鉴权"

TOKEN="$("${CURL[@]}" -X POST "$BASE/api/login" \
    -H 'Content-Type: application/json' \
    --data-binary "{\"password\":\"$PASSWORD\"}" | json_str token)"

if [ -n "$TOKEN" ]; then
    ok "登录成功（令牌 ${#TOKEN} 字符）"
else
    bad "登录失败：口令不对，或服务未设置 WOL_ADMIN_PASSWORD"
    exit 1
fi

NOAUTH="$("${CURL[@]}" -o /dev/null -w '%{http_code}' "$BASE/api/devices")"
if [ "$NOAUTH" = "401" ]; then
    ok "无令牌访问设备列表被拒（401）"
else
    bad "无令牌访问设备列表返回 $NOAUTH（期望 401）"
fi

# ---------------------------------------------------------------- 3 设备列表

section "3 既有设备"

DEVICES="$("${CURL[@]}" "$BASE/api/devices" -H "Authorization: Bearer $TOKEN")"
COUNT="$(printf '%s' "$DEVICES" | grep -o '"id"' | wc -l | tr -d ' ')"
ok "设备列表可读，现有 $COUNT 台（本脚本不会改动它们）"

[ -n "$MAC" ] || {
    printf '\n提示：加 --mac AA:BB:CC:DD:EE:FF 可继续验证建/删设备、发唤醒包与在线校验。\n'
    printf '\n%s\n' "汇总：PASS $PASS / FAIL $FAIL"
    [ "$FAIL" -eq 0 ] || exit 1
    exit 0
}

# ---------------------------------------------------------------- 4 建临时设备

section "4 建临时设备并删除（用临时数据，不动既有设备）"

PROBE_TYPE="$PROBE"; PROBE_VALUE=""
case "$PROBE" in
    none) ;;
    heartbeat) PROBE_TYPE="heartbeat"; PROBE_VALUE="smoke-$(date +%s)-$RANDOM$RANDOM" ;;
    ping:*|tcp:*) PROBE_TYPE="${PROBE%%:*}"; PROBE_VALUE="${PROBE#*:}" ;;
    *) die "--probe 只支持 none / ping:IP / tcp:主机:端口 / heartbeat，收到：$PROBE" ;;
esac

NAME="SMOKE-$(date +%m%d-%H%M%S)"
CREATE="$(printf '%s' "$("${CURL[@]}" -X POST "$BASE/api/devices" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$NAME\",\"mac\":\"$MAC\",\"broadcast_addr\":\"$BROADCAST\",\"broadcast_port\":9,\"probe_type\":\"$PROBE_TYPE\",\"probe_value\":\"$PROBE_VALUE\"}")")"
TMP_ID="$(printf '%s' "$CREATE" | json_num id)"

if [ -n "$TMP_ID" ]; then
    ok "创建设备成功（id=$TMP_ID，name=$NAME）"
else
    bad "创建设备失败：${CREATE:-无响应}"
    exit 1
fi

# ---------------------------------------------------------------- 5 唤醒

section "5 发唤醒包（魔术包，走 UDP 广播）"

WAKE_CODE="$("${CURL[@]}" -o /tmp/smoke-wake.json -w '%{http_code}' \
    -X POST "$BASE/api/devices/$TMP_ID/wake" -H "Authorization: Bearer $TOKEN")"
WAKE_BODY="$(cat /tmp/smoke-wake.json 2>/dev/null)"

if [ "$WAKE_CODE" = "202" ] && printf '%s' "$WAKE_BODY" | grep -q '"sent":true'; then
    ok "唤醒接口返回 202 且 sent=true（$WAKE_BODY）"
else
    bad "唤醒失败：HTTP $WAKE_CODE，响应 ${WAKE_BODY:-空}"
fi

# ---------------------------------------------------------------- 6 在线校验

section "6 在线校验（可选）"

if [ "$PROBE_TYPE" = "heartbeat" ]; then
    # 心跳模式下服务端不主动探测，这里模拟客户端上报一次。
    HB_CODE="$("${CURL[@]}" -o /tmp/smoke-hb.json -w '%{http_code}' \
        -X POST "$BASE/api/heartbeat" -H 'Content-Type: application/json' \
        -d "{\"mac\":\"$MAC\",\"secret\":\"$PROBE_VALUE\"}")"
    HB_BODY="$(cat /tmp/smoke-hb.json 2>/dev/null)"
    if [ "$HB_CODE" = "200" ] && printf '%s' "$HB_BODY" | grep -q '"status":"online"'; then
        ok "心跳上报被接受（HTTP 200，$HB_BODY）"
    else
        bad "心跳上报失败：HTTP $HB_CODE，响应 ${HB_BODY:-空}"
    fi

    # 再确认状态接口把该设备算成 online。
    STATE="$("${CURL[@]}" "$BASE/api/status" -H "Authorization: Bearer $TOKEN" \
        | tr '}' '\n' | grep "\"device_id\":$TMP_ID" | json_str status)"
    if [ "$STATE" = "online" ]; then
        ok "状态接口显示 online（心跳链路打通）"
    else
        bad "状态接口显示 ${STATE:-空}，期望 online"
    fi
elif [ "$PROBE_TYPE" = "none" ]; then
    printf '  跳过：校验类型为 none（只发唤醒包，不判断目标是否上线）\n'
    printf '  说明：Win10/11 默认屏蔽入站 ICMP，用 ping 校验会一直"超时未响应"；\n'
    printf '        改用 --probe tcp:192.168.1.20:445（SMB）或 :3389（远程桌面）更可靠。\n'
else
    CHECK_CODE="$("${CURL[@]}" -o /dev/null -w '%{http_code}' \
        -X POST "$BASE/api/devices/$TMP_ID/check" -H "Authorization: Bearer $TOKEN")"
    if [ "$CHECK_CODE" = "202" ]; then
        ok "触发校验返回 202（$PROBE_TYPE:$PROBE_VALUE）"
        printf '  轮询状态，最多 %s 秒（真实唤醒通常 20–60 秒）...\n' "$CHECK_WAIT"
        STATE=""; WAITED=0
        while [ "$WAITED" -lt "$CHECK_WAIT" ]; do
            sleep 3; WAITED=$((WAITED + 3))
            STATE="$("${CURL[@]}" "$BASE/api/status" -H "Authorization: Bearer $TOKEN" \
                | tr '}' '\n' | grep "\"device_id\":$TMP_ID" | json_str status)"
            case "$STATE" in
                online|timeout|failed) break ;;
            esac
            printf '    %ss → %s\n' "$WAITED" "${STATE:-未知}"
        done
        case "$STATE" in
            online)  ok "校验结果 online：目标已上线（$PROBE_TYPE:$PROBE_VALUE）" ;;
            timeout) bad "校验结果 timeout：$CHECK_WAIT 秒内 $PROBE_TYPE:$PROBE_VALUE 无响应" ;;
            failed)  bad "校验结果 failed：探测无法执行（内网工具缺失或目标配置非法）" ;;
            *)       bad "等待 $CHECK_WAIT 秒未得到终态（最后状态：${STATE:-无}）" ;;
        esac
    else
        bad "触发校验返回 $CHECK_CODE（期望 202）"
    fi
fi

# ---------------------------------------------------------------- 7 清理

section "7 删除临时设备"

DEL_CODE="$("${CURL[@]}" -o /dev/null -w '%{http_code}' \
    -X DELETE "$BASE/api/devices/$TMP_ID" -H "Authorization: Bearer $TOKEN")"
if [ "$DEL_CODE" = "204" ] || [ "$DEL_CODE" = "200" ]; then
    ok "临时设备已删除（HTTP $DEL_CODE）"
    TMP_ID=""
else
    bad "删除临时设备返回 $DEL_CODE，请到界面手动删除 $NAME"
fi

# ---------------------------------------------------------------- 汇总

printf '\n\033[1m== 汇总 ==\033[0m\n'
PASS_LIST=0
for line in "${SUMMARY[@]}"; do
    case "$line" in PASS*) PASS_LIST=$((PASS_LIST + 1)) ;; esac
done
printf '  PASS %s / FAIL %s\n\n' "$PASS_LIST" "$FAIL"
for line in "${SUMMARY[@]}"; do printf '  %s\n' "$line"; done

if [ "$FAIL" -eq 0 ]; then
    printf '\n结论文本：健康、鉴权、增删设备、唤醒包发送均正常'
    [ "$PROBE_TYPE" = "none" ] && printf '（未做在线校验）'
    printf '。\n'
    exit 0
fi
exit 1
