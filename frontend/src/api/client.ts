import type {
  ApiResponse,
  AlistItem,
  AppLogsResponse,
  BatchJobActionResult,
  CurrentTaskData,
  DashboardOverview,
  DryRunResult,
  EngineHealth,
  FilterTestResult,
  JobItem,
  NotifyItem,
  PageData,
  SystemBackupPayload,
  SystemImportSummary,
  SystemSettings,
  TaskItem,
  TaskRecord,
} from "../types";

export const apiBase = "/app/opensync/svr";
export type LatestVersion = {
  latestVersion: string;
  releaseURL: string;
  hasUpdate: boolean;
};
export function serializeParams(params: Record<string, unknown>) {
  const result = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    for (const item of Array.isArray(value) ? value : [value])
      if (item !== undefined && item !== null && item !== "")
        result.append(key, String(item));
  }
  return result.toString();
}

export const requestTimeoutMs = 90000;
export const timeoutMessage = "请求超时，请重试";
export const sessionExpiredEvent = "opensync:session-expired";
export const sessionExpiredMessage = "登录已失效，请从飞牛桌面重新打开应用";

/**
 * Combines abort signals without relying on AbortSignal.any (Chrome 116+ /
 * Safari 17.4+). Vite does not polyfill runtime APIs, so older fnOS WebViews
 * threw a TypeError on every request. `cleanup` detaches the fallback
 * listeners and must run once the request settles.
 */
export function combineSignals(
  signals: (AbortSignal | undefined)[],
): { signal: AbortSignal; cleanup: () => void } {
  const list = signals.filter((s): s is AbortSignal => !!s);
  if (list.length <= 1)
    return {
      signal: list[0] ?? new AbortController().signal,
      cleanup: () => {},
    };
  const any = (
    AbortSignal as { any?: (signals: AbortSignal[]) => AbortSignal }
  ).any;
  if (typeof any === "function")
    return { signal: any.call(AbortSignal, list), cleanup: () => {} };
  const controller = new AbortController();
  const listeners: [AbortSignal, () => void][] = [];
  const cleanup = () => {
    for (const [signal, listener] of listeners)
      signal.removeEventListener("abort", listener);
    listeners.length = 0;
  };
  for (const signal of list) {
    if (signal.aborted) {
      cleanup();
      controller.abort(signal.reason);
      break;
    }
    const listener = () => {
      cleanup();
      controller.abort(signal.reason);
    };
    signal.addEventListener("abort", listener);
    listeners.push([signal, listener]);
  }
  return { signal: controller.signal, cleanup };
}

export async function request<T>(
  path: string,
  options: {
    method?: string;
    data?: unknown;
    params?: Record<string, unknown>;
    signal?: AbortSignal;
    cache?: RequestCache;
    /** Per-call timeout; falls back to requestTimeoutMs. */
    timeoutMs?: number;
  } = {},
): Promise<T> {
  // A manual timer instead of AbortSignal.timeout keeps older WebViews working
  // and lets the timeout be told apart from a caller's abort.
  const timeout = new AbortController();
  let timedOut = false;
  const timeoutMs = options.timeoutMs ?? requestTimeoutMs;
  const timer = setTimeout(() => {
    timedOut = true;
    timeout.abort();
  }, timeoutMs);
  const combined = combineSignals([options.signal, timeout.signal]);
  let response: Response;
  let result: ApiResponse<T> | null = null;
  try {
    response = await fetch(
      `${apiBase}${path}${options.params ? "?" + serializeParams(options.params) : ""}`,
      {
        method: options.method || "GET",
        credentials: "same-origin",
        cache: options.cache,
        signal: combined.signal,
        headers: { "Content-Type": "application/json" },
        ...(options.data !== undefined
          ? { body: JSON.stringify(options.data) }
          : {}),
      },
    );
    // A successful response without a body (204, or an empty 200) carries
    // no envelope to unwrap; it is a success, not a malformed reply.
    if (
      response.ok &&
      (response.status === 204 || response.headers?.get("Content-Length") === "0")
    ) {
      return undefined as T;
    }
    try {
      result = await response.json();
    } catch (err) {
      // An abort while the body streams in is not a malformed response.
      if (combined.signal.aborted) throw err;
    }
  } catch (err) {
    if (timedOut && !options.signal?.aborted) throw new Error(timeoutMessage);
    throw err;
  } finally {
    clearTimeout(timer);
    combined.cleanup();
  }
  // The status is checked before the body: a gateway can answer 401 with an
  // HTML page, which used to surface as "服务响应异常" and skip expiry handling.
  if (response.status === 401) {
    // /session is what the expiry listener re-requests. Dispatching for it too
    // made a 401 there re-trigger itself forever behind a spinner.
    if (path !== "/session")
      window.dispatchEvent(new CustomEvent(sessionExpiredEvent));
    throw new Error(result?.msg || sessionExpiredMessage);
  }
  if (!result)
    throw new Error(
      response.ok
        ? "服务响应异常，请重试"
        : `服务响应异常（HTTP ${response.status}），请重试`,
    );
  if (!response.ok || result.code !== 200)
    throw new Error(
      result.msg ||
        (response.ok ? "操作失败" : `操作失败（HTTP ${response.status}）`),
    );
  return result.data;
}

