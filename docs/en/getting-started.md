# Getting started with Lerryn

[简体中文](../zh-CN/getting-started.md) · [Documentation](README.md)

External agents create and maintain Lerryn tasks through APIs, MCP, or the CLI. The console displays tasks, logs, results, and artifacts and provides account and credential management. This guide connects to an instance and verifies submission, execution, and result retrieval with a Python task that requires no network access or credentials.

## Prepare an instance and client

For an existing instance, obtain its address from the maintainer and sign in to the console. Local accounts use a username and password; enterprise accounts use the instance's configured OIDC provider. To host an instance, follow [deployment and operations](deploy.md), section 2 for a single node or section 3 for PostgreSQL, S3, and separate runners. Docker executes tasks. Multi-node deployments share PostgreSQL, object storage, and the master key.

Create an administrator before opening a new instance to the internet, then disable open registration. See [public deployment](deploy.md#11-public-deployment) for commands. Keep an instance private until the first administrator exists.

Use the `lerryn` binary as the client and match its version to the server. See the [development instructions](README.md#development) to build `bin/lerryn` from source; installation and availability of release archives depend on the corresponding release records. The commands below assume `lerryn` is on `PATH`.

```bash
lerryn login https://lerryn.example.com --username alice
lerryn discover
```

The terminal hides password input. For OIDC-only access, create an access token in the console and save it through standard input:

```bash
lerryn login https://lerryn.example.com --token-stdin < token.txt
```

Treat the token file as a credential. See [CLI authentication and configuration](cli.md#authentication-and-configuration) for configuration paths, permissions, and logout behavior. `discover` returns the server version, contract version, runtimes, and current identity. Execution also depends on online runners with suitable labels, slots, and runtimes.

## Connect an agent

Clients that support remote MCP connect to `https://lerryn.example.com/mcp` with an access token bound to the user's identity. Clients that only support stdio use the authenticated CLI:

```bash
lerryn skills install
claude mcp add lerryn -- lerryn mcp --stdio
```

`skills install` defaults to `~/.claude/skills`. Use `--dir` for another client's Skill directory. Reinstall after upgrading the CLI to keep Skills aligned with the server contract.

An example request to the agent:

> Discover Lerryn's capabilities, then create a Python test task with no network access. Print a greeting and report a structured result. Preview the submission before saving, run a test, and inspect its status, logs, and result. Leave scheduled triggers unconfigured for now.

The agent should retain the task ID, version, and run ID. When third-party credentials are needed, the agent creates a credential request and the user fills in its dedicated page. Keep plaintext credentials out of chat, task parameters, and source files.

## Verify a first task through the CLI

Create `hello-lerryn/` with two files. In `lerryn.yaml`:

```yaml
name: hello-lerryn
description: Verify task submission and result retrieval
runtime: {language: python, version: "3.12"}
entrypoint: [python, main.py]
workflow:
  - title: Generate a greeting
    description: Read the recipient, print the greeting, and report the result.
params: {who: world}
limits: {timeout: "1m", memory: "128Mi"}
network: none
```

In `main.py`:

```python
import sbx

who = sbx.params().get("who", "world")
print(f"Hello, {who}!")
sbx.result("ok", "Greeting completed", {"who": who})
```

Lerryn injects `sbx` into the task container; no installation is needed in the client or task dependencies. Preview, submit, and run:

```bash
lerryn tasks apply ./hello-lerryn --dry-run
lerryn tasks apply ./hello-lerryn
lerryn runs start hello-lerryn --test --wait 60s
```

Keep the returned run ID, then inspect details and logs:

```bash
lerryn runs get <run-id>
lerryn runs logs <run-id> --tail 100
```

Check that the status is `completed`, the logs include `Hello, world!`, and the result contains `who: world`. Process completion and business results are separate information: inspect `status`, `result`, and `error` together. A wait timeout only means the client has not confirmed completion; the run continues. Cancel explicitly with `lerryn runs cancel <run-id>` when needed.

## Update and activate

Read the task's `latest_version.seq`. After editing files, preview and submit with that version:

```bash
lerryn tasks get <task-id>
lerryn tasks apply ./hello-lerryn --expected-version 1 --dry-run
lerryn tasks apply ./hello-lerryn --expected-version 1
```

Replace `1` with the actual version you read. On `version_conflict`, inspect and merge the changes, then read the version again before submitting. Test every new version. By default a version requires a successful test before activation.

For scheduled or webhook execution, have the agent read the current operation descriptions, preview triggers, confirm the time zone and upcoming fire times, and activate after testing. Establish retry rules for tasks that send messages or write data. Configure triggers, pause tasks, and edit tasks through the agent, API, or CLI. Console manual-run and retry buttons appear only for authorized members; a task can also disable manual execution.

Use [day-to-day operations](operations.md) to maintain an instance and the [CLI guide](cli.md) for command details.
