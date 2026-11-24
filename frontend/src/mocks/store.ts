import type {
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
  RealtimeTaskItemPage,
  SystemBackupPayload,
  SystemImportSummary,
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
  scheduledFullSyncConfirm: 0,
  fullSyncApprovalTimeout: 30,
  engineHealthCheckInterval: 15,
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
    maxConcurrentJobs: Math.max(0, numberValue(merged.maxConcurrentJobs, 0)),
    activeJobs: numberValue(merged.activeJobs, 0),
    queuedJobs: numberValue(merged.queuedJobs, 0),
    createTime: numberValue(merged.createTime, now()),
  };
}

function normalizeJob(input: JobInput, id: number, previous?: JobItem): JobItem {
  const merged: Record<string, unknown> = {
    ...(previous as unknown as Record<string, unknown> | undefined),
    ...input,
  };
  const notifyIds = Array.isArray(merged.notifyIds)
    ? JSON.stringify(merged.notifyIds)
    : merged.notifyIds == null
      ? "[]"
      : stringValue(merged.notifyIds);
  return {
    id: numberValue(merged.id, id),
    enable: merged.enable === true ? 1 : numberValue(merged.enable, 1),
    remark: merged.remark == null ? null : stringValue(merged.remark),
    srcPath: toPathJson(merged.srcPath),
    dstPath: toPathJson(merged.dstPath),
    dstMap: merged.dstMap == null ? null : stringValue(merged.dstMap),
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
    compareMode: numberValue(merged.compareMode, 0),
    conflictPolicy: numberValue(merged.conflictPolicy, 0),
    trashEnable: merged.trashEnable === true ? 1 : numberValue(merged.trashEnable, 0),
    trashDir: merged.trashDir == null ? ".opensync-trash" : stringValue(merged.trashDir),
    trashRetentionDays: numberValue(merged.trashRetentionDays, 30),
    deleteEmptySrcDirs:
      merged.deleteEmptySrcDirs === true ? 1 : numberValue(merged.deleteEmptySrcDirs, 0),
    filterMode: numberValue(merged.filterMode, 0),
    include: merged.include == null ? null : stringValue(merged.include),
    minFileAgeMinutes: numberValue(merged.minFileAgeMinutes, 0),
    olderThanDays: numberValue(merged.olderThanDays, 0),
    newerThanDays: numberValue(merged.newerThanDays, 0),
    copyConcurrency: numberValue(merged.copyConcurrency, 0),
    scanConcurrency: numberValue(merged.scanConcurrency, 0),
    maxRetries: merged.maxRetries === undefined ? -1 : numberValue(merged.maxRetries, -1),
    windowEnable:
      merged.windowEnable === true ? 1 : numberValue(merged.windowEnable, 0),
    windowStart: merged.windowStart == null ? "00:00" : stringValue(merged.windowStart),
    windowEnd: merged.windowEnd == null ? "06:00" : stringValue(merged.windowEnd),
    windowStopOutside:
      merged.windowStopOutside === true ? 1 : numberValue(merged.windowStopOutside, 0),
    notifyMode: numberValue(merged.notifyMode, 0),
    notifyIds,
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
const defaultBarkServer = "https://api.day.app";

/** Same key set as backend notifySecretKeys (service/notify_service.go). */
const notifySecretKeys: Record<number, string[]> = {
  0: ["url", "headers", "body"],
  1: ["sendKey"],
  2: ["url", "webhook", "secret"],
  3: ["corpsecret", "corpSecret"],
  4: ["url", "webhook", "secret"],
  5: ["server", "deviceKey"],
  6: ["botToken", "apiBase"],
  7: ["url", "webhook"],
  8: ["password"],
  9: ["server", "token"],
  10: ["server", "token"],
  11: ["token"],
};
const allNotifySecretKeys = [
  "url",
  "webhook",
  "headers",
  "body",
  "sendKey",
  "secret",
  "corpsecret",
  "corpSecret",
  "deviceKey",
  "server",
  "botToken",
  "apiBase",
  "password",
  "token",
];

function maskSecretValue(value: string): string {
  return value.length <= 8 ? notifyRedactionMarker : notifyRedactionMarker + value.slice(-4);
}

function maskNotifyQuery(rawQuery: string): string {
  return rawQuery
    .split("&")
    .map((pair) => {
      if (!pair) return pair;
      const eq = pair.indexOf("=");
      if (eq < 0) return pair;
      const name = pair.slice(0, eq);
      const value = pair.slice(eq + 1);
      if (!value) return pair;
      let decoded = value;
      try {
        decoded = decodeURIComponent(value.replace(/\+/g, " "));
      } catch {
        decoded = value;
      }
      const escaped = encodeURIComponent(maskSecretValue(decoded)).replace(
        /%2A/g,
        "*",
      );
      return `${name}=${escaped}`;
    })
    .join("&");
}

/** Keeps the host visible while masking credentials in query/path (like backend). */
function maskNotifyUrl(rawUrl: string): string {
  try {
    const url = new URL(rawUrl);
    if (!url.hostname) return maskSecretValue(rawUrl);
    if (url.username || url.password) {
      url.username = notifyRedactionMarker;
      url.password = "";
    }
    const rawQuery = url.search.startsWith("?") ? url.search.slice(1) : "";
    const maskedQuery = rawQuery ? maskNotifyQuery(rawQuery) : "";
    const segments = url.pathname.split("/");
    const last = segments[segments.length - 1];
    if (last && last.length >= 12) {
      segments[segments.length - 1] = maskSecretValue(last);
      url.pathname = segments.join("/");
    }
    const hadTrailingSlash =
      rawUrl.split(/[?#]/)[0].endsWith("/") || url.pathname !== "/";
    const base = url.origin + (hadTrailingSlash ? url.pathname : "");
    return base + (maskedQuery ? `?${maskedQuery}` : "") + url.hash;
  } catch {
    return maskSecretValue(rawUrl);
  }
}

function isEmptyNotifyValue(value: unknown): boolean {
  if (value === null || value === undefined) return true;
  if (typeof value === "string") return value.trim() === "";
  if (typeof value === "object" && !Array.isArray(value)) {
    return Object.keys(value).length === 0;
  }
  return false;
}

function isMaskedNotifyValue(value: string): boolean {
  if (value.includes(notifyRedactionMarker)) return true;
  try {
    return decodeURIComponent(value.replace(/\+/g, " ")).includes(
      notifyRedactionMarker,
    );
  } catch {
    return false;
  }
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
  const keys = notifySecretKeys[method] ?? allNotifySecretKeys;
  for (const key of keys) {
    const value = params[key];
    if (value === undefined || value === null) continue;
    if (key === "headers") {
      if (isEmptyNotifyValue(value)) continue;
      params[key] = notifyRedactionMarker;
      continue;
    }
    if (typeof value !== "string") {
      params[key] = notifyRedactionMarker;
      continue;
    }
    if (value === "") continue;
    if (key === "body") params[key] = notifyBodyRedacted;
    else if (key === "url" || key === "webhook" || key === "server")
      params[key] = maskNotifyUrl(value);
    else params[key] = maskSecretValue(value);
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

function paramString(
  params: Record<string, unknown>,
  ...keys: string[]
): string {
  for (const key of keys) {
    const val = params[key];
    if (typeof val === "string" && val.trim()) return val.trim();
  }
  return "";
}

function withDefaultBarkServer(
  params: Record<string, unknown>,
): Record<string, unknown> {
  if (paramString(params, "server")) return params;
  return { ...params, server: defaultBarkServer };
}

function notifyTargetChanged(
  incoming: Record<string, unknown>,
  existing: Record<string, unknown>,
): boolean {
  const incomingURL = paramString(incoming, "url", "webhook", "server");
  if (incomingURL && !isMaskedNotifyValue(incomingURL)) {
    const existingURL = paramString(existing, "url", "webhook", "server");
    if (!existingURL || incomingURL !== existingURL) return true;
  }
  const incomingCorp = paramString(incoming, "corpid", "corpId");
  const existingCorp = paramString(existing, "corpid", "corpId");
  return Boolean(
    incomingCorp && existingCorp && incomingCorp !== existingCorp,
  );
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
  if (method !== previous.method) return input;
  const compareIncoming =
    method === 5 ? withDefaultBarkServer(incoming) : incoming;
  const compareStored =
    method === 5 ? withDefaultBarkServer(stored) : stored;
  if (notifyTargetChanged(compareIncoming, compareStored)) {
    return input;
  }
  const keys = notifySecretKeys[method] ?? [];
  for (const key of keys) {
    const value = incoming[key];
    if (value === undefined || value === null) {
      if (stored[key] !== undefined && stored[key] !== null) {
        incoming[key] = stored[key];
      }
      continue;
    }
    if (typeof value === "string") {
      if (key === "secret" && value === "") {
        delete incoming[key]; // explicit "disable signing"
        continue;
      }
      if (value === "" || isMaskedNotifyValue(value)) {
        if (stored[key] !== undefined && stored[key] !== null) {
          incoming[key] = stored[key];
        }
      }
      continue;
    }
    if (
      key === "headers" &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      Object.keys(value).length === 0
    ) {
      delete incoming[key];
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
  private dryRuns = new Map<number, DryRunResult>();
  private engineHealthMap = new Map<number, EngineHealth>();

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
    return this.engines.map((engine) => ({
      ...engine,
      activeJobs: [...this.currentTasks.keys()].filter((jobId) => {
        const job = this.jobs.find((j) => j.id === jobId);
        return job?.alistId === engine.id;
      }).length,
      queuedJobs: 0,
      health: this.getEngineHealth(engine.id),
    }));
  }

  getEngineHealth(id: number, refresh = false): EngineHealth {
    const existing = this.engineHealthMap.get(id);
    if (existing && !refresh) return { ...existing };
    const h: EngineHealth = {
      alistId: id,
      status: "online",
      latencyMs: 24,
      lastCheckedAt: now(),
      storagesKnown: true,
      storages: [
        { mountPath: "/Photos", driver: "Local", status: "work", disabled: false },
        { mountPath: "/Backup", driver: "AliyundriveOpen", status: "work", disabled: false },
      ],
    };
    this.engineHealthMap.set(id, h);
    return { ...h };
  }

  listAllEngineHealth(refresh = false): EngineHealth[] {
    return this.engines.map((e) => this.getEngineHealth(e.id, refresh));
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
      dataList: pageDemoRows(this.jobs, pageNum, pageSize).map((job) => {
        const current = this.currentTasks.get(job.id);
        const latest = (this.histories.get(job.id) || [])[0];
        return {
          ...job,
          running: Boolean(current),
          queued: false,
          pendingApproval: Boolean(current?.approval),
          lastStatus: latest?.status ?? 0,
        };
      }),
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
    for (const task of this.currentTasks.values()) {
      if (task.taskId === taskId) return "running";
    }
    for (const records of this.histories.values()) {
      const index = records.findIndex((record) => record.id === taskId);
      if (index < 0) continue;
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
    const job = this.jobs.find((item) => item.id === jobId);
    const task = {
      ...base,
      taskId,
      approval:
        job?.method === 1
          ? {
              deleteCount: 3,
              deleteSize: 48 * 1024 * 1024,
              pathCount: 2,
              paths: [
                {
                  path: "/Backup/Nightly/old-camera",
                  isPath: true,
                  count: 2,
                  size: 48 * 1024 * 1024,
                  action: "删除",
                },
                {
                  path: "/Backup/Nightly/obsolete.mov",
                  isPath: false,
                  count: 1,
                  size: 0,
                  action: "删除",
                },
              ],
            }
          : undefined,
    };
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
      if (input.action === "approve" || input.action === "reject") {
        for (const [jobId, task] of this.currentTasks) {
          if (task.taskId !== taskId) continue;
          if (input.action === "reject") this.finishCurrentTask(jobId, 4, taskId);
          else task.approval = undefined;
          return;
        }
      }
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
    const current = this.currentTasks.get(jobId);
    if (current) {
      records.unshift({
        id: current.taskId,
        status: 1,
        createTime: current.createTime,
        runTime: current.createTime,
      });
    }
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

  updateNotification(input: NotifyInput): NotifyItem | null {
    const rawId = input.id;
    const requestedId =
      typeof rawId === "number"
        ? rawId
        : typeof rawId === "string" && /^\s*\d+\s*$/.test(rawId)
          ? Number(rawId.trim())
          : 0;
    if (!Number.isInteger(requestedId) || requestedId <= 0) return null;
    const index = this.notifications.findIndex(
      (item) => item.id === requestedId,
    );
    if (index < 0) return null;
    const previous = this.notifications[index];
    const resolved = resolveNotifyInput(input, previous);
    const updated = normalizeNotify(resolved, requestedId, previous);
    this.notifications[index] = updated;
    return { ...updated };
  }

  toggleNotification(id: number, enable: number): boolean {
    const item = this.notifications.find((notify) => notify.id === id);
    if (!item) return false;
    item.enable = enable ? 1 : 0;
    return true;
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

  getDashboard(): DashboardOverview {
    const pendingApprovals: DashboardOverview["pendingApprovals"] = [];
    let runningJobs = 0;
    let enabledJobs = 0;
    const jobSummaries = this.jobs.map((job) => {
      if (job.enable === 1) enabledJobs += 1;
      const current = this.currentTasks.get(job.id);
      if (current) runningJobs += 1;
      if (current?.approval) {
        pendingApprovals.push({
          jobId: job.id,
          jobName: job.remark || `任务 #${job.id}`,
          taskId: current.taskId,
          deleteCount: current.approval.deleteCount,
          deleteSize: current.approval.deleteSize,
          pathCount: current.approval.pathCount,
        });
      }
      const engine = this.engines.find((e) => e.id === job.alistId);
      const latest = (this.histories.get(job.id) || [])[0];
      const done = current?.doneSize ?? 0;
      const total = done + (current?.remainSize ?? 0);
      return {
        id: job.id,
        remark: job.remark,
        enable: job.enable,
        alistId: job.alistId,
        alistName: engine?.remark || engine?.url || `引擎 #${job.alistId}`,
        method: job.method,
        isCron: job.isCron,
        interval: job.interval,
        srcPath: job.srcPath,
        dstPath: job.dstPath,
        running: Boolean(current),
        queued: false,
        currentTaskId: current?.taskId ?? 0,
        pendingApproval: current?.approval ?? null,
        liveNum: current?.num ?? null,
        liveProgress: total > 0 ? (done * 100) / total : 0,
        nextRunTime: job.nextRunTime ?? 0,
        windowEnable: Number(job.windowEnable ?? 0),
        windowStart: job.windowStart ?? "00:00",
        windowEnd: job.windowEnd ?? "06:00",
        lastTask: latest
          ? {
              id: latest.id,
              status: latest.status,
              createTime: latest.createTime ?? 0,
              runTime: latest.runTime ?? 0,
              successNum: latest.successNum ?? 0,
              failNum: latest.failNum ?? 0,
              allNum: latest.allNum ?? 0,
              sumSize: latest.sumSize ?? 0,
              errMsg: latest.errMsg ?? null,
            }
          : null,
      };
    });
    const healths = this.listAllEngineHealth(false);
    let totalRuns = 0;
    let successRuns = 0;
    let failRuns = 0;
    let stoppedRuns = 0;
    let totalFiles = 0;
    let totalBytes = 0;
    for (const records of this.histories.values()) {
      for (const rec of records) {
        totalRuns += 1;
        if (rec.status === 2 || rec.status === 8) successRuns += 1;
        else if (rec.status === 4) stoppedRuns += 1;
        else failRuns += 1;
        totalFiles += rec.successNum ?? 0;
        totalBytes += rec.sumSize ?? 0;
      }
    }
    return {
      kpi: {
        totalJobs: this.jobs.length,
        enabledJobs,
        runningJobs,
        queuedJobs: 0,
        pendingApprovals: pendingApprovals.length,
        totalEngines: this.engines.length,
        onlineEngines: healths.filter((h) => h.status === "online").length,
        offlineEngines: healths.filter((h) => h.status === "offline").length,
      },
      recent7d: {
        totalRuns,
        successRuns,
        failRuns,
        stoppedRuns,
        totalFiles,
        totalBytes,
      },
      pendingApprovals,
      jobs: jobSummaries,
    };
  }

  batchJobAction(action: string, ids?: number[]): BatchJobActionResult {
    const targetIds =
      action === "run_all" || action === "stop_all" || !ids?.length
        ? this.jobs.map((j) => j.id)
        : ids;
    const results: BatchJobActionResult["results"] = [];
    let succeeded = 0;
    let failed = 0;
    for (const id of targetIds) {
      const job = this.jobs.find((j) => j.id === id);
      if (!job) {
        failed += 1;
        results.push({ jobId: id, ok: false, error: "任务不存在" });
        continue;
      }
      if (action === "run" || action === "run_all") this.runJob(id);
      else if (action === "stop" || action === "stop_all") this.abortJob(id);
      else if (action === "enable") job.enable = 1;
      else if (action === "disable") this.pauseJob(id);
      succeeded += 1;
      results.push({ jobId: id, ok: true });
    }
    return { action, succeeded, failed, results };
  }

  deleteTasksBatch(taskIds: number[]): number {
    let deleted = 0;
    for (const id of taskIds) {
      if (this.removeTaskRecord(id) === "deleted") deleted += 1;
    }
    return deleted;
  }

  clearJobHistory(jobId: number): number {
    const records = this.histories.get(jobId) || [];
    const count = records.length;
    for (const rec of records) this.taskItems.delete(rec.id);
    this.histories.set(jobId, []);
    return count;
  }

  startDryRun(jobId: number): DryRunResult | null {
    const job = this.jobs.find((j) => j.id === jobId);
    if (!job) return null;
    const result: DryRunResult = {
      jobId,
      status: "completed",
      method: job.method,
      startedAt: now() - 1,
      finishedAt: now(),
      scannedDirs: 6,
      copyCount: 2,
      copySize: 18 * 1024 * 1024,
      updateCount: 1,
      updateSize: 4 * 1024 * 1024,
      deleteCount: job.method === 1 ? 1 : 0,
      deleteSize: job.method === 1 ? 2 * 1024 * 1024 : 0,
      moveCount: job.method === 2 ? 2 : 0,
      moveSize: job.method === 2 ? 18 * 1024 * 1024 : 0,
      mkdirCount: 1,
      skipCount: 12,
      totalItems: job.method === 1 ? 5 : 4,
      itemsTruncated: false,
      items: [
        {
          action: "mkdir",
          srcPath: "/Photos/2026",
          dstPath: "/Backup/Nightly/2026",
          fileName: "2026",
          fileSize: 0,
          isPath: true,
        },
        {
          action: job.method === 2 ? "move" : "copy",
          srcPath: "/Photos/2026",
          dstPath: "/Backup/Nightly/2026",
          fileName: "IMG_001.jpg",
          fileSize: 8 * 1024 * 1024,
          isPath: false,
        },
        {
          action: "update",
          srcPath: "/Photos",
          dstPath: "/Backup/Nightly",
          fileName: "catalog.db",
          fileSize: 4 * 1024 * 1024,
          isPath: false,
          reason: "文件已更新",
        },
        ...(job.method === 1
          ? [
              {
                action: "delete",
                dstPath: "/Backup/Nightly",
                fileName: "obsolete.mov",
                fileSize: 2 * 1024 * 1024,
                isPath: false,
                reason: "目标端多余项",
              },
            ]
          : []),
      ],
    };
    this.dryRuns.set(jobId, result);
    return result;
  }

  getDryRun(jobId: number): DryRunResult | null {
    return this.dryRuns.get(jobId) ?? null;
  }

  cancelDryRun(jobId: number): void {
    const existing = this.dryRuns.get(jobId);
    if (existing) this.dryRuns.set(jobId, { ...existing, status: "cancelled" });
  }

  testJobFilter(body: Record<string, unknown>): FilterTestResult {
    const samplePath = stringValue(body.samplePath).trim();
    const exclude = stringValue(body.exclude);
    const include = stringValue(body.include);
    const filterMode = numberValue(body.filterMode, 0);
    const sampleSize = numberValue(body.sampleSize, 0);
    const minSize = numberValue(body.minFileSize, 0);
    const maxSize = numberValue(body.maxFileSize, 0);
    const minAge = numberValue(body.minFileAgeMinutes, 0);
    const sampleAge = numberValue(body.sampleAgeMinutes, 60);
    const baseName = samplePath.split("/").pop() || samplePath;
    for (const rawLine of exclude.split("\n")) {
      const rule = rawLine.trim();
      if (!rule || rule.startsWith("# ")) continue;
      if (rule.endsWith("/") && samplePath.includes(rule.slice(0, -1))) {
        return { willSync: false, reason: `命中排除规则：${rule}`, matchedRule: rule };
      }
      if (rule.startsWith("*.") && baseName.toLowerCase().endsWith(rule.slice(1).toLowerCase())) {
        return { willSync: false, reason: `命中排除规则：${rule}`, matchedRule: rule };
      }
      if (rule === baseName) {
        return { willSync: false, reason: `命中排除规则：${rule}`, matchedRule: rule };
      }
    }
    if (filterMode === 1) {
      const rules = include
        .split("\n")
        .map((l) => l.trim())
        .filter((l) => l && !l.startsWith("# "));
      const matched = rules.find((r) =>
        r.startsWith("*.")
          ? baseName.toLowerCase().endsWith(r.slice(1).toLowerCase())
          : r === baseName,
      );
      if (!matched) {
        return { willSync: false, reason: "未命中白名单包含规则", matchedRule: "" };
      }
    }
    if (minSize > 0 && sampleSize < minSize) {
      return { willSync: false, reason: "文件小于最小限制", matchedRule: "minFileSize" };
    }
    if (maxSize > 0 && sampleSize > maxSize) {
      return { willSync: false, reason: "文件大于最大限制", matchedRule: "maxFileSize" };
    }
    if (minAge > 0 && sampleAge < minAge) {
      return { willSync: false, reason: `文件修改时间不足 ${minAge} 分钟`, matchedRule: "minFileAgeMinutes" };
    }
    return { willSync: true, reason: "通过全部过滤规则，将被同步", matchedRule: "" };
  }

  exportConfig(includeSecrets: boolean): SystemBackupPayload {
    return {
      version: 1,
      exportedAt: now(),
      hasSecrets: includeSecrets,
      settings: this.getSettings(),
      engines: this.engines.map((e) => ({
        ...e,
        token: includeSecrets ? "mock-token" : "",
      })),
      notifications: (includeSecrets ? this.notifications : this.listNotifications()).map(
        (n) => ({ ...n }),
      ),
      jobs: this.jobs.map((j) => ({ ...j })),
    };
  }

  importConfig(
    payload: SystemBackupPayload,
    conflictMode: string,
  ): SystemImportSummary {
    if (payload.settings) this.updateSettings(payload.settings);
    let enginesCreated = 0;
    let enginesSkipped = 0;
    for (const eng of payload.engines || []) {
      const url = stringValue(eng.url);
      if (this.engines.some((e) => e.url === url) && conflictMode !== "overwrite") {
        enginesSkipped += 1;
      } else {
        this.saveEngine(eng);
        enginesCreated += 1;
      }
    }
    return {
      settingsUpdated: Boolean(payload.settings),
      enginesCreated,
      enginesUpdated: 0,
      enginesSkipped,
      notifiesCreated: 0,
      notifiesUpdated: 0,
      notifiesSkipped: (payload.notifications || []).length,
      jobsCreated: 0,
      jobsUpdated: 0,
      jobsSkipped: (payload.jobs || []).length,
      warnings: payload.hasSecrets ? [] : ["导入文件不含凭据，请在引擎和通知页面补填 Token / 密钥"],
    };
  }

  getLogs(lines = 200, level = "", keyword = ""): AppLogsResponse {
    const sample = [
      "2026/11/24 08:00:01 INFO OpenSync mock server started",
      "2026/11/24 08:05:12 WARN engine #1 latency elevated: 410ms",
      "2026/11/24 08:10:33 ERROR copy failed for /Photos/raw.dng: connection reset",
    ];
    let filtered = sample;
    if (level && level !== "all") {
      filtered = filtered.filter((l) => l.toLowerCase().includes(level.toLowerCase()));
    }
    if (keyword) {
      filtered = filtered.filter((l) =>
        l.toLowerCase().includes(keyword.toLowerCase()),
      );
    }
    return {
      lines: filtered.slice(-lines),
      total: filtered.length,
      fileSize: filtered.join("\n").length,
      source: "memory",
    };
  }
}

export function createMockStore(seed?: MockStoreSeed): MockStore {
  return new MockStore(seed);
}