// 500 == backend/internal/mapper.MaxPageSize (mapper/db.go). The backend clamps
// larger values silently, so keep this in sync with that constant: a larger
// value here would silently stop after the first 500 rows.
async function fetchAllJobs() {
  const pageSize = 500;
  const first = await request<PageData<JobItem>>("/job", {
    params: { pageNum: 1, pageSize },
  });
  const dataList = [...first.dataList];
  const pageCount = Math.ceil(first.count / pageSize);
  for (let page = 2; page <= pageCount; page += 1) {
    const next = await request<PageData<JobItem>>("/job", {
      params: { pageNum: page, pageSize },
    });
    dataList.push(...next.dataList);
  }
  return { dataList, count: first.count };
}

// The shell sidebar and the Tasks page both load the full job list on mount
// and again on every `opensync:jobs-changed` event. Sharing one short-lived
// request (and the in-flight promise) removed the duplicate initial fetch
// without making the two lists diverge.
const jobsCacheTTL = 5000;
let jobsCache: { at: number; promise: ReturnType<typeof fetchAllJobs> } | null =
  null;

export function invalidateJobsCache() {
  jobsCache = null;
}

function requestAllJobs(_signal?: AbortSignal) {
  const now = Date.now();
  if (!jobsCache || now - jobsCache.at > jobsCacheTTL) {
    const promise = fetchAllJobs();
    const entry = { at: now, promise };
    jobsCache = entry;
    promise.catch(() => {
      if (jobsCache === entry) jobsCache = null;
    });
  }
  return jobsCache.promise;
}

/**
 * Single shared request for the realtime progress endpoint
 * (`GET /svr/job?current=1`). It is used both by the shell/overview (snapshot
 * only) and by the realtime page (snapshot, status page or item page), so the
 * cache policy lives here instead of being repeated at each call site.
 */
export function requestCurrentTask<T>(
  params: Record<string, unknown>,
  signal?: AbortSignal,
) {
  return request<T>("/job", {
    params: { ...params, current: 1 },
    signal,
    cache: "no-store",
  });
}

export function buildApiUrl(path: string, params?: Record<string, unknown>) {
  const query = params ? serializeParams(params) : "";
  return `${apiBase}${path}${query ? `?${query}` : ""}`;
}

