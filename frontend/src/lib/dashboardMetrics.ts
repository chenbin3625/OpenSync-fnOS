import { formatSize } from "../pages/Home/homeUtils";
import type {
  DashboardJobItem,
  DashboardOverview,
  DashboardQuotaItem,
  DashboardSpeedSample,
} from "../types";

export const MAX_SPEED_HISTORY_POINTS = 30;

export function safeSpeed(bytesPerSec?: number | null): number {
  const n = Number(bytesPerSec);
  if (!Number.isFinite(n) || n <= 0) return 0;
  return n;
}

export function formatSpeed(bytesPerSec?: number | null): string {
  const sp = safeSpeed(bytesPerSec);
  if (sp <= 0) return "0 B/s";
  return `${formatSize(sp)}/s`;
}

/** Compact speed label for chart Y-axis ticks (strips redundant `.00` decimals). */
export function formatAxisSpeed(bytesPerSec?: number | null): string {
  const sp = safeSpeed(bytesPerSec);
  if (sp <= 0) return "0";
  const units = ["B/s", "KB/s", "MB/s", "GB/s", "TB/s"];
  let size = sp;
  let idx = 0;
  while (size >= 1024 && idx < units.length - 1) {
    size /= 1024;
    idx += 1;
  }
  const rounded =
    idx === 0 || size >= 10 || Math.abs(size - Math.round(size)) < 0.05
      ? String(Math.round(size))
      : size.toFixed(1);
  return `${rounded} ${units[idx]}`;
}

export function splitSpeedParts(bytesPerSec?: number | null): {
  value: string;
  unit: string;
} {
  const sp = safeSpeed(bytesPerSec);
  if (sp <= 0) return { value: "0", unit: "B/s" };
  const units = ["B/s", "KB/s", "MB/s", "GB/s", "TB/s"];
  let size = sp;
  let idx = 0;
  while (size >= 1024 && idx < units.length - 1) {
    size /= 1024;
    idx += 1;
  }
  const value =
    idx === 0
      ? String(Math.round(size))
      : size >= 100
        ? size.toFixed(1)
        : size.toFixed(2);
  return { value, unit: units[idx] };
}

export function formatMbps(bytesPerSec?: number | null): string {
  const sp = safeSpeed(bytesPerSec);
  if (sp <= 0) return "";
  const bitsPerSec = sp * 8;
  if (bitsPerSec >= 1e9) return `${(bitsPerSec / 1e9).toFixed(2)} Gbps`;
  if (bitsPerSec >= 1e6) {
    const mbps = bitsPerSec / 1e6;
    return `${mbps >= 100 ? Math.round(mbps) : mbps.toFixed(1)} Mbps`;
  }
  if (bitsPerSec >= 1e3) return `${Math.round(bitsPerSec / 1e3)} Kbps`;
  return `${Math.round(bitsPerSec)} bps`;
}

export function mergeSpeedHistory(
  prev: DashboardSpeedSample[],
  incoming: DashboardSpeedSample[] | undefined,
  nowSec: number,
  currentSpeed: number,
  maxPoints = MAX_SPEED_HISTORY_POINTS,
): DashboardSpeedSample[] {
  const cleanSpeed = safeSpeed(currentSpeed);
  if (incoming && incoming.length > 0) {
    const normalized = incoming
      .map((s) => ({
        ts: Math.floor(Number(s.ts) || nowSec),
        speed: safeSpeed(s.speed),
      }))
      .sort((a, b) => a.ts - b.ts);
    const merged: DashboardSpeedSample[] = [];
    for (const s of normalized) {
      const last = merged[merged.length - 1];
      if (last && last.ts === s.ts) {
        last.speed = s.speed;
      } else {
        merged.push(s);
      }
    }
    return merged.slice(-maxPoints);
  }

  const next = prev.slice();
  const ts = Math.floor(nowSec);
  const last = next[next.length - 1];
  if (last && last.ts >= ts) {
    next[next.length - 1] = { ts: last.ts, speed: cleanSpeed };
  } else {
    next.push({ ts, speed: cleanSpeed });
  }
  return next.slice(-maxPoints);
}

