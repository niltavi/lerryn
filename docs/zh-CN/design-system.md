# Lerryn 设计语言

版本：0.2　日期：2026年11月5日　负责目录：`design/`

本文写给后续开发 Lerryn 界面的人和 Agent：设计语言现在做到了哪一步，在 App 里怎么用，改动时必须遵守什么。旧版视觉规范（牛皮纸主题，尚未同步本次迁移）在 Claude Design：[Lerryn 设计系统](https://claude.ai/artifact/FPPdharrE7VXPo5ZaJPmVq)，页面默认私有，需要由所有者分享后才能打开。

## 1 现状

界面统一采用 [niltavi-ui](https://github.com/niltavi/niltavi-ui) 的 Niltavi 主题：浅色纸白、森林墨色正文、蓝色强调；暗色采用深蓝表面。按钮、表单、卡片、导航和浮层使用适度圆角与细边框，去除厚度唇边和按压下沉。Logo 沿用现有波形，改用溪水蓝绿顶面与深青侧面。

上游主题固定在 `design/niltavi-ui/`，来源、提交号和可访问性适配见该目录的 README。`design/tokens.json` 通过 `source` 引用主题快照；同一个脚本生成 Web、Native、仓库原型和独立凭证填写页的主题。

| 部分 | 状态 | 位置 |
| --- | --- | --- |
| 设计 token | 可用。30 个颜色 × 浅色、暗色两套主题，4 个无厚度兼容别名，11 级字号，间距，圆角 | `design/tokens.json` |
| Logo | 可用。纯图形，横向、正方形、圆形，SVG 与 PNG；官网与自部署站点共用暖纸色圆底、细边框 favicon，另有 32、180 像素 PNG | `design/brand/logo/` |
| React Native 组件库 | 可用。`@lerryn/ui-native`，基于 HeroUI Native 1.0.10 + Uniwind 1.12.2 | `design/ui-native/` |
| 示例 App | 可用。Expo SDK 57，展示全部组件，可切换深浅主题 | `design/gallery/` |
| Claude Design 页面 | 可用。品牌手册、token、9 个组件的 Web 预览、Logo 素材 | 见上方链接 |
| 控制台原型 | 可用。Claude Design 画布 [Lerryn 控制台原型](https://claude.ai/artifact/X2ivYVnSoKvTiVeq8DkqR1)：Web 19 页、移动端 5 屏、弹窗与状态，数据是示例 | `design/prototype/` |
| Web 组件库 | 可用。`@lerryn/ui-web`，基于 HeroUI v3.2.6 + Tailwind v4.3.3；含 RunStatusBadge（10 种状态）、RunTimeline、RunStages（有阶段依赖时画依赖简图，窄屏换成带依赖说明的阶段列表）、ResultPanel、LogViewer、CronText、AgentPanels（metric、bars、breakdown、table、text）、QrCode、DayStrip、RunsStrip、Toggle、ActionMenu、Disclosure | `packages/ui-web/` |

迁移验证包含 TypeScript 检查、前端测试、Web 构建和深浅色对比度测试。旧版曾在 iOS 模拟器上核对；该记录不能视为新主题的移动端视觉验收。Claude Design 的远端画布仍为旧主题。

### 与产品文档的差距

组件库先于 PRD 0.3 定稿开始做，有几处和产品文档不一致。开发控制台前先处理：

1. **运行状态。** PRD 第 6 节和技术设计第 5.4 节定义了 10 种运行状态，`StatusChip` 目前只有 6 种，文案也不同。控制台用的 `RunStatusBadge` 应按 10 种状态重做，文案以 PRD 为准。建议的映射见第 7 节。
2. **对话类组件。** PRD 规定首版不内置聊天 Agent，也不提供任务制作页面。`PromptComposer` 和 `AgentMessage` 是设计语言的样例，P0 的控制台用不到；不要因为组件存在就去做对话页面。
3. **领域组件。** 技术设计第 13.2 节列出的 LogViewer、RunTimeline、RunStatusBadge、CronText、ResultPanel、CredentialForm 都还没有。`FlowStep` 可以作为 RunTimeline 的视觉起点，但它表达的是「步骤类型」（触发、Agent、条件、动作），运行时间线表达的是「执行阶段」，语义要重新定。
4. **目录。** 技术设计第 13.1 节规划了 `packages/tokens`、`packages/ui-native`。现在 token 和组件库都在 `design/` 下，见第 2 节；第 13.1 节需要按本文修订。
5. **多语言。** 控制台首版要支持简体中文和英文，组件里的默认文案（状态词、占位文字）目前写死为中文。

### 任务状态与刷新

任务列表和详情页顶部优先显示任务标题，未填写标题时显示任务名。列表搜索匹配标题、任务名和用途说明。

控制台任务列表与详情页共用 `@lerryn/core` 的任务状态模型：当前有运行时显示排队、准备、运行或取消状态，包括未激活任务的手动运行和测试运行；启用且有下一次定时执行的任务使用 `CalendarClock` 表示「按计划」。已暂停、从未激活、已激活但没有定时计划分别展示，停用任务不放入「按计划」分组。上次运行的结果在运行记录或「需要关注」分组中展示。

运行变化经 SSE 刷新；任务、触发器、版本列表、凭证填写请求及授权、工具、项目和访问令牌等可变元数据在前台每 5 秒重新读取，覆盖外部 Agent 或其他页面的修改。运行查询在事件流连接时仍保留至少 15 秒的轮询兜底；已结束的运行和已关闭的填写请求停止轮询，历史版本文件与固定基线的差异继续缓存。

## 2 目录

主题源文件和 Native 组件在 `design/` 下，Web 组件与样式在 `packages/ui-web/`。

```
design/
  tokens.json            产品 token 映射、字号、间距与圆角
  niltavi-ui/             固定版本的上游主题快照与 MIT 许可证
  brand/
    logo/svg/            Logo 矢量文件：lerryn-mark.svg 是母版，另有横向、正方形、圆形
    logo/png/            由 SVG 导出的 PNG
    source/              带字标的原始参考图，只作存档
  ui-native/             @lerryn/ui-native，React Native 组件库
    styles/theme.css     HeroUI Native 组件的样式覆盖（手写）
    styles/tokens.css    Uniwind 主题变量（生成）
    src/theme/tokens.ts  JS 侧的 token 数值（生成）
    src/components/      Lerryn 组件；logoMark.ts 是生成的
    src/primitives/      Raised：旧布局接口的扁平兼容包装
  gallery/               @lerryn/design-gallery，展示全部组件的 Expo App
  prototype/             控制台原型：Claude Design 画布的画板源文件和共享样式
  scripts/
    sync-tokens.mjs      tokens.json → tokens.css、tokens.ts
    build-logo.mjs       lerryn-mark.svg → 各版本 SVG、PNG、logoMark.ts
  tsconfig.base.json     组件库共用的 TypeScript 配置
```

`design/` 之外与设计语言有关的只有这些：`pnpm-workspace.yaml` 里的 `design/ui-native`、`design/gallery` 两行；根目录 `package.json` 里以 `design:` 开头的脚本；本文；项目级 Skill `.claude/skills/lerryn-design-system/`；以及生成脚本写到 Web 组件库的两份产物 `packages/ui-web/styles/tokens.css`、`packages/ui-web/src/brand/logoMark.ts`（不要手改，改源文件后重新生成）。

约定：

- 产品代码（`apps/web`、`apps/mobile`、`packages/*`）不放进 `design/`，通过 workspace 依赖 `@lerryn/ui-native` 使用组件库。
- 设计语言的任何文件不放到 `design/` 以外。需要新的设计资产、脚本或包，都建在 `design/` 下，再把包路径加进 `pnpm-workspace.yaml`。
- 根目录的 `skills/` 在技术设计里留给产品提供给用户的 Agent Skills，和本仓库开发用的 `.claude/skills/` 是两回事。

## 3 在 App 里使用

### 3.1 接入

以后的 `apps/mobile` 按这四步接入，可以照抄 `design/gallery`：

1. `package.json` 加 `"@lerryn/ui-native": "workspace:*"`，以及它的 peer 依赖：`heroui-native`、`uniwind`、`tailwindcss`、`react-native-reanimated`、`react-native-worklets`、`react-native-gesture-handler`、`react-native-safe-area-context`、`react-native-screens`、`react-native-svg`、`@gorhom/bottom-sheet`、`expo-blur`、`expo-font`、`@expo-google-fonts/sora`、`@expo-google-fonts/ibm-plex-mono`、`lucide-react-native`、`tailwind-variants`、`tailwind-merge`。版本与 `design/gallery/package.json` 保持一致。
2. `global.css` 按这个顺序引入，`@source` 的相对路径按 App 所在位置调整：

   ```css
   @import 'tailwindcss';
   @import 'uniwind';
   @import 'heroui-native/styles';
   @import '@lerryn/ui-native/theme.css';

   @source '../../node_modules/heroui-native/lib';
   @source '../../design/ui-native/src';
   ```

3. `metro.config.js`：`withUniwindConfig(getDefaultConfig(__dirname), { cssEntryFile: './global.css', dtsFile: './uniwind-types.d.ts' })`。
4. 根组件先加载字体，再包 `LerrynProvider`：

   ```tsx
   import './global.css';
   import { LerrynProvider, useLerrynFonts } from '@lerryn/ui-native';

   export default function App() {
     const [fontsLoaded] = useLerrynFonts();
     if (!fontsLoaded) return null;
     return <LerrynProvider>{/* 页面 */}</LerrynProvider>;
   }
   ```

`LerrynProvider` 包含手势根节点、HeroUI Native 的 Provider，并铺好 `paper` 底色。

### 3.2 写样式

- 一律从 `@lerryn/ui-native` 导入组件。它原样导出 HeroUI Native 的全部组件，所以 `Card`、`TextField`、`Switch`、`Tabs`、`Dialog`、`BottomSheet`、`Toast` 等都从这里拿，外观已经是 Lerryn 主题。
- 颜色写成 className，token 名就是 Tailwind 颜色名：`bg-paper`、`bg-liner`、`text-ink`、`text-ink-muted`、`border-line-strong`、`bg-kraft-soft`、`text-run`、`shadow-flute-b`。HeroUI 的语义类（`bg-accent`、`text-muted`、`bg-surface`）也已映射到 Lerryn token。
- 拿不到 className 的地方（SVG 描边、图标的 `color`、动画插值），用 `useLerrynTheme().colors['token 名']`。
- 切换主题：`useLerrynTheme().setTheme('light' | 'dark' | 'system')`。默认跟随系统。
- 自定义表面使用细边框与背景色。`Raised` 保留旧调用接口，但不再绘制唇边或执行下沉；新增控件直接使用 `View` 或 HeroUI 组件。

### 3.3 组件

| 组件 | 来源 | 用途 |
| --- | --- | --- |
| `Button` | HeroUI Button + 兼容布局包装 | 操作按钮。`variant`：primary、secondary、tertiary、outline、ghost、danger、danger-soft；`size`：sm、md、lg；布局 className 写在 `containerClassName` |
| `StatusChip` | Lerryn | 运行状态标签，图标加文字。目前 6 种状态，需按第 1 节重做 |
| `FlowStep` | Lerryn，内部用 HeroUI Card | 流程中的一步：类型徽标、波纹连接线、步骤卡片 |
| `WaveLoader` | Lerryn | 波纹流动，表示正在处理。总要配一句说明 |
| `WaveRule` | Lerryn | 波纹分隔线，一屏最多一条 |
| `Numeral`、`Mono` | Lerryn | DM Sans 数字、IBM Plex Mono 等宽文字 |
| `Logo` | Lerryn | `variant`：horizontal、square、circle |
| `PromptComposer`、`AgentMessage` | Lerryn | 对话样例，P0 控制台不用 |
| `Raised`、`WaveGlyph` | Lerryn | 旧布局兼容包装；波纹图标，与 Lucide 图标同签名 |
| 其余 | HeroUI Native | 按 HeroUI 文档使用，外观由 `theme.css` 统一 |

导出的还有 `tokens`（全部 token 数值）、`useLerrynTheme`、`useLerrynFonts`、`LerrynProvider`。

## 4 约束

### token 与生成文件

- `design/tokens.json` 是产品 token 的入口。颜色通过 `source` 引用 `design/niltavi-ui/tokens.json`，字号、间距、圆角直接定义；改完运行 `pnpm design:tokens`。上游快照仅在有意升级时整体替换。
- 生成文件不手改：`ui-native/styles/tokens.css`、`ui-native/src/theme/tokens.ts`、`ui-native/src/components/logoMark.ts`。
- 组件和页面里不写字面量颜色、字号、阴影。需要新值时先加到 `tokens.json`，并写清 `usage`。
- 文字颜色在它说明的每种底色上，两套主题都要达到 4.5:1（24px 以上或粗体 19px 以上为 3:1）；控件边界、焦点环、图标至少 3:1。

### 视觉

- **表面。** 按钮、输入框、卡片和浮层统一扁平处理，不画厚度唇边，不做按压下沉；`flute-*` 的旧名字对应零阴影。
- **颜色。** `kraft`、`kraft-strong`、`kraft-soft` 是 Niltavi 强调色的兼容别名。主按钮配 `on-kraft`；运行状态使用 `run`、`success`、`warning-ink`、`danger` 与对应浅底，同时保留图形与文字语义。填充 `warning` 上使用 `on-warning`。
- **字体。** Web 的拉丁正文和数字用本地打包的 DM Sans，中文回落系统字体；Native 的 `Numeral` 用 DM Sans，中文与正文用系统字体。Cron、运行 ID 和变量名用 IBM Plex Mono。
- **圆角。** sm 按钮 8px，md、lg 按钮与输入框 12px，卡片 16px，浮层 20px；状态标签保持胶囊形。通过 token 使用这些值。
- **动效。** 保留控件状态反馈与运行中的波纹动画，不使用弹性下沉。遵守系统「减弱动态效果」设置。
- **图标。** Lucide（`lucide-react-native`），描边 2 到 2.5，颜色跟随旁边的文字。Agent 相关的图标用 `WaveGlyph`，不用星星或机器人。
- **Logo。** 用 `<Logo>` 组件，不直接引用图片文件。不改色、描边、拉伸或加阴影。横向版直接放在页面上，通过 `mark-side`、`mark-top` token 随主题取色（暗色主题提亮，与官网一致），不垫底板；正方形和圆形版保持品牌原色，带纸色底板。

### 文案

- 按钮动词开头，2 到 6 个字。错误信息写清哪里不对、怎么改，不道歉。
- 中文与英文、数字之间留一个空格；时间用 24 小时制。不用 emoji，不用感叹号。
- 运行状态的文案以 PRD 为准，全平台一致。
- 界面上不出现面向开发者的说明文字。

### 技术

- **第三方组件不认 className。** Uniwind 只给 React Native 自带组件接了 className。`react-native-safe-area-context` 的 `SafeAreaView` 这类第三方组件，写 `style`，或先用 Uniwind 的 `withUniwind` 包一层。
- **调整 HeroUI 组件的外观，改 `ui-native/styles/theme.css`。** HeroUI 组件内部的样式是自定义 CSS 类（如 `button__root--variant-primary`），在页面上用 Tailwind 类去覆盖它，不保证生效。要改所有同类组件，就在 `theme.css` 覆盖对应的类。
- **单角圆角要逐个角写。** `rounded-3xl rounded-tl-lg` 在 Uniwind 里不生效，要写成 `rounded-tl-lg rounded-tr-3xl rounded-br-3xl rounded-bl-3xl`。
- **版本。** HeroUI Native 1.0.10、Uniwind 1.12.2、Expo SDK 57（React Native 0.86.3）。升级任何一个，都要在 Gallery 里把两套主题完整看一遍。
- **新增组件。** 先看 HeroUI Native 有没有：有就用，只在 `theme.css` 里调外观；没有才在 `ui-native/src/components/` 新建。新组件要做四件事：从 `src/index.ts` 导出，加进 Gallery，补进 Claude Design 页面，在本文第 3.3 节登记。

## 5 修改流程

| 要改的 | 步骤 |
| --- | --- |
| token | 改 `design/tokens.json` → `pnpm design:tokens` → `pnpm design:typecheck` → 在 Gallery 里看两套主题 → 将解析后的 token 适配到 Claude Design 的 `project/tokens.json` ；原型和独立凭证填写页的 token 随脚本自动生成 |
| 组件 | 改 `design/ui-native/src/` → `pnpm design:typecheck` → 在 Gallery 里看两套主题 → 外观或参数变了的话，同步 Claude Design 的 Web 预览 |
| Logo | 只改母版 `design/brand/logo/svg/lerryn-mark.svg`（两条 path：先深青侧面，后溪水蓝绿顶面）→ `pnpm design:logo` → 上传新素材到 Claude Design |

旧 Claude Design 页面的 `project/tokens.json` 使用带 `value` 的 token；当前 `design/tokens.json` 含 `source` 引用，同步时需要使用解析后的主题值，不能直接复制映射文件。`components/bundle.js` 和 `bundle.css` 是 React Native 组件的手写 Web 复刻，不会自动同步。旧远端画布尚未同步 Niltavi 迁移，不能作为当前主题的验收依据。后续获得画布编辑权限后再同步主题、字体和组件外观。

## 6 运行与检查

```bash
pnpm design:typecheck
pnpm design:test
```

```bash
pnpm design:gallery:ios
```

```bash
pnpm design:gallery
```

- `design:gallery:ios` 在 iOS 模拟器里打开 Gallery，开发服务器地址固定为 `127.0.0.1`。本机开着 TUN 模式的代理时，Expo 会把代理的虚拟网卡地址（198.18.x.x）当成局域网 IP，模拟器和手机都连不上。
- `design:gallery` 用于真机扫码。遇到上面的情况，在命令前加 `REACT_NATIVE_PACKAGER_HOSTNAME=<本机局域网 IP>`。
- 开发服务器由开发者自己在终端里启动和停止。

## 7 待办

按优先级：

1. 按 10 种运行状态重做状态标签（`RunStatusBadge`），文案以 PRD 为准。建议的映射：

   | 状态 | 颜色 | 图标 |
   | --- | --- | --- |
   | 排队中 | `liner-2` 底，`ink-muted` 字 | Clock |
   | 准备中 | `run-soft` 底，`run` 字 | WaveLoader |
   | 运行中 | `run-soft` 底，`run` 字 | WaveLoader |
   | 取消中 | `liner-2` 底，`ink-muted` 字 | WaveLoader |
   | 执行完成 | `success-soft` 底，`success` 字 | Check |
   | 执行失败 | `danger-soft` 底，`danger` 字 | X |
   | 已超时 | `danger-soft` 底，`danger` 字 | Timer |
   | 已取消 | `liner-2` 底，`ink-muted` 字 | Ban |
   | 运行中断 | `warning-soft` 底，`warning-ink` 字 | Unplug |
   | 已跳过 | `liner-2` 底，`ink-muted` 字 | SkipForward |

2. 技术设计第 13.2 节的领域组件：Web 端 LogViewer、RunTimeline、CronText、ResultPanel 已完成；CredentialForm 不在控制台里做，凭证由服务端渲染的填写页 `/c/{id}` 填写。配对二维码用 `plate` 底、`bark` 码点，两套主题下都是浅底深码，方便扫码；若要给二维码单独的 token，需加到 `tokens.json`。
3. 组件内的默认文案改为可传入，配合控制台的多语言。
4. 在 Android 上运行 Gallery 并核对。
5. ~~Web 组件库 `ui-web`（HeroUI v3），由 `sync-tokens.mjs` 增加 Web 主题输出。~~ 已完成：`packages/ui-web`，`pnpm design:tokens` 同时生成它的 `styles/tokens.css`。HeroUI 组件的外观覆盖写在 `packages/ui-web/styles/theme.css`，控制台骨架和领域组件的样式在 `styles/console.css`（从 `design/prototype/console.css` 移植）。Web 端的 RunStatusBadge 已按第 7 节第 1 条的映射实现，RN 端的 `StatusChip` 仍是 6 种。
6. Logo 的反白版本（用于深色底），需要品牌方提供或确认。
7. 确认控制台原型提出的两项变化：列表中的运行状态只用图形，文字放在悬停提示和读屏标签里（与第 4 节「状态必须同时有图标和文字」不同）；Agent 面板由任务声明、数据来自运行上报，需要改任务清单 Schema 和 OpenAPI 契约。确认后改第 4 节，并在设计系统中补上对应组件。
