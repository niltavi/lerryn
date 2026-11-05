import { createLerrynClient } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { ApiProvider } from "../client";
import type { LogLine } from "../logs/model";
import { queryKeys } from "../queries/keys";
import { useRunLog } from "../queries/runs";
import { StreamProvider, useEventStream, type OpenStream, type StreamCallbacks } from "./live";

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
const line = (seq: number): LogLine => ({ seq, ts: "2026-11-02T02:45:01Z", stream: "stdout", stage: null, text: `l${seq}` });

/** A fake OpenStream that records what was opened and lets the test push events. */
function fakeStream() {
  const opened: { path: string; query: Record<string, unknown>; cb: StreamCallbacks; closed: boolean }[] = [];
  const open: OpenStream = (path, query, _events, cb) => {
    const entry = { path, query, cb, closed: false };
    opened.push(entry);
    return () => {
      entry.closed = true;
    };
  };
  return { open, opened };
}

function setup(fetch: (req: Request) => Promise<Response>, open: OpenStream | null) {
  const client = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>
      <ApiProvider client={client}>
        <StreamProvider open={open}>{children}</StreamProvider>
      </ApiProvider>
    </QueryClientProvider>
  );
  return { queryClient, wrapper };
}

describe("useRunLog over the stream", () => {
  it("reads the tail, then streams from its cursor and ends on the end event", async () => {
    const fetch = vi.fn(async (req: Request) => {
      const url = new URL(req.url);
      expect(url.searchParams.get("tail")).toBe("500");
      return json({ lines: [line(1), line(2)], next_cursor: "c2", complete: false, dropped_lines: 0 });
    });
    const s = fakeStream();
    const { wrapper } = setup(fetch, s.open);
    const { result } = renderHook(() => useRunLog("r1"), { wrapper });
    await waitFor(() => expect(s.opened).toHaveLength(1));
    expect(s.opened[0]!.path).toBe("/api/v1/runs/r1/logs/stream");
    expect(s.opened[0]!.query).toEqual({ cursor: "c2" });

    act(() => {
      s.opened[0]!.cb.onOpen?.();
      s.opened[0]!.cb.onEvent("line", JSON.stringify(line(3)), "3");
      s.opened[0]!.cb.onEvent("line", JSON.stringify(line(2)), "2"); // a repeat after reconnect
      s.opened[0]!.cb.onEvent("line", "not json", "");
    });
    await waitFor(() => expect(result.current.lines.map((l) => l.seq)).toEqual([1, 2, 3]));
    expect(result.current.mode).toBe("stream");

    act(() => s.opened[0]!.cb.onEvent("end", JSON.stringify({ status: "completed", last_seq: 3, dropped_lines: 4 }), ""));
    await waitFor(() => expect(result.current.complete).toBe(true));
    expect(result.current.dropped).toBe(4);
    expect(s.opened[0]!.closed).toBe(true);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("falls back to polling from the tail cursor when the stream is not available", async () => {
    const cursors: (string | null)[] = [];
    const fetch = vi.fn(async (req: Request) => {
      const url = new URL(req.url);
      cursors.push(url.searchParams.get("cursor"));
      if (url.searchParams.get("tail")) return json({ lines: [line(1)], next_cursor: "c1", complete: false, dropped_lines: 0 });
      return json({ lines: [line(2)], next_cursor: "c2", complete: true, dropped_lines: 0 });
    });
    const s = fakeStream();
    const { wrapper } = setup(fetch, s.open);
    const { result } = renderHook(() => useRunLog("r1"), { wrapper });
    await waitFor(() => expect(s.opened).toHaveLength(1));
    // The server answered with an error: EventSource gives up.
    act(() => s.opened[0]!.cb.onError(false));
    await waitFor(() => expect(result.current.complete).toBe(true));
    expect(cursors).toEqual([null, "c1"]);
    expect(result.current.lines.map((l) => l.seq)).toEqual([1, 2]);
    expect(s.opened[0]!.closed).toBe(true);
  });

  it("counts reconnects while the browser retries by itself", async () => {
    const fetch = vi.fn(async () => json({ lines: [], next_cursor: "c0", complete: false, dropped_lines: 0 }));
    const s = fakeStream();
    const { wrapper } = setup(fetch, s.open);
    const { result } = renderHook(() => useRunLog("r1"), { wrapper });
    await waitFor(() => expect(s.opened).toHaveLength(1));
    act(() => s.opened[0]!.cb.onError(true));
    expect(result.current.reconnectAttempt).toBe(1);
    act(() => s.opened[0]!.cb.onOpen?.());
    expect(result.current.reconnectAttempt).toBe(0);
    expect(s.opened).toHaveLength(1);
  });
});

describe("useEventStream", () => {
  it("reports stream mode while open and invalidates what a run update affects", async () => {
    const s = fakeStream();
    const { wrapper, queryClient } = setup(async () => json({}), s.open);
    const { result } = renderHook(() => useEventStream({ enabled: true, projectId: "p1" }), { wrapper });
    expect(result.current).toBe("polling");
    expect(s.opened[0]!.path).toBe("/api/v1/events/stream");
    expect(s.opened[0]!.query).toEqual({ project_id: "p1" });

    act(() => s.opened[0]!.cb.onOpen?.());
    expect(result.current).toBe("stream");
    const affected = [queryKeys.overview(null), queryKeys.calendar(null, 0), queryKeys.taskList(null), queryKeys.runList({}),
      queryKeys.task("t1"), queryKeys.run("r1"), queryKeys.runEvents("r1"), queryKeys.runArtifacts("r1")];
    for (const key of [...affected, queryKeys.task("other")]) queryClient.setQueryData(key, {});
    act(() => s.opened[0]!.cb.onEvent("run", JSON.stringify({ run_id: "r1", task_id: "t1" }), ""));
    for (const key of affected) expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(queryKeys.task("other"))?.isInvalidated).toBe(false);

    act(() => s.opened[0]!.cb.onError(true));
    expect(result.current).toBe("polling");
  });

  it("retries a stream the server refused a minute later", async () => {
    vi.useFakeTimers();
    try {
      const s = fakeStream();
      const { wrapper } = setup(async () => json({}), s.open);
      renderHook(() => useEventStream({ enabled: true }), { wrapper });
      act(() => s.opened[0]!.cb.onError(false));
      expect(s.opened[0]!.closed).toBe(true);
      act(() => vi.advanceTimersByTime(60_000));
      expect(s.opened).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