export interface DerivedDashboardRealtime {
  totalSpeed: number;
  totalSpeedAvg: number;
  peakSpeed: number;
  activeJobs: number;
  pausedJobs: number;
  activeTransfers: number;
  totalDoneBytes: number;
  totalRemainBytes: number;
  totalBytes: number;
  overallPercent: number;
  estimatedRemainTime: number;
  globalBwLimit: string;
  speedHistory: DashboardSpeedSample[];
  isTransferring: boolean;
  isAllPaused: boolean;
}

export function deriveDashboardRealtime(
  data: DashboardOverview | null | undefined,
  localHistory: DashboardSpeedSample[],
): DerivedDashboardRealtime {
  const jobs = data?.jobs || [];
  const rt = data?.realtime;

  let fallbackSpeed = 0;
  let fallbackSpeedAvg = 0;
  let fallbackActive = 0;
  let fallbackPaused = 0;
  let fallbackDone = 0;
  let fallbackRemain = 0;

  for (const job of jobs) {
    if (!job.running) continue;
    if (job.paused) {
      fallbackPaused += 1;
      continue;
    }
    fallbackActive += 1;
    fallbackSpeed += safeSpeed(job.liveSpeed);
    fallbackSpeedAvg += safeSpeed(job.liveSpeedAvg);
    const done = Math.max(0, Number(job.liveDoneSize || 0));
    const total = Math.max(done, Number(job.liveTotalSize || 0));
    fallbackDone += done;
    fallbackRemain += Math.max(0, total - done);
  }

  const totalSpeed = rt ? safeSpeed(rt.totalSpeed) : fallbackSpeed;
  const totalSpeedAvg = rt ? safeSpeed(rt.totalSpeedAvg) : fallbackSpeedAvg;
  const activeJobs = rt ? Math.max(0, rt.activeJobs) : fallbackActive;
  const pausedJobs = rt ? Math.max(0, rt.pausedJobs) : fallbackPaused;
  const activeTransfers = rt ? Math.max(0, rt.activeTransfers) : 0;
  const totalDoneBytes = rt ? Math.max(0, rt.totalDoneBytes) : fallbackDone;
  const totalRemainBytes = rt
    ? Math.max(0, rt.totalRemainBytes)
    : fallbackRemain;
  const totalBytes = totalDoneBytes + totalRemainBytes;
  const overallPercent =
    totalBytes > 0
      ? Math.min(99, Math.round((totalDoneBytes / totalBytes) * 100))
      : 0;

  let estimatedRemainTime = rt ? Math.max(0, rt.estimatedRemainTime) : 0;
  if (!rt && activeJobs > 0 && totalRemainBytes > 0) {
    const rate =
      totalSpeed > 0 && totalSpeedAvg > 0
        ? 0.6 * totalSpeed + 0.4 * totalSpeedAvg
        : totalSpeed || totalSpeedAvg;
    if (rate > 0) {
      estimatedRemainTime = Math.ceil(totalRemainBytes / rate);
    }
  }

  const speedHistory =
    rt && rt.speedHistory && rt.speedHistory.length > 0
      ? rt.speedHistory
      : localHistory;

  let peakSpeed = Math.max(
    totalSpeed,
    rt ? safeSpeed(rt.peakSpeed) : 0,
  );
  for (const s of speedHistory) {
    const sp = safeSpeed(s.speed);
    if (sp > peakSpeed) peakSpeed = sp;
  }

  const isTransferring = activeJobs > 0 || activeTransfers > 0;
  const isAllPaused = !isTransferring && pausedJobs > 0;

  return {
    totalSpeed,
    totalSpeedAvg,
    peakSpeed,
    activeJobs,
    pausedJobs,
    activeTransfers,
    totalDoneBytes,
    totalRemainBytes,
    totalBytes,
    overallPercent,
    estimatedRemainTime,
    globalBwLimit: (rt?.globalBwLimit || "").trim(),
    speedHistory,
    isTransferring,
    isAllPaused,
  };
}

export interface SpeedChartPoint {
  x: number;
  y: number;
  ts: number;
  speed: number;
}

export interface SpeedChartGeometry {
  points: SpeedChartPoint[];
  linePath: string;
  areaPath: string;
  yMax: number;
  yMid: number;
  hasSignal: boolean;
}

