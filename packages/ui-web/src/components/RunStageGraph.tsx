import {
  elapsedMs,
  formatDuration,
  hasStageDependencies,
  stageDependencyNames,
  stageStatusMeta,
  useI18n,
  type RunStage,
  type StatusIcon as StatusIconName,
} from "@lerryn/core";

import { StatusIcon } from "./RunStatusBadge";
import { RunTimeline } from "./RunTimeline";
import { STAGE_NODE, layoutStageGraph } from "./stageGraph";

export interface RunStageGraphProps {
  stages: readonly RunStage[];
  /** Current time, so running stages count up; pass a ticking value from the page. */
  now?: number;
  /** Called with a stage ID when the user picks a stage, for example to jump the log there. */
  onSelectStage?: (stageId: string) => void;
  className?: string;
}

const STAGE_ICON: Record<string, StatusIconName> = { pending: "clock", running: "wave", done: "check", failed: "x" };

const cx = (...parts: (string | false | undefined)[]) => parts.filter(Boolean).join(" ");

/** What every view of a stage says about it: status, duration, what it waits for. */
function useStageFacts(stages: readonly RunStage[], now: number) {
  const { t, locale } = useI18n();
  const after = stageDependencyNames(stages);
  const sep = locale === "zh-CN" ? "，" : ", ";
  return stages.map((stage) => {
    const meta = stageStatusMeta(stage.status);
    const ms = stage.started_at ? elapsedMs(stage.started_at, stage.finished_at, now) : null;
    const names = after.get(stage.id) ?? [];
    const status = t(meta.label);
    const duration = ms !== null ? formatDuration(ms, { whole: stage.status === "running" }) : null;
    const waits = names.length ? t("run.stageAfter", { names: names.join(t("run.stageAfterSep")) }) : null;
    return {
      stage,
      tone: meta.tone,
      icon: STAGE_ICON[stage.status] ?? "clock",
      status,
      duration,
      waits,
      label: [stage.name, status, duration, waits].filter(Boolean).join(sep),
    };
  });
}

/**
 * The stages of a run as a dependency graph: columns left to right, each
 * stage after the ones it declares in `after`, edges between them. Each
 * node carries a status icon and the status in words, and picking one
 * calls `onSelectStage` like the timeline does. Without any dependency the
 * component renders nothing; use RunStages to pick the right view.
 */
export function RunStageGraph({ stages, now = Date.now(), onSelectStage, className }: RunStageGraphProps) {
  const { t } = useI18n();
  const facts = useStageFacts(stages, now);
  if (!hasStageDependencies(stages)) return null;
  const layout = layoutStageGraph(stages);
  const byId = new Map(facts.map((f) => [f.stage.id, f]));

  return (
    <div className={cx("sgraph", className)} role="group" aria-label={t("run.stages")}>
      <div className="sgraph-canvas" style={{ width: layout.width, height: layout.height }}>
        <svg className="sgraph-edges" width={layout.width} height={layout.height} aria-hidden="true" focusable="false">
          {layout.edges.map((e) => (
            <path
              key={`${e.from}>${e.to}`}
              d={e.path}
              className={byId.get(e.from)?.stage.status === "done" ? "sedge sedge--done" : "sedge"}
              data-edge={`${e.from}>${e.to}`}
            />
          ))}
        </svg>
        {layout.nodes.map((n) => {
          const f = byId.get(n.id)!;
          return (
            <button
              key={n.id}
              type="button"
              className={`snode snode--${f.tone}`}
              style={{ left: n.x, top: n.y, width: STAGE_NODE.width, height: STAGE_NODE.height }}
              onClick={onSelectStage ? () => onSelectStage(n.id) : undefined}
              disabled={!onSelectStage}
              aria-label={f.label}
              // Long names are cut in the node; the tooltip keeps them whole.
              title={f.stage.message ? `${f.stage.name} · ${f.stage.message}` : f.stage.name}
              data-stage={n.id}
              data-status={f.stage.status}
            >
              <span className={`g g--${f.tone}`} aria-hidden="true">
                <StatusIcon icon={f.icon} />
              </span>
              <span className="snode-text" aria-hidden="true">
                <span className="snode-name">{f.stage.name}</span>
                <span className="snode-meta">{f.duration ? `${f.status} · ${f.duration}` : f.status}</span>
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

/**
 * The stages as an ordered list with what each one waits for, for screens
 * too narrow for the graph.
 */
export function RunStageList({ stages, now = Date.now(), onSelectStage, className }: RunStageGraphProps) {
  const { t } = useI18n();
  const facts = useStageFacts(stages, now);
  if (stages.length === 0) return null;
  return (
    <ol className={cx("slist", className)} aria-label={t("run.stages")}>
      {facts.map((f) => (
        <li key={f.stage.id}>
          <button
            type="button"
            className={`sitem sitem--${f.tone}`}
            onClick={onSelectStage ? () => onSelectStage(f.stage.id) : undefined}
            disabled={!onSelectStage}
            aria-label={f.label}
            title={f.stage.message ?? undefined}
            data-stage={f.stage.id}
            data-status={f.stage.status}
          >
            <span className={`g g--${f.tone}`} aria-hidden="true">
              <StatusIcon icon={f.icon} />
            </span>
            <span className="sitem-text" aria-hidden="true">
              <span className="sitem-name">{f.stage.name}</span>
              <span className="sitem-meta">{f.waits ? `${f.status} · ${f.waits}` : f.status}</span>
            </span>
            <span className="sitem-t" aria-hidden="true">
              {f.duration ?? "—"}
            </span>
          </button>
        </li>
      ))}
    </ol>
  );
}

/**
 * The stage view of a run page. Stages that declare dependencies are drawn
 * as a graph, which a narrow board swaps for the ordered list with each
 * stage's dependencies; stages without any keep the duration timeline.
 * The swap is a container query, so it follows the board, not the window.
 */
export function RunStages(props: RunStageGraphProps) {
  if (!hasStageDependencies(props.stages)) return <RunTimeline {...props} />;
  return (
    <div className={cx("stages", props.className)}>
      <RunStageGraph {...props} className="stages-graph" />
      <RunStageList {...props} className="stages-list" />
    </div>
  );
}
