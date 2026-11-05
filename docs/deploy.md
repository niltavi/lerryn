# Lerryn 部署与运维

[English](en/deploy.md) · [文档目录](README.zh-CN.md)

这份文档写给部署和维护 Lerryn 的人：安装、配置、指标、备份恢复、迁移、升级回退、容量规划和公网部署。设计依据见技术设计第 4.3、6.1、15 节。

部署文件都在 `deploy/` 下：

| 文件 | 内容 |
| --- | --- |
| `deploy/lerryn.example.yaml` | 全部配置项的示例 |
| `deploy/single-node/` | 单机拓扑：一个 all-in-one 容器，SQLite，本地对象存储 |
| `deploy/multi-node/` | 标准拓扑的参考部署：Caddy、2 个 server、PostgreSQL、MinIO、2 个 runner |
| `deploy/multi-node/runner-host.compose.yaml` | 生产环境中专用 runner 主机上的部署 |

## 1 拓扑选择

| 拓扑 | 组成 | 数据库 | 对象存储 | 适用 |
| --- | --- | --- | --- | --- |
| 单机 | 1 个 all-in-one 容器 | SQLite | 本地目录 | 个人、小团队、试用 |
| 标准 | 2 个以上 server、若干 runner | PostgreSQL 15 及以上 | S3 兼容存储 | 企业部署 |

两种拓扑运行同一个镜像，只是启动的角色和配置不同。单机部署以后要扩容，先用 `lerryn migrate-db` 迁到 PostgreSQL（第 7 节）。

镜像以 nonroot 用户（uid 65532）运行，里面没有 shell、curl 和包管理器。容器健康检查使用 `lerryn healthcheck`，它请求本机的 `/readyz`。

## 2 单机部署

前提：一台装有 Docker Engine 的 Linux 主机，dockerd 自己管理 iptables（否则任务之间的网络隔离不生效）。rootless Docker 也可以，需要 cgroup v2 并委派 cpu、memory、pids 控制器；Podman 目前不支持（验收矩阵第 6 节）。在 Mac mini 上部署见第 2.1 节。

1. 准备数据目录，交给 uid 65532：

   ```bash
   sudo mkdir -p /var/lib/lerryn && sudo chown 65532:65532 /var/lib/lerryn
   ```

2. 进入 `deploy/single-node/`，复制 `.env.example` 为 `.env`，填写：

   - `DOCKER_GID`：`stat -c %g /var/run/docker.sock` 的输出。镜像里没有 docker 组，只能写数字 GID。
   - `LERRYN_PUBLIC_URL`：用户和 Agent 访问的地址，返回给 Agent 的控制台链接由它生成。
   - `LERRYN_BIND`：默认 `127.0.0.1`，只允许本机访问。前面有反向代理时保持不变；直接对外提供服务时改成 `0.0.0.0`。

3. 启动并确认就绪：

   ```bash
   docker compose up -d
   docker compose ps          # STATUS 显示 healthy
   curl -fsS http://127.0.0.1:33333/readyz
   ```

4. 打开控制台，第一个注册的账号成为管理员。公网部署时先完成首次注册，或者预先创建管理员，二选一，其余必做事项见第 11 节：

   ```bash
   # 在 server 所在主机上直接写数据库，不经过 API
   docker compose exec -T lerryn lerryn admin create admin --password-stdin < admin-password.txt
   ```

   也可以设置 `auth.initial_admin.username` 和 `auth.initial_admin.password_file`，server 启动时发现还没有任何账号就创建这个管理员，之后的启动不再改动。忘记密码时用 `lerryn admin reset-password <用户名>` 重置，该账号的会话随之结束，访问令牌不受影响。

数据目录必须是同路径的绑定挂载（`/var/lib/lerryn:/var/lib/lerryn`），不能换成命名卷。runner 在容器里看到的是容器内的路径，而 daemon 创建任务容器时按宿主机路径解析挂载源，两边不一致时任务一个都启动不了。

数据目录里的内容：

| 路径 | 内容 | 备份 |
| --- | --- | --- |
| `lerryn.db`、`lerryn.db-wal` | SQLite 数据库 | `lerryn backup` |
| `storage/` | 任务文件等对象 | `lerryn backup` |
| `master.key` | 凭证加密主密钥 | 单独保管，见第 6.4 节 |
| `runner/` | 运行目录和环境缓存 | 不需要，可以重建 |

### 2.1 Mac mini（macOS + OrbStack）

