import { createLerrynClient } from "@lerryn/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiProvider } from "../client";
import { queryKeys } from "./keys";
import { PREVIEW_CHARS, useArtifactPreview, type Artifact } from "./runs";

const clients: QueryClient[] = [];
afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
  vi.restoreAllMocks();
});

const artifact = (overrides: Partial<Artifact> = {}): Artifact => ({
  id: "artifact-1", run_id: "run-1", path: "report.txt", title: null,
  size: 100 * 1024 * 1024, content_type: "text/plain", sha256: "test-hash", preview: "text",
  created_at: "2026-11-04T00:00:00Z", expired_at: null,
  links: { download: "http://lerryn.test/api/v1/artifacts/artifact-1/download" },
  ...overrides,
});

function setup(fetch: (request: Request) => Promise<Response>) {
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
  clients.push(queryClient);
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client: queryClient }, createElement(ApiProvider, { client: api, children }));
  return { wrapper, queryClient };
}

function body(bytes: Uint8Array, chunkSize = 4093) {
  let emitted = 0;
  const cancel = vi.fn();
  const stream = new ReadableStream<Uint8Array>({
    pull(controller) {
      if (emitted === bytes.length) {
        controller.close();
        return;
      }
      const end = Math.min(bytes.length, emitted + chunkSize);
      controller.enqueue(bytes.subarray(emitted, end));
      emitted = end;
    },
    cancel,
  });
  return { stream, cancel, emitted: () => emitted };
}

function pendingBody() {
  let pulls = 0;
  const cancel = vi.fn();
  const stream = new ReadableStream<Uint8Array>({
    pull(controller) {
      pulls++;
      if (pulls === 1) controller.enqueue(new TextEncoder().encode("prefix"));
    },
    cancel,
  });
  return { stream, cancel, pulls: () => pulls };
}

