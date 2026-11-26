export type BwSpeedUnit = "K" | "M" | "G";
export type BwMode = "inherit" | "unlimited" | "fixed" | "schedule" | "raw";
export type BwDayPreset = "daily" | "weekdays" | "weekend" | "custom";

export interface BwRateValue {
  unlimited: boolean;
  asymmetric: boolean;
  value: string;
  unit: BwSpeedUnit;
  uploadUnlimited: boolean;
  uploadValue: string;
  uploadUnit: BwSpeedUnit;
  downloadUnlimited: boolean;
  downloadValue: string;
  downloadUnit: BwSpeedUnit;
}

export interface BwScheduleRule {
  id: string;
  days: number[]; // 0=Sun, 1=Mon ... 6=Sat
  startTime: string; // HH:MM
  endTime: string; // HH:MM (cross-midnight when endTime < startTime)
  rate: BwRateValue;
}

export interface BwLimitModel {
  mode: BwMode;
  fixed: BwRateValue;
  defaultRate: BwRateValue;
  rules: BwScheduleRule[];
  raw: string;
}

export interface BwResolvedSlot {
  activeRate: string;
  isUnlimited: boolean;
  hasSchedule: boolean;
  nextChangeAt?: Date;
  nextRate?: string;
}

export interface BwTimelineSegment {
  startMin: number; // 0..1439
  endMin: number; // 1..1440
  rate: string;
  isUnlimited: boolean;
}

export interface BwSchedulePreset {
  id: string;
  label: string;
  description: string;
  expression: string;
}

const MINUTES_PER_DAY = 24 * 60;
const MINUTES_PER_WEEK = 7 * MINUTES_PER_DAY;
const MAX_BW_LIMIT_LEN = 1024;
const MAX_BW_LIMIT_TOKENS = 128;

const BW_RATE_PART_RE = /^(?:off|0|[0-9]+(?:\.[0-9]+)?(?:b|[kmgtp](?:i|ib)?)?)$/i;
const BW_SCHEDULE_TOKEN_RE =
  /^(?:(sun|mon|tue|wed|thu|fri|sat)-)?([01]?[0-9]|2[0-3]):([0-5][0-9]),(.+)$/i;
const HHMM_RE = /^([01]\d|2[0-3]):([0-5]\d)$/;

const WEEKDAY_TO_RCLONE = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;
const WEEKDAY_MAP: Record<string, number> = {
  sun: 0,
  mon: 1,
  tue: 2,
  wed: 3,
  thu: 4,
  fri: 5,
  sat: 6,
};

export const bwWeekdayOptions: Array<{ value: number; label: string; short: string }> = [
  { value: 1, label: "周一", short: "一" },
  { value: 2, label: "周二", short: "二" },
  { value: 3, label: "周三", short: "三" },
  { value: 4, label: "周四", short: "四" },
  { value: 5, label: "周五", short: "五" },
  { value: 6, label: "周六", short: "六" },
  { value: 0, label: "周日", short: "日" },
];

export const bwSpeedUnits: Array<{ value: BwSpeedUnit; label: string }> = [
  { value: "K", label: "KB/s" },
  { value: "M", label: "MB/s" },
  { value: "G", label: "GB/s" },
];

export const bwFixedPresets: Array<{ label: string; value: string; unit: BwSpeedUnit }> = [
  { label: "1 MB/s", value: "1", unit: "M" },
  { label: "5 MB/s", value: "5", unit: "M" },
  { label: "10 MB/s", value: "10", unit: "M" },
  { label: "20 MB/s", value: "20", unit: "M" },
  { label: "50 MB/s", value: "50", unit: "M" },
  { label: "100 MB/s", value: "100", unit: "M" },
];

export const bwSchedulePresets: BwSchedulePreset[] = [
  {
    id: "daytime-5m",
    label: "日间限 5 MB/s · 夜间不限",
    description: "每天 08:00–23:00 限速 5 MB/s，其余时间全速",
    expression: "08:00,5M 23:00,off",
  },
  {
    id: "workdays-5m",
    label: "工作日限 5 MB/s · 周末不限",
    description: "周一至周五 09:00–18:00 限速 5 MB/s，其余时间全速",
    expression:
      "Mon-09:00,5M Mon-18:00,off Tue-09:00,5M Tue-18:00,off Wed-09:00,5M Wed-18:00,off Thu-09:00,5M Thu-18:00,off Fri-09:00,5M Fri-18:00,off",
  },
  {
    id: "evening-peak-2m",
    label: "晚高峰限 2 MB/s",
    description: "每天 19:00–23:30 限速 2 MB/s，避免影响影音娱乐",
    expression: "19:00,2M 23:30,off",
  },
  {
    id: "tiered",
    label: "白天 10M · 晚间 2M · 深夜全速",
    description: "08:00–19:00 限 10 MB/s，19:00–23:00 限 2 MB/s，23:00–08:00 全速",
    expression: "08:00,10M 19:00,2M 23:00,off",
  },
];

let ruleSeq = 0;
export function nextBwRuleId(): string {
  ruleSeq += 1;
  return `bw-rule-${ruleSeq}`;
}

export function createBwRateValue(overrides: Partial<BwRateValue> = {}): BwRateValue {
  return {
    unlimited: false,
    asymmetric: false,
    value: "10",
    unit: "M",
    uploadUnlimited: false,
    uploadValue: "5",
    uploadUnit: "M",
    downloadUnlimited: false,
    downloadValue: "20",
    downloadUnit: "M",
    ...overrides,
  };
}

export function createDefaultScheduleRule(
  overrides: Partial<BwScheduleRule> = {},
): BwScheduleRule {
  return {
    id: nextBwRuleId(),
    days: [0, 1, 2, 3, 4, 5, 6],
    startTime: "08:00",
    endTime: "23:00",
    rate: createBwRateValue({ value: "5", unit: "M" }),
    ...overrides,
  };
}

