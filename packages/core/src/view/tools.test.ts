import type { Schemas } from "@lerryn/api";
import { describe, expect, it } from "vitest";

import { groupToolsByProject, taskTools, toolHost, toolLocation, TREE_HASH_SHORT } from "./tools";

const tool = (name: string, projectId: string, over: Partial<Schemas["Tool"]> = {}): Schemas["Tool"] => ({
  id: `${projectId}-${name}`,
  project_id: projectId,
  name,
  kind: "mcp",
  description: "",
  url: "https://crm.example.com/mcp",
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  ...over,
});

describe("taskTools", () => {
  it("resolves names in manifest order against the task's own project", () => {
    const registry = [tool("crm-mcp", "p1"), tool("search", "p1", { kind: "http" }), tool("ledger", "p2")];
    const got = taskTools(["search", "ledger", "crm-mcp"], registry, "p1");
    expect(got.map((v) => [v.name, v.tool?.kind ?? null])).toEqual([
      ["search", "http"],
      // Registered in another project only: the run would fail at claim.
      ["ledger", null],
      ["crm-mcp", "mcp"],
    ]);
  });

  it("is empty for a manifest without tools", () => {
    expect(taskTools(undefined, [tool("crm-mcp", "p1")], "p1")).toEqual([]);
  });
});

describe("groupToolsByProject", () => {
  it("puts the personal project first, then projects by name, tools by name", () => {
    const projects = [
      { id: "team-b", name: "运营", is_personal: false },
      { id: "me", name: "zhanglin", is_personal: true },
      { id: "team-a", name: "数据", is_personal: false },
    ];
    const groups = groupToolsByProject(
      [tool("zeta", "team-a"), tool("alpha", "team-a"), tool("crm", "me"), tool("x", "gone"), tool("y", "team-b")],
      projects,
    );
    expect(groups.map((g) => [g.projectId, g.tools.map((t) => t.name)])).toEqual([
      ["me", ["crm"]],
      ["team-a", ["alpha", "zeta"]],
      ["team-b", ["y"]],
      ["gone", ["x"]],
    ]);
    expect(groups[3]!.project).toBeNull();
  });
});

describe("toolHost", () => {
  it.each([
    ["https://crm.example.com/mcp", "crm.example.com"],
    ["http://10.0.0.5:8080/api", "10.0.0.5:8080"],
    ["not a url", "not a url"],
  ])("%s", (url, want) => expect(toolHost(url)).toBe(want));
});

describe("toolLocation", () => {
  const hash = "3f9a0c4be1d27788aa00bb11cc22dd33ee44ff5566778899aabbccddeeff0011";
  const file = (path: string) => ({ path, size: 1, sha256: "aa", executable: false });
  it.each<[string, Partial<Schemas["Tool"]>, ReturnType<typeof toolLocation>]>([
    ["an mcp address", {}, { kind: "url", url: "https://crm.example.com/mcp", host: "crm.example.com" }],
    ["an empty address", { kind: "http", url: "" }, null],
    ["a blank address", { kind: "mcp", url: "  " }, null],
    [
      "a script tool's file tree",
      { kind: "script", url: "", files: [file("fmt.py"), file("lib.py")], tree_hash: hash },
      { kind: "files", treeHash: hash, shortHash: hash.slice(0, TREE_HASH_SHORT), fileCount: 2 },
    ],
    ["a script tool without its tree", { kind: "script", url: "" }, { kind: "files", treeHash: null, shortHash: null, fileCount: 0 }],
  ])("%s", (_, over, want) => expect(toolLocation(tool("t", "p1", over))).toEqual(want));
});
