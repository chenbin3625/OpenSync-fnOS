import type { JobItem, NativeExtraFlags, NativeRemoteItem } from "../types";
import {
  defaultCronFields,
  defaultExclude,
  formatSize,
  parseJobPathList,
} from "../pages/Home/homeUtils";
import {
  fileSizeToBytes,
  fileSizeInputToBytes,
  parseFileSizeInput,
  splitBytesToFileSize,
} from "../pages/Home/fileSizeUnits";
import type { CronFieldKey } from "./schedule";
import {
  cleanPathList,
  syncPathContains,
  validateSyncPathSelection,
  type ExistingJobPathSummary,
} from "./syncPathPreview";
import { validateBwLimitExpression } from "./bwLimit";

function parseNotifyIdList(value: unknown): number[] {
  if (Array.isArray(value)) {
    return Array.from(
      new Set(
        value
          .map((item) => Number(item))
          .filter((id) => Number.isInteger(id) && id > 0),
      ),
    );
  }
  if (typeof value === "string" && value.trim()) {
    try {
      const parsed: unknown = JSON.parse(value);
      if (Array.isArray(parsed)) return parseNotifyIdList(parsed);
    } catch {
      return Array.from(
        new Set(
          value
            .split(",")
            .map((part) => Number(part.trim()))
            .filter((id) => Number.isInteger(id) && id > 0),
        ),
      );
    }
  }
  return [];
}

function emptyNativeExtraFlags(): Required<NativeExtraFlags> {
  return {
    noUpdateModtime: false,
    inplace: false,
    checkFirst: false,
    s3NoHead: false,
    ignoreSize: false,
    trackRenames: false,
    useMmap: false,
    maxTransfer: "",
    cutoffMode: "",
    maxDuration: "",
    orderBy: "",
    maxBacklog: 0,
    bufferSize: "",
    multiThreadCutoff: "",
    multiThreadChunkSize: "",
    userAgent: "",
    ioTimeout: "",
    conTimeout: "",
    lowLevelRetries: 0,
    retriesSleep: "",
    resumeEnable: false,
    resumeMinSizeMB: 0,
    watchEnable: false,
    watchDebounceSec: 0,
    syncPollIntervalSec: 0,
    syncAuditIntervalMin: 0,
    autoRetryMax: 0,
    autoRetryDelayMin: 0,
    postSyncJobId: 0,
    checkSchedule: "",
    missedCatchUp: false,
    directStream: false,
    bisyncMaxDelete: 0,
    bisyncForce: false,
  };
}

function parseNativeExtraFlags(value: unknown): Required<NativeExtraFlags> {
  const empty = emptyNativeExtraFlags();
  if (!value) return empty;
  let obj: Record<string, unknown> | null = null;
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (!trimmed) return empty;
    try {
      const parsed: unknown = JSON.parse(trimmed);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        obj = parsed as Record<string, unknown>;
      }
    } catch {
      return empty;
    }
  } else if (typeof value === "object" && !Array.isArray(value)) {
    obj = value as Record<string, unknown>;
  }
  if (!obj) return empty;
  const str = (k: string) =>
    typeof obj![k] === "string" ? String(obj![k]).trim() : "";
  const num = (k: string) => {
    const n = Number(obj![k] ?? 0);
    return Number.isFinite(n) && n > 0 ? Math.trunc(n) : 0;
  };
  return {
    noUpdateModtime: Boolean(obj.noUpdateModtime),
    inplace: Boolean(obj.inplace),
    checkFirst: Boolean(obj.checkFirst),
    s3NoHead: Boolean(obj.s3NoHead),
    ignoreSize: Boolean(obj.ignoreSize),
    trackRenames: Boolean(obj.trackRenames),
    useMmap: Boolean(obj.useMmap),
    maxTransfer: str("maxTransfer"),
    cutoffMode: str("cutoffMode"),
    maxDuration: str("maxDuration"),
    orderBy: str("orderBy"),
    maxBacklog: num("maxBacklog"),
    bufferSize: str("bufferSize"),
    multiThreadCutoff: str("multiThreadCutoff"),
    multiThreadChunkSize: str("multiThreadChunkSize"),
    userAgent: str("userAgent"),
    ioTimeout: str("ioTimeout"),
    conTimeout: str("conTimeout"),
    lowLevelRetries: Math.min(50, num("lowLevelRetries")),
    retriesSleep: str("retriesSleep"),
    resumeEnable: Boolean(obj.resumeEnable),
    resumeMinSizeMB: Math.min(10240, num("resumeMinSizeMB")),
    watchEnable: Boolean(obj.watchEnable),
    watchDebounceSec: Math.min(3600, num("watchDebounceSec")),
    syncPollIntervalSec: num("syncPollIntervalSec"),
    syncAuditIntervalMin: num("syncAuditIntervalMin"),
    autoRetryMax: Math.min(10, num("autoRetryMax")),
    autoRetryDelayMin: Math.min(1440, num("autoRetryDelayMin")),
    postSyncJobId: num("postSyncJobId"),
    checkSchedule: str("checkSchedule"),
    missedCatchUp: Boolean(obj.missedCatchUp),
    directStream: Boolean(obj.directStream),
    bisyncMaxDelete: Math.min(100, num("bisyncMaxDelete")),
    bisyncForce: Boolean(obj.bisyncForce),
  };
}

function serializeNativeExtraFlags(flags: NativeExtraFlags | undefined): string {
  if (!flags) return "";
  const out: NativeExtraFlags = {};
  if (flags.noUpdateModtime) out.noUpdateModtime = true;
  if (flags.inplace) out.inplace = true;
  if (flags.checkFirst) out.checkFirst = true;
  if (flags.s3NoHead) out.s3NoHead = true;
  if (flags.ignoreSize) out.ignoreSize = true;
  if (flags.trackRenames) out.trackRenames = true;
  if (flags.useMmap) out.useMmap = true;
  if (flags.maxTransfer?.trim()) out.maxTransfer = flags.maxTransfer.trim();
  if (flags.cutoffMode?.trim()) out.cutoffMode = flags.cutoffMode.trim();
  if (flags.maxDuration?.trim()) out.maxDuration = flags.maxDuration.trim();
  if (flags.orderBy?.trim()) out.orderBy = flags.orderBy.trim();
  if (Number(flags.maxBacklog || 0) > 0)
    out.maxBacklog = Math.trunc(Number(flags.maxBacklog));
  if (flags.bufferSize?.trim()) out.bufferSize = flags.bufferSize.trim();
  if (flags.multiThreadCutoff?.trim())
    out.multiThreadCutoff = flags.multiThreadCutoff.trim();
  if (flags.multiThreadChunkSize?.trim())
    out.multiThreadChunkSize = flags.multiThreadChunkSize.trim();
  if (flags.userAgent?.trim()) out.userAgent = flags.userAgent.trim();
  if (flags.ioTimeout?.trim()) out.ioTimeout = flags.ioTimeout.trim();
  if (flags.conTimeout?.trim()) out.conTimeout = flags.conTimeout.trim();
  if (Number(flags.lowLevelRetries || 0) > 0)
    out.lowLevelRetries = Math.min(50, Math.trunc(Number(flags.lowLevelRetries)));
  if (flags.retriesSleep?.trim()) out.retriesSleep = flags.retriesSleep.trim();
  if (flags.resumeEnable) out.resumeEnable = true;
  if (Number(flags.resumeMinSizeMB || 0) > 0)
    out.resumeMinSizeMB = Math.min(10240, Math.trunc(Number(flags.resumeMinSizeMB)));
  if (flags.watchEnable) out.watchEnable = true;
  if (flags.syncPollIntervalSec) out.syncPollIntervalSec = Math.trunc(flags.syncPollIntervalSec);
  if (flags.syncAuditIntervalMin) out.syncAuditIntervalMin = Math.trunc(flags.syncAuditIntervalMin);
  if (Number(flags.watchDebounceSec || 0) > 0)
    out.watchDebounceSec = Math.min(3600, Math.trunc(Number(flags.watchDebounceSec)));
  if (Number(flags.autoRetryMax || 0) > 0)
    out.autoRetryMax = Math.min(10, Math.trunc(Number(flags.autoRetryMax)));
  if (Number(flags.autoRetryDelayMin || 0) > 0)
    out.autoRetryDelayMin = Math.min(1440, Math.trunc(Number(flags.autoRetryDelayMin)));
  if (Number(flags.postSyncJobId || 0) > 0)
    out.postSyncJobId = Math.trunc(Number(flags.postSyncJobId));
  if (flags.checkSchedule?.trim())
    out.checkSchedule = flags.checkSchedule.trim();
  if (flags.missedCatchUp) out.missedCatchUp = true;
  if (flags.directStream) out.directStream = true;
  if (Number(flags.bisyncMaxDelete || 0) > 0)
    out.bisyncMaxDelete = Math.min(100, Math.trunc(Number(flags.bisyncMaxDelete)));
  if (flags.bisyncForce) out.bisyncForce = true;
  return Object.keys(out).length > 0 ? JSON.stringify(out) : "";
}

