import type { JobItem } from "../types";
import {
  defaultCronFields,
  defaultExclude,
  formatSize,
  parseJobPathList,
} from "../pages/Home/homeUtils";
import {
  fileSizeToBytes,
  fileSizeInputToBytes,
  parseFileSizeInput,
  splitBytesToFileSize,
} from "../pages/Home/fileSizeUnits";
import {
  maxJobIntervalMinutes,
  validateCronFields,
  type CronFieldKey,
} from "./schedule";
import {
  cleanPathList,
  validateSyncPathSelection,
  type ExistingJobPathSummary,
} from "./syncPathPreview";

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
// Preserve all supported modes when editing stored jobs.
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
export interface JobValidationOptions {
  storedDstMap?: string | null;
  existingJobs?: readonly ExistingJobPathSummary[];
  sizeEnabled?: { min?: boolean; max?: boolean };
}

export type JobFieldErrorKey =
  | "remark"
  | "alistId"
  | "srcPath"
  | "dstPath"
  | "interval"
  | CronFieldKey
  | "minFileSize"
  | "maxFileSize"
  | "fileSizeRange";

function resolveValidationOptions(
  storedDstMapOrOptions?: string | null | JobValidationOptions,
): JobValidationOptions {
  if (
    storedDstMapOrOptions &&
    typeof storedDstMapOrOptions === "object"
  ) {
    return storedDstMapOrOptions;
  }
  return { storedDstMap: storedDstMapOrOptions };
}

// Mirrors validateJobInput in backend/internal/service/job_service.go: same or
// nested source/destination roots (including derived or preserved destination
// subdirectories) would make two scan works mutate one tree, and in mirror mode
// delete each other's files. Catching it here keeps the error on the field
// instead of surfacing it only after the final save.
function validateJobPaths(
  form: JobForm,
  options?: JobValidationOptions,
): string {
  const effectiveDstMap =
    options?.storedDstMap !== undefined
      ? options.storedDstMap
      : (form as JobForm & { dstMap?: string | null }).dstMap;
  return validateSyncPathSelection({
    srcPaths: form.srcPath,
    dstPaths: form.dstPath,
    storedDstMap: effectiveDstMap,
    method: form.method,
    existingJobs: options?.existingJobs,
    currentJobId: form.id,
    engineId: form.alistId,
  });
}

