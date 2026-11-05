import { createLerrynClient } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, ApiProvider, unwrap } from "../client";
import type { LogLine } from "../logs/model";
import { queryKeys } from "./keys";
import { useMe } from "./account";
import { fetchFullLog, useRerunRun, useRunLog, useStartRun } from "./runs";

const json = (body: unknown, status = 200, type = "application/json") =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": type } });
const problem = (status: number, code: string) =>
  json({ type: `urn:lerryn:error:${code}`, title: code, status, code, request_id: "req_1" }, status, "application/problem+json");

const line = (seq: number): LogLine => ({ seq, ts: "2026-11-02T02:45:01Z", stream: "stdout", stage: null, text: `l${seq}` });

/** A fake log endpoint: `total` lines, `ended` decides completion. Cursors are "c<seq>". */
function fakeLogServer(opts: { total: () => number; ended: () => boolean; failNext?: () => boolean }) {
  return vi.fn(async (input: Request) => {
    const url = new URL(input.url);
    if (opts.failNext?.()) throw new TypeError("network down");
    const total = opts.total();
    const tail = url.searchParams.get("tail");
    const cursor = url.searchParams.get("cursor");
    const limit = Number(url.searchParams.get("limit") ?? tail ?? 200);
    const after = tail ? Math.max(0, total - Number(tail)) : cursor ? Number(cursor.slice(1)) : 0;
    const to = Math.min(after + limit, total);
    const lines = Array.from({ length: Math.max(0, to - after) }, (_, i) => line(after + 1 + i));
    const last = lines.at(-1)?.seq ?? after;
    return json({ lines, next_cursor: `c${last}`, complete: opts.ended() && last >= total, dropped_lines: 0 });
  });
}

function setup(fetch: (req: Request) => Promise<Response>) {
  const client = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>{children}</ApiProvider>
    </QueryClientProvider>
  );
  return { client, wrapper, queryClient };
}

describe("unwrap", () => {
  it("throws ApiError with the problem code", async () => {
    const { client } = setup(async () => problem(404, "not_found"));
    const err = await unwrap(client.GET("/api/v1/me")).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 404, code: "not_found", requestId: "req_1" });
  });
  it("turns a failed fetch into network_error", async () => {
    const { client } = setup(async () => {
      throw new TypeError("offline");
    });
    await expect(unwrap(client.GET("/api/v1/me"))).rejects.toMatchObject({ code: "network_error", status: 0 });
  });
});

