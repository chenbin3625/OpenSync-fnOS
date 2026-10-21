import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { setupServer } from "msw/node";
import {
  handlers,
  resetMockStore,
} from "../src/mocks/handlers";

const server = setupServer(...handlers);
const apiUrl = "http://127.0.0.1/app/opensync/svr";

async function call<T = unknown>(
  path: string,
  init?: RequestInit,
): Promise<{ status: number; body: { code: number; data: T; msg: string } }> {
  const response = await fetch(`${apiUrl}${path}`, init);
  return {
    status: response.status,
    body: (await response.json()) as {
      code: number;
      data: T;
      msg: string;
    },
  };
}

function jsonInit(method: string, body: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  };
}

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => {
  server.resetHandlers();
  resetMockStore();
});
afterAll(() => server.close());

describe("mock task record deletion", () => {
  it("deletes a terminal record instead of stopping an unrelated task", async () => {
    resetMockStore({
      jobs: [{ id: 1, remark: "任务一" }],
      history: {
        1: [{ id: 9001, status: 4, createTime: 100, runTime: 200 }],
      },
    });

    const before = await call<{ count: number }>("/job?id=1&pageNum=1&pageSize=10");
    expect(before.body.data.count).toBe(1);

    const deleted = await call("/job?taskId=9001", { method: "DELETE" });
    expect(deleted.body.code).toBe(200);

    const after = await call<{ count: number }>("/job?id=1&pageNum=1&pageSize=10");
    expect(after.body.data.count).toBe(0);
  });

  it("reports a missing record as an error envelope", async () => {
    resetMockStore({ jobs: [{ id: 1, remark: "任务一" }], history: { 1: [] } });
    const result = await call("/job?taskId=123", { method: "DELETE" });
    expect(result.status).toBe(200);
    expect(result.body.code).toBe(500);
  });
});

describe("mock history search contract", () => {
  it("only matches the task id, like backend mapper.GetJobTaskList", async () => {
    resetMockStore({
      jobs: [{ id: 1, remark: "任务一" }],
      history: {
        1: [
          {
            id: 9001,
            status: 7,
            errMsg: "磁盘空间不足",
            createTime: 100,
            runTime: 200,
          },
        ],
      },
    });
    const byError = await call<{ count: number }>(
      "/job?id=1&pageNum=1&pageSize=10&keyword=%E7%A3%81%E7%9B%98",
    );
    expect(byError.body.data.count).toBe(0);

    const byId = await call<{ count: number }>(
      "/job?id=1&pageNum=1&pageSize=10&keyword=9001",
    );
    expect(byId.body.data.count).toBe(1);
  });
});

describe("mock job action contract", () => {
  it("rejects an empty PUT /job body like the backend", async () => {
    resetMockStore({ jobs: [{ id: 1, remark: "任务一" }] });
    const result = await call("/job", jsonInit("PUT", {}));
    expect(result.status).toBe(200);
    expect(result.body.code).toBe(500);
  });

  it("keeps a paused job enabled while aborting only the running task", async () => {
    const store = resetMockStore({
      jobs: [{ id: 7, remark: "任务", enable: 1 }],
    });
    await call("/job", jsonInit("PUT", { id: "7" }));
    const running = store.getCurrentTask(7);
    expect(running).not.toBeNull();

    await call("/job", jsonInit("PUT", { id: "7", pause: true, abort: true }));
    expect(store.getCurrentTask(7)).toBeNull();
    expect(store.listJobs(1, 10).dataList[0].enable).toBe(1);
    // PauseJob/AbortJob leave an aborted history row.
    expect(store.listHistory(7, { pageNum: 1, pageSize: 10 }).count).toBe(1);
  });

  it("disables the job and records history on a plain pause", async () => {
    const store = resetMockStore({
      jobs: [{ id: 7, remark: "任务", enable: 1 }],
    });
    await call("/job", jsonInit("PUT", { id: "7" }));
    await call("/job", jsonInit("PUT", { id: "7", pause: true }));
    expect(store.listJobs(1, 10).dataList[0].enable).toBe(0);
    expect(store.listHistory(7, { pageNum: 1, pageSize: 10 }).count).toBe(1);
  });

  it("retries a history record even when no task is currently running", async () => {
    const store = resetMockStore({
      jobs: [{ id: 1, remark: "任务一", enable: 1 }],
      history: { 1: [{ id: 9001, status: 7 }] },
    });
    expect(store.getCurrentTask(1)).toBeNull();

    const result = await call(
      "/job",
      jsonInit("PUT", { taskId: "9001", action: "retry" }),
    );
    expect(result.body.code).toBe(200);
    expect(store.getCurrentTask(1)).not.toBeNull();
  });
});

