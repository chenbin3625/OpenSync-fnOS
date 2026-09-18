import { test, expect } from "@playwright/test";

test("opens usable task workspace without a second login", async ({ page }) => {
  await page.goto("/app/opensync/tasks");
  await expect(page.getByRole("main")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "新建任务", exact: true }),
  ).toBeVisible();
  await expect(page.getByText("密码", { exact: true })).toHaveCount(0);
  await expect(page.getByText("空空如也", { exact: true })).toBeVisible();
});

test("opens task editor as a step-by-step wizard", async ({ page }) => {
  await page.goto("/app/opensync/tasks");
  await page.getByRole("button", { name: "新建任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "新建任务", exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("当前步骤")).toHaveText(/步骤\s*1\s*\/\s*4/);
  const dialogBox = await page.locator(".editor-modal .semi-modal").boundingBox();
  const viewport = page.viewportSize();
  if ((viewport?.width || 0) >= 1000) {
    expect(dialogBox?.width).toBeLessThanOrEqual(520);
  }
  await expect(
    page.getByRole("heading", { name: "引擎与路径", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("combobox", { name: "同步方式", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "下一步", exact: true }),
  ).toBeVisible();
  await expect(
    dialog.locator(".setting-row").filter({ hasText: "目标缓存" }),
  ).toHaveCSS("border-radius", "8px");
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
    await expect(page.locator("main h1")).toHaveCount(0);
    await expect(page.getByRole("button", { name: /刷新/ })).toHaveCount(0);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  }
});

test("local storage cannot bypass the existing engine dependency", async ({
  page,
}) => {
  await page.goto("/app/opensync/engines?view=local");
  await expect(
    page.getByRole("button", { name: "授权目录", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "直接同步" })).toHaveCount(0);
});