function niceSpeedCeiling(peakBytesPerSec: number): number {
  if (!Number.isFinite(peakBytesPerSec) || peakBytesPerSec <= 0) {
    return 1024 * 1024; // 1 MB/s default scale when idle
  }
  const units = [1, 1024, 1024 ** 2, 1024 ** 3, 1024 ** 4];
  let unit = 1;
  for (let i = units.length - 1; i >= 0; i -= 1) {
    if (peakBytesPerSec >= units[i]) {
      unit = units[i];
      break;
    }
  }
  const scaled = peakBytesPerSec / unit;
  const niceSteps = [1, 2, 5, 10, 15, 20, 30, 50, 60, 80, 100, 150, 200, 300, 500, 1024];
  for (const step of niceSteps) {
    if (scaled <= step * 0.96) {
      return step * unit;
    }
  }
  return Math.ceil(scaled / 100) * 100 * unit;
}

export function buildSpeedChartGeometry(
  samples: DashboardSpeedSample[],
  width: number,
  height: number,
  padTop = 10,
  padBottom = 6,
): SpeedChartGeometry {
  const usableHeight = Math.max(1, height - padTop - padBottom);
  const baselineY = height - padBottom;
  const clean = samples.map((s) => ({
    ts: s.ts,
    speed: safeSpeed(s.speed),
  }));

  const maxRaw = clean.reduce((m, s) => Math.max(m, s.speed), 0);
  const hasSignal = maxRaw > 0;
  const yMax = niceSpeedCeiling(maxRaw);
  const yMid = yMax / 2;

  if (clean.length === 0) {
    return {
      points: [],
      linePath: "",
      areaPath: "",
      yMax,
      yMid,
      hasSignal: false,
    };
  }

  // Fill the available horizontal span cleanly when >= 2 samples exist so
  // the waveform is immediately legible without wasted empty left space.
  const count = clean.length;
  const points: SpeedChartPoint[] = clean.map((sample, idx) => {
    const x = count > 1 ? (idx / (count - 1)) * width : width;
    const ratio = Math.min(1, sample.speed / yMax);
    const y = baselineY - ratio * usableHeight;
    return {
      x: Number(x.toFixed(2)),
      y: Number(y.toFixed(2)),
      ts: sample.ts,
      speed: sample.speed,
    };
  });

  // If there is only 1 sample, draw a flat horizontal span across the chart.
  const renderPoints =
    points.length === 1 ? [{ ...points[0], x: 0 }, points[0]] : points;

  const lineParts = renderPoints.map(
    (pt, i) => `${i === 0 ? "M" : "L"}${pt.x},${pt.y}`,
  );
  const linePath = lineParts.join(" ");
  const firstX = renderPoints[0].x;
  const lastX = renderPoints[renderPoints.length - 1].x;
  const areaPath = `${linePath} L${lastX},${baselineY} L${firstX},${baselineY} Z`;

  return {
    points,
    linePath,
    areaPath,
    yMax,
    yMid,
    hasSignal,
  };
}

export function findNextScheduledJob(
  jobs: DashboardJobItem[],
  nowSec = Math.floor(Date.now() / 1000),
): DashboardJobItem | null {
  let best: DashboardJobItem | null = null;
  for (const job of jobs) {
    if (job.enable !== 1 || job.running || job.legacyOpenList) continue;
    const nextRun = Number(job.nextRunTime || 0);
    if (nextRun <= 0 || nextRun < nowSec - 60) continue;
    if (!best || nextRun < best.nextRunTime) {
      best = job;
    }
  }
  return best;
}

export interface NormalizedStorageQuota extends DashboardQuotaItem {
  totalBytes: number;
  usedBytes: number;
  percent: number;
}

export function sortStorageQuotas(
  quotas?: DashboardQuotaItem[] | null,
): NormalizedStorageQuota[] {
  if (!quotas || quotas.length === 0) return [];
  return quotas
    .filter((q) => q.supported && (q.total || 0) > 0)
    .map((q) => {
      const totalBytes = q.total || 0;
      const usedBytes =
        q.used !== undefined
          ? q.used
          : q.free !== undefined
            ? Math.max(0, totalBytes - q.free)
            : 0;
      const percent =
        totalBytes > 0
          ? Math.min(100, Math.round((usedBytes / totalBytes) * 100))
          : 0;
      return { ...q, totalBytes, usedBytes, percent };
    })
    .sort((a, b) => b.percent - a.percent);
}
