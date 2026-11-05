// SPDX-License-Identifier: Apache-2.0
//
// Report stages and results from a Lerryn task.
//
// Every call appends one JSON line to the file named by LERRYN_EVENTS_FILE.
// The runner reads that file and forwards the events to the platform, so
// this works without network access. The line format is described in
// runtimes/sdk/python/README.md; this module writes the same format as the
// Python SDK. Only Node built-ins are used.

import { closeSync, constants, openSync, writeSync } from "node:fs";
import { randomBytes } from "node:crypto";

/** Version of the event line format, written as "v" in every line. */
export const FORMAT_VERSION = 1;

const TOOLS_ENV = "LERRYN_TOOLS";
const EVENTS_FILE_ENV = "LERRYN_EVENTS_FILE";
const PARAMS_ENV = "LERRYN_PARAMS";
const LOG_MARKER_ENV = "LERRYN_LOG_MARKER";

/** Limits enforced by the runner. Longer messages and summaries are cut;
 * larger data is rejected here so the task notices instead of losing it. */
export const MESSAGE_MAX_BYTES = 1024;
export const SUMMARY_MAX_BYTES = 2048;
export const DATA_MAX_BYTES = 64 * 1024;

export const RESULT_STATUSES = Object.freeze(["ok", "partial", "failed"]);

// Same rule as stage IDs in the task manifest.
const STAGE_ID = /^[a-z0-9][a-z0-9_-]{0,62}$/;

/** Returns the run's parameters, decoded from LERRYN_PARAMS. */
export function params() {
  const raw = process.env[PARAMS_ENV] ?? "";
  if (raw.trim() === "") {
    return {};
  }
  const value = JSON.parse(raw);
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new TypeError(`${PARAMS_ENV} is not a JSON object`);
  }
  return value;
}

/** Reports that the stage started. */
export function stageStart(stageId, message) {
  emitStage("stage.start", stageId, message);
}

/** Reports that the stage finished successfully. */
export function stageDone(stageId, message) {
  emitStage("stage.done", stageId, message);
}

/** Reports that the stage failed. */
export function stageFail(stageId, message) {
  emitStage("stage.fail", stageId, message);
}

/**
 * Runs fn as a stage: done when it returns, or when the promise it returns
 * resolves; failed when it throws or rejects, and the error is passed on.
 * Returns what fn returns.
 */
export function stage(stageId, fn) {
  stageStart(stageId);
  let value;
  try {
    value = fn();
  } catch (err) {
    stageFail(stageId, describe(err));
    throw err;
  }
  if (value !== null && typeof value === "object" && typeof value.then === "function") {
    return Promise.resolve(value).then(
      (v) => {
        stageDone(stageId);
        return v;
      },
      (err) => {
        stageFail(stageId, describe(err));
        throw err;
      },
    );
  }
  stageDone(stageId);
  return value;
}

/**
 * Reports the business result of the run. status is "ok", "partial" or
 * "failed"; data is any value JSON.stringify accepts, at most 64 KiB once
 * encoded.
 */
export function result(status, summary, data) {
  if (!RESULT_STATUSES.includes(status)) {
    throw new RangeError(`status must be one of ${RESULT_STATUSES.join(", ")}, got ${JSON.stringify(status)}`);
  }
  if (typeof summary !== "string") {
    throw new TypeError("summary must be a string");
  }
  const event = { kind: "result", status, summary: truncate(summary, SUMMARY_MAX_BYTES) };
  if (data !== undefined && data !== null) {
    const encoded = JSON.stringify(data);
    if (encoded === undefined) {
      throw new TypeError("result data cannot be encoded as JSON");
    }
    const size = Buffer.byteLength(encoded, "utf8");
    if (size > DATA_MAX_BYTES) {
      throw new RangeError(`result data is ${size} bytes encoded, the limit is ${DATA_MAX_BYTES}`);
    }
    event.data = JSON.parse(encoded);
  }
  emit(event);
}

/** A tool call failed; result holds the tool's error result, if any. */
export class ToolError extends Error {
  constructor(message, result) {
    super(message);
    this.name = "ToolError";
    this.result = result;
  }
}

/** MCP protocol version sent in initialize; servers may answer with another. */
export const MCP_PROTOCOL_VERSION = "2025-06-18";
const ARGS_SUMMARY_MAX_CHARS = 200;

