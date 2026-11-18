import { Copy } from "@gravity-ui/icons";
import { I18nProvider, type AgentPanel, type Locale } from "@lerryn/core";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AgentPanels } from "./AgentPanels";
import { CollapsiblePanel } from "./CollapsiblePanel";
import { ConsoleDialog } from "./ConsoleDialog";
import { ActionMenu, Disclosure, Toggle } from "./Controls";
import { CronDetails, CronText } from "./CronText";
import { CopyButton, copyText } from "./IconButton";
import { InfoChip } from "./InfoChip";
import { QrCode, qrPath } from "./QrCode";
import { ErrorState, StateCard } from "./StateCard";

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
    // A native switch input: its state is the checked property, not an aria attribute.
    const sw = screen.getByRole<HTMLInputElement>("switch", { name: "开放注册" });
    expect(sw.checked).toBe(false);
    fireEvent.click(sw);
    expect(onChange).toHaveBeenCalledWith(true);
  });
});

describe("ActionMenu", () => {
  it("opens a named menu, runs the picked action and returns focus to the trigger", async () => {
    const user = userEvent.setup();
    const onRename = vi.fn();
    render(
      <ActionMenu
        label="陈默的操作"
        icon={Copy}
        items={[
          { label: "重命名", onSelect: onRename },
          { label: "移除", danger: true, onSelect: () => {} },
        ]}
      />,
    );
    const trigger = screen.getByRole("button", { name: "陈默的操作" });
    expect(screen.queryByRole("menu")).toBeNull();
    await user.click(trigger);
    const menu = screen.getByRole("menu", { name: "陈默的操作" });
    expect(within(menu).getAllByRole("menuitem").map((item) => item.textContent)).toEqual(["重命名", "移除"]);
    await user.click(within(menu).getByRole("menuitem", { name: "重命名" }));
    expect(onRename).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(trigger));
    await user.click(trigger);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
  });
});

describe("Disclosure and InfoChip", () => {
  it("shows the details as a named group after a press and closes on Escape", async () => {
    const user = userEvent.setup();
    render(
      <InfoChip detailsLabel="工具调用" details={<div className="pop-row">crm-mcp</div>}>
        工具 2
      </InfoChip>,
    );
    const chip = screen.getByRole("button", { name: "工具 2" });
    expect(chip.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("group", { name: "工具调用" })).toBeNull();
    await user.click(chip);
    expect(chip.getAttribute("aria-expanded")).toBe("true");
    const details = screen.getByRole("group", { name: "工具调用" });
    expect(details.querySelector(".pop-row")?.textContent).toBe("crm-mcp");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("group", { name: "工具调用" })).toBeNull();
    expect(chip.getAttribute("aria-expanded")).toBe("false");
  });

  it("names a trigger that is only a shape and opens it from the keyboard", async () => {
    const user = userEvent.setup();
    render(
      <Disclosure
        triggerClassName="g g--bad g-btn"
        label="离线详情"
        detailsLabel="runner-1"
        trigger={<Copy aria-hidden="true" />}
        details={<span>离线</span>}
      />,
    );
    const trigger = screen.getByRole("button", { name: "离线详情" });
    expect(trigger.className).toContain("g--bad");
    trigger.focus();
    await user.keyboard("{Enter}");
    expect(within(screen.getByRole("group", { name: "runner-1" })).getByText("离线")).toBeTruthy();
  });
});

describe("ConsoleDialog", () => {
  it("is named by its heading and keeps a secret dialog open on Escape", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    render(
      <ConsoleDialog isOpen onOpenChange={onOpenChange} title="新建令牌" footer={<button type="button">完成</button>} isDismissable={false}>
        <p>只显示一次</p>
      </ConsoleDialog>,
    );
    const dialog = screen.getByRole("dialog", { name: "新建令牌" });
    expect(within(dialog).getByText("只显示一次")).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "完成" })).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(onOpenChange).not.toHaveBeenCalled();
  });
});

describe("StateCard", () => {
  it("is a status by default and an alert for a failed request", () => {
    const { unmount } = renderIn(<StateCard icon={Copy} title="还没有令牌" detail="创建后显示在这里" />);
    const card = screen.getByRole("status");
    expect(within(card).getByText("还没有令牌")).toBeTruthy();
    expect(within(card).getByText("创建后显示在这里")).toBeTruthy();
    // The shape is decoration: the title says it.
    expect(card.querySelector(".g")?.getAttribute("aria-hidden")).toBe("true");
    unmount();
    renderIn(<ErrorState requestId="req_42" />);
    expect(within(screen.getByRole("alert")).getByText("req_42")).toBeTruthy();
  });
});

describe("CollapsiblePanel", () => {
  it("keeps the header and the aside when the body collapses", async () => {
    const user = userEvent.setup();
    render(
      <CollapsiblePanel title="任务说明" aside={<span>v2</span>}>
        <p>正文</p>
      </CollapsiblePanel>,
    );
    const panel = within(screen.getByRole("region", { name: "任务说明" }));
    const button = panel.getByRole("button", { name: "任务说明" });
    expect(panel.getByRole("heading", { level: 2 })).toBeTruthy();
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(panel.getByRole("group", { name: "任务说明" })).toBeTruthy();
    await user.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(panel.queryByRole("group", { name: "任务说明" })).toBeNull();
    expect(panel.getByText("v2")).toBeTruthy();
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
