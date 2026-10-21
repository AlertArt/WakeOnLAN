# E7 契约：镜像规格、网络模式与部署口径

本契约自 E7 起生效，约束 `Dockerfile`、`deploy/` 下的编排与样例配置。**不改后端代码、不改前端资源、不改任何接口**。

## 一、镜像规格

### 构建阶段（`build`）

| 项 | 值 | 原因 |
| --- | --- | --- |
| 基础镜像 | `golang:1.24-bookworm` | 固定版本，不用 `latest`；与 `go.mod` 的 `go 1.24` 一致 |
| 平台 | `FROM --platform=$BUILDPLATFORM` + `ARG TARGETOS TARGETARCH` | 一次构建可产出 amd64 / arm64 两个架构 |
| 编译环境 | `CGO_ENABLED=0` | `modernc.org/sqlite` 是纯 Go 实现，关掉 CGO 后可静态编译、可跨架构 |
| 编译参数 | `-trimpath -ldflags="-s -w"` | 去掉构建机路径与符号表，缩小体积、避免路径泄露 |
| 产物 | `/out/wold` | 单文件，无外部依赖 |

### 运行层

| 项 | 值 | 原因 |
| --- | --- | --- |
| 基础镜像 | `debian:bookworm-slim` | 需要 `iputils-ping` 的官方包；不用 `alpine` 是因为 busybox ping 参数行为与 iputils 有差异，而 3-5 的结论是按 `iputils` 语义验证的 |
| 安装包 | `iputils-ping`、`curl`、`ca-certificates`（`--no-install-recommends`） | ping 供 E3 的 ping 类校验；curl 供 HEALTHCHECK；`ca-certificates` 防止将来出网场景失败 |
| 安装后 | `rm -rf /var/lib/apt/lists/*` | 不把包索引留在层里 |
| 用户 | 启动 `root` → 入口脚本降权到 `wol`（uid/gid `10001`） | root 只存在于启动瞬间，用于自愈 `/data` 属主；业务进程始终非 root |
| 工作目录 | `/app`，含 `wold` 与 `web/` | 与 `httpapi.New(..., "web")` 的相对路径一致 |
| 端口 | `8080`（`EXPOSE`） | host 网络下仅作文档说明，不参与映射 |
| 数据目录 | `VOLUME ["/data"]` | SQLite 落在卷上，不进容器层 |
| 入口 | `ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]`（exec 形式，脚本最终 `exec` 到 wold） | PID 1 直接收到 SIGTERM，走 `main.go` 的优雅关闭 |

运行层**不得出现**：Go 工具链、`gcc`/`make`/`git`、源码、`docs/`、`.git`、`.env`、`deploy/`、任何测试文件。

## 二、运行身份与文件系统

- 入口脚本（`docker/entrypoint.sh`）以 root 启动：把 `/data` 属主递归修正为 `10001:10001`（目录属主已是 10001 时跳过），随后 `setpriv --reuid=10001 --regid=10001 --clear-groups` 降权并 `exec` 到 wold。**业务进程始终以 uid/gid `10001:10001` 运行**，`docker top` 可验证。
- 使用**命名卷**时，Docker 会用镜像内 `/data` 的属主初始化空卷，开箱即可写；入口脚本检测到属主正确时不再执行 chown。
- 使用 **bind mount**（如 `-v /srv/wol:/data`）时，宿主目录属主不受控——**由入口脚本自动修正，无需在宿主上做任何事**。这是为只能用图形界面部署的 NAS（如极空间）设计的：用户没有宿主 shell，也无法在文件管理器里把属主改成数字 uid 10001。

- 容器内**只有 `/data` 需要可写**。其余路径只读即可，不要求 `read_only: true`（避免与 SQLite 的 WAL 临时文件、`/tmp` 交互时踩坑），但也不允许把业务数据写到 `/data` 之外。

## 三、网络模式（7-8，本环节最关键的一条）

**必须使用 `network_mode: host`。**

原因：WOL 魔术包是 UDP 广播，发往 `192.168.x.255` 这类受限广播地址。Docker 默认桥接网络下，容器处在独立网络命名空间，广播包只会落在 `docker0` 虚拟网桥上，**永远到不了物理局域网**。此时：

- 接口仍返回 `202 {"sent":true}`（UDP 无连接，`sendto` 不校验可达性）；
- 日志无任何错误；
- 设备就是不开机。

这是本项目最容易"看起来一切正常"的故障，因此网络模式不是可选项，而是部署前提。

| 宿主机 | host 网络的实际效果 |
| --- | --- |
| Linux（含 NAS、树莓派、软路由） | 容器与宿主共享网络栈，广播正常发出 → **推荐** |
| macOS / Windows（Docker Desktop） | `network_mode: host` 语义被虚拟化层改写，达不到物理网段 → **不支持，需 Linux 宿主** |

由此带来的两个副作用，必须在文档里写清：

1. **`ports` 映射失效**：host 网络下容器直接用宿主 `8080`（或 `WOL_PORT` 指定的端口），不需要也不应该写 `ports`。端口冲突时改 `WOL_PORT`。
2. **端口直接暴露在宿主上**：没有反向代理兜底，登录口令是第一道也是唯一一道门。口令必须足够长（建议 ≥16 位随机串）。

