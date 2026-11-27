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
        return success([{ name: "2024" }, { name: "2025" }]);
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
    .getByRole("button", { name: "编辑任务", exact: true })
    .click();
  const dialog = page.getByRole("dialog");

  await page.getByRole("tab", { name: "过滤规则", exact: true }).click();
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
  // 清空并失焦应保持勾选态，而不是静默取消勾选并重置为 10
  await minInput.fill("");
  await minRow.getByText("排除小于", { exact: true }).click();
  await expect(
    minRow.getByRole("checkbox", { name: "启用排除小于指定大小的文件" }),
  ).toBeChecked();
  await expect(minInput).toHaveValue("");

  await minInput.fill("not-a-size");
  await minRow.getByText("排除小于", { exact: true }).click();
  await expect(minInput).toHaveValue("not-a-size");
  await dialog.getByRole("button", { name: "保存" }).click();
  await expect(
    dialog.locator(".field-error").filter({ hasText: "文件大小必须是有效的非负数" }),
  ).toBeVisible();
  expect(saveAttempts).toBe(0);

  const rootNode = dialog.locator('.exclude-tree [role="treeitem"][data-key="/Photos"]');
  await rootNode.scrollIntoViewIfNeeded();
  await expect(rootNode).toHaveAttribute("aria-disabled", "true");
  await expect(
    rootNode.getByRole("checkbox", { name: "Toggle the checked state of checkbox" }),
  ).toHaveCount(0);
  const albumsNode = dialog.locator(
    '.exclude-tree [role="treeitem"][data-key="/Photos/Albums"]',
  );
  await expect(albumsNode).toHaveAttribute("aria-expanded", "false");
  await albumsNode.getByRole("button").click();
  const childNode = dialog.locator(
    '.exclude-tree [role="treeitem"][data-key="/Photos/Albums/2024"]',
  );
  await expect(childNode).toBeVisible();
  await childNode.locator(".semi-checkbox").click();
  await expect(dialog.locator(".exclude-selected-bar")).toContainText("Albums/2024/");
  const systemDirFilter = dialog.locator(".system-dir-groups");
  await systemDirFilter.scrollIntoViewIfNeeded();
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
    .getByRole("button", { name: "编辑任务", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await page.getByRole("tab", { name: "过滤规则", exact: true }).click();

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
    .getByRole("button", { name: "编辑任务", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();

  await expect(
    page.getByRole("tab", { name: "过滤规则", exact: true }),
  ).toBeVisible();
  await page.getByRole("tab", { name: "过滤规则", exact: true }).click();
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

  // 非法自定义规则给出具体的内联错误说明
  const customInput = page.getByRole("textbox", {
    name: "自定义文件规则",
    exact: true,
  });
  await customInput.fill("dir/a.txt");
  await page.getByRole("button", { name: "新增文件规则", exact: true }).click();
  await expect(dialog.locator(".file-type-add-block .field-error")).toContainText(
    "路径分隔符",
  );

  // 新增合法自定义规则后以可关闭 Tag 展示，不再提供误导性的 Checkbox
  await customInput.fill("*.pst");
  await page.getByRole("button", { name: "新增文件规则", exact: true }).click();
  await expect(
    page.getByRole("button", { name: /^自定义规则/ }),
  ).toBeVisible();
  const groupLabels = await dialog.locator(".file-type-group-label").allTextContents();
  expect(groupLabels.at(-1)).toContain("自定义规则");
  const customTag = dialog.locator(".custom-file-tag").filter({ hasText: "*.pst" });
  await expect(customTag).toBeVisible();
  await customTag.locator(".semi-tag-close").click();
  await expect(customTag).toHaveCount(0);
});

test("storage form rejects invalid names and protects dirty cancellation", async ({
  page,
}) => {
  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "添加远程存储", exact: true }).click();
  const name = page.getByRole("textbox", { name: "存储名称", exact: true });
  await name.fill("invalid/name");
  await page.getByRole("button", { name: "保存存储", exact: true }).click();
  await expect(
    page.getByText(/^存储名称需以字母/).first(),
  ).toBeVisible();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByText("放弃未保存的修改？")).toBeVisible();
  await page.getByRole("button", { name: "放弃", exact: true }).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
});

