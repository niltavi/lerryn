---
name: lerryn
description: Lerryn 的入口 Skill：说明 Lerryn 能做什么，Agent 如何连接实例（远程 MCP、CLI stdio 代理、HTTP API），有哪些内置 MCP 工具，以及创建任务、检查任务和运行进度的规范做法。用户第一次接入 Lerryn、问 Lerryn 能做什么、想把一件事交给 Lerryn 定时或按 Webhook 自动执行、问某个任务或运行现在怎么样、要看今天有哪些任务失败或待处理时使用。具体建任务、凭证、排错和运维转到对应的 lerryn-* Skill。
license: Apache-2.0
---

# Lerryn

Lerryn 是企业自部署的自动化沙箱。你是调度侧：替用户把需求写成任务（脚本加清单），提交到 Lerryn，测试、设置触发器、激活，之后查看运行和结果。代码不在你本地执行，而是由平台在加固的 Docker 容器里运行；凭证由用户在平台填写页提交，你只接触引用名。

## 能力一览

| 能力 | 说明 |
| --- | --- |
| 运行时 | Python、Node、Shell，以 `discover` 返回的 `runtimes` 为准 |
| 触发方式 | 手动、cron 定时（必须带 IANA 时区）、Webhook |
| 版本 | 每次提交生成不可变版本；触发器只运行激活的版本；默认未经成功测试的版本不能激活 |
| 运行记录 | 状态、阶段、日志、业务结果（`sbx result`）、产物、事件，带控制台链接 |
| 凭证 | API Key、Token、Cookie 等由用户在填写页提交，加密保存，运行时注入 |
| 项目工具 | 登记外部 HTTP API、MCP 服务或脚本包，任务在运行时引用 |
| AI 能力 | 用当前账号配置的模型生成文本（`ai_models`、`ai_chat`） |
| 其余接口 | 项目、成员、通知渠道、运行历史等，经 `api_describe` 和 `api_call` 使用 |

## 连接实例

1. **先看是否已连上。** 工具列表里有 `discover` 时直接调用它，跳到第 4 步。
2. **问实例地址。** 地址在控制台 Settings → Tools 页，形如 `https://lerryn.example.com`。可以先不带令牌请求 `GET <地址>/api/v1/discover` 确认实例可达，匿名调用时 `principal` 为 null。
3. **让用户配置连接。** 访问令牌由用户在控制台 Settings → 访问令牌中创建，自己写进客户端配置或环境变量。不在对话里索要、复述或保存令牌。按客户端选择方式，配置示例见 [references/connect.md](references/connect.md)：
   - 支持远程 MCP：连接 `<地址>/mcp`（Streamable HTTP），请求头 `Authorization: Bearer <令牌>`。
   - 只支持 stdio：用户先 `lerryn login <地址> --token-stdin`，再把 `lerryn mcp --stdio` 注册为 MCP 服务器。
   - 都不方便时：直接调 HTTP API（`<地址>/api/v1`）或 `lerryn` CLI，操作名与 MCP 工具一一对应。
4. **发现能力。** 调用 `discover`，记下：
   - `principal.personal_project_id`：默认的 `project_id`；`principal.scopes`：令牌范围，缺 `write` 或 `execute` 时提醒用户换令牌。
   - `runtimes`、`capabilities.network_policies`、`capabilities.require_test_before_activate`。
   - `skills.version`：与本地 Skill 目录下 `.lerryn-skill` 记录的版本不同，或本地缺少 `lerryn-*` Skill 时，请用户运行 `lerryn skills install` 安装同版本的 Skill。
5. **确认环境。** 要做有副作用的操作时，先调用 `environment_get`，生产环境（`production`）上更谨慎。

## 内置 MCP 工具

| 用途 | 工具 |
| --- | --- |
| 发现 | `discover`、`environment_get` |
| 任务 | `task_list`、`task_get`、`task_apply`（支持 `dry_run`）、`task_validate`、`task_set_tags` |
| 触发与激活 | `trigger_set`（支持 `dry_run`）、`task_set_active` |
| 运行 | `run_start`、`run_get`（`wait` 最多 60 秒）、`run_logs`、`run_cancel`、`run_rerun` |
| 凭证 | `credential_request` |
| AI | `ai_models`、`ai_chat` |
| 通用 | `api_describe`（按 `operation_id` 或 `query` 查接口说明）、`api_call`（参数放在 `arguments` 里） |

没有独立工具的操作，例如 `getOverview`、`listRuns`、`listRunEvents`、`listArtifacts`、`getCredentialRequest`、`listTools`、`registerTool`，先用 `api_describe` 查 `input_schema`，再用 `api_call` 调用。工具、操作、令牌范围和 CLI 命令的完整对照在 `lerryn-automation` Skill 的 `references/tools.md`。

## 创建任务的规范

完整流程和硬规则在 `lerryn-automation` Skill，动手前先读它。要点：

