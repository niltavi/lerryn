// A13 (large logs and disconnects): 100 000 lines stay browsable in
// segments, a live log follows and pauses, a dropped connection resumes
// without losing or repeating lines, and truncation and removal by the
// retention policy are said out loud.
import type { Locator, Page } from "@playwright/test";

import { expect, expectInLogView, test } from "./fixtures.ts";
import { waitStatus, type Run } from "./support/api.ts";

interface LogPage {
  lines: { seq: number }[];
  dropped_lines: number;
  complete: boolean;
}

/** The seqs of the line rows the virtualizer has rendered, top to bottom. */
async function renderedSeqs(log: Locator): Promise<number[]> {
  return log.locator(".ll[data-seq]").evaluateAll((els) =>
    els
      .map((e) => ({ seq: Number(e.getAttribute("data-seq")), top: e.getBoundingClientRect().top }))
      .sort((a, b) => a.top - b.top)
      .map((e) => e.seq),
  );
}

/** The seqs of the line rows inside the visible part of the log. */
async function visibleSeqs(log: Locator): Promise<number[]> {
  return log.evaluate((el) => {
    const box = el.getBoundingClientRect();
    return [...el.querySelectorAll<HTMLElement>(".ll[data-seq]")]
      .filter((r) => {
        const b = r.getBoundingClientRect();
        return b.top >= box.top && b.bottom <= box.bottom;
      })
      .map((r) => Number(r.dataset.seq))
      .sort((a, b) => a - b);
  });
}

/** How many lines the "earlier lines" button says are missing. */
async function gapCount(log: Locator): Promise<number> {
  const text = (await log.locator(".log-more button").textContent()) ?? "";
  return Number(text.replace(/[^\d]/g, ""));
}

function expectContiguous(seqs: number[]) {
  expect(seqs.length).toBeGreaterThan(0);
  for (let i = 1; i < seqs.length; i++) expect(seqs[i], `after line ${seqs[i - 1]}`).toBe(seqs[i - 1]! + 1);
}

async function scrollLog(log: Locator, to: "top" | "bottom" | number) {
  await log.evaluate((el, where) => {
    el.scrollTop = where === "top" ? 0 : where === "bottom" ? el.scrollHeight : el.scrollTop - (where as number);
    el.dispatchEvent(new Event("scroll"));
  }, to);
}

/** Reads a live event stream from the server for a while and returns its complete events. */
async function relayFor(url: string, headers: Record<string, string>, ms: number): Promise<string> {
  const abort = new AbortController();
  const resp = await fetch(url, { headers, signal: abort.signal });
  const reader = resp.body!.getReader();
  const decoder = new TextDecoder();
  let text = "";
  const timer = setTimeout(() => abort.abort(), ms);
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      text += decoder.decode(value, { stream: true });
    }
  } catch {
    // aborted: the cut
  } finally {
    clearTimeout(timer);
  }
  return text.slice(0, text.lastIndexOf("\n\n") + 2);
}

/** The last event ID in an event stream body. */
function lastId(body: string): number {
  const ids = [...body.matchAll(/^id: ?(\d+)$/gm)].map((m) => Number(m[1]));
  return ids.at(-1) ?? 0;
}

const logOf = (page: Page) => page.getByRole("main", { name: "运行信息" }).getByRole("log");