describe("mock settings contract", () => {
  it("defaults match the backend", async () => {
    resetMockStore();
    const result = await call<Record<string, number>>("/system/config");
    expect(result.body.data).toEqual({
      taskTimeout: 48,
      taskSave: 30,
      copyConcurrency: 5,
      scanConcurrency: 16,
      maxRetries: 2,
    });
  });

  it("accepts the backend maximum scan concurrency and rejects above it", async () => {
    resetMockStore();
    const max = await call("/system/config", jsonInit("PUT", { scanConcurrency: 50 }));
    expect(max.body.code).toBe(200);

    const tooBig = await call(
      "/system/config",
      jsonInit("PUT", { scanConcurrency: 51 }),
    );
    expect(tooBig.status).toBe(200);
    expect(tooBig.body.code).toBe(500);
    expect(tooBig.body.msg).toContain("扫描并发数");
  });

  it("answers malformed JSON with HTTP 400 like the handler", async () => {
    resetMockStore();
    const response = await fetch(`${apiUrl}/system/config`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: "{",
    });
    expect(response.status).toBe(400);
    const body = (await response.json()) as { code: number };
    expect(body.code).toBe(500);
  });
});

describe("mock engine and notify drift", () => {
  it("refuses to delete an engine still used by a job", async () => {
    resetMockStore({
      engines: [{ id: 1, remark: "引擎", url: "https://e.test" }],
      jobs: [{ id: 1, remark: "任务", alistId: 1 }],
    });
    const used = await call("/alist?id=1", { method: "DELETE" });
    expect(used.body.code).toBe(500);
    expect(used.body.msg).toContain("任务");
  });

  it("returns the created notify id and redacts secrets in the list", async () => {
    resetMockStore();
    const created = await call<{ id: number }>(
      "/notify",
      jsonInit("POST", {
        notify: {
          method: 1,
          enable: 1,
          params: { sendKey: "SCT-secret-value" },
        },
      }),
    );
    expect(created.body.data.id).toBeGreaterThan(0);

    const list = await call<
      { id: number; method: number; params: string }[]
    >("/notify");
    const createdEntry = list.body.data.find(
      (item) => item.method === 1 && item.id === created.body.data.id,
    );
    expect(createdEntry).toBeDefined();
    const params = JSON.parse(createdEntry!.params) as {
      sendKey: string;
    };
    expect(params.sendKey).not.toContain("secret");
    expect(params.sendKey).toContain("****");
  });

  it("restores a masked secret when editing other fields", async () => {
    const store = resetMockStore({
      notifications: [
        {
          id: 1,
          method: 1,
          enable: 1,
          params: { sendKey: "SCT-real-secret-key", version: "v3" },
        },
      ],
    });
    const list = await call<{ id: number; params: string }[]>("/notify");
    const masked = JSON.parse(list.body.data[0].params) as {
      sendKey: string;
    };

    const saved = await call(
      "/notify",
      jsonInit("PUT", {
        notify: {
          id: 1,
          method: 1,
          enable: 0,
          params: { ...masked, version: "v1" },
        },
      }),
    );
    expect(saved.body.code).toBe(200);
    // Raw store keeps the real key; only the list response is redacted.
    expect(store.getRawNotificationParams(1)).toContain("SCT-real-secret-key");
    expect(store.getRawNotificationParams(1)).toContain('"v1"');
  });

  it("returns the real child-path shape used by the backend", async () => {
    resetMockStore();
    const result = await call<{ path: string }[]>("/alist?alistId=1&path=/");
    expect(Object.keys(result.body.data[0])).toEqual(["path"]);
  });
});
