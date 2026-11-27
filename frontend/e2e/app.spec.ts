import { test, expect } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  // The app shell checks the latest release on mount, and the real backend
  // answers that by calling GitHub. Stub it so this smoke suite passes offline
  // and cannot be slowed down by an unreachable upstream.
  await page.route("**/app/opensync/svr/version/latest*", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        code: 200,
        data: {
          latestVersion: "v0.0.0",
          releaseURL: "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest",
          hasUpdate: false,
        },
        msg: "success",
      }),
    }),
  );
});

test("opens dashboard by default at root path", async ({ page }) => {
  await page.goto("/app/opensync/");
  await expect(page).toHaveURL(/\/app\/opensync\/dashboard$/);
  await expect(page.getByRole("heading", { name: "总览看板", exact: true })).toBeVisible();
});

test("opens usable task workspace without a second login", async ({ page }) => {
  await page.goto("/app/opensync/tasks");
  await expect(page.getByRole("main")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "返回任务列表", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("密码", { exact: true })).toHaveCount(0);
  await expect(page.locator(".task-workspace")).toBeVisible();
});

test("opens task editor with step navigation", async ({ page }) => {
  await page.goto("/app/opensync/sync");
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "新建持续同步任务", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "选择同步文件夹", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("当前步骤")).toHaveText("步骤 1 / 3");
  const viewport = page.viewportSize();
  const wizardBox = await page.locator(".editor-modal .semi-modal").boundingBox();
  if ((viewport?.width || 0) >= 1000) {
    expect(wizardBox?.width).toBeGreaterThanOrEqual(700);
    expect(wizardBox?.width).toBeLessThanOrEqual(780);
  }
  await expect(
    page.getByRole("button", { name: "下一步", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "下一步", exact: true }),
  ).toHaveCSS("height", "36px");

  // Switching to Free Mode preserves existing advanced dual-direction UI & 620px modal width
  await dialog
    .getByRole("button", { name: "切换为自由多对多 / 云对云模式", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "基础与路径", exact: true }),
  ).toBeVisible();
  const freeBox = await page.locator(".editor-modal .semi-modal").boundingBox();
  if ((viewport?.width || 0) >= 1000) {
    expect(freeBox?.width).toBeGreaterThanOrEqual(560);
    expect(freeBox?.width).toBeLessThanOrEqual(620);
  }
  const directions = dialog.getByRole("radiogroup", { name: "同步方向", exact: true });
  await expect(directions.getByRole("radio")).toHaveCount(2);
  await expect(directions.getByRole("radio", { name: /^单向同步/, checked: true })).toBeVisible();
  await expect(directions.getByRole("radio", { name: /^双向同步/, checked: false })).toBeVisible();
  await expect(dialog.locator(".sync-method-banner")).toContainText(
    "不会删除目标端已有文件",
  );
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
});

test("four modules fit desktop and mobile windows", async ({ page }) => {
  for (const path of ["tasks", "engines", "notifications", "settings"]) {
    await page.goto(`/app/opensync/${path}`);
    await expect(page.locator(".page-toolbar")).toBeVisible();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  }
});
