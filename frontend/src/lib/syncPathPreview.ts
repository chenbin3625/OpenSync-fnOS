const JOB_DST_MAP_VERSION = 1;

interface StoredJobDstMap {
  version: number;
  base: string[];
  map: Record<string, string>;
}

export type SyncPathMappingRule =
  | "empty"
  | "missing-src"
  | "missing-dst"
  | "single-src"
  | "shared-parent"
  | "base-name"
  | "disambiguated-base-name";

export interface SyncPathPreviewItem {
  key: string;
  src: string;
  srcDir: string;
  dstBase: string;
  dstBaseDir: string;
  dstBasePrefix: string;
  appendedSuffix: string;
  resolvedDst: string;
  freshDst: string;
  preservedFromStoredMap: boolean;
  hasConflict: boolean;
}

export interface SyncPathPreviewGroup {
  dstBase: string;
  dstBaseDir: string;
  items: SyncPathPreviewItem[];
}

export interface ExistingJobPathSummary {
  id: number;
  remark?: string | null;
  alistId: number;
  method?: number;
  srcPath: string | readonly string[];
  dstPath: string | readonly string[];
  dstMap?: string | null;
}

export interface SyncPathPreviewResult {
  rule: SyncPathMappingRule;
  commonParent: string;
  summary: string;
  items: SyncPathPreviewItem[];
  groups: SyncPathPreviewGroup[];
  hasPreservedMapping: boolean;
  conflicts: string[];
}

// Mirrors Go's path.Clean on POSIX paths so preview results match backend
// task_scan.go / job_paths.go 1:1.
export function cleanPosixPath(value: string): string {
  if (value === "") return ".";
  const rooted = value.startsWith("/");
  const out: string[] = [];
  for (const part of value.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") {
      if (out.length > 0 && out[out.length - 1] !== "..") {
        out.pop();
      } else if (!rooted) {
        out.push("..");
      }
      continue;
    }
    out.push(part);
  }
  if (rooted) return `/${out.join("/")}`;
  return out.length > 0 ? out.join("/") : ".";
}

// Mirrors Go's path.Base.
export function posixBase(value: string): string {
  if (value === "") return ".";
  let end = value.length;
  while (end > 0 && value[end - 1] === "/") end--;
  if (end === 0) return "/";
  const idx = value.lastIndexOf("/", end - 1);
  return idx >= 0 ? value.slice(idx + 1, end) : value.slice(0, end);
}

// Mirrors Go's path.Dir.
export function posixDir(value: string): string {
  const idx = value.lastIndexOf("/");
  const prefix = idx >= 0 ? value.slice(0, idx + 1) : "";
  return cleanPosixPath(prefix);
}

export function pathHasParentSegment(value: string): boolean {
  return value.split(/[/\\]+/).some((segment) => segment === "..");
}

export function pathHasUnsafeChars(value: string): boolean {
  return /[\\\u0000-\u001f\u007f-\u009f]/.test(value);
}

export function normalizeDirPath(value: string): string {
  const trimmed = String(value ?? "").trim();
  if (!trimmed) return "";
  return trimmed.endsWith("/") ? trimmed : `${trimmed}/`;
}

export function cleanPathList(paths: readonly string[]): string[] {
  const cleaned: string[] = [];
  const seen = new Set<string>();
  for (const raw of paths) {
    const item = String(raw ?? "").trim();
    if (!item) continue;
    const key =
      !pathHasParentSegment(item) && !pathHasUnsafeChars(item)
        ? cleanPosixPath(item)
        : item;
    if (seen.has(key)) continue;
    seen.add(key);
    cleaned.push(key);
  }
  return cleaned;
}

