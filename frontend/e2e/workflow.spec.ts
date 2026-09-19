import { test, expect } from "@playwright/test";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

// Only the external engine is substituted; app routes, SQLite and UI are real.
let engine: Server, engineUrl: string;
let webhookBody: unknown;
let webhookCalls = 0;
test.beforeAll(async () => {
  engine = createServer(async (request, response) => {
    response.setHeader("Content-Type", "application/json");
    if (request.url === "/hook") {
      let body = "";
      for await (const chunk of request) body += chunk;
      webhookBody = JSON.parse(body);
      expect(request.headers["x-fixture"]).toBe("test-secret");
      webhookCalls++;
      response.end("{}");
      return;
    }
    if (request.url === "/api/me") {
      response.end(
        JSON.stringify({
          code: 200,
          message: "ok",
          data: { username: "contract-engine" },
        }),
      );
      return;
    }
    if (request.url === "/api/fs/list") {
      let body = "";
      for await (const chunk of request) body += chunk;
      const content =
        JSON.parse(body).path === "/"
          ? [
              { name: "Photos", is_dir: true },
              { name: "Backup", is_dir: true },
            ]
          : [];
      response.end(
        JSON.stringify({
          code: 200,
          message: "ok",
          data: { content, total: content.length },
        }),
      );
      return;
    }
    response.end(JSON.stringify({ code: 200, message: "ok", data: {} }));
  });
  await new Promise<void>((resolve) => engine.listen(0, "127.0.0.1", resolve));
  engineUrl = `http://127.0.0.1:${(engine.address() as AddressInfo).port}`;
});

test("sends a webhook template and preserves masked credentials when editing", async ({
  page,
  request,
}) => {
  let id: number | undefined;
  const initialCalls = webhookCalls;
  try {
    await page.goto("/app/opensync/notifications");
    await page.getByRole("button", { name: "添加通知", exact: true }).click();
    await page
      .getByRole("textbox", { name: "Webhook URL", exact: true })
      .fill(engineUrl + "/hook");
    await page
      .getByRole("textbox", { name: "请求体 JSON", exact: true })
      .fill('{"subject":"{title}","message":"{content}"}');
    await page
      .getByRole("textbox", { name: "请求头 JSON", exact: true })
      .fill('{"X-Fixture":"test-secret"}');
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "发送测试通知", exact: true })
      .click();
    await expect.poll(() => webhookCalls).toBe(initialCalls + 1);
    expect(webhookBody).toHaveProperty("subject");
    expect(JSON.stringify(webhookBody)).not.toContain("{title}");
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect(page.getByRole("dialog")).not.toBeVisible();
    const result = await (await request.get("/app/opensync/svr/notify")).json();
    id = result.data.at(-1).id;
    const card = page
      .locator(".notification-item")
      .filter({ hasText: `通知 #${id}` });
    await card
      .getByRole("button", { name: "编辑通知", exact: true })
      .click();
    await page
      .getByRole("switch", { name: "无变更时不发送", exact: true })
      .check();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "发送测试通知", exact: true })
      .click();
    await expect.poll(() => webhookCalls).toBe(initialCalls + 2);
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect(page.getByRole("dialog")).not.toBeVisible();
    await card
      .getByRole("button", { name: "发送测试通知", exact: true })
      .click();
    await expect.poll(() => webhookCalls).toBe(initialCalls + 3);
  } finally {
    if (id)
      await request.delete(`/app/opensync/svr/notify?notifyId=${id}`);
  }
});
test.afterAll(async () => {
  await new Promise<void>((resolve) => engine.close(() => resolve()));
});

