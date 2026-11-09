import { createLerrynClient, type Schemas } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { ApiProvider } from "../client";
import { queryKeys } from "./keys";
import { useProjects, useTaskList } from "./overview";
import { useCanControlRuns } from "./runs";

type Task = Schemas["TaskSummary"];
const task = (id: string): Task => ({ id, name: id, project_id: "project", description: "", tags: [], state: "inactive", active_version: null,
  latest_version: 1, last_run: null, next_run_at: null, active_runs: 0, updated_at: "2026-11-03T00:00:00Z", links: { console: "http://lerryn.test/" } });
const page = (items: { id: string }[], next_cursor: string | null = null) => new Response(JSON.stringify({ items, next_cursor }), { headers: { "Content-Type": "application/json" } });
const project = (id: string, role: Schemas["Project"]["role"] = "viewer"): Schemas["Project"] => ({
  id, name: id, role, is_personal: false, member_count: 1, owner: { id: "owner", username: "owner", display_name: null },
  created_at: "2026-11-03T00:00:00Z", updated_at: "2026-11-03T00:00:00Z", links: { console: "http://lerryn.test/" },
});

function setup(fetch: (req: Request) => Promise<Response>) {
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}><ApiProvider client={api}>{children}</ApiProvider></QueryClientProvider>
  );
  return { queryClient, wrapper };
}

afterEach(cleanup);

describe("complete task lists", () => {
  it.each([101, 1000, 1201])("reads all %i tasks with forward-only cursors", async (count) => {
    const tasks = Array.from({ length: count }, (_, i) => task(String(i)));
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      cursors.push(url.searchParams.get("cursor"));
      expect(url.searchParams.get("limit")).toBe("100");
      const after = Number(url.searchParams.get("cursor") ?? 0);
      return page(tasks.slice(after, after + 100), after + 100 < count ? String(after + 100) : null);
    });
    const { result } = renderHook(() => ({ ...useTaskList(null) }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items).toHaveLength(count);
    expect(result.current.data?.items.map((it) => it.id).join(",")).toBe(tasks.map((it) => it.id).join(","));
    expect(result.current.data?.next_cursor).toBeNull();
    expect(cursors).toEqual(Array.from({ length: Math.ceil(count / 100) }, (_, i) => i === 0 ? null : String(i * 100)));
  });

  it("follows an empty page and deduplicates task IDs across later pages", async () => {
    const { wrapper } = setup(async (req) => {
      switch (new URL(req.url).searchParams.get("cursor")) {
        case null: return page([], "empty");
        case "empty": return page([task("one")], "next");
        default: return page([task("one"), task("two")]);
      }
    });
    const { result } = renderHook(() => ({ ...useTaskList(null) }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((it) => it.id)).toEqual(["one", "two"]);
  });

  it("keeps the old complete snapshot when a later refresh page fails, then retries from the start", async () => {
    let fail = false;
    let changed = false;
    const { wrapper } = setup(async (req) => {
      if (!new URL(req.url).searchParams.has("cursor")) return page([task(changed ? "new" : "one")], "next");
      if (fail) throw new TypeError("offline");
      return page([task("two")]);
    });
    const { result } = renderHook(() => ({ ...useTaskList(null) }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((it) => it.id)).toEqual(["one", "two"]);
    fail = changed = true;
    await act(() => result.current.refetch());
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.data?.items.map((it) => it.id)).toEqual(["one", "two"]);
    fail = false;
    await act(() => result.current.refetch());
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((it) => it.id)).toEqual(["new", "two"]);
  });

  it("does not show old-project data or continue its page scan after switching projects", async () => {
    let finishOld: ((response: Response) => void) | undefined;
    let oldSignal: AbortSignal | undefined;
    const calls: string[] = [];
    const { wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      const project = url.searchParams.get("project_id");
      calls.push(`${project}:${url.searchParams.get("cursor")}`);
      if (project === "old" && !url.searchParams.has("cursor")) return page([task("old-first")], "old-next");
      if (project === "old") {
        oldSignal = req.signal;
        return new Promise<Response>((resolve) => { finishOld = resolve; });
      }
      return page([task("new")]);
    });
    const { result, rerender } = renderHook(({ project }) => ({ ...useTaskList(project) }), { initialProps: { project: "old" }, wrapper });
    await waitFor(() => expect(finishOld).toBeDefined());
    rerender({ project: "new" });
    await waitFor(() => expect(result.current.data?.items[0]?.id).toBe("new"));
    expect(oldSignal?.aborted).toBe(true);
    await act(async () => finishOld!(page([task("old-last")], "do-not-read")));
    expect(calls).not.toContain("old:do-not-read");
    expect(result.current.data?.items.map((it) => it.id)).toEqual(["new"]);
  });

  it("does not reuse the previous project's complete list while the new project loads", async () => {
    let finishNew: ((response: Response) => void) | undefined;
    const { wrapper } = setup(async (req) => {
      if (new URL(req.url).searchParams.get("project_id") === "old") return page([task("old")]);
      return new Promise<Response>((resolve) => { finishNew = resolve; });
    });
    const { result, rerender } = renderHook(({ project }) => ({ ...useTaskList(project) }), { initialProps: { project: "old" }, wrapper });
    await waitFor(() => expect(result.current.data?.items[0]?.id).toBe("old"));
    rerender({ project: "new" });
    await waitFor(() => expect(finishNew).toBeDefined());
    expect(result.current.data).toBeUndefined();
    await act(async () => finishNew!(page([task("new")])));
    await waitFor(() => expect(result.current.data?.items[0]?.id).toBe("new"));
  });

  it.each(["repeated cursor", "unbounded cursors"])("reports an incomplete snapshot instead of accepting %s", async (kind) => {
    let reads = 0;
    const { wrapper } = setup(async () => { reads++; return page([task(String(reads))], kind === "repeated cursor" ? "same" : String(reads)); });
    const { result } = renderHook(() => ({ ...useTaskList(null) }), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.data).toBeUndefined();
    expect(reads).toBeLessThanOrEqual(100);
  });

  it("rebuilds every page after a live invalidation adds a task at the front", async () => {
    let tasks = [task("one"), task("two")];
    const { queryClient, wrapper } = setup(async (req) => {
      const after = Number(new URL(req.url).searchParams.get("cursor") ?? 0);
      return page(tasks.slice(after, after + 1), after + 1 < tasks.length ? String(after + 1) : null);
    });
    const { result } = renderHook(() => ({ ...useTaskList(null) }), { wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(2));
    tasks = [task("new"), ...tasks];
    await act(() => queryClient.invalidateQueries({ queryKey: queryKeys.taskListAll }));
    await waitFor(() => expect(result.current.data?.items.map((it) => it.id)).toEqual(["new", "one", "two"]));
  });
});

