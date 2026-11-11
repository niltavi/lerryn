import { createLerrynClient, type Schemas } from "@lerryn/api";
import { ApiProvider, I18nProvider } from "@lerryn/core";
import { LerrynThemeProvider } from "@lerryn/ui-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import contract from "../../../api/bundled/openapi.yaml?raw";
import { consolePath } from "./lib/links";
import { countdown } from "./pages/settings/PairingPage";
import { generatePassword } from "./pages/admin/AdminUsersPage";
import { diffLines } from "./pages/task/VersionDiffDialog";
import { createAppRouter } from "./router";

const NOW = new Date();
const iso = (offsetMin: number) => new Date(NOW.getTime() + offsetMin * 60_000).toISOString();

const ME = {
  id: "0192f3a1-0000-7000-8000-000000000001",
  username: "zhanglin",
  display_name: "张林",
  email: null,
  is_admin: false,
  personal_project_id: "0192f3a1-0000-7000-8000-000000000002",
  created_at: "2026-11-01T00:00:00Z",
  locale: null,
  timezone: null,
  has_password: true,
  identities: [],
};
const discovery = (methods: string[], adminPasswordLogin = false) => ({
  server: { version: "dev", commit: "none", build_date: null },
  api: { version: "v1", spec_version: "0.1.0" },
  capabilities: { login_methods: methods, admin_password_login: adminPasswordLogin, registration_open: false, first_account: false, network_policies: ["full"], require_test_before_activate: true },
  runtimes: [],
  skills: { version: "dev", items: [] },
  principal: null,
  links: { console: "http://lerryn.test/" },
});
const TASK_REF = { id: "0192f3a1-0000-7000-8000-0000000000t1", name: "weekday-digest", project_id: ME.personal_project_id };
const OVERVIEW = {
  generated_at: iso(0),
  counts: { tasks: 3, active_tasks: 2, attention: 2, running: 1, queued: 0 },
  attention: [
    { kind: "run_timed_out", at: iso(-60), task: { ...TASK_REF, name: "dataset-backup" }, run_id: "0192f3a1-0000-7000-8000-0000000000r9", stage: "上传", summary: "超过 30 分钟", credential_request_id: null, credential_id: null, runner_id: null, due_at: null, link: "http://lerryn.test/runs/0192f3a1-0000-7000-8000-0000000000r9" },
    { kind: "credential_request", at: iso(-30), task: TASK_REF, run_id: null, stage: null, summary: "PRICE_API_TOKEN", credential_request_id: "0192f3a1-0000-7000-8000-0000000000c1", credential_id: null, runner_id: null, due_at: iso(120), link: "http://lerryn.test/c/0192f3a1-0000-7000-8000-0000000000c1" },
  ],
  active: [{ run_id: "0192f3a1-0000-7000-8000-0000000000r2", task: { ...TASK_REF, name: "lead-sync" }, status: "running", test: false, trigger: "cron", queued_at: iso(-2), started_at: iso(-1), current_stage: "写入多维表格", links: { console: "http://lerryn.test/runs/r2" } }],
  upcoming: [{ task: TASK_REF, trigger_id: "tr1", at: iso(90), schedule_text: "每个工作日 08:00" }],
};
const EMPTY_OVERVIEW = { ...OVERVIEW, counts: { tasks: 0, active_tasks: 0, attention: 0, running: 0, queued: 0 }, attention: [], active: [], upcoming: [] };
const TASK = {
  id: TASK_REF.id,
  project_id: ME.personal_project_id,
  name: "weekday-digest",
  description: "工作日早上汇总新线索",
  tags: [] as string[],
  state: "active",
  active_version: 2,
  latest_version: {
    seq: 2,
    created_at: "2026-11-01T00:00:00Z",
    created_by: ME.id,
    client: "Claude Code · 笔记本",
    tested_at: "2026-11-01T00:05:00Z",
    tree_hash: "h",
    manifest: { runtime: { language: "python", version: "3.12" }, entrypoint: ["python", "main.py"], credentials: [{ name: "LLM_API_KEY" }], tools: ["crm-mcp", "ledger", "fmt-lib"] },
    files: [{ path: "main.py", size: 2048, sha256: "ab", executable: false }],
  },
  created_by: ME.id,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  archived_at: null,
  console: { manual_run: true },
  links: { console: "http://lerryn.test/tasks/" + TASK_REF.id },
};
const PROJECT = {
  id: ME.personal_project_id,
  name: "张林的个人项目",
  is_personal: true,
  role: "owner",
  owner: { id: ME.id, username: ME.username, display_name: ME.display_name },
  member_count: 1,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
  links: { console: "http://lerryn.test/" },
};
const FAILED_RUN = {
  id: "0192f3a1-0000-7000-8000-0000000000r1",
  task_id: TASK.id,
  project_id: ME.personal_project_id,
  version: 2,
  status: "failed",
  test: false,
  trigger: { kind: "cron", trigger_id: "tr1", scheduled_at: iso(-60), user_id: null, client: null },
  params: {},
  attempt: 1,
  max_attempts: 1,
  retry_of: null,
  rerun_of: null,
  skip_reason: null,
  claimed_at: iso(-60),
  cancelled_by: null,
  runner: null,
  environment: null,
  log_lines: 0,
  dropped_log_lines: 0,
  logs_expired_at: null,
  queued_at: iso(-60),
  started_at: iso(-60),
  finished_at: iso(-59),
  exit_code: 1,
  error: { code: "script_error", stage: null, message: "退出码 1" },
  stages: [],
  result: null,
  cancel_requested: false,
  links: { console: "http://lerryn.test/runs/0192f3a1-0000-7000-8000-0000000000r1" },
};
// The run a button starts: queued, started from the console's session.
const NEW_RUN = {
  ...FAILED_RUN,
  id: "0192f3a1-0000-7000-8000-0000000000r5",
  status: "queued",
  trigger: { kind: "manual", trigger_id: null, scheduled_at: null, user_id: ME.id, client: null },
  claimed_at: null,
  started_at: null,
  finished_at: null,
  exit_code: null,
  error: null,
  links: { console: "http://lerryn.test/runs/0192f3a1-0000-7000-8000-0000000000r5" },
};
const TRIGGERS: Schemas["TriggerSet"] = {
  task_id: TASK.id,
  revision: 1,
  dry_run: false,
  task_active: true,
  triggers: [
    {
      id: "tr1",
      name: "weekday-morning",
      type: "cron",
      enabled: true,
      catch_up: "skip",
      params: null,
      cron: { schedule: "0 8 * * 1-5", timezone: "Asia/Shanghai", schedule_text: "每个工作日 08:00", next_fires: [iso(90)] },
      webhook: null,
      next_fire_at: iso(90),
      last_fired_at: null,
      created_at: iso(-1000),
      updated_at: iso(-1000),
    },
  ],
};
const MORNING_TRIGGER = { ...TRIGGERS.triggers[0]!, params: { inbox: "morning" } };
const AFTERNOON_TRIGGER = {
  ...MORNING_TRIGGER,
  id: "tr2",
  name: "weekday-afternoon",
  params: { inbox: "afternoon" },
  cron: { ...MORNING_TRIGGER.cron!, schedule: "0 15 * * 1-5", schedule_text: "每个工作日 15:00" },
};
const TOOL = {
  id: "0192f3a1-0000-7000-8000-0000000000u1",
  project_id: ME.personal_project_id,
  name: "crm-mcp",
  kind: "mcp",
  description: "查询客户和商机",
  url: "https://crm.example.com/mcp",
  created_at: iso(-5000),
  updated_at: iso(-5000),
};
const TREE = "3f9a0c4be1d27788aa00bb11cc22dd33ee44ff5566778899aabbccddeeff0011";
const SCRIPT_TOOL = {
  ...TOOL,
  id: "0192f3a1-0000-7000-8000-0000000000u2",
  name: "fmt-lib",
  kind: "script",
  description: "格式化金额",
  url: "",
  files: [
    { path: "fmt.py", size: 64, sha256: "aa", executable: false },
    { path: "README.md", size: 32, sha256: "bb", executable: false },
  ],
  tree_hash: TREE,
};
// An http tool whose address is empty: shown without an address or a copy button.
const BARE_TOOL = { ...TOOL, id: "0192f3a1-0000-7000-8000-0000000000u3", name: "bare-http", kind: "http", description: "没有地址", url: "" };
const CREDENTIAL: Schemas["Credential"] = {
  id: "0192f3a1-0000-7000-8000-0000000000k1",
  name: "LLM_API_KEY",
  purpose: "调用模型",
  fields: ["LLM_API_KEY"],
  status: "active",
  version: 3,
  expires_at: iso(60 * 24 * 3),
  revoked_at: null,
  last_used_at: null,
  created_at: iso(-5000),
  updated_at: iso(-60),
  grants: [{ task: TASK_REF, name: "LLM_API_KEY", granted_at: iso(-5000) }],
};

const REQUEST_ID = "0192f3a1-0000-7000-8000-0000000000c1";
// An agent's request for a task credential: a secret with a hint and an optional plain field.
const TASK_REQUEST: Schemas["CredentialRequest"] = {
  id: REQUEST_ID,
  status: "pending",
  url: `http://lerryn.test/c/${REQUEST_ID}`,
  name: "PRICE_API_TOKEN",
  purpose: "读取竞品价格接口",
  task: TASK_REF,
  credential_id: null,
  fields: [
    { name: "PRICE_API_TOKEN", label: "令牌", description: "在价格平台的开发者设置里生成", secret: true, multiline: false, optional: false },
    { name: "REGION", secret: false, multiline: false, optional: true },
  ],
  client: "claude-code",
  created_at: iso(-30),
  expires_at: iso(120),
  fulfilled_at: null,
};
// The request the console makes to update CREDENTIAL.
const UPDATE_REQUEST = {
  ...TASK_REQUEST,
  id: "req2",
  url: "http://lerryn.test/c/req2",
  name: "LLM_API_KEY",
  purpose: "调用模型",
  task: null,
  credential_id: "0192f3a1-0000-7000-8000-0000000000k1",
  fields: [{ name: "LLM_API_KEY", secret: true, multiline: false, optional: false }],
  client: null,
  expires_at: iso(60),
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status: number, code: string) =>
  new Response(JSON.stringify({ type: `urn:lerryn:error:${code}`, title: code, status, code, request_id: "req_1" }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });

interface FakeOptions {
  signedIn?: boolean;
  locale?: "zh-CN" | "en";
  overview?: unknown;
  methods?: string[];
  adminPasswordLogin?: boolean;
  calendar?: Schemas["CalendarItem"][];
  /** The user's role in the task's project; none lists no projects. */
  role?: "viewer" | "maintainer" | "owner";
  /** The task's console.manual_run. */
  manualRun?: boolean;
  /** An error code startRun answers with instead of a run. */
  startError?: string;
  task?: Partial<Schemas["Task"]>;
  runs?: Schemas["RunSummary"][];
  triggerItems?: Schemas["Trigger"][];
  taskControlSubmit?: (path: string, body: unknown) => Promise<Response>;
  run?: Partial<Schemas["Run"]>;
  runDetail?: () => Promise<Response>;
  runArchiveSubmit?: (body: Schemas["SetRunArchiveRequest"]) => Promise<Response>;
  showRunInList?: boolean;
  admin?: boolean;
  tasks?: Schemas["TaskSummary"][];
  adminSubmit?: (body: unknown) => Promise<Response>;
  credentials?: Schemas["Credential"][];
  credentialPage?: (url: URL) => Promise<Response>;
  pendingCredentialRequests?: Schemas["CredentialRequest"][];
  toolsResponse?: () => Response;
  channelSubmit?: (body: Schemas["CreateNotificationChannelRequest"]) => Promise<Response>;
}

