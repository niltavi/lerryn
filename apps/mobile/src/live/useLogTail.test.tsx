// @vitest-environment jsdom

import { createLerrynClient } from '@lerryn/api';
import { ApiProvider, type LogLine } from '@lerryn/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, cleanup, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { readEventStream, type ReadEventStreamOptions } from '../lib/sse';
import { useLogTail } from './useLogTail';

vi.mock('../session/SessionProvider', () => ({
  useSignedIn: () => ({ session: { server: 'https://lerryn.test', cookie: 'session' } }),
}));

vi.mock('./LiveEvents', () => ({
  useAppActive: () => true,
  streamFetch: vi.fn(),
}));

vi.mock('../lib/sse', async (importOriginal) => ({
  ...await importOriginal<typeof import('../lib/sse')>(),
  readEventStream: vi.fn(),
}));

afterEach(() => {
  cleanup();
  vi.mocked(readEventStream).mockReset();
});

const line = (seq: number): LogLine => ({ seq, ts: '2026-11-03T08:00:00Z', stream: 'stdout', stage: null, text: `l${seq}` });
const page = (seq: number, cursor: string, dropped = 0, complete = true) =>
  new Response(JSON.stringify({ lines: [line(seq)], next_cursor: cursor, complete, dropped_lines: dropped }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });

describe('useLogTail earlier pages', () => {
  it.each([
    { name: 'switching runs', nextRun: 'r2' },
    { name: 'retrying the same run', nextRun: 'r1' },
  ])('discards old responses after $name without clearing the new request', async ({ nextRun }) => {
    let tailReads = 0;
    const pending: ((response: Response) => void)[] = [];
    const client = createLerrynClient({
      baseUrl: 'https://lerryn.test',
      fetch: async (req) => {
        if (new URL(req.url).searchParams.has('tail')) {
          tailReads++;
          return page(tailReads * 1000 + 1, `tail${tailReads}`);
        }
        return new Promise<Response>((resolve) => pending.push(resolve));
      },
    });
    const queryClient = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client}>{children}</ApiProvider>
      </QueryClientProvider>
    );
    const { result, rerender } = renderHook(({ runId }) => useLogTail(runId), { initialProps: { runId: 'r1' }, wrapper });
    await waitFor(() => expect(result.current.loaded).toBe(true));
    act(() => result.current.loadEarlier());
    await waitFor(() => expect(pending).toHaveLength(1));

    if (nextRun === 'r1') act(() => result.current.retry());
    else rerender({ runId: nextRun });
    await waitFor(() => expect(result.current.lines.map((l) => l.seq)).toEqual([2001]));
    expect(result.current.loadingEarlier).toBe(false);
    act(() => result.current.loadEarlier());
    await waitFor(() => expect(pending).toHaveLength(2));

    await act(async () => pending[0]!(page(1, 'old', 99)));
    expect(result.current.lines.map((l) => l.seq)).toEqual([2001]);
    expect(result.current.headCursor).toBeNull();
    expect(result.current.dropped).toBe(0);
    expect(result.current.loadingEarlier).toBe(true);

    await act(async () => pending[1]!(page(2, 'new')));
    expect(result.current.lines.map((l) => l.seq)).toEqual([2, 2001]);
    expect(result.current.headCursor).toBe('new');
    expect(result.current.loadingEarlier).toBe(false);
  });

  it('discards buffered stream lines when the previous run rejects after abort', async () => {
    let stream: { options: ReadEventStreamOptions; reject: (reason: unknown) => void } | undefined;
    vi.mocked(readEventStream).mockImplementation((options) => new Promise((_resolve, reject) => {
      stream = { options, reject };
    }));
    const client = createLerrynClient({
      baseUrl: 'https://lerryn.test',
      fetch: async (req) => new URL(req.url).pathname.includes('r1') ? page(1001, 'old', 0, false) : page(2001, 'new'),
    });
    const queryClient = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>
        <ApiProvider client={client}>{children}</ApiProvider>
      </QueryClientProvider>
    );
    const { result, rerender } = renderHook(({ runId }) => useLogTail(runId), { initialProps: { runId: 'r1' }, wrapper });
    await waitFor(() => expect(stream).toBeDefined());
    act(() => stream!.options.onMessage({ event: 'line', data: JSON.stringify(line(1002)), lastEventId: '1002' }));
    rerender({ runId: 'r2' });
    await waitFor(() => expect(result.current.lines.map((l) => l.seq)).toEqual([2001]));
    expect(stream!.options.signal.aborted).toBe(true);

    await act(async () => stream!.reject(new DOMException('The operation was aborted', 'AbortError')));
    expect(result.current.lines.map((l) => l.seq)).toEqual([2001]);
    act(() => {
      stream!.options.onOpen?.();
      stream!.options.onMessage({ event: 'truncated', data: JSON.stringify({ dropped_lines: 99 }), lastEventId: '' });
    });
    expect(result.current.mode).toBe('idle');
    expect(result.current.dropped).toBe(0);
  });
});
