import { I18nProvider, type Locale, type RunStage } from "@lerryn/core";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";

import { RunStageGraph, RunStageList, RunStages } from "./RunStageGraph";
import { STAGE_NODE, layoutStageGraph } from "./stageGraph";

const renderIn = (ui: ReactElement, locale: Locale = "zh-CN") => render(<I18nProvider locale={locale}>{ui}</I18nProvider>);

const stage = (id: string, after: string[] = [], over: Partial<RunStage> = {}): RunStage => ({
  id,
  name: id,
  declared: true,
  after,
  status: "pending",
  started_at: null,
  finished_at: null,
  message: null,
  ...over,
});

const place = (layout: ReturnType<typeof layoutStageGraph>) =>
  Object.fromEntries(layout.nodes.map((n) => [n.id, [n.layer, n.row]]));

describe("layoutStageGraph", () => {
  const { width: w, height: h, colGap, rowGap } = STAGE_NODE;

  it("puts each stage one column after the furthest stage it waits for", () => {
    // fetch → (clean, enrich) → save; save also waits for fetch directly.
    const layout = layoutStageGraph([
      stage("fetch"),
      stage("clean", ["fetch"]),
      stage("enrich", ["fetch"]),
      stage("save", ["clean", "enrich", "fetch"]),
    ]);
    expect(place(layout)).toEqual({ fetch: [0, 0], clean: [1, 0], enrich: [1, 1], save: [2, 0] });
    expect(layout.edges.map((e) => `${e.from}>${e.to}`).sort()).toEqual(
      ["clean>save", "enrich>save", "fetch>clean", "fetch>enrich", "fetch>save"].sort(),
    );
    expect(layout.width).toBe(3 * w + 2 * colGap);
    expect(layout.height).toBe(2 * h + rowGap);
    // Shorter columns are centred against the tallest.
    const y = Object.fromEntries(layout.nodes.map((n) => [n.id, n.y]));
    expect(y.fetch).toBe((h + rowGap) / 2);
    expect(y.save).toBe(y.fetch);
    expect(y.enrich).toBe(h + rowGap);
  });

  it("lists nodes column by column, which is the keyboard order", () => {
    const layout = layoutStageGraph([stage("save", ["fetch"]), stage("fetch")]);
    expect(layout.nodes.map((n) => n.id)).toEqual(["fetch", "save"]);
    expect(layout.nodes.map((n) => n.index)).toEqual([1, 0]);
  });

  it("orders a column by where its dependencies sit", () => {
    // b2 waits for the lower of two roots, so it goes below b1 even though it is declared first.
    const layout = layoutStageGraph([stage("a1"), stage("a2"), stage("b2", ["a2"]), stage("b1", ["a1"])]);
    expect(place(layout)).toMatchObject({ a1: [0, 0], a2: [0, 1], b1: [1, 0], b2: [1, 1] });
  });

  it("draws edges from the right side of a stage to the left side of the next", () => {
    const layout = layoutStageGraph([stage("a"), stage("b", ["a"])]);
    expect(layout.edges).toHaveLength(1);
    const mx = (w + w + colGap) / 2;
    expect(layout.edges[0]!.path).toBe(`M${w} ${h / 2}C${mx} ${h / 2} ${mx} ${h / 2} ${w + colGap} ${h / 2}`);
  });

  it("breaks a dependency cycle instead of looping", () => {
    const layout = layoutStageGraph([stage("a", ["c"]), stage("b", ["a"]), stage("c", ["b"])]);
    expect(layout.nodes).toHaveLength(3);
    expect(layout.edges).toHaveLength(2);
    expect(new Set(layout.nodes.map((n) => n.layer))).toEqual(new Set([0, 1, 2]));
  });

  it("ignores references to unknown stages and to the stage itself", () => {
    const layout = layoutStageGraph([stage("a", ["a", "ghost"]), stage("b", ["a", "a"])]);
    expect(layout.edges.map((e) => `${e.from}>${e.to}`)).toEqual(["a>b"]);
  });

  it("gives reported stages that were not declared a column after the declared ones", () => {
    const layout = layoutStageGraph([stage("a"), stage("b", ["a"]), stage("extra", [], { declared: false })]);
    expect(place(layout)).toMatchObject({ a: [0, 0], b: [1, 0], extra: [2, 0] });
    expect(layout.edges).toHaveLength(1);
  });

  it("is empty without stages", () => {
    expect(layoutStageGraph([])).toEqual({ nodes: [], edges: [], width: 0, height: 0 });
  });
});

