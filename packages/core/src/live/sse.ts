// Server-Sent Events over a streaming fetch. React Native has no
// EventSource; Expo's fetch exposes the response body as a ReadableStream,
// so this reads it and parses the event stream format itself
// (https://html.spec.whatwg.org/multipage/server-sent-events.html).

export interface SSEMessage {
  /** Event type; "message" when the server did not name one. */
  event: string;
  data: string;
  /** The last event ID seen so far in the stream, "" if none. */
  lastEventId: string;
}

/** Incremental parser: feed text as it arrives, get complete events back. */
export class SSEParser {
  private buffer = "";
  private event = "";
  private data: string[] = [];
  private hasData = false;
  lastEventId = "";
  /** Reconnection delay the server asked for with `retry:`, in ms. */
  retry: number | null = null;

  constructor(lastEventId = "") {
    this.lastEventId = lastEventId;
  }

  feed(chunk: string): SSEMessage[] {
    this.buffer += chunk;
    const out: SSEMessage[] = [];
    // Lines end with CRLF, LF or CR. A trailing CR may be the first half of
    // a CRLF split across chunks, so keep it until the next chunk.
    let start = 0;
    for (let i = 0; i < this.buffer.length; i++) {
      const c = this.buffer[i];
      if (c !== "\n" && c !== "\r") continue;
      if (c === "\r" && i === this.buffer.length - 1) break;
      const line = this.buffer.slice(start, i);
      if (c === "\r" && this.buffer[i + 1] === "\n") i++;
      start = i + 1;
      const message = this.line(line);
      if (message) out.push(message);
    }
    this.buffer = this.buffer.slice(start);
    return out;
  }

  private line(line: string): SSEMessage | null {
    if (line === "") return this.dispatch();
    if (line.startsWith(":")) return null; // comment, e.g. heartbeat
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "event":
        this.event = value;
        break;
      case "data":
        this.data.push(value);
        this.hasData = true;
        break;
      case "id":
        if (!value.includes("\0")) this.lastEventId = value;
        break;
      case "retry":
        if (/^\d+$/.test(value)) this.retry = Number(value);
        break;
    }
    return null;
  }

  private dispatch(): SSEMessage | null {
    const event = this.event || "message";
    const data = this.data.join("\n");
    const had = this.hasData;
    this.event = "";
    this.data = [];
    this.hasData = false;
    if (!had) return null;
    return { event, data, lastEventId: this.lastEventId };
  }
}

/** The stream could not be opened as SSE; the caller should fall back to polling. */
export class StreamUnavailableError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "StreamUnavailableError";
    this.status = status;
  }
}

export interface FetchLike {
  (
    url: string,
    init: { method: string; headers: Record<string, string>; signal?: AbortSignal; credentials?: RequestCredentials },
  ): Promise<{
    status: number;
    ok: boolean;
    headers: { get(name: string): string | null };
    body: ReadableStream<Uint8Array> | null;
    text(): Promise<string>;
  }>;
}

export interface ReadEventStreamOptions {
  url: string;
  headers: Record<string, string>;
  credentials?: RequestCredentials;
  fetch: FetchLike;
  signal: AbortSignal;
  /** Resume after this event ID (sent as Last-Event-ID). */
  lastEventId?: string;
  onOpen?: () => void;
  onMessage: (message: SSEMessage) => void;
}

/**
 * Opens one SSE connection and delivers its events until the server closes
 * it or the signal aborts. Resolves with the last event ID, so the caller
 * can reconnect from there. Throws StreamUnavailableError when the response
 * is not an event stream (for example an error, or a server without the
 * endpoint yet), and the fetch error when the connection drops.
 */
export async function readEventStream(options: ReadEventStreamOptions): Promise<string> {
  const headers: Record<string, string> = { Accept: "text/event-stream", ...options.headers };
  if (options.lastEventId) headers["Last-Event-ID"] = options.lastEventId;
  const response = await options.fetch(options.url, {
    method: "GET",
    headers,
    signal: options.signal,
    credentials: options.credentials,
  });
  const type = response.headers.get("content-type") ?? "";
  if (!response.ok || !type.toLowerCase().startsWith("text/event-stream") || !response.body) {
    // Read the body so the connection can be reused; its content is not needed.
    await response.text().catch(() => "");
    throw new StreamUnavailableError(response.status, `event stream unavailable (HTTP ${response.status})`);
  }
  options.onOpen?.();
  const parser = new SSEParser(options.lastEventId ?? '');
  const decoder = new TextDecoder();
  const reader = response.body.getReader();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      for (const message of parser.feed(decoder.decode(value, { stream: true }))) {
        options.onMessage(message);
      }
    }
    // An event without its closing blank line is discarded, as the spec says.
    for (const message of parser.feed(decoder.decode())) options.onMessage(message);
  } finally {
    reader.releaseLock?.();
  }
  return parser.lastEventId;
}

/** Delay before reconnect attempt n (1-based): 1s, 2s, 4s … capped at 30s, with jitter. */
export function reconnectDelay(attempt: number, random: () => number = Math.random): number {
  const base = Math.min(1000 * 2 ** Math.max(0, attempt - 1), 30_000);
  return Math.round(base * (0.8 + random() * 0.4));
}
