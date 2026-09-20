import { test, expect } from "@playwright/test";

test("opens usable task workspace without a second login", async ({ page }) => {
  await page.goto("/app/opensync/tasks");
  await expect(page.getByRole("main")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "新建任务", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("密码", { exact: true })).toHaveCount(0);
  await expect(page.locator(".task-workspace")).toBeVisible();
});

test("opens task editor with step navigation", async ({ page }) => {
  await page.goto("/app/opensync/tasks");
  await page.getByRole("button", { name: "新建任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "新建任务", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "引擎与路径", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("当前步骤")).toHaveText("步骤 1 / 4");
  const dialogBox = await page.locator(".editor-modal .semi-modal").boundingBox();
  const viewport = page.viewportSize();
  if ((viewport?.width || 0) >= 1000) {
    expect(dialogBox?.width).toBeLessThanOrEqual(520);
  }
  await expect(
    page.getByRole("combobox", { name: "同步方式", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "下一步", exact: true }),
  ).toBeVisible();
  await expect(
    dialog.locator(".setting-row").filter({ hasText: "目标缓存" }),
  ).toHaveCSS("border-radius", "8px");
  const sourceCacheLabel = dialog
    .locator(".setting-row")
    .filter({ hasText: "源端缓存" })
    .locator(".setting-row-label");
  await expect(sourceCacheLabel).toHaveCSS("display", "flex");
  await expect(sourceCacheLabel).toHaveCSS("gap", "6px");
  const sourceCacheTipIcon = sourceCacheLabel.locator(".field-tip-icon");
  await expect(sourceCacheTipIcon).toHaveCSS("width", "16px");
  await expect(sourceCacheTipIcon).toHaveCSS("height", "16px");
  await expect(
    page.getByRole("button", { name: "下一步", exact: true }),
  ).toHaveCSS("height", "36px");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
});

test("four modules fit desktop and mobile windows", async ({ page }) => {
  for (const path of ["tasks", "engines", "notifications", "settings"]) {
    await page.goto(`/app/opensync/${path}`);
    await expect(page.locator(".page-toolbar")).toBeVisible();
    await expect(page.getByRole("button", { name: /刷新/ })).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  }
});
