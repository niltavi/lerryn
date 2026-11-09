# Tools 接入

[English](../en/tools.md) · [文档目录](README.md)

Lerryn 向外部 Agent 提供 MCP、HTTP API 和 CLI，用于创建与更新任务、设置触发器、执行任务，以及读取日志和结果。Settings → Tools 列出内置能力和当前实例的接入地址，提供本文下载；项目工具单独列在页面下方，供任务在运行时引用。

以下示例使用 `https://lerryn.example.com`。接入时替换成 Tools 页显示的实例地址，客户端与服务端尽量使用同一版本。

## 访问令牌

登录控制台，在 Settings → 访问令牌中创建令牌。令牌只在创建时显示一次，将它保存到客户端的凭证配置中，通过 `Authorization: Bearer <令牌>` 请求头发送。令牌范围和项目角色共同决定可执行的操作：

| 范围 | 用途 |
| --- | --- |
| `read` | 发现能力、读取任务、运行、日志和接口说明 |
| `write` | 提交任务、配置触发器、申请凭证、登记项目工具 |
| `execute` | 发起、取消、重跑、激活或暂停任务，以及调用 AI 模型 |

创建和维护任务的 Agent 通常需要这三个范围；只查询运行结果时可只授予 `read`。令牌继承所属用户在项目中的权限，范围不会提升项目角色。令牌过期或撤销后，在客户端更换令牌。不要把令牌放进 URL、任务文件或任务参数。

## 连接 MCP

远程 MCP 端点是 `https://lerryn.example.com/mcp`，使用 Streamable HTTP 和访问令牌。支持 `mcpServers`、`url`、`headers` 配置的客户端可参考下面的结构；其他客户端在对应的服务器地址、传输方式和认证栏中填写相同信息。

```json
{
  "mcpServers": {
    "lerryn": {
      "url": "https://lerryn.example.com/mcp",
      "headers": {
        "Authorization": "Bearer <LERRYN_ACCESS_TOKEN>"
      }
    }
  }
}
```

`<LERRYN_ACCESS_TOKEN>` 由用户在客户端本地替换。连接后先调用 `discover`，确认 `principal`、`runtimes`、`capabilities` 和 `skills.version`。运行时从返回的列表中选择。

仅支持本地 stdio 的客户端使用 CLI 代理。先登录并安装 Skill，再把 `lerryn mcp --stdio` 注册为 MCP 服务器：

```bash
lerryn login https://lerryn.example.com --token-stdin < token.txt
lerryn skills install
lerryn mcp --stdio
```

令牌文件按凭证保管。最后一行由 MCP 客户端启动，命令为 `lerryn`，参数为 `["mcp", "--stdio"]`。CLI 也支持本地账号登录：`lerryn login https://lerryn.example.com --username alice`，密码在终端隐藏输入。OIDC 用户可使用控制台创建的令牌。

## 内置 MCP 工具

| 工具 | HTTP 操作 | 范围 | 用途 |
| --- | --- | --- | --- |
| `discover` | `discover` | `read` | 版本、运行时、部署能力、当前身份 |
| `task_get` | `getTask` | `read` | 任务、标签和最新版本 |
| `task_list` | `listTasks` | `read` | 查询可见任务，支持按项目、名称或标签筛选 |
| `task_set_tags` | `setTaskTags` | `write` | 整体替换标签，空数组清空，不创建新版本 |
| `task_apply` | `applyTask` | `write` | 创建任务或提交完整新版本，支持 `dry_run` |
| `task_validate` | `validateTaskVersion` | `read` | 检查凭证、工具、runner 和测试状态 |
| `trigger_set` | `setTriggers` | `write` | 整体替换定时或 Webhook 触发器，支持预览 |
| `task_set_active` | `setTaskActivation` | `execute` | 激活版本或暂停任务 |
| `run_start` | `startRun` | `execute` | 发起测试或正式运行 |
| `run_get` | `getRun` | `read` | 状态、阶段、结果与错误，`wait` 最多 60 秒 |
| `run_logs` | `readRunLogs` | `read` | 末尾日志或按游标分页读取 |
| `run_cancel` | `cancelRun` | `execute` | 请求取消；之后读取状态确认 |
| `run_rerun` | `rerunRun` | `execute` | 新建关联原运行的重跑 |
| `credential_request` | `createCredentialRequest` | `write` | 生成用户凭证填写链接 |
| `ai_models` | `listAIModels` | `read` | 列出当前账号启用的模型 |
| `ai_chat` | `createAIChat` | `execute` | 使用当前账号配置的模型生成文本 |
| `api_describe` | 接口说明与 schema | `read` | 按 `operation_id` 或 `query` 查询 |
| `api_call` | 指定公开操作 | 按接口 | 按 `operation_id` 调用，参数放在 `arguments` 中 |

MCP 的主流程工具覆盖常用操作。其余接口先查询说明，再调用，例如读取项目工具列表：

```json
{"operation_id": "listTools"}
```

将上面的参数传给 `api_describe`，再将下面的参数传给 `api_call`：

```json
{"operation_id": "listTools", "arguments": {"project_id": "<PROJECT_ID>"}}
```

以 `api_describe` 返回的 `input_schema`、令牌范围和认证要求为准。部分接口只接受控制台会话，访问令牌无法调用；文件下载等操作可能需要直接使用 HTTP 或 CLI。

整理任务时，可用 `task_list` 传入 `{"tag":"日报","limit":10}`。要更新标签，先用 `task_get` 读取当前标签，再用 `task_set_tags` 传入 `{"task_id":"<TASK_ID>","tags":["日报","CRM"]}`，保留仍需使用的标签。

