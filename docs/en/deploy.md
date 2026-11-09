# Deployment and operations

[简体中文](../zh-CN/deploy.md) · [Documentation](README.md)

This guide covers installation, configuration, metrics, backup and restore, migration, upgrades and rollback, capacity planning, and public deployment. Design background is in sections 4.3, 6.1, and 15 of the [technical design (Chinese)](../zh-CN/agent-automation-sandbox-tech-design.md).

Deployment files are in `deploy/`:

| File | Contents |
| --- | --- |
| `deploy/lerryn.example.yaml` | Example of all configuration options |
| `deploy/single-node/` | One all-in-one container, SQLite, local object storage |
| `deploy/multi-node/` | Reference standard topology: Caddy, 2 servers, PostgreSQL, MinIO, 2 runners |
| `deploy/multi-node/runner-host.compose.yaml` | Deployment on a dedicated production runner host |

## 1 Topology selection

| Topology | Components | Database | Object storage | Intended use |
| --- | --- | --- | --- | --- |
| Single node | 1 all-in-one container | SQLite | Local directory | Individuals, small teams, trials |
| Standard | 2 or more servers, multiple runners | PostgreSQL 15 or later | S3-compatible storage | Enterprise deployments |

Both use the same image with different roles and configuration. Before scaling a single-node deployment, use `lerryn migrate-db` to migrate to PostgreSQL (section 7).

The image runs as nonroot (uid 65532) and contains no shell, curl, or package manager. Its health check uses `lerryn healthcheck` to request the local `/readyz` endpoint.

## 2 Single-node deployment

Prerequisite: a Linux host with Docker Engine, with dockerd managing iptables; otherwise task network isolation does not work. Rootless Docker is supported with cgroup v2 and delegated cpu, memory, and pids controllers. Podman is currently unsupported (acceptance matrix, section 6). For Mac mini, see section 2.1.

1. Create the data directory and assign it to uid 65532:

   ```bash
   sudo mkdir -p /var/lib/lerryn && sudo chown 65532:65532 /var/lib/lerryn
   ```

2. In `deploy/single-node/`, copy `.env.example` to `.env` and set:

   - `DOCKER_GID`: output of `stat -c %g /var/run/docker.sock`. Use a numeric GID; the image has no docker group.
   - `LERRYN_PUBLIC_URL`: address used by users and agents, also used to generate console links.
   - `LERRYN_BIND`: defaults to `127.0.0.1`, allowing local access only. Keep it behind a reverse proxy; use `0.0.0.0` for direct external access.

3. Start and check readiness:

   ```bash
   docker compose up -d
   docker compose ps          # STATUS should be healthy
   curl -fsS http://127.0.0.1:33333/readyz
   ```

4. Open the console. The first registered account becomes administrator. Before public access, either complete that registration or create the administrator in advance; see section 11 for the remaining requirements:

   ```bash
   # Writes directly to the database on the server host, without the API
   docker compose exec -T lerryn lerryn admin create admin --password-stdin < admin-password.txt
   ```

   Alternatively, set `auth.initial_admin.username` and `auth.initial_admin.password_file`. At startup the server creates this administrator only if no accounts exist; subsequent starts leave it unchanged. Reset a forgotten password using `lerryn admin reset-password <username>`. This ends the account's sessions while preserving access tokens.

The data directory must be a bind mount with identical host and container paths (`/var/lib/lerryn:/var/lib/lerryn`), not a named volume. The runner sees container paths while the daemon resolves task-container mount sources on the host. Mismatched paths prevent every task from starting.

| Data path | Contents | Backup |
| --- | --- | --- |
| `lerryn.db`, `lerryn.db-wal` | SQLite database | `lerryn backup` |
| `storage/` | Task files and other objects | `lerryn backup` |
| `master.key` | Credential encryption master key | Store separately; section 6.4 |
| `runner/` | Run directories and environment cache | Rebuildable; no backup required |

### 2.1 Mac mini (macOS and OrbStack)

