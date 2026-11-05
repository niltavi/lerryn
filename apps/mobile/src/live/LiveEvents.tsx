import {
  invalidateForRunUpdate,
  invalidateLiveQueries,
  LiveModeProvider,
  parseEventData,
  type LiveMode,
  type RunUpdate,
} from '@lerryn/core';
import { focusManager, useQueryClient } from '@tanstack/react-query';
import { fetch as expoFetch } from 'expo/fetch';
import { useEffect, useState, type ReactNode } from 'react';
import { AppState } from 'react-native';

import { authHeaders } from '../lib/client';
import { readEventStream, reconnectDelay, StreamUnavailableError, type FetchLike } from '../lib/sse';
import type { StoredSession } from '../lib/session';

// The live mode context, useLiveMode and usePollInterval are core's, so the
// shared queries in @lerryn/core follow what this provider reports.
export { useLiveMode, usePollInterval, type LiveMode } from '@lerryn/core';

export const streamFetch = expoFetch as unknown as FetchLike;

/** Seconds between stream attempts after the server said it has no stream. */
const UNAVAILABLE_RETRY_MS = 60_000;

/** Whether the app is in the foreground; streams and polling pause in the background. */
export function useAppActive(): boolean {
  const [active, setActive] = useState(AppState.currentState === 'active');
  useEffect(() => {
    const sub = AppState.addEventListener('change', (s) => setActive(s === 'active'));
    return () => sub.remove();
  }, []);
  return active;
}

/**
 * Keeps one streamEvents connection open while the app is in the
 * foreground and turns each `run` event into query invalidations, per the
 * contract: events are not replayed, so every (re)connect refetches first.
 */
export function LiveEventsProvider({
  session,
  projectId,
  children,
}: {
  /** Null while signed out: no stream. */
  session: StoredSession | null;
  projectId: string | null;
  children: ReactNode;
}) {
  const queryClient = useQueryClient();
  const active = useAppActive();
  const [mode, setMode] = useState<LiveMode>('polling');

  // TanStack Query refetches stale queries when the app comes back.
  useEffect(() => {
    focusManager.setFocused(active);
  }, [active]);

  useEffect(() => {
    if (!active || !session) {
      setMode('polling');
      return;
    }
    const { server, cookie } = session;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;

    const query = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
    const url = `${server}/api/v1/events/stream${query}`;

    const onRun = (update: RunUpdate) => invalidateForRunUpdate(queryClient, update);

    const connect = async () => {
      try {
        await readEventStream({
          url,
          headers: authHeaders(cookie),
          credentials: 'omit',
          fetch: streamFetch,
          signal: controller.signal,
          onOpen: () => {
            attempt = 0;
            setMode('stream');
            invalidateLiveQueries(queryClient);
          },
          onMessage: (message) => {
            if (message.event === 'resync') {
              invalidateLiveQueries(queryClient);
            } else if (message.event === 'run') {
              const update = parseEventData<RunUpdate>(message.data);
              if (update?.run_id && update.task_id) onRun(update);
            }
          },
        });
        // The server closed the stream; reconnect soon.
        attempt = 1;
      } catch (err) {
        if (controller.signal.aborted) return;
        attempt++;
        if (err instanceof StreamUnavailableError) {
          setMode('polling');
          if (err.status === 401) {
            // The regular queries will see the same and sign out.
            invalidateLiveQueries(queryClient, { queryKey: ['me'] });
          }
          timer = setTimeout(connect, UNAVAILABLE_RETRY_MS);
          return;
        }
      }
      if (controller.signal.aborted) return;
      setMode('polling');
      timer = setTimeout(connect, reconnectDelay(attempt));
    };

    void connect();
    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [active, projectId, queryClient, session]);

  return <LiveModeProvider mode={mode}>{children}</LiveModeProvider>;
}

