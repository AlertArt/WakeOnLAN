# 局域网唤醒系统（WakeOnLAN）

单用户、自用的局域网唤醒服务：录入设备 → 点一下 → 发送 WOL 魔术包 → 用 Ping / TCP 端口探测给出"已上线 / 超时未响应"的明确结论。移动端为响应式 Web + PWA，部署为单容器 + 单文件 SQLite。

> 当前处于 **E3 环节：唤醒校验与状态轮询**（已放行）。设备 CRUD、一键唤醒、异步校验与状态查询均可用；前端页面与鉴权尚未实现。

## 快速开始（本地）

```bash
# 需要 Go 1.24+
WOL_DATA_DIR=./tmp/data WOL_PORT=8080 go run ./cmd/wold

# 验证
curl -s http://127.0.0.1:8080/api/health
# {"status":"ok","db":"ok","time":"..."}

# 设备接口
curl -s -X POST http://127.0.0.1:8080/api/devices \
  -d '{"name":"书房主机","mac":"AA:BB:CC:DD:EE:FF","broadcast_addr":"192.168.1.255","probe_type":"ping","probe_value":"192.168.1.10"}'
curl -s http://127.0.0.1:8080/api/devices

# 一键唤醒（F2）
curl -s -X POST http://127.0.0.1:8080/api/devices/1/wake
# {"sent":true,"sent_at":"..."}   ← 仅表示包已发出，不代表设备已开机

# 触发校验（F3）：异步，立即返回；结果从状态接口取
curl -s -X POST http://127.0.0.1:8080/api/devices/1/check
# {"status":"checking","started_at":"..."}
curl -s http://127.0.0.1:8080/api/status
# [{"device_id":1,"status":"online","message":"","checked_at":"..."}]

# 单元测试
go test ./...
```

状态取值：`pending` / `checking` / `online` / `timeout` / `failed`。轮询上限 120 秒、间隔 5 秒，均为常量，不提供配置。

`ping` 类型校验依赖系统 `ping` 命令（tcp 类型不依赖）。容器镜像必须包含 ping，否则这类校验会明确失败并说明原因。

浏览器打开 `http://127.0.0.1:8080/` 可看到占位页与服务状态（正式页面属 E5）。

接口契约：`docs/E1-contract.md`（设备 CRUD）、`docs/E2-contract.md`（唤醒）、`docs/E3-contract.md`（校验与状态）。
各环节验收记录：`docs/E*-acceptance.md`。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `WOL_PORT` | `8080` | HTTP 监听端口 |
| `WOL_DATA_DIR` | `/data` | 数据目录（SQLite 文件所在位置） |
| `WOL_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `WOL_ADMIN_PASSWORD` | 空 | 登录口令，由环境注入（E4 启用），**禁止写入代码或镜像** |

## 目录结构

```
cmd/wold/            进程入口：加载配置 → 初始化存储 → 迁移 → 启动 HTTP → 优雅退出
internal/config/     环境变量解析与校验
internal/store/      SQLite 连接、建表迁移、device 表读写、校验状态（只留最近一次）
internal/httpapi/    路由、中间件、健康检查、设备 CRUD、唤醒、校验与状态（接口一律在此登记）
internal/wol/        魔术包构造与发送（纯函数 + 单次 UDP 发送，不重试不并发）
internal/probe/      唤醒结果校验：Ping / TCP 探测 + 进程内任务生命周期（无队列）
web/                 静态前端资源（无构建链）
deploy/              Dockerfile 与编排文件（E7）
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
