import type { Schemas, LerrynClient } from "@lerryn/api";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import { useCallback, useEffect, useReducer, useRef, useState } from "react";

import { ApiError, isNotFound, unwrap, useApi, useApiSession } from "../client";
import { isExpired, shouldRetry } from "../errors";
import { useOpenStream, usePollInterval } from "../live/live";
import {
  applyEarlier,
  applyForward,
  applyStreamEnd,
  applyStreamLines,
  applyTail,
  initialLogState,
  isLogLine,
  parseEventData,
  type LogLine,
  type LogPage,
  type LogState,
} from "../logs/model";
import { isRunActive, type RunStatus } from "../status";
import { queryKeys } from "./keys";
import { useProjects } from "./overview";

export type Task = Schemas["Task"];
export type Run = Schemas["Run"];
export type RunStage = Schemas["RunStage"];
export type RunResult = Schemas["RunResult"];
export type RunError = Schemas["RunError"];
export type RunPage = Schemas["RunPage"];
export type RunEvent = Schemas["RunEvent"];
export type ToolCallRecord = Schemas["ToolCallRecord"];
export type Artifact = Schemas["Artifact"];
export type TriggerKind = Schemas["TriggerKind"];

/** Milliseconds between run polls while it is still active and no stream is open. */
const RUN_POLL_MS = 2_000;

const noRetryOnNotFound = (failureCount: number, err: unknown) => !isNotFound(err) && failureCount < 2;

export function useTask(taskId: string | undefined) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.task(taskId ?? ""),
    enabled: !!taskId,
    queryFn: () => unwrap(api.GET("/api/v1/tasks/{taskId}", { params: { path: { taskId: taskId! } } })),
    retry: noRetryOnNotFound,
  });
}

/**
 * A run, refreshed until it reaches a final state: every `pollMs` while the
 * event stream is down, and on each run event while it is up.
 */
export function useRun(runId: string, options: { pollMs?: number; enabled?: boolean } = {}) {
  const api = useApi();
  const poll = usePollInterval(options.pollMs ?? RUN_POLL_MS);
  return useQuery({
    queryKey: queryKeys.run(runId),
    enabled: options.enabled ?? true,
    queryFn: () => unwrap(api.GET("/api/v1/runs/{runId}", { params: { path: { runId } } })),
    retry: noRetryOnNotFound,
    refetchInterval: (query) => {
      const run = query.state.data;
      return run && isRunActive(run.status) ? poll : false;
    },
  });
}

/** The phone's name for useRun, polling every 3 seconds as tech design 13.4 says. */
export function useRunLive(runId: string) {
  return useRun(runId, { pollMs: 3_000 });
}

export interface RunListFilter {
  projectId?: string | null;
  taskId?: string;
  status?: RunStatus[];
}

const RUN_PAGE = 50;

/** Runs across the user's tasks, newest first, a page at a time. */
export function useRunList(filter: RunListFilter) {
  const api = useApi();
  const poll = usePollInterval(10_000);
  const key = { projectId: filter.projectId ?? null, taskId: filter.taskId ?? null, status: filter.status ?? [] };
  return useInfiniteQuery({
    queryKey: queryKeys.runList(key),
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/api/v1/runs", {
          params: {
            query: {
              limit: RUN_PAGE,
              ...(key.projectId ? { project_id: key.projectId } : {}),
              ...(key.taskId ? { task_id: key.taskId } : {}),
              ...(key.status.length ? { status: key.status } : {}),
              ...(pageParam ? { cursor: pageParam } : {}),
            },
          },
        }),
      ),
    getNextPageParam: (page) => page.next_cursor ?? null,
    select: (data: InfiniteData<RunPage, string | null>) => ({
      items: data.pages.flatMap((p) => p.items),
      hasMore: data.pages.at(-1)?.next_cursor != null,
    }),
    retry: shouldRetry,
    refetchInterval: poll,
  });
}