## 四、环境变量

| 变量 | 必需 | 默认 | 说明 |
| --- | --- | --- | --- |
| `WOL_ADMIN_PASSWORD` | **是** | 无 | 登录口令。缺失时进程拒绝启动（不是"空口令启动"）。只从运行时注入 |
| `WOL_PORT` | 否 | `8080` | HTTP 监听端口，host 网络下即宿主端口 |
| `WOL_DATA_DIR` | 否 | `/data` | 数据目录，镜像内已固定为 `/data` |
| `WOL_TOKEN_TTL` | 否 | `168h` | 登录令牌有效期，Go duration 格式 |
| `WOL_LOG_LEVEL` | 否 | `info` | `debug` / `info` / `warn` / `error` |

镜像内只预设 `WOL_DATA_DIR` 与 `WOL_PORT` 两个 `ENV`。**`WOL_ADMIN_PASSWORD` 绝不出现在 Dockerfile 的任何指令中。**

## 五、健康检查

| 项 | 值 |
| --- | --- |
| 命令 | `curl -fsS http://127.0.0.1:8080/api/health` |
| 判定 | HTTP 200 → 健康；`/api/health` 在 DB 故障时返回 503，`curl -f` 即非 0 → 不健康 |
| 间隔 / 超时 / 启动宽限 / 重试 | `30s` / `5s` / `10s` / `3` |
| 位置 | Dockerfile 的 `HEALTHCHECK` 与 compose 的 `healthcheck` 必须一致 |

健康检查不鉴权、不写库、不触发探测：它只回答"进程还在、DB 连得上"。

## 六、体积与分层

| 项 | 目标 |
| --- | --- |
| 最终镜像 | ≤ 200MB（`debian:bookworm-slim` ≈ 75MB + ping/curl ≈ 10MB + 二进制 ≈ 15–20MB） |
| 层数控制 | `apt` 安装与清理在同一条 `RUN`；二进制与静态资源分两条 `COPY` |
| 缓存友好 | 先 `COPY go.mod go.sum` + `go mod download`，再 `COPY` 源码，改代码不必重下依赖 |

体积数字以 `docker image inspect --format '{{.Size}}'` 为准；没有 Docker 环境时只能给估算并注明依据（本契约的目标值即估算依据）。

## 七、编排组成（7-7）

`deploy/docker-compose.yml` 只允许：

```yaml
services:
  wold:            # 唯一的服务
    build / image
    network_mode: host
    env_file: .env
    volumes: [wol-data:/data]
    restart: unless-stopped
    healthcheck

volumes:
  wol-data:
```

硬性要求：`services` 下恰好一个键；无 `db`/`redis`/`nginx`/`caddy`/`watchtower`/`portainer`；无 `depends_on`；无 `privileged: true`；无 `pid: host`（host 网络已足够发出广播）。

## 八、验收口径（7-1 ~ 7-8）

| 序号 | 判定方式 | 证据形态 |
| --- | --- | --- |
| 7-1 | `docker compose up -d --build` 后 `docker inspect` 的 `Health.Status` 变为 `healthy` | 容器状态行 |
| 7-2 | 建一台设备 → `docker compose restart` → 再查，设备仍在 | 两次接口返回 |
| 7-3 | `docker compose down`（不删卷）→ `up -d` → 设备仍在；`docker compose down -v` 后数据消失（反证数据确实在卷上） | 前后对比 |
| 7-4 | `docker image inspect` 体积 ≤ 200MB；`docker run --rm <img> sh -c 'command -v go gcc make git'` 全部无输出 | 数值 + 空输出 |
| 7-5 | 镜像内 `find / -name '.env*' -o -name '*.db' -o -name '.git'` 无结果；`docker history --no-trunc` 中口令明文命中 0 次 | 空输出 + 计数 0 |
| 7-6 | `docker buildx build --platform linux/arm64` 成功，或在 arm64 宿主上启动健康 | 构建日志 / 容器状态 |
| 7-7 | compose 中 `services` 键数为 1 | 计数 |
| 7-8 | 容器以 host 网络运行（`docker inspect` 的 `HostConfig.NetworkMode == "host"`）；真实目标机的开机验证归 E8 | 配置值 + E8 报告 |

## 九、本环节明确不做

- HTTPS / TLS / 反向代理 / 域名（局域网 + host 网络，属使用场景决策，不属交付必需）。
- CI 流水线、GitHub Actions、自动构建与镜像仓库推送。
- Kubernetes / Helm / Swarm 清单。
- 多架构清单合并（`docker manifest create`）与签名；`buildx --platform` 能构建即达标。
- 镜像内定时备份、日志轮转、监控埋点。
- 自动更新（Watchtower）、容器内自升级。
- 资源限制（`cpus`/`mem_limit`）、只读根文件系统、`no-new-privileges`（后者会破坏非 root 用户执行 ping 所需的文件能力，属反向优化）。
