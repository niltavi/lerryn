# Lerryn 首个试用版人工审查清单

这份清单写给发布前做人工审查的人。范围来自 `AGENTS.md` 的"需要人工审查的改动"和实施规划"首个企业试用版"的发布条件：权限、会话和单点登录、凭证、注入与脱敏、容器加固、runner 认证、通知出口，以及契约、迁移和第三方依赖。

每一项写明看哪些文件、检查什么、哪些测试已经覆盖。测试覆盖不等于审查通过：审查要看的是测试没有想到的路径。设计依据见技术设计，括号里是章节号。

审查时发现的问题记在对应条目下，修复后注明提交。全部条目勾选后，更新实施规划"首个企业试用版"表格中人工审查一行。

## 审查记录（0.9.0）

2026年11月3日，基于 main 的 `0b75a37`。维护者授权由 AI 会话完成全部审查和修复：按节分五组逐项对照代码（不只看测试），给出通过、问题或说明不准确三种结论，问题修复后补测试，再在目标环境上重跑全部检查（记录见[验收矩阵](acceptance-matrix.md)第 6 节）。全部条目通过，没有阻塞发布的问题。下表是发现并已修复的问题，修复都在这次的发布准备改动中。

| 级别 | 位置 | 问题 | 修复 |
| --- | --- | --- | --- |
| 中 | 7 runner 认证 | 持有注册令牌（固定令牌可反复使用）的人可以用已有的 runner ID 再次注册，顶替正在工作的 runner，拿到它名下准备中运行的凭证并伪造结果 | 已存在的 ID 只有在安静下来（超过一个租约周期没有心跳，也没有进行中的运行）后才换发凭证，否则 409 `runner_id_in_use`；内嵌 runner 的 ID 一律拒绝；换取在一个事务里完成。`service/runners_takeover_test.go` |
| 中 | 4 脱敏 | 阶段消息、结果摘要、工具调用的参数摘要和错误先截断再脱敏，凭证正好落在截断处时前半段会存进事件 | 先脱敏再截断；SDK 在容器内截断的文字（以"…"结尾）按被截断处理。`runner/redact_events_test.go` |
| 中 | 1 权限、10 契约 | `adminCreateUser`、`adminUpdateUser`、`adminResetPassword` 接受管理员的访问令牌，泄露的令牌可以重置管理员密码或新建管理员，绕过"令牌不能生成新令牌" | 三个操作改为只接受会话（契约变更）。`httpapi/m4_test.go`、`mcp/contract_test.go` |
| 中 | 2 会话 | 反向代理后面所有客户端共用代理的地址，10 次失败登录就锁住所有人（包括管理员）；审计里记的也是代理地址 | 新增 `server.trusted_proxies`，来自可信网段的请求按 `X-Forwarded-For` 取地址；多节点参考部署固定后端网段并信任它。`auth/clientip_test.go`、`httpapi/proxies_test.go` |
| 中 | 2 会话 | 密码哈希（Argon2id，19 MiB）没有并发上限，限速先查后记，并发请求能绕过次数限制，几百个并发登录就能耗尽内存 | 全局信号量限制同时进行的哈希；尝试先计数再校验，成功后退回。`auth/limits_test.go`、`service/security_test.go` |
| 中低 | 2 会话 | 注册没有限速 | 按地址每分钟 5 次 |
| 低 | 2 会话 | PostgreSQL 上并发注册第一个账号会出现两个管理员 | 方言层的 advisory 锁（`LockAccounts`），注册、初始管理员和 OIDC 首次登录共用 |
| 低 | 2 会话 | 重置或修改密码后未使用的配对码仍然有效 | 结束会话时一并删除 |
| 低中 | 1 权限 | 已打开的 SSE（日志跟随、事件流）在登出、令牌撤销、账号停用后继续推送 | 流定期重新校验会话或令牌，失效即结束；事件流的可见性缓存从 30 秒降到 10 秒 |
| 低 | 10 契约 | 幂等中间件在范围检查之前登记幂等键，被拒的请求能占用键，重放能绕过范围检查 | 先检查范围再登记 |
| 低中 | 6 容器 | 已存在的 `lerryn-tasks` 网络不检查选项，手工建的、开着容器互通的网络会被直接使用 | 检查驱动为 bridge 且 `enable_icc=false`，否则报加固错误 |
| 低 | 6 容器 | `seccomp` 为 `profile=unconfined` 的 daemon 被当作启用了 seccomp | 视为未启用 |
| 低 | 4 注入 | runner 领取凭证时没有检查租约是否过期 | 加上租约检查 |
| 低 | 5 sbx-exec | 为写凭证文件设置的 `umask 077` 带进了任务进程，runner 不是任务用户时读不到事件文件（目标环境上 `TestIntegrationToolCalls` 因此失败） | 执行任务前恢复原来的 umask。`TestSBXExecRestoresUmask` |
| 低 | 8 通知 | 没有拒绝 `::/96`、`::ffff:0:0:0/96` 和 Teredo `2001::/32` | 加入拒绝列表 |
| 低 | 7 runner | 非 HTTPS 的告警只比较小写的 `http://` 前缀 | 解析 URL 判断协议 |
| 低 | 6 容器 | `runner.data_dir: /` 会让挂载根目录的检查失效 | 拒绝 `/` 和系统目录 |
| 低 | 3 填写页 | 关闭密码登录后，普通用户在填写页登录只看到笼统的错误 | 说明原因，引导用单点登录 |
| 加固 | 13 其他 | 日志下载没有 CSP `sandbox` | 加上，与产物下载一致 |
| 加固 | 10 契约 | 幂等操作的集合没有固定，以后给返回明文令牌的操作加幂等键会把令牌存 24 小时 | 测试固定这五个操作 |
| 整理 | 12 依赖 | `go-oidc`、`oauth2`、`go-jose` 直接使用却标为 indirect | `go mod tidy`，版本不变 |

