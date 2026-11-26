import { expect, test, type Page } from "@playwright/test";

const now = Math.floor(Date.now() / 1000);

const richDashboardFixture = {
  kpi: {
    totalJobs: 5,
    enabledJobs: 4,
    runningJobs: 2,
    queuedJobs: 1,
    totalEngines: 3,
    onlineEngines: 2,
    offlineEngines: 1,
  },
  realtime: {
    totalSpeed: 38.4 * 1024 * 1024,
    totalSpeedAvg: 32.1 * 1024 * 1024,
    peakSpeed: 46.8 * 1024 * 1024,
    activeJobs: 2,
    pausedJobs: 0,
    activeTransfers: 0,
    totalDoneBytes: 1.8 * 1024 * 1024 * 1024,
    totalRemainBytes: 0.9 * 1024 * 1024 * 1024,
    estimatedRemainTime: 26,
    globalBwLimit: "50M",
    sampledAt: now,
    speedHistory: [
      { ts: now - 14, speed: 18.2 * 1024 * 1024 },
      { ts: now - 12, speed: 24.6 * 1024 * 1024 },
      { ts: now - 10, speed: 31.5 * 1024 * 1024 },
      { ts: now - 8, speed: 46.8 * 1024 * 1024 },
      { ts: now - 6, speed: 41.2 * 1024 * 1024 },
      { ts: now - 4, speed: 35.9 * 1024 * 1024 },
      { ts: now - 2, speed: 37.1 * 1024 * 1024 },
      { ts: now, speed: 38.4 * 1024 * 1024 },
    ],
  },
  recent7d: {
    totalRuns: 18,
    successRuns: 14,
    failRuns: 3,
    stoppedRuns: 1,
    totalFiles: 1246,
    totalBytes: 28 * 1024 * 1024 * 1024 + 420 * 1024 * 1024,
  },
  dailyTrend: Array.from({ length: 7 }, (_, i) => ({
    date: `2026-11-${String(20 + i).padStart(2, "0")}`,
    runs: 2 + (i % 3),
    successRuns: i === 5 ? 1 : 2 + (i % 3),
    failRuns: i === 5 ? 1 : 0,
    files: 120 + i * 35,
    bytes: (2 + (i % 4)) * 1024 * 1024 * 1024,
  })),
  storageQuotas: [
    {
      name: "本地",
      type: "local",
      supported: true,
      total: 4 * 1024 * 1024 * 1024 * 1024,
      used: 3.3 * 1024 * 1024 * 1024 * 1024,
      free: 0.7 * 1024 * 1024 * 1024 * 1024,
    },
    {
      name: "AliyunDrive",
      type: "s3",
      supported: true,
      total: 2 * 1024 * 1024 * 1024 * 1024,
      used: 0.9 * 1024 * 1024 * 1024 * 1024,
      free: 1.1 * 1024 * 1024 * 1024 * 1024,
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
      liveNum: { wait: 12, running: 3, success: 35, fail: 0, other: 0 },
      liveProgress: 72.4,
      liveDoneSize: 1.2 * 1024 * 1024 * 1024,
      liveTotalSize: 1.65 * 1024 * 1024 * 1024,
      liveSpeed: 24.6 * 1024 * 1024,
      liveRemainTime: 19,
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
      liveNum: null,
      liveProgress: 56,
      liveDoneSize: 0.6 * 1024 * 1024 * 1024,
      liveTotalSize: 1.05 * 1024 * 1024 * 1024,
      liveSpeed: 13.8 * 1024 * 1024,
      liveRemainTime: 34,
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
  await expect(page.locator(".dashboard-kpi-card").first()).toContainText("38.40");
  await expect(page.locator(".dashboard-kpi-card").first()).toContainText("MB/s");
  await expect(page.locator(".dashboard-live-card")).toBeVisible();
  await expect(page.locator(".dashboard-speed-chart")).toBeVisible();
  await expect(page.locator(".dashboard-live-task-row")).toHaveCount(2);
  await expect(page.locator(".dashboard-job-speed").first()).toContainText("24.60 MB/s");
  await expect(page.locator(".dashboard-job-row")).toHaveCount(5);

  // Switch between Sync Trend and Storage Quota insights
  await page.getByRole("tab", { name: /存储配额/ }).click();
  await expect(page.locator(".dashboard-quota-item")).toHaveCount(2);
  await page.getByRole("tab", { name: "同步趋势" }).click();
  await expect(page.locator(".dashboard-trend-chart")).toBeVisible();

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
  await page.getByRole("button", { name: /同步异常/ }).click();
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

  // Speed chart SVG and Y-axis must stay strictly contained inside .dashboard-speed-chart
  // and never overflow onto the active tasks list (regression guard for wide viewports).
  const chartBox = await page.locator(".dashboard-speed-chart").boundingBox();
  const svgBox = await page.locator(".dashboard-speed-svg").boundingBox();
  const yAxisBox = await page.locator(".dashboard-speed-yaxis").boundingBox();
  const liveTasksBox = await page.locator(".dashboard-live-tasks").boundingBox();
  expect(chartBox).not.toBeNull();
  expect(svgBox).not.toBeNull();
  expect(yAxisBox).not.toBeNull();
  expect(liveTasksBox).not.toBeNull();
  expect(svgBox!.y + svgBox!.height).toBeLessThanOrEqual(
    chartBox!.y + chartBox!.height + 1,
  );
  expect(yAxisBox!.y + yAxisBox!.height).toBeLessThanOrEqual(
    chartBox!.y + chartBox!.height + 1,
  );
  expect(liveTasksBox!.y).toBeGreaterThanOrEqual(
    chartBox!.y + chartBox!.height - 1,
  );

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

test("dashboard chart stays bounded at 1920px and stays flicker-free across live polls", async ({
  page,
}) => {
  test.skip(
    (page.viewportSize()?.width || 0) < 1400,
    "wide desktop chart & poll regression",
  );
  await page.setViewportSize({ width: 1920, height: 1080 });
  let phase: "initial" | "fail" | "swapped" = "initial";
  let failSeen = false;
  let swappedSeen = false;

  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "0.0.53" });
    if (url.pathname.endsWith("/version/latest"))
      return success({
        latestVersion: "v0.0.53",
        releaseURL: "https://example.test/releases",
        hasUpdate: false,
      });
    if (url.pathname.endsWith("/alist"))
      return success([
        {
          id: 1,
          remark: "内置存储",
          url: "rclone://native",
          kind: "rclone",
          health: {
            status: "online",
            storagesKnown: true,
            storages: [
              { mountPath: "/本地", status: "ok" },
              { mountPath: "/谷歌", status: "ok" },
              { mountPath: "/夸克", status: "ok" },
            ],
          },
        },
      ]);
    if (url.pathname.endsWith("/job"))
      return success({
        dataList: richDashboardFixture.jobs.slice(0, 2).map((j) => ({
          id: j.id,
          remark: j.remark,
          enable: 1,
          alistId: 1,
          method: j.method,
          isCron: 3,
          srcPath: j.srcPath,
          dstPath: j.dstPath,
          running: true,
        })),
        count: 2,
      });
    if (url.pathname.endsWith("/dashboard")) {
      // Simulate a transient background poll failure after initial load — must not unmount dashboard.
      if (phase === "fail") {
        failSeen = true;
        phase = "swapped";
        return route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({ code: 503, data: null, msg: "服务繁忙" }),
        });
      }
      // Swap speeds on subsequent poll — active task order must stay stable (no row jumping).
      const swap = phase === "swapped";
      if (swap) swappedSeen = true;
      const jobs = [
        {
          ...richDashboardFixture.jobs[0],
          liveSpeed: (swap ? 5.2 : 24.6) * 1024 * 1024,
        },
        {
          ...richDashboardFixture.jobs[1],
          liveSpeed: (swap ? 31.4 : 13.8) * 1024 * 1024,
        },
      ];
      return success({
        ...richDashboardFixture,
        recent7d: {
          totalRuns: 3,
          successRuns: 0,
          failRuns: 0,
          stoppedRuns: 0,
          totalFiles: 0,
          totalBytes: 0,
        },
        jobs,
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/dashboard");
  await expect(page.locator(".dashboard-speed-chart")).toBeVisible();
  await expect(page.locator(".dashboard-live-task-row")).toHaveCount(2);

  // Verify zero vertical overflow of SVG/Y-axis at 1920x1080
  const boxes = await page.evaluate(() => {
    const rect = (sel: string) =>
      document.querySelector<HTMLElement | SVGElement>(sel)!.getBoundingClientRect();
    const chart = rect(".dashboard-speed-chart");
    const svg = rect(".dashboard-speed-svg");
    const yaxis = rect(".dashboard-speed-yaxis");
    const tasks = rect(".dashboard-live-tasks");
    return {
      chartBottom: chart.bottom,
      svgBottom: svg.bottom,
      yaxisBottom: yaxis.bottom,
      tasksTop: tasks.top,
    };
  });
  expect(boxes.svgBottom).toBeLessThanOrEqual(boxes.chartBottom + 1);
  expect(boxes.yaxisBottom).toBeLessThanOrEqual(boxes.chartBottom + 1);
  expect(boxes.tasksTop).toBeGreaterThanOrEqual(boxes.chartBottom - 1);

  // Verify 0% success pill uses muted styling rather than green success styling
  const recentPill = page.locator(".dashboard-kpi-card").nth(3).locator(".dashboard-kpi-pill");
  await expect(recentPill).toHaveText("成功率 0%");
  await expect(recentPill).toHaveClass(/is-muted/);
  await expect(recentPill).not.toHaveClass(/is-success/);

  const initialOrder = await page
    .locator(".dashboard-live-task-name")
    .allTextContents();
  expect(initialOrder).toEqual(["家庭相册每日备份", "影音库全量镜像"]);

  if (process.env.PLAYWRIGHT_SCREENSHOTS === "1") {
    await page.screenshot({
      path: "test-results/dashboard-light-1920.png",
      fullPage: true,
    });
  }

  // Next poll fails transiently (must keep dashboard mounted), followed by swapped speeds
  phase = "fail";
  await expect.poll(() => failSeen, { timeout: 6000 }).toBe(true);
  await expect(page.locator(".dashboard-kpi-card")).toHaveCount(4);
  await expect(page.getByText(/实时数据刷新失败/)).toBeVisible();

  await expect.poll(() => swappedSeen, { timeout: 6000 }).toBe(true);
  await expect(page.getByText(/实时数据刷新失败/)).toHaveCount(0);
  await expect(page.locator(".dashboard-kpi-card")).toHaveCount(4);
  const afterOrder = await page
    .locator(".dashboard-live-task-name")
    .allTextContents();
  expect(afterOrder).toEqual(["家庭相册每日备份", "影音库全量镜像"]);
});
