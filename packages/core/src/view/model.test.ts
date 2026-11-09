import { describe, expect, it } from "vitest";

import type { RunStage } from "../queries/runs";
import {
  attentionView,
  credentialSummary,
  dayStrip,
  hasStageDependencies,
  nextCron,
  panelViews,
  resolvePointer,
  runCause,
  runGlyph,
  runOrigin,
  runRidges,
  sortTasksBySchedule,
  stageDependencies,
  stageDependencyNames,
  stageRows,
  taskGlyph,
  type AttentionItem,
  type CalendarItem,
  type RunSummary,
  type TaskCredential,
  type TaskSummary,
  type Trigger,
} from "./model";

const task = { id: "t1", name: "weekday-digest", project_id: "p1" };

function attention(over: Partial<AttentionItem>): AttentionItem {
  return {
    kind: "run_failed",
    at: "2026-11-03T08:00:52Z",
    task,
    run_id: "r1",
    stage: null,
    summary: "429 Too Many Requests",
    credential_request_id: null,
    credential_id: null,
    runner_id: null,
    due_at: null,
    link: "https://lerryn.example.com/runs/r1",
    ...over,
  };
}

describe("runGlyph", () => {
  it.each([
    ["queued", "idle", "clock"],
    ["preparing", "run", "wave"],
    ["running", "run", "wave"],
    ["cancelling", "idle", "wave"],
    ["completed", "ok", "check"],
    ["failed", "bad", "x"],
    ["timed_out", "bad", "timer"],
    ["cancelled", "idle", "ban"],
    ["interrupted", "warn", "unplug"],
    ["skipped", "idle", "skip"],
    ["from_a_newer_server", "idle", "clock"],
  ])("%s → %s %s", (status, tone, icon) => {
    expect(runGlyph(status)).toEqual({ tone, icon });
  });
});

describe("attentionView", () => {
  it("opens failed runs in the app with the stage in the subtitle", () => {
    const v = attentionView(attention({ kind: "run_timed_out", stage: "上传" }), 0);
    expect(v.target).toEqual({ kind: "run", runId: "r1" });
    expect(v.glyph).toEqual({ tone: "bad", icon: "timer" });
    expect(v.title).toBe("weekday-digest");
    expect(v.subtitle).toBe("上传 · 429 Too Many Requests");
  });

  it("sends credential requests to the browser and leads with the credential name", () => {
    const v = attentionView(
      attention({
        kind: "credential_request",
        run_id: null,
        summary: "PRICE_API_TOKEN",
        credential_request_id: "cr1",
        due_at: "2026-11-03T20:00:00Z",
        link: "https://lerryn.example.com/c/cr1",
      }),
      0,
    );
    expect(v.target).toEqual({ kind: "browser", url: "https://lerryn.example.com/c/cr1" });
    expect(v.title).toBe("PRICE_API_TOKEN");
    expect(v.titleMono).toBe(true);
    expect(v.subtitle).toBe("weekday-digest");
    expect(v.at).toBe("2026-11-03T20:00:00Z");
  });

  it("sends runner problems to the browser", () => {
    const v = attentionView(attention({ kind: "runner_offline", task: null, run_id: null, runner_id: "runner-03", summary: "runner-03" }), 2);
    expect(v.target).toEqual({ kind: "browser", url: "https://lerryn.example.com/runs/r1" });
    expect(v.glyph.tone).toBe("warn");
    expect(v.title).toBe("runner-03");
  });
});

