import type {
  AlistItem,
  CurrentTaskData,
  JobItem,
  NotifyItem,
  PageData,
  RealtimeTaskItemPage,
  SystemSettings,
  TaskItem,
  TaskRecord,
} from "../types";
import {
  createDemoTaskItems,
  createDemoTaskRecords,
  createDemoTaskView,
  pageDemoRows,
} from "./fixtures/tasks";

type JobInput = Record<string, unknown>;
type EngineInput = Record<string, unknown>;
type NotifyInput = Record<string, unknown>;

export type MockStoreSeed = {
  engines?: EngineInput[];
  jobs?: JobInput[];
  notifications?: NotifyInput[];
  settings?: Partial<SystemSettings>;
  history?: Record<number, Partial<TaskRecord>[]>;
};

// Mirrors backend/internal/config/config.go defaults; mock mode must not show
// different defaults than a real install.
const defaultSettings: SystemSettings = {
  taskTimeout: 48,
  taskSave: 30,
  copyConcurrency: 5,
  scanConcurrency: 16,
  maxRetries: 2,
};

const defaultPaths: Record<string, { name: string }[]> = {
  "/": [{ name: "Photos" }, { name: "Documents" }, { name: "Backup" }],
  "/Photos": [{ name: "Albums" }, { name: "Raw" }, { name: "Exports" }],
  "/Photos/Albums": [{ name: "2024" }, { name: "2025" }, { name: "2026" }],
  "/Photos/Raw": [{ name: "Camera-A" }, { name: "Camera-B" }],
  "/Documents": [{ name: "Reports" }, { name: "Contracts" }],
  "/Documents/Reports": [{ name: "2026-Q1.pdf" }, { name: "2026-Q2.pdf" }],
  "/Backup": [{ name: "Nightly" }, { name: "Weekly" }],
};

const now = () => Math.floor(Date.now() / 1000);

function numberValue(value: unknown, fallback: number): number {
  const result = Number(value);
  return Number.isFinite(result) ? result : fallback;
}

function stringValue(value: unknown, fallback = ""): string {
  return value === undefined || value === null ? fallback : String(value);
}

function parsePathList(value: unknown): string[] {
  if (Array.isArray(value)) return value.map(String);
  if (typeof value === "string") {
    try {
      const parsed: unknown = JSON.parse(value);
      if (Array.isArray(parsed)) return parsed.map(String);
    } catch {
      // Treat an old-style plain path as a one-item path list.
    }
    return value ? [value] : [];
  }
  return [];
}

function toPathJson(value: unknown): string {
  return JSON.stringify(parsePathList(value));
}

function normalizeEngine(
  input: EngineInput,
  id: number,
  previous?: AlistItem,
): AlistItem {
  const merged: Record<string, unknown> = {
    ...(previous as Record<string, unknown> | undefined),
    ...input,
  };
  return {
    id: numberValue(merged.id, id),
    remark: merged.remark == null ? null : stringValue(merged.remark),
    url: stringValue(merged.url, "https://mock-engine.test"),
    userName: stringValue(merged.userName, "mock-user"),
    createTime: numberValue(merged.createTime, now()),
  };
}

