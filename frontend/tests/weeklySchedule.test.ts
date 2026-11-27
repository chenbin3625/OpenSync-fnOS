import { describe, expect, it } from "vitest";
import {
  applyWeeklyScheduleTemplate,
  countWeeklyScheduleSlots,
  createDefaultWeeklySchedule,
  cycleWeeklySlotMode,
  formatWeeklyScheduleSummary,
  hasLimitedWeeklySlots,
  normalizeWeeklySchedule,
  validateWeeklySchedule,
} from "../src/lib/weeklySchedule";

describe("weeklySchedule helpers", () => {
  it("creates 7x24 default schedule filled with full mode", () => {
    const ws = createDefaultWeeklySchedule(true);
    expect(ws.enabled).toBe(true);
    expect(ws.limitRate).toBe("5M");
    expect(ws.stopOutside).toBe(false);
    expect(ws.days).toHaveLength(7);
    expect(ws.days.every((row) => row.length === 24 && row.every((c) => c === "full"))).toBe(true);
    expect(countWeeklyScheduleSlots(ws)).toEqual({ full: 168, limited: 0, pause: 0 });
    expect(hasLimitedWeeklySlots(ws)).toBe(false);
  });

  it("cycles slot modes cleanly full -> limited -> pause -> full", () => {
    expect(cycleWeeklySlotMode("full")).toBe("limited");
    expect(cycleWeeklySlotMode("limited")).toBe("pause");
    expect(cycleWeeklySlotMode("pause")).toBe("full");
  });

  it("applies built-in templates accurately across days and hours", () => {
    const nightOnly = applyWeeklyScheduleTemplate("night_full");
    expect(nightOnly.enabled).toBe(true);
    // 23:00-06:59 (8 hours/day * 7 = 56 hours) are full; 16 hours/day * 7 = 112 pause
    expect(nightOnly.days[1][23]).toBe("full");
    expect(nightOnly.days[1][2]).toBe("full");
    expect(nightOnly.days[1][6]).toBe("full");
    expect(nightOnly.days[1][7]).toBe("pause");
    expect(nightOnly.days[1][22]).toBe("pause");
    expect(countWeeklyScheduleSlots(nightOnly)).toEqual({ full: 56, limited: 0, pause: 112 });

    const workdayLimited = applyWeeklyScheduleTemplate("workday_limited_night_full", {
      enabled: true,
      limitRate: "8M",
      stopOutside: true,
      days: nightOnly.days,
    });
    expect(workdayLimited.limitRate).toBe("8M");
    expect(workdayLimited.stopOutside).toBe(true);
    // Mon(1)..Fri(5) 09:00..17:59 = 9 hours * 5 = 45 limited
    expect(workdayLimited.days[1][9]).toBe("limited");
    expect(workdayLimited.days[5][17]).toBe("limited");
    expect(workdayLimited.days[1][18]).toBe("full");
    // Weekends (0=Sun, 6=Sat) remain all full
    expect(workdayLimited.days[0][12]).toBe("full");
    expect(workdayLimited.days[6][12]).toBe("full");
    expect(countWeeklyScheduleSlots(workdayLimited)).toEqual({
      full: 168 - 45,
      limited: 45,
      pause: 0,
    });
  });

  it("validates limitRate only when enabled and containing limited slots", () => {
    const base = createDefaultWeeklySchedule(false);
    expect(validateWeeklySchedule({ ...base, limitRate: "bad-rate" })).toBe("");

    const enabledNoLimited = { ...base, enabled: true, limitRate: "" };
    expect(validateWeeklySchedule(enabledNoLimited)).toBe("");

    const withLimited = applyWeeklyScheduleTemplate("workday_limited_night_full");
    expect(validateWeeklySchedule(withLimited)).toBe("");

    expect(
      validateWeeklySchedule({ ...withLimited, limitRate: "" }),
    ).toContain("限速速率");
    expect(
      validateWeeklySchedule({ ...withLimited, limitRate: "08:00,5M" }),
    ).toContain("单个带宽上限");
    expect(
      validateWeeklySchedule({ ...withLimited, limitRate: "not-a-rate" }),
    ).toBeTruthy();
  });

  it("normalizes arbitrary server JSON payloads and formats readable summaries", () => {
    expect(normalizeWeeklySchedule(null)).toBeUndefined();
    const partial = normalizeWeeklySchedule({
      enabled: true,
      limitRate: " 10M ",
      stopOutside: true,
      days: [["pause", "unknown"]],
    });
    expect(partial?.enabled).toBe(true);
    expect(partial?.limitRate).toBe("10M");
    expect(partial?.stopOutside).toBe(true);
    expect(partial?.days).toHaveLength(7);
    expect(partial?.days[0][0]).toBe("pause");
    expect(partial?.days[0][1]).toBe("full");
    expect(partial?.days[6][23]).toBe("full");

    expect(formatWeeklyScheduleSummary(createDefaultWeeklySchedule(false))).toContain(
      "全天候 7×24 小时",
    );
    expect(
      formatWeeklyScheduleSummary(
        applyWeeklyScheduleTemplate("workday_limited_night_full"),
      ),
    ).toContain("限速 45h（5M）");
  });
});