export type TaskScenarioPresetKey =
  | "media"
  | "small_files"
  | "cloud_drive"
  | "cold_archive";

export const taskScenarioPresets: Array<{
  key: TaskScenarioPresetKey;
  label: string;
  desc: string;
}> = [
  {
    key: "media",
    label: "相册 / 影音大文件",
    desc: "开启断点续传、多线程分片、大文件优先",
  },
  {
    key: "small_files",
    label: "海量小文件",
    desc: "Fast-List + 直接流式传输 + 提高并发",
  },
  {
    key: "cloud_drive",
    label: "国内网盘防限流",
    desc: "限制 API 频率、开启弱网重试与失败自动重试",
  },
  {
    key: "cold_archive",
    label: "冷备归档",
    desc: "MD5 校验 + 目标回收站 + 每周自动校验 + 漏跑补跑",
  },
];

export function applyTaskScenarioPreset(
  form: JobForm,
  preset: TaskScenarioPresetKey,
): JobForm {
  const baseFlags = { ...form.extraFlags };
  switch (preset) {
    case "media":
      return {
        ...form,
        multiThreadStreams: 8,
        extraFlags: {
          ...baseFlags,
          resumeEnable: true,
          resumeMinSizeMB: 32,
          orderBy: "size,descending",
          lowLevelRetries: 15,
        },
      };
    case "small_files":
      return {
        ...form,
        fastList: true,
        copyConcurrency: 16,
        scanConcurrency: 16,
        extraFlags: {
          ...baseFlags,
          directStream: true,
          orderBy: "size,ascending",
        },
      };
    case "cloud_drive":
      return {
        ...form,
        copyConcurrency: 3,
        tpsLimit: 4,
        tpsLimitBurst: 6,
        extraFlags: {
          ...baseFlags,
          resumeEnable: true,
          lowLevelRetries: 15,
          retriesSleep: "10s",
          autoRetryMax: 3,
          autoRetryDelayMin: 5,
        },
      };
    case "cold_archive":
      return {
        ...form,
        compareMode: 2,
        trashEnable: true,
        trashRetentionDays: 30,
        extraFlags: {
          ...baseFlags,
          resumeEnable: true,
          checkSchedule: "weekly",
        },
      };
  }
}

export function defaultJobForm(alistId?: number) {
  return {
    id: undefined as number | undefined,
    alistId,
    remark: "",
    srcPath: [] as string[],
    dstPath: [] as string[],
    method: 0,
    isCron: 3,
    interval: 1440,
    enable: true,
    useCacheS: false,
    useCacheT: false,
    scanIntervalS: 0,
    scanIntervalT: 0,
    minFileSize: 0 as number | string,
    minFileSizeUnit: "MB",
    maxFileSize: 0 as number | string,
    maxFileSizeUnit: "MB",
    exclude: defaultExclude,
    compareMode: 0,
    conflictPolicy: 0,
    trashEnable: false,
    trashDir: ".opensync-trash",
    trashRetentionDays: 30,
    deleteEmptySrcDirs: false,
    filterMode: 0,
    include: "",
    minFileAgeMinutes: 0,
    olderThanDays: 0,
    newerThanDays: 0,
    copyConcurrency: 0,
    scanConcurrency: 0,
    maxRetries: -1,
    windowEnable: false,
    windowStart: "00:00",
    windowEnd: "06:00",
    windowStopOutside: false,
    notifyMode: 0,
    notifyIds: [] as number[],
    bwLimit: "",
    tpsLimit: 0,
    tpsLimitBurst: 0,
    modifyWindow: "",
    fastList: false,
    multiThreadStreams: 0,
    createEmptySrcDirs: false,
    followSymlinks: false,
    extraFlags: emptyNativeExtraFlags(),
    ...defaultCronFields,
  };
}
export type JobForm = ReturnType<typeof defaultJobForm>;
// Continuous sync supports one-way (0/1) and bidirectional (3).
const supportedJobMethods = new Set([0, 1, 3]);

export function normalizeJobMethod(method: unknown) {
  const parsed = Number(method);
  return supportedJobMethods.has(parsed) && Number.isInteger(parsed) ? parsed : 0;
}

export function requiresFirstMirrorPreview(job: Pick<JobItem, "method" | "isCron" | "enable" | "syncStatus">) {
  return job.method === 1 && job.isCron === 3 && job.enable !== 1 && !job.syncStatus?.lastSuccess;
}

export function selectJob<T extends { id: number }>(
  jobs: T[],
  selectedId: number | null,
) {
  return selectedId === null
    ? jobs[0]
    : jobs.find((job) => job.id === selectedId);
}

/**
 * Resolves the task shown for the jobId in the URL.
 *
 * A well-formed id that is no longer in the list (deleted in another tab, an
 * old bookmark) falls back to the first task and reports `replaceWith`, so the
 * caller rewrites the URL (history replace) and the address bar, the sidebar
 * highlight and every action keep pointing at the task actually on screen.
 * A malformed id (the -1 sentinel) stays an explicit empty selection.
 */
export function resolveJobSelection<T extends { id: number }>(
  jobs: T[],
  selectedId: number | null,
  options?: { pendingId?: number | null; refreshing?: boolean },
): { job: T | undefined; replaceWith: number | null } {
  const job = selectJob(jobs, selectedId);
  if (job || selectedId === null || selectedId <= 0 || !jobs.length) {
    return { job, replaceWith: null };
  }
  if (
    options?.refreshing ||
    (options?.pendingId != null && options.pendingId === selectedId)
  ) {
    return { job: undefined, replaceWith: null };
  }
  return { job: jobs[0], replaceWith: jobs[0].id };
}

/**
 * Returns the move-mode (method === 2) jobs affected by a batch start action
 * so the UI can require confirmation before deleting source files.
 */
export function findBatchMoveJobs<
  T extends { id: number; method: number; enable?: number },
>(
  action: "run" | "stop" | "enable" | "disable" | "run_all" | "stop_all",
  jobs: readonly T[],
  ids?: readonly number[],
): T[] {
  if (action === "run_all") {
    return jobs.filter((job) => job.enable === 1 && job.method === 2);
  }
  if (action === "run") {
    const idSet = new Set(ids || []);
    return jobs.filter((job) => idSet.has(job.id) && job.method === 2);
  }
  return [];
}

export type TaskTab = "overview" | "records";

export function parseTaskTab(raw: string | null | undefined): TaskTab | null {
  if (raw === "overview" || raw === "records") {
    return raw;
  }
  return null;
}

/**
 * The tab actually rendered for a continuous sync task.
 *
 * - Default (no explicit tab) always lands on `overview`, which embeds the
 *   Cloud Sync status card and live transfer summary.
 * - Explicit `overview`, `history`, and `realtime` are always respected so
 *   short continuous sync batches never yank the user between tabs.
 */
export function resolveTaskTab(
  tab: string | null | undefined,
  _options?: {
    settled?: boolean;
    hasCurrentTask?: boolean;
    jobRunning?: boolean;
  },
): TaskTab {
  return parseTaskTab(tab) ?? "overview";
}

/**
 * Builds the query string for navigating to a task from the sidebar or mobile
 * job switcher. Keeps `history` when already browsing history; otherwise opens
 * the Cloud Sync overview directly without auto-jumping into realtime.
 */
