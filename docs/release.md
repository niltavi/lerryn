# Lerryn 发布流程

这份文档写给发布 Lerryn 的维护者：版本号怎么定、怎么打 tag、怎么在本地构建和发布镜像与二进制，以及第三方许可证声明怎么维护。GitHub Actions 关闭期间，本地脚本就是发布流水线。

发布前要完成的检查在另外两份文档里：[人工审查清单](review-checklist.md)和[目标环境验证手册](target-env-runbook.md)。

## 1 版本号

### 1.1 方案

版本号遵循 SemVer，git tag 写作 `vX.Y.Z`，候选版本写作 `vX.Y.Z-rc.N`。

首个企业试用版定为 **0.9.0**，不用 1.0.0-rc：

- 1.0.0 意味着承诺接口稳定。技术设计第 16.3 节约定 oasdiff 在第一个版本发布后才加入，契约的破坏性变更还没有自动检查；人工审查和目标环境验证也还没做完。现在叫 1.0.0-rc，就等于提前承诺了做不到的事。
- 试用的目的是收集真实反馈，反馈可能要求调整契约。0.x 的次版本号可以承载这类调整，升到 0.10.0 或直接进入 1.0.0 都顺理成章。
- 0.9 表明功能范围已经是首版的完整范围（PRD 的 P0），只差稳定性承诺。

此后的规则：

| 变化 | 版本 |
| --- | --- |
| 试用期间的修复 | 0.9.Z |
| 试用期间需要不兼容的契约或配置调整 | 0.10.0，发布说明写明迁移方法 |
| 审查、目标环境验证和试用反馈都处理完 | 1.0.0，此后契约在 `/api/v1` 内只做增量变更 |

发布候选版本（`-rc.N`）用于在目标环境上跑验证手册，通过后在同一提交上打正式 tag。

### 1.2 版本号出现在哪里

| 位置 | 值 | 说明 |
| --- | --- | --- |
| git tag | `v0.9.0` | 唯一的来源 |
| `buildinfo.Version`（`lerryn version`、`discover` 的 `server.version`、MCP 的实现版本、CLI 的 User-Agent） | `v0.9.0` | 构建时由 `git describe --tags` 写入，见 `Makefile` 的 `VERSION` 和 `Dockerfile` 的 `ARG VERSION`；没有 tag 的构建是 `<最近的 tag>-<提交数>-g<提交>` 或提交号，本地开发构建是 `dev` |
| `api/openapi/openapi.yaml` 的 `info.version`（`discover` 的 `api.spec_version`） | `0.9.0` | 契约版本，等于发布版本去掉 `v` 和候选后缀。`httpapi.SpecVersion` 由测试保持一致 |
| 镜像 tag | `0.9.0`，正式版另加 `0.9` | 不带 `v`，与 Compose 文件的 `LERRYN_VERSION` 一致；不发布 `latest`，部署方总是写明版本 |
| `deploy/*/compose.yaml` 和 `.env.example` 的 `LERRYN_VERSION` 默认值 | `0.9.0` | 随发布更新 |
| `runtimes/sdk/node/package.json` | `0.9.0` | sbx SDK 嵌入在二进制里，随发布版本走 |

`info.version` 在发布提交里改成新版本，之后的契约改动不再改它，直到下一次发布。发布脚本检查 tag 和 `info.version` 是否一致，不一致时拒绝构建。

## 2 发布步骤

以 0.9.0 为例，候选版本把 tag 换成 `v0.9.0-rc.N`。

