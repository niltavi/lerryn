# S4 容器执行验证

日期：2026年11月2日　对应：[技术设计 0.2](../agent-automation-sandbox-tech-design.md) 第 7、15.2 节，[实施规划 0.2](../agent-automation-sandbox-implementation-plan.md) 第 5 节 S4　代码：`server/internal/executor/`

## 1 结论

**S4 通过。** 按技术设计第 7.2 节实现的 Docker 执行器，在 macOS（OrbStack）和 Linux（runner 在容器中运行）两种方式下，A09 场景都通过：带子进程的任务被取消或超时后，容器和其中所有进程都消失；在 Docker 确认容器停止之前，执行器不会报告"已停止"。第 7.2 节列出的默认加固全部生效，每一项都在容器内部核对过，不只是检查请求参数。

技术设计中最需要改的是下面三处，其余建议见第 8 节：

1. **挂载路径。** "数据目录在容器内外使用相同路径"这条要求本身可行，但第 15.2 节的 Compose 示例用的是命名卷 `lerryn-data:/var/lib/lerryn`，容器内外路径并不相同，照这个示例部署，任务容器一个都启动不了。建议改为同路径绑定挂载，或者让 runner 改用"命名卷 + 子路径"挂载。
2. **任务之间的网络隔离依赖 dockerd 管理 iptables。** 专用网络上的 `enable_icc=false` 在原生 dockerd 上生效，在 OrbStack 上不生效。生产部署要写明这个前提。
3. **凭证进入容器的方式要重新定。** 只读根文件系统的容器不能用 `docker cp` 往 tmpfs 里写文件；在宿主机上建 tmpfs 再绑定挂载，又会碰到上面的路径问题。

不通过时的退路（调整挂载方案，或 runner 直接装在宿主机）不需要启用。

## 2 环境

| 环境 | 测试进程在哪里 | Docker daemon | 数据目录 |
| --- | --- | --- | --- |
| macOS | macOS 26.6.2 arm64，Go 1.27.3 | OrbStack，Docker Engine 29.8.2 | macOS 临时目录 |
| Linux：runner 在容器中 | `golang:1.27`（linux/arm64）容器，挂载宿主的 `/var/run/docker.sock` | 同上（OrbStack） | macOS 目录，以相同路径挂进测试容器 |
| Linux：原生 dockerd | `golang:1.27` 容器，通过共享卷里的 unix socket 连接 | `docker:29-dind`，Docker Engine 29.9.2，Alpine，自己管理 iptables | 命名卷，在测试容器和 dind 容器里挂在同一路径 `/var/lib/lerryn-s4` |

三者共用 OrbStack 的 Linux 内核（7.2.12，arm64，cgroup v2，cgroupfs 驱动）。第三种环境排除了 OrbStack 对网络和文件共享的改动，代表一台普通 Linux 主机上的 dockerd，但内核仍是同一个。还没有在 amd64、其他内核版本、systemd cgroup 驱动、rootless Docker 和 Podman 上跑过，建议的 CI 作业见第 9 节。

## 3 实现概要

接口在 `server/internal/executor/executor.go`，Docker 实现在 `server/internal/executor/docker/`，使用 Docker 官方 Go 客户端 `github.com/moby/moby/client` v0.6.2（API 类型在 `github.com/moby/moby/api` v1.56.1）。`github.com/docker/docker` 停在 v28.5.2，客户端已经拆到 moby 仓库下的独立模块，不再用旧路径。

| 操作 | 做法 |
| --- | --- |
| `Start` | 校验规格 → 检查镜像 → 创建容器 → attach → 启动。attach 在启动之前，不会漏掉最早的输出 |
| 输出 | 不经 TTY，stdout 和 stderr 分开，按 daemon 送达的顺序写给调用方的 `io.Writer`。写入方出错后，该流余下的内容丢弃，但仍然读完，避免任务被阻塞 |
| `Wait` | 等容器退出并且输出全部写完，再返回退出码和 OOM 标记 |
| `Stop` | 先注册退出通知，再发 SIGTERM；宽限期内没有退出就发 SIGKILL。daemon 的 wait 接口只作为提醒，是否停止以 inspect 看到"未运行"为准，另外每 500 毫秒轮询一次。确认不了就返回 `ErrNotStopped` |
| `Remove` | 不强制删除；删除后再 inspect 一次，确认容器已不存在 |
| `List` | 按 `lerryn.managed=true` 加调用方给的标签过滤，包括已退出的容器 |
| `Supervise` | 包在执行器外面的辅助函数：等待退出，超时或收到取消时调用 `Stop`。只有 `Stop` 确认之后才返回"已超时"或"已取消"；调用方的 context 结束时直接返回，不碰容器，留给对账处理 |