test("100 000 lines: virtual scrolling, load earlier in segments, jump to end", async ({ page, agent, state }) => {
  const tail = await agent.call<LogPage>(`/api/v1/runs/${state.runs.big}/logs?tail=1000`);
  const last = tail.lines.at(-1)!.seq;
  expect(last).toBe(100_001); // 100 000 lines and the platform's exit line
  await page.goto(`/runs/${state.runs.big}`);
  const log = logOf(page);

  // A finished run opens at its end; only a screenful of rows is in the DOM.
  await expectInLogView(log.locator(`.ll[data-seq="${last}"]`));
  expect(await log.locator(".ll").count()).toBeLessThan(120);
  expectContiguous(await renderedSeqs(log));

  // Up to the top of what is held: the rest is one gap, read forward from line 1.
  await scrollLog(log, "top");
  const more = log.getByRole("button", { name: /^更早 [\d,]+ 行$/ });
  await expect(more).toBeVisible();
  await expect(page.getByRole("button", { name: "跳到末尾" })).toBeVisible();
  // The gap is exactly what the tail does not hold.
  const firstHeld = Math.min(...(await renderedSeqs(log)));
  const missing = await gapCount(log);
  expect(missing).toBe(firstHeld - 1);
  expect(missing).toBeGreaterThan(90_000);

  await more.click();
  // The first segment arrives above the gap, which shrinks by a page.
  await expect.poll(() => gapCount(log)).toBe(missing - 1000);
  await scrollLog(log, "top");
  await expect(log.locator('.ll[data-seq="1"] .m')).toHaveText("line 1");
  await expect(log.locator(".ll--stage").first()).toHaveText("▸ 阶段 · 输出");
  expectContiguous(await renderedSeqs(log));

  // A second segment, then the gap again in place.
  // Scroll down to the gap, now below the first segment.
  for (let i = 0; i < 100 && (await log.locator(".log-more").count()) === 0; i++) {
    await log.evaluate((el) => {
      el.scrollTop += el.clientHeight;
      el.dispatchEvent(new Event("scroll"));
    });
  }
  await more.click();
  await expect.poll(() => gapCount(log)).toBe(missing - 2000);
  await expect(log.locator('.ll[data-seq="2000"]')).toBeAttached();
  expect(await log.locator(".ll").count()).toBeLessThan(120);

  // Search finds a line far from the end and scrolls to it.
  await page.getByRole("searchbox", { name: "在日志中搜索" }).fill("line 1500");
  await page.getByRole("searchbox", { name: "在日志中搜索" }).press("Enter");
  await expectInLogView(log.locator('.ll--hit[data-seq="1500"]'));
  await page.getByRole("searchbox", { name: "在日志中搜索" }).press("Escape");

  await page.getByRole("button", { name: "跳到末尾" }).click();
  await expectInLogView(log.locator(`.ll[data-seq="${last}"]`));
  await expect(page.getByRole("button", { name: "跳到末尾" })).toHaveCount(0);
});

test("output over the limit says how many lines were dropped", async ({ page, agent, state }) => {
  const tail = await agent.call<LogPage>(`/api/v1/runs/${state.runs.flood}/logs?tail=10`);
  expect(tail.dropped_lines).toBeGreaterThan(0);
  await page.goto(`/runs/${state.runs.flood}`);
  const board = page.getByRole("main", { name: "运行信息" });
  await expect(board.getByRole("region", { name: "日志" }).getByRole("status")).toHaveText(
    `超出日志上限，已丢弃 ${tail.dropped_lines.toLocaleString("zh-CN")} 行`,
  );
  await expect(logOf(page).getByText(/log limit reached/)).toBeVisible();
});

