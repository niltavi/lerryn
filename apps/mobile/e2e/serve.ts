// The deployment for the mobile flows: a real all-in-one seeded like the
// web suite, behind a proxy that records what the phone asks for. It
// prints a pairing link for the simulator and keeps running until Ctrl-C,
// then lists the phone's requests and exits non-zero if any would have
// changed something.
//
//   node apps/mobile/e2e/serve.ts
import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { Api, login } from "../../web/e2e/support/api.ts";
import { seed } from "../../web/e2e/support/seed.ts";
import { freePort, startServer } from "../../web/e2e/support/server.ts";
import { Recorder } from "./recorder.ts";

export interface MobileDeployment {
  base: string;
  pairing: string;
  recorder: Recorder;
  state: Awaited<ReturnType<typeof seed>>;
  /** A run that keeps printing, for the live log tail. */
  liveRun: string;
  stop: () => Promise<void>;
}

export async function startMobileDeployment(): Promise<MobileDeployment> {
  const proxyPort = await freePort();
  const base = `http://127.0.0.1:${proxyPort}`;
  const server = await startServer({ publicUrl: base });
  const recorder = new Recorder(server.listen);
  await recorder.listen(proxyPort);
  const stop = async () => {
    await recorder.close();
    await server.stop();
  };
  try {
    const state = await seed(server);
    const agent = new Api(base).withToken(state.alice.token);
    const live = await agent.call<{ id: string }>(`/api/v1/tasks/${state.tasks.ticker}/runs`, { method: "POST", body: {}, want: 202 });
    const web = new Api(base).withCookie(await login(base, state.alice.username, state.alice.password));
    const code = await web.call<{ uri: string }>("/api/v1/auth/pairing", { method: "POST", want: 201 });
    writeFileSync(join(server.dir, "mobile-state.json"), JSON.stringify({ ...state, pairing: code.uri, liveRun: live.id }, null, 2));
    return { base, pairing: code.uri, recorder, state, liveRun: live.id, stop };
  } catch (err) {
    console.error(`server log: ${server.logFile}`);
    await stop();
    throw err;
  }
}

if (import.meta.main) {
  const d = await startMobileDeployment();
  console.log(
    [
      `deployment ${d.base} (alice / ${d.state.alice.password})`,
      `pairing link (valid 5 minutes): ${d.pairing}`,
      `Expo Go: xcrun simctl openurl booted "exp://127.0.0.1:8081/--/pair?${new URL(d.pairing).search.slice(1)}"`,
      `runs: daily ${d.state.runs.daily}, broken ${d.state.runs.broken}, big ${d.state.runs.big}, live ${d.liveRun}`,
      "Ctrl-C to stop and list the phone's requests",
    ].join("\n"),
  );
  const finish = async () => {
    const writes = d.recorder.writes();
    console.log(`\nphone requests: ${d.recorder.phone.length}`);
    for (const r of d.recorder.phone) console.log(`  ${r.method} ${r.path} ${r.status}`);
    console.log(writes.length ? `WRITES: ${writes.map((w) => `${w.method} ${w.path}`).join(", ")}` : "no writes");
    await d.stop();
    process.exit(writes.length ? 1 : 0);
  };
  process.on("SIGINT", () => void finish());
  process.on("SIGTERM", () => void finish());
}