test("creates an engine and manual job, then edits without changing sync mode", async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(90000);
  const name = "合同测试-" + testInfo.project.name + "-" + Date.now();
  let engineId: number | undefined, jobId: number | undefined;
  try {
    await page.goto("/app/opensync/engines");
    await page.getByRole("button", { name: "添加引擎", exact: true }).click();
    await page
      .getByRole("textbox", { name: "引擎地址", exact: true })
      .fill(engineUrl);
    await page
      .getByRole("textbox", { name: "API Token", exact: true })
      .fill("test-fixture-token");
    await page.getByRole("textbox", { name: "名称", exact: true }).fill(name);
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await expect(page.getByRole("dialog")).not.toBeVisible();
    await expect(
      page.getByRole("heading", { name, exact: true }),
    ).toBeVisible();
    const engines = await (await request.get("/app/opensync/svr/alist")).json();
    engineId = engines.data.find(
      (e: { url: string }) => e.url === engineUrl,
    ).id;
    await page.goto("/app/opensync/tasks");
    await page.getByRole("button", { name: "新建任务", exact: true }).click();
    await page.getByRole("combobox", { name: "存储引擎", exact: true }).click();
    await page.getByRole("option").filter({ hasText: name }).click();
    const dialog = page.getByRole("dialog");
    const trees = dialog.locator(".remote-paths");
    await trees.nth(0).locator('[role="combobox"]').click();
    const sourceRoot = page.locator('.semi-tree-option[data-key="/"]').first();
    if ((await sourceRoot.getAttribute("aria-expanded")) !== "true")
      await sourceRoot.locator(".semi-tree-option-expand-icon").click();
    await page
      .locator(".semi-tree-option")
      .filter({ hasText: /^Photos$/ })
      .click();
    await page
      .getByRole("heading", { name: "新建任务", exact: true })
      .click();
    const sourcePathBox = await trees.nth(0).boundingBox();
    const sourceCacheBox = await dialog
      .getByText("源端缓存", { exact: true })
      .boundingBox();
    expect(sourceCacheBox!.y).toBeGreaterThan(sourcePathBox!.y);
    expect(sourceCacheBox!.y).toBeLessThan(sourcePathBox!.y + 120);
    await trees.nth(1).locator('[role="combobox"]').click();
    const targetRoot = page.locator('.semi-tree-option[data-key="/"]').first();
    if ((await targetRoot.getAttribute("aria-expanded")) !== "true")
      await targetRoot.locator(".semi-tree-option-expand-icon").click();
    await page
      .locator(".semi-tree-option")
      .filter({ hasText: /^Backup$/ })
      .click();
    await page
      .getByRole("heading", { name: "新建任务", exact: true })
      .click();
    const targetPathBox = await trees.nth(1).boundingBox();
    const targetCacheBox = await dialog
      .getByText("目标缓存", { exact: true })
      .boundingBox();
    expect(targetCacheBox!.y).toBeGreaterThan(targetPathBox!.y);
    expect(targetCacheBox!.y).toBeLessThan(targetPathBox!.y + 120);
    await page
      .getByRole("textbox", { name: "任务名称", exact: true })
      .fill(name);
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    await page.getByRole("combobox", { name: "调度方式", exact: true }).click();
    await page.getByRole("option").filter({ hasText: "仅手动" }).click();
    await expect(
      page.getByRole("combobox", { name: "调度方式", exact: true }),
    ).toHaveText("仅手动");
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    const posted = page.waitForRequest(
      (r) => r.url().endsWith("/svr/job") && r.method() === "POST",
    );
    await page
      .getByRole("button", { name: "保存任务配置", exact: true })
      .click();
    expect((await posted).postDataJSON()).toMatchObject({
      isCron: 2,
      method: 0,
    });
    await expect(page.getByRole("dialog")).not.toBeVisible();
    const jobs = await (
      await request.get("/app/opensync/svr/job?pageNum=1&pageSize=100")
    ).json();
    const job = jobs.data.dataList.find(
      (j: { remark: string }) => j.remark === name,
    );
    jobId = job.id;
    expect(job.method).toBe(0);
    expect(job.isCron).toBe(2);
    expect(job.enable).toBe(1);
    expect(job.srcPath).toContain("/Photos");
    expect(job.dstPath).toContain("/Backup");
    const forceMobileClick = testInfo.project.name.includes("mobile");
    await page.goto(`/app/opensync/tasks?jobId=${jobId}`);
    await page
      .locator(".overview-card")
      .getByRole("button", { name: "更多操作", exact: true })
      .click({ force: forceMobileClick });
    const editButton = page
      .locator(".semi-dropdown-menu")
      .getByText("设置", { exact: true });
    await expect(editButton).toBeVisible();
    await editButton.click({ force: forceMobileClick });
    await page
      .getByRole("textbox", { name: "任务名称", exact: true })
      .fill(name + "-修改");
    await page
      .getByRole("button", { name: "下一步", exact: true })
      .click({ force: forceMobileClick });
    await page
      .getByRole("button", { name: "下一步", exact: true })
      .click({ force: forceMobileClick });
    const saveButton = page.getByRole("button", {
      name: "保存任务配置",
      exact: true,
    });
    await expect(saveButton).toBeVisible();
    await saveButton.click({ force: forceMobileClick });
    await expect(page.getByRole("dialog")).not.toBeVisible();
    await page.screenshot({
      path: `test-results/workspace-${testInfo.project.name}.png`,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    const updated = await (
      await request.get("/app/opensync/svr/job?pageNum=1&pageSize=100")
    ).json();
    expect(
      updated.data.dataList.find((j: { id: number }) => j.id === jobId),
    ).toMatchObject({ method: 0, isCron: 2, remark: name + "-修改" });
  } finally {
    if (!engineId) {
      const es = await (await request.get("/app/opensync/svr/alist")).json();
      engineId = es.data.find((e: { url: string }) => e.url === engineUrl)?.id;
    }
    if (!jobId) {
      const js = await (
        await request.get("/app/opensync/svr/job?pageNum=1&pageSize=100")
      ).json();
      jobId = js.data.dataList.find((j: { remark: string }) =>
        j.remark?.startsWith(name),
      )?.id;
    }
    if (jobId) await request.delete(`/app/opensync/svr/job?id=${jobId}`);
    if (engineId) await request.delete(`/app/opensync/svr/alist?id=${engineId}`);
  }
});
