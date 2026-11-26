import { test, expect } from "@playwright/test";

test("connects a first WebDAV account inline and reuses it from the sync home", async ({ page }) => {
  const jobs: Record<string, unknown>[] = [];
  const savedJobs: Record<string, unknown>[] = [];
  let cloud = "";
  let localAuthorized = false;
  await page.route("**/app/opensync/svr/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const success = (data: unknown) => route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ code: 200, data, msg: "" }),
    });
    if (url.pathname.endsWith("/session")) return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/native/storage")) {
      if (req.method() === "POST") cloud = req.postDataJSON().name;
      return success({
        engineId: 1, enabled: true, rcloneAvailable: true, remark: "内置存储",
        localRoots: localAuthorized ? [{ id: "Photos", path: "/vol1/Photos" }] : [],
        remotes: cloud ? [{
          name: cloud, type: "webdav", summary: "", options: { vendor: "other" },
          usedByJobs: [], usedByCrypts: [], configuredSecrets: [],
        }] : [],
      });
    }
    if (url.pathname.endsWith("/alist")) {
      const path = url.searchParams.get("path");
      if (path === "/") return success([{ path: "本地" }, { path: cloud }]);
      if (path === "/本地") return success([{ path: "Photos" }]);
      if (path === `/${cloud}`) return success([{ path: "Backup" }]);
      if (path) return success([]);
      return success([{ id: 1, remark: "内置存储", url: "native://rclone" }]);
    }
    if (url.pathname.endsWith("/job") && req.method() === "POST") {
      const data = req.postDataJSON();
      const id = jobs.length + 1;
      savedJobs.push(data);
      jobs.push({ ...data, id, srcPath: JSON.stringify(data.srcPath), dstPath: JSON.stringify(data.dstPath) });
      return success({ id });
    }
    if (url.pathname.endsWith("/job") && req.method() === "PUT") {
      jobs[0].enable = req.postDataJSON().pause ? 0 : 1;
      return success(null);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("current")) return success(null);
    if (url.pathname.endsWith("/job")) return success({ dataList: jobs, count: jobs.length });
    return success(null);
  });

  await page.goto("/app/opensync/");
  await expect(page.getByRole("heading", { name: "同步任务", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "添加网盘连接", exact: true }).click();
  await dialog.getByRole("button", { name: "OpenList / AList (WebDAV)", exact: true }).click();
  await dialog.getByRole("textbox", { name: "存储名称", exact: true }).fill("PhotoCloud");
  await dialog.getByRole("button", { name: "保存存储", exact: true }).click();
  await expect(dialog.getByRole("heading", { name: "新建持续同步任务", exact: true })).toBeVisible();
  await expect(dialog.getByRole("status").filter({ hasText: "文件夹访问权限" })).toBeVisible();
  localAuthorized = true;
  await dialog.getByRole("button", { name: "刷新目录" }).click();
  await dialog.getByRole("radio", { name: /仅上传本地更改/ }).click();

  const picker = dialog.locator(".remote-paths");
  await picker.nth(0).getByRole("combobox").click();
  await page.locator('.remote-paths-dropdown [role="treeitem"][data-key="/本地/Photos"]').click();
  await picker.nth(1).getByRole("combobox").click();
  await page.locator('.remote-paths-dropdown [role="treeitem"][data-key="/PhotoCloud/Backup"]').click();
  await expect(dialog.getByRole("textbox", { name: "任务名称" })).toHaveAttribute("placeholder", "Photos · PhotoCloud");
  await dialog.getByRole("button", { name: "下一步", exact: true }).click();
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(page).toHaveURL(/\/tasks\?jobId=1/);
  expect(savedJobs[0]).toMatchObject({
    remark: "Photos · PhotoCloud", srcPath: ["/本地/Photos"], dstPath: ["/PhotoCloud/Backup"], method: 0, isCron: 3, enable: 1,
  });

  await page.goto("/app/opensync/sync");
  const group = page.locator(".sync-connection-group").filter({ hasText: "PhotoCloud" });
  await expect(group.getByRole("link", { name: "Photos · PhotoCloud" })).toBeVisible();
  await group.getByRole("link", { name: "Photos · PhotoCloud" }).click();
  const detail = page.locator(".overview-card .overview-head-actions");
  await detail.getByRole("button", { name: "暂停同步", exact: true }).click();
  await detail.getByRole("button", { name: "恢复同步", exact: true }).click();
  await expect(detail.getByRole("button", { name: "暂停同步", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "返回任务列表" }).click();
  await group.getByRole("button", { name: "添加同步任务" }).click();
  await expect(dialog.getByRole("heading", { name: "新建持续同步任务", exact: true })).toBeVisible();
  await dialog.getByRole("combobox", { name: "选择目标目录" }).click();
  await expect(page.locator('.remote-paths-dropdown [role="treeitem"][data-key="/PhotoCloud/Backup"]')).toBeVisible();
});

