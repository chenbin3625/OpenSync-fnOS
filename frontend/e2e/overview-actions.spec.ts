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

test("prioritizes the realtime tab when entering or switching to a running task", async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 88,
    scanFinish: true,
    createTime: 1,
    duration: 12,
    num: { wait: 0, running: 1, success: 2, fail: 0, other: 0 },
    size: { wait: 0, running: 1024, success: 2048, fail: 0, other: 0 },
    doneSize: 2048,
    remainSize: 1024,
    speed: 512,
    speedAvg: 512,
    remainTime: 2,
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
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      const id = url.searchParams.get("id");
      if (id === "2") {
        if (url.searchParams.has("status")) {
          return success({
            taskId: activeTask.taskId,
            createTime: activeTask.createTime,
            status: Number(url.searchParams.get("status")),
            pageNum: Number(url.searchParams.get("pageNum") || 1),
            pageSize: Number(url.searchParams.get("pageSize") || 10),
            stale: false,
            dataList: [],
            count: 0,
          });
        }
        return success(activeTask);
      }
      return success(null);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "空闲任务",
            srcPath: '["/src1"]',
            dstPath: '["/dst1"]',
            alistId: 1,
            useCacheT: 0,
            useCacheS: 0,
            method: 0,
            interval: 0,
            isCron: 2,
            running: false,
          },
          {
            id: 2,
            enable: 1,
            remark: "运行中任务",
            srcPath: '["/src2"]',
            dstPath: '["/dst2"]',
            alistId: 1,
            useCacheT: 0,
            useCacheS: 0,
            method: 0,
            interval: 0,
            isCron: 2,
            running: true,
          },
        ],
        count: 2,
      });
    }
    return success(null);
  });

  // 1. Direct entry to a running task without ?tab= defaults to realtime.
  await page.goto("/app/opensync/tasks?jobId=2");
  const toolbar = page.locator(".page-toolbar");
  await expect(
    toolbar.getByRole("tab", { name: "实时任务", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/jobId=2&tab=realtime$/);

  // 2. Manual switch to overview stays on overview even while running.
  await toolbar.getByRole("tab", { name: "总览", exact: true }).click();
  await expect(
    toolbar.getByRole("tab", { name: "总览", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/jobId=2&tab=overview$/);

  // 3. Starting from idle job on overview, switching to running job prioritizes realtime.
  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  await expect(
    toolbar.getByRole("tab", { name: "总览", exact: true }),
  ).toHaveAttribute("aria-selected", "true");

  const isMobile = (page.viewportSize()?.width || 0) <= 640;
  if (isMobile) {
    await page
      .locator(".mobile-job-bar")
      .getByRole("tab", { name: "运行中任务", exact: true })
      .click();
  } else {
    await page
      .locator(".app-sidebar")
      .getByRole("link", { name: "运行中任务", exact: true })
      .click();
  }
  await expect(
    toolbar.getByRole("tab", { name: "实时任务", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/jobId=2&tab=realtime$/);
});
