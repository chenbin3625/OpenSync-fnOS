import { test, expect, type Page } from "@playwright/test";

async function routeLayoutFixtures(
  page: Page,
  {
    engines = [],
    remotes = [],
    jobs = [],
    notifications = [],
  }: {
    engines?: Record<string, unknown>[];
    remotes?: Record<string, unknown>[];
    jobs?: Record<string, unknown>[];
    notifications?: Record<string, unknown>[];
  },
) {
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/version/latest"))
      return success({
        latestVersion: "v0.0.0",
        releaseURL: "https://example.test/releases",
        hasUpdate: false,
      });
    if (url.pathname.endsWith("/system/config"))
      return success({
        taskTimeout: 48,
        taskSave: 30,
        copyConcurrency: 5,
        scanConcurrency: 16,
        maxRetries: 2,
      });
    if (url.pathname.endsWith("/alist")) return success(engines);
    if (url.pathname.endsWith("/native/storage")) {
      return success({ engineId: engines[0]?.id || 0, enabled: true, rcloneAvailable: true, remotes, localRoots: [] });
    }
    if (url.pathname.endsWith("/notify")) return success(notifications);
    if (url.pathname.endsWith("/job") && url.searchParams.has("current"))
      return success(null);
    if (
      url.pathname.endsWith("/job") &&
      (url.searchParams.has("taskId") || url.searchParams.has("id"))
    )
      return success({ dataList: [], count: 0 });
    if (url.pathname.endsWith("/job"))
      return success({ dataList: jobs, count: jobs.length });
    return success(null);
  });
}

async function routeMobileTaskFixture(page: Page) {
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/job") && url.searchParams.has("current"))
      return success(null);
    if (url.pathname.endsWith("/job")) {
      return success({
        dataList: [
          {
            id: 1,
            enable: 1,
            remark: "相册备份",
            srcPath: '["/photos"]',
            dstPath: '["/backup"]',
            alistId: 1,
            method: 0,
            interval: 0,
            isCron: 2,
          },
          {
            id: 2,
            enable: 1,
            remark: "工作资料同步",
            srcPath: '["/work"]',
            dstPath: '["/backup"]',
            alistId: 1,
            method: 0,
            interval: 0,
            isCron: 2,
          },
        ],
        count: 2,
      });
    }
    return success(null);
  });
}



test("desktop sidebar is 220px with icon menus and bottom settings", async ({
  page,
}) => {
  test.skip(
    (page.viewportSize()?.width || 0) <= 640,
    "mobile uses bottom navigation",
  );
  await page.route("**/app/opensync/svr/job**", async (route) => {
    const url = new URL(route.request().url());
    const data = url.searchParams.has("current")
      ? null
      : {
          dataList: [{ id: 1, remark: "布局测试任务", enable: 1 }],
          count: 1,
        };
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({ code: 200, data, msg: "" }),
    });
  });
  await page.goto("/app/opensync/tasks");
  const sidebar = page.locator(".app-sidebar");
  await expect(sidebar).toBeVisible();
  expect((await sidebar.boundingBox())?.width).toBe(220);
  await expect(sidebar.locator("nav .nav-link .nav-label")).toHaveText([
    "同步工作台",
    "总览看板",
    "存储管理",
    "通知配置",
  ]);
  const firstNav = sidebar.locator("nav .nav-link").first();
  await expect(firstNav).toHaveText("同步工作台");
  const firstNavBox = await firstNav.boundingBox();
  expect(firstNavBox?.width).toBe(204);
  expect(firstNavBox?.height).toBe(36);
  await expect(firstNav).toHaveCSS("font-size", "14px");
  const syncNav = sidebar.getByRole("link", { name: "同步工作台", exact: true });
  const syncNavBox = await syncNav.boundingBox();
  expect(syncNavBox?.width).toBe(204);
  expect(syncNavBox?.height).toBe(36);
  await expect(syncNav).toHaveCSS("font-size", "14px");
  const toggle = sidebar.getByRole("button", { name: "任务快捷列表" });
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(sidebar.locator(".nav-submenu")).toHaveCount(1);
  await expect(toggle.locator(".task-menu-triangle")).toHaveAttribute(
    "aria-label",
    "tree_triangle_down",
  );
  await expect(sidebar.locator(".task-sub-icon").first()).toHaveAttribute(
    "aria-label",
    "folder_stroked",
  );
  await expect(toggle.locator(".task-menu-triangle")).toHaveCSS(
    "font-size",
    "16px",
  );
  await expect(page.getByRole("button", { name: "切换导航" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "切换主题" })).toHaveCount(0);
  const settings = page.locator(".sidebar-settings");
  await expect(settings.getByRole("link", { name: "设置" })).toBeVisible();
  const bounds = await settings.boundingBox();
  expect(bounds!.y + bounds!.height).toBeGreaterThan(
    page.viewportSize()!.height - 24,
  );
  expect(
    await settings.evaluate((el) => getComputedStyle(el).borderTopWidth),
  ).toBe("1px");
});

