import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import {
  calcRealtimeProgress,
  mergeTaskItemPatches,
  normalizeTaskItemPage,
  taskProgressPercent,
} from "../src/pages/Home/taskRows";

describe("realtime task rows", () => {
  it("formats running progress as an integer percent", () => {
    expect(taskProgressPercent(66.6)).toBe(67);
    expect(taskProgressPercent("23.2")).toBe(23);
    expect(taskProgressPercent(-12)).toBe(0);
    expect(taskProgressPercent(120)).toBe(100);
  });

  it("rejects realtime detail responses without complete query identity", () => {
    expect(
      normalizeTaskItemPage(
        {
          dataList: [{ id: 1, fileName: "stale.txt", status: 1, type: 0 }],
          count: 1,
        },
        { taskId: 27, createTime: 100, status: 1, pageNum: 1, pageSize: 10 },
      ),
    ).toEqual({ rows: [], total: 0 });
  });

  it("rejects legacy array responses for an identified realtime query", () => {
    expect(
      normalizeTaskItemPage(
        [{ id: 1, fileName: "legacy-stale.txt", status: 1, type: 0 }],
        { taskId: 27, createTime: 100, status: 1, pageNum: 1, pageSize: 10 },
      ),
    ).toEqual({ rows: [], total: 0 });
  });

  it("rejects realtime detail responses for another status tab", () => {
    expect(
      normalizeTaskItemPage(
        {
          taskId: 27,
          createTime: 100,
          status: 2,
          pageNum: 1,
          pageSize: 10,
          stale: false,
          dataList: [{ id: 1, fileName: "success.txt", status: 2, type: 0 }],
          count: 1,
        },
        { taskId: 27, createTime: 100, status: 1, pageNum: 1, pageSize: 10 },
      ),
    ).toEqual({ rows: [], total: 0 });
  });

  it("filters rows that do not belong to the requested realtime status", () => {
    expect(
      normalizeTaskItemPage(
        {
          taskId: 27,
          createTime: 100,
          status: 2,
          pageNum: 1,
          pageSize: 10,
          stale: false,
          dataList: [
            { id: 1, fileName: "success.txt", status: 2, type: 0 },
            { id: 2, fileName: "running.txt", status: 1, type: 0 },
          ],
          count: 2,
        },
        { taskId: 27, createTime: 100, status: 2, pageNum: 1, pageSize: 10 },
      ),
    ).toEqual({
      rows: [{ id: 1, fileName: "success.txt", status: 2, type: 0 }],
      total: 1,
    });
  });

  it("keeps only grouped statuses in the realtime other tab", () => {
    expect(
      normalizeTaskItemPage(
        {
          taskId: 27,
          createTime: 100,
          status: -1,
          pageNum: 1,
          pageSize: 10,
          stale: false,
          dataList: [
            { id: 1, fileName: "stopped.txt", status: 4, type: 0 },
            { id: 2, fileName: "failed.txt", status: 7, type: 0 },
          ],
          count: 2,
        },
        { taskId: 27, createTime: 100, status: -1, pageNum: 1, pageSize: 10 },
      ),
    ).toEqual({
      rows: [{ id: 1, fileName: "stopped.txt", status: 4, type: 0 }],
      total: 1,
    });
  });

  it("stacks the progress percent above the progress bar", () => {
    const css = readFileSync(
      new URL("../src/styles.css", import.meta.url),
      "utf8",
    );
    const rule = /\.file-progress\s*\{(?<body>[^}]+)\}/.exec(css)?.groups
      ?.body;

    expect(rule).toContain("flex-direction: column");
  });
});

describe("realtime patch frames", () => {
  it("updates a doing row by id", () => {
    expect(
      mergeTaskItemPatches(
        [
          { id: 1, fileName: "a.bin", status: 1, progress: 10 },
          { id: 2, fileName: "b.bin", status: 1, progress: 20 },
        ],
        [{ id: 1, status: 1, progress: 80 }],
      ),
    ).toEqual([
      { id: 1, fileName: "a.bin", status: 1, progress: 80 },
      { id: 2, fileName: "b.bin", status: 1, progress: 20 },
    ]);
  });

  it("updates a doing row by alistTaskId when the local id is absent", () => {
    expect(
      mergeTaskItemPatches(
        [{ alistTaskId: "remote-1", fileName: "a.bin", status: 1, progress: 5 }],
        [{ alistTaskId: "remote-1", status: 1, progress: 55 }],
      ),
    ).toEqual([
      {
        alistTaskId: "remote-1",
        fileName: "a.bin",
        status: 1,
        progress: 55,
      },
    ]);
  });

  it("updates a path-keyed doing row and appends unmatched patches", () => {
    const merged = mergeTaskItemPatches(
      [
        {
          fileName: "dir",
          srcPath: "/a/dir",
          dstPath: "/b/dir",
          status: 1,
          progress: 1,
        },
      ],
      [
        {
          fileName: "dir",
          srcPath: "/a/dir",
          dstPath: "/b/dir",
          status: 1,
          progress: 99,
        },
        { id: 7, fileName: "new.bin", srcPath: "/a", dstPath: "/b", status: 1, progress: 3 },
      ],
    );
    expect(merged).toHaveLength(2);
    expect(merged[0].progress).toBe(99);
    expect(merged[1]).toMatchObject({ id: 7, progress: 3 });
  });

  it("ignores empty patch frames", () => {
    const base = [{ id: 1, status: 1, progress: 10 }];
    expect(mergeTaskItemPatches(base, [])).toBe(base);
    expect(mergeTaskItemPatches(base, undefined)).toBe(base);
  });
});

describe("realtime progress calculation", () => {
  const base: Parameters<typeof calcRealtimeProgress>[0] = {
    taskId: 1,
    scanFinish: true,
    createTime: 100,
    duration: 10,
    num: { wait: 0, running: 1, success: 0, fail: 0, other: 0 },
    size: { wait: 0, running: 100, success: 0, fail: 0, other: 0 },
    doneSize: 50,
    remainSize: 50,
    speed: 5,
    speedAvg: 4,
    remainTime: 12,
  };

  it("prefers server-provided numbers", () => {
    expect(calcRealtimeProgress(base, null)).toEqual({
      doneSize: 50,
      remainSize: 50,
      speed: 5,
      speedAvg: 4,
      remainTime: 12,
    });
  });

  it("derives speed from the previous snapshot when the server omits it", () => {
    const current = { ...base, speed: undefined, duration: 20, doneSize: 150 };
    const previous = { duration: 10, doneSize: 50 };
    expect(calcRealtimeProgress(current, previous).speed).toBe(10);
  });

  it("never reports a negative speed when the clock or counters regress", () => {
    const regressed = { ...base, speed: undefined, duration: 5, doneSize: 10 };
    expect(calcRealtimeProgress(regressed, { duration: 20, doneSize: 100 }).speed).toBe(
      0,
    );
  });

  it("falls back to size maps when the server payload is legacy", () => {
    const legacy = {
      ...base,
      doneSize: undefined,
      remainSize: undefined,
      speed: undefined,
      speedAvg: undefined,
      remainTime: undefined,
      doingTask: [{ id: 1, fileSize: 200, progress: 50, status: 1 }],
      size: { wait: 30, running: 200, success: 40, fail: 0, other: 0 },
      firstSync: undefined,
    } as unknown as Parameters<typeof calcRealtimeProgress>[0];
    const result = calcRealtimeProgress(legacy, null);
    expect(result.doneSize).toBe(140);
    expect(result.remainSize).toBe(130);
    expect(result.remainTime).toBe(0);
  });
});