审查基线之后合入 main 的 #31（控制台里的凭证填写对话框，新接口 `fillCredentialRequest`）按第 3 节同样检查过：契约只接受会话，service 层也拒绝访问令牌，cookie 写请求经过 Origin 检查，响应不回显提交的值，用完的值被清除，不在 MCP 中暴露，也不是幂等操作。

另外两处是目标环境上发现的开发环境问题，一并修复：runner 不是任务用户时，Node 依赖目录改权限失败（目录本来就可读时接受）；SDK 的测试继承了开发机的 HTTP 代理变量。

## 1 权限检查

文件：

- `server/internal/service/authz.go`：`Authorizer.Require`，角色和动作的对应
- `server/internal/service/*.go`：每个公开方法开头的权限检查
- `server/internal/auth/principal.go`：`Principal`、令牌范围
- `server/internal/httpapi/authn.go`、`server/internal/httpapi/operations.go`：认证、范围检查、只接受会话的操作
- `server/internal/mcp/forward.go`：MCP 工具调用在进程内转成 HTTP 请求

检查：

- [x] 每个读写项目资源的 service 方法都调用了 `Require`，且动作与操作相符（读、写、运行、管理成员）。没有一处在接口层（`httpapi`、`mcp`）做权限判断。
- [x] 没有查看权限的资源一律返回 `not_found`，包括按 ID 读取、列表过滤、差异、日志、产物、事件和 SSE。错误信息不泄露资源是否存在。
- [x] 列表查询在数据库层按成员关系过滤，而不是取出后再过滤（分页游标不会越过别人的数据）。
- [x] 平台管理员没有隐含的项目权限（`authz.go` 的注释是这样设计的），管理接口只做账号管理。
- [x] 令牌范围：每个操作需要的范围由 `x-operation-kind` 决定（`read`、`write`、`execute`），只读令牌不能写。手机配对的会话是只读的（`ReadOnly`），所有写操作返回 `insufficient_scope`。
- [x] 访问令牌代表成员本人，不能再创建令牌；成员被移出项目、停用后，会话和令牌立即失效。
- [x] MCP 转发保留调用者的身份和范围，不会以服务端身份执行。`x-mcp.expose: false` 只表示不注册为单独的工具，这些操作仍可经 `api_call` 调用，能否成功由令牌范围和 service 层的权限检查决定。
- [x] 凭证只能由所有者读取元数据、撤销、授权；别人的凭证不能授权给自己的任务（`server/internal/service/credentials.go`）。
- [x] 工具只在登记它的项目内可见，任务引用别的项目的工具时版本校验不通过（`server/internal/service/tools.go`）。

