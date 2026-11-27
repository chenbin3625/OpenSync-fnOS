import { validateBwLimitExpression } from "./bwLimit";
import type { WeeklyScheduleConfig, WeeklySlotMode } from "../types";

export const WEEKLY_DAY_COUNT = 7;
export const WEEKLY_HOUR_COUNT = 24;

/** UI display order: Monday (1) .. Saturday (6), Sunday (0), aligned with Go time.Weekday. */
export const WEEKLY_DISPLAY_DAYS = [1, 2, 3, 4, 5, 6, 0] as const;

export const WEEKLY_DAY_LABELS: Record<number, string> = {
  0: "周日",
  1: "周一",
  2: "周二",
  3: "周三",
  4: "周四",
  5: "周五",
  6: "周六",
};

export const WEEKLY_DAY_SHORT_LABELS: Record<number, string> = {
  0: "日",
  1: "一",
  2: "二",
  3: "三",
  4: "四",
  5: "五",
  6: "六",
};

export const WEEKLY_SLOT_MODES: readonly WeeklySlotMode[] = [
  "full",
  "limited",
  "pause",
];

export const WEEKLY_SLOT_LABELS: Record<WeeklySlotMode, string> = {
  full: "全速同步",
  limited: "限速同步",
  pause: "暂停同步",
};

export const WEEKLY_LIMIT_RATE_PRESETS = ["2M", "5M", "10M", "20M"] as const;

export type WeeklyScheduleTemplateKey =
  | "all_full"
  | "night_full"
  | "workday_limited_night_full";

export const WEEKLY_SCHEDULE_TEMPLATES: Array<{
  key: WeeklyScheduleTemplateKey;
  label: string;
  desc: string;
}> = [
  {
    key: "all_full",
    label: "全天同步",
    desc: "7×24 小时全速实时同步",
  },
  {
    key: "night_full",
    label: "仅夜间全速",
    desc: "每日 23:00–07:00 全速运行，白天暂停同步",
  },
  {
    key: "workday_limited_night_full",
    label: "工作日白天限速+夜间全速",
    desc: "周一至五 09:00–18:00 限速同步，其余时段及周末全速运行",
  },
];

function isValidSlotMode(value: unknown): value is WeeklySlotMode {
  return value === "full" || value === "limited" || value === "pause";
}

export function createEmptyWeeklyDays(
  fill: WeeklySlotMode = "full",
): WeeklySlotMode[][] {
  return Array.from({ length: WEEKLY_DAY_COUNT }, () =>
    Array.from({ length: WEEKLY_HOUR_COUNT }, () => fill),
  );
}

export function cloneWeeklyDays(
  days?: readonly (readonly WeeklySlotMode[])[],
): WeeklySlotMode[][] {
  return Array.from({ length: WEEKLY_DAY_COUNT }, (_, dayIdx) => {
    const row = days?.[dayIdx];
    return Array.from({ length: WEEKLY_HOUR_COUNT }, (_, hourIdx) => {
      const cell = row?.[hourIdx];
      return isValidSlotMode(cell) ? cell : "full";
    });
  });
}

export function createDefaultWeeklySchedule(
  enabled = false,
): WeeklyScheduleConfig {
  return {
    enabled,
    limitRate: "5M",
    stopOutside: false,
    days: createEmptyWeeklyDays("full"),
  };
}

export function cloneWeeklySchedule(
  schedule?: WeeklyScheduleConfig,
): WeeklyScheduleConfig {
  if (!schedule) return createDefaultWeeklySchedule(false);
  return {
    enabled: Boolean(schedule.enabled),
    limitRate: (schedule.limitRate || "").trim() || "5M",
    stopOutside: Boolean(schedule.stopOutside),
    days: cloneWeeklyDays(schedule.days),
  };
}

export function normalizeWeeklySchedule(
  raw: unknown,
): WeeklyScheduleConfig | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return undefined;
  }
  const obj = raw as Record<string, unknown>;
  const enabled = Boolean(obj.enabled);
  const limitRate =
    typeof obj.limitRate === "string" ? obj.limitRate.trim() : "";
  const stopOutside = Boolean(obj.stopOutside);
  const rawDays = Array.isArray(obj.days) ? obj.days : undefined;
  const days = cloneWeeklyDays(
    rawDays as WeeklySlotMode[][] | undefined,
  );
  return {
    enabled,
    limitRate: limitRate || "5M",
    stopOutside,
    days,
  };
}

