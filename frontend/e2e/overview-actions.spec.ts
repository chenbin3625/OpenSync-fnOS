import { expect, test } from "@playwright/test";

test("overview switches the run action to stop while a task is active", async ({
  page,
}) => {
  const activeTask = {
    taskId: 71,
    scanFinish: true,
    createTime: 1,
    duration: 1,
    num: { wait: 0, running: 1, success: 0, fail: 0, other: 0 },
    size: { wait: 0, running: 1, success: 0, fail: 0, other: 0 },
    doneSize: 0,
    remainSize: 1,
    speed: 1,
    speedAvg: 1,
    remainTime: 1,
  };

  await page.route("**/app/opensync/svr/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && request.method() === "PUT")
      return success(null);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current"))
      return success(activeTask);
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "总览操作任务",
            srcPath: '["/src"]',
            dstPath: '["/dst"]',
            alistId: 1,
            useCacheT: 0,
            useCacheS: 0,
            method: 0,
            interval: 0,
            isCron: 2,
          },
        ],
        count: 1,
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  const overview = page.locator(".overview-card");
  const stopButton = overview.getByRole("button", {
    name: "停止任务",
    exact: true,
  });
  await expect(stopButton).toBeVisible();
  await expect(
    overview.getByRole("button", { name: "启动任务", exact: true }),
  ).toHaveCount(0);

  const stopRequest = page.waitForRequest(
    (request) =>
      request.url().endsWith("/svr/job") && request.method() === "PUT",
  );
  await stopButton.click();
  const dialog = page
    .getByRole("dialog")
    .filter({ hasText: "停止当前任务？" });
  await expect(dialog).toBeVisible();
  await dialog
    .getByRole("button", { name: "停止任务", exact: true })
    .click();
  expect((await stopRequest).postDataJSON()).toEqual({
    taskId: "71",
    action: "stop",
  });
});
