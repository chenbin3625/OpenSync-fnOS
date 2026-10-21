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

/**
 * Mirrors backend handler.respondError: business errors are HTTP 200 with
 * `code:500` in the envelope. Only the few paths that really answer a non-200
 * status (e.g. invalid JSON on PUT /system/config) pass `httpStatus`.
 */
function fail(
  message: string,
  httpStatus = 200,
): HttpResponse<DefaultBodyType> {
  return HttpResponse.json(
    { code: 500, data: null, msg: message } as DefaultBodyType,
    { status: httpStatus },
  );
}

const systemSettingRanges: Record<string, { name: string; min: number; max: number }> = {
  copyConcurrency: { name: "操作并发数", min: 1, max: 100 },
  scanConcurrency: { name: "扫描并发数", min: 1, max: 50 },
  maxRetries: { name: "失败重试次数", min: 0, max: 10 },
  taskTimeout: { name: "任务超时时间", min: 0, max: 8760 },
  taskSave: { name: "历史任务保留", min: 0, max: 3650 },
};

function validateSettingsPatch(
  body: Record<string, unknown>,
): string | null {
  for (const [key, range] of Object.entries(systemSettingRanges)) {
    if (body[key] === undefined) continue;
    const value = Number(body[key]);
    if (!Number.isInteger(value) || value < range.min || value > range.max) {
      return `${range.name}必须在${range.min}到${range.max}之间`;
    }
  }
  return null;
}

function validateJobInput(body: Record<string, unknown>): string | null {
  const srcPath = Array.isArray(body.srcPath) ? body.srcPath : [];
  const dstPath = Array.isArray(body.dstPath) ? body.dstPath : [];
  if (!srcPath.length || !dstPath.length || Number(body.alistId) <= 0) {
    return "缺少必要参数";
  }
  const hasParentTraversal = [...srcPath, ...dstPath].some(
    (path) => typeof path === "string" && path.split(/[\\/]/).includes(".."),
  );
  if (hasParentTraversal) return "缺少必要参数";
  const method = Number(body.method ?? 0);
  if (!Number.isInteger(method) || method < 0 || method > 2) {
    return "缺少必要参数";
  }
  const isCron = Number(body.isCron ?? 0);
  if (!Number.isInteger(isCron) || isCron < 0 || isCron > 2) {
    return "缺少必要参数";
  }
  if (isCron === 0 && Number(body.interval ?? 0) <= 0) {
    return "请填写执行间隔";
  }
  return null;
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
  http.get(`${apiBase}/version/latest`, () =>
    ok({
      latestVersion: "v0.0.27",
      releaseURL: "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest",
      hasUpdate: false,
    }),
  ),

  http.get(`${apiBase}/system/config`, () => ok(mockStore.getSettings())),
  http.put(`${apiBase}/system/config`, async ({ request }) => {
    const body = await readBody(request);
    // The real handler answers malformed JSON with HTTP 400 (body code 500).
    if (!body) return fail("请求内容无效", 400);
    const invalid = validateSettingsPatch(body);
    if (invalid) return fail(invalid);
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
    if (!mockStore.listEngines().some((engine) => engine.id === id))
      return fail("引擎不存在");
    // service.RemoveClient refuses while jobs still reference the engine.
    if (mockStore.listJobs(1, 500).dataList.some((job) => job.alistId === id))
      return fail("该引擎仍被同步任务使用，请先删除相关任务");
    mockStore.deleteEngine(id);
    return ok(null);
  }),

  http.get(`${apiBase}/job`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const jobId = numberParam(query, "id");
    const taskId = numberParam(query, "taskId");
    const rawId = query.get("id");
    const rawTaskId = query.get("taskId");
    if (rawId !== null && rawId !== "" && jobId === undefined)
      return fail("缺少必要参数");
    if (rawTaskId !== null && rawTaskId !== "" && taskId === undefined)
      return fail("缺少必要参数");

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
    const invalid = validateJobInput(body);
    if (invalid) return fail(invalid);
    mockStore.saveJob(body);
    return ok(null);
  }),
  http.put(`${apiBase}/job`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    if (body.taskId !== undefined) {
      if (typeof body.action !== "string" || !["stop", "retry"].includes(body.action))
        return fail("缺少必要参数");
    } else if (body.pause === undefined) {
      // Manual run: an empty body used to fall through and run every job.
      if (body.id === undefined || body.id === null) return fail("缺少必要参数");
    } else if (body.id === undefined || body.id === null) {
      return fail("缺少必要参数");
    }
    mockStore.updateJobAction({
      id: body.id as string | number | undefined,
      taskId: body.taskId as string | number | undefined,
      action: typeof body.action === "string" ? body.action : undefined,
      pause: typeof body.pause === "boolean" ? body.pause : undefined,
      abort: typeof body.abort === "boolean" ? body.abort : undefined,
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
      // service.RemoveTask deletes a finished task record; it must not fall
      // through to StopTask (the old mock silently stopped the wrong thing and
      // never removed the record).
      const result = mockStore.removeTaskRecord(taskId);
      if (result === "running") return fail("任务正在运行，无法删除");
      if (result === "missing") return fail("任务不存在");
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
    if (notify.enable === undefined) return fail("缺少必要参数");
    const created = mockStore.saveNotification(notify);
    // The real handler returns the new row id so clients do not guess it.
    return ok({ id: created.id });
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
