# Agent 自动化沙箱实施规划

版本：0.2 草案　日期：2026年11月3日　对应 [PRD 0.3](agent-automation-sandbox-prd.md)、[技术设计 0.4](agent-automation-sandbox-tech-design.md)

PRD 说明要做什么，技术设计说明怎么做。这份文档回答剩下的问题：先做什么，哪些可以同时做，每一步做到什么程度算完成。开发主要由多个 AI 会话完成，所以专门有一节讲会话之间怎么分工、怎么避免互相打架。

设计语言和组件库（全部在 `design/` 下，说明见 [design-system.md](design-system.md)）正在单独推进。本文不规划它的内部工作，只记录控制台对它的依赖和对接时机。

本文不给日历日期。开发速度取决于 AI 会话的实际产出，M0 结束后按真实节奏再估。

## 1 现状

| 项 | 状态 |
| --- | --- |
| PRD | 0.3 草案，在 `docs/` |
| 技术设计 | 0.4 草案，在 `docs/`，已按 M0 的技术验证和 M1 至 M6 的实现修订，修订记录在第 23 节 |
| 设计语言与组件库 | 进行中：`design/tokens.json`、`design/ui-native`（`@lerryn/ui-native`，HeroUI Native + Uniwind）、`design/gallery`；现状见 [design-system.md](design-system.md) |
| 仓库 | github.com/niltavi/lerryn，许可证 AGPL-3.0（`runtimes/sdk/`、`skills/` 为 Apache-2.0）。Go module 与 pnpm workspace（`design/ui-native`、`design/gallery`、`packages/api`，以及预留的 `apps/*`）并存 |
| 服务端 | M1 至 M6 完成。`lerryn all-in-one` 在一个进程里提供 API、`/mcp`、Web 控制台和内嵌 runner；`lerryn server` 和 `lerryn runner` 分开部署时，多个 server 共用 PostgreSQL 和对象存储。任务、版本、触发器、运行、凭证、工具登记、账号与审计、通知、备份恢复和指标都已实现 |
| CLI、Skill | CLI（`login`、`tasks`、`runs`、`api`、`mcp --stdio`、`skills install`）见 [cli.md](cli.md)；四个 Skill（`lerryn-automation`、`lerryn-troubleshoot`、`lerryn-credentials`、`lerryn-operate`）随二进制发布，四个评测场景按提议的阈值达标，见[验收矩阵](acceptance-matrix.md)第 4 节 |
| runner | 内嵌和独立部署两种模式；Python、Node、Shell 三种运行时；凭证经 stdin 注入、输出脱敏；脚本工具只读挂载，`sbx mcp call` 调用登记的 MCP 工具并记录调用。协议 2 增加跨运行 `/sbx/state`：领取时固定输入、成功事务发布、失败保留旧状态；双 server／双 runner 的真实容器验收见 `tests/acceptance/state_test.go`，协议升级边界见技术设计第 4.5 节 |
| 控制台 | Web 控制台的全部页面已接入接口，经 SSE 实时更新；移动端为只读的 Expo 应用，扫码配对。A03、A04、A06、A13、A17 的界面部分已通过端到端测试；Android 未验证，详见[验收矩阵](acceptance-matrix.md) |
| 验收 | A01 至 A18、B01 至 B03 的服务端部分都有真实进程和 Docker 上的测试，逐项结果和剩余缺口见[验收矩阵](acceptance-matrix.md) |
| CI | Gitea Actions 的工作流文件位于 `.gitea/workflows/`，保留 7 个作业：Go（lint、vet、测试、govulncheck）、契约（生成一致性、Spectral、许可证声明）、PostgreSQL、两个执行器集成测试、前端（类型检查、单元测试、官网检查）、镜像构建。实例启用与 runner 要求见 [Gitea Actions 配置](ci.md)；首次运行通过前，合并前仍在本地运行 `make check`、`make test-pg` 和 `make test-integration` |

## 2 规划原则

