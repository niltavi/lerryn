import { DatabaseSync } from "node:sqlite";
import { join } from "node:path";

import { Api, login, sessionCookie, waitRun, type Run } from "./api.ts";
import { ADMIN, type RunningServer } from "./server.ts";
import { bigLog, broken, dailyReport, needsKey, plainEcho, ticker, type TaskBody } from "./tasks.ts";

/** What the specs read back: who signed in where, and the seeded IDs. */
export interface E2EState {
  base: string;
  dir: string;
  /** The client name the agent token's runs carry. */
  client: string;
  alice: { username: string; password: string; token: string; project: string; cookie: string };
  admin: { username: string; password: string; cookie: string };
  tasks: Record<"dailyReport" | "plainEcho" | "broken" | "bigLog" | "floodLog" | "ticker" | "ticker2" | "needsKey" | "oldReport", string>;
  runs: Record<"daily" | "plain" | "broken" | "big" | "flood", string>;
  /** A run whose log the retention policy removed; its task has a newer run. */
  expiredRun: string;
  credentialRequest: string;
}

export const ALICE = { username: "alice", password: "alice e2e password" };
const CLIENT = "Claude Code · e2e";

async function register(base: string, username: string, password: string) {
  const resp = await new Api(base).raw("/api/v1/auth/register", { method: "POST", body: { username, password, display_name: "Alice" } });
  if (resp.status !== 201) throw new Error(`register ${username} = ${resp.status}: ${await resp.text()}`);
  const me = (await resp.json()) as { personal_project_id: string };
  return { cookie: sessionCookie(resp), project: me.personal_project_id };
}

async function apply(agent: Api, project: string, name: string, body: TaskBody, ifMatch?: number): Promise<string> {
  const out = await agent.call<{ task: { id: string } }>(`/api/v1/projects/${project}/tasks/${name}`, {
    method: "PUT",
    body,
    headers: ifMatch ? { "If-Match": String(ifMatch) } : {},
    want: 200,
  });
  return out.task.id;
}

async function start(agent: Api, taskId: string, params: Record<string, unknown> = {}): Promise<string> {
  const run = await agent.call<Run>(`/api/v1/tasks/${taskId}/runs`, { method: "POST", body: { params }, want: 202 });
  return run.id;
}

/**
 * Seeds one deployment the way people use it: an agent (access token)
 * builds and runs tasks in Alice's personal project, one run fails, one
 * task waits for a credential, a notification channel is set up, and the
 * admin exists from the configuration.
 */
/**
 * Ages a run's log past the retention period (logs_days is 7, see
 * server.ts) while the server is down, the way ten days passing would.
 * The log must be archived first, or there is nothing for retention to
 * remove.
 */
async function expireLog(server: RunningServer, runId: string) {
  const db = join(server.dir, "lerryn.db");
  const deadline = Date.now() + 60_000;
  for (;;) {
    const conn = new DatabaseSync(db, { readOnly: true });
    const row = conn.prepare("SELECT log_blob FROM runs WHERE id = ?").get(runId) as { log_blob: string | null } | undefined;
    conn.close();
    if (row?.log_blob) break;
    if (Date.now() > deadline) throw new Error(`the log of run ${runId} was not archived`);
    await new Promise((r) => setTimeout(r, 200));
  }
  await server.restart(() => {
    const at = new Date(Date.now() - 10 * 24 * 3600_000).toISOString().replace("T", " ").replace("Z", "+00:00");
    const conn = new DatabaseSync(db);
    conn.prepare("UPDATE runs SET finished_at = ? WHERE id = ?").run(at, runId);
    conn.close();
  });
}

