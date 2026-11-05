import { describe, expect, it } from "vitest";

import {
  applyEarlier,
  applyForward,
  applyTail,
  filterLines,
  findGap,
  firstLineOfStage,
  initialLogState,
  logToText,
  mergeLines,
  searchLines,
  type LogLine,
  type LogPage,
} from "./model";

const line = (seq: number, over: Partial<LogLine> = {}): LogLine => ({
  seq,
  ts: "2026-11-02T02:45:01.204Z",
  stream: "stdout",
  stage: null,
  text: `line ${seq}`,
  ...over,
});
const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => line(from + i));
const page = (lines: LogLine[], over: Partial<LogPage> = {}): LogPage => ({
  lines,
  next_cursor: `c${lines.at(-1)?.seq ?? 0}`,
  complete: false,
  dropped_lines: 0,
  ...over,
});

describe("mergeLines", () => {
  it("merges sorted runs and drops duplicates", () => {
    const merged = mergeLines([line(1), line(3), line(4)], [line(2), line(3), line(5)]);
    expect(merged.map((l) => l.seq)).toEqual([1, 2, 3, 4, 5]);
  });
});

describe("log paging", () => {
  it("opens on the tail and reports the gap before it", () => {
    const s = applyTail(initialLogState, page(range(501, 1000)));
    expect(s.loaded).toBe(true);
    expect(s.tailCursor).toBe("c1000");
    expect(findGap(s.lines)).toEqual({ index: 0, count: 500 });
  });

  it("fills the gap from line 1 and closes it", () => {
    let s = applyTail(initialLogState, page(range(1501, 2000)));
    s = applyEarlier(s, page(range(1, 1000)));
    expect(s.headCursor).toBe("c1000");
    expect(findGap(s.lines)).toEqual({ index: 1000, count: 500 });
    // The next page overlaps the tail; the overlap is not duplicated.
    s = applyEarlier(s, page(range(1001, 2000)));
    expect(findGap(s.lines)).toBeNull();
    expect(s.lines).toHaveLength(2000);
  });

  it("follows forward and records completion and drops", () => {
    let s = applyTail(initialLogState, page(range(1, 3)));
    s = applyForward(s, page(range(4, 6), { complete: true, dropped_lines: 7 }));
    expect(s.lines.map((l) => l.seq)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(s.complete).toBe(true);
    expect(s.dropped).toBe(7);
    expect(s.tailCursor).toBe("c6");
  });

  it("keeps the cursor when an empty page arrives", () => {
    let s = applyTail(initialLogState, page(range(1, 3)));
    s = applyForward(s, { lines: [], next_cursor: "c3", complete: false, dropped_lines: 0 });
    expect(s.tailCursor).toBe("c3");
    expect(s.lines).toHaveLength(3);
  });

  it("drops the oldest lines past the cap and restarts reading from line 1", () => {
    let s = applyTail(initialLogState, page(range(1, 6)));
    s = applyEarlier(s, page([]));
    s = applyForward(s, page(range(7, 10)), 5);
    expect(s.lines.map((l) => l.seq)).toEqual([6, 7, 8, 9, 10]);
    expect(s.headCursor).toBeNull();
    expect(findGap(s.lines)).toEqual({ index: 0, count: 5 });
  });
});

describe("filters", () => {
  const lines = [
    line(1, { stream: "system", text: "Preparing" }),
    line(2, { stream: "stdout", text: "fetched 37 leads", stage: "fetch" }),
    line(3, { stream: "stderr", text: "HTTP 429 Too Many Requests", stage: "llm" }),
    line(4, { stream: "stderr", text: "http 429 again", stage: "llm" }),
  ];

  it("keeps system lines with either stream", () => {
    expect(filterLines(lines, "stderr").map((l) => l.seq)).toEqual([1, 3, 4]);
    expect(filterLines(lines, "stdout").map((l) => l.seq)).toEqual([1, 2]);
    expect(filterLines(lines, "all")).toHaveLength(4);
  });

  it("searches case-insensitively", () => {
    expect(searchLines(lines, "HTTP 429")).toEqual([3, 4]);
    expect(searchLines(lines, "  ")).toEqual([]);
  });

  it("finds the first line of a stage", () => {
    expect(firstLineOfStage(lines, "llm")).toBe(3);
    expect(firstLineOfStage(lines, "save")).toBeNull();
  });

  it("renders plain text for download", () => {
    expect(logToText(lines.slice(0, 1))).toBe("2026-11-02T02:45:01.204Z system Preparing\n");
    expect(logToText([])).toBe("");
  });
});
