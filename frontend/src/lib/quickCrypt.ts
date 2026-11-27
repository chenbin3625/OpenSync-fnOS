import type { NativeRemoteItem } from "../types";
import { resolveRemoteChain, validateManualSyncPath } from "./taskForm";

export interface EncryptableDstTarget {
  /** Normalized sync path selected in the task form, e.g. `/s3/bucket/backup`. */
  syncPath: string;
  /** Canonical remote name from native storage config, e.g. `s3`. */
  remoteName: string;
  /** Relative path beneath the remote, without leading/trailing slashes. */
  subPath: string;
  /** Underlying rclone remote spec, e.g. `s3:bucket/backup` or `aliyun:`. */
  rcloneRemote: string;
  /** Existing crypt remotes that already cover this target or its ancestor. */
  reusableCrypts: ReusableCryptMatch[];
  /** Existing crypt remotes configured beneath this target (potential overlap). */
  overlappingCrypts: string[];
}

export interface ReusableCryptMatch {
  /** Existing crypt remote name. */
  cryptName: string;
  /** The crypt remote's underlying rclone target, e.g. `s3:bucket`. */
  underlyingRemote: string;
  /** Sync path to switch the task destination to, e.g. `/s3-crypt` or `/s3-crypt/backup`. */
  encryptedSyncPath: string;
  /** True when the existing crypt points to the exact same directory. */
  exact: boolean;
}

export interface DstEncryptionAnalysis {
  /** Valid, writable, non-local destination paths that do not yet have crypt in their chain. */
  encryptable: EncryptableDstTarget[];
  /** Destination paths that already resolve through a crypt remote. */
  encrypted: Array<{
    syncPath: string;
    remoteName: string;
  }>;
}

export interface QuickCryptFormValues {
  name: string;
  subDir: string;
  password: string;
  confirmPassword: string;
  password2: string;
  filenameEncryption: "standard" | "obfuscate" | "off";
  filenameEncoding: "base32" | "base64" | "base32768";
  directoryNameEncryption: "true" | "false";
  noDataEncryption: boolean;
}

export interface QuickCryptValidationErrors {
  name?: string;
  subDir?: string;
  password?: string;
  confirmPassword?: string;
  password2?: string;
}

export interface StorageCryptTarget extends EncryptableDstTarget {
  /** True when the backing backend requires a bucket/container/share prefix (e.g. S3, SMB). */
  requiresSubDir: boolean;
  /** Underlying storage backend type, e.g. `s3`, `webdav`, `smb`. */
  leafBackend: string;
}

const containerRequiredBackends = new Set([
  "s3",
  "smb",
  "b2",
  "azureblob",
  "googlecloudstorage",
  "swift",
  "oracleobjectstorage",
  "qingstor",
  "storj",
]);

const nonEncryptableStorageTypes = new Set([
  "local",
  "crypt",
  "http",
  "archive",
  "chunker",
  "compress",
  "hasher",
  "alias",
  "union",
  "combine",
]);

export function isContainerRequiredBackend(backend: string): boolean {
  return containerRequiredBackends.has(backend.trim().toLowerCase());
}

/**
 * Returns true when a storage card in「存储管理」represents a genuine writable
 * remote storage that can directly back a new encrypted (Crypt) view.
 */
export function canCreateCryptFromRemote(
  remote: NativeRemoteItem,
  remotes: readonly NativeRemoteItem[],
): boolean {
  const typ = remote.type.trim().toLowerCase();
  if (!typ || nonEncryptableStorageTypes.has(typ)) {
    return false;
  }
  const chain = resolveRemoteChain(remote.name, remotes);
  if (!chain.found || chain.readOnly || chain.hasCrypt) {
    return false;
  }
  return true;
}

/**
 * Builds the QuickCrypt target descriptor for creating an encrypted storage
 * directly from a real remote storage card in「存储管理」.
 */
