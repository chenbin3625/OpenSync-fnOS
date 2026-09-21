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
const supportedJobMethods = new Set([0, 1]);

function normalizeJobMethod(method: unknown) {
  return supportedJobMethods.has(Number(method)) ? Number(method) : 0;
}

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
    method: normalizeJobMethod(job.method),
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
      if (!form.remark.trim()) return "请输入任务名称";
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

// ---- 文件过滤 ----

export const fileTypeFilterGroups = [
  {
    key: "system",
    label: "系统文件",
    patterns: [
      ".DS_Store", "._*", "Thumbs.db", "Desktop.ini",
    ],
  },
  {
    key: "audio",
    label: "音乐",
    patterns: [
      "*.mp3", "*.flac", "*.wav", "*.aac", "*.m4a",
      "*.ogg", "*.wma", "*.ape", "*.alac", "*.aiff",
      "*.opus", "*.mid", "*.midi",
    ],
  },
  {
    key: "video",
    label: "视频",
    patterns: [
      "*.mp4", "*.mkv", "*.avi", "*.mov", "*.wmv",
      "*.flv", "*.webm", "*.m4v", "*.rmvb", "*.rm",
      "*.ts", "*.m2ts", "*.vob", "*.3gp", "*.mpg",
      "*.mpeg",
    ],
  },
  {
    key: "image",
    label: "图像",
    patterns: [
      "*.jpg", "*.jpeg", "*.png", "*.gif", "*.bmp",
      "*.webp", "*.heic", "*.heif", "*.tif", "*.tiff",
      "*.raw", "*.svg", "*.ico", "*.psd", "*.cr2",
      "*.nef", "*.arw", "*.dng",
    ],
  },
  {
    key: "document",
    label: "文档",
    patterns: [
      "*.doc", "*.docx", "*.xls", "*.xlsx", "*.ppt",
      "*.pptx", "*.pdf", "*.txt", "*.rtf", "*.csv",
      "*.md", "*.odt", "*.ods", "*.odp", "*.epub",
      "*.pages", "*.numbers", "*.keynote", "*.json",
      "*.xml", "*.yaml", "*.yml",
    ],
  },
  {
    key: "archive",
    label: "压缩文件",
    patterns: [
      "*.zip", "*.rar", "*.7z", "*.tar", "*.gz",
      "*.bz2", "*.xz", "*.zst", "*.iso", "*.dmg",
      "*.img", "*.cab", "*.lz", "*.lzma",
    ],
  },
  {
    key: "executable",
    label: "可执行文件",
    patterns: [
      "*.exe", "*.msi", "*.dll", "*.so", "*.dylib",
      "*.app", "*.deb", "*.rpm", "*.apk", "*.ipa",
      "*.bat", "*.cmd", "*.sh", "*.bin",
    ],
  },
  {
    key: "temporary",
    label: "临时文件",
    patterns: [
      "*.tmp", "*.temp", "*.part", "*.crdownload",
      "*.download", "*.bak", "*.old", "*.orig",
      "*.swp", "*.swo", "*.swn",
    ],
  },
  {
    key: "lock",
    label: "锁文件",
    patterns: ["~$*", ".~lock.*#"],
  },
] as const;

export const systemDirFilterGroups = [
  {
    key: "macos",
    label: "macOS",
    patterns: [
      ".Spotlight-V100/", ".Trashes/", ".fseventsd/",
      ".DocumentRevisions-V100/", ".TemporaryItems/",
    ],
  },
  {
    key: "windows",
    label: "Windows",
    patterns: ["$RECYCLE.BIN/", "System Volume Information/"],
  },
  {
    key: "nas",
    label: "NAS 回收站",
    patterns: [
      "\\#recycle/", "@Recycle/", ".Recycle/",
      ".Trash-*/", ".Trash/",
    ],
  },
  {
    key: "other-dirs",
    label: "其他系统目录",
    patterns: ["lost+found/", "@eaDir/", ".git/"],
  },
] as const;

