# 局域网唤醒系统（WakeOnLAN）

单用户、自用的局域网唤醒服务：录入设备 → 点一下 → 发送 WOL 魔术包 → 用 Ping / TCP 端口探测给出"已上线 / 超时未响应"的明确结论。移动端为响应式 Web + PWA，部署为单容器 + 单文件 SQLite。

> 当前处于 **E7 环节：Docker 镜像与编排**（已放行）。7-1~7-7 在 Docker Desktop 4.95 / Engine 29.8 上实测通过，7-8 的广播包已在宿主网卡抓到（102 字节标准魔术包）；"目标机真的开机"留给 E8 端到端演练。

## 快速开始（本地）

```bash
# 需要 Go 1.24+
# 口令必填，服务不会以空口令启动
WOL_ADMIN_PASSWORD='换成你自己的口令' WOL_DATA_DIR=./tmp/data WOL_PORT=8080 go run ./cmd/wold

# 健康检查是唯一免鉴权的接口（容器探活用）
curl -s http://127.0.0.1:8080/api/health
# {"status":"ok","db":"ok","time":"..."}

# 登录拿令牌（默认 7 天有效）
TOKEN=$(curl -s -X POST http://127.0.0.1:8080/api/login \
  -H 'Content-Type: application/json' \
  -d '{"password":"换成你自己的口令"}' | sed -E 's/.*"token":"([^"]+)".*/\1/')
# {"token":"...","expires_at":"..."}

# 其余接口都要带 Authorization: Bearer
curl -s -X POST http://127.0.0.1:8080/api/devices \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"书房主机","mac":"AA:BB:CC:DD:EE:FF","broadcast_addr":"192.168.1.255","probe_type":"ping","probe_value":"192.168.1.10"}'
curl -s http://127.0.0.1:8080/api/devices -H "Authorization: Bearer $TOKEN"

# 一键唤醒（F2）
curl -s -X POST http://127.0.0.1:8080/api/devices/1/wake -H "Authorization: Bearer $TOKEN"
# {"sent":true,"sent_at":"..."}   ← 仅表示包已发出，不代表设备已开机

# 触发校验（F3）：异步，立即返回；结果从状态接口取
curl -s -X POST http://127.0.0.1:8080/api/devices/1/check -H "Authorization: Bearer $TOKEN"
# {"status":"checking","started_at":"..."}
curl -s http://127.0.0.1:8080/api/status -H "Authorization: Bearer $TOKEN"
# [{"device_id":1,"status":"online","message":"","checked_at":"..."}]

# 登出（吊销当前令牌）
curl -s -X POST http://127.0.0.1:8080/api/logout -H "Authorization: Bearer $TOKEN"

# 单元测试
go test ./...
```

状态取值：`pending` / `checking` / `online` / `timeout` / `failed`。轮询上限 120 秒、间隔 5 秒，均为常量，不提供配置。

`ping` 类型校验依赖系统 `ping` 命令（tcp 类型不依赖）。容器镜像必须包含 ping，否则这类校验会明确失败并说明原因。

浏览器打开 `http://127.0.0.1:8080/`：未登录会停在登录页，输入口令后进入设备列表页。页面上可添加/编辑/删除设备（弹层操作）、一键唤醒并自动轮询校验结果（已上线 / 超时未响应 / 校验失败）；令牌过期会自动带回登录页。手机浏览器里可"添加到主屏幕"当作独立应用使用（局域网 HTTP 地址下 Service Worker 不生效，退化为普通书签；要完整 PWA 需 HTTPS，属部署决策）。

接口契约：`docs/E1-contract.md`（设备 CRUD）、`docs/E2-contract.md`（唤醒）、`docs/E3-contract.md`（校验与状态）、`docs/E4-contract.md`（鉴权与限流）、`docs/E5-contract.md`（前端页面与令牌存放）、`docs/E6-contract.md`（PWA 清单与缓存策略）、`docs/E7-contract.md`（镜像规格、网络模式与部署口径）。
各环节验收记录：`docs/E*-acceptance.md`；界面截图见 `docs/e5-shots/`、`docs/e6-shots/`。

## 部署（Docker）

前提：**Linux 主机**（小主机 / NAS / 树莓派均可）+ Docker 与 compose 插件。Windows / macOS 的 Docker Desktop 不适用，原因见下方第 1 条。

```bash
cd deploy
cp .env.example .env
vi .env                       # 把 WOL_ADMIN_PASSWORD 改成 16 位以上随机串
docker compose up -d --build
docker compose ps             # STATUS 列出现 (healthy) 即就绪
```

然后浏览器访问 `http://<主机IP>:8080`。

四条必须知道的事：

1. **网络模式是 `host`，不是可选项。** WOL 是 UDP 广播，Docker 默认桥接网络下广播包只会落在 `docker0` 虚拟网桥上，永远到不了物理局域网——此时接口照样返回 `202`、日志照样无错误、设备就是不开机。Docker Desktop（macOS/Windows）会把 `host` 语义改写到虚拟机内，同样发不出广播，所以部署机必须是 Linux。
2. **端口直接占用宿主端口**，没有 `ports` 映射也没有反向代理：端口冲突改 `WOL_PORT`；反过来说，口令是唯一的门，务必设置得足够长。
3. **数据在命名卷 `wol-data` 上**，删容器、重建容器都不丢。若改用 bind mount（`-v /srv/wol:/data`），宿主目录需先 `sudo chown 10001:10001 /srv/wol`，否则非 root 进程写不进去。
4. **镜像内含 `iputils-ping`**：ping 类校验依赖系统命令，缺了会直接判"校验失败"。镜像还以非 root（uid 10001）运行，因此不要给容器加 `no-new-privileges`——那会破坏 ping 所需的文件能力。

