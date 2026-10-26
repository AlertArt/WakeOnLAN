# Windows 心跳客户端

服务端不用 ping 判断在线（Windows 默认丢弃入站 ICMP）。这台机器开机后由客户端定时上报心跳，服务端据此显示"已上线"。

客户端只发起**出站** HTTP 请求，不需要在目标机防火墙上开任何入站端口。

两个客户端任选其一：

| 客户端 | 适合场景 | 形态 |
| --- | --- | --- |
| `WolHeartbeat.exe`（Avalonia 桌面程序，**推荐**） | 有人用的电脑：填一次配置，点一下就常驻 | 单文件 exe，约 99 MB（自带运行时，免安装 .NET） |
| `heartbeat.ps1`（PowerShell 脚本） | 无人值守 / 服务器 / 不想装 GUI | 单脚本，配任务计划程序开机自启 |

## 一、先在服务端配好设备

1. 浏览器打开服务端 → 添加/编辑设备；
2. 校验方式选 **心跳上报**；
3. 心跳密钥填 16–64 位字符（字母、数字、`_`、`-`），例如 `hb-7f3c9a2d5e8b1f4c`；
4. 记下这台机器的 MAC——客户端会自动读取并上报，必须与设备记录一致。

## 二、Avalonia 客户端

### 部署

把 `WolHeartbeat.exe` 拷到目标机任意目录（建议 `C:\wol\`，**别放 Program Files**——那里默认不可写，日志会退到用户目录），双击运行：

1. **服务器地址**：填 NAS 上服务的地址，如 `http://192.168.1.10:8080`；
2. **心跳密钥**：粘贴网页里该设备的密钥；
3. **本机 MAC**：点「自动识别」读取物理网卡 MAC（多网卡机器也可手动改）；
4. **上报间隔**：默认 60 秒，服务端给出的建议间隔会覆盖它；
5. 点 **「保存并开始上报」**。右上角变绿「上报正常」即配置正确；此时网页上该设备应显示 **已上线**。

配置保存在程序目录的 `config.json`（和 exe 一起拷贝即迁移），日志在程序目录的 `logs\heartbeat-日期.log`——界面底部有完整路径，「打开日志目录」一键直达。

### 常驻

- 勾选 **「登录 Windows 时自动启动」**：写入当前用户的 Run 注册表项（不需要管理员权限），登录后自动拉起并开始上报；
- 勾选 **「启动程序即开始上报」**：程序启动后立即按已保存的配置开始，不再人工点击；
- 客户端有单实例保护：重复双击只会退出新开的那个，不会双重上报。

### 命令行验证（不打开窗口）

```cmd
C:\wol\WolHeartbeat.exe --once --server http://192.168.1.10:8080 --secret hb-7f3c9a2d5e8b1f4c
```

- 退出码 `0` 上报成功、`1` 上报失败、`2` 配置有误；
- `--server` / `--secret` / `--mac` 可临时覆盖 config.json，不落地；
- 结果同时打印到控制台并写入日志文件。

## 三、PowerShell 客户端（无人值守备选）

把 `heartbeat.ps1` 放到 `C:\wol\heartbeat.ps1`，先验一次：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File C:\wol\heartbeat.ps1 `
  -Server http://192.168.1.10:8080 `
  -Secret hb-7f3c9a2d5e8b1f4c `
  -Once
```

看到 `心跳成功：status=online` 且退出码为 0，说明服务端已经认这台机器。此时网页上该设备应显示 **已上线**。

> 系统默认禁止运行脚本（`about_Execution_Policies`），所以命令里带 `-ExecutionPolicy Bypass`——只对这一次进程生效，不改系统设置。

设成开机常驻（以**管理员**身份运行，整行复制）：

```cmd
schtasks /Create /TN "WOL-Heartbeat" /SC ONSTART /RU SYSTEM /RL HIGHEST /F /TR "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File C:\wol\heartbeat.ps1 -Server http://192.168.1.10:8080 -Secret hb-7f3c9a2d5e8b1f4c"
```

