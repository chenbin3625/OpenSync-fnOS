import { expect, test, type Page } from "@playwright/test";

const latestURL = "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest";
const repositoryURL = "https://github.com/chenbin3625/OpenSync-fnOS";

async function mockSession(page: Page, local: string) {
  await page.route("**/app/opensync/svr/session", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        code: 200,
        data: { uid: 1, development: false, version: local },
        msg: "success",
      }),
    }),
  );
}

async function mockVersion(
  page: Page,
  result: unknown,
  status = 200,
  local = "0.0.26",
  onCheck?: (url: string) => void,
) {
  await mockSession(page, local);
  // 结尾的 * 让带查询串的手动检查（?refresh=1）也命中同一条 route。
  await page.route("**/app/opensync/svr/version/latest*", (route) => {
    onCheck?.(route.request().url());
    return route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify({
        code: status === 200 ? 200 : 500,
        data: result,
        msg: status === 200 ? "success" : "暂时无法检查最新版本",
      }),
    });
  });
}

/** 按请求 URL 决定返回值的版本接口 mock：手动检查可以拿到与自动检查不同的结果。 */
async function mockVersionByUrl(
  page: Page,
  resolve: (url: string) => unknown,
  local = "0.0.26",
) {
  await mockSession(page, local);
  await page.route("**/app/opensync/svr/version/latest*", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        code: 200,
        data: resolve(route.request().url()),
        msg: "success",
      }),
    }),
  );
}

const noUpdate = {
  latestVersion: "v0.0.26",
  releaseURL: latestURL,
  hasUpdate: false,
};
const updateAvailable = {
  latestVersion: "v0.0.27",
  releaseURL: latestURL,
  hasUpdate: true,
};

/** 版本入口在桌面端位于侧边栏，移动端位于设置页底部。 */
function entry(page: Page) {
  return (page.viewportSize()?.width || 0) <= 640
    ? page.locator(".mobile-version")
    : page.locator(".sidebar-settings");
}

test("desktop sidebar combines GitHub and version with an upgrade icon linking to the release", async ({ page }) => {
  test.skip((page.viewportSize()?.width || 0) <= 640);
  await mockVersion(page, {
    latestVersion: "v0.0.27",
    releaseURL: latestURL,
    hasUpdate: true,
  });
  await page.goto("/app/opensync/tasks");
  const version = page.locator(".sidebar-settings").getByRole("link", {
    name: "GitHub 当前版本 v0.0.26，有新版本 v0.0.27",
  });
  await expect(page.locator(".sidebar-settings").getByRole("link")).toHaveCount(2);
  await expect(version).toContainText("GitHub");
  await expect(version).toContainText("v0.0.26");
  await expect(version).toHaveAttribute("href", latestURL);
  await expect(version).toHaveAttribute("target", "_blank");
  await expect(version.getByRole("status", { name: "有新版本 v0.0.27" })).toBeVisible();
  await expect(version.locator(".semi-icon-arrow_up")).toBeVisible();
  const settings = page.locator(".sidebar-settings").getByRole("link", { name: "设置" });
  const typography = await version.evaluate((el) => {
    const style = getComputedStyle(el);
    const icon = el.querySelector(".semi-icon")!;
    return { fontSize: style.fontSize, fontWeight: style.fontWeight, iconSize: getComputedStyle(icon).fontSize };
  });
  expect(typography).toEqual(await settings.evaluate((el) => {
    const style = getComputedStyle(el);
    const icon = el.querySelector(".semi-icon")!;
    return { fontSize: style.fontSize, fontWeight: style.fontWeight, iconSize: getComputedStyle(icon).fontSize };
  }));
});

test("current version links to the GitHub homepage without an upgrade icon", async ({ page }) => {
  test.skip((page.viewportSize()?.width || 0) <= 640);
  await mockVersion(page, {
    latestVersion: "v0.0.26",
    releaseURL: latestURL,
    hasUpdate: false,
  });
  await page.goto("/app/opensync/settings");
  const version = page.locator(".sidebar-settings").getByRole("link", {
    name: "GitHub 当前版本 v0.0.26",
  });
  await expect(version).toBeVisible();
  await expect(version).toHaveAttribute("href", repositoryURL);
  await expect(version.getByRole("status")).toHaveCount(0);
});

test("failed release check does not hide the installed version or show a false update", async ({ page }) => {
  test.skip((page.viewportSize()?.width || 0) <= 640);
  await mockVersion(page, null, 502);
  await page.goto("/app/opensync/tasks");
  const version = page.locator(".sidebar-settings").getByRole("link", {
    name: "GitHub 当前版本 v0.0.26",
  });
  await expect(version).toBeVisible();
  await expect(version).toHaveAttribute("href", repositoryURL);
  await expect(version.getByRole("status")).toHaveCount(0);
  await expect(page.getByRole("main")).toBeVisible();
});

