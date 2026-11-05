import { createLerrynClient, type Schemas } from "@lerryn/api";
import { ApiProvider, I18nProvider, queryKeys } from "@lerryn/core";
import { LerrynThemeProvider } from "@lerryn/ui-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { createAppRouter } from "./router";

type Runner = Schemas["Runner"];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = () => json({ type: "urn:lerryn:error:not_found", title: "not_found", status: 404, code: "not_found", request_id: "runner-request" }, 404);
const page = (items: Runner[], next_cursor: string | null = null) => json({ items, next_cursor });
const runner = (id: string, status: Runner["status"] = "disabled", running = 0): Runner => ({
  id, name: id, status, running, labels: [], slots: 4, version: null,
  last_seen_at: "2026-11-04T00:00:00Z", created_at: "2026-11-04T00:00:00Z",
  disabled_at: status === "disabled" ? "2026-11-04T00:00:00Z" : null,
});
const clients: QueryClient[] = [];

afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
});

function renderRunners(fetch: (req: Request) => Promise<Response>) {
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: async (req) => {
    const url = new URL(req.url);
    if (url.pathname === "/api/v1/me") return json({
      id: "admin", username: "admin", display_name: "Admin", is_admin: true, personal_project_id: "project",
      email: null, identities: [], has_password: true, locale: null, timezone: null, created_at: "2026-11-04T00:00:00Z",
    });
    if (url.pathname === "/api/v1/discover") return json({ links: { console: "http://lerryn.test" }, capabilities: {} });
    if (url.pathname === "/api/v1/admin/runners" || url.pathname.startsWith("/api/v1/admin/runners/")) return fetch(req);
    return json({ items: [], next_cursor: null });
  } });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(queryClient);
  const router = createAppRouter({ api, queryClient });
  router.update({ context: { api, queryClient }, history: createMemoryHistory({ initialEntries: ["/admin/runners"] }) });
  render(
    <LerrynThemeProvider><I18nProvider locale="zh-CN"><QueryClientProvider client={queryClient}><ApiProvider client={api}>
      <RouterProvider router={router} />
    </ApiProvider></QueryClientProvider></I18nProvider></LerrynThemeProvider>,
  );
  return queryClient;
}

function stats(board: HTMLElement) {
  return [...board.querySelectorAll(".hero .stat-n")].map((el) => el.textContent);
}

