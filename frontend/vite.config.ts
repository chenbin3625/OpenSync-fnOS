import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { rmSync, writeFileSync } from "node:fs";

export default defineConfig({
  base: "/app/opensync/",
  plugins: [
    react(),
    {
      name: "go-embed-placeholder",
      closeBundle() {
        writeFileSync(
          new URL("../backend/cmd/server/web/.gitkeep", import.meta.url),
          "",
        );
        // public/ is copied wholesale, so the MSW service worker would ship
        // inside the go:embed payload. Only dev serves it, and a mock
        // interceptor has no business being installable in a release build.
        if (process.env.VITE_DATA_MODE !== "mock") {
          rmSync(
            new URL(
              "../backend/cmd/server/web/mockServiceWorker.js",
              import.meta.url,
            ),
            { force: true },
          );
        }
      },
    },
  ],
  server: {
    port: 3020,
    strictPort: true,
    proxy: {
      "/app/opensync/svr": {
        target: "http://127.0.0.1:8040",
        changeOrigin: false,
      },
    },
  },
  build: { outDir: "../backend/cmd/server/web", emptyOutDir: true },
  test: { include: ["tests/**/*.test.ts"] },
});
