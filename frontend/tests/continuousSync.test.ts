import { describe, expect, it } from "vitest";
import { buildJobPayload, defaultJobForm, jobToForm, validateJobForm } from "../src/lib/taskForm";
import { formatNextRun, formatSchedule, formatSchedulePlan } from "../src/pages/Home/homeUtils";
import type { JobForm } from "../src/lib/taskForm";
import type { JobItem } from "../src/types";

describe("continuous synchronization", () => {
  const draft = () => ({
    ...defaultJobForm(7), remark: "持续备份", isCron: 3,
    srcPath: ["/本地/root/photos"], dstPath: ["/Cloud/photos"],
    extraFlags: { ...defaultJobForm(7).extraFlags, syncDirection: "upload", syncPollIntervalSec: 30, syncAuditIntervalMin: 60, watchDebounceSec: 2 },
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

  it("validates polling limits, direction and move compatibility", () => {
    expect(validateJobForm(draft())).toBe("");
    expect(validateJobForm({ ...draft(), extraFlags: { ...draft().extraFlags, syncPollIntervalSec: 9 } })).toContain("10–86400");
    expect(validateJobForm({ ...draft(), extraFlags: { ...draft().extraFlags, syncAuditIntervalMin: 10081 } })).toContain("10080");
    expect(validateJobForm({ ...draft(), extraFlags: { ...draft().extraFlags, syncDirection: "download" } })).toContain("下载");
    expect(validateJobForm({ ...draft(), method: 2 })).toContain("移动任务");
  });

  it("shows a continuous schedule while leaving existing schedules unchanged", () => {
    const job = stored(draft());
    expect(formatSchedulePlan(job)).toContain("持续");
    expect(formatSchedule(job)).toBe("持续同步");
    expect(formatNextRun(job)).toBe("持续同步中");
    expect(formatNextRun({ ...job, enable: 0 })).toBe("持续同步已暂停");
    const legacy = jobToForm({ ...job, isCron: 1, extraFlags: "" });
    expect(buildJobPayload(legacy).isCron).toBe(1);
    expect(legacy.extraFlags?.syncDirection).toBe("");
  });
});
