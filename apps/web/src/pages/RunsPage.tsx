import {
  RUN_FILTER_STATUSES,
  isRunActive,
  shortId,
  useI18n,
  useRunList,
  type RunFilter,
  type RunSummary,
} from "@lerryn/core";
import { StatusGlyph } from "@lerryn/ui-web";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Kanban, List } from "lucide-react";
import { useEffect } from "react";

import { useNow } from "../lib/hooks";
import { shortWhen, skipReasonLabel } from "../lib/present";
import { Board, ListColumn, LoadMore, usePageTitle } from "../shell/layout";
import { RunPreview } from "./RunPreview";
import { RunsBoard } from "./RunsBoard";

export interface RunsSearch {
  run?: string;
  filter?: RunFilter;
  task?: string;
  view?: RunsView;
}

export type RunsView = "list" | "board";

export function validateRunsSearch(s: Record<string, unknown>): RunsSearch {
  const out: RunsSearch = {};
  if (typeof s.run === "string" && s.run) out.run = s.run;
  if (s.filter === "failed" || s.filter === "active" || s.filter === "archived") out.filter = s.filter;
  if (typeof s.task === "string" && s.task) out.task = s.task;
  if (s.view === "board" || s.view === "list") out.view = s.view;
  return out;
}

const FILTERS: RunFilter[] = ["all", "failed", "active", "archived"];

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
  const view = search.view ?? storedRunsView();
  useEffect(() => {
    if (search.view) storeRunsView(search.view);
  }, [search.view]);
  const runs = useRunList(
    { status: RUN_FILTER_STATUSES[filter], taskId: search.task, archived: filter === "archived" },
    { enabled: view === "list" },
  );
  const items = runs.data?.items ?? [];
  const selected = search.run ?? items[0]?.id;
  const now = useNow(30_000);

  const setFilter = (f: RunFilter) =>
    void navigate({ to: "/runs", search: { ...(f === "all" ? {} : { filter: f }), ...(search.task ? { task: search.task } : {}) }, replace: true });
  const viewSwitch = <ViewSwitch view={view} task={search.task} />;

  if (view === "board") {
    return <RunsBoard taskId={search.task} runId={search.run} viewSwitch={viewSwitch} />;
  }

  return (
    <>
      <ListColumn title={t("runs.title")} label={t("runs.list")} tools={viewSwitch} inlineTools>
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

const VIEW_KEY = "lerryn.runs.view";

/** The view last chosen on this browser; the list when none was or storage is off. */
export function storedRunsView(): RunsView {
  try {
    return localStorage.getItem(VIEW_KEY) === "board" ? "board" : "list";
  } catch {
    return "list";
  }
}

function storeRunsView(view: RunsView) {
  try {
    localStorage.setItem(VIEW_KEY, view);
  } catch {
    // Remembering the view is a convenience; private windows may refuse it.
  }
}

/** List / Board as plain links, so each view has its own address. */
function ViewSwitch({ view, task }: { view: RunsView; task?: string }) {
  const { t } = useI18n();
  const views: [RunsView, typeof List, string][] = [
    ["list", List, t("runs.view.list")],
    ["board", Kanban, t("runs.view.board")],
  ];
  return (
    <nav className="seg" aria-label={t("runs.view.label")}>
      {views.map(([v, Icon, label]) => (
        <Link key={v} to="/runs" search={{ view: v, ...(task ? { task } : {}) }} aria-current={view === v ? "page" : undefined}>
          <Icon className="wl-icon" width={16} height={16} aria-hidden="true" />
          {label}
        </Link>
      ))}
    </nav>
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
