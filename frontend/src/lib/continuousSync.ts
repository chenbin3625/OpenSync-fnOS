import type { NativeExtraFlags, SyncStatus } from "../types";
import { formatTimestamp } from "../utils/date";

export type SyncDirectionKey = "bidirectional" | "upload" | "download" | "custom";

export interface ContinuousSyncMeta {
  directionKey: SyncDirectionKey;
  directionLabel: string;
  shortLabel: string;
  arrow: "⇄" | "→";
  syncDelete: boolean;
  pollIntervalSec: number;
  auditIntervalMin: number;
  debounceSec: number;
}

export const syncStatusLabels: Record<SyncStatus["state"], string> = {
  monitoring: "监控中",
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
    case "paused":
    default:
      return "muted";
  }
}

export function isSyncStatusError(status?: SyncStatus | null): boolean {
  return Boolean(status && status.state === "error");
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
  const rawDir = (job.syncStatus?.direction || extra.syncDirection || "").trim();
  const poll =
    Number(job.syncStatus?.pollIntervalSec || extra.syncPollIntervalSec || 0) || 60;
  const audit =
    Number(job.syncStatus?.auditIntervalMin || extra.syncAuditIntervalMin || 0) ||
    1440;
  const debounce =
    Number(job.syncStatus?.debounceSec || extra.watchDebounceSec || 0) || 30;

  if (method === 3) {
    return {
      directionKey: "bidirectional",
      directionLabel: "双向同步",
      shortLabel: "双向同步",
      arrow: "⇄",
      syncDelete: true,
      pollIntervalSec: poll,
      auditIntervalMin: audit,
      debounceSec: debounce,
    };
  }
  const syncDelete = method === 1;
  if (rawDir === "upload") {
    return {
      directionKey: "upload",
      directionLabel: "仅上传本地更改",
      shortLabel: "单向上传",
      arrow: "→",
      syncDelete,
      pollIntervalSec: poll,
      auditIntervalMin: audit,
      debounceSec: debounce,
    };
  }
  if (rawDir === "download") {
    return {
      directionKey: "download",
      directionLabel: "仅下载远端更改",
      shortLabel: "单向下载",
      arrow: "→",
      syncDelete,
      pollIntervalSec: poll,
      auditIntervalMin: audit,
      debounceSec: debounce,
    };
  }
  return {
    directionKey: "custom",
    directionLabel: "自定义单向同步",
    shortLabel: "单向同步",
    arrow: "→",
    syncDelete,
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