export async function seed(server: RunningServer): Promise<E2EState> {
  const { base, dir } = server;
  const alice = await register(base, ALICE.username, ALICE.password);
  const session = new Api(base).withCookie(alice.cookie);
  const { token } = await session.call<{ token: string }>("/api/v1/me/tokens", {
    method: "POST",
    body: { name: CLIENT, scopes: ["read", "write", "execute"] },
    want: 201,
  });
  const agent = new Api(base).withToken(token);
  const p = alice.project;

  // Version 2 is the one that runs, so the run page shows v2.
  const daily = await apply(agent, p, "daily-report", dailyReport("每日线索"));
  await apply(agent, p, "daily-report", dailyReport(), 1);
  await agent.call(`/api/v1/tasks/${daily}/triggers`, {
    method: "PUT",
    body: { triggers: [{ name: "weekday-morning", type: "cron", schedule: "0 8 * * 1-5", timezone: "Asia/Shanghai" }] },
    want: 200,
  });
  await agent.call(`/api/v1/tasks/${daily}/activation`, { method: "POST", body: { active: true }, want: 200 });

  const tasks = {
    dailyReport: daily,
    plainEcho: await apply(agent, p, "plain-echo", plainEcho),
    broken: await apply(agent, p, "broken-import", broken),
    bigLog: await apply(agent, p, "big-log", bigLog),
    floodLog: await apply(agent, p, "flood-log", bigLog),
    ticker: await apply(agent, p, "ticker", ticker),
    ticker2: await apply(agent, p, "ticker-b", ticker),
    needsKey: await apply(agent, p, "needs-key", needsKey),
    oldReport: await apply(agent, p, "old-report", plainEcho),
  };

  // The runs share the runner's slots; start them together and wait.
  const runs = {
    daily: await start(agent, tasks.dailyReport),
    plain: await start(agent, tasks.plainEcho),
    broken: await start(agent, tasks.broken),
    big: await start(agent, tasks.bigLog, { mode: "lines" }),
    flood: await start(agent, tasks.floodLog, { mode: "flood" }),
  };
  // The first of two old-report runs gets its log removed by retention below.
  const expiredRun = await start(agent, tasks.oldReport);
  await waitRun(agent, expiredRun);
  const want: Record<keyof typeof runs, string> = { daily: "completed", plain: "completed", broken: "failed", big: "completed", flood: "completed" };
  for (const [key, id] of Object.entries(runs) as [keyof typeof runs, string][]) {
    const run = await waitRun(agent, id);
    if (run.status !== want[key]) throw new Error(`seed run ${key} = ${run.status}: ${JSON.stringify(run.error)}`);
  }

  const request = await agent.call<{ id: string }>("/api/v1/credential-requests", {
    method: "POST",
    body: { task_id: tasks.needsKey, name: "API_TOKEN", purpose: "调用报价接口", fields: [{ name: "API_TOKEN" }] },
    want: 201,
  });
  await session.call("/api/v1/notification-channels", {
    method: "POST",
    body: {
      name: "运维群",
      kind: "webhook",
      url: "https://hooks.example.com/lerryn",
      subscriptions: [{ project_id: p, events: ["run_failed"] }],
    },
    want: 201,
  });

  await waitRun(agent, await start(agent, tasks.oldReport));
  // Ten days pass for the first run; the new leader's first cleanup comes about 30 s after it starts.
  await expireLog(server, expiredRun);
  const deadline = Date.now() + 120_000;
  while (!(await agent.call<Run & { logs_expired_at: string | null }>(`/api/v1/runs/${expiredRun}`)).logs_expired_at) {
    if (Date.now() > deadline) throw new Error(`retention did not remove the log of run ${expiredRun}`);
    await new Promise((r) => setTimeout(r, 1_000));
  }

  const adminCookie = await login(base, ADMIN.username, ADMIN.password);
  return {
    base,
    dir,
    client: CLIENT,
    alice: { ...ALICE, token, project: p, cookie: alice.cookie },
    admin: { ...ADMIN, cookie: adminCookie },
    tasks,
    runs,
    expiredRun,
    credentialRequest: request.id,
  };
}