test("desktop shell does not overflow a short host window", async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 400 });
  await page.goto("/app/opensync/tasks");
  await page.locator(".app-shell").waitFor();

  const layout = await page.evaluate(() => {
    const root = document.documentElement;
    const shell = document.querySelector<HTMLElement>(".app-shell")!;
    const workspace = document.querySelector<HTMLElement>(".app-workspace")!;
    return {
      viewportHeight: window.innerHeight,
      documentClientHeight: root.clientHeight,
      documentScrollHeight: root.scrollHeight,
      shellClientHeight: shell.clientHeight,
      shellScrollHeight: shell.scrollHeight,
      workspaceBottom: workspace.getBoundingClientRect().bottom,
    };
  });

  expect(layout.documentScrollHeight).toBe(layout.documentClientHeight);
  expect(layout.shellScrollHeight).toBe(layout.shellClientHeight);
  expect(layout.workspaceBottom).toBeLessThanOrEqual(layout.viewportHeight);
});

test("task management expands in the sidebar and lists task names", async ({
  page,
}, testInfo) => {
  test.skip(
    (page.viewportSize()?.width || 0) <= 640,
    "mobile uses bottom navigation",
  );
  const name = "菜单任务-" + testInfo.project.name;
  const engineId = 101;
  const jobId = 201;
  await routeLayoutFixtures(page, {
    engines: [
      {
        id: engineId,
        url: "http://127.0.0.1:18080",
        token: "layout-token",
        remark: name,
        userName: "layout-engine",
      },
    ],
    jobs: [
      {
        id: jobId,
        alistId: engineId,
        srcPath: JSON.stringify(["/photos"]),
        dstPath: JSON.stringify(["/backup/photos"]),
        method: 0,
        isCron: 2,
        enable: 1,
        useCacheS: false,
        useCacheT: false,
        remark: name,
      },
    ],
  });
  await page.goto(`/app/opensync/tasks?jobId=${jobId}`);
  const sidebar = page.locator(".app-sidebar");
  const taskToggle = sidebar.getByRole("button", {
    name: "任务快捷列表",
    exact: true,
  });
  await expect(taskToggle).toHaveAttribute("aria-expanded", "true");
  await expect(
    taskToggle.locator(".task-menu-triangle"),
  ).toHaveAttribute("aria-label", "tree_triangle_down");
  const taskLink = sidebar.getByRole("link", { name, exact: true });
  await expect(taskLink).toBeVisible();
  await expect(taskLink.locator(".task-sub-icon")).toHaveAttribute(
    "aria-label",
    "folder_stroked",
  );
  await expect(taskLink).toHaveCSS("background-color", "rgb(255, 255, 255)");
  await expect(page.locator(".task-list-pane")).toHaveCount(0);
  await expect(page.locator(".task-detail-pane")).toBeVisible();
});

