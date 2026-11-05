import { I18nProvider, RUN_STATUSES, type Locale, type LogLine, type RunStage } from "@lerryn/core";
import { fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { describe, expect, it, vi } from "vitest";

import { LogViewer } from "./LogViewer";
import { ResultPanel, viewOfData } from "./ResultPanel";
import { RunStatusBadge, StatusGlyph } from "./RunStatusBadge";
import { RunTimeline } from "./RunTimeline";

const renderIn = (ui: ReactElement, locale: Locale = "zh-CN") =>
  render(<I18nProvider locale={locale}>{ui}</I18nProvider>);

describe("RunStatusBadge", () => {
  it.each(RUN_STATUSES)("%s shows an icon and text", (status) => {
    const { container } = renderIn(<RunStatusBadge status={status} />);
    const badge = container.querySelector(".rs")!;
    expect(badge.textContent).not.toBe("");
    // Every status carries a graphic, never colour alone.
    expect(badge.querySelector("svg")).not.toBeNull();
  });

  it("uses the PRD wording and the design tones", () => {
    renderIn(<RunStatusBadge status="interrupted" />);
    const badge = screen.getByText("运行中断");
    expect(badge.className).toContain("rs--warn");
  });

  it("follows the locale", () => {
    renderIn(<RunStatusBadge status="timed_out" />, "en");
    expect(screen.getByText("Timed out")).toBeTruthy();
  });

  it("names a glyph for screen readers", () => {
    renderIn(<StatusGlyph status="failed" />);
    expect(screen.getByRole("img", { name: "执行失败" })).toBeTruthy();
  });
});

const stage = (over: Partial<RunStage>): RunStage => ({
  id: "s",
  name: "stage",
  declared: true,
  after: [],
  status: "pending",
  started_at: null,
  finished_at: null,
  message: null,
  ...over,
});

describe("RunTimeline", () => {
  it("draws nothing without stages", () => {
    const { container } = renderIn(<RunTimeline stages={[]} />);
    expect(container.innerHTML).toBe("");
  });

  it("sizes stages by duration and reports picks", () => {
    const onSelect = vi.fn();
    const now = Date.parse("2026-11-02T02:45:40Z");
    renderIn(
      <RunTimeline
        now={now}
        onSelectStage={onSelect}
        stages={[
          stage({ id: "fetch", name: "拉取新线索", status: "done", started_at: "2026-11-02T02:45:00Z", finished_at: "2026-11-02T02:45:06.2Z" }),
          stage({ id: "write", name: "写入多维表格", status: "running", started_at: "2026-11-02T02:45:10Z" }),
          stage({ id: "save", name: "保存结果", status: "pending" }),
        ]}
      />,
    );
    const fetch = screen.getByRole("button", { name: /拉取新线索/ });
    const write = screen.getByRole("button", { name: /写入多维表格/ });
    expect(fetch.style.flexGrow).toBe("6.2");
    expect(write.style.flexGrow).toBe("30");
    expect(write.className).toContain("flute--run");
    expect(screen.getByRole("button", { name: /保存结果/ }).className).toContain("flute--idle");
    fireEvent.click(write);
    expect(onSelect).toHaveBeenCalledWith("write");
  });
});

describe("ResultPanel", () => {
  it("renders nothing without a reported result", () => {
    const { container } = renderIn(<ResultPanel result={null} />);
    expect(container.innerHTML).toBe("");
  });

  it("labels the result as reported by the task, apart from the run status", () => {
    renderIn(<ResultPanel result={{ status: "partial", summary: "42 条新线索", data: { 新线索: 42, 高意向: 7 } }} />);
    const panel = screen.getByRole("region", { name: "结果" });
    expect(within(panel).getByText("任务上报")).toBeTruthy();
    expect(within(panel).getByText("部分完成")).toBeTruthy();
    expect(within(panel).getByText("42")).toBeTruthy();
    expect(within(panel).getByText("高意向")).toBeTruthy();
  });

  it("never renders reported markup", () => {
    const { container } = renderIn(
      <ResultPanel result={{ status: "ok", summary: "<img src=x onerror=alert(1)>", data: { note: "<b>x</b>" } }} />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("b")).toBeNull();
    expect(container.textContent).toContain("<img src=x onerror=alert(1)>");
  });

  it.each([
    [null, "none"],
    [{ a: 1, b: 2 }, "metrics"],
    [{ a: 1, b: "x" }, "pairs"],
    [[{ name: "a", score: 92 }, { name: "b", score: 88 }], "table"],
    [{ nested: { a: 1 } }, "json"],
    [[1, 2, 3], "json"],
  ])("lays out %j as %s", (data, kind) => {
    expect(viewOfData(data).kind).toBe(kind);
  });
});

const line = (seq: number, over: Partial<LogLine> = {}): LogLine => ({
  seq,
  ts: "2026-11-02T02:45:01.204Z",
  stream: "stdout",
  stage: null,
  text: `line ${seq}`,
  ...over,
});

describe("LogViewer", () => {
  const lines = [
    line(18, { stream: "system", text: "阶段开始：调用模型" }),
    line(19, { text: "生成摘要" }),
    line(20, { stream: "stderr", text: "HTTP 429 Too Many Requests" }),
    line(21, { stream: "stderr", text: "http 429 again" }),
  ];

  it("shows the gap before the tail and loads earlier lines on request", () => {
    const onLoadEarlier = vi.fn();
    renderIn(<LogViewer lines={lines} loaded complete onLoadEarlier={onLoadEarlier} />);
    const more = screen.getByRole("button", { name: /更早 17 行/ });
    fireEvent.click(more);
    expect(onLoadEarlier).toHaveBeenCalled();
  });

  it("separates stderr and filters by stream", () => {
    const { container } = renderIn(<LogViewer lines={lines} loaded complete />);
    expect(container.querySelectorAll(".ll--stderr")).toHaveLength(2);
    expect(container.querySelectorAll(".ll--system")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "stdout" }));
    // stdout keeps platform lines, drops stderr.
    expect(container.querySelectorAll(".ll--stderr")).toHaveLength(0);
    expect(container.querySelectorAll(".ll")).toHaveLength(2);
  });

  it("highlights search matches and counts them", () => {
    const { container } = renderIn(<LogViewer lines={lines} loaded complete />);
    fireEvent.change(screen.getByRole("searchbox", { name: "在日志中搜索" }), { target: { value: "429" } });
    expect(screen.getByText("2 处")).toBeTruthy();
    expect(container.querySelectorAll("mark")).toHaveLength(2);
  });

  it("says when lines were dropped and when it is reconnecting", () => {
    renderIn(<LogViewer lines={lines} loaded complete={false} dropped={12400} reconnectAttempt={3} />);
    expect(screen.getByText("超出日志上限，已丢弃 12,400 行")).toBeTruthy();
    expect(screen.getByText("重新连接 · 第 3 次")).toBeTruthy();
    expect(screen.getByRole("switch", { name: "跟随最新输出" }).getAttribute("aria-checked")).toBe("true");
  });

  it("hides the follow switch once the run has ended", () => {
    renderIn(<LogViewer lines={lines} loaded complete />);
    expect(screen.queryByRole("switch")).toBeNull();
  });

  it("labels where each stage's output begins", () => {
    const staged = [
      line(1, { stage: "fetch" }),
      line(2, { stage: "fetch" }),
      line(3, { stage: "save" }),
      line(4, { stream: "system", text: "process exited with code 0" }),
    ];
    const { container } = renderIn(
      <LogViewer lines={staged} loaded complete stages={[{ id: "fetch", name: "获取" }, { id: "save", name: "保存" }]} />,
    );
    const labels = [...container.querySelectorAll(".ll--stage")].map((el) => el.textContent);
    expect(labels).toEqual(["▸ 阶段 · 获取", "▸ 阶段 · 保存"]);
    expect(container.querySelector('[data-seq="3"]')?.getAttribute("data-stage")).toBe("save");
    // Lines without a stage get no label, and filtering keeps the labels of what is shown.
    fireEvent.click(screen.getByRole("button", { name: "stderr" }));
    expect(container.querySelectorAll(".ll--stage")).toHaveLength(0);
  });

  it("shows an empty state", () => {
    renderIn(<LogViewer lines={[]} loaded complete />);
    expect(screen.getByText("尚无输出")).toBeTruthy();
  });

  it("says when retention removed the log and offers no download", () => {
    renderIn(<LogViewer lines={[]} loaded={false} complete={false} expired onDownload={() => {}} />);
    expect(screen.getByText("日志已按保留策略清理")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "下载日志" })).toBeNull();
  });
});
