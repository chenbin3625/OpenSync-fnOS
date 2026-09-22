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

export async function request<T>(
  path: string,
  options: {
    method?: string;
    data?: unknown;
    params?: Record<string, unknown>;
    signal?: AbortSignal;
    cache?: RequestCache;
  } = {},
): Promise<T> {
  const timeout = AbortSignal.timeout(90000);
  let response: Response;
  response = await fetch(
    `${apiBase}${path}${options.params ? "?" + serializeParams(options.params) : ""}`,
    {
      method: options.method || "GET",
      credentials: "same-origin",
      cache: options.cache,
      signal: options.signal
        ? AbortSignal.any([options.signal, timeout])
        : timeout,
      headers: { "Content-Type": "application/json" },
      ...(options.data !== undefined
        ? { body: JSON.stringify(options.data) }
        : {}),
    },
  );
  let result: ApiResponse<T>;
  try {
    result = await response.json();
  } catch {
    throw new Error("服务响应异常，请重试");
  }
  if (!response.ok || result.code !== 200) {
    if (response.status === 401)
      window.dispatchEvent(new CustomEvent("opensync:session-expired"));
    throw new Error(result.msg || "操作失败");
  }
  warnIfTruncated(path, result.data);
  return result.data;
}

// The backend caps a list request that carries no pageNum/pageSize and marks the
// response `truncated`. Nothing read that flag, so such a response rendered as a
// complete list. Every caller in this module paginates, so this is a guard
// against a future one that forgets: it is a developer-facing error, not a user
// message, because the fix is to pass paging parameters.
export function warnIfTruncated(path: string, data: unknown): boolean {
  if (
    !data ||
    typeof data !== "object" ||
    !("truncated" in data) ||
    (data as PageData<unknown>).truncated !== true
  )
    return false;
  const page = data as PageData<unknown>;
  console.error(
    `[OpenSync] ${path} 返回了被截断的列表：共 ${page.count} 条，仅取回 ${page.dataList?.length ?? 0} 条。请求必须携带 pageNum/pageSize。`,
  );
  return true;
}

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

export const api = {
  session: () =>
    request<{ uid: number; development: boolean; version: string }>("/session"),
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
  deleteJob: (id: number) =>
    request("/job", { method: "DELETE", params: { id } }),
  current: (id: number, signal?: AbortSignal) =>
    request<CurrentTaskData | null>("/job", {
      params: { id, current: 1 },
      signal,
    }),
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
