#!/bin/sh
# 自愈权限的入口脚本。
#
# 背景：NAS 上用图形界面部署时，/data 挂载的宿主目录属主不受控
#（往往是 root 或管理员的 uid），非 root 进程写不进去，日志表现为
# "unable to open database file"，而用户未必有条件在宿主上执行 chown。
#
# 方案：默认以 root 启动，先修好数据目录属主，再用 setpriv 降到
# uid/gid 10001 运行主进程。setpriv 属 util-linux，debian-slim 自带，
# 不引入额外依赖；最终业务进程仍以非 root 运行，root 只存在于启动瞬间。

set -eu

DATA_DIR="${WOL_DATA_DIR:-/data}"

if [ "$(id -u)" = "0" ]; then
    mkdir -p "$DATA_DIR"

    # 目录属主已是 10001 时跳过递归 chown，避免每次重启都全量扫描。
    owner="$(stat -c '%u' "$DATA_DIR" 2>/dev/null || true)"
    if [ "$owner" != "10001" ]; then
        echo "entrypoint: 修正数据目录属主 $DATA_DIR -> 10001:10001（原属主 uid=${owner:-未知}）"
        chown -R 10001:10001 "$DATA_DIR"
    fi

    exec setpriv --reuid=10001 --regid=10001 --clear-groups /app/wold "$@"
fi

# 非 root 启动（部署方显式覆盖了用户）：权限只能听宿主的，直接运行。
exec /app/wold "$@"
