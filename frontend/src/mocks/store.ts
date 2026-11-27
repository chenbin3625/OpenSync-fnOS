import type {
  AlistItem,
  AppLogsResponse,
  BatchJobActionResult,
  BatchJobSkippedItem,
  CurrentTaskData,
  DashboardOverview,
  DryRunResult,
  EngineHealth,
  FilterTestResult,
  JobCheckResult,
  JobItem,
  NativeAboutInfo,
  NativeAuthStepSnapshot,
  NativeBrowseResult,
  NativeCryptRecoveryResult,
  NativeExportResult,
  NativeImportCandidate,
  NativeImportPreview,
  NativeImportResult,
  NativeLocalRoot,
  NativeOpenListDiscoverResult,
  NativeRemoteItem,
  NativeRenameResult,
  NativeSizeInfo,
  NativeStorageState,
  NativeStorageTestResult,
  NotifyItem,
  PageData,
  RealtimeTaskItemPage,
  RecentTaskItem,
  RecentTaskItemsPageData,
  RecentTaskItemStats,
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
  taskItems?: Record<number, TaskItem[]>;
};

// Mirrors backend/internal/config/config.go defaults; mock mode must not show
// different defaults than a real install.
const defaultSettings: SystemSettings = {
  taskTimeout: 48,
  taskSave: 30,
  copyConcurrency: 5,
  scanConcurrency: 16,
  maxRetries: 2,
  engineHealthCheckInterval: 15,
  defaultBwLimit: "",
  proxyUrl: "",
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

function mockNativeSummary(params: Record<string, string>): string {
  switch (params.type) {
    case "local":
      return "本地授权目录";
    case "s3":
      return [params.provider, params.endpoint, params.region]
        .filter(Boolean)
        .join(" · ");
    case "webdav":
      return [params.url, params.user].filter(Boolean).join(" · ");
    case "quark":
      if (params.account_name) return `夸克网盘 · ${params.account_name}`;
      if (params.root_folder_id && params.root_folder_id !== "0") {
        return `夸克网盘 · 目录 ${params.root_folder_id}`;
      }
      return "夸克网盘";
    case "pan123": {
      const modeLabel = params.protocol === "web" ? "个人盘 Web" : "开放平台";
      if (params.account_name) return `123云盘 (${modeLabel}) · ${params.account_name}`;
      if (params.protocol === "web" && params.web_user) {
        return `123云盘 (${modeLabel}) · ${params.web_user}`;
      }
      if (params.root_folder_id && params.root_folder_id !== "0") {
        return `123云盘 (${modeLabel}) · 目录 ${params.root_folder_id}`;
      }
      return `123云盘 (${modeLabel})`;
    }
    case "sftp":
    case "smb": {
      const addr = params.port ? `${params.host}:${params.port}` : params.host || "";
      if (params.user) {
        return params.type === "smb" && params.domain
          ? `${params.domain}\\${params.user}@${addr}`
          : `${params.user}@${addr}`;
      }
      return addr;
    }
    case "crypt":
      return params.remote ? `底层 ${params.remote}` : "";
    default:
      return params.type || "";
  }
}

function parseMockINI(
  input: string,
): Record<string, Record<string, string>> | string {
  const cfg: Record<string, Record<string, string>> = {};
  let section: Record<string, string> | null = null;
  const lines = input.replace(/^\uFEFF/, "").split(/\r?\n/);
  for (let i = 0; i < lines.length; i += 1) {
    const line = lines[i].trim();
    if (!line || line.startsWith("#") || line.startsWith(";")) continue;
    if (line.startsWith("[") && line.endsWith("]")) {
      const name = line.slice(1, -1).trim();
      if (!name) return `第 ${i + 1} 行的存储名称为空`;
      section = {};
      cfg[name] = section;
      continue;
    }
    const eq = line.indexOf("=");
    if (eq <= 0 || !section) return `第 ${i + 1} 行不是有效的 rclone.conf 配置`;
    const key = line.slice(0, eq).trim();
    const val = line.slice(eq + 1).trim();
    section[key] = val;
  }
  if (Object.keys(cfg).length === 0) return "配置中没有命名存储";
  return cfg;
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
    ...(merged.kind === "rclone" ? { kind: "rclone" as const } : {}),
    ...(merged.remotes && typeof merged.remotes === "object"
      ? { remotes: merged.remotes as Record<string, string> }
      : {}),
    ...(merged.remoteDetails && typeof merged.remoteDetails === "object"
      ? {
          remoteDetails: merged.remoteDetails as Record<
            string,
            { type: string; summary?: string }
          >,
        }
      : {}),
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
    bwLimit: merged.bwLimit == null ? "" : stringValue(merged.bwLimit),
    tpsLimit: numberValue(merged.tpsLimit, 0),
    tpsLimitBurst: numberValue(merged.tpsLimitBurst, 0),
    modifyWindow: merged.modifyWindow == null ? "" : stringValue(merged.modifyWindow),
    fastList: merged.fastList === true ? 1 : numberValue(merged.fastList, 0),
    multiThreadStreams: numberValue(merged.multiThreadStreams, 0),
    createEmptySrcDirs:
      merged.createEmptySrcDirs === true
        ? 1
        : numberValue(merged.createEmptySrcDirs, 0),
    followSymlinks:
      merged.followSymlinks === true ? 1 : numberValue(merged.followSymlinks, 0),
    extraFlags: merged.extraFlags == null ? "" : typeof merged.extraFlags === "string" ? merged.extraFlags : JSON.stringify(merged.extraFlags),
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

function parseJsonPathList(raw?: string | null): string[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed)
      ? parsed.map((v) => String(v || "").trim()).filter(Boolean)
      : [];
  } catch {
    return [];
  }
}

function firstPathSegment(pathValue: string): string {
  const trimmed = pathValue.trim().replace(/^\/+|\/+$/g, "");
  if (!trimmed) return "";
  const slashIdx = trimmed.indexOf("/");
  return slashIdx >= 0 ? trimmed.slice(0, slashIdx) : trimmed;
}

