import { createLerrynClient, type Schemas } from "@lerryn/api";
import { ApiProvider, I18nProvider } from "@lerryn/core";
import { LerrynThemeProvider } from "@lerryn/ui-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { createAppRouter } from "./router";

const ME = {
  id: "0192f3a1-0000-7000-8000-000000000001",
  username: "zhanglin",
  display_name: "张林",
  email: null,
  is_admin: true,
  personal_project_id: "0192f3a1-0000-7000-8000-000000000002",
  created_at: "2026-11-01T00:00:00Z",
  locale: null,
  timezone: null,
  has_password: true,
  identities: [],
};
const DISCOVERY = {
  server: { version: "dev", commit: "none", build_date: null },
  api: { version: "v1", spec_version: "0.1.0" },
  capabilities: { login_methods: ["password"], registration_open: true, network_policies: ["full"], require_test_before_activate: true },
  runtimes: [],
  skills: { version: "dev", items: [] },
  principal: null,
  links: { console: "http://lerryn.test/" },
};
const RUN = {
  id: "0192f3a1-0000-7000-8000-0000000000r1",
  task_id: "0192f3a1-0000-7000-8000-0000000000t1",
  project_id: ME.personal_project_id,
  version: 7,
  status: "failed",
  test: false,
  trigger: { kind: "cron", trigger_id: null, scheduled_at: "2026-11-02T00:00:00Z", user_id: null, client: null },
  params: {},
  attempt: 1,
  max_attempts: 1,
  retry_of: null,
  rerun_of: null,
  skip_reason: null,
  claimed_at: "2026-11-02T00:00:01Z",
  cancelled_by: null,
  runner: { id: "embedded", name: "runner-01" },
  environment: "env_9c1e4a",
  log_lines: 3,
  dropped_log_lines: 0,
  queued_at: "2026-11-02T00:00:00Z",
  started_at: "2026-11-02T00:00:03Z",
  finished_at: "2026-11-02T00:00:55.300Z",
  exit_code: 1,
  error: { code: "script_error", stage: "llm", message: "429 Too Many Requests，重试 3 次后退出" },
  stages: [
    { id: "fetch", name: "获取输入", declared: true, after: [], status: "done", started_at: "2026-11-02T00:00:03Z", finished_at: "2026-11-02T00:00:15.4Z", message: null },
    { id: "llm", name: "调用模型", declared: true, after: [], status: "failed", started_at: "2026-11-02T00:00:15.4Z", finished_at: "2026-11-02T00:00:53.2Z", message: null },
    { id: "save", name: "保存结果", declared: true, after: [], status: "pending", started_at: null, finished_at: null, message: null },
  ],
  result: { status: "failed", summary: "第 2 批没有生成摘要", data: { 新线索: 42 } },
  cancel_requested: false,
  links: { console: "http://lerryn.test/runs/0192f3a1-0000-7000-8000-0000000000r1" },
};
const TASK = {
  id: RUN.task_id,
  project_id: ME.personal_project_id,
  name: "weekday-digest",
  description: "工作日早上汇总新线索",
  latest_version: {
    seq: 7,
    created_at: "2026-11-01T00:00:00Z",
    manifest: { runtime: { language: "python", version: "3.12" }, entrypoint: ["python", "main.py"] },
    files: [{ path: "main.py", size: 2048, sha256: "ab", executable: false }],
  },
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  links: { console: "http://lerryn.test/tasks/" + RUN.task_id },
};
const LOG = [
  { seq: 1, ts: "2026-11-02T00:00:15.4Z", stream: "system", stage: "llm", text: "阶段开始：调用模型" },
  { seq: 2, ts: "2026-11-02T00:00:18.2Z", stream: "stderr", stage: "llm", text: "HTTP 429 Too Many Requests" },
  { seq: 3, ts: "2026-11-02T00:00:53.0Z", stream: "system", stage: null, text: "进程退出，退出码 1" },
];
const LONG_ARGS = `{"query":"${"线索".repeat(60)}"}`;
const EVENTS = [
  {
    seq: 1,
    ts: "2026-11-02T00:00:16Z",
    kind: "tool.call",
    stage: "llm",
    message: "crm-mcp search_leads",
    // A record never carries what the tool returned; an unexpected field must not reach the page either.
    data: { tool: "crm-mcp", method: "search_leads", ok: true, duration_ms: 38, args_summary: '{"token":"[REDACTED]","day":"2026-11-02"}', error: null, result: "RETURNED-CONTENT" },
  },
  {
    seq: 2,
    ts: "2026-11-02T00:00:17Z",
    kind: "tool.call",
    stage: "llm",
    message: "crm-mcp get_lead",
    data: { tool: "crm-mcp", method: "get_lead", ok: false, duration_ms: 2400, args_summary: LONG_ARGS, error: "HTTP 503" },
  },
];
/** The same run, with stages that wait for each other: fetch, then llm and save side by side. */
const GRAPH_RUN = {
  ...RUN,
  id: "0192f3a1-0000-7000-8000-0000000000r3",
  stages: RUN.stages.map((s) => (s.id === "fetch" ? s : { ...s, after: ["fetch"] })),
};
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = (status: number, code: string) =>
  new Response(JSON.stringify({ type: `urn:lerryn:error:${code}`, title: code, status, code, request_id: "req_1" }), {
    status,
    headers: { "Content-Type": "application/problem+json" },
  });

