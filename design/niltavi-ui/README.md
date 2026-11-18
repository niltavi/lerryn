# Niltavi UI 来源

Lerryn 的界面遵循 [Niltavi UI](https://ui.niltavi.com/)（[niltavi/niltavi-ui](https://github.com/niltavi/niltavi-ui/tree/2d9d20d244e4ae5dcd7c4b85717fd6c9f8bf35e9)），固定提交为 `2d9d20d244e4ae5dcd7c4b85717fd6c9f8bf35e9`。上游尚未发布 npm 组件包，仓库里保存的是原样复制的文件。许可证为 MIT，原始声明保存在 `LICENSE`，发布时由 `scripts/notices.mjs` 汇入 `THIRD_PARTY_NOTICES`。

| 本仓库 | 上游 | 用途 |
| --- | --- | --- |
| `design/niltavi-ui/tokens.json` | `src/theme/tokens.json` | Native、原型和独立凭证填写页的主题来源，经 `design/scripts/sync-tokens.mjs` 映射 |
| `packages/ui-web/styles/theme/{palette,niltava,components,motion}.css` | `src/theme/` 同名文件 | Web 控制台的主题，原样引入，不经过生成脚本 |
| `packages/ui-web/styles/theme/templates.css` | `src/templates/templates.css`、`scenarios.css` | 模板的布局类，只保留控制台用到的部分 |
| `packages/ui-web/src/components/ForestScene.tsx` | `src/templates/saas/ForestScene.tsx` | 登录页的森林插画 |

Web 控制台的外壳（侧栏、账号菜单、跳转面板、登录页）按上游 `src/templates/saas/` 的模板改写，组件用法以 `src/showcase/examples/` 为准。产品自己的 token（等宽字体、Logo 配色）写在 `packages/ui-web/styles/lerryn.css`，不改主题文件。

`design/tokens.json` 的 `source` 引用快照里的 `resolved.light` 和 `resolved.dark`。为满足 Native 端已有的对比度要求，状态文字使用上游 `*-soft-foreground`，控件边框和占位文字使用 `muted`，焦点环使用 `accent`。`kraft-*`、`flute-*` 和 `Raised` 的旧名字只在 Native、原型和凭证填写页保留兼容；Web 控制台直接使用上游语义变量。Logo 使用 Lerryn 的溪水蓝绿与深青，专用浅色底板保持纸白。

升级时重新选择一个上游提交，原样替换上表中的文件与许可证（`templates.css` 按文件头的说明重新摘取），并同步此处和 `scripts/notices.mjs` 中的提交号。运行 `pnpm design:tokens`、`pnpm design:test`、`make notices`、`make check`，再核对深浅主题和窄屏显示。不要直接编辑快照或主题文件中的单个颜色。
