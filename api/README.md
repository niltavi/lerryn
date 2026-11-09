# api：接口契约

这里是 Lerryn 公开 HTTP API 的唯一来源。Go 服务端接口、TS 客户端、MCP 工具注册表、Skill 参考资料和接口文档页都由这份 OpenAPI 3.1 规范生成（技术设计第 9.1 节）。改接口先改这里，再运行 `make generate`。

`api/` 下的改动需要维护者人工审查（`AGENTS.md`）。

## 目录

| 路径 | 内容 |
| --- | --- |
| `openapi/openapi.yaml` | 根文件：info、servers、全局 security、tags、路径登记、所有具名组件的登记 |
| `openapi/common/` | 各资源共用的 schema、参数和响应：Problem、ErrorCode、CursorPage、Links 等 |
| `openapi/<资源>/paths.yaml` | 某个资源的路径项，例如 `discover/paths.yaml` |
| `openapi/<资源>/schemas.yaml` | 该资源专属的 schema |
| `bundled/openapi.yaml` | Redocly 打包后的单文件。生成物，不要手改 |
| `.spectral.yaml`、`spectral-functions/` | Spectral 规则 |
| `redocly.yaml` | Redocly 配置，只用于打包 |
| `spikes/s2/` | S2 技术验证用的小规范和检查代码，不是正式契约，见 `docs/zh-CN/spikes/s2-openapi-codegen.md` |

runner 与 server 之间的内部协议不在这份契约里，见 `server/internal/runproto`。

## 工作流程

```bash
# 1. 修改 api/openapi/ 下的文件
make generate    # 打包 → go generate（oapi-codegen）→ openapi-typescript
make api-lint    # Spectral 检查打包结果
make check       # 合并前的全部检查，包含 api-lint
```

`make generate` 依次做三件事：

1. `make api-bundle`：用 Redocly CLI 把 `openapi/` 打包成 `bundled/openapi.yaml`，组件名冲突时直接失败。
2. `go generate ./...`：`server/internal/httpapi/generate.go` 用 oapi-codegen 从打包结果生成 strict server（`api.gen.go`）。oapi-codegen 通过 `go.mod` 的 `tool` 指令固定版本。
3. `pnpm --filter @lerryn/api generate`：用 openapi-typescript 生成 `packages/api/src/schema.gen.ts`。S2 验证用的 TS 类型也在这一步重新生成。

生成物都要提交。CI 的 `make generate-check` 会重新生成并检查有没有差异。

Spectral 检查的是打包结果而不是源文件：Spectral 6.17 读取多文件的 3.1 源文件时，会对外部引用的路径项误报 `oas3-schema`，并把所有组件都当成未使用（S2 文档问题 7）。报错位置因此指向 `bundled/openapi.yaml`，按组件名或 operationId 回到源文件修改即可。

## 拆分约定

- 每个资源一个目录，路径放 `paths.yaml`，专属 schema 放 `schemas.yaml`。多个资源共用的放 `common/`。
- 新增的每个具名组件都要在根文件的 `components` 下登记一次（`Name: {$ref: ./dir/file.yaml#/Name}`）。Redocly 按登记的名字放进打包结果，生成的 Go 和 TS 类型名因此稳定，不随文件名变化。
- 资源文件之间直接用相对路径引用，例如 `$ref: "../common/schemas.yaml#/Links"`。同一文件内用 `#/Name`。

## 命名

| 对象 | 规则 | 示例 |
| --- | --- | --- |
| 路径 | 公开接口以 `/api/v1/` 开头；webhook 触发入口以 `/hooks/` 开头 | `/api/v1/runs/{runId}` |
| operationId | lowerCamelCase，与技术设计第 9.2 节一致 | `startRun` |
| schema 名 | UpperCamelCase | `RunPage` |
| JSON 字段 | snake_case | `next_cursor` |
| MCP 工具名 | snake_case，全局唯一 | `run_start` |

## 每个操作必须有的内容

Spectral 规则（`lerryn-*`）会检查：

