# 本环节任务单（E6 PWA 与移动端适配）

## 环节

E6 —— PWA 与移动端适配。

## 功能范围

仅 F7（PWA）：Web App Manifest、图标、Service Worker 静态缓存、离线提示；以及 F6 的正式宽度适配验收（360 / 390 / 768 / 1440px）。

不涉及 Docker（E7）、不涉及端到端脚本（E8）、**不新增页面、不改后端、不缓存任何接口数据**。

目标是"手机浏览器把系统当成一个入口，而不是每次输地址"，不是做离线可用的应用。这句话决定了本环节应该做得**多小**。

## 背景

E5 交付了两页 + 两弹层，功能已完整，但手机上每次都要开浏览器输地址；断网时打开页面会白屏。本环节只解决这两件事：可添加到主屏、断网有话说。

最容易越界的地方是**离线能力**：一旦追求"断网也能唤醒"，就会引出离线队列、后台同步、IndexedDB、冲突合并。这些在局域网唤醒场景里毫无意义——断网时魔术包根本发不出去。因此本环节的缓存只覆盖静态资源，接口一律走网络。

## Agent 输入

- 清单字段：`name`、`short_name`、`description`、`lang`、`start_url`、`scope`、`display: standalone`、`theme_color`、`background_color`、`icons`（192、512、maskable 512）。
- 图标：新增 192、512、maskable 512、apple-touch 180 四种 PNG，全部自带，不引用外部 CDN。
- 缓存策略：**静态资源缓存优先**（版本化缓存名），**`/api/` 一律不拦截、不进缓存**，导航请求**网络优先、失败回退缓存壳**。
- 离线行为：页面能打开（缓存壳），顶部出现"网络已断开"提示，数据区显示"无法连接服务"而不展示上次结果。
- 宽度：360 / 390 / 768 / 1440px 四档，无横向滚动、按钮可点、文字不重叠。
- 契约见 `docs/E6-contract.md`。

## 允许改动的文件

- `web/`：`manifest.json`、`sw.js`、`icons/*.png`、两个 HTML 的 `<head>`、`app.js` 追加离线提示与 SW 注册、`app.css` 追加离线条样式
- `docs/E6-contract.md`、`docs/E6-acceptance.md`、`e6/task.md`、`README.md`

**不修改** `internal/`、`cmd/`、`go.mod`、`go.sum`。本环节不产生任何 Go 代码变更。

## 禁止事项

1. **禁止新增页面**：不新增 `offline.html`、`install.html`、`about.html`。页面数仍为 2，离线状态是页内提示，不是独立页面。
2. **禁止缓存任何 `/api/` 响应**（包括运行时缓存与预缓存清单）。接口数据一旦落缓存，就会出现"断网看到的是昨天的设备状态"，这正是 6-4 要防的。
3. **禁止实现离线队列、后台同步**（`sync`、`periodicSync`、`BackgroundFetch`）、IndexedDB 任务表、离线唤醒重放。断网时包发不出去，排队的唤醒没有意义。出现即视为扩大开发，一票否决（6-5）。
4. **禁止引入构建链、Workbox、PWA 框架、CDN 图标**。`sw.js` 手写，图标本地生成。
5. **禁止在 Service Worker 里做推送**（`push`、`notificationclick`）与消息推送权限申请。项目不含推送能力。
6. **禁止修改 E5 的页面数量、令牌存放位置与状态机**。SW 只加缓存层，不改变业务行为。
7. **禁止让缓存无限增长**：缓存名带版本号，`activate` 时删除旧版本缓存；静态资源回填只存同源 `GET` 且 `response.ok` 的响应。
8. **禁止绕过 HTTPS/权限限制的"技巧"**：SW 只在安全上下文注册（localhost 与局域网 HTTPS 之外不注册），注册失败必须安静降级，不影响页面功能。
9. **禁止把 `display` 改成 `fullscreen`**，也禁止伪造启动画面截图（`screenshots` 字段）。
10. **禁止改后端口径改文案**：离线提示要说明"当前无网络、数据可能不是最新"，不得暗示"已是最新"。

## 输出要求

1. 文件：
   - `web/manifest.json`：字段与 `docs/E6-contract.md` 第一节逐字一致。
   - `web/icons/icon-192.png`、`icon-512.png`、`icon-maskable-512.png`、`apple-touch-icon.png`（180×180）。maskable 图标内容留在中间 80% 安全区内。
   - `web/sw.js`：`install` 预缓存壳资源、`activate` 清理旧版本、`fetch` 按第三节策略分流。
   - 两个 HTML 的 `<head>`：manifest、icon、apple-touch-icon、`apple-mobile-web-app-capable`、`apple-mobile-web-app-status-bar-style`。
   - `app.js`：`navigator.onLine` 变化时显示/隐藏离线条；注册 SW（失败只记控制台之外的降级，不影响页面）。
2. 缓存表：明确列出预缓存清单（壳资源）与"永不缓存清单"（`/api/` 全部）。
3. 文档：`docs/E6-contract.md`（契约）、`docs/E6-acceptance.md`（6-1~6-5 证据 + 四档宽度截图）。

## 验收方式

按开发计划 4.7 节 6-1 至 6-5 执行：

```bash
go vet ./... && go test ./...          # 139 个用例必须仍全绿（本环节不碰后端）
ls web/*.html                          # 必须仍为 2 个
grep -rn "api" web/sw.js               # 只允许出现在"跳过"分支，不得进入缓存写入
grep -rniE "indexedDB|periodicSync|backgroundSync|BackgroundFetch|pushManager|showNotification" web/
git diff --stat -- internal cmd go.mod go.sum    # 必须为空
```

**6-1 与 6-4 必须真机实测**：用 Chrome 的 `Page.getInstallabilityErrors` 确认可安装（零错误），用 CDP 断网后重新打开页面确认壳可用、提示可见、无陈旧数据。6-2 四档宽度逐一截图。

## 完成定义

6-1 至 6-5 全部通过；`web/` 下仍恰好 2 个 HTML；缓存清单中不含任何 `/api/` 路径；离线队列类 API 零命中；后端 139 个用例全绿且 `internal/`、`cmd/` 无改动。
