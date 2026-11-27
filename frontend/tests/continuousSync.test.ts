import { describe, expect, it } from "vitest";
import { buildJobPayload, defaultJobForm, jobToForm, validateJobForm } from "../src/lib/taskForm";
import { formatNextRun, formatSchedule, formatSchedulePlan } from "../src/pages/Home/homeUtils";
import {
  formatContinuousSyncPlan,
  formatRelativeSyncTime,
  getContinuousSyncMeta,
  isSyncStatusError,
  jobStatusDotClass,
  parseJobExtraFlagsObject,
  resolveEffectiveSyncStatus,
} from "../src/lib/continuousSync";
import type { JobForm } from "../src/lib/taskForm";
import type { JobItem } from "../src/types";

describe("continuous synchronization", () => {
  const draft = () => ({
    ...defaultJobForm(7), remark: "持续备份", isCron: 3,
    srcPath: ["/本地/root/photos"], dstPath: ["/Cloud/photos"],
    extraFlags: { ...defaultJobForm(7).extraFlags, syncPollIntervalSec: 30, syncAuditIntervalMin: 60, watchDebounceSec: 2 },
  });
  const stored = (form: JobForm): JobItem => {
    const p = buildJobPayload(form);
    return {
      id: 4, alistId: 7, enable: p.enable, remark: p.remark,
      method: p.method, isCron: p.isCron, interval: p.interval,
      useCacheS: p.useCacheS, useCacheT: p.useCacheT,
      srcPath: JSON.stringify(p.srcPath), dstPath: JSON.stringify(p.dstPath), extraFlags: p.extraFlags,
    };
  };

  it("round trips settings and paused state without changing existing job paths", () => {
    const form = { ...draft(), enable: false };
    const payload = buildJobPayload(form);
    expect(payload.enable).toBe(0);
    expect(payload.isCron).toBe(3);
    const restored = jobToForm(stored(form));
    expect(restored.extraFlags).toMatchObject(form.extraFlags);
    expect(restored.srcPath).toEqual(form.srcPath);
    expect(restored.dstPath).toEqual(form.dstPath);
    expect(restored.enable).toBe(false);
    expect(validateJobForm(restored)).toBe("");
  });

  it("keeps runtime checkpoints out of saved configuration", () => {
    const form = jobToForm({ ...stored(draft()),
      syncStatus: { state: "synced", pendingChanges: 0, lastSuccess: 100, lastPoll: 90, nextPoll: 120, error: "", failures: 0 },
    });
    expect(buildJobPayload(form)).not.toHaveProperty("syncStatus");
  });

  it("validates polling and debounce limits and rejects move mode", () => {
    expect(validateJobForm(draft())).toBe("");
    expect(validateJobForm({ ...draft(), extraFlags: { ...draft().extraFlags, syncPollIntervalSec: 9 } })).toContain("10–86400");
    expect(validateJobForm({ ...draft(), extraFlags: { ...draft().extraFlags, syncAuditIntervalMin: 10081 } })).toContain("10080");
    expect(validateJobForm({ ...draft(), extraFlags: { ...draft().extraFlags, watchDebounceSec: 1 } })).toContain("2–3600");
    expect(validateJobForm({ ...draft(), method: 2 })).toContain("持续同步");
  });

  it("defaults to continuous sync and preserves existing schedules", () => {
    const job = stored(draft());
    expect(formatSchedulePlan(job)).toContain("持续");
    expect(formatSchedule(job)).toBe("持续同步");
    expect(formatNextRun(job)).toBe("持续同步中");
    expect(formatNextRun({ ...job, enable: 0 })).toBe("持续同步已暂停");
    const scheduled = jobToForm({ ...job, isCron: 1, extraFlags: "" });
    expect(buildJobPayload(scheduled).isCron).toBe(1);
  });

  it.each(["upload", "download", "custom"])("opens an existing %s task as one-way sync and allows changing its endpoints", (syncDirection) => {
    const job = stored(draft());
    if (syncDirection === "download") [job.srcPath, job.dstPath] = [job.dstPath, job.srcPath];
    job.extraFlags = JSON.stringify({ ...parseJobExtraFlagsObject(job.extraFlags), syncDirection });

    expect(getContinuousSyncMeta(job).directionKey).toBe("oneway");
    const form = jobToForm(job);
    const saved = buildJobPayload(form);
    expect(saved.srcPath).toEqual(JSON.parse(job.srcPath));
    expect(saved.dstPath).toEqual(JSON.parse(job.dstPath));
    expect(JSON.parse(saved.extraFlags)).not.toHaveProperty("syncDirection");

    const localCopy = { ...form, srcPath: ["/本地/root/photos"], dstPath: ["/本地/root/backup"] };
    expect(validateJobForm(localCopy)).toBe("");
  });

  it("derives Cloud Sync metadata, readable cadence plan, relative sync time and error status", () => {
    const uploadMeta = getContinuousSyncMeta({
      method: 1,
      extraFlags: JSON.stringify({ syncDirection: "upload", syncPollIntervalSec: 300, syncAuditIntervalMin: 360, watchDebounceSec: 15 }),
    });
    expect(uploadMeta).toMatchObject({
      directionKey: "oneway",
      directionLabel: "单向同步",
      arrow: "→",
      syncDelete: true,
      pollIntervalSec: 300,
      auditIntervalMin: 360,
      debounceSec: 15,
    });
    expect(formatContinuousSyncPlan(uploadMeta)).toBe(
      "本地实时监听 · 远端每 5 分钟 检测 · 静默 15 秒 触发 · 每 6 小时完整核对",
    );

    const biMeta = getContinuousSyncMeta({ method: 3, extraFlags: "" });
    expect(biMeta.directionKey).toBe("bidirectional");
    expect(biMeta.arrow).toBe("⇄");

    const nowMs = 1_700_000_000_000;
    expect(formatRelativeSyncTime(1_700_000_000 - 5, nowMs)).toBe("刚刚");
    expect(formatRelativeSyncTime(1_700_000_000 - 180, nowMs)).toBe("3 分钟前");
    expect(formatRelativeSyncTime(1_700_000_000 - 7200, nowMs)).toBe("2 小时前");

    expect(isSyncStatusError({ state: "error", pendingChanges: 0, lastSuccess: 0, lastPoll: 0, nextPoll: 0, error: "boom", failures: 2 })).toBe(true);
    expect(isSyncStatusError({ state: "synced", pendingChanges: 0, lastSuccess: 100, lastPoll: 100, nextPoll: 160, error: "", failures: 0 })).toBe(false);

    expect(resolveEffectiveSyncStatus({ enable: 0 }).state).toBe("paused");
    expect(resolveEffectiveSyncStatus({ enable: 1, running: true }).state).toBe("syncing");
    expect(resolveEffectiveSyncStatus({ enable: 1 }).state).toBe("monitoring");

    const baseJob = stored(draft());
    expect(jobStatusDotClass({ ...baseJob, legacyOpenList: true })).toBe("job-dot job-dot--error");
    expect(jobStatusDotClass({ ...baseJob, running: true, paused: true })).toBe("job-dot job-dot--paused");
    expect(jobStatusDotClass({ ...baseJob, running: true })).toBe("job-dot job-dot--running");
    expect(jobStatusDotClass({ ...baseJob, queued: true })).toBe("job-dot job-dot--queued");
    expect(jobStatusDotClass({ ...baseJob, enable: 0 })).toBe("job-dot job-dot--disabled");
    expect(
      jobStatusDotClass({
        ...baseJob,
        syncStatus: {
          state: "error",
          pendingChanges: 0,
          lastSuccess: 0,
          lastPoll: 0,
          nextPoll: 0,
          error: "boom",
          failures: 1,
        },
      }),
    ).toBe("job-dot job-dot--error");
    expect(
      jobStatusDotClass({
        ...baseJob,
        syncStatus: {
          state: "waiting_window",
          pendingChanges: 0,
          lastSuccess: 0,
          lastPoll: 0,
          nextPoll: 0,
          error: "",
          failures: 0,
        },
      }),
    ).toBe("job-dot job-dot--paused");
    expect(jobStatusDotClass(baseJob)).toBe("job-dot job-dot--synced");
  });
});
