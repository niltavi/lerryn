import { Tooltip } from "@heroui/react";
import { Focusable } from "@heroui/react/rac";
import { formatClock, formatCount, type RidgeBar, type StripBucket } from "@lerryn/core";
import { isValidElement, type ComponentProps, type ReactNode } from "react";

type RidgeLinkProps = { className: string; style: { height: string }; children: ReactNode; "aria-label": string };

export interface DayStripProps {
  buckets: readonly StripBucket[];
  /** Start of the day shown, local midnight. */
  dayStart: number;
  now: number;
  /** Text of one bucket's tooltip and accessible name. */
  describe: (bucket: StripBucket) => string;
  /** Renders a bucket that has a run as a link; others are plain shapes. */
  renderLink: (bucket: StripBucket, props: RidgeLinkProps) => ReactNode;
  label: string;
  legend: { run: string; bad: string; warn: string; active: string; plan: string };
}

const MAX_RUNS_DRAWN = 8;

/**
 * A ridge that links to a run, with what it stands for in a tooltip on hover
 * and focus. The page supplies the link, so the tooltip attaches to whatever
 * element it rendered; that element has to pass props and its ref through to
 * the anchor, as router links do.
 */
function RidgeLink({ link, text }: { link: ReactNode; text: string }) {
  if (!isValidElement(link)) return link;
  return (
    <Tooltip delay={0} closeDelay={0}>
      <Focusable>{link as ComponentProps<typeof Focusable>["children"]}</Focusable>
      <Tooltip.Content className="whitespace-pre-line">{text}</Tooltip.Content>
    </Tooltip>
  );
}

/**
 * Today's corrugation strip: 48 half-hour ridges whose height is the number
 * of runs, coloured only where something failed, broke off or is running;
 * planned fires are dashed (prototype Main.dc.html).
 */
export function DayStrip({ buckets, dayStart, now, describe, renderLink, label, legend }: DayStripProps) {
  const pct = Math.min(100, Math.max(0, ((now - dayStart) / 86_400_000) * 100));
  const keys: [tone: string, text: string][] = [
    ["ok", legend.run],
    ["bad", legend.bad],
    ["warn", legend.warn],
    ["run", legend.active],
    ["plan", legend.plan],
  ];
  return (
    <section className="strip" aria-label={label}>
      <div className="strip-plot">
        {buckets.map((b) => {
          const n = b.runs || b.planned;
          const h = n === 0 ? 4 : 14 + Math.min(n, MAX_RUNS_DRAWN) * 12;
          const cls = b.tone === "empty" ? "ridge ridge--empty" : b.tone === "ok" || b.tone === "idle" ? "ridge" : `ridge ridge--${b.tone}`;
          const text = describe(b);
          const style = { height: `${h}px` };
          return b.runId ? (
            <span key={b.start} className="contents">
              <RidgeLink text={text} link={renderLink(b, { className: cls, style, children: null, "aria-label": text.replace(/\n/g, " · ") })} />
            </span>
          ) : (
            <span key={b.start} className={cls} style={style} aria-hidden={b.tone === "empty" ? true : undefined} title={b.tone === "empty" ? undefined : text} />
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
      <div className="tpl-legend strip-legend" aria-hidden="true">
        {keys.map(([tone, text]) => (
          <span key={tone}>
            <i className="strip-key" data-tone={tone} />
            {text}
          </span>
        ))}
      </div>
    </section>
  );
}

export interface RunsStripProps {
  bars: readonly RidgeBar[];
  describe: (bar: RidgeBar) => string;
  renderLink: (bar: RidgeBar, props: RidgeLinkProps) => ReactNode;
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
          <span key={bar.key} role="listitem" className="contents">
            <RidgeLink
              text={text}
              link={renderLink(bar, {
                className: cls,
                style: { height: `${Math.round(bar.height * 100)}%` },
                children: null,
                "aria-label": text.replace(/\n/g, " · "),
              })}
            />
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
