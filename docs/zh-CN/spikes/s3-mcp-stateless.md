# S3 验证：MCP 无状态模式与进程内转发

日期：2026年11月2日　工作流：W6　对应：[实施规划](../agent-automation-sandbox-implementation-plan.md)第 5 节 S3，[技术设计](../agent-automation-sandbox-tech-design.md)第 9.2、9.3、10 节

> 合并后的变化：本文中的 `x-mcp.name` 已统一为 `api/README.md` 中的 `x-mcp.tool`，并支持 `title` 和 `hints`；工具标注按 README 的表格推导。`/mcp` 前已加访问令牌校验。本文保留验证当时的记录，现行规则以 `api/README.md` 和技术设计第 10 节为准。

## 1 结论

**通过。** 不需要启用退路（有状态模式加会话粘滞）。

| S3 通过标准 | 结果 |
| --- | --- |
| 用 go-sdk 的无状态模式，由 `x-mcp` 生成 2 个工具 | 通过。go-sdk v1.8.0，`StreamableHTTPOptions.Stateless = true`；测试规范里的 `getRun`、`startRun` 注册为 `run_get`、`run_start`，没有 `x-mcp` 的 `login` 不暴露 |
| 经同一条 HTTP 处理链转发 | 通过。工具调用在进程内构造 `*http.Request`，交给一个 `http.Handler`。测试里这个 handler 是桩，正式实现把 `/api/v1` 的处理链（含中间件）传进去即可，接口不变 |
| Claude Code 能连接并调用 | 通过。Claude Code 2.1.273，协议 2026-07-28 |
| Codex 能连接并调用 | 通过。Codex 0.154.0，协议 2025-06-18 |
| 旧协议客户端的行为记录在案 | 已记录，见第 5 节。旧协议在无状态模式下同样不需要会话粘滞 |

两个真实客户端走的是两代协议：Claude Code 已经用 2026-07-28（`server/discover`，无握手），Codex 还在用 2025-06-18（`initialize` 握手）。两者都要支持，go-sdk 的无状态模式能同时覆盖。

## 2 做了什么

代码在 `server/internal/mcp/`：

| 文件 | 内容 |
| --- | --- |
| `registry.go` | 读取 OpenAPI 3.1 规范，按 `x-mcp` 构建工具清单 |
| `schema.go` | 把参数和请求体的 schema 转成自包含的 JSON Schema |
| `annotations.go` | 由 `x-operation-kind` 和 HTTP 方法推出工具提示 |
| `forward.go` | 工具参数转 HTTP 请求；响应转工具结果；截断 |
| `handler.go` | `NewHandler`：go-sdk Server 加无状态的 Streamable HTTP handler |
| `testdata/openapi.yaml` | 测试规范（不是正式契约） |
| `testdata/tools.golden.json` | 由测试规范得到的工具清单快照 |
| `*_test.go` | 单元测试、go-sdk 客户端端到端测试、真实客户端测试（默认跳过） |

调用路径：

```
Agent ──POST /mcp──► StreamableHTTPHandler（Stateless）
                         │ 每个请求一个临时会话
                         ▼
                     工具 handler ── 参数 → 路径/查询/请求头/请求体
                         │           透传 Authorization
                         │           context 标记 ToolCall{tool, operationId}
                         ▼
                     api http.Handler（与 REST 相同的路由和中间件）
                         │
                         ▼
                     有界的 ResponseWriter ── 2xx JSON → structuredContent
                                             problem+json → isError，保留 code、next_actions
                                             超过上限 → 截断
```

## 3 工具注册

### 3.1 `x-mcp` 的形状

```yaml
x-operation-kind: read            # read | mutate | execute，每个操作都必须有
x-mcp:
  expose: true                    # 是否注册为独立工具
  name: run_get                   # snake_case，全局唯一
  description: >-                 # 写给 Agent 的说明，expose 为 true 时必填
    Read one run: status, current phase, exit code and the console link. ...
```

`x-mcp` 中出现未知字段直接报错，防止拼写错误被静默忽略。工具标题取操作的 `summary`。

### 3.2 输入 schema

