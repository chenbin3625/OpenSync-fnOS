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
  hostPath: string;
  baseName: string;
  suffix: string;
  label: string;
}

export function isNativeLocalPath(rawPath?: string | null): boolean {
  if (!rawPath) return false;
  const first = rawPath.replace(/^\/+/, "").split("/").filter(Boolean)[0];
  return first === "本地";
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
    return {
      id: root.id,
      virtualPath: `/本地/${root.id}`,
      hostPath: root.path || "",
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
 * Formats a selected NAS virtual path (`/本地/Photos~a1b2c3d4e5f6/2026/Summer`)
 * without exposing `/本地` or `~12hex`. Uses collision-safe root labels when known.
 */
export function formatNasDisplayPath(
  rawNasPath: string,
  localRoots?: readonly NativeLocalRoot[] | null,
): string {
  const clean = normalizeVirtualPath(rawNasPath);
  if (clean === "/" || clean === "/本地") return "";
  const segs = clean.replace(/^\/+/, "").split("/").filter(Boolean);
  if (segs[0] !== "本地") {
    return clean.replace(/^\/+/, "");
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
 * Determines whether an existing or draft job fits the 1:1 "NAS 本地 ↔ 云端网盘"
 * standard 3-step wizard. Cloud-to-cloud, local-to-local, multi-source, multi-dst,
 * move mode (method=2), and legacy non-continuous schedules open in Free Mode.
 */
export function isWizardCompatibleExistingJob(
  form: Pick<JobForm, "method" | "isCron" | "srcPath" | "dstPath">,
): boolean {
  if (form.method === 2 || form.isCron !== 3) return false;
  if (form.srcPath.length !== 1 || form.dstPath.length !== 1) return false;
  const srcLocal = isNativeLocalPath(form.srcPath[0]);
  const dstLocal = isNativeLocalPath(form.dstPath[0]);
  return srcLocal !== dstLocal;
}

/**
 * Checks whether a Free Mode form can return cleanly to the Standard Wizard.
 * Allows empty selection or at most one NAS path paired with at most one cloud path.
 */
export function canSwitchBackToWizard(
  form: Pick<JobForm, "method" | "isCron" | "srcPath" | "dstPath">,
): boolean {
  if (form.method === 2 || form.isCron !== 3) return false;
  if (form.srcPath.length === 0 && form.dstPath.length === 0) return true;
  if (form.srcPath.length > 1 || form.dstPath.length > 1) return false;
  const all = [...form.srcPath, ...form.dstPath];
  const localCount = all.filter((p) => isNativeLocalPath(p)).length;
  const remoteCount = all.length - localCount;
  return localCount <= 1 && remoteCount <= 1;
}

export function extractWizardPaths(
  form: Pick<JobForm, "srcPath" | "dstPath">,
): { nasPath: string; remotePath: string } {
  const all = [...form.srcPath, ...form.dstPath];
  const nasPath = all.find((p) => isNativeLocalPath(p)) || "";
  const remotePath = all.find((p) => p && !isNativeLocalPath(p)) || "";
  return { nasPath, remotePath };
}

export function inferWizardDirection(
  form: Pick<JobForm, "method" | "srcPath" | "dstPath" | "extraFlags">,
): SyncDirectionMode {
  if (form.method === 3) return "bidirectional";
  const stored = form.extraFlags?.syncDirectionMode;
  if (stored === "download") {
    if (!form.srcPath.length && !form.dstPath.length) return "download";
    if (!isNativeLocalPath(form.srcPath[0]) && isNativeLocalPath(form.dstPath[0])) {
      return "download";
    }
  }
  if (stored === "upload") {
    if (!form.srcPath.length && !form.dstPath.length) return "upload";
    if (isNativeLocalPath(form.srcPath[0]) && !isNativeLocalPath(form.dstPath[0])) {
      return "upload";
    }
  }
  if (!isNativeLocalPath(form.srcPath[0]) && isNativeLocalPath(form.dstPath[0])) {
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

export function applyWizardEndpoints(
  direction: SyncDirectionMode,
  deleteMode: OneWayDeleteMode,
  nasPath: string,
  remotePath: string,
): { srcPath: string[]; dstPath: string[]; method: number } {
  const cleanNas = nasPath.trim();
  const cleanRemote = remotePath.trim();
  if (direction === "bidirectional") {
    return {
      srcPath: cleanNas ? [cleanNas] : [],
      dstPath: cleanRemote ? [cleanRemote] : [],
      method: 3,
    };
  }
  const method = deleteMode === "mirror" ? 1 : 0;
  if (direction === "download") {
    return {
      srcPath: cleanRemote ? [cleanRemote] : [],
      dstPath: cleanNas ? [cleanNas] : [],
      method,
    };
  }
  return {
    srcPath: cleanNas ? [cleanNas] : [],
    dstPath: cleanRemote ? [cleanRemote] : [],
    method,
  };
}