已有测试：`server/internal/service/authz_test.go`、`projects_test.go`、`tools_test.go`、`credentials_test.go`，`tests/acceptance/access_test.go`（A17、A18），`server/cmd/lerryn/m4_test.go`。

## 2 会话、密码与单点登录

文件：

- `server/internal/auth/password.go`：密码哈希、`VerifyDummy`（防止按耗时判断账号是否存在）
- `server/internal/auth/token.go`：令牌生成、哈希、显示前缀
- `server/internal/auth/ratelimit.go`：登录限速
- `server/internal/auth/oidc.go`、`server/internal/service/oidc.go`：OIDC 登录
- `server/internal/service/accounts.go`：注册、登录、会话、配对码、初始管理员
- `server/internal/httpapi/sessions.go`、`server/internal/httpapi/authn.go`：会话 cookie、Origin 检查
- `server/internal/console/console.go`：控制台的安全响应头

检查：

- [x] 密码哈希参数合理，比较是常量时间；账号不存在时也走一次哈希。
- [x] 会话令牌和访问令牌只以哈希保存，数据库里没有明文；令牌有足够的随机性。
- [x] 会话 cookie 带 `HttpOnly`、`Secure`（HTTPS 时）、`SameSite`；cookie 认证的写请求检查 `Origin` 等于 `server.public_url`。
- [x] 登录、注册、配对码兑换都有限速；限速是每个副本各自计数的，多副本时总量为副本数倍，确认可以接受。
- [x] 开放注册关闭后，注册接口拒绝；第一个账号成为管理员的逻辑在并发注册时不会产生两个管理员。
- [x] `auth.initial_admin` 只在没有任何账号时生效，密码从文件读取，不进日志。
- [x] OIDC：`state`、`nonce` 和 PKCE 都校验；回调地址固定为 `server.public_url` 加 `/api/v1/auth/oidc/callback`；`returnTo` 只接受站内路径，不能被用来跳转到外部地址。
- [x] OIDC 首次登录建立账号时按 `issuer` 加 `sub` 绑定，不按邮箱或用户名合并已有账号；用户名冲突时加后缀。
- [x] 提供方认证通过的人都会得到账号（不受开放注册影响），这一点写在了部署文档第 11 节；确认这是期望的行为。
- [x] 关闭本地密码登录后，普通用户不能用密码登录，管理员仍可以。
- [x] 配对码一次有效、有过期时间，换到的会话只读。
- [x] 停用账号、重置密码时，相关会话立即失效。

已有测试：`server/internal/auth/auth_test.go`、`oidc_test.go`，`server/internal/service/accounts_test.go`、`oidc_test.go`、`profile_test.go`，`server/cmd/lerryn/m4_test.go`（A18）。

## 3 凭证加密、指纹与填写页

文件：

- `server/internal/secrets/secrets.go`：信封加密（每个值一个数据密钥，AAD 绑定）
- `server/internal/secrets/keyfile.go`：主密钥文件的读取、生成、轮换
- `server/internal/secrets/fingerprint.go`：带密钥的指纹
- `server/internal/secrets/forms.go`：参与指纹的变换形式
- `server/internal/service/credentials.go`、`credentials_fill.go`、`credentials_manifest.go`、`channel_sealer.go`：凭证的生命周期、填写请求、通知渠道的密钥
- `server/internal/service/credentials_plaintext.go`：提交中出现凭证值时拒绝
- `server/internal/web/fill.go`、`server/internal/web/page.go`：服务端渲染的填写页
- `server/internal/ops/backup.go`、`export.go`：备份和导出不含明文和主密钥

检查：

- [x] 加密使用 AEAD，nonce 随机且不重复；AAD 包含凭证和版本的标识，密文不能被挪到别的记录上解密。
- [x] 主密钥文件权限放宽时告警；主密钥不进日志、不进备份；轮换后旧密钥只用于解包，`rewrap` 可以重复执行。
- [x] 指纹用从主密钥派生的独立密钥计算（HMAC），短于下限的形式不计算指纹；数据库里的指纹不能被用来离线猜测短值。
- [x] 明文检测只比较调用者有权使用的凭证（自己的和绑定到这个任务的），拒绝信息只写引用名，不暴露别人的凭证或所有者。
- [x] 填写页：只有请求的目标用户能填写，别人打开得到"不属于当前账号"；提交只接受会话（不接受访问令牌），检查 Origin；有 CSRF 防护。
- [x] 填写页的响应头：`Content-Security-Policy` 带 nonce、`X-Frame-Options: DENY`、`Cache-Control: no-store`、`Referrer-Policy`；表单值不会回显到页面上。
- [x] 填写完成后，值不出现在任何 API 响应、MCP 返回、日志、审计详情和事件中。
- [x] 撤销后的凭证不再注入，已排队的运行在准备阶段失败。

