import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { setupServer } from "msw/node";
import {
  buildNotifyParams,
  channelNames,
  getNotifyEndpointSummary,
  notifyToForm,
  validateNotifyForm,
} from "../src/lib/notifyForm";
import {
  buildJobPayload,
  cloneJobToForm,
  defaultJobForm,
  jobToForm,
  summarizeJobFilters,
  validateJobForm,
} from "../src/lib/taskForm";
import { handlers, resetMockStore } from "../src/mocks/handlers";

const server = setupServer(...handlers);
const apiUrl = "http://127.0.0.1/app/opensync/svr";

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  server.resetHandlers();
  resetMockStore();
});
afterAll(() => server.close());

describe("new notification channels, event filters and templates", () => {
  it("exposes all 12 channels and validates channels 6..11", () => {
    expect(channelNames).toHaveLength(12);
    expect(validateNotifyForm({ method: 6, enable: true })).toContain("Bot Token");
    expect(
      validateNotifyForm({ method: 6, enable: true, botToken: "1:abc", chatId: "99" }),
    ).toBe("");
    expect(
      validateNotifyForm({ method: 7, enable: true, url: "http://qyapi.weixin.qq.com/x" }),
    ).toContain("HTTPS");
    expect(
      validateNotifyForm({
        method: 7,
        enable: true,
        url: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=x",
      }),
    ).toBe("");
    expect(validateNotifyForm({ method: 8, enable: true, host: "smtp.test", port: "70000" })).toContain(
      "端口",
    );
    expect(
      validateNotifyForm({
        method: 8,
        enable: true,
        host: "smtp.test",
        port: "587",
        from: "a@b.com",
        to: "c@d.com",
      }),
    ).toBe("");
    expect(validateNotifyForm({ method: 9, enable: true, topic: "ops" })).toBe("");
    expect(
      validateNotifyForm({ method: 10, enable: true, server: "http://10.0.0.2:8080", token: "t" }),
    ).toBe("");
    expect(validateNotifyForm({ method: 11, enable: true, token: "pp" })).toBe("");
  });

  it("omits default eventMode/templates from params and includes them when customized", () => {
    const custom = buildNotifyParams({
      method: 6,
      enable: true,
      botToken: "123:XYZ",
      chatId: "42",
      eventMode: "custom",
      events: ["fail", "timeout"],
      titleTemplate: "[{status}] {jobName}",
      contentTemplate: "Err: {errSummary}",
    });
    expect(custom).toMatchObject({
      botToken: "123:XYZ",
      chatId: "42",
      eventMode: "custom",
      events: ["fail", "timeout"],
      titleTemplate: "[{status}] {jobName}",
      contentTemplate: "Err: {errSummary}",
    });
    const roundTripped = notifyToForm({
      id: 9,
      method: 6,
      enable: 1,
      params: JSON.stringify(custom),
    });
    expect(roundTripped.eventMode).toBe("custom");
    expect(roundTripped.events).toEqual(["fail", "timeout"]);
    expect(roundTripped.titleTemplate).toBe("[{status}] {jobName}");
    expect(getNotifyEndpointSummary({ method: 6, params: JSON.stringify(custom) })).toBe(
      "Chat ID: 42",
    );
  });
});

