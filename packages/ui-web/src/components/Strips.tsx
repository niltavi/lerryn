import { formatClock, formatCount, type RidgeBar, type StripBucket } from "@lerryn/core";
import type { ReactNode } from "react";

export interface DayStripProps {
  buckets: readonly StripBucket[];
  /** Start of the day shown, local midnight. */
  dayStart: number;
  now: number;
  /** Text of one bucket's tooltip and accessible name. */
  describe: (bucket: StripBucket) => string;
  /** Renders a bucket that has a run as a link; others are plain shapes. */
  renderLink: (bucket: StripBucket, props: { className: string; style: { height: string }; children: ReactNode; "aria-label": string }) => ReactNode;
  label: string;
  legend: { run: string; bad: string; warn: string; active: string; plan: string };
}

const MAX_RUNS_DRAWN = 8;

/**
 * Today's corrugation strip: 48 half-hour ridges whose height is the number
 * of runs, coloured only where something failed, broke off or is running;
 * planned fires are dashed (prototype Main.dc.html).
 */
export function DayStrip({ buckets, dayStart, now, describe, renderLink, label, legend }: DayStripProps) {
  const pct = Math.min(100, Math.max(0, ((now - dayStart) / 86_400_000) * 100));
  return (
    <section className="strip" aria-label={label}>
      <div className="strip-plot">
        {buckets.map((b) => {
          const n = b.runs || b.planned;
          const h = n === 0 ? 4 : 14 + Math.min(n, MAX_RUNS_DRAWN) * 12;
          const cls = b.tone === "empty" ? "ridge ridge--empty" : b.tone === "ok" || b.tone === "idle" ? "ridge" : `ridge ridge--${b.tone}`;
          const text = describe(b);
          const style = { height: `${h}px` };
          const tip = <span className="pop" aria-hidden="true">{text}</span>;
          return b.runId ? (
            <span key={b.start} style={{ display: "contents" }}>
              {renderLink(b, { className: cls, style, children: tip, "aria-label": text.replace(/\n/g, " · ") })}
            </span>
          ) : (
            <span key={b.start} className={cls} style={style} aria-hidden={b.tone === "empty" ? true : undefined} title={b.tone === "empty" ? undefined : text}>
              {b.tone === "empty" ? null : tip}
            </span>
          );
        })}
        <span className="strip-now" style={{ left: `${pct}%` }} aria-hidden="true">
          <span>{formatClock(now)}</span>
        </span>
      </div>
      <div className="strip-axis" aria-hidden="true">
        <span>00:00</span>
        <span>06:00</span>
        <span>12:00</span>
        <span>18:00</span>
        <span>24:00</span>
      </div>
      <div className="legend" aria-hidden="true">
        <span>
          <i />
          {legend.run}
        </span>
        <span>
          <i className="bad" />
          {legend.bad}
        </span>
        <span>
          <i className="warn" />
          {legend.warn}
        </span>
        <span>
          <i className="run" />
          {legend.active}
        </span>
        <span>
          <i className="plan" />
          {legend.plan}
        </span>
      </div>
    </section>
  );
}

export interface RunsStripProps {
  bars: readonly RidgeBar[];
  describe: (bar: RidgeBar) => string;
  renderLink: (bar: RidgeBar, props: { className: string; style: { height: string }; children: ReactNode; "aria-label": string }) => ReactNode;
  label: string;
}

/** A task's recent runs, one ridge per run, as tall as it took. */
export function RunsStrip({ bars, describe, renderLink, label }: RunsStripProps) {
  return (
    <div className="runs-strip" role="list" aria-label={label}>
      {bars.map((bar) => {
        const cls = bar.tone === "ok" || bar.tone === "idle" || bar.tone === "empty" ? "ridge" : `ridge ridge--${bar.tone}`;
        const text = describe(bar);
        return (
          <span key={bar.key} role="listitem" style={{ display: "contents" }}>
            {renderLink(bar, {
              className: cls,
              style: { height: `${Math.round(bar.height * 100)}%` },
              children: (
                <span className="pop" aria-hidden="true">
                  {text}
                </span>
              ),
              "aria-label": text.replace(/\n/g, " · "),
            })}
          </span>
        );
      })}
    </div>
  );
}

/** "3" or "8+" for counts drawn on a strip. */
export function capCount(n: number, locale: "zh-CN" | "en", cap = 99): string {
  return n > cap ? `${cap}+` : formatCount(n, locale);
}
