import { createLerrynClient, type Schemas } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiProvider } from "../client";
import { queryKeys } from "../queries/keys";
import { useTaskList } from "../queries/overview";
import { invalidateForRunUpdate, LiveModeProvider, StreamProvider, useEventStream, type OpenStream, type StreamCallbacks } from "./live";

const update = { run_id: "run", task_id: "task0" };

function setup(count = 200) {
  const state = { label: "old", delay: 0, fail: false };
  const reads: { project: string | null; cursor: number; signal: AbortSignal; label: string }[] = [];
  let callbacks: StreamCallbacks | undefined;
  const open: OpenStream = (_path, _query, _events, cb) => {
    callbacks = cb;
    return () => {};
  };
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: async (req) => {
    const url = new URL(req.url);
    const cursor = Number(url.searchParams.get("cursor") ?? 0);
    const project = url.searchParams.get("project_id");
    reads.push({ project, cursor, signal: req.signal, label: state.label });
    const fail = state.fail;
    const items: Schemas["TaskSummary"][] = Array.from({ length: Math.min(100, count - cursor) }, (_, offset) => ({
      id: `task${cursor + offset}`, name: `${state.label}-${cursor + offset}`, project_id: project ?? "project", description: "", tags: [],
      state: "inactive", active_version: null, latest_version: 1, last_run: null, next_run_at: null,
      active_runs: 0, updated_at: "2026-11-03T00:00:00Z", links: { console: "http://lerryn.test/" },
    }));
    const response = () => fail
      ? new Response(JSON.stringify({ code: "internal_error", status: 503, request_id: "failed-refresh" }), { status: 503, headers: { "Content-Type": "application/problem+json" } })
      : new Response(JSON.stringify({ items, next_cursor: cursor + 100 < count ? String(cursor + 100) : null }), {
        headers: { "Content-Type": "application/json" },
      });
    if (!state.delay) return response();
    return new Promise<Response>((resolve, reject) => {
      const aborted = () => {
        clearTimeout(timer);
        reject(new DOMException("aborted", "AbortError"));
      };
      const timer = setTimeout(() => {
        req.signal.removeEventListener("abort", aborted);
        resolve(response());
      }, state.delay);
      req.signal.addEventListener("abort", aborted, { once: true });
    });
  } });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={api}>
        <StreamProvider open={open}><LiveModeProvider mode="stream">{children}</LiveModeProvider></StreamProvider>
      </ApiProvider>
    </QueryClientProvider>
  );
  return { state, reads, queryClient, wrapper, callbacks: () => callbacks! };
}

