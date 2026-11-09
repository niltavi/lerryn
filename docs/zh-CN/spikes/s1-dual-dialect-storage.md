# S1 验证：用一套 Bun 代码覆盖 PostgreSQL 和 SQLite

日期：2026年11月2日　对应：[实施规划 0.2](../agent-automation-sandbox-implementation-plan.md) 第 5 节 S1，[技术设计 0.2](../agent-automation-sandbox-tech-design.md) 第 4.4、5.5、6 节

## 结论

**通过。** 实施规划给 S1 定的两条通过标准都满足，不需要启用退路（改用 Ent，或者用 sqlc 按方言各生成一份）。存储层继续使用 Bun + goose。

| 通过标准 | 结果 |
| --- | --- |
| 测试全部通过 | 同一套测试在 SQLite 和 PostgreSQL 17 上全部通过（`make check`、`make test-pg`），`-race -count=8` 重复运行稳定 |
| 方言专属代码集中在 `store/dialect/` | 是。`store/` 下的其他代码和全部测试只用 Bun 的查询构建器，或者两种数据库通用的原生 SQL |

有几件事需要在 M1 之前定下来，最重要的是 SQLite 的写入串行化。第 3 节是测量数据，第 7 节是对技术设计的修改建议。

## 1 范围与环境

| 内容 | 位置 |
| --- | --- |
| 双方言初始化：`store.Open` 按 `config.Database` 选择方言，检查会话设置 | `server/internal/store/store.go`、`dialect/{postgres,sqlite}.go` |
| 迁移：goose，按方言 embed，`store.Migrate`、`store.CheckMigrations`，`lerryn migrate` | `server/migrations/`、`server/internal/store/migrate.go`、`server/cmd/lerryn/roles.go` |
| 首批迁移：users、access_tokens、projects、role_bindings、audit_events、leases | `server/migrations/{postgres,sqlite}/20261102120000_initial_schema.sql` |
| 存储契约测试框架：按环境变量选库，PostgreSQL 每个测试一个 schema，SQLite 每个测试一个临时文件 | `server/internal/store/storetest/` |
| 选主租约（正式表 `leases`） | `dialect.AcquireLease`、`store.DB.AcquireLease` |
| S1 验证：运行领取、`concurrency_key` 部分唯一索引、运行租约续期，建在测试专用的 `s1_runs`、`s1_run_required_labels`、`s1_runner_labels` 上 | 方言 SQL 在 `dialect/s1_spike_test.go`，测试在 `dialect/s1_test.go` |

`runs` 表属于 M1，所以 S1 的表结构和查询都只存在于测试文件中。M1 建表时，表结构移进迁移，查询移进 `dialect/` 的正式文件，`s1_test.go` 就是调用方的样子。

M1 已经按这个方式落地：表结构在 `server/migrations/{postgres,sqlite}/20261103010000_tasks_runs.sql`，领取、续租和过期扫描在 `server/internal/store/dialect/runs.go`，对应的测试在 `server/internal/store/runs_test.go`。`s1_spike_test.go` 和 `s1_test.go` 随之删除，上表中的这两个文件只在 M1 之前的提交里能找到。

环境：macOS arm64，Go 1.27.3，Bun v1.3.0，pgx v5.12.0，modernc.org/sqlite v1.61.1（内置 SQLite 3.54.1），goose v3.29.0，PostgreSQL 17.11（OrbStack 中的 `postgres:17` 容器）。`CGO_ENABLED=0` 下 linux/amd64、linux/arm64、darwin/arm64 都能构建。

### 测试覆盖

运行领取：

- 按排队时间先后领取。
- 标签匹配：runner 没有标签时只领不要求标签的运行；运行要求的每个标签 runner 都有，才能领取。
- 16 个 goroutine 并发领取 300 条，每次领 1 到 4 条：没有重复，没有遗漏，没有错误，库里的 `runner_id` 和领取方一致。
- 第一个领取事务未提交时发起第二次领取。PostgreSQL 不等待，直接拿到另一条；SQLite 等待写锁，第一个事务提交后拿到另一条。两种数据库只在这里行为不同，测试分别断言。

`concurrency_key`：