/** Runs started as reruns of this one, for the "rerun" link under a failed run. */
export function useReruns(runId: string, enabled: boolean) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.reruns(runId),
    enabled,
    queryFn: () => unwrap(api.GET("/api/v1/runs", { params: { query: { rerun_of: runId, limit: 1 } } })),
    retry: shouldRetry,
  });
}

/**
 * Whether the user may start, rerun and cancel runs in a project: maintainers
 * and owners may, viewers may not. Unknown until the project list loads.
 */
export function useCanControlRuns(projectId: string | undefined): boolean {
  const projects = useProjects();
  const role = projects.data?.items.find((p) => p.id === projectId)?.role;
  return role === "maintainer" || role === "owner";
}

/** Refreshes what a new run changes: the task's runs, run lists and the overview. */
function useRunStarted() {
  const queryClient = useQueryClient();
  return (run: Run, _variables: string, isCurrent: (() => boolean) | undefined) => {
    if (!isCurrent?.()) return;
    queryClient.setQueryData(queryKeys.run(run.id), run);
    void queryClient.invalidateQueries({ queryKey: queryKeys.task(run.task_id) });
    void queryClient.invalidateQueries({ queryKey: queryKeys.runListAll });
    void queryClient.invalidateQueries({ queryKey: queryKeys.overviewAll });
    if (run.rerun_of) void queryClient.invalidateQueries({ queryKey: queryKeys.reruns(run.rerun_of) });
  };
}

function runIdempotencyKey(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  // HTTP deployments have getRandomValues, but randomUUID needs a secure context.
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6]! & 0x0f) | 0x40;
  bytes[8] = (bytes[8]! & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/**
 * Starts a run of a task's latest version with its default params. Each press
 * carries a fresh idempotency key, so a request the network retries does not
 * start a second run.
 */
export function useStartRun() {
  const session = useApiSession();
  const api = session.client;
  const started = useRunStarted();
  return useMutation({
    mutationFn: (taskId: string) =>
      unwrap(
        api.POST("/api/v1/tasks/{taskId}/runs", {
          params: { path: { taskId }, header: { "Idempotency-Key": runIdempotencyKey() } },
        }),
      ),
    onMutate: session.capture,
    onSuccess: started,
  });
}

/** Runs a finished run again: the task's latest version, the run's params. */
export function useRerunRun() {
  const session = useApiSession();
  const api = session.client;
  const started = useRunStarted();
  return useMutation({
    mutationFn: (runId: string) =>
      unwrap(
        api.POST("/api/v1/runs/{runId}/rerun", {
          params: { path: { runId }, header: { "Idempotency-Key": runIdempotencyKey() } },
        }),
      ),
    onMutate: session.capture,
    onSuccess: started,
  });
}

/** Event pages read at most, so a run with many tool calls stays cheap. */
const EVENT_PAGES = 5;

/**
 * The events the platform recorded for a run: status changes, stages,
 * tool calls, artifacts and warnings. Refreshed while the run is active.
 */
export function useRunEvents(runId: string, active: boolean) {
  const api = useApi();
  const poll = usePollInterval(5_000);
  return useQuery({
    queryKey: queryKeys.runEvents(runId),
    queryFn: async () => {
      const events: RunEvent[] = [];
      let cursor: string | undefined;
      let complete = false;
      for (let i = 0; i < EVENT_PAGES; i++) {
        const page = await unwrap(
          api.GET("/api/v1/runs/{runId}/events", {
            params: { path: { runId }, query: { limit: 500, ...(cursor ? { cursor } : {}) } },
          }),
        );
        events.push(...page.events);
        complete = page.complete;
        if (page.events.length < 500 || page.next_cursor === cursor) break;
        cursor = page.next_cursor;
      }
      return { events, complete };
    },
    retry: shouldRetry,
    refetchInterval: active ? poll : false,
  });
}

/** The run's artifacts, including expired ones. */
export function useArtifacts(runId: string, active: boolean) {
  const api = useApi();
  const poll = usePollInterval(10_000);
  return useQuery({
    queryKey: queryKeys.runArtifacts(runId),
    queryFn: () =>
      unwrap(api.GET("/api/v1/runs/{runId}/artifacts", { params: { path: { runId }, query: { limit: 200 } } })),
    retry: shouldRetry,
    refetchInterval: active ? poll : false,
  });
}

/** Characters of an artifact shown in the preview; the rest is in the download. */
export const PREVIEW_CHARS = 64 * 1024;

/**
 * The text of an artifact whose `preview` is text or json, read inline. The
 * server never inlines HTML or scripts; the page renders the result as text.
 */
export function useArtifactPreview(artifact: Artifact | null) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.artifactPreview(artifact?.id ?? ""),
    enabled: !!artifact && artifact.preview !== "none" && !artifact.expired_at,
    queryFn: async ({ signal }) => {
      const stream = await unwrap(
        api.GET("/api/v1/artifacts/{artifactId}/download", {
          params: { path: { artifactId: artifact!.id }, query: { inline: true } },
          parseAs: "stream",
          signal,
        }),
      );
      const { text, cut } = await readArtifactPreview(stream, signal);
      let shown = text;
      if (artifact!.preview === "json" && !cut) {
        try {
          shown = JSON.stringify(JSON.parse(text), null, 2);
        } catch {
          // Not valid JSON after all; show it as it is.
        }
      }
      return { text: shown, cut };
    },
    retry: shouldRetry,
    staleTime: Infinity,
  });
}