已有测试：`server/internal/secrets/secrets_test.go`、`fingerprint_test.go`，`server/internal/service/credentials_test.go`、`credentials_plaintext_test.go`、`channel_sealer_test.go`，`server/internal/web/fill_test.go`，`tests/acceptance/credentials_test.go`（A05、A06、A07），`tests/acceptance/m6_test.go`（A15 备份中没有明文）。

## 4 注入与脱敏

文件：

- `server/internal/service/credentials_runner.go`：server 把运行的凭证交给 runner
- `server/internal/runnerhttp/server.go`、`client.go`、`wire.go`：凭证经 runner 协议传输
- `server/internal/runner/credentials.go`：runner 取凭证、编码成 `sbx-exec` 的输入、用完清除
- `server/internal/runner/redact.go`：日志、阶段消息、结果和事件的遮盖
- `server/internal/runner/logs.go`、`events.go`、`markers.go`：遮盖在上传前生效
- `server/internal/logging/logging.go`：server 和 runner 自己的日志

检查：

- [x] 凭证只在运行准备阶段、只交给领取了这个运行并持有有效租约的 runner；租约过期或运行已结束后不能再取。准备阶段内重试可以取多次（runner 最多重试 3 次）。
- [x] 凭证经容器的 stdin 交给 `sbx-exec`，不进入容器的环境变量配置、命令行、标签和 Docker 的 inspect 输出。
- [x] 交给容器的 stdin 载荷在容器读取后清零；值本身作为字符串留在 runner 内存中直到运行结束、被垃圾回收，Go 无法主动清零。runner 的日志里没有值。
- [x] 遮盖覆盖原值和常见变换（base64、URL 编码、JSON 转义、Basic 头），也覆盖跨两次输出被拆开的值；遮盖发生在上传之前，数据库和对象存储里没有原值。
- [x] 太短的值不遮盖（会误伤正常输出）的下限合理，并在文档中说明。
- [x] 结构化日志字段里没有凭证、任务日志内容和令牌（`AGENTS.md` 的安全规则）。

已有测试：`server/internal/runner/redact_test.go`、`credentials_integration_test.go`、`runner_test.go`，`server/internal/runnerhttp/secrets_test.go`，`tests/acceptance/credentials_test.go`（A06），`apps/web/e2e/a06-sensitive.spec.ts`（A06 的控制台部分）。

## 5 sbx-exec 与 sbx SDK

文件：

- `runtimes/sdk/sh/sbx-exec`：每个任务容器的入口，读取凭证后 exec 任务入口
- `runtimes/sdk/sh/sbx`、`runtimes/sdk/python/`、`runtimes/sdk/node/`：上报阶段和结果、`sbx mcp call`
- `runtimes/sdk/sdk.go`：嵌入和挂载
- `server/internal/runner/sdk.go`、`tools.go`：SDK 和脚本工具的只读挂载

检查：

- [x] `sbx-exec` 的输入解析：名称只允许大写字母、数字和下划线，长度有上限，单个值有上限；格式错误时以 125 退出，任务入口不启动。
- [x] 写到 `/run/secrets` 的文件权限为 0600、目录为 0700，写入时 `umask 077`，执行任务前恢复原来的 umask；值不会被打印。
- [x] `exec` 之后任务进程拿不到 stdin 中剩余的数据。
- [x] `sbx mcp call` 只连接 `LERRYN_TOOLS` 中登记的地址，参数摘要在记录前遮盖；令牌不进入 `tool.call` 事件。
- [x] SDK 和脚本工具以只读方式挂载，任务不能改写。

已有测试：`server/internal/runner/sbxexec_test.go`、`sdk_test.go`、`sdk_mcp_test.go`、`tools_integration_test.go`，`tests/acceptance/tools_test.go`（A14）。