export function buildStorageCryptTarget(
  remote: NativeRemoteItem,
  remotes: readonly NativeRemoteItem[],
): StorageCryptTarget {
  const { reusable, overlapping } = findReusableCryptRemotes(
    remote.name,
    "",
    remotes,
  );
  return {
    syncPath: `/${remote.name}`,
    remoteName: remote.name,
    subPath: "",
    rcloneRemote: `${remote.name}:`,
    reusableCrypts: reusable,
    overlappingCrypts: overlapping,
    requiresSubDir: isContainerRequiredBackend(remote.type),
    leafBackend: remote.type,
  };
}

// Mirrors validateRemoteName in NativeStorageEditor.tsx and validName in
// backend/internal/native/config.go.
const remoteNamePattern = /^[\p{L}\p{N}_][\p{L}\p{N}_. -]{0,63}$/u;
const controlCharPattern = /[\u0000-\u001f\u007f]/;

export function validateCryptRemoteName(
  raw: string,
  existingRemotes?: readonly NativeRemoteItem[],
): string {
  const name = raw.trim();
  if (!name) {
    return "请输入加密存储名称";
  }
  if (
    name === "本地" ||
    name.toLowerCase() === "rclone_config" ||
    name.toLowerCase().startsWith("_opensync_")
  ) {
    return "不能使用系统保留名称";
  }
  if (!remoteNamePattern.test(name)) {
    return "存储名称需以字母、数字、中文或下划线开头（1–64 个字符），不能包含 / : \\ 等特殊字符";
  }
  if (
    existingRemotes?.some(
      (item) => item.name.toLowerCase() === name.toLowerCase(),
    )
  ) {
    return `存储「${name}」已存在，请换一个名称`;
  }
  return "";
}

export function normalizeRelativeSubDir(raw: string): {
  normalized: string;
  error: string;
} {
  const trimmed = raw.trim();
  if (!trimmed) {
    return { normalized: "", error: "" };
  }
  if (trimmed.includes("\\") || trimmed.includes(":") || controlCharPattern.test(trimmed)) {
    return {
      normalized: "",
      error: "子目录不能包含反斜杠、冒号或控制字符",
    };
  }
  const stripped = trimmed.replace(/^\/+|\/+$/g, "");
  if (!stripped) {
    return { normalized: "", error: "" };
  }
  const segs = stripped.split("/");
  if (segs.some((seg) => !seg || seg === "." || seg === "..")) {
    return {
      normalized: "",
      error: "子目录不能包含空段、. 或 ..",
    };
  }
  return { normalized: segs.join("/"), error: "" };
}

function parseRcloneRemoteSpec(raw: string): { ref: string; sub: string } | null {
  const trimmed = raw.trim();
  const colonIdx = trimmed.indexOf(":");
  if (colonIdx <= 0) return null;
  const ref = trimmed.slice(0, colonIdx).trim();
  const sub = trimmed.slice(colonIdx + 1).replace(/^\/+|\/+$/g, "");
  if (!ref) return null;
  return { ref, sub };
}

export function composeCryptUnderlyingRemote(
  remoteName: string,
  baseSubPath: string,
  extraSubDir = "",
): string {
  const base = baseSubPath.replace(/^\/+|\/+$/g, "");
  const extra = extraSubDir.replace(/^\/+|\/+$/g, "");
  const combined = [base, extra].filter(Boolean).join("/");
  return combined ? `${remoteName}:${combined}` : `${remoteName}:`;
}

