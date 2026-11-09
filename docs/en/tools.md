# Tools integration

[简体中文](../zh-CN/tools.md) · [Documentation index](README.md)

Lerryn provides MCP, HTTP API, and CLI access for external agents to create and update tasks, configure triggers, execute tasks, and read logs and results. Settings → Tools lists built-in capabilities and connection addresses for the current instance, and offers this guide for download. Project tools appear separately below them for tasks to reference at runtime.

Examples use `https://lerryn.example.com`. Replace it with the instance address shown on the Tools page. Keep client and server versions aligned where possible.

## Access tokens

Sign in to the console and create a token under Settings → Access tokens. The token is displayed once. Store it in your client's credential configuration and send it in the `Authorization: Bearer <token>` header. Token scopes and project roles both apply:

| Scope | Use |
| --- | --- |
| `read` | Discover capabilities and read tasks, runs, logs, and operation descriptions |
| `write` | Submit tasks, configure triggers, request credentials, and register project tools |
| `execute` | Start, cancel, rerun, activate, or pause tasks, and call AI models |

An agent maintaining tasks usually needs all three scopes; a client querying results may only need `read`. Tokens inherit their owner's project permissions. Scopes do not elevate a project role. Replace expired or revoked tokens in your client. Keep tokens out of URLs, task files, and task parameters.

## Connect through MCP

The remote endpoint is `https://lerryn.example.com/mcp`, using Streamable HTTP and an access token. Clients supporting `mcpServers`, `url`, and `headers` can use this structure. In other clients, enter the same address, transport, and authentication in their corresponding fields.

```json
{
  "mcpServers": {
    "lerryn": {
      "url": "https://lerryn.example.com/mcp",
      "headers": {
        "Authorization": "Bearer <LERRYN_ACCESS_TOKEN>"
      }
    }
  }
}
```

The user replaces `<LERRYN_ACCESS_TOKEN>` locally in the client. Call `discover` first and check `principal`, `runtimes`, `capabilities`, and `skills.version`. Select a runtime from the returned list.

Clients limited to local stdio can use the CLI proxy. Log in, install Skills, and register `lerryn mcp --stdio` as the server:

```bash
lerryn login https://lerryn.example.com --token-stdin < token.txt
lerryn skills install
lerryn mcp --stdio
```

Protect the token file as a credential. The MCP client launches the final command: executable `lerryn`, arguments `["mcp", "--stdio"]`. Local account login is also supported: `lerryn login https://lerryn.example.com --username alice` prompts for a hidden password. OIDC users can use a console-created token.

## Built-in MCP tools

| Tool | HTTP operation | Scope | Purpose |
| --- | --- | --- | --- |
| `discover` | `discover` | `read` | Versions, runtimes, capabilities, identity |
| `environment_get` | `getEnvironment` | `read` | Check the current deployment environment |
| `task_get` | `getTask` | `read` | Task, tags, and latest version |
| `task_list` | `listTasks` | `read` | List visible tasks by project, name, or tag |
| `task_set_tags` | `setTaskTags` | `write` | Replace all tags without creating a version; pass an empty array to clear them |
| `task_apply` | `applyTask` | `write` | Create a task or submit a complete version, with `dry_run` |
| `task_validate` | `validateTaskVersion` | `read` | Check credentials, tools, runners, and test status |
| `trigger_set` | `setTriggers` | `write` | Replace all cron or Webhook triggers, with previews |
| `task_set_active` | `setTaskActivation` | `execute` | Activate a version or pause a task |
| `run_start` | `startRun` | `execute` | Start a test or regular run |
| `run_get` | `getRun` | `read` | Status, stages, results, and errors; `wait` is at most 60 seconds |
| `run_logs` | `readRunLogs` | `read` | Tail logs or read cursor-based pages |
| `run_cancel` | `cancelRun` | `execute` | Request cancellation, then read status to confirm |
| `run_rerun` | `rerunRun` | `execute` | Create a new run linked to the original |
| `credential_request` | `createCredentialRequest` | `write` | Create a credential form link for the user |
| `ai_models` | `listAIModels` | `read` | List the models enabled for the current account |
| `ai_chat` | `createAIChat` | `execute` | Generate text with the model service configured for the current account |
| `api_describe` | Descriptions and schemas | `read` | Search by `operation_id` or `query` |
| `api_call` | A public operation | By operation | Call by `operation_id`, passing parameters in `arguments` |

