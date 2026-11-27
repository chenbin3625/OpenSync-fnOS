import { test, expect } from "@playwright/test";
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

const jsonWrite = { headers: { "Content-Type": "application/json" } };

let openListServer: Server;
let openListPort: number;

test.beforeAll(async () => {
  openListServer = createServer((req, res) => {
    if (req.url?.startsWith("/dav") && req.method === "PROPFIND") {
      res.writeHead(207, {
        "Content-Type": "application/xml",
        Server: "OpenList/v4.0",
      });
      res.end(
        '<D:multistatus xmlns:D="DAV:"><D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>',
      );
      return;
    }
    res.writeHead(404);
    res.end();
  });
  await new Promise<void>((resolve) =>
    openListServer.listen(0, "127.0.0.1", resolve),
  );
  openListPort = (openListServer.address() as AddressInfo).port;
});

test.afterAll(async () => {
  await new Promise<void>((resolve) => openListServer.close(() => resolve()));
});

test("two-level storage cards, OpenList local discovery + SSRF block, OAuth helper, and job-managed crypt recovery", async ({
  page,
  request,
}) => {
  const davRemoteName = `e2e-openlist-${Date.now()}`;
  const cryptRemoteName = `e2e-crypt-${Date.now()}`;

  try {
    // 1. SSRF protection check directly against real backend
    const ssrfResp = await request.post("/app/opensync/svr/native/storage/discover-openlist", {
      ...jsonWrite,
      data: { endpoints: ["http://192.168.1.88:5244/dav"] },
    });
    const ssrfJson = (await ssrfResp.json()) as { code: number; msg: string };
    expect(ssrfJson.code).not.toBe(200);
    expect(ssrfJson.msg).toContain("本机回环地址");

    // 2. Same-origin /app/opensync/oauth/callback page extraction
    await page.goto(
      "/app/opensync/oauth/callback?code=e2e_auth_code&state=e2e_state_1",
    );
    await expect(page.locator("#statusBadge")).toHaveText("已提取授权码");
    await expect(page.locator("#valCode")).toHaveText("e2e_auth_code");
    await expect(page.locator("#valState")).toHaveText("e2e_state_1");
    await expect(
      page.getByRole("button", { name: "一键复制回调参数" }),
    ).toBeEnabled();

    // 3. Open Storage page (/app/opensync/engines) -> two-level cards & real OpenList port auto-discovery
    await page.goto("/app/opensync/engines");
    await page.getByRole("button", { name: "添加远程存储", exact: true }).click();

    const dialog = page.getByRole("dialog", { name: "添加远程存储" });
    await expect(dialog).toBeVisible();
    await expect(
      dialog.getByRole("group", { name: "热门网盘与常用存储" }),
    ).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: "夸克网盘", exact: true }),
    ).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: "123云盘", exact: true }),
    ).toBeVisible();
    await expect(
      dialog.getByRole("button", { name: "OpenList / AList (WebDAV)", exact: true }),
    ).toBeVisible();

    // Collapsible 30+ full rclone protocols selector
    await expect(dialog.getByLabel("存储类型")).toBeHidden();
    await dialog
      .getByRole("button", { name: /更多 30\+ 协议/ })
      .click();
    await expect(dialog.getByLabel("存储类型")).toBeVisible();

    // Click domestic OpenList hint card (阿里云盘) and detect ephemeral loopback port
    await dialog.getByRole("button", { name: "阿里云盘", exact: true }).click();
    await dialog.getByLabel("OpenList 本机端口").fill(String(openListPort));
    await dialog
      .getByRole("region", { name: "OpenList / AList 同机自动发现" })
      .getByRole("button", { name: "探测本机端口" })
      .click();

    await expect(
      dialog.getByRole("button", { name: "一键填充配置" }),
    ).toBeVisible();
    await dialog.getByRole("button", { name: "一键填充配置" }).click();
    await expect(dialog.getByRole("textbox", { name: "WebDAV 地址" })).toHaveValue(
      `http://127.0.0.1:${openListPort}/dav`,
    );

    // Override name to unique E2E name & save real remote
    await dialog.getByRole("textbox", { name: "存储名称" }).fill(davRemoteName);
    await dialog.getByRole("button", { name: "保存存储", exact: true }).click();
    await expect(dialog).toBeHidden();

    // Verify OneDrive OAuth wizard supports fragment auto-recognition/submission cleanly
    await page.route("**/app/opensync/svr/native/storage/auth/start", (route) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          code: 200,
          data: {
            sessionId: "e2e-oauth-sess",
            type: "onedrive",
            status: "oauth",
            authUrl: "https://login.example.invalid/oauth2/authorize?state=e2e_st",
            redirectUri: "http://localhost:53682/",
          },
          msg: "",
        }),
      }),
    );
    await page.route("**/app/opensync/svr/native/storage/auth/callback", (route) =>
      route.fulfill({
        contentType: "application/json",
        body: JSON.stringify({
          code: 200,
          data: {
            sessionId: "e2e-oauth-sess",
            type: "onedrive",
            status: "completed",
            publicOptions: {},
            configuredSecrets: ["token"],
          },
          msg: "",
        }),
      }),
    );
    await page.getByRole("button", { name: "添加远程存储", exact: true }).click();
    const authDialog = page.getByRole("dialog", { name: "添加远程存储" });
    await authDialog.getByRole("button", { name: "OneDrive", exact: true }).click();
    await authDialog.getByRole("button", { name: "开始网页授权登录" }).click();
    await expect(
      authDialog.getByRole("region", { name: "OneDrive 网页授权向导" }),
    ).toBeVisible();
    await authDialog
      .getByRole("textbox", { name: "粘贴授权后的浏览器回调网址" })
      .fill("#code=e2e_fragment_code&state=e2e_st");
    await expect(authDialog).toContainText("已自动识别回调 URL 片段中的授权码");
    await authDialog.getByRole("button", { name: "完成授权", exact: true }).click();
    await expect(authDialog).toContainText("已完成 Microsoft OneDrive 图形化授权接入");

    // Switch seamlessly from add form to "导入 rclone.conf" view
    await authDialog.getByRole("button", { name: "导入 rclone.conf" }).click();
    await expect(
      page.getByRole("dialog", { name: "导入 rclone.conf" }),
    ).toBeVisible();
    await page
      .getByRole("dialog", { name: "导入 rclone.conf" })
      .getByRole("button", { name: "取消", exact: true })
      .click();

    // 4. Create job-managed crypt remote backed by the real WebDAV remote, verify collapse + recovery
    const createCryptResp = await request.post("/app/opensync/svr/native/storage", {
      ...jsonWrite,
      data: {
        name: cryptRemoteName,
        options: {
          type: "crypt",
          remote: `${davRemoteName}:vault`,
          password: "E2eStrongPassword!",
          managed_by_job: "true",
          managed_job_label: "E2E 隐私同步",
        },
      },
    });
    expect(((await createCryptResp.json()) as { code: number }).code).toBe(200);

    await page.reload();
    const collapsedRegion = page.getByRole("region", {
      name: "任务专属加密通道（可展开查看/管理）",
    });
    await expect(collapsedRegion).toBeVisible();
    const toggleBtn = collapsedRegion.getByRole("button", {
      name: /任务专属加密通道（可展开查看\/管理）/,
    });
    await expect(toggleBtn).toHaveAttribute("aria-expanded", "false");
    await expect(collapsedRegion.getByText(cryptRemoteName)).toBeHidden();

    // Expand collapsed group & open Crypt Recovery Modal
    await toggleBtn.click();
    await expect(toggleBtn).toHaveAttribute("aria-expanded", "true");
    await expect(collapsedRegion.getByText(cryptRemoteName)).toBeVisible();
    const cryptCard = collapsedRegion
      .locator(".storage-card")
      .filter({ hasText: cryptRemoteName });
    await expect(cryptCard).toBeVisible();
    await expect(cryptCard.getByText("任务：E2E 隐私同步").first()).toBeVisible();
    await cryptCard.getByRole("button", { name: "导出密钥恢复包", exact: true }).click();

    const recDialog = page.getByRole("dialog").filter({ hasText: `导出密钥恢复包 · ${cryptRemoteName}` });
    await expect(recDialog).toBeVisible();
    await expect(recDialog).toContainText(`${davRemoteName}:vault`);

    // Verify wrong vs right password against real backend rclone obscure
    const verifyPwInput = recDialog.locator('input[aria-label="验证主加密密码"]');
    await verifyPwInput.fill("WrongPass");
    await recDialog.getByRole("button", { name: "验证密码是否匹配" }).click();
    await expect(recDialog).toContainText("主加密密码与当前配置不一致");

    await verifyPwInput.fill("E2eStrongPassword!");
    await recDialog.getByRole("button", { name: "验证密码是否匹配" }).click();
    await expect(recDialog).toContainText("主加密密码验证通过");
    await expect(
      recDialog.getByRole("button", { name: "从加密存储一键还原到本地" }),
    ).toBeVisible();
    await recDialog.locator('button[aria-label="Close"], .semi-modal-close').click();
  } finally {
    await request
      .delete(`/app/opensync/svr/native/storage?name=${encodeURIComponent(cryptRemoteName)}`, jsonWrite)
      .catch(() => undefined);
    await request
      .delete(`/app/opensync/svr/native/storage?name=${encodeURIComponent(davRemoteName)}`, jsonWrite)
      .catch(() => undefined);
  }
});