/** Reads one extra UTF-16 unit to distinguish a full preview from a truncated one. */
async function readArtifactPreview(stream: ReadableStream<Uint8Array> | null | undefined, signal: AbortSignal) {
  if (!stream) return { text: "", cut: false };
  let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
  let ended = false;
  const abort = () => {
    void reader?.cancel().catch(() => {});
  };
  try {
    reader = stream.getReader();
    const decoder = new TextDecoder();
    let raw = "";
    signal.addEventListener("abort", abort, { once: true });
    signal.throwIfAborted();
    while (raw.length <= PREVIEW_CHARS) {
      const { done, value } = await reader.read();
      signal.throwIfAborted();
      if (done) {
        ended = true;
        raw += decoder.decode();
        break;
      }
      // A fetch chunk can exceed the preview. Decode bounded pieces rather
      // than creating a second, potentially file-sized string from it.
      let offset = 0;
      while (offset < value.length && raw.length <= PREVIEW_CHARS) {
        const remaining = PREVIEW_CHARS + 1 - raw.length;
        const end = Math.min(value.length, offset + remaining);
        raw += decoder.decode(value.subarray(offset, end), { stream: true }).slice(0, remaining);
        offset = end;
      }
    }
    return { text: raw.slice(0, PREVIEW_CHARS), cut: raw.length > PREVIEW_CHARS };
  } catch (err) {
    throw new ApiError(0, "network_error", null, err instanceof Error ? err.message : String(err));
  } finally {
    signal.removeEventListener("abort", abort);
    if (reader) {
      // A rejected or stalled cancel must not delay the preview or replace
      // the original read error.
      if (!ended) void reader.cancel().catch(() => {});
      reader.releaseLock();
    }
  }
}

/** Lines read on open; the API allows up to 1000 per page. */
export const LOG_FIRST_PAGE = 500;
const LOG_PAGE = 1000;
const FOLLOW_MS = 1_500;
const MAX_BACKOFF_MS = 15_000;
/** Lines pushed by the stream are applied in batches so a fast log does not re-render per line. */
const FLUSH_MS = 150;

type LogAction = { type: "reset" } | { type: "set"; state: LogState };

function logReducer(_state: LogState, action: LogAction): LogState {
  return action.type === "reset" ? initialLogState : action.state;
}

