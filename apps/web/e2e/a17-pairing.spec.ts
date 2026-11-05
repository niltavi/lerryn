// A17 (web side): the pairing page shows a one-time QR code; once a phone
// exchanges it, the phone shows up among the signed-in devices and can be
// removed again.
import { expect, test } from "./fixtures.ts";
import { Api, sessionCookie } from "./support/api.ts";

test("pairing shows a QR code and the phone appears after the exchange", async ({ page, state }) => {
  const created = page.waitForResponse((r) => r.url().endsWith("/api/v1/auth/pairing") && r.request().method() === "POST");
  await page.goto("/settings/pairing");
  const board = page.getByRole("main", { name: "手机" });
  const { uri } = (await (await created).json()) as { uri: string };

  await expect(board.getByRole("img", { name: "配对二维码" })).toBeVisible();
  await expect(board.getByText(/^[45]:\d{2}$/)).toBeVisible();
  await expect(board.getByText("只读")).toBeVisible();
  // The manual code is the one in the QR code's URI.
  const params = new URL(uri).searchParams;
  expect(params.get("server")).toBe(state.base);
  await expect(board.getByText(params.get("code")!, { exact: true })).toBeVisible();

  // The phone scans it.
  const name = `iPhone e2e ${Date.now()}`;
  const resp = await new Api(state.base).raw("/api/v1/auth/pairing/exchange", {
    method: "POST",
    body: { server: params.get("server"), code: params.get("code"), device: { name, platform: "ios", os_version: "iOS 26.5" } },
  });
  expect(resp.status).toBe(200);
  const phone = new Api(state.base).withCookie(sessionCookie(resp));
  // The phone session is read-only.
  expect((await phone.raw(`/api/v1/tasks/${state.tasks.plainEcho}/runs`, { method: "POST", body: {} })).status).toBe(403);

  // The list refreshes by itself.
  const devices = board.getByRole("list", { name: "已登录的手机" });
  const row = devices.getByRole("listitem").filter({ hasText: name });
  await expect(row).toBeVisible({ timeout: 20_000 });
  await expect(row).toContainText("iOS 26.5");

  // A used code cannot pair a second phone.
  const again = await new Api(state.base).raw("/api/v1/auth/pairing/exchange", {
    method: "POST",
    body: { server: params.get("server"), code: params.get("code"), device: { name: "second", platform: "ios" } },
  });
  expect(again.status).toBeGreaterThanOrEqual(400);

  // Removing the phone ends its session.
  await row.getByRole("button", { name: `移除 ${name}` }).click();
  await page.getByRole("dialog").getByRole("button", { name: "移除" }).click();
  await expect(row).toHaveCount(0);
  expect((await phone.raw("/api/v1/me")).status).toBe(401);
});