describe("artifact preview over HTTP", () => {
  it("stops reading a large artifact after the preview and releases the reader", async () => {
    const source = body(new Uint8Array(1024 * 1024).fill(120), 8192);
    let request: Request | undefined;
    const { wrapper } = setup(async (value) => {
      request = value;
      return new Response(source.stream, { headers: { "Content-Length": String(1024 * 1024) } });
    });
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: "x".repeat(PREVIEW_CHARS), cut: true });
    expect(source.emitted()).toBeLessThanOrEqual(PREVIEW_CHARS + 1 + 2 * 8192);
    expect(source.cancel).toHaveBeenCalledTimes(1);
    expect(source.stream.locked).toBe(false);
    expect(new URL(request!.url).searchParams.get("inline")).toBe("true");
  });

  it("does not decode an entire oversized fetch chunk into another large string", async () => {
    const source = body(new Uint8Array(1024 * 1024).fill(120), 1024 * 1024);
    const decode = vi.spyOn(TextDecoder.prototype, "decode");
    const { wrapper } = setup(async () => new Response(source.stream));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.text).toHaveLength(PREVIEW_CHARS);
    for (const [input] of decode.mock.calls) {
      if (input) expect(input.byteLength).toBeLessThanOrEqual(PREVIEW_CHARS + 1);
    }
    expect(source.stream.locked).toBe(false);
  });

  it.each([0, PREVIEW_CHARS - 1, PREVIEW_CHARS, PREVIEW_CHARS + 1])("preserves the exact text boundary for %i characters", async (length) => {
    const raw = "x".repeat(length);
    const source = body(new TextEncoder().encode(raw));
    const { wrapper } = setup(async () => new Response(source.stream));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: raw.slice(0, PREVIEW_CHARS), cut: length > PREVIEW_CHARS });
    expect(source.stream.locked).toBe(false);
  });

  it("waits for EOF when content ends exactly at the limit, regardless of metadata", async () => {
    let controller: ReadableStreamDefaultController<Uint8Array> | undefined;
    const stream = new ReadableStream<Uint8Array>({ start(value) {
      controller = value;
      value.enqueue(new Uint8Array(PREVIEW_CHARS).fill(120));
    } });
    const { wrapper } = setup(async () => new Response(stream));
    const { result } = renderHook(() => useArtifactPreview(artifact({ size: PREVIEW_CHARS + 1000 })), { wrapper });
    await waitFor(() => expect(stream.locked).toBe(true));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(result.current.isPending).toBe(true);
    await act(async () => { controller!.close(); });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: "x".repeat(PREVIEW_CHARS), cut: false });
    expect(stream.locked).toBe(false);
  });

  it.each([1, 2, 5])("decodes UTF-8, BOM and split characters with %i-byte chunks", async (chunkSize) => {
    const raw = "\uFEFF中🙂é\n尾\u2028end";
    const source = body(new TextEncoder().encode(raw), chunkSize);
    const { wrapper } = setup(async () => new Response(source.stream));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: raw.slice(1), cut: false });
    expect(source.stream.locked).toBe(false);
  });

  it("keeps the existing UTF-16 boundary when it falls inside a surrogate pair", async () => {
    const raw = "中".repeat(PREVIEW_CHARS - 1) + "🙂tail";
    const source = body(new TextEncoder().encode(raw), 4096);
    const { wrapper } = setup(async () => new Response(source.stream));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: raw.slice(0, PREVIEW_CHARS), cut: true });
    expect(source.stream.locked).toBe(false);
  });

  it("flushes an incomplete UTF-8 suffix at EOF before deciding whether to truncate", async () => {
    const bytes = new Uint8Array(PREVIEW_CHARS + 1).fill(120);
    bytes.set([0xe2, 0x82], PREVIEW_CHARS - 1);
    const source = body(bytes, 4096);
    const { wrapper } = setup(async () => new Response(source.stream));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: "x".repeat(PREVIEW_CHARS - 1) + "�", cut: false });
  });

  it.each([
    { raw: '{"answer":42,"items":[1,2]}', expected: '{\n  "answer": 42,\n  "items": [\n    1,\n    2\n  ]\n}' },
    { raw: "{not-valid-json", expected: "{not-valid-json" },
    { raw: "", expected: "" },
  ])("formats complete JSON when valid and otherwise keeps its text: $raw", async ({ raw, expected }) => {
    const source = body(new TextEncoder().encode(raw), 2);
    const { wrapper } = setup(async () => new Response(source.stream));
    const { result } = renderHook(() => useArtifactPreview(artifact({ preview: "json" })), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: expected, cut: false });
  });

  it("shows only the original prefix for oversized JSON", async () => {
    const raw = JSON.stringify({ value: "x".repeat(PREVIEW_CHARS) });
    const source = body(new TextEncoder().encode(raw));
    const { wrapper } = setup(async () => new Response(source.stream));
    const { result } = renderHook(() => useArtifactPreview(artifact({ preview: "json" })), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: raw.slice(0, PREVIEW_CHARS), cut: true });
  });

  it.each([
    { status: 401, code: "unauthenticated", attempts: 1 },
    { status: 404, code: "not_found", attempts: 1 },
    { status: 410, code: "expired", attempts: 1 },
    { status: 429, code: "rate_limited", attempts: 3 },
  ])("retains API error and retry behavior for $status", async ({ status, code, attempts }) => {
    const fetch = vi.fn(async () => new Response(JSON.stringify({
      type: `urn:lerryn:error:${code}`, title: code, status, code, request_id: "review-request",
    }), { status, headers: { "Content-Type": "application/problem+json" } }));
    const { wrapper } = setup(fetch);
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toMatchObject({ status, code, requestId: "review-request" });
    expect(fetch).toHaveBeenCalledTimes(attempts);
    expect(result.current.data).toBeUndefined();
  });

  it("reports a response read failure without returning a partial preview", async () => {
    const streams: ReadableStream<Uint8Array>[] = [];
    const { wrapper } = setup(async () => {
      let pulls = 0;
      const stream = new ReadableStream<Uint8Array>({ pull(controller) {
        if (pulls++ === 0) controller.enqueue(new TextEncoder().encode("partial text"));
        else controller.error(new TypeError("connection closed"));
      } });
      streams.push(stream);
      return new Response(stream);
    });
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toMatchObject({ code: "network_error", status: 0, message: "connection closed" });
    expect(result.current.data).toBeUndefined();
    expect(streams).toHaveLength(3);
    expect(streams.every((stream) => !stream.locked)).toBe(true);
  });

  it("wraps a locked body as a network error without releasing another reader's lock", async () => {
    const response = new Response("locked body");
    const stream = response.body!;
    const foreignReader = stream.getReader();
    try {
      const { wrapper } = setup(async () => response);
      const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
      await waitFor(() => expect(result.current.isError).toBe(true));
      expect(result.current.error).toMatchObject({ code: "network_error", status: 0 });
      expect(result.current.data).toBeUndefined();
      expect(stream.locked).toBe(true);
      const { value } = await foreignReader.read();
      expect(new TextDecoder().decode(value)).toBe("locked body");
    } finally {
      await foreignReader.cancel();
      foreignReader.releaseLock();
    }
  });

  it("does not mask a ready preview with an error from cancelling the remaining body", async () => {
    const stream = new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new Uint8Array(PREVIEW_CHARS + 1).fill(120)); },
      cancel() { throw new Error("cleanup failed"); },
    });
    const { wrapper } = setup(async () => new Response(stream));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: "x".repeat(PREVIEW_CHARS), cut: true });
    expect(stream.locked).toBe(false);
  });

  it("releases the reader without waiting for a stalled underlying cancellation", async () => {
    const stream = new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new Uint8Array(PREVIEW_CHARS + 1).fill(120)); },
      cancel() { return new Promise<void>(() => {}); },
    });
    const { wrapper } = setup(async () => new Response(stream));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: "x".repeat(PREVIEW_CHARS), cut: true });
    expect(stream.locked).toBe(false);
  });

  it("does not mistake a body AbortError for query cancellation", async () => {
    const { wrapper } = setup(async () => new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.error(new DOMException("remote body aborted", "AbortError")); },
    })));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toMatchObject({ code: "network_error", status: 0 });
    expect(String(result.current.error)).toContain("remote body aborted");
    expect(result.current.data).toBeUndefined();
  });

  it.each([204, 200])("treats an empty successful response %i as an empty preview", async (status) => {
    const { wrapper } = setup(async () => new Response(null, { status, headers: { "Content-Length": "0" } }));
    const { result } = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ text: "", cut: false });
  });

  it.each(["unmount", "hide", "cancel query"])("cancels an unfinished body on %s and releases its reader", async (action) => {
    const source = pendingBody();
    let request: Request | undefined;
    const { wrapper, queryClient } = setup(async (value) => { request = value; return new Response(source.stream); });
    const hook = renderHook((value: Artifact | null) => useArtifactPreview(value), { initialProps: artifact() as Artifact | null, wrapper });
    await waitFor(() => expect(source.pulls()).toBeGreaterThan(1));
    if (action === "unmount") hook.unmount();
    else if (action === "hide") hook.rerender(null);
    else await act(async () => { await queryClient.cancelQueries({ queryKey: queryKeys.artifactPreview("artifact-1") }); });
    await waitFor(() => expect(source.stream.locked).toBe(false));
    expect(request!.signal.aborted).toBe(true);
    expect(source.cancel).toHaveBeenCalledTimes(1);
    expect(queryClient.getQueryData(queryKeys.artifactPreview("artifact-1"))).toBeUndefined();
    expect(queryClient.getQueryState(queryKeys.artifactPreview("artifact-1"))).toMatchObject({ error: null, fetchStatus: "idle" });
  });

  it("isolates a newly selected artifact from an older response that ignores abort", async () => {
    let finishOld: ((response: Response) => void) | undefined;
    let oldRequest: Request | undefined;
    const oldSource = pendingBody();
    const { wrapper, queryClient } = setup(async (request) => {
      if (request.url.includes("artifact-1")) {
        oldRequest = request;
        return new Promise<Response>((resolve) => { finishOld = resolve; });
      }
      return new Response("new artifact");
    });
    const hook = renderHook((value: Artifact) => useArtifactPreview(value), { initialProps: artifact(), wrapper });
    await waitFor(() => expect(finishOld).toBeDefined());
    hook.rerender(artifact({ id: "artifact-2" }));
    await waitFor(() => expect(hook.result.current.data?.text).toBe("new artifact"));
    expect(oldRequest!.signal.aborted).toBe(true);
    await act(async () => { finishOld!(new Response(oldSource.stream)); });
    await waitFor(() => expect(oldSource.cancel).toHaveBeenCalledTimes(1));
    expect(oldSource.stream.locked).toBe(false);
    expect(hook.result.current.data).toEqual({ text: "new artifact", cut: false });
    expect(queryClient.getQueryData(queryKeys.artifactPreview("artifact-1"))).toBeUndefined();
  });

  it("can refetch the same artifact after a cancelled pending preview", async () => {
    const source = pendingBody();
    let calls = 0;
    const { wrapper, queryClient } = setup(async () => new Response(calls++ === 0 ? source.stream : "fresh text"));
    const hook = renderHook(() => useArtifactPreview(artifact()), { wrapper });
    expect(hook.result.current.data).toBeUndefined();
    await waitFor(() => expect(source.pulls()).toBeGreaterThan(1));
    await act(async () => { await queryClient.cancelQueries({ queryKey: queryKeys.artifactPreview("artifact-1") }); });
    await waitFor(() => expect(source.stream.locked).toBe(false));
    await act(async () => { await hook.result.current.refetch(); });
    await waitFor(() => expect(hook.result.current.data).toEqual({ text: "fresh text", cut: false }));
    expect(calls).toBe(2);
  });
});
