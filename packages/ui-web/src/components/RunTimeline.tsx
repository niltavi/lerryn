import { elapsedMs, formatDuration, stageStatusMeta, useI18n, type RunStage } from "@lerryn/core";
import { X } from "lucide-react";

import { WaveLoader } from "./Wave";

export interface RunTimelineProps {
  stages: readonly RunStage[];
  /** Current time, so running stages grow; pass a ticking value from the page. */
  now?: number;
  /** Called with a stage ID when the user picks a stage, for example to jump the log there. */
  onSelectStage?: (stageId: string) => void;
  className?: string;
}

/** Stages without a duration still get a visible width. */
const MIN_GROW = 1;

/**
 * The stage bar of a run: one segment per declared or reported stage, its
 * width proportional to how long it took. Only stages the platform received
 * are drawn; with no stages the component renders nothing, since the page
 * then shows the overall status alone (PRD 4, execution diagram).
 */
export function RunTimeline({ stages, now = Date.now(), onSelectStage, className }: RunTimelineProps) {
  const { t, locale } = useI18n();
  const sep = locale === "zh-CN" ? "，" : ", ";
  if (stages.length === 0) return null;
  const durations = stages.map((s) => (s.started_at ? elapsedMs(s.started_at, s.finished_at, now) : null));
  const known = durations.filter((d): d is number => d !== null && d > 0);
  // Pending stages get the average width, so the bar does not suggest how long they will take.
  const fallback = known.length ? known.reduce((a, b) => a + b, 0) / known.length : 1;

  return (
    <div className={["flutes", className].filter(Boolean).join(" ")} role="group" aria-label={t("run.stages")}>
      {stages.map((stage, i) => {
        const meta = stageStatusMeta(stage.status);
        const ms = durations[i];
        const grow = Math.max(MIN_GROW, (ms ?? fallback) / 1000);
        const statusText = t(meta.label);
        const shown = ms !== null && ms !== undefined ? formatDuration(ms, { whole: stage.status === "running" }) : null;
        return (
          <button
            key={stage.id}
            type="button"
            className={`flute flute--${meta.tone}`}
            style={{ flexGrow: grow }}
            onClick={onSelectStage ? () => onSelectStage(stage.id) : undefined}
            disabled={!onSelectStage}
            // Starts with the visible text, name and time as shown, so voice control can say what is on screen (WCAG 2.5.3).
            aria-label={[shown ? `${stage.name} ${shown}` : stage.name, statusText].join(sep)}
            title={stage.message ?? undefined}
            data-stage={stage.id}
            data-status={stage.status}
          >
            <span className="flute-bar" aria-hidden="true" />
            <span className="flute-name" aria-hidden="true">
              {stage.status === "running" ? <WaveLoader size="sm" /> : null}
              {stage.status === "failed" ? <X className="wl-icon" /> : null}
              <span>{stage.name}</span>
            </span>
            <span className="flute-t" aria-hidden="true">
              {shown ?? "—"}
            </span>
          </button>
        );
      })}
    </div>
  );
}
