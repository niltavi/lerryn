import { formatCount, useI18n, type Locale, type RunResult } from "@lerryn/core";

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

export interface ResultPanelProps {
  /** What the task reported with `sbx result`; null renders nothing. */
  result: RunResult | null;
  className?: string;
}

/**
 * The business result a task reported, kept apart from the run status the
 * platform recorded (tech design 13.2): the panel is labelled as reported by
 * the task and carries its own status pill. Without a report it renders
 * nothing, so pages never invent a result.
 */
export function ResultPanel({ result, className }: ResultPanelProps) {
  const { t, locale } = useI18n();
  if (!result) return null;
  const view = viewOfData(result.data);

  return (
    <section className={["wl-card panel", className].filter(Boolean).join(" ")} aria-label={t("result.title")}>
      <div className="panel-head">
        <span className="panel-title">
          <WaveGlyph size={18} />
          {t("result.title")}
        </span>
        <span className="row" style={{ gap: "var(--space-2)" }}>
          <ResultStatusPill status={result.status} />
          <span className="tag">{t("result.reported")}</span>
        </span>
      </div>
      {result.summary ? <p className="panel-summary">{result.summary}</p> : null}
      {view.kind === "metrics" ? (
        <div className="hero" style={{ gap: "12px 36px" }}>
          {view.items.map(([k, v]) => (
            <div className="stat" key={k}>
              <span className="stat-n stat-n--md">{formatCount(v, locale)}</span>
              <span className="stat-l">{k}</span>
            </div>
          ))}
        </div>
      ) : null}
      {view.kind === "pairs" ? (
        <table className="ptable">
          <tbody>
            {view.items.map(([k, v]) => (
              <tr key={k}>
                <th scope="row">{k}</th>
                <td className={typeof v === "number" ? "r num" : undefined}>{show(v, locale)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
      {view.kind === "table" ? (
        <div style={{ overflowX: "auto" }}>
          <table className="ptable">
            <thead>
              <tr>
                {view.columns.map((c) => (
                  <th key={c} scope="col" className="caption">
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {view.rows.map((row, i) => (
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
      ) : null}
      {view.kind === "json" ? (
        // Long data scrolls inside the box; tabIndex lets keyboard users scroll it too.
        <pre className="data-pre" aria-label={t("result.data")} tabIndex={0}>
          {view.text}
        </pre>
      ) : null}
    </section>
  );
}
