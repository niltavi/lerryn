# Lerryn 目标环境验证手册

这份手册写给在目标 Linux 主机上做发布前验证的人：每一步要执行的命令、预期结果，以及结果记到哪里。它对应实施规划"首个企业试用版"中还差的几项：本地以外的完整检查、B01 的 24 小时运行、带真实容器的容量复测、原生 dockerd 上的容器间隔离、rootless Docker 和 Podman 的兼容情况，以及和企业 OIDC 提供方的联调。

Gitea Actions 的 runner 配置见 [CI 配置](ci.md)。CI 首次运行通过前，以本地检查和本手册的验证结果作为发布依据；启用 CI 后，目标环境验证仍需完成。每次验证都针对一个确定的提交或候选 tag（例如 `v0.9.0-rc.1`），记录时写明。

## 1 主机准备

目标规格：一台原生 Linux 主机（不是 Docker Desktop、OrbStack 这类桌面环境），x86_64 或 arm64，4 vCPU、8 GiB 内存以上，40 GiB 可用磁盘。下面以 Ubuntu 24.04 为例。

```bash
# Docker Engine（官方源），dockerd 自己管理 iptables
sudo apt-get update && sudo apt-get install -y ca-certificates curl git jq make build-essential
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER" && newgrp docker
docker info --format '{{.Driver}} {{.CgroupVersion}} {{.SecurityOptions}}'
#   预期：overlay2 2 [name=apparmor name=seccomp,profile=builtin name=cgroupns]

# Go 1.27 的最新补丁版本
GO_VERSION=$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -1)   # 确认是 go1.27.x
curl -fsSL "https://go.dev/dl/${GO_VERSION}.linux-$(dpkg --print-architecture).tar.gz" | sudo tar -C /usr/local -xz
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH

# Node 24 和 pnpm（版本由 package.json 的 packageManager 决定）
curl -fsSL https://deb.nodesource.com/setup_24.x | sudo bash - && sudo apt-get install -y nodejs
sudo corepack enable

# golangci-lint，与 CI 相同的版本
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b "$HOME/go/bin" v2.15.0
```

确认 dockerd 管理 iptables（否则第 5 节的隔离检查没有意义）：

```bash
docker info --format '{{json .}}' | jq '.Warnings'            # 不应有 iptables 相关的警告
sudo iptables -S DOCKER-USER >/dev/null && echo "iptables managed by dockerd"
grep -s iptables /etc/docker/daemon.json                        # 没有 "iptables": false
```

取代码并安装依赖：

```bash
git clone https://git.yarinz.com/niltavi/lerryn.git && cd lerryn
git checkout v0.9.0-rc.1                                       # 被验证的候选版本
pnpm install --frozen-lockfile
go mod download
docker pull busybox:1.37.0 postgres:17
```

记录：主机规格（`nproc`、`free -g`、`uname -r`）、`docker version`、`go version`、被验证的提交。

## 2 完整检查

```bash
make check
make generate-check
```

PostgreSQL 上的测试，先起一个临时的 PostgreSQL 17：

```bash
docker run -d --name lerryn-test-pg -e POSTGRES_USER=lerryn -e POSTGRES_PASSWORD=lerryn \
  -e POSTGRES_DB=lerryn_test -p 127.0.0.1:5432:5432 postgres:17
until docker exec lerryn-test-pg pg_isready -U lerryn; do sleep 1; done
make test-pg
```

技术设计第 16.4 节承诺的最低版本是 PostgreSQL 15，CI 只跑 17。在这台主机上补跑 15：

```bash
docker rm -f lerryn-test-pg
docker run -d --name lerryn-test-pg -e POSTGRES_USER=lerryn -e POSTGRES_PASSWORD=lerryn \
  -e POSTGRES_DB=lerryn_test -p 127.0.0.1:5432:5432 postgres:15
until docker exec lerryn-test-pg pg_isready -U lerryn; do sleep 1; done
make test-pg
docker rm -f lerryn-test-pg
```

执行器和验收的集成测试，要求容器间隔离生效：

```bash
LERRYN_TEST_REQUIRE_ICC=1 make test-integration
```

runner 自身在容器中运行的情况（与 CI 的 `go-integration-in-container` 作业相同）。只跑执行器、runner 和服务端入口的测试：`golang:1.27` 镜像里没有 `docker` 命令，验收、对象存储和评测示例的测试要用它启动依赖；任务容器也只能访问 Docker 主机，访问不到这个容器里的测试服务器，依赖它的工具调用测试会说明原因并跳过（宿主机上的那一遍已经覆盖）。

