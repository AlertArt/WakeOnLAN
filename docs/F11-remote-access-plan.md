# F11 远程访问开发计划：状态监控 + 网页远程桌面 + 网络回滚安全网

> 背景与定位：目标机为 **Win10 企业版**（RDP 服务端原生支持）；人在外部网络通过 NAS 上已暴露的 WakeOnLAN 页面操作；本套功能定位为**商用远控软件失效后的兜底手段**。
> 前置结论见 `eval-remote-desktop.md` 第五节与 `eval-monitoring.md`。总体分四个里程碑，串行实施，每个里程碑独立可验收、独立可交付。

## 0. 全局前提（一次性，开工前确认）

| 项 | 要求 | 验证方式 |
| --- | --- | --- |
| 客户端安装方式 | WolHeartbeat 以 **SYSTEM 计划任务**安装（开机即活、无需登录、天然免 UAC） | 目标机执行 `schtasks /query /tn WolHeartbeat`，状态"正在运行"，`whoami /user` 验证会话为 SYSTEM |
| WakeOnLAN 管理口令 | 弃用弱口令，≥12 位随机（NAS 已暴露公网，这是整条链路的第一道门） | 登录页手测 + `deploy/smoke.sh` 错误口令 401 |
| NAS 对外访问 | 走 HTTPS（NAS 自带反代或 frp+TLS）；wold 与 Guacamole 端口**不对公网直开**，仅经反代 | 外部扫描端口仅见 443 |
| 容器资源 | NAS 预留 ≥512MB 内存给 guacamole+guacd | `docker stats` 观察 |

---

## M1：心跳轻量指标（预计 1–2 天）

**目标**：不进桌面，在设备卡片上直接看 CPU / 内存 / 磁盘 / 网络速率 / 开机时长 / IP，以及断网前的最后状态。

### 服务端

1. 心跳请求体扩展可选 `metrics` 对象（F9 契约向后兼容：无此字段行为不变）：
   ```json
   { "mac": "...", "secret": "...",
     "metrics": { "cpu": 12, "mem_used": 6442, "mem_total": 16384,
                  "disk_used": 233, "disk_total": 476,
                  "net_up": 1024, "net_down": 8192,
                  "ip": "192.168.1.66", "uptime_s": 86400,
                  "hostname": "OFFICE-PC", "os": "Windows 10 企业版" } }
   ```
   字段校验：数值范围钳制（cpu 0–100，速率 0–10^9，字符串 ≤64 字符），异常值丢弃不报错。
2. 存储：`device` 表新增 `metrics_json`、`metrics_at` 两列（只存最新一份，M1 不做历史曲线；24h 曲线列为可选延展项，不在本里程碑）。
3. 接口：`GET /api/status` 响应的每个设备带 `metrics` 与 `metrics_at`；前端据此渲染。

### 客户端（WolHeartbeat）

1. 新增 `MetricsCollector.cs`：
   - CPU：`PerformanceCounter("% Processor Time", "_Total")`（首次取样丢弃，计数器需两次采样）；
   - 内存：`GlobalMemoryStatusEx`；
   - 磁盘：`DriveInfo` 汇总固定盘；
   - 网络速率：`PerformanceCounter("Network Interface","Bytes Sent/Received/sec", 实例名)`，实例按物理网卡匹配（复用 MacUtil 的过滤逻辑）；
   - IP：物理网卡 IPv4（排除虚拟网卡，同 MacUtil）；
   - uptime：`Environment.TickCount64`；hostname/os：`Environment`。
2. 心跳请求附带 metrics；采集异常（计数器不存在/权限不足）单字段降级为缺省，不影响心跳本身。
3. 托盘悬停提示保持现状，不塞指标。

### 网页端

1. 设备卡片在状态 chip 下加一行指标条：`CPU 12% · 内存 39% · D盘 49% · ↑1.0M/s ↓8.2M/s · 运行 1天3时`；
2. 设备离线时指标条变灰并标注"最后上报 14:32"（用 metrics_at）；
3. Service Worker 缓存版本 +1。

### M1 验收标准（全部满足才算完成）

