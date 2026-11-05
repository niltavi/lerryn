// A17 on a real phone screen: the app in Expo Go on an iOS simulator,
// against a real all-in-one. It pairs with the console's link, goes from
// the overview to a task and its runs, reads the stages and the log tail,
// and checks with a recording proxy that the phone changed nothing.
//
// The pairing link waits for one tap on 登录 in the simulator, as it does
// for a person (a link can come from anywhere, so the app asks first);
// everything else is driven by deep links and read back with macOS Vision.
//
//   make e2e-mobile            (or: node apps/mobile/e2e/flow.ts)
//
// LERRYN_E2E_SIMULATOR picks the simulator (name or UDID); by default the
// first booted one with Expo Go installed.
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";

import { Api } from "../../web/e2e/support/api.ts";
import { freePort } from "../../web/e2e/support/server.ts";
import { startMobileDeployment } from "./serve.ts";

const here = import.meta.dirname;
const appDir = resolve(here, "..");
const out = join(here, ".results");
const EXPO_GO = "host.exp.Exponent";

function simulator(): string {
  const wanted = process.env.LERRYN_E2E_SIMULATOR;
  const list = JSON.parse(execFileSync("xcrun", ["simctl", "list", "devices", "booted", "-j"], { encoding: "utf8" })) as {
    devices: Record<string, { udid: string; name: string }[]>;
  };
  const booted = Object.values(list.devices).flat();
  for (const d of booted) {
    if (wanted && d.udid !== wanted && d.name !== wanted) continue;
    const apps = execFileSync("xcrun", ["simctl", "listapps", d.udid], { encoding: "utf8" });
    if (apps.includes(EXPO_GO)) return d.udid;
  }
  throw new Error(
    wanted
      ? `simulator ${wanted} is not booted or has no Expo Go`
      : "no booted iOS simulator with Expo Go; boot one (Xcode > Simulator) and install Expo Go (npx expo start --ios once)",
  );
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
let shot = 0;

/** Screenshots the simulator and returns the recognized text without spaces. */
function screen(udid: string, name: string): string {
  const file = join(out, `${String(++shot).padStart(2, "0")}-${name}.png`);
  execFileSync("xcrun", ["simctl", "io", udid, "screenshot", file], { stdio: "ignore" });
  return execFileSync("swift", [join(here, "ocr.swift"), file], { encoding: "utf8" }).replace(/\s+/g, "");
}

/** Waits until the screen shows every text (spaces ignored). */
async function expectScreen(udid: string, name: string, texts: string[], withinMs = 30_000): Promise<string> {
  const deadline = Date.now() + withinMs;
  let seen = "";
  for (;;) {
    seen = screen(udid, name);
    const missing = texts.filter((t) => !seen.includes(t.replace(/\s+/g, "")));
    if (missing.length === 0) {
      console.log(`  ok  ${name}: ${texts.join(" · ")}`);
      return seen;
    }
    if (Date.now() > deadline) throw new Error(`${name}: missing ${missing.join(", ")} in screen text:\n${seen}`);
    await sleep(1_000);
  }
}

async function startMetro(port: number) {
  const child = spawn("pnpm", ["exec", "expo", "start", "--port", String(port)], {
    cwd: appDir,
    env: { ...process.env, CI: "1", REACT_NATIVE_PACKAGER_HOSTNAME: "127.0.0.1" },
    stdio: ["ignore", "pipe", "pipe"],
    detached: true,
  });
  let log = "";
  child.stdout.on("data", (d) => (log += d));
  child.stderr.on("data", (d) => (log += d));
  const deadline = Date.now() + 120_000;
  for (;;) {
    try {
      if ((await (await fetch(`http://127.0.0.1:${port}/status`)).text()).includes("running")) break;
    } catch {
      // not up yet
    }
    if (child.exitCode !== null || Date.now() > deadline) throw new Error(`Metro did not start:\n${log}`);
    await sleep(500);
  }
  return () => {
    // Expo starts Metro as a child; end the whole group.
    try {
      process.kill(-child.pid!, "SIGTERM");
    } catch {
      // already gone
    }
  };
}

async function main() {
  const udid = simulator();
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  const d = await startMobileDeployment();
  let stopMetro = () => {};
  const agent = new Api(d.base).withToken(d.state.alice.token);
  try {
    const metro = await freePort();
    stopMetro = await startMetro(metro);
    const app = `exp://127.0.0.1:${metro}/--`;
    const open = (path: string) => execFileSync("xcrun", ["simctl", "openurl", udid, `${app}${path}`]);
    const host = new URL(d.base).host;

    console.log(`simulator ${udid}, deployment ${d.base}, screenshots in ${out}`);
    open(`/pair${new URL(d.pairing).search}`);
    await expectScreen(udid, "pair", [`登录到 ${host}`], 180_000);
    console.log(`  >>  tap 登录 in the simulator to pair`);
    const deadline = Date.now() + 180_000;
    while (!d.recorder.phone.some((r) => r.path === "/api/v1/auth/pairing/exchange" && r.status === 200)) {
      if (Date.now() > deadline) throw new Error("the pairing was not confirmed in the simulator within 3 minutes");
      await sleep(500);
    }

    await expectScreen(udid, "overview", ["需要处理", "运行中", "broken-import", "API_TOKEN", "daily-report"]);
    open(`/tasks/${d.state.tasks.dailyReport}`);
    await expectScreen(udid, "task", ["daily-report", "每个工作日 08:00", "生成 3 份报告", "v2"]);
    open(`/runs/${d.state.runs.daily}`);
    await expectScreen(udid, "run", ["执行完成", "获取数据", "分析", "发布", "publish report", "process exited with code 0"]);
    open(`/runs/${d.state.runs.broken}`);
    await expectScreen(udid, "failed-run", ["执行失败", "导入", "column price is missing"]);

    // A 100 000-line log opens on its tail, with earlier lines on request.
    open(`/runs/${d.state.runs.big}`);
    await expectScreen(udid, "big-run", ["执行完成", "输出", "更早", "line 99"]);

    // A run whose log retention removed: the record stays, the log says it
    // was cleared, and the phone does not ask for it (checked below).
    open(`/runs/${d.state.expiredRun}`);
    await expectScreen(udid, "expired-log", ["执行完成", "输出了 3 行", "日志已按保留策略清理"]);

    // A live run keeps updating by itself. The phone draws the newest 200
    // lines held and offers the rest as "earlier", so once the run has
    // printed more than that, that count grows as new lines arrive.
    const lines = async () =>
      (await agent.call<{ lines: { seq: number }[] }>(`/api/v1/runs/${d.liveRun}/logs?tail=1`)).lines.at(-1)?.seq ?? 0;
    while ((await lines()) < 260) await sleep(1_000);
    open(`/runs/${d.liveRun}`);
    const earlier = (text: string) => Number(/更早([\d,]+)行/.exec(text)?.[1]?.replace(/,/g, "") ?? -1);
    const first = earlier(await expectScreen(udid, "live-run", ["运行中", "计时", "更早", "tick"]));
    await sleep(4_000);
    const later = earlier(screen(udid, "live-run-later"));
    if (first < 0 || later <= first) throw new Error(`the live log did not move: ${first} earlier lines, then ${later}`);
    console.log(`  ok  live tail moves: ${first} → ${later} earlier lines`);

    // What the phone asked for, and that it changed nothing.
    const seen = new Set(d.recorder.phone.map((r) => `${r.method} ${r.path}`));
    for (const want of [
      "GET /api/v1/overview",
      `GET /api/v1/tasks/${d.state.tasks.dailyReport}`,
      `GET /api/v1/runs/${d.state.runs.daily}`,
      `GET /api/v1/runs/${d.state.runs.daily}/logs`,
      `GET /api/v1/runs/${d.liveRun}/logs/stream`,
    ]) {
      if (!seen.has(want)) throw new Error(`the phone never requested ${want}; it did: ${[...seen].join(", ")}`);
    }
    const expiredReads = d.recorder.phone.filter((r) => r.path.startsWith(`/api/v1/runs/${d.state.expiredRun}/logs`));
    if (expiredReads.length) throw new Error(`the phone read the removed log: ${expiredReads.map((r) => `${r.path} ${r.status}`).join(", ")}`);
    const writes = d.recorder.writes();
    if (writes.length) throw new Error(`the phone sent writes: ${writes.map((w) => `${w.method} ${w.path} ${w.status}`).join(", ")}`);
    console.log(`  ok  ${d.recorder.phone.length} phone requests, no writes`);
    console.log("A17 mobile flow passed");
  } finally {
    await agent.call(`/api/v1/runs/${d.liveRun}/cancel`, { method: "POST", body: {} }).catch(() => undefined);
    stopMetro();
    await d.stop();
  }
}

main().then(
  () => process.exit(0),
  (err: unknown) => {
    console.error(err instanceof Error ? err.message : err);
    process.exit(1);
  },
);
