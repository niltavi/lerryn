import { expect, expectAccessible, expectNoHorizontalScroll, test } from "./fixtures.ts";
import { Api, sessionCookie } from "./support/api.ts";

test.use({ signedInAs: null, viewport: { width: 375, height: 812 } });

test("manages AI configuration without showing the saved key", async ({ page, state }) => {
  const api = new Api(state.base);
  const registration = await api.raw("/api/v1/auth/register", {
    method: "POST", body: { username: `ai-settings-${Date.now().toString(36)}`, password: "AI settings browser test" },
  });
  expect(registration.status).toBe(201);
  const cookie = sessionCookie(registration);
  const [name, value] = cookie.split("=", 2) as [string, string];
  await page.context().addCookies([{ name, value, url: state.base, httpOnly: true, sameSite: "Lax" }]);
  const session = api.withCookie(cookie);
  const baseURL = "https://model-gateway.example/api/openai/compatible/v1";
  const apiKey = "sk-e2e-provider-key-0123456789";

  await page.goto("/settings/credentials");
  await page.getByRole("button", { name: "配置 AI", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "AI 能力" });
  const url = dialog.getByLabel("API 基础地址");
  const key = dialog.getByLabel("API Key", { exact: false });
  const models = dialog.getByLabel("可用模型");
  await expect(key).toHaveAttribute("type", "password");
  await url.fill(baseURL);
  await key.fill(apiKey);
  await models.fill("provider/model-a\nprovider/model-b");
  await expectNoHorizontalScroll(page);
  await expectAccessible(page);
  await dialog.getByRole("button", { name: "保存", exact: true }).click();

  await expect(page.getByText("2 个模型", { exact: true })).toBeVisible();
  await expectNoHorizontalScroll(page);
  const config = await session.call("/api/v1/me/ai");
  expect(config).toEqual({ base_url: baseURL, models: ["provider/model-a", "provider/model-b"] });

  await page.getByRole("button", { name: "编辑配置", exact: true }).click();
  await expect(key).toHaveValue("");
  await url.fill("https://other.example/v1");
  await expect(dialog.getByRole("button", { name: "保存", exact: true })).toBeDisabled();
  await url.fill(baseURL);
  await models.fill("provider/model-b");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page.getByText("1 个模型", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "编辑配置", exact: true }).click();
  await dialog.getByRole("button", { name: "移除配置", exact: true }).click();
  await page.getByRole("dialog", { name: "移除 AI 配置" }).getByRole("button", { name: "移除配置", exact: true }).click();
  await expect(page.getByRole("button", { name: "配置 AI", exact: true })).toBeVisible();
  expect(await session.call("/api/v1/me/ai")).toEqual({ models: [] });
});