export function findReusableCryptRemotes(
  remoteName: string,
  subPath: string,
  remotes: readonly NativeRemoteItem[],
): {
  reusable: ReusableCryptMatch[];
  overlapping: string[];
} {
  const targetRefLower = remoteName.trim().toLowerCase();
  const targetSub = subPath.replace(/^\/+|\/+$/g, "");
  const reusable: ReusableCryptMatch[] = [];
  const overlapping: string[] = [];

  for (const item of remotes) {
    if (item.type !== "crypt") continue;
    const parsed = parseRcloneRemoteSpec(item.options?.remote || "");
    if (!parsed || parsed.ref.toLowerCase() !== targetRefLower) continue;

    if (parsed.sub === targetSub) {
      reusable.push({
        cryptName: item.name,
        underlyingRemote: item.options?.remote || `${parsed.ref}:${parsed.sub}`,
        encryptedSyncPath: `/${item.name}`,
        exact: true,
      });
    } else if (
      parsed.sub === "" ||
      (targetSub && targetSub.startsWith(`${parsed.sub}/`))
    ) {
      const rel = parsed.sub ? targetSub.slice(parsed.sub.length + 1) : targetSub;
      reusable.push({
        cryptName: item.name,
        underlyingRemote: item.options?.remote || `${parsed.ref}:${parsed.sub}`,
        encryptedSyncPath: rel ? `/${item.name}/${rel}` : `/${item.name}`,
        exact: false,
      });
    } else if (
      targetSub === "" ||
      parsed.sub.startsWith(`${targetSub}/`)
    ) {
      overlapping.push(item.name);
    }
  }

  reusable.sort((a, b) => Number(b.exact) - Number(a.exact) || a.cryptName.localeCompare(b.cryptName));
  return { reusable, overlapping };
}

export function analyzeDstEncryption(
  dstPaths: readonly string[],
  remotes?: readonly NativeRemoteItem[],
  method = 0,
): DstEncryptionAnalysis {
  const encryptable: EncryptableDstTarget[] = [];
  const encrypted: Array<{ syncPath: string; remoteName: string }> = [];
  if (!remotes?.length || !dstPaths.length) {
    return { encryptable, encrypted };
  }

  const seenEncryptable = new Set<string>();
  const seenEncrypted = new Set<string>();

  for (const rawPath of dstPaths) {
    const check = validateManualSyncPath(rawPath, remotes, { role: "dst", method });
    if (check.error) continue;
    const normalizedPath = check.normalized;
    const segs = normalizedPath.replace(/^\/+/, "").split("/").filter(Boolean);
    const first = segs[0];
    if (!first || first === "本地") continue;
    const subPath = segs.slice(1).join("/");
    const chain = resolveRemoteChain(first, remotes, new Set(), subPath);
    if (!chain.found || chain.readOnly) continue;

    if (chain.hasCrypt) {
      if (!seenEncrypted.has(normalizedPath)) {
        seenEncrypted.add(normalizedPath);
        encrypted.push({
          syncPath: normalizedPath,
          remoteName: chain.canonicalName,
        });
      }
      continue;
    }

    if (seenEncryptable.has(normalizedPath)) continue;
    seenEncryptable.add(normalizedPath);

    const { reusable, overlapping } = findReusableCryptRemotes(
      chain.canonicalName,
      subPath,
      remotes,
    );
    encryptable.push({
      syncPath: normalizedPath,
      remoteName: chain.canonicalName,
      subPath,
      rcloneRemote: composeCryptUnderlyingRemote(chain.canonicalName, subPath),
      reusableCrypts: reusable,
      overlappingCrypts: overlapping,
    });
  }

  return { encryptable, encrypted };
}

function sanitizeNameFragment(raw: string): string {
  return raw
    .trim()
    .replace(/[^\p{L}\p{N}_.-]+/gu, "-")
    .replace(/^[-.]+|[-.]+$/g, "");
}

export function suggestCryptRemoteName(
  remoteName: string,
  subPath: string,
  existingRemotes?: readonly NativeRemoteItem[],
): string {
  const existingLower = new Set(
    (existingRemotes || []).map((item) => item.name.toLowerCase()),
  );
  const remoteFrag = sanitizeNameFragment(remoteName) || "vault";
  const lastSeg = subPath.split("/").filter(Boolean).pop() || "";
  const subFrag = sanitizeNameFragment(lastSeg);

  const rawBase = subFrag ? `${remoteFrag}-${subFrag}-crypt` : `${remoteFrag}-crypt`;
  // Ensure room for numeric collision suffix (e.g. "-99") within the 64-char cap.
  let base = rawBase.slice(0, 58).replace(/[-.]+$/g, "");
  if (!base || !remoteNamePattern.test(base) || base.toLowerCase().startsWith("_opensync_")) {
    base = `${remoteFrag.slice(0, 50)}-crypt`;
  }
  if (!remoteNamePattern.test(base) || base.toLowerCase().startsWith("_opensync_")) {
    base = "crypt-vault";
  }

  if (!existingLower.has(base.toLowerCase())) {
    return base;
  }
  for (let i = 2; i < 1000; i++) {
    const candidate = `${base}-${i}`;
    if (!existingLower.has(candidate.toLowerCase())) {
      return candidate;
    }
  }
  return `${base}-${Date.now() % 10000}`;
}