test("task tabs and page commands share an integrated toolbar row", async ({
  page,
}) => {
  const toolbars = [
    { path: "tasks?tab=overview", tab: "任务详情" },
    { path: "tasks?tab=records", tab: "同步记录" },
  ];
  for (const { path, tab } of toolbars) {
    await page.goto(`/app/opensync/${path}`);
    const toolbar = page.locator(".page-toolbar");
    await expect(toolbar).toBeVisible();
    const tabLocator = toolbar.getByRole("tab", { name: tab, exact: true });
    await expect(tabLocator).toBeVisible();
    await expect(tabLocator).toHaveAttribute("aria-selected", "true");
    await expect(toolbar).toHaveCSS("border-radius", "8px 8px 0px 0px");
    await expect(toolbar.locator(".semi-tabs-bar")).toHaveCSS(
      "border-bottom-width",
      "0px",
    );
    await expect(toolbar.locator(".semi-tabs-bar")).toHaveCSS(
      "border-top-width",
      "0px",
    );
    await expect(toolbar.locator(".semi-tabs-bar")).toHaveCSS(
      "background-color",
      "rgba(0, 0, 0, 0)",
    );
    const tabBox = await tabLocator.boundingBox();
    expect(tabBox).not.toBeNull();
    const actionButton = toolbar.getByRole("button", { name: "返回任务列表", exact: true });
    await expect(actionButton).toBeVisible();
    const actionBox = await actionButton.boundingBox();
    expect(actionBox).not.toBeNull();
    if (page.viewportSize()!.width > 640) {
      expect(
        Math.abs(
          tabBox!.y + tabBox!.height / 2 - actionBox!.y - actionBox!.height / 2,
        ),
      ).toBeLessThan(8);
    }
  }
  // 引擎管理只有一个页面，不再有 Tab；工具栏仍须撑满高度并右对齐操作按钮。
  await page.goto("/app/opensync/engines");
  const toolbar = page.locator(".page-toolbar");
  await expect(toolbar).toBeVisible();
  await expect(toolbar.locator(".semi-tabs-bar")).toHaveCount(0);
  const addEngineButton = toolbar.getByRole("button", {
    name: "添加远程存储",
    exact: true,
  });
  await expect(addEngineButton).toBeVisible();
  const toolbarBox = await toolbar.boundingBox();
  const actionBox = await addEngineButton.boundingBox();
  expect(toolbarBox!.height).toBeLessThan(80);
  expect(
    Math.abs(
      actionBox!.y + actionBox!.height / 2 - toolbarBox!.y - toolbarBox!.height / 2,
    ),
  ).toBeLessThan(8);
});

test("mobile task tabs fit inside the page toolbar", async ({ page }) => {
  test.skip(
    (page.viewportSize()?.width || 0) > 640,
    "desktop uses the fixed-height toolbar",
  );

  await routeMobileTaskFixture(page);
  await page.goto("/app/opensync/tasks?tab=overview");
  await expect(page.locator(".mobile-navigation a")).toHaveText([
    "同步工作台",
    "总览看板",
    "存储管理",
    "通知配置",
    "设置",
  ]);
  const toolbar = page.locator(".page-toolbar");
  const tabs = toolbar.getByRole("tab");
  const action = toolbar.getByRole("button", {
    name: "返回任务列表",
    exact: true,
  });

  const toolbarBox = await toolbar.boundingBox();
  expect(toolbarBox).not.toBeNull();
  for (const item of [tabs.first(), tabs.last(), action]) {
    const itemBox = await item.boundingBox();
    expect(itemBox).not.toBeNull();
    expect(itemBox!.y).toBeGreaterThanOrEqual(toolbarBox!.y);
    expect(itemBox!.y + itemBox!.height).toBeLessThanOrEqual(
      toolbarBox!.y + toolbarBox!.height,
    );
  }

  expect(await toolbar.evaluate((element) => element.scrollHeight)).toBe(
    await toolbar.evaluate((element) => element.clientHeight),
  );
});

test("mobile task switcher stays below the toolbar", async ({ page }) => {
  test.skip(
    (page.viewportSize()?.width || 0) > 640,
    "mobile layout regression only",
  );

  await routeMobileTaskFixture(page);
  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  const toolbar = page.locator(".page-toolbar");
  const switcher = page.getByRole("tablist", { name: "任务列表" });
  await expect(switcher.getByRole("tab")).toHaveCount(2);

  const toolbarBox = await toolbar.boundingBox();
  const switcherBox = await switcher.boundingBox();
  expect(toolbarBox).not.toBeNull();
  expect(switcherBox).not.toBeNull();
  expect(
    switcherBox!.y - (toolbarBox!.y + toolbarBox!.height),
  ).toBeGreaterThanOrEqual(16);
});

