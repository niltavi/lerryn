import {
  RUN_FILTER_STATUSES,
  filterRuns,
  groupRunsByTask,
  isRunActive,
  shortId,
  useI18n,
  useRunList,
  type RunFilter,
  type RunSummary,
  type RunTaskGroup,
} from "@lerryn/core";
import { IconButton, StatusGlyph } from "@lerryn/ui-web";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ChevronRight, ChevronsDownUp, ChevronsUpDown, Kanban, List, ListTree, Search, X } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";

import { useNow } from "../lib/hooks";
import { shortWhen, skipReasonLabel } from "../lib/present";
import { Board, ListColumn, LoadMore, usePageTitle } from "../shell/layout";
import { RunPreview } from "./RunPreview";
import { RunsBoard } from "./RunsBoard";

export interface RunsSearch {
  run?: string;
  filter?: RunFilter;
  task?: string;
  view?: RunsView | "tasks";
}

export type RunsView = "list" | "board";

export function validateRunsSearch(s: Record<string, unknown>): RunsSearch {
  const out: RunsSearch = {};
  if (typeof s.run === "string" && s.run) out.run = s.run;
  if (s.filter === "failed" || s.filter === "active" || s.filter === "archived") out.filter = s.filter;
  if (typeof s.task === "string" && s.task) out.task = s.task;
  if (s.view === "board" || s.view === "list" || s.view === "tasks") out.view = s.view;
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
  const grouped = search.view === "tasks";
  const view = search.view === "tasks" ? "list" : search.view ?? storedRunsView();
  useEffect(() => {
    if (search.view) storeRunsView(search.view === "tasks" ? "list" : search.view);
  }, [search.view]);
  const runs = useRunList(
    { status: RUN_FILTER_STATUSES[filter], taskId: search.task, archived: filter === "archived" },
    { enabled: view === "list" },
  );
  const items = runs.data?.items ?? [];
  const now = useNow(30_000);
  const [searching, setSearching] = useState(false);
  const [query, setQuery] = useState("");
  const searchButton = useRef<HTMLButtonElement>(null);
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(() => new Set());
  const shown = filterRuns(items, query);
  const selected = search.run ?? shown[0]?.id;
  const groups = grouped ? groupRunsByTask(shown) : [];
  const allCollapsed = groups.length > 0 && groups.every((g) => collapsed.has(g.task.id));

  const setFilter = (f: RunFilter) =>
    void navigate({
      to: "/runs",
      search: { view: search.view, task: search.task, filter: f === "all" ? undefined : f },
      replace: true,
    });
  const setGrouped = (on: boolean) =>
    void navigate({ to: "/runs", search: { ...search, view: on ? "tasks" : "list" }, replace: true });
  const closeSearch = () => {
    setSearching(false);
    setQuery("");
    searchButton.current?.focus();
  };
  const toggleGroup = (taskId: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (!next.delete(taskId)) next.add(taskId);
      return next;
    });
  const toggleAllGroups = () =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      for (const { task } of groups) {
        if (allCollapsed) next.delete(task.id);
        else next.add(task.id);
      }
      return next;
    });

  const viewSwitch = <ViewSwitch view={view} listView={grouped ? "tasks" : "list"} task={search.task} />;

  if (view === "board") {
    return <RunsBoard taskId={search.task} runId={search.run} viewSwitch={viewSwitch} />;
  }

  return (
    <>
      <ListColumn
        title={t("runs.title")}
        label={t("runs.list")}
        inlineTools
        tools={
          <>
            {viewSwitch}
            <div className="tools">
              {grouped && groups.length > 1 ? (
                <IconButton
                  label={allCollapsed ? t("runs.expandAll") : t("runs.collapseAll")}
                  icon={allCollapsed ? ChevronsUpDown : ChevronsDownUp}
                  onPress={toggleAllGroups}
                />
              ) : null}
              <IconButton
                label={grouped ? t("runs.ungroup") : t("runs.groupByTask")}
                icon={grouped ? List : ListTree}
                onPress={() => setGrouped(!grouped)}
              />
              {items.length > 0 || searching ? (
                <IconButton
                  ref={searchButton}
                  aria-expanded={searching}
                  label={searching ? t("runs.closeSearch") : t("runs.search")}
                  icon={searching ? X : Search}
                  onPress={() => {
                    if (searching) closeSearch();
                    else setSearching(true);
                  }}
                />
              ) : null}
            </div>
          </>
        }
      >
        {searching ? (
          <div className="list-search">
            <label className="log-find">
              <Search className="wl-icon" aria-hidden="true" />
              <input
                type="search"
                value={query}
                autoFocus
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    e.preventDefault();
                    closeSearch();
                  }
                }}
                aria-label={t("runs.search")}
                placeholder={t("runs.searchPlaceholder")}
              />
            </label>
          </div>
        ) : null}
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
            <Link to="/runs" search={{ filter: search.filter, view: search.view }} className="link caption">
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
        ) : runs.data && shown.length === 0 ? (
          <div className="list-empty">{t("runs.noMatch")}</div>
        ) : null}
        {grouped
          ? groups.map((g) => (
              <RunGroup
                key={g.task.id}
                group={g}
                open={!collapsed.has(g.task.id)}
                onToggle={() => toggleGroup(g.task.id)}
                selected={selected}
                now={now}
              />
            ))
          : shown.map((run) => <RunItem key={run.id} run={run} current={run.id === selected} now={now} />)}
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
function ViewSwitch({ view, listView, task }: { view: RunsView; listView: "list" | "tasks"; task?: string }) {
  const { t } = useI18n();
  const views: [RunsView, typeof List, string][] = [
    ["list", List, t("runs.view.list")],
    ["board", Kanban, t("runs.view.board")],
  ];
  return (
    <nav className="seg" aria-label={t("runs.view.label")}>
      {views.map(([v, Icon, label]) => (
        <Link key={v} to="/runs" search={{ view: v === "list" ? listView : v, ...(task ? { task } : {}) }} aria-current={view === v ? "page" : undefined}>
          <Icon className="wl-icon" width={16} height={16} aria-hidden="true" />
          {label}
        </Link>
      ))}
    </nav>
  );
}

