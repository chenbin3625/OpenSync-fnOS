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
  await page.goto("/app/opensync/tasks");
  const sidebar = page.locator(".app-sidebar");
  await expect(sidebar).toBeVisible();
  expect((await sidebar.boundingBox())?.width).toBe(220);
  const firstNav = sidebar.getByRole("link", { name: "任务管理" });
  const firstNavBox = await firstNav.boundingBox();
  expect(firstNavBox?.width).toBe(204);
  expect(firstNavBox?.height).toBe(36);
  await expect(firstNav).toHaveCSS("font-size", "14px");
  await expect(firstNav).not.toHaveAttribute("aria-expanded", /.+/);
  await expect(sidebar.locator(".nav-submenu")).toHaveCount(0);
  await expect(sidebar.locator(".semi-icon")).toHaveCount(4);
  await expect(sidebar.locator(".semi-icon").first()).toHaveAttribute(
    "aria-label",
    "cloud_stroked",
  );
  await expect(sidebar.locator(".semi-icon").first()).toHaveCSS(
    "font-size",
    "16px",
  );
  await expect(page.getByRole("button", { name: "切换导航" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "切换主题" })).toHaveCount(0);
  const settings = page.locator(".sidebar-settings");
  await expect(settings.getByRole("link", { name: "系统设置" })).toBeVisible();
  const bounds = await settings.boundingBox();
  expect(bounds!.y + bounds!.height).toBeGreaterThan(
    page.viewportSize()!.height - 24,
  );
  expect(
    await settings.evaluate((el) => getComputedStyle(el).borderTopWidth),
  ).toBe("1px");
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

test("task and engine tabs share an integrated toolbar row with commands", async ({
  page,
}) => {
  for (const [path, tab, action] of [
    ["tasks", "总览", "新建任务"],
    ["engines", "OpenList / AList", "添加引擎"],
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
    const actionBox = await toolbar
      .getByRole("button", { name: action, exact: true })
      .boundingBox();
    expect(tabBox).not.toBeNull();
    expect(actionBox).not.toBeNull();
    if (page.viewportSize()!.width > 640) {
      expect(
        Math.abs(
          tabBox!.y + tabBox!.height / 2 - actionBox!.y - actionBox!.height / 2,
        ),
      ).toBeLessThan(8);
    }
  }
  await page.goto("/app/opensync/engines?view=local");
  await expect(
    page
      .locator(".page-toolbar")
      .getByRole("button", { name: "授权目录", exact: true }),
  ).toBeVisible();
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
  // 五个设置项合并成两张分组卡片，条目之间用分隔线而不是各自成卡。
  await expect(page.locator(".settings-card")).toHaveCount(2);
  await expect(page.locator(".settings-card").first()).toHaveCSS(
    "border-top-width",
    "1px",
  );
  await expect(page.locator(".settings-card-body .setting-row")).toHaveCount(5);
  await expect(page.locator(".settings-card .settings-card")).toHaveCount(0);

  const input = page.getByRole("textbox", { name: "复制并发数", exact: true });
  await input.hover();
  const wrapper = input.locator(
    "xpath=ancestor::*[contains(@class, 'semi-input-wrapper')][1]",
  );
  await expect(wrapper).toHaveCSS("background-color", "rgb(255, 255, 255)");
  await expect(wrapper).toHaveCSS("border-top-color", "rgb(0, 102, 255)");

  await input.fill("9");
  const save = page.getByRole("button", { name: "保存设置", exact: true });
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
    await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
    await expect(page.locator(".engine-item .item-symbol .semi-icon")).toHaveCSS(
      "font-size",
      "16px",
    );

    await page.goto(`/app/opensync/tasks?jobId=${jobId}`);
    await expect(
      page.locator(".app-sidebar").getByRole("link", { name, exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name, exact: true }),
    ).toBeVisible();

    await page.goto("/app/opensync/notifications");
    await expect(page.getByText(`通知 #${notifyId}`, { exact: true })).toBeVisible();
    await expect(
      page.locator(".notification-item .semi-switch").first(),
    ).toHaveCSS("width", "40px");
    await expect(
      page.locator(".notification-item .semi-switch").first(),
    ).toHaveCSS("height", "24px");
    await expect(
      page.locator(".notification-item .semi-switch").first(),
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
    const summary = page.locator(".task-summary");
    const detail = page.locator(".task-detail-pane");
    await expect(summary).toBeVisible();
    await expect(summary).toHaveCSS("background-color", "rgb(255, 255, 255)");
    await expect(summary).toHaveCSS("border-radius", "8px");
    await expect(summary).toHaveCSS("border-top-width", "1px");
    const summaryBox = await summary.boundingBox();
    const detailBox = await detail.boundingBox();
    expect(summaryBox!.width).toBeGreaterThan(detailBox!.width - 12);
  } finally {
    await request.delete(`/app/opensync/svr/job?id=${jobId}`);
    await request.delete(`/app/opensync/svr/alist?id=${engineId}`);
  }
});

test("empty pages render the reference folder asset", async ({ page }) => {
  for (const path of ["tasks", "engines", "notifications"]) {
    await page.goto(`/app/opensync/${path}`);
    const empty = page.getByRole("status", { name: "空空如也", exact: true });
    await expect(empty).toBeVisible();
    expect(
      await empty
        .locator("img")
        .evaluate(
          (img: HTMLImageElement) => img.complete && img.naturalWidth > 0,
        ),
    ).toBe(true);
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
  expect(bounds!.width).toBe(
    page.viewportSize()!.width <= 640 ? page.viewportSize()!.width : 454,
  );
  expect(bounds!.x).toBeGreaterThanOrEqual(0);
  expect(bounds!.y).toBeGreaterThanOrEqual(0);
  expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(
    page.viewportSize()!.height + 1,
  );
  await expect(
    dialog.getByRole("button", { name: "保存", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: `test-results/dialog-${test.info().project.name}.png`,
  });
});

test("dirty-confirm dialog title is aligned to the top left", async ({
  page,
}) => {
  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "添加引擎", exact: true }).click();
  await page.getByRole("textbox", { name: "名称", exact: true }).fill("dirty");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  const dialog = page
    .getByRole("dialog")
    .filter({ hasText: "放弃未保存的修改？" });
  await expect(dialog).toBeVisible();
  const title = dialog.getByText("放弃未保存的修改？", { exact: true });
  await expect(title).toBeVisible();
  const dialogBox = await dialog.boundingBox();
  const titleBox = await title.boundingBox();
  expect(titleBox!.x - dialogBox!.x).toBeLessThan(34);
  expect(titleBox!.y - dialogBox!.y).toBeLessThan(34);
});

test("settings save stays at the top right", async ({ page }) => {
  await page.goto("/app/opensync/settings");
  const save = page
    .locator(".page-toolbar")
    .getByRole("button", { name: "保存设置", exact: true });
  await expect(save).toBeVisible();
  await expect(page.getByRole("textbox", { name: "复制并发数", exact: true })).toBeVisible();
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
  await expect(page.locator(".page-toolbar")).toHaveClass(/no-tabs/);
  await page.goto("/app/opensync/engines?view=engine");
  const toolbar = page.locator(".page-toolbar");
  await expect(toolbar.locator(".page-tabs .semi-tabs-tab").first()).toBeVisible();
  await expect(toolbar).not.toHaveClass(/no-tabs/);
  await expect(toolbar).toHaveCSS("border-bottom-width", "1px");
});
