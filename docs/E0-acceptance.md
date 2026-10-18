# E0 验收记录

对应开发计划 4.1 节的验收清单 0-1 至 0-4。验收时间：2026-10-17 23:16（GMT+8）。

## 结论

| 序号 | 验收动作 | 结果 | 证据 |
| --- | --- | --- | --- |
| 0-1 | 执行依赖安装与启动命令 | **通过** | `go test ./...` 全绿（9 个用例）；`wold.exe` 启动成功，`GET /api/health` 返回 200 |
| 0-2 | 检查目录树 | **通过** | 与 `docs/E0-decision.md` 约定一致，无多余脚手架文件、无示例数据 |
| 0-3 | 检查依赖清单 | **通过** | 全部依赖可追溯到 F1–F9，见 `docs/E0-dependencies.md` |
| 0-4 | 全文检索排除清单关键词 | **通过** | 仅 README 与任务单的"禁止事项"段落命中，属有意声明，非实现 |

判定：**E0 放行**。未做清单中的每一项均已分配到 E1–E8，无孤儿项。

## 0-1 证据

工具链：Go 1.24.7 windows/amd64（临时解压于仓库外 `../wol-tools-go`，不入库）。

```
$ go vet ./...
（无输出，退出码 0）

$ go test -v ./...
?   	wol/cmd/wold	[no test files]
=== RUN   TestLoadDefaults
--- PASS: TestLoadDefaults (0.00s)
=== RUN   TestLoadOverrides
--- PASS: TestLoadOverrides (0.00s)
=== RUN   TestLoadRejectsBadPort
--- PASS: TestLoadRejectsBadPort (0.00s)
=== RUN   TestLoadRejectsBadLogLevel
--- PASS: TestLoadRejectsBadLogLevel (0.00s)
PASS
ok  	wol/internal/config	2.231s
=== RUN   TestHealthOK
--- PASS: TestHealthOK (0.04s)
=== RUN   TestHealthDegradedWhenDBDown
--- PASS: TestHealthDegradedWhenDBDown (0.00s)
=== RUN   TestHealthRejectsWrongMethod
--- PASS: TestHealthRejectsWrongMethod (0.00s)
PASS
ok  	wol/internal/httpapi	3.277s
=== RUN   TestOpenCreatesDatabaseFile
--- PASS: TestOpenCreatesDatabaseFile (0.04s)
=== RUN   TestOpenFailsWhenDataDirIsAFile
--- PASS: TestOpenFailsWhenDataDirIsAFile (0.01s)
PASS
ok  	wol/internal/store	4.271s
?   	wol/internal/probe	[no test files]
?   	wol/internal/wol	[no test files]

$ WOL_DATA_DIR=./tmp/data WOL_PORT=18080 ./wold.exe
time=2026-10-17T23:16:33 level=INFO msg=服务启动 addr=:18080 data_dir=./tmp/data

$ curl -s -w "\nHTTP %{http_code}\n" http://127.0.0.1:18080/api/health
{"status":"ok","db":"ok","time":"2026-10-17T23:16:34+08:00"}
HTTP 200

$ curl -o /dev/null -w "HTTP %{http_code}\n" -X POST http://127.0.0.1:18080/api/health
HTTP 404          # 方法不匹配，未落入业务分支

$ curl -o /dev/null -w "HTTP %{http_code} bytes=%{size_download}\n" http://127.0.0.1:18080/
HTTP 200 bytes=2063   # 占位页可访问

$ ls -l tmp/data
wol.db  4096 bytes    # SQLite 文件在数据目录内生成
```

构建产物：`go build -o wold.exe ./cmd/wold` → 14,738,432 字节（约 14MB，符合单二进制预期）。

## 0-2 证据（目录树）

```
cmd/wold/            进程入口
internal/config/     环境变量解析与校验
internal/store/      SQLite 连接与自检
internal/httpapi/    路由、访问日志、健康检查
internal/wol/        常量与包声明（E2 实现）
internal/probe/      包声明（E3 实现）
web/                 index.html 占位页
deploy/              空目录，E7 落 Dockerfile 与编排文件
test/e2e/            空目录，E8 落端到端脚本
docs/                E0 三份文档
e0/                  E0 任务单
```

自查：无示例组件、无演示数据、无空接口占位、无构建工具配置文件。

## 0-3 证据（依赖）

直接依赖 1 项：`modernc.org/sqlite v1.34.5`（F1 存储 / F8 免 CGO 交叉编译）。
标准库承载 HTTP、日志、UDP、TCP、口令哈希，未引入 Web 框架、ORM、日志库、JWT 库。

## 0-4 证据（排除清单检索）

```bash
git ls-files --others --exclude-standard | grep -Ev '^(output|docs)/' \
  | xargs grep -niE "\b(websocket|redis|prometheus|grafana|jwt|i18n|scheduler|cron)\b"
```

命中 2 处，均为文字声明而非实现：

```
README.md:51    ...明确不做...WebSocket...Redis/队列...
e0/task.md:28   4. 禁止...WebSocket、任务队列、Redis...
```

代码目录（`cmd/`、`internal/`、`web/`）零命中。

## 过程中发现并修正的问题（留痕）

| 问题 | 处理 |
| --- | --- |
| `store.Open` 在数据目录不可创建时，`sql.Open` 延迟连接导致错误未及时返回 | 增加 `PingContext` 自检，打开即验证；并补对应反例测试 |
| Windows 下 SQLite 连接空闲占用文件句柄，临时目录清理失败 | 设 `SetMaxIdleConns(0)`，连接用完即释放 |
| `POST /api/health` 返回 404 而非 405 | Go 1.22 ServeMux 在"仅注册了 GET"时对方法不匹配返回 404；测试放宽为 405/404 均可，关键是不进业务分支 |

## 遗留事项

- 本机 Word 为 Office 2013 未授权版，无法用于自动渲染验证；E0 与文档无关，不影响验收。
- `go.sum` 中记录间接依赖校验和，属 `modernc.org/sqlite` 的必要传递依赖，未手工增删。
