import { createLerrynClient, type Schemas } from "@lerryn/api";
import { QueryClient, QueryClientProvider, type InfiniteData } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { ApiProvider } from "../client";
import { useLogin, useLogout, type Me } from "./account";
import { queryKeys } from "./keys";
import { useNotificationDeliveries, useTestNotificationChannel } from "./settings";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status: number, code: string) => json({ type: `urn:lerryn:error:${code}`, title: code, status, code, request_id: "delivery-request" }, status);
const page = (items: Schemas["NotificationDelivery"][], next_cursor: string | null = null) => json({ items, next_cursor });
const delivery = (id: string): Schemas["NotificationDelivery"] => ({
  id, event_type: "test", status: "delivered", attempts: 1, http_status: 200, error: null,
  task: null, run_id: null, merged_count: 1, created_at: "2026-11-03T00:00:00Z",
  last_attempt_at: "2026-11-03T00:00:00Z", delivered_at: "2026-11-03T00:00:00Z",
});
const me = (id: string): Me => ({
  id, username: id, display_name: id, email: null, is_admin: false, personal_project_id: "project",
  created_at: "2026-11-03T00:00:00Z", locale: null, timezone: null, has_password: true, identities: [],
});
const clients: QueryClient[] = [];

function setup(fetch: (req: Request) => Promise<Response>) {
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
  clients.push(queryClient);
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}><ApiProvider client={api}>{children}</ApiProvider></QueryClientProvider>
  );
  return { queryClient, wrapper };
}

afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
});