- **契约先行。** OpenAPI、runner 协议、任务清单 schema 和数据模型先定下来，各工作流按契约并行，不互相等实现。
- **先打通最窄的一条路。** 第一个能跑的版本只做一件事：Agent 经 MCP 提交一个 Python 脚本，平台在容器里执行，Agent 读到日志和结果。之后的功能都在这条路上加厚，而不是各自做完再拼装。
- **风险前置。** 拿不准的技术点先做小规模验证（第 5 节），有了结论再写正式代码。
- **以验收场景收尾。** 每个阶段结束时，对应的 PRD 验收场景要用真实执行和故障注入跑通。代码写完不算完成。
- **尽早自用。** 从 M1 起用 Claude Code 通过 MCP 在平台上建任务。接口设计上的毛病，越早暴露越便宜。
- **按目录分工。** 每个工作流有自己负责的目录，多个会话同时开发时尽量不改同一批文件。

## 3 工作流

| 编号 | 工作流 | 范围 | 负责目录 |
| --- | --- | --- | --- |
| W0 | 工程基础 | 仓库布局、Go module、Makefile、CI、`AGENTS.md`、服务端入口骨架、镜像构建 | 根目录配置、`.gitea/`、`Makefile`、`Dockerfile`、`server/cmd/` |
| W1 | 契约 | OpenAPI 规范、runner 协议、任务清单 schema、错误码、`x-mcp` 约定、Spectral 规则、代码生成配置 | `api/` |
| W2 | 存储 | 数据模型、两种数据库的存储层和迁移、存储契约测试、对象存储接口 | `server/internal/store/`、`server/migrations/`、`server/internal/blob/` |
| W3 | 执行内核 | runner 主循环、Docker 执行器、运行状态机、环境准备、日志管道、取消和超时、对账、`sbx` 辅助工具、运行时镜像 | `server/internal/runner/`、`server/internal/executor/`、`runtimes/` |
| W4 | 调度与触发 | cron、webhook、队列、并发、重试、幂等、选主 | `server/internal/scheduler/` |
| W5 | 账号、凭证与审计 | 内置账号、OIDC、会话、访问令牌、移动端配对、项目和角色、管理设置、凭证加密、填写流程和填写页、注入、脱敏、审计 | `server/internal/auth/`、`server/internal/secrets/`、`server/internal/web/` |
| W6 | Agent 入口 | MCP 生成器与服务、CLI、Skill、Agent 评测 | `server/internal/mcp/`、`server/internal/cli/`、`skills/`、`tests/agent-evals/` |
| W7 | 多节点 | PostgreSQL 领取与事件总线、S3 存储、runner 标签、故障测试 | `server/internal/events/`、`tests/chaos/` |
| W8 | 部署与运维 | Compose、配置、通知、备份恢复、导出导入、升级、指标、容量测试 | `deploy/`、`server/internal/notify/` |
| W9 | 控制台 | Web 控制台和移动端的页面 | 组件库完成后确定 |
| D | 设计语言与组件库 | 单独推进，不在本文规划 | `design/` |

`server/internal/service/` 是业务逻辑层，几乎每个工作流都会碰到。约定按领域分文件（`task.go`、`run.go`、`credential.go` 等），各工作流只改自己领域的文件。

## 4 依赖与关键路径

```
W0 → W1 → W2、W3 → W6（最小闭环）→ W4、W5 → W7 → W8 → 首个试用版
```

- W2 和 W3 依赖的是 W1 定下的数据模型和存储接口，不依赖存储层的实现。接口定下后两者可以同时开始。
- W4 和 W5 互不依赖，可以同时进行。
- W9 不在关键路径上。组件库可用、相关接口稳定后即可开始；开发期间对着 OpenAPI 生成的模拟服务工作，不必等后端。
- W8 中的 Compose、配置和指标可以从 M1 开始逐步补，备份恢复和容量测试放在后期。

关键路径上最可能拖慢进度的是 W3。容器生命周期、日志管道和对账都有大量边界情况，W3 应该最早开始，也最值得投入人工审查。

## 5 技术验证

M0 期间先做以下验证。每项都写明通过标准和不通过时的退路，免得验证变成无限期的调研。

