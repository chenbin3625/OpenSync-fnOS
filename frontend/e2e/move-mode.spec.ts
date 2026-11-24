import { test, expect } from "@playwright/test";

test("selects and saves verified move mode in an existing task", async ({ page }) => {
  let method = 0;
  let saved: Record<string, unknown> | undefined;
  await page.route("**/app/opensync/svr/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const success = (data: unknown) => route.fulfill({
      contentType: "application/json", body: JSON.stringify({ code: 200, data, msg: "" }),
    });
    if (url.pathname.endsWith("/session")) return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) return success([]);
    if (url.pathname.endsWith("/alist")) return success([
      { id: 1, remark: "测试引擎", url: "https://engine.test", userName: "test" },
    ]);
    if (url.pathname.endsWith("/job") && ["POST", "PUT"].includes(request.method())) {
      saved = request.postDataJSON();
      method = Number(saved?.method);
      return success(null);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("current")) return success(null);
    if (url.pathname.endsWith("/job")) return success({
      dataList: [{ id: 9, enable: 1, remark: "文件移动任务", alistId: 1,
        srcPath: '["/Source"]', dstPath: '["/Backup"]', method, isCron: 2, exclude: "" }], count: 1,
    });
    return success(null);
  });
  await page.goto("/app/opensync/tasks?jobId=9");
  await page.locator(".overview-card").getByRole("button", { name: "编辑任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  // 同步方式已前置到「基础与路径」首屏，无需切 Tab
  const select = dialog.getByRole("combobox", { name: "同步方式", exact: true });
  await select.click();
  await page.getByRole("option", { name: /移动$/ }).click();
  await expect(select).toContainText("移动");
  await expect(dialog.locator(".sync-method-banner.is-warning")).toContainText("删除源文件");
  expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  await page.screenshot({ path: test.info().outputPath("move-mode.png"), fullPage: true });
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(saved).toMatchObject({ method: 2, dstPath: ["/Backup"], srcPath: ["/Source"] });
  await expect(page.locator(".overview-card").getByText("移动", { exact: true })).toBeVisible();
});

test("offers one-click fix when switching a multi-destination task to move mode", async ({ page }) => {
  let saved: Record<string, unknown> | undefined;
  await page.route("**/app/opensync/svr/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const success = (data: unknown) => route.fulfill({
      contentType: "application/json", body: JSON.stringify({ code: 200, data, msg: "" }),
    });
    if (url.pathname.endsWith("/session")) return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) return success([]);
    if (url.pathname.endsWith("/alist")) return success([
      { id: 1, remark: "测试引擎", url: "https://engine.test", userName: "test" },
    ]);
    if (url.pathname.endsWith("/job") && request.method() === "POST") {
      saved = request.postDataJSON();
      return success(null);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("current")) return success(null);
    if (url.pathname.endsWith("/job")) return success({
      dataList: [{ id: 10, enable: 1, remark: "多目标任务", alistId: 1,
        srcPath: '["/Source"]', dstPath: '["/Backup1","/Backup2"]', method: 0, isCron: 2, exclude: "" }], count: 1,
    });
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=10");
  await page.locator(".overview-card").getByRole("button", { name: "编辑任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  const select = dialog.getByRole("combobox", { name: "同步方式", exact: true });
  await select.click();
  await page.getByRole("option", { name: /移动$/ }).click();

  const preview = dialog.getByRole("region", { name: "同步位置预览" });
  await expect(preview.getByRole("alert")).toContainText("移动模式只允许一个目标目录");
  await preview.getByRole("button", { name: "仅保留首个目标目录", exact: true }).click();
  await expect(preview.getByRole("alert")).toHaveCount(0);

  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(saved).toMatchObject({ method: 2, dstPath: ["/Backup1"] });
});
