# Lerryn Logo

纯图形，不带字。两色：溪水蓝绿 `#428c9a`（顶面）+ 深青 `#245761`（侧面），颜色固定，不随主题变化。

| 文件 | 尺寸 | 底 | 用在 |
| --- | --- | --- | --- |
| `svg/lerryn-mark.svg` | 1128 × 426 | 透明，无留白 | 矢量母版，其他版本都由它生成 |
| `svg/` `png/lerryn-logo-horizontal` | 1224 × 522（PNG 2448 × 1044） | 透明，四周留 48 | 页眉、登录页、文档 |
| `svg/` `png/lerryn-logo-square` | 1024 × 1024 | 纸色 `#fef8ef` 铺满 | App 图标、社交头像（平台自己切圆角） |
| `svg/` `png/lerryn-logo-circle` | 1024 × 1024 | 纸色圆形，圆外透明 | Agent 头像、圆形头像位 |

- 横向版是透明底，放在浅色底上。暗色底上用正方形或圆形版，或者垫一块纸色底板（`plate` token）。
- 不要改色、描边、拉伸或加阴影。最小显示宽度：横向 24px 高，正方形 / 圆形 20px。
- `../source/original-reference.webp` 是改名前带旧字标的原始参考图，矢量图形由它描摹得到，只作存档。
- 在 App 里不要直接引用这些文件，用 `@lerryn/ui-native` 的 `<Logo variant="horizontal | square | circle" />`，它用同一份矢量路径绘制。
- 修改 Logo 只改母版 `svg/lerryn-mark.svg`，然后在仓库根目录运行 `pnpm design:logo`，其余 SVG、PNG 和组件用的路径会重新生成。
