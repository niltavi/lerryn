import {
  RUN_FILTER_STATUSES,
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
  useRunList,
  useTask,
  type Run,
  type RunFilter,
  type RunSummary,
} from "@lerryn/core";
import { CopyButton, RunStages, StatusGlyph, StatusIcon } from "@lerryn/ui-web";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowUpRight, CircleAlert, Hash, RotateCcw } from "lucide-react";

import { useNow } from "../lib/hooks";
import { shortWhen, skipReasonLabel } from "../lib/present";
import { Board, ListColumn, LoadError, LoadMore, usePageTitle } from "../shell/layout";
import { NotFoundBody } from "./NotFoundPage";
import { RetryButton } from "./run/RunActions";

export interface RunsSearch {
  run?: string;
  filter?: RunFilter;
  task?: string;
}

export function validateRunsSearch(s: Record<string, unknown>): RunsSearch {
  const out: RunsSearch = {};
  if (typeof s.run === "string" && s.run) out.run = s.run;
  if (s.filter === "failed" || s.filter === "active") out.filter = s.filter;
  if (typeof s.task === "string" && s.task) out.task = s.task;
  return out;
}

const FILTERS: RunFilter[] = ["all", "failed", "active"];

/**
 * Runs (prototype Runs.dc.html): a list of runs, newest first, beside a
 * preview of the selected one with a link to its full page.
 */
export function RunsPage() {
  const { t } = useI18n();
  usePageTitle(t("runs.title"));
  const search = useSearch({ from: "/app/runs" });
  const navigate = useNavigate();
  const filter = search.filter ?? "all";
  const runs = useRunList({ status: RUN_FILTER_STATUSES[filter], taskId: search.task });
  const items = runs.data?.items ?? [];
  const selected = search.run ?? items[0]?.id;
  const now = useNow(30_000);

  const setFilter = (f: RunFilter) =>
    void navigate({ to: "/runs", search: { ...(f === "all" ? {} : { filter: f }), ...(search.task ? { task: search.task } : {}) }, replace: true });

  return (
    <>
      <ListColumn title={t("runs.title")} label={t("runs.list")}>
        <div className="list-seg">
          <div className="seg seg--block" role="group" aria-label={t("runs.filterLabel")}>
            {FILTERS.map((f) => (
              <button key={f} type="button" aria-pressed={filter === f} onClick={() => setFilter(f)}>
                {t(`runs.filter.${f}`)}
              </button>
            ))}
          </div>
        </div>
        {search.task && items[0] ? (
          <div className="list-group">
            <span className="mono-sm">{items[0].task.name}</span>
            <Link to="/runs" search={search.filter ? { filter: search.filter } : {}} className="link caption">
              {t("runs.allTasks")}
            </Link>
          </div>
        ) : null}
        {runs.isError ? (
          <div className="list-empty">
            <span>{t("states.errorTitle")}</span>
            <button className="link" type="button" style={{ border: 0, background: "none", cursor: "pointer", font: "inherit" }} onClick={() => void runs.refetch()}>
              {t("common.retry")}
            </button>
          </div>
        ) : runs.data && items.length === 0 ? (
          <div className="list-empty">{filter === "all" ? t("states.noRuns") : t("runs.noneMatch")}</div>
        ) : null}
        {items.map((run) => (
          <RunItem key={run.id} run={run} current={run.id === selected} now={now} />
        ))}
        <div style={{ padding: "8px 12px" }}>
          <LoadMore show={!!runs.data?.hasMore} pending={runs.isFetchingNextPage} onPress={() => void runs.fetchNextPage()} />
        </div>
      </ListColumn>
      <Board label={t("runs.preview")}>{selected ? <RunPreview runId={selected} /> : null}</Board>
    </>
  );
}

function RunItem({ run, current, now }: { run: RunSummary; current: boolean; now: number }) {
  const { t, locale } = useI18n();
  const search = useSearch({ from: "/app/runs" });
  let sub: string = shortId(run.id);
  let mono = true;
  const skip = run.status === "skipped" ? skipReasonLabel(run.skip_reason, t) : null;
  if (skip) {
    sub = skip;
    mono = false;
  } else if (run.rerun_of) {
    sub = `${t("run.origin.rerun")} · v${run.version}`;
    mono = false;
  } else if (run.test) {
    sub = `${t("run.origin.test")} · v${run.version}`;
    mono = false;
  } else if (run.current_stage && isRunActive(run.status)) {
    sub = run.current_stage;
    mono = false;
  }
  const bad = run.status === "failed" || run.status === "timed_out";
  return (
    <Link
      to="/runs"
      search={{ ...search, run: run.id }}
      className="li"
      aria-current={current ? "true" : undefined}
      replace
    >
      <StatusGlyph status={run.status} />
      <span style={{ minWidth: 0 }}>
        <span className="li-name">{run.task.name}</span>
        <span className={mono ? "li-sub mono-sm" : "li-sub"}>{sub}</span>
      </span>
      <span className={bad ? "li-meta bad" : "li-meta"}>{shortWhen(run.started_at ?? run.queued_at, locale, now)}</span>
    </Link>
  );
}

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

function RunPreview({ runId }: { runId: string }) {
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
