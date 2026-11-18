import { Card, Chip, Table } from "@heroui/react";
import { formatCount, useI18n, type Locale, type RunResult } from "@lerryn/core";

import { CollapsiblePanel } from "./CollapsiblePanel";
import { ResultStatusPill } from "./RunStatusBadge";
import { WaveGlyph } from "./Wave";

type Primitive = string | number | boolean | null;
const isPrimitive = (v: unknown): v is Primitive =>
  v === null || ["string", "number", "boolean"].includes(typeof v);
const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

/** How the reported data is laid out. Exported for tests. */
export type DataView =
  | { kind: "none" }
  | { kind: "metrics"; items: [string, number][] }
  | { kind: "pairs"; items: [string, Primitive][] }
  | { kind: "table"; columns: string[]; rows: Primitive[][] }
  | { kind: "json"; text: string };

const MAX_METRICS = 6;
const MAX_TABLE_ROWS = 50;
const MAX_TABLE_COLUMNS = 4;

/**
 * Picks a plain layout for structured data a task reported. Only text and
 * numbers are rendered, never markup: a flat object of numbers becomes big
 * figures, a flat object becomes key-value rows, a short list of flat
 * records becomes a table, and anything else is shown as formatted JSON.
 */
export function viewOfData(data: unknown): DataView {
  if (data === null || data === undefined) return { kind: "none" };
  if (isRecord(data)) {
    const entries = Object.entries(data);
    if (entries.length === 0) return { kind: "none" };
    if (entries.length <= MAX_METRICS && entries.every(([, v]) => typeof v === "number" && Number.isFinite(v))) {
      return { kind: "metrics", items: entries as [string, number][] };
    }
    if (entries.every(([, v]) => isPrimitive(v))) return { kind: "pairs", items: entries as [string, Primitive][] };
  }
  if (Array.isArray(data) && data.length > 0 && data.length <= MAX_TABLE_ROWS && data.every(isRecord)) {
    const columns = [...new Set(data.flatMap((r) => Object.keys(r)))];
    if (columns.length <= MAX_TABLE_COLUMNS && data.every((r) => Object.values(r).every(isPrimitive))) {
      return { kind: "table", columns, rows: data.map((r) => columns.map((c) => (r[c] ?? null) as Primitive)) };
    }
  }
  return { kind: "json", text: JSON.stringify(data, null, 2) };
}

const show = (v: Primitive, locale: Locale) =>
  v === null ? "—" : typeof v === "number" ? formatCount(v, locale) : String(v);

/**
 * Reported rows as a table that scrolls sideways inside its own box. A column
 * whose first value is a number is right-aligned, like its cells. Shared with
 * the Agent table panel.
 */
export function DataTable({
  label,
  columns,
  rows,
  locale,
  hideHeader = false,
}: {
  label: string;
  columns: readonly string[];
  rows: readonly (readonly (Primitive | undefined)[])[];
  locale: Locale;
  /** A single unnamed column needs no visible header; screen readers still get it. */
  hideHeader?: boolean;
}) {
  return (
    <Table>
      <Table.ScrollContainer>
        <Table.Content aria-label={label}>
          <Table.Header className={hideHeader ? "sr-only" : undefined}>
            {columns.map((title, j) => (
              <Table.Column key={j} id={`c${j}`} isRowHeader={j === 0} className={typeof rows[0]?.[j] === "number" ? "text-end" : undefined}>
                {title}
              </Table.Column>
            ))}
          </Table.Header>
          <Table.Body>
            {rows.map((row, i) => (
              <Table.Row key={i} id={`r${i}`}>
                {columns.map((_, j) => {
                  const v = row[j] ?? null;
                  return (
                    <Table.Cell key={j} className={typeof v === "number" ? "num text-end tabular-nums" : undefined}>
                      {show(v, locale)}
                    </Table.Cell>
                  );
                })}
              </Table.Row>
            ))}
          </Table.Body>
        </Table.Content>
      </Table.ScrollContainer>
    </Table>
  );
}

export interface ResultPanelProps {
  /** What the task reported with `sbx result`; null renders nothing. */
  result: RunResult | null;
  collapsible?: boolean;
  className?: string;
}

/**
 * The business result a task reported, kept apart from the run status the
 * platform recorded (tech design 13.2): the panel is labelled as reported by
 * the task and carries its own status pill. Without a report it renders
 * nothing, so pages never invent a result.
 */
export function ResultPanel({ result, collapsible, className }: ResultPanelProps) {
  const { t, locale } = useI18n();
  if (!result) return null;
  const view = viewOfData(result.data);

  const title = t("result.title");
  const icon = <WaveGlyph size={16} />;
  const badges = (
    <span className="view-badges">
      <ResultStatusPill status={result.status} />
      <Chip size="sm" variant="soft">
        {t("result.reported")}
      </Chip>
    </span>
  );
  const body = (
    <>
      {result.summary ? <p className="view-summary">{result.summary}</p> : null}
      {view.kind === "metrics" ? (
        <div className="view-metrics">
          {view.items.map(([k, v]) => (
            <div className="view-metric" key={k}>
              <span className="view-metric__value">{formatCount(v, locale)}</span>
              <span className="view-metric__label">{k}</span>
            </div>
          ))}
        </div>
      ) : null}
      {view.kind === "pairs" ? (
        <dl className="kv">
          {view.items.map(([k, v]) => (
            <div className="contents" key={k}>
              <dt>{k}</dt>
              <dd className={typeof v === "number" ? "tabular-nums" : undefined}>{show(v, locale)}</dd>
            </div>
          ))}
        </dl>
      ) : null}
      {view.kind === "table" ? <DataTable label={t("result.data")} columns={view.columns} rows={view.rows} locale={locale} /> : null}
      {view.kind === "json" ? (
        // Long data scrolls inside the box; tabIndex lets keyboard users scroll it too.
        <pre className="view-pre" aria-label={t("result.data")} tabIndex={0}>
          {view.text}
        </pre>
      ) : null}
    </>
  );

  if (collapsible) {
    return <CollapsiblePanel title={title} icon={icon} aside={badges} className={className}>{body}</CollapsiblePanel>;
  }
  return (
    <Card role="region" aria-label={title} className={["tpl-card", className].filter(Boolean).join(" ")}>
      <div className="tpl-card__head view-head">
        <span className="view-title">
          {icon}
          {title}
        </span>
        {badges}
      </div>
      {body}
    </Card>
  );
}