export function createDefaultBwLimitModel(allowInherit = false): BwLimitModel {
  return {
    mode: allowInherit ? "inherit" : "unlimited",
    fixed: createBwRateValue({ value: "10", unit: "M" }),
    defaultRate: createBwRateValue({ unlimited: true, value: "20", unit: "M" }),
    rules: [createDefaultScheduleRule()],
    raw: "",
  };
}

export function normalizeDays(days: readonly number[]): number[] {
  const unique = Array.from(
    new Set(days.filter((d) => Number.isInteger(d) && d >= 0 && d <= 6)),
  );
  unique.sort((a, b) => a - b);
  return unique;
}

export function getDayPreset(days: readonly number[]): BwDayPreset {
  const norm = normalizeDays(days);
  if (norm.length === 7) return "daily";
  if (norm.length === 5 && norm.join(",") === "1,2,3,4,5") return "weekdays";
  if (norm.length === 2 && norm.join(",") === "0,6") return "weekend";
  return "custom";
}

export function daysForPreset(preset: Exclude<BwDayPreset, "custom">): number[] {
  switch (preset) {
    case "daily":
      return [0, 1, 2, 3, 4, 5, 6];
    case "weekdays":
      return [1, 2, 3, 4, 5];
    case "weekend":
      return [0, 6];
  }
}

export function formatDaysLabel(days: readonly number[]): string {
  const preset = getDayPreset(days);
  if (preset === "daily") return "每天";
  if (preset === "weekdays") return "工作日";
  if (preset === "weekend") return "周末";
  const ordered = [1, 2, 3, 4, 5, 6, 0].filter((d) => days.includes(d));
  return ordered
    .map((d) => bwWeekdayOptions.find((o) => o.value === d)?.label || "")
    .filter(Boolean)
    .join("、");
}

function isValidRateSpec(spec: string): boolean {
  if (!spec) return false;
  const colonIdx = spec.indexOf(":");
  if (colonIdx >= 0) {
    const up = spec.slice(0, colonIdx);
    const down = spec.slice(colonIdx + 1);
    if (!up || !down || down.includes(":")) return false;
    return BW_RATE_PART_RE.test(up) && BW_RATE_PART_RE.test(down);
  }
  return BW_RATE_PART_RE.test(spec);
}

/**
 * Validates an rclone --bwlimit expression identically to backend ValidateBwLimit.
 * Returns null when valid, or a Chinese user-facing error message when invalid.
 */
export function validateBwLimitExpression(raw: string): string | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  if (
    trimmed.length > MAX_BW_LIMIT_LEN ||
    trimmed.startsWith("-") ||
    /[\x00-\x1f\x7f]/.test(trimmed)
  ) {
    return "限速规则长度超限或包含非法字符";
  }
  const tokens = trimmed.split(/\s+/);
  if (tokens.length > MAX_BW_LIMIT_TOKENS) {
    return "分时段限速规则节点数量过多";
  }
  if (tokens.length === 1 && !tokens[0].includes(",")) {
    if (!isValidRateSpec(tokens[0])) {
      if (/^[0-9]+(?:\.[0-9]+)?[kmgtp]b$/i.test(tokens[0])) {
        return "rclone 速率单位请使用 K / M / G（如 10M、500K），不要附加 B 后缀";
      }
      return "限速格式无效，支持如 10M、500K、5M:20M、off 或分时段规则 08:00,5M 23:00,off";
    }
    return null;
  }
  for (const tok of tokens) {
    const match = BW_SCHEDULE_TOKEN_RE.exec(tok);
    if (!match || !isValidRateSpec(match[4])) {
      if (match && /^[0-9]+(?:\.[0-9]+)?[kmgtp]b$/i.test(match[4])) {
        return `规则「${tok}」中的速率单位请使用 K / M / G（不要附加 B 后缀）`;
      }
      return `分时段规则「${tok}」格式无效，应为 HH:MM,速率 或 Mon-HH:MM,速率`;
    }
  }
  return null;
}

function formatNumericString(num: number): string {
  if (!Number.isFinite(num) || num <= 0) return "";
  const rounded = Math.round(num * 100) / 100;
  return String(rounded);
}

function parseSinglePartToUnit(
  spec: string,
): { unlimited: boolean; value: string; unit: BwSpeedUnit } | null {
  const s = spec.trim();
  if (!s || s === "0" || /^off$/i.test(s)) {
    return { unlimited: true, value: "10", unit: "M" };
  }
  if (!BW_RATE_PART_RE.test(s)) return null;
  const low = s.toLowerCase();
  const numMatch = /^([0-9]+(?:\.[0-9]+)?)(b|[kmgtp](?:i|ib)?)?$/.exec(low);
  if (!numMatch) return null;
  const num = Number(numMatch[1]);
  if (!Number.isFinite(num) || num <= 0) {
    return { unlimited: true, value: "10", unit: "M" };
  }
  const suffix = numMatch[2] || "";
  if (!suffix) {
    // Bare number in rclone --bwlimit is KiB/s
    return { unlimited: false, value: formatNumericString(num), unit: "K" };
  }
  if (suffix === "b") {
    const kib = num / 1024;
    if (kib < 0.01) return null;
    return { unlimited: false, value: formatNumericString(kib), unit: "K" };
  }
  const letter = suffix[0];
  if (letter === "k") {
    return { unlimited: false, value: formatNumericString(num), unit: "K" };
  }
  if (letter === "m") {
    return { unlimited: false, value: formatNumericString(num), unit: "M" };
  }
  if (letter === "g") {
    return { unlimited: false, value: formatNumericString(num), unit: "G" };
  }
  if (letter === "t") {
    return { unlimited: false, value: formatNumericString(num * 1024), unit: "G" };
  }
  return null;
}

export function normalizeSingleRateSpec(spec: string): string {
  const s = spec.trim();
  if (!s || s === "0" || /^off$/i.test(s)) return "off";
  const colonIdx = s.indexOf(":");
  if (colonIdx >= 0) {
    const up = normalizeRateAtom(s.slice(0, colonIdx));
    const down = normalizeRateAtom(s.slice(colonIdx + 1));
    if (up === "off" && down === "off") return "off";
    return `${up}:${down}`;
  }
  return normalizeRateAtom(s);
}

