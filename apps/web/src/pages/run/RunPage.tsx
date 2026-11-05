import {
  agentPanels,
  elapsedMs,
  fetchFullLog,
  firstLineOfStage,
  formatClockSeconds,
  formatCallDuration,
  formatDateTime,
  formatDuration,
  isAttentionStatus,
  isNotFound,
  isRunActive,
  logToText,
  runStatusMeta,
  shortId,
  summarizeRunEvents,
  useApi,
  useI18n,
  useReruns,
  useRun,
  useRunEvents,
  useRunLog,
  useTask,
  useTaskVersion,
  type Run,
  type StatusTone,
  type ToolCallView,
} from "@lerryn/core";
import {
  AgentPanels,
  CopyButton,
  InfoChip,
  LogViewer,
  ResultPanel,
  RunStages,
  StatusGlyph,
  StatusIcon,
  ToneGlyph,
} from "@lerryn/ui-web";
import { Link, useParams } from "@tanstack/react-router";
import {
  CalendarClock,
  Check,
  ChevronLeft,
  CircleAlert,
  FlaskConical,
  Hash,
  Link2,
  RotateCcw,
  Server,
  TriangleAlert,
  Webhook,
  Wrench,
  X,
} from "lucide-react";
import { useState } from "react";

import { saveText, useNow } from "../../lib/hooks";
import { errorCodeLabel, skipReasonLabel, triggerLabel } from "../../lib/present";
import { Board, LoadError, usePageTitle } from "../../shell/layout";
import { NotFoundBody } from "../NotFoundPage";
import { storedRunsView } from "../RunsPage";
import { Artifacts } from "./Artifacts";
import { RetryButton } from "./RunActions";

export function RunPage() {
  const { runId } = useParams({ from: "/app/runs/$runId" });
  const run = useRun(runId);

  if (run.isPending) return <Board>{null}</Board>;
  if (run.isError) {
    return (
      <Board>
        {isNotFound(run.error) ? <NotFoundBody /> : <LoadError error={run.error} onRetry={() => void run.refetch()} />}
      </Board>
    );
  }
  return <RunBoard run={run.data} />;
}

