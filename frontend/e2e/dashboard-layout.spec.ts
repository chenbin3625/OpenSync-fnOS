import { expect, test, type Page } from "@playwright/test";

const now = Math.floor(Date.now() / 1000);

const richDashboardFixture = {
  kpi: {
    totalJobs: 5,
    enabledJobs: 4,
    runningJobs: 2,
    queuedJobs: 1,
    pendingApprovals: 1,
    totalEngines: 3,
    onlineEngines: 2,
    offlineEngines: 1,
  },
  recent7d: {
    totalRuns: 18,
    successRuns: 14,
    failRuns: 3,
    stoppedRuns: 1,
    totalFiles: 1246,
    totalBytes: 28 * 1024 * 1024 * 1024 + 420 * 1024 * 1024,
  },
  pendingApprovals: [
    {
      jobId: 2,
      jobName: "影音库全量镜像",
      taskId: 8042,
      deleteCount: 14,
      deleteSize: 186 * 1024 * 1024,
      pathCount: 4,
    },
  ],
  jobs: [
    {
      id: 1,
      remark: "家庭相册每日备份",
      enable: 1,
      alistId: 1,
      alistName: "飞牛 NAS 主引擎",
      method: 0,
      isCron: 1,
      interval: 0,
      srcPath: '["/Photos/2026","/Photos/Family"]',
      dstPath: '["/AliyunDrive/Backup/Photos"]',
      running: true,
      queued: false,
      currentTaskId: 8051,
      pendingApproval: null,
      liveNum: { wait: 12, running: 3, success: 35, fail: 0, other: 0 },
      liveProgress: 72.4,
      nextRunTime: now + 3600 * 6,
      windowEnable: 1,
      windowStart: "22:00",
      windowEnd: "07:00",
      lastTask: null,
    },
    {
      id: 2,
      remark: "影音库全量镜像",
      enable: 1,
      alistId: 1,
      alistName: "飞牛 NAS 主引擎",
      method: 1,
      isCron: 1,
      interval: 0,
      srcPath: '["/Media/Movies"]',
      dstPath: '["/Cloud115/Movies-Mirror"]',
      running: true,
      queued: false,
      currentTaskId: 8042,
      pendingApproval: {
        deleteCount: 14,
        deleteSize: 186 * 1024 * 1024,
        pathCount: 4,
        paths: [],
      },
      liveNum: null,
      liveProgress: 0,
      nextRunTime: now + 86400,
      windowEnable: 0,
      lastTask: null,
    },
    {
      id: 3,
      remark: "下载目录自动归档移动",
      enable: 1,
      alistId: 2,
      alistName: "夸克云盘中转引擎",
      method: 2,
      isCron: 0,
      interval: 60,
      srcPath: '["/Downloads/Completed/VeryLongSubfolderNameForTruncationCheck"]',
      dstPath: '["/Archive/Completed/2026/Q4"]',
      running: false,
      queued: true,
      currentTaskId: 0,
      pendingApproval: null,
      liveNum: null,
      liveProgress: 0,
      nextRunTime: now + 1800,
      windowEnable: 0,
      lastTask: {
        id: 8019,
        status: 2,
        createTime: now - 3600,
        runTime: now - 3540,
        successNum: 28,
        failNum: 0,
        allNum: 28,
        sumSize: 3.4 * 1024 * 1024 * 1024,
        errMsg: null,
      },
    },
    {
      id: 4,
      remark: "工作文档异地容灾",
      enable: 1,
      alistId: 3,
      alistName: "异地 WebDAV 备份",
      method: 0,
      isCron: 1,
      interval: 0,
      srcPath: '["/Work/Projects"]',
      dstPath: '["/WebDAV/Offsite/Work"]',
      running: false,
      queued: false,
      currentTaskId: 0,
      pendingApproval: null,
      liveNum: null,
      liveProgress: 0,
      nextRunTime: now + 7200,
      windowEnable: 0,
      lastTask: {
        id: 8010,
        status: 3,
        createTime: now - 7200,
        runTime: now - 7000,
        successNum: 42,
        failNum: 3,
        allNum: 45,
        sumSize: 512 * 1024 * 1024,
        errMsg: "WebDAV 写入超时：504 Gateway Timeout",
      },
    },
    {
      id: 5,
      remark: "临时素材手动归档",
      enable: 0,
      alistId: 2,
      alistName: "夸克云盘中转引擎",
      method: 0,
      isCron: 2,
      interval: 0,
      srcPath: '["/Temp/Clips"]',
      dstPath: '["/Archive/Clips"]',
      running: false,
      queued: false,
      currentTaskId: 0,
      pendingApproval: null,
      liveNum: null,
      liveProgress: 0,
      nextRunTime: 0,
      windowEnable: 0,
      lastTask: null,
    },
  ],
};