const CUSTOM_FILE_TYPE_START = "# --- 自定义文件过滤 ---";
const CUSTOM_FILE_TYPE_END = "# --- 自定义文件过滤结束 ---";
const LEGACY_CUSTOM_FILE_TYPE_START = "# --- 自定义文件类型过滤 ---";
const LEGACY_CUSTOM_FILE_TYPE_END = "# --- 自定义文件类型过滤结束 ---";

function fileTypePresetPatterns() {
  return new Set<string>(
    fileTypeFilterGroups.flatMap((group) => [...group.patterns]),
  );
}

export function isExcludePatternEnabled(exclude: string, pattern: string) {
  return exclude.split("\n").some((line) => line.trim() === pattern);
}

export function updateExcludePatterns(
  exclude: string,
  patterns: readonly string[],
  enabled: boolean,
) {
  let lines = exclude.split("\n");
  for (const pattern of patterns) {
    let found = false;
    const nextLines: string[] = [];
    for (const line of lines) {
      const value = line.trim();
      if (value !== pattern && value !== `# ${pattern}`) {
        nextLines.push(line);
        continue;
      }
      if (found) continue;
      found = true;
      nextLines.push(enabled ? pattern : `# ${pattern}`);
    }
    if (!found && enabled) nextLines.push(pattern);
    lines = nextLines;
  }
  return lines.join("\n");
}

function normalizeCustomFilePattern(input: string) {
  const pattern = input.trim();
  if (
    !pattern ||
    pattern.startsWith("#") ||
    pattern.includes("/") ||
    pattern.includes("\\")
  ) {
    return "";
  }

  if (/^\*\.[a-z0-9][a-z0-9.+_-]*$/i.test(pattern)) {
    return pattern.toLowerCase();
  }

  return pattern.endsWith("/") ? "" : pattern;
}

function findCustomFileFilterBlock(lines: string[]) {
  for (const [startMarker, endMarker] of [
    [CUSTOM_FILE_TYPE_START, CUSTOM_FILE_TYPE_END],
    [LEGACY_CUSTOM_FILE_TYPE_START, LEGACY_CUSTOM_FILE_TYPE_END],
  ]) {
    const start = lines.findIndex((line) => line.trim() === startMarker);
    const end = lines.findIndex(
      (line, index) => index > start && line.trim() === endMarker,
    );
    if (start !== -1 && end !== -1) return { start, end };
  }
  return null;
}

export function parseCustomFileTypeFilters(exclude: string) {
  const lines = exclude.split("\n");
  const block = findCustomFileFilterBlock(lines);
  if (!block) return [];
  return lines.slice(block.start + 1, block.end).flatMap((line) => {
    const value = line.trim();
    if (!value) return [];
    const enabled = !value.startsWith("# ");
    const pattern = enabled ? value : value.slice(2).trim();
    return pattern ? [{ pattern, enabled }] : [];
  });
}

function parseFileTypePatternLine(line: string) {
  const value = line.trim();
  if (!value) return null;
  const enabled = !value.startsWith("# ");
  const pattern = enabled ? value : value.slice(2).trim();
  if (/^\*\.[a-z0-9][a-z0-9.+_-]*$/i.test(pattern)) {
    return { pattern: pattern.toLowerCase(), enabled };
  }
  if (enabled) {
    const normalized = normalizeCustomFilePattern(pattern);
    return normalized ? { pattern: normalized, enabled } : null;
  }
  return null;
}