| 编号 | 要回答的问题 | 做法 | 通过标准 | 不通过时 |
| --- | --- | --- | --- | --- |
| S1 | Bun 能否用一套代码覆盖 PostgreSQL 和 SQLite | 实现运行的领取、部分唯一索引、租约续期，同一套测试在两种数据库上运行 | 测试全部通过；方言专属代码集中在 `store/dialect/` | 改用 Ent，或 sqlc 按方言各生成一份 |
| S2 | OpenAPI 3.1 的生成链路是否可靠 | 写一个包含 nullable、oneOf、webhooks、problem+json 的小规范，生成 Go strict server 和 TS 类型 | 两端生成结果正确，问题列成清单 | 规范改写为 3.0.3，构建时转换出 MCP 需要的 JSON Schema |
| S3 | MCP 无状态模式和进程内转发是否可行 | 用 go-sdk 的无状态模式，由 `x-mcp` 生成 2 个工具，经同一条 HTTP 处理链转发 | Claude Code 和 Codex 都能连接并调用；旧协议客户端的行为记录在案 | 改用有状态模式，负载均衡做会话粘滞 |
| S4 | 容器执行的加固和清理 | 在 macOS（OrbStack 或 Docker Desktop）和 Linux 上运行带子进程的任务，测取消、超时、资源限制，以及 runner 自身在容器中运行时的挂载路径 | A09 场景在两个平台都通过 | 调整挂载方案；runner 改为直接装在宿主机 |
| S5 | 日志写入 PostgreSQL 能否承载目标容量 | 50 个并发运行，每个每秒 500 行，写入分片表并发送通知 | 新日志 2 秒内可见；数据库和 server 的资源占用记录在案 | 日志热数据改由独立存储承载，数据库只存索引 |

S1 到 S4 是 M0 的完成条件。S5 需要先确定容量目标，最晚在 M5 前完成。MCP OAuth 已经移到 P1，原计划的 OAuth 验证随之取消。

## 6 阶段计划

| 阶段 | 状态 | 覆盖验收 |
| --- | --- | --- |
| M0 工程基础与技术验证 | 完成：工程基础；S1 至 S4 均通过；账号骨架；技术设计按结论更新到 0.3。完成标准由 `server/cmd/lerryn/m0_test.go` 端到端验证 | |
| M1 最小闭环 | 完成：A09、A10 在单机下通过，A13 接口层通过，由 `server/cmd/lerryn/m1_integration_test.go` 在真实 Docker 上验证；Claude Code 经 MCP 从零建立并运行了 Python 任务。详见 M1 小节 | A09、A10、A13 |
| M2 Agent 入口 | 完成（#2、#3、#10、#14）：CLI、`api_describe` 和 `api_call`，Node 和 Shell 运行时，任务生命周期、版本差异、`If-Match`、幂等、运行历史、SSE、产物，Skill 和评测框架。A01、A11、A12 由 `tests/acceptance/` 在真实 Docker 上验证，评测场景的结果见[验收矩阵](acceptance-matrix.md)第 4 节 | A01、A11、A12 |
| M3 调度与触发 | 完成（#12、#16）：cron 和时区、触发实例去重、重叠、停机错过、webhook 及请求体交到 `/sbx/in`、有界队列、项目级并发、自动重试、`trigger_set`。A02、A08 由 `tests/acceptance/` 用真实进程验证 | A02、A08 |
| M4 账号、凭证与审计 | 完成（#11、#13，首个试用版补充）：OIDC、设置、配对、项目与角色、管理和审计；凭证的加密、填写页、注入、脱敏和撤销；提交中出现凭证值时拒绝。A05 至 A07、A18 由 `tests/acceptance/` 验证 | A05、A06、A07、A18 |
| M5 多节点 | 完成（#7、#16、#17，首个试用版补充）：runner 协议的 HTTP 传输、远程 runner、选主、事件总线、S3、日志归档、跨副本事件；工具登记、脚本工具和 `sbx mcp call`。B02、B03 由 `server/cmd/lerryn/m5_integration_test.go` 验证，A14、B01 和 B03 的 SSE 续读由 `tests/acceptance/multinode_test.go` 验证，B01 只跑了 4 分钟；S5 见 [capacity.md](capacity.md) | A14、B01、B02、B03 |
| M6 运维与通知 | 完成（#4、#8、#17）：指标、备份恢复、导出导入、SQLite 迁到 PostgreSQL、通知、两种拓扑的 Compose 文件和部署文档。A15、A16 由 `tests/acceptance/m6_test.go` 验证；两种拓扑的控制面容量在开发机上实测达到提议的目标，目标主机上带容器的复测未做 | A15、A16 |
| 控制台轨道 | 第一版完成（#1、#5、#6、#9、#15、#18）：Web 控制台的全部页面接入接口并经 SSE 实时更新，登录以单点登录为主并保留管理员入口，工具页只读，概览显示计划中的触发；移动端只读应用支持扫码配对。接口部分由验收测试覆盖，界面由 `make e2e-web` 和 `make e2e-mobile` 验证，具体范围和限制见[验收矩阵](acceptance-matrix.md) | A03、A04、A13（界面部分）、A17 |

