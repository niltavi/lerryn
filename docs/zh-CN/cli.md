# lerryn CLI

[English](../en/cli.md) · [文档目录](README.md)

`lerryn` 二进制既是服务端，也是命令行客户端。客户端命令供 Agent 在 shell 中使用，也供维护者使用。设计背景见[技术设计](agent-automation-sandbox-tech-design.md)第 10.6 节。

## 工作方式

CLI 没有手写的请求代码。它和 MCP 工具共用一份操作索引：二进制里嵌入的 OpenAPI 契约（`api/bundled/openapi.yaml`）在启动时加载成索引，每个操作的参数映射规则与 MCP 工具相同（技术设计第 10.3 节）。因此：

- 契约里新增的操作，不改 CLI 代码就能用 `lerryn api <operationId>` 调用。
- 参数名与 MCP 工具一致：路径、查询、请求头参数转为 snake_case，请求体的属性平铺在同一层。`api_describe` 返回的 `input_schema` 同样适用于 CLI。
- 权限检查只在服务端。CLI 不做范围判断，令牌范围不够时服务端返回 `insufficient_scope`。

常用流程另有几个友好命令：`login`、`logout`、`discover`、`tasks`、`runs`、`mcp`。它们也经同一份索引构造请求，只是参数和输出更顺手。`skills` 不访问服务端，只安装二进制里嵌入的 Skill。

CLI 的契约与服务端版本应当一致。CLI 比服务端旧时，新操作不可用；比服务端新时，调用服务端没有的操作会得到 `not_found`。`lerryn discover` 返回服务端的契约版本（`api.spec_version`）。

## 登录与配置

```bash
lerryn login https://lerryn.example.com --username alice          # 在终端输入密码，不回显
echo "$PASSWORD" | lerryn login https://lerryn.example.com -u alice --password-stdin
lerryn login https://lerryn.example.com --token-stdin < token.txt  # 保存控制台里创建的访问令牌
lerryn logout
```

用密码登录时，CLI 先登录拿到会话，用会话创建一个名为 `lerryn-cli on <主机名>` 的访问令牌（默认范围 `read`、`write`、`execute`，可用 `--scopes`、`--expires-in-days` 调整），然后立即登出会话。之后的请求只用这个访问令牌。`lerryn logout` 会先在服务端撤销这个令牌，再删除本地配置；用 `--token` 或 `--token-stdin` 保存的令牌可能还在别处使用，logout 只删除本地配置，不撤销。

配置文件保存服务端地址和访问令牌：

| 位置 | 说明 |
| --- | --- |
| `$LERRYN_CLI_CONFIG` | 设置时使用这个路径 |
| `$XDG_CONFIG_HOME/lerryn/cli.json` | 其次 |
| `~/.config/lerryn/cli.json` | 默认。Windows 上为 `%AppData%\lerryn\cli.json` |

文件以 0600 权限写入，目录为 0700，写入时先写临时文件再改名。其他用户能读写这个文件时，CLI 拒绝使用并提示 `chmod 600`，做法与 ssh 对私钥的处理相同。

服务端地址和令牌的来源，优先级从高到低：

| 项目 | 来源 |
| --- | --- |
| 服务端地址 | `--server`，`LERRYN_SERVER`，配置文件 |
| 访问令牌 | `LERRYN_TOKEN`，配置文件 |

配置文件里的令牌只发给它所属的服务端。用 `--server` 或 `LERRYN_SERVER` 指向别的地址时，CLI 不会带上这个令牌。在 CI 或 Agent 沙箱里，可以不登录，直接设置 `LERRYN_SERVER` 和 `LERRYN_TOKEN`。

服务端地址可以带路径前缀，例如反向代理下的 `https://example.com/lerryn`。地址是非本机的 `http://` 时，login 会提示密码和令牌以明文传输。

## 输出

每个客户端命令都接受 `-o, --output`：

| 取值 | 行为 |
| --- | --- |
| `auto` | 默认。标准输出是终端时用 `text`，否则用 `json` |
| `json` | 打印 API 返回的 JSON，原样不改字段，供 Agent 和脚本解析 |
| `text` | 打印给人看的摘要 |

Agent 在 shell 里调用时，标准输出通常不是终端，默认就是 JSON。

API 返回错误时，`json` 模式把 problem 文档（含 `code`、`next_actions`、`request_id`）打印到标准输出，`text` 模式把标题、详情、请求 ID 和下一步打印到标准错误。两种模式都会在标准错误打印一行 `error:`。

## 退出码

| 退出码 | 含义 |
| --- | --- |
| 0 | 成功 |
| 1 | 本地错误：配置文件、任务文件、连不上服务端 |
| 2 | 命令行用法错误：参数缺失、取值类型不对、未知的操作或参数 |
| 3 | 没有登录，或服务端拒绝了凭证（HTTP 401） |
| 4 | API 返回了其他错误，例如 `not_found`、`validation_failed`、`insufficient_scope` |
| 5 | 等待的运行已结束，但没有成功 |
| 6 | 等待时限已到，未确认运行结束 |

