import { expect, test } from "@playwright/test";

const now = Math.floor(Date.now() / 1000);

test("renders redesigned /sync overview with live progress, quick actions, and status pills", async ({
  page,
}) => {
  let jobs: Array<Record<string, unknown>> = [
    {
      id: 1,
      enable: 1,
      remark: "家庭相册实时镜像",
      alistId: 1,
      method: 1,
      isCron: 3,
      interval: 0,
      bwLimit: "20M",
      trashEnable: 1,
      windowEnable: 1,
      windowStart: "22:00",
      windowEnd: "07:00",
      srcPath: '["/本地/Photos"]',
      dstPath: '["/CloudVault/Photos"]',
      running: true,
      queued: false,
      paused: false,
      syncStatus: {
        state: "syncing",
        pendingChanges: 0,
        lastSuccess: now - 3600,
        lastPoll: now - 10,
        nextPoll: now + 50,
        error: "",
        failures: 0,
      },
    },
    {
      id: 2,
      enable: 1,
      remark: "工作资料双向同步",
      alistId: 1,
      method: 3,
      isCron: 3,
      interval: 0,
      srcPath: '["/CloudVault/Docs"]',
      dstPath: '["/本地/Docs"]',
      running: false,
      queued: false,
      syncStatus: {
        state: "synced",
        pendingChanges: 0,
        lastSuccess: now - 300,
        lastPoll: now - 15,
        nextPoll: now + 45,
        error: "",
        failures: 0,
        conflictCount: 2,
      },
    },
    {
      id: 3,
      enable: 1,
      remark: "异地冷备任务",
      alistId: 1,
      method: 0,
      isCron: 1,
      interval: 60,
      srcPath: '["/本地/Archive"]',
      dstPath: '["/BackupWebDAV/Archive"]',
      running: false,
      queued: false,
      lastStatus: 5,
      syncStatus: {
        state: "error",
        pendingChanges: 0,
        lastSuccess: 0,
        lastPoll: now - 20,
        nextPoll: 0,
        error: "WebDAV 写入超时：504 Gateway Timeout",
        failures: 3,
      },
    },
    {
      id: 4,
      enable: 0,
      remark: "本地归档手动任务",
      alistId: 1,
      method: 0,
      isCron: 2,
      interval: 0,
      srcPath: '["/本地/Src"]',
      dstPath: '["/本地/Dst"]',
      running: false,
      queued: false,
    },
  ];

  let livePaused = false;
  const putPayloads: Array<Record<string, unknown>> = [];
  const deletedIds: string[] = [];

  await page.route("**/app/opensync/svr/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });

    if (url.pathname.endsWith("/session")) {
      return success({ uid: 1, development: false, version: "0.0.56" });
    }
    if (url.pathname.endsWith("/native/storage")) {
      return success({
        engineId: 1,
        enabled: true,
        rcloneAvailable: true,
        remark: "内置存储",
        localRoots: [{ id: "Photos", path: "/vol1/Photos" }],
        remotes: [
          {
            name: "CloudVault",
            type: "webdav",
            summary: "https://dav.example.test",
            options: {},
            usedByJobs: [1, 2],
            usedByCrypts: [],
            configuredSecrets: [],
          },
          {
            name: "BackupWebDAV",
            type: "s3",
            summary: "s3.example.test",
            options: {},
            usedByJobs: [3],
            usedByCrypts: [],
            configuredSecrets: [],
          },
          {
            name: "EmptyRemote",
            type: "webdav",
            summary: "",
            options: {},
            usedByJobs: [],
            usedByCrypts: [],
            configuredSecrets: [],
          },
        ],
      });
    }
    if (url.pathname.endsWith("/alist")) {
      if (url.searchParams.has("path")) return success([]);
      return success([
        {
          id: 1,
          remark: "内置存储",
          url: "native://rclone",
          health: {
            alistId: 1,
            status: "online",
            latencyMs: 5,
            lastCheckedAt: now,
            storages: [
              { mountPath: "/CloudVault", driver: "webdav", status: "work", disabled: false },
              { mountPath: "/BackupWebDAV", driver: "s3", status: "error", disabled: false },
              { mountPath: "/本地", driver: "local", status: "work", disabled: false },
            ],
          },
        },
      ]);
    }
    if (url.pathname.endsWith("/dashboard")) {
      const runningJob = jobs.find((j) => j.id === 1);
      return success({
        kpi: {
          totalJobs: jobs.length,
          enabledJobs: jobs.filter((j) => j.enable === 1).length,
          runningJobs: runningJob?.running ? 1 : 0,
          queuedJobs: 0,
          totalEngines: 1,
          onlineEngines: 1,
          offlineEngines: 0,
        },
        jobs: runningJob?.running
          ? [
              {
                id: 1,
                remark: runningJob.remark,
                enable: 1,
                alistId: 1,
                alistName: "内置存储",
                method: 1,
                isCron: 3,
                interval: 0,
                srcPath: runningJob.srcPath,
                dstPath: runningJob.dstPath,
                running: true,
                queued: false,
                paused: livePaused,
                currentTaskId: 501,
                liveNum: { wait: 4, running: 2, success: 14, fail: 0, other: 0 },
                liveProgress: 72,
                liveDoneSize: 720 * 1024 * 1024,
                liveTotalSize: 1000 * 1024 * 1024,
                liveSpeed: livePaused ? 0 : 24.6 * 1024 * 1024,
                liveRemainTime: 12,
                lastTask: null,
              },
            ]
          : [],
      });
    }
    if (url.pathname.endsWith("/job") && req.method() === "PUT") {
      const payload = req.postDataJSON() as Record<string, unknown>;
      putPayloads.push(payload);
      if (payload.taskId === "501" && payload.action === "pause") {
        livePaused = true;
        jobs = jobs.map((j) => (j.id === 1 ? { ...j, paused: true } : j));
      } else if (payload.taskId === "501" && payload.action === "resume") {
        livePaused = false;
        jobs = jobs.map((j) => (j.id === 1 ? { ...j, paused: false } : j));
      } else if (payload.taskId === "501" && payload.action === "stop") {
        jobs = jobs.map((j) =>
          j.id === 1 ? { ...j, running: false, paused: false } : j,
        );
      } else if (payload.pause !== undefined) {
        const targetId = Number(payload.id);
        jobs = jobs.map((j) =>
          j.id === targetId ? { ...j, enable: payload.pause ? 0 : 1 } : j,
        );
      }
      return success(null);
    }
    if (url.pathname.endsWith("/job") && req.method() === "DELETE") {
      const id = url.searchParams.get("id") || "";
      deletedIds.push(id);
      jobs = jobs.filter((j) => String(j.id) !== id);
      return success(null);
    }
    if (url.pathname.endsWith("/job") && !url.searchParams.has("current")) {
      return success({ dataList: jobs, count: jobs.length });
    }
    return success(null);
  });

  await page.goto("/app/opensync/sync");

  // Status summary bar pills & global batch controls
  const pillGroup = page.getByRole("group", { name: "同步状态概览" });
  await expect(pillGroup).toBeVisible();
  await expect(pillGroup.getByRole("button", { name: /全部\s*4/ })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await expect(pillGroup.getByRole("button", { name: /同步中\s*1/ })).toBeVisible();
  await expect(pillGroup.getByRole("button", { name: /已同步\s*1/ })).toBeVisible();
  await expect(pillGroup.getByRole("button", { name: /同步异常\s*1/ })).toBeVisible();
  await expect(pillGroup.getByRole("button", { name: /已暂停\s*1/ })).toBeVisible();
  await expect(page.getByRole("button", { name: "全部立即同步" })).toBeVisible();
  await expect(page.getByRole("button", { name: "全部中止" })).toBeVisible();

  // Group headers & empty group spacing
  const backupGroup = page
    .locator(".sync-connection-group")
    .filter({ hasText: "BackupWebDAV" });
  await expect(backupGroup).toContainText("连接异常");
  const localGroup = page
    .locator(".sync-connection-group")
    .filter({ hasText: "本地目录" });
  await expect(localGroup).toContainText("已授权 1 个目录");
  const emptyGroup = page
    .locator(".sync-connection-group")
    .filter({ hasText: "EmptyRemote" });
  await expect(emptyGroup).toContainText("此连接还没有同步任务");

  // Live running task displays speed, pct & transfer pause/stop controls
  const liveRow = page
    .locator(".sync-overview-row")
    .filter({ hasText: "家庭相册实时镜像" });
  await expect(liveRow).toContainText("正在同步");
  await expect(liveRow).toContainText("24.60 MB/s");
  await expect(liveRow).toContainText("72%");
  await expect(liveRow).toContainText("限速 20 MB/s");
  await expect(liveRow).toContainText("回收站");

  await liveRow.getByRole("button", { name: "暂停传输" }).click();
  await expect.poll(() => putPayloads.at(-1)).toEqual({
    taskId: "501",
    action: "pause",
  });
  await expect(liveRow.getByRole("button", { name: "继续传输" })).toBeVisible();

  // Conflict CTA & Quick Actions on idle row
  const docsRow = page
    .locator(".sync-overview-row")
    .filter({ hasText: "工作资料双向同步" });
  await expect(docsRow).toContainText("存在 2 项冲突待处理");
  await expect(docsRow.getByRole("link", { name: "查看冲突" })).toBeVisible();

  await docsRow.getByRole("button", { name: "立即同步" }).click();
  await expect.poll(() => putPayloads.at(-1)).toEqual({ id: "2" });

  await docsRow.getByRole("button", { name: "暂停同步" }).click();
  await expect.poll(() => putPayloads.at(-1)).toEqual({ id: "2", pause: true });

  // Clone via ActionMenu opens JobEditor prefilled
  await docsRow.getByRole("button", { name: "更多操作" }).click();
  await page.getByRole("menuitem", { name: "复制任务" }).click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByRole("heading", { name: "复制同步任务", exact: true }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await page.getByRole("button", { name: "放弃", exact: true }).click();
  await expect(dialog).toBeHidden();

  // Status pill filtering & filter-empty recovery
  await pillGroup.getByRole("button", { name: /同步异常\s*1/ }).click();
  await expect(page).toHaveURL(/\/sync\?status=failed$/);
  await expect(page.locator(".sync-overview-row")).toHaveCount(1);
  await expect(page.locator(".sync-overview-row")).toContainText("异地冷备任务");

  await page.getByPlaceholder("搜索任务名称或路径").fill("不存在的任务关键词");
  await expect(page.locator(".sync-filter-empty")).toBeVisible();
  await page.getByRole("button", { name: "清空筛选条件" }).click();
  await expect(page.locator(".sync-overview-row")).toHaveCount(4);

  // Stop running batch with confirmation modal
  await liveRow.getByRole("button", { name: "中止本次同步" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "中止本次同步" }).click();
  await expect.poll(() => putPayloads.at(-1)).toEqual({
    taskId: "501",
    action: "stop",
  });

  // Delete failed task via ActionMenu with confirmation modal
  const failedRow = page
    .locator(".sync-overview-row")
    .filter({ hasText: "异地冷备任务" });
  await failedRow.getByRole("button", { name: "更多操作" }).click();
  await page.getByRole("menuitem", { name: "删除任务" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "删除", exact: true }).click();
  await expect.poll(() => deletedIds).toEqual(["3"]);

  // Zero horizontal overflow in light and dark modes across all viewports
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);

  await page.evaluate(() => {
    document.body.setAttribute("theme-mode", "dark");
    document.documentElement.dataset.theme = "dark";
  });
  await expect(page.locator(".sync-list-controls")).toHaveCSS(
    "background-color",
    "rgb(35, 36, 41)",
  );
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
});

