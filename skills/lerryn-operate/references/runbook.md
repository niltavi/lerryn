# 运维命令

以 `deploy/single-node/` 和 `deploy/multi-node/` 的 Compose 文件为例。服务名按实际部署替换。

## 单机部署

```bash
sudo mkdir -p /var/lib/lerryn && sudo chown 65532:65532 /var/lib/lerryn
cd deploy/single-node && cp .env.example .env      # 填 DOCKER_GID、LERRYN_PUBLIC_URL、LERRYN_BIND
stat -c %g /var/run/docker.sock                    # DOCKER_GID 的值
docker compose up -d
docker compose ps                                  # STATUS 为 healthy
curl -fsS http://127.0.0.1:33333/readyz
```

- 数据目录必须是同路径的绑定挂载（`/var/lib/lerryn:/var/lib/lerryn`），不能用命名卷，否则任务容器的挂载源找不到。
- `LERRYN_BIND` 默认 `127.0.0.1`。前面有反向代理时保持不变。

## 多节点部署

```bash
cd deploy/multi-node && cp .env.example .env
./init-secrets.sh                                  # 生成 secrets/ 下的口令、pgpass、对象存储密钥和 runner 注册令牌
docker compose up -d server-1                      # 先让它生成主密钥
docker compose up -d
docker compose --profile runners up -d
```

- 数据库口令放在 pgpass 文件里，`PGPASSFILE` 指向它，DSN 不带口令。
- server 经 `server.runner_registration_token_file` 接受注册令牌。runner 第一次启动时用 `runner.registration_token_file` 中的令牌换取自己的凭证，存在 `runner.data_dir` 下的 `runner-credential`，之后不再需要注册令牌。专用 runner 主机用同一个注册令牌，`runner.server_url` 指向 server 或负载均衡，用 HTTPS。
- 多节点必须用 S3 兼容存储：`storage.driver: s3`，`storage.s3.endpoint`、`bucket` 必填，访问密钥经 `access_key_id_file`、`secret_access_key_file` 提供；MinIO 一般要 `path_style: true`。桶由运维人员事先创建。

## 配置

三层，后者覆盖前者：内置默认值、YAML 配置文件（`--config` 或 `LERRYN_CONFIG`）、`LERRYN_` 前缀的环境变量（例如 `LERRYN_RUNNER_SLOTS=8`，列表用逗号分隔）。未知的键直接报错。全部配置项见 `deploy/lerryn.example.yaml` 和 `docs/zh-CN/deploy.md` 第 4 节。

通知只能发往公网地址；内网的 webhook 接收方要把它的网段加进 `notify.allowed_networks`（CIDR 列表），只加需要的网段。

## 指标

```yaml
metrics:
  enabled: true
  listen: ":9090"        # 只开放给监控网络
```

挂在公开地址上时不设 `listen`，改设 `metrics.token_file`，抓取时带 `Authorization: Bearer <令牌>`。队列、运行、runner 类指标每个 server 报告的相同，汇总用 `max()`；请求数、租约过期数用 `sum()`。

## 备份与恢复（SQLite）

```bash
docker compose exec lerryn lerryn backup --output /var/lib/lerryn/backups/lerryn-$(date -u +%Y%m%dT%H%M%SZ).tar.gz

docker compose stop lerryn
docker compose run --rm --no-deps lerryn restore --input /var/lib/lerryn/backups/<文件名>.tar.gz
docker compose up -d
```

- 备份不含主密钥。恢复到新机器时把主密钥放到 `secrets.master_key_file` 的位置。
- 目标位置已有数据时 `restore` 拒绝执行；加 `--force` 后，被替换的内容改名为 `<原名>.pre-restore-<时间>` 保留。

## 备份与恢复（PostgreSQL）

```bash
pg_dump --format=custom --no-owner --dbname "postgres://lerryn@db.example.com:5432/lerryn" --file lerryn.dump
# 恢复到新库，确认后再切换
createdb -U postgres -O lerryn lerryn_restored
pg_restore --no-owner --role=lerryn -U postgres -d lerryn_restored lerryn.dump
```

先备份数据库，再备份对象存储。恢复时先停止全部 server。

## 导出、导入与迁库

```bash
lerryn export --output lerryn-export.tar.gz
lerryn --config new.yaml import --input lerryn-export.tar.gz          # 目标库必须为空
docker compose run --rm --no-deps -v "$PWD/new.yaml:/etc/lerryn/new.yaml:ro" \
  lerryn migrate-db --to-config /etc/lerryn/new.yaml                   # 导出加导入，最后核对行数
```

凭证以密文导出，目标实例需要同一个主密钥。

只搬一个任务时不用整库导出：`lerryn tasks export <任务> --history --file task.json`，在目标部署 `lerryn tasks import task.json`。任务包不含运行、凭证和 webhook 令牌，导入后需要重新填写凭证、测试再激活。

## 升级与回退

```bash
docker compose stop lerryn                         # 标准拓扑停止全部 server
# 修改 .env 中的 LERRYN_VERSION
docker compose pull
docker compose run --rm --no-deps lerryn migrate   # 可选，先单独执行迁移
docker compose up -d
```

回退：停止全部 server，恢复升级前的备份（`lerryn restore --force`，或上面 PostgreSQL 一节的恢复步骤），改回 `LERRYN_VERSION`，启动。升级后产生的数据会丢失，需要保留时先 `lerryn export`。

## 检查运行状态

```bash
curl -fsS http://127.0.0.1:33333/readyz
lerryn discover                                    # 服务端版本、能力、运行时目录
lerryn api listRunners                             # 管理员：runner、标签、槽位和最近心跳
```
