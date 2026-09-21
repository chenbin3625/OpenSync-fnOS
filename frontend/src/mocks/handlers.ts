import { http, HttpResponse, type DefaultBodyType } from "msw";
import { MockStore, createMockStore, type MockStoreSeed } from "./store";

const apiBase = "*/app/opensync/svr";

export let mockStore = createMockStore();

export function resetMockStore(seed?: MockStoreSeed): MockStore {
  mockStore = createMockStore(seed);
  return mockStore;
}

function ok(data: unknown): HttpResponse<DefaultBodyType> {
  return HttpResponse.json({ code: 200, data, msg: "" } as DefaultBodyType);
}

function fail(message: string, status = 400): HttpResponse<DefaultBodyType> {
  return HttpResponse.json(
    { code: status, data: null, msg: message } as DefaultBodyType,
    { status },
  );
}

function numberParam(
  query: URLSearchParams,
  key: string,
  fallback?: number,
): number | undefined {
  const value = query.get(key);
  if (value === null || value === "") return fallback;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function recordBody(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

async function readBody(request: Request): Promise<Record<string, unknown> | null> {
  try {
    return recordBody(await request.json()) || null;
  } catch {
    return null;
  }
}

export const handlers = [
  http.get(`${apiBase}/session`, () =>
    ok({ uid: 0, development: true, version: "mock" }),
  ),

  http.get(`${apiBase}/system/config`, () => ok(mockStore.getSettings())),
  http.put(`${apiBase}/system/config`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    return ok(mockStore.updateSettings(body));
  }),

  http.get(`${apiBase}/alist`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const engineId = numberParam(query, "alistId");
    const path = query.get("path");
    if (engineId !== undefined && path !== null) return ok(mockStore.listPaths(path));
    return ok(mockStore.listEngines());
  }),
  http.post(`${apiBase}/alist`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    mockStore.saveEngine(body);
    return ok(null);
  }),
  http.put(`${apiBase}/alist`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    mockStore.saveEngine(body);
    return ok(null);
  }),
  http.post(`${apiBase}/alist/test`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const id = numberParam(query, "id");
    if (id === undefined) return fail("缺少引擎 ID");
    return mockStore.listEngines().some((engine) => engine.id === id)
      ? ok(null)
      : fail("引擎不存在", 404);
  }),
  http.delete(`${apiBase}/alist`, ({ request }) => {
    const id = numberParam(new URL(request.url).searchParams, "id");
    if (id === undefined) return fail("缺少引擎 ID");
    mockStore.deleteEngine(id);
    return ok(null);
  }),

  http.get(`${apiBase}/job`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const jobId = numberParam(query, "id");
    const taskId = numberParam(query, "taskId");

    if (query.has("current") && jobId !== undefined) {
      if (query.has("status")) {
        return ok(
          mockStore.listCurrentItems(jobId, {
            taskId: numberParam(query, "expectedTaskId"),
            createTime: numberParam(query, "expectedCreateTime"),
            status: numberParam(query, "status"),
            pageNum: numberParam(query, "pageNum"),
            pageSize: numberParam(query, "pageSize"),
          }),
        );
      }
      return ok(mockStore.getCurrentTask(jobId));
    }

    if (taskId !== undefined) {
      return ok(
        mockStore.listTaskItems(taskId, {
          pageNum: numberParam(query, "pageNum"),
          pageSize: numberParam(query, "pageSize"),
          status: numberParam(query, "status"),
          type: numberParam(query, "type"),
          isPath: numberParam(query, "isPath"),
          hasError: numberParam(query, "hasError"),
          keyword: query.get("keyword") || undefined,
        }),
      );
    }

    if (jobId !== undefined) {
      return ok(
        mockStore.listHistory(jobId, {
          pageNum: numberParam(query, "pageNum"),
          pageSize: numberParam(query, "pageSize"),
          status: numberParam(query, "status"),
          statusIn: query
            .getAll("statusIn")
            .map(Number)
            .filter((value) => Number.isFinite(value)),
          keyword: query.get("keyword") || undefined,
          startTime: numberParam(query, "startTime"),
          endTimeExclusive: numberParam(query, "endTimeExclusive"),
        }),
      );
    }

    return ok(
      mockStore.listJobs(
        numberParam(query, "pageNum"),
        numberParam(query, "pageSize"),
      ),
    );
  }),
  http.post(`${apiBase}/job`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    mockStore.saveJob(body);
    return ok(null);
  }),
  http.put(`${apiBase}/job`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    mockStore.updateJobAction({
      id: body.id as string | number | undefined,
      taskId: body.taskId as string | number | undefined,
      action: typeof body.action === "string" ? body.action : undefined,
      pause: typeof body.pause === "boolean" ? body.pause : undefined,
    });
    return ok(null);
  }),
  http.delete(`${apiBase}/job`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const jobId = numberParam(query, "id");
    const taskId = numberParam(query, "taskId");
    if (jobId !== undefined) {
      mockStore.deleteJob(jobId);
      return ok(null);
    }
    if (taskId !== undefined) {
      for (const job of mockStore.listJobs(1, 500).dataList) {
        const current = mockStore.getCurrentTask(job.id);
        if (current?.taskId === taskId) mockStore.stopTask(taskId);
      }
      return ok(null);
    }
    return fail("缺少任务 ID");
  }),

  http.get(`${apiBase}/notify`, () => ok(mockStore.listNotifications())),
  http.post(`${apiBase}/notify`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    const notify = recordBody(body.notify);
    if (!notify) return fail("通知配置无效");
    mockStore.saveNotification(notify);
    return ok(null);
  }),
  http.post(`${apiBase}/notify/test`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || !recordBody(body.notify)) return fail("通知配置无效");
    return ok(null);
  }),
  http.put(`${apiBase}/notify`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    if (body.notifyId !== undefined) {
      mockStore.toggleNotification(
        Number(body.notifyId),
        Number(body.enable) ? 1 : 0,
      );
    } else {
      const notify = recordBody(body.notify);
      if (!notify) return fail("通知配置无效");
      mockStore.saveNotification(notify);
    }
    return ok(null);
  }),
  http.delete(`${apiBase}/notify`, ({ request }) => {
    const id = numberParam(new URL(request.url).searchParams, "notifyId");
    if (id === undefined) return fail("缺少通知 ID");
    mockStore.deleteNotification(id);
    return ok(null);
  }),
];
