# 本环节任务单（E2 唤醒核心：魔术包发送）

## 环节

E2 —— 唤醒核心：魔术包发送。

## 功能范围

仅 F2（一键唤醒）。不涉及 F3 校验、F4 状态展示。

产出三样东西：可独立测试的包构造函数、发包函数、一个供上层调用的接口。外加一个触发发包的 HTTP 接口，使 M1 里程碑的「curl 能触发发包」可达成。

## 背景

E1 已把设备配置（MAC、广播地址、广播端口）落库并可查询。本环节负责把这些配置变成线上真实发出的 102 字节 UDP 广播包。

这是整个系统的技术核心，也是最容易被过度工程化的部分：**发一次就是发一次**，不重试、不排期、不并发。

## Agent 输入

- 魔术包协议：6 字节 `0xFF` 同步头 + 目标 MAC 的 6 字节重复 16 次 = 102 字节。
- 发送方式：UDP，目的地址为设备的广播地址，端口为设备的广播端口（默认 9）。发送后不等待任何响应。
- 失败场景仅限两类：发送参数非法（MAC 无法解析、地址不是 IPv4、端口越界）、本机网络不可用。
- 上层调用形状：`Service.Wake(mac, broadcastAddr string, port int) error`。

## 允许改动的文件

- `internal/wol/`（`doc.go` 保留常量，新增 `packet.go`、`sender.go`、`service.go` 与测试）
- `internal/httpapi/wake.go`（新增）、`internal/httpapi/server.go`（仅追加路由与依赖注入）
- `cmd/wold/main.go`（仅构造并注入 wol.Service）
- `docs/E2-acceptance.md`、`e2/task.md`

## 禁止事项

1. **禁止实现重试次数、重试间隔、发送超时配置**。发一次就是一次；失败把错误返回给调用方，由用户自己再点一次。这是本环节的一票否决项。
2. **禁止并发发送、goroutine 池、批量唤醒**。一次请求只发一个包。
3. 禁止引入第三方网络库、原始 socket、`syscall` 调用；只用 `net` 标准库。
4. 禁止在 `internal/wol` 内 import `database/sql`、`net/http`、`internal/store`、`internal/httpapi`。该包必须能脱离本服务单独调用。
5. 禁止实现定时唤醒、唤醒历史记录、发送日志表。
6. 禁止修改 E1 已冻结的设备字段与 `/api/devices` 四个接口的响应结构。
7. 禁止让发包接口等待或返回探测结果（属 F3）。发包成功仅代表「包已交给内核」。
8. 禁止新增环境变量配置项。

## 输出要求

1. 代码：
   - `internal/wol/packet.go`：`ParseMAC(string) (MAC, error)`、`BuildPacket(MAC) []byte`、`BuildPacketFromString(string) ([]byte, error)`。
   - `internal/wol/sender.go`：`Send(packet []byte, addr string, port int) error`，参数校验前置。
   - `internal/wol/service.go`：`Service` 与 `Wake(mac, broadcastAddr string, port int) error`。
   - `internal/httpapi/wake.go`：`POST /api/devices/{id}/wake`。
2. 测试：
   - 包结构：长度、同步头、16 段 MAC 全等。
   - 三种 MAC 写法（大写冒号、小写短横线、12 位裸十六进制）构造结果一致。
   - 真实 UDP 回环收包：验证实际发出的字节与端口。
   - 非法参数四类：MAC 无法解析、地址非 IPv4、端口越界、空包。
3. 证据：`go test ./...` 输出、回环收包的实际字节（hexdump）、依赖隔离核验命令与输出、反向检查 grep 结果。
4. 未做清单：见 `docs/E2-acceptance.md`「本次明确未做」。

## 验收方式

按开发计划 4.3 节 2-1 至 2-6 执行，结果记录在 `docs/E2-acceptance.md`：

```bash
go vet ./...
go test ./...

# 2-5 依赖隔离：wol 包不得依赖 DB 与 HTTP
go list -deps ./internal/wol | grep -E "database/sql|net/http|wol/internal/(store|httpapi)"

# 2-6 反向检查：不得出现重试与并发
grep -rniE "\b(retry|retries|interval|backoff|goroutine|go func|sync\.WaitGroup|batch)\b" internal/wol/
```

## 完成定义

2-1 至 2-5 全部通过；2-6 反向检查零命中。发一次包，可观测到一次 UDP 报文，不多发也不少发。
