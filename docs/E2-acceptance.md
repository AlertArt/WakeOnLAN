# E2 验收记录

对应开发计划 4.3 节的验收清单 2-1 至 2-6。验收时间：2026-10-18 10:52–10:54（GMT+8）。

## 结论

| 序号 | 验收动作 | 结果 | 证据 |
| --- | --- | --- | --- |
| 2-1 | 运行包构造单元测试 | **通过** | 输出恒为 102 字节、前 6 字节全 `0xFF`、其后 16 段全等且等于目标 MAC |
| 2-2 | 三种 MAC 写法输入 | **通过** | 大写冒号 / 小写短横线 / 12 位裸十六进制，构造结果逐字节相同 |
| 2-3 | 抓包验证 | **通过** | 真实 UDP 收包（回环），报文 102 字节，hexdump 见下；端口由设备配置决定 |
| 2-4 | 向不可达地址发送 | **通过（附协议说明）** | 参数非法 10 类全部返回明确错误；不可达时服务不崩、无 panic，UDP 语义下不报错属正常 |
| 2-5 | 检查模块依赖 | **通过** | 仅依赖 `errors` `fmt` `net` `strings` `time`，无 DB、无 HTTP |
| 2-6 | 检查是否实现重试/间隔/并发 | **通过（反向项）** | 关键词零命中；连发 3 次恰好收到 3 个包，无重发 |

判定：**E2 放行**。2-6 是本次的一票否决项，已用「包计数」实测，而非只看代码。

## 2-1 / 2-2 证据

```
$ go test -count=1 ./internal/wol/ -v
--- PASS: TestBuildPacketStructure
--- PASS: TestBuildPacketAcceptsThreeMACForms
--- PASS: TestParseMACString
--- PASS: TestParseMACRejectsInvalid
--- PASS: TestBuildPacketIsPure
--- PASS: TestSendDeliversExactPacket
--- PASS: TestSendSendsExactlyOnce
--- PASS: TestSendRejectsBadArguments
--- PASS: TestSendAcceptsBroadcastAddress
--- PASS: TestServiceWakeDeliversPacket
--- PASS: TestServiceWakeRejectsBadMAC
--- PASS: TestServiceWakeRejectsBadPort
ok  	wol/internal/wol	1.584s
```

全部用例数：`go test ./... -v` 计 81 个 `=== RUN`（E1 的 52 个 + E2 新增 29 个）。

`TestBuildPacketStructure` 断言四项：长度 `== PacketLen`、长度 `== 102`（常量被改也会被这行抓住）、6 字节同步头、16 段 MAC 逐段比对。

`TestBuildPacketAcceptsThreeMACForms` 用 5 种写法（含首尾空白、大小写混排）与基准比较，要求 `bytes.Equal` 全等。

## 2-3 证据（真实报文）

HTTP 触发 → store 读配置 → wol 发包 → 回环套接字接收。用真实 UDP 套接字取代抓包工具，验证的是内核实际发出的字节。

```
== 1. 创建设备（广播地址指向回环，端口 19999）==
HTTP 201  id=1  mac=AA:BB:CC:DD:EE:FF  port=19999

== 2. 触发唤醒 ==
HTTP 202  {"sent": true, "sent_at": "2026-10-18T10:52:36+08:00"}

== 3. 接收端收到的报文 ==
来源 127.0.0.1:60818   长度 102 字节

== 4. hexdump ==
  0000  FF FF FF FF FF FF AA BB CC DD EE FF AA BB CC DD
  0010  EE FF AA BB CC DD EE FF AA BB CC DD EE FF AA BB
  0020  CC DD EE FF AA BB CC DD EE FF AA BB CC DD EE FF
  0030  AA BB CC DD EE FF AA BB CC DD EE FF AA BB CC DD
  0040  EE FF AA BB CC DD EE FF AA BB CC DD EE FF AA BB
  0050  CC DD EE FF AA BB CC DD EE FF AA BB CC DD EE FF
  0060  AA BB CC DD EE FF

== 5. 结构校验 ==
✅ 长度 102
✅ 前 6 字节全为 0xFF
✅ 16 段 MAC 全等且等于目标：AA:BB:CC:DD:EE:FF
```

端口核验：接收端绑在设备配置的 `19999` 上，能收到即证明发的就是这个端口，而非默认 9。另一组用 `19998` 与 `9` 的对照同样收到，说明端口取自设备记录。

**未做真实局域网抓包**：需要真机配合 Wireshark 且目标机需在同一广播域，属 E8 端到端验收（8-x）范围。此处以回环收包证明字节与端口正确，剩余不确定因素是链路层，非本环节代码。

## 2-4 证据

参数非法共 10 类，全部返回可 `errors.Is` 匹配的语义错误，无 panic：

```
空包 / 包过短(101) / 包过长(103)      -> ErrInvalidPacket
地址为空 / host.local / 192.168.1 / ::1 -> ErrInvalidAddr
端口 0 / -1 / 65536                     -> ErrInvalidPort
```

向不可达地址（保留网段 `192.0.2.255`）发送：

