import type { Schemas } from "@lerryn/api";
import { useQueryClient, type Query, type QueryClient, type QueryFilters } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

import { parseEventData } from "../logs/model";
import { queryKeys } from "../queries/keys";

export type RunUpdate = Schemas["RunUpdate"];

/**
 * How the screens learn about changes. `stream`: the event stream is open
 * and invalidates queries as runs change. `polling`: the stream is not
 * available (an older server, a proxy that buffers SSE, a dropped
 * connection), so queries refetch on an interval instead.
 */
export type LiveMode = "stream" | "polling";

const LiveModeContext = createContext<LiveMode>("polling");

/** Tells the queries below whether the event stream keeps them fresh. */
export function LiveModeProvider({ mode, children }: { mode: LiveMode; children: ReactNode }) {
  return <LiveModeContext.Provider value={mode}>{children}</LiveModeContext.Provider>;
}

export function useLiveMode(): LiveMode {
  return useContext(LiveModeContext);
}

/**
 * refetchInterval for a query: off while the event stream keeps it fresh,
 * `ms` while polling.
 */
export function usePollInterval(ms: number): number | false {
  return useLiveMode() === "stream" ? false : ms;
}

/** Callbacks of one server-sent event stream. */
export interface StreamCallbacks {
  /** The response is an event stream and events will follow. */
  onOpen?: () => void;
  onEvent: (event: string, data: string, lastEventId: string) => void;
  /**
   * The connection failed. `retrying` is true when the transport reconnects
   * by itself (a dropped connection) and false when it gave up (an error
   * answer, or no stream on this server).
   */
  onError: (retrying: boolean) => void;
}

/**
 * Opens a server-sent event stream on a same-origin API path, listening for
 * the named events, and returns a function that closes it. Each platform
 * provides its own: the browser uses EventSource, which sends the session
 * cookie and resumes with Last-Event-ID; React Native has none.
 */
export type OpenStream = (
  path: string,
  query: Record<string, string | number | undefined>,
  events: readonly string[],
  callbacks: StreamCallbacks,
) => () => void;

const StreamContext = createContext<OpenStream | null>(null);

/** Makes server-sent events available to the hooks below. Without it they poll. */
export function StreamProvider({ open, children }: { open: OpenStream | null; children: ReactNode }) {
  return <StreamContext.Provider value={open}>{children}</StreamContext.Provider>;
}

export function useOpenStream(): OpenStream | null {
  return useContext(StreamContext);
}

/** Builds an OpenStream on the browser's EventSource. */
export function eventSourceStream(base = ""): OpenStream {
  return (path, query, events, callbacks) => {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== "") params.set(k, String(v));
    const qs = params.toString();
    const source = new EventSource(`${base}${path}${qs ? `?${qs}` : ""}`);
    const listeners = events.map((name) => {
      const fn = (e: Event) => {
        const m = e as MessageEvent<string>;
        callbacks.onEvent(name, m.data, m.lastEventId);
      };
      source.addEventListener(name, fn);
      return [name, fn] as const;
    });
    source.onopen = () => callbacks.onOpen?.();
    source.onerror = () => callbacks.onError(source.readyState !== EventSource.CLOSED);
    return () => {
      for (const [name, fn] of listeners) source.removeEventListener(name, fn);
      source.onopen = null;
      source.onerror = null;
      source.close();
    };
  };
}

const liveRefreshes = new WeakMap<Query, { followUp: boolean }>();

/** Keeps an event burst from restarting a slow read, and rereads changes received during it. */
export function invalidateLiveQueries(queryClient: QueryClient, filters?: QueryFilters): void {
  const cache = queryClient.getQueryCache();
  for (const query of cache.findAll(filters)) {
    const pending = liveRefreshes.get(query);
    if (pending) {
      pending.followUp = true;
      query.invalidate();
      continue;
    }
    const state = { followUp: query.state.fetchStatus !== "idle" };
    query.invalidate();
    if (!query.isActive()) continue;
    liveRefreshes.set(query, state);
    void (async () => {
      try {
        while (cache.get(query.queryHash) === query && query.isActive()) {
          const before = query.state.dataUpdateCount;
          await queryClient.invalidateQueries({ predicate: (current) => current === query }, { cancelRefetch: false });
          // Cancellation can resolve with restored data. Only a new successful
          // cache write may start a trailing refresh; failed reads stop here too.
          if (!state.followUp || query.state.dataUpdateCount === before) return;
          state.followUp = false;
        }
      } finally {
        liveRefreshes.delete(query);
      }
    })();
  }
}

/**
 * Refreshes what a run change affects: the overview, the calendar, the
 * task lists, the task and its runs, and the run itself.
 */
export function invalidateForRunUpdate(queryClient: QueryClient, update: Pick<RunUpdate, "run_id" | "task_id">) {
  invalidateLiveQueries(queryClient, { queryKey: queryKeys.overviewAll });
  invalidateLiveQueries(queryClient, { queryKey: queryKeys.calendarAll });
  invalidateLiveQueries(queryClient, { queryKey: queryKeys.taskListAll });
  invalidateLiveQueries(queryClient, { queryKey: queryKeys.runListAll });
  invalidateLiveQueries(queryClient, { queryKey: queryKeys.task(update.task_id) });
  invalidateLiveQueries(queryClient, { queryKey: queryKeys.run(update.run_id), exact: true });
  invalidateLiveQueries(queryClient, { queryKey: queryKeys.runEvents(update.run_id) });
  invalidateLiveQueries(queryClient, { queryKey: queryKeys.runArtifacts(update.run_id) });
}

/** Seconds before trying the stream again after the server said it has none. */
export const STREAM_UNAVAILABLE_RETRY_MS = 60_000;

/**
 * Keeps one streamEvents connection open while `enabled` and turns each
 * `run` event into query invalidations. Events are not replayed, so every
 * (re)connect refetches first (contract of streamEvents). Returns whether
 * the stream is up, for usePollInterval through LiveModeProvider.
 */
export function useEventStream(options: { enabled: boolean; projectId?: string | null }): LiveMode {
  const open = useOpenStream();
  const queryClient = useQueryClient();
  const [mode, setMode] = useState<LiveMode>("polling");
  const { enabled, projectId } = options;

  useEffect(() => {
    if (!enabled || !open) {
      setMode("polling");
      return;
    }
    let close: (() => void) | null = null;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;

    const connect = () => {
      close = open("/api/v1/events/stream", { project_id: projectId ?? undefined }, ["run", "resync"], {
        onOpen: () => {
          setMode("stream");
          invalidateLiveQueries(queryClient);
        },
        onEvent: (event, data) => {
          if (event === "resync") {
            invalidateLiveQueries(queryClient);
            return;
          }
          const update = parseEventData<RunUpdate>(data);
          if (update?.run_id && update.task_id) invalidateForRunUpdate(queryClient, update);
        },
        onError: (retrying) => {
          setMode("polling");
          if (retrying || stopped) return;
          close?.();
          close = null;
          timer = setTimeout(connect, STREAM_UNAVAILABLE_RETRY_MS);
        },
      });
    };
    connect();
    return () => {
      stopped = true;
      close?.();
      if (timer) clearTimeout(timer);
    };
  }, [enabled, open, projectId, queryClient]);

  return mode;
}