describe("dayStrip", () => {
  const day = Date.parse("2026-11-03T00:00:00Z");
  const item = (start: string, kind: "run" | "planned", status: CalendarItem["status"], run_id: string | null): CalendarItem => ({
    kind,
    task,
    run_id,
    status,
    start,
    end: null,
  });

  it("buckets by half hour and keeps the most severe run", () => {
    const b = dayStrip(
      [
        item("2026-11-03T08:00:00Z", "run", "completed", "a"),
        item("2026-11-03T08:10:00Z", "run", "failed", "b"),
        item("2026-11-03T08:20:00Z", "run", "completed", "c"),
        item("2026-11-03T20:00:00Z", "planned", null, null),
        item("2026-11-04T01:00:00Z", "run", "failed", "out-of-range"),
      ],
      day,
    );
    expect(b).toHaveLength(48);
    expect(b[16]).toMatchObject({ runs: 3, tone: "bad", runId: "b" });
    expect(b[40]).toMatchObject({ runs: 0, planned: 1, tone: "plan", runId: null });
    expect(b[0]!.tone).toBe("empty");
    expect(b.reduce((n, x) => n + x.runs, 0)).toBe(3);
  });

  it("lets a run outrank a planned fire in the same slot", () => {
    const b = dayStrip(
      [item("2026-11-03T09:00:00Z", "planned", null, null), item("2026-11-03T09:05:00Z", "run", "running", "r")],
      day,
    );
    expect(b[18]).toMatchObject({ tone: "run", runId: "r", planned: 1, runs: 1 });
  });

  it("names each trigger's planned fires once per slot with the server's wording", () => {
    const planned = (start: string, trigger_id: string | null, schedule_text: string | null, name = task.name): CalendarItem => ({
      ...item(start, "planned", null, null),
      task: { ...task, name },
      trigger_id,
      schedule_text,
    });
    const b = dayStrip(
      [
        planned("2026-11-03T09:00:00Z", "tr1", "每分钟"),
        planned("2026-11-03T09:01:00Z", "tr1", "每分钟"),
        planned("2026-11-03T09:15:00Z", "tr2", "每个工作日 09:15（Asia/Singapore）", "lead-sync"),
        planned("2026-11-03T10:00:00Z", null, null),
      ],
      day,
    );
    expect(b[18]!.planned).toBe(3);
    expect(b[18]!.plans).toEqual([
      { taskName: "weekday-digest", scheduleText: "每分钟" },
      { taskName: "lead-sync", scheduleText: "每个工作日 09:15（Asia/Singapore）" },
    ]);
    // Older servers send neither field.
    expect(b[20]!.plans).toEqual([{ taskName: "weekday-digest", scheduleText: null }]);
    expect(b[0]!.plans).toEqual([]);
  });
});

function summary(over: Partial<RunSummary>): RunSummary {
  return {
    id: "r",
    task,
    version: 8,
    status: "completed",
    test: false,
    trigger: { kind: "cron", trigger_id: "tr", scheduled_at: null, user_id: null, client: null },
    attempt: 1,
    retry_of: null,
    rerun_of: null,
    skip_reason: null,
    queued_at: "2026-11-03T08:00:00Z",
    started_at: "2026-11-03T08:00:01Z",
    finished_at: "2026-11-03T08:01:01Z",
    error: null,
    result_status: null,
    result_summary: null,
    current_stage: null,
    links: { console: "https://lerryn.example.com/runs/r" },
    ...over,
  };
}

describe("runOrigin", () => {
  it("names what started the run, rerun first", () => {
    expect(runOrigin(summary({ rerun_of: "x", test: true }))).toBe("rerun");
    expect(runOrigin(summary({ trigger: { ...summary({}).trigger, kind: "retry" } }))).toBe("retry");
    expect(runOrigin(summary({ test: true, trigger: { ...summary({}).trigger, kind: "manual" } }))).toBe("test");
    expect(runOrigin(summary({}))).toBe("cron");
    expect(runOrigin(summary({ trigger: { ...summary({}).trigger, kind: "webhook" } }))).toBe("webhook");
    expect(runOrigin(summary({ trigger: { ...summary({}).trigger, kind: "manual" } }))).toBe("manual");
  });
});

describe("runRidges", () => {
  it("puts the oldest first and scales by duration", () => {
    const now = Date.parse("2026-11-03T09:00:00Z");
    const ridges = runRidges(
      [
        summary({ id: "new", status: "failed", started_at: "2026-11-03T08:00:00Z", finished_at: "2026-11-03T08:00:30Z" }),
        summary({ id: "old", started_at: "2026-11-02T08:00:00Z", finished_at: "2026-11-02T08:01:00Z" }),
        summary({ id: "skipped", status: "skipped", started_at: null, finished_at: null }),
      ],
      now,
    );
    expect(ridges.map((r) => r.runId)).toEqual(["skipped", "old", "new"]);
    expect(ridges[1]!.height).toBe(1);
    expect(ridges[2]).toMatchObject({ tone: "bad", height: 0.5 });
    expect(ridges[0]).toMatchObject({ height: 0.15, durationMs: null });
  });
});

function stage(over: Partial<RunStage>): RunStage {
  return { id: "s", name: "S", declared: true, after: [], status: "pending", started_at: null, finished_at: null, message: null, ...over };
}

