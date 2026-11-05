// Smoke coverage of the console around the acceptance scenarios: sign-in
// without SSO, settings and admin pages, both themes, a phone-width
// viewport and an accessibility scan of the main pages.
import type { Page } from "@playwright/test";

import { expect, expectAccessible, expectNoHorizontalScroll, test } from "./fixtures.ts";
import { login } from "./support/api.ts";

test.describe("signed out", () => {
  test.use({ signedInAs: null });

  test("login without SSO: password form, register link, then the overview", async ({ page, state }) => {
    await page.goto(`/runs/${state.runs.daily}`);
    await expect(page).toHaveURL(/\/login\?redirect=/);
    await expect(page.getByRole("heading", { name: "登录" })).toBeVisible();
    // No SSO configured: no SSO button and no "or" divider, no admin entry.
    await expect(page.getByRole("button", { name: /企业账号|SSO/ })).toHaveCount(0);
    await expect(page.getByText("或", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "管理员登录" })).toHaveCount(0);
    await expect(page.getByRole("link", { name: "注册" })).toBeVisible();

    // An empty form says what is missing without a request.
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await expect(page.getByText("请填写此项").first()).toBeVisible();

    await page.getByLabel("用户名").fill(state.alice.username);
    await page.getByLabel("密码", { exact: true }).fill(state.alice.password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    // Back where the sign-in started.
    await expect(page).toHaveURL(new RegExp(`/runs/${state.runs.daily}$`));
    await expect(page.getByRole("main", { name: "运行信息" })).toBeVisible();
  });

  test("the login page is accessible and fits a phone", async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto("/login");
    await expect(page.getByRole("heading", { name: "登录" })).toBeVisible();
    await expectNoHorizontalScroll(page);
    await expectAccessible(page);
  });
});

const settings: [string, string][] = [
  ["/settings/tokens", "访问令牌"],
  ["/settings/credentials", "凭证"],
  ["/settings/notifications", "通知"],
  ["/settings/pairing", "手机"],
  ["/settings/tools", "工具"],
  ["/settings/account", "账号"],
];

test("settings pages show the seeded data", async ({ page, state }) => {
  for (const [path, title] of settings) {
    await page.goto(path);
    await expect(page.getByRole("main", { name: title })).toBeVisible();
    await expect(page.getByRole("heading", { name: title, level: 1 })).toBeVisible();
  }
  await page.goto("/settings/tokens");
  await expect(page.getByRole("main", { name: "访问令牌" })).toContainText(state.client);
  await page.goto("/settings/credentials");
  await expect(page.getByRole("main", { name: "凭证" })).toContainText("API_TOKEN");
  await page.goto("/settings/notifications");
  await expect(page.getByRole("main", { name: "通知" })).toContainText("运维群");
  // A regular member has no admin pages.
  await page.goto("/admin/users");
  await expect(page.getByText("找不到")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "管理" })).toHaveCount(0);
});

// The server accepts session writes only from server.public_url. The seeded
// deployment's is 127.0.0.1; localhost reaches the same server under another
// name, as a host's IP does next to its configured name.
test("at another address than the deployment's, the console says so and changes say why they fail", async ({ page, state }) => {
  const other = state.base.replace("127.0.0.1", "localhost");
  const [name, value] = (await login(state.base, state.alice.username, state.alice.password)).split("=", 2) as [string, string];
  await page.context().addCookies([{ name, value, url: other, httpOnly: true, sameSite: "Lax" }]);
  await page.goto(`${other}/settings/tokens`);
  const banner = page.getByRole("status").filter({ hasText: "当前地址只能查看" });
  await expect(banner).toContainText(new URL(state.base).host);
  await expect(banner.getByRole("link", { name: `改用部署地址 ${state.base}` })).toHaveAttribute("href", `${state.base}/settings/tokens`);

  await page.getByRole("button", { name: "新建" }).click();
  const dialog = page.getByRole("dialog", { name: "新建令牌" });
  await dialog.getByLabel("名称").fill("from the wrong address");
  await dialog.getByRole("button", { name: "创建" }).click();
  await expect(dialog.getByRole("alert")).toHaveText("当前打开的地址不是部署地址，修改没有生效。请按页面顶部的提示改用部署地址");

  // At the deployment's own address there is no banner.
  await page.goto("/settings/tokens");
  await expect(page.getByRole("main", { name: "访问令牌" })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "当前地址只能查看" })).toHaveCount(0);
});