Call `environment_get` without arguments before an operation that depends on the deployment environment. A production instance returns `{"environment":"production"}`. The value comes from the [deployment configuration](deploy.md#4-configuration-reference). An unconfigured instance returns `unspecified`. The `test` parameter on `run_start` still controls whether an individual run is a test.

The dedicated tools cover common workflows. Describe other operations before calling them. For example, pass this to `api_describe` to inspect the project tool list operation:

```json
{"operation_id": "listTools"}
```

Then pass this to `api_call`:

```json
{"operation_id": "listTools", "arguments": {"project_id": "<PROJECT_ID>"}}
```

Follow the returned `input_schema`, token scopes, and authentication requirements. Some operations require a console session and cannot use access tokens. File downloads and similar operations may need direct HTTP or the CLI.

To find tagged tasks, call `task_list` with `{"tag":"Reporting","limit":10}`. To edit tags, read the current tags with `task_get`, then pass the complete desired set to `task_set_tags`, for example `{"task_id":"<TASK_ID>","tags":["Reporting","CRM"]}`.

To build a task, call `discover`, preview `task_apply` with `dry_run`, and submit. Request credentials with `credential_request` when needed. Test with `run_start` and `test: true`, and inspect logs and business results. Preview triggers before setting them, then activate the tested version. Before an update, read `latest_version.seq` and pass it as `if_match`; merge changes on a version conflict. Cron triggers require an IANA timezone. Tests execute real code: confirm tasks with side effects with the user first.

## Configure AI capabilities

Open Settings → Credentials → AI capabilities in the web console. Enter the provider’s HTTPS API base URL, API key, and one model ID per line. Include the provider’s version path, as in `https://api.openai.com/v1`. Lerryn appends `/chat/completions`.

An agent with a token from the same account can call `ai_models` and pass one of the returned IDs to `ai_chat`. This tool generates text in a single response. Messages are sent to the configured provider, and calls may incur charges. Lerryn blocks requests and responses that contain a stored credential value from the current account.

```json
{
  "model": "your-model-id",
  "messages": [{"role": "user", "content": "Summarize the following in one sentence."}]
}
```

The API key is encrypted in the account’s credential store. Leave the key blank when changing only the model list, and re-enter it if you change the base URL. For an internal HTTPS model gateway, the deployment operator must configure `ai.allowed_networks`.

## HTTP API and CLI

The HTTP base address is `https://lerryn.example.com/api/v1`. This example assumes `LERRYN_TOKEN` has been set in the local credential environment:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $LERRYN_TOKEN" \
  https://lerryn.example.com/api/v1/discover
```

The complete contract is in `api/bundled/openapi.yaml` in the repository. Use MCP's `api_describe` for paths, parameters, and responses. The CLI uses the same operation IDs:

```bash
lerryn discover
lerryn api getEnvironment
lerryn api listTools --describe
lerryn api listTools --project-id <PROJECT_ID>
lerryn tasks apply ./hello-lerryn --dry-run
lerryn tasks apply ./hello-lerryn
lerryn runs start hello-lerryn --test --wait 60s
```

Generic `api_call` and CLI parameters use snake_case; CLI flags replace underscores with hyphens, for example `project_id` becomes `--project-id`. Direct HTTP requests follow the contract's path, query, header, and body definitions.

Use `code`, `errors`, `next_actions`, and `request_id` in HTTP error responses to investigate failures. `insufficient_scope` means a token scope is missing. `not_found` can also mean the caller lacks access. Correct the indicated fields for `validation_failed`; reread and merge for `version_conflict`. `completed` means the process exited normally; inspect `result` to judge business success.

## Agent Skills and task helpers

`lerryn skills install` installs four embedded Skills: `lerryn-automation`, `lerryn-credentials`, `lerryn-troubleshoot`, and `lerryn-operate`. The default directory is `~/.claude/skills`; use `--dir <directory>` for other clients. MCP clients can also read resources at `lerryn://skills/<name>/SKILL.md`. Match versions using `discover.skills.version`.

Task containers provide `sbx` for Python, Node, and Shell without installing it as a task dependency. It reports stages and results and calls external MCP tools:

```bash
sbx stage start fetch
sbx stage done fetch
sbx result --status ok --summary "Done" --data-json '{"count": 1}'
```

Python offers `sbx.params()`, `with sbx.stage("fetch"):` and `sbx.result(...)`; Node provides corresponding helpers. Write artifacts to `/sbx/out`, temporary files to `/sbx/work` or `/tmp`, and persistent state to `/sbx/state`. State is shared per task, including test runs. Only completed runs with exit code zero commit state; the last successful commit wins under concurrency.

## Connect project tools

Project tools are external HTTP APIs, MCP services, or script packages that tasks depend on. Maintainers and owners register them with `registerTool`; registering the same name updates it. A task references names in its manifest's `tools` field and uses the current registration at its next run. Removing a tool with `removeTool` prevents tasks still referencing it from validating.

For example, register an external MCP server with `api_call`:

```json
{
  "operation_id": "registerTool",
  "arguments": {
    "project_id": "<PROJECT_ID>",
    "name": "crm-mcp",
    "kind": "mcp",
    "description": "Look up customers and opportunities",
    "url": "https://crm.example.com/mcp"
  }
}
```

Reference it and declare its credential separately in the task manifest:

```yaml
tools: [crm-mcp]
network: full
credentials:
  - name: CRM_TOKEN
```

Use `credential_request` to have the user fill in and bind the credential. Call the tool inside the task:

```bash
sbx mcp call crm-mcp search_accounts --args '{"query":"acme"}' \
  -H "Authorization: Bearer $CRM_TOKEN"
```

These calls create `tool.call` run events. HTTP and MCP addresses are injected as `LERRYN_TOOL_<NAME>_URL`, with names uppercased and hyphens replaced by underscores. All referenced tools are also included in the JSON environment variable `LERRYN_TOOLS`. Tasks make their own HTTP requests for HTTP tools. External calls require `network: full` and network connectivity from the container to the target.

Script tools register with `kind: script` and `files`, without `url`. Files are mounted read-only at `/sbx/tools/<name>` and exposed through `LERRYN_TOOL_<NAME>_DIR`. Inject tokens and other credentials through the task's `credentials` declarations; keep tool URLs, descriptions, and files free of secrets.
