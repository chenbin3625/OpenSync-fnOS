import { describe, expect, it } from "vitest";
import {
  createBwRateValue,
  createDefaultScheduleRule,
  describeBwLimit,
  detectBwModelIssues,
  estimateMbps,
  formatBwLimitSummary,
  formatBwRateCompact,
  formatBwRateHuman,
  formatBwTimelineSegLabel,
  getDayTimelineSegments,
  hasDifferentWeekdays,
  parseBwLimit,
  resolveBwLimitAt,
  serializeBwLimit,
  validateBwLimitExpression,
} from "../src/lib/bwLimit";

describe("bwLimit validation", () => {
  it("accepts standard rclone rates, asymmetric limits, and daily/weekly timetables", () => {
    for (const expr of [
      "",
      "off",
      "0",
      "10M",
      "500K",
      "1.5G",
      "10Mi",
      "10MiB",
      "5M:20M",
      "5M:off",
      "off:20M",
      "08:00,5M 23:00,off",
      "08:00,10M 19:00,2M 23:00,off",
      "Mon-09:00,5M Mon-18:00,off Fri-09:00,5M Fri-18:00,off",
    ]) {
      expect(validateBwLimitExpression(expr), expr).toBeNull();
    }
  });

  it("rejects invalid syntax including rclone-unsupported MB/KB suffixes", () => {
    expect(validateBwLimitExpression("10MB")).toContain("不要附加 B 后缀");
    expect(validateBwLimitExpression("500KB")).toContain("不要附加 B 后缀");
    expect(validateBwLimitExpression("08:00,10MB 23:00,off")).toContain("不要附加 B 后缀");
    expect(validateBwLimitExpression("not-a-speed")).not.toBeNull();
    expect(validateBwLimitExpression("-10M")).not.toBeNull();
    expect(validateBwLimitExpression("25:00,5M")).not.toBeNull();
    expect(validateBwLimitExpression("Foo-08:00,5M")).not.toBeNull();
  });
});

