# F9 心跳在线验收记录

契约见 `docs/heartbeat-contract.md`。全部为 2026-10-26 实测证据，环境：Windows 11 + Docker Desktop（Engine 29.8.2），被测镜像 `wol:1.1.0`（含心跳功能，bridge 网络 + 18093 端口映射）。

## 一、单元与接口测试

`go vet ./... && go test ./...` 全绿（golang:1.24-bookworm 容器内执行）：

```
ok  wol/internal/auth      0.016s
ok  wol/internal/config    0.015s
ok  wol/internal/httpapi   0.133s
ok  wol/internal/probe     2.328s
ok  wol/internal/store     5.859s
ok  wol/internal/wol       0.713s
```

新增用例：

| 用例 | 覆盖点 |
| --- | --- |
| `TestValidateHeartbeatSecret` / `TestValidateTargetAcceptsHeartbeat` | 密钥长度 16–64、字符集白名单；`ValidateTarget` 接受 heartbeat |
| `TestProbeRejectsHeartbeat` | 心跳误走主动探测时立即失败，不去拨空地址 |
| `TestHeartbeatStale` | 零值/1 秒/2 分/恰好 180 秒/180 秒+1/1 小时 的边界 |
| `TestHeartbeatAccepted` | 200、三个响应键、MAC 归一化（小写连字符写法）、写入 online |
| `TestHeartbeatRejectsWrongSecret` / `TestHeartbeatRejectsUnknownMACAndNonHeartbeat` | 三种失败统一 401 `heartbeat_rejected`，且不写状态 |
| `TestHeartbeatRejectsMalformedRequest` | 缺 secret / 非法 MAC / 未知字段 / 非 JSON |
| `TestHeartbeatWriteFailure` | 存储故障 → 500，不回显内部错误 |
| `TestStartCheckRejectsHeartbeatMode` | `/check` → 400 `heartbeat_mode` |
| `TestListStatusDerivesHeartbeatState` | 从未上报=pending；10 秒前=online；5 分钟前=timeout |
| `TestCreateHeartbeatDevice` | 过短密钥 400；合法密钥 201 |

## 二、容器实测（真实镜像 + 真实 HTTP）

| # | 场景 | 结果 |
| --- | --- | --- |
| 1 | 建心跳设备（probe_type=heartbeat，密钥 16 位） | 201，资源形状仍是 9 键 |
| 2 | `client/heartbeat.ps1 -Once` 上报（MAC 用连字符写法） | `心跳成功：status=online`，退出码 0 |
| 3 | `GET /api/status` | `online`，checked_at=心跳时刻 |
| 4 | 错误密钥上报 | 401 `heartbeat_rejected` |
| 5 | 对心跳设备调 `/check` | 400 `heartbeat_mode`，文案可操作 |
| 6 | 省略 `-Mac`（自动识别物理网卡 10:7B:44:80:25:B7，避开 Hyper-V 虚拟网卡） | 上报成功 |
| 7 | `-Server 127.0.0.1:18093`（省略 `http://`） | 自动补协议头 |
| 8 | 停止上报 3 分 51 秒后查询 | `timeout`，message="心跳超时：最近一次心跳在 3 分 51 秒前…" |
| 9 | 重新上报 | 立即回到 `online`（时间戳刷新） |

## 三、冒烟脚本

`deploy/smoke.sh --probe heartbeat`（对上述容器执行）：

```
== 汇总 ==
  PASS 9 / FAIL 0
```

新增两断言：心跳上报被接受（200 + online）、状态接口显示 online。

## 四、前端（真实 Chrome + CDP，headless）

脚本 `tmp/hb-ui.mjs`，9/9 通过：

```
[PASS] 登录成功并进入设备页
[PASS] 校验方式含「心跳上报」
[PASS] 选中后字段变为「心跳密钥」
[PASS] 字段提示说明上报间隔
[PASS] 未上报时显示「等待心跳」
[PASS] 页面内上报心跳返回 online
[PASS] 刷新后显示「已上线」
[PASS] 唤醒后提示等待心跳
[PASS] 临时设备已删除（204）
```

截图（`tmp/shots-hb/`）确认：超时卡片显示橙色"超时未响应"徽标 + 超时说明；在线卡片显示绿色"已上线"；最近结论显示本地时区的心跳时刻。`sw.js` 缓存版本已升到 `wol-static-v2`。

## 五、Avalonia 客户端（client/WolHeartbeat，2026-10-26 补充）

.NET 10 + Avalonia 12.1.1，`dotnet build` 零警告，单文件自包含发布 99 MB（`-p:PublishSingleFile=true`）。

| # | 场景 | 结果 |
| --- | --- | --- |
| 1 | 发布版 `WolHeartbeat.exe --once --server http://127.0.0.1:18096 --secret …` | `上报成功 · 服务端建议间隔 60 秒`，退出码 0，日志落在 exe 同目录 `logs\heartbeat-2026-10-26.log` |
| 2 | 错误密钥 `--once` | 退出码 1，日志 `心跳被拒绝：被拒绝（401）…` |
| 3 | 密钥过短（<16 位） | 退出码 2，日志 `配置有误：心跳密钥需为 16–64 位…` |
| 4 | 服务端口不可达（系统代理开着） | 退出码 1，`无法连接服务器：…积极拒绝`——已验证 `UseProxy=false` 生效（修复前同一场景被系统代理劫持成 HTTP 502） |
| 5 | GUI 启动（config.json 预置合法配置 + startOnLaunch） | 启动即自动开始上报，日志连续出现 `心跳成功` |
| 6 | 界面截图检查（GDI 抓帧） | 状态徽标绿色「上报正常」、`最近上报 21:52:18 · 上报成功`、`下次上报：约 54 秒后` 倒计时、四个按钮与两个复选框、日志区实时滚动、页脚显示日志路径，无重叠/截断 |
| 7 | 保存的 config.json | 位于程序目录，字段与界面一致 |

设计要点（对应契约第八节）：

- 日志按天分文件，超 2 MB 轮转 `.1`，保留 7 天；程序目录不可写时退到 `%LOCALAPPDATA%\WolHeartbeat\logs` 并在页脚提示；
- 「登录 Windows 时自动启动」写 HKCU `Software\Microsoft\Windows\CurrentVersion\Run`（无需管理员），开发期以 dotnet.exe 运行时自动跳过；
- 单实例保护（命名 Mutex），重复启动直接退出，避免同一台机器双重上报；
- MAC 自动识别跳过回环/隧道/虚拟网卡（Hyper-V、VirtualBox、WSL 等），优先选有默认网关的物理网卡。

离线包 `deploy/package-offline.sh` 会把发布出的 `WolHeartbeat.exe` 一并放进 `dist/client/`（未发布时自动跳过，只带脚本版）。

## 六、遗留与说明

- `timeout` 状态由读取时推算，数据库里仍是最后一次的 `online`——这是设计行为（无后台清扫任务），见契约第四节。
- E8（真实目标机关机 → 唤醒 → 客户端随系统启动 → 心跳把状态拉回在线）需要一台能装客户端的真机做端到端，仍待用户实机执行；`client/README.md` 已给出从验证到开机常驻的完整命令。
