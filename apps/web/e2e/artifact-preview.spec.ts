import { readFileSync } from "node:fs";

import { expect, test } from "./fixtures.ts";
import { Api, waitRun, type Run } from "./support/api.ts";

test.use({ signedInAs: "admin" });

test("artifact previews truncate text, format JSON and keep downloads complete", async ({ page, state }) => {
  test.setTimeout(180_000);
  // Keep Alice's overview counts independent of this task's lifecycle.
  const api = new Api(state.base).withCookie(state.admin.cookie);
  const me = await api.call<{ personal_project_id: string }>("/api/v1/me");
  const name = `artifact-preview-${Date.now().toString(36)}`;
  const { task } = await api.call<{ task: { id: string } }>(`/api/v1/projects/${me.personal_project_id}/tasks/${name}`, {
    method: "PUT",
    body: {
      manifest: {
        description: "预览与下载验证",
        runtime: { language: "python", version: "3.12" },
        entrypoint: ["python", "main.py"],
        network: "none",
        limits: { timeout: "2m", memory: "128Mi" },
      },
      files: [{ path: "main.py", content: `from pathlib import Path
import json

out = Path("/sbx/out")
(out / "report.txt").write_text("x" * 65536 + "DOWNLOAD_ONLY_TAIL", encoding="utf-8")
(out / "data.json").write_text(json.dumps({"title": "中文🙂", "count": 3}, ensure_ascii=False), encoding="utf-8")
(out / "unsafe.html").write_text("<script>alert('artifact-script')</script>", encoding="utf-8")
` }],
    },
    want: 200,
  });
  const started = await api.call<Run>(`/api/v1/tasks/${task.id}/runs`, { method: "POST", body: { test: true }, want: 202 });
  expect((await waitRun(api, started.id)).status).toBe("completed");
  await page.goto(`/runs/${started.id}`);
  const board = page.getByRole("main", { name: "运行信息" });
  await expect(board.getByRole("heading", { level: 1 })).toHaveText("执行完成");

  await board.getByRole("button", { name: "预览 report.txt", exact: true }).click();
  const preview = board.locator('pre.preview[aria-label="report.txt"]');
  await expect(preview).toContainText("预览只显示前 64 KB，完整内容请下载");
  const text = await preview.textContent();
  expect(text?.startsWith("x".repeat(65536))).toBe(true);
  expect(text).not.toContain("DOWNLOAD_ONLY_TAIL");
  await board.getByRole("button", { name: "收起预览", exact: true }).click();
  await expect(preview).toHaveCount(0);

  await board.getByRole("button", { name: "预览 data.json", exact: true }).click();
  await expect(board.locator('pre.preview[aria-label="data.json"]')).toHaveJSProperty("textContent", `{
  "title": "中文🙂",
  "count": 3
}`);
  await expect(board.getByRole("link", { name: "下载 unsafe.html", exact: true })).toBeVisible();
  await expect(board.getByRole("button", { name: "预览 unsafe.html", exact: true })).toHaveCount(0);

  const [download] = await Promise.all([
    page.waitForEvent("download"),
    board.getByRole("link", { name: "下载 report.txt", exact: true }).click(),
  ]);
  const content = readFileSync((await download.path())!, "utf8");
  expect(content.length).toBe(65536 + "DOWNLOAD_ONLY_TAIL".length);
  expect(content.endsWith("DOWNLOAD_ONLY_TAIL")).toBe(true);
});