| # | 场景 | 通过标准 |
| --- | --- | --- |
| M1-1 | 单元测试 | metrics 字段钳制/缺省/类型错误全部有测试覆盖；`go test ./internal/...` 全绿 |
| M1-2 | 兼容性 | **不带 metrics 的旧客户端心跳行为完全不变**（回归 F9 全部用例） |
| M1-3 | 真机上报 | 目标机运行新版客户端 ≥5 分钟，`GET /api/status` 可见合理指标（CPU 波动、内存接近任务管理器值 ±10%） |
| M1-4 | CPU 准确性 | 目标机跑 stress 压测 30 秒，上报 CPU >80%；空闲时 <20% |
| M1-5 | 网络速率 | 目标机下载大文件时 net_down 明显上升；空闲时接近 0 |
| M1-6 | 断网表现 | 拔目标机网线：心跳超时转离线，指标条冻结并显示最后上报时间 |
| M1-7 | 采集容错 | 停用某 PerformanceCounter（或改注册表制造异常）后心跳仍成功，仅对应字段缺失 |
| M1-8 | 性能 | 客户端稳态内存增量 <30MB；采集周期开销可忽略（Task Manager CPU 占用 ≈0%） |

---

## M2：enable_rdp 指令 + Guacamole 网关接入（预计 5–7 天）✅ 已完成

> **实施状态（v2.0.0-beta）**：代码、自动化测试与文档全部落地；验收记录见 `docs/F11-M2-acceptance.md`。
> 方案有一处调整：Guacamole 集成没有走"管理 REST API + JDBC + 数据库"（需要 3 个容器、wold 要持有管理员口令），
> 改用官方 `guacamole-auth-json` 扩展——wold 用共享密钥签名 JSON 载荷，Guacamole 验签即建连，
> 容器数降为 2（guacd + guacamole），无数据库、无管理员凭据，符合"NAS 侧不引入数据库"的约束。

**目标**：网页点「远程桌面」→ 自动开启目标机 RDP → 浏览器新标签页进入完整 Windows 桌面；手机/平板/任何浏览器可用。

### 服务端（wold）

1. F10 指令通道新增指令类型（`docs/F10-command-contract.md` 同步更新）：
   - `enable_rdp`：`reg add ... fDenyTSConnections=0` + 防火墙放行远程桌面组 + 确保 NLA（`UserAuthentication=1`）；
   - `disable_rdp`：反向。
2. `enable_rdp` 成功回执后，服务端把该设备的 `rdp_enabled=1`（由客户端回执携带，服务端不猜测）。
3. Guacamole 集成（NAS 侧新增容器，wold 不内嵌流媒体）——**按 auth-json 方案落地**：
   - `deploy/docker-compose.nas.yml` 增加 `guacd` + `guacamole` 两个服务，加入独立内网 `guac`，guacd 无宿主端口，guacamole 默认只绑 `127.0.0.1` 可达的 8088；两容器放 `rdp` profile，不启用远程桌面则不启动；凭据经环境变量注入；
   - wold 新增 `internal/guac` 模块：用共享密钥（`WOL_GUAC_URL/WOL_GUAC_SECRET`）把 RDP 连接参数（hostname=心跳上报 IP，port 3389，NLA）签成 auth-json 载荷——`JSON → HMAC-SHA256（二进制签名前置）→ AES-128-CBC（全零 IV）→ base64`，算法与官方 `guacamole-auth-json` 一致；
   - 接口：`POST /api/devices/{id}/rdp-session` → 校验（心跳设备/在线/rdp_enabled/已配置 guac/有 IP）→ 签发 32 位十六进制一次性票据（60 秒），返回 `{ "url": "/rdp/<ticket>", "expires_in": 60 }`；浏览器访问 `GET /rdp/{ticket}` 时才把票据兑换成 auth-json 载荷并 302 跳转到 `<WOL_GUAC_URL>/#/?data=<载荷>`（载荷在 fragment 里，不进任何服务端日志）；
   - 未配置 Guacamole 环境变量时接口返回 503 与明确错误（功能降级但不出 500）；只配一半时进程拒绝启动；
   - 载荷生成、票据存储与兑换有单元/接口级测试；与真实 Guacamole 1.6.0 做过联调（合法载荷 200、过期载荷 403）。
4. 心跳 metrics 已含 `ip`（M1），RDP connection 用它；IP 变化时下次创建自动更新。

### 客户端

