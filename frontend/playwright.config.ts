import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:3010",
    channel: "chrome",
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
});
