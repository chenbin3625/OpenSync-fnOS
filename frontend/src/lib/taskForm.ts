import type { JobItem } from "../types";
import {
  defaultCronFields,
  defaultExclude,
  parseJobPathList,
} from "../pages/Home/homeUtils";
import {
  fileSizeToBytes,
  parseFileSizeInput,
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
    minFileSize: 0 as number | string,
    minFileSizeUnit: "MB",
    maxFileSize: 0 as number | string,
    maxFileSizeUnit: "MB",
    exclude: defaultExclude,
    ...defaultCronFields,
  };
}
export type JobForm = ReturnType<typeof defaultJobForm>;
// 0=增量同步、1=全量同步、2=移动。The backend still implements 2 as a move
// (copy then delete the source), so editing such a job must not silently turn
// it into a copy: only unknown values fall back to 0.
const supportedJobMethods = new Set([0, 1, 2]);

export function normalizeJobMethod(method: unknown) {
  const parsed = Number(method);
  return supportedJobMethods.has(parsed) && Number.isInteger(parsed) ? parsed : 0;
}

export function selectJob<T extends { id: number }>(
  jobs: T[],
  selectedId: number | null,
) {
  return selectedId === null
    ? jobs[0]
    : jobs.find((job) => job.id === selectedId);
}

/**
 * Resolves the task shown for the jobId in the URL.
 *
 * A well-formed id that is no longer in the list (deleted in another tab, an
 * old bookmark) falls back to the first task and reports `replaceWith`, so the
 * caller rewrites the URL (history replace) and the address bar, the sidebar
 * highlight and every action keep pointing at the task actually on screen.
 * A malformed id (the -1 sentinel) stays an explicit empty selection.
 */
export function resolveJobSelection<T extends { id: number }>(
  jobs: T[],
  selectedId: number | null,
): { job: T | undefined; replaceWith: number | null } {
  const job = selectJob(jobs, selectedId);
  if (job || selectedId === null || selectedId <= 0 || !jobs.length) {
    return { job, replaceWith: null };
  }
  return { job: jobs[0], replaceWith: jobs[0].id };
}

/**
 * The tab actually rendered. The realtime tab is disabled without a running
 * task, so a deep link or sidebar link carrying tab=realtime must not leave a
 * disabled tab active over an empty pane once the current-task check settles.
 */
export function resolveTaskTab(
  tab: string,
  { settled, hasCurrentTask }: { settled: boolean; hasCurrentTask: boolean },
): string {
  if (tab === "realtime" && settled && !hasCurrentTask) return "overview";
  return tab;
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
    // A stored job with no rules must stay without rules: falling back to the
    // default template silently added filters the user never configured (and
    // saved them back on the next edit).
    exclude: job.exclude ?? "",
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
/** Directory spelling used for overlap checks: absolute, no duplicate or trailing slashes. */
function comparableDirPath(path: string): string {
  const trimmed = String(path || "").trim();
  if (!trimmed) return "/";
  const absolute = trimmed.startsWith("/") ? trimmed : `/${trimmed}`;
  const collapsed = absolute.replace(/\/+/g, "/");
  return collapsed.length > 1 ? collapsed.replace(/\/+$/, "") : "/";
}

function pathContains(parent: string, child: string): boolean {
  return parent === "/" || child === parent || child.startsWith(`${parent}/`);
}

function findOverlappingPaths(
  left: string[],
  right: string[],
): [string, string] | undefined {
  for (const a of left) {
    for (const b of right) {
      if (pathContains(a, b) || pathContains(b, a)) return [a, b];
    }
  }
}

function findNestedPath(paths: string[]): [string, string] | undefined {
  for (const parent of paths) {
    for (const child of paths) {
      if (parent === child) continue;
      if (pathContains(parent, child)) return [parent, child];
    }
  }
}

// Mirrors validateJobInput in backend/internal/service/job_service.go: same or
// nested source/destination roots would make two scan works mutate one tree,
// and in mirror mode delete each other's files. Catching it here keeps the
// error on the field instead of surfacing it only after the final save.
function validateJobPaths(form: JobForm): string {
  const srcPaths = form.srcPath.map(comparableDirPath);
  const dstPaths = form.dstPath.map(comparableDirPath);
  const overlap = findOverlappingPaths(srcPaths, dstPaths);
  if (overlap)
    return `源目录与目标目录不能相同或互相包含（${overlap[0]} / ${overlap[1]}）`;
  const nestedSrc = findNestedPath(srcPaths);
  if (nestedSrc)
    return `多个源目录之间不能互相包含（${nestedSrc[0]} / ${nestedSrc[1]}）`;
  const nestedDst = findNestedPath(dstPaths);
  if (nestedDst)
    return `多个目标目录之间不能互相包含（${nestedDst[0]} / ${nestedDst[1]}）`;
  return "";
}

export function validateJobFormStep(form: JobForm, step: number) {
  switch (step) {
    case 0:
      if (!form.remark.trim()) return "请输入任务名称";
      if (!form.alistId) return "请选择存储引擎";
      if (!form.srcPath.length) return "请选择源目录";
      if (!form.dstPath.length) return "请选择目标目录";
      return validateJobPaths(form);
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
      const minValue = parseFileSizeInput(form.minFileSize);
      const maxValue = parseFileSizeInput(form.maxFileSize);
      if (minValue === null || maxValue === null) {
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
      "*.download", "*.partial",
      "*.swp", "*.swo", "*.swn",
    ],
  },
  {
    key: "lock",
    label: "锁文件",
    patterns: ["~$*", ".~lock.*#", ".#*"],
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
      "#recycle/", "@Recycle/", ".Recycle/",
      ".recycle/", ".Trash/", ".Trash-1000/",
    ],
  },
  {
    key: "other-dirs",
    label: "其他系统目录",
    patterns: ["lost+found/", "@eaDir/"],
  },
] as const;