```bash
mkdir -p /tmp/lerryn-shared
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$PWD:$PWD:ro" -w "$PWD" -v /tmp/lerryn-shared:/tmp/lerryn-shared \
  -e LERRYN_TEST_MOUNT_ROOT=/tmp/lerryn-shared -e LERRYN_TEST_REQUIRE_ICC=1 \
  golang:1.27 go test -race -count=1 -tags integration ./server/internal/executor/... ./server/internal/runner/... ./server/cmd/...
```

预期：全部通过，没有跳过 `TestNetworkFull` 中的隔离检查。记录每条命令的结果和用时。

## 3 B01 的 24 小时运行

PRD 要求多副本调度连续 24 小时每个计划分钟恰好一条运行。测试启动 postgres:17 容器、三个 server 和两个 runner，在一个计划时间前强制结束主节点：

```bash
tmux new -s b01
LERRYN_B01_MINUTES=1440 go test -tags integration -run 'TestMultiNode/B01' -v -timeout 26h ./tests/acceptance/ \
  2>&1 | tee b01-$(date -u +%Y%m%dT%H%M%SZ).log
```

`-timeout` 要大于 24 小时，否则 Go 测试在默认的 10 分钟后中止。跑之前确认主机不会休眠、磁盘有余量（日志和数据库约几 GiB）。

预期：测试通过，日志末尾报告的每个计划分钟都有一条已完成的运行，没有缺失和重复。结束后确认没有遗留容器：

```bash
docker ps -a --filter label=lerryn.test --format '{{.Names}}'   # 应为空
```

记录到[验收矩阵](acceptance-matrix.md)的 B01 一行。

## 4 容量复测

### 4.1 控制面

与[容量测试](capacity.md)第 2 节相同的测试，换到目标主机上：

```bash
LERRYN_CAPACITY_OUT=$PWD/capacity-$(date -u +%Y%m%d) \
  go test -tags capacity -run TestCapacity -v -timeout 30m ./tests/capacity/
cat capacity-*/results.md
```

预期：两种拓扑的各项指标达到 capacity.md 第 1 节的目标。把 `results.md` 的表格补进 capacity.md 第 3 节，注明主机规格。

### 4.2 带真实容器

4.1 的 runner 是合成的，不启动容器。这一步用参考部署和真实 runner，在 PRD 的规模下跑真实容器。

1. 在这台主机上按 [deploy.md](deploy.md) 第 3 节启动标准拓扑（`deploy/multi-node/`），`LERRYN_VERSION` 设为被验证的版本。把 `deploy/multi-node/runner.yaml` 的 `slots` 改为 25，两个 runner 合计 50 个槽位。参考部署的指标已开启，在容器网络 `lerryn_backend` 内的 `server-1:9090`。
2. 注册一个账号，登录 CLI，准备一个每秒输出 500 行、持续 20 秒的任务：

   ```bash
   lerryn login https://<主机>:<端口> --username capacity
   mkdir -p /tmp/load-task && cd /tmp/load-task
   cat > lerryn.yaml <<'EOF'
   name: load-500lps
   description: 容量测试：每秒 500 行，持续 20 秒
   runtime: {language: shell, version: "5"}
   entrypoint: [bash, main.sh]
   network: none
   concurrency: allow
   limits: {timeout: 5m, cpu: 0.5, memory: 128Mi}
   EOF
   cat > main.sh <<'EOF'
   #!/usr/bin/env bash
   set -euo pipefail
   for s in $(seq 1 20); do
     for i in $(seq 1 500); do echo "second $s line $i"; done
     sleep 1
   done
   sbx result --status ok --summary "done"
   EOF
   lerryn tasks apply .
   ```

3. 一次发起 50 个测试运行，等全部结束：

   ```bash
   for i in $(seq 1 50); do lerryn runs start load-500lps --test -o json | jq -r '.id'; done > run-ids.txt
   start=$(date +%s)
   while read -r id; do lerryn runs wait "$id" --timeout 15m -o json | jq -r '[.id, .status] | @tsv'; done < run-ids.txt | sort -k2 | uniq -c -f1
   echo "elapsed $(( $(date +%s) - start ))s"
   ```

4. 运行期间每 5 秒采样一次指标和主机负载：

   ```bash
   while sleep 5; do
     docker run --rm --network lerryn_backend curlimages/curl:8.11.1 -s http://server-1:9090/metrics | grep -E '^lerryn_(run_queue_depth|run_queue_oldest_seconds|runner_slots_in_use|runs\{)'
     uptime; docker stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}' | grep -E 'server|postgres|runner'
   done | tee load-metrics.log
   ```