function fakeServer(opts: FakeOptions = {}) {
  let failedRun = { ...FAILED_RUN, archived_at: null as string | null, ...opts.run };
  let task = { ...TASK, ...opts.task, console: { manual_run: opts.manualRun ?? true } };
  let triggerItems = opts.triggerItems ?? TRIGGERS.triggers;
  const credentials = [...(opts.credentials ?? [CREDENTIAL])];
  let aiConfig: Schemas["AIConfiguration"] = { models: [] };
  const channels: Schemas["NotificationChannel"][] = [];
  const requests: { method: string; path: string; body?: unknown; idempotencyKey?: string | null }[] = [];
  const fetch = vi.fn(async (req: Request) => {
    const url = new URL(req.url);
    const { pathname } = url;
    const body = req.method === "GET" ? undefined : await req.clone().json().catch(() => undefined);
    requests.push({ method: req.method, path: pathname, body, idempotencyKey: req.headers.get("Idempotency-Key") });
    const revoke = pathname.match(/^\/api\/v1\/credentials\/([^/]+)$/);
    if (req.method === "DELETE" && revoke) {
      const index = credentials.findIndex((c) => c.id === revoke[1]);
      if (index < 0) return problem(404, "not_found");
      credentials[index] = { ...credentials[index]!, status: "revoked", revoked_at: iso(0) };
      return json(credentials[index]);
    }
    const purge = pathname.match(/^\/api\/v1\/credentials\/([^/]+)\/purge$/);
    if (req.method === "POST" && purge) {
      const index = credentials.findIndex((c) => c.id === purge[1]);
      if (index < 0) return problem(404, "not_found");
      if (credentials[index]!.status !== "revoked") return problem(422, "validation_failed");
      credentials.splice(index, 1);
      return new Response(null, { status: 204 });
    }
    const key = `${req.method} ${pathname}`;
    switch (key) {
      case "GET /api/v1/discover":
        return json(discovery(opts.methods ?? ["password"], opts.adminPasswordLogin));
      case "GET /api/v1/me":
        return opts.signedIn === false ? problem(401, "unauthenticated") : json({ ...ME, is_admin: opts.admin ?? false });
      case "GET /api/v1/overview":
        return json(opts.overview ?? OVERVIEW);
      case "GET /api/v1/overview/calendar": {
        const from = url.searchParams.get("from")!;
        const to = url.searchParams.get("to")!;
        const taskId = url.searchParams.get("task_id");
        const items = opts.calendar ?? [{ kind: "run", task: TASK_REF, run_id: "r1", status: "failed", start: iso(-60), end: iso(-59) }];
        return json({ from, to, items: items.filter((item) =>
          Date.parse(item.start) >= Date.parse(from) && Date.parse(item.start) < Date.parse(to) && (!taskId || item.task.id === taskId)), truncated: false });
      }
      case "GET /api/v1/tasks": {
        const tasks = opts.tasks ?? [];
        const after = Number(url.searchParams.get("cursor") ?? 0);
        const limit = Number(url.searchParams.get("limit") ?? 100);
        return json({ items: tasks.slice(after, after + limit), next_cursor: after + limit < tasks.length ? String(after + limit) : null });
      }
      case "GET /api/v1/admin/users":
        return json({ items: [{ ...ME, id: "bob", username: "bob", display_name: "Bob", sso: false, disabled_at: null, last_seen_at: null }], next_cursor: null });
      case "POST /api/v1/admin/users":
      case "POST /api/v1/admin/users/bob/password":
        return opts.adminSubmit ? opts.adminSubmit(body) : json({ ...ME, id: "bob", username: "bob" });
      case "GET /api/v1/projects":
        return json({ items: opts.role ? [{ ...PROJECT, role: opts.role }] : [], next_cursor: null });
      case "GET /api/v1/me/ai":
        return json(aiConfig);
      case "PUT /api/v1/me/ai": {
        const { base_url, models } = body as Schemas["SetAIConfigurationBody"];
        aiConfig = { base_url, models };
        return json(aiConfig);
      }
      case "DELETE /api/v1/me/ai":
        aiConfig = { models: [] };
        return new Response(null, { status: 204 });
      case "GET /api/v1/credential-requests":
        return json({ items: opts.pendingCredentialRequests ?? [], next_cursor: null });
      case "GET /api/v1/me/tokens":
      case "GET /api/v1/me/devices":
        return json({ items: [], next_cursor: null });
      case "GET /api/v1/notification-channels":
        return json({ items: channels });
      case "POST /api/v1/notification-channels": {
        if (!opts.channelSubmit) return problem(500, "internal");
        const response = await opts.channelSubmit(body as Schemas["CreateNotificationChannelRequest"]);
        if (response.ok) channels.push(((await response.clone().json()) as Schemas["CreatedNotificationChannel"]).channel);
        return response;
      }
      case `GET /api/v1/tasks/${TASK.id}`:
        return json(task);
      case `PUT /api/v1/tasks/${TASK.id}/tags`: {
        const { tags } = body as Schemas["TaskTagSet"];
        task = { ...task, tags };
        return json({ tags });
      }
      case `DELETE /api/v1/tasks/${TASK.id}`:
        if (opts.taskControlSubmit) return opts.taskControlSubmit(pathname, body);
        task = { ...task, archived_at: iso(0), state: "inactive" };
        return json(task);
      case `POST /api/v1/tasks/${TASK.id}/activation`: {
        if (opts.taskControlSubmit) return opts.taskControlSubmit(pathname, body);
        const input = body as Schemas["SetTaskActivationRequest"];
        task = { ...task, state: input.active ? "active" : "inactive", active_version: input.version ?? task.active_version };
        return json(task);
      }
      case `POST /api/v1/tasks/${TASK.id}/skip-next-run`: {
        if (opts.taskControlSubmit) return opts.taskControlSubmit(pathname, body);
        const input = body as Schemas["SkipNextRunRequest"];
        triggerItems = triggerItems.map((tr) => tr.next_fire_at === input.scheduled_at
          ? { ...tr, next_fire_at: iso(90 + 24 * 60) } : tr);
        return json({ ...TRIGGERS, triggers: triggerItems });
      }
      case `POST /api/v1/tasks/${TASK.id}/runs`:
        return opts.startError ? problem(409, opts.startError) : json(NEW_RUN, 202);
      case `GET /api/v1/runs/${FAILED_RUN.id}`:
        return opts.runDetail?.() ?? json(failedRun);
      case `PUT /api/v1/runs/${FAILED_RUN.id}/archive`: {
        const input = body as Schemas["SetRunArchiveRequest"];
        if (opts.runArchiveSubmit) {
          const response = await opts.runArchiveSubmit(input);
          if (response.ok) failedRun = { ...failedRun, ...await response.clone().json() };
          return response;
        }
        failedRun = { ...failedRun, archived_at: input.archived ? iso(0) : null };
        return json(failedRun);
      }
      case `GET /api/v1/tasks/${TASK.id}/versions/2`:
        return json({ ...TASK.latest_version, active: true });
      case `GET /api/v1/runs/${FAILED_RUN.id}/logs`:
      case `GET /api/v1/runs/${NEW_RUN.id}/logs`:
        return json({ lines: [], next_cursor: "bDA", complete: true, dropped_lines: 0 });
      case `GET /api/v1/runs/${FAILED_RUN.id}/events`:
      case `GET /api/v1/runs/${NEW_RUN.id}/events`:
        return json({ events: [], next_cursor: "ZTA", complete: true });
      case `GET /api/v1/runs/${FAILED_RUN.id}/artifacts`:
      case `GET /api/v1/runs/${NEW_RUN.id}/artifacts`:
        return json({ items: [], next_cursor: null });
      case `POST /api/v1/runs/${FAILED_RUN.id}/rerun`:
        return json({ ...NEW_RUN, rerun_of: FAILED_RUN.id }, 202);
      case `GET /api/v1/runs/${NEW_RUN.id}`:
        return json(NEW_RUN);
      case `GET /api/v1/tasks/${TASK.id}/triggers`:
        return json({ ...TRIGGERS, task_active: task.state === "active", triggers: triggerItems.map((tr) => ({ ...tr, next_fire_at: task.state === "active" ? tr.next_fire_at : null })) });
      case `GET /api/v1/tasks/${TASK.id}/credentials`:
        return json({ version: 2, items: [{ name: "LLM_API_KEY", as: "env", optional: false, status: "missing", credential: null, expires_at: null, pending_request_id: "0192f3a1-0000-7000-8000-0000000000c1" }] });
      case "GET /api/v1/credentials": {
        if (opts.credentialPage) return opts.credentialPage(url);
        const offset = Number(url.searchParams.get("cursor") ?? 0);
        const limit = Number(url.searchParams.get("limit") ?? 200);
        return json({ items: credentials.slice(offset, offset + limit), next_cursor: offset + limit < credentials.length ? String(offset + limit) : null });
      }
      case "POST /api/v1/credential-requests":
        return json(UPDATE_REQUEST, 201);
      case `GET /api/v1/credential-requests/${REQUEST_ID}`:
        return json(TASK_REQUEST);
      case "GET /api/v1/credential-requests/req2":
        return json(UPDATE_REQUEST);
      case `POST /api/v1/credential-requests/${REQUEST_ID}/values`:
        return json({ ...TASK_REQUEST, status: "fulfilled", credential_id: "0192f3a1-0000-7000-8000-0000000000k2", fulfilled_at: iso(0) });
      case "POST /api/v1/credential-requests/req2/values":
        return json({ ...UPDATE_REQUEST, status: "fulfilled", fulfilled_at: iso(0) });
      case "DELETE /api/v1/credential-requests/req2":
        return json({ ...UPDATE_REQUEST, status: "cancelled" });
      case "GET /api/v1/runs": {
        const archiveOnly = url.searchParams.get("archived") === "true";
        const excludeArchivedTasks = url.searchParams.get("exclude_archived_tasks") === "true";
        const items = (opts.showRunInList
          ? [{ ...failedRun, task: TASK_REF, current_stage: null, result_status: null, result_summary: null }]
          : opts.runs ?? []).filter((r) =>
          !!r.archived_at === archiveOnly && !(excludeArchivedTasks && r.task.id === task.id && task.archived_at));
        const offset = Number(url.searchParams.get("cursor") ?? 0);
        const end = offset + Number(url.searchParams.get("limit") ?? 50);
        return json({ items: items.slice(offset, end), next_cursor: end < items.length ? String(end) : null });
      }
      case "GET /api/v1/tools":
        return opts.toolsResponse?.() ?? json({ items: [TOOL, SCRIPT_TOOL, BARE_TOOL] });
      default:
        return problem(404, "not_found");
    }
  });
  return { fetch, requests };
}

function renderApp(path: string, opts: FakeOptions = {}) {
  const server = fakeServer(opts);
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: server.fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } });
  const router = createAppRouter({ api, queryClient });
  router.update({ history: createMemoryHistory({ initialEntries: [path] }) } as never);
  render(
    <LerrynThemeProvider>
      <I18nProvider locale={opts.locale ?? "zh-CN"}>
        <QueryClientProvider client={queryClient}>
          <ApiProvider client={api}>
            <RouterProvider router={router} />
          </ApiProvider>
        </QueryClientProvider>
      </I18nProvider>
    </LerrynThemeProvider>,
  );
  return { router, server, queryClient };
}

afterEach(() => vi.restoreAllMocks());