const CUSTOM_FILE_TYPE_START = "# --- 自定义文件过滤 ---";
const CUSTOM_FILE_TYPE_END = "# --- 自定义文件过滤结束 ---";
const LEGACY_CUSTOM_FILE_TYPE_START = "# --- 自定义文件类型过滤 ---";
const LEGACY_CUSTOM_FILE_TYPE_END = "# --- 自定义文件类型过滤结束 ---";

function fileTypePresetPatterns() {
  return new Set<string>(
    fileTypeFilterGroups.flatMap((group) =>
      [...group.patterns].map((pattern) => filterRuleKey(pattern)),
    ),
  );
}

export function isExcludePatternEnabled(exclude: string, pattern: string) {
  const target = filterRuleKey(pattern);
  return exclude.split("\n").some((line) => {
    const value = line.trim();
    return !value.startsWith("# ") && filterRuleKey(value) === target;
  });
}

/**
 * Adds or removes one or more exclude rules.
 *
 * There is no persisted "disabled" state: the backend strips every "# " comment
 * when it saves (normalizeExclude in backend/internal/service/job_exclude.go),
 * so a rule kept as a comment would silently disappear on the next load.
 * Unchecking a rule therefore removes it from the exclude text; the UI treats
 * absence as disabled. Comment lines in the text are left untouched.
 */
export function updateExcludePatterns(
  exclude: string,
  patterns: readonly string[],
  enabled: boolean,
) {
  let lines = exclude.split("\n");
  for (const pattern of patterns) {
    const target = filterRuleKey(pattern);
    let found = false;
    const nextLines: string[] = [];
    for (const line of lines) {
      const value = line.trim();
      if (!value || value.startsWith("# ")) {
        nextLines.push(line);
        continue;
      }
      if (filterRuleKey(value) !== target) {
        nextLines.push(line);
        continue;
      }
      if (found) continue;
      found = true;
      if (!enabled) continue;
      nextLines.push(value);
    }
    if (found && !enabled) {
      // Removing the last rule must not leave a blank trailing line that turns
      // re-adding it into "\n#recycle/".
      while (nextLines.length && nextLines[nextLines.length - 1].trim() === "")
        nextLines.pop();
    }
    if (!found && enabled) {
      if (nextLines.length === 1 && nextLines[0] === "") nextLines.length = 0;
      nextLines.push(pattern);
    }
    lines = nextLines;
  }
  return lines.join("\n");
}

export function normalizeCustomFileTypePattern(input: string) {
  return normalizeCustomFilePattern(input);
}

function normalizeCustomFilePattern(input: string) {
  const pattern = input.trim();
  if (
    !pattern ||
    pattern.startsWith("#") ||
    pattern.startsWith("!") ||
    pattern.includes("/") ||
    pattern.includes("\\") ||
    /[\u0000-\u001f\u007f?[\]{}]/.test(pattern) ||
    pattern === "*" ||
    pattern.includes("**")
  ) {
    return "";
  }

  if (/^\*\.[a-z0-9][a-z0-9.+_-]*$/i.test(pattern)) {
    return pattern;
  }

  return pattern.startsWith("*.") ? "" : pattern.endsWith("/") ? "" : pattern;
}

function filterRuleKey(pattern: string) {
  const value = pattern.trim().replace(/^\\(?=#recycle\/$)/, "");
  return /^\*\.[a-z0-9][a-z0-9.+_-]*$/i.test(value)
    ? value.toLowerCase()
    : value;
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
    const rawPattern = enabled ? value : value.slice(2).trim();
    const pattern = normalizeCustomFilePattern(rawPattern);
    return pattern ? [{ pattern: filterRuleKey(pattern), enabled }] : [];
  });
}

function parseFileTypePatternLine(line: string) {
  const value = line.trim();
  if (!value) return null;
  const enabled = !value.startsWith("# ");
  const rawPattern = enabled ? value : value.slice(2).trim();
  if (!enabled && !/^\*\.[a-z0-9][a-z0-9.+_-]*$/i.test(rawPattern)) {
    return null;
  }
  const normalized = normalizeCustomFilePattern(rawPattern);
  return normalized
    ? { pattern: filterRuleKey(normalized), enabled }
    : null;
}

