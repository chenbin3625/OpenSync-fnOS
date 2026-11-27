import { test, expect, type Page } from "@playwright/test";

interface MockEnvOptions {
  coldStart?: boolean;
}

async function setupWorkbenchMock(page: Page, options: MockEnvOptions = {}) {
  const coldStart = Boolean(options.coldStart);
  const nowSec = Math.floor(Date.now() / 1000);
  const batchCalls: Array<{ action: string; ids?: number[] }> = [];

  const remotes = coldStart
    ? []
    : [
        {
          name: "PhotoCloud",
          type: "webdav",
          summary: "https://webdav.example.com/photos",
          options: { url: "https://webdav.example.com/photos" },
          usedByJobs: [1],
          usedByCrypts: [],
          configuredSecrets: [],
        },
        {
          name: "SafeCrypt",
          type: "crypt",
          summary: "PhotoCloud:vault",
          options: { remote: "PhotoCloud:vault" },
          usedByJobs: [2],
          usedByCrypts: [],
          configuredSecrets: ["password"],
        },
      ];

  const jobs: Record<string, unknown>[] = coldStart
    ? []
    : [
        {
          id: 1,
          enable: 1,
          remark: "家庭相册备份",
          srcPath: JSON.stringify(["/本地/Photos"]),
          dstPath: JSON.stringify(["/PhotoCloud/Backup"]),
          alistId: 1,
          method: 0,
          isCron: 3,
          running: true,
          paused: false,
          syncStatus: {
            state: "syncing",
            pendingChanges: 2,
            conflictCount: 0,
            lastSuccess: nowSec - 180,
            lastPoll: nowSec - 10,
            nextPoll: nowSec + 50,
            error: "",
            failures: 0,
            watchDegraded: true,
            watchDegradedReason: "inotify watch 上限已满，已回退轮询",
          },
        },
        {
          id: 2,
          enable: 0,
          remark: "加密镜像归档",
          srcPath: JSON.stringify(["/本地/Archive"]),
          dstPath: JSON.stringify(["/SafeCrypt/Archive"]),
          alistId: 1,
          method: 1,
          isCron: 3,
          running: false,
          paused: false,
          syncStatus: {
            state: "paused",
            pendingChanges: 0,
            conflictCount: 1,
            lastSuccess: 0,
            lastPoll: 0,
            nextPoll: 0,
            error: "",
            failures: 0,
          },
        },
        {
          id: 3,
          enable: 1,
          remark: "本地双向互备",
          srcPath: JSON.stringify(["/本地/WorkA"]),
          dstPath: JSON.stringify(["/本地/WorkB"]),
          alistId: 1,
          method: 3,
          isCron: 3,
          running: false,
          paused: false,
          syncStatus: {
            state: "monitoring",
            pendingChanges: 0,
            conflictCount: 0,
            lastSuccess: nowSec - 60,
            lastPoll: nowSec - 5,
            nextPoll: nowSec + 55,
            error: "",
            failures: 0,
          },
        },
      ];

  const recentItems = coldStart
    ? []
    : [
        {
          id: 101,
          taskId: 501,
          jobId: 1,
          jobName: "家庭相册备份",
          jobMethod: 0,
          srcPath: "/本地/Photos/IMG_001.jpg",
          dstPath: "/PhotoCloud/Backup/IMG_001.jpg",
          fileName: "IMG_001.jpg",
          fileSize: 4 * 1024 * 1024,
          isPath: 0,
          type: 0,
          status: 2,
          errMsg: "",
          createTime: nowSec - 45,
        },
        {
          id: 102,
          taskId: 503,
          jobId: 3,
          jobName: "本地双向互备",
          jobMethod: 3,
          srcPath: "/本地/WorkA/draft.docx",
          dstPath: "/本地/WorkB/draft.docx",
          fileName: "draft.docx",
          fileSize: 256 * 1024,
          isPath: 0,
          type: 1,
          status: 7,
          errMsg: "目标端拒绝覆盖只读文件",
          createTime: nowSec - 20,
        },
      ];

  await page.route("**/app/opensync/svr/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });

    if (url.pathname.endsWith("/session")) {
      return success({ uid: 1, development: false, version: "test" });
    }
    if (url.pathname.endsWith("/version/latest")) {
      return success({ latestVersion: "1.0.0", hasUpdate: false, releaseURL: "" });
    }
    if (url.pathname.endsWith("/native/storage")) {
      return success({
        engineId: 1,
        enabled: true,
        rcloneAvailable: true,
        remark: "内置存储",
        localRoots: [{ id: "Photos", path: "/vol1/1000/Photos" }],
        remotes,
      });
    }
    if (url.pathname.endsWith("/native/storage/test")) {
      return success({
        name: req.postDataJSON().name,
        ok: true,
        latencyMs: 38,
        message: "连接成功",
      });
    }
    if (url.pathname.endsWith("/native/storage/about")) {
      return success({
        name: url.searchParams.get("name") || "PhotoCloud",
        supported: true,
        total: 100 * 1024 ** 3,
        used: 42 * 1024 ** 3,
        free: 58 * 1024 ** 3,
      });
    }
    if (url.pathname.endsWith("/alist")) {
      if (url.searchParams.get("path")) return success([]);
      return success([
        {
          id: 1,
          kind: "rclone",
          remark: "内置存储",
          url: "rclone://native",
          remotes: { PhotoCloud: "webdav", SafeCrypt: "crypt" },
          health: {
            engineId: 1,
            storages: [
              { mountPath: "PhotoCloud", status: "work", disabled: false },
              { mountPath: "SafeCrypt", status: "work", disabled: false },
            ],
          },
        },
      ]);
    }
    if (url.pathname.endsWith("/dashboard")) {
      return success({
        kpi: {
          totalJobs: jobs.length,
          enabledJobs: jobs.filter((j) => j.enable === 1).length,
          runningJobs: jobs.filter((j) => j.running).length,
          queuedJobs: 0,
          totalEngines: remotes.length,
          onlineEngines: remotes.length,
          offlineEngines: 0,
          todayTasks: recentItems.length,
          todayFailedTasks: 0,
        },
        realtime: {
          totalSpeed: jobs[0]?.running ? 5_242_880 : 0,
          activeJobs: jobs[0]?.running ? 1 : 0,
          activeTransfers: jobs[0]?.running ? 2 : 0,
          totalDoneBytes: 50_000_000,
          totalRemainBytes: 50_000_000,
          estimatedRemainTime: 10,
          sampledAt: nowSec,
          speedHistory: [{ ts: nowSec, speed: jobs[0]?.running ? 5_242_880 : 0 }],
        },
        recent7d: {
          totalRuns: jobs.length,
          successRuns: jobs.length,
          failRuns: 0,
          stoppedRuns: 0,
          totalFiles: 10,
          totalBytes: 4_000_000,
        },
        jobs: jobs.map((j) => ({
          id: j.id,
          name: j.remark,
          enable: j.enable,
          running: j.running,
          paused: j.paused,
          liveSpeed: j.running ? 5_242_880 : 0,
          liveDoneSize: j.running ? 52_428_800 : 0,
          liveTotalSize: j.running ? 104_857_600 : 0,
          liveScanFinish: true,
          syncStatus: j.syncStatus,
        })),
      });
    }
    if (url.pathname.endsWith("/job/items/recent")) {
      const storageFilter = url.searchParams.get("storage");
      const hasErrorFilter = url.searchParams.get("hasError");
      let rows = recentItems.slice();
      if (storageFilter === "__local__") {
        rows = rows.filter((r) => r.jobId === 3);
      } else if (storageFilter === "PhotoCloud") {
        rows = rows.filter((r) => r.jobId === 1);
      } else if (storageFilter === "SafeCrypt") {
        rows = rows.filter((r) => r.jobId === 2);
      }
      if (hasErrorFilter === "1") {
        rows = rows.filter((r) => Boolean(r.errMsg));
      }
      return success({
        dataList: rows,
        count: rows.length,
        pageNum: 1,
        pageSize: 20,
        stats: {
          allNum: rows.length,
          waitNum: 0,
          runningNum: 0,
          successNum: rows.filter((r) => r.status === 2).length,
          failNum: rows.filter((r) => r.status === 7).length,
          otherNum: 0,
          deleteNum: rows.filter((r) => r.type === 1).length,
          errorNum: rows.filter((r) => Boolean(r.errMsg)).length,
          sumSize: rows
            .filter((r) => r.status === 2 && r.type !== 1)
            .reduce((s, r) => s + r.fileSize, 0),
        },
      });
    }
    if (url.pathname.endsWith("/job/batch") && req.method() === "POST") {
      const payload = req.postDataJSON() as { action: string; ids?: number[] };
      batchCalls.push(payload);
      if (payload.action === "disable_all") {
        for (const j of jobs) {
          j.enable = 0;
          j.running = false;
        }
        return success({
          action: "disable_all",
          succeeded: 2,
          failed: 0,
          skipped: 0,
          skippedJobs: [],
          results: [],
        });
      }
      if (payload.action === "enable_all") {
        jobs[0].enable = 1;
        jobs[2].enable = 1;
        return success({
          action: "enable_all",
          succeeded: 2,
          failed: 0,
          skipped: 1,
          skippedJobs: [
            {
              jobId: 2,
              jobName: "加密镜像归档",
              reason: "未完成首次镜像影响预览，已安全跳过",
            },
          ],
          results: [],
        });
      }
      return success({
        action: payload.action,
        succeeded: 1,
        failed: 0,
        results: [],
      });
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("current")) {
      const id = Number(url.searchParams.get("id"));
      if (id === 1 && jobs[0]?.running) {
        return success({
          taskId: 501,
          status: 1,
          paused: false,
          scanFinish: true,
          speed: 5_242_880,
          doneSize: 52_428_800,
          remainSize: 52_428_800,
          remainTime: 10,
          num: { wait: 1, running: 1, success: 8, fail: 0, other: 0 },
          createTime: nowSec - 30,
        });
      }
      return success(null);
    }
    if (
      url.pathname.endsWith("/job") &&
      url.searchParams.has("id") &&
      url.searchParams.has("pageNum")
    ) {
      return success({ dataList: [], count: 0 });
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: jobs, count: jobs.length });
    }
    return success(null);
  });

  return { batchCalls };
}