test("storage edit saves a new name with or without connection changes", async ({ page }) => {
  let remoteName = "Archive";
  let options = { type: "webdav", url: "https://dav.example.com/archive", vendor: "other" };
  await page.route("**/app/opensync/svr/**", async (route) => {
    const request = route.request();
    const { pathname } = new URL(request.url());
    const success = (data: unknown) => route.fulfill({
      json: { code: 200, data, msg: "" },
    });
    if (pathname.endsWith("/session")) return success({ uid: 1, version: "test" });
    if (pathname.endsWith("/alist")) return success([{ id: 1, kind: "rclone" }]);
    if (pathname.endsWith("/native/storage/test")) {
      expect(request.postDataJSON()).toMatchObject({ name: "Archive", editing: true });
      return success({ ok: true, latencyMs: 1, message: "连接正常" });
    }
    if (pathname.endsWith("/native/storage/rename")) {
      expect(request.postDataJSON()).toEqual({ oldName: "Archive", newName: "ArchiveV2" });
      remoteName = "ArchiveV2";
      return success({ oldName: "Archive", newName: remoteName, updatedJobs: 1 });
    }
    if (pathname.endsWith("/native/storage")) {
      if (request.method() === "PUT") {
        const edit = request.postDataJSON();
        expect(edit).toMatchObject({
          name: "ArchiveV2", newName: "ArchiveV3",
          options: { url: "https://dav.example.com/new-archive" },
        });
        remoteName = edit.newName;
        options = edit.options;
        return success(null);
      }
      return success({
        engineId: 1, enabled: true, rcloneAvailable: true, localRoots: [],
        remotes: [{
          name: remoteName, type: "webdav", options,
          configuredSecrets: ["pass"], usedByCrypts: [], usedByJobs: ["备份"],
        }],
      });
    }
    return success(null);
  });

  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "编辑 Archive", exact: true }).click();
  await expect(page.getByRole("button", { name: "重命名 Archive", exact: true })).toHaveCount(0);
  const dialog = page.getByRole("dialog");
  const name = dialog.getByRole("textbox", { name: "存储名称", exact: true });
  await expect(name).toHaveValue("Archive");
  await name.fill("ArchiveV2");
  await dialog.getByRole("button", { name: "测试连接", exact: true }).click();
  await expect(dialog.getByText("连接正常", { exact: false })).toBeVisible();
  await dialog.getByRole("button", { name: "保存修改", exact: true }).click();
  await expect(dialog).toBeHidden();

  await page.getByRole("button", { name: "编辑 ArchiveV2", exact: true }).click();
  await name.fill("ArchiveV3");
  await dialog.getByRole("textbox", { name: "WebDAV 地址", exact: true }).fill("https://dav.example.com/new-archive");
  await dialog.getByRole("button", { name: "保存修改", exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("button", { name: "编辑 ArchiveV3", exact: true })).toBeVisible();
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
  const channelSelect = page.getByRole("combobox", {
    name: "通知渠道",
    exact: true,
  });
  await channelSelect.click();
  const webhookOption = page
    .getByRole("option")
    .filter({ hasText: "自定义 Webhook" });
  await webhookOption.click();
  // On the mobile project the select popover can stay mounted one tick longer
  // than the click; saving immediately afterwards hit the overlay instead of
  // the button and no validation toast was produced.
  await expect(webhookOption).toBeHidden();
  await expect(channelSelect).toContainText("自定义 Webhook");
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(
    page.getByText("请输入有效的 Webhook URL", { exact: true }),
  ).toBeVisible();
  await channelSelect.click();
  const wecomOption = page.getByRole("option").filter({ hasText: /企业微信$/ });
  await wecomOption.click();
  await expect(wecomOption).toBeHidden();
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
  await expect(page.getByLabel("任务超时说明", { exact: true })).toBeVisible();
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
  let saveRequests = 0;
  await page.route("**/app/opensync/svr/system/config", async (route) => {
    if (route.request().method() === "PUT") saveRequests += 1;
    await route.continue();
  });
  await timeout.fill("abc");
  await save.click();
  const warningToast = page
    .locator(".semi-toast-content")
    .filter({ hasText: "任务超时请输入 0–8760 的整数" });
  await expect(warningToast).toHaveCount(1);
  await expect(warningToast).toBeVisible();
  const closeToast = page.locator(".semi-toast-close-button").first();
  if (await closeToast.isVisible()) {
    await closeToast.click();
  }
  await expect(warningToast).toHaveCount(0, { timeout: 6000 });
  await timeout.fill("8761");
  await save.click();
  await expect(warningToast).toHaveCount(1);
  await expect(warningToast).toBeVisible();
  expect(saveRequests).toBe(0);
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
    .getByRole("button", { name: "编辑任务", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox", { name: "选择源目录" }).first().click();

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
    .getByRole("button", { name: "编辑任务", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  const source = dialog.locator(".remote-paths").first();
  await source.getByRole("combobox", { name: "选择源目录" }).click();
  const popover = page.locator(".remote-paths-dropdown");
  const option = (key: string) =>
    popover.locator(`[role="treeitem"][data-key="${key}"]`);
  // 只展开未展开的节点：根目录加载后会自动展开，再点一次会收起
  const expand = async (key: string) => {
    await expect(option(key)).toBeVisible();
    if (key === "/") {
      await expect(option(key)).toHaveAttribute("aria-expanded", "true");
      return;
    }
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

test("task editor previews destination folders live across path rules and edit dstMap", async ({
  page,
}) => {
  const listing: Record<string, string[]> = {
    "/": ["drive", "media", "archive", "backup", "backup-new"],
    "/drive": ["2", "学习"],
    "/media": ["photos"],
    "/archive": ["photos", "videos"],
    "/backup": [],
    "/backup-new": [],
  };
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
      return success((listing[path] || []).map((name) => ({ name })));
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
            id: 51,
            enable: 1,
            remark: "已保存映射任务",
            alistId: 1,
            srcPath: '["/drive/2","/drive/学习"]',
            dstPath: '["/backup"]',
            dstMap: JSON.stringify({
              version: 1,
              base: ["/backup/"],
              map: {
                "/drive/2/\u0000/backup/": "/backup/2/",
                "/drive/学习/\u0000/backup/": "/backup/学习/",
              },
            }),
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

  await page.goto("/app/opensync/sync");
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  const preview = dialog.getByRole("region", { name: "同步位置预览" });
  await expect(preview).toContainText("选择源路径和目标路径后");

  const trees = dialog.locator(".remote-paths");
  const popover = page.locator(".remote-paths-dropdown");
  const togglePicker = async (
    picker: import("@playwright/test").Locator,
    shouldOpen: boolean,
  ) => {
    // Click the left edge of the combobox so we never hit a selected tag's
    // close icon or the right-hand clear button, and never get blocked by an
    // upward-opening dropdown covering the modal header.
    await picker.locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
    if (shouldOpen) {
      await expect(popover).toBeVisible();
    } else {
      await expect(popover).toBeHidden();
    }
  };
  const option = (key: string) =>
    popover.locator(`[role="treeitem"][data-key="${key}"]`);
  const expand = async (key: string) => {
    await expect(option(key)).toBeVisible();
    if ((await option(key).getAttribute("aria-expanded")) === "true") return;
    await option(key).locator(".semi-tree-option-expand-icon").click();
    await expect(option(key)).toHaveAttribute("aria-expanded", "true");
  };

  // Select single source /drive/2 and destination /backup-new -> direct root sync
  await togglePicker(trees.nth(0), true);
  await expand("/");
  await expand("/drive");
  await option("/drive/2").locator(".semi-tree-option-label").click();
  await togglePicker(trees.nth(0), false);
  await expect(preview).toContainText("请选择目标路径");

  await togglePicker(trees.nth(1), true);
  await expand("/");
  await option("/backup-new").locator(".semi-tree-option-label").click();
  await togglePicker(trees.nth(1), false);

  await expect(preview).toContainText("单个源目录");
  await expect(preview).toContainText("/drive/2/");
  await expect(preview).toContainText("/backup-new/");
  await expect(preview).toContainText("例：/drive/2/a.jpg → /backup-new/a.jpg");

  // Add sibling /drive/学习 -> shared parent /drive stripped
  await togglePicker(trees.nth(0), true);
  await expand("/drive");
  await option("/drive/学习").locator(".semi-tree-option-label").click();
  await togglePicker(trees.nth(0), false);

  await expect(preview).toContainText("共同父目录 /drive");
  await expect(preview).toContainText("/backup-new/2/");
  await expect(preview).toContainText("/backup-new/学习/");

  // Add /media/photos and /archive/photos (colliding base names, no shared parent)
  await togglePicker(trees.nth(0), true);
  await expand("/media");
  await option("/media/photos").locator(".semi-tree-option-label").click();
  await expand("/archive");
  await option("/archive/photos").locator(".semi-tree-option-label").click();
  await togglePicker(trees.nth(0), false);

  await expect(preview).toContainText("含同名目录");
  await expect(preview).toContainText("/backup-new/media/photos/");
  await expect(preview).toContainText("/backup-new/archive/photos/");

  // Selecting /backup-new as source while /backup-new is destination surfaces inline conflict
  await togglePicker(trees.nth(0), true);
  await option("/backup-new").locator(".semi-tree-option-label").click();
  await togglePicker(trees.nth(0), false);
  await expect(preview.getByRole("alert")).toContainText("源目录与目标目录不能相同或互相包含");

  expect(
    await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
  ).toBe(true);

  // Close new-task modal and edit existing job with stored dstMap
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await page.getByRole("button", { name: "放弃", exact: true }).click();
  await expect(dialog).not.toBeVisible();

  await page.getByRole("main").getByRole("link", { name: "已保存映射任务", exact: true }).click();
  await page
    .locator(".overview-card")
    .getByRole("button", { name: "编辑任务", exact: true })
    .click();
  const editDialog = page.getByRole("dialog");
  const editPreview = editDialog.getByRole("region", { name: "同步位置预览" });
  await expect(editPreview).toContainText("/backup/2/");
  await expect(editPreview).toContainText("/backup/学习/");

  // Removing /drive/学习 on edit preserves /backup/2/ from stored dstMap
  const editSourceTree = editDialog.locator(".remote-paths").first();
  await togglePicker(editSourceTree, true);
  await expand("/");
  await expand("/drive");
  await option("/drive/学习").locator(".semi-tree-option-label").click();
  await togglePicker(editSourceTree, false);

  await expect(editPreview).toContainText("/backup/2/");
  await expect(editPreview).toContainText("沿用原位置");
  expect(
    await editDialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
  ).toBe(true);
});

test("guides no-engine onboarding, detects cross-job conflicts, and deduplicates dir listing", async ({
  page,
}) => {
  let engines: Array<Record<string, unknown>> = [];
  let jobs: Array<Record<string, unknown>> = [];
  let rootCalls = 0;

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
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) {
      const path = url.searchParams.get("path") || "/";
      if (path === "/") {
        rootCalls++;
        return success([
          { name: "Photos" },
          { name: "Backup" },
          { name: "Backup2" },
        ]);
      }
      return success([]);
    }
    if (url.pathname.endsWith("/alist")) {
      return success(engines);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(null);
    }
    if (url.pathname.endsWith("/job") && request.method() === "POST") {
      return success({ id: 99 });
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: jobs, count: jobs.length });
    }
    return success(null);
  });

  // 1. 无引擎时的冷启动引导
  await page.goto("/app/opensync/sync");
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  const emptyDialog = page.getByRole("dialog");
  await expect(emptyDialog.locator(".engine-empty-guide")).toContainText("内置存储尚未就绪");
  await emptyDialog
    .getByRole("button", { name: "前往存储管理", exact: true })
    .click();
  await expect(page).toHaveURL(/\/app\/opensync\/engines/);

  // 2. 有引擎和已有任务时：检测跨任务冲突、目录请求去重、日程预设与提前保存
  engines = [
    { id: 1, remark: "内置存储", url: "native://rclone", userName: "" },
  ];
  jobs = [
    {
      id: 50,
      enable: 1,
      remark: "已有相册备份",
      alistId: 1,
      srcPath: '["/Photos"]',
      dstPath: '["/Backup"]',
      method: 0,
      isCron: 3,
      interval: 0,
      useCacheS: 0,
      useCacheT: 0,
      exclude: "",
    },
  ];

  await page.goto("/app/opensync/tasks?jobId=50");
  // 总览页显示所属引擎与过滤概览
  await expect(page.locator(".overview-card")).toContainText("已有相册备份");
  await page.getByRole("button", { name: "返回任务列表", exact: true }).click();
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "任务名称", exact: true }).fill("新任务");

  const trees = dialog.locator(".remote-paths");
  const popover = page.locator(".remote-paths-dropdown");
  await trees.nth(0).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover.locator('[role="treeitem"][data-key="/Photos"]')).toBeVisible();
  // 同引擎源/目标共享目录缓存：打开弹窗只请求一次根目录
  expect(rootCalls).toBe(1);
  await popover
    .locator('[role="treeitem"][data-key="/Photos"] .semi-tree-option-label')
    .click();
  await trees.nth(0).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });

  await trees.nth(1).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await popover
    .locator('[role="treeitem"][data-key="/Backup"] .semi-tree-option-label')
    .click();
  await trees.nth(1).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });

  // 跨任务冲突在 Step 1 实时提示，并阻止进入下一步
  const preview = dialog.getByRole("region", { name: "同步位置预览" });
  await expect(preview.getByRole("alert")).toContainText("已有相册备份");

  // 改为不冲突的目标路径 /Backup2 后进入第 2 步
  await trees.nth(1).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await popover
    .locator('[role="treeitem"][data-key="/Backup"] .semi-tree-option-label')
    .click();
  await popover
    .locator('[role="treeitem"][data-key="/Backup2"] .semi-tree-option-label')
    .click();
  await trees.nth(1).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(preview.getByRole("alert")).toHaveCount(0);

  await dialog.getByRole("button", { name: "下一步", exact: true }).click();
  await expect(dialog.locator(".schedule-preview-card")).toContainText("远端每");

  // 切换远端轮询与完整核对快捷预设
  await dialog.getByRole("button", { name: "每 5 分钟", exact: true }).click();
  await expect(dialog.locator(".schedule-preview-plan")).toContainText("每 5 分钟");

  await dialog.getByRole("button", { name: "6 小时", exact: true }).click();
  await expect(dialog.locator(".schedule-preview-plan")).toContainText("每 6 小时完整核对");

  // 第 2 步直接可见「保存」与可选的「过滤规则」按钮，且窄屏下不裁切
  const footer = dialog.locator(".editor-actions");
  await expect(footer.getByRole("button", { name: "过滤规则", exact: true })).toBeVisible();
  await expect(footer.getByRole("button", { name: "保存", exact: true })).toBeVisible();
  const dialogBox = await dialog.boundingBox();
  for (const label of ["上一步", "取消", "过滤规则", "保存"]) {
    const btnBox = await footer
      .getByRole("button", { name: label, exact: true })
      .boundingBox();
    expect(btnBox!.x).toBeGreaterThanOrEqual(dialogBox!.x - 1);
    expect(btnBox!.x + btnBox!.width).toBeLessThanOrEqual(
      dialogBox!.x + dialogBox!.width + 1,
    );
  }
});