1. **问清需求。** 做什么、输入从哪来、结果交到哪里、多久一次（时区）、需要哪些外部账号、是否会发消息或写外部数据（`side_effects`）。
2. **先查重。** 用 `task_list` 按 `q` 或 `tag` 找有没有相近的任务。找到时先告诉用户它的名称、标题和现状，确认是修改它还是另建，不要直接新建重复任务，也不要未经确认就改别人的任务。
3. **写清单和脚本。** 运行时从 `runtimes` 里选；`manifest.title` 用用户的语言写简短标题；`manifest.workflow` 按执行顺序写出每一步；脚本用 `sbx` 上报阶段和结果。
4. **预览再提交。** `task_apply` 先带 `dry_run: true` 看校验和 `diff`；修改已有任务时先 `task_get`，把 `latest_version.seq` 作为 `if_match`。
5. **凭证。** 需要外部账号时，告诉用户会发起填写请求、由他在填写页提交；不要问用户“有没有现成的 Token”或让他“提供”密钥，这会引导用户把值贴进对话。出现 `credential_missing` 时转到 `lerryn-credentials` Skill。
6. **测试并核对结果。** `run_start` 带 `test: true`，等结束后逐项核对 `result`。有副作用的任务，测试前先征得用户同意。
7. **触发器和激活。** `trigger_set` 先 `dry_run`，把 `schedule_text` 和 `next_fires`（换算到用户时区）复述给用户确认；再用 `task_set_active` 激活测试通过的版本。
8. **交付。** 告诉用户任务 ID、版本、测试运行 ID、结果摘要和 `links.console`。

## 检查进度

按用户问的范围选入口，细节见 [references/progress.md](references/progress.md)：

| 用户问 | 做法 |
| --- | --- |
| “这次运行怎么样了” | `run_get` 带 `wait`（最多 60 秒）；看 `status`、`stages`、`result`、`error`。需要细节时 `run_logs` 带 `tail` |
| “这个任务最近跑得怎么样” | `task_get` 看 `state` 和 `active_version`；`api_call` 调用 `listRuns`，带 `task_id` 和 `limit` |
| “整体情况、有什么要处理的” | 按下面两步，不要逐个 `task_get`、`run_get` |
| “有哪些任务” | `task_list`，可按 `project_id`、`tag`、`q` 筛选 |
| “产出了什么文件” | `api_call` 调用 `listArtifacts`；下载用 CLI 或控制台链接 |

整体情况固定两次调用，参数已经给出，不必先 `api_describe`：

1. `api_call` 传 `{"operation_id": "getOverview", "arguments": {}}`：`counts`、`attention`（运行失败、超时、中断，待填或将过期的凭证）、`active`（进行中）、`upcoming`（下次触发）。
2. `api_call` 传 `{"operation_id": "listRuns", "arguments": {"since": "<用户时区今天 0 点，RFC 3339>", "limit": 50}}`：`attention` 不收业务结果有问题的运行，要从这里看。每条带 `status`、`result_status`、`result_summary`、`test`、`trigger`、`links`；`completed` 而 `result_status` 为 null，就是没有上报结果。

先报需要处理的，再报进行中和接下来的触发，正常的一句带过。

汇报时说清：状态、业务结果（`result.status` 和摘要）、失败在哪个阶段、下一次触发时间，并附 `links.console`。测试运行还是正式运行只看运行的 `test` 字段，来源看 `trigger.kind`（手动、定时、Webhook、自动重试），不要从任务是否激活去推断。下一步建议用用户的话说（“激活这个版本”），不要把工具名当成给用户的操作说明。运行失败或结果不对时转到 `lerryn-troubleshoot` Skill。

## 转到其他 Skill

| 场景 | Skill |
| --- | --- |
| 建立、修改、测试、激活任务，写清单和 `sbx` | `lerryn-automation` |
| 任务需要 API Key、Token、Cookie，或要更换、撤销凭证 | `lerryn-credentials` |
| 运行失败、超时、中断、结果不对，API 返回错误 | `lerryn-troubleshoot` |
| 部署、升级、备份、扩容 Lerryn 服务端 | `lerryn-operate` |

## 硬规则

1. 先调用 `discover` 再操作。不凭记忆猜接口、字段和运行时，不认识的参数用 `api_describe` 查。
2. 访问令牌和凭证的明文不进对话、代码、清单、`params` 和日志。令牌由用户自己配置到客户端，凭证只经 `credential_request` 的填写页提交。
3. `completed` 只表示进程以 0 退出。没有看到 `result` 时，不向用户宣称业务成功。
4. 有副作用的任务，测试运行、重跑之前先征得用户同意；失败后不自动重跑。
5. 等待运行时用 `run_get` 的 `wait`，不要连续快速轮询。超过几分钟的运行，把运行 ID 和 `links.console` 交给用户，之后再查。
6. 每次完成操作，把任务或运行的 `links.console` 交给用户。
