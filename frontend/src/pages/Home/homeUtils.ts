import type { JobItem, TaskItem, TreeNode } from "../../types";
import { formatDisplayPath } from "../../lib/syncPathPreview";

type ScheduleValues = {
  isCron?: number;
  interval?: number;
  second?: string | null;
  minute?: string | null;
  hour?: string | null;
  day?: string | null;
  month?: string | null;
  day_of_week?: string | null;
};

// ---- Constants ----

export const taskRecordStatusNames: Record<number, string> = {
  0: "等待中",
  1: "运行中",
  2: "成功",
  3: "部分失败",
  4: "已停止",
  5: "超时",
  6: "系统错误",
  7: "失败",
  8: "无需同步",
};
// Mirrors backend/internal/service/job_task_item.go taskStatus* values. Keep in
// sync with tests/homeUtils.test.ts: a wrong label here (e.g. showing a timeout
// as "will retry") sends users chasing the wrong problem.
export const taskItemStatusNames: Record<number, string> = {
  0: "等待中",
  1: "进行中",
  2: "成功",
  3: "部分失败",
  4: "已停止",
  5: "超时",
  6: "系统错误",
  7: "已失败",
  10: "重试中",
};
export const taskTypeNames: Record<number, string> = {
  0: "复制",
  1: "删除",
  2: "移动",
  3: "移动（校验后删源）",
};
export const methodOptions = [
  {
    name: "增量同步",
    description:
      "复制源目录中目标端不存在或内容变化的文件，不会删除目标端多余文件，适合持续备份。",
  },
  {
    name: "全量同步",
    description:
      "目标目录与源目录保持一致，完成新增、更新与同端移动后直接清理目标端多余文件（开启回收站时优先移入回收站）。",
  },
  {
    name: "移动",
    description:
      "复制文件并校验目标大小、源文件未变后删除源文件；仅支持一个目标目录，保留空源目录。需刷新、复制和删除权限，请勿使用指向同一数据的别名目录或同时修改文件。",
  },
  {
    name: "双向同步",
    description:
      "在两个目录之间双向同步新增、修改与删除。首次运行建立同步基线，后续根据两端变更同步，冲突可在任务概览中处理。",
  },
];
export const methodNames = methodOptions.map((method) => method.name);

export const compareModeOptions = [
  { value: 0, label: "默认（大小 + 修改时间 / MD5）" },
  { value: 1, label: "仅按文件大小（忽略修改时间）" },
  { value: 2, label: "大小 + MD5（忽略修改时间，适合跨网盘）" },
] as const;

export const conflictPolicyOptions = [
  { value: 0, label: "覆盖目标文件（开启回收站时先移入回收站）" },
  { value: 1, label: "跳过已存在的目标文件" },
  { value: 2, label: "保留双方（旧目标文件重命名为 .conflict-时间戳）" },
] as const;

export const filterModeOptions = [
  { value: 0, label: "排除模式（黑名单，默认）" },
  { value: 1, label: "仅同步匹配文件（白名单）" },
] as const;

export const notifyModeOptions = [
  { value: 0, label: "跟随全局通知渠道" },
  { value: 1, label: "仅发送到指定通知渠道" },
  { value: 2, label: "关闭此任务通知" },
] as const;

export const compareModeLabel = (mode?: number | null) =>
  compareModeOptions.find((item) => item.value === Number(mode ?? 0))?.label ??
  compareModeOptions[0].label;

export const conflictPolicyLabel = (policy?: number | null) =>
  conflictPolicyOptions.find((item) => item.value === Number(policy ?? 0))?.label ??
  conflictPolicyOptions[0].label;
export const methodLabel = (method?: number | null) => {
  if (method === null || method === undefined || method === ("" as unknown)) {
    return "—";
  }
  const index = Number(method);
  if (!Number.isInteger(index) || index < 0 || index >= methodNames.length) {
    return "—";
  }
  return methodNames[index];
};
export const cronTypeNames = ["固定间隔", "定时计划", "仅手动", "持续同步"];
export const cronFields = [
  { name: "second", label: "秒", placeholder: "0" },
  { name: "minute", label: "分", placeholder: "*" },
  { name: "hour", label: "时", placeholder: "*" },
  { name: "day", label: "日", placeholder: "*" },
  { name: "month", label: "月", placeholder: "*" },
  { name: "day_of_week", label: "周", placeholder: "*" },
];
export const defaultCronFields = {
  second: "0",
  minute: "0",
  hour: "2",
  day: "*",
  month: "*",
  day_of_week: "*",
};

