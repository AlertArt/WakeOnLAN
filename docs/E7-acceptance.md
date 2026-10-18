# E7 验收记录

对应开发计划 4.8 节的验收清单 7-1 至 7-8。验收时间：2026-10-18 21:36–21:58（GMT+8）。

## 结论：产物就绪，验收阻塞（未放行）

本机**没有任何可用的容器运行时**：`docker` 命令不存在、Docker Desktop 未安装、WSL 被本机安全策略禁用（`wsl.exe` 在程序黑名单中，不可绕过）。7-1、7-2、7-3、7-6 的本质是"容器真的起来了、卷真的挂上了、容器删了数据还在、arm64 真的能跑"，这四问只有真实 Docker 守护进程能回答。

按 `e7/task.md` 的约定：**未执行即未执行**，不以"读 Dockerfile 判断应该没问题"替代证据。因此本环节状态记为 **阻塞**，不标"放行"。

本环节实际交付了两样东西来消解这个阻塞：一份经过机械核验的产物集，以及一条命令就能产出全部证据的 `deploy/verify-e7.sh`。

| 序号 | 验收动作 | 结果 | 依据 |
| --- | --- | --- | --- |
| 7-1 | 干净环境一条命令启动、健康检查转健康 | **待证（需 Docker）** | 本机无 Docker；`verify-e7.sh` 已实现自动判定（等 `Health.Status=healthy`，超时 120s） |
| 7-2 | 重启容器后数据仍在 | **待证（需 Docker）** | 同上；脚本流程为 建设备 → restart → 复查 |
| 7-3 | 删除容器后重建，数据仍在 | **待证（需 Docker）** | 脚本还加了一步 `down -v` 反证（数据确实在卷上，不在别处） |
| 7-4 | 镜像体积合理、无工具链残留 | **部分** | 工具链：运行层无编译产物可 COPY（见 §2 结构性证据）；体积只能实测，估算 95–105MB，脚本会打印 `docker image inspect` 的真实值 |
| 7-5 | 镜像内无 .env、密钥、源码仓库 | **部分** | 口令在 Dockerfile 中零命中（实测）；镜像内文件扫描需 Docker |
| 7-6 | arm64 环境启动 | **部分** | `linux/arm64` 交叉编译实测通过（静态 ELF，`e_machine=183`）；arm64 容器启动未验证 |
| 7-7 | 编排文件仅一个应用服务 | **通过** | compose 结构解析：`services` 键数 = 1，无 db/redis/nginx 等 |
| 7-8 | 容器必须能发出广播包 | **部分** | `network_mode: host` 已写入并通过结构断言；真实开机验证归 E8 |

## 一、本机已产生的证据

### 1. 构建上下文（`.dockerignore` 生效后）

用 Docker 的匹配语义（不含分隔符的模式等价于 `**/<pattern>`，且匹配路径与所有上级目录）模拟构建上下文：

```
上下文文件数：53
    .dockerignore   Dockerfile   go.mod   go.sum   cmd/wold/main.go
    internal/**（54 个 .go）   web/**（12 个静态文件）
PASS  上下文里没有 .env / .git / *.db
PASS  上下文包含 Dockerfile 与编译所需源码（含 cmd/wold/main.go）
PASS  上下文包含 web/ 静态资源 [12 个文件]
PASS  上下文不含 docs/、eN/、test/、tmp/、deploy/
```

### 2. 静态核验（43 项）

```
== Dockerfile 断言（23 项，全过） ==
PASS 多阶段构建（至少两个 FROM）
PASS 构建阶段固定版本（golang:1.24-bookworm，非 latest）
PASS 运行层固定版本（debian:bookworm-slim）
PASS 关闭 CGO 静态编译 / 按 TARGETARCH 交叉编译 / -trimpath 与 -s -w
PASS 运行层安装 iputils-ping / 含 curl / apt 索引已清理 / 无 apt-get upgrade
PASS 声明数据卷 VOLUME ["/data"] / 数据目录属主为运行用户
PASS 以非 root 用户运行 / USER 位于 ENTRYPOINT 之前
PASS HEALTHCHECK 打 /api/health / 参数与契约一致（30s/5s/10s/3）
PASS ENTRYPOINT 用 exec 形式
PASS 只预设 DATA_DIR / PORT 两个 ENV
PASS 镜像内无口令 ENV/ARG / 未 COPY 整个项目 / 未 COPY docs|eN|test|deploy

== compose 断言（13 项，全过） ==
PASS services 恰好一个 [wold]        PASS network_mode = host
PASS 未声明 ports                    PASS 未使用 privileged / 无 depends_on
PASS restart = unless-stopped        PASS env_file 指向 .env
PASS 挂载 wol-data 到 /data          PASS 顶层声明了 wol-data 卷
PASS healthcheck 打 /api/health      PASS healthcheck 周期与 Dockerfile 一致
PASS 无数据库/缓存/反代服务          PASS 镜像标签非 latest [wol:1.0.0]

== 结果：43/43 通过 ==
```

