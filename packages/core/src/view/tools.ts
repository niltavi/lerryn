// View models for the project tool registry: the tools a task's manifest
// names, and the registry grouped by project. Pure functions.
import type { Schemas } from "@lerryn/api";

type Tool = Schemas["Tool"];
type ProjectRef = Pick<Schemas["Project"], "id" | "name" | "is_personal">;

export interface TaskToolView {
  name: string;
  /** The registered tool, or null when the project has no tool by that name. */
  tool: Tool | null;
}

/**
 * Resolves the tools a manifest names against its project's registry, in
 * manifest order. A name with no registered tool makes the run fail at claim
 * with validation_failed, so the page marks it.
 */
export function taskTools(names: readonly string[] | undefined, registry: readonly Tool[], projectId: string): TaskToolView[] {
  const byName = new Map(registry.filter((t) => t.project_id === projectId).map((t) => [t.name, t]));
  return (names ?? []).map((name) => ({ name, tool: byName.get(name) ?? null }));
}

export interface ToolGroup {
  projectId: string;
  /** Null when the project is not in the caller's project list. */
  project: ProjectRef | null;
  tools: Tool[];
}

/**
 * Groups registered tools by project: the personal project first, then
 * projects by name, tools by name within each.
 */
export function groupToolsByProject(tools: readonly Tool[], projects: readonly ProjectRef[]): ToolGroup[] {
  const known = new Map(projects.map((p) => [p.id, p]));
  const groups = new Map<string, ToolGroup>();
  for (const tool of tools) {
    let g = groups.get(tool.project_id);
    if (!g) {
      g = { projectId: tool.project_id, project: known.get(tool.project_id) ?? null, tools: [] };
      groups.set(tool.project_id, g);
    }
    g.tools.push(tool);
  }
  const rank = (g: ToolGroup) => (g.project?.is_personal ? 0 : g.project ? 1 : 2);
  const out = [...groups.values()].sort(
    (a, b) => rank(a) - rank(b) || (a.project?.name ?? a.projectId).localeCompare(b.project?.name ?? b.projectId),
  );
  for (const g of out) g.tools.sort((a, b) => a.name.localeCompare(b.name));
  return out;
}

/** The host of a tool's address, for a compact row; the full address stays in the details. */
export function toolHost(url: string): string {
  try {
    return new URL(url).host || url;
  } catch {
    return url;
  }
}

/** Hex digits of a tree hash shown in a row; the full hash stays in the tooltip. */
export const TREE_HASH_SHORT = 12;

/**
 * Where a tool lives, for a compact row: the address of an http or mcp
 * tool, the file tree of a script tool (which has no address).
 */
export type ToolLocation =
  | { kind: "url"; url: string; host: string }
  | { kind: "files"; treeHash: string | null; shortHash: string | null; fileCount: number };

/** The tool's location, or null when there is nothing to show, e.g. an http or mcp tool with an empty address. */
export function toolLocation(tool: Pick<Tool, "kind" | "url" | "files" | "tree_hash">): ToolLocation | null {
  if (tool.kind === "script") {
    const treeHash = tool.tree_hash || null;
    return { kind: "files", treeHash, shortHash: treeHash ? treeHash.slice(0, TREE_HASH_SHORT) : null, fileCount: tool.files?.length ?? 0 };
  }
  const url = (tool.url ?? "").trim();
  return url ? { kind: "url", url, host: toolHost(url) } : null;
}
