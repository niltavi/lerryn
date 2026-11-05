import { createServer } from "node:http";
import type { AddressInfo } from "node:net";

import { expect, expectNoHorizontalScroll, test } from "./fixtures.ts";
import { Api, sessionCookie } from "./support/api.ts";

test.use({ signedInAs: "admin" });

test("access-token counts remain partial until every page is read, then follow revocation", async ({ page, state, browser }) => {
  test.setTimeout(180_000);
  // A separate owner keeps revoked history and active counts independent of the seed.
  const registration = await new Api(state.base).raw("/api/v1/auth/register", {
    method: "POST",
    body: { username: `token-pages-${Date.now().toString(36)}`, password: "token pagination e2e password" },
  });
  expect(registration.status).toBe(201);
  const cookie = sessionCookie(registration);
  const [cookieName, cookieValue] = cookie.split("=", 2) as [string, string];
  await page.context().addCookies([{ name: cookieName, value: cookieValue, url: state.base, httpOnly: true, sameSite: "Lax" }]);
  const api = new Api(state.base).withCookie(cookie);
  const oldestName = "oldest-active";
  let oldestId = "";
  for (let i = 0; i < 51; i++) {
    const created = await api.call<{ access_token: { id: string } }>("/api/v1/me/tokens", {
      method: "POST", want: 201, body: { name: i === 0 ? oldestName : `revoked-${i}`, scopes: ["read"] },
    });
    if (i === 0) oldestId = created.access_token.id;
    else await api.call(`/api/v1/me/tokens/${created.access_token.id}`, { method: "DELETE", want: 204 });
  }

  await page.goto("/settings/tokens");
  const board = page.getByRole("main", { name: "访问令牌", exact: true });
  const rows = board.getByRole("listitem");
  const count = page.getByRole("region", { name: "密钥", exact: true }).getByRole("link", { name: /^访问令牌/ }).locator(".li-meta");
  await expect(rows).toHaveCount(50);
  await expect(count).toHaveText("0+");
  await expect(rows.filter({ hasText: oldestName })).toHaveCount(0);
  await board.getByRole("button", { name: "继续读取", exact: true }).click();
  await expect(rows).toHaveCount(51);
  await expect(count).toHaveText("1");
  await expect(board.getByRole("button", { name: "继续读取", exact: true })).toHaveCount(0);

  await board.getByRole("button", { name: `撤销 ${oldestName}`, exact: true }).click();
  const revoke = page.getByRole("dialog", { name: `撤销 ${oldestName}`, exact: true });
  await revoke.getByRole("button", { name: "撤销", exact: true }).click();
  await expect(revoke).not.toBeVisible();
  await expect(count).toHaveText("0");
  const tail = await api.call<{ items: { id: string; revoked_at: string | null }[]; next_cursor: string | null }>("/api/v1/me/tokens?limit=100");
  expect(tail.items).toHaveLength(51);
  expect(tail.next_cursor).toBeNull();
  expect(tail.items.find((token) => token.id === oldestId)?.revoked_at).toBeTruthy();
  await page.setViewportSize({ width: 375, height: 812 });
  await expectNoHorizontalScroll(page);

  const english = await browser.newContext({ locale: "en-US", viewport: { width: 375, height: 812 } });
  try {
    await english.addCookies([{ name: cookieName, value: cookieValue, url: state.base, httpOnly: true, sameSite: "Lax" }]);
    const translated = await english.newPage();
    await translated.goto(`${state.base}/settings/tokens`);
    const translatedBoard = translated.getByRole("main", { name: "Access tokens", exact: true });
    const translatedCount = translated.getByRole("region", { name: "Key", exact: true }).getByRole("link", { name: /^Access tokens/ }).locator(".li-meta");
    // Unread records do not guarantee that any further token is usable.
    await expect(translatedCount).toHaveText("0+");
    await translatedBoard.getByRole("button", { name: "Load more", exact: true }).click();
    await expect(translatedBoard.getByRole("listitem")).toHaveCount(51);
    await expect(translatedCount).toHaveText("0");
    await expectNoHorizontalScroll(translated);
  } finally {
    await english.close();
  }
});