// Mirrors syncPathContains in backend/internal/service/job_paths.go
// (case-insensitive containment check).
export function syncPathContains(parent: string, candidate: string): boolean {
  const trimmedParent = String(parent ?? "").trim();
  const trimmedCandidate = String(candidate ?? "").trim();
  if (!trimmedParent || !trimmedCandidate) return false;
  if (
    pathHasParentSegment(trimmedParent) ||
    pathHasParentSegment(trimmedCandidate) ||
    pathHasUnsafeChars(trimmedParent) ||
    pathHasUnsafeChars(trimmedCandidate)
  ) {
    return false;
  }
  const normalizedParent = cleanPosixPath(trimmedParent).toLowerCase();
  const normalizedCandidate = cleanPosixPath(trimmedCandidate).toLowerCase();
  if (normalizedParent === "." || normalizedCandidate === ".") return false;
  if (normalizedParent === normalizedCandidate) return true;
  if (normalizedParent === "/") return normalizedCandidate.startsWith("/");
  return normalizedCandidate.startsWith(`${normalizedParent}/`);
}

// Mirrors native.StripLocalRootID in backend/internal/native/paths.go.
export function stripNativeLocalRootID(segment: string): {
  base: string;
  stripped: boolean;
} {
  const idx = segment.lastIndexOf("~");
  if (idx <= 0 || segment.length - idx - 1 !== 12) {
    return { base: segment, stripped: false };
  }
  const base = segment.slice(0, idx);
  if (base === "." || base === "..") {
    return { base: segment, stripped: false };
  }
  const suffix = segment.slice(idx + 1);
  if (!/^[0-9a-f]{12}$/.test(suffix)) {
    return { base: segment, stripped: false };
  }
  return { base, stripped: true };
}

// Mirrors splitLocalRootSegment in backend/internal/service/task_scan.go.
function splitLocalRootSegment(cleanPath: string): {
  storage: string;
  rootSeg: string;
  rest: string;
} | null {
  if (!cleanPath.startsWith("/")) return null;
  const trimmed = cleanPath.slice(1);
  const firstSlash = trimmed.indexOf("/");
  if (firstSlash <= 0) return null;
  const storage = trimmed.slice(0, firstSlash);
  const afterStorage = trimmed.slice(firstSlash + 1);
  if (!afterStorage) return null;
  const secondSlash = afterStorage.indexOf("/");
  const rootSeg =
    secondSlash >= 0 ? afterStorage.slice(0, secondSlash) : afterStorage;
  if (!rootSeg) return null;
  const rest = secondSlash >= 0 ? afterStorage.slice(secondSlash + 1) : "";
  return { storage, rootSeg, rest };
}

// Mirrors sanitizeSrcPathsForDstDerivation in backend/internal/service/task_scan.go.
function sanitizeSrcPathsForDstDerivation(srcPaths: readonly string[]): {
  sanitized: string[];
  sanitizeOne: (cleanPath: string) => string;
} {
  const cleanedInputs = srcPaths.map((raw) => cleanPosixPath(raw.trim()));
  const rawByBase = new Map<string, Set<string>>();
  for (const clean of cleanedInputs) {
    const parts = splitLocalRootSegment(clean);
    if (!parts) continue;
    const { base } = stripNativeLocalRootID(parts.rootSeg);
    const baseKey = base.toLowerCase();
    const rawKey = parts.rootSeg.toLowerCase();
    let set = rawByBase.get(baseKey);
    if (!set) {
      set = new Set<string>();
      rawByBase.set(baseKey, set);
    }
    set.add(rawKey);
  }

  const sanitizeOne = (clean: string): string => {
    const parts = splitLocalRootSegment(clean);
    if (!parts) return clean;
    const { base, stripped } = stripNativeLocalRootID(parts.rootSeg);
    if (!stripped) return clean;
    const set = rawByBase.get(base.toLowerCase());
    if (set && set.size > 1) return clean;
    if (set && set.size === 1 && !set.has(parts.rootSeg.toLowerCase())) {
      return clean;
    }
    return parts.rest
      ? `/${parts.storage}/${base}/${parts.rest}`
      : `/${parts.storage}/${base}`;
  };

  return {
    sanitized: cleanedInputs.map(sanitizeOne),
    sanitizeOne,
  };
}