test("task management top tabs keep a visible selected state", async ({ page }) => {
  await page.goto("/app/opensync/tasks?tab=records");
  const toolbar = page.locator(".page-toolbar");
  const activeTab = toolbar.getByRole("tab", {
    name: "同步记录",
    exact: true,
  });

  await expect(activeTab).toHaveClass(/semi-tabs-tab-active/);
  await expect(activeTab).toHaveCSS("color", "rgb(0, 102, 255)");
  await expect(activeTab).toHaveCSS("border-bottom-width", "2px");
  await expect(activeTab).toHaveCSS("border-bottom-color", "rgb(0, 102, 255)");

  if (page.viewportSize()!.width > 640) {
    const toolbarBox = await toolbar.boundingBox();
    const activeTabBox = await activeTab.boundingBox();
    expect(toolbarBox).not.toBeNull();
    expect(activeTabBox).not.toBeNull();
    expect(activeTabBox!.y + activeTabBox!.height).toBeLessThanOrEqual(
      toolbarBox!.y + toolbarBox!.height,
    );
  }
});

test("primary surface, cards and controls use consistent radii and full width", async ({
  page,
}) => {
  await page.goto("/app/opensync/settings");
  // Mobile deliberately goes edge-to-edge; the desktop shell keeps the radius.
  await expect(page.locator(".app-workspace")).toHaveCSS(
    "border-radius",
    page.viewportSize()!.width <= 640 ? "0px" : "8px",
  );
  const section = page.locator(".settings-card").first();
  const pageBox = await page.locator(".page").boundingBox();
  const sectionBox = await section.boundingBox();
  expect(sectionBox!.width).toBeGreaterThan(pageBox!.width - 60);
  await expect(section.getByRole("heading", { name: "任务执行与安全保护" })).toBeVisible();
  await expect(page.locator(".settings-card").first()).toHaveCSS(
    "border-top-width",
    "1px",
  );
  const rows = page.locator(".settings-card-body .setting-row");
  await expect(rows.first()).toBeVisible();
  await expect(page.locator(".settings-card .settings-card")).toHaveCount(0);
  // 分组标题不带图标，卡片内部也不画分隔线。
  await expect(page.locator(".settings-card .item-symbol")).toHaveCount(0);
  await expect(page.locator(".settings-card-head .semi-icon")).toHaveCount(0);
  await expect(rows.first()).toHaveCSS("border-top-width", "0px");
  await expect(rows.first()).toHaveCSS("border-bottom-width", "0px");

  const input = page.getByRole("textbox", { name: "操作并发数", exact: true });
  await input.hover();
  const wrapper = input.locator(
    "xpath=ancestor::*[contains(@class, 'semi-input-wrapper')][1]",
  );
  await expect(wrapper).toHaveCSS("background-color", "rgb(255, 255, 255)");
  await expect(wrapper).toHaveCSS("border-top-color", "rgb(0, 102, 255)");

  await input.fill("9");
  const save = page.getByRole("button", { name: "保存", exact: true });
  await expect(save).toBeEnabled();
  await expect(save).toHaveCSS("background-color", "rgb(0, 102, 255)");
  await expect(save).toHaveCSS("border-radius", "8px");
});

