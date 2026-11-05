// A03 (console observation): from the overview into a task and one of its
// runs, which shows status, duration, trigger, version, logs and result.
import type { Page } from "@playwright/test";

import { expect, test } from "./fixtures.ts";
import type { Run } from "./support/api.ts";

interface Overview {
  counts: { tasks: number; attention: number; running: number; queued: number };
}

const stat = (page: Page, label: string) => page.locator(".hero .stat").filter({ hasText: label }).locator(".stat-n");

test("the overview shows the deployment's counts", async ({ page, agent }) => {
  await page.goto("/");
  const main = page.getByRole("main", { name: "概览" });
  await expect(main.getByRole("region", { name: "需要处理" })).toBeVisible();
  const overview = await agent.call<Overview>("/api/v1/overview");
  // One failed run and one pending credential request at least; other specs may add runs.
  expect(overview.counts.attention).toBeGreaterThanOrEqual(2);
  await expect(stat(page, "需要处理")).toHaveText(String(overview.counts.attention));
  await expect(stat(page, "运行中")).toHaveText(/^\d+$/);
  await expect(page.getByRole("heading", { name: `任务 ${overview.counts.tasks}` })).toBeVisible();

  const attention = main.getByRole("region", { name: "需要处理" });
  await expect(attention.getByRole("link", { name: /执行失败 broken-import 导入 · the process exited with code 2/ })).toBeVisible();
  const credentialRequest = attention.getByRole("button", { name: /待填写的凭证 API_TOKEN needs-key 请你填写/ });
  await expect(credentialRequest).toBeVisible();
  await credentialRequest.click();
  const fill = page.getByRole("dialog", { name: "needs-key 需要凭证" });
  await expect(fill.getByLabel("API_TOKEN")).toBeVisible();
  await fill.getByRole("button", { name: "取消", exact: true }).click();
  await expect(fill).not.toBeVisible();
  // The cron trigger of the active task is the next fire.
  await expect(page.locator(".hero .stat").filter({ hasText: "下一次" })).toContainText("08:00");
});

test("overview → task → run shows status, duration, trigger, version, result and logs", async ({ page, agent, state }) => {
  const run = await agent.call<Run>(`/api/v1/runs/${state.runs.daily}`);
  await page.goto("/");
  await page.getByRole("region", { name: "任务列表" }).getByRole("link", { name: /daily-report/ }).click();

  const task = page.getByRole("main", { name: "daily-report" });
  await expect(task.getByRole("heading", { name: "daily-report", level: 1 })).toBeVisible();
  await expect(task).toContainText(/已激活\s*v2/);
  await expect(task.getByRole("button", { name: "每个工作日 08:00（Asia/Shanghai）" })).toBeVisible();
  await task.getByRole("list", { name: "最近的运行" }).getByRole("link", { name: /执行完成 · .* · v2/ }).first().click();

  await expect(page).toHaveURL(new RegExp(`/runs/${run.id}$`));
  const board = page.getByRole("main", { name: "运行信息" });
  await expect(board.getByRole("heading", { level: 1 })).toHaveText("执行完成");
  // Duration as the API reports it, to a tenth of a second.
  const ms = Date.parse(run.finished_at!) - Date.parse(run.started_at!);
  expect(ms).toBeLessThan(10_000);
  await expect(board.locator(".verdict-time")).toHaveText(`${(Math.round(ms / 100) / 10).toFixed(1)}s`);
  await expect(board.getByText(/今天 \d{2}:\d{2} 开始/)).toBeVisible();
  await expect(board.getByRole("navigation", { name: "位置" }).getByRole("link", { name: "daily-report" })).toBeVisible();

  // Trigger, with the client that started it in the timeline.
  await board.getByRole("button", { name: "Agent 发起" }).click();
  const timeline = page.locator(".events");
  await expect(timeline).toContainText(`排队 · ${state.client}`);
  await expect(timeline).toContainText("运行");
  await expect(timeline).toContainText("执行完成 · 退出码 0");
  await page.keyboard.press("Escape");

  // Version and its limits.
  await board.getByRole("button", { name: "v2" }).click();
  const versionDetails = page.locator("dl.kv").filter({ hasText: "版本" });
  await expect(versionDetails).toContainText("v2");
  await expect(versionDetails).toContainText("5m");
  await page.keyboard.press("Escape");

  // Result: status, summary, and the Agent panels the manifest declares.
  const result = board.getByRole("region", { name: "结果" });
  await expect(result).toContainText("成功");
  await expect(result.getByText("生成 3 份报告")).toBeVisible();
  await expect(board.getByRole("region", { name: "报告数" })).toContainText("3份");
  const leads = board.getByRole("region", { name: "高意向线索" }).getByRole("table");
  await expect(leads.getByRole("row", { name: "华东客户 91" })).toBeVisible();
  await expect(board.getByRole("region", { name: "来源" }).getByRole("img", { name: "来源：官网 5，活动 2" })).toBeVisible();
  await expect(board.getByRole("region", { name: "备注" })).toContainText("本周线索比上周多 2 条");

  // Logs: every line of this short run, in order, ending with the exit.
  const log = board.getByRole("region", { name: "日志" }).getByRole("log");
  await expect(log.locator(".ll[data-seq]")).toHaveCount(10);
  await expect(log.locator('.ll[data-seq="1"] .m')).toHaveText("fetch source 0");
  await expect(log.locator('.ll[data-seq="10"] .m')).toContainText("process exited with code 0");
});

test("a failed run says where and why it failed", async ({ page, state }) => {
  await page.goto(`/runs/${state.runs.broken}`);
  const board = page.getByRole("main", { name: "运行信息" });
  await expect(board.getByRole("heading", { level: 1 })).toHaveText("执行失败");
  await expect(board.getByRole("status").filter({ hasText: "the process exited with code 2" })).toContainText("导入");
  await expect(board.getByRole("group", { name: "阶段" }).getByRole("button", { name: /^导入，失败/ })).toBeVisible();
  const log = board.getByRole("log");
  await expect(log.locator(".ll--stderr .m")).toHaveText("row 17: column price is missing");
  // stderr filter keeps stderr and platform lines only.
  await board.getByRole("group", { name: "输出" }).getByRole("button", { name: "stderr" }).click();
  await expect(log.locator(".ll--stdout")).toHaveCount(0);
  await expect(log.locator(".ll--stderr")).toHaveCount(1);
});

test("the runs list filters failed runs and opens one", async ({ page, state }) => {
  await page.goto("/runs");
  await page.getByRole("group", { name: "按状态筛选" }).getByRole("button", { name: "失败" }).click();
  const list = page.getByRole("region", { name: "运行列表" });
  await expect(list.getByRole("link", { name: /broken-import/ }).first()).toBeVisible();
  await expect(list.getByRole("link", { name: /daily-report/ })).toHaveCount(0);
  await list.getByRole("link", { name: /broken-import/ }).first().click();
  await expect(page.getByRole("main", { name: "运行预览" })).toContainText("执行失败");
  await page.getByRole("main", { name: "运行预览" }).getByRole("link", { name: "打开" }).click();
  await expect(page).toHaveURL(new RegExp(`/runs/${state.runs.broken}$`));
});
