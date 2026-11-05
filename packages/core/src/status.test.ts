import type { Schemas } from "@lerryn/api";
import { describe, expect, it } from "vitest";

import { translate } from "./i18n/i18n";
import { RUN_STATUSES, isRunActive, resultStatusMeta, runStatusMeta, stageStatusMeta } from "./status";

describe("run status", () => {
  it("covers every status in the contract", () => {
    // A compile-time check: adding a status to the contract breaks this line.
    const all: Record<Schemas["RunStatus"], true> = Object.fromEntries(
      RUN_STATUSES.map((s) => [s, true]),
    ) as Record<Schemas["RunStatus"], true>;
    expect(Object.keys(all)).toHaveLength(10);
  });

  it.each([
    ["queued", "idle", "clock", true, "排队中", "Queued"],
    ["preparing", "run", "wave", true, "准备中", "Preparing"],
    ["running", "run", "wave", true, "运行中", "Running"],
    ["cancelling", "idle", "wave", true, "取消中", "Cancelling"],
    ["completed", "ok", "check", false, "执行完成", "Completed"],
    ["failed", "bad", "x", false, "执行失败", "Failed"],
    ["timed_out", "bad", "timer", false, "已超时", "Timed out"],
    ["cancelled", "idle", "ban", false, "已取消", "Cancelled"],
    ["interrupted", "warn", "unplug", false, "运行中断", "Interrupted"],
    ["skipped", "idle", "skip", false, "已跳过", "Skipped"],
  ] as const)("%s", (status, tone, icon, active, zh, en) => {
    const meta = runStatusMeta(status);
    expect(meta).toMatchObject({ tone, icon, active });
    expect(isRunActive(status)).toBe(active);
    expect(translate("zh-CN", meta.label)).toBe(zh);
    expect(translate("en", meta.label)).toBe(en);
  });

  it("treats an unknown status from a newer server as final", () => {
    expect(isRunActive("paused")).toBe(false);
    expect(runStatusMeta("paused").tone).toBe("idle");
  });
});

describe("stage and result status", () => {
  it("maps stage states", () => {
    expect(stageStatusMeta("running").tone).toBe("run");
    expect(stageStatusMeta("failed").tone).toBe("bad");
    expect(stageStatusMeta("done").tone).toBe("ok");
    expect(stageStatusMeta("bogus").tone).toBe("idle");
  });
  it("keeps the reported result separate from the run status", () => {
    expect(translate("zh-CN", resultStatusMeta("partial").label)).toBe("部分完成");
    expect(resultStatusMeta("failed").tone).toBe("bad");
  });
});