test("data fixture renders inspectable engine, task and notification rows", async ({
  page,
}, testInfo) => {
  const name = "检查数据-" + testInfo.project.name;
  const engineId = 102;
  const jobId = 202;
  const notifyId = 302;
  await routeLayoutFixtures(page, {
    engines: [
      {
        id: engineId,
        url: "http://127.0.0.1:18080",
        token: "fixture-token",
        remark: name,
        userName: "layout-engine",
      },
    ],
    remotes: [{ name, type: "webdav", summary: "WebDAV", options: {}, configuredSecrets: [], usedByJobs: [], usedByCrypts: [] }],
    jobs: [
      {
        id: jobId,
        alistId: engineId,
        srcPath: JSON.stringify(["/Photos"]),
        dstPath: JSON.stringify(["/Backup"]),
        method: 0,
        isCron: 2,
        enable: 1,
        useCacheS: false,
        useCacheT: false,
        remark: name,
      },
    ],
    notifications: [
      {
        id: notifyId,
        method: 0,
        enable: 1,
        params: JSON.stringify({
          url: "http://127.0.0.1:18080/hook",
          httpMethod: "POST",
          contentType: "application/json",
          needContent: true,
          titleName: "title",
          contentName: "content",
          notSendNull: false,
        }),
      },
    ],
  });

  await page.goto("/app/opensync/engines");
  const engineCard = page
    .locator(".engine-item")
    .filter({ hasText: name })
    .first();
  await expect(engineCard.getByRole("heading")).toContainText(name);
  await expect(engineCard.locator(".item-symbol")).toHaveCount(0);
  await expect(
    engineCard.getByRole("button", { name: `测试连接 ${name}`, exact: true }),
  ).toBeVisible();

  await page.goto(`/app/opensync/tasks?jobId=${jobId}`);
  // Mobile replaces the sidebar with the bottom navigation, so the task name
  // only appears there on desktop.
  if ((page.viewportSize()?.width || 0) > 640) {
    await expect(
      page.locator(".app-sidebar").getByRole("link", { name, exact: true }),
    ).toBeVisible();
  }
  await expect(
    page.getByRole("heading", { name, exact: true }),
  ).toBeVisible();

  await page.goto("/app/opensync/notifications");
  const notifySwitch = page
    .locator(`.notification-item[data-notify-id="${notifyId}"]`)
    .getByRole("switch", { name: "启用自定义 Webhook", exact: true });
  await expect(notifySwitch).toBeVisible();
  await expect(page.locator(".notification-item .item-symbol")).toHaveCount(0);
  await expect(
    notifySwitch.locator("xpath=ancestor::*[contains(@class, 'semi-switch')][1]"),
  ).toHaveCSS("width", "40px");
  await expect(
    notifySwitch.locator("xpath=ancestor::*[contains(@class, 'semi-switch')][1]"),
  ).toHaveCSS("height", "24px");
  await expect(
    notifySwitch.locator("xpath=ancestor::*[contains(@class, 'semi-switch')][1]"),
  ).toHaveCSS("background-color", "rgb(0, 102, 255)");
  if (process.env.PLAYWRIGHT_SCREENSHOTS === "1")
    await page.screenshot({
      path: `test-results/data-${testInfo.project.name}.png`,
      fullPage: true,
    });
});

test("task cards follow the reference card style and fill the detail pane", async ({
  page,
}, testInfo) => {
  const name = "布局任务-" + testInfo.project.name;
  const engineId = 103;
  const jobId = 203;
  await routeLayoutFixtures(page, {
    engines: [
      {
        id: engineId,
        url: "http://127.0.0.1:18080",
        token: "layout-token",
        remark: name,
        userName: "layout-engine",
      },
    ],
    jobs: [
      {
        id: jobId,
        alistId: engineId,
        srcPath: JSON.stringify(["/docker"]),
        dstPath: JSON.stringify(["/dav/nas/docker"]),
        method: 0,
        isCron: 3,
        enable: 1,
        useCacheS: false,
        useCacheT: false,
        remark: name,
      },
    ],
  });
  await page.goto(`/app/opensync/tasks?jobId=${jobId}`);
  const summary = page.locator(".overview-card");
  const detail = page.locator(".task-detail-pane");
  await expect(summary).toBeVisible();
  await expect(summary).toHaveCSS("background-color", "rgb(255, 255, 255)");
  await expect(summary).toHaveCSS("border-radius", "8px");
  await expect(summary).toHaveCSS("border-top-width", "1px");
  const summaryBox = await summary.boundingBox();
  const detailBox = await detail.boundingBox();
  expect(summaryBox!.width).toBeGreaterThan(detailBox!.width - 12);
  // 卡片头部：名称 + 状态标签 + 「⋯」菜单
  await expect(
    summary.getByRole("heading", { name, exact: true }),
  ).toBeVisible();
  const menu = summary.getByRole("button", {
    name: "更多操作",
    exact: true,
  });
  await expect(menu).toBeVisible();
  // 源 → 目标 路由卡片，以及持续同步状态面板
  await expect(summary.locator(".sync-route-arrow")).toBeVisible();
  await expect(summary.locator(".continuous-status-card")).toBeVisible();
  await expect(
    summary.getByText("/docker", { exact: true }),
  ).toBeVisible();
  await expect(
    summary.getByText("/dav/nas/docker", { exact: true }),
  ).toBeVisible();
  await expect(summary.getByRole("button", { name: "编辑任务", exact: true })).toBeVisible();
  await menu.click();
  for (const label of ["复制任务", "删除"]) {
    await expect(
      page.locator(".semi-dropdown-menu").getByText(label, { exact: true }),
    ).toBeVisible();
  }
  await page.keyboard.press("Escape");
});

