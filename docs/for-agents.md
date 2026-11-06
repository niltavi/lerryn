# For agents

[English](en/for-agents.md) · [仓库首页](../README.md)

如果用户把这个链接交给你，从这里开始操作 Lerryn。根据用户的目标选择下面的指南，完成接入、执行和结果检查，再把结果与控制台链接交还给用户。

Lerryn 是自部署的自动化沙箱。你通过 MCP、HTTP API 或 CLI 维护 Python、Node 和 Shell 任务；平台负责调度、容器执行、凭证注入和运行记录。

## 从这里开始

1. 确认用户要连接已有实例，还是部署一个新实例。已有实例从[快速入门](getting-started.md)开始；新实例按[部署与运维](deploy.md)选择单机或多节点方案，完成首次管理员注册后再开放访问。
2. 按 [Tools 接入](tools.md)连接远程 MCP，或通过已登录的 `lerryn` CLI 使用 stdio 代理。凭证由用户在本地客户端或专用填写页提交。
3. 调用 `discover`，读取当前身份、项目、运行时、能力和 Skill 版本。用 `lerryn skills install` 安装与实例版本匹配的操作 Skill；其他客户端可用 `--dir <目录>` 指定位置。MCP 客户端也可以读取 `lerryn://skills/<name>/SKILL.md`。
4. 按[快速入门](getting-started.md)验证一个无网络、无凭证的测试任务，再按用户的目标继续。保留任务 ID、版本、运行 ID 和响应中的 `links.console`，方便之后接着处理。

## 操作目录

| 用户的目标 | 阅读入口 |
| --- | --- |
| 安装、首次注册、公网访问、单机或多节点部署 | [部署与运维](deploy.md)、[运维 Skill](../skills/lerryn-operate/SKILL.md) |
| 登录、访问令牌、MCP、HTTP API、CLI 和能力发现 | [Tools 接入](tools.md)、[CLI 与 Agent 接入](cli.md) |
| 创建、修改、预览、测试和激活任务 | [自动化任务 Skill](../skills/lerryn-automation/SKILL.md) |
| 编写任务清单、参数、依赖和执行限制 | [清单说明](../skills/lerryn-automation/references/manifest.md)、[完整字段](../skills/lerryn-automation/references/manifest-fields.md)、[任务示例](../skills/lerryn-automation/references/examples.md) |
| 设置定时与 Webhook、确认时区和下次触发时间 | [触发器说明](../skills/lerryn-automation/references/triggers.md) |
| 查看运行、阶段、日志、业务结果和产物；取消或关联重跑 | [运行排查 Skill](../skills/lerryn-troubleshoot/SKILL.md)、[CLI 与 Agent 接入](cli.md) |
| 保存、更新、授权或撤销 API Key、Token、Cookie 等凭证 | [凭证 Skill](../skills/lerryn-credentials/SKILL.md) |
| 上报阶段和结果、保存文件、跨运行保留状态 | [sbx 使用说明](../skills/lerryn-automation/references/sbx.md)、[Python SDK](../runtimes/sdk/python/README.md)、[Node SDK](../runtimes/sdk/node/README.md)、[Shell SDK](../runtimes/sdk/sh/README.md) |
| 接入外部 MCP、HTTP API 或脚本工具 | [Tools 接入](tools.md) |
| 管理账号、项目、成员、权限和其他系统资源 | [Tools 接入](tools.md)中的 `api_describe` / `api_call`、[CLI 通用 API 命令](cli.md)、[完整接口契约](../api/bundled/openapi.yaml) |
| 排查未触发、失败、超时、中断或结果不符合预期 | [运行排查 Skill](../skills/lerryn-troubleshoot/SKILL.md)、[错误码](../skills/lerryn-troubleshoot/references/error-codes.md) |
| 备份、恢复、升级、回退、迁移、扩容和健康检查 | [运维 Skill](../skills/lerryn-operate/SKILL.md)、[运维操作手册](../skills/lerryn-operate/references/runbook.md)、[日常运维](operations.md) |
| 导出或导入任务及整个部署的数据 | [CLI 与 Agent 接入](cli.md)、[部署与运维](deploy.md) |
| 修改 Lerryn 源码、验证或发布 | [仓库协作规则](../AGENTS.md)、[实施规划](agent-automation-sandbox-implementation-plan.md)、[发布流程](release.md) |

## 执行与交付

操作前读取对应 Skill。以实例的 `discover` 和 `api_describe` 返回为准，确认参数、权限与运行时；不凭记忆猜接口。CLI 可用 `lerryn api <operationId> --describe` 查看操作定义。

新建或修改任务时，先预览提交，再测试并检查业务结果，通过后才激活。修改已有任务前读取最新版本，发生版本冲突时合并改动。定时触发器需要明确的 IANA 时区，并核对接下来的触发时间。具体步骤见自动化任务 Skill。

凭证通过平台填写页处理，Agent 只使用引用和状态。测试运行也会执行真实代码；发送消息、写入外部数据等副作用，以及生产实例的停机、恢复、删除操作，按对应 Skill 和用户已有授权处理。

交付时说明完成了什么、运行结果和产物在哪里，附上控制台链接。`completed` 只表示进程正常退出，业务是否成功还要检查 `result` 和 `error`；缺少结果时如实说明。
