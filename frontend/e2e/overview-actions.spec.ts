import { expect, test } from "@playwright/test";

test("bisync conflicts name the configured sides and restrict type conflicts to keeping both", async ({ page }) => {
  const conflicts = [
    { id: 31, relPath: "notes.txt", conflictType: "content", conflictCopyPath: "notes.conflict.txt" },
    { id: 32, relPath: "photos", conflictType: "type", conflictCopyPath: "photos.conflict" },
  ];
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({ json: { code: 200, data, msg: "" } });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job/conflicts")) return success(conflicts);
    if (url.pathname.endsWith("/job") && !url.searchParams.has("current")) {
      return success({
        dataList: [
          {
            id: 1, enable: 1, remark: "双向任务", alistId: 1, method: 3, isCron: 3,
            srcPath: '["/cloud/notes"]', dstPath: '["/local/disk/notes"]',
            syncStatus: { state: "synced", conflictCount: 2 },
          },
        ],
        count: 1,
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  await page.getByRole("button", { name: "处理 2 项冲突" }).click();
  const dialog = page.getByRole("dialog").filter({ hasText: "双向同步冲突处理" });
  await expect(dialog.getByText("/cloud/notes", { exact: true })).toBeVisible();
  await expect(dialog.getByText("/local/disk/notes", { exact: true })).toBeVisible();
  // There are two conflicts, but only the content conflict offers version selection.
  await expect(dialog.getByRole("button", { name: "保留左侧版本" })).toHaveCount(1);
  await expect(dialog.getByRole("button", { name: "保留右侧版本" })).toHaveCount(1);
  await expect(dialog.getByRole("button", { name: "保留本地版本" })).toHaveCount(0);
  const request = page.waitForRequest("**/job/conflicts/resolve");
  await dialog.getByRole("button", { name: "保留右侧版本" }).click();
  expect((await request).postDataJSON()).toEqual({ jobId: 1, conflictId: 31, action: "keep_remote" });
});

test("overview embeds live sync progress and stops current batch without disabling continuous sync", async ({
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
            isCron: 3,
          },
        ],
        count: 1,
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  const overview = page.locator(".overview-card");
  await expect(overview.locator(".continuous-live-progress")).toBeVisible();
  const stopButton = overview.getByRole("button", {
    name: "中止本次同步",
    exact: true,
  });
  await expect(stopButton).toBeVisible();
  await expect(
    overview.getByRole("button", { name: "立即同步", exact: true }),
  ).toHaveCount(0);

  const stopRequest = page.waitForRequest(
    (request) =>
      request.url().endsWith("/svr/job") && request.method() === "PUT",
  );
  await stopButton.click();
  const dialog = page
    .getByRole("dialog")
    .filter({ hasText: "中止本次同步批次？" });
  await expect(dialog).toBeVisible();
  await dialog
    .getByRole("button", { name: "中止本次同步", exact: true })
    .click();
  expect((await stopRequest).postDataJSON()).toEqual({
    taskId: "71",
    action: "stop",
  });
});

test("continuous sync stays on overview and opens realtime details on demand", async ({
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
            isCron: 3,
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
            isCron: 3,
            running: true,
          },
        ],
        count: 2,
      });
    }
    return success(null);
  });

  // 1. Direct entry to a syncing continuous job stays on Cloud Sync overview with embedded live progress.
  await page.goto("/app/opensync/tasks?jobId=2");
  const toolbar = page.locator(".page-toolbar");
  await expect(
    toolbar.getByRole("tab", { name: "任务详情", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".continuous-live-progress")).toBeVisible();

  // 2. Clicking "查看传输明细" navigates to realtime file transfer view.
  await page.getByRole("button", { name: "查看传输明细" }).click();
  await expect(
    toolbar.getByRole("tab", { name: /实时任务/ }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/jobId=2&tab=realtime$/);
});