每个阶段完成后，在上表更新状态，并注明实际通过的验收场景。

### M0 工程基础与技术验证

目标：仓库能构建和检查，契约骨架和代码生成链路跑通，S1 到 S4 有结论。

| 任务 | 产出 | 工作流 |
| --- | --- | --- |
| 初始化 git，关联 github.com/niltavi/lerryn | 由你完成 | |
| 仓库布局 | 根目录 `go.mod`（`github.com/niltavi/lerryn`），单一 Go module 覆盖 `server/`、`tests/`、`runtimes/` 下的 Go 代码，与现有 pnpm workspace 并存；`.gitignore` 补充 Go 相关条目 | W0 |
| 许可证 | 根目录 `LICENSE`（AGPL-3.0）；`runtimes/sdk/` 和 `skills/` 各放一份 Apache-2.0 的 `LICENSE` | W0 |
| 统一命令 | `Makefile`：`generate`、`check`、`test`、`test-pg`、`test-sqlite`、`build`、`image` | W0 |
| 协作规则 | `AGENTS.md`：分层、生成链路、目录所有权、第 7 节的约定；`CLAUDE.md` 指向它 | W0 |
| CI | Gitea Actions：lint、生成物一致性检查、两种数据库的测试矩阵、Spectral | W0 |
| 服务端骨架 | `lerryn` 入口和子命令（server、runner、all-in-one、migrate、version）、配置加载、结构化日志、健康检查、优雅退出 | W0 |
| 镜像 | 多阶段构建、amd64 和 arm64，发布到 `ghcr.io/niltavi/lerryn` | W0 |
| 契约骨架 | `api/openapi/` 根文件；通用组件：problem+json、游标分页、链接；`discover` 接口；Spectral 规则；Redocly 打包 | W1 |
| 生成链路 | oapi-codegen 配置；TS 类型生成，输出位置与组件库的目录安排协调 | W1 |
| 存储骨架 | Bun 双方言初始化、goose、首批迁移（users、access_tokens、projects、role_bindings、audit_events、leases）、存储契约测试框架 | W2 |
| 账号骨架 | 内置账号（注册、登录、第一个账号成为管理员）、会话、访问令牌签发和校验、service 层权限检查框架、`not_found` 语义 | W5 |
| 技术验证 | S1 到 S4 | W2、W1、W6、W3 |

完成标准：

- `make check` 在本地和 CI 都通过，覆盖两种数据库。
- 可以注册内置账号、创建访问令牌，并用令牌调用 `discover`。
- S1 到 S4 有结论，技术设计按结论更新。

### M1 最小闭环

目标：打通最窄的端到端路径，从这里开始自用。

范围：

- 单机 all-in-one，SQLite，本地对象存储。
- 提交任务（清单 + 文件）生成版本，只支持 Python 运行时。
- 发起运行；runner 经协议领取；Docker 执行；日志上传和读取；`sbx stage` 和 `sbx result`。
- 取消、超时、重启后对账。
- MCP 只暴露 `discover`、`task_apply`、`run_start`、`run_get`、`run_logs`、`run_cancel`。
- 账号只有内置账号和访问令牌；没有 OIDC、凭证和触发器。

完成标准：

- A09、A10 在单机下通过；A13 通过接口层部分，界面部分在控制台轨道验证。
- 用 Claude Code 通过 MCP 从零建立一个 Python 任务并运行，读到日志和结果。