- 同一个 key 的两条活动运行冲突；key 为空的运行互不冲突；终态运行不占用 key。
- 排队中、准备中、运行中、取消中四种状态都占用 key，运行结束后释放。
- 把终态运行改回活动状态时同样受约束（索引对 UPDATE 生效）。
- 16 个并发插入同一个 key，恰好 1 个成功，其余都是唯一约束错误，没有 `SQLITE_BUSY` 之类的其他错误。

运行租约：

- 属主可以续租，其他 runner 不能。
- 时钟来源：应用时钟拨慢 10 年后续租，PostgreSQL 的到期时间仍是数据库 `now()` 加 30 秒；SQLite 用应用时钟。
- 租约过期后，对账扫描把运行标为运行中断，只影响过期的那条；之后 runner 续租失败，`concurrency_key` 被释放。

选主租约：获取、他人不能获取、续期、释放、过期后被接管；12 个并发竞争者恰好 1 个胜出。

正式表与迁移：Bun 的增删改查在两种数据库上往返一致（可空字段、布尔、JSON、微秒时间）；用户名唯一不区分大小写；外键和级联删除；每个用户只有一个个人项目；SQLite 的 WAL、外键和 busy_timeout 生效，关掉外键或改用 deferred 事务会在 `Open` 时报错；只读事务打开时写入不受阻；迁移可重复执行；4 个进程并发迁移；down 迁移可以回到空库再重新 up；数据库版本比二进制新时拒绝运行。

## 2 实现要点

**驱动。** PostgreSQL 用 pgx（`database/sql` 接口），SQLite 用纯 Go 的 modernc.org/sqlite，两者都不需要 cgo。

**SQLite 连接参数。** 配置里的 `dsn` 可以是文件路径或 `file:` URI，`store.Open` 补上缺省参数，再读回检查：

| 参数 | 作用 |
| --- | --- |
| `journal_mode=WAL` | 读不阻塞写 |
| `foreign_keys=1` | SQLite 默认关闭外键 |
| `busy_timeout=5000` | 写锁被占用时等待，而不是立即失败 |
| `_txlock=immediate` | 读写事务在 `BEGIN` 时就拿写锁，见第 4 节。只读事务（`sql.TxOptions{ReadOnly: true}`）仍然是 deferred |

运维人员可以在 DSN 里改 busy_timeout，但关闭外键、改用 deferred 事务、使用内存数据库都会在启动时报错。

**PostgreSQL 连接参数。** 使用 simple protocol（原因见第 5 节），会话固定 `standard_conforming_strings=on`、`timezone=UTC`，启动时检查前者确实生效。

**迁移。** 使用 goose 的 Provider API：迁移文件 embed 进二进制，不用 goose 的全局状态，也不接收进程里其他地方注册的 Go 迁移。PostgreSQL 下用会话级 advisory lock 串行化并发迁移，锁 ID 由当前 schema 派生，这样并行测试的各个 schema 之间、以及同库中其他使用 goose 的应用之间不会互相阻塞。`lerryn migrate` 可重复执行，第二次输出 `applied 0 migration(s)`。

## 3 测量

300 条运行，16 个 goroutine 并发领取，不开 `-race`，每种配置跑 3 到 5 次：

| 数据库 | 配置 | 耗时 | 吞吐（条/秒） |
| --- | --- | --- | --- |
| PostgreSQL 17 | 默认连接池，`FOR UPDATE SKIP LOCKED` | 38–43 ms | 7,000–8,000 |
| SQLite | 默认连接池，`BEGIN IMMEDIATE` + busy_timeout（当前实现） | 135–947 ms | 317–2,218，波动大 |
| SQLite | 连接池上限设为 1（临时实验，未保留） | 13–16 ms | 18,000–23,000 |
| SQLite | 领取事务外加一把进程内互斥锁（临时实验，未保留） | 11–13 ms | 22,000–26,000 |

SQLite 当前实现的正确性没有问题，慢在等待方式上：多个连接争抢写锁时，拿不到锁的连接由 SQLite 的 busy handler 按 1、2、5、10……100 毫秒逐级退避后重试（modernc 编译时带 `HAVE_USLEEP`，用的就是这个默认间隔表），锁释放时它们往往还在睡眠，于是同样 300 次领取，整批耗时在 135 到 947 毫秒之间波动。单机部署里的写入方不只有领取，还有每 500 毫秒一批的日志分片、心跳和事件，这种等待会直接拉长日志的可见延迟。在进程内先排队再进 SQLite，吞吐提高一个数量级以上，波动也消失了。建议见第 7 节第 2 条。

