---
name: lerryn-operate
description: 部署和维护 Lerryn 服务端：Docker Compose 单机或多节点部署、首次注册管理员、配置、指标与告警、备份恢复、导出导入、SQLite 迁到 PostgreSQL、升级回退、runner 扩容和节点排查。维护者要安装、升级、备份、扩容 Lerryn，或排查服务端和 runner 的问题时使用。不用于编写自动化任务。
license: Apache-2.0
---

# Lerryn 运维

面向部署和维护 Lerryn 的人。完整说明在仓库的 `docs/zh-CN/deploy.md`，本 Skill 是操作顺序和要点；命令细节见 [references/runbook.md](references/runbook.md)。

## 先确认

1. 拓扑：单机（一个 `lerryn all-in-one` 容器，SQLite，本地对象存储）还是标准拓扑（多个 server、PostgreSQL、S3 兼容存储、独立 runner）。
2. 版本：`lerryn version`，以及服务端 `GET /api/v1/discover` 的 `server.version`。
3. 健康：`/readyz` 返回 200；返回 503 时响应体的 `failed` 列出失败的检查。

## 硬规则

1. 改动数据之前先备份：升级、恢复、迁移、导入之前都执行一次备份（SQLite 用 `lerryn backup`，PostgreSQL 用 `pg_dump`），并确认备份文件存在。
2. 主密钥（`secrets.master_key_file`）单独保管，不放进备份，不打印、不提交、不贴到对话里。所有 server 必须使用同一个主密钥，丢失后已保存的凭证无法解密。
3. 恢复、导入、迁移之前停止全部 server；`restore` 默认拒绝覆盖，只有确认后才加 `--force`。
4. 回退版本时同时恢复升级前的数据库备份。数据库迁移版本比二进制新时，server 拒绝启动，只换镜像不够。
5. 不把 `/metrics` 和数据库暴露到公网或 runner 主机可达的地址；任务容器在 `network: full` 下能访问宿主机网关。
6. 敏感配置一律通过文件提供（`*_file` 配置项），不写进环境变量和配置文件正文。
7. 生产环境的 runner 主机使用原生 dockerd，并由它管理 iptables。OrbStack、Docker Desktop 上任务之间的网络隔离不生效，只用于开发。
8. 对生产实例执行停机、恢复、删除类操作前，向用户复述影响范围并得到确认。

## 常见任务

| 任务 | 要点 |
| --- | --- |
| 单机部署 | 数据目录交给 uid 65532，同路径绑定挂载；`.env` 填 `DOCKER_GID`、`LERRYN_PUBLIC_URL`；第一个注册的账号成为管理员，公网部署先完成首次注册 |
| 多节点部署 | 先启动第一个 server 生成主密钥，再复制给其他 server；反向代理不缓冲响应、读超时不短于 60 秒 |
| 指标 | `metrics.enabled: true` 加单独的 `metrics.listen`；建议告警：排队超过 5 分钟、在线 runner 为 0、`lerryn_store_up == 0`、租约过期数增长 |
| 备份 | SQLite：`lerryn backup --output <文件>`，可以在线执行；PostgreSQL：`pg_dump --format=custom`，再备份对象存储 |
| 恢复 | 停止 server，`lerryn restore --input <文件>`，放好主密钥，启动后等 `/readyz` 返回 200 |
| 迁到 PostgreSQL | 停止 server，备份，`lerryn migrate-db --to-config new.yaml`，行数一致（`row counts match`）后切换 |
| 升级 | 读发布说明，备份，停止全部 server，换镜像，可选先 `lerryn migrate`，启动，最后逐台升级 runner |
| 扩容 | runner 槽位按“主机可用资源 ÷ 单个任务的资源上限”估算；`lerryn_runner_slots_in_use` 长期接近 `lerryn_runner_slots` 时加 runner；SQLite 写入成为瓶颈时迁到 PostgreSQL |

## 排查

| 现象 | 先看 |
| --- | --- |
| 容器反复重启 | `docker compose logs`，启动日志一次列出全部配置问题 |
| `/readyz` 503 | 响应体 `failed`；`database` 失败是数据库不可达或迁移版本不一致 |
| 运行全部卡在排队 | `lerryn_runners_online`、runner 日志；单机检查 `DOCKER_GID` 和数据目录挂载 |
| 运行被标为中断 | runner 与 server 断开超过 30 秒；`lerryn_runs_lease_expirations_total` |
| MCP 客户端收到 403 | 同机反向代理转发到 127.0.0.1 并保留公网 Host，设 `mcp.disable_localhost_protection: true` |
| MCP 客户端收不到流式结果 | 代理在缓冲响应；关闭缓冲，或设 `mcp.json_response: true` |
