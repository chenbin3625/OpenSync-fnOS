import type {
  ApiResponse,
  AlistItem,
  JobItem,
  NotifyItem,
  PageData,
  CurrentTaskData,
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
async function requestAllJobs(signal?: AbortSignal) {
  const pageSize = 500;
  const first = await request<PageData<JobItem>>("/job", {
    params: { pageNum: 1, pageSize },
    signal,
  });
  const dataList = [...first.dataList];
  const pageCount = Math.ceil(first.count / pageSize);
  for (let page = 2; page <= pageCount; page += 1) {
    const next = await request<PageData<JobItem>>("/job", {
      params: { pageNum: page, pageSize },
      signal,
    });
    dataList.push(...next.dataList);
  }
  return { dataList, count: first.count };
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
  engines: (signal?: AbortSignal) => request<AlistItem[]>("/alist", { signal }),
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
    request("/alist/test", { method: "POST", params: { id } }),
  jobMenu: (signal?: AbortSignal) => requestAllJobs(signal),
  saveJob: (data: unknown, signal?: AbortSignal) =>
    request("/job", { method: "POST", data, signal }),
  jobAction: (data: unknown) => request("/job", { method: "PUT", data }),
  // RemoveJobClient waits up to 2 minutes for a running task to stop before it
  // deletes the row (backend/internal/service/job_service.go), so this call
  // must outlive the default 90s request timeout.
  deleteJob: (id: number) =>
    request("/job", { method: "DELETE", params: { id }, timeoutMs: 130000 }),
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
  taskAction: (taskId: number, action: "stop" | "retry") =>
    request("/job", {
      method: "PUT",
      data: { taskId: String(taskId), action },
    }),
  deleteTask: (taskId: number) =>
    request("/job", { method: "DELETE", params: { taskId } }),
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
};
