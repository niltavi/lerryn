import { createLerrynClient, type Schemas } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { ApiProvider } from "../client";
import { useLogin, useLogout, type Me } from "./account";
import { queryKeys } from "./keys";
import { useCredentials } from "./settings";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status: number, code: string) => json({ type: `urn:lerryn:error:${code}`, title: code, status, code, request_id: "req_1" }, status);
const page = (items: Schemas["Credential"][], next_cursor: string | null = null) => json({ items, next_cursor });
const credential = (id: string): Schemas["Credential"] => ({
  id, name: id, purpose: "API access", fields: ["TOKEN"], status: "active", version: 1,
  expires_at: null, revoked_at: null, last_used_at: null, grants: [],
  created_at: "2026-11-03T00:00:00Z", updated_at: "2026-11-03T00:00:00Z",
});
const me = (id: string): Me => ({
  id, username: id, display_name: id, email: null, is_admin: false, personal_project_id: "project",
  created_at: "2026-11-03T00:00:00Z", locale: null, timezone: null, has_password: true, identities: [],
});

function setup(fetch: (req: Request) => Promise<Response>) {
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}><ApiProvider client={api}>{children}</ApiProvider></QueryClientProvider>
  );
  return { queryClient, wrapper };
}

afterEach(cleanup);

describe("paged own credentials", () => {
  it.each([201, 401])("makes all %i credentials reachable through forward cursors", async (count) => {
    const credentials = Array.from({ length: count }, (_, i) => credential(`CRED_${String(i).padStart(3, "0")}`));
    credentials[0] = { ...credentials[0]!, status: "revoked", revoked_at: "2026-11-03T00:00:00Z" };
    credentials[1] = { ...credentials[1]!, status: "expired", expires_at: "2026-11-02T00:00:00Z" };
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      expect(url.searchParams.get("limit")).toBe("200");
      expect(url.searchParams.has("status")).toBe(false);
      cursors.push(url.searchParams.get("cursor"));
      const offset = Number(url.searchParams.get("cursor") ?? 0);
      return page(credentials.slice(offset, offset + 200), offset + 200 < count ? String(offset + 200) : null);
    });
    const { result } = renderHook(() => ({ ...useCredentials() }), { wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(200));
    expect(result.current.hasNextPage).toBe(true);
    while (result.current.hasNextPage) await act(() => result.current.fetchNextPage());
    expect(result.current.data?.items).toEqual(credentials);
    expect(cursors).toEqual(Array.from({ length: Math.ceil(count / 200) }, (_, i) => i ? String(i * 200) : null));
  });

  it("keeps loaded credentials when a later page fails and retries that cursor", async () => {
    let fail = true;
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor) return page([credential("first")], "next");
      return fail ? problem(403, "permission_denied") : page([credential("last")]);
    });
    const { result } = renderHook(() => ({ ...useCredentials() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => result.current.fetchNextPage());
    await waitFor(() => expect(result.current.isFetchNextPageError).toBe(true));
    expect(result.current.data?.items.map((c) => c.id)).toEqual(["first"]);
    expect(result.current.hasNextPage).toBe(true);
    fail = false;
    await act(() => result.current.fetchNextPage());
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((c) => c.id)).toEqual(["first", "last"]);
    expect(result.current.hasNextPage).toBe(false);
    expect(cursors).toEqual([null, "next", "next"]);
  });

  it("recomputes later cursors when invalidation inserts a credential before loaded pages", async () => {
    let credentials = Array.from({ length: 201 }, (_, i) => credential(String(i)));
    const cursors: (string | null)[] = [];
    const { queryClient, wrapper } = setup(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      const offset = cursor ? credentials.findIndex((c) => c.id === cursor) + 1 : 0;
      const items = credentials.slice(offset, offset + 200);
      return page(items, offset + 200 < credentials.length ? items.at(-1)!.id : null);
    });
    const { result } = renderHook(() => ({ ...useCredentials() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => result.current.fetchNextPage());
    await waitFor(() => expect(result.current.data?.items).toHaveLength(201));
    credentials = [credential("new"), ...credentials];
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.credentials }));
    await waitFor(() => expect(result.current.data?.items).toEqual(credentials));
    expect(cursors).toEqual([null, "199", null, "198"]);
  });

  it.each(["cancel", "unmount"])("discards an in-flight later page after %s", async (action) => {
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      if (!new URL(req.url).searchParams.has("cursor")) return page([credential("first")], "next");
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    const { result, unmount } = renderHook(() => ({ ...useCredentials() }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    act(() => { void result.current.fetchNextPage(); });
    await waitFor(() => expect(finish).toBeDefined());
    if (action === "cancel") await act(() => queryClient.cancelQueries({ queryKey: queryKeys.credentials }));
    else unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => finish!(page([credential("old-last")])));
    const cached = queryClient.getQueryData<{ pages: Schemas["CredentialPage"][] }>(queryKeys.credentials);
    expect(cached?.pages.flatMap((p) => p.items.map((c) => c.id))).toEqual(["first"]);
  });

  it("does not restore another user's later page after logout and login clear the cache", async () => {
    let user = "alice";
    let finishOld: ((response: Response) => void) | undefined;
    let oldSignal: AbortSignal | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      if (url.pathname.endsWith("/logout")) { user = ""; return json({}); }
      if (url.pathname.endsWith("/login")) { user = "bob"; return json(me(user)); }
      if (!user) return problem(401, "unauthenticated");
      if (user === "bob") return page([credential("bob")]);
      if (!url.searchParams.has("cursor")) return page([credential("alice")], "alice-next");
      oldSignal = req.signal;
      return new Promise<Response>((resolve) => { finishOld = resolve; });
    });
    const { result } = renderHook(() => ({ credentials: { ...useCredentials() }, login: useLogin(), logout: useLogout() }), { wrapper });
    await waitFor(() => expect(result.current.credentials.isSuccess).toBe(true));
    act(() => { void result.current.credentials.fetchNextPage(); });
    await waitFor(() => expect(finishOld).toBeDefined());
    await act(() => result.current.logout.mutateAsync());
    expect(oldSignal?.aborted).toBe(true);
    await act(() => result.current.login.mutateAsync({ username: "bob", password: "password" }));
    await waitFor(() => expect(result.current.credentials.data?.items.map((c) => c.id)).toEqual(["bob"]));
    await act(async () => finishOld!(page([credential("alice-last")])));
    expect(result.current.credentials.data?.items.map((c) => c.id)).toEqual(["bob"]);
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(me("bob"));
    expect(queryClient.getQueryData<{ pages: Schemas["CredentialPage"][] }>(queryKeys.credentials)?.pages).toHaveLength(1);
  });
});
