import { describe, expect, it } from "vitest";

import {
  daysUntil,
  formatAgo,
  formatBytes,
  formatClock,
  formatDate,
  formatDateTime,
  formatDuration,
  formatFullDate,
  formatLogTime,
  shortId,
} from "./format";

// Local-time dates, so the expectations hold in any time zone.
const at = (y: number, mo: number, d: number, h = 0, mi = 0, s = 0, ms = 0) =>
  new Date(y, mo - 1, d, h, mi, s, ms);
const NOW = at(2026, 11, 2, 10, 46);

describe("formatDuration", () => {
  it.each([
    [0, {}, "0.0s"],
    [6_200, {}, "6.2s"],
    [1_100, {}, "1.1s"],
    [12_400, {}, "12.4s"],
    [38_000, {}, "38s"],
    [52_340, {}, "52.3s"],
    [38_900, { whole: true }, "38s"],
    [5_900, { whole: true }, "5s"],
    [67_000, {}, "1m 07s"],
    [46 * 60_000 + 12_000, {}, "46m 12s"],
    [2 * 3_600_000 + 5 * 60_000, {}, "2h 05m"],
    [-1, {}, "—"],
    [Number.NaN, {}, "—"],
  ])("%d ms %o → %s", (ms, opts, want) => {
    expect(formatDuration(ms, opts)).toBe(want);
  });
});

describe("clock formats", () => {
  it("uses a 24-hour clock with zero padding", () => {
    expect(formatClock(at(2026, 11, 2, 8, 5))).toBe("08:05");
    expect(formatClock(at(2026, 11, 2, 18, 0))).toBe("18:00");
  });
  it("formats log timestamps with milliseconds", () => {
    expect(formatLogTime(at(2026, 11, 2, 10, 45, 1, 204))).toBe("10:45:01.204");
    expect(formatLogTime(at(2026, 11, 2, 9, 5, 7, 3))).toBe("09:05:07.003");
  });
});

describe("formatDate", () => {
  it("drops the year within the current year", () => {
    expect(formatDate(at(2026, 11, 9), "zh-CN", NOW)).toBe("11月9日");
    expect(formatDate(at(2026, 11, 9), "en", NOW)).toBe("Nov 9");
  });
  it("keeps the year otherwise", () => {
    expect(formatDate(at(2025, 3, 1), "zh-CN", NOW)).toBe("2025/3/1");
    expect(formatDate(at(2025, 3, 1), "en", NOW)).toBe("Mar 1, 2025");
  });
  it("always writes the year for full dates", () => {
    expect(formatFullDate(at(2027, 2, 1), "zh-CN")).toBe("2027/2/1");
    expect(formatFullDate(at(2027, 2, 1), "en")).toBe("Feb 1, 2027");
  });
});

describe("formatDateTime", () => {
  it.each([
    [at(2026, 11, 2, 8, 0), "zh-CN", "今天 08:00"],
    [at(2026, 11, 1, 23, 59), "zh-CN", "昨天 23:59"],
    [at(2026, 11, 3, 8, 0), "zh-CN", "明天 08:00"],
    [at(2026, 10, 30, 9, 0), "zh-CN", "10月30日 09:00"],
    [at(2026, 11, 2, 8, 0), "en", "Today 08:00"],
    [at(2026, 11, 1, 8, 0), "en", "Yesterday 08:00"],
    [at(2026, 10, 30, 9, 0), "en", "Oct 30 09:00"],
  ] as const)("%s in %s", (value, locale, want) => {
    expect(formatDateTime(value, locale, NOW)).toBe(want);
  });
});

describe("formatAgo", () => {
  it.each([
    [at(2026, 11, 2, 10, 45, 30), "zh-CN", "刚刚"],
    [at(2026, 11, 2, 10, 43), "zh-CN", "3 分钟前"],
    [at(2026, 11, 2, 7, 40), "zh-CN", "3 小时前"],
    [at(2026, 11, 1, 22, 10), "zh-CN", "昨天 22:10"],
    [at(2026, 10, 30, 22, 10), "zh-CN", "10月30日"],
    [at(2026, 11, 2, 10, 43), "en", "3 min ago"],
  ] as const)("%s in %s", (value, locale, want) => {
    expect(formatAgo(value, locale, NOW)).toBe(want);
  });
});

describe("misc", () => {
  it("counts days until a date, rounding up", () => {
    expect(daysUntil(at(2026, 11, 30, 10, 46), NOW)).toBe(28);
    expect(daysUntil(at(2026, 11, 2, 12, 0), NOW)).toBe(1);
    expect(daysUntil(at(2026, 11, 1, 10, 46), NOW)).toBe(-1);
  });
  it("formats byte sizes", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(4300)).toBe("4.2 KB");
    expect(formatBytes(39_526)).toBe("38.6 KB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MB");
  });
  it("shortens UUIDv7 IDs to their random tail", () => {
    expect(shortId("0192f3a1-7c2b-7d4e-9a1b-3c4d5e6f7a8b")).toBe("5e6f7a8b");
  });
});
