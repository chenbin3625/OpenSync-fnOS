import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("../src/components/common", () => ({
  getErrorMessage: (err: unknown, fallback = "操作失败") =>
    err instanceof Error ? err.message : fallback,
}));
import {
  applyProgressFrame,
  createJobProgressStream,
  type JobProgressStreamState,
} from "../src/pages/Home/jobProgressStream";
import {
  buildRealtimeTaskListKey,
  buildRealtimeTaskQueryKey,
  buildRealtimeTaskTableKey,
  isSameRealtimeTaskListKey,
} from "../src/pages/Home/useRealtimeTaskItems";
import {
  advanceSnapshotHistory,
  selectPreviousSnapshot,
} from "../src/pages/Home/useRealtimeTask";
import {
  formatNextRun,
  formatSchedule,
  formatSchedulePlan,
  methodLabel,
  normalizeDateRange,
  normalizeSettingFieldValue,
  parseIntervalInput,
} from "../src/pages/Home/homeUtils";
import {
  hasPendingFileSizeDraftChange,
  mergeFileSizeDrafts,
} from "../src/pages/Home/fileSizeUnits";
import { defaultJobForm, validateJobFormStep } from "../src/lib/taskForm";
import type { CurrentTaskData, JobItem } from "../src/types";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  close() {
    this.closed = true;
  }

  open() {
    this.onopen?.();
  }

  emit(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) });
  }
}

const makeTask = (overrides: Partial<CurrentTaskData> = {}): CurrentTaskData => ({
  taskId: 42,
  scanFinish: true,
  createTime: 1_700_000_000,
  duration: 10,
  num: { wait: 1, running: 1, success: 2, fail: 0, other: 0 },
  size: { wait: 10, running: 20, success: 30, fail: 0, other: 0 },
  doneSize: 30,
  remainSize: 30,
  speed: 100,
  speedAvg: 80,
  remainTime: 5,
  doingTask: [{ id: 1, fileName: "a.bin", status: 1, progress: 25 }],
  ...overrides,
});

describe("useRealtimeTaskItems list/table keys", () => {
  it("keeps listKey and tableKey stable across page turns even when taskIdentity contains colons", () => {
    const taskIdentity = "42:1700000000";
    const list1 = buildRealtimeTaskListKey({
      jobId: "7",
      taskIdentity,
      activeTab: 1,
      pageSize: 10,
      refreshToken: 0,
    });
    const list2 = buildRealtimeTaskListKey({
      jobId: "7",
      taskIdentity,
      activeTab: 1,
      pageSize: 10,
      refreshToken: 0,
    });
    const table1 = buildRealtimeTaskTableKey({
      jobId: "7",
      taskIdentity,
      activeTab: 1,
      refreshToken: 0,
    });
    const query1 = buildRealtimeTaskQueryKey({
      jobId: "7",
      taskIdentity,
      activeTab: 1,
      tabTaskPage: 1,
      pageSize: 10,
      refreshToken: 0,
    });
    const query2 = buildRealtimeTaskQueryKey({
      jobId: "7",
      taskIdentity,
      activeTab: 1,
      tabTaskPage: 2,
      pageSize: 10,
      refreshToken: 0,
    });

    expect(isSameRealtimeTaskListKey(list1, list2)).toBe(true);
    expect(query1).not.toBe(query2);
    expect(table1).toBe(
      buildRealtimeTaskTableKey({
        jobId: "7",
        taskIdentity,
        activeTab: 1,
        refreshToken: 0,
      }),
    );
  });

  it("invalidates listKey and tableKey when switching tabs or tasks", () => {
    const taskIdentity = "42:1700000000";
    const runningList = buildRealtimeTaskListKey({
      jobId: "7",
      taskIdentity,
      activeTab: 1,
      pageSize: 10,
      refreshToken: 0,
    });
    const successList = buildRealtimeTaskListKey({
      jobId: "7",
      taskIdentity,
      activeTab: 2,
      pageSize: 10,
      refreshToken: 1,
    });
    const nextTaskList = buildRealtimeTaskListKey({
      jobId: "7",
      taskIdentity: "43:1700000100",
      activeTab: 1,
      pageSize: 10,
      refreshToken: 0,
    });

    expect(isSameRealtimeTaskListKey(runningList, successList)).toBe(false);
    expect(isSameRealtimeTaskListKey(runningList, nextTaskList)).toBe(false);
    expect(
      buildRealtimeTaskTableKey({
        jobId: "7",
        taskIdentity,
        activeTab: 1,
        refreshToken: 0,
      }),
    ).not.toBe(
      buildRealtimeTaskTableKey({
        jobId: "7",
        taskIdentity,
        activeTab: 2,
        refreshToken: 1,
      }),
    );
  });
});