1. `CommandRunner` 增加 `enable_rdp` / `disable_rdp` 分支：四步走——注册表总开关 → 防火墙**按规则名**放行（`RemoteDesktop-UserMode-In-TCP/UDP`，与系统语言无关；不用组名，中文系统组名是「远程桌面」）→ NLA（`UserAuthentication=1`）→ 确保 TermService 运行；逐条执行、逐条记录日志；
2. 执行后回执 message 带结果摘要（如 `RDP 已启用` / `防火墙规则失败：...`）；
3. 非提权运行时若 reg 写入失败，回执 `ok=false, message="需要 SYSTEM/管理员方式运行客户端"`。

### 网页端

1. 心跳设备卡片新增「远程桌面」按钮：仅当 `rdp_enabled` 且在线时可点，否则置灰并悬浮提示原因；
2. 点击 → 确认弹层（说明将创建一次性会话票据）→ `POST rdp-session` → `window.open(url)`；
3. 卡片显示 RDP 状态 chip（未启用/已启用）。

### NAS 部署物

1. `deploy/package-offline.sh` 与安装说明更新：镜像 tar 增加 guacamole/guacd 两层（或引导 NAS 在线拉取）；
2. `deploy/smoke.sh` 增加 `--rdp` 可选段（不依赖真机时跳过）。

### M2 验收标准

| # | 场景 | 通过标准 |
| --- | --- | --- |
| M2-1 | 单元/接口测试 | 指令校验、rdp-session 的 4 类拒绝（非心跳/离线/未启用/未配置 guac）各有测试；`go test` 全绿 |
| M2-2 | 指令真机下发 | 网页对在线目标机下发 enable_rdp → 60 秒内回执"RDP 已启用"，目标机 `reg query fDenyTSConnections` = 0，`netstat -an \| findstr 3389` 处于 LISTENING |
| M2-3 | 一次性票据 | rdp-session 返回的 URL 60 秒后再次访问被拒；同 URL 二次使用被拒 |
| M2-4 | 外部浏览器连入 | 手机浏览器（走公网）登录 WakeOnLAN → 点远程桌面 → 新标签出现 Windows 登录界面 → 输入凭据进入桌面 |
| M2-5 | 桌面可用性 | 桌面内完成：打开资源管理器、复制粘贴文本、中文输入法输入、分辨率正常不糊 |
| M2-6 | **改网络设置不断连**（核心场景） | 在 Guacamole 会话内把目标机 IP 改为静态另一地址 → 应用后**会话保持存活**（内网路径不受影响），稍后按新 IP 重新连入成功 |
| M2-7 | 会话收尾 | 关闭标签页后 Guacamole 会话结束；wold 不残留连接对象（重启 wold 无影响） |
| M2-8 | disable_rdp | 下发后 3389 停止监听，卡片 chip 变"未启用"，rdp-session 拒绝 |
| M2-9 | 容器回归 | 原 wold 容器功能不受影响：`deploy/verify-e7.sh` 全绿 + `smoke.sh` 9 项通过 |
| M2-10 | 安全 | Guacamole 管理口令非默认；票据/连接参数不出现在 wold 日志明文中；guac/guacd 端口无公网映射（`docker ps` 核对） |

---

## M3：网络回滚安全网 net_revert（预计 0.5–1 天）

**目标**：改网络设置前一键布防——目标机快照当前网络配置并预置定时回滚；改坏了自动恢复，改对了手动解除。

### 契约

- 指令 `net_revert_arm`，payload `{ "after_minutes": 5 }`（1–30，默认 5）：
  客户端执行 `netsh -c interface dump > <exe目录>\net-backup.txt` → 创建 SYSTEM 计划任务 `WolNetRevert`，在 +N 分钟运行 `netsh -f net-backup.txt`（若取消文件存在则跳过并自删）→ 回执含快照路径与计划任务时间。
- 指令 `net_revert_disarm`：删除计划任务与快照文件，回执确认。
- 回滚执行后客户端在下次心跳回执 message 附带 `net_revert_fired`，网页展示"已自动回滚网络配置"。

### 服务端 + 网页端

1. 指令校验复用 F10 状态机（同设备单指令、TTL、过期）；
2. 设备卡片加「网络回滚保护」入口：弹层选择分钟数 → 下发 arm → 卡片 chip 显示"回滚保护至 14:52"；点击可 disarm；
3. F11 全程操作说明写入安装文档："先布防 → RDP 里改网络 → 正常则解除，异常等自动回滚"。

