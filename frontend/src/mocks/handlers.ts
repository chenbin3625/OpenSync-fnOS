import { http, HttpResponse, type DefaultBodyType } from "msw";
import { MockStore, createMockStore, type MockStoreSeed } from "./store";
import type { NativeTransferTask } from "../types";
import { validateBwLimitExpression } from "../lib/bwLimit";

const apiBase = "*/app/opensync/svr";

const nativeTransfers: NativeTransferTask[] = [];

export let mockStore = createMockStore();

export function resetMockStore(seed?: MockStoreSeed): MockStore {
  mockStore = createMockStore(seed);
  nativeTransfers.length = 0;
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
  httpStatus = 400,
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
  engineHealthCheckInterval: { name: "引擎健康检查周期", min: 0, max: 1440 },
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
  if (typeof body.defaultBwLimit === "string") {
    const bwErr = validateBwLimitExpression(body.defaultBwLimit);
    if (bwErr) return bwErr;
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
  if (!Number.isInteger(method) || method < 0 || method > 3) {
    return "缺少必要参数";
  }
  if (method === 3 && (srcPath.length !== 1 || dstPath.length !== 1)) {
    return "双向同步模式只允许一个源目录和一个目标目录";
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

  http.get(`${apiBase}/dashboard`, () => ok(mockStore.getDashboard())),
  http.get(`${apiBase}/system/config`, () => ok(mockStore.getSettings())),
  http.put(`${apiBase}/system/config`, async ({ request }) => {
    const body = await readBody(request);
    // The real handler answers malformed JSON with HTTP 400 (body code 500).
    if (!body) return fail("请求内容无效", 400);
    const invalid = validateSettingsPatch(body);
    if (invalid) return fail(invalid);
    return ok(mockStore.updateSettings(body));
  }),
  http.get(`${apiBase}/system/export`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const includeSecrets = query.get("includeSecrets") === "1";
    const data = mockStore.exportConfig(includeSecrets);
    if (query.get("download") === "1") {
      return new HttpResponse(JSON.stringify(data, null, 2), {
        headers: {
          "Content-Type": "application/json; charset=utf-8",
          "Content-Disposition": 'attachment; filename="opensync-backup.json"',
        },
      });
    }
    return ok(data);
  }),
  http.post(`${apiBase}/system/import`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效", 400);
    const payload = (recordBody(body.payload) || body) as unknown as import("../types").SystemBackupPayload;
    const conflictMode = typeof body.conflictMode === "string" ? body.conflictMode : "skip";
    return ok(mockStore.importConfig(payload, conflictMode));
  }),
  http.get(`${apiBase}/system/logs`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    return ok(
      mockStore.getLogs(
        numberParam(query, "lines", 200),
        query.get("level") || "",
        query.get("keyword") || "",
      ),
    );
  }),
  http.get(`${apiBase}/system/logs/download`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const data = mockStore.getLogs(
      numberParam(query, "lines", 2000),
      query.get("level") || "",
      query.get("keyword") || "",
    );
    return new HttpResponse(data.lines.join("\n") + "\n", {
      headers: {
        "Content-Type": "text/plain; charset=utf-8",
        "Content-Disposition": 'attachment; filename="opensync-logs.log"',
      },
    });
  }),

  http.get(`${apiBase}/native/storage`, () =>
    ok(mockStore.getNativeStorageState()),
  ),
  http.put(`${apiBase}/native/engine`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    const remark =
      typeof body.remark === "string" ? body.remark : undefined;
    const maxConcurrentJobs =
      body.maxConcurrentJobs === undefined
        ? undefined
        : Number(body.maxConcurrentJobs);
    if (remark !== undefined && !remark.trim()) return fail("请输入引擎名称");
    if (
      maxConcurrentJobs !== undefined &&
      (!Number.isInteger(maxConcurrentJobs) ||
        maxConcurrentJobs < 0 ||
        maxConcurrentJobs > 50)
    ) {
      return fail("最大并发任务数必须在 0–50 之间");
    }
    return ok(mockStore.saveNativeEngine({ remark, maxConcurrentJobs }));
  }),
  http.post(`${apiBase}/native/storage`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    if (typeof body.config === "string" && body.config.trim()) {
      const res = mockStore.importNativeRemotes({
        config: body.config,
        replace: Boolean(body.replace),
      });
      return typeof res === "string" ? fail(res) : ok(null);
    }
    const err = mockStore.saveNativeRemote({
      name: typeof body.name === "string" ? body.name : undefined,
      options: recordBody(body.options) as Record<string, string> | undefined,
      replace: Boolean(body.replace),
      maxConcurrentJobs:
        body.maxConcurrentJobs === undefined
          ? undefined
          : Number(body.maxConcurrentJobs),
      authSessionId:
        typeof body.authSessionId === "string" ? body.authSessionId : undefined,
    });
    return err ? fail(err) : ok(null);
  }),
  http.put(`${apiBase}/native/storage`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.name !== "string" || !recordBody(body.options)) {
      return fail("缺少必要参数");
    }
    const err = mockStore.updateNativeRemote({
      name: body.name,
      newName: typeof body.newName === "string" ? body.newName : undefined,
      options: recordBody(body.options) as Record<string, string>,
      clearSecrets: Array.isArray(body.clearSecrets)
        ? body.clearSecrets.map(String)
        : undefined,
      authSessionId:
        typeof body.authSessionId === "string" ? body.authSessionId : undefined,
    });
    return err ? fail(err) : ok(null);
  }),
  http.delete(`${apiBase}/native/storage`, ({ request }) => {
    const name = new URL(request.url).searchParams.get("name") || "";
    if (!name.trim()) return fail("缺少必要参数");
    const err = mockStore.deleteNativeRemote(name);
    return err ? fail(err) : ok(null);
  }),
  http.post(`${apiBase}/native/storage/test`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    return ok(
      mockStore.testNativeRemote({
        name: typeof body.name === "string" ? body.name : undefined,
        options: recordBody(body.options) as Record<string, string> | undefined,
      }),
    );
  }),
  http.post(`${apiBase}/native/storage/rename`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.oldName !== "string" || typeof body.newName !== "string") {
      return fail("缺少必要参数");
    }
    const res = mockStore.renameNativeRemote(body.oldName, body.newName);
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.post(`${apiBase}/native/storage/export`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    const res = mockStore.exportNativeConfig(
      Array.isArray(body.names) ? body.names.map(String) : undefined,
      typeof body.password === "string" ? body.password : undefined,
    );
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.post(`${apiBase}/native/storage/about`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.name !== "string") return fail("缺少必要参数");
    const res = mockStore.aboutNativeStorage(body.name);
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.post(`${apiBase}/native/storage/browse`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    const res = mockStore.browseNativeStorage(
      typeof body.path === "string" ? body.path : "/",
    );
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.post(`${apiBase}/native/storage/transfers/restore`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    const err = mockStore.restoreNativeFile({
      srcPath: typeof body.srcPath === "string" ? body.srcPath : "",
      dstLocalDir: typeof body.dstLocalDir === "string" ? body.dstLocalDir : "",
    });
    if (err) return fail(err);
    const now = Math.floor(Date.now() / 1000);
    const task: NativeTransferTask = {
      id: crypto.randomUUID(),
      kind: "restore",
      label: "恢复到本地",
      status: "completed",
      doneBytes: 0,
      totalBytes: 0,
      speed: 0,
      startedAt: now,
      finishedAt: now,
      percent: 100,
      totalItems: 1,
      completedItems: 1,
    };
    nativeTransfers.push(task);
    return ok(task);
  }),
  http.get(`${apiBase}/native/storage/transfers`, () => ok(nativeTransfers)),
  http.delete(`${apiBase}/native/storage/transfers`, ({ request }) => {
    const id = new URL(request.url).searchParams.get("id");
    if (!id) nativeTransfers.length = 0;
    return ok(null);
  }),
  http.post(`${apiBase}/native/storage/mkdir`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.path !== "string") return fail("缺少目录路径");
    const err = mockStore.mkdirNativeStorage(body.path);
    return err ? fail(err) : ok(null);
  }),
  http.post(`${apiBase}/native/storage/move`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.srcPath !== "string" || typeof body.dstPath !== "string") {
      return fail("缺少源或目标路径");
    }
    const err = mockStore.moveNativeStoragePath({
      srcPath: body.srcPath,
      dstPath: body.dstPath,
      isDir: Boolean(body.isDir),
    });
    return err ? fail(err) : ok(null);
  }),
  http.post(`${apiBase}/native/storage/delete`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.path !== "string") return fail("缺少路径");
    const err = mockStore.deleteNativeStoragePath({
      path: body.path,
      isDir: Boolean(body.isDir),
    });
    return err ? fail(err) : ok(null);
  }),
  http.post(`${apiBase}/native/storage/size`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.path !== "string") return fail("缺少路径");
    const res = mockStore.sizeNativeStorage(body.path);
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.post(`${apiBase}/native/storage/auth/start`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.type !== "string") return fail("缺少存储类型");
    const res = mockStore.startNativeStorageAuth({
      type: body.type,
      options: recordBody(body.options) as Record<string, string> | undefined,
    });
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.get(`${apiBase}/native/storage/auth/status`, ({ request }) => {
    const sessionId = new URL(request.url).searchParams.get("sessionId") || "";
    if (!sessionId.trim()) return fail("缺少必要参数");
    const res = mockStore.getNativeStorageAuth(sessionId);
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.post(`${apiBase}/native/storage/auth/callback`, async ({ request }) => {
    const body = await readBody(request);
    if (
      !body ||
      typeof body.sessionId !== "string" ||
      typeof body.callbackUrl !== "string"
    ) {
      return fail("缺少必要参数");
    }
    const res = mockStore.submitNativeStorageAuthCallback(
      body.sessionId,
      body.callbackUrl,
    );
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.post(`${apiBase}/native/storage/auth/continue`, async ({ request }) => {
    const body = await readBody(request);
    if (
      !body ||
      typeof body.sessionId !== "string" ||
      typeof body.state !== "string" ||
      typeof body.answer !== "string"
    ) {
      return fail("缺少必要参数");
    }
    const res = mockStore.continueNativeStorageAuth(
      body.sessionId,
      body.state,
      body.answer,
    );
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.delete(`${apiBase}/native/storage/auth`, ({ request }) => {
    const sessionId = new URL(request.url).searchParams.get("sessionId") || "";
    if (!sessionId.trim()) return fail("缺少必要参数");
    mockStore.cancelNativeStorageAuth(sessionId);
    return ok(null);
  }),
  http.post(`${apiBase}/native/storage/import/preview`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.config !== "string") return fail("请求内容无效");
    const res = mockStore.previewNativeImport(body.config);
    return typeof res === "string" ? fail(res) : ok(res);
  }),
  http.post(`${apiBase}/native/storage/import`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.config !== "string") return fail("请求内容无效");
    const res = mockStore.importNativeRemotes({
      config: body.config,
      selected: Array.isArray(body.selected)
        ? body.selected.map(String)
        : undefined,
      replace: Boolean(body.replace),
    });
    return typeof res === "string" ? fail(res) : ok(res);
  }),

  http.get(`${apiBase}/alist`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const engineId = numberParam(query, "alistId");
    const path = query.get("path");
    if (engineId !== undefined && path !== null) return ok(mockStore.listPaths(path));
    return ok(mockStore.listEngines());
  }),
  http.get(`${apiBase}/alist/health`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const id = numberParam(query, "id");
    const refresh = query.get("refresh") === "1";
    if (id !== undefined) {
      if (!mockStore.listEngines().some((e) => e.id === id))
        return fail("引擎不存在", 404);
      return ok(mockStore.getEngineHealth(id, refresh));
    }
    return ok(mockStore.listAllEngineHealth(refresh));
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

  http.post(`${apiBase}/job/batch`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || typeof body.action !== "string") return fail("缺少必要参数");
    const action = body.action.trim().toLowerCase();
    if (
      ![
        "run",
        "stop",
        "enable",
        "disable",
        "run_all",
        "stop_all",
        "enable_all",
        "disable_all",
      ].includes(action)
    ) {
      return fail("不支持的批量操作");
    }
    const ids = Array.isArray(body.ids)
      ? body.ids.map(Number).filter(Number.isFinite)
      : undefined;
    return ok(mockStore.batchJobAction(action, ids));
  }),
  http.get(`${apiBase}/job/items/recent`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const parseIntParam = (key: string): number | undefined | "invalid" => {
      const raw = query.get(key)?.trim();
      if (!raw) return undefined;
      if (!/^-?\d+$/.test(raw)) return "invalid";
      return Number(raw);
    };
    const pageNum = parseIntParam("pageNum");
    const pageSize = parseIntParam("pageSize");
    const jobId = parseIntParam("jobId");
    const status = parseIntParam("status");
    const type = parseIntParam("type");
    const isPath = parseIntParam("isPath");
    if (
      pageNum === "invalid" ||
      pageSize === "invalid" ||
      jobId === "invalid" ||
      status === "invalid" ||
      type === "invalid" ||
      isPath === "invalid"
    ) {
      return fail("缺少必要参数");
    }

    let jobIds: number[] | undefined;
    const rawJobIds = query.getAll("jobIds");
    if (rawJobIds.length > 0) {
      const tokens = rawJobIds
        .flatMap((v) => v.split(","))
        .map((s) => s.trim())
        .filter(Boolean);
      const parsedIds: number[] = [];
      for (const token of tokens) {
        if (!/^\d+$/.test(token)) return fail("缺少必要参数");
        parsedIds.push(Number(token));
      }
      if (parsedIds.length > 0) jobIds = parsedIds;
    }

    let hasError: number | undefined;
    const rawHasError = query.get("hasError")?.trim().toLowerCase();
    if (rawHasError) {
      if (rawHasError === "1" || rawHasError === "true") hasError = 1;
      else if (rawHasError === "0" || rawHasError === "false") hasError = 0;
      else return fail("缺少必要参数");
    }

    return ok(
      mockStore.listRecentTaskItems({
        pageNum,
        pageSize,
        jobId,
        jobIds,
        storage: query.get("storage")?.trim() || undefined,
        status,
        type,
        isPath,
        hasError,
        keyword: query.get("keyword")?.trim() || undefined,
      }),
    );
  }),
  http.post(`${apiBase}/job/tasks/delete`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    if (body.clearJobId !== undefined) {
      return ok({ deleted: mockStore.clearJobHistory(Number(body.clearJobId)) });
    }
    if (Array.isArray(body.taskIds)) {
      return ok({ deleted: mockStore.deleteTasksBatch(body.taskIds.map(Number)) });
    }
    return fail("缺少必要参数");
  }),
  http.get(`${apiBase}/job/items/export`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const taskId = numberParam(query, "taskId");
    if (taskId === undefined) return fail("缺少任务 ID");
    const format = query.get("format")?.trim().toLowerCase();
    if (format && format !== "csv") return fail("仅支持 CSV 导出");
    const rows = mockStore.listTaskItems(taskId, { pageNum: 1, pageSize: 500 }).dataList;
    const text =
      "\uFEFF文件名,源路径,目标路径,大小,状态\n" +
      rows.map((r) => `${r.fileName || ""},${r.srcPath || ""},${r.dstPath || ""},${r.fileSize || 0},${r.status}`).join("\n");
    return new HttpResponse(text, {
      headers: {
        "Content-Type": "text/csv; charset=utf-8",
        "Content-Disposition": `attachment; filename="task-${taskId}.csv"`,
      },
    });
  }),
  http.post(`${apiBase}/job/dry-run`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || body.id === undefined) return fail("缺少任务 ID");
    const res = mockStore.startDryRun(Number(body.id));
    return res ? ok(res) : fail("任务不存在");
  }),
  http.get(`${apiBase}/job/dry-run`, ({ request }) => {
    const id = numberParam(new URL(request.url).searchParams, "id");
    if (id === undefined) return fail("缺少任务 ID");
    return ok(mockStore.getDryRun(id));
  }),
  http.delete(`${apiBase}/job/dry-run`, ({ request }) => {
    const id = numberParam(new URL(request.url).searchParams, "id");
    if (id === undefined) return fail("缺少任务 ID");
    mockStore.cancelDryRun(id);
    return ok(null);
  }),
  http.post(`${apiBase}/job/check`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || body.id === undefined) return fail("缺少任务 ID");
    const res = mockStore.startJobCheck(
      Number(body.id),
      Boolean(body.download),
      Boolean(body.cryptCheck),
    );
    return res ? ok(res) : fail("任务不存在");
  }),
  http.get(`${apiBase}/job/conflicts`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const id = numberParam(query, "id");
    if (id === undefined) return fail("缺少任务 ID");
    const includeResolved = query.get("includeResolved") === "1";
    return ok(mockStore.listJobConflicts(id, includeResolved));
  }),
  http.post(`${apiBase}/job/conflicts/resolve`, async ({ request }) => {
    const body = await readBody(request);
    if (!body || body.jobId === undefined || body.conflictId === undefined) {
      return fail("缺少必要参数");
    }
    const err = mockStore.resolveJobConflict(
      Number(body.jobId),
      Number(body.conflictId),
      String(body.action || ""),
    );
    return err ? fail(err) : ok(null);
  }),
  http.get(`${apiBase}/job/check`, ({ request }) => {
    const id = numberParam(new URL(request.url).searchParams, "id");
    if (id === undefined) return fail("缺少任务 ID");
    return ok(mockStore.getJobCheck(id));
  }),
  http.delete(`${apiBase}/job/check`, ({ request }) => {
    const id = numberParam(new URL(request.url).searchParams, "id");
    if (id === undefined) return fail("缺少任务 ID");
    mockStore.cancelJobCheck(id);
    return ok(null);
  }),
  http.post(`${apiBase}/job/filter/test`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    return ok(mockStore.testJobFilter(body));
  }),
  http.get(`${apiBase}/job/stream`, ({ request }) => {
    const query = new URL(request.url).searchParams;
    const jobId = numberParam(query, "id");
    if (jobId === undefined || !Number.isInteger(jobId) || jobId <= 0) {
      return fail("缺少必要参数");
    }
    const encoder = new TextEncoder();
    let timer: ReturnType<typeof setInterval> | undefined;
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        const payload = JSON.stringify({
          code: 200,
          data: mockStore.getCurrentTask(jobId),
          msg: "success",
        });
        controller.enqueue(encoder.encode(`data: ${payload}\n\n`));
        const cleanup = () => {
          if (timer !== undefined) {
            clearInterval(timer);
            timer = undefined;
          }
        };
        timer = setInterval(() => {
          try {
            controller.enqueue(encoder.encode(": heartbeat\n\n"));
          } catch {
            cleanup();
          }
        }, 15_000);
        if (typeof timer === "object" && timer && "unref" in timer) {
          (timer as { unref?: () => void }).unref?.();
        }
        request.signal?.addEventListener("abort", () => {
          cleanup();
          try {
            controller.close();
          } catch {
            /* stream already closed */
          }
        });
      },
      cancel() {
        if (timer !== undefined) {
          clearInterval(timer);
          timer = undefined;
        }
      },
    });
    return new HttpResponse(stream, {
      headers: {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
      },
    });
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
    const isEdit =
      body.id !== undefined && body.id !== null && String(body.id) !== "";
    const saved = mockStore.saveJob(body);
    return ok(isEdit ? null : { id: saved.id });
  }),
  http.put(`${apiBase}/job`, async ({ request }) => {
    const body = await readBody(request);
    if (!body) return fail("请求内容无效");
    if (body.taskId !== undefined) {
      if (
        typeof body.action !== "string" ||
        !["stop", "retry", "approve", "reject", "pause", "resume", "bwlimit"].includes(body.action)
      )
        return fail("缺少必要参数");
      if (body.bwLimit !== undefined && body.action !== "bwlimit")
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
      bwLimit: typeof body.bwLimit === "string" ? body.bwLimit : undefined,
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
    const parseStrictID = (raw: unknown): number | null => {
      if (typeof raw === "number") {
        return Number.isInteger(raw) && raw > 0 ? raw : null;
      }
      if (typeof raw === "string" && /^\s*\d+\s*$/.test(raw)) {
        const parsed = Number(raw.trim());
        return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null;
      }
      return null;
    };
    const parseEnable = (raw: unknown): 0 | 1 | null => {
      if (typeof raw === "boolean") return raw ? 1 : 0;
      if (typeof raw === "number") return raw === 0 ? 0 : raw === 1 ? 1 : null;
      if (typeof raw === "string") {
        const v = raw.trim().toLowerCase();
        if (v === "0" || v === "false") return 0;
        if (v === "1" || v === "true") return 1;
      }
      return null;
    };
    if (body.notifyId !== undefined) {
      const notifyId = parseStrictID(body.notifyId);
      const enable = parseEnable(body.enable);
      if (notifyId === null || enable === null) {
        return fail("缺少必要参数");
      }
      if (!mockStore.toggleNotification(notifyId, enable)) {
        return fail("通知配置不存在");
      }
    } else if (body.notify !== undefined) {
      const notify = recordBody(body.notify);
      if (!notify) return fail("缺少必要参数");
      const id = parseStrictID(notify.id);
      if (id === null) {
        return fail("缺少必要参数");
      }
      if (!mockStore.updateNotification(notify)) {
        return fail("通知配置不存在");
      }
    } else {
      return fail("缺少必要参数");
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
