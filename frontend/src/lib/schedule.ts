export type CronFieldKey =
  | "second"
  | "minute"
  | "hour"
  | "day"
  | "month"
  | "day_of_week";

export type SchedulePresetMode = "daily" | "weekly" | "monthly" | "custom";
export type IntervalUnit = "minute" | "hour" | "day";

export interface CronScheduleFields {
  second: string;
  minute: string;
  hour: string;
  day: string;
  month: string;
  day_of_week: string;
}

// Mirrors maxJobIntervalMinutes in backend/internal/service/job_scheduler.go
export const maxJobIntervalMinutes = 52_560_000;

export const cronFieldRanges: Record<
  CronFieldKey,
  readonly [number, number, string, string]
> = {
  second: [0, 59, "秒", "0-59"],
  minute: [0, 59, "分", "0-59"],
  hour: [0, 23, "时", "0-23"],
  day: [1, 31, "日", "1-31"],
  month: [1, 12, "月", "1-12"],
  day_of_week: [0, 6, "周", "0=周日..6=周六"],
};

export const intervalUnitOptions: Array<{
  value: IntervalUnit;
  label: string;
  factor: number;
}> = [
  { value: "minute", label: "分钟", factor: 1 },
  { value: "hour", label: "小时", factor: 60 },
  { value: "day", label: "天", factor: 1440 },
];

export const intervalUnitFactors: Record<IntervalUnit, number> = {
  minute: 1,
  hour: 60,
  day: 1440,
};

export const intervalPresets: Array<{
  label: string;
  value: number;
  unit: IntervalUnit;
}> = [
  { label: "30 分钟", value: 30, unit: "minute" },
  { label: "1 小时", value: 1, unit: "hour" },
  { label: "6 小时", value: 6, unit: "hour" },
  { label: "12 小时", value: 12, unit: "hour" },
  { label: "1 天", value: 1, unit: "day" },
];

export const weekdayOptions: Array<{ value: number; label: string; short: string }> = [
  { value: 1, label: "周一", short: "一" },
  { value: 2, label: "周二", short: "二" },
  { value: 3, label: "周三", short: "三" },
  { value: 4, label: "周四", short: "四" },
  { value: 5, label: "周五", short: "五" },
  { value: 6, label: "周六", short: "六" },
  { value: 0, label: "周日", short: "日" },
];

export const customCronPresets: Array<{
  label: string;
  fields: CronScheduleFields;
}> = [
  {
    label: "每小时整点",
    fields: { second: "0", minute: "0", hour: "*", day: "*", month: "*", day_of_week: "*" },
  },
  {
    label: "每 6 小时",
    fields: { second: "0", minute: "0", hour: "*/6", day: "*", month: "*", day_of_week: "*" },
  },
  {
    label: "工作日 02:00",
    fields: { second: "0", minute: "0", hour: "2", day: "*", month: "*", day_of_week: "1-5" },
  },
  {
    label: "每 30 分钟",
    fields: { second: "0", minute: "*/30", hour: "*", day: "*", month: "*", day_of_week: "*" },
  },
];

function isPlainIntInRange(value: string, low: number, high: number): boolean {
  const trimmed = value.trim();
  if (!/^\d+$/.test(trimmed)) return false;
  const num = Number(trimmed);
  return Number.isInteger(num) && num >= low && num <= high;
}

/**
 * Parses a single cron field into the set of matching integer values, or null if invalid.
 * Matches robfig/cron numeric syntax: `*`, `n`, `a-b`, `*\/s`, `a-b/s`, comma-separated.
 */
export function parseCronFieldValues(
  key: CronFieldKey,
  raw: string,
): { values: Set<number>; wildcard: boolean } | null {
  const [low, high] = cronFieldRanges[key];
  const value = String(raw ?? "").trim();
  if (!value) return null;

  const parts = value.split(",");
  const values = new Set<number>();
  let wildcard = false;

  for (const rawPart of parts) {
    const part = rawPart.trim();
    if (!part) return null;
    if (part === "*") {
      wildcard = true;
      for (let i = low; i <= high; i++) values.add(i);
      continue;
    }
    const match = part.match(/^(\*|\d+)(?:-(\d+))?(?:\/(\d+))?$/);
    if (!match) return null;

    const hasStar = match[1] === "*";
    if (hasStar && match[2]) return null;

    const start = hasStar ? low : Number(match[1]);
    const end =
      match[2] !== undefined
        ? Number(match[2])
        : hasStar || match[3] !== undefined
          ? high
          : start;
    const step = match[3] !== undefined ? Number(match[3]) : 1;

    if (
      start < low ||
      end > high ||
      start > end ||
      step <= 0 ||
      (match[3] !== undefined && step > high)
    ) {
      return null;
    }

    if (hasStar) wildcard = true;
    for (let i = start; i <= end; i += step) {
      values.add(i);
    }
  }

  return values.size > 0 ? { values, wildcard } : null;
}

