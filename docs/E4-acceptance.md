# E4 验收记录

对应开发计划 4.5 节的验收清单 4-1 至 4-8。验收时间：2026-10-18 17:00–17:03（GMT+8）。

## 结论

| 序号 | 验收动作 | 结果 | 证据 |
| --- | --- | --- | --- |
| 4-1 | 不带令牌调用设备列表 | **通过** | 401 `unauthorized`；设备已存在时响应体只有 `error`/`message` 两键，不含名称与 MAC |
| 4-2 | 正确口令登录 | **通过** | 200，字段恰为 `token`/`expires_at`，令牌 43 字符，到期时间为 7 天后同一时刻 |
| 4-3 | 用令牌访问受保护接口 | **通过** | 建设备 201、查列表 200；唤醒 202、触发校验 202、查状态 200，与 E1–E3 的响应逐字一致 |
| 4-4 | 连续 5 次错误口令 | **通过（真机）** | 第 1–5 次 401；第 6 次用**正确口令**也返回 429 `too_many_attempts` |
| 4-5 | 令牌过期后访问 | **通过（真机 TTL=3s）** | 登录后立即 200；4 秒后 401 `token_expired`；再过 1 秒仍是 `token_expired`（未退化为 `unauthorized`） |
| 4-6 | 登出后复用旧令牌 | **通过** | 登出 204 且 0 字节响应体；复用旧令牌 401；重复登出 401（中间件先拦） |
| 4-7 | 口令不出现在源码、配置、日志、镜像层 | **通过** | 仓库命中 0、三份服务日志命中 0、二进制命中 0；日志中连 `password` 字样都是 0 |
| 4-8 | 检查用户表与注册接口（反向项） | **通过（反向）** | 建表语句只有 `device` 一张；`register/signup/forgot/reset_password` 零命中 |

判定：**E4 放行**。4-4 与 4-5 均为真机实测（真实限流、真实过期），未用单元测试结果代替。

## 4-1 证据（不带令牌）

```
$ curl -s -w "\nHTTP %{http_code}\n" $B/api/devices
{"error":"unauthorized","message":"请先登录"}
HTTP 401

$ curl -s -w "\nHTTP %{http_code}\n" -X POST $B/api/devices/1/wake
{"error":"unauthorized","message":"请先登录"}
HTTP 401
```

关键点：此时库中**已有**设备（`书房主机 / AA:BB:CC:DD:EE:FF`），401 响应体里没有任何业务字段——因为中间件在 handler 之前返回。同批还验证了 `DELETE /api/devices/1` 无令牌同样 401。

`GET /api/health` 不带令牌仍为 200：

```
$ curl -s -w "  HTTP %{http_code}\n" $B/api/health
{"status":"ok","db":"ok","time":"2026-10-18T17:00:30+08:00"}
  HTTP 200
```

## 4-2 证据（登录）

```
$ curl -X POST $B/api/login -H 'Content-Type: application/json' -d '{"password":"<20 位随机口令>"}'
{"token":"w3STI1koA2rcH3giMRcnly-MU2ucJLSZj2Fb8eA6Y7s","expires_at":"2026-10-25T17:01:12+08:00"}
HTTP 200
```

- 键集合恰为 `expires_at`、`token`（由单元测试 `TestLoginIssuesToken` 用 `assertKeys` 逐键比对）。
- 令牌 43 字符，base64url 无填充，解码后 32 字节。
- 签发时间 10-18 17:01，到期 10-25 17:01，正好 7 天（默认 `168h`）。

## 4-3 证据（带令牌）

```
$ curl -X POST $B/api/devices -H "Authorization: Bearer $TOKEN" -d '{...}'
{"id":1,"name":"书房主机","mac":"AA:BB:CC:DD:EE:FF",...,"updated_at":"2026-10-18T17:01:18+08:00"}
HTTP 201

$ curl $B/api/devices -H "Authorization: Bearer $TOKEN"
[{"id":1,...,"probe_value":"127.0.0.1:18080",...}]
HTTP 200
```

