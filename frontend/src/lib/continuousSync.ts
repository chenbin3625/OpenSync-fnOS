import type {
  JobItem,
  NativeExtraFlags,
  SyncErrorSide,
  SyncErrorType,
  SyncStatus,
} from "../types";
import { formatTimestamp } from "../utils/date";

export type SyncDirectionKey = "oneway" | "bidirectional";

export interface ContinuousSyncMeta {
  directionKey: SyncDirectionKey;
  directionLabel: string;
  arrow: "⇄" | "→";
  syncDelete: boolean;
  pollIntervalSec: number;
  auditIntervalMin: number;
  debounceSec: number;
}

export const syncStatusLabels: Record<SyncStatus["state"], string> = {
  monitoring: "监控中",
  idle: "按周历待命",
  pending: "待同步",
  scanning: "扫描中",
  syncing: "同步中",
  synced: "已同步",
  waiting_window: "等待运行窗口",
  paused: "已暂停",
  error: "同步异常",
};

export type SyncStatusTone = "success" | "primary" | "warning" | "danger" | "muted";

export function syncStatusTone(state?: SyncStatus["state"]): SyncStatusTone {
  switch (state) {
    case "synced":
      return "success";
    case "monitoring":
    case "pending":
    case "scanning":
    case "syncing":
      return "primary";
    case "waiting_window":
      return "warning";
    case "error":
      return "danger";
    case "idle":
    case "paused":
    default:
      return "muted";
  }
}

export function isSyncStatusError(status?: SyncStatus | null): boolean {
  return Boolean(status && status.state === "error");
}

export interface SyncErrorInfo {
  type: SyncErrorType | "";
  side: SyncErrorSide | "";
  endpoint: string;
}

function classifyLegacySyncError(message: string): Pick<SyncErrorInfo, "type" | "side"> {
  const low = message.trim().toLowerCase();
  if (!low) return { type: "", side: "" };
  if (
    low.includes("登录状态已失效") ||
    low.includes("认证失败") ||
    low.includes("令牌已过期") ||
    low.includes("cookie 过期") ||
    low.includes("token expired") ||
    low.includes("unauthorized") ||
    low.includes("invalid_grant")
  ) {
    return { type: "auth", side: "remote" };
  }
  if (low.includes("限流") || low.includes("too many requests") || low.includes("429")) {
    return { type: "rate_limited", side: "remote" };
  }
  if (
    low.includes("本地授权目录") ||
    low.includes("未授权本地") ||
    low.includes("本地无权限") ||
    low.includes("无法读取本地") ||
    low.includes("本地权限") ||
    (low.includes("本地") &&
      (low.includes("权限被拒绝") ||
        low.includes("权限不足") ||
        low.includes("不可访问")))
  ) {
    return { type: "local_permission", side: "local" };
  }
  if (
    low.includes("无目录访问权限") ||
    low.includes("accessdenied") ||
    low.includes("permission denied") ||
    low.includes("forbidden") ||
    low.includes("403")
  ) {
    return { type: "remote_permission", side: "remote" };
  }
  if (
    low.includes("超时") ||
    low.includes("连接") ||
    low.includes("域名解析") ||
    low.includes("no such host") ||
    low.includes("i/o timeout") ||
    low.includes("deadline exceeded") ||
    low.includes("tls:")
  ) {
    return { type: "connection", side: "remote" };
  }
  return { type: "transfer", side: "unknown" };
}

export function resolveSyncError(status?: SyncStatus | null): SyncErrorInfo {
  if (!status) return { type: "", side: "", endpoint: "" };
  const fallback = classifyLegacySyncError(status.error || "");
  return {
    type: status.errorType || fallback.type,
    side: status.errorSide || fallback.side,
    endpoint: status.errorEndpoint || "",
  };
}

export function formatSyncNextAction(
  status?: SyncStatus | null,
  nowMs = Date.now(),
): string {
  if (!status || status.state === "paused") return "";
  const now = Math.floor(nowMs / 1000);
  const retryAt = Number(status.retryAt || 0);
  if (retryAt > now) return `下次重试 ${formatTimestamp(retryAt, true)}`;
  const nextPoll = Number(status.nextPoll || 0);
  if (nextPoll > now) return `下次检查 ${formatTimestamp(nextPoll, true)}`;
  return "";
}

export function parseJobExtraFlagsObject(
  raw?: string | NativeExtraFlags | null,
): NativeExtraFlags {
  if (!raw) return {};
  if (typeof raw === "object") return raw;
  const trimmed = raw.trim();
  if (!trimmed) return {};
  try {
    const parsed: unknown = JSON.parse(trimmed);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed as NativeExtraFlags;
    }
  } catch {
    /* ignore malformed JSON */
  }
  return {};
}