describe("useMe", () => {
  it("returns null when signed out", async () => {
    const { wrapper } = setup(async () => problem(401, "unauthenticated"));
    const { result } = renderHook(() => useMe(), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeNull();
  });
});

describe("run actions over HTTP", () => {
  it("refreshes task lists and the calendar immediately after starting a run", async () => {
    const { wrapper, queryClient } = setup(async () => json({ id: "r1", task_id: "t1", rerun_of: null }));
    const keys = [queryKeys.taskList(null), queryKeys.calendar(null, 0), queryKeys.task("t1"), queryKeys.overview(null)];
    for (const key of keys) queryClient.setQueryData(key, {});
    const { result } = renderHook(() => useStartRun(), { wrapper });
    await act(() => result.current.mutateAsync("t1"));
    for (const key of keys) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
  });

  afterEach(() => vi.unstubAllGlobals());

  it.each([
    { useAction: useStartRun, path: "/api/v1/tasks/t1/runs" },
    { useAction: useRerunRun, path: "/api/v1/runs/t1/rerun" },
  ])("sends a fresh idempotency key to $path without randomUUID", async ({ useAction, path }) => {
    vi.stubGlobal("crypto", { getRandomValues: crypto.getRandomValues.bind(crypto) });
    const keys: string[] = [];
    const { wrapper } = setup(async (req) => {
      expect(new URL(req.url).pathname).toBe(path);
      keys.push(req.headers.get("Idempotency-Key") ?? "");
      return json({ id: "r1", task_id: "t1", rerun_of: null });
    });
    const { result } = renderHook(() => useAction(), { wrapper });

    await act(() => result.current.mutateAsync("t1"));
    await act(() => result.current.mutateAsync("t1"));

    expect(keys).toHaveLength(2);
    for (const key of keys) expect(key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(keys[0]).not.toBe(keys[1]);
  });
});

describe("useRunLog", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => vi.useRealTimers());

  it("opens on the tail of a finished run without polling", async () => {
    const fetch = fakeLogServer({ total: () => 1200, ended: () => true });
    const { wrapper } = setup(fetch);
    const { result } = renderHook(() => useRunLog("r1"), { wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.lines[0]?.seq).toBe(701);
    expect(result.current.complete).toBe(true);
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(fetch).toHaveBeenCalledTimes(1);

    // Earlier lines load forward from line 1 until the gap closes.
    act(() => result.current.loadEarlier());
    await waitFor(() => expect(result.current.lines).toHaveLength(1200));
    expect(result.current.headCursor).toBe("c1000");
  });

  it("follows an active run and backs off on failure without losing lines", async () => {
    let total = 3;
    let ended = false;
    let fail = false;
    const fetch = fakeLogServer({ total: () => total, ended: () => ended, failNext: () => fail });
    const { wrapper } = setup(fetch);
    const { result } = renderHook(() => useRunLog("r1"), { wrapper });
    await waitFor(() => expect(result.current.lines).toHaveLength(3));

    fail = true;
    await act(() => vi.advanceTimersByTimeAsync(1_600));
    await waitFor(() => expect(result.current.reconnectAttempt).toBe(1));

    fail = false;
    total = 5;
    ended = true;
    await act(() => vi.advanceTimersByTimeAsync(3_100));
    await waitFor(() => expect(result.current.complete).toBe(true));
    expect(result.current.lines.map((l) => l.seq)).toEqual([1, 2, 3, 4, 5]);
    expect(result.current.reconnectAttempt).toBe(0);
  });

  it("reports a log removed by retention instead of an error", async () => {
    const fetch = vi.fn(async () => problem(410, "expired"));
    const { wrapper } = setup(fetch);
    const { result } = renderHook(() => useRunLog("r1"), { wrapper });
    await waitFor(() => expect(result.current.expired).toBe(true));
    expect(result.current.error).toBeNull();
    await act(() => vi.advanceTimersByTimeAsync(5_000));
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("reads nothing when the run says its log was removed", async () => {
    const fetch = vi.fn(async () => problem(410, "expired"));
    const { wrapper } = setup(fetch);
    const { result } = renderHook(() => useRunLog("r1", { expired: true }), { wrapper });
    expect(result.current.expired).toBe(true);
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(fetch).not.toHaveBeenCalled();
  });

  it("discards earlier pages from the previous run without clearing the new request", async () => {
    const pending: ((response: Response) => void)[] = [];
    const fetch = vi.fn(async (req: Request) => {
      const url = new URL(req.url);
      if (url.searchParams.has("tail")) {
        const seq = url.pathname.includes("r1") ? 1001 : 2001;
        return json({ lines: [line(seq)], next_cursor: `c${seq}`, complete: true, dropped_lines: 0 });
      }
      return new Promise<Response>((resolve) => pending.push(resolve));
    });
    const { wrapper } = setup(fetch);
    const { result, rerender } = renderHook(({ runId }) => useRunLog(runId), { initialProps: { runId: "r1" }, wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    act(() => result.current.loadEarlier());
    await waitFor(() => expect(pending).toHaveLength(1));

    rerender({ runId: "r2" });
    await waitFor(() => expect(result.current.lines.map((l) => l.seq)).toEqual([2001]));
    expect(result.current.loadingEarlier).toBe(false);
    act(() => result.current.loadEarlier());
    await waitFor(() => expect(pending).toHaveLength(2));

    await act(async () => pending[0]!(json({ lines: [line(1)], next_cursor: "old", complete: false, dropped_lines: 99 })));
    expect(result.current.lines.map((l) => l.seq)).toEqual([2001]);
    expect(result.current.loadingEarlier).toBe(true);
    expect(result.current.dropped).toBe(0);

    await act(async () => pending[1]!(json({ lines: [line(2)], next_cursor: "new", complete: false, dropped_lines: 0 })));
    expect(result.current.lines.map((l) => l.seq)).toEqual([2, 2001]);
    expect(result.current.loadingEarlier).toBe(false);
  });

  it("discards earlier pages requested before a retry of the same run", async () => {
    let resolveEarlier: ((response: Response) => void) | undefined;
    let tailReads = 0;
    const { wrapper } = setup(async (req) => {
      if (new URL(req.url).searchParams.has("tail")) {
        tailReads++;
        return json({ lines: [line(1001)], next_cursor: "tail", complete: true, dropped_lines: 0 });
      }
      return new Promise<Response>((resolve) => { resolveEarlier = resolve; });
    });
    const { result } = renderHook(() => useRunLog("r1"), { wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    act(() => result.current.loadEarlier());
    await waitFor(() => expect(resolveEarlier).toBeDefined());
    act(() => result.current.retry());
    await waitFor(() => expect(tailReads).toBe(2));
    expect(result.current.loadingEarlier).toBe(false);

    await act(async () => resolveEarlier!(json({ lines: [line(1)], next_cursor: "old", complete: false, dropped_lines: 0 })));
    expect(result.current.lines.map((l) => l.seq)).toEqual([1001]);
    expect(result.current.headCursor).toBeNull();
  });
});

describe("fetchFullLog", () => {
  it("reads every page in order", async () => {
    const fetch = fakeLogServer({ total: () => 2500, ended: () => true });
    const { client } = setup(fetch);
    const lines = await fetchFullLog(client, "r1");
    expect(lines).toHaveLength(2500);
    expect(lines.at(-1)?.seq).toBe(2500);
    expect(fetch).toHaveBeenCalledTimes(3);
  });
});
