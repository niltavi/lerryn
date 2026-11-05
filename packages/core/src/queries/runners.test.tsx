import { createLerrynClient, type Schemas } from "@lerryn/api";
import { QueryClient, QueryClientProvider, type InfiniteData } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiProvider } from "../client";
import { useLogin, useLogout, type Me } from "./account";
import { useRunners, useUpdateRunner, type Runner } from "./admin";
import { queryKeys } from "./keys";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status: number, code: string) => json({ type: `urn:lerryn:error:${code}`, title: code, status, code, request_id: "runner-request" }, status);
const page = (items: Runner[], next_cursor: string | null = null) => json({ items, next_cursor });
const runner = (id: string, status: Runner["status"] = "disabled"): Runner => ({
  id, name: id, status, labels: [], slots: 4, running: 0, version: null,
  last_seen_at: "2026-11-04T00:00:00Z", created_at: "2026-11-04T00:00:00Z",
  disabled_at: status === "disabled" ? "2026-11-04T00:00:00Z" : null,
});
const me = (id: string): Me => ({
  id, username: id, display_name: id, email: null, is_admin: true, personal_project_id: "project",
  created_at: "2026-11-04T00:00:00Z", locale: null, timezone: null, has_password: true, identities: [],
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
  vi.useRealTimers();
});

