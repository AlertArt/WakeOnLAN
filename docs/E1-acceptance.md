# E1 验收记录

对应开发计划 4.2 节的验收清单 1-1 至 1-7。验收时间：2026-10-17 23:19–23:24（GMT+8）。

## 结论

| 序号 | 验收动作 | 结果 | 证据 |
| --- | --- | --- | --- |
| 1-1 | 运行单元测试 | **通过** | `go test ./...` 全绿；`=== RUN` 计数 52 个（含子测试），增删改查各有正例与反例 |
| 1-2 | 用示例 curl 依次执行四个接口 | **通过** | 返回结构、字段名、错误码与 `docs/E1-contract.md` 逐字一致 |
| 1-3 | 提交非法 MAC（长度错误、含非法字符） | **通过** | 三种写法均 400 `invalid_mac`，且设备数不变，未写库 |
| 1-4 | 提交重复 MAC | **通过** | 409 `duplicate_mac`；用大小写与分隔符改写仍被识别，记录数保持 1 |
| 1-5 | 删除不存在的设备 | **通过** | 404 `not_found`，随后健康检查仍 200，服务未崩溃 |
| 1-6 | 检查接口返回字段 | **通过** | 列表、POST、PUT 三种响应逐键比对，均恰好 9 个键，无多余、无缺失 |
| 1-7 | 重启服务后查询 | **通过** | `taskkill` 强杀后重启，2 条记录与其 `created_at`/`updated_at` 逐字节保留 |

判定：**E1 放行**。1-6 是本次的一票否决项，已用脚本逐键比对而非目视检查。

## 1-1 证据

工具链：Go 1.24.7 windows/amd64（仓库外 `../wol-tools-go`，不入库）。

```
$ gofmt -l .
（无输出）

$ go vet ./...
（无输出，退出码 0）

$ go test ./...
?   	wol/cmd/wold	[no test files]
ok  	wol/internal/config	(cached)
ok  	wol/internal/httpapi	1.943s
?   	wol/internal/probe	[no test files]
ok  	wol/internal/store	5.700s
?   	wol/internal/wol	[no test files]
```

用例分布：

| 包 | 文件 | 覆盖内容 |
| --- | --- | --- |
| `store` | `device_test.go` | 建表幂等、新增后读回、查询不存在、空列表非 nil、id 升序、重复 MAC、修改、修改不存在、修改撞 MAC、删除、删除不存在、超上限 |
| `httpapi` | `devices_test.go` | 新增（含默认值）、13 个非法输入子用例、重复 MAC、超上限、空列表返回 `[]`、列表字段与排序、修改（含 created_at 不变）、同 MAC 不判重、撞他人 MAC、修改不存在、非法 ID、删除 204 无响应体、删除不存在、500 不泄露细节、健康检查结构未变 |
| `httpapi` | `devices_validate_test.go` | `normalizeMAC` 16 组、`isIPv4` 11 组 |

`httpapi` 的 handler 测试注入 `fakeDeviceStore`，语义错误与真实 store 保持一致；`store` 测试跑真实 SQLite 临时库。

## 1-2 证据（契约核验）
```bash
$ curl -s -X POST $B/api/devices -d '{"name":"书房主机","mac":"aa:bb:cc:dd:ee:ff",...}'
{"id":1,"name":"书房主机","mac":"AA:BB:CC:DD:EE:FF","broadcast_addr":"192.168.1.255",
 "broadcast_port":9,"probe_type":"ping","probe_value":"192.168.1.10",
 "created_at":"2026-10-17T23:23:06+08:00","updated_at":"2026-10-17T23:23:06+08:00"}
HTTP 201

$ curl -s -X POST $B/api/devices -d '{"name":"客厅 NAS","mac":"AA-BB-CC-DD-EE-02","broadcast_addr":"192.168.1.255"}'
{"id":2,...,"mac":"AA:BB:CC:DD:EE:02","broadcast_port":9,"probe_type":"none","probe_value":"",...}
HTTP 201        # 最小字段：端口默认 9、校验类型默认 none、probe_value 为空串

$ curl -s $B/api/devices
[{"id":1,...},{"id":2,...}]
HTTP 200        # 数组直出，无 total/page 包装，id 升序

$ curl -s -X PUT $B/api/devices/1 -d '{"name":"书房主机（改名）",...,"broadcast_port":7,"probe_type":"tcp","probe_value":"192.168.1.10:22"}'
{"id":1,"name":"书房主机（改名）",...,"broadcast_port":7,"probe_type":"tcp","probe_value":"192.168.1.10:22",
 "created_at":"2026-10-17T23:23:06+08:00","updated_at":"2026-10-17T23:23:07+08:00"}
HTTP 200        # created_at 不变，updated_at 刷新

$ curl -s -X DELETE $B/api/devices/2
HTTP 204        # 无响应体
```

## 1-3 证据（非法 MAC）

```
aa:bb:cc:dd:ee     -> HTTP 400  {"error":"invalid_mac","message":"MAC 格式应为 AA:BB:CC:DD:EE:FF、AA-BB-CC-DD-EE-FF 或 12 位十六进制"}
aa:bb:cc:dd:ee:zz  -> HTTP 400  {"error":"invalid_mac",...}
aa:bb-cc:dd:ee:ff  -> HTTP 400  {"error":"invalid_mac",...}

非法请求后设备数：count = 1     # 未写库
```

