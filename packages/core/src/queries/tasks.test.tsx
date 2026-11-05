import { createLerrynClient } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiProvider } from "../client";
import { LiveModeProvider, type LiveMode } from "../live/live";
import { useTaskRuns } from "./tasks";

describe("task run discovery", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => { cleanup(); vi.useRealTimers(); });

  it.each([
    { name: "empty", initial: [] },
    { name: "completed", initial: [{ id: "old", status: "completed" }] },
  ])("finds new runs after a settled $name list while polling", async ({ initial }) => {
    let items = initial;
    const fetch = vi.fn(async () => new Response(JSON.stringify({ items, next_cursor: null }), { headers: { "Content-Type": "application/json" } }));
    const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
    const queryClient = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}><ApiProvider client={api}>{children}</ApiProvider></QueryClientProvider>
    );
    const { result } = renderHook(() => ({ ...useTaskRuns("task") }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    items = [{ id: "new", status: "running" }];
    await act(() => vi.advanceTimersByTimeAsync(5_100));
    expect(fetch).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(result.current.data?.items.map((run) => run.id)).toEqual(["new"]));
  });

  it("discovers runs both with an open stream and after it goes down", async () => {
    let mode: LiveMode = "stream";
    const fetch = vi.fn(async () => new Response(JSON.stringify({ items: [], next_cursor: null }), { headers: { "Content-Type": "application/json" } }));
    const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
    const queryClient = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}><ApiProvider client={api}><LiveModeProvider mode={mode}>{children}</LiveModeProvider></ApiProvider></QueryClientProvider>
    );
    const { result, rerender } = renderHook(() => ({ ...useTaskRuns("task") }), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    await act(() => vi.advanceTimersByTimeAsync(5_100));
    expect(fetch).toHaveBeenCalledTimes(2);
    mode = "polling";
    rerender();
    await act(() => vi.advanceTimersByTimeAsync(5_100));
    expect(fetch).toHaveBeenCalledTimes(3);
  });
});
