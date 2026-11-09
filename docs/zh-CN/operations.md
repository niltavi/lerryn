# Lerryn 日常运维

[English](../en/operations.md) · [文档目录](README.md)

本文用于已经部署的实例，覆盖健康检查、运行排查、备份安排与节点维护。首次安装、完整配置、恢复和迁移命令见[部署与运维](deploy.md)。下面的 `docker compose` 命令在实际部署目录中运行，服务名以对应 Compose 文件为准；`lerryn api` 命令需要事先登录，有些操作要求管理员身份。

## 检查实例与运行

```bash
docker compose ps
curl -fsS http://127.0.0.1:33333/readyz
lerryn discover
lerryn api listRunners
lerryn api listRuns --status failed --status interrupted --limit 20
```

`/readyz` 检查数据库和迁移状态，返回 503 时先读响应中的 `failed`，再查 server 日志。它就绪不代表一定有 runner 能执行任务；还需检查节点最近心跳、标签与空闲槽位。`listRunners` 需要管理员身份。通过反向代理访问时，将示例地址换成实例地址。

读取某次运行时，先保存运行 ID，再读取结构化详情与有限日志：

```bash
lerryn runs get <run-id>
lerryn runs logs <run-id> --tail 200
lerryn runs logs <run-id> --follow
```

| 状态 | 判断与下一步 |
| --- | --- |
| `queued` | 检查在线 runner、所需标签、剩余槽位、项目并发上限与同任务重叠策略 |
| `preparing` | 检查依赖安装、运行时镜像、网络、凭证授权和 runner 日志 |
| `running` | 查看阶段、日志和资源使用；客户端断开不会自动取消 |
| `cancelling` | 正在等待容器停止确认，确认前保持此状态 |
| `completed` | 进程以 0 退出，业务是否成功仍需读取 `result` |
| `failed`、`timed_out` | 读取 `error.code`、阶段与日志，修复输入、依赖或代码后再测试 |
| `interrupted` | 检查 runner 与 server 的连通性和租约过期，恢复节点后确认遗留容器已清理 |
| `cancelled`、`skipped` | 读取触发来源、错误或跳过原因，确认是否为预期操作或调度策略 |

任务会发消息、写数据或调用有副作用的外部接口时，先核对上一次是否已经执行了这些操作，再决定重跑。修复代码要提交新版本、测试并检查结果。遇到凭证失效时，让凭证所有者通过填写页或控制台更新，不把明文交给 Agent 或写进参数。

日志过期会返回 `410 expired`，运行记录仍可能保留。超出日志行数或大小上限时也会截断；排查时区分过期、截断和读取失败。脱敏会遮盖已知凭证，但已经注入进程的凭证无法远程收回；需要停止进程时明确取消运行。

## 观察容量与保留策略

指标启用方式见[部署文档第 5 节](deploy.md#5-指标)。重点看队列等待时间、在线节点、占用槽位和数据库可达性：

| 指标 | 处理 |
| --- | --- |
| `lerryn_run_queue_oldest_seconds > 300` | 检查 runner 是否失联、槽位是否不足、标签是否匹配 |
| `lerryn_runners_online == 0` | 检查 runner 进程、网络和身份凭证 |
| `lerryn_runner_slots_in_use` 长期接近 `lerryn_runner_slots` | 按主机 CPU、内存和任务资源上限调整槽位或增加 runner |
| `lerryn_store_up == 0` | 检查数据库连接与迁移版本 |
| `rate(lerryn_runs_lease_expirations_total[10m]) > 0` | 检查节点故障与网络中断 |

队列、运行与 runner 指标是全实例值，多 server 汇总用 `max()`；请求数、租约过期与清理计数按 server 累加，用 `sum()`。

运行、日志和产物默认一直保留。按容量设置 `retention` 的天数与总量上限；日志、产物的天数不能大于运行保留天数。对象只有在没有记录引用时才被回收，每个任务最近的一次运行始终保留。容量规划和已有实测的适用边界见[部署文档第 9 节](deploy.md#9-容量规划)与[容量结果](capacity.md)。

## 安排备份与恢复演练

备份必须覆盖数据库、对象存储和单独保管的主密钥。SQLite 可在线执行：

```bash
docker compose exec lerryn lerryn backup --output /var/lib/lerryn/backups/lerryn-$(date -u +%Y%m%dT%H%M%SZ).tar.gz
```

该命令会保护快照需要的对象，备份不含主密钥。把归档移到另一台机器，记录备份时间和对应版本。Mac mini 上的文件位于 OrbStack 虚拟机内，要用 `docker compose cp` 拷出。

PostgreSQL 需要数据库与对象存储的配套备份：停服完成两者，或用对象版本控制、存储快照保留数据库快照引用的内容。只在 `pg_dump` 后复制对象存储可能漏掉期间被回收的对象。

定期在独立环境中演练恢复：先停止目标 server，按[备份与恢复](deploy.md#6-备份与恢复)恢复数据和主密钥，检查 `/readyz`、任务文件、日志与产物，以及凭证能否解密。演练环境中的定时任务、Webhook 和通知应先隔离，避免重复产生外部操作。不要用正在服务的实例直接覆盖演练。

## 维护 runner 与升级

在停机或升级一个 runner 前，先停止它领取新运行，让手上的运行继续结束：

```bash
lerryn api updateRunner --runner-id <runner-id> --disabled=true
lerryn api listRunners
```

确认该节点的 `running` 为 0 后再停止进程。维护结束，检查网络、运行时、数据目录和凭证，再恢复领取：

```bash
lerryn api updateRunner --runner-id <runner-id> --disabled=false
```

runner 数据目录保存稳定 ID、注册后取得的凭证和缓存。不要为了清缓存删除整个目录；保护其中的身份文件。注册令牌可用于登记新节点，按高权限凭证保管，所有节点注册后从 server 的配置中移除固定注册令牌。

升级整个实例时先读发布说明，备份后按[升级与回退](deploy.md#8-升级与回退)执行。首次上线协议 2 与 `/sbx/state` 必须先停领并排空，升级全部 server 与迁移后再升级 runner、恢复领取；all-in-one 的内嵌 runner 同样受此限制。回退需要恢复匹配的数据库和对象存储，不能只替换旧镜像。

跨运行状态与协议 2 尚属[未发布改动](release-notes/unreleased.md)，不沿用 0.9.0 的审查和目标环境验证结论。实际部署时以所选发行版本的发布说明为准。
