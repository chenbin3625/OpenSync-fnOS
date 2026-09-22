import { test, expect } from "@playwright/test";

test("task editor keeps size filters disabled until selected", async ({
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
      return success({ uid: 1, development: false, version: "test" });
    }
    if (
      url.pathname.endsWith("/alist") &&
      url.searchParams.has("alistId")
    ) {
      const path = url.searchParams.get("path");
      if (path === "/Photos") {
        return success([{ name: "Albums" }, { name: "Raw" }]);
      }
      if (path === "/Photos/Albums") {
        return success([{ name: "2024" }]);
      }
      if (path === "/Photos/Raw") {
        return success([]);
      }
      return success([]);
    }
    if (url.pathname.endsWith("/alist")) {
      return success([
        { id: 1, remark: "测试引擎", url: "https://engine.test", userName: "a" },
      ]);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(null);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 42,
            enable: 1,
            remark: "过滤任务",
            alistId: 1,
            srcPath: "[\"/Photos\"]",
            dstPath: "[\"/Backup\"]",
            method: 0,
            isCron: 2,
            interval: 1440,
            minFileSize: 0,
            maxFileSize: 0,
            exclude: "",
          },
        ],
        count: 1,
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=42");
  await page
    .locator(".overview-card")
    .getByRole("button", { name: "更多操作", exact: true })
    .click();
  await page.locator(".semi-dropdown-menu").getByText("设置", { exact: true }).click();
  const dialog = page.getByRole("dialog");

  await page.getByRole("tab", { name: "文件过滤", exact: true }).click();
  const sizeFilters = dialog.locator(".file-size-filters");
  const fileTypeFilter = dialog.locator(".file-type-filter");
  await expect(sizeFilters).toBeVisible();
  await expect(fileTypeFilter).toBeVisible();
  const sizeBox = await sizeFilters.boundingBox();
  const fileTypeBox = await fileTypeFilter.boundingBox();
  expect(sizeBox!.y).toBeLessThan(fileTypeBox!.y);

  const minRow = dialog.locator(".file-size-filter-row").filter({ hasText: "排除小于" });
  const maxRow = dialog.locator(".file-size-filter-row").filter({ hasText: "排除大于" });
  await expect(
    minRow.getByRole("checkbox", { name: "启用排除小于指定大小的文件" }),
  ).not.toBeChecked();
  await expect(
    maxRow.getByRole("checkbox", { name: "启用排除大于指定大小的文件" }),
  ).not.toBeChecked();
  await expect(
    minRow.getByRole("textbox", { name: "排除小于文件大小" }),
  ).toBeDisabled();
  await expect(
    maxRow.getByRole("textbox", { name: "排除大于文件大小" }),
  ).toBeDisabled();

  await minRow.locator(".semi-checkbox").click();
  await expect(
    minRow.getByRole("textbox", { name: "排除小于文件大小" }),
  ).toBeEnabled();
  await expect(
    minRow.getByRole("textbox", { name: "排除小于文件大小" }),
  ).toHaveValue("10");
  await expect(minRow.locator(".semi-select")).not.toHaveClass(
    /semi-select-disabled/,
  );

  await page.getByRole("tab", { name: "文件夹过滤", exact: true }).click();
  const rootNode = dialog.locator('.exclude-tree [role="treeitem"][data-key="/Photos"]');
  await expect(rootNode).toHaveAttribute("aria-disabled", "true");
  await expect(
    rootNode.getByRole("checkbox", { name: "Toggle the checked state of checkbox" }),
  ).toHaveCount(0);
  const albumsNode = dialog.locator(
    '.exclude-tree [role="treeitem"][data-key="/Photos/Albums"]',
  );
  await expect(albumsNode).toHaveAttribute("aria-expanded", "false");
  await albumsNode.getByRole("button").click();
  await expect(
    dialog.locator('.exclude-tree [role="treeitem"][data-key="/Photos/Albums/2024"]'),
  ).toBeVisible();
  const systemDirFilter = dialog.locator(".system-dir-groups");
  await expect(systemDirFilter).toBeVisible();
  await expect(systemDirFilter).toHaveCSS("border-radius", "8px");
});

