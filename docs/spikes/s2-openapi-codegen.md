# S2：OpenAPI 3.1 生成链路验证

日期：2026年11月2日　对应：实施规划第 5 节 S2、技术设计第 2、9 节

## 结论

**OpenAPI 3.1 链路可用，不需要退回 3.0.3。**

用一份覆盖 nullable、oneOf + discriminator、webhooks、problem+json 的小规范，分别生成了 Go strict server 和 TS 类型，并用测试和类型断言逐项核对。两端对这些写法的生成结果都正确。发现的问题共 13 条（见第 4 节），其中会导致错误结果的 4 条都有确定的规避办法，已经落实到正式契约的写法约定、生成配置和 Spectral 规则中；其余是需要知道的行为差异或待决定的选项。

实施规划的通过标准是“两端生成结果正确，问题列成清单”，两项都满足。

## 1 做了什么

### 工具版本

| 工具 | 版本 | 引入方式 |
| --- | --- | --- |
| oapi-codegen | v2.8.2 | `go.mod` 的 `tool` 指令，`go tool oapi-codegen` |
| oapi-codegen/runtime | v1.8.0 | `go.mod` |
| kin-openapi | v0.144.0（oapi-codegen 依赖 v0.142.0，见问题 12） | `go.mod`，只在生成器和测试中使用 |
| Redocly CLI | 2.72.1 | 根目录 `package.json` |
| Spectral CLI | 6.17.0 | 根目录 `package.json` |
| openapi-typescript | 7.13.0 | `packages/api` |
| openapi-fetch | 0.17.0 | `packages/api` |

### 验证材料

| 文件 | 内容 |
| --- | --- |
| `api/spikes/s2/openapi.yaml` | 验证用的小规范：3 个操作、1 个 webhook，覆盖下表的写法。其中两处是故意写错的（可空枚举里的 `null`、没有 title 的 oneOf 枚举），用来记录生成器的行为 |
| `api/spikes/s2/goserver/` | 生成的 Go strict server（`server.gen.go`）和 12 个测试（`spike_test.go`） |
| `api/spikes/s2/ts/` | 生成的 TS 类型（`schema.gen.ts`）和类型断言（`assertions.ts`），只需通过 `tsc` |

验证材料不进入正式的包。它们挂在 `make generate`、`make test` 和 `make typecheck` 上，升级 oapi-codegen 或 openapi-typescript 时会重新跑一遍：名为 `TestKnownIssue*` 的测试固定了当前的错误行为，哪天上游修好了，这些测试会失败，提示重新评估对应的规避办法。确认不再需要时可以整个删掉 `api/spikes/`，没有其他代码依赖它。

复现：

```bash
make generate                              # 含 spike 的 Go 和 TS 生成
go test -v ./api/spikes/s2/goserver/
make typecheck                             # 含 api/spikes/s2/ts 的类型断言
```

### 覆盖的写法与结果

