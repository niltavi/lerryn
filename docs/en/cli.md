# lerryn CLI

[简体中文](../cli.md) · [Documentation](README.md)

The `lerryn` binary is both the server and the command-line client. Client commands are available to agents running in a shell and to maintainers. For design background, see section 10.6 of the [technical design (Chinese)](../agent-automation-sandbox-tech-design.md).

## How it works

The CLI shares an operation index with MCP tools, without handwritten request code. At startup, the embedded OpenAPI contract (`api/bundled/openapi.yaml`) is loaded into the index. Parameter mapping follows the same rules as MCP tools (technical design, section 10.3):

- Operations added to the contract become available through `lerryn api <operationId>` without CLI code changes.
- Parameter names match MCP: path, query, and header parameters become snake_case; request-body properties are flattened into the same object. The `input_schema` returned by `api_describe` also applies to the CLI.
- Authorization is enforced by the server. The CLI does not decide access scope; insufficient token scope produces `insufficient_scope`.

Common workflows have convenience commands: `login`, `logout`, `discover`, `tasks`, `runs`, and `mcp`. They build requests through the same index with more convenient arguments and output. `skills` installs embedded Skills locally without accessing the server.

Match the CLI contract version to the server. An older CLI cannot call newer operations; a newer CLI calling operations absent from the server receives `not_found`. `lerryn discover` reports the server's contract version as `api.spec_version`.

## Authentication and configuration

```bash
lerryn login https://lerryn.example.com --username alice          # Hidden password prompt
echo "$PASSWORD" | lerryn login https://lerryn.example.com -u alice --password-stdin
lerryn login https://lerryn.example.com --token-stdin < token.txt  # Token created in the console
lerryn logout
```

Password login first opens a session, creates an access token named `lerryn-cli on <hostname>`, and immediately signs out of that session. Default scopes are `read`, `write`, and `execute`; adjust them with `--scopes` and `--expires-in-days`. Subsequent requests use only the access token. `lerryn logout` revokes this token on the server before removing local configuration. Tokens saved using `--token` or `--token-stdin` may be used elsewhere, so logout removes their local configuration without revoking them.

The configuration file stores the server URL and access token:

| Location | Selection |
| --- | --- |
| `$LERRYN_CLI_CONFIG` | Used when set |
| `$XDG_CONFIG_HOME/lerryn/cli.json` | Next choice |
| `~/.config/lerryn/cli.json` | Default; `%AppData%\lerryn\cli.json` on Windows |

Files use mode 0600 and directories 0700. Writes use a temporary file followed by a rename. If other users can read or write the file, the CLI refuses it and suggests `chmod 600`, following the same approach as SSH private keys.

Sources in descending precedence:

| Setting | Sources |
| --- | --- |
| Server URL | `--server`, `LERRYN_SERVER`, configuration file |
| Access token | `LERRYN_TOKEN`, configuration file |

The saved token is sent only to its associated server. When `--server` or `LERRYN_SERVER` points elsewhere, the saved token is omitted. CI and agent sandboxes can set `LERRYN_SERVER` and `LERRYN_TOKEN` directly without login.

Server URLs can include a path prefix, such as `https://example.com/lerryn` behind a reverse proxy. Login warns about plaintext password and token transmission when using a non-local `http://` address.

## Output

Every client command accepts `-o, --output`:

| Value | Behavior |
| --- | --- |
| `auto` | Default: `text` when stdout is a terminal, otherwise `json` |
| `json` | API JSON with unchanged fields, for agents and scripts |
| `text` | A human-readable summary |

Agent shell calls usually have non-terminal stdout and therefore default to JSON.

For API errors, `json` writes the problem document, including `code`, `next_actions`, and `request_id`, to stdout. `text` writes the title, detail, request ID, and next actions to stderr. Both modes also write an `error:` line to stderr.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Local error: configuration, task files, or server connection |
| 2 | Usage error: missing arguments, invalid types, unknown operation or parameter |
| 3 | Not logged in, or server rejected authentication (HTTP 401) |
| 4 | Other API error, such as `not_found`, `validation_failed`, or `insufficient_scope` |
| 5 | The awaited run ended unsuccessfully |
| 6 | The wait limit elapsed before completion was confirmed |