function RunBoard({ run }: { run: Run }) {
  const { t, locale } = useI18n();
  const api = useApi();
  const task = useTask(run.task_id);
  const version = useTaskVersion(run.task_id, run.version);
  const log = useRunLog(run.id, { expired: !!run.logs_expired_at });
  const active = isRunActive(run.status);
  const now = useNow(1000, active);
  const events = useRunEvents(run.id, active);
  const reruns = useReruns(run.id, isAttentionStatus(run.status) || run.status === "cancelled");
  const origin = useRun(run.rerun_of ?? run.retry_of ?? "", { enabled: !!(run.rerun_of ?? run.retry_of) });
  const meta = runStatusMeta(run.status);
  const [target, setTarget] = useState<{ seq: number; key: number } | null>(null);
  const [downloading, setDownloading] = useState(false);

  const taskName = task.data?.name;
  usePageTitle(`${taskName ?? shortId(run.id)} · ${t(meta.label)}`);

  const start = run.started_at ?? run.queued_at;
  const duration = run.started_at ? elapsedMs(run.started_at, run.finished_at, now) : null;
  const errorStage = run.error?.stage ? run.stages.find((s) => s.id === run.error?.stage) : undefined;
  const summary = summarizeRunEvents(events.data?.events ?? []);
  const rerun = reruns.data?.items[0];
  const skip = run.status === "skipped" ? skipReasonLabel(run.skip_reason, t) : null;
  const panels = agentPanels(version.data?.manifest.panels, run.result?.data);

  const download = async () => {
    setDownloading(true);
    try {
      const lines = await fetchFullLog(api, run.id);
      saveText(`${taskName ?? "run"}-${shortId(run.id)}.log`, logToText(lines));
    } finally {
      setDownloading(false);
    }
  };

  const timeline: { at: string; label: string; tone?: StatusTone }[] = [
    { at: run.queued_at, label: run.trigger.client ? `${t("run.eventQueued")} · ${run.trigger.client}` : t("run.eventQueued") },
  ];
  if (run.claimed_at) {
    timeline.push({ at: run.claimed_at, label: run.runner ? `${t("run.eventPreparing")} · ${run.runner.name}` : t("run.eventPreparing") });
  }
  if (run.started_at) timeline.push({ at: run.started_at, label: t("run.eventStarted"), tone: "run" });
  if (run.cancelled_by) timeline.push({ at: run.cancelled_by.requested_at, label: t("run.eventCancelRequested") });
  if (run.finished_at) {
    timeline.push({
      at: run.finished_at,
      label: [t(meta.label), run.exit_code !== null ? t("run.exitCodeValue", { code: run.exit_code }) : null].filter(Boolean).join(" · "),
      tone: meta.tone,
    });
  }
  const TriggerIcon = run.trigger.kind === "webhook" ? Webhook : run.trigger.kind === "cron" ? CalendarClock : run.rerun_of || run.trigger.kind === "retry" ? RotateCcw : Hash;

  return (
    <Board label={t("run.info")}>
      <div className="board-top">
        <nav className="eyebrow" aria-label={t("run.location")}>
          <Link to="/runs" search={{ view: storedRunsView(), run: run.id }}>
            <ChevronLeft className="wl-icon" width={16} aria-hidden="true" />
            {t("nav.runs")}
          </Link>
          <span aria-hidden="true">/</span>
          <Link to="/tasks/$taskId" params={{ taskId: run.task_id }}>
            {taskName ?? t("run.taskLink")}
          </Link>
          <span className="mono-sm">{shortId(run.id)}</span>
        </nav>
        <div className="tools">
          {!active && task.data ? <RetryButton run={run} taskArchived={!!task.data.archived_at} /> : null}
          <CopyButton value={run.id} label={t("run.copyId")} icon={Hash} />
          <CopyButton value={run.links.console} label={t("run.copyLink")} icon={Link2} />
        </div>
      </div>

      <div className={`verdict verdict--${meta.tone}`}>
        <div className="verdict-main">
          <span className={`g g--${meta.tone} g--lg`} aria-hidden="true">
            <StatusIcon icon={meta.icon} size="md" />
          </span>
          <h1 className="verdict-word" aria-live="polite">
            {t(meta.label)}
          </h1>
        </div>
        {duration !== null ? <span className="verdict-time">{formatDuration(duration, { whole: active })}</span> : null}
        <span className="muted">
          {t(run.started_at ? "run.startedAt" : "run.queuedAt", { time: formatDateTime(start, locale, now) })}
        </span>
      </div>

      {run.error ? (
        <div className={meta.tone === "warn" ? "cause cause--warn" : "cause"} role="status">
          <CircleAlert className="wl-icon" aria-hidden="true" />
          <span>
            {errorStage ? (
              <>
                <b>{errorStage.name}</b>
                {" · "}
              </>
            ) : null}
            {run.error.message}
            {errorCodeLabel(run.error.code, t) ? <span className="muted"> · {errorCodeLabel(run.error.code, t)}</span> : null}
          </span>
        </div>
      ) : skip ? (
        <div className="cause cause--warn" role="status">
          <CircleAlert className="wl-icon" aria-hidden="true" />
          <span>{skip}</span>
        </div>
      ) : null}

      {summary.warnings.map((w) => (
        <div className="cause cause--warn" role="status" key={w.ts + w.message}>
          <TriangleAlert className="wl-icon" aria-hidden="true" />
          <span>{w.message}</span>
        </div>
      ))}

      <div className="chips" aria-label={t("run.info")}>
        {rerun ? (
          <Link to="/runs/$runId" params={{ runId: rerun.id }} className="chip">
            <RotateCcw className="wl-icon" aria-hidden="true" />
            {t("run.rerunDone")}
            <StatusGlyph status={rerun.status} />
            <span className="mono-sm muted">v{rerun.version}</span>
          </Link>
        ) : null}
        {origin.data ? (
          <Link to="/runs/$runId" params={{ runId: origin.data.id }} className="chip">
            <RotateCcw className="wl-icon" aria-hidden="true" />
            {run.rerun_of ? t("run.rerunOf") : t("run.retryOf")}
            <StatusGlyph status={origin.data.status} />
            <span className="mono-sm muted">v{origin.data.version}</span>
          </Link>
        ) : null}
        <InfoChip
          detailsLabel={t("run.timeline")}
          details={
            <ol className="events">
              {timeline.map((e) => (
                <li key={e.at + e.label} className={e.tone ? `event event--${e.tone}` : "event"}>
                  <span className="event-t">{formatClockSeconds(e.at)}</span>
                  <span className="event-dot" />
                  <span>{e.label}</span>
                </li>
              ))}
            </ol>
          }
        >
          <TriggerIcon className="wl-icon" aria-hidden="true" />
          {triggerLabel(run, t)}
        </InfoChip>
        <InfoChip
          detailsLabel={t("run.versionAndEnv")}
          details={
            <dl className="kv">
              <dt>{t("run.version")}</dt>
              <dd className="mono-sm">{origin.data && origin.data.version !== run.version ? `v${origin.data.version} → v${run.version}` : `v${run.version}`}</dd>
              {run.environment ? (
                <>
                  <dt>{t("run.environment")}</dt>
                  <dd className="mono-sm">{run.environment.slice(0, 16)}</dd>
                </>
              ) : null}
              {version.data?.manifest.limits?.timeout ? (
                <>
                  <dt>{t("task.timeout")}</dt>
                  <dd>{version.data.manifest.limits.timeout}</dd>
                </>
              ) : null}
              {run.test ? (
                <>
                  <dt>{t("run.test")}</dt>
                  <dd>{t("common.yes")}</dd>
                </>
              ) : null}
            </dl>
          }
        >
          <span className="mono-sm">v{run.version}</span>
          {run.test ? <FlaskConical className="wl-icon" aria-label={t("run.test")} /> : null}
        </InfoChip>
        <InfoChip
          detailsLabel={t("run.execution")}
          details={
            <dl className="kv">
              {run.exit_code !== null ? (
                <>
                  <dt>{t("run.exitCode")}</dt>
                  <dd className="mono-sm">{run.exit_code}</dd>
                </>
              ) : null}
              <dt>{t("run.attempt")}</dt>
              <dd>
                {run.attempt} / {run.max_attempts}
              </dd>
              <dt>{t("run.runId")}</dt>
              <dd className="mono-sm">{run.id}</dd>
              {run.cancel_requested ? (
                <>
                  <dt>{t("run.eventCancelRequested")}</dt>
                  <dd>{run.cancelled_by?.client ?? t("common.yes")}</dd>
                </>
              ) : null}
            </dl>
          }
        >
          <Server className="wl-icon" aria-hidden="true" />
          {run.runner?.name ?? t("run.execution")}
        </InfoChip>
        {summary.toolCallCount > 0 ? (
          <InfoChip
            // Opens rightwards: chips wrap, so on a narrow board this one starts a line and a left-opening list would leave the screen.
            detailsLabel={t("run.toolCalls")}
            details={<ToolCalls calls={summary.calls} failed={summary.toolFailedCount} stages={run.stages} />}
          >
            <Wrench className="wl-icon" aria-hidden="true" />
            {t("run.toolCount", { count: summary.toolCallCount })}
            {summary.toolFailedCount ? <span className="dot dot--bad" role="img" aria-label={t("run.toolFailed", { count: summary.toolFailedCount })} /> : null}
          </InfoChip>
        ) : null}
      </div>

      <RunStages
        stages={run.stages}
        now={now}
        onSelectStage={(stageId) => {
          const seq = firstLineOfStage(log.lines, stageId);
          if (seq !== null) setTarget({ seq, key: Date.now() });
        }}
      />

      <ResultPanel result={run.result} />
      <AgentPanels panels={panels} />

      <Artifacts runId={run.id} active={active} />

      <div id="logs">
        <LogViewer
          lines={log.lines}
          loaded={log.loaded}
          complete={log.complete}
          dropped={log.dropped}
          reconnectAttempt={log.reconnectAttempt}
          failed={!!log.error}
          expired={log.expired}
          onRetry={log.retry}
          onLoadEarlier={log.loadEarlier}
          loadingEarlier={log.loadingEarlier}
          onDownload={() => void download()}
          downloading={downloading}
          scrollTarget={target}
          stages={run.stages}
        />
      </div>
    </Board>
  );
}