5 和 6 只出现在 `runs wait` 和带 `--wait` 的 `runs start`。"成功"指状态为 `completed`，并且任务没有用 `sbx result` 上报 `failed`；上报 `partial` 算成功，需要区分时读输出里的 `result`。

## 任务

```bash
lerryn tasks apply ./weekday-digest
lerryn tasks apply ./weekday-digest --dry-run --expected-version 3
lerryn tasks apply ./weekday-digest --expected-version 3
lerryn tasks get <task-id>
lerryn tasks export <task> --file task.json
lerryn tasks import task.json
```

任务目录里放一个清单文件 `lerryn.yaml`（也接受 `lerryn.yml`，或用 `-f` 指定），内容是任务清单的字段（技术设计第 5.3 节），另加一个可选的 `name`。`name` 省略时取目录名，`--name` 优先于两者。

```yaml
name: weekday-digest
description: 工作日早上汇总
runtime: {language: python, version: "3.12"}
entrypoint: [python, main.py]
workflow:
  - title: 生成线索摘要
    description: 读取内部 CRM 的新线索，汇总并保存摘要。
params: {source: internal-crm}
```

`workflow` 必填。按执行顺序为每步填写 `title` 和 `description`，Web 任务详情据此显示流程图。

目录里的其他文件按 UTF-8 文本上传，保留可执行位。以下内容不上传：清单文件本身，以 `.` 开头的文件和目录（例如 `.git`、`.venv`、`.env`），`__pycache__` 和 `node_modules`。遇到符号链接或非 UTF-8 文件时直接报错，不会读取目录之外的内容。

项目默认是当前用户的个人项目（`discover` 返回的 `principal.personal_project_id`），可以用 `--project` 指定。

修改已有任务时必须带 `--expected-version`，值为读到的最新版本号（`tasks get` 输出的 `latest_version.seq`），CLI 把它放进 `If-Match`。不带时服务端返回 `precondition_required`；期间有别的会话提交了新版本时返回 `version_conflict` 和当前版本号 `current_version`，这时先看对方改了什么（`lerryn api diffTaskVersion --task-id <id> --seq <版本>`），合并后再提交，不要直接覆盖。新建任务不需要这个参数。

`--dry-run` 只校验并返回预览：将会生成的版本号、与激活版本（没有时为上一个版本）的差异和警告，不保存任何内容。`text` 模式下列出有变化的文件和增删行数。

### 导出和导入单个任务

```bash
lerryn tasks export weekday-digest --history --file digest.json
lerryn tasks export <task-id> --version 3 > digest.json
lerryn tasks import digest.json --dry-run
lerryn tasks import digest.json --project <project-id> --name digest-copy
```

`export` 调用 `exportTask`，把任务写成一个 JSON 任务包：任务名、最新版本（或 `--version` 指定的版本）的清单和全部文件，加 `--history` 时连同之前的版本，以及触发器定义（`--no-triggers` 不带）。任务包不含运行记录、凭证的值和绑定、webhook 令牌，也不含项目、用户、任务的 ID，所以可以导入到另一个部署。不带 `--file` 时任务包写到标准输出，与 `-o` 无关；带 `--file` 时以 0600 写入文件，输出一段摘要。任务包最多约 4 MiB，`--history` 最多带最近的 50 个版本，超出时从最早的版本开始省略，摘要和任务包的 `omitted_versions` 给出省略的数量。能查看任务的成员都可以导出，包括查看者。

`import` 调用 `importTask`，在个人项目（或 `--project`）里建立一个新任务，`-` 表示从标准输入读取。导入的人成为任务和每个版本的创建者，需要项目的维护者或所有者角色。导入后：

- 版本从 1 重新编号，输出的 `versions` 给出新旧版本号的对应。
- 版本都没有测试过，任务未激活。先 `lerryn runs start <task> --test`，确认结果后再激活。
- 清单里的凭证仍是引用名，都没有绑定（`credentials` 中为 `missing`），由用户经填写请求提供，或由凭证所有者授权。
- webhook 触发器生成新的令牌，只在这次输出中出现。
- 项目里已有同名任务时返回 `version_conflict`，用 `--name` 换一个名字。
- 校验规则与 `tasks apply` 相同，包括文件数量和大小，以及文件、清单和触发器参数中不能出现调用者凭证的值。

整个部署的迁移不用这两个命令，用维护命令 `lerryn export` 和 `lerryn import`（[部署文档](deploy.md)第 7 节）。

## 运行

