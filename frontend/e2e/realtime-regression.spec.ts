import { expect, test } from "@playwright/test";

function realtimePage(
  url: URL,
  task: { taskId: number; createTime: number },
  dataList: Record<string, unknown>[],
  count = dataList.length,
) {
  return {
    taskId: task.taskId,
    createTime: task.createTime,
    status: Number(url.searchParams.get("status")),
    pageNum: Number(url.searchParams.get("pageNum") || 1),
    pageSize: Number(url.searchParams.get("pageSize") || 10),
    stale: false,
    dataList,
    count,
  };
}

test("realtime view loads the initial active task snapshot", async ({ page }) => {
  test.setTimeout(15000);
  const errors: Error[] = [];
  page.on("pageerror", (error) => errors.push(error));
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 7,
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
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "回归任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await expect(page.getByRole("heading", { name: "正在同步" })).toBeVisible();
  const stopButton = page.getByRole("button", { name: "停止任务", exact: true });
  await expect(stopButton).toHaveCSS("height", "28px");
  await stopButton.click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  const dialogBox = await dialog.boundingBox();
  const viewport = page.viewportSize()!;
  expect(
    Math.abs(dialogBox!.x + dialogBox!.width / 2 - viewport.width / 2),
  ).toBeLessThanOrEqual(1);
  expect(
    Math.abs(dialogBox!.y + dialogBox!.height / 2 - viewport.height / 2),
  ).toBeLessThanOrEqual(1);
  expect(errors).toEqual([]);
});

test("realtime view loads running rows when the live snapshot is missing", async ({ page }) => {
  test.setTimeout(15000);
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 8,
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
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.get("status") === "1") {
        return success(
          realtimePage(url, activeTask, [
            {
              id: 10,
              fileName: "mobile-running-file.txt",
              srcPath: "/src",
              dstPath: "/dst",
              fileSize: 1024,
              type: 0,
              status: 1,
              progress: 42,
            },
          ]),
        );
      }
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "手机实时任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await expect(page.getByRole("heading", { name: "正在同步" })).toBeVisible({
    timeout: 10000,
  });

  const visibleList = page.locator(".desktop-data:visible, .mobile-data:visible").last();
  await expect(visibleList.getByText("mobile-running-file.txt")).toBeVisible();
  await expect(visibleList.getByText("42%")).toBeVisible();
});

test("mobile realtime view loads the initial status snapshot", async ({
  page,
}) => {
  test.skip(
    (page.viewportSize()?.width || 0) > 640,
    "mobile layout regression only",
  );
  const activeTask = {
    taskId: 21,
    scanFinish: true,
    createTime: 1,
    duration: 1,
    num: { wait: 0, running: 1, success: 3, fail: 0, other: 0 },
    size: { wait: 0, running: 1, success: 3, fail: 0, other: 0 },
    doneSize: 3,
    remainSize: 1,
    speed: 1,
    speedAvg: 1,
    remainTime: 1,
  };

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.has("status")) {
        return success(realtimePage(url, activeTask, []));
      }
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "静默流任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await expect(page.getByRole("tab", { name: "成功 3" })).toBeVisible({
    timeout: 2500,
  });
});

test("mobile realtime pagination stays at the bottom with short content", async ({
  page,
}) => {
  test.skip(
    (page.viewportSize()?.width || 0) > 640,
    "mobile layout regression only",
  );
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 22,
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
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.get("status") === "1") {
        return success(
          realtimePage(url, activeTask, [
            {
              id: 220,
              fileName: "bottom-pager-row.txt",
              srcPath: "/src",
              dstPath: "/dst",
              fileSize: 1024,
              type: 0,
              status: 1,
              progress: 12,
            },
          ]),
        );
      }
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "分页贴底任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  const mobileList = page.locator(".mobile-data").last();
  await expect(mobileList.getByText("bottom-pager-row.txt")).toBeVisible();

  const layout = await page.evaluate(() => {
    const workspace = document.querySelector<HTMLElement>(".task-workspace")!;
    const pager = document.querySelector<HTMLElement>(
      ".execution-view .table-pagination",
    )!;
    const workspaceBox = workspace.getBoundingClientRect();
    const pagerBox = pager.getBoundingClientRect();
    return {
      workspaceBottom: workspaceBox.bottom,
      pagerBottom: pagerBox.bottom,
    };
  });

  expect(Math.abs(layout.workspaceBottom - layout.pagerBottom)).toBeLessThan(3);
});