function formatOneDisplayPath(
  rawPath: string,
  sanitizeOne: (cleanPath: string) => string,
): string {
  const trimmed = String(rawPath ?? "").trim();
  if (!trimmed) return "";
  if (pathHasParentSegment(trimmed) || pathHasUnsafeChars(trimmed)) {
    return trimmed;
  }
  const hadTrailingSlash = trimmed.length > 1 && trimmed.endsWith("/");
  const formatted = sanitizeOne(cleanPosixPath(trimmed));
  return hadTrailingSlash && formatted !== "/"
    ? normalizeDirPath(formatted)
    : formatted;
}

/**
 * Formats a virtual sync path for UI display by stripping the `~<12hex>`
 * native local root fingerprint from the authorized root segment
 * (`/<storage>/<base>~<12hex>[/...]`).
 *
 * When `siblingPaths` is supplied, any root segment whose stripped base name
 * collides (case-insensitively) with a different raw root segment in
 * `siblingPaths` retains its fingerprint so distinct volumes stay distinguishable.
 */
export function formatDisplayPath(
  rawPath: string,
  siblingPaths?: readonly string[],
): string {
  const trimmed = String(rawPath ?? "").trim();
  if (!trimmed) return "";
  const { sanitizeOne } = sanitizeSrcPathsForDstDerivation(
    siblingPaths ?? [trimmed],
  );
  return formatOneDisplayPath(trimmed, sanitizeOne);
}

/** Formats a list of virtual sync paths for UI display using the list (or `siblingPaths`) for collision detection. */
export function formatDisplayPathList(
  paths: readonly string[],
  siblingPaths?: readonly string[],
): string[] {
  const { sanitizeOne } = sanitizeSrcPathsForDstDerivation(
    siblingPaths ?? paths,
  );
  return paths.map((rawPath) => formatOneDisplayPath(rawPath, sanitizeOne));
}

function commonSrcSelectionParentRaw(srcPaths: readonly string[]): string {
  if (srcPaths.length < 2) return "";
  let common = cleanPosixPath(srcPaths[0].trim());
  for (let i = 1; i < srcPaths.length; i++) {
    const selected = cleanPosixPath(srcPaths[i].trim());
    while (
      common !== "." &&
      common !== "/" &&
      selected !== common &&
      !selected.startsWith(normalizeDirPath(common))
    ) {
      common = posixDir(common);
    }
  }
  return common;
}

// Mirrors commonSrcSelectionParent in backend/internal/service/task_scan.go.
export function commonSrcSelectionParent(srcPaths: readonly string[]): string {
  const { sanitized } = sanitizeSrcPathsForDstDerivation(srcPaths);
  return commonSrcSelectionParentRaw(sanitized);
}

// Mirrors srcSelectionBaseNameIsAmbiguous in backend/internal/service/task_scan.go.
export function srcSelectionBaseNameIsAmbiguous(
  cleanSrc: string,
  srcPaths: readonly string[],
): boolean {
  const base = posixBase(cleanSrc).toLowerCase();
  for (const other of srcPaths) {
    const cleanOther = cleanPosixPath(other.trim());
    if (cleanOther === cleanSrc) continue;
    if (posixBase(cleanOther).toLowerCase() === base) return true;
  }
  return false;
}

// Mirrors srcSelectionSuffix in backend/internal/service/task_scan.go.
export function srcSelectionSuffix(
  srcPath: string,
  srcPaths: readonly string[],
): string {
  const { sanitized, sanitizeOne } = sanitizeSrcPathsForDstDerivation(srcPaths);
  const cleanSrc = sanitizeOne(cleanPosixPath(srcPath.trim()));
  const commonParent = commonSrcSelectionParentRaw(sanitized);
  if (commonParent === "" || commonParent === "." || commonParent === "/") {
    if (srcSelectionBaseNameIsAmbiguous(cleanSrc, sanitized)) {
      return cleanSrc.startsWith("/") ? cleanSrc.slice(1) : cleanSrc;
    }
    return posixBase(cleanSrc);
  }
  if (cleanSrc === commonParent) return "";
  const prefix = normalizeDirPath(commonParent);
  return cleanSrc.startsWith(prefix) ? cleanSrc.slice(prefix.length) : cleanSrc;
}