test("saves a fractional file-size threshold while the input is focused", async ({
  page,
}) => {
  let saved: Record<string, unknown> | null = null;
  await page.route("**/app/opensync/svr/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session")) {
      return success({ uid: 1, development: false, version: "test" });
    }
    if (
      url.pathname.endsWith("/alist") &&
      url.searchParams.has("alistId")
    ) {
      return success([]);
    }
    if (url.pathname.endsWith("/alist")) {
      return success([
        { id: 1, remark: "测试引擎", url: "https://engine.test", userName: "a" },
      ]);
    }
    if (url.pathname.endsWith("/job") && request.method() === "POST") {
      saved = request.postDataJSON() as Record<string, unknown>;
      return success(null);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(null);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 42,
            enable: 1,
            remark: "过滤任务",
            alistId: 1,
            srcPath: "[\"/Photos\"]",
            dstPath: "[\"/Backup\"]",
            method: 0,
            isCron: 2,
            interval: 1440,
            minFileSize: 0,
            maxFileSize: 0,
            exclude: "",
          },
        ],
        count: 1,
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=42");
  await page
    .locator(".overview-card")
    .getByRole("button", { name: "更多操作", exact: true })
    .click();
  await page.locator(".semi-dropdown-menu").getByText("设置", { exact: true }).click();
  const dialog = page.getByRole("dialog");
  await page.getByRole("tab", { name: "文件过滤", exact: true }).click();

  const minRow = dialog.locator(".file-size-filter-row").filter({ hasText: "排除小于" });
  await minRow.locator(".semi-checkbox").click();
  await minRow.locator(".semi-select").click();
  await page.locator(".semi-select-option").filter({ hasText: "MB" }).click();
  await expect(minRow.locator(".semi-select")).toContainText("MB");
  const input = minRow.getByRole("textbox", { name: "排除小于文件大小" });
  await input.click();
  await input.press("ControlOrMeta+A");
  await input.type("1.2");
  await expect(input).toHaveValue("1.2");
  await dialog.locator("form").evaluate((form: HTMLFormElement) => {
    form.requestSubmit();
  });

  await expect.poll(() => saved).not.toBeNull();
  expect(saved).toMatchObject({
    minFileSize: Math.round(1.2 * 1024 ** 2),
    maxFileSize: 0,
  });
});