test("pending requests beyond the first page can be filled and counts stay accurate", async ({ page, state }) => {
  test.setTimeout(180_000);
  // A fresh owner keeps exact request counts independent of other specs or held runs.
  const registration = await new Api(state.base).raw("/api/v1/auth/register", {
    method: "POST",
    body: { username: `pagination-${Date.now().toString(36)}`, password: "pagination e2e password" },
  });
  expect(registration.status).toBe(201);
  const cookie = sessionCookie(registration);
  const [cookieName, cookieValue] = cookie.split("=", 2) as [string, string];
  await page.context().addCookies([{ name: cookieName, value: cookieValue, url: state.base, httpOnly: true, sameSite: "Lax" }]);
  const api = new Api(state.base).withCookie(cookie);
  const me = (await registration.json()) as { personal_project_id: string };
  const name = `pending-pages-${Date.now().toString(36)}`;
  const { task } = await api.call<{ task: { id: string } }>(`/api/v1/projects/${me.personal_project_id}/tasks/${name}`, {
    method: "PUT", want: 200,
    body: {
      manifest: { runtime: { language: "python", version: "3.12" }, entrypoint: ["python", "main.py"] },
      files: [{ path: "main.py", content: "print('pending request pagination')\n" }],
    },
  });
  let oldest: { id: string; name: string } | undefined;
  for (let i = 0; i < 201; i++) {
    const request = await api.call<{ id: string; name: string }>("/api/v1/credential-requests", {
      method: "POST", want: 201,
      body: {
        task_id: task.id, name: `PAGE_KEY_${String(i).padStart(3, "0")}`,
        purpose: "分页填写验证", fields: [{ name: "API_TOKEN" }], expires_in_minutes: 60,
      },
    });
    if (i === 0) oldest = request;
  }

  await page.goto("/settings/credentials");
  const pending = page.getByRole("region", { name: "待填写", exact: true });
  const rows = pending.getByRole("listitem");
  await expect(rows).toHaveCount(200);
  await expect(page.getByText("200+ 待填", { exact: true })).toBeVisible();
  await expect(rows.filter({ hasText: oldest!.name })).toHaveCount(0);
  await pending.getByRole("button", { name: "显示更多", exact: true }).click();
  await expect(rows).toHaveCount(201);
  await expect(page.getByText("201 待填", { exact: true })).toBeVisible();
  await expect(pending.getByRole("button", { name: "显示更多", exact: true })).toHaveCount(0);

  await rows.filter({ hasText: oldest!.name }).click();
  const fill = page.getByRole("dialog", { name: `${name} 需要凭证` });
  await fill.getByLabel("API_TOKEN").fill("pagination-test-value");
  await fill.getByRole("button", { name: "保存", exact: true }).click();
  await expect(fill).not.toBeVisible();
  await expect(rows).toHaveCount(200);
  await expect(rows.filter({ hasText: oldest!.name })).toHaveCount(0);
  await expect(page.getByText("200 待填", { exact: true })).toBeVisible();
  await expect(page.getByText("200+ 待填", { exact: true })).toHaveCount(0);
  expect((await api.call<{ status: string }>(`/api/v1/credential-requests/${oldest!.id}`)).status).toBe("fulfilled");
});

test("notification history can show a failed delivery after the first fifty records", async ({ page, state }) => {
  test.setTimeout(180_000);
  let received = 0;
  const receiver = createServer((request, response) => {
    request.resume();
    response.writeHead(++received === 1 ? 500 : 204);
    response.end();
  });
  await new Promise<void>((resolve, reject) => {
    receiver.once("error", reject);
    receiver.listen(0, "127.0.0.1", resolve);
  });
  const api = new Api(state.base).withCookie(state.admin.cookie);
  let channelId: string | undefined;
  try {
    const port = (receiver.address() as AddressInfo).port;
    const name = `history-pages-${Date.now().toString(36)}`;
    const created = await api.call<{ channel: { id: string } }>("/api/v1/notification-channels", {
      method: "POST", want: 201,
      body: { name, kind: "webhook", url: `http://127.0.0.1:${port}/hook`, subscriptions: [] },
    });
    channelId = created.channel.id;
    for (let i = 0; i < 51; i++) {
      const delivery = await api.call<{ status: string }>(`/api/v1/notification-channels/${created.channel.id}/test`, {
        method: "POST", want: 200,
      });
      expect(delivery.status).toBe(i === 0 ? "failed" : "delivered");
    }
    expect(received).toBe(51);

    await page.goto("/settings/notifications");
    await page.getByRole("button", { name: `${name} 的发送记录`, exact: true }).click();
    const history = page.getByRole("dialog", { name: `${name} · 发送记录` });
    await expect(history.locator(".pop-row")).toHaveCount(50);
    await expect(history.getByText("发送失败 · 500", { exact: true })).toHaveCount(0);
    await history.getByRole("button", { name: "显示更多", exact: true }).click();
    await expect(history.locator(".pop-row")).toHaveCount(51);
    const failed = history.getByText("发送失败 · 500", { exact: true });
    await failed.scrollIntoViewIfNeeded();
    await expect(failed).toBeVisible();
    await expect(history.getByRole("button", { name: "显示更多", exact: true })).toHaveCount(0);
  } finally {
    if (channelId) await api.raw(`/api/v1/notification-channels/${channelId}`, { method: "DELETE" }).catch(() => undefined);
    receiver.closeAllConnections();
    await new Promise<void>((resolve) => { receiver.close(() => resolve()); });
  }
});
