# Niltavi UI 主题来源

`tokens.json` 原样取自 [niltavi/niltavi-ui](https://github.com/niltavi/niltavi-ui/tree/070e63c3be6ae6807ab907c9d1ca2614993614f7) 的 `src/theme/tokens.json`，固定提交为 `070e63c3be6ae6807ab907c9d1ca2614993614f7`。上游尚未发布 npm 组件包；这里只保存主题快照，不引入展示站及其依赖。许可证为 MIT，原始声明保存在 `LICENSE`，发布时由 `scripts/notices.mjs` 汇入 `THIRD_PARTY_NOTICES`。

`design/tokens.json` 的 `source` 引用快照里的 `resolved.light` 和 `resolved.dark`。`design/scripts/sync-tokens.mjs` 将其映射到现有 Lerryn token 和 HeroUI 语义变量，供 Web、Native、原型与独立凭证填写页共用。`kraft-*`、`flute-*` 和 `Raised` 的旧名字保留兼容；界面不再绘制厚度或按压下沉。

为满足 Lerryn 已有的对比度要求，状态文字使用上游 `*-soft-foreground`；控件边框和占位文字使用 `muted`；焦点环使用 `accent`。Logo 使用 Lerryn 的溪水蓝绿与深青，专用浅色底板保持纸白。

升级时重新选择一个上游提交、原样替换主题快照与许可证，并同步此处和 `scripts/notices.mjs` 中的提交号。运行 `pnpm design:tokens`、`pnpm design:test`、`make notices`、`make check`，再核对深浅主题和移动端显示。不要直接编辑快照中的单个颜色。