/**
 * Calls the MCP tool `name` of the registered MCP tool `tool`, which the
 * manifest lists in `tools`. `options.headers` go with every request, for
 * example an Authorization header built from a credential; the default
 * `options.timeout` is 60 seconds per request. The call is recorded in the
 * run's events (kind tool.call) with a redacted summary of the arguments;
 * the response is not recorded.
 *
 * Resolves to the MCP CallToolResult. Rejects with a ToolError when the
 * call fails or the tool returns isError.
 */
export async function mcpCall(tool, name, args = {}, options = {}) {
  const summary = cutChars(JSON.stringify(args ?? {}), ARGS_SUMMARY_MAX_CHARS);
  const started = Date.now();
  let res;
  try {
    const session = new McpSession(mcpUrl(tool), options.headers ?? {}, (options.timeout ?? 60) * 1000);
    res = await session.call(name, args ?? {});
  } catch (err) {
    const message = err instanceof ToolError ? err.message : `cannot call the tool: ${describe(err)}`;
    recordCall(tool, name, false, started, summary, message);
    throw err instanceof ToolError ? err : new ToolError(message);
  }
  if (res.isError) {
    const message = firstText(res) || "the tool returned an error";
    recordCall(tool, name, false, started, summary, message);
    throw new ToolError(`${tool} ${name}: ${message}`, res);
  }
  recordCall(tool, name, true, started, summary);
  return res;
}

function mcpUrl(tool) {
  let tools = [];
  try {
    tools = JSON.parse(process.env[TOOLS_ENV] || "[]");
  } catch {
    tools = [];
  }
  const t = Array.isArray(tools) ? tools.find((x) => x && x.name === tool) : undefined;
  if (!t) {
    throw new ToolError(`tool ${tool} is not one of this run's tools; list it in the manifest's tools`);
  }
  if (t.kind !== "mcp") {
    throw new ToolError(`tool ${tool} is a ${t.kind} tool; only mcp tools can be called this way`);
  }
  return t.url;
}

class McpSession {
  constructor(url, headers, timeoutMs) {
    this.url = url;
    this.headers = headers;
    this.timeoutMs = timeoutMs;
    this.session = undefined;
    this.version = undefined;
  }

  async call(name, args) {
    const init = await this.request(1, "initialize", {
      protocolVersion: MCP_PROTOCOL_VERSION,
      capabilities: {},
      clientInfo: { name: "lerryn-sbx", version: "1" },
    });
    this.version = typeof init.protocolVersion === "string" ? init.protocolVersion : MCP_PROTOCOL_VERSION;
    try {
      await this.post({ jsonrpc: "2.0", method: "notifications/initialized" });
      return await this.request(2, "tools/call", { name, arguments: args });
    } finally {
      await this.end();
    }
  }

  async request(id, method, params) {
    const { body, type } = await this.post({ jsonrpc: "2.0", id, method, params });
    for (const msg of messages(body, type)) {
      if (msg && msg.id === id) {
        if (msg.error) {
          throw new ToolError(`${method}: ${msg.error.message ?? "error"} (code ${msg.error.code})`);
        }
        if (msg.result && typeof msg.result === "object") {
          return msg.result;
        }
      }
    }
    throw new ToolError(`${method}: the server sent no answer`);
  }

  headersFor() {
    const h = { "Content-Type": "application/json", Accept: "application/json, text/event-stream", ...this.headers };
    if (this.session) h["Mcp-Session-Id"] = this.session;
    if (this.version) h["MCP-Protocol-Version"] = this.version;
    return h;
  }

  async post(message) {
    let resp;
    try {
      resp = await fetch(this.url, {
        method: "POST",
        headers: this.headersFor(),
        body: JSON.stringify(message),
        signal: AbortSignal.timeout(this.timeoutMs),
      });
    } catch (err) {
      throw new ToolError(`cannot reach the tool: ${describe(err.cause ?? err)}`);
    }
    if (!resp.ok) {
      throw new ToolError(`HTTP ${resp.status} from the tool`);
    }
    const sid = resp.headers.get("mcp-session-id");
    if (sid) this.session = sid;
    return { body: await resp.text(), type: resp.headers.get("content-type") ?? "" };
  }