export function validateCronField(key: CronFieldKey, value: string): string {
  const [low, high, label] = cronFieldRanges[key];
  if (!parseCronFieldValues(key, value)) {
    return `Cron「${label}」字段无效，范围 ${low}-${high}`;
  }
  return "";
}

export function validateCronFields(
  fields: Partial<Record<CronFieldKey, string | null | undefined>>,
): Partial<Record<CronFieldKey, string>> {
  const errors: Partial<Record<CronFieldKey, string>> = {};
  for (const key of Object.keys(cronFieldRanges) as CronFieldKey[]) {
    const err = validateCronField(key, String(fields[key] ?? ""));
    if (err) errors[key] = err;
  }
  return errors;
}

export function parseWeekdaySelection(dayOfWeek: string): number[] | null {
  const trimmed = dayOfWeek.trim();
  if (!trimmed || trimmed === "*") return null;
  if (!/^[0-6](?:-[0-6])?(?:,[0-6](?:-[0-6])?)*$/.test(trimmed)) return null;
  const set = new Set<number>();
  for (const part of trimmed.split(",")) {
    const [startStr, endStr] = part.split("-");
    const start = Number(startStr);
    const end = endStr === undefined ? start : Number(endStr);
    if (start > end) return null;
    for (let d = start; d <= end; d++) set.add(d);
  }
  return [...set].sort((a, b) => a - b);
}

export function formatWeekdayCronExpression(days: number[]): string {
  const unique = [...new Set(days.filter((d) => Number.isInteger(d) && d >= 0 && d <= 6))].sort(
    (a, b) => a - b,
  );
  if (unique.length === 0) return "0";
  if (unique.length === 7) return "*";

  const ranges: string[] = [];
  let start = unique[0];
  let prev = unique[0];
  for (let i = 1; i < unique.length; i++) {
    if (unique[i] === prev + 1) {
      prev = unique[i];
      continue;
    }
    ranges.push(start === prev ? String(start) : `${start}-${prev}`);
    start = unique[i];
    prev = unique[i];
  }
  ranges.push(start === prev ? String(start) : `${start}-${prev}`);
  return ranges.join(",");
}

export function detectCronPresetMode(
  fields: Partial<Record<CronFieldKey, string | null | undefined>>,
): SchedulePresetMode {
  const second = String(fields.second ?? "0").trim();
  const minute = String(fields.minute ?? "").trim();
  const hour = String(fields.hour ?? "").trim();
  const day = String(fields.day ?? "*").trim();
  const month = String(fields.month ?? "*").trim();
  const dayOfWeek = String(fields.day_of_week ?? "*").trim();

  if (
    second !== "0" ||
    month !== "*" ||
    !isPlainIntInRange(hour, 0, 23) ||
    !isPlainIntInRange(minute, 0, 59)
  ) {
    return "custom";
  }
  if (day === "*" && dayOfWeek === "*") return "daily";
  if (day === "*" && parseWeekdaySelection(dayOfWeek) !== null) return "weekly";
  if (dayOfWeek === "*" && isPlainIntInRange(day, 1, 31)) return "monthly";
  return "custom";
}

export function applyCronPresetMode(
  current: Partial<Record<CronFieldKey, string | null | undefined>>,
  mode: SchedulePresetMode,
): CronScheduleFields {
  const hour = isPlainIntInRange(String(current.hour ?? ""), 0, 23)
    ? String(Number(current.hour))
    : "2";
  const minute = isPlainIntInRange(String(current.minute ?? ""), 0, 59)
    ? String(Number(current.minute))
    : "0";
  const day = isPlainIntInRange(String(current.day ?? ""), 1, 31)
    ? String(Number(current.day))
    : "1";
  const existingDays = parseWeekdaySelection(String(current.day_of_week ?? ""));
  const dayOfWeek = existingDays?.length
    ? formatWeekdayCronExpression(existingDays)
    : "0";

  switch (mode) {
    case "daily":
      return { second: "0", minute, hour, day: "*", month: "*", day_of_week: "*" };
    case "weekly":
      return { second: "0", minute, hour, day: "*", month: "*", day_of_week: dayOfWeek };
    case "monthly":
      return { second: "0", minute, hour, day, month: "*", day_of_week: "*" };
    case "custom":
      return {
        second: String(current.second ?? "0").trim() || "0",
        minute: String(current.minute ?? "0").trim() || "0",
        hour: String(current.hour ?? "2").trim() || "2",
        day: String(current.day ?? "*").trim() || "*",
        month: String(current.month ?? "*").trim() || "*",
        day_of_week: String(current.day_of_week ?? "*").trim() || "*",
      };
  }
}