// Mirrors dstPathForSrcSelection in backend/internal/service/task_scan.go.
export function dstPathForSrcSelection(
  dstPath: string,
  srcPath: string,
  srcPaths: readonly string[],
): string {
  const normalizedDst = normalizeDirPath(dstPath);
  if (srcPaths.length <= 1) return normalizedDst;
  const suffix = srcSelectionSuffix(srcPath, srcPaths);
  if (suffix === "." || suffix === "/" || suffix === "") return normalizedDst;
  return normalizeDirPath(normalizedDst + suffix);
}

export function jobDstMapKey(srcRoot: string, dstRoot: string): string {
  return `${normalizeDirPath(srcRoot)}\u0000${normalizeDirPath(dstRoot)}`;
}

function parseStoredJobDstMap(raw?: string | null): StoredJobDstMap | null {
  if (typeof raw !== "string") return null;
  const trimmed = raw.trim();
  if (!trimmed) return null;
  try {
    const parsed = JSON.parse(trimmed) as Partial<StoredJobDstMap>;
    if (
      parsed.version !== JOB_DST_MAP_VERSION ||
      !Array.isArray(parsed.base) ||
      !parsed.map ||
      typeof parsed.map !== "object" ||
      Array.isArray(parsed.map)
    ) {
      return null;
    }
    return {
      version: parsed.version,
      base: parsed.base.map(String),
      map: Object.fromEntries(
        Object.entries(parsed.map).map(([k, v]) => [k, String(v ?? "")]),
      ),
    };
  } catch {
    return null;
  }
}

function jobDstBaseEquals(
  stored: readonly string[],
  current: readonly string[],
): boolean {
  if (stored.length !== current.length) return false;
  for (let i = 0; i < stored.length; i++) {
    if (normalizeDirPath(stored[i]) !== normalizeDirPath(current[i])) {
      return false;
    }
  }
  return true;
}

function splitResolvedDestination(
  dstBaseDir: string,
  resolvedDst: string,
): { dstBasePrefix: string; appendedSuffix: string } {
  if (
    dstBaseDir &&
    resolvedDst.toLowerCase().startsWith(dstBaseDir.toLowerCase())
  ) {
    return {
      dstBasePrefix: resolvedDst.slice(0, dstBaseDir.length),
      appendedSuffix: resolvedDst.slice(dstBaseDir.length),
    };
  }
  return { dstBasePrefix: "", appendedSuffix: resolvedDst };
}

function findOverlappingPair(
  left: readonly string[],
  right: readonly string[],
): [string, string] | undefined {
  for (const a of left) {
    for (const b of right) {
      if (syncPathContains(a, b) || syncPathContains(b, a)) return [a, b];
    }
  }
}

function findNestedPair(paths: readonly string[]): [string, string] | undefined {
  for (let i = 0; i < paths.length; i++) {
    for (let j = 0; j < paths.length; j++) {
      if (i === j) continue;
      if (syncPathContains(paths[i], paths[j])) return [paths[i], paths[j]];
    }
  }
}

function parseJobPathInput(value: string | readonly string[]): string[] {
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
}

/** Mirrors resolvedDstPathsForJob in backend/internal/service/job_paths.go. */
export function resolveJobDestinationPaths(
  job: Pick<ExistingJobPathSummary, "srcPath" | "dstPath" | "dstMap">,
): string[] {
  const cleanSrc = cleanPathList(parseJobPathInput(job.srcPath));
  const cleanDst = cleanPathList(parseJobPathInput(job.dstPath));
  if (cleanDst.length === 0) return [];
  if (cleanSrc.length === 0) return cleanDst.map(normalizeDirPath);

  const base = cleanDst.map(normalizeDirPath);
  const stored = parseStoredJobDstMap(job.dstMap);
  const reuse = stored && jobDstBaseEquals(stored.base, base) ? stored.map : null;
  const resolved: string[] = [];
  for (const dst of cleanDst) {
    const dstBaseDir = normalizeDirPath(dst);
    for (const src of cleanSrc) {
      const srcDir = normalizeDirPath(src);
      const mapKey = jobDstMapKey(srcDir, dstBaseDir);
      const storedValue = reuse?.[mapKey];
      const reusedStored = Boolean(
        storedValue && syncPathContains(dstBaseDir, storedValue),
      );
      resolved.push(
        reusedStored
          ? normalizeDirPath(storedValue!)
          : dstPathForSrcSelection(dstBaseDir, srcDir, cleanSrc),
      );
    }
  }
  return resolved;
}