export const defaultExclude = `# macOS
.DS_Store
._*
.Spotlight-V100/
.Trashes/
.fseventsd/
.DocumentRevisions-V100/
.TemporaryItems/

# Windows
Thumbs.db
Desktop.ini
$RECYCLE.BIN/
System Volume Information/

# NAS / Linux
lost+found/
@eaDir/
#recycle/
@Recycle/
.Recycle/
.recycle/
.Trash/
.Trash-1000/

# 临时文件
*.tmp
*.temp
*.part
*.crdownload
*.download
*.partial
*.swp
*.swo
*.swn

# 锁文件
~$*
.~lock.*#
.#*`;

// ---- Utility Functions ----

const cronValue = (value?: string | null, fallback = "*") => {
  const normalized = String(value ?? "").trim();
  return normalized || fallback;
};

const isPlainCronNumber = (value: string) => /^\d+$/.test(value);

const isCronIntInRange = (value: string, min: number, max: number) => {
  if (!isPlainCronNumber(value)) return false;
  const num = Number(value);
  return Number.isInteger(num) && num >= min && num <= max;
};

const isValidCronWeekdayList = (value: string) => {
  if (!/^[0-6](?:-[0-6])?(?:,[0-6](?:-[0-6])?)*$/.test(value)) {
    return false;
  }
  return value.split(",").every((part) => {
    const [start, end] = part.split("-");
    return end === undefined || Number(start) <= Number(end);
  });
};

/**
 * Only plain numbers within valid clock bounds can be rendered as a clock.
 * A step/range/list or out-of-range expression falls back to the raw
 * expression in describeCronPlan instead.
 */
const formatTime = (
  hour?: string | null,
  minute?: string | null,
  second?: string | null,
) => {
  const h = cronValue(hour);
  const m = cronValue(minute);
  const s = cronValue(second, "0");
  if (
    isCronIntInRange(h, 0, 23) &&
    isCronIntInRange(m, 0, 59) &&
    isCronIntInRange(s, 0, 59)
  ) {
    return `${String(Number(h)).padStart(2, "0")}:${String(Number(m)).padStart(2, "0")}:${String(Number(s)).padStart(2, "0")}`;
  }
  return `${h}:${m}:${s}`;
};

const weekdayNames = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];

export const formatCronDayOfWeek = (value?: string | null) => {
  const normalized = cronValue(value);
  if (!isValidCronWeekdayList(normalized)) {
    return normalized;
  }

  return normalized
    .split(",")
    .map((part) => {
      const [start, end] = part.split("-");
      const startLabel = weekdayNames[Number(start)];
      if (end === undefined) return startLabel;
      return `${startLabel}至${weekdayNames[Number(end)]}`;
    })
    .join("、");
};

const describeCronPlan = (values?: ScheduleValues | null) => {
  const safe = values || {};
  const second = cronValue(safe.second, "0");
  const minute = cronValue(safe.minute);
  const hour = cronValue(safe.hour);
  const day = cronValue(safe.day);
  const month = cronValue(safe.month);
  const dayOfWeek = cronValue(safe.day_of_week);
  const dayOfWeekLabel = formatCronDayOfWeek(dayOfWeek);
  const time = formatTime(hour, minute, second);
  // "每天 02:00:00" is only truthful when 时/分/秒 are single in-range numbers.
  const renderableClock =
    isCronIntInRange(hour, 0, 23) &&
    isCronIntInRange(minute, 0, 59) &&
    isCronIntInRange(second, 0, 59);

  if (
    renderableClock &&
    day === "*" &&
    month === "*" &&
    dayOfWeek === "*"
  ) {
    return `每天 ${time} 执行`;
  }
  if (
    renderableClock &&
    isCronIntInRange(day, 1, 31) &&
    month === "*" &&
    dayOfWeek === "*"
  ) {
    return `每月 ${Number(day)} 日 ${time} 执行`;
  }
  if (
    renderableClock &&
    day === "*" &&
    month === "*" &&
    isValidCronWeekdayList(dayOfWeek)
  ) {
    return `每周 ${dayOfWeekLabel} 的 ${time} 执行`;
  }
  return `按 Cron 表达式 ${[second, minute, hour, day, month, dayOfWeek].join(" ")} 执行`;
};