- `operationId`，且全局唯一。
- `tags`，以及中文的 `description`（技术设计第 9.5 节）。`summary` 不强制，但建议写，它会成为生成代码和文档中的标题。
- `x-operation-kind`，取值见下文；GET 只能是 `read`。
- `x-mcp`，结构见下文。
- 错误响应：至少声明 `default`，引用 `#/components/responses/Problem`。错误响应只能用 `application/problem+json`。

## x-operation-kind

```yaml
x-operation-kind: read   # read | mutate | execute
```

| 取值 | 含义 | 需要的令牌范围 | 例子 |
| --- | --- | --- | --- |
| `read` | 只读，没有副作用 | `read` | `discover`、`getRun`、`validateTaskVersion` |
| `mutate` | 修改平台内的数据，不运行任务代码 | `write` | `applyTask`、`setTriggers`、`createCredentialRequest` |
| `execute` | 运行、取消或激活任务，可能经由任务代码影响外部系统 | `execute` | `startRun`、`cancelRun`、`setTaskActivation` |

它同时用于三处：访问令牌的范围检查（第 12.1 节），MCP 工具的标注（下文），以及审计记录中的操作类型。

## x-mcp

每个操作都要声明 `x-mcp`，明确是否作为独立的 MCP 工具暴露（技术设计第 10.2、10.3 节）。

```yaml
x-mcp:
  expose: true                 # 必填。false 时只能经 api_call 调用
  tool: discover               # expose 为 true 时必填。snake_case，全局唯一，最长 64 个字符
  title: 发现平台能力            # 可选。工具的显示名
  description: |               # expose 为 true 时必填。面向 Agent 的说明，可以与 operation 的 description 不同，应更紧凑
    读取 Lerryn 服务端的版本、可用运行时……
  hints:                       # 可选。覆盖按规则推出的标注，只在推导结果不对时使用
    destructive: true
    idempotent: false
    open_world: true
```

不暴露的操作写 `x-mcp: {expose: false}`，此时不能带其他字段。一个工具只对应一个操作，不做组合。

工具的输入 schema 由操作的参数和请求体生成。MCP 工具标注按下表推出，`hints` 中给出的字段覆盖推导结果：

| x-operation-kind | readOnlyHint | destructiveHint | idempotentHint | openWorldHint |
| --- | --- | --- | --- | --- |
| `read` | true | false | true | false |
| `mutate` | false | 方法为 DELETE 时为 true，否则 false | 方法为 PUT 或 DELETE 时为 true，否则 false | false |
| `execute` | false | true | false | true |

`hints` 的典型用途：`setTriggers` 用 PUT 整体替换触发器，推导出 destructive 为 false，但它会删除未列出的触发器，应写 `hints: {destructive: true}`。

## 错误

所有错误都是 RFC 9457 problem details，媒体类型 `application/problem+json`，schema 为 `Problem`：

```json
{
  "type": "urn:lerryn:error:credential_missing",
  "title": "缺少凭证",
  "status": 422,
  "code": "credential_missing",
  "detail": "任务引用的凭证 LLM_API_KEY 尚未填写",
  "instance": "/api/v1/tasks/…/runs",
  "next_actions": [{"action": "credential_request", "hint": "为该任务创建凭证填写请求，并把链接交给用户"}],
  "request_id": "req_7FZ2CQ4M3XKJ6N5W2Y4T3A6B"
}
```

- `type` 固定为 `urn:lerryn:error:<code>`。`title` 由 code 决定，不随请求变化。
- `code` 取自 `ErrorCode`，是 Agent 判断错误类别的唯一依据。`detail` 只给人和 Agent 阅读。
- `next_actions` 中的 `action` 取 MCP 工具名或 operationId。
- `request_id` 与响应头 `X-Request-Id` 相同。服务端为每个请求生成新的 ID，不采用客户端传来的值。
- `validation_failed` 额外带 `errors`，每项用 `pointer`（请求体中的 JSON Pointer）、`parameter` 或 `header` 指明位置。
- 某个错误需要额外字段时（例如 `version_conflict` 附当前版本号），在 `Problem` 中把它声明为可选属性。不要给 `Problem` 加 `additionalProperties: true`：oapi-codegen 为此生成的附加字段在部分响应类型上会被静默丢弃（S2 文档问题 2）。