function normalizeRateAtom(atom: string): string {
  const s = atom.trim();
  if (!s || s === "0" || /^off$/i.test(s)) return "off";
  const parsed = parseSinglePartToUnit(s);
  if (parsed && !parsed.unlimited) {
    // Only canonicalize simple K/M/G inputs cleanly; keep unusual units untouched if needed.
    const low = s.toLowerCase();
    if (/^[0-9]+(?:\.[0-9]+)?[kmg](?:i|ib)?$/.test(low)) {
      return `${parsed.value}${parsed.unit}`;
    }
  }
  return s;
}

export function parseRateValue(spec: string): BwRateValue | null {
  const trimmed = spec.trim();
  if (!trimmed || trimmed === "0" || /^off$/i.test(trimmed)) {
    return createBwRateValue({ unlimited: true });
  }
  const colonIdx = trimmed.indexOf(":");
  if (colonIdx >= 0) {
    const upRaw = trimmed.slice(0, colonIdx);
    const downRaw = trimmed.slice(colonIdx + 1);
    if (downRaw.includes(":")) return null;
    const up = parseSinglePartToUnit(upRaw);
    const down = parseSinglePartToUnit(downRaw);
    if (!up || !down) return null;
    if (up.unlimited && down.unlimited) {
      return createBwRateValue({ unlimited: true });
    }
    return createBwRateValue({
      unlimited: false,
      asymmetric: true,
      value: !up.unlimited ? up.value : down.value,
      unit: !up.unlimited ? up.unit : down.unit,
      uploadUnlimited: up.unlimited,
      uploadValue: up.value,
      uploadUnit: up.unit,
      downloadUnlimited: down.unlimited,
      downloadValue: down.value,
      downloadUnit: down.unit,
    });
  }
  const single = parseSinglePartToUnit(trimmed);
  if (!single) return null;
  return createBwRateValue({
    unlimited: single.unlimited,
    asymmetric: false,
    value: single.value,
    unit: single.unit,
    uploadUnlimited: single.unlimited,
    uploadValue: single.value,
    uploadUnit: single.unit,
    downloadUnlimited: single.unlimited,
    downloadValue: single.value,
    downloadUnit: single.unit,
  });
}

function formatPartToken(value: string, unit: BwSpeedUnit): string | null {
  const trimmed = value.trim();
  if (!trimmed || !/^[0-9]+(?:\.[0-9]+)?$/.test(trimmed)) return null;
  const num = Number(trimmed);
  if (!Number.isFinite(num) || num <= 0) return null;
  return `${formatNumericString(num)}${unit}`;
}

export function serializeRateValue(rate: BwRateValue): string | null {
  if (rate.unlimited) return "off";
  if (rate.asymmetric) {
    const up = rate.uploadUnlimited
      ? "off"
      : formatPartToken(rate.uploadValue, rate.uploadUnit);
    const down = rate.downloadUnlimited
      ? "off"
      : formatPartToken(rate.downloadValue, rate.downloadUnit);
    if (!up || !down) return null;
    if (up === "off" && down === "off") return "off";
    return `${up}:${down}`;
  }
  return formatPartToken(rate.value, rate.unit);
}

export function parseHHMM(hhmm: string): number | null {
  const m = HHMM_RE.exec(hhmm.trim());
  if (!m) return null;
  return Number(m[1]) * 60 + Number(m[2]);
}

export function formatHHMM(minOfDay: number): string {
  const normalized = ((minOfDay % MINUTES_PER_DAY) + MINUTES_PER_DAY) % MINUTES_PER_DAY;
  const hh = Math.floor(normalized / 60);
  const mm = normalized % 60;
  return `${String(hh).padStart(2, "0")}:${String(mm).padStart(2, "0")}`;
}

interface WeeklyEntry {
  weekMin: number;
  rate: string;
}

function parseTimetableEntries(raw: string): WeeklyEntry[] | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  const tokens = trimmed.split(/\s+/);
  if (tokens.length === 1 && !tokens[0].includes(",")) return null;
  const rawEntries: WeeklyEntry[] = [];
  for (const tok of tokens) {
    const m = BW_SCHEDULE_TOKEN_RE.exec(tok);
    if (!m || !isValidRateSpec(m[4])) return null;
    const hh = Number(m[2]);
    const mm = Number(m[3]);
    const dayMin = hh * 60 + mm;
    const rate = normalizeSingleRateSpec(m[4]);
    if (m[1]) {
      const wd = WEEKDAY_MAP[m[1].toLowerCase()];
      rawEntries.push({ weekMin: wd * MINUTES_PER_DAY + dayMin, rate });
    } else {
      for (let wd = 0; wd < 7; wd += 1) {
        rawEntries.push({ weekMin: wd * MINUTES_PER_DAY + dayMin, rate });
      }
    }
  }
  if (rawEntries.length === 0) return null;
  rawEntries.sort((a, b) => a.weekMin - b.weekMin);
  const deduped: WeeklyEntry[] = [];
  for (const entry of rawEntries) {
    if (deduped.length > 0 && deduped[deduped.length - 1].weekMin === entry.weekMin) {
      deduped[deduped.length - 1].rate = entry.rate;
    } else {
      deduped.push({ ...entry });
    }
  }
  return deduped;
}

function buildWeeklyGridFromEntries(entries: WeeklyEntry[]): string[] {
  const grid = new Array<string>(MINUTES_PER_WEEK);
  let activeIdx = entries.length - 1;
  let nextIdx = 0;
  for (let t = 0; t < MINUTES_PER_WEEK; t += 1) {
    while (nextIdx < entries.length && entries[nextIdx].weekMin <= t) {
      activeIdx = nextIdx;
      nextIdx += 1;
    }
    grid[t] = entries[activeIdx].rate;
  }
  return grid;
}

