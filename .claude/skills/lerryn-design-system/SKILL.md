---
name: lerryn-design-system
description: Lerryn 设计语言与 React Native 组件库 @lerryn/ui-native 的用法和约束。凡是编写或修改 Lerryn 的界面（移动端、Expo 页面、控制台组件、样式、颜色、字体、图标、Logo、状态文案），或者修改 design/ 目录下的 token、组件库、示例 App、Logo、生成脚本时使用。
---

# Lerryn 设计语言

完整说明在 `docs/design-system.md`：现状、与 PRD 的差距、组件清单、约束、待办。动手前先读它的第 1 节和第 4 节。本文是速查版。

## 先分清改动类型

- **用设计语言写页面**：代码放在产品目录（`apps/web`、`apps/mobile`、`packages/*`），通过 `@lerryn/ui-native` 使用组件。不要把页面代码放进 `design/`。
- **改设计语言本身**：只改 `design/` 下的文件。设计语言的任何文件都不放到 `design/` 以外。

## 写页面

- 组件一律从 `@lerryn/ui-native` 导入。它原样导出 HeroUI Native 的全部组件，外观已是 Lerryn 主题；先找 HeroUI 现成的组件，没有再考虑新增。
- 颜色写 className，用 token 名：`bg-paper`、`bg-liner`、`bg-liner-2`、`text-ink`、`text-ink-muted`、`border-line`、`border-line-strong`、`bg-kraft-strong`、`text-on-kraft`、`bg-kraft-soft`、`text-run`、`bg-success-soft`、`text-danger`、`shadow-flute-b`。不写字面量颜色、字号、阴影。
- 拿不到 className 的地方（SVG、图标 `color`、动画）用 `useLerrynTheme().colors['token 名']`。
- 根组件用 `useLerrynFonts()` 加载字体，再包 `LerrynProvider`。
- 主题采用固定版本的 niltavi-ui，见 `design/niltavi-ui/README.md`。所有表面扁平处理，不画厚度唇边，不做按压下沉；`Raised` 与 `flute-*` 只保留兼容。
- 字体：中文用系统字体和 `Typography.Heading`；`Numeral`（DM Sans）只用于数字和拉丁字样，它没有中文字形；Cron、ID、变量名用 `Mono`。
- 图标用 `lucide-react-native`，描边 2 到 2.5；Agent 相关用 `WaveGlyph`。Logo 只用 `<Logo variant="horizontal | square | circle" />`。
- 文案：按钮动词开头 2 到 6 个字；中英文、数字之间加空格；不用 emoji 和感叹号；错误信息写清怎么改；界面上不出现面向开发者的说明。

产品相关的两条限制：

- 运行状态以 PRD 的 10 种为准（排队中、准备中、运行中、取消中、执行完成、执行失败、已超时、已取消、运行中断、已跳过）。现有 `StatusChip` 只有 6 种，需要按 `docs/design-system.md` 第 7 节的映射重做后再用于控制台。
- PRD 首版不内置聊天 Agent、不做任务制作页面。`PromptComposer`、`AgentMessage` 只是样例，不要据此做对话页面。

## 改设计语言

| 要改的 | 做法 |
| --- | --- |
| 颜色、厚度、字号、间距、圆角 | 改 `design/tokens.json` 的映射或数值（每个 token 写清 `usage`），运行 `pnpm design:tokens` 和 `pnpm design:test`；原型与独立填写页自动生成，上游主题快照只在升级时整体替换 |
| 控制台原型 | 改 `design/prototype/` 下的画板，发布到 Claude Design 画布，两边保持一致，做法见该目录的 README |
| HeroUI 组件的外观 | 改 `design/ui-native/styles/theme.css` 里对应的组件类，不在页面上用 className 硬盖 |
| Lerryn 自己的组件 | 改或新建 `design/ui-native/src/components/`，从 `src/index.ts` 导出，加进 `design/gallery/App.tsx`，在 `docs/design-system.md` 第 3.3 节登记 |
| Logo | 只改母版 `design/brand/logo/svg/lerryn-mark.svg`，运行 `pnpm design:logo` |

不要手改的生成文件：`design/ui-native/styles/tokens.css`、`design/ui-native/src/theme/tokens.ts`、`design/ui-native/src/components/logoMark.ts`、`design/brand/logo/` 下除母版以外的 SVG 和 PNG。

新增或修改 token 时，文字颜色在它说明的底色上，浅色、暗色两套主题都要达到 4.5:1；控件边界、焦点环、图标至少 3:1。

远端 Claude Design 页面尚为旧版牛皮纸主题；获得编辑权限后需同步。外观或参数有变化时，Claude Design 页面（链接在 `docs/design-system.md` 开头）也要同步：`project/tokens.json` 使用旧版 `value` 格式，同步时需先解析当前 `design/tokens.json` 的 `source` 引用，`project/components/bundle.js`、`bundle.css` 是组件的手写 Web 复刻，不会自动更新。没有权限同步时，在结果里说明哪些地方没同步。

## 完成前检查

1. `pnpm design:typecheck` 与 `pnpm design:test` 通过。
2. 在 Gallery 里看浅色、暗色两套主题。开发服务器由开发者自己启动（`pnpm design:gallery:ios`），不要自己反复起服务器或换端口；没有在运行时，请开发者启动，或说明没有做界面核对。
3. 改了现状、组件或约束，更新 `docs/design-system.md` 对应的节。

## 常见坑

- 第三方组件（如 `SafeAreaView`）不认 className，写 `style` 或用 Uniwind 的 `withUniwind` 包一层。
- 单角圆角要四个角分别写：`rounded-tl-lg rounded-tr-3xl rounded-br-3xl rounded-bl-3xl`，`rounded-3xl rounded-tl-lg` 不生效。
- 本机开着 TUN 代理时，Expo 默认地址是 198.18.x.x，模拟器连不上；用 `pnpm design:gallery:ios`，它固定为 `127.0.0.1`。
- 升级 HeroUI Native、Uniwind 或 Expo 后，Gallery 两套主题要完整看一遍。
