# F11 M2 验收记录：远程桌面（enable_rdp + Guacamole 网关 + 一次性票据）

对应计划：`docs/F11-remote-access-plan.md` 第二节（M2）。
验收日期：2026-11-02。版本：服务端 wol:2.0.0-beta.2 / 客户端 WolHeartbeat 2.0.0-beta.2 / Guacamole 1.6.0。

标记说明：✅ 自动化通过（有脚本与输出为证）；👤 需在目标机/真机上人工执行（本文列出步骤）。

---

## M2-1 单元/接口测试 ✅

```text
gofmt -l internal/ cmd/     → 无输出（格式全部合规）
go vet ./cmd/... ./internal/... → 通过
go test ./internal/... ./cmd/... → 全绿：
    wol/internal/guac      16 项（载荷往返、换密钥解不开、篡改被 HMAC 拦、
                                  URL 转义且参数不进 RawQuery、ParseConfig 校验、
                                  票据一次性/过期/未知 ID/签发时清理）
    wol/internal/httpapi  105 项（rdp-session 的 4 类拒绝：非心跳 400/离线 409/
                                  未启用 409/未配置 503/无 IP 409；302 跳转与
                                  no-store；二次使用 410；日志票据占位符；
                                  RDP 指令落库不带 delay；心跳回执更新/不猜 RDP 状态）
    wol/internal/store     31 项（含 rdp_enabled 迁移与 SetRDPEnabled）
    其余包（auth/config/probe/wol）全部通过
dotnet build（客户端）→ 0 警告 0 错误
```

客户端 UI 链路另有 41 项浏览器自动化（`tmp/m2-ui.mjs`，临时脚本不入库）：chip 两种状态与样式、
「启用远程桌面/远程桌面/关闭 RDP」按钮随状态切换、三段确认弹窗文案、指令落库、回执翻转、
票据 URL 形状、302 跳转、二次使用 410、失败回执不改状态——全部 PASS。

## M2-2 指令真机下发 ✅链路 / 👤真机落锤

自动化已验证完整链路：网页下发 enable_rdp → 指令落库（`delay_seconds=0`，不带倒计时）→
心跳取走 → 回执 `ok=true, rdp_enabled=true` → 服务端 `rdp_enabled` 置 1 → 卡片 chip 变「RDP 已启用」；
失败回执（如未提权）即使带上 `rdp_enabled` 也**不**改状态。

👤 真机步骤（Win10 企业版目标机，客户端以管理员运行）：

1. 网页点「启用远程桌面」→ 确认下发；
2. 60 秒内卡片提示「RDP 已启用…」；
3. 目标机验证：
   ```bat
   reg query "HKLM\SYSTEM\CurrentControlSet\Control\Terminal Server" /v fDenyTSConnections
   :: 期望 0x0
   netstat -an | findstr :3389
   :: 期望 0.0.0.0:3389 处于 LISTENING
   ```
4. 客户端日志（exe 同目录 logs/）应能看到四步各自的结果行。

## M2-3 一次性票据 ✅

- 单元测试：签发 60 秒后 `Consume` 返回 false；未知 ID 返回 false；取走即删。
- 接口级（真实服务）：签发返回 `/rdp/<32位十六进制>` 与 `expires_in=60`；
  首次访问 302 跳转 `<WOL_GUAC_URL>/#/?data=<载荷>` 且 `Cache-Control: no-store`；
  同一票据二次访问 410；载荷只在 URL fragment 里，不进服务端访问日志（日志中路径为 `/rdp/<ticket>` 占位符）。

👤 60 秒自然过期的人工复核：签发后等 61 秒再打开票据 URL，应看到"票据已失效"页。

## M2-4 外部浏览器连入 👤

手机浏览器（外网/蜂窝网络）→ 打开 wold（建议反代 + HTTPS）→ 登录 → 点在线目标机的「远程桌面」→
新标签页出现 Windows 登录界面 → 输入 Windows 账号口令进入桌面。
前置：按 `deploy/README.md` 第九节完成 Guacamole 配置并 `--profile rdp up -d`。

