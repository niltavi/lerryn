import { defineConfig, devices } from "@playwright/test";

/**
 * End-to-end tests of the console against a real `lerryn all-in-one`
 * (Docker required). Run with `make e2e-web`; see apps/web/e2e/README.md.
 */
export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./e2e/global-setup.ts",
  // The specs share one deployment and only add data of their own.
  fullyParallel: true,
  workers: process.env.CI ? 2 : 4,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [["list"]],
  outputDir: "./e2e/.results",
  use: {
    locale: "zh-CN",
    timezoneId: "Asia/Shanghai",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