export function buildTaskSearchParams(
  job: { id: number; running?: boolean },
  options?: {
    onTasksPage?: boolean;
    currentSearch?: string;
    activeJobId?: number | string | null;
  },
): URLSearchParams {
  const onTasksPage = Boolean(options?.onTasksPage);
  const current = new URLSearchParams(
    onTasksPage ? (options?.currentSearch ?? "") : "",
  );
  const sameJob =
    onTasksPage &&
    options?.activeJobId != null &&
    String(options.activeJobId) === String(job.id);

  if (sameJob) {
    current.set("jobId", String(job.id));
    return current;
  }

  const next = new URLSearchParams();
  next.set("jobId", String(job.id));
  if (onTasksPage && parseTaskTab(current.get("tab")) === "records") {
    next.set("tab", "records");
  }
  return next;
}
export function jobToForm(job: JobItem): JobForm {
  const min = splitBytesToFileSize(job.minFileSize),
    max = splitBytesToFileSize(job.maxFileSize);
  return {
    ...defaultJobForm(job.alistId),
    ...job,
    isCron: job.isCron,
    method: job.method === 2 && job.isCron !== 3 ? 2 : normalizeJobMethod(job.method),
    srcPath: parseJobPathList(job.srcPath),
    dstPath: parseJobPathList(job.dstPath),
    remark: job.remark || "",
    enable: job.enable === 1,
    useCacheS: Boolean(job.useCacheS),
    useCacheT: Boolean(job.useCacheT),
    scanIntervalS: job.scanIntervalS ?? 0,
    scanIntervalT: job.scanIntervalT ?? 0,
    minFileSize: min.value,
    minFileSizeUnit: min.unit,
    maxFileSize: max.value,
    maxFileSizeUnit: max.unit,
    // A stored job with no rules must stay without rules: falling back to the
    // default template silently added filters the user never configured (and
    // saved them back on the next edit).
    exclude: job.exclude ?? "",
    compareMode: Number(job.compareMode ?? 0),
    conflictPolicy: Number(job.conflictPolicy ?? 0),
    trashEnable: Boolean(job.trashEnable),
    trashDir: (job.trashDir || ".opensync-trash").trim() || ".opensync-trash",
    trashRetentionDays: Number(job.trashRetentionDays ?? 30),
    deleteEmptySrcDirs: Boolean(job.deleteEmptySrcDirs),
    filterMode: Number(job.filterMode ?? 0),
    include: job.include ?? "",
    minFileAgeMinutes: Number(job.minFileAgeMinutes ?? 0),
    olderThanDays: Number(job.olderThanDays ?? 0),
    newerThanDays: Number(job.newerThanDays ?? 0),
    copyConcurrency: Number(job.copyConcurrency ?? 0),
    scanConcurrency: Number(job.scanConcurrency ?? 0),
    maxRetries: job.maxRetries === undefined || job.maxRetries === null ? -1 : Number(job.maxRetries),
    windowEnable: Boolean(job.windowEnable),
    windowStart: (job.windowStart || "00:00").trim() || "00:00",
    windowEnd: (job.windowEnd || "06:00").trim() || "06:00",
    windowStopOutside: Boolean(job.windowStopOutside),
    notifyMode: Number(job.notifyMode ?? 0),
    notifyIds: parseNotifyIdList(job.notifyIds),
    bwLimit: (job.bwLimit || "").trim(),
    tpsLimit: Number(job.tpsLimit ?? 0),
    tpsLimitBurst: Number(job.tpsLimitBurst ?? 0),
    modifyWindow: (job.modifyWindow || "").trim(),
    fastList: Boolean(job.fastList),
    multiThreadStreams: Number(job.multiThreadStreams ?? 0),
    createEmptySrcDirs: Boolean(job.createEmptySrcDirs),
    followSymlinks: Boolean(job.followSymlinks),
    extraFlags: parseNativeExtraFlags(job.extraFlags),
    second: job.second || "0",
    minute: job.minute || "0",
    hour: job.hour || "2",
    day: job.day || "*",
    month: job.month || "*",
    day_of_week: job.day_of_week || "*",
  };
}

/** Creates a brand-new draft form cloned from an existing job (without id). */
export function cloneJobToForm(job: JobItem): JobForm {
  const form = jobToForm(job);
  const baseName = form.remark.trim() || `同步任务 #${job.id}`;
  return {
    ...form,
    id: undefined,
    remark: `${baseName} - 副本`,
  };
}

export function isLegacyOpenListJob(
  job?: { legacyOpenList?: boolean; alistId?: number } | null,
  engines?: readonly { id: number }[] | null,
): boolean {
  if (!job) return false;
  if (job.legacyOpenList) return true;
  if (engines && engines.length > 0 && Number(job.alistId || 0) > 0) {
    return !engines.some((e) => e.id === job.alistId);
  }
  return false;
}

export function buildJobPayload(form: JobForm) {
  const { minFileSizeUnit, maxFileSizeUnit, ...data } = form;
  // Strip any read-only runtime decoration fields if spread from a stored JobItem
  const runtimeStripped = { ...data } as typeof data & {
    legacyOpenList?: unknown;
    running?: unknown;
    paused?: unknown;
    queued?: unknown;
    watching?: unknown;
    checkRunning?: unknown;
    lastStatus?: unknown;
    nextRunTime?: unknown;
    syncStatus?: unknown;
  };
  delete runtimeStripped.legacyOpenList;
  delete runtimeStripped.running;
  delete runtimeStripped.paused;
  delete runtimeStripped.queued;
  delete runtimeStripped.watching;
  delete runtimeStripped.checkRunning;
  delete runtimeStripped.lastStatus;
  delete runtimeStripped.nextRunTime;
  delete runtimeStripped.syncStatus;
  const legacySchedule = form.id !== undefined && form.isCron !== 3;
  const method = legacySchedule && form.method === 2 ? 2 : normalizeJobMethod(form.method);
  const extraFlags: NativeExtraFlags = {
    ...form.extraFlags,
    watchEnable: legacySchedule && Boolean(form.extraFlags.watchEnable),
    missedCatchUp: legacySchedule && Boolean(form.extraFlags.missedCatchUp),
  };
  return {
    ...runtimeStripped,
    method,
    isCron: legacySchedule ? form.isCron : 3,
    remark: form.remark.trim() || null,
    exclude: form.exclude.trim() || null,
    include: form.include.trim() || null,
    enable: Number(Boolean(form.enable)),
    useCacheS: Number(form.useCacheS),
    useCacheT: Number(form.useCacheT),
    scanIntervalS: Math.max(0, Math.min(300, Math.trunc(Number(form.scanIntervalS) || 0))),
    scanIntervalT: Math.max(0, Math.min(300, Math.trunc(Number(form.scanIntervalT) || 0))),
    minFileSize: fileSizeToBytes(form.minFileSize, minFileSizeUnit),
    maxFileSize: fileSizeToBytes(form.maxFileSize, maxFileSizeUnit),
    compareMode: Number(form.compareMode) || 0,
    conflictPolicy: Number(form.conflictPolicy) || 0,
    trashEnable: Number(Boolean(form.trashEnable)),
    trashDir:
      !form.trashEnable && validateTrashDir(form.trashDir)
        ? ".opensync-trash"
        : form.trashDir.trim() || ".opensync-trash",
    trashRetentionDays: Math.max(0, Math.trunc(Number(form.trashRetentionDays) || 0)),
    deleteEmptySrcDirs: Number(legacySchedule && method === 2 && form.deleteEmptySrcDirs),
    filterMode: Number(form.filterMode) || 0,
    minFileAgeMinutes: Math.max(0, Math.trunc(Number(form.minFileAgeMinutes) || 0)),
    olderThanDays: Math.max(0, Math.trunc(Number(form.olderThanDays) || 0)),
    newerThanDays: Math.max(0, Math.trunc(Number(form.newerThanDays) || 0)),
    copyConcurrency: Math.max(0, Math.trunc(Number(form.copyConcurrency) || 0)),
    scanConcurrency: Math.max(0, Math.trunc(Number(form.scanConcurrency) || 0)),
    maxRetries: Number.isFinite(Number(form.maxRetries)) ? Math.trunc(Number(form.maxRetries)) : -1,
    windowEnable: Number(Boolean(form.windowEnable)),
    windowStart: form.windowStart.trim() || "00:00",
    windowEnd: form.windowEnd.trim() || "06:00",
    windowStopOutside: Number(Boolean(form.windowStopOutside)),
    notifyMode: Number(form.notifyMode) || 0,
    notifyIds: form.notifyMode === 1 ? form.notifyIds : [],
    bwLimit: form.bwLimit.trim(),
    tpsLimit: Math.max(0, Math.min(1000, Number(form.tpsLimit) || 0)),
    tpsLimitBurst: Math.max(0, Math.min(1000, Math.trunc(Number(form.tpsLimitBurst) || 0))),
    modifyWindow: form.modifyWindow.trim(),
    fastList: Number(Boolean(form.fastList)),
    multiThreadStreams: Math.max(0, Math.min(64, Math.trunc(Number(form.multiThreadStreams) || 0))),
    createEmptySrcDirs: Number(Boolean(form.createEmptySrcDirs)),
    followSymlinks: Number(Boolean(form.followSymlinks)),
    extraFlags: serializeNativeExtraFlags(extraFlags),
  };
}
export interface JobValidationOptions {
  storedDstMap?: string | null;
  existingJobs?: readonly ExistingJobPathSummary[];
  sizeEnabled?: { min?: boolean; max?: boolean };
  remotes?: readonly NativeRemoteItem[];
}

const containerRequiredBackends = new Set([
  "s3",
  "smb",
  "b2",
  "azureblob",
  "googlecloudstorage",
  "swift",
  "oracleobjectstorage",
  "qingstor",
  "storj",
]);

const wrapperRemoteTypes = new Set([
  "crypt",
  "chunker",
  "compress",
  "hasher",
  "alias",
  "archive",
]);

function splitCompositeUpstreams(raw: string): string[] {
  const tokens: string[] = [];
  const regex = /"([^"]*)"|(\S+)/g;
  let match: RegExpExecArray | null;
  while ((match = regex.exec(raw)) !== null) {
    const tok = (match[1] ?? match[2] ?? "").trim();
    if (tok) tokens.push(tok);
  }
  return tokens;
}

const reliableWebDAVVendors = new Set([
  "owncloud",
  "nextcloud",
  "rclone",
  "fastmail",
  "sharepoint",
]);

export type CompareModeSource = "default" | "auto" | "manual";

export interface ResolvedRemoteChain {
  found: boolean;
  canonicalName: string;
  leafBackend: string;
  leafVendor: string;
  hasCrypt: boolean;
  readOnly: boolean;
  accumulatedPrefix: string;
  isStandardWebDAV: boolean;
  hasQuark?: boolean;
}

