import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { setupServer } from "msw/node";
import { handlers, resetMockStore } from "../src/mocks/handlers";

const server = setupServer(...handlers);
const apiUrl = "http://127.0.0.1/app/opensync/svr";

async function json<T>(response: Response): Promise<T> {
  return response.json() as Promise<T>;
}

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  server.resetHandlers();
  resetMockStore();
});
afterAll(() => server.close());

describe("mock HTTP handlers", () => {
  it("routes job list requests by query shape", async () => {
    resetMockStore({
      jobs: [
        { id: 1, remark: "任务一" },
        { id: 2, remark: "任务二" },
      ],
    });

    const response = await fetch(`${apiUrl}/job?pageNum=2&pageSize=1`);
    const body = await json<{
      code: number;
      data: { dataList: { id: number; remark: string }[]; count: number };
      msg: string;
    }>(response);

    expect(response.status).toBe(200);
    expect(body).toMatchObject({
      code: 200,
      data: { dataList: [{ id: 2, remark: "任务二" }], count: 2 },
      msg: "",
    });
  });

  it("keeps CRUD changes visible to later requests", async () => {
    resetMockStore({ jobs: [] });

    const createResponse = await fetch(`${apiUrl}/job`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        remark: "持久化任务",
        alistId: 1,
        srcPath: ["/Photos"],
        dstPath: ["/Backup"],
        method: 0,
        isCron: 2,
        enable: 1,
      }),
    });
    const created = await json<{ code: number; data: { id: number } }>(
      createResponse,
    );
    expect(created.code).toBe(200);
    expect(created.data.id).toBeGreaterThan(0);

    const editResponse = await fetch(`${apiUrl}/job`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        id: created.data.id,
        remark: "持久化任务",
        alistId: 1,
        srcPath: ["/Photos"],
        dstPath: ["/Backup"],
        method: 0,
        isCron: 2,
        enable: 1,
      }),
    });
    expect(await json<{ code: number; data: null }>(editResponse)).toMatchObject({
      code: 200,
      data: null,
    });

    const listResponse = await fetch(`${apiUrl}/job?pageNum=1&pageSize=20`);
    const list = await json<{
      data: { dataList: { remark: string; srcPath: string }[] };
    }>(listResponse);
    expect(list.data.dataList[0]).toMatchObject({
      remark: "持久化任务",
      srcPath: JSON.stringify(["/Photos"]),
    });
  });

  it("mirrors the backend notification contract", async () => {
    resetMockStore({ notifications: [] });

    // The real handler answers HTTP 400 with code 500 for missing `enable`.
    const missingEnable = await fetch(`${apiUrl}/notify`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ notify: { method: 1, params: {} } }),
    });
    expect(missingEnable.status).toBe(400);
    expect(await json<{ code: number; msg: string }>(missingEnable)).toMatchObject({
      code: 500,
      msg: "缺少必要参数",
    });

    const created = await fetch(`${apiUrl}/notify`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ notify: { method: 1, enable: 1, params: {} } }),
    });
    const createdBody = await json<{ code: number; data: { id: number } }>(created);
    expect(createdBody.code).toBe(200);
    expect(createdBody.data.id).toBeGreaterThan(0);

    const list = await json<{ data: { id: number; enable: number }[] }>(
      await fetch(`${apiUrl}/notify`),
    );
    expect(list.data).toHaveLength(1);

    await fetch(`${apiUrl}/notify`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ notifyId: createdBody.data.id, enable: 0 }),
    });
    const disabled = await json<{ data: { enable: number }[] }>(
      await fetch(`${apiUrl}/notify`),
    );
    expect(disabled.data[0].enable).toBe(0);
  });

  it("returns a task snapshot and status page through the same job endpoint", async () => {
    resetMockStore({ jobs: [{ id: 7, remark: "实时任务", enable: 1 }] });

    await fetch(`${apiUrl}/job`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: "7" }),
    });
    const snapshotResponse = await fetch(
      `${apiUrl}/job?id=7&current=1`,
    );
    const snapshot = await json<{
      data: { taskId: number; createTime: number; num: { running: number } };
    }>(snapshotResponse);

    const pageResponse = await fetch(
      `${apiUrl}/job?id=7&current=1&status=1&pageNum=1&pageSize=5&expectedTaskId=${snapshot.data.taskId}&expectedCreateTime=${snapshot.data.createTime}`,
    );
    const page = await json<{
      data: {
        taskId: number;
        status: number;
        pageSize: number;
        dataList: { status: number }[];
      };
    }>(pageResponse);

    expect(snapshot.data.num.running).toBeGreaterThan(0);
    expect(page.data).toMatchObject({
      taskId: snapshot.data.taskId,
      status: 1,
      pageSize: 5,
    });
    expect(page.data.dataList.every((item) => item.status === 1)).toBe(true);
  });

  it("supports job-level items query, csv export, and cross-batch item retry", async () => {
    resetMockStore({
      jobs: [{ id: 3, remark: "汇总记录", enable: 1 }],
      history: {
        3: [
          { id: 301, status: 2, createTime: 100 },
          { id: 302, status: 6, errMsg: "network unreachable", createTime: 200 },
        ],
      },
      taskItems: {
        301: [
          { id: 41, fileName: "ok.jpg", status: 2, createTime: 101 },
          { id: 42, fileName: "bad.jpg", status: 7, errMsg: "read failed", createTime: 102 },
        ],
      },
    });

    const itemsResp = await fetch(`${apiUrl}/job/items?jobId=3&pageNum=1&pageSize=20`);
    const itemsBody = await json<{
      code: number;
      data: {
        count: number;
        errorGroups?: { reason: string; count: number }[];
        recentErrors?: { errMsg: string }[];
      };
    }>(itemsResp);
    expect(itemsBody.code).toBe(200);
    expect(itemsBody.data.count).toBe(2);
    expect(itemsBody.data.errorGroups).toEqual([{ reason: "read failed", count: 1 }]);
    expect(itemsBody.data.recentErrors?.[0]?.errMsg).toBe("network unreachable");

    const exportResp = await fetch(`${apiUrl}/job/items/export?jobId=3`);
    expect(exportResp.status).toBe(200);
    const csv = await exportResp.text();
    expect(csv).toContain("ok.jpg");
    expect(csv).toContain("bad.jpg");

    const retryResp = await fetch(`${apiUrl}/job`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: "3", action: "retry", itemIds: [42] }),
    });
    expect(retryResp.status).toBe(200);
    expect(await json<{ code: number }>(retryResp)).toMatchObject({ code: 200 });
  });
});