| 写法 | Go（oapi-codegen） | TS（openapi-typescript + openapi-fetch） |
| --- | --- | --- |
| 必填且可空：`type: [string, "null"]`、`format: date-time` | `*time.Time`；nil 编码为 `null`，不会被省略 | `string \| null` |
| 必填且可空的整数、数组 | `*int`、`*[]string` | `number \| null`、`string[] \| null` |
| 可选且可空 | `*int` + `omitempty`；缺省和 `null` 解码结果相同（问题 6） | `number \| null \| undefined`，三种状态可区分 |
| 可空的对象引用：`oneOf: [$ref, {type: "null"}]` | `*RunError` | `RunError \| null` |
| 同上，`anyOf` 写法 | `*Principal` | `Principal \| null` |
| 可空的枚举，`enum` 中含 `null` | 生成错误常量 `LessThannil = "<nil>"`（问题 1） | `"low" \| "normal" \| "high" \| null`，正确 |
| `oneOf` + `const` 枚举，分支带 `title` | 带类型的常量和 `Valid()` | 字面量联合类型 |
| 同上，分支不带 `title` | 退化为 `= string`，没有常量（问题 4） | 字面量联合类型 |
| 属性上的 `const` | 单值枚举类型 | 字面量类型 |
| `oneOf` + `discriminator`（带 `mapping`） | 联合包装类型，`From*`／`As*`／`ValueByDiscriminator`；`From*` 自动填入判别值；未知判别值返回错误 | 可辨识联合，`switch` 可以穷尽收窄；未知判别值和缺字段在编译期报错 |
| `allOf` 组合的分页包络 | 合并成一个结构体 | 交叉类型 |
| `webhooks` | 载荷类型 + `WebhookReceiverInterface`；需要 `skip-prune`（问题 3） | `webhooks` 接口 |
| `application/problem+json` 的 `default` 响应 | 设置 `Content-Type` 和状态码；`StatusCode` 留 0 会 panic（问题 9） | openapi-fetch 的 `error` 类型为 `Problem` |
| 按状态码列出、引用共享响应组件的 problem+json | 正确；但 `additionalProperties: true` 的附加字段被丢弃（问题 2） | 同上 |
| 必填请求头（`If-Match`） | 生成的包装层检查，缺失时返回 400 | 编译期必填 |
| `$ref` 旁的 `description` | 保留在注释中 | 保留在 JSDoc 中 |
| schema 的 `examples` 数组 | 写入注释 | `@example` |
| 数值型 `exclusiveMinimum` | 接受；`number` 映射为 `float32`（问题 10） | `number` |
| 嵌入规范（`embedded-spec`） | `GetSwagger()` 保留 3.1 的类型数组、webhooks 和 `x-` 扩展 | 不适用 |
| 请求校验 | strict server 不校验请求体（问题 8）；kin-openapi 的 `openapi3filter` 对上述 3.1 写法校验正确 | 不适用 |

打包和检查：Redocly CLI 正确打包多文件的 3.1 规范，在根文件登记过的组件保持原名；Spectral 对打包结果检查正常，对多文件源文件有误报（问题 7）。

## 2 正式契约的落地情况

验证结论已经用在 M0 的契约骨架上：

- `api/openapi/` 按资源拆分，`make api-bundle` 打包到 `api/bundled/openapi.yaml`（提交入库）。
- `server/internal/httpapi/oapi-codegen.yaml` 开启 `skip-prune`、`always-prefix-enum-values` 和 `ToCamelCaseWithInitialisms`，不开 `embedded-spec`。
- `ErrorCode` 用 `oneOf` + `const` + `title` 声明，每个值带含义和下一步。`Problem` 的扩展字段逐个声明，没有 `additionalProperties: true`。
- `discover` 的 `principal` 用 `oneOf: [$ref, {type: "null"}]`。
- `httpapi` 提供 `problemResponse` 构造 `default` 响应，避免问题 9。
- `httpapi` 的测试用 kin-openapi 按打包规范校验处理函数的真实响应（`assertMatchesSpec`），补上生成类型管不到的必填、可空和枚举约束。
- Spectral 规则 `lerryn-enum-oneof-title`、`lerryn-no-null-in-enum` 拦住问题 4 和问题 1 的写法。
- 写法约定写进了 `api/README.md` 的“3.1 写法”一节。

## 3 判断依据

- **两端结果是否正确**：Go 端以测试验证 JSON 编解码、判别联合的往返、problem+json 响应的状态码和 `Content-Type`、嵌入规范的内容、kin-openapi 对 3.1 写法的校验。TS 端以 `Equal` 类型断言和 `@ts-expect-error` 验证类型，并用 openapi-fetch 的调用确认请求参数和错误类型。
- **问题是否影响 3.1 的选择**：13 条问题里没有一条要靠退回 3.0.3 才能解决。问题 1、2、3、4 是 oapi-codegen 的缺陷，3.0.3 下也有对应的写法问题（3.0.3 没有 `webhooks`、`const` 和类型数组，nullable 需要 `nullable: true`，MCP 需要的 JSON Schema 还得在构建时再转换一次）。退回 3.0.3 换来的不是更少的问题，而是另一组问题加一个转换步骤。

## 4 问题清单

严重程度：高 = 静默产生错误结果或无法编译；中 = 容易踩到、需要约定规避；低 = 需要知道的差异。

