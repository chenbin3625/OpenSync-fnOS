import { expect, test, type Page } from "@playwright/test";
const activeTask = {
  taskId: 91,
  scanFinish: true,
  createTime: 1,
  duration: 1,
  num: { wait: 0, running: 1, success: 2, fail: 0, other: 0 },
  size: { wait: 0, running: 1, success: 2, fail: 0, other: 0 },
  doneSize: 2,
  remainSize: 1,
  speed: 1,
  speedAvg: 1,
  remainTime: 1,
};

const fixtureJob = {
  id: 1,
  enable: 1,
  remark: "布局测试任务",
  srcPath: '["/src"]',
  dstPath: '["/dst"]',
  alistId: 1,
  method: 0,
  interval: 0,
  isCron: 2,
  useCacheT: 0,
  useCacheS: 0,
  minFileSize: 0,
  maxFileSize: 0,
  exclude: "",
};

function realtimePage(url: URL, task: typeof activeTask, dataList: unknown[]) {
  return {
    taskId: task.taskId,
    createTime: task.createTime,
    status: Number(url.searchParams.get("status")),
    pageNum: Number(url.searchParams.get("pageNum") || 1),
    pageSize: Number(url.searchParams.get("pageSize") || 10),
    stale: false,
    dataList,
    count: dataList.length,
  };
}

/**
 * These layout specs used to depend on whatever task happened to exist in the
 * developer's database (jobId=1). The e2e backend now runs on an isolated data
 * directory, so every case must provide its own task workspace fixture.
 */
async function routeTaskWorkspace(page: Page, current: typeof activeTask | null) {
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "success" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/version/latest"))
      return success({
        latestVersion: "v0.0.0",
        releaseURL: "https://example.test/releases",
        hasUpdate: false,
      });
    if (url.pathname.endsWith("/system/config"))
      return success({
        taskTimeout: 48,
        taskSave: 30,
        copyConcurrency: 5,
        scanConcurrency: 16,
        maxRetries: 2,
      });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      if (url.searchParams.has("status"))
        return success(realtimePage(url, current!, []));
      return success(current);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("taskId"))
      return success({ dataList: [], count: 0 });
    if (url.pathname.endsWith("/job") && url.searchParams.has("id"))
      return success({ dataList: [], count: 0 });
    if (url.pathname.endsWith("/job"))
      return success({ dataList: [fixtureJob], count: 1 });
    return success(null);
  });
}


test("dark host theme colors the shell, surfaces, controls, and text together", async ({
  page,
}) => {
  await page.goto("/app/opensync/settings");
  await expect(page.locator(".settings-card")).toHaveCount(2);
  await page.evaluate(() => {
    document.body.setAttribute("theme-mode", "dark");
    document.documentElement.dataset.theme = "dark";
  });

  const colors = await page.evaluate(() => {
    const color = (selector: string, property: "color" | "backgroundColor") =>
      getComputedStyle(document.querySelector(selector) as HTMLElement)[
        property
      ];
    return {
      shell: color(".app-shell", "backgroundColor"),
      workspace: color(".app-workspace", "backgroundColor"),
      card: color(".settings-card", "backgroundColor"),
      input: color(".numeric-setting .semi-input-wrapper", "backgroundColor"),
      text: color(".settings-card h2", "color"),
    };
  });
  for (const surface of [
    colors.shell,
    colors.workspace,
    colors.card,
    colors.input,
  ]) {
    expect(surface).not.toBe("rgb(255, 255, 255)");
    expect(surface).not.toBe("rgb(243, 243, 243)");
  }
  expect(colors.text).not.toBe("rgb(36, 38, 43)");
});

test("small secondary text has readable contrast on a light surface", async ({
  page,
}) => {
  await page.goto("/app/opensync/settings");
  await expect(page.locator(".settings-card")).toHaveCount(2);
  const ratio = await page.evaluate(() => {
    const luminance = (color: string) => {
      const channels = color.match(/\d+/g)!.slice(0, 3).map((value) => {
        const normalized = Number(value) / 255;
        return normalized <= 0.04045
          ? normalized / 12.92
          : ((normalized + 0.055) / 1.055) ** 2.4;
      });
      return (
        channels[0] * 0.2126 +
        channels[1] * 0.7152 +
        channels[2] * 0.0722
      );
    };
    const unit = document.querySelector<HTMLElement>(
      ".numeric-setting > span",
    )!;
    const foreground = luminance(getComputedStyle(unit).color);
    const card = document.querySelector<HTMLElement>(".settings-card")!;
    const background = luminance(getComputedStyle(card).backgroundColor);
    return (
      (Math.max(foreground, background) + 0.05) /
      (Math.min(foreground, background) + 0.05)
    );
  });
  expect(ratio).toBeGreaterThanOrEqual(4.5);
});

test("320px task controls and realtime status tabs stay on one row", async ({
  page,
}) => {
  test.skip(page.viewportSize()?.width !== 320, "narrow mobile layout only");
  await routeTaskWorkspace(page, activeTask);
  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  const toolbar = page.locator(".page-toolbar");
  const toolbarBox = await toolbar.boundingBox();
  const actionBox = await toolbar
    .getByRole("button", { name: "新建任务" })
    .boundingBox();
  const topTabBox = await toolbar.getByRole("tab", { name: "总览" }).boundingBox();
  expect(toolbarBox!.height).toBeLessThanOrEqual(54);
  expect(
    Math.abs(
      actionBox!.y +
        actionBox!.height / 2 -
        topTabBox!.y -
        topTabBox!.height / 2,
    ),
  ).toBeLessThan(8);

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  const statusTabs = page.locator(".execution-tabs").getByRole("tab");
  await expect(statusTabs).toHaveCount(5);
  const first = await statusTabs.first().boundingBox();
  const last = await statusTabs.last().boundingBox();
  expect(Math.abs(first!.y - last!.y)).toBeLessThan(2);
  await statusTabs.last().scrollIntoViewIfNeeded();
  const scrolledLast = await statusTabs.last().boundingBox();
  const statusBar = await page
    .locator(".execution-tabs .semi-tabs-bar")
    .boundingBox();
  expect(scrolledLast!.x + scrolledLast!.width).toBeLessThanOrEqual(
    statusBar!.x + statusBar!.width + 1,
  );
});

test("320px settings and overview labels keep a single line", async ({
  page,
}) => {
  test.skip(page.viewportSize()?.width !== 320, "narrow mobile layout only");
  await routeTaskWorkspace(page, null);
  await page.goto("/app/opensync/settings");
  await expect(page.locator(".settings-card")).toHaveCount(2);
  const settingLabel = page.locator(".setting-row-label").first();
  const headingBox = await page
    .locator(".settings-card-head h2")
    .first()
    .boundingBox();
  const labelBox = await settingLabel.boundingBox();
  expect(Math.abs(headingBox!.x - labelBox!.x)).toBeLessThanOrEqual(1);
  expect(
    await settingLabel.evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element.firstChild!);
      return range.getClientRects().length;
    }),
  ).toBe(1);
  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  const latestLabel = page
    .locator(".info-row")
    .filter({ hasText: "最近一次执行" })
    .locator(":scope > span");
  await expect(latestLabel).toBeVisible();
  expect(
    await latestLabel.evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element);
      return range.getClientRects().length;
    }),
  ).toBe(1);
});