  async end() {
    if (!this.session) return;
    try {
      await fetch(this.url, { method: "DELETE", headers: this.headersFor(), signal: AbortSignal.timeout(5000) });
    } catch {
      // The session times out on the server.
    }
  }
}

function messages(body, type) {
  if (!type.includes("text/event-stream")) {
    if (body.trim() === "") return [];
    let msg;
    try {
      msg = JSON.parse(body);
    } catch {
      throw new ToolError("the tool's answer is not JSON");
    }
    return Array.isArray(msg) ? msg : [msg];
  }
  const out = [];
  let data = [];
  for (const line of [...body.split(/\r?\n/), ""]) {
    if (line.startsWith("data:")) {
      data.push(line.slice(line.startsWith("data: ") ? 6 : 5));
    } else if (line === "" && data.length > 0) {
      try {
        out.push(JSON.parse(data.join("\n")));
      } catch {
        // Not a JSON-RPC message.
      }
      data = [];
    }
  }
  return out;
}

function firstText(res) {
  for (const c of res.content ?? []) {
    if (c && c.type === "text" && typeof c.text === "string") return cutChars(c.text, 500);
  }
  return "";
}

function recordCall(tool, name, ok, started, summary, error) {
  const event = { kind: "tool.call", tool, method: name, ok, duration_ms: Date.now() - started, args_summary: summary };
  if (error) event.error = truncate(error, MESSAGE_MAX_BYTES);
  try {
    emit(event);
  } catch {
    // Recording must not turn a successful call into a failure.
  }
}

function cutChars(text, limit) {
  const chars = Array.from(text);
  return chars.length <= limit ? text : chars.slice(0, limit - 1).join("") + "…";
}

function emitStage(kind, stageId, message) {
  if (typeof stageId !== "string" || !STAGE_ID.test(stageId)) {
    throw new RangeError(
      `stage id ${JSON.stringify(stageId)} must be 1 to 63 lowercase letters, digits, '_' or '-', ` +
        "starting with a letter or digit",
    );
  }
  const event = { kind, stage: stageId };
  if (message !== undefined && message !== null) {
    event.message = truncate(String(message), MESSAGE_MAX_BYTES);
  }
  // The marker goes to stdout, the same stream as the task's own output,
  // so the runner switches the stage of log lines at exactly this point.
  // process.stdout keeps the order of earlier console.log calls.
  const token = process.env[LOG_MARKER_ENV];
  if (token && process.env[EVENTS_FILE_ENV]) {
    const mark = randomBytes(8).toString("hex");
    try {
      process.stdout.write(`\x1elerryn ${token} ${mark} ${kind} ${stageId}\n`);
      event.mark = mark;
    } catch {
      // Without a marker the runner labels lines from the events file.
    }
  }
  emit(event);
}

function emit(event) {
  const record = { v: FORMAT_VERSION, ts: new Date().toISOString(), ...event };
  const line = JSON.stringify(record) + "\n";
  const path = process.env[EVENTS_FILE_ENV];
  if (!path) {
    // Outside Lerryn, show the event so a local run stays readable.
    process.stderr.write("sbx: " + line);
    return;
  }
  // One write per line on an O_APPEND descriptor: concurrent writers (for
  // example the sbx command and this module in one task) never interleave
  // within a line.
  const buf = Buffer.from(line, "utf8");
  const fd = openSync(path, constants.O_WRONLY | constants.O_APPEND | constants.O_CREAT, 0o644);
  try {
    let off = 0;
    while (off < buf.length) {
      off += writeSync(fd, buf, off, buf.length - off);
    }
  } finally {
    closeSync(fd);
  }
}

function truncate(text, limit) {
  const encoded = Buffer.from(text, "utf8");
  if (encoded.length <= limit) {
    return text;
  }
  const ellipsis = "…";
  let n = limit - Buffer.byteLength(ellipsis, "utf8");
  // Back up to the start of a character.
  while (n > 0 && (encoded[n] & 0xc0) === 0x80) {
    n--;
  }
  return encoded.subarray(0, n).toString("utf8") + ellipsis;
}

function describe(err) {
  if (err instanceof Error) {
    return err.message ? `${err.name}: ${err.message}` : err.name;
  }
  return String(err);
}