function buildWeeklyGridFromRules(
  defaultRate: string,
  rules: ReadonlyArray<{ days: number[]; startMin: number; endMin: number; rate: string }>,
): string[] {
  const grid = new Array<string>(MINUTES_PER_WEEK).fill(defaultRate);
  for (const rule of rules) {
    if (rule.startMin === rule.endMin) continue;
    for (const day of rule.days) {
      if (rule.endMin > rule.startMin) {
        const base = day * MINUTES_PER_DAY;
        for (let m = rule.startMin; m < rule.endMin; m += 1) {
          grid[base + m] = rule.rate;
        }
      } else {
        const base = day * MINUTES_PER_DAY;
        for (let m = rule.startMin; m < MINUTES_PER_DAY; m += 1) {
          grid[base + m] = rule.rate;
        }
        const nextBase = ((day + 1) % 7) * MINUTES_PER_DAY;
        for (let m = 0; m < rule.endMin; m += 1) {
          grid[nextBase + m] = rule.rate;
        }
      }
    }
  }
  return grid;
}

function compileWeeklyGridToExpression(
  grid: readonly string[],
  allowInherit: boolean,
): string {
  // Check whether the entire week has a single constant rate.
  const first = grid[0];
  let allSame = true;
  for (let t = 1; t < MINUTES_PER_WEEK; t += 1) {
    if (grid[t] !== first) {
      allSame = false;
      break;
    }
  }
  if (allSame) {
    if (first === "off") return allowInherit ? "off" : "";
    return first;
  }

  // Check if all 7 days are identical -> compact daily syntax (no weekday prefix).
  let allDaysIdentical = true;
  for (let d = 1; d < 7 && allDaysIdentical; d += 1) {
    const base = d * MINUTES_PER_DAY;
    for (let m = 0; m < MINUTES_PER_DAY; m += 1) {
      if (grid[base + m] !== grid[m]) {
        allDaysIdentical = false;
        break;
      }
    }
  }
  if (allDaysIdentical) {
    const tokens: string[] = [];
    for (let m = 0; m < MINUTES_PER_DAY; m += 1) {
      const prev = grid[(m + MINUTES_PER_DAY - 1) % MINUTES_PER_DAY];
      if (grid[m] !== prev) {
        tokens.push(`${formatHHMM(m)},${grid[m]}`);
      }
    }
    return tokens.join(" ");
  }

  // Emit transitions in Mon..Sun order for clean human readability.
  const tokens: string[] = [];
  for (const day of [1, 2, 3, 4, 5, 6, 0]) {
    const base = day * MINUTES_PER_DAY;
    for (let m = 0; m < MINUTES_PER_DAY; m += 1) {
      const t = base + m;
      const prev = grid[(t + MINUTES_PER_WEEK - 1) % MINUTES_PER_WEEK];
      if (grid[t] !== prev) {
        tokens.push(`${WEEKDAY_TO_RCLONE[day]}-${formatHHMM(m)},${grid[t]}`);
      }
    }
  }
  return tokens.join(" ");
}

/**
 * Parses an rclone --bwlimit string into a structured BwLimitModel.
 * Guarantees lossless round-trip: exotic multi-day expressions automatically
 * fall back to `mode: "raw"` rather than silently altering the schedule.
 */