export function cycleWeeklySlotMode(mode: WeeklySlotMode): WeeklySlotMode {
  switch (mode) {
    case "full":
      return "limited";
    case "limited":
      return "pause";
    case "pause":
    default:
      return "full";
  }
}

export function applyWeeklyScheduleTemplate(
  template: WeeklyScheduleTemplateKey,
  current?: WeeklyScheduleConfig,
): WeeklyScheduleConfig {
  const days = createEmptyWeeklyDays("full");
  if (template === "night_full") {
    for (let d = 0; d < WEEKLY_DAY_COUNT; d++) {
      for (let h = 0; h < WEEKLY_HOUR_COUNT; h++) {
        // 23:00 - 06:59 full; 07:00 - 22:59 pause
        days[d][h] = h >= 23 || h < 7 ? "full" : "pause";
      }
    }
  } else if (template === "workday_limited_night_full") {
    for (let d = 1; d <= 5; d++) {
      for (let h = 0; h < WEEKLY_HOUR_COUNT; h++) {
        // Mon-Fri 09:00 - 17:59 limited; rest & weekends full
        days[d][h] = h >= 9 && h < 18 ? "limited" : "full";
      }
    }
  }
  return {
    enabled: true,
    limitRate: current?.limitRate?.trim() || "5M",
    stopOutside: current?.stopOutside ?? false,
    days,
  };
}

export function countWeeklyScheduleSlots(schedule?: WeeklyScheduleConfig): {
  full: number;
  limited: number;
  pause: number;
} {
  const days = schedule?.days ?? createEmptyWeeklyDays("full");
  let full = 0;
  let limited = 0;
  let pause = 0;
  for (let d = 0; d < WEEKLY_DAY_COUNT; d++) {
    for (let h = 0; h < WEEKLY_HOUR_COUNT; h++) {
      const mode = days[d]?.[h] ?? "full";
      if (mode === "limited") limited += 1;
      else if (mode === "pause") pause += 1;
      else full += 1;
    }
  }
  return { full, limited, pause };
}

export function hasLimitedWeeklySlots(schedule?: WeeklyScheduleConfig): boolean {
  return countWeeklyScheduleSlots(schedule).limited > 0;
}

export function validateWeeklySchedule(
  schedule?: WeeklyScheduleConfig,
): string {
  if (!schedule || !schedule.enabled) return "";
  if (
    !Array.isArray(schedule.days) ||
    schedule.days.length !== WEEKLY_DAY_COUNT ||
    schedule.days.some(
      (row) =>
        !Array.isArray(row) ||
        row.length !== WEEKLY_HOUR_COUNT ||
        row.some((cell) => !isValidSlotMode(cell)),
    )
  ) {
    return "周计划网格数据无效（需包含 7×24 小时时段状态）";
  }
  if (hasLimitedWeeklySlots(schedule)) {
    const rate = (schedule.limitRate || "").trim();
    if (!rate) {
      return "周计划中包含「限速同步」时段，请填写限速速率（如 5M）";
    }
    if (
      /\s/.test(rate) ||
      rate.includes(",") ||
      rate.toLowerCase() === "off" ||
      rate === "0"
    ) {
      return "限速时段速率需填写单个带宽上限，如 5M、10M 或 500K";
    }
    const err = validateBwLimitExpression(rate);
    if (err) {
      return err;
    }
  }
  return "";
}

export function formatWeeklyScheduleSummary(
  schedule?: WeeklyScheduleConfig,
): string {
  if (!schedule || !schedule.enabled) {
    return "持续实时同步（全天候 7×24 小时全速运行）";
  }
  const { full, limited, pause } = countWeeklyScheduleSlots(schedule);
  const parts = [`全速 ${full}h`];
  if (limited > 0) {
    parts.push(`限速 ${limited}h（${(schedule.limitRate || "").trim() || "5M"}）`);
  }
  if (pause > 0) {
    parts.push(`暂停 ${pause}h`);
  }
  if (schedule.stopOutside) {
    parts.push("暂停时段立即中断传输");
  }
  return `按周计划运行：${parts.join(" · ")}`;
}