test("empty pages render the reference folder asset", async ({ page }) => {
  await page.route("**/app/opensync/svr/**", async (route) => {
    const url = new URL(route.request().url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });
    if (url.pathname.endsWith("/session"))
      return success({ uid: 1, development: false, version: "test" });
    if (url.pathname.endsWith("/job"))
      return success({ dataList: [], count: 0 });
    if (url.pathname.endsWith("/alist")) return success([]);
    if (url.pathname.endsWith("/native/storage"))
      return success({ engineId: 1, enabled: true, rcloneAvailable: true, remotes: [], localRoots: [] });
    if (url.pathname.endsWith("/notify")) return success([]);
    return success(null);
  });
  const layouts: Array<{
    path: string;
    height: number;
    topOffset: number;
    imageOffset: number;
  }> = [];
  for (const path of ["tasks", "notifications"]) {
    await page.goto(`/app/opensync/${path}`);
    const toolbarBox = await page.locator(".page-toolbar").boundingBox();
    const empty = page.getByRole("status", { name: "空空如也", exact: true });
    await expect(empty).toBeVisible();
    const emptyBox = await empty.boundingBox();
    const imageBox = await empty.locator("img").boundingBox();
    expect(toolbarBox).not.toBeNull();
    expect(emptyBox).not.toBeNull();
    expect(imageBox).not.toBeNull();
    const toolbarBottom = toolbarBox!.y + toolbarBox!.height;
    layouts.push({
      path,
      height: emptyBox!.height,
      topOffset: emptyBox!.y - toolbarBottom,
      imageOffset: imageBox!.y + imageBox!.height / 2 - toolbarBottom,
    });
    expect(
      await empty
        .locator("img")
        .evaluate(
          (img: HTMLImageElement) => img.complete && img.naturalWidth > 0,
        ),
    ).toBe(true);
  }
  await page.goto("/app/opensync/engines");
  await expect(page.locator(".storage-card--local").getByRole("heading")).toContainText("本地授权目录");
  await expect(page.getByText(/^尚未添加远程存储/)).toBeVisible();
  const [reference, ...others] = layouts;
  for (const layout of others) {
    expect.soft(layout.height, `${layout.path} empty height`).toBe(reference.height);
    expect
      .soft(
        Math.abs(layout.topOffset - reference.topOffset),
        `${layout.path} empty top offset`,
      )
      .toBeLessThanOrEqual(1);
    expect
      .soft(
        Math.abs(layout.imageOffset - reference.imageOffset),
        `${layout.path} empty image offset`,
      )
      .toBeLessThanOrEqual(1);
  }
});

test("storage editor is 660px on desktop and remains within the viewport", async ({
  page,
}) => {
  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "添加远程存储", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect
    .poll(async () => (await dialog.boundingBox())?.width)
    .toBe(page.viewportSize()!.width <= 640 ? page.viewportSize()!.width : 660);
  await expect(page.locator(".semi-modal-mask")).toHaveCSS(
    "background-color",
    "rgba(255, 255, 255, 0.65)",
  );
  const bounds = await dialog.boundingBox();
  const viewport = page.viewportSize()!;
  expect(bounds!.width).toBe(
    viewport.width <= 640 ? viewport.width : 660,
  );
  expect(bounds!.x).toBeGreaterThanOrEqual(0);
  expect(bounds!.y).toBeGreaterThanOrEqual(0);
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(
    viewport.height + 1,
  );
  expect(
    Math.abs(bounds!.x + bounds!.width / 2 - viewport.width / 2),
  ).toBeLessThanOrEqual(1);
  expect(
    Math.abs(bounds!.y + bounds!.height / 2 - viewport.height / 2),
  ).toBeLessThanOrEqual(1);
  await expect(
    dialog.getByRole("button", { name: "保存存储", exact: true }),
  ).toBeVisible();
  if (process.env.PLAYWRIGHT_SCREENSHOTS === "1")
    await page.screenshot({
      path: `test-results/dialog-${test.info().project.name}.png`,
    });
});

