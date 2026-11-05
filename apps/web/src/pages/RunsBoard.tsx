import {
  RUN_BOARD_COLUMNS,
  RUN_BOARD_WINDOW_MS,
  isRunActive,
  runBoardColumn,
  shortId,
  useI18n,
  useRunList,
  type RunBoardColumn,
  type RunSummary,
} from "@lerryn/core";
import { StatusGlyph } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";

import { useNow } from "../lib/hooks";
import { errorCodeLabel, shortWhen, skipReasonLabel } from "../lib/present";
import { LoadError, LoadMore } from "../shell/layout";

/** Pages fetched without asking; past this the board offers "show more". */
const AUTO_PAGES = 6;

/**
 * The runs board: the last 48 hours of runs in four columns by status. When
 * a run's status moves it to another column, its card is carried there.
 */
export function RunsBoard({ taskId, tools }: { taskId?: string; tools: ReactNode }) {
  const { t } = useI18n();
  // Fixed per visit so polling keeps one query; the exact window is cut below.
  const [since] = useState(() => new Date(Date.now() - RUN_BOARD_WINDOW_MS).toISOString());
  const runs = useRunList({ taskId, since });
  const now = useNow(30_000);
  const items = runs.data?.items;
  const pages = runs.data?.pageCount ?? 0;

  const { hasNextPage, isFetchingNextPage, fetchNextPage } = runs;
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage && pages < AUTO_PAGES) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage, pages]);

  const columns = useMemo(() => {
    const out = Object.fromEntries(RUN_BOARD_COLUMNS.map((c) => [c, [] as RunSummary[]])) as Record<RunBoardColumn, RunSummary[]>;
    const from = now - RUN_BOARD_WINDOW_MS;
    for (const run of items ?? []) {
      if (Date.parse(run.queued_at) < from) continue;
      out[runBoardColumn(run.status)].push(run);
    }
    return out;
  }, [items, now]);

  const boardRef = useRef<HTMLDivElement>(null);
  useCardMoves(boardRef, columns, !!runs.data);

  return (
    <>
      <div className="board-top">
        <div className="row" style={{ gap: 12, alignItems: "baseline" }}>
          <h1 className="h2">{t("runs.title")}</h1>
          <span className="caption muted">{t("runs.board.window")}</span>
        </div>
        {tools}
      </div>
      {runs.isError ? (
        <LoadError error={runs.error} onRetry={() => void runs.refetch()} />
      ) : (
        <div className="kb" ref={boardRef} aria-busy={runs.isPending}>
          {RUN_BOARD_COLUMNS.map((column) => (
            <BoardColumn key={column} column={column} runs={columns[column]} now={now} />
          ))}
        </div>
      )}
      <LoadMore show={pages >= AUTO_PAGES && !!runs.data?.hasMore} pending={isFetchingNextPage} onPress={() => void fetchNextPage()} />
    </>
  );
}

function BoardColumn({ column, runs, now }: { column: RunBoardColumn; runs: RunSummary[]; now: number }) {
  const { t } = useI18n();
  const title = t(`runs.board.column.${column}`);
  return (
    <section className={`kb-col kb-col--${column}`} aria-label={title} data-column={column}>
      <h2 className="kb-head">
        <span>{title}</span>
        <span className="n">{runs.length}</span>
      </h2>
      <ul className="kb-list">
        {runs.map((run) => (
          <li key={run.id} className="kb-item" data-run={run.id}>
            <RunCard run={run} now={now} />
          </li>
        ))}
      </ul>
      {runs.length === 0 ? <div className="kb-empty">{t("runs.board.empty")}</div> : null}
    </section>
  );
}

function RunCard({ run, now }: { run: RunSummary; now: number }) {
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
    <Link to="/runs/$runId" params={{ runId: run.id }} className="kb-card">
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
  index: number;
  x: number;
  y: number;
}

/**
 * Animates cards whose place on the board changed (FLIP). A card that changes
 * column is lifted, carried across and set down like a dragged card; one that
 * only shifts within its column slides; a new one fades in. Places are offsets
 * inside the board (its offsetParent), which ignore transforms and scrolling,
 * so a card mid-flight or a scrolled page is not mistaken for a move.
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
      columns[column].forEach((run, index) => {
        const el = board.querySelector<HTMLElement>(`[data-run="${run.id}"]`);
        if (!el) return;
        const spot = { column, index, x: el.offsetLeft, y: el.offsetTop };
        after.set(run.id, spot);
        if (!before || reduce || typeof el.animate !== "function") return;

        const prev = before.get(run.id);
        if (!prev) {
          el.animate([{ opacity: 0, transform: "scale(0.96)" }, { opacity: 1, transform: "none" }], { duration: 260, easing: "ease-out" });
          return;
        }
        const dx = prev.x - spot.x;
        const dy = prev.y - spot.y;
        if (prev.column !== column) {
          el.getAnimations().forEach((a) => a.cancel());
          carry(el, dx, dy);
        } else if (dx || dy) {
          el.getAnimations().forEach((a) => a.cancel());
          el.animate([{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "none" }], { duration: 280, easing: "cubic-bezier(0.2, 0, 0, 1)" });
        }
      });
    }
    spots.current = after;
  }, [boardRef, columns, loaded]);
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
