// Reads for the overview board, the task list and projects. Shared by the web
// console and the phone. Metadata also polls because the stream only carries runs.
import type { Schemas, LerrynClient } from "@lerryn/api";
import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { unwrap, useApi } from "../client";
import { shouldRetry } from "../errors";
import { RESOURCE_POLL_MS } from "../live/live";
import { startOfLocalDay } from "../view/model";
import { queryKeys } from "./keys";

export type Calendar = Schemas["Calendar"];
export type UpcomingFire = Schemas["UpcomingFire"];
export type Project = Schemas["Project"];
export type ProjectMember = Schemas["Member"];

/** Refetch interval for mutable overview metadata, including external changes. */
export const OVERVIEW_POLL_MS = RESOURCE_POLL_MS;
const DAY_MS = 86_400_000;
const TASK_PAGE = 100;
const PROJECT_PAGE = 200;
// A faulty or ever-growing cursor chain must fail rather than show a partial list.
const MAX_LIST_PAGES = 100;

class IncompleteListError extends Error {}

const projectQuery = (projectId: string | null) => (projectId ? { project_id: projectId } : {});

/** The overview board: what needs attention, active runs, upcoming fires and counts. */
export function useOverview(projectId: string | null) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.overview(projectId),
    queryFn: () => unwrap(api.GET("/api/v1/overview", { params: { query: projectQuery(projectId) } })),
    retry: shouldRetry,
    placeholderData: keepPreviousData,
    refetchInterval: RESOURCE_POLL_MS,
  });
}

/** Runs and planned fires in [from, to), optionally for one task. */
export function useCalendar(range: { projectId: string | null; from: number; to: number; taskId?: string }) {
  const api = useApi();
  const { projectId, from, to, taskId } = range;
  return useQuery({
    queryKey: queryKeys.calendar(projectId, from, to, taskId),
    queryFn: () =>
      unwrap(
        api.GET("/api/v1/overview/calendar", {
          params: {
            query: {
              ...projectQuery(projectId),
              ...(taskId ? { task_id: taskId } : {}),
              from: new Date(from).toISOString(),
              to: new Date(to).toISOString(),
            },
          },
        }),
      ),
    retry: shouldRetry,
    placeholderData: keepPreviousData,
    refetchInterval: RESOURCE_POLL_MS,
  });
}

/** Today's runs and planned fires, local midnight to midnight, for the ridge strip. */
export function useTodayCalendar(projectId: string | null, now: number) {
  const from = startOfLocalDay(now);
  return useCalendar({ projectId, from, to: from + DAY_MS });
}

async function readCompleteList<T extends { id: string }>(
  readPage: (cursor: string | undefined) => Promise<{ items: T[]; next_cursor: string | null }>,
  signal: AbortSignal,
  pageSize: number,
): Promise<{ items: T[]; next_cursor: null }> {
  const items = new Map<string, T>();
  const cursors = new Set<string>();
  let cursor: string | undefined;
  for (let pageNumber = 0; pageNumber < MAX_LIST_PAGES; pageNumber++) {
    if (signal.aborted) throw signal.reason ?? new Error("list read aborted");
    const page = await readPage(cursor);
    if (signal.aborted) throw signal.reason ?? new Error("list read aborted");
    for (const item of page.items) items.set(item.id, item);
    if (items.size > pageSize * MAX_LIST_PAGES) throw new IncompleteListError("list exceeds the snapshot limit");
    if (page.next_cursor === null) return { items: [...items.values()], next_cursor: null };
    if (!page.next_cursor || cursors.has(page.next_cursor)) throw new IncompleteListError("list cursor did not advance");
    cursors.add(page.next_cursor);
    cursor = page.next_cursor;
  }
  throw new IncompleteListError("list exceeds the page limit");
}

function readTaskList(api: LerrynClient, projectId: string | null, signal: AbortSignal) {
  return readCompleteList((cursor) => unwrap(api.GET("/api/v1/tasks", {
    params: { query: { ...projectQuery(projectId), limit: TASK_PAGE, cursor } }, signal,
  })), signal, TASK_PAGE);
}

/** Reads every page before replacing the list used by task search and counts. */
export function useTaskList(projectId: string | null) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.taskList(projectId),
    queryFn: ({ signal }) => readTaskList(api, projectId, signal),
    retry: (count, err) => !(err instanceof IncompleteListError) && shouldRetry(count, err),
    refetchInterval: RESOURCE_POLL_MS,
  });
}

/** Projects the user belongs to. */
export function useProjects() {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.projects,
    queryFn: ({ signal }) => readCompleteList((cursor) => unwrap(api.GET("/api/v1/projects", {
      params: { query: { limit: PROJECT_PAGE, cursor } }, signal,
    })), signal, PROJECT_PAGE),
    retry: (count, err) => !(err instanceof IncompleteListError) && shouldRetry(count, err),
    refetchInterval: RESOURCE_POLL_MS,
  });
}

/** Members of a project, to name a task's owner. */
export function useProjectMembers(projectId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.projectMembers(projectId ?? ""),
    enabled: !!projectId,
    queryFn: () =>
      unwrap(
        api.GET("/api/v1/projects/{projectId}/members", {
          params: { path: { projectId: projectId! }, query: { limit: 200 } },
        }),
      ),
    retry: shouldRetry,
    refetchInterval: RESOURCE_POLL_MS,
  });
}