export function parseOtherFileTypeFilters(exclude: string) {
  const presetPatterns = fileTypePresetPatterns();
  const lines = exclude.split("\n");
  const block = findCustomFileFilterBlock(lines);
  const filters = new Map<string, { pattern: string; enabled: boolean }>();
  const add = (item: { pattern: string; enabled: boolean } | null) => {
    if (!item || presetPatterns.has(item.pattern) || filters.has(item.pattern)) {
      return;
    }
    filters.set(item.pattern, item);
  };

  parseCustomFileTypeFilters(exclude).forEach((item) =>
    add({
      pattern: item.pattern.startsWith("*.")
        ? item.pattern.toLowerCase()
        : item.pattern,
      enabled: item.enabled,
    }),
  );
  lines.forEach((line, index) => {
    if (block && index >= block.start && index <= block.end) return;
    add(parseFileTypePatternLine(line));
  });
  return [...filters.values()];
}

export function addCustomFileTypeFilter(exclude: string, input: string) {
  const pattern = normalizeCustomFilePattern(input);
  if (!pattern) return exclude;

  const presetPatterns = fileTypePresetPatterns();
  if (presetPatterns.has(pattern)) {
    return updateExcludePatterns(exclude, [pattern], true);
  }

  const existing = parseOtherFileTypeFilters(exclude).find(
    (item) => item.pattern === pattern,
  );
  if (existing) return updateExcludePatterns(exclude, [pattern], true);

  const lines = exclude.split("\n");
  const block = findCustomFileFilterBlock(lines);
  if (block) {
    lines.splice(block.end, 0, pattern);
    return lines.join("\n");
  }

  const newBlock = [CUSTOM_FILE_TYPE_START, pattern, CUSTOM_FILE_TYPE_END].join(
    "\n",
  );
  const current = exclude.trimEnd();
  return current ? `${current}\n\n${newBlock}` : newBlock;
}

export function removeCustomFileTypeFilter(exclude: string, pattern: string) {
  let lines = exclude
    .split("\n")
    .filter((line) => ![pattern, `# ${pattern}`].includes(line.trim()));
  const block = findCustomFileFilterBlock(lines);
  if (!block) return lines.join("\n");

  const nextBlock = lines.slice(block.start + 1, block.end);
  if (nextBlock.some((line) => line.trim())) return lines.join("\n");

  const before = lines.slice(0, block.start).join("\n").trimEnd();
  const after = lines.slice(block.end + 1).join("\n").trimStart();
  return [before, after].filter(Boolean).join("\n\n");
}

// ---- 文件夹过滤标记块 ----

const FOLDER_BLOCK_START =
  "# --- 文件夹过滤（自动生成，请勿手动编辑）---";
const FOLDER_BLOCK_END = "# --- 文件夹过滤结束 ---";

/** 从 exclude 文本的标记块中提取文件夹路径列表 */
export function parseExcludeFolders(exclude: string): string[] {
  const startIdx = exclude.indexOf(FOLDER_BLOCK_START);
  const endIdx = exclude.indexOf(FOLDER_BLOCK_END);
  if (startIdx === -1 || endIdx === -1 || endIdx <= startIdx) return [];
  const block = exclude.slice(
    startIdx + FOLDER_BLOCK_START.length,
    endIdx,
  );
  return block
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l && !l.startsWith("#"));
}

/** 更新 exclude 文本中的标记块内容，保留手动规则不变 */
export function updateExcludeFolders(
  exclude: string,
  folders: string[],
): string {
  const startIdx = exclude.indexOf(FOLDER_BLOCK_START);
  const endIdx = exclude.indexOf(FOLDER_BLOCK_END);

  const block =
    folders.length > 0
      ? [FOLDER_BLOCK_START, ...folders, FOLDER_BLOCK_END].join("\n")
      : "";

  // 已有标记块 — 替换
  if (startIdx !== -1 && endIdx !== -1 && endIdx > startIdx) {
    const before = exclude.slice(0, startIdx).trimEnd();
    const after = exclude.slice(endIdx + FOLDER_BLOCK_END.length).trimStart();
    const parts = [before, block, after].filter(Boolean);
    return parts.join("\n\n");
  }

  // 无标记块 — 追加到末尾
  if (!block) return exclude;
  const trimmed = exclude.trimEnd();
  return trimmed ? trimmed + "\n\n" + block : block;
}
