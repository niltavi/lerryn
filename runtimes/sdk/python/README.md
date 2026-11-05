<!-- SPDX-License-Identifier: Apache-2.0 -->

# sbx：Python 任务的上报工具

Lerryn 在每个 Python 任务的容器里提供 `sbx` 命令和同名的 Python 模块，用来上报阶段和业务结果。只依赖标准库，支持 Python 3.8 及以上。

容器里的位置：

| 路径 | 内容 |
| --- | --- |
| `/sbx/sdk/bin/sbx` | 命令，已在 `PATH` 中 |
| `/sbx/sdk/sbx/` | Python 模块，`/sbx/sdk` 已在 `PYTHONPATH` 中，直接 `import sbx` |

## 命令行

```bash
sbx stage start fetch
sbx stage done fetch --message "获取 120 条记录"
sbx stage fail save --message "HTTP 503"
sbx result --status ok --summary "生成 3 份摘要" --data-json '{"count": 3}'
sbx result --status partial --summary "3 份中保存了 2 份" --data-file result.json
```

- 阶段 ID 与清单中 `stages[].id` 的规则相同：1 到 63 个字符，只能包含小写字母、数字、`_` 和 `-`，以字母或数字开头。没有在清单中声明的阶段也可以上报。
- `--status` 取 `ok`、`partial` 或 `failed`。
- `--data-json` 和 `--data-file` 二选一，内容是任意 JSON 值，编码后不超过 64 KiB。
- 参数有误时退出码为 2，写入失败或数据不合法时为 1。

`python -m sbx ...` 与 `sbx ...` 等价。

## 调用 MCP 工具

清单的 `tools` 引用了项目中登记的 `mcp` 工具时，任务可以用 `sbx mcp call` 调用它。调用经 Streamable HTTP 直接从容器发出，需要清单的 `network` 为 `full`。每次调用在事件文件里记一条 `tool.call`，平台据此显示实际发生的工具调用；任务自己发请求，平台看不到。

```bash
sbx mcp call crm-mcp search_accounts --args '{"query": "acme"}' -H "Authorization: Bearer $CRM_TOKEN"
sbx mcp call crm-mcp search_accounts --args-file /sbx/work/query.json --timeout 120
```

```python
import os
import sbx

res = sbx.mcp_call("crm-mcp", "search_accounts", {"query": "acme"},
                   headers={"Authorization": "Bearer " + os.environ["CRM_TOKEN"]})
print(res["structuredContent"])
```

- 工具的地址取自 `LERRYN_TOOLS`。工具不在本次运行的工具里、不是 `mcp` 类型、连不上、返回 HTTP 错误或 JSON-RPC 错误，以及工具返回 `isError` 时，命令退出码为 1，`mcp_call` 抛出 `sbx.ToolError`（工具返回的错误结果在 `ToolError.result` 中）。参数有误时退出码为 2。
- 命令把 MCP 的 CallToolResult 作为一行 JSON 打印到 stdout；工具返回 `isError` 时也打印，再以 1 退出。
- `--args`、`--args-file` 必须是 JSON 对象，省略时为 `{}`。`-H` 可以重复，每个请求都带上，令牌从清单的 `credentials` 注入的环境变量或文件里取。每次请求默认等待 60 秒。
- 记录里只有工具名、MCP 工具名、成功与否、耗时、参数的前 200 个字符和失败原因，不记录返回内容。runner 遮盖其中的凭证值。

sh 实现的 `sbx` 命令参数相同：有 Node 时交给 Node 辅助库执行，否则用 `curl` 或 `wget`。

## Python

跨运行的游标可以直接写入 `/sbx/state`，不需要调用提交 API：

```python
from pathlib import Path

cursor = Path("/sbx/state/counter.txt")
before = int(cursor.read_text()) if cursor.exists() else 0
cursor.write_text(str(before + 1))
```

同一 TaskID 的不同版本和 runner 共享状态。只有进程退出码为 0 的完成运行提交，测试运行同样提交；业务结果 `failed` 或 `partial` 本身不改变这个条件。失败、取消、超时保留旧快照，正常空目录会清空。状态最多 10 MiB、1000 项，路径最多 255 个 UTF-8 字节；链接、特殊文件或不完整目录会使整包提交失败。并发以最后成功提交的运行覆盖，不合并目录。

```python
import sbx

params = sbx.params()            # LERRYN_PARAMS 解码后的 dict

with sbx.stage("fetch"):         # 正常结束报 done，抛出异常报 fail 并重新抛出
    records = fetch(params["source"])

sbx.stage_start("save")
try:
    save(records)
    sbx.stage_done("save", f"保存了 {len(records)} 条")
except Exception as exc:
    sbx.stage_fail("save", str(exc))
    raise

sbx.result("ok", f"处理了 {len(records)} 条记录", {"count": len(records)})
```

