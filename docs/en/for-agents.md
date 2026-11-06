# For agents

[简体中文](../for-agents.md) · [Repository](../../README.md)

If the user gave you this link, start here to operate Lerryn. Choose the guides for their goal, connect to the instance, carry out the work, and return the results with console links.

Lerryn is a self-hosted automation sandbox. You maintain Python, Node, and Shell tasks through MCP, the HTTP API, or the CLI. The platform handles scheduling, container execution, credential injection, and run records.

## Start here

1. Establish whether the user wants to connect to an existing instance or deploy a new one. For an existing instance, follow [getting started](getting-started.md). For a new one, choose a single-node or multi-node setup in [deployment and operations](deploy.md), and create the first administrator before opening access.
2. Follow [Tools integration](tools.md) to connect through remote MCP or the stdio proxy of an authenticated `lerryn` CLI. Users enter credentials in their local client or the dedicated credential form.
3. Call `discover` to read the current identity, project, runtimes, capabilities, and Skill version. Install matching operation Skills with `lerryn skills install`; use `--dir <directory>` for another client. MCP clients can also read `lerryn://skills/<name>/SKILL.md`.
4. Verify a test task with no network access or credentials using [getting started](getting-started.md), then continue with the user's goal. Retain task IDs, versions, run IDs, and returned `links.console` for follow-up work.

## Operation directory

The four bundled Skills and their references are written in Chinese. User guides are available in both languages.

| User goal | Read |
| --- | --- |
| Install, register the first administrator, expose an instance, or deploy multiple nodes | [Deployment and operations](deploy.md), [operations Skill](../../skills/lerryn-operate/SKILL.md) |
| Authenticate, configure tokens, connect through MCP, HTTP API, or CLI, and discover capabilities | [Tools integration](tools.md), [CLI and agent access](cli.md) |
| Create, update, preview, test, and activate tasks | [Automation Skill](../../skills/lerryn-automation/SKILL.md) |
| Define manifests, parameters, dependencies, and execution limits | [Manifest guide](../../skills/lerryn-automation/references/manifest.md), [complete fields](../../skills/lerryn-automation/references/manifest-fields.md), [examples](../../skills/lerryn-automation/references/examples.md) |
| Configure schedules and webhooks, check timezones and upcoming fire times | [Trigger guide](../../skills/lerryn-automation/references/triggers.md) |
| Inspect runs, stages, logs, business results, and artifacts; cancel or create a linked rerun | [Troubleshooting Skill](../../skills/lerryn-troubleshoot/SKILL.md), [CLI and agent access](cli.md) |
| Store, update, authorize, or revoke API keys, tokens, cookies, and other credentials | [Credentials Skill](../../skills/lerryn-credentials/SKILL.md) |
| Report stages and results, save artifacts, or persist state between runs | [sbx guide](../../skills/lerryn-automation/references/sbx.md), [Python SDK](../../runtimes/sdk/python/README.md), [Node SDK](../../runtimes/sdk/node/README.md), [Shell SDK](../../runtimes/sdk/sh/README.md) |
| Connect external MCP services, HTTP APIs, or script tools | [Tools integration](tools.md) |
| Manage accounts, projects, members, permissions, and other system resources | `api_describe` / `api_call` in [Tools integration](tools.md), [generic CLI API commands](cli.md), [complete API contract](../../api/bundled/openapi.yaml) |
| Diagnose missed triggers, failed, timed-out, or interrupted runs, or unexpected results | [Troubleshooting Skill](../../skills/lerryn-troubleshoot/SKILL.md), [error codes](../../skills/lerryn-troubleshoot/references/error-codes.md) |
| Back up, restore, upgrade, roll back, migrate, scale, or check instance health | [Operations Skill](../../skills/lerryn-operate/SKILL.md), [runbook](../../skills/lerryn-operate/references/runbook.md), [day-to-day operations](operations.md) |
| Export or import tasks or an entire deployment | [CLI and agent access](cli.md), [deployment and operations](deploy.md) |
| Change Lerryn source code, validate changes, or publish releases | [Repository instructions](../../AGENTS.md), [implementation plan](../agent-automation-sandbox-implementation-plan.md), [release process](../release.md) |

## Execute and report

Read the relevant Skill before operating. Use the instance's `discover` and `api_describe` responses to check parameters, permissions, and runtimes. Describe unfamiliar operations instead of guessing. The CLI equivalent is `lerryn api <operationId> --describe`.

Preview task submissions, test them, and inspect business results before activation. Read the latest version before updating a task and merge changes on version conflicts. Scheduled triggers need an explicit IANA timezone and a check of upcoming fire times. Follow the automation Skill for the full procedure.

Handle credentials through the platform's dedicated form, using only references and status. Tests execute real code. Follow the relevant Skill and the user's existing authorization for side effects such as sending messages or writing external data, and for production downtime, restore, or deletion.

Report what was completed and where to find the results and artifacts, with console links. `completed` only means the process exited normally; inspect `result` and `error` to determine business success, and say when a result is missing.
