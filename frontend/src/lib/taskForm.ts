import type { JobItem } from "../types";
import {
  defaultCronFields,
  defaultExclude,
  parseJobPathList,
} from "../pages/Home/homeUtils";
import {
  fileSizeToBytes,
  splitBytesToFileSize,
} from "../pages/Home/fileSizeUnits";

export function defaultJobForm(alistId?: number) {
  return {
    id: undefined as number | undefined,
    alistId,
    remark: "",
    srcPath: [] as string[],
    dstPath: [] as string[],
    method: 0,
    isCron: 1,
    interval: 1440,
    enable: true,
    useCacheS: false,
    useCacheT: false,
    scanIntervalS: 0,
    scanIntervalT: 0,
    minFileSize: 0,
    minFileSizeUnit: "MB",
    maxFileSize: 0,
    maxFileSizeUnit: "MB",
    exclude: defaultExclude,
    ...defaultCronFields,
  };
}
export type JobForm = ReturnType<typeof defaultJobForm>;
export function selectJob<T extends { id: number }>(
  jobs: T[],
  selectedId: number | null,
) {
  return selectedId === null
    ? jobs[0]
    : jobs.find((job) => job.id === selectedId);
}
export function jobToForm(job: JobItem): JobForm {
  const min = splitBytesToFileSize(job.minFileSize),
    max = splitBytesToFileSize(job.maxFileSize);
  return {
    ...defaultJobForm(job.alistId),
    ...job,
    srcPath: parseJobPathList(job.srcPath),
    dstPath: parseJobPathList(job.dstPath),
    remark: job.remark || "",
    enable: job.enable === 1,
    useCacheS: Boolean(job.useCacheS),
    useCacheT: Boolean(job.useCacheT),
    scanIntervalS: job.scanIntervalS ?? 0,
    scanIntervalT: job.scanIntervalT ?? 0,
    minFileSize: min.value,
    minFileSizeUnit: min.unit,
    maxFileSize: max.value,
    maxFileSizeUnit: max.unit,
    exclude: job.exclude ?? defaultExclude,
    second: job.second || "0",
    minute: job.minute || "0",
    hour: job.hour || "2",
    day: job.day || "*",
    month: job.month || "*",
    day_of_week: job.day_of_week || "*",
  };
}
export function buildJobPayload(form: JobForm) {
  const { minFileSizeUnit, maxFileSizeUnit, ...data } = form;
  return {
    ...data,
    remark: form.remark.trim() || null,
    exclude: form.exclude.trim() || null,
    enable: form.isCron === 2 ? 1 : Number(form.enable),
    useCacheS: Number(form.useCacheS),
    useCacheT: Number(form.useCacheT),
    minFileSize: fileSizeToBytes(form.minFileSize, minFileSizeUnit),
    maxFileSize: fileSizeToBytes(form.maxFileSize, maxFileSizeUnit),
  };
}
const ranges = {
  second: [0, 59, "秒"],
  minute: [0, 59, "分"],
  hour: [0, 23, "时"],
  day: [1, 31, "日"],
  month: [1, 12, "月"],
  day_of_week: [0, 6, "周"],
} as const;
export function validateJobFormStep(form: JobForm, step: number) {
  switch (step) {
    case 0:
      if (!form.alistId) return "请选择存储引擎";
      if (!form.srcPath.length) return "请选择源目录";
      if (!form.dstPath.length) return "请选择目标目录";
      return "";
    case 1:
      if (
        form.isCron === 0 &&
        (!Number.isInteger(form.interval) || form.interval < 1)
      ) {
        return "执行间隔必须是大于 0 的整数";
      }
      if (form.isCron === 1) {
        for (const [key, [low, high, label]] of Object.entries(ranges)) {
          const value = form[key as keyof typeof ranges];
          if (
            !value ||
            !value.split(",").every((part) => {
              if (part.trim() === "*") return true;
              const match = part
                .trim()
                .match(/^(\*|\d+)(?:-(\d+))?(?:\/(\d+))?$/);
              if (!match) return false;
              const start = match[1] === "*" ? low : Number(match[1]),
                end = match[2] ? Number(match[2]) : start;
              return (
                start >= low &&
                end <= high &&
                start <= end &&
                (!match[3] || (Number(match[3]) > 0 && Number(match[3]) <= high))
              );
            })
          ) {
            return `Cron「${label}」字段无效，范围 ${low}-${high}`;
          }
        }
      }
      return "";
    case 2: {
      if (
        ![form.minFileSize, form.maxFileSize].every(
          (n) => Number.isFinite(n) && n >= 0,
        )
      ) {
        return "文件大小必须是有效的非负数";
      }
      const min = fileSizeToBytes(form.minFileSize, form.minFileSizeUnit),
        max = fileSizeToBytes(form.maxFileSize, form.maxFileSizeUnit);
      if (max > 0 && min > max) return "最大文件大小必须大于等于最小文件大小";
      return "";
    }
    default:
      return "";
  }
}
export function validateJobForm(form: JobForm) {
  for (const step of [0, 1, 2]) {
    const err = validateJobFormStep(form, step);
    if (err) return err;
  }
  return "";
}