Codes 5 and 6 occur only in `runs wait` and `runs start --wait`. Success means `completed` with no business result of `failed` reported by `sbx result`. A `partial` result counts as success; inspect `result` to distinguish it.

## Tasks

```bash
lerryn tasks apply ./weekday-digest
lerryn tasks apply ./weekday-digest --dry-run --expected-version 3
lerryn tasks apply ./weekday-digest --expected-version 3
lerryn tasks get <task-id>
lerryn tasks export <task> --file task.json
lerryn tasks import task.json
```

A task directory contains `lerryn.yaml` (`lerryn.yml` is also accepted; use `-f` to select another file). Its contents are the task manifest fields (technical design, section 5.3), plus an optional `name`. If omitted, the directory name is used. `--name` overrides both.

```yaml
name: weekday-digest
description: Weekday morning digest
runtime: {language: python, version: "3.12"}
entrypoint: [python, main.py]
params: {source: internal-crm}
```

Other files are uploaded as UTF-8 text, preserving the executable bit. The manifest itself, files and directories starting with `.` (such as `.git`, `.venv`, and `.env`), `__pycache__`, and `node_modules` are skipped. Symbolic links and non-UTF-8 files cause an error; the CLI does not read outside the directory.

The default project is the user's personal project (`principal.personal_project_id` from `discover`). Select another with `--project`.

Updating an existing task requires `--expected-version` with the latest version you read (`latest_version.seq` in `tasks get`). The CLI maps it to `If-Match`. Omitting it produces `precondition_required`. A concurrent new version produces `version_conflict` and `current_version`. Inspect the other changes with `lerryn api diffTaskVersion --task-id <id> --seq <version>`, merge, then resubmit. New tasks do not require this argument.

`--dry-run` validates and returns a preview without saving: the prospective version, differences from the active version (or previous version if none is active), and warnings. Text output lists changed files with added and removed line counts.

### Export and import a single task

```bash
lerryn tasks export weekday-digest --history --file digest.json
lerryn tasks export <task-id> --version 3 > digest.json
lerryn tasks import digest.json --dry-run
lerryn tasks import digest.json --project <project-id> --name digest-copy
```

`export` calls `exportTask` and writes a JSON task bundle: task name, the latest version's manifest and files (or the version selected by `--version`), and trigger definitions (`--no-triggers` omits them). `--history` includes earlier versions. Bundles omit run records, credential values and bindings, webhook tokens, and project, user, and task IDs, so they can be imported into another deployment. Without `--file`, the bundle goes to stdout regardless of `-o`. With `--file`, the file uses mode 0600 and the command outputs a summary. Bundles are limited to approximately 4 MiB. `--history` includes at most the latest 50 versions, omitting oldest versions when necessary; the summary and `omitted_versions` report the count. Any member who can view a task, including viewers, can export it.

`import` calls `importTask` to create a task in the personal project or `--project`. Use `-` for stdin. The importer becomes the task and version creator and needs the project's maintainer or owner role. After import:

- Versions are renumbered from 1; `versions` maps old to new numbers.
- Versions are untested and the task is inactive. Run `lerryn runs start <task> --test`, inspect the result, then activate.
- Credential reference names remain unbound (`missing` in `credentials`). Users supply them through credential requests or owners authorize their use.
- Webhook triggers receive new tokens, shown only in this output.
- A duplicate task name in the project produces `version_conflict`; select another with `--name`.
- Validation matches `tasks apply`, including file-count and size limits and rejecting the caller's credential values in files, manifests, and trigger parameters.

For an entire deployment use the maintenance commands `lerryn export` and `lerryn import` instead; see [deployment](deploy.md), section 7.

## Runs