describe("bwLimit visual model parse & serialize round-trip", () => {
  it("distinguishes inherit vs explicit unlimited in TaskEditor mode", () => {
    const inheritModel = parseBwLimit("", { allowInherit: true });
    expect(inheritModel.mode).toBe("inherit");
    expect(serializeBwLimit(inheritModel, { allowInherit: true })).toBe("");

    const unlimitedOverride = parseBwLimit("off", { allowInherit: true });
    expect(unlimitedOverride.mode).toBe("unlimited");
    expect(serializeBwLimit(unlimitedOverride, { allowInherit: true })).toBe("off");

    const globalUnlimited = parseBwLimit("", { allowInherit: false });
    expect(globalUnlimited.mode).toBe("unlimited");
    expect(serializeBwLimit(globalUnlimited, { allowInherit: false })).toBe("");
  });

  it("round-trips fixed symmetric and asymmetric upload:download limits", () => {
    const fixed = parseBwLimit("20M");
    expect(fixed.mode).toBe("fixed");
    expect(fixed.fixed).toMatchObject({
      unlimited: false,
      asymmetric: false,
      value: "20",
      unit: "M",
    });
    expect(serializeBwLimit(fixed)).toBe("20M");

    const asym = parseBwLimit("5M:20M");
    expect(asym.mode).toBe("fixed");
    expect(asym.fixed).toMatchObject({
      asymmetric: true,
      uploadUnlimited: false,
      uploadValue: "5",
      uploadUnit: "M",
      downloadUnlimited: false,
      downloadValue: "20",
      downloadUnit: "M",
    });
    expect(serializeBwLimit(asym)).toBe("5M:20M");

    const upOnly = parseBwLimit("5M:off");
    expect(upOnly.mode).toBe("fixed");
    expect(upOnly.fixed.uploadUnlimited).toBe(false);
    expect(upOnly.fixed.downloadUnlimited).toBe(true);
    expect(serializeBwLimit(upOnly)).toBe("5M:off");
  });

  it("round-trips daily multi-slot and cross-midnight schedules losslessly", () => {
    const daily = parseBwLimit("08:00,5M 23:00,off");
    expect(daily.mode).toBe("schedule");
    expect(daily.defaultRate.unlimited).toBe(true);
    expect(daily.rules).toHaveLength(1);
    expect(daily.rules[0]).toMatchObject({
      days: [0, 1, 2, 3, 4, 5, 6],
      startTime: "08:00",
      endTime: "23:00",
    });
    expect(serializeBwLimit(daily)).toBe("08:00,5M 23:00,off");

    const tiered = parseBwLimit("08:00,10M 19:00,2M 23:00,off");
    expect(tiered.mode).toBe("schedule");
    expect(tiered.rules).toHaveLength(2);
    expect(serializeBwLimit(tiered)).toBe("08:00,10M 19:00,2M 23:00,off");

    // Cross-midnight rule: 22:00-06:00 = 2M, rest unlimited
    const crossMidnightModel = {
      mode: "schedule" as const,
      fixed: createBwRateValue(),
      defaultRate: createBwRateValue({ unlimited: true }),
      rules: [
        createDefaultScheduleRule({
          days: [0, 1, 2, 3, 4, 5, 6],
          startTime: "22:00",
          endTime: "06:00",
          rate: createBwRateValue({ value: "2", unit: "M" }),
        }),
      ],
      raw: "",
    };
    const expr = serializeBwLimit(crossMidnightModel);
    expect(expr).toBe("06:00,off 22:00,2M");
    const reparsed = parseBwLimit(expr!);
    expect(reparsed.mode).toBe("schedule");
    expect(reparsed.rules[0]).toMatchObject({
      startTime: "22:00",
      endTime: "06:00",
    });
    expect(serializeBwLimit(reparsed)).toBe("06:00,off 22:00,2M");
  });

  it("compiles and parses weekday-specific schedules cleanly", () => {
    const weekdayExpr =
      "Mon-09:00,5M Mon-18:00,off Tue-09:00,5M Tue-18:00,off Wed-09:00,5M Wed-18:00,off Thu-09:00,5M Thu-18:00,off Fri-09:00,5M Fri-18:00,off";
    const parsed = parseBwLimit(weekdayExpr);
    expect(parsed.mode).toBe("schedule");
    expect(parsed.rules).toHaveLength(1);
    expect(parsed.rules[0].days).toEqual([1, 2, 3, 4, 5]);
    expect(parsed.rules[0].startTime).toBe("09:00");
    expect(parsed.rules[0].endTime).toBe("18:00");
    expect(serializeBwLimit(parsed)).toBe(weekdayExpr);
    expect(hasDifferentWeekdays(weekdayExpr)).toBe(true);
    expect(hasDifferentWeekdays("08:00,5M 23:00,off")).toBe(false);
  });

  it("preserves exotic multi-day continuous schedules in raw mode without corruption", () => {
    const exotic = "Mon-08:00,5M Wed-18:00,off";
    const parsed = parseBwLimit(exotic);
    expect(parsed.mode).toBe("raw");
    expect(serializeBwLimit(parsed)).toBe(exotic);
  });

  it("detects overlap warnings and invalid rule errors", () => {
    const overlapping = {
      mode: "schedule" as const,
      fixed: createBwRateValue(),
      defaultRate: createBwRateValue({ unlimited: true }),
      rules: [
        createDefaultScheduleRule({
          startTime: "08:00",
          endTime: "20:00",
          rate: createBwRateValue({ value: "10", unit: "M" }),
        }),
        createDefaultScheduleRule({
          startTime: "18:00",
          endTime: "23:00",
          rate: createBwRateValue({ value: "2", unit: "M" }),
        }),
      ],
      raw: "",
    };
    const res = detectBwModelIssues(overlapping);
    expect(res.errors).toHaveLength(0);
    expect(res.warnings).toHaveLength(1);

    const sameTime = {
      ...overlapping,
      rules: [
        createDefaultScheduleRule({
          startTime: "08:00",
          endTime: "08:00",
        }),
      ],
    };
    expect(detectBwModelIssues(sameTime).errors[0]).toContain("不能相同");
  });
});

