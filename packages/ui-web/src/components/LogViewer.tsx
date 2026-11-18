import {
  filterLines,
  findGap,
  formatCount,
  formatLogTime,
  searchLines,
  useI18n,
  type LogLine,
  type LogStream,
} from "@lerryn/core";
import { ArrowDown, ArrowDownToLine, ChevronUp, CircleExclamation, Clock, TriangleExclamation } from "@gravity-ui/icons";
import { Button, SearchField, ToggleButton, ToggleButtonGroup } from "@heroui/react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from "react";

import { Toggle } from "./Controls";
import { IconButton } from "./IconButton";
import { WaveLoader } from "./Wave";

type StreamFilter = LogStream | "all";
type Row = { kind: "line"; line: LogLine } | { kind: "gap"; count: number } | { kind: "stage"; stage: string; seq: number };

export interface LogViewerProps {
  /** Every line held, sorted by seq; filtering happens here. */
  lines: readonly LogLine[];
  /** The first page has arrived. */
  loaded: boolean;
  /** The run has ended and the newest line is held. */
  complete: boolean;
  /** Lines dropped over the per-run log limit. */
  dropped?: number;
  /** Consecutive failed attempts to read new lines. */
  reconnectAttempt?: number;
  /** The first page failed. */
  failed?: boolean;
  /** The log was removed by the retention policy. */
  expired?: boolean;
  onRetry?: () => void;
  /** Reads the next page of lines missing before the tail. */
  onLoadEarlier?: () => void;
  loadingEarlier?: boolean;
  onDownload?: () => void;
  downloading?: boolean;
  /** Scrolls to this line, for example the first line of a stage picked in the timeline. */
  scrollTarget?: { seq: number; key: number } | null;
  /**
   * The run's stages, to label where each one's output begins. Lines name
   * their stage by ID; without a matching entry the ID is shown.
   */
  stages?: readonly { id: string; name: string }[];
  /** Largest viewport height in px; shorter logs shrink to fit. Defaults to 560. */
  height?: number;
  className?: string;
}

const ROW_HEIGHT = 20;
const DEFAULT_HEIGHT = 560;
const BODY_PADDING = 16;
/** Hands the row geometry to views.css, so the styles cannot drift from the virtualizer. */
const GEOMETRY = { "--log-row": `${ROW_HEIGHT}px`, "--log-pad": `${BODY_PADDING / 2}px` } as CSSProperties;
const ICON = { width: 16, height: 16, "aria-hidden": true } as const;

/** Splits text around case-insensitive matches of query and marks them. */
export function highlight(text: string, query: string): ReactNode {
  const q = query.trim();
  if (!q) return text;
  const lower = text.toLowerCase();
  const needle = q.toLowerCase();
  const parts: ReactNode[] = [];
  let from = 0;
  for (let at = lower.indexOf(needle); at !== -1; at = lower.indexOf(needle, from)) {
    if (at > from) parts.push(text.slice(from, at));
    parts.push(<mark key={at}>{text.slice(at, at + q.length)}</mark>);
    from = at + q.length;
  }
  if (from < text.length) parts.push(text.slice(from));
  return parts;
}

/**
 * A run's log with virtual scrolling, so long logs stay fast. It follows new
 * output until the reader scrolls up, separates stdout, stderr and platform
 * messages, searches, jumps to the end, loads earlier lines on request and
 * says when lines were dropped or the connection is being retried.
 */
