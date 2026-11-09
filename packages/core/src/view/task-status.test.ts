import { describe, expect, it } from "vitest";

import { taskStatus } from "./model";

// Task activation and execution are independent, including test/manual runs.
describe("current task status", () => {
  it.each([
    { task: { state: "inactive", active_version: null }, label: "task.neverActivated", icon: "pending" },
    { task: { state: "inactive", active_version: 2 }, label: "task.paused", icon: "pause" },
    { task: { state: "active", next_run_at: "2026-11-06T00:00:00Z" }, label: "task.scheduled", icon: "calendar" },
    { task: { state: "active", next_run_at: null }, label: "task.active", icon: "pending" },
    { task: { state: "inactive", active_runs: 1, last_run: { status: "failed" } }, label: "run.status.running", icon: "wave" },
    { task: { state: "inactive", active_runs: 0, last_run: { status: "queued" } }, label: "run.status.queued", icon: "clock" },
    { task: { state: "inactive", last_run: { status: "preparing" } }, label: "run.status.preparing", icon: "wave" },
    { task: { state: "active", last_run: { status: "cancelling" } }, label: "run.status.cancelling", icon: "wave" },
    { task: { state: "active", archived_at: "2026-11-05T00:00:00Z" }, label: "task.archived", icon: "archive" },
  ] as const)("shows $label", ({ task, label, icon }) => {
    expect(taskStatus(task)).toMatchObject({ label, glyph: { icon } });
  });

  it("keeps the next schedule distinct from a completed previous run", () => {
    expect(taskStatus({ state: "active", last_run: { status: "completed" }, next_run_at: "2026-11-06T00:00:00Z" }))
      .toEqual({ label: "task.scheduled", glyph: { tone: "idle", icon: "calendar" } });
  });
});
