# Lerryn

[简体中文](docs/README.zh-CN.md)

A self-hosted automation sandbox in the Niltavi product family. External agents operate Lerryn through MCP or HTTP APIs. Run Python, Node, and Shell tasks with schedules, webhooks, personal credentials, logs, and artifacts. The web console lets people observe and manage the work; the mobile app provides read access.

## Get started

The [deployment guide](docs/deploy.md) covers single-node Docker Compose, PostgreSQL and S3 deployments, account setup, backups, and upgrades. Use [the CLI and agent guide](docs/cli.md) to connect an agent over MCP or run tasks from the command line.

`lerryn all-in-one` provides the API, MCP endpoint, web console, and an embedded runner in one process. `lerryn server` and `lerryn runner` support separate deployment. Single-node deployments use SQLite and local object storage; multi-node deployments use PostgreSQL and S3-compatible storage. Docker is required for sandbox execution.

## Development

Requirements: Go 1.27, Node.js 24, pnpm 10.18.0, and golangci-lint 2.x. Install frontend dependencies and build the console and server:

```sh
pnpm install --frozen-lockfile
make build
```

The executable is `bin/lerryn`. Run the required checks before merging:

```sh
make check
```

`make help` lists all commands. Developers start their own local servers; see [the design guide](design/README.md) for the component gallery and theme generation.

## Project layout

| Directory | Purpose |
| --- | --- |
| `server/` | Go server, runner, and CLI; `server/cmd/lerryn` is the entry point |
| `api/` | OpenAPI source, bundled contract, and generation configuration |
| `apps/web/`, `apps/mobile/` | Web console and mobile app |
| `packages/` | API client, shared domain models, and web components |
| `design/` | Niltavi theme mappings, native components, gallery, and logo assets |
| `deploy/` | Configuration examples and single-node / multi-node Compose files |
| `runtimes/sdk/` | Python, Node, and Shell task helpers |
| `skills/` | Agent skills for automation, credentials, troubleshooting, and operations |
| `tests/` | Acceptance scenarios, agent evaluations, and capacity checks |
| `docs/` | Product scope, architecture, implementation plan, and operational guides |
| `website/` | English / Chinese introduction website for `lerryn.niltavi.com` |

## Introduction website

Build and validate the standalone website with `pnpm website:build` and `pnpm website:check`. The output is `website/dist/`; hosting and domain setup are described in [website/README.md](website/README.md).

## Documentation

- [Product requirements](docs/agent-automation-sandbox-prd.md), [technical design](docs/agent-automation-sandbox-tech-design.md), and [implementation plan](docs/agent-automation-sandbox-implementation-plan.md)
- [Deployment and configuration](docs/deploy.md)
- [CLI and agent access](docs/cli.md)
- [Acceptance results and known gaps](docs/acceptance-matrix.md)
- [Design system](docs/design-system.md)
- [Repository migration](docs/repository-migration.md) and [name migration](docs/rename-to-lerryn.md)
- [Contributor and agent rules](AGENTS.md)

Technical documents and the product interface currently use Chinese. Lerryn continues from the previous sandbox codebase, with a fresh Git history. Active development takes place in `niltavi/lerryn` on [GitHub](https://github.com/niltavi/lerryn) and [Gitea](https://git.yarinz.com/niltavi/lerryn).

## License

The repository is licensed under [AGPL-3.0](LICENSE). `runtimes/sdk/` and `skills/` are licensed under Apache-2.0; see their own license files. Third-party notices are in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).
