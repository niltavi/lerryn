import { createLerrynClient, type Schemas } from "@lerryn/api";
import { ApiProvider, I18nProvider } from "@lerryn/core";
import { LerrynThemeProvider } from "@lerryn/ui-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { createAppRouter } from "./router";

type CreateToken = Schemas["CreateRunnerTokenRequest"];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const token = () => json({ id: "runner-token-first", token: "wl_rrt_fixture_first", expires_at: "2099-11-04T00:00:00Z" }, 201);
const clients: QueryClient[] = [];

afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
});

function renderRunnerTokens(post: (body: CreateToken) => Promise<Response>) {
  const submitted: CreateToken[] = [];
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: async (req) => {
    const path = new URL(req.url).pathname;
    if (path === "/api/v1/me") return json({
      id: "admin", username: "admin", display_name: "Admin", is_admin: true, personal_project_id: "project",
      email: null, identities: [], has_password: true, locale: null, timezone: null, created_at: "2026-11-03T00:00:00Z",
    });
    if (path === "/api/v1/discover") return json({ links: { console: "http://lerryn.test" }, capabilities: {} });
    if (path === "/api/v1/admin/runner-tokens" && req.method === "POST") {
      const body = await req.json() as CreateToken;
      submitted.push(body);
      return post(body);
    }
    return json({ items: [], next_cursor: null });
  } });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(queryClient);
  const router = createAppRouter({ api, queryClient });
  router.update({ context: { api, queryClient }, history: createMemoryHistory({ initialEntries: ["/admin/runners"] }) });
  render(
    <LerrynThemeProvider><I18nProvider locale="zh-CN"><QueryClientProvider client={queryClient}><ApiProvider client={api}>
      <RouterProvider router={router} />
    </ApiProvider></QueryClientProvider></I18nProvider></LerrynThemeProvider>,
  );
  return { submitted };
}

async function openTokenDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("button", { name: "注册令牌" }));
  return screen.findByRole("dialog", { name: "新建注册令牌" });
}

describe("runner registration token form", () => {
  it("submits once while Enter is repeated and clears the copied token after completion", async () => {
    let finish: ((response: Response) => void) | undefined;
    const { submitted } = renderRunnerTokens(async () => new Promise<Response>((resolve) => { finish = resolve; }));
    const user = userEvent.setup();
    const dialog = await openTokenDialog(user);
    const name = within(dialog).getByRole("textbox", { name: "备注" }) as HTMLInputElement;
    await user.type(name, "rack-1{Enter}");
    await waitFor(() => expect(submitted).toHaveLength(1));
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "创建" }).getAttribute("aria-disabled")).toBe("true"));
    await user.keyboard("{Enter}{Enter}");
    fireEvent.submit(name.form!);
    expect(submitted).toEqual([{ name: "rack-1", expires_in_hours: 24 }]);
    await act(async () => { finish!(token()); });
    expect((await screen.findByTestId("runner-token")).textContent).toBe("wl_rrt_fixture_first");
    await user.click(screen.getByRole("button", { name: "复制令牌" }));
    expect(await navigator.clipboard.readText()).toBe("wl_rrt_fixture_first");
    await user.click(screen.getByRole("button", { name: "完成" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.body.textContent).not.toContain("wl_rrt_fixture_first");
    expect(JSON.stringify(localStorage)).not.toContain("wl_rrt_fixture_first");
    const reopened = await openTokenDialog(user);
    expect((within(reopened).getByRole("textbox", { name: "备注" }) as HTMLInputElement).value).toBe("");
    expect(within(reopened).getByRole("button", { name: "24 小时" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.queryByTestId("runner-token")).toBeNull();
  });

  it.each(["cancel", "Escape"])("retains the first response when %s is attempted during creation", async (action) => {
    let finish: ((response: Response) => void) | undefined;
    const { submitted } = renderRunnerTokens(async () => new Promise<Response>((resolve) => { finish = resolve; }));
    const user = userEvent.setup();
    const dialog = await openTokenDialog(user);
    await user.type(within(dialog).getByRole("textbox", { name: "备注" }), "rack-1{Enter}");
    await waitFor(() => expect(finish).toBeDefined());
    if (action === "cancel") await user.click(within(dialog).getByRole("button", { name: "取消" }));
    else await user.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBe(dialog);
    await act(async () => { finish!(token()); });
    expect((await screen.findByTestId("runner-token")).textContent).toBe("wl_rrt_fixture_first");
    expect(submitted).toHaveLength(1);
  });

  it("allows a failed submission to be retried with Enter", async () => {
    let attempts = 0;
    const { submitted } = renderRunnerTokens(async () => {
      if (attempts++ === 0) throw new TypeError("offline");
      return token();
    });
    const user = userEvent.setup();
    const dialog = await openTokenDialog(user);
    const name = within(dialog).getByRole("textbox", { name: "备注" });
    await user.type(name, "rack-1{Enter}");
    expect((await within(dialog).findByRole("alert")).textContent).toContain("请求没有成功");
    await user.click(name);
    await user.keyboard("{Enter}");
    expect((await screen.findByTestId("runner-token")).textContent).toBe("wl_rrt_fixture_first");
    expect(submitted).toEqual([
      { name: "rack-1", expires_in_hours: 24 },
      { name: "rack-1", expires_in_hours: 24 },
    ]);
  });

  it("keeps the optional note and lets the user close a failed request", async () => {
    const { submitted } = renderRunnerTokens(async () => { throw new TypeError("offline"); });
    const user = userEvent.setup();
    const dialog = await openTokenDialog(user);
    await user.click(within(dialog).getByRole("button", { name: "创建" }));
    await within(dialog).findByRole("alert");
    expect(submitted).toEqual([{ expires_in_hours: 24 }]);
    await user.click(within(dialog).getByRole("button", { name: "取消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const reopened = await openTokenDialog(user);
    expect(within(reopened).queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("runner-token")).toBeNull();
  });
});