| 来源 | 规则 |
| --- | --- |
| 路径、查询、请求头参数 | 成为顶层参数，名称统一转 snake_case：`runId` → `run_id`，`Idempotency-Key` → `idempotency_key`。路径参数一律必填 |
| `Authorization`、`Accept`、`Content-Type` 请求头参数 | 忽略（OpenAPI 规定如此，转发时由 MCP 层设置） |
| Cookie 参数 | 不支持，加载时报错 |
| `application/json` 请求体，普通对象 | 属性平铺为顶层参数，名称不变；请求体必填时，其 `required` 进入顶层 `required` |
| 请求体是数组、组合 schema（`oneOf` 等）或没有属性的对象 | 整体作为一个 `body` 参数 |
| 参数与请求体属性重名 | 加载时报错 |
| `$ref` | 内联展开。部分客户端不解析 tool schema 里的 `$ref` 和 `$defs`。递归 schema 无法内联，加载时报错 |
| `readOnly: true` 的属性 | 从输入中去掉（例如服务端生成的 `id`） |
| `discriminator`、`xml`、`externalDocs`、`example`、`x-*` | 去掉，它们对 MCP 客户端没有意义。3.1 的 `examples`、`const`、类型数组（`[string, "null"]`）原样保留 |
| 顶层 | `type: object`，`additionalProperties: false` |

不输出 `outputSchema`：它会增加 `tools/list` 的体积，而且按规范 `structuredContent` 要符合它，problem 结果显然不符合。

### 3.3 工具提示

| 类型 | 方法 | readOnly | destructive | idempotent | openWorld |
| --- | --- | --- | --- | --- | --- |
| read | GET，或 POST（如 `validateTaskVersion`） | true | false | true | false |
| mutate | POST | false | false | false | false |
| mutate | PUT、DELETE | false | true | true | false |
| mutate | PATCH | false | true | false | false |
| execute | POST | false | true | false | true |
| execute | PUT、DELETE | false | true | true | true |

原则是拿不准时偏向让客户端更谨慎。execute 标为 openWorld，因为运行的是用户代码，可能访问外部系统。`read` 配 PUT、PATCH、DELETE，或 `mutate`、`execute` 配 GET，视为契约错误，加载时报错。

已知的偏差：`applyTask` 是 PUT，按规则会标为 destructive，但它实际只新增版本。见第 7 节建议 4。

### 3.4 解析库：kin-openapi

选 kin-openapi v0.152.0，不选 libopenapi v0.42.3。

| 考虑 | kin-openapi | libopenapi |
| --- | --- | --- |
| 与 S2 生成链路的关系 | oapi-codegen v2.8.2 本身就基于 kin-openapi（要求 v0.142.0）。MCP 注册表和生成的服务端用同一个库理解规范，参数合并、引用解析的行为一致；go.mod 里也只有一个 OpenAPI 库 | 链路里会出现第二个解析库，两边对规范的理解可能有细微差异 |
| OpenAPI 3.1 | 支持类型数组、`const`、`examples`、`$defs`、`webhooks`、`jsonSchemaDialect`、`license.identifier`；测试规范加载并通过 `Validate` | 支持完整，保真度更高 |
| 版本冲突 | 已验证 oapi-codegen v2.8.2 能在 kin-openapi v0.152.0 下编译（同一 module 内 MVS 会选 v0.152.0） | 不涉及 |
| 依赖体积 | 模块约 10 MB，依赖 jsonpointer、oasdiff/yaml、santhosh-tekuri/jsonschema | 模块约 29 MB，自带 yaml 分支、jsonpath、有序 map 等 |
| libopenapi 的长处 | | 保留 YAML 节点、行号和键顺序，自带 `RenderInline`、差异比较、打包。这些在 Lerryn 由 Redocly、oasdiff 承担，MCP 层用不上 |

kin-openapi 的一个缺点：YAML 键顺序在解析后丢失，工具参数在 JSON 里按字母序排列。对 Agent 影响很小。

### 3.5 运行时构建还是代码生成

这次用的是运行时构建：启动时读规范，构建注册表。**建议正式实现也用运行时构建**，理由：

- `api_describe` 要按 operationId 返回 schema，`api_call` 要调用任意公开接口，运行时本来就需要一份完整的操作索引。12 个主工具和这两个通用工具走同一份代码，不必维护两条路径。
- 工具是数据，不是类型化的 handler。转发逻辑是通用的，代码生成换不来额外的编译期检查。
- 生成代码的另一个好处是改动在 diff 里可见。这一点由 `testdata/tools.golden.json` 承担：工具名、说明、输入 schema、提示有任何变化，`go test` 都会失败，需要 `-update` 并在评审里看到 golden 文件的差异。正式实现时对真实规范做同样的 golden 测试。
- 契约错误同样在 CI 里暴露：加载真实规范的测试失败，CI 就失败；服务启动时加载失败也会直接退出。
- 开销很小：62 个工具的规范构建一次约 7.5 ms（Apple M5 Pro，`BenchmarkLoad`）。

