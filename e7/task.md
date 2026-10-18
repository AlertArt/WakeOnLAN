# 本环节任务单（E7 Docker 镜像与编排）

## 环节

E7 —— Docker 镜像与编排。目标只有一个：**别人（或半年后的自己）在一台陌生机器上一条命令跑起来**。

## 功能范围

仅 F8（容器化交付）：镜像构建文件、编排文件、环境变量样例、健康检查、部署说明、镜像体积记录。

不涉及新接口、不涉及新页面、不涉及端到端演练（E8）。**本环节不产生任何 Go 代码变更**。

## 背景

E1–E6 交付的是"能跑的程序"，E7 交付的是"能装的东西"。两者差别不在功能，而在**边界条件**：容器里没有 ping 命令、容器里没有 tzdata、容器里的 `/data` 是卷、容器的网络默认是桥接。这四条恰好都能让系统"看起来正常但实际不可用"——尤其是桥接网络下发不出 UDP 广播：接口返回 202、日志无报错、设备就是不开机（开发计划风险表里点名的"最难排查"故障）。

因此本环节的验收重点不是"镜像能 build 成功"，而是**镜像里的运行时前提是否成立**。

## 环境事实（决定本环节的验收方式）

执行本环节的机器上**没有可用的 Docker**：`docker` 命令不存在，Docker Desktop 未安装，WSL 被本机安全策略禁用（`wsl.exe` 在程序黑名单中，不可绕过）。

这意味着：

- **7-1、7-2、7-3、7-6 无法在本机产生证据**。这三项的本质是"容器真的跑起来了、卷真的挂上了、容器删了数据还在"，只能由真实 Docker 守护进程回答。
- 本环节**不接受**用"读 Dockerfile 判断应该没问题"代替上述证据。未执行就是未执行，验收文档里必须写成"待证"，不得写成"通过"。
- 作为补偿，本环节必须交付一个**一键验收脚本**（`deploy/verify-e7.sh`），把 7-1~7-7 变成一条命令 + 一份 PASS/FAIL 清单，让任何人（包括用户自己）在任意一台装了 Docker 的机器上跑一遍就能产出证据，而不是靠口头复述步骤。

## Agent 输入

- 单容器交付；数据目录固定为容器内 `/data`，由命名卷挂载。
- 全部配置走环境变量：`WOL_PORT`、`WOL_ADMIN_PASSWORD`、`WOL_TOKEN_TTL`、`WOL_LOG_LEVEL`、`WOL_DATA_DIR`。**口令只能运行时注入，不得进入镜像层**。
- 健康检查路径 `GET /api/health`（E4 起已是唯一免鉴权接口，DB 故障返回 503）。
- 镜像需同时支持 `linux/amd64` 与 `linux/arm64`。
- 运行层必须包含 `ping`（`iputils-ping`）：E3 的 ping 类校验依赖系统命令，缺失时该类型校验会直接判 `failed`（3-5 已证明）。
- 网络模式：`host`（开发计划 7-8 的结论：不确定就选 host）。
- 不允许引入数据库容器、缓存容器、反向代理容器。

## 允许改动的文件

- 新增：`Dockerfile`、`.dockerignore`、`deploy/docker-compose.yml`、`deploy/.env.example`、`deploy/verify-e7.sh`
- 新增：`e7/task.md`、`docs/E7-contract.md`、`docs/E7-acceptance.md`
- 修改：`README.md`（部署章节）、`.gitignore`（仅为放行 `deploy/.env.example`）

**不修改** `internal/`、`cmd/`、`web/`、`go.mod`、`go.sum`。若发现必须改代码才能容器化，先停下来写进报告，不在本环节擅自改。

## 禁止事项