## 4 结果

单元测试不依赖 Docker：`config_test.go` 逐项固定"运行规格 → 容器配置"的映射，改动任何一项加固都会让它失败；`docker_test.go` 用模拟的 daemon 覆盖 `Stop` 的各种时序，包括 wait 接口提前返回、信号与退出竞争、daemon 始终不确认停止；`supervise_test.go` 覆盖超时、取消和调用方退出。

集成测试带 `integration` 构建标签，用 `make test-integration` 运行。三种环境的结果：

| 测试 | 内容 | macOS | Linux：容器中 | Linux：原生 dockerd |
| --- | --- | --- | --- | --- |
| `TestCancelStopsProcessTree` | A09：三层子进程，取消后容器和所有进程消失；响应 SIGTERM 与忽略 SIGTERM 各一次 | 通过 | 通过 | 通过 |
| `TestTimeoutStopsProcessTree` | A09：同上，超时触发 | 通过 | 通过 | 通过 |
| `TestStopConfirmsBeforeReturning` | 忽略 SIGTERM 的任务，`Stop` 返回时容器已是 exited；重复 `Stop`、删除后 `Stop` 的行为 | 通过 | 通过 | 通过 |
| `TestMemoryLimit` | 主进程超内存：退出码 137，`OOMKilled`；子进程超内存、主进程正常退出：退出码 0，`OOMKilled` 仍为真 | 通过 | 通过 | 通过 |
| `TestHardening` | 非 root、只读根文件系统、tmpfs、capabilities、no-new-privileges、seccomp、资源上限、init、挂载、标签，共 10 项 | 通过 | 通过 | 通过 |
| `TestPidsLimitContainsForkBomb` | 进程数上限 32，fork 炸弹被拒绝（EAGAIN），进程数峰值 32，停止后无残留进程 | 通过 | 通过 | 通过 |
| `TestNetworkNone` | 只有 `lo`，IP 和 DNS 都不通 | 通过 | 通过 | 通过 |
| `TestNetworkFull` | 只接入专用网络；一个任务访问不到另一个任务监听的端口 | 隔离项跳过 | 隔离项跳过 | 通过（强制检查） |
| `TestMounts` | 只读、可写挂载；源目录不存在时创建失败且不留容器 | 通过 | 通过 | 通过 |
| `TestOutputStreams` | 3000 行 stdout 和 3000 行 stderr 不丢、不串，`Wait` 返回时最后一行（无换行）已到 | 通过 | 通过 | 通过 |
| `TestListByLabels` | 按 `run_id`、`runner_id` 列出；模拟 runner 重启后停止并删除自己的容器 | 通过 | 通过 | 通过 |
| `TestRejectsImageWithVolumes` | 声明了 `VOLUME` 的镜像被拒绝 | 通过 | 通过 | 通过 |
| `TestRemoveRefusesRunningContainer` | 运行中的容器不会被 `Remove` 删掉 | 通过 | 通过 | 通过 |
| `TestExitCode`、`TestSuperviseExitBeatsTimeout`、`TestCheckDaemon` | 退出码和时间、正常退出先于超时、daemon 能力检查 | 通过 | 通过 | 通过 |

整套集成测试在 macOS 上约 6 秒，Linux 容器中约 6 秒，原生 dockerd 约 10 秒（多出的是网络隔离检查里 3 秒的连接超时）。最后一版代码在 macOS 上跑了 5 遍，两种 Linux 方式各跑了 1 遍（之前的版本各跑过 1 到 2 遍），都没有失败。