7-4 的"无工具链残留"在此处是**结构性证据**而非实测：运行层只执行 `COPY --from=build /out/wold` 与 `COPY web/`，构建阶段（含 Go 工具链）在另一层，不会出现在最终镜像里。

### 3. 交叉编译（7-6 的构建部分）

```
linux/amd64  9,969,848 字节（9.5 MB）  静态 ELF，无 PT_INTERP，e_machine=62
linux/arm64  9,437,368 字节（9.0 MB）  静态 ELF，无 PT_INTERP，e_machine=183
```

无 `PT_INTERP` 表示不依赖动态链接器，与 `CGO_ENABLED=0` 的预期一致；两个架构都产出可执行文件，说明 Dockerfile 里 `GOOS=$TARGETOS GOARCH=$TARGETARCH` 的做法成立。

### 4. 构建阶段文件集重放

把 Dockerfile **实际 COPY 的文件子集**（`go.mod`、`go.sum`、`cmd/`、`internal/`）单独复制到空目录，在 `GOPROXY=off`（禁用网络）下编译：

```
exit=0
-rwxr-xr-x 9969848  out/wold      ← 与仓库内构建产物字节数完全一致
```

结论：Dockerfile 的 COPY 清单是**充分**的，不存在"漏拷某个文件导致容器内编译失败"的情况。

### 5. 运行层目录布局模拟

按镜像内的布局（`/app/wold` + `/app/web/`，`WORKDIR /app`）搭出同样结构并启动：

```
/api/health            200
/                      200
/login.html            200
/app.js                200
/manifest.json         200
/sw.js                 200
/icons/icon-192.png    200
data/wol.db            已生成（WOL_DATA_DIR 相对路径解析正确）
```

结论：`httpapi.New(..., "web")` 的相对路径在容器工作目录下能正确命中静态资源，不需要改代码也不需要传绝对路径。

### 6. 后端零改动

```
go vet ./...                → 无输出
go test ./...               → 六包全绿，139 个用例
git status -- internal cmd web go.mod go.sum → 空
```

## 二、待执行（任意一台装了 Docker 的机器）

```bash
cd deploy
cp .env.example .env
# 把 WOL_ADMIN_PASSWORD 改成真实口令（16 位以上随机串）
${EDITOR:-vi} .env

./verify-e7.sh          # 一条命令跑完 7-1 ~ 7-8，逐项 PASS/FAIL
echo "exit=$?"          # 0 = 全部通过
```

脚本会真实执行 `docker compose up -d --build`、`restart`、`down`、`down -v`，因此**会清空测试数据**，只应在测试环境运行。它输出的是逐项证据行（体积数值、`Health.Status`、`History` 命中数、`NetworkMode` 等），不是"已通过"四个字。

## 三、本环节发现并修掉的一个真问题

`.dockerignore` 里最初写了裸规则 `wold`（想排除本地编译出的二进制）。Docker 的匹配语义会把不含分隔符的模式当作 `**/wold`，于是**源码目录 `cmd/wold/` 被一并排除**，构建阶段将找不到 `main.go`，`docker build` 会直接失败。

这个错误在"只读一遍文件"的验收方式下几乎必然漏过——上下文清单是机械枚举出来的，`cmd/wold/main.go` 从列表里消失才暴露出来。已改为只排除 `*.exe`，并在 `.dockerignore` 里留了注释说明原因。

## 四、无法在此环境验证、也不打算"推断通过"的项

| 项 | 为什么无法验证 |
| --- | --- |
| 容器真的能起来 | 需要 Docker 守护进程；本机没有，WSL 被安全策略禁用 |
| 命名卷的属主初始化 | 依赖 Docker 在创建卷时从镜像目录复制属主，只有真实运行才能确认 uid 10001 可写 `/data` |
| 非 root 用户执行 `ping` | 依赖 `iputils-ping` 的文件能力（`cap_net_raw`）在容器内生效；README 与契约已提示"不要加 `no-new-privileges`"，但结论需实测 |
| 镜像体积 | 只能实测；估算 95–105MB 的依据是 debian-slim ≈ 75MB + ping/curl ≈ 12MB + 二进制 9.5MB |
| arm64 容器启动 | 本机只能交叉编译，不能运行 arm64 二进制 |

## 五、留给 E8 的两件事

1. **7-8 的最终确认**：`verify-e7.sh` 只能证明容器用的是 host 网络，证明不了广播包真的到了物理网段。这一步需要真实目标机（关机 → 点唤醒 → 观察开机），属 E8 端到端演练。
2. **升级时的缓存版本**：若 E8 期间改动 `web/` 任何文件，必须同步提升 `sw.js` 的 `CACHE` 常量，否则老用户会停在旧壳上。