function readLogs(api: LerrynClient, runId: string, query: { cursor?: string; tail?: number; limit?: number }) {
  return unwrap(api.GET("/api/v1/runs/{runId}/logs", { params: { path: { runId }, query } }));
}

export interface RunLog extends LogState {
  /** The first page failed; the viewer shows an error with retry. */
  error: unknown;
  /** The log was removed by the retention policy; nothing is read. */
  expired: boolean;
  /** Consecutive failed follow attempts; above 0 the viewer shows "reconnecting". */
  reconnectAttempt: number;
  /** How new lines arrive: pushed over SSE, read by polling, or not at all because the run ended. */
  mode: "stream" | "polling" | "idle";
  loadingEarlier: boolean;
  /** Reads the next page of the missing lines before the tail. */
  loadEarlier: () => void;
  retry: () => void;
}

/**
 * Reads a run's log: the last lines first, then new lines as they arrive
 * until the run ends, and earlier lines on request. New lines come over
 * streamRunLogs when a StreamProvider is present (the browser resumes with
 * Last-Event-ID after a drop); otherwise, or when the stream is not
 * available, by polling readRunLogs with its cursor, backing off on
 * failures. Both paths merge by line number, so no line is lost or repeated.
 */
export function useRunLog(runId: string, options: { expired?: boolean } = {}): RunLog {
  const api = useApi();
  const open = useOpenStream();
  const queryClient = useQueryClient();
  const [state, dispatch] = useReducer(logReducer, initialLogState);
  const [error, setError] = useState<unknown>(null);
  const [expiredNow, setExpiredNow] = useState(false);
  const knownExpired = !!options.expired;
  const [attempt, setAttempt] = useState(0);
  const [mode, setMode] = useState<RunLog["mode"]>("idle");
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [generation, setGeneration] = useState(0);
  const stateRef = useRef(state);
  const earlierRequestRef = useRef<object | null>(null);

  const commit = useCallback((next: LogState) => {
    stateRef.current = next;
    dispatch({ type: "set", state: next });
  }, []);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    let closeStream: (() => void) | null = null;
    let pending: LogLine[] = [];
    stateRef.current = initialLogState;
    earlierRequestRef.current = null;
    dispatch({ type: "reset" });
    setError(null);
    setExpiredNow(false);
    setAttempt(0);
    setMode("idle");
    setLoadingEarlier(false);
    // The run says its log is gone; there is nothing to read.
    if (knownExpired) return;

    const schedule = (fn: () => void, ms: number) => {
      if (!cancelled) timer = setTimeout(fn, ms);
    };
    const ended = () => {
      setMode("idle");
      setAttempt(0);
      // The run is final now; read its final status, stages and result.
      void queryClient.invalidateQueries({ queryKey: queryKeys.run(runId), exact: true });
    };

    let failures = 0;
    const follow = async () => {
      const cursor = stateRef.current.tailCursor;
      if (cancelled || !cursor) return;
      setMode("polling");
      try {
        const page = await readLogs(api, runId, { cursor, limit: LOG_PAGE });
        if (cancelled) return;
        failures = 0;
        setAttempt(0);
        commit(applyForward(stateRef.current, page));
        if (page.complete) return ended();
        // A full page means there is more right now; read it at once.
        schedule(follow, page.lines.length >= LOG_PAGE ? 0 : FOLLOW_MS);
      } catch {
        if (cancelled) return;
        failures++;
        setAttempt(failures);
        schedule(follow, Math.min(FOLLOW_MS * 2 ** failures, MAX_BACKOFF_MS));
      }
    };

    const flush = () => {
      if (flushTimer) clearTimeout(flushTimer);
      flushTimer = undefined;
      if (pending.length === 0 || cancelled) return;
      commit(applyStreamLines(stateRef.current, pending));
      pending = [];
    };

    const stream = () => {
      if (!open) return void follow();
      // The tail page's cursor starts the stream right after it. The polling
      // fallback keeps using that cursor and merges away what it re-reads.
      const cursor = stateRef.current.tailCursor ?? undefined;
      closeStream = open(`/api/v1/runs/${encodeURIComponent(runId)}/logs/stream`, { cursor }, ["line", "truncated", "end"], {
        onOpen: () => {
          setMode("stream");
          setAttempt(0);
        },
        onEvent: (event, data) => {
          if (event === "line") {
            const line = parseEventData<LogLine>(data);
            if (isLogLine(line)) {
              pending.push(line);
              flushTimer ??= setTimeout(flush, FLUSH_MS);
            }
          } else if (event === "truncated") {
            const d = parseEventData<{ dropped_lines?: number }>(data);
            if (typeof d?.dropped_lines === "number") {
              flush();
              commit({ ...stateRef.current, dropped: Math.max(stateRef.current.dropped, d.dropped_lines) });
            }
          } else if (event === "end") {
            flush();
            const end = parseEventData<Schemas["LogStreamEnd"]>(data);
            commit(applyStreamEnd(stateRef.current, { dropped_lines: end?.dropped_lines ?? stateRef.current.dropped }));
            closeStream?.();
            closeStream = null;
            ended();
          }
        },
        onError: (retrying) => {
          flush();
          if (cancelled || stateRef.current.complete) return;
          if (retrying) {
            // The browser reconnects by itself with Last-Event-ID.
            setAttempt((a) => a + 1);
            return;
          }
          // No usable stream here; poll from the last cursor instead.
          closeStream?.();
          closeStream = null;
          setAttempt(0);
          void follow();
        },
      });
    };

    (async () => {
      try {
        // The first page always comes from readRunLogs: it says whether the
        // run already ended (then nothing follows) and gives the cursor.
        const page = await readLogs(api, runId, { tail: LOG_FIRST_PAGE });
        if (cancelled) return;
        commit(applyTail(initialLogState, page));
        if (!page.complete) stream();
      } catch (err) {
        if (cancelled) return;
        if (isExpired(err)) setExpiredNow(true);
        else setError(err);
      }
    })();

    return () => {
      cancelled = true;
      earlierRequestRef.current = null;
      closeStream?.();
      if (timer) clearTimeout(timer);
      if (flushTimer) clearTimeout(flushTimer);
    };
  }, [api, open, commit, queryClient, runId, generation, knownExpired]);

  const loadEarlier = useCallback(() => {
    if (earlierRequestRef.current) return;
    const request = {};
    earlierRequestRef.current = request;
    setLoadingEarlier(true);
    const cursor = stateRef.current.headCursor;
    readLogs(api, runId, { limit: LOG_PAGE, ...(cursor ? { cursor } : {}) })
      .then((page) => {
        // Switching runs or retrying can finish before the previous page arrives.
        if (earlierRequestRef.current === request) commit(applyEarlier(stateRef.current, page));
      })
      .catch(() => {
        // The gap stays and the button can be pressed again.
      })
      .finally(() => {
        if (earlierRequestRef.current !== request) return;
        earlierRequestRef.current = null;
        setLoadingEarlier(false);
      });
  }, [api, commit, runId]);

  const retry = useCallback(() => setGeneration((g) => g + 1), []);

  return { ...state, error, expired: knownExpired || expiredNow, reconnectAttempt: attempt, mode, loadingEarlier, loadEarlier, retry };
}

/**
 * Reads the whole log from line 1, for download. Pages are read one after
 * another so a long log does not flood the server.
 */
export async function fetchFullLog(api: LerrynClient, runId: string): Promise<LogLine[]> {
  const lines: LogLine[] = [];
  let cursor: string | undefined;
  for (;;) {
    const page: LogPage = await readLogs(api, runId, { limit: LOG_PAGE, ...(cursor ? { cursor } : {}) });
    lines.push(...page.lines);
    if (page.lines.length === 0 || page.complete || page.next_cursor === cursor) break;
    cursor = page.next_cursor;
  }
  return lines;
}
