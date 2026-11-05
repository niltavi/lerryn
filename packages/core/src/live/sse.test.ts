import { describe, expect, it, vi } from "vitest";

import { readEventStream, reconnectDelay, SSEParser, StreamUnavailableError, type FetchLike } from "./sse";

describe("SSEParser", () => {
  it("parses events, ids, comments and multi-line data", () => {
    const p = new SSEParser();
    const out = p.feed(": heartbeat\n\nevent: line\nid: 7\ndata: {\"seq\":7}\n\ndata: a\ndata: b\n\n");
    expect(out).toEqual([
      { event: "line", data: "{\"seq\":7}", lastEventId: "7" },
      { event: "message", data: "a\nb", lastEventId: "7" },
    ]);
  });

  it("handles events split across chunks, including a split CRLF", () => {
    const p = new SSEParser();
    expect(p.feed("event: run\r")).toEqual([]);
    expect(p.feed("\ndata: {\"x\"")).toEqual([]);
    expect(p.feed(":1}\r\n\r\n")).toEqual([{ event: "run", data: "{\"x\":1}", lastEventId: "" }]);
  });

  it("accepts CR-only line endings and a field without a space", () => {
    const p = new SSEParser();
    // A trailing CR may be half of a CRLF, so it waits for the next chunk.
    expect(p.feed("data:x\r\r")).toEqual([]);
    expect(p.feed("data:y")).toEqual([{ event: "message", data: "x", lastEventId: "" }]);
  });

  it("ignores events without data and reads retry", () => {
    const p = new SSEParser("3");
    expect(p.feed("event: end\n\nretry: 5000\n\n")).toEqual([]);
    expect(p.retry).toBe(5000);
    expect(p.lastEventId).toBe("3");
  });
});

function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c));
      controller.close();
    },
  });
}

function fakeFetch(status: number, type: string, chunks: string[]): FetchLike & ReturnType<typeof vi.fn> {
  return vi.fn(async () => ({
    status,
    ok: status >= 200 && status < 300,
    headers: { get: (n: string) => (n.toLowerCase() === "content-type" ? type : null) },
    body: streamOf(chunks),
    text: async () => "",
  }));
}

describe("readEventStream", () => {
  it("delivers events and returns the last id, sending Last-Event-ID and auth headers", async () => {
    const fetch = fakeFetch(200, "text/event-stream; charset=utf-8", ["id: 41\nevent: line\nda", "ta: x\n\nid: 42\nevent: line\ndata: y\n\n"]);
    const got: string[] = [];
    const onOpen = vi.fn();
    const last = await readEventStream({
      url: "https://a.example/api/v1/runs/r/logs/stream",
      headers: { Cookie: "lerryn_session=abc" },
      fetch,
      signal: new AbortController().signal,
      lastEventId: "40",
      onOpen,
      onMessage: (m) => got.push(`${m.event}:${m.data}`),
    });
    expect(got).toEqual(['line:x', 'line:y']);
    expect(last).toBe('42');
    expect(onOpen).toHaveBeenCalledOnce();
    const init = fetch.mock.calls[0]![1] as { headers: Record<string, string> };
    expect(init.headers).toMatchObject({ Accept: 'text/event-stream', 'Last-Event-ID': '40', Cookie: 'lerryn_session=abc' });
  });

  it('drops an event cut off by the end of the stream', async () => {
    const got: string[] = [];
    await readEventStream({
      url: 'u',
      headers: {},
      fetch: fakeFetch(200, 'text/event-stream', ['data: whole\n\ndata: half']),
      signal: new AbortController().signal,
      onMessage: (m) => got.push(m.data),
    });
    expect(got).toEqual(['whole']);
  });

  it.each([
    [500, 'application/problem+json'],
    [404, 'application/problem+json'],
    [200, 'application/json'],
  ])('reports HTTP %i %s as unavailable, for the polling fallback', async (status, type) => {
    await expect(
      readEventStream({
        url: 'u',
        headers: {},
        fetch: fakeFetch(status, type, []),
        signal: new AbortController().signal,
        onMessage: () => {},
      }),
    ).rejects.toBeInstanceOf(StreamUnavailableError);
  });
});

describe('reconnectDelay', () => {
  it('backs off exponentially up to 30s', () => {
    const mid = () => 0.5;
    expect(reconnectDelay(1, mid)).toBe(1000);
    expect(reconnectDelay(3, mid)).toBe(4000);
    expect(reconnectDelay(20, mid)).toBe(30000);
  });
});