test("dirty-confirm dialog is centered", async ({ page }) => {
  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "添加远程存储", exact: true }).click();
  await page
    .getByRole("textbox", { name: "存储名称", exact: true })
    .fill("dirty");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  const dialog = page
    .getByRole("dialog")
    .filter({ hasText: "放弃未保存的修改？" });
  await expect(dialog).toBeVisible();
  const title = dialog.getByText("放弃未保存的修改？", { exact: true });
  await expect(title).toBeVisible();
  const viewport = page.viewportSize()!;
  await expect
    .poll(async () => {
      const dialogBox = await dialog.boundingBox();
      return Math.abs(dialogBox!.x + dialogBox!.width / 2 - viewport.width / 2);
    })
    .toBeLessThanOrEqual(1);
  await expect
    .poll(async () => {
      const dialogBox = await dialog.boundingBox();
      return Math.abs(dialogBox!.y + dialogBox!.height / 2 - viewport.height / 2);
    })
    .toBeLessThanOrEqual(1);
});

test("settings save stays at the top right", async ({ page }) => {
  await page.goto("/app/opensync/settings");
  const save = page
    .locator(".page-toolbar")
    .getByRole("button", { name: "保存", exact: true });
  await expect(save).toBeVisible();
  await expect(page.getByRole("textbox", { name: "操作并发数", exact: true })).toBeVisible();
  const bounds = await save.boundingBox();
  expect(bounds!.y).toBeLessThan(60);
  expect(bounds!.x + bounds!.width).toBeGreaterThan(
    page.viewportSize()!.width - 50,
  );
  // 设置页没有左侧 tab，操作栏底部的分隔条应当去掉。
  await expect(page.locator(".page-toolbar")).toHaveCSS(
    "border-bottom-width",
    "0px",
  );
  if (process.env.PLAYWRIGHT_SCREENSHOTS === "1")
    await page.screenshot({
      path: `test-results/settings-${test.info().project.name}.png`,
    });
});

test("toolbar keeps its divider only when tabs are present", async ({
  page,
}) => {
  await page.goto("/app/opensync/settings");
  await expect(page.locator(".page-toolbar .semi-tabs-bar")).toHaveCount(0);
  await expect(page.locator(".page-toolbar")).toHaveCSS("border-bottom-width", "0px");
  await page.goto("/app/opensync/tasks");
  const toolbar = page.locator(".page-toolbar");
  await expect(toolbar.locator(".page-tabs .semi-tabs-tab").first()).toBeVisible();
  await expect(toolbar).toHaveCSS("border-bottom-width", "1px");
});

test("settings page stops scrolling at the bottom log card without blank document overflow", async ({
  page,
}) => {
  await page.route("**/app/opensync/svr/system/logs**", async (route) => {
    const lines = Array.from(
      { length: 120 },
      (_, idx) => `2026/11/26 12:00:${String(idx % 60).padStart(2, "0")} INFO settings log entry #${idx + 1}`,
    );
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        code: 200,
        data: { lines, total: lines.length, fileSize: 8192, source: "memory" },
        msg: "",
      }),
    });
  });

  await page.goto("/app/opensync/settings");
  await expect(page.locator(".log-viewer-content")).toBeVisible();

  const metrics = await page.evaluate(() => {
    const root = document.documentElement;
    const shell = document.querySelector<HTMLElement>(".app-shell")!;
    const workspace = document.querySelector<HTMLElement>(".app-workspace")!;
    const logBox = document.querySelector<HTMLElement>(".log-viewer-box")!;

    workspace.scrollTop = workspace.scrollHeight;
    logBox.scrollTop = logBox.scrollHeight;
    window.scrollTo(0, 99999);

    const lastCard = [...document.querySelectorAll<HTMLElement>(".settings-card")].at(-1)!;
    return {
      documentClientHeight: root.clientHeight,
      documentScrollHeight: root.scrollHeight,
      windowScrollY: window.scrollY,
      shellClientHeight: shell.clientHeight,
      shellScrollHeight: shell.scrollHeight,
      workspaceBottom: workspace.getBoundingClientRect().bottom,
      lastCardBottom: lastCard.getBoundingClientRect().bottom,
      logScrollMax: logBox.scrollHeight - logBox.clientHeight,
      logScrollTop: logBox.scrollTop,
    };
  });

  expect(metrics.documentScrollHeight).toBe(metrics.documentClientHeight);
  expect(metrics.shellScrollHeight).toBe(metrics.shellClientHeight);
  expect(metrics.windowScrollY).toBe(0);
  expect(metrics.logScrollMax).toBeGreaterThan(0);
  expect(Math.abs(metrics.logScrollTop - metrics.logScrollMax)).toBeLessThanOrEqual(2);
  expect(metrics.lastCardBottom).toBeLessThanOrEqual(metrics.workspaceBottom + 1);
});
