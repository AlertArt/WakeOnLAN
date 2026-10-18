# 本环节任务单（E3 唤醒校验与状态轮询）

## 环节

E3 —— 唤醒校验与状态轮询。

## 功能范围

仅 F3（唤醒校验），以及 F4 所依赖的**状态数据**（状态值、说明、时间）。不涉及 F4 的页面渲染（属 E5）、不涉及 F5 认证（属 E4）。

产出三样东西：可单独调用的探测器、进程内的校验任务生命周期管理、两个 HTTP 接口（触发校验、查询状态）。

## 背景

E2 的发包接口返回 202，只代表"102 字节已交给内核"。UDP 无连接，往不可达地址发也返回成功，所以**设备到底有没有起来，只能靠探测**。本环节就是把这个结论产出来。

这是本项目唯一有异步特征的部分，也是验收最容易含糊的部分：说"校验中"很容易，说清"什么时候一定不再是校验中"才是真要求。

## Agent 输入

- 校验方式二选一，由设备记录的 `probe_type` 决定：`ping`（ICMP）或 `tcp`（端口探测）。`none` 表示未配置，不可触发校验。
- 轮询规则：**总时长上限 120 秒，间隔 5 秒**，单次探测超时 3 秒。
- 并发约束：同一设备同一时刻只允许一个校验任务。
- 状态取值固定五个：`pending`（待校验）、`checking`（校验中）、`online`（已上线）、`timeout`（超时未响应）、`failed`（校验失败）。
- 状态查询接口契约见 `docs/E3-contract.md`。

## 允许改动的文件

- `internal/probe/`（`doc.go` 更新注释，新增 `probe.go`、`runner.go` 与测试）
- `internal/store/`（`schema.go` 追加列迁移，新增 `status.go` 与测试）
- `internal/httpapi/`（新增 `check.go` 与测试，`server.go` 仅追加路由与依赖注入）
- `cmd/wold/main.go`（仅构造 Runner、调用启动重置、注入）
- `docs/E3-contract.md`、`docs/E3-acceptance.md`、`e3/task.md`

## 禁止事项

1. **禁止引入任务队列、调度组件、消息中间件、定时任务框架**。校验任务必须是进程内的 goroutine + 内存表，进程退出即消失。违反视为扩大开发，一票否决。
2. **禁止新增状态历史表或校验记录表**。只保留"最近一次结果"三个字段，不保存曲线、不保存每次尝试。这是排除清单内的"唤醒历史图表"。
3. **禁止新增环境变量或前端配置项**。120 秒与 5 秒写成常量，不允许用户调。
4. **禁止实现重试策略配置、指数退避、并发数控制**。固定节奏轮询，没有可调参数。
5. **禁止在 `internal/probe` 内 import `database/sql`、`net/http`、`internal/store`**。探测器必须能脱离本服务单独调用；Runner 通过接口访问状态存储。
6. **禁止修改 E1 已冻结的 9 个设备字段与四个设备接口的响应结构**。状态是独立资源，不塞进设备对象。
7. **禁止让触发接口同步等待校验结果**。触发立即返回，结果靠查询接口获取。
8. **禁止在状态查询接口返回探测命令的原始输出**（如 ping 的完整 stdout）。只返回归一化后的原因说明，避免把系统细节暴露到前端。

## 输出要求

1. 代码：
   - `internal/probe/probe.go`：`Probe(ctx, probeType, value) error`，返回 `nil` 表示在线；`ErrUnreachable` 表示本轮未响应（可重试）；`ErrToolMissing` / `ErrInvalidTarget` 表示终态失败（立即停止轮询）。
   - `internal/probe/runner.go`：`Runner.Start(deviceID, probeType, probeValue) error`，`ErrInProgress` 表示该设备已在校验中。
   - `internal/store/status.go`：`SetProbeStatus`、`ListProbeStatus`、`ResetInterruptedChecks`。
   - `internal/httpapi/check.go`：`POST /api/devices/{id}/check` 与 `GET /api/status`。
2. 测试：
   - ping 与 tcp 各一条在线正例、一条未响应反例。
   - 探测命令不存在 → `ErrToolMissing`。
   - Runner：首次在线、全程离线超时、重复触发拒绝、终态失败立即结束、取消时不再写状态。
   - 存储：默认 `pending`、状态更新、启动重置 `checking` → `pending`、旧库（无新列）可迁移。
   - 接口：404 / 400 / 409 / 202 / 200 与字段白名单。
3. 证据：`go test ./...` 输出、真实 120 秒超时的时间戳、重启前后状态对比、无 ping 时的状态与原因、反向检查 grep 结果。
4. 未做清单：见 `docs/E3-acceptance.md`「本次明确未做」。

## 验收方式

按开发计划 4.4 节 3-1 至 3-7 执行，结果记录在 `docs/E3-acceptance.md`：

```bash
go vet ./...
go test ./...

# 3-6 反向检查：不得引入队列与调度组件
grep -rniE "\b(queue|broker|scheduler|cron|redis|worker[ -]?pool)\b" internal/ cmd/

# 3-6 补充：校验任务必须是进程内实现
grep -rn "go r.loop\|go func" internal/probe/*.go

# 3-7 反向检查：不得新增配置项
grep -rn "LookupEnv\|os.Getenv" internal/probe/ internal/store/status.go internal/httpapi/check.go
```

**3-2 与 3-4 必须真机实测，不接受单元测试结果代替**：超时要真实等满 120 秒；重启要在 `checking` 状态下强杀进程，再确认状态不卡死。

## 完成定义

3-1 至 3-5 全部通过；3-6、3-7 反向检查零命中。任一状态都不会永久停留在 `checking`——无论是探测超时、进程重启还是探测工具缺失。
