import { expect, test, type Page } from "@playwright/test";

const latestURL = "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest";
const repositoryURL = "https://github.com/chenbin3625/OpenSync-fnOS";

async function mockVersion(
  page: Page,
  result: unknown,
  status = 200,
  local = "0.0.26",
  onCheck?: () => void,
) {
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
  await page.route("**/app/opensync/svr/version/latest", (route) => {
    onCheck?.();
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
