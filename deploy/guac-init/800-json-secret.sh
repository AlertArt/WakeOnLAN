#
# 把 wold 与 Guacamole 共享的密钥写进 guacamole.properties。
#
# 为什么用脚本生成，而不是让用户维护两份配置：
# 两边必须用同一把密钥，分两处写迟早出现「改了一边」的情况，
# 症状是浏览器里只显示 "Permission denied"，日志里也只有一句 invalid credentials。
# 这里让密钥只在 .env 里出现一次（WOL_GUAC_SECRET），容器启动时落到 Guacamole 的配置目录。
#
# 挂载位置是 /opt/guacamole/entrypoint.d/800-json-secret.sh：
# 序号必须大于 100——镜像的 100-generate-guacamole-home.sh 会把 GUACAMOLE_HOME
# 指到本次运行的临时目录（可写），800 在这个之后执行才写得进去。
# 本文件由入口脚本以 source 方式加载，因此不能 set -e。
#
if [ -z "${JSON_SECRET_KEY}" ]; then
    echo "[wol] 错误：JSON_SECRET_KEY 为空。请在 .env 设置 WOL_GUAC_SECRET 后，用 --profile rdp 启动。" >&2
    exit 1
fi

if ! printf '%s' "${JSON_SECRET_KEY}" | grep -Eq '^[0-9a-fA-F]{32}$'; then
    echo "[wol] 错误：WOL_GUAC_SECRET 必须是 32 位十六进制（16 字节），当前长度 ${#JSON_SECRET_KEY}。" >&2
    exit 1
fi

printf 'json-secret-key: %s\n' "${JSON_SECRET_KEY}" > "${GUACAMOLE_HOME}/guacamole.properties"
echo "[wol] 已写入 Guacamole 的 JSON 密钥（json-secret-key）"
