import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import {
  pageTaskItems,
  realtimeRunningSnapshotIsComplete,
  taskProgressPercent,
} from "../src/pages/Home/taskRows";

describe("realtime task rows", () => {
  it("detects when running rows must be fetched because the live snapshot is missing", () => {
    expect(
      realtimeRunningSnapshotIsComplete({
        num: { wait: 0, running: 2, success: 0, fail: 0, other: 0 },
        doingTask: [],
      }),
    ).toBe(false);
    expect(
      realtimeRunningSnapshotIsComplete({
        num: { wait: 0, running: 1, success: 0, fail: 0, other: 0 },
        doingTask: [{ id: 1, fileName: "a.txt", status: 1, type: 0 }],
      }),
    ).toBe(true);
  });

  it("keeps the realtime running list complete instead of slicing by page", () => {
    const rows = [
      { id: 1, fileName: "a.txt", status: 1, type: 0 },
      { id: 2, fileName: "b.txt", status: 1, type: 0 },
      { id: 3, fileName: "c.txt", status: 1, type: 0 },
    ];

    expect(pageTaskItems(rows, 1, 2, 2)).toEqual(rows);
  });

  it("formats running progress as an integer percent", () => {
    expect(taskProgressPercent(66.6)).toBe(67);
    expect(taskProgressPercent("23.2")).toBe(23);
    expect(taskProgressPercent(-12)).toBe(0);
    expect(taskProgressPercent(120)).toBe(100);
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
