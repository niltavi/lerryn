// Copy that only the mobile app uses. Shared copy (run states, log, time)
// comes from @lerryn/core's catalogs so the two consoles say the same thing.
// Rules from docs/design-system.md section 4: calm and short, a space between
// Chinese and Latin text or digits, no exclamation marks.
export const zhCN = {
  login: {
    scan: '扫码配对',
    or: '或',
    server: '服务器',
    serverPlaceholder: 'lerryn.example.com',
    readOnly: '只读',
    insecure: '连接未加密',
    errors: {
      serverInvalid: '服务器地址不完整，例如 lerryn.example.com',
      serverUnreachable: '连不上这个服务器，检查地址和网络后再试',
      notLerryn: '这个地址不是 Lerryn 服务器',
      passwordDisabled: '这个服务器已关闭密码登录，请扫码配对',
    },
  },
  scan: {
    title: '扫码配对',
    needCamera: '扫码需要使用相机',
    allowCamera: '允许使用相机',
    openSettings: '打开设置',
    notPairing: '不是 Lerryn 的配对二维码',
    manual: '手动输入',
    code: '配对码',
    codePlaceholder: 'K7Q2XN4V',
    pairing: '正在登录',
    errors: {
      codeInvalid: '配对码无效或已过期，请在控制台重新生成',
      codeFormat: '配对码是 8 位字母和数字',
    },
  },
  pair: {
    title: '登录到 {host}',
    confirm: '登录',
    invalidLink: '配对链接无效，请在控制台重新生成二维码',
  },
  overview: {
    next: '下一次',
    tasks: '任务',
    noTasks: '还没有任务',
    allProjects: '全部项目',
    switchProject: '切换项目，当前为 {name}',
    todayRuns: '今天的运行',
    bucket: '{time} 起 {runs} 次运行，{planned} 次计划',
  },
  task: {
    enabled: '已启用',
    paused: '已暂停',
    nextRun: '下次执行',
    runCount: '次运行',
    lastResult: '上次结果',
    credentials: '凭证 {count}',
    credentialsAttention: '有凭证需要处理',
    recentRuns: '最近的运行',
    origin: {
      cron: '定时',
      webhook: 'Webhook',
      manual: '手动',
      test: '测试',
      rerun: '重跑',
      retry: '重试',
    },
  },
  run: {
    attempt: '第 {attempt}/{max} 次尝试',
    rerun: '已重跑',
    openInBrowser: '在浏览器中打开',
    stageNotRun: '未执行',
    showEarlier: '更早 {count} 行',
    skip: {
      overlap: '上一次运行还没有结束',
      queue_full: '队列已满',
      missed: '错过了计划时间',
      inactive: '触发时任务已暂停或归档',
    },
  },
  account: {
    server: '服务器',
    session: '会话',
    readOnlySession: '只读',
  },
  projects: {
    title: '项目',
  },
  errors: {
    offline: '连不上服务器',
    offlineHint: '检查网络，或确认服务器地址',
    signedOut: '登录已失效，请重新登录',
    failed: '加载失败',
    failedHint: '稍后再试',
    readOnly: '手机上只能查看',
  },
} as const;

type Widen<T> = { readonly [K in keyof T]: T[K] extends string ? string : Widen<T[K]> };
export type MobileMessages = Widen<typeof zhCN>;

export const en: MobileMessages = {
  login: {
    scan: 'Scan to pair',
    or: 'or',
    server: 'Server',
    serverPlaceholder: 'lerryn.example.com',
    readOnly: 'Read-only',
    insecure: 'Not encrypted',
    errors: {
      serverInvalid: 'Enter the full server address, e.g. lerryn.example.com',
      serverUnreachable: 'Cannot reach this server. Check the address and network, then try again',
      notLerryn: 'This address is not a Lerryn server',
      passwordDisabled: 'Password sign-in is turned off on this server. Scan to pair instead',
    },
  },
  scan: {
    title: 'Scan to pair',
    needCamera: 'Scanning needs the camera',
    allowCamera: 'Allow camera',
    openSettings: 'Open Settings',
    notPairing: 'Not a Lerryn pairing code',
    manual: 'Enter manually',
    code: 'Pairing code',
    codePlaceholder: 'K7Q2XN4V',
    pairing: 'Signing in',
    errors: {
      codeInvalid: 'The code is invalid or expired. Generate a new one in the console',
      codeFormat: 'The pairing code has 8 letters and digits',
    },
  },
  pair: {
    title: 'Sign in to {host}',
    confirm: 'Sign in',
    invalidLink: 'The pairing link is invalid. Generate a new QR code in the console',
  },
  overview: {
    next: 'Next',
    tasks: 'Tasks',
    noTasks: 'No tasks yet',
    allProjects: 'All projects',
    switchProject: 'Switch project, current: {name}',
    todayRuns: "Today's runs",
    bucket: 'From {time}: {runs} runs, {planned} planned',
  },
  task: {
    enabled: 'Active',
    paused: 'Paused',
    nextRun: 'Next run',
    runCount: 'runs',
    lastResult: 'Last result',
    credentials: 'Credentials {count}',
    credentialsAttention: 'A credential needs attention',
    recentRuns: 'Recent runs',
    origin: {
      cron: 'Scheduled',
      webhook: 'Webhook',
      manual: 'Manual',
      test: 'Test',
      rerun: 'Rerun',
      retry: 'Retry',
    },
  },
  run: {
    attempt: 'Attempt {attempt}/{max}',
    rerun: 'Rerun',
    openInBrowser: 'Open in browser',
    stageNotRun: 'Not run',
    showEarlier: '{count} earlier lines',
    skip: {
      overlap: 'The previous run had not finished',
      queue_full: 'The queue was full',
      missed: 'The scheduled time was missed',
      inactive: 'The task was paused or archived',
    },
  },
  account: {
    server: 'Server',
    session: 'Session',
    readOnlySession: 'Read-only',
  },
  projects: {
    title: 'Projects',
  },
  errors: {
    offline: 'Cannot reach the server',
    offlineHint: 'Check the network or the server address',
    signedOut: 'Your session has ended. Sign in again',
    failed: 'Could not load',
    failedHint: 'Try again later',
    readOnly: 'The phone is view-only',
  },
};
