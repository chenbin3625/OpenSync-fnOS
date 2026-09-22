import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { rmSync, writeFileSync } from "node:fs";

const embedDir = new URL("../backend/cmd/server/web/", import.meta.url);
const gitkeep = new URL(".gitkeep", embedDir);

export default defineConfig({
  base: "/app/opensync/",
  plugins: [
    react(),
    {
      name: "go-embed-placeholder",
      // `emptyOutDir` deletes everything in the Go embed directory, including
      // the tracked .gitkeep. `go:embed all:web` fails to compile against an
      // empty directory, so a build interrupted after the wipe used to leave
      // the backend unbuildable until the file was restored by hand.
      //
      // Vite empties outDir in renderStart with order "pre"; restoring the
      // placeholder in a "post" handler of the same hook closes that window
      // instead of waiting for closeBundle, which never runs on a failed build.
      renderStart: {
        order: "post",
        handler() {
          writeFileSync(gitkeep, "");
        },
      },
      closeBundle() {
        writeFileSync(gitkeep, "");
        // public/ is copied wholesale, so the MSW service worker would ship
        // inside the go:embed payload. Only dev serves it, and a mock
        // interceptor has no business being installable in a release build.
        if (process.env.VITE_DATA_MODE !== "mock") {
          rmSync(new URL("mockServiceWorker.js", embedDir), { force: true });
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