export const formatSchedulePlan = (values?: ScheduleValues | null) => {
  if (!values || values.isCron === 3) {
    return "持续监控本地变化、定期检测远端，并自动补齐未完成变更";
  }
  if (values.isCron === 0) {
    const mins = Number(values.interval);
    return `每 ${Number.isFinite(mins) && mins >= 0 ? mins : 0} 分钟执行一次`;
  }
  if (values.isCron === 1) return describeCronPlan(values);
  return "持续监控本地变化、定期检测远端，并自动补齐未完成变更";
};

export const parseJobPathList = (value: unknown): string[] => {
  if (Array.isArray(value)) {
    return value.map((item) => String(item).trim()).filter(Boolean);
  }
  const raw = String(value ?? "").trim();
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) {
      return parsed.map((item) => String(item).trim()).filter(Boolean);
    }
  } catch {
    /* plain single path */
  }
  return [raw];
};

export const normalizeTreePath = (value: unknown): string => {
  const raw = String(value ?? "").trim();
  if (!raw || raw === "/") return "/";
  return `/${raw.replace(/^\/+/, "").replace(/\/+$/, "")}`.replace(/\/+/g, "/");
};

export const buildPathTreeData = (paths: string[]): TreeNode[] => {
  const root: TreeNode = { title: "/", value: "/", key: "/", children: [] };
  const normalizedPaths = [
    ...new Set(paths.map(normalizeTreePath).filter(Boolean)),
  ];

  normalizedPaths.forEach((path) => {
    if (path === "/") return;
    const segments = path.split("/").filter(Boolean);
    let currentPath = "";
    let currentChildren = root.children || [];
    root.children = currentChildren;

    segments.forEach((segment) => {
      currentPath = `${currentPath}/${segment}`;
      let node = currentChildren.find((item) => item.value === currentPath);
      if (!node) {
        node = {
          title: segment,
          value: currentPath,
          key: currentPath,
          isLeaf: false,
          children: [],
        };
        currentChildren.push(node);
      }
      node.children = node.children || [];
      currentChildren = node.children;
    });
  });

  return [root];
};

export const formatSize = (bytes: number) => {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = bytes;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex++;
  }
  return `${size.toFixed(unitIndex === 0 ? 0 : 2)} ${units[unitIndex]}`;
};

export const getJobName = (job: JobItem) => job.remark || `同步任务 #${job.id}`;

export const formatNextRun = (job?: JobItem | null) => {
  if (!job) return "—";
  return job.enable === 1 ? "持续同步中" : "持续同步已暂停";
};

export const formatSchedule = (job?: JobItem | null) => {
  if (!job) return "—";
  return "持续同步";
};

export function formatDuration(seconds: number): string {
  if (seconds < 0) seconds = 0;
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const secs = Math.floor(seconds % 60);
  const parts: string[] = [];
  if (days > 0) parts.push(`${days}天`);
  if (hours > 0) parts.push(`${hours}小时`);
  if (minutes > 0) parts.push(`${minutes}分`);
  if (secs > 0 || parts.length === 0) parts.push(`${secs}秒`);
  return parts.join(" ");
}

export function getTaskDisplayName(task: TaskItem): string {
  if (task.fileName) return task.fileName;
  const path = task.dstPath || task.srcPath || "";
  if (!path) return "--";
  const cleanPath = formatDisplayPath(path).replace(/\/+$/, "");
  return cleanPath.split("/").pop() || cleanPath;
}

export function parseIntervalInput(value: string): number {
  const trimmed = value.trim();
  if (!trimmed) return 0;
  if (/^\d+$/.test(trimmed)) return Number(trimmed);
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) && !Number.isInteger(parsed) ? parsed : 0;
}

export function normalizeDateRange(value: unknown): Date[] {
  if (!Array.isArray(value)) return [];
  const parsed = value.slice(0, 2).map((item) => {
    if (item === null || item === undefined || item === "") return null;
    const date =
      item instanceof Date
        ? new Date(item.getTime())
        : new Date(item as string | number);
    return Number.isFinite(date.getTime()) ? date : null;
  });
  return parsed.length === 2 && parsed.every((d): d is Date => d !== null)
    ? parsed
    : [];
}

export function normalizeSettingFieldValue(
  value: unknown,
  isSwitch = false,
): string {
  if (value === undefined || value === null || value === "") {
    return isSwitch ? "0" : "";
  }
  return String(value);
}
