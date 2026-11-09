// View models the web console draws beyond what the phone needs: the task
// list groups, all five Agent panel types, credential states and the run's
// recorded tool calls. Pure functions, tested without React.
import type { Schemas } from "@lerryn/api";

import { daysUntil, formatDuration } from "../format";
import { isRunActive, runStatusMeta } from "../status";
import { resolvePointer, sortTasksBySchedule, type PanelSpec, type TaskSummary } from "./model";

type RunEvent = Schemas["RunEvent"];
type Credential = Schemas["Credential"];

// ── Task list ──

const ATTENTION = new Set(["failed", "timed_out", "interrupted"]);

export interface TaskGroups {
  /** The last run failed, timed out or was interrupted. */
  attention: TaskSummary[];
  /** Something is queued or running. */
  running: TaskSummary[];
  /** Enabled tasks with a scheduled fire, soonest first. */
  scheduled: TaskSummary[];
  enabled: TaskSummary[];
  inactive: TaskSummary[];
}

/** Splits the task list into the status groups of the list column (prototype Main.dc.html). */
export function groupTasks(tasks: readonly TaskSummary[]): TaskGroups {
  const groups: TaskGroups = { attention: [], running: [], scheduled: [], enabled: [], inactive: [] };
  for (const task of tasks) {
    if (task.active_runs > 0 || (task.last_run && isRunActive(task.last_run.status))) groups.running.push(task);
    else if (task.state === "inactive") groups.inactive.push(task);
    else if (task.last_run && ATTENTION.has(task.last_run.status)) groups.attention.push(task);
    else if (task.next_run_at) groups.scheduled.push(task);
    else groups.enabled.push(task);
  }
  const byFinish = (a: TaskSummary, b: TaskSummary) =>
    Date.parse(b.last_run?.finished_at ?? b.updated_at) - Date.parse(a.last_run?.finished_at ?? a.updated_at);
  groups.attention.sort(byFinish);
  groups.running.sort((a, b) => a.name.localeCompare(b.name));
  groups.scheduled = sortTasksBySchedule(groups.scheduled);
  groups.enabled.sort((a, b) => a.name.localeCompare(b.name));
  groups.inactive.sort((a, b) => a.name.localeCompare(b.name));
  return groups;
}

/** Keeps the tasks whose name or description contains the query, ignoring case. */
export function filterTasks(tasks: readonly TaskSummary[], query: string): TaskSummary[] {
  const q = query.trim().toLowerCase();
  if (!q) return tasks as TaskSummary[];
  return tasks.filter((t) => t.name.toLowerCase().includes(q) || t.description.toLowerCase().includes(q));
}

// ── Agent panels ──

export type PanelCell = string | number | boolean | null;

export type AgentPanel =
  | { id: string; type: "metric"; title: string; value: number; label: string | null; unit: string | null }
  | { id: string; type: "bars"; title: string; unit: string | null; items: { label: string; value: number; runId?: string }[] }
  | { id: string; type: "breakdown"; title: string; unit: string | null; items: { label: string; value: number }[] }
  | { id: string; type: "table"; title: string; columns: { key: string; title: string }[]; rows: PanelCell[][] }
  | { id: string; type: "text"; title: string; text: string };

const MAX_ROWS = 50;
const MAX_COLUMNS = 8;
const MAX_TEXT = 2000;
const MAX_ITEMS = 50;

const isFiniteNumber = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);
const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

function labelled(value: unknown): { label: string; value: number }[] | null {
  if (!Array.isArray(value) || value.length === 0) return null;
  const items: { label: string; value: number }[] = [];
  for (const item of value.slice(0, MAX_ITEMS)) {
    if (!isRecord(item) || !isFiniteNumber(item.value)) return null;
    const label = typeof item.label === "string" ? item.label : typeof item.label === "number" ? String(item.label) : null;
    if (label === null) return null;
    items.push({ label, value: item.value });
  }
  return items;
}

function cell(v: unknown): PanelCell {
  if (v === null || v === undefined) return null;
  if (typeof v === "string" || typeof v === "number" || typeof v === "boolean") return v;
  // Nested values show as their JSON text, never as markup.
  return JSON.stringify(v);
}

/** One point of a bars panel's history: a finished run's reported data. */
export interface HistoryPoint {
  runId: string;
  at: string;
  data: unknown;
}