describe("stageRows", () => {
  const now = Date.parse("2026-11-03T08:01:00Z");
  const stages = [
    stage({ id: "fetch", name: "获取输入", status: "done", started_at: "2026-11-03T08:00:00Z", finished_at: "2026-11-03T08:00:12.4Z" }),
    stage({ id: "llm", name: "调用模型", status: "failed", started_at: "2026-11-03T08:00:12.4Z", finished_at: "2026-11-03T08:00:50.2Z" }),
    stage({ id: "save", name: "保存结果" }),
  ];

  it("marks stages that never ran in a finished run as skipped", () => {
    const rows = stageRows({ status: "failed", stages }, now);
    expect(rows.map((r) => r.glyph.icon)).toEqual(["check", "x", "skip"]);
    expect(rows[0]!.durationMs).toBe(12_400);
    expect(rows[1]).toMatchObject({ failed: true, durationMs: 37_800 });
    expect(rows[2]).toMatchObject({ skipped: true, durationMs: null });
  });

  it("shows the running stage with a live duration while the run is active", () => {
    const rows = stageRows(
      { status: "running", stages: [stage({ status: "running", started_at: "2026-11-03T08:00:30Z" }), stage({ id: "b" })] },
      now,
    );
    expect(rows[0]).toMatchObject({ glyph: { tone: "run", icon: "wave" }, durationMs: 30_000 });
    expect(rows[1]).toMatchObject({ skipped: false, glyph: { tone: "idle", icon: "pending" } });
  });

  it("marks a stage left running by an interrupted run", () => {
    const rows = stageRows({ status: "interrupted", stages: [stage({ status: "running", started_at: "2026-11-03T08:00:30Z" })] }, now);
    expect(rows[0]).toMatchObject({ glyph: { tone: "warn", icon: "unplug" }, durationMs: null });
  });
});

describe("stageDependencies", () => {
  it("keeps references the run can resolve, in declaration order", () => {
    const stages = [
      stage({ id: "fetch", name: "获取" }),
      stage({ id: "clean", name: "清洗" }),
      stage({ id: "save", name: "保存", after: ["clean", "fetch"] }),
    ];
    expect(stageDependencies(stages).get("save")).toEqual(["clean", "fetch"]);
    expect(stageDependencyNames(stages).get("save")).toEqual(["清洗", "获取"]);
    expect(hasStageDependencies(stages)).toBe(true);
  });

  it("drops unknown stages, the stage itself and repeats", () => {
    const stages = [stage({ id: "a" }), stage({ id: "b", after: ["missing", "b", "a", "a"] })];
    expect(stageDependencies(stages).get("b")).toEqual(["a"]);
    expect(hasStageDependencies([stage({ id: "a", after: ["a", "gone"] })])).toBe(false);
  });

  it("treats a stage without the field as independent", () => {
    const old = { ...stage({ id: "a" }), after: undefined } as unknown as RunStage;
    expect(stageDependencies([old]).get("a")).toEqual([]);
    expect(hasStageDependencies([old])).toBe(false);
  });

  it("gives the stage rows their dependencies by name", () => {
    const rows = stageRows({ status: "running", stages: [stage({ id: "a", name: "获取" }), stage({ id: "b", after: ["a"] })] }, 0);
    expect(rows.map((r) => r.after)).toEqual([[], ["获取"]]);
  });
});

describe("runCause", () => {
  it("uses the stage display name", () => {
    expect(
      runCause({
        error: { code: "script_error", stage: "llm", message: "429" },
        stages: [stage({ id: "llm", name: "调用模型" })],
      }),
    ).toEqual({ stage: "调用模型", message: "429" });
    expect(runCause({ error: { code: "timeout", stage: null, message: "m" }, stages: [] })).toEqual({ stage: null, message: "m" });
    expect(runCause({ error: null, stages: [] })).toBeNull();
  });
});

