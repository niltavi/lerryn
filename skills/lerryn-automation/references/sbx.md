# sbx：在任务里上报阶段和结果

每个任务容器里都有 `sbx` 命令（`/sbx/sdk/bin/sbx`，已在 `PATH` 中），Python 和 Node 另有同名的辅助库。不需要写进依赖，也不需要网络。

## 命令行

```bash
sbx stage start fetch
sbx stage done fetch --message "获取 120 条记录"
sbx stage fail save --message "HTTP 503"
sbx result --status ok --summary "生成 3 份摘要" --data-json '{"count": 3}'
sbx result --status partial --summary "3 份中保存了 2 份" --data-file /sbx/work/result.json
```

- `--status` 取 `ok`、`partial` 或 `failed`。业务没完成时如实上报 `partial` 或 `failed`，不要上报 `ok`。
- `--data-json` 与 `--data-file` 二选一，任意 JSON 值，编码后不超过 64 KiB。
- `message` 超过 1 KiB、`summary` 超过 2 KiB 时截断。
- 参数有误时退出码为 2，写入失败或数据不合法时为 1。

## Python

```python
import sbx

params = sbx.params()                 # LERRYN_PARAMS 解码后的 dict

with sbx.stage("fetch"):              # 正常结束报 done，抛出异常报 fail 并重新抛出
    rows = fetch(params["source"])

sbx.result("ok", f"处理了 {len(rows)} 条", {"count": len(rows)})
```

另有 `sbx.stage_start(id)`、`sbx.stage_done(id, message)`、`sbx.stage_fail(id, message)`。`data` 超过 64 KiB 时抛出 `ValueError`。

## Node

```ts
import * as sbx from "@lerryn/sbx";

interface Params { source: string }
const params = sbx.params<Params>();

const rows = await sbx.stage("fetch", () => fetchRows(params.source));   // 返回或 resolve 报 done，抛出或 reject 报 fail
sbx.result("ok", `处理了 ${rows.length} 条`, { count: rows.length });
```

`@lerryn/sbx` 由平台挂载，任务文件直接 `import`，不要写进 `package.json`。另有 `sbx.stageStart`、`sbx.stageDone`、`sbx.stageFail`。在 `sbx.stage()` 里调用 `process.exit()` 会直接结束进程，不报 done 或 fail。

## Shell

```bash
#!/usr/bin/env bash
set -euo pipefail
sbx stage start fetch
count=$(wc -l < /sbx/task/data.txt)
sbx stage done fetch --message "读取 ${count} 行"
sbx result --status ok --summary "读取 ${count} 行" --data-json "{\"count\": ${count}}"
```

## 阶段标记

`sbx` 上报阶段时，先刷新任务的输出，再向 stdout 写一行阶段标记，然后写事件。runner 从日志中去掉这一行，并在这个位置切换日志行的阶段，所以日志能按阶段准确分开。这带来一条限制：

- 不要用 `$(sbx ...)`、管道或重定向捕获 `sbx` 的 stdout。被捕获的输出里会混进标记行，日志的阶段标注也会退回近似。直接调用即可，`sbx` 的退出码足够判断成败。

## 工具

清单的 `tools` 列出项目中登记的工具名，可用的工具用 `api_call` 调用 `listTools` 查看。运行时：

- `http`、`mcp` 工具的地址在 `LERRYN_TOOL_<名称>_URL`（名称转大写，`-` 换成 `_`）。调用它们需要清单的 `network` 为 `full`。
- `script` 工具的文件以只读方式挂载在 `/sbx/tools/<名称>`，目录在 `LERRYN_TOOL_<名称>_DIR`。`network: none` 也能用。
- 全部工具以 JSON 数组放在 `LERRYN_TOOLS`。

调用 `mcp` 工具用 `sbx mcp call`。这样的调用记进运行事件（`tool.call`），控制台显示为平台记录的工具调用；任务自己发请求，平台看不到。

```bash
sbx mcp call crm-mcp search_accounts --args '{"query": "acme"}' -H "Authorization: Bearer $CRM_TOKEN"
```

```python
res = sbx.mcp_call("crm-mcp", "search_accounts", {"query": "acme"},
                   headers={"Authorization": "Bearer " + os.environ["CRM_TOKEN"]})
```

```ts
const res = await sbx.mcpCall("crm-mcp", "search_accounts", { query: "acme" },
  { headers: { Authorization: `Bearer ${process.env.CRM_TOKEN}` } });
```

- 结果是 MCP 的 CallToolResult：`content`，以及工具提供时的 `structuredContent`。命令把它作为一行 JSON 打印到 stdout，只有这一行，可以用 `$(...)` 捕获。
- 工具返回 `isError` 或调用失败时，命令退出码为 1，Python 和 Node 抛出 `ToolError`。参数有误时退出码为 2。
- 工具需要的令牌在清单的 `credentials` 中声明，用 `-H`（`headers`）交给工具，不要写进 `--args`。记录只保留参数的前 200 个字符，凭证的值会被遮盖。
- 每次请求默认等 60 秒，`--timeout`（`timeout`）可以调整。

可复用的脚本可以登记成 `script` 工具：`api_call` 调用 `registerTool`，`kind` 为 `script`，`files` 与 `task_apply` 的文件写法相同，`description` 写清楚用法。其他任务在 `tools` 中引用后就能使用。再次登记会整体替换文件，引用它的任务下一次运行时生效，运行日志记下挂载的是哪一份。

## 运行环境

| 位置 | 内容 |
| --- | --- |
| `/sbx/task` | 任务文件，只读，也是工作目录 |
| `/sbx/work` | 可写的工作目录，运行结束后删除 |
| `/sbx/out` | 运行产物，结束后上传 |
| `/sbx/state` | 按 TaskID 跨版本和 runner 保存的完整状态，成功结束才提交 |
| `/tmp` | 可写的临时目录 |
| `/run/secrets/<name>/` | `as: file` 的凭证 |
| `/sbx/tools/<name>/` | 引用的 `script` 工具，只读 |

| 环境变量 | 内容 |
| --- | --- |
| `LERRYN_PARAMS` | 本次运行的参数，JSON |
| `LERRYN_RUN_ID` | 运行 ID |
| 凭证字段名 | `as: env` 的凭证 |

根文件系统只读，进程以 uid 65532 运行，没有特权。退出码 0 记为 `completed`，非 0 记为 `failed`（`script_error`）。

状态目录首次为空，运行前读取领取时固定的快照。测试运行也会提交，`sbx result --status failed` 本身不改变退出码；要保留旧状态应让任务以非零退出。正常空目录会清空旧状态。普通文件内容总量最多 10 MiB，包含空目录在内最多 1000 项，路径最多 255 个 UTF-8 字节；目录缺失、不可读、超限或含链接／特殊文件时整包提交失败，旧快照保留。并发运行以最后成功提交的事务覆盖，平台不会合并目录。
