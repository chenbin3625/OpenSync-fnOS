import { expect, test } from "@playwright/test";

test("cloud browse appends provider pages and searches the complete directory", async ({ page }) => {
  const browseRequests: { path: string; paged?: boolean; cursor?: string; fresh?: boolean }[] = [];
  const directory = "/云盘";
  const entries = ["alpha.txt", "zulu.txt"].map((name) => ({
    path: `${directory}/${name}`,
    name,
    isDir: false,
    size: 12,
    modTime: 1_700_000_000,
  }));
  await page.route("**/app/opensync/svr/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let data: unknown = null;
    if (path.endsWith("/session")) {
      data = { uid: 1, development: false, version: "test" };
    } else if (path.endsWith("/alist")) {
      data = [{ id: 1, remark: "内置存储", url: "native://rclone" }];
    } else if (path.endsWith("/job")) {
      data = { dataList: [], count: 0 };
    } else if (path.endsWith("/native/storage")) {
      data = {
        engineId: 1,
        enabled: true,
        remark: "内置存储",
        maxConcurrentJobs: 1,
        rcloneAvailable: true,
        localRoots: [],
        remotes: [{ name: "云盘", type: "quark", options: {}, configuredSecrets: ["cookie"], usedByJobs: [], usedByCrypts: [] }],
      };
    } else if (path.endsWith("/native/storage/browse")) {
      const body = request.postDataJSON();
      browseRequests.push(body);
      data = !body.paged
        ? { path: directory, entries }
        : body.cursor
          ? { path: directory, entries: entries.slice(1) }
          : { path: directory, entries: entries.slice(0, 1), nextCursor: "2" };
    }
    await route.fulfill({ json: { code: 200, data, msg: "" } });
  });

  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "浏览与恢复 云盘" }).click();
  const dialog = page.getByRole("dialog");
  const more = dialog.getByRole("button", { name: "加载更多" });
  await expect(dialog.getByText("alpha.txt", { exact: true })).toBeVisible();
  await expect(dialog.getByText("zulu.txt", { exact: true })).toHaveCount(0);
  expect(browseRequests).toEqual([{ path: directory, paged: true }]);

  await more.click();
  await expect(dialog.getByText("zulu.txt", { exact: true })).toBeVisible();
  await expect(dialog.getByText("alpha.txt", { exact: true })).toBeVisible();
  await expect(more).toHaveCount(0);
  expect(browseRequests.at(-1)).toEqual({ path: directory, paged: true, cursor: "2" });

  await dialog.getByRole("button", { name: "刷新", exact: true }).click();
  await expect(more).toBeVisible();
  expect(browseRequests.at(-1)).toEqual({ path: directory, paged: true, fresh: true });
  await dialog.getByPlaceholder("搜索当前目录文件/文件夹…").fill("zulu");
  await expect(dialog.getByText("zulu.txt", { exact: true })).toBeVisible();
  await expect(dialog.getByText("alpha.txt", { exact: true })).toHaveCount(0);
  expect(browseRequests.at(-1)).toEqual({ path: directory, paged: false });
});
