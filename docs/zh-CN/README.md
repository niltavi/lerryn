# Lerryn

[English](../en/README.md) · [语言选择](../README.md) · [仓库首页](../../README.md)

你把一件需要定期做的事交给 Codex 或 Claude，然后继续忙自己的事。等到回来查看时，却未必清楚任务有没有运行、执行得怎样，产出的文件又在哪里。

Lerryn 希望让这件事简单一些：说清楚你需要什么，让 AI 安排执行，再回来看看结果。哪里需要改进，就继续告诉它。代码、定时和配置交给 Agent 处理，省去拖拽节点、填写参数和翻找日志的麻烦。

Agent 通过 MCP 或 HTTP API 接入。Python、Node 和 Shell 任务在各自的容器中运行，按计划执行，也能由 Webhook 触发。每次运行都会留下记录，结果和文件收在一起。对话结束后，任务仍会继续执行；你可以在 Web 控制台查看和管理，也可以用手机查看进展。

Lerryn 运行在你自己的机器上，可以是一台 Mac mini，也可以是一个小型集群。密钥和数据留在你手中。

仓库协作规则见 [AGENTS.md](../../AGENTS.md)。

## 用户文档

| 文档 | 内容 |
| --- | --- |
| [快速入门](getting-started.md) | 选择部署方式、连接 Agent、提交并测试第一个任务 |
| [部署与运维](deploy.md) | Linux 与 Mac mini 安装、多节点部署、全部配置项、指标、备份、迁移、升级和公网访问 |
| [CLI 与 Agent 接入](cli.md) | 登录、配置、任务版本、运行、任务包、通用 API 命令、Skill 和 MCP stdio 代理 |
| [Tools 接入](tools.md) | 内置能力、MCP、HTTP API、CLI、Skill 与项目工具接入 |
| [日常运维](operations.md) | 健康检查、失败排查、备份安排、runner 维护与恢复 |

以上用户文档均提供中英文版本。产品界面目前使用中文，文档语言不会改变界面语言。

## 产品与工程资料（中文）

这些文档保留中文。阅读验收和发布记录时，按文中记录的日期与版本判断适用范围，区分 0.9.0 与未发布改动。

| 主题 | 文档 |
| --- | --- |
| 产品与架构 | [产品需求](agent-automation-sandbox-prd.md)、[技术设计](agent-automation-sandbox-tech-design.md)、[实施规划](agent-automation-sandbox-implementation-plan.md) |
| 设计 | [设计语言](design-system.md)、[组件开发](../../design/README.md) |
| 验证 | [验收矩阵](acceptance-matrix.md)、[容量结果](capacity.md)、[人工审查清单](review-checklist.md)、[目标环境验证手册](target-env-runbook.md) |
| CI 与镜像发布 | [Gitea Actions 配置](ci.md) |
| 发布 | [发布流程](release.md)、[0.9.0 发布说明](release-notes/0.9.0.md)、[未发布改动](release-notes/unreleased.md) |
| 迁移 | [改名与升级](rename-to-lerryn.md)、[仓库搬迁](repository-migration.md) |
| 技术验证 | [S1 双数据库](spikes/s1-dual-dialect-storage.md)、[S2 OpenAPI 生成链路](spikes/s2-openapi-codegen.md)、[S3 MCP 无状态模式](spikes/s3-mcp-stateless.md)、[S4 容器执行](spikes/s4-container-execution.md) |
| API 与 SDK | [API 约定](../../api/README.md)、[Python](../../runtimes/sdk/python/README.md)、[Node](../../runtimes/sdk/node/README.md)、[Shell](../../runtimes/sdk/sh/README.md) |

## 仓库目录

```
docs/
  README.md             语言选择与文档入口
  en/                   英文文档
  zh-CN/                简体中文文档、产品与技术设计、实施规划
    spikes/             技术验证记录
    release-notes/      发布说明
  assets/               共用图片与许可证文件
design/                设计语言的全部内容
  tokens.json          设计 token 的唯一来源
  brand/               Logo（SVG 母版、横向、正方形、圆形，以及 PNG）
  ui-native/           @lerryn/ui-native，React Native 组件库（HeroUI Native + Uniwind）
  gallery/             @lerryn/design-gallery，展示全部组件的 Expo App
  scripts/             token 与 Logo 的生成脚本
api/                   OpenAPI 3.1 契约：openapi/ 是源文件，bundled/ 是打包结果，约定见 api/README.md
server/                Go 服务端与 CLI：cmd/lerryn 是唯一入口，internal/ 按技术设计第 16.1 节分包
apps/web/              Web 控制台，构建后嵌入服务端二进制
apps/mobile/           只读的 Expo 移动端，扫码配对
packages/api/          @lerryn/api，由契约生成的 TypeScript 类型和客户端
packages/core/         Web 与移动端共用的数据查询、日志和展示模型
packages/ui-web/       Web 控制台组件
deploy/                部署文件：配置示例、单机和标准拓扑的 Compose 文件，说明见 docs/zh-CN/deploy.md
runtimes/sdk/          Python、Node、Shell 的 sbx 辅助库（Apache-2.0）
skills/                任务自动化、排错、凭证与运维的 Agent Skills（Apache-2.0）
tests/                 验收、Agent 评测和容量测试
.claude/skills/        本仓库开发用的项目级 Skill
```

`lerryn all-in-one` 在一个进程中提供 API、MCP、Web 控制台和内嵌 runner；`lerryn server` 与 `lerryn runner` 支持分开部署。单机使用 SQLite 和本地对象存储，多节点使用 PostgreSQL 和 S3 兼容存储。部署步骤见 [docs/zh-CN/deploy.md](deploy.md)。

## 常用命令

开发需要 Go 1.27、Node.js 24、pnpm 10.18.0 和 golangci-lint 2.x。在仓库根目录安装依赖、构建控制台和服务端，再运行合并前检查：

```bash
pnpm install --frozen-lockfile
make build
make check
```

构建结果为 `bin/lerryn`。`make help` 列出全部命令；组件库和主题生成命令见 [design/README.md](../../design/README.md)。开发服务器由维护者在本地启动。

## 官网介绍页

运行 `pnpm website:build` 和 `pnpm website:check` 构建并验证英文、中文官网，输出在 `website/dist/`。部署与域名配置见 [website/README.md](../../website/README.md)。

## 许可证

仓库整体采用 AGPL-3.0，见 [LICENSE](../../LICENSE)。`runtimes/sdk/` 和 `skills/` 采用 Apache-2.0，见各自目录下的 `LICENSE`。第三方许可证声明见 [THIRD_PARTY_NOTICES](../../THIRD_PARTY_NOTICES)。

<br />

<div>
<a href="for-agents.md">For agents</a>
<a href="https://niltavi.com/">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="../assets/niltavi-logo-dark.svg" />
  <img align="right" src="../assets/niltavi-logo-light.svg" width="72" height="26" alt="niltavi" />
</picture>
</a>
</div>