interface FakeOptions {
  signedIn?: boolean;
  tokenSubmit?: (body: Schemas["CreateTokenRequest"]) => Promise<Response>;
}

/** A small fake of the API the console uses today. */
function fakeServer(opts: FakeOptions = {}) {
  let signedIn = opts.signedIn ?? false;
  let currentUser = ME;
  const tokenLists = new Map<string, unknown[]>();
  const tokensForUser = () => {
    let tokens = tokenLists.get(currentUser.id);
    if (!tokens) {
      tokens = [];
      tokenLists.set(currentUser.id, tokens);
    }
    return tokens;
  };
  const fetch = vi.fn(async (req: Request) => {
    const { pathname } = new URL(req.url);
    const key = `${req.method} ${pathname}`;
    switch (key) {
      case "GET /api/v1/discover":
        return json(DISCOVERY);
      case "GET /api/v1/me":
        return signedIn ? json(currentUser) : problem(401, "unauthenticated");
      case "POST /api/v1/auth/login": {
        const body = (await req.json()) as { username: string; password: string };
        if (body.password !== "correct horse") return problem(401, "unauthenticated");
        signedIn = true;
        currentUser = body.username === "bob" ? { ...ME, id: "bob", username: "bob", display_name: "Bob" } : ME;
        return json(currentUser);
      }
      case "POST /api/v1/auth/logout":
        signedIn = false;
        return json({});
      case "GET /api/v1/me/tokens":
        return json({ items: tokensForUser(), next_cursor: null });
      case "POST /api/v1/me/tokens": {
        const body = (await req.json()) as Schemas["CreateTokenRequest"];
        const tokens = tokensForUser();
        if (opts.tokenSubmit) {
          const response = await opts.tokenSubmit(body);
          if (response.ok) tokens.unshift(((await response.clone().json()) as Schemas["CreatedToken"]).access_token);
          return response;
        }
        const token = {
          id: "0192f3a1-0000-7000-8000-0000000000aa",
          name: body.name,
          prefix: "wl_pat_k7q2",
          scopes: body.scopes,
          created_at: new Date().toISOString(),
          expires_at: null,
          last_used_at: null,
          revoked_at: null,
        };
        tokens.unshift(token);
        return json({ token: "wl_pat_k7q2SECRETSECRETSECRET", access_token: token }, 201);
      }
      case `GET /api/v1/runs/${RUN.id}`:
        return json(RUN);
      case `GET /api/v1/runs/${GRAPH_RUN.id}`:
        return json(GRAPH_RUN);
      case `GET /api/v1/runs/${GRAPH_RUN.id}/logs`:
        return json({ lines: LOG, next_cursor: "bDQ", complete: true, dropped_lines: 0 });
      case `GET /api/v1/runs/${GRAPH_RUN.id}/events`:
        return json({ events: [], next_cursor: "ZTM", complete: true });
      case `GET /api/v1/tasks/${RUN.task_id}`:
        return json(TASK);
      case `GET /api/v1/runs/${RUN.id}/logs`:
        return json({ lines: LOG, next_cursor: "bDQ", complete: true, dropped_lines: 0 });
      case `GET /api/v1/runs/${RUN.id}/events`:
        return json({ events: EVENTS, next_cursor: "ZTM", complete: true });
      default:
        return problem(404, "not_found");
    }
  });
  return { fetch };
}

