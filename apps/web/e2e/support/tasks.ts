/**
 * The tasks the suite seeds. Each one is real Python run by the embedded
 * runner in a Docker container, so what the console shows comes from the
 * same path as production: sbx stage and result events, stdout and stderr.
 */

const python = { language: "python", version: "3.12" };
const entrypoint = ["python", "main.py"];

export interface TaskBody {
  manifest: Record<string, unknown>;
  files: { path: string; content: string }[];
}

/** Three stages with output in each, and a result the Agent panels read. */
export const dailyReportCode = `import time
import sbx

with sbx.stage("fetch", "读取 3 个来源"):
    for i in range(4):
        print(f"fetch source {i}", flush=True)
        time.sleep(0.6)
with sbx.stage("analyze"):
    for i in range(4):
        print(f"analyze batch {i}", flush=True)
        time.sleep(0.6)
with sbx.stage("publish"):
    print("publish report", flush=True)
    time.sleep(0.5)
sbx.result("ok", "生成 3 份报告", {
    "reports": 3,
    "leads": [{"name": "华东客户", "score": 91}, {"name": "华南客户", "score": 78}],
    "note": "本周线索比上周多 2 条",
    "by_source": [{"label": "官网", "value": 5}, {"label": "活动", "value": 2}],
})
`;

export function dailyReport(description = "每日线索报告"): TaskBody {
  return {
    manifest: {
      description,
      runtime: python,
      entrypoint,
      network: "none",
      limits: { timeout: "5m", memory: "256Mi" },
      stages: [
        { id: "fetch", name: "获取数据" },
        { id: "analyze", name: "分析", after: ["fetch"] },
        { id: "publish", name: "发布", after: ["analyze"] },
      ],
      panels: [
        { id: "reports", type: "metric", title: "报告数", source: "/reports", unit: "份" },
        { id: "leads", type: "table", title: "高意向线索", source: "/leads", columns: [{ key: "name", title: "客户" }, { key: "score", title: "评分" }] },
        { id: "sources", type: "breakdown", title: "来源", source: "/by_source" },
        { id: "note", type: "text", title: "备注", source: "/note" },
      ],
    },
    files: [{ path: "main.py", content: dailyReportCode }],
  };
}

/** No stages at all: the run page must not invent any. */
export const plainEcho: TaskBody = {
  manifest: { description: "简单输出", runtime: python, entrypoint, network: "none", limits: { timeout: "2m", memory: "128Mi" } },
  files: [
    {
      path: "main.py",
      content: `import sys
import sbx

print("hello from plain-echo", flush=True)
print("a warning on stderr", file=sys.stderr, flush=True)
print("done", flush=True)
sbx.result("ok", "输出了 3 行")
`,
    },
  ],
};

/** Fails inside its second stage with exit status 2. */
export const broken: TaskBody = {
  manifest: {
    description: "会失败的导入",
    runtime: python,
    entrypoint,
    network: "none",
    limits: { timeout: "2m", memory: "128Mi" },
    stages: [
      { id: "prepare", name: "准备" },
      { id: "load", name: "导入", after: ["prepare"] },
    ],
  },
  files: [
    {
      path: "main.py",
      content: `import sys
import sbx

with sbx.stage("prepare"):
    print("prepared", flush=True)
with sbx.stage("load"):
    print("loading rows", flush=True)
    print("row 17: column price is missing", file=sys.stderr, flush=True)
    sys.exit(2)
`,
    },
  ],
};

/**
 * Large logs: mode "lines" prints 100 000 lines, mode "flood" prints more
 * than the per-run limit (200 000) so the platform drops the rest.
 */
export const bigLog: TaskBody = {
  manifest: {
    description: "大日志",
    runtime: python,
    entrypoint,
    network: "none",
    limits: { timeout: "5m", memory: "256Mi" },
    stages: [{ id: "emit", name: "输出" }],
  },
  files: [
    {
      path: "main.py",
      content: `import sys
import sbx

count = 100000 if sbx.params().get("mode") == "lines" else 210000
with sbx.stage("emit"):
    out = sys.stdout
    for i in range(1, count + 1):
        out.write(f"line {i}\\n")
    out.flush()
sbx.result("ok", f"printed {count} lines", {"count": count})
`,
    },
  ],
};

/** Prints a numbered line every 200 ms until cancelled; for following a live log. */
export const ticker: TaskBody = {
  manifest: {
    description: "持续输出",
    runtime: python,
    entrypoint,
    network: "none",
    limits: { timeout: "15m", memory: "128Mi" },
    stages: [{ id: "tick", name: "计时" }],
  },
  files: [
    {
      path: "main.py",
      content: `import time
import sbx

with sbx.stage("tick"):
    for i in range(1, 4501):
        print(f"tick {i}", flush=True)
        time.sleep(0.2)
`,
    },
  ],
};

/** Declares a credential nobody has filled in; it is never run. */
export const needsKey: TaskBody = {
  manifest: {
    description: "需要 Key 的任务",
    runtime: python,
    entrypoint,
    network: "none",
    limits: { timeout: "2m", memory: "128Mi" },
    credentials: [{ name: "API_TOKEN" }],
  },
  files: [{ path: "main.py", content: 'print("never run")\n' }],
};
