// Reads about one task: its runs, triggers, credentials, versions and diffs.
// The console never edits a task (tech design 13.3), so there are no writes.
import type { Schemas } from "@lerryn/api";
import { useQueries, useQuery } from "@tanstack/react-query";

import { unwrap, useApi } from "../client";
import { shouldRetry } from "../errors";
import { usePollInterval } from "../live/live";
import { isRunActive } from "../status";
import { queryKeys } from "./keys";

export type TriggerSet = Schemas["TriggerSet"];
export type TaskCredentialList = Schemas["TaskCredentialList"];
export type TaskVersion = Schemas["TaskVersion"];
export type TaskVersionSummary = Schemas["TaskVersionSummary"];
export type TaskDiff = Schemas["TaskDiff"];
export type FileDiff = Schemas["FileDiff"];

const ACTIVE_POLL_MS = 3_000;

/** The latest runs of a task, newest first. */
export function useTaskRuns(taskId: string, limit = 30) {
  const api = useApi();
  const poll = usePollInterval(ACTIVE_POLL_MS * 3);
  return useQuery({
    queryKey: [...queryKeys.taskRuns(taskId), limit],
    queryFn: () => unwrap(api.GET("/api/v1/runs", { params: { query: { task_id: taskId, limit } } })),
    retry: shouldRetry,
    refetchInterval: poll,
  });
}

export function useTaskTriggers(taskId: string) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.taskTriggers(taskId),
    queryFn: () => unwrap(api.GET("/api/v1/tasks/{taskId}/triggers", { params: { path: { taskId } } })),
    retry: shouldRetry,
    staleTime: 60_000,
  });
}

export function useTaskCredentials(taskId: string) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.taskCredentials(taskId),
    queryFn: () => unwrap(api.GET("/api/v1/tasks/{taskId}/credentials", { params: { path: { taskId } } })),
    retry: shouldRetry,
    staleTime: 60_000,
  });
}

/** The task's versions, newest first, with test state and which one is active. */
export function useTaskVersions(taskId: string) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.taskVersions(taskId),
    queryFn: () =>
      unwrap(api.GET("/api/v1/tasks/{taskId}/versions", { params: { path: { taskId }, query: { limit: 50 } } })),
    retry: shouldRetry,
    staleTime: 30_000,
  });
}

/** One version's manifest and file list. Versions never change. */
export function useTaskVersion(taskId: string | undefined, seq: number | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.taskVersion(taskId ?? "", seq ?? 0),
    enabled: !!taskId && !!seq,
    queryFn: () =>
      unwrap(api.GET("/api/v1/tasks/{taskId}/versions/{seq}", { params: { path: { taskId: taskId!, seq: seq! } } })),
    retry: shouldRetry,
    staleTime: Infinity,
  });
}

/**
 * The difference between a version and `base`; without base the server
 * compares with the active version, or the previous one.
 */
export function useTaskDiff(taskId: string, seq: number | null, base: number | null = null) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.taskDiff(taskId, seq ?? 0, base),
    enabled: seq !== null,
    queryFn: () =>
      unwrap(
        api.GET("/api/v1/tasks/{taskId}/versions/{seq}/diff", {
          params: { path: { taskId, seq: seq! }, query: base ? { base } : {} },
        }),
      ),
    retry: shouldRetry,
    staleTime: Infinity,
  });
}

/**
 * The reported data of the last `count` finished runs that reported a
 * result, oldest first, for bars panels with `history`. Finished runs never
 * change, so each is read once.
 */
export function useResultHistory(taskId: string, count: number, enabled: boolean) {
  const api = useApi();
  const runs = useQuery({
    queryKey: [...queryKeys.taskRuns(taskId), "results", count],
    enabled,
    queryFn: () =>
      unwrap(api.GET("/api/v1/runs", { params: { query: { task_id: taskId, test: false, limit: Math.min(100, count * 3) } } })),
    retry: shouldRetry,
    staleTime: 30_000,
  });
  const ids = (runs.data?.items ?? [])
    .filter((r) => r.result_status !== null && !isRunActive(r.status))
    .slice(0, count)
    .map((r) => r.id);
  const details = useQueries({
    queries: ids.map((runId) => ({
      queryKey: queryKeys.run(runId),
      queryFn: () => unwrap(api.GET("/api/v1/runs/{runId}", { params: { path: { runId } } })),
      retry: shouldRetry,
      staleTime: Infinity,
    })),
  });
  const ready = runs.isSuccess && details.every((d) => d.isSuccess);
  const points = ready
    ? details
        .map((d) => d.data!)
        .reverse()
        .map((run) => ({ runId: run.id, at: run.finished_at ?? run.queued_at, data: run.result?.data ?? null }))
    : [];
  return { points, ready, isError: runs.isError || details.some((d) => d.isError) };
}