开发中出现过一次不稳定：最初的 fork 炸弹（`b() { b | b & }`）在进程数上限下会自行熄灭，因为每个进程 fork 完立即退出，fork 失败又会断掉整条分支。改成循环 fork，并以"fork 返回 EAGAIN"作为炸弹确实撞到上限的证据，而不是依赖某次采样恰好碰到峰值。

几个时间数据（宽限期 2 秒）：

| 场景 | macOS | Linux：容器中 | Linux：原生 dockerd |
| --- | --- | --- | --- |
| 取消，任务响应 SIGTERM，到确认停止 | 76–83 毫秒，退出码 143 | 77 毫秒 | 45 毫秒 |
| 取消，任务忽略 SIGTERM，到确认停止 | 2.05 秒，退出码 137 | 2.05 秒 | 2.05 秒 |

**怎么确认"所有进程都消失"。** 测试给每个子进程的命令行带上一个随机数，取消前后各启动一次辅助容器，在宿主机 PID 命名空间里扫描 `/proc/*/cmdline`。取消前必须扫到至少 3 个，删除容器后必须是 0 个。这样检查的是宿主机上真实存在的进程，脱离了容器 PID 命名空间或 cgroup 的进程也会被发现。macOS 上的"宿主机"是 OrbStack 的 Linux 虚拟机。

**清理。** 测试创建的容器、网络和镜像都带 `lerryn.test=<本次运行 ID>` 标签。`TestMain` 结束时按标签删除，再检查一遍，有残留就让测试失败。每轮测试后用 `docker ps -a --filter label=lerryn.test` 和 `docker network ls --filter label=lerryn.test` 核对过，都为空。

## 5 默认加固逐项

| 加固项 | 配置 | 容器内看到的结果 | 是否生效 |
| --- | --- | --- | --- |
| 非 root 用户 | 默认 `65532:65532`；只接受数字 uid:gid，拒绝 uid 0、gid 0 和只给 uid 的写法 | `id` 为 65532:65532 | 生效 |
| 只读根文件系统 | `ReadonlyRootfs` | 写 `/`、`/etc` 报 Read-only file system | 生效 |
| `/tmp` tmpfs | `rw,noexec,nosuid,nodev,size=64MiB,mode=1777` | 可写，不可执行 | 生效 |
| 凭证 tmpfs | `/run/secrets`：`noexec,nosuid,nodev,size=1MiB,mode=0700,uid,gid` 为任务用户 | 权限 700，属主 65532，任务用户可写 | 生效 |
| 去掉全部 capabilities | `CapDrop: ALL` | CapInh、CapPrm、CapEff、CapBnd、CapAmb 全为 0 | 生效 |
| no-new-privileges | `no-new-privileges:true` | `NoNewPrivs: 1` | 生效 |
| 默认 seccomp | 不传任何 `seccomp=` | `Seccomp: 2`（已加载过滤器） | 生效 |
| CPU | `NanoCPUs`，默认 1 核 | `cpu.max` 为 `50000 100000`（0.5 核时） | 生效 |
| 内存 | `Memory`，`MemorySwap` 等于 `Memory`，即不用 swap | `memory.max` 与设定一致，`memory.swap.max` 为 0；超限时被 OOM 杀掉 | 生效 |
| 进程数 | cgroup `PidsLimit`，默认 256 | `pids.max` 与设定一致；fork 炸弹停在上限 | 生效 |
| 打开文件数 | `nofile` 软硬上限，默认 1024 | `ulimit -n` 与设定一致 | 生效 |
| 只挂载指定目录 | 只用 `Mounts`（type bind），不用 `Binds`；拒绝 Docker socket、`/`、`/run`、`/var/run`，以及覆盖 `/proc`、`/sys`、`/dev`、`/tmp`、`/run/secrets` 的挂载 | 除 `/tmp` 和 `/run/secrets` 外只有显式挂载的目录 | 生效 |
| 专用网络 | `full` 接入 `lerryn-tasks` 网桥（`enable_icc=false`）；`none` 时 `NetworkMode: none` | `none`：只有 `lo`；`full`：只接入专用网络 | 生效；任务间隔离见第 6 节 |
| 标签 | `lerryn.managed`、`lerryn.run_id`、`lerryn.runner_id` | inspect 可见；`List` 可按标签过滤 | 生效 |

