import { stageDependencies, type RunStage } from "@lerryn/core";

/** Node and gap sizes in CSS pixels; console.css sizes the node contents to fit. */
export const STAGE_NODE = { width: 168, height: 56, colGap: 36, rowGap: 12 } as const;

export interface StageGraphNode {
  id: string;
  /** Position of the stage in the run's stage list. */
  index: number;
  /** Column, counted from the stages that wait for nothing. */
  layer: number;
  /** Row within the column. */
  row: number;
  x: number;
  y: number;
}

export interface StageGraphEdge {
  from: string;
  to: string;
  /** SVG path data, from the right side of `from` to the left side of `to`. */
  path: string;
}

export interface StageGraphLayout {
  /** In reading order: column by column, top to bottom. */
  nodes: StageGraphNode[];
  edges: StageGraphEdge[];
  width: number;
  height: number;
}

type GraphStage = Pick<RunStage, "id" | "after" | "declared">;

/**
 * Lays out a run's stages as a layered graph, left to right: each stage sits
 * one column after the furthest stage it waits for, and stages within a
 * column are ordered by where their dependencies sit, so edges cross less.
 *
 * Validation rejects dependency cycles, but the layout does not rely on it:
 * an edge that would close a cycle is left out instead of looping forever.
 * Stages the task reported without declaring them have no dependencies;
 * they get a column of their own after the declared ones, so they do not
 * read as the start of the run.
 */
export function layoutStageGraph(stages: readonly GraphStage[]): StageGraphLayout {
  const deps = stageDependencies(stages);
  const index = new Map(stages.map((s, i) => [s.id, i]));
  const layer = new Map<string, number>();
  const visiting = new Set<string>();
  const kept = new Map<string, string[]>();

  const visit = (id: string): number => {
    const known = layer.get(id);
    if (known !== undefined) return known;
    if (visiting.has(id)) return -1;
    visiting.add(id);
    let l = 0;
    const keep: string[] = [];
    for (const d of deps.get(id) ?? []) {
      const dl = visit(d);
      if (dl < 0) continue; // d is still being visited: this edge closes a cycle.
      keep.push(d);
      l = Math.max(l, dl + 1);
    }
    visiting.delete(id);
    kept.set(id, keep);
    layer.set(id, l);
    return l;
  };
  const declared = stages.filter((s) => s.declared !== false);
  for (const s of declared) visit(s.id);
  const lastDeclared = declared.reduce((m, s) => Math.max(m, layer.get(s.id) ?? 0), -1);
  for (const s of stages) {
    if (s.declared === false) {
      layer.set(s.id, lastDeclared + 1);
      kept.set(s.id, []);
    }
  }

  const columns: string[][] = [];
  for (const s of stages) {
    const l = layer.get(s.id) ?? 0;
    (columns[l] ??= []).push(s.id);
  }
  const row = new Map<string, number>();
  columns.forEach((col, l) => {
    if (l > 0) {
      // Barycentre ordering: a stage goes near the average row of what it waits for.
      const centre = (id: string) => {
        const from = kept.get(id) ?? [];
        return from.length ? from.reduce((sum, d) => sum + (row.get(d) ?? 0), 0) / from.length : Number.POSITIVE_INFINITY;
      };
      col.sort((a, b) => centre(a) - centre(b) || index.get(a)! - index.get(b)!);
    }
    col.forEach((id, r) => row.set(id, r));
  });

  const { width: w, height: h, colGap, rowGap } = STAGE_NODE;
  const rows = Math.max(0, ...columns.map((c) => c?.length ?? 0));
  const nodes: StageGraphNode[] = [];
  const at = new Map<string, StageGraphNode>();
  columns.forEach((col, l) => {
    // Shorter columns are centred against the tallest one.
    const offset = ((rows - col.length) * (h + rowGap)) / 2;
    col.forEach((id, r) => {
      const node = { id, index: index.get(id)!, layer: l, row: r, x: l * (w + colGap), y: offset + r * (h + rowGap) };
      nodes.push(node);
      at.set(id, node);
    });
  });

  const edges: StageGraphEdge[] = [];
  for (const node of nodes) {
    for (const d of kept.get(node.id) ?? []) {
      const a = at.get(d)!;
      const x1 = a.x + w;
      const y1 = a.y + h / 2;
      const x2 = node.x;
      const y2 = node.y + h / 2;
      const mx = (x1 + x2) / 2;
      edges.push({ from: d, to: node.id, path: `M${x1} ${y1}C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}` });
    }
  }

  const width = columns.length ? columns.length * w + (columns.length - 1) * colGap : 0;
  const height = rows ? rows * h + (rows - 1) * rowGap : 0;
  return { nodes, edges, width, height };
}