test("task editor provides preset and custom file type filters", async ({
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
      return success({ uid: 1, development: false, version: "test" });
    }
    if (url.pathname.endsWith("/alist")) {
      return success([
        { id: 1, remark: "测试引擎", url: "https://engine.test", userName: "a" },
      ]);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(null);
    }
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 42,
            enable: 1,
            remark: "过滤任务",
            alistId: 1,
            srcPath: "[\"/Photos\"]",
            dstPath: "[\"/Backup\"]",
            method: 0,
            isCron: 2,
            interval: 1440,
            minFileSize: 0,
            maxFileSize: 0,
            exclude: "",
          },
        ],
        count: 1,
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/tasks?jobId=42");
  await page
    .locator(".overview-card")
    .getByRole("button", { name: "更多操作", exact: true })
    .click();
  await page.locator(".semi-dropdown-menu").getByText("设置", { exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();

  await expect(
    page.getByRole("tab", { name: "文件夹过滤", exact: true }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "文件夹过滤", exact: true }).click();
  await expect(
    page.getByText("仅针对源目录，勾选要忽略的子文件夹", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("checkbox", { name: "临时文件", exact: true }),
  ).toHaveCount(0);

  await page.getByRole("tab", { name: "文件过滤", exact: true }).click();
  await expect(
    dialog.locator(".field-hint").filter({ hasText: "0 表示不限" }),
  ).toHaveCount(0);
  await expect(
    dialog.locator(".field > label").filter({ hasText: "最小文件大小" }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "高级排除规则", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByText("高级排除规则", { exact: true })).toHaveCount(0);

  await expect(
    page.getByRole("checkbox", { name: "临时文件", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("checkbox", { name: "音乐", exact: true }),
  ).not.toBeChecked();

  await page
    .getByRole("button", { name: "展开临时文件", exact: true })
    .click();
  const firstRowPatternCount = await page
    .locator(".file-type-patterns .semi-checkbox")
    .evaluateAll((items) => {
      const tops = items.map((item) => item.getBoundingClientRect().top);
      const firstTop = Math.min(...tops);
      return tops.filter((top) => Math.abs(top - firstTop) < 2).length;
    });
  if ((page.viewportSize()?.width ?? 0) <= 640) {
    expect(firstRowPatternCount).toBeLessThanOrEqual(2);
  }

  await page
    .getByRole("textbox", { name: "自定义文件扩展名", exact: true })
    .fill("pst");
  await page.getByRole("button", { name: "新增文件类型", exact: true }).click();
  await expect(
    page.getByRole("button", { name: /^其他/ }),
  ).toBeVisible();
  const groupLabels = await dialog.locator(".file-type-group-label").allTextContents();
  expect(groupLabels.at(-1)).toContain("其他");
  await expect(
    page.getByRole("checkbox", { name: "*.pst", exact: true }),
  ).toBeChecked();
});

test("engine form rejects invalid addresses and protects dirty cancellation", async ({
  page,
}) => {
  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "添加引擎", exact: true }).click();
  await page
    .getByRole("textbox", { name: "引擎名称", exact: true })
    .fill("校验测试引擎");
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(
    page.getByText("请输入有效的 HTTP / HTTPS 引擎地址"),
  ).toBeVisible();
  await page
    .getByRole("textbox", { name: "引擎地址", exact: true })
    .fill("ftp://invalid");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByText("放弃未保存的修改？")).toBeVisible();
  await page.getByRole("button", { name: "放弃", exact: true }).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
});
test("notification channels switch without retaining unrelated fields", async ({
  page,
}) => {
  await page.goto("/app/opensync/notifications");
  await page.getByRole("button", { name: "添加通知", exact: true }).click();
  await expect(
    page.getByRole("switch", { name: "启用通知", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(
    page.getByRole("alert").filter({ hasText: "请选择通知渠道" }),
  ).toBeVisible();
  await page.getByRole("combobox", { name: "通知渠道", exact: true }).click();
  await page.getByRole("option").filter({ hasText: "自定义 Webhook" }).click();
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(
    page.getByText("请输入有效的 Webhook URL", { exact: true }),
  ).toBeVisible();
  await page.getByRole("combobox", { name: "通知渠道", exact: true }).click();
  await page.getByText("企业微信", { exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "企业 ID", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("textbox", { name: "Webhook URL", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(
    page.getByText("请填写企业 ID、Secret 和 AgentId", { exact: true }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});
test("settings uses plain inputs, validates limits and keeps backend units", async ({
  page,
}) => {
  await page.goto("/app/opensync/settings");
  const timeout = page.getByRole("textbox", {
    name: "任务超时",
    exact: true,
  });
  await expect(timeout).toBeVisible();
  await expect(
    page.locator(".settings-card-body .setting-row .field-tip-icon"),
  ).toHaveCount(5);
  await page.getByLabel("任务超时说明", { exact: true }).hover();
  await expect(
    page.getByText("单个任务运行超过该时长后自动标记为超时", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("并发数越大占用的系统资源越多，请按硬件配置调整。", {
      exact: true,
    }),
  ).toHaveCount(0);
  await expect(
    page.getByText("超时或保留时间为 0 时，不设置相应限制。", {
      exact: true,
    }),
  ).toHaveCount(0);
  const original = await timeout.inputValue();
  await timeout.fill(String(Number(original) + 1));
  await timeout.blur();
  const save = page.getByRole("button", { name: "保存", exact: true });
  await expect(save).toBeEnabled();
  await expect(page.getByRole("spinbutton")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "还原", exact: true }),
  ).toHaveCount(0);
  await timeout.fill("abc");
  await save.click();
  await expect(
    page.getByText("任务超时请输入 0–8760 的整数", { exact: true }),
  ).toBeVisible();
  await timeout.fill("8761");
  await save.click();
  await expect(
    page.getByText("任务超时请输入 0–8760 的整数", { exact: true }).first(),
  ).toBeVisible();
  await page.reload();
  await expect(timeout).toHaveValue(original);
  await expect(page.getByText("小时", { exact: true })).toBeVisible();
  await expect(page.getByText("天", { exact: true })).toBeVisible();
});
