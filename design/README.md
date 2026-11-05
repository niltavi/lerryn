# design/

Lerryn 设计语言的全部内容：token、Logo、React Native 组件库 `@lerryn/ui-native`、示例 App。现状、用法和约束见 [docs/design-system.md](../docs/design-system.md)。

| 目录 | 内容 |
| --- | --- |
| `tokens.json` | 产品 token 映射与排版、间距、圆角定义 |
| `niltavi-ui/` | 固定版本的上游 Niltavi 主题快照、来源与 MIT 许可证 |
| `brand/` | Logo（SVG 母版与各版本、PNG）和原始参考图 |
| `ui-native/` | `@lerryn/ui-native` 组件库 |
| `gallery/` | `@lerryn/design-gallery`，展示全部组件的 Expo App |
| `scripts/` | `sync-tokens.mjs`、`build-logo.mjs` 两个生成脚本 |
| `prototype/` | 控制台原型，Claude Design 画布的源文件，见其中的 README |

在仓库根目录运行：

| 命令 | 作用 |
| --- | --- |
| `pnpm design:tokens` | 改完 `tokens.json` 后重新生成主题变量和 JS 数值 |
| `pnpm design:logo` | 改完 Logo 母版后重新生成各版本 SVG、PNG 和组件用的路径 |
| `pnpm design:test` | 检查深浅色对比度与主题生成一致性 |
| `pnpm design:typecheck` | 检查组件库和示例 App 的类型 |
| `pnpm design:gallery:ios` | 在 iOS 模拟器里打开示例 App |
| `pnpm design:gallery` | 启动示例 App 的开发服务器，用于真机扫码 |