function jobMatchesStorage(job: JobItem, storage: string): boolean {
  const target = storage.trim().toLowerCase();
  if (!target) return true;
  const paths = [
    ...parseJsonPathList(job.srcPath),
    ...parseJsonPathList(job.dstPath),
  ];
  if (target === "__local__") {
    let hasAny = false;
    for (const p of paths) {
      const seg = firstPathSegment(p);
      if (!seg) continue;
      hasAny = true;
      if (seg !== "本地") return false;
    }
    return hasAny;
  }
  return paths.some((p) => firstPathSegment(p).toLowerCase() === target);
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
  private jobChecks = new Map<number, JobCheckResult>();
  private nativeAuthSessions = new Map<
    string,
    {
      snapshot: NativeAuthStepSnapshot;
      oauthState: string;
      secrets: Record<string, string>;
      publicOptions: Record<string, string>;
    }
  >();
  private nextAuthSessionSeq = 1;
  private engineHealthMap = new Map<number, EngineHealth>();
  private nativeRemotes = new Map<string, Record<string, string>>();
  private customDirs = new Map<string, string[]>();
  private nativeLocalRoots: NativeLocalRoot[] = [
    { path: "/vol1/1000/Photos", id: "Photos~a1b2c3d4e5f6" },
    { path: "/vol2/1000/Backup", id: "Backup~1234567890ab" },
  ];

  constructor(seed: MockStoreSeed = {}) {
    const seededEngines = seed.engines ?? [
      {
        id: 1,
        kind: "rclone",
        remark: "内置存储",
        url: "rclone://native",
        userName: "rclone",
        maxConcurrentJobs: 0,
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

    if (seed.taskItems) {
      for (const [taskId, items] of Object.entries(seed.taskItems)) {
        this.taskItems.set(
          Number(taskId),
          items.map((item, idx) => ({
            ...item,
            id: numberValue(item.id, idx + 1),
            taskId: numberValue(item.taskId, Number(taskId)),
            createTime: numberValue(item.createTime, now()),
          })),
        );
      }
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
    const engine = this.engines.find((e) => e.id === id);
    const storages =
      engine?.kind === "rclone"
        ? [
            {
              mountPath: "/本地",
              driver: "local",
              status: this.nativeLocalRoots.length > 0 ? "work" : "未授权本地目录",
              disabled: false,
            },
            ...[...this.nativeRemotes.entries()]
              .sort(([a], [b]) => a.localeCompare(b))
              .map(([name, p]) => ({
                mountPath: `/${name}`,
                driver: p.type || "s3",
                status: "work",
                disabled: false,
              })),
          ]
        : [
            { mountPath: "/Photos", driver: "Local", status: "work", disabled: false },
            { mountPath: "/Backup", driver: "AliyundriveOpen", status: "work", disabled: false },
          ];
    const h: EngineHealth = {
      alistId: id,
      status: "online",
      latencyMs: 24,
      lastCheckedAt: now(),
      storagesKnown: true,
      storages,
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
    const clean = (path || "/").replace(/\/+$/, "") || "/";
    const custom = this.customDirs.get(clean) || [];
    const withCustom = (names: string[]) =>
      Array.from(new Set([...names, ...custom])).map((name) => ({ path: name }));
    if (clean === "/") {
      return withCustom([
        ...defaultPaths["/"].map((item) => item.name),
        ...(this.nativeLocalRoots.length ? ["本地"] : []),
        ...this.nativeRemotes.keys(),
      ]);
    }
    if (clean === "/本地") {
      return withCustom(this.nativeLocalRoots.map((root) => root.id));
    }
    const [, mount, ...relative] = clean.split("/");
    if (this.nativeRemotes.has(mount)) {
      const defaults = (defaultPaths[`/${relative.join("/")}`] || []).map(
        (item) => item.name,
      );
      return withCustom(defaults);
    }
    if (mount === "本地") {
      const [root, ...folders] = relative;
      const label = this.nativeLocalRoots
        .find((item) => item.id === root)
        ?.path.split("/")
        .at(-1);
      const defaults = (
        defaultPaths[`/${[label, ...folders].join("/")}`] || []
      ).map((item) => item.name);
      return withCustom(defaults);
    }
    return withCustom((defaultPaths[clean] || []).map((item) => item.name));
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
    const target = this.engines.find((engine) => engine.id === id);
    if (target?.kind === "rclone") {
      this.nativeRemotes.clear();
    }
    this.engines = this.engines.filter((engine) => engine.id !== id);
  }

  private findNativeEngine(): AlistItem | undefined {
    return this.engines.find(
      (e) => e.kind === "rclone" || e.url === "rclone://native",
    );
  }

  private syncNativeEngineMetadata(): AlistItem | undefined {
    const engine = this.findNativeEngine();
    if (!engine) return undefined;
    const remotes: Record<string, string> = {};
    const remoteDetails: Record<string, { type: string; summary?: string }> = {};
    for (const [name, params] of this.nativeRemotes.entries()) {
      remotes[name] = params.type || "s3";
      remoteDetails[name] = {
        type: params.type || "s3",
        summary: mockNativeSummary(params),
      };
    }
    engine.kind = "rclone";
    engine.remotes = remotes;
    engine.remoteDetails = remoteDetails;
    return engine;
  }

  private ensureNativeEngine(
    remark?: string,
    maxConcurrentJobs?: number,
  ): AlistItem {
    let engine = this.findNativeEngine();
    if (!engine) {
      engine = {
        id: this.nextEngineId++,
        kind: "rclone",
        remark: remark?.trim() || "内置存储",
        url: "rclone://native",
        userName: "rclone",
        maxConcurrentJobs: maxConcurrentJobs ?? 1,
        activeJobs: 0,
        queuedJobs: 0,
        remotes: {},
        remoteDetails: {},
        createTime: now(),
      };
      this.engines.push(engine);
    } else {
      if (remark !== undefined) engine.remark = remark.trim() || "内置存储";
      if (maxConcurrentJobs !== undefined) {
        engine.maxConcurrentJobs = maxConcurrentJobs;
      }
    }
    return this.syncNativeEngineMetadata()!;
  }

  getNativeStorageState(): NativeStorageState {
    const engine = this.syncNativeEngineMetadata();
    const secretKeys = new Set([
      "secret_access_key",
      "session_token",
      "pass",
      "bearer_token",
      "key_pem",
      "key_file_pass",
      "password",
      "password2",
      "key",
      "token",
      "client_secret",
      "service_account_credentials",
      "sas_url",
      "auth_token",
      "application_credential_secret",
      "access_grant",
      "api_key",
      "passphrase",
      "library_key",
      "connection_string",
      "mailbox_password",
      "otp_secret_key",
      "client_access_token",
      "client_refresh_token",
      "client_salted_key_pass",
      "trust_token",
      "cookies",
      "master_keys",
      "private_key",
      "file_password",
      "folder_password",
      "access_token",
      "cookie",
      "web_password",
      "direct_link_key",
    ]);
    const supportedTypes = [
      "local",
      "s3",
      "webdav",
      "quark",
      "pan123",
      "sftp",
      "smb",
      "ftp",
      "http",
      "b2",
      "onedrive",
      "drive",
      "dropbox",
      "box",
      "pcloud",
      "mega",
      "azureblob",
      "azurefiles",
      "googlecloudstorage",
      "swift",
      "oracleobjectstorage",
      "qingstor",
      "storj",
      "yandex",
      "koofr",
      "seafile",
      "pikpak",
      "protondrive",
      "iclouddrive",
      "jottacloud",
      "filen",
      "hidrive",
      "huaweidrive",
      "mailru",
      "fichier",
      "gofile",
      "pixeldrain",
      "crypt",
      "chunker",
      "compress",
      "hasher",
      "alias",
      "archive",
      "union",
      "combine",
    ];
    const remotes: NativeRemoteItem[] = [...this.nativeRemotes.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, params]) => {
        const pub: Record<string, string> = {};
        const configuredSecrets: string[] = [];
        for (const [k, v] of Object.entries(params)) {
          if (!v || k === "type") continue;
          if (secretKeys.has(k)) configuredSecrets.push(k);
          else pub[k] = v;
        }
        const usedByCrypts: string[] = [];
        for (const [otherName, otherParams] of this.nativeRemotes.entries()) {
          if (otherName === name) continue;
          const ot = otherParams.type;
          if (
            ot === "crypt" ||
            ot === "chunker" ||
            ot === "compress" ||
            ot === "hasher" ||
            ot === "alias" ||
            ot === "archive"
          ) {
            const ref = (otherParams.remote || "").split(":")[0];
            if (ref.toLowerCase() === name.toLowerCase()) {
              usedByCrypts.push(otherName);
            }
          } else if (ot === "union") {
            const upstreams = (otherParams.upstreams || "").split(/\s+/).filter(Boolean);
            if (
              upstreams.some(
                (u) => u.replace(/:(?:ro|nc|ep)$/i, "").split(":")[0]?.toLowerCase() === name.toLowerCase(),
              )
            ) {
              usedByCrypts.push(otherName);
            }
          } else if (ot === "combine") {
            const upstreams = (otherParams.upstreams || "").split(/\s+/).filter(Boolean);
            if (
              upstreams.some((u) => {
                const eq = u.indexOf("=");
                const rhs = eq >= 0 ? u.slice(eq + 1) : u;
                return rhs.split(":")[0]?.toLowerCase() === name.toLowerCase();
              })
            ) {
              usedByCrypts.push(otherName);
            }
          }
        }
        const usedByJobs: string[] = [];
        if (engine) {
          for (const job of this.jobs) {
            if (job.alistId !== engine.id) continue;
            const paths = [
              ...parsePathList(job.srcPath),
              ...parsePathList(job.dstPath),
            ];
            if (
              paths.some(
                (p) =>
                  p.replace(/^\/+/, "").split("/")[0]?.toLowerCase() ===
                  name.toLowerCase(),
              )
            ) {
              usedByJobs.push(job.remark || `#${job.id}`);
            }
          }
        }
        return {
          name,
          type: params.type || "s3",
          summary: mockNativeSummary(params),
          options: pub,
          configuredSecrets,
          usedByJobs,
          usedByCrypts,
          activeJobs: 0,
          queuedJobs: 0,
        };
      });
    return {
      engineId: engine?.id ?? 0,
      enabled: Boolean(engine),
      remark: engine?.remark || "内置存储",
      maxConcurrentJobs: engine?.maxConcurrentJobs ?? 0,
      localActiveJobs: this.currentTasks.size,
      localQueuedJobs: 0,
      rcloneAvailable: true,
      rcloneVersion: "v1.76.0",
      supportedTypes,
      localRoots: [...this.nativeLocalRoots],
      remotes,
    };
  }

  saveNativeEngine(input: {
    remark?: string;
    maxConcurrentJobs?: number;
  }): NativeStorageState {
    const engine = this.ensureNativeEngine(
      input.remark,
      input.maxConcurrentJobs,
    );
    this.engineHealthMap.delete(engine.id);
    return this.getNativeStorageState();
  }

  saveNativeRemote(input: {
    name?: string;
    options?: Record<string, string>;
    replace?: boolean;
    maxConcurrentJobs?: number;
    authSessionId?: string;
  }): string | null {
    const name = (input.name || "").trim();
    const opts = { ...(input.options || {}) };
    if (Object.keys(opts).length === 0) {
      const eng = this.ensureNativeEngine(undefined, input.maxConcurrentJobs);
      this.engineHealthMap.delete(eng.id);
      return null;
    }
    if (!name) return "请输入存储名称";
    if (input.authSessionId) {
      const err = this.mergeMockAuthSession(input.authSessionId, opts);
      if (err) return err;
    }
    const existingKey = [...this.nativeRemotes.keys()].find(
      (k) => k.toLowerCase() === name.toLowerCase(),
    );
    if (existingKey && !input.replace) {
      return `存储 "${existingKey}" 已存在，请使用其他名称或选择编辑`;
    }
    if (
      existingKey &&
      this.nativeRemotes.get(existingKey)?.type !== opts.type
    ) {
      return "同名存储的类型不能修改";
    }
    const targetKey = existingKey || name;
    this.nativeRemotes.set(targetKey, { ...opts });
    if (input.authSessionId) {
      this.nativeAuthSessions.delete(input.authSessionId);
    }
    const eng = this.ensureNativeEngine(undefined, input.maxConcurrentJobs);
    this.engineHealthMap.delete(eng.id);
    return null;
  }

  updateNativeRemote(input: {
    name: string;
    newName?: string;
    options: Record<string, string>;
    clearSecrets?: string[];
    authSessionId?: string;
  }): string | null {
    const name = (input.name || "").trim();
    const existingKey = [...this.nativeRemotes.keys()].find(
      (k) => k.toLowerCase() === name.toLowerCase(),
    );
    if (!existingKey) return `存储 "${name}" 不存在`;
    const oldParams = this.nativeRemotes.get(existingKey)!;
    if (input.options.type && input.options.type !== oldParams.type) {
      return "同名存储的类型不能修改";
    }
    const clearSet = new Set(input.clearSecrets || []);
    const next: Record<string, string> = {
      ...input.options,
      type: oldParams.type,
    };
    if (input.authSessionId) {
      const err = this.mergeMockAuthSession(input.authSessionId, next);
      if (err) return err;
    }
    for (const secretKey of [
      "secret_access_key",
      "session_token",
      "pass",
      "bearer_token",
      "key_pem",
      "key_file_pass",
      "password",
      "password2",
      "key",
      "token",
      "client_secret",
      "service_account_credentials",
      "sas_url",
      "auth_token",
      "trust_token",
      "cookies",
      "application_credential_secret",
    ]) {
      if (!next[secretKey] && !clearSet.has(secretKey) && oldParams[secretKey]) {
        next[secretKey] = oldParams[secretKey];
      }
    }
    const newName = input.newName?.trim();
    if (newName && newName !== existingKey) {
      const result = this.renameNativeRemote(existingKey, newName, next);
      if (typeof result === "string") return result;
    } else {
      this.nativeRemotes.set(existingKey, next);
    }
    if (input.authSessionId) {
      this.nativeAuthSessions.delete(input.authSessionId);
    }
    const eng = this.syncNativeEngineMetadata();
    if (eng) this.engineHealthMap.delete(eng.id);
    return null;
  }

  private mergeMockAuthSession(
    sessionId: string,
    target: Record<string, string>,
  ): string | null {
    const sess = this.nativeAuthSessions.get(sessionId);
    if (!sess || sess.snapshot.status !== "completed") {
      return "授权会话不存在或尚未完成，请重新完成授权";
    }
    if (target.type && target.type !== sess.snapshot.type) {
      return "授权会话与当前所选存储类型不匹配，请重新授权";
    }
    for (const [k, v] of Object.entries(sess.publicOptions)) {
      if (!target[k] && v) target[k] = v;
    }
    for (const [k, v] of Object.entries(sess.secrets)) {
      if (v) target[k] = v;
    }
    return null;
  }

  startNativeStorageAuth(input: {
    type: string;
    options?: Record<string, string>;
  }): NativeAuthStepSnapshot | string {
    const typ = (input.type || "").trim();
    const opts = input.options || {};
    const interactive = new Set([
      "onedrive",
      "drive",
      "dropbox",
      "box",
      "pcloud",
      "yandex",
      "jottacloud",
      "hidrive",
      "huaweidrive",
      "googlecloudstorage",
      "iclouddrive",
      "seafile",
      "quark",
    ]);
    if (!interactive.has(typ)) {
      return `存储类型 "${typ}" 不需要引导授权，请直接填写表单参数`;
    }
    if (typ === "iclouddrive" && (!opts.apple_id?.trim() || !opts.password)) {
      return "请先填写 Apple ID 和密码后再发起登录验证";
    }
    if (typ === "seafile" && (!opts.url?.trim() || !opts.user?.trim() || !opts.pass)) {
      return "请先填写有效的 Seafile 服务地址、账号和密码后再发起两步验证登录";
    }
    const sessionId = `mock-auth-${this.nextAuthSessionSeq++}`;
    const oauthState = `mock-state-${sessionId}`;
    const expiresAt = Math.floor(Date.now() / 1000) + 600;

    if (typ === "quark") {
      const mockSvg =
        "data:image/svg+xml;base64," +
        btoa(
          `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 29 29" shape-rendering="crispEdges"><rect width="29" height="29" fill="#fff"/><path fill="#000" d="M4 4h7v7H4zm1 1v5h5V5zm1 1h3v3H6zm12-2h7v7h-7zm1 1v5h5V5zm1 1h3v3h-3zM4 18h7v7H4zm1 1v5h5v-5zm1 1h3v3H6zm8-6h3v3h-3zm5 5h4v4h-4z"/></svg>`,
        );
      const snapshot: NativeAuthStepSnapshot = {
        sessionId,
        type: typ,
        status: "qrcode",
        qrCodeUrl: `https://su.quark.cn/4_eMHBJ?token=mock-${sessionId}&client_id=532`,
        qrCodeImage: mockSvg,
        qrStatus: "waiting",
        expiresAt,
      };
      this.nativeAuthSessions.set(sessionId, {
        snapshot,
        oauthState,
        secrets: {},
        publicOptions: {},
      });
      return snapshot;
    }

    if (typ === "jottacloud") {
      const snapshot: NativeAuthStepSnapshot = {
        sessionId,
        type: typ,
        status: "question",
        question: {
          state: "auth_type_done",
          name: "config_type",
          help: "Type of authentication.",
          default: "standard",
          exclusive: true,
          type: "string",
          examples: [
            { value: "standard", help: "Standard authentication (Personal login token)" },
            { value: "traditional", help: "Traditional authentication (OAuth)" },
          ],
        },
        expiresAt,
      };
      this.nativeAuthSessions.set(sessionId, {
        snapshot,
        oauthState,
        secrets: {},
        publicOptions: {},
      });
      return snapshot;
    }

    if (typ === "iclouddrive") {
      const snapshot: NativeAuthStepSnapshot = {
        sessionId,
        type: typ,
        status: "question",
        question: {
          state: "2fa_code",
          name: "2fa_code",
          help: "Enter the 6-digit Apple 2FA verification code",
          required: true,
          type: "string",
        },
        expiresAt,
      };
      this.nativeAuthSessions.set(sessionId, {
        snapshot,
        oauthState,
        secrets: {},
        publicOptions: {},
      });
      return snapshot;
    }

    if (typ === "seafile") {
      const snapshot: NativeAuthStepSnapshot = {
        sessionId,
        type: typ,
        status: "question",
        question: {
          state: "2fa_do",
          name: "config_2fa",
          help: "Two-factor authentication: please enter your 2FA code",
          required: true,
          type: "string",
        },
        expiresAt,
      };
      this.nativeAuthSessions.set(sessionId, {
        snapshot,
        oauthState,
        secrets: {},
        publicOptions: {},
      });
      return snapshot;
    }

    const redirectUri =
      typ === "onedrive" || typ === "dropbox" || typ === "pcloud"
        ? "http://localhost:53682/"
        : "http://127.0.0.1:53682/";
    const snapshot: NativeAuthStepSnapshot = {
      sessionId,
      type: typ,
      status: "oauth",
      authUrl: `https://auth.example.com/${typ}/authorize?redirect_uri=${encodeURIComponent(redirectUri)}&state=${oauthState}`,
      redirectUri,
      expiresAt,
    };
    this.nativeAuthSessions.set(sessionId, {
      snapshot,
      oauthState,
      secrets: {},
      publicOptions: {},
    });
    return snapshot;
  }

  getNativeStorageAuth(sessionId: string): NativeAuthStepSnapshot | string {
    const sess = this.nativeAuthSessions.get(sessionId);
    if (!sess) return "授权会话不存在或已过期，请重新发起授权";
    if (sess.snapshot.type === "quark" && sess.snapshot.status === "qrcode") {
      sess.secrets.cookie = `__pus=mock_pus_${sessionId}; __puus=mock_puus_${sessionId}`;
      sess.publicOptions.account_name = "夸克演示账号";
      sess.snapshot = {
        ...sess.snapshot,
        status: "completed",
        qrStatus: undefined,
        publicOptions: { ...sess.publicOptions },
        configuredSecrets: ["cookie"],
      };
    }
    return sess.snapshot;
  }

  submitNativeStorageAuthCallback(
    sessionId: string,
    callbackUrl: string,
  ): NativeAuthStepSnapshot | string {
    const sess = this.nativeAuthSessions.get(sessionId);
    if (!sess) return "授权会话不存在或已过期，请重新发起授权";
    if (sess.snapshot.status !== "oauth") return sess.snapshot;

    const trimmed = callbackUrl.trim().replace(/^["'<>`]+|["'<>`]+$/g, "").trim();
    if (!trimmed) {
      return "请粘贴授权完成后浏览器地址栏中的回调网址、?code=... 查询串或 URL 片段";
    }
    let queryStr = "";
    if (trimmed.includes("://")) {
      try {
        const u = new URL(trimmed);
        queryStr = u.search ? u.search.slice(1) : "";
        if (!queryStr && u.hash) {
          const frag = u.hash.replace(/^#/, "");
          const qIdx = frag.indexOf("?");
          queryStr = qIdx >= 0 ? frag.slice(qIdx + 1) : frag.replace(/^[/?&]+/, "");
        }
      } catch {
        return "回调网址格式无效";
      }
    } else if (trimmed.includes("?")) {
      const idx = trimmed.indexOf("?");
      const afterQ = trimmed.slice(idx + 1);
      const beforeQ = trimmed.slice(0, idx);
      if (beforeQ.includes("#")) {
        queryStr = afterQ.replace(/^[?&]+/, "");
      } else {
        const hashIdx = afterQ.indexOf("#");
        queryStr =
          hashIdx >= 0 ? afterQ.slice(0, hashIdx) : afterQ;
      }
    } else if (trimmed.includes("#")) {
      const [, afterHash = ""] = trimmed.split("#", 2);
      const cleanedFrag = afterHash.replace(/^[/?&]+/, "");
      queryStr = cleanedFrag.includes("=") ? cleanedFrag : "";
    } else if (trimmed.includes("=")) {
      queryStr = trimmed.replace(/^[?&/]+/, "").split("#")[0];
    } else {
      return "未能识别回调参数。支持粘贴完整回调网址、纯查询串（?code=...&state=...）或 URL 片段（#code=...）";
    }
    const params = new URLSearchParams(queryStr);
    if (params.get("error")) {
      return `网盘拒绝了授权（${params.get("error")}）`;
    }
    if (!params.get("code")) {
      return "回调网址中缺少授权码（code 参数）";
    }
    const gotState = params.get("state");
    if (gotState && gotState !== sess.oauthState) {
      return "回调网址中的 state 与当前授权会话不匹配";
    }

    const typ = sess.snapshot.type;
    sess.secrets.token = JSON.stringify({
      access_token: `mock-access-${sessionId}`,
      refresh_token: `mock-refresh-${sessionId}`,
      expiry: "2099-01-01T00:00:00Z",
    });

    if (typ === "onedrive") {
      sess.snapshot = {
        ...sess.snapshot,
        status: "question",
        authUrl: undefined,
        redirectUri: undefined,
        question: {
          state: "choose_type_done",
          name: "config_type",
          help: "Type of connection",
          default: "onedrive",
          exclusive: true,
          type: "string",
          examples: [
            { value: "onedrive", help: "OneDrive Personal or Business" },
            { value: "sharepoint", help: "Root Sharepoint site" },
          ],
        },
      };
      return sess.snapshot;
    }

    if (typ === "drive") {
      sess.snapshot = {
        ...sess.snapshot,
        status: "question",
        authUrl: undefined,
        redirectUri: undefined,
        question: {
          state: "teamdrive",
          name: "config_change_team_drive",
          help: "Configure this as a Shared Drive (Team Drive)?",
          default: "false",
          exclusive: true,
          type: "bool",
          examples: [
            { value: "false", help: "No" },
            { value: "true", help: "Yes" },
          ],
        },
      };
      return sess.snapshot;
    }

    if (typ === "pcloud") {
      sess.publicOptions.hostname = params.get("hostname") || "api.pcloud.com";
    }

    sess.snapshot = {
      sessionId,
      type: typ,
      status: "completed",
      publicOptions: { ...sess.publicOptions },
      configuredSecrets: Object.keys(sess.secrets),
      expiresAt: Math.floor(Date.now() / 1000) + 1800,
    };
    return sess.snapshot;
  }

  continueNativeStorageAuth(
    sessionId: string,
    state: string,
    answer: string,
  ): NativeAuthStepSnapshot | string {
    const sess = this.nativeAuthSessions.get(sessionId);
    if (!sess) return "授权会话不存在或已过期，请重新发起授权";
    if (sess.snapshot.status !== "question" || !sess.snapshot.question) {
      return sess.snapshot;
    }
    if (state && state !== sess.snapshot.question.state) {
      return "配置步骤已变更，请按当前提示重新提交";
    }
    const typ = sess.snapshot.type;

    if (typ === "onedrive" && state === "choose_type_done") {
      sess.snapshot = {
        ...sess.snapshot,
        status: "question",
        question: {
          state: "driveid_final",
          name: "config_driveid",
          help: "Select drive",
          default: "b!mock-onedrive-01",
          exclusive: true,
          type: "string",
          examples: [
            { value: "b!mock-onedrive-01", help: "OneDrive 个人版 (personal)" },
            { value: "b!mock-onedrive-02", help: "OneDrive 商业版 (business)" },
          ],
        },
      };
      return sess.snapshot;
    }

    if (typ === "onedrive" && state === "driveid_final") {
      sess.publicOptions.drive_id = answer || "b!mock-onedrive-01";
      sess.publicOptions.drive_type =
        answer === "b!mock-onedrive-02" ? "business" : "personal";
      sess.snapshot = {
        sessionId,
        type: typ,
        status: "completed",
        publicOptions: { ...sess.publicOptions },
        configuredSecrets: Object.keys(sess.secrets),
        expiresAt: Math.floor(Date.now() / 1000) + 1800,
      };
      return sess.snapshot;
    }

    if (typ === "drive" && state === "teamdrive") {
      if (answer === "true") {
        sess.snapshot = {
          ...sess.snapshot,
          status: "question",
          question: {
            state: "teamdrive_ok",
            name: "config_team_drive",
            help: "Choose a Shared Drive",
            default: "0AMockTeamDrive01",
            exclusive: true,
            type: "string",
            examples: [
              { value: "0AMockTeamDrive01", help: "家庭共享盘" },
            ],
          },
        };
        return sess.snapshot;
      }
      sess.snapshot = {
        sessionId,
        type: typ,
        status: "completed",
        publicOptions: { ...sess.publicOptions },
        configuredSecrets: Object.keys(sess.secrets),
        expiresAt: Math.floor(Date.now() / 1000) + 1800,
      };
      return sess.snapshot;
    }

    if (typ === "drive" && state === "teamdrive_ok") {
      sess.publicOptions.team_drive = answer;
      sess.snapshot = {
        sessionId,
        type: typ,
        status: "completed",
        publicOptions: { ...sess.publicOptions },
        configuredSecrets: Object.keys(sess.secrets),
        expiresAt: Math.floor(Date.now() / 1000) + 1800,
      };
      return sess.snapshot;
    }

    if (typ === "jottacloud" && state === "auth_type_done") {
      if (answer === "traditional") {
        sess.snapshot = {
          ...sess.snapshot,
          status: "oauth",
          question: undefined,
          authUrl: `https://id.jottacloud.com/auth/realms/jottacloud/protocol/openid-connect/auth?state=${sess.oauthState}`,
          redirectUri: "http://127.0.0.1:53682/",
        };
        return sess.snapshot;
      }
      sess.snapshot = {
        ...sess.snapshot,
        status: "question",
        question: {
          state: "standard_token",
          name: "config_login_token",
          help: "Personal login token from https://www.jottacloud.com/web/secure",
          required: true,
          type: "string",
        },
      };
      return sess.snapshot;
    }

    if (typ === "jottacloud" && state === "standard_token") {
      if (!answer.trim()) return "请输入个人登录令牌";
      sess.secrets.token = JSON.stringify({
        access_token: `mock-jotta-${sessionId}`,
        refresh_token: `mock-jotta-refresh-${sessionId}`,
      });
      sess.publicOptions.configVersion = "1";
      sess.snapshot = {
        sessionId,
        type: typ,
        status: "completed",
        publicOptions: { ...sess.publicOptions },
        configuredSecrets: Object.keys(sess.secrets),
        expiresAt: Math.floor(Date.now() / 1000) + 1800,
      };
      return sess.snapshot;
    }

    if (typ === "iclouddrive") {
      if (!answer.trim()) return "请输入 Apple 两步验证码";
      sess.secrets.trust_token = `mock-trust-${sessionId}`;
      sess.secrets.cookies = `mock-cookies-${sessionId}`;
      sess.snapshot = {
        sessionId,
        type: typ,
        status: "completed",
        publicOptions: {},
        configuredSecrets: ["trust_token", "cookies"],
        expiresAt: Math.floor(Date.now() / 1000) + 1800,
      };
      return sess.snapshot;
    }

    if (typ === "seafile") {
      if (!answer.trim()) return "请输入 Seafile 2FA 验证码";
      sess.secrets.auth_token = `mock-seafile-token-${sessionId}`;
      sess.snapshot = {
        sessionId,
        type: typ,
        status: "completed",
        publicOptions: {},
        configuredSecrets: ["auth_token"],
        expiresAt: Math.floor(Date.now() / 1000) + 1800,
      };
      return sess.snapshot;
    }

    sess.snapshot = {
      sessionId,
      type: typ,
      status: "completed",
      publicOptions: { ...sess.publicOptions },
      configuredSecrets: Object.keys(sess.secrets),
      expiresAt: Math.floor(Date.now() / 1000) + 1800,
    };
    return sess.snapshot;
  }

  cancelNativeStorageAuth(sessionId: string): void {
    this.nativeAuthSessions.delete(sessionId);
  }

  deleteNativeRemote(name: string): string | null {
    const state = this.getNativeStorageState();
    const item = state.remotes.find(
      (r) => r.name.toLowerCase() === name.trim().toLowerCase(),
    );
    if (!item) return `存储 "${name}" 不存在`;
    if (item.usedByCrypts.length > 0) {
      return `存储 "${item.name}" 正被加密存储 "${item.usedByCrypts[0]}" 引用，请先删除对应加密存储`;
    }
    if (item.usedByJobs.length > 0) {
      return `存储 "${item.name}" 正被同步任务「${item.usedByJobs[0]}」使用，请先修改或删除关联任务`;
    }
    this.nativeRemotes.delete(item.name);
    const eng = this.syncNativeEngineMetadata();
    if (eng) this.engineHealthMap.delete(eng.id);
    return null;
  }

  previewNativeImport(config: string): NativeImportPreview | string {
    const cleaned = config.replace(/^\uFEFF/, "").trim();
    if (!cleaned) return "请先选择非空的 rclone.conf 文件";
    const parsed = parseMockINI(cleaned);
    if (typeof parsed === "string") return parsed;
    const supported = new Set([
      "local",
      "s3",
      "webdav",
      "sftp",
      "smb",
      "ftp",
      "http",
      "b2",
      "onedrive",
      "drive",
      "dropbox",
      "box",
      "pcloud",
      "mega",
      "azureblob",
      "azurefiles",
      "googlecloudstorage",
      "swift",
      "oracleobjectstorage",
      "qingstor",
      "storj",
      "yandex",
      "koofr",
      "seafile",
      "pikpak",
      "protondrive",
      "iclouddrive",
      "jottacloud",
      "filen",
      "hidrive",
      "huaweidrive",
      "mailru",
      "fichier",
      "gofile",
      "pixeldrain",
      "crypt",
      "chunker",
      "compress",
      "hasher",
      "alias",
      "archive",
      "union",
      "combine",
    ]);
    const oauthTypes = new Set([
      "onedrive",
      "drive",
      "dropbox",
      "box",
      "pcloud",
      "yandex",
      "jottacloud",
      "hidrive",
      "huaweidrive",
    ]);
    const items: NativeImportCandidate[] = Object.entries(parsed)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, params]) => {
        const typ = params.type || "";
        const existingKey = [...this.nativeRemotes.keys()].find(
          (k) => k.toLowerCase() === name.toLowerCase(),
        );
        if (!supported.has(typ)) {
          return {
            name,
            type: typ,
            status: "invalid",
            canReplace: false,
            summary: mockNativeSummary(params),
            options: {},
            configuredSecrets: [],
            error: `存储 "${name}" 的后端类型不受支持`,
          };
        }
        if (oauthTypes.has(typ)) {
          let validToken = false;
          if (params.token) {
            try {
              const obj = JSON.parse(params.token) as Record<string, unknown>;
              validToken = Boolean(obj && (obj.access_token || obj.refresh_token));
            } catch {
              validToken = false;
            }
          }
          if (!validToken) {
            return {
              name,
              type: typ,
              status: "invalid",
              canReplace: false,
              summary: mockNativeSummary(params),
              options: {},
              configuredSecrets: [],
              error: `存储 "${name}" 缺少有效的 OAuth Token JSON`,
            };
          }
        }
        if (existingKey) {
          const oldType = this.nativeRemotes.get(existingKey)?.type || "";
          if (oldType === typ) {
            return {
              name,
              type: typ,
              status: "conflict",
              existingName: existingKey,
              existingType: oldType,
              canReplace: true,
              summary: mockNativeSummary(params),
              options: {},
              configuredSecrets: [],
            };
          }
          return {
            name,
            type: typ,
            status: "incompatible",
            existingName: existingKey,
            existingType: oldType,
            canReplace: false,
            summary: mockNativeSummary(params),
            options: {},
            configuredSecrets: [],
            error: `已存在同名存储 "${existingKey}"（类型 ${oldType}），无法用 ${typ} 类型覆盖`,
          };
        }
        return {
          name,
          type: typ,
          status: "ready",
          canReplace: false,
          summary: mockNativeSummary(params),
          options: {},
          configuredSecrets: [],
        };
      });
    return { encrypted: false, items };
  }

  importNativeRemotes(input: {
    config: string;
    selected?: string[];
    replace?: boolean;
  }): NativeImportResult | string {
    const cleaned = (input.config || "").replace(/^\uFEFF/, "").trim();
    if (!cleaned) return "请先选择非空的 rclone.conf 文件";
    const parsed = parseMockINI(cleaned);
    if (typeof parsed === "string") return parsed;
    const filter = new Set(
      (input.selected || []).map((s) => s.trim().toLowerCase()).filter(Boolean),
    );
    const imported: string[] = [];
    const replaced: string[] = [];
    for (const [name, params] of Object.entries(parsed)) {
      if (filter.size > 0 && !filter.has(name.toLowerCase())) continue;
      const existingKey = [...this.nativeRemotes.keys()].find(
        (k) => k.toLowerCase() === name.toLowerCase(),
      );
      if (existingKey) {
        if (!input.replace) {
          return `存储 "${existingKey}" 已存在，请勾选覆盖同名存储后重试`;
        }
        if (this.nativeRemotes.get(existingKey)?.type !== params.type) {
          return "同名存储的类型不能修改";
        }
        this.nativeRemotes.set(existingKey, { ...params });
        replaced.push(existingKey);
      } else {
        this.nativeRemotes.set(name, { ...params });
        imported.push(name);
      }
    }
    if (imported.length === 0 && replaced.length === 0) {
      return "请至少选择一个可导入的存储配置";
    }
    const eng = this.ensureNativeEngine();
    this.engineHealthMap.delete(eng.id);
    return { imported, replaced };
  }

  testNativeRemote(input: {
    name?: string;
    options?: Record<string, string>;
  }): NativeStorageTestResult {
    if (input.name === "本地" && !input.options) {
      return {
        ok: true,
        latencyMs: 6,
        message: `已检测到 ${this.nativeLocalRoots.length} 个本地授权目录`,
        dirsCount: this.nativeLocalRoots.length,
      };
    }
    return {
      ok: true,
      latencyMs: 18,
      message: "连接正常，可读取 3 个目录/存储桶",
      dirsCount: 3,
    };
  }

  renameNativeRemote(
    oldName: string,
    newName: string,
    options?: Record<string, string>,
  ): NativeRenameResult | string {
    const oldTrim = oldName.trim();
    const newTrim = newName.trim();
    if (!oldTrim || !newTrim) return "请提供原存储名称和新存储名称";
    const actualOld = [...this.nativeRemotes.keys()].find(
      (k) => k.toLowerCase() === oldTrim.toLowerCase(),
    );
    if (!actualOld) return `存储 "${oldTrim}" 不存在`;
    const conflict = [...this.nativeRemotes.keys()].find(
      (k) => k !== actualOld && k.toLowerCase() === newTrim.toLowerCase(),
    );
    if (conflict) return `存储 "${conflict}" 已存在`;
    const params = options ?? this.nativeRemotes.get(actualOld)!;
    this.nativeRemotes.delete(actualOld);
    this.nativeRemotes.set(newTrim, params);
    for (const [k, other] of this.nativeRemotes.entries()) {
      if (other.remote) {
        const colon = other.remote.indexOf(":");
        if (
          colon > 0 &&
          other.remote.slice(0, colon).toLowerCase() === actualOld.toLowerCase()
        ) {
          other.remote = `${newTrim}${other.remote.slice(colon)}`;
          this.nativeRemotes.set(k, other);
        }
      }
    }
    let updatedJobs = 0;
    const engine = this.syncNativeEngineMetadata();
    if (engine) {
      const rewrite = (p: string) => {
        const trimmed = p.replace(/^\/+/, "");
        const idx = trimmed.indexOf("/");
        const seg = idx >= 0 ? trimmed.slice(0, idx) : trimmed;
        const rest = idx >= 0 ? trimmed.slice(idx + 1) : "";
        if (seg.toLowerCase() !== actualOld.toLowerCase()) return p;
        return rest ? `/${newTrim}/${rest}` : `/${newTrim}`;
      };
      for (const job of this.jobs) {
        if (job.alistId !== engine.id) continue;
        const src = parsePathList(job.srcPath);
        const dst = parsePathList(job.dstPath);
        const nextSrc = src.map(rewrite);
        const nextDst = dst.map(rewrite);
        if (
          JSON.stringify(src) !== JSON.stringify(nextSrc) ||
          JSON.stringify(dst) !== JSON.stringify(nextDst)
        ) {
          job.srcPath = JSON.stringify(nextSrc);
          job.dstPath = JSON.stringify(nextDst);
          updatedJobs += 1;
        }
      }
      this.engineHealthMap.delete(engine.id);
    }
    return { oldName: actualOld, newName: newTrim, updatedJobs };
  }

  exportNativeConfig(
    names?: string[],
    password?: string,
  ): NativeExportResult | string {
    const selected =
      names && names.length > 0
        ? names
        : [...this.nativeRemotes.keys()].sort((a, b) => a.localeCompare(b));
    const blocks: string[] = [];
    const skippedBuiltIn: string[] = [];
    for (const name of selected) {
      const actual = [...this.nativeRemotes.keys()].find(
        (k) => k.toLowerCase() === name.toLowerCase(),
      );
      if (!actual) continue;
      const params = this.nativeRemotes.get(actual)!;
      if (params.type === "quark" || params.type === "pan123") {
        if (names && names.length > 0) {
          return `存储 "${actual}" 使用 OpenSync 内置网盘驱动，外部官方 rclone 暂不支持直接导出`;
        }
        skippedBuiltIn.push(actual);
        continue;
      }
      const openSyncMetaFields = new Set([
        "insecure_skip_verify",
        "proxy_url",
        "allow_hashcheck",
        "max_concurrent_jobs",
        "managed_by_job",
        "managed_job_label",
      ]);
      const lines = [`[${actual}]`];
      for (const [k, v] of Object.entries(params)) {
        if (openSyncMetaFields.has(k)) continue;
        lines.push(`${k} = ${v}`);
      }
      blocks.push(lines.join("\n"));
    }
    if (blocks.length === 0) {
      return skippedBuiltIn.length > 0
        ? "当前仅包含 OpenSync 内置网盘驱动（如夸克网盘、123云盘），暂不支持导出到外部官方 rclone"
        : "没有可导出的远端存储配置";
    }
    const header =
      skippedBuiltIn.length > 0
        ? `# OpenSync 提示：已自动跳过内置驱动存储（外部官方 rclone 暂不支持）：${skippedBuiltIn.join(", ")}\n\n`
        : "";
    return {
      content: password
        ? `# Encrypted rclone configuration\n# RCLONE_ENCRYPT_V0:\n${btoa(header + blocks.join("\n\n"))}\n`
        : `${header}${blocks.join("\n\n")}\n`,
      encrypted: Boolean(password),
      filename: "rclone-export.conf",
    };
  }

  discoverOpenList(input?: {
    ports?: number[];
    endpoints?: string[];
  }): NativeOpenListDiscoverResult | string {
    for (const raw of input?.endpoints || []) {
      try {
        const u = new URL(raw);
        const h = u.hostname.toLowerCase();
        if (
          (u.protocol !== "http:" && u.protocol !== "https:") ||
          (h !== "127.0.0.1" && h !== "localhost" && h !== "::1" && h !== "[::1]")
        ) {
          return "出于安全限制，仅允许探测本机回环地址（127.0.0.1、localhost 或 [::1]）";
        }
      } catch {
        return "本机探测地址格式无效";
      }
    }
    const endpoint =
      input?.endpoints?.[0] ||
      (input?.ports?.[0]
        ? `http://127.0.0.1:${input.ports[0]}/dav`
        : "http://127.0.0.1:5244/dav");
    const baseUrl = endpoint.replace(/\/dav\/?$/, "");
    return {
      found: true,
      message: `检测到本机 WebDAV 服务：${endpoint}（响应 6ms），WebDAV 已启用鉴权，填充后请填写 OpenList / AList 用户名与密码。`,
      suggestedConfig: {
        name: "openlist",
        type: "webdav",
        vendor: "other",
        url: endpoint,
        compareMode: 1,
        compareModeLabel: "仅按文件大小比对",
        recommendationNote:
          "建议保持 WebDAV 类型为「标准 WebDAV / OpenList / AList（other）」；创建以它为目标的同步任务时建议配合「仅按文件大小」比对，避免网盘文件触发逐文件下载校验。",
      },
      candidates: [
        {
          endpoint,
          baseUrl,
          reachable: true,
          statusCode: 401,
          authRequired: true,
          latencyMs: 6,
          serverType: "OpenList",
        },
      ],
      checkedAt: now(),
    };
  }

  getCryptRecovery(input: {
    name: string;
    verifyPassword?: string;
    verifyPassword2?: string;
  }): NativeCryptRecoveryResult | string {
    const trimmed = input.name.trim();
    if (!trimmed) return "请指定要导出密钥恢复包的加密存储名称";
    const actual = [...this.nativeRemotes.keys()].find(
      (k) => k.toLowerCase() === trimmed.toLowerCase(),
    );
    if (!actual) return `未找到远程存储「${trimmed}」`;
    const p = this.nativeRemotes.get(actual)!;
    if (p.type !== "crypt") {
      return `「${actual}」不是加密存储（crypt），无法生成密钥恢复包`;
    }
    const remote = p.remote || "";
    const colonIdx = remote.indexOf(":");
    const underlyingRemote = colonIdx >= 0 ? remote.slice(0, colonIdx) : remote;
    const underlyingSubPath = colonIdx >= 0 ? remote.slice(colonIdx + 1) : "";
    const fnEnc = p.filename_encryption || "standard";
    const fnEncoding = p.filename_encoding || "base32";
    const dirEnc = p.directory_name_encryption || "true";
    const noDataEnc = p.no_data_encryption === "true";
    const suffix = p.suffix || "";
    const hasPassword = Boolean(p.password);
    const hasPassword2 = Boolean(p.password2);
    const managedByJob = p.managed_by_job === "true";
    const managedJobLabel = p.managed_job_label || "";

    let passwordChecked = false;
    let passwordVerified: boolean | undefined;
    let password2Checked = false;
    let password2Verified: boolean | undefined;
    const verifyParts: string[] = [];

    if (input.verifyPassword) {
      passwordChecked = true;
      passwordVerified = Boolean(p.password) && p.password === input.verifyPassword;
      verifyParts.push(
        passwordVerified
          ? "主加密密码验证通过"
          : "主加密密码与当前配置不一致",
      );
    }
    if (input.verifyPassword2) {
      password2Checked = true;
      if (!hasPassword2) {
        password2Verified = false;
        verifyParts.push("当前存储未设置加密盐（password2）");
      } else {
        password2Verified = p.password2 === input.verifyPassword2;
        verifyParts.push(
          password2Verified
            ? "加密盐（password2）验证通过"
            : "加密盐（password2）与当前配置不一致",
        );
      }
    }

    const rcloneSnippetTemplate = [
      `[${actual}]`,
      "type = crypt",
      `remote = ${remote}`,
      `filename_encryption = ${fnEnc}`,
      `filename_encoding = ${fnEncoding}`,
      `directory_name_encryption = ${dirEnc}`,
      ...(noDataEnc ? ["no_data_encryption = true"] : []),
      ...(suffix ? [`suffix = ${suffix}`] : []),
      "# password = <请填入你的主加密密码，或通过 `rclone obscure -` 生成的混淆值>",
      ...(hasPassword2
        ? [
            "# password2 = <请填入你的加密盐（password2），或通过 `rclone obscure -` 生成的混淆值>",
          ]
        : []),
      "",
    ].join("\n");

    const rcloneCommand = `rclone copy ${actual}: ./restored-${actual}`;
    const recoverySteps = [
      `在 OpenSync 恢复：进入「存储管理」找到「${actual}」并点击「浏览与恢复」，或在新建同步任务时将源路径选为「/${actual}」、目标路径选为本地目录即可解密还原。`,
      `异机或离线恢复：在任意安装 rclone 的设备上先配置同名底层存储（${underlyingRemote}），再按本恢复包中的 rclone.conf 段落重建 crypt 配置并填入主密码（与加密盐）。`,
      `命令行还原示例：${rcloneCommand}（将先解密文件名与文件内容后下载到本地目录）。`,
    ];
    const warning =
      "安全提醒：主加密密码与加密盐是解密云端文件的唯一凭证，一旦丢失任何人都无法还原文件。若在导出的备份中包含明文密码，请务必存放在密码管理器或离线安全介质中，切勿直接保存在同一云盘目录下。";
    const generatedAt = now();
    const bundle = {
      version: 1,
      generatedAt,
      remoteName: actual,
      underlyingRemote,
      ...(underlyingSubPath ? { underlyingSubPath } : {}),
      remote,
      filenameEncryption: fnEnc,
      filenameEncoding: fnEncoding,
      directoryNameEncryption: dirEnc,
      noDataEncryption: noDataEnc,
      ...(suffix ? { suffix } : {}),
      hasPassword,
      hasPassword2,
      managedByJob,
      ...(managedJobLabel ? { managedJobLabel } : {}),
      rcloneSnippetTemplate,
      rcloneCommand,
      recoverySteps,
      warning,
    };
    const recoveryText = [
      `=== OpenSync 加密存储密钥恢复包：${actual} ===`,
      `底层路径：${remote}`,
      `文件名加密方式：${fnEnc}`,
      `文件名编码：${fnEncoding}`,
      `目录名加密：${dirEnc}`,
      `是否设置加密盐（password2）：${hasPassword2}`,
      "",
      "--- rclone.conf 配置模板 ---",
      rcloneSnippetTemplate,
      "--- 解密恢复步骤 ---",
      ...recoverySteps.map((s, idx) => `${idx + 1}. ${s}`),
      "",
      warning,
    ].join("\n");

    return {
      name: actual,
      passwordChecked,
      ...(passwordChecked ? { passwordVerified } : {}),
      password2Checked,
      ...(password2Checked ? { password2Verified } : {}),
      ...(verifyParts.length > 0 ? { verifyMessage: verifyParts.join("；") } : {}),
      bundle,
      recoveryText,
    };
  }

  aboutNativeStorage(name: string): NativeAboutInfo | string {
    const trimmed = name.trim();
    if (!trimmed) return "请提供存储名称";
    if (trimmed === "本地") {
      return {
        supported: true,
        total: 4 * 1024 * 1024 * 1024 * 1024,
        used: 1.2 * 1024 * 1024 * 1024 * 1024,
        free: 2.8 * 1024 * 1024 * 1024 * 1024,
      };
    }
    const actual = [...this.nativeRemotes.keys()].find(
      (k) => k.toLowerCase() === trimmed.toLowerCase(),
    );
    if (!actual) return `存储 "${trimmed}" 不存在`;
    return {
      supported: true,
      total: 1024 * 1024 * 1024 * 1024,
      used: 256 * 1024 * 1024 * 1024,
      free: 768 * 1024 * 1024 * 1024,
    };
  }

  browseNativeStorage(path: string): NativeBrowseResult | string {
    const trimmed = (path || "/").trim() || "/";
    if (trimmed === "/") {
      const entries = [
        {
          name: "本地",
          path: "/本地",
          isDir: true,
          size: 0,
          modTime: now(),
        },
        ...[...this.nativeRemotes.keys()]
          .sort((a, b) => a.localeCompare(b))
          .map((name) => ({
            name,
            path: `/${name}`,
            isDir: true,
            size: 0,
            modTime: now(),
          })),
      ];
      return { path: "/", entries };
    }
    const clean = trimmed.replace(/\/+$/, "");
    return {
      path: clean,
      entries: [
        {
          name: "2026",
          path: `${clean}/2026`,
          isDir: true,
          size: 0,
          modTime: now() - 3600,
        },
        {
          name: "backup-report.txt",
          path: `${clean}/backup-report.txt`,
          isDir: false,
          size: 4096,
          modTime: now() - 1800,
        },
      ],
    };
  }

  restoreNativeFile(input: {
    srcPath: string;
    dstLocalDir: string;
  }): string | null {
    if (!input.srcPath?.trim() || !input.dstLocalDir?.trim()) {
      return "请提供要恢复的远端路径和本地目标目录";
    }
    return null;
  }

  mkdirNativeStorage(path: string): string | null {
    const clean = (path || "").trim().replace(/\/+$/, "");
    if (!clean || clean === "/") return "请提供有效目录路径";
    const lastSlash = clean.lastIndexOf("/");
    const parent = lastSlash <= 0 ? "/" : clean.slice(0, lastSlash);
    const child = lastSlash >= 0 ? clean.slice(lastSlash + 1) : clean;
    if (child && child !== "." && child !== "..") {
      const existing = this.customDirs.get(parent) || [];
      if (!existing.includes(child)) {
        this.customDirs.set(parent, [...existing, child]);
      }
    }
    return null;
  }

  moveNativeStoragePath(input: {
    srcPath: string;
    dstPath: string;
    isDir?: boolean;
  }): string | null {
    if (!input.srcPath?.trim() || !input.dstPath?.trim()) {
      return "请提供源路径与目标路径";
    }
    return null;
  }

  deleteNativeStoragePath(input: {
    path: string;
    isDir?: boolean;
  }): string | null {
    if (!input.path?.trim() || input.path.trim() === "/") return "请提供有效路径";
    return null;
  }

  sizeNativeStorage(path: string): NativeSizeInfo | string {
    if (!path?.trim() || path.trim() === "/") return "请提供有效路径";
    return { count: 18, bytes: 128 * 1024 * 1024, sizeless: 0 };
  }

  listJobConflicts(jobId: number, _includeResolved = false) {
    const job = this.jobs.find((j) => j.id === jobId);
    if (!job) return [];
    return [];
  }

  resolveJobConflict(jobId: number, _conflictId: number, _action: string): string | null {
    const job = this.jobs.find((j) => j.id === jobId);
    if (!job) return "任务不存在";
    return null;
  }

  startJobCheck(
    jobId: number,
    download: boolean,
    cryptCheck = false,
  ): JobCheckResult | null {
    const job = this.jobs.find((j) => j.id === jobId);
    if (!job) return null;
    const mode = cryptCheck ? "cryptcheck" : download ? "download" : "check";
    const result: JobCheckResult = {
      jobId,
      status: "completed",
      mode,
      download: !cryptCheck && download,
      cryptCheck,
      startedAt: now() - 1,
      finishedAt: now(),
      checkedFiles: 24,
      totalFiles: 24,
      matchedCount: 24,
      differCount: 0,
      missingOnSrcCount: 0,
      missingOnDstCount: 0,
      errorCount: 0,
      differ: [],
      missingOnSrc: [],
      missingOnDst: [],
      errors: [],
      truncated: false,
    };
    this.jobChecks.set(jobId, result);
    return result;
  }

  getJobCheck(jobId: number): JobCheckResult | null {
    return this.jobChecks.get(jobId) ?? null;
  }

  cancelJobCheck(jobId: number): void {
    const existing = this.jobChecks.get(jobId);
    if (existing) {
      this.jobChecks.set(jobId, { ...existing, status: "cancelled" });
    }
  }

  listJobs(pageNum = 1, pageSize = 500): PageData<JobItem> {
    return {
      dataList: pageDemoRows(this.jobs, pageNum, pageSize).map((job) => {
        const current = this.currentTasks.get(job.id);
        const latest = (this.histories.get(job.id) || [])[0];
        return {
          ...job,
          running: Boolean(current),
          paused: Boolean(current?.paused),
          queued: false,
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
    const task: CurrentTaskData = {
      ...base,
      taskId,
    };
    this.currentTasks.set(jobId, task);
    this.currentItems.set(jobId, items);
    this.taskItems.set(taskId, items);
    return { ...task };
  }

  getCurrentTask(jobId: number): CurrentTaskData | null {
    const task = this.currentTasks.get(jobId);
    if (!task) return null;
    if (task.paused) {
      return {
        ...task,
        speed: 0,
        remainTime: 0,
      };
    }
    return { ...task };
  }

  pauseTask(taskId: number): void {
    for (const task of this.currentTasks.values()) {
      if (task.taskId === taskId) {
        task.paused = true;
        return;
      }
    }
  }

  resumeTask(taskId: number): void {
    for (const task of this.currentTasks.values()) {
      if (task.taskId === taskId) {
        task.paused = false;
        return;
      }
    }
  }

  setTaskLiveBwLimit(taskId: number, bwLimit: string): void {
    const trimmed = bwLimit.trim();
    const effective =
      trimmed === "" || trimmed === "0" || trimmed.toLowerCase() === "off"
        ? ""
        : trimmed;
    for (const task of this.currentTasks.values()) {
      if (task.taskId === taskId) {
        task.liveBwLimit = effective;
        return;
      }
    }
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
    bwLimit?: string;
  }): void {
    if (input.taskId !== undefined) {
      const taskId = numberValue(input.taskId, 0);
      if (input.action === "stop") this.stopTask(taskId);
      if (input.action === "retry") this.retryTask(taskId);
      if (input.action === "pause") this.pauseTask(taskId);
      if (input.action === "resume") this.resumeTask(taskId);
      if (input.action === "bwlimit") {
        this.setTaskLiveBwLimit(taskId, input.bwLimit ?? "");
      }
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

  listRecentTaskItems(
    params: {
      pageNum?: number;
      pageSize?: number;
      jobId?: number;
      jobIds?: number[];
      storage?: string;
      status?: number;
      type?: number;
      isPath?: number;
      hasError?: number;
      keyword?: string;
    } = {},
  ): RecentTaskItemsPageData {
    const pageNum = Math.max(1, params.pageNum || 1);
    const pageSize = Math.min(500, Math.max(1, params.pageSize || 20));
    const emptyStats: RecentTaskItemStats = {
      allNum: 0,
      waitNum: 0,
      runningNum: 0,
      successNum: 0,
      failNum: 0,
      otherNum: 0,
      deleteNum: 0,
      errorNum: 0,
      sumSize: 0,
    };

    const legacyEngineIds = new Set(
      this.engines.filter((e) => e.kind !== "rclone").map((e) => e.id),
    );
    let jobs = this.jobs.filter((job) => !legacyEngineIds.has(job.alistId));
    if (params.jobId !== undefined) {
      jobs = jobs.filter((job) => job.id === params.jobId);
    } else if (params.jobIds !== undefined) {
      const allowed = new Set(params.jobIds);
      jobs = jobs.filter((job) => allowed.has(job.id));
    } else if (params.storage?.trim()) {
      const targetStorage = params.storage.trim();
      jobs = jobs.filter((job) => jobMatchesStorage(job, targetStorage));
    }

    const enriched: RecentTaskItem[] = [];
    for (const job of jobs) {
      const jobName = job.remark?.trim() || `同步任务 #${job.id}`;
      const seenTaskIds = new Set<number>();
      const current = this.currentTasks.get(job.id);
      if (current) seenTaskIds.add(current.taskId);
      for (const rec of this.histories.get(job.id) || []) {
        seenTaskIds.add(rec.id);
      }
      for (const taskId of seenTaskIds) {
        const items = this.taskItems.get(taskId) || [];
        for (const item of items) {
          enriched.push({
            ...item,
            taskId: item.taskId ?? taskId,
            jobId: job.id,
            jobName,
            jobMethod: job.method,
          });
        }
      }
    }

    const keyword = params.keyword?.trim().toLowerCase() || "";
    const filtered = enriched
      .filter((item) => {
        if (params.status !== undefined && !includesStatus(item.status, params.status)) {
          return false;
        }
        if (params.type !== undefined && item.type !== params.type) {
          return false;
        }
        if (params.isPath !== undefined && item.isPath !== params.isPath) {
          return false;
        }
        if (params.hasError === 1 && !item.errMsg) return false;
        if (params.hasError === 0 && item.errMsg) return false;
        if (keyword) {
          const text = `${item.fileName || ""} ${item.srcPath || ""} ${
            item.dstPath || ""
          } ${item.errMsg || ""} ${item.jobName}`.toLowerCase();
          if (!text.includes(keyword)) return false;
        }
        return true;
      })
      .sort(
        (a, b) =>
          (b.createTime ?? 0) - (a.createTime ?? 0) ||
          Number(b.id ?? 0) - Number(a.id ?? 0),
      );

    const stats = filtered.reduce<RecentTaskItemStats>(
      (acc, item) => {
        acc.allNum += 1;
        if (item.status === 0) acc.waitNum += 1;
        else if (item.status === 1) acc.runningNum += 1;
        else if (item.status === 2) acc.successNum += 1;
        else if (item.status === 7) acc.failNum += 1;
        else acc.otherNum += 1;
        if (item.type === 1) acc.deleteNum += 1;
        if (item.errMsg) acc.errorNum += 1;
        if (item.status === 2 && item.type !== 1) {
          acc.sumSize += item.fileSize || 0;
        }
        return acc;
      },
      { ...emptyStats },
    );

    const start = (pageNum - 1) * pageSize;
    return {
      dataList: filtered.slice(start, start + pageSize),
      count: filtered.length,
      pageNum,
      pageSize,
      stats,
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
    let runningJobs = 0;
    let activeJobs = 0;
    let pausedJobs = 0;
    let enabledJobs = 0;
    let totalSpeed = 0;
    let totalSpeedAvg = 0;
    let totalDoneBytes = 0;
    let totalRemainBytes = 0;
    const jobSummaries = this.jobs.map((job) => {
      if (job.enable === 1) enabledJobs += 1;
      const current = this.currentTasks.get(job.id);
      if (current) runningJobs += 1;
      const engine = this.engines.find((e) => e.id === job.alistId);
      const latest = (this.histories.get(job.id) || [])[0];
      const done = current?.doneSize ?? 0;
      const remain = current?.remainSize ?? 0;
      const total = done + remain;
      const paused = Boolean(current?.paused);
      const liveSpeed = current && !paused ? (current.speed ?? 0) : 0;
      const liveSpeedAvg = current ? (current.speedAvg ?? 0) : 0;
      const liveRemainTime = current && !paused ? (current.remainTime ?? 0) : 0;
      if (current) {
        if (paused) {
          pausedJobs += 1;
        } else {
          activeJobs += 1;
          totalSpeed += liveSpeed;
          totalSpeedAvg += liveSpeedAvg;
          totalDoneBytes += done;
          totalRemainBytes += remain;
        }
      }
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
        paused,
        queued: false,
        currentTaskId: current?.taskId ?? 0,
        liveNum: current?.num ?? null,
        liveProgress: total > 0 ? (done * 100) / total : 0,
        liveDoneSize: done,
        liveTotalSize: total,
        liveSpeed,
        liveSpeedAvg,
        liveRemainTime,
        liveBwLimit: current?.liveBwLimit ?? "",
        liveScanFinish: current ? current.scanFinish : true,
        liveScan: current?.scan ?? null,
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
    const sampledAt = now();
    const rate =
      totalSpeed > 0 && totalSpeedAvg > 0
        ? 0.6 * totalSpeed + 0.4 * totalSpeedAvg
        : totalSpeed || totalSpeedAvg;
    const estimatedRemainTime =
      activeJobs > 0 && totalRemainBytes > 0 && rate > 0
        ? Math.ceil(totalRemainBytes / rate)
        : 0;
    return {
      kpi: {
        totalJobs: this.jobs.length,
        enabledJobs,
        runningJobs,
        queuedJobs: 0,
        totalEngines: this.engines.length,
        onlineEngines: healths.filter((h) => h.status === "online").length,
        offlineEngines: healths.filter((h) => h.status === "offline").length,
      },
      realtime: {
        totalSpeed,
        totalSpeedAvg,
        peakSpeed: totalSpeed,
        activeJobs,
        pausedJobs,
        activeTransfers: 0,
        totalDoneBytes,
        totalRemainBytes,
        estimatedRemainTime,
        globalBwLimit: this.settings.defaultBwLimit || "",
        sampledAt,
        speedHistory: [{ ts: sampledAt, speed: totalSpeed }],
      },
      recent7d: {
        totalRuns,
        successRuns,
        failRuns,
        stoppedRuns,
        totalFiles,
        totalBytes,
      },
      jobs: jobSummaries,
    };
  }

  batchJobAction(action: string, ids?: number[]): BatchJobActionResult {
    const normalizedAction = action.trim().toLowerCase();
    const isBulkAll =
      normalizedAction === "run_all" ||
      normalizedAction === "stop_all" ||
      normalizedAction === "enable_all" ||
      normalizedAction === "disable_all";
    const results: BatchJobActionResult["results"] = [];
    const skippedJobs: BatchJobSkippedItem[] = [];
    let skipped = 0;
    const abortTargetIds = new Set<number>();

    let targetIds: number[];
    if (isBulkAll || !ids?.length) {
      const legacyEngineIds = new Set(
        this.engines.filter((e) => e.kind !== "rclone").map((e) => e.id),
      );
      targetIds = [];
      for (const job of this.jobs) {
        if (isBulkAll && legacyEngineIds.has(job.alistId)) continue;
        if (normalizedAction === "run_all" && job.enable !== 1) continue;
        if (normalizedAction === "enable_all") {
          if (job.enable === 1) continue;
          const hasLastSuccess =
            (job.syncStatus?.lastSuccess ?? 0) > 0 ||
            (this.histories.get(job.id) || []).some((rec) => rec.status === 2);
          if (job.method === 1 && job.isCron === 3 && !hasLastSuccess) {
            const jobName = job.remark?.trim() || `同步任务 #${job.id}`;
            const reason = "未完成首次镜像影响预览，已安全跳过";
            skipped += 1;
            skippedJobs.push({ jobId: job.id, jobName, reason });
            results.push({
              jobId: job.id,
              ok: false,
              skipped: true,
              error: reason,
            });
            continue;
          }
        } else if (normalizedAction === "disable_all") {
          const running = this.currentTasks.has(job.id);
          if (job.isCron === 2) {
            if (!running) continue;
            abortTargetIds.add(job.id);
          } else if (job.enable !== 1 && !running) {
            continue;
          }
        }
        targetIds.push(job.id);
      }
    } else {
      targetIds = ids;
    }

    let succeeded = 0;
    let failed = 0;
    for (const id of targetIds) {
      const job = this.jobs.find((j) => j.id === id);
      if (!job) {
        failed += 1;
        results.push({ jobId: id, ok: false, error: "任务不存在" });
        continue;
      }
      if (normalizedAction === "run" || normalizedAction === "run_all") {
        this.runJob(id);
      } else if (
        normalizedAction === "stop" ||
        normalizedAction === "stop_all"
      ) {
        this.abortJob(id);
      } else if (
        normalizedAction === "enable" ||
        normalizedAction === "enable_all"
      ) {
        job.enable = 1;
      } else if (
        normalizedAction === "disable" ||
        normalizedAction === "disable_all"
      ) {
        if (abortTargetIds.has(id)) this.abortJob(id);
        else this.pauseJob(id);
      }
      succeeded += 1;
      results.push({ jobId: id, ok: true });
    }
    return {
      action: normalizedAction,
      succeeded,
      failed,
      skipped,
      skippedJobs,
      results,
    };
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
