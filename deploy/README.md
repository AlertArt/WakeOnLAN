# 部署指南（Docker）

这是一个单容器应用：一个进程、一个 SQLite 文件、一个端口。部署只有两件事容易做错——**网络必须是 host**，**数据目录的属主必须是 10001**。其余步骤照抄即可。

两种部署形态，按机器能否访问 GitHub 选一种：

| 形态 | 适用机器 | 做法 |
| --- | --- | --- |
| A. 源码构建 | 能访问 GitHub 的 Linux 主机 | `git clone` → `docker compose up -d --build` |
| B. 离线镜像 | 极空间等不能访问 GitHub 的 NAS | 在开发机打包 → 拷到 NAS → 导入镜像 |

两种形态跑的是同一个镜像，行为完全一致。

---

## 0. 共同前提

- **部署机必须是 Linux 或 NAS**。Windows / macOS 的 Docker Desktop 不适用（原因见第 6 节）。
- 部署机与要唤醒的设备在**同一个二层局域网**。跨 VLAN、跨网段、走了三层路由的广播都到不了。
- 目标机侧：BIOS 开启 Wake-on-LAN，网卡属性里允许"此设备唤醒计算机"，并且是**有线网卡**（无线网卡基本不支持 WOL）。
- 宿主端口 `8080` 未被占用。极空间的 ZOS 管理界面若占用该端口，改 `WOL_PORT` 即可。

---

## 1. 方式 A：源码构建（Linux，能访问 GitHub）

```bash
git clone https://github.com/AlertArt/WakeOnLAN.git
cd WakeOnLAN/deploy

cp .env.example .env
sed -i "s|change-me-to-a-long-random-string|$(openssl rand -base64 24)|" .env
chmod 600 .env

docker compose up -d --build
docker compose ps          # STATUS 列出现 (healthy) 即就绪
```

浏览器打开 `http://<主机IP>:8080`，输入 `.env` 里的口令。

国内网络若构建失败（`auth.docker.io` 或 `deb.debian.org` 返回 502），见第 7 节。

---

## 2. 方式 B：离线镜像（极空间等无法访问 GitHub 的 NAS）

原理：在有网的机器上构建镜像 → 导出成 tar → 拷进 NAS → 导入 → 用仓库里的运行时 compose 启动。NAS 全程不需要访问 GitHub 或 Docker Hub。

### 2.1 在开发机打包

需要一台装了 Docker 的机器（Windows 的 Docker Desktop 也可以，只能构建、不能部署）。

```bash
cd WakeOnLAN/deploy

# Windows 上 docker 可能不在 PATH 里，先补上：
# export PATH="/c/Program Files/Docker/Docker/resources/bin:$PATH"

# 国内网络加 --apt-mirror，否则 apt 装 ping 会卡在 deb.debian.org
./package-offline.sh --apt-mirror mirrors.aliyun.com

# 已经构建过镜像、只想重新导出：
./package-offline.sh --skip-build
```

产物在仓库根的 `dist/`（已被 `.gitignore` 忽略，不会误提交）：

| 文件 | 用途 |
| --- | --- |
| `wol-1.0.0-amd64.tar` | **NAS 图形界面导入用这个**（未压缩，界面只认 tar） |
| `wol-1.0.0-amd64.tar.gz` | 传输用，体积约为 tar 的 40%，`docker load` 能直接读 |
| `docker-compose.nas.yml` | 运行时编排，只有 `image` 没有 `build` |
| `.env.example` | 配置样例 |
| `SHA256SUMS` | 校验和，传输出错时能立刻发现 |
| `安装说明.txt` | 上面这些内容的精简版，随包一起走 |

两个容易卡住的点，脚本已经处理：

- **镜像格式**。新版 Docker 在 containerd 存储驱动下导出的是 OCI 布局（`blobs/sha256/…`），而 NAS 上较老的 Docker 只认经典布局（`manifest.json` + 每层 `layer.tar`），导入会直接报格式错误。脚本会自动转成经典布局，**这一步需要 Python 3**；Windows 上若找不到，用 `PYTHON_BIN=/c/.../python.exe ./package-offline.sh ...` 指定。
- **架构别选错**。脚本默认 `amd64`。极空间 Z4 / Z4S / Z4Pro / Z423 等 x86 机型用默认值即可；Z2 / Z2S / Z2Pro 这类 ARM 机型加 `--arch arm64`（在 x86 开发机上构建会走 QEMU 模拟，慢但结果可用）。不确定就在 NAS 的 SSH 里执行 `uname -m`：`x86_64` → amd64，`aarch64` → arm64。

