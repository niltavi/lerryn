import type { Schemas } from "@lerryn/api";

import type { MessageKey } from "./i18n/i18n";

export type RunStatus = Schemas["RunStatus"];
export type StageStatus = Schemas["RunStage"]["status"];
export type ResultStatus = Schemas["RunResult"]["status"];

/** Every run status, in lifecycle order (tech design 5.4). */
export const RUN_STATUSES = [
  "queued",
  "preparing",
  "running",
  "cancelling",
  "completed",
  "failed",
  "timed_out",
  "cancelled",
  "interrupted",
  "skipped",
] as const satisfies readonly RunStatus[];

/**
 * Colour family of a status. Maps to the design tokens: idle → liner-2 and
 * ink-muted, run → run-soft and run, ok → success, bad → danger, warn →
 * warning-soft and warning-ink.
 */
export type StatusTone = "idle" | "run" | "ok" | "bad" | "warn";

/**
 * Icon of a status. `wave` is the animated corrugation loader; the rest map
 * to Lucide icons in the UI layer.
 */
export type StatusIcon = "clock" | "wave" | "check" | "x" | "timer" | "ban" | "unplug" | "skip";

export interface RunStatusMeta {
  tone: StatusTone;
  icon: StatusIcon;
  /** The run has not reached a final state and may still change. */
  active: boolean;
  label: MessageKey;
}

// docs/zh-CN/design-system.md section 7 gives this mapping.
const META: Record<RunStatus, RunStatusMeta> = {
  queued: { tone: "idle", icon: "clock", active: true, label: "run.status.queued" },
  preparing: { tone: "run", icon: "wave", active: true, label: "run.status.preparing" },
  running: { tone: "run", icon: "wave", active: true, label: "run.status.running" },
  cancelling: { tone: "idle", icon: "wave", active: true, label: "run.status.cancelling" },
  completed: { tone: "ok", icon: "check", active: false, label: "run.status.completed" },
  failed: { tone: "bad", icon: "x", active: false, label: "run.status.failed" },
  timed_out: { tone: "bad", icon: "timer", active: false, label: "run.status.timed_out" },
  cancelled: { tone: "idle", icon: "ban", active: false, label: "run.status.cancelled" },
  interrupted: { tone: "warn", icon: "unplug", active: false, label: "run.status.interrupted" },
  skipped: { tone: "idle", icon: "skip", active: false, label: "run.status.skipped" },
};

/**
 * Returns how to show a run status. An unknown value from a newer server is
 * shown as an idle, final state rather than crashing the page.
 */
export function runStatusMeta(status: RunStatus | string): RunStatusMeta {
  return (
    META[status as RunStatus] ?? { tone: "idle", icon: "clock", active: false, label: "common.none" }
  );
}

/** Whether the run may still change, so the page should keep polling. */
export function isRunActive(status: RunStatus | string): boolean {
  return runStatusMeta(status).active;
}

export interface StageStatusMeta {
  tone: StatusTone;
  label: MessageKey;
}

const STAGE_META: Record<StageStatus, StageStatusMeta> = {
  pending: { tone: "idle", label: "run.stage.pending" },
  running: { tone: "run", label: "run.stage.running" },
  done: { tone: "ok", label: "run.stage.done" },
  failed: { tone: "bad", label: "run.stage.failed" },
};

export function stageStatusMeta(status: StageStatus | string): StageStatusMeta {
  return STAGE_META[status as StageStatus] ?? STAGE_META.pending;
}

const RESULT_META: Record<ResultStatus, StageStatusMeta> = {
  ok: { tone: "ok", label: "result.status.ok" },
  partial: { tone: "warn", label: "result.status.partial" },
  failed: { tone: "bad", label: "result.status.failed" },
};

export function resultStatusMeta(status: ResultStatus | string): StageStatusMeta {
  return RESULT_META[status as ResultStatus] ?? { tone: "idle", label: "common.none" };
}