export function resolveRemoteChain(
  remoteName: string,
  remotes: readonly NativeRemoteItem[],
  visited = new Set<string>(),
  subPath = "",
): ResolvedRemoteChain {
  const byLower = new Map<string, NativeRemoteItem>();
  for (const r of remotes) {
    byLower.set(r.name.toLowerCase(), r);
  }
  const start = byLower.get(remoteName.trim().toLowerCase());
  if (!start) {
    return {
      found: false,
      canonicalName: remoteName,
      leafBackend: "",
      leafVendor: "",
      hasCrypt: false,
      readOnly: false,
      accumulatedPrefix: "",
      isStandardWebDAV: false,
      hasQuark: false,
    };
  }

  let cur: NativeRemoteItem | undefined = start;
  let hasCrypt = false;
  let readOnly = false;
  let accumulatedPrefix = "";
  let restPath = subPath.replace(/^\/+|\/+$/g, "");
  let leafBackend = start.type;
  let leafVendor = "";
  let isStandardWebDAV = false;
  let hasQuark = false;

  while (cur) {
    const lower = cur.name.toLowerCase();
    if (visited.has(lower)) break;
    visited.add(lower);

    leafBackend = cur.type;
    if (cur.type === "crypt") hasCrypt = true;
    if (cur.type === "quark") hasQuark = true;
    if (cur.type === "archive" || cur.type === "http") readOnly = true;

    if (wrapperRemoteTypes.has(cur.type)) {
      const target = (cur.options?.remote || "").trim();
      const colonIdx = target.indexOf(":");
      if (colonIdx <= 0) break;
      const nextRef = target.slice(0, colonIdx).trim();
      const prefix = target.slice(colonIdx + 1).replace(/^\/+|\/+$/g, "");
      if (prefix) {
        accumulatedPrefix = accumulatedPrefix
          ? `${prefix}/${accumulatedPrefix}`
          : prefix;
        restPath = restPath ? `${prefix}/${restPath}` : prefix;
      }
      if (nextRef === "本地") {
        leafBackend = "local";
        break;
      }
      cur = byLower.get(nextRef.toLowerCase());
      continue;
    }

    if (cur.type === "webdav") {
      leafVendor = (cur.options?.vendor || "").trim().toLowerCase();
      isStandardWebDAV = !reliableWebDAVVendors.has(leafVendor);
      break;
    }

    if (cur.type === "union" || cur.type === "combine") {
      const tokens = splitCompositeUpstreams(cur.options?.upstreams || "");
      let allReadOnly = tokens.length > 0;
      const restSegs = restPath ? restPath.split("/").filter(Boolean) : [];
      const mountName = cur.type === "combine" ? (restSegs[0] ?? "") : "";
      const mountSub = cur.type === "combine" ? restSegs.slice(1).join("/") : restPath;
      for (const tok of tokens) {
        let target = tok;
        const upstreamReadOnly = cur.type === "union" && target.endsWith(":ro");
        if (cur.type === "combine") {
          const eqIdx = target.indexOf("=");
          if (eqIdx <= 0) continue;
          const dirName = target.slice(0, eqIdx).trim();
          if (mountName && dirName !== mountName) continue;
          target = target.slice(eqIdx + 1).trim();
        } else {
          target = target.replace(/:(ro|nc)$/, "");
        }
        const colonIdx = target.indexOf(":");
        if (colonIdx <= 0) continue;
        const ref = target.slice(0, colonIdx).trim();
        const upPrefix = target.slice(colonIdx + 1).replace(/^\/+|\/+$/g, "");
        if (!ref || ref === "本地") {
          if (!ref || !upstreamReadOnly) allReadOnly = false;
          continue;
        }
        const childSub = [upPrefix, mountSub].filter(Boolean).join("/");
        const child = resolveRemoteChain(ref, remotes, new Set(visited), childSub);
        if (child.hasCrypt) hasCrypt = true;
        if (child.isStandardWebDAV) isStandardWebDAV = true;
        if (child.hasQuark) hasQuark = true;
        if (cur.type === "combine" && child.readOnly) readOnly = true;
        if (!child.found || (!child.readOnly && !upstreamReadOnly)) allReadOnly = false;
      }
      if (cur.type === "union" && allReadOnly) readOnly = true;
      break;
    }
    break;
  }

  return {
    found: true,
    canonicalName: start.name,
    leafBackend,
    leafVendor,
    hasCrypt,
    readOnly,
    accumulatedPrefix,
    isStandardWebDAV,
    hasQuark,
  };
}

export function findQuarkRemotesForPaths(
  paths: readonly string[],
  remotes?: readonly NativeRemoteItem[],
): string[] {
  if (!remotes?.length) return [];
  const hits = new Set<string>();
  for (const raw of paths) {
    const segs = raw.replace(/^\/+/, "").split("/").filter(Boolean);
    const seg = segs[0]?.trim();
    if (!seg || seg === "本地") continue;
    const subPath = segs.slice(1).join("/");
    const info = resolveRemoteChain(seg, remotes, new Set(), subPath);
    if (info.found && info.hasQuark) {
      hits.add(info.canonicalName);
    }
  }
  return Array.from(hits);
}

export function findStandardWebDAVRemotesForPaths(
  paths: readonly string[],
  remotes?: readonly NativeRemoteItem[],
): string[] {
  if (!remotes?.length) return [];
  const hits = new Set<string>();
  for (const raw of paths) {
    const segs = raw.replace(/^\/+/, "").split("/").filter(Boolean);
    const seg = segs[0]?.trim();
    if (!seg || seg === "本地") continue;
    const subPath = segs.slice(1).join("/");
    const info = resolveRemoteChain(seg, remotes, new Set(), subPath);
    if (info.found && info.isStandardWebDAV) {
      hits.add(info.canonicalName);
    }
  }
  return Array.from(hits);
}

/**
 * Resolves the automatic comparison mode when destination paths change.
 * When any destination resolves to standard WebDAV (such as OpenList / AList),
 * the default comparison mode switches to size-only (1) to avoid downloading
 * large amounts of remote data locally for comparison. Manual choices are always
 * respected, and auto-applied size-only reverts to 0 if the destination is
 * switched back to a non-WebDAV storage.
 */
export function resolveAutoCompareModeForDst(params: {
  dstPaths: readonly string[];
  remotes?: readonly NativeRemoteItem[];
  currentCompareMode: number;
  source: CompareModeSource;
}): {
  compareMode: number;
  source: CompareModeSource;
  matchedDstRemotes: string[];
} {
  const matchedDstRemotes = findStandardWebDAVRemotesForPaths(
    params.dstPaths,
    params.remotes,
  );
  if (params.source === "manual") {
    return {
      compareMode: params.currentCompareMode,
      source: "manual",
      matchedDstRemotes,
    };
  }
  if (matchedDstRemotes.length > 0) {
    return {
      compareMode: 1,
      source: "auto",
      matchedDstRemotes,
    };
  }
  if (params.source === "auto") {
    return {
      compareMode: 0,
      source: "default",
      matchedDstRemotes,
    };
  }
  return {
    compareMode: params.currentCompareMode,
    source: params.source,
    matchedDstRemotes,
  };
}

export function isPathDisabledInPicker(
  rawPath: string,
  remotes?: readonly NativeRemoteItem[],
): boolean {
  const clean = rawPath.trim().replace(/\/+$/, "");
  if (!clean || clean === "/" || clean === "/本地") {
    return true;
  }
  const segs = clean.replace(/^\/+/, "").split("/").filter(Boolean);
  if (segs.length === 1 && remotes?.length) {
    const info = resolveRemoteChain(segs[0], remotes);
    if (
      info.found &&
      containerRequiredBackends.has(info.leafBackend) &&
      !info.accumulatedPrefix
    ) {
      return true;
    }
  }
  return false;
}

export function validateManualSyncPath(
  rawPath: string,
  remotes?: readonly NativeRemoteItem[],
  options?: { role?: "src" | "dst"; method?: number },
): { error: string; normalized: string } {
  const trimmed = rawPath.trim();
  if (!trimmed || !trimmed.startsWith("/")) {
    return { error: "路径必须以 / 开头，例如 /本地/目录 或 /存储名称/目录", normalized: "" };
  }
  if (trimmed.includes("\\") || /[\u0000-\u001f\u007f]/.test(trimmed)) {
    return { error: "路径不能包含反斜杠或控制字符", normalized: "" };
  }
  const rawSegs = trimmed.split("/").filter((s) => s !== "");
  if (rawSegs.length === 0) {
    return { error: "不能直接选择根目录 /，请选择具体授权目录或远端路径", normalized: "" };
  }
  if (rawSegs.some((s) => s === "." || s === "..")) {
    return { error: "路径不能包含 . 或 .. 目录段", normalized: "" };
  }

  let canonicalFirst = rawSegs[0];
  if (canonicalFirst === "本地") {
    if (rawSegs.length < 2) {
      return { error: "请选择「/本地」下的具体飞牛授权目录", normalized: "" };
    }
  } else if (remotes !== undefined) {
    const info = resolveRemoteChain(canonicalFirst, remotes, new Set(), rawSegs.slice(1).join("/"));
    if (!info.found) {
      return {
        error: `存储「${canonicalFirst}」不存在；本地目录请使用 /本地/...，远端目录请先在存储管理中添加`,
        normalized: "",
      };
    }
    canonicalFirst = info.canonicalName;
    if (
      containerRequiredBackends.has(info.leafBackend) &&
      !info.accumulatedPrefix &&
      rawSegs.length < 2
    ) {
      return {
        error: `存储「${info.canonicalName}」需指定存储桶、容器或共享目录（如 /${info.canonicalName}/目录名）`,
        normalized: "",
      };
    }
    if (options?.role === "dst" && info.readOnly) {
      return {
        error: `只读存储「${info.canonicalName}」不能作为同步目标`,
        normalized: "",
      };
    }
    if (
      options?.role === "src" &&
      info.readOnly &&
      (options.method === 2 || options.method === 3)
    ) {
      return {
        error: `只读存储「${info.canonicalName}」不支持移动或双向同步`,
        normalized: "",
      };
    }
  }

  const normalized = "/" + [canonicalFirst, ...rawSegs.slice(1)].join("/");
  return { error: "", normalized };
}

