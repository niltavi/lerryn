// Maps API responses to what the console screens draw, on the web and on the
// phone. Pure functions only: no React, no React Native, so they are
// unit-tested in Node.
import type { Schemas } from "@lerryn/api";
import { elapsedMs } from "../format";
import type { MessageKey } from "../i18n/i18n";
import { isRunActive, runStatusMeta, type StatusIcon, type StatusTone } from "../status";

export type Overview = Schemas["Overview"];
export type AttentionItem = Schemas["AttentionItem"];
export type ActiveRun = Schemas["ActiveRun"];
export type CalendarItem = Schemas["CalendarItem"];
export type RunSummary = Schemas["RunSummary"];
export type TaskSummary = Schemas["TaskSummary"];
type Run = Schemas["Run"];
export type PanelSpec = Schemas["PanelSpec"];
export type Trigger = Schemas["Trigger"];
export type TaskCredential = Schemas["TaskCredential"];

/** Filter options come from the complete task list for the current project. */
export function taskTagOptions(tasks: readonly TaskSummary[], locale: string): string[] {
  const tags = new Set<string>();
  for (const task of tasks) {
    for (const tag of task.tags ?? []) tags.add(tag);
  }
  return [...tags].sort((a, b) => a.localeCompare(b, locale));
}

export function tasksWithTag(tasks: readonly TaskSummary[], tag: string): readonly TaskSummary[] {
  return tag ? tasks.filter((task) => task.tags?.includes(tag)) : tasks;
}

/** Status tones plus `key`, the kraft tone the prototype gives credential items. */
export type Tone = StatusTone | "key";
export type GlyphIcon = StatusIcon | "key" | "server" | "calendar" | "pause" | "pending" | "archive";

export interface Glyph {
  tone: Tone;
  icon: GlyphIcon;
}

export function runGlyph(status: string): Glyph {
  const meta = runStatusMeta(status);
  return { tone: meta.tone, icon: meta.icon };
}

/** Where tapping an item goes: a screen in the app, or a page in the browser. */
export type Target = { kind: "run"; runId: string } | { kind: "task"; taskId: string } | { kind: "browser"; url: string };

export interface AttentionView {
  key: string;
  glyph: Glyph;
  /** The attention kind, for the screen reader label. */
  kind: AttentionItem["kind"];
  title: string;
  /** The title is an identifier (credential name), shown in monospace. */
  titleMono: boolean;
  subtitle: string;
  at: string;
  target: Target | null;
  /** The fill-in request of a credential_request row, which the web console fills in place. */
  requestId: string | null;
}

const ATTENTION_GLYPH: Record<AttentionItem["kind"], Glyph> = {
  run_failed: { tone: "bad", icon: "x" },
  run_timed_out: { tone: "bad", icon: "timer" },
  run_interrupted: { tone: "warn", icon: "unplug" },
  credential_request: { tone: "key", icon: "key" },
  credential_expiring: { tone: "warn", icon: "key" },
  runner_offline: { tone: "warn", icon: "server" },
};

/**
 * One "needs attention" row. Runs open in the app; credential requests and
 * runner pages open in the browser, because the app never fills credentials
 * or changes anything.
 */
export function attentionView(item: AttentionItem, index: number): AttentionView {
  const glyph = ATTENTION_GLYPH[item.kind] ?? { tone: "warn", icon: "x" };
  const credential = item.kind === "credential_request" || item.kind === "credential_expiring";
  const taskName = item.task?.name ?? "";
  let target: Target | null = null;
  if (item.run_id && item.kind.startsWith("run_")) target = { kind: "run", runId: item.run_id };
  else if (item.link) target = { kind: "browser", url: item.link };
  return {
    key: `${item.kind}:${item.run_id ?? item.credential_request_id ?? item.credential_id ?? item.runner_id ?? index}`,
    glyph,
    kind: item.kind,
    // Credential rows lead with the credential name (the summary) and name
    // the task underneath, as in the prototype; run rows lead with the task.
    title: credential ? item.summary : taskName || item.summary,
    titleMono: credential,
    subtitle: credential ? taskName : item.stage ? `${item.stage} · ${item.summary}` : item.summary,
    at: item.due_at && credential ? item.due_at : item.at,
    target,
    requestId: item.kind === "credential_request" ? (item.credential_request_id ?? null) : null,
  };
}

/** How long a run has been going, or took: from start (or queueing) to end (or now). */
export function runElapsed(
  run: { started_at: string | null; queued_at: string; finished_at?: string | null },
  now: number,
): number {
  return elapsedMs(run.started_at ?? run.queued_at, run.finished_at ?? null, now);
}