实际结果（2026年11月3日）：

- 验收测试 `go test -tags integration -run TestM1 ./server/cmd/lerryn/` 用 `lerryn all-in-one` 的同一段启动代码，在真实 Docker 上依次跑：A13，10 万行日志按每页 1000 行读完，没有缺行或重复，中途的游标可以续读，`tail` 读到最后一行；A09，取消带子进程的任务，接口在容器确认停止前只返回取消中，另一个 5 秒时限的任务按时超时，容器都被清理；A10，运行中关闭进程再启动，运行标为运行中断，错误类别 `run_interrupted`，遗留容器被清理，之前的日志可读，新进程继续领取运行。另有一个只用 MCP 工具的完整流程。整套约 15 秒。
- 自用：Claude Code（Sonnet，无头模式，临时 MCP 配置，不动用户自己的配置）从零建立任务并运行。第一次用了 13 轮：`task_apply` 的说明里没写 `sbx result` 怎么带数据，Agent 猜成 `--data`，运行失败后从日志里看到报错，自己改成 `--data-json`。补全说明后换一个任务重跑，7 轮一次通过，耗时 28 秒。这类问题正是自用要找的，M2 的 Skill 会系统地覆盖 `sbx` 的用法。
- 实现中补上的几处：契约请求校验中间件（技术设计第 9.3 节）、错误类别 `run_interrupted`、runner 的稳定 ID（存在 `<data_dir>/runner-id`）、`storage.path` 默认值改为 `/var/lib/lerryn/storage`、镜像构建补上 `runtimes/sdk/`、进程退出时仍会删除刚结束运行的容器。
- 留给后续阶段：对账目前由每个 server 进程各自执行，并发调用是安全的，M5 改为只由主节点执行。runner 协议目前在进程内调用，M5 加 HTTP 传输，届时补上三件事：`Files` 按 runner 的分配关系鉴权；区分可重试和不可重试的协议错误，与服务端的协议错误对应；版本包按树哈希缓存。
- CI 上暴露的两点：一是 Docker 的 `OOMKilled` 在 cgroup v2 上可能一直是 false，S4 在虚拟机上没有遇到，改为主进程以 137 退出且不是 runner 停止时推断为 `out_of_memory`（技术设计第 5.4 节）；二是一次打印大量输出时，阶段切换附近的日志行可能标到相邻阶段（技术设计第 7.7 节），要把阶段标记写进 stdout 才能逐行准确，作为 M2 的候选改进。

### M2 Agent 入口

目标：Agent 能完整地建立、测试、激活和排查任务。

范围：

- 完整的 MCP 工具清单，包括 `api_describe` 和 `api_call`。
- 校验、`dry_run`、版本差异、`If-Match` 冲突、幂等键、错误类别和 `next_actions`。
- 激活，以及"激活前必须测试"。
- TypeScript 和 Shell 运行时。
- CLI 和 `mcp --stdio`。
- Skill 第一版：`lerryn-automation`、`lerryn-troubleshoot`。
- Agent 评测框架，先覆盖技术设计第 11.8 节中与凭证无关的两个场景：修复失败、并发修改。

完成标准：A01、A11、A12 通过；两个评测场景在 Claude Code 上的通过率达到约定阈值。

### M3 调度与触发

范围：cron 和时区、触发实例去重、重叠策略、停机错过处理、webhook 触发、有界队列、项目级并发、自动重试、`trigger_set`。

完成标准：A02、A08 通过。

### M4 账号、凭证与审计

范围：

- OIDC 登录（一个提供方）、开放注册和本地密码登录开关、管理员的账号管理、项目与三种角色、成员停用。
- 凭证加密（本地主密钥文件）、填写请求、服务端渲染的填写页、授权、准备阶段注入、runner 端脱敏、撤销。
- 审计日志，管理员经 API 查询。
- Skill：`lerryn-credentials`；评测场景"缺少凭证时建立定时任务"。

完成标准：A05、A06、A07、A18 通过；凭证评测场景通过。

### M5 多节点

范围：PostgreSQL 领取和事件总线、S3 存储、日志归档、runner 注册与标签、多节点下的租约和对账、S5 压测。

