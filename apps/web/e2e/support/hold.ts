// Starts and seeds the e2e deployment and keeps it until Ctrl-C, for
// writing specs without paying for the seed on every run:
//
//   node apps/web/e2e/support/hold.ts
//   LERRYN_E2E_STATE=<printed path> pnpm --filter @lerryn/web exec playwright test
//
// Specs add data of their own, so a held deployment drifts; the suite proper
// always starts from a fresh one.
import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { seed } from "./seed.ts";
import { startServer } from "./server.ts";

const server = await startServer();
let stopping = false;
const stop = async () => {
  if (stopping) return;
  stopping = true;
  await server.stop();
  process.exit(0);
};
process.on("SIGINT", () => void stop());
process.on("SIGTERM", () => void stop());
try {
  const state = await seed(server);
  const file = join(server.dir, "e2e-state.json");
  writeFileSync(file, JSON.stringify(state, null, 2));
  console.log(`seeded ${server.base}\nLERRYN_E2E_STATE=${file}`);
} catch (err) {
  console.error(err, `\nserver log: ${server.logFile}`);
  await stop();
}