Single-node deployments can run on an Apple silicon Mac mini using [OrbStack](https://orbstack.dev) Docker. **Tasks with external network access (`network: full`) are not isolated from one another**: a task can reach ports opened by another. OrbStack does not enforce `enable_icc=false` on the task network. The runner logs a startup warning (`desktop Docker does not isolate tasks ...`). Other hardening remains effective: nonroot, read-only root filesystem, all capabilities dropped, seccomp, and resource limits. `network: none` tasks still have no egress. This suits a team's own tasks; use Linux when strict isolation between tasks is required. Docker Desktop has not been validated.

OrbStack Docker runs in its own Linux VM. Paths such as `/var/lib/lerryn` that do not exist on the Mac reside in that VM, preserving identical host and container paths. Compose files need no changes, but the data is in the VM rather than the Mac filesystem. Copy backups out with `docker compose cp` (step 6 below).

One-time machine setup:

- Install OrbStack and enable “Start at login.” `restart: unless-stopped` depends on OrbStack running.
- In System Settings → Energy, prevent automatic sleep when the display is off (or use `sudo pmset -a sleep 0`). Enable automatic restart after power failure if needed. Scheduled tasks do not execute during sleep; waking uses downtime-miss handling.
- Assign a fixed LAN address or use the Bonjour name (`<computer-name>.local`, shown in System Settings → General → Sharing).

Run these steps in the Mac mini terminal:

1. **Prepare the image.** Until `ghcr.io/niltavi/lerryn` is published, build from the repository with the Compose default version:

   ```bash
   docker build --build-arg VERSION=v0.9.0 -t ghcr.io/niltavi/lerryn:0.9.0 .
   ```

   Or transfer an existing build: `docker save ghcr.io/niltavi/lerryn:0.9.0 | ssh <Mac-mini> docker load`. After publication, `docker compose up` pulls it automatically.

2. **Prepare the data directory.** It lives in OrbStack's VM. Use a temporary container, without sudo, to create it and assign uid 65532:

   ```bash
   docker run --rm -v /var/lib:/var/lib busybox:1.37.0 sh -c 'mkdir -p /var/lib/lerryn && chown 65532:65532 /var/lib/lerryn'
   ```

3. **Configure `.env`.** In `deploy/single-node/`, copy `.env.example` to `.env`:

   - `DOCKER_GID=0`. OrbStack's container-visible docker.sock belongs to root's group. Verify with `docker run --rm -v /var/run/docker.sock:/var/run/docker.sock busybox:1.37.0 stat -c %g /var/run/docker.sock`.
   - `LERRYN_PUBLIC_URL=http://<computer-name>.local:33333` (or fixed IP), used by LAN users and agents.
   - `LERRYN_BIND=0.0.0.0` for LAN access; retain `127.0.0.1` for local-only use.

4. **Start.**

   ```bash
   docker compose up -d
   docker compose ps                       # STATUS should be healthy
   curl -fsS http://127.0.0.1:33333/readyz
   docker compose logs lerryn | grep -E 'docker daemon ready|desktop Docker'
   ```

   Allow OrbStack's incoming connections if macOS prompts. A client's HTTP proxy may intercept `<computer-name>.local` and return 502; add `.local` to direct-connect rules or `no_proxy`.

5. **Create the administrator.** As in section 2, step 4: register the first account in the console or use `docker compose exec -T lerryn lerryn admin create admin --password-stdin < admin-password.txt`.

6. **Back up.** Use section 6.1's backup command, then copy the VM file to the Mac:

   ```bash
   f=lerryn-$(date -u +%Y%m%dT%H%M%SZ).tar.gz
   docker compose exec lerryn lerryn backup --output /var/lib/lerryn/backups/$f
   mkdir -p ~/lerryn-backups && docker compose cp lerryn:/var/lib/lerryn/backups/$f ~/lerryn-backups/
   ```

   After the first start, copy the master key out and store it separately (section 6.4): `docker compose cp lerryn:/var/lib/lerryn/master.key ~/lerryn-master.key`. Move it off the Mac. Schedule the three backup lines with `crontab -e` if needed.

Upgrade and rollback follow the Linux procedure (section 8). The multi-node topology can also run on OrbStack: on macOS `init-secrets.sh` automatically uses a temporary container without sudo. Multiple nodes on one Mac are useful for experiments, with no production scaling benefit.

## 3 Multi-node deployment

`deploy/multi-node/compose.yaml` places all standard-topology roles on one machine for trials and acceptance testing. In production:

- Put runners on dedicated hosts, each using `runner-host.compose.yaml`. Access to the local Docker API effectively grants host root privileges.
- Use existing enterprise high-availability PostgreSQL and object storage. Remove `postgres`, `minio`, and `minio-init` from Compose and update `server.yaml`.
- An existing load balancer can replace the reverse proxy, subject to the requirements below.

Steps:

1. In `deploy/multi-node/`, copy `.env.example` to `.env` and edit it. For a public domain, set `LERRYN_SITE_ADDRESS` to the domain; Caddy obtains a certificate automatically. Set `LERRYN_PUBLIC_URL` to `https://<domain>`.
2. Run `./init-secrets.sh` to create password files and directories. Under `secrets/` it creates the database password, pgpass, object-storage keys, and runner registration token, plus the master-key directory and two runner data directories. `secrets/` is gitignored.
3. Start the first server so it generates the master key, then start the other services:

   ```bash
   docker compose up -d server-1
   docker compose up -d
   ```

   All servers must use the same master key. With servers on different hosts, copy the first server's key to the `secrets.master_key_file` location on every other server.

4. Start runners. The server accepts `secrets/runner-registration-token` through `server.runner_registration_token_file`. At first startup each runner exchanges it for its own credential, saved as `runner-credential` in its data directory. Later starts no longer need the registration token. Dedicated hosts use the same registration token.

   ```bash
   docker compose --profile runners up -d
   ```

Keep database passwords out of DSNs and environment variables. The `server.yaml` DSN omits the password; pgx reads it from the pgpass file selected by `PGPASSFILE`. Object-storage keys are also read from files.

Metrics are served on each server's `:9090`, reachable only within the `backend` container network. They are neither reverse-proxied nor published to the host. Connect Prometheus to the same network and scrape `server-1:9090` and `server-2:9090`.

**Reverse-proxy requirements:**

- Disable response buffering: run-event SSE, streaming MCP responses, and long-poll run waits require timely forwarding. Use Caddy `flush_interval -1` or Nginx `proxy_buffering off`. If buffering cannot be disabled, set `mcp.json_response: true`.
- Use a read timeout of at least 60 seconds for long polling.
- Health-check `/readyz`. It returns 503 when the database is unreachable or migration versions disagree, keeping traffic away during migration.
- Keep `/metrics` off the internet.
- When a proxy on the same host forwards to `127.0.0.1` while preserving the public Host header, MCP SDK DNS-rebinding protection returns 403. Set `mcp.disable_localhost_protection: true` in this setup.
- Session-authenticated writes accept requests from `server.public_url`. For additional console addresses (for example a Cloudflare Tunnel domain alongside a LAN address), add them to `server.extra_origins` and preserve the original Host header at the proxy.

## 4 Configuration reference

Configuration precedence, from lowest to highest: built-in defaults, YAML (`--config` or `LERRYN_CONFIG`), then `LERRYN_` environment variables. Separate list values with commas, as in `LERRYN_RUNNER_LABELS=intranet,gpu`. Unknown YAML keys prevent startup; all invalid settings are reported together. Settings editable in the console, such as open registration and local password login, live in the database.

Supply sensitive values through files (`*_file` options), keeping them out of configuration and environment values.

The Chinese reference is checked against `server/internal/config` by tests. Keep this English table synchronized with it whenever options are added, renamed, or given new defaults. “Empty” below denotes an unset string or list.

<!-- config-reference:start -->
| Option | Environment variable | Default | Description |
| --- | --- | --- | --- |
| `server.listen` | `LERRYN_SERVER_LISTEN` | `:33333` | Listen address for the API, MCP, and console. |
| `server.public_url` | `LERRYN_SERVER_PUBLIC_URL` | `http://localhost:33333` | Public address used to generate console links; must be an absolute URL. |
| `server.trusted_proxies` | `LERRYN_SERVER_TRUSTED_PROXIES` | Empty | Reverse-proxy CIDRs. Requests from these addresses use X-Forwarded-For for login rate limits, audit, and sessions. Empty uses the direct peer address. The multi-node reference uses Caddy’s 172.30.10.0/24 network. |
| `server.extra_origins` | `LERRYN_SERVER_EXTRA_ORIGINS` | Empty | Additional console origins, as scheme://host[:port], such as a tunnel domain alongside LAN access. Sessions from these origins can write; agent links still use server.public_url. |
| `server.runner_registration_token_file` | `LERRYN_SERVER_RUNNER_REGISTRATION_TOKEN_FILE` | Empty | File containing a runner registration token, accepted alongside administrator-generated tokens. Empty accepts only administrator-generated tokens. |
| `server.require_test_before_activate` | `LERRYN_SERVER_REQUIRE_TEST_BEFORE_ACTIVATE` | `true` | Require a successful test run of the task version before activation. |
| `database.driver` | `LERRYN_DATABASE_DRIVER` | `sqlite` | sqlite or postgres. |
| `database.dsn` | `LERRYN_DATABASE_DSN` | `/var/lib/lerryn/lerryn.db` | SQLite file path or PostgreSQL connection string. Store PostgreSQL passwords in pgpass. |
| `database.max_open_conns` | `LERRYN_DATABASE_MAX_OPEN_CONNS` | `0` | PostgreSQL connection limit per process, retaining the same number of idle connections. 0 means 30. Total connections across servers and maintenance commands must stay below PostgreSQL max_connections. Unused for SQLite. |
| `storage.driver` | `LERRYN_STORAGE_DRIVER` | `local` | local or s3. Multi-node deployments require s3. |
| `storage.path` | `LERRYN_STORAGE_PATH` | `/var/lib/lerryn/storage` | Local object-storage directory. |
| `storage.s3.endpoint` | `LERRYN_STORAGE_S3_ENDPOINT` | Empty | S3-compatible endpoint, such as https://s3.example.com. Required for s3. |
| `storage.s3.region` | `LERRYN_STORAGE_S3_REGION` | Empty | Signing region; empty means us-east-1. |
| `storage.s3.bucket` | `LERRYN_STORAGE_S3_BUCKET` | Empty | Bucket name, required for s3. Operators create it; startup checks availability. |
| `storage.s3.prefix` | `LERRYN_STORAGE_S3_PREFIX` | Empty | Object-key prefix to separate deployments sharing a bucket. |
| `storage.s3.path_style` | `LERRYN_STORAGE_S3_PATH_STYLE` | `false` | Use path-style bucket access. Usually required for self-hosted services such as MinIO. |
| `storage.s3.access_key_id_file` | `LERRYN_STORAGE_S3_ACCESS_KEY_ID_FILE` | Empty | File containing the access key ID; required for s3. |
| `storage.s3.secret_access_key_file` | `LERRYN_STORAGE_S3_SECRET_ACCESS_KEY_FILE` | Empty | File containing the secret access key; required for s3. |
| `secrets.master_key_file` | `LERRYN_SECRETS_MASTER_KEY_FILE` | `/var/lib/lerryn/master.key` | Credential encryption master-key file. Identical on all servers; store separately from backups. |
| `auth.oidc.issuer` | `LERRYN_AUTH_OIDC_ISSUER` | Empty | OIDC provider URL; empty enables built-in accounts only. |
| `auth.oidc.client_id` | `LERRYN_AUTH_OIDC_CLIENT_ID` | Empty | Required when issuer is set. |
| `auth.oidc.client_secret_file` | `LERRYN_AUTH_OIDC_CLIENT_SECRET_FILE` | Empty | File containing the OIDC client secret. |
| `auth.oidc.username_claim` | `LERRYN_AUTH_OIDC_USERNAME_CLAIM` | `preferred_username` | ID Token claim used for usernames on first OIDC login. Conflicts receive a numeric suffix. |
| `auth.initial_admin.username` | `LERRYN_AUTH_INITIAL_ADMIN_USERNAME` | Empty | Administrator created at startup when no accounts exist; ignored once an account exists. |
| `auth.initial_admin.password_file` | `LERRYN_AUTH_INITIAL_ADMIN_PASSWORD_FILE` | Empty | Password file for the initial administrator; the first line is the password. Set together with username. |
| `runner.mode` | `LERRYN_RUNNER_MODE` | `embedded` | embedded (all-in-one) or remote. |
| `runner.labels` | `LERRYN_RUNNER_LABELS` | `default` | Runner labels used by task label requirements. |
| `runner.slots` | `LERRYN_RUNNER_SLOTS` | `4` | Concurrent run slots; at least 1. |
| `runner.allowed_networks` | `LERRYN_RUNNER_ALLOWED_NETWORKS` | `none,full` | Allowed task network policies: none and full. |
| `runner.data_dir` | `LERRYN_RUNNER_DATA_DIR` | `/var/lib/lerryn/runner` | Run directories, environment cache, and runner ID. Must be absolute and identical inside and outside the container. |
| `runner.python_image` | `LERRYN_RUNNER_PYTHON_IMAGE` | `ghcr.io/astral-sh/uv:0.13.6-python3.12-trixie-slim` | Python 3.12 runtime image; must contain python3 and uv. |
| `runner.node_image` | `LERRYN_RUNNER_NODE_IMAGE` | `node:22.23.4-trixie` | Node 22 runtime image; must contain node and npm. The full default includes curl, wget, git, and compilers for downloaded binaries and native modules. node:22.23.4-trixie-slim is smaller but cannot install dependencies needing those tools. |
| `runner.shell_image` | `LERRYN_RUNNER_SHELL_IMAGE` | `bash:5.3.20-alpine3.24` | Shell runtime image; must contain bash 5. |
| `runner.server_url` | `LERRYN_RUNNER_SERVER_URL` | Empty | Server or load-balancer URL for remote runners; required by lerryn runner. Startup warns for non-HTTPS addresses. |
| `runner.registration_token_file` | `LERRYN_RUNNER_REGISTRATION_TOKEN_FILE` | Empty | Registration token file, exchanged on first startup for a credential saved as runner-credential in runner.data_dir. |
| `mirrors.pypi` | `LERRYN_MIRRORS_PYPI` | Empty | Internal PyPI mirror URL. |
| `mirrors.npm` | `LERRYN_MIRRORS_NPM` | Empty | Internal npm mirror URL. |
| `mcp.disable_localhost_protection` | `LERRYN_MCP_DISABLE_LOCALHOST_PROTECTION` | `false` | Set true when a same-host reverse proxy forwards to 127.0.0.1 while preserving the public Host header. |
| `mcp.json_response` | `LERRYN_MCP_JSON_RESPONSE` | `false` | Set true when the proxy buffers streaming responses; returns one JSON response per request. |
| `mcp.max_result_bytes` | `LERRYN_MCP_MAX_RESULT_BYTES` | `0` | Tool-result size limit per call. 0 means the default 32 KiB. |
| `metrics.enabled` | `LERRYN_METRICS_ENABLED` | `false` | Enable Prometheus metrics. |
| `metrics.listen` | `LERRYN_METRICS_LISTEN` | Empty | Separate metrics listen address, such as :9090. Empty mounts /metrics on server.listen. |
| `metrics.token_file` | `LERRYN_METRICS_TOKEN_FILE` | Empty | Bearer token file for scraping. Required when metrics share server.listen. |
| `ai.allowed_networks` | `LERRYN_AI_ALLOWED_NETWORKS` | Empty | Internal CIDRs that AI calls may reach. The default permits public HTTPS destinations only. |
| `notify.allowed_networks` | `LERRYN_NOTIFY_ALLOWED_NETWORKS` | Empty | Allowed internal notification CIDRs, such as 10.20.0.0/16. Default permits public addresses only, rejecting loopback, private, and link-local destinations. |
| `scheduler.max_queued_runs` | `LERRYN_SCHEDULER_MAX_QUEUED_RUNS` | `1000` | Deployment-wide queued-run limit. Cron and webhook triggers exceeding it are skipped with queue_full; API starts return 429 queue_full. 0 is unlimited. |
| `scheduler.max_queued_runs_per_project` | `LERRYN_SCHEDULER_MAX_QUEUED_RUNS_PER_PROJECT` | `200` | Per-project queued-run limit to prevent queue monopolization. 0 is unlimited. |
| `scheduler.project_concurrency` | `LERRYN_SCHEDULER_PROJECT_CONCURRENCY` | `0` | Per-project concurrent-run limit; other runs wait in the queue. 0 is unlimited. |
| `scheduler.missed_after_seconds` | `LERRYN_SCHEDULER_MISSED_AFTER_SECONDS` | `60` | Seconds after a scheduled time before an unprocessed fire is treated as missed during downtime. |
| `scheduler.webhook_rate_per_minute` | `LERRYN_SCHEDULER_WEBHOOK_RATE_PER_MINUTE` | `60` | Accepted requests per webhook trigger per server per minute; excess returns 429 rate_limited. |
| `retention.runs_days` | `LERRYN_RETENTION_RUNS_DAYS` | `0` | Delete ended runs and their logs, artifacts, and events after this many days. Always retain each task’s latest run. 0 disables deletion. |
| `retention.logs_days` | `LERRYN_RETENTION_LOGS_DAYS` | `0` | Remove logs this many days after a run ends, retaining the run and showing expiration. 0 disables cleanup; must not exceed runs_days. |
| `retention.artifacts_days` | `LERRYN_RETENTION_ARTIFACTS_DAYS` | `0` | Remove artifacts this many days after creation, keeping expired listings. 0 disables cleanup; must not exceed runs_days. |
| `retention.logs_max_mib` | `LERRYN_RETENTION_LOGS_MAX_MIB` | `0` | Total archived-log limit in MiB; clean oldest-ended runs first. 0 is unlimited. |
| `retention.artifacts_max_mib` | `LERRYN_RETENTION_ARTIFACTS_MAX_MIB` | `0` | Total artifact-size limit in MiB; clean oldest-created artifacts first. 0 is unlimited. |
| `log.level` | `LERRYN_LOG_LEVEL` | `info` | debug, info, warn, or error. |
| `log.format` | `LERRYN_LOG_FORMAT` | `json` | json or text. |
<!-- config-reference:end -->

## 5 Metrics

Metrics are disabled by default. Enable them in either way:

- Separate listener (recommended): `metrics.enabled: true`, `metrics.listen: ":9090"`. Restrict it to the monitoring network; a token is optional.
- Shared public listener: `metrics.enabled: true`, leave `metrics.listen` empty and set `metrics.token_file`. Scrapes require `Authorization: Bearer <token>` or receive 401.

| Metric | Type | Meaning |
| --- | --- | --- |
| `lerryn_http_requests_total{operation,code}` | counter | Requests by contract operationId and status. Non-API paths use `mcp`, `healthz`, `readyz`, `metrics`; unmatched paths use `unknown` |
| `lerryn_http_request_duration_seconds{operation}` | histogram | Full request duration, including long polling and streaming |
| `lerryn_http_requests_in_flight` | gauge | Requests being processed, including long polling and streaming |
| `lerryn_runs{status}` | gauge | Runs in each state |
| `lerryn_run_queue_depth` | gauge | Queued runs |
| `lerryn_run_queue_oldest_seconds` | gauge | Age of the oldest queued run; detects insufficient or offline runners |
| `lerryn_runners_online` | gauge | Runners seen within 30 seconds |
| `lerryn_runner_slots` | gauge | Total online runner slots |
| `lerryn_runner_slots_in_use` | gauge | Occupied slots: preparing, running, and canceling runs |
| `lerryn_retention_removed_total{kind}` | counter | Runs (`runs`), logs (`logs`), artifacts (`artifacts`), and stored objects (`objects`) removed while this server is leader |
| `lerryn_retention_removed_bytes_total{kind}` | counter | Removed log and artifact bytes, without deduplication adjustment |
| `lerryn_runs_lease_expirations_total` | counter | Runs marked interrupted by expired leases while this server is leader; reconciliation is leader-only, so sum across servers |
| `lerryn_store_up` | gauge | Whether the last scrape could read the database-backed metrics |
| `go_sql_*{db_name}` | gauge, counter | Database pool: open, in-use, and idle connections and wait counts |
| `go_*`, `process_*` | | Go runtime and process metrics |

Queue, run, and runner values are read from the database at scrape time and represent the entire deployment. Every server reports the same values: aggregate with `max()`. Request, lease-expiration, and retention-removal counts are per server: use `sum()`.

Suggested alerts: `lerryn_run_queue_oldest_seconds > 300` (over five minutes queued), `lerryn_runners_online == 0`, `lerryn_store_up == 0`, and `rate(lerryn_runs_lease_expirations_total[10m]) > 0`.

## 6 Backup and restore

| Data | Single node (SQLite) | Standard topology (PostgreSQL) |
| --- | --- | --- |
| Database | `lerryn backup` | `pg_dump` or existing enterprise tools such as PITR |
| Object storage | Included in `lerryn backup` | Storage replication or backup |
| Master key | Store separately | Store separately |

Restore the database and object storage as a matching set. SQLite online backup protects objects referenced by its snapshot, as described below. For PostgreSQL, back up both while services are stopped, or use object versioning or storage snapshots that preserve the required contents. Garbage collection can delete old objects after the database snapshot: simply backing up the database and then copying object storage does not ensure completeness.

### 6.1 SQLite: lerryn backup

```bash
docker compose exec lerryn lerryn backup --output /var/lib/lerryn/backups/lerryn-$(date -u +%Y%m%dT%H%M%SZ).tar.gz
```

- The server can remain running. `VACUUM INTO` copies the database in one read transaction without blocking writes.
- A renewable database lease suspends object garbage collection during backup. Task writes and log/artifact expiration marking continue. If protection fails or a snapshot-referenced object cannot be archived, backup fails and removes the incomplete archive. Diagnose and retry.
- The tar.gz contains a versioned manifest, database copy, and referenced local objects, each checked against SHA-256 while copied. Internal filenames and fields preserve the pre-rename format, so existing backups remain restorable. Unrelated storage files, temporary files, and the backup itself are excluded. File mode is 0640.
- The output is treated as a file path; `?`, `#`, and `%` in directory names are not database connection parameters. Existing output files are never overwritten. Move backups to another machine regularly and remove old local copies.
- The master key is excluded. The command prints its location when finished.

Schedule the command with host cron or a systemd timer.

### 6.2 SQLite: lerryn restore

```bash
docker compose stop lerryn
docker compose run --rm --no-deps lerryn restore --input /var/lib/lerryn/backups/<filename>.tar.gz
docker compose up -d
```

- Stop the server first.
- `database.dsn` accepts ordinary file paths and local `file:` URIs, including `file:///` and `file://localhost/`. Percent-encode spaces, `%`, `?`, and `#` in URI filenames. Restore writes to the actual server database path; `%` and `#` remain literal in ordinary paths. In-memory databases, remote-host URIs, and paths that cannot be parsed safely are rejected before replacement.
- Database, `-wal`, and `-shm` paths must not overlap object storage. Restore checks this before staging so moving storage cannot also move the database. Database path symlinks are resolved before following `..`; existing-data checks and forced restore use the same actual location. Unresolvable links are rejected before swapping; new parent directories can still be created.
- Existing database or object data prevents restore unless `--force` is given. Replaced targets are retained in independent sibling directories named `<original>.pre-restore-<time>-<random-id>/`. Output identifies the actual saved paths. Repeated restores within one second do not overwrite earlier copies. Remove retained directories manually after verification.
- A failed swap is rolled back in reverse order. If rollback also fails, the error reports retained originals and staging paths. Preserve these and recover according to the actual target state before cleaning staging directories.
- Replacement occurs only after extraction and full validation: gzip integrity, database readability, and a migration version no newer than the binary. Truncation, checksum failure, and damaged trailing data reject restore. Older migration versions are upgraded automatically at server startup.
- On a new machine, place the original master key at `secrets.master_key_file`; otherwise stored credentials cannot be decrypted.

### 6.3 PostgreSQL

Back up:

```bash
pg_dump --format=custom --no-owner --dbname "postgres://lerryn@db.example.com:5432/lerryn" \
  --file lerryn-$(date -u +%Y%m%dT%H%M%SZ).dump
```

In the reference deployment, run inside the PostgreSQL container:

```bash
docker compose exec -T postgres pg_dump --format=custom --no-owner -U lerryn -d lerryn > lerryn.dump
```

`pg_dump` provides a consistent database snapshot with the server running. To pair it with object storage, stop services for both backups, or preserve snapshot-referenced objects with versioning or storage snapshots. Running MinIO `mc mirror` after `pg_dump` alone may miss objects garbage-collected in between.

Restore:

1. Stop all servers.
2. Restore to a new database:

   ```bash
   createdb -U postgres -O lerryn lerryn_restored
   pg_restore --no-owner --role=lerryn -U postgres -d lerryn_restored lerryn.dump
   ```

3. Point `database.dsn` at it, or rename after verification: `ALTER DATABASE lerryn RENAME TO lerryn_old; ALTER DATABASE lerryn_restored RENAME TO lerryn;`.
4. Restore object storage and the master key, then start the server. Restore traffic after `/readyz` returns 200.

### 6.4 Master key

The master key encrypts saved credentials. Without it, backed-up ciphertext cannot be decrypted and credentials must be supplied again.

- If the key file is absent on first startup, the server generates it and logs the location.
- Store it separately from database backups, such as in enterprise key management or offline media.
- All servers in the standard topology use the same master key.
- The file contains one key per line: current key first, previous keys below. Use mode 0600; broader permissions produce a startup warning.

Rotation leaves credential ciphertext unchanged and rewraps each credential-version and notification-channel data key:

1. Stop all servers.
2. On a database-connected machine, run `lerryn secrets rotate-key --config <configuration-file>`. It prepends a new master key, retains the old keys, rewraps all data, and prints the processed count.
3. Copy the new key file to every server, back it up separately, then start servers.
4. After a partial failure, rerun `lerryn secrets rewrap`. It processes only data not yet using the current key and is repeatable. Remove old keys only after `rewrap` reports zero remaining entries.

## 7 Export, import, and migration to PostgreSQL

The tar.gz export contains all data tables (one NDJSON file per table) and record-referenced objects. It imports into either database and supports instance migration, SQLite-to-PostgreSQL migration, and A15 recovery verification.

- Credentials are exported encrypted; the destination needs the same master key.
- Migration-version tables are excluded (managed by the destination's migrations), as are lease tables (live coordination state).
- All tables are read from one read-only snapshot. Stop servers before exporting to replace an instance, or later writes will be lost.
- Objects include task files, script-tool files, unexpired artifacts, archived logs, and webhook bodies. Expired artifacts and logs need no objects and are omitted.
- Records referencing missing source objects are still exported; `missing_blobs` in the manifest lists those objects.
- Copying verifies SHA-256 as a stream. Content/hash mismatch, read failure, or close failure rejects export and removes this attempt's output and staging files. Corrupt objects are not treated as missing. Existing outputs are not overwritten.
- Import validates the entire gzip stream. Truncation, checksum failure, or damaged trailing data rolls back the business-data transaction.

```bash
lerryn export --output lerryn-export.tar.gz
lerryn --config new.yaml import --input lerryn-export.tar.gz
```

Import requirements and behavior:

- The destination database must be empty. Import before starting a server with the new configuration.
- Import migrates the destination to the export's version, inserts all records in one transaction, verifies per-table row counts against the manifest, commits, then applies later migrations. Older exports can therefore be imported by newer binaries.
- Each written object is checked in destination storage by length and SHA-256. Correct existing objects can be reused. A corrupted object at a hash path, incomplete read, or validation failure rejects import without committing business records or overwriting existing objects. Inspect destination storage or retry with a fresh directory.
- Exports newer than the binary are rejected.

`lerryn export` and `lerryn import` move an entire deployment. For a single task between deployments or projects, use API `exportTask`/`importTask` or CLI `lerryn tasks export`/`lerryn tasks import` ([CLI guide](cli.md)). Bundles contain task names, version manifests and files, and trigger definitions. They exclude run records, credentials, and webhook tokens and do not require the master key. The importer becomes creator, supplies or authorizes credentials again, and must test imported versions before activation.

### 7.1 SQLite to PostgreSQL

`lerryn migrate-db` combines export and import and compares per-table row counts afterward. Source and destination must be different databases. SQLite configurations using different paths or link aliases are also checked by actual file identity.

1. Prepare an empty PostgreSQL database and `new.yaml` with `database.driver: postgres` and the new object-storage settings.
2. Stop the single-node deployment: `docker compose stop lerryn`.
3. Back up first with `lerryn backup` (section 6.1).
4. Migrate:

   ```bash
   docker compose run --rm --no-deps \
     -v "$PWD/new.yaml:/etc/lerryn/new.yaml:ro" \
     lerryn migrate-db --to-config /etc/lerryn/new.yaml
   ```

   Output lists both databases' row counts and reports `row counts match` on success. Mismatches produce an error, usually because the source server was left running. `--keep-archive` retains the intermediate export; `--work-dir` selects its directory.

5. Copy the master key to the destination and start servers as in section 3. The source database is unchanged; retain it until the destination is verified.

## 8 Upgrade and rollback

First-release server upgrades require brief downtime. Zero-downtime rolling upgrades are deferred to P1.

Upgrade:

1. Read the new release notes (`docs/zh-CN/release-notes/`) and check rollback compatibility.
2. Back up the database (section 6).
3. Stop every server: `docker compose stop lerryn` for single node, or `docker compose stop server-1 server-2` for the standard topology.
4. Change `LERRYN_VERSION` in `.env` and pull: `docker compose pull`.
5. Optionally run migrations separately: `docker compose run --rm --no-deps server-1 migrate`. `lerryn migrate` is repeatable. Concurrent PostgreSQL migration attempts serialize through an advisory lock.
6. Start servers. Startup automatically applies pending migrations. Wait for `/readyz` to return 200.
7. Upgrade runners before resuming claims. Protocol 2 pins task-state input at claim time. Version 1 can finish already-claimed runs but cannot claim new work; new runners do not downgrade to old servers.

For the first upgrade to protocol 2 with `/sbx/state`, disable old nodes in administration and wait for active runs to drain, then stop old runners and all servers. Upgrade every server and apply migrations before starting new runners. Confirm their registered versions before re-enabling nodes. All-in-one embedded runners must also wait until every server is upgraded before claiming. Keep no old server that still assigns work to version 1 runners. Disabled nodes continue to show occupied slots until drained. Completing old runs is a compatibility boundary; this first rollout does not support zero-downtime rolling upgrades.

Migration `20261110010000_task_state` adds the current task snapshot and run input/output references. Existing instances initially read empty state; old working directories are not converted. Reversing the migration removes state references, so rolling back only the binary cannot preserve new state. Retain a complete pre-upgrade database, object-storage, and master-key backup.

Rollback:

- The server and `lerryn migrate` refuse databases with newer migration versions. Restore a pre-upgrade database backup along with the old image.
- Stop every server, restore the pre-upgrade backup (`lerryn restore --force` for SQLite; section 6.3 for PostgreSQL), revert `LERRYN_VERSION`, and start.
- Post-upgrade data will be lost. If it must be retained, use `lerryn export` before rollback and reconcile it manually afterward.

## 9 Capacity planning

PRD section 8 targets 100 tasks and 5 concurrent lightweight runs for single node, and provisionally 1,000 tasks and 50 concurrent runs for multi-node. Control-plane measurements on a development machine are recorded in [capacity results (Chinese)](../zh-CN/capacity.md). Those measurements exclude container overhead; retest on the target Linux host before a production release. Planning considerations:

- **SQLite has one writer.** Log writes, heartbeats, events, and claims share the process-local write queue. Use single node for fewer than 10 concurrent runs with modest log volume. Migrate to PostgreSQL if queue age (`lerryn_run_queue_oldest_seconds`) or request latency rises persistently.
- **Runner slots.** `runner.slots` limits concurrent runs on a runner. Each container separately limits CPU and memory. Estimate slots from host resources divided by per-task limits, reserving capacity for dockerd and dependency installation. Add runners when `lerryn_runner_slots_in_use` stays near `lerryn_runner_slots`.
- **Runner disk.** Environment caches are retained by environment ID. Budget disk by distinct environments multiplied by environment size.
- **Servers.** Stateless servers scale with requests. Each has a connection pool of up to 30 by default (`database.max_open_conns`). PostgreSQL `max_connections` must cover every server and maintenance command. Observe usage with `go_sql_open_connections`.
- **Logs.** A run defaults to at most 200,000 lines or 100 MiB. Live log chunks go to the database; log-heavy deployments need adequate database disk and write capacity.
- **Object storage.** Task files, artifacts, and archived logs are deduplicated by content hash.
- **Retention.** Runs, logs, and artifacts are retained indefinitely by default. Configure `retention` days and total-size limits for available disk, such as logs for 30 days, artifacts for 90, and runs for 365, with `logs_max_mib` and `artifacts_max_mib` as total limits. The leader first cleans up 30 seconds after startup, then every 10 minutes. Expired logs and artifacts are marked in the console and API; run records remain. Each task's latest run is always retained. Each cleanup creates a `retention.cleanup` audit entry. Objects are deleted only when no record references them.

## 10 Troubleshooting

| Symptom | Check |
| --- | --- |
| Container restarts repeatedly | `docker compose logs`. Startup reports all invalid configuration options together. All-in-one exits with a reason if Docker is unreachable or default hardening cannot be applied |
| `/readyz` returns 503 | Response `failed` lists failing checks. `database` means unreachable database, missing migrations, or a database newer than the binary |
| Every task remains queued | `lerryn_runners_online`, runner logs, and for single node `DOCKER_GID` and identical host/container bind-mount paths |
| Run marked interrupted | Runner disconnected from the server for more than 30 seconds; inspect `lerryn_runs_lease_expirations_total` and runner logs |
| MCP client receives 403 | Same-host reverse proxy forwarding to 127.0.0.1; see section 3 |
| Console reads work but writes report a different deployment address | Console origin differs from `server.public_url`. Use that address or add this origin to `server.extra_origins` and restart; see section 3 |

## 11 Public deployment

Complete these steps before allowing internet access to the console, API, or MCP. Open registration defaults to enabled. Before the first registration, anyone who can open the console can become administrator (technical design, section 20).

1. **Create an administrator before public access.** Choose one:

   - Configure `auth.initial_admin.username` and `auth.initial_admin.password_file` for creation on first startup (recommended).
   - Run `lerryn admin create <username> --password-stdin` on the server host (section 2, step 4).
   - Keep local binding (`LERRYN_BIND=127.0.0.1`), open the console through an SSH tunnel, register, then enable external binding.

2. **Disable open registration.** An administrator can turn it off in console administration, or use an administrator access token:

   ```bash
   lerryn api updateSettings --open-registration=false
   ```

   Verify without authentication:

   ```bash
   curl -fsS https://lerryn.example.com/api/v1/discover | jq '.capabilities.registration_open'   # false
   ```

   OIDC first login still creates accounts regardless of open registration. Restrict application assignment at the provider to intended users or groups. After confirming OIDC works, local password login can be disabled (`--local-password-login=false`); administrators retain password access for recovery.

3. **Use HTTPS.** Set `server.public_url` to `https://` and terminate TLS at the reverse proxy (Caddy in section 3 obtains certificates automatically). Use HTTPS for remote `runner.server_url` too; non-HTTPS produces a startup warning.

4. **Keep metrics private.** Use a separate `metrics.listen` (such as `:9090`) restricted to monitoring, or set `metrics.token_file` on a shared public listener. Do not proxy `/metrics`. Verify that `curl -s -o /dev/null -w '%{http_code}' https://lerryn.example.com/metrics` does not return 200.

5. **Back up the master key.** Copy `secrets.master_key_file` (default `/var/lib/lerryn/master.key`) to offline media or enterprise key management. Store it separately from database backups with mode 0600. Losing it makes saved credentials undecryptable; they must be supplied again (section 6.4).

6. **Keep runners private and protect registration tokens as root credentials.** Runner hosts need no inbound ports. Registration tokens can enroll runners that claim tasks, receive credentials, and read/write run results; disclosure has impact comparable to root access on a runner host. Keep the token file (`server.runner_registration_token_file`) only on server and runner hosts with mode 0600. Remove it from server configuration after all runners register. Registration tokens cannot replace working runners.

7. **Retain default notification egress.** Empty `notify.allowed_networks` permits only public destinations, rejecting loopback, private, and link-local addresses. Allow only the required CIDRs for internal recipients.

After enabling access, check with a fresh browser session that registration is unavailable, metrics are inaccessible, and the console uses HTTPS.