## 4 方言差异

| 项 | PostgreSQL | SQLite | 处理 |
| --- | --- | --- | --- |
| 队列领取 | `FOR UPDATE SKIP LOCKED`，并发领取互不等待 | 没有行锁，写事务排队执行 | `dialect` 各写一份 SQL |
| 写事务的开始方式 | 无需处理 | 默认的 deferred 事务先读后写；若期间别的连接已经写入，升级写锁时直接返回 `SQLITE_BUSY`，busy_timeout 不起作用 | 强制 `_txlock=immediate` |
| 批量领取的写法 | `WITH picked AS MATERIALIZED (SELECT … FOR UPDATE SKIP LOCKED LIMIT n) UPDATE … FROM picked`。物化保证加锁的子查询只执行一次，`WHERE id IN (子查询)` 可能被计划器重复执行 | `UPDATE … WHERE id IN (SELECT … LIMIT n) RETURNING *` | 两份 SQL |
| 时钟 | `now()`，即事务开始时间，与应用时钟无关 | 应用时钟；单机下与数据库同一个时钟 | 方言函数都接收 `now` 参数，PostgreSQL 忽略它 |
| 时间类型 | `timestamptz`，微秒精度 | STRICT 表中的 TEXT。Bun 统一写成 UTC 的 `2006-01-02 15:04:05.999999+00:00`，文本比较和排序与时间顺序一致（测试覆盖了 `.45` 与 `.5` 这类长度不同的小数秒） | 写入前截断到微秒（`store.Now`）。不得绕过 Bun 直接用 `database/sql` 写 `time.Time`，modernc 的默认格式不同，会破坏文本比较 |
| UUID | `uuid` | TEXT，小写规范形式 | Go 侧统一用 `google/uuid`，两边透明 |
| 布尔 | `boolean` | INTEGER 0/1。Bun 写 `TRUE`/`FALSE` 字面量，SQLite 3.23 起视为 1/0 | 无需处理 |
| JSON | `text` | TEXT | 只存不查，Bun 自动编解码切片和 map |
| 主键为 NULL | 主键隐含 NOT NULL | 非整数主键允许 NULL（历史遗留行为） | 迁移里显式写 `NOT NULL`；模型的 `id` 用 `nullzero`，漏设 ID 会报错，而不是写入全零 UUID |
| 不区分大小写的唯一 | `lower()` 折叠全部 Unicode | `lower()` 只折叠 ASCII | 两边都用 `lower(username)` 表达式唯一索引；用户名应限定为 ASCII，见第 7 节 |
| 修改枚举约束 | `ALTER TABLE` 即可 | 改 CHECK 约束要重建表 | 枚举不用 CHECK 约束，取值在 service 层校验 |
| 唯一和外键冲突的错误 | SQLSTATE 23505、23503，带约束名 | 扩展错误码 2067、787；消息里是 `表.列` 或 `index 'name'`，格式不统一 | `dialect.IsUniqueViolation`、`IsForeignKeyViolation` 只判断类别，不解析约束名 |
| upsert 与 RETURNING | 支持 | 3.35 起支持 RETURNING；`ON CONFLICT … DO UPDATE … WHERE` 的写法与 PostgreSQL 一致 | 选主用一条 upsert 完成首次创建、获取和续期 |
| 并发迁移 | advisory lock 串行，4 个并发迁移全部成功 | 不加锁。并发迁移在写锁上排队，后到的因"表已存在"失败，不会留下半套表 | SQLite 只有一个 server，可以接受 |
| 首次切换到 WAL | 无 | 切换 journal_mode 需要独占锁，且不走 busy_timeout；多个进程同时首次打开同一个新文件，可能在连接时报 `database is locked` | 同上，记录在案 |
| 读事务 | MVCC | WAL 下读不阻塞写；只读事务不占写锁 | 测试覆盖 |
| 标签匹配 | `NOT EXISTS` 两层嵌套，从 `runner_labels` 读 runner 的标签 | 同一写法 | 不把标签列表作为参数传入，避开空列表的 `NOT IN ()` 边界情况 |

## 5 Bun 的不足

