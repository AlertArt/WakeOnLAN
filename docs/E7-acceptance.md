# E7 验收记录

对应开发计划 4.8 节的验收清单 7-1 至 7-8。验收时间：2026-10-19 14:12–14:27（GMT+8）。

验收环境：Windows 10 19045 + Docker Desktop 4.95.0（Engine 29.8.2，containerd 2.3.6，linux/amd64 容器模式），镜像库为空（干净环境）。

## 结论：7-1 至 7-7 全部通过；7-8 链路已验证（真实开机待 E8）

| 序号 | 验收动作 | 结果 | 证据 |
| --- | --- | --- | --- |
| 7-1 | 干净环境一条命令启动、健康检查转健康 | **通过** | `docker compose up -d` 后约 4 秒 `STATUS = Up (healthy)`；`State.Health.Log` 最近一次 `ExitCode=0`，输出 `{"status":"ok","db":"ok",...}` |
| 7-2 | 重启容器后数据仍在 | **通过** | 建"E7-验收-书房主机"→ `docker compose restart` → 重新登录查询，设备仍在（`id=1` 完整字段） |
| 7-3 | 删除容器后重建，数据仍在 | **通过** | `down`（不删卷）→ `up`，设备仍在；再 `down -v` → 设备数 **0**（反证数据确实只在卷上） |
| 7-4 | 镜像体积合理、无工具链残留 | **通过** | `docker image inspect` = **149,181,097 字节（142 MB）** ≤ 200MB；镜像内 `command -v go gcc make git` 无输出 |
| 7-5 | 镜像内无 .env、密钥、源码仓库 | **通过** | `find / -xdev` 查 `.env*`/`.git`/`*.db`/`go.mod`/`*.go` 全无；`docker history --no-trunc` 与 `Config.Env` 中口令命中 **0 次**；镜像 Env 只有 `WOL_DATA_DIR`、`WOL_PORT` |
| 7-6 | arm64 环境启动 | **通过（QEMU 仿真）** | `buildx --platform linux/arm64` 构建成功，`docker run --platform linux/arm64` 启动后 `(healthy)`，容器内 `uname -m` = `aarch64`、`ping` 可用、`uid=10001` |
| 7-7 | 编排文件仅一个应用服务 | **通过** | `docker compose config --services` 输出仅 `wold`，计数 1 |
| 7-8 | 容器必须能发出广播包 | **通过（链路）；开机待 E8** | 容器内 `tcpdump` 抓到 3 个 `UDP length 102` 包，载荷 `ffffffffffff + aabbccddee11×16`；Windows 宿主 `0.0.0.0:9` 实收 **3/3 包**，源地址 `192.168.1.11`（宿主网卡） |

判定：**E7 放行**。7-8 的口径是"容器能否发出广播包"，已实测；"设备真的开机"属 E8 端到端演练。

## 一、7-1 启动与健康

```
NAME      IMAGE       COMMAND       SERVICE   CREATED          STATUS
wold      wol:1.0.0   "/app/wold"   wold      12 seconds ago   Up 11 seconds (healthy)

{"Status":"healthy","FailingStreak":0,"Log":[{"ExitCode":0,
 "Output":"{\"status\":\"ok\",\"db\":\"ok\",\"time\":\"2026-10-19T06:16:11Z\"}\n"}]}
```

健康检查由镜像自带 `curl` 打 `/api/health`，命中数据库真实状态（`db:"ok"`），不是返回固定字符串的假健康。

## 二、7-2 / 7-3 数据落点

```
== 卷内容 ==
-rw-r--r-- 1 10001 10001 16384  wol.db      ← 属主就是运行用户，命名卷属主初始化生效

7-2  restart 后：  [{"id":1,"name":"E7-验收-书房主机","mac":"AA:BB:CC:DD:EE:FF",...}]
7-3a down + up 后： [{"id":1,"name":"E7-验收-书房主机",...}]
7-3b down -v 后：   设备数 0
```

反证的意义：只做 7-3a 无法排除"数据其实写进了容器可写层、恰好在 `down` 时被保留"的可能；`down -v` 后归零，才证明数据落点唯一的容器外位置就是 `wol-data` 卷。

**过程中的一个正常现象**：`restart` 后旧令牌立即失效（`{"error":"unauthorized"}`）。会话表在内存，这是 E4 的既定行为（重启即全员登出，家庭场景可接受），不是缺陷。

## 三、7-4 / 7-5 镜像干净度

```
体积：149,181,097 字节 = 142 MB（debian-slim 116MB + ping/curl + 二进制 9.9MB）
工具链：command -v go gcc make git  →  （空）
敏感文件：find .env* .git *.db go.mod *.go  →  （空）
docker history 口令命中：0
Config.Env 口令命中：0
Config.Env 实际值：["PATH=...","WOL_DATA_DIR=/data","WOL_PORT=8080"]
```

同一次实测还确认了两个运行时前提：

- **非 root 可执行 ping**：`uid=10001`，`/usr/bin/ping` 带 `cap_net_raw=ep`，实际 `ping -c 1 223.5.5.5` 退出码 0、不可达地址输出 `100% packet loss` 且退出码非 0——E3 依赖的退出码语义成立。
- **无口令时拒绝启动**：`docker run wol:1.0.0` 不带环境变量时直接打印 `WOL_ADMIN_PASSWORD 未设置` 并退出，不会以空口令跑起来。

