export const repository = 'https://github.com/niltavi/lerryn';
export const origin = 'https://lerryn.niltavi.com';
// Each page is one slide: one sentence, one line of detail, one scene.
// Headings use *word* to mark the one accent phrase rendered in ochre.
export const languages = {
  en: {
    lang: 'en', path: '', alternate: 'zh/', alternateLabel: '简体中文',
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
      { id: 'keys', heading: 'The keys stay\n*within reach*.', text: 'Tasks ask for credentials by name. Lerryn hands them over at run time and masks them in logs.', art: 'A key hangs from a branch while a niltava keeps watch' },
      { id: 'record', heading: 'In the morning,\nit’s all *on record*.', text: 'Logs, results, and files from every run are kept, so nothing needs to be asked twice.', art: 'A record card with a pressed sprig and finished lines' },
    ],
    connect: { heading: 'One line\nto your *agent*.', text: 'Connect over MCP, the HTTP API, or the CLI.' },
    tabsLabel: 'Connection examples', copy: 'Copy', copied: 'Copied', copyError: 'Select the command to copy it.',
    snippets: [
      { id: 'mcp', label: 'MCP', code: 'lerryn login https://your-lerryn.example --username alice\nclaude mcp add lerryn -- lerryn mcp --stdio' },
      { id: 'api', label: 'HTTP API', code: 'curl https://your-lerryn.example/api/v1/discover \\\n  -H "Authorization: Bearer $LERRYN_TOKEN"' },
      { id: 'cli', label: 'CLI', code: 'lerryn tasks apply ./daily-report\nlerryn runs start daily-report --test --wait 60s' },
    ],
    end: { heading: 'Start with *one* machine.', text: 'Docker and SQLite are enough for the first deployment.' },
    themeDark: 'Switch to dark theme', themeLight: 'Switch to light theme',
  },
  zh: {
    lang: 'zh-CN', path: 'zh/', alternate: '', alternateLabel: 'English',
    title: 'Lerryn — 为 Agent 任务提供稳定的执行空间',
    description: '把重复的工作交给 Agent。Lerryn 在你自己的机器上按时运行，凭证留在身边，每一次运行都有记录。',
    skip: '跳到正文', github: 'GitHub 仓库',
    cover: {
      heading: '把重复的事交出去，\n把*清晨*留给自己。',
      text: 'Lerryn 让 Agent 在你自己的机器上，替你跑完那些反复的工作。',
      art: '插画：日出时分，溪流穿过芦苇与石头',
    },
    primary: '开始自部署', secondary: '在 GitHub 查看',
    slides: [
      { id: 'night', heading: '你睡着的时候，\n报表*已经在做了*。', text: '定时计划和 Webhook 让工作按时开始，不需要有人熬夜守着。', art: '插画：夜里，一只挂着灯笼的叶子小船顺溪而下' },
      { id: 'ground', heading: '工作留在\n*你自己的地方*。', text: 'Python、Node、Shell 在你机器上的容器里运行，运行记录、日志和文件也都留在那里。', art: '插画：叶子小船在一圈树林里依次经过三块石头' },
      { id: 'keys', heading: '钥匙一直\n*在你手边*。', text: '任务按名称取用凭证，运行时才交给它，日志里自动遮去。', art: '插画：钥匙挂在树枝上，一只蓝鸲鹟在旁边守着' },
      { id: 'record', heading: '醒来时，\n一切*都有记录*。', text: '每次运行的日志、结果和文件都会留下，不用再追问发生了什么。', art: '插画：一张记录卡片，夹着压制的枝叶，记录已经写满' },
    ],
    connect: { heading: '一句话，\n交给你的 *Agent*。', text: '通过 MCP、HTTP API 或命令行接入。' },
    tabsLabel: '接入示例', copy: '复制', copied: '已复制', copyError: '请选中命令后复制。',
    snippets: [
      { id: 'mcp', label: 'MCP', code: 'lerryn login https://your-lerryn.example --username alice\nclaude mcp add lerryn -- lerryn mcp --stdio' },
      { id: 'api', label: 'HTTP API', code: 'curl https://your-lerryn.example/api/v1/discover \\\n  -H "Authorization: Bearer $LERRYN_TOKEN"' },
      { id: 'cli', label: 'CLI', code: 'lerryn tasks apply ./daily-report\nlerryn runs start daily-report --test --wait 60s' },
    ],
    end: { heading: '从*一台机器*开始。', text: '首次部署，有 Docker 和 SQLite 就够了。' },
    themeDark: '切换到暗色主题', themeLight: '切换到浅色主题',
  },
};
