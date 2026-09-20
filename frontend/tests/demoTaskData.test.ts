import { describe, expect, it } from "vitest";
import {
  createDemoTaskItems,
  createDemoTaskRecords,
  createDemoTaskView,
  pageDemoRows,
} from "../src/pages/Home/demoTaskData";

describe("demo task data", () => {
  it("provides at least 100 realtime rows for every task item group", () => {
    const items = createDemoTaskItems();
    const groups = {
      wait: items.filter((item) => item.status === 0),
      running: items.filter((item) => item.status === 1),
      success: items.filter((item) => item.status === 2),
      fail: items.filter((item) => item.status === 7),
      other: items.filter((item) => ![0, 1, 2, 7].includes(item.status)),
    };

    for (const [name, rows] of Object.entries(groups)) {
      expect(rows.length, name).toBeGreaterThanOrEqual(100);
    }
  });

  it("keeps demo task counters aligned with generated realtime rows", () => {
    const items = createDemoTaskItems();
    const task = createDemoTaskView(1_800_000_000, items);

    expect(task.num).toEqual({
      wait: items.filter((item) => item.status === 0).length,
      running: items.filter((item) => item.status === 1).length,
      success: items.filter((item) => item.status === 2).length,
      fail: items.filter((item) => item.status === 7).length,
      other: items.filter((item) => ![0, 1, 2, 7].includes(item.status)).length,
    });
    expect(task.doingTask).toHaveLength(task.num.running);
  });

  it("provides at least 100 history records", () => {
    expect(createDemoTaskRecords(1_800_000_000)).toHaveLength(120);
  });

  it("slices demo rows by page and page size", () => {
    const records = createDemoTaskRecords(1_800_000_000);

    expect(pageDemoRows(records, 2, 20).map((record) => record.id)).toEqual(
      records.slice(20, 40).map((record) => record.id),
    );
    expect(pageDemoRows(records, 1, 50)).toHaveLength(50);
  });
});