export function parseBwLimit(
  raw: string,
  options: { allowInherit?: boolean } = {},
): BwLimitModel {
  const allowInherit = Boolean(options.allowInherit);
  const trimmed = (raw || "").trim();
  const base = createDefaultBwLimitModel(allowInherit);
  if (!trimmed) {
    return base;
  }
  if (validateBwLimitExpression(trimmed) !== null) {
    return { ...base, mode: "raw", raw: trimmed };
  }
  const tokens = trimmed.split(/\s+/);
  if (tokens.length === 1 && !tokens[0].includes(",")) {
    const norm = normalizeSingleRateSpec(tokens[0]);
    if (norm === "off") {
      return { ...base, mode: "unlimited", raw: trimmed };
    }
    const parsedRate = parseRateValue(tokens[0]);
    if (!parsedRate) {
      return { ...base, mode: "raw", raw: trimmed };
    }
    return {
      ...base,
      mode: "fixed",
      fixed: parsedRate,
      raw: trimmed,
    };
  }

  const entries = parseTimetableEntries(trimmed);
  if (!entries) {
    return { ...base, mode: "raw", raw: trimmed };
  }
  const grid = buildWeeklyGridFromEntries(entries);

  // Pick dominant/default rate: prefer "off" if present anywhere in the week.
  const counts = new Map<string, number>();
  for (const r of grid) {
    counts.set(r, (counts.get(r) || 0) + 1);
  }
  if (counts.size === 1) {
    const onlyRate = grid[0];
    if (onlyRate === "off") {
      return { ...base, mode: "unlimited", raw: trimmed };
    }
    const parsedRate = parseRateValue(onlyRate);
    if (!parsedRate) {
      return { ...base, mode: "raw", raw: trimmed };
    }
    return { ...base, mode: "fixed", fixed: parsedRate, raw: trimmed };
  }

  let defaultRateStr = "off";
  if (!counts.has("off")) {
    let bestCount = -1;
    for (const [r, c] of counts.entries()) {
      if (c > bestCount) {
        bestCount = c;
        defaultRateStr = r;
      }
    }
  }
  const defaultRateParsed = parseRateValue(defaultRateStr);
  if (!defaultRateParsed) {
    return { ...base, mode: "raw", raw: trimmed };
  }

  // Locate a circular starting point where grid[start] === defaultRateStr so no
  // non-default interval is artificially split across Sunday 00:00.
  let pivot = -1;
  for (let t = 0; t < MINUTES_PER_WEEK; t += 1) {
    if (grid[t] === defaultRateStr) {
      pivot = t;
      break;
    }
  }
  if (pivot < 0) {
    return { ...base, mode: "raw", raw: trimmed };
  }

  interface RawSlot {
    day: number;
    startMin: number;
    endMin: number;
    rate: string;
  }
  const rawSlots: RawSlot[] = [];
  let offset = 0;
  while (offset < MINUTES_PER_WEEK) {
    const idx = (pivot + offset) % MINUTES_PER_WEEK;
    const rate = grid[idx];
    if (rate === defaultRateStr) {
      offset += 1;
      continue;
    }
    const startOffset = offset;
    while (
      offset < MINUTES_PER_WEEK &&
      grid[(pivot + offset) % MINUTES_PER_WEEK] === rate
    ) {
      offset += 1;
    }
    const duration = offset - startOffset;
    if (duration >= MINUTES_PER_DAY) {
      // Multi-day continuous slot (e.g. Mon 08:00 -> Wed 18:00) stays in raw mode.
      return { ...base, mode: "raw", raw: trimmed };
    }
    const absStart = (pivot + startOffset) % MINUTES_PER_WEEK;
    const absEnd = (pivot + offset) % MINUTES_PER_WEEK;
    rawSlots.push({
      day: Math.floor(absStart / MINUTES_PER_DAY),
      startMin: absStart % MINUTES_PER_DAY,
      endMin: absEnd % MINUTES_PER_DAY,
      rate,
    });
  }

  // Group days that share the exact same (startMin, endMin, rate).
  const grouped = new Map<
    string,
    { days: number[]; startMin: number; endMin: number; rate: string }
  >();
  for (const slot of rawSlots) {
    const key = `${slot.startMin}-${slot.endMin}-${slot.rate}`;
    const existing = grouped.get(key);
    if (existing) {
      if (!existing.days.includes(slot.day)) existing.days.push(slot.day);
    } else {
      grouped.set(key, {
        days: [slot.day],
        startMin: slot.startMin,
        endMin: slot.endMin,
        rate: slot.rate,
      });
    }
  }

  const parsedRules: BwScheduleRule[] = [];
  const verifyRules: Array<{ days: number[]; startMin: number; endMin: number; rate: string }> =
    [];
  for (const item of grouped.values()) {
    const rateObj = parseRateValue(item.rate);
    if (!rateObj) {
      return { ...base, mode: "raw", raw: trimmed };
    }
    const days = normalizeDays(item.days);
    verifyRules.push({
      days,
      startMin: item.startMin,
      endMin: item.endMin,
      rate: serializeRateValue(rateObj) || item.rate,
    });
    parsedRules.push({
      id: nextBwRuleId(),
      days,
      startTime: formatHHMM(item.startMin),
      endTime: formatHHMM(item.endMin),
      rate: rateObj,
    });
  }
  parsedRules.sort((a, b) => {
    const sa = parseHHMM(a.startTime) ?? 0;
    const sb = parseHHMM(b.startTime) ?? 0;
    if (sa !== sb) return sa - sb;
    return (parseHHMM(a.endTime) ?? 0) - (parseHHMM(b.endTime) ?? 0);
  });

  // Verify 100% minute-by-minute equivalence before committing to visual mode.
  const recompiledDefault = serializeRateValue(defaultRateParsed) || defaultRateStr;
  const recompiledGrid = buildWeeklyGridFromRules(recompiledDefault, verifyRules);
  for (let t = 0; t < MINUTES_PER_WEEK; t += 1) {
    if (normalizeSingleRateSpec(recompiledGrid[t]) !== normalizeSingleRateSpec(grid[t])) {
      return { ...base, mode: "raw", raw: trimmed };
    }
  }

  return {
    ...base,
    mode: "schedule",
    defaultRate: defaultRateParsed,
    rules: parsedRules.length > 0 ? parsedRules : [createDefaultScheduleRule()],
    raw: trimmed,
  };
}

/**
 * Serializes a BwLimitModel back to a canonical rclone --bwlimit string.
 * Returns null if the current form state is incomplete/invalid (e.g. blank speed).
 */
export function serializeBwLimit(
  model: BwLimitModel,
  options: { allowInherit?: boolean } = {},
): string | null {
  const allowInherit = Boolean(options.allowInherit);
  switch (model.mode) {
    case "inherit":
      return "";
    case "unlimited":
      return allowInherit ? "off" : "";
    case "raw":
      return model.raw.trim();
    case "fixed": {
      const rate = serializeRateValue(model.fixed);
      if (!rate) return null;
      if (rate === "off") return allowInherit ? "off" : "";
      return rate;
    }
    case "schedule": {
      const defRate = serializeRateValue(model.defaultRate);
      if (!defRate) return null;
      const compiledRules: Array<{
        days: number[];
        startMin: number;
        endMin: number;
        rate: string;
      }> = [];
      for (const rule of model.rules) {
        const days = normalizeDays(rule.days);
        const startMin = parseHHMM(rule.startTime);
        const endMin = parseHHMM(rule.endTime);
        const rate = serializeRateValue(rule.rate);
        if (days.length === 0 || startMin === null || endMin === null || !rate) {
          return null;
        }
        if (startMin === endMin) {
          return null;
        }
        compiledRules.push({ days, startMin, endMin, rate });
      }
      if (compiledRules.length === 0) {
        return defRate === "off" ? (allowInherit ? "off" : "") : defRate;
      }
      const grid = buildWeeklyGridFromRules(defRate, compiledRules);
      return compileWeeklyGridToExpression(grid, allowInherit);
    }
  }
}