test("edits Cloud Sync continuous direction, deletion policy and polling without persisting runtime state", async ({ page }) => {
  let saved: Record<string, unknown> | undefined;
  let enabled = true;
  let previews = 0;
  await page.route("**/app/opensync/svr/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const success = (data: unknown) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ code: 200, data, msg: "" }) });
    if (url.pathname.endsWith("/session")) return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) return success([]);
    if (url.pathname.endsWith("/alist")) return success([{ id: 1, remark: "测试引擎", url: "https://engine.test" }]);
    if (url.pathname.endsWith("/job/dry-run")) {
      previews++;
      return success({
        jobId: 9, status: "completed", method: 1, copyCount: 0, updateCount: 0,
        deleteCount: 1, moveCount: 0, mkdirCount: 0, items: [],
      });
    }
    if (url.pathname.endsWith("/job") && req.method() === "POST") {
      saved = req.postDataJSON();
      enabled = saved?.enable === 1;
      return success(null);
    }
    if (url.pathname.endsWith("/job") && req.method() === "PUT") {
      enabled = req.postDataJSON().pause === false;
      return success(null);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("current")) return success(null);
    if (url.pathname.endsWith("/job")) return success({ dataList: [{
      id: 9, enable: Number(enabled), remark: "持续同步任务", alistId: 1,
      srcPath: '["/本地/root/source"]', dstPath: '["/Cloud/target"]', method: saved?.method ?? 0, isCron: 3, exclude: "",
      extraFlags: '{"syncDirection":"upload","syncPollIntervalSec":60,"syncAuditIntervalMin":1440}',
      syncStatus: { state: "synced", pendingChanges: 0, lastSuccess: 100, lastPoll: 100, nextPoll: 160, error: "", failures: 0 },
    }], count: 1 });
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=9");
  await expect(page.locator(".continuous-status-card")).toContainText("已同步");
  await expect(page.locator(".overview-card").getByRole("button", { name: "暂停同步", exact: true })).toBeVisible();
  await expect(page.locator(".overview-card").getByRole("button", { name: "立即同步", exact: true })).toBeVisible();

  await page.locator(".overview-card").getByRole("button", { name: "编辑任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("radio", { name: /仅下载远端更改/ }).click();
  await dialog.getByRole("switch", { name: "镜像对齐", exact: true }).click();
  await dialog.getByRole("tab", { name: "同步设置", exact: true }).click();
  await dialog.getByRole("textbox", { name: "远端检测间隔（秒）", exact: true }).fill("15");
  await dialog.getByRole("textbox", { name: "完整核对间隔（分钟）", exact: true }).fill("60");
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog.getByRole("button", { name: "确认并启动镜像", exact: true })).toBeVisible();
  expect(saved).toMatchObject({ enable: 0, isCron: 3, method: 1, srcPath: ["/Cloud/target"], dstPath: ["/本地/root/source"] });
  expect(JSON.parse(String(saved?.extraFlags))).toMatchObject({ syncDirection: "download", syncPollIntervalSec: 15, syncAuditIntervalMin: 60 });
  expect(saved).not.toHaveProperty("syncStatus");
  expect(previews).toBe(1);
  await dialog.getByRole("button", { name: "确认并启动镜像", exact: true }).click();
  await expect(dialog).toBeHidden();
  expect(enabled).toBe(true);

  await page.getByRole("button", { name: "返回任务列表", exact: true }).click();
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  await expect(page.getByRole("dialog").getByRole("radiogroup", { name: "同步方向" })).toBeVisible();
  await expect(page.getByRole("dialog").getByRole("switch", { name: "镜像对齐", exact: true })).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "取消", exact: true }).click();
});

test("offers one-click fix when switching a multi-destination task to bidirectional sync", async ({ page }) => {
  let saved: Record<string, unknown> | undefined;
  await page.route("**/app/opensync/svr/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const success = (data: unknown) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ code: 200, data, msg: "" }) });
    if (url.pathname.endsWith("/session")) return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) return success([]);
    if (url.pathname.endsWith("/alist")) return success([{ id: 1, remark: "测试引擎", url: "https://engine.test" }]);
    if (url.pathname.endsWith("/job") && ["POST", "PUT"].includes(req.method())) {
      saved = req.postDataJSON();
      return success(null);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.has("current")) return success(null);
    if (url.pathname.endsWith("/job")) return success({ dataList: [{
      id: 10, enable: 1, remark: "多目标持续同步", alistId: 1,
      srcPath: '["/Source"]', dstPath: '["/Backup1","/Backup2"]', method: 0, isCron: 3, exclude: "",
    }], count: 1 });
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=10");
  await page.locator(".overview-card").getByRole("button", { name: "编辑任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("radio", { name: /双向同步/ }).click();

  const preview = dialog.getByRole("region", { name: "同步位置预览" });
  await expect(preview.getByRole("alert")).toContainText("双向同步模式只允许一个源目录和一个目标目录");
  await preview.getByRole("button", { name: "仅保留首个目标目录", exact: true }).click();
  await expect(preview.getByRole("alert")).toHaveCount(0);

  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toBeHidden();
  expect(saved).toMatchObject({ isCron: 3, method: 3, dstPath: ["/Backup1"] });
});