/**
 * 判断在目录选择器中是否允许在指定节点下直接新建子文件夹。
 * - `/`、`/本地` 本身不能新建目录（本地根须由飞牛系统授权）；
 * - 需指定存储桶/共享目录的后端（S3/SMB 等）以及 combine 根不支持在存储根下直接建目录；
 * - 只读存储（HTTP、archive、全只读 union 等）禁止创建目录。
 */
export function canCreateFolderUnderPath(
  rawPath: string,
  remotes?: readonly NativeRemoteItem[],
): boolean {
  const trimmed = rawPath.trim();
  if (!trimmed || !trimmed.startsWith("/")) return false;
  if (trimmed.includes("\\") || /[\u0000-\u001f\u007f]/.test(trimmed)) {
    return false;
  }
  const segs = trimmed.replace(/^\/+|\/+$/g, "").split("/").filter(Boolean);
  if (segs.length === 0 || segs.some((s) => s === "." || s === "..")) {
    return false;
  }
  const [first, ...rest] = segs;
  if (first === "本地") {
    return rest.length >= 1;
  }
  if (!remotes?.length) {
    return true;
  }
  const info = resolveRemoteChain(first, remotes, new Set(), rest.join("/"));
  if (!info.found || info.readOnly) {
    return false;
  }
  if (
    rest.length === 0 &&
    !info.accumulatedPrefix &&
    (containerRequiredBackends.has(info.leafBackend) ||
      info.leafBackend === "combine")
  ) {
    return false;
  }
  return true;
}

/**
 * 校验目录选择器里新建文件夹的输入（支持单级名称如 `Backup` 或多级相对路径如 `2026/Photos`）。
 */
export function validateNewFolderName(rawName: string): {
  error: string;
  segments: string[];
  clean: string;
} {
  const trimmed = rawName.trim();
  if (!trimmed) {
    return { error: "请输入文件夹名称", segments: [], clean: "" };
  }
  if (trimmed.includes("\\") || /[\u0000-\u001f\u007f]/.test(trimmed)) {
    return {
      error: "文件夹名称不能包含反斜杠或控制字符",
      segments: [],
      clean: "",
    };
  }
  const stripped = trimmed.replace(/^\/+|\/+$/g, "");
  if (!stripped) {
    return { error: "请输入文件夹名称", segments: [], clean: "" };
  }
  if (stripped.includes("//")) {
    return { error: "文件夹名称不能包含连续斜杠", segments: [], clean: "" };
  }
  const segments = stripped.split("/");
  if (segments.some((s) => !s || s === "." || s === "..")) {
    return {
      error: "文件夹名称不能包含 . 或 ..",
      segments: [],
      clean: "",
    };
  }
  return { error: "", segments, clean: segments.join("/") };
}

/**
 * 计算新建文件夹后的目标路径、需要逐级重新加载的父目录列表以及需要展开的祖先节点列表。
 */
export function buildCreatedFolderPlan(
  parentPath: string,
  folderInput: string,
): {
  error: string;
  clean: string;
  targetPath: string;
  loadPaths: string[];
  expandPaths: string[];
} {
  const checked = validateNewFolderName(folderInput);
  if (checked.error) {
    return {
      error: checked.error,
      clean: "",
      targetPath: "",
      loadPaths: [],
      expandPaths: [],
    };
  }
  const parentSegs = parentPath
    .trim()
    .replace(/^\/+|\/+$/g, "")
    .split("/")
    .filter(Boolean);
  if (parentSegs.length === 0) {
    return {
      error: "请选择有效的上级目录",
      clean: "",
      targetPath: "",
      loadPaths: [],
      expandPaths: [],
    };
  }
  const cleanParent = "/" + parentSegs.join("/");
  const targetPath = `${cleanParent}/${checked.clean}`;

  const loadPaths: string[] = [cleanParent];
  let cur = cleanParent;
  for (let i = 0; i < checked.segments.length - 1; i += 1) {
    cur = `${cur}/${checked.segments[i]}`;
    loadPaths.push(cur);
  }

  const expandPaths: string[] = ["/"];
  const allSegs = [...parentSegs, ...checked.segments.slice(0, -1)];
  let acc = "";
  for (const seg of allSegs) {
    acc = `${acc}/${seg}`;
    expandPaths.push(acc);
  }

  return {
    error: "",
    clean: checked.clean,
    targetPath,
    loadPaths,
    expandPaths,
  };
}

/**
 * 将刚创建的文件夹合并进目录选择器的已选值；多选时移除与其父子重叠的旧祖先项，避免产生嵌套路径冲突。
 */
export function mergeCreatedFolderSelection(
  currentValue: readonly string[],
  createdPath: string,
  multiple: boolean,
): string[] {
  if (!multiple) {
    return [createdPath];
  }
  const next = currentValue.filter(
    (item) => item !== createdPath && !syncPathContains(item, createdPath),
  );
  next.push(createdPath);
  return next;
}

export function validateTrashDir(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  if (
    trimmed.startsWith("/") ||
    trimmed.includes("//") ||
    /[\\*?[\]{}!]/.test(trimmed) ||
    /[\u0000-\u001f\u007f]/.test(trimmed)
  ) {
    return "回收站目录名须为相对路径，不能以 / 开头或包含 \\ * ? [ ] { } ! 等特殊字符";
  }
  const clean = trimmed.replace(/\/+$/, "");
  if (!clean) {
    return "回收站目录名无效";
  }
  for (const seg of clean.split("/")) {
    if (!seg || seg === "." || seg === "..") {
      return "回收站目录名不能包含 . 或 .. 路径段";
    }
  }
  return "";
}

export type JobFieldErrorKey =
  | "remark"
  | "alistId"
  | "srcPath"
  | "dstPath"
  | "trashDir"
  | "interval"
  | "window"
  | "notifyIds"
  | "bwLimit"
  | CronFieldKey
  | "minFileSize"
  | "maxFileSize"
  | "fileSizeRange"
  | "include";

function resolveValidationOptions(
  storedDstMapOrOptions?: string | null | JobValidationOptions,
): JobValidationOptions {
  if (
    storedDstMapOrOptions &&
    typeof storedDstMapOrOptions === "object"
  ) {
    return storedDstMapOrOptions;
  }
  return { storedDstMap: storedDstMapOrOptions };
}

// Mirrors validateJobInput in backend/internal/service/job_service.go: same or
// nested source/destination roots (including derived or preserved destination
// subdirectories) would make two scan works mutate one tree, and in mirror mode
// delete each other's files. Catching it here keeps the error on the field
// instead of surfacing it only after the final save.
function validateJobPaths(
  form: JobForm,
  options?: JobValidationOptions,
): string {
  const effectiveDstMap =
    options?.storedDstMap !== undefined
      ? options.storedDstMap
      : (form as JobForm & { dstMap?: string | null }).dstMap;
  return validateSyncPathSelection({
    srcPaths: form.srcPath,
    dstPaths: form.dstPath,
    storedDstMap: effectiveDstMap,
    method: form.method,
    existingJobs: options?.existingJobs,
    currentJobId: form.id,
    engineId: form.alistId,
  });
}

