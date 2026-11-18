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
import { ArrowRotateLeft, ArrowUpRight, Hashtag } from "@gravity-ui/icons";
import { Alert, Chip, CopyButton, RunStages, StatusGlyph, StatusIcon, WaveLoader, buttonVariants } from "@lerryn/ui-web";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { useNow } from "../lib/hooks";
import { shortWhen } from "../lib/present";
import { LoadError } from "../shell/layout";
import { NotFoundBody } from "./NotFoundPage";
import { ArchiveRunButton } from "./run/ArchiveRunButton";
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
  const { t } = useI18n();
  const run = useRun(runId);
  if (run.isPending) {
    return (
      <div className="run-preview-loading" role="status">
        <WaveLoader size="sm" />
        {t("common.loading")}
      </div>
    );
  }
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
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-mono text-xs text-muted">{shortId(run.id)}</span>
        <div className="flex flex-wrap items-center gap-1">
          {run.archived_at ? <Chip size="sm" variant="soft">{t("run.archived")}</Chip> : null}
          <ArchiveRunButton key={run.id} run={run} />
          {isAttentionStatus(run.status) && task.data ? <RetryButton run={run} taskArchived={!!task.data.archived_at} /> : null}
          <CopyButton value={run.id} label={t("run.copyId")} icon={Hashtag} />
          <Link to="/runs/$runId" params={{ runId: run.id }} className={buttonVariants({ size: "sm", variant: "primary" })}>
            {t("runs.open")}
            <ArrowUpRight width={16} height={16} aria-hidden="true" />
          </Link>
        </div>
      </div>

      <div className="flex min-w-0 flex-col gap-2">
        <Link to="/tasks/$taskId" params={{ taskId: run.task_id }} className="self-start text-base font-semibold text-foreground hover:underline">
          {task.data?.name ?? t("run.taskLink")}
        </Link>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <span className="run-verdict" data-tone={meta.tone}>
            <span className={`g g--${meta.tone}`} aria-hidden="true">
              <StatusIcon icon={meta.icon} />
            </span>
            <span className="run-verdict__word">{t(meta.label)}</span>
          </span>
          <span className="run-verdict__meta">
            {duration !== null ? <span className="verdict-time">{formatDuration(duration, { whole: active })}</span> : null}
            <span>
              {shortWhen(run.started_at ?? run.queued_at, locale, now)} · v{run.version}
            </span>
          </span>
        </div>
      </div>

      {cause ? (
        <Alert status={meta.tone === "warn" ? "warning" : "danger"} role="status">
          <Alert.Indicator />
          <Alert.Content>
            <Alert.Description>
              {cause.stage ? (
                <>
                  <b className="font-semibold">{cause.stage}</b>
                  {" · "}
                </>
              ) : null}
              {cause.message}
            </Alert.Description>
          </Alert.Content>
        </Alert>
      ) : null}

      <RunStages stages={run.stages} now={now} />

      {logsExpired ? <span className="text-xs text-muted">{t("log.expired")}</span> : null}
      {lines.length > 0 ? (
        <section aria-label={t("runs.logTail")}>
          <div className="run-tail" role="log">
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
        <Link to="/runs/$runId" params={{ runId: rerun.id }} className="run-link self-start">
          <Chip variant="soft">
            <ArrowRotateLeft width={12} height={12} aria-hidden="true" />
            <Chip.Label>{t("run.rerunDone")}</Chip.Label>
            <StatusGlyph status={rerun.status} />
            <span className="font-mono text-xs text-muted">
              {shortWhen(rerun.started_at ?? rerun.queued_at, locale, now)} · v{rerun.version}
            </span>
          </Chip>
        </Link>
      ) : null}
    </>
  );
}