## 4 调用与结果转换

### 4.1 请求构造

- 路径参数用 `url.PathEscape` 转义；`""`、`.`、`..` 直接拒绝，它们会被路由解析成别的路径。
- 查询参数支持标量和标量数组（重复键，即 OpenAPI 默认的 form + explode）。
- 请求头参数拒绝换行。
- 只透传 `Authorization`。客户端带来的 `Cookie`、`Mcp-Session-Id` 等不进入 API 请求。
- 请求按服务端请求的约定构造：`Body` 不为 nil（GET 用 `http.NoBody`），`RequestURI` 已设置。最初没有这样做，桩 API 读 `r.Body` 时空指针崩溃；`net/http` 保证服务端请求的 Body 非空，处理函数有权依赖这一点。
- 未知参数、缺少路径参数、类型不对，直接返回 `validation_failed` 工具错误，不调用 API。其余校验交给 API，MCP 和 REST 返回同样的校验错误。
- API handler 的 panic 被捕获并转为 MCP 错误。go-sdk 在 HTTP 服务器 goroutine 之外执行工具 handler，`net/http` 的按连接恢复覆盖不到，不捕获的话整个进程会退出。
- 进程内请求的 context 里带 `ToolCall{Tool, OperationID}`，用 `mcp.ToolCallFromContext` 读取。它只能在进程内设置，不能像请求头那样被客户端伪造，审计可以用它记录入口。

已验证外层 `/mcp` 请求 context 中的值会传到进程内的 API 请求（四个协议版本都是如此，`TestOuterContextReachesAPI` 固定了这一点）。这意味着 `/mcp` 前的中间件可以把请求 ID、客户端地址、已认证的身份放进 context，API 链路能直接读到。

### 4.2 响应转换

| API 响应 | 工具结果 |
| --- | --- |
| 2xx，JSON 对象 | `structuredContent` 为该对象；`content` 是同一段 JSON 文本 |
| 2xx，JSON 数组或标量 | 只有文本。旧协议要求 `structuredContent` 是对象 |
| `application/problem+json`（任何状态码） | `isError: true`；`structuredContent` 是 problem 对象；文本是 problem 的 JSON，所以只把文本交给模型的客户端也能看到 `code` 和 `next_actions` |
| 其他非 2xx | `isError: true`，文本为 `HTTP <状态码>: <正文>` |
| 空正文（如 204） | 文本说明状态码 |
| 非 UTF-8 的二进制 | 不返回内容，提示用 HTTP API 下载 |

每个结果的 `_meta` 带 `lerryn/http_status`；截断时另带 `lerryn/truncated` 和 `lerryn/total_bytes`。

### 4.3 截断

- 默认上限 32 KiB（`Options.MaxResultBytes`）。Claude Code 对 MCP 输出有 token 上限，大约 10k token 起给出警告，32 KiB 的 JSON 在这个量级以内。注意文本和 `structuredContent` 各有一份，实际传输约为两倍。
- 有界的 ResponseWriter 只缓存上限内的字节，其余只计数，大响应不会占满内存。
- 截断时去掉 `structuredContent`（截断的 JSON 无法解析），文本按字符边界切开，末尾说明已显示和总字节数，并提示用 `limit`、`cursor` 缩小请求。
- MCP 层给不出通用的续读游标：它不知道任意 JSON 应该从哪里接着读，重新调用也不一定幂等。见第 7 节建议 2。

## 5 协议版本与无状态行为

### 5.1 go-sdk 客户端测试

`TestEndpoint` 用 go-sdk v1.8.0 客户端，对 4 个协议版本 × 2 种响应格式（SSE、`application/json`）各跑一遍。每个请求轮流发给两个独立的 handler 实例，模拟没有会话粘滞的两个副本。覆盖：列出工具、调用成功、problem 错误（`not_found`、`credential_missing` 的 `code` 和 `next_actions`）、参数转发（路径、查询、请求头、请求体）、鉴权头透传（有令牌和无令牌各一次）、截断、参数错误不触达 API。全部通过，所有响应都没有 `Mcp-Session-Id`。