export function validateJobFormFields(
  form: JobForm,
  storedDstMapOrOptions?: string | null | JobValidationOptions,
): {
  fields: Partial<Record<JobFieldErrorKey, string>>;
  stepErrors: [string, string, string];
} {
  const options = resolveValidationOptions(storedDstMapOrOptions);
  const fields: Partial<Record<JobFieldErrorKey, string>> = {};

  // Step 0: 基础与路径（任务名称、存储引擎、同步方式、源路径、目标路径与跨任务冲突）
  const step0Errors: string[] = [];
  if (!form.remark.trim()) {
    fields.remark = "请输入任务名称";
    step0Errors.push(fields.remark);
  }
  if (!form.alistId) {
    fields.alistId = "内置存储引擎尚未就绪，请稍后重试";
    step0Errors.push(fields.alistId);
  }
  if (!form.srcPath.length) {
    fields.srcPath = "请选择源目录";
    step0Errors.push(fields.srcPath);
  }
  if (!form.dstPath.length) {
    fields.dstPath = "请选择目标目录";
    step0Errors.push(fields.dstPath);
  }
  if (form.method === 2 && form.dstPath.length > 1) {
    fields.dstPath = "移动模式只允许一个目标目录";
    step0Errors.push(fields.dstPath);
  }
  if (form.method === 3 && form.srcPath.length > 1) {
    fields.srcPath = "双向同步模式只允许一个源目录";
    step0Errors.push(fields.srcPath);
  }
  if (form.method === 3 && form.dstPath.length > 1) {
    fields.dstPath = "双向同步模式只允许一个目标目录";
    step0Errors.push(fields.dstPath);
  }
  for (const p of form.srcPath) {
    const check = validateManualSyncPath(p, options.remotes, {
      role: "src",
      method: form.method,
    });
    if (check.error) {
      fields.srcPath = fields.srcPath || check.error;
      step0Errors.push(check.error);
      break;
    }
  }
  for (const p of form.dstPath) {
    const check = validateManualSyncPath(p, options.remotes, {
      role: "dst",
      method: form.method,
    });
    if (check.error) {
      fields.dstPath = fields.dstPath || check.error;
      step0Errors.push(check.error);
      break;
    }
  }
  if (form.srcPath.length > 0 && form.dstPath.length > 0) {
    const pathError = validateJobPaths(form, options);
    if (pathError) {
      if (
        pathError.includes("多个源目录") ||
        pathError.startsWith("双向同步源目录") ||
        pathError.startsWith("移动模式源目录")
      ) {
        fields.srcPath = fields.srcPath || pathError;
      } else {
        fields.dstPath = fields.dstPath || pathError;
      }
      step0Errors.push(pathError);
    }
  }
  if (form.trashEnable && form.method !== 2) {
    const trashErr = validateTrashDir(form.trashDir);
    if (trashErr) {
      fields.trashDir = trashErr;
      step0Errors.push(fields.trashDir);
    }
  }

  // Step 1: 同步设置（持续同步轮询 / 完整核对 / 变动静默 / 时间窗口 / 通知绑定）
  const step1Errors: string[] = [];
  if (form.method === 2 && form.isCron === 3) {
    fields.interval = "当前仅支持持续同步模式，请使用单向同步或双向同步";
    step1Errors.push(fields.interval);
  }
  const poll = Number(form.extraFlags?.syncPollIntervalSec ?? 0);
  const audit = Number(form.extraFlags?.syncAuditIntervalMin ?? 0);
  const debounce = Number(form.extraFlags?.watchDebounceSec ?? 0);
  if (
    (poll !== 0 && (!Number.isInteger(poll) || poll < 10 || poll > 86400)) ||
    (audit !== 0 && (!Number.isInteger(audit) || audit < 1 || audit > 10080))
  ) {
    fields.interval = "远端检测间隔需为 10–86400 秒，完整核对间隔需为 1–10080 分钟";
    step1Errors.push(fields.interval);
  } else if (
    debounce !== 0 &&
    (!Number.isInteger(debounce) || debounce < 2 || debounce > 3600)
  ) {
    fields.interval = "变动合并等待需为 2–3600 秒的整数";
    step1Errors.push(fields.interval);
  }
  if (form.windowEnable) {
    const hhmm = /^([01]\d|2[0-3]):([0-5]\d)$/;
    const s = form.windowStart.trim();
    const e = form.windowEnd.trim();
    if (!hhmm.test(s) || !hhmm.test(e)) {
      fields.window = "时间窗口格式必须为 HH:MM（如 23:00）";
      step1Errors.push(fields.window);
    } else if (s === e) {
      fields.window = "时间窗口的开始时间与结束时间不能相同";
      step1Errors.push(fields.window);
    }
  }
  if (form.notifyMode === 1 && form.notifyIds.length === 0) {
    fields.notifyIds = "已选择指定通知渠道，请至少选择一个渠道";
    step1Errors.push(fields.notifyIds);
  }
  const bwLimitError = validateBwLimitExpression(form.bwLimit);
  if (bwLimitError) {
    fields.bwLimit = bwLimitError;
    step1Errors.push(bwLimitError);
  }

  // Step 2: 过滤规则（文件大小上下限）
  const step2Errors: string[] = [];
  const minValue = parseFileSizeInput(form.minFileSize);
  const maxValue = parseFileSizeInput(form.maxFileSize);
  const minRawEmpty =
    typeof form.minFileSize === "string" && form.minFileSize.trim() === "";
  const maxRawEmpty =
    typeof form.maxFileSize === "string" && form.maxFileSize.trim() === "";

  if (minValue === null) {
    fields.minFileSize = "文件大小必须是有效的非负数";
    step2Errors.push(fields.minFileSize);
  } else if (options.sizeEnabled?.min && (minRawEmpty || minValue <= 0)) {
    fields.minFileSize = "请输入大于 0 的文件大小，或取消勾选以关闭此限制";
    step2Errors.push(fields.minFileSize);
  }

  if (maxValue === null) {
    fields.maxFileSize = "文件大小必须是有效的非负数";
    step2Errors.push(fields.maxFileSize);
  } else if (options.sizeEnabled?.max && (maxRawEmpty || maxValue <= 0)) {
    fields.maxFileSize = "请输入大于 0 的文件大小，或取消勾选以关闭此限制";
    step2Errors.push(fields.maxFileSize);
  }

  if (minValue !== null && maxValue !== null) {
    const minBytes = fileSizeInputToBytes(
      form.minFileSize,
      form.minFileSizeUnit,
    );
    const maxBytes = fileSizeInputToBytes(
      form.maxFileSize,
      form.maxFileSizeUnit,
    );
    if (minBytes === null) {
      fields.minFileSize =
        fields.minFileSize || "文件大小换算后必须是 1 字节到安全整数范围内的值";
      step2Errors.push(fields.minFileSize);
    }
    if (maxBytes === null) {
      fields.maxFileSize =
        fields.maxFileSize || "文件大小换算后必须是 1 字节到安全整数范围内的值";
      step2Errors.push(fields.maxFileSize);
    }
    if (
      minBytes !== null &&
      maxBytes !== null &&
      maxBytes > 0 &&
      minBytes > maxBytes
    ) {
      fields.fileSizeRange = "最大文件大小必须大于等于最小文件大小";
      step2Errors.push(fields.fileSizeRange);
    }
  }

  if (
    form.olderThanDays > 0 &&
    form.newerThanDays > 0 &&
    form.olderThanDays >= form.newerThanDays
  ) {
    fields.fileSizeRange =
      fields.fileSizeRange ||
      "「仅同步早于 N 天」必须小于「仅同步最近 N 天」";
    step2Errors.push(fields.fileSizeRange);
  }
  if (form.filterMode === 1 && !form.include.trim()) {
    fields.include = "白名单模式下请至少填写一条包含规则（如 *.jpg）";
    step2Errors.push(fields.include);
  }

  return {
    fields,
    stepErrors: [
      step0Errors[0] ?? "",
      step1Errors[0] ?? "",
      step2Errors[0] ?? "",
    ],
  };
}

export function validateJobFormStep(
  form: JobForm,
  step: number,
  storedDstMapOrOptions?: string | null | JobValidationOptions,
) {
  const { stepErrors } = validateJobFormFields(form, storedDstMapOrOptions);
  return stepErrors[step] ?? "";
}

export function validateJobForm(
  form: JobForm,
  storedDstMapOrOptions?: string | null | JobValidationOptions,
) {
  const { stepErrors } = validateJobFormFields(form, storedDstMapOrOptions);
  return stepErrors.find(Boolean) ?? "";
}

// ---- 文件过滤 ----

