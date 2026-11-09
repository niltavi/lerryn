export const repository = 'https://github.com/niltavi/lerryn';
export const origin = 'https://lerryn.niltavi.com';
// Each page is one slide: one sentence, one line of detail, one scene.
// Headings use *word* to mark the one accent phrase rendered in ochre.
// Body copy uses *word* for strong emphasis.
export const languages = {
  en: {
    lang: 'en', path: '', docs: 'en/', alternate: 'zh/', alternateLabel: '简体中文',
    title: 'Lerryn — A self-hosted sandbox for agent work',
    description: 'Hand routine work to your agent. Lerryn runs it on your own machines, on schedule, with credentials kept close and every run on record.',
    skip: 'Skip to content', github: 'GitHub repository',
    cover: {
      heading: 'Hand off the routine.\nKeep the *morning*.',
      text: 'Lerryn gives your agent a place to run the repetitive work, on your own machines.',
      art: 'A stream at sunrise between reeds and stones',
    },
    primary: 'Start self-hosting', secondary: 'View on GitHub',
    slides: [
      { id: 'night', heading: 'While you sleep,\nthe report *gets made*.', text: 'Schedules and webhooks start the work on time. No one has to stay up for it.', art: 'A small leaf boat with a lantern drifts down a stream at night' },
      { id: 'ground', heading: 'The work stays on\n*your own* ground.', text: 'Python, Node, and Shell run in containers on your machines. Runs, logs, and files stay there too.', art: 'The leaf boat passes three stones inside a ring of trees' },
      { id: 'keys', heading: 'The keys stay\n*within reach*.', text: 'Tasks ask for credentials by name. Lerryn hands them over at run time and masks them in logs.', art: 'A key hangs from a branch where a niltava keeps watch' },
      { id: 'record', heading: 'In the morning,\nit’s all *on record*.', text: 'Logs, results, and files from every run are kept, so nothing needs to be asked twice.', art: 'A record card with a pressed sprig and finished lines' },
    ],
    connect: {
      heading: 'One line\nto your *agent*.',
      text: 'Send your agent the line below. It reads the install guide, deploys Lerryn on your machine, and connects itself.',
      label: 'Prompt for your agent',
      prompt: 'Go to lerryn.niltavi.com and install Lerryn for me.',
      guide: 'MCP, HTTP API, and CLI setup',
    },
    copy: 'Copy', copied: 'Copied. Paste it to your agent.', copyError: 'Select the text to copy it.',
    end: { heading: 'Start with *one* machine.', text: 'Docker and SQLite are enough for the first deployment.' },
    themeDark: 'Switch to dark theme', themeLight: 'Switch to light theme',
  },
  zh: {
    lang: 'zh-CN', path: 'zh/', docs: 'zh-CN/', alternate: '', alternateLabel: 'English',
    title: 'Lerryn — 让 Agent 在你自己的机器上跑自动化任务',
    description: '把重复的工作交给 Agent，由 Lerryn 在你自己的机器上按时运行。凭证由你保管，每次运行都有记录。',
    skip: '跳到正文', github: 'GitHub 仓库',
    cover: {
      heading: '把重复的事交出去，\n把*清晨*留给自己。',
      text: 'Lerryn 帮你从繁忙的重复工作中解脱出来，让 Agent 任务像溪流一般稳定流淌',
      art: '插画：日出时分，溪流穿过芦苇与石头',
    },
    primary: '开始本地部署', secondary: '在 GitHub 查看',
    slides: [
      { id: 'night', heading: '当你睡着的时候，*所有工作*由 Lerryn 协调', text: '定时任务会为你准时启动，让 Webhook 推送到你习惯的聊天工具中，没人需要熬夜。', art: '插画：夜里，一只挂着灯笼的叶子小船顺溪而下' },
      { id: 'ground', heading: '一切都留在\n*你自己的机器上*。', text: '无论是 Python、Node.js、Shell 任务，都能在自己的设备上以容器模式运行。你只需要像*批阅奏折*一样审阅最终结果。', art: '插画：叶子小船在一圈树林里依次经过三块石头' },
      { id: 'keys', heading: '钥匙一直\n*在你手边*。', text: 'Lerryn 尽最大努力守护你的密钥，任务执行时只会引用密钥，在运行时 Lerryn 才把凭证交给任务。连日志里出现的明文都会被遮住。', art: '插画：钥匙挂在树枝上，一只仙鹟停在枝头守着' },
      { id: 'record', heading: '早上醒来，\n*一切都有记录*。', text: '每次运行的日志、结果和文件都会保存下来，发生过什么，一查就清楚。', art: '插画：一张记录卡片，夹着压制的枝叶，记录已经写满' },
    ],
    connect: {
      heading: '一句话，\n交给你的 *Agent*。',
      text: '把下面这句话发给 Agent。它会读完安装说明，在你的机器上部署 Lerryn。',
      label: '发给 Agent 的提示词',
      prompt: '帮我去 lerryn.niltavi.com 安装 Lerryn 服务。',
      guide: 'MCP、HTTP API 与 CLI 接入文档',
    },
    copy: '复制', copied: '已复制，粘贴给你的 Agent 即可。', copyError: '请选中文字后复制。',
    end: { heading: '从*一台机器*开始。', text: '第一次部署，只要一台能跑 Docker 的机器。' },
    themeDark: '切换到暗色主题', themeLight: '切换到浅色主题',
  },
};