/**
 * Turns the panels a task declared and the data its run reported into what
 * the board draws. Only the five declarative types render; a panel whose
 * data does not have the declared shape is left out without an error, and
 * nothing is ever rendered as HTML (contract of PanelSpec). A bars panel
 * with `history` reads `source` from each of `history` (oldest first) and
 * labels the bars with `historyLabel`.
 */
export function agentPanels(
  panels: readonly PanelSpec[] | undefined,
  data: unknown,
  options: { history?: Record<string, readonly HistoryPoint[]>; historyLabel?: (at: string) => string } = {},
): AgentPanel[] {
  if (!panels) return [];
  const out: AgentPanel[] = [];
  for (const p of panels) {
    const unit = p.unit ?? null;
    if (p.type === "bars" && p.history) {
      const points = options.history?.[p.id] ?? [];
      const items = points
        .map((pt) => ({ pt, v: resolvePointer(pt.data, p.source) }))
        .filter((x): x is { pt: HistoryPoint; v: number } => isFiniteNumber(x.v))
        .slice(-p.history)
        .map(({ pt, v }) => ({ label: options.historyLabel ? options.historyLabel(pt.at) : pt.at, value: v, runId: pt.runId }));
      if (items.length > 0) out.push({ id: p.id, type: "bars", title: p.title, unit, items });
      continue;
    }
    if (data === undefined || data === null) continue;
    const value = resolvePointer(data, p.source);
    switch (p.type) {
      case "metric":
        if (isFiniteNumber(value)) {
          out.push({ id: p.id, type: "metric", title: p.title, value, label: null, unit });
        } else if (isRecord(value) && isFiniteNumber(value.value)) {
          const label = typeof value.label === "string" ? value.label : null;
          out.push({ id: p.id, type: "metric", title: p.title, value: value.value, label, unit });
        }
        break;
      case "bars":
      case "breakdown": {
        const items = labelled(value);
        if (items) out.push({ id: p.id, type: p.type, title: p.title, unit, items } as AgentPanel);
        break;
      }
      case "table": {
        if (!Array.isArray(value) || value.length === 0 || !value.every(isRecord)) break;
        const rows = value.slice(0, MAX_ROWS) as Record<string, unknown>[];
        const columns = (p.columns?.length ? p.columns : Object.keys(rows[0]!).slice(0, MAX_COLUMNS).map((key) => ({ key, title: key })))
          .slice(0, MAX_COLUMNS);
        out.push({ id: p.id, type: "table", title: p.title, columns, rows: rows.map((r) => columns.map((c) => cell(r[c.key]))) });
        break;
      }
      case "text":
        if (typeof value === "string" && value.length > 0) {
          out.push({ id: p.id, type: "text", title: p.title, text: value.slice(0, MAX_TEXT) });
        }
        break;
    }
  }
  return out;
}

/** The bars panels that need the result history, with how many runs each wants. */
export function historyNeeds(panels: readonly PanelSpec[] | undefined): number {
  return Math.max(0, ...(panels ?? []).filter((p) => p.type === "bars" && p.history).map((p) => p.history ?? 0));
}

// ── Credentials ──

export type CredentialState = "active" | "expiring" | "expired" | "revoked";

/** Credentials expiring within this many days are flagged. */
export const CREDENTIAL_SOON_DAYS = 7;

/** How a credential is doing, and the days left when it expires soon. */
export function credentialState(c: Pick<Credential, "status" | "expires_at">, now = Date.now()): { state: CredentialState; days: number | null } {
  if (c.status === "revoked") return { state: "revoked", days: null };
  if (c.status === "expired") return { state: "expired", days: null };
  if (c.expires_at) {
    const days = daysUntil(c.expires_at, now);
    if (days <= 0) return { state: "expired", days: null };
    if (days <= CREDENTIAL_SOON_DAYS) return { state: "expiring", days };
  }
  return { state: "active", days: null };
}

// ── Run events ──

export interface ToolCallGroup {
  /** "crm-mcp · search_leads" */
  key: string;
  tool: string;
  method: string;
  count: number;
  failed: number;
}

/**
 * One recorded tool call. Only what the platform keeps: the record has no
 * return value, and the event message is not read, so a page built from
 * this never shows what a tool returned.
 */