export const fileTypeFilterGroups = [
  {
    key: "system",
    label: "系统文件",
    patterns: [
      ".DS_Store", "._*", "Thumbs.db", "Desktop.ini",
    ],
  },
  {
    key: "audio",
    label: "音乐",
    patterns: [
      "*.mp3", "*.flac", "*.wav", "*.aac", "*.m4a",
      "*.ogg", "*.wma", "*.ape", "*.alac", "*.aiff",
      "*.opus", "*.mid", "*.midi",
    ],
  },
  {
    key: "video",
    label: "视频",
    patterns: [
      "*.mp4", "*.mkv", "*.avi", "*.mov", "*.wmv",
      "*.flv", "*.webm", "*.m4v", "*.rmvb", "*.rm",
      "*.ts", "*.m2ts", "*.vob", "*.3gp", "*.mpg",
      "*.mpeg",
    ],
  },
  {
    key: "image",
    label: "图像",
    patterns: [
      "*.jpg", "*.jpeg", "*.png", "*.gif", "*.bmp",
      "*.webp", "*.heic", "*.heif", "*.tif", "*.tiff",
      "*.raw", "*.svg", "*.ico", "*.psd", "*.cr2",
      "*.nef", "*.arw", "*.dng",
    ],
  },
  {
    key: "document",
    label: "文档",
    patterns: [
      "*.doc", "*.docx", "*.xls", "*.xlsx", "*.ppt",
      "*.pptx", "*.pdf", "*.txt", "*.rtf", "*.csv",
      "*.md", "*.odt", "*.ods", "*.odp", "*.epub",
      "*.pages", "*.numbers", "*.keynote", "*.json",
      "*.xml", "*.yaml", "*.yml",
    ],
  },
  {
    key: "archive",
    label: "压缩文件",
    patterns: [
      "*.zip", "*.rar", "*.7z", "*.tar", "*.gz",
      "*.bz2", "*.xz", "*.zst", "*.iso", "*.dmg",
      "*.img", "*.cab", "*.lz", "*.lzma",
    ],
  },
  {
    key: "executable",
    label: "可执行文件",
    patterns: [
      "*.exe", "*.msi", "*.dll", "*.so", "*.dylib",
      "*.app", "*.deb", "*.rpm", "*.apk", "*.ipa",
      "*.bat", "*.cmd", "*.sh", "*.bin",
    ],
  },
  {
    key: "temporary",
    label: "临时文件",
    patterns: [
      "*.tmp", "*.temp", "*.part", "*.crdownload",
      "*.download", "*.partial",
      "*.swp", "*.swo", "*.swn",
    ],
  },
  {
    key: "lock",
    label: "锁文件",
    patterns: ["~$*", ".~lock.*#", ".#*"],
  },
] as const;

export const systemDirFilterGroups = [
  {
    key: "macos",
    label: "macOS",
    patterns: [
      ".Spotlight-V100/", ".Trashes/", ".fseventsd/",
      ".DocumentRevisions-V100/", ".TemporaryItems/",
    ],
  },
  {
    key: "windows",
    label: "Windows",
    patterns: ["$RECYCLE.BIN/", "System Volume Information/"],
  },
  {
    key: "nas",
    label: "NAS 回收站",
    patterns: [
      "#recycle/", "@Recycle/", ".Recycle/",
      ".recycle/", ".Trash/", ".Trash-1000/",
    ],
  },
  {
    key: "other-dirs",
    label: "其他系统目录",
    patterns: ["lost+found/", "@eaDir/"],
  },
] as const;

const CUSTOM_FILE_TYPE_START = "# --- 自定义文件过滤 ---";
const CUSTOM_FILE_TYPE_END = "# --- 自定义文件过滤结束 ---";
const LEGACY_CUSTOM_FILE_TYPE_START = "# --- 自定义文件类型过滤 ---";
const LEGACY_CUSTOM_FILE_TYPE_END = "# --- 自定义文件类型过滤结束 ---";

function fileTypePresetPatterns() {
  return new Set<string>(
    fileTypeFilterGroups.flatMap((group) =>
      [...group.patterns].map((pattern) => filterRuleKey(pattern)),
    ),
  );
}

export function isExcludePatternEnabled(exclude: string, pattern: string) {
  const target = filterRuleKey(pattern);
  return exclude.split("\n").some((line) => {
    const value = line.trim();
    return !value.startsWith("# ") && filterRuleKey(value) === target;
  });
}

/**
 * Adds or removes one or more exclude rules.
 *
 * There is no persisted "disabled" state: the backend strips every "# " comment
 * when it saves (normalizeExclude in backend/internal/service/job_exclude.go),
 * so a rule kept as a comment would silently disappear on the next load.
 * Unchecking a rule therefore removes it from the exclude text; the UI treats
 * absence as disabled. Comment lines in the text are left untouched.
 */
export function updateExcludePatterns(
  exclude: string,
  patterns: readonly string[],
  enabled: boolean,
) {
  let lines = exclude.split("\n");
  for (const pattern of patterns) {
    const target = filterRuleKey(pattern);
    let found = false;
    const nextLines: string[] = [];
    for (const line of lines) {
      const value = line.trim();
      if (!value || value.startsWith("# ")) {
        nextLines.push(line);
        continue;
      }
      if (filterRuleKey(value) !== target) {
        nextLines.push(line);
        continue;
      }
      if (found) continue;
      found = true;
      if (!enabled) continue;
      nextLines.push(value);
    }
    if (found && !enabled) {
      // Removing the last rule must not leave a blank trailing line that turns
      // re-adding it into "\n#recycle/".
      while (nextLines.length && nextLines[nextLines.length - 1].trim() === "")
        nextLines.pop();
    }
    if (!found && enabled) {
      if (nextLines.length === 1 && nextLines[0] === "") nextLines.length = 0;
      nextLines.push(pattern);
    }
    lines = nextLines;
  }
  return lines.join("\n");
}

export function validateCustomFilePattern(input: string): {
  pattern: string;
  error: string;
} {
  const pattern = input.trim();
  if (!pattern) {
    return { pattern: "", error: "请输入文件名或文件后缀" };
  }
  if (pattern.startsWith("#")) {
    return { pattern: "", error: "规则不能以 # 开头" };
  }
  if (pattern.includes("!")) {
    return { pattern: "", error: "暂不支持取反规则（!）" };
  }
  if (pattern.includes("/") || pattern.includes("\\")) {
    return {
      pattern: "",
      error: "请勿包含路径分隔符，目录请在下方「文件夹过滤」中配置",
    };
  }
  if (pattern === "*" || pattern.includes("**")) {
    return { pattern: "", error: "暂不支持单独的 * 或 ** 通配符" };
  }
  if (/[\u0000-\u001f\u007f-\u009f]/.test(pattern)) {
    return { pattern: "", error: "包含无效控制字符" };
  }
  if (/[?[\]{}]/.test(pattern)) {
    return { pattern: "", error: "仅支持 * 通配符，暂不支持 ?、[]、{}" };
  }
  if (pattern.startsWith("*.")) {
    return isValidExtensionRule(pattern)
      ? { pattern, error: "" }
      : { pattern: "", error: "文件后缀格式无效，例如 *.log、*.mp4" };
  }
  if (pattern === "." || pattern === "..") {
    return { pattern: "", error: "请输入有效的文件名或文件后缀" };
  }
  return { pattern, error: "" };
}

export function normalizeCustomFileTypePattern(input: string) {
  return validateCustomFilePattern(input).pattern;
}

function isValidExtensionRule(pattern: string): boolean {
  if (!pattern.startsWith("*.")) return false;
  const ext = pattern.slice(2);
  return (
    ext !== "" &&
    !ext.startsWith(".") &&
    !ext.endsWith(".") &&
    !/[/\\*?\[\]{}!\u0000-\u001f\u007f-\u009f]/.test(ext)
  );
}

function normalizeCustomFilePattern(input: string) {
  return validateCustomFilePattern(input).pattern;
}

function filterRuleKey(pattern: string) {
  const trimmed = pattern.trim();
  const value =
    trimmed.startsWith("\\#") && trimmed.endsWith("/")
      ? trimmed.slice(1)
      : trimmed;
  return isValidExtensionRule(value) ? value.toLowerCase() : value;
}

function findCustomFileFilterBlock(lines: string[]) {
  for (const [startMarker, endMarker] of [
    [CUSTOM_FILE_TYPE_START, CUSTOM_FILE_TYPE_END],
    [LEGACY_CUSTOM_FILE_TYPE_START, LEGACY_CUSTOM_FILE_TYPE_END],
  ]) {
    const start = lines.findIndex((line) => line.trim() === startMarker);
    const end = lines.findIndex(
      (line, index) => index > start && line.trim() === endMarker,
    );
    if (start !== -1 && end !== -1) return { start, end };
  }
  return null;
}

export function parseCustomFileTypeFilters(exclude: string) {
  const lines = exclude.split("\n");
  const block = findCustomFileFilterBlock(lines);
  if (!block) return [];
  return lines.slice(block.start + 1, block.end).flatMap((line) => {
    const value = line.trim();
    if (!value) return [];
    const enabled = !value.startsWith("# ");
    const rawPattern = enabled ? value : value.slice(2).trim();
    const pattern = normalizeCustomFilePattern(rawPattern);
    return pattern ? [{ pattern: filterRuleKey(pattern), enabled }] : [];
  });
}

function parseFileTypePatternLine(line: string) {
  const value = line.trim();
  if (!value) return null;
  const enabled = !value.startsWith("# ");
  const rawPattern = enabled ? value : value.slice(2).trim();
  if (!enabled && !isValidExtensionRule(rawPattern)) {
    return null;
  }
  const normalized = normalizeCustomFilePattern(rawPattern);
  return normalized
    ? { pattern: filterRuleKey(normalized), enabled }
    : null;
}

export function parseOtherFileTypeFilters(exclude: string) {
  const presetPatterns = fileTypePresetPatterns();
  const lines = exclude.split("\n");
  const block = findCustomFileFilterBlock(lines);
  const filters = new Map<string, { pattern: string; enabled: boolean }>();
  const add = (item: { pattern: string; enabled: boolean } | null) => {
    if (!item || presetPatterns.has(filterRuleKey(item.pattern))) return;
    const pattern = filterRuleKey(item.pattern);
    const current = filters.get(pattern);
    if (current) {
      // A legacy "# " comment line can name the same rule as a live one; the
      // live rule is what the job actually applies.
      if (!current.enabled && item.enabled) filters.set(pattern, { ...item, pattern });
      return;
    }
    filters.set(pattern, { ...item, pattern });
  };

  parseCustomFileTypeFilters(exclude).forEach(add);
  lines.forEach((line, index) => {
    if (block && index >= block.start && index <= block.end) return;
    add(parseFileTypePatternLine(line));
  });
  return [...filters.values()];
}

