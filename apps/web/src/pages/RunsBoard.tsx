import {
  RUN_BOARD_COLUMNS,
  isRunActive,
  runBoardColumn,
  shortId,
  useCalendar,
  useI18n,
  useRunList,
  type CalendarItem,
  type RunBoardColumn,
  type RunSummary,
} from "@lerryn/core";
import { StatusGlyph } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";
import { X } from "lucide-react";
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";

import { useNow } from "../lib/hooks";
import { errorCodeLabel, GlyphView, shortWhen, skipReasonLabel } from "../lib/present";
import { LoadError, LoadMore } from "../shell/layout";
import { RunPreview } from "./RunPreview";

/** Pages fetched without asking; past this the board offers "show more". */
const AUTO_PAGES = 6;

/** Finished columns grow all day; they show this many until expanded. */
export const COLUMN_LIMIT = 8;
const CAPPED: ReadonlySet<RunBoardColumn> = new Set(["done", "attention"]);

/**
 * The runs board: three local days of run history and planned fires through
 * tomorrow. When a run moves to another column, its card is carried there.
 */
export function RunsBoard({ taskId, runId, viewSwitch }: { taskId?: string; runId?: string; viewSwitch: ReactNode }) {
  const { t } = useI18n();
  const now = useNow(30_000);
  const date = new Date(now);
  const midnight = (offset: number) => new Date(date.getFullYear(), date.getMonth(), date.getDate() + offset).getTime();
  const from = midnight(-2);
  const tomorrow = midnight(1);
  const to = midnight(2);
  const runs = useRunList({ taskId, since: new Date(from).toISOString() });
  // Today's runs and plans share the calendar's item limit. Give tomorrow its own window.
  const todayCalendar = useCalendar({ projectId: null, taskId, from: midnight(0), to: tomorrow });
  const tomorrowCalendar = useCalendar({ projectId: null, taskId, from: tomorrow, to });
  const todayItems = todayCalendar.isPlaceholderData ? undefined : todayCalendar.data?.items;
  const tomorrowItems = tomorrowCalendar.isPlaceholderData ? undefined : tomorrowCalendar.data?.items;
  const calendarPending = todayCalendar.isPending || tomorrowCalendar.isPending || todayCalendar.isPlaceholderData || tomorrowCalendar.isPlaceholderData;
  const calendarError = todayCalendar.error ?? tomorrowCalendar.error;
  const calendarTruncated = todayCalendar.data?.truncated || tomorrowCalendar.data?.truncated;
  const items = runs.data?.items;
  const pages = runs.data?.pageCount ?? 0;
  const [expanded, setExpanded] = useState<ReadonlySet<RunBoardColumn>>(new Set());

  const { hasNextPage, isFetchingNextPage, fetchNextPage } = runs;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && pages < AUTO_PAGES) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage, pages]);

  const columns = useMemo(() => {
    const out = Object.fromEntries(RUN_BOARD_COLUMNS.map((c) => [c, [] as RunSummary[]])) as Record<RunBoardColumn, RunSummary[]>;
    for (const run of items ?? []) {
      const queued = Date.parse(run.queued_at);
      if (queued < from || queued >= to) continue;
      out[runBoardColumn(run.status)].push(run);
    }
    return out;
  }, [items, from, to]);

  const planned = useMemo(() => {
    // The run list can observe a fire before the calendar refreshes.
    const fired = new Set((items ?? []).flatMap(({ trigger }) =>
      trigger.trigger_id && trigger.scheduled_at ? [`${trigger.trigger_id}:${Date.parse(trigger.scheduled_at)}`] : []));
    return [...(todayItems ?? []), ...(tomorrowItems ?? [])].filter((item) => {
      const at = Date.parse(item.start);
      return item.kind === "planned" && at >= now && at < to && !fired.has(`${item.trigger_id}:${at}`);
    });
  }, [items, todayItems, tomorrowItems, now, to]);

  const shown = useMemo(() => {
    const out = { ...columns };
    for (const c of CAPPED) if (!expanded.has(c)) out[c] = columns[c].slice(0, COLUMN_LIMIT);
    return out;
  }, [columns, expanded]);

  const boardRef = useRef<HTMLDivElement>(null);
  useCardMoves(boardRef, shown, !!runs.data);

  const toggle = (c: RunBoardColumn) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (!next.delete(c)) next.add(c);
      return next;
    });

  const calendarNotice = calendarError ? (
    <LoadError error={calendarError} onRetry={() => {
      if (todayCalendar.isError) void todayCalendar.refetch();
      if (tomorrowCalendar.isError) void tomorrowCalendar.refetch();
    }} />
  ) : !calendarPending && calendarTruncated ? <div className="kb-empty">{t("runs.board.plansTruncated")}</div> : null;

  return (
    <>
      <main className="board board--runs" aria-label={t("runs.board.label")}>
        <div className="list-head list-head--inline">
          <h1 className="list-title">{t("runs.title")}</h1>
          {viewSwitch}
          <span className="caption muted">{t("runs.board.window")}</span>
        </div>
        {runs.isError ? (
          <LoadError error={runs.error} onRetry={() => void runs.refetch()} />
        ) : (
          <div className="kb" ref={boardRef} aria-busy={runs.isPending}>
            {RUN_BOARD_COLUMNS.map((column) => (
              <BoardColumn
                key={column}
                column={column}
                runs={shown[column]}
                total={columns[column].length}
                expanded={expanded.has(column)}
                onToggle={CAPPED.has(column) ? () => toggle(column) : undefined}
                taskId={taskId}
                now={now}
                planned={column === "scheduled" ? planned : undefined}
                pending={column === "scheduled" && calendarPending}
                notice={column === "scheduled" ? calendarNotice : null}
              />
            ))}
          </div>
        )}
        <LoadMore show={pages >= AUTO_PAGES && !!runs.data?.hasMore} pending={isFetchingNextPage} onPress={() => void fetchNextPage()} />
      </main>
      {runId ? (
        <aside className="board kb-preview" aria-label={t("runs.preview")}>
          <Link
            to="/runs"
            search={{ view: "board", ...(taskId ? { task: taskId } : {}) }}
            className="chip kb-preview-close"
            activeOptions={{ exact: true, includeSearch: true }}
            aria-label={t("common.close")}
          >
            <X className="wl-icon" aria-hidden="true" />
          </Link>
          <RunPreview runId={runId} />
        </aside>
      ) : null}
    </>
  );
}