1. 在 main 上准备发布提交：`info.version`、Compose 默认版本、SDK 版本改为新版本，运行 `make generate`；写好 `docs/release-notes/0.9.0.md`；`make notices`。
2. 本地跑 `make check`、`make generate-check`、`make test-pg`、`make test-integration`，全部通过。
3. 在目标 Linux 主机上按[目标环境验证手册](target-env-runbook.md)跑一遍；正式版之前完成[人工审查清单](review-checklist.md)。
4. 打 tag（带注释，必要时签名），推送 tag：

   ```bash
   git tag -a v0.9.0 -m "Lerryn 0.9.0"
   git push origin v0.9.0
   ```

   `.github/workflows/release.yml` 会在推送 `v*` tag 时构建并推送镜像，标签规则与本地脚本相同。Actions 关闭期间它不会运行；重新开启 Actions 后，二者只用一个，避免同一个 tag 被推送两次。

   候选版本和正式版打在同一个提交上时，`git describe` 可能取到其中任意一个，所以下面的命令都显式写出 `VERSION`。

5. 在 tag 对应的干净检出上构建并推送镜像。需要先 `docker login ghcr.io`，并有一个支持多架构的 buildx builder（`docker buildx create --use` 创建的 docker-container builder 即可）：

   ```bash
   git checkout v0.9.0
   VERSION=v0.9.0 RELEASE_PUSH=1 make release-image
   ```

6. 构建二进制和校验和：

   ```bash
   VERSION=v0.9.0 RELEASE=1 make release-binaries
   ```

   输出在 `dist/v0.9.0/`：四个 `lerryn_0.9.0_<os>_<arch>.tar.gz`（各含 `lerryn`、`LICENSE`、`THIRD_PARTY_NOTICES`）和 `SHA256SUMS`。

7. 在 GitHub 上建 release，正文用发布说明，附上 `dist/v0.9.0/` 下的全部文件：

   ```bash
   gh release create v0.9.0 --title "Lerryn 0.9.0" --notes-file docs/release-notes/0.9.0.md dist/v0.9.0/*
   ```

   候选版本加 `--prerelease`。

## 3 发布脚本

`make release-image` 和 `make release-binaries` 调用 `scripts/release.sh`。默认只构建，不推送任何东西。

| 变量 | 默认值 | 作用 |
| --- | --- | --- |
| `VERSION` | `git describe --tags --always --dirty` | 发布版本 |
| `IMAGE` | `ghcr.io/niltavi/lerryn` | 镜像仓库 |
| `PLATFORMS` | `linux/amd64,linux/arm64` | 镜像架构 |
| `RELEASE_PUSH` | 未设置 | 为 `1` 时推送镜像，并执行发布检查 |
| `RELEASE` | 未设置 | 为 `1` 时执行发布检查，不推送 |
| `RELEASE_LOAD` | 未设置 | 为 `1` 时把单一架构的镜像载入本机 daemon，用于本地验证 |
| `BUILDX_BUILDER` | 当前 builder | 指定 buildx builder |
| `DIST` | `dist` | 二进制的输出目录，已在 `.gitignore` 中 |

发布检查：`VERSION` 是 `vX.Y.Z` 或 `vX.Y.Z-rc.N`，与 `info.version` 一致，工作区干净，HEAD 正是这个 tag 指向的提交，`THIRD_PARTY_NOTICES` 是最新的。不是发布 tag 的构建只给出提示。

构建时间取提交时间，同一个 tag 重新构建得到同样的 `buildinfo.Date`。镜像带 OCI 标签（版本、提交、来源、许可证），`/usr/share/doc/lerryn/` 下有 `LICENSE` 和 `THIRD_PARTY_NOTICES`。

本地验证的写法：

```bash
# 构建本机架构并载入，然后检查版本
PLATFORMS=linux/arm64 RELEASE_LOAD=1 VERSION=v0.9.0-rc.0 IMAGE=lerryn-local make release-image
docker run --rm lerryn-local:0.9.0-rc.0 version

# 两种架构都构建一遍，不推送
VERSION=v0.9.0-rc.0 make release-image
```

不推送时，docker-container builder 把结果只留在构建缓存里；启用了 containerd 镜像存储的 docker 驱动（例如 OrbStack）会把多架构镜像存进本机。默认的 docker 驱动在不启用 containerd 镜像存储时不能一次构建多个架构，这时用 `docker buildx create --name lerryn-release --use` 建一个 docker-container builder。