```bash
lerryn runs start <task> --test --param who=world --wait 60s
lerryn runs start <task> --params-json @params.json --idempotency-key deploy-42
lerryn runs get <run-id> [--wait 30s]
lerryn runs wait <run-id> --timeout 10m
lerryn runs logs <run-id> --tail 200
lerryn runs logs <run-id> --follow
lerryn runs cancel <run-id>
```

- `<task>` 可以是任务 ID，也可以是任务名。任务名经 `listTasks` 的 `name` 筛选精确查找；不同项目里有同名任务时报错，改用任务 ID。
- `--param name=value` 的值是合法 JSON 时按 JSON 解析（`n=3` 是数字，`ok=true` 是布尔值），否则当作字符串。给出参数时整体替换清单中的 `params`。
- `runs wait` 反复调用 `getRun` 的服务端等待（每次最多 60 秒），直到运行结束或超时。`--timeout` 同时约束连接、响应头和响应体的等待；超时时打印最近一次成功读取的运行，没有读取成功时不输出运行数据。
- `runs logs` 在 `json` 模式下打印 API 的一页日志，带 `next_cursor`，可以用 `--cursor` 续读。`--follow` 会一直读到运行结束并读完最后一行，`json` 模式下每行输出一个日志行对象。

## 通用的 api 命令

```bash
lerryn api                                   # 列出全部操作
lerryn api getTask --describe                # 说明、令牌范围、输入和响应 schema
lerryn api getTask --task-id <id>
lerryn api getRun --arg run_id=<id> --wait 10
lerryn api applyTask --input request.json    # 参数从 JSON 对象读取，- 表示标准输入
```

每个操作是 `lerryn api` 的一个子命令，名称就是 operationId。参数按 schema 类型生成选项，下划线写作连字符，例如 `run_id` 对应 `--run-id`：

- 字符串、整数、数字：`--name value`，整数和数字在发送前检查格式。
- 布尔值：`--flag` 或 `--flag=false`。
- 标量数组：选项可重复，例如 `--tag a --tag b`。
- 对象和其他复合类型：值写 JSON。

参数也可以用 `--arg name=value`（值按 JSON 解析，失败时当作字符串）或 `--input` 给出。三者同时出现时，类型化的选项优先，其次是 `--arg`，最后是 `--input`。参数名与 `--server`、`--output` 等 CLI 自身的选项重名时，只能用 `--arg` 或 `--input` 传。

响应体原样打印。只能在控制台会话中调用的操作（例如 `createToken`），用访问令牌调用会得到 `insufficient_scope`；`--describe` 中的 `session_only` 和 `callable` 会说明这一点。

## Skill

```bash
lerryn skills list
lerryn skills install                    # Claude Code 的用户目录 ~/.claude/skills
lerryn skills install --project          # 当前目录的 .claude/skills
lerryn skills install --dir <目录> --skill lerryn-automation
```

二进制里嵌入了与它的契约匹配的四个 Skill：`lerryn-automation`、`lerryn-credentials`、`lerryn-troubleshoot`、`lerryn-operate`（源文件在仓库的 `skills/`）。`install` 每个 Skill 写一个子目录，并留下 `.lerryn-skill` 标记记录版本。再次安装时替换这些目录，旧版本里删掉的文件不会残留，目录里的其他内容不动。同名目录不是 `install` 写的（没有标记）时拒绝覆盖并以 1 退出，确认要替换时加 `--force`。其他读取 Agent Skills 的客户端用 `--dir` 指定它的 skills 目录。

升级 CLI 或服务端之后重新运行一次 `install`，让 Skill 与服务端的契约版本一致。

## MCP stdio 代理

```bash
lerryn mcp --stdio
claude mcp add lerryn -- lerryn mcp --stdio
```

供只支持本地 stdio MCP 服务器的客户端使用。工具清单与服务端 `/mcp` 相同：契约中暴露的工具，加上 `api_describe` 和 `api_call`。工具由同一段代码（`mcp.NewServer`）注册，区别只在转发：`/mcp` 在进程内转给 API 处理链，stdio 代理通过 HTTP 发给服务端，带上 CLI 自己的访问令牌。

服务端响应读取中断或调用取消时，工具返回错误，已收到的部分内容不会作为成功结果交给 Agent。完整响应超出工具结果长度上限时，仍按既有规则标记截断，并提示缩小请求范围。

- 标准输出只承载 MCP 协议，诊断信息写到标准错误。
- 没有可用的令牌时直接退出（退出码 3），不会启动一个所有调用都失败的服务器。
- 连不上服务端时，工具返回 `internal_error` 的工具错误，`next_actions` 建议确认地址后重试。
- 服务端看到的是普通的 REST 请求，User-Agent 为 `lerryn-cli/<版本>`。审计中区分 MCP 入口的 `ToolCall` 只在服务端进程内的 `/mcp` 调用里存在。

能连接远程 MCP 的客户端，直接配置服务端的 `/mcp` 端点，不需要这个代理。