function renderApp(path: string, opts: FakeOptions = {}) {
  const server = fakeServer(opts);
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: server.fetch });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createAppRouter({ api, queryClient });
  router.update({ history: createMemoryHistory({ initialEntries: [path] }) } as never);
  render(
    <LerrynThemeProvider>
      <I18nProvider locale="zh-CN">
        <QueryClientProvider client={queryClient}>
          <ApiProvider client={api}>
            <RouterProvider router={router} />
          </ApiProvider>
        </QueryClientProvider>
      </I18nProvider>
    </LerrynThemeProvider>,
  );
  return { router, server };
}

const createdToken = (body: Schemas["CreateTokenRequest"]): Schemas["CreatedToken"] => ({
  token: "wl_pat_FIRST_SECRET",
  access_token: {
    id: "token-first", name: body.name, scopes: body.scopes, prefix: "wl_pat_FIRST",
    created_at: "2026-11-03T00:00:00Z", expires_at: null, revoked_at: null, last_used_at: null,
  },
});

describe("access token form", () => {
  it("submits once while Enter is repeated and preserves the first successful token", async () => {
    const submitted: Schemas["CreateTokenRequest"][] = [];
    let finish: ((response: Response) => void) | undefined;
    renderApp("/settings/tokens", { signedIn: true, tokenSubmit: async (body) => {
      submitted.push(body);
      return new Promise<Response>((resolve) => { finish = resolve; });
    } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "新建" }));
    const dialog = await screen.findByRole("dialog");
    const name = within(dialog).getByRole("textbox", { name: "名称" }) as HTMLInputElement;
    await user.type(name, "workstation{Enter}");
    await waitFor(() => expect(submitted).toHaveLength(1));
    expect(within(dialog).getByRole("button", { name: "创建" }).getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(name);
    await user.keyboard("{Enter}{Enter}");
    await act(async () => { fireEvent.submit(name.form!); });
    expect(submitted).toHaveLength(1);
    expect(name.form!.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true);
    await act(async () => finish!(json(createdToken(submitted[0]!), 201)));
    expect((await screen.findByTestId("created-token")).textContent).toBe("wl_pat_FIRST_SECRET");
    await user.click(screen.getByRole("button", { name: "复制令牌" }));
    expect(await navigator.clipboard.readText()).toBe("wl_pat_FIRST_SECRET");
    await user.click(screen.getByRole("button", { name: "完成" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.body.textContent).not.toContain("FIRST_SECRET");
    await user.click(screen.getByRole("button", { name: "新建" }));
    expect((await screen.findByRole("textbox", { name: "名称" }) as HTMLInputElement).value).toBe("");
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(submitted).toHaveLength(1);
  });

  it("retains the response when cancellation or Escape is attempted during creation", async () => {
    let finish: ((response: Response) => void) | undefined;
    let submitted: Schemas["CreateTokenRequest"] | undefined;
    renderApp("/settings/tokens", { signedIn: true, tokenSubmit: async (body) => {
      submitted = body;
      return new Promise<Response>((resolve) => { finish = resolve; });
    } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "新建" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox", { name: "名称" }), "workstation{Enter}");
    await waitFor(() => expect(finish).toBeDefined());
    const cancel = within(dialog).getByRole("button", { name: "取消" }) as HTMLButtonElement;
    expect(cancel.disabled).toBe(true);
    await user.click(cancel);
    await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBe(dialog);
    await act(async () => finish!(json(createdToken(submitted!), 201)));
    expect((await screen.findByTestId("created-token")).textContent).toBe("wl_pat_FIRST_SECRET");
  });

  it("allows Enter to retry in place after a failed creation", async () => {
    const submitted: Schemas["CreateTokenRequest"][] = [];
    let finish: ((response: Response) => void) | undefined;
    renderApp("/settings/tokens", { signedIn: true, tokenSubmit: async (body) => {
      submitted.push(body);
      if (submitted.length === 1) throw new TypeError("offline");
      return new Promise<Response>((resolve) => { finish = resolve; });
    } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "新建" }));
    const dialog = await screen.findByRole("dialog");
    await user.type(within(dialog).getByRole("textbox", { name: "名称" }), "workstation{Enter}");
    expect((await within(dialog).findByRole("alert")).textContent).toContain("请求没有成功");
    expect((within(dialog).getByRole("button", { name: "取消" }) as HTMLButtonElement).disabled).toBe(false);
    await user.keyboard("{Enter}");
    await waitFor(() => expect(submitted).toHaveLength(2));
    expect(within(dialog).queryByRole("alert")).toBeNull();
    await user.keyboard("{Enter}");
    expect(submitted).toHaveLength(2);
    await act(async () => finish!(json(createdToken(submitted[1]!), 201)));
    expect((await screen.findByTestId("created-token")).textContent).toBe("wl_pat_FIRST_SECRET");
  });

  it("does not show a previous session's pending token after logout and another login", async () => {
    let finish: ((response: Response) => void) | undefined;
    let submitted: Schemas["CreateTokenRequest"] | undefined;
    const { router } = renderApp("/settings/tokens", { signedIn: true, tokenSubmit: async (body) => {
      submitted = body;
      return new Promise<Response>((resolve) => { finish = resolve; });
    } });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "新建" }));
    await user.type(await screen.findByRole("textbox", { name: "名称" }), "workstation{Enter}");
    await waitFor(() => expect(finish).toBeDefined());
    await act(() => router.navigate({ to: "/settings/account" }));
    await user.click(await screen.findByRole("button", { name: "退出登录" }));
    await screen.findByRole("heading", { name: "登录" });
    await user.type(screen.getByLabelText("用户名"), "bob");
    await user.type(screen.getByLabelText("密码", { selector: "input" }), "correct horse");
    await user.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    await act(() => router.navigate({ to: "/settings/tokens" }));
    await screen.findByText("尚无访问令牌");
    await act(async () => finish!(json(createdToken(submitted!), 201)));
    await user.click(screen.getByRole("button", { name: "新建" }));
    await screen.findByRole("dialog");
    expect(screen.queryByTestId("created-token")).toBeNull();
    expect(screen.queryByText("workstation")).toBeNull();
    expect(document.body.textContent).not.toContain("FIRST_SECRET");
  });
});