### 2.2 传到 NAS

把 `dist/` 下所有文件放进 NAS 的共享目录即可（SMB 挂载、极空间"文件管理"上传、U 盘都行）。约 100 MB（tar）/ 40 MB（tar.gz），随架构与基础镜像浮动。

传输后核对一下，避免半个文件导致导入失败：

```bash
sha256sum -c SHA256SUMS      # 在 NAS 的 SSH 里；没有 sha256sum 就手工比对
```

### 2.3 导入镜像

**图形界面**：Docker → 镜像 → 导入镜像 → 选择 `wol-1.0.0-amd64.tar` → 等待完成，镜像列表出现 `wol:1.0.0`。

**SSH（更快，且能顺手做后面必须做的权限设置）**：

```bash
docker load -i wol-1.0.0-amd64.tar.gz     # docker 能直接读 gzip
docker image ls | grep wol
```

### 2.4 启动

#### 路线一：SSH + compose（推荐）

能保证 `host` 网络和命名卷，两个最容易出错的点都自动正确：

```bash
cp .env.example .env
vi .env                    # 只改 WOL_ADMIN_PASSWORD，16 位以上
docker compose -f docker-compose.nas.yml up -d
docker compose -f docker-compose.nas.yml ps
```

数据落在命名卷 `deploy_wol-data` 上，删容器、重建都不丢。

#### 路线二：纯图形界面建容器

极空间各版本界面用词略有差异，按下面这张表填即可：

| 项 | 填什么 |
| --- | --- |
| 镜像 | `wol:1.0.0` |
| 容器名称 | `wold` |
| 网络模式 | **`host`**（有的版本写作"主机网络"） |
| 环境变量 | `WOL_ADMIN_PASSWORD` = 你的口令；`WOL_DATA_DIR` = `/data` |
| 端口 | **不要映射**（host 模式下端口直接就是宿主的） |
| 存储 | 容器内 `/data` 指向一个目录或卷 |
| 重启策略 | 除非手动停止，否则总是重启 |

**如果网络模式里没有 `host` 选项**，说明这个版本的界面不支持——只能走路线一。用桥接网络启动会得到一个"看起来完全正常、但永远唤不醒设备"的服务：接口返回 202、日志无错误、设备就是不开机。

**关于 `/data` 的属主**（纯界面部署最容易踩的坑）：

容器以非 root 用户 `10001:10001` 运行。若把 `/data` 挂到一个新建的 NAS 目录，该目录默认属于 `root` 或管理员账号，进程写不进去，容器会反复重启，日志里是 `unable to open database file`。

三种解法，按优先级：

```bash
# 1) 用命名卷（最省事，属主由 Docker 自动初始化）
docker volume create wol-data
# 建容器时挂载 wol-data:/data

# 2) 挂宿主目录，先改属主
mkdir -p /vol1/docker/wol/data
chown -R 10001:10001 /vol1/docker/wol/data

# 3) 完全无法改属主时，暂时以 root 运行（不推荐，削弱隔离）
#    在界面里把"用户"设为 root / 0:0
```

### 2.5 验证

```bash
curl -s http://<NAS的局域网IP>:8080/api/health
# {"status":"ok","db":"ok","time":"..."}
```

浏览器打开同一地址，应看到登录页。登录后添加一台设备，`broadcast_addr` 填**所在网段的广播地址**（如 `192.168.1.255`），不是设备 IP。

真正的验收是让目标机关机，点"唤醒"，看它是否启动——这一步只能用真实设备验证（E8）。

---

