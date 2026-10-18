# E3 验收记录

对应开发计划 4.4 节的验收清单 3-1 至 3-7。验收时间：2026-10-18 16:28–16:33（GMT+8）。

## 结论

| 序号 | 验收动作 | 结果 | 证据 |
| --- | --- | --- | --- |
| 3-1 | 目标设备在线时触发校验 | **通过** | 首个轮询周期内即为 `online`，`checked_at` 与触发时刻同一秒；ping 与 tcp 两条路径均验证 |
| 3-2 | 目标设备离线时触发校验 | **通过（真机 120 秒）** | 16:29:19 开始，16:31:19 转 `timeout`，实测 120 秒；期间每 15 秒采样均为 `checking` |
| 3-3 | 同一设备连续点击两次校验 | **通过** | 第二次返回 409 `check_in_progress`；检查期间无任务叠加 |
| 3-4 | 校验期间重启服务 | **通过（强杀进程）** | 重启日志 `已重置上次中断的校验状态 count=1`，状态转 `pending` 并说明原因，随后可再次触发 |
| 3-5 | 探测命令不存在 | **通过（空 PATH 实测）** | 1 秒内转 `failed`，说明为「探测命令 ping 不存在，无法校验」；健康检查仍 200，无 panic |
| 3-6 | 检查是否引入任务队列或调度组件 | **通过（反向项）** | 关键词零命中；校验任务仅一行 `go r.loop`；建表语句只有 `device` 一张 |
| 3-7 | 检查轮询时长与间隔是否可配置 | **通过（反向项）** | 新增代码零 `os.Getenv`；`MaxWait = 120s`、`Interval = 5s` 为常量；环境变量仍为 E0 的 4 个 |

判定：**E3 放行**。3-2 与 3-4 均为真机实测（真实等满 120 秒、强杀进程），未用单元测试结果代替。

## 3-1 证据（在线目标）

设备 1：`probe_type=tcp`，`probe_value=127.0.0.1:18080`（服务自身监听端口，必然在线）。

```
$ curl -X POST $B/api/devices/1/check
{"status":"checking","started_at":"2026-10-18T16:28:42+08:00"}
HTTP 202

$ curl $B/api/status
[{"device_id":1,"status":"online","message":"","checked_at":"2026-10-18T16:28:42+08:00"},
 {"device_id":2,"status":"pending","message":"","checked_at":""},
 {"device_id":3,"status":"pending","message":"","checked_at":""}]
```

同一秒内完成，属于「首个轮询周期内返回已上线」。

ping 路径同样验证（设备 3：`ping 127.0.0.1`）：

```
$ curl -X POST $B/api/devices/3/check
{"status":"checking","started_at":"2026-10-18T16:28:49+08:00"}   HTTP 202

1 秒后： {'device_id': 3, 'status': 'online', 'message': '', 'checked_at': '2026-10-18T16:28:49+08:00'}
```

## 3-2 证据（离线目标，真实 120 秒）

设备 2：`probe_type=tcp`，`probe_value=127.0.0.1:65000`（无监听）。

每 15 秒采样一次，`16:29:19` 为触发时刻：

```
16:29:27  status=checking
16:29:43  status=checking
16:29:59  status=checking
16:30:16  status=checking
16:30:33  status=checking
16:30:49  status=checking
16:31:06  status=checking
16:31:23  status=timeout
16:31:39  status=timeout

最终： {'device_id': 2, 'status': 'timeout',
        'message': '120 秒内未探测到设备上线',
        'checked_at': '2026-10-18T16:31:19+08:00'}
```

`started_at 16:29:19` → `checked_at 16:31:19`，正好 120 秒。全程未出现「永久停留在校验中」。

## 3-3 证据（重复触发）

```
$ curl -X POST $B/api/devices/2/check      # 第一次
{"status":"checking","started_at":"2026-10-18T16:28:57+08:00"}   HTTP 202

$ curl -X POST $B/api/devices/2/check      # 立即第二次
{"error":"check_in_progress","message":"该设备正在校验中，请等待本次结果"}   HTTP 409

$ curl $B/api/status（3 秒后）
[{'device_id': 2, 'status': 'checking', 'message': '', 'checked_at': ''}]
```

