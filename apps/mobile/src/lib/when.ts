import { formatClock, formatDate, formatDateTime, type Locale } from '@lerryn/core';

/**
 * A compact time for list rows: "08:00" today, "明天 08:00" tomorrow or
 * yesterday, otherwise the date alone ("11月9日").
 */
export function shortWhen(value: string | number, locale: Locale, now: number): string {
  const d = new Date(value);
  const day = (t: number) => {
    const x = new Date(t);
    return new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  };
  const days = Math.round((day(d.getTime()) - day(now)) / 86_400_000);
  if (days === 0) return formatClock(d);
  if (Math.abs(days) === 1) return formatDateTime(d, locale, now);
  return formatDate(d, locale, now);
}