export function validateQuickCryptForm(
  values: QuickCryptFormValues,
  existingRemotes?: readonly NativeRemoteItem[],
  options?: {
    requireSubDir?: boolean;
    subDirRequiredMessage?: string;
  },
): QuickCryptValidationErrors {
  const errors: QuickCryptValidationErrors = {};
  const nameErr = validateCryptRemoteName(values.name, existingRemotes);
  if (nameErr) errors.name = nameErr;

  const subCheck = normalizeRelativeSubDir(values.subDir);
  if (subCheck.error) {
    errors.subDir = subCheck.error;
  } else if (options?.requireSubDir && !subCheck.normalized) {
    errors.subDir =
      options.subDirRequiredMessage ||
      "请填写存储桶或共享目录路径（例如 my-bucket/encrypted）";
  }

  if (!values.password) {
    errors.password = "请输入加密密码";
  } else if (controlCharPattern.test(values.password) || values.password.length > 65536) {
    errors.password = "加密密码不能包含控制字符或超过长度限制";
  }

  if (!values.confirmPassword) {
    errors.confirmPassword = "请再次输入加密密码以确认";
  } else if (values.password && values.confirmPassword !== values.password) {
    errors.confirmPassword = "两次输入的加密密码不一致";
  }

  if (values.password2 && (controlCharPattern.test(values.password2) || values.password2.length > 65536)) {
    errors.password2 = "加密盐不能包含控制字符或超过长度限制";
  }

  return errors;
}

export function buildQuickCryptOptions(
  target: Pick<EncryptableDstTarget, "remoteName" | "subPath">,
  values: QuickCryptFormValues,
  meta?: {
    managedByJob?: boolean;
    managedJobLabel?: string;
  },
): Record<string, string> {
  const subCheck = normalizeRelativeSubDir(values.subDir);
  const options: Record<string, string> = {
    type: "crypt",
    remote: composeCryptUnderlyingRemote(
      target.remoteName,
      target.subPath,
      subCheck.normalized,
    ),
    password: values.password,
  };
  if (values.password2) {
    options.password2 = values.password2;
  }
  if (values.filenameEncryption && values.filenameEncryption !== "standard") {
    options.filename_encryption = values.filenameEncryption;
  }
  if (values.filenameEncoding && values.filenameEncoding !== "base32") {
    options.filename_encoding = values.filenameEncoding;
  }
  if (values.directoryNameEncryption === "false") {
    options.directory_name_encryption = "false";
  }
  if (values.noDataEncryption) {
    options.no_data_encryption = "true";
  }
  if (meta?.managedByJob) {
    options.managed_by_job = "true";
    const label = meta.managedJobLabel?.trim();
    if (label) {
      options.managed_job_label = label;
    }
  }
  return options;
}

export function replaceDstPathWithEncrypted(
  dstPaths: readonly string[],
  targetSyncPath: string,
  encryptedSyncPath: string,
): string[] {
  const normalize = (p: string) => {
    const cleaned = "/" + p.replace(/^\/+|\/+$/g, "");
    return cleaned === "/" ? "/" : cleaned;
  };
  const targetNorm = normalize(targetSyncPath);
  const next: string[] = [];
  let replaced = false;
  for (const raw of dstPaths) {
    if (!replaced && normalize(raw) === targetNorm) {
      replaced = true;
      if (!next.includes(encryptedSyncPath)) {
        next.push(encryptedSyncPath);
      }
    } else if (!next.includes(raw)) {
      next.push(raw);
    }
  }
  if (!replaced && !next.includes(encryptedSyncPath)) {
    next.push(encryptedSyncPath);
  }
  return next;
}
