import { createLerrynClient, type Schemas } from "@lerryn/api";
import { ApiProvider, I18nProvider } from "@lerryn/core";
import { LerrynThemeProvider } from "@lerryn/ui-web";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { createAppRouter } from "./router";

type FillRequest = Schemas["CredentialRequest"];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const problem = () => json({ type: "urn:lerryn:error:permission_denied", title: "permission_denied", status: 403, code: "permission_denied", request_id: "request-page" }, 403);
const request = (i: number): FillRequest => ({
  id: `request-${i}`, name: `REQUEST_${String(i).padStart(3, "0")}`, purpose: "Read the API", status: "pending",
  url: `http://lerryn.test/c/request-${i}`,
  task: { id: `task-${i}`, name: `task-${i}`, project_id: "project" }, credential_id: null, client: null,
  fields: [{ name: "API_KEY", label: "Key", description: "", secret: true, multiline: false, optional: false }],
  created_at: "2026-11-03T00:00:00Z", expires_at: "2099-11-04T00:00:00Z", fulfilled_at: null,
});
const credential = (i: number): Schemas["Credential"] => ({
  id: `credential-${i}`, name: `CRED_${String(i).padStart(3, "0")}`, purpose: "API access", fields: ["API_KEY"],
  status: "active", version: 1, expires_at: null, revoked_at: null, last_used_at: null, grants: [],
  created_at: "2026-11-03T00:00:00Z", updated_at: "2026-11-03T00:00:00Z",
});

const clients: QueryClient[] = [];
afterEach(() => {
  cleanup();
  for (const client of clients.splice(0)) client.clear();
});

function renderRequests(count: number, opts: { fail?: (cursor: string | null) => boolean; credentialCount?: number } = {}) {
  const requests = Array.from({ length: count }, (_, i) => request(i));
  const credentials = Array.from({ length: opts.credentialCount ?? 0 }, (_, i) => credential(i));
  const cursors: (string | null)[] = [];
  const filled: string[] = [];
  const api = createLerrynClient({ baseUrl: "http://lerryn.test", fetch: async (req) => {
    const url = new URL(req.url);
    if (url.pathname === "/api/v1/me") return json({
      id: "alice", username: "alice", display_name: "Alice", is_admin: false, personal_project_id: "project",
      email: null, identities: [], has_password: true, locale: null, timezone: null, created_at: "2026-11-03T00:00:00Z",
    });
    if (url.pathname === "/api/v1/discover") return json({ links: { console: "http://lerryn.test" }, capabilities: {} });
    if (url.pathname === "/api/v1/credential-requests") {
      expect(url.searchParams.get("status")).toBe("pending");
      expect(url.searchParams.get("limit")).toBe("200");
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      if (opts.fail?.(cursor)) return problem();
      const pending = requests.filter((r) => r.status === "pending");
      const offset = Number(cursor ?? 0);
      return json({ items: pending.slice(offset, offset + 200), next_cursor: offset + 200 < pending.length ? String(offset + 200) : null });
    }
    const detail = url.pathname.match(/^\/api\/v1\/credential-requests\/request-(\d+)(\/values)?$/);
    if (detail) {
      const item = requests[Number(detail[1])]!;
      if (req.method === "POST" && detail[2]) {
        filled.push(item.id);
        Object.assign(item, { status: "fulfilled", credential_id: "created-credential", fulfilled_at: "2026-11-04T00:00:00Z" });
      }
      return json(item);
    }
    if (url.pathname === "/api/v1/credentials") {
      const offset = Number(url.searchParams.get("cursor") ?? 0);
      return json({ items: credentials.slice(offset, offset + 200), next_cursor: offset + 200 < credentials.length ? String(offset + 200) : null });
    }
    return json({ items: [], next_cursor: null });
  } });
  const queryClient = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
  clients.push(queryClient);
  const router = createAppRouter({ api, queryClient });
  router.update({ context: { api, queryClient }, history: createMemoryHistory({ initialEntries: ["/settings/credentials"] }) });
  render(
    <LerrynThemeProvider><I18nProvider locale="zh-CN"><QueryClientProvider client={queryClient}><ApiProvider client={api}>
      <RouterProvider router={router} />
    </ApiProvider></QueryClientProvider></I18nProvider></LerrynThemeProvider>,
  );
  return { cursors, filled };
}

