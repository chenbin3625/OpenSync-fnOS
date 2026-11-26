import { describe, expect, it } from "vitest";
import {
  buildSpeedChartGeometry,
  deriveDashboardRealtime,
  findNextScheduledJob,
  formatAxisSpeed,
  formatMbps,
  formatSpeed,
  mergeSpeedHistory,
  sortStorageQuotas,
  splitSpeedParts,
} from "../src/lib/dashboardMetrics";
import type { DashboardJobItem, DashboardOverview } from "../src/types";

function makeJob(overrides: Partial<DashboardJobItem> = {}): DashboardJobItem {
  return {
    id: 1,
    remark: "测试任务",
    enable: 1,
    alistId: 1,
    alistName: "内置存储",
    method: 0,
    isCron: 1,
    interval: 0,
    srcPath: '["/src"]',
    dstPath: '["/dst"]',
    running: false,
    queued: false,
    currentTaskId: 0,
    liveProgress: 0,
    nextRunTime: 0,
    ...overrides,
  };
}

describe("dashboardMetrics", () => {
  it("formats speed values, split parts, and Mbps equivalents", () => {
    expect(formatSpeed(0)).toBe("0 B/s");
    expect(formatSpeed(-10)).toBe("0 B/s");
    expect(formatSpeed(15 * 1024 * 1024)).toBe("15.00 MB/s");
    expect(formatAxisSpeed(0)).toBe("0");
    expect(formatAxisSpeed(50 * 1024 * 1024)).toBe("50 MB/s");
    expect(formatAxisSpeed(2.5 * 1024 * 1024)).toBe("2.5 MB/s");

    expect(splitSpeedParts(0)).toEqual({ value: "0", unit: "B/s" });
    expect(splitSpeedParts(24.5 * 1024 * 1024)).toEqual({
      value: "24.50",
      unit: "MB/s",
    });
    expect(splitSpeedParts(156.4 * 1024 * 1024)).toEqual({
      value: "156.4",
      unit: "MB/s",
    });

    expect(formatMbps(0)).toBe("");
    expect(formatMbps(12.5 * 1024 * 1024)).toBe("105 Mbps");
    expect(formatMbps(200 * 1024 * 1024)).toBe("1.68 Gbps");
  });

  it("derives realtime metrics from backend realtime payload and falls back to jobs when absent", () => {
    const legacyOverview: DashboardOverview = {
      kpi: {
        totalJobs: 2,
        enabledJobs: 2,
        runningJobs: 2,
        queuedJobs: 0,
        pendingApprovals: 0,
        totalEngines: 1,
        onlineEngines: 1,
        offlineEngines: 0,
      },
      recent7d: {
        totalRuns: 4,
        successRuns: 4,
        failRuns: 0,
        stoppedRuns: 0,
        totalFiles: 100,
        totalBytes: 1024 * 1024 * 100,
      },
      pendingApprovals: [],
      jobs: [
        makeJob({
          id: 1,
          running: true,
          liveSpeed: 10 * 1024 * 1024,
          liveDoneSize: 50 * 1024 * 1024,
          liveTotalSize: 150 * 1024 * 1024,
        }),
        makeJob({
          id: 2,
          running: true,
          paused: true,
          liveSpeed: 5 * 1024 * 1024,
          liveDoneSize: 20 * 1024 * 1024,
          liveTotalSize: 80 * 1024 * 1024,
        }),
      ],
    };

    const derived = deriveDashboardRealtime(legacyOverview, [
      { ts: 1000, speed: 12 * 1024 * 1024 },
    ]);
    expect(derived.activeJobs).toBe(1);
    expect(derived.pausedJobs).toBe(1);
    expect(derived.totalSpeed).toBe(10 * 1024 * 1024);
    expect(derived.peakSpeed).toBe(12 * 1024 * 1024);
    expect(derived.totalDoneBytes).toBe(50 * 1024 * 1024);
    expect(derived.totalRemainBytes).toBe(100 * 1024 * 1024);
    expect(derived.estimatedRemainTime).toBe(10);
    expect(derived.isTransferring).toBe(true);
    expect(derived.isAllPaused).toBe(false);
  });

  it("merges speed history with deduplication and bounded length", () => {
    const merged = mergeSpeedHistory(
      [{ ts: 100, speed: 1000 }],
      undefined,
      100,
      2500,
      5,
    );
    expect(merged).toEqual([{ ts: 100, speed: 2500 }]);

    const serverMerged = mergeSpeedHistory(
      [],
      [
        { ts: 102, speed: 3000 },
        { ts: 100, speed: 1000 },
        { ts: 102, speed: 4000 },
      ],
      102,
      4000,
      5,
    );
    expect(serverMerged).toEqual([
      { ts: 100, speed: 1000 },
      { ts: 102, speed: 4000 },
    ]);
  });

  it("builds valid SVG chart geometry with nice Y-axis scaling", () => {
    const geom = buildSpeedChartGeometry(
      [
        { ts: 100, speed: 5 * 1024 * 1024 },
        { ts: 102, speed: 18 * 1024 * 1024 },
        { ts: 104, speed: 12 * 1024 * 1024 },
      ],
      600,
      100,
    );
    expect(geom.hasSignal).toBe(true);
    expect(geom.yMax).toBe(20 * 1024 * 1024);
    expect(geom.points).toHaveLength(3);
    expect(geom.linePath).toContain("M");
    expect(geom.areaPath).toContain("Z");
    expect(geom.points[2].x).toBe(600);
  });

  it("finds earliest upcoming scheduled job and sorts quotas by usage descending", () => {
    const nowSec = 1_700_000_000;
    const next = findNextScheduledJob(
      [
        makeJob({ id: 1, enable: 0, nextRunTime: nowSec + 60 }),
        makeJob({ id: 2, running: true, nextRunTime: nowSec + 120 }),
        makeJob({ id: 3, enable: 1, nextRunTime: nowSec + 3600 }),
        makeJob({ id: 4, enable: 1, nextRunTime: nowSec + 600 }),
      ],
      nowSec,
    );
    expect(next?.id).toBe(4);

    const sorted = sortStorageQuotas([
      { name: "Local", type: "local", supported: true, total: 1000, used: 200 },
      { name: "Cloud", type: "s3", supported: true, total: 1000, used: 920 },
      { name: "Unsupported", type: "webdav", supported: false },
    ]);
    expect(sorted.map((q) => q.name)).toEqual(["Cloud", "Local"]);
    expect(sorted[0].percent).toBe(92);
  });
});