export function LogViewer({
  lines,
  loaded,
  complete,
  dropped = 0,
  reconnectAttempt = 0,
  failed = false,
  expired = false,
  onRetry,
  onLoadEarlier,
  loadingEarlier = false,
  onDownload,
  downloading = false,
  scrollTarget,
  stages,
  height,
  className,
}: LogViewerProps) {
  const { t, locale } = useI18n();
  const [stream, setStream] = useState<StreamFilter>("all");
  const [query, setQuery] = useState("");
  const [hitIndex, setHitIndex] = useState(0);
  const [follow, setFollow] = useState(true);
  const [atBottom, setAtBottom] = useState(true);
  const scrollRef = useRef<HTMLDivElement>(null);
  const programmatic = useRef(false);

  const visible = useMemo(() => filterLines(lines, stream), [lines, stream]);
  const rows = useMemo<Row[]>(() => {
    const gap = findGap(lines);
    const boundary = gap ? lines[gap.index]!.seq : Infinity;
    const out: Row[] = [];
    let gapPlaced = !gap;
    let stage: string | null = null;
    for (const line of visible) {
      if (!gapPlaced && line.seq >= boundary) {
        out.push({ kind: "gap", count: gap!.count });
        gapPlaced = true;
        // Lines after the gap may be in another stage; label them again.
        stage = null;
      }
      // A label where a stage's output begins, so lines read as belonging
      // to their stage. Platform lines outside any stage carry none.
      if (line.stage && line.stage !== stage) out.push({ kind: "stage", stage: line.stage, seq: line.seq });
      if (line.stage) stage = line.stage;
      out.push({ kind: "line", line });
    }
    if (!gapPlaced) out.push({ kind: "gap", count: gap!.count });
    return out;
  }, [lines, visible]);
  const stageNames = useMemo(() => new Map((stages ?? []).map((s) => [s.id, s.name])), [stages]);
  const hits = useMemo(() => searchLines(visible, query), [visible, query]);
  const hitSet = useMemo(() => new Set(hits), [hits]);

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 20,
    getItemKey: (i) => {
      const row = rows[i]!;
      return row.kind === "gap" ? "gap" : row.kind === "stage" ? `stage-${row.seq}` : row.line.seq;
    },
    // Lets the first rows render before the element is measured (and in tests).
    initialRect: { width: 800, height: height ?? 560 },
  });

  const scrollToIndex = useCallback(
    (index: number, align: "start" | "end" | "center") => {
      if (index < 0 || index >= rows.length) return;
      programmatic.current = true;
      virtualizer.scrollToIndex(index, { align });
    },
    [rows.length, virtualizer],
  );

  const live = !complete;
  // Keep the newest line in view while following. A finished run opens at
  // its end once and then stays where the reader puts it.
  useEffect(() => {
    if (!follow || rows.length === 0) return;
    scrollToIndex(rows.length - 1, "end");
    if (!live) setFollow(false);
  }, [follow, live, rows.length, scrollToIndex]);

  // After earlier lines arrive above the gap, keep the line the reader was
  // looking at in place instead of letting the new lines push it down.
  const anchor = useRef<{ seq: number; gap: number } | null>(null);
  useEffect(() => {
    const a = anchor.current;
    if (!a) return;
    const gap = rows.find((r): r is Extract<Row, { kind: "gap" }> => r.kind === "gap");
    // The earlier page has not arrived yet.
    if (gap && gap.count === a.gap) return;
    anchor.current = null;
    const index = rows.findIndex((r) => r.kind === "line" && r.line.seq >= a.seq);
    if (index > 0) scrollToIndex(index - 1, "start");
  }, [rows, scrollToIndex]);
  const loadEarlier = () => {
    const gapIndex = rows.findIndex((r) => r.kind === "gap");
    const gap = rows[gapIndex];
    const after = rows.slice(gapIndex + 1).find((r) => r.kind === "line");
    anchor.current = gap?.kind === "gap" && after?.kind === "line" ? { seq: after.line.seq, gap: gap.count } : null;
    setFollow(false);
    onLoadEarlier?.();
  };

  // Jump to the line the page asked for, and stop following so it stays put.
  useEffect(() => {
    if (!scrollTarget) return;
    const index = rows.findIndex((r) => r.kind === "line" && r.line.seq >= scrollTarget.seq);
    if (index === -1) return;
    setFollow(false);
    // Show the stage's label above its first line too.
    scrollToIndex(rows[index - 1]?.kind === "stage" ? index - 1 : index, "start");
    // Only a new target should scroll, not new lines arriving.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scrollTarget?.key]);

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < ROW_HEIGHT * 2;
    setAtBottom(bottom);
    if (programmatic.current) {
      programmatic.current = false;
      return;
    }
    // The reader scrolled up: pause following until they come back down.
    if (!bottom && follow) setFollow(false);
  };

  const goToHit = (next: number) => {
    if (hits.length === 0) return;
    const i = ((next % hits.length) + hits.length) % hits.length;
    setHitIndex(i);
    setFollow(false);
    const seq = hits[i]!;
    scrollToIndex(
      rows.findIndex((r) => r.kind === "line" && r.line.seq === seq),
      "center",
    );
  };
  // Escape is the search field's own: it clears the query.
  const onSearchKey = (e: KeyboardEvent) => {
    if (e.key !== "Enter") return;
    e.preventDefault();
    goToHit(e.shiftKey ? hitIndex - 1 : hitIndex + 1);
  };

  const jumpToEnd = () => {
    scrollToIndex(rows.length - 1, "end");
    if (live) setFollow(true);
  };

  const streams: { key: StreamFilter; label: string }[] = [
    { key: "all", label: t("log.all") },
    { key: "stdout", label: "stdout" },
    { key: "stderr", label: "stderr" },
  ];

  const items = virtualizer.getVirtualItems();
  const totalSize = virtualizer.getTotalSize();
  // Short logs get a short box; long ones scroll inside the viewport height.
  const viewport = Math.min(totalSize + BODY_PADDING, height ?? DEFAULT_HEIGHT);

  // Filtering or new lines change the content height without a scroll event.
  useEffect(() => {
    const el = scrollRef.current;
    if (el) setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < ROW_HEIGHT * 2);
  }, [totalSize, viewport]);

  return (
    <section className={["log", className].filter(Boolean).join(" ")} aria-label={t("log.title")}>
      <div className="log-bar">
        <SearchField
          aria-label={t("log.search")}
          value={query}
          onChange={(value) => {
            setQuery(value);
            setHitIndex(0);
          }}
          onKeyDown={onSearchKey}
        >
          <SearchField.Group>
            <SearchField.SearchIcon />
            <SearchField.Input className="w-36 font-mono text-xs" />
            <SearchField.ClearButton />
          </SearchField.Group>
        </SearchField>
        {query.trim() ? (
          <span className="font-mono text-xs text-muted" aria-live="polite">
            {t("log.matches", { count: hits.length })}
          </span>
        ) : null}
        {/* The buttons keep their pressed state, so the set reads as a group of toggles. */}
        <div role="group" aria-label={t("log.streams")}>
          <ToggleButtonGroup
            size="sm"
            selectionMode="multiple"
            selectedKeys={[stream]}
            onSelectionChange={(keys) => {
              const next = [...keys].find((key) => key !== stream);
              if (next) setStream(next as StreamFilter);
            }}
          >
            {streams.map((s, i) => (
              <ToggleButton key={s.key} id={s.key}>
                {i > 0 ? <ToggleButtonGroup.Separator /> : null}
                {s.label}
              </ToggleButton>
            ))}
          </ToggleButtonGroup>
        </div>
        <span className="log-bar__gap" />
        {live ? (
          // Toggle brings its own label element, so the caption sits beside it instead of wrapping it.
          <span className="log-follow">
            <Toggle size="sm" checked={follow} onChange={setFollow} label={t("log.followLabel")} />
            <span aria-hidden="true">{t("log.follow")}</span>
          </span>
        ) : null}
        {onDownload && !expired ? (
          <IconButton label={t("log.download")} icon={ArrowDownToLine} onPress={onDownload} isPending={downloading} />
        ) : null}
      </div>

      {dropped > 0 ? (
        <div className="log-note log-note--warn" role="status">
          <TriangleExclamation {...ICON} />
          {t("log.dropped", { count: formatCount(dropped, locale) })}
        </div>
      ) : null}
      {reconnectAttempt > 0 ? (
        <div className="log-note log-note--run" role="status">
          <WaveLoader size="sm" />
          {t("log.reconnecting", { attempt: reconnectAttempt })}
        </div>
      ) : null}

      {expired ? (
        <div className="log-empty" role="status">
          <Clock {...ICON} />
          {t("log.expired")}
        </div>
      ) : failed ? (
        <div className="log-empty" role="alert">
          <CircleExclamation {...ICON} />
          {t("states.errorTitle")}
          {onRetry ? (
            <Button variant="ghost" size="sm" onPress={onRetry}>
              {t("common.retry")}
            </Button>
          ) : null}
        </div>
      ) : !loaded ? (
        <div className="log-empty">
          <WaveLoader size="sm" label={t("common.loading")} />
        </div>
      ) : lines.length === 0 ? (
        <div className="log-empty">
          {live ? <WaveLoader size="sm" /> : null}
          {live ? t("log.receiving") : t("log.empty")}
        </div>
      ) : (
        <div className="log-wrap">
          <div
            ref={scrollRef}
            className="log-body"
            style={{ ...GEOMETRY, height: viewport }}
            onScroll={onScroll}
            role="log"
            aria-live={follow && live ? "polite" : "off"}
            tabIndex={0}
          >
            <div className="log-rows" style={{ height: virtualizer.getTotalSize() }}>
              {items.map((item) => {
                const row = rows[item.index]!;
                const style = { transform: `translateY(${item.start}px)` };
                if (row.kind === "stage") {
                  return (
                    <div
                      key={item.key}
                      ref={virtualizer.measureElement}
                      data-index={item.index}
                      style={style}
                      className="ll ll--system ll--stage"
                      data-stage={row.stage}
                    >
                      <span className="n" aria-hidden="true" />
                      <span className="t" aria-hidden="true" />
                      <span className="m">▸ {t("log.stage", { name: stageNames.get(row.stage) ?? row.stage })}</span>
                    </div>
                  );
                }
                if (row.kind === "gap") {
                  return (
                    <div key={item.key} ref={virtualizer.measureElement} data-index={item.index} style={style} className="log-more">
                      <Button variant="ghost" size="sm" onPress={loadEarlier} isDisabled={!onLoadEarlier || loadingEarlier}>
                        {loadingEarlier ? <WaveLoader size="sm" /> : <ChevronUp {...ICON} />}
                        {t("log.earlier", { count: formatCount(row.count, locale) })}
                      </Button>
                    </div>
                  );
                }
                const l = row.line;
                const hit = hitSet.has(l.seq);
                return (
                  <div
                    key={item.key}
                    ref={virtualizer.measureElement}
                    data-index={item.index}
                    style={style}
                    className={["ll", `ll--${l.stream}`, hit && hits[hitIndex] === l.seq && "ll--hit"].filter(Boolean).join(" ")}
                    data-seq={l.seq}
                    data-stage={l.stage ?? undefined}
                  >
                    <span className="n">{l.seq}</span>
                    <span className="t">{formatLogTime(l.ts)}</span>
                    <span className="m">
                      {l.stream === "system" ? "▸ " : ""}
                      {hit ? highlight(l.text, query) : l.text}
                    </span>
                  </div>
                );
              })}
            </div>
          </div>
          {!atBottom ? (
            <div className="log-jump">
              <Button variant="secondary" size="sm" onPress={jumpToEnd}>
                <ArrowDown {...ICON} />
                {t("log.jumpToEnd")}
              </Button>
            </div>
          ) : null}
          {live && follow ? (
            <div className="log-empty log-empty--tail">
              <WaveLoader size="sm" label={t("log.receiving")} />
            </div>
          ) : null}
        </div>
      )}
    </section>
  );
}