一键验收（在装有 Docker 的机器上执行，逐项打印 7-1~7-8 的 PASS/FAIL）：

```bash
cd deploy && ./verify-e7.sh
```

镜像支持 amd64 与 arm64：`docker buildx build --platform linux/arm64 -f Dockerfile .`

**国内网络构建**：`auth.docker.io` 与 `deb.debian.org` 常返回 502，两个办法：

```bash
# 1) 基础镜像拉不下来：改用镜像加速源后再打本地标签
docker pull docker.1ms.run/library/golang:1.24-bookworm
docker tag  docker.1ms.run/library/golang:1.24-bookworm golang:1.24-bookworm
docker pull docker.1ms.run/library/debian:bookworm-slim
docker tag  docker.1ms.run/library/debian:bookworm-slim debian:bookworm-slim

# 2) apt 装不上 ping：切换 Debian 源（不填则用官方源）
docker build --build-arg APT_MIRROR=mirrors.aliyun.com -t wol:1.0.0 .
```

实测结论见 `docs/E7-acceptance.md`（7-1~7-7 全部通过，7-8 广播包已在宿主网卡抓到）。

> **在 Windows/macOS 上试用会看不到页面**：Docker Desktop 的 `host` 网络指虚拟机内部的 host，不与 Windows 宿主共享端口（实测容器内 `127.0.0.1:8080` 通、宿主 `127.0.0.1:8080` 与局域网 IP 均不通）。广播包仍能发出，只是浏览器访问不到界面。临时试用可加 `ports: ["8080:8080"]` 并去掉 `network_mode: host`，但那只是调试手段——桥接网络下发不出广播，不能作为部署形态。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `WOL_PORT` | `8080` | HTTP 监听端口 |
| `WOL_DATA_DIR` | `/data` | 数据目录（SQLite 文件所在位置） |
| `WOL_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `WOL_ADMIN_PASSWORD` | **无（必填）** | 登录口令，由环境注入；为空时进程拒绝启动，**禁止写入代码或镜像** |
| `WOL_TOKEN_TTL` | `168h` | 登录令牌有效期，Go duration 写法（如 `12h`、`30m`） |

安全提示：令牌为明文 Bearer，服务设计为**局域网直连**使用。不要暴露到公网；不建议放在反向代理后面（会因共享来源 IP 让限流误伤所有人）。

## 目录结构

```
cmd/wold/            进程入口：加载配置 → 初始化存储 → 迁移 → 启动 HTTP → 优雅退出
internal/config/     环境变量解析与校验
internal/store/      SQLite 连接、建表迁移、device 表读写、校验状态（只留最近一次）
internal/httpapi/    路由、中间件、健康检查、设备 CRUD、唤醒、校验与状态（接口一律在此登记）
internal/wol/        魔术包构造与发送（纯函数 + 单次 UDP 发送，不重试不并发）
internal/probe/      唤醒结果校验：Ping / TCP 探测 + 进程内任务生命周期（无队列）
internal/auth/       单口令认证：令牌签发/校验/吊销、按 IP 的失败限流（仅标准库）
web/                 静态前端：登录页 + 设备列表页 + 弹层、manifest、Service Worker、图标（原生实现，零依赖零构建）
Dockerfile           多阶段构建：静态编译（amd64/arm64）→ debian-slim 运行层（含 ping、非 root）
.dockerignore        构建上下文白名单化：源码与 web/ 进，.env/.git/docs/数据一律不进
deploy/              docker-compose.yml（单服务 + host 网络 + 命名卷）、.env.example、verify-e7.sh 一键验收
test/e2e/            端到端验收脚本（E8）
docs/                接口契约、各环节说明与验收记录
e0/ e1/ ...          各环节任务单
```

## 范围边界

本项目只做 9 项能力（F1–F9：设备管理、一键唤醒、唤醒校验、状态展示、单用户登录、响应式界面、PWA、Docker 部署、部署文档）。

以下内容**明确不做**，也不接受"顺手加上"：用户注册与多账号、定时/批量唤醒、ARP 扫描与路由设备发现、历史图表、多语言与主题、原生 App、消息推送、WebSocket、可观测全家桶、ORM 抽象层、Redis/队列。

完整定义见 `docs/` 下的开发计划与验收规范。

## 技术选型（已冻结）

| 维度 | 选择 | 理由 |
| --- | --- | --- |
| 后端 | Go + 标准库 `net/http` | 单二进制、低内存、交叉编译简单；无需框架 |
| 存储 | SQLite（纯 Go 驱动，免 CGO） | 单文件，挂载一个卷即可 |
| 前端 | 原生 HTML/CSS/JS，无构建 | 两页应用不需要构建链与状态管理框架 |
| 部署 | 单容器，环境变量配置 | 一条命令启动；数据落 `/data` 卷 |

## 开发纪律

- 一次只做一个环节（E0–E8），不合并下发、不跨环节重构已放行代码。
- 新增接口必须在 `internal/httpapi` 的路由表中登记，并同步更新接口契约。
- 每个环节交付需附：可复现命令、证据文件、变更清单、未做清单。