加鉴权后业务语义未变，逐条对过：

| 接口 | 结果 |
| --- | --- |
| `POST /api/devices/1/wake` | 202 `{"sent":true,"sent_at":"..."}` |
| `POST /api/devices/1/check` | 202 `{"status":"checking","started_at":"..."}` |
| `GET /api/status` | 200 `[{"device_id":1,"status":"online",...}]` |

## 4-4 证据（限流，真机）

```
第 1 次错误口令 -> HTTP 401  {"error":"invalid_credentials","message":"口令错误"}
...
第 5 次错误口令 -> HTTP 401  {"error":"invalid_credentials","message":"口令错误"}
第 6 次（正确口令）-> HTTP 429  {"error":"too_many_attempts","message":"尝试次数过多，请稍后再试"}
```

第 6 次刻意使用**正确口令**，仍被拒绝——证明锁定不是"口令校验失败"的另一种说法，而是独立的判定。

## 4-5 证据（过期，真机 TTL=3s）

```
time=2026-10-18T17:01:47 level=INFO msg=服务启动 addr=:18082 token_ttl=3s
登录 HTTP 200（34 ms），令牌 43 字符
立即访问 HTTP 200（期望 200）
4 秒后访问 HTTP 401 -> {"error":"token_expired","message":"登录已过期，请重新登录"}
再过 1 秒访问 HTTP 401 -> {"error":"token_expired","message":"登录已过期，请重新登录"}
```

第二次访问仍是 `token_expired`，这是刻意设计：若在首次校验时删除过期条目，后续会退化为 `unauthorized`，前端就分不清"过期"与"无效"。

**留痕**：第一次用 `WOL_TOKEN_TTL=1s` 实测时，"登录后立即访问"那一步因为跨了三个进程（bash → curl → python 解析）耗掉 1 秒以上，拿到了 401。判定这是测量方法问题而非实现缺陷，改用 3 秒有效期并在单个 Python 进程内完成"登录→立即访问→等待→再访问"，才得到上表结果。1 秒实例的启动日志与过期结果保留在 `tmp_server_ttl.log`（临时文件，已随验收清理）。

## 4-6 证据（登出）

```
$ curl -o /dev/null -w "HTTP %{http_code}（%{size_download} 字节）" -X POST $B/api/logout -H "Authorization: Bearer $TOKEN"
HTTP 204（0 字节）

$ curl $B/api/devices -H "Authorization: Bearer $TOKEN"
{"error":"unauthorized","message":"登录状态无效，请重新登录"}
HTTP 401
```

另外验证了两点：
- 重复登出 → 401（中间件先拦，符合"所有接口都需令牌"）。
- 登出旧令牌后，**重新登录得到的新令牌**立即可用（200），说明吊销是按令牌粒度，不是清空所有会话。

## 4-7 证据（口令不落盘）

口令为运行时随机生成的 20 位字符串，仅存在于 `tmp/wol_test.env`（`.gitignore` 已排除，验收后删除），未写入任何被跟踪文件。

```
=== 口令值在仓库文件中的命中数（排除 tmp/、.git、output/）===
0
=== 口令值在服务日志中的命中数 ===
tmp_server.log:0   tmp_server_ttl.log:0   tmp_server_ttl2.log:0
=== 口令值在二进制中的命中数 ===
0
=== 令牌值在日志中的命中数 ===
tmp_server.log: 0   tmp_server_ttl.log: 0   tmp_server_ttl2.log: 0
=== 日志中是否出现 password 字样 ===
tmp_server.log:0   tmp_server_ttl.log:0   tmp_server_ttl2.log:0
=== WOL_ADMIN_PASSWORD 在业务代码中的引用点 ===
./internal/config/config.go:67  c.AdminPassword = os.Getenv(EnvPrefix + "ADMIN_PASSWORD")
./internal/config/config.go:71  return Config{}, fmt.Errorf("%sADMIN_PASSWORD 未设置：...")
```