建立任务时按这个顺序操作：先 `discover`，再用 `task_apply` 的 `dry_run` 预览并正式提交；需要凭证时用 `credential_request` 请用户填写；用 `run_start` 的 `test: true` 测试，并检查日志和业务结果；预览触发器后再设置，最后激活已测试的版本。修改任务时先读取 `latest_version.seq`，通过 `if_match` 提交，遇到版本冲突先合并。定时器要指定 IANA 时区。测试会真实执行代码，有副作用的任务先向用户确认。

## 配置 AI 能力

在 Web 控制台的「密钥 → 凭证 → AI 能力」填写服务商的 HTTPS API 基础地址、API Key 和模型 ID，每行填一个模型。基础地址保留服务商要求的版本路径，例如 `https://api.openai.com/v1`，Lerryn 会在后面加上 `/chat/completions`。

Agent 使用同一账号的访问令牌调用 `ai_models`，再从返回列表中选一个 ID 传给 `ai_chat`。消息会发送给所配置的服务商，调用可能产生费用。当前支持非流式文本生成。请求或结果含有当前账号已存凭证的值时，平台会拦截。

```json
{
  "model": "your-model-id",
  "messages": [{"role": "user", "content": "用一句话概括下文……"}]
}
```

API Key 加密保存在该账号的凭证库里。仅改模型列表时可以留空保留现有 Key，更改基础地址时要重新填写。内网 HTTPS 模型网关需要部署方配置 `ai.allowed_networks`。

## HTTP API 与 CLI

HTTP API 基地址为 `https://lerryn.example.com/api/v1`。下面的命令假设已在本地凭证环境中设置 `LERRYN_TOKEN`：

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $LERRYN_TOKEN" \
  https://lerryn.example.com/api/v1/discover
```

完整契约在仓库的 `api/bundled/openapi.yaml`。具体路径、参数和响应可通过 MCP 的 `api_describe` 查询，CLI 使用同一组 operationId：

```bash
lerryn discover
lerryn api listTools --describe
lerryn api listTools --project-id <PROJECT_ID>
lerryn tasks apply ./hello-lerryn --dry-run
lerryn tasks apply ./hello-lerryn
lerryn runs start hello-lerryn --test --wait 60s
```

`api_call` 和 CLI 的通用参数用 snake_case；CLI 选项将下划线改成连字符，例如 `project_id` 对应 `--project-id`。直接发 HTTP 请求时按 HTTP 契约填写路径、查询、请求头和请求体。

HTTP 错误响应中的 `code`、`errors`、`next_actions` 和 `request_id` 用于定位失败。`insufficient_scope` 表示令牌缺少范围；`not_found` 也可能表示当前身份无权查看；`validation_failed` 按错误位置修正；`version_conflict` 要重新读取版本并合并。`completed` 表示进程正常退出，业务是否成功还要检查 `result`。

## Agent Skill 与任务内置能力

`lerryn skills install` 安装二进制内嵌的四份 Skill：`lerryn-automation`、`lerryn-credentials`、`lerryn-troubleshoot` 和 `lerryn-operate`。默认目录是 `~/.claude/skills`，其他客户端可用 `--dir <目录>` 指定安装位置。MCP 客户端也可读取 `lerryn://skills/<名称>/SKILL.md` resource。根据 `discover.skills.version` 保持版本匹配。

任务容器提供 Python、Node 和 Shell 的 `sbx`，无需在任务依赖中安装，用于阶段、结果和外部 MCP 调用：

```bash
sbx stage start fetch
sbx stage done fetch
sbx result --status ok --summary "Done" --data-json '{"count": 1}'
```

Python 可用 `sbx.params()`、`with sbx.stage("fetch"):` 和 `sbx.result(...)`；Node 提供对应的辅助函数。产物写 `/sbx/out`，临时文件写 `/sbx/work` 或 `/tmp`，跨运行状态写 `/sbx/state`。状态按任务共享，测试运行也会提交状态；只有退出码为 0 的完成运行提交，并发以最后成功提交为准。

## 接入项目工具

项目工具是任务依赖的外部 HTTP API、MCP 服务或脚本文件包。维护者或所有者用 `registerTool` 登记同名工具时会更新它，任务清单在 `tools` 中引用名称；任务下次运行使用届时的登记内容。`removeTool` 会使仍引用该名称的任务无法通过校验。

例如通过 `api_call` 登记外部 MCP 服务：

```json
{
  "operation_id": "registerTool",
  "arguments": {
    "project_id": "<PROJECT_ID>",
    "name": "crm-mcp",
    "kind": "mcp",
    "description": "查询客户和商机",
    "url": "https://crm.example.com/mcp"
  }
}
```

任务清单引用它，并单独声明所需凭证：

```yaml
tools: [crm-mcp]
network: full
credentials:
  - name: CRM_TOKEN
```

通过 `credential_request` 请用户填写并绑定凭证。任务里调用：

```bash
sbx mcp call crm-mcp search_accounts --args '{"query":"acme"}' \
  -H "Authorization: Bearer $CRM_TOKEN"
```

这类调用写入运行的 `tool.call` 事件。HTTP 和 MCP 工具的地址通过 `LERRYN_TOOL_<名称>_URL` 注入，名称转大写、连字符换成下划线；全部引用放在 JSON 环境变量 `LERRYN_TOOLS` 中。HTTP 工具由任务自行发请求。外部调用需要 `network: full`，网络还须允许容器访问目标地址。

脚本工具使用 `kind: script` 和 `files` 登记，不带 `url`。文件只读挂载到 `/sbx/tools/<名称>`，目录由 `LERRYN_TOOL_<名称>_DIR` 提供。令牌和其他凭证通过任务的 `credentials` 注入，工具的地址、说明与文件中只保存非敏感内容。