/** Mirrors writablePathsForJob in backend/internal/service/job_paths.go. */
export function resolveJobWritablePaths(
  job: Pick<ExistingJobPathSummary, "srcPath" | "dstPath" | "dstMap" | "method">,
): string[] {
  const paths = resolveJobDestinationPaths(job);
  if (job.method === 3) {
    for (const src of cleanPathList(parseJobPathInput(job.srcPath))) {
      paths.push(normalizeDirPath(src));
    }
  }
  return paths;
}

export function buildSyncPathPreview({
  srcPaths,
  dstPaths,
  storedDstMap,
  method = 0,
  existingJobs,
  currentJobId,
  engineId,
}: {
  srcPaths: readonly string[];
  dstPaths: readonly string[];
  storedDstMap?: string | null;
  method?: number;
  existingJobs?: readonly ExistingJobPathSummary[];
  currentJobId?: number;
  engineId?: number;
}): SyncPathPreviewResult {
  const cleanSrc = cleanPathList(srcPaths);
  const cleanDst = cleanPathList(dstPaths);

  if (cleanSrc.length === 0 && cleanDst.length === 0) {
    return {
      rule: "empty",
      commonParent: "",
      summary: "选择源路径和目标路径后，这里会实时预览同步文件夹的目标位置。",
      items: [],
      groups: [],
      hasPreservedMapping: false,
      conflicts: [],
    };
  }

  if (cleanSrc.length === 0) {
    return {
      rule: "missing-src",
      commonParent: "",
      summary: "请选择源路径，预览将显示同步文件夹在目标目录中的具体位置。",
      items: [],
      groups: [],
      hasPreservedMapping: false,
      conflicts: [],
    };
  }

  if (cleanDst.length === 0) {
    return {
      rule: "missing-dst",
      commonParent: "",
      summary: "请选择目标路径，预览将显示同步文件夹在目标目录中的具体位置。",
      items: [],
      groups: [],
      hasPreservedMapping: false,
      conflicts: [],
    };
  }

  const base = cleanDst.map(normalizeDirPath);
  const stored = parseStoredJobDstMap(storedDstMap);
  const reuse = stored && jobDstBaseEquals(stored.base, base) ? stored.map : null;

  const { sanitized: sanitizedSrc } =
    sanitizeSrcPathsForDstDerivation(cleanSrc);
  const commonParent = commonSrcSelectionParentRaw(sanitizedSrc);
  const hasSharedParent =
    sanitizedSrc.length > 1 &&
    commonParent !== "" &&
    commonParent !== "." &&
    commonParent !== "/";
  const hasAmbiguousBase =
    sanitizedSrc.length > 1 &&
    !hasSharedParent &&
    sanitizedSrc.some((src) =>
      srcSelectionBaseNameIsAmbiguous(src, sanitizedSrc),
    );

  const rule: SyncPathMappingRule =
    cleanSrc.length <= 1
      ? "single-src"
      : hasSharedParent
        ? "shared-parent"
        : hasAmbiguousBase
          ? "disambiguated-base-name"
          : "base-name";

  const items: SyncPathPreviewItem[] = [];
  const groups: SyncPathPreviewGroup[] = [];

  for (const dst of cleanDst) {
    const dstBaseDir = normalizeDirPath(dst);
    const groupItems: SyncPathPreviewItem[] = [];
    for (const src of cleanSrc) {
      const srcDir = normalizeDirPath(src);
      const freshDst = dstPathForSrcSelection(dstBaseDir, srcDir, cleanSrc);
      const mapKey = jobDstMapKey(srcDir, dstBaseDir);
      const storedValue = reuse?.[mapKey];
      const reusedStored = Boolean(
        storedValue && syncPathContains(dstBaseDir, storedValue),
      );
      const resolvedDst = reusedStored
        ? normalizeDirPath(storedValue!)
        : freshDst;
      const { dstBasePrefix, appendedSuffix } = splitResolvedDestination(
        dstBaseDir,
        resolvedDst,
      );
      const item: SyncPathPreviewItem = {
        key: mapKey,
        src,
        srcDir,
        dstBase: dst,
        dstBaseDir,
        dstBasePrefix,
        appendedSuffix,
        resolvedDst,
        freshDst,
        preservedFromStoredMap: reusedStored && resolvedDst !== freshDst,
        hasConflict: false,
      };
      items.push(item);
      groupItems.push(item);
    }
    groups.push({ dstBase: dst, dstBaseDir, items: groupItems });
  }

  const conflicts: string[] = [];

  if (method === 2 && cleanDst.length > 1) {
    conflicts.push("移动模式只允许一个目标目录");
  }
  if (method === 3 && (cleanSrc.length > 1 || cleanDst.length > 1)) {
    conflicts.push("双向同步模式只允许一个源目录和一个目标目录");
  }

  const allConflictSiblings = [...cleanSrc, ...cleanDst];

  const overlap = findOverlappingPair(cleanSrc, cleanDst);
  if (overlap) {
    conflicts.push(
      `源目录与目标目录不能相同或互相包含（${formatDisplayPath(overlap[0], allConflictSiblings)} / ${formatDisplayPath(overlap[1], allConflictSiblings)}）`,
    );
    for (const item of items) {
      if (
        syncPathContains(item.src, item.dstBase) ||
        syncPathContains(item.dstBase, item.src)
      ) {
        item.hasConflict = true;
      }
    }
  }

  const nestedSrc = findNestedPair(cleanSrc);
  if (nestedSrc) {
    conflicts.push(
      `多个源目录之间不能互相包含（${formatDisplayPath(nestedSrc[0], cleanSrc)} / ${formatDisplayPath(nestedSrc[1], cleanSrc)}）`,
    );
    for (const item of items) {
      for (const otherSrc of cleanSrc) {
        if (otherSrc === item.src) continue;
        if (
          syncPathContains(otherSrc, item.src) ||
          syncPathContains(item.src, otherSrc)
        ) {
          item.hasConflict = true;
          break;
        }
      }
    }
  }

  const nestedConfiguredDst = findNestedPair(cleanDst);
  if (nestedConfiguredDst) {
    conflicts.push(
      `多个目标目录之间不能互相包含（${formatDisplayPath(nestedConfiguredDst[0], cleanDst)} / ${formatDisplayPath(nestedConfiguredDst[1], cleanDst)}）`,
    );
    for (const item of items) {
      for (const otherDst of cleanDst) {
        if (otherDst === item.dstBase) continue;
        if (
          syncPathContains(otherDst, item.dstBase) ||
          syncPathContains(item.dstBase, otherDst)
        ) {
          item.hasConflict = true;
          break;
        }
      }
    }
  } else {
    let nestedResolved: [SyncPathPreviewItem, SyncPathPreviewItem] | undefined;
    for (let i = 0; i < items.length; i++) {
      for (let j = i + 1; j < items.length; j++) {
        if (
          syncPathContains(items[i].resolvedDst, items[j].resolvedDst) ||
          syncPathContains(items[j].resolvedDst, items[i].resolvedDst)
        ) {
          items[i].hasConflict = true;
          items[j].hasConflict = true;
          if (!nestedResolved) nestedResolved = [items[i], items[j]];
        }
      }
    }
    if (nestedResolved) {
      const [first, second] = nestedResolved;
      const causedByPreserved =
        first.preservedFromStoredMap || second.preservedFromStoredMap;
      const allResolved = items.map((item) => item.resolvedDst);
      const firstDisplay = formatDisplayPath(first.resolvedDst, allResolved);
      const secondDisplay = formatDisplayPath(second.resolvedDst, allResolved);
      conflicts.push(
        causedByPreserved
          ? `多个目标目录之间不能互相包含（${firstDisplay} / ${secondDisplay}），修改目标路径可按新规则重新分配`
          : `多个目标目录之间不能互相包含（${firstDisplay} / ${secondDisplay}）`,
      );
    }
  }

  if (existingJobs?.length && engineId !== undefined && engineId > 0) {
    const reported = new Set<string>();
    for (const other of existingJobs) {
      if (other.id === currentJobId || other.alistId !== engineId) continue;
      const otherName = other.remark?.trim() || `同步任务 #${other.id}`;
      const otherWritable = resolveJobWritablePaths(other);
      for (const otherPath of otherWritable) {
        let hitDst = false;
        let hitBisyncSrc = false;
        for (const item of items) {
          if (
            syncPathContains(item.resolvedDst, otherPath) ||
            syncPathContains(otherPath, item.resolvedDst)
          ) {
            item.hasConflict = true;
            hitDst = true;
          }
          if (
            method === 3 &&
            (syncPathContains(item.srcDir, otherPath) ||
              syncPathContains(otherPath, item.srcDir))
          ) {
            item.hasConflict = true;
            hitBisyncSrc = true;
          }
        }
        if (hitDst) {
          const msgKey = `dst:${other.id}:${otherPath}`;
          if (!reported.has(msgKey)) {
            reported.add(msgKey);
            conflicts.push(
              `目标目录与任务「${otherName}」的目标位置冲突（${formatDisplayPath(otherPath)}）`,
            );
          }
        }
        if (hitBisyncSrc) {
          const msgKey = `src:${other.id}:${otherPath}`;
          if (!reported.has(msgKey)) {
            reported.add(msgKey);
            conflicts.push(
              `双向同步源目录与任务「${otherName}」的目标位置冲突（${formatDisplayPath(otherPath)}）`,
            );
          }
        }
      }
    }
  }

  const hasPreservedMapping = items.some((item) => item.preservedFromStoredMap);
  const allPreserved =
    hasPreservedMapping && items.every((item) => item.preservedFromStoredMap);

  let summary: string;
  if (allPreserved) {
    summary = "沿用任务保存时的目标位置，避免触发整目录重传或误删";
  } else {
    switch (rule) {
      case "single-src":
        summary = "单个源目录：内容直接同步到目标目录下，不额外建源目录同名文件夹";
        break;
      case "shared-parent":
        summary = `多个源目录（共同父目录 ${commonParent}）：省略共同父目录，保留其后的相对层级`;
        break;
      case "disambiguated-base-name":
        summary = "多个源目录（无共同父目录且含同名目录）：同名项保留完整路径以防覆盖";
        break;
      case "base-name":
        summary = "多个源目录（无共同父目录）：按末级文件夹名在目标目录下建子目录";
        break;
    }
  }

  return {
    rule,
    commonParent,
    summary,
    items,
    groups,
    hasPreservedMapping,
    conflicts,
  };
}

export function validateSyncPathSelection({
  srcPaths,
  dstPaths,
  storedDstMap,
  method = 0,
  existingJobs,
  currentJobId,
  engineId,
}: {
  srcPaths: readonly string[];
  dstPaths: readonly string[];
  storedDstMap?: string | null;
  method?: number;
  existingJobs?: readonly ExistingJobPathSummary[];
  currentJobId?: number;
  engineId?: number;
}): string {
  const preview = buildSyncPathPreview({
    srcPaths,
    dstPaths,
    storedDstMap,
    method,
    existingJobs,
    currentJobId,
    engineId,
  });
  return preview.conflicts[0] ?? "";
}