记录到的请求序列：

| 协议 | 请求序列 |
| --- | --- |
| 2026-07-28 | `POST server/discover` → 200；`POST tools/list` → 200；`POST tools/call` → 200 …… 没有 GET、DELETE |
| 2025-11-25、2025-06-18、2025-03-26 | `POST initialize`（无 `Mcp-Protocol-Version` 头）→ 200；`GET`（打开独立 SSE 流）→ 405；`POST notifications/initialized` → 202；`POST tools/list` → 200；`POST tools/call` → 200 …… |

### 5.2 旧协议在无状态模式下的行为

- `initialize` 由收到它的副本回答，响应里没有 `Mcp-Session-Id`，客户端之后也不会带。
- 之后的每个请求都可能落到任何副本。SDK 为每个请求合成默认的初始化状态，所以没见过握手的副本也能处理 `tools/call`。`TestLegacyClientWithoutInitialize` 直接发一个 2025-06-18 的 `tools/call`，没有任何握手，结果正常。
- 客户端打开独立 SSE 流的 GET 得到 405（带 `Allow: POST`）。go-sdk 客户端容忍这一点；Codex 根本不发这个 GET。
- 没有会话 ID，客户端也就不发 DELETE；即使发了，也是 405。
- 失去的能力：服务端主动向客户端发请求（sampling、elicitation、roots）会立即失败；`list_changed` 通知没有通道送达；调用期间的进度和日志通知只能随该请求的 SSE 流返回，`JSONResponse` 模式下没有。Lerryn 的工具清单随服务端版本固定，不依赖这些能力。
- go-sdk 曾有兼容开关 `allowsessionsinstateless`，让无状态模式继续读写 `Mcp-Session-Id`。它将在 SDK 1.9.0 删除，本实现没有使用。

结论：技术设计 10.4 节"旧协议需要会话时，负载均衡按 `Mcp-Session-Id` 做粘滞"的预案用不上。

### 5.3 本地回环保护

go-sdk 默认开启 DNS rebinding 保护：请求到达回环地址、而 `Host` 不是回环地址时，返回 403。同一台机器上的反向代理转发到 `127.0.0.1` 并保留公网 `Host`，正好触发这一条（`TestLocalhostProtection`）。`Options.DisableLocalhostProtection` 可以关闭它。Lerryn 的每次工具调用都需要访问令牌，DNS rebinding 拿不到令牌，关闭是安全的。

### 5.4 其他观察

- Claude Code 在列出工具后会发 `subscriptions/listen`，这是一个挂起的 POST，直到客户端退出才结束。反向代理需要对 `/mcp` 关闭 SSE 缓冲并放宽读超时；被周期性断开也无害，因为 Lerryn 不发 `list_changed`。
- `PropagateRequestCancellation` 已打开。按 SDK 文档，2026-07-28 的请求在客户端断开时会取消工具 handler 的 context，进而取消进程内的 API 请求。这一点没有单独测试。
- go-sdk 默认的 `/mcp` 请求体上限是 4 MiB。`task_apply` 带文件时可能不够，应与 API 的上限保持一致。

## 6 客户端兼容矩阵

| 客户端 | 版本 | 协议 | 建立方式 | 列出工具 | 调用成功 | problem 错误可见 | 鉴权头透传 | SSE | JSON 响应 | 状态 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Claude Code（`claude -p`） | 2.1.273 | 2026-07-28 | `server/discover` | 是 | 是 | 是 | 是 | 是 | 是 | 已验证 |
| Codex（`codex exec`） | 0.154.0 | 2025-06-18 | `initialize` | 是 | 是 | 是 | 是（`bearer_token_env_var`） | 是 | 是 | 已验证 |
| go-sdk 客户端 | v1.8.0 | 2026-07-28 | `server/discover` | 是 | 是 | 是 | 是 | 是 | 是 | 已验证（单元测试） |
| go-sdk 客户端 | v1.8.0 | 2025-11-25、2025-06-18、2025-03-26 | `initialize` | 是 | 是 | 是 | 是 | 是 | 是 | 已验证（单元测试） |
| Cursor、Claude Desktop、VS Code | | | | | | | | | | 未验证 |
| 经真实负载均衡器的多副本部署 | | | | | | | | | | 未验证（只在进程内用两个 handler 模拟） |
| 只支持 OAuth 的客户端 | | | | | | | | | | 不在首版范围（P1） |

