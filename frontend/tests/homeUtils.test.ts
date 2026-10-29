import { describe, expect, it } from "vitest";
import {
  formatDuration,
  formatSchedule,
  formatSchedulePlan,
  methodLabel,
  taskItemStatusNames,
} from "../src/pages/Home/homeUtils";
import type { JobItem } from "../src/types";

function job(overrides: Partial<JobItem>): JobItem {
  return {
    id: 1,
    enable: 1,
    srcPath: "[]",
    dstPath: "[]",
    alistId: 1,
    useCacheT: 0,
    useCacheS: 0,
    method: 0,
    interval: 0,
    isCron: 1,
    second: "0",
    minute: "0",
    hour: "2",
    day: "*",
    month: "*",
    day_of_week: "*",
    ...overrides,
  } as JobItem;
}

describe("task item status labels", () => {
  it("matches the backend taskStatus enum", () => {
    // backend/internal/service/job_task_item.go: 3 partial fail, 4 stopped,
    // 5 timeout, 6 system failed, 10 retrying. There is no 11.
    expect(taskItemStatusNames).toEqual({
      0: "等待中",
      1: "进行中",
      2: "成功",
      3: "部分失败",
      4: "已停止",
      5: "超时",
      6: "系统错误",
      7: "已失败",
      10: "重试中",
    });
    expect(taskItemStatusNames[11]).toBeUndefined();
    expect(taskItemStatusNames[5]).not.toContain("重试");
  });
});

describe("schedule display", () => {
  it("renders a plain daily cron expression as a clock", () => {
    expect(
      formatSchedulePlan({
        isCron: 1,
        second: "0",
        minute: "0",
        hour: "2",
        day: "*",
        month: "*",
        day_of_week: "*",
      }),
    ).toBe("每天 02:00:00 执行");
  });

  it("falls back to the raw expression instead of faking a time for step fields", () => {
    const plan = formatSchedulePlan({
      isCron: 1,
      second: "0",
      minute: "*/5",
      hour: "2",
      day: "*",
      month: "*",
      day_of_week: "*",
    });
    expect(plan).toBe("按 Cron 表达式 0 */5 2 * * * 执行");
    expect(plan).not.toContain("2:*/5");
  });

  it("falls back for range fields too", () => {
    const plan = formatSchedulePlan({
      isCron: 1,
      second: "0",
      minute: "10-20",
      hour: "3",
      day: "*",
      month: "*",
      day_of_week: "*",
    });
    expect(plan).toContain("按 Cron 表达式");
    expect(plan).not.toContain("3:10-20");
  });

  it("keeps weekly schedules readable with day-of-week ranges", () => {
    expect(
      formatSchedulePlan({
        isCron: 1,
        second: "0",
        minute: "30",
        hour: "9",
        day: "*",
        month: "*",
        day_of_week: "1-5",
      }),
    ).toBe("每周 周一至周五 的 09:30:00 执行");
  });

  it("renders interval and manual schedules", () => {
    expect(formatSchedule(job({ isCron: 0, interval: 15 }))).toBe("每 15 分钟");
    expect(formatSchedule(job({ isCron: 2 }))).toBe("仅手动触发");
    expect(formatSchedulePlan({ isCron: 2 })).toBe(
      "不自动执行，只能手动触发",
    );
  });
});

describe("sync method labels", () => {
  it("labels all supported synchronization modes", () => {
    expect(methodLabel(0)).toBe("增量同步");
    expect(methodLabel(1)).toBe("全量同步");
    expect(methodLabel(2)).toBe("移动");
    expect(methodLabel(9)).toBe("—");
  });
});

describe("duration formatting", () => {
  it("formats the latest execution duration the overview shows", () => {
    expect(formatDuration(0)).toBe("0秒");
    expect(formatDuration(65)).toBe("1分 5秒");
    expect(formatDuration(3661)).toBe("1小时 1分 1秒");
    expect(formatDuration(-5)).toBe("0秒");
  });
});