async function routeDashboardFixtures(page: Page) {
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "0.0.42" });
    if (url.pathname.endsWith("/version/latest"))
      return success({
        latestVersion: "v0.0.42",
        releaseURL: "https://example.test/releases",
        hasUpdate: false,
      });
    if (url.pathname.endsWith("/dashboard"))
      return success(richDashboardFixture);
    if (url.pathname.endsWith("/alist"))
      return success([
        { id: 1, remark: "飞牛 NAS 主引擎", url: "http://127.0.0.1:5244", userName: "admin" },
        { id: 2, remark: "夸克云盘中转引擎", url: "http://127.0.0.1:5245", userName: "admin" },
        { id: 3, remark: "异地 WebDAV 备份", url: "https://dav.example.test", userName: "backup" },
      ]);
    if (url.pathname.endsWith("/job"))
      return success({
        dataList: richDashboardFixture.jobs.map((j) => ({
          id: j.id,
          remark: j.remark,
          enable: j.enable,
          alistId: j.alistId,
          method: j.method,
          isCron: j.isCron,
          interval: j.interval,
          srcPath: j.srcPath,
          dstPath: j.dstPath,
          running: j.running,
          queued: j.queued,
          pendingApproval: Boolean(j.pendingApproval),
          lastStatus: j.lastTask?.status ?? 0,
        })),
        count: richDashboardFixture.jobs.length,
      });
    return success(null);
  });
}

test("dashboard renders KPI cards, padded section header, status filters and zero overflow", async ({
  page,
}, testInfo) => {
  await routeDashboardFixtures(page);
  await page.goto("/app/opensync/dashboard");

  await expect(page.locator(".dashboard-kpi-card")).toHaveCount(4);
  await expect(page.locator(".approval-alert-banner")).toBeVisible();
  await expect(page.locator(".dashboard-job-row")).toHaveCount(5);

  // Section card header must have explicit padding & flex layout (no unstyled header regression)
  const cardHead = page.locator(".dashboard-jobs-card .section-card-head");
  await expect(cardHead).toBeVisible();
  await expect(cardHead).toHaveCSS("display", "flex");
  const paddingLeft = await cardHead.evaluate((el) =>
    parseFloat(getComputedStyle(el).paddingLeft),
  );
  expect(paddingLeft).toBeGreaterThanOrEqual(16);

  // No horizontal overflow at any project viewport (320px .. 1440px)
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);

  if (process.env.PLAYWRIGHT_SCREENSHOTS === "1") {
    await page.screenshot({
      path: `test-results/dashboard-light-${testInfo.project.name}.png`,
      fullPage: true,
    });
  }

  // Interactive status pill filtering + reset
  await page.getByRole("button", { name: /最近失败/ }).click();
  await expect(page.locator(".dashboard-job-row")).toHaveCount(1);
  await expect(page.locator(".dashboard-job-row")).toContainText("工作文档异地容灾");
  await page.getByRole("button", { name: "重置筛选" }).click();
  await expect(page.locator(".dashboard-job-row")).toHaveCount(5);

  // Batch selection bar appears on selecting a row
  await page.locator(".dashboard-job-row .dashboard-job-check .semi-checkbox").first().click();
  await expect(page.locator(".batch-action-bar")).toBeVisible();
  await expect(page.locator(".batch-action-bar")).toContainText("已选中 1 个任务");
  await page.getByRole("button", { name: "取消选择" }).click();
  await expect(page.locator(".batch-action-bar")).toHaveCount(0);

  // Verify checkbox and job info sit tightly adjacent (regression guard for grid auto-placement)
  const firstCheckBox = await page.locator(".dashboard-job-check").first().boundingBox();
  const firstInfoBox = await page.locator(".dashboard-job-info").first().boundingBox();
  expect(firstCheckBox).not.toBeNull();
  expect(firstInfoBox).not.toBeNull();
  expect(firstInfoBox!.x - (firstCheckBox!.x + firstCheckBox!.width)).toBeLessThanOrEqual(24);

  // Dark theme tokens apply cleanly across dashboard surfaces (wait for CSS transition)
  await page.evaluate(() => {
    document.body.setAttribute("theme-mode", "dark");
    document.documentElement.dataset.theme = "dark";
  });
  const firstKpiCard = page.locator(".dashboard-kpi-card").first();
  await expect(firstKpiCard).toHaveCSS("background-color", "rgb(46, 48, 54)");
  await expect(page.locator(".dashboard-job-title-btn").first()).toHaveCSS(
    "color",
    "rgb(232, 234, 238)",
  );

  if (process.env.PLAYWRIGHT_SCREENSHOTS === "1") {
    await page.screenshot({
      path: `test-results/dashboard-dark-${testInfo.project.name}.png`,
      fullPage: true,
    });
  }
});
