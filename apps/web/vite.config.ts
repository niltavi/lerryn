import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

// The favicon is the square logo from the design language; it is read from
// design/ at build time instead of keeping a copy in the app.
const FAVICON = fileURLToPath(new URL("../../design/brand/logo/svg/lerryn-logo-square.svg", import.meta.url));

function favicon(): Plugin {
  return {
    name: "lerryn-favicon",
    configureServer(server) {
      server.middlewares.use("/favicon.svg", (_req, res) => {
        res.setHeader("Content-Type", "image/svg+xml");
        res.end(readFileSync(FAVICON));
      });
    },
    generateBundle() {
      this.emitFile({ type: "asset", fileName: "favicon.svg", source: readFileSync(FAVICON) });
    },
  };
}

// `pnpm --filter @lerryn/web dev` proxies the API to a server the developer
// runs themselves (LERRYN_DEV_SERVER, default http://localhost:33333). The
// server rejects session writes whose Origin is not its public URL, so the
// proxy presents the target as the origin.
const target = process.env.LERRYN_DEV_SERVER ?? "http://localhost:33333";
const proxy = {
  target,
  changeOrigin: true,
  configure: (p: { on: (event: "proxyReq", fn: (req: { setHeader: (k: string, v: string) => void }) => void) => void }) => {
    p.on("proxyReq", (req) => req.setHeader("Origin", new URL(target).origin));
  },
};

export default defineConfig({
  plugins: [react(), tailwindcss(), favicon()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    // Hashed file names let the server cache assets for a year.
    assetsDir: "assets",
    sourcemap: false,
    // One bundle, loaded once and then cached as an immutable asset.
    chunkSizeWarningLimit: 800,
  },
  server: {
    port: 5173,
    proxy: { "/api": proxy, "/mcp": proxy },
  },
  test: {
    environment: "jsdom",
    globals: true,
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["../../packages/ui-web/src/test/setup.ts"],
  },
});
