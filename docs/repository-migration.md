# Lerryn 仓库搬迁

2026 年 11 月 5 日，Lerryn 源码完整迁移到 `/Users/yarinz/Personal/CodeLibrary/Personal/niltavi-lerryn`，作为 Niltavi 产品线的独立项目继续开发。Waleng 仓库停止功能更新与维护，后续改动均在新目录完成。

新仓库使用全新的 Git 根提交，不继承原仓库的提交、分支、标签、remote 或 worktree 元数据。保留完整源文件、许可证、第三方声明、测试与文档；依赖、构建产物和本地运行数据不进入版本库。

- GitHub：`https://github.com/niltavi/lerryn`
- Gitea：`https://git.yarinz.com/niltavi/lerryn`
- Go module：`github.com/niltavi/lerryn`
- 镜像目标：`ghcr.io/niltavi/lerryn`
- 官网目标：`https://lerryn.niltavi.com`

两个远端使用同一个 `main` 分支和相同的提交。Git 提交标题与正文使用英文，按 Git 命令式标题风格书写。仓库隐私沿用原项目的私有设置，公开发布由维护者另行决定。

本次搬迁不发布容器镜像、不修改域名 DNS，也不部署官网。已有部署的配置、协议前缀与兼容格式见[改名与升级](rename-to-lerryn.md)。原仓库只保留作为搬迁来源，不再同步新项目提交。
