import { Card, Label, Meter } from "@heroui/react";
import { formatCount, useI18n, type AgentPanel } from "@lerryn/core";
import type { ReactNode } from "react";

import { DataTable } from "./ResultPanel";
import { WaveGlyph } from "./Wave";

const withUnit = (n: string, unit: string | null) => (unit ? `${n} ${unit}` : n);

function PanelCard({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <Card role="region" aria-label={title} className="tpl-card">
      <div className="tpl-card__head view-head">
        <span className="view-title">
          <WaveGlyph size={16} />
          {title}
        </span>
        {aside}
      </div>
      {children}
    </Card>
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
    <div className="view-panels">
      {panels.map((p) => {
        switch (p.type) {
          case "metric":
            return (
              <PanelCard key={p.id} title={p.title} aside={aside}>
                <div className="view-metric">
                  <span className="view-metric__value">
                    {formatCount(p.value, locale)}
                    {p.unit ? <small>{p.unit}</small> : null}
                  </span>
                  {p.label ? <span className="view-metric__label">{p.label}</span> : null}
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
                      <a key={i} className="bar" style={style} title={tip} href={barHref(item.runId)} aria-hidden="true" tabIndex={-1}>
                        {inner}
                      </a>
                    ) : (
                      <span key={i} className="bar" style={style} title={tip}>
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
                {/* One image for assistive technology: its name already lists every share. */}
                <div className="view-shares" role="img" aria-label={label}>
                  {p.items.map((item, i) => (
                    <Meter
                      key={i}
                      color="accent"
                      size="sm"
                      value={Math.max(0, item.value)}
                      maxValue={max}
                      valueLabel={withUnit(formatCount(item.value, locale), p.unit)}
                    >
                      <Label>{item.label}</Label>
                      <Meter.Output />
                      <Meter.Track>
                        <Meter.Fill />
                      </Meter.Track>
                    </Meter>
                  ))}
                </div>
              </PanelCard>
            );
          }
          case "table":
            return (
              <PanelCard key={p.id} title={p.title} aside={aside}>
                <DataTable
                  label={p.title}
                  columns={p.columns.map((c) => c.title)}
                  rows={p.rows}
                  locale={locale}
                  hideHeader={p.columns.length <= 1}
                />
              </PanelCard>
            );
          case "text":
            return (
              <PanelCard key={p.id} title={p.title} aside={aside}>
                <p className="view-text">{p.text}</p>
              </PanelCard>
            );
        }
      })}
    </div>
  );
}
