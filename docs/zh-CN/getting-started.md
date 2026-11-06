# Lerryn 快速入门

[English](../en/getting-started.md) · [文档目录](README.md)

Lerryn 由外部 Agent 通过 API、MCP 或 CLI 创建和维护任务。控制台用于查看任务、日志、结果与产物，也提供账号和凭证管理。本文先连接一个可用实例，再用一个不访问网络、不需要凭证的 Python 任务验证提交、执行和结果读取。

## 准备实例与客户端

已有实例时，向维护者取得访问地址，登录控制台；本地账号使用用户名和密码，企业账号使用实例配置的 OIDC 登录。自行部署时，按[部署与运维](deploy.md)第 2 节安装单机版，或按第 3 节部署 PostgreSQL、S3 和独立 runner。Docker 用于执行任务；多节点共用 PostgreSQL、对象存储和主密钥。

首次部署要先创建管理员，再开放公网访问，随后关闭开放注册；具体命令见[公网部署](deploy.md#11-公网部署)。不要把仍未完成首次注册的实例直接暴露到公网。

客户端使用 `lerryn` 二进制，与服务端保持版本一致。源码构建见[常用命令](README.md#常用命令)，构建结果是 `bin/lerryn`；发行包的安装与可用性以对应版本的发布记录为准。下面的命令假设 `lerryn` 已在 `PATH` 中。

```bash
lerryn login https://lerryn.example.com --username alice
lerryn discover
```

密码会在终端中隐藏输入。只使用 OIDC 时，先在控制台创建访问令牌，再从标准输入保存：

```bash
lerryn login https://lerryn.example.com --token-stdin < token.txt
```

令牌文件按凭证保管。CLI 的配置位置、权限和退出登录行为见 [CLI 文档](cli.md#登录与配置)。`discover` 会返回服务端版本、契约版本、运行时和当前身份。任务能否执行还取决于在线 runner 的标签、槽位和可用运行时。

## 连接 Agent

支持远程 MCP 的客户端连接实例的 `https://lerryn.example.com/mcp`，使用绑定本人身份的访问令牌。仅支持 stdio 的客户端使用已登录的 CLI：

```bash
lerryn skills install
claude mcp add lerryn -- lerryn mcp --stdio
```

`skills install` 默认安装到 `~/.claude/skills`；其他客户端可用 `--dir` 指定 Skill 目录。CLI 升级后重新安装，让 Skill 与服务端契约保持一致。

可以让 Agent 执行这样的任务：

> 先发现 Lerryn 的能力，建立一个不访问网络的 Python 测试任务，输出问候语并上报结构化结果。先预览提交，再测试，检查运行状态、日志和结果。暂时不要配置定时触发。

Agent 应保留任务 ID、版本号和运行 ID。需要第三方凭证时，由 Agent 创建填写请求，用户在专用页面填写；聊天、任务参数和源文件里都不要放凭证明文。

## 通过 CLI 验证第一个任务

建立目录 `hello-lerryn/`，放入两个文件。`lerryn.yaml`：

```yaml
name: hello-lerryn
description: 验证任务提交与结果读取
runtime: {language: python, version: "3.12"}
entrypoint: [python, main.py]
params: {who: world}
limits: {timeout: "1m", memory: "128Mi"}
network: none
```

`main.py`：

```python
import sbx

who = sbx.params().get("who", "world")
print(f"Hello, {who}!")
sbx.result("ok", "Greeting completed", {"who": who})
```

`sbx` 由 Lerryn 注入任务容器，无需在客户端或任务依赖中安装。先预览，再提交和运行：

```bash
lerryn tasks apply ./hello-lerryn --dry-run
lerryn tasks apply ./hello-lerryn
lerryn runs start hello-lerryn --test --wait 60s
```

记录返回的运行 ID，读取详情和日志：

```bash
lerryn runs get <run-id>
lerryn runs logs <run-id> --tail 100
```

检查运行状态为 `completed`，日志包含 `Hello, world!`，结果包含 `who: world`。进程退出成功和业务结果是两项信息；排查时同时读 `status`、`result` 和 `error`。等待超时只表示客户端尚未确认结束，运行会继续；需要取消时明确执行 `lerryn runs cancel <run-id>`。

## 修改与激活

读取任务的 `latest_version.seq`，修改文件后带上已读版本，先预览，再提交：

```bash
lerryn tasks get <task-id>
lerryn tasks apply ./hello-lerryn --expected-version 1 --dry-run
lerryn tasks apply ./hello-lerryn --expected-version 1
```

这里的 `1` 要替换为实际读到的版本。遇到 `version_conflict` 时先读取差异并合并，重新读取版本后再提交。每个新版本都要测试；默认没有成功测试的版本不能激活。

需要定时或 Webhook 触发时，让 Agent 读取当前接口说明，预览触发器、确认时区与后续触发时间，完成测试后再激活。涉及发消息、写数据等副作用时，先确认任务的重试约定。触发器配置、暂停和任务编辑经 Agent、API 或 CLI 完成；控制台的手动执行与重试按钮仅向有权限的成员显示，任务也可以关闭手动执行。

持续维护实例时使用[日常运维](operations.md)，命令详情见 [CLI 文档](cli.md)。
