// A06 (sensitive values) in the console: a credential filled on the fill
// page and printed by the task in pieces and encodings shows up nowhere
// the console renders or fetches, nor in the downloaded log.
import { readFileSync } from "node:fs";

import type { Page, Response } from "@playwright/test";

import { expect, test } from "./fixtures.ts";
import { waitRun, type Run } from "./support/api.ts";

// The same value and task as tests/acceptance TestA06SensitiveValuesNeverShown.
const SECRET = "pk_live_A06+/sensitive=value";
const FORMS = [
  SECRET,
  "pk_live_A06",
  encodeURIComponent(SECRET),
  Buffer.from(SECRET).toString("base64"),
  Buffer.from(`user:${SECRET}`).toString("base64"),
  JSON.stringify(SECRET).slice(1, -1),
];

const leaky = `import base64, json, os, sys, time, urllib.parse
import sbx

key = os.environ["API_TOKEN"]
print("raw", key, flush=True)
sys.stdout.write("split " + key[:7]); sys.stdout.flush()
time.sleep(0.3)
sys.stdout.write(key[7:] + " end\\n"); sys.stdout.flush()
print("b64", base64.b64encode(key.encode()).decode())
print("basic", base64.b64encode(("user:" + key).encode()).decode())
print("url", urllib.parse.quote(key))
print("json", json.dumps({"token": key}))
print("err", key, file=sys.stderr)
sbx.stage_start("call", "calling with " + key)
sbx.stage_done("call")
sbx.result("ok", "token " + key, {"token": key})
print("length", len(key))
`;

function expectNoSecret(where: string, text: string) {
  for (const form of FORMS) {
    const i = text.indexOf(form);
    expect(i, `${where} contains ${form}: …${text.slice(Math.max(0, i - 80), i + 80)}…`).toBe(-1);
  }
}

/** Keeps the body of every API response the page receives, except streams. */
function recordResponses(page: Page) {
  const bodies: Promise<[string, string]>[] = [];
  page.on("response", (resp: Response) => {
    if ((resp.headers()["content-type"] ?? "").includes("text/event-stream")) return;
    bodies.push(resp.text().then((t) => [resp.url(), t] as [string, string], () => [resp.url(), ""] as [string, string]));
  });
  return async () => Promise.all(bodies);
}

async function expectPageClean(page: Page, where: string) {
  expectNoSecret(`${where} (text)`, await page.locator("body").innerText());
  expectNoSecret(`${where} (DOM)`, await page.content());
}

test("a filled credential never shows in the console", async ({ page, agent, state }) => {
  test.setTimeout(180_000);
  const responses = recordResponses(page);
  // Unique per run, so the spec can repeat against a held server.
  const name = `leaky-${Date.now().toString(36)}`;
  const { task } = await agent.call<{ task: { id: string } }>(`/api/v1/projects/${state.alice.project}/tasks/${name}`, {
    method: "PUT",
    body: {
      manifest: {
        description: "打印凭证",
        runtime: { language: "python", version: "3.12" },
        entrypoint: ["python", "main.py"],
        workflow: [{ title: "验证凭证注入", description: "读取运行时注入的凭证并验证使用结果。" }],
        network: "none",
        limits: { timeout: "2m", memory: "128Mi" },
        credentials: [{ name: "API_TOKEN" }],
        stages: [{ id: "call", name: "调用" }],
      },
      files: [{ path: "main.py", content: leaky }],
    },
    want: 200,
  });
  const request = await agent.call<{ id: string; url: string }>("/api/v1/credential-requests", {
    method: "POST",
    body: { task_id: task.id, name: "API_TOKEN", purpose: "调用价格接口", fields: [{ name: "API_TOKEN" }] },
    want: 201,
  });

  // Alice fills it in on the fill page; what comes back does not echo it.
  await page.goto(request.url);
  await expect(page.getByRole("heading", { name: `${name} 需要凭证` })).toBeVisible();
  const field = page.getByLabel("API_TOKEN");
  await expect(field).toHaveAttribute("autocomplete", "off");
  await field.fill(SECRET);
  await page.getByRole("button", { name: "保存" }).click();
  await expect(page.getByRole("heading", { name: "已保存" })).toBeVisible();
  await expectPageClean(page, "fill page after saving");

  const started = await agent.call<Run>(`/api/v1/tasks/${task.id}/runs`, { method: "POST", body: { test: true }, want: 202 });
  const run = await waitRun(agent, started.id);
  expect(run.status).toBe("completed");

  // The run page: result, stage, the whole log, stderr and the timeline.
  await page.goto(`/runs/${run.id}`);
  const board = page.getByRole("main", { name: "运行信息" });
  await expect(board.getByRole("heading", { level: 1 })).toHaveText("执行完成");
  await expect(board.getByRole("region", { name: "结果" })).toContainText("token [REDACTED]");
  const log = board.getByRole("region", { name: "日志" }).getByRole("log");
  await expect(log.getByText("raw [REDACTED]")).toBeVisible();
  await expect(log.getByText("split [REDACTED] end")).toBeVisible();
  await expect(log.getByText("length 28")).toBeVisible();
  await expectPageClean(page, "run page");
  await board.getByRole("group", { name: "输出" }).getByRole("button", { name: "stderr" }).click();
  await expect(log.locator(".ll--stderr .m")).toHaveText("err [REDACTED]");
  await expectPageClean(page, "run page, stderr only");
  // A test run's trigger chip reads 测试; it opens the timeline.
  await board.getByRole("button", { name: "测试", exact: true }).click();
  await expect(page.locator(".events")).toContainText("执行完成");
  await expectPageClean(page, "run timeline");
  await page.keyboard.press("Escape");

  // The downloaded log is redacted the same way.
  const [download] = await Promise.all([page.waitForEvent("download"), board.getByRole("button", { name: "下载日志" }).click()]);
  const file = readFileSync((await download.path())!, "utf8");
  expect(file).toContain("raw [REDACTED]");
  expectNoSecret("downloaded log", file);

  // The task page with its credential details open. Test runs stay out of
  // the task's run history, so the page has none to show.
  await page.goto(`/tasks/${task.id}`);
  await page.getByRole("main", { name }).getByRole("button", { name: "凭证 1" }).click();
  await expect(page.getByRole("group", { name: "引用的凭证" }).getByRole("img", { name: "有效" })).toBeVisible();
  await expectPageClean(page, "task credentials");
  await page.keyboard.press("Escape");

  // Where else the credential and the run appear; each page waits for the
  // row that mentions this task or credential.
  for (const [path, shows] of [
    ["/settings/credentials", page.getByRole("list", { name: "凭证" }).getByText("API_TOKEN").first()],
    ["/runs", page.getByRole("region", { name: "运行列表" }).getByRole("link", { name: new RegExp(name) })],
    ["/", page.getByRole("region", { name: "任务列表" }).getByRole("link", { name: new RegExp(name) })],
  ] as const) {
    await page.goto(path);
    await expect(shows).toBeVisible();
    await expectPageClean(page, path);
  }
  await expect(page.getByRole("region", { name: "需要处理" }).getByText(name)).toHaveCount(0);

  // Nothing the console fetched carried it either.
  const bodies = await responses();
  expect(bodies.some(([url]) => url.includes(`/api/v1/runs/${run.id}`))).toBe(true);
  for (const [url, body] of bodies) expectNoSecret(url, body);
});