预期：50 个运行全部 `completed`；每个运行的日志正好 10000 行任务输出，最后一行任务输出为 `second 20 line 500`，之后是平台追加的退出记录。不带 `--follow` 的 `lerryn runs logs` 只打印一页，数行数时用 `lerryn runs logs <id> --follow -o text | grep -c '^second '`；`lerryn_run_queue_oldest_seconds` 在发起后很快回到 0；控制台在负载期间打开运行详情不超过 3 秒，跟随的日志 2 秒内出现。记录总用时、单个运行从发起到开始执行的时间（运行详情的 `started_at` 减 `created_at`）、server 和 PostgreSQL 的 CPU 与内存峰值，补进 capacity.md。

单机拓扑用同样的方法，以 `deploy/single-node/` 部署、`runner.slots: 5`、5 个运行重复一遍。

## 5 容器间隔离（enable_icc）

`full` 网络的任务之间不能互访，靠的是任务网络的 `com.docker.network.bridge.enable_icc=false`，在 OrbStack 上不生效（技术设计第 20 节）。第 2 节的 `LERRYN_TEST_REQUIRE_ICC=1` 已经在测试里检查了一次，这里再手工确认一次，并记下主机的规则：

```bash
go test -tags integration -race -run 'TestNetworkFull' -v ./server/internal/executor/docker/ 2>&1 | tee icc.log
grep -E 'PASS|SKIP|FAIL' icc.log                                 # 预期 PASS，没有 SKIP
```

手工复现：

```bash
docker network create --driver bridge -o com.docker.network.bridge.enable_icc=false lerryn-icc-check
docker run -d --rm --name icc-a --network lerryn-icc-check busybox:1.37.0 httpd -f -p 8080
A=$(docker inspect -f '{{(index .NetworkSettings.Networks "lerryn-icc-check").IPAddress}}' icc-a)
docker run --rm --network lerryn-icc-check busybox:1.37.0 wget -T 3 -qO- "http://$A:8080/" \
  && echo "ISOLATION BROKEN" || echo "isolated"
docker run --rm --network lerryn-icc-check busybox:1.37.0 wget -T 3 -qO /dev/null http://example.com/ \
  && echo "outbound ok"
docker rm -f icc-a; docker network rm lerryn-icc-check
sudo iptables -S | grep -i -E 'icc|DOCKER-ISOLATION' | head
```

预期：第一个 `wget` 失败（`isolated`），第二个成功（`outbound ok`）。另外确认已知的边界仍然存在，写进记录：`full` 网络的任务能访问宿主机网关和宿主机上发布的端口。

```bash
docker network create --driver bridge -o com.docker.network.bridge.enable_icc=false lerryn-icc-check
GW=$(docker network inspect -f '{{(index .IPAM.Config 0).Gateway}}' lerryn-icc-check)
docker run --rm --network lerryn-icc-check busybox:1.37.0 sh -c "nc -z -w 3 $GW 22 && echo 'gateway reachable'"
docker network rm lerryn-icc-check
```

## 6 rootless Docker 和 Podman

技术设计第 21 节把这两种运行时列为尚未验证。这一节的目标是记录现状，不要求通过：哪些加固项生效、哪些测试失败、失败的原因。执行器通过 `DOCKER_HOST` 连接 daemon。

### 6.1 rootless Docker

用一个单独的普通用户：

```bash
sudo apt-get install -y uidmap dbus-user-session slirp4netns
sudo useradd -m lerryn-rootless && sudo loginctl enable-linger lerryn-rootless
sudo -iu lerryn-rootless
dockerd-rootless-setuptool.sh install
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/docker.sock
docker info --format '{{.SecurityOptions}} {{.CgroupVersion}}'   # 含 name=rootless，cgroup 2
cat /sys/fs/cgroup/user.slice/user-$(id -u).slice/user@$(id -u).service/cgroup.controllers
#   需要含 cpu memory pids，否则资源限制不生效；缺少时按 Docker 文档开启 cgroup 委派
```

在这个用户下取代码（同第 1 节），然后：

```bash
docker pull busybox:1.37.0
go test -tags integration -race -v ./server/internal/executor/docker/ 2>&1 | tee rootless-executor.log
go test -tags integration -race -run TestM1 -v ./server/cmd/lerryn/ 2>&1 | tee rootless-m1.log
grep -E '^(--- )?(PASS|FAIL|SKIP)' rootless-*.log
```

关注：`TestHardening`（用户、capabilities、只读根文件系统）、`TestPidsLimitContainsForkBomb`、`TestMemoryLimit`、`TestNetworkFull`（rootless 的网络由 slirp4netns 或 pasta 提供，`enable_icc` 可能不适用）、`TestMounts`（数据目录要对 rootless 用户可写）。

