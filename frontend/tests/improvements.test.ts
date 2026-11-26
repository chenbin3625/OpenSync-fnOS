import { describe, expect, it } from "vitest";
import {
  applyTaskScenarioPreset,
  buildJobPayload,
  defaultJobForm,
  jobToForm,
  taskScenarioPresets,
} from "../src/lib/taskForm";
import { notifyEventOptions } from "../src/lib/notifyForm";
import { applyProgressFrame } from "../src/pages/Home/jobProgressStream";
import type { CurrentTaskData, JobItem } from "../src/types";

describe("scenario presets and extended extraFlags", () => {
  it("exposes all four scenario presets and applies them cleanly", () => {
    expect(taskScenarioPresets.map((p) => p.key)).toEqual([
      "media",
      "small_files",
      "cloud_drive",
      "cold_archive",
    ]);

    const base = defaultJobForm(1);

    const media = applyTaskScenarioPreset(base, "media");
    expect(media.multiThreadStreams).toBe(8);
    expect(media.extraFlags.resumeEnable).toBe(true);
    expect(media.extraFlags.resumeMinSizeMB).toBe(32);
    expect(media.extraFlags.orderBy).toBe("size,descending");

    const small = applyTaskScenarioPreset(base, "small_files");
    expect(small.fastList).toBe(true);
    expect(small.copyConcurrency).toBe(16);
    expect(small.extraFlags.directStream).toBe(true);

    const cloud = applyTaskScenarioPreset(base, "cloud_drive");
    expect(cloud.tpsLimit).toBe(4);
    expect(cloud.extraFlags.lowLevelRetries).toBe(15);
    expect(cloud.extraFlags.retriesSleep).toBe("10s");
    expect(cloud.extraFlags.autoRetryMax).toBe(3);
    expect(cloud.extraFlags.autoRetryDelayMin).toBe(5);

    const archive = applyTaskScenarioPreset(base, "cold_archive");
    expect(archive.compareMode).toBe(2);
    expect(archive.trashEnable).toBe(true);
    expect(archive.extraFlags.checkSchedule).toBe("weekly");
    expect(archive.extraFlags.missedCatchUp).toBe(true);
  });

  it("round-trips all new extraFlags and strips runtime decoration fields on save", () => {
    const form = {
      ...defaultJobForm(2),
      remark: "full-featured",
      srcPath: ["/本地/vol1/photos"],
      dstPath: ["/oss/bucket/photos"],
      extraFlags: {
        ...defaultJobForm(2).extraFlags,
        resumeEnable: true,
        resumeMinSizeMB: 64,
        watchEnable: true,
        watchDebounceSec: 15,
        lowLevelRetries: 12,
        retriesSleep: "5s",
        autoRetryMax: 2,
        autoRetryDelayMin: 10,
        postSyncJobId: 9,
        checkSchedule: "daily",
        missedCatchUp: true,
        directStream: true,
      },
    };
    const payload = buildJobPayload(form);
    expect(payload).not.toHaveProperty("watching");
    expect(payload).not.toHaveProperty("checkRunning");

    const parsedExtra = JSON.parse(String(payload.extraFlags));
    expect(parsedExtra).toMatchObject({
      resumeEnable: true,
      resumeMinSizeMB: 64,
      watchEnable: true,
      watchDebounceSec: 15,
      lowLevelRetries: 12,
      retriesSleep: "5s",
      autoRetryMax: 2,
      autoRetryDelayMin: 10,
      postSyncJobId: 9,
      checkSchedule: "daily",
      missedCatchUp: true,
      directStream: true,
    });

    const job: JobItem = {
      ...(payload as unknown as JobItem),
      id: 42,
      watching: true,
      checkRunning: true,
    };
    const roundTripped = jobToForm(job);
    expect(roundTripped.extraFlags).toMatchObject({
      resumeEnable: true,
      resumeMinSizeMB: 64,
      watchEnable: true,
      watchDebounceSec: 15,
      lowLevelRetries: 12,
      retriesSleep: "5s",
      autoRetryMax: 2,
      autoRetryDelayMin: 10,
      postSyncJobId: 9,
      checkSchedule: "daily",
      missedCatchUp: true,
      directStream: true,
    });
    const rebuilt = buildJobPayload(roundTripped);
    expect(rebuilt).not.toHaveProperty("watching");
    expect(rebuilt).not.toHaveProperty("checkRunning");
  });
});

describe("realtime progress live controls & per-file metrics", () => {
  it("preserves paused, liveBwLimit, doneBytes and speed across full and delta SSE frames", () => {
    const initial: CurrentTaskData = {
      taskId: 100,
      scanFinish: true,
      createTime: 1_700_000_000,
      duration: 20,
      num: { wait: 1, running: 1, success: 0, fail: 0, other: 0 },
      size: { wait: 1024, running: 2048, success: 0, fail: 0, other: 0 },
      doneSize: 512,
      remainSize: 2560,
      speed: 256,
      paused: false,
      liveBwLimit: "20M",
      doingTask: [
        {
          id: 1,
          fileName: "video.mp4",
          fileSize: 2048,
          doneBytes: 512,
          speed: 256,
          status: 1,
          progress: 25,
        },
      ],
    };

    const next = applyProgressFrame(initial, {
      taskId: 100,
      createTime: 1_700_000_000,
      scanFinish: true,
      duration: 25,
      num: { wait: 1, running: 1, success: 0, fail: 0, other: 0 },
      size: { wait: 1024, running: 2048, success: 0, fail: 0, other: 0 },
      doneSize: 1024,
      remainSize: 2048,
      speed: 0,
      paused: true,
      liveBwLimit: "10M",
      doingTask: null,
      doingPatch: [
        {
          id: 1,
          fileName: "video.mp4",
          doneBytes: 1024,
          speed: 0,
          progress: 50,
          status: 1,
        },
      ],
    });

    expect(next?.paused).toBe(true);
    expect(next?.liveBwLimit).toBe("10M");
    expect(next?.doingTask?.[0]).toMatchObject({
      fileName: "video.mp4",
      doneBytes: 1024,
      speed: 0,
      progress: 50,
    });
  });
});

describe("notification events", () => {
  it("includes check_alert and digest event options", () => {
    const values = notifyEventOptions.map((o) => o.value);
    expect(values).toContain("check_alert");
    expect(values).toContain("digest");
  });
});