单机拓扑可以部署在一台 Mac mini（Apple 芯片）上，用 [OrbStack](https://orbstack.dev) 提供的 Docker。和 Linux 主机相比有一个差别要先接受：**开了外网（`network: full`）的任务之间不隔离**，一个任务能访问另一个任务监听的端口。OrbStack 的网络不执行任务网络的 `enable_icc=false`，runner 启动时会在日志里告警（`desktop Docker does not isolate tasks ...`）。容器的其余加固（非 root、只读根文件系统、去掉全部 capabilities、seccomp、资源上限）照常生效，`network: none` 的任务也照常没有任何出口。适合一个团队自己用；需要任务之间严格隔离时，改用 Linux 主机。Docker Desktop 没有验证过。

OrbStack 的 Docker 跑在它自己的 Linux 虚拟机里。compose 文件中 `/var/lib/lerryn` 这类 Mac 上不存在的路径，会落在这个虚拟机里，容器内外看到的是同一个路径，满足同路径挂载的要求。所以 compose 文件不用改，只是数据在虚拟机里，不在 Mac 的文件系统上，备份时要用 `docker compose cp` 拷出来（见下面第 6 步）。

一次性的机器设置：

- 安装 OrbStack，在设置里打开"登录时启动"（Start at login）。`restart: unless-stopped` 依赖 OrbStack 在运行。
- 系统设置 → 能源 → 打开"显示器关闭时防止自动进入睡眠"（或 `sudo pmset -a sleep 0`），并按需打开"停电后自动启动"。睡眠期间定时任务不会执行，醒来后按停机错过处理。
- 给 Mac mini 一个固定的局域网地址，或者用它的 Bonjour 名字（`<电脑名>.local`，在"系统设置 → 通用 → 共享"里查看）。

步骤，都在 Mac mini 的终端里执行：

1. **准备镜像。** `ghcr.io/niltavi/lerryn` 发布之前，在仓库目录里构建，标签与 compose 的默认版本一致：

   ```bash
   docker build --build-arg VERSION=v0.9.0 -t ghcr.io/niltavi/lerryn:0.9.0 .
   ```

   也可以在已经构建好的机器上传过去：`docker save ghcr.io/niltavi/lerryn:0.9.0 | ssh <Mac mini> docker load`。镜像发布后这一步可以省掉，`docker compose up` 会自己拉取。

2. **准备数据目录。** 目录在 OrbStack 的虚拟机里，所以不用 sudo，用一个临时容器创建并交给 uid 65532：

   ```bash
   docker run --rm -v /var/lib:/var/lib busybox:1.37.0 sh -c 'mkdir -p /var/lib/lerryn && chown 65532:65532 /var/lib/lerryn'
   ```

3. **填写 `.env`。** 进入 `deploy/single-node/`，复制 `.env.example` 为 `.env`：

   - `DOCKER_GID=0`。OrbStack 在容器里提供的 docker.sock 属于 root 组，可以用 `docker run --rm -v /var/run/docker.sock:/var/run/docker.sock busybox:1.37.0 stat -c %g /var/run/docker.sock` 确认。
   - `LERRYN_PUBLIC_URL=http://<电脑名>.local:33333`（或固定 IP），局域网里的人和 Agent 都用这个地址。
   - `LERRYN_BIND=0.0.0.0`，让局域网能访问。只在本机用时保持 `127.0.0.1`。

4. **启动。**

   ```bash
   docker compose up -d
   docker compose ps                       # STATUS 显示 healthy
   curl -fsS http://127.0.0.1:33333/readyz
   docker compose logs lerryn | grep -E 'docker daemon ready|desktop Docker'
   ```

   第一次从局域网访问时，macOS 可能询问是否允许 OrbStack 接受传入连接，选允许。客户端的电脑开着 HTTP 代理时，`<电脑名>.local` 可能被代理接走（返回 502），把 `.local` 加进代理的直连名单或 `no_proxy`。

5. **建管理员。** 和第 2 节第 4 步相同：在控制台注册第一个账号，或 `docker compose exec -T lerryn lerryn admin create admin --password-stdin < admin-password.txt`。

6. **备份。** 备份命令和第 6.1 节相同，备份文件写在虚拟机里的数据目录下，再拷到 Mac 上：

   ```bash
   f=lerryn-$(date -u +%Y%m%dT%H%M%SZ).tar.gz
   docker compose exec lerryn lerryn backup --output /var/lib/lerryn/backups/$f
   mkdir -p ~/lerryn-backups && docker compose cp lerryn:/var/lib/lerryn/backups/$f ~/lerryn-backups/
   ```

   主密钥第一次启动后就拷出来，单独保管（第 6.4 节）：`docker compose cp lerryn:/var/lib/lerryn/master.key ~/lerryn-master.key`，然后移到 Mac 以外的地方。定时备份可以用 `crontab -e` 执行上面三行。

升级、回退和 Linux 主机相同（第 8 节）。多节点拓扑也能在 OrbStack 上启动（`init-secrets.sh` 在 macOS 上会自动改在临时容器里执行，不需要 sudo），但一台 Mac 上跑多节点没有实际意义，只用于试验。

## 3 多节点部署

`deploy/multi-node/compose.yaml` 把标准拓扑的所有角色放在一台机器上，用于试用和验收。生产环境里：

- runner 放在专用主机上，每台用 `runner-host.compose.yaml` 启动一个 runner。runner 需要访问本机的 Docker API，这基本等于拥有主机的 root 权限。
- PostgreSQL 和对象存储使用企业已有的高可用服务，删掉 compose 文件中的 `postgres`、`minio`、`minio-init`，改 `server.yaml`。
- 反向代理可以换成企业已有的负载均衡，要求见本节末尾。

步骤：

1. 进入 `deploy/multi-node/`，复制 `.env.example` 为 `.env` 并修改。用域名对外提供服务时，`LERRYN_SITE_ADDRESS` 填域名，Caddy 会自动申请证书，`LERRYN_PUBLIC_URL` 改成 `https://<域名>`。
2. 生成口令文件和目录：`./init-secrets.sh`。它在 `secrets/` 下生成数据库口令、`pgpass`、对象存储的访问密钥和 runner 注册令牌，并创建主密钥目录和两个 runner 的数据目录。`secrets/` 已在 `.gitignore` 中。
3. 先启动第一个 server，让它生成主密钥，再启动其余服务：

   ```bash
   docker compose up -d server-1
   docker compose up -d
   ```

   所有 server 必须使用同一个主密钥。生产环境中各 server 在不同主机上时，把第一个 server 生成的主密钥复制到其他 server 的 `secrets.master_key_file` 位置。

4. 启动 runner。server 经 `server.runner_registration_token_file` 接受 `secrets/runner-registration-token` 中的注册令牌，runner 第一次启动时用它换取自己的凭证，存在各自数据目录的 `runner-credential` 文件里，之后不再需要注册令牌。专用 runner 主机上用同一个注册令牌：

   ```bash
   docker compose --profile runners up -d
   ```

数据库口令不写在 DSN 和环境变量里：`server.yaml` 的 DSN 不带口令，pgx 按环境变量 `PGPASSFILE` 指向的 pgpass 文件读取口令。对象存储的密钥也通过文件读取。

指标在 server 的 `:9090` 上提供，只在容器网络 `backend` 内可达，不经过反向代理，也不映射到宿主机。Prometheus 接入同一网络后抓取 `server-1:9090`、`server-2:9090`。

**反向代理的要求。**

- 不缓冲响应：运行事件的 SSE、MCP 的流式响应和运行等待的长轮询都依赖及时转发。Caddy 用 `flush_interval -1`，Nginx 用 `proxy_buffering off`。代理无法关闭缓冲时，把 `mcp.json_response` 设为 `true`。
- 读超时不短于 60 秒，长轮询会保持连接。
- 健康检查用 `/readyz`。它在数据库不可达或迁移版本不一致时返回 503，迁移进行中的 server 不会收到流量。
- 不把 `/metrics` 暴露到公网。
- 代理和 server 在同一台主机、转发到 `127.0.0.1` 并保留公网 Host 头时，MCP SDK 的 DNS 重绑定保护会返回 403，需要把 `mcp.disable_localhost_protection` 设为 `true`。
- 用会话发起的写操作只接受来自 `server.public_url` 的请求。同一个部署还要从另一个地址打开控制台（例如局域网地址之外再加一个 Cloudflare Tunnel 域名）时，把那个地址写进 `server.extra_origins`，并让代理保留原始 Host 头。

## 4 配置参考

配置来自三层，后面的覆盖前面的：内置默认值、YAML 配置文件（`--config` 或环境变量 `LERRYN_CONFIG`）、`LERRYN_` 前缀的环境变量。列表类配置项在环境变量里用逗号分隔，例如 `LERRYN_RUNNER_LABELS=intranet,gpu`。配置文件中出现未知的键时启动直接报错，所有无效项一次报告完。开放注册、本地密码登录等可以在后台修改的设置存在数据库里，不在这里。

敏感内容一律通过文件提供（`*_file` 配置项），不直接写在配置文件和环境变量里。

下表由 `server/internal/config` 的测试与代码核对，新增或改名的配置项必须同步更新这张表，否则测试失败。

<!-- config-reference:start -->
| 配置项 | 环境变量 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `server.listen` | `LERRYN_SERVER_LISTEN` | `:33333` | API、MCP 和控制台的监听地址 |
| `server.public_url` | `LERRYN_SERVER_PUBLIC_URL` | `http://localhost:33333` | 对外访问地址，用来生成控制台链接，必须是绝对 URL |
| `server.trusted_proxies` | `LERRYN_SERVER_TRUSTED_PROXIES` | （空） | 反向代理所在的网段（CIDR 列表）。来自这些地址的请求按 `X-Forwarded-For` 取客户端地址，用于登录限速、审计和会话记录；留空时一律用直接连接的地址。参考部署的多节点拓扑填了 Caddy 所在的 `172.30.10.0/24` |
| `server.extra_origins` | `LERRYN_SERVER_EXTRA_ORIGINS` | （空） | 控制台的其他访问地址，只写 `协议://主机[:端口]`，例如局域网部署前面再接一个隧道域名。从这些地址登录后同样可以修改；返回给 Agent 的链接仍用 `server.public_url` |
| `server.runner_registration_token_file` | `LERRYN_SERVER_RUNNER_REGISTRATION_TOKEN_FILE` | （空） | 文件内容可作为 runner 注册令牌，与管理员生成的令牌并用；留空时只接受管理员生成的令牌 |
| `server.require_test_before_activate` | `LERRYN_SERVER_REQUIRE_TEST_BEFORE_ACTIVATE` | `true` | 激活前必须测试：任务版本没有成功的测试运行时不能激活 |
| `database.driver` | `LERRYN_DATABASE_DRIVER` | `sqlite` | `sqlite` 或 `postgres` |
| `database.dsn` | `LERRYN_DATABASE_DSN` | `/var/lib/lerryn/lerryn.db` | SQLite 为文件路径；PostgreSQL 为连接串，口令建议放在 pgpass 文件中 |
| `database.max_open_conns` | `LERRYN_DATABASE_MAX_OPEN_CONNS` | `0` | 每个进程连接 PostgreSQL 的连接数上限，空闲连接保留同样多；`0` 表示 30。所有 server 和维护命令的总数要低于 PostgreSQL 的 `max_connections`。SQLite 不使用 |
| `storage.driver` | `LERRYN_STORAGE_DRIVER` | `local` | `local` 或 `s3`；多节点必须使用 `s3` |
| `storage.path` | `LERRYN_STORAGE_PATH` | `/var/lib/lerryn/storage` | 本地对象存储的目录 |
| `storage.s3.endpoint` | `LERRYN_STORAGE_S3_ENDPOINT` | （空） | S3 兼容存储的地址，例如 `https://s3.example.com`；`s3` 时必填 |
| `storage.s3.region` | `LERRYN_STORAGE_S3_REGION` | （空） | 签名用的区域；留空时为 `us-east-1` |
| `storage.s3.bucket` | `LERRYN_STORAGE_S3_BUCKET` | （空） | 桶名，`s3` 时必填。桶由运维人员创建，server 启动时检查它可用 |
| `storage.s3.prefix` | `LERRYN_STORAGE_S3_PREFIX` | （空） | 对象键前缀，多个部署共用一个桶时区分 |
| `storage.s3.path_style` | `LERRYN_STORAGE_S3_PATH_STYLE` | `false` | 用路径风格访问桶；MinIO 等自建服务一般需要设为 `true` |
| `storage.s3.access_key_id_file` | `LERRYN_STORAGE_S3_ACCESS_KEY_ID_FILE` | （空） | 访问密钥 ID 所在的文件，`s3` 时必填 |
| `storage.s3.secret_access_key_file` | `LERRYN_STORAGE_S3_SECRET_ACCESS_KEY_FILE` | （空） | 访问密钥所在的文件，`s3` 时必填 |
| `secrets.master_key_file` | `LERRYN_SECRETS_MASTER_KEY_FILE` | `/var/lib/lerryn/master.key` | 凭证加密主密钥文件；所有 server 必须相同，和备份分开保管 |
| `auth.oidc.issuer` | `LERRYN_AUTH_OIDC_ISSUER` | （空） | OIDC 提供方地址；留空时只有内置账号 |
| `auth.oidc.client_id` | `LERRYN_AUTH_OIDC_CLIENT_ID` | （空） | 设置了 issuer 时必填 |
| `auth.oidc.client_secret_file` | `LERRYN_AUTH_OIDC_CLIENT_SECRET_FILE` | （空） | OIDC 客户端密钥文件 |
| `auth.oidc.username_claim` | `LERRYN_AUTH_OIDC_USERNAME_CLAIM` | `preferred_username` | 首次单点登录建立账号时，用作用户名的 ID Token 声明；冲突时加数字后缀 |
| `auth.initial_admin.username` | `LERRYN_AUTH_INITIAL_ADMIN_USERNAME` | （空） | 部署中还没有任何账号时，server 启动时创建的管理员用户名；已有账号时忽略 |
| `auth.initial_admin.password_file` | `LERRYN_AUTH_INITIAL_ADMIN_PASSWORD_FILE` | （空） | 上述管理员的密码文件，第一行为密码；与用户名同时设置 |
| `runner.mode` | `LERRYN_RUNNER_MODE` | `embedded` | `embedded`（all-in-one）或 `remote` |
| `runner.labels` | `LERRYN_RUNNER_LABELS` | `default` | runner 的标签，任务可以按标签选择 runner |
| `runner.slots` | `LERRYN_RUNNER_SLOTS` | `4` | 同时执行的运行数，至少为 1 |
| `runner.allowed_networks` | `LERRYN_RUNNER_ALLOWED_NETWORKS` | `none,full` | 允许任务使用的网络策略：`none`、`full` |
| `runner.data_dir` | `LERRYN_RUNNER_DATA_DIR` | `/var/lib/lerryn/runner` | 运行目录、环境缓存和 runner ID；必须是绝对路径，容器内外路径一致 |
| `runner.python_image` | `LERRYN_RUNNER_PYTHON_IMAGE` | `ghcr.io/astral-sh/uv:0.13.6-python3.12-trixie-slim` | Python 3.12 任务的运行时镜像，需包含 python3 和 uv |
| `runner.node_image` | `LERRYN_RUNNER_NODE_IMAGE` | `node:22.23.4-trixie` | Node 22 任务的运行时镜像，需包含 node 和 npm。默认用完整版镜像，带 curl、wget、git 和编译工具链，依赖安装时下载二进制或编译原生模块都能成功；想要更小的镜像可改为 `node:22.23.4-trixie-slim`，代价是这类依赖装不上 |
| `runner.shell_image` | `LERRYN_RUNNER_SHELL_IMAGE` | `bash:5.3.20-alpine3.24` | Shell 任务的运行时镜像，需包含 bash 5 |
| `runner.server_url` | `LERRYN_RUNNER_SERVER_URL` | （空） | remote runner 连接的 server 或负载均衡地址，`lerryn runner` 必填；不是 HTTPS 时启动日志会警告 |
| `runner.registration_token_file` | `LERRYN_RUNNER_REGISTRATION_TOKEN_FILE` | （空） | 注册令牌所在的文件。runner 第一次启动时用它换取凭证，凭证存在 `runner.data_dir` 下的 `runner-credential` |
| `mirrors.pypi` | `LERRYN_MIRRORS_PYPI` | （空） | 内部 PyPI 镜像地址 |
| `mirrors.npm` | `LERRYN_MIRRORS_NPM` | （空） | 内部 npm 镜像地址 |
| `mcp.disable_localhost_protection` | `LERRYN_MCP_DISABLE_LOCALHOST_PROTECTION` | `false` | 同机反向代理转发到 127.0.0.1 并保留公网 Host 头时设为 `true` |
| `mcp.json_response` | `LERRYN_MCP_JSON_RESPONSE` | `false` | 代理会缓冲流式响应时设为 `true`，每个请求返回一个 JSON |
| `mcp.max_result_bytes` | `LERRYN_MCP_MAX_RESULT_BYTES` | `0` | 单次工具调用返回内容的上限，0 表示默认的 32 KiB |
| `metrics.enabled` | `LERRYN_METRICS_ENABLED` | `false` | 是否提供 Prometheus 指标 |
| `metrics.listen` | `LERRYN_METRICS_LISTEN` | （空） | 指标的单独监听地址，例如 `:9090`；留空时挂在 `server.listen` 的 `/metrics` 上 |
| `metrics.token_file` | `LERRYN_METRICS_TOKEN_FILE` | （空） | 抓取指标需要的 Bearer 令牌文件；挂在 `server.listen` 上时必填 |
| `notify.allowed_networks` | `LERRYN_NOTIFY_ALLOWED_NETWORKS` | （空） | 通知允许发往的内网网段（CIDR），例如 `10.20.0.0/16`。默认只发往公网地址，回环、私有和链路本地地址一律拒绝 |
| `scheduler.max_queued_runs` | `LERRYN_SCHEDULER_MAX_QUEUED_RUNS` | `1000` | 整个部署排队中的运行上限。超过时定时和 webhook 触发记为已跳过（`queue_full`），API 发起返回 429 `queue_full`；0 表示不限 |
| `scheduler.max_queued_runs_per_project` | `LERRYN_SCHEDULER_MAX_QUEUED_RUNS_PER_PROJECT` | `200` | 单个项目排队中的运行上限，避免一个项目占满队列；0 表示不限 |
| `scheduler.project_concurrency` | `LERRYN_SCHEDULER_PROJECT_CONCURRENCY` | `0` | 单个项目同时执行的运行上限，其余在队列中等待；0 表示不限 |
| `scheduler.missed_after_seconds` | `LERRYN_SCHEDULER_MISSED_AFTER_SECONDS` | `60` | 计划时间过去多少秒仍未处理就视为停机错过 |
| `scheduler.webhook_rate_per_minute` | `LERRYN_SCHEDULER_WEBHOOK_RATE_PER_MINUTE` | `60` | 每个 webhook 触发器每分钟在每个 server 上接受的请求数，超出返回 429 `rate_limited` |
| `retention.runs_days` | `LERRYN_RETENTION_RUNS_DAYS` | `0` | 已结束的运行在结束多少天后删除，日志、产物和事件一并删除；每个任务最近的一次运行始终保留。0 表示不删除 |
| `retention.logs_days` | `LERRYN_RETENTION_LOGS_DAYS` | `0` | 已结束运行的日志在结束多少天后清理，运行记录保留并显示日志已清理。0 表示不清理；不能大于 `runs_days` |
| `retention.artifacts_days` | `LERRYN_RETENTION_ARTIFACTS_DAYS` | `0` | 产物在生成多少天后清理，产物仍会列出并标为已清理。0 表示不清理；不能大于 `runs_days` |
| `retention.logs_max_mib` | `LERRYN_RETENTION_LOGS_MAX_MIB` | `0` | 已归档日志的总量上限（MiB），超出时从最早结束的运行开始清理日志。0 表示不限 |
| `retention.artifacts_max_mib` | `LERRYN_RETENTION_ARTIFACTS_MAX_MIB` | `0` | 产物的总量上限（MiB），按产物大小合计，超出时从最早生成的产物开始清理。0 表示不限 |
| `log.level` | `LERRYN_LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error` |
| `log.format` | `LERRYN_LOG_FORMAT` | `json` | `json` 或 `text` |
<!-- config-reference:end -->

## 5 指标

指标默认关闭。两种开启方式：

- 单独的监听地址（推荐）：`metrics.enabled: true`，`metrics.listen: ":9090"`。这个地址只开放给监控网络，可以不设令牌。
- 挂在公开监听地址上：`metrics.enabled: true`，`metrics.listen` 留空，`metrics.token_file` 指向令牌文件。抓取时带 `Authorization: Bearer <令牌>`，否则返回 401。

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `lerryn_http_requests_total{operation,code}` | counter | 请求数，按契约中的 operationId 和状态码；非 API 路径为 `mcp`、`healthz`、`readyz`、`metrics`，未知路径为 `unknown` |
| `lerryn_http_request_duration_seconds{operation}` | histogram | 请求耗时；长轮询和流式响应按完整时长计 |
| `lerryn_http_requests_in_flight` | gauge | 正在处理的请求，包括长轮询和流式响应 |
| `lerryn_runs{status}` | gauge | 各状态的运行数 |
| `lerryn_run_queue_depth` | gauge | 排队中的运行数 |
| `lerryn_run_queue_oldest_seconds` | gauge | 最早一条排队运行已等待的秒数，用来发现 runner 不足或离线 |
| `lerryn_runners_online` | gauge | 最近 30 秒内出现过的 runner 数 |
| `lerryn_runner_slots` | gauge | 在线 runner 的槽位总数 |
| `lerryn_runner_slots_in_use` | gauge | 占用槽位的运行数（准备中、运行中、取消中） |
| `lerryn_retention_removed_total{kind}` | counter | 本 server 担任主节点期间按保留策略删除的运行（`runs`）、日志（`logs`）、产物（`artifacts`）和对象存储中的对象（`objects`） |
| `lerryn_retention_removed_bytes_total{kind}` | counter | 被清理的日志（`logs`）和产物（`artifacts`）的大小，不扣除去重 |
| `lerryn_runs_lease_expirations_total` | counter | 本 server 担任主节点期间因 runner 租约过期标为运行中断的运行数；对账只在主节点执行，多节点时按 server 求和 |
| `lerryn_store_up` | gauge | 上一次抓取能否从数据库读到上面几项 |
| `go_sql_*{db_name}` | gauge、counter | 数据库连接池：打开、使用中、空闲的连接数和等待次数 |
| `go_*`、`process_*` | | Go 运行时和进程 |

队列、运行和 runner 的数字在抓取时从数据库读取，是整个部署的值，每个 server 报告的相同。汇总多个 server 时用 `max()`，不要用 `sum()`。请求数、租约过期数和保留策略的清理数是每个 server 各自的，用 `sum()`。

建议的告警：`lerryn_run_queue_oldest_seconds > 300`（排队超过 5 分钟）、`lerryn_runners_online == 0`、`lerryn_store_up == 0`、`rate(lerryn_runs_lease_expirations_total[10m]) > 0`。

## 6 备份与恢复

| 内容 | 单机（SQLite） | 标准拓扑（PostgreSQL） |
| --- | --- | --- |
| 数据库 | `lerryn backup` | `pg_dump` 或企业现有方案（PITR 等） |
| 对象存储 | 包含在 `lerryn backup` 中 | 存储自身的复制或备份 |
| 主密钥 | 单独保管 | 单独保管 |

数据库与对象存储需要配套恢复。SQLite 的在线备份会保护快照需要的对象，见下文。PostgreSQL 配套备份应在停服期间完成，或采用对象版本控制、存储快照等能保留所需内容的方案；对象回收可能在数据库快照之后删除旧对象，仅靠“先备份数据库，再复制对象存储”不能保证完整性。

### 6.1 SQLite：lerryn backup

```bash
docker compose exec lerryn lerryn backup --output /var/lib/lerryn/backups/lerryn-$(date -u +%Y%m%dT%H%M%SZ).tar.gz
```

- server 运行时可以执行。数据库用 `VACUUM INTO` 复制，整个复制在一个读事务里完成，不阻塞写入。
- 备份期间通过可续期的数据库租约暂缓对象回收；任务写入及日志、产物的过期标记继续处理。保护中断或快照引用的对象未写入归档时，备份报错并移除残缺归档，应排查错误后重试。
- 备份是一个 tar.gz：版本化的备份清单、数据库副本和快照引用的本地对象，每个对象在复制时校验 SHA-256。内部文件名与字段保持改名前的格式，已有备份可继续恢复。存储目录下的无关文件、临时文件及备份自身不写入归档。文件权限为 0640。
- 输出位置按文件路径处理，目录中的 `?`、`#`、`%` 不作为数据库连接参数解析。输出文件已存在时拒绝覆盖。备份放在数据目录下时注意定期挪到别的机器上，并清理旧备份。
- 主密钥不在备份里。命令结束时会提示主密钥的位置。

定时备份可以用宿主机的 cron 或 systemd timer 执行上面的命令。

### 6.2 SQLite：lerryn restore

```bash
docker compose stop lerryn
docker compose run --rm --no-deps lerryn restore --input /var/lib/lerryn/backups/<文件名>.tar.gz
docker compose up -d
```

- 必须先停止 server。
- `database.dsn` 支持普通文件路径和本地 `file:` URI（包括 `file:///` 和 `file://localhost/`）。URI 文件名中的空格、`%`、`?`、`#` 要做百分号编码，恢复会写入 server 实际打开的路径；普通路径中的 `%`、`#` 保留字面含义。内存数据库、带远程主机名的 URI 或无法安全解析的路径会在替换目标前拒绝。
- 数据库及其 `-wal`、`-shm` 路径不能与对象存储目录重叠。恢复会在创建暂存区前检查，避免移动存储目录时一并移走数据库。 数据库路径中的符号链接先展开，再处理其后的 `..`，已有数据检查和强制恢复都使用同一个实际位置；无法解析的链接在交换前拒绝，新父目录仍可创建。
- 目标位置已有数据库或对象时拒绝执行，加 `--force` 才会替换。旧目标移入独立的同级目录 `<原名>.pre-restore-<时间>-<随机标识>/`，命令输出其中实际保存的文件或目录路径；同一秒重复恢复也不会覆盖先前的副本。确认无误后手工删除这些保留目录。
- 交换失败时会反向回滚。若回滚也失败，错误会给出保留的旧副本和暂存路径；先保管这些内容，再按实际目标状态恢复，不要直接清理暂存目录。
- 整个备份解压并检查通过后才替换现有数据：gzip 校验通过、数据库能打开，迁移版本不比当前二进制新。归档截断、校验错误或尾部损坏时拒绝恢复。备份的迁移版本较旧时，server 启动时自动迁移。
- 恢复到新机器时，把原实例的主密钥放到 `secrets.master_key_file` 的位置，否则已保存的凭证无法解密。

### 6.3 PostgreSQL

备份：

```bash
pg_dump --format=custom --no-owner --dbname "postgres://lerryn@db.example.com:5432/lerryn" \
  --file lerryn-$(date -u +%Y%m%dT%H%M%SZ).dump
```

参考部署中可以在 postgres 容器里执行：

```bash
docker compose exec -T postgres pg_dump --format=custom --no-owner -U lerryn -d lerryn > lerryn.dump
```

`pg_dump` 自身可以在 server 运行时取得一致的数据库快照。需要配套对象存储时，应停服完成两者的备份，或用对象版本控制、存储快照保留数据库快照引用的对象。单独在 `pg_dump` 之后运行 MinIO 的 `mc mirror` 可能漏掉期间被回收的对象。

恢复：

1. 停止全部 server。
2. 恢复到一个新库，而不是覆盖现有库：

   ```bash
   createdb -U postgres -O lerryn lerryn_restored
   pg_restore --no-owner --role=lerryn -U postgres -d lerryn_restored lerryn.dump
   ```

3. 把 `database.dsn` 指向新库，或在确认后改名：`ALTER DATABASE lerryn RENAME TO lerryn_old; ALTER DATABASE lerryn_restored RENAME TO lerryn;`。
4. 恢复对象存储，放好主密钥，启动 server。`/readyz` 返回 200 后再把流量切回来。

### 6.4 主密钥

主密钥用于加密保存的凭证。没有它，备份里的凭证密文无法解密，任务需要重新填写凭证。

- 首次启动时如果没有找到主密钥文件，server 会生成一个，并在日志中提示位置。
- 主密钥和数据库备份分开保管，例如放进企业的密钥管理系统或离线介质。
- 标准拓扑中所有 server 使用同一个主密钥。
- 文件里每行一个密钥，第一行是当前密钥，后面是轮换前的旧密钥。文件权限应为 0600，放宽时 server 启动会告警。

轮换主密钥时，凭证密文不变，只重新包裹每个凭证版本和通知渠道的数据密钥：

1. 停止全部 server。
2. 在一台能访问数据库的机器上执行 `lerryn secrets rotate-key --config <配置文件>`。它在主密钥文件的第一行写入新密钥，保留旧密钥，再重新包裹全部数据，最后打印处理的条数。
3. 把新的主密钥文件复制到每个 server，单独备份，然后启动 server。
4. 中途失败时重新执行 `lerryn secrets rewrap`，它只处理还没有改用当前密钥的数据，可以重复执行。确认 `rewrap` 报告 0 条之后，才可以从文件里删除旧密钥。

## 7 导出导入与迁到 PostgreSQL

导出格式是一个 tar.gz，包含全部数据表（每表一个 NDJSON 文件）和记录引用的对象，可以导入任一种数据库。它用于跨实例迁移、SQLite 迁到 PostgreSQL，以及 A15 的恢复验证。

- 凭证以密文导出，不含明文；导入的实例需要同一个主密钥。
- 不导出的内容：迁移版本表（由目标库自己的迁移维护）和租约表（运行中的 server 之间的协调状态）。
- 所有表在一个只读快照里读取，导出结果自身一致。导出用于替换实例时，先停止 server，否则导出之后的写入会丢失。
- 导出的对象包括任务文件、脚本工具文件、未过期的产物、归档的日志和 webhook 请求体。已按保留策略过期的产物和日志不需要对象，不导出。
- 记录引用的对象在源对象存储中已经不存在时，记录照常导出，对象记在清单的 `missing_blobs` 中。
- 导出在复制对象时流式校验 SHA-256；内容与哈希不符、读取或关闭失败时拒绝导出并清理本次输出和暂存内容。损坏对象不会被当作缺失对象忽略，已有输出文件也不会被覆盖。
- 导入会完整校验 gzip；归档截断、校验错误或尾部损坏时，业务数据写入的事务回滚。

```bash
lerryn export --output lerryn-export.tar.gz
lerryn --config new.yaml import --input lerryn-export.tar.gz
```

导入的要求和行为：

- 目标库必须为空。不要先用新配置启动 server，再导入。
- 导入先把目标库迁移到导出时的版本，在一个事务里写入全部记录，核对每张表的行数与清单一致后才提交，然后执行之后的迁移。所以较旧版本导出的数据可以导入较新的二进制。
- 导入的每个对象在写入后会按长度和 SHA-256 核验目标存储中的实际内容。可以复用正确的已有对象；同哈希路径中的对象损坏、读取不完整或校验失败时拒绝导入，不提交业务记录，也不覆盖已有对象。此时先检查目标存储，或改用新的存储目录后重试。
- 导出的版本比当前二进制新时拒绝导入。

`lerryn export` 和 `lerryn import` 搬的是整个部署。只想把某个任务带到另一个部署或另一个项目时，用 API 的 `exportTask` 和 `importTask`，或 CLI 的 `lerryn tasks export` 和 `lerryn tasks import`（[CLI 文档](cli.md)）。任务包只含任务名、版本的清单和文件、触发器定义，不含运行记录、凭证和 webhook 令牌，也不需要主密钥；导入的人成为创建者，凭证在新部署重新填写或授权，导入的版本需要重新测试才能激活。

### 7.1 SQLite 迁到 PostgreSQL

`lerryn migrate-db` 把导出和导入合成一步，并在最后比较源库和目标库每张表的行数。 源、目标不能指向同一个数据库；SQLite 配置使用不同路径或链接别名时，还会比较实际文件身份。

1. 准备空的 PostgreSQL 库和目标配置 `new.yaml`（`database.driver: postgres`，以及新的对象存储配置）。
2. 停止单机部署：`docker compose stop lerryn`。
3. 先备份：`lerryn backup`（第 6.1 节）。
4. 执行迁移：

   ```bash
   docker compose run --rm --no-deps \
     -v "$PWD/new.yaml:/etc/lerryn/new.yaml:ro" \
     lerryn migrate-db --to-config /etc/lerryn/new.yaml
   ```

   输出列出每张表在两边的行数，全部一致时提示 `row counts match`。行数不一致时命令以错误退出，通常是源 server 没有停止。`--keep-archive` 保留中间的导出文件，`--work-dir` 指定它的位置。

5. 把主密钥复制到新部署，按第 3 节启动 server。源库不会被修改，确认新部署工作正常之前保留它。

## 8 升级与回退

首版的 server 升级需要短暂停机，不停机的滚动升级在 P1。

升级：

1. 阅读新版本的发布说明（`docs/release-notes/`），确认能否直接回退。
2. 备份数据库（第 6 节）。
3. 停止全部 server：单机为 `docker compose stop lerryn`，标准拓扑为 `docker compose stop server-1 server-2`。
4. 修改 `.env` 中的 `LERRYN_VERSION`，拉取新镜像：`docker compose pull`。
5. 可选：先单独执行迁移，确认成功：`docker compose run --rm --no-deps server-1 migrate`。`lerryn migrate` 可以重复执行；多个 server 同时迁移时在 PostgreSQL 的 advisory lock 下轮流进行。
6. 启动 server。server 启动时自动执行待执行的迁移。等 `/readyz` 返回 200。
7. 升级 runner 后再恢复领取。协议 2 的状态目录要求新领取时固定状态输入，版本 1 只能完成已经领取的运行，不能领取新工作；新 runner 不会向旧 server 降级。

首次升级到带 `/sbx/state` 的协议 2 时，先在管理页停用旧节点并等待活动运行排空，再停旧 runner 和全部 server。完成所有 server 的升级及迁移后，启动新 runner，确认登记版本已更新，再恢复节点；all-in-one 也要等全部 server 都升级后再启动内嵌 runner 领取。不能保留仍会给版本 1 领取任务的旧 server。停用节点仍显示尚未排空的占用。旧运行可以上报结束是兼容边界，不代表这次首次上线支持不停机滚动升级。

迁移 `20261110010000_task_state` 新增任务当前快照与运行输入／输出引用。旧实例首次读取状态为空，不会把旧工作目录自动转换成状态。回退这项迁移会删除状态引用，不能靠回退二进制保留新状态；升级前保留数据库、对象存储及主密钥的完整备份。

回退：

- 数据库的迁移版本比二进制新时，server 和 `lerryn migrate` 都拒绝运行。所以只回退镜像不够，必须同时恢复升级前的数据库备份。
- 步骤：停止全部 server，恢复升级前的备份（SQLite 用 `lerryn restore --force`，PostgreSQL 见第 6.3 节），把 `LERRYN_VERSION` 改回原版本，启动。
- 升级后产生的数据会随回退丢失。需要保留时，回退前用 `lerryn export` 导出，回退后人工核对。

## 9 容量规划

PRD 第 8 节的容量目标（单机 100 个任务、5 个并发轻任务；多节点暂定 1000 个任务、50 个并发运行）已在一台开发机上测过控制面，方法、目标和结果见 [capacity.md](capacity.md)。容器执行本身的开销不在其中，正式发布前要在目标规格的 Linux 主机上复测。以下是规划依据。

- **SQLite 只有一个写入者。** 写事务在进程内排队，日志写入、心跳、事件和领取共用这个队列。单机适合并发运行在 10 个以内、日志量不大的场景。运行排队时间（`lerryn_run_queue_oldest_seconds`）或请求耗时持续上升时，迁到 PostgreSQL。
- **runner 槽位。** `runner.slots` 是一个 runner 同时执行的运行数。任务的 CPU 和内存在容器里单独限制，槽位数按“主机可用资源 ÷ 单个任务的资源上限”估算，并给 dockerd 和环境准备（依赖安装）留出余量。`lerryn_runner_slots_in_use` 长期接近 `lerryn_runner_slots` 时增加 runner。
- **runner 数据目录。** 环境缓存按环境 ID 保留，依赖多的任务会占用较多磁盘。runner 数据目录所在的磁盘按任务种类数 × 单个环境大小预留。
- **server。** server 无状态，按请求量增加副本。每个 server 都持有一个数据库连接池，默认最多 30 个连接（`database.max_open_conns`），PostgreSQL 的 `max_connections` 要覆盖全部 server 和维护命令的连接数，用 `go_sql_open_connections` 观察实际用量。
- **日志。** 单次运行的日志默认上限为 20 万行或 100 MiB。运行中的日志先写入数据库的日志分片表，日志量大的部署优先考虑数据库的磁盘和写入能力。
- **对象存储。** 任务文件、产物和日志归档按内容哈希去重，同一内容只存一份。
- **保留策略。** 默认一直保留运行记录、日志和产物，磁盘会持续增长。按磁盘大小设置 `retention` 下的保留天数和总量上限，例如日志 30 天、产物 90 天、运行记录 365 天，再用 `logs_max_mib`、`artifacts_max_mib` 兜住总量。主节点启动 30 秒后清理一次，之后每 10 分钟一次；被清理的日志和产物在控制台和 API 中显示为已清理，运行记录保留，每个任务最近的一次运行始终保留。每次清理在审计日志中留下一条 `retention.cleanup` 记录。对象存储中的对象在没有任何记录引用时才删除。

## 10 排查

| 现象 | 检查 |
| --- | --- |
| 容器反复重启 | `docker compose logs`。配置无效时启动日志一次列出全部问题；all-in-one 连不上 Docker 或 daemon 不能施加默认加固时直接退出并给出原因 |
| `/readyz` 返回 503 | 返回体的 `failed` 列出失败的检查。`database` 失败表示数据库不可达，或迁移未执行、版本比二进制新 |
| 任务全部卡在排队 | `lerryn_runners_online`；runner 日志；单机检查 `DOCKER_GID` 和数据目录是否为同路径绑定挂载 |
| 运行被标为中断 | runner 与 server 之间断开超过 30 秒，见 `lerryn_runs_lease_expirations_total` 和 runner 日志 |
| MCP 客户端收到 403 | 同机反向代理转发到 127.0.0.1，见第 3 节末尾 |
| 控制台能看不能改，提示部署地址不同 | 打开控制台的地址不是 `server.public_url`。改用部署地址，或把这个地址加进 `server.extra_origins` 后重启，见第 3 节末尾 |

## 11 公网部署

控制台、API 和 MCP 能从公网访问时，下面几步必须在开放访问之前做完。原因见技术设计第 20 节：开放注册默认开启，部署后到首次注册之间，任何能打开控制台的人都可以注册成为管理员。

1. **先有管理员，再开放访问。** 三种做法选一种：

   - 配置 `auth.initial_admin.username` 和 `auth.initial_admin.password_file`，server 第一次启动时创建管理员（推荐）。
   - 在 server 所在主机上执行 `lerryn admin create <用户名> --password-stdin`（第 2 节第 4 步）。
   - 保持只监听本机（单机部署的 `LERRYN_BIND=127.0.0.1`），经 SSH 隧道打开控制台完成首次注册，再改成对外监听。

2. **关闭开放注册。** 管理员登录控制台的管理页面后关闭"开放注册"，或者用管理员的访问令牌：

   ```bash
   lerryn api updateSettings --open-registration=false
   ```

   确认结果（`discover` 不需要登录）：

   ```bash
   curl -fsS https://lerryn.example.com/api/v1/discover | jq '.capabilities.registration_open'   # false
   ```

   配置了单点登录时，提供方认证通过的人第一次登录就会得到账号，不受开放注册影响。在提供方那边把这个应用只分配给需要的人或组。单点登录确认可用后，可以再关闭本地密码登录（`--local-password-login=false`），管理员仍能用密码登录，供应急使用。

3. **只用 HTTPS。** `server.public_url` 写 `https://` 地址，TLS 在反向代理上终止（第 3 节的 Caddy 会自动申请证书）。remote runner 的 `runner.server_url` 也用 HTTPS，否则启动日志会警告。

4. **指标不对公网开放。** 用单独的 `metrics.listen`（例如 `:9090`），只开放给监控网络；或者挂在公开地址上时必须设置 `metrics.token_file`。反向代理不转发 `/metrics`。验证：`curl -s -o /dev/null -w '%{http_code}' https://lerryn.example.com/metrics` 不是 200。

5. **备份主密钥。** 把 `secrets.master_key_file`（默认 `/var/lib/lerryn/master.key`）复制到离线介质或企业的密钥管理系统，和数据库备份分开保管，文件权限 0600。丢失主密钥后，已保存的凭证全部无法解密，只能重新填写（第 6.4 节）。

6. **runner 不对外，注册令牌按 root 凭证保管。** runner 主机不需要任何入站端口。注册令牌能注册新的 runner 并领取任务，也就能拿到这些任务的凭证、读写它们的运行结果，泄露的影响等同于 runner 主机的 root 权限。注册令牌文件（`server.runner_registration_token_file`）只放在 server 和 runner 主机上，权限 0600；所有 runner 注册完成后，把它从 server 配置中去掉。已经在工作的 runner 不会被注册令牌顶替。

7. **通知出口保持默认。** `notify.allowed_networks` 留空时，通知只发往公网地址，回环、私有和链路本地地址一律拒绝。只有确实要发往内网的接收方时，才放行对应的网段。

开放访问之后，用一个新的浏览器会话检查：注册入口不可用，`/metrics` 拿不到，控制台跳转到 HTTPS。
