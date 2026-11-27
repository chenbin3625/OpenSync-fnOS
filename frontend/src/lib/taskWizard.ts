import type {
  NativeLocalRoot,
  OneWayDeleteMode,
  SyncDirectionMode,
} from "../types";
import { stripNativeLocalRootID } from "./syncPathPreview";
import type { JobForm } from "./taskForm";

export interface WizardFolderChildNode {
  name: string;
  fullPath: string;
}

/**
 * Normalizes `api.paths` directory listing items into child folder nodes without
 * duplicating parent prefixes. The backend returns full virtual paths in
 * `item.path` (e.g. `/本地/sync~92618e31210e/rime`, `/jianguoyun/sync`) and
 * display names in `item.name`; legacy mocks may return relative names only.
 */
export function normalizeWizardFolderChildren(
  parentPath: string,
  entries?: readonly { name?: string; path?: string }[] | null,
): WizardFolderChildNode[] {
  const cleanParent =
    parentPath === "/" ? "/" : `/${parentPath.replace(/^\/+|\/+$/g, "")}`;
  const seen = new Set<string>();
  const out: WizardFolderChildNode[] = [];
  for (const item of entries || []) {
    const rawPath = String(item?.path ?? "").trim();
    const rawName = String(item?.name ?? "").trim();
    let fullPath = "";
    let name = "";
    if (rawPath.startsWith("/")) {
      const trimmedPath = rawPath.replace(/^\/+|\/+$/g, "");
      if (!trimmedPath) continue;
      const segs = trimmedPath.split("/").filter(Boolean);
      const lastSeg = segs[segs.length - 1] || "";
      if (!lastSeg || lastSeg === "." || lastSeg === "..") continue;
      fullPath = `/${trimmedPath}`;
      name =
        rawName && rawName !== "." && rawName !== ".." ? rawName : lastSeg;
    } else {
      const seg = (rawName || rawPath).replace(/^\/+|\/+$/g, "");
      if (!seg || seg === "." || seg === "..") continue;
      fullPath = cleanParent === "/" ? `/${seg}` : `${cleanParent}/${seg}`;
      name = seg;
    }
    if (seen.has(fullPath)) continue;
    seen.add(fullPath);
    out.push({ name, fullPath });
  }
  return out;
}

/**
 * Extracts fallback `NativeLocalRoot` items from `/本地` child listings when
 * `localRoots` metadata was not supplied separately.
 */
export function extractFallbackLocalRoots(
  entries?: readonly WizardFolderChildNode[] | null,
): NativeLocalRoot[] {
  const out: NativeLocalRoot[] = [];
  const seen = new Set<string>();
  for (const entry of entries || []) {
    const segs = entry.fullPath.replace(/^\/+/, "").split("/").filter(Boolean);
    const id = segs[segs.length - 1] || entry.name;
    if (!id || seen.has(id)) continue;
    seen.add(id);
    out.push({
      id,
      path: entry.name.startsWith("/") ? entry.name : "",
    });
  }
  return out;
}

export interface LocalRootDisplayItem {
  id: string;
  virtualPath: string;
  storagePath: string;
  hostPath: string;
  baseName: string;
  suffix: string;
  label: string;
}

export function isNativeLocalPath(
  rawPath?: string | null,
  localRoots?: readonly NativeLocalRoot[] | null,
): boolean {
  if (!rawPath) return false;
  const clean = normalizeVirtualPath(rawPath);
  const first = clean.replace(/^\/+/, "").split("/").filter(Boolean)[0];
  if (first === "本地") return true;
  if (localRoots?.length) {
    for (const root of localRoots) {
      if (!root?.path) continue;
      const rootClean = normalizeVirtualPath(root.path);
      if (
        rootClean !== "/" &&
        (clean === rootClean || clean.startsWith(`${rootClean}/`))
      ) {
        return true;
      }
    }
  }
  return false;
}

function normalizeVirtualPath(rawPath: string): string {
  const cleaned = "/" + rawPath.replace(/^\/+|\/+$/g, "");
  return cleaned === "/" ? "/" : cleaned;
}

function hostParentSegments(hostPath: string, baseName: string): string[] {
  const segs = hostPath.replace(/\\/g, "/").split("/").filter(Boolean);
  if (
    segs.length > 0 &&
    segs[segs.length - 1]?.toLowerCase() === baseName.toLowerCase()
  ) {
    return segs.slice(0, -1);
  }
  return segs;
}

/**
 * Formats NAS authorized roots for display:
 * - Never shows `/本地` or the `~12hex` backend disambiguation fingerprint.
 * - Appends a concise distinguishing volume/parent suffix ONLY when two roots
 *   share the same visible folder name.
 */
