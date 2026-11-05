import type { Schemas } from "@lerryn/api";

export type LogLine = Schemas["LogLine"];
export type LogPage = Schemas["LogPage"];
export type LogStream = LogLine["stream"];

/**
 * What the console holds of a run's log. The API pages forward only: `tail`
 * reads the end, `cursor` continues after a page. So the viewer opens on the
 * tail, follows forward from there, and fills the part before it by reading
 * forward from line 1. That leaves at most one gap, between the lines read
 * from the start and the tail.
 */
export interface LogState {
  /** Lines held, sorted by seq, without duplicates. */
  lines: LogLine[];
  /** Continues after the newest line held; null before the first page. */
  tailCursor: string | null;
  /**
   * Continues after the newest line read from the start; null when nothing
   * has been read from the start yet (the next read starts at line 1).
   */
  headCursor: string | null;
  /** The run has ended and the newest line has been read. */
  complete: boolean;
  /** Lines the server dropped over the per-run log limit. */
  dropped: number;
  /** The first page has arrived. */
  loaded: boolean;
}

export const initialLogState: LogState = {
  lines: [],
  tailCursor: null,
  headCursor: null,
  complete: false,
  dropped: 0,
  loaded: false,
};

/** Lines kept in memory while following; older lines turn into the gap. */
export const MAX_HELD_LINES = 50_000;

/** Merges two seq-sorted arrays, keeping the first copy of each seq. */
export function mergeLines(a: readonly LogLine[], b: readonly LogLine[]): LogLine[] {
  const out: LogLine[] = [];
  let i = 0;
  let j = 0;
  let last = -Infinity;
  while (i < a.length || j < b.length) {
    const next = j >= b.length || (i < a.length && a[i]!.seq <= b[j]!.seq) ? a[i++]! : b[j++]!;
    if (next.seq !== last) {
      out.push(next);
      last = next.seq;
    }
  }
  return out;
}

/** Applies the first page, read with `tail`. */
export function applyTail(state: LogState, page: LogPage): LogState {
  return {
    ...state,
    lines: mergeLines(state.lines, page.lines),
    tailCursor: page.next_cursor,
    complete: page.complete,
    dropped: page.dropped_lines,
    loaded: true,
  };
}

/** Applies a page read after the tail while following the run. */
export function applyForward(state: LogState, page: LogPage, maxLines = MAX_HELD_LINES): LogState {
  let lines = mergeLines(state.lines, page.lines);
  let headCursor = state.headCursor;
  if (lines.length > maxLines) {
    // Drop the oldest lines. What was read from the start is gone, so the
    // next read of earlier lines starts again at line 1.
    lines = lines.slice(lines.length - maxLines);
    headCursor = null;
  }
  return {
    ...state,
    lines,
    headCursor,
    tailCursor: page.next_cursor,
    complete: page.complete,
    dropped: page.dropped_lines,
    loaded: true,
  };
}

/** Applies a page read forward from line 1 (or from headCursor). */
export function applyEarlier(state: LogState, page: LogPage): LogState {
  return {
    ...state,
    lines: mergeLines(state.lines, page.lines),
    headCursor: page.next_cursor,
    dropped: page.dropped_lines,
  };
}

export interface LogGap {
  /** Index in `lines` before which the missing lines belong. */
  index: number;
  /** How many lines are missing. */
  count: number;
}

/**
 * Finds the missing run of lines, if any. Seqs start at 1 and are
 * contiguous, so a jump marks the gap.
 */
export function findGap(lines: readonly LogLine[]): LogGap | null {
  if (lines.length === 0) return null;
  if (lines[0]!.seq > 1) return { index: 0, count: lines[0]!.seq - 1 };
  for (let i = 1; i < lines.length; i++) {
    const missing = lines[i]!.seq - lines[i - 1]!.seq - 1;
    if (missing > 0) return { index: i, count: missing };
  }
  return null;
}

/** Keeps the lines of one stream; system lines show with either. */
export function filterLines(lines: readonly LogLine[], stream: LogStream | "all"): LogLine[] {
  if (stream === "all") return lines as LogLine[];
  return lines.filter((l) => l.stream === stream || l.stream === "system");
}

/** Seqs of the lines whose text contains query, case-insensitively. */
export function searchLines(lines: readonly LogLine[], query: string): number[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];
  const hits: number[] = [];
  for (const l of lines) if (l.text.toLowerCase().includes(q)) hits.push(l.seq);
  return hits;
}

/** The seq of the first line written in a stage, if held. */
export function firstLineOfStage(lines: readonly LogLine[], stageId: string): number | null {
  for (const l of lines) if (l.stage === stageId) return l.seq;
  return null;
}

/** Renders lines as plain text for download: one line each, with time and stream. */
export function logToText(lines: readonly LogLine[]): string {
  return lines.map((l) => `${l.ts} ${l.stream} ${l.text}`).join("\n") + (lines.length ? "\n" : "");
}

// Lines pushed by streamRunLogs. The stream and the paged reads both merge by
// line number, so switching between them never loses or repeats a line.

/** Applies lines pushed by streamRunLogs. Same dedupe-by-seq merge as the paged reads. */
export function applyStreamLines(state: LogState, lines: readonly LogLine[], maxLines = MAX_HELD_LINES): LogState {
  if (lines.length === 0) return state;
  // A reconnect can repeat lines out of order; the merge needs them sorted.
  const sorted = [...lines].sort((a, b) => a.seq - b.seq);
  let merged = mergeLines(state.lines, sorted);
  let headCursor = state.headCursor;
  if (merged.length > maxLines) {
    merged = merged.slice(merged.length - maxLines);
    headCursor = null;
  }
  return { ...state, lines: merged, headCursor, loaded: true };
}

/** The stream's `end` event: the run ended and its last line has been sent. */
export function applyStreamEnd(state: LogState, end: { dropped_lines: number }): LogState {
  return { ...state, complete: true, dropped: Math.max(state.dropped, end.dropped_lines) };
}

/** The seq to resume the stream after (Last-Event-ID), or "" to start from the tail. */
export function lastSeq(state: LogState): string {
  const last = state.lines[state.lines.length - 1];
  return last ? String(last.seq) : "";
}

/** Parses a JSON event payload; null when it is not an object, so one bad event is skipped. */
export function parseEventData<T>(data: string): T | null {
  try {
    const value: unknown = JSON.parse(data);
    return value !== null && typeof value === "object" ? (value as T) : null;
  } catch {
    return null;
  }
}

/** Whether a pushed value looks like a LogLine; guards against a server that sends something else. */
export function isLogLine(value: unknown): value is LogLine {
  const v = value as Partial<LogLine> | null;
  return !!v && typeof v.seq === "number" && typeof v.text === "string" && typeof v.ts === "string";
}