function BoardColumn({
  column,
  runs,
  total,
  expanded,
  onToggle,
  taskId,
  now,
  planned = [],
  pending,
  notice,
}: {
  column: RunBoardColumn;
  runs: RunSummary[];
  total: number;
  expanded: boolean;
  onToggle?: () => void;
  taskId?: string;
  now: number;
  planned?: CalendarItem[];
  pending: boolean;
  notice: ReactNode;
}) {
  const { t } = useI18n();
  const title = t(`runs.board.column.${column}`);
  return (
    <section className={`kb-col kb-col--${column}`} aria-label={title} data-column={column} aria-busy={pending || undefined}>
      <h2 className="kb-head">
        <span>{title}</span>
        <span className="n">{total + planned.length}</span>
      </h2>
      <ul className="kb-list">
        {runs.map((run) => (
          <li key={run.id} className="kb-item" data-run={run.id}>
            <RunCard run={run} taskId={taskId} now={now} />
          </li>
        ))}
        {planned.map((item) => (
          <li key={`${item.task.id}:${item.trigger_id}:${item.start}`} className="kb-item">
            <PlannedCard item={item} now={now} />
          </li>
        ))}
      </ul>
      {notice}
      {total === 0 && planned.length === 0 && !pending && !notice ? <div className="kb-empty">{t("runs.board.empty")}</div> : null}
      {onToggle && total > COLUMN_LIMIT ? (
        <button type="button" className="kb-more" aria-expanded={expanded} onClick={onToggle}>
          {expanded ? t("runs.board.showLess") : t("runs.board.showAll", { count: total })}
        </button>
      ) : null}
    </section>
  );
}

function PlannedCard({ item, now }: { item: CalendarItem; now: number }) {
  const { t, locale } = useI18n();
  return (
    <Link to="/tasks/$taskId" params={{ taskId: item.task.id }} className="kb-card">
      <GlyphView glyph={{ tone: "idle", icon: "calendar" }} label={t("task.scheduled")} />
      <span className="li-name">{item.task.name}</span>
      <time className="li-meta" dateTime={item.start}>{shortWhen(item.start, locale, now)}</time>
    </Link>
  );
}

