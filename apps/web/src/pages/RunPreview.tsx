import {
  elapsedMs,
  formatDuration,
  formatLogTime,
  isAttentionStatus,
  isNotFound,
  isRunActive,
  runCause,
  runStatusMeta,
  shortId,
  shouldRetry,
  unwrap,
  useApi,
  useI18n,
  useReruns,
  useRun,
  useTask,
  type Run,
} from "@lerryn/core";
import { CopyButton, RunStages, StatusGlyph, StatusIcon } from "@lerryn/ui-web";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, CircleAlert, Hash, RotateCcw } from "lucide-react";

import { useNow } from "../lib/hooks";
import { shortWhen } from "../lib/present";
import { LoadError } from "../shell/layout";
import { NotFoundBody } from "./NotFoundPage";
import { RetryButton } from "./run/RunActions";

/** The last lines of a run's log, for the preview. */
function useLogTail(runId: string, active: boolean, expired: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: ["runs", runId, "logs", "tail"],
    queryFn: () => unwrap(api.GET("/api/v1/runs/{runId}/logs", { params: { path: { runId }, query: { tail: 8 } } })),
    refetchInterval: active ? 3_000 : false,
    enabled: !expired,
    retry: shouldRetry,
  });
}

export function RunPreview({ runId }: { runId: string }) {
  const run = useRun(runId);
  if (run.isPending) return null;
  if (run.isError) {
    return isNotFound(run.error) ? <NotFoundBody /> : <LoadError error={run.error} onRetry={() => void run.refetch()} />;
  }
  return <PreviewBody run={run.data} />;
}

function PreviewBody({ run }: { run: Run }) {
  const { t, locale } = useI18n();
  const active = isRunActive(run.status);
  const now = useNow(1000, active);
  const meta = runStatusMeta(run.status);
  const cause = runCause(run);
  const logsExpired = !!run.logs_expired_at;
  const tail = useLogTail(run.id, active, logsExpired);
  const task = useTask(run.task_id);
  const reruns = useReruns(run.id, isAttentionStatus(run.status));
  const rerun = reruns.data?.items[0];
  const duration = run.started_at ? elapsedMs(run.started_at, run.finished_at, now) : null;
  const lines = tail.data?.lines ?? [];

  return (
    <>
      <div className="board-top">
        <span className="eyebrow mono-sm">{shortId(run.id)}</span>
        <div className="tools" style={{ gap: 8 }}>
          {isAttentionStatus(run.status) && task.data ? <RetryButton run={run} taskArchived={!!task.data.archived_at} /> : null}
          <CopyButton value={run.id} label={t("run.copyId")} icon={Hash} />
          <Link to="/runs/$runId" params={{ runId: run.id }} className="chip" style={{ height: 36 }}>
            {t("runs.open")}
            <ArrowUpRight className="wl-icon" aria-hidden="true" />
          </Link>
        </div>
      </div>

      <div className="stack" style={{ gap: 16 }}>
        <Link to="/tasks/$taskId" params={{ taskId: run.task_id }} className="h3" style={{ color: "var(--ink)", textDecoration: "none" }}>
          {task.data?.name ?? t("run.taskLink")}
        </Link>
        <div className={`verdict verdict--${meta.tone}`}>
          <div className="verdict-main">
            <span className={`g g--${meta.tone} g--lg`} aria-hidden="true">
              <StatusIcon icon={meta.icon} size="md" />
            </span>
            <span className="verdict-word">{t(meta.label)}</span>
          </div>
          {duration !== null ? <span className="verdict-time">{formatDuration(duration, { whole: active })}</span> : null}
          <span className="muted">
            {shortWhen(run.started_at ?? run.queued_at, locale, now)} · v{run.version}
          </span>
        </div>
      </div>

      {cause ? (
        <div className={meta.tone === "warn" ? "cause cause--warn" : "cause"} role="status">
          <CircleAlert className="wl-icon" aria-hidden="true" />
          <span>
            {cause.stage ? (
              <>
                <b>{cause.stage}</b>
                {" · "}
              </>
            ) : null}
            {cause.message}
          </span>
        </div>
      ) : null}

      <RunStages stages={run.stages} now={now} />

      {logsExpired ? <span className="caption muted">{t("log.expired")}</span> : null}
      {lines.length > 0 ? (
        <section className="log" aria-label={t("runs.logTail")}>
          <div className="log-body" role="log" style={{ maxHeight: 240 }}>
            {lines.map((l) => (
              <div key={l.seq} className={`ll ll--${l.stream}`}>
                <span className="n">{l.seq}</span>
                <span className="t">{formatLogTime(l.ts)}</span>
                <span className="m">{l.text}</span>
              </div>
            ))}
          </div>
        </section>
      ) : null}

      {rerun ? (
        <Link to="/runs/$runId" params={{ runId: rerun.id }} className="chip" style={{ alignSelf: "flex-start" }}>
          <RotateCcw className="wl-icon" aria-hidden="true" />
          {t("run.rerunDone")}
          <StatusGlyph status={rerun.status} />
          <span className="mono-sm muted">
            {shortWhen(rerun.started_at ?? rerun.queued_at, locale, now)} · v{rerun.version}
          </span>
        </Link>
      ) : null}
    </>
  );
}
