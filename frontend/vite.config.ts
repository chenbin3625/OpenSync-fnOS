import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { writeFileSync } from "node:fs";

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