describe("settings navigation", () => {
  it("opens Key from the key icon and Account from the avatar", async () => {
    const { router, server } = renderApp("/");
    const user = userEvent.setup();
    const rail = within(await screen.findByRole("navigation", { name: "主导航" }));
    await user.click(rail.getByRole("link", { name: "密钥" }));
    await screen.findByRole("main", { name: "访问令牌" });
    expect(router.state.location.pathname).toBe("/settings/tokens");
    const keyMenu = screen.getByRole("region", { name: "密钥" });
    expect(within(keyMenu).getAllByRole("link").map((link) => link.querySelector(".li-name")?.textContent)).toEqual(["访问令牌", "凭证", "工具"]);
    expect(rail.getByRole("link", { name: "密钥" }).getAttribute("aria-current")).toBe("page");
    expect(server.requests.some((req) => ["/api/v1/notification-channels", "/api/v1/me/devices"].includes(req.path))).toBe(false);

    await user.click(rail.getByRole("link", { name: "张林 · 账号" }));
    await screen.findByRole("heading", { level: 1, name: "账号" });
    expect(router.state.location.pathname).toBe("/settings/account");
    expect(screen.queryByRole("region", { name: "密钥" })).toBeNull();
    const accountMenu = within(screen.getByRole("region", { name: "账号" }));
    expect(accountMenu.getAllByRole("link").map((link) => link.textContent)).toEqual(["账号", "通知", "手机", "关于"]);
    expect(rail.getByRole("link", { name: "张林 · 账号" }).getAttribute("aria-current")).toBe("page");
    expect(rail.getByRole("link", { name: "密钥" }).getAttribute("aria-current")).toBeNull();

    await user.click(accountMenu.getByRole("link", { name: "通知" }));
    await screen.findByRole("main", { name: "通知" });
    expect(router.state.location.pathname).toBe("/settings/notifications");
    expect(rail.getByRole("link", { name: "张林 · 账号" }).getAttribute("aria-current")).toBe("page");
    await user.click(accountMenu.getByRole("link", { name: "手机" }));
    await screen.findByRole("main", { name: "手机" });
    expect(router.state.location.pathname).toBe("/settings/pairing");

    await user.click(accountMenu.getByRole("link", { name: "关于" }));
    const about = within(await screen.findByRole("main", { name: "关于" }));
    expect(about.getByRole("heading", { name: "Lerryn" })).toBeTruthy();
    expect(await about.findByText("dev")).toBeTruthy();
    expect(about.getByRole("link", { name: "官网" }).getAttribute("href")).toBe("https://lerryn.niltavi.com/");
    expect(about.getByRole("link", { name: "GitHub" }).getAttribute("href")).toBe("https://github.com/niltavi/lerryn");
    expect(about.getByRole("link", { name: "理念" }).getAttribute("href")).toBe("https://ui.niltavi.com/#/philosophy");
    expect(about.getByText(/在人工智能不断拓展人类能力边界的时代/)).toBeTruthy();
    const niltavi = about.getByRole("link", { name: "niltavi" });
    expect(niltavi.getAttribute("href")).toBe("https://niltavi.com/");
    expect(within(niltavi).getByRole("img", { name: "niltavi" })).toBeTruthy();
  });

  it.each([
    ["/settings/tokens?new=true", "密钥", "访问令牌", "密钥"],
    ["/settings/credentials", "密钥", "凭证", "密钥"],
    ["/settings/tools", "密钥", "工具", "密钥"],
    ["/settings/notifications", "账号", "通知", "张林 · 账号"],
    ["/settings/pairing", "账号", "手机", "张林 · 账号"],
    ["/settings/account", "账号", "账号", "张林 · 账号"],
    ["/settings/about", "账号", "关于", "张林 · 账号"],
  ])("keeps the menu and rail selection on a direct visit to %s", async (path, menu, section, entry) => {
    const { server } = renderApp(path);
    if (path.includes("?new")) {
      const dialog = await screen.findByRole("dialog", { name: "新建令牌" });
      await userEvent.setup().click(within(dialog).getByRole("button", { name: "取消" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    }
    const list = within(await screen.findByRole("region", { name: menu }));
    const selected = list.getAllByRole("link").find((link) => link.getAttribute("aria-current") === "page");
    expect(selected?.textContent).toContain(section);
    const rail = within(screen.getByRole("navigation", { name: "主导航" }));
    expect(rail.getByRole("link", { name: entry }).getAttribute("aria-current")).toBe("page");
    expect(rail.getAllByRole("link").filter((link) => link.getAttribute("aria-current") === "page")).toHaveLength(1);
    if (menu === "账号") {
      expect(server.requests.some((req) => ["/api/v1/me/tokens", "/api/v1/tools"].includes(req.path))).toBe(false);
    }
  });

  it("places the admin entry immediately above the avatar and opens organization settings", async () => {
    const { router } = renderApp("/settings/account", { admin: true });
    const rail = within(await screen.findByRole("navigation", { name: "主导航" }));
    const admin = rail.getByRole("link", { name: "管理" });
    expect(admin.parentElement?.className).toBe("rail-foot");
    expect(admin.nextElementSibling).toBe(rail.getByRole("link", { name: "张林 · 账号" }));
    await userEvent.setup().click(admin);
    const menu = within(await screen.findByRole("region", { name: "管理" }));
    expect(router.state.location.pathname).toBe("/admin/users");
    expect(menu.getAllByRole("link").map((link) => link.textContent)).toEqual(["用户", "登录", "执行节点", "审计"]);
    expect(admin.getAttribute("aria-current")).toBe("page");
    expect(rail.getByRole("link", { name: "张林 · 账号" }).getAttribute("aria-current")).toBeNull();
  });

  it.each(["/admin", "/admin/users", "/admin/login", "/admin/runners", "/admin/audit"])("hides the admin entry and rejects a non-admin visit to %s", async (path) => {
    const { router, server } = renderApp("/settings/account");
    const rail = within(await screen.findByRole("navigation", { name: "主导航" }));
    expect(rail.queryByRole("link", { name: "管理" })).toBeNull();
    await act(() => router.navigate({ to: path }));
    await screen.findByText("找不到");
    expect(screen.queryByRole("region", { name: "管理" })).toBeNull();
    expect(server.requests.some((req) => req.path.startsWith("/api/v1/admin/"))).toBe(false);
  });
});

const createdChannel = (body: Schemas["CreateNotificationChannelRequest"]): Schemas["CreatedNotificationChannel"] => ({
  channel: {
    id: "channel-first", name: body.name, kind: body.kind, enabled: true,
    url_preview: "https://example.com/…/hook", subscriptions: [], last_delivery: null,
    created_at: iso(0), updated_at: iso(0),
  },
  signing_secret: body.kind === "webhook" ? "whsec_FIRST_SECRET" : null,
});

describe("notification channel form", () => {
  it("creates one channel on repeated Enter and retains its one-time signing secret", async () => {
    const submitted: Schemas["CreateNotificationChannelRequest"][] = [];
    let finish: ((response: Response) => void) | undefined;
    renderApp("/settings/notifications", { role: "owner", channelSubmit: async (body) => {
      submitted.push(body);
      return new Promise<Response>((resolve) => { finish = resolve; });
    } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "添加" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Webhook" }));
    await user.type(within(dialog).getByRole("textbox", { name: "名称" }), "team-alerts");
    const url = within(dialog).getByRole("textbox", { name: "机器人或 Webhook 地址" }) as HTMLInputElement;
    await user.type(url, "https://example.com/hook{Enter}");
    await waitFor(() => expect(submitted).toHaveLength(1));
    await user.keyboard("{Enter}{Enter}");
    await act(async () => { fireEvent.submit(url.form!); });
    expect(submitted).toHaveLength(1);
    await act(async () => finish!(json(createdChannel(submitted[0]!), 201)));
    expect((await screen.findByTestId("signing-secret")).textContent).toBe("whsec_FIRST_SECRET");
    await user.click(screen.getByRole("button", { name: "复制密钥" }));
    expect(await navigator.clipboard.readText()).toBe("whsec_FIRST_SECRET");
    await user.click(screen.getByRole("button", { name: "完成" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByText("team-alerts")).toBeTruthy();
    expect(document.body.textContent).not.toContain("FIRST_SECRET");
    await user.click(screen.getByRole("button", { name: "添加" }));
    expect((await screen.findByRole("textbox", { name: "名称" }) as HTMLInputElement).value).toBe("");
    expect(screen.queryByTestId("signing-secret")).toBeNull();
  });

  it("keeps creation open through Cancel and Escape until its response arrives", async () => {
    let submitted: Schemas["CreateNotificationChannelRequest"] | undefined;
    let finish: ((response: Response) => void) | undefined;
    renderApp("/settings/notifications", { role: "owner", channelSubmit: async (body) => {
      submitted = body;
      return new Promise<Response>((resolve) => { finish = resolve; });
    } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "添加" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Webhook" }));
    await user.type(within(dialog).getByRole("textbox", { name: "名称" }), "team-alerts");
    await user.type(within(dialog).getByRole("textbox", { name: "机器人或 Webhook 地址" }), "https://example.com/hook{Enter}");
    await waitFor(() => expect(finish).toBeDefined());
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBe(dialog);
    await act(async () => finish!(json(createdChannel(submitted!), 201)));
    expect((await screen.findByTestId("signing-secret")).textContent).toBe("whsec_FIRST_SECRET");
  });

  it("allows an Enter retry after failure and closes a successful bot channel", async () => {
    const submitted: Schemas["CreateNotificationChannelRequest"][] = [];
    renderApp("/settings/notifications", { role: "owner", channelSubmit: async (body) => {
      submitted.push(body);
      if (submitted.length === 1) throw new TypeError("offline");
      return json(createdChannel(body), 201);
    } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "添加" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox", { name: "名称" }), "bot-alerts");
    await user.type(within(dialog).getByRole("textbox", { name: "机器人或 Webhook 地址" }), "https://example.com/hook{Enter}");
    expect((await within(dialog).findByRole("alert")).textContent).toContain("请求没有成功");
    await user.keyboard("{Enter}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(submitted).toHaveLength(2);
    expect(submitted[1]?.kind).toBe("feishu");
    expect(await screen.findByText("bot-alerts")).toBeTruthy();
    expect(screen.queryByTestId("signing-secret")).toBeNull();
  });
});

describe("overview", () => {
  it("combines tag and text filters across all task pages", async () => {
    const tasks: Schemas["TaskSummary"][] = Array.from({ length: 101 }, (_, i) => ({
      id: `task-${i}`, project_id: PROJECT.id, name: `task-${String(i).padStart(3, "0")}`, description: "",
      tags: i >= 99 ? ["日报", i === 100 ? "财务" : "销售"] : [],
      state: "inactive", active_version: null, latest_version: 1, last_run: null, next_run_at: null,
      active_runs: 0, updated_at: iso(0), links: { console: "http://lerryn.test/" },
    }));
    renderApp("/", { tasks });
    await screen.findByText("task-099");
    const filter = screen.getByRole("combobox", { name: "按标签筛选" });
    fireEvent.change(filter, { target: { value: "财务" } });
    expect(screen.queryByText("task-099")).toBeNull();
    expect(screen.getByText("task-100")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "搜索任务" }));
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索任务" }), { target: { value: "task-099" } });
    expect(screen.getByText("没有匹配的任务")).toBeTruthy();
    fireEvent.change(filter, { target: { value: "" } });
    expect(screen.getByText("task-099")).toBeTruthy();
    expect(screen.queryByText("task-100")).toBeNull();
  });

  it("shows what needs attention, what is running and today's strip", async () => {
    renderApp("/");
    const attention = await screen.findByRole("region", { name: "需要处理" });
    expect(within(attention).getByText("dataset-backup")).toBeTruthy();
    expect(within(attention).getByText("上传 · 超过 30 分钟")).toBeTruthy();
    // A credential request leads with its name and opens the fill dialog.
    const cred = within(attention).getByText("PRICE_API_TOKEN").closest("button")!;
    expect(within(cred).getByRole("img", { name: "待填写的凭证" })).toBeTruthy();
    const running = screen.getByRole("region", { name: "运行中" });
    expect(within(running).getByText("lead-sync")).toBeTruthy();
    expect(within(running).getByText("写入多维表格")).toBeTruthy();
    expect(await screen.findByRole("region", { name: "今天的运行" })).toBeTruthy();
    expect(screen.getByText("下一次 · 1 个任务")).toBeTruthy();
  });

  it("fills an agent's credential request in a dialog", async () => {
    const { server } = renderApp("/");
    const attention = await screen.findByRole("region", { name: "需要处理" });
    fireEvent.click(within(attention).getByText("PRICE_API_TOKEN").closest("button")!);
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("weekday-digest 需要凭证")).toBeTruthy();
    expect(within(dialog).getByText("读取竞品价格接口")).toBeTruthy();
    expect(within(dialog).getByText("claude-code")).toBeTruthy();
    expect(within(dialog).getByText("仅此任务可用")).toBeTruthy();
    expect(within(dialog).getByText("在价格平台的开发者设置里生成")).toBeTruthy();
    const secret = within(dialog).getByLabelText(/^PRICE_API_TOKEN · 令牌/, { selector: "input" });
    const region = within(dialog).getByLabelText(/^REGION/, { selector: "input" });
    // Secrets are masked until shown; plain fields are not.
    expect(secret.getAttribute("type")).toBe("password");
    expect(region.getAttribute("type")).toBe("text");
    fireEvent.click(within(dialog).getByRole("button", { name: "显示内容" }));
    expect(secret.getAttribute("type")).toBe("text");

    // A required field left empty is caught before anything is sent.
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    expect(await within(dialog).findByText("请填写此项")).toBeTruthy();
    expect(server.requests.some((r) => r.path.endsWith("/values"))).toBe(false);

    fireEvent.change(secret, { target: { value: "sk-price-token" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const sent = server.requests.find((r) => r.path.endsWith("/values"))!;
    expect(sent).toMatchObject({ method: "POST", path: `/api/v1/credential-requests/${REQUEST_ID}/values`, body: { values: { PRICE_API_TOKEN: "sk-price-token" } } });
    // An agent's request is never withdrawn by the console.
    expect(server.requests.some((r) => r.method === "DELETE")).toBe(false);
  });

  it("names planned fires in today's strip with the server's schedule text", async () => {
    // Planned for the current half hour, so the test does not depend on the time of day.
    const at = new Date(Math.floor(NOW.getTime() / 1_800_000) * 1_800_000).toISOString();
    const planned = (id: string, name: string, text: string | null): Schemas["CalendarItem"] => ({ kind: "planned", task: { ...TASK_REF, name }, run_id: null, status: null, start: at, end: null, trigger_id: id, schedule_text: text });
    renderApp("/", { calendar: [planned("tr1", "weekday-digest", "每个工作日 08:00（Asia/Shanghai）"), planned("tr2", "lead-sync", null)] });
    const strip = await screen.findByRole("region", { name: "今天的运行" });
    const ridge = within(strip).getByTitle(/计划 2 次/);
    expect(ridge.title).toContain("weekday-digest · 每个工作日 08:00（Asia/Shanghai）");
    expect(ridge.title).toContain("\nlead-sync");
  });

  it("shows how to connect an agent on a deployment without tasks", async () => {
    renderApp("/", { overview: EMPTY_OVERVIEW });
    expect(await screen.findByRole("heading", { name: "接入你的 Agent" })).toBeTruthy();
  });
});

describe("admin passwords", () => {
  it.each(["create", "reset"] as const)("locks %s password controls while submitting and shows the submitted password", async (action) => {
    let submitted: { password: string } | undefined;
    let resolve: ((response: Response) => void) | undefined;
    renderApp("/admin/users", { admin: true, adminSubmit: async (body) => {
      submitted = body as { password: string };
      return new Promise<Response>((done) => { resolve = done; });
    } });
    if (action === "create") fireEvent.click(await screen.findByRole("button", { name: "创建" }));
    else {
      fireEvent.click(await screen.findByRole("button", { name: "Bob 的操作" }));
      fireEvent.click(await screen.findByRole("menuitem", { name: "重置密码" }));
    }
    const dialog = await screen.findByRole("dialog");
    if (action === "create") fireEvent.change(within(dialog).getByLabelText("用户名"), { target: { value: "bob" } });
    fireEvent.click(within(dialog).getByRole("button", { name: action === "create" ? "创建" : "重置密码" }));
    await waitFor(() => expect(resolve).toBeDefined());
    const regenerate = within(dialog).getByRole("button", { name: "重新生成" }) as HTMLButtonElement;
    expect(regenerate.disabled).toBe(true);
    expect((within(dialog).getByRole("button", { name: "取消" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(regenerate);
    await act(async () => resolve!(json({ ...ME, id: "bob", username: "bob" })));
    expect(await within(dialog).findByRole("button", { name: "完成" })).toBeTruthy();
    expect(dialog.querySelector("code")?.textContent).toBe(submitted!.password);
  });
});

describe("compact list navigation", () => {
  it("toggles the compact task list by keyboard and closes it after selecting the current task", async () => {
    const user = userEvent.setup();
    renderApp(`/tasks/${TASK.id}`, { tasks: [{
      id: TASK.id, project_id: PROJECT.id, name: TASK.name, description: TASK.description, tags: [],
      state: "inactive", active_version: null, latest_version: 2, last_run: null, next_run_at: null,
      active_runs: 0, updated_at: iso(0), links: TASK.links,
    }] });
    await screen.findByRole("heading", { name: "weekday-digest" });
    const list = screen.getByRole("region", { name: "任务列表" });
    const toggle = within(list).getByRole("button", { expanded: false });
    const body = document.getElementById(toggle.getAttribute("aria-controls")!)!;
    expect(body.className).toBe("list-body");
    toggle.focus();
    await user.keyboard("{Enter}");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(list.getAttribute("data-expanded")).toBe("true");
    await user.click(within(body).getByRole("link", { name: /weekday-digest/ }));
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(list.getAttribute("data-expanded")).toBe("false");
    expect(screen.getByRole("heading", { name: "weekday-digest" })).toBeTruthy();
  });

  it("closes a compact settings list when the selected section changes", async () => {
    const { router } = renderApp("/settings/tokens");
    const list = await screen.findByRole("region", { name: "密钥" });
    const toggle = within(list).getByRole("button", { expanded: false });
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    await act(async () => { await router.navigate({ to: "/settings/tools" }); });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("task page", () => {
  it.each(["结果", "上次结果"])("shows the latest workflow above %s and folds each panel independently", async (resultTitle) => {
    const workflow = [
      { title: "读取邮件", description: "读取尚未处理的收件箱邮件。" },
      { title: "保存摘要", description: "整理邮件并保存摘要。" },
    ];
    const panels: Schemas["Manifest"]["panels"] = resultTitle === "上次结果"
      ? [{ id: "emails", type: "metric", title: "已处理邮件", source: "/processed" }]
      : undefined;
    renderApp(`/tasks/${TASK.id}`, {
      task: {
        active_version: 1,
        latest_version: {
          ...TASK.latest_version,
          manifest: {
            ...TASK.latest_version.manifest,
            runtime: { language: "python", version: "3.12" },
            credentials: [{ name: "LLM_API_KEY", as: "env", optional: false }],
            network: "full", concurrency: "forbid", side_effects: false, workflow, panels,
          },
        },
      },
      runs: [{
        ...FAILED_RUN, task: TASK_REF, status: "completed",
        trigger: { ...FAILED_RUN.trigger, kind: "cron" },
        error: null,
        current_stage: null, result_status: "ok", result_summary: "摘要已保存",
      }],
      run: { result: { status: "ok", summary: "摘要已保存", data: { processed: 2 } } },
    });
    const flow = await screen.findByRole("region", { name: "任务说明" });
    const region = within(flow);
    expect(region.getByText("v2")).toBeTruthy();
    const steps = region.getAllByRole("listitem");
    expect(steps.map((step) => within(step).getByRole("heading").textContent)).toEqual(workflow.map((step) => step.title));
    expect(steps.map((step) => step.querySelector("p")?.textContent)).toEqual(workflow.map((step) => step.description));
    const result = await screen.findByRole("region", { name: resultTitle });
    expect(flow.compareDocumentPosition(result)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    const flowButton = region.getByRole("button", { name: "任务说明" });
    const resultPanel = within(result);
    const resultButton = resultPanel.getByRole("button", { name: resultTitle });
    expect(flowButton.getAttribute("aria-expanded")).toBe("true");
    expect(resultButton.getAttribute("aria-expanded")).toBe("true");

    const user = userEvent.setup();
    await user.click(flowButton);
    expect(flowButton.getAttribute("aria-expanded")).toBe("false");
    expect(region.queryByRole("list")).toBeNull();
    expect(resultPanel.getByRole("group", { name: resultTitle })).toBeTruthy();
    expect(region.getByText("v2")).toBeTruthy();

    await user.click(resultButton);
    expect(resultButton.getAttribute("aria-expanded")).toBe("false");
    expect(resultPanel.queryByRole("group", { name: resultTitle })).toBeNull();
    await user.keyboard("{Enter}");
    expect(resultButton.getAttribute("aria-expanded")).toBe("true");
    expect(resultPanel.getByRole("group", { name: resultTitle })).toBeTruthy();
    expect(flowButton.getAttribute("aria-expanded")).toBe("false");
  });

  it("shows and searches a task title, falling back to the name for older tasks", async () => {
    const title = "GitHub 费用日报";
    const summary: Schemas["TaskSummary"] = {
      ...TASK, state: "active", latest_version: 2, active_runs: 0, last_run: null, next_run_at: null,
    };
    renderApp(`/tasks/${TASK.id}`, {
      task: { title },
      tasks: [{ ...summary, title }, { ...summary, id: "untitled", name: "untitled-task" }],
    });
    expect(await screen.findByRole("heading", { name: title })).toBeTruthy();
    expect(screen.queryByRole("region", { name: "任务说明" })).toBeNull();
    const list = within(await screen.findByRole("region", { name: "任务列表" }));
    expect(await list.findByRole("link", { name: new RegExp(title) })).toBeTruthy();
    expect(list.getByRole("link", { name: /untitled-task/ })).toBeTruthy();
    expect(list.queryByText(TASK.name)).toBeNull();

    fireEvent.click(list.getByRole("button", { name: "搜索任务" }));
    fireEvent.change(list.getByRole("searchbox", { name: "搜索任务" }), { target: { value: "github 费用" } });
    expect(list.getByRole("link", { name: new RegExp(title) })).toBeTruthy();
    expect(list.queryByRole("link", { name: /untitled-task/ })).toBeNull();
  });

  it("lets a maintainer replace and clear task tags", async () => {
    const { server } = renderApp(`/tasks/${TASK.id}`, { role: "maintainer", task: { tags: ["销售"] } });
    const user = userEvent.setup();
    const title = await screen.findByRole("heading", { name: TASK.name });
    const copy = within(title.parentElement!).getByRole("button", { name: "复制任务链接" });
    await user.click(copy);
    expect(await navigator.clipboard.readText()).toBe(TASK.links.console);
    const actions = within(copy.parentElement!);
    const edit = actions.getByRole("button", { name: "编辑标签" });
    expect(actions.getAllByRole("button")).toEqual([edit, copy]);
    await user.click(edit);
    const dialog = await screen.findByRole("dialog", { name: "编辑标签" });
    const input = within(dialog).getByRole("textbox", { name: "任务标签" });
    expect((input as HTMLTextAreaElement).value).toBe("销售");
    fireEvent.change(input, { target: { value: "  日报  \n\nCRM" } });
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const group = screen.getByRole("group", { name: "任务标签" });
    expect(within(group).getByText("日报")).toBeTruthy();
    expect(within(group).queryByText("销售")).toBeNull();
    await user.click(edit);
    await user.clear(screen.getByRole("textbox", { name: "任务标签" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(screen.queryByRole("group", { name: "任务标签" })).toBeNull());
    expect(actions.getByRole("button", { name: "编辑标签" })).toBe(edit);
    expect(server.requests.filter((r) => r.method === "PUT" && r.path.endsWith("/tags")).map((r) => r.body))
      .toEqual([{ tags: ["日报", "CRM"] }, { tags: [] }]);
  });

  it("confirms deletion from the task menu, hides runs by default and preserves task history", async () => {
    const { server, router } = renderApp(`/tasks/${TASK.id}`, { role: "owner", showRunInList: true });
    const user = userEvent.setup();
    const more = await screen.findByRole("button", { name: "更多任务操作" });
    expect(screen.queryByRole("button", { name: "删除任务" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "删除任务" })).toBeNull();
    await user.click(more);
    await user.click(screen.getByRole("menuitem", { name: "删除任务" }));
    let dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/历史运行仍可查看/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() => expect(document.activeElement).toBe(more));
    expect(server.requests.some((r) => r.method === "DELETE")).toBe(false);
    await user.click(more);
    await user.click(screen.getByRole("menuitem", { name: "删除任务" }));
    dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "删除任务" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    expect(server.requests.filter((r) => r.method === "DELETE")).toEqual([expect.objectContaining({ path: `/api/v1/tasks/${TASK.id}` })]);

    await act(() => router.navigate({ to: "/runs", search: { view: "list" } }));
    const list = within(await screen.findByRole("region", { name: "运行列表" }));
    expect(await list.findByText("尚无运行记录")).toBeTruthy();
    expect(list.queryByRole("link", { name: /weekday-digest/ })).toBeNull();

    await act(() => router.navigate({ to: "/runs", search: { view: "list", task: TASK.id } }));
    expect(await list.findByRole("link", { name: /weekday-digest/ })).toBeTruthy();
  });

  it.each(["queued", "preparing", "running", "cancelling"] as const)("shows %s for an inactive task with an active test run", async (status) => {
    const labels = { queued: "排队中", preparing: "准备中", running: "运行中", cancelling: "取消中" };
    renderApp(`/tasks/${TASK.id}`, {
      task: { state: "inactive", active_version: null },
      runs: [{
        ...NEW_RUN, task: TASK_REF, test: true, status, trigger: { ...NEW_RUN.trigger, kind: "manual" },
        result_status: null, result_summary: null, current_stage: null,
      }],
    });
    const board = await screen.findByRole("main", { name: TASK.name });
    expect(await within(board).findByRole("img", { name: labels[status] })).toBeTruthy();
    expect(within(board).queryByText("未激活")).toBeNull();
  });

  it("keeps an inactive task visibly running when its active run is older than the recent history", async () => {
    const summary: Schemas["TaskSummary"] = {
      ...TASK, state: "inactive", active_version: null, latest_version: 2,
      active_runs: 1, last_run: null, next_run_at: null,
    };
    renderApp(`/tasks/${TASK.id}`, { task: { state: "inactive", active_version: null }, tasks: [summary] });
    const board = await screen.findByRole("main", { name: TASK.name });
    expect(await within(board).findByRole("img", { name: "运行中" })).toBeTruthy();
    const running = screen.getByRole("group", { name: "运行中" });
    expect(within(running).getByRole("img", { name: "运行中" })).toBeTruthy();
    expect(within(board).queryByText("未激活")).toBeNull();
  });

  it("separates scheduled tasks from paused tasks and uses the same calendar status on the board", async () => {
    const summary: Schemas["TaskSummary"] = {
      ...TASK, state: "active", latest_version: 2, active_runs: 0, last_run: null,
      next_run_at: TRIGGERS.triggers[0]!.next_fire_at,
    };
    renderApp(`/tasks/${TASK.id}`, { tasks: [summary, { ...summary, id: "paused", name: "paused-task", state: "inactive", next_run_at: null }] });
    const scheduled = await screen.findByRole("group", { name: "按计划" });
    expect(within(scheduled).getByRole("img", { name: "按计划" }).querySelector("svg.lucide-calendar-clock")).toBeTruthy();
    expect(within(scheduled).queryByText("paused-task")).toBeNull();
    const inactive = screen.getByRole("group", { name: "未启用" });
    expect(within(inactive).getByRole("img", { name: "已暂停" })).toBeTruthy();
    const board = await screen.findByRole("main", { name: TASK.name });
    expect(await within(board).findByRole("img", { name: "按计划" })).toBeTruthy();
  });

  it("disables and enables the previously active version, independently of manual runs", async () => {
    const { server } = renderApp(`/tasks/${TASK.id}`, { role: "maintainer", manualRun: false, task: { active_version: 1 } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "停用任务" }));
    let dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/已排队和正在执行的运行继续完成/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "停用任务" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByText("已暂停")).toBeTruthy();
    const more = screen.getByRole("button", { name: "更多任务操作" });
    await user.click(more);
    const menu = screen.getByRole("menu", { name: "更多任务操作" });
    expect(within(menu).queryByRole("menuitem", { name: "跳过下次" })).toBeNull();
    expect(within(menu).getByRole("menuitem", { name: "删除任务" })).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    await user.click(screen.getByRole("button", { name: "启用任务" }));
    dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/启用 v1/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "启用任务" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByText("按计划")).toBeTruthy();
    expect(server.requests.filter((r) => r.method === "POST").map((r) => r.body)).toEqual([{ active: false }, { active: true, version: 1 }]);
  });

  it("confirms the exact next time and refreshes the displayed schedule after skipping", async () => {
    const { server } = renderApp(`/tasks/${TASK.id}`, { role: "owner" });
    const user = userEvent.setup();
    const more = await screen.findByRole("button", { name: "更多任务操作" });
    await screen.findByText("按计划");
    expect(screen.queryByRole("button", { name: "跳过下次" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "跳过下次" })).toBeNull();
    await user.click(more);
    await user.click(screen.getByRole("menuitem", { name: "跳过下次" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "取消" }));
    expect(server.requests.filter((r) => r.method === "POST")).toHaveLength(0);
    await user.click(more);
    await user.click(screen.getByRole("menuitem", { name: "跳过下次" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "跳过下次" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const post = server.requests.find((r) => r.method === "POST")!;
    expect(post.path).toBe(`/api/v1/tasks/${TASK.id}/skip-next-run`);
    expect(post.body).toEqual({ scheduled_at: TRIGGERS.triggers[0]!.next_fire_at });
    expect(screen.getByText("按计划")).toBeTruthy();
    expect(server.requests.filter((r) => r.path.endsWith("/triggers"))).toHaveLength(2);
  });

  it("keeps the confirmation open and submits once while waiting", async () => {
    let finish: ((response: Response) => void) | undefined;
    const { server } = renderApp(`/tasks/${TASK.id}`, { role: "owner", taskControlSubmit: () => new Promise((resolve) => { finish = resolve; }) });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "停用任务" }));
    const dialog = await screen.findByRole("dialog");
    const button = within(dialog).getByRole("button", { name: "停用任务" });
    fireEvent.click(button);
    fireEvent.click(button);
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeTruthy();
    await waitFor(() => expect(server.requests.filter((r) => r.method === "POST")).toHaveLength(1));
    await act(async () => finish!(json({ ...TASK, state: "inactive" })));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("explains when a version cannot be enabled", async () => {
    renderApp(`/tasks/${TASK.id}`, { role: "owner", task: { state: "inactive", active_version: null }, taskControlSubmit: async () => problem(409, "version_not_tested") });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "启用任务" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "启用任务" }));
    expect((await screen.findByRole("alert")).textContent).toContain("还没有通过测试");
  });

  it.each(["viewer", "archived"])("hides lifecycle controls for %s tasks", async (state) => {
    renderApp(`/tasks/${TASK.id}`, { role: state === "viewer" ? "viewer" : "owner", task: state === "archived" ? { archived_at: iso(-10) } : {} });
    await screen.findByRole("heading", { name: "weekday-digest" });
    await screen.findByRole("img", { name: "有凭证不可用" });
    for (const name of ["停用任务", "启用任务", "更多任务操作", "立即触发", "编辑标签"]) expect(screen.queryByRole("button", { name })).toBeNull();
  });

  it("shows activation, the schedule, credentials and versions, read-only", async () => {
    renderApp(`/tasks/${TASK.id}`);
    await screen.findByRole("heading", { name: "weekday-digest" });
    expect(screen.getByText("按计划")).toBeTruthy();
    expect((await screen.findAllByText("每个工作日 08:00")).length).toBeGreaterThan(0);
    expect(screen.getByTitle("0 8 * * 1-5 · Asia/Shanghai")).toBeTruthy();
    expect(await screen.findByText("凭证 1")).toBeTruthy();
    expect(screen.getByRole("img", { name: "有凭证不可用" })).toBeTruthy();
    // A missing credential with a pending request opens the fill dialog.
    fireEvent.click(screen.getByRole("button", { name: "填写" }));
    expect(await within(await screen.findByRole("dialog")).findByText("weekday-digest 需要凭证")).toBeTruthy();
    // Without run control in the project: no editing, running or pausing.
    for (const name of [/运行$/, /暂停/, /编辑/, /重试/, /立即触发/]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
  });

  it.each([
    { scenario: "no enabled schedule", triggerItems: [{ ...MORNING_TRIGGER, enabled: false }], body: undefined },
    { scenario: "one enabled schedule", triggerItems: [MORNING_TRIGGER, { ...AFTERNOON_TRIGGER, enabled: false }], body: { params: MORNING_TRIGGER.params, test: false } },
  ])("starts a run directly with $scenario and opens it", async ({ triggerItems, body }) => {
    const { router, server } = renderApp(`/tasks/${TASK.id}`, { role: "maintainer", triggerItems });
    const button = await screen.findByRole("button", { name: "立即触发" });
    expect(button.textContent).toContain("立即触发");
    fireEvent.click(button);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/runs/${NEW_RUN.id}`));
    const post = server.requests.find((r) => r.method === "POST")!;
    expect(post.path).toBe(`/api/v1/tasks/${TASK.id}/runs`);
    expect(post.body).toEqual(body);
    expect(post.idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(await screen.findByText("控制台发起")).toBeTruthy();
  });

  it("waits for a schedule choice and starts only the selected one", async () => {
    const { router, server } = renderApp(`/tasks/${TASK.id}`, { role: "maintainer", triggerItems: [MORNING_TRIGGER, AFTERNOON_TRIGGER] });
    const user = userEvent.setup();
    const button = await screen.findByRole("button", { name: "立即触发" });
    button.focus();
    await user.keyboard("{ArrowDown}");
    const menu = within(await screen.findByRole("menu", { name: "立即触发" }));
    expect(menu.getByRole("menuitem", { name: /weekday-morning/ }).textContent).toContain("08:00");
    expect(menu.getByRole("menuitem", { name: /weekday-afternoon/ }).textContent).toContain("15:00");
    expect(server.requests.filter((r) => r.method === "POST")).toEqual([]);
    await user.keyboard("{ArrowDown}{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe(`/runs/${NEW_RUN.id}`));
    const posts = server.requests.filter((r) => r.method === "POST");
    expect(posts).toHaveLength(1);
    expect(posts[0]).toMatchObject({ path: `/api/v1/tasks/${TASK.id}/runs`, body: { params: AFTERNOON_TRIGGER.params } });
  });

  it("stays on the task when the run is refused", async () => {
    const { router } = renderApp(`/tasks/${TASK.id}`, { role: "owner", startError: "run_in_progress" });
    const user = userEvent.setup();
    const button = await screen.findByRole("button", { name: "立即触发" });
    await user.click(button);
    await user.unhover(button);
    await user.hover(button);
    expect((await screen.findByRole("tooltip")).textContent).toContain("上一次运行");
    expect(router.state.location.pathname).toBe(`/tasks/${TASK.id}`);
  });

  it("hides manual runs from viewers", async () => {
    renderApp(`/tasks/${TASK.id}`, { role: "viewer" });
    await screen.findByRole("heading", { name: "weekday-digest" });
    await screen.findByRole("img", { name: "有凭证不可用" });
    expect(screen.queryByRole("button", { name: "立即触发" })).toBeNull();
  });

  it("hides manual runs when the agent turned them off", async () => {
    renderApp(`/tasks/${TASK.id}`, { role: "owner", manualRun: false });
    await screen.findByRole("heading", { name: "weekday-digest" });
    await screen.findByRole("img", { name: "有凭证不可用" });
    expect(screen.queryByRole("button", { name: "立即触发" })).toBeNull();
  });
});

describe("runs list", () => {
  afterEach(() => localStorage.removeItem("lerryn.runs.view"));

  const runAt = (n: number, taskId: string, name: string, over: Partial<Schemas["RunSummary"]> = {}): Schemas["RunSummary"] => ({
    id: `0192f3a1-0000-7000-8000-00000000${String(n).padStart(4, "0")}`,
    task: { id: taskId, name, project_id: PROJECT.id },
    version: n,
    status: "completed",
    test: false,
    trigger: { kind: "cron", trigger_id: null, scheduled_at: null, user_id: null, client: null },
    attempt: 1,
    retry_of: null,
    rerun_of: null,
    skip_reason: null,
    queued_at: iso(-n),
    started_at: iso(-n),
    finished_at: iso(-n),
    error: null,
    result_status: null,
    result_summary: null,
    current_stage: null,
    archived_at: null,
    links: { console: "http://lerryn.test/" },
    ...over,
  });
  const runs = [runAt(3, "ta", "linear-digest"), runAt(2, "tb", "dataset-backup", { status: "failed" }), runAt(1, "ta", "linear-digest")];

  it("searches the loaded runs", async () => {
    renderApp("/runs?view=list", { runs });
    const list = await screen.findByRole("region", { name: "运行列表" });
    await within(list).findAllByText("linear-digest");
    fireEvent.click(screen.getByRole("button", { name: "搜索运行" }));
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索运行" }), { target: { value: "backup" } });
    expect(within(list).queryByText("linear-digest")).toBeNull();
    expect(within(list).getByText("dataset-backup")).toBeTruthy();
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索运行" }), { target: { value: "nothing" } });
    expect(within(list).getByText("已加载的运行中没有匹配项")).toBeTruthy();
    fireEvent.keyDown(screen.getByRole("searchbox", { name: "搜索运行" }), { key: "Escape" });
    expect(within(list).queryByRole("searchbox")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "搜索运行" }));
    expect(within(list).getAllByText("linear-digest")).toHaveLength(2);
  });

  it("groups runs under their task and folds one task away", async () => {
    const { router } = renderApp("/runs?view=list", { runs });
    const list = await screen.findByRole("region", { name: "运行列表" });
    await within(list).findAllByText("linear-digest");
    fireEvent.click(screen.getByRole("button", { name: "按任务分组" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ view: "tasks" }));
    const head = within(list).getByRole("button", { name: /linear-digest/ });
    expect(head.getAttribute("aria-expanded")).toBe("true");
    expect(within(head).getByText("2 次")).toBeTruthy();
    expect(within(within(list).getByRole("group", { name: "linear-digest" })).getAllByRole("link")).toHaveLength(2);
    fireEvent.click(head);
    expect(head.getAttribute("aria-expanded")).toBe("false");
    expect(within(list).queryByRole("group", { name: "linear-digest" })).toBeNull();
    expect(within(list).getByRole("group", { name: "dataset-backup" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "全部折叠" }));
    expect(within(list).queryByRole("group", { name: "dataset-backup" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "全部展开" }));
    expect(within(list).getByRole("group", { name: "linear-digest" })).toBeTruthy();
  });

  it("preserves grouping across archive filters and can switch to the board", async () => {
    const archived = runAt(4, "tc", "archived-report", { archived_at: iso(-1) });
    const { router } = renderApp("/runs?view=tasks", { runs: [...runs, archived] });
    const list = within(await screen.findByRole("region", { name: "运行列表" }));
    await list.findByRole("group", { name: "linear-digest" });
    expect(list.queryByRole("group", { name: "archived-report" })).toBeNull();
    expect(list.getByRole("link", { name: "列表" }).getAttribute("aria-current")).toBe("page");

    await userEvent.click(list.getByRole("button", { name: "已归档" }));
    await list.findByRole("group", { name: "archived-report" });
    expect(router.state.location.search).toEqual({ view: "tasks", filter: "archived" });
    expect(list.queryByRole("group", { name: "linear-digest" })).toBeNull();

    await userEvent.click(list.getByRole("link", { name: "看板" }));
    await screen.findByRole("main", { name: "运行看板" });
    expect(router.state.location.search).toEqual({ view: "board" });
  });
});

describe("run page", () => {
  const archivePreferenceKey = `lerryn.runs.skipArchiveConfirmation.${ME.id}`;
  afterEach(() => {
    localStorage.removeItem(archivePreferenceKey);
    localStorage.removeItem("lerryn.runs.view");
  });

  it("keeps the confirmed archive target when another client changes the record", async () => {
    const { server, queryClient } = renderApp(`/runs/${FAILED_RUN.id}`, { role: "owner" });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "归档记录" }));
    const dialog = await screen.findByRole("dialog");
    await act(async () => { queryClient.setQueryData(["runs", FAILED_RUN.id], { ...FAILED_RUN, archived_at: iso(0) }); });
    await user.click(within(dialog).getByRole("button", { name: "归档记录" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(server.requests.find((r) => r.path.endsWith("/archive"))?.body).toEqual({ archived: true });
  });

  it.each([false, true])("submits archiving once and shows failures (skip confirmation: %s)", async (skip) => {
    if (skip) localStorage.setItem(archivePreferenceKey, "true");
    let finish: ((response: Response) => void) | undefined;
    const { server } = renderApp(`/runs/${FAILED_RUN.id}`, { role: "owner", runArchiveSubmit: () => new Promise((resolve) => { finish = resolve; }) });
    const user = userEvent.setup();
    const archive = await screen.findByRole("button", { name: "归档记录" });
    if (!skip) await user.click(archive);
    const button = skip ? archive : within(await screen.findByRole("dialog")).getByRole("button", { name: "归档记录" });
    fireEvent.click(button);
    fireEvent.click(button);
    await user.keyboard("{Escape}");
    expect(!!screen.queryByRole("dialog")).toBe(!skip);
    await waitFor(() => expect(server.requests.filter((r) => r.path.endsWith("/archive"))).toHaveLength(1));
    await act(async () => finish!(problem(500, "internal_error")));
    expect((await screen.findByRole("alert")).textContent).toContain("归档状态未更新");
  });

  it("archives and restores a run without hiding its readable history", async () => {
    const { server } = renderApp(`/runs/${FAILED_RUN.id}`, { role: "maintainer" });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "归档记录" }));
    let dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/完整日志和现有产物长期保留/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "归档记录" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByText("已归档")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "取消归档" }));
    dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/重新按部署的保留策略清理/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "取消归档" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(server.requests.filter((r) => r.path.endsWith("/archive")).map((r) => r.body)).toEqual([{ archived: true }, { archived: false }]);
  });

  it("remembers the archive preference from the attention preview after a successful archive", async () => {
    const { router, server } = renderApp("/runs?view=board", { role: "owner", showRunInList: true });
    const user = userEvent.setup();
    const attention = within(await screen.findByRole("region", { name: "待处理" }));
    await user.click(await attention.findByRole("link", { name: /weekday-digest/ }));
    const preview = within(await screen.findByRole("complementary", { name: "运行预览" }));
    const archive = () => preview.findByRole("button", { name: "归档记录" });

    await user.click(await archive());
    let dialog = within(await screen.findByRole("dialog", { name: "归档记录" }));
    await user.click(dialog.getByText("不再提示"));
    await user.click(dialog.getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(server.requests.filter((r) => r.path.endsWith("/archive"))).toHaveLength(0);

    await user.click(await archive());
    dialog = within(await screen.findByRole("dialog", { name: "归档记录" }));
    expect((dialog.getByRole("checkbox", { name: "不再提示" }) as HTMLInputElement).checked).toBe(false);
    await user.click(dialog.getByText("不再提示"));
    await user.click(dialog.getByRole("button", { name: "归档记录" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(attention.queryByRole("link", { name: /weekday-digest/ })).toBeNull();

    await act(() => router.navigate({ to: "/runs/$runId", params: { runId: FAILED_RUN.id } }));
    await user.click(await screen.findByRole("button", { name: "取消归档" }));
    dialog = within(await screen.findByRole("dialog", { name: "取消归档" }));
    expect(dialog.queryByRole("checkbox")).toBeNull();
    await user.click(dialog.getByRole("button", { name: "取消归档" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    await user.click(screen.getByRole("button", { name: "归档记录" }));
    await screen.findByRole("button", { name: "取消归档" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(server.requests.filter((r) => r.path.endsWith("/archive")).map((r) => r.body)).toEqual([
      { archived: true }, { archived: false }, { archived: true },
    ]);
  });

  it.each(["viewer", "running", "expired"])("prevents archiving for %s", async (state) => {
    renderApp(`/runs/${FAILED_RUN.id}`, { role: state === "viewer" ? "viewer" : "owner", run: state === "running" ? { status: "running" } : state === "expired" ? { logs_expired_at: iso(0) } : {} });
    await screen.findByRole("heading", { name: state === "running" ? "运行中" : "执行失败" });
    await screen.findByRole("button", { name: "复制运行 ID" });
    const button = screen.queryByRole("button", { name: "归档记录" });
    if (state === "expired") expect(button?.hasAttribute("disabled")).toBe(true);
    else expect(button).toBeNull();
  });

  it("opens the archived filter through the server query", async () => {
    const { server } = renderApp("/runs", { role: "owner", showRunInList: true, run: { archived_at: iso(-5) } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "已归档" }));
    expect(await screen.findByRole("button", { name: "取消归档" })).toBeTruthy();
    expect(server.requests.some((r) => r.path === "/api/v1/runs")).toBe(true);
    expect(server.fetch.mock.calls.some(([req]) => new URL(req.url).searchParams.get("archived") === "true")).toBe(true);
  });
  it("retries a finished run whatever the task's manual run switch says", async () => {
    const { router, server } = renderApp(`/runs/${FAILED_RUN.id}`, { role: "maintainer", manualRun: false });
    fireEvent.click(await screen.findByRole("button", { name: "重试" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/runs/${NEW_RUN.id}`));
    const post = server.requests.find((r) => r.method === "POST")!;
    expect(post.path).toBe(`/api/v1/runs/${FAILED_RUN.id}/rerun`);
    expect(post.idempotencyKey).toBeTruthy();
  });

  it("offers no retry to viewers", async () => {
    renderApp(`/runs/${FAILED_RUN.id}`, { role: "viewer" });
    await screen.findByText("退出码 1");
    await screen.findByRole("heading", { name: /失败/ });
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
  });
});

describe("tools", () => {
  it("shows built-in capabilities and connection details without registered project tools", async () => {
    const user = userEvent.setup();
    renderApp("/settings/tools", { toolsResponse: () => json({ items: [] }) });
    const builtins = await screen.findByRole("list", { name: "内置 MCP 工具" });
    const names = [...contract.matchAll(/^\s+tool: ([a-z_]+)$/gm)].map((match) => match[1]);
    expect(within(builtins).getAllByRole("listitem").map((row) => row.querySelector("code")?.textContent).sort())
      .toEqual([...names, "api_describe", "api_call"].sort());
    expect(within(builtins).getByText("task_apply")).toBeTruthy();
    expect(within(builtins).getByText("api_describe")).toBeTruthy();
    expect(within(builtins).getByText("api_call")).toBeTruthy();
    expect(await screen.findByText("尚无登记的工具")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "任务内置能力 · sbx" })).toBeTruthy();
    const interfaces = screen.getByRole("region", { name: "接入方式" });
    expect(within(interfaces).getByText(`${window.location.origin}/api/v1`)).toBeTruthy();
    expect(within(interfaces).getByText(`lerryn login ${window.location.origin}`)).toBeTruthy();
    await user.click(within(interfaces).getByRole("button", { name: "复制 MCP 地址" }));
    expect(await navigator.clipboard.readText()).toBe(`${window.location.origin}/mcp`);
    const guide = screen.getByRole("link", { name: "下载英文文档" });
    expect(guide.getAttribute("href")).toBe("/docs/en/tools.md");
    expect(guide.getAttribute("download")).toBe("lerryn-tools.md");
    const guideZh = screen.getByRole("link", { name: "下载中文文档" });
    expect(guideZh.getAttribute("href")).toBe("/docs/zh-CN/tools.md");
    expect(guideZh.getAttribute("download")).toBe("lerryn-tools-zh-CN.md");
    expect(screen.getByRole("link", { name: "管理访问令牌" }).getAttribute("href")).toBe("/settings/tokens");
  });

  it("offers both guides with the English interface", async () => {
    renderApp("/settings/tools", { locale: "en", toolsResponse: () => json({ items: [] }) });
    const guide = await screen.findByRole("link", { name: "Download English guide" });
    expect(guide.getAttribute("href")).toBe("/docs/en/tools.md");
    expect(screen.getByRole("link", { name: "Download Chinese guide" }).getAttribute("href")).toBe("/docs/zh-CN/tools.md");
    expect(screen.getByRole("list", { name: "Built-in MCP tools" })).toBeTruthy();
  });

  it("keeps built-ins and the guide available when the project registry fails", async () => {
    renderApp("/settings/tools", { toolsResponse: () => problem(500, "internal_error") });
    expect(await screen.findByRole("button", { name: "重试" })).toBeTruthy();
    expect(screen.getByRole("list", { name: "内置 MCP 工具" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "下载英文文档" })).toBeTruthy();
  });

  it("shows the task's tools with their kind and marks a name the project never registered", async () => {
    renderApp(`/tasks/${TASK.id}`);
    await screen.findByRole("heading", { name: "weekday-digest" });
    const runtime = screen.getByRole("group", { name: "运行环境" });
    expect(await within(runtime).findByText("MCP")).toBeTruthy();
    expect(within(runtime).getByText("crm-mcp")).toBeTruthy();
    expect(within(runtime).getByText("ledger")).toBeTruthy();
    expect(within(runtime).getByRole("img", { name: "未登记" })).toBeTruthy();
    // A script tool shows its file tree instead of an address.
    expect(within(runtime).getByText("SCRIPT")).toBeTruthy();
    expect(within(runtime).getByText("3f9a0c4be1d2 · 2 个文件")).toBeTruthy();
    expect(within(runtime).getByText("fmt-lib").getAttribute("title")).toBe(`格式化金额\n${TREE}`);
    // The chip itself carries the warning.
    expect(screen.getByRole("button", { name: /Python 3\.12/ }).querySelector(".dot--bad")).toBeTruthy();
  });

  it("lists registered project tools in their own settings section", async () => {
    renderApp("/settings/tools");
    expect(await screen.findByText("查询客户和商机")).toBeTruthy();
    expect(screen.getByText("crm.example.com")).toBeTruthy();
    expect(screen.getByRole("button", { name: "复制 crm-mcp 的地址" })).toBeTruthy();
    // Script tools have files, not an address.
    expect(screen.getByText("SCRIPT")).toBeTruthy();
    expect(screen.getByTitle(TREE).textContent).toBe("3f9a0c4be1d2 · 2 个文件");
    expect(screen.queryByRole("button", { name: "复制 fmt-lib 的地址" })).toBeNull();
    // A tool without an address is listed without one.
    expect(screen.getByText("没有地址")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "复制 bare-http 的地址" })).toBeNull();
    for (const name of [/登记/, /移除/, /删除/, /编辑/]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
  });
});

describe("tokens page", () => {
  it("offers the skill install command beside the connection addresses", async () => {
    renderApp("/settings/tokens");
    const connect = await screen.findByRole("region", { name: "接入" });
    expect(within(connect).getByText("lerryn skills install")).toBeTruthy();
    expect(within(connect).getByRole("button", { name: "复制 Skill 安装命令" })).toBeTruthy();
  });
});

describe("credentials page", () => {
  const manyCredentials = (): Schemas["Credential"][] => Array.from({ length: 201 }, (_, i) => ({
    ...CREDENTIAL, id: `credential-${i}`, name: `CRED_${String(i).padStart(3, "0")}`, grants: [], expires_at: null,
  }));

  it("configures AI, keeps the key blank when editing, and removes the configuration", async () => {
    const { server } = renderApp("/settings/credentials");
    fireEvent.click(await screen.findByRole("button", { name: "配置 AI" }));
    let dialog = within(await screen.findByRole("dialog"));
    const url = "https://provider.example/v1";
    fireEvent.change(dialog.getByLabelText(/API 基础地址/), { target: { value: url } });
    fireEvent.change(dialog.getByLabelText(/API Key/), { target: { value: "sk-provider-test" } });
    fireEvent.change(dialog.getByLabelText(/可用模型/), { target: { value: "provider/model-a\n\nprovider/model-b" } });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));
    fireEvent.click(await screen.findByRole("button", { name: "编辑配置" }));
    expect(server.requests.filter((r) => r.method === "PUT" && r.path === "/api/v1/me/ai")).toMatchObject([
      { body: { base_url: url, api_key: "sk-provider-test", models: ["provider/model-a", "provider/model-b"] } },
    ]);
    dialog = within(await screen.findByRole("dialog"));
    expect((dialog.getByLabelText(/API Key/) as HTMLInputElement).value).toBe("");
    fireEvent.change(dialog.getByLabelText(/可用模型/), { target: { value: "provider/model-b" } });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));
    await screen.findByText("1 个模型");
    expect(server.requests.filter((r) => r.method === "PUT" && r.path === "/api/v1/me/ai").at(-1)?.body).toEqual({
      base_url: url, models: ["provider/model-b"],
    });
    fireEvent.click(await screen.findByRole("button", { name: "编辑配置" }));
    fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "移除配置" }));
    fireEvent.click(within(await screen.findByRole("dialog", { name: "移除 AI 配置" })).getByRole("button", { name: "移除配置" }));
    expect(await screen.findByRole("button", { name: "配置 AI" })).toBeTruthy();
  });

  it("loads later credentials with revoke controls while retaining requests and historical records", async () => {
    const credentials = manyCredentials();
    credentials[198] = { ...credentials[198]!, status: "revoked", revoked_at: iso(-60) };
    credentials[199] = { ...credentials[199]!, status: "expired", expires_at: iso(-60) };
    const { server } = renderApp("/settings/credentials", { credentials, pendingCredentialRequests: [TASK_REQUEST] });
    await screen.findByText("CRED_199");
    expect(screen.getByText("PRICE_API_TOKEN")).toBeTruthy();
    expect(screen.getByText("CRED_198")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "更新 CRED_198" })).toBeNull();
    expect(screen.queryByRole("button", { name: "撤销 CRED_199" })).toBeNull();
    expect(screen.queryByText("CRED_200")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "显示更多" }));
    const revoke = await screen.findByRole("button", { name: "撤销 CRED_200" });
    expect(screen.getByRole("button", { name: "更新 CRED_200" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "显示更多" })).toBeNull();
    fireEvent.click(revoke);
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "撤销" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(screen.queryByRole("button", { name: "撤销 CRED_200" })).toBeNull());
    expect(screen.getByText("CRED_200")).toBeTruthy();
    expect(server.requests.some((r) => r.method === "DELETE" && r.path === "/api/v1/credentials/credential-200")).toBe(true);
  });

  it("deletes only the selected revoked credential when names match", async () => {
    const revoked = { ...CREDENTIAL, status: "revoked", revoked_at: iso(-60) } as const;
    const { server } = renderApp("/settings/credentials", {
      credentials: [
        CREDENTIAL,
        { ...revoked, id: "revoked-first" },
        { ...revoked, id: "revoked-second", revoked_at: iso(-30) },
        { ...CREDENTIAL, id: "expired", name: "EXPIRED_KEY", status: "expired", expires_at: iso(-60) },
      ],
      pendingCredentialRequests: [TASK_REQUEST],
    });
    const buttons = await screen.findAllByRole("button", { name: "删除凭证 LLM_API_KEY" });
    expect(buttons).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "删除凭证 EXPIRED_KEY" })).toBeNull();
    const purge = buttons[1]!;
    const purgePath = "/api/v1/credentials/revoked-second/purge";
    fireEvent.click(purge);
    const confirmation = within(await screen.findByRole("dialog", { name: "删除凭证 LLM_API_KEY" }));
    expect(confirmation.getByText(/删除后无法恢复/)).toBeTruthy();
    fireEvent.click(confirmation.getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(server.requests.some((r) => r.path === purgePath)).toBe(false);

    fireEvent.click(purge);
    fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "删除凭证" }));
    await waitFor(() => expect(screen.queryAllByRole("button", { name: "删除凭证 LLM_API_KEY" })).toHaveLength(1));
    expect(server.requests.filter((r) => r.path === purgePath)).toMatchObject([{ method: "POST" }]);
    expect(screen.getAllByText("LLM_API_KEY")).toHaveLength(2);
    expect(screen.getByText("PRICE_API_TOKEN")).toBeTruthy();
  });

  it("keeps earlier credentials visible and retries a failed later page", async () => {
    const credentials = manyCredentials();
    let fail = true;
    const cursors: (string | null)[] = [];
    renderApp("/settings/credentials", { credentialPage: async (url) => {
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor) return json({ items: credentials.slice(0, 200), next_cursor: "next" });
      return fail ? problem(403, "permission_denied") : json({ items: credentials.slice(200), next_cursor: null });
    } });
    await screen.findByText("CRED_199");
    fireEvent.click(screen.getByRole("button", { name: "显示更多" }));
    const retry = await screen.findByRole("button", { name: "重试" });
    expect(screen.getByText("CRED_199")).toBeTruthy();
    expect(screen.queryByText("CRED_200")).toBeNull();
    fail = false;
    fireEvent.click(retry);
    expect(await screen.findByRole("button", { name: "更新 CRED_200" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
    expect(screen.queryByRole("button", { name: "显示更多" })).toBeNull();
    expect(cursors).toEqual([null, "next", "next"]);
  });

  it("lists credentials without values and updates through a fill-in request", async () => {
    const { server } = renderApp("/settings/credentials");
    await screen.findByText("LLM_API_KEY");
    expect(screen.getByText("1 个任务")).toBeTruthy();
    expect(screen.getByRole("img", { name: "即将过期" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "更新 LLM_API_KEY" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("替换当前内容")).toBeTruthy();
    const post = server.requests.find((r) => r.method === "POST")!;
    expect(post.path).toBe("/api/v1/credential-requests");
    expect(post.body).toMatchObject({ credential_id: CREDENTIAL.id, fields: [{ name: "LLM_API_KEY" }] });

    fireEvent.change(within(dialog).getByLabelText(/^LLM_API_KEY/, { selector: "input" }), { target: { value: "sk-rotated" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(server.requests.find((r) => r.path.endsWith("/values"))).toMatchObject({ path: "/api/v1/credential-requests/req2/values", body: { values: { LLM_API_KEY: "sk-rotated" } } });
    expect(server.requests.some((r) => r.method === "DELETE")).toBe(false);
  });

  it("withdraws an update request closed without saving", async () => {
    const { server } = renderApp("/settings/credentials");
    fireEvent.click(await screen.findByRole("button", { name: "更新 LLM_API_KEY" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() => expect(server.requests.some((r) => r.method === "DELETE" && r.path === "/api/v1/credential-requests/req2")).toBe(true));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(server.requests.some((r) => r.path.endsWith("/values"))).toBe(false);
  });
});

describe("login", () => {
  it("offers company sign-in when SSO is configured", async () => {
    renderApp("/login", { signedIn: false, methods: ["password", "oidc"] });
    expect(await screen.findByRole("button", { name: "企业账号登录" })).toBeTruthy();
    expect(screen.getByLabelText("用户名")).toBeTruthy();
  });

  it("leads with company sign-in and keeps a quiet admin entry when local password login is off", async () => {
    renderApp("/login", { signedIn: false, methods: ["oidc"], adminPasswordLogin: true });
    await screen.findByRole("button", { name: "企业账号登录" });
    expect(screen.queryByLabelText("用户名")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "管理员登录" }));
    expect(screen.getByLabelText("用户名")).toBeTruthy();
  });

  it("offers no password form at all when not even admins may use one", async () => {
    renderApp("/login", { signedIn: false, methods: ["oidc"], adminPasswordLogin: false });
    await screen.findByRole("button", { name: "企业账号登录" });
    expect(screen.queryByLabelText("用户名")).toBeNull();
    expect(screen.queryByRole("button", { name: "管理员登录" })).toBeNull();
  });

  it("explains an SSO failure sent back by the callback", async () => {
    renderApp("/login?error=access_denied", { signedIn: false, methods: ["password", "oidc"] });
    expect(await screen.findByText("企业账号登录没有完成，请重试")).toBeTruthy();
  });
});

describe("helpers", () => {
  it("splits a unified diff into coloured lines", () => {
    expect(diffLines("--- a/main.py\n+++ b/main.py\n@@ -1 +1 @@\n-old\n+new\n same\n")).toEqual([
      { kind: "hunk", text: "@@ -1 +1 @@" },
      { kind: "del", text: "-old" },
      { kind: "add", text: "+new" },
      { kind: "ctx", text: " same" },
    ]);
  });

  it("counts down the pairing code", () => {
    expect(countdown(272_000)).toBe("4:32");
    expect(countdown(-5)).toBe("0:00");
  });

  it("generates distinct readable passwords", () => {
    const a = generatePassword();
    expect(a).toMatch(/^[A-Za-z2-9]{4}-[A-Za-z2-9]{4}-[A-Za-z2-9]{4}$/);
    expect(generatePassword()).not.toBe(a);
  });

  it("routes console links inside the app and leaves the fill page to the server", () => {
    expect(consolePath("http://lerryn.test/runs/r1", "http://lerryn.test")).toBe("/runs/r1");
    expect(consolePath("http://lerryn.test/c/abc", "http://lerryn.test")).toBeNull();
    expect(consolePath("https://evil.test/runs/r1", "http://lerryn.test")).toBeNull();
  });
});

describe("runs board", () => {
  afterEach(() => vi.useRealTimers());

  const relative = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString();
  const summary = (n: number, name: string, status: Schemas["RunStatus"], queuedMin: number): Schemas["RunSummary"] => ({
    ...NEW_RUN,
    id: `0192f3a1-0000-7000-8000-0000000b${String(n).padStart(4, "0")}`,
    task: { ...TASK_REF, name },
    trigger: { ...NEW_RUN.trigger, kind: "manual" },
    status,
    queued_at: relative(queuedMin),
    started_at: status === "queued" ? null : relative(queuedMin + 1),
    finished_at: null,
    result_status: null,
    result_summary: null,
    current_stage: null,
  });

  it("groups runs since midnight two days ago and shows planned fires through tomorrow", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(2026, 2, 8, 12));
    const from = new Date(2026, 2, 6).toISOString();
    const today = new Date(2026, 2, 8).toISOString();
    const tomorrow = new Date(2026, 2, 9).toISOString();
    const to = new Date(2026, 2, 10).toISOString();
    const alreadyQueued = relative(10);
    const runs: Schemas["RunSummary"][] = [
      { ...summary(1, "queued-run", "queued", -1), trigger: { ...NEW_RUN.trigger, kind: "cron", trigger_id: "queued", scheduled_at: alreadyQueued } },
      summary(2, "preparing-run", "preparing", -2),
      summary(3, "running-run", "running", -3),
      { ...summary(4, "completed-run", "completed", -60), queued_at: from, started_at: from },
      summary(5, "failed-run", "failed", -120),
      summary(6, "cancelled-run", "cancelled", -180),
      { ...summary(7, "stale-run", "completed", -60), queued_at: new Date(Date.parse(from) - 1).toISOString() },
    ];
    const plan = (name: string, start: string, trigger_id = name): Schemas["CalendarItem"] => ({
      kind: "planned", task: { ...TASK_REF, name }, run_id: null, status: null, start, end: null, trigger_id,
    });
    const calendar = [
      plan("past-plan", relative(-10)),
      plan("stale-queued-plan", alreadyQueued, "queued"),
      plan("today-plan", relative(60)),
      plan("tomorrow-first", tomorrow),
      plan("tomorrow-last", new Date(Date.parse(to) - 1).toISOString()),
      plan("day-after-plan", to),
    ];
    const { server } = renderApp("/runs?view=board", { runs, calendar });
    const board = await screen.findByRole("main", { name: "运行看板" });
    const column = (name: string) => within(within(board).getByRole("region", { name }));
    const scheduled = column("已排期");
    const tomorrowCard = await scheduled.findByRole("link", { name: /tomorrow-first/ });
    expect(tomorrowCard.getAttribute("href")).toBe(`/tasks/${TASK_REF.id}`);
    expect(within(tomorrowCard).getByText("明天 00:00").getAttribute("datetime")).toBe(tomorrow);
    await waitFor(() => expect(scheduled.getAllByRole("link").map((a) => a.querySelector(".li-name")?.textContent)).toEqual([
      "queued-run", "today-plan", "tomorrow-first", "tomorrow-last",
    ]));
    expect(within(board).getByText("前天至明天")).toBeTruthy();
    expect(column("进行中").getAllByRole("link").map((a) => a.querySelector(".li-name")?.textContent)).toEqual(["preparing-run", "running-run"]);
    expect(column("已完成").getAllByRole("link").map((a) => a.querySelector(".li-name")?.textContent)).toEqual(["completed-run"]);
    expect(column("待处理").getAllByRole("link").map((a) => a.querySelector(".li-name")?.textContent)).toEqual(["failed-run", "cancelled-run"]);
    expect(within(board).queryByText("stale-run")).toBeNull();

    const calls = server.fetch.mock.calls.map(([req]) => new URL(req.url));
    const listCall = calls.find((u) => u.pathname === "/api/v1/runs");
    expect(listCall?.searchParams.get("exclude_archived_tasks")).toBe("true");
    expect(listCall?.searchParams.get("since")).toBe(from);
    expect(calls.filter((u) => u.pathname === "/api/v1/overview/calendar").map((u) => [u.searchParams.get("from"), u.searchParams.get("to")]))
      .toEqual([[today, tomorrow], [tomorrow, to]]);
  });

  it("carries a card to its new column when the run's status changes", async () => {
    const animate = vi.fn(() => ({ onfinish: null, oncancel: null, cancel() {} }) as unknown as Animation);
    Object.assign(HTMLElement.prototype, { animate, getAnimations: () => [] });
    const runs = [summary(1, "moving-run", "queued", -1), summary(2, "steady-run", "completed", -5)];
    const { queryClient } = renderApp("/runs?view=board", { runs });
    const board = await screen.findByRole("main", { name: "运行看板" });
    const column = (name: string) => within(within(board).getByRole("region", { name }));
    await waitFor(() => expect(column("已排期").getByText("moving-run")).toBeTruthy());
    expect(animate).not.toHaveBeenCalled();

    runs[0] = { ...runs[0]!, status: "running" };
    await act(() => queryClient.invalidateQueries());
    await waitFor(() => expect(column("进行中").getByText("moving-run")).toBeTruthy());
    expect(animate).toHaveBeenCalledTimes(1);
    const item = column("进行中").getByText("moving-run").closest("li")!;
    const flying = board.querySelector(".kb-flying-card");
    expect(animate.mock.contexts[0]).toBe(flying);
    expect(flying?.getAttribute("aria-hidden")).toBe("true");
    expect(item.classList.contains("kb-item--moving")).toBe(true);
    delete (HTMLElement.prototype as Partial<HTMLElement>).animate;
    delete (HTMLElement.prototype as Partial<HTMLElement>).getAnimations;
  });

  it("switches views by link, next to the title, and remembers the choice", async () => {
    localStorage.removeItem("lerryn.runs.view");
    const { router } = renderApp("/runs", { runs: [summary(1, "scheduled-run", "queued", -1)] });
    const user = userEvent.setup();
    await screen.findByRole("region", { name: "运行列表" });
    const views = () => within(screen.getByRole("navigation", { name: "视图" }));
    expect(views().getByRole("link", { name: "列表" }).getAttribute("aria-current")).toBe("page");
    await user.click(views().getByRole("link", { name: "看板" }));
    await screen.findByRole("main", { name: "运行看板" });
    expect(router.state.location.search).toEqual({ view: "board" });
    expect(views().getByRole("link", { name: "看板" }).getAttribute("aria-current")).toBe("page");

    // The rail's plain /runs comes back to the board.
    await act(() => router.navigate({ to: "/runs" }));
    await screen.findByRole("main", { name: "运行看板" });
    await user.click(views().getByRole("link", { name: "列表" }));
    await screen.findByRole("region", { name: "运行列表" });
    expect(router.state.location.search).toEqual({ view: "list" });
    await act(() => router.navigate({ to: "/runs" }));
    await screen.findByRole("region", { name: "运行列表" });
  });

  it("opens a floating run preview and switches sizes without changing the selected run", async () => {
    const { router } = renderApp("/runs?view=board", { runs: [{ ...summary(1, "failed-run", "failed", -5), id: FAILED_RUN.id }] });
    const user = userEvent.setup();
    const board = await screen.findByRole("main", { name: "运行看板" });
    await user.click(await within(board).findByRole("link", { name: /failed-run/ }));
    expect(router.state.location.pathname).toBe("/runs");
    expect(router.state.location.search).toEqual({ view: "board", run: FAILED_RUN.id });
    const preview = await screen.findByRole("complementary", { name: "运行预览" });
    expect(preview.getAttribute("data-mode")).toBe("compact");
    expect(await within(preview).findByRole("link", { name: /打开/ })).toBeTruthy();
    expect(within(board).getByRole("link", { name: /failed-run/ }).getAttribute("aria-current")).toBe("page");
    expect(within(preview).getByRole("link", { name: "关闭" }).getAttribute("aria-current")).toBeNull();
    const resize = within(preview).getByRole("button", { name: "向上展开" });
    await user.click(resize);
    expect(preview.getAttribute("data-mode")).toBe("expanded");
    expect(within(preview).getByRole("button", { name: "切换全屏" })).toBe(resize);
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: "运行预览" })).toBe(preview);
    expect(preview.getAttribute("data-mode")).toBe("fullscreen");
    expect(within(preview).getByRole("button", { name: "返回小窗" })).toBe(resize);
    await user.click(resize);
    expect(await screen.findByRole("complementary", { name: "运行预览" })).toBe(preview);
    expect(preview.getAttribute("data-mode")).toBe("compact");
    await user.keyboard("{Enter}{Enter}{Escape}");
    expect(preview.getAttribute("data-mode")).toBe("compact");
    expect(router.state.location.search).toEqual({ view: "board", run: FAILED_RUN.id });
    await user.click(within(preview).getByRole("link", { name: "关闭" }));
    await waitFor(() => expect(screen.queryByRole("complementary", { name: "运行预览" })).toBeNull());
    expect(router.state.location.search).toEqual({ view: "board" });
  });

  it("shows a loading indicator until the run preview is ready", async () => {
    let finish!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    renderApp(`/runs?view=board&run=${FAILED_RUN.id}`, { runDetail: () => pending });
    const preview = within(await screen.findByRole("complementary", { name: "运行预览" }));
    expect(preview.getByRole("status").textContent).toBe("正在加载");
    expect(preview.queryByRole("link", { name: "打开" })).toBeNull();

    finish(json(FAILED_RUN));
    expect(await preview.findByRole("link", { name: "打开" })).toBeTruthy();
    expect(preview.queryByText("正在加载")).toBeNull();
  });

  it("shows 50 cards per column and appends the next batch only to the chosen column", async () => {
    const runs = [
      ...Array.from({ length: 103 }, (_, n) => summary(n + 1, `done-${n + 1}`, "completed", -n - 1)),
      ...Array.from({ length: 49 }, (_, n) => summary(n + 104, `queued-${n + 1}`, "queued", -n - 104)),
      ...Array.from({ length: 11 }, (_, n) => summary(n + 153, `failed-${n + 1}`, "failed", -n - 153)),
    ];
    const date = new Date();
    const calendar = [0, 1].map((hour): Schemas["CalendarItem"] => ({
      kind: "planned",
      task: { ...TASK_REF, name: `plan-${hour + 1}` },
      run_id: null,
      status: null,
      start: new Date(date.getFullYear(), date.getMonth(), date.getDate() + 1, hour).toISOString(),
      end: null,
      trigger_id: `trigger-${hour + 1}`,
    }));
    renderApp("/runs?view=board", { runs, calendar });
    const user = userEvent.setup();
    const done = within(await screen.findByRole("region", { name: "已完成" }));
    const scheduled = within(screen.getByRole("region", { name: "已排期" }));
    const attention = within(screen.getByRole("region", { name: "待处理" }));
    expect(await attention.findByText("failed-11")).toBeTruthy();
    expect(attention.getAllByRole("link")).toHaveLength(11);
    expect(attention.queryByRole("button")).toBeNull();
    expect(done.getAllByRole("link")).toHaveLength(50);
    expect(scheduled.getAllByRole("link")).toHaveLength(50);
    expect(scheduled.queryByText("plan-2")).toBeNull();
    expect(done.getByText("103")).toBeTruthy();
    await user.click(done.getByRole("button", { name: "再显示 50 条" }));
    expect(done.getAllByRole("link")).toHaveLength(100);
    expect(document.activeElement).toBe(done.getByRole("link", { name: /done-51/ }));
    expect(scheduled.getAllByRole("link")).toHaveLength(50);
    await user.click(done.getByRole("button", { name: "再显示 3 条" }));
    expect(done.getAllByRole("link")).toHaveLength(103);
    expect(done.queryByRole("button")).toBeNull();
    await user.click(scheduled.getByRole("button", { name: "再显示 1 条" }));
    expect(scheduled.getAllByRole("link")).toHaveLength(51);
    expect(document.activeElement).toBe(scheduled.getByRole("link", { name: /plan-2/ }));
  });

  it("leads back from a run's page to the runs view last used", async () => {
    localStorage.setItem("lerryn.runs.view", "board");
    const { router } = renderApp(`/runs/${FAILED_RUN.id}`);
    const user = userEvent.setup();
    const crumbs = within(await screen.findByRole("navigation", { name: "位置" }));
    await user.click(crumbs.getByRole("link", { name: "运行" }));
    expect(router.state.location.pathname).toBe("/runs");
    expect(router.state.location.search).toEqual({ view: "board", run: FAILED_RUN.id });
    localStorage.removeItem("lerryn.runs.view");
  });
});
