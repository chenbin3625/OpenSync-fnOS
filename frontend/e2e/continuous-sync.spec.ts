import { test, expect } from "@playwright/test";

test("edits Cloud Sync continuous direction, deletion policy and polling without persisting runtime state", async ({ page }) => {
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
      id: 9, enable: 1, remark: "持续同步任务", alistId: 1,
      srcPath: '["/本地/root/source"]', dstPath: '["/Cloud/target"]', method: 0, isCron: 3, exclude: "",
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
  await dialog.getByRole("switch", { name: "同步删除", exact: true }).click();
  await dialog.getByRole("tab", { name: "同步设置", exact: true }).click();
  await dialog.getByRole("textbox", { name: "远端检测间隔（秒）", exact: true }).fill("15");
  await dialog.getByRole("textbox", { name: "完整核对间隔（分钟）", exact: true }).fill("60");
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toBeHidden();
  expect(saved).toMatchObject({ isCron: 3, method: 1, srcPath: ["/Cloud/target"], dstPath: ["/本地/root/source"] });
  expect(JSON.parse(String(saved?.extraFlags))).toMatchObject({ syncDirection: "download", syncPollIntervalSec: 15, syncAuditIntervalMin: 60 });
  expect(saved).not.toHaveProperty("syncStatus");

  await page.getByRole("button", { name: "新建任务", exact: true }).click();
  await expect(page.getByRole("dialog").getByRole("radiogroup", { name: "同步方向" })).toBeVisible();
  await expect(page.getByRole("dialog").getByRole("switch", { name: "同步删除", exact: true })).toBeVisible();
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