describe("panels", () => {
  const data = { new_leads: 42, high: { value: 7, label: "高意向" }, note: "今天正常", "a/b": { "~x": 1 }, list: [1, 2] };

  it("resolves JSON pointers", () => {
    expect(resolvePointer(data, "")).toBe(data);
    expect(resolvePointer(data, "/a~1b/~0x")).toBe(1);
    expect(resolvePointer(data, "/list/1")).toBe(2);
    expect(resolvePointer(data, "/list/01")).toBeUndefined();
    expect(resolvePointer(data, "/missing/x")).toBeUndefined();
    expect(resolvePointer(data, "new_leads")).toBeUndefined();
    expect(resolvePointer(data, "/toString")).toBeUndefined();
  });

  it("keeps metric and text panels with the right shape and drops the rest silently", () => {
    const views = panelViews(
      [
        { id: "a", type: "metric", title: "新线索", source: "/new_leads", unit: "条" },
        { id: "b", type: "metric", title: "高意向", source: "/high" },
        { id: "c", type: "metric", title: "坏", source: "/note" },
        { id: "d", type: "text", title: "备注", source: "/note" },
        { id: "e", type: "bars", title: "趋势", source: "/list" },
        { id: "f", type: "text", title: "空", source: "/missing" },
      ],
      data,
    );
    expect(views).toEqual([
      { id: "a", type: "metric", title: "新线索", value: 42, label: null, unit: "条" },
      { id: "b", type: "metric", title: "高意向", value: 7, label: "高意向", unit: null },
      { id: "d", type: "text", title: "备注", text: "今天正常" },
    ]);
    expect(panelViews(undefined, data)).toEqual([]);
    expect(panelViews([{ id: "a", type: "metric", title: "t", source: "" }], null)).toEqual([]);
  });
});

function trigger(over: Partial<Trigger>): Trigger {
  return {
    id: "x",
    name: "x",
    type: "cron",
    enabled: true,
    catch_up: "skip",
    params: null,
    cron: { schedule: "0 8 * * 1-5", timezone: "Asia/Singapore", schedule_text: "每个工作日 08:00", next_fires: [] },
    webhook: null,
    next_fire_at: null,
    last_fired_at: null,
    created_at: "2026-11-01T00:00:00Z",
    updated_at: "2026-11-01T00:00:00Z",
    ...over,
  } as Trigger;
}

describe("nextCron", () => {
  it("prefers the enabled cron trigger that fires soonest", () => {
    const got = nextCron([
      trigger({ id: "hook", type: "webhook", cron: null }),
      trigger({ id: "late", next_fire_at: "2026-11-05T00:00:00Z" }),
      trigger({ id: "soon", next_fire_at: "2026-11-04T00:00:00Z" }),
      trigger({ id: "off", enabled: false, next_fire_at: "2026-11-03T12:00:00Z" }),
    ]);
    expect(got?.id).toBe("soon");
    expect(nextCron([trigger({ id: "hook", type: "webhook", cron: null })])).toBeNull();
  });
});

describe("credentialSummary", () => {
  const now = Date.parse("2026-11-03T00:00:00Z");
  const cred = (over: Partial<TaskCredential>): TaskCredential => ({
    name: "K",
    as: "env",
    optional: false,
    status: "bound",
    credential: null,
    expires_at: null,
    pending_request_id: null,
    ...over,
  });

  it("counts broken and soon-expiring credentials", () => {
    expect(
      credentialSummary(
        [
          cred({}),
          cred({ status: "missing" }),
          cred({ status: "expired", optional: true }),
          cred({ expires_at: "2026-11-05T00:00:00Z" }),
          cred({ expires_at: "2027-01-01T00:00:00Z" }),
        ],
        now,
      ),
    ).toEqual({ total: 5, broken: 1, expiring: 1 });
  });
});

describe("task list", () => {
  const t = (name: string, next: string | null, over: Partial<TaskSummary> = {}): TaskSummary => ({
    id: name,
    project_id: "p",
    name,
    description: "",
    tags: [],
    state: "active",
    active_version: 1,
    latest_version: 1,
    last_run: null,
    next_run_at: next,
    active_runs: 0,
    updated_at: "2026-11-01T00:00:00Z",
    links: { console: "https://x" },
    ...over,
  });

  it("sorts by next run, then name", () => {
    const sorted = sortTasksBySchedule([t("b", null), t("c", "2026-11-04T00:00:00Z"), t("a", null), t("d", "2026-11-03T00:00:00Z")]);
    expect(sorted.map((x) => x.name)).toEqual(["d", "c", "a", "b"]);
  });

  it("picks the row glyph", () => {
    expect(taskGlyph(t("a", null, { state: "inactive" }))).toEqual({ tone: "idle", icon: "pause" });
    expect(taskGlyph(t("a", null))).toEqual({ tone: "idle", icon: "pending" });
    expect(taskGlyph(t("a", "2026-11-04T00:00:00Z"))).toEqual({ tone: "idle", icon: "calendar" });
    expect(
      taskGlyph(
        t("a", null, {
          last_run: {
            id: "r",
            status: "failed",
            test: false,
            version: 1,
            queued_at: "",
            started_at: null,
            finished_at: null,
            result_status: null,
            error_code: null,
          },
        }),
      ),
    ).toEqual({ tone: "idle", icon: "pending" });
  });
});