export function detectBwModelIssues(
  model: BwLimitModel,
  options: { allowInherit?: boolean } = {},
): { errors: string[]; warnings: string[] } {
  const errors: string[] = [];
  const warnings: string[] = [];

  if (model.mode === "raw") {
    const err = validateBwLimitExpression(model.raw);
    if (err) errors.push(err);
    return { errors, warnings };
  }
  if (model.mode === "fixed") {
    if (!serializeRateValue(model.fixed)) {
      errors.push("请输入大于 0 的限速数值");
    }
    return { errors, warnings };
  }
  if (model.mode === "schedule") {
    if (!serializeRateValue(model.defaultRate)) {
      errors.push("其余时段默认限速请输入大于 0 的数值");
    }
    if (model.rules.length === 0) {
      errors.push("请至少添加一条定时限速时段");
    }
    const occupancy = new Int16Array(MINUTES_PER_WEEK).fill(-1);
    let hasOverlap = false;
    model.rules.forEach((rule, idx) => {
      const n = idx + 1;
      const days = normalizeDays(rule.days);
      if (days.length === 0) {
        errors.push(`时段 #${n} 请至少选择一天`);
      }
      const startMin = parseHHMM(rule.startTime);
      const endMin = parseHHMM(rule.endTime);
      if (startMin === null || endMin === null) {
        errors.push(`时段 #${n} 时间格式无效（应为 HH:MM）`);
      } else if (startMin === endMin) {
        errors.push(`时段 #${n} 的开始时间与结束时间不能相同`);
      }
      if (!serializeRateValue(rule.rate)) {
        errors.push(`时段 #${n} 请输入大于 0 的限速数值`);
      }
      if (days.length > 0 && startMin !== null && endMin !== null && startMin !== endMin) {
        const mark = (t: number) => {
          if (occupancy[t] !== -1 && occupancy[t] !== idx) {
            hasOverlap = true;
          }
          occupancy[t] = idx;
        };
        for (const d of days) {
          if (endMin > startMin) {
            for (let m = startMin; m < endMin; m += 1) mark(d * MINUTES_PER_DAY + m);
          } else {
            for (let m = startMin; m < MINUTES_PER_DAY; m += 1) mark(d * MINUTES_PER_DAY + m);
            const nd = (d + 1) % 7;
            for (let m = 0; m < endMin; m += 1) mark(nd * MINUTES_PER_DAY + m);
          }
        }
      }
    });
    if (hasOverlap) {
      warnings.push("存在重叠的限速时段，重叠部分将以后一条规则为准");
    }
    if (errors.length === 0) {
      const expr = serializeBwLimit(model, options);
      if (expr !== null) {
        const exprErr = validateBwLimitExpression(expr);
        if (exprErr) errors.push(exprErr);
      }
    }
  }
  return { errors, warnings };
}

/**
 * Resolves the active speed limit and next transition time at `now`
 * matching rclone's BwTimetable and backend ResolveBwLimitAt semantics.
 */
export function resolveBwLimitAt(raw: string, now: Date = new Date()): BwResolvedSlot {
  const trimmed = (raw || "").trim();
  if (!trimmed) {
    return { activeRate: "off", isUnlimited: true, hasSchedule: false };
  }
  const entries = parseTimetableEntries(trimmed);
  if (!entries) {
    const rate = normalizeSingleRateSpec(trimmed);
    return {
      activeRate: rate,
      isUnlimited: rate === "off",
      hasSchedule: false,
    };
  }
  const nowWeekMin = now.getDay() * MINUTES_PER_DAY + now.getHours() * 60 + now.getMinutes();
  let activeIdx = entries.length - 1;
  for (let i = entries.length - 1; i >= 0; i -= 1) {
    if (entries[i].weekMin <= nowWeekMin) {
      activeIdx = i;
      break;
    }
  }
  const activeRate = entries[activeIdx].rate;
  let nextChangeAt: Date | undefined;
  let nextRate: string | undefined;

  for (let step = 1; step <= entries.length; step += 1) {
    const idx = (activeIdx + step) % entries.length;
    if (entries[idx].rate.toLowerCase() === activeRate.toLowerCase()) continue;
    const targetWeekMin = entries[idx].weekMin;
    const targetWD = Math.floor(targetWeekMin / MINUTES_PER_DAY);
    const targetDayMin = targetWeekMin % MINUTES_PER_DAY;
    const daysAhead = (targetWD - now.getDay() + 7) % 7;
    let candidate = new Date(
      now.getFullYear(),
      now.getMonth(),
      now.getDate() + daysAhead,
      Math.floor(targetDayMin / 60),
      targetDayMin % 60,
      0,
      0,
    );
    if (candidate.getTime() <= now.getTime()) {
      candidate = new Date(
        now.getFullYear(),
        now.getMonth(),
        now.getDate() + daysAhead + 7,
        Math.floor(targetDayMin / 60),
        targetDayMin % 60,
        0,
        0,
      );
    }
    nextChangeAt = candidate;
    nextRate = entries[idx].rate;
    break;
  }

  return {
    activeRate,
    isUnlimited: activeRate === "off",
    hasSchedule: true,
    nextChangeAt,
    nextRate,
  };
}

/**
 * Returns contiguous 24-hour segments for a given weekday (0=Sun..6=Sat)
 * to render a visual timeline bar.
 */
export function getDayTimelineSegments(raw: string, weekday: number): BwTimelineSegment[] {
  const trimmed = (raw || "").trim();
  if (!trimmed) {
    return [{ startMin: 0, endMin: MINUTES_PER_DAY, rate: "off", isUnlimited: true }];
  }
  const entries = parseTimetableEntries(trimmed);
  if (!entries) {
    const rate = normalizeSingleRateSpec(trimmed);
    return [
      {
        startMin: 0,
        endMin: MINUTES_PER_DAY,
        rate,
        isUnlimited: rate === "off",
      },
    ];
  }
  const grid = buildWeeklyGridFromEntries(entries);
  const wd = ((weekday % 7) + 7) % 7;
  const base = wd * MINUTES_PER_DAY;
  const segments: BwTimelineSegment[] = [];
  let segStart = 0;
  let segRate = grid[base];
  for (let m = 1; m < MINUTES_PER_DAY; m += 1) {
    if (grid[base + m] !== segRate) {
      segments.push({
        startMin: segStart,
        endMin: m,
        rate: segRate,
        isUnlimited: segRate === "off",
      });
      segStart = m;
      segRate = grid[base + m];
    }
  }
  segments.push({
    startMin: segStart,
    endMin: MINUTES_PER_DAY,
    rate: segRate,
    isUnlimited: segRate === "off",
  });
  return segments;
}