describe("bwLimit runtime resolution, timeline, and formatting", () => {
  it("resolves active rate and next change across daily and weekend boundaries", () => {
    // Wednesday 2026-11-25 10:30
    const wedMorning = new Date(2026, 10, 25, 10, 30, 0);
    const active = resolveBwLimitAt("08:00,5M 23:00,off", wedMorning);
    expect(active.hasSchedule).toBe(true);
    expect(active.isUnlimited).toBe(false);
    expect(active.activeRate).toBe("5M");
    expect(active.nextRate).toBe("off");
    expect(active.nextChangeAt?.getHours()).toBe(23);

    // Wednesday 2026-11-25 07:30 -> wraps back to 23:00,off
    const earlyMorning = new Date(2026, 10, 25, 7, 30, 0);
    const early = resolveBwLimitAt("08:00,5M 23:00,off", earlyMorning);
    expect(early.isUnlimited).toBe(true);
    expect(early.nextRate).toBe("5M");
    expect(early.nextChangeAt?.getHours()).toBe(8);

    const segs = getDayTimelineSegments("08:00,5M 23:00,off", 3);
    expect(segs).toEqual([
      { startMin: 0, endMin: 480, rate: "off", isUnlimited: true },
      { startMin: 480, endMin: 1380, rate: "5M", isUnlimited: false },
      { startMin: 1380, endMin: 1440, rate: "off", isUnlimited: true },
    ]);
  });

  it("formats human-readable rates, Mbps hints, and summaries", () => {
    expect(formatBwRateHuman("10M")).toBe("10 MB/s");
    expect(formatBwRateHuman("500K")).toBe("500 KB/s");
    expect(formatBwRateHuman("5M:20M")).toBe("上传 5 MB/s / 下载 20 MB/s");
    expect(formatBwRateHuman("5M:off")).toBe("上传 5 MB/s / 下载 不限");
    expect(formatBwRateHuman("off")).toBe("不限速");
    expect(estimateMbps("10", "M")).toBe("≈ 80 Mbps");

    const wedDay = new Date(2026, 10, 25, 10, 30, 0);
    expect(formatBwLimitSummary("08:00,5M 23:00,off", { now: wedDay })).toBe(
      "定时限速（当前 5 MB/s）",
    );
    expect(formatBwLimitSummary("off", { allowInherit: true })).toBe(
      "不限速（覆盖全局）",
    );
    expect(formatBwLimitSummary("", { allowInherit: true })).toBe("跟随全局设置");
  });

  it("formats compact rates, timeline segment labels, and rich display info", () => {
    expect(formatBwRateCompact("off")).toBe("不限速");
    expect(formatBwRateCompact("10M")).toBe("10 MB/s");
    expect(formatBwRateCompact("5.5M:off")).toBe("上传 5.5 MB/s");
    expect(formatBwRateCompact("off:20M")).toBe("下载 20 MB/s");
    expect(formatBwRateCompact("5M:20M")).toBe("↑5 MB/s ↓20 MB/s");

    expect(formatBwTimelineSegLabel("off")).toBe("不限");
    expect(formatBwTimelineSegLabel("5.5M:off")).toBe("↑5.5M");
    expect(formatBwTimelineSegLabel("off:20M")).toBe("↓20M");
    expect(formatBwTimelineSegLabel("5M:20M")).toBe("↑5M/↓20M");
    expect(formatBwTimelineSegLabel("10M")).toBe("10M");

    const empty = describeBwLimit("");
    expect(empty.configured).toBe(false);
    expect(empty.chipLabel).toBe("全局不限速");
    expect(empty.kpiLabel).toBe("");

    const staticOff = describeBwLimit("off");
    expect(staticOff.configured).toBe(false);
    expect(staticOff.chipLabel).toBe("全局不限速");

    const fixedUpload = describeBwLimit("5.5M:off");
    expect(fixedUpload.configured).toBe(true);
    expect(fixedUpload.hasSchedule).toBe(false);
    expect(fixedUpload.isCurrentlyLimited).toBe(true);
    expect(fixedUpload.chipLabel).toBe("全局限速 · 上传 5.5 MB/s");
    expect(fixedUpload.kpiLabel).toBe("上传限速 5.5 MB/s");
    expect(fixedUpload.liveOptionLabel).toBe("上传 5.5 MB/s");

    const workdayExpr =
      "Mon-10:00,5.5M:off Mon-20:00,off Tue-10:00,5.5M:off Tue-20:00,off Wed-10:00,5.5M:off Wed-20:00,off Thu-10:00,5.5M:off Thu-20:00,off Fri-10:00,5.5M:off Fri-20:00,off";
    // Thursday 2026-11-26 14:30 -> inside limited window
    const thuAfternoon = new Date(2026, 10, 26, 14, 30, 0);
    const activeInfo = describeBwLimit(workdayExpr, { now: thuAfternoon });
    expect(activeInfo.configured).toBe(true);
    expect(activeInfo.hasSchedule).toBe(true);
    expect(activeInfo.isCurrentlyLimited).toBe(true);
    expect(activeInfo.chipLabel).toBe("定时限速 · 上传 5.5 MB/s");
    expect(activeInfo.kpiLabel).toBe("定时 · 上传 5.5 MB/s");
    expect(activeInfo.nextChangeLabel).toBe("今天 20:00 → 不限速");
    expect(activeInfo.rules).toEqual([
      {
        daysLabel: "工作日",
        timeRange: "10:00–20:00",
        crossMidnight: false,
        rateLabel: "上传 5.5 MB/s / 下载 不限",
        rateCompact: "上传 5.5 MB/s",
      },
    ]);
    expect(activeInfo.defaultRateHuman).toBe("不限速");

    // Thursday 2026-11-26 08:45 -> before 10:00 window (currently unlimited)
    const thuMorning = new Date(2026, 10, 26, 8, 45, 0);
    const idleInfo = describeBwLimit(workdayExpr, { now: thuMorning });
    expect(idleInfo.configured).toBe(true);
    expect(idleInfo.hasSchedule).toBe(true);
    expect(idleInfo.isCurrentlyLimited).toBe(false);
    expect(idleInfo.chipLabel).toBe("定时限速 · 当前不限速");
    expect(idleInfo.kpiLabel).toBe("定时 · 当前不限");
    expect(idleInfo.nextChangeLabel).toBe("今天 10:00 → 上传 5.5 MB/s");
  });
});
