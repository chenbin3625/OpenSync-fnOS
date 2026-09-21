import { test, expect } from "@playwright/test";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

let engine: Server;
let engineUrl: string;

test.beforeAll(async () => {
  engine = createServer(async (request, response) => {
    response.setHeader("Content-Type", "application/json");
    if (request.url === "/api/me") {
      response.end(
        JSON.stringify({
          code: 200,
          message: "ok",
          data: { username: "layout-engine" },
        }),
      );
      return;
    }
    response.end(JSON.stringify({ code: 200, message: "ok", data: {} }));
  });
  await new Promise<void>((resolve) => engine.listen(0, "127.0.0.1", resolve));
  engineUrl = `http://127.0.0.1:${(engine.address() as AddressInfo).port}`;
});

test.afterAll(async () => {
  await new Promise<void>((resolve) => engine.close(() => resolve()));
});

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
  const firstNav = sidebar.getByRole("button", { name: "任务管理" });
  const firstNavBox = await firstNav.boundingBox();
  expect(firstNavBox?.width).toBe(204);
  expect(firstNavBox?.height).toBe(36);
  await expect(firstNav).toHaveCSS("font-size", "14px");
  await expect(firstNav).toHaveAttribute("aria-expanded", "true");
  await expect(sidebar.locator(".nav-submenu")).toHaveCount(1);
  await expect(firstNav.locator(".task-menu-triangle")).toHaveAttribute(
    "aria-label",
    "tree_triangle_down",
  );
  await expect(sidebar.locator(".task-sub-icon").first()).toHaveAttribute(
    "aria-label",
    "folder_stroked",
  );
  await expect(firstNav.locator(".task-menu-triangle")).toHaveCSS(
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
  request,
}, testInfo) => {
  test.skip(
    (page.viewportSize()?.width || 0) <= 640,
    "mobile uses bottom navigation",
  );
  const name = "菜单任务-" + testInfo.project.name + "-" + Date.now();
  await request.post("/app/opensync/svr/alist", {
    data: {
      url: engineUrl,
      token: "layout-token",
      remark: name,
    },
  });
  const engines = await (await request.get("/app/opensync/svr/alist")).json();
  const engineId = engines.data.find(
    (e: { remark: string }) => e.remark === name,
  ).id;
  await request.post("/app/opensync/svr/job", {
    data: {
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
  });
  const jobs = await (
    await request.get("/app/opensync/svr/job?pageNum=1&pageSize=100")
  ).json();
  const jobId = jobs.data.dataList.find(
    (j: { remark: string }) => j.remark === name,
  ).id;
  try {
    await page.goto(`/app/opensync/tasks?jobId=${jobId}`);
    const sidebar = page.locator(".app-sidebar");
    const taskToggle = sidebar.getByRole("button", {
      name: "任务管理",
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
  } finally {
    await request.delete(`/app/opensync/svr/job?id=${jobId}`);
    await request.delete(`/app/opensync/svr/alist?id=${engineId}`);
  }
});

test("task tabs and page commands share an integrated toolbar row", async ({
  page,
}) => {
  for (const [path, tab, action] of [
    ["tasks?tab=overview", "总览", "新建任务"],
    ["tasks?tab=realtime", "实时任务", null],
    ["tasks?tab=history", "历史任务", null],
  ]) {
    await page.goto(`/app/opensync/${path}`);
    const toolbar = page.locator(".page-toolbar");
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
    const tabBox = await toolbar
      .getByRole("tab", { name: tab, exact: true })
      .boundingBox();
    expect(tabBox).not.toBeNull();
    if (action) {
      const actionBox = await toolbar
        .getByRole("button", { name: action, exact: true })
        .boundingBox();
      expect(actionBox).not.toBeNull();
      if (page.viewportSize()!.width > 640) {
        expect(
          Math.abs(
            tabBox!.y + tabBox!.height / 2 - actionBox!.y - actionBox!.height / 2,
          ),
        ).toBeLessThan(8);
      }
    } else {
      expect(
        await toolbar.locator(".page-actions").textContent(),
      ).toBe("");
    }
  }
  // 引擎管理只有一个页面，不再有 Tab；工具栏仍须撑满高度并右对齐操作按钮。
  await page.goto("/app/opensync/engines");
  const toolbar = page.locator(".page-toolbar");
  await expect(toolbar.locator(".semi-tabs-bar")).toHaveCount(0);
  const toolbarBox = await toolbar.boundingBox();
  const actionBox = await toolbar
    .getByRole("button", { name: "添加引擎", exact: true })
    .boundingBox();
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

  await page.goto("/app/opensync/tasks?tab=overview");
  const toolbar = page.locator(".page-toolbar");
  const tabs = toolbar.getByRole("tab");
  const action = toolbar.getByRole("button", {
    name: "新建任务",
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

  await page.goto("/app/opensync/tasks?jobId=1&tab=overview");
  const toolbar = page.locator(".page-toolbar");
  const switcher = page.getByRole("tablist", { name: "任务列表" });
  await expect(switcher.getByRole("tab")).toHaveCount(2);

  const toolbarBox = await toolbar.boundingBox();
  const switcherBox = await switcher.boundingBox();
  expect(toolbarBox).not.toBeNull();
  expect(switcherBox).not.toBeNull();
  expect(switcherBox!.y).toBeGreaterThanOrEqual(
    toolbarBox!.y + toolbarBox!.height,
  );
});

test("task management top tabs keep a visible selected state", async ({ page }) => {
  await page.goto("/app/opensync/tasks?tab=history");
  const toolbar = page.locator(".page-toolbar");
  const activeTab = toolbar.getByRole("tab", {
    name: "历史任务",
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
  await expect(page.locator(".app-workspace")).toHaveCSS(
    "border-radius",
    "8px",
  );
  const section = page.locator(".settings-card").first();
  const pageBox = await page.locator(".page").boundingBox();
  const sectionBox = await section.boundingBox();
  expect(sectionBox!.width).toBeGreaterThan(pageBox!.width - 60);
  // 五个设置项合并成两张分组卡片，条目不再各自成卡。
  await expect(page.locator(".settings-card")).toHaveCount(2);
  await expect(page.locator(".settings-card").first()).toHaveCSS(
    "border-top-width",
    "1px",
  );
  const rows = page.locator(".settings-card-body .setting-row");
  await expect(rows).toHaveCount(5);
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
  request,
}, testInfo) => {
  const name = "检查数据-" + testInfo.project.name + "-" + Date.now();
  let engineId: number | undefined;
  let jobId: number | undefined;
  let notifyId: number | undefined;
  try {
    await request.post("/app/opensync/svr/alist", {
      data: {
        url: engineUrl,
        token: "fixture-token",
        remark: name,
      },
    });
    const engines = await (
      await request.get("/app/opensync/svr/alist")
    ).json();
    engineId = engines.data.find(
      (item: { remark: string }) => item.remark === name,
    ).id;

    await request.post("/app/opensync/svr/job", {
      data: {
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
    });
    const jobs = await (
      await request.get("/app/opensync/svr/job?pageNum=1&pageSize=100")
    ).json();
    jobId = jobs.data.dataList.find(
      (item: { remark: string }) => item.remark === name,
    ).id;

    await request.post("/app/opensync/svr/notify", {
      data: {
        notify: {
          method: 0,
          enable: 1,
          params: JSON.stringify({
            url: engineUrl + "/hook",
            httpMethod: "POST",
            contentType: "application/json",
            needContent: true,
            titleName: "title",
            contentName: "content",
            notSendNull: false,
          }),
        },
      },
    });
    const notifications = await (
      await request.get("/app/opensync/svr/notify")
    ).json();
    notifyId = notifications.data.at(-1).id;

    await page.goto("/app/opensync/engines");
    const engineCard = page
      .locator(".engine-item")
      .filter({ hasText: name })
      .first();
    await expect(engineCard.getByRole("heading", { name, exact: true })).toBeVisible();
    await expect(engineCard.locator(".item-symbol")).toHaveCount(0);
    await expect(
      engineCard.getByRole("button", { name: "测试引擎", exact: true }),
    ).toBeVisible();

    await page.goto(`/app/opensync/tasks?jobId=${jobId}`);
    await expect(
      page.locator(".app-sidebar").getByRole("link", { name, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name, exact: true }),
    ).toBeVisible();

    await page.goto("/app/opensync/notifications");
    const notifySwitch = page.getByLabel(`通知 ${notifyId} 开关`, { exact: true });
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
    await page.screenshot({
      path: `test-results/data-${testInfo.project.name}.png`,
      fullPage: true,
    });
  } finally {
    if (notifyId)
      await request.delete(
        `/app/opensync/svr/notify?notifyId=${notifyId}`,
      );
    if (jobId)
      await request.delete(`/app/opensync/svr/job?id=${jobId}`);
    if (engineId)
      await request.delete(`/app/opensync/svr/alist?id=${engineId}`);
  }
});

test("task cards follow the reference card style and fill the detail pane", async ({
  page,
  request,
}, testInfo) => {
  const name = "布局任务-" + testInfo.project.name + "-" + Date.now();
  await request.post("/app/opensync/svr/alist", {
    data: {
      url: engineUrl,
      token: "layout-token",
      remark: name,
    },
  });
  const engines = await (await request.get("/app/opensync/svr/alist")).json();
  const engineId = engines.data.find(
    (e: { remark: string }) => e.remark === name,
  ).id;
  await request.post("/app/opensync/svr/job", {
    data: {
      alistId: engineId,
      srcPath: JSON.stringify(["/docker"]),
      dstPath: JSON.stringify(["/dav/nas/docker"]),
      method: 0,
      isCron: 1,
      enable: 1,
      hour: "2",
      minute: "0",
      second: "0",
      useCacheS: false,
      useCacheT: false,
      remark: name,
    },
  });
  const jobs = await (
    await request.get("/app/opensync/svr/job?pageNum=1&pageSize=100")
  ).json();
  const jobId = jobs.data.dataList.find(
    (j: { remark: string }) => j.remark === name,
  ).id;
  try {
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
    // 源 → 目标 流向，以及「下次执行」
    await expect(summary.locator(".flow-arrow")).toBeVisible();
    await expect(
      summary.locator(".flow-next").getByText("下次执行", { exact: true }),
    ).toBeVisible();
    await expect(
      summary.getByText("/docker", { exact: true }),
    ).toBeVisible();
    await expect(
      summary.getByText("/dav/nas/docker", { exact: true }),
    ).toBeVisible();
    // 「⋯」菜单里的操作与参考稿一致
    await menu.click();
    for (const label of ["设置", "删除"]) {
      await expect(
        page.locator(".semi-dropdown-menu").getByText(label, { exact: true }),
      ).toBeVisible();
    }
    await page.keyboard.press("Escape");
  } finally {
    await request.delete(`/app/opensync/svr/job?id=${jobId}`);
    await request.delete(`/app/opensync/svr/alist?id=${engineId}`);
  }
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
    if (url.pathname.endsWith("/notify")) return success([]);
    return success(null);
  });
  const layouts: Array<{
    path: string;
    height: number;
    topOffset: number;
    imageOffset: number;
  }> = [];
  for (const path of ["tasks", "engines", "notifications"]) {
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

test("editor is 454px on desktop and remains within the viewport", async ({
  page,
}) => {
  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "添加引擎", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await expect
    .poll(async () => (await dialog.boundingBox())?.width)
    .toBe(page.viewportSize()!.width <= 640 ? page.viewportSize()!.width : 454);
  await expect(page.locator(".semi-modal-mask")).toHaveCSS(
    "background-color",
    "rgba(255, 255, 255, 0.65)",
  );
  const bounds = await dialog.boundingBox();
  const viewport = page.viewportSize()!;
  expect(bounds!.width).toBe(
    viewport.width <= 640 ? viewport.width : 454,
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
    dialog.getByRole("button", { name: "保存", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: `test-results/dialog-${test.info().project.name}.png`,
  });
});

test("dirty-confirm dialog is centered", async ({ page }) => {
  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "添加引擎", exact: true }).click();
  await page
    .getByRole("textbox", { name: "引擎名称", exact: true })
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
