import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdtempSync, openSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

/** The repository root, from apps/web/e2e/support. */
export const repoRoot = resolve(import.meta.dirname, "../../../..");

export const ADMIN = { username: "e2e-admin", password: "e2e admin password" };

/**
 * Builds the lerryn binary with the console currently in
 * server/internal/console/webdist (`make web` puts it there), unless
 * LERRYN_E2E_BINARY points at one already built.
 */
export function buildBinary(dir: string): string {
  const given = process.env.LERRYN_E2E_BINARY;
  if (given) return resolve(given);
  if (!existsSync(join(repoRoot, "server/internal/console/webdist/index.html"))) {
    throw new Error("server/internal/console/webdist is empty; run `make web` first (make e2e-web does)");
  }
  const out = join(dir, "lerryn");
  execFileSync("go", ["build", "-o", out, "./server/cmd/lerryn"], {
    cwd: repoRoot,
    stdio: "inherit",
    env: { ...process.env, CGO_ENABLED: "0" },
  });
  return out;
}

export async function freePort(): Promise<number> {
  return new Promise((ok, fail) => {
    const srv = createServer();
    srv.once("error", fail);
    srv.listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      srv.close(() => (typeof addr === "object" && addr ? ok(addr.port) : fail(new Error("no port"))));
    });
  });
}

export interface RunningServer {
  /** The public URL. */
  base: string;
  /** Where the server listens; the same as base without a proxy. */
  listen: string;
  dir: string;
  logFile: string;
  stop: () => Promise<void>;
  /** Stops the process, runs whileDown (the database is free then) and starts it again on the same data. */
  restart: (whileDown?: () => void | Promise<void>) => Promise<void>;
}

/**
 * Starts `lerryn all-in-one` on its own temporary directories: SQLite, the
 * local object store, the embedded runner on the Docker daemon from the
 * environment, and a first admin from auth.initial_admin.
 * LERRYN_TEST_MOUNT_ROOT places the directory where the daemon can mount
 * it, as for the Go integration tests.
 */
export async function startServer(options: { publicUrl?: string } = {}): Promise<RunningServer> {
  const dir = mkdtempSync(join(process.env.LERRYN_TEST_MOUNT_ROOT || tmpdir(), "lerryn-e2e-"));
  const binary = buildBinary(dir);
  const port = await freePort();
  const listen = `http://127.0.0.1:${port}`;
  // A proxy in front of the server (the mobile suite records requests) is the public address.
  const base = options.publicUrl ?? listen;
  const adminPassword = join(dir, "admin-password");
  writeFileSync(adminPassword, ADMIN.password, { mode: 0o600 });

  const env: NodeJS.ProcessEnv = {
    ...process.env,
    LERRYN_SERVER_LISTEN: `127.0.0.1:${port}`,
    LERRYN_SERVER_PUBLIC_URL: base,
    LERRYN_SERVER_REQUIRE_TEST_BEFORE_ACTIVATE: "false",
    LERRYN_DATABASE_DRIVER: "sqlite",
    LERRYN_DATABASE_DSN: join(dir, "lerryn.db"),
    LERRYN_STORAGE_DRIVER: "local",
    LERRYN_STORAGE_PATH: join(dir, "storage"),
    LERRYN_SECRETS_MASTER_KEY_FILE: join(dir, "master.key"),
    LERRYN_RUNNER_DATA_DIR: join(dir, "runner"),
    LERRYN_RUNNER_SLOTS: "4",
    LERRYN_AUTH_INITIAL_ADMIN_USERNAME: ADMIN.username,
    LERRYN_AUTH_INITIAL_ADMIN_PASSWORD_FILE: adminPassword,
    LERRYN_LOG_LEVEL: "warn",
    LERRYN_LOG_FORMAT: "text",
    // Notification history is seeded against a test-owned loopback receiver.
    LERRYN_NOTIFY_ALLOWED_NETWORKS: "127.0.0.1/32",
  };
  if (process.env.LERRYN_TEST_PYTHON_IMAGE) env.LERRYN_RUNNER_PYTHON_IMAGE = process.env.LERRYN_TEST_PYTHON_IMAGE;

  // Logs of ended runs expire after 7 days, so the seed can age one run's
  // log past that and the leader removes it (see seed.ts).
  env.LERRYN_RETENTION_LOGS_DAYS = "7";

  const logFile = join(dir, "server.log");
  let child: ChildProcess;
  let exited: number | null | undefined;
  const launch = async () => {
    const out = openSync(logFile, "a");
    exited = undefined;
    child = spawn(binary, ["all-in-one"], { env, stdio: ["ignore", out, out] });
    child.on("exit", (code) => (exited = code));
    const deadline = Date.now() + 60_000;
    for (;;) {
      if (exited !== undefined) throw new Error(`lerryn exited during startup (${exited}):\n${readFileSync(logFile, "utf8")}`);
      try {
        const r = await fetch(`${listen}/readyz`);
        if (r.ok) return;
      } catch {
        // not listening yet
      }
      if (Date.now() > deadline) throw new Error(`lerryn did not become ready:\n${readFileSync(logFile, "utf8")}`);
      await new Promise((r) => setTimeout(r, 100));
    }
  };
  const halt = async () => {
    if (exited !== undefined) return;
    const done = new Promise<void>((r) => child.once("exit", () => r()));
    child.kill("SIGINT");
    const timer = setTimeout(() => child.kill("SIGKILL"), 60_000);
    await done;
    clearTimeout(timer);
  };
  await launch();

  const stop = async () => {
    await halt();
    removeRunnerContainers(join(dir, "runner"));
    if (!process.env.LERRYN_E2E_KEEP) rmSync(dir, { recursive: true, force: true });
  };
  const restart = async (whileDown?: () => void | Promise<void>) => {
    await halt();
    await whileDown?.();
    await launch();
  };
  return { base, listen, dir, logFile, stop, restart };
}

/** Removes containers the embedded runner left, found by its runner ID label. */
export function removeRunnerContainers(runnerDir: string) {
  let id: string;
  try {
    id = readFileSync(join(runnerDir, "runner-id"), "utf8").trim();
  } catch {
    return;
  }
  try {
    const ids = execFileSync("docker", ["ps", "-aq", "--filter", `label=lerryn.runner_id=${id}`], { encoding: "utf8" })
      .split(/\s+/)
      .filter(Boolean);
    if (ids.length) execFileSync("docker", ["rm", "-f", ...ids], { stdio: "ignore" });
  } catch {
    // Docker is gone; nothing to clean.
  }
}