describe("useRealtimeTask StrictMode-safe snapshot history", () => {
  it("returns the same previous snapshot across repeated render reads and idempotent effect commits", () => {
    const frame1 = makeTask({ duration: 10, doneSize: 100 });
    const frame2 = makeTask({ duration: 12, doneSize: 200 });

    let history = advanceSnapshotHistory({ current: null, previous: null }, frame1);
    // StrictMode double render while transitioning from frame1 -> frame2
    expect(selectPreviousSnapshot(history, frame2)).toBe(frame1);
    expect(selectPreviousSnapshot(history, frame2)).toBe(frame1);

    // StrictMode double effect commit
    history = advanceSnapshotHistory(history, frame2);
    history = advanceSnapshotHistory(history, frame2);

    expect(selectPreviousSnapshot(history, frame2)).toBe(frame1);

    // Task identity change clears previous
    const frameOtherTask = makeTask({ taskId: 43, duration: 1, doneSize: 10 });
    expect(selectPreviousSnapshot(history, frameOtherTask)).toBeNull();
    history = advanceSnapshotHistory(history, null);
    expect(history).toEqual({ current: null, previous: null });
  });
});

describe("jobProgressStream omitempty reset & non-200 recovery", () => {
  const original = globalThis.EventSource;
  beforeEach(() => {
    FakeEventSource.instances = [];
    globalThis.EventSource = FakeEventSource as unknown as typeof EventSource;
  });
  afterEach(() => {
    globalThis.EventSource = original;
  });

  it("resets omitted numeric Go omitempty fields to 0 and clears stale scan", () => {
    const prev = makeTask({
      scanFinish: false,
      scan: { scannedDirs: 3, remainingDirs: 2, totalDirs: 5 },
      speed: 1024,
      speedAvg: 512,
      remainTime: 60,
    });
    const stalledPayload = {
      taskId: 42,
      scanFinish: true,
      createTime: 1_700_000_000,
      duration: 20,
      num: { wait: 1, running: 1, success: 2, fail: 0, other: 0 },
      size: { wait: 10, running: 20, success: 30, fail: 0, other: 0 },
      doneSize: 30,
      remainSize: 30,
      doingTask: null,
      doingPatch: [{ id: 1, status: 1, progress: 25 }],
      // speed, speedAvg, remainTime, scan omitted by Go omitempty
    };

    const next = applyProgressFrame(prev, stalledPayload);
    expect(next).not.toBeNull();
    expect(next?.speed).toBe(0);
    expect(next?.speedAvg).toBe(0);
    expect(next?.remainTime).toBe(0);
    expect(next?.scan).toBeUndefined();
    expect(next?.doingTask).toHaveLength(1);
    expect(next?.doingTask?.[0]).toMatchObject({ id: 1, fileName: "a.bin" });
  });

  it("recovers live=true when code=200 frames resume after a non-200 envelope", () => {
    const states: JobProgressStreamState[] = [];
    const frames: (CurrentTaskData | null)[] = [];
    const stream = createJobProgressStream({
      jobId: "9",
      onData: (data) => frames.push(data),
      onStateChange: (state) => states.push(state),
    });
    const source = FakeEventSource.instances.at(-1)!;
    source.open();
    expect(states.at(-1)?.live).toBe(true);

    source.emit({ code: 500, data: null, msg: "err" });
    expect(states.at(-1)).toEqual({ live: false, reason: "error" });

    source.emit({ code: 200, data: makeTask(), msg: "success" });
    expect(states.at(-1)).toEqual({ live: true });
    expect(frames).toHaveLength(1);
    stream.close();
  });
});

describe("TaskEditor draft & interval validation", () => {
  it("does not truncate decimal interval inputs so validation rejects them", () => {
    expect(parseIntervalInput("15")).toBe(15);
    expect(parseIntervalInput("1.5")).toBe(1.5);
    expect(parseIntervalInput("1.0")).toBe(0);
    expect(parseIntervalInput("abc")).toBe(0);
    expect(parseIntervalInput("")).toBe(0);

    const form = {
      ...defaultJobForm(1),
      isCron: 0,
      interval: parseIntervalInput("1.5"),
    };
    expect(validateJobFormStep(form, 1)).toBe("执行间隔必须是大于 0 的整数");
  });

  it("tracks dirty state and validates unblurred file size drafts", () => {
    const base = defaultJobForm(1);
    expect(hasPendingFileSizeDraftChange(base, {})).toBe(false);
    expect(hasPendingFileSizeDraftChange(base, { minFileSize: "0" })).toBe(false);
    expect(hasPendingFileSizeDraftChange(base, { minFileSize: "25" })).toBe(true);

    const invalidMerged = mergeFileSizeDrafts(base, { minFileSize: "-5" });
    expect(validateJobFormStep(invalidMerged, 2)).toBe("文件大小必须是有效的非负数");

    const rangeInvalid = mergeFileSizeDrafts(
      { ...base, minFileSize: 100, maxFileSize: 200 },
      { minFileSize: "300" },
    );
    expect(validateJobFormStep(rangeInvalid, 2)).toBe(
      "最大文件大小必须大于等于最小文件大小",
    );
  });
});