describe("complete project lists", () => {
  it.each([201, 1001])("includes all %i projects and enables run controls according to roles beyond the first page", async (count) => {
    const projects = Array.from({ length: count }, (_, i) => project(String(i), i === count - 1 ? "owner" : "viewer"));
    const cursors: (string | null)[] = [];
    const { wrapper } = setup(async (req) => {
      const url = new URL(req.url);
      expect(url.pathname).toBe("/api/v1/projects");
      expect(url.searchParams.get("limit")).toBe("200");
      cursors.push(url.searchParams.get("cursor"));
      const offset = Number(url.searchParams.get("cursor") ?? 0);
      return page(projects.slice(offset, offset + 200), offset + 200 < count ? String(offset + 200) : null);
    });
    const { result } = renderHook(() => ({
      projects: useProjects(), owner: useCanControlRuns(String(count - 1)), viewer: useCanControlRuns("0"),
    }), { wrapper });
    await waitFor(() => expect(result.current.projects.isSuccess).toBe(true));
    expect(result.current.projects.data?.items).toEqual(projects);
    expect(result.current.projects.data?.next_cursor).toBeNull();
    expect(result.current.owner).toBe(true);
    expect(result.current.viewer).toBe(false);
    expect(cursors).toEqual(Array.from({ length: Math.ceil(count / 200) }, (_, i) => i ? String(i * 200) : null));
  });

  it("keeps the last complete project list when a later page fails", async () => {
    let fail = false;
    let changed = false;
    const { wrapper } = setup(async (req) => {
      if (!new URL(req.url).searchParams.has("cursor")) return page([project(changed ? "new" : "one")], "next");
      if (fail) throw new TypeError("offline");
      return page([project("two", "maintainer")]);
    });
    const { result } = renderHook(() => ({ ...useProjects() }), { wrapper });
    await waitFor(() => expect(result.current.data?.items.map((p) => p.id)).toEqual(["one", "two"]));
    changed = fail = true;
    await act(() => result.current.refetch());
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.data?.items.map((p) => p.id)).toEqual(["one", "two"]);
    fail = false;
    await act(() => result.current.refetch());
    await waitFor(() => expect(result.current.data?.items.map((p) => p.id)).toEqual(["new", "two"]));
  });

  it("does not publish a partial project list or continue pagination after unmount", async () => {
    let finish: ((response: Response) => void) | undefined;
    let signal: AbortSignal | undefined;
    const cursors: (string | null)[] = [];
    const { queryClient, wrapper } = setup(async (req) => {
      const cursor = new URL(req.url).searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor) return page([project("one")], "second");
      signal = req.signal;
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    const { unmount } = renderHook(() => useProjects(), { wrapper });
    await waitFor(() => expect(finish).toBeDefined());
    unmount();
    expect(signal?.aborted).toBe(true);
    await act(async () => finish!(page([project("two")], "third")));
    expect(cursors).toEqual([null, "second"]);
    expect(queryClient.getQueryData(queryKeys.projects)).toBeUndefined();
  });

  it("rejects a repeating cursor without retrying or granting roles from a partial list", async () => {
    let reads = 0;
    const { wrapper } = setup(async () => { reads++; return page([project("owner-project", "owner")], "same"); });
    const { result } = renderHook(() => ({ projects: useProjects(), canControl: useCanControlRuns("owner-project") }), { wrapper });
    await waitFor(() => expect(result.current.projects.isError).toBe(true));
    expect(result.current.projects.data).toBeUndefined();
    expect(result.current.canControl).toBe(false);
    expect(reads).toBe(2);
  });
});