export interface ToolCallView {
  seq: number;
  ts: string;
  /** Stage ID the call ran in, or null outside any stage. */
  stage: string | null;
  tool: string;
  method: string;
  ok: boolean;
  durationMs: number | null;
  /** Redacted argument summary, cut to TOOL_ARGS_PREVIEW characters. */
  args: string | null;
  /** The full redacted summary when args was cut, for a tooltip. */
  argsFull: string | null;
  error: string | null;
}

/** Characters of an argument summary shown in a call row; the server keeps up to 200. */
export const TOOL_ARGS_PREVIEW = 80;

export interface RunEventSummary {
  toolCalls: ToolCallGroup[];
  toolCallCount: number;
  /** Every recorded call in event order. */
  calls: ToolCallView[];
  /** Calls that failed. */
  toolFailedCount: number;
  /** Platform warnings, e.g. a process killed for memory while the run completed. */
  warnings: { ts: string; message: string }[];
}

/** A tool call's duration: whole milliseconds under a second, where most calls land, else as runs show it. */
export function formatCallDuration(ms: number | null): string {
  if (ms === null || !Number.isFinite(ms) || ms < 0) return "—";
  return ms < 1000 ? `${Math.round(ms)}ms` : formatDuration(ms);
}

const text = (v: unknown): string | null => (typeof v === "string" && v !== "" ? v : null);

function cutText(s: string, n: number): string {
  const chars = [...s];
  return chars.length <= n ? s : chars.slice(0, n - 1).join("") + "…";
}

/** Groups the tool calls the platform recorded, lists each call and collects warnings. */
export function summarizeRunEvents(events: readonly RunEvent[]): RunEventSummary {
  const groups = new Map<string, ToolCallGroup>();
  const warnings: { ts: string; message: string }[] = [];
  const calls: ToolCallView[] = [];
  let failed = 0;
  for (const e of events) {
    if (e.kind === "tool.call" && isRecord(e.data)) {
      const tool = text(e.data.tool);
      if (!tool) continue;
      const method = text(e.data.method) ?? "";
      const ok = e.data.ok !== false;
      const key = method ? `${tool} · ${method}` : tool;
      const g = groups.get(key) ?? { key, tool, method, count: 0, failed: 0 };
      g.count++;
      if (!ok) {
        g.failed++;
        failed++;
      }
      groups.set(key, g);
      const args = text(e.data.args_summary);
      const cut = args ? cutText(args, TOOL_ARGS_PREVIEW) : null;
      calls.push({
        seq: e.seq,
        ts: e.ts,
        stage: e.stage ?? null,
        tool,
        method,
        ok,
        durationMs: isFiniteNumber(e.data.duration_ms) && e.data.duration_ms >= 0 ? e.data.duration_ms : null,
        args: cut,
        argsFull: cut !== args ? args : null,
        error: ok ? null : text(e.data.error),
      });
    } else if (e.kind === "warning" && e.message) {
      warnings.push({ ts: e.ts, message: e.message });
    }
  }
  return {
    toolCalls: [...groups.values()].sort((a, b) => b.count - a.count),
    toolCallCount: calls.length,
    calls,
    toolFailedCount: failed,
    warnings,
  };
}

// ── Run list ──

export type RunFilter = "all" | "failed" | "active" | "archived";

/** The statuses each run list filter asks the server for. */
export const RUN_FILTER_STATUSES: Record<RunFilter, Schemas["RunStatus"][]> = {
  all: [],
  archived: [],
  failed: ["failed", "timed_out", "interrupted"],
  active: ["queued", "preparing", "running", "cancelling"],
};

/**
 * Kanban columns of the runs board, left to right: waiting to start, running,
 * finished well, and finished without a result (cancelled, failed, skipped).
 */
export const RUN_BOARD_COLUMNS = ["scheduled", "active", "done", "attention"] as const;
export type RunBoardColumn = (typeof RUN_BOARD_COLUMNS)[number];

/** The board column a run belongs to. An unknown status from a newer server waits in "attention". */
export function runBoardColumn(status: string): RunBoardColumn {
  switch (status) {
    case "queued":
      return "scheduled";
    case "preparing":
    case "running":
    case "cancelling":
      return "active";
    case "completed":
      return "done";
    default:
      return "attention";
  }
}

/** Whether a run's status needs someone to look at it. */
export function isAttentionStatus(status: string): boolean {
  return ATTENTION.has(status);
}

/** The colour family of a run on strips and in lists; active runs are "run". */
export function runTone(status: string) {
  return isRunActive(status) && status !== "queued" ? "run" : runStatusMeta(status).tone;
}
