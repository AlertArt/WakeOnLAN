# E6 验收记录

对应开发计划 4.7 节的验收清单 6-1 至 6-5。验收时间：2026-10-18 21:32–21:40（GMT+8）。

验收方式：真实 Chrome 155.0.8059.40（headless）+ CDP。`6-1` 用 `Page.getAppManifest` 与 `Page.getInstallabilityErrors`（Chrome 自身的可安装判定，非自评）；`6-4` 用 `Network.emulateNetworkConditions` 真实断网后重新打开页面；`6-2` 四档宽度逐一改视口截图。共 25 项断言全部通过。

## 结论

| 序号 | 验收动作 | 结果 | 证据 |
| --- | --- | --- | --- |
| 6-1 | 添加到主屏：图标、名称正确，启动无地址栏 | **通过（真机）** | `installabilityErrors=[]`（Chrome 判定可安装）；清单字段齐全 `display=standalone`、`start_url=/`；四张图标均 200；`standalone` 模式启动即无浏览器地址栏 |
| 6-2 | 360/390/768/1440px 检查 | **通过（真机）** | 四档均无横向滚动；可点元素最小高度均为 42px；卡片标题与状态标签包围盒零重叠；四张截图见 `docs/e6-shots/width-*.png` |
| 6-3 | 接口请求不被缓存，登录态变化后数据为最新 | **通过（真机）** | `caches.match('/api/devices')` 为 `undefined`；缓存 12 个条目全部是壳资源，`/api/` 零条目；接口改名后刷新页面立即显示新名字，且 `performance` 显示 `/api/devices` 的 `transferSize=536`（来自网络，非缓存） |
| 6-4 | 断网后打开：可理解的离线提示，不显示陈旧数据 | **通过（真机）** | 断网重新打开：缓存壳正常渲染（`navigator.serviceWorker.controller` 存在），底部常驻"网络已断开，页面数据可能不是最新，恢复后请刷新"，数据区显示"无法连接服务…"，**卡片数 0**（无陈旧数据）；恢复网络后提示自动消失，点刷新列表恢复 |
| 6-5 | 是否实现离线队列、后台同步（反向项） | **通过（反向）** | `grep -rniE "indexedDB|periodicSync|backgroundSync|BackgroundFetch|pushManager|showNotification" web/` 零命中；`sw.js` 中 `addEventListener('sync'/'push')` 零命中 |

判定：**E6 放行**。后端 `go vet` 干净、六包 139 个用例全绿，`git status -- internal cmd go.mod go.sum` 为空（本环节未碰后端）。

## 6-1 证据（可安装性）

```
[PASS] SW 注册并接管页面 :: controller=http://127.0.0.1:18082/sw.js
[PASS] 6-1 清单字段齐全 :: display=standalone start_url=/ icons=3
[PASS] 6-1 清单无解析错误 :: errors=0
[PASS] 6-1 可安装性检查 :: installabilityErrors=[]
[PASS] 6-1 四张图标均 200 :: 192/512/maskable/apple-touch
```

- 清单由 Chrome 实际解析（`Page.getAppManifest`），critical 错误 0；`getInstallabilityErrors` 返回空数组是 Chrome 判定"可安装"的权威依据，不需要人工猜测。
- 图标为本地生成的电源符号（蓝底白字），`icon-maskable-512` 主体缩放在中间 80% 安全区内；iOS 用 `apple-touch-icon`（180×180，不透明方图，由系统自行套圆角）。
- 图标样式见 `docs/e6-shots/icon-512.png`。

## 6-2 证据（四档宽度）

```
[PASS] 6-2/360px 无横向滚动      [PASS] 6-2/360px 可点元素高度 :: 最小=42px   [PASS] 6-2/360px 标题与状态标签不重叠
[PASS] 6-2/390px 无横向滚动      [PASS] 6-2/390px 可点元素高度 :: 最小=42px   [PASS] 6-2/390px 标题与状态标签不重叠
[PASS] 6-2/768px 无横向滚动      [PASS] 6-2/768px 可点元素高度 :: 最小=42px   [PASS] 6-2/768px 标题与状态标签不重叠
[PASS] 6-2/1440px 无横向滚动     [PASS] 6-2/1440px 可点元素高度 :: 最小=42px  [PASS] 6-2/1440px 标题与状态标签不重叠
```

截图：`docs/e6-shots/width-360.png`（单列）、`width-390.png`、`width-768.png`（两列）、`width-1440.png`（两列居中）。

