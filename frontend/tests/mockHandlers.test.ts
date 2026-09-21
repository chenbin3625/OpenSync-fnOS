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
    const created = await json<{ code: number; data: null }>(createResponse);
    expect(created).toMatchObject({ code: 200, data: null });

    const listResponse = await fetch(`${apiUrl}/job?pageNum=1&pageSize=20`);
    const list = await json<{
      data: { dataList: { remark: string; srcPath: string }[] };
    }>(listResponse);
    expect(list.data.dataList[0]).toMatchObject({
      remark: "持久化任务",
      srcPath: JSON.stringify(["/Photos"]),
    });
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
});