```bash
lerryn runs start <task> --test --param who=world --wait 60s
lerryn runs start <task> --params-json @params.json --idempotency-key deploy-42
lerryn runs get <run-id> [--wait 30s]
lerryn runs wait <run-id> --timeout 10m
lerryn runs logs <run-id> --tail 200
lerryn runs logs <run-id> --follow
lerryn runs cancel <run-id>
```

- `<task>` accepts an ID or name. Names use an exact `name` filter on `listTasks`. Duplicate names across projects cause an error; use an ID instead.
- Values in `--param name=value` are parsed as JSON when valid (`n=3` is numeric, `ok=true` is boolean), otherwise as strings. Supplying parameters replaces the manifest's entire `params` object.
- `runs wait` repeatedly uses `getRun` server-side waiting (up to 60 seconds per call) until completion or timeout. `--timeout` covers connection, headers, and body reads. On timeout it prints the last successfully read run, or no run data if none was read.
- In JSON mode, `runs logs` outputs one API page with `next_cursor`; continue with `--cursor`. `--follow` reads through completion and the final line, emitting one log-line JSON object per line in JSON mode.

## Generic API commands

```bash
lerryn api                                   # List all operations
lerryn api getTask --describe                # Description, scopes, input and response schemas
lerryn api getTask --task-id <id>
lerryn api getRun --arg run_id=<id> --wait 10
lerryn api applyTask --input request.json    # JSON argument object; - reads stdin
```

Each operation becomes a subcommand named after its operationId. Options follow schema types; underscores become hyphens (`run_id` becomes `--run-id`):

- Strings, integers, and numbers: `--name value`; numeric formats are checked before sending.
- Booleans: `--flag` or `--flag=false`.
- Scalar arrays: repeat the option, as in `--tag a --tag b`.
- Objects and other compound types: JSON values.

Arguments can also use `--arg name=value` (JSON when parseable, otherwise a string) or `--input`. Precedence is typed options, then `--arg`, then `--input`. Arguments colliding with CLI options such as `--server` or `--output` must use `--arg` or `--input`.

Response bodies are printed unchanged. Session-only operations, such as `createToken`, return `insufficient_scope` when called with an access token. `session_only` and `callable` in `--describe` indicate this restriction.

## Skills

```bash
lerryn skills list
lerryn skills install                    # Claude Code user directory: ~/.claude/skills
lerryn skills install --project          # .claude/skills in the current directory
lerryn skills install --dir <directory> --skill lerryn-automation
```

The binary embeds four Skills matched to its contract: `lerryn-automation`, `lerryn-credentials`, `lerryn-troubleshoot`, and `lerryn-operate` (source: `skills/`). Installation creates a subdirectory per Skill and a `.lerryn-skill` version marker. Reinstallation replaces managed directories, removing files deleted in later versions and leaving unrelated directory contents alone. An existing same-name directory without the marker is refused with exit code 1; use `--force` when replacement is intended. Other Agent Skills clients can use `--dir` to select their directory.

Reinstall after upgrading the CLI or server so Skills match the server contract.

## MCP stdio proxy

```bash
lerryn mcp --stdio
claude mcp add lerryn -- lerryn mcp --stdio
```

This supports clients that only accept a local stdio MCP server. Tools match the server's `/mcp`: exposed contract operations plus `api_describe` and `api_call`. Both use `mcp.NewServer` for registration. The server forwards `/mcp` internally into the API chain; the stdio proxy sends HTTP requests with the CLI's access token.

Interrupted response reads and canceled calls return tool errors; partial content is never presented as success. Complete responses exceeding the tool-result size limit are marked truncated, with advice to narrow the request.

- Stdout carries only MCP protocol data; diagnostics go to stderr.
- Without a usable token the proxy exits immediately with code 3.
- Connection failures return an `internal_error` tool error with `next_actions` advising an address check and retry.
- The server sees ordinary REST requests with User-Agent `lerryn-cli/<version>`. The audit `ToolCall` metadata identifying MCP exists only for in-process server `/mcp` calls.

Clients supporting remote MCP should connect directly to the server's `/mcp` endpoint.