/** Duration of a finished or running run; null when it never started. */
export function runDuration(
  run: { started_at: string | null; finished_at: string | null },
  now: number,
): number | null {
  if (!run.started_at) return null;
  return elapsedMs(run.started_at, run.finished_at, now);
}

export type StripTone = 'empty' | 'ok' | 'bad' | 'warn' | 'run' | 'idle' | 'plan';

export interface StripBucket {
  /** Start of the half hour, ms since epoch. */
  start: number;
  runs: number;
  planned: number;
  /**
   * The planned fires in this half hour, one per trigger, with the
   * trigger's frequency as the server words it (schedule_text). Null text
   * comes from servers that do not send it.
   */
  plans: PlannedFire[];
  tone: StripTone;
  /** The run to open when the bucket is tapped: its most severe one. */
  runId: string | null;
}

export interface PlannedFire {
  taskName: string;
  scheduleText: string | null;
}

const SEVERITY: Record<StripTone, number> = { empty: 0, plan: 1, idle: 2, ok: 3, run: 4, warn: 5, bad: 6 };

function stripTone(status: string | null | undefined): StripTone {
  return status ? runStatusMeta(status).tone : 'idle';
}

/**
 * Groups a day of calendar items into 48 half-hour buckets for the
 * overview's ridge strip. Each bucket takes the tone of its most severe run;
 * a bucket with only planned fires is drawn dashed.
 */
export function dayStrip(items: readonly CalendarItem[], dayStart: number, slots = 48): StripBucket[] {
  const width = (24 * 3_600_000) / slots;
  const buckets: StripBucket[] = Array.from({ length: slots }, (_, i) => ({
    start: dayStart + i * width,
    runs: 0,
    planned: 0,
    plans: [],
    tone: 'empty',
    runId: null,
  }));
  const seen = new Set<string>();
  for (const item of items) {
    const at = Date.parse(item.start);
    const i = Math.floor((at - dayStart) / width);
    const bucket = buckets[i];
    if (!bucket) continue;
    if (item.kind === 'planned') {
      bucket.planned++;
      if (bucket.tone === 'empty') bucket.tone = 'plan';
      // A minutely trigger fires 30 times in a slot; name it once.
      const key = `${i}:${item.trigger_id ?? item.task.id}`;
      if (!seen.has(key)) {
        seen.add(key);
        bucket.plans.push({ taskName: item.task.name, scheduleText: item.schedule_text ?? null });
      }
      continue;
    }
    bucket.runs++;
    const tone = stripTone(item.status);
    if (SEVERITY[tone] >= SEVERITY[bucket.tone]) {
      bucket.tone = tone;
      bucket.runId = item.run_id;
    }
  }
  return buckets;
}