## 四、7-6 arm64

```
wol:1.0.0-arm64   linux/arm64   size=37,673,727
容器状态：Up 13 seconds (healthy)
容器内：uname -m → aarch64 ；ping → /usr/bin/ping ；health=200 ；uid=10001(wol)
```

说明：这是 QEMU 仿真启动，证明二进制与镜像在该架构上可执行、依赖齐全；真机性能不做结论（本项目负载极低，不构成风险）。

## 五、7-8 广播链路（本环节最关键的取证）

在 host 网络容器内以 root 抓包，同时在 **Windows 宿主**监听 `0.0.0.0:9`，触发 3 次唤醒：

```
容器内 tcpdump：
06:24:18.706812 eth0  Out IP 192.168.65.3.59483 > 192.168.1.255.9: UDP, length 102
06:24:20.692078 eth0  Out IP 192.168.65.3.51456 > 192.168.1.255.9: UDP, length 102
06:24:22.705868 eth0  Out IP 192.168.65.3.51969 > 192.168.1.255.9: UDP, length 102
0x0030:  ffff ffff ffff aabb ccdd ee11 aabb ccdd

Windows 宿主监听：
[recv] from=192.168.1.11:56589 len=102 prefix=ffffffffffff mac=aabbccddee11
[recv] from=192.168.1.11:56602 len=102 prefix=ffffffffffff mac=aabbccddee11
[recv] from=192.168.1.11:56615 len=102 prefix=ffffffffffff mac=aabbccddee11
[done] 共收到 3 个包
```

结论：

1. 包确实以**广播地址** `192.168.1.255:9` 发出，不是被错配成单播；
2. 载荷是标准魔术包：6 字节 `FF` + MAC 重复 16 次 = **102 字节**，与 `docs/E2-contract.md` 逐字一致；
3. 包越过了 Docker 的虚拟网络，**到达宿主机网卡**（源地址显示为 `192.168.1.11`，即宿主机物理网卡地址，说明经过了 NAT）；
4. 连发 3 次恰好 3 包，无重复发送。

已同时排除一个干扰项：抓包显示容器内到 `192.168.1.255` 的路由是 `via 192.168.65.1 dev eth0`——`192.168.65.0/24` 是 Docker Desktop 在 VM 内使用的网段，**与真实局域网 `192.168.1.0/24` 数值不同，不构成冲突**；但这也说明 Docker Desktop 场景下必须依赖 NAT 才能把包送进局域网。

## 六、必须写进部署文档的两条限制（实测发现）

### 1. Docker Desktop 的 `host` 网络不与 Windows 宿主共享端口

实测：容器以 `network_mode: host` 运行，容器内 `curl 127.0.0.1:8080` 返回 200，但宿主机 `127.0.0.1:8080`、`192.168.1.11:8080`、容器 IP `192.168.65.3:8080` **全部连接失败**。同一镜像改用 bridge + `-p 18080:8080`，宿主访问立刻 200。

原因：Docker Desktop 的 "host" 指的是 Linux 虚拟机内部的 host，不是 Windows host。这不是本项目配置错误，是平台语义差异。

影响：**在 Docker Desktop 上跑本 compose，浏览器访问不到服务**（广播包仍能发出，见 7-8）。因此：

- **生产部署必须是原生 Linux**（小主机 / NAS / 树莓派），host 网络与端口语义都符合预期；
- 在 Windows/macOS 上想试用界面，需要临时加 `ports` 或改 bridge 网络，属**调试手段**，不能作为交付形态（bridge 下广播出不去）。

### 2. 国内网络构建会卡在两处，需加速

- **镜像拉取**：`auth.docker.io` 在本环境返回 502（不走代理也 000），需配 registry mirror 或直接用加速源拉取后 `docker tag`；
- **apt 安装**：`deb.debian.org` 返回 502，已通过 `--build-arg APT_MIRROR=mirrors.aliyun.com` 解决（Dockerfile 内置该参数，不填则用官方源）。

另：bookworm 的 apt 源是 deb822 格式的 `/etc/apt/sources.list.d/debian.sources`，**没有** `/etc/apt/sources.list`，最初按老写法替换文件直接报 `can't read`；现已对两种布局都做替换。

## 七、构建与清理

```bash
# 本次实际使用的构建命令（国内网络）
docker build --build-arg APT_MIRROR=mirrors.aliyun.com -t wol:1.0.0 .
docker compose up -d          # 约 4 秒转 healthy

# 验收后清理（已执行）
docker compose down -v        # 容器 + 卷 + 临时安装的 tcpdump 全部消失
docker rmi wol:1.0.0-arm64
rm -f deploy/.env             # 含随机 24 位口令
```

验收期间生成的随机口令只存在于被 gitignore 的 `deploy/.env`；最终镜像、`docker history`、`Config.Env` 三处检索命中均为 0。

## 八、留给 E8 的两件事

1. **真实目标机开机**：本环节只证明了"包以正确格式发到了宿主网卡"，没有证明"目标机被唤醒"。这一步需要一台支持 WOL 的关机设备，属 E8 端到端演练。
2. **若 E8 改动 `web/` 任何文件**，必须同步提升 `sw.js` 的 `CACHE` 常量，否则老用户停在旧壳上。