const rail = () => within(screen.getByRole("navigation", { name: "主导航" }));

describe("pending credential requests in settings", () => {
  it("pages requests on demand, marks incomplete counts and opens the oldest request", async () => {
    const { cursors, filled } = renderRequests(401);
    await screen.findByText("REQUEST_199");
    const pending = screen.getByRole("region", { name: "待填写" });
    expect(within(pending).getAllByRole("listitem")).toHaveLength(200);
    expect(cursors).toEqual([null]);
    expect(screen.queryByText("REQUEST_200")).toBeNull();
    expect(screen.getByText("200+ 待填")).toBeTruthy();
    expect(rail().getByRole("link", { name: "密钥，200+ 个凭证待填写" })).toBeTruthy();
    fireEvent.click(within(pending).getByRole("button", { name: "显示更多" }));
    await screen.findByText("REQUEST_399");
    expect(screen.getByText("400+ 待填")).toBeTruthy();
    fireEvent.click(within(pending).getByRole("button", { name: "显示更多" }));
    const oldest = await screen.findByText("REQUEST_400");
    expect(screen.getByText("401 待填")).toBeTruthy();
    expect(rail().getByRole("link", { name: "密钥，401 个凭证待填写" })).toBeTruthy();
    expect(within(pending).queryByRole("button", { name: "显示更多" })).toBeNull();
    expect(cursors).toEqual([null, "200", "400"]);
    fireEvent.click(oldest.closest("button")!);
    const dialog = await screen.findByRole("dialog", { name: "task-400 需要凭证" });
    const user = userEvent.setup();
    await user.type(within(dialog).getByLabelText(/^API_KEY · Key/, { selector: "input" }), "new-api-value");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(screen.queryByText("REQUEST_400")).toBeNull());
    expect(filled).toEqual(["request-400"]);
    expect(screen.getByText("400 待填")).toBeTruthy();
  });

  it("retains earlier requests after a later page fails and retries that page", async () => {
    let fail = true;
    const { cursors } = renderRequests(201, { fail: (cursor) => !!cursor && fail });
    await screen.findByText("REQUEST_199");
    const pending = screen.getByRole("region", { name: "待填写" });
    fireEvent.click(within(pending).getByRole("button", { name: "显示更多" }));
    const retry = await within(pending).findByRole("button", { name: "重试" });
    expect(within(pending).getAllByRole("listitem")).toHaveLength(200);
    expect(screen.queryByText("REQUEST_200")).toBeNull();
    expect(screen.getByText("200+ 待填")).toBeTruthy();
    fail = false;
    fireEvent.click(retry);
    await screen.findByText("REQUEST_200");
    expect(screen.getByText("201 待填")).toBeTruthy();
    expect(within(pending).queryByRole("button", { name: "重试" })).toBeNull();
    expect(cursors).toEqual([null, "200", "200"]);
  });

  it("shows a request-load failure with retry instead of an empty credentials message", async () => {
    let fail = true;
    renderRequests(1, { fail: () => fail });
    const pending = await screen.findByRole("region", { name: "待填写" });
    const retry = await within(pending).findByRole("button", { name: "重试" });
    expect(screen.queryByText("尚无凭证")).toBeNull();
    fail = false;
    fireEvent.click(retry);
    await screen.findByText("REQUEST_000");
    expect(within(pending).queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("keeps request and credential paging controls independently usable", async () => {
    renderRequests(201, { credentialCount: 201 });
    await screen.findByText("REQUEST_199");
    await screen.findByText("CRED_199");
    const pending = screen.getByRole("region", { name: "待填写" });
    expect(screen.getAllByRole("button", { name: "显示更多" })).toHaveLength(2);
    fireEvent.click(within(pending).getByRole("button", { name: "显示更多" }));
    await screen.findByText("REQUEST_200");
    expect(screen.queryByText("CRED_200")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "显示更多" }));
    await screen.findByText("CRED_200");
    expect(screen.queryByRole("button", { name: "显示更多" })).toBeNull();
  });
});
