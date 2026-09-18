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
  } = {},
): Promise<T> {
  const timeout = AbortSignal.timeout(90000);
  const response = await fetch(
    `${apiBase}${path}${options.params ? "?" + serializeParams(options.params) : ""}`,
    {
      method: options.method || "GET",
      credentials: "same-origin",
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
  return result.data;
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
  jobs: (page: number, signal?: AbortSignal, pageSize = 12) =>
    request<PageData<JobItem>>("/job", {
      params: { pageNum: page, pageSize },
      signal,
    }),
  jobMenu: (signal?: AbortSignal) =>
    request<PageData<JobItem>>("/job", {
      params: { pageNum: 1, pageSize: 100 },
      signal,
    }),
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
  currentItems: (
    id: number,
    status: number,
    page: number,
    signal?: AbortSignal,
  ) =>
    request<PageData<TaskItem> | TaskItem[]>("/job", {
      params: { id, current: 1, status, pageNum: page, pageSize: 20 },
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