test("dashboard shows cold-start 3-step onboarding when empty", async ({ page }) => {
  await setupWorkbenchMock(page, { coldStart: true });
  await page.goto("/app/opensync/dashboard");

  const guide = page.locator(".dashboard-onboarding-card");
  await expect(guide).toBeVisible();
  await expect(guide.getByText("确认飞牛本地授权目录")).toBeVisible();
  await expect(guide.getByText("添加云盘或远程存储")).toBeVisible();
  await expect(guide.getByText("创建第一个同步任务")).toBeVisible();
  await guide.getByRole("button", { name: "新建同步任务" }).click();
  await expect(page).toHaveURL(/\/app\/opensync\/sync$/);
  await expect(page.getByRole("dialog", { name: "新建持续同步任务" })).toBeVisible();
});

test("master-detail workbench, drawer watchDegraded banner, global sync bar & recent feed", async ({ page }) => {
  const { batchCalls } = await setupWorkbenchMock(page);

  await page.goto("/app/opensync/sync");
  await expect(page.getByTestId("sync-workbench")).toBeVisible();

  // Global sidebar status bar is active and supports pause/resume all
  const globalBar = page.getByTestId("global-sync-bar");
  await expect(globalBar).toBeVisible();
  await expect(globalBar.getByText("5.00 MB/s")).toBeVisible();
  await expect(globalBar.getByRole("link", { name: "冲突 1" })).toBeVisible();

  await globalBar.getByRole("button", { name: "全部暂停" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "全部暂停" }).click();
  await expect(globalBar.getByText("全局已暂停")).toBeVisible();
  expect(batchCalls.some((c) => c.action === "disable_all")).toBe(true);

  await globalBar.getByRole("button", { name: "全部恢复" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "全部恢复" }).click();
  expect(batchCalls.some((c) => c.action === "enable_all")).toBe(true);

  // Left connection rail switches connections cleanly
  await expect(page.getByTestId("connection-item-PhotoCloud")).toBeVisible();
  await expect(page.getByTestId("connection-item-SafeCrypt")).toBeVisible();
  await expect(page.getByTestId("connection-item-local")).toBeVisible();

  // Crypt badge + deletion policy badge appear in workbench
  await expect(page.getByText("端到端加密")).toBeVisible();
  await expect(page.getByText("保留目标")).toBeVisible();

  // Open slide-out detail drawer right inside /sync and verify watchDegraded warning
  await page.getByRole("button", { name: "详情" }).first().click();
  const drawer = page.locator(".task-detail-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByText(/实时文件监听已降级为定期轮询/)).toBeVisible();
  await expect(drawer.getByRole("link", { name: "完整页面打开" })).toBeVisible();
  await drawer.getByRole("button", { name: "close" }).click();
  await expect(drawer).toHaveCount(0);

  // Switch to Sync History tab filtered to local connection
  await page.getByTestId("connection-item-local").click();
  await page.getByRole("tab", { name: "同步历史" }).click();
  await expect(page.locator(".sync-history-card")).toBeVisible();
  await expect(page.getByText("draft.docx", { exact: true })).toBeVisible();
  await expect(page.getByText("IMG_001.jpg", { exact: true })).toHaveCount(0);
  await page.locator(".sync-history-card").getByRole("link", { name: "查看任务" }).click();
  await expect(drawer.getByRole("tab", { name: "同步记录", selected: true })).toBeVisible();
  expect(new URL(page.url()).searchParams.get("jobId")).toBe("3");
  await drawer.getByRole("button", { name: "close" }).click();
  await expect(drawer).toHaveCount(0);
  await expect(page).toHaveURL(/storage=__local__&view=history$/);

  // Switch to Connection Settings on PhotoCloud and test connection
  await page.getByTestId("connection-item-PhotoCloud").click();
  await page.getByRole("tab", { name: "连接设置" }).click();
  await page.getByRole("button", { name: "测试连接" }).last().click();
  await expect(page.getByText("连接成功（38ms）", { exact: true })).toBeVisible();

  // Dashboard hides cold-start guide when tasks exist & renders recent sync feed
  await page.goto("/app/opensync/dashboard");
  await expect(page.locator(".dashboard-onboarding-card")).toHaveCount(0);
  const feed = page.locator(".dashboard-recent-card");
  await expect(feed).toBeVisible();
  await expect(feed.getByText("IMG_001.jpg", { exact: true })).toBeVisible();
  await feed.getByRole("button", { name: "异常" }).click();
  await expect(feed.getByText("draft.docx", { exact: true })).toBeVisible();
  await feed.getByRole("link", { name: "draft.docx" }).click();
  await expect(drawer.getByRole("tab", { name: "同步记录", selected: true })).toBeVisible();
  await expect(page.getByTestId("connection-item-local")).toHaveAttribute("aria-pressed", "true");
});

test("mobile layout keeps header reachable and zero horizontal overflow", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await setupWorkbenchMock(page);
  await page.goto("/app/opensync/sync");

  await expect(page.getByTestId("global-sync-bar")).toBeVisible();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(overflow).toBe(false);
});