### 6.2 Podman

```bash
sudo apt-get install -y podman
systemctl --user enable --now podman.socket
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/podman/podman.sock
docker version 2>/dev/null || podman version                    # Docker API 兼容层可用
podman pull docker.io/library/busybox:1.37.0
go test -tags integration -race -v ./server/internal/executor/docker/ 2>&1 | tee podman-executor.log
go test -tags integration -race -run TestM1 -v ./server/cmd/lerryn/ 2>&1 | tee podman-m1.log
grep -E '^(--- )?(PASS|FAIL|SKIP)' podman-*.log
```

root 模式的 Podman（`sudo systemctl enable --now podman.socket`，`DOCKER_HOST=unix:///run/podman/podman.sock`）也跑一遍。

记录：每个运行时下通过、失败、跳过的测试和失败原因，写进技术设计第 21 节"Podman、rootless Docker、userns-remap"一行，并在 deploy.md 写明支持范围。没有全部通过的运行时，发布说明里写为"不支持"。

## 7 OIDC 提供方联调

A18 的单点登录部分目前只用进程内的测试提供方验证过。这一步和企业实际使用的提供方（技术设计第 21 节"公司首个 OIDC 提供方"）联调，在第 4.2 节的参考部署或一个单机部署上进行，地址必须是 HTTPS。

1. 在提供方创建一个 OIDC 客户端（Web 应用，授权码流程）：

   - 重定向 URI：`https://<部署地址>/api/v1/auth/oidc/callback`，与 `server.public_url` 完全一致。
   - scope：`openid profile email`。
   - 只分配给参与试用的用户或组，另准备一个**没有**分配的测试账号。

2. 配置 server：

   ```yaml
   auth:
     oidc:
       issuer: https://idp.example.com/realms/corp     # 与提供方 discovery 文档中的 issuer 完全一致
       client_id: lerryn
       client_secret_file: /run/secrets/oidc-client-secret
       username_claim: preferred_username
   ```

   ```bash
   curl -fsS https://idp.example.com/realms/corp/.well-known/openid-configuration | jq '.issuer, .authorization_endpoint'
   docker compose restart server-1 server-2                       # 单机为 lerryn
   curl -fsS https://<部署地址>/api/v1/discover | jq '.capabilities.login_methods'   # 含 oidc
   ```

3. 逐项验证，在新的浏览器隐身窗口中进行：

   | 步骤 | 预期 |
   | --- | --- |
   | 控制台点单点登录，用已分配的账号登录 | 回到控制台，已登录；`lerryn api getMe`（用该账号的令牌）中的用户名取自 `preferred_username` |
   | 同一账号再登录一次 | 是同一个账号，没有新建 |
   | 用户名与已有内置账号相同的人第一次登录 | 新建账号，用户名带数字后缀，不会登录进已有账号 |
   | 用没有分配的测试账号登录 | 被提供方拒绝，Lerryn 中没有产生账号 |
   | 关闭开放注册后，新的已分配账号第一次登录 | 仍然能登录并建立账号（单点登录不受开放注册影响） |
   | 管理员在 Lerryn 中停用这个账号，再登录 | 被拒绝，没有产生会话 |
   | 篡改回调 URL 中的 `state`，或重放一次已用过的回调 URL | 被拒绝 |
   | 打开 `/api/v1/auth/oidc/start?return_to=//evil.example.com/` | 请求被拒绝（`validation_failed`），不会跳到外部地址 |
   | 关闭本地密码登录，普通用户用密码登录 | 被拒绝；管理员仍可用密码登录 |
   | 管理员查询审计日志（`lerryn api listAuditEvents`） | 有上面每次登录成功和失败的记录 |

4. 时钟偏差：`timedatectl` 确认 server 主机已同步时间。ID Token 的校验对时钟敏感，偏差过大时登录失败。

记录：提供方的产品和版本、上表每一步的结果、遇到的问题。结果更新到验收矩阵 A18 一行的"缺口"，以及技术设计第 21 节。

## 8 收尾

- 清理：`docker ps -a --filter label=lerryn.test`、`docker network ls --filter label=lerryn.test` 为空；第 4.2 节的参考部署 `docker compose down -v`。
- 汇总：每一节的结论、日志文件和结果目录，附在发布的记录里。
- 更新：[验收矩阵](acceptance-matrix.md)第 2、5 节，[容量测试](capacity.md)第 3 节，技术设计第 21 节，实施规划"首个企业试用版"的表格，以及[发布说明](release-notes/0.9.0.md)第 6.3 节。