export function validateJobFormFields(
  form: JobForm,
  storedDstMapOrOptions?: string | null | JobValidationOptions,
): {
  fields: Partial<Record<JobFieldErrorKey, string>>;
  stepErrors: [string, string, string];
} {
  const options = resolveValidationOptions(storedDstMapOrOptions);
  const fields: Partial<Record<JobFieldErrorKey, string>> = {};

  // Step 0: 基础与路径（任务名称、存储引擎、同步方式、源路径、目标路径与跨任务冲突）
  const step0Errors: string[] = [];
  if (!form.remark.trim()) {
    fields.remark = "请输入任务名称";
    step0Errors.push(fields.remark);
  }
  if (!form.alistId) {
    fields.alistId = "请选择存储引擎";
    step0Errors.push(fields.alistId);
  }
  if (!form.srcPath.length) {
    fields.srcPath = "请选择源目录";
    step0Errors.push(fields.srcPath);
  }
  if (!form.dstPath.length) {
    fields.dstPath = "请选择目标目录";
    step0Errors.push(fields.dstPath);
  }
  if (form.method === 2 && form.dstPath.length > 1) {
    fields.dstPath = "移动模式只允许一个目标目录";
    step0Errors.push(fields.dstPath);
  }
  if (form.srcPath.length > 0 && form.dstPath.length > 0) {
    const pathError = validateJobPaths(form, options);
    if (pathError) {
      if (pathError.includes("多个源目录")) {
        fields.srcPath = fields.srcPath || pathError;
      } else {
        fields.dstPath = fields.dstPath || pathError;
      }
      step0Errors.push(pathError);
    }
  }

  // Step 1: 执行计划（固定间隔 / Cron / 仅手动）
  const step1Errors: string[] = [];
  if (form.isCron === 0) {
    if (!Number.isInteger(form.interval) || form.interval < 1) {
      fields.interval = "执行间隔必须是大于 0 的整数";
      step1Errors.push(fields.interval);
    } else if (form.interval > maxJobIntervalMinutes) {
      fields.interval = `执行间隔不能超过 ${maxJobIntervalMinutes} 分钟`;
      step1Errors.push(fields.interval);
    }
  } else if (form.isCron === 1) {
    const cronErrors = validateCronFields(form);
    for (const [key, message] of Object.entries(cronErrors) as Array<
      [CronFieldKey, string]
    >) {
      fields[key] = message;
      step1Errors.push(message);
    }
  }

  // Step 2: 过滤规则（文件大小上下限）
  const step2Errors: string[] = [];
  const minValue = parseFileSizeInput(form.minFileSize);
  const maxValue = parseFileSizeInput(form.maxFileSize);
  const minRawEmpty =
    typeof form.minFileSize === "string" && form.minFileSize.trim() === "";
  const maxRawEmpty =
    typeof form.maxFileSize === "string" && form.maxFileSize.trim() === "";

  if (minValue === null) {
    fields.minFileSize = "文件大小必须是有效的非负数";
    step2Errors.push(fields.minFileSize);
  } else if (options.sizeEnabled?.min && (minRawEmpty || minValue <= 0)) {
    fields.minFileSize = "请输入大于 0 的文件大小，或取消勾选以关闭此限制";
    step2Errors.push(fields.minFileSize);
  }

  if (maxValue === null) {
    fields.maxFileSize = "文件大小必须是有效的非负数";
    step2Errors.push(fields.maxFileSize);
  } else if (options.sizeEnabled?.max && (maxRawEmpty || maxValue <= 0)) {
    fields.maxFileSize = "请输入大于 0 的文件大小，或取消勾选以关闭此限制";
    step2Errors.push(fields.maxFileSize);
  }

  if (minValue !== null && maxValue !== null) {
    const minBytes = fileSizeInputToBytes(
      form.minFileSize,
      form.minFileSizeUnit,
    );
    const maxBytes = fileSizeInputToBytes(
      form.maxFileSize,
      form.maxFileSizeUnit,
    );
    if (minBytes === null) {
      fields.minFileSize =
        fields.minFileSize || "文件大小换算后必须是 1 字节到安全整数范围内的值";
      step2Errors.push(fields.minFileSize);
    }
    if (maxBytes === null) {
      fields.maxFileSize =
        fields.maxFileSize || "文件大小换算后必须是 1 字节到安全整数范围内的值";
      step2Errors.push(fields.maxFileSize);
    }
    if (
      minBytes !== null &&
      maxBytes !== null &&
      maxBytes > 0 &&
      minBytes > maxBytes
    ) {
      fields.fileSizeRange = "最大文件大小必须大于等于最小文件大小";
      step2Errors.push(fields.fileSizeRange);
    }
  }

  return {
    fields,
    stepErrors: [
      step0Errors[0] ?? "",
      step1Errors[0] ?? "",
      step2Errors[0] ?? "",
    ],
  };
}

export function validateJobFormStep(
  form: JobForm,
  step: number,
  storedDstMapOrOptions?: string | null | JobValidationOptions,
) {
  const { stepErrors } = validateJobFormFields(form, storedDstMapOrOptions);
  return stepErrors[step] ?? "";
}