`ErrorCode` 用 3.1 的 `oneOf` + `const` 声明，每个值带 `description`（含义和下一步），供 Skill 参考资料生成。每个分支必须有 `title`，生成的 Go 常量名为 `ErrorCode` + title。新增错误码只能追加，不能改名或删除。服务端在 `server/internal/httpapi/problem.go` 中为每个 code 维护中文 title，测试会检查两边一致。

HTTP 状态码与 code 的对应：

| code | 状态码 |
| --- | --- |
| `validation_failed` | 400（参数或请求体格式不对）；422（格式正确但内容校验不通过，例如清单） |
| `unauthenticated` | 401 |
| `insufficient_scope`、`registration_disabled`、`origin_not_allowed`、`permission_denied`、`password_login_disabled` | 403 |
| `not_found` | 404。没有查看权限的资源同样返回 404 |
| `expired` | 410。日志或产物已按保留策略清理，运行记录仍在 |
| `idempotency_conflict`、`username_taken`、`run_in_progress`、`version_not_tested`、`task_archived`、`last_owner`、`request_closed` | 409 |
| `version_conflict` | 412 |
| `precondition_required` | 428 |
| `credential_missing`、`credential_unavailable` | 422 |
| `queue_full`、`rate_limited` | 429 |
| `runner_unavailable` | 503 |
| `internal_error` | 500 |
| `dependency_install_failed`、`script_error`、`timeout`、`out_of_memory`、`run_interrupted` | 不作为 HTTP 错误返回，出现在运行记录的错误类别中 |

操作统一写 `default: {$ref: "#/components/responses/Problem"}`。需要在文档中突出某个状态码时，额外列出该状态码，同样引用这个响应。

## 分页

列表接口使用不透明游标（技术设计第 9.3 节）：

```yaml
parameters:
  - $ref: "../common/parameters.yaml#/Limit"
  - $ref: "../common/parameters.yaml#/Cursor"
responses:
  "200":
    content:
      application/json:
        schema:
          $ref: "./schemas.yaml#/RunPage"
```

```yaml
# <资源>/schemas.yaml
RunPage:
  allOf:
    - $ref: "../common/schemas.yaml#/CursorPage"
    - type: object
      required: [items]
      properties:
        items:
          type: array
          items:
            $ref: "#/Run"
```

`CursorPage` 只有 `next_cursor`（`string | null`，null 表示没有更多数据）。oapi-codegen 会把 `allOf` 合并成一个带 `Items` 和 `NextCursor` 的结构体，openapi-typescript 生成交叉类型；两端都已在 S2 中验证。

## 并发修改、幂等和预览

| 机制 | 写法 | 用在 |
| --- | --- | --- |
| 并发修改 | 请求头 `If-Match`（`common/parameters.yaml#/IfMatch`），值为读到的版本号，可带双引号。不一致返回 412 `version_conflict`，`Problem.current_version` 给出当前版本；修改已有资源却缺少它返回 428 `precondition_required` | `applyTask`（值为 `latest_version.seq`）、`setTriggers`（值为 `TriggerSet.revision`） |
| 幂等 | 请求头 `Idempotency-Key`（`#/IdempotencyKey`），相同键相同请求返回首次的结果，不同请求返回 409 `idempotency_conflict`，保留 24 小时 | `applyTask`、`startRun`、`rerunRun`、`createCredentialRequest`、`triggerWebhook` |
| 预览 | 查询参数 `dry_run`（`#/DryRun`），只校验并返回预览，不保存 | `applyTask`、`setTriggers` |

经 MCP 调用时，请求头参数按第 10.3 节的规则变成 `if_match`、`idempotency_key`。

版本号用请求头而不是请求体字段，是因为它描述的是“我读到的状态”，不是资源内容；同一份请求体可以原样重试。

## 非 JSON 的响应

| 场景 | 写法 | 例子 |
| --- | --- | --- |
| SSE | `200` 的 `text/event-stream`，schema 为 `type: string`；事件类型和 data 结构写在 description 中，data 的结构登记为具名 schema（例如 `LogLine`、`RunUpdate`），供客户端引用类型 | `streamRunLogs`、`streamEvents` |
| 下载 | `200` 的 `text/plain` 或 `application/octet-stream`，带 `Content-Disposition` 头；可以另列 `302` 跳转到预签名地址 | `downloadRunLogs`、`downloadArtifact` |
| 跳转 | `302` 带 `Location` 头，没有响应体。只用于浏览器导航的接口 | `oidcStart`、`oidcCallback` |

