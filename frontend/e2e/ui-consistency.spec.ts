import { expect, test } from "@playwright/test";

test("dark host theme colors the shell, surfaces, controls, and text together", async ({
  page,
}) => {
  await page.goto("/app/opensync/settings");
  await expect(page.locator(".settings-card")).toHaveCount(2);
  await page.evaluate(() => {
    document.body.setAttribute("theme-mode", "dark");
    document.documentElement.dataset.theme = "dark";
  });

  const colors = await page.evaluate(() => {
    const color = (selector: string, property: "color" | "backgroundColor") =>
      getComputedStyle(document.querySelector(selector) as HTMLElement)[
        property
      ];
    return {
      shell: color(".app-shell", "backgroundColor"),
      workspace: color(".app-workspace", "backgroundColor"),
      card: color(".settings-card", "backgroundColor"),
      input: color(".numeric-setting .semi-input-wrapper", "backgroundColor"),
      text: color(".settings-card h2", "color"),
    };
  });
  for (const surface of [
    colors.shell,
    colors.workspace,
    colors.card,
    colors.input,
  ]) {
    expect(surface).not.toBe("rgb(255, 255, 255)");
    expect(surface).not.toBe("rgb(243, 243, 243)");
  }
  expect(colors.text).not.toBe("rgb(36, 38, 43)");
});

test("small secondary text has readable contrast on a light surface", async ({
  page,
}) => {
  await page.goto("/app/opensync/settings");
  await expect(page.locator(".settings-card")).toHaveCount(2);
  const ratio = await page.evaluate(() => {
    const luminance = (color: string) => {
      const channels = color.match(/\d+/g)!.slice(0, 3).map((value) => {
        const normalized = Number(value) / 255;
        return normalized <= 0.04045
          ? normalized / 12.92
          : ((normalized + 0.055) / 1.055) ** 2.4;
      });
      return (
        channels[0] * 0.2126 +
        channels[1] * 0.7152 +
        channels[2] * 0.0722
      );
    };
    const unit = document.querySelector<HTMLElement>(
      ".numeric-setting > span",
    )!;
    const foreground = luminance(getComputedStyle(unit).color);
    const card = document.querySelector<HTMLElement>(".settings-card")!;
    const background = luminance(getComputedStyle(card).backgroundColor);
    return (
      (Math.max(foreground, background) + 0.05) /
      (Math.min(foreground, background) + 0.05)
    );
  });
  expect(ratio).toBeGreaterThanOrEqual(4.5);
});

test("320px task controls and realtime status tabs stay on one row", async ({
  page,
}) => {
  test.skip(page.viewportSize()?.width !== 320, "narrow mobile layout only");
  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  const toolbar = page.locator(".page-toolbar");
  const toolbarBox = await toolbar.boundingBox();
  const actionBox = await toolbar
    .getByRole("button", { name: "新建任务" })
    .boundingBox();
  const topTabBox = await toolbar.getByRole("tab", { name: "总览" }).boundingBox();
  expect(toolbarBox!.height).toBeLessThanOrEqual(54);
  expect(
    Math.abs(
      actionBox!.y +
        actionBox!.height / 2 -
        topTabBox!.y -
        topTabBox!.height / 2,
    ),
  ).toBeLessThan(8);

  await page.goto("/app/opensync/tasks?jobId=1&tab=realtime");
  const statusTabs = page.locator(".execution-tabs").getByRole("tab");
  await expect(statusTabs).toHaveCount(5);
  const first = await statusTabs.first().boundingBox();
  const last = await statusTabs.last().boundingBox();
  expect(Math.abs(first!.y - last!.y)).toBeLessThan(2);
  await statusTabs.last().scrollIntoViewIfNeeded();
  const scrolledLast = await statusTabs.last().boundingBox();
  const statusBar = await page
    .locator(".execution-tabs .semi-tabs-bar")
    .boundingBox();
  expect(scrolledLast!.x + scrolledLast!.width).toBeLessThanOrEqual(
    statusBar!.x + statusBar!.width + 1,
  );
});

test("320px settings and overview labels keep a single line", async ({
  page,
}) => {
  test.skip(page.viewportSize()?.width !== 320, "narrow mobile layout only");
  await page.goto("/app/opensync/settings");
  await expect(page.locator(".settings-card")).toHaveCount(2);
  const settingLabel = page.locator(".setting-row-label").first();
  const headingBox = await page
    .locator(".settings-card-head h2")
    .first()
    .boundingBox();
  const labelBox = await settingLabel.boundingBox();
  expect(Math.abs(headingBox!.x - labelBox!.x)).toBeLessThanOrEqual(1);
  expect(
    await settingLabel.evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element.firstChild!);
      return range.getClientRects().length;
    }),
  ).toBe(1);
  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  const latestLabel = page
    .locator(".info-row")
    .filter({ hasText: "最近一次执行" })
    .locator(":scope > span");
  await expect(latestLabel).toBeVisible();
  expect(
    await latestLabel.evaluate((element) => {
      const range = document.createRange();
      range.selectNodeContents(element);
      return range.getClientRects().length;
    }),
  ).toBe(1);
});
