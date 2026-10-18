# 本环节任务单（E4 认证与接口保护）

## 环节

E4 —— 认证与接口保护。

## 功能范围

仅 F5（单用户登录）：口令登录、令牌签发与校验、登出、失败限流。

不涉及前端登录页面（属 E5）、不涉及多账号与权限模型、不涉及 HTTPS 与网络层加固。

目标是"局域网内其他人不能随手点开我的设备列表并唤醒"，不是企业级身份系统。这句话决定了本环节应该做得**多小**。

## 背景

E1–E3 的所有接口目前完全开放：任何人访问 `/api/devices` 就能看到全部设备，`POST /api/devices/1/wake` 就能给任意设备开机。在家庭局域网里，这至少意味着同一 Wi-Fi 下的访客、被入侵的手机、智能家居设备都能操作。

本环节加一层最低成本的保护。**关键风险不是"做得不够"，而是"做多了"**：一旦引入用户表，就会连带出注册、改密、找回、角色、迁移，一期范围立刻失控。

## Agent 输入

- 单口令模式。口令由环境变量 `WOL_ADMIN_PASSWORD` 注入，**不写入代码、配置文件、数据库、日志、镜像层**。
- 登录成功后签发令牌，默认有效期 **7 天**（`WOL_TOKEN_TTL`，Go duration 写法，如 `168h`）。
- 除登录接口与健康检查外，**所有 `/api/` 接口**均需令牌。
- 连续失败 **5 次**锁定 **10 分钟**，锁定期间连正确口令也拒绝。
- 令牌只接受 `Authorization: Bearer <token>`，不接受 Cookie、查询参数。
- 契约见 `docs/E4-contract.md`（本环节先冻结，E5 前端按此对接）。

## 允许改动的文件

- `internal/auth/`（新增包：`auth.go`、`doc.go` 与测试）
- `internal/httpapi/`（新增 `auth.go` 与测试；`server.go` 追加依赖与路由包装；测试辅助函数追加令牌注入）
- `internal/config/`（`config.go` 追加 `TokenTTL`，`config_test.go` 同步用例）
- `cmd/wold/main.go`（仅构造 Authenticator 并注入）
- `docs/E4-contract.md`、`docs/E4-acceptance.md`、`e4/task.md`、`README.md`

## 禁止事项

1. **禁止建立用户表、账号体系，禁止实现注册、找回密码、修改密码、角色权限接口**。口令只有一个，存在环境变量里。出现任何一张用户/账号表即视为扩大开发，一票否决（对应反向检查 4-8）。
2. **禁止引入 JWT 库、会话框架、OAuth/OIDC、第三方鉴权组件**。令牌用 `crypto/rand` 生成、服务端内存表校验，仅用标准库。
3. **禁止实现刷新令牌、滑动续期、多设备会话列表、在线设备踢出**。令牌到期即重新登录，只有"签发/校验/吊销"三个动作。
4. **禁止接受 Cookie 或 URL 查询参数中的令牌**。只认 `Authorization: Bearer`，避免令牌进入访问日志与 Referer。
5. **禁止信任 `X-Forwarded-For` / `X-Real-IP`** 来识别来源。限流按连接的对端地址计，否则加个头就能绕过锁定。
6. **禁止修改 E1–E3 已冻结的接口路径与响应结构**。鉴权是外面包一层，业务响应体一个字段都不许变。
7. **禁止把口令或完整令牌写入日志、错误响应、数据库**。登录失败只记录来源 IP 与结果。
8. **禁止给 `/api/health` 加鉴权**。容器 `HEALTHCHECK` 用它探活，加了鉴权会导致容器永远不健康（E7 验收 7-1 直接失败）。
9. **禁止在本环节写登录页面**。前端属 E5；本环节交付的是接口与中间件。

## 输出要求

1. 代码：
   - `internal/auth/auth.go`：`New(password string, ttl time.Duration) (*Authenticator, error)`、`Login(ip, password string) (Token, error)`、`Validate(token string) error`、`Revoke(token string)`。错误值 `ErrBadPassword`、`ErrLocked`、`ErrNoSession`、`ErrExpired`。
   - `internal/httpapi/auth.go`：`POST /api/login`、`POST /api/logout`、`requireAuth` 中间件。
   - `internal/config/config.go`：`TokenTTL time.Duration`，环境变量 `WOL_TOKEN_TTL`，非法值拒绝启动；`WOL_ADMIN_PASSWORD` 为空时拒绝启动。
2. 测试：
   - 口令正确/错误/空；令牌校验通过、过期、吊销后失效。
   - 连续 5 次失败后第 6 次返回锁定；锁定期间正确口令也被拒；成功登录后计数清零。
   - 接口：无令牌 401、`Bearer` 格式错误 401、过期 401 且 error 为 `token_expired`、登出后 401、登录响应字段白名单。
   - 确认 `GET /api/health` 不带令牌仍 200（防止把探活锁死）。
3. 证据：`go test ./...` 输出；真机登录、带令牌、过期、登出、限流五组 curl 输出；仓库与日志的口令检索结果。
4. 未做清单：见 `docs/E4-acceptance.md`「本次明确未做」。

## 验收方式

按开发计划 4.5 节 4-1 至 4-8 执行，结果记录在 `docs/E4-acceptance.md`：

```bash
go vet ./...
go test ./...

# 4-7 口令不得出现在源码、文档示例、日志、二进制中
grep -rn "WOL_ADMIN_PASSWORD" . --include=*.go --include=*.md | grep -v "_test.go"
grep -rn "password" cmd internal --include=*.go | grep -v "_test.go"

# 4-8 反向检查：不得存在用户表与注册接口
grep -rniE "CREATE TABLE.*(user|account)" internal/store/
grep -rniE "register|signup|reset[_-]?password|forgot" internal/httpapi/
```

**4-5 与 4-4 必须真机实测**：用 `WOL_TOKEN_TTL=1s` 启动一个实例，登录后等 2 秒再调用，确认返回 `token_expired`；再连续 5 次错误口令，确认第 6 次为 429。这两项不接受单元测试结果代替。

## 完成定义

4-1 至 4-7 全部通过，4-8 反向检查零命中。且：**不带令牌拿不到任何业务数据；带令牌行为与 E3 完全一致**——加鉴权没有改变任何业务语义。