test("slow realtime status request completes without cancellation", async ({
  page,
}) => {
  test.setTimeout(12000);
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 17,
    scanFinish: true,
    createTime: 1,
    duration: 1,
    num: { wait: 0, running: 0, success: 21, fail: 0, other: 0 },
    size: { wait: 0, running: 0, success: 21, fail: 0, other: 0 },
    doneSize: 21,
    remainSize: 0,
    speed: 0,
    speedAvg: 0,
    remainTime: 0,
  };
  let successRequests = 0;
  let cancelledRequests = 0;
  page.on("requestfailed", (request) => {
    const url = new URL(request.url());
    if (url.searchParams.get("status") === "2") cancelledRequests += 1;
  });

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (
      url.pathname.endsWith("/job") &&
      url.searchParams.get("current") &&
      url.searchParams.get("status") === "2"
    ) {
      successRequests += 1;
      await new Promise((resolve) => setTimeout(resolve, 3500));
      return success(
        realtimePage(
          url,
          activeTask,
          [
          { id: 171, fileName: "slow-success.txt", status: 2, type: 0 },
          ],
          21,
        ),
      );
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.has("status"))
        return success(realtimePage(url, activeTask, []));
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "慢查询任务",
            srcPath: '["/src"]',
            dstPath: '["/dst"]',
            alistId: 1,
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await page.getByRole("tab", { name: /成功/ }).click();
  const visibleList = page
    .locator(".desktop-data:visible, .mobile-data:visible")
    .last();
  await expect(visibleList.getByText("slow-success.txt")).toBeVisible({
    timeout: 8000,
  });
  expect(successRequests).toBe(1);
  expect(cancelledRequests).toBe(0);
});

test("realtime status load failure can be retried", async ({ page }) => {
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 19,
    scanFinish: true,
    createTime: 1,
    duration: 1,
    num: { wait: 0, running: 0, success: 1, fail: 0, other: 0 },
    size: { wait: 0, running: 0, success: 1, fail: 0, other: 0 },
    doneSize: 1,
    remainSize: 0,
    speed: 0,
    speedAvg: 0,
    remainTime: 0,
  };
  let successRequests = 0;

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (
      url.pathname.endsWith("/job") &&
      url.searchParams.get("current") &&
      url.searchParams.get("status") === "2"
    ) {
      successRequests += 1;
      if (successRequests === 1) {
        return route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({
            code: 503,
            data: null,
            msg: "实时明细暂时不可用",
          }),
        });
      }
      return success(
        realtimePage(url, activeTask, [
          { id: 191, fileName: "retry-success.txt", status: 2, type: 0 },
        ]),
      );
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.has("status"))
        return success(realtimePage(url, activeTask, []));
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "失败重试任务",
            srcPath: '["/src"]',
            dstPath: '["/dst"]',
            alistId: 1,
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await page.getByRole("tab", { name: /成功/ }).click();
  await expect(page.getByText("实时明细暂时不可用")).toBeVisible();
  await page.getByRole("button", { name: "重试", exact: true }).click();
  const visibleList = page
    .locator(".desktop-data:visible, .mobile-data:visible")
    .last();
  await expect(visibleList.getByText("retry-success.txt")).toBeVisible();
  expect(successRequests).toBe(2);
});

