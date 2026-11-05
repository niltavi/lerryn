import { createLerrynClient, type LerrynClient } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, renderHook, waitFor } from "@testing-library/react";
import { StrictMode, type ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { ApiProvider } from "../client";
import { useLogin, useLogout, useMe, useRegister, type Me } from "./account";
import { useUpdateAdminSettings } from "./admin";
import { queryKeys } from "./keys";
import { useStartRun } from "./runs";
import { useRequestCredentialUpdate, useUpdateMe } from "./settings";

const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
const alice: Me = {
  id: "alice", username: "alice", display_name: "Alice", email: null, is_admin: false,
  personal_project_id: "project", created_at: "2026-11-03T00:00:00Z", locale: null,
  timezone: null, has_password: true, identities: [],
};
const bob: Me = { ...alice, id: "bob", username: "bob", display_name: "Bob" };

function setup(fetch: (req: Request) => Promise<Response>) {
  let client = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(queryKeys.me, alice);
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>{children}</ApiProvider>
    </QueryClientProvider>
  );
  return { queryClient, wrapper, setClient: (next: LerrynClient) => { client = next; } };
}

afterEach(cleanup);

describe("session-scoped cache writes", () => {
  it("still applies a profile update in the current session", async () => {
    const updated = { ...alice, display_name: "Edited" };
    const { queryClient, wrapper } = setup(async () => json(updated));
    const { result } = renderHook(() => useUpdateMe(), { wrapper });
    await act(() => result.current.mutateAsync({ display_name: "Edited" }));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(updated);
  });

  it("still applies current mutations after StrictMode replays provider effects", async () => {
    const updated = { ...alice, display_name: "Edited" };
    const { queryClient, wrapper: Wrapper } = setup(async () => json(updated));
    const wrapper = ({ children }: { children: ReactNode }) => <StrictMode><Wrapper>{children}</Wrapper></StrictMode>;
    const { result } = renderHook(() => useUpdateMe(), { wrapper });
    await act(() => result.current.mutateAsync({ display_name: "Edited" }));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(updated);
  });

  it("lets a mounted screen write after authentication renews its session", async () => {
    const updated = { ...bob, display_name: "Edited" };
    const { queryClient, wrapper } = setup(async (req) => req.method === "PATCH" ? json(updated) : json(bob));
    const { result } = renderHook(() => ({ login: useLogin(), update: useUpdateMe() }), { wrapper });
    await act(() => result.current.login.mutateAsync({ username: bob.username, password: "password" }));
    await act(() => result.current.update.mutateAsync({ display_name: "Edited" }));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(updated);
  });

  it.each(["http://other.test", "http://lerryn.test"])("rejects cache writes from mutations invoked after their provider unmounted, with a new session on %s", async (baseUrl) => {
    const { queryClient, wrapper, setClient } = setup(async () => json({ ...alice, display_name: "Old response" }));
    const old = renderHook(() => useUpdateMe(), { wrapper });
    const mutateOld = old.result.current.mutateAsync;
    old.unmount();

    setClient(createLerrynClient({ baseUrl, headers: { Authorization: "Bearer new-session" }, fetch: async () => json(bob) }));
    const current = renderHook(() => useLogin(), { wrapper });
    await act(() => current.result.current.mutateAsync({ username: bob.username, password: "password" }));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(bob);
    await act(() => mutateOld({ display_name: "Old response" }));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(bob);
  });

  it("does not let a saved unmounted mutation capture a newer session of the same provider", async () => {
    const { queryClient, wrapper: Wrapper } = setup(async (req) => req.method === "PATCH" ? json(alice) : json(bob));
    let mutateOld: ReturnType<typeof useUpdateMe>["mutateAsync"] | undefined;
    let login: ReturnType<typeof useLogin>["mutateAsync"] | undefined;
    function OldScreen() {
      mutateOld = useUpdateMe().mutateAsync;
      return null;
    }
    function NewScreen() {
      login = useLogin().mutateAsync;
      return null;
    }
    const view = render(<Wrapper><OldScreen /></Wrapper>);
    const saved = mutateOld!;
    view.rerender(<Wrapper><NewScreen /></Wrapper>);
    await act(() => login!({ username: bob.username, password: "password" }));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(bob);
    await act(() => saved({ display_name: "Old response" }));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(bob);
  });

  it.each([
    { name: "logout", next: null },
    { name: "another account", next: bob },
    { name: "a new session of the same account", next: alice },
  ])("discards old profile updates after $name", async ({ next }) => {
    let resolveOld: ((response: Response) => void) | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      if (req.method === "PATCH") return new Promise<Response>((resolve) => { resolveOld = resolve; });
      return json(next ?? {});
    });
    const { result } = renderHook(() => ({ update: useUpdateMe(), login: useLogin(), logout: useLogout() }), { wrapper });
    act(() => result.current.update.mutate({ display_name: "Old response" }));
    await waitFor(() => expect(resolveOld).toBeDefined());
    await act(() => result.current.logout.mutateAsync());
    if (next) await act(() => result.current.login.mutateAsync({ username: next.username, password: "password" }));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(next);

    await act(async () => resolveOld!(json({ ...alice, display_name: "Old response" })));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(next);
  });

  it("discards old cache writes when the same user logs in again without logout", async () => {
    let resolveOld: ((response: Response) => void) | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      if (req.method === "PATCH") return new Promise<Response>((resolve) => { resolveOld = resolve; });
      return json(alice);
    });
    const { result } = renderHook(() => ({ update: useUpdateMe(), login: useLogin() }), { wrapper });
    act(() => result.current.update.mutate({ display_name: "Old response" }));
    await waitFor(() => expect(resolveOld).toBeDefined());
    await act(() => result.current.login.mutateAsync({ username: alice.username, password: "password" }));
    await act(async () => resolveOld!(json({ ...alice, display_name: "Old response" })));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(alice);
  });

  it.each(["http://other.test", "http://lerryn.test"])("discards a previous API client's response after switching to %s", async (baseUrl) => {
    let resolveOld: ((response: Response) => void) | undefined;
    const { queryClient, wrapper, setClient } = setup(() => new Promise<Response>((resolve) => { resolveOld = resolve; }));
    const { result, rerender } = renderHook(() => useUpdateMe(), { wrapper });
    act(() => result.current.mutate({ display_name: "Old response" }));
    await waitFor(() => expect(resolveOld).toBeDefined());
    setClient(createLerrynClient({ baseUrl, headers: { Authorization: "Bearer new-session" }, fetch: async () => json(alice) }));
    rerender();
    await act(async () => resolveOld!(json({ ...alice, display_name: "Old response" })));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(alice);
  });

  it.each(["login", "register", "logout"] as const)("does not apply an old %s response after changing API clients", async (action) => {
    let resolveOld: ((response: Response) => void) | undefined;
    const { queryClient, wrapper, setClient } = setup(() => new Promise<Response>((resolve) => { resolveOld = resolve; }));
    const { result, rerender } = renderHook(() => ({ login: useLogin(), register: useRegister(), logout: useLogout() }), { wrapper });
    act(() => {
      if (action === "logout") result.current.logout.mutate();
      else result.current[action].mutate({ username: alice.username, password: "password" });
    });
    await waitFor(() => expect(resolveOld).toBeDefined());
    setClient(createLerrynClient({ baseUrl: "http://other.test", fetch: async () => json(bob) }));
    rerender();
    queryClient.setQueryData(queryKeys.me, bob);
    await act(async () => resolveOld!(json(alice)));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(bob);
  });

  it("does not repopulate private run, credential-request or admin caches after logout", async () => {
    const pending = new Map<string, (response: Response) => void>();
    const { queryClient, wrapper } = setup(async (req) => {
      if (req.url.endsWith("/logout")) return json({});
      return new Promise<Response>((resolve) => pending.set(new URL(req.url).pathname, resolve));
    });
    const { result } = renderHook(() => ({
      start: useStartRun(), request: useRequestCredentialUpdate(), admin: useUpdateAdminSettings(), logout: useLogout(),
    }), { wrapper });
    act(() => {
      result.current.start.mutate("task");
      result.current.request.mutate({ id: "credential", purpose: "Update", fields: ["TOKEN"] });
      result.current.admin.mutate({ open_registration: false });
    });
    await waitFor(() => expect(pending.size).toBe(3));
    await act(() => result.current.logout.mutateAsync());
    await act(async () => {
      pending.get("/api/v1/tasks/task/runs")!(json({ id: "old-run", task_id: "task", rerun_of: null }));
      pending.get("/api/v1/credential-requests")!(json({ id: "old-request" }));
      pending.get("/api/v1/admin/settings")!(json({ open_registration: false }));
    });
    expect(queryClient.getQueryData(queryKeys.run("old-run"))).toBeUndefined();
    expect(queryClient.getQueryData(queryKeys.credentialRequest("old-request"))).toBeUndefined();
    expect(queryClient.getQueryData(queryKeys.adminSettings)).toBeUndefined();
  });

  it("keeps the new login when an old current-user query finishes later", async () => {
    let resolveOld: ((response: Response) => void) | undefined;
    const { queryClient, wrapper } = setup(async (req) => {
      if (req.method === "GET") return new Promise<Response>((resolve) => { resolveOld = resolve; });
      return json(bob);
    });
    queryClient.removeQueries();
    const { result } = renderHook(() => ({ me: useMe(), login: useLogin() }), { wrapper });
    await waitFor(() => expect(resolveOld).toBeDefined());
    await act(() => result.current.login.mutateAsync({ username: bob.username, password: "password" }));
    await act(async () => resolveOld!(json(alice)));
    expect(queryClient.getQueryData(queryKeys.me)).toEqual(bob);
  });
});
