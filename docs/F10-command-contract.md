# F10 远程关机/重启契约（冻结）

本文件定义"远程关机/重启"。它和心跳共用同一条**纯出站**通道：服务端不主动连目标机，指令随心跳回执下发，回执随下一次心跳上送。

## 一、为什么不用"服务端直连执行"

| 方案 | 前提 | 结论 |
| --- | --- | --- |
| 服务端 `shutdown /m \\host` | 目标机开放 445 + 管理员凭据 + 防火墙放行 | 家庭/办公 Windows 默认全不满足，等于不可用 |
| 服务端 SSH/WinRM | 目标机跑服务并开放端口 | 与 F9 的"零入站端口"前提冲突 |
| **心跳捎带指令** | 只要目标机能访问服务端 | 与心跳同一条连接，零新增网络依赖 |

因此 F10 只支持 `probe_type = heartbeat` 的设备。其它设备返回 `400 not_heartbeat`，不静默排队。

## 二、数据模型

新表 `device_command`（不改 `device` 表）：

| 列 | 类型 | 说明 |
| --- | --- | --- |
| `id` | INTEGER PK | 指令编号，回执与界面都用它 |
| `device_id` | INTEGER | 目标设备 |
| `type` | TEXT | `shutdown` / `restart` |
| `delay_seconds` | INTEGER | 客户端执行前的缓冲，10–600，默认 60 |
| `force` | INTEGER | 1 = 强制关闭未保存的进程（`/f`） |
| `status` | TEXT | `pending` → `delivered` → `done` / `failed`；未送达超时则 `expired` |
| `message` | TEXT | 客户端回执说明，或失败原因 |
| `created_at` / `delivered_at` / `finished_at` / `expires_at` | TEXT | RFC3339（UTC） |

状态机：

```
pending --(心跳取走)--> delivered --(回执 ok)--> done
                                   \--(回执 失败)--> failed
   \--(超过 expires_at 仍未取走)--> expired
```

同一设备**同时只允许一条未完成指令**（`pending`/`delivered`），重复下发给 `409 command_pending`，避免"关机 + 重启"互相打架。

## 三、接口：下发指令

```
POST /api/devices/{id}/commands          （需管理令牌）

{ "type": "shutdown", "delay_seconds": 60, "force": false }
```

`delay_seconds` 省略时为 60，`force` 省略时为 false。

成功 `201`：

```json
{ "id": 12, "device_id": 3, "type": "shutdown", "delay_seconds": 60, "force": false,
  "status": "pending", "message": "", "created_at": "...", "expires_at": "..." }
```

失败：

| 状态 | error | 触发条件 |
| --- | --- | --- |
| 400 | `invalid_json` / `invalid_command` | 字段非法、`type` 不在枚举内、`delay_seconds` 越界 |
| 400 | `not_heartbeat` | 设备不是心跳校验方式（无下发通道） |
| 404 | `not_found` | 设备不存在 |
| 409 | `device_offline` | 最近心跳超过阈值，设备当前不在线 |
| 409 | `command_pending` | 已有未完成指令 |

> 要求"在线"是硬约束：离线设备的指令只能等到下次开机才可能送达，那时用户早已忘了自己点过什么——这种"幽灵关机"必须杜绝。

## 四、接口：查询指令

```
GET /api/devices/{id}/commands?limit=20    （需管理令牌）  → 200 [Command...] 按 id 倒序
GET /api/commands?limit=20                 （需管理令牌）  → 200 [{..., "device_name": "客厅主机"}]
```

`limit` 默认 20，范围 1–100。

## 五、心跳捎带（协议扩展，向后兼容）

心跳回执新增两个字段：

```json
{ "status": "online", "interval_seconds": 60, "timeout_seconds": 180,
  "features": ["commands"],
  "commands": [ { "id": 12, "type": "shutdown", "delay_seconds": 60, "force": false,
                  "expires_at": "2026-11-02T10:10:00Z" } ] }
```

心跳请求新增**可选** `acks`：

```json
{ "mac": "AA:BB:CC:DD:EE:FF", "secret": "hb-...",
  "acks": [ { "id": 12, "ok": true, "message": "已执行 shutdown /s /t 60" } ] }
```

兼容性：

- 旧客户端不读 `features` / `commands`，字段被忽略，行为不变；
- 旧服务端不认识 `acks` 会按"未知字段"回 `400`。因此客户端**只有在回执里见过 `features` 含 `commands` 时才发送 `acks`**，避免升级顺序造成的假故障；
- 取走指令即置 `delivered`，客户端若因断电未执行也不会重复执行：`delivered` 不再下发，靠 `expires_at` 与界面状态告知用户；
- 指令 5 分钟未取走即 `expired`（`expires_at = created_at + 300s`）。

## 六、客户端执行规则

1. `shutdown` → `shutdown.exe /s /t <delay> [/f] /c "远程关机：WakeOnLAN 管理端"`
2. `restart` → `shutdown.exe /r /t <delay> [/f] /c "远程重启：WakeOnLAN 管理端"`
3. 延时下限 10 秒，给用户 `shutdown /a` 撤销的机会；托盘菜单提供"取消关机（撤销）"；
4. 执行结果（含 Windows 返回码）写入日志，并在下一次心跳回执里上报；
5. `WOL_SHUTDOWN_DRYRUN=1` 或 `--dry-run` 时只记录不执行，用于验收与演示。

## 七、安全

- 下发接口需管理令牌；指令表记录时间与结果，可在界面追溯；
- 客户端只执行服务端下发的 `shutdown` / `restart`，不接受任意命令字符串（无 shell 注入面）；
- 心跳密钥即设备凭据：拿到密钥的人能让该设备关机，因此密钥按口令对待，不入日志、不入截图。