test("realtime detail refreshes without changing the current view", async ({
  page,
}) => {
  test.setTimeout(15000);
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 23,
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
  let detailRequests = 0;

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.has("status")) {
        detailRequests += 1;
        return success(
          realtimePage(url, activeTask, [
            {
              id: 230 + detailRequests,
              fileName: `detail-version-${detailRequests}.txt`,
              srcPath: "/src",
              dstPath: "/dst",
              fileSize: 1024,
              type: 0,
              status: 1,
              progress: detailRequests,
            },
          ]),
        );
      }
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "定期刷新的实时任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  const realtimeTable = page
    .locator(".desktop-data:visible, .mobile-data:visible")
    .last();
  await expect(realtimeTable.getByText("detail-version-1.txt")).toBeVisible();

  await expect(realtimeTable.getByText("detail-version-2.txt")).toBeVisible();
  await expect(realtimeTable.getByText("detail-version-1.txt")).toHaveCount(0);
  expect(detailRequests).toBe(2);
});

test("realtime detail refresh keeps the selected page", async ({ page }) => {
  test.setTimeout(15000);
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 25,
    scanFinish: true,
    createTime: 1,
    duration: 1,
    num: { wait: 0, running: 12, success: 0, fail: 0, other: 0 },
    size: { wait: 0, running: 12, success: 0, fail: 0, other: 0 },
    doneSize: 0,
    remainSize: 12,
    speed: 1,
    speedAvg: 1,
    remainTime: 12,
  };
  const pageRequestsAfterPageTwo: string[] = [];
  let pageTwoVisible = false;
  let pageTwoRequests = 0;

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.has("status")) {
        const pageNum = url.searchParams.get("pageNum") || "1";
        if (pageTwoVisible) pageRequestsAfterPageTwo.push(pageNum);
        const version =
          pageNum === "2" ? String(++pageTwoRequests) : "1";
        return success(
          realtimePage(
            url,
            activeTask,
            [
            {
              id: 250 + Number(pageNum),
              fileName: `running-page-${pageNum}-v${version}.txt`,
              srcPath: "/src",
              dstPath: "/dst",
              fileSize: 1024,
              type: 0,
              status: 1,
              progress: 50,
            },
            ],
            12,
          ),
        );
      }
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "分页刷新任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  const realtimeTable = page
    .locator(".desktop-data:visible, .mobile-data:visible")
    .last();
  await expect(realtimeTable.getByText("running-page-1-v1.txt")).toBeVisible();
  await page.getByRole("button", { name: "Next" }).click();
  await expect(realtimeTable.getByText("running-page-2-v1.txt")).toBeVisible();
  pageTwoVisible = true;

  await expect(realtimeTable.getByText("running-page-2-v2.txt")).toBeVisible();
  await expect(realtimeTable.getByText("running-page-1-v1.txt")).toHaveCount(0);
  expect(pageRequestsAfterPageTwo.length).toBeGreaterThan(0);
  expect(pageRequestsAfterPageTwo.every((pageNum) => pageNum === "2")).toBe(
    true,
  );
});

test("realtime snapshot refresh keeps the selected status view", async ({
  page,
}) => {
  test.setTimeout(15000);
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const snapshot = (version: number) => ({
    taskId: 24,
    scanFinish: true,
    createTime: 1,
    duration: version,
    num: {
      wait: 0,
      running: version === 1 ? 1 : 0,
      success: version,
      fail: 0,
      other: 0,
    },
    size: {
      wait: 0,
      running: version === 1 ? 1 : 0,
      success: version,
      fail: 0,
      other: 0,
    },
    doneSize: version,
    remainSize: version === 1 ? 1 : 0,
    speed: version,
    speedAvg: version,
    remainTime: version === 1 ? 1 : 0,
  });
  let allowSnapshotUpdate = false;
  let successTabSelected = false;
  const statusesAfterSuccessTab: string[] = [];

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.has("status")) {
        const status = url.searchParams.get("status") || "";
        if (successTabSelected) statusesAfterSuccessTab.push(status);
        return success(
          realtimePage(
            url,
            snapshot(allowSnapshotUpdate ? 2 : 1),
            [
            {
              id: 240 + Number(status || 0),
              fileName:
                status === "2"
                  ? "success-view-file.txt"
                  : "running-view-file.txt",
              srcPath: "/src",
              dstPath: "/dst",
              fileSize: 1024,
              type: 0,
              status: Number(status),
              progress: 100,
            },
            ],
            status === "2" ? 2 : 1,
          ),
        );
      }
      return success(snapshot(allowSnapshotUpdate ? 2 : 1));
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "快照刷新任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await expect(page.getByRole("tab", { name: "成功 1" })).toBeVisible();
  successTabSelected = true;
  await page.getByRole("tab", { name: "成功 1" }).click();
  allowSnapshotUpdate = true;
  const realtimeTable = page
    .locator(".desktop-data:visible, .mobile-data:visible")
    .last();
  await expect(realtimeTable.getByText("success-view-file.txt")).toBeVisible();

  const refreshedSuccessTab = page.getByRole("tab", { name: "成功 2" });
  await expect(refreshedSuccessTab).toBeVisible();
  await expect(refreshedSuccessTab).toHaveClass(/semi-tabs-tab-active/);
  await expect(
    realtimeTable.getByText("running-view-file.txt"),
  ).toHaveCount(0);
  expect(statusesAfterSuccessTab.length).toBeGreaterThan(0);
  expect(statusesAfterSuccessTab.every((status) => status === "2")).toBe(
    true,
  );
});