"problem 错误可见"的判定：让模型调用 `run_get` 读取一个返回 404 problem 的运行，模型在回答里正确复述了 `code`（`not_found`）和第一个 `next_actions` 的 `action`（`run_list`）。成功调用的判定：模型复述了只出现在成功结果里的 `task_id`。两项都通过，桩 API 收到的每个请求都带着配置的令牌。

Codex 的输出里有一条 `rmcp::transport::worker` 的 403 错误，内容是一张与 Lerryn 无关的网页。Lerryn 端点的请求记录里没有 403，这条错误来自 Codex 自己的其他连接，与本验证无关；复现时看到它不必处理。

### 6.1 复现

真实客户端测试默认跳过，需要显式打开。它在随机回环端口上启动端点（桩 API），写一份临时 MCP 配置，用完关闭，不修改用户的全局配置：

```bash
LERRYN_MCP_REAL_CLIENTS=claude,codex go test -run TestRealClient -v ./server/internal/mcp/
# 端点改为 application/json 响应
LERRYN_MCP_JSON_RESPONSE=1 LERRYN_MCP_REAL_CLIENTS=claude,codex go test -run TestRealClient -v ./server/internal/mcp/
```

用到的客户端参数：

- Claude Code：`claude -p <提示> --mcp-config <临时文件> --strict-mcp-config --allowedTools mcp__lerryn__run_get --no-session-persistence --model haiku`。临时文件内容为 `{"mcpServers":{"lerryn":{"type":"http","url":"<端点>","headers":{"Authorization":"Bearer <令牌>"}}}}`。
- Codex：`codex exec --ignore-user-config --ephemeral --skip-git-repo-check --sandbox read-only -c 'mcp_servers.lerryn.url="<端点>"' -c 'mcp_servers.lerryn.bearer_token_env_var="LERRYN_TEST_TOKEN"' -c 'approval_policy="never"' <提示>`，令牌放在环境变量 `LERRYN_TEST_TOKEN` 中。

### 6.2 维护者手动验证未验证的部分

接入真实服务后（`/mcp` 挂到 `server/internal/app`，见第 8 节），对未验证的客户端按以下步骤做一次：

1. 启动 `lerryn server`，在控制台创建一个访问令牌。
2. 在客户端里添加 HTTP MCP 服务器，地址 `https://<主机>/mcp`，请求头 `Authorization: Bearer <令牌>`。
3. 让 Agent 调用 `discover`，再读取一个不存在的运行。
4. 确认：工具列表出现；成功调用返回结构化内容；错误调用里 Agent 能说出 `code` 和 `next_actions`；服务端审计记录里入口为 MCP、工具名正确。
5. 经反向代理访问时，确认没有 403（本地回环保护）；Claude Code 的 `subscriptions/listen` 被代理断开后客户端能继续使用。

## 7 对技术设计的修改建议

只是建议，技术设计本身没有改动。