## 6 容器加固

文件：

- `server/internal/executor/docker/config.go`：容器和主机配置
- `server/internal/executor/docker/docker.go`：daemon 检查、镜像检查、挂载根目录检查、任务网络
- `server/internal/executor/supervise.go`：取消、超时和进程树清理
- `server/internal/runner/dirs.go`、`run.go`：运行目录和挂载

检查：

- [x] 以 65532 运行，`CapDrop: ALL`，`no-new-privileges`，只读根文件系统，`/tmp` 为 `noexec` 的 tmpfs，进程数、内存、CPU 上限都设置了。`/sbx/work`、`/sbx/out`、`/sbx/events` 是可写的绑定挂载，没有 `noexec`，所以 `/tmp` 的 `noexec` 不能阻止任务执行自己写出的文件。
- [x] 不挂载 Docker socket、宿主机数据目录和 `/etc`；挂载源只能在允许的根目录下（`checkMountRoots`）。
- [x] 拒绝带 `VOLUME` 的镜像、不满足加固前提的 daemon（例如没有 pids 限制），并在启动时报出原因。
- [x] `none` 网络完全没有出口；`full` 网络使用 `enable_icc=false` 的专用网络。任务仍能访问宿主机网关和发布的端口，这一点在技术设计第 20 节和发布说明中写明。
- [x] 取消和超时会结束整个进程树，容器确认停止前状态保持取消中；确认不了时 runner 放弃这个运行，租约过期后 server 记为中断，容器留到 runner 重启时清理。遗留容器只清理带 Lerryn 标签、属于这个 runner 的。
- [x] 默认 seccomp 和 AppArmor 配置没有被关闭。

已有测试：`server/internal/executor/docker/docker_integration_test.go`（`TestHardening`、`TestNetworkNone`、`TestNetworkFull`、`TestPidsLimitContainsForkBomb`、`TestMounts`、`TestRejectsImageWithVolumes` 等），`config_test.go`，`tests/acceptance/multinode_test.go`（A14 越界部分）。`enable_icc` 的隔离只在原生 dockerd 上能验证，见[目标环境验证手册](target-env-runbook.md)。

## 7 runner 认证

文件：

- `server/internal/runnerhttp/server.go`：注册、领取、心跳、文件、上报，`authed` 中间件
- `server/internal/service/runners.go`：注册令牌、runner 凭证、协议版本
- `server/internal/runner/id.go`：runner 凭证在本地的保存
- `server/internal/config/`：`server.runner_registration_token_file`、`runner.registration_token_file`

检查：

- [x] runner 凭证只以哈希保存；注册令牌换取凭证后，凭证存在 runner 数据目录的 `runner-credential`，文件权限 0600。
- [x] 每个运行相关的请求都检查运行确实由这个 runner 领取且仍在进行中，一个 runner 不能读写别的 runner 的运行、文件和凭证。除凭证外，这些检查不比较租约到期时间：租约过期但还没被主节点回收的运行，原 runner 仍可上报，这是有意的。
- [x] 注册令牌文件与管理员生成的令牌并用；注册令牌泄露的影响是"可以注册新的 runner 并领取任务"（不能顶替正在工作的 runner，见审查记录），部署文档第 11 节写明了它要像 root 凭证一样保管。
- [x] 协议版本检查拒绝不支持的版本，并给出明确的错误。
- [x] `runner.server_url` 不是 HTTPS 时启动告警。

已有测试：`server/internal/runnerhttp/runnerhttp_test.go`、`server/internal/service/runners_test.go`、`coordinator_test.go`，`server/cmd/lerryn/m5_integration_test.go`（B02）。

## 8 通知出口（SSRF 防护）

文件：

- `server/internal/notify/guard.go`：允许的地址判断，在建立连接时按解析后的地址检查
- `server/internal/notify/sender.go`：发送、重定向、超时
- `server/internal/notify/secret.go`、`server/internal/service/notifications*.go`：签名和渠道密钥

检查：

