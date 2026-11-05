import { I18nProvider, type AgentPanel, type Locale } from "@lerryn/core";
import { fireEvent, render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { Copy } from "lucide-react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AgentPanels } from "./AgentPanels";
import { Toggle } from "./Controls";
import { CronDetails, CronText } from "./CronText";
import { CopyButton, copyText } from "./IconButton";
import { QrCode, qrPath } from "./QrCode";

const renderIn = (ui: ReactElement, locale: Locale = "zh-CN") => render(<I18nProvider locale={locale}>{ui}</I18nProvider>);

const cron = {
  schedule: "0 8 * * 1-5",
  timezone: "Asia/Shanghai",
  schedule_text: "每个工作日 08:00",
  next_fires: ["2026-11-03T00:00:00Z", "2026-11-04T00:00:00Z", "2026-11-05T00:00:00Z", "2026-11-06T00:00:00Z"],
};

describe("CronText", () => {
  it("shows the readable frequency with the expression and zone in the tooltip", () => {
    renderIn(<CronText cron={cron} />);
    const el = screen.getByText("每个工作日 08:00");
    expect(el.getAttribute("title")).toBe("0 8 * * 1-5 · Asia/Shanghai");
  });

  it("lists the next fires in the trigger's time zone", () => {
    renderIn(<CronDetails cron={cron} firing />);
    expect(screen.getByText("11月3日 周二")).toBeTruthy();
    expect(screen.getAllByText("08:00")).toHaveLength(3);
    expect(screen.getByText(/Asia\/Shanghai/)).toBeTruthy();
    expect(screen.queryByText(/预计时间/)).toBeNull();
  });

  it("says the fires are expected only when the task is not active", () => {
    renderIn(<CronDetails cron={cron} firing={false} />);
    expect(screen.getByText(/预计时间/)).toBeTruthy();
  });
});

describe("AgentPanels", () => {
  const panels: AgentPanel[] = [
    { id: "m", type: "metric", title: "新线索", value: 12400, label: null, unit: "条" },
    { id: "b", type: "bars", title: "趋势", unit: null, items: [{ label: "23", value: 4 }, { label: "24", value: 8 }] },
    { id: "d", type: "breakdown", title: "来源", unit: null, items: [{ label: "官网", value: 18 }, { label: "展会", value: 9 }] },
    { id: "t", type: "table", title: "排名", columns: [{ key: "n", title: "名称" }, { key: "s", title: "分" }], rows: [["华东药房", 92]] },
    { id: "x", type: "text", title: "备注", text: "<script>alert(1)</script>" },
  ];

  it("renders every type with an accessible description", () => {
    const { container } = renderIn(<AgentPanels panels={panels} />);
    expect(screen.getByText("12,400")).toBeTruthy();
    expect(screen.getByRole("img", { name: "趋势：23 4，24 8" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "来源：官网 18，展会 9" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "名称" })).toBeTruthy();
    expect(screen.getByText("92").className).toContain("num");
    // Text is text: no element is created from it.
    expect(screen.getByText("<script>alert(1)</script>").tagName).toBe("P");
    expect(container.querySelector("script")).toBeNull();
  });

  it("renders nothing without panels", () => {
    const { container } = renderIn(<AgentPanels panels={[]} />);
    expect(container.innerHTML).toBe("");
  });
});

describe("QrCode", () => {
  it("encodes the value as a square of modules", () => {
    const { size, d } = qrPath("lerryn://pair?server=https%3A%2F%2Fw.test&code=K7Q2XN4V");
    expect(size).toBeGreaterThanOrEqual(21);
    expect(d.startsWith("M0 0h1v1h-1z")).toBe(true); // the finder pattern's corner
  });

  it("is labelled and dims when expired", () => {
    const { container } = render(<QrCode value="x" label="配对二维码" dimmed />);
    expect(screen.getByRole("img", { name: "配对二维码" })).toBeTruthy();
    expect(container.querySelector(".qr--off")).not.toBeNull();
  });
});

describe("Toggle", () => {
  it("is a switch that reports the new state", () => {
    const onChange = vi.fn();
    render(<Toggle checked={false} onChange={onChange} label="开放注册" />);
    const sw = screen.getByRole("switch", { name: "开放注册" });
    expect(sw.getAttribute("aria-checked")).toBe("false");
    fireEvent.click(sw);
    expect(onChange).toHaveBeenCalledWith(true);
  });
});

describe("CopyButton", () => {
  const setClipboard = (clipboard: unknown) =>
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: clipboard });
  const setExecCommand = (impl: (command: string) => boolean) => {
    const spy = vi.fn(impl);
    Object.defineProperty(document, "execCommand", { configurable: true, value: spy });
    return spy;
  };

  afterEach(() => {
    setClipboard(undefined);
    Reflect.deleteProperty(document, "execCommand");
  });

  it("uses the Clipboard API when it is available", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });
    const execCommand = setExecCommand(() => true);
    renderIn(<CopyButton value="run_123" label="复制运行 ID" icon={Copy} />);
    fireEvent.click(screen.getByRole("button", { name: "复制运行 ID" }));
    expect(await screen.findByText("已复制")).toBeTruthy();
    expect(writeText).toHaveBeenCalledWith("run_123");
    expect(execCommand).not.toHaveBeenCalled();
  });

  it("falls back to the copy command over plain HTTP", async () => {
    setClipboard(undefined);
    let copied = "";
    const execCommand = setExecCommand(() => {
      copied = (document.activeElement as HTMLTextAreaElement).value;
      return true;
    });
    renderIn(<CopyButton value="https://lerryn.internal/mcp" label="复制 MCP 地址" icon={Copy} />);
    fireEvent.click(screen.getByRole("button", { name: "复制 MCP 地址" }));
    expect(await screen.findByText("已复制")).toBeTruthy();
    expect(execCommand).toHaveBeenCalledWith("copy");
    expect(copied).toBe("https://lerryn.internal/mcp");
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("falls back when the browser denies clipboard access", async () => {
    setClipboard({ writeText: vi.fn().mockRejectedValue(new DOMException("denied", "NotAllowedError")) });
    setExecCommand(() => true);
    await expect(copyText("x")).resolves.toBe(true);
  });

  it("says so when nothing could copy", async () => {
    setClipboard(undefined);
    setExecCommand(() => false);
    renderIn(<CopyButton value="secret" label="复制密钥" icon={Copy} />);
    fireEvent.click(screen.getByRole("button", { name: "复制密钥" }));
    expect(await screen.findByText("复制失败，请手动选中复制")).toBeTruthy();
  });

  it("keeps the textarea inside an open dialog", () => {
    setClipboard(undefined);
    let parent: Element | null = null;
    setExecCommand(() => {
      parent = document.activeElement?.parentElement ?? null;
      return true;
    });
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const button = document.createElement("button");
    dialog.appendChild(button);
    document.body.appendChild(dialog);
    void copyText("token", button);
    expect(parent).toBe(dialog);
    dialog.remove();
  });
});
