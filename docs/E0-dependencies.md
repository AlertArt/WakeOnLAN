# 依赖清单

约束：每一项都必须能对应到 F1–F9 的某个能力；无对应能力的一律不引入。

## 后端（Go）

| 依赖 | 版本 | 用途 | 对应能力 | 备注 |
| --- | --- | --- | --- | --- |
| 标准库 `net/http` | Go 1.24 | HTTP 服务与路由 | F1 F2 F3 F5 F8 | 不引入 Gin/Echo 等框架 |
| 标准库 `database/sql` | Go 1.24 | 数据库抽象 | F1 F8 | 不引入 ORM |
| 标准库 `log/slog` | Go 1.24 | 结构化日志 | F8 | 不引入 zap/logrus |
| 标准库 `net` | Go 1.24 | UDP 广播（E2）、TCP 探测（E3） | F2 F3 | — |
| 标准库 `os/exec` | Go 1.24 | 容器内调用 `ping`（E3 备选路径） | F3 | 需镜像内提供 ping |
| `modernc.org/sqlite` | v1.34.x | SQLite 驱动 | F1 F8 | 纯 Go，免 CGO，便于交叉编译 amd64/arm64 |

暂未引入、由后续环节按需决定的依赖：

| 环节 | 可能需要 | 倾向 |
| --- | --- | --- |
| E4 | 令牌签发 | 优先标准库 `crypto/hmac` + `crypto/rand`，不引入 JWT 库 |
| E5 | 前端请求封装 | 原生 `fetch`，不引入 axios |
| E6 | PWA 图标生成 | 离线生成 PNG 静态资源，不引入构建工具 |

## 前端

无第三方运行时依赖（原生 HTML/CSS/JS，无构建链）。

## 明确不引入

图表库、UI 组件库全家桶、状态管理框架、国际化库、WebSocket 库、任务队列/调度库、ORM 与迁移框架、Redis 客户端、指标采集库、APM SDK。
