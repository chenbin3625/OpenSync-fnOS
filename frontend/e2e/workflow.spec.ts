import { test, expect } from "@playwright/test";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

// The backend rejects writes without Content-Type: application/json (CSRF
// guard). Playwright only sets it for requests with a JSON body, so bodiless
// DELETEs used for cleanup must declare it explicitly.
const jsonWrite = { headers: { "Content-Type": "application/json" } };

// Only the remote storage and webhook are substituted; app routes, SQLite and UI are real.
let engine: Server, engineUrl: string;
let webhookBody: unknown;
let webhookCalls = 0;
const webhookHeaders: (string | string[] | undefined)[] = [];
test.beforeAll(async () => {
  engine = createServer(async (request, response) => {
    response.setHeader("Content-Type", "application/json");
    if (request.url === "/hook") {
      let body = "";
      for await (const chunk of request) body += chunk;
      webhookBody = JSON.parse(body);
      webhookHeaders.push(request.headers["x-fixture"]);
      webhookCalls++;
      response.end("{}");
      return;
    }
    if (request.method === "PROPFIND") {
      const path = new URL(request.url || "/", "http://localhost").pathname;
      const dirs = path === "/" && request.headers.depth === "1" ? ["/", "/Photos/", "/Backup/"] : [path];
      response.writeHead(207, { "Content-Type": "application/xml" });
      response.end(`<D:multistatus xmlns:D="DAV:">${dirs.map((dir) => `<D:response><D:href>${dir}</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`).join("")}</D:multistatus>`);
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
    await page.getByRole("combobox", { name: "通知渠道", exact: true }).click();
    await page.getByRole("option").filter({ hasText: "自定义 Webhook" }).click();
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
    expect(webhookHeaders.at(-1)).toBe("test-secret");
    expect(webhookBody).toHaveProperty("subject");
    expect(JSON.stringify(webhookBody)).not.toContain("{title}");
    // The id is read from the create response rather than the tail of the list:
    // the finally block deletes it, and "last item" would be someone else's
    // real notification config whenever the list is not insertion-ordered.
    const [createResponse] = await Promise.all([
      // The path has to match exactly: this test sends a test notification
      // first, and POST /svr/notify/test also contains "/svr/notify" while
      // returning a null data payload.
      page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === "/app/opensync/svr/notify" &&
          response.request().method() === "POST",
      ),
      page.getByRole("button", { name: "保存", exact: true }).click(),
    ]);
    await expect(page.getByRole("dialog")).not.toBeVisible();
    id = (await createResponse.json()).data.id;
    expect(id).toBeTruthy();
    const card = page.locator(`.notification-item[data-notify-id="${id}"]`);
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
    expect(webhookHeaders.slice(initialCalls)).toEqual([
      "test-secret",
      "test-secret",
      "test-secret",
    ]);
  } finally {
    if (id)
      await request.delete(`/app/opensync/svr/notify?notifyId=${id}`, jsonWrite);
  }
});
test.afterAll(async () => {
  await new Promise<void>((resolve) => engine.close(() => resolve()));
});

test("creates a WebDAV storage and continuous task, then edits without changing sync mode", async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(90000);
  page.setDefaultTimeout(8000);
  const name = "合同测试-" + testInfo.project.name + "-" + Date.now();
  let jobId: number | undefined;
  let storageCreated = false;
  try {
    await page.goto("/app/opensync/engines");
    await page.getByRole("button", { name: "添加远程存储", exact: true }).click();
    await page.getByRole("button", { name: "OpenList / AList (WebDAV)", exact: true }).click();
    await page.getByRole("textbox", { name: "存储名称", exact: true }).fill(name);
    await page.getByRole("textbox", { name: "WebDAV 地址", exact: true }).fill(engineUrl);
    await page.getByRole("button", { name: "保存存储", exact: true }).click();
    await expect(page.getByRole("dialog")).not.toBeVisible();
    storageCreated = true;
    await expect(
      page.locator(".storage-card").filter({ hasText: name }).getByRole("heading"),
    ).toBeVisible();
    await page.goto("/app/opensync/tasks");
    await page.getByRole("button", { name: "新建任务", exact: true }).click();
    await page.getByRole("button", { name: "高级设置", exact: true }).click();
    const dialog = page.getByRole("dialog");
    const manualPath = dialog.getByPlaceholder("/存储名称/存储桶/子目录");
    await manualPath.fill(`/${name}/Photos`);
    await dialog.getByRole("button", { name: "添加到源路径", exact: true }).click();
    await manualPath.fill(`/${name}/Backup`);
    await dialog.getByRole("button", { name: "添加到目标路径", exact: true }).click();
    await page
      .getByRole("textbox", { name: "任务名称", exact: true })
      .fill(name);
    await page.getByRole("button", { name: "下一步", exact: true }).click();
    await expect(dialog.locator(".schedule-preview-card")).toBeVisible();
    // 第 2 步可直接保存，不必强制经过可选的「过滤规则」步
    const posted = page.waitForRequest(
      (r) => r.url().endsWith("/svr/job") && r.method() === "POST",
    );
    await page
      .getByRole("button", { name: "保存", exact: true })
      .click();
    expect((await posted).postDataJSON()).toMatchObject({
      isCron: 3,
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
    expect(job.isCron).toBe(3);
    expect(job.enable).toBe(1);
    expect(job.srcPath).toContain("/Photos");
    expect(job.dstPath).toContain("/Backup");
    // 创建后自动定位到新建任务
    await expect(page).toHaveURL(new RegExp(`jobId=${jobId}`));
    const forceMobileClick = testInfo.project.name.includes("mobile");
    await page
      .locator(".overview-card")
      .getByRole("button", { name: "编辑任务", exact: true })
      .click({ force: forceMobileClick });
    await expect(
      page.getByRole("tab", { name: "过滤规则", exact: true }),
    ).toBeVisible();
    await page.getByRole("tab", { name: "过滤规则", exact: true }).click();
    await expect(
      page.getByRole("textbox", { name: "高级排除规则", exact: true }),
    ).toHaveCount(0);
    await page.getByRole("tab", { name: "基础与路径", exact: true }).click();
    await page
      .getByRole("textbox", { name: "任务名称", exact: true })
      .fill(name + "-修改");
    const saveButton = page.getByRole("button", {
      name: "保存",
      exact: true,
    });
    await expect(saveButton).toBeVisible();
    await saveButton.click({ force: forceMobileClick });
    await expect(page.getByRole("dialog")).not.toBeVisible();
    if (process.env.PLAYWRIGHT_SCREENSHOTS === "1")
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
    ).toMatchObject({ method: 0, isCron: 3, remark: name + "-修改" });
  } finally {
    if (!jobId) {
      const js = await (
        await request.get("/app/opensync/svr/job?pageNum=1&pageSize=100")
      ).json();
      jobId = js.data.dataList.find((j: { remark: string }) =>
        j.remark?.startsWith(name),
      )?.id;
    }
    if (jobId) await request.delete(`/app/opensync/svr/job?id=${jobId}`, jsonWrite);
    if (storageCreated) await request.delete(`/app/opensync/svr/native/storage?name=${encodeURIComponent(name)}`, jsonWrite);
  }
});
