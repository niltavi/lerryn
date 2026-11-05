# Day-to-day operations

[简体中文](../operations.md) · [Documentation](README.md)

This guide covers health checks, run diagnosis, backup practice, and runner maintenance for an existing instance. For installation, full configuration, restore, and migration commands, see [deployment and operations](deploy.md). Run `docker compose` commands in the actual deployment directory, using its service names. Authenticate before using `lerryn api`; some operations require administrator access.

## Check the instance and runs

```bash
docker compose ps
curl -fsS http://127.0.0.1:33333/readyz
lerryn discover
lerryn api listRunners
lerryn api listRuns --status failed --status interrupted --limit 20
```

`/readyz` checks database and migration readiness. For a 503, read `failed` in the response before checking server logs. Readiness alone does not mean a runner can execute tasks: inspect recent heartbeats, labels, and available slots too. `listRunners` requires administrator access. Replace the address when accessing the instance through a reverse proxy.

For a specific run, retain its ID and read structured details and bounded logs:

```bash
lerryn runs get <run-id>
lerryn runs logs <run-id> --tail 200
lerryn runs logs <run-id> --follow
```

| Status | Interpretation and next action |
| --- | --- |
| `queued` | Check online runners, required labels, available slots, project concurrency, and task overlap policy |
| `preparing` | Check dependency installation, runtime images, network, credential authorization, and runner logs |
| `running` | Inspect stages, logs, and resource use; client disconnection does not cancel a run |
| `cancelling` | Waiting for container-stop confirmation; remains in this state until confirmed |
| `completed` | Process exited with code 0; inspect `result` for business success |
| `failed`, `timed_out` | Read `error.code`, stage, and logs; correct input, dependencies, or code and retest |
| `interrupted` | Check runner/server connectivity and lease expiration; after recovery verify leftover container cleanup |
| `cancelled`, `skipped` | Read trigger source and error or skip reason to check whether the action or scheduling policy was expected |

Before rerunning tasks that send messages, write data, or call external APIs with side effects, check which actions already happened. Code fixes require a new version, testing, and result inspection. For expired credentials, have their owner update them through the credential page or console. Keep plaintext out of agent conversations and parameters.

Expired logs return `410 expired`, while the run record may remain. Line or size limits can also truncate logs; distinguish expiration, truncation, and read failure. Redaction masks known credentials, but injected values cannot be remotely reclaimed from a process. Explicitly cancel the run when it needs to stop.

## Monitor capacity and retention

Enable metrics as described in [deployment, section 5](deploy.md#5-metrics). Watch queue age, online nodes, occupied slots, and database availability:

| Metric | Action |
| --- | --- |
| `lerryn_run_queue_oldest_seconds > 300` | Check offline runners, insufficient slots, and label matching |
| `lerryn_runners_online == 0` | Check runner processes, connectivity, and credentials |
| `lerryn_runner_slots_in_use` stays near `lerryn_runner_slots` | Adjust slots or add runners based on host CPU, memory, and task limits |
| `lerryn_store_up == 0` | Check database connectivity and migration version |
| `rate(lerryn_runs_lease_expirations_total[10m]) > 0` | Investigate node failures and network interruptions |

Queue, run, and runner gauges are deployment-wide: aggregate multiple servers with `max()`. Requests, lease expirations, and cleanup counters are per-server: aggregate with `sum()`.

Runs, logs, and artifacts are retained indefinitely by default. Set `retention` days and total-size limits for available capacity; log and artifact retention days cannot exceed run retention days. Objects are collected only when no record references them. Each task's latest run is always retained. For planning and the limits of existing measurements, see [deployment, section 9](deploy.md#9-capacity-planning) and [capacity results (Chinese)](../capacity.md).

## Schedule backups and rehearse recovery

Cover the database, object storage, and a separately stored master key. SQLite supports online backup:

```bash
docker compose exec lerryn lerryn backup --output /var/lib/lerryn/backups/lerryn-$(date -u +%Y%m%dT%H%M%SZ).tar.gz
```

The command protects snapshot-referenced objects and excludes the master key. Move the archive to another machine and record its timestamp and version. Mac mini files reside in OrbStack's VM; copy them out with `docker compose cp`.

PostgreSQL requires matching database and object-storage backups. Stop services for both, or preserve snapshot-referenced objects through versioning or storage snapshots. Copying object storage only after `pg_dump` can miss objects collected in between.

Rehearse recovery periodically in an independent environment. Stop destination servers first, follow [backup and restore](deploy.md#6-backup-and-restore), restore data and the master key, then check `/readyz`, task files, logs, artifacts, and credential decryption. Isolate scheduled tasks, webhooks, and notifications before starting the rehearsal environment to prevent duplicate external actions. Keep rehearsals separate from the serving instance.

## Maintain runners and upgrade

Before stopping or upgrading a runner, disable new claims and let active runs finish:

```bash
lerryn api updateRunner --runner-id <runner-id> --disabled=true
lerryn api listRunners
```

Stop the process after that node's `running` reaches 0. After maintenance, check networking, runtimes, data directories, and credentials, then re-enable claims:

```bash
lerryn api updateRunner --runner-id <runner-id> --disabled=false
```

The runner data directory holds its stable ID, enrolled credential, and caches. Preserve identity files when cleaning caches; do not delete the whole directory. Registration tokens enroll new nodes and must be protected as high-privilege credentials. Remove fixed registration tokens from server configuration after all nodes register.

Before upgrading an instance, read release notes and back up, then follow [upgrade and rollback](deploy.md#8-upgrade-and-rollback). The first rollout of protocol 2 and `/sbx/state` requires disabling claims and draining, upgrading all servers and migrations, then upgrading runners and resuming claims. Embedded all-in-one runners follow the same restriction. Rollback needs matching database and object-storage recovery along with the old image.

Cross-run state and protocol 2 are [unreleased changes (Chinese)](../release-notes/unreleased.md). The 0.9.0 review and target-environment validation do not cover them. Follow the release notes for the version actually deployed.
