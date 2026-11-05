import { createLerrynClient, type Schemas } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { ApiProvider } from "../client";
import { useLogin, useLogout, type Me } from "./account";
import { queryKeys } from "./keys";
import { usePendingCredentialRequests } from "./settings";

type FillRequest = Schemas["CredentialRequest"];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status: number, code: string) => json({ type: `urn:lerryn:error:${code}`, title: code, status, code, request_id: "req_1" }, status);
const page = (items: FillRequest[], next_cursor: string | null = null) => json({ items, next_cursor });
const request = (id: string): FillRequest => ({
  id, name: id, purpose: "API access", status: "pending", url: `http://lerryn.test/c/${id}`,
  task: null, credential_id: null, client: null, fields: [],
  created_at: "2026-11-03T00:00:00Z", expires_at: "2099-11-04T00:00:00Z", fulfilled_at: null,
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

describe("paged pending credential requests", () => {
  it.each([201, 401])("reads one page by default and makes all %i pending requests reachable", async (count) => {
    const requests = Array.from({ length: count }, (_, i) => request(`REQUEST_${i}`));
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      expect(url.searchParams.get("status")).toBe("pending");
      expect(url.searchParams.get("limit")).toBe("200");
      cursors.push(url.searchParams.get("cursor"));
      const offset = Number(url.searchParams.get("cursor") ?? 0);
      return page(requests.slice(offset, offset + 200), offset + 200 < count ? String(offset + 200) : null);
    });
    const { result } = renderHook(() => ({ ...usePendingCredentialRequests() }), { wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(200));
    expect(cursors).toEqual([null]);
    expect(result.current.hasNextPage).toBe(true);
    while (result.current.hasNextPage) await act(() => result.current.fetchNextPage());
    expect(result.current.data?.items).toEqual(requests);
    expect(cursors).toEqual(Array.from({ length: Math.ceil(count / 200) }, (_, i) => i ? String(i * 200) : null));
  });

  it("retains loaded requests when the next page fails and retries its cursor", async () => {
    let fail = true;
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor) return page([request("first")], "next");
      return fail ? problem(403, "permission_denied") : page([request("last")]);
    });
    const { result } = renderHook(() => ({ ...usePendingCredentialRequests() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => result.current.fetchNextPage());
    await waitFor(() => expect(result.current.isFetchNextPageError).toBe(true));
    expect(result.current.data?.items.map((r) => r.id)).toEqual(["first"]);
    expect(result.current.hasNextPage).toBe(true);
    fail = false;
    await act(() => result.current.fetchNextPage());
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((r) => r.id)).toEqual(["first", "last"]);
    expect(result.current.hasNextPage).toBe(false);
    expect(cursors).toEqual([null, "next", "next"]);
  });

  it("recomputes loaded cursors when a new request arrives before the first page", async () => {
    let requests = Array.from({ length: 201 }, (_, i) => request(`REQUEST_${i}`));
    const cursors: (string | null)[] = [];
    const { queryClient, wrapper } = setup(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      const offset = cursor ? requests.findIndex((r) => r.id === cursor) + 1 : 0;
      const items = requests.slice(offset, offset + 200);
      return page(items, offset + 200 < requests.length ? items.at(-1)!.id : null);
    });
    const { result } = renderHook(() => ({ ...usePendingCredentialRequests() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => result.current.fetchNextPage());
    await waitFor(() => expect(result.current.data?.items).toHaveLength(201));
    requests = [request("new"), ...requests];
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.credentialRequests }));
    await waitFor(() => expect(result.current.data?.items).toEqual(requests));
    expect(cursors).toEqual([null, "REQUEST_199", null, "REQUEST_198"]);
  });

  it("drops fulfilled, cancelled and expired requests after refreshing loaded pages", async () => {
    const requests = Array.from({ length: 201 }, (_, i) => request(`REQUEST_${i}`));
    const cursors: (string | null)[] = [];
    const { queryClient, wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      expect(url.searchParams.get("status")).toBe("pending");
      const current = requests.filter((r) => r.status === "pending" && Date.parse(r.expires_at) > Date.now());
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      const offset = cursor ? current.findIndex((r) => r.id === cursor) + 1 : 0;
      const items = current.slice(offset, offset + 200);
      return page(items, offset + 200 < current.length ? items.at(-1)!.id : null);
    });
    const { result } = renderHook(() => ({ ...usePendingCredentialRequests() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => result.current.fetchNextPage());
    await waitFor(() => expect(result.current.data?.items).toHaveLength(201));
    requests[0] = { ...requests[0]!, status: "fulfilled" };
    requests[1] = { ...requests[1]!, status: "cancelled" };
    requests[2] = { ...requests[2]!, expires_at: "2000-01-01T00:00:00Z" };
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.credentialRequests }));
    await waitFor(() => expect(result.current.data?.items).toEqual(requests.slice(3)));
    expect(result.current.hasNextPage).toBe(false);
    expect(cursors).toEqual([null, "REQUEST_199", null]);
  });

  it.each(["cancel", "unmount"])("discards a pending later page after %s", async (action) => {
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      if (!new URL(req.url).searchParams.has("cursor")) return page([request("first")], "next");
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    const { result, unmount } = renderHook(() => ({ ...usePendingCredentialRequests() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    act(() => { void result.current.fetchNextPage(); });
    await waitFor(() => expect(finish).toBeDefined());
    if (action === "cancel") await act(() => queryClient.cancelQueries({ queryKey: queryKeys.credentialRequests }));
    else unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => finish!(page([request("old-last")])));
    const cached = queryClient.getQueryData<{ pages: Schemas["CredentialRequestPage"][] }>(queryKeys.credentialRequests);
    expect(cached?.pages.flatMap((p) => p.items.map((r) => r.id))).toEqual(["first"]);
  });

  it("does not restore a previous user's page after logout and a new login", async () => {
    let user = "alice";
    let finishOld: ((response: Response) => void) | undefined;
    let oldSignal: AbortSignal | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      if (url.pathname.endsWith("/logout")) { user = ""; return json({}); }
      if (url.pathname.endsWith("/login")) { user = "bob"; return json(me(user)); }
      if (!user) return problem(401, "unauthenticated");
      if (user === "bob") return page([request("bob")]);
      if (!url.searchParams.has("cursor")) return page([request("alice")], "alice-next");
      oldSignal = req.signal;
      return new Promise<Response>((resolve) => { finishOld = resolve; });
    });
    const { result } = renderHook(() => ({ requests: { ...usePendingCredentialRequests() }, login: useLogin(), logout: useLogout() }), { wrapper });
    await waitFor(() => expect(result.current.requests.isSuccess).toBe(true));
    act(() => { void result.current.requests.fetchNextPage(); });
    await waitFor(() => expect(finishOld).toBeDefined());
    await act(() => result.current.logout.mutateAsync());
    expect(oldSignal?.aborted).toBe(true);
    await act(() => result.current.login.mutateAsync({ username: "bob", password: "password" }));
    await waitFor(() => expect(result.current.requests.data?.items.map((r) => r.id)).toEqual(["bob"]));
    await act(async () => finishOld!(page([request("alice-last")])));
    expect(result.current.requests.data?.items.map((r) => r.id)).toEqual(["bob"]);
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(me("bob"));
    expect(queryClient.getQueryData<{ pages: Schemas["CredentialRequestPage"][] }>(queryKeys.credentialRequests)?.pages).toHaveLength(1);
  });
});