test("task editor quick-enables end-to-end crypt storage for remote destination", async ({
  page,
}) => {
  const remotes: Array<Record<string, unknown>> = [
    {
      name: "aliyun",
      type: "webdav",
      summary: "WebDAV · https://dav.example.com",
      options: { url: "https://dav.example.com", vendor: "nextcloud" },
      configuredSecrets: ["pass"],
      usedByJobs: [],
      usedByCrypts: [],
    },
  ];
  const createdDirs = new Map<string, string[]>();
  let createdStoragePayload: Record<string, unknown> | null = null;

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
    if (url.pathname.endsWith("/native/storage/mkdir") && request.method() === "POST") {
      const body = request.postDataJSON() as { path: string };
      const segs = body.path.replace(/^\/+|\/+$/g, "").split("/").filter(Boolean);
      let acc = "";
      for (let i = 0; i < segs.length; i += 1) {
        const parent = acc || "/";
        acc = `${acc}/${segs[i]}`;
        if (i >= 1) {
          const list = createdDirs.get(parent) || [];
          if (!list.includes(segs[i])) list.push(segs[i]);
          createdDirs.set(parent, list);
        }
      }
      return success(null);
    }
    if (url.pathname.endsWith("/native/storage") && request.method() === "GET") {
      return success({
        engineId: 1,
        enabled: true,
        remark: "内置存储",
        maxConcurrentJobs: 2,
        rcloneAvailable: true,
        localRoots: [{ id: "photos", path: "/vol1/Photos" }],
        remotes,
      });
    }
    if (url.pathname.endsWith("/native/storage") && request.method() === "POST") {
      const body = request.postDataJSON() as Record<string, unknown>;
      createdStoragePayload = body;
      const name = String(body.name || "");
      const options = (body.options || {}) as Record<string, string>;
      remotes.push({
        name,
        type: options.type || "crypt",
        summary: `底层 ${options.remote || ""}`,
        options: { remote: options.remote || "" },
        configuredSecrets: ["password"],
        usedByJobs: [],
        usedByCrypts: [],
      });
      return success(null);
    }
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) {
      const p = url.searchParams.get("path") || "/";
      if (p === "/") {
        return success([
          { name: "本地" },
          ...remotes.map((r) => ({ name: String(r.name) })),
        ]);
      }
      const base =
        p === "/本地"
          ? ["Photos"]
          : p === "/aliyun"
            ? ["backup"]
            : [];
      const extra = createdDirs.get(p) || [];
      return success(
        Array.from(new Set([...base, ...extra])).map((name) => ({ name })),
      );
    }
    if (url.pathname.endsWith("/alist")) {
      return success([
        { id: 1, remark: "内置存储", url: "native://rclone", userName: "" },
      ]);
    }
    if (url.pathname.endsWith("/notify")) {
      return success([]);
    }
    if (url.pathname.endsWith("/system/config")) {
      return success({});
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(null);
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: [], count: 0 });
    }
    return success(null);
  });

  await page.goto("/app/opensync/sync");
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();

  const editorDialog = page
    .getByRole("dialog")
    .filter({ hasText: "新建持续同步任务" })
    .first();
  await expect(editorDialog).toBeVisible();
  await expect(editorDialog.getByText("手动输入路径")).toHaveCount(0);
  await expect(editorDialog.getByPlaceholder("/存储名称/存储桶/子目录")).toHaveCount(0);

  const dstPicker = editorDialog.locator(".remote-paths").nth(1);
  const popover = page.locator(".remote-paths-dropdown");
  await dstPicker.locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover).toBeVisible();
  const aliyunNode = popover.locator('[role="treeitem"][data-key="/aliyun"]');
  await expect(aliyunNode).toBeVisible();
  await aliyunNode.locator(".semi-tree-option-expand-icon").click();
  const backupNode = popover.locator('[role="treeitem"][data-key="/aliyun/backup"]');
  await expect(backupNode).toBeVisible();
  await backupNode.locator(".semi-tree-option-label").click();
  await dstPicker.locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover).toBeHidden();

  const cryptBanner = editorDialog.locator(".quick-crypt-banner");
  await expect(cryptBanner).toBeVisible();
  await expect(cryptBanner).toContainText("/aliyun/backup");
  await cryptBanner.getByRole("button", { name: "启用端到端加密" }).click();

  const cryptModal = page.locator('.quick-crypt-modal [role="dialog"]');
  await expect(cryptModal).toBeVisible();
  await expect(cryptModal.getByLabel("加密存储名称")).toHaveValue("aliyun-backup-crypt");

  await cryptModal.getByLabel("加密子目录").fill("encrypted");
  await expect(cryptModal.locator(".quick-crypt-mapping-preview")).toContainText(
    "aliyun:backup/encrypted",
  );
  await cryptModal.getByRole("textbox", { name: "加密密码", exact: true }).fill("StrongPass#2026");
  await cryptModal.getByRole("textbox", { name: "确认加密密码", exact: true }).fill("StrongPass#2026");
  await cryptModal.getByRole("button", { name: "创建并启用加密" }).click();

  await expect(cryptModal).toBeHidden();
  expect(createdStoragePayload).toEqual({
    name: "aliyun-backup-crypt",
    options: {
      type: "crypt",
      remote: "aliyun:backup/encrypted",
      password: "StrongPass#2026",
    },
  });

  await expect(editorDialog.locator(".quick-crypt-banner.is-success")).toContainText(
    "已启用端到端加密",
  );
  await expect(editorDialog.locator(".quick-crypt-banner.is-success")).toContainText(
    "aliyun-backup-crypt",
  );

  // Create a nested subfolder under /aliyun/backup directly inside the
  // directory picker, then verify the modal offers one-click reuse of the
  // existing crypt storage.
  await dstPicker.locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover).toBeVisible();
  await expect(aliyunNode).toBeVisible();
  if ((await aliyunNode.getAttribute("aria-expanded")) !== "true") {
    await aliyunNode.locator(".semi-tree-option-expand-icon").click();
  }
  await expect(backupNode).toBeVisible();
  await backupNode.getByRole("button", { name: "在 /aliyun/backup 下新建文件夹" }).click();
  await popover.getByRole("textbox", { name: "新文件夹名称" }).fill("encrypted/photos");
  await popover.getByRole("button", { name: "创建", exact: true }).click();
  await expect(
    popover.locator('[role="treeitem"][data-key="/aliyun/backup/encrypted/photos"]'),
  ).toBeVisible();
  await dstPicker.locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover).toBeHidden();
  await editorDialog
    .locator(".quick-crypt-banner")
    .getByRole("button", { name: "启用端到端加密" })
    .click();
  await expect(cryptModal).toBeVisible();
  await expect(cryptModal.getByRole("region", { name: "复用已有加密存储" })).toContainText(
    "aliyun-backup-crypt",
  );
  await cryptModal
    .getByRole("region", { name: "复用已有加密存储" })
    .getByRole("button", { name: "直接切换" })
    .click();
  await expect(cryptModal).toBeHidden();
  await expect(editorDialog.locator(".quick-crypt-banner.is-success")).toContainText(
    "aliyun-backup-crypt",
  );
});

