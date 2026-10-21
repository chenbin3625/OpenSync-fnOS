import { defineConfig } from "@playwright/test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const baseURL = process.env.PLAYWRIGHT_BASE_URL || "http://127.0.0.1:3020";

// Every backend-mutating spec (engine/job/notify CRUD) writes to the same
// SQLite database the preview server uses unless it is redirected. A dedicated
// directory per run keeps the suite hermetic and leaves the developer's
// backend/data untouched.
const dataDir =
  process.env.OPENSYNC_E2E_DATA_DIR ||
  mkdtempSync(join(tmpdir(), "opensync-e2e-"));
process.once("exit", () => {
  if (!process.env.OPENSYNC_E2E_DATA_DIR)
    rmSync(dataDir, { recursive: true, force: true });
});

// Locally the suite runs against the installed Google Chrome; CI installs
// Playwright's bundled Chromium and sets PLAYWRIGHT_CHANNEL=chromium.
const channel =
  process.env.PLAYWRIGHT_CHANNEL || (process.env.CI ? undefined : "chrome");

export default defineConfig({
  testDir: "./e2e",
  workers: 1,
  // A retry in CI absorbs the few genuinely timing-sensitive UI assertions
  // without hiding functional failures: the report still shows the first run.
  retries: process.env.CI ? 1 : 0,
  use: {
    baseURL,
    channel,
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "desktop", use: { viewport: { width: 1100, height: 640 } } },
    { name: "wide-desktop", use: { viewport: { width: 1440, height: 900 } } },
    { name: "tablet", use: { viewport: { width: 768, height: 900 } } },
    {
      name: "mobile",
      use: { viewport: { width: 390, height: 844 }, isMobile: true },
    },
    {
      name: "small-mobile",
      use: { viewport: { width: 320, height: 740 }, isMobile: true },
    },
  ],
  // The suite drives a real Go backend, so it has to start one. Without this the
  // specs silently depended on a server someone had already launched by hand,
  // which is why they could never run in CI.
  //
  // The backend and Vite are started directly rather than through
  // scripts/dev.mjs: that script spawns both as detached process-group leaders
  // so it can signal them itself, which also means they survive the tree-kill
  // Playwright performs on its webServer, leaving the ports held after the run.
  //
  // PLAYWRIGHT_BASE_URL pointing elsewhere means the caller is deliberately
  // testing an already-running target, so nothing is started in that case.
  webServer: process.env.PLAYWRIGHT_BASE_URL
    ? undefined
    : [
        {
          // --dev binds loopback only and skips the fnOS gateway requirement.
          command: "go run ./cmd/server --dev --port 8040",
          cwd: "../backend",
          env: { OPENSYNC_DATA_DIR: dataDir },
          url: "http://127.0.0.1:8040/app/opensync/svr/session",
          // A first run compiles the whole Go module.
          timeout: 300_000,
          // Never adopt a server started for something else: it may run old
          // code or point at a different data directory.
          reuseExistingServer: false,
          stdout: "pipe",
          stderr: "pipe",
        },
        {
          command: "npm run dev",
          url: `${baseURL}/app/opensync/`,
          timeout: 120_000,
          reuseExistingServer: false,
          stdout: "pipe",
          stderr: "pipe",
        },
      ],
});