## 3. 配置项

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `WOL_ADMIN_PASSWORD` | **必填** | 登录口令。为空时进程拒绝启动 |
| `WOL_PORT` | `8080` | 监听端口；host 网络下即宿主端口 |
| `WOL_DATA_DIR` | `/data` | SQLite 所在目录 |
| `WOL_TOKEN_TTL` | `168h` | 令牌有效期，Go duration 写法 |
| `WOL_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

口令是唯一的一道门：host 网络直接占用宿主端口，没有反向代理兜底。用 `openssl rand -base64 24` 生成，不要用有意义的短词。

---

## 4. 备份与升级

```bash
# 备份命名卷
docker run --rm -v deploy_wol-data:/data -v "$PWD":/backup debian:bookworm-slim \
  tar czf /backup/wol-data-$(date +%F).tar.gz -C /data .

# 升级：重新打包镜像 → 导入 → 重建容器，数据卷不动
docker compose -f docker-compose.nas.yml up -d --force-recreate
```

令牌只存在内存里，**容器重启后所有人都要重新登录**，这是设计行为，不是 bug。

---

## 5. 故障排查

| 现象 | 原因与处理 |
| --- | --- |
| 容器反复重启，日志 `unable to open database file` | `/data` 属主不是 10001，见 2.4 |
| 容器反复重启，日志 `listen tcp :8080: bind: address already in use` | 宿主上已有服务或别的容器占用该端口。host 模式没有端口映射可绕，只能改 `WOL_PORT` |
| 容器起不来，日志提示口令为空 | 没注入 `WOL_ADMIN_PASSWORD`；这是有意的保护 |
| `unhealthy` 但进程在跑 | `WOL_PORT` 改过但健康检查仍用旧端口；两处要一致 |
| 页面能开，唤醒返回 202 但设备不开机 | 广播地址填成了设备 IP / 网络不是 host / 目标机未开 WOL |
| 校验直接"失败"，提示找不到 ping | 用了自己改的镜像，丢了 `iputils-ping` |
| 给容器加了 `no-new-privileges: true` 后 ping 全失败 | 该选项剥夺 `cap_net_raw`，不要加 |
| 手机打不开 | 防火墙未放行端口，或手机不在同一网段 |
| 导入镜像报格式错误（找不到 `manifest.json`） | tar 是 OCI 布局，老版本 Docker 不认；用 `package-offline.sh` 重新打包，它负责转换 |
| 导入镜像报格式错误（其实是 `.tar.gz`） | 图形界面只认未压缩的 `.tar`，先 `gunzip` |

---

## 6. 平台限制：为什么必须是 Linux

WOL 是 UDP 广播。Docker 默认桥接网络下，广播包只会落在 `docker0` 虚拟网桥上，永远到不了物理局域网——而接口照样返回 `202`，日志照样没有错误。所以 `network_mode: host` 不是优化项，是功能前提。

Docker Desktop（Windows / macOS）会把 `host` 语义改写到自己的虚拟机里：实测容器内 `127.0.0.1:8080` 通、Windows 宿主的 `127.0.0.1:8080` 和局域网 IP 全不通，浏览器打不开界面；广播包倒是能经 NAT 发到物理网卡。结论是 **Windows 可以构建、不能部署**。

如果只是想在本机看一眼界面：改用桥接 + 端口映射（`-p 8080:8080`，去掉 `network_mode: host`），界面可用，唤醒无效。

---

## 7. 国内网络构建

两处会卡，都是网络而不是配置问题：

```bash
# 1) 基础镜像拉不下来（auth.docker.io 502）：走加速源再打本地标签
docker pull docker.1ms.run/library/golang:1.24-bookworm
docker tag  docker.1ms.run/library/golang:1.24-bookworm golang:1.24-bookworm
docker pull docker.1ms.run/library/debian:bookworm-slim
docker tag  docker.1ms.run/library/debian:bookworm-slim debian:bookworm-slim

# 2) apt 装不上 ping（deb.debian.org 502）：切 Debian 源
docker build --build-arg APT_MIRROR=mirrors.aliyun.com -t wol:1.0.0 .
```

`package-offline.sh` 的 `--apt-mirror` 参数就是给第 2 种情况用的。

---

## 8. 一键验收（可选）

在能部署的 Linux 机器上，`deploy/verify-e7.sh` 会逐项跑完镜像规格检查（健康检查、数据持久化、体积、口令是否进镜像层、广播包是否真发出），逐条打印 PASS/FAIL。

```bash
cd deploy && ./verify-e7.sh
```
