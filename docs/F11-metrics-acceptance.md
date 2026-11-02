# F11-M1 心跳轻量指标验收记录

- 日期：2026-11-02
- 版本：服务端 main（本提交）、客户端 v2.0.0-beta.1
- 环境：Windows 开发机，Go 1.24（tmp/gotc），.NET 10；服务端 `go run ./cmd/wold`（127.0.0.1:18098/18099），客户端 `WolHeartbeat.exe --once`

## 结论

**自动化验收全部通过：Go 单测全绿（新增 9 个用例）、端到端 14/14、Chrome CDP UI 6/6。**
遗留人工场景（需真实目标机）：M1-3 持续上报观测、M1-4 CPU 压测、M1-5 网速压测、M1-6 断网冻结展示——随 beta 部署后在目标机确认。

## 一、单元测试（go test ./internal/... 全绿）

| 用例 | 覆盖 |
| --- | --- |
| TestMetricsClamp / TestMetricsClampAllEmpty | 负数/NaN/超上限/超长字符串钳制；全空快照判定不可用 |
| TestSaveMetricsRoundTrip | 写入→ListProbeStatus 读回，字段与时间一致 |
| TestSaveMetricsOverwriteAndNotFound | 覆盖写；设备不存在返回 ErrNotFound |
| TestHeartbeatSavesMetrics / SavesPartialMetrics | 合法与部分非法指标落库内容正确 |
| TestHeartbeatSanitizesBadMetrics | 类型错/负数/超上限被钳制，心跳仍 200 |
| TestStatusOmitsMetricsForLegacyClients | 旧格式心跳不产生 metrics 键（兼容性） |
| TestStatusReturnsMetrics | /api/status 透出 metrics + metrics_at |
| 兼容回归 | F9/F10 全部既有用例不变通过（含 features 扩为 [commands metrics]） |

## 二、端到端（tmp/m1-e2e.py，14/14 PASS）

服务端本地启动 + 真实客户端 exe `--once` 上报：

1. 服务端启动 / 管理员登录 / 创建心跳设备 ✅
2. 客户端心跳 rc=0；**首次上报后自动补发一次把指标送达**（features 协商在首次成功之后，--once 模式只有一次机会）✅
3. /api/status 返回 metrics + metrics_at ✅
4. 指标值合理：CPU 0–100、内存 used≤total>1GB、磁盘 total>10GB、IP/主机名/Windows 系统、开机时长>60s ✅
5. 畸形指标（cpu:-99、net_down:1e15、ip:42、hostname:{}）：心跳 200，坏字段钳制/丢弃 ✅
6. 旧格式心跳（无 metrics）：200，行为不变 ✅
7. 重启服务端后指标仍在（SQLite 持久化，非内存）✅

## 三、UI（tmp/m1-ui.mjs，Chrome CDP 6/6 PASS）

1. 心跳设备卡片出现指标条：`CPU 39% · 内存 51% · 硬盘 33% · 运行 18时45分 · 192.168.1.11` ✅
2. 排版协调无溢出（截图核对 tmp/shots-m1/1-metrics-card.png）✅
3. 无指标的设备（Ping 校验）不显示指标条 ✅

## 四、实现要点（备查）

- 服务端：`device` 表加 `metrics_json`/`metrics_at` 两列（ensureColumns 幂等迁移）；心跳请求的 `metrics` 按宽松 map 解析，未知键忽略、类型错丢弃、数值钳制（Clamp）；全空快照不落库；指标写失败仅告警不影响心跳 200
- 契约：heartbeat features 扩为 `["commands","metrics"]`；客户端只在上报时见过 `metrics` 能力才发送（旧服务端不 400）
- 客户端：`MetricsCollector.cs`（PerformanceCounter CPU/网速 + GlobalMemoryStatusEx 内存 + DriveInfo 磁盘 + 网卡 IPv4/开机时长/主机名/系统），单字段异常各自降级；汇编级 `SupportedOSPlatform("windows")` 消除平台告警
- 客户端发布：单文件 exe `v2.0.0-beta.1`（页脚同步），编译零警告

## 五、遗留与人工验收清单（目标机上做）

- [ ] M1-3 常驻运行 ≥5 分钟，网页指标随时间变化
- [ ] M1-4 压测 CPU（如跑 stress/编译任务），网页 CPU 应 >80%；空闲 <20%
- [ ] M1-5 大文件下载，网页 ↓速率明显上升
- [ ] M1-6 拔网线：转离线后指标条变灰、悬浮显示"最后上报时间"
- [ ] M1-7（可选）停用计数器场景下单字段缺失不影响在线状态
- [ ] M1-8 客户端稳态内存增量 <30MB（任务管理器观察）