export function validateJobForm(
  form: JobForm,
  storedDstMapOrOptions?: string | null | JobValidationOptions,
) {
  const { stepErrors } = validateJobFormFields(form, storedDstMapOrOptions);
  return stepErrors.find(Boolean) ?? "";
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

export function validateCustomFilePattern(input: string): {
  pattern: string;
  error: string;
} {
  const pattern = input.trim();
  if (!pattern) {
    return { pattern: "", error: "请输入文件名或文件后缀" };
  }
  if (pattern.startsWith("#")) {
    return { pattern: "", error: "规则不能以 # 开头" };
  }
  if (pattern.includes("!")) {
    return { pattern: "", error: "暂不支持取反规则（!）" };
  }
  if (pattern.includes("/") || pattern.includes("\\")) {
    return {
      pattern: "",
      error: "请勿包含路径分隔符，目录请在下方「文件夹过滤」中配置",
    };
  }
  if (pattern === "*" || pattern.includes("**")) {
    return { pattern: "", error: "暂不支持单独的 * 或 ** 通配符" };
  }
  if (/[\u0000-\u001f\u007f-\u009f]/.test(pattern)) {
    return { pattern: "", error: "包含无效控制字符" };
  }
  if (/[?[\]{}]/.test(pattern)) {
    return { pattern: "", error: "仅支持 * 通配符，暂不支持 ?、[]、{}" };
  }
  if (pattern.startsWith("*.")) {
    return isValidExtensionRule(pattern)
      ? { pattern, error: "" }
      : { pattern: "", error: "文件后缀格式无效，例如 *.log、*.mp4" };
  }
  if (pattern === "." || pattern === "..") {
    return { pattern: "", error: "请输入有效的文件名或文件后缀" };
  }
  return { pattern, error: "" };
}

export function normalizeCustomFileTypePattern(input: string) {
  return validateCustomFilePattern(input).pattern;
}

function isValidExtensionRule(pattern: string): boolean {
  if (!pattern.startsWith("*.")) return false;
  const ext = pattern.slice(2);
  return (
    ext !== "" &&
    !ext.startsWith(".") &&
    !ext.endsWith(".") &&
    !/[/\\*?\[\]{}!\u0000-\u001f\u007f-\u009f]/.test(ext)
  );
}

function normalizeCustomFilePattern(input: string) {
  return validateCustomFilePattern(input).pattern;
}

function filterRuleKey(pattern: string) {
  const trimmed = pattern.trim();
  const value =
    trimmed.startsWith("\\#") && trimmed.endsWith("/")
      ? trimmed.slice(1)
      : trimmed;
  return isValidExtensionRule(value) ? value.toLowerCase() : value;
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
  if (!enabled && !isValidExtensionRule(rawPattern)) {
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
function normalizeFolderRuleLine(line: string): string {
  const value = line.trim();
  return value.startsWith("\\#") && value.endsWith("/")
    ? value.slice(1)
    : value;
}

function isUnmarkedFolderRule(line: string): boolean {
  const value = normalizeFolderRuleLine(line);
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
        .map(normalizeFolderRuleLine)
        .filter(
          (l) =>
            l &&
            !l.startsWith("# ") &&
            (l.endsWith("/") || !l.startsWith("#")),
        )
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
    .map(normalizeFolderRuleLine);
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

/**
 * 判断源目录变更后是否已完全脱离原有源目录集合，以便自动清理失效的相对文件夹过滤规则。
 */
export function shouldClearExcludeFoldersOnSrcChange(
  prevSrcPaths: readonly string[],
  nextSrcPaths: readonly string[],
  exclude: string,
): boolean {
  if (!prevSrcPaths.length) return false;
  if (!parseExcludeFolders(exclude).length) return false;
  const prevSet = new Set(cleanPathList(prevSrcPaths).map((p) => p.toLowerCase()));
  const nextClean = cleanPathList(nextSrcPaths).map((p) => p.toLowerCase());
  return !nextClean.some((p) => prevSet.has(p));
}

/**
 * 生成任务总览页展示的过滤规则摘要标签。
 */
export function summarizeJobFilters(
  job: Pick<JobItem, "exclude" | "minFileSize" | "maxFileSize">,
): string[] {
  const items: string[] = [];
  const minBytes = Number(job.minFileSize || 0);
  const maxBytes = Number(job.maxFileSize || 0);
  if (minBytes > 0) items.push(`排除 < ${formatSize(minBytes)}`);
  if (maxBytes > 0) items.push(`排除 > ${formatSize(maxBytes)}`);

  const exclude = job.exclude || "";
  const presetFileCount = fileTypeFilterGroups.reduce(
    (sum, group) =>
      sum +
      group.patterns.filter((p) => isExcludePatternEnabled(exclude, p)).length,
    0,
  );
  const customFileCount = parseOtherFileTypeFilters(exclude).filter(
    (item) => item.enabled,
  ).length;
  const totalFileRules = presetFileCount + customFileCount;
  if (totalFileRules > 0) {
    items.push(`文件规则 ${totalFileRules} 条`);
  }

  const folderRules = parseExcludeFolders(exclude);
  if (folderRules.length > 0) {
    items.push(`排除目录 ${folderRules.length} 个`);
  }

  const systemDirCount = systemDirFilterGroups.reduce(
    (sum, group) =>
      sum +
      group.patterns.filter((p) => isExcludePatternEnabled(exclude, p)).length,
    0,
  );
  if (systemDirCount > 0) {
    items.push(`系统目录 ${systemDirCount} 项`);
  }

  return items;
}
