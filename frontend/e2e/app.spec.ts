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

test("opens and cancels task editor with pinned actions", async ({ page }) => {
  await page.goto("/app/opensync/tasks");
  await page.getByRole("button", { name: "新建任务", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "保存任务配置" }),
  ).toBeVisible();
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
