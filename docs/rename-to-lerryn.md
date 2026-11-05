# Lerryn 改名与升级

2026 年 11 月 5 日，产品统一命名为 **Lerryn**，归入 Niltavi 产品线。原有波形 Logo 保留，顶面改为溪水蓝绿 `#428c9a`，侧面改为深青 `#245761`。本次同步更新代码、接口契约、CLI、SDK、Agent Skills、部署文件、移动端标识和文档。

## 已有部署

这是需要停机协调的名称迁移。先备份数据库、对象存储和主密钥文件，停止所有 server、runner 与正在运行的任务，再一起升级。旧二进制、运行时镜像与新版本不能混用。

- 命令使用 `lerryn`，配置示例与任务清单使用 `lerryn.yaml`；原有任务目录里的清单也需要改名。
- 环境变量统一使用 `LERRYN_` 前缀，包括服务端配置、CLI 配置、测试配置及任务读取的参数、输入与工具地址。任务脚本中的旧变量名与 Node SDK 的 import 路径需要同步调整，新 Node 包名为 `@lerryn/sbx`。
- 默认数据目录为 `/var/lib/lerryn`，默认 SQLite 文件为 `/var/lib/lerryn/lerryn.db`。已有部署可以在配置中继续显式指定原数据库、存储、runner 数据目录及主密钥路径；不要直接用新默认值启动，否则会打开新的数据库。移动文件时也要更新容器卷挂载与文件权限。
- Compose 服务、网络、卷和默认数据库名称均已改名。使用已有部署时，在启动前将卷和连接信息显式指向原有资源，或按备份恢复流程迁移；不要让 Compose 自动创建空卷后当作原部署使用。
- CLI 配置默认位于 `$XDG_CONFIG_HOME/lerryn/cli.json` 或 `~/.config/lerryn/cli.json`。可移动原配置，或用 `LERRYN_CLI_CONFIG` 指向原配置文件。Agent 客户端需要更新 MCP 启动命令并安装 `lerryn-*` Skills。
- 会话 Cookie、浏览器本地设置键和 OIDC 状态标识均已改名，浏览器需要重新登录。OIDC 的 HTTP 回调路径未变；若部署域名改变，还需调整身份提供商配置。
- 错误类型使用 `urn:lerryn:error:*`，通知请求头使用 `X-Lerryn-*`，任务导出格式标记使用 `lerryn.task-bundle`。外部调用方、Webhook 接收方和已导出的任务包需要同步更新。任务包仅需修改顶层 `format` 字段，结构未变。
- 移动端显示名与 URL scheme 为 `Lerryn`、`lerryn://`，应用 ID 为 `ai.lerryn.mobile`。这是新的应用标识；既有安装需要安装新应用并重新配对。

数据库表结构、迁移版本、备份与整库导出的内部格式、依赖环境缓存哈希和对象内容未改变，改名前的备份可以继续恢复。凭证加密的四个历史域分隔标签、PostgreSQL 的两个锁命名空间以及 `wl_*` 令牌格式保持原值，避免使已有密文、密钥 ID、令牌或数据库锁失效。这些是兼容格式，不作为当前品牌使用。凭证兼容性测试使用改名前生成的固定密文，检查解密、密钥派生和轮换。

## 外部发布

代码中的 Go module、GitHub 链接和镜像发布配置以 `github.com/niltavi/lerryn`、`ghcr.io/niltavi/lerryn` 为目标。源码已迁移到独立的 `niltavi-lerryn` 目录，使用全新 Git 历史，同时维护 GitHub 与 Gitea 的 `niltavi/lerryn` 仓库，详见[仓库搬迁说明](repository-migration.md)。新镜像仍需维护者按发布流程构建与发布，已有镜像标签不会自动迁移。

Claude Design 的远端画布尚未同步这次名称与 Logo 变化。`design/brand/source/original-reference.webp` 保留改名前的字标与颜色，只作来源存档，不用于产品界面。当前资产以 `design/brand/logo/` 为准。