/** Local midnight of the day that contains `now`. */
export function startOfLocalDay(now: number): number {
  const d = new Date(now);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

export interface RidgeBar {
  key: string;
  runId: string;
  tone: StripTone;
  /** 0 to 1 of the tallest bar. */
  height: number;
  status: string;
  at: string;
  durationMs: number | null;
}

/**
 * The task page's strip: recent runs oldest first, each as tall as it took.
 * Runs that never started get a short stub so they still show.
 */
export function runRidges(runs: readonly RunSummary[], now: number, max = 30): RidgeBar[] {
  const recent = runs.slice(0, max).reverse();
  const durations = recent.map((r) => runDuration(r, now));
  const longest = Math.max(1, ...durations.map((d) => d ?? 0));
  return recent.map((r, i) => ({
    key: r.id,
    runId: r.id,
    tone: stripTone(r.status),
    height: Math.max(0.15, (durations[i] ?? 0) / longest),
    status: r.status,
    at: r.started_at ?? r.queued_at,
    durationMs: durations[i] ?? null,
  }));
}

export type RunOrigin = 'rerun' | 'retry' | 'test' | 'cron' | 'webhook' | 'manual';

/** What started a run, as the task page labels it: "定时 · v7", "重跑 · v8". */
export function runOrigin(run: { trigger: { kind: string }; test: boolean; rerun_of: string | null }): RunOrigin {
  if (run.rerun_of) return 'rerun';
  if (run.trigger.kind === 'retry') return 'retry';
  if (run.test) return 'test';
  if (run.trigger.kind === 'cron') return 'cron';
  if (run.trigger.kind === 'webhook') return 'webhook';
  return 'manual';
}

type RunStage = Schemas["RunStage"];

/**
 * The dependencies of each stage, by stage ID, as far as the run's own
 * stages resolve them: references to stages the run does not list, to the
 * stage itself, and repeats are dropped. Validation rejects such manifests,
 * so this only guards against data the console should not trust blindly.
 */
export function stageDependencies(stages: readonly Pick<RunStage, "id" | "after">[]): Map<string, string[]> {
  const known = new Set(stages.map((s) => s.id));
  const deps = new Map<string, string[]>();
  for (const s of stages) {
    // A server older than the after field sends none.
    const after = (s.after ?? []).filter((d, i, all) => d !== s.id && known.has(d) && all.indexOf(d) === i);
    deps.set(s.id, after);
  }
  return deps;
}

/** Whether any stage depends on another, so the run has a graph worth drawing. */
export function hasStageDependencies(stages: readonly Pick<RunStage, "id" | "after">[]): boolean {
  for (const after of stageDependencies(stages).values()) if (after.length > 0) return true;
  return false;
}

/** The display names of the stages each stage waits for, by stage ID. */
export function stageDependencyNames(stages: readonly Pick<RunStage, "id" | "name" | "after">[]): Map<string, string[]> {
  const name = new Map(stages.map((s) => [s.id, s.name]));
  const out = new Map<string, string[]>();
  for (const [id, after] of stageDependencies(stages)) out.set(id, after.map((d) => name.get(d) ?? d));
  return out;
}

export interface StageRow {
  id: string;
  name: string;
  /** Names of the stages this one waits for, in declaration order. */
  after: string[];
  glyph: Glyph;
  /** Never ran because an earlier stage failed or the run ended first. */
  skipped: boolean;
  failed: boolean;
  durationMs: number | null;
  message: string | null;
}

/**
 * The run page's stage list. A pending stage of a finished run did not run,
 * so it is drawn as skipped rather than as waiting.
 */
export function stageRows(run: Pick<Run, 'stages' | 'status'>, now: number): StageRow[] {
  const ended = !runStatusMeta(run.status).active;
  const after = stageDependencyNames(run.stages);
  return run.stages.map((s) => {
    const skipped = s.status === 'pending' && ended;
    let glyph: Glyph;
    switch (s.status) {
      case 'done':
        glyph = { tone: 'ok', icon: 'check' };
        break;
      case 'failed':
        glyph = { tone: 'bad', icon: 'x' };
        break;
      case 'running':
        glyph = ended ? { tone: 'warn', icon: 'unplug' } : { tone: 'run', icon: 'wave' };
        break;
      default:
        glyph = { tone: 'idle', icon: skipped ? 'skip' : 'pending' };
    }
    let durationMs: number | null = null;
    if (s.started_at && s.finished_at) durationMs = elapsedMs(s.started_at, s.finished_at);
    else if (s.started_at && s.status === 'running' && !ended) durationMs = elapsedMs(s.started_at, null, now);
    return { id: s.id, name: s.name, after: after.get(s.id) ?? [], glyph, skipped, failed: s.status === 'failed', durationMs, message: s.message };
  });
}

export interface RunCause {
  stage: string | null;
  message: string;
}

/** Why a run did not complete, with the stage's display name instead of its ID. */
export function runCause(run: Pick<Run, 'error' | 'stages'>): RunCause | null {
  if (!run.error) return null;
  const stage = run.error.stage ? (run.stages.find((s) => s.id === run.error!.stage)?.name ?? run.error.stage) : null;
  return { stage, message: run.error.message };
}

/** Resolves a JSON Pointer (RFC 6901) against a value; undefined when it does not exist. */
export function resolvePointer(data: unknown, pointer: string): unknown {
  if (pointer === '') return data;
  if (!pointer.startsWith('/')) return undefined;
  let node: unknown = data;
  for (const raw of pointer.slice(1).split('/')) {
    const key = raw.replace(/~1/g, '/').replace(/~0/g, '~');
    if (Array.isArray(node)) {
      if (!/^(0|[1-9]\d*)$/.test(key)) return undefined;
      node = node[Number(key)];
    } else if (node !== null && typeof node === 'object' && Object.prototype.hasOwnProperty.call(node, key)) {
      node = (node as Record<string, unknown>)[key];
    } else {
      return undefined;
    }
  }
  return node;
}

export type PanelView =
  | { id: string; type: 'metric'; title: string; value: number; label: string | null; unit: string | null }
  | { id: string; type: 'text'; title: string; text: string };

/**
 * The Agent panels the phone shows: metric and text. The others (bars,
 * breakdown, table) need more room and stay on the web console. Per the
 * contract, a panel whose data does not have the declared shape is left out
 * without an error.
 */
export function panelViews(panels: readonly PanelSpec[] | undefined, data: unknown): PanelView[] {
  if (!panels || data === undefined || data === null) return [];
  const out: PanelView[] = [];
  for (const p of panels) {
    const value = resolvePointer(data, p.source);
    if (p.type === 'metric') {
      if (typeof value === 'number' && Number.isFinite(value)) {
        out.push({ id: p.id, type: 'metric', title: p.title, value, label: null, unit: p.unit ?? null });
      } else if (value && typeof value === 'object' && !Array.isArray(value)) {
        const v = value as { value?: unknown; label?: unknown };
        if (typeof v.value === 'number' && Number.isFinite(v.value)) {
          out.push({
            id: p.id,
            type: 'metric',
            title: p.title,
            value: v.value,
            label: typeof v.label === 'string' ? v.label : null,
            unit: p.unit ?? null,
          });
        }
      }
    } else if (p.type === 'text' && typeof value === 'string' && value.length > 0) {
      out.push({ id: p.id, type: 'text', title: p.title, text: value.slice(0, 2000) });
    }
  }
  return out;
}

/** The cron trigger that fires next, for the task page's schedule chip. */
export function nextCron(triggers: readonly Trigger[]): Trigger | null {
  const crons = triggers.filter((t) => t.type === 'cron' && t.cron);
  if (crons.length === 0) return null;
  const enabled = crons.filter((t) => t.enabled);
  const pool = enabled.length ? enabled : crons;
  return [...pool].sort((a, b) => {
    const at = a.next_fire_at ? Date.parse(a.next_fire_at) : Infinity;
    const bt = b.next_fire_at ? Date.parse(b.next_fire_at) : Infinity;
    return at - bt;
  })[0]!;
}

export interface CredentialSummary {
  total: number;
  /** Some credential is missing, revoked or expired: the task cannot run as is. */
  broken: number;
  /** Some bound credential expires within the window. */
  expiring: number;
}

export function credentialSummary(items: readonly TaskCredential[], now: number, windowDays = 7): CredentialSummary {
  let broken = 0;
  let expiring = 0;
  for (const c of items) {
    if (c.status !== 'bound') {
      if (!c.optional) broken++;
    } else if (c.expires_at && Date.parse(c.expires_at) - now < windowDays * 86_400_000) {
      expiring++;
    }
  }
  return { total: items.length, broken, expiring };
}

/** Sorts the task list for the overview: soonest next run first, unscheduled last by name. */
export function sortTasksBySchedule(tasks: readonly TaskSummary[]): TaskSummary[] {
  return [...tasks].sort((a, b) => {
    const at = a.next_run_at ? Date.parse(a.next_run_at) : Infinity;
    const bt = b.next_run_at ? Date.parse(b.next_run_at) : Infinity;
    if (at !== bt) return at - bt;
    return a.name.localeCompare(b.name);
  });
}

/** Current activity takes precedence over activation: inactive tasks can run manually. */
export function taskStatus(task: {
  state: TaskSummary['state'];
  active_version?: number | null;
  archived_at?: string | null;
  active_runs?: number;
  last_run?: { status: string } | null;
  next_run_at?: string | null;
}): { glyph: Glyph; label: MessageKey } {
  if (task.archived_at) return { glyph: { tone: 'idle', icon: 'archive' }, label: 'task.archived' };
  if ((task.active_runs ?? 0) > 0 || (task.last_run && isRunActive(task.last_run.status))) {
    const status = task.last_run && isRunActive(task.last_run.status) ? task.last_run.status : 'running';
    return { glyph: runGlyph(status), label: runStatusMeta(status).label };
  }
  if (task.state === 'inactive') return {
    glyph: { tone: 'idle', icon: task.active_version === null ? 'pending' : 'pause' },
    label: task.active_version === null ? 'task.neverActivated' : 'task.paused',
  };
  if (task.next_run_at) return { glyph: { tone: 'idle', icon: 'calendar' }, label: 'task.scheduled' };
  return { glyph: { tone: 'idle', icon: 'pending' }, label: 'task.active' };
}

/** The same current task status is used by task lists and detail headers. */
export function taskGlyph(task: Parameters<typeof taskStatus>[0]): Glyph {
  return taskStatus(task).glyph;
}
