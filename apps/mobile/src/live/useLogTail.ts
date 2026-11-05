import type { Schemas } from '@lerryn/api';
import {
  applyEarlier,
  applyForward,
  applyTail,
  initialLogState,
  isExpired,
  queryKeys,
  unwrap,
  useApi,
  type LogState,
} from '@lerryn/core';
import { useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useRef, useState } from 'react';

import { authHeaders } from '../lib/client';
import { applyStreamEnd, applyStreamLines, isLogLine, lastSeq, MOBILE_MAX_LINES, parseEventData } from '../lib/logs';
import { readEventStream, reconnectDelay, StreamUnavailableError } from '../lib/sse';
import { useSignedIn } from '../session/SessionProvider';
import { streamFetch, useAppActive } from './LiveEvents';

type LogLine = Schemas['LogLine'];

/** Lines read when the run page opens. */
const TAIL = 200;
const PAGE = 500;
const POLL_MS = 2_000;
/** Lines pushed by the stream are applied in batches so a fast log does not re-render per line. */
const FLUSH_MS = 150;

export interface LogTail extends LogState {
  error: unknown;
  /** stream: lines arrive over SSE; polling: readRunLogs on an interval. */
  mode: 'stream' | 'polling' | 'idle';
  /** The log was removed by the deployment's retention policy; there is nothing to read. */
  expired: boolean;
  loadingEarlier: boolean;
  loadEarlier: () => void;
  retry: () => void;
}

/**
 * The end of a run's log, kept current: the last lines first, then new lines
 * over streamRunLogs (resuming with Last-Event-ID after a drop), or by
 * polling readRunLogs with its cursor when the stream is not available.
 * Both paths merge by line number, so switching between them never loses or
 * repeats a line.
 *
 * A log removed by retention answers 410 expired. With `expired` (the run's
 * logs_expired_at is set) nothing is read at all; otherwise the first 410
 * stops reading and the hook reports the log as expired instead of failed.
 */