export function addCustomFileTypeFilter(exclude: string, input: string) {
  const pattern = normalizeCustomFilePattern(input);
  if (!pattern) return exclude;

  const presetPatterns = fileTypePresetPatterns();
  if (presetPatterns.has(filterRuleKey(pattern))) {
    return updateExcludePatterns(exclude, [pattern], true);
  }

  const existing = parseOtherFileTypeFilters(exclude).find(
    (item) => filterRuleKey(item.pattern) === filterRuleKey(pattern),
  );
  if (existing) return updateExcludePatterns(exclude, [pattern], true);

  const lines = exclude.split("\n");
  const block = findCustomFileFilterBlock(lines);
  if (block) {
    lines.splice(block.end, 0, pattern);
    return lines.join("\n");
  }

  const newBlock = [CUSTOM_FILE_TYPE_START, pattern, CUSTOM_FILE_TYPE_END].join(
    "\n",
  );
  const current = exclude.trimEnd();
  return current ? `${current}\n\n${newBlock}` : newBlock;
}

export function removeCustomFileTypeFilter(exclude: string, pattern: string) {
  const target = filterRuleKey(pattern);
  let lines = exclude
    .split("\n")
    .filter((line) => {
      const value = line.trim();
      const serialized = value.startsWith("# ")
        ? value.slice(2).trim()
        : value;
      return !serialized || filterRuleKey(serialized) !== target;
    });
  const block = findCustomFileFilterBlock(lines);
  if (!block) return lines.join("\n");

  const nextBlock = lines.slice(block.start + 1, block.end);
  if (nextBlock.some((line) => line.trim())) return lines.join("\n");

  const before = lines.slice(0, block.start).join("\n").trimEnd();
  const after = lines.slice(block.end + 1).join("\n").trimStart();
  return [before, after].filter(Boolean).join("\n\n");
}

// ---- 文件夹过滤标记块 ----

const FOLDER_BLOCK_START =
  "# --- 文件夹过滤（自动生成，请勿手动编辑）---";
const FOLDER_BLOCK_END = "# --- 文件夹过滤结束 ---";

/**
 * 判断一行是否是「文件夹过滤」生成的目录规则（标记块之外）。
 *
 * 后端保存时会去掉所有 `# ` 注释，标记块也随之消失，只剩裸的 `目录/` 行。
 * 系统目录预设、已停用的注释行和带通配符的旧默认规则（如 `.Trash-*`）
 * 不属于文件夹树，不能当成文件夹过滤，否则树一保存就会被冲掉。
 */
function normalizeFolderRuleLine(line: string): string {
  const value = line.trim();
  return value.startsWith("\\#") && value.endsWith("/")
    ? value.slice(1)
    : value;
}

function isUnmarkedFolderRule(line: string): boolean {
  const value = normalizeFolderRuleLine(line);
  if (!value.endsWith("/") || value.startsWith("# ") || value.includes("*")) {
    return false;
  }
  return !systemDirPresetPatterns().has(filterRuleKey(value));
}

function systemDirPresetPatterns() {
  return new Set<string>(
    systemDirFilterGroups.flatMap((group) =>
      [...group.patterns].map((pattern) => filterRuleKey(pattern)),
    ),
  );
}

function findFolderBlock(exclude: string) {
  const startIdx = exclude.indexOf(FOLDER_BLOCK_START);
  const endIdx = exclude.indexOf(FOLDER_BLOCK_END);
  if (startIdx === -1 || endIdx === -1 || endIdx <= startIdx) return null;
  return { startIdx, endIdx };
}

/** 从 exclude 文本中提取文件夹路径列表：标记块内的行 + 标记块被后端去掉后残留的目录行 */
export function parseExcludeFolders(exclude: string): string[] {
  const block = findFolderBlock(exclude);
  const inside = block
    ? exclude
        .slice(block.startIdx + FOLDER_BLOCK_START.length, block.endIdx)
        .split("\n")
        .map(normalizeFolderRuleLine)
        .filter(
          (l) =>
            l &&
            !l.startsWith("# ") &&
            (l.endsWith("/") || !l.startsWith("#")),
        )
    : [];
  const outside = (
    block
      ? exclude.slice(0, block.startIdx) +
        "\n" +
        exclude.slice(block.endIdx + FOLDER_BLOCK_END.length)
      : exclude
  )
    .split("\n")
    .filter(isUnmarkedFolderRule)
    .map(normalizeFolderRuleLine);
  return Array.from(new Set([...inside, ...outside]));
}

/** 更新 exclude 文本中的文件夹过滤内容，保留手动规则和系统目录预设不变 */
export function updateExcludeFolders(
  exclude: string,
  folders: string[],
): string {
  // 先去掉标记块之外残留的目录行，避免同一目录重复且取消勾选后仍生效
  const cleaned = exclude
    .split("\n")
    .filter((line) => !isUnmarkedFolderRule(line))
    .join("\n")
    .replace(/\n{3,}/g, "\n\n");
  const block = findFolderBlock(cleaned);

  const content =
    folders.length > 0
      ? [FOLDER_BLOCK_START, ...folders, FOLDER_BLOCK_END].join("\n")
      : "";

  // 已有标记块 — 替换
  if (block) {
    const before = cleaned.slice(0, block.startIdx).trimEnd();
    const after = cleaned
      .slice(block.endIdx + FOLDER_BLOCK_END.length)
      .trimStart();
    return [before, content, after].filter(Boolean).join("\n\n");
  }

  // 无标记块 — 追加到末尾
  if (!content) return cleaned === exclude ? exclude : cleaned.trimEnd();
  const trimmed = cleaned.trimEnd();
  return trimmed ? trimmed + "\n\n" + content : content;
}

/**
 * 判断源目录变更后是否已完全脱离原有源目录集合，以便自动清理失效的相对文件夹过滤规则。
 */
export function shouldClearExcludeFoldersOnSrcChange(
  prevSrcPaths: readonly string[],
  nextSrcPaths: readonly string[],
  exclude: string,
): boolean {
  if (!prevSrcPaths.length) return false;
  if (!parseExcludeFolders(exclude).length) return false;
  const prevSet = new Set(cleanPathList(prevSrcPaths).map((p) => p.toLowerCase()));
  const nextClean = cleanPathList(nextSrcPaths).map((p) => p.toLowerCase());
  return !nextClean.some((p) => prevSet.has(p));
}

/**
 * 生成任务总览页展示的过滤规则摘要标签。
 */
export function summarizeJobFilters(
  job: Pick<
    JobItem,
    | "exclude"
    | "minFileSize"
    | "maxFileSize"
    | "filterMode"
    | "include"
    | "minFileAgeMinutes"
    | "olderThanDays"
    | "newerThanDays"
  >,
): string[] {
  const items: string[] = [];
  if (Number(job.filterMode || 0) === 1) {
    const includeLines = (job.include || "")
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith("# "));
    items.push(`白名单 ${includeLines.length} 条`);
  }
  const minBytes = Number(job.minFileSize || 0);
  const maxBytes = Number(job.maxFileSize || 0);
  if (minBytes > 0) items.push(`排除 < ${formatSize(minBytes)}`);
  if (maxBytes > 0) items.push(`排除 > ${formatSize(maxBytes)}`);
  if (Number(job.minFileAgeMinutes || 0) > 0)
    items.push(`稳定 ≥ ${Number(job.minFileAgeMinutes)} 分钟`);
  if (Number(job.olderThanDays || 0) > 0)
    items.push(`早于 ${Number(job.olderThanDays)} 天`);
  if (Number(job.newerThanDays || 0) > 0)
    items.push(`最近 ${Number(job.newerThanDays)} 天`);

  const exclude = job.exclude || "";
  const presetFileCount = fileTypeFilterGroups.reduce(
    (sum, group) =>
      sum +
      group.patterns.filter((p) => isExcludePatternEnabled(exclude, p)).length,
    0,
  );
  const customFileCount = parseOtherFileTypeFilters(exclude).filter(
    (item) => item.enabled,
  ).length;
  const totalFileRules = presetFileCount + customFileCount;
  if (totalFileRules > 0) {
    items.push(`文件规则 ${totalFileRules} 条`);
  }

  const folderRules = parseExcludeFolders(exclude);
  if (folderRules.length > 0) {
    items.push(`排除目录 ${folderRules.length} 个`);
  }

  const systemDirCount = systemDirFilterGroups.reduce(
    (sum, group) =>
      sum +
      group.patterns.filter((p) => isExcludePatternEnabled(exclude, p)).length,
    0,
  );
  if (systemDirCount > 0) {
    items.push(`系统目录 ${systemDirCount} 项`);
  }

  return items;
}