const NOW = Date.parse("2026-11-03T08:01:00Z");
const STAGES = [
  stage("fetch", [], { name: "获取输入", status: "done", started_at: "2026-11-03T08:00:00Z", finished_at: "2026-11-03T08:00:12.4Z" }),
  stage("llm", ["fetch"], { name: "调用模型", status: "failed", started_at: "2026-11-03T08:00:12.4Z", finished_at: "2026-11-03T08:00:50.2Z" }),
  stage("chart", ["fetch"], { name: "生成图表", status: "running", started_at: "2026-11-03T08:00:30Z", message: "第 2 张" }),
  stage("save", ["llm", "chart"], { name: "保存结果" }),
];

describe("RunStageGraph", () => {
  it("draws nothing when no stage depends on another", () => {
    const { container } = renderIn(<RunStageGraph stages={[stage("a"), stage("b")]} />);
    expect(container.innerHTML).toBe("");
  });

  it("shows each stage with a status icon, the status in words and what it waits for", () => {
    const { container } = renderIn(<RunStageGraph stages={STAGES} now={NOW} onSelectStage={() => {}} />);
    const graph = screen.getByRole("group", { name: "阶段" });
    const nodes = within(graph).getAllByRole("button");
    expect(nodes.map((n) => n.dataset.stage)).toEqual(["fetch", "llm", "chart", "save"]);
    for (const n of nodes) expect(n.querySelector(".g svg")).not.toBeNull();
    expect(nodes[0]!.getAttribute("aria-label")).toBe("获取输入，已完成，12.4s");
    expect(nodes[1]!.getAttribute("aria-label")).toBe("调用模型，失败，37.8s，在 获取输入 之后");
    expect(nodes[3]!.getAttribute("aria-label")).toBe("保存结果，未开始，在 调用模型、生成图表 之后");
    expect(within(nodes[1]!).getByText("失败 · 37.8s")).toBeTruthy();
    expect(nodes[1]!.className).toContain("snode--bad");
    expect(nodes[2]!.className).toContain("snode--run");
    expect(nodes[2]!.title).toBe("生成图表 · 第 2 张");
    expect(nodes[0]!.title).toBe("获取输入");
    // Edges out of a finished stage are solid, the others dashed.
    expect(container.querySelector('[data-edge="fetch>llm"]')!.getAttribute("class")).toContain("sedge--done");
    expect(container.querySelector('[data-edge="llm>save"]')!.getAttribute("class")).not.toContain("sedge--done");
    expect(container.querySelector("svg.sgraph-edges")!.getAttribute("aria-hidden")).toBe("true");
  });

  it("reports the picked stage", () => {
    const onSelect = vi.fn();
    renderIn(<RunStageGraph stages={STAGES} now={NOW} onSelectStage={onSelect} />);
    fireEvent.click(screen.getByRole("button", { name: /^生成图表/ }));
    expect(onSelect).toHaveBeenCalledWith("chart");
  });

  it("disables the nodes when there is nothing to locate", () => {
    renderIn(<RunStageGraph stages={STAGES} now={NOW} />);
    for (const n of screen.getAllByRole("button")) expect((n as HTMLButtonElement).disabled).toBe(true);
  });

  it("follows the locale", () => {
    renderIn(<RunStageGraph stages={STAGES} now={NOW} />, "en");
    expect(screen.getByRole("group", { name: "Stages" })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^保存结果, Not started, After 调用模型, 生成图表$/ })).toBeTruthy();
  });
});

describe("RunStageList", () => {
  it("lists the stages in order with their dependencies", () => {
    const onSelect = vi.fn();
    renderIn(<RunStageList stages={STAGES} now={NOW} onSelectStage={onSelect} />);
    const list = screen.getByRole("list", { name: "阶段" });
    const items = within(list).getAllByRole("button");
    expect(items.map((i) => i.dataset.stage)).toEqual(["fetch", "llm", "chart", "save"]);
    expect(within(items[3]!).getByText("未开始 · 在 调用模型、生成图表 之后")).toBeTruthy();
    expect(within(items[0]!).getByText("12.4s")).toBeTruthy();
    fireEvent.click(items[1]!);
    expect(onSelect).toHaveBeenCalledWith("llm");
  });
});

describe("RunStages", () => {
  it("keeps the duration timeline for stages without dependencies", () => {
    const { container } = renderIn(<RunStages stages={[stage("a"), stage("b")]} />);
    expect(container.querySelector(".flutes")).not.toBeNull();
    expect(container.querySelector(".sgraph")).toBeNull();
  });

  it("offers the graph and, for narrow boards, the list", () => {
    const { container } = renderIn(<RunStages stages={STAGES} now={NOW} />);
    expect(container.querySelector(".stages > .sgraph.stages-graph")).not.toBeNull();
    expect(container.querySelector(".stages > .slist.stages-list")).not.toBeNull();
    expect(container.querySelector(".flutes")).toBeNull();
  });
});