/**
 * Each tool call the platform recorded, in order: where it ran, what it
 * called, how it went and with which (redacted, cut) arguments. What the
 * tool returned is never recorded, so it is never shown.
 */
function ToolCalls({ calls, failed, stages }: { calls: ToolCallView[]; failed: number; stages: Run["stages"] }) {
  const { t } = useI18n();
  const stageName = new Map(stages.map((s) => [s.id, s.name]));
  return (
    <div className="stack" style={{ gap: 0 }}>
      <div className="pop-title">
        <span>{t("run.toolCalls")}</span>
        {failed ? <span style={{ color: "var(--danger)" }}>{t("run.toolFailed", { count: failed })}</span> : null}
      </div>
      <div className="tool-calls" role="list">
        {calls.map((c) => {
          const where = c.stage ? (stageName.get(c.stage) ?? c.stage) : null;
          return (
            <div className="pop-row" role="listitem" key={c.seq} style={{ alignItems: "flex-start" }}>
              <span className="stack" style={{ gap: 2, minWidth: 0 }}>
                <span className="mono-sm" style={{ color: "var(--ink)", overflowWrap: "anywhere" }}>
                  {c.method ? `${c.tool} · ${c.method}` : c.tool}
                </span>
                {where || c.args ? (
                  <span className="caption muted" style={{ overflowWrap: "anywhere" }}>
                    {where}
                    {where && c.args ? " · " : null}
                    {c.args ? (
                      <span className="mono-sm" title={c.argsFull ?? undefined}>
                        {c.args}
                      </span>
                    ) : null}
                  </span>
                ) : null}
                {c.error ? (
                  <span className="caption" style={{ color: "var(--danger)", overflowWrap: "anywhere" }}>
                    {c.error}
                  </span>
                ) : null}
              </span>
              <span className="row" style={{ gap: 6, flex: "none" }}>
                <span className="mono-sm muted">{formatCallDuration(c.durationMs)}</span>
                <ToneGlyph tone={c.ok ? "ok" : "bad"} icon={c.ok ? Check : X} label={c.ok ? t("run.toolOk") : t("run.toolError")} />
              </span>
            </div>
          );
        })}
      </div>
      <span className="caption muted" style={{ marginTop: 8 }}>
        {t("run.toolCallsNote")}
      </span>
    </div>
  );
}
