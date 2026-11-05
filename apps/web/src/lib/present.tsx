import {
  formatClock,
  formatDate,
  formatDateTime,
  runStatusMeta,
  type Glyph,
  type Locale,
  type MessageKey,
  type TaskSummary,
  type Translate,
} from "@lerryn/core";
import { StatusIcon } from "@lerryn/ui-web";
import { CalendarClock, CircleDashed, KeyRound, Pause, Server } from "lucide-react";

const cx = (...parts: (string | false | undefined)[]) => parts.filter(Boolean).join(" ");

/** Draws a view-model glyph (tone + icon) with its accessible name. */
export function GlyphView({ glyph, label, size = "md" }: { glyph: Glyph; label: string; size?: "md" | "lg" }) {
  const { icon } = glyph;
  let inner;
  switch (icon) {
    case "key":
      inner = <KeyRound className="wl-icon" aria-hidden="true" />;
      break;
    case "server":
      inner = <Server className="wl-icon" aria-hidden="true" />;
      break;
    case "calendar":
      inner = <CalendarClock className="wl-icon" aria-hidden="true" />;
      break;
    case "pause":
      inner = <Pause className="wl-icon" aria-hidden="true" />;
      break;
    case "pending":
      inner = <CircleDashed className="wl-icon" aria-hidden="true" />;
      break;
    default:
      inner = <StatusIcon icon={icon} size={size === "lg" ? "md" : "sm"} />;
  }
  return (
    <span className={cx("g", `g--${glyph.tone}`, size === "lg" && "g--lg")} role="img" aria-label={label} title={label}>
      {inner}
    </span>
  );
}

/** The accessible name of a task row's glyph. */
export function taskGlyphLabel(task: Pick<TaskSummary, "state" | "last_run">, t: Translate): string {
  if (task.state === "inactive") return t("task.paused");
  if (task.last_run) return t(runStatusMeta(task.last_run.status).label);
  return t("states.awaitingFirstRun");
}

/**
 * A short time for list meta columns: "18:00" today, "明天 08:00" tomorrow,
 * "昨天 07:30" yesterday, "11/9 09:00" otherwise.
 */
export function shortWhen(value: string, locale: Locale, now = Date.now()): string {
  const d = new Date(value);
  const days = Math.round(
    (new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() -
      new Date(new Date(now).getFullYear(), new Date(now).getMonth(), new Date(now).getDate()).getTime()) /
      86_400_000,
  );
  if (days === 0) return formatClock(d);
  if (days === 1 || days === -1) return formatDateTime(d, locale, now);
  if (locale === "zh-CN") return `${d.getMonth() + 1}/${d.getDate()} ${formatClock(d)}`;
  return `${formatDate(d, locale, now)} ${formatClock(d)}`;
}

const ERROR_CODES: Record<string, MessageKey> = {
  script_error: "errorCode.script_error",
  timeout: "errorCode.timeout",
  run_interrupted: "errorCode.run_interrupted",
  out_of_memory: "errorCode.out_of_memory",
  credential_missing: "errorCode.credential_missing",
  credential_unavailable: "errorCode.credential_unavailable",
  dependency_install_failed: "errorCode.dependency_install_failed",
  runner_unavailable: "errorCode.runner_unavailable",
  queue_full: "errorCode.queue_full",
  validation_failed: "errorCode.validation_failed",
  internal_error: "errorCode.internal_error",
};

/** A run error category in words, or null for one the console does not name. */
export function errorCodeLabel(code: string | null | undefined, t: Translate): string | null {
  const key = code ? ERROR_CODES[code] : undefined;
  return key ? t(key) : null;
}

/** The words for why a run was skipped. */
export function skipReasonLabel(reason: string | null | undefined, t: Translate): string | null {
  switch (reason) {
    case "manual":
      return t("run.skip.manual");
    case "overlap":
      return t("run.skip.overlap");
    case "queue_full":
      return t("run.skip.queue_full");
    case "missed":
      return t("run.skip.missed");
    case "inactive":
      return t("run.skip.inactive");
    default:
      return null;
  }
}

/** What started a run, in words: "定时", "Webhook", "Agent 发起", "控制台发起", "自动重试". */
export function triggerLabel(run: { trigger: { kind: string; client?: string | null }; test: boolean; rerun_of: string | null }, t: Translate): string {
  if (run.rerun_of) return t("run.origin.rerun");
  switch (run.trigger.kind) {
    case "cron":
      return t("run.origin.cron");
    case "webhook":
      return t("run.origin.webhook");
    case "retry":
      return t("run.origin.retry");
    default:
      if (run.test) return t("run.origin.test");
      // A manual run without a token name was started from a browser session.
      return run.trigger.client === null ? t("run.origin.console") : t("run.origin.manual");
  }
}