export function formatLocalRootDisplayItems(
  localRoots?: readonly NativeLocalRoot[] | null,
): LocalRootDisplayItem[] {
  if (!localRoots?.length) return [];
  const rawItems = localRoots.map((root) => {
    const baseName = stripNativeLocalRootID(root.id).base || root.id;
    const hostPath = root.path ? normalizeVirtualPath(root.path) : "";
    const virtualPath = `/本地/${root.id}`;
    return {
      id: root.id,
      virtualPath,
      storagePath: hostPath || virtualPath,
      hostPath,
      baseName,
    };
  });

  const groups = new Map<string, typeof rawItems>();
  for (const item of rawItems) {
    const key = item.baseName.toLowerCase();
    const list = groups.get(key) ?? [];
    list.push(item);
    groups.set(key, list);
  }

  const suffixById = new Map<string, string>();
  for (const list of groups.values()) {
    if (list.length <= 1) continue;
    const parentLists = list.map((item) =>
      hostParentSegments(item.hostPath, item.baseName),
    );
    // Find the first differing parent segment across colliding roots (e.g. vol1 vs vol2),
    // falling back to trailing differing segments or sequential index.
    const maxLen = Math.max(...parentLists.map((s) => s.length));
    const chosenCandidates = list.map((_, idx) => {
      const mySegs = parentLists[idx];
      for (let i = 0; i < maxLen; i++) {
        const val = mySegs[i];
        if (!val) continue;
        const distinctFromAll = parentLists.every(
          (otherSegs, oIdx) => oIdx === idx || otherSegs[i] !== val,
        );
        if (distinctFromAll) return val;
      }
      if (mySegs.length > 0) {
        return mySegs.slice(-2).join("/");
      }
      return `位置 ${idx + 1}`;
    });

    const used = new Map<string, number>();
    list.forEach((item, idx) => {
      let cand = chosenCandidates[idx];
      const count = used.get(cand) ?? 0;
      used.set(cand, count + 1);
      if (count > 0) cand = `${cand} #${count + 1}`;
      suffixById.set(item.id, cand);
    });
  }

  return rawItems
    .map((item) => {
      const suffix = suffixById.get(item.id) ?? "";
      return {
        ...item,
        suffix,
        label: suffix ? `${item.baseName} (${suffix})` : item.baseName,
      };
    })
    .sort((a, b) => a.label.localeCompare(b.label, "zh-Hans-CN"));
}

/**
 * Formats a selected NAS path for display:
 * - Real host paths (`/vol1/1000/主文件夹/sync/rime`) are shown verbatim.
 * - Legacy virtual paths (`/本地/Photos~a1b2c3d4e5f6/2026/Summer`) hide `/本地`
 *   and `~12hex`, using collision-safe root labels when known.
 */
export function formatNasDisplayPath(
  rawNasPath: string,
  localRoots?: readonly NativeLocalRoot[] | null,
): string {
  const clean = normalizeVirtualPath(rawNasPath);
  if (clean === "/" || clean === "/本地") return "";
  const segs = clean.replace(/^\/+/, "").split("/").filter(Boolean);
  if (segs[0] !== "本地") {
    return clean;
  }
  const rootId = segs[1];
  if (!rootId) return "";
  const rest = segs.slice(2);
  const displayItems = formatLocalRootDisplayItems(localRoots);
  const matched = displayItems.find((item) => item.id === rootId);
  const rootLabel = matched
    ? matched.label
    : stripNativeLocalRootID(rootId).base;
  return [rootLabel, ...rest].filter(Boolean).join("/");
}

export function extractRemoteRootAndSubPath(rawRemotePath: string): {
  remoteName: string;
  subPath: string;
} {
  const clean = normalizeVirtualPath(rawRemotePath);
  if (clean === "/") return { remoteName: "", subPath: "" };
  const segs = clean.replace(/^\/+/, "").split("/").filter(Boolean);
  return {
    remoteName: segs[0] || "",
    subPath: segs.slice(1).join("/"),
  };
}

/**
 * Determines whether an existing or draft job fits the standard 3-step wizard:
 * - Bidirectional (method=3): 1 NAS ↔ 1 Cloud
 * - One-way (method=0/1): 1+ source directories on one side → 1 destination on the other side
 * Cloud-to-cloud, local-to-local, multi-dst, move mode (method=2), and legacy
 * non-continuous schedules open in Free Mode.
 */
export function isWizardCompatibleExistingJob(
  form: Pick<JobForm, "method" | "isCron" | "srcPath" | "dstPath">,
  localRoots?: readonly NativeLocalRoot[] | null,
): boolean {
  if (form.method === 2 || form.isCron !== 3) return false;
  if (form.srcPath.length === 0 || form.dstPath.length !== 1) return false;
  if (form.method === 3 && form.srcPath.length !== 1) return false;
  const dstLocal = isNativeLocalPath(form.dstPath[0], localRoots);
  return form.srcPath.every(
    (p) => Boolean(p) && isNativeLocalPath(p, localRoots) !== dstLocal,
  );
}

/**
 * Checks whether a Free Mode form can return cleanly to the Standard Wizard.
 * Allows empty selection, 1:1 bidirectional, or one-way multi-source -> single-dst
 * between NAS local and cloud remote.
 */