describe("console app", () => {
  it("sends a signed-out visitor to the login page and back after signing in", async () => {
    const { router } = renderApp("/settings/tokens");
    await screen.findByRole("heading", { name: "登录" });
    expect(router.state.location.pathname).toBe("/login");
    expect(router.state.location.search).toEqual({ redirect: "/settings/tokens" });

    fireEvent.change(await screen.findByLabelText("用户名"), { target: { value: "zhanglin" } });
    fireEvent.change(screen.getByLabelText("密码", { selector: "input" }), { target: { value: "wrong" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await screen.findByText("用户名或密码不正确");

    fireEvent.change(screen.getByLabelText("密码", { selector: "input" }), { target: { value: "correct horse" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await screen.findByRole("heading", { name: "访问令牌" });
    expect(router.state.location.pathname).toBe("/settings/tokens");
  });

  it("creates an access token and shows it only once", async () => {
    const { router } = renderApp("/login");
    await screen.findByRole("heading", { name: "登录" });
    fireEvent.change(await screen.findByLabelText("用户名"), { target: { value: "zhanglin" } });
    fireEvent.change(screen.getByLabelText("密码", { selector: "input" }), { target: { value: "correct horse" } });
    fireEvent.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));

    await router.navigate({ to: "/settings/tokens" });
    await screen.findByText("尚无访问令牌");
    fireEvent.click(screen.getByRole("button", { name: "新建" }));
    fireEvent.change(await screen.findByLabelText("名称"), { target: { value: "Claude Code · 笔记本" } });
    fireEvent.click(screen.getByRole("button", { name: "创建" }));

    expect((await screen.findByTestId("created-token")).textContent).toBe("wl_pat_k7q2SECRETSECRETSECRET");
    fireEvent.click(screen.getByRole("button", { name: "完成" }));
    await waitFor(() => expect(screen.queryByTestId("created-token")).toBeNull());
    // The list shows only the prefix, never the token.
    await screen.findByText("Claude Code · 笔记本");
    expect(document.body.textContent).not.toContain("SECRET");
  });

  it("shows a run: status, cause, stages, reported result and logs", async () => {
    renderApp(`/runs/${RUN.id}`, { signedIn: true });
    await screen.findByRole("heading", { name: "执行失败" });
    expect(screen.getByText("52.3s")).toBeTruthy();
    expect(screen.getByText(/重试 3 次后退出/)).toBeTruthy();
    // The breadcrumb names the task once it loads.
    await screen.findByRole("link", { name: /weekday-digest/ });
    expect(screen.getByRole("button", { name: /调用模型 .*，失败/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /保存结果，未开始/ })).toBeTruthy();
    // The reported result sits apart from the run status.
    expect(screen.getByText("任务上报")).toBeTruthy();
    expect(screen.getByText("未完成")).toBeTruthy();
    expect(await screen.findByText("HTTP 429 Too Many Requests")).toBeTruthy();
  });

  it("draws stages with dependencies as a graph, with the list for narrow boards", async () => {
    renderApp(`/runs/${GRAPH_RUN.id}`, { signedIn: true });
    await screen.findByRole("heading", { name: "执行失败" });
    const graph = screen.getByRole("group", { name: "阶段" });
    expect(graph.querySelectorAll(".snode")).toHaveLength(3);
    expect(graph.querySelectorAll(".sedge")).toHaveLength(2);
    expect(within(graph).getByRole("button", { name: /调用模型，失败.*在 获取输入 之后/ })).toBeTruthy();
    const list = screen.getByRole("list", { name: "阶段" });
    expect(within(list).getByText("未开始 · 在 获取输入 之后")).toBeTruthy();
    // The duration bar is for stages without dependencies.
    expect(document.querySelector(".flutes")).toBeNull();
  });

  it("lists the run's recorded tool calls in the tools chip, without what they returned", async () => {
    renderApp(`/runs/${RUN.id}`, { signedIn: true });
    const chip = await screen.findByRole("button", { name: /工具 2/ });
    expect(chip.querySelector(".dot--bad")).toBeTruthy();
    const calls = screen.getByRole("group", { name: "工具调用" });
    const rows = within(calls).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]!).getByText("crm-mcp · search_leads")).toBeTruthy();
    expect(within(rows[0]!).getByText("38ms")).toBeTruthy();
    expect(within(rows[0]!).getByText('{"token":"[REDACTED]","day":"2026-11-02"}')).toBeTruthy();
    expect(within(rows[0]!).getByText(/调用模型/)).toBeTruthy();
    expect(within(rows[0]!).getByRole("img", { name: "成功" })).toBeTruthy();
    expect(within(rows[1]!).getByText("2.4s")).toBeTruthy();
    expect(within(rows[1]!).getByText("HTTP 503")).toBeTruthy();
    expect(within(rows[1]!).getByRole("img", { name: "失败" })).toBeTruthy();
    // Long arguments are cut in the row and kept whole in the tooltip.
    const args = within(rows[1]!).getByTitle(LONG_ARGS);
    expect(args.textContent!.length).toBeLessThan(LONG_ARGS.length);
    expect(args.textContent!.endsWith("…")).toBe(true);
    expect(within(calls).getByText("失败 1")).toBeTruthy();
    expect(document.body.textContent).not.toContain("RETURNED-CONTENT");
  });

  it("answers unknown runs with not found", async () => {
    renderApp("/runs/0192f3a1-0000-7000-8000-0000000000zz", { signedIn: true });
    await screen.findByText("找不到");
  });
});
