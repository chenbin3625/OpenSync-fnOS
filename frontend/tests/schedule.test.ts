import { describe, expect, it } from "vitest";
import {
  applyCronPresetMode,
  computeNextCronRuns,
  detectCronPresetMode,
  formatCronRunTime,
  formatIntervalHuman,
  formatWeekdayCronExpression,
  intervalInputToMinutes,
  parseWeekdaySelection,
  splitIntervalMinutes,
  validateCronField,
  validateCronFields,
} from "../src/lib/schedule";

describe("schedule preset, interval and cron helpers", () => {
  it("detects and applies daily, weekly, monthly, and custom cron modes", () => {
    expect(
      detectCronPresetMode({
        second: "0",
        minute: "30",
        hour: "2",
        day: "*",
        month: "*",
        day_of_week: "*",
      }),
    ).toBe("daily");

    expect(
      detectCronPresetMode({
        second: "0",
        minute: "0",
        hour: "3",
        day: "*",
        month: "*",
        day_of_week: "1-5",
      }),
    ).toBe("weekly");

    expect(
      detectCronPresetMode({
        second: "0",
        minute: "15",
        hour: "4",
        day: "15",
        month: "*",
        day_of_week: "*",
      }),
    ).toBe("monthly");

    expect(
      detectCronPresetMode({
        second: "0",
        minute: "*/15",
        hour: "*",
        day: "*",
        month: "*",
        day_of_week: "*",
      }),
    ).toBe("custom");

    expect(
      applyCronPresetMode({ hour: "5", minute: "20" }, "weekly"),
    ).toEqual({
      second: "0",
      minute: "20",
      hour: "5",
      day: "*",
      month: "*",
      day_of_week: "0",
    });

    expect(
      applyCronPresetMode({ hour: "5", minute: "20", day: "10" }, "monthly"),
    ).toEqual({
      second: "0",
      minute: "20",
      hour: "5",
      day: "10",
      month: "*",
      day_of_week: "*",
    });
  });

  it("parses and compacts weekday cron expressions", () => {
    expect(parseWeekdaySelection("1-5")).toEqual([1, 2, 3, 4, 5]);
    expect(parseWeekdaySelection("0,6")).toEqual([0, 6]);
    expect(parseWeekdaySelection("*")).toBeNull();
    expect(parseWeekdaySelection("5-1")).toBeNull();

    expect(formatWeekdayCronExpression([1, 2, 3, 4, 5])).toBe("1-5");
    expect(formatWeekdayCronExpression([0, 2, 3, 6])).toBe("0,2-3,6");
    expect(formatWeekdayCronExpression([0, 1, 2, 3, 4, 5, 6])).toBe("*");
  });

  it("splits and converts interval units cleanly", () => {
    expect(splitIntervalMinutes(1440)).toEqual({ value: 1, unit: "day" });
    expect(splitIntervalMinutes(2880)).toEqual({ value: 2, unit: "day" });
    expect(splitIntervalMinutes(180)).toEqual({ value: 3, unit: "hour" });
    expect(splitIntervalMinutes(45)).toEqual({ value: 45, unit: "minute" });

    expect(intervalInputToMinutes("6", "hour")).toBe(360);
    expect(intervalInputToMinutes("2", "day")).toBe(2880);
    expect(intervalInputToMinutes("1.5", "minute")).toBe(1.5);
    expect(intervalInputToMinutes("1.5", "hour")).toBe(1.5);
    expect(intervalInputToMinutes("abc", "minute")).toBe(0);

    expect(formatIntervalHuman(1440)).toBe("每 1 天（1440 分钟）执行一次");
    expect(formatIntervalHuman(120)).toBe("每 2 小时（120 分钟）执行一次");
    expect(formatIntervalHuman(30)).toBe("每 30 分钟执行一次");
  });

  it("validates cron fields and computes upcoming execution times", () => {
    expect(validateCronField("hour", "25")).toContain("时");
    expect(validateCronField("day_of_week", "7")).toContain("周");
    expect(validateCronField("minute", "*/15")).toBe("");
    expect(
      validateCronFields({
        second: "0",
        minute: "0",
        hour: "2",
        day: "*",
        month: "*",
        day_of_week: "*",
      }),
    ).toEqual({});

    const now = new Date(2026, 4, 10, 1, 30, 0); // 2026-05-10 01:30:00 (Sunday)
    const dailyRuns = computeNextCronRuns(
      {
        second: "0",
        minute: "0",
        hour: "2",
        day: "*",
        month: "*",
        day_of_week: "*",
      },
      2,
      now,
    );
    expect(dailyRuns.map(formatCronRunTime)).toEqual([
      "2026-05-10 02:00:00",
      "2026-05-11 02:00:00",
    ]);

    const weekdayRuns = computeNextCronRuns(
      {
        second: "0",
        minute: "0",
        hour: "9",
        day: "*",
        month: "*",
        day_of_week: "1-5",
      },
      2,
      now,
    );
    expect(weekdayRuns.map(formatCronRunTime)).toEqual([
      "2026-05-11 09:00:00",
      "2026-05-12 09:00:00",
    ]);

    const impossibleRuns = computeNextCronRuns(
      {
        second: "0",
        minute: "0",
        hour: "2",
        day: "31",
        month: "2",
        day_of_week: "*",
      },
      2,
      now,
    );
    expect(impossibleRuns).toEqual([]);

    // Step syntax with explicit start value (e.g. 5/15 -> 5, 20, 35, 50)
    expect(validateCronField("minute", "5/15")).toBe("");
    const stepFromStartRuns = computeNextCronRuns(
      {
        second: "0",
        minute: "5/15",
        hour: "*",
        day: "*",
        month: "*",
        day_of_week: "*",
      },
      4,
      new Date(2026, 4, 10, 1, 0, 0),
    );
    expect(stepFromStartRuns.map(formatCronRunTime)).toEqual([
      "2026-05-10 01:05:00",
      "2026-05-10 01:20:00",
      "2026-05-10 01:35:00",
      "2026-05-10 01:50:00",
    ]);

    const hourStepRuns = computeNextCronRuns(
      {
        second: "0",
        minute: "0",
        hour: "2/6",
        day: "*",
        month: "*",
        day_of_week: "*",
      },
      4,
      new Date(2026, 4, 10, 0, 0, 0),
    );
    expect(hourStepRuns.map(formatCronRunTime)).toEqual([
      "2026-05-10 02:00:00",
      "2026-05-10 08:00:00",
      "2026-05-10 14:00:00",
      "2026-05-10 20:00:00",
    ]);
  });
});