1. **10.3 生成方式。** "生成器读取规范，生成工具注册代码"改为"启动时从内嵌的打包规范构建工具注册表，以工具清单的 golden 测试代替生成代码"。理由见 3.5 节。
2. **10.3 续读游标。** "超出时截断并给出续读游标"改为"超出上限时截断，作为兜底；分页由接口自己的 `limit`、`cursor` 负责。暴露为工具的列表和日志接口，单页大小要能放进上限（默认 32 KiB）"。`run_logs` 对应的 `readRunLogs` 尤其需要按字节限制单页大小。
3. **10.3 补充映射规则。** 把 3.2 节的输入 schema 规则和 3.3 节的提示推导表写进技术设计，或写进 `api/` 的约定文档，并用 Spectral 规则落实：暴露的操作必须有 `x-mcp.name`（snake_case、唯一）和 `x-mcp.description`；请求体用普通对象；参数转 snake_case 后不与请求体属性重名；不用 Cookie 参数；输入不含递归 schema。
4. **10.3 提示覆盖。** 规则推出的提示对 `applyTask` 偏保守（PUT，标为 destructive，实际只新增版本）。两种处理：接受保守标注；或在 `x-mcp` 中允许可选的 `destructive: false` 覆盖。倾向前者，等真实使用中出现不必要的确认再加。
5. **10.4 删除粘滞预案。** "如果旧协议需要会话，负载均衡按 `Mcp-Session-Id` 做粘滞"改为"旧协议客户端在无状态模式下同样不需要粘滞，代价是没有服务端主动请求和 `list_changed` 通知，Lerryn 不依赖这些"。并注明当前客户端的协议分布：Claude Code 已用 2026-07-28，Codex 仍用 2025-06-18。
6. **10.4 或 15 节补充部署要求。** 同机反向代理转发到回环地址时关闭 SDK 的本地回环保护（新增配置项，例如 `mcp.disable_localhost_protection`）；`/mcp` 请求体上限与 API 一致；反向代理对 `/mcp` 关闭缓冲、放宽读超时；`JSONResponse` 作为可选配置，两个客户端都兼容，但会失去调用期间的进度通知。
7. **10.5 令牌前置校验。** 在 `/mcp` 前加令牌校验中间件：缺失或无效时直接返回 HTTP 401 和 `WWW-Authenticate`，而不是让每次工具调用各自得到 401 problem。这样客户端能把服务器标为"需要认证"，`tools/list` 也受到保护。校验得到的身份可以放进 context（4.1 节已验证会传到 API 请求），由 W5 决定 API 链路是否复用它、避免重复校验。
8. **12.7 审计入口。** 审计用 `mcp.ToolCallFromContext` 区分 API 和 MCP 入口并记录工具名，不依赖请求头。
9. **9 节处理函数约束。** 进程内请求没有 `RemoteAddr`、`Host`、`TLS`。处理函数不能依赖它们：控制台链接用配置的公开地址生成；按客户端地址限流、记录来源时，从中间件放进 context 的值读取。
10. **2 节技术栈。** MCP 一行写明 go-sdk v1.8.0；新增"OpenAPI 解析：kin-openapi，与 oapi-codegen 使用同一个库"。
11. **`x-mcp.description` 的语言。** 9.5 节要求操作带中文说明；`x-mcp.description` 是另一份面向 Agent 的说明。测试规范用了英文，与工具名、参数名一致，也更省 token。需要定一个约定。

## 8 未完成与对其他工作流的影响

未完成：

- `/mcp` 没有挂到 `server/internal/app`，那是 W0 的目录。挂载方式：`mux.Handle("/mcp", mcp.NewHandler(reg, apiHandler, opts))`，其中 `apiHandler` 是 `/api/v1` 的完整中间件链（鉴权、请求 ID、审计、恢复），不能是包含 `/mcp` 自身的根路由。
- 真实的 API 链路（oapi-codegen strict server 和鉴权中间件）还不存在，测试用的是桩。接入后要对真实规范跑 golden 测试和端到端测试。
- `api_describe`、`api_call` 按实施规划属于 M2，没有做。
- 续读游标没有做，建议按第 7 节建议 2 修改设计。
- 第 6 节标为"未验证"的客户端和部署形态。

对其他工作流的影响：

- **W1（契约）**：规范保持 3.1；每个操作都要有 `x-operation-kind`；`x-mcp` 采用 3.1 节的形状；提供一个可 `go:embed` 的打包规范（例如 `api/` 下的 Go 文件导出打包后的字节），打包结果作为生成物提交；按第 7 节建议 3 增加 Spectral 规则。oapi-codegen v2.8.2 与 kin-openapi v0.152.0 已验证可以一起编译。
- **W5（账号与审计）**：`/mcp` 前的令牌校验中间件、context 中的身份、审计用 `ToolCallFromContext`，见第 7 节建议 7、8。
- **W0、W8（工程基础、部署）**：挂载 `/mcp`；新增配置项 `max_result_bytes`、`json_response`、`disable_localhost_protection`；反向代理配置见第 7 节建议 6。
- **go.mod**：新增 `github.com/modelcontextprotocol/go-sdk v1.8.0`、`github.com/getkin/kin-openapi v0.152.0`，以及它们的间接依赖；`golang.org/x/text` 从 v0.14.0（kin-openapi 依赖链引入）升到 v0.41.0，消除 govulncheck 报告的两个模块级漏洞（GO-2026-6629、GO-2026-5970，代码不可达）。按 AGENTS.md，新增依赖需要人工审查。两个库的许可证：go-sdk 为 MIT 向 Apache-2.0 过渡，kin-openapi 为 MIT，都与 AGPL-3.0 兼容；没有复制第三方代码，不需要更新 `THIRD_PARTY_NOTICES`。
