# Lerryn

[English](../README.md)

Niltavi 产品线中的自动化沙箱，由外部 Agent 通过 API 和 MCP 操作，面向企业自部署。支持 Python、Node 和 Shell 任务、定时与 Webhook 触发、个人凭证、运行日志与产物；Web 控制台用于观察和管理，移动端提供只读查看。协作规则见 [AGENTS.md](../AGENTS.md)。

- 产品需求、技术设计、实施规划：`docs/`
- 改名与升级：[docs/rename-to-lerryn.md](../docs/rename-to-lerryn.md)
- 仓库搬迁：[docs/repository-migration.md](repository-migration.md)
- 官网源码：[website/README.md](../website/README.md)，目标域名 `lerryn.niltavi.com`
- 部署与配置：[docs/deploy.md](../docs/deploy.md)
- CLI 与 Agent 接入：[docs/cli.md](../docs/cli.md)
- 验收结果与已知缺口：[docs/acceptance-matrix.md](../docs/acceptance-matrix.md)
- 设计语言与组件库：`design/`，说明见 [docs/design-system.md](../docs/design-system.md)
- 设计系统页面（Claude Design）：https://claude.ai/artifact/FPPdharrE7VXPo5ZaJPmVq

## 目录

```
docs/
  agent-automation-sandbox-prd.md                  产品需求（PRD）
  agent-automation-sandbox-tech-design.md          技术设计
  agent-automation-sandbox-implementation-plan.md  实施规划
  design-system.md                                 设计语言：现状、用法与约束
  deploy.md                                        部署与运维：安装、配置参考、备份恢复、升级回退
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
deploy/                部署文件：配置示例、单机和标准拓扑的 Compose 文件，说明见 docs/deploy.md
runtimes/sdk/          Python、Node、Shell 的 sbx 辅助库（Apache-2.0）
skills/                任务自动化、排错、凭证与运维的 Agent Skills（Apache-2.0）
tests/                 验收、Agent 评测和容量测试
.claude/skills/        本仓库开发用的项目级 Skill
```

`lerryn all-in-one` 在一个进程中提供 API、MCP、Web 控制台和内嵌 runner；`lerryn server` 与 `lerryn runner` 支持分开部署。单机使用 SQLite 和本地对象存储，多节点使用 PostgreSQL 和 S3 兼容存储。部署步骤见 [docs/deploy.md](../docs/deploy.md)。

## 常用命令

```bash
pnpm install
```

```bash
pnpm design:typecheck
```

```bash
pnpm design:gallery:ios
```

其余命令（`design:tokens`、`design:logo`、`design:gallery`）见 [design/README.md](../design/README.md)。

服务端需要 Go 1.27 和 golangci-lint 2.x。合并前跑一遍全部检查：

```bash
make check
```

构建 `bin/lerryn`：

```bash
make build
```

`make help` 列出全部命令。

## 官网介绍页

运行 `pnpm website:build` 和 `pnpm website:check` 构建并验证英文、中文官网，输出在 `website/dist/`。部署与域名配置见 [website/README.md](../website/README.md)。

## 许可证

仓库整体采用 AGPL-3.0，见 [LICENSE](../LICENSE)。`runtimes/sdk/` 和 `skills/` 采用 Apache-2.0，见各自目录下的 `LICENSE`。
