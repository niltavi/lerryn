# Lerryn documentation

[简体中文](../README.zh-CN.md) · [Language selection](../README.md) · [Repository](../../README.md)

Lerryn is a self-hosted automation sandbox in the Niltavi product family. External agents create and maintain Python, Node, and Shell tasks through HTTP APIs, MCP, or the CLI. Lerryn handles schedules, webhooks, isolated execution, personal credentials, logs, results, and artifacts. People use the web console to observe runs and manage access; the mobile app provides read access.

## User guides

| Guide | Contents |
| --- | --- |
| [Getting started](getting-started.md) | Choose a deployment, connect an agent, submit and test a first task |
| [Deployment and operations](deploy.md) | Linux and Mac mini installation, multi-node deployment, every configuration option, metrics, backups, migration, upgrades, and public access |
| [CLI and agent access](cli.md) | Authentication, configuration, task versions, runs, task bundles, generic API calls, Skills, and the MCP stdio proxy |
| [Day-to-day operations](operations.md) | Health checks, diagnosing failed runs, backup practice, runner maintenance, and recovery |

Start with the getting-started guide. Maintainers should read deployment and operations before opening an instance to users. The product console currently uses Chinese; documentation language does not change the interface language.

## Development

The repository uses Go 1.27, Node.js 24, pnpm 10.18.0, and golangci-lint 2.x. From the repository root:

```sh
pnpm install --frozen-lockfile
make build
make check
```

`make build` produces `bin/lerryn`, including the web console. `make help` lists the available commands. Developers start their own local development servers. Read [AGENTS.md](../../AGENTS.md) before contributing.

| Directory | Purpose |
| --- | --- |
| `server/` | Go server, runner, and CLI; entry point: `server/cmd/lerryn` |
| `api/` | OpenAPI source, bundled contract, and code-generation configuration |
| `apps/web/`, `apps/mobile/` | Web console and read-only mobile app |
| `packages/` | Shared API client, domain models, and web components |
| `design/` | Design tokens, native components, component gallery, and brand assets |
| `deploy/` | Configuration examples and Compose deployments |
| `runtimes/sdk/` | Python, Node, and Shell task helpers |
| `skills/` | Agent Skills for automation, credentials, troubleshooting, and operations |
| `tests/` | Acceptance tests, agent evaluations, and capacity tests |
| `docs/` | User guides and product, architecture, and validation records |
| `website/` | Standalone English and Chinese website for `lerryn.niltavi.com` |

Build and check the introduction website with `pnpm website:build` and `pnpm website:check`. Its output is `website/dist/`; see [website/README.md](../../website/README.md) for hosting.

## Engineering and historical records (Chinese)

These documents have not yet been translated. Their recorded dates and validation status apply to the versions they describe, including the distinction between 0.9.0 and unreleased changes.

| Topic | Documents |
| --- | --- |
| Product and architecture | [Product requirements](../agent-automation-sandbox-prd.md), [technical design](../agent-automation-sandbox-tech-design.md), [implementation plan](../agent-automation-sandbox-implementation-plan.md) |
| Design | [Design system](../design-system.md), [component development](../../design/README.md) |
| Validation | [Acceptance matrix](../acceptance-matrix.md), [capacity results](../capacity.md), [review checklist](../review-checklist.md), [target-environment runbook](../target-env-runbook.md) |
| Releases | [Release process](../release.md), [0.9.0 release notes](../release-notes/0.9.0.md), [unreleased changes](../release-notes/unreleased.md) |
| Migration | [Name migration](../rename-to-lerryn.md), [repository migration](../repository-migration.md) |
| Technical spikes | [S1: dual-dialect storage](../spikes/s1-dual-dialect-storage.md), [S2: OpenAPI code generation](../spikes/s2-openapi-codegen.md), [S3: stateless MCP](../spikes/s3-mcp-stateless.md), [S4: container execution](../spikes/s4-container-execution.md) |
| API and SDKs | [API conventions](../../api/README.md), [Python](../../runtimes/sdk/python/README.md), [Node](../../runtimes/sdk/node/README.md), [Shell](../../runtimes/sdk/sh/README.md) |

## License

The repository is licensed under [AGPL-3.0](../../LICENSE). `runtimes/sdk/` and `skills/` use Apache-2.0; see their respective license files. Third-party notices are in [THIRD_PARTY_NOTICES](../../THIRD_PARTY_NOTICES).