```bash
$ curl -s -X POST $B/api/devices/2/wake
{"sent":true,"sent_at":"2026-10-18T10:52:44+08:00"}
HTTP 202

$ curl -s $B/api/health
{"status":"ok","db":"ok","time":"2026-10-18T10:52:44+08:00"}
HTTP 200                    # 服务存活

$ grep -c panic tmp_server.log
0
```

**关于「返回明确错误」的说明**：UDP 是无连接的，`sendto` 成功后内核不会因目标不可达而立即报错，报文会被路由或丢弃。因此本环节能「明确报错」的只有两类——参数非法、本机套接字创建/写入失败，这与任务单约定的失败场景一致。真正的「设备没开机」判定必须靠 E3 的探测，不能靠发送返回值。这一点已写入 `Send` 的注释，避免后续环节误以为 `Wake` 返回 nil 就等于唤醒成功。

## 2-5 证据（依赖隔离）

```bash
$ go list -f '{{join .Imports "\n"}}' ./internal/wol | sort | tr '\n' ' '
errors fmt net strings time

$ go list -deps ./internal/wol | grep -E "database/sql|net/http|wol/internal/(store|httpapi)"
（无输出）
```

`wol` 包的传递依赖中，除自身外全部为标准库。可脱离本服务单独 import 使用。

## 2-6 证据（反向检查）

静态检查：

```bash
$ grep -rniE "\b(retry|retries|interval|backoff|goroutine|go func|sync\.WaitGroup|batch)\b" internal/wol/
（零命中）

$ grep -rnE "\bgo [a-zA-Z_]" internal/wol/*.go | grep -v _test.go
（零命中，业务代码中无 goroutine）

$ grep -rn "time.Sleep\|time.After" internal/wol/*.go | grep -v _test.go
（零命中，无等待）
```

动态检查——数包（若实现里偷偷重试，收包数会大于请求数）：

```
设备 id=3
  第 1 次请求 -> HTTP 202
  第 2 次请求 -> HTTP 202
  第 3 次请求 -> HTTP 202

请求次数 3，收到报文 3 个，长度 [102, 102, 102]
判定： 通过（一发一收，无重发）
```

另有单测 `TestSendSendsExactlyOnce`：发出一个包后，把读超时设为 400ms，若收到第二个报文即失败。

## 接口契约核验（`docs/E2-contract.md`）

| 场景 | 实际响应 |
| --- | --- |
| 正常唤醒 | `202` `{"sent":true,"sent_at":"2026-10-18T10:52:36+08:00"}` |
| 设备不存在 | `404` `{"error":"not_found","message":"设备不存在"}` |
| ID 非法（`abc`） | `400` `{"error":"invalid_id","message":"设备 ID 必须为正整数"}` |
| `GET` 该方法 | `404`（ServeMux 方法不匹配，未落入业务分支） |
| 发送失败 | `500` `{"error":"send_failed",...}`，不回显系统错误细节（单测 `TestWakeSendFailureIsSanitized`） |

响应字段白名单恰好 2 个键，由 `assertKeys` 逐键比对；单测 `TestWakeUsesStoredDeviceConfig` 在请求体里塞入 `mac`/`broadcast_addr`/`broadcast_port` 干扰值，断言发包仍使用库内配置（`AA:BB:CC:DD:EE:FF` / `192.168.1.255` / `7`）。

## 过程中发现并修正的问题（留痕）

| 问题 | 处理 |
| --- | --- |
| `httpapi.New` 增加 `Waker` 参数后，3 处既有测试的构造调用需要同步 | 新增 `fakeWaker` 并更新 `server_test.go`、`devices_test.go` 三处调用；E1 的断言未改动 |
| `packet_test.go` 首次 `gofmt` 未通过（注释对齐） | `gofmt -w` 修正，现 `gofmt -l .` 零输出 |
| 向不可达地址发送时 HTTP 返回 202 而非错误 | 核实为 UDP 无连接语义的必然结果，非缺陷；已在 `Send` 注释与本文档中说明，避免后续环节误解 |

## 本次明确未做

| 未做项 | 归属环节 |
| --- | --- |
| 唤醒后的在线探测、状态轮询、状态接口 | E3 |
| 设备列表上的状态展示 | E4/E5 |
| 登录与接口保护（当前唤醒接口无鉴权） | E4 |
| 真实局域网抓包与真机唤醒验证 | E8 |
| 重试、发送间隔、批量唤醒、定时唤醒 | 一期排除清单内，二期再说 |
| 多网卡绑定、指定源地址发送 | 不在范围内（`net.DialUDP` 由系统选路） |

## 遗留事项

1. **唤醒接口目前无鉴权**，局域网内任何人可触发。这是 E4 覆盖范围，E4 放行前不得对外暴露。
2. **容器网络**：桥接模式下 UDP 广播通常出不了容器，E7 必须用 host 网络并做真机验证（开发计划风险表第 1 条）。
3. `wol` 包与 `httpapi` 各有一份 MAC 解析逻辑：前者为拿到 6 字节，后者为返回带中文提示的校验错误。功能重叠但用途不同，未做合并；若后续出现第三处调用，应抽到公共位置。此条记录为二期候选，不在本环节处理。
