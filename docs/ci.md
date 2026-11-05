# Gitea Actions 配置

CI 与镜像发布在 `https://git.yarinz.com/niltavi/lerryn` 上运行，工作流位于 `.gitea/workflows/`。推送 `main` 和提交 PR 触发 `ci.yml`；推送 `v*` tag 触发 `release.yml`。GitHub 远端保留源码同步用途，镜像继续发布到 `ghcr.io/niltavi/lerryn`。

## 仓库与 runner

在 Gitea 仓库设置中启用 Actions，并注册能够领取本仓库作业的 runner。工作流统一使用 `runs-on: lerryn-linux`，runner 的标签配置为 `lerryn-linux:host`。标签的环境部分决定作业在宿主机还是容器中执行，参见 [Gitea runner 标签说明](https://docs.gitea.com/runner/labels/)。

这个 runner 应直接安装在专用的原生 Linux 主机上。执行器测试需要用本机 Docker socket 启动任务容器；其中一个作业还会把检出目录和临时数据目录按相同路径挂进 `golang:1.27` 容器。在普通作业容器里使用宿主机 socket 时，检出路径通常不在 Docker 宿主机上，会导致绑定挂载失败。因此这里显式选择宿主机执行，runner 进程本身也应直接运行在宿主机上。

主机需要：

- Git、Bash、curl、make、C/C++ 编译工具链和 Node.js 24。JavaScript action 执行前就需要 Node；`setup-node` 不能代替这个前提。
- 原生 Docker Engine 和 Buildx 插件，runner 用户可访问 `/var/run/docker.sock`。dockerd 管理 iptables，以便执行器验证容器间隔离。
- 可写的工具缓存、临时目录和工作目录，以及拉取 PostgreSQL、Go、运行时镜像所需的磁盘空间。
- 能访问本 Gitea 实例、GitHub action 仓库、Go/Node/pnpm 下载源、npm registry 和工作流使用的镜像仓库。工作流用完整 action URL，来源不受实例的 `DEFAULT_ACTIONS_URL` 影响。

每台主机只启动一个 runner，并设置 `runner.capacity: 1`。PostgreSQL 测试使用宿主机的 5432 端口；串行执行避免多个作业争用端口和宿主机工具链。增加并行能力时使用独立主机。宿主机 runner 仅用于可信代码，外部 PR 的执行需维护者审查。

工作流安装 Go 1.27 的最新补丁版本；pnpm 从根目录 `package.json` 读取固定版本，Node 使用 24，golangci-lint 固定为 2.15.0。CI 保留 7 个作业，覆盖 `make check` 的检查项、生成物一致性、PostgreSQL 17、宿主机与容器中的执行器测试，以及 amd64 镜像构建。

## 镜像发布凭证

在 Gitea 仓库的 Actions secrets 中设置：

| Secret | 内容 |
| --- | --- |
| `GHCR_USERNAME` | 有权发布 `ghcr.io/niltavi/lerryn` 的 GitHub 账号 |
| `GHCR_TOKEN` | 该账号的 GitHub classic PAT，具有 `write:packages`，并拥有目标组织和镜像包的写权限；组织要求 SSO 时需完成授权 |

Gitea 的作业令牌只用于检出当前仓库，不能替代 GHCR 的发布凭证。GHCR 在 GitHub Actions 以外的认证要求见 [GitHub Container registry 文档](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)。凭证只交给 `docker/login-action`，CI 工作流不使用发布凭证。

发布作业通过 QEMU 和 Buildx 构建 `linux/amd64`、`linux/arm64` 镜像；runner 需允许安装 binfmt 模拟器。正式 tag `v0.9.0` 产生 `0.9.0` 和 `0.9` 两个镜像标签；候选 tag `v0.9.0-rc.1` 只产生 `0.9.0-rc.1`。不发布 `latest`。

## 首次验证

先把改动推送到 Gitea，确认 `ci` 的 7 个作业全部通过，并将 Gitea 上的检查设为合并依据。若原先配置过 GitHub 必需检查，同时移除已停用的检查要求。之后按[发布流程](release.md)推送候选 tag，确认两种架构的镜像与版本信息。

本地静态检查不能代替这次 runner 验证。首次运行通过前，继续在本地和目标 Linux 主机运行 `make check`、`make generate-check`、`make test-pg` 与 `LERRYN_TEST_REQUIRE_ICC=1 make test-integration`。目标环境的其他验收项仍按[验证手册](target-env-runbook.md)执行。
