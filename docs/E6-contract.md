# E6 契约：PWA 清单、缓存策略与移动端适配

本契约自 E6 起生效，约束 `web/` 下的 manifest、Service Worker 与响应式样式。**不改后端，不改 E1–E5 的接口与页面数量**。

## 一、Web App Manifest（`web/manifest.json`）

| 字段 | 值 | 说明 |
| --- | --- | --- |
| `name` | `局域网唤醒` | 添加到主屏后的完整名称 |
| `short_name` | `局域网唤醒` | 主屏图标下方文字，≤ 12 字符 |
| `description` | `唤醒局域网内的设备，并用 Ping / TCP 探测确认是否上线` | 安装提示里的一句话 |
| `lang` | `zh-CN` | |
| `start_url` | `/` | 启动进入设备列表页（未登录会自行跳登录页） |
| `scope` | `/` | 全站 |
| `display` | `standalone` | **不是** `fullscreen`；保留系统状态栏 |
| `background_color` | `#f4f6f8` | 与页面底色一致，避免启动闪白 |
| `theme_color` | `#1565c0` | 与页面主色一致 |
| `icons` | 见下 | 全部本地文件 |

图标清单：

| 文件 | 尺寸 | `purpose` | 用途 |
| --- | --- | --- | --- |
| `icons/icon-192.png` | 192×192 | `any` | Android 主屏 |
| `icons/icon-512.png` | 512×512 | `any` | 启动画面 / 安装弹窗 |
| `icons/icon-maskable-512.png` | 512×512 | `maskable` | Android 自适应图标，主体在中间 80% 安全区 |
| `icons/apple-touch-icon.png` | 180×180 | —— | iOS 添加到主屏（不走 manifest，用 `<link rel="apple-touch-icon">`） |

禁止事项：不写 `screenshots`（会要求配套截图，属额外内容）、不写 `related_applications`、不引用任何外链图片。

## 二、页面 `<head>` 追加项

两个页面完全一致：

```html
<link rel="manifest" href="manifest.json">
<link rel="icon" type="image/png" sizes="192x192" href="icons/icon-192.png">
<link rel="apple-touch-icon" href="icons/apple-touch-icon.png">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-title" content="局域网唤醒">
<meta name="apple-mobile-web-app-status-bar-style" content="default">
```

`theme-color` 与 `viewport` 已在 E5 写入（`viewport-fit=cover` 保持不变，配合安全区内边距）。

## 三、Service Worker（`web/sw.js`）

### 缓存分区（唯一允许写缓存的地方）

| 类别 | 路径 | 策略 |
| --- | --- | --- |
| 壳资源（预缓存） | `./`、`index.html`、`login.html`、`app.css`、`app.js`、`login.js`、`devices.js`、`manifest.json`、四张图标 | `install` 时 `addAll` 预缓存 |
| 同源静态资源 | 上述之外的 `GET` 同源请求 | 缓存优先，未命中走网络并回填（仅 `response.ok` 且 `response.type === 'basic'`） |
| 导航请求 | `request.mode === 'navigate'` | **网络优先**，失败回退缓存中的 `index.html` |
| **接口** | 路径以 `/api/` 开头 | **不拦截**（不调用 `respondWith`），直接走浏览器网络栈 |
| 跨域请求 | 其它 origin | 不拦截、不缓存 |

三条硬规则：

1. `/api/` 永不进入 `caches.put`，也永不出现在预缓存清单里。
2. 缓存名带版本（`wol-static-v1`），`activate` 删除所有非当前版本的缓存。
3. `install` 完成后 `skipWaiting()`，`activate` 中 `clients.claim()`，避免用户长时间停在旧壳上。

### 注册

在 `app.js` 中统一注册：

```js
if ('serviceWorker' in navigator) {
  window.addEventListener('load', function () {
    navigator.serviceWorker.register('sw.js').catch(function () { /* 静默降级，页面功能不受影响 */ });
  });
}
```

- 只在安全上下文（HTTPS 或 localhost）注册；`register` 失败不提示、不阻断页面。
- 不等待 `ready`、不做更新提示条：单机自用场景，刷新即拿到新壳。

## 四、离线行为（6-4）

| 场景 | 期望 |
| --- | --- |
| 断网打开 | 由缓存壳渲染出页面，顶部常驻"网络已断开，数据可能不是最新"提示 |
| 断网时列表页 | 显示"无法连接服务，请确认服务正在运行、手机与主机在同一局域网，然后重试"；**设备区域为空，不显示上一次的卡片** |
| 恢复网络 | 提示自动消失；点"刷新"重新拉取，可正常操作 |
| 断网时点唤醒 | 请求失败 → 页内提示 + 按钮恢复可点；**不排队、不重放** |

离线提示条：

- 由 `navigator.onLine` 驱动，`online` / `offline` 事件即时切换。
- 文案只描述"无网络 / 可能不是最新"，不得暗示数据是最新的。
- 是页内元素（`position: fixed` 顶部条），**不是独立页面**。

## 五、响应式与四档宽度（6-2）

| 宽度 | 布局 |
| --- | --- |
| 360px | 单列卡片；顶栏、工具栏、按钮不换行溢出 |
| 390px | 单列卡片 |
| 768px | 两列卡片（断点 `min-width: 720px`） |
| 1440px | 两列卡片，内容区最大宽度 1080px 居中 |

硬性要求：

- `document.documentElement.scrollWidth <= window.innerWidth`（无横向滚动条）。
- 可点元素高度 ≥ 42px（按钮、输入框、下拉）。
- 卡片标题与状态标签不重叠：标题可换行，标签 `flex: none` 不压缩。
- 长串（MAC、`host:port`）用 `word-break: break-all` / 等宽字体，不撑破卡片。
- 弹层在 ≤ 520px 时贴底显示（拇指可达），> 520px 居中。

## 六、本环节明确不做

- 离线队列、后台同步（`sync` / `periodicSync` / `BackgroundFetch`）、IndexedDB。
- 消息推送（`push` / `notificationclick`）、角标、静默安装提示条。
- 独立离线页、安装引导页、关于页（页面数仍为 2）。
- 更新提示条、版本号展示、强制刷新按钮。
- 深色模式、多语言、启动画面定制（`screenshots`）。
- 服务端渲染、预渲染、CDN。