这些操作都不暴露为 MCP 工具。oapi-codegen 为它们生成以 `io.Reader` 为响应体的类型，SSE 处理函数需要自己写一个带刷新的响应类型，或者在路由上单独处理。

## webhook 触发入口

`POST /hooks/{triggerId}` 在契约中声明，`security` 只有 `triggerToken`，因为调用方是外部系统，不是任何用户。它不在 `/api/v1` 下：API 的鉴权中间件只接受 `wl_pat_` 令牌，这个入口要在 `server/internal/app` 中单独挂载并自行校验触发器令牌。

## 尚未实现的操作

契约先于实现。M1 之后新增的操作在 `server/internal/httpapi` 中按领域各有一个桩文件（`tasks_versions.go`、`run_history.go`、`overview.go`、`triggers.go`、`projects.go`、`sessions.go`、`admin.go`、`credentials.go`、`notifications.go`、`runners.go`），返回 `internal_error`。实现某个领域时替换对应的文件，并从 `pending_test.go` 中删掉对应的行。

## 链接

返回任务、运行等资源时带 `links`，引用 `common/schemas.yaml#/Links`。`links.console` 是控制台对应页面的完整 URL，由部署配置的 `server.public_url` 拼出，Agent 可以直接交给用户。

## 鉴权

根文件的全局 `security` 要求访问令牌或会话之一：

| 方案 | 类型 | 用途 |
| --- | --- | --- |
| `accessToken` | HTTP Bearer，`wl_pat_…` | Agent、CLI、MCP |
| `sessionCookie` | cookie `lerryn_session` | Web 控制台、移动端。移动端的设备会话只有 read 范围 |
| `triggerToken` | HTTP Bearer，`wl_hook_…` | 只用于 `POST /hooks/{triggerId}`，见上文“webhook 触发入口” |

允许匿名调用的操作在 `security` 中加一项空对象：

```yaml
security:
  - {}
  - accessToken: []
  - sessionCookie: []
```

令牌范围不写进 `security`，由 `x-operation-kind` 推出。

## 3.1 写法

S2 验证过的写法和需要避开的写法：

| 场景 | 写法 | 说明 |
| --- | --- | --- |
| 可空的标量或数组 | `type: [string, "null"]` | Go 为指针；必填字段为 null 时会输出 `null` |
| 可空的对象引用 | `oneOf: [{$ref: …}, {type: "null"}]` | Go 为指针，TS 为 `X \| null` |
| 可选且可空 | 尽量避免 | Go 默认映射无法区分“缺省”和 null。PATCH 请求体确实需要时，先决定是否启用 `nullable-type`（S2 文档建议 3） |
| 枚举 | `enum: [...]` | 每个值都需要说明时改用 `oneOf` + `const`，并给每个分支加 `title` |
| 可空的枚举 | `type: [string, "null"]` + `enum`，**不要**把 `null` 写进 `enum` | oapi-codegen 会为 `null` 生成名为 `<nil>` 的错误常量（S2 文档问题 1） |
| 单值常量 | `const: cron` | 判别字段用这种写法 |
| 多态 | `oneOf` + `discriminator`（带 `mapping`），各分支的判别字段写 `const` | Go 生成 `ValueByDiscriminator`，TS 可以按判别字段收窄 |
| 示例 | schema 中用 `examples: [...]`；媒体类型中用 `example` | `example` 在 3.1 的 schema 中已废弃 |
| 对外通知 | 根文件的 `webhooks` | oapi-codegen 已配置 `skip-prune`，只被 webhooks 引用的 schema 才会生成 |

## 契约测试

`server/internal/httpapi` 的测试用 kin-openapi 读取 `bundled/openapi.yaml`，用 `assertMatchesSpec` 校验处理函数的真实响应：状态码已声明、媒体类型正确、响应体符合 schema。生成的 Go 类型无法单独保证必填、可空和枚举约束，新接口的测试都应该调用它。