export function canSwitchBackToWizard(
  form: Pick<JobForm, "method" | "isCron" | "srcPath" | "dstPath">,
  localRoots?: readonly NativeLocalRoot[] | null,
): boolean {
  if (form.method === 2 || form.isCron !== 3) return false;
  if (form.srcPath.length === 0 && form.dstPath.length === 0) return true;
  if (form.dstPath.length > 1) return false;
  if (form.method === 3 && form.srcPath.length > 1) return false;
  const srcLocalFlags = form.srcPath
    .filter(Boolean)
    .map((p) => isNativeLocalPath(p, localRoots));
  const dstLocalFlags = form.dstPath
    .filter(Boolean)
    .map((p) => isNativeLocalPath(p, localRoots));
  if (srcLocalFlags.length > 1 && new Set(srcLocalFlags).size > 1) {
    return false;
  }
  if (srcLocalFlags.length > 0 && dstLocalFlags.length > 0) {
    return srcLocalFlags[0] !== dstLocalFlags[0];
  }
  return true;
}

export function extractWizardMultiPaths(
  form: Pick<JobForm, "srcPath" | "dstPath">,
  localRoots?: readonly NativeLocalRoot[] | null,
): { nasPaths: string[]; remotePaths: string[] } {
  const all = [...form.srcPath, ...form.dstPath].filter(Boolean);
  const nasPaths: string[] = [];
  const remotePaths: string[] = [];
  for (const p of all) {
    if (isNativeLocalPath(p, localRoots)) {
      if (!nasPaths.includes(p)) nasPaths.push(p);
    } else if (!remotePaths.includes(p)) {
      remotePaths.push(p);
    }
  }
  return { nasPaths, remotePaths };
}

export function extractWizardPaths(
  form: Pick<JobForm, "srcPath" | "dstPath">,
  localRoots?: readonly NativeLocalRoot[] | null,
): { nasPath: string; remotePath: string } {
  const { nasPaths, remotePaths } = extractWizardMultiPaths(form, localRoots);
  return { nasPath: nasPaths[0] || "", remotePath: remotePaths[0] || "" };
}

export function inferWizardDirection(
  form: Pick<JobForm, "method" | "srcPath" | "dstPath" | "extraFlags">,
  localRoots?: readonly NativeLocalRoot[] | null,
): SyncDirectionMode {
  if (form.method === 3) return "bidirectional";
  const stored = form.extraFlags?.syncDirectionMode;
  if (stored === "download") {
    if (!form.srcPath.length && !form.dstPath.length) return "download";
    if (
      !isNativeLocalPath(form.srcPath[0], localRoots) &&
      isNativeLocalPath(form.dstPath[0], localRoots)
    ) {
      return "download";
    }
  }
  if (stored === "upload") {
    if (!form.srcPath.length && !form.dstPath.length) return "upload";
    if (
      isNativeLocalPath(form.srcPath[0], localRoots) &&
      !isNativeLocalPath(form.dstPath[0], localRoots)
    ) {
      return "upload";
    }
  }
  if (
    !isNativeLocalPath(form.srcPath[0], localRoots) &&
    isNativeLocalPath(form.dstPath[0], localRoots)
  ) {
    return "download";
  }
  return "upload";
}

export function inferOneWayDeleteMode(
  form: Pick<JobForm, "method" | "extraFlags">,
): OneWayDeleteMode {
  if (form.method === 1) return "mirror";
  if (form.extraFlags?.oneWayDeleteMode === "tracked") return "tracked";
  if (form.extraFlags?.oneWayDeleteMode === "mirror") return "mirror";
  return "none";
}

export function applyWizardMultiEndpoints(
  direction: SyncDirectionMode,
  deleteMode: OneWayDeleteMode,
  nasPaths: readonly string[],
  remotePaths: readonly string[],
): { srcPath: string[]; dstPath: string[]; method: number } {
  const cleanNas = nasPaths.map((p) => p.trim()).filter(Boolean);
  const cleanRemote = remotePaths.map((p) => p.trim()).filter(Boolean);
  if (direction === "bidirectional") {
    return {
      srcPath: cleanNas.slice(0, 1),
      dstPath: cleanRemote.slice(0, 1),
      method: 3,
    };
  }
  const method = deleteMode === "mirror" ? 1 : 0;
  if (direction === "download") {
    return {
      srcPath: cleanRemote,
      dstPath: cleanNas.slice(0, 1),
      method,
    };
  }
  return {
    srcPath: cleanNas,
    dstPath: cleanRemote.slice(0, 1),
    method,
  };
}

export function applyWizardEndpoints(
  direction: SyncDirectionMode,
  deleteMode: OneWayDeleteMode,
  nasPath: string,
  remotePath: string,
): { srcPath: string[]; dstPath: string[]; method: number } {
  return applyWizardMultiEndpoints(
    direction,
    deleteMode,
    nasPath ? [nasPath] : [],
    remotePath ? [remotePath] : [],
  );
}