export function getContinuousSyncMeta(job: {
  method?: number | null;
  extraFlags?: string | NativeExtraFlags | null;
  syncStatus?: SyncStatus | null;
}): ContinuousSyncMeta {
  const extra = parseJobExtraFlagsObject(job.extraFlags);
  const method = Number(job.method ?? 0);
  const bidirectional = method === 3;
  const poll =
    Number(job.syncStatus?.pollIntervalSec || extra.syncPollIntervalSec || 0) || 60;
  const audit =
    Number(job.syncStatus?.auditIntervalMin || extra.syncAuditIntervalMin || 0) ||
    1440;
  const debounce =
    Number(job.syncStatus?.debounceSec || extra.watchDebounceSec || 0) || 30;

  return {
    directionKey: bidirectional ? "bidirectional" : "oneway",
    directionLabel: bidirectional ? "双向同步" : "单向同步",
    arrow: bidirectional ? "⇄" : "→",
    syncDelete:
      bidirectional ||
      method === 1 ||
      extra.oneWayDeleteMode === "tracked" ||
      extra.oneWayDeleteMode === "mirror",
    pollIntervalSec: poll,
    auditIntervalMin: audit,
    debounceSec: debounce,
  };
}

export function formatSecondsShort(seconds: number): string {
  const s = Math.max(1, Math.trunc(Number(seconds) || 60));
  if (s % 3600 === 0 && s >= 3600) return `${s / 3600} 小时`;
  if (s % 60 === 0 && s >= 60) return `${s / 60} 分钟`;
  return `${s} 秒`;
}

export function formatMinutesShort(minutes: number): string {
  const m = Math.max(1, Math.trunc(Number(minutes) || 1440));
  if (m === 1440) return "每天";
  if (m % 1440 === 0) return `每 ${m / 1440} 天`;
  if (m % 60 === 0 && m >= 60) return `每 ${m / 60} 小时`;
  return `每 ${m} 分钟`;
}

export function formatContinuousSyncPlan(meta: {
  pollIntervalSec?: number;
  auditIntervalMin?: number;
  debounceSec?: number;
}): string {
  const poll = formatSecondsShort(meta.pollIntervalSec || 60);
  const audit = formatMinutesShort(meta.auditIntervalMin || 1440);
  const debounce = formatSecondsShort(meta.debounceSec || 30);
  return `本地实时监听 · 远端每 ${poll} 检测 · 静默 ${debounce} 触发 · ${audit}完整核对`;
}

export function formatRelativeSyncTime(
  unixSeconds?: number | null,
  nowMs = Date.now(),
): string {
  const ts = Number(unixSeconds || 0);
  if (!Number.isFinite(ts) || ts <= 0) return "";
  const diffSec = Math.floor(nowMs / 1000) - ts;
  if (diffSec < 15) return "刚刚";
  if (diffSec < 60) return `${diffSec} 秒前`;
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin} 分钟前`;
  const diffHour = Math.floor(diffMin / 60);
  if (diffHour < 24) return `${diffHour} 小时前`;
  const diffDay = Math.floor(diffHour / 24);
  if (diffDay < 30) return `${diffDay} 天前`;
  return formatTimestamp(ts);
}

export function resolveEffectiveSyncStatus(job: {
  enable: number;
  running?: boolean;
  paused?: boolean;
  syncStatus?: SyncStatus | null;
}): SyncStatus {
  if (job.syncStatus) return job.syncStatus;
  if (job.enable !== 1) {
    return {
      state: "paused",
      pendingChanges: 0,
      lastSuccess: 0,
      lastPoll: 0,
      nextPoll: 0,
      error: "",
      failures: 0,
    };
  }
  if (job.running) {
    return {
      state: job.paused ? "paused" : "syncing",
      pendingChanges: 0,
      lastSuccess: 0,
      lastPoll: 0,
      nextPoll: 0,
      error: "",
      failures: 0,
    };
  }
  return {
    state: "monitoring",
    pendingChanges: 0,
    lastSuccess: 0,
    lastPoll: 0,
    nextPoll: 0,
    error: "",
    failures: 0,
  };
}

export function jobStatusDotClass(job: JobItem): string {
  if (job.legacyOpenList) return "job-dot job-dot--error";
  if (job.syncStatus?.state === "error") return "job-dot job-dot--error";
  if (job.running && job.paused) return "job-dot job-dot--paused";
  if (
    job.running ||
    job.syncStatus?.state === "syncing" ||
    job.syncStatus?.state === "scanning" ||
    job.syncStatus?.state === "pending"
  ) {
    return "job-dot job-dot--running";
  }
  if (job.queued) return "job-dot job-dot--queued";
  if (job.syncStatus?.state === "waiting_window") return "job-dot job-dot--paused";
  if (job.enable !== 1 || job.syncStatus?.state === "paused") {
    return "job-dot job-dot--disabled";
  }
  if (job.lastStatus && [3, 5, 6, 7].includes(job.lastStatus) && !job.syncStatus) {
    return "job-dot job-dot--error";
  }
  return "job-dot job-dot--synced";
}
