#!/usr/bin/env bash
# 打包离线镜像：在能联网构建的机器上执行，产出可直接拷进 NAS 的目录。
#
#   ./package-offline.sh                          # 默认 amd64
#   ./package-offline.sh --arch arm64             # ARM 机型
#   ./package-offline.sh --apt-mirror mirrors.aliyun.com   # 国内构建
#   ./package-offline.sh --skip-build             # 复用本地已有镜像
#
# 产物在 dist/：镜像 tar、tar.gz、compose 文件、.env 样例、校验和、安装说明。

set -euo pipefail

VERSION="${VERSION:-1.1.0}"
ARCH="amd64"
APT_MIRROR=""
SKIP_BUILD=0
OUT=""

usage() {
    sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
    case "$1" in
        --arch)        ARCH="${2:-}"; shift 2 ;;
        --arch=*)      ARCH="${1#*=}"; shift ;;
        --apt-mirror)  APT_MIRROR="${2:-}"; shift 2 ;;
        --apt-mirror=*) APT_MIRROR="${1#*=}"; shift ;;
        --skip-build)  SKIP_BUILD=1; shift ;;
        --out)         OUT="${2:-}"; shift 2 ;;
        -h|--help)     usage; exit 0 ;;
        *) echo "未知参数：$1" >&2; usage; exit 2 ;;
    esac
done

case "$ARCH" in
    amd64|arm64) ;;
    *) echo "架构只能是 amd64 或 arm64，收到：$ARCH" >&2; exit 2 ;;
esac

command -v docker >/dev/null 2>&1 || {
    echo "找不到 docker 命令。Windows 上请先把它加入 PATH：" >&2
    echo '  export PATH="/c/Program Files/Docker/Docker/resources/bin:$PATH"' >&2
    exit 1
}

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${OUT:-$ROOT/dist}"
IMAGE="wol:$VERSION"
TAR="wol-${VERSION}-${ARCH}.tar"

mkdir -p "$OUT"

echo "== 镜像 =="
if [ "$SKIP_BUILD" -eq 1 ]; then
    docker image inspect "$IMAGE" >/dev/null 2>&1 || {
        echo "本地没有 $IMAGE，去掉 --skip-build 重新执行。" >&2
        exit 1
    }
    echo "复用本地镜像 $IMAGE"
else
    BUILD_ARGS=(--platform "linux/$ARCH" -t "$IMAGE" --load)
    [ -n "$APT_MIRROR" ] && BUILD_ARGS+=(--build-arg "APT_MIRROR=$APT_MIRROR")
    if docker buildx version >/dev/null 2>&1; then
        docker buildx build "${BUILD_ARGS[@]}" "$ROOT"
    else
        # 没有 buildx 时只能构建本机架构，跨架构会失败，提前说清楚。
        if [ "$ARCH" != "$(docker info --format '{{.Architecture}}' | sed 's/x86_64/amd64/;s/aarch64/arm64/')" ]; then
            echo "没有 buildx，无法构建 $ARCH 镜像；请在对应架构的机器上打包。" >&2
            exit 1
        fi
        docker build -t "$IMAGE" "$ROOT"
    fi
fi

GOT="$(docker image inspect -f '{{.Os}}/{{.Architecture}}' "$IMAGE")"
if [ "$GOT" != "linux/$ARCH" ]; then
    echo "镜像架构不符：期望 linux/$ARCH，实际 $GOT" >&2
    echo "若本地已有同名镜像，先 docker rmi $IMAGE 再重试。" >&2
    exit 1
fi

SIZE="$(docker image inspect -f '{{.Size}}' "$IMAGE")"
echo "镜像 $IMAGE  平台 $GOT  大小 $((SIZE / 1024 / 1024)) MB"

echo "== 导出 =="
rm -f "$OUT/$TAR" "$OUT/$TAR.gz"
# 在产物目录内用相对文件名，避免 Windows 上 docker 收到 MSYS 路径时的转换问题。
(cd "$OUT" && docker save -o "$TAR" "$IMAGE")

# 新版 Docker 在 containerd 存储驱动下导出的是 OCI 布局（blobs/sha256 + index.json），
# 即使带一个 manifest.json，其路径也指向 blobs/，老版本 Docker 仍可能拒绝。
# 这里统一转成经典布局（每层一个目录 + layer.tar），让 tar 在任何版本的 Docker 上都能 load。
if tar -tf "$OUT/$TAR" | grep -qx "index.json"; then
    echo "== 转换 OCI 布局 → docker-archive 布局 =="
    PY="${PYTHON_BIN:-}"
    if [ -z "$PY" ]; then
        for c in python3 python py; do
            if command -v "$c" >/dev/null 2>&1; then PY="$c"; break; fi
        done
    fi
    [ -n "$PY" ] || {
        echo "需要 Python 3 做格式转换（老版本 Docker 不认 OCI 布局）。" >&2
        echo "安装 Python 后重试，或用 PYTHON_BIN=/path/to/python 指定。" >&2
        exit 1
    }
    "$PY" "$ROOT/deploy/oci2docker.py" "$OUT/$TAR" "$OUT/$TAR.docker" "$IMAGE"
    mv "$OUT/$TAR.docker" "$OUT/$TAR"
fi

gzip -c "$OUT/$TAR" > "$OUT/$TAR.gz"