describe("extended job form fields, validation and cloning", () => {
  it("round trips compareMode, conflictPolicy, trash, time window, filters and notify binding", () => {
    const draft = {
      ...defaultJobForm(1),
      remark: "全功能任务",
      srcPath: ["/Photos"],
      dstPath: ["/Backup"],
      compareMode: 2,
      conflictPolicy: 2,
      trashEnable: true,
      trashDir: ".trash-bin",
      trashRetentionDays: 14,
      filterMode: 1,
      include: "*.jpg\n*.png",
      minFileAgeMinutes: 5,
      olderThanDays: 1,
      newerThanDays: 30,
      copyConcurrency: 4,
      scanConcurrency: 8,
      maxRetries: 3,
      scanIntervalS: 2,
      scanIntervalT: 3,
      windowEnable: true,
      windowStart: "23:00",
      windowEnd: "06:00",
      windowStopOutside: true,
      notifyMode: 1,
      notifyIds: [1, 2],
    };
    expect(validateJobForm(draft)).toBe("");
    const payload = buildJobPayload(draft);
    expect(payload).toMatchObject({
      compareMode: 2,
      conflictPolicy: 2,
      trashEnable: 1,
      trashDir: ".trash-bin",
      trashRetentionDays: 14,
      filterMode: 1,
      include: "*.jpg\n*.png",
      minFileAgeMinutes: 5,
      windowEnable: 1,
      windowStart: "23:00",
      windowEnd: "06:00",
      windowStopOutside: 1,
      notifyMode: 1,
      notifyIds: [1, 2],
    });

    const cloned = cloneJobToForm({
      ...payload,
      id: 99,
      srcPath: JSON.stringify(["/Photos"]),
      dstPath: JSON.stringify(["/Backup"]),
      notifyIds: JSON.stringify([1, 2]),
    } as Parameters<typeof jobToForm>[0]);
    expect(cloned.id).toBeUndefined();
    expect(cloned.remark).toBe("全功能任务 - 副本");
    expect(cloned.notifyIds).toEqual([1, 2]);
  });

  it("rejects invalid time window, empty whitelist, and traversal trashDir", () => {
    const base = {
      ...defaultJobForm(1),
      remark: "校验任务",
      srcPath: ["/a"],
      dstPath: ["/b"],
    };
    expect(validateJobForm({ ...base, trashEnable: true, trashDir: "../bad" })).toContain(
      "回收站",
    );
    expect(
      validateJobForm({ ...base, windowEnable: true, windowStart: "08:00", windowEnd: "08:00" }),
    ).toContain("相同");
    expect(validateJobForm({ ...base, filterMode: 1, include: "" })).toContain("白名单");
    expect(
      summarizeJobFilters({
        exclude: "",
        filterMode: 1,
        include: "*.jpg\n*.raw",
        minFileAgeMinutes: 10,
        olderThanDays: 7,
      }),
    ).toEqual(["白名单 2 条", "稳定 ≥ 10 分钟", "早于 7 天"]);
  });
});

describe("new MSW endpoints for dashboard, dry-run, filter tester, backup, health and logs", () => {
  it("serves dashboard, dry-run, filter test, health, export/import, logs and batch delete", async () => {
    const dashRes = await fetch(`${apiUrl}/dashboard`);
    const dash = (await dashRes.json()) as {
      code: number;
      data: { kpi: { totalJobs: number }; jobs: unknown[] };
    };
    expect(dash.code).toBe(200);
    expect(dash.data.kpi.totalJobs).toBe(1);

    const dryStart = await fetch(`${apiUrl}/job/dry-run`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: 1 }),
    });
    const dryBody = (await dryStart.json()) as {
      code: number;
      data: { status: string; copyCount: number };
    };
    expect(dryBody.code).toBe(200);
    expect(dryBody.data.status).toBe("completed");

    const filterRes = await fetch(`${apiUrl}/job/filter/test`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        filterMode: 1,
        include: "*.jpg",
        samplePath: "2026/a.jpg",
        sampleSize: 1024,
        sampleAgeMinutes: 30,
      }),
    });
    expect(await filterRes.json()).toMatchObject({
      code: 200,
      data: { willSync: true },
    });

    const healthRes = await fetch(`${apiUrl}/alist/health?refresh=1`);
    expect(await healthRes.json()).toMatchObject({
      code: 200,
      data: [{ alistId: 1, status: "online" }],
    });

    const exportRes = await fetch(`${apiUrl}/system/export?includeSecrets=0`);
    const exported = (await exportRes.json()) as {
      code: number;
      data: { version: number; hasSecrets: boolean };
    };
    expect(exported.data.version).toBe(1);
    expect(exported.data.hasSecrets).toBe(false);

    const logsRes = await fetch(`${apiUrl}/system/logs?lines=50&level=error`);
    const logs = (await logsRes.json()) as { code: number; data: { lines: string[] } };
    expect(logs.code).toBe(200);
    expect(logs.data.lines.length).toBeGreaterThan(0);

    const batchDel = await fetch(`${apiUrl}/job/tasks/delete`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ clearJobId: 1 }),
    });
    expect(await batchDel.json()).toMatchObject({ code: 200 });
  });
});