test("realtime status tabs ignore stale task detail responses", async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 31,
    scanFinish: true,
    createTime: 100,
    duration: 1,
    num: { wait: 0, running: 1, success: 1, fail: 0, other: 0 },
    size: { wait: 0, running: 1, success: 1, fail: 0, other: 0 },
    doneSize: 1,
    remainSize: 1,
    speed: 1,
    speedAvg: 1,
    remainTime: 1,
  };
  let successRequestIdentity = { taskId: "", createTime: "" };

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.get("status") === "2") {
        successRequestIdentity = {
          taskId: url.searchParams.get("expectedTaskId") || "",
          createTime: url.searchParams.get("expectedCreateTime") || "",
        };
        return success({
          taskId: 999,
          createTime: 999,
          status: 2,
          pageNum: 1,
          pageSize: 10,
          stale: true,
          dataList: [
            {
              id: 310,
              fileName: "stale-success-detail.txt",
              srcPath: "/src",
              dstPath: "/dst",
              fileSize: 1024,
              type: 0,
              status: 2,
            },
          ],
          count: 1,
        });
      }
      if (url.searchParams.has("status")) {
        return success({
          taskId: 31,
          createTime: 100,
          status: 1,
          pageNum: 1,
          pageSize: 10,
          stale: false,
          dataList: [
            {
              id: 311,
              fileName: "running-detail.txt",
              srcPath: "/src",
              dstPath: "/dst",
              fileSize: 1024,
              type: 0,
              status: 1,
              progress: 50,
            },
          ],
          count: 1,
        });
      }
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "过期明细任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  const realtimeTable = page
    .locator(".desktop-data:visible, .mobile-data:visible")
    .last();
  await expect(realtimeTable.getByText("running-detail.txt")).toBeVisible();

  const staleResponse = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return (
      url.pathname.endsWith("/job") &&
      url.searchParams.get("current") === "1" &&
      url.searchParams.get("status") === "2"
    );
  });
  await page.getByRole("tab", { name: /成功/ }).click();
  await staleResponse;

  expect(successRequestIdentity).toEqual({
    taskId: "31",
    createTime: "100",
  });
  await expect(
    realtimeTable.getByText("stale-success-detail.txt"),
  ).toHaveCount(0);
});