| # | 组件 | 问题 | 严重程度 | 处理 |
| --- | --- | --- | --- | --- |
| 1 | oapi-codegen | 可空枚举写成 `type: [string, "null"]` 且 `enum` 中含 `null` 时，为 `null` 生成名为 `LessThannil`、值为 `"<nil>"` 的常量，`Valid()` 也会接受这个值 | 中 | 约定不把 `null` 写进 `enum`，可空性只用类型数组表达；Spectral `lerryn-no-null-in-enum` |
| 2 | oapi-codegen | schema 带 `additionalProperties: true` 时生成自定义 `MarshalJSON`。按状态码列出、引用共享响应组件的响应类型是 `type X Problem` 这样的新定义类型，不继承这个方法，附加字段在编码时被静默丢弃；`default` 响应用 `Body Problem` 字段，不受影响 | 高 | `Problem` 不声明 `additionalProperties: true`，扩展字段逐个声明为可选属性 |
| 3 | oapi-codegen | 裁剪未使用组件时不遍历 `webhooks`，只被 webhooks 引用的 schema 被删掉，生成的代码引用了不存在的类型，无法编译 | 高 | `output-options.skip-prune: true`。顺带让 `CursorPage`、`ErrorCode` 等通用组件在第一个接口使用前就有 Go 类型 |
| 4 | oapi-codegen | `oneOf` + `const` 枚举只有在每个分支都带 `title` 时才生成常量，`title` 即常量名；缺一个 `title` 就整体退化为 `type X = string`，不报错 | 中 | 每个分支写 `title`；Spectral `lerryn-enum-oneof-title` |
| 5 | oapi-codegen | 枚举常量默认不带类型前缀（如 `Queued`、`Cron`），只有出现同名值时才加前缀。以后新增一个含相同值的枚举，会让已有常量改名，破坏与改动无关的代码 | 中 | `compatibility.always-prefix-enum-values: true`，常量一律为 `类型名 + 值`，例如 `NetworkPolicyNone` |
| 6 | oapi-codegen | 可选且可空的字段默认映射为指针加 `omitempty`，无法区分“缺省”和 `null`。对响应没有影响，对 PATCH 请求体有影响（“不修改”和“清空”无法区分） | 中 | 暂不处理，见建议 3。`output-options.nullable-type: true` 可以改为 `nullable.Nullable[T]`，已验证三种状态都能区分，代价是所有可空字段的 Go 类型都会变 |
| 7 | Spectral 6.17 | 直接检查多文件的 3.1 源文件时，对引用外部文件的路径项误报 `oas3-schema`（“must not have unevaluated properties”），并把所有组件报成未使用 | 中 | 改为检查打包结果（`make api-lint` 先打包）。报错位置指向 `api/bundled/openapi.yaml` |
| 8 | oapi-codegen | strict server 只做参数绑定和 JSON 解码，不按 schema 校验请求体：缺必填字段、`const` 不符、未知判别值都会原样交给处理函数。必填参数和请求头是检查的 | 中 | 见建议 4。kin-openapi 的 `openapi3filter` 已验证能正确校验这些 3.1 写法 |
| 9 | oapi-codegen | 每个操作的 `default` 响应类型需要处理函数自己填 `StatusCode`，漏填时生成代码调用 `WriteHeader(0)`，net/http 直接 panic | 中 | `httpapi.problemResponse` 统一构造，同时保证 `StatusCode` 与 `Body.Status` 一致 |
| 10 | oapi-codegen | `type: number` 默认映射为 `float32` | 低 | 目前没有 number 字段。出现时在配置中用 `type-mapping` 映射为 `float64` |
| 11 | openapi-typescript | `oneOf` + `const` 枚举生成为 `string & ("a" \| "b")`，与联合类型等价；但每个值的 `description` 不会进入 TS 注释 | 低 | 接受。错误码说明以规范和 Skill 参考资料为准 |
| 12 | kin-openapi | oapi-codegen v2.8.2 依赖的 kin-openapi v0.142.0 有两个已公开的漏洞：GO-2026-6112（content 参数无 schema 时 nil 指针 panic）、GO-2026-6095（默认的 `NoopAuthenticationFunc` 导致鉴权被绕过）。govulncheck 判定代码未调用，但仍会提示 | 中 | 升到 v0.144.0，两端生成结果逐字节不变，测试全部通过。将来引入 `openapi3filter` 中间件时，必须显式设置 `AuthenticationFunc` |
| 13 | Redocly CLI、Spectral CLI | Redocly CLI 默认上报使用统计；Spectral CLI 依赖 `@scarf/scarf`，安装时上报 | 低 | `api/redocly.yaml` 设 `telemetry: off`，Makefile 另外导出 `REDOCLY_TELEMETRY=off`；pnpm 10 默认不运行依赖的安装脚本，根目录 `package.json` 把 `@scarf/scarf` 列入 `ignoredBuiltDependencies`，明确不执行 |

