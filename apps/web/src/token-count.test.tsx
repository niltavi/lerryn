import { createLerrynClient, type Schemas } from "@lerryn/api";
import { ApiProvider, I18nProvider, queryKeys, type Locale } from "@lerryn/core";
import { LerrynThemeProvider } from "@lerryn/ui-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { createAppRouter } from "./router";

type AccessToken = Schemas["AccessToken"];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status = 503) => json({ type: "urn:lerryn:error:internal_error", title: "internal_error", status, code: status === 401 ? "unauthenticated" : "internal_error", request_id: "token-page" }, status);
const token = (id: string, state: "active" | "expired" | "revoked" = "active"): AccessToken => ({
  id, name: id, prefix: "wl_pat_fixture", scopes: ["read"], created_at: "2026-11-03T00:00:00Z",
  expires_at: state === "expired" ? "2000-01-01T00:00:00Z" : null,
  revoked_at: state === "revoked" ? "2026-11-04T00:00:00Z" : null, last_used_at: null,
});
const revoked = () => Array.from({ length: 50 }, (_, i) => token(`revoked-${i}`, "revoked"));
const clients: QueryClient[] = [];
afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
});

function renderTokens(items: AccessToken[], options: {
  locale?: Locale;
  later?: AccessToken[];
  read?: (cursor: string | null, owner: string) => Promise<Response | undefined>;
} = {}) {
  let owner = "alice";
  let signedIn = true;
  const lists = { alice: items, bob: options.later ?? [token("bob-only")] };
  const cursors: (string | null)[] = [];
  const revokedIds: string[] = [];
  const me = () => ({
    id: owner, username: owner, display_name: owner, is_admin: false, personal_project_id: `project-${owner}`,
    email: null, identities: [], has_password: true, locale: null, timezone: null, created_at: "2026-11-03T00:00:00Z",
  });
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: async (req) => {
    const url = new URL(req.url);
    if (url.pathname === "/api/v1/me") return signedIn ? json(me()) : problem(401);
    if (url.pathname === "/api/v1/discover") return json({ links: { console: "http://lerryn.test" }, capabilities: { login_methods: ["password"] } });
    if (url.pathname === "/api/v1/auth/logout") { signedIn = false; return json({}); }
    if (url.pathname === "/api/v1/auth/login") {
      const body = await req.json() as { username: string };
      owner = body.username;
      signedIn = true;
      return json(me());
    }
    if (url.pathname === "/api/v1/me/tokens") {
      expect(url.searchParams.get("limit")).toBe("50");
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      const response = await options.read?.(cursor, owner);
      if (response) return response;
      const all = lists[owner as keyof typeof lists];
      const offset = Number(cursor ?? 0);
      return json({ items: all.slice(offset, offset + 50), next_cursor: offset + 50 < all.length ? String(offset + 50) : null });
    }
    if (url.pathname.startsWith("/api/v1/me/tokens/") && req.method === "DELETE") {
      const id = url.pathname.split("/").at(-1)!;
      revokedIds.push(id);
      const item = lists[owner as keyof typeof lists].find((item) => item.id === id)!;
      item.revoked_at = "2026-11-04T00:00:00Z";
      return new Response(null, { status: 204 });
    }
    return json({ items: [], next_cursor: null });
  } });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(queryClient);
  const router = createAppRouter({ api, queryClient });
  router.update({ context: { api, queryClient }, history: createMemoryHistory({ initialEntries: ["/settings/tokens"] }) });
  render(
    <LerrynThemeProvider><I18nProvider locale={options.locale ?? "zh-CN"}><QueryClientProvider client={queryClient}><ApiProvider client={api}>
      <RouterProvider router={router} />
    </ApiProvider></QueryClientProvider></I18nProvider></LerrynThemeProvider>,
  );
  return { queryClient, router, cursors, revokedIds };
}

function sidebarCount() {
  const settings = screen.getByRole("region", { name: /^(个人设置|Settings)$/ });
  const link = within(settings).getByRole("link", { name: /^(访问令牌|Access tokens)/ });
  return link.querySelector(".li-meta")?.textContent;
}

