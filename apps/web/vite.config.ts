import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

// Read from the canonical sources and keep guide URLs stable across releases.
const STATIC_FILES = [
  ["favicon.svg", "../../design/brand/logo/svg/lerryn-icon-circle.svg", "image/svg+xml"],
  ["docs/en/tools.md", "../../docs/en/tools.md", "text/markdown; charset=utf-8"],
  ["docs/zh-CN/tools.md", "../../docs/zh-CN/tools.md", "text/markdown; charset=utf-8"],
] as const;

function staticFiles(): Plugin {
  return {
    name: "lerryn-static-files",
    configureServer(server) {
      for (const [fileName, source, contentType] of STATIC_FILES) {
        server.middlewares.use(`/${fileName}`, (_req, res) => {
          res.setHeader("Content-Type", contentType);
          res.end(readFileSync(fileURLToPath(new URL(source, import.meta.url))));
        });
      }
    },
    generateBundle() {
      for (const [fileName, source] of STATIC_FILES) {
        this.emitFile({ type: "asset", fileName, source: readFileSync(fileURLToPath(new URL(source, import.meta.url))) });
      }
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
  plugins: [react(), tailwindcss(), staticFiles()],
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
