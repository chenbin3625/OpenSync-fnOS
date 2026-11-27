import { expect, test } from "@playwright/test";

test("standard 3-step wizard hides /本地 & ~12hex, disambiguates duplicate local roots, supports remote mkdir, inline crypt, delete modes & weekly schedule", async ({
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
  const createdFolders = new Set<string>();
  const mkdirRequests: string[] = [];
  const captured: {
    storage?: Record<string, unknown>;
    job?: Record<string, unknown>;
  } = {};

  await page.route("**/app/opensync/svr/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });

    if (url.pathname.endsWith("/session")) {
      return success({ uid: 1, development: false, version: "test" });
    }
    if (url.pathname.endsWith("/version/latest")) {
      return success({
        latestVersion: "v0.0.0",
        releaseURL: "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest",
        hasUpdate: false,
      });
    }
    if (url.pathname.endsWith("/native/storage") && req.method() === "GET") {
      return success({
        engineId: 1,
        enabled: true,
        rcloneAvailable: true,
        remark: "内置存储",
        localRoots: [
          { id: "Photos~a1b2c3d4e5f6", path: "/vol1/1000/Photos" },
          { id: "Photos~fedcba987654", path: "/vol2/1000/Photos" },
          { id: "Documents~112233445566", path: "/vol1/1000/Documents" },
        ],
        remotes,
      });
    }
    if (url.pathname.endsWith("/native/storage") && req.method() === "POST") {
      const body = req.postDataJSON() as Record<string, unknown>;
      captured.storage = body;
      const opts = (body.options || {}) as Record<string, string>;
      remotes.push({
        name: String(body.name || ""),
        type: opts.type || "crypt",
        summary: `底层 ${opts.remote || ""}`,
        options: opts,
        configuredSecrets: ["password"],
        usedByJobs: [],
        usedByCrypts: [],
      });
      return success(null);
    }
    if (url.pathname.endsWith("/native/storage/mkdir") && req.method() === "POST") {
      const body = req.postDataJSON() as { path?: string };
      const targetPath = String(body.path || "");
      mkdirRequests.push(targetPath);
      const lastSlash = targetPath.lastIndexOf("/");
      const parent = targetPath.slice(0, lastSlash);
      const name = targetPath.slice(lastSlash + 1);
      if (parent === "/aliyun") {
        createdFolders.add(name);
      }
      return success(null);
    }
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) {
      const path = url.searchParams.get("path") || "/";
      if (path === "/本地/Photos~a1b2c3d4e5f6") {
        return success([{ path: "2026" }]);
      }
      if (path === "/aliyun") {
        return success([
          { path: "Backup" },
          ...Array.from(createdFolders).map((name) => ({ path: name })),
        ]);
      }
      return success([]);
    }
    if (url.pathname.endsWith("/alist")) {
      return success([
        { id: 1, remark: "内置存储", url: "native://rclone", userName: "" },
      ]);
    }
    if (url.pathname.endsWith("/system/config")) {
      return success({});
    }
    if (url.pathname.endsWith("/notify")) {
      return success([]);
    }
    if (url.pathname.endsWith("/job") && req.method() === "POST") {
      captured.job = req.postDataJSON() as Record<string, unknown>;
      return success({ id: 77 });
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: [], count: 0 });
    }
    return success(null);
  });

  await page.goto("/app/opensync/sync");
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();

  // Step 1: Verify NAS picker hides /本地 and ~12hex fingerprints, and only disambiguates duplicates
  const nasSection = dialog.getByRole("region", { name: "NAS 本地文件夹" });
  await expect(nasSection).not.toContainText("/本地");
  await expect(nasSection).not.toContainText("~a1b2c3d4e5f6");
  await expect(nasSection).not.toContainText("~fedcba987654");
  await expect(nasSection).not.toContainText("~112233445566");

  await expect(
    nasSection.getByRole("button", { name: "Documents", exact: true }),
  ).toBeVisible();
  await expect(
    nasSection.getByRole("button", { name: "Photos (vol1)", exact: true }),
  ).toBeVisible();
  await expect(
    nasSection.getByRole("button", { name: "Photos (vol2)", exact: true }),
  ).toBeVisible();

  await nasSection.getByRole("button", { name: "展开 Photos (vol1)" }).click();
  await nasSection.getByRole("button", { name: "2026", exact: true }).click();
  await expect(nasSection.locator(".wizard-picker-selected")).toContainText(
    "Photos (vol1)/2026",
  );
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);

  // Cloud picker: create a new folder inline on aliyun
  const cloudSection = dialog.getByRole("region", { name: "云端存储文件夹" });
  await cloudSection.getByRole("button", { name: "新建文件夹", exact: true }).click();
  await cloudSection
    .getByRole("textbox", { name: "新建云端文件夹名称" })
    .fill("Vault");
  await cloudSection.getByRole("button", { name: "创建", exact: true }).click();
  await expect(cloudSection.locator(".wizard-picker-selected")).toContainText(
    "/aliyun/Vault",
  );
  expect(mkdirRequests).toEqual(["/aliyun/Vault"]);

  await dialog.getByRole("button", { name: "下一步", exact: true }).click();

  // Step 2: 3 direction cards + 3 one-way delete modes + inline E2E encryption + recycle bin
  const dirGroup = dialog.getByRole("radiogroup", { name: "同步方向" });
  await expect(dirGroup.getByRole("radio")).toHaveCount(3);
  await dirGroup.getByRole("radio", { name: /^仅上传本地更改/ }).click();

  const deleteGroup = dialog.getByRole("radiogroup", { name: "单向删除策略" });
  await expect(deleteGroup.getByRole("radio")).toHaveCount(3);
  await deleteGroup
    .getByRole("radio", { name: /^仅同步本任务产生的删除/ })
    .click();

  // Enable inline E2E encryption (no modal!)
  await dialog.getByRole("switch", { name: "目标端启用端到端加密" }).click();
  await expect(page.locator(".quick-crypt-modal")).toHaveCount(0);
  await dialog.getByRole("textbox", { name: "加密密码", exact: true }).fill("VaultPass#2026");
  await dialog.getByRole("textbox", { name: "确认密码", exact: true }).fill("WrongPass");
  await dialog
    .getByRole("button", { name: /密码盐与文件名加密设置/ })
    .click();
  await dialog.getByRole("textbox", { name: /密码盐/ }).fill("Salt#2026");

  // Password mismatch blocks advancing cleanly with inline field error
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog.getByRole("alert").filter({ hasText: "不一致" })).toBeVisible();

  await dialog.getByRole("textbox", { name: "确认密码", exact: true }).fill("VaultPass#2026");
  await dialog.getByRole("switch", { name: "启用删除回收站保护" }).click();
  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);

  // Step 3 (Optional): Weekly schedule grid + collapsed rclone expert drawer + 4 presets
  await dialog.getByRole("button", { name: "同步计划与过滤", exact: true }).click();

  // Expert rclone drawer must be collapsed by default
  const expertToggle = dialog.getByRole("button", {
    name: /高级引擎调优（rclone 专家选项）/,
  });
  await expect(expertToggle).toHaveAttribute("aria-expanded", "false");
  await expect(dialog.getByText("快速列表（--fast-list）")).toHaveCount(0);
  // 4 scenario presets remain accessible
  await expect(dialog.getByRole("button", { name: "相册 / 影音大文件" })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "海量小文件" })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "国内网盘防限流" })).toBeVisible();
  await expect(dialog.getByRole("button", { name: "冷备归档" })).toBeVisible();

  // Switch to weekly schedule mode and apply template + paint cell + set limitRate + stopOutside
  await dialog
    .getByRole("radio", { name: /^按周计划调度/ })
    .click();
  await dialog
    .getByRole("button", { name: "工作日白天限速+夜间全速", exact: true })
    .click();
  const monday2am = dialog.locator(
    '[role="gridcell"][data-day="1"][data-hour="2"]',
  );
  await expect(monday2am).toHaveAttribute("data-mode", "full");
  await monday2am.click();
  await expect(monday2am).toHaveAttribute("data-mode", "limited");
  await dialog.getByRole("textbox", { name: "限速时段速率" }).fill("8M");
  await dialog
    .getByRole("switch", { name: "暂停时段立即中断正在传输的文件" })
    .click();

  expect(await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toBeHidden();

  // Verify inline crypt creation sent managed_by_job: "true" and job destination was rewritten
  expect(captured.storage).toEqual({
    name: "aliyun-Vault-crypt",
    options: {
      type: "crypt",
      remote: "aliyun:Vault",
      password: "VaultPass#2026",
      password2: "Salt#2026",
      managed_by_job: "true",
      managed_job_label: "2026 · aliyun",
    },
  });

  expect(captured.job).toMatchObject({
    srcPath: ["/本地/Photos~a1b2c3d4e5f6/2026"],
    dstPath: ["/aliyun-Vault-crypt"],
    method: 0,
    isCron: 3,
    trashEnable: 1,
  });

  const extraFlags = JSON.parse(String(captured.job?.extraFlags));
  expect(extraFlags.syncDirectionMode).toBe("upload");
  expect(extraFlags.oneWayDeleteMode).toBe("tracked");
  expect(extraFlags.weeklySchedule).toMatchObject({
    enabled: true,
    limitRate: "8M",
    stopOutside: true,
  });
  expect(extraFlags.weeklySchedule.days[1][2]).toBe("limited");
  expect(extraFlags.weeklySchedule.days[1][9]).toBe("limited");
  expect(extraFlags.weeklySchedule.days[0][12]).toBe("full");
});