/**
 * One task in the grouped view: a header that folds the task's runs away,
 * above the runs themselves on a tree line. A folded task still shows its
 * latest run's status, so a failure is not hidden by folding.
 */
function RunGroup({
  group,
  open,
  onToggle,
  selected,
  now,
}: {
  group: RunTaskGroup;
  open: boolean;
  onToggle: () => void;
  selected?: string;
  now: number;
}) {
  const { t, locale } = useI18n();
  const bodyId = useId();
  const latest = group.runs[0]!;
  const holdsSelected = group.runs.some((r) => r.id === selected);
  return (
    <div className="rg" data-open={open}>
      <button
        type="button"
        className="rg-head"
        aria-expanded={open}
        aria-controls={bodyId}
        data-current={!open && holdsSelected ? "true" : undefined}
        onClick={onToggle}
      >
        <ChevronRight className="wl-icon rg-chevron" aria-hidden="true" />
        <StatusGlyph status={latest.status} />
        <span style={{ minWidth: 0 }}>
          <span className="li-name">{group.task.name}</span>
          <span className="li-sub">{t("runs.groupRuns", { count: group.runs.length })}</span>
        </span>
        <span className="li-meta">{shortWhen(latest.started_at ?? latest.queued_at, locale, now)}</span>
      </button>
      <div id={bodyId} className="rg-body" role="group" aria-label={group.task.name} hidden={!open}>
        {open ? group.runs.map((run) => (
          <RunItem key={run.id} run={run} current={run.id === selected} now={now} nested />
        )) : null}
      </div>
    </div>
  );
}

function RunItem({ run, current, now, nested = false }: { run: RunSummary; current: boolean; now: number; nested?: boolean }) {
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
      className={nested ? "li li--leaf" : "li"}
      aria-current={current ? "true" : undefined}
      replace
    >
      <StatusGlyph status={run.status} />
      <span style={{ minWidth: 0 }}>
        {nested ? (
          // Under its task the name would repeat, so the origin leads.
          <span className={mono ? "li-name mono-sm" : "li-name"}>{sub}</span>
        ) : (
          <>
            <span className="li-name">{run.task.name}</span>
            <span className={mono ? "li-sub mono-sm" : "li-sub"}>{sub}</span>
          </>
        )}
      </span>
      <span className={bad ? "li-meta bad" : "li-meta"}>{shortWhen(run.started_at ?? run.queued_at, locale, now)}</span>
    </Link>
  );
}
