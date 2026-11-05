// A04 (execution progress): three reported stages show in order and update
// while the run goes; picking one finds its log; a run that reports no
// stage shows no invented stage bar or percentage.
import { expect, expectInLogView, test } from "./fixtures.ts";
import { waitRun, type Run } from "./support/api.ts";

test("a live run's stages update in order and locate their logs", async ({ page, agent, state }) => {
  const started = await agent.call<Run>(`/api/v1/tasks/${state.tasks.dailyReport}/runs`, { method: "POST", body: {}, want: 202 });
  await page.goto(`/runs/${started.id}`);
  const board = page.getByRole("main", { name: "运行信息" });
  const stages = board.getByRole("group", { name: "阶段" }).getByRole("button");

  // Declared order, before and while they run.
  await expect(stages).toHaveCount(3);
  await expect(stages.nth(0)).toHaveAttribute("data-stage", "fetch");
  await expect(stages.nth(1)).toHaveAttribute("data-stage", "analyze");
  await expect(stages.nth(2)).toHaveAttribute("data-stage", "publish");

  // Each stage is seen running and then done, in order, without a reload.
  const seen: string[] = [];
  const deadline = Date.now() + 120_000;
  while (Date.now() < deadline) {
    const statuses = await stages.evaluateAll((els) => els.map((e) => e.getAttribute("data-status")));
    const running = statuses.indexOf("running");
    if (running !== -1 && seen.at(-1) !== String(running)) seen.push(String(running));
    // A stage after a running one has not started; one before it is done.
    if (running !== -1) {
      expect(statuses.slice(0, running).every((s) => s === "done")).toBe(true);
      expect(statuses.slice(running + 1).every((s) => s === "pending")).toBe(true);
    }
    if (statuses.every((s) => s === "done")) break;
    await page.waitForTimeout(100);
  }
  expect(seen).toEqual(["0", "1", "2"]);
  await expect(board.getByRole("heading", { level: 1 })).toHaveText("执行完成");
  await expect(stages.nth(0)).toHaveAccessibleName(/^获取数据，已完成，\d+\.\ds/);
  await expect(stages.nth(2)).toHaveAccessibleName(/^发布，已完成，\d+\.\ds/);
  // The run records the same order.
  const done = await waitRun(agent, started.id);
  expect(done.stages.map((s) => [s.id, s.status])).toEqual([
    ["fetch", "done"],
    ["analyze", "done"],
    ["publish", "done"],
  ]);

  // Lines are labelled by their stage, with a label where each one begins.
  const log = board.getByRole("log");
  await expect(log.locator(".ll--stage")).toHaveText(["▸ 阶段 · 获取数据", "▸ 阶段 · 分析", "▸ 阶段 · 发布"]);
  await expect(log.locator('.ll[data-stage="analyze"][data-seq]')).toHaveCount(4);
  await expect(log.locator('.ll[data-stage="publish"][data-seq] .m')).toHaveText(["publish report"]);
  // Picking a stage in the graph puts its first lines at the top of the log.
  await stages.nth(1).click();
  await expectInLogView(log.locator('.ll--stage[data-stage="analyze"]'));
  await expectInLogView(log.locator('.ll[data-stage="analyze"][data-seq]').first());
});

test("picking a stage scrolls the log to where it begins", async ({ page, agent, state }) => {
  // 100 000 lines in one stage after a stage-less start: the first line of
  // the stage is far above the tail the page opens on.
  const run = await agent.call<Run>(`/api/v1/runs/${state.runs.big}`);
  expect(run.stages.map((s) => s.id)).toEqual(["emit"]);
  await page.goto(`/runs/${run.id}`);
  const board = page.getByRole("main", { name: "运行信息" });
  const log = board.getByRole("log");
  await expect(log.locator('.ll[data-seq="100001"]')).toBeVisible();
  // The tail holds the end of the stage only; picking it shows the first line held.
  await board.getByRole("group", { name: "阶段" }).getByRole("button", { name: /^输出 .*已完成$/ }).click();
  await expectInLogView(log.locator(".ll--stage").first());
  const first = await log.locator(".ll[data-seq]").first().getAttribute("data-seq");
  await expectInLogView(log.locator(`.ll[data-seq="${first}"]`));
  await expectInLogView(log.locator('.ll[data-seq="100001"]'), false);
});

test("a run without stages shows no stage bar", async ({ page, agent, state }) => {
  const run = await agent.call<Run>(`/api/v1/runs/${state.runs.plain}`);
  expect(run.stages).toEqual([]);
  await page.goto(`/runs/${run.id}`);
  const board = page.getByRole("main", { name: "运行信息" });
  await expect(board.getByRole("heading", { level: 1 })).toHaveText("执行完成");
  await expect(board.getByRole("log").locator(".ll--stdout .m")).toHaveText(["hello from plain-echo", "done"]);
  await expect(board.getByRole("group", { name: "阶段" })).toHaveCount(0);
  await expect(board.locator(".flutes, .flute")).toHaveCount(0);
  await expect(board.locator(".ll--stage")).toHaveCount(0);
  // Nothing on the page reads as a made-up progress figure.
  await expect(board).not.toContainText("%");
});