第二次没有启动新任务：若发生叠加，两次轮询各自独立计时，状态会提前跳变或出现重复写终态；实测只在 120 秒整点转了唯一一次 `timeout`。

## 3-4 证据（校验中重启）

```
强杀前： [{'device_id': 2, 'status': 'checking', 'message': '', 'checked_at': ''}]
$ taskkill /F /IM wold.exe
成功: 已终止进程 "wold.exe"，其 PID 为 14456。
```

重启后：

```
$ curl $B/api/status
[{"device_id":1,"status":"online",...},
 {"device_id":2,"status":"pending","message":"服务重启，上次校验中断","checked_at":""},
 {"device_id":3,"status":"online",...}]

$ cat tmp_server2.log
level=INFO msg=已重置上次中断的校验状态 count=1
level=INFO msg=服务启动 addr=:18080 data_dir=./tmp/data

$ curl -X POST $B/api/devices/2/check
{"status":"checking","started_at":"2026-10-18T16:29:19+08:00"}   HTTP 202
```

三件事同时成立才算通过：状态不再是 `checking`、说明了被打断的原因、能被重新触发。

## 3-5 证据（探测命令不存在）

以空 PATH 启动，模拟容器内没有 ping：

```bash
PATH= WOL_DATA_DIR=./tmp/data WOL_PORT=18080 ./wold.exe
```

```
健康检查： {"status":"ok","db":"ok",...}   HTTP 200
触发：     {"status":"checking","started_at":"2026-10-18T16:32:17+08:00"}   HTTP 202
1 秒后：   {'device_id': 3, 'status': 'failed',
            'message': '探测命令 ping 不存在，无法校验',
            'checked_at': '2026-10-18T16:32:17+08:00'}
再次健康： {"status":"ok",...}   HTTP 200

日志： level=WARN msg=校验无法进行，探测命令缺失 id=3 err="探测命令不存在: ping"
```

工具缺失被当作**终态**处理：立即结束，不空转 120 秒。这是设计选择——命令不存在时轮询一万次也不会成功。

## 3-6 证据（反向检查：不得引入队列与调度组件）

```
$ grep -rniE "\b(queue|broker|scheduler|cron|redis|worker[ _-]?pool|asynq|machinery)\b" internal/ cmd/ --include=*.go | grep -v _test.go
✅ 零命中

$ grep -rn "go r.loop\|go func" internal/probe/*.go | grep -v _test.go
internal/probe/runner.go:83:	go r.loop(deviceID, probeType, probeValue)

$ grep -rn "CREATE TABLE" internal/store/*.go | grep -v _test.go
internal/store/schema.go:13:CREATE TABLE IF NOT EXISTS device (
```

只有一行 goroutine 启动，只有一张表，没有任务表、没有状态历史表。

## 3-7 证据（反向检查：不得新增配置项）

```
$ grep -rn "LookupEnv\|os.Getenv" internal/probe/ internal/store/status.go internal/httpapi/check.go
✅ 零命中

$ grep -n "MaxWait\|Interval" internal/probe/runner.go
17:	MaxWait = 120 * time.Second
19:	Interval = 5 * time.Second

$ grep -n "EnvPrefix" internal/config/config.go
13:const EnvPrefix = "WOL_"      # 仍是 PORT / DATA_DIR / LOG_LEVEL / ADMIN_PASSWORD 四个
```

120 秒与 5 秒是常量，用户改不了，也就不会出现「前端多一个高级设置」。

## 字段白名单（运行时核验）

```
POST /api/devices/1/check -> 202  keys=['started_at', 'status']
status item device=1 keys=['checked_at', 'device_id', 'message', 'status']
status item device=2 keys=['checked_at', 'device_id', 'message', 'status']
status item device=3 keys=['checked_at', 'device_id', 'message', 'status']
device id=1 keys=9 多余=无 缺失=无
device id=2 keys=9 多余=无 缺失=无
device id=3 keys=9 多余=无 缺失=无
判定： 通过
```

