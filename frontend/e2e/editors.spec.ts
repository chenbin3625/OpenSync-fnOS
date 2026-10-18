import { test, expect } from "@playwright/test";

test("task editor keeps size filters disabled until selected", async ({
  page,
}) => {
  let saveAttempts = 0;
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
    if (url.pathname.endsWith("/job") && route.request().method() === "POST") {
      saveAttempts++;
      return success(null);
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
  const minInput = minRow.getByRole("textbox", { name: "排除小于文件大小" });
  await minInput.fill("not-a-size");
  await minRow.getByText("排除小于").click();
  await expect(minInput).toHaveValue("not-a-size");
  await dialog.getByRole("button", { name: "保存" }).click();
  await expect(page.getByText("文件大小必须是有效的非负数")).toBeVisible();
  expect(saveAttempts).toBe(0);

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
    .getByRole("textbox", { name: "自定义文件规则", exact: true })
    .fill("*.pst");
  await page.getByRole("button", { name: "新增文件规则", exact: true }).click();
  await expect(
    page.getByRole("button", { name: /^其他/ }),
  ).toBeVisible();
  const groupLabels = await dialog.locator(".file-type-group-label").allTextContents();
  expect(groupLabels.at(-1)).toContain("其他");
  await page.getByRole("button", { name: "展开其他", exact: true }).click();
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

test("directory picker scrolls when there are many folders", async ({ page }) => {
  const folders = Array.from({ length: 120 }, (_, i) => ({
    name: `dir-${String(i).padStart(3, "0")}`,
  }));
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
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) {
      return success(url.searchParams.get("path") === "/" ? folders : []);
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
            remark: "目录任务",
            alistId: 1,
            srcPath: "[\"/dir-000\"]",
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
  await dialog.getByRole("combobox", { name: "选择引擎目录" }).first().click();

  const list = page.locator(".remote-paths-dropdown .semi-tree-option-list");
  const last = list.locator('[role="treeitem"][data-key="/dir-119"]');
  await expect(last).toBeAttached();
  // 列表必须是可滚动区域，且整体落在视口内，否则底部目录无法到达。
  const metrics = await list.evaluate((el) => ({
    scrollable: el.scrollHeight > el.clientHeight,
    bottom: el.getBoundingClientRect().bottom,
    viewport: window.innerHeight,
  }));
  expect(metrics.scrollable).toBe(true);
  expect(metrics.bottom).toBeLessThanOrEqual(metrics.viewport);

  await last.scrollIntoViewIfNeeded();
  await last.locator(".semi-tree-option-label").click();
  await expect(dialog.locator(".remote-paths").first()).toContainText("/dir-119");
});

async function openPickerForEditedTask(
  page: import("@playwright/test").Page,
  options: {
    srcPath: string;
    listing: (path: string, call: number) => string[];
    delay?: (path: string) => number;
  },
) {
  const calls = new Map<string, number>();
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
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) {
      const path = url.searchParams.get("path") || "/";
      const call = (calls.get(path) || 0) + 1;
      calls.set(path, call);
      const wait = options.delay?.(path) || 0;
      if (wait) await new Promise((resolve) => setTimeout(resolve, wait));
      return success(options.listing(path, call).map((name) => ({ path: name })));
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
            remark: "目录任务",
            alistId: 1,
            srcPath: JSON.stringify([options.srcPath]),
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
  const source = dialog.locator(".remote-paths").first();
  await source.getByRole("combobox", { name: "选择引擎目录" }).click();
  const popover = page.locator(".remote-paths-dropdown");
  const option = (key: string) =>
    popover.locator(`[role="treeitem"][data-key="${key}"]`);
  // 只展开未展开的节点：根目录加载后会自动展开，再点一次会收起
  const expand = async (key: string) => {
    await expect(option(key)).toBeVisible();
    if ((await option(key).getAttribute("aria-expanded")) === "true") return;
    await option(key).locator(".semi-tree-option-expand-icon").click();
    await expect(option(key)).toHaveAttribute("aria-expanded", "true");
  };
  return { calls, dialog, source, popover, option, expand };
}

test("editing a task keeps sibling folders of the selected path loadable", async ({
  page,
}) => {
  const listing: Record<string, string[]> = {
    "/": ["Photos", "Backup"],
    "/Photos": ["Albums", "Raw"],
    "/Photos/Albums": ["2024", "2025"],
  };
  // 根目录请求慢于用户展开预置路径：这是原来子树被冲掉的触发条件
  const { option, expand } = await openPickerForEditedTask(page, {
    srcPath: "/Photos/Albums",
    listing: (path) => listing[path] || [],
    delay: (path) => (path === "/" ? 1500 : 0),
  });
  await expand("/");
  await expand("/Photos");
  await expect(option("/Photos/Raw")).toBeVisible();
  await expand("/Photos/Albums");
  await expect(option("/Photos/Albums/2025")).toBeVisible();
  // 根目录返回后，已加载的下级目录必须仍在
  await expect(option("/Backup")).toBeVisible();
  await expect(option("/Photos/Raw")).toBeVisible();
  await expect(option("/Photos/Albums/2025")).toBeVisible();
  await option("/Photos/Raw").locator(".semi-tree-option-label").click();
  await expect(page.getByRole("dialog").locator(".remote-paths").first()).toContainText("Raw");
});

test("refresh button reloads expanded folders from the engine", async ({ page }) => {
  const { calls, popover, option, expand } = await openPickerForEditedTask(page, {
    srcPath: "/Photos",
    listing: (path, call) => {
      if (path === "/") return ["Photos"];
      if (path === "/Photos") return call === 1 ? ["Albums"] : ["Albums", "New"];
      return [];
    },
  });
  await expect(option("/Photos")).toBeVisible();
  await expand("/Photos");
  await expect(option("/Photos/Albums")).toBeVisible();
  await expect(option("/Photos/New")).toHaveCount(0);

  const before = { root: calls.get("/") || 0, photos: calls.get("/Photos") || 0 };
  await popover.getByRole("button", { name: "刷新目录" }).click();
  await expect(option("/Photos/New")).toBeVisible();
  expect(calls.get("/")).toBe(before.root + 1);
  expect(calls.get("/Photos")).toBe(before.photos + 1);
  // 刷新后下拉仍保持打开，可继续选择
  await option("/Photos/New").locator(".semi-tree-option-label").click();
  await expect(page.getByRole("dialog").locator(".remote-paths").first()).toContainText("New");
});

test("deep paths keep folder names readable", async ({ page }) => {
  const names = [
    "Photos",
    "Albums-from-family-devices",
    "2024-summer-vacation-collection",
    "very-long-directory-name-level-four",
    "final-selected-folder-name",
  ];
  const paths = names.map((_, i) => "/" + names.slice(0, i + 1).join("/"));
  const deepest = paths[paths.length - 1];
  const { source, option, expand } = await openPickerForEditedTask(page, {
    srcPath: deepest,
    listing: (path) => {
      const index = path === "/" ? 0 : paths.indexOf(path) + 1;
      return names[index] ? [names[index]] : [];
    },
  });
  // 选择框里最后一级目录名完整可见
  const selectedName = source.locator(".remote-paths-selected-name");
  await expect(selectedName).toHaveText("final-selected-folder-name");
  await expect(source.locator(".remote-paths-selected")).toHaveAttribute("title", deepest);
  const nameMetrics = await selectedName.evaluate((el) => ({
    scroll: el.scrollWidth,
    client: el.clientWidth,
  }));
  expect(nameMetrics.scroll).toBeLessThanOrEqual(nameMetrics.client);

  await expand("/");
  for (const path of paths.slice(0, -1)) await expand(path);
  const label = option(deepest).locator(".remote-paths-node");
  await expect(label).toHaveText("final-selected-folder-name");
  // 深层节点名称单行显示，不被挤成多行
  const box = await label.boundingBox();
  expect(box!.height).toBeLessThanOrEqual(24);
});
