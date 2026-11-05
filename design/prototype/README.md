# 控制台原型

Lerryn 控制台的全局原型，用 Lerryn 设计语言画成：Web 19 页、移动端 5 屏、弹窗和状态 2 张表。原型在 Claude Design 画布 [Lerryn 控制台原型](https://claude.ai/artifact/X2ivYVnSoKvTiVeq8DkqR1) 上浏览和演示，本目录存放这个画布的源文件。画布默认私有，由所有者分享后才能打开。

这里只是原型，不是产品代码。数据都是示例，页面之间可以点击跳转。产品前端以后写在 `apps/`、`packages/` 里。

## 设计要点

- 结构：图标轨 + 列表 + 看板。概览的看板只放三样东西：需要处理的事、正在运行的任务、今天的波纹日历。
- 次要信息收进胶囊、悬停提示和浮层，不放说明文字。
- 区域按数据出现：没有任务上报的结果就不显示结果区，没有产物就不显示文件区，任务没声明阶段就不画阶段条。
- Agent 面板：任务声明 panels，数据来自运行上报的结果，平台只渲染 metric、bars、breakdown、table、text 这几种类型。
- 两处与现有规范不同，还需要确认：列表里的运行状态只用图形表示，文字放在悬停提示和读屏标签里，而 `docs/design-system.md` 第 4 节要求状态同时有图标和文字；Agent 面板需要改任务清单的 Schema 和 OpenAPI 契约。

## 文件

| 文件 | 内容 |
| --- | --- |
| `canvas.json` | 画布索引：画板位置和尺寸、三个分页、便签、所用设计系统 |
| `console.css` | 共享样式：自动生成的 token，加上页面骨架和控制台专用样式 |
| `Main.dc.html` | 概览 |
| `Overview-Empty.dc.html` | 概览 · 首次使用 |
| `Runs.dc.html` | 运行（列表 + 预览） |
| `Task.dc.html` | 任务 |
| `Run-Running.dc.html`、`Run-Failed.dc.html`、`Run-Done.dc.html` | 运行中、执行失败、执行完成（含 Agent 面板） |
| `Login.dc.html`、`Register.dc.html` | 登录、注册 |
| `Credential-Fill.dc.html`、`Credential-States.dc.html` | 凭证填写页，以及提交后和异常时的状态 |
| `Settings-*.dc.html` | 个人设置：访问令牌、凭证、通知、手机、账号 |
| `Admin-*.dc.html` | 管理：账号、登录、执行节点 |
| `M-*.dc.html` | 移动端：登录、概览、任务、运行，以及在手机浏览器里打开的凭证填写页 |
| `Dialogs.dc.html`、`States.dc.html` | 弹窗；运行状态、Agent 面板类型、日志提示和空态 |

每个画板的 Tweaks 里都有「深色」开关；登录页另有开放注册模式和错误态开关，注册页有「首个账号」开关，管理 · 登录页有「已配置单点登录」开关。

## 不在本目录的部分

`.dc.html` 文件要在 Claude Design 里运行，不能直接用浏览器打开。下面这些由画布提供，不放进仓库：

- `support.js`：画布运行时。
- `ds/lerryn/`：发布时从 Claude Design 设计系统 [Lerryn](https://claude.ai/artifact/FPPdharrE7VXPo5ZaJPmVq) 复制过来的 `tokens.json`、`components/bundle.js`、`components/bundle.css` 和字体。
- Logo：页面里的 `/_blob/46d7ba75db40f7da30f9bb6f6115b8fb` 是上传到画布的 `design/brand/logo/svg/lerryn-mark.svg`。

## 修改与同步

- 本目录和画布要保持一致。改了这里，就把改动的文件发布到画布；在画布上直接改了，就把文件读回来覆盖这里。
- 用 Claude Code 发布时，把本目录的文件映射到画布的 `project/` 下，例如 `project/Main.dc.html` 对应 `Main.dc.html`。只有增删画板、调整位置或尺寸、改便签时，才需要连同 `canvas.json` 一起发布。
- `console.css` 标记区中的 token 由 `pnpm design:tokens` 自动生成。仓库共享样式已迁移到 Niltavi；远端画布及其 `ds/lerryn/` 组件和字体尚未同步。
- 画布的两个行为：写在 `<x-import>` 上的 `class` 和 `style` 不会传给组件，要给设计系统组件加样式，就包一层普通元素，或者用 CSS 按子元素选择；Tweaks 里开关的默认值会作为 `this.props` 传进来，所以 `this.props.x ?? 兜底值` 里的兜底值不会生效。
