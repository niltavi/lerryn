import { formatCount, useI18n, type AgentPanel, type Locale, type PanelCell } from "@lerryn/core";
import type { ReactNode } from "react";

import { WaveGlyph } from "./Wave";

const show = (v: PanelCell, locale: Locale) =>
  v === null ? "—" : typeof v === "number" ? formatCount(v, locale) : typeof v === "boolean" ? String(v) : v;

const withUnit = (n: string, unit: string | null) => (unit ? `${n} ${unit}` : n);

function PanelCard({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className="wl-card panel" aria-label={title}>
      <div className="panel-head">
        <span className="panel-title">
          <WaveGlyph size={18} className="wl-wave" />
          {title}
        </span>
        {aside}
      </div>
      {children}
    </section>
  );
}

export interface AgentPanelsProps {
  panels: readonly AgentPanel[];
  /** A short note in each panel's corner, such as when the data was reported. */
  aside?: ReactNode;
  /** Called for a bar that stands for a run, so the page can link it. */
  barHref?: (runId: string) => string;
}

/**
 * The Agent panels a task declared, drawn from what its run reported:
 * metric, bars, breakdown, table and text. Text is shown as text and never
 * parsed as Markdown or HTML (tech design 5.3). Renders nothing without
 * panels, so a run that reported no matching data shows no panel area.
 */
export function AgentPanels({ panels, aside, barHref }: AgentPanelsProps) {
  const { t, locale } = useI18n();
  if (panels.length === 0) return null;
  const sep = locale === "zh-CN" ? "，" : ", ";
  return (
    <div className="panels">
      {panels.map((p) => {
        switch (p.type) {
          case "metric":
            return (
              <PanelCard key={p.id} title={p.title} aside={aside}>
                <div className="stat">
                  <span className="stat-n stat-n--md">
                    {formatCount(p.value, locale)}
                    {p.unit ? <small>{p.unit}</small> : null}
                  </span>
                  {p.label ? <span className="stat-l">{p.label}</span> : null}
                </div>
              </PanelCard>
            );
          case "bars": {
            const max = Math.max(...p.items.map((i) => i.value), 0) || 1;
            const peak = p.items.findIndex((i) => i.value === max);
            const label = `${p.title}${t("panels.colon")}${p.items.map((i) => `${i.label} ${formatCount(i.value, locale)}`).join(sep)}`;
            return (
              <PanelCard key={p.id} title={p.title} aside={aside}>
                <div className="bars" role="img" aria-label={label}>
                  {p.items.map((item, i) => {
                    const last = i === p.items.length - 1;
                    const style = { height: `${Math.max(2, (item.value / max) * 100)}%` };
                    const tip = `${item.label} · ${withUnit(formatCount(item.value, locale), p.unit)}`;
                    const inner = last || i === peak ? <b>{formatCount(item.value, locale)}</b> : null;
                    return item.runId && barHref ? (
                      <a key={i} className={last ? "bar bar--last" : "bar"} style={style} title={tip} href={barHref(item.runId)} aria-hidden="true" tabIndex={-1}>
                        {inner}
                      </a>
                    ) : (
                      <span key={i} className={last ? "bar bar--last" : "bar"} style={style} title={tip}>
                        {inner}
                      </span>
                    );
                  })}
                </div>
                <div className="bars-axis" aria-hidden="true">
                  {p.items.map((item, i) => (
                    <span key={i}>{item.label}</span>
                  ))}
                </div>
              </PanelCard>
            );
          }
          case "breakdown": {
            const max = Math.max(...p.items.map((i) => i.value), 0) || 1;
            const label = `${p.title}${t("panels.colon")}${p.items.map((i) => `${i.label} ${formatCount(i.value, locale)}`).join(sep)}`;
            return (
              <PanelCard key={p.id} title={p.title} aside={aside}>
                <div className="hbars" role="img" aria-label={label}>
                  {p.items.map((item, i) => (
                    <div className="hbar" key={i}>
                      <span title={item.label}>{item.label}</span>
                      <span className="hbar-track">
                        <span style={{ width: `${Math.max(0, (item.value / max) * 100)}%` }} />
                      </span>
                      <span className="v">{withUnit(formatCount(item.value, locale), p.unit)}</span>
                    </div>
                  ))}
                </div>
              </PanelCard>
            );
          }
          case "table":
            return (
              <PanelCard key={p.id} title={p.title} aside={aside}>
                <div style={{ overflowX: "auto" }}>
                  <table className="ptable">
                    {p.columns.length > 1 ? (
                      <thead>
                        <tr>
                          {p.columns.map((c, j) => (
                            <th key={c.key} scope="col" className={typeof p.rows[0]?.[j] === "number" ? "caption r" : "caption"}>
                              {c.title}
                            </th>
                          ))}
                        </tr>
                      </thead>
                    ) : null}
                    <tbody>
                      {p.rows.map((row, i) => (
                        <tr key={i}>
                          {row.map((v, j) => (
                            <td key={j} className={typeof v === "number" ? "r num" : undefined}>
                              {show(v, locale)}
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </PanelCard>
            );
          case "text":
            return (
              <PanelCard key={p.id} title={p.title} aside={aside}>
                <p className="panel-text">{p.text}</p>
              </PanelCard>
            );
        }
      })}
    </div>
  );
}
