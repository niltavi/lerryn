# 控制台和手机的端到端测试

两套测试都对一个真实的 `lerryn all-in-one` 运行：用仓库代码构建二进制，临时目录里的 SQLite、本地对象存储和内置执行节点，任务在本机 Docker 的容器中真实执行。测试结束时停止进程，删除容器和临时目录。两套测试都不在 `make check` 中。

## Web：`make e2e-web`

需要 Docker 和 Chromium（`make e2e-web` 会先运行 `playwright install chromium`，已安装时不下载）。用时约 1.5 分钟，其中准备数据约 1 分钟。

`e2e/global-setup.ts` 构建并启动服务，经 API 准备数据（`e2e/support/seed.ts`）：注册 alice 并建访问令牌，像 Agent 一样提交任务并运行，包括三个阶段带 Agent 面板的 `daily-report`（v2，已激活，有定时触发器）、没有阶段的 `plain-echo`、在第二个阶段失败的 `broken-import`、输出 10 万行的 `big-log`、超出每次运行日志上限的 `flood-log`、持续输出的 `ticker`；另有一个待填写的凭证请求、一个通知渠道，以及配置文件建立的管理员。最后让 `old-report` 的第一次运行过期：停掉服务，把它的结束时间改到 10 天前（服务以 `retention.logs_days: 7` 启动），重新启动并等主节点清理它的日志，这一步约 40 秒。

| 文件 | 覆盖 |
| --- | --- |
| `a03-observe.spec.ts` | A03：概览计数与接口一致，从概览进入任务和运行，看到状态、耗时、触发来源（时间线里有客户端名）、版本、结果、Agent 面板和日志；失败的运行说明阶段和原因；运行列表按失败筛选 |
| `a04-stages.spec.ts` | A04：运行中的三个阶段按顺序依次进行中、完成，不刷新页面；日志标出每个阶段的开始，每行带阶段；点阶段时日志滚到它的第一行；没有阶段的运行不画阶段，也没有百分比 |
| `a06-sensitive.spec.ts` | A06 的控制台部分：自己建一个打印凭证的任务，在填写页填入凭证后测试运行；运行页（结果、日志、stderr、时间线）、下载的日志、任务的凭证详情、凭证设置页、运行列表和概览的文字与 DOM，以及页面收到的接口响应中都没有原值和它的几种编码 |
| `a13-logs.spec.ts` | A13：10 万行日志只渲染一屏的行（虚拟滚动），分段读取更早的行并且行号连续，搜索，跳到末尾；运行中跟随最新、上滑暂停、跳到末尾后恢复；连接断开时显示重新连接，恢复后带 `Last-Event-ID` 续读，不缺行也不重复；超出上限时说明丢弃的行数；日志被保留策略清理后显示说明，不再请求日志 |
| `a17-pairing.spec.ts` | A17 的 Web 部分：配对页显示二维码和配对码，手机换取会话后自动出现在设备列表中，配对码只能用一次，移除后手机会话失效 |
| `ai-configuration.spec.ts` | 375 px 下通过真实控制台新建、编辑和移除个人 AI 配置，检查表单可访问性、Key 不回显，以及更换地址需要重新输入 Key |
| `artifact-preview.spec.ts` | 真实产物的文本截断、中文及 emoji JSON 格式化、HTML 只提供下载、普通下载保持完整内容 |
| `settings-pagination.spec.ts` | 令牌未读完显示部分可用数，读取第 51 条及撤销后显示精确值，中英与 375px 核对；第 201 条待填请求可继续读取并填写，侧栏部分计数与刷新；第 51 条旧失败通知可从发送记录继续加载。令牌和凭证场景使用独立用户，通知只发给测试自己的 loopback 接收器 |
| `runner-management.spec.ts` | 独立实例中创建注册令牌，真实响应延迟时重复 Enter 和 Escape 不丢令牌，令牌只能注册一次；200 条历史停用节点之后的当前节点可继续加载并停用，统计范围随分页更新，375 px 下没有横向滚动 |
| `smoke.spec.ts` | 未配置 SSO 时的登录页、登录后回到原页面；个人设置和管理页面；浅色、深色和跟随系统；375 px 宽度下没有横向滚动；主要页面在两种外观下的 axe 扫描（WCAG 2.1 AA，serious 和 critical 即失败） |

测试进程以 `notify.allowed_networks: 127.0.0.1/32` 允许通知历史用例访问本机临时接收器，此配置只在端到端测试启动环境中设置。

写测试时可以先启动一个准备好数据的服务，反复运行而不必每次准备数据：

```sh
make web
node apps/web/e2e/support/hold.ts            # 打印 LERRYN_E2E_STATE=...，Ctrl-C 停止并清理
LERRYN_E2E_STATE=... pnpm --filter @lerryn/web exec playwright test a13
```

其他变量：`LERRYN_E2E_BINARY` 用已构建的二进制，`LERRYN_E2E_KEEP=1` 保留临时目录和服务日志，`LERRYN_TEST_MOUNT_ROOT` 和 `LERRYN_TEST_PYTHON_IMAGE` 与 Go 集成测试相同。失败时的截图和 trace 在 `apps/web/e2e/.results/`。

## 手机：`make e2e-mobile`

需要 Docker、一个已启动且装有 Expo Go 的 iOS 模拟器（Xcode 自带的 `swift` 用于识别截图中的文字）。`LERRYN_E2E_SIMULATOR` 指定模拟器的名称或 UDID。

`apps/mobile/e2e/flow.ts` 用同样的数据启动服务，服务前面放一个记录请求的代理，部署地址指向代理；再启动 Metro，用 Expo Go 打开配对链接。配对页出现后，在模拟器上点一次「登录」：链接可能来自任何地方，App 会先让人确认，测试保留这一步。之后全部经深链接进行：概览、任务、运行详情（状态、阶段、日志、结果）、失败的运行、10 万行日志的末尾、日志已被保留策略清理的运行、运行中的运行。每一屏截图后用 macOS Vision 识别文字并检查，截图在 `apps/mobile/e2e/.results/`。最后检查代理记下的手机请求：读过概览、任务、运行、日志和日志流，除了配对没有任何写请求。

只想手动看 App 时，`node apps/mobile/e2e/serve.ts` 启动同样的服务并打印配对链接，Ctrl-C 时列出手机发出的请求，有写请求时以非零状态退出。
