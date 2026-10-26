# F9 心跳在线契约（冻结）

本文件定义"心跳上报"这种在线校验方式。它复用设备资源的既有字段，**不新增表、不新增列、不改变设备资源形状**（仍是 E1 契约的 9 个键）。

## 一、为什么需要它

F3 的两种主动探测各有一个硬前提：

| 方式 | 前提 | 在 Windows 上的现实 |
| --- | --- | --- |
| `ping` | 目标机回应入站 ICMP | Win10/11 默认丢弃入站回显请求，主机开着也永远"超时未响应" |
| `tcp` | 目标机监听某个端口 | 没开文件共享/远程桌面就没有端口可探 |

心跳把方向反过来：**目标机定时打电话回家**。它只需要目标机能访问服务端，不需要目标机开放任何入站端口，因此对 Windows 尤其合适。

## 二、数据映射

| 语义 | 字段 | 取值 |
| --- | --- | --- |
| 校验方式 | `probe_type` | `heartbeat`（与 `none` / `ping` / `tcp` 并列） |
| 上报密钥 | `probe_value` | 16–64 位，只允许 `A-Z a-z 0-9 _ -` |
| 最近一次心跳 | `probe_checked_at` | 每次上报刷新为服务端当前时间 |
| 状态 | `probe_status` | 上报时写 `online`；离线状态不落库，读取时推算 |

> 为什么密钥不单独建列：设备资源已被 E1 冻结为 9 个键，加字段会让所有客户端与前端同步返工。`probe_value` 在 `heartbeat` 模式下没有"探测目标"可存，用它存密钥是零成本复用。

## 三、接口：上报心跳

```
POST /api/heartbeat
```

- **公开接口，不带管理令牌**：目标机上的客户端没有也无法保存管理口令。
- 请求体（只接受这两个键，未知字段一律拒绝）：

```json
{"mac":"AA:BB:CC:DD:EE:FF","secret":"hb-7f3c9a2d5e8b1f4c"}
```

- 成功 `200`：

```json
{"status":"online","interval_seconds":60,"timeout_seconds":180}
```

`interval_seconds` 是建议上报间隔，客户端不必自己写死。

- 失败：

| 状态码 | `error` | 触发条件 | 客户端应做什么 |
| --- | --- | --- | --- |
| 400 | `invalid_json` | 请求体不是合法 JSON / 含未知字段 | 修正客户端 |
| 400 | `invalid_heartbeat` | MAC 格式非法，或 `secret` 为空 | 修正客户端 |
| 401 | `heartbeat_rejected` | MAC 不存在、设备不是心跳方式、密钥不匹配 | **三者合并为同一个码**，避免同网段的人用它枚举设备 |
| 500 | `internal` | 写状态失败 | 重试 |

## 四、状态推算（读取时计算）

`GET /api/status` 对 `probe_type=heartbeat` 的设备不走数据库里的 `probe_status`，而是按 `probe_checked_at` 与当前时间计算：

| 条件 | 返回 `status` | `message` |
| --- | --- | --- |
| `checked_at` 为空（从未上报） | `pending` | 尚未收到心跳，请确认目标机上的客户端已启动 |
| 距上次心跳 ≤ 180 秒 | `online` | 空 |
| 距上次心跳 > 180 秒 | `timeout` | 心跳超时：最近一次心跳在 X 前，请确认目标机上的客户端正在运行 |

- `checked_at` 始终返回真实的心跳时间，前端据此显示"最近结论"。
- **不写库、不跑后台清扫任务**：读取时算一次，比定时任务简单，也不会因为进程重启丢掉判断。
- 说明文案用"X 前"这种相对时长，不写具体时刻——容器里通常没有 tzdata，`Local` 就是 UTC，写时刻会误导。

## 五、与主动校验的关系

- `POST /api/devices/{id}/check` 对心跳设备返回 `400 heartbeat_mode`：
  "该设备使用心跳校验，无法主动探测；唤醒后请等待客户端上报，最长 3 分钟"。
- 前端唤醒流程对心跳设备**不调 check**：发完包后每 5 秒读一次 `/api/status`，最多等 200 秒，只把 `online` 当成功，`timeout` 在窗口内忽略（刚唤醒的机器在客户端起来前本来就该是超时）。

## 六、常量

| 常量 | 值 | 依据 |
| --- | --- | --- |
| `HeartbeatInterval` | 60 秒 | 一天 1440 次请求，SQLite 写入压力可忽略 |
| `HeartbeatTimeout` | 180 秒 | 3 倍间隔，允许连续丢 1–2 次而不误判离线 |

两者都在 `internal/probe/heartbeat.go`，不通过环境变量开放——调大调小没有实际收益，反而多一个部署时的出错点。

## 七、安全边界（明确写出，避免高估）

- 接口公开，但**没有密钥写不进任何状态**；密钥按设备隔离，泄露一台不影响其他设备。
- 密钥比较使用 `crypto/subtle.ConstantTimeCompare`，不做前缀短路。
- 未实现速率限制：已知 MAC + 密钥者可以刷自己设备的 `checked_at`，影响范围仅限该设备显示为在线。攻击者要造成这点影响，先得拿到管理口令才能读到密钥。
- 心跳不携带任何主机信息（只发 MAC 与密钥），服务端也不回传设备详情，避免把服务端变成信息泄露点。

## 八、客户端

`client/heartbeat.ps1`（Windows PowerShell 5.1+）：

```powershell
# 先验一次配置
powershell -NoProfile -ExecutionPolicy Bypass -File heartbeat.ps1 `
  -Server http://192.168.1.10:8080 -Secret hb-7f3c9a2d5e8b1f4c -Once

# 常驻上报（默认 60 秒一次，MAC 自动识别）
powershell -NoProfile -ExecutionPolicy Bypass -File heartbeat.ps1 `
  -Server http://192.168.1.10:8080 -Secret hb-7f3c9a2d5e8b1f4c
```

- 只发起出站 HTTP 请求，**不需要在目标机防火墙上开任何入站规则**。
- 不是 Windows 服务、不装驱动；常驻靠任务计划程序，见 `client/README.md`。
- 上报失败按 15 秒重试（不必等满一个周期），并在日志里区分 401（密钥/MAC 不对）、404（地址不对）与其他网络错误。