test("directory picker creates folders inline without manual path input", async ({
  page,
}) => {
  const mkdirCalls: string[] = [];
  const dirChildren = new Map<string, string[]>([
    ["/", ["本地", "PhotoCloud", "ReadOnlyMirror"]],
    ["/本地", ["Photos~a1b2c3d4e5f6"]],
    ["/本地/Photos~a1b2c3d4e5f6", ["2025"]],
    ["/PhotoCloud", ["Existing"]],
    ["/PhotoCloud/Existing", []],
    ["/ReadOnlyMirror", ["public"]],
  ]);

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
    if (url.pathname.endsWith("/native/storage") && request.method() === "GET") {
      return success({
        engineId: 1,
        enabled: true,
        remark: "内置存储",
        maxConcurrentJobs: 2,
        rcloneAvailable: true,
        localRoots: [{ id: "Photos~a1b2c3d4e5f6", path: "/vol1/Photos" }],
        remotes: [
          {
            name: "PhotoCloud",
            type: "webdav",
            summary: "WebDAV",
            options: { vendor: "nextcloud" },
            configuredSecrets: ["pass"],
            usedByJobs: [],
            usedByCrypts: [],
          },
          {
            name: "ReadOnlyMirror",
            type: "http",
            summary: "HTTP 只读",
            options: { url: "https://mirror.example.com" },
            configuredSecrets: [],
            usedByJobs: [],
            usedByCrypts: [],
          },
        ],
      });
    }
    if (url.pathname.endsWith("/native/storage/mkdir") && request.method() === "POST") {
      const body = request.postDataJSON() as { path: string };
      mkdirCalls.push(body.path);
      const segs = body.path.replace(/^\/+|\/+$/g, "").split("/").filter(Boolean);
      let cur = `/${segs[0]}`;
      for (let i = 1; i < segs.length; i += 1) {
        if (!(segs[0] === "本地" && i === 1)) {
          const list = dirChildren.get(cur) || [];
          if (!list.includes(segs[i])) list.push(segs[i]);
          dirChildren.set(cur, list);
        }
        cur = `${cur}/${segs[i]}`;
        if (!dirChildren.has(cur)) dirChildren.set(cur, []);
      }
      return success(null);
    }
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) {
      const p = url.searchParams.get("path") || "/";
      return success((dirChildren.get(p) || []).map((name) => ({ name })));
    }
    if (url.pathname.endsWith("/alist")) {
      return success([{ id: 1, remark: "内置存储", url: "native://rclone" }]);
    }
    if (url.pathname.endsWith("/job") && url.searchParams.get("current")) {
      return success(null);
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: [], count: 0 });
    }
    return success(null);
  });

  await page.goto("/app/opensync/sync");
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "新建持续同步任务", exact: true })).toBeVisible();
  await expect(dialog.getByText("手动输入路径")).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "添加到源路径" })).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "添加到目标路径" })).toHaveCount(0);

  const trees = dialog.locator(".remote-paths");
  const popover = page.locator(".remote-paths-dropdown");

  // 1. Source picker: verify read-only & synthetic roots do not allow folder creation,
  // and create a folder directly inside an authorized local root.
  await trees.nth(0).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover).toBeVisible();
  await expect(popover.locator('[role="treeitem"][data-key="/"] .remote-paths-row-mkdir')).toHaveCount(0);
  await expect(popover.locator('[role="treeitem"][data-key="/本地"] .remote-paths-row-mkdir')).toHaveCount(0);
  await expect(popover.locator('[role="treeitem"][data-key="/ReadOnlyMirror"] .remote-paths-row-mkdir')).toHaveCount(0);

  const localRoot = popover.locator('[role="treeitem"][data-key="/本地/Photos~a1b2c3d4e5f6"]');
  await expect(localRoot).toBeVisible();
  await localRoot.getByRole("button", { name: "在 /本地/Photos 下新建文件夹" }).click();

  // Validate invalid folder name inline before creating
  await popover.getByRole("textbox", { name: "新文件夹名称" }).fill("bad/../name");
  await popover.getByRole("button", { name: "创建", exact: true }).click();
  await expect(popover.getByRole("alert")).toContainText("不能包含 . 或 ..");

  await popover.getByRole("textbox", { name: "新文件夹名称" }).fill("SyncInbox");
  await popover.getByRole("button", { name: "创建", exact: true }).click();
  await expect.poll(() => mkdirCalls[0]).toBe("/本地/Photos~a1b2c3d4e5f6/SyncInbox");
  await expect(
    popover.locator('[role="treeitem"][data-key="/本地/Photos~a1b2c3d4e5f6/SyncInbox"]'),
  ).toBeVisible();
  await expect(trees.nth(0)).toContainText("SyncInbox");
  await trees.nth(0).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover).toBeHidden();

  // 2. Destination picker: use the toolbar "新建文件夹" button after selecting parent
  // or directly on /PhotoCloud via row action.
  await trees.nth(1).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover).toBeVisible();
  const cloudNode = popover.locator('[role="treeitem"][data-key="/PhotoCloud"]');
  await cloudNode.getByRole("button", { name: "在 /PhotoCloud 下新建文件夹" }).click();
  await popover.getByRole("textbox", { name: "新文件夹名称" }).fill("Backup");
  await popover.getByRole("button", { name: "创建", exact: true }).click();
  await expect.poll(() => mkdirCalls[1]).toBe("/PhotoCloud/Backup");
  await expect(
    popover.locator('[role="treeitem"][data-key="/PhotoCloud/Backup"]'),
  ).toBeVisible();
  await expect(trees.nth(1)).toContainText("Backup");

  // 3. Toolbar "新建文件夹" button + Escape cancel + Enter submit
  await popover.getByRole("button", { name: "新建文件夹", exact: true }).click();
  await expect(popover.getByRole("textbox", { name: "新文件夹名称" })).toBeVisible();
  await popover.getByRole("textbox", { name: "新文件夹名称" }).press("Escape");
  await expect(popover).toBeVisible();
  await expect(popover.getByRole("textbox", { name: "新文件夹名称" })).toHaveCount(0);

  await popover.getByRole("button", { name: "新建文件夹", exact: true }).click();
  await popover.getByRole("textbox", { name: "新文件夹名称" }).fill("2026");
  await popover.getByRole("textbox", { name: "新文件夹名称" }).press("Enter");
  await expect.poll(() => mkdirCalls[2]).toBe("/PhotoCloud/Backup/2026");
  await expect(trees.nth(1)).toContainText("2026");
  await trees.nth(1).locator('[role="combobox"]').click({ position: { x: 2, y: 18 } });
  await expect(popover).toBeHidden();
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
});