1. **禁止为了"能起来"而改代码**：不改端口默认值、不改 `/data` 默认值、不新增 `-health` 之类的容器专用子命令、不加 `os.Getenv` 之外的配置来源（配置文件、命令行参数一律不加）。
2. **禁止把口令写进镜像**：不写 `ENV WOL_ADMIN_PASSWORD`、不写 `ARG` 默认值、不在 `RUN` 里 echo、不 COPY `.env`。口令只允许来自运行时 `-e` / `env_file`。
3. **禁止把数据写进容器层**：`/data` 必须是 `VOLUME`，compose 里必须是命名卷（或显式 bind mount）；不允许为了省事把 SQLite 文件放 `/app` 或 `/tmp`。
4. **禁止多服务编排**：compose 里只允许一个 `services` 条目。不加 Postgres/Redis/Nginx/Caddy/Watchtower/Portainer，不加 sidecar。健康检查用镜像内 `curl`，不引入额外容器。
5. **禁止 `network_mode: bridge` 默认放行**：必须 `host`，并在文档中写明"桥接网络下发不出 UDP 广播，唤醒会静默失败"。这是 7-8 的验收点，也是本项目最容易"看起来正常"的坑。
6. **禁止使用 `latest` 基础镜像标签**：构建阶段与运行阶段都用固定版本标签（`golang:1.24-bookworm`、`debian:bookworm-slim`），保证半年后重建结果一致。
7. **禁止在运行层留下编译工具链**：`gcc`、`make`、`git`、Go 工具链一律不进最终镜像；用多阶段构建，最终层只放二进制、静态资源、ping、curl。
8. **禁止 `apt-get upgrade`**，禁止不清理 `apt` 缓存的写法（`rm -rf /var/lib/apt/lists/*`）。
9. **禁止以 root 运行**：最终镜像必须 `USER` 非 0，且 `/data` 属主为该用户；文档要说明 bind mount 时需自行 `chown`。
10. **禁止引入 CI 配置、K8s 清单、Helm chart、`.gitlab-ci.yml`、GitHub Actions**。部署方式只有 compose（外加一条等价的 `docker run`）。
11. **禁止伪造镜像体积**：7-4 的数字必须来自 `docker image inspect`；本机无 Docker 时只能标注"估算"并说明依据，不得写成实测。
12. **禁止改前端资源却不提升 SW 缓存版本**：`sw.js` 的 `CACHE` 常量属 E6 产物，本环节不动 `web/`（若动则必须同步升版本）。

## 输出要求

1. `Dockerfile`（多阶段）：
   - 构建阶段用 `$BUILDPLATFORM` + `TARGETOS`/`TARGETARCH`，`CGO_ENABLED=0`，`-trimpath -ldflags="-s -w"`；
   - 运行阶段 `debian:bookworm-slim` + `iputils-ping` + `curl` + `ca-certificates`，非 root，`WORKDIR /app`，拷贝 `wold` 与 `web/`；
   - `ENV WOL_DATA_DIR=/data WOL_PORT=8080`（仅这两项，不含口令）；`EXPOSE 8080`；`VOLUME ["/data"]`；
   - `HEALTHCHECK` 打 `/api/health`，`ENTRYPOINT` 用 exec 形式（保证 SIGTERM 直达进程，走优雅关闭）。
2. `.dockerignore`：至少排除 `.git`、`.env*`、`tmp/`、`docs/`、`e[0-9]/`、`output/`、`test/`、本地二进制。构建上下文里不得出现任何口令文件。
3. `deploy/docker-compose.yml`：单服务、`network_mode: host`、命名卷 `wol-data:/data`、`env_file: .env`、`restart: unless-stopped`、与 Dockerfile 一致的健康检查。
4. `deploy/.env.example`：逐项注释，口令一栏写占位符并注明"必须改，且不要提交 `.env`"。
5. `deploy/verify-e7.sh`：一条命令跑完 7-1~7-7，逐项打印 `PASS`/`FAIL` 与证据行，退出码非 0 表示存在失败项。
6. `docs/E7-contract.md`：镜像规格、网络模式、卷、环境变量、健康检查、验收口径逐条写清。
7. `docs/E7-acceptance.md`：7-1~7-8 逐项结论；本机无法执行的三项写"待证（需 Docker 主机）"并给出待执行的命令。

## 验收方式

```bash
# 本机可执行（无 Docker 也成立）
grep -c "^  [a-z]" deploy/docker-compose.yml   # 只允许 1 个服务键
grep -n "network_mode" deploy/docker-compose.yml
grep -rn "WOL_ADMIN_PASSWORD" Dockerfile deploy/   # 仅允许出现在 .env.example / verify 脚本的变量名引用
git diff --stat -- internal cmd web go.mod go.sum  # 必须为空

# 需在有 Docker 的机器上执行（产证据）
cd deploy && cp .env.example .env && ./verify-e7.sh
```

7-8（广播是否真的发得出去）需要真实目标机，归 E8 端到端演练，本环节只负责把网络模式与文档前提固定下来。

## 完成定义

- 7-1~7-7 在**有 Docker 的主机**上全部 PASS（由 `verify-e7.sh` 输出证明），7-8 的网络前提写入文档。
- 镜像体积有明确数字与来源；镜像内无 `.env`、无口令、无 `.git`、无 Go 工具链。
- compose 仅一个服务；`internal/`、`cmd/`、`web/` 零改动。
- 若缺少 Docker 环境导致 7-1~7-3、7-6 未执行，本环节状态记为 **"产物就绪，验收阻塞"**，不得标注"放行"。