test("supports download endpoint reversal, inline crypt reuse, bidirectional sync and weekly drag-painting", async ({
  page,
}) => {
  const captured: { job?: Record<string, unknown> } = {};
  let cryptCreatedCount = 0;

  await page.route("**/app/opensync/svr/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const success = (data: unknown) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({ code: 200, data, msg: "" }),
      });

    if (url.pathname.endsWith("/session")) {
      return success({ uid: 1, development: false, version: "test" });
    }
    if (url.pathname.endsWith("/version/latest")) {
      return success({ latestVersion: "v0.0.0", releaseURL: "", hasUpdate: false });
    }
    if (url.pathname.endsWith("/native/storage") && req.method() === "GET") {
      return success({
        engineId: 1,
        enabled: true,
        rcloneAvailable: true,
        remark: "内置存储",
        localRoots: [{ id: "Work~1234567890ab", path: "/vol1/Work" }],
        remotes: [
          {
            name: "od",
            type: "onedrive",
            summary: "OneDrive",
            options: {},
            configuredSecrets: ["token"],
            usedByJobs: [],
            usedByCrypts: ["od-sync-crypt"],
          },
          {
            name: "od-sync-crypt",
            type: "crypt",
            summary: "底层 od:Sync",
            options: { remote: "od:Sync" },
            configuredSecrets: ["password"],
            usedByJobs: [],
            usedByCrypts: [],
          },
        ],
      });
    }
    if (url.pathname.endsWith("/native/storage") && req.method() === "POST") {
      cryptCreatedCount += 1;
      return success(null);
    }
    if (url.pathname.endsWith("/alist") && url.searchParams.has("alistId")) {
      const path = url.searchParams.get("path") || "/";
      if (path === "/od") return success([{ path: "Sync" }]);
      return success([]);
    }
    if (url.pathname.endsWith("/alist")) {
      return success([
        { id: 1, remark: "内置存储", url: "native://rclone", userName: "" },
      ]);
    }
    if (url.pathname.endsWith("/system/config")) {
      return success({});
    }
    if (url.pathname.endsWith("/notify")) {
      return success([]);
    }
    if (url.pathname.endsWith("/job") && req.method() === "POST") {
      captured.job = req.postDataJSON() as Record<string, unknown>;
      return success({ id: 88 });
    }
    if (url.pathname.endsWith("/job")) {
      return success({ dataList: [], count: 0 });
    }
    return success(null);
  });

  await page.goto("/app/opensync/sync");
  await page.getByRole("button", { name: "新建同步任务", exact: true }).click();
  const dialog = page.getByRole("dialog");

  await dialog.locator('[data-virtual-path="/本地/Work~1234567890ab"]').click();
  const cloudTree = dialog.getByRole("tree", { name: "云端存储连接列表" });
  await cloudTree.getByRole("button", { name: "展开 od", exact: true }).click();
  await cloudTree.getByRole("button", { name: "Sync", exact: true }).click();
  await dialog.getByRole("button", { name: "下一步", exact: true }).click();

  // Test download direction hides one-way delete vs shows download banner
  await dialog.getByRole("radio", { name: /^仅下载云端更改/ }).click();
  await expect(dialog.getByText("目标端为 NAS 本地目录")).toBeVisible();

  // Switch to bidirectional sync -> hides one-way delete policy radios and offers reusable crypt
  await dialog.getByRole("radio", { name: /^双向同步/ }).click();
  await expect(
    dialog.getByRole("radiogroup", { name: "单向删除策略" }),
  ).toHaveCount(0);

  await dialog.getByRole("switch", { name: "目标端启用端到端加密" }).click();
  await dialog
    .getByRole("radio", { name: /复用已有加密存储/ })
    .click();
  await expect(dialog.locator(".inline-crypt-preview")).toContainText(
    "/od-sync-crypt",
  );

  // Advance to Step 3 and test brush painting on the weekly schedule grid
  await dialog.getByRole("button", { name: "同步计划与过滤", exact: true }).click();
  await dialog.getByRole("radio", { name: /^按周计划调度/ }).click();
  await dialog.getByRole("button", { name: /涂抹：暂停/ }).click();
  const cell0 = dialog.locator('[role="gridcell"][data-day="2"][data-hour="10"]');
  const cell1 = dialog.locator('[role="gridcell"][data-day="2"][data-hour="11"]');
  await cell0.scrollIntoViewIfNeeded();
  await cell1.scrollIntoViewIfNeeded();
  const box0 = await cell0.boundingBox();
  const box1 = await cell1.boundingBox();
  if (box0 && box1) {
    await page.mouse.move(box0.x + box0.width / 2, box0.y + box0.height / 2);
    await page.mouse.down();
    await page.mouse.move(box1.x + box1.width / 2, box1.y + box1.height / 2);
    await page.mouse.up();
  }
  await expect(cell0).toHaveAttribute("data-mode", "pause");
  await expect(cell1).toHaveAttribute("data-mode", "pause");

  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await expect(dialog).toBeHidden();

  // Reuse should not POST a new storage
  expect(cryptCreatedCount).toBe(0);
  expect(captured.job).toMatchObject({
    srcPath: ["/本地/Work~1234567890ab"],
    dstPath: ["/od-sync-crypt"],
    method: 3,
    isCron: 3,
  });
  const flags = JSON.parse(String(captured.job?.extraFlags));
  expect(flags.syncDirectionMode).toBe("bidirectional");
  expect(flags.oneWayDeleteMode).toBeUndefined();
  expect(flags.weeklySchedule.days[2][10]).toBe("pause");
  expect(flags.weeklySchedule.days[2][11]).toBe("pause");
});