export const api = {
  session: (signal?: AbortSignal) =>
    request<{ uid: number; development: boolean; version: string }>("/session", {
      signal,
    }),
  latestVersion: (signal?: AbortSignal, refresh = false) =>
    // refresh=1 makes the backend ask GitHub instead of answering from its
    // 30 minute cache; the version entry's "检查更新" button uses it.
    request<LatestVersion>("/version/latest", {
      params: refresh ? { refresh: 1 } : undefined,
      signal,
    }),
  dashboard: (signal?: AbortSignal) =>
    request<DashboardOverview>("/dashboard", { signal, cache: "no-store" }),
  engines: (signal?: AbortSignal) => request<AlistItem[]>("/alist", { signal }),
  engineHealth: (params?: { id?: number; refresh?: number }, signal?: AbortSignal) =>
    request<EngineHealth | EngineHealth[]>("/alist/health", {
      params: params as Record<string, unknown> | undefined,
      signal,
      cache: "no-store",
    }),
  paths: (alistId: number, path: string, signal?: AbortSignal) =>
    request<{ name?: string; path?: string }[]>("/alist", {
      params: { alistId, path },
      signal,
    }),
  saveEngine: (data: unknown, edit: boolean) =>
    request("/alist", { method: edit ? "PUT" : "POST", data }),
  deleteEngine: (id: number) =>
    request("/alist", { method: "DELETE", params: { id } }),
  testEngine: (id: number) =>
    request<EngineHealth | null>("/alist/test", { method: "POST", params: { id } }),
  jobMenu: (signal?: AbortSignal) => requestAllJobs(signal),
  saveJob: (data: unknown, signal?: AbortSignal) =>
    request<{ id?: number } | null>("/job", { method: "POST", data, signal }).then(
      (result) => {
        invalidateJobsCache();
        return result;
      },
    ),
  jobAction: (data: unknown) =>
    request("/job", { method: "PUT", data }).then((result) => {
      invalidateJobsCache();
      return result;
    }),
  batchJobAction: (
    action: "run" | "stop" | "enable" | "disable" | "run_all" | "stop_all",
    ids?: number[],
  ) =>
    request<BatchJobActionResult>("/job/batch", {
      method: "POST",
      data: ids ? { action, ids } : { action },
    }).then((result) => {
      invalidateJobsCache();
      return result;
    }),
  // RemoveJobClient waits up to 2 minutes for a running task to stop before it
  // deletes the row (backend/internal/service/job_service.go), so this call
  // must outlive the default 90s request timeout.
  deleteJob: (id: number) =>
    request("/job", { method: "DELETE", params: { id }, timeoutMs: 130000 }).then(
      (result) => {
        invalidateJobsCache();
        return result;
      },
    ),
  current: (id: number, signal?: AbortSignal) =>
    requestCurrentTask<CurrentTaskData | null>({ id }, signal),
  history: (
    id: number,
    params: Record<string, unknown>,
    signal?: AbortSignal,
  ) =>
    request<PageData<TaskRecord>>("/job", {
      params: { id, statusIn: [2, 3, 4, 5, 6, 7, 8], ...params },
      signal,
    }),
  items: (
    taskId: number | string,
    params: Record<string, unknown>,
    signal?: AbortSignal,
  ) =>
    request<PageData<TaskItem>>("/job", {
      params: { taskId, ...params },
      signal,
    }),
  exportTaskItemsUrl: (
    taskId: number | string,
    format: "csv" | "txt",
    params: Record<string, unknown> = {},
  ) => buildApiUrl("/job/items/export", { taskId, format, ...params }),
  taskAction: (
    taskId: number,
    action: "stop" | "retry" | "approve" | "reject",
    itemIds?: number[],
  ) =>
    request("/job", {
      method: "PUT",
      data: {
        taskId: String(taskId),
        action,
        ...(itemIds && itemIds.length > 0 ? { itemIds } : {}),
      },
    }),
  deleteTask: (taskId: number) =>
    request("/job", { method: "DELETE", params: { taskId } }),
  deleteTasksBatch: (taskIds: number[]) =>
    request<{ deleted: number }>("/job/tasks/delete", {
      method: "POST",
      data: { taskIds },
    }),
  clearJobHistory: (jobId: number) =>
    request<{ deleted: number }>("/job/tasks/delete", {
      method: "POST",
      data: { clearJobId: jobId },
    }),
  startDryRun: (id: number) =>
    request<DryRunResult>("/job/dry-run", { method: "POST", data: { id } }),
  getDryRun: (id: number, signal?: AbortSignal) =>
    request<DryRunResult | null>("/job/dry-run", {
      params: { id },
      signal,
      cache: "no-store",
    }),
  cancelDryRun: (id: number) =>
    request("/job/dry-run", { method: "DELETE", params: { id } }),
  testJobFilter: (data: Record<string, unknown>) =>
    request<FilterTestResult>("/job/filter/test", { method: "POST", data }),
  notifications: (signal?: AbortSignal) =>
    request<NotifyItem[]>("/notify", { signal }),
  saveNotification: (notify: unknown, edit: boolean) =>
    request("/notify", { method: edit ? "PUT" : "POST", data: { notify } }),
  testNotification: (notify: unknown) =>
    request("/notify/test", { method: "POST", data: { notify } }),
  toggleNotification: (notifyId: number, enable: number) =>
    request("/notify", { method: "PUT", data: { notifyId, enable } }),
  deleteNotification: (notifyId: number) =>
    request("/notify", { method: "DELETE", params: { notifyId } }),
  settings: (signal?: AbortSignal) =>
    request<SystemSettings>("/system/config", { signal }),
  saveSettings: (data: SystemSettings) =>
    request<SystemSettings>("/system/config", { method: "PUT", data }),
  exportConfig: (includeSecrets: boolean) =>
    request<SystemBackupPayload>("/system/export", {
      params: { includeSecrets: includeSecrets ? 1 : 0 },
      cache: "no-store",
    }),
  exportConfigUrl: (includeSecrets: boolean) =>
    buildApiUrl("/system/export", {
      includeSecrets: includeSecrets ? 1 : 0,
      download: 1,
    }),
  importConfig: (payload: SystemBackupPayload, conflictMode: "skip" | "overwrite") =>
    request<SystemImportSummary>("/system/import", {
      method: "POST",
      data: { conflictMode, payload },
    }),
  systemLogs: (
    params: { lines?: number; level?: string; keyword?: string },
    signal?: AbortSignal,
  ) =>
    request<AppLogsResponse>("/system/logs", {
      params: params as Record<string, unknown>,
      signal,
      cache: "no-store",
    }),
  downloadSystemLogsUrl: (params: {
    lines?: number;
    level?: string;
    keyword?: string;
  }) => buildApiUrl("/system/logs/download", params as Record<string, unknown>),
};