async function revokeToken(user: ReturnType<typeof userEvent.setup>, name: string) {
  const board = screen.getByRole("main", { name: "访问令牌" });
  await user.click(within(board).getByRole("button", { name: `撤销 ${name}` }));
  const dialog = await screen.findByRole("dialog", { name: `撤销 ${name}` });
  await user.click(within(dialog).getByRole("button", { name: "撤销" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
}

describe("settings access-token count", () => {
  it.each(["zh-CN", "en"] as const)("marks zero as incomplete until the oldest active token is loaded, locale=%s", async (locale) => {
    const { cursors } = renderTokens([...revoked(), token("oldest-active")], { locale });
    await screen.findByText("revoked-49");
    expect(sidebarCount()).toBe("0+");
    expect(cursors).toEqual([null]);
    expect(screen.queryByText("oldest-active")).toBeNull();
    await userEvent.setup().click(screen.getByRole("button", { name: locale === "en" ? "Load more" : "继续读取" }));
    await screen.findByText("oldest-active");
    expect(sidebarCount()).toBe("1");
    expect(cursors).toEqual([null, "50"]);
    expect(screen.queryByRole("button", { name: locale === "en" ? "Load more" : "继续读取" })).toBeNull();
  });

  it("filters expired and revoked tokens while refreshing partial and exact counts after revocation", async () => {
    const items = [token("current"), token("expired", "expired"), ...revoked().slice(0, 48), token("older")];
    const { revokedIds } = renderTokens(items);
    await screen.findByText("current");
    expect(sidebarCount()).toBe("1+");
    const user = userEvent.setup();
    await revokeToken(user, "current");
    await waitFor(() => expect(sidebarCount()).toBe("0+"));
    await user.click(screen.getByRole("button", { name: "继续读取" }));
    await screen.findByText("older");
    expect(sidebarCount()).toBe("1");
    await revokeToken(user, "older");
    await waitFor(() => expect(sidebarCount()).toBe("0"));
    expect(revokedIds).toEqual(["current", "older"]);
  });

  it("does not advertise a total while the first page failed", async () => {
    let fail = true;
    renderTokens([...revoked(), token("older")], { read: async () => fail ? problem() : undefined });
    const retry = await screen.findByRole("button", { name: "重试" });
    expect(sidebarCount()).toBe("");
    fail = false;
    await userEvent.setup().click(retry);
    await screen.findByText("revoked-49");
    expect(sidebarCount()).toBe("0+");
  });

  it("retains the partial count after a later-page failure and completes it after retry", async () => {
    let fail = true;
    const { cursors } = renderTokens([token("current"), ...revoked().slice(0, 49), token("older")], {
      read: async (cursor) => cursor && fail ? problem() : undefined,
    });
    await screen.findByText("current");
    expect(sidebarCount()).toBe("1+");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "继续读取" }));
    await screen.findByRole("button", { name: "重试" });
    expect(sidebarCount()).toBe("1+");
    fail = false;
    await user.click(screen.getByRole("button", { name: "继续读取" }));
    await screen.findByText("older");
    expect(sidebarCount()).toBe("2");
    expect(cursors).toEqual([null, "50", "50"]);
  });

  it("discards an old later page after logout and a different account logs in", async () => {
    let finish: ((response: Response) => void) | undefined;
    const { router, queryClient } = renderTokens([...revoked(), token("alice-private")], {
      read: async (cursor, owner) => owner === "alice" && cursor ? new Promise<Response>((resolve) => { finish = resolve; }) : undefined,
    });
    await screen.findByText("revoked-49");
    expect(sidebarCount()).toBe("0+");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "继续读取" }));
    await waitFor(() => expect(finish).toBeDefined());
    await act(() => router.navigate({ to: "/settings/account" }));
    await user.click(await screen.findByRole("button", { name: "退出登录" }));
    await screen.findByRole("heading", { name: "登录" });
    await act(() => router.navigate({ to: "/settings/tokens" }));
    await user.type(await screen.findByLabelText("用户名"), "bob");
    await user.type(screen.getByLabelText("密码", { selector: "input" }), "correct horse");
    await user.click(screen.getByRole("button", { name: "登录" }));
    await screen.findByText("bob-only");
    expect(sidebarCount()).toBe("1");
    await act(async () => finish!(json({ items: [token("alice-private")], next_cursor: null })));
    expect(sidebarCount()).toBe("1");
    expect(screen.queryByText("alice-private")).toBeNull();
    expect(JSON.stringify(queryClient.getQueryData(queryKeys.tokens))).not.toContain("alice-private");
  });
});
