# AGENTS.md

这份文件写给在本仓库里工作的 AI Agent 和人。动手之前先读完。这里的规则和 `docs/` 中的文档冲突时，以文档为准，并在交付说明里指出冲突。

## 项目

Lerryn 是由外部 Agent 通过 API 和 MCP 操作的自动化沙箱，面向企业自部署。

- 产品范围：[docs/zh-CN/agent-automation-sandbox-prd.md](docs/zh-CN/agent-automation-sandbox-prd.md)
- 技术设计：[docs/zh-CN/agent-automation-sandbox-tech-design.md](docs/zh-CN/agent-automation-sandbox-tech-design.md)
- 实施规划和当前阶段：[docs/zh-CN/agent-automation-sandbox-implementation-plan.md](docs/zh-CN/agent-automation-sandbox-implementation-plan.md)
- 设计语言与组件库：[docs/zh-CN/design-system.md](docs/zh-CN/design-system.md)，以及项目 Skill `.claude/skills/lerryn-design-system`

## 目录与负责的工作流

| 目录 | 内容 | 工作流 |
| --- | --- | --- |
| `go.mod`、`Makefile`、`Dockerfile`、`.gitea/`、`server/cmd/`、`server/internal/{app,config,logging,buildinfo}/` | 工程基础、服务端入口 | W0 |
| `api/`、`server/internal/httpapi/` | OpenAPI 规范、runner 协议、生成的接口 | W1 |
| `server/internal/store/`、`server/migrations/`、`server/internal/blob/` | 存储 | W2 |
| `server/internal/runner/`、`server/internal/executor/`、`runtimes/` | 执行内核 | W3 |
| `server/internal/scheduler/` | 调度与触发 | W4 |
| `server/internal/auth/`、`server/internal/secrets/`、`server/internal/web/` | 账号、凭证与审计 | W5 |
| `server/internal/mcp/`、`server/internal/cli/`、`skills/`、`tests/agent-evals/` | Agent 入口 | W6 |
| `server/internal/events/`、`tests/chaos/` | 多节点 | W7 |
| `deploy/`、`server/internal/notify/` | 部署与运维 | W8 |
| `server/internal/service/` | 业务逻辑，按领域分文件（`task.go`、`run.go` 等） | 各工作流只改自己领域的文件 |
| `design/` | 设计语言与组件库 | 设计语言工作；改动前读 `docs/zh-CN/design-system.md` |
| `apps/`、`packages/` | 产品前端，组件库完成后确定 | W9 |
| `docs/` | 产品与技术文档 | 改动时说明原因 |
| `website/` | 面向 lerryn.niltavi.com 的独立静态官网 | 官网；不与产品控制台共用入口 |

只改当前任务负责的目录。需要改别的目录时，在交付说明里写清楚，不要顺手改。

## 分层

- 请求路径固定为：接口层（`httpapi`、`mcp`）→ `service` → `store`。
- 接口层只做解码、调用 service、编码，不写业务逻辑。MCP 工具在进程内转成 HTTP 请求，走同一条处理链。
- 权限检查只在 service 层。没有查看权限的资源一律返回 `not_found`。
- `server/internal/app` 是组装入口，负责把各层连起来。
- 不引入依赖注入框架和反射式路由；数据访问只用 Bun。

## 契约与代码生成

- 接口改动从 `api/` 开始，再运行 `make generate`。
- 生成的文件以 `Code generated ... DO NOT EDIT.` 开头，不手工修改。
- 生成器通过 `//go:generate` 指令挂到 `make generate` 上。
- Go 工具（代码生成器、迁移工具、govulncheck）用 `go.mod` 的 `tool` 指令固定版本，用 `go tool <名字>` 运行，不要求全局安装。

## 数据库

- 同时支持 PostgreSQL 和 SQLite。迁移在 `server/migrations/postgres/` 和 `server/migrations/sqlite/` 各一份，必须同时提交，文件名以时间戳开头。
- 方言专属的 SQL 只放在 `server/internal/store/dialect/`。
- 主键用应用生成的 UUIDv7；时间由应用以 UTC 写入；JSON 字段只存不查；不用触发器和存储过程。
- 存储层测试读取 `LERRYN_TEST_DATABASE`（`sqlite` 或 `postgres`）和 `LERRYN_TEST_POSTGRES_DSN` 选择数据库，`make test-pg` 会设置这两个变量。

## 测试

- `make check` 是合并前的必过项：Spectral（`make api-lint`）、golangci-lint、go vet、带 `-race` 的测试、govulncheck、前端类型检查和测试、第三方许可证声明（`make notices-check`；依赖有变化时运行 `make notices` 并提交 `THIRD_PARTY_NOTICES`）。CI 另外跑 `make generate-check`、`make test-pg` 和两个执行器集成测试作业。
- 单元测试不访问网络，不依赖 Docker。需要 Docker 的测试加 build tag `integration`，用 `make test-integration` 运行。里程碑的验收场景也写成这类测试，例如 `server/cmd/lerryn/m1_integration_test.go` 用 all-in-one 的启动代码跑 A09、A10、A13。
- 服务端嵌入了 `api/` 的契约和 `runtimes/sdk/` 的 sbx SDK。新增嵌入的目录时，同步修改 `Dockerfile` 的 `COPY`，否则镜像构建会失败。
- 优先写表驱动测试，测试与被测代码放在同一个包。
- 验收场景（A01 至 A18、B01 至 B03）的测试放在 `tests/acceptance/`。

## 代码风格

- 代码、注释、日志和错误信息用英文；文档和面向用户的界面文案用中文。
- 日志用 `log/slog` 的结构化字段，不拼接字符串。日志中不出现凭证，也不出现任务日志的内容。
- 错误用 `%w` 包装，并带上下文。
- 注释说明为什么这样做，不复述代码。

## 安全

- 凭证明文不进入日志、URL、普通 API 响应、MCP 返回或任务参数。
- 加密、权限、脱敏、容器加固的改动必须附测试，并在交付说明中标出。

## git 与运行环境

- 提交说明使用英文，遵循 Git 的命令式标题风格；标题简洁，不加句号。需要正文时，空一行说明原因与结果。
- 提交与推送按维护者授权执行；不擅自变基、强推、切换或删除分支。
- 开发服务器由维护者在本地启动。需要运行中的服务时说明需求，不自行启动或重启。测试自己拉起的临时进程和容器不在此列。

## 需要人工审查的改动

- `api/` 下的契约变更。
- 数据库迁移。
- 权限、凭证、脱敏、容器加固相关代码。
- `skills/` 中的硬规则和服务端约束。
- 新增的第三方依赖（Go 或 npm）。

## 交付说明

每次任务结束时给出：

1. 改动的文件清单。
2. 运行过的验证命令及结果。
3. 未完成的部分。
4. 对契约或其他工作流的影响。

## 许可证

仓库整体采用 AGPL-3.0；`runtimes/sdk/` 和 `skills/` 采用 Apache-2.0。复用第三方代码时保留原版权和许可证声明，并记录在 `THIRD_PARTY_NOTICES` 中。
