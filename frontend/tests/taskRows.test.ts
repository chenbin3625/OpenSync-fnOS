import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import {
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