test("mobile settings expose the same update link", async ({ page }) => {
  test.skip((page.viewportSize()?.width || 0) > 640);
  await mockVersion(page, {
    latestVersion: "v0.0.27",
    releaseURL: latestURL,
    hasUpdate: true,
  });
  await page.goto("/app/opensync/settings");
  const version = page.getByRole("main").getByRole("link", {
    name: "GitHub 当前版本 v0.0.26，有新版本 v0.0.27",
  });
  await expect(version).toHaveAttribute("href", latestURL);
  await expect(version.locator(".semi-icon-arrow_up")).toBeVisible();
  await expect(version.getByRole("status", { name: "有新版本 v0.0.27" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("returning to the app shortly after a check does not check again", async ({ page }) => {
  let checks = 0;
  await mockVersion(page, {
    latestVersion: "v0.0.26",
    releaseURL: latestURL,
    hasUpdate: false,
  }, 200, "0.0.26", () => checks++);
  await page.goto("/app/opensync/settings");
  await expect.poll(() => checks).toBe(2);
  await page.evaluate(() => document.dispatchEvent(new Event("visibilitychange")));
  await expect.poll(() => checks).toBe(2);
});

test("local development build displays dev without a version prefix", async ({ page }) => {
  await mockVersion(page, {
    latestVersion: "v0.0.26",
    releaseURL: latestURL,
    hasUpdate: false,
  }, 200, "dev");
  await page.goto("/app/opensync/settings");
  const version = page.viewportSize()!.width <= 640
    ? page.getByRole("main").getByRole("link", { name: "GitHub 当前版本 dev" })
    : page.locator(".sidebar-settings").getByRole("link", { name: "GitHub 当前版本 dev" });
  await expect(version).toBeVisible();
  await expect(version).toHaveAttribute("href", repositoryURL);
});

test("有新版本时入口显示蓝色圆形升级图标，右上角弹出可点击的升级提醒", async ({ page }) => {
  test.skip((page.viewportSize()?.width || 0) <= 640);
  await mockVersion(page, updateAvailable);
  await page.goto("/app/opensync/tasks");

  const badge = page.locator(".sidebar-settings .version-update");
  await expect(badge).toBeVisible();
  const badgeStyle = await badge.evaluate((el) => {
    const style = getComputedStyle(el);
    return {
      background: style.backgroundColor,
      color: style.color,
      width: style.width,
      height: style.height,
      radius: style.borderRadius,
    };
  });
  // 蓝底 + 白色图标 + 正圆
  expect(badgeStyle.background).toBe("rgb(0, 102, 255)");
  expect(badgeStyle.color).toBe("rgb(255, 255, 255)");
  expect(badgeStyle.width).toBe(badgeStyle.height);
  expect(parseFloat(badgeStyle.radius)).toBeGreaterThanOrEqual(
    parseFloat(badgeStyle.width) / 2,
  );

  const notice = page.locator(".update-notice");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("发现新版本 v0.0.27");
  await expect(notice).toContainText("当前版本 v0.0.26");
  await expect(page.locator(".update-notice-link")).toHaveAttribute("href", latestURL);
  const box = (await notice.boundingBox())!;
  const viewport = page.viewportSize()!;
  // 右上角：贴着右侧、靠近顶部
  expect(box.x + box.width).toBeGreaterThan(viewport.width * 0.7);
  expect(box.y).toBeLessThan(viewport.height * 0.3);

  // 点卡片任意位置都能去 GitHub 下载。GitHub 会把 /releases/latest 302 到具体
  // tag，所以这里拦下目标页，断言应用真正请求的就是发布页地址。
  await page.context().route(latestURL, (route) =>
    route.fulfill({ contentType: "text/html", body: "<title>release</title>" }),
  );
  const popupPromise = page.waitForEvent("popup");
  await notice.locator(".semi-notification-notice-title").click();
  const popup = await popupPromise;
  await popup.waitForLoadState("domcontentloaded");
  expect(popup.url()).toBe(latestURL);
});

test("手动「检查更新」绕过后端缓存，发现新版本后同时提示图标与提醒", async ({ page }) => {
  const requested: string[] = [];
  await mockVersionByUrl(page, (url) => {
    requested.push(url);
    return url.includes("refresh=1") ? updateAvailable : noUpdate;
  });
  await page.goto("/app/opensync/settings");
  const scope = entry(page);
  await expect(scope.locator(".version-update")).toHaveCount(0);
  await expect(page.locator(".update-notice")).toHaveCount(0);

  const button = scope.getByRole("button", { name: "检查更新" });
  await expect(button).toBeVisible();
  await button.click();
  await expect
    .poll(() => requested.filter((url) => url.includes("refresh=1")).length)
    .toBe(1);
  await expect(scope.locator(".version-update")).toBeVisible();
  await expect(page.locator(".update-notice")).toContainText("发现新版本 v0.0.27");
  await expect(button).toBeEnabled();
});

test("手动「检查更新」在已是最新版本时给出提示，不弹升级提醒", async ({ page }) => {
  const requested: string[] = [];
  await mockVersionByUrl(page, (url) => {
    requested.push(url);
    return noUpdate;
  });
  await page.goto("/app/opensync/settings");
  const button = entry(page).getByRole("button", { name: "检查更新" });
  await button.click();
  await expect
    .poll(() => requested.filter((url) => url.includes("refresh=1")).length)
    .toBe(1);
  await expect(page.locator(".semi-toast-content")).toContainText(
    "已是最新版本 v0.0.26",
  );
  await expect(page.locator(".update-notice")).toHaveCount(0);
});

test("「检查更新」图标 hover 展示中文提示", async ({ page }) => {
  await mockVersion(page, noUpdate);
  await page.goto("/app/opensync/settings");

  await entry(page).getByRole("button", { name: "检查更新" }).hover();

  const tooltip = page
    .locator(".semi-tooltip-wrapper")
    .filter({ hasText: "检查更新" });
  await expect(tooltip).toBeVisible();
  await expect(tooltip.locator(".semi-tooltip-content")).toHaveText("检查更新");
});

test("版本号使用 Tag 组件展示", async ({ page }) => {
  await mockVersion(page, noUpdate);
  await page.goto("/app/opensync/settings");

  const versionTag = entry(page)
    .getByRole("link", { name: "GitHub 当前版本 v0.0.26" })
    .locator(".semi-tag");
  await expect(versionTag).toBeVisible();
  await expect(versionTag).toHaveText("v0.0.26");
});
