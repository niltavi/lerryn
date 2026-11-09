# Agent 自动化沙箱技术设计初稿

版本：0.4 草案　日期：2026年11月3日　对应 PRD：0.3 草案

本文是 [PRD](agent-automation-sandbox-prd.md) 的技术设计，实施顺序和阶段划分见[实施规划](agent-automation-sandbox-implementation-plan.md)。

产品名为 Lerryn，代码托管在 [github.com/niltavi/lerryn](https://github.com/niltavi/lerryn)，以 AGPL-3.0 开源。命令行工具、镜像和 Skill 前缀都用 `lerryn`。

0.2 版按几项已经确定的决策做了收敛：首版面向企业自部署，但企业专属的功能尽量少做；内置一套门槛很低的账号体系，与 OIDC 并存；沙箱只用加固的 Docker 容器，不做工具网关和更强的隔离方案；移动端只用来查看任务进度。推迟到 P1 的功能集中列在第 19 节。

0.3 版按 M0 的四项技术验证修订，验证记录在 `docs/zh-CN/spikes/` 下（[S1 双数据库](spikes/s1-dual-dialect-storage.md)、[S2 OpenAPI 生成链路](spikes/s2-openapi-codegen.md)、[S3 MCP 无状态模式](spikes/s3-mcp-stateless.md)、[S4 容器执行](spikes/s4-container-execution.md)），同时补上验证合并后已经实现的事实，以 `main` 上的代码为准。0.4 版按 M1 最小闭环的实现修订。各版本的变化见第 23 节。

文档按这个顺序展开：前提和选型（第 1 至 3 节），架构、数据和执行（第 4 至 8 节），对外接口、MCP 和 Skill（第 9 至 11 节），账号、权限、前端和运维（第 12 至 16 节），最后是验收对应、里程碑、首版从简清单、已知限制、待确认事项、参考和修订记录。

文中库和协议的版本状态核对于 2026年11月；容量和性能数字都是待实测的目标，不是结论。

## 1 设计前提

| 前提 | 含义 |
| --- | --- |
| 企业自部署 | 每家企业部署自己的实例，不存在由项目方运营的中心服务器。控制面不依赖外部云服务、许可证校验或遥测 |
| 首版从简 | 企业专属功能（MCP OAuth、外部密钥服务、用户组同步、审计界面等）放到 P1，首版先让一个团队用顺 |
| 部署内多节点 | API 服务可以多副本，执行节点可以横向扩展。单机部署是多节点的特例，不是另一套实现 |
| Docker 交付 | 平台本身以容器镜像发布，任务也在容器中执行 |
| 两种数据库 | PostgreSQL 用于标准部署和多节点；SQLite 用于单机、试用和开发 |
| 低门槛账号 | 内置账号只需用户名和密码即可注册；同时支持 OIDC 单点登录；管理员可以关闭开放注册 |
| 契约优先 | HTTP API 以 OpenAPI 描述；MCP、CLI、前端客户端都由同一份契约派生 |
| Agent 优先 | 任务的建立和维护经 API／MCP 完成；随版本发布一套 Skill 引导 Agent 正确使用 |
| 前端 | Web 用 React，移动端用 React Native；UI 基于 HeroUI 的变形版本和新组件库 |
| 开源许可证 | AGPL-3.0；供任务代码引用的辅助库和 Skill 为 Apache-2.0，见第 16.2 节 |
| 可维护性 | 代码主要由 AI 编写和维护，选型优先考虑编译期检查、写法统一和反馈速度 |

## 2 技术栈总览

| 层 | 选择 | 说明 |
| --- | --- | --- |
| 后端语言 | Go | 理由见第 3 节 |
| API 契约 | OpenAPI 3.1 | 规范文件是接口的唯一来源，写法约定见 `api/README.md` |
| 服务端代码生成 | oapi-codegen v2.8.2（v2.8 起支持 3.1） | 生成 strict server 接口，新增接口不实现就无法编译。配置开启 `skip-prune`、`always-prefix-enum-values` 和 `ToCamelCaseWithInitialisms`，理由见第 9.1 节 |
| OpenAPI 解析 | kin-openapi v0.152.0 | 与 oapi-codegen 用同一个库，MCP 工具注册表、请求校验和契约测试都用它读取规范。版本不低于 v0.144.0，以避开 GO-2026-6112、GO-2026-6095 |
| 前端客户端生成 | openapi-typescript + openapi-fetch | 生成到 `packages/api`（`@lerryn/api`），Web 和移动端共用 |
| MCP | 官方 Go SDK v1.8.0（v1.7 起支持 2026-07-28 规范） | Streamable HTTP，无状态模式，同时兼容旧版协议的客户端 |
| 数据访问 | Bun | 一套查询代码适配 PostgreSQL 和 SQLite。驱动为 pgx 和纯 Go 的 modernc.org/sqlite，不需要 cgo |
| 数据库迁移 | goose | 每种方言一套 SQL 迁移，嵌入二进制 |
| 数据库 | PostgreSQL／SQLite | 见第 6 节 |
| 对象存储 | S3 兼容存储／本地磁盘 | 日志归档、产物、任务版本包 |
| 容器 | Docker Engine API | 官方 Go 客户端 `github.com/moby/moby/client`。Podman 提供兼容 API，计划可以替换，尚未验证（第 21 节） |
| 任务运行时 | Python + uv、TypeScript + Bun、Bash | 运行时目录可扩展 |
| 身份 | 内置账号（Argon2id）+ OIDC（coreos/go-oidc） | 见第 12.1 节 |
| 实时推送 | SSE | 断线后按 `Last-Event-ID` 续读 |
| Web | Vite、React、TypeScript、TanStack Router／Query、HeroUI v3 变形版 | 构建产物内嵌进服务端二进制 |
| 移动端 | Expo、Expo Router、HeroUI Native 变形版 | 与 Web 共享客户端、hooks 和设计令牌 |
| 仓库 | 单仓库：根目录 Go module `github.com/niltavi/lerryn` + pnpm workspace | 见第 16 节 |
| CI 与镜像 | Gitea Actions；镜像发布到 `ghcr.io/niltavi/lerryn` | |
| 可观测 | slog（JSON 日志）、Prometheus 指标 | OpenTelemetry 放到 P1 |

## 3 后端语言

### 3.1 评估标准

"便于 AI 编写和维护"拆成几条可以比较的标准：

- 编译器能拦住多少错误。AI 写错的代码越早暴露，返工越少。
- 写法是否统一。不同会话、不同时间写出的代码风格越接近，越好维护。
- 构建和测试够不够快。AI 每改一次就要跑一次检查。
- 生态里的"魔法"多不多。反射、装饰器、隐式注入越多，AI 越容易写出看似正确、运行出错的代码。

再加上这个产品自己的要求：进程和容器控制、多节点下的并发、镜像体积和内存占用、MCP SDK 的成熟度。

### 3.2 对比

| | Go | TypeScript | Python | Rust |
| --- | --- | --- | --- | --- |
| AI 一次写对的概率 | 高。语法少，gofmt 统一格式 | 高，但框架和写法流派多 | 高，但很多错要到运行时才暴露 | 中。借用检查常需反复修改 |
| 编译期兜底 | 强 | 中。`any` 可以绕过，运行时还要另做校验 | 弱 | 很强 |
| 进程、信号、容器控制 | 强。Docker 客户端本身就是 Go | 能做 | 能做 | 强 |
| 发行和镜像 | 静态二进制，镜像几十 MB | 需要 Node 或 Bun 运行时 | 需要完整解释器环境 | 静态二进制 |
| 多节点下的并发和内存 | goroutine，占用低 | 单线程事件循环，占用中等 | 占用中等 | 占用最低 |
| MCP SDK | 官方，v1.7 已支持 2026-07-28 规范 | 官方，通常最先跟进新规范 | 官方 | 官方 |
| 与前端共享类型 | 经 OpenAPI 生成 | 可直接共享 | 经 OpenAPI 生成 | 经 OpenAPI 生成 |

### 3.3 结论

选 Go。最难写的部分是执行节点、进程和容器生命周期、流式日志、多节点协调，这些正好是 Go 擅长的；单个静态二进制和较低的内存占用，也让 Docker 交付和横向扩展更省事。

TypeScript 的优势是前后端一种语言。本方案用 OpenAPI 生成前端类型和客户端，这部分优势已经覆盖大半。代价是前后端之间多了一步生成，需要 CI 保证契约和生成物一致。

## 4 系统架构

### 4.1 组件与角色

平台发布一个镜像，启动时选择角色：

| 角色 | 职责 | 扩展方式 |
| --- | --- | --- |
| server | REST API、MCP、SSE、控制台静态资源、凭证填写页、账号与权限、凭证管理。通过租约选出一个主节点，负责调度、对账、日志归档和通知 | 无状态，放在负载均衡后增加副本 |
| runner | 向 server 领取运行，准备环境，调用本机 Docker 启动容器，回传日志、事件和产物 | 按机器增加；用标签分组，可部署在能访问内网系统的网段 |
| all-in-one | 同一进程内运行 server 和 runner | 单机部署 |

all-in-one 模式下，server 和 runner 之间仍然走第 4.5 节的 runner 协议，只是不经过网络。这样单机和多节点只有一条代码路径。

同一个 `lerryn` 二进制还提供 CLI 子命令（第 10.6 节），供 Agent 和维护者使用。

### 4.2 架构图

```
         Agents (MCP / REST / CLI)      Web / Mobile
                      └──────────┬──────────┘
                           Load Balancer
                                 │
            ┌────────────────────┼────────────────────┐
         server               server               server
            └────────────────────┼────────────────────┘
                 ┌───────────────┼───────────────┐
                 │               │               │
            PostgreSQL     S3-compatible      runners
            (or SQLite)   (or local disk)        │
                                          Docker / Podman
```

- server 之间不直接通信，共享状态都在数据库和对象存储里。
- runner 只通过 HTTPS 访问 server，不持有数据库和对象存储的管理凭证。
- 每次运行在 runner 所在主机上启动一个独立容器。

### 4.3 部署拓扑

| 拓扑 | 组成 | 数据库 | 文件存储 | 适用 |
| --- | --- | --- | --- | --- |
| 单机 | 1 个 all-in-one 容器 | SQLite 或 PostgreSQL | 本地卷 | 个人、小团队、试用 |
| 标准 | 2 个以上 server、若干 runner | PostgreSQL | S3 兼容存储 | 企业部署 |
| 扩展（P1） | 标准拓扑 + Kubernetes 执行器 | PostgreSQL | S3 兼容存储 | 已有 Kubernetes 平台的企业 |

数据库和对象存储的高可用由企业按自己的方案提供，平台不内置。

### 4.4 多节点协调

不引入 Redis 或消息队列，需要协调的地方都落在数据库上。

**运行队列。** 待执行的运行就是 `runs` 表中状态为排队中的记录。runner 领取时，server 在一个事务里选出符合条件的运行并改为准备中。PostgreSQL 用 `FOR UPDATE SKIP LOCKED`，多个 server 并发领取互不阻塞；批量领取写成 `WITH picked AS MATERIALIZED (SELECT … FOR UPDATE SKIP LOCKED LIMIT n) UPDATE … FROM picked`，物化保证加锁的子查询只执行一次。SQLite 只有一个写入者，领取事务必须以 `BEGIN IMMEDIATE` 开始，并在进程内串行执行，原因见第 6.1 节。

**标签匹配。** 任务可以声明 `runner_labels`，比如要求运行在 `intranet` 网段的 runner 上。标签存在关联表里，匹配条件用两种数据库都支持的 `NOT EXISTS` 写法，不依赖 PostgreSQL 数组：运行入队时把清单的 `runner_labels` 写入 `run_required_labels`，runner 注册时把 `runner.labels` 写入 `runner_labels`（每次注册整体替换），领取时只选出所需标签全部在该 runner 上的运行。拿不到的运行不挡住后面的运行。标签为 1 到 63 个小写字母、数字、`-`、`_` 或 `.`，以字母或数字开头，两种数据库比较结果一致。管理员停用的 runner 不再领取新运行，手上的运行照常续租直到结束，用于下线前排空。

**同一任务不重叠。** 用部分唯一索引保证（第 5.5 节的 `concurrency_key`）：对 `concurrency: forbid` 的任务，同一任务在排队中、准备中、运行中、取消中这几个状态下最多一条记录。PostgreSQL 和 SQLite 都支持部分索引，这条规则由数据库兜底，不靠应用代码自觉。

**运行租约。** runner 领取运行后每 2 秒续租一次（`runproto.HeartbeatInterval`），租约 30 秒，到期时间按数据库时钟计算（SQLite 用应用时钟，单机下是同一个时钟）。租约过期说明 runner 可能失联，由对账任务把运行标为运行中断。过期之后、对账处理之前，只要运行仍处于活动状态、runner 也匹配，续租仍然成功，短暂的网络抖动不会中断一次健康的运行。对账和续租都是单行 UPDATE，PostgreSQL 上由行锁串行，先到的一方生效：对账先到时，运行已经是运行中断，runner 下一次续租失败，按 server 的指令停止容器。runner 恢复后上报本机仍存在的容器，server 回复处理方式。

**选主。** 调度、对账、日志归档、通知这几类后台工作由主节点执行。PostgreSQL 下用一张租约表选主，租约时间以数据库时钟为准，避免节点之间的时钟偏差：

```sql
INSERT INTO leases (id, name, holder, expires_at)
VALUES ($1, 'leader', $2, now() + interval '15 seconds')
ON CONFLICT (name) DO UPDATE
SET holder = excluded.holder, expires_at = excluded.expires_at
WHERE leases.holder = excluded.holder OR leases.expires_at < now()
RETURNING expires_at;
```

返回一行即为主节点。一条 upsert 同时完成首次创建、获取和续期；只写 UPDATE 的话，表里还没有这一行时永远不会成功，还需要另一步预先插入。SQLite 用同样的写法，时间取应用时钟。主节点正常退出时删除自己的租约，其他节点不必等到过期。SQLite 模式只有一个 server，启动即为主节点。

实现在 `server/internal/leader`。每个 server 进程启动时生成一个随机的持有者 ID，每 5 秒尝试获取或续期一次，租约 15 秒。主节点续期失败时不马上退位，但如果按本机单调时钟计算，距上次成功续期已经达到租约减去一个续期间隔（10 秒），就主动退位，保证在租约可能转给别人之前停下后台工作；续期时发现租约已被其他节点持有，也立即退位。主节点异常退出后，其他节点最晚在租约到期后一个续期间隔内接任，正常退出时在一个续期间隔内接任。M5 起对账和日志归档只在主节点执行，调度和通知随 M3、M6 接入同一个入口：`leader.Elector.Run(ctx, lead)` 在每次当选时调用 `lead`，失去主节点身份或进程退出时取消它的 context；`leader.All(tasks...)` 把多个循环合成一个 `lead`；`IsLeader()` 报告当前是否为主节点。`cmd/lerryn` 的 `leaderTasks` 列出主节点上的全部循环。

选主只是为了避免重复工作，正确性不依赖它。调度实例由唯一约束去重（第 8.2 节），即使短时间出现两个主节点，也不会产生重复运行。

**事件通知。** 运行状态变化、新日志到达等事件需要通知所有 server 上的 SSE 连接。PostgreSQL 用 `LISTEN/NOTIFY`，载荷只带运行 ID 和序号，内容由各 server 自己查询；SQLite 模式用进程内广播。事件总线是一个接口，将来确有瓶颈时可以增加 NATS 或 Redis 实现。

M5 实现了两类事件（`server/internal/events`）：`run.queued` 唤醒正在长轮询领取的 runner，`run.ended` 唤醒等待运行结束的 `getRun?wait`。PostgreSQL 上每个 server 用一条独占连接 `LISTEN`，断开后退避重连；发布时经连接池执行 `pg_notify`，同时直接投递给本进程的订阅者，所以本进程的事件可能收到两次，两类事件都可以重复。频道名由当前 schema 派生，同一个库里不同 schema 的部署互不干扰。连接断开期间的通知会丢失，订阅方都另有轮询兜底：有跨节点总线时 `getRun?wait` 每 5 秒查一次，领取每 2 秒重试一次，丢失只影响延迟。

运行变化的事件流（`streamEvents`）用第三类事件 `run.changed`。事件带发布进程的标识（`o`），本进程的变化直接交给本地订阅者，经总线回到自己时跳过，所以本地不会收到两次；其他 server 收到后自己读取运行状态，推给本机的流。只有跨节点总线时才发布 `run.changed`，单机不产生多余的通知。触发器入队的运行只发 `run.queued`，不带发布进程标识，各 server 收到后同样推给本机的流。断线期间丢失的变化没有补发，客户端靠 `resync` 和重连后重新读取列表恢复。

### 4.5 runner 协议

runner 和 server 之间是一组 HTTPS 接口，挂在 server 的 `/runner/v1/` 下，不对外公开。协议的调用定义在 `server/internal/runproto`（`Coordinator` 接口），HTTP 传输在 `server/internal/runnerhttp`：server 端的处理器调用 service 层的同一个 `Coordinator`，runner 端的客户端实现同一个接口，所以 all-in-one 的内嵌 runner 和独立部署的 runner 是同一份代码。

线上格式定义在 `runnerhttp` 的 Go 类型里，没有像早先设想的那样写成 `api/runner.yaml` 再生成代码。原因：两端都在同一个二进制里发布；协议不对外，不需要给其他语言生成客户端；兼容性靠协议版本号和 JSON 字段名保证，改字段必须同时升版本号。将来需要其他语言的 runner 时，再补一份 OpenAPI 描述。

| 接口 | 作用 |
| --- | --- |
| `POST /runner/v1/register` | 用注册令牌换取 runner 凭证；已有凭证时更新名称、容量和标签 |
| `POST /runner/v1/claim` | 领取运行，返回运行规格：任务版本、资源限制、网络策略、参数。没有可领取的运行时最多挂起 30 秒（runner 默认请求 10 秒），新运行入队的事件到达时立即重试 |
| `POST /runner/v1/heartbeat` | 上报正在执行的运行并续租；响应中带回要取消的运行和已不属于该 runner 的运行 |
| `GET /runner/v1/versions/{id}/files` | 取任务版本的文件。只有该 runner 手上有这个版本的活动运行时才返回，否则按租约丢失处理 |
| `POST /runner/v1/runs/{id}/started` | 容器已启动，运行进入运行中 |
| `POST /runner/v1/runs/{id}/logs` | 批量上传已脱敏的日志，按序号幂等 |
| `POST /runner/v1/runs/{id}/events` | 阶段事件和结果 |
| `POST /runner/v1/runs/{id}/finish` | 终态 |
| `POST /runner/v1/runs/{id}/secrets` | M4：准备阶段领取本次运行需要的凭证。server 校验运行确实分配给该 runner、处于准备中、凭证授权有效 |
| `POST /runner/v1/runs/{id}/input` | 领取运行输入（webhook 的请求体和媒体类型）。只有领取时 `has_input` 为真的运行才调用；server 校验运行分配给该 runner 且仍在执行，内容按哈希核对 |
| `GET /runner/v1/runs/{id}/state` | 协议 2：下载领取时固定的任务状态，要求归属和有效租约，响应禁止缓存 |
| `PUT /runner/v1/runs/{id}/state` | 协议 2：准备完整的输出候选，确认对象可用后标为 ready，由成功的 Finish 事务发布 |
| `POST /runner/v1/runs/{id}/artifacts` | M6：申请产物上传地址，或直接上传 |

**认证。** 管理员生成注册令牌（`wl_rrt_` 开头），也可以在 `server.runner_registration_token_file` 中配置一个固定令牌，用于还没有人登录时先部署 runner。runner 第一次启动时用注册令牌换取自己的凭证（`wl_rnr_` 开头），存在数据目录的 `runner-credential` 文件里（权限 0600），之后每次调用都以 Bearer 方式携带。数据库只保存两种令牌的 SHA-256。server 从凭证得出 runner 身份，不采信请求体里的 runner ID，一个 runner 不能替另一个 runner 上报，也不能用自己的凭证注册成别的 ID。用注册令牌注册一个已经存在的 runner ID 时，只有这个 runner 已经安静下来（超过一个租约周期没有心跳，也没有准备中、执行中或取消中的运行）才换发新凭证，旧凭证随即失效，凭证文件丢失时这样恢复；仍在工作的 runner 返回 409 `runner_id_in_use`，客户端按可重试处理，等旧的登记安静下来再换取。内嵌 runner 的 ID 一律拒绝。这样拿到注册令牌的人只能新增 runner，不能顶替正在工作的 runner、拿走它名下运行的凭证（首个试用版审查）。凭证不再被接受时（被换发或 runner 被删除），配置了注册令牌的 runner 会自动重新注册一次。管理员生成的注册令牌只能用一次，有效期 1 到 720 小时，默认 24 小时（`createRunnerToken`）；配置文件中的固定令牌可以反复使用，适合一组 runner 共用。注册信息无效时先拒绝，不消耗令牌。令牌被撤销不影响已经换发的凭证。管理员经 `listRunners` 查看节点的标签、版本、状态（online、offline、disabled）和占用，经 `updateRunner` 停用或恢复节点；只对管理员开放，其他用户得到 `permission_denied`。领取时 server 记下 `runs.claimed_at` 和 `runs.runner_name`，跳过 `not_before` 未到的重试。

**错误分类。** 处理器把 service 层的错误映射为三类，客户端映射回 `runproto` 的错误：调用违反协议（序号跳号、超过日志上限、标签无效、协议版本不支持，即 `service.ErrProtocol`，以及 403）返回 422，对应 `runproto.ErrRejected`，runner 不再重试；运行已不属于该 runner 返回 409，对应 `runproto.ErrLeaseLost`；其余错误（server 内部错误、503、代理的 502、网络错误）都可以重试。启动阶段的注册被拒绝时 `lerryn runner` 直接退出并说明原因，不无限重试。

**版本。** 请求头 `Lerryn-Runner-Protocol` 带协议版本，当前为版本 2。版本 1 已领取的运行仍可心跳、上报和结束，保留旧任务状态；新领取和状态接口只允许版本 2，登记版本和请求版本都要满足要求。版本 2 runner 遇到旧 server 会明确拒绝，不静默降级为没有状态目录的执行。首次上线须先完成所有 server 的升级，再恢复 runner 领取；不能让旧 server 继续发出不固定状态输入的运行，内嵌 runner 也不例外。具体维护顺序见 [部署文档](deploy.md)第 8 节。

**批量与响应上限。** HTTP 领取的客户端和服务端都将单批限制为最多 8 条，保证脚本工具文件经 base64 编码、路径和清单经 JSON 转义后仍在 256 MiB 的响应上限内。更多空闲槽位分批领取，runner 的并发槽位数不变；all-in-one 的进程内领取仍可一次领取 64 条。客户端检查声明长度并最多多读 1 字节，超限时报错，不解码被截断的响应。线上字段和协议版本不变。

runner 的 HTTP 请求编码保留结果 JSON 的原有字符表示，关闭供 HTML、JavaScript 嵌入使用的额外转义，避免 `<>&` 或 Unicode 行分隔符把合法的 `result.data` 扩大到 64 KiB 限制之外。字段编码沿用 v1 兼容选项，请求体和结果的现有大小限制继续生效。事件上报还按实际编码后的字节数拆分 HTTP 请求，每个请求不超过 16 MiB；最终集中读取或网络积压后的合法结果不会因合批过大被永久拒绝。子批按原顺序发送，失败后重发仍按事件序号去重。

脚本工具的文件在领取事务提交后读取，不持有数据库事务等待对象存储。读取和通知结束后，server 再用一次新的查询筛选仍归属该 runner、处于准备中或取消中且租约有效的运行，保持原领取顺序。读取失败也必须重新锁定运行并检查归属、状态和租约；写入失败状态、事件及释放引用后，事务末尾再次检查租约。租约失效时整笔事务回滚，包括失败状态、错误和结束时间、事件及引用释放，不发送终态通知。已结束、转属或失去租约的运行不被晚到的读取错误覆盖，也不重复发送终态通知。这个检查保证最后一次查询观察到的状态，不能保证网络送达时租约仍有效；runner 随后的状态下载仍须检查归属和租约。

已知不足：server 已经领取了运行、但响应没能送到 runner（连接在这一刻断开）时，这些运行要等租约过期后标为运行中断，不会重新入队。

### 4.6 一次运行的完整过程

1. Agent 调用 `run_start`，带幂等键。server 检查权限和版本，写入一条排队中的运行，返回运行 ID 和控制台链接。
2. runner 长轮询 `claim`。server 在事务中领取，把状态改为准备中，返回运行规格。
3. runner 下载版本包（按哈希缓存），准备环境（按环境 ID 缓存），下载任务状态目录，领取凭证。任何一步失败，上报执行失败和错误类别，流程结束。
4. runner 创建并启动容器，经容器的 stdin 交付凭证（第 12.5 节），上报运行中。之后读取输出，分行、脱敏、编号、分批上传；同时读取事件文件，上报阶段、结果和工具调用。执行器只交出按 stdout、stderr 分开的原始字节，按 daemon 送达的顺序写出；分行、截断和脱敏都在 runner 的日志管道里完成。两个流之间的相对顺序不保证与进程写入的顺序一致。
5. 进程退出后，runner 上传产物和状态目录，送完剩余的日志和事件，按退出码和 OOM 标记上报执行完成或执行失败（第 5.4 节），然后删除容器。遇到超时或取消时，先停止容器，确认停止后再上报已超时或已取消（第 7.6 节）。
6. server 写入终态并发出事件。主节点随后归档日志、按订阅发送通知。

整个过程中 runner 每 2 秒续租一次。客户端断开不影响以上任何一步。

## 5 领域模型

### 5.1 主要对象

| 对象 | 说明 |
| --- | --- |
| User | 成员身份。内置账号或 OIDC 首次登录时创建 |
| Identity | 用户与 OIDC 身份的关联 |
| Session | Web 和移动端的登录会话 |
| AccessToken | Agent 使用的访问令牌，绑定用户，带范围和有效期 |
| Project | 共享和授权的单位。每个用户有一个个人项目，默认只有本人可见 |
| RoleBinding | 用户在项目中的角色 |
| Task | 自动化任务，属于一个项目 |
| TaskVersion | 不可变的任务版本：清单 + 文件树。每次修改生成新版本 |
| Environment | 运行环境：运行时镜像 digest + 依赖锁文件，按内容哈希标识 |
| Trigger | 触发条件：cron 或 webhook，属于任务，独立维护 |
| TriggerFire | 一次计划触发实例，（触发器 ID，计划时间）唯一 |
| Run | 一次运行，固定关联任务版本和环境，记录触发来源和重跑关联 |
| RunEvent | 状态变化、阶段事件、结果、工具调用 |
| LogChunk／LogArchive | 运行中的日志分片，以及结束后的归档 |
| Artifact | 运行产物 |
| Credential | 凭证，属于用户，有多个加密版本 |
| CredentialGrant | 凭证所有者授权某个任务使用 |
| CredentialRequest | 凭证填写请求，绑定用户、用途和有效期 |
| Tool | 项目内登记的工具：外部 MCP 服务、HTTP API、脚本工具 |
| Runner | 执行节点，带标签和容量 |
| NotificationChannel | 通知出口和订阅 |
| Setting | 部署级设置，例如开放注册、本地登录 |
| IdempotencyRecord | 幂等键记录 |
| AuditEvent | 审计记录 |

### 5.2 任务、版本与激活

任务的生命周期：

1. Agent 提交清单和文件，平台生成一个新的不可变版本，同时返回校验结果，以及与当前激活版本的差异。
2. Agent 用该版本发起测试运行，检查日志和结果。
3. Agent 激活该版本。激活后，触发器只运行激活版本。
4. 后续修改重复以上步骤。旧版本保留，运行记录始终指向自己使用的版本。

这里不设单独的草稿状态。每次修改都是一个版本，激活才影响定时触发。比起"草稿 + 发布"，少一个状态，Agent 也更不容易搞混。

部署可以开启"激活前必须测试"：一个版本没有成功的测试运行，就不能激活。默认开启。"成功的测试运行"指带 `test` 发起、进程以 0 退出、并且没有用 `sbx result` 上报 `failed` 的运行；runner 上报终态时，服务端在同一事务里把第一次这样的运行的结束时间写进版本的 `tested_at`。归档的任务不能激活、运行或修改，同名的新提交建立一个新任务。

### 5.3 任务清单

Agent 提交的清单示例：

```yaml
name: weekday-digest
description: 工作日早上汇总指定系统的信息，调用模型生成摘要并保存
runtime:
  language: python
  version: "3.12"
  lockfile: uv.lock
entrypoint: ["python", "main.py"]
params:
  source: internal-crm
stages:                     # 可选。声明后控制台按此显示阶段
  - id: fetch
    name: 获取输入
  - id: summarize
    name: 调用模型
    after: [fetch]
  - id: save
    name: 保存结果
    after: [summarize]
credentials:                # 只写引用名，不含明文；绑定哪个凭证由授权决定
  - name: LLM_API_KEY
    as: env                 # 每个字段成为同名环境变量；file 时写入 /run/secrets/<name>/
tools: [crm-mcp]            # 允许使用的已登记工具
limits:
  timeout: 10m
  cpu: 1
  memory: 1Gi
network: full               # none 或 full
runner_labels: [intranet]
concurrency: forbid         # 同一任务不重叠
retry:
  max_attempts: 0           # 默认关闭自动重试
side_effects: true          # 声明有外部副作用，Agent 据此判断能否重试
panels:                     # 可选。Agent 面板，数据来自 sbx result 的 data
  - id: new-leads
    type: metric            # metric、bars、breakdown、table、text
    title: 新线索
    source: /new_leads      # data 中的 JSON Pointer
```

凭证引用按名字而不是凭证 ID：清单里只写 `name`，凭证所有者用授权（`credential_grants`，按任务和引用名唯一）决定实际使用哪一个凭证。这样填写凭证、更换凭证都不产生新的任务版本，也就不需要重新测试；清单在别的部署中也能原样使用。

Agent 面板只渲染五种声明式类型，平台不执行任务给出的 HTML 或脚本。各类型需要的数据形状写在契约的 `PanelSpec` 中；数据形状不符时控制台不显示该面板。

触发器单独维护，不放进版本。修改触发频率不应该产生新的代码版本。`setTriggers` 整体替换任务的触发器，按 `name` 对应已有触发器（同名同类型的保留 ID 和 webhook 令牌），触发器集合带版本号 `revision`，用 `If-Match` 防止覆盖：

```yaml
- name: weekday-morning
  type: cron
  schedule: "0 8 * * 1-5"
  timezone: Asia/Shanghai
  catch_up: skip            # skip 或 latest，见第 8.2 节
- name: crm-push
  type: webhook
  max_body_bytes: 1048576
```

清单的 JSON Schema 是 OpenAPI 的一部分，Skill 中的清单说明由它生成。

### 5.4 运行状态

状态与 PRD 第 6 节一致：

| 状态 | 进入条件 | 可以转到 |
| --- | --- | --- |
| 排队中 | 运行被接受 | 准备中、已取消、已跳过 |
| 准备中 | runner 领取 | 运行中、执行失败、取消中、运行中断 |
| 运行中 | 容器启动 | 执行完成、执行失败、已超时、取消中、运行中断 |
| 取消中 | 收到取消请求 | 已取消、运行中断 |
| 执行完成 | 进程以 0 退出 | 终态 |
| 执行失败 | 准备失败，或进程非 0 退出 | 终态 |
| 已超时 | 超过时限，且容器已确认停止 | 终态 |
| 已取消 | runner 确认容器已停止 | 终态 |
| 运行中断 | runner 失联且租约过期，runner 重启时停止了遗留容器，或 runner 报告容器异常消失。错误类别 `run_interrupted` | 终态 |
| 已跳过 | 重叠、队列已满、停机错过等原因未执行 | 终态 |

几条规则：

- 只有 runner 确认容器已停止，才进入已取消或已超时。runner 失联时停留在取消中，直到租约过期转为运行中断。
- 执行完成只表示进程正常退出。业务结果由任务通过辅助工具上报（第 7.7 节），与运行状态分开存储和展示。
- 失败时记录失败所在阶段和错误类别（第 9.4 节）。
- 容器中有进程被内核 OOM 杀掉时，执行器报告 `OOMKilled`。它只说明容器里有进程被杀过，主进程照样可能以 0 退出。规则是：主进程非 0 退出且 `OOMKilled`，记为执行失败，错误类别为 `out_of_memory`；主进程以 0 退出而 `OOMKilled`，仍记为执行完成，另写一条告警事件，控制台和 API 都会显示。
- `OOMKilled` 在 cgroup v2 上并不可靠。OOM 让主进程退出时，容器的 cgroup 随即被删除，containerd 有时来不及上报 OOM 事件，标记就一直是 false（M1 的 CI 上稳定复现，等待也没有用）。所以另加一条：主进程以 137 退出（被 SIGKILL），而且不是 runner 停止的，同样记为 `out_of_memory`，错误信息注明是推断。容器里没有特权进程，平台也没有发信号，能 SIGKILL 主进程的基本只有内核；任务自己 `kill -9` 自己会被误判，这个代价可以接受。子进程被杀、主进程以 0 退出的告警仍然依赖这个标记，可能漏报。

### 5.5 主要数据表

只列关键字段和约束，完整定义以迁移文件为准。所有表的主键都是应用生成的 UUIDv7，时间字段为 UTC。

| 表 | 关键字段 | 约束 |
| --- | --- | --- |
| `users` | username、display_name、email（可空）、password_hash（OIDC 用户为空）、is_admin、disabled_at | username 唯一，不区分大小写（`lower(username)` 表达式唯一索引，见第 6.3 节） |
| `identities` | user_id、issuer、subject | （issuer，subject）唯一 |
| `sessions` | user_id、token_hash、kind（web／mobile）、expires_at、last_seen_at | token_hash 唯一 |
| `access_tokens` | user_id、name、token_hash、prefix（令牌开头几个字符，用于辨认）、scopes、expires_at、last_used_at、revoked_at | token_hash 唯一 |
| `projects` | name、owner_user_id、is_personal | 部分唯一索引：is_personal 为真时 owner_user_id 唯一，保证每人只有一个个人项目 |
| `role_bindings` | project_id、user_id、role | （project_id，user_id）唯一 |
| `tasks` | project_id、name、state（active／inactive）、active_version_id、active_seq、latest_seq、archived_at、triggers_revision、console_hide_manual_run | 部分唯一索引：未归档时（project_id，name）唯一。`If-Match` 对任务取 latest_seq，对触发器集合取 triggers_revision，不另设 row_version |
| `task_versions` | task_id、seq、manifest、tree_hash、client（提交时的令牌名）、tested_at、created_by | （task_id，seq）唯一 |
| `task_tags` | task_id、tag | （task_id，tag）主键；（tag，task_id）索引用于按标签筛选；任务物理删除时级联删除 |
| `task_files` | version_id、path、blob_hash、size、mode | （version_id，path）唯一 |
| `environments` | runtime、image_digest、lockfile_hash | 主键即环境 ID（内容哈希） |
| `triggers` | task_id、name、type、enabled、schedule、timezone、catch_up、params、max_body_bytes、token_hash、token_prefix、next_fire_at、last_fired_at | （task_id，name）唯一；token_hash 唯一 |
| `trigger_fires` | trigger_id、scheduled_at、outcome、run_id（可空）、missed_count | （trigger_id，scheduled_at）唯一 |
| `runs` | task_id、version_id、env_id、status、trigger_kind、trigger_id、trigger_fire_id、scheduled_at、client、attempt、max_attempts、retry_of、not_before、rerun_of、skip_reason、is_test、runner_id、runner_name、claimed_at、lease_expires_at、queued_at、started_at、finished_at、exit_code、error_code、error_stage、cancel_requested_by、input_key、concurrency_key、log_archive、log_blob、log_bytes、logs_expired_at、archived_at | 部分唯一索引：concurrency_key 非空且状态为活动状态时唯一；（status，queued_at）索引；（status，lease_expires_at）索引，供对账扫描过期租约；log_archive 为空且已结束的运行按 finished_at 的部分索引，供日志归档扫描。重试和重跑分成 retry_of、rerun_of 两列，代替原来的 parent_run_id，控制台据此分别显示“重试”和“重跑自” |
| `run_required_labels` | run_id、label | （run_id，label）唯一 |
| `run_events` | run_id、seq、kind、payload、ts | （run_id，seq）唯一 |
| `log_chunks` | run_id、seq_from、seq_to、data | （run_id，seq_from）唯一 |
| `artifacts` | run_id、path、size、content_type、blob_key、expired_at | |
| `task_states` | task_id、blob_hash、size、updated_at | 每个任务一个已发布快照，主键为 task_id |
| `run_state_refs` | run_id、role、blob_hash（可空）、size、ready | （run_id，role）主键；input 固定领取时的快照，output 是待发布候选，保护对象不被 GC 删除 |
| `credentials` | owner_user_id、name、purpose、status、current_version | |
| `credential_versions` | credential_id、version、ciphertext、wrapped_dek、kek_id | （credential_id，version）唯一 |
| `credential_grants` | credential_id、task_id、name（清单中的引用名）、granted_by | （credential_id，task_id）唯一；（task_id，name）唯一 |
| `credential_requests` | user_id、task_id、credential_id、name、purpose、fields、status、client、expires_at | |
| `tools` | project_id、kind、name、config、credential_id | （project_id，name）唯一 |
| `notification_channels` | owner_user_id、name、kind、enabled、url_preview、加密的目标地址和签名密钥 | |
| `notification_subscriptions` | channel_id、project_id、task_id（可空）、events | |
| `notification_deliveries` | channel_id、event_type、merge_key、merged_count、payload、status、attempts、http_status、next_attempt_at、scheduled_at | |
| `notification_events` | event_type、run_id、credential_id、credential_expires_at、deliveries | run_id 唯一；（credential_id，credential_expires_at）唯一，保证每个事件只展开一次（第 14 节） |
| `pairing_codes` | user_id、code_hash、expires_at、used_at、session_id | code_hash 唯一 |
| `runners` | name、slots、token_hash（内嵌 runner 为空）、protocol_version、version（runner 的软件版本）、last_seen_at、disabled_at | 主键是 runner 自己生成的稳定 ID（文本）；token_hash 唯一 |
| `runner_labels` | runner_id、label | （runner_id，label）唯一 |
| `runner_tokens` | name、token_hash、prefix、created_by、expires_at、revoked_at、last_used_at | token_hash 唯一。管理员生成的注册令牌，只能用一次，过期或撤销后失效；配置文件中的固定令牌不在这张表里，可以反复使用 |
| `settings` | key、value、updated_by | key 唯一 |
| `idempotency_records` | principal、key、operation、request_hash、status、response、expires_at | （principal，key）唯一 |
| `audit_events` | ts、actor_kind、actor_id、actor_name、action、kind、resource_kind、resource_id、result、via、tool、ip、request_id、details | ts 索引；（actor_id，ts）索引；（resource_kind，resource_id）索引 |
| `leases` | name、holder、expires_at | name 唯一 |
| `blob_releases` | hash、released_at、deleted_at | hash 唯一。保留策略释放的对象和回收后的墓碑（第 6.6 节） |

运行事件有两个来源：任务经 runner 上报的阶段和结果，以及服务端自己记录的状态变化（`status`，data 为 `{"from", "to"}`）、产物（`artifact`）和日志截断（`log.truncated`）。两者共用一个序号，`listRunEvents` 按它分页，时间线不需要合并两份列表。`runs.event_seq` 是最后一个事件的序号，新增的 `runs.runner_event_seq` 是 runner 发来的最后一个序号，runner 重发的批次按后者去重（迁移 20261105010000）。服务端事件与引起它的状态变化在同一事务里写入，所以读到终态的运行时，它的事件已经完整。

`concurrency_key` 对 `concurrency: forbid` 的任务取任务 ID，其他任务为空。两种数据库的唯一索引都不把空值视为重复，所以一个部分唯一索引就能保证同一任务不重叠。

## 6 数据存储

### 6.1 两种数据库的分工

| | PostgreSQL | SQLite |
| --- | --- | --- |
| 定位 | 标准部署 | 单机、试用、开发 |
| 版本要求 | 15 及以上 | 随二进制内置（modernc.org/sqlite） |
| 多个 server | 支持 | 不支持 |
| 队列领取 | `FOR UPDATE SKIP LOCKED` | `BEGIN IMMEDIATE` 写事务，进程内串行 |
| 时钟 | 租约用数据库的 `now()` | 应用时钟 |
| 并发迁移 | 会话级 advisory lock 串行 | 不加锁，只有一个 server |
| 跨节点通知 | `LISTEN/NOTIFY` | 进程内广播 |
| 备份 | 企业现有方案（pg_dump、PITR 等） | `lerryn backup` 在线备份 |

PostgreSQL 的最低版本定为 15：领取查询用到的 `AS MATERIALIZED` 需要 12 以上，再排除社区已经停止维护的版本。CI 目前只在 17 上测试，M5 前补上 15，覆盖最低和最新两个版本。

**SQLite 的单写入者。** SQLite 没有行锁，写事务排队执行，需要两处处理：

- 读写事务一律以 `BEGIN IMMEDIATE` 开始（连接参数 `_txlock=immediate`）。默认的 deferred 事务先读后写，期间如果别的连接已经写入，升级写锁时直接返回 `SQLITE_BUSY`，busy_timeout 对这种情况不起作用。只读事务仍然是 deferred，WAL 模式下读不阻塞写。
- 写事务在进程内串行。多个连接争抢写锁时，拿不到锁的连接由 SQLite 的 busy handler 按 1 到 100 毫秒逐级退避后重试，锁释放时它们往往还在睡眠。S1 中 16 个并发领取 300 条运行，耗时在 135 到 947 毫秒之间波动；先在进程内排队再进入 SQLite，吞吐提高一个数量级以上，波动也消失了。单机部署里的写入方不只有领取，还有日志分片、心跳和事件，这种等待会直接拉长日志的可见延迟。所以 store 提供统一的写事务入口：SQLite 实现先取得进程内的写闸门再开始事务，PostgreSQL 实现直接开始事务；读操作仍走连接池。busy_timeout（默认 5 秒）只用来兜底跨进程的写入，例如 `lerryn migrate` 和在线备份。写闸门在 M1 与第一批写路径一起实现。

从 SQLite 迁到 PostgreSQL，用平台的导出导入格式完成（第 15.5 节），不做数据库层面的直接转换。

### 6.2 数据访问与迁移

- 数据访问用 Bun。它的查询构建器按方言处理占位符和类型，大部分查询只写一份。
- 方言专属的代码集中放在 `store/dialect/` 下，便于对照：按方言各写一份原生 SQL 的热点（运行领取、运行租约续期、租约过期扫描即对账、选主），连接参数和会话检查，迁移锁，以及驱动错误的分类（唯一冲突、外键冲突）。`store/` 下的其他代码和全部测试只用 Bun 的查询构建器，或两种数据库通用的原生 SQL。
- 驱动：PostgreSQL 用 pgx（`database/sql` 接口），SQLite 用纯 Go 的 modernc.org/sqlite。`store.Open` 打开连接后读回会话设置检查：PostgreSQL 固定 `standard_conforming_strings=on` 和 `timezone=UTC`；SQLite 固定 WAL、外键和 busy_timeout，运维人员关闭外键、改用 deferred 事务或使用内存数据库时，启动直接报错。
- Bun 把参数在客户端拼进 SQL 文本，不使用绑定参数。所以 pgx 改用 simple protocol，否则每个不同的 SQL 文本都会准备成一条服务端语句；Bun 转义字符串只把单引号加倍，依赖 `standard_conforming_strings=on`，连接池代理可能丢弃启动参数，所以要在启动时检查。Bun 也不检查占位符个数，参数少了要到数据库执行时才以语法错误出现。较长的原生 SQL 用 `?0`、`?1` 这样的序号占位符，或者传结构体用字段名占位，不靠位置对齐。
- 迁移用 goose 的 Provider API，`migrations/postgres` 和 `migrations/sqlite` 各一套，嵌入二进制，不使用 goose 的全局注册表。新增迁移必须两套同时提交，文件名的时间戳相同（`goose create` 为两个方言各执行一次会得到不同的时间戳，需要手工对齐），`TestDialectsInSync` 会拦住不一致的提交。
- 存储层有一套契约测试，在两种数据库上各跑一遍，方言差异在合并前暴露。PostgreSQL 每个测试一个 schema，SQLite 每个测试一个临时文件。

没有选的方案：

- sqlc：一个生成包只对应一种数据库引擎，支持两种数据库就要写两份查询，外加一层类型转换。
- GORM：隐式行为多，很多问题要到运行时才暴露。
- Ent：可以作为备选。一份 schema 能生成两种方言的迁移，代价是生成代码量大，复杂查询仍要回到原生 SQL。

### 6.3 建模约定

- 主键用应用生成的 UUIDv7。多节点生成不冲突，按时间有序，便于游标分页。
- 时间统一由应用以 UTC 写入，写入前截断到微秒（`store.Now`），这是两种数据库共同的精度，读回的值与写入的值相等。只有租约比较使用数据库时钟。
- JSON 字段只存不查。需要筛选或排序的字段单独成列。
- 不用触发器和存储过程，业务逻辑都在 Go 代码里。类型只用下表中两种数据库都有对应表示的几种：

| 用途 | PostgreSQL | SQLite |
| --- | --- | --- |
| 主键和其他 UUID | `uuid` | TEXT，小写规范形式 |
| 时间 | `timestamptz`，微秒精度 | TEXT，Bun 固定写成 UTC 的 `2006-01-02 15:04:05.999999+00:00`，文本比较和排序与时间顺序一致 |
| 布尔 | `boolean` | INTEGER 0／1 |
| JSON | text | TEXT |
| 枚举 | text | TEXT |

- SQLite 表一律 STRICT，类型不对的值会像在 PostgreSQL 上一样写入失败，而不是被静默保存。SQLite 允许非整数主键为 NULL，迁移里主键显式写 `NOT NULL`。
- 不绕过 Bun 直接用 `database/sql` 写 `time.Time`。modernc.org/sqlite 的默认时间格式与 Bun 不同，会破坏 SQLite 上的文本比较。
- 枚举不用 CHECK 约束，取值在 service 层校验。SQLite 上修改 CHECK 约束要重建表，新增一个取值不应该需要迁移。
- 不区分大小写的唯一约束用 `lower(列)` 表达式唯一索引。SQLite 的 `lower()` 只折叠 ASCII，PostgreSQL 折叠全部 Unicode，所以这类列的取值要限定为 ASCII，或者入库前统一转成小写，否则两种数据库对唯一性的判定会不一样。
- 唯一冲突和外键冲突由 `store.IsUniqueViolation`、`store.IsForeignKeyViolation` 判断类别，不解析约束名，两种数据库的错误消息格式不统一。

### 6.4 对象存储

对象存储接口有两个实现：本地磁盘和 S3 兼容存储。多节点部署必须使用 S3 兼容存储，可以直接接企业已有的对象存储。

| 前缀 | 内容 |
| --- | --- |
| `blobs/sha256/...` | 任务文件内容和日志归档，按哈希去重 |
| `bundles/<version_id>.tar.zst` | 任务版本包，runner 下载后按哈希缓存 |
| `logs/<run_id>/` | 原计划存日志归档和索引；M5 改为放在 `blobs/sha256/` 下，见第 6.5 节 |
| `artifacts/<run_id>/` | 运行产物 |
| `state/<task_id>/` | 任务状态目录（第 7.4 节） |
| `inputs/<run_id>` | webhook 请求体等运行输入 |

上传和下载优先使用预签名地址，大文件不经过 server 转发。

S3 兼容存储的实现在 `blob.S3`（M5）。对象布局与本地实现相同，键为 `<prefix>blobs/sha256/<前两位>/<哈希>`，`storage.s3.prefix` 让多个部署共用一个桶。请求用 AWS Signature Version 4 签名，签名代码自己实现，不引入 AWS SDK 或 MinIO 客户端：只用到 PUT、GET（含 Range）、HEAD 三种请求，单元测试用 AWS 文档中的签名示例核对。上传时内容的 SHA-256 就是对象名，同时作为 `x-amz-content-sha256` 发出，存储端会校验内容。上传前先读完内容算出哈希，8 MiB 以内在内存里，超过的部分写入临时文件。已存在的对象不再上传。server 启动时用 HEAD 检查桶是否可用，地址或密钥写错时启动直接失败。桶由运维人员创建，server 不创建桶。默认用路径风格访问（`path_style`），自建的 S3 兼容服务大多需要它。集成测试在 MinIO 容器上运行。预签名地址尚未实现，runner 目前经 server 取任务文件。

### 6.5 日志

日志分运行中和运行结束两段处理。

**运行中：**

1. runner 逐行读取 stdout 和 stderr，先脱敏（第 12.6 节），再编号。
2. 每 500 毫秒或攒够 256 KiB 上传一批。
3. server 写入日志分片表，以（运行 ID，起始序号）去重，然后发出事件通知。
4. 各 server 上的 SSE 连接收到通知后，从分片表读取新内容推给客户端。

每行一条 NDJSON 记录：

```json
{"seq": 1024, "ts": "2026-11-02T00:00:01.234Z", "stream": "stdout", "stage": "fetch", "text": "fetched 120 records"}
```

**运行结束后：** 未手动归档的运行由主节点把分片合并为归档写入对象存储，再删除分片。归档按每 1000 行一个独立压缩帧组织，另存一份索引，记录每帧的起始序号和字节偏移。读取任意位置时只解压对应的帧，10 万行的日志也不需要整体加载。

M5 的实现（`service/logarchive.go`）：主节点每 10 秒找出已经结束、日志仍在分片表里的运行，每批最多 20 个，按结束时间先后归档；积压时连续处理。归档对象由若干个独立的 gzip 成员首尾相接组成，每个成员是 1000 行与分片相同的 NDJSON 记录。对象按内容哈希存放在 `blobs/sha256/` 下，与任务文件共用一套存储接口，不另设 `logs/<run_id>/` 前缀：内容寻址的对象写入即不可变，两个节点重复归档得到同一个对象，不需要额外的删除和覆盖语义。帧索引（每帧的起止序号、字节偏移和长度）和对象哈希存在 `runs.log_archive` 一列 JSON 里，只存不查，读取时按 Range 只取需要的帧。写入归档和删除分片在同一个事务里，条件是 `log_archive` 仍为空，两个主节点同时归档也只生效一次。读取日志时，如果先读到的运行还没有归档、读分片时分片已经被删除，就重新读一次运行，改从归档读取，所以归档过程中的翻页不会缺行。压缩用标准库的 gzip，不引入新的依赖。单机部署同样归档，写入本地对象存储。

**用户手动归档。** `setRunArchive`（`PUT /api/v1/runs/{runId}/archive`，变更）需要运行控制权限，设置 `runs.archived_at`（迁移 20261111010000）。只允许归档已结束且日志未过期的运行，日志已清理返回 410 `expired`。先在事务外按每批 1000 行读取完整日志，写入立即取消文件链接的临时文件，内存只保留一批；再锁定运行行并复查状态、日志行数及清理标记，在一个事务内写入 `log_chunks`、清除对象归档引用并设置手动归档时间。此前已自动转存到对象存储的日志也会读回数据库，旧归档对象进入引用回收。后台日志转存及提交条件都排除手动归档的运行，因此完整日志持续留在数据库，分页、流和下载沿用现有读取路径。取消手动归档后恢复后台转存及保留策略。每次实际状态变更写 `run.archive` 审计，重复设置相同状态没有额外影响。

**限制：** 单行超过 64 KiB 截断；单次运行的日志总量有上限，默认 20 万行或 100 MiB，部署可调。超出部分丢弃，同时写入一条截断事件，记录丢弃的行数和字节数，控制台和 API 都会显示。

### 6.6 保留与清理

运行记录、日志、产物分别设置保留期和容量上限，部署级给默认值，项目级可以收紧。清理由主节点定期执行，每次清理都留下记录。被清理的日志和产物，查询时返回"已过期"，而不是空内容。

首个试用版的实现（`service/retention.go`、`service/blobgc.go`，迁移 20261108010000）：

**手动保留优先。** 手动归档的运行不参加日志、运行记录和产物的时间清理，也不计入自动清理的容量上限。保留策略的事务与手动归档使用同一运行行锁，重新检查归档状态后才能清理，避免先选中旧记录、随后误删刚完成的归档。归档不停止自动重试；取消归档后，已经超期的内容可能在下一轮保留策略清理时被删除。

**配置。** 部署级的五项，都在 `retention` 下，0 表示不限，默认全部为 0：部署者决定删什么，升级不会突然删掉数据。PRD 第 11 节的默认值待试用后按磁盘和使用频率确定。

| 配置项 | 作用 |
| --- | --- |
| `runs_days` | 运行结束多少天后删除运行记录，连同事件、日志和产物 |
| `logs_days` | 运行结束多少天后清理日志，运行记录保留 |
| `artifacts_days` | 产物生成多少天后清理，产物行保留 |
| `logs_max_mib` | 已归档日志的总量上限，超出时从最早结束的运行开始清理 |
| `artifacts_max_mib` | 产物的总量上限，按产物大小合计，超出时从最早生成的开始清理 |

`logs_days`、`artifacts_days` 不能大于 `runs_days`，日志和产物不会比运行记录活得长，配置成那样只会误导读配置的人，启动时直接报错。项目级收紧没有做：需要项目设置的存储和接口，试用阶段先看部署级是否够用。单次运行的日志上限（20 万行或 100 MiB）和产物上限（100 个文件、单个 100 MiB、合计 500 MiB）是 runner 协议的常量，runner 和 server 两边都检查，暂不开放配置；超出时 runner 在运行日志里写一行平台消息说明跳过的文件，日志截断记一条 `log.truncated` 事件。

**清理顺序。** 主节点成为主节点 30 秒后第一次清理，之后每 10 分钟一次。每次按下面的顺序处理到没有剩余为止，每批最多 200 行，一批一个事务：

1. 补齐旧归档的 `runs.log_blob` 和 `runs.log_bytes`。归档对象的哈希和大小原来只在 `log_archive` 的 JSON 里，JSON 只存不查，所以新增两列，归档时一并写入，迁移前的归档由这一步从 JSON 读出后补上。
2. 按 `logs_days` 清理日志，再在超出 `logs_max_mib` 时从最早结束的运行开始清理。还在分片表里的日志不计入总量，主节点在运行结束后几秒内就会归档。清理时删除分片，设置 `runs.logs_expired_at`，`log_bytes` 记为 0；从未归档的运行写入一个空归档，归档循环不再处理它。
3. 按 `artifacts_days` 和 `artifacts_max_mib` 清理产物，设置 `artifacts.expired_at`，行保留，列表照常列出。内容相同的产物共用一个对象，总量按每个产物的大小合计，不扣除去重。
4. 按 `runs_days` 删除运行，事件、分片、产物行和所需标签随外键级联删除。删除前把其他运行的 `retry_of`、`rerun_of` 和触发记录的 `run_id` 置空，这几列不都是带删除动作的外键。每个任务最近的一次非测试运行始终保留，任务列表靠它显示上次运行的状态；测试运行不受这条保护。
5. 回收对象（见下文）。

进行中的运行从不处理，已结束但自动重试还没决定的运行（`retry_decided_at` 为空且还有剩余次数）也不处理。

**读取被清理的内容。** `readRunLogs`、`streamRunLogs`、`downloadRunLogs` 和 `downloadArtifact` 返回 410 `expired`，`next_actions` 建议需要时重跑；运行的 `logs_expired_at` 记录日志的清理时间，`log_lines` 保留原来的行数。选 410 而不是 404 `not_found`：运行记录还在，`not_found` 会让 Agent 以为运行 ID 写错了；也不返回空页加标记，Agent 容易忽略标记，把空日志当成"脚本没有输出"。没有查看权限时仍然返回 `not_found`。控制台看到 `logs_expired_at` 就不再读日志，显示"日志已按保留策略清理"。被删除的运行记录返回 `not_found`。

**留下的记录。** 每次清理有删除时写一条审计记录，`action` 为 `retention.cleanup`，执行者为 system，`details` 是删除的运行、日志、产物、对象数和字节数，以及当时的策略；没有删除时不写。服务日志写同样的总数，指标 `lerryn_retention_removed_total{kind}` 和 `lerryn_retention_removed_bytes_total{kind}` 累计。

**对象回收。** 对象存储按内容寻址，同一个对象可能同时是任务文件、脚本工具文件、几个产物、webhook 请求体和日志归档。清理不直接删对象，而是把不再引用的哈希写入 `blob_releases`；回收时逐个确认没有任何行引用（`task_files.blob_hash`、`tool_files.blob_hash`、未清理产物的 `blob_key`、`runs.log_blob`、`runs.input_key`）再删除。还有未补齐的旧归档时不回收，免得漏看它们引用的对象。

写入方先存对象、再提交引用它的行，内容已存在时存储什么也不做。所以写入方可能看到对象还在，回收在它提交之前把对象删掉。两条规则堵住这个窗口：

- 回收在一个事务里标记释放记录、检查引用、删除对象，标记过的记录作为墓碑保留 1 小时。
- 写入方在添加引用的事务最后撤回这些哈希的待回收记录（`claimBlobs`）。回收正持有同一行时，撤回会等它提交；之后写入方查到墓碑，提交后对象确实不在就重新存一次。任务文件、工具文件、webhook 请求体和日志归档都还在内存或临时文件里，直接重存；产物的内容已经读完，这次上传返回错误，runner 重传时重新存入。

写入方从存对象到提交只有几秒，远小于墓碑的 1 小时，看到对象又被删掉的写入方一定能查到墓碑。对象删除用 `blob.Deleter`，本地存储删除文件，S3 发 DELETE。

**顺带修正。** `streamRunLogs` 和 `downloadRunLogs` 原来只读分片表，日志归档后下载得到空文件、流不会结束；现在和 `readRunLogs` 一样，分片不在时从归档读。

## 7 执行与沙箱

### 7.1 隔离等级的取舍

Lerryn 的定位是企业自部署：任务代码由企业成员借助 Agent 编写，跑在企业自己的机器上。按这个定位，首版要防的是：

- 一个任务影响其他任务：抢占资源、改动别的任务的文件或环境、遗留进程。
- 一个任务拿到不属于它的东西：宿主机目录、其他任务的工作目录、未授权的凭证、平台自己的管理凭证。
- 失控的脚本：死循环、内存泄漏、输出暴涨。

首版不防的是：成员有意逃逸容器、攻击内核或宿主机。这类风险由企业内部的账号和权限管理约束，平台不宣称适合运行陌生人提交的代码。

所以首版只用 Docker 容器加默认加固，不做 gVisor 适配、工具网关和出站白名单。这些以后可以叠加上去，不影响执行器接口。

| 风险 | 首版措施 |
| --- | --- |
| 资源抢占 | CPU、内存（不使用 swap）、进程数（cgroup 的 pids 限制）、打开文件数、执行时间、输出量上限 |
| 遗留进程 | 每次运行一个独立容器和 PID 命名空间，容器内有 init 进程回收孤儿进程，停止容器即结束其中所有进程 |
| 越界访问文件 | 只挂载本次运行需要的目录，根文件系统只读，拒绝声明了 `VOLUME` 的镜像 |
| 环境互相影响 | 环境按锁文件哈希构建，只读挂载 |
| 任务之间互访 | `full` 网络使用专用网桥，关闭容器之间的通信（第 7.5 节） |
| 凭证越权 | 只注入授权给该任务的凭证；runner 只能领取分配给自己的运行的凭证 |
| 提权 | 非 root 用户、去掉全部 capabilities、`no-new-privileges`、默认 seccomp |
| 访问平台内部 | 容器不挂载 Docker socket；运行容器使用单独的 Docker 网络；平台 API 都需要令牌；部署文档要求数据库和 server 不监听在 runner 主机可达的地址上 |
| 未脱敏的输出落盘 | 容器日志驱动为 `none`，输出只经 attach 交给 runner |

### 7.2 容器执行器

执行器是一个接口（`server/internal/executor`），提供启动、等待退出、停止、删除和按标签列出容器这几个操作。它不了解 runner 协议、运行状态和日志脱敏，只报告事实：退出码、是否被 OOM 杀过、是否需要 SIGKILL 才停下，由 runner 映射成运行状态和错误类别。P0 实现 Docker 执行器，使用 Docker 官方 Go 客户端；Podman 的兼容性尚未验证，P1 评估 Kubernetes 执行器。另有一个本地进程实现，只用于开发和测试。

每次运行启动一个新容器，默认加固：

- 以非 root 用户运行，默认 `65532:65532`。只接受数字形式的 uid:gid，拒绝 uid 0、gid 0 和只给 uid 的写法，因为用户名要从任务镜像的 `/etc/passwd` 解析，缺 gid 时 Docker 会用镜像里该用户的主组，可能是 root。
- 根文件系统只读，可写的只有工作目录、输出目录、`/tmp` 和凭证目录。`/tmp` 是 tmpfs（默认 64 MiB），挂载为 `noexec`，运行时镜像不能依赖在 `/tmp` 里执行文件。
- 去掉全部 capabilities，设置 `no-new-privileges`，使用 daemon 默认的 seccomp 配置。
- 设置 CPU、内存、进程数、打开文件数上限。内存加 swap 的上限等于内存上限，即不使用 swap。进程数用 cgroup 的 pids 限制，不用 `RLIMIT_NPROC`：所有任务都以同一个 uid 运行，`RLIMIT_NPROC` 按 uid 计数，并发的任务会互相挤占。
- 使用 init 进程（Docker 的 `--init`）。没有 init 时任务进程就是 PID 1，内核不会向没有安装信号处理函数的 PID 1 投递 SIGTERM，每次取消都要等满宽限期才能 SIGKILL；init 还负责回收孤儿进程。
- 日志驱动为 `none`，输出只经 attach 交给 runner。用 json-file 等驱动时，未脱敏的输出会写到 runner 主机的磁盘上。
- `/dev/shm` 固定为 64 MiB，不依赖 daemon 的默认值；IPC 和 cgroup 命名空间私有；`OomScoreAdj` 设为 500，宿主机内存紧张时先杀任务，不先杀 runner、server 或数据库；不设重启策略。
- 只挂载本次运行需要的目录：版本包、环境、工作目录、输出目录。凭证目录 `/run/secrets` 是执行器自己建的 tmpfs（`noexec`，权限 0700，属主为任务用户），凭证由容器内的 `sbx` 入口写入（第 12.5 节）。
- 拒绝声明了 `VOLUME` 的镜像。Docker 会给这类路径自动建可写的匿名卷，绕过只读根文件系统和"只挂载需要的目录"。
- 容器带 `lerryn.managed`、`lerryn.run_id`、`lerryn.runner_id` 标签，供对账使用。

挂载的规则：

- 只用 `Mounts`（type bind），不用 `Binds`。挂载源在 daemon 所在的主机上不存在时，前者创建容器失败，后者会在宿主机上静默建一个空目录挂进去，任务在空目录上运行而没有任何报错。runner 与宿主机路径不一致的问题（第 15.2 节）因此会在创建容器时明确暴露。
- 拒绝挂载容器运行时的 socket、`/`、`/run`、`/var/run`，也不允许覆盖 `/proc`、`/sys`、`/dev`、`/tmp`、`/run/secrets`。
- runner 只挂载自己新建的目录，挂载前检查路径上没有符号链接，因为 daemon 会在宿主机上解析挂载源中的符号链接。目录里面的符号链接在容器的挂载命名空间里解析，不会指到宿主机。

加固做不到时不降级运行：

- daemon 创建容器时返回任何告警，执行器都删除容器并报错。例如 cgroup 控制器没有委派时，Docker 只给一条"PIDs limit discarded"告警，容器照样运行，限制却没有生效。
- runner 启动时读取 `docker info` 自检，缺少 seccomp、内存限制、CPU 配额或进程数限制中的任何一项，就拒绝领取运行。

部署者如果需要更强的隔离，可以自行把 Docker 的默认运行时换成 gVisor，首版不做专门适配和测试。

### 7.3 运行环境与依赖

环境由两部分确定：运行时基础镜像的 digest，和依赖锁文件。两者合起来的哈希就是环境 ID，写入每条运行记录。

| 运行时 | 清单中的 `runtime` | 默认基础镜像 | 依赖管理 |
| --- | --- | --- | --- |
| Python | `python`，`"3.12"` | `ghcr.io/astral-sh/uv:0.13.6-python3.12-trixie-slim` | uv，锁文件 `uv.lock`，同时需要 `pyproject.toml` |
| TypeScript 与 JavaScript | `node`，`"22"` | `node:22.23.4-trixie` | npm，锁文件 `package-lock.json`，同时需要 `package.json` |
| Shell | `shell`，`"5"`（bash 5） | `bash:5.3.20-alpine3.24` | 无 |

Node 默认用完整版镜像，不用 slim。不少 npm 包的本体是预编译的二进制，在 postinstall 里用 curl 下载（例如 `@larksuite/cli`），还有些包要用 node-gyp 编译原生模块；slim 镜像没有 curl、wget 和编译工具链，这类依赖会以 `dependency_install_failed` 失败（issue #22）。完整版镜像大约 1.8 GB，比 slim 大四倍多，换来的是按 Skill 的常规写法加依赖就能装上。部署方可以用 `runner.node_image` 改回 slim。

- 镜像由 `runner.python_image`、`runner.node_image`、`runner.shell_image` 配置，默认是固定标签的官方镜像，不另建 `lerryn-runtime-*` 镜像：`sbx` 和辅助库由 runner 挂载进容器（第 7.7 节），镜像不需要定制。部署者可以换成内部镜像，Node 镜像要有 node、npm、`/bin/sh` 和 awk，Shell 镜像要有 bash、`/bin/sh` 和 awk。
- TypeScript 原计划用 Bun 和 `bun.lock`，实现时改为 Node 22 和 npm。Node 22.18 起默认去掉类型注解直接运行 `.ts` 文件，不需要额外的编译工具；npm 随官方镜像提供，`package-lock.json` 是 npm 生态最通用的锁文件，内网的 npm 镜像源都支持。这种运行方式只支持类型注解这类可以直接擦除的语法，`enum`、`namespace` 等需要转换的写法要由任务自己编译。
- Node 的依赖在安装容器里以 `npm ci --omit=dev` 安装，安装目录里只有 `package.json` 和 `package-lock.json` 两个文件。得到的 `node_modules` 只读挂载到 `/sbx/task/node_modules`，任务文件中的 `import` 和 `require` 都能找到，`node_modules/.bin` 在 `PATH` 中。依赖的安装脚本照常执行，它们只在安装容器里运行。
- Shell 镜像基于 Alpine，带 bash 和 busybox 的常用工具（含 `wget`），不带 curl 和 jq。需要更多工具时由部署者换成自己的镜像。

准备环境的过程：

1. runner 按环境 ID 查本机缓存，命中就直接使用。
2. 未命中时，启动一个安装容器，按锁文件把依赖安装到 `envs/<环境 ID>/` 目录。安装容器可以访问配置好的包镜像源。
3. 运行容器以只读方式挂载这个目录。

环境内容不可变，所以同一环境 ID 只构建一次，多个任务可以安全共享，一个任务装依赖不会影响其他任务。安装失败时，运行在准备阶段失败，错误类别为 `dependency_install_failed`，安装日志计入运行日志。

本机缓存按最近使用时间清理。PyPI 和 npm 的镜像源由部署配置，用于内网和离线环境。

### 7.4 工作目录和任务状态

| 路径 | 内容 | 生命周期 |
| --- | --- | --- |
| `/sbx/task` | 任务版本文件，只读 | 每次运行 |
| `/sbx/work` | 工作目录，可写 | 每次运行，结束后删除 |
| `/sbx/out` | 产物目录，结束后上传 | 每次运行 |
| `/sbx/state` | 任务状态目录，跨运行保留 | 运行前从对象存储下载，成功结束后上传 |
| `/sbx/in` | 运行输入，只读：webhook 运行的请求体为 `/sbx/in/body`，媒体类型在环境变量 `LERRYN_INPUT_CONTENT_TYPE` 中 | 每次运行 |
| `/run/secrets` | 凭证文件，tmpfs，只有任务用户可读，由 `sbx` 入口写入 | 每次运行 |

runner 新建的可写目录（工作目录、输出目录）要让任务用户 65532 可写。`lerryn` 镜像本身以 65532 运行，runner 在容器中运行时建出的目录属主就是这个用户；runner 以 root 运行时，需要先把目录 chown 给任务用户。

产物的收集：容器停止后、上报终态之前，runner 先送完任务的事件和日志，再经 `os.Root` 读取 `/sbx/out` 并逐个上传（runner 协议的 `AddArtifact`），最后上报终态，所以运行结束时产物已经齐全。输出目录由任务写入，内容不可信：符号链接和特殊文件一律跳过，不跟随；每个运行最多 100 个产物，单个不超过 100 MiB，合计不超过 500 MiB，超出的文件跳过并在日志中写一行系统消息，不影响运行的状态。服务端把内容按哈希存进对象存储，媒体类型先按扩展名的固定表判断，再看内容开头；只有纯文本、Markdown、CSV、YAML 和 JSON 可以预览，HTML、SVG、脚本和二进制只能下载，下载响应带 `X-Content-Type-Options: nosniff` 和 `Content-Security-Policy: sandbox`。同一路径再次上传时替换内容。 运维日志的 `artifacts uploaded` 只统计返回成功的文件及字节，失败文件仍保留原有警告，不计入成功汇总。

普通文件的上传失败不能按主动跳过处理。产物与最后的日志、事件及终态共用报告期限（默认 10 分钟），瞬时故障在期限内退避重试，每次重新打开文件；上传重试不会重新执行任务。输出目录丢失或无法读取，也不能视为没有产物；正常空目录可以结束。永久拒绝或报告期限耗尽时不提交终态，清理已停止的容器后交由租约对账标为运行中断；runner 自身退出时保留本地目录和容器，沿用重启对账流程。租约已丢失时停止交付，不再上报该运行的终态。

任务状态目录用于保存游标、上次处理位置这类小数据。同一个 TaskID 的版本共享状态；归档保留状态，归档后同名新任务不继承。领取时把当前快照固定为该运行的输入引用，跨 runner 从共享对象存储读取，不依赖本机目录。对象缺失、损坏或无法读取时准备失败，不能当作首次空状态继续执行。

只有执行状态为 `completed` 时才发布新状态，包括测试运行及业务结果为 `failed` 或 `partial` 的零退出码运行。脚本失败、取消、超时和中断保留旧快照。正常空目录是显式清空；目录丢失、不可读、含符号链接或特殊文件、超限时整包拒绝，不按产物规则跳过。状态与最后的日志、产物和终态共用报告期限，重试不重新执行任务。

快照使用版本化 JSON 文件集，规范排序后按内容哈希存储。普通文件内容总量最多 10 MiB，文件与目录合计最多 1000 项，路径最多 255 个 UTF-8 字节，线上编码最多 16 MiB。允许普通文件、空目录和必要执行位，不保留 owner、特权位或链接关系。路径必须干净相对、父目录显式存在；接收端经新 staging 目录和 `os.Root` 排他写入，完整校验后安装可写挂载，拒绝目标主机上的大小写或 Unicode 别名冲突。

上传先登记固定的 output 候选，修复可能的 GC tombstone 并核对对象内容后才标 ready。Finish 在一个事务里检查有效租约、发布 head、记录终态及事件，并释放 input/output 引用；事务末尾再次检查数据库时钟的租约，失败全部回滚。并发运行以最后成功发布的事务覆盖前值，不按 runner 时间排序，不合并目录。旧 Finish 重放不能覆盖后来发布的状态。当前 head、input pin 和未 ready 候选均参与 GC 引用保护；运行保留期不会删除任务当前状态。

实例备份和整库导出包含这些状态引用与对象，单任务 TaskBundle 仍只迁移定义，不附带运行状态。状态不是公开浏览接口，下载只给有有效租约的所属 runner；内容不进入普通任务详情、MCP 返回或平台日志。

### 7.5 网络

首版只有两种网络策略：

| 策略 | 效果 |
| --- | --- |
| `none` | 无网络，容器里只有回环接口 |
| `full` | 只接入专用网桥 `lerryn-tasks`，可以访问外部网络，默认值 |

- 专用网桥关闭容器之间的通信（`enable_icc=false`），一个任务访问不到另一个任务监听的端口。这项隔离依赖 dockerd 自己管理 iptables：daemon 配置了 `"iptables": false`，或者在 OrbStack 这类桌面环境里，它不生效。S4 在原生 dockerd 上确认生效，在 OrbStack 上确认不生效。macOS 上用 OrbStack 部署单机拓扑是支持的（部署文档第 2.1 节），前提是接受开了外网的任务之间不隔离；runner 发现 daemon 是 OrbStack 或 Docker Desktop 时在启动日志里告警。需要任务之间严格隔离时用 Linux 主机。
- `full` 网络下，任务仍然能访问宿主机网关和宿主机上发布的端口。数据库和 server 不应监听在 runner 主机可达的地址上。
- 部署者可以限制某类 runner 只允许 `none`。出站域名白名单放到 P1。

### 7.6 取消、超时与清理

1. 收到取消请求或超时后，runner 向容器发送 SIGTERM，等待宽限期（默认 10 秒），再发送 SIGKILL。宽限期也写进容器配置，daemon 自己停止容器时（例如 daemon 关闭）给任务同样的时间。
2. 容器有独立的 PID 命名空间，停止容器会结束其中所有子进程，不会遗留后台进程。
3. 容器是否已经停止，以查询容器状态（inspect）看到"未运行"为准。daemon 的 wait 通知只作为提醒，另外每 500 毫秒查询一次。确认不了时执行器返回错误，runner 不上报已取消或已超时，运行留在取消中，交给对账处理。
4. runner 确认容器已经停止，并送完剩余的日志、事件和符合收集规则的产物后，才上报已取消或已超时；上报之后再删除容器。停止确认和报告内容交付完成后才提交终态，删除失败不会让运行卡在取消中，留下的已停止容器在 runner 下次启动时清理。删除不强制，删除后再查询一次，确认容器已经不存在。进程退出时，刚结束的运行仍会完成删除，最多等 10 秒。
5. 取消或超时与进程自然退出同时发生时，以实际退出为准：发出信号之前容器已经退出的，按退出码上报执行完成或执行失败。
6. 停止过程中容器消失，按"容器异常消失"处理，标为运行中断。
7. runner 重启时，列出本机带 `lerryn.runner_id` 标签的容器并上报。P0 一律停止这些容器，对应运行标为运行中断，错误类别 `run_interrupted`。重新接管运行中的容器，放到后续版本。runner 的 ID 在第一次启动时生成，存在数据目录的 `runner-id` 文件里，重启后不变，才能认出自己留下的容器。

S4 实测（宽限期 2 秒）：响应 SIGTERM 的任务从取消到确认停止约 45 至 83 毫秒，忽略 SIGTERM 的任务约 2.05 秒。

### 7.7 沙箱内辅助工具

运行容器中提供 `sbx` 命令，以及 Python、TypeScript 的轻量封装，用于上报平台能识别的信息：

```bash
sbx stage start fetch
sbx stage done fetch --message "fetched 120 records"
sbx result --status ok --summary "生成 3 份摘要" --data result.json
sbx artifact add report.md --title "周报摘要"
sbx mcp call crm-mcp search_accounts --args '{"query": "acme"}'
```

这些命令写入挂载的事件文件，runner 读取后上报。用事件文件而不是网络，是为了让 `network: none` 的任务也能上报阶段和结果。

日志行按阶段逐行标注。事件文件和容器输出是两条独立的通道，只看事件文件，无法知道阶段切换落在输出的哪一行：M1 只能在处理每段输出之前读一次事件文件，CI 上一次打印 10 万行时，切换附近的行标到了相邻的阶段。现在 `sbx` 上报阶段时，先刷新任务的输出，再向 stdout 写一行阶段标记，最后追加事件。标记与任务输出走同一条通道，runner 在日志管道里识别它，从日志中去掉，并在流中的这个位置切换阶段。

- 标记的格式是 `\x1elerryn <令牌> <标记 ID> <事件类型> <阶段 ID>`。令牌是 runner 为每次运行生成的随机值，经环境变量 `LERRYN_LOG_MARKER` 交给任务，任务输出中碰巧相似的内容不会被当成标记。标记 ID 同时写进事件文件的同一事件（`mark` 字段），runner 据此把两边对上。
- 标记前面如果有任务没有换行的半行输出，这半行单独成为一行，标为切换之前的阶段。
- 事件文件仍然是阶段状态和时间的依据，也是标注的后备。没有标记 ID 的事件，读到时生效；有标记 ID 而标记没有到达的（例如任务重定向了 `sbx` 的输出），在 stdout 又收到 4 MiB 输出、或 stdout 静默 1 秒之后生效，这一段的标注回到近似。`sbx` 先写标记再写事件，正常情况下读到事件时，标记已经在容器到 runner 之间的管道里，远小于 4 MiB。
- stderr 是另一条流，其中的行按到达 runner 时 stdout 的位置标注，接近但不保证准确。
- Python、Node、Shell 各有一个集成测试，打印 10 万行、跨两个阶段，逐行检查标注。

`sbx` 由 runner 挂载进每个运行容器（`/sbx/sdk/bin/sbx`，已在 `PATH` 中），不要求运行时镜像预装。Python 运行时用 Python 实现；Node 和 Shell 运行时用 POSIX sh 实现，只依赖 sh、awk，以及 coreutils 或 busybox 里的 `od`、`tr`、`date`、`dd`，JSON 的校验和压缩由 awk 完成。没有用 Go 写静态二进制，是因为它要按 CPU 架构交叉编译、随 server 分发，在 macOS 开发机上也要另行构建；sh 实现和辅助库一样，是嵌入 server 的文本文件。辅助库：Python 中 `import sbx`，Node 中 `import * as sbx from "@lerryn/sbx"`（挂载在 `/sbx/node_modules`，任务目录下的文件都能解析到）。用法见 `runtimes/sdk/` 下各自的 README。

`sbx` 同时是任务容器的入口程序：从 stdin 读取本次运行的凭证，写入 `/run/secrets` 后再 exec 任务清单中的 `entrypoint`（第 12.5 节）。

控制台只展示收到的事件。任务没有上报阶段时，只显示整体状态和时间线；没有上报结果时，不生成摘要或成功数量。

### 7.8 工具

首版支持三类工具：

| 类型 | 登记者 | 使用方式 |
| --- | --- | --- |
| 外部 MCP 服务 | 项目维护者 | 任务清单声明后，runner 把服务地址和所需凭证注入容器；任务用 `sbx mcp call` 或任意 MCP 客户端调用 |
| HTTP API | 项目维护者 | 同上，注入 base URL 和凭证，任务直接调用 |
| 脚本工具 | Agent | 一段可复用的脚本包，其他任务在清单中引用后以只读方式挂载 |

- Agent 通过 `discover` 和工具列表接口看到项目内可用的工具及说明。
- 经 `sbx mcp call` 发出的调用会写入事件文件，控制台显示为"平台实际记录的工具调用"。任务用其他客户端或直接发 HTTP 请求，平台看不到。
- 工具需要的凭证和其他凭证一样，按授权注入容器。

工具网关放到 P1：由 runner 代为调用外部工具，凭证不进入容器，调用记录也不再依赖任务配合。

当前实现（表 `tools`，迁移 20261106010000；脚本工具的 `tools.tree_hash` 和 `tool_files`，迁移 20261107020000）：

- 三类工具：HTTP API（`http`）、外部 MCP 服务（`mcp`）和脚本工具（`script`）。登记项为项目内唯一的名称、类型和说明，`http` 和 `mcp` 另有 http/https 地址，`script` 另有一组文件。地址不能带用户名和密码，工具需要的凭证在任务清单的 `credentials` 中照常声明和授权。
- `listTools`（`GET /api/v1/tools`，可按 `project_id` 筛选）按查看任务的权限返回，脚本工具带文件列表（不含内容）和 `tree_hash`；`registerTool`（`POST /api/v1/projects/{projectId}/tools`，同名时更新，脚本工具的文件整体替换）和 `removeTool`（`DELETE /api/v1/tools/{toolId}`）需要“登记工具”的动作，即维护者或所有者，持有维护者令牌的 Agent 也可以。三者都不作为独立的 MCP 工具暴露，经 `api_call` 调用。登记的说明、地址和文件同样经过第 11.7 节的明文检查。
- 脚本工具的文件规则与任务文件相同（最多 100 个、合计 1 MiB、路径规则一致），内容放在对象存储里，`tree_hash` 的算法与任务版本相同。
- 清单的 `tools` 只能引用任务所在项目中登记的名称。`applyTask` 把未登记的引用列在 `warnings` 中，`validateTaskVersion` 列为错误，版本不可激活。`network: none` 的任务引用 `http` 或 `mcp` 工具时给出警告，不阻止激活。
- 领取时 server 按当时的登记解析工具，随运行规格交给 runner；引用的工具已被移除时，运行在领取时以 `validation_failed` 失败，不会缺着工具运行。脚本工具的文件内容在领取事务提交之后从对象存储读出并校验哈希，读不出来的运行以 `internal_error` 失败。工具按运行时的登记解析，不随任务版本固定：再次登记脚本工具，引用它的任务下一次运行就用新文件。为了能对上每次运行用的是哪一份，runner 在运行日志里写一行 `tool <名称>: script tool sha256:<前 12 位>`。
- runner 把 `http`、`mcp` 工具的地址放进 `LERRYN_TOOL_<名称>_URL`（名称转大写，`-` 换成 `_`），把脚本工具的文件写到运行目录的 `tools/<名称>`，只读挂载到 `/sbx/tools/<名称>`，目录放进 `LERRYN_TOOL_<名称>_DIR`。全部工具以 JSON 数组放在 `LERRYN_TOOLS`（`name`、`kind`，以及 `url` 或 `dir` 和 `tree_hash`）。runner 协议的 `tool` 新增可选字段 `tree_hash` 和 `files`，不升协议版本。

`sbx mcp call`：

- 三种运行时都有。Python 由 `sbx` 模块用 `urllib` 实现；Node 由 `@lerryn/sbx` 用 `fetch` 实现，sh 实现的 `sbx` 命令在 Node 运行时交给它的 `cli.js`；Shell 运行时没有 Node 和 curl，sh 实现用 busybox 的 `wget`（有 `curl` 时优先用 `curl`），JSON-RPC 应答由 awk 解析。Python 中 `sbx.mcp_call`，Node 中 `sbx.mcpCall`。
- 走 MCP 的 Streamable HTTP：`initialize`、`notifications/initialized`、一次 `tools/call`，最后用 `DELETE` 结束会话（wget 做不到，交给服务端超时）。应答可以是 JSON，也可以是 SSE。工具的地址取自 `LERRYN_TOOLS`，只能调用本次运行引用的 `mcp` 工具。请求头由任务用 `-H` 给出，令牌从注入的凭证里取。
- 调用从容器直接发出，所以需要 `network: full`，并受部署的网络策略约束；`none` 的任务调用时连不上，记录为失败。由 runner 代为调用的工具网关仍在 P1。
- 每次调用向事件文件追加一条 `tool.call`：工具名、MCP 工具名、是否成功、耗时、参数紧凑 JSON 的前 200 个字符和失败原因，不记录返回内容。runner 校验字段，遮盖其中的凭证值，记下调用时所在的阶段，经 runner 协议的事件（新增可选字段 `tool_call`）交给 server；server 存为 `run_events` 中 `kind` 为 `tool.call` 的事件，`data` 是契约中的 `ToolCallRecord`，`message` 为“工具名 方法名”。旧版 server 不认识这类事件，所以先升级 server 再升级 runner。
- 命令把 CallToolResult 作为一行 JSON 打印到 stdout。工具返回 `isError`、连不上或应答出错时退出码为 1，参数有误时为 2。

## 8 调度与触发

### 8.1 Cron

- 必须指定 IANA 时区，夏令时切换按该时区的本地时间计算。本地时间不存在时（春季拨快跳过的那一小时），按跳过的时长顺延，例如 02:30 在 03:30 触发；本地时间出现两次时（秋季拨回重复的那一小时）只触发一次。同一个时刻不会触发两次。
- 表达式解析使用成熟的 cron 解析库（robfig/cron v3，只用它解析五段表达式和 `@daily` 这类简写），下一次触发时间和调度循环自行实现，因为去重、重叠和错过处理都需要自己掌控，库自带的计算在夏令时缺口处会落到切换之前。不接受 `@every` 和 `TZ=` 前缀：时区是单独的字段，触发时间必须是日历时间。时区数据随二进制嵌入，不依赖镜像里的 zoneinfo。
- 校验时返回人可读的频率描述，以及后续 5 次触发时间。
- 代码在 `server/internal/scheduler`（解析、描述、调度循环）和 `server/internal/service`（触发器、触发、webhook、重试）。
- 运行日历（`getRunCalendar`）用 `scheduler.Planner` 在请求的时间段内展开每个启用的 cron 触发器，夏令时规则与调度循环相同，日历上的时间就是实际触发的时间。日历最多返回 2000 项，展开也以此为界：凑满之后时间段收缩到已保留的最后一项，触发器再多、频率再高，计算量和内存都有上限。计划项带 `trigger_id` 和 `schedule_text`；概览的“接下来的触发”也用同一个描述。

### 8.2 调度循环

主节点每秒检查一次到期的触发器，对每个触发器在一个事务里完成：

1. 按读到的 `next_fire_at` 做比较并交换，把它移到下一次触发时间；没有更新到行说明其他节点或 `setTriggers` 已经处理，直接放弃。PostgreSQL 上第二个主节点在行锁上等待，提交后匹配不到行。
2. 插入 TriggerFire。（触发器 ID，计划时间）唯一，冲突即回滚，作为第二道保险。
3. 根据重叠策略和队列状态，创建排队中的运行，或者创建已跳过的记录并写明原因。

下一次触发时间总是从当前时间计算。计划时间早于当前时间超过 60 秒（`scheduler.missed_after_seconds`）视为停机错过。默认不补跑，只记录一条已跳过记录（`skip_reason` 为 missed），TriggerFire 的 `missed_count` 记错过的次数，运行的 `error_message` 写明时间范围。需要补跑时由 Agent 明确发起。触发器可以设 `catch_up: latest`，恢复后只补跑最近错过的一次，其余合并为一条已跳过记录；有未迟到的触发时，错过的全部记为已跳过。

任务未激活、已暂停时，到期的触发器只向后移动，不产生记录，所以重新激活后不会把暂停期间的时间算作停机错过。`next_fire_at` 在库里对启用的 cron 触发器始终有值，接口在任务未激活时返回 null。归档任务的触发器不再触发。

调度循环通过 `scheduler.Firer` 接口驱动 service，时钟可以替换，测试用假时钟逐秒推进，两种数据库都跑同一组测试。循环挂在选主（第 4.4 节）的 `leaderTasks` 上，只在主节点运行。

### 8.3 Webhook

- 每个 webhook 触发器有独立的 URL（`/hooks/{triggerId}`，在 `/api/v1` 之外单独挂载）和令牌（`wl_hook_` 前缀，只存哈希），令牌放在 `Authorization` 请求头中，可以单独轮换和撤销。令牌不对、缺失或触发器不存在一律返回 401，不暴露哪些触发器存在。HMAC 签名放到 P1。
- 请求体大小有上限，默认 1 MiB，按触发器限流（`scheduler.webhook_rate_per_minute`，每个 server 单独计数）。
- 请求体存入对象存储作为运行输入（`runs.input_key`），运行中以文件形式提供：领取结果带 `has_input`，runner 在准备阶段经 `POST /runner/v1/runs/{id}/input` 取回请求体，写到运行目录的 `in/body`，只读挂载到容器的 `/sbx/in`，媒体类型放进 `LERRYN_INPUT_CONTENT_TYPE`（去掉控制字符）。没有输入的运行不挂载 `/sbx/in`。
- 调用方可以带 `Idempotency-Key`，重复投递不会产生多次运行。幂等记录的主体为 `trigger:<id>`，与运行写在同一个事务里；主节点每小时清理过期记录。

### 8.4 队列与并发

- 全局并发等于在线 runner 的槽位总数。
- 同一任务是否允许重叠由任务清单决定，默认不重叠。
- 排队数量有上限：整个部署（`scheduler.max_queued_runs`，默认 1000）和单个项目（`scheduler.max_queued_runs_per_project`，默认 200）。定时、webhook 和重试遇到队列已满，记录为已跳过；API 发起的运行返回 `queue_full`。不无限积压。上限按计数检查，并发进入时可能超出几条，它限制积压，不是锁。
- 项目级并发（`scheduler.project_concurrency`，默认 0 不限）在领取时检查：项目已有这么多运行处于准备中、运行中或取消中时，它的排队运行留在队列里。设了上限时逐条领取，每条语句都能看到前一条的结果；PostgreSQL 上不同 server 的并发领取看不到彼此未提交的结果，每个领取方最多多领一条，它是配额，不是锁。原计划放到 P1，M3 按实施规划提前实现。

### 8.5 重试

自动重试默认关闭。任务可以设置最多重试次数和等待时间（清单的 `retry`）。以下情况不重试：取消、凭证缺失或被撤销、校验失败、超过队列上限、测试运行。每次重试是一条新运行（`trigger_kind` 为 retry，`retry_of` 指向上一次尝试，`attempt` 递增），在队列中等到上一次结束时间加等待时间（`not_before`，领取查询会跳过未到时间的运行）。

主节点每秒扫描已结束、还有剩余次数、尚未决定的尝试（`runs.retry_decided_at` 为空，迁移 20261105020000），先按比较并交换写入决定时间再建重试，`retry_of` 上的唯一索引保证一次尝试最多一个重试。重试时任务已归档记为已跳过（inactive）；不允许重叠的任务此时已有运行，记为已跳过（overlap），重试链到此结束。

## 9 API 设计

### 9.1 契约与生成链路

```
api/openapi/ (OpenAPI 3.1 源文件，按资源拆分)
 └─► api/bundled/openapi.yaml（Redocly 打包，提交入库）
      ├─► Go 服务端接口（oapi-codegen strict server）
      ├─► TS 类型与客户端（packages/api）→ Web、移动端
      ├─► 嵌入二进制（api.Spec）→ MCP 工具注册表与操作索引 → MCP、CLI；请求校验
      ├─► Skill 参考资料（清单 schema、错误码表、工具列表）
      └─► 接口文档页（/api/docs）
```

所有接口改动从修改规范开始，写法约定（拆分、命名、`x-operation-kind`、`x-mcp`、错误、分页、鉴权、3.1 写法）见 `api/README.md`。生成物不手工编辑，CI 会重新生成并检查有没有差异。

- `make generate` 依次打包规范、运行 `go generate`（oapi-codegen）、生成 TS 类型，需要 Go 和 Node 两套工具链。CI 为此单独设了 `api` 作业，运行 `make generate-check` 和 Spectral。
- 打包结果 `api/bundled/openapi.yaml` 是两端生成的输入，也由 `api/spec.go` 用 go:embed 嵌入服务端。MCP 工具注册表不生成代码，server 启动时从嵌入的契约构建（第 10.3 节）。
- oapi-codegen 的三项配置来自 S2 的问题清单：`skip-prune` 让只被 `webhooks` 引用的 schema 不被裁掉，否则生成的代码无法编译；`always-prefix-enum-values` 让枚举常量一律带类型前缀，以后新增含相同取值的枚举时，已有常量不会改名；`ToCamelCaseWithInitialisms` 统一缩写的大小写。
- S2 验证的 3.1 写法里有两种会让 oapi-codegen 静默出错：把 `null` 写进 `enum`，以及 `oneOf` + `const` 枚举的分支缺 `title`。另外 `Problem` 不声明 `additionalProperties: true`，扩展字段逐个声明，否则部分响应类型会在编码时丢掉附加字段。这些约定写在 `api/README.md` 中，前两条由 Spectral 规则拦截。

### 9.2 主要端点

以下是首版的端点草案，最终以 `api/openapi/` 为准。类型列对应 `x-operation-kind`：读取、变更、执行。

| 分组 | 方法与路径 | operationId | 类型 |
| --- | --- | --- | --- |
| 发现 | `GET /api/v1/discover` | discover | 读取 |
| 账号 | `POST /api/v1/auth/register` | register | 变更 |
| 账号 | `POST /api/v1/auth/login` | login | 变更 |
| 账号 | `POST /api/v1/auth/logout` | logout | 变更 |
| 账号 | `GET /api/v1/auth/oidc/start`、`GET /api/v1/auth/oidc/callback` | oidcStart、oidcCallback | 读取（GET 只能是读取；登录的副作用只有建立会话） |
| 账号 | `POST /api/v1/auth/pairing` | createPairingCode | 变更 |
| 账号 | `POST /api/v1/auth/pairing/exchange` | exchangePairingCode | 变更 |
| 个人 | `GET /api/v1/me`、`PATCH /api/v1/me` | getMe、updateMe | 读取、变更 |
| 个人 | `POST /api/v1/me/password` | changePassword | 变更 |
| 个人 | `GET /api/v1/me/tokens`、`POST /api/v1/me/tokens`、`DELETE /api/v1/me/tokens/{tokenId}` | listTokens、createToken、revokeToken | 读取、变更 |
| 项目 | `GET /api/v1/projects`、`POST /api/v1/projects` | listProjects、createProject | 读取、变更 |
| 项目 | `PUT /api/v1/projects/{projectId}/members/{userId}`、`DELETE` 同路径 | setMember、removeMember | 变更 |
| 任务 | `GET /api/v1/tasks` | listTasks | 读取 |
| 任务 | `PUT /api/v1/projects/{projectId}/tasks/{taskName}` | applyTask | 变更 |
| 任务 | `GET /api/v1/tasks/{taskId}` | getTask | 读取 |
| 任务 | `PUT /api/v1/tasks/{taskId}/tags` | setTaskTags | 变更 |
| 任务 | `GET /api/v1/tasks/{taskId}/versions`、`GET /api/v1/tasks/{taskId}/versions/{seq}` | listTaskVersions、getTaskVersion | 读取 |
| 任务 | `GET /api/v1/tasks/{taskId}/versions/{seq}/diff` | diffTaskVersion | 读取 |
| 任务 | `POST /api/v1/tasks/{taskId}/versions/{seq}/validate` | validateTaskVersion | 读取 |
| 任务 | `POST /api/v1/tasks/{taskId}/activation` | setTaskActivation | 执行 |
| 触发器 | `GET /api/v1/tasks/{taskId}/triggers`、`PUT` 同路径 | listTriggers、setTriggers | 读取、变更 |
| 触发器 | `POST /api/v1/triggers/{triggerId}/rotate-token` | rotateTriggerToken | 变更 |
| 运行 | `POST /api/v1/tasks/{taskId}/runs` | startRun | 执行 |
| 运行 | `GET /api/v1/runs`、`GET /api/v1/runs/{runId}` | listRuns、getRun | 读取 |
| 运行 | `POST /api/v1/runs/{runId}/cancel` | cancelRun | 执行 |
| 运行 | `POST /api/v1/runs/{runId}/rerun` | rerunRun | 执行 |
| 运行 | `GET /api/v1/runs/{runId}/events` | listRunEvents | 读取 |
| 日志 | `GET /api/v1/runs/{runId}/logs` | readRunLogs | 读取 |
| 日志 | `GET /api/v1/runs/{runId}/logs/stream`（SSE） | streamRunLogs | 读取 |
| 日志 | `GET /api/v1/runs/{runId}/logs/download` | downloadRunLogs | 读取 |
| 产物 | `GET /api/v1/runs/{runId}/artifacts`、`GET /api/v1/artifacts/{artifactId}/download` | listArtifacts、downloadArtifact | 读取 |
| 事件 | `GET /api/v1/events/stream`（SSE） | streamEvents | 读取 |
| 凭证 | `POST /api/v1/credential-requests`、`GET /api/v1/credential-requests/{requestId}` | createCredentialRequest、getCredentialRequest | 变更、读取 |
| 凭证 | `GET /api/v1/credentials`、`DELETE /api/v1/credentials/{credentialId}` | listCredentials、revokeCredential | 读取、变更 |
| 凭证 | `PUT /api/v1/credentials/{credentialId}/grants/{taskId}`、`DELETE` 同路径 | grantCredential、revokeGrant | 变更 |
| 工具 | `GET /api/v1/tools`、`POST /api/v1/projects/{projectId}/tools`、`DELETE /api/v1/tools/{toolId}` | listTools、registerTool、removeTool | 读取、变更 |
| 通知 | `GET /api/v1/notification-channels`、`POST`、`DELETE /{channelId}` | listChannels、createChannel、deleteChannel | 读取、变更 |
| Skill | `GET /api/v1/skills`、`GET /api/v1/skills/{name}` | listSkills、getSkill | 读取 |
| 管理 | `GET /api/v1/admin/users`、`POST /api/v1/admin/users`、`PATCH /api/v1/admin/users/{userId}` | adminListUsers、adminCreateUser、adminUpdateUser | 读取、变更 |
| 管理 | `GET /api/v1/admin/settings`、`PATCH /api/v1/admin/settings` | getSettings、updateSettings | 读取、变更 |
| 管理 | `GET /api/v1/admin/runners`、`PATCH /api/v1/admin/runners/{runnerId}`、`POST /api/v1/admin/runner-tokens` | listRunners、updateRunner、createRunnerToken | 读取、变更 |
| 管理 | `GET /api/v1/admin/audit-events` | listAuditEvents | 读取 |

上表是草案。0.5 版写出了 M2 至 M6 和控制台需要的完整契约，与上表的差异以 `api/openapi/` 为准，主要有：

- 任务：`listTasks` 带 `project_id`、`name`（精确匹配，供 CLI 按名字找任务）、`state`、`q`、`archived` 筛选；新增 `getTaskFile`（`GET /tasks/{taskId}/versions/{seq}/file?path=`）、`listTaskCredentials`、`archiveTask`（`DELETE /tasks/{taskId}`，归档而不删除）。`applyTask` 返回 `ApplyTaskResult`（`dry_run`、`created`、`version`、`task`、`diff`、`warnings`），预览用查询参数 `dry_run`，并发保护用请求头 `If-Match`（值为读到的 `latest_version.seq`）。
- 任务标签：`listTasks` 支持按 `tag` 精确筛选，在游标分页前生效，可以和其他筛选组合。任务详情和列表返回 `tags`，无标签时为空数组。`setTaskTags` 由有任务修改权限的调用者整体替换标签，空数组清空；最多 20 个不重复的标签，每个 1 至 40 个字符，支持中文，区分大小写。归档后仍可读取标签，不能修改。写入和 `task.tags` 审计在同一个事务中完成；审计只记数量，不记标签内容。标签属于项目内的分类元数据，不随任务版本或任务包迁移。
- 运行：`listRuns` 带项目、任务、状态（可多选）、来源、测试、`rerun_of`、`runner_id` 和时间筛选；`listRunEvents` 用游标分页；新增 `streamRunLogs`、`downloadRunLogs`、`streamEvents` 的 SSE 和下载约定（`api/README.md`）。
- 新增控制台概览 `getOverview`（需要处理的事、未结束的运行、接下来的触发）和 `getRunCalendar`。
- 项目新增 `getProject`、`updateProject`、`listMembers` 和 `searchUsers`（`GET /api/v1/users`，按名字开头查找，用于添加成员）。
- 个人新增 `listDevices`、`revokeDevice`；`login` 带 `device` 时建立只读的设备会话；`exchangePairingCode` 的请求带 `server` 和 `device`。
- 管理新增 `adminResetPassword`；runner 的管理接口放在 `/api/v1/admin/runners` 下。
- 任务凭证新增 `listCredentialRequests`、`cancelCredentialRequest`、`getCredential`；更新已有凭证也走填写请求（`createCredentialRequest` 带 `credential_id`），明文只经过填写请求的提交。控制台的填写框用 `fillCredentialRequest`（`POST /api/v1/credential-requests/{requestId}/values`）提交，只接受登录会话（第 12.4 节）。
- 个人 AI 配置经 `GET /api/v1/me/ai` 读取、`PUT` 保存、`DELETE` 移除，这三个操作只接受登录会话。Agent 经 `GET /api/v1/ai/models` 读取模型，通过 `POST /api/v1/ai/chat` 调用，见第 10.2 节。
- 通知渠道属于用户本人，订阅写在渠道里（`subscriptions`），新增 `updateNotificationChannel`、`testNotificationChannel`、`listNotificationDeliveries`；通用 webhook 的载荷 `NotificationEvent` 和签名规则写在契约的 `webhooks` 中。
- Skill：`listSkills`（`GET /api/v1/skills`）返回版本和每个 Skill 的文件清单，`getSkill`（`GET /api/v1/skills/{name}`）返回全部文件内容。两者允许匿名调用，与 `discover` 相同，Skill 本来就随二进制公开发行；都不作为独立的 MCP 工具暴露（`x-mcp: {expose: false}`），MCP 客户端读 resource，或经 `api_call` 调用。
- 工具：`listTools`、`registerTool`、`removeTool` 的最小实现见第 7.8 节。
- 任务包：`exportTask`（`GET /api/v1/tasks/{taskId}/export`）和 `importTask`（`POST /api/v1/projects/{projectId}/task-imports`），见第 15.5 节。导入的路径不放在 `/projects/{projectId}/tasks/` 下，因为 `import` 本身是合法的任务名。

不在 `/api/v1` 下的入口：

| 路径 | 用途 |
| --- | --- |
| `POST /hooks/{triggerId}` | webhook 触发，用触发器令牌鉴权。契约中声明为 `triggerWebhook`，安全方案为 `triggerToken`；它不经过 `/api/v1` 的鉴权中间件，由 `server/internal/app` 单独挂载 |
| `GET /c/{requestId}`、`POST /c/{requestId}` | 凭证填写页，服务端渲染 |
| `/mcp` | MCP 端点，工具调用转发到 `/api/v1` 的处理链（第 10.3 节） |
| `/healthz`、`/readyz`、`/metrics` | 健康检查和指标；`/readyz` 同时检查迁移版本（第 15.6 节） |

`applyTask` 按项目和任务名定位：任务不存在时创建并生成第 1 个版本；已存在时生成新版本，必须带 `If-Match`。Agent 只需要知道任务名就能提交，不用先查 ID。

`discover` 允许匿名调用，响应包含：

| 字段 | 内容 |
| --- | --- |
| `server` | 版本、提交、构建时间 |
| `api` | 主版本（`v1`）和契约版本（`info.version`） |
| `capabilities` | 登录方式、是否开放注册、可用的网络策略、是否要求激活前测试 |
| `runtimes` | 运行时目录，任务清单的 `runtime` 只能从这里选 |
| `skills` | 与服务端匹配的 Skill 版本和列表 |
| `principal` | 当前调用者的身份和有效范围，匿名时为 null |
| `links.console` | 控制台首页 |

各字段只做增量扩展，Agent 遇到不认识的字段应忽略。设置服务落地之前，`capabilities` 中的开放注册和激活前测试返回第 12.1 节、第 5.2 节规定的默认值，之后改为读取数据库。

### 9.3 通用约定

- **路径版本**：`/api/v1`。v1 内只做增量变更，破坏性变更进入 v2，并保留过渡期。
- **操作分类**：每个操作用 `x-operation-kind` 标为 `read`、`mutate` 或 `execute`，用于 MCP 标注、访问令牌范围和审计。令牌范围与它一一对应：`read` 操作需要 `read`，`mutate` 需要 `write`，`execute` 需要 `execute`。GET 只能是 `read`。
- **鉴权**：Agent 和 CLI 用访问令牌（`Authorization: Bearer wl_pat_…`），Web 控制台和移动端用名为 `lerryn_session` 的 cookie 会话（第 12.1 节）。契约的全局 `security` 要求两者之一；允许匿名调用的操作（例如 `discover`）在 `security` 中加一项空对象。令牌范围不写进 `security`，由 `x-operation-kind` 推出。
- **请求 ID**：每个响应带 `X-Request-Id`，由服务端为每个请求新生成（`req_` 加 26 位 base32），不采用客户端传入的值，否则客户端可以决定写进日志和审计的 ID。错误体中的 `request_id` 与它相同。
- **请求校验**：oapi-codegen 的 strict server 只做参数绑定和 JSON 解码，不按 schema 校验请求体，缺必填字段、`const` 不符、未知判别值都会原样交给处理函数。M1 在 `/api/v1` 上加了 kin-openapi `openapi3filter` 请求校验中间件（`httpapi/validate.go`），按嵌入的契约（`api.Spec`）校验参数和请求体，未知字段、越界取值都在进入处理函数之前拦下，返回 400 `validation_failed`，`errors` 按 JSON 指针或参数名逐条给出。MCP 的工具调用走同一条处理链，得到同样的校验。几个细节：校验排在认证之后，调用方没有权限的请求不做校验，直接交给权限检查回答 401 或 403，没有权限的调用方从校验错误里得不到信息；请求体上限 4 MiB，超出返回 413；错误详情只取原因，不带原值，因为 kin-openapi 的完整错误信息会引用请求里的值，其中可能有密码；`AuthenticationFunc` 显式设置，检查认证中间件放进 context 的身份与安全方案一致，kin-openapi 默认的 `NoopAuthenticationFunc` 会让鉴权被绕过（GO-2026-6095）。处理函数里的内容校验（例如阶段依赖成环、文件路径越界）仍返回 422。
- **可空字段**：可空的标量和数组写 `type: [T, "null"]`，可空的对象引用写 `oneOf: [$ref, {type: "null"}]`，Go 端都映射为指针。"可选且可空"的字段在 Go 端无法区分"缺省"和 null，这对响应没有影响，对 PATCH 请求体有影响（"不修改"和"清空"分不开）。约定保持默认的指针映射，PATCH 请求体不使用可选且可空的字段，清空用明确的值表达；确有三态需求时，再整体启用 oapi-codegen 的 `nullable-type`。这条在第一个 PATCH 接口（`updateMe`）之前生效。
- **分页**：统一游标分页，`?limit=&cursor=`，返回 `items` 和 `next_cursor`，游标不透明。
- **幂等**：创建任务、发起运行、创建凭证请求等接口接受 `Idempotency-Key`。相同键和相同请求体，返回首次的结果；相同键、不同请求体，返回 `idempotency_conflict`。记录保留 24 小时。
  M2 的实现：幂等是 `/api/v1` 处理链上的一层中间件，排在认证和契约校验之后，契约里声明了 `Idempotency-Key` 请求头的操作都生效（目前是 `applyTask`、`startRun`、`rerunRun` 和 `createCredentialRequest`），MCP 的 `idempotency_key` 参数经同一条链生效。键按用户区分。请求是否相同，按方法、路径和查询、`If-Match` 以及规范化后的 JSON 请求体的哈希判断，字段顺序和空白不同仍算同一个请求。首次的响应连同状态码保存，包括 4xx 错误；5xx 和超过 1 MiB 的响应不保存，同一个键可以再试。首个请求还在处理时，重试得到 `idempotency_conflict`；处理它的 server 中途退出、记录 10 分钟没有完成时，键重新可用。重放的响应带 `Idempotent-Replayed: true`。
- **并发修改**：任务和触发器带版本号，更新时通过 `If-Match` 传入已读版本，不一致返回 412 和 `version_conflict`，并附当前版本号。`applyTask` 对已存在的任务缺少 `If-Match` 时返回 428 `precondition_required`；`If-Match: 0` 表示调用者认为任务尚不存在，任务已存在时同样返回 `version_conflict`。版本号的递增和比较在一条带条件的 UPDATE 里完成，两个基于同一版本的提交只有一个成功。
- **变更预览**：提交新版本时可以加 `dry_run=true`，只返回校验结果和差异。
- **长操作**：发起运行返回 202 和运行对象，包含运行 ID、状态和控制台链接。可选 `wait` 参数，最多等待指定时间后返回当前状态，减少 Agent 轮询次数。客户端断开不会取消运行。
- **实时**：SSE 接口的事件格式在规范中描述，处理函数手写。断线后用 `Last-Event-ID` 续读。M2 的实现：日志流在本进程收到日志分片时立即推送，另外每秒查一次分片表，覆盖写在其他 server 上的日志；每次读取都重新检查权限，成员被移出项目后流随之结束。运行变化的事件流经事件总线覆盖所有 server（第 4.4 节），订阅方落后超过 256 条时收到 `resync`；可见的项目列表每 30 秒刷新一次。server 开始停机时主动结束所有流，客户端按约定重连，不拖住优雅退出。
- **对外通知**：通知 webhook 的载荷在规范的 `webhooks` 字段中定义。
- **链接**：任务、运行等响应都带 `links.console`，指向控制台对应页面，由部署配置的 `server.public_url` 拼出完整地址，Agent 可以直接交给用户。
- **处理函数的约束**：MCP 工具调用在进程内构造的请求没有 `RemoteAddr`、`Host` 和 `TLS`（第 10.3 节），处理函数不能依赖它们。控制台链接用配置的公开地址生成；按客户端地址限流、记录来源地址时，从中间件放进 context 的值读取。

### 9.4 错误格式

错误统一使用 RFC 9457 problem details，媒体类型 `application/problem+json`，在标准字段之外扩展 `code`、`next_actions`、`request_id`，校验失败时另带 `errors`：

```json
{
  "type": "urn:lerryn:error:credential_missing",
  "title": "缺少凭证",
  "status": 422,
  "code": "credential_missing",
  "detail": "任务引用的凭证 LLM_API_KEY 尚未填写",
  "instance": "/api/v1/tasks/…/runs",
  "next_actions": [
    {"action": "credential_request", "hint": "为该任务创建凭证填写请求，并把链接交给用户"}
  ],
  "request_id": "req_7FZ2CQ4M3XKJ6N5W2Y4T3A6B"
}
```

- `type` 固定为 `urn:lerryn:error:<code>`。`title` 由 code 决定，不随请求变化，服务端为每个 code 维护一份中文 title。
- `code` 是 Agent 判断错误类别的唯一依据，`detail` 只给人和 Agent 阅读，不用于程序判断。
- `instance` 是出错的请求路径。
- `next_actions` 中的 `action` 取 MCP 工具名或 operationId。
- `errors` 只在 `validation_failed` 时出现，每项是一个 `detail`，加上 `pointer`（请求体中的 JSON Pointer）、`parameter`（查询或路径参数名）、`header`（请求头名）三者之一，指明出错的位置，格式参照 RFC 9457 的示例。
- 某个错误需要额外字段时（例如 `version_conflict` 附当前版本号），在 `Problem` 中把它声明为可选属性，不给 `Problem` 加 `additionalProperties: true`（第 9.1 节）。

错误码在契约中用 `ErrorCode` 声明，HTTP 错误的 `code` 和运行失败的错误类别共用这一组值。新增错误码只能追加，不能改名或删除。状态码以 `api/README.md` 的对应表为准：

| code | 状态码 | 含义 | 下一步 |
| --- | --- | --- | --- |
| `validation_failed` | 400：参数或请求体格式不对；422：格式正确但内容校验不通过，例如清单 | 清单或参数校验失败 | 按 `errors` 字段修正 |
| `unauthenticated` | 401 | 缺少有效的访问令牌或会话 | 在 `Authorization` 头中提供有效的访问令牌 |
| `insufficient_scope` | 403 | 访问令牌范围不足，或用访问令牌调用了只接受会话的操作 | 换用范围足够的令牌，或在控制台中操作 |
| `registration_disabled` | 403 | 管理员已关闭开放注册 | 联系管理员创建账号，或使用单点登录 |
| `origin_not_allowed` | 403 | 用会话发起的写操作缺少 `Origin`，或来源不是部署地址 | 从控制台页面发起；Agent 和脚本改用访问令牌 |
| `not_found` | 404 | 资源不存在或无权访问 | 确认 ID 和权限 |
| `expired` | 410 | 日志或产物已按保留策略清理，运行记录仍在（第 6.6 节） | 需要时重新运行，不必重试读取 |
| `manual_run_disabled` | 409 | 任务关闭了控制台的手动执行，用浏览器会话发起运行（第 13.3 节） | 需要时请 Agent 用 `setTaskConsole` 打开 |
| `idempotency_conflict` | 409 | 同一幂等键对应不同请求 | 换新的幂等键，或使用原请求 |
| `username_taken` | 409 | 用户名已被占用，不区分大小写 | 换一个用户名 |
| `version_conflict` | 412 | 任务已被其他会话修改 | 读取最新版本和差异后重新提交 |
| `credential_missing` | 422 | 引用的凭证不存在或未填写 | 创建填写请求 |
| `credential_unavailable` | 422 | 凭证已撤销、过期，或未授权给该任务 | 请所有者更新或授权 |
| `queue_full` | 429 | 队列已满 | 稍后重试 |
| `rate_limited` | 429 | 短时间内请求过多，例如连续登录失败 | 一分钟后再试 |
| `internal_error` | 500 | 服务端内部错误，细节只写进服务端日志 | 稍后重试；持续出现时把 `request_id` 交给管理员 |
| `runner_unavailable` | 503 | 没有满足标签要求的在线 runner | 联系管理员，或调整标签 |
| `dependency_install_failed` | 不作为 HTTP 错误 | 依赖安装失败 | 查看准备阶段日志，修正锁文件 |
| `script_error` | 不作为 HTTP 错误 | 脚本以非 0 退出 | 查看日志和失败阶段 |
| `timeout` | 不作为 HTTP 错误 | 超过执行时限 | 优化脚本，或调整时限 |
| `out_of_memory` | 不作为 HTTP 错误 | 容器内进程超出内存上限被杀，且主进程非 0 退出（第 5.4 节） | 查看日志，减少内存占用，或调大 `limits.memory` |
| `run_interrupted` | 不作为 HTTP 错误 | 运行进行中时 runner 重启或失联（第 7.6 节），容器已停止 | 查看保留的日志，确认脚本可以重复执行后重新发起 |

标为"不作为 HTTP 错误"的几项只出现在运行记录的错误类别中。`out_of_memory` 和 `run_interrupted` 是 0.3 版新增的类别，M1 实现运行记录时追加进契约。

没有查看权限的资源一律返回 `not_found`，不透露资源是否存在。

### 9.5 契约治理

- Spectral 检查规范风格：每个操作必须有 `operationId`（lowerCamelCase，全局唯一）、`tags`、中文的 `description`、`x-operation-kind`（GET 只能是 `read`）、`x-mcp`（结构见第 10.3 节，工具名全局唯一），以及引用 `Problem` 的错误响应；JSON 字段用 snake_case，公开接口在 `/api/v1` 下；另有两条规则拦住第 9.1 节提到的会让 oapi-codegen 静默出错的写法。
- Spectral 检查打包结果 `api/bundled/openapi.yaml`，不检查源文件：Spectral 6.17 读取多文件的 3.1 源文件时，会对外部引用的路径项误报，并把所有组件当成未使用。报错位置因此指向打包结果，按组件名或 operationId 回到源文件修改。
- `httpapi` 的测试用 kin-openapi 按打包结果校验处理函数的真实响应：状态码已声明、媒体类型正确、响应体符合 schema。生成的 Go 类型管不到必填、可空和枚举约束，新接口的测试都要做这项校验。
- oasdiff 对比上一个发布版本，v1 内的破坏性变更直接拦下。第一个版本发布后启用。
- 规范按资源拆成多个文件（`api/openapi/<资源>/`），用 Redocly CLI 打包。打包结果提交入库，组件名冲突时打包直接失败。
- Redocly CLI 和 Spectral CLI 默认会上报使用统计，仓库里都已关闭。

## 10 MCP

### 10.1 定位

MCP 是 OpenAPI 的一层外壳，不包含业务逻辑。它和 HTTP API 共享鉴权、校验、错误处理和审计，平台不为 MCP 设独立的管理员身份。

### 10.2 工具清单

| 工具 | 类型 | 对应接口 |
| --- | --- | --- |
| `discover` | 读取 | discover |
| `environment_get` | 读取 | getEnvironment |
| `task_get` | 读取 | getTask |
| `task_list` | 读取 | listTasks，可按标签筛选 |
| `task_set_tags` | 变更 | setTaskTags，整体替换任务标签 |
| `task_apply` | 变更 | applyTask，带 `expected_version`，支持 `dry_run` |
| `task_validate` | 读取 | validateTaskVersion |
| `trigger_set` | 变更 | setTriggers |
| `task_set_active` | 执行 | setTaskActivation |
| `run_start` | 执行 | startRun，带幂等键 |
| `run_get` | 读取 | getRun |
| `run_logs` | 读取 | readRunLogs |
| `run_cancel` | 执行 | cancelRun |
| `run_rerun` | 执行 | rerunRun |
| `credential_request` | 变更 | createCredentialRequest |
| `ai_models` | 读取 | listAIModels |
| `ai_chat` | 执行 | createAIChat |
| `api_describe` | 读取 | 按 operationId 或关键词返回接口说明和 schema |
| `api_call` | 视接口而定 | 按 operationId 调用任意公开接口 |

Agent 首次接入加载表中的工具。其他接口通过 `api_describe` 查询，可调用的操作经 `api_call` 访问。

`getEnvironment` 读取 `server.environment`，未配置时返回 `unspecified`，访问令牌需要 `read` 范围。部署环境与单次运行的 `test` 标记独立。

个人 AI 配置只能在 Web 登录会话中修改。配置与密钥复用凭证库的加密和轮换，内部凭证不能通过普通凭证接口读取或授权给任务。`ai_chat` 按令牌所属账号读取配置，检查模型 ID 后从服务端发起请求。AI 配置的地址和模型、聊天请求及返回文本沿用凭证指纹检查。AI 出口沿用通知的连接地址检查，单独由 `ai.allowed_networks` 放行需要访问的内网网段。请求使用 HTTPS，不跟随重定向。

### 10.3 工具注册与调用路径

**`x-mcp`。** 每个 OpenAPI 操作都声明 `x-mcp`，明确是否作为独立的 MCP 工具暴露。结构以 `api/README.md` 为准：

```yaml
x-mcp:
  expose: true                 # 必填。false 时只能经 api_call 调用
  tool: discover               # expose 为 true 时必填
  title: 发现平台能力            # 可选
  description: |               # expose 为 true 时必填
    读取 Lerryn 服务端的版本、可用运行时……
  hints:                       # 可选
    destructive: true
```

| 字段 | 说明 |
| --- | --- |
| `expose` | 必填。是否注册为独立的工具。为 false 时只能经 `api_call` 调用，写成 `x-mcp: {expose: false}`，不能带其他字段 |
| `tool` | `expose` 为 true 时必填。工具名，snake_case，全局唯一，最长 64 个字符 |
| `title` | 可选。工具的显示名，缺省时取操作的 `summary` |
| `description` | `expose` 为 true 时必填。面向 Agent 的说明，可以与操作的 `description` 不同，应更紧凑。用中文书写，与契约中的其他说明一致；工具名和参数名用英文 snake_case |
| `hints` | 可选。覆盖按规则推出的工具标注，字段为 `destructive`、`idempotent`、`open_world`，只在推导结果不对时使用 |

一个工具只对应一个操作，不做组合。Spectral 检查 `x-mcp` 的结构和工具名的唯一性；注册表加载时遇到未知字段也直接报错，防止拼写错误被静默忽略。

**工具标注。** 按 `x-operation-kind` 推出，`hints` 中给出的字段覆盖推导结果：

| x-operation-kind | readOnlyHint | destructiveHint | idempotentHint | openWorldHint |
| --- | --- | --- | --- | --- |
| `read` | true | false | true | false |
| `mutate` | false | 方法为 DELETE 时为 true，否则 false | 方法为 PUT 或 DELETE 时为 true，否则 false | false |
| `execute` | false | true | false | true |

`execute` 标为 openWorld，因为运行的是用户代码，可能访问外部系统；`read` 和 `mutate` 只改动 Lerryn 自己的数据。`read` 配 PUT、PATCH、DELETE，或 `mutate`、`execute` 配 GET，视为契约错误，加载时报错。`hints` 的典型用途是 `setTriggers`：它用 PUT 整体替换触发器，推导出 destructive 为 false，但它会删除未列出的触发器，应写 `hints: {destructive: true}`。

**输入 schema。** 由操作的参数和请求体生成：

- 路径、查询、请求头参数成为顶层参数，名称转为 snake_case，例如 `runId` 变为 `run_id`，`Idempotency-Key` 变为 `idempotency_key`。路径参数一律必填。`Authorization`、`Accept`、`Content-Type` 请求头由转发层设置，不进入输入。
- `application/json` 请求体是普通对象时，属性平铺为顶层参数，请求体必填时其 `required` 进入顶层；请求体是数组、组合 schema 或没有属性的对象时，整体作为一个 `body` 参数。
- `$ref` 内联展开，因为部分客户端不解析工具 schema 里的 `$ref` 和 `$defs`。`readOnly` 的属性从输入中去掉；`discriminator`、`example`、`x-*` 等对客户端没有意义的字段去掉；3.1 的 `examples`、`const` 和类型数组原样保留。顶层为 `type: object`，`additionalProperties: false`。
- 不输出 `outputSchema`。它会增加 `tools/list` 的体积，而且按规范 `structuredContent` 要符合它，错误结果显然不符合。
- 以下写法无法忠实映射，注册表加载时报错：Cookie 参数、参数与请求体属性重名、递归 schema。

**运行时构建。** 工具注册表不生成代码。契约由 `api/spec.go` 用 go:embed 嵌入（`api.Spec`），server 启动时据此构建注册表，加载失败直接退出。选择运行时构建的理由：

- `api_describe` 要按 operationId 返回 schema，`api_call` 要调用任意公开接口，运行时本来就需要一份完整的操作索引。主流程工具和这两个通用工具走同一份代码，不必维护两条路径。
- 工具是数据，转发逻辑是通用的，代码生成换不来额外的编译期检查。
- 契约错误同样在 CI 中暴露：测试加载真实契约（`TestLoadContract`），加载失败测试就失败。生成代码的另一个好处是改动在 diff 里可见，这一点由工具清单的 golden 文件承担：工具名、说明、输入 schema、标注有任何变化，测试都会失败，需要更新 golden 文件，评审时能看到差异。目前 golden 测试覆盖测试规范，真实契约的 golden 测试随主流程工具加入。
- 开销很小：62 个工具的规范构建一次约 7.5 毫秒。

**操作索引与通用工具。** 注册表为契约中的每个操作建立索引，不论是否暴露为独立工具。参数映射规则与独立工具相同，独立工具和 `api_call` 共用同一份映射和转发代码。

- `x-operation-kind` 与方法矛盾、operationId 重复，对所有操作都是加载错误。参数无法映射（非 JSON 请求体、Cookie 参数、参数重名、递归 schema）只对暴露的操作报错；未暴露的操作仍进入索引，标为不可调用，`api_describe` 说明原因。这样契约中加入文件上传之类的接口时，服务端不会因此无法启动。
- `api_describe`：传 `operation_id` 返回方法、路径、说明、`x-operation-kind`、所需令牌范围、对应的独立工具、是否允许匿名、是否只接受会话、输入 schema 和第一个 2xx 响应的 schema；传 `query` 按关键词搜索，多个词须全部匹配；都不传时列出全部操作。响应 schema 去掉 `writeOnly` 属性，保留 `readOnly` 属性，与输入相反。
- `api_call`：参数为 `operation_id` 和 `arguments`，`arguments` 的结构就是 `api_describe` 返回的输入 schema。请求经同一条 `/api/v1` 处理链，令牌范围、校验和错误都由 API 处理，MCP 层不另做权限判断。进程内请求的 `ToolCall` 为 `{api_call, <operationId>}`，结果的 `_meta` 另带 `lerryn/operation_id`。响应是事件流的操作和参数无法映射的操作直接返回 `validation_failed` 工具错误，不调用 API。参数错误的 `next_actions` 指向 `api_describe`。
- `api_call` 可能调用任何类型的操作，工具标注取最保守的组合：非只读、destructive、非幂等、openWorld。
- 两个通用工具的名称保留，契约中的工具不能使用。

**调用路径。** `/mcp` 挂在根路由上，工具调用转发的目标是 `/api/v1` 的处理链（含全部中间件），不是根路由。工具调用因此经过与 REST 相同的中间件，也无法转回 `/mcp` 自身。

```
Agent ──POST /mcp──► Streamable HTTP（无状态）
                         ▼
                     工具 handler ── 参数 → 路径/查询/请求头/请求体
                         │           透传 Authorization
                         │           context 标记 ToolCall{tool, operationId}
                         ▼
                     /api/v1 处理链（与 REST 相同的路由和中间件）
                         ▼
                     有界的 ResponseWriter ── 2xx JSON → structuredContent
                                             problem+json → isError，保留 code、next_actions
                                             超过上限 → 截断
```

- 请求按服务端请求的约定在进程内构造：路径参数转义，拒绝空值、`.` 和 `..`；请求头参数拒绝换行；只透传 `Authorization`，客户端带来的 `Cookie`、`Mcp-Session-Id` 等不进入 API 请求。进程内请求没有 `RemoteAddr`、`Host` 和 `TLS`（第 9.3 节）。
- 未知参数、缺少路径参数、类型不对时直接返回 `validation_failed` 工具错误，不调用 API；其余校验交给 API，MCP 和 REST 返回同样的校验错误。
- 进程内请求的 context 带 `ToolCall{Tool, OperationID}`，用 `mcp.ToolCallFromContext` 读取。它只能在进程内设置，不能像请求头那样被客户端伪造，审计据此区分入口（第 12.7 节）。外层 `/mcp` 请求 context 中的值会传到进程内的 API 请求，`/mcp` 前的中间件放进去的请求 ID、客户端地址、已认证身份，API 链路可以直接读到。
- API 处理链中的 panic 被捕获并转为 MCP 错误。SDK 在 HTTP 服务器的 goroutine 之外执行工具 handler，`net/http` 按连接的恢复覆盖不到，不捕获的话整个进程会退出。
- 响应转换：2xx 的 JSON 对象成为 `structuredContent`，同一段 JSON 也作为文本返回；JSON 数组或标量只返回文本。`application/problem+json` 转为 `isError: true` 的工具错误，`structuredContent` 和文本都是 problem 本身，只把文本交给模型的客户端也能看到 `code` 和 `next_actions`。非 UTF-8 的二进制内容不返回，提示改用 HTTP API 下载。每个结果的 `_meta` 带 `lerryn/http_status`。

**长度上限。** 单次工具调用返回的 API 响应默认最多 32 KiB，可以用 `mcp.max_result_bytes` 调整（第 10.4 节）。超出时截断，这只是兜底：去掉 `structuredContent`（截断的 JSON 无法解析），文本按字符边界切开，末尾说明已显示和总字节数，`_meta` 另带 `lerryn/truncated` 和 `lerryn/total_bytes`，并提示用 `limit`、`cursor` 缩小请求。MCP 层不提供通用的续读游标，它不知道任意 JSON 应该从哪里接着读，重新调用也不一定幂等。分页由接口自己的 `limit`、`cursor` 负责：暴露为工具的列表和日志接口，单页大小要能放进这个上限，`readRunLogs` 尤其需要按字节限制单页大小。超出上限的部分不进入内存，有界的 ResponseWriter 只缓存上限内的字节，其余只计数。

这样一来，MCP 和 API 行为一致是由结构保证的，不依赖开发时同步修改两处代码。

### 10.4 协议版本与传输

- 使用 Streamable HTTP，端点为 `/mcp`，SDK 以无状态模式运行。任何一个 server 副本都能处理任何请求，负载均衡不需要会话粘滞。
- 2026-07-28 规范去掉了初始化握手，客户端直接发 `server/discover`、`tools/list`、`tools/call`。
- 旧版本协议（2025-03-26、2025-06-18、2025-11-25）的客户端同样不需要会话粘滞。`initialize` 由收到它的副本回答，响应里没有 `Mcp-Session-Id`，客户端之后也不会带；之后的请求落到任何副本，SDK 都为它合成默认的初始化状态，没见过握手的副本也能处理 `tools/call`。打开独立 SSE 流的 GET 和结束会话的 DELETE 返回 405。
- 无状态模式的代价：服务端不能主动向客户端发请求（sampling、elicitation、roots），`list_changed` 通知没有通道送达，调用期间的进度和日志通知只能随该请求的 SSE 流返回。Lerryn 的工具清单随服务端版本固定，不依赖这些能力。
- 当前客户端的协议分布：Claude Code 已用 2026-07-28，Codex 仍用 2025-06-18，两者都在 S3 中连接并调用成功。Cursor、Claude Desktop、VS Code，以及经过真实负载均衡器的多副本部署，尚未验证。

`mcp` 配置段（第 15.3 节）：

| 配置项 | 默认 | 说明 |
| --- | --- | --- |
| `disable_localhost_protection` | false | SDK 默认开启 DNS rebinding 保护：请求到达回环地址、而 `Host` 不是回环地址时返回 403。同机的反向代理转发到 `127.0.0.1` 并保留公网 `Host` 时，正好触发这一条，需要设为 true。每次工具调用都需要访问令牌，DNS rebinding 拿不到令牌，关闭是安全的 |
| `json_response` | false | 设为 true 时每个请求返回一个 `application/json` 响应体，不用 SSE 流，适合会缓冲 SSE 的代理。两个已验证的客户端都兼容，代价是调用期间没有进度通知 |
| `max_result_bytes` | 0，即 32 KiB | 单次工具调用返回的 API 响应上限（第 10.3 节） |

部署要求：

- 反向代理对 `/mcp` 关闭响应缓冲，放宽读超时。Claude Code 在列出工具后会发一个挂起的 `subscriptions/listen` 请求，直到客户端退出才结束；被代理周期性断开也无害，因为 Lerryn 不发 `list_changed`。
- `/mcp` 的请求体上限与 API 保持一致。SDK 默认 4 MiB，`task_apply` 带文件时可能不够，M2 实现 `task_apply` 时一并调整。

### 10.5 授权

- 首版用访问令牌：用户在控制台创建，Agent 客户端放在 `Authorization` 请求头中。Claude Code、Codex、Cursor 等客户端都支持这种配置。
- 访问令牌代表某个用户，实际权限是用户权限与令牌范围的交集。
- `/mcp` 前加一层令牌校验：缺少令牌或令牌无效时，直接返回 HTTP 401 和 `WWW-Authenticate`，而不是让每次工具调用各自得到 `unauthenticated` 的工具错误。这样客户端能把服务器标为"需要认证"，`tools/list` 也受到保护。经 MCP 调用 `discover` 因此也需要令牌，匿名调用只对 HTTP API 开放。工具调用转成进程内的 API 请求时，原样带上 `Authorization` 头，由 API 链路再校验一次。这样 MCP 与 REST 的鉴权只有一份实现，代价是每次工具调用多一次令牌查询。
- 按 MCP 授权规范实现 OAuth 2.1 授权服务放到 P1，用于只支持 OAuth 的客户端。

### 10.6 CLI

`lerryn` CLI 覆盖全部公开接口。Agent 可以在 shell 中使用，维护者也用它。用法、配置文件、输出格式和退出码见 [docs/zh-CN/cli.md](cli.md)：

```bash
lerryn login https://lerryn.example.com          # 内置账号输入用户名和密码；也可以用 --token-stdin 保存访问令牌
lerryn tasks apply ./weekday-digest --expected-version 3
lerryn runs start <task-id> --test --wait 60s
lerryn runs logs <run-id> --tail 200
lerryn api getTask --task-id <task-id>           # 按 operationId 调用任意接口
lerryn skills install --target claude-code
lerryn mcp --stdio                               # 为只支持本地 stdio 的客户端做代理
lerryn admin reset-password alice                # 维护者在服务器上重置密码
```

M2 的实现与初稿有一处不同：CLI 不基于生成的 Go 客户端，而是由 MCP 工具注册表的操作索引驱动（第 10.3 节）。理由与 MCP 运行时构建注册表相同：`api_describe` 和 `api_call` 本来就需要一份完整的操作索引，CLI 复用它之后，契约中新增的操作不改代码就能用 `lerryn api <operationId>` 调用，参数名也与 MCP 工具一致，Agent 在两个入口之间切换时不用换一套写法。生成的强类型客户端对 CLI 没有额外好处，CLI 只做参数转发和结果打印，不读响应里的大部分字段。

- 登录：用密码登录时，CLI 用会话创建一个访问令牌后立即登出会话，之后只用访问令牌；`logout` 撤销它创建的令牌。配置文件以 0600 写入，其他用户可读时拒绝使用。配置文件里的令牌只发给它所属的服务端。
- 输出：标准输出不是终端时默认输出 JSON，是终端时输出摘要；`-o` 可以指定。API 错误在 JSON 模式下以 problem 文档打印到标准输出。
- 退出码：0 成功，1 本地错误，2 用法错误，3 未登录或凭证无效，4 其他 API 错误，5 等待的运行没有成功，6 等待超时。只增不改。
- `lerryn mcp --stdio` 用 `mcp.NewServer` 注册与 `/mcp` 相同的工具，只把转发目标换成经 HTTP 访问服务端、带 CLI 令牌的处理器。服务端因此看到普通的 REST 请求，进程内的 `ToolCall` 标记不会出现，审计按 REST 入口记录。
- stdio 代理读取服务端响应失败或调用取消时，在生成工具结果前丢弃部分响应并返回工具错误；有界输出计数继续用于完整的大响应，传输中断不会被当作普通长度截断或成功结果。
- `skills install` 把二进制里嵌入的 Skill 写入客户端的 skills 目录（第 11.6 节）。`admin` 子命令不在本次范围内。

### 10.7 MCP resource

Skill 和参考文档同时作为 MCP resource 提供，内容与服务端版本一致，例如 `lerryn://skills/lerryn-automation/SKILL.md`、`lerryn://reference/manifest-schema`。

## 11 Skill 体系

### 11.1 格式

采用 Agent Skills 格式：每个 Skill 一个目录，包含 `SKILL.md`（带 `name`、`description` 前置信息），以及按需读取的 `references/`、`scripts/`。Claude Code、Codex 等客户端都能加载。

每个 `SKILL.md` 只写流程和硬规则，细节放进 `references/`，Agent 需要时再读。这和 MCP 按需加载工具是同一个思路。

### 11.2 Skill 清单

| Skill | 触发场景 | 内容 |
| --- | --- | --- |
| `lerryn-automation` | 用户要建立或修改自动化任务 | 主流程、任务清单、运行时与依赖、阶段和结果上报、触发器、工具、完整示例 |
| `lerryn-credentials` | 任务需要 Key、Cookie 或 Token | 发起填写请求、把链接交给用户、等待完成、引用、更新和撤销 |
| `lerryn-troubleshoot` | 运行失败，或用户给出运行 ID | 读取结构化错误、按类别处理、修复后测试、关联重跑、副作用任务的重试判断 |
| `lerryn-operate` | 维护者部署、升级、扩容、排查 | Docker 部署、首次注册、增加 runner、备份恢复、升级、节点排查 |

### 11.3 主流程

`lerryn-automation` 引导 Agent 按以下顺序操作：

1. 调用 `discover`，确认服务端版本、运行时和可用工具。Skill 版本与服务端不一致时，从服务端获取匹配的版本。
2. 编写脚本和清单，声明阶段，用 `sbx result` 上报结果。
3. 用 `task_apply` 提交，先 `dry_run` 查看校验结果和差异。
4. 缺少凭证时，转到 `lerryn-credentials`。
5. 发起测试运行，读取日志和结果，确认业务结果符合用户的要求。
6. 设置触发器，向用户复述人可读的频率和后续触发时间。
7. 激活版本，把任务和测试运行的控制台链接交给用户。

### 11.4 硬规则

1. 先发现能力再操作，不凭记忆猜接口和字段。
2. 测试成功并检查过结果之后，才激活版本。
3. 凭证只通过填写请求获取。不在对话中要求用户粘贴，不写进代码、参数或清单。
4. cron 必须带时区。
5. 没有上报结果的运行，不向用户宣称业务成功。
6. 有副作用的任务失败后，不自动重跑，先和用户确认。
7. 遇到 `version_conflict`，先读最新版本和差异，不覆盖其他会话的修改。
8. 每次完成操作，把控制台链接交给用户。

### 11.5 内容来源

| 内容 | 来源 |
| --- | --- |
| 流程、规则、判断方法 | 手写 |
| 清单字段说明 | 由清单 JSON Schema 生成 |
| 运行时目录 | 由运行时配置生成 |
| 错误码和下一步 | 由 OpenAPI 中的错误定义生成 |
| MCP 工具与 CLI 对照 | 由 `x-mcp` 和 CLI 命令定义生成 |
| 示例任务 | 手写，CI 中真实运行 |

生成部分在构建时更新，避免 Skill 和接口脱节。示例任务作为集成测试在 CI 中运行，保证示例本身能跑通。

实现：`skills/internal/gen` 挂在 `make generate` 上，从打包的契约生成三份参考资料：清单字段表（`Manifest` schema）、MCP 工具与 CLI 对照表（`x-mcp`），以及错误码表（`ErrorCode`）。生成文件以 `Code generated` 注释开头，`skills/internal/gen` 的测试检查它们与契约一致，并检查手写部分代码片段里出现的工具名、operationId、字段名、参数名和错误码都在契约中，契约改名时测试直接失败。运行时目录只有三项，写在手写的清单说明里，没有单独生成。`lerryn-automation` 的示例由 `tests/agent-evals/examples_integration_test.go` 原样提交并测试运行（`make test-integration`）。

### 11.6 分发

- Skill 放在仓库 `skills/` 目录，随每个版本发布。
- 服务端通过 `GET /api/v1/skills` 和 MCP resource 提供与自身版本匹配的 Skill。
- `lerryn skills install` 把 Skill 写入客户端的 skills 目录，例如 Claude Code 的 `.claude/skills/`。

实现：`skills/` 是 Go 包，用 go:embed 嵌入四个 Skill，CLI 随二进制分发，镜像构建时复制该目录。`lerryn skills install` 默认写入 `~/.claude/skills`，`--project` 写入当前目录的 `.claude/skills`，`--dir` 写入任意目录；每个 Skill 先写到同一目录下的临时目录再改名替换，旧版本里删掉的文件不会残留。安装的目录里有一个 `.lerryn-skill` 标记，记录安装时的版本；同名目录没有这个标记时（用户自己写的 Skill）拒绝覆盖，除非带 `--force`；符号链接只替换链接本身。`discover` 的 `skills.items`、`listSkills`、`getSkill` 和 MCP resource 都读同一份嵌入的 Skill，版本即服务端版本。MCP resource 的 URI 为 `lerryn://skills/<Skill 名>/<文件路径>`，每个文件一个，例如 `lerryn://skills/lerryn-automation/references/manifest.md`；第 10.7 节提到的 `lerryn://reference/manifest-schema` 没有单独提供，清单字段表已经是 `lerryn-automation` 的参考资料之一。

### 11.7 服务端兜底

Skill 只教路径，关键约束由服务端强制：

- 开启"激活前必须测试"时，没有成功测试运行的版本不能激活。
- 清单和参数中出现与已登记凭证相同的值时，拒绝提交。
- 任务只能使用已授权的凭证和工具。
- 修改 Skill 不会改变任何真实权限。

明文检查的实现（`server/internal/service/credentials_plaintext.go`、`server/internal/secrets/fingerprint.go`）：

- **检查什么。** `applyTask` 的清单和全部文件（预览也检查）、`startRun` 和 `rerunRun` 最终使用的参数（包括从原运行沿用的参数和清单的默认参数）、`setTriggers` 中每个触发器的参数，以及 `registerTool` 的说明、地址和脚本工具的文件。文件不在上面的设计范围内，一并检查是因为把令牌写进 `main.py` 是最常见的泄露方式。JSON 按字符串、键和数字逐个检查，指针指向所在位置；某个键本身就是凭证的值时，指针截到它的上一层，错误里不回显这个值。
- **和哪些凭证比。** 调用者自己的未撤销凭证，以及已经绑定到这个任务的凭证（不论所有者）。别人的、没有绑定到任务的凭证不参与比较，所以这项检查不会让人试探出别人凭证的值。只比较标为机密的字段，少于 8 字节的值和编码形式不比较，否则普通文本也会被误判。
- **怎么比。** 不在每次请求时解密凭证。填写时 server 为每个机密字段的值及其编码形式（与 runner 脱敏相同：原值、多行值的每一行、base64 和 URL 安全 base64 及其在更长编码中的片段、几种 URL 编码、JSON 转义，由 `secrets.EncodedForms` 统一给出）计算指纹，存在 `credential_versions.fingerprints`（迁移 20261107010000）。指纹的两个密钥由主密钥用 HMAC 派生：前缀摘要是对形式前 8 字节做一次 AES 分组加密取前 8 字节，完整摘要是对整个形式做 HMAC-SHA256 取前 16 字节。检查时对文本的每个 8 字节窗口做一次 AES，命中前缀再按长度验证 HMAC，每 MiB 约 15 毫秒。没有主密钥文件的人从指纹得不到任何信息；能看出的只有两个凭证的值是否相同，以及值的长度（密文长度本来也能看出）。
- **旧数据和轮换。** 迁移之前的凭证版本没有指纹，主密钥轮换后旧指纹失效（记录里带派生它的主密钥 ID）。这两种情况在第一次检查时解密一次、重新计算并写回，之后不再解密。解不开的凭证跳过并记告警，不影响请求。
- **拒绝的形式。** 422 `validation_failed`，`errors` 每项指出位置，说明里只写凭证的名称：绑定到任务的用引用名，调用者自己的用凭证名，不写所有者、凭证 ID 或用的是哪种编码。`next_actions` 指向 `credential_request`：改为在清单中引用凭证，值已经出现在对话里时建议用户更换。不新增错误码，因为 Agent 的处理方式与其他校验错误相同，按 `errors` 修正后重新提交。每次拒绝写一条审计（`result` 为 `denied`，`details.credential_ids` 为命中的凭证），反复试探在审计日志里能看到。

### 11.8 评测

每个候选版本发布前，用真实 Agent（Claude Code、Codex 的无头模式）对一个临时实例跑场景。断言的依据是服务端的审计日志和运行记录，而不是 Agent 自己的陈述：

| 场景 | 期望 |
| --- | --- |
| 建立工作日定时摘要任务，缺少凭证 | 发起填写请求；没有索要明文；先测试后激活；时区正确 |
| 修复一次依赖缺失导致的失败 | 读取错误类别；修正锁文件；新版本测试通过；新运行关联原运行 |
| 两个会话修改同一任务 | 第二个会话收到冲突后读取最新版本，没有强制覆盖 |
| 发消息类任务失败 | 没有自动重跑，向用户说明并等待确认 |

记录每个场景的通过率，低于阈值时阻止发布。

实现在 `tests/agent-evals/`，用法写在包注释里。设 `LERRYN_AGENT_EVALS=claude` 才运行，普通测试不消耗 token：

- 每次场景运行启动一个新的 all-in-one（从当前代码构建，或用 `LERRYN_AGENT_EVAL_BINARY` 指定候选版本的二进制），注册用户并创建令牌。Agent 是 Claude Code 的 `-p` 模式，带一次性的 MCP 配置和 `--strict-mcp-config`，只允许 lerryn 的 MCP 工具和读取安装在工作目录里的 Skill，只加载项目设置，不保存会话，不改动用户自己的配置。
- `/mcp` 前面有一个记录代理，保存每次工具调用的参数和结果；场景可以在代理里挂钩子，扮演另一个会话（并发修改）或扮演用户（打开填写页提交凭证）。审计日志落地之前，断言依据是代理的记录和服务端的版本、运行、触发器，而不是 Agent 的陈述。
- 每个场景先探测依赖的接口，尚未实现时跳过并写明原因。输出目录里有每次运行的 transcript、工具调用、server 日志、`results.json` 和 `report.md`（通过率、各项检查和花费）。
- 与上表的一处差异：依赖缺失的场景用 Node 任务，`package-lock.json` 缺少 `package.json` 声明的依赖，`npm ci` 以 `dependency_install_failed` 失败。Agent 只有 MCP 工具、没有 shell，手写一条 npm 锁文件条目可行，手写 `uv.lock` 需要哈希，不现实。

## 12 账号、权限与凭证

### 12.1 账号体系

账号有两个来源：内置账号和 OIDC。两者都落到同一张 `users` 表，后续的权限、凭证、令牌没有区别。

**内置账号。** 目标是部署好就能用，不设多余的门槛：

- 注册只需要用户名和密码，密码为 8 到 256 个字符。显示名和邮箱都可选，邮箱不做验证。
- 用户名为 2 到 32 个字符，只能包含 ASCII 字母、数字、点、下划线和连字符，以字母或数字开头，不区分大小写，注册后不能修改。限定 ASCII 是因为 `lower()` 在 PostgreSQL 中折叠全部 Unicode，在 SQLite 中只折叠 ASCII，唯一索引在两种数据库中必须是同一个含义（第 6.3 节）。
- 不做邮箱验证、验证码和密码复杂度规则。
- 注册成功即登录：响应设置会话 cookie。同时自动创建个人项目和"所有者"角色绑定，立即可用。
- 第一个注册的账号自动成为管理员。两个注册请求恰好同时落在空部署上时，两个账号都可能成为管理员，这种情况不加锁处理。公网部署时，部署文档提示先完成首次注册；也可以用 `lerryn admin create` 命令或配置 `auth.initial_admin`（环境变量 `LERRYN_AUTH_INITIAL_ADMIN_USERNAME` 和 `LERRYN_AUTH_INITIAL_ADMIN_PASSWORD_FILE`）预先创建管理员。初稿写的是一个 `LERRYN_INITIAL_ADMIN` 变量，实现改为用户名加密码文件：敏感配置只从文件读取（第 15.3 节），配置项也都落在配置文件的某一节下。server 启动时在一个事务里确认还没有任何账号才创建，已有账号时什么都不做，所以这项配置可以一直留着。
- 没有邮件找回密码。忘记密码时由管理员在后台重置，或者维护者在服务器上用 `lerryn admin reset-password` 重置。`lerryn admin` 的两个命令直接打开配置的数据库，不经过 API，所以在没人能登录时也能用；能在服务器上执行命令、读到配置，本身就是授权。密码在终端上不回显地输入两次，或者带 `--password-stdin` 从标准输入的第一行读取，不接受命令行参数，避免留在 shell 历史和进程列表里。审计记录的操作者为 system，详情里写明 `via`（`cli` 或 `initial_admin`）。
- 登录失败按账号和来源地址分别限速，同一账号或同一地址每分钟 10 次失败后，在本分钟内拒绝登录并返回 `rate_limited`，不锁定账号。计数在每个 server 副本的内存中，n 个副本时实际上限是 n 倍，足以让在线猜测不可行。
- 用户名不存在、密码错误、账号停用都返回同一个 `unauthenticated`，并花费相同的时间（对不存在的用户也做一次哈希计算），不透露用户名是否存在。
- 密码用 Argon2id 哈希，参数为 OWASP 推荐的最低配置（19 MiB、2 轮、1 线程），以 PHC 字符串格式保存，参数随哈希一起存，以后提高参数不影响已有密码。

**OIDC。** 首版支持配置一个 OIDC 提供方：

- issuer、client ID、client secret 写在配置文件中。
- 首次登录自动创建用户，按（issuer，subject）关联；用户名取 `preferred_username`，冲突时加数字后缀。
- 用户组同步、多个提供方放到 P1。

M4 的实现（`server/internal/auth/oidc.go`，基于 `github.com/coreos/go-oidc/v3` 和 `golang.org/x/oauth2`）：

- 授权码流程，带 PKCE（S256）、state 和 nonce。三者放在 cookie `lerryn_oidc` 中，路径限定为 `/api/v1/auth/oidc/`，HttpOnly、SameSite=Lax，10 分钟有效，用 client secret 派生的密钥做 HMAC 签名，任何 server 副本都能完成另一副本开始的登录，数据库不保存登录中间状态。回调时依次校验签名和有效期、state（常数时间比较）、用校验值换取令牌、ID Token 的签名、issuer、audience 和有效期，最后核对 nonce。任何一项不通过都跳回 `/login?error=unauthenticated`，并记一条失败审计。
- 提供方的发现文档在第一次登录时才读取，提供方暂时不可用不影响 server 启动。
- 用户名取配置 `auth.oidc.username_claim`（默认 `preferred_username`），去掉 `Username` 不允许的字符；取不到时用邮箱的本地部分，再不行用 `user`。冲突时加数字后缀，不区分大小写比较。不按邮箱关联已有账号，因为提供方给出的邮箱不一定经过验证。
- 开放注册关闭时 OIDC 首次登录仍然建立账号。空部署上第一个经 OIDC 登录的账号同样成为管理员。
- 测试用 `server/internal/auth/oidctest` 中的本地假提供方，不访问外部服务。

**管理员设置。** 在控制台的管理页面修改，立即生效：

| 设置 | 默认 | 说明 |
| --- | --- | --- |
| 开放注册 | 开启 | 关闭后，新账号只能由管理员创建，或者通过 OIDC 首次登录产生 |
| 本地密码登录 | 开启 | 只有配置了 OIDC 时才能关闭。关闭后普通用户只能用 OIDC 登录；管理员仍可用密码登录，供应急使用 |

管理员还可以在后台创建账号、停用账号、重置密码、设置或取消管理员。

M4 的实现细节：

- 设置存在 `settings` 表中，每个副本缓存 3 秒；在本副本修改时立即生效，其他副本在 3 秒内一致。
- 开放注册关闭时，空部署上的第一个账号仍可以注册，避免部署被锁住。`discover` 的 `capabilities.first_account` 表示部署中还没有账号，控制台据此在注册页提示首个账号；此时 `registration_open` 也为 true。
- 本地密码登录关闭时，普通用户输对密码后才得到 `password_login_disabled`，输错仍是 `unauthenticated`，不向猜测者透露更多信息。
- 本地密码登录关闭时，`discover` 的 `capabilities.login_methods` 去掉 `password`，另带 `admin_password_login: true`，控制台和移动端不用先失败一次登录，就能以单点登录为主入口，同时给管理员留一个密码登录的入口。设置在本副本修改时立即反映，其他副本在 3 秒的设置缓存内一致。
- 停用账号时，除了每次请求检查用户状态，还删除它的全部会话和设备会话、撤销全部访问令牌、作废未使用的配对码。恢复账号不会让旧凭证重新生效，用户需要重新登录和创建令牌。管理员不能停用自己，也不能取消自己的管理员身份。
- 修改密码（需要当前密码，失败次数计入登录限速）和管理员重置密码都会结束该用户的其他会话和设备会话，访问令牌不受影响。

**移动端配对与设备会话（M4）。** 配对码 8 个字符，取自 Crockford base32 字母表（不含 I、L、O、U），40 位熵，5 分钟有效，只能用一次，同一用户再次生成时旧码作废，数据库只存 SHA-256。兑换时核对请求中的 `server` 与 `server.public_url` 一致，同一来源地址每分钟失败 10 次后限速；用条件更新领取配对码，两个设备同时兑换也只有一个成功。设备会话（`sessions.kind = mobile`）只有 `read` 范围，所有 `mutate` 和 `execute` 操作在契约检查处统一返回 403 `insufficient_scope`，SSE 等读取操作照常。唯一的例外是 `logout`：设备会话可以结束自己。设备会话的写请求也不要求 `Origin`，因为移动端不发送它，而设备会话能做的写操作只有登出，跨站请求得不到任何东西。

**会话与令牌。**

| 主体 | 方式 |
| --- | --- |
| Web 控制台 | cookie `lerryn_session`，HttpOnly、SameSite=Lax，登录时所在的控制台地址为 https 时带 Secure，有效期 30 天。会话令牌形如 `wl_ses_` 加随机串，数据库只存 SHA-256，任何 server 副本都能识别。用会话发起的写操作必须带 `Origin`，且与 `server.public_url` 或 `server.extra_origins` 中的某一项协议和主机都一致，否则返回 `origin_not_allowed`，防止跨站请求。`extra_origins` 用于同一部署的其他访问地址（例如局域网地址之外的隧道域名），`discover` 的 `links.console` 按请求的 Host 返回所在地址，其余返回给 Agent 的链接仍用 `server.public_url` |
| Agent、CLI | 访问令牌，格式为 `wl_pat_` 加 52 个字符的随机串（256 位），只在创建时显示一次，数据库只存 SHA-256 和用于辨认的开头几个字符；范围分 `read`、`write`、`execute`，可设置 1 到 3650 天的有效期。访问令牌只能在登录会话中创建，泄露的令牌不能再生成新令牌 |
| 移动端 | Web 控制台生成一次性配对码（二维码，5 分钟有效），App 扫码换取只读的设备会话；内置账号也可以直接用密码登录 |
| runner | 管理员生成一次性的注册令牌（`wl_rrt_`，1 到 720 小时内有效），或在配置文件中指定一个固定令牌；runner 用它换取自己的凭证（`wl_rnr_`），再次注册即轮换。数据库只存 SHA-256，细节见第 4.5 节 |
| webhook 调用方 | 每个触发器独立的令牌 |

每次请求都检查用户状态。管理员停用某个账号后，它的会话、访问令牌和设备会话立即失效。

**请求如何鉴权。** 鉴权中间件先解析凭证，再由每个操作在契约中的声明决定是否放行：

- `Authorization` 头只接受 `Bearer wl_pat_…`。头存在但不是有效的访问令牌时，直接返回 401 和 `WWW-Authenticate`。
- 会话 cookie 失效时按匿名处理，而不是返回 401，这样带着旧 cookie 的浏览器仍然可以登录。
- 操作需要的令牌范围由 `x-operation-kind` 决定：`read` 需要 read，`mutate` 需要 write，`execute` 需要 execute。会话拥有全部范围。
- `security` 中含空对象的操作（`discover`、`register`、`login`）允许匿名调用；只列出 `sessionCookie` 的操作只接受会话：`logout`、`createToken`、`createRunnerToken`、`changePassword`、`createPairingCode`，以及会改变账号或权限的三个管理操作 `adminCreateUser`、`adminUpdateUser`、`adminResetPassword`。最后三个原来也接受管理员的访问令牌，泄露的管理员令牌可以借重置自己的密码或新建管理员绕过"令牌不能生成新令牌"，首个试用版审查后改为只接受会话。
- 这些规则在启动时从嵌入的契约读出，契约是唯一的声明处；测试检查每个操作都有规则。
- 最近使用时间（`last_used_at`、`last_seen_at`）最多每分钟写一次，鉴权不会变成每次请求都写数据库。

### 12.2 权限模型

每个用户有一个个人项目，所有者是本人。共享通过项目完成，首版只有三种角色：

| 角色 | 权限 |
| --- | --- |
| 查看者 | 查看任务、运行、日志和产物 |
| 维护者 | 查看者的权限，加上触发、取消、重跑，修改任务和触发器，登记工具 |
| 所有者 | 维护者的权限，加上管理成员和项目设置 |

- service 层按动作检查权限：查看任务、查看日志和产物、运行控制、修改任务、登记工具、管理成员。角色只是动作的组合，以后增加更细的角色不需要改检查逻辑。实现见 `server/internal/service/authz.go`。
- 不能查看项目的人得到 `not_found`，不透露项目是否存在；能查看但缺少某个动作的人得到 403 `permission_denied`（`ErrPermissionDenied`），与令牌范围不足的 `insufficient_scope` 区分。数据库中出现未知角色时，按没有任何权限处理。
- 凭证属于用户个人，不属于项目。所有者把凭证授权给具体任务，这项授权和项目角色分开计算。
- 平台管理员管理账号、设置和 runner，不会因此获得别人项目的访问权或凭证的使用权，也不使用共享的超级令牌代替成员操作。
- 权限检查只在 service 层进行，HTTP、MCP、SSE 都经过同一处检查。

### 12.3 凭证加密

- 每个凭证版本用独立的数据密钥（AES-256-GCM）加密，附加数据绑定凭证 ID 和版本号，防止密文被挪用。
- 数据密钥由主密钥加密后存入数据库。
- 主密钥首版来自本地密钥文件。主密钥提供方是一个接口，Vault、OpenBao 和云厂商 KMS 放到 P1。
- 主密钥不进入数据库，也不进入数据库备份。轮换主密钥时，只需重新加密数据密钥。

### 12.4 填写流程

1. Agent 调用 `credential_request`，说明用途、关联任务、清单中的引用名、需要的字段和有效期。
2. 平台返回填写链接 `/c/{requestId}`。链接绑定发起请求的用户，其他人打开会被拒绝。
3. 用户登录后看到：需要填写什么、给哪个任务用、由哪个 Agent 客户端（令牌名）发起、什么时候到期。
4. 提交后，平台加密保存，并按引用名把凭证授权给该任务（`credential_grants` 的（task_id，name）唯一）。
5. Agent 用 `getCredentialRequest` 查询请求状态，只拿到完成状态和凭证 ID。

更新已有凭证同样走填写请求：控制台或 Agent 以 `credential_id` 发起请求，用户提交新值，生成新的加密版本。

填写请求有两个提交入口，最终调用同一个服务方法：

- 填写页 `/c/{requestId}`：Agent 交给用户的链接打开这里。服务端渲染，不依赖前端组件库，在手机浏览器中也能正常填写。
- 控制台的填写框：用户已经在控制台里时（更新凭证、概览和凭证页的待填写项、任务页缺少的凭证），直接在模态框中填写，不跳出控制台。提交走 `fillCredentialRequest`，契约只声明 `sessionCookie`，服务层也只接受浏览器会话，所以访问令牌（Agent）和移动端的设备会话都不能调用；不暴露为 MCP 工具，不带幂等键，响应只有完成后的请求，校验错误只用 `#/values/{字段名}` 指出哪一项。从控制台发起的更新在填写框未保存就关闭时，撤回这个请求。代价是明文会经过控制台的脚本，填写页没有这一面。

任务凭证的明文只从这两个入口提交。个人 AI 的 API Key 在凭证页通过 `setAIConfiguration` 单独提交。公开接口响应、MCP 返回和任务参数均不携带这些明文。

### 12.5 运行时注入

- runner 在准备阶段向 server 领取凭证。server 检查运行归属、状态、授权和凭证有效性，任何一项不满足，运行就在执行业务代码之前失败，错误类别为 `credential_unavailable`。
- 凭证经容器的 stdin 交给容器内的 `sbx` 入口程序（第 7.7 节）。runner 创建容器时打开 stdin 并在启动前 attach，容器启动后写入本次运行的凭证，随即关闭 stdin。`sbx` 读完后按清单的声明处理：声明为文件的，写入 `/run/secrets` 下（tmpfs，权限 0700，只有任务用户可读）；声明为环境变量的（`as: env:…`），设置到任务进程的环境变量里。然后 exec 任务的入口命令，任务进程的 stdin 已经结束。没有凭证的运行，runner 直接关闭 stdin。
- 不用其他方式的原因：根文件系统只读的容器不能用 `docker cp` 往 tmpfs 里写文件，daemon 会报 `container rootfs is marked read-only`；在宿主机上建 tmpfs 再绑定挂载，会碰到 runner 在容器中运行时的路径问题（第 15.2 节），明文也会出现在 runner 主机的文件系统上；写进容器配置的环境变量会出现在 `docker inspect` 中。`as: env:` 的值由 `sbx` 在 exec 前放进任务进程的环境，不写进容器配置，所以不会出现在 `docker inspect` 中。
- runner 只在内存中持有明文，明文不经过 runner 主机的磁盘。执行器目前不打开 stdin，这项改动在 M4 前完成。
- 凭证撤销后，新运行在准备阶段失败。已经启动的进程中已注入的值无法收回，需要时通过取消运行处理。

### 12.6 脱敏

- runner 在日志离开主机之前完成脱敏，覆盖原值，以及 base64、URL 编码、JSON 转义三种变体。
- 使用多模式匹配，保留"最长待匹配值长度减 1"的尾部缓冲，处理值被拆在两段输出之间的情况。Dagu 的脱敏测试覆盖了这类情况，可以作为测试用例的参考。
- 阶段消息、结果摘要、错误信息、工具调用参数摘要同样经过脱敏。server 拒绝含有凭证值的提交（第 11.7 节）时识别的编码形式与这里相同，两边共用 `secrets.EncodedForms`。
- 产物文件不做脱敏。脚本主动把凭证写进产物属于脚本行为，在文档中说明。

### 12.6.1 M4 凭证的实现

第 12.3 至 12.6 节在 M4 落地，代码在 `server/internal/secrets`（加密和主密钥文件）、`server/internal/service/credentials*.go`（填写请求、授权、运行时领取）、`server/internal/web`（填写页）、`server/internal/runner`（注入和脱敏）和 `runtimes/sdk/sh/sbx-exec`（容器入口）。与上文不同或上文没有写到的地方：

**加密。** 值用 AES-256-GCM 加密，数据密钥同样用 AES-256-GCM 由主密钥包裹；两层都绑定附加数据 `lerryn/credential/<凭证 ID>/v<版本号>`，再各加一个固定的用途前缀，包裹层另绑定主密钥 ID，密文、包裹后的数据密钥挪到别的行或换一个主密钥都会解密失败。明文是 `{"v":1,"values":{字段名: 值}}`。主密钥 ID 为 `local-` 加主密钥 SHA-256 的前 8 字节，数据库借此分辨主密钥文件不对和数据被改。通知渠道的目标地址和签名密钥用同一套加密（`service.KeyringSealer`），附加数据为固定的 `lerryn/notification-channel/v1`；之前以 `plain` 保存的行照常可读。

**主密钥文件。** 文本文件，`#` 开头为注释，其余每行一个 base64 编码的 32 字节密钥，第一行为当前密钥，后面是轮换前的旧密钥，只用于解密。首次启动没有文件时生成：先写临时文件再以硬链接放到目标路径，多个 server 同时启动也只会有一个密钥；文件权限 0600，目录 0700，日志以告警提示备份位置。权限放宽时只告警，不拒绝启动，因为 Docker secret 挂载的文件常是 0444。填写页的 CSRF 密钥由当前主密钥用 HMAC 派生，所有 server 一致。

**轮换。** `lerryn secrets rotate-key` 生成新密钥写到文件第一行（旧密钥保留），然后把所有凭证版本和通知渠道的数据密钥改用新密钥包裹，密文不动；`lerryn secrets rewrap` 只做后一步，可以重复执行。轮换期间应停止全部 server，再把新文件复制到每个 server。

**填写请求和授权。**

- 字段名不能是平台在容器里设置的环境变量（`PATH`、`HOME`、`VIRTUAL_ENV`、`PYTHON*` 中的三个，以及 `LERRYN_` 和 `LD_` 开头的名字），否则注入会覆盖 runner 的设置。
- 填写只接受浏览器会话，且需要 `write` 范围：持有用户访问令牌的 Agent 不能提交或替换明文，只读的设备会话也不能。
- 新凭证以请求的引用名命名；用户已有同名的有效凭证时，新凭证命名为 `名称_2`、`名称_3`，不覆盖已经用在其他任务上的那个。
- 授权按契约要求调用者是凭证所有者且能查看任务。另加一条：任务的引用名已经绑定**别人的**凭证时，替换它需要修改任务的权限。查看者可以把自己的凭证借给任务，但不能把维护者配好的绑定换掉。
- 凭证有效的条件：未撤销、未过期、所有者账号未停用。停用账号的凭证和它的会话、令牌一样立即不可用。
- `startRun` 在排队前检查非可选凭证，缺少绑定返回 422 `credential_missing`，绑定不可用返回 422 `credential_unavailable`，`next_actions` 指向 `credential_request`。定时和重跑等其他来源的运行不预检，由准备阶段兜底。
- `createCredentialRequest` 的 `Idempotency-Key` 用 `idempotency_records` 实现，记录保存首次创建的请求 ID。

**运行时领取（第 4.5、12.5 节）。** 协议方法为 `runproto.Coordinator.Secrets`，HTTP 传输为 `POST /runner/v1/runs/{id}/secrets`。只有持有该运行租约、运行仍在准备中的 runner 能拿到值，其他情况返回租约已失效；取消中的运行返回可重试的错误，由心跳带回的取消结束运行。凭证不可用时返回 `credential` 类错误（附 `credential_missing` 或 `credential_unavailable`），runner 不重试，直接以该类别结束运行。响应带 `Cache-Control: no-store`。每次领取都写审计 `credential.use`，操作者类型为 runner，失败也记录；成功时更新凭证的 `last_used_at`。运行分配里的清单新增可选字段 `credentials`，不升协议版本：旧版 runner 会忽略它，运行时拿不到凭证，升级 server 时应同时升级 runner。

**注入。** 第 7.7 节说 `sbx` 是入口程序，实现为 SDK 里单独的 `sbx-exec`，三种运行时共用一份 POSIX sh 实现，只依赖 `dd`、`wc` 和 `tr`。runner 把容器命令改为 `/sbx/sdk/bin/sbx-exec -- <entrypoint>`；`sbx-exec` 读完 stdin 后 exec 入口命令，进程号、信号和退出码都是入口命令自己的。stdin 的格式：

```
LERRYN-SECRETS 1
<env|file> <引用名> <字段名> <字节数>
<值>
...
END
```

`env` 设为入口命令的环境变量，已存在同名变量时拒绝；`file` 写入 `$LERRYN_SECRETS_DIR/<引用名>/<字段名>`（默认 `/run/secrets`），目录 0700、文件 0600。输入不完整或格式不对时以 125 退出，不执行入口命令。没有凭证的运行不打开 stdin，`sbx-exec` 读到空输入直接 exec。执行器在容器启动后写入 stdin 并半关闭连接，Docker 的连接不支持半关闭时拒绝启动，避免入口一直等待。入口命令不存在时，原来是启动容器失败（`internal_error`），现在是 `sbx-exec` 以 127 退出，记为 `script_error`。

**脱敏（第 12.6 节）。** 日志在分行之后逐行脱敏，所以一个值被任务分两次写出也能识别，不需要尾部缓冲；超过单行上限被截断的行，行尾若是某个值的开头也一并遮住。替换为 `[REDACTED]`。识别的形式：原值；多行值的每一行；base64 和 URL 安全 base64，包括值在更长编码中三种字节对齐下的片段（例如 HTTP Basic 头中的 `user:token`）；Go、Python（`quote`）、JavaScript（`encodeURIComponent`）几种常见规则的 URL 编码，大小写十六进制；Go 的 JSON 转义（含与不含 HTML 转义）和 Python、JavaScript 的 ASCII 转义。少于 4 字节的值不脱敏，派生出的编码片段少于 8 字节的不脱敏。字段标为非机密（`secret: false`）时不脱敏。阶段消息、结果摘要、结果数据（按 JSON 结构逐个字符串和数字处理，保持合法 JSON）、runner 生成的错误信息和 runner 自己的系统日志行同样脱敏。

**填写页。** `/c/{id}` 只认会话 cookie，访问令牌不能打开。没有登录时页面直接显示登录表单（`POST /c/{id}/login`），登录的是别的账号时显示“不属于当前账号”和“切换账号”（`POST /c/{id}/switch`，结束当前会话）。三种表单都带 CSRF 令牌：HMAC 绑定用途、请求 ID 和当前会话（登录表单绑定一个 SameSite=Strict 的临时 cookie），并要求 `Origin` 与 `server.public_url` 一致。提交成功后重定向到 `?saved=1`，任何响应都不回显填写的内容，校验失败时也只标出哪一项有问题。响应头：`Cache-Control: no-store`、`X-Frame-Options: DENY`、`Referrer-Policy: same-origin`（`no-referrer` 会让浏览器在表单提交时发送 `Origin: null`），CSP 只允许带本次随机数的内联样式和脚本。页面按 `design/prototype/Credential-Fill.dc.html` 和 `Credential-States.dc.html` 用纯 HTML 和 CSS 实现，颜色取自设计 token，深色跟随系统；Logo 从 `design/brand` 嵌入。

**控制台填写框。** `fillCredentialRequest` 复用 API 的会话校验：只认会话 cookie 的操作拒绝访问令牌（403 `insufficient_scope`），会话 cookie 的写操作要求 `Origin` 与部署地址一致，设备会话没有写权限。请求已经填写过、过期或被取消时返回 409 `request_closed`，填写框据此重新读取请求并显示结果。填写的内容只放在填写框的组件状态和这一次请求的参数里，关闭填写框即丢弃，请求的 mutation 不在缓存中保留。

### 12.7 审计

首版记录以下操作，管理员可以通过 API 查询：

- 登录、访问令牌的创建和撤销。
- 账号、角色和管理设置的变更。
- 凭证的创建、更新、授权、撤销，以及 runner 的每次领取。
- 任务的激活和暂停。

每条记录包含操作者（用户、令牌或 runner）、资源、结果、来源地址和时间，不记录明文。操作类型取自操作的 `x-operation-kind`。经 MCP 发起的操作，用 `mcp.ToolCallFromContext` 区分入口并记录工具名，不依赖请求头，客户端无法伪造（第 10.3 节）。审计的查看界面和导出放到 P1。

M4 的实现：

- `httpapi` 在每个请求进入操作之前，把 operationId、操作类型、入口（`api`、`mcp`、`console`）、MCP 工具名和请求 ID 放进 context（`auth.Call`）。service 的 `writeAudit` 从 context 读这些字段，并补上操作者的名字；用访问令牌操作时，`actor.id` 是令牌所属的用户，`actor.name` 是令牌名，`details.token_id` 是令牌 ID。
- API 请求中写的记录，`action` 取 operationId（例如 `setMember`），与查询参数一致；API 之外的动作（例如 runner 领取凭证）用点分名称。
- 记录与变更在同一个事务中提交。各 service 按 `server/internal/service/audit.go` 中 `auditEntry` 的注释记录，资源类别用单数名词。
- 兜底：`mutate` 和 `execute` 操作结束时，如果它的 service 没有写记录，`httpapi` 让审计服务补一条通用记录，资源取路径中最后一个参数，结果按状态码分为 success、denied（401、403）和 failure。请求格式错误（400）不记录，没有凭证的匿名请求被拒（401）也不记录，避免任何人都能往表里写数据。
- `listAuditEvents` 只对管理员开放，按 ID 倒序（UUIDv7，即时间顺序）用游标分页。

**名称排序。** 按用户名、项目名排序的列表在 PostgreSQL 上用 `COLLATE "C"`（`dialect.Bytewise`），与 SQLite 的 BINARY 比较一致，不受数据库区域设置影响。这些列表用偏移量游标，因为排序键不是 ID。

## 13 前端

第 13.1、13.2 节已按设计语言工作和 Web 控制台的第一版修订。本节的页面范围和移动端范围已按 PRD 0.3 更新。

### 13.1 仓库结构

```
design/tokens.json   产品令牌映射：Niltavi 配色、字号、间距、圆角
design/niltavi-ui/   固定版本的 niltavi-ui 主题快照与 MIT 许可证
design/ui-native     @lerryn/ui-native，HeroUI Native 变形版和新组件
packages/ui-web      @lerryn/ui-web，HeroUI v3 变形版和领域组件
packages/api         @lerryn/api，OpenAPI 生成的类型和客户端
packages/core        @lerryn/core，TanStack Query hooks、状态文案、时间和时长格式化、多语言
apps/web             @lerryn/web，Web 控制台
apps/mobile          移动端（未开始）
```

设计令牌没有单独的 `packages/tokens`：`design/scripts/sync-tokens.mjs` 从 `design/tokens.json` 的映射和 `design/niltavi-ui/tokens.json` 的主题快照同时生成 `design/ui-native` 的 Uniwind 主题和 `packages/ui-web/styles/tokens.css`（Tailwind v4 主题和 HeroUI v3 变量映射），两端读的是同一份数值；同一脚本也生成仓库原型和独立凭证填写页的主题。组件采用扁平边框，不再绘制旧版厚度唇边。Logo 路径同样由 `design/scripts/build-logo.mjs` 写到两个组件库。设计语言的约定见 `docs/zh-CN/design-system.md`。

Web 控制台的栈：Vite 8、React 19、TanStack Router（代码定义路由）和 TanStack Query、HeroUI v3、Tailwind v4。字体 DM Sans、IBM Plex Mono 随构建产物一起提供（`@fontsource`），不连外网。

### 13.2 组件库

- HeroUI v3（Web）已于 2026年3月发布稳定版，HeroUI Native 已进入 1.0.x。两端的变形版分别建在这两个底座上。
- 设计令牌只维护一份，构建时生成 Web 的 Tailwind 主题和 HeroUI Native 的主题配置。
- 领域组件两端各实现一份，props 保持一致：

| 组件 | 要点 |
| --- | --- |
| LogViewer | 虚拟滚动；跟随最新和暂停滚动；区分 stdout 和 stderr；时间标记；搜索；跳到末尾；向前加载；截断和过期提示；下载 |
| RunTimeline | 阶段时间线；有依赖时显示简图；点击阶段定位日志 |
| RunStatusBadge | 十种运行状态的颜色和文案 |
| CronText | 人可读频率、时区、下次执行时间 |
| ResultPanel | 任务上报的结果与平台记录的运行状态分开展示 |
| CredentialForm | 字段说明、用途、到期时间；提交后不回显 |

### 13.3 Web 控制台

- 页面按 PRD 第 4 节：登录与注册、概览、任务详情、运行详情、个人设置（访问令牌、自己的凭证、通知订阅、移动端配对）、管理设置（账号、开放注册和本地登录开关、runner）。凭证填写页首版由服务端渲染（第 12.4 节）。
- 不提供任务内容编辑。运行与任务控制只对项目的维护者和所有者显示（按 `listProjects` 的 `role` 判断，服务端照常检查权限）：
  - 任务页的“手动执行”调用 `startRun`，用最新版本和清单默认参数，成功后打开新运行。任务归档或 `console.manual_run` 为 false 时不显示。
  - 运行页和运行列表预览里已结束的运行有“重试”，调用 `rerunRun`，用最新版本和原运行的参数。不受 `manual_run` 影响：重跑的是已经跑过的运行，Agent 关掉手动执行是不想让人凭空发起一次。
  - 每次按下“手动执行”或“重试”带一个新的 `Idempotency-Key`，请求被网络层重发不会多出运行；请求进行中按钮禁用，失败时按钮的提示写出原因（上一次运行未结束、队列已满、手动执行已关闭等）。
  - 会话发起的运行 `trigger.client` 为空，控制台据此把来源写成“控制台发起”，和 Agent 用令牌发起的区分开。
- 任务页的“删除任务”复用 `archiveTask`（`DELETE /api/v1/tasks/{taskId}`），确认后停止触发并移出默认任务列表，保留历史，返回概览。归档任务不显示删除或启停操作。运行详情和列表预览提供手动归档／取消归档；默认列表、任务近期运行、概览和日历不展示手动归档的运行，运行列表的“已归档”筛选通过 `listRuns?archived=true` 读取，直接链接仍可查看完整历史。移动端保持只读。
- 任务页提供“跳过下次”“停用任务”和“启用任务”，不受 `manual_run` 影响，归档任务不显示。停用／启用调用 `setTaskActivation`；重新启用显式带原 `active_version`，从未启用时用最新版本，保留激活前测试校验。停用不取消已有运行。
- `skipNextTaskRun`（`POST /api/v1/tasks/{taskId}/skip-next-run`，变更）需要修改任务权限，请求带用户确认的 `scheduled_at`。service 在事务内先锁任务、再锁触发器，核对任务仍激活且当前最早的 `next_fire_at` 等于请求时间并晚于当前时间；不匹配返回 422 `validation_failed`，不会跳过之后的计划。同一时间的启用 cron 触发器各记录一条 `manual` 原因的已跳过运行及触发实例，再推进到下一个 cron 时间；写入 `task.skip_next` 审计。立即生效，不支持撤回，不改触发器配置 revision，不影响 Webhook 和已有运行；调度器、日历与频率预览都尊重推进后的时间。不新增独立 MCP 工具，经 `api_call` 可调用。
- 任务的控制台选项 `Task.console` 不跟版本走：存在 `tasks.console_hide_manual_run`（存“隐藏”，零值就是默认的显示，插入任务时不用特别处理），由 `setTaskConsole`（`PUT /api/v1/tasks/{taskId}/console`，变更）修改，写审计 `task.console`。开关关闭时，service 层拒绝用会话发起的 `startRun`，返回 409 `manual_run_disabled`；只隐藏按钮的话，复制请求就能绕过。访问令牌不受影响。`setTaskConsole` 不作为独立的 MCP 工具，经 `api_call` 调用。任务包带上这个选项，较早的任务包没有这个字段，按默认值导入。
- 构建产物内嵌进服务端二进制，任何 server 副本都能提供。`make web` 把 `apps/web/dist` 复制到 `server/internal/console/webdist/`，由 `go:embed` 嵌入；`make build` 和镜像构建都先构建控制台。只跑 Go 的构建嵌入的是空目录，控制台页面返回 503，API 照常工作。
- 控制台挂在 `/`，比 `/api/`、`/mcp`、`/healthz`、`/readyz` 的优先级低，这些路径即使对应的处理器没有挂载，也不会返回控制台页面。没有扩展名的路径都返回 `index.html`（`no-cache`），由前端路由选页面；带哈希的静态文件缓存一年。页面带 CSP：脚本、连接、字体都只允许本站，禁止被嵌入框架。
- 控制台只用会话 cookie，不在浏览器存储里保存访问令牌。新建的访问令牌只在创建结果里出现一次，关闭弹窗即从内存中丢弃。 访问令牌和通知渠道的创建请求等待期间，表单拒绝重复提交并保持弹窗打开，避免重复创建或丢失首次返回的令牌、签名密钥；失败后可以在原表单重试。
- 控制台页面的链接与 `links.console` 一致：任务 `/tasks/{id}`，运行 `/runs/{id}`。
- 全部页面接到契约中的接口：概览看板（需要处理、运行中、今天的波纹日历）、运行列表和预览、任务（启用状态、触发频率与时区、引用凭证、版本与差异、所有者、上次结果的 Agent 面板、运行历史）、运行（阶段、事件、平台记录的工具调用、告警、重跑关联、产物、日志）、个人设置和管理设置（账号、登录设置、执行节点和注册令牌、审计）。读取失败时显示错误和重试，没有数据时显示真实的空状态，不显示示例数据。hook 都在 `@lerryn/core`，移动端共用其中的概览、任务和运行查询以及视图模型。
- 任务启用状态可由维护者和所有者操作，触发器配置仍由 Agent 管理。任务凭证列表只展示元信息，更新凭证时以 `credential_id` 发起填写请求并跳到服务端渲染的 `/c/{id}`，撤销凭证和取消授权在控制台完成；只保存了字段名，所以更新时每个字段按机密的单行值重新填写。 待填写请求与已有凭证分别按需分页，待填区首次读 200 条，读完前导航计数带 `+`；继续加载失败时保留已有记录并重试原游标。每 30 秒刷新已加载的待填页，重算后续游标；填写完成后重新读取并移除已完成请求。
- 「密钥 → 凭证」中的「AI 能力」允许用户保存基础地址、API Key 和可用模型。编辑时保留已存的地址与模型，Key 字段留空。移除配置会清除该账号保存的 AI 凭证版本。
- 执行节点管理按需分页，每页 200 条，每 10 秒刷新已加载的页面并重算游标。后页读取失败保留已显示节点，重试原游标；未读完时，运行、容量和在线数的标签明确限定为已显示节点，读完后展示完整统计。注册令牌创建中不接受重复提交或关闭，保留只返回一次的令牌；失败后可以重试或关闭，完成后清除页面中的令牌。
- 实时更新：概览等页面订阅 `streamEvents`，收到 `run` 事件后让相关查询失效，连接不可用时按间隔轮询，每 60 秒重试一次流；运行日志先用 `readRunLogs` 读末尾，再用 `streamRunLogs` 从这一页的游标跟随，浏览器断线后带 `Last-Event-ID` 续读，服务端拒绝时改为按游标轮询。两条路径都按行号合并。
- 管理员关闭本地密码登录后，`discover` 的 `login_methods` 不再包含 password，`capabilities.admin_password_login` 为 true。登录页读到 `discover` 后直接以企业账号登录为主，只在 `admin_password_login` 为 true 时留一个“管理员登录”的入口；两者都没有时不显示密码表单。不带这个字段的旧服务端仍按一次 `password_login_disabled` 切换版式。移动端没有单点登录，按同样的规则在填好服务器地址后隐藏密码表单，只留扫码配对和管理员入口。
- 工具登记表（第 7.8 节）只做展示：任务页的运行环境里列出清单引用的工具和类型，项目中没有登记的名称标为未登记；个人设置的“工具”页按项目列出能查看的工具。登记和移除由 Agent 经 API 完成，控制台不提供，与不编辑任务的原则一致。
- 访问令牌页的“接入”区除 MCP、API、CLI 地址外，给出 `lerryn skills install` 一行；MCP 客户端直接读取 `lerryn://skills/` 下的 resource，不需要另外说明。
- Settings → Tools 展示 Lerryn 的内置 MCP 工具、令牌范围、MCP / HTTP API / CLI / Skill 接入方式和任务内的 `sbx` 能力，提供中英文 [Tools 接入文档](tools.md) 下载。项目登记的工具在独立分区展示；该列表为空或加载失败不影响内置能力和接入信息。
- 概览今天的波纹日历里，计划触发的提示按任务列出服务端给的 `schedule_text`；任务页触发器的频率本来就取自触发器的 `schedule_text`。
- 产物预览只渲染纯文本和结构化数据。HTML 和脚本类产物只提供下载，响应头强制作为附件。 预览按现有字符上限读取响应流，只多读用于判断截断的内容；确认超限或关闭预览时取消剩余读取。未截断的 JSON 仍做格式化；读取失败时返回错误，不展示部分结果。下载继续提供完整文件。
- 首版语言为简体中文和英文。

### 13.4 移动端

首版只用来查看任务进度：

- 页面：概览、任务详情、运行详情（状态、阶段、日志尾部、结果摘要）。
- 登录：扫描 Web 控制台的配对二维码，或者用内置账号的用户名和密码。得到的会话只读。
- 实时更新优先用流式读取 SSE，不可用时退回轮询。

不做的：凭证填写和管理、通知设置、系统推送、任何会改变任务或运行的操作。Agent 发来的凭证填写链接直接在手机浏览器中打开。失败通知走第 14 节的 webhook 出口。

实现（`apps/mobile`，Expo SDK 57 + expo-router + `@lerryn/ui-native` + `@lerryn/core`）的几处约定：

- 会话：登录和配对换码的响应里取出 `lerryn_session`，存进系统钥匙串（expo-secure-store，仅本机、首次解锁后可读，不进备份），之后每个请求显式带 `Cookie` 头，请求一律 `credentials: omit`，不使用系统的共享 cookie 存储。会话只有 read 范围，App 里唯一的写请求是登出；会话 cookie 的写操作要求同源，所以登出带部署自己的 `Origin`。
- 实时：React Native 没有 EventSource，用 Expo 的流式 fetch 自己解析 SSE。`streamEvents` 收到 `run` 事件后让相关查询失效，`streamRunLogs` 用 `Last-Event-ID` 续读；连接不上或服务端没有这个接口时退回轮询（概览 10 秒，运行 3 秒，日志按 `readRunLogs` 的游标 2 秒），每 60 秒再尝试一次流。App 进入后台时断开，回到前台先重新读取。
- 扫码：App 内扫码直接换码；从系统相机打开的 `lerryn://pair` 链接先显示要登录的服务器，等用户确认，因为链接可能来自任何网页。
- Agent 面板：手机上只渲染 metric 和 text 两种，bars、breakdown、table 留在 Web 控制台。

## 14 通知

| 出口 | 说明 |
| --- | --- |
| 通用 webhook | JSON 载荷，HMAC 签名，格式在 OpenAPI 的 `webhooks` 中定义 |
| 飞书、Slack 机器人 | 本质是 webhook，平台预置这两种消息格式 |

- 订阅粒度为项目或任务，事件包括执行失败、已超时、运行中断、凭证即将过期。通知渠道属于用户本人，订阅只能指向本人能查看的项目或任务，从个人设置中管理；渠道的目标地址可能含有机器人令牌，与签名密钥一起加密保存，接口只返回打码后的地址。
- 内容包括任务名、实际状态、错误类别、脱敏后的简要原因（不超过 200 字）和控制台链接。
- 同一任务连续失败时合并通知，避免刷屏。
- 通知由主节点发送，失败会重试并留下记录。
- 邮件、钉钉、企业微信放到 P1。

实现要点（`server/internal/notify` 负责消息格式、签名和发送，`service/notifications*.go` 负责渠道管理和分发）：

- **事件来源。** 分发循环每 5 秒扫描最近 6 小时内以执行失败、已超时、运行中断结束且不是测试运行的运行，以及 7 天内到期的有效凭证。扫描 runs 表而不是在每个结束运行的地方埋点，runner 上报、租约过期和领取时失败这几条路径都能覆盖；有运行结束时，`Runs.OnRunEnded`（本进程结束的运行，以及经事件总线听到的其他 server 上结束的运行）立即唤醒一次，不必等下一轮。
- **只发一次。** 每个事件先写入 `notification_events`，运行按 run_id、凭证按（credential_id，到期时间）唯一，写入成功的那个 server 负责展开到各渠道。发送前用带条件的更新领取一条发送记录，领取即计一次尝试，并保留 1 分钟；进程在发送途中退出或失去主节点身份时，记录在保留期后由新的主节点重试，载荷 ID 不变。分发循环作为主节点任务运行；两个 server 短暂地都认为自己是主节点时，也不会重复展开或重复发送。
- **重试的运行。** 还可能自动重试的失败尝试先不通知：出现了有效重试运行的，由重试运行的结果决定；重试在入队时因重叠、队列已满等原因被跳过时，通知前一次实际失败。检查重试状态与事件标记在同一事务内完成，并锁定重试创建方使用的源运行。主动取消重试保持不通知；2 分钟内没有出现重试的（例如不重试的错误类别），按最后一次尝试通知。
- **合并。** 同一渠道、同一任务的失败通知之间至少间隔 10 分钟。窗口内的后续失败合并到一条等待中的记录，`merged_count` 累加，载荷换成最近一次运行。
- **重试与超时。** 普通 webhook 和 Slack 在 10 秒内返回 2xx 视为送达；飞书还需完整响应体中的 `code: 0`（兼容旧 `StatusCode: 0`）确认，见[官方机器人说明](https://open.larksuite.com/document/uAjLw4CM/ukTMukTMukTM/bot-v3/use-custom-bots-in-a-group)。非零状态码、响应读取失败、超过 64 KiB 或没有有效状态码都按失败处理，避免把截断的错误响应当作送达。失败后按 1 分钟、5 分钟、30 分钟重试，共 4 次尝试；测试通知不重试。不跟随重定向。发送记录保留 30 天。
- **订阅与权限。** 展开时只发给渠道启用、所有者未停用且仍是项目成员的订阅；订阅在事件发生之后才建立的不补发。每次实际发送前（包括重试和合并等待之后）再次核对当前权限及项目、任务、事件订阅。账号被停用、成员被移出或订阅不再匹配时，旧发送记录标记失败，不发出请求，也不在恢复权限后补发；查询权限遇到数据库错误时保留待发送状态，恢复后重试。
- **渠道详情与测试。** 渠道列表和更新结果只展示当前仍可查看的订阅，历史发送记录不再带出无权查看的任务名称；读取时不删除原订阅，恢复权限后重新可见。测试通知选用第一个仍可查看的订阅项目，均不可查看时用本人的个人项目；没有可访问项目时返回 `not_found`，不发出请求。名称投影的权限查询失败会返回错误，更新事务回滚。 控制台发送记录每页读 50 条，可继续加载较早记录；后页失败保留当前记录并提供重试，关闭或切换渠道会取消旧读取。
- **SSRF 防护。** 默认只允许发往公网单播地址：回环、私有、链路本地（含 169.254.169.254）、CGNAT、文档和保留网段，以及能嵌入 IPv4 的 IPv6 过渡前缀一律拒绝。保存渠道时检查 IP 字面量和 localhost，每次建立连接时再检查域名实际解析到的地址，防止 DNS 重绑定；发送不经过环境变量中的代理。需要发往内网接收方时，由部署方在配置 `notify.allowed_networks` 中列出允许的网段。这是部署级设置而不是后台设置：允许访问内网等同于放开一条从 server 出发的网络通道，应当和其他网络配置一起由运维管理。
- **密钥保存。** 渠道的目标地址和签名密钥合在一起经 `ChannelSealer` 保存，接口只返回打码后的地址，签名密钥只在创建时返回一次，审计记录不含地址。凭证加密（第 12.3 节）落地前使用 `PlainSealer`，`secret_kek_id` 为 `plain`，表中是明文；接入加密实现后，旧记录仍可读取，下次修改地址时改为加密保存。
- **内容。** 原因取运行的错误信息，去掉控制字符，再次遮盖 Lerryn 令牌和 Bearer 令牌，截断到 200 个字符。飞书使用 `post` 富文本消息，Slack 使用 mrkdwn 文本并转义 `&`、`<`、`>`。通知中的时间按渠道所有者在个人设置中的时区显示。

## 15 部署与运维

### 15.1 发行物

| 发行物 | 说明 |
| --- | --- |
| `ghcr.io/niltavi/lerryn` 镜像 | 包含 server、runner、all-in-one 和 CLI；amd64 和 arm64 |
| 运行时镜像 | `lerryn-runtime-python`、`lerryn-runtime-bun`、`lerryn-runtime-shell`，按版本发布，记录 digest |
| `lerryn` CLI | macOS、Linux、Windows 二进制，通过 GitHub Releases 发布 |
| Compose 文件 | 单机和标准两种拓扑 |
| Helm Chart | P1 |

所有镜像都可以同步到企业私有仓库，用于内网和离线部署。

### 15.2 Docker 部署

单机拓扑的最小配置。先在宿主机上准备数据目录，交给镜像里的 nonroot 用户（uid 65532）：

```bash
sudo mkdir -p /var/lib/lerryn && sudo chown 65532:65532 /var/lib/lerryn
```

```yaml
services:
  lerryn:
    image: ghcr.io/niltavi/lerryn:0.1.0
    command: ["all-in-one"]
    volumes:
      # 数据目录在容器内外使用相同路径，不能换成命名卷
      - /var/lib/lerryn:/var/lib/lerryn
      - /var/run/docker.sock:/var/run/docker.sock
    group_add:
      # 宿主机上 docker.sock 所属组的 GID，用 stat -c %g /var/run/docker.sock 查看
      - "999"
    ports: ["33333:33333"]
```

数据目录必须是同路径的绑定挂载。runner 在容器中运行时，它看到的是自己容器里的路径，而 daemon 解析任务容器的挂载源时用的是宿主机路径。如果用命名卷 `lerryn-data:/var/lib/lerryn`，卷在宿主机上的实际路径是 `/var/lib/docker/volumes/lerryn-data/_data`，和容器里的 `/var/lib/lerryn` 不同，执行器创建任务容器时会因为挂载源不存在而失败（第 7.2 节），任务一个都启动不了。S4 复现过这个问题，0.2 版的 Compose 示例照着部署就会这样失败。同路径绑定挂载让两边路径一致，CI 的 `go-integration-in-container` 作业按这种方式运行执行器测试。

镜像以 nonroot（uid 65532）运行，镜像里预建了属于这个用户的 `/var/lib/lerryn`。用命名卷时数据目录会继承这个属主，改用绑定挂载后，宿主机上的目录要先交给 65532，所以有上面的 `chown`。容器里没有 root，访问 Docker socket 需要用 `group_add` 加入 socket 所属的组；distroless 镜像里没有 docker 组，只能写数字 GID。

另一种做法是让 runner 改用"命名卷加子路径"挂载，例如 `--mount type=volume,src=lerryn-data,dst=/sbx/work,volume-subpath=runs/<id>/work`。它不需要知道宿主机路径，命名卷、绑定挂载、远程 daemon 都能用，S4 在原生 dockerd 上手工验证过，只读挂载、非 root 和去掉 capabilities 的容器下都正常。代价是需要 Docker Engine 26 以上（API 1.45），Podman 的支持情况没有验证，执行器也要先支持这种挂载形式。首版的 Compose 示例用同路径绑定挂载，它是执行器当前支持、CI 也在覆盖的方式；卷加子路径是否作为容器化 runner 的默认方式，见第 21 节。

启动后打开控制台，第一个注册的账号成为管理员。首次启动时如果没有找到主密钥文件，会生成一个并在日志中提示备份位置。

标准拓扑包含 2 个 server、若干 runner 和 PostgreSQL，对象存储使用企业已有的 S3 兼容服务。

两种拓扑的 Compose 文件在 `deploy/single-node/` 和 `deploy/multi-node/`，安装步骤见 [docs/zh-CN/deploy.md](deploy.md)。标准拓扑的参考部署用 Caddy 做反向代理，对象存储用 MinIO 的社区构建（官方已不再发布社区版镜像）。

runner 需要访问 Docker 或 Podman 的 API，这基本等于拥有所在主机的 root 权限。建议：

- runner 部署在专用主机上，不和 server、数据库混部。
- runner 主机上的 dockerd 自己管理 iptables，否则任务之间的网络隔离不生效（第 7.5 节）。runner 启动时自检 daemon 能否施加默认加固，不能就拒绝领取运行（第 7.2 节）。all-in-one 连不上 daemon 或自检不通过时直接退出并给出原因，不会只起 API 而让运行一直排队。
- 能用 rootless Docker 或 rootless Podman 时优先使用。rootless 下需要委派 cgroup 控制器，否则资源限制不生效，执行器会拒绝运行。这两种方式尚未验证（第 21 节）。
- runner 自身以容器运行时，数据目录在容器内外使用相同路径，原因同上。runner 以非 root 运行时，用 `group_add` 加入 Docker socket 所属的组。

### 15.3 配置

使用一个 YAML 配置文件，所有项都可以用环境变量覆盖（`LERRYN_` 前缀）。敏感配置通过文件读取，不直接写在环境变量里。开放注册、本地密码登录这类可以在后台修改的设置存在数据库中，不在配置文件里。

```yaml
server:
  listen: ":33333"
  public_url: "https://lerryn.example.com"
  runner_registration_token_file: "/run/secrets/runner-registration-token"  # 可选，第 4.5 节
database:
  driver: sqlite                    # sqlite 或 postgres
  dsn: "/var/lib/lerryn/lerryn.db"
storage:
  driver: local                     # local 或 s3；多个 server 时必须用 s3
  path: "/var/lib/lerryn/storage"   # local 使用
  s3:                               # s3 使用，第 6.4 节
    endpoint: "https://s3.example.com"
    region: us-east-1
    bucket: lerryn
    prefix: ""
    path_style: true
    access_key_id_file: "/run/secrets/s3-access-key-id"
    secret_access_key_file: "/run/secrets/s3-secret-access-key"
secrets:
  master_key_file: "/var/lib/lerryn/master.key"
auth:
  oidc:                             # 可选
    issuer: "https://login.example.com"
    client_id: "lerryn"
    client_secret_file: "/run/secrets/oidc-client-secret"
runner:
  mode: embedded                    # embedded（all-in-one）或 remote
  labels: [default]
  slots: 4
  allowed_networks: [none, full]
  data_dir: /var/lib/lerryn/runner  # 运行目录、环境缓存、runner-id 和 runner-credential；容器内外路径必须一致
  python_image: ghcr.io/astral-sh/uv:0.13.6-python3.12-trixie-slim
  node_image: node:22.23.4-trixie
  shell_image: bash:5.3.20-alpine3.24
  server_url: "https://lerryn.example.com"                          # remote 使用
  registration_token_file: "/run/secrets/runner-registration-token" # remote 首次启动时换取凭证
mirrors:                            # 可选，内网环境使用
  pypi: "https://mirrors.example.com/pypi/simple"
  npm: "https://mirrors.example.com/npm/"
mcp:                                # 各项说明见第 10.4 节
  disable_localhost_protection: false
  json_response: false
  max_result_bytes: 0               # 0 表示使用默认值 32 KiB
retention:                          # 第 6.6 节，0 表示不限
  runs_days: 0
  logs_days: 0
  artifacts_days: 0
  logs_max_mib: 0
  artifacts_max_mib: 0
log:
  level: info                       # debug、info、warn、error
  format: json                      # json 或 text
```

完整的示例在 `deploy/lerryn.example.yaml`。配置文件中出现未知的键时，启动直接报错；所有无效项一次报告完。

### 15.4 升级

- 首版的 server 升级允许短暂停机：停止全部 server，启动新版本。server 启动时自动执行待执行的迁移，也可以先用 `lerryn migrate` 单独执行，它可以重复运行。
- 多个 server 同时启动也安全。PostgreSQL 上用会话级 advisory lock 串行化迁移，锁 ID 由当前 schema 派生，这样同一个库里的不同 schema、以及同库中其他使用 goose 的应用不会互相阻塞；拿不到锁时每秒重试，最长等 5 分钟。每个迁移在自己的事务里执行。SQLite 只有一个 server，不加锁。
- 数据库的迁移版本比二进制新时，server 和 `lerryn migrate` 都拒绝运行。goose 本身遇到这种情况什么也不做并返回成功，迁移之后要另外检查。所以回退二进制时必须同时恢复数据库。
- server 兼容当前和上一个版本的 runner 协议，runner 可以在 server 升级后逐台升级。
- 每个版本的发布说明写明能否直接回退，以及回退需要的步骤。
- 不停机的滚动升级放到 P1，届时迁移需要遵循先扩展、后收缩。

### 15.5 备份、恢复与导出

| 内容 | 方式 |
| --- | --- |
| 数据库 | PostgreSQL 用企业现有方案；SQLite 用 `lerryn backup` |
| 对象存储 | 存储自身的复制或备份 |
| 主密钥 | 单独保管，不和数据库备份放在一起 |

导出格式是一个压缩包，包含任务、版本、触发器、授权关系和凭证引用，不含凭证明文。它有三个用途：跨实例迁移、SQLite 迁到 PostgreSQL、A15 的恢复验证。导入时保留运行关联；对象存储中已经不存在的产物标为缺失。

M6 的实现（`server/internal/ops`，操作步骤见 [docs/zh-CN/deploy.md](deploy.md)）：

- `lerryn backup` 在快照前登记独立的 `blob-backup:` 租约（30 秒有效，每 10 秒续期），用 `VACUUM INTO` 在一个读事务里复制 SQLite，再按快照引用复制本地对象，打成一个 tar.gz，server 运行时可以执行。对象回收在每个 SQLite 写事务内检查这些租约，有备份时暂缓删除对象，普通写入与保留策略的记录清理继续执行。对象在复制时校验 SHA-256；续期失败、保护中断、对象缺失或校验不符时移除残缺归档，临时文件与存储目录里的无关文件不进入备份。`lerryn restore` 在创建暂存区前检查数据库及其 WAL、SHM 路径与存储目录是否重叠；暂存父目录创建后再检查一次，覆盖此前不存在的大小写别名，重叠时清理本次暂存内容和新建的空目录。完整解压并检查后才替换现有数据，原数据改名保留。PostgreSQL 不走这两个命令，用 `pg_dump`。
- 导出按表导出全部数据，而不是只挑任务相关的几类对象。SQLite 迁到 PostgreSQL 需要完整的实例，账号、令牌、运行和日志都要带走；按表导出也不需要为以后新增的表逐一修改导出代码。表和外键从数据库读取，按外键顺序写入；值转成两种数据库都接受的形式（布尔、UTC 文本时间、文本 UUID）。对象按引用它的列收集，与保留策略回收对象时检查的引用一致：各表的 `blob_hash`、未过期产物的 `blob_key`、运行的 `log_blob` 和 `input_key`，以及 `log_blob` 出现之前归档的日志在 `log_archive` 中记录的对象。最初只收集 `blob_hash`，产物、归档日志和 webhook 请求体没有导出，导入的实例上这些内容读不到。不导出 goose 的版本表和租约表。凭证按密文导出，导入的实例需要同一个主密钥。
- 导入要求目标库为空：先迁移到导出时的版本，在一个事务里写入并核对每张表的行数，再执行之后的迁移。
- 恢复和导入在 tar 结束后继续流式读到 gzip 的实际 EOF，校验所有 gzip 成员的尾部；CRC 错误、截断或尾部格式错误会在恢复交换前拒绝，或使导入业务记录的事务回滚。读取与尾部校验均支持取消，正常的 tar padding 和多 gzip 成员保持兼容。
- `lerryn migrate-db --to-config` 把导出和导入合成一步，最后比较两边每张表的行数。
- 整库导出导入搬的是整个部署。单个任务另有任务包，见下文。

**任务包（单个任务的导出导入）。** 整库导出保留全部 ID，单个任务放进另一个部署时，项目、创建者、凭证和 webhook 令牌在那里都没有对应。任务包因此不做映射，只带可移植的部分，其余由导入的一方重新确定：

- `exportTask`（`GET /api/v1/tasks/{taskId}/export`，读取）返回 JSON 的 `TaskBundle`：格式标识和格式版本、任务名、所选版本（默认最新，`history=true` 时连同之前的版本）的清单和全部文件、触发器定义（`triggers=false` 时不带）。导出时的激活版本号和服务端版本只作参考。不含运行记录、日志、产物，不含凭证的值和绑定、webhook 令牌，也不含项目、用户、任务的 ID。文件本来只能是 UTF-8 文本，所以任务包是普通 JSON，不需要压缩包。
- `importTask`（`POST /api/v1/projects/{projectId}/task-imports`，变更）在目标项目建立新任务。目标项目由调用者选择，调用者成为任务、每个版本和每个触发器的创建者，版本的 `client` 记为调用者的令牌名。版本按顺序从 1 重新编号，响应给出新旧版本号的对应。任务未激活。
- 凭证引用仍是清单里的引用名（第 5.3 节按名字引用正是为了这一点），导入后都没有绑定，响应的 `credentials` 全部为 `missing`，由用户经填写请求提供，或由凭证所有者授权。导入的版本 `tested_at` 为空，激活前必须测试的规则照常生效。webhook 触发器生成新的令牌，只在导入的响应中出现；cron 触发器重新计算下一次触发时间。
- 校验与 `applyTask` 相同：每个版本的文件数量、大小、路径和清单规则，以及第 11.7 节的明文检查（清单、文件和触发器参数，和调用者自己的凭证比较，新任务没有绑定的凭证）。触发器按 `setTriggers` 的规则校验。出错位置的 JSON Pointer 指向任务包内，例如 `#/bundle/versions/0/files/1/content`。任务包的形状（格式标识、格式版本、未知字段）由契约校验，不认识的格式版本返回 400。
- 目标项目已有同名且未归档的任务时返回 412 `version_conflict`，与 `applyTask` 带 `If-Match: 0` 时相同，调用者用 `name` 换一个名字。这也让重复的导入请求自然失败，所以 `importTask` 不接受 `Idempotency-Key`：幂等记录会保存首次的响应，其中有 webhook 令牌的明文，而令牌在数据库里只应存哈希。
- 权限：导出需要查看任务，查看者也可以。任务包里的内容查看者本来就能经 `getTaskVersion`、`getTaskFile` 和 `listTriggers` 读到，导出没有扩大可见范围；不让查看者导出只会让他们逐个文件去读。导入需要目标项目的修改任务权限（维护者或所有者），与 `applyTask` 相同。令牌范围按 `x-operation-kind`：导出 `read`，导入 `write`。
- 上限：导入的请求体与其他接口一样最大 4 MiB。导出时按编码后的大小累加，从所选版本往前取，超过约 4 MiB 或 50 个版本时省略更早的版本，省略的数量写在 `omitted_versions` 中，导出的结果总能导入。单个版本的文件最多 1 MiB，正常情况下一定放得下；JSON 转义让一个版本超过上限时，导出返回 `validation_failed`。
- 审计：导出写 `exportTask`（读取操作本来不记录，导出把任务的全部代码带离部署，所以单独记录），详情为导出的版本号、省略数和是否带触发器；导入写 `importTask`，详情为源任务名、版本、建立的触发器和需要绑定的凭证引用名。拒绝含凭证值的导入按第 11.7 节记 `denied`。
- 两个操作都不作为独立的 MCP 工具（`x-mcp: {expose: false}`），经 `api_call` 调用：它们不在第 11.3 节的主流程里，而且任务包可能远大于单次工具结果的 32 KiB 上限。导入的响应只带任务的 ID、名字、最新版本号和控制台链接，不带完整的 `Task`，否则 `api_describe importTask` 会超过这个上限。CLI 为它们提供 `lerryn tasks export` 和 `lerryn tasks import`（[docs/zh-CN/cli.md](cli.md)）。
- 不需要迁移：导入写的是已有的任务、版本、文件和触发器表。

### 15.6 可观测

- `/healthz`：进程存活。`/readyz`：数据库可达，迁移版本与二进制一致，即内嵌的迁移都已应用，数据库里也没有更新的版本。检查直接读取 goose 的版本表，不写入任何东西，其他 server 正在迁移时也可以调用。
- Prometheus 指标：队列长度、各状态运行数、领取延迟、调度延迟、日志写入速率、runner 在线数和空闲槽位、SSE 连接数。
  - M6 已实现（`server/internal/metrics`）：按 operationId 统计的请求数和耗时、各状态运行数、队列长度和最早排队时长（代替领取延迟）、runner 在线数、槽位总数和占用数、租约过期数、数据库连接池。调度延迟、日志写入速率和 SSE 连接数随对应功能补上。
  - 队列和 runner 的数字在抓取时从数据库读取，多个 server 报告相同的值，汇总用 `max()`。
  - 指标默认关闭。开启后要么用单独的监听地址（`metrics.listen`），要么挂在公开监听地址上并要求 Bearer 令牌（`metrics.token_file`），不会无保护地暴露在公网。
- 镜像里没有 shell 和 curl，容器健康检查用 `lerryn healthcheck`，它请求本机的 `/readyz`。
- 服务自身的日志为 JSON 格式，不包含凭证和任务日志内容。
- OpenTelemetry 追踪放到 P1。

## 16 仓库、许可证与工程约定

### 16.1 目录

```
go.mod                Go module：github.com/niltavi/lerryn
api/
  openapi/            公开 API 规范，按资源拆分
  bundled/            Redocly 打包结果，生成物，提交入库
  spec.go             用 go:embed 嵌入打包结果（api.Spec）
  README.md           契约的写法约定
server/
  cmd/lerryn/         入口：server、runner、all-in-one、migrate、admin、CLI
  internal/
    app/              组装入口：路由、/mcp 挂载、就绪检查
    config/           配置文件和环境变量
    httpapi/          生成的接口和适配
    mcp/              工具注册表（运行时从契约构建）和进程内转发
    service/          业务逻辑和权限检查
    auth/             内置账号、OIDC、会话、访问令牌、配对
    store/            数据访问；dialect/ 下是方言专属 SQL
    scheduler/        调度循环和对账
    runner/           runner 主循环
    runproto/         runner 协议的调用和数据（Coordinator 接口）
    runnerhttp/       runner 协议的 HTTP 传输：server 端处理器和 runner 端客户端
    executor/         执行器接口；docker/、local/ 实现
    secrets/          加密、主密钥提供方、脱敏
    blob/             对象存储接口和本地、S3 实现
    events/           事件总线：进程内和 PostgreSQL LISTEN/NOTIFY 实现
    leader/           选主，主节点的后台工作由它启动
    notify/           通知出口
    cli/              命令行子命令
    web/              凭证填写页等服务端渲染页面
  migrations/
    postgres/
    sqlite/
runtimes/             运行时镜像
  sdk/                sbx 命令和 Python／TS 辅助库（Apache-2.0）
skills/               Agent Skills（Apache-2.0）
apps/                 web、mobile
packages/             tokens、ui-web、ui-native、api、core
deploy/               Compose 文件，后续加 Helm
docs/                 语言入口；en/ 英文，zh-CN/ 中文（含 PRD、技术设计、实施规划），assets/ 共用资源
tests/
  acceptance/         A01 至 A18、B01 至 B03
  chaos/              多节点故障注入
  agent-evals/        Skill 评测
.gitea/workflows/    CI 与发布
```

Go module 放在仓库根目录，与现有的 pnpm workspace 并存，覆盖 `server/`、`tests/` 和 `runtimes/` 下的 Go 代码。`apps/` 和 `packages/` 的具体内容以设计语言和组件库工作的结果为准。

### 16.2 许可证

- 仓库整体采用 AGPL-3.0，根目录放 `LICENSE`。
- 对使用者的含义：企业不修改源码直接部署，没有额外义务；修改后通过网络向成员提供服务，需要向这些用户提供修改后的源码。
- 例外：`runtimes/sdk/`（`sbx` 命令和 Python、TypeScript 辅助库）和 `skills/` 采用 Apache-2.0，各自目录下放单独的 `LICENSE`。这两部分会进入用户自己的任务代码和 Agent 环境，用宽松许可证，企业就不用担心自己的任务代码被视为 AGPL 的衍生作品。
- 复用第三方代码：GPL-3.0（如 Dagu）和 AGPL-3.0（如 OliveTin）的代码可以与本项目组合；MIT、Apache-2.0、BSD 代码也兼容。复用时保留原版权和许可证声明，并在 `THIRD_PARTY_NOTICES` 中记录来源。复用仍以必要为限，不因为功能相似就整段搬用。
- 外部贡献规则待定：如果将来可能采用双许可证等商业方案，需要在接受第一笔外部贡献前引入 CLA；否则使用 DCO（提交时带 `Signed-off-by`）即可。

### 16.3 面向 AI 的工程约定

- 仓库根目录放 `AGENTS.md`，写明分层、生成链路和禁止事项；`CLAUDE.md` 指向它。
- 分层固定：接口层（HTTP、MCP）→ service → store。接口层不写业务逻辑，权限检查只在 service 层。
- 接口改动从 `api/` 开始，然后运行 `make generate`。生成的代码不手工修改。
- `make check` 是合并前的必过项：Spectral、golangci-lint（含 gofmt、goimports、gosec）、go vet、带 `-race` 的 Go 测试（SQLite）、govulncheck、TypeScript 类型检查。生成物一致性（`make generate-check`）和 PostgreSQL 上的测试（`make test-pg`）需要额外的工具链或数据库，由 CI 运行。oasdiff 在第一个版本发布后加入，前端测试随控制台轨道加入。
- CI 使用 Gitea Actions。作业有：`go`（lint、vet、SQLite 测试、govulncheck）、`api`（生成物一致性、Spectral、许可证声明）、`go-postgres`（PostgreSQL 17 上的测试）、两个执行器集成测试作业（第 16.4 节）、`web`（类型检查、单元测试、官网检查）、`image`（镜像构建）。
- Runner 标签、宿主机要求和发布凭证见 [Gitea Actions 配置](ci.md)。
- CI 安装 Go 1.27 的最新补丁版本。`go.mod` 中的 `go 1.27.0` 只是最低语言版本，setup-go 按它安装会得到 1.27.0，govulncheck 会报告后续补丁已经修复的标准库漏洞。
- Go 工具（oapi-codegen、goose、govulncheck）用 `go.mod` 的 `tool` 指令固定版本，用 `go tool <名字>` 运行。
- 不引入依赖注入框架和反射式路由。

### 16.4 测试

| 层级 | 内容 |
| --- | --- |
| 单元测试 | service、调度计算、脱敏、清单校验 |
| 存储契约测试 | 同一套测试在 PostgreSQL 和 SQLite 上各跑一遍。CI 目前只用 PostgreSQL 17，M5 前补上最低支持版本 15 |
| 契约测试 | 加载真实契约构建 MCP 工具注册表；按打包结果校验 API 处理函数的真实响应（第 9.5 节） |
| 执行器集成测试 | 使用真实 Docker，带 build tag `integration`：默认加固逐项在容器内核对、资源限制、取消、超时、子进程清理、挂载、网络隔离。CI 有两个作业：`go-integration` 通过 `lerryn-linux:host` runner 直接在 Linux 宿主机上运行，并要求任务之间的网络隔离生效（`LERRYN_TEST_REQUIRE_ICC=1`）；`go-integration-in-container` 让测试进程在 `golang:1.27` 容器中运行、经宿主机的 Docker socket 启动任务容器，数据目录同路径挂载，覆盖 runner 自身在容器中运行的情况 |
| 验收测试 | A01 至 A18、B01 至 B03，使用真实执行和故障注入 |
| 多节点故障测试 | 运行中杀掉 server 和 runner、断开数据库连接，检查状态和去重 |
| 前端测试 | 组件测试；Web 控制台用 Playwright 覆盖主要页面 |
| Agent 评测 | 见第 11.8 节 |

## 17 验收场景的实现对应

| 编号 | 主要机制 | 测试方式 |
| --- | --- | --- |
| A01 | MCP 工具、Skill 主流程、校验和测试接口 | Agent 评测：从零建立任务 |
| A02 | 带时区的 cron、调度循环 | 设置工作日任务，断开 Agent 后检查运行记录 |
| A03 | 控制台页面、运行详情接口 | Playwright |
| A04 | `sbx stage`、运行事件、RunTimeline | 上报三个阶段和不上报阶段的两种任务 |
| A05 | 凭证归属、授权、`not_found` 语义 | 两个用户交叉访问 |
| A06 | runner 端流式脱敏、参数中的凭证值检测 | 跨片段输出的 Token，检查日志、快照、API、MCP、控制台 |
| A07 | 准备阶段的凭证检查 | 撤销后发起新运行 |
| A08 | 部分唯一索引、幂等记录 | 运行时长超过触发间隔；重复提交同一幂等请求 |
| A09 | 停止容器，确认后再改状态 | 启动带子进程的任务后取消和超时 |
| A10 | 租约、对账、触发实例唯一约束 | 运行中重启 server 和 runner |
| A11 | 错误类别、日志游标、重跑关联 | Agent 评测：修复失败 |
| A12 | `If-Match` 和版本冲突 | 两个会话并发修改 |
| A13 | 日志分片和分帧归档、SSE 续读 | 10 万行输出，中途断线重连 |
| A14 | 容器加固、网络策略、凭证和工具授权 | 越界访问目录、使用未授权凭证、超出资源限制 |
| A15 | 导出格式、主密钥分开保管 | 恢复到新实例后继续运行 |
| A16 | 通知出口和模板 | 故意失败，检查通知内容中没有明文 |
| A17 | 移动端页面、配对登录、只读会话 | 移动端端到端测试：扫码配对，从概览进入运行查看进度；尝试写操作被拒绝 |
| A18 | 内置账号、注册和本地登录开关、OIDC、访问令牌、停用即失效 | 使用测试用 OIDC 提供方（如 Dex）：无邮箱注册、关闭注册后注册被拒、OIDC 登录、Agent 用令牌操作、停用后检查会话和令牌 |

多节点另有三项验收：

| 编号 | 场景 | 通过条件 |
| --- | --- | --- |
| B01 | 多 server 调度 | 3 个 server 同时运行 24 小时，同一计划时间没有重复运行 |
| B02 | runner 故障 | 运行中强制关闭一台 runner，租约过期后运行标为运行中断，其他 runner 继续领取新运行 |
| B03 | server 故障 | 运行中关闭一个 server，Agent 调用和控制台经其他副本继续，SSE 按 `Last-Event-ID` 续读不丢日志 |

## 18 里程碑

阶段划分、任务和完成标准以[实施规划](agent-automation-sandbox-implementation-plan.md)为准，这里只列摘要：

| 阶段 | 内容 | 覆盖验收 |
| --- | --- | --- |
| M0 工程基础与技术验证 | 仓库与 CI、契约骨架与生成链路、存储骨架、内置账号和访问令牌、技术验证 S1 至 S4 | |
| M1 最小闭环 | 单机下 Agent 经 MCP 提交 Python 任务、容器执行、读取日志和结果；取消、超时、对账 | A09、A10、A13 |
| M2 Agent 入口 | 完整 MCP 工具、CLI、Skill 第一版、Agent 评测 | A01、A11、A12 |
| M3 调度与触发 | cron、webhook、队列、并发、重试、幂等 | A02、A08 |
| M4 账号、凭证与审计 | OIDC、注册和登录设置、项目和角色、凭证全流程、审计 | A05、A06、A07、A18 |
| M5 多节点 | PostgreSQL 领取和事件通知、S3 存储、runner 标签 | A14、B01、B02、B03 |
| M6 运维与通知 | 通知、备份恢复、导出导入、升级、容量测试 | A15、A16 |
| 控制台轨道 | Web 控制台和移动端，与 M2 至 M6 并行，依赖组件库 | A03、A04、A13（界面部分）、A17 |

所有接口从 M0 起就按多节点设计（runner 协议、无状态 server、对象存储接口），M5 集中验证多节点行为。

## 19 首版从简

以下功能首版只做最基本的形式，完整版本放到 P1。理由都一样：先让一个团队在一个部署里用顺，再按真实需求补企业功能。

| 方面 | 首版 | 放到 P1 |
| --- | --- | --- |
| 账号 | 内置账号 + 一个 OIDC 提供方 | 多个提供方、用户组同步、SCIM |
| Agent 授权 | 访问令牌 | MCP OAuth 2.1 授权服务 |
| 权限 | 三种项目角色 | 更细的角色，例如日志单独授权 |
| 沙箱 | 加固的 Docker 容器，网络只有 `none` 和 `full` | 工具网关、出站白名单、gVisor 适配、Kubernetes 执行器 |
| 凭证密钥 | 本地主密钥文件 | Vault、OpenBao、云厂商 KMS |
| 并发控制 | 全局并发（runner 槽位）+ 同一任务不重叠 | 项目级配额 |
| Webhook 触发 | 令牌鉴权 | HMAC 签名 |
| 通知 | webhook，预置飞书和 Slack 格式 | 邮件、钉钉、企业微信 |
| 审计 | 记录关键操作，API 查询 | 查看界面、导出 |
| 移动端 | 只读查看任务进度，扫码配对 | 凭证填写、推送、通知设置 |
| 升级 | server 短暂停机升级，runner 逐台升级 | 滚动升级 |
| 部署 | Docker Compose | Helm Chart |
| 可观测 | Prometheus 指标、健康检查 | OpenTelemetry |

## 20 已知限制

这份设计有几处明确的边界，先写清楚：

- **runner 主机权限高。** runner 能调用容器运行时，基本等于所在主机的 root。runner 主机需要专门管理，不适合和其他服务混部。
- **隔离面向企业内部。** 默认的容器加固适合企业内部成员提交的代码，不适合运行陌生人提交的代码，也不宣称适合跨组织多租户。
- **任务之间的网络隔离依赖 dockerd。** `full` 网络的任务之间不能互访，靠的是 dockerd 管理的 iptables 规则；关闭了 dockerd 的 iptables 管理，或者在 OrbStack 这类桌面环境中，这项隔离不生效。`full` 网络下任务仍能访问宿主机网关和宿主机上发布的端口。
- **已注入的凭证收不回。** 撤销只影响新运行。一个已经合法拿到凭证的脚本，可以变换或外传这个值，加密和脱敏都拦不住。所以修改代码的权限和使用凭证的授权需要一起看。
- **工具调用记录依赖任务配合。** 只有经 `sbx mcp call` 发出的调用会被记录；工具凭证会进入容器。工具网关在 P1 解决这两点。
- **开放注册默认开启。** 部署后到首次注册之间，任何能访问控制台的人都可以注册成为管理员。公网部署时需要先完成首次注册，或预先创建管理员。
- **SQLite 不能多节点。** 选了 SQLite 的部署要扩容，得先迁到 PostgreSQL。
- **日志写入压在数据库上。** 运行中的日志分片写入数据库，并发运行多、输出量大时会增加数据库负担。`LISTEN/NOTIFY` 在极高事件量下也可能成为瓶颈。容量目标确定后需要压测，必要时把日志热数据和事件总线换成独立实现。
- **移动端只能看。** 首版移动端没有推送，也不能做任何操作。
- **MCP 没有服务端主动请求。** 无状态模式下，服务端不能向客户端发 sampling、elicitation 这类请求，也发不出 `list_changed` 通知。Lerryn 目前不需要这些能力，以后需要时要重新评估传输方式。

## 21 待确认事项

| 事项 | 影响 | 当前处理 |
| --- | --- | --- |
| 外部贡献规则 | 将来能否采用双许可证 | 待定；接受第一笔外部贡献前确定 |
| 标准拓扑的容量目标 | 日志写入方案、默认 runner 规格、压测设计 | 暂按 1000 个任务、50 个并发运行设计 |
| 公司首个 OIDC 提供方 | M4 的联调对象 | 待确认 |
| 是否需要 Kubernetes 执行器 | 执行器优先级 | 暂列 P1 |
| 项目级共享凭证 | 凭证模型 | 首版只支持个人凭证 |
| Windmill 迁移范围 | 是否需要导入工具 | 以实际任务盘点为准 |
| 容器化 runner 的挂载方式 | 部署时是否必须在宿主机上准备同路径的数据目录 | 首版用同路径绑定挂载（第 15.2 节）；执行器支持"命名卷加子路径"后，评估是否作为容器化 runner 的默认方式，宿主机上直接运行的 runner 仍用绑定挂载 |
| Podman、rootless Docker、userns-remap | 执行器的兼容范围和部署文档 | 首个试用版在原生 Linux（Docker Engine 29.9.2，arm64）上验证：rootless Docker 支持，执行器集成测试和 `TestM1` 全部通过，要求 cgroup 委派 cpu、memory、pids；Podman 4.9.3 不支持，root 模式不认 tmpfs 的 `uid` 选项，rootless 模式缺少 CPU 配额，执行器都在开始前拒绝。userns-remap 没有验证。记录见验收矩阵第 6 节 |

## 22 参考

- [oapi-codegen 发布记录](https://github.com/oapi-codegen/oapi-codegen/releases)：v2.8.0 起支持 OpenAPI 3.1
- [MCP Go SDK 发布记录](https://github.com/modelcontextprotocol/go-sdk/releases)：v1.7.0 起支持 2026-07-28 规范和无状态 Streamable HTTP，Lerryn 使用 v1.8.0
- [MCP 工具规范](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)
- [HeroUI v3 稳定版公告](https://herouiv3.featurebase.app/changelog/heroui-v3-is-officially-stable-and-pro-pre-sale-is)
- [HeroUI Native 发布记录](https://newreleases.io/project/github/heroui-inc/heroui-native/release/v1.0.1)
- [RFC 9457 Problem Details for HTTP APIs](https://www.rfc-editor.org/rfc/rfc9457)
- [GNU AGPL-3.0 许可证文本](https://www.gnu.org/licenses/agpl-3.0.html)
- PRD 第 12 节列出的同类项目源码，用于对照功能行为
- M0 技术验证记录：[S1 双数据库](spikes/s1-dual-dialect-storage.md)、[S2 OpenAPI 生成链路](spikes/s2-openapi-codegen.md)、[S3 MCP 无状态模式](spikes/s3-mcp-stateless.md)、[S4 容器执行](spikes/s4-container-execution.md)
- `api/README.md`：契约的写法约定，`x-mcp` 结构和错误码状态码以它为准

## 23 修订记录

| 版本 | 主要变化 |
| --- | --- |
| 0.1 | 初稿：后端选 Go；HTTP API 以 OpenAPI 3.1 为唯一契约，MCP、CLI 和前端客户端由它派生；MCP 用 Streamable HTTP 无状态模式；PostgreSQL 与 SQLite 两种数据库；server 与 runner 分离，部署内多节点，协调落在数据库上；容器执行；Skill 体系与评测 |
| 0.2 | 按已经确定的决策收敛范围：首版面向企业自部署，企业专属功能尽量少做，MCP OAuth、外部密钥服务、工具网关、审计界面、滚动升级等移到 P1（第 19 节）；增加门槛很低的内置账号，与 OIDC 并存，管理员可关闭开放注册和本地密码登录；沙箱只用加固的 Docker 容器；移动端只读查看任务进度；确定名称 Lerryn 和 AGPL-3.0 许可证；里程碑和目录与实施规划同步 |
| 0.3 | 按 M0 技术验证 S1 至 S4 修订，补上合并后已经实现的事实。存储：选主改为 upsert，SQLite 写事务用 `BEGIN IMMEDIATE` 并在进程内串行，类型对照和建模约定，方言专属代码范围，PostgreSQL 最低版本 15，启动时自动迁移和迁移锁，`/readyz` 检查迁移版本。契约：生成链路以打包结果为输入并嵌入服务端，新增错误码 `unauthenticated`、`internal_error`、`out_of_memory` 和状态码对应，请求 ID、请求校验、可空字段的约定，`discover` 的响应结构。MCP：`x-mcp` 结构和工具标注以 `api/README.md` 为准，工具注册表运行时构建，`/mcp` 挂在 API 处理链上，新增 `mcp` 配置段，删除旧协议的会话粘滞预案，`/mcp` 前置令牌校验。执行：默认加固补全，只用 `Mounts` 挂载，`full` 网络的任务间隔离及其前提，OOM 的判定规则，停止确认的细节，凭证改为经 stdin 交给 `sbx` 入口写入。部署：Compose 示例改为同路径绑定挂载并说明原因，镜像预建 `/var/lib/lerryn`，CI 新增两个执行器集成测试作业。账号：按 M0 账号骨架的实现补全第 12.1、12.2 节（用户名规则、会话 cookie、`Origin` 校验、令牌只能在会话中创建、按契约声明鉴权、登录限速、权限框架），`access_tokens` 增加 `prefix`，新增错误码 `username_taken`、`rate_limited`、`origin_not_allowed` |
| 0.4 | 按 M1 的实现修订：runner 续租间隔改为 2 秒；第 7.6 节改为先上报终态再删除容器，并说明 runner ID 的保存位置；新增错误类别 `run_interrupted`，runner 重启清理和租约过期都用它；第 9.3 节的请求校验改为已实现的事实；第 7.7 节说明日志行阶段标注的近似性；第 5.4 节补充 `OOMKilled` 在 cgroup v2 上不可靠，主进程以 137 退出时推断为 `out_of_memory`；`storage.path` 默认值改为 `/var/lib/lerryn/storage`，避免与对象前缀 `blobs/` 叠成两层 |
| 0.4（M2 补充） | MCP 注册表为全部操作建立索引，新增 `api_describe` 和 `api_call`（第 10.3 节）；CLI 改为由操作索引驱动，不基于生成的 Go 客户端，补充登录、配置文件、输出格式、退出码和 stdio 代理的设计（第 10.6 节，详见 `docs/zh-CN/cli.md`） |
| 0.4（控制台补充） | 按 Web 控制台的第一版修订第 13 节：仓库结构改为设计令牌和 `ui-native` 留在 `design/`、`ui-web` 的主题由同一个生成脚本输出；写明控制台的栈、嵌入方式、挂载与缓存规则、CSP，以及接口未就绪页面的处理 |
| 0.5（M2 实现） | 第 5.2 节写明"成功的测试运行"的判定和归档的效果；第 5.5 节运行事件由 runner 和服务端共用一个序号，新增 `runs.runner_event_seq`；第 7.4 节产物的收集、上限、类型判断和下载头；第 9.3 节幂等中间件、`If-Match` 的细节和 SSE 的实现方式。契约修正：`RunEvent.data` 和 `RunResult.data` 原来只有说明没有类型，OpenAPI 3.0 语义的校验器（kin-openapi 的响应校验）把它们当成不可为 null，现在列出全部 JSON 类型和 null。校验接口按 runner 标签检查有没有能领取的在线 runner；工具清单尚无登记接口，引用任何工具都报错；日历的计划触发在调度器提供展开 cron 的接口前只列每个触发器的下一次 |
| 0.5 | 写出 M2 至 M6 和控制台的完整契约与数据模型：第 9.2 节记录契约与端点草案的差异；第 5.3 节清单的凭证改为按引用名声明、由授权绑定，新增 Agent 面板 `panels`，触发器按 `name` 整体替换并带 `revision`；第 5.5 节按迁移 20261104010000 至 20261104014000 更新表（任务归档和激活列、运行的来源、重试和重跑列、触发器、幂等、配对码、凭证、产物、通知）；第 7.4 节新增 `/sbx/in`；第 8.2 节新增 `catch_up: latest`；第 12.4 节更新凭证也走填写请求；第 14 节通知渠道属于用户本人。新增错误码 `permission_denied`、`precondition_required`、`version_not_tested`、`task_archived`、`password_login_disabled`、`last_owner`。SQLite 迁移中用 ALTER 加的列不带外键，因为 SQLite 不能删除参与外键的列，回滚会失败 |
| 0.5（M4 凭证） | 第 12.6.1 节记录凭证的实现：加密格式和附加数据、主密钥文件格式与首次生成、`lerryn secrets rotate-key` 和 `rewrap`、字段名保留规则、填写只接受会话、同名凭证加后缀、替换他人绑定需要修改任务的权限、停用账号的凭证不可用、`startRun` 预检、runner 协议的 `Secrets` 方法和 `/runner/v1/runs/{id}/secrets`、入口改为 SDK 中的 `sbx-exec` 及其 stdin 格式、逐行脱敏与识别的编码形式、填写页的登录、切换账号和 CSRF 方案。通知渠道的密钥改用同一套加密 |
| 0.4 补充（M5 多节点核心） | 第 4.5 节：runner 协议的 HTTP 传输落在 `server/internal/runnerhttp`，线上格式用 Go 类型定义，不再计划 `api/runner.yaml`；接口清单改为实际实现，补充注册令牌与 runner 凭证、错误分类、协议版本头、长轮询领取。第 4.4 节：标签匹配的写入与规则、停用 runner 只排空不领取、选主的时间参数和退位规则、事件总线的两类事件与轮询兜底。第 5.5 节：`runners`、`runner_labels`、`run_required_labels`、`runs.log_archive` 按迁移更新，新增 `runner_tokens`。第 6.4 节：S3 实现自写 SigV4、路径风格、键前缀、启动检查。第 6.5 节：日志归档的格式和触发，归档对象改为内容寻址放在 `blobs/sha256/` 下。第 15.3 节：新增 `server.runner_registration_token_file`、`storage.s3.*`、`runner.server_url`、`runner.registration_token_file` |
| 0.5（W6 Skill 与评测） | 第 10.6 节：`lerryn skills install` 已实现。第 11.5 节：清单字段、工具与 CLI 对照、错误码三份参考资料由契约生成，示例任务在集成测试中运行。第 11.6 节：Skill 嵌入二进制，安装方式和覆盖规则。第 11.8 节：评测框架的实现、断言依据和依赖缺失场景的写法 |
| 0.5 补充（M4 账号与审计） | 第 12.1 节：OIDC 的实现（PKCE、state 和 nonce 放在签名 cookie 中、用户名规则、首个账号）、设置缓存、停用账号时撤销全部凭证、修改和重置密码结束会话、配对码和只读设备会话（设备会话可以登出且不要求 `Origin`）；`discover` 新增 `capabilities.first_account`；新增配置 `auth.oidc.username_claim`。第 12.2 节：角色不允许的动作返回 `permission_denied`。第 12.7 节：审计字段从请求 context 读取、`action` 取 operationId、未记录的变更由 HTTP 层兜底；按名称排序的列表在 PostgreSQL 上用 `COLLATE "C"` |
| 0.5（首个试用版补充） | 第 7.8 节：脚本工具（迁移 20261107020000）、`sbx mcp call` 在三种运行时的实现、`tool.call` 事件和 runner 协议的可选字段；网络策略对工具调用的影响。第 11.7 节：拒绝含有凭证值的提交，检查范围（含任务文件和工具登记）、比较的凭证、密钥派生的指纹（迁移 20261107010000）、旧数据和轮换、错误形式和审计。第 12.6 节：脱敏与明文检查共用编码形式。客户端断开导致的请求失败改记 DEBUG 日志 |
| 0.5（保留策略） | 第 6.6 节写出保留策略的实现：`retention` 下的五项配置及默认值、清理顺序和保护的运行、被清理内容返回 410 `expired`（新增错误码，`Run.logs_expired_at`）、审计与指标、按引用回收内容寻址对象及其与写入方的配合（迁移 20261108010000，新增 `runs.log_blob`、`runs.log_bytes`、`runs.logs_expired_at` 和 `blob_releases`）。项目级收紧和单次运行上限的配置没有做。`streamRunLogs` 和 `downloadRunLogs` 改为也读归档 |
| 0.5（控制台填写框） | 第 9.2、12.4、12.6.1 节：用户在控制台里时，凭证在模态框中填写，不再跳转到填写页；新增 `fillCredentialRequest`（只接受登录会话）和错误码 `request_closed`。填写页保留给 Agent 交出的链接 |
| 0.5（M3 补充） | 按 M3 的实现修订第 8 节：cron 的夏令时规则和解析库；调度循环先比较并交换 `next_fire_at` 再插入 TriggerFire；未激活任务的触发器只向后移动；错过记录的形式；webhook 单独挂载在 `/hooks/` 和令牌前缀 `wl_hook_`；队列上限和项目级并发（原计划 P1，提前实现）及其配置项；重试的决定记录 `runs.retry_decided_at`（迁移 20261105020000）。请求体交到容器的 `/sbx/in` 尚未实现 |
| 0.5（任务包） | 第 15.5 节：单个任务的导出导入以任务包实现，`exportTask` 和 `importTask`，导入的一方选择项目、成为创建者，凭证引用保留名字、需要重新绑定，webhook 令牌重新生成，导入的版本未测试；校验、明文检查、权限、上限、审计和不接受幂等键的理由。第 9.2 节记录两个新端点。导入在事务末尾向对象回收登记文件对象（`claimBlobs`）。整库导出补上产物、归档日志和 webhook 请求体的对象 |
| 0.5（控制台运行按钮） | 第 13.3 节：控制台增加“手动执行”和“重试”按钮；新增 `Task.console`、`setTaskConsole`（不作为独立 MCP 工具）、错误码 `manual_run_disabled`；第 5.5 节 `tasks` 加列 `console_hide_manual_run`（迁移 20261109010000）；任务包带上控制台选项（issue #32） |
