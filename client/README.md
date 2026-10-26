# Windows 心跳客户端

服务端不用 ping 判断在线（Windows 默认丢弃入站 ICMP）。这台机器开机后由本脚本定时上报心跳，服务端据此显示"已上线"。

脚本只发起**出站** HTTP 请求，不需要在目标机防火墙上开任何入站端口。

## 一、先在服务端配好设备

1. 浏览器打开服务端 → 添加/编辑设备；
2. 校验方式选 **心跳上报**；
3. 心跳密钥填 16–64 位字符（字母、数字、`_`、`-`），例如 `hb-7f3c9a2d5e8b1f4c`；
4. 记下这台机器的 MAC——客户端会自动读取并上报，必须与设备记录一致。

## 二、在目标机上验证一次

把 `heartbeat.ps1` 放到 `C:\wol\heartbeat.ps1`，然后：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File C:\wol\heartbeat.ps1 `
  -Server http://192.168.1.10:8080 `
  -Secret hb-7f3c9a2d5e8b1f4c `
  -Once
```

看到 `心跳成功：status=online` 且退出码为 0，说明服务端已经认这台机器。此时网页上该设备应显示 **已上线**。

> 系统默认禁止运行脚本（`about_Execution_Policies`），所以命令里带 `-ExecutionPolicy Bypass`——只对这一次进程生效，不改系统设置。

## 三、设成开机常驻（任务计划程序）

以**管理员**身份打开命令提示符，执行（整行复制，不要换行）：

```cmd
schtasks /Create /TN "WOL-Heartbeat" /SC ONSTART /RU SYSTEM /RL HIGHEST /F /TR "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File C:\wol\heartbeat.ps1 -Server http://192.168.1.10:8080 -Secret hb-7f3c9a2d5e8b1f4c"
```

- `ONSTART` + `SYSTEM`：开机即跑，**不需要有人登录**，适合无人值守的台式机。
- 想改成"用户登录后再跑"：把 `/SC ONSTART /RU SYSTEM` 换成 `/SC ONLOGON /RU 你的用户名`。
- 服务端地址带端口；如果走默认端口 8080，`http://192.168.1.10:8080` 就是完整地址。

检查与维护：

```cmd
schtasks /Query /TN "WOL-Heartbeat" /V /FO LIST   :: 看状态与上次运行结果
schtasks /Run   /TN "WOL-Heartbeat"               :: 立即启动一次
schtasks /End   /TN "WOL-Heartbeat"               :: 停止
schtasks /Delete /TN "WOL-Heartbeat" /F           :: 卸载
```

## 四、参数

| 参数 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- |
| `-Server` | 是 | — | 服务端地址，可省略 `http://`；不带端口时按 80 处理，建议写全 `http://IP:8080` |
| `-Secret` | 是 | — | 设备的心跳密钥，必须与网页里一致 |
| `-Mac` | 否 | 自动识别 | 多网卡或自动识别不对时手动指定，如 `-Mac 10:7B:44:80:25:B7` |
| `-IntervalSeconds` | 否 | 60 | 上报间隔；服务端 180 秒收不到心跳判离线 |
| `-LogPath` | 否 | `%ProgramData%\wol-heartbeat\heartbeat.log` | 日志文件，超 1 MB 自动轮转为 `.log.1` |
| `-Once` | 否 | 关 | 只上报一次就退出，用于验证配置 |

## 五、排障

| 现象 | 原因与处理 |
| --- | --- |
| 日志 `心跳被拒绝（401）` | MAC 或密钥不匹配。核对网页里设备的 MAC（区分大小写无所谓，格式要一致）与密钥；改过密钥要同步改脚本参数 |
| 日志 `心跳地址不存在（404）` | 服务端版本不支持心跳，或 `-Server` 指错了地址/端口 |
| 日志 `心跳失败：无法连接…` | 目标机访问不到服务端：查服务端是否在跑、端口是否被防火墙拦（是**服务端**的入站 8080，不是目标机） |
| 网页一直 `等待心跳` | 客户端没在跑；或客户端上报的 MAC 与设备记录不同（多网卡机器常见，用 `-Mac` 指定） |
| 网页 `心跳超时`，但机器确实开着 | 看日志最后一条时间：超过 3 分钟没有成功上报，通常是网络断了或计划任务被停 |

## 六、安全提示

- 心跳密钥等同于这台设备的身份证：拿到密钥 + MAC 就能让它显示为在线。不要把它写进公开仓库或截图外发。
- 怀疑泄露时：在网页里改设备的心跳密钥，再更新这台机器上的任务参数（`schtasks /Change` 或删了重建）。
- 脚本不需要管理员权限运行（注册任务那一步需要），日常上报用普通用户即可。

## 七、不用 PowerShell 可以吗

可以，协议只有一个 HTTP 请求：

```bash
curl -s -X POST http://192.168.1.10:8080/api/heartbeat \
  -H 'Content-Type: application/json' \
  -d '{"mac":"10:7B:44:80:25:B7","secret":"hb-7f3c9a2d5e8b1f4c"}'
```

任何能发 HTTP 的语言/设备（路由器脚本、NAS 定时任务、树莓派、Python）都能当客户端，只要把它排进定时任务即可。
