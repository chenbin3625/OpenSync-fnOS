import { test, expect } from "@playwright/test";

test("task editor provides preset and custom file type filters", async ({
  page,
}) => {
  await page.goto("/app/opensync/tasks");
  await page.getByRole("button", { name: "新建任务", exact: true }).click();
  const dialog = page.getByRole("dialog");

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
  ).toContainText("0 表示不限");
  await expect(
    page.getByRole("textbox", { name: "高级排除规则", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByText("高级排除规则", { exact: true })).toHaveCount(0);

  await expect(
    page.getByRole("checkbox", { name: "临时文件", exact: true }),
  ).toBeChecked();
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
    .fill("iso");
  await page.getByRole("button", { name: "新增文件类型", exact: true }).click();
  await expect(page.getByRole("button", { name: /其他/ })).toBeVisible();
  const groupLabels = await dialog.locator(".file-type-group-label").allTextContents();
  expect(groupLabels.at(-1)).toContain("其他");
  await expect(
    page.getByRole("checkbox", { name: "*.iso", exact: true }),
  ).toBeChecked();
});

test("engine form rejects invalid addresses and protects dirty cancellation", async ({
  page,
}) => {
  await page.goto("/app/opensync/engines");
  await page.getByRole("button", { name: "添加引擎", exact: true }).click();
  await page.getByRole("button", { name: "保存", exact: true }).click();
  await expect(
    page.getByText("请输入有效的 HTTP / HTTPS 引擎地址"),
  ).toBeVisible();
  await page
    .getByRole("textbox", { name: "引擎地址", exact: true })
    .fill("ftp://invalid");
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByText("放弃未保存的修改？")).toBeVisible();
  await page.getByRole("button", { name: "放弃修改", exact: true }).click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
});
test("notification channels switch without retaining unrelated fields", async ({
  page,
}) => {
  await page.goto("/app/opensync/notifications");
  await page.getByRole("button", { name: "添加通知", exact: true }).click();
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
  const original = await timeout.inputValue();
  await timeout.fill(String(Number(original) + 1));
  await timeout.blur();
  await expect(
    page.getByRole("button", { name: "保存设置", exact: true }),
  ).toBeEnabled();
  await expect(page.getByRole("spinbutton")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "还原", exact: true }),
  ).toHaveCount(0);
  await timeout.fill("abc");
  await page.getByRole("button", { name: "保存设置", exact: true }).click();
  await expect(
    page.getByText("任务超时请输入 0–8760 的整数", { exact: true }),
  ).toBeVisible();
  await timeout.fill("8761");
  await page.getByRole("button", { name: "保存设置", exact: true }).click();
  await expect(
    page.getByText("任务超时请输入 0–8760 的整数", { exact: true }),
  ).toBeVisible();
  await page.reload();
  await expect(timeout).toHaveValue(original);
  await expect(page.getByText("小时", { exact: true })).toBeVisible();
  await expect(page.getByText("天", { exact: true })).toBeVisible();
});