export function hasDifferentWeekdays(raw: string): boolean {
  const entries = parseTimetableEntries(raw);
  if (!entries) return false;
  const grid = buildWeeklyGridFromEntries(entries);
  for (let d = 1; d < 7; d += 1) {
    const base = d * MINUTES_PER_DAY;
    for (let m = 0; m < MINUTES_PER_DAY; m += 1) {
      if (grid[base + m] !== grid[m]) return true;
    }
  }
  return false;
}

function formatSingleAtomHuman(atom: string): string {
  const trimmed = atom.trim();
  if (!trimmed || trimmed === "0" || /^off$/i.test(trimmed)) return "不限速";
  const parsed = parseSinglePartToUnit(trimmed);
  if (!parsed) return trimmed;
  if (parsed.unlimited) return "不限速";
  const unitLabel = bwSpeedUnits.find((u) => u.value === parsed.unit)?.label || parsed.unit;
  return `${parsed.value} ${unitLabel}`;
}

export interface BwScheduleRuleSummary {
  daysLabel: string;
  timeRange: string;
  crossMidnight: boolean;
  rateLabel: string;
  rateCompact: string;
}

export interface BwLimitDisplayInfo {
  configured: boolean;
  hasSchedule: boolean;
  isCurrentlyLimited: boolean;
  activeRate: string;
  activeRateHuman: string;
  activeRateCompact: string;
  chipLabel: string;
  kpiLabel: string;
  liveOptionLabel: string;
  tooltipSummary: string;
  nextChangeLabel?: string;
  rules: BwScheduleRuleSummary[];
  defaultRateHuman?: string;
  isRawFallback: boolean;
  raw: string;
}

export function formatBwRateHuman(rate: string): string {
  const trimmed = (rate || "").trim();
  if (!trimmed || trimmed === "0" || /^off$/i.test(trimmed)) return "不限速";
  const colonIdx = trimmed.indexOf(":");
  if (colonIdx >= 0) {
    const up = trimmed.slice(0, colonIdx).trim();
    const down = trimmed.slice(colonIdx + 1).trim();
    const upOff = !up || up === "0" || /^off$/i.test(up);
    const downOff = !down || down === "0" || /^off$/i.test(down);
    if (upOff && downOff) return "不限速";
    const upText = upOff ? "不限" : formatSingleAtomHuman(up);
    const downText = downOff ? "不限" : formatSingleAtomHuman(down);
    return `上传 ${upText} / 下载 ${downText}`;
  }
  return formatSingleAtomHuman(trimmed);
}

/**
 * Concise human-readable rate string for badges, KPI footers, and chips.
 * Omits the unlimited direction in asymmetric single-direction limits (e.g. "5.5M:off" -> "上传 5.5 MB/s").
 */
export function formatBwRateCompact(rate: string): string {
  const trimmed = (rate || "").trim();
  if (!trimmed || trimmed === "0" || /^off$/i.test(trimmed)) return "不限速";
  const colonIdx = trimmed.indexOf(":");
  if (colonIdx >= 0) {
    const up = trimmed.slice(0, colonIdx).trim();
    const down = trimmed.slice(colonIdx + 1).trim();
    const upOff = !up || up === "0" || /^off$/i.test(up);
    const downOff = !down || down === "0" || /^off$/i.test(down);
    if (upOff && downOff) return "不限速";
    if (!upOff && downOff) return `上传 ${formatSingleAtomHuman(up)}`;
    if (upOff && !downOff) return `下载 ${formatSingleAtomHuman(down)}`;
    return `↑${formatSingleAtomHuman(up)} ↓${formatSingleAtomHuman(down)}`;
  }
  return formatSingleAtomHuman(trimmed);
}

/**
 * Ultra-short segment label for 24h timeline bar cells (e.g. "5.5M:off" -> "↑5.5M").
 */
export function formatBwTimelineSegLabel(rate: string): string {
  const trimmed = (rate || "").trim();
  if (!trimmed || trimmed === "0" || /^off$/i.test(trimmed)) return "不限";
  const colonIdx = trimmed.indexOf(":");
  if (colonIdx >= 0) {
    const up = trimmed.slice(0, colonIdx).trim();
    const down = trimmed.slice(colonIdx + 1).trim();
    const upOff = !up || up === "0" || /^off$/i.test(up);
    const downOff = !down || down === "0" || /^off$/i.test(down);
    if (upOff && downOff) return "不限";
    if (!upOff && downOff) return `↑${up}`;
    if (upOff && !downOff) return `↓${down}`;
    return `↑${up}/↓${down}`;
  }
  return trimmed;
}

/**
 * Produces structured, human-readable display metadata for any rclone --bwlimit expression
 * (unlimited, fixed symmetric/asymmetric, or weekly/daily timetable).
 */