此外加了几项第 7.2 节没有列出的设置，理由都写在代码注释里：

- **init 进程**（`Init: true`，即 docker-init/tini）。没有 init 时任务就是 PID 1，内核不会对没装处理函数的 PID 1 投递 SIGTERM，每次取消都要等满宽限期再 SIGKILL。实测有 init 时，响应 SIGTERM 的任务 80 毫秒内就停了。init 还负责回收孤儿进程。
- **日志驱动为 `none`**，输出只通过 attach 交给 runner。用 json-file 等驱动时，未脱敏的输出会写到 runner 主机磁盘上。
- **`/dev/shm` 固定为 64 MiB**、IPC 命名空间私有、cgroup 命名空间私有、`OomScoreAdj` 设为 500（宿主内存紧张时先杀任务，不先杀 runner、server 或数据库）、禁用重启策略。
- **拒绝声明了 `VOLUME` 的镜像。** Docker 会给这类路径自动建可写的匿名卷，绕过只读根文件系统和"只挂载需要的目录"。
- **daemon 返回告警时直接失败。** 例如 cgroup 控制器没有委派时，Docker 只给一条"PIDs limit discarded"告警，容器照样运行。执行器遇到任何创建告警都会删除容器并返回 `ErrHardening`。
- **启动前自检。** `Check` 读取 `docker info`，缺少 seccomp、内存、CPU 配额或进程数限制中的任何一项就报错，runner 应据此拒绝领取运行。

## 6 OrbStack 与 Linux 的差异

| 项 | OrbStack | 原生 dockerd | 影响 |
| --- | --- | --- | --- |
| 专用网桥上的 `enable_icc=false` | **不生效**，一个任务能访问另一个任务的端口。`docker info` 报告的防火墙后端是 iptables，原因没有深究，推测是 OrbStack 自己处理了容器之间的转发 | 生效 | macOS 只能用于开发。测试在 OrbStack 上跳过这一项并说明原因；设置 `LERRYN_TEST_REQUIRE_ICC=1` 时改为失败，原生 dockerd 下按强制检查跑过 |
| `/dev/shm` 默认大小 | 约 8 GiB（虚拟机内存的一半） | 64 MiB | 执行器已显式设为 64 MiB。tmpfs 页本来就计入内存 cgroup，所以不设也超不过内存上限 |
| 绑定挂载的文件属主 | 任务写出的文件，在 macOS 上显示为当前用户（501:20），在容器里经 macOS 共享目录访问时显示为 0:0 | 65532:65532 | 在 Linux 上，runner 要把可写目录交给任务用户（chown 或同 uid 运行），见第 7 节。macOS 上测试结果不能说明权限问题 |
| 可挂载的源路径 | macOS 路径（`/Users`、`/private`、`/var/folders` 等）可以直接作为绑定挂载源 | 只能是 daemon 所在主机上的路径 | 见第 7 节 |
| 默认 `nofile` | 20480 | 依主机配置 | 执行器总是显式设置，不依赖默认值 |
| AppArmor | daemon 未启用 | 依发行版；Ubuntu 上默认加载 `docker-default` | 执行器不覆盖 AppArmor 配置，有就用 |

内核行为（cgroup v2 限额、OOM、seccomp、PID 命名空间的回收）在三种环境下一致，因为用的是同一个内核。这部分还需要在一台真正的 Linux 主机上复核，见第 9 节。

## 7 挂载路径

**问题。** runner 以容器运行时，它看到的路径是自己容器里的路径，而 daemon 解析绑定挂载源时用的是宿主机路径。两边不一致时，任务容器拿不到正确的目录。

**同路径方案可行。** 测试进程在 `golang:1.27` 容器里，数据目录以相同路径挂进去，`TestMounts` 和整套测试都通过：OrbStack 下是 macOS 目录同路径挂载，原生 dockerd 下是命名卷在两个容器里挂在同一路径。