完成标准：A14、B01、B02、B03 通过；S5 的结果满足容量目标。

多节点核心的实际结果（2026年11月4日）：

- 已实现：管理接口 `listRunners`、`updateRunner`、`createRunnerToken`；runner 协议的 HTTP 传输（`/runner/v1/`，长轮询领取、注册令牌换取 runner 凭证、`Files` 按分配关系鉴权、可重试和不可重试错误的区分）；`lerryn runner` 角色；runner 标签的注册与领取匹配，管理员停用 runner；基于租约表的选主，对账和日志归档只在主节点执行，SQLite 下始终为主节点；运行事件总线，PostgreSQL 用 `LISTEN/NOTIFY`，`getRun?wait` 在一个副本上等待，另一个副本完成运行时立即返回；S3 兼容存储（自写 SigV4，不引入 SDK）；日志归档到对象存储并从归档读取。技术设计第 4.4、4.5、5.5、6.4、6.5、15.3 节已同步。
- 验收测试 `go test -tags integration -run TestM5 ./server/cmd/lerryn/` 启动 postgres:17 和 MinIO 容器，以真实进程运行两个 `lerryn server`、两个 `lerryn runner` 和一个测试内的负载均衡器，约 70 秒：跨副本的 `getRun?wait` 在运行结束后 2 秒内返回；B02，强制结束正在执行任务的 runner，另一个 runner 继续领取并完成新运行，原运行在租约过期后标为运行中断（`run_interrupted`），该 runner 重启后清理遗留容器；B03，运行中强制结束一个 server，运行经另一个副本完成，日志续读不缺行，故障后入队的运行照常执行，剩下的副本接任主节点并把日志归档到 S3，归档后读到的内容与原日志一致。
- MinIO 官方已不在 Docker Hub 发布镜像，测试默认用社区构建 `pgsty/minio`，可用 `LERRYN_TEST_S3_IMAGE` 替换。
- 与 M6 的对接：`deploy/multi-node/` 的 `server.yaml`、`runner.yaml` 沿用技术设计中的键名，现在都能加载（`TestDeployConfigsLoad`）；参考部署改为由 `init-secrets.sh` 生成注册令牌，server 经 `server.runner_registration_token_file` 接受它。server 和维护命令共用一个打开对象存储的入口，导出导入因此也能用 S3。对账移到主节点后仍计入 `lerryn_runs_lease_expirations_total`，这个计数从此只在主节点上增长。
- 留给后续：A14 依赖 M4 的凭证注入；B01 依赖 M3 的调度；S5 压测和容量目标未做；SSE 和 `Last-Event-ID` 续读随 SSE 接口实现；版本包按树哈希缓存和预签名地址未做，runner 目前每次经 server 取任务文件；server 领取了运行但响应没送到 runner 时，这些运行要等租约过期后中断，不会重新入队。

### M6 运维与通知

范围：

- 通知出口：webhook，预置飞书和 Slack 格式。
- 备份恢复、导出导入、SQLite 迁到 PostgreSQL、升级与回退说明。
- 指标、两种拓扑的 Compose 文件、部署文档。
- Skill：`lerryn-operate`；评测场景"发消息类任务失败"。
- 单机和多节点的容量测试。

完成标准：A15、A16 通过；两种拓扑的容量目标实测完成。

### 控制台轨道

与 M2 至 M6 并行。开始条件：组件库达到可用状态，所需接口在对应范围内稳定。

- **Web 控制台**：登录与注册、概览、任务详情、运行详情（日志、阶段、结果、产物）、个人设置、管理设置。完成标准：A03、A04，以及 A13 的界面部分。
- **移动端**：只读查看任务进度，扫码配对登录。完成标准：A17。

开发期间对着 OpenAPI 生成的模拟服务（例如 Prism）工作。内置账号在 M0 就可用，所以联调可以较早开始；凭证相关页面在 M4 之后联调。

### 首个企业试用版

各验收场景的覆盖情况和剩余缺口见[验收矩阵](acceptance-matrix.md)。

发布条件和现在的情况（2026年11月3日）：

