import { writeFileSync } from "node:fs";
import { join } from "node:path";

import { seed } from "./support/seed.ts";
import { startServer } from "./support/server.ts";

/**
 * Builds and starts a real all-in-one, seeds it through the API and hands
 * the specs its address and IDs through a state file. The returned
 * function stops the server and removes its containers and directory.
 *
 * With LERRYN_E2E_STATE already set (see e2e/support/hold.ts), the specs
 * run against that seeded server instead and nothing is started here.
 */
export default async function globalSetup() {
  if (process.env.LERRYN_E2E_STATE) return;
  const server = await startServer();
  try {
    const state = await seed(server);
    const file = join(server.dir, "e2e-state.json");
    writeFileSync(file, JSON.stringify(state, null, 2));
    process.env.LERRYN_E2E_STATE = file;
  } catch (err) {
    console.error(`seeding failed; server log: ${server.logFile}`);
    await server.stop();
    throw err;
  }
  return async () => {
    if (process.env.LERRYN_E2E_KEEP) console.log(`kept ${server.dir} (server log: ${server.logFile})`);
    await server.stop();
  };
}