test("realtime file columns keep the name readable at 1100px desktop width", async ({
  page,
}) => {
  test.skip(page.viewportSize()?.width !== 1100, "1100px desktop layout only");
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 9,
    scanFinish: true,
    createTime: 1,
    duration: 1,
    num: { wait: 0, running: 0, success: 0, fail: 1, other: 0 },
    size: { wait: 0, running: 0, success: 0, fail: 1, other: 0 },
    doneSize: 0,
    remainSize: 1,
    speed: 1,
    speedAvg: 1,
    remainTime: 1,
    doingTask: [],
  };

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.get("status") === "7") {
        return success(
          realtimePage(url, activeTask, [
            {
              id: 11,
              fileName: "annual-audit-source-archive-with-readable-name.tar.gz",
              srcPath: "/source/path/with/a/readable/file/name",
              dstPath: "/backup/path/with/a/readable/file/name",
              fileSize: 1024,
              type: 0,
              status: 7,
              progress: 0,
              errMsg:
                "目标端返回了一个非常长的错误说明，包含路径、重试建议、服务端响应和多段上下文信息，需要在进度列内被截断而不是把文件名列挤没。",
            },
          ]),
        );
      }
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "桌面实时任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  await page.getByRole("tab", { name: /失败/ }).click();
  const desktopTable = page.locator(".desktop-data").last();
  await expect(
    desktopTable.getByText("annual-audit-source-archive-with-readable-name.tar.gz"),
  ).toBeVisible();

  const row = desktopTable.locator(".semi-table-tbody tr").first();
  const fileCell = row.locator("td").nth(0);
  const progressCell = row.locator("td").nth(3);
  const fileBox = await fileCell.boundingBox();
  const progressBox = await progressCell.boundingBox();
  expect(fileBox).not.toBeNull();
  expect(progressBox).not.toBeNull();
  expect(fileBox!.width).toBeGreaterThanOrEqual(260);
  expect(progressBox!.width).toBeLessThanOrEqual(200);
});

test("engine connectivity checks are serialized", async ({ page }) => {
  let releaseRequest: (() => void) | undefined;
  const requestStarted = new Promise<void>((resolve) => {
    releaseRequest = resolve;
  });
  let finishRequest: (() => void) | undefined;
  const requestCanFinish = new Promise<void>((resolve) => {
    finishRequest = resolve;
  });
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist/test")) {
      releaseRequest?.();
      await requestCanFinish;
      return success(null);
    }
    if (url.pathname.endsWith("/alist"))
      return success([
        { id: 1, remark: "引擎 A", url: "https://a.test", userName: "a" },
        { id: 2, remark: "引擎 B", url: "https://b.test", userName: "b" },
      ]);
    if (url.pathname.endsWith("/job"))
      return success({ dataList: [], count: 0 });
    return success(null);
  });

  await page.goto("/app/opensync/engines");
  const buttons = page.getByRole("button", { name: "测试引擎", exact: true });
  await buttons.first().click();
  await requestStarted;
  await expect(buttons).toHaveCount(2);
  await expect(buttons.nth(0)).toBeDisabled();
  await expect(buttons.nth(1)).toBeDisabled();
  finishRequest?.();
  await expect(buttons.nth(0)).toBeEnabled();
});

test("history load errors stay visible and can be retried", async ({ page }) => {
  let allowHistorySuccess = false;
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const response = (code: number, data: unknown, msg = "") =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code, data, msg }),
      });
    if (url.pathname.endsWith("/session"))
      return response(200, { uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return response(200, []);
    if (
      url.pathname.endsWith("/job") &&
      url.searchParams.has("id") &&
      !url.searchParams.has("current")
    ) {
      return !allowHistorySuccess
        ? response(500, null, "历史记录加载失败")
        : response(200, { dataList: [], count: 0 });
    }
    if (url.pathname.endsWith("/job"))
      return response(200, {
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "历史任务",
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
    return response(200, null);
  });

  await page.goto("/app/opensync/tasks?jobId=1&tab=history");
  await expect(page.getByText("历史记录加载失败", { exact: true })).toBeVisible();
  allowHistorySuccess = true;
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await expect(page.getByText("历史记录加载失败", { exact: true })).toHaveCount(0);
});