test.describe("a live log", () => {
  let runId = "";
  test.afterEach(async ({ agent }) => {
    if (runId) await agent.call(`/api/v1/runs/${runId}/cancel`, { method: "POST", body: {} }).catch(() => undefined);
  });

  test("follows new output, pauses when scrolled up, resumes at the end", async ({ page, agent, state }) => {
    runId = (await agent.call<Run>(`/api/v1/tasks/${state.tasks.ticker}/runs`, { method: "POST", body: {}, want: 202 })).id;
    await waitStatus(agent, runId, ["running"]);
    await page.goto(`/runs/${runId}`);
    const log = logOf(page);
    const follow = page.getByRole("switch", { name: "跟随最新输出" });
    await expect(follow).toHaveAttribute("aria-checked", "true");

    // Following: the newest line keeps coming into view.
    await expect.poll(async () => (await renderedSeqs(log)).at(-1) ?? 0, { timeout: 30_000 }).toBeGreaterThan(40);
    const newest = async () => Number(await log.locator(".ll[data-seq]").last().getAttribute("data-seq"));
    const before = await newest();
    await expect.poll(newest).toBeGreaterThan(before + 5);
    await expectInLogView(log.locator(".ll[data-seq]").last());

    // Scrolling up pauses following; the view stays where the reader is.
    await scrollLog(log, 300);
    await expect(follow).toHaveAttribute("aria-checked", "false");
    const jump = page.getByRole("button", { name: "跳到末尾" });
    await expect(jump).toBeVisible();
    const top = (await visibleSeqs(log))[0];
    const height = () => log.evaluate((el) => el.scrollHeight);
    const heightThen = await height();
    await page.waitForTimeout(2_000);
    // New lines kept arriving below while the view stayed put.
    expect(await height()).toBeGreaterThan(heightThen);
    const topRow = log.locator(`.ll[data-seq="${top}"]`);
    await expectInLogView(topRow);
    expect((await visibleSeqs(log))[0]).toBe(top);

    // Jump to the end and follow again.
    await jump.click();
    await expect(follow).toHaveAttribute("aria-checked", "true");
    const resumed = await newest();
    await expect.poll(newest).toBeGreaterThan(resumed + 3);
    await expectInLogView(log.locator(".ll[data-seq]").last());

    // The switch pauses as well.
    await follow.click();
    await expect(follow).toHaveAttribute("aria-checked", "false");
    expectContiguous(await renderedSeqs(log));
  });

  test("resumes after the connection drops, without missing or repeating lines", async ({ page, agent, state }) => {
    runId = (await agent.call<Run>(`/api/v1/tasks/${state.tasks.ticker2}/runs`, { method: "POST", body: {}, want: 202 })).id;
    await waitStatus(agent, runId, ["running"]);

    // The first stream is the real one, relayed for a few seconds and then
    // cut, as a dropped connection does. Reconnects fail while `down`, then
    // reach the server again with the browser's Last-Event-ID.
    const reconnects: (string | null)[] = [];
    let first = true;
    let down = true;
    let cutAt = 0;
    await page.route("**/logs/stream*", async (route) => {
      const req = route.request();
      if (first) {
        first = false;
        const body = await relayFor(req.url(), await req.allHeaders(), 4_000);
        cutAt = lastId(body);
        return route.fulfill({ status: 200, headers: { "content-type": "text/event-stream", "cache-control": "no-store" }, body });
      }
      reconnects.push(req.headers()["last-event-id"] ?? null);
      return down ? route.abort("internetdisconnected") : route.continue();
    });
    await page.goto(`/runs/${runId}`);
    const log = logOf(page);

    // The relayed lines arrive, then the connection drops and the log says so.
    const reconnecting = page.getByRole("status").filter({ hasText: /重新连接 · 第 \d+ 次/ });
    await expect(reconnecting).toBeVisible({ timeout: 30_000 });
    expect(cutAt).toBeGreaterThan(5);
    await expect.poll(async () => Math.max(...(await renderedSeqs(log)))).toBe(cutAt);
    await expect.poll(() => reconnects.length, { timeout: 30_000 }).toBeGreaterThanOrEqual(2);
    expect(reconnects.every((id) => id === String(cutAt))).toBe(true);

    // The network is back: the stream resumes after the last line it had,
    // and the lines printed meanwhile arrive.
    down = false;
    await expect(page.getByRole("status").filter({ hasText: /重新连接/ })).toHaveCount(0, { timeout: 30_000 });
    await expect.poll(async () => Math.max(...(await renderedSeqs(log))), { timeout: 30_000 }).toBeGreaterThan(cutAt + 20);
    expect(reconnects.at(-1)).toBe(String(cutAt));

    // Every line once, in order: the whole held log, scrolled through.
    const seqs = new Set<number>();
    await scrollLog(log, "top");
    // Scrolling updates the virtual rows after the browser dispatches it.
    // Wait for the head before scanning so the previous tail is not sampled.
    await expectInLogView(log.locator('.ll[data-seq="1"]'));
    for (let guard = 0; guard < 200; guard++) {
      for (const s of await renderedSeqs(log)) seqs.add(s);
      const atEnd = await log.evaluate((el) => el.scrollTop + el.clientHeight >= el.scrollHeight - 2);
      if (atEnd) break;
      await log.evaluate((el) => {
        el.scrollTop += el.clientHeight - 40;
        el.dispatchEvent(new Event("scroll"));
      });
      await page.waitForTimeout(30);
    }
    const sorted = [...seqs].sort((a, b) => a - b);
    expect(sorted[0]).toBe(1);
    expectContiguous(sorted);
    const rows = await log.locator(".ll[data-seq]").evaluateAll((els) => els.map((e) => e.getAttribute("data-seq")));
    expect(new Set(rows).size).toBe(rows.length);
  });
});

test("a log removed by retention says so instead of failing", async ({ page, agent, state }) => {
  // The seed aged this run's log past logs_days and waited for the leader to remove it.
  const run = await agent.call<Run & { logs_expired_at: string | null }>(`/api/v1/runs/${state.expiredRun}`);
  expect(run.logs_expired_at).not.toBeNull();
  expect((await agent.raw(`/api/v1/runs/${run.id}/logs?tail=10`)).status).toBe(410);

  const logReads: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes(`/runs/${run.id}/logs`)) logReads.push(r.url());
  });
  await page.goto(`/runs/${run.id}`);
  const board = page.getByRole("main", { name: "运行信息" });
  await expect(board.getByRole("heading", { level: 1 })).toHaveText("执行完成");
  // The run record and its result stay; the log says it was cleared.
  await expect(board.getByRole("region", { name: "结果" })).toContainText("输出了 3 行");
  const logs = board.getByRole("region", { name: "日志" });
  await expect(logs.getByRole("status")).toHaveText("日志已按保留策略清理");
  await expect(logs.getByRole("alert")).toHaveCount(0);
  await expect(logs.getByRole("button", { name: "下载日志" })).toHaveCount(0);
  await expect(logs.locator(".ll")).toHaveCount(0);
  // logs_expired_at is known up front, so the page does not ask for the log at all.
  await page.waitForTimeout(1_000);
  expect(logReads).toEqual([]);
});