test.describe("as the admin", () => {
  test.use({ signedInAs: "admin" });

  test("admin pages list accounts, the embedded runner and audit events", async ({ page, state }) => {
    await page.goto("/");
    await page.getByRole("navigation", { name: "主导航" }).getByRole("link", { name: "管理" }).click();
    await expect(page).toHaveURL(/\/admin\/users$/);
    const users = page.getByRole("main", { name: "账号" });
    await expect(users).toContainText(state.alice.username);
    await expect(users).toContainText(state.admin.username);

    await page.goto("/admin/runners");
    await expect(page.getByRole("main", { name: "执行节点" })).toContainText("在线");
    await page.goto("/admin/audit");
    await expect(page.getByRole("main", { name: "审计" })).toContainText("login");
    await page.goto("/admin/login");
    // Without auth.oidc the SSO settings say it is not configured.
    await expect(page.getByRole("main", { name: "登录" })).toBeVisible();
  });

  test("admin pages are accessible", async ({ page }) => {
    for (const path of ["/admin/users", "/admin/runners", "/admin/audit", "/admin/login"]) {
      await page.goto(path);
      await page.waitForTimeout(800);
      await expectAccessible(page);
    }
  });
});

test("light and dark themes switch and stick", async ({ page }) => {
  await page.goto("/settings/account");
  const html = page.locator("html");
  await page.getByRole("button", { name: "深色" }).click();
  await expect(html).toHaveAttribute("data-theme", "dark");
  await expect(page.getByRole("button", { name: "深色" })).toHaveAttribute("aria-pressed", "true");
  const dark = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  await page.reload();
  await expect(html).toHaveAttribute("data-theme", "dark");

  await page.getByRole("button", { name: "浅色" }).click();
  await expect(html).toHaveAttribute("data-theme", "light");
  const light = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  expect(light).not.toBe(dark);

  // "Follow the system" follows the browser's scheme.
  await page.getByRole("button", { name: "跟随系统" }).click();
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(html).toHaveAttribute("data-theme", "dark");
  await page.emulateMedia({ colorScheme: "light" });
  await expect(html).toHaveAttribute("data-theme", "light");
});

const mainPages = (state: { runs: { daily: string; broken: string }; tasks: { dailyReport: string } }): [string, string][] => [
  ["/", "概览"],
  ["/runs", "运行预览"],
  [`/runs/${state.runs.daily}`, "运行信息"],
  [`/runs/${state.runs.broken}`, "运行信息"],
  [`/tasks/${state.tasks.dailyReport}`, "daily-report"],
  ...settings.map(([p, t]) => [p, t] as [string, string]),
];

async function settle(page: Page, main: string) {
  await expect(page.getByRole("main", { name: main })).toBeVisible();
  // Queries settle; the pages keep a live stream open, so there is no network idle.
  await page.waitForTimeout(800);
}

test("main pages fit a 375 px phone without horizontal scroll", async ({ page, state }) => {
  await page.setViewportSize({ width: 375, height: 812 });
  for (const [path, main] of mainPages(state)) {
    await page.goto(path);
    await settle(page, main);
    await expectNoHorizontalScroll(page);
  }
});

test("task details stay above the fold across the compact layout breakpoint", async ({ page, state }) => {
  for (const width of [320, 788, 960, 961, 1280]) {
    await page.setViewportSize({ width, height: 812 });
    await page.goto(`/tasks/${state.tasks.dailyReport}`);
    const heading = page.getByRole("heading", { name: "daily-report", level: 1 });
    const list = page.getByRole("region", { name: "任务列表" });
    await expect(heading).toBeInViewport();
    await expectNoHorizontalScroll(page);
    const boardBox = await page.getByRole("main", { name: "daily-report" }).boundingBox();
    expect(boardBox!.y).toBeLessThan(100);
    const toggle = list.getByRole("button", { name: /^任务/ });
    if (width <= 960) {
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await toggle.click();
      await expect(toggle).toHaveAttribute("aria-expanded", "true");
      await list.getByRole("link", { name: /daily-report/ }).click();
      await expect(toggle).toHaveAttribute("aria-expanded", "false");
      await expect(heading).toBeInViewport();
    } else {
      await expect(toggle).toBeHidden();
      const listBox = await list.boundingBox();
      expect(boardBox!.x).toBeGreaterThanOrEqual(listBox!.x + listBox!.width);
    }
  }
});

for (const scheme of ["light", "dark"] as const) {
  test(`main pages pass an axe scan (${scheme})`, async ({ page, state }) => {
    await page.emulateMedia({ colorScheme: scheme });
    for (const [path, main] of mainPages(state)) {
      await page.goto(path);
      await settle(page, main);
      await expect(page.locator("html")).toHaveAttribute("data-theme", scheme);
      await expectAccessible(page);
    }
  });
}