只有一处读取、一处报错提示，没有任何默认值。另外验证了空口令无法启动：

```
$ WOL_DATA_DIR=./tmp/data WOL_PORT=18080 ./wold.exe
ERROR 服务退出 err="WOL_ADMIN_PASSWORD 未设置：请通过环境变量注入登录口令，服务不会以空口令启动"
```

**镜像层**：E4 阶段尚无 Dockerfile（属 E7）。二进制文件中检索口令值命中 0，说明口令不参与编译；镜像层检查将在 E7 用 `docker history` / `docker run --env` 复核，已写入遗留项。

## 4-8 证据（反向检查）

```
=== 建表语句全集 ===
internal/store/schema.go:13:CREATE TABLE IF NOT EXISTS device (

=== 用户/账号相关表 ===
✅ 零命中

=== 注册/找回/改密接口 ===
✅ 零命中

=== 实际路由表 ===
GET  /api/health          （公开）
POST /api/login           （公开）
GET  /api/devices         （guard）
POST /api/devices         （guard）
PUT  /api/devices/{id}    （guard）
DELETE /api/devices/{id}  （guard）
POST /api/devices/{id}/wake  （guard）
POST /api/devices/{id}/check （guard）
GET  /api/status          （guard）
POST /api/logout          （guard）
/                         静态资源（公开）
```

`go.mod` 直接依赖仍只有 `modernc.org/sqlite`，未引入 JWT、OAuth、会话框架等任何第三方鉴权库。

## 附加验证（契约中声明、但不在 4-x 清单内）

| 项 | 结果 |
| --- | --- |
| 重启后旧令牌 | 401，重新登录后 200（会话在内存，不落库） |
| 跨实例令牌 | A 实例的令牌在 B 实例无效（401），因为各自持有独立密钥表 |
| 令牌放查询参数 | 401（只认 `Authorization` 头） |
| `Bearer` 前缀大小写 | 小写 `bearer` 可用；`Basic`、空令牌、`Bearer` 后无内容均 401 |

## 测试

`gofmt -l` 无输出，`go vet ./...` 通过，`go test -count=1 ./...` 六包全绿，共 **139 个用例**（E3 为 114，本环节新增 25：auth 包 11、接口层 12、config 2）。

## 本次明确未做

| 未做项 | 归属 / 理由 |
| --- | --- |
| 登录页面 | E5；本环节只交付接口 |
| 用户表、注册、找回密码、改密 | 排除清单；反向检查 4-8 明确禁止 |
| 刷新令牌、滑动续期 | 7 天到期重新登录即可 |
| 多会话列表、踢出在线设备 | 无界面、无需求 |
| Cookie 会话、CSRF 防护 | 不用 Cookie，令牌只在请求头 |
| HTTPS / 证书 / HSTS | 属部署与网络边界，E7 文档提示勿暴露公网 |
| 登录审计表、失败记录持久化 | 排除清单内的"操作日志"；失败计数只在内存 |
| 镜像层口令检查 | E7（尚无 Dockerfile） |

## 遗留与风险

1. **会话在内存，重启即全员登出。** 家庭场景可接受；若以后要求"重启不掉登录"，需要建会话表，那会逼近"用户体系"的边界，应作为独立决策而不是顺手加。
2. **限流按 IP，且不信任 `X-Forwarded-For`。** 若部署在反向代理后面，所有请求会共享代理的 IP，5 次失败会锁住全部人 10 分钟。E7 文档需写明"本服务设计为直连使用，不建议加反代"。
3. **令牌是明文 Bearer，依赖网络边界。** 局域网内被抓包即可重放。HTTPS 不在本期范围，部署文档需提示"勿暴露公网"。
4. **登录失败只记 IP，不记口令。** 这是 4-7 的要求，但也意味着事后无法从日志判断"是不是有人在猜口令"——只有 `too_many_attempts` 这一条痕迹。可接受，且优于记录口令。
