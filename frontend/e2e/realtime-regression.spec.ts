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