describe("notification delivery pages", () => {
  it.each([51, 151])("keeps all %i retained records reachable without eagerly reading later pages", async (count) => {
    const rows = Array.from({ length: count }, (_, i) => delivery(String(i)));
    rows[count - 1] = { ...rows[count - 1]!, status: "failed", http_status: 503, error: "receiver unavailable", delivered_at: null };
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      expect(url.pathname).toBe("/api/v1/notification-channels/channel-a/deliveries");
      expect(url.searchParams.get("limit")).toBe("50");
      expect(url.searchParams.has("status")).toBe(false);
      cursors.push(url.searchParams.get("cursor"));
      const offset = Number(url.searchParams.get("cursor") ?? 0);
      return page(rows.slice(offset, offset + 50), offset + 50 < count ? String(offset + 50) : null);
    });
    const { result } = renderHook(() => ({ ...useNotificationDeliveries("channel-a") }), { wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(50));
    expect(cursors).toEqual([null]);
    expect(result.current.hasNextPage).toBe(true);
    while (result.current.hasNextPage) await act(() => result.current.fetchNextPage({ cancelRefetch: false }));
    expect(result.current.data?.items).toEqual(rows);
    expect(result.current.data?.next_cursor).toBeNull();
    expect(cursors).toEqual(Array.from({ length: Math.ceil(count / 50) }, (_, i) => i ? String(i * 50) : null));
  });

  it("preserves loaded records after a later-page failure and retries its cursor", async () => {
    let fail = true;
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor) return page([delivery("first")], "next");
      return fail ? problem(403, "permission_denied") : page([delivery("last")]);
    });
    const { result } = renderHook(() => ({ ...useNotificationDeliveries("channel-a") }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => result.current.fetchNextPage({ cancelRefetch: false }));
    await waitFor(() => expect(result.current.isFetchNextPageError).toBe(true));
    expect(result.current.data?.items.map((d) => d.id)).toEqual(["first"]);
    expect(result.current.data?.next_cursor).toBe("next");
    fail = false;
    await act(() => result.current.fetchNextPage({ cancelRefetch: false }));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((d) => d.id)).toEqual(["first", "last"]);
    expect(cursors).toEqual([null, "next", "next"]);
  });

  it("recomputes loaded cursors when a test notification inserts a new first record", async () => {
    let rows = Array.from({ length: 51 }, (_, i) => delivery(String(i)));
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      if (req.method === "POST") {
        rows = [delivery("new"), ...rows];
        return json(rows[0]);
      }
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      const offset = cursor ? rows.findIndex((d) => d.id === cursor) + 1 : 0;
      const items = rows.slice(offset, offset + 50);
      return page(items, offset + 50 < rows.length ? items.at(-1)!.id : null);
    });
    const { result } = renderHook(() => ({ history: { ...useNotificationDeliveries("channel-a") }, test: useTestNotificationChannel() }), { wrapper });
    await waitFor(() => expect(result.current.history.isSuccess).toBe(true));
    await act(() => result.current.history.fetchNextPage({ cancelRefetch: false }));
    await waitFor(() => expect(result.current.history.data?.items).toHaveLength(51));
    await act(() => result.current.test.mutateAsync("channel-a"));
    await waitFor(() => expect(result.current.history.data?.items).toEqual(rows));
    // Existing channel and history invalidations can both restart the head.
    // Later pages must follow its new cursor after the inserted record.
    expect(cursors.slice(0, 2)).toEqual([null, "49"]);
    expect(cursors.slice(2).filter((cursor) => cursor !== null)).toEqual(["48"]);
  });

  it.each([null, ""])("does not request history without an open channel: %s", async (channelId) => {
    let calls = 0;
    const { wrapper } = setup(async () => { calls++; return page([]); });
    const { result } = renderHook(() => ({ ...useNotificationDeliveries(channelId) }), { wrapper });
    expect(result.current.fetchStatus).toBe("idle");
    expect(calls).toBe(0);
  });

  it("represents a successful empty history without a further page", async () => {
    const { wrapper } = setup(async () => page([]));
    const { result } = renderHook(() => ({ ...useNotificationDeliveries("channel-a") }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ items: [], next_cursor: null });
    expect(result.current.hasNextPage).toBe(false);
  });

  it.each([
    { status: 404, code: "not_found", calls: 1 },
    { status: 410, code: "expired", calls: 1 },
    { status: 503, code: "internal_error", calls: 3 },
  ])("retains API error and retry behavior for $status", async ({ status, code, calls }) => {
    let attempts = 0;
    const { wrapper } = setup(async () => { attempts++; return problem(status, code); });
    const { result } = renderHook(() => ({ ...useNotificationDeliveries("channel-a") }), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toMatchObject({ status, code, requestId: "delivery-request" });
    expect(attempts).toBe(calls);
    expect(result.current.data).toBeUndefined();
  });

  it.each(["cancel", "unmount", "close"])("aborts an unfinished next page on %s and discards its late response", async (action) => {
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      if (!new URL(req.url).searchParams.has("cursor")) return page([delivery("first")], "next");
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    const hook = renderHook((channelId: string | null) => ({ ...useNotificationDeliveries(channelId) }), { initialProps: "channel-a" as string | null, wrapper });
    await waitFor(() => expect(hook.result.current.isSuccess).toBe(true));
    act(() => { void hook.result.current.fetchNextPage({ cancelRefetch: false }); });
    await waitFor(() => expect(finish).toBeDefined());
    if (action === "cancel") await act(() => queryClient.cancelQueries({ queryKey: queryKeys.deliveries("channel-a") }));
    else if (action === "unmount") hook.unmount();
    else hook.rerender(null);
    expect(signal?.aborted).toBe(true);
    await act(async () => finish!(page([delivery("late")])));
    const cached = queryClient.getQueryData<InfiniteData<Schemas["NotificationDeliveryPage"]>>(queryKeys.deliveries("channel-a"));
    expect(cached?.pages.flatMap((p) => p.items.map((d) => d.id))).toEqual(["first"]);
  });

  it("isolates another channel from an older channel's pending response", async () => {
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    const { wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      if (url.pathname.includes("channel-b")) return page([delivery("channel-b-first")]);
      if (!url.searchParams.has("cursor")) return page([delivery("channel-a-first")], "next");
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    const hook = renderHook((id: string) => ({ ...useNotificationDeliveries(id) }), { initialProps: "channel-a", wrapper });
    await waitFor(() => expect(hook.result.current.isSuccess).toBe(true));
    act(() => { void hook.result.current.fetchNextPage({ cancelRefetch: false }); });
    await waitFor(() => expect(finish).toBeDefined());
    hook.rerender("channel-b");
    await waitFor(() => expect(hook.result.current.data?.items.map((d) => d.id)).toEqual(["channel-b-first"]));
    expect(signal?.aborted).toBe(true);
    await act(async () => finish!(page([delivery("channel-a-late")])));
    expect(hook.result.current.data?.items.map((d) => d.id)).toEqual(["channel-b-first"]);
  });

  it("does not restore another user's history after logout and login clear the cache", async () => {
    let user = "alice";
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      if (url.pathname.endsWith("/logout")) { user = ""; return json({}); }
      if (url.pathname.endsWith("/login")) { user = "bob"; return json(me(user)); }
      if (!user) return problem(401, "unauthenticated");
      if (url.pathname.includes("channel-b") && user === "bob") return page([delivery("bob")]);
      if (user !== "alice") return problem(404, "not_found");
      if (!url.searchParams.has("cursor")) return page([delivery("alice")], "alice-next");
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    const hook = renderHook((id: string) => ({ history: { ...useNotificationDeliveries(id) }, login: useLogin(), logout: useLogout() }), { initialProps: "channel-a", wrapper });
    await waitFor(() => expect(hook.result.current.history.isSuccess).toBe(true));
    act(() => { void hook.result.current.history.fetchNextPage({ cancelRefetch: false }); });
    await waitFor(() => expect(finish).toBeDefined());
    await act(() => hook.result.current.logout.mutateAsync());
    expect(signal?.aborted).toBe(true);
    await act(() => hook.result.current.login.mutateAsync({ username: "bob", password: "password" }));
    hook.rerender("channel-b");
    await waitFor(() => expect(hook.result.current.history.data?.items.map((d) => d.id)).toEqual(["bob"]));
    await act(async () => finish!(page([delivery("alice-late")])));
    expect(hook.result.current.history.data?.items.map((d) => d.id)).toEqual(["bob"]);
    expect(queryClient.getQueryData(queryKeys.deliveries("channel-a"))).toBeUndefined();
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(me("bob"));
  });
});
