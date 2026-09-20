import { expect, test } from "@playwright/test";

test("realtime view can transition from idle to an active task", async ({ page }) => {
  test.setTimeout(15000);
  const errors: Error[] = [];
  page.on("pageerror", (error) => errors.push(error));
  await page.addInitScript(() => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    Object.defineProperty(globalThis, "EventSource", { value: undefined });
  });
  let currentCalls = 0;
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
      currentCalls += 1;
      return success(currentCalls === 1 ? null : activeTask);
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
  expect(errors).toEqual([]);
});

test("realtime view loads running rows when the live snapshot is missing", async ({ page }) => {
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
        return success({
          dataList: [
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

  const mobileList = page.locator(".mobile-data").last();
  await expect(mobileList.getByText("mobile-running-file.txt")).toBeVisible();
  await expect(mobileList.getByText("42%")).toBeVisible();
});

test("realtime status pages refresh independently of SSE summary updates", async ({
  page,
}) => {
  test.setTimeout(15000);
  const activeTask = {
    taskId: 18,
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
  await page.addInitScript((task) => {
    Object.defineProperty(globalThis, "ReadableStream", { value: undefined });
    class StableEventSource {
      onmessage: ((event: MessageEvent<string>) => void) | null = null;
      onerror: (() => void) | null = null;

      constructor() {
        setTimeout(() => {
          this.onmessage?.(
            new MessageEvent("message", {
              data: JSON.stringify({ code: 200, data: task, msg: "" }),
            }),
          );
        });
      }

      close() {}
    }
    Object.defineProperty(globalThis, "EventSource", {
      value: StableEventSource,
    });
  }, activeTask);

  let pageTwoVersion = 1;
  const successRequests: number[] = [];
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
      const pageNum = Number(url.searchParams.get("pageNum"));
      successRequests.push(pageNum);
      return success({
        dataList: [
          {
            id: pageNum * 100 + pageTwoVersion,
            fileName:
              pageNum === 2
                ? `success-page-2-v${pageTwoVersion}.txt`
                : "success-page-1.txt",
            status: 2,
            type: 0,
          },
        ],
        count: 21,
      });
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success({ dataList: [], count: 0 });
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
  await page.getByRole("tab", { name: /成功/ }).click();
  const realtimeTable = page
    .locator(".desktop-data:visible, .mobile-data:visible")
    .last();
  await expect(realtimeTable.getByText("success-page-1.txt")).toBeVisible();

  await page
    .locator(".table-pagination")
    .getByRole("button", { name: "Next" })
    .click();
  await expect(realtimeTable.getByText("success-page-2-v1.txt")).toBeVisible();
  expect(successRequests.slice(0, 2)).toEqual([1, 2]);

  pageTwoVersion = 2;
  await expect(realtimeTable.getByText("success-page-2-v2.txt")).toBeVisible({
    timeout: 7000,
  });
  expect(successRequests.at(-1)).toBe(2);
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
        return success({
          dataList: [
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
  expect(progressBox!.width).toBeLessThanOrEqual(180);
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
  let historyCalls = 0;
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
    if (url.pathname.endsWith("/job") && url.searchParams.has("id")) {
      historyCalls += 1;
      return historyCalls === 1
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
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await expect(page.getByText("历史记录加载失败", { exact: true })).toHaveCount(0);
  expect(historyCalls).toBeGreaterThanOrEqual(2);
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