test("completed execution details omit file status filtering", async ({ page }) => {
  const detailRequests: string[] = [];
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(null);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("taskId")) {
      detailRequests.push(url.search);
      return success({
        dataList: [
          {
            id: 31,
            fileName: "detail-done.txt",
            srcPath: "/src/detail-done.txt",
            dstPath: "/dst/detail-done.txt",
            fileSize: 1024,
            type: 0,
            status: 2,
          },
        ],
        count: 1,
      });
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("id")) {
      return success({
        dataList: [
          {
            id: 101,
            status: 2,
            createTime: 1,
            runTime: 5,
            successNum: 1,
            failNum: 0,
            allNum: 1,
          },
        ],
        count: 1,
      });
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "已结束任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=history");
  await page
    .getByRole("button", { name: "查看执行明细", exact: true })
    .click();
  const details = page.getByRole("dialog");
  await expect(details.getByText("执行明细", { exact: true })).toBeVisible();
  const visibleDetails = details.locator(
    ".desktop-data:visible, .mobile-data:visible",
  );
  await expect(
    visibleDetails.getByText("detail-done.txt", { exact: true }).first(),
  ).toBeVisible();
  await expect(details.getByText("全部状态", { exact: true })).toHaveCount(0);
  await expect(details.getByText("全部操作类型", { exact: true })).toBeVisible();
  await expect(details.getByText("全部对象类型", { exact: true })).toBeVisible();
  await expect(details.getByText("全部错误信息", { exact: true })).toBeVisible();
  expect(detailRequests.some((query) => query.includes("status="))).toBe(false);
});

test("remote path load errors remain actionable", async ({ page }) => {
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const response = (code: number, data: unknown, msg = "") =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code, data, msg }),
      });
    if (url.pathname.endsWith("/session"))
      return response(200, { uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId"))
      return response(500, null, "目录暂时不可用");
    if (url.pathname.endsWith("/alist"))
      return response(200, [
        { id: 1, remark: "测试引擎", url: "https://engine.test", userName: "a" },
      ]);
    if (url.pathname.endsWith("/job"))
      return response(200, { dataList: [], count: 0 });
    return response(200, null);
  });

  await page.goto("/app/opensync/tasks");
  await page.getByRole("button", { name: "新建任务", exact: true }).click();
  await expect(page.getByText("目录暂时不可用").first()).toBeVisible();
  await expect(page.getByRole("button", { name: "重试" }).first()).toBeVisible();
});

test("switching realtime tab clears the previous tab rows immediately", async ({
  page,
}) => {
  test.setTimeout(20000);
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  const activeTask = {
    taskId: 27,
    scanFinish: true,
    createTime: 1,
    duration: 1,
    num: { wait: 0, running: 1, success: 1, fail: 0, other: 0 },
    size: { wait: 0, running: 1, success: 1, fail: 0, other: 0 },
    doneSize: 1,
    remainSize: 1,
    speed: 1,
    speedAvg: 1,
    remainTime: 1,
  };

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      const status = url.searchParams.get("status");
      // 运行中：立即返回
      if (status === "1") {
        return success(
          realtimePage(url, activeTask, [
            { id: 271, fileName: "running-file.txt", status: 1, type: 0 },
          ]),
        );
      }
      // 成功：故意延迟，留出观察脏数据的窗口
      if (status === "2") {
        await new Promise((resolve) => setTimeout(resolve, 4000));
        return success(
          realtimePage(url, activeTask, [
            { id: 272, fileName: "success-file.txt", status: 2, type: 0 },
          ]),
        );
      }
      if (status !== null)
        return success(realtimePage(url, activeTask, []));
      return success(activeTask);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "切换 tab 任务",
            srcPath: '["/src"]',
            dstPath: '["/dst"]',
            alistId: 1,
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  const visibleList = page
    .locator(".desktop-data:visible, .mobile-data:visible")
    .last();
  await expect(visibleList.getByText("running-file.txt")).toBeVisible({
    timeout: 8000,
  });

  await page.getByRole("tab", { name: /成功/ }).click();

  // 成功 tab 的响应还在路上，运行中的行必须立刻消失，
  // 否则就是上一个 tab 的脏数据残留。
  await expect(visibleList.getByText("running-file.txt")).toBeHidden({
    timeout: 1200,
  });

  // 新数据到达后正常渲染
  await expect(visibleList.getByText("success-file.txt")).toBeVisible({
    timeout: 8000,
  });
});