设备接口仍是 E1 的 9 个键——状态是独立资源，没有塞进设备对象。

## 单元测试

```
$ gofmt -l .          # 无输出
$ go vet ./...        # 通过
$ go test ./...       # 5 个包全绿，114 个 === RUN（E2 为 81，本环节新增 33）
```

本环节新增用例（节选）：

```
--- PASS: TestProbeTCPOnline
--- PASS: TestProbeTCPUnreachable
--- PASS: TestProbePingLoopback
--- PASS: TestProbePingToolMissing
--- PASS: TestValidateTargetRejects            (12 种非法目标)
--- PASS: TestRunnerMarksOnlineOnFirstAttempt
--- PASS: TestRunnerMarksTimeoutAfterMaxWait
--- PASS: TestRunnerRejectsDuplicateStart
--- PASS: TestRunnerFailsImmediatelyWhenToolMissing
--- PASS: TestRunnerFailsOnInvalidTarget
--- PASS: TestRunnerAllowsRestartAfterTerminal
--- PASS: TestRunnerStartReportsWriteFailure
--- PASS: TestRunnerSilentOnShutdown
--- PASS: TestNewDeviceDefaultsToPending
--- PASS: TestMarkCheckingKeepsPreviousCheckedAt
--- PASS: TestMarkInterruptedResetsChecking
--- PASS: TestMigrateAddsStatusColumnsToOldDB   (E1 旧库补列)
--- PASS: TestStartCheckAccepted / Conflict / NotFound / BadID / StatusWriteFailure
--- PASS: TestListStatusContract / EmptyIsArray / StoreFailure
```

## 三个实现层面的判断

**一、`checking` 由 handler 同步写入，先写再起 goroutine。** 否则调用方拿到 202 立刻查询，可能读到上一轮的旧终态（比如上一次的 `timeout`），前端会误以为这次已经失败。顺序反了就会产生一个几毫秒的窗口，这个窗口恰好是前端最可能查询的时刻。

**二、工具缺失与目标非法是终态，不是「本轮未响应」。** 三种错误用 `errors.Is` 区分：`ErrUnreachable` 继续轮询，`ErrToolMissing` / `ErrInvalidTarget` 立即 `failed`。混在一起会让「容器里没 ping」变成等 120 秒再报一个含糊的超时。

**三、进程退出时不写终态。** 服务关闭时若把 `timeout` 写进库，用户会把「我关了服务」误读成「设备没起来」。改为静默退出，由下次启动的 `MarkInterrupted` 统一标记为「服务重启，上次校验中断」——原因准确，且不会永久卡死。

## 本次明确未做

| 未做项 | 归属 |
| --- | --- |
| 前端页面的状态展示与轮询 | E5（F4） |
| 校验失败后的自动重发魔术包 | 不在范围内；用户可自己再点唤醒 |
| 校验历史、耗时、成功率 | 排除清单内 |
| 批量触发全部设备校验 | 一期排除清单内 |
| SSE / WebSocket 推送 | E3 契约中已说明取舍 |
| 校验参数的界面配置 | 3-7 明确不新增配置项 |
| 鉴权 | E4 |

## 遗留与风险

1. **容器必须带 ping。** 3-5 证明没有 ping 时 ping 类校验会直接失败。E7 的镜像需要包含 `iputils-ping`（或 busybox 的 ping），并在 `docs/E7-*` 中写明。tcp 校验不依赖外部命令。
2. **E1 与 E3 的 tcp 目标校验宽严不一。** E1 只用 `net.SplitHostPort` 判断格式，`host:99999` 能存进去；E3 触发校验时会以 400 `invalid_probe` 拦下。不影响正确性（用户看到的是可操作的提示），但属于两处规则不一致，若后续要统一，应在 E1 复用 `probe.ValidateTarget`——本环节按纪律未改动 E1。
3. **ping 依赖系统命令，输出格式随系统与语言变化。** 因此只取退出码判在线，`message` 用固定文案，不透传 ping 的原始输出（契约已写明）。