cp "$ROOT/deploy/docker-compose.nas.yml" "$OUT/"
cp "$ROOT/deploy/.env.example" "$OUT/.env.example"
cp "$ROOT/deploy/smoke.sh" "$OUT/smoke.sh"
chmod +x "$OUT/smoke.sh"

# Windows 心跳客户端随包分发：目标机无法访问 GitHub 时也能直接拷过去用。
# GUI 版（Avalonia）需要先 dotnet publish（见 client/README.md），有产物就带上。
mkdir -p "$OUT/client"
cp "$ROOT/client/heartbeat.ps1" "$OUT/client/heartbeat.ps1"
cp "$ROOT/client/README.md" "$OUT/client/README.md"
if [ -f "$ROOT/client/WolHeartbeat/publish/WolHeartbeat.exe" ]; then
    cp "$ROOT/client/WolHeartbeat/publish/WolHeartbeat.exe" "$OUT/client/WolHeartbeat.exe"
    echo "已附带 GUI 客户端 client/WolHeartbeat.exe"
else
    echo "提示：未找到 client/WolHeartbeat/publish/WolHeartbeat.exe，跳过 GUI 客户端（只带脚本版）"
fi

cat > "$OUT/安装说明.txt" <<EOF
局域网唤醒（WakeOnLAN）离线镜像包
版本 $VERSION / 平台 linux/$ARCH

一、拷到 NAS
把本目录下所有文件放进 NAS 的共享目录（SMB / 文件管理上传均可）。

二、导入镜像
图形界面：Docker → 镜像 → 导入镜像 → 选 $TAR
命令行：  docker load -i $TAR        （也可直接用 $TAR.gz）

三、启动（二选一）
1) SSH + compose（推荐，能保证 host 网络）
   cp .env.example .env && vi .env      # 改 WOL_ADMIN_PASSWORD
   docker compose -f docker-compose.nas.yml up -d

2) 图形界面建容器
   镜像 wol:$VERSION，网络模式 host，
   环境变量 WOL_ADMIN_PASSWORD=<口令>、WOL_DATA_DIR=/data，
   存储把容器内 /data 指向一个卷或目录（目录属主无需预处理，
   容器启动时自动修正为 10001:10001）。
   若 8080 被占用，加环境变量 WOL_PORT=<其它端口>。

四、验证
浏览器打开 http://<NAS的局域网IP>:8080，输入口令登录。
另可执行：curl -s http://<NAS的局域网IP>:8080/api/health

五、一键验收（不需要 SSH，也不需要 Docker）
在任意能访问 NAS 的电脑上执行（Windows 用 Git Bash）：
  bash smoke.sh --host <NAS的IP> --password '<口令>'
  bash smoke.sh --host <NAS的IP> --password '<口令>' --mac AA:BB:CC:DD:EE:FF
退出码 0 = 健康、登录、鉴权、建删设备、唤醒包发送全部正常；
脚本只建一台临时设备，跑完自动删除，不动既有设备。

注意：Windows 10/11 默认屏蔽入站 ping，校验别用 ping——
三种可选：TCP（填 目标IP:445 或 目标IP:3389）、心跳上报（见下）、不校验。
（唤醒本身不受目标机防火墙影响，魔术包由网卡直接接收。）

六、Windows 目标机推荐用心跳（无需目标机开放任何端口）
1) 网页里编辑设备 → 校验方式选"心跳上报" → 心跳密钥填 16 位以上随机字符
2) 桌面客户端（推荐）：把 client\WolHeartbeat.exe 拷到目标机如 C:\wol\，双击运行——
   程序收进右下角托盘（可能在 ^ 隐藏区），双击托盘图标打开窗口；
   填服务器地址与密钥，点"自动识别"取 MAC，点"保存并开始上报"即可；
   点 X 只是收进托盘，退出请用托盘右键菜单；勾选"登录 Windows 时自动启动"可开机常驻。
   配置和日志都在 exe 所在目录。
   命令行验证：C:\wol\WolHeartbeat.exe --once --server http://<NAS的IP>:8080 --secret <密钥>
3) 无界面场景（服务器 / 不想装 GUI）：用 client\heartbeat.ps1，先验一次：
   powershell -NoProfile -ExecutionPolicy Bypass -File C:\wol\heartbeat.ps1 ^
     -Server http://<NAS的IP>:8080 -Secret <密钥> -Once
   开机常驻（管理员命令行，开机即跑、无需登录）：
   schtasks /Create /TN "WOL-Heartbeat" /SC ONSTART /RU SYSTEM /RL HIGHEST /F ^
     /TR "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File C:\wol\heartbeat.ps1 -Server http://<NAS的IP>:8080 -Secret <密钥>"
   客户端每 60 秒上报一次，3 分钟没有心跳即判离线；详见 client\README.md。

注意：网络必须是 host 模式，桥接模式下唤醒包发不出局域网。
详细说明见仓库 deploy/README.md。
EOF

echo "== 校验和 =="
(cd "$OUT" && sha256sum "$TAR" "$TAR.gz" docker-compose.nas.yml .env.example smoke.sh client/heartbeat.ps1 $([ -f client/WolHeartbeat.exe ] && echo client/WolHeartbeat.exe) > SHA256SUMS && cat SHA256SUMS)

echo
echo "完成。产物目录：$OUT"
ls -lh "$OUT" | sed '1d'