## 4 第三方许可证声明

仓库根目录的 `THIRD_PARTY_NOTICES` 由 `scripts/notices.mjs` 生成，覆盖一个发布实际分发的第三方代码：

- `lerryn` 二进制链接的 Go 模块，按四个发布平台（darwin、linux 的 amd64、arm64）取并集，加上 Go 标准库。
- 嵌入二进制的 Web 控制台打包的 npm 包：从 `apps/web` 的 `dependencies` 出发，经过工作区包（`@lerryn/api`、`@lerryn/core`、`@lerryn/ui-web`）求出运行时依赖闭包，再加上 Vite（它向产物注入少量运行时代码）。`@types/*` 只有类型声明，不进入产物，不列入。
- 镜像的基础镜像 `gcr.io/distroless/static-debian12` 只说明位置：Debian 软件包的版权文件在镜像的 `/usr/share/doc/` 下。

不覆盖的内容：

- **移动端。** `apps/mobile` 单独分发，不在服务端镜像和二进制里。它的许可证声明在移动端打包时按它自己的依赖生成，随 App 提供。
- **开发和测试依赖。** 不分发。
- **任务运行时镜像。** runner 在运行时拉取的 Python、Node、Shell 镜像不属于 Lerryn 发布。

命令：

```bash
make notices        # 重新生成
make notices-check  # make check 的一部分
```

`make notices-check` 在以下情况失败：依赖有增减或版本变化而文件没有重新生成；某个依赖找不到许可证文件，或许可证无法识别；许可证不在允许清单内。允许清单在 `scripts/notices.mjs` 的 `ALLOWED`：0BSD、Apache-2.0、BSD-2-Clause、BSD-3-Clause、CC0-1.0、ISC、MIT、MPL-2.0、OFL-1.1、Unlicense，以及公有领域声明（SQLite）。它们都可以与 AGPL-3.0 的作品组合分发，也不影响 `runtimes/sdk/` 和 `skills/` 采用 Apache-2.0（这两部分目前没有任何第三方代码）。扩充允许清单，以及 `FILE_DECISIONS` 里对个别许可证文件的判断，都属于人工审查的范围。

0.9.0 的结果：Go 模块 79 个加标准库，npm 包 74 个，全部在允许清单内，没有与 AGPL-3.0 分发或 Apache-2.0 部分冲突的许可证。值得知道的几项：

| 依赖 | 许可证 | 说明 |
| --- | --- | --- |
| `@fontsource/ibm-plex-mono`、`@fontsource/sora` | 字体 OFL-1.1，包本身 MIT | 字体随控制台分发；OFL 允许与软件一起分发，限制是字体不能单独出售 |
| `modernc.org/sqlite` | BSD-3-Clause，SQLite 本身为公有领域 | 它附带的 `LICENSE-3RD-PARTY.md` 全文收录 |
| `modernc.org/libc` | BSD-3-Clause，内含 musl libc 等 MIT 代码 | 同上 |
| `github.com/modelcontextprotocol/go-sdk` | Apache-2.0 与 MIT | 许可证文件同时包含两者 |
| `isbot` | Unlicense | |
| `client-only` | MIT | 包里没有许可证文件，以 `package.json` 的声明为准 |
| `@heroui/react`、`@heroui/styles` | Apache-2.0 | 许可证文件是 Apache-2.0，`package.json` 写的是 MIT。两者都在允许清单内，通知以许可证文件为准；`package.json` 与许可证文件不一致的包，在通知中都会注明 |
| `github.com/opencontainers/go-digest` 的 `LICENSE.docs`、`modernc.org/memory` 的 `LICENSE-LOGO` | CC-BY-SA-4.0、徽标署名 | 分别针对文档和徽标，不进入二进制，未收录 |