Bun 胜任这次验证：建表以外的全部增删改查（单行和多行插入、表达式条件、联表、计数、更新、删除）都是一份代码跑在两种数据库上，原生 SQL 的 `?` 占位符也由 Bun 按方言格式化。需要注意的地方如下。

1. **参数在客户端拼进 SQL 文本，不使用绑定参数。** 这带来三个后果：
   - pgx 默认把每个不同的 SQL 文本准备成服务端语句并缓存。参数值一变就是一条新语句，每次查询多一次往返，缓存也不断被挤出。现已改用 simple protocol。
   - Bun 转义字符串只把单引号加倍，前提是 `standard_conforming_strings=on`。现已在连接参数里强制打开，并在 `Open` 时检查，因为连接池代理可能丢弃启动参数。
   - 查询计划无法在服务端复用。S5 的日志写入这类高频路径需要实测，必要时在 `dialect/` 里改用 pgx 的批量或 COPY 接口。
2. **占位符个数不匹配时不报错。** 参数少了，Bun 原样保留 `?`，错误要到数据库执行时才以语法错误出现；参数多了直接忽略。Bun 按字符扫描 `?`，不识别字符串字面量。原生 SQL 较长时，建议用 `?0`、`?1` 这样的序号占位符，或者传一个结构体用字段名占位，不要靠位置对齐。S1 的领取查询有 4 个参数，两种方言的顺序还不一样，就是这类风险。
3. **错误分类要自己做。** Bun 不提供跨方言的错误类别，唯一冲突、外键冲突、`SQLITE_BUSY` 都要按驱动的错误类型判断。已在 `dialect` 中封装。
4. **时间格式依赖"只用 Bun"的约定。** SQLite 的时间比较依赖 Bun 固定的写入格式，见第 4 节。
5. **小问题。** `store.DB` 嵌入 `*bun.DB`，不能再定义名为 `Dialect()` 的方法，否则会遮蔽 `bun.DB.Dialect()`，`*store.DB` 不再满足 `bun.IDB`，所以方言对象的访问方法改名为 `Native()`。模型的默认表别名是结构体名的蛇形（`Project` 对应 `project`），联表时要用这个别名，或者在标签里写 `alias:`。

## 6 goose 的问题

- **CLI 的依赖很重。** 按 `AGENTS.md` 用 `tool` 指令引入 goose CLI 后，它支持的全部数据库驱动（ClickHouse、MSSQL、MySQL、Vertica、YDB、libsql 及其 Azure、gRPC 依赖）都写进了 `go.mod` 和 `go.sum`。只引入 Bun、pgx、modernc 和 goose 库时，`go.mod` 是 49 行（28 条 indirect），`go.sum` 是 120 行；再加上 goose CLI，变成 102 行（78 条 indirect）和 360 行。这些依赖不进入 lerryn 二进制（`go version -m` 显示 32 个依赖模块，其中没有这些驱动），govulncheck 也不报告。但 Dockerfile 第一步 `go mod download` 会下载它们，缓存失效时镜像构建变慢，依赖审查的范围也变大。goose 库本身只带来 3 个很小的依赖：`sethvargo/go-retry`、`mfridman/interpolate`、`go.uber.org/multierr`。
- **两份迁移要手工对齐时间戳。** `goose create` 每次取当前时间，为两个方言各执行一次，得到的时间戳不同，需要手工改名。`TestDialectsInSync` 会拦住不一致的提交。
- **只读操作并不只读。** `HasPending`、`GetVersions` 在版本表不存在时会不加锁地创建它。所以 `store.CheckMigrations` 改为直接查询 `goose_db_version`，适合在其他 server 正在迁移时用作就绪检查。
- **`Up` 不检查数据库是否比二进制新。** 回退二进制而没有恢复数据库时，`Up` 什么也不做并返回成功。`store.Migrate` 在 `Up` 之后补了这项检查，拒绝运行。
- `Provider.Close()` 会关闭传入的连接池，与 Bun 共用连接池时不能调用。

## 7 对技术设计的修改建议

按实施规划"技术设计按结论更新"的要求，以下建议供维护者修订技术设计时参考。本次没有改动技术设计。