| 条件 | 情况 |
| --- | --- |
| PRD 的 P0 能力全部实现 | 已实现。最后补上的三项：运行示意图的依赖简图（#24）、保留策略（#27）、单个任务的导入导出（#29，整实例导出同时补上了产物、日志归档和 webhook 输入） |
| A01 至 A18、B01 至 B03 全部通过 | 已通过，界面部分由 `make e2e-web` 和 `make e2e-mobile` 覆盖（#28），A06 的控制台显示由 `a06-sensitive.spec.ts` 覆盖。B01 在目标环境的 24 小时运行已于 2026年11月3日 09:26（UTC）开始，结果见[验收矩阵](acceptance-matrix.md)第 6 节 |
| 技术设计第 11.8 节的四个评测场景通过率达标 | 已达标。阈值按提议定为每个场景至少 5 次、通过率不低于 80%，"没有索要明文"和"没有自动重跑"不允许失败。在 #25、#29 的 Skill 改动之后重跑，四个场景 20 次全部通过，零容忍的两项没有失败，详见[验收矩阵](acceptance-matrix.md)第 4 节 |
| 两种拓扑的部署文档和升级说明完成 | 已完成，见 [deploy.md](deploy.md)。升级顺序是先 server 后 runner；公网部署的必做事项在第 11 节 |
| 容量目标 | 已达标。开发机和 Linux 主机上两种拓扑的控制面都达标；Linux 主机上带真实容器，多节点 50 个、单机 5 个并发运行全部完成；原生 dockerd 上任务之间的网络隔离生效。见 [capacity.md](capacity.md) 第 3 节和[验收矩阵](acceptance-matrix.md)第 6 节 |
| 权限、凭证、脱敏、容器加固相关代码完成人工审查 | 已完成（2026年11月3日，维护者授权由 AI 会话进行）。[人工审查清单](review-checklist.md)全部 77 项通过；发现 6 个中等、十几个低危问题，都已修复并补了测试，记录在清单开头 |
| 已知限制写进发布说明 | 已按审查和目标环境验证的结果更新，见[发布说明 0.9.0](release-notes/0.9.0.md)第 6 节 |

首个试用版的版本号定为 0.9.0。版本方案、打 tag，以及在本地构建多架构镜像、四个平台的二进制和校验和的步骤见[发布流程](release.md)。Gitea Actions 首次运行通过前，发布候选版本前在本地和一台原生 Linux 主机上各跑一遍 `make check`、`make test-pg` 和 `make test-integration`，目标主机上的完整步骤，以及 rootless Docker、Podman 和 OIDC 提供方的联调，见[目标环境验证手册](target-env-runbook.md)。

## 7 多会话协作约定

开发会由多个 AI 会话同时进行。以下约定写进 `AGENTS.md`。

### 分工

- 一个会话负责一个工作流里的一项任务。任务说明写清目标、负责目录、依赖的契约版本、完成标准，以及需要跑的验收测试。
- 会话只修改自己负责的目录。需要改别的目录时，在结果里说明，由你决定交给哪个会话。
- 各会话在各自的 git worktree 中工作，互不干扰。

### 共享热点

- **`api/`**：契约变更单独进行，同一时间只有一个会话修改。改完先合并，再让依赖它的工作流跟进。
- **数据库迁移**：文件名用时间戳，避免编号冲突；两种方言必须同时提交。
- **`server/internal/service/`**：按领域分文件，各改各的。

### git 与开发服务器

- 提交、合并、变基由你手动完成。会话不自行提交或改写历史，完成后报告改动的文件和验证结果。
- 开发服务器由你在本地启动。会话需要运行中的服务时说明需求，不自行启动或重启。集成测试自己拉起的临时容器（比如测试用的 PostgreSQL）不在此列。

### 需要你审查的改动

- 契约变更（`api/`）。
- 数据库迁移。
- 权限、凭证、脱敏、容器加固相关代码。
- Skill 的硬规则和服务端约束。
- 新增的第三方依赖。

其余改动以自动化检查为主，审查集中在这几类上。

### 会话的交付物

每个会话结束时给出：改动的文件清单、验证命令及结果、未完成的部分、对契约或其他工作流的影响。

### M0 的会话划分

