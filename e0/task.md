# 本环节任务单（E0 规格冻结与仓库骨架）

## 环节

E0 —— 规格冻结与仓库骨架。

## 功能范围

不实现任何业务功能。仅落地 F8（单容器 + 环境变量配置）与"服务可启动"这一前提条件，为 F1–F9 提供共同的目录、命名与依赖基线。

## 允许改动的文件

- `go.mod`
- `cmd/wold/`
- `internal/config/`
- `internal/store/`（仅连接与自检，不建业务表）
- `internal/httpapi/`（仅健康检查与静态托管）
- `internal/wol/doc.go`、`internal/probe/doc.go`（仅包声明与常量）
- `web/index.html`
- `docs/E0-*.md`
- `README.md`、`.gitignore`、`e0/task.md`

## 禁止事项

1. 禁止创建任何业务数据表（`device` 表属 E1），禁止预建空接口。
2. 禁止引入 Gin/Echo/Chi 等 Web 框架、ORM、配置库、日志库；依赖清单见 `docs/E0-dependencies.md`。
3. 禁止实现登录、令牌、魔术包发送、探测、PWA 清单、Dockerfile（分属 E2–E7）。
4. 禁止多语言、主题切换、图表库、状态管理框架、WebSocket、任务队列、Redis、指标采集。
5. 禁止编写"演示用"页面、示例组件、示例数据。

## 输出要求

1. 代码：进程入口、配置解析、SQLite 连接、路由与健康检查、占位页。
2. 测试：配置解析（默认值/覆盖/非法值）、SQLite 连通、健康检查（正常/数据库故障/方法不允许）。
3. 证据：`go test ./...` 输出、`curl /api/health` 输出、目录树、依赖清单。
4. 未做清单：见 `docs/E0-decision.md`「E0 明确未做」。

## 验收方式

按开发计划 4.1 节 0-1 至 0-4 执行，结果记录在 `docs/E0-acceptance.md`：

```bash
go test ./...
go vet ./...
WOL_DATA_DIR=./tmp/data WOL_PORT=8080 go run ./cmd/wold
curl -s http://127.0.0.1:8080/api/health
```

## 完成定义

0-1 至 0-4 全部通过，且未做清单中的每一项都能对应到明确的后续环节。
