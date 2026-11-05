/**
 * A small HTTP client for the e2e suite: it seeds data the way an agent
 * would (access token) and signs in the way the console does (session
 * cookie). It speaks the public API only.
 */

export class ApiError extends Error {
  readonly status: number;
  readonly body: string;
  constructor(status: number, body: string, what: string) {
    super(`${what} = ${status}: ${body.slice(0, 500)}`);
    this.status = status;
    this.body = body;
  }
}

export interface Call {
  method?: string;
  body?: unknown;
  headers?: Record<string, string>;
  /** Expected status; anything else throws. */
  want?: number;
}

export class Api {
  readonly base: string;
  /** An access token or a session cookie ("lerryn_session=..."). */
  private readonly auth: { token?: string; cookie?: string };
  constructor(base: string, auth: { token?: string; cookie?: string } = {}) {
    this.base = base;
    this.auth = auth;
  }

  withToken(token: string): Api {
    return new Api(this.base, { token });
  }

  withCookie(cookie: string): Api {
    return new Api(this.base, { cookie });
  }

  async raw(path: string, { method = "GET", body, headers = {} }: Call = {}): Promise<Response> {
    const h: Record<string, string> = { ...headers };
    if (body !== undefined) h["Content-Type"] = "application/json";
    if (this.auth.token) h.Authorization = `Bearer ${this.auth.token}`;
    if (this.auth.cookie) {
      h.Cookie = this.auth.cookie;
      // Session writes must come from the public origin.
      h.Origin = this.base;
    }
    return fetch(this.base + path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body) });
  }

  async call<T = unknown>(path: string, opts: Call = {}): Promise<T> {
    const resp = await this.raw(path, opts);
    const text = await resp.text();
    const want = opts.want ?? (opts.method && opts.method !== "GET" ? undefined : 200);
    if (want !== undefined ? resp.status !== want : resp.status >= 300) {
      throw new ApiError(resp.status, text, `${opts.method ?? "GET"} ${path}`);
    }
    return (text ? JSON.parse(text) : undefined) as T;
  }
}

/** The session cookie a successful login or register response set. */
export function sessionCookie(resp: Response): string {
  for (const c of resp.headers.getSetCookie()) {
    const [pair] = c.split(";");
    if (pair?.startsWith("lerryn_session=")) return pair;
  }
  throw new Error(`no session cookie in ${resp.status} response`);
}

export async function login(base: string, username: string, password: string, device?: { name: string; platform: string }) {
  const resp = await new Api(base).raw("/api/v1/auth/login", { method: "POST", body: { username, password, ...(device ? { device } : {}) } });
  if (resp.status !== 200) throw new ApiError(resp.status, await resp.text(), "login");
  return sessionCookie(resp);
}

export interface Run {
  id: string;
  task_id: string;
  status: string;
  version: number;
  queued_at: string;
  started_at: string | null;
  finished_at: string | null;
  exit_code: number | null;
  trigger: { kind: string; client?: string | null };
  stages: { id: string; name: string; status: string }[];
  result: { status: string; summary: string; data?: unknown } | null;
  error: { code: string; message: string; stage?: string | null } | null;
}

const ACTIVE = new Set(["queued", "preparing", "running", "cancelling"]);

/** Waits until the run has ended, using the API's long poll. */
export async function waitRun(api: Api, id: string, withinMs = 300_000): Promise<Run> {
  const deadline = Date.now() + withinMs;
  for (;;) {
    const run = await api.call<Run>(`/api/v1/runs/${id}?wait=30`);
    if (!ACTIVE.has(run.status)) return run;
    if (Date.now() > deadline) throw new Error(`run ${id} still ${run.status}`);
  }
}

/** Waits until the run has reached one of the statuses. */
export async function waitStatus(api: Api, id: string, statuses: string[], withinMs = 120_000): Promise<Run> {
  const deadline = Date.now() + withinMs;
  for (;;) {
    const run = await api.call<Run>(`/api/v1/runs/${id}`);
    if (statuses.includes(run.status)) return run;
    if (!ACTIVE.has(run.status) || Date.now() > deadline) throw new Error(`run ${id} is ${run.status}, want ${statuses.join("|")}`);
    await new Promise((r) => setTimeout(r, 250));
  }
}