**路径不一致时的表现，取决于用哪个 API：**

| 写法 | 源路径在宿主机上不存在时 |
| --- | --- |
| `HostConfig.Binds`（`docker run -v`） | **静默在宿主机上建一个空目录**挂进去，任务看到空目录，没有任何报错 |
| `HostConfig.Mounts` type bind（`--mount`，执行器用的就是这种） | 创建失败：`invalid mount config for type "bind": bind source path does not exist` |

执行器只用 `Mounts`，所以路径不一致会在创建容器时明确失败，不会让任务在空目录上运行。

**第 15.2 节的 Compose 示例有这个问题。** 示例中 all-in-one 用命名卷 `lerryn-data:/var/lib/lerryn`，宿主机上的实际路径是 `/var/lib/docker/volumes/lerryn-data/_data`，和容器内的 `/var/lib/lerryn` 不同。复现：把一个命名卷挂到测试容器的 `/var/lib/lerryn`，再让测试在其中建目录，结果每个容器都创建失败，报上面那条错误。

**两种改法，都验证过：**

1. **同路径绑定挂载。** Compose 中写 `/var/lib/lerryn:/var/lib/lerryn`。改动最小，要求部署者在宿主机上准备这个目录。
2. **命名卷 + 子路径。** runner 不再把宿主机路径交给 daemon，而是请求挂载"卷 `lerryn-data` 中的 `runs/<id>/work`"（`--mount type=volume,src=lerryn-data,dst=/sbx/work,volume-subpath=runs/<id>/work`）。在原生 dockerd 上手工验证过：配合只读、非 root、去掉 capabilities 的容器，可以读到 runner 写入的文件，只读挂载也照常拒绝写入。这种方式不需要知道宿主机路径，命名卷、绑定挂载、远程 daemon 都能用。代价是需要 Docker Engine 26 以上（API 1.45），Podman 的支持情况还没验证。

建议首版采用第 2 种作为容器化 runner 的默认方式，宿主机上直接运行的 runner 仍用绑定挂载；第 1 种写进部署文档作为备选。`executor.Mount` 需要增加"卷名 + 子路径"的形式。

**目录属主。** 任务以 65532 运行，runner 建的工作目录和输出目录必须让这个用户可写。在原生 dockerd 下，任务写出的文件属主就是 65532:65532。runner 有两种做法：以 root 运行并 chown；或者以 65532 运行（`lerryn` 镜像本来就是 distroless nonroot，uid 65532），这时要通过 `group_add` 加入 Docker socket 所属的组。

**符号链接。** 绑定挂载源如果本身是符号链接，daemon 会在宿主机上解析它。runner 只应挂载自己新建的目录，挂载前检查路径上没有符号链接；目录里面的符号链接在容器的挂载命名空间里解析，不会指到宿主机。执行器目前只做字面检查（绝对路径、无 `..`、在 `AllowedMountRoots` 之内），这一项留给 runner 实现。

## 8 对技术设计的修改建议

只列建议，技术设计本身没有改。