describe("TaskExecution normalizeDateRange", () => {
  it("rejects null, undefined, partial, and invalid dates instead of producing NaN/1970", () => {
    expect(normalizeDateRange(null)).toEqual([]);
    expect(normalizeDateRange(undefined)).toEqual([]);
    expect(normalizeDateRange([null, null])).toEqual([]);
    expect(normalizeDateRange([undefined, new Date("2025-01-02")])).toEqual([]);
    expect(normalizeDateRange([new Date("2025-01-01"), null])).toEqual([]);
    expect(normalizeDateRange(["not-a-date", new Date("2025-01-02")])).toEqual([]);

    const valid = normalizeDateRange([
      new Date("2025-01-01T00:00:00Z"),
      new Date("2025-01-02T00:00:00Z"),
    ]);
    expect(valid).toHaveLength(2);
    expect(valid[0].toISOString()).toBe("2025-01-01T00:00:00.000Z");
  });
});

describe("Settings missing keys normalization", () => {
  it("handles missing or null config keys without rendering undefined", () => {
    expect(normalizeSettingFieldValue(5, false)).toBe("5");
    expect(normalizeSettingFieldValue(0, false)).toBe("0");
    expect(normalizeSettingFieldValue(undefined, false)).toBe("");
    expect(normalizeSettingFieldValue(null, false)).toBe("");
    expect(normalizeSettingFieldValue(undefined, true)).toBe("0");
    expect(normalizeSettingFieldValue(null, true)).toBe("0");
    expect(normalizeSettingFieldValue(1, true)).toBe("1");
  });
});

describe("homeUtils guards", () => {
  it("guards methodLabel against null, undefined, empty string, and non-integers", () => {
    expect(methodLabel(null)).toBe("—");
    expect(methodLabel(undefined)).toBe("—");
    expect(methodLabel("" as unknown as number)).toBe("—");
    expect(methodLabel(1.5)).toBe("—");
    expect(methodLabel(-1)).toBe("—");
    expect(methodLabel(0)).toBe("增量同步");
    expect(methodLabel(2)).toBe("移动");
  });

  it("falls back to raw cron expression for invalid clock/day/weekday values", () => {
    expect(
      formatSchedulePlan({
        isCron: 1,
        second: "0",
        minute: "99",
        hour: "25",
        day: "*",
        month: "*",
        day_of_week: "*",
      }),
    ).toBe("按 Cron 表达式 0 99 25 * * * 执行");

    expect(
      formatSchedulePlan({
        isCron: 1,
        second: "0",
        minute: "0",
        hour: "2",
        day: "*/2",
        month: "*",
        day_of_week: "*",
      }),
    ).toBe("按 Cron 表达式 0 0 2 */2 * * 执行");

    expect(
      formatSchedulePlan({
        isCron: 1,
        second: "0",
        minute: "0",
        hour: "2",
        day: "*",
        month: "*",
        day_of_week: "*/2",
      }),
    ).toBe("按 Cron 表达式 0 0 2 * * */2 执行");

    expect(
      formatSchedulePlan({
        isCron: 1,
        second: "0",
        minute: "0",
        hour: "2",
        day: "*",
        month: "*",
        day_of_week: "5-1",
      }),
    ).toBe("按 Cron 表达式 0 0 2 * * 5-1 执行");
  });

  it("guards formatNextRun and formatSchedule against missing/invalid values", () => {
    const baseJob = {
      id: 1,
      enable: 1,
      srcPath: "[]",
      dstPath: "[]",
      alistId: 1,
      useCacheT: 0,
      useCacheS: 0,
      method: 0,
      interval: 15,
      isCron: 0,
    } as JobItem;

    expect(formatNextRun(null)).toBe("—");
    expect(formatSchedule(undefined)).toBe("—");
    expect(formatNextRun({ ...baseJob, nextRunTime: Number.NaN })).toBe("每 15 分钟");
    expect(formatNextRun({ ...baseJob, nextRunTime: 1e20 })).toBe("每 15 分钟");
    expect(formatNextRun({ ...baseJob, nextRunTime: 0 })).toBe("每 15 分钟");
  });
});