export function describeBwLimit(
  raw: string | null | undefined,
  options: { now?: Date } = {},
): BwLimitDisplayInfo {
  const trimmed = (raw || "").trim();
  const now = options.now ?? new Date();
  if (!trimmed) {
    return {
      configured: false,
      hasSchedule: false,
      isCurrentlyLimited: false,
      activeRate: "off",
      activeRateHuman: "不限速",
      activeRateCompact: "不限速",
      chipLabel: "全局不限速",
      kpiLabel: "",
      liveOptionLabel: "不限速",
      tooltipSummary: "全局不限速",
      rules: [],
      isRawFallback: false,
      raw: "",
    };
  }

  const resolved = resolveBwLimitAt(trimmed, now);
  const activeRateHuman = formatBwRateHuman(resolved.activeRate);
  const activeRateCompact = formatBwRateCompact(resolved.activeRate);
  const isCurrentlyLimited = !resolved.isUnlimited;

  if (!resolved.hasSchedule) {
    if (!isCurrentlyLimited) {
      return {
        configured: false,
        hasSchedule: false,
        isCurrentlyLimited: false,
        activeRate: "off",
        activeRateHuman: "不限速",
        activeRateCompact: "不限速",
        chipLabel: "全局不限速",
        kpiLabel: "",
        liveOptionLabel: "不限速",
        tooltipSummary: "全局不限速",
        rules: [],
        isRawFallback: false,
        raw: trimmed,
      };
    }
    const isDirectedSingle =
      activeRateCompact.startsWith("上传 ") || activeRateCompact.startsWith("下载 ");
    const chipLabel = isDirectedSingle
      ? `全局限速 · ${activeRateCompact}`
      : `全局限速 ${activeRateCompact}`;
    const kpiLabel = isDirectedSingle
      ? `${activeRateCompact.slice(0, 2)}限速 ${activeRateCompact.slice(3)}`
      : `限速 ${activeRateCompact}`;
    const liveOptionLabel =
      isDirectedSingle || activeRateCompact.startsWith("↑")
        ? activeRateCompact
        : `限速 ${activeRateCompact}`;
    return {
      configured: true,
      hasSchedule: false,
      isCurrentlyLimited: true,
      activeRate: resolved.activeRate,
      activeRateHuman,
      activeRateCompact,
      chipLabel,
      kpiLabel,
      liveOptionLabel,
      tooltipSummary: `全局限速：${activeRateHuman}`,
      rules: [],
      isRawFallback: false,
      raw: trimmed,
    };
  }

  const parsed = parseBwLimit(trimmed, { allowInherit: false });
  const rules: BwScheduleRuleSummary[] = [];
  let defaultRateHuman: string | undefined;
  let isRawFallback = false;

  if (parsed.mode === "schedule") {
    for (const rule of parsed.rules) {
      const rateToken = serializeRateValue(rule.rate) || "off";
      const startMin = parseHHMM(rule.startTime) ?? 0;
      const endMin = parseHHMM(rule.endTime) ?? 0;
      rules.push({
        daysLabel: formatDaysLabel(rule.days),
        timeRange: `${rule.startTime}–${rule.endTime}`,
        crossMidnight: endMin < startMin,
        rateLabel: formatBwRateHuman(rateToken),
        rateCompact: formatBwRateCompact(rateToken),
      });
    }
    const defaultToken = serializeRateValue(parsed.defaultRate) || "off";
    defaultRateHuman = formatBwRateHuman(defaultToken);
  } else if (parsed.mode === "raw") {
    isRawFallback = true;
  }

  const nextChangeLabel =
    resolved.nextChangeAt && resolved.nextRate
      ? `${formatNextChangeTime(resolved.nextChangeAt, now)} → ${formatBwRateCompact(resolved.nextRate)}`
      : undefined;

  const chipLabel = isCurrentlyLimited
    ? `定时限速 · ${activeRateCompact}`
    : "定时限速 · 当前不限速";
  const kpiLabel = isCurrentlyLimited
    ? `定时 · ${activeRateCompact}`
    : "定时 · 当前不限";
  const liveOptionLabel = isCurrentlyLimited
    ? `定时 · ${activeRateCompact}`
    : "定时 · 当前不限";
  const tooltipParts = [
    `定时限速：${isCurrentlyLimited ? `当前 ${activeRateHuman}` : "当前不限速"}`,
  ];
  if (nextChangeLabel) {
    tooltipParts.push(`下次切换 ${nextChangeLabel}`);
  }

  return {
    configured: true,
    hasSchedule: true,
    isCurrentlyLimited,
    activeRate: resolved.activeRate,
    activeRateHuman,
    activeRateCompact,
    chipLabel,
    kpiLabel,
    liveOptionLabel,
    tooltipSummary: tooltipParts.join(" · "),
    nextChangeLabel,
    rules,
    defaultRateHuman,
    isRawFallback,
    raw: trimmed,
  };
}

/**
 * Estimates approximate Mbps for a single numeric value + unit (bytes/sec -> bits/sec).
 */
export function estimateMbps(value: string, unit: BwSpeedUnit): string {
  const num = Number(value);
  if (!Number.isFinite(num) || num <= 0) return "";
  let mbps = 0;
  if (unit === "K") mbps = (num * 8) / 1024;
  else if (unit === "M") mbps = num * 8;
  else if (unit === "G") mbps = num * 8 * 1024;
  if (mbps < 0.1) return `≈ ${Math.round(mbps * 1000)} Kbps`;
  if (mbps >= 1000) return `≈ ${(mbps / 1000).toFixed(1).replace(/\.0$/, "")} Gbps`;
  return `≈ ${mbps >= 10 ? Math.round(mbps) : mbps.toFixed(1).replace(/\.0$/, "")} Mbps`;
}

export function formatNextChangeTime(nextChange: Date, now: Date = new Date()): string {
  const hhmm = formatHHMM(nextChange.getHours() * 60 + nextChange.getMinutes());
  const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const targetStart = new Date(
    nextChange.getFullYear(),
    nextChange.getMonth(),
    nextChange.getDate(),
  ).getTime();
  const dayDiff = Math.round((targetStart - todayStart) / 86_400_000);
  if (dayDiff === 0) return `今天 ${hhmm}`;
  if (dayDiff === 1) return `明天 ${hhmm}`;
  const wdLabel = bwWeekdayOptions.find((w) => w.value === nextChange.getDay())?.label || "";
  return `${wdLabel} ${hhmm}`;
}

/**
 * Produces a concise Chinese label for task cards, badges, and global default hints.
 */
export function formatBwLimitSummary(
  raw: string | null | undefined,
  options: { allowInherit?: boolean; now?: Date } = {},
): string {
  const trimmed = (raw || "").trim();
  if (!trimmed) {
    return options.allowInherit ? "跟随全局设置" : "不限速";
  }
  const resolved = resolveBwLimitAt(trimmed, options.now);
  if (!resolved.hasSchedule) {
    if (resolved.isUnlimited) {
      return options.allowInherit ? "不限速（覆盖全局）" : "不限速";
    }
    return formatBwRateHuman(resolved.activeRate);
  }
  const currentLabel = resolved.isUnlimited
    ? "当前不限速"
    : `当前 ${formatBwRateHuman(resolved.activeRate)}`;
  return `定时限速（${currentLabel}）`;
}