test("shows helpful onboarding CTAs when /sync has no jobs or local folders", async ({
  page,
}) => {
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session")) {
      return success({ uid: 1, development: false, version: "0.0.56" });
    }
    if (url.pathname.endsWith("/native/storage")) {
      return success({
        engineId: 1,
        enabled: true,
        rcloneAvailable: true,
        remark: "内置存储",
        localRoots: [],
        remotes: [],
      });
    }
    if (url.pathname.endsWith("/alist")) {
      return success([{ id: 1, remark: "内置存储", url: "native://rclone" }]);
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: [], count: 0 });
    }
    return success(null);
  });

  await page.goto("/app/opensync/sync");
  await expect(page.getByText(/尚未配置存储连接或本地授权目录/)).toBeVisible();
  await page.getByRole("button", { name: "前往连接设置", exact: true }).click();
  await expect(page).toHaveURL(/\/sync\?view=settings$/);
  await expect(page.getByTestId("sync-workbench-settings")).toBeVisible();
});

test("shows status dot on mobile job chips and avoids /tasks header overflow", async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 640 });
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session")) {
      return success({ uid: 1, development: false, version: "0.0.56" });
    }
    if (url.pathname.endsWith("/alist")) {
      return success([{ id: 1, remark: "内置存储", url: "native://rclone" }]);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("current")) {
      return success(null);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "超长名称持续同步任务测试移动端排版",
            alistId: 1,
            method: 0,
            isCron: 3,
            srcPath: '["/本地/Photos"]',
            dstPath: '["/CloudVault/Photos"]',
            running: true,
          },
          {
            id: 2,
            enable: 0,
            remark: "已暂停任务",
            alistId: 1,
            method: 0,
            isCron: 3,
            srcPath: '["/本地/Docs"]',
            dstPath: '["/CloudVault/Docs"]',
          },
        ],
        count: 2,
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=1");
  const chips = page.locator(".mobile-job-chip");
  await expect(chips).toHaveCount(2);
  await expect(chips.first().locator(".job-dot--running")).toBeVisible();
  await expect(chips.nth(1).locator(".job-dot--disabled")).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
});