### M3 验收标准

| # | 场景 | 通过标准 |
| --- | --- | --- |
| M3-1 | 单元测试 | arm/disarm 校验、分钟数钳制、同设备单指令约束覆盖 |
| M3-2 | 布防 | 下发 arm(1 分钟) → 目标机存在 net-backup.txt 与计划任务，`schtasks /query /tn WolNetRevert` 确认 |
| M3-3 | 自动回滚（真机） | RDP 会话内改错 IP（如 10.255.255.1）→ 不做任何操作 → 约 1 分钟后目标机网络恢复原配置，心跳恢复在线，网页出现"已自动回滚" |
| M3-4 | 手动解除 | 改成正确 IP → 网页点解除 → 计划任务与快照删除，回执确认 |
| M3-5 | 断链兜底 | 布防后**直接拔掉目标机网线**（wold 无法下发任何指令）→ 计划任务照常触发 → 插回网线后设备以原配置回到在线 |
| M3-6 | 审计 | arm/disarm/fired 全部出现在 `GET /api/commands` 与客户端日志 |

---

## M4（可选收尾）：.rdp 下载入口（0.5 天）

内网 Windows 电脑直连体验更好：卡片加「下载 .rdp」，服务端按 metrics.ip 生成文件（screen mode/auto logon 不预填，凭据留空）。验收：文件可导入 mstsc，连接成功；非心跳设备无此按钮。

---

## 里程碑与工作量汇总

| 里程碑 | 内容 | 估时 | 依赖 |
| --- | --- | --- | --- |
| M1 | 心跳轻量指标 + 卡片指标条 | 1–2 天 | 无 |
| M2 | enable_rdp 指令 + Guacamole 网关 + 一次性票据 | 5–7 天 | M1（需要 ip 字段） |
| M3 | net_revert 安全网 | 0.5–1 天 | M2（在 RDP 会话里操作才有意义） |
| M4 | .rdp 下载 | 0.5 天 | M2 |

总量约 **8–11 天**。M1 结束即可先部署一版（看状态的能力独立有价值）；M2 结束是核心交付；M3 强烈建议同批上线。

## 主要风险与对策

| 风险 | 对策 |
| --- | --- |
| Guacamole REST API 版本耦合 | 交互收敛在 `internal/guac` 单模块，httptest 假服务端做契约测试；版本锁定镜像 tag |
| 客户端需 SYSTEM 才能开 RDP/回滚 | M0 前提核实；非提权时回执明确错误信息而非静默失败 |
| PerformanceCounter 实例名因机器而异 | 单字段降级设计（M1-7），缺失不致命 |
| NAS 内存紧张 | guacamole+guacd 限内存（compose mem_limit），超限重启不影响 wold |
| 公网暴露面变大 | 一次性 60 秒票据 + Guacamole 自身认证 + 强口令 + 仅 443 反代（M0 前提） |
| 改网络期间真机失联无法远程修复 | M3 回滚覆盖；最坏情况保留 WOL 断电重启 + 本地接显示器兜底说明写入文档 |

## 版本与交付

- **F11 整体升 V2.0.0**：远程桌面是全新能力域，部署形态变化（compose 新增 guacd/guacamole 容器）、心跳契约扩展、新增 `WOL_GUAC_*` 环境变量，语义化版本够 major 标准；
- 交付节奏：M1 完成后先打 **v2.0.0-beta.1**（仅指标功能，可独立部署试运行）；M2 + M3 全部验收通过后发布正式 **v2.0.0**；M4 若同期完成并入，否则留 v2.0.1；
- 发布前全量回归：`verify-e7.sh` + `smoke.sh` + 离线包重打（镜像 tar 含三个容器镜像/引导在线拉取）+ GitHub Release（Release 说明注明 compose 结构变化与新增环境变量、**升级部署需重建 compose 而非仅换镜像**）；
- 验收记录分别落在 `docs/F11-metrics-acceptance.md`、`docs/F11-rdp-acceptance.md`、`docs/F11-revert-acceptance.md`（沿用 F10 格式：环境、步骤、实际结果、遗留问题）。
