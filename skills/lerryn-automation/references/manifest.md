# 任务清单

`task_apply` 的请求由三部分组成：路径上的 `project_id` 和任务名（`task_name`），请求体里的 `manifest` 和 `files`。字段的完整定义和取值范围见 [manifest-fields.md](manifest-fields.md)，它由接口契约生成，以它为准。

## 最小清单

```json
{
  "project_id": "<discover 返回的 principal.personal_project_id>",
  "task_name": "weekday-digest",
  "manifest": {
    "description": "工作日早上汇总 CRM 新线索",
    "runtime": {"language": "python", "version": "3.12"},
    "entrypoint": ["python", "main.py"]
  },
  "files": [{"path": "main.py", "content": "import sbx\nsbx.result(\"ok\", \"完成\")\n"}]
}
```

- 任务名：小写字母、数字和连字符，1 到 63 个字符，项目内唯一。同名提交生成新版本。
- `entrypoint` 是 argv，工作目录为 `/sbx/task`（任务文件，只读）。
- `files` 是全部文件，UTF-8 文本，最多 100 个，合计不超过 1 MiB。没有列出的文件不会保留。
- 修改已有任务必须带 `if_match`（读到的 `latest_version.seq`），否则返回 `precondition_required`。

## 运行时与依赖

运行时只能从 `discover` 的 `runtimes` 中选：

| language | version | 入口示例 | 依赖 |
| --- | --- | --- | --- |
| `python` | `"3.12"` | `["python", "main.py"]` | `lockfile: uv.lock`，同时提交 `pyproject.toml` |
| `node` | `"22"` | `["node", "main.ts"]` 或 `["node", "main.mjs"]` | `lockfile: package-lock.json`，同时提交 `package.json` |
| `shell` | `"5"` | `["bash", "main.sh"]` | 不支持锁文件 |

- 不设置 `lockfile` 时只能用运行时自带的库。Python 只有标准库；Node 只有内置模块和 `@lerryn/sbx`。
- 依赖在单独的安装容器里按锁文件安装（Python 为 `uv sync --frozen`，Node 为 `npm ci --omit=dev`），结果按锁文件内容缓存。锁文件和 `pyproject.toml` 或 `package.json` 不一致时，运行在准备阶段以 `dependency_install_failed` 失败，日志里有安装输出。
- 安装容器可以联网，依赖的安装脚本照常执行，`npm ci` 也会执行任务自己 `package.json` 里的 `postinstall`。Node 运行时镜像带 curl、wget、git 和编译工具链，在 postinstall 里下载预编译二进制的包（例如 `@larksuite/cli`）和需要 node-gyp 编译的包都能装上。Shell 运行时只有 busybox 的 `wget`，没有 curl。
- 加依赖时，锁文件和 `package.json`（或 `pyproject.toml`）要一起改。npm 的锁文件版本 3 中，每个依赖至少要有 `packages["node_modules/<名字>"].version`，根项 `packages[""].dependencies` 与 `package.json` 一致。
- Node 22 直接运行 `.ts` 文件，只支持可以直接擦除的类型注解；`enum`、`namespace` 不能用。用 ES 模块写法（`import`），文件名用 `.ts`、`.mts` 或 `.mjs`。
- Shell 镜像基于 Alpine，有 bash 和 busybox 的常用工具（含 `wget`），没有 curl 和 jq。

## 参数

`params` 是默认参数，运行时以 JSON 放在环境变量 `LERRYN_PARAMS` 中，`sbx.params()` 读取。`run_start` 和触发器的 `params` 整体替换它，不做合并。参数会出现在运行记录里，不放凭证。

## 阶段与结果

`stages` 声明阶段（`id`、`name`、可选的 `after`），任务用 `sbx stage` 上报进度，控制台按声明的顺序显示。阶段 ID 是小写字母、数字、`_`、`-`。不声明也能上报，但声明后控制台能显示还没开始的阶段。

业务结果用 `sbx result` 上报，见 [sbx.md](sbx.md)。不上报时 `result` 为 null，平台不知道业务是否成功。

## 资源限制与网络

```json
"limits": {"timeout": "10m", "cpu": 1, "memory": "512Mi"},
"network": "none"
```

- `timeout` 默认 `10m`，最长 `24h`，超时后运行为 `timed_out`。
- `memory` 默认 `1Gi`，范围 `64Mi` 到 `16Gi`，超出时进程被杀，错误类别 `out_of_memory`。
- `network` 默认 `full`（可以访问外部网络），`none` 为无网络。只能用 `discover` 的 `capabilities.network_policies` 中列出的值。不需要网络的任务用 `none`；`sbx` 上报不走网络，`none` 下照常工作。

## 并发、重试和副作用

- `concurrency` 默认 `forbid`：上一个运行没结束时，`run_start` 返回 `run_in_progress`，定时触发记为已跳过。
- `retry.max_attempts` 默认 0，不自动重试。只有能安全重复执行的任务才开启。
- 任务会改动外部系统（发消息、写数据、下单）时写 `side_effects: true`。失败后由人决定是否重跑。

## 凭证引用

```json
"credentials": [{"name": "LLM_API_KEY", "as": "env", "description": "调用模型"}]
```

清单只写引用名，不写值。`as: env` 时凭证的每个字段成为同名环境变量；`as: file` 时写入 `/run/secrets/<name>/<字段名>`。绑定哪个凭证由凭证所有者的授权决定，填写和更换凭证都不产生新版本。缺少绑定时按 `lerryn-credentials` Skill 处理。

## runner 标签

`runner_labels` 让任务只在带有全部这些标签的 runner 上执行，例如需要访问内网的任务。没有满足标签的在线 runner 时，`task_validate` 和运行都会报 `runner_unavailable`。不确定时不写。

## Agent 面板

`panels` 在控制台展示 `sbx result` 的 `data` 中的数据，只有五种声明式类型：`metric`、`bars`、`breakdown`、`table`、`text`。`source` 是指向 `data` 的 JSON Pointer。

```json
"panels": [
  {"id": "new-leads", "type": "metric", "title": "新线索", "source": "/new_leads"},
  {"id": "by-source", "type": "breakdown", "title": "来源", "source": "/by_source"}
]
```

对应的结果：`sbx.result("ok", "新增 12 条", {"new_leads": 12, "by_source": [{"label": "官网", "value": 8}, {"label": "展会", "value": 4}]})`。数据形状不符时面板不显示，不报错。