- `with sbx.stage(...)` 中调用 `sys.exit(0)` 算作 done。
- `message` 超过 1 KiB、`summary` 超过 2 KiB 时截断并以 `…` 结尾；`data` 编码后超过 64 KiB 时抛出 `ValueError`，不会静默丢弃。

每次上报阶段时，`sbx` 先刷新 stdout 和 stderr，再向 stdout（文件描述符 1）写一行阶段标记，然后写入事件。runner 从日志中去掉这一行，并在 stdout 的这个位置切换日志行的阶段，所以 stdout 的每一行都标在打印它时所在的阶段，输出量再大也一样。stderr 的行按到达时 stdout 的位置标注，接近但不保证准确。标记只在 runner 提供了 `LERRYN_LOG_MARKER` 时写出；任务如果捕获了 `sbx` 命令的 stdout，会在其中看到这一行。

不在 Lerryn 中运行（没有 `LERRYN_EVENTS_FILE`）时，事件打印到 stderr，便于本地调试。

## 事件文件格式

`sbx` 把事件追加到 `LERRYN_EVENTS_FILE` 指向的文件（`/sbx/events/events.ndjson`），runner 读取后上报。用文件而不用网络，`network: none` 的任务也能上报。

文件是 NDJSON：每行一个 JSON 对象，以 `\n` 结尾，用一次追加写入完成，多个进程同时写也不会交错。格式版本 1：

```json
{"v":1,"ts":"2026-11-02T08:00:01.234Z","kind":"stage.start","stage":"fetch"}
{"v":1,"ts":"2026-11-02T08:00:03.120Z","kind":"stage.done","stage":"fetch","message":"获取 120 条记录"}
{"v":1,"ts":"2026-11-02T08:00:09.877Z","kind":"stage.fail","stage":"save","message":"HTTP 503"}
{"v":1,"ts":"2026-11-02T08:00:09.901Z","kind":"result","status":"partial","summary":"3 份中保存了 2 份","data":{"saved":2}}
{"v":1,"ts":"2026-11-02T08:00:10.020Z","kind":"tool.call","tool":"crm-mcp","method":"search_accounts","ok":true,"duration_ms":118,"args_summary":"{\"query\":\"acme\"}"}
```

| 字段 | 说明 |
| --- | --- |
| `v` | 格式版本，目前为 1。缺省按 1 处理，其他值整行忽略 |
| `ts` | UTC 时间，RFC 3339。缺省、格式不对或与 runner 读到的时间相差超过 1 小时，改用读到的时间 |
| `kind` | `stage.start`、`stage.done`、`stage.fail`、`result` 或 `tool.call` |
| `stage` | 阶段事件必填，规则同上 |
| `message` | 阶段事件可选，超过 1 KiB 截断 |
| `status` | 结果事件必填：`ok`、`partial` 或 `failed` |
| `summary` | 结果事件的摘要，超过 2 KiB 截断 |
| `data` | 结果事件可选，任意 JSON 值；编码后超过 64 KiB 或不是合法的 UTF-8 时丢弃 `data`，保留结果的其他部分 |
| `mark` | 阶段事件可选，同一事件在 stdout 上的阶段标记的 ID，1 到 32 个小写十六进制字符。runner 据此把事件和标记对上 |
| `tool`、`method`、`ok` | `tool.call` 必填：登记的工具名、调用的 MCP 工具名、是否成功 |
| `duration_ms` | `tool.call` 可选，调用耗时 |
| `args_summary` | `tool.call` 可选，参数的紧凑 JSON，超过 200 个字符截断 |
| `error` | `tool.call` 在 `ok` 为 false 时可选，失败原因，超过 1 KiB 截断 |

其他字段忽略。

stdout 上的阶段标记是一整行：`\x1elerryn <令牌> <mark> <kind> <stage>`，字段之间各一个空格，令牌是 `LERRYN_LOG_MARKER` 的值。令牌不符或字段不合规则的行按普通输出处理。标记的处理见技术设计第 7.7 节和 `server/internal/runner/markers.go`。

runner 的处理规则：

- 只读取以换行结束的完整行。进程结束时最后一行没有换行，说明写到一半被杀，这一行忽略。
- 单行超过 128 KiB、不是 JSON 对象、`kind` 未知、字段不合规则的行，在运行日志里写一条 system 警告后跳过，运行继续。
- 每次运行最多 10000 个事件，事件文件最多读取 64 MiB。
- `stage.start` 把该阶段设为当前阶段；同一阶段的 `stage.done` 或 `stage.fail` 结束它，回到外层阶段。运行失败时，错误所在阶段取当前阶段；没有当前阶段时，取最近一次报告失败的阶段。
- 多次上报 `result` 时，runner 按顺序全部转交服务端。

格式在 runner 一侧的实现见 `server/internal/runner/events.go`。Node 辅助库（`runtimes/sdk/node`）和 sh 实现的 `sbx` 命令（`runtimes/sdk/sh`）写同样的格式。改动格式时各处同时修改，并递增 `v`。
