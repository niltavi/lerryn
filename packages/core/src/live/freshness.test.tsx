import { createLerrynClient } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiProvider } from "../client";
import { useCalendar, useOverview, useProjects, useProjectMembers, useTaskList } from "../queries/overview";
import { useAccessTokens } from "../queries/account";
import { useTools } from "../queries/tools";
import { useRun, useTask } from "../queries/runs";
import { useCredentialRequest, useCredentials, usePendingCredentialRequests } from "../queries/settings";
import { useTaskCredentials, useTaskTriggers, useTaskVersions } from "../queries/tasks";
import { LiveModeProvider, type LiveMode } from "./live";

const json = (data: unknown) => new Response(JSON.stringify(data), { headers: { "Content-Type": "application/json" } });

function setup(mode: LiveMode) {
  const state = { filled: false, active: false, runStatus: "running" };
  const reads: string[] = [];
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: async (req) => {
    const path = new URL(req.url).pathname;
    reads.push(path);
    const request = { id: "request", status: state.filled ? "fulfilled" : "pending" };
    switch (path) {
      case "/api/v1/tools": return json({ items: state.active ? [{ id: "tool" }] : [] });
      case "/api/v1/me/tokens": return json({ items: state.active ? [{ id: "token" }] : [], next_cursor: null });
      case "/api/v1/projects": return json({ items: [{ id: "project", role: state.active ? "viewer" : "owner" }], next_cursor: null });
      case "/api/v1/projects/project/members": return json({ items: state.active ? [{ id: "member" }] : [], next_cursor: null });
      case "/api/v1/tasks/task": return json({ id: "task", state: state.active ? "active" : "inactive" });
      case "/api/v1/tasks/task/triggers": return json({ task_active: state.active, triggers: [] });
      case "/api/v1/tasks/task/versions": return json({ items: [{ seq: state.active ? 2 : 1 }] });
      case "/api/v1/tasks/task/credentials": return json({ items: [{ status: state.filled ? "bound" : "missing" }] });
      case "/api/v1/credentials": return json({ items: state.filled ? [{ id: "credential" }] : [], next_cursor: null });
      case "/api/v1/credential-requests": return json({ items: state.filled ? [] : [request], next_cursor: null });
      case "/api/v1/credential-requests/request": return json(request);
      case "/api/v1/overview": return json({ attention: state.filled ? [] : [{ kind: "credential_request" }] });
      case "/api/v1/overview/calendar": return json({ items: state.active ? [{ kind: "planned" }] : [] });
      case "/api/v1/tasks": return json({ items: [{ id: "task", state: state.active ? "active" : "inactive" }], next_cursor: null });
      case "/api/v1/runs/run": return json({ id: "run", status: state.runStatus });
      default: throw new Error(`Unexpected path: ${path}`);
    }
  } });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}><ApiProvider client={api}>
      <LiveModeProvider mode={mode}>{children}</LiveModeProvider>
    </ApiProvider></QueryClientProvider>
  );
  return { state, reads, wrapper };
}

beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
afterEach(() => { cleanup(); vi.useRealTimers(); });

describe("external changes without stream events", () => {
  it.each(["stream", "polling"] as const)("refreshes task, credential and overview metadata in %s mode", async (mode) => {
    const s = setup(mode);
    const { result, unmount } = renderHook(() => ({
      tools: useTools(null), tokens: useAccessTokens(), projects: useProjects(), members: useProjectMembers("project"),
      task: useTask("task"), triggers: useTaskTriggers("task"), versions: useTaskVersions("task"),
      bindings: useTaskCredentials("task"), credentials: useCredentials(),
      pending: usePendingCredentialRequests(), request: useCredentialRequest("request"),
      overview: useOverview(null), tasks: useTaskList(null),
      calendar: useCalendar({ projectId: null, from: 0, to: 86_400_000 }),
    }), { wrapper: s.wrapper });
    await waitFor(() => expect(Object.values(result.current).every((query) => query.isSuccess)).toBe(true));
    expect(result.current.pending.data?.items).toHaveLength(1);
    expect(result.current.bindings.data?.items[0]?.status).toBe("missing");
    s.state.filled = true;
    s.state.active = true;
    await act(() => vi.advanceTimersByTimeAsync(5_100));
    expect(result.current.tools.data?.items).toHaveLength(1);
    expect(result.current.tokens.data?.items).toHaveLength(1);
    expect(result.current.projects.data?.items[0]?.role).toBe("viewer");
    expect(result.current.members.data?.items).toHaveLength(1);
    expect(result.current.task.data?.state).toBe("active");
    expect(result.current.triggers.data?.task_active).toBe(true);
    expect(result.current.versions.data?.items[0]?.seq).toBe(2);
    expect(result.current.bindings.data?.items[0]?.status).toBe("bound");
    expect(result.current.credentials.data?.items).toHaveLength(1);
    expect(result.current.pending.data?.items).toHaveLength(0);
    expect(result.current.request.data?.status).toBe("fulfilled");
    expect(result.current.overview.data?.attention).toHaveLength(0);
    expect(result.current.tasks.data?.items[0]?.state).toBe("active");
    expect(result.current.calendar.data?.items).toHaveLength(1);
    const requestReads = s.reads.filter((path) => path.endsWith("/request")).length;
    await act(() => vi.advanceTimersByTimeAsync(5_100));
    expect(s.reads.filter((path) => path.endsWith("/request"))).toHaveLength(requestReads);
    unmount();
    const total = s.reads.length;
    await act(() => vi.advanceTimersByTimeAsync(15_100));
    expect(s.reads).toHaveLength(total);
  });

  it("recovers a missed run event on an open stream and stops polling at completion", async () => {
    const s = setup("stream");
    const { result } = renderHook(() => useRun("run"), { wrapper: s.wrapper });
    await waitFor(() => expect(result.current.data?.status).toBe("running"));
    s.state.runStatus = "completed";
    await act(() => vi.advanceTimersByTimeAsync(3_000));
    expect(s.reads).toHaveLength(1);
    await act(() => vi.advanceTimersByTimeAsync(12_100));
    expect(result.current.data?.status).toBe("completed");
    expect(s.reads).toHaveLength(2);
    await act(() => vi.advanceTimersByTimeAsync(30_100));
    expect(s.reads).toHaveLength(2);
  });
});