## 5 对技术设计的修改建议

以下只是建议，技术设计本身没有改动。

1. **第 9.4 节错误码**：补充两个码。`unauthenticated`（缺少或无效的令牌、会话，401）在任何带鉴权的接口中都需要，账号骨架马上会用到；`internal_error`（500）是服务端内部错误的唯一出口。两者已经加进契约。另建议在表中增加“HTTP 状态码”一列，对应关系见 `api/README.md` 的“错误”一节；并写明 `Problem` 的 `errors` 字段格式（`detail` 加 `pointer`／`parameter`／`header` 之一，参照 RFC 9457 的示例）和 `instance` 字段。
2. **第 9.1、9.5 节生成链路**：写明打包结果的位置 `api/bundled/openapi.yaml` 且提交入库；Spectral 检查打包结果而不是源文件（问题 7）；`make generate` 需要 Go 和 Node 两套工具链，CI 为此单独设了 `api` 作业。
3. **第 9.3 节可空字段**：在第一个 PATCH 接口（`updateMe`）之前定下可选且可空字段的处理方式（问题 6）。建议保持默认的指针映射，PATCH 请求体避免“可选且可空”的字段，清空用明确的值表达；确有三态需求时再整体启用 `nullable-type`。这件事越晚定，切换时要改的处理函数越多。
4. **第 9.3 节请求校验**：strict server 不校验请求体（问题 8）。建议 M1 在 `/api/v1` 上加 kin-openapi `openapi3filter` 请求校验中间件，把校验错误统一转成带 `errors` 的 `validation_failed`。这需要开启 `embedded-spec`，kin-openapi 会进入运行时二进制；中间件必须显式设置 `AuthenticationFunc`（问题 12）。另一条路是在 service 层手写校验，重复劳动多，也容易和契约脱节。
5. **第 10.3 节 x-mcp**：把 `api/README.md` 中的 `x-mcp` 结构写进技术设计：`expose`（必填）、`tool`、`title`、`description`、可选的 `hints`；每个操作都必须声明 `x-mcp`，不暴露的写 `{expose: false}`；以及由 `x-operation-kind` 和 HTTP 方法推出 MCP 工具标注的规则表。
6. **第 9.3 节请求 ID**：补一条约定：每个响应带 `X-Request-Id`，由服务端生成（`req_` 加 26 位 base32），不采用客户端传入的值，错误体中的 `request_id` 与它相同。
7. **第 2 节技术栈**：oapi-codegen 一行补充生成配置的三个要点（`skip-prune`、`always-prefix-enum-values`、`ToCamelCaseWithInitialisms`），并注明 kin-openapi 不低于 v0.144.0。
8. **第 13.1 节和实施规划第 9 节**：“生成的 TS 客户端放在哪里”可以关闭：按第 13.1 节放在 `packages/api`，包名 `@lerryn/api`，导出生成的类型和基于 openapi-fetch 的 `createLerrynClient`。
9. **第 9.2 节 discover**：补充响应结构：`server`（版本、提交、构建时间）、`api`（主版本、契约版本）、`capabilities`（登录方式、开放注册、网络策略、激活前必须测试）、`runtimes`、`skills`（版本和列表）、`principal`（匿名时为 null）、`links.console`。`capabilities` 中的开放注册和“激活前必须测试”目前返回文档规定的默认值，设置服务落地后改为读取数据库。