function RunCard({ run, taskId, now }: { run: RunSummary; taskId?: string; now: number }) {
  const { t, locale } = useI18n();
  let sub = shortId(run.id);
  let mono = true;
  const words =
    (run.status === "skipped" ? skipReasonLabel(run.skip_reason, t) : null) ??
    (run.current_stage && isRunActive(run.status) ? run.current_stage : null) ??
    errorCodeLabel(run.error?.code, t);
  if (words) {
    sub = words;
    mono = false;
  }
  const bad = run.status === "failed" || run.status === "timed_out";
  return (
    <Link
      to="/runs"
      search={{ view: "board", run: run.id, ...(taskId ? { task: taskId } : {}) }}
      className="kb-card"
      activeOptions={{ exact: true, includeSearch: true }}
    >
      <StatusGlyph status={run.status} />
      <span style={{ minWidth: 0 }}>
        <span className="li-name">{run.task.name}</span>
        <span className={mono ? "li-sub mono-sm" : "li-sub"}>{sub}</span>
      </span>
      <span className={bad ? "li-meta bad" : "li-meta"}>
        {shortWhen(run.finished_at ?? run.started_at ?? run.queued_at, locale, now)}
      </span>
    </Link>
  );
}

interface Spot {
  column: RunBoardColumn;
  x: number;
  y: number;
}

/**
 * Animates cards whose place on the board changed (FLIP). A card that changes
 * column is lifted, carried across and set down like a dragged card; one that
 * only shifts within its column slides briefly, if it is on screen; a new one
 * fades in. Places are offsets inside the board (its offsetParent), which
 * ignore transforms and scrolling, so a card mid-flight or a scrolled page is
 * not mistaken for a move.
 */
function useCardMoves(boardRef: RefObject<HTMLDivElement | null>, columns: Record<RunBoardColumn, RunSummary[]>, loaded: boolean) {
  const spots = useRef<Map<string, Spot> | null>(null);

  useLayoutEffect(() => {
    const board = boardRef.current;
    // The first loaded board is the starting point, not a batch of new cards.
    if (!board || !loaded) return;
    const reduce = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
    const before = spots.current;
    const after = new Map<string, Spot>();

    for (const column of RUN_BOARD_COLUMNS) {
      for (const run of columns[column]) {
        const el = board.querySelector<HTMLElement>(`[data-run="${run.id}"]`);
        if (!el) continue;
        const spot = { column, x: el.offsetLeft, y: el.offsetTop };
        after.set(run.id, spot);
        if (!before || reduce || typeof el.animate !== "function") continue;

        const prev = before.get(run.id);
        if (!prev) {
          if (onScreen(el)) el.animate([{ opacity: 0, transform: "scale(0.96)" }, { opacity: 1, transform: "none" }], { duration: 260, easing: "ease-out" });
          continue;
        }
        const dx = prev.x - spot.x;
        const dy = prev.y - spot.y;
        if (prev.column !== column) {
          el.getAnimations().forEach((a) => a.cancel());
          carry(el, dx, dy);
        } else if ((dx || dy) && onScreen(el)) {
          // Neighbours making room should read as a nudge, not a second move.
          el.getAnimations().forEach((a) => a.cancel());
          el.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }], { duration: 200, easing: "ease-out" });
        }
      }
    }
    spots.current = after;
  }, [boardRef, columns, loaded]);
}

function onScreen(el: HTMLElement): boolean {
  const r = el.getBoundingClientRect();
  return r.bottom > 0 && r.top < window.innerHeight;
}

/** Lift, carry and drop: the card leaves its old spot and lands in the new one. */
function carry(el: HTMLElement, dx: number, dy: number) {
  const tilt = dx > 0 ? 2 : -2;
  el.classList.add("kb-item--moving");
  const anim = el.animate(
    [
      { transform: `translate(${dx}px, ${dy}px)`, offset: 0, easing: "ease-out" },
      { transform: `translate(${dx}px, ${dy - 6}px) rotate(${tilt}deg) scale(1.04)`, offset: 0.18, easing: "cubic-bezier(0.45, 0, 0.2, 1)" },
      { transform: `translate(0, -6px) rotate(${-tilt / 2}deg) scale(1.04)`, offset: 0.82, easing: "ease-in" },
      { transform: "none", offset: 1 },
    ],
    { duration: 900 },
  );
  const done = () => el.classList.remove("kb-item--moving");
  anim.onfinish = done;
  anim.oncancel = done;
}