describe("event-driven query refreshes", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => { cleanup(); vi.useRealTimers(); });

  it.each(["run", "resync", "reconnect"])("commits full refreshes while %s events keep arriving", async (event) => {
    const s = setup(1000);
    const { result } = renderHook(() => {
      useEventStream({ enabled: true });
      return { ...useTaskList(null) };
    }, { wrapper: s.wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(1000));
    s.state.label = "new";
    s.state.delay = 20;
    act(() => s.callbacks().onOpen?.());
    const events = setInterval(() => {
      if (event === "reconnect") s.callbacks().onOpen?.();
      else s.callbacks().onEvent(event, JSON.stringify(update), "");
    }, 50);
    let during: string | undefined;
    try {
      await act(() => vi.advanceTimersByTimeAsync(2000));
      during = result.current.data?.items[0]?.name;
    } finally {
      clearInterval(events);
    }
    await act(() => vi.advanceTimersByTimeAsync(500));
    const settledReads = s.reads.length;
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(during).toBe("new-0");
    expect(s.reads).toHaveLength(settledReads);
  });

  it.each(["live", "manual"])("performs a trailing refresh when the last event changes an already-read page during a %s refresh", async (source) => {
    const s = setup();
    const { result } = renderHook(() => ({ ...useTaskList(null) }), { wrapper: s.wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(200));
    s.state.label = "middle";
    s.state.delay = 20;
    act(() => {
      if (source === "live") invalidateForRunUpdate(s.queryClient, update);
      else void result.current.refetch();
    });
    await act(() => vi.advanceTimersByTimeAsync(25));
    s.state.label = "final";
    act(() => invalidateForRunUpdate(s.queryClient, update));
    await act(() => vi.advanceTimersByTimeAsync(20));
    expect(result.current.data?.items[0]?.name).toBe("middle-0");
    await act(() => vi.advanceTimersByTimeAsync(60));
    expect(result.current.data?.items[0]?.name).toBe("final-0");
    expect(s.reads).toHaveLength(6);
    await act(() => vi.advanceTimersByTimeAsync(200));
    expect(s.reads).toHaveLength(6);
  });

  it.each(["project switch", "cancel"])("drops pending trailing refreshes after a %s", async (stop) => {
    const s = setup();
    const { result, rerender } = renderHook(({ project }) => ({ ...useTaskList(project) }), {
      initialProps: { project: "old" }, wrapper: s.wrapper,
    });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(200));
    s.state.delay = 20;
    s.state.label = "discarded";
    act(() => invalidateForRunUpdate(s.queryClient, update));
    await act(() => vi.advanceTimersByTimeAsync(25));
    act(() => invalidateForRunUpdate(s.queryClient, update));
    const oldReads = s.reads.filter((read) => read.project === "old").length;
    if (stop === "project switch") rerender({ project: "new" });
    else await act(() => s.queryClient.cancelQueries({ queryKey: queryKeys.taskListAll }));
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(s.reads.filter((read) => read.project === "old")).toHaveLength(oldReads);
    if (stop === "project switch") expect(result.current.data?.items[0]?.project_id).toBe("new");
    else {
      expect(result.current.data?.items[0]?.name).toBe("old-0");
      s.state.delay = 0;
      act(() => invalidateForRunUpdate(s.queryClient, update));
      await waitFor(() => expect(result.current.data?.items[0]?.name).toBe("discarded-0"));
    }
  });

  it("stops a dirty refresh after failure and recovers on the next event", async () => {
    const s = setup();
    const { result } = renderHook(() => ({ ...useTaskList(null) }), { wrapper: s.wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(200));
    s.state.delay = 20;
    s.state.fail = true;
    act(() => invalidateForRunUpdate(s.queryClient, update));
    await act(() => vi.advanceTimersByTimeAsync(10));
    act(() => invalidateForRunUpdate(s.queryClient, update));
    await act(() => vi.advanceTimersByTimeAsync(100));
    expect(result.current.isError).toBe(true);
    expect(result.current.data?.items[0]?.name).toBe("old-0");
    const failedReads = s.reads.length;
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(s.reads).toHaveLength(failedReads);

    s.state.fail = false;
    s.state.label = "recovered";
    act(() => invalidateForRunUpdate(s.queryClient, update));
    await act(() => vi.advanceTimersByTimeAsync(100));
    expect(result.current.data?.items[0]?.name).toBe("recovered-0");
    expect(result.current.isSuccess).toBe(true);
    expect(s.reads).toHaveLength(failedReads + 2);
  });

  it.each(["clear", "remove"])("does not transfer a pending refresh to a replacement query after cache %s", async (remove) => {
    const s = setup();
    const { result, rerender } = renderHook(() => ({ ...useTaskList(null) }), { wrapper: s.wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(200));
    const old = s.queryClient.getQueryCache().find({ queryKey: queryKeys.taskList(null), exact: true });
    s.state.delay = 20;
    s.state.label = "discarded";
    act(() => invalidateForRunUpdate(s.queryClient, update));
    await act(() => vi.advanceTimersByTimeAsync(25));
    act(() => invalidateForRunUpdate(s.queryClient, update));
    act(() => {
      if (remove === "clear") s.queryClient.clear();
      else s.queryClient.removeQueries({ queryKey: queryKeys.taskListAll });
    });
    s.state.label = "replacement";
    s.state.delay = 0;
    rerender();
    await waitFor(() => expect(result.current.data?.items[0]?.name).toBe("replacement-0"));
    expect(s.queryClient.getQueryCache().find({ queryKey: queryKeys.taskList(null), exact: true })).not.toBe(old);
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(s.reads.filter((read) => read.label === "discarded")).toHaveLength(2);
    expect(s.reads.filter((read) => read.label === "replacement")).toHaveLength(2);
    expect(result.current.data?.items[0]?.name).toBe("replacement-0");
  });
});