export function parseOtherFileTypeFilters(exclude: string) {
  const presetPatterns = fileTypePresetPatterns();
  const lines = exclude.split("\n");
  const block = findCustomFileFilterBlock(lines);
  const filters = new Map<string, { pattern: string; enabled: boolean }>();
  const add = (item: { pattern: string; enabled: boolean } | null) => {
    if (!item || presetPatterns.has(filterRuleKey(item.pattern))) return;
    const pattern = filterRuleKey(item.pattern);
    const current = filters.get(pattern);
    if (current) {
      // A legacy "# " comment line can name the same rule as a live one; the
      // live rule is what the job actually applies.
      if (!current.enabled && item.enabled) filters.set(pattern, { ...item, pattern });
      return;
    }
    filters.set(pattern, { ...item, pattern });
  };

  parseCustomFileTypeFilters(exclude).forEach(add);
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
  if (presetPatterns.has(filterRuleKey(pattern))) {
    return updateExcludePatterns(exclude, [pattern], true);
  }

  const existing = parseOtherFileTypeFilters(exclude).find(
    (item) => filterRuleKey(item.pattern) === filterRuleKey(pattern),
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
  const target = filterRuleKey(pattern);
  let lines = exclude
    .split("\n")
    .filter((line) => {
      const value = line.trim();
      const serialized = value.startsWith("# ")
        ? value.slice(2).trim()
        : value;
      return !serialized || filterRuleKey(serialized) !== target;
    });
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

/**
 * 判断一行是否是「文件夹过滤」生成的目录规则（标记块之外）。
 *
 * 后端保存时会去掉所有 `# ` 注释，标记块也随之消失，只剩裸的 `目录/` 行。
 * 系统目录预设、已停用的注释行和带通配符的旧默认规则（如 `.Trash-*`）
 * 不属于文件夹树，不能当成文件夹过滤，否则树一保存就会被冲掉。
 */
function isUnmarkedFolderRule(line: string): boolean {
  const value = line.trim();
  if (!value.endsWith("/") || value.startsWith("# ") || value.includes("*")) {
    return false;
  }
  return !systemDirPresetPatterns().has(filterRuleKey(value));
}

function systemDirPresetPatterns() {
  return new Set<string>(
    systemDirFilterGroups.flatMap((group) =>
      [...group.patterns].map((pattern) => filterRuleKey(pattern)),
    ),
  );
}

function findFolderBlock(exclude: string) {
  const startIdx = exclude.indexOf(FOLDER_BLOCK_START);
  const endIdx = exclude.indexOf(FOLDER_BLOCK_END);
  if (startIdx === -1 || endIdx === -1 || endIdx <= startIdx) return null;
  return { startIdx, endIdx };
}

/** 从 exclude 文本中提取文件夹路径列表：标记块内的行 + 标记块被后端去掉后残留的目录行 */
export function parseExcludeFolders(exclude: string): string[] {
  const block = findFolderBlock(exclude);
  const inside = block
    ? exclude
        .slice(block.startIdx + FOLDER_BLOCK_START.length, block.endIdx)
        .split("\n")
        .map((l) => l.trim())
        .filter((l) => l && !l.startsWith("#"))
    : [];
  const outside = (
    block
      ? exclude.slice(0, block.startIdx) +
        "\n" +
        exclude.slice(block.endIdx + FOLDER_BLOCK_END.length)
      : exclude
  )
    .split("\n")
    .filter(isUnmarkedFolderRule)
    .map((l) => l.trim());
  return Array.from(new Set([...inside, ...outside]));
}

/** 更新 exclude 文本中的文件夹过滤内容，保留手动规则和系统目录预设不变 */
export function updateExcludeFolders(
  exclude: string,
  folders: string[],
): string {
  // 先去掉标记块之外残留的目录行，避免同一目录重复且取消勾选后仍生效
  const cleaned = exclude
    .split("\n")
    .filter((line) => !isUnmarkedFolderRule(line))
    .join("\n")
    .replace(/\n{3,}/g, "\n\n");
  const block = findFolderBlock(cleaned);

  const content =
    folders.length > 0
      ? [FOLDER_BLOCK_START, ...folders, FOLDER_BLOCK_END].join("\n")
      : "";

  // 已有标记块 — 替换
  if (block) {
    const before = cleaned.slice(0, block.startIdx).trimEnd();
    const after = cleaned
      .slice(block.endIdx + FOLDER_BLOCK_END.length)
      .trimStart();
    return [before, content, after].filter(Boolean).join("\n\n");
  }

  // 无标记块 — 追加到末尾
  if (!content) return cleaned === exclude ? exclude : cleaned.trimEnd();
  const trimmed = cleaned.trimEnd();
  return trimmed ? trimmed + "\n\n" + content : content;
}