## M2-5 桌面可用性 👤

进入桌面后逐项确认：打开资源管理器、跨应用复制粘贴文本、中文输入法正常输入、
分辨率与目标机一致或随窗口更新（`resize-method=display-update`）、画面不糊（guacd `shm_size: 256m`）。

## M2-6 改网络设置不断连（核心场景）👤

在 Guacamole 会话内把目标机 IP 改为另一静态地址 → 应用 → **会话保持存活**（WOL 广播/心跳链路不受影响，
Guacamole 走的是 guacd → 目标机 3389 的既有 TCP 连接）→ 稍后按新 IP 重新连入成功。
此场景是 M3（net_revert 安全网）存在的前提，建议 M3 上线前先在真机演练一遍。

## M2-7 会话收尾 ✅设计 / 👤复核

- 设计保证：wold 不与 Guacamole 保持任何长连接，也不存连接对象——票据在内存中 60 秒过期即删，
  兑换是纯 302；重启 wold 不影响进行中的 Guacamole 会话（代码路径上无任何共享状态）。
- 👤 复核：关闭浏览器标签页后 Guacamole 会话断开；重启 wold 容器期间桌面不闪断。

## M2-8 disable_rdp ✅链路 / 👤真机落锤

自动化已验证：下发 disable_rdp → 回执 `rdp_enabled=false` → 服务端状态回 0 → chip 变「RDP 未启用」；
此时调 rdp-session 返回 409（未启用拒绝）。同设备同时只允许一条未完成指令（第二条 409）。

👤 真机步骤：下发关闭指令后，目标机 `reg query fDenyTSConnections` 应为 `0x1`，
`netstat -an | findstr :3389` 无 LISTENING；已打开的会话不受影响（按设计保留）。

## M2-9 容器回归 ✅

```text
docker build -t wol:2.0.0-beta.2 .        → 成功（含 M2 全部改动）
smoke.sh 9 项（健康/登录/鉴权/建删设备/唤醒包/在线校验/心跳链路）→ 全部 PASS
tmp/beta-e2e.py（真实容器 + 真实客户端 exe）→ 17/17 PASS
deploy/docker-compose.nas.yml config 校验 → 通过：
    不带 profile 只有 wold；--profile rdp 时为 guacd/guacamole/wold
（具体输出见当日提交信息或 CI 日志。）
```

## M2-10 安全 ✅

- 日志脱敏有测试：含 `/rdp/<ticket>` 的请求路径在访问日志中记为 `/rdp/<ticket>`；
  Guacamole 载荷只存在于 302 的 fragment 里，fragment 不会到达服务端，也就不进任何日志；
- 共享密钥只从环境变量注入，wold 启动日志不打印密钥；
- `WOL_GUAC_SECRET` 与 `WOL_GUAC_URL` 必须同时设置或同时留空，只配一半进程拒绝启动（有测试）；
- compose：guacd 无任何宿主端口；guacamole 只暴露 `${WOL_GUAC_PORT:-8088}`；
  Guacamole **只启用 JSON 认证**（`JSON_ENABLED=true`），不存在任何默认管理员口令——
  比计划原文"管理口令非默认"更强：根本没有口令登录入口；
- 票据兑换响应带 `Cache-Control: no-store`，页面带 `Referrer-Policy: no-referrer`，
  避免载荷经浏览器缓存/Referer 泄漏。

---

## 已知限制与后续

1. `heartbeat.ps1`（脚本版客户端）不支持 RDP 指令，回执会明确提示仅 GUI 客户端支持；
2. Guacamole 载荷本身无法限制打开次数（auth-json 机制如此），"一次性"由 wold 的兑换层保证：
   过了 wold 这一关，同一路浏览器链接在 Guacamole 侧的 authToken 自然失效前可刷新——
   该 token 60 秒后过期；
3. 会话内改坏网络配置的风险由 M3（net_revert 安全网）兜底，M2 阶段请先在目标机手工核对后再改。