1. **第 4.4 节，选主 SQL 改为 upsert。** 现在的 `UPDATE leases … WHERE name = 'leader' AND (…)` 在表里还没有这一行时永远不会成功，需要另有一步预先插入。建议改为：

   ```sql
   INSERT INTO leases (id, name, holder, expires_at)
   VALUES ($1, 'leader', $2, now() + interval '15 seconds')
   ON CONFLICT (name) DO UPDATE
   SET holder = excluded.holder, expires_at = excluded.expires_at
   WHERE leases.holder = excluded.holder OR leases.expires_at < now()
   RETURNING expires_at;
   ```

   返回一行即为主节点。已在 `dialect.Postgres.AcquireLease` 实现。
2. **第 4.4、6.1 节，写明 SQLite 的单写入者做法。** "普通事务即可"不够：必须是 `BEGIN IMMEDIATE`，否则先读后写的事务会在并发时失败（第 4 节）。另外建议 SQLite 下所有写事务在进程内串行化：store 提供统一的写事务入口，SQLite 实现先取得进程内的写闸门再开始事务，PostgreSQL 实现直接开始事务；读操作仍走连接池。busy_timeout 只用来兜底跨进程的写入（`lerryn migrate`、在线备份）。依据是第 3 节的测量。这项工作放在 M1，与第一批写路径一起做。
3. **第 6.3 节，细化"不用数据库专有类型"。** 建议写成类型对照：UUID 在 PostgreSQL 用 `uuid`、在 SQLite 用 TEXT；时间用 `timestamptz` 和 TEXT（Bun 的固定 UTC 格式）；布尔用 `boolean` 和 INTEGER；JSON 两边都用 text；SQLite 表一律 STRICT。另加两条约定：时间写入前截断到微秒；枚举不用 CHECK 约束。
4. **第 12.1 节，用户名限定为 ASCII。** 例如 `[A-Za-z0-9._-]`，或者入库前统一转小写。否则 `lower(username)` 唯一索引在两种数据库上的判定会不一样（第 4 节）。
5. **第 6.2 节，补全方言专属的热点。** 除了运行领取、租约续期和选主，还有租约过期扫描（对账）、连接参数和会话检查、迁移锁、驱动错误分类。
6. **第 5.5 节，补充约束和索引。**
   - `projects`：迁移里已经加了 `(owner_user_id) WHERE is_personal` 部分唯一索引，保证每人只有一个个人项目（第 12.2 节），建议补进约束列。
   - `runs`：对账扫描按状态和 `lease_expires_at` 过滤，M1 建表时考虑加相应索引。
   - `runs`：领取查询用到 `AS MATERIALIZED`，需要 PostgreSQL 12 以上。建议在部署要求中写明支持的最低 PostgreSQL 版本，CI 覆盖最低和最新两个版本。本次只测了 17。
7. **第 4.4 节，明确租约过期后、对账处理前的续租。** 本次的实现是：只要运行仍处于活动状态、runner 也匹配，续租就成功。对账和续租都是单行 UPDATE，在 PostgreSQL 上由行锁串行，结果是确定的。如果希望"过期即不可续"，需要在续租条件里加上 `lease_expires_at >= now()`。
8. **第 15.4 节，补充迁移细节。** 迁移锁是按 schema 派生 ID 的 advisory lock；数据库版本比二进制新时 `lerryn migrate` 拒绝运行，所以回退二进制时必须同时恢复数据库。
9. **工具链（`AGENTS.md`，供维护者决定）。** 如果在意第 6 节的依赖体积，可以用 `lerryn migrate create <name>` 一次生成两份同时间戳的迁移文件，替代 goose CLI，并移除对应的 `tool` 指令。程序内迁移继续使用 goose 库。这与 `AGENTS.md` 中"迁移工具用 tool 指令固定"的规则冲突，所以本次没有这样做。

## 8 没有覆盖的部分

- S5（日志写入容量）不在本次范围。
- 只在 macOS 上运行过；Linux 由 CI 覆盖（`make test-sqlite`、`make test-pg`）。
- PostgreSQL 只测了 17。
- SQLite 写闸门（第 7 节第 2 条）没有实现。
- `store.CheckMigrations` 还没有接到 `/readyz`；server 启动时是否自动迁移也还没有决定。两者都属于 `server/internal/app` 和 `server/cmd`（W0）。`store.Migrate` 支持多个 server 并发调用。