export function useLogTail(runId: string, options: { expired?: boolean } = {}): LogTail {
  const knownExpired = !!options.expired;
  const api = useApi();
  const { session } = useSignedIn();
  const queryClient = useQueryClient();
  const active = useAppActive();
  const [state, setState] = useState<LogState>(initialLogState);
  const [error, setError] = useState<unknown>(null);
  const [mode, setMode] = useState<LogTail['mode']>('idle');
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [generation, setGeneration] = useState(0);
  const [expiredNow, setExpiredNow] = useState(false);
  const ref = useRef(state);
  const earlierRequestRef = useRef<object | null>(null);

  const commit = useCallback((next: LogState) => {
    ref.current = next;
    setState(next);
  }, []);

  // A new run starts from nothing.
  useEffect(() => {
    earlierRequestRef.current = null;
    commit(initialLogState);
    setError(null);
    setExpiredNow(false);
    setLoadingEarlier(false);
    return () => {
      earlierRequestRef.current = null;
    };
  }, [api, runId, generation, knownExpired, commit]);

  useEffect(() => {
    if (knownExpired) {
      setMode('idle');
      return;
    }
    if (!active) {
      setMode('idle');
      return;
    }
    const controller = new AbortController();
    const signal = controller.signal;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    let pending: LogLine[] = [];

    const read = (query: { tail?: number; cursor?: string; limit?: number }) =>
      unwrap(api.GET('/api/v1/runs/{runId}/logs', { params: { path: { runId }, query } }));

    const ended = () => {
      setMode('idle');
      // The run is final now; read its final status, stages and result.
      void queryClient.invalidateQueries({ queryKey: queryKeys.run(runId), exact: true });
    };

    const poll = async () => {
      if (signal.aborted) return;
      setMode('polling');
      try {
        const cursor = ref.current.tailCursor;
        const page = cursor ? await read({ cursor, limit: PAGE }) : await read({ tail: TAIL });
        if (signal.aborted) return;
        commit(cursor ? applyForward(ref.current, page, MOBILE_MAX_LINES) : applyTail(ref.current, page));
        if (page.complete) return ended();
        timer = setTimeout(poll, page.lines.length >= PAGE ? 0 : POLL_MS);
      } catch (err) {
        if (signal.aborted) return;
        if (isExpired(err)) {
          setExpiredNow(true);
          return setMode('idle');
        }
        timer = setTimeout(poll, POLL_MS * 4);
      }
    };

    const flush = () => {
      flushTimer = undefined;
      if (signal.aborted || pending.length === 0) return;
      commit(applyStreamLines(ref.current, pending));
      pending = [];
    };

    let attempt = 0;
    const stream = async () => {
      try {
        await readEventStream({
          url: `${session.server}/api/v1/runs/${encodeURIComponent(runId)}/logs/stream`,
          headers: authHeaders(session.cookie),
          credentials: 'omit',
          fetch: streamFetch,
          signal,
          lastEventId: lastSeq(ref.current),
          onOpen: () => {
            if (signal.aborted) return;
            attempt = 0;
            setMode('stream');
          },
          onMessage: (message) => {
            if (signal.aborted) return;
            if (message.event === 'line') {
              const line = parseEventData<LogLine>(message.data);
              if (isLogLine(line)) {
                pending.push(line);
                flushTimer ??= setTimeout(flush, FLUSH_MS);
              }
            } else if (message.event === 'truncated') {
              const data = parseEventData<{ dropped_lines?: number }>(message.data);
              if (typeof data?.dropped_lines === 'number') {
                flush();
                commit({ ...ref.current, dropped: Math.max(ref.current.dropped, data.dropped_lines) });
              }
            } else if (message.event === 'end') {
              flush();
              const end = parseEventData<Schemas['LogStreamEnd']>(message.data);
              commit(applyStreamEnd(ref.current, { dropped_lines: end?.dropped_lines ?? ref.current.dropped }));
            }
          },
        });
        if (signal.aborted) return;
        flush();
        if (ref.current.complete) return ended();
        attempt++;
      } catch (err) {
        if (signal.aborted) return;
        flush();
        if (err instanceof StreamUnavailableError) {
          // No usable stream on this server or path; poll from where we are.
          return void poll();
        }
        attempt++;
      }
      if (signal.aborted) return;
      if (attempt > 3) return void poll();
      setMode('polling');
      timer = setTimeout(stream, reconnectDelay(attempt));
    };

    (async () => {
      try {
        // The first page always comes from readRunLogs: it says whether the
        // run already ended (then no stream is needed) and gives a cursor for
        // the polling fallback.
        if (!ref.current.loaded) {
          const page = await read({ tail: TAIL });
          if (signal.aborted) return;
          commit(applyTail(ref.current, page));
        }
        if (ref.current.complete) return setMode('idle');
        void stream();
      } catch (err) {
        if (signal.aborted) return;
        if (isExpired(err)) {
          setExpiredNow(true);
          setMode('idle');
        } else {
          setError(err);
        }
      }
    })();

    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
      if (flushTimer) clearTimeout(flushTimer);
    };
  }, [active, api, commit, generation, knownExpired, queryClient, runId, session.cookie, session.server]);

  const loadEarlier = useCallback(() => {
    if (earlierRequestRef.current) return;
    const request = {};
    earlierRequestRef.current = request;
    setLoadingEarlier(true);
    const cursor = ref.current.headCursor;
    unwrap(
      api.GET('/api/v1/runs/{runId}/logs', {
        params: { path: { runId }, query: { limit: PAGE, ...(cursor ? { cursor } : {}) } },
      }),
    )
      .then((page) => {
        // Switching runs or retrying can finish before the previous page arrives.
        if (earlierRequestRef.current === request) commit(applyEarlier(ref.current, page));
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

  return { ...state, error, expired: knownExpired || expiredNow, mode, loadingEarlier, loadEarlier, retry };
}