- [x] 检查发生在连接时（`Dialer.Control`），而不是只在保存地址时，DNS 重绑定无法绕过。
- [x] 回环、私有、链路本地、CGNAT、组播、IPv4 映射的 IPv6 等地址都被拒绝；只有 `notify.allowed_networks` 放行的网段例外。
- [x] 不跟随重定向，或者重定向的目标同样经过检查。
- [x] 代理环境变量（`HTTP_PROXY`）不会让请求绕过检查。
- [x] 响应体只读有限长度，超时合理；载荷中没有凭证明文。

已有测试：`server/internal/notify/guard_test.go`、`format_test.go`，`server/internal/service/notifications_test.go`，`tests/acceptance/m6_test.go`（A16）。

## 9 webhook 触发器

文件：`server/internal/service/webhook.go`、`triggers.go`，`server/internal/httpapi/` 中的 webhook 处理。

- [x] 触发器令牌只以哈希保存，比较是常量时间；令牌在 URL 路径之外传递时不进入访问日志。
- [x] 幂等键在同一触发器内去重，换请求体返回冲突；按触发器限速。
- [x] 请求体大小有上限（默认 1 MiB，可在 1 KiB 到 10 MiB 之间配置）。请求体原样交给任务（`/sbx/in/body`），不做 schema 校验和凭证值检测；凭证值检测作用于触发器自己的固定参数。

## 10 契约

首个试用版之前没有发布过版本，所以审查整个 `api/openapi/`，以后只审查差异（第一个版本发布后加入 oasdiff，技术设计第 16.3 节）。

- [x] 每个操作都有 `x-operation-kind` 和 `x-mcp`；`x-operation-kind` 与操作的实际效果相符，决定了需要的令牌范围。
- [x] `x-mcp.expose: false` 的操作（管理、会话、设置类）经 `api_call` 调用时的行为符合预期：管理员的令牌能调用管理接口，只接受会话的操作拒绝令牌。
- [x] 响应 schema 里没有可能带出凭证值、令牌明文或密码哈希的字段；创建令牌、触发器的响应只在创建时返回一次明文。
- [x] 错误码（`common/schemas.yaml` 的 `ErrorCode`）与 service 的错误对应，没有权限时是 `not_found`。
- [x] `info.version` 为 0.9.0，`api/bundled/openapi.yaml` 和生成的代码一致（`make generate-check`）。

## 11 数据库迁移

`server/migrations/postgres/` 和 `server/migrations/sqlite/` 各 16 个文件，时间戳从 `20261102120000` 到 `20261108010000`，两边一一对应。

- [x] 两种方言的表、列、约束、索引一致；方言差异只在类型和语法上，唯一的例外是 `retry_of`、`rerun_of`、`cancel_requested_by` 的外键只在 PostgreSQL 上有（SQLite 迁移里说明了原因），保留策略删除前先清掉这两列，行为一致。
- [x] 凭证、令牌、会话相关的表只有哈希和密文列，没有明文列。
- [x] 唯一约束保证了去重的语义：同一计划分钟一条运行、幂等键。运行的领取不靠唯一约束，靠行锁（PostgreSQL 的 `FOR UPDATE SKIP LOCKED`）或单条条件更新（SQLite）。
- [x] 后加的迁移在已有数据上可以执行（例如给旧行补默认值），而不只是在空库上。
- [x] 回退策略：迁移文件里的 `Down` 只给测试用（`TestMigrationsRollBack`），二进制从不执行，在有数据的库上也不保证能执行；生产回退要恢复升级前的备份，写在部署文档第 8 节。

已有测试：`server/internal/store/` 的存储契约测试（`make test` 和 `make test-pg`），`server/cmd/lerryn/migratedb_integration_test.go`。

## 12 第三方依赖

完整清单和许可证见 `THIRD_PARTY_NOTICES`，规则见[发布流程](release.md)第 4 节。`make notices-check` 保证清单与依赖一致、许可证都在允许清单内。审查要看的是允许清单本身和下面的直接依赖。

直接依赖（Go，`go.mod`）：