function normalizeJob(input: JobInput, id: number, previous?: JobItem): JobItem {
  const merged: Record<string, unknown> = {
    ...(previous as unknown as Record<string, unknown> | undefined),
    ...input,
  };
  return {
    id: numberValue(merged.id, id),
    enable: merged.enable === true ? 1 : numberValue(merged.enable, 1),
    remark: merged.remark == null ? null : stringValue(merged.remark),
    srcPath: toPathJson(merged.srcPath),
    dstPath: toPathJson(merged.dstPath),
    alistId: numberValue(merged.alistId, 1),
    useCacheT: merged.useCacheT === true ? 1 : numberValue(merged.useCacheT, 0),
    scanIntervalT: numberValue(merged.scanIntervalT, 0),
    useCacheS: merged.useCacheS === true ? 1 : numberValue(merged.useCacheS, 0),
    scanIntervalS: numberValue(merged.scanIntervalS, 0),
    method: numberValue(merged.method, 0),
    interval: numberValue(merged.interval, 1440),
    isCron: numberValue(merged.isCron, 2),
    month: merged.month == null ? "*" : stringValue(merged.month),
    day: merged.day == null ? "*" : stringValue(merged.day),
    day_of_week:
      merged.day_of_week == null ? "*" : stringValue(merged.day_of_week),
    hour: merged.hour == null ? "2" : stringValue(merged.hour),
    minute: merged.minute == null ? "0" : stringValue(merged.minute),
    second: merged.second == null ? "0" : stringValue(merged.second),
    exclude: merged.exclude == null ? null : stringValue(merged.exclude),
    minFileSize: numberValue(merged.minFileSize, 0),
    maxFileSize: numberValue(merged.maxFileSize, 0),
    createTime: numberValue(merged.createTime, now()),
  };
}

function normalizeNotify(input: NotifyInput, id: number, previous?: NotifyItem): NotifyItem {
  const merged: Record<string, unknown> = {
    ...(previous as unknown as Record<string, unknown> | undefined),
    ...input,
  };
  const params =
    typeof merged.params === "string"
      ? merged.params
      : JSON.stringify(merged.params || {});
  const lastSendError =
    typeof merged.lastSendError === "string" ? merged.lastSendError : null;
  return {
    id: numberValue(merged.id, id),
    enable: merged.enable === true ? 1 : numberValue(merged.enable, 1),
    method: numberValue(merged.method, 0),
    params,
    // 保留投递结果，界面才能在 mock 模式下也显示"最近一次发送失败"。
    lastSendStatus: numberValue(merged.lastSendStatus, 0),
    lastSendTime: numberValue(merged.lastSendTime, 0),
    lastSendError,
    createTime: numberValue(merged.createTime, now()),
  };
}

function includesStatus(status: number, requested: number): boolean {
  return requested === -1
    ? ![0, 1, 2, 7].includes(status)
    : status === requested;
}


const notifyRedactionMarker = "****";
const notifyBodyRedacted = "******";

/** Same key set as backend notifySecretKeys (service/notify_service.go). */
const notifySecretKeys: Record<number, string[]> = {
  0: ["url", "headers", "body"],
  1: ["sendKey"],
  2: ["url", "webhook", "secret"],
  3: ["corpsecret", "corpSecret"],
  4: ["url", "webhook", "secret"],
  5: ["deviceKey"],
};

function maskSecretValue(value: string): string {
  return value.length <= 8 ? notifyRedactionMarker : notifyRedactionMarker + value.slice(-4);
}

/** Keeps the host visible while masking credentials in query/path (like backend). */
function maskNotifyUrl(rawUrl: string): string {
  try {
    const url = new URL(rawUrl);
    if (url.username) url.username = notifyRedactionMarker;
    if (url.password) url.password = notifyRedactionMarker;
    if (url.search) {
      const pairs = url.search.slice(1).split("&").map((pair) => {
        const [name] = pair.split("=");
        return `${name}=${notifyRedactionMarker}`;
      });
      url.search = `?${pairs.join("&")}`;
    }
    const segments = url.pathname.split("/");
    const last = segments[segments.length - 1];
    if (last && last.length >= 12) {
      segments[segments.length - 1] = maskSecretValue(last);
      url.pathname = segments.join("/");
    }
    return url.toString();
  } catch {
    return maskSecretValue(rawUrl);
  }
}

function isMaskedNotifyValue(value: string): boolean {
  return value.includes(notifyRedactionMarker);
}

