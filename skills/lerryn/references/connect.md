# 连接 Lerryn

以下用 `https://lerryn.example.com` 代表实例地址，替换成控制台 Settings → Tools 页显示的地址。`LERRYN_TOKEN` 是用户在控制台 Settings → 访问令牌中创建的令牌，由用户本人放进环境变量或客户端配置。你只写配置结构，不经手令牌的值。

## 令牌范围

| 范围 | 能做什么 |
| --- | --- |
| `read` | 发现能力，读取任务、运行、日志、接口说明 |
| `write` | 提交任务、设置触发器、发起凭证填写请求、登记项目工具 |
| `execute` | 发起、取消、重跑运行，激活或暂停任务，调用 AI 模型 |

建立和维护任务需要三个范围；只看结果可以只给 `read`。令牌继承用户在项目中的角色，范围不会提升角色。连上后从 `discover` 的 `principal.scopes` 确认。

## 远程 MCP

端点 `https://lerryn.example.com/mcp`，传输方式 Streamable HTTP，请求头 `Authorization: Bearer <令牌>`。

Claude Code，项目级 `.mcp.json`（`${LERRYN_TOKEN}` 在启动时从环境变量展开，文件里不出现令牌）：

```json
{
  "mcpServers": {
    "lerryn": {
      "type": "http",
      "url": "https://lerryn.example.com/mcp",
      "headers": {"Authorization": "Bearer ${LERRYN_TOKEN}"}
    }
  }
}
```

Codex，`~/.codex/config.toml`：

```toml
[mcp_servers.lerryn]
url = "https://lerryn.example.com/mcp"
bearer_token_env_var = "LERRYN_TOKEN"
```

其他支持 `mcpServers`、`url`、`headers` 的客户端用相同结构；只有图形界面的客户端，在服务器地址、传输方式和认证栏里填相同信息。字段名以客户端自己的文档为准。

配置改动后通常要重启客户端或重新加载 MCP 服务器，之后调用 `discover` 验证。

## stdio 代理（CLI）

客户端只支持本地 stdio 时，由用户在终端里登录并安装 Skill：

```bash
lerryn login https://lerryn.example.com --token-stdin < token.txt
lerryn skills install
```

再把 `lerryn mcp --stdio` 注册为 MCP 服务器，例如 Claude Code：

```bash
claude mcp add lerryn -- lerryn mcp --stdio
```

其他客户端：命令 `lerryn`，参数 `["mcp", "--stdio"]`。本地账号也可以用 `lerryn login <地址> --username <用户名>` 登录，密码在终端隐藏输入，由用户自己输入。

## HTTP API 与 CLI

没有 MCP 时，HTTP API 的基地址是 `https://lerryn.example.com/api/v1`，操作和 MCP 工具一一对应：

```bash
curl --fail-with-body -H "Authorization: Bearer $LERRYN_TOKEN" \
  https://lerryn.example.com/api/v1/discover
```

CLI 用同一组 operationId：

```bash
lerryn discover
lerryn api listRuns --describe
lerryn api listRuns --task-id <TASK_ID> --limit 10
lerryn runs get <RUN_ID> --wait 30s
```

令牌只放在请求头或 CLI 的登录配置里，不放进 URL。

## 安装其他 Skill

`lerryn skills install` 默认把与 CLI 版本匹配的全部 Lerryn Skill 写入 `~/.claude/skills`；`--project` 写入当前目录的 `.claude/skills`，`--dir <目录>` 写入其他客户端的 skills 目录，`--skill <名称>` 只装一个。

没有 CLI 时，从实例获取与服务端同版本的 Skill：MCP 客户端读 `lerryn://skills/<名称>/SKILL.md` 等 resource；也可以匿名请求 `GET https://lerryn.example.com/api/v1/skills/<名称>`（`getSkill`），把返回的 `files` 按路径写进 skills 目录。写入用户目录前先征得用户同意。

## 连接不上时

| 现象 | 处理 |
| --- | --- |
| 401、`unauthenticated` | 令牌错误、过期或已撤销，请用户换令牌 |
| `insufficient_scope` | 令牌缺少范围，请用户新建带所需范围的令牌 |
| `not_found` | 资源不存在，或当前身份无权查看 |
| MCP 返回 403、请求挂起 | 反向代理配置问题，转到 `lerryn-operate` Skill |
| `discover` 能通但 `principal` 为 null | 请求没带上令牌，检查请求头和环境变量 |
