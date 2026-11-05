import { expect, test } from "@playwright/test";

import { expectNoHorizontalScroll } from "./fixtures.ts";
import { Api, login } from "./support/api.ts";
import { ADMIN, startServer } from "./support/server.ts";

test("registration keeps its first token and runners beyond two hundred stay manageable", async ({ page, browser }) => {
  test.setTimeout(180_000);
  // Runner and audit records are deployment-wide, so this test owns a deployment.
  const server = await startServer();
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  try {
    const cookie = await login(server.base, ADMIN.username, ADMIN.password);
    const [name, value] = cookie.split("=", 2) as [string, string];
    await page.context().addCookies([{ name, value, url: server.base, httpOnly: true, sameSite: "Lax" }]);
    const admin = new Api(server.base).withCookie(cookie);
    const anonymous = new Api(server.base);

    let creates = 0;
    await page.route("**/api/v1/admin/runner-tokens", async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      creates++;
      const response = await route.fetch();
      await held;
      await route.fulfill({ response });
    });
    await page.goto(`${server.base}/admin/runners`);
    const board = page.getByRole("main", { name: "执行节点" });
    await expect(board.getByRole("list", { name: "执行节点" }).getByRole("listitem")).toHaveCount(1);
    await board.getByRole("button", { name: "注册令牌", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "新建注册令牌" });
    const note = dialog.getByRole("textbox", { name: "备注" });
    await note.fill("runner pagination");
    await note.press("Enter");
    await expect.poll(() => creates).toBe(1);
    await expect(dialog.getByRole("button", { name: "创建", exact: true })).toBeDisabled();
    await note.press("Enter");
    await note.press("Escape");
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("button", { name: "取消", exact: true })).toBeDisabled();
    release();
    const token = page.getByTestId("runner-token");
    await expect(token).toHaveText(/^wl_rrt_/);
    expect(creates).toBe(1);
    const first = (await token.textContent())!;
    await page.getByRole("button", { name: "完成", exact: true }).click();
    await expect(token).toHaveCount(0);
    await page.unroute("**/api/v1/admin/runner-tokens");

    const prefix = `zz-pages-${Date.now().toString(36)}`;
    const last = `${prefix}-200`;
    for (let i = 0; i <= 200; i++) {
      const registration = i === 0 ? first : (await admin.call<{ token: string }>("/api/v1/admin/runner-tokens", {
        method: "POST", want: 201, body: { expires_in_hours: 1 },
      })).token;
      const id = `${prefix}-${String(i).padStart(3, "0")}`;
      await anonymous.call("/runner/v1/register", {
        method: "POST", want: 200,
        headers: { Authorization: `Bearer ${registration}`, "Lerryn-Runner-Protocol": "1" },
        body: { id, name: id, slots: 1, labels: ["pagination-e2e"], version: "e2e" },
      });
      if (i < 200) await admin.call(`/api/v1/admin/runners/${id}`, { method: "PATCH", want: 200, body: { disabled: true } });
      if (i === 0) {
        expect((await anonymous.raw("/runner/v1/register", {
          method: "POST", headers: { Authorization: `Bearer ${registration}`, "Lerryn-Runner-Protocol": "1" },
          body: { id: `${prefix}-used`, name: "used token", slots: 1, labels: [] },
        })).status).toBe(401);
      }
    }

    await page.reload();
    const rows = board.getByRole("list", { name: "执行节点" }).getByRole("listitem");
    await expect(rows).toHaveCount(200);
    await expect(board.getByText("已显示的在线节点", { exact: true })).toBeVisible();
    await expect(rows.filter({ hasText: last })).toHaveCount(0);
    await page.setViewportSize({ width: 375, height: 812 });
    await expectNoHorizontalScroll(page);
    await board.getByRole("button", { name: "显示更多", exact: true }).click();
    await expect(rows).toHaveCount(202); // Embedded runner, 200 old records and the current node.
    await expect(board.getByText("已显示的在线节点", { exact: true })).toHaveCount(0);
    await expect(board.getByRole("button", { name: "显示更多", exact: true })).toHaveCount(0);
    const current = rows.filter({ hasText: last });
    await current.scrollIntoViewIfNeeded();
    await current.getByRole("button", { name: `${last} 的操作` }).click();
    await page.getByRole("menuitem", { name: "停用", exact: true }).click();
    await expect(current.getByRole("button", { name: "启用", exact: true })).toBeVisible();
    const disabled = await admin.call<{ next_cursor: string | null }>("/api/v1/admin/runners?status=disabled&limit=200");
    expect(disabled.next_cursor).not.toBeNull();
    const disabledTail = await admin.call<{ items: { id: string; status: string }[] }>(
      `/api/v1/admin/runners?status=disabled&limit=200&cursor=${encodeURIComponent(disabled.next_cursor!)}`,
    );
    expect(disabledTail.items).toEqual([expect.objectContaining({ id: last, status: "disabled" })]);

    await page.setViewportSize({ width: 375, height: 812 });
    await expectNoHorizontalScroll(page);
    const english = await browser.newContext({ locale: "en-US", viewport: { width: 375, height: 812 } });
    try {
      await english.addCookies([{ name, value, url: server.base, httpOnly: true, sameSite: "Lax" }]);
      const translated = await english.newPage();
      await translated.goto(`${server.base}/admin/runners`);
      await expect(translated.getByText("Online runners shown", { exact: true })).toBeVisible();
      await expectNoHorizontalScroll(translated);
    } finally {
      await english.close();
    }
  } finally {
    release();
    await server.stop();
  }
});
