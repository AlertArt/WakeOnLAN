# E1 接口契约（冻结）

本文件是 E1–E5 的共同基准。**一旦放行，字段名与错误码不得再改**；若确需变更，必须先评审并同步修改本文件与前端。

## 通用约定

- 前缀：`/api/`
- 编码：请求与响应均为 `application/json; charset=utf-8`
- 成功响应：直接返回资源对象或数组，**不做 `{code, data, msg}` 包装**
- 失败响应：统一为 `{"error":"<机器可读码>","message":"<中文可读说明>"}`
- 时间格式：RFC 3339，含时区偏移，例如 `2026-10-17T23:40:12+08:00`
- 设备数量上限：100

## 错误码表

| HTTP | `error` | 触发条件 |
| --- | --- | --- |
| 400 | `invalid_json` | 请求体不是合法 JSON，或含未知字段 |
| 400 | `invalid_mac` | MAC 长度/字符/分隔符不合规 |
| 400 | `invalid_name` | 名称为空或超过 32 字符 |
| 400 | `invalid_broadcast_addr` | 广播地址不是合法 IPv4 |
| 400 | `invalid_broadcast_port` | 端口不在 1–65535 |
| 400 | `invalid_probe` | 校验目标类型非法，或类型为 ping/tcp 但缺少值 |
| 400 | `invalid_id` | 路径中的 ID 不是正整数 |
| 404 | `not_found` | 设备不存在 |
| 409 | `duplicate_mac` | MAC 已存在（唯一约束） |
| 409 | `limit_exceeded` | 设备数已达 100 |
| 405 | — | 方法不允许，由 ServeMux 直接返回，无响应体 |
| 500 | `internal` | 未预期的存储错误，响应体不含内部细节 |

## 资源模型

```json
{
  "id": 1,
  "name": "书房主机",
  "mac": "AA:BB:CC:DD:EE:FF",
  "broadcast_addr": "192.168.1.255",
  "broadcast_port": 9,
  "probe_type": "ping",
  "probe_value": "192.168.1.10",
  "created_at": "2026-10-17T23:40:12+08:00",
  "updated_at": "2026-10-17T23:40:12+08:00"
}
```

字段白名单（**响应只允许出现以下 9 个键，多一个即为不通过**）：

| 键 | 类型 | 说明 |
| --- | --- | --- |
| `id` | number | 自增主键，从 1 开始 |
| `name` | string | 1–32 字符，允许中文 |
| `mac` | string | 归一化后的大写冒号格式，如 `AA:BB:CC:DD:EE:FF` |
| `broadcast_addr` | string | 点分十进制 IPv4 |
| `broadcast_port` | number | 1–65535，默认 9 |
| `probe_type` | string | `none` / `ping` / `tcp` |
| `probe_value` | string | `none` 时为 `""`；`ping` 为 IPv4；`tcp` 为 `主机:端口` |
| `created_at` | string | RFC 3339 |
| `updated_at` | string | RFC 3339 |

## MAC 输入归一化

接受三种写法，输出一律为大写冒号格式：

| 输入 | 归一化结果 |
| --- | --- |
| `aa:bb:cc:dd:ee:ff` | `AA:BB:CC:DD:EE:FF` |
| `AA-BB-CC-DD-EE-FF` | `AA:BB:CC:DD:EE:FF` |
| `aabbccddeeff` | `AA:BB:CC:DD:EE:FF` |

拒绝：长度不为 12 个十六进制字符、含非十六进制字符、分隔符混用（如 `AA:BB-CC`）、首尾空白以外的任何修饰。

## 接口清单

### 1. 列表

```
GET /api/devices
```

请求体：无。

响应 200：

```json
[
  {"id":1,"name":"书房主机","mac":"AA:BB:CC:DD:EE:FF","broadcast_addr":"192.168.1.255","broadcast_port":9,"probe_type":"ping","probe_value":"192.168.1.10","created_at":"2026-10-17T23:40:12+08:00","updated_at":"2026-10-17T23:40:12+08:00"}
]
```

- 无设备时返回 `[]`（空数组，不是 `null`）。
- 排序：`id` 升序。
- 不返回 `total`、`page` 等包装字段。

### 2. 新增

```
POST /api/devices
```

请求体（`id`/`created_at`/`updated_at` 由服务端生成，不接受客户端传入）：

```json
{
  "name": "书房主机",
  "mac": "AA:BB:CC:DD:EE:FF",
  "broadcast_addr": "192.168.1.255",
  "broadcast_port": 9,
  "probe_type": "ping",
  "probe_value": "192.168.1.10"
}
```

可选字段缺省值：`broadcast_port` → `9`；`probe_type` → `none`；`probe_value` → `""`。

响应 201：完整资源对象（含 `id`、`created_at`、`updated_at`）。

### 3. 修改

```
PUT /api/devices/{id}
```

请求体：与新增相同，**全量覆盖**（不做 PATCH 局部更新）。

响应 200：更新后的完整资源对象，`created_at` 不变、`updated_at` 刷新。

- `id` 不存在 → 404 `not_found`
- `id` 非正整数 → 400 `invalid_id`
- MAC 与其他设备重复 → 409 `duplicate_mac`（与自身相同的 MAC 不算重复）

### 4. 删除

```
DELETE /api/devices/{id}
```

响应 204：无响应体。

- `id` 不存在 → 404 `not_found`

## 契约之外的明确不做

| 不做项 | 归属 |
| --- | --- |
| 唤醒接口 `POST /api/devices/{id}/wake` | E2 |
| 状态接口 `GET /api/devices/{id}/status` | E3 |
| 认证头 `Authorization` | E4 |
| 分页、排序、搜索参数 | 二期 |
| 软删除、回收站 | 不在范围内 |