| 位置 | 建议 |
| --- | --- |
| 第 15.2 节 | Compose 示例改为同路径绑定挂载，或者按第 7 节第 2 种方式让 runner 使用"命名卷 + 子路径"，并在示例里写明。补充：runner 以非 root 运行时需要 `group_add` Docker socket 的组 |
| 第 7.2 节 | 默认加固列表补上：init 进程、日志驱动 `none`、`/dev/shm` 上限、私有 IPC 和 cgroup 命名空间、`OomScoreAdj`、不使用 swap、拒绝声明 `VOLUME` 的镜像、创建告警即失败、runner 启动时自检。写明 `/tmp` 是 `noexec`，运行时镜像不能依赖在 `/tmp` 里执行文件 |
| 第 7.2 节 | 写明只使用 `Mounts` 而不是 `Binds`，挂载源不存在时创建失败 |
| 第 7.1 节、7.5 节 | `full` 网络用专用网桥并关闭 ICC，任务之间不能互访。这依赖 dockerd 自己管理 iptables（`"iptables": false` 或 OrbStack 下不生效），写进部署要求和已知限制。另外写明：`full` 网络下任务仍能访问宿主机网关和宿主机上发布的端口，数据库和 server 不应监听在 runner 主机可达的地址上 |
| 第 7.1 节 | "进程数上限"明确用 cgroup pids 限制，不用 `RLIMIT_NPROC`。所有任务都是 uid 65532，`RLIMIT_NPROC` 按 uid 计数，会让并发的任务互相挤占 |
| 第 12.5 节、7.2 节 | 凭证不能用 `docker cp` 写进只读容器的 tmpfs（daemon 报 `container rootfs is marked read-only`），在宿主机建 tmpfs 再挂载又会碰到路径问题。建议由容器内的 `sbx` 入口程序从 attach 的 stdin 读取凭证，写入 `/run/secrets` 后再 exec 任务入口；环境变量方式会出现在 `docker inspect` 中，只作为清单里 `as: env:` 的兼容手段。需要在 M4 前定下来 |
| 第 9.4 节、5.4 节 | 增加错误类别 `out_of_memory`。`OOMKilled` 只表示容器里有进程被 OOM 杀过，主进程照样可能以 0 退出。建议规则：退出码非 0 且 `OOMKilled` → 执行失败，`out_of_memory`；退出码 0 且 `OOMKilled` → 执行完成，另记一条告警事件 |
| 第 7.6 节 | 补充三条：取消或超时与进程自然退出同时发生时，以实际退出为准，报执行完成或执行失败；`Stop` 以 inspect 结果为准，不只依赖 daemon 的 wait 通知；停止过程中容器消失，按"容器异常消失"处理为运行中断 |
| 第 4.6 节 | 执行器只交出按流分开的原始字节，分行、截断和脱敏在 runner 的日志管道里做。stdout 和 stderr 之间的相对顺序不保证 |
| 第 16.4 节 | 增加执行器集成测试的 CI 作业，见下一节 |

## 9 未覆盖的部分

- 没有在真正的 Linux 主机上运行。三种环境用的是同一个 OrbStack 内核（arm64）。建议在 CI 加两个作业：GitHub Actions 的 `ubuntu-latest`（amd64、systemd cgroup 驱动、Ubuntu 的 AppArmor）上直接跑 `make test-integration`，并设置 `LERRYN_TEST_REQUIRE_ICC=1`；再在 `golang:1.27` 容器里跑一遍，数据目录同路径挂载。
- rootless Docker、Podman、userns-remap 没有验证。rootless 下 cgroup 控制器需要委派，否则会出现上面提到的"limit discarded"告警，执行器会拒绝运行。
- 凭证注入、版本包和环境目录的挂载、状态目录、事件文件都属于后续里程碑，这里只预留了 `/run/secrets` tmpfs。
- 执行器不负责拉取镜像。运行时镜像按 digest 由 runner 在准备阶段拉取。
- daemon 重启、runner 在 `Stop` 中途崩溃这类故障注入，留到 M1 的 A10 一起做。

## 10 复现

```bash
# macOS（或任何能直接访问 Docker 的主机）
make test-integration

# Linux：runner 在容器中，数据目录同路径挂载
SHARED=/tmp/lerryn-shared && mkdir -p "$SHARED"
docker run --rm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$PWD:$PWD:ro" -w "$PWD" \
  -v "$SHARED:$SHARED" -e LERRYN_TEST_MOUNT_ROOT="$SHARED" \
  golang:1.27 make test-integration
```

测试读取的环境变量：

| 变量 | 作用 |
| --- | --- |
| `LERRYN_TEST_IMAGE` | 测试镜像，默认 `busybox:1.37.0`，不存在时自动拉取 |
| `LERRYN_TEST_MOUNT_ROOT` | 挂载测试建目录的位置。测试进程在容器里时，必须是容器内外路径相同的目录 |
| `LERRYN_TEST_REQUIRE_ICC` | 设为 `1` 时，任务之间网络不隔离会让测试失败，而不是跳过 |
| `DOCKER_HOST` | daemon 地址，和 Docker CLI 一致 |
