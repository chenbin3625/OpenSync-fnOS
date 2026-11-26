import { test, expect } from "@playwright/test";

test("edits continuous direction, deletion policy and polling without persisting runtime state", async ({ page }) => {
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
  await expect(page.locator(".overview-card").getByText("已同步", { exact: true })).toBeVisible();
  await page.locator(".overview-card").getByRole("button", { name: "编辑任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: "同步方向", exact: true }).click();
  await page.getByRole("option").filter({ hasText: "下载远端变更（远端 → 本地）" }).click();
  await dialog.getByRole("switch", { name: "同步删除", exact: true }).click();
  await dialog.getByRole("tab", { name: "执行计划", exact: true }).click();
  await dialog.getByRole("textbox", { name: "远端检测间隔（秒）", exact: true }).fill("15");
  await dialog.getByRole("textbox", { name: "完整核对间隔（分钟）", exact: true }).fill("60");
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toBeHidden();
  expect(saved).toMatchObject({ isCron: 3, method: 1, srcPath: ["/Cloud/target"], dstPath: ["/本地/root/source"] });
  expect(JSON.parse(String(saved?.extraFlags))).toMatchObject({ syncDirection: "download", syncPollIntervalSec: 15, syncAuditIntervalMin: 60 });
  expect(saved).not.toHaveProperty("syncStatus");

  await page.getByRole("button", { name: "新建任务", exact: true }).click();
  await expect(page.getByRole("dialog").getByRole("combobox", { name: "同步方式", exact: true })).toContainText("单向同步");
  await expect(page.getByRole("dialog").getByRole("switch", { name: "同步删除", exact: true })).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "取消", exact: true }).click();
});