function redactNotifyParams(method: number, paramsStr: string): string {
  let params: Record<string, unknown>;
  try {
    const parsed: unknown = JSON.parse(paramsStr || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return "{}";
    params = { ...(parsed as Record<string, unknown>) };
  } catch {
    // Fail closed like the backend: a malformed row may still contain a token.
    return "{}";
  }
  const keys =
    notifySecretKeys[method] ?? Object.values(notifySecretKeys).flat();
  for (const key of keys) {
    const value = params[key];
    if (value === undefined || value === null || value === "") continue;
    if (key === "headers") {
      params[key] = notifyRedactionMarker;
      continue;
    }
    const text = typeof value === "string" ? value : "";
    if (key === "body") params[key] = notifyBodyRedacted;
    else if (key === "url" || key === "webhook" || key === "server")
      params[key] = maskNotifyUrl(text);
    else params[key] = maskSecretValue(text);
  }
  return JSON.stringify(params);
}

function notifyParamsRecord(value: unknown): Record<string, unknown> | null {
  if (typeof value === "string") {
    try {
      const parsed: unknown = JSON.parse(value || "{}");
      return parsed && typeof parsed === "object" && !Array.isArray(parsed)
        ? ({ ...(parsed as Record<string, unknown>) })
        : null;
    } catch {
      return null;
    }
  }
  if (value && typeof value === "object" && !Array.isArray(value))
    return { ...(value as Record<string, unknown>) };
  return null;
}

/** Restores redacted/blank secrets from the stored config on edit (backend resolveNotifyParams). */
function resolveNotifyInput(
  input: NotifyInput,
  previous: NotifyItem,
): NotifyInput {
  const incoming = notifyParamsRecord(input.params);
  const stored = notifyParamsRecord(previous.params);
  if (!incoming || !stored) return input;
  const method = numberValue(input.method, previous.method);
  const keys =
    notifySecretKeys[method] ?? Object.values(notifySecretKeys).flat();
  for (const key of keys) {
    const value = incoming[key];
    if (value === undefined || value === null) {
      if (stored[key] !== undefined) incoming[key] = stored[key];
      continue;
    }
    if (typeof value !== "string") continue;
    if (key === "secret" && value === "") continue; // explicit "disable signing"
    if (value === "" || isMaskedNotifyValue(value)) {
      if (stored[key] !== undefined) incoming[key] = stored[key];
    }
  }
  return { ...input, params: JSON.stringify(incoming) };
}

export class MockStore {
  private engines: AlistItem[];
  private jobs: JobItem[];
  private notifications: NotifyItem[];
  private settings: SystemSettings;
  private histories = new Map<number, TaskRecord[]>();
  private currentTasks = new Map<number, CurrentTaskData>();
  private currentItems = new Map<number, TaskItem[]>();
  private taskItems = new Map<number, TaskItem[]>();
  private nextEngineId: number;
  private nextJobId: number;
  private nextNotifyId: number;
  private nextTaskId = 7000;

  constructor(seed: MockStoreSeed = {}) {
    const seededEngines = seed.engines ?? [
      {
        id: 1,
        remark: "本地测试引擎",
        url: "https://mock-engine.test",
        userName: "mock-user",
      },
    ];
    this.engines = seededEngines.map((item, index) =>
      normalizeEngine(item, index + 1),
    );
    this.nextEngineId = Math.max(0, ...this.engines.map((item) => item.id)) + 1;

    const seededJobs = seed.jobs ?? [
      {
        id: 1,
        enable: 1,
        remark: "示例同步任务",
        srcPath: ["/Photos"],
        dstPath: ["/Backup/Nightly"],
        alistId: this.engines[0]?.id || 1,
        method: 0,
        interval: 1440,
        isCron: 2,
      },
    ];
    this.jobs = seededJobs.map((item, index) => normalizeJob(item, index + 1));
    this.nextJobId = Math.max(0, ...this.jobs.map((item) => item.id)) + 1;

    const seededNotifications = seed.notifications ?? [
      {
        id: 1,
        enable: 1,
        method: 0,
        params: {
          url: "https://webhook.mock.test/notify",
          httpMethod: "POST",
          contentType: "application/json",
          needContent: true,
          titleName: "title",
          contentName: "content",
          notSendNull: false,
        },
      },
    ];
    this.notifications = seededNotifications.map((item, index) =>
      normalizeNotify(item, index + 1),
    );
    this.nextNotifyId =
      Math.max(0, ...this.notifications.map((item) => item.id)) + 1;
    this.settings = { ...defaultSettings, ...seed.settings };

    if (seed.history) {
      for (const [jobId, records] of Object.entries(seed.history)) {
        this.histories.set(
          Number(jobId),
          records.map((record, index) => ({
            id: numberValue(record.id, 9001 + index),
            status: numberValue(record.status, 2),
            errMsg: record.errMsg,
            runTime: numberValue(record.runTime, now()),
            successNum: numberValue(record.successNum, 0),
            failNum: numberValue(record.failNum, 0),
            allNum: numberValue(record.allNum, 0),
            createTime: numberValue(record.createTime, now()),
          })),
        );
      }
    } else if (seed.jobs === undefined && this.jobs.length > 0) {
      this.histories.set(this.jobs[0].id, createDemoTaskRecords());
    }

    if (seed.jobs === undefined && this.jobs[0]) this.runJob(this.jobs[0].id);
  }

  listEngines(): AlistItem[] {
    return this.engines.map((engine) => ({ ...engine }));
  }

  // The real handler (service.GetChildPath -> AlistClient.FilePathList) returns
  // [{"path": "<name>"}]; the frontend tolerates both shapes, so mock mode
  // should exercise the real one.
  listPaths(path: string): { path: string }[] {
    return (defaultPaths[path] || []).map((item) => ({ path: item.name }));
  }

  saveEngine(input: EngineInput): AlistItem {
    const requestedId = input.id === undefined ? undefined : numberValue(input.id, 0);
    const index = requestedId
      ? this.engines.findIndex((engine) => engine.id === requestedId)
      : -1;
    if (index >= 0) {
      const updated = normalizeEngine(input, requestedId!, this.engines[index]);
      this.engines[index] = updated;
      return { ...updated };
    }
    const created = normalizeEngine(input, this.nextEngineId++);
    this.engines.push(created);
    return { ...created };
  }

  deleteEngine(id: number): void {
    this.engines = this.engines.filter((engine) => engine.id !== id);
  }

  listJobs(pageNum = 1, pageSize = 500): PageData<JobItem> {
    return {
      dataList: pageDemoRows(this.jobs, pageNum, pageSize).map((job) => ({
        ...job,
      })),
      count: this.jobs.length,
    };
  }

  saveJob(input: JobInput): JobItem {
    const requestedId = input.id === undefined ? undefined : numberValue(input.id, 0);
    const index = requestedId
      ? this.jobs.findIndex((job) => job.id === requestedId)
      : -1;
    if (index >= 0) {
      const updated = normalizeJob(input, requestedId!, this.jobs[index]);
      this.jobs[index] = updated;
      return { ...updated };
    }
    const created = normalizeJob(input, this.nextJobId++);
    this.jobs.push(created);
    return { ...created };
  }

  deleteJob(id: number): void {
    this.jobs = this.jobs.filter((job) => job.id !== id);
    this.currentTasks.delete(id);
    this.currentItems.delete(id);
    this.histories.delete(id);
  }

  /** Removes one finished task record, mirroring service.RemoveTask. */
  removeTaskRecord(taskId: number): "deleted" | "running" | "missing" {
    for (const [jobId, records] of this.histories) {
      const index = records.findIndex((record) => record.id === taskId);
      if (index < 0) continue;
      if (this.currentTasks.get(jobId)?.taskId === taskId) return "running";
      records.splice(index, 1);
      this.taskItems.delete(taskId);
      return "deleted";
    }
    return "missing";
  }

  runJob(jobId: number): CurrentTaskData {
    const items = createDemoTaskItems();
    const base = createDemoTaskView(now(), items);
    const taskId = this.nextTaskId++;
    const task = { ...base, taskId };
    this.currentTasks.set(jobId, task);
    this.currentItems.set(jobId, items);
    this.taskItems.set(taskId, items);
    return { ...task };
  }

  getCurrentTask(jobId: number): CurrentTaskData | null {
    const task = this.currentTasks.get(jobId);
    return task ? { ...task } : null;
  }

  /** Marks a running task as aborted and keeps a history record (status 4). */
  private finishCurrentTask(
    jobId: number,
    status = 4,
    taskId?: number,
  ): void {
    const task = this.currentTasks.get(jobId);
    if (!task) return;
    this.currentTasks.delete(jobId);
    this.currentItems.delete(jobId);
    const records = this.histories.get(jobId) || [];
    records.unshift({
      id: taskId ?? task.taskId,
      status,
      createTime: task.createTime,
      runTime: now(),
      successNum: task.num.success,
      failNum: task.num.fail,
      allNum: Object.values(task.num).reduce((sum, value) => sum + value, 0),
    });
    this.histories.set(jobId, records);
  }

  stopTask(taskId: number): void {
    for (const [jobId, task] of this.currentTasks) {
      if (task.taskId === taskId) {
        this.finishCurrentTask(jobId, 4, taskId);
        return;
      }
    }
  }

  /** AbortJob: stop the running task but keep the job enabled. */
  abortJob(jobId: number): void {
    this.finishCurrentTask(jobId);
  }

  /** PauseJob: disable the schedule and abort the running task. */
  pauseJob(jobId: number): void {
    const job = this.jobs.find((item) => item.id === jobId);
    if (job) job.enable = 0;
    this.finishCurrentTask(jobId);
  }

  retryTask(taskId: number): void {
    // Retry applies to a *history* record, so it must not depend on another
    // task currently running (the old loop body never executed with an empty
    // currentTasks map, making retry a no-op).
    for (const [jobId, records] of this.histories) {
      if (!records.some((record) => record.id === taskId)) continue;
      if (this.currentTasks.has(jobId)) return; // backend rejects while busy
      const job = this.jobs.find((item) => item.id === jobId);
      if (!job || job.enable !== 1) return;
      this.runJob(jobId);
      return;
    }
  }

  updateJobAction(input: {
    id?: number | string;
    taskId?: number | string;
    action?: string;
    pause?: boolean;
    abort?: boolean;
  }): void {
    if (input.taskId !== undefined) {
      const taskId = numberValue(input.taskId, 0);
      if (input.action === "stop") this.stopTask(taskId);
      if (input.action === "retry") this.retryTask(taskId);
      return;
    }
    const jobId = numberValue(input.id, 0);
    const job = this.jobs.find((item) => item.id === jobId);
    if (!job) return;
    if (input.pause === true) {
      // AbortJob keeps the schedule enabled; PauseJob disables it. Both stop
      // the running task and leave an aborted history row, unlike the old mock
      // which silently deleted the task.
      if (input.abort === true) this.abortJob(jobId);
      else this.pauseJob(jobId);
      return;
    }
    if (input.pause === false) {
      job.enable = 1;
      return;
    }
    this.runJob(jobId);
  }

  listHistory(
    jobId: number,
    params: {
      pageNum?: number;
      pageSize?: number;
      status?: number;
      statusIn?: number[];
      keyword?: string;
      startTime?: number;
      endTimeExclusive?: number;
    } = {},
  ): PageData<TaskRecord> {
    let records = [...(this.histories.get(jobId) || [])];
    if (params.status !== undefined)
      records = records.filter((record) => record.status === params.status);
    if (params.statusIn?.length)
      records = records.filter((record) => params.statusIn!.includes(record.status));
    if (params.keyword) {
      // matches backend mapper.GetJobTaskList: the job-task keyword filter only
      // searches the task id (item-level search is a different endpoint).
      const keyword = params.keyword.toLowerCase();
      records = records.filter((record) =>
        String(record.id).toLowerCase().includes(keyword),
      );
    }
    if (params.startTime !== undefined)
      records = records.filter((record) => (record.createTime || 0) >= params.startTime!);
    if (params.endTimeExclusive !== undefined)
      records = records.filter(
        (record) => (record.createTime || 0) < params.endTimeExclusive!,
      );
    return {
      dataList: pageDemoRows(
        records,
        params.pageNum || 1,
        params.pageSize || 20,
      ),
      count: records.length,
    };
  }

  listCurrentItems(
    jobId: number,
    params: {
      taskId?: number;
      createTime?: number;
      status?: number;
      pageNum?: number;
      pageSize?: number;
    } = {},
  ): RealtimeTaskItemPage {
    const task = this.currentTasks.get(jobId);
    const pageNum = params.pageNum || 1;
    const pageSize = params.pageSize || 20;
    const stale =
      !task ||
      (params.taskId !== undefined && task.taskId !== params.taskId) ||
      (params.createTime !== undefined && task.createTime !== params.createTime);
    const items = stale
      ? []
      : (this.currentItems.get(jobId) || []).filter((item) =>
          params.status === undefined ? true : includesStatus(item.status, params.status),
        );
    return {
      taskId: params.taskId || task?.taskId || 0,
      createTime: params.createTime || task?.createTime || 0,
      status: params.status || 0,
      pageNum,
      pageSize,
      stale,
      dataList: pageDemoRows(items, pageNum, pageSize),
      count: items.length,
    };
  }

  listTaskItems(
    taskId: number,
    params: {
      pageNum?: number;
      pageSize?: number;
      status?: number;
      type?: number;
      isPath?: number;
      hasError?: number;
      keyword?: string;
    } = {},
  ): PageData<TaskItem> {
    let items = this.taskItems.get(taskId);
    if (!items) {
      items = createDemoTaskItems();
      this.taskItems.set(taskId, items);
    }
    items = items.filter((item) => {
      if (params.status !== undefined && !includesStatus(item.status, params.status))
        return false;
      if (params.type !== undefined && item.type !== params.type) return false;
      if (params.isPath !== undefined && item.isPath !== params.isPath) return false;
      if (params.hasError === 1 && !item.errMsg) return false;
      if (params.hasError === 0 && item.errMsg) return false;
      if (params.keyword) {
        const keyword = params.keyword.toLowerCase();
        if (
          !`${item.fileName || ""} ${item.srcPath || ""} ${item.dstPath || ""} ${
            item.errMsg || ""
          }`
            .toLowerCase()
            .includes(keyword)
        )
          return false;
      }
      return true;
    });
    return {
      dataList: pageDemoRows(items, params.pageNum || 1, params.pageSize || 20),
      count: items.length,
    };
  }

  /** Raw (unredacted) params for tests that assert the redaction round-trip. */
  getRawNotificationParams(id: number): string {
    return this.notifications.find((item) => item.id === id)?.params ?? "";
  }

  listNotifications(): NotifyItem[] {
    // Mirrors service.GetNotifyList: secrets never leave the backend, the UI
    // gets redaction markers and round-trips them on save.
    return this.notifications.map((item) => ({
      ...item,
      params: redactNotifyParams(item.method, item.params),
    }));
  }

  saveNotification(input: NotifyInput): NotifyItem {
    const requestedId =
      input.id === undefined ? undefined : numberValue(input.id, 0);
    const index = requestedId
      ? this.notifications.findIndex((item) => item.id === requestedId)
      : -1;
    if (index >= 0) {
      const previous = this.notifications[index];
      const resolved = resolveNotifyInput(input, previous);
      const updated = normalizeNotify(resolved, requestedId!, previous);
      this.notifications[index] = updated;
      return { ...updated };
    }
    const created = normalizeNotify(input, this.nextNotifyId++);
    this.notifications.push(created);
    return { ...created };
  }

  toggleNotification(id: number, enable: number): void {
    const item = this.notifications.find((notify) => notify.id === id);
    if (item) item.enable = enable ? 1 : 0;
  }

  deleteNotification(id: number): void {
    this.notifications = this.notifications.filter((item) => item.id !== id);
  }

  getSettings(): SystemSettings {
    return { ...this.settings };
  }

  updateSettings(settings: Partial<SystemSettings>): SystemSettings {
    this.settings = { ...this.settings, ...settings };
    return this.getSettings();
  }
}

export function createMockStore(seed?: MockStoreSeed): MockStore {
  return new MockStore(seed);
}