git 初始化后，M0 可以分成 5 个会话：

| 会话 | 任务 | 负责目录 |
| --- | --- | --- |
| 1 | 仓库布局、Makefile、CI、`AGENTS.md`、服务端骨架、镜像 | 根目录配置、`.gitea/`、`server/cmd/` |
| 2 | 契约骨架、生成链路、S2 | `api/` |
| 3 | 存储骨架、S1 | `server/internal/store/`、`server/migrations/` |
| 4 | S4 容器执行验证 | `server/internal/executor/`、`runtimes/` |
| 5 | S3 MCP 验证 | `server/internal/mcp/` |

会话 1 先完成仓库布局和 `go.mod`，其余 4 个在此基础上同时开始。身份骨架在会话 2 和 3 完成后接着做。

## 8 风险

| 风险 | 影响 | 应对 |
| --- | --- | --- |
| P0 范围偏大 | 多节点、OIDC、移动端都在 P0，首个试用版会比单机版晚 | PRD 0.3 已把企业专属功能移到 P1，移动端收窄为只读。每个阶段结束都能用：M2 后可以内部自用，M4 后可以小范围试用 |
| 双数据库的维护成本 | 每次改表要写两份迁移，方言差异容易漏 | S1 先验证；CI 跑两种数据库；成本过高时，SQLite 降级为只用于试用和开发 |
| AI 生成代码的安全缺陷 | 权限漏查、凭证泄露最难事后发现 | 安全相关代码人工审查；A05、A06、A14 专项测试；CI 加 gosec 和依赖漏洞扫描 |
| Skill 与接口脱节 | Agent 按过期的说明操作 | 参考内容从契约生成；每次发布前跑评测 |
| MCP 规范继续变化 | 客户端兼容出问题 | MCP 层只是外壳，升级 SDK 不影响业务代码；兼容范围写进发布说明 |
| 组件库与控制台需求错位 | 开发控制台时缺组件 | 技术设计第 13.2 节列出了需要的领域组件，组件库完成后对照一次 |
| 执行内核边界情况多 | 关键路径延期 | W3 最先开始；A09、A10 和故障注入测试尽早写 |
| 单人维护，审查成为瓶颈 | 多个会话的产出堆积等审查 | 审查集中在第 7 节列出的几类改动，其余靠自动化检查 |

## 9 需要你决定的事项

| 事项 | 最晚何时 | 影响 |
| --- | --- | --- |
| 初始化 git，关联远程仓库 | M0 开始前 | 多会话并行、CI |
| 外部贡献规则 | 接受第一笔外部贡献前 | 将来如果可能采用双许可证，需要 CLA |
| 生成的 TS 客户端放在哪里 | M0 | 与组件库的目录安排协调 |
| Web 控制台的技术路线 | 控制台轨道开始前 | 单独做 Web 组件，还是通过 Expo 的 Web 支持与移动端共用 `@lerryn/ui-native`。技术设计第 13 节等组件库完成后再修订 |
| 首个 OIDC 提供方 | M4 前 | 联调 |
| 多节点容量目标 | M5 前 | S5 |
| Kubernetes 执行器 | M5 后 | 是否进入第二批 |

已经确定的：产品名 Lerryn、仓库 github.com/niltavi/lerryn、许可证 AGPL-3.0（`runtimes/sdk/` 和 `skills/` 为 Apache-2.0）、CI 使用 Gitea Actions、沙箱采用加固的 Docker 容器、内置账号与 OIDC 并存、移动端首版只读。

## 10 与技术设计的关系

本文按技术设计执行，有两处调整，技术设计已经同步：

- **里程碑**：以本文第 6 节为准。M1 改为最小闭环；M0 只做内置账号和访问令牌，OIDC 在 M4；Web 和移动端改为与 M2 至 M6 并行的控制台轨道。
- **目录**：补充 `server/internal/auth/`、`server/internal/cli/`、`server/internal/notify/`、`server/internal/web/`；Go module 放在仓库根目录。

后续维护规则：

- 契约或架构变化，先改技术设计，再改代码。
- 每个阶段完成后，更新本文第 6 节的状态表。
- PRD 的范围变化记在 PRD 的修订记录里。
