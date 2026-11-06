import { translate, type Locale } from "./i18n/i18n";

// Formatting helpers shared by web and mobile. All times are shown in the
// viewer's local time zone with a 24-hour clock (docs/zh-CN/design-system.md 4).

const pad = (n: number, width = 2) => String(n).padStart(width, "0");

type DateInput = Date | string | number;
const toDate = (value: DateInput) => (value instanceof Date ? value : new Date(value));

/**
 * Formats a duration the way the console shows it: "6.2s", "38s",
 * "1m 07s", "2h 05m". Under a minute it keeps one decimal unless `whole`
 * is set, which suits a live counter that ticks once a second.
 */
export function formatDuration(ms: number, options: { whole?: boolean } = {}): string {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  if (ms < 60_000) {
    if (options.whole || ms >= 10_000) {
      const s = options.whole ? Math.floor(ms / 1000) : Math.round(ms / 100) / 10;
      return Number.isInteger(s) ? `${s}s` : `${s.toFixed(1)}s`;
    }
    return `${(Math.round(ms / 100) / 10).toFixed(1)}s`;
  }
  const totalSeconds = Math.floor(ms / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours}h ${pad(minutes)}m`;
  return `${minutes}m ${pad(seconds)}s`;
}

/** Milliseconds between two timestamps; `end` defaults to now. */
export function elapsedMs(start: DateInput, end?: DateInput | null, now: DateInput = Date.now()): number {
  return toDate(end ?? now).getTime() - toDate(start).getTime();
}

/** "08:00" */
export function formatClock(value: DateInput): string {
  const d = toDate(value);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** "08:00:53" */
export function formatClockSeconds(value: DateInput): string {
  const d = toDate(value);
  return `${formatClock(d)}:${pad(d.getSeconds())}`;
}

/** "10:45:01.204", the log line timestamp. */
export function formatLogTime(value: DateInput): string {
  const d = toDate(value);
  return `${formatClockSeconds(d)}.${pad(d.getMilliseconds(), 3)}`;
}

const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
const DAY = 86_400_000;

/** A calendar date without time: "11月2日" / "Nov 2", with the year when it differs from now. */
export function formatDate(value: DateInput, locale: Locale, now: DateInput = Date.now()): string {
  const d = toDate(value);
  const sameYear = d.getFullYear() === toDate(now).getFullYear();
  if (locale === "zh-CN") {
    return sameYear
      ? `${d.getMonth() + 1}月${d.getDate()}日`
      : `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
  }
  return new Intl.DateTimeFormat("en", {
    month: "short",
    day: "numeric",
    ...(sameYear ? {} : { year: "numeric" }),
  }).format(d);
}

/** A full date for things like expiry: "2027/2/1" / "Feb 1, 2027". */
export function formatFullDate(value: DateInput, locale: Locale): string {
  const d = toDate(value);
  if (locale === "zh-CN") return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()}`;
  return new Intl.DateTimeFormat("en", { month: "short", day: "numeric", year: "numeric" }).format(d);
}

/**
 * A point in time relative to today: "今天 08:00", "昨天 08:00",
 * "明天 08:00", or "11月2日 08:00".
 */
export function formatDateTime(value: DateInput, locale: Locale, now: DateInput = Date.now()): string {
  const d = toDate(value);
  const days = Math.round((startOfDay(d) - startOfDay(toDate(now))) / DAY);
  const time = formatClock(d);
  if (days === 0) return translate(locale, "time.today", { time });
  if (days === -1) return translate(locale, "time.yesterday", { time });
  if (days === 1) return translate(locale, "time.tomorrow", { time });
  return `${formatDate(d, locale, now)} ${time}`;
}

/**
 * How long ago something happened, for "last used" columns: "刚刚",
 * "3 分钟前", "2 小时前", then dates.
 */
export function formatAgo(value: DateInput, locale: Locale, now: DateInput = Date.now()): string {
  const d = toDate(value);
  const diff = toDate(now).getTime() - d.getTime();
  if (diff < 60_000) return translate(locale, "time.justNow");
  if (diff < 3_600_000) return translate(locale, "time.minutesAgo", { count: Math.floor(diff / 60_000) });
  if (startOfDay(d) === startOfDay(toDate(now))) {
    return translate(locale, "time.hoursAgo", { count: Math.floor(diff / 3_600_000) });
  }
  if (startOfDay(toDate(now)) - startOfDay(d) === DAY) {
    return translate(locale, "time.yesterday", { time: formatClock(d) });
  }
  return formatDate(d, locale, now);
}

/** Whole days from now until value, rounded up; negative when past. */
export function daysUntil(value: DateInput, now: DateInput = Date.now()): number {
  return Math.ceil((toDate(value).getTime() - toDate(now).getTime()) / DAY);
}

/** "4.2 KB", "38.6 KB", "1.2 MB". Base 1024, one decimal above bytes. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(1)} ${units[unit]}`;
}

/** Groups digits: 12400 → "12,400". */
export function formatCount(n: number, locale: Locale): string {
  return new Intl.NumberFormat(locale === "zh-CN" ? "zh-CN" : "en").format(n);
}

/**
 * The short form of an ID shown in breadcrumbs. IDs are UUIDv7, whose
 * leading characters are a timestamp shared by runs started close together,
 * so this takes the random tail instead.
 */
export function shortId(id: string): string {
  return id.replace(/-/g, "").slice(-8);
}

/**
 * A point in time as a date with weekday and a 24-hour clock, read in a
 * given IANA time zone: { date: "11月3日 周二", time: "08:00" } or
 * { date: "Tue, Nov 3", time: "08:00" }. Cron triggers fire in their own
 * time zone, so their next fires are shown there. An unknown zone falls
 * back to the viewer's.
 */
export function formatInZone(value: DateInput, timeZone: string | undefined, locale: Locale): { date: string; time: string } {
  const d = toDate(value);
  const make = (tz: string | undefined) => {
    const parts = new Intl.DateTimeFormat(locale === "zh-CN" ? "zh-CN" : "en", {
      timeZone: tz,
      month: locale === "zh-CN" ? "numeric" : "short",
      day: "numeric",
      weekday: "short",
      hour: "2-digit",
      minute: "2-digit",
      hourCycle: "h23",
    }).formatToParts(d);
    const get = (type: Intl.DateTimeFormatPartTypes) => parts.find((p) => p.type === type)?.value ?? "";
    const time = `${get("hour").padStart(2, "0")}:${get("minute")}`;
    const date =
      locale === "zh-CN" ? `${get("month")}月${get("day")}日 ${get("weekday")}` : `${get("weekday")}, ${get("month")} ${get("day")}`;
    return { date, time };
  };
  try {
    return make(timeZone);
  } catch {
    return make(undefined);
  }
}

/** The viewer's IANA time zone, e.g. "Asia/Singapore". */
export function localTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone;
}