| 模块 | 版本 | 许可证 |
| --- | --- | --- |
| `github.com/caarlos0/env/v11` | v11.4.1 | MIT |
| `github.com/containerd/errdefs` | v1.0.0 | Apache-2.0 |
| `github.com/coreos/go-oidc/v3` | v3.17.0 | Apache-2.0 |
| `github.com/go-jose/go-jose/v4` | v4.1.7 | Apache-2.0（测试用的 OIDC 提供方直接使用，二进制经 go-oidc 间接引入） |
| `github.com/getkin/kin-openapi` | v0.152.0 | MIT |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause |
| `github.com/jackc/pgx/v5` | v5.12.0 | MIT |
| `github.com/moby/moby/api` | v1.56.1 | Apache-2.0 |
| `github.com/moby/moby/client` | v0.6.2 | Apache-2.0 |
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 | Apache-2.0 与 MIT |
| `github.com/oapi-codegen/runtime` | v1.8.0 | Apache-2.0 |
| `github.com/pressly/goose/v3` | v3.29.0 | MIT |
| `github.com/prometheus/client_golang` | v1.25.0 | Apache-2.0 |
| `github.com/robfig/cron/v3` | v3.0.1 | MIT |
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.3 | Apache-2.0 |
| `github.com/spf13/cobra` | v1.10.2 | Apache-2.0 |
| `github.com/uptrace/bun`（含 pgdialect、sqlitedialect） | v1.3.0 | BSD-2-Clause |
| `go.yaml.in/yaml/v3` | v3.0.6 | Apache-2.0 与 MIT |
| `golang.org/x/crypto` | v0.58.0 | BSD-3-Clause |
| `golang.org/x/oauth2` | v0.37.0 | BSD-3-Clause |
| `golang.org/x/term` | v0.47.0 | BSD-3-Clause |
| `golang.org/x/text` | v0.43.0 | BSD-3-Clause |
| `modernc.org/sqlite` | v1.61.1 | BSD-3-Clause，SQLite 为公有领域 |

`go.mod` 的 `tool` 指令另有 oapi-codegen、goose、govulncheck，只在开发时使用，不进入二进制。

直接依赖（Web 控制台运行时，`apps/web` 和工作区包）：

| 包 | 版本 | 许可证 |
| --- | --- | --- |
| `@fontsource/ibm-plex-mono`、`@fontsource/sora` | 5.3.0 | 字体 OFL-1.1，包 MIT |
| `@heroui/react`、`@heroui/styles` | 3.2.6 | 许可证文件为 Apache-2.0，`package.json` 写的是 MIT |
| `@internationalized/date` | 3.12.4 | Apache-2.0 |
| `@react-aria/ssr` | 3.10.1 | Apache-2.0 |
| `@react-aria/utils` | 3.34.1 | Apache-2.0 |
| `@tanstack/react-query` | 5.105.2 | MIT |
| `@tanstack/react-router` | 1.170.46 | MIT |
| `@tanstack/react-virtual` | 3.14.25 | MIT |
| `lucide-react` | 1.58.0 | ISC（含源自 Feather 的 MIT 部分） |
| `react`、`react-dom` | 19.2.3 | MIT |
| `react-aria` | 3.53.1 | Apache-2.0 |
| `react-aria-components` | 1.22.1 | Apache-2.0 |
| `tailwindcss` | 4.3.3 | MIT |
| `openapi-fetch`（`@lerryn/api`） | 0.17.0 | MIT |
| `uqr`（`@lerryn/ui-web`） | 0.1.3 | MIT |

检查：

- [x] 允许清单（`scripts/notices.mjs` 的 `ALLOWED`）中的许可证都可以与 AGPL-3.0 组合分发。
- [x] `FILE_DECISIONS` 中不收录的文件（`go-digest` 的文档许可证、`modernc.org/memory` 的徽标署名）确实不进入二进制。
- [x] HeroUI 的许可证文件与 `package.json` 不一致，两种许可证都在允许清单内，通知中收录了许可证文件的全文；确认无需进一步处理。
- [x] `govulncheck`（`make vulncheck`）没有报告。
- [x] 依赖的版本都是固定的（`go.sum`、`pnpm-lock.yaml`），没有指向 fork 或私有源的 `replace`。

## 13 其他

- [x] 控制台和下载类响应的安全头：`server/internal/console/console.go`（CSP、`X-Frame-Options`），`server/internal/httpapi/run_history.go`（日志和产物下载的 CSP `sandbox`）。HSTS 交给反向代理。
- [x] 镜像：distroless、nonroot（65532），没有 shell；`Dockerfile` 的构建参数里没有密钥。
- [x] 备份文件权限 0640，导出的凭证只有密文（`server/internal/ops/`）。