- `ONSTART` + `SYSTEM`：开机即跑，**不需要有人登录**——这是 GUI 客户端覆盖不到的场景（Avalonia 程序需要用户会话）。
- 想改成"用户登录后再跑"：把 `/SC ONSTART /RU SYSTEM` 换成 `/SC ONLOGON /RU 你的用户名`。

检查与维护：

```cmd
schtasks /Query /TN "WOL-Heartbeat" /V /FO LIST   :: 看状态与上次运行结果
schtasks /Run   /TN "WOL-Heartbeat"               :: 立即启动一次
schtasks /End   /TN "WOL-Heartbeat"               :: 停止
schtasks /Delete /TN "WOL-Heartbeat" /F           :: 卸载
```

### 脚本参数

| 参数 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `-Server` | 是 | — | 服务端地址，可省略 `http://`；不带端口时按 80 处理，建议写全 `http://IP:8080` |
| `-Secret` | 是 | — | 设备的心跳密钥，必须与网页里一致 |
| `-Mac` | 否 | 自动识别 | 多网卡或自动识别不对时手动指定，如 `-Mac 10:7B:44:80:25:B7` |
| `-IntervalSeconds` | 否 | 60 | 上报间隔；服务端 180 秒收不到心跳判离线 |
| `-LogPath` | 否 | `%ProgramData%\wol-heartbeat\heartbeat.log` | 日志文件，超 1 MB 自动轮转为 `.log.1` |
| `-Once` | 否 | 关 | 只上报一次就退出，用于验证配置 |

## 四、排障

| 现象 | 原因与处理 |
| --- | --- |
| 状态「上报失败」，日志 `被拒绝（401）` | MAC 或密钥不匹配。核对网页里设备的 MAC 与密钥；改过密钥要同步改客户端配置 |
| 日志 `地址不对（404）` | 服务端版本不支持心跳（需 ≥ 1.1.0），或服务器地址/端口填错 |
| 日志 `无法连接服务器` / `连接超时` | 目标机访问不到服务端：查服务端是否在跑、端口是否被防火墙拦（是**服务端**的入站 8080，不是目标机） |
| 网页一直 `等待心跳` | 客户端没在跑；或上报的 MAC 与设备记录不同（多网卡机器常见，手动指定 MAC） |
| 网页 `心跳超时`，但机器确实开着 | 日志最后一条超过 3 分钟没有成功上报：网络断了、电脑睡眠，或计划任务被停 |
| exe 放在 Program Files，界面提示"程序目录不可写" | 日志已自动退到 `%LOCALAPPDATA%\WolHeartbeat\logs`；想用程序目录就把 exe 挪到普通目录（如 `C:\wol\`） |
| 双击没反应或提示"已在运行" | 已有实例在跑（单实例保护生效），别重复启动 |

## 五、安全提示

- 心跳密钥等同于这台设备的身份证：拿到密钥 + MAC 就能让它显示为在线。不要把它写进公开仓库或截图外发。
- 怀疑泄露时：在网页里改设备的心跳密钥，再同步改这台机器上的客户端配置（GUI 里改一处保存即可；脚本要改任务参数）。
- 客户端不需要管理员权限运行（PowerShell 方案注册任务那一步需要），日常上报用普通用户即可。

## 六、其它平台

协议只有一个 HTTP 请求：

```bash
curl -s -X POST http://192.168.1.10:8080/api/heartbeat \
  -H 'Content-Type: application/json' \
  -d '{"mac":"10:7B:44:80:25:B7","secret":"hb-7f3c9a2d5e8b1f4c"}'
```

任何能发 HTTP 的设备（路由器脚本、NAS 定时任务、树莓派、Python）都能当客户端，排进定时任务即可。

Avalonia 客户端源码在 `client/WolHeartbeat/`（.NET 10），想改行为（间隔策略、通知等）直接改代码重新发布：

```bash
dotnet publish client/WolHeartbeat/WolHeartbeat.csproj -c Release -r win-x64 \
  --self-contained true -p:PublishSingleFile=true -p:DebugType=None -p:DebugSymbols=false \
  -o client/WolHeartbeat/publish
```
