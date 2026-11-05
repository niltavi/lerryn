import { describe, expect, it } from "vitest";

import { formatInZone } from "../format";
import {
  agentPanels,
  credentialState,
  filterTasks,
  formatCallDuration,
  groupTasks,
  historyNeeds,
  runBoardColumn,
  summarizeRunEvents,
  TOOL_ARGS_PREVIEW,
} from "./console";
import type { PanelSpec, TaskSummary } from "./model";

function task(over: Partial<TaskSummary>): TaskSummary {
  return {
    id: over.name ?? "t",
    project_id: "p",
    name: "t",
    description: "",
    state: "active",
    active_version: 1,
    latest_version: 1,
    last_run: null,
    next_run_at: null,
    active_runs: 0,
    updated_at: "2026-11-01T00:00:00Z",
    links: { console: "https://w.test/tasks/t" },
    ...over,
  };
}
const brief = (status: string, finished = "2026-11-02T08:00:00Z") =>
  ({
    id: "r",
    status,
    test: false,
    version: 1,
    queued_at: finished,
    started_at: finished,
    finished_at: finished,
    result_status: null,
    error_code: null,
  }) as TaskSummary["last_run"];

describe("groupTasks", () => {
  it("separates running, failed, scheduled, enabled and inactive tasks", () => {
    const g = groupTasks([
      task({ name: "paused", state: "inactive", last_run: brief("failed") }),
      task({ name: "later", next_run_at: "2026-11-03T09:00:00Z" }),
      task({ name: "soon", next_run_at: "2026-11-03T08:00:00Z" }),
      task({ name: "broken", last_run: brief("timed_out") }),
      task({ name: "busy", active_runs: 1, last_run: brief("failed") }),
      task({ name: "fine", last_run: brief("completed") }),
    ]);
    expect(g.running.map((t) => t.name)).toEqual(["busy"]);
    expect(g.attention.map((t) => t.name)).toEqual(["broken"]);
    expect(g.scheduled.map((t) => t.name)).toEqual(["soon", "later"]);
    expect(g.enabled.map((t) => t.name)).toEqual(["fine"]);
    expect(g.inactive.map((t) => t.name)).toEqual(["paused"]);
  });

  it("filters by name or purpose", () => {
    const list = [task({ name: "weekday-digest", description: "汇总线索" }), task({ name: "backup" })];
    expect(filterTasks(list, "线索").map((t) => t.name)).toEqual(["weekday-digest"]);
    expect(filterTasks(list, "  ")).toHaveLength(2);
  });
});

describe("agentPanels", () => {
  const data = {
    new_leads: 42,
    high: { value: 7, label: "高意向" },
    sources: [
      { label: "官网", value: 18 },
      { label: "展会", value: 11 },
    ],
    top: [
      { name: "华东药房", score: 92, tags: ["a"] },
      { name: "零部件厂", score: 88 },
    ],
    note: "<b>3 条</b>缺少联系方式",
    bad: [{ label: "x" }],
  };
  const panels: PanelSpec[] = [
    { id: "m", type: "metric", title: "新线索", source: "/new_leads", unit: "条" },
    { id: "m2", type: "metric", title: "高意向", source: "/high" },
    { id: "b", type: "bars", title: "来源", source: "/sources" },
    { id: "d", type: "breakdown", title: "来源", source: "/sources" },
    { id: "t", type: "table", title: "排名", source: "/top" },
    { id: "t2", type: "table", title: "只要名字", source: "/top", columns: [{ key: "name", title: "名称" }] },
    { id: "x", type: "text", title: "备注", source: "/note" },
    { id: "skip", type: "breakdown", title: "坏", source: "/bad" },
    { id: "gone", type: "metric", title: "没有", source: "/missing" },
  ];

  it("renders all five types and leaves out data of the wrong shape", () => {
    const out = agentPanels(panels, data);
    expect(out.map((p) => p.id)).toEqual(["m", "m2", "b", "d", "t", "t2", "x"]);
    expect(out[0]).toMatchObject({ type: "metric", value: 42, unit: "条", label: null });
    expect(out[1]).toMatchObject({ type: "metric", value: 7, label: "高意向" });
    expect(out[2]).toMatchObject({ type: "bars", items: [{ label: "官网", value: 18 }, { label: "展会", value: 11 }] });
    const table = out[4] as Extract<(typeof out)[number], { type: "table" }>;
    expect(table.columns.map((c) => c.key)).toEqual(["name", "score", "tags"]);
    // Nested values show as JSON text; missing cells are null.
    expect(table.rows).toEqual([
      ["华东药房", 92, '["a"]'],
      ["零部件厂", 88, null],
    ]);
    expect((out[5] as typeof table).rows).toEqual([["华东药房"], ["零部件厂"]]);
    // Text stays text: markup is not interpreted.
    expect(out[6]).toMatchObject({ type: "text", text: "<b>3 条</b>缺少联系方式" });
  });

  it("draws bars with history from earlier runs, oldest first", () => {
    const spec: PanelSpec[] = [{ id: "h", type: "bars", title: "高意向", source: "/high_n", history: 3 }];
    expect(historyNeeds(spec)).toBe(3);
    const history = {
      h: [
        { runId: "a", at: "2026-10-30T08:00:00Z", data: { high_n: 4 } },
        { runId: "b", at: "2026-10-31T08:00:00Z", data: { high_n: "x" } },
        { runId: "c", at: "2026-11-01T08:00:00Z", data: { high_n: 6 } },
        { runId: "d", at: "2026-11-02T08:00:00Z", data: { high_n: 7 } },
      ],
    };
    const [bars] = agentPanels(spec, null, { history, historyLabel: (at) => at.slice(8, 10) });
    expect(bars).toMatchObject({ type: "bars", items: [{ label: "30", value: 4, runId: "a" }, { label: "01", value: 6 }, { label: "02", value: 7 }] });
  });

  it("renders nothing without panels or data", () => {
    expect(agentPanels(undefined, data)).toEqual([]);
    expect(agentPanels(panels, null)).toEqual([]);
  });
});

