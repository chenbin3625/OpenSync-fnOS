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
  await page.locator(".overview-card").getByRole("button", { name: "更多操作", exact: true }).click();
  await page.locator(".semi-dropdown-menu").getByText("设置", { exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("tab", { name: "同步与调度", exact: true }).click();
  const select = dialog.getByRole("combobox", { name: "同步方式", exact: true });
  await select.click();
  await page.getByRole("option", { name: /移动$/ }).click();
  await expect(select).toContainText("移动");
  expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
  await page.screenshot({ path: test.info().outputPath("move-mode.png"), fullPage: true });
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).not.toBeVisible();
  expect(saved).toMatchObject({ method: 2, dstPath: ["/Backup"], srcPath: ["/Source"] });
  await expect(page.locator(".overview-card").getByText("移动", { exact: true })).toBeVisible();
});
