import { createLerrynClient, type Schemas } from "@lerryn/api";
import { ApiProvider, I18nProvider } from "@lerryn/core";
import { LerrynThemeProvider } from "@lerryn/ui-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { createAppRouter } from "./router";

type Delivery = Schemas["NotificationDelivery"];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = () => json({ type: "urn:lerryn:error:permission_denied", title: "permission_denied", status: 403, code: "permission_denied", request_id: "delivery-page" }, 403);
const delivery = (id: string): Delivery => ({
  id, event_type: "run_failed", status: "delivered", attempts: 1, http_status: 200, error: null,
  task: { id: `task-${id}`, name: `task-${id}`, project_id: "project" }, run_id: null, merged_count: 1,
  created_at: "2026-11-03T00:00:00Z", last_attempt_at: "2026-11-03T00:00:00Z", delivered_at: "2026-11-03T00:00:00Z",
});
const clients: QueryClient[] = [];

afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
});

function renderHistory(count: number, opts: {
  fail?: (cursor: string | null) => boolean;
  nextPage?: (req: Request) => Promise<Response>;
} = {}) {
  let rows = Array.from({ length: count }, (_, i) => delivery(String(i)));
  if (rows.length) rows[rows.length - 1] = { ...rows.at(-1)!, status: "failed", http_status: 503, error: "receiver unavailable", delivered_at: null };
  const otherRows = [delivery("other-channel")];
  const cursors: (string | null)[] = [];
  const channel = (id: string): Schemas["NotificationChannel"] => ({
    id, name: id, kind: "webhook", enabled: true, url_preview: "https://notify.example.com/…/hook", subscriptions: [],
    last_delivery: null, created_at: "2026-11-03T00:00:00Z", updated_at: "2026-11-03T00:00:00Z",
  });
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: async (req) => {
    const url = new URL(req.url);
    if (url.pathname === "/api/v1/me") return json({
      id: "alice", username: "alice", display_name: "Alice", is_admin: false, personal_project_id: "project",
      email: null, identities: [], has_password: true, locale: null, timezone: null, created_at: "2026-11-03T00:00:00Z",
    });
    if (url.pathname === "/api/v1/discover") return json({ links: { console: "http://lerryn.test" }, capabilities: {} });
    if (url.pathname === "/api/v1/notification-channels") return json({ items: [channel("channel-a"), channel("channel-b")] });
    if (url.pathname === "/api/v1/notification-channels/channel-a/test") {
      rows = [delivery("new-test"), ...rows];
      return json(rows[0]);
    }
    if (url.pathname === "/api/v1/notification-channels/channel-a/deliveries") {
      expect(url.searchParams.get("limit")).toBe("50");
      expect(url.searchParams.has("status")).toBe(false);
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      if (opts.fail?.(cursor)) return problem();
      if (cursor && opts.nextPage) return opts.nextPage(req);
      const offset = cursor ? rows.findIndex((d) => d.id === cursor) + 1 : 0;
      const items = rows.slice(offset, offset + 50);
      return json({ items, next_cursor: offset + 50 < rows.length ? items.at(-1)!.id : null });
    }
    if (url.pathname === "/api/v1/notification-channels/channel-b/deliveries") return json({ items: otherRows, next_cursor: null });
    return json({ items: [], next_cursor: null });
  } });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
  clients.push(queryClient);
  const router = createAppRouter({ api, queryClient });
  router.update({ context: { api, queryClient }, history: createMemoryHistory({ initialEntries: ["/settings/notifications"] }) });
  render(
    <LerrynThemeProvider><I18nProvider locale="zh-CN"><QueryClientProvider client={queryClient}><ApiProvider client={api}>
      <RouterProvider router={router} />
    </ApiProvider></QueryClientProvider></I18nProvider></LerrynThemeProvider>,
  );
  return { cursors };
}

async function openHistory(id = "channel-a") {
  fireEvent.click(await screen.findByRole("button", { name: `${id} 的发送记录` }));
  return screen.findByRole("dialog");
}