export function splitIntervalMinutes(minutes: number): {
  value: number;
  unit: IntervalUnit;
} {
  if (!Number.isInteger(minutes) || minutes <= 0) {
    return { value: Math.max(0, Number.isFinite(minutes) ? minutes : 0), unit: "minute" };
  }
  if (minutes % 1440 === 0) {
    return { value: minutes / 1440, unit: "day" };
  }
  if (minutes % 60 === 0) {
    return { value: minutes / 60, unit: "hour" };
  }
  return { value: minutes, unit: "minute" };
}

export function intervalInputToMinutes(raw: string, unit: IntervalUnit): number {
  const trimmed = raw.trim();
  if (!trimmed) return 0;
  if (/^\d+$/.test(trimmed)) {
    return Number(trimmed) * intervalUnitFactors[unit];
  }
  const parsed = Number(trimmed);
  if (Number.isFinite(parsed) && !Number.isInteger(parsed)) {
    // Keep non-integer signal (or fractional minute) so validation rejects it cleanly
    const scaled = parsed * intervalUnitFactors[unit];
    return Number.isInteger(scaled) ? parsed : scaled;
  }
  return 0;
}

export function formatIntervalHuman(minutes: number): string {
  if (!Number.isInteger(minutes) || minutes <= 0) {
    return "执行间隔无效";
  }
  const { value, unit } = splitIntervalMinutes(minutes);
  if (unit === "day") {
    return `每 ${value} 天（${minutes} 分钟）执行一次`;
  }
  if (unit === "hour") {
    return `每 ${value} 小时（${minutes} 分钟）执行一次`;
  }
  return `每 ${minutes} 分钟执行一次`;
}

/**
 * Computes the next `count` execution timestamps for a 6-field robfig/cron schedule.
 * Uses field-level jumping so sparse schedules (e.g. Feb 29) resolve in sub-millisecond time.
 */
export function computeNextCronRuns(
  fields: Partial<Record<CronFieldKey, string | null | undefined>>,
  count = 3,
  now = new Date(),
): Date[] {
  const sec = parseCronFieldValues("second", String(fields.second ?? ""));
  const min = parseCronFieldValues("minute", String(fields.minute ?? ""));
  const hr = parseCronFieldValues("hour", String(fields.hour ?? ""));
  const dom = parseCronFieldValues("day", String(fields.day ?? ""));
  const mon = parseCronFieldValues("month", String(fields.month ?? ""));
  const dow = parseCronFieldValues("day_of_week", String(fields.day_of_week ?? ""));
  if (!sec || !min || !hr || !dom || !mon || !dow) return [];

  const results: Date[] = [];
  const cursor = new Date(Math.floor(now.getTime() / 1000) * 1000 + 1000);
  const maxYear = cursor.getFullYear() + 5;
  let guard = 0;

  while (results.length < count && cursor.getFullYear() <= maxYear && guard < 10_000) {
    guard++;
    if (!mon.values.has(cursor.getMonth() + 1)) {
      cursor.setMonth(cursor.getMonth() + 1, 1);
      cursor.setHours(0, 0, 0, 0);
      continue;
    }

    const domMatches = dom.values.has(cursor.getDate());
    const dowMatches = dow.values.has(cursor.getDay());
    // robfig/cron semantics: when both dom and dow are restricted (non-wildcard),
    // either matching satisfies the day condition; otherwise both must match.
    const dayMatches =
      !dom.wildcard && !dow.wildcard
        ? domMatches || dowMatches
        : domMatches && dowMatches;

    if (!dayMatches) {
      cursor.setDate(cursor.getDate() + 1);
      cursor.setHours(0, 0, 0, 0);
      continue;
    }

    if (!hr.values.has(cursor.getHours())) {
      cursor.setHours(cursor.getHours() + 1, 0, 0, 0);
      continue;
    }

    if (!min.values.has(cursor.getMinutes())) {
      cursor.setMinutes(cursor.getMinutes() + 1, 0, 0);
      continue;
    }

    if (!sec.values.has(cursor.getSeconds())) {
      cursor.setSeconds(cursor.getSeconds() + 1, 0);
      continue;
    }

    results.push(new Date(cursor.getTime()));
    cursor.setSeconds(cursor.getSeconds() + 1, 0);
  }

  return results;
}

export function formatCronRunTime(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}