describe("credentialState", () => {
  const now = Date.parse("2026-11-03T00:00:00Z");
  it.each([
    [{ status: "active", expires_at: null }, "active", null],
    [{ status: "active", expires_at: "2026-11-08T00:00:00Z" }, "expiring", 5],
    [{ status: "active", expires_at: "2026-12-30T00:00:00Z" }, "active", null],
    [{ status: "active", expires_at: "2026-11-01T00:00:00Z" }, "expired", null],
    [{ status: "revoked", expires_at: null }, "revoked", null],
    [{ status: "expired", expires_at: null }, "expired", null],
  ] as const)("%o is %s", (c, state, days) => {
    expect(credentialState(c, now)).toEqual({ state, days });
  });
});

describe("summarizeRunEvents", () => {
  it("groups tool calls and collects warnings, ignoring unknown kinds", () => {
    const ev = (kind: string, data: unknown, message: string | null = null) => ({ seq: 1, ts: "2026-11-02T08:00:00Z", kind, stage: null, message, data }) as never;
    const s = summarizeRunEvents([
      ev("tool.call", { tool: "crm-mcp", method: "get_lead", ok: true }),
      ev("tool.call", { tool: "crm-mcp", method: "get_lead", ok: false }),
      ev("tool.call", { tool: "crm-mcp", method: "search_leads", ok: true }),
      ev("warning", null, "容器内有进程因内存超限被杀"),
      ev("something.new", { tool: "x" }),
    ]);
    expect(s.toolCallCount).toBe(3);
    expect(s.toolCalls).toEqual([
      { key: "crm-mcp · get_lead", tool: "crm-mcp", method: "get_lead", count: 2, failed: 1 },
      { key: "crm-mcp · search_leads", tool: "crm-mcp", method: "search_leads", count: 1, failed: 0 },
    ]);
    expect(s.warnings).toEqual([{ ts: "2026-11-02T08:00:00Z", message: "容器内有进程因内存超限被杀" }]);
    expect(s.toolFailedCount).toBe(1);
  });

  it("lists each call with its stage, outcome, duration and cut arguments, never anything returned", () => {
    const long = "a".repeat(TOOL_ARGS_PREVIEW + 20);
    const s = summarizeRunEvents([
      { seq: 4, ts: "t1", kind: "tool.call", stage: "call", message: "calc add", data: { tool: "calc", method: "add", ok: true, duration_ms: 38, args_summary: '{"a":1}', error: null, result: { sum: 2 } } },
      { seq: 7, ts: "t2", kind: "tool.call", stage: null, message: "calc add", data: { tool: "calc", method: "add", ok: false, duration_ms: -1, args_summary: long, error: "HTTP 503" } },
      // A succeeded call keeps no error text even if one came along.
      { seq: 9, ts: "t3", kind: "tool.call", stage: "call", message: null, data: { tool: "calc", method: "", ok: true, duration_ms: 5, args_summary: null, error: "ignored" } },
    ] as never);
    expect(s.calls).toEqual([
      { seq: 4, ts: "t1", stage: "call", tool: "calc", method: "add", ok: true, durationMs: 38, args: '{"a":1}', argsFull: null, error: null },
      { seq: 7, ts: "t2", stage: null, tool: "calc", method: "add", ok: false, durationMs: null, args: "a".repeat(TOOL_ARGS_PREVIEW - 1) + "…", argsFull: long, error: "HTTP 503" },
      { seq: 9, ts: "t3", stage: "call", tool: "calc", method: "", ok: true, durationMs: 5, args: null, argsFull: null, error: null },
    ]);
    expect(JSON.stringify(s)).not.toContain("sum");
    expect(s.toolFailedCount).toBe(1);
  });
});

describe("formatCallDuration", () => {
  it.each([
    [null, "—"],
    [0, "0ms"],
    [38, "38ms"],
    [999, "999ms"],
    [2400, "2.4s"],
    [75_000, "1m 15s"],
  ])("%s → %s", (ms, want) => {
    expect(formatCallDuration(ms)).toBe(want);
  });
});

describe("formatInZone", () => {
  it("reads the time in the trigger's own zone", () => {
    expect(formatInZone("2026-11-03T00:00:00Z", "Asia/Shanghai", "zh-CN")).toEqual({ date: "11月3日 周二", time: "08:00" });
    expect(formatInZone("2026-11-03T00:00:00Z", "America/New_York", "en")).toEqual({ date: "Mon, Nov 2", time: "19:00" });
  });
  it("falls back to the viewer's zone for an unknown one", () => {
    expect(formatInZone("2026-11-03T00:00:00Z", "Not/AZone", "en").time).toMatch(/^\d\d:\d\d$/);
  });
});

describe("runBoardColumn", () => {
  it.each([
    ["queued", "scheduled"],
    ["preparing", "active"],
    ["running", "active"],
    ["cancelling", "active"],
    ["completed", "done"],
    ["failed", "attention"],
    ["timed_out", "attention"],
    ["cancelled", "attention"],
    ["interrupted", "attention"],
    ["skipped", "attention"],
    ["from_the_future", "attention"],
  ])("puts %s in %s", (status, column) => {
    expect(runBoardColumn(status)).toBe(column);
  });
});