describe("notification delivery history in settings", () => {
  it("loads older retained failures on demand and blocks duplicate clicks while loading", async () => {
    let finish: ((response: Response) => void) | undefined;
    const { cursors } = renderHistory(51, { nextPage: async () => new Promise<Response>((resolve) => { finish = resolve; }) });
    await screen.findByRole("button", { name: "channel-a 的发送记录" });
    expect(cursors).toEqual([]);
    const dialog = await openHistory();
    await within(dialog).findByText(/task-49$/);
    expect(within(dialog).queryByText(/task-50$/)).toBeNull();
    expect(cursors).toEqual([null]);
    const user = userEvent.setup();
    const more = within(dialog).getByRole("button", { name: "显示更多" });
    await user.click(more);
    await waitFor(() => expect(finish).toBeDefined());
    expect(more.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(more);
    fireEvent.click(more);
    expect(cursors).toEqual([null, "49"]);
    const last = { ...delivery("50"), status: "failed" as const, http_status: 503, delivered_at: null };
    await act(async () => finish!(json({ items: [last], next_cursor: null })));
    await within(dialog).findByText(/task-50$/);
    expect(within(dialog).getByText(/^发送失败/).textContent).toContain("503");
    expect(within(dialog).getAllByText(/^执行失败 · task-/)).toHaveLength(51);
    expect(within(dialog).queryByRole("button", { name: "显示更多" })).toBeNull();
  });

  it("preserves the current page when loading more fails and retries the same cursor", async () => {
    let fail = true;
    const { cursors } = renderHistory(51, { fail: (cursor) => !!cursor && fail });
    const dialog = await openHistory();
    await within(dialog).findByText(/task-49$/);
    fireEvent.click(within(dialog).getByRole("button", { name: "显示更多" }));
    const retry = await within(dialog).findByRole("button", { name: "重试" });
    expect(within(dialog).getByText(/task-0$/)).toBeTruthy();
    expect(within(dialog).getByText(/task-49$/)).toBeTruthy();
    expect(within(dialog).queryByText(/task-50$/)).toBeNull();
    expect(within(dialog).queryByRole("button", { name: "显示更多" })).toBeNull();
    fail = false;
    fireEvent.click(retry);
    await within(dialog).findByText(/task-50$/);
    expect(cursors).toEqual([null, "49", "49"]);
    expect(within(dialog).queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("offers retry for an initial read error and shows a real empty history afterwards", async () => {
    let fail = true;
    const { cursors } = renderHistory(0, { fail: () => fail });
    const dialog = await openHistory();
    const retry = await within(dialog).findByRole("button", { name: "重试" });
    expect(within(dialog).queryByText("尚未发送")).toBeNull();
    fail = false;
    fireEvent.click(retry);
    await within(dialog).findByText("尚未发送");
    expect(cursors).toEqual([null, null]);
    expect(within(dialog).queryByRole("button", { name: "显示更多" })).toBeNull();
  });

  it("cancels a closed channel's next page and keeps another channel's records isolated", async () => {
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    renderHistory(51, { nextPage: async (req) => {
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    } });
    const first = await openHistory();
    await within(first).findByText(/task-49$/);
    fireEvent.click(within(first).getByRole("button", { name: "显示更多" }));
    await waitFor(() => expect(finish).toBeDefined());
    fireEvent.click(within(first).getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(signal?.aborted).toBe(true);
    const second = await openHistory("channel-b");
    await within(second).findByText(/task-other-channel$/);
    await act(async () => finish!(json({ items: [delivery("late-a")], next_cursor: null })));
    expect(within(second).queryByText(/task-late-a$/)).toBeNull();
    expect(within(second).getByText(/task-other-channel$/)).toBeTruthy();
  });

  it("refreshes the loaded page chain after sending a new test notification", async () => {
    const { cursors } = renderHistory(51);
    const first = await openHistory();
    await within(first).findByText(/task-49$/);
    fireEvent.click(within(first).getByRole("button", { name: "显示更多" }));
    await within(first).findByText(/task-50$/);
    fireEvent.click(within(first).getByRole("button", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    fireEvent.click(screen.getByRole("button", { name: "向 channel-a 发送测试消息" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "向 channel-a 发送测试消息" }).getAttribute("aria-disabled")).not.toBe("true"));
    const refreshed = await openHistory();
    await within(refreshed).findByText(/task-new-test$/);
    await waitFor(() => expect(within(refreshed).getAllByText(/^执行失败 · task-/)).toHaveLength(52));
    expect(cursors).toEqual([null, "49", null, "48"]);
  });
});