describe("runner registry pages", () => {
  it.each([201, 401])("keeps the current runner reachable after %i registrations without eager paging", async (count) => {
    const rows = Array.from({ length: count }, (_, i) => runner(String(i)));
    rows[count - 1] = runner("current", "online");
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      expect(url.pathname).toBe("/api/v1/admin/runners");
      expect(url.searchParams.get("limit")).toBe("200");
      expect(url.searchParams.has("status")).toBe(false);
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      const offset = Number(cursor ?? 0);
      return page(rows.slice(offset, offset + 200), offset + 200 < count ? String(offset + 200) : null);
    });
    const { result } = renderHook(() => ({ ...useRunners() }), { wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(200));
    expect(cursors).toEqual([null]);
    expect(result.current.hasNextPage).toBe(true);
    while (result.current.hasNextPage) {
      await act(() => result.current.fetchNextPage({ cancelRefetch: false }));
      await waitFor(() => expect(result.current.isFetching).toBe(false));
    }
    expect(result.current.data?.items).toEqual(rows);
    expect(result.current.data?.next_cursor).toBeNull();
    expect(cursors).toEqual(Array.from({ length: Math.ceil(count / 200) }, (_, i) => i ? String(i * 200) : null));
  });

  it("keeps loaded runners on a later-page failure and retries the original cursor", async () => {
    let fail = true;
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor) return page([runner("old")], "next");
      return fail ? problem(403, "permission_denied") : page([runner("current", "online")]);
    });
    const { result } = renderHook(() => ({ ...useRunners() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => result.current.fetchNextPage({ cancelRefetch: false }));
    await waitFor(() => expect(result.current.isFetchNextPageError).toBe(true));
    expect(result.current.data?.items.map((r) => r.id)).toEqual(["old"]);
    expect(result.current.hasNextPage).toBe(true);
    fail = false;
    await act(() => result.current.fetchNextPage({ cancelRefetch: false }));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((r) => r.id)).toEqual(["old", "current"]);
    expect(cursors).toEqual([null, "next", "next"]);
  });

  it("represents an empty registry without a further page", async () => {
    const { wrapper } = setup(async () => page([]));
    const { result } = renderHook(() => ({ ...useRunners() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ items: [], next_cursor: null });
    expect(result.current.hasNextPage).toBe(false);
  });

  it.each([
    { status: 401, code: "unauthenticated", calls: 1 },
    { status: 404, code: "not_found", calls: 1 },
    { status: 503, code: "internal_error", calls: 3 },
  ])("preserves API errors and retry behavior for $status", async ({ status, code, calls }) => {
    let attempts = 0;
    const { wrapper } = setup(async () => { attempts++; return problem(status, code); });
    const { result } = renderHook(() => ({ ...useRunners() }), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toMatchObject({ status, code, requestId: "runner-request" });
    expect(attempts).toBe(calls);
  });

  it("refreshes only loaded pages every ten seconds and follows their fresh cursor", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let generation = 0;
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor) return page([runner(`head-${++generation}`)], `next-${generation}`);
      return page([runner(`tail-${generation}`, "online")], "unread");
    });
    const { result } = renderHook(() => ({ ...useRunners() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => result.current.fetchNextPage({ cancelRefetch: false }));
    await waitFor(() => expect(result.current.data?.items).toHaveLength(2));
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    await waitFor(() => expect(result.current.data?.items.map((r) => r.id)).toEqual(["head-2", "tail-2"]));
    expect(cursors).toEqual([null, "next-1", null, "next-2"]);
    expect(result.current.hasNextPage).toBe(true);
  });

  it("refreshes every loaded page after disabling a runner", async () => {
    let disabled = false;
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      if (req.method === "PATCH") {
        expect(await req.json()).toEqual({ disabled: true });
        disabled = true;
        return json(runner("current"));
      }
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      return cursor ? page([runner("current", disabled ? "disabled" : "online")]) : page([runner("old")], "next");
    });
    const { result } = renderHook(() => ({ registry: { ...useRunners() }, update: useUpdateRunner() }), { wrapper });
    await waitFor(() => expect(result.current.registry.isSuccess).toBe(true));
    await act(() => result.current.registry.fetchNextPage({ cancelRefetch: false }));
    await waitFor(() => expect(result.current.registry.data?.items).toHaveLength(2));
    await act(() => result.current.update.mutateAsync({ runnerId: "current", disabled: true }));
    await waitFor(() => expect(result.current.registry.data?.items.at(-1)?.status).toBe("disabled"));
    expect(cursors).toEqual([null, "next", null, "next"]);
  });

  it.each(["cancel", "unmount"])("aborts a pending next page on %s without caching its late response", async (action) => {
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      if (!new URL(req.url).searchParams.has("cursor")) return page([runner("old")], "next");
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    const hook = renderHook(() => ({ ...useRunners() }), { wrapper });
    await waitFor(() => expect(hook.result.current.isSuccess).toBe(true));
    act(() => { void hook.result.current.fetchNextPage({ cancelRefetch: false }); });
    await waitFor(() => expect(finish).toBeDefined());
    if (action === "cancel") await act(() => queryClient.cancelQueries({ queryKey: queryKeys.runners }));
    else hook.unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => finish!(page([runner("late")])));
    const cached = queryClient.getQueryData<InfiniteData<Schemas["RunnerPage"]>>(queryKeys.runners);
    expect(cached?.pages.flatMap((p) => p.items.map((r) => r.id))).toEqual(["old"]);
  });

  it("does not restore a previous admin's registry after logout and login", async () => {
    let user = "alice";
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      if (url.pathname.endsWith("/logout")) { user = ""; return json({}); }
      if (url.pathname.endsWith("/login")) { user = "bob"; return json(me(user)); }
      if (!user) return problem(401, "unauthenticated");
      if (user === "bob") return page([runner("bob")]);
      if (!url.searchParams.has("cursor")) return page([runner("alice")], "next");
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    const hook = renderHook(() => ({ registry: { ...useRunners() }, login: useLogin(), logout: useLogout() }), { wrapper });
    await waitFor(() => expect(hook.result.current.registry.isSuccess).toBe(true));
    act(() => { void hook.result.current.registry.fetchNextPage({ cancelRefetch: false }); });
    await waitFor(() => expect(finish).toBeDefined());
    await act(() => hook.result.current.logout.mutateAsync());
    expect(signal?.aborted).toBe(true);
    await act(() => hook.result.current.login.mutateAsync({ username: "bob", password: "password" }));
    await waitFor(() => expect(hook.result.current.registry.data?.items.map((r) => r.id)).toEqual(["bob"]));
    await act(async () => finish!(page([runner("alice-late")])));
    expect(hook.result.current.registry.data?.items.map((r) => r.id)).toEqual(["bob"]);
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(me("bob"));
  });
});
