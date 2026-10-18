# 本环节任务单（E1 后端数据层与设备 CRUD）

## 环节

E1 —— 后端数据层与设备 CRUD。

## 功能范围

仅 F1（设备管理）。不涉及 F2 发包、F3 校验、F4 状态展示、F5 认证。

本环节把 `device` 表结构与四个接口的请求/响应形状**一次定死**，E2–E5 只允许读取，不允许改动。

## 背景

E0 已有可启动的服务、SQLite 连接与健康检查。本环节在此基础上补齐"设备"这一唯一业务实体的持久化与增删改查，使后续发包环节能按 ID 找到目标设备。

## Agent 输入

- 字段定义：名称、MAC、广播地址、广播端口、可选校验目标（类型 + 值）、创建时间、更新时间。
- 接口契约：`docs/E1-contract.md`（路径、方法、请求体、响应体、错误码、字段白名单）。
- 存储约定：SQLite 文件位于 `WOL_DATA_DIR` 下的 `wol.db`；表名 `device`，单数小写下划线。
- 分页规则：一期不分页，设备数上限 100（超限返回 409）。

## 允许改动的文件

- `internal/store/`（新增 `device.go`、`schema.go` 与对应测试）
- `internal/httpapi/`（新增 `devices.go` 与对应测试；`server.go` 仅追加路由登记与依赖注入）
- `cmd/wold/main.go`（仅传递 store 实例，不改启动流程）
- `docs/E1-contract.md`、`docs/E1-acceptance.md`
- `e1/task.md`

## 禁止事项

1. 禁止引入 Web 框架、ORM、SQL 构造器、校验库；继续只使用 `database/sql` 与标准库。
2. 禁止在响应体中出现契约之外的字段：不得暴露自增 ID 以外的内部字段、不得出现 `deleted_at` 软删除标记、不得出现 `password`/`token` 类字段。
3. 禁止实现分页、排序参数、模糊搜索、批量导入导出。
4. 禁止实现定时任务、唤醒记录表、状态历史表（属 E2/E3，且历史曲线已在排除清单内）。
5. 禁止实现登录、鉴权中间件、限流（属 E4）。
6. 禁止改动 `/api/health` 的响应结构。
7. 禁止编写演示数据、seed 脚本、示例设备。
8. 禁止为"方便前端"增加 `count`、`total`、`page` 等列表包装字段；列表直接返回数组。

## 输出要求

1. 代码：
   - `internal/store/schema.go`：`device` 表建表语句与初始化函数（幂等）。
   - `internal/store/device.go`：`Device` 结构、`ErrNotFound`/`ErrConflict`/`ErrLimitExceeded` 语义错误、`ListDevices`/`GetDevice`/`CreateDevice`/`UpdateDevice`/`DeleteDevice`。
   - `internal/httpapi/devices.go`：四个 handler、请求体解析、MAC 归一化与校验、统一错误响应。
   - `internal/httpapi/server.go`：在 `Routes()` 中登记四条路由。
2. 测试：新增、查询、修改、删除各至少一条正例与一条反例；非法 MAC（长度错误、非法字符、分隔符混用）；重复 MAC 冲突；删除不存在；超上限。
3. 证据：`go test ./...` 输出、四接口 curl 实际响应、重启前后查询对比、响应字段白名单核验结果。
4. 未做清单：见 `docs/E1-acceptance.md`「本次明确未做」。

## 验收方式

按开发计划 4.2 节 1-1 至 1-7 执行，结果记录在 `docs/E1-acceptance.md`：

```bash
go vet ./...
go test ./...

# 1-2 契约核验
curl -s -X POST http://127.0.0.1:18080/api/devices -d '{"name":"书房主机","mac":"AA:BB:CC:DD:EE:FF","broadcast_addr":"192.168.1.255","broadcast_port":9}'
curl -s http://127.0.0.1:18080/api/devices
curl -s -X PUT http://127.0.0.1:18080/api/devices/1 -d '{"name":"书房主机2","mac":"AA:BB:CC:DD:EE:FF","broadcast_addr":"192.168.1.255","broadcast_port":9}'
curl -s -X DELETE http://127.0.0.1:18080/api/devices/1

# 1-3 非法 MAC / 1-4 重复 MAC / 1-5 删除不存在
# 1-7 重启：停止进程 → 重新启动 → 再次 GET
```

## 完成定义

1-1 至 1-7 全部通过；1-6 由响应字段白名单脚本逐字段比对，不允许"多一个也无害"的放行；未做清单中的每一项都能对应到明确的后续环节。