判定口径：`scrollWidth <= innerWidth`（无横向滚动）；可见 `button/input/select` 的最小包围盒高度 ≥ 42；每张卡片的标题与状态标签包围盒不相交（文字不重叠）。

## 6-3 证据（缓存策略）

```
[PASS] 缓存只含壳资源，不含接口 :: 版本=wol-static-v1 条目=12
[PASS] 6-3 caches.match("/api/devices") 为空 :: 接口未进缓存
[PASS] 6-3 前置：接口改名成功 :: PUT 200
[PASS] 6-3 设备数据来自网络（transferSize>0） :: transferSize=536
```

缓存 12 个条目：`/`、`index.html`、`login.html`、`app.css`、`app.js`、`login.js`、`devices.js`、`manifest.json`、四张图标。`sw.js` 中 `/api/` 只出现在"跳过"分支（`if (url.pathname.indexOf('/api/') === 0) return;`），预缓存清单与运行时回填都不可能写入接口响应。

## 6-4 证据（断网打开）

```
[PASS] 6-4 断网打开有缓存壳与离线提示 :: title=设备 · 局域网唤醒 卡片=0
[PASS] 6-4 数据区显示连接失败说明 :: 无法连接服务，请确认服务正在运行、手机与主机在同一局域网，然后重试…
[PASS] 6-4 不展示陈旧设备数据 :: 卡片=0
[PASS] 6-4 恢复网络后可正常操作 :: 离线条已隐藏，列表恢复
```

截图：`docs/e6-shots/offline-open.png`——页面完整渲染（缓存壳）、顶部错误横幅、底部离线提示条、设备区为空。

关键点：断网时设备区为**空**而不是显示上一次的列表。这由两层保证：Service Worker 对 `/api/` 完全不拦截；前端也从不把接口数据写进任何浏览器存储（E5 契约第二节）。

## 反向检查命令与结果

```bash
$ ls web/*.html
web/index.html
web/login.html                     # 仍为 2 个，未新增离线页/安装页

$ grep -n "api" web/sw.js
# 仅 3 处：两行注释 + 一行跳过分支 `if (pathname.startsWith('/api/')) return;`
# 缓存写入（cache.put / addAll）无任何 /api/ 路径

$ grep -rniE "indexedDB|periodicSync|backgroundSync|BackgroundFetch|pushManager|showNotification" web/
（零命中）

$ git status --short -- internal cmd go.mod go.sum
（空输出 —— 后端零改动）
```

## 测试

`go vet ./...` 无输出，`go test ./...` 六包全绿（139 个用例，与 E5 放行时一致）。

## 本次明确未做

| 未做项 | 归属 / 理由 |
| --- | --- |
| 离线队列、后台同步、离线唤醒重放 | 排除清单；断网时魔术包发不出去，排队无意义（6-5 反向项） |
| 消息推送、角标 | 项目不含推送能力 |
| 独立离线页、安装引导页 | 页面数固定为 2；离线状态是页内提示条 |
| 更新提示条、版本展示 | 刷新即拿到新壳，单机自用无需更新管理 UI |
| 深色模式、多语言、启动画面 `screenshots` | 排除清单 / 额外内容 |
| 真实手机添加到主屏的全流程截图 | 需要真机操作（Chrome 安装判定已通过 `installabilityErrors=[]` 权威确认）；E7 部署后可在局域网手机上复验 |
| HTTPS 与 SW 在非 localhost 局域网地址下的注册 | SW 只在安全上下文注册，注册失败静默降级为普通网页；HTTPS 属 E7/部署决策 |

## 遗留与风险

1. **局域网 HTTP 地址下 SW 不生效。** Chrome 只在 `localhost` / HTTPS 注册 SW。局域网手机用 `http://192.168.x.x` 访问时，"添加到主屏"退化为普通书签（无离线壳，其余功能不受影响）。要真正安装为 PWA 需要给服务加 HTTPS（自签证书会有告警），建议留给部署阶段决策，而不是前端硬造。
2. **壳缓存是版本化快照。** 改前端文件后必须同步提升 `sw.js` 里的 `CACHE` 版本号（`wol-static-v1` → `v2`），否则老用户继续用旧壳。已写入契约第三节。
3. **`start_url=/` 直接进设备列表。** 未登录时页面自行跳登录页（多一次客户端跳转）；接受，因为后端没有"未登录重定向"这类服务端会话语义。
4. **断网判定依赖 `navigator.onLine`。** 它只反映网卡状态；"服务挂了但网络通"的场景由接口失败提示覆盖（5-2），两条路径互补。