## 1-4 证据（重复 MAC）

```bash
$ curl -s -X POST $B/api/devices -d '{"name":"重复项","mac":"aa-bb-cc-dd-ee-ff","broadcast_addr":"192.168.1.255"}'
{"error":"duplicate_mac","message":"该 MAC 已被其他设备使用"}
HTTP 409

$ curl -s $B/api/devices | jq length
1                               # 初始记录已写成 AA:BB:CC:DD:EE:FF，换成小写+短横线仍识别为同一网卡
```

## 1-5 证据（删除不存在）

```bash
$ curl -s -X DELETE $B/api/devices/999
{"error":"not_found","message":"设备不存在"}
HTTP 404

$ curl -s $B/api/health
{"status":"ok","db":"ok","time":"2026-10-17T23:23:17+08:00"}
HTTP 200                        # 服务存活
```

边界补充：

```
DELETE /api/devices/abc   -> 400 {"error":"invalid_id"}
POST   含 "deleted_at" 字段 -> 400 {"error":"invalid_json"}      # 未知字段被拒
PUT    broadcast_port=70000 -> 400 {"error":"invalid_broadcast_port"}
```

服务日志（两段运行）中 `level=ERROR` 计数为 0，无 panic。

## 1-6 证据（字段白名单）

脚本对列表每一项、POST 响应、PUT 响应分别取键集合，与契约的 9 个键做双向比对（多余 + 缺失）：

```
列表返回 1 条
  id=1 keys=9 多余=无 缺失=无
POST 201 keys=9 多余=无 缺失=无
PUT 200 keys=9 多余=无 缺失=无

1-6 判定： 通过（响应字段与契约白名单完全一致）
```

对照确认：响应中未出现 `deleted_at`、`total`、`count`、`page` 等任何契约外字段；错误响应固定为 `{"error","message"}` 两键。

## 1-7 证据（重启不丢数据）

```
重启前：
[{"id":1,"name":"白名单核验","mac":"AA:BB:CC:DD:EE:FF",...,"created_at":"2026-10-17T23:23:06+08:00","updated_at":"2026-10-17T23:23:25+08:00"},
 {"id":3,"name":"白名单核验","mac":"AA:BB:CC:DD:EE:77",...,"created_at":"2026-10-17T23:23:24+08:00","updated_at":"2026-10-17T23:23:24+08:00"}]
count = 2

$ taskkill /F /IM wold.exe
成功: 已终止进程 "wold.exe"，其 PID 为 5764。
$ curl -m 2 $B/api/health
HTTP 000                        # 端口已释放

（重新启动）
time=2026-10-17T23:23:38.589+08:00 level=INFO msg=服务启动 addr=:18080 data_dir=./tmp/data

重启后：
count = 2
  id=1 name=白名单核验 mac=AA:BB:CC:DD:EE:FF created=2026-10-17T23:23:06+08:00 updated=2026-10-17T23:23:25+08:00
  id=3 name=白名单核验 mac=AA:BB:CC:DD:EE:77 created=2026-10-17T23:23:24+08:00 updated=2026-10-17T23:23:24+08:00
HTTP 200

数据文件：tmp/data/wol.db  16384 字节
```

重启时 `Migrate()` 再次执行未报错，`CREATE TABLE IF NOT EXISTS` 幂等性成立（`store.TestMigrateIsIdempotent` 亦覆盖）。

## 过程中发现并修正的问题（留痕）

| 问题 | 处理 |
| --- | --- |
| `CreateDevice`/`UpdateDevice` 返回的是内存中未截断的纳秒时间戳，而落库的是秒级字符串，导致同一对象返回值与后续读回值不相等 | 两个方法改为写入后回读一次，保证返回值与落盘值逐字段一致。此问题由 `TestCreateAndGetDevice` 直接暴露 |
| 后端以子进程方式起在后台时随 shell 退出被回收，导致契约核验首轮全部 502 | 改为前后台分离的一体化启动方式执行验收，验收期间的 502 不计入结果，重跑后全部通过 |
| `devices_validate_test.go` 存在列对齐问题 | `gofmt -w` 修正，`gofmt -l .` 现已零输出 |

## 本次明确未做

| 未做项 | 归属环节 |
| --- | --- |
| `POST /api/devices/{id}/wake` 唤醒接口、魔术包构造与发送 | E2 |
| `GET /api/devices/{id}/status` 状态接口、探测与轮询 | E3 |
| 登录、令牌、`Authorization` 校验、失败限流 | E4 |
| 设备列表页与表单（前端只改了接口，页面仍是 E0 占位页） | E5 |
| 分页、排序、搜索参数 | 二期（已在一期排除清单内） |
| 软删除、回收站、操作审计 | 不在范围内 |
| 批量导入导出、示例数据 | 不在范围内 |

## 遗留事项

- 设备上限 100 通过 `store.MaxDevices` 常量硬编码，未做环境变量配置。若后续需要调整，应在 E7 与其它配置项一并纳入 `WOL_` 前缀，不在本环节开口子。
- 唯一约束依赖 `modernc.org/sqlite` 的错误文本 `UNIQUE constraint failed` 判断，未引入驱动错误码常量。如后续更换驱动需同步修改 `isUniqueViolation`。
- `git init` 已执行，但尚未建立首个 commit；建议在 E2 开始前把 E0+E1 一并提交为基线。