describe("admin runner registry paging", () => {
  it("finds and manages the current node after 200 retired nodes without eagerly loading", async () => {
    const retired = Array.from({ length: 200 }, (_, i) => runner(`retired-${i}`));
    let current = runner("current-host", "online", 2);
    let finish: ((response: Response) => void) | undefined;
    let delayed = true;
    const cursors: (string | null)[] = [];
    const patches: unknown[] = [];
    renderRunners(async (req) => {
      if (req.method === "PATCH") {
        patches.push(await req.json());
        current = runner("current-host");
        return json(current);
      }
      const url = new URL(req.url);
      expect(url.searchParams.get("limit")).toBe("200");
      expect(url.searchParams.has("status")).toBe(false);
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor) return page(retired, "current-page");
      if (delayed) return new Promise<Response>((resolve) => { finish = resolve; });
      return page([current]);
    });
    const list = await screen.findByRole("list", { name: "执行节点" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(200);
    const board = screen.getByRole("main", { name: "执行节点" });
    expect(cursors).toEqual([null]);
    expect(stats(board)).toEqual(["0/ 0", "0"]);
    expect(within(board).getByText("已显示节点的运行 / 容量")).toBeTruthy();
    expect(within(board).getByText("已显示的在线节点")).toBeTruthy();
    expect(within(board).queryByText("current-host")).toBeNull();
    const user = userEvent.setup();
    const more = within(board).getByRole("button", { name: "显示更多" });
    await user.click(more);
    await waitFor(() => expect(finish).toBeDefined());
    await user.click(more);
    expect(cursors).toEqual([null, "current-page"]);
    delayed = false;
    await act(async () => finish!(page([current])));
    await within(board).findByText("current-host");
    expect(stats(board)).toEqual(["2/ 4", "1"]);
    expect(within(board).queryByText("已显示的在线节点")).toBeNull();
    expect(within(board).queryByRole("button", { name: "显示更多" })).toBeNull();
    await user.click(within(board).getByRole("button", { name: "current-host 的操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "停用" }));
    await waitFor(() => expect(within(list).getAllByRole("button", { name: "启用" })).toHaveLength(201));
    expect(patches).toEqual([{ disabled: true }]);
    expect(stats(board)).toEqual(["0/ 0", "0"]);
  });

  it("qualifies each partial statistic without promising later disabled nodes add capacity", async () => {
    const head = [runner("active", "online", 2), runner("quiet", "offline", 1)];
    const cursors: (string | null)[] = [];
    renderRunners(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      return cursor ? page([runner("retired")]) : page(head, "disabled-page");
    });
    await screen.findByRole("list", { name: "执行节点" });
    const board = screen.getByRole("main", { name: "执行节点" });
    expect(stats(board)).toEqual(["3/ 4", "1", "1"]);
    for (const label of ["已显示节点的运行 / 容量", "已显示的在线节点", "已显示的失联节点"]) {
      expect(within(board).getByText(label)).toBeTruthy();
    }
    const user = userEvent.setup();
    await user.click(within(board).getByRole("button", { name: "显示更多" }));
    await within(board).findByText("retired");
    expect(stats(board)).toEqual(["3/ 4", "1", "1"]);
    expect(board.textContent).not.toContain("已显示");
    expect(cursors).toEqual([null, "disabled-page"]);
  });

  it.each([false, true])("counts draining nodes among all shown active runs, partial=%s", async (partial) => {
    const head = [runner("active", "online", 2), runner("quiet", "offline", 1), runner("draining", "disabled", 1), runner("retired")];
    renderRunners(async (req) => new URL(req.url).searchParams.has("cursor")
      ? page([runner("later-draining", "disabled", 2)])
      : page(head, partial ? "draining-page" : null));
    const list = await screen.findByRole("list", { name: "执行节点" });
    const board = screen.getByRole("main", { name: "执行节点" });
    expect(stats(board)).toEqual(["4/ 4", "1", "1"]);
    expect(within(board).getByText(partial ? "已显示节点的运行 / 容量" : "运行中 / 容量")).toBeTruthy();
    const draining = within(list).getByText("draining").closest<HTMLElement>("[role=listitem]")!;
    expect(within(draining).getByLabelText("1 / 4 个槽位在用").textContent).toBe("1/4");
    expect(within(draining).getByRole("button", { name: "启用" })).toBeTruthy();
    const retired = within(list).getByText("retired").closest<HTMLElement>("[role=listitem]")!;
    expect(within(retired).queryByLabelText("0 / 4 个槽位在用")).toBeNull();
    if (partial) {
      await userEvent.setup().click(within(board).getByRole("button", { name: "显示更多" }));
      await within(list).findByText("later-draining");
      expect(stats(board)).toEqual(["6/ 4", "1", "1"]);
      expect(within(board).getByText("运行中 / 容量")).toBeTruthy();
      expect(within(board).queryByText("已显示节点的运行 / 容量")).toBeNull();
      const later = within(list).getByText("later-draining").closest<HTMLElement>("[role=listitem]")!;
      expect(within(later).getByLabelText("2 / 4 个槽位在用").textContent).toBe("2/4");
    }
  });

  it("keeps a disabled node's active run visible until a refresh confirms it drained", async () => {
    let current = runner("draining", "online", 1);
    const available = runner("available", "online");
    const queryClient = renderRunners(async (req) => {
      if (req.method === "PATCH") {
        expect(await req.json()).toEqual({ disabled: true });
        current = { ...current, status: "disabled", disabled_at: "2026-11-04T00:00:00Z" };
        return json(current);
      }
      return page([available, current]);
    });
    const list = await screen.findByRole("list", { name: "执行节点" });
    const board = screen.getByRole("main", { name: "执行节点" });
    expect(stats(board)).toEqual(["1/ 8", "2"]);
    const user = userEvent.setup();
    await user.click(within(board).getByRole("button", { name: "draining 的操作" }));
    await user.click(await screen.findByRole("menuitem", { name: "停用" }));
    const draining = within(list).getByText("draining").closest<HTMLElement>("[role=listitem]")!;
    await within(draining).findByRole("button", { name: "启用" });
    expect(stats(board)).toEqual(["1/ 4", "1"]);
    expect(within(draining).getByLabelText("1 / 4 个槽位在用").textContent).toBe("1/4");
    current = { ...current, running: 0 };
    await act(async () => queryClient.invalidateQueries({ queryKey: queryKeys.runners }));
    await waitFor(() => expect(stats(board)).toEqual(["0/ 4", "1"]));
    expect(within(draining).queryByLabelText("1 / 4 个槽位在用")).toBeNull();
    expect(within(draining).queryByLabelText("0 / 4 个槽位在用")).toBeNull();
    expect(within(draining).getByRole("button", { name: "启用" })).toBeTruthy();
  });

  it("keeps loaded rows and their partial statistics after a later-page error, then retries that page", async () => {
    let fail = true;
    const cursors: (string | null)[] = [];
    renderRunners(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      return cursor ? (fail ? problem() : page([runner("current-host", "online")])) : page([runner("retired")], "next");
    });
    await screen.findByText("retired");
    const board = screen.getByRole("main", { name: "执行节点" });
    const user = userEvent.setup();
    await user.click(within(board).getByRole("button", { name: "显示更多" }));
    const retry = await within(board).findByRole("button", { name: "重试" });
    expect(within(board).getByText("retired")).toBeTruthy();
    expect(within(board).getByText("已显示的在线节点")).toBeTruthy();
    expect(within(board).queryByText("尚无执行节点")).toBeNull();
    expect(within(board).queryByRole("button", { name: "显示更多" })).toBeNull();
    fail = false;
    await user.click(retry);
    await within(board).findByText("current-host");
    expect(within(board).queryByRole("button", { name: "重试" })).toBeNull();
    expect(within(board).getAllByRole("listitem")).toHaveLength(2);
    expect(cursors).toEqual([null, "next", "next"]);
  });

  it.each([false, true])("shows a successful empty registry, after first-page failure=%s", async (initialFailure) => {
    let fail = initialFailure;
    renderRunners(async () => fail ? problem() : page([]));
    if (initialFailure) {
      const retry = await screen.findByRole("button", { name: "重试" });
      expect(screen.queryByText("尚无执行节点")).toBeNull();
      fail = false;
      await userEvent.setup().click(retry);
    }
    await screen.findByText("尚无执行节点");
    expect(screen.queryByRole("button", { name: "显示更多" })).toBeNull();
    expect(screen.queryByRole("list", { name: "执行节点" })).toBeNull();
  });
});
