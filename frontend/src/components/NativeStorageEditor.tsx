import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconArrowUp,
  IconBarChartVStroked,
  IconChainStroked,
  IconChevronRight,
  IconDeleteStroked,
  IconDownload,
  IconEditStroked,
  IconFile,
  IconFolderStroked,
  IconInfoCircle,
  IconPlusStroked,
  IconRefresh,
  IconTickCircle,
  IconUpload,
} from "@douyinfe/semi-icons";
import {
  Editor,
  Field,
  IconButton,
  InlineNotice,
  confirmDelete,
  errorToast,
} from "./common";
import { api } from "../api/client";
import {
  NativeStorageAuthWizard,
  type CompletedAuthSession,
} from "./NativeStorageAuthWizard";
import { formatSize } from "../pages/Home/homeUtils";
import { formatTimestamp } from "../utils/date";
import type {
  AlistItem,
  NativeBrowseResult,
  NativeDirEntry,
  NativeImportPreview,
  NativeRemoteItem,
  NativeSizeInfo,
  NativeStorageState,
  NativeStorageTestResult,
  NativeTransferTask,
  NativeTrashItem,
} from "../types";

type RemoteFormType =
  | "s3"
  | "webdav"
  | "quark"
  | "pan123"
  | "sftp"
  | "smb"
  | "ftp"
  | "http"
  | "b2"
  | "onedrive"
  | "drive"
  | "dropbox"
  | "box"
  | "pcloud"
  | "mega"
  | "azureblob"
  | "azurefiles"
  | "googlecloudstorage"
  | "swift"
  | "oracleobjectstorage"
  | "qingstor"
  | "storj"
  | "yandex"
  | "koofr"
  | "seafile"
  | "pikpak"
  | "protondrive"
  | "iclouddrive"
  | "jottacloud"
  | "filen"
  | "hidrive"
  | "huaweidrive"
  | "mailru"
  | "fichier"
  | "gofile"
  | "pixeldrain"
  | "crypt"
  | "chunker"
  | "compress"
  | "hasher"
  | "alias"
  | "archive"
  | "union"
  | "combine"
  | "local";

export type NativeEditorMode =
  | "local"
  | "form"
  | "import"
  | "export"
  | "browse";

export const remoteTypeLabels: Record<string, string> = {
  s3: "S3 兼容存储",
  webdav: "WebDAV",
  quark: "夸克网盘",
  pan123: "123云盘",
  sftp: "SFTP",
  smb: "SMB",
  ftp: "FTP / FTPS",
  http: "HTTP 只读源",
  b2: "Backblaze B2",
  onedrive: "Microsoft OneDrive",
  drive: "Google Drive",
  dropbox: "Dropbox",
  box: "Box",
  pcloud: "pCloud",
  mega: "MEGA",
  azureblob: "Azure Blob",
  azurefiles: "Azure Files",
  googlecloudstorage: "Google Cloud Storage",
  swift: "OpenStack Swift",
  oracleobjectstorage: "Oracle Cloud Object Storage",
  qingstor: "QingStor 青云对象存储",
  storj: "Storj DCS",
  yandex: "Yandex Disk",
  koofr: "Koofr",
  seafile: "Seafile",
  pikpak: "PikPak",
  protondrive: "Proton Drive",
  iclouddrive: "iCloud Drive",
  jottacloud: "Jottacloud",
  filen: "Filen",
  hidrive: "STRATO HiDrive",
  huaweidrive: "华为云盘 / Huawei Drive",
  mailru: "Mail.ru Cloud",
  fichier: "1Fichier",
  gofile: "Gofile",
  pixeldrain: "Pixeldrain",
  crypt: "加密存储",
  chunker: "Chunker 分片",
  compress: "Compress 压缩",
  hasher: "Hasher 哈希缓存",
  alias: "Alias 路径别名",
  archive: "Archive 压缩包浏览（只读）",
  union: "Union 多源聚合",
  combine: "Combine 多存储目录组合",
  local: "本地目录别名",
};

export interface RemoteTypeGroup {
  key: string;
  label: string;
  types: readonly RemoteFormType[];
}

// Only genuine, writable remote storage backends are exposed when adding a new
// remote storage. Virtual/meta wrappers, read-only sources, and temporary file
// hosts remain in `formTypes` so existing or imported rclone.conf entries stay
// fully editable and compatible.
export const remoteTypeGroups: readonly RemoteTypeGroup[] = [
  {
    key: "protocols",
    label: "常用协议与自建存储",
    types: ["s3", "webdav", "sftp", "smb", "ftp", "seafile"],
  },
  {
    key: "cloud_drives",
    label: "公有网盘",
    types: [
      "quark",
      "pan123",
      "onedrive",
      "drive",
      "dropbox",
      "iclouddrive",
      "huaweidrive",
      "box",
      "pcloud",
      "mega",
      "pikpak",
      "protondrive",
      "yandex",
      "koofr",
      "jottacloud",
      "filen",
      "hidrive",
      "mailru",
    ],
  },
  {
    key: "object_storage",
    label: "对象存储与云存储",
    types: [
      "b2",
      "azureblob",
      "azurefiles",
      "googlecloudstorage",
      "swift",
      "oracleobjectstorage",
      "qingstor",
      "storj",
    ],
  },
];

export const creatableRemoteTypes: RemoteFormType[] = remoteTypeGroups.flatMap(
  (group) => [...group.types],
);

const creatableRemoteTypeSet = new Set<RemoteFormType>(creatableRemoteTypes);

const remoteTypeGroupLabelByType = new Map<RemoteFormType, string>(
  remoteTypeGroups.flatMap((group) =>
    group.types.map((t) => [t, group.label] as const),
  ),
);

const formTypes: RemoteFormType[] = [
  "s3",
  "webdav",
  "quark",
  "pan123",
  "sftp",
  "smb",
  "ftp",
  "http",
  "b2",
  "onedrive",
  "drive",
  "dropbox",
  "box",
  "pcloud",
  "mega",
  "azureblob",
  "azurefiles",
  "googlecloudstorage",
  "swift",
  "oracleobjectstorage",
  "qingstor",
  "storj",
  "yandex",
  "koofr",
  "seafile",
  "pikpak",
  "protondrive",
  "iclouddrive",
  "jottacloud",
  "filen",
  "hidrive",
  "huaweidrive",
  "mailru",
  "fichier",
  "gofile",
  "pixeldrain",
  "crypt",
  "chunker",
  "compress",
  "hasher",
  "alias",
  "archive",
  "union",
  "combine",
  "local",
];

const oauthTypes = new Set<RemoteFormType>([
  "onedrive",
  "drive",
  "dropbox",
  "box",
  "pcloud",
  "yandex",
  "jottacloud",
  "hidrive",
  "huaweidrive",
]);

const wrapperTypes = new Set<RemoteFormType>([
  "crypt",
  "chunker",
  "compress",
  "hasher",
  "alias",
  "archive",
]);

const proxyCapableTypes = new Set<RemoteFormType>([
  "s3",
  "webdav",
  "quark",
  "pan123",
  "http",
  "b2",
  "onedrive",
  "drive",
  "dropbox",
  "box",
  "pcloud",
  "mega",
  "azureblob",
  "azurefiles",
  "googlecloudstorage",
  "swift",
  "oracleobjectstorage",
  "qingstor",
  "storj",
  "yandex",
  "koofr",
  "seafile",
  "pikpak",
  "protondrive",
  "iclouddrive",
  "jottacloud",
  "filen",
  "hidrive",
  "huaweidrive",
  "mailru",
  "fichier",
  "gofile",
  "pixeldrain",
]);

const tlsSkipCapableTypes = new Set<RemoteFormType>([
  "s3",
  "webdav",
  "quark",
  "pan123",
  "ftp",
  "http",
  "b2",
  "onedrive",
  "drive",
  "dropbox",
  "box",
  "pcloud",
  "mega",
  "azureblob",
  "azurefiles",
  "googlecloudstorage",
  "swift",
  "oracleobjectstorage",
  "qingstor",
  "storj",
  "yandex",
  "koofr",
  "seafile",
  "pikpak",
  "protondrive",
  "iclouddrive",
  "jottacloud",
  "filen",
  "hidrive",
  "huaweidrive",
  "mailru",
  "fichier",
  "gofile",
  "pixeldrain",
]);

const allSecretKeys = new Set([
  "pass",
  "secret_access_key",
  "session_token",
  "bearer_token",
  "password",
  "password2",
  "key_pem",
  "key_file_pass",
  "key",
  "token",
  "client_secret",
  "service_account_credentials",
  "sas_url",
  "auth_token",
  "application_credential_secret",
  "access_grant",
  "api_key",
  "passphrase",
  "library_key",
  "connection_string",
  "mailbox_password",
  "otp_secret_key",
  "client_access_token",
  "client_refresh_token",
  "client_salted_key_pass",
  "trust_token",
  "cookies",
  "cookie",
  "master_keys",
  "private_key",
  "file_password",
  "folder_password",
  "access_token",
  "web_password",
  "direct_link_key",
]);

const s3Providers: Record<string, string> = {
  Alibaba: "阿里云 OSS",
  TencentCOS: "腾讯云 COS",
  Cloudflare: "Cloudflare R2",
  Minio: "MinIO",
  AWS: "Amazon S3",
  DigitalOcean: "DigitalOcean Spaces",
  Wasabi: "Wasabi",
  Ceph: "Ceph Object Gateway",
  HuaweiOBS: "华为云 OBS",
  Qiniu: "七牛云 Kodo",
  Other: "Backblaze B2 / 其他 S3",
};

const s3EndpointPlaceholders: Record<string, string> = {
  Alibaba: "例如 oss-cn-hangzhou.aliyuncs.com",
  TencentCOS: "例如 cos.ap-guangzhou.myqcloud.com",
  Cloudflare: "例如 https://<account-id>.r2.cloudflarestorage.com",
  Minio: "例如 http://192.168.1.10:9000",
  AWS: "留空使用区域默认端点，或填写自定义端点",
  DigitalOcean: "例如 nyc3.digitaloceanspaces.com",
  Wasabi: "例如 s3.wasabisys.com",
  Ceph: "例如 https://ceph.example.com",
  HuaweiOBS: "例如 obs.cn-east-3.myhuaweicloud.com",
  Qiniu: "例如 s3-cn-east-1.qiniucs.com",
  Other: "例如 s3.us-west-004.backblazeb2.com 或 https://...",
};

const webdavVendors: Record<string, string> = {
  other: "标准 WebDAV（含 OpenList / AList）",
  nextcloud: "Nextcloud",
  owncloud: "ownCloud",
  rclone: "rclone / 支持修改时间的 WebDAV",
  fastmail: "Fastmail",
  sharepoint: "SharePoint",
};

interface RemoteFormDraft {
  editing: boolean;
  type: RemoteFormType;
  name: string;
  provider: string;
  vendor: string;
  forcePathStyle: boolean;
  noCheckBucket: boolean;
  authMode: "keys" | "runtime" | "anonymous";
  options: Record<string, string>;
  configuredSecrets: string[];
  clearSecrets: string[];
  authSessionId?: string;
  authPublicOptions?: Record<string, string>;
  authConfiguredSecrets?: string[];
}

export function emptyDraft(type: RemoteFormType = "s3"): RemoteFormDraft {
  return {
    editing: false,
    type,
    name: "",
    provider: "Alibaba",
    vendor: "other",
    forcePathStyle: false,
    noCheckBucket: false,
    authMode: "keys",
    options: {},
    configuredSecrets: [],
    clearSecrets: [],
    authSessionId: undefined,
    authPublicOptions: undefined,
    authConfiguredSecrets: undefined,
  };
}

export function draftFromRemote(item: NativeRemoteItem): RemoteFormDraft {
  const type = (
    formTypes.includes(item.type as RemoteFormType) ? item.type : "s3"
  ) as RemoteFormType;
  const opts = { ...(item.options || {}) };
  const provider = opts.provider || "Other";
  const vendor = opts.vendor || "other";
  const forcePathStyle = opts.force_path_style === "true";
  const noCheckBucket = opts.no_check_bucket === "true";
  if (type === "s3") delete opts.provider;
  delete opts.vendor;
  delete opts.force_path_style;
  delete opts.no_check_bucket;
  return {
    editing: true,
    type,
    name: item.name,
    provider,
    vendor,
    forcePathStyle,
    noCheckBucket,
    authMode: opts.access_key_id || item.configuredSecrets?.includes("secret_access_key")
      ? "keys"
      : opts.env_auth === "true" ? "runtime" : "anonymous",
    options: opts,
    configuredSecrets: [...(item.configuredSecrets || [])],
    clearSecrets: [],
  };
}

function snapshotDraft(d: RemoteFormDraft): string {
  const sortedOpts = Object.fromEntries(
    Object.entries(d.options)
      .filter(([, v]) => v !== "")
      .sort(([a], [b]) => a.localeCompare(b)),
  );
  return JSON.stringify({
    type: d.type,
    name: d.name,
    provider: d.provider,
    vendor: d.vendor,
    forcePathStyle: d.forcePathStyle,
    noCheckBucket: d.noCheckBucket,
    authMode: d.authMode,
    options: sortedOpts,
    clearSecrets: [...d.clearSecrets].sort(),
    authSessionId: d.authSessionId || "",
  });
}

export function changeStorageAuthMode(draft: RemoteFormDraft, mode: string): RemoteFormDraft {
  const authMode = mode === "runtime" || mode === "anonymous" ? mode : "keys";
  const options: Record<string, string> = { ...draft.options, env_auth: authMode === "runtime" ? "true" : "false" };
  const clearSecrets = new Set(draft.clearSecrets);
  if (authMode !== "keys") {
    options.access_key_id = "";
    for (const key of ["secret_access_key", "session_token"]) {
      options[key] = "";
      if (draft.configuredSecrets.includes(key)) clearSecrets.add(key);
    }
  } else {
    for (const key of ["secret_access_key", "session_token"]) {
      clearSecrets.delete(key);
    }
  }
  return { ...draft, authMode, options, clearSecrets: [...clearSecrets] };
}

export function buildRemoteOptions(draft: RemoteFormDraft): Record<string, string> {
  const settings: Record<string, string> = { type: draft.type };
  for (const [k, rawVal] of Object.entries(draft.options)) {
    if ((draft.type === "s3" || draft.type === "qingstor") && draft.authMode !== "keys" &&
        ["access_key_id", "secret_access_key", "session_token"].includes(k)) continue;
    if (rawVal === undefined || rawVal === null) continue;
    if (k === "key_pem") {
      const normalized = rawVal.trim().replace(/\r?\n/g, "\\n");
      if (normalized) settings[k] = normalized;
    } else if (k === "known_hosts") {
      if (rawVal.trim()) settings[k] = rawVal.trim();
    } else if (allSecretKeys.has(k)) {
      if (rawVal !== "") settings[k] = rawVal;
    } else if (rawVal.trim() !== "") {
      settings[k] = rawVal.trim();
    }
  }
  if (draft.type === "s3") {
    settings.provider = draft.provider || "Other";
    settings.env_auth = draft.authMode === "runtime" ? "true" : "false";
    if (draft.forcePathStyle) settings.force_path_style = "true";
    if (draft.noCheckBucket) settings.no_check_bucket = "true";
  }
  if (draft.type === "qingstor") {
    settings.env_auth = draft.authMode === "runtime" ? "true" : "false";
  }
  if (draft.type === "webdav" && draft.vendor) {
    settings.vendor = draft.vendor;
  }
  if (draft.type === "pan123") {
    const proto = (draft.options.protocol || "").trim().toLowerCase() === "web" ? "web" : "open_api";
    settings.protocol = proto;
  }
  if (draft.type === "sftp") {
    // Backend enforces shell_type exclusively from allow_hashcheck (unix vs none).
    delete settings.shell_type;
  }
  return settings;
}

const remoteNamePattern = /^[\p{L}\p{N}_][\p{L}\p{N}_. -]{0,63}$/u;

export function validateRemoteName(raw: string): string {
  const name = raw.trim();
  if (!name) {
    return "请输入存储名称";
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
  return "";
}

export function appendCombineUpstream(
  current: string,
  src: { value: string; kind: "remote" | "local" },
): string {
  const rawBase =
    src.kind === "local"
      ? src.value.split("/").filter(Boolean).pop() || "local"
      : src.value.replace(/:$/, "");
  const baseName = rawBase.replace(/[^\p{L}\p{N}_-]/gu, "") || "dir";

  const existingNames = new Set<string>();
  const trimmedCur = current.trim();
  if (trimmedCur) {
    const tokenRegex = /"([^"]*)"|(\S+)/g;
    let match: RegExpExecArray | null;
    while ((match = tokenRegex.exec(trimmedCur)) !== null) {
      const tok = match[1] ?? match[2] ?? "";
      const eqIdx = tok.indexOf("=");
      if (eqIdx > 0) {
        existingNames.add(tok.slice(0, eqIdx).trim().toLowerCase());
      }
    }
  }

  let dirName = baseName;
  let seq = 2;
  while (existingNames.has(dirName.toLowerCase())) {
    dirName = `${baseName}_${seq++}`;
  }
  const rawEntry = `${dirName}=${src.value}`;
  const entry = /\s/.test(rawEntry) ? `"${rawEntry}"` : rawEntry;
  return trimmedCur ? `${trimmedCur} ${entry}` : entry;
}

function isBoolTrue(opts: Record<string, string>, key: string): boolean {
  return (opts[key] || "").toLowerCase() === "true";
}

export function validateRemoteDraft(
  draft: RemoteFormDraft,
  requireName: boolean,
): Record<string, string> {
  const errs: Record<string, string> = {};
  if (requireName) {
    const nameErr = validateRemoteName(draft.name);
    if (nameErr) {
      errs.name = nameErr;
    }
  }

  const hasSecret = (key: string) =>
    Boolean(draft.options[key]) ||
    Boolean(draft.authSessionId && draft.authConfiguredSecrets?.includes(key)) ||
    (draft.editing &&
      draft.configuredSecrets.includes(key) &&
      !draft.clearSecrets.includes(key));

  const checkPort = (raw?: string) => {
    const p = (raw || "").trim();
    if (!p) return;
    const n = Number(p);
    if (!/^\d+$/.test(p) || !Number.isInteger(n) || n < 1 || n > 65535) {
      errs.port = "端口必须是 1–65535 的整数";
    }
  };

  const checkUploadOptions = () => {
    const conc = (draft.options.upload_concurrency || "").trim();
    if (conc) {
      const n = Number(conc);
      if (!/^\d+$/.test(conc) || !Number.isInteger(n) || n < 1 || n > 8) {
        errs.upload_concurrency = "分片上传并发数需为 1–8 的整数";
      }
    }
    const tps = (draft.options.tps_limit || "").trim();
    if (tps) {
      const v = Number(tps);
      if (!Number.isFinite(v) || v <= 0 || v > 20) {
        errs.tps_limit = "接口限流速率需为大于 0 且不超过 20 的数值";
      }
    }
  };

  const checkHttpUrl = (raw: string | undefined, key: string, label: string) => {
    const trimmed = (raw || "").trim();
    if (!trimmed) {
      errs[key] = `请输入${label}`;
      return;
    }
    try {
      const parsed = new URL(trimmed);
      if (!["http:", "https:"].includes(parsed.protocol)) {
        errs[key] = "请输入有效的 HTTP / HTTPS 地址";
      }
    } catch {
      errs[key] = "请输入有效的 HTTP / HTTPS 地址";
    }
  };

  if (draft.type === "s3" || draft.type === "qingstor") {
    if (draft.authMode === "keys") {
      if (!(draft.options.access_key_id || "").trim()) {
        errs.access_key_id = "请输入 Access Key ID";
      }
      if (!hasSecret("secret_access_key")) {
        errs.secret_access_key = "请输入 Secret Access Key";
      }
    }
  } else if (draft.type === "webdav") {
    checkHttpUrl(draft.options.url, "url", " WebDAV 地址");
  } else if (draft.type === "quark") {
    if (!hasSecret("cookie")) {
      errs.cookie = "请使用上方夸克 App 扫码登录，或粘贴夸克网盘 Cookie";
    }
    checkUploadOptions();
  } else if (draft.type === "pan123") {
    const proto =
      (draft.options.protocol || "").trim().toLowerCase() === "web"
        ? "web"
        : "open_api";
    const hasToken = hasSecret("access_token");
    if (proto === "open_api") {
      const hasClientPair =
        Boolean((draft.options.client_id || "").trim()) &&
        hasSecret("client_secret");
      if (!hasToken && !hasClientPair) {
        if (!(draft.options.client_id || "").trim()) {
          errs.client_id = "请输入开放平台 Client ID，或在下方直接粘贴 Access Token";
        }
        if (!hasSecret("client_secret")) {
          errs.client_secret = "请输入开放平台 Client Secret，或在下方直接粘贴 Access Token";
        }
      }
    } else {
      const hasUserPass =
        Boolean((draft.options.web_user || "").trim()) &&
        hasSecret("web_password");
      if (!hasToken && !hasUserPass) {
        if (!(draft.options.web_user || "").trim()) {
          errs.web_user = "请输入 123云盘手机号或邮箱，或在下方直接粘贴 Web Access Token";
        }
        if (!hasSecret("web_password")) {
          errs.web_password = "请输入账号密码，或在下方直接粘贴 Web Access Token";
        }
      }
    }
    const rf = (draft.options.root_folder_id || "").trim();
    if (rf && !/^\d+$/.test(rf)) {
      errs.root_folder_id = "根目录 ID 需为非负整数（留空默认 0）";
    }
    checkUploadOptions();
    const ttl = (draft.options.direct_link_ttl || "").trim();
    if (ttl) {
      const n = Number(ttl);
      if (!/^\d+$/.test(ttl) || !Number.isInteger(n) || n < 1 || n > 10080) {
        errs.direct_link_ttl = "直链签名有效期需为 1–10080 分钟的整数";
      }
    }
  } else if (draft.type === "http") {
    checkHttpUrl(draft.options.url, "url", " HTTP 源地址");
  } else if (draft.type === "sftp") {
    if (!(draft.options.host || "").trim()) {
      errs.host = "请输入 SFTP 主机地址";
    }
    if (!(draft.options.user || "").trim()) {
      errs.user = "请输入用户名";
    }
    checkPort(draft.options.port);
    if (!hasSecret("pass") && !hasSecret("key_pem")) {
      errs.pass = "密码与 SSH 私钥至少填写一项";
    }
  } else if (draft.type === "smb" || draft.type === "ftp") {
    if (!(draft.options.host || "").trim()) {
      errs.host = `请输入 ${draft.type.toUpperCase()} 主机地址`;
    }
    if (!(draft.options.user || "").trim()) {
      errs.user = "请输入用户名";
    }
    checkPort(draft.options.port);
  } else if (draft.type === "b2") {
    if (!(draft.options.account || "").trim()) {
      errs.account = "请输入 Account ID / Application Key ID";
    }
    if (!hasSecret("key")) {
      errs.key = "请输入 Application Key";
    }
  } else if (oauthTypes.has(draft.type)) {
    if (draft.type === "drive") {
      if (!hasSecret("token") && !hasSecret("service_account_credentials")) {
        errs.token = "请先完成 Google Drive 网页授权登录，或填写 Service Account 凭据 JSON";
      }
    } else if (draft.type === "pcloud") {
      if (!hasSecret("token") && (!(draft.options.username || "").trim() || !hasSecret("password"))) {
        errs.token = "请先完成 pCloud 网页授权登录，或填写账号邮箱与密码";
      }
    } else if (draft.type === "jottacloud") {
      if (!hasSecret("token")) {
        errs.token = "请先完成 Jottacloud 授权配置";
      }
    } else if (!hasSecret("token")) {
      errs.token = "请先完成网页授权登录";
    }
  } else if (draft.type === "mega") {
    if (!(draft.options.user || "").trim()) {
      errs.user = "请输入 MEGA 账号邮箱";
    }
    if (!hasSecret("pass")) {
      errs.pass = "请输入 MEGA 登录密码";
    }
  } else if (draft.type === "azureblob") {
    if (
      !hasSecret("sas_url") &&
      (!(draft.options.account || "").trim() ||
        (!hasSecret("key") && !hasSecret("client_secret")))
    ) {
      errs.account = "请填写存储账户与密钥，或提供 SAS URL";
    }
  } else if (draft.type === "azurefiles") {
    const hasConn = hasSecret("connection_string");
    const hasSAS = hasSecret("sas_url");
    const hasKey = Boolean((draft.options.account || "").trim()) && hasSecret("key");
    const hasSP =
      Boolean((draft.options.account || "").trim()) &&
      Boolean((draft.options.tenant || "").trim()) &&
      Boolean((draft.options.client_id || "").trim()) &&
      hasSecret("client_secret");
    if (!hasConn && !hasSAS && !hasKey && !hasSP) {
      errs.account = "请填写连接字符串、SAS URL、Account + Key 或 Service Principal 凭据";
    }
  } else if (draft.type === "googlecloudstorage") {
    if (
      !hasSecret("service_account_credentials") &&
      !hasSecret("token") &&
      !isBoolTrue(draft.options, "anonymous")
    ) {
      errs.service_account_credentials =
        "请先完成 Google 网页授权、填写 Service Account JSON 凭据或开启匿名访问";
    }
  } else if (draft.type === "swift") {
    const hasAppCred =
      Boolean((draft.options.application_credential_id || draft.options.application_credential_name || "").trim()) &&
      hasSecret("application_credential_secret");
    const hasUserKey =
      Boolean((draft.options.user || "").trim()) && hasSecret("key");
    const hasStorageToken =
      Boolean((draft.options.storage_url || "").trim()) && hasSecret("auth_token");
    if (!hasAppCred && !hasUserKey && !hasStorageToken) {
      errs.user = "请填写用户名与密钥、应用凭据或 Storage URL + Auth Token";
    }
    if (!hasStorageToken) {
      checkHttpUrl(draft.options.auth, "auth", "认证地址（auth）");
    }
  } else if (draft.type === "oracleobjectstorage") {
    if (!(draft.options.namespace || "").trim()) {
      errs.namespace = "请输入 Oracle Namespace";
    }
    if (!(draft.options.region || "").trim()) {
      errs.region = "请输入区域 (region)";
    }
  } else if (draft.type === "storj") {
    const hasGrant = hasSecret("access_grant");
    const hasKeys =
      Boolean((draft.options.satellite_address || "").trim()) &&
      hasSecret("api_key") &&
      hasSecret("passphrase");
    if (!hasGrant && !hasKeys) {
      errs.access_grant = "请填写 Access Grant，或填写 Satellite + API Key + Passphrase";
    }
  } else if (draft.type === "koofr") {
    checkHttpUrl(draft.options.endpoint, "endpoint", " Koofr 服务地址");
    if (!(draft.options.user || "").trim()) {
      errs.user = "请输入 Koofr 用户名";
    }
    if (!hasSecret("password")) {
      errs.password = "请输入 Koofr 应用密码";
    }
  } else if (draft.type === "seafile") {
    checkHttpUrl(draft.options.url, "url", " Seafile 服务地址");
    if (!(draft.options.user || "").trim()) {
      errs.user = "请输入 Seafile 登录邮箱 / 用户名";
    }
    if (!hasSecret("pass") && !hasSecret("auth_token")) {
      errs.pass = "请输入 Seafile 密码或 Auth Token";
    }
  } else if (draft.type === "pikpak") {
    if (!(draft.options.user || "").trim()) {
      errs.user = "请输入 PikPak 账号";
    }
    if (!hasSecret("pass")) {
      errs.pass = "请输入 PikPak 密码";
    }
  } else if (draft.type === "protondrive") {
    if (!(draft.options.username || "").trim()) {
      errs.username = "请输入 Proton 用户名";
    }
    if (!hasSecret("password")) {
      errs.password = "请输入 Proton 密码";
    }
  } else if (draft.type === "iclouddrive") {
    if (!(draft.options.apple_id || "").trim()) {
      errs.apple_id = "请输入 Apple ID";
    }
    if (!hasSecret("password")) {
      errs.password = "请输入密码";
    }
  } else if (draft.type === "filen") {
    if (!(draft.options.email || "").trim()) {
      errs.email = "请输入 Filen 邮箱";
    }
    if (!hasSecret("password")) {
      errs.password = "请输入 Filen 密码";
    }
    if (!hasSecret("api_key")) {
      errs.api_key = "请输入 Filen API Key";
    }
  } else if (draft.type === "mailru") {
    const hasToken = hasSecret("token");
    const hasUserPass = Boolean((draft.options.user || "").trim()) && hasSecret("pass");
    if (!hasToken && !hasUserPass) {
      errs.user = "请填写账号与密码，或填写 OAuth Token JSON";
    }
  } else if (draft.type === "fichier") {
    if (!hasSecret("api_key") && !(draft.options.shared_folder || "").trim()) {
      errs.api_key = "请填写 1Fichier API Key 或共享目录链接";
    }
  } else if (draft.type === "gofile") {
    if (!hasSecret("access_token") && !(draft.options.root_folder_id || "").trim()) {
      errs.access_token = "请填写 Gofile Access Token 或 Root Folder ID";
    }
  } else if (draft.type === "pixeldrain") {
    if (!hasSecret("api_key") && !(draft.options.root_folder_id || "").trim()) {
      errs.api_key = "请填写 Pixeldrain API Key 或 Root Folder ID";
    }
  } else if (draft.type === "crypt") {
    if (!(draft.options.remote || "").trim()) {
      errs.remote = "请输入底层存储路径";
    }
    if (!hasSecret("password")) {
      errs.password = "请输入加密密码";
    }
  } else if (wrapperTypes.has(draft.type)) {
    if (!(draft.options.remote || "").trim()) {
      errs.remote = "请输入底层存储路径";
    }
  } else if (draft.type === "union") {
    if (!(draft.options.upstreams || "").trim()) {
      errs.upstreams = "请输入至少一个上游存储（空格分隔）";
    }
  } else if (draft.type === "combine") {
    if (!(draft.options.upstreams || "").trim()) {
      errs.upstreams = "请输入至少一个目录映射（格式：目录名=存储名:路径）";
    }
  }

  if (draft.options.proxy_url?.trim()) {
    try {
      const parsed = new URL(draft.options.proxy_url.trim());
      if (
        !["http:", "https:", "socks5:", "socks5h:"].includes(parsed.protocol) ||
        !parsed.hostname
      ) {
        errs.proxy_url = "代理地址仅支持 http://、https://、socks5:// 或 socks5h://";
      }
    } catch {
      errs.proxy_url = "代理地址格式无效";
    }
  }

  return errs;
}

export function NativeStorageEditor({
  engine,
  mode: view,
  editRemote = null,
  initialBrowsePath = "/",
  onClose,
  onSaved,
  onChanged,
}: {
  engine?: AlistItem;
  mode: NativeEditorMode;
  editRemote?: NativeRemoteItem | null;
  initialBrowsePath?: string;
  onClose: () => void;
  onSaved?: (remoteName?: string) => void;
  onChanged?: () => void;
}) {
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [state, setState] = useState<NativeStorageState | null>(null);

  // Local engine settings
  const [remark, setRemark] = useState(engine?.remark || "内置存储");
  const [maxConcurrentJobs, setMaxConcurrentJobs] = useState(
    String(engine?.maxConcurrentJobs ?? 0),
  );
  const [savingEngine, setSavingEngine] = useState(false);
  // Single remote add/edit form
  const initialDraftValue = useMemo(
    () => (view === "form" && editRemote ? draftFromRemote(editRemote) : emptyDraft("s3")),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );
  const [draft, setDraft] = useState<RemoteFormDraft>(() => initialDraftValue);
  const savedRemoteName = editRemote?.name;
  const connectionName = savedRemoteName ?? draft.name.trim();
  const [initialDraftSnap, setInitialDraftSnap] = useState(() =>
    snapshotDraft(initialDraftValue),
  );
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [savingForm, setSavingForm] = useState(false);
  const [testingForm, setTestingForm] = useState(false);
  const [formTestResult, setFormTestResult] =
    useState<NativeStorageTestResult | null>(null);
  const [advancedOpen, setAdvancedOpen] = useState(() =>
    Boolean(
      initialDraftValue.options.proxy_url ||
        initialDraftValue.options.insecure_skip_verify ||
        initialDraftValue.options.storage_class ||
        initialDraftValue.options.chunk_size ||
        initialDraftValue.options.allow_hashcheck ||
        initialDraftValue.options.filename_encryption ||
        initialDraftValue.options.description,
    ),
  );
  const [oauthAdvancedOpen, setOauthAdvancedOpen] = useState(() =>
    Boolean(
      initialDraftValue.options.client_id ||
        initialDraftValue.configuredSecrets.includes("client_secret"),
    ),
  );
  const completedAuthRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    completedAuthRef.current = draft.authSessionId;
  }, [draft.authSessionId]);
  useEffect(() => {
    return () => {
      if (completedAuthRef.current) {
        void api.cancelNativeStorageAuth(completedAuthRef.current).catch(() => undefined);
      }
    };
  }, []);

  // Import rclone.conf view
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [importFileName, setImportFileName] = useState("");
  const [importContent, setImportContent] = useState("");
  const [importPassword, setImportPassword] = useState("");
  const [previewingImport, setPreviewingImport] = useState(false);
  const [importPreview, setImportPreview] = useState<NativeImportPreview | null>(
    null,
  );
  const [importPreviewError, setImportPreviewError] = useState("");
  const [importReplace, setImportReplace] = useState(false);
  const [selectedImports, setSelectedImports] = useState<string[]>([]);
  const [importing, setImporting] = useState(false);

  // Export rclone.conf view
  const [selectedExports, setSelectedExports] = useState<string[]>([]);
  const [exportPassword, setExportPassword] = useState("");
  const [exporting, setExporting] = useState(false);

  // Browse & restore view
  const [browsePath, setBrowsePath] = useState("/");
  const [browseData, setBrowseData] = useState<NativeBrowseResult | null>(null);
  const [browsing, setBrowsing] = useState(false);
  const [browsePaging, setBrowsePaging] = useState(false);
  const [browseError, setBrowseError] = useState("");
  const browseRequestRef = useRef(0);
  const browseAbortRef = useRef<AbortController | null>(null);
  const [restoreEntry, setRestoreEntry] = useState<NativeDirEntry | null>(null);
  const [restoreLocalRoot, setRestoreLocalRoot] = useState("");
  const [restoreSubPath, setRestoreSubPath] = useState("");
  const [restoreOverwrite, setRestoreOverwrite] = useState(false);
  const [restoring, setRestoring] = useState(false);
  const [sizeInfo, setSizeInfo] = useState<NativeSizeInfo | null>(null);
  const [sizing, setSizing] = useState(false);
  const [mkdirOpen, setMkdirOpen] = useState(false);
  const [mkdirName, setMkdirName] = useState("");
  const [mkdirBusy, setMkdirBusy] = useState(false);
  const [moveTarget, setMoveTarget] = useState<NativeDirEntry | null>(null);
  const [moveDstPath, setMoveDstPath] = useState("");
  const [moveBusy, setMoveBusy] = useState(false);
  const [deletingEntryPath, setDeletingEntryPath] = useState<string | null>(null);
  const [browseSearch, setBrowseSearch] = useState("");
  const [browseSort, setBrowseSort] = useState<
    "name_asc" | "name_desc" | "size_desc" | "mtime_desc"
  >("name_asc");
  const [selectedBrowsePaths, setSelectedBrowsePaths] = useState<string[]>([]);
  const [batchRestoreOpen, setBatchRestoreOpen] = useState(false);
  const [batchMoveOpen, setBatchMoveOpen] = useState(false);
  const [batchMoveDstDir, setBatchMoveDstDir] = useState("");
  const [uploadingFile, setUploadingFile] = useState(false);
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const [transfers, setTransfers] = useState<NativeTransferTask[]>([]);
  const [transfersOpen, setTransfersOpen] = useState(false);
  const transferRequestRef = useRef(0);
  const transferAbortRef = useRef<AbortController | null>(null);
  const [trashOpen, setTrashOpen] = useState(false);
  const [trashLoading, setTrashLoading] = useState(false);
  const [trashItems, setTrashItems] = useState<NativeTrashItem[]>([]);
  const [restoringTrashPath, setRestoringTrashPath] = useState<string | null>(null);

  const notifyParent = useCallback(() => {
    onChanged?.();
  }, [onChanged]);

  const loadState = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setLoadError("");
    try {
      const next = await api.nativeStorageState(signal);
      if (signal?.aborted) return;
      setState(next);
      setRemark(next.remark || "内置存储");
      setMaxConcurrentJobs(String(next.maxConcurrentJobs ?? 0));
      if (view === "export") {
        setSelectedExports(
          (next.remotes || [])
            .filter((r) => r.type !== "local")
            .map((r) => r.name),
        );
      }
      if (view === "browse" && next.localRoots.length > 0) {
        setRestoreLocalRoot((prev) => prev || next.localRoots[0].path);
      }
    } catch (err) {
      if (signal?.aborted) return;
      setLoadError(err instanceof Error ? err.message : "加载存储配置失败");
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [view]);

  useEffect(() => {
    const controller = new AbortController();
    void loadState(controller.signal);
    return () => controller.abort();
  }, [loadState]);

  const engineSettingsDirty = useMemo(() => {
    if (!state) return false;
    return (
      remark.trim() !== (state.remark || "内置存储") ||
      (maxConcurrentJobs.trim() || "0") !== String(state.maxConcurrentJobs ?? 0)
    );
  }, [state, remark, maxConcurrentJobs]);

  const formDirty = useMemo(
    () => view === "form" && snapshotDraft(draft) !== initialDraftSnap,
    [view, draft, initialDraftSnap],
  );

  const importDirty = useMemo(
    () =>
      view === "import" &&
      Boolean(importContent || importPassword || importPreview),
    [view, importContent, importPassword, importPreview],
  );

  const anyBusy =
    savingEngine ||
    savingForm ||
    testingForm ||
    importing ||
    previewingImport ||
    exporting ||
    restoring ||
    sizing ||
    mkdirBusy ||
    moveBusy ||
    deletingEntryPath !== null;

  const modalDirty =
    view === "local"
      ? engineSettingsDirty
      : view === "form"
        ? formDirty
        : view === "import"
          ? importDirty
          : false;

  // --- Engine actions ---
  const saveEngineSettings = async () => {
    const trimmedRemark = remark.trim();
    if (!trimmedRemark) {
      Toast.warning("请输入名称");
      return;
    }
    const rawMc = maxConcurrentJobs.trim() || "0";
    const parsedMc = Number(rawMc);
    if (
      !/^\d+$/.test(rawMc) ||
      !Number.isInteger(parsedMc) ||
      parsedMc < 0 ||
      parsedMc > 50
    ) {
      Toast.warning("最大并发任务数请输入 0–50 的整数");
      return;
    }
    setSavingEngine(true);
    try {
      const updated = await api.saveNativeEngine({
        remark: trimmedRemark,
        maxConcurrentJobs: parsedMc,
      });
      setState(updated);
      setRemark(updated.remark || "内置存储");
      setMaxConcurrentJobs(String(updated.maxConcurrentJobs ?? 0));
      Toast.success("本地目录与引擎设置已保存");
      notifyParent();
      onSaved ? onSaved() : onClose();
    } catch (err) {
      errorToast(err);
    } finally {
      setSavingEngine(false);
    }
  };

  const loadBrowse = useCallback(async (
    targetPath: string,
    options: { cursor?: string; full?: boolean; fresh?: boolean } = {},
  ) => {
    const requestId = ++browseRequestRef.current;
    browseAbortRef.current?.abort();
    const controller = new AbortController();
    browseAbortRef.current = controller;
    setBrowsing(true);
    setBrowsePaging(Boolean(options.cursor));
    setBrowseError("");
    if (!options.cursor) {
      setBrowsePath(targetPath);
      setRestoreEntry(null);
      setSizeInfo(null);
    }
    try {
      // A replaced browse effect can abort this request before any network IO.
      await Promise.resolve();
      if (controller.signal.aborted) return;
      const res = await api.browseNativeStorage(targetPath, controller.signal, {
        paged: !options.full,
        cursor: options.cursor,
        fresh: options.fresh,
      });
      if (controller.signal.aborted || requestId !== browseRequestRef.current) return;
      setBrowseData((prev) => {
        if (!options.cursor && !res.nextCursor) return res;
        const entriesByPath = new Map(
          options.cursor && prev ? prev.entries.map((entry) => [entry.path, entry]) : [],
        );
        for (const entry of res.entries) {
          const old = entriesByPath.get(entry.path);
          if (!old || entry.modTime > old.modTime) entriesByPath.set(entry.path, entry);
        }
        return { ...res, entries: [...entriesByPath.values()] };
      });
      setBrowsePath(res.path || targetPath);
    } catch (err) {
      if (controller.signal.aborted || requestId !== browseRequestRef.current) return;
      if (!options.cursor) setBrowseData(null);
      setBrowseError(err instanceof Error ? err.message : "读取目录失败");
    } finally {
      if (requestId === browseRequestRef.current) {
        setBrowsing(false);
        setBrowsePaging(false);
      }
    }
  }, []);

  useEffect(() => {
    if (view === "browse") {
      void loadBrowse(initialBrowsePath || "/");
    }
  }, [view, initialBrowsePath, loadBrowse]);

  const handleExecuteExport = async () => {
    if (selectedExports.length === 0) {
      Toast.warning("请至少选择一个要导出的存储");
      return;
    }
    setExporting(true);
    try {
      const res = await api.exportNativeConfig({
        names: selectedExports,
        password: exportPassword,
      });
      const blob = new Blob([res.content], { type: "text/plain;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = res.filename || "rclone.conf";
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(url);
      Toast.success(
        res.encrypted
          ? `已导出加密配置 ${res.filename}`
          : `已导出配置 ${res.filename}`,
      );
    } catch (err) {
      errorToast(err);
    } finally {
      setExporting(false);
    }
  };

  const refreshTransfers = useCallback(async () => {
    const requestId = ++transferRequestRef.current;
    transferAbortRef.current?.abort();
    const controller = new AbortController();
    try {
      const list = await api.listNativeTransfers(controller.signal);
      if (controller.signal.aborted || requestId !== transferRequestRef.current) return;
      setTransfers(list || []);
    } catch {
      /* transient transfer list refresh error ignored */
    }
    if (transferAbortRef.current === controller) transferAbortRef.current = null;
  }, []);

  useEffect(() => {
    return () => {
      browseRequestRef.current += 1;
      browseAbortRef.current?.abort();
      transferRequestRef.current += 1;
      transferAbortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (view !== "browse") return;
    void refreshTransfers();
    const timer = window.setInterval(() => {
      void refreshTransfers();
    }, 2000);
    return () => {
      window.clearInterval(timer);
      transferRequestRef.current += 1;
      transferAbortRef.current?.abort();
    };
  }, [view, refreshTransfers]);

  useEffect(() => {
    if (view === "browse") return;
    browseRequestRef.current += 1;
    browseAbortRef.current?.abort();
  }, [view]);

  useEffect(() => {
    setSelectedBrowsePaths([]);
    setTrashOpen(false);
    setTrashItems([]);
  }, [browsePath]);

  const browseNeedsFullList = browseSearch.trim() !== "" || browseSort !== "name_asc";
  useEffect(() => {
    if (view === "browse" && !trashOpen && !browsing && !browseError &&
        browseNeedsFullList && browseData?.nextCursor && browseData.path === browsePath) {
      void loadBrowse(browsePath, { full: true });
    }
  }, [view, trashOpen, browsing, browseError, browseNeedsFullList, browseData, browsePath, loadBrowse]);

  const filteredBrowseEntries = useMemo(() => {
    const entries = browseData?.entries || [];
    const kw = browseSearch.trim().toLowerCase();
    const filtered = kw
      ? entries.filter((e) => e.name.toLowerCase().includes(kw))
      : entries.slice();
    filtered.sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? -1 : 1;
      switch (browseSort) {
        case "name_desc":
          return b.name.localeCompare(a.name);
        case "size_desc":
          return (b.size || 0) - (a.size || 0);
        case "mtime_desc":
          return (b.modTime || 0) - (a.modTime || 0);
        default:
          return a.name.localeCompare(b.name);
      }
    });
    return filtered;
  }, [browseData?.entries, browseSearch, browseSort]);

  const activeTransferCount = useMemo(
    () => transfers.filter((t) => t.status === "running").length,
    [transfers],
  );

  const handleUploadFileChange = async (file?: File) => {
    if (!file || browsePath === "/") return;
    setUploadingFile(true);
    try {
      const res = await api.uploadNativeStorageFile(browsePath, file);
      Toast.success(`已上传「${res.fileName}」（${formatSize(res.size)}）`);
      await loadBrowse(browsePath, { fresh: true, full: browseNeedsFullList });
    } catch (err) {
      errorToast(err);
    } finally {
      setUploadingFile(false);
      if (uploadInputRef.current) uploadInputRef.current.value = "";
    }
  };

  const handleLoadTrash = async () => {
    if (browsePath === "/") return;
    setTrashOpen(true);
    setTrashLoading(true);
    try {
      const items = await api.browseNativeTrash(browsePath);
      setTrashItems(items || []);
    } catch (err) {
      errorToast(err);
    } finally {
      setTrashLoading(false);
    }
  };

  const handleRestoreTrashItem = async (item: NativeTrashItem) => {
    setRestoringTrashPath(item.trashPath);
    try {
      await api.restoreNativeTrashItem({
        trashPath: item.trashPath,
        originalPath: item.originalPath,
        isDir: item.isDir,
        overwrite: true,
      });
      Toast.success(`已将「${item.name}」还原至 ${item.originalPath}`);
      setTrashItems((prev) => prev.filter((x) => x.trashPath !== item.trashPath));
      await loadBrowse(browsePath, { fresh: true, full: browseNeedsFullList });
    } catch (err) {
      errorToast(err);
    } finally {
      setRestoringTrashPath(null);
    }
  };

  const selectedBrowseEntries = useMemo(() => {
    const set = new Set(selectedBrowsePaths);
    return (browseData?.entries || []).filter((e) => set.has(e.path));
  }, [browseData?.entries, selectedBrowsePaths]);

  const handleBatchDeleteSelected = () => {
    if (!selectedBrowseEntries.length) return;
    confirmDelete(
      `批量删除选中的 ${selectedBrowseEntries.length} 项？`,
      async () => {
        try {
          await api.startBatchNativeStorageAction({
            action: "delete",
            items: selectedBrowseEntries.map((e) => ({
              path: e.path,
              isDir: e.isDir,
            })),
          });
          setSelectedBrowsePaths([]);
          setTransfersOpen(true);
          Toast.success("已提交后台批量删除任务");
          await refreshTransfers();
        } catch (err) {
          errorToast(err);
        }
      },
      "删除任务将在后台异步队列中执行，操作不可撤销。",
    );
  };

  const handleBatchRestoreSelected = async () => {
    if (!selectedBrowseEntries.length) return;
    const baseRoot = restoreLocalRoot.trim();
    if (!baseRoot) {
      Toast.warning("请先选择本地授权目录");
      return;
    }
    const sub = restoreSubPath.trim().replace(/^\/+|\/+$/g, "");
    const dstDir = sub ? `${baseRoot.replace(/\/+$/, "")}/${sub}` : baseRoot;
    try {
      await api.startBatchNativeStorageAction({
        action: "restore",
        items: selectedBrowseEntries.map((e) => ({
          path: e.path,
          isDir: e.isDir,
        })),
        dstLocalDir: dstDir,
        overwrite: restoreOverwrite,
      });
      setBatchRestoreOpen(false);
      setSelectedBrowsePaths([]);
      setTransfersOpen(true);
      Toast.success(`已提交后台批量恢复（${selectedBrowseEntries.length} 项）`);
      await refreshTransfers();
    } catch (err) {
      errorToast(err);
    }
  };

  const handleBatchMoveSelected = async () => {
    if (!selectedBrowseEntries.length) return;
    const dstDir = batchMoveDstDir.trim();
    if (!dstDir) {
      Toast.warning("请输入目标目录路径");
      return;
    }
    try {
      await api.startBatchNativeStorageAction({
        action: "move",
        items: selectedBrowseEntries.map((e) => ({
          path: e.path,
          isDir: e.isDir,
        })),
        dstDir,
      });
      setBatchMoveOpen(false);
      setBatchMoveDstDir("");
      setSelectedBrowsePaths([]);
      setTransfersOpen(true);
      Toast.success(`已提交后台批量移动（${selectedBrowseEntries.length} 项）`);
      await refreshTransfers();
    } catch (err) {
      errorToast(err);
    }
  };

  const handleExecuteRestore = async () => {
    if (!restoreEntry) return;
    const baseRoot = restoreLocalRoot.trim();
    if (!baseRoot) {
      Toast.warning("请先选择本地授权目录");
      return;
    }
    const sub = restoreSubPath.trim().replace(/^\/+|\/+$/g, "");
    const dstDir = sub ? `${baseRoot.replace(/\/+$/, "")}/${sub}` : baseRoot;
    setRestoring(true);
    try {
      await api.startAsyncRestoreNativeFile({
        srcPath: restoreEntry.path,
        dstLocalDir: dstDir,
        isDir: restoreEntry.isDir,
        overwrite: restoreOverwrite,
      });
      Toast.success(`已加入后台恢复队列：「${restoreEntry.name}」→ ${dstDir}`);
      setRestoreEntry(null);
      setTransfersOpen(true);
      await refreshTransfers();
    } catch (err) {
      errorToast(err);
    } finally {
      setRestoring(false);
    }
  };

  const handleSizeBrowse = async () => {
    if (browsePath === "/") return;
    setSizing(true);
    try {
      const res = await api.sizeNativeStorage(browsePath);
      setSizeInfo(res);
    } catch (err) {
      errorToast(err);
    } finally {
      setSizing(false);
    }
  };

  const handleExecuteMkdir = async () => {
    const clean = mkdirName.trim().replace(/^\/+|\/+$/g, "");
    if (!clean) {
      Toast.warning("请输入目录名称");
      return;
    }
    const target = `${browsePath.replace(/\/+$/, "")}/${clean}`;
    setMkdirBusy(true);
    try {
      await api.mkdirNativeStorage(target);
      Toast.success(`已创建目录 ${clean}`);
      setMkdirOpen(false);
      setMkdirName("");
      await loadBrowse(browsePath, { fresh: true, full: browseNeedsFullList });
    } catch (err) {
      errorToast(err);
    } finally {
      setMkdirBusy(false);
    }
  };

  const handleExecuteMove = async () => {
    if (!moveTarget) return;
    const dst = moveDstPath.trim();
    if (!dst || dst === moveTarget.path) {
      Toast.warning("请输入新的目标路径");
      return;
    }
    setMoveBusy(true);
    try {
      await api.moveNativeStoragePath({
        srcPath: moveTarget.path,
        dstPath: dst,
        isDir: moveTarget.isDir,
      });
      Toast.success(`已移动到 ${dst}`);
      setMoveTarget(null);
      setMoveDstPath("");
      await loadBrowse(browsePath, { fresh: true, full: browseNeedsFullList });
    } catch (err) {
      errorToast(err);
    } finally {
      setMoveBusy(false);
    }
  };

  const handleDeleteEntry = (entry: NativeDirEntry) => {
    confirmDelete(
      `删除${entry.isDir ? "目录" : "文件"}「${entry.name}」？`,
      async () => {
        setDeletingEntryPath(entry.path);
        try {
          await api.deleteNativeStoragePath({
            path: entry.path,
            isDir: entry.isDir,
          });
          Toast.success(`已删除「${entry.name}」`);
          await loadBrowse(browsePath, { fresh: true, full: browseNeedsFullList });
        } catch (err) {
          errorToast(err);
        } finally {
          setDeletingEntryPath(null);
        }
      },
      entry.isDir
        ? "将调用 rclone purge 递归删除该目录及其全部内容，操作不可撤销。"
        : "将调用 rclone deletefile 永久删除该远端文件，操作不可撤销。",
    );
  };

  // --- Form helpers ---
  const updateDraftOption = (key: string, value: string) => {
    setDraft((prev) => ({
      ...prev,
      options: { ...prev.options, [key]: value },
      clearSecrets:
        value !== ""
          ? prev.clearSecrets.filter((k) => k !== key)
          : prev.clearSecrets,
    }));
    setFieldErrors((prev) => {
      if (!prev[key]) return prev;
      const next = { ...prev };
      delete next[key];
      return next;
    });
    setFormTestResult(null);
  };

  const toggleDraftBoolOption = (key: string, checked: boolean) => {
    updateDraftOption(key, checked ? "true" : "");
  };

  const toggleClearSecret = (key: string, checked: boolean) => {
    setDraft((prev) => ({
      ...prev,
      options: checked ? { ...prev.options, [key]: "" } : prev.options,
      clearSecrets: checked
        ? Array.from(new Set([...prev.clearSecrets, key]))
        : prev.clearSecrets.filter((k) => k !== key),
    }));
    setFormTestResult(null);
  };

  const handleFormTypeChange = (nextType: RemoteFormType) => {
    if (draft.authSessionId) {
      void api.cancelNativeStorageAuth(draft.authSessionId).catch(() => undefined);
    }
    setDraft((prev) => ({
      ...emptyDraft(nextType),
      name: prev.name,
    }));
    setOauthAdvancedOpen(false);
    setFieldErrors({});
    setFormTestResult(null);
  };

  const handleAuthCompleted = useCallback((result: CompletedAuthSession) => {
    setDraft((prev) => {
      if (prev.authSessionId && prev.authSessionId !== result.sessionId) {
        void api.cancelNativeStorageAuth(prev.authSessionId).catch(() => undefined);
      }
      const nextOptions = { ...prev.options };
      for (const [k, v] of Object.entries(result.publicOptions || {})) {
        if (v !== "") {
          nextOptions[k] = v;
        }
      }
      const nextClear = prev.clearSecrets.filter(
        (k) => !result.configuredSecrets.includes(k),
      );
      return {
        ...prev,
        options: nextOptions,
        clearSecrets: nextClear,
        authSessionId: result.sessionId,
        authPublicOptions: result.publicOptions,
        authConfiguredSecrets: result.configuredSecrets,
      };
    });
    setFieldErrors((prev) => {
      const next = { ...prev };
      delete next.token;
      delete next.cookie;
      delete next.service_account_credentials;
      delete next.pass;
      return next;
    });
    setFormTestResult(null);
  }, []);

  const handleProviderChange = (nextProvider: string) => {
    setDraft((prev) => ({
      ...prev,
      provider: nextProvider,
      forcePathStyle: !prev.editing ? nextProvider === "Minio" : prev.forcePathStyle,
    }));
    setFormTestResult(null);
  };

  const handleStorageAuthModeChange = (mode: string) => {
    setDraft((prev) => changeStorageAuthMode(prev, mode));
    setFieldErrors({});
    setFormTestResult(null);
  };

  const handleTestForm = async () => {
    const errs = validateRemoteDraft(draft, draft.editing);
    if (Object.keys(errs).length > 0) {
      setFieldErrors(errs);
      Toast.warning(Object.values(errs)[0]);
      return;
    }
    setTestingForm(true);
    setFormTestResult(null);
    try {
      const res = await api.testNativeStorage({
        name: connectionName,
        options: buildRemoteOptions(draft),
        editing: draft.editing,
        clearSecrets: draft.clearSecrets.length > 0 ? draft.clearSecrets : undefined,
        authSessionId: draft.authSessionId || undefined,
      });
      setFormTestResult(res);
    } catch (err) {
      errorToast(err);
    } finally {
      setTestingForm(false);
    }
  };

  const handleSaveForm = async () => {
    const trimmedName = draft.name.trim();
    const nameChanged = savedRemoteName !== undefined && trimmedName !== savedRemoteName;
    const renameOnly = nameChanged &&
      snapshotDraft({ ...draft, name: savedRemoteName }) === initialDraftSnap;
    const errs = renameOnly ? {} : validateRemoteDraft(draft, true);
    const nameError = renameOnly && validateRemoteName(trimmedName);
    if (nameError) errs.name = nameError;
    if (Object.keys(errs).length > 0) {
      setFieldErrors(errs);
      Toast.warning(Object.values(errs)[0]);
      return;
    }
    setSavingForm(true);
    try {
      if (savedRemoteName !== undefined) {
        if (renameOnly) {
          await api.renameNativeStorage({ oldName: savedRemoteName, newName: trimmedName });
        } else {
          await api.updateNativeStorage({
            name: savedRemoteName,
            newName: nameChanged ? trimmedName : undefined,
            options: buildRemoteOptions(draft),
            clearSecrets:
              draft.clearSecrets.length > 0 ? draft.clearSecrets : undefined,
            authSessionId: draft.authSessionId || undefined,
          });
        }
        completedAuthRef.current = undefined;
        Toast.success(`存储「${trimmedName}」已更新`);
      } else {
        await api.saveNativeStorage({
          name: trimmedName,
          options: buildRemoteOptions(draft),
          authSessionId: draft.authSessionId || undefined,
        });
        completedAuthRef.current = undefined;
        Toast.success(`存储「${trimmedName}」已添加`);
      }
      await loadState();
      notifyParent();
      onSaved ? onSaved(trimmedName) : onClose();
    } catch (err) {
      errorToast(err);
    } finally {
      setSavingForm(false);
    }
  };

  // --- Import helpers ---
  const runImportPreview = async (content: string, password: string) => {
    if (!content.trim()) {
      setImportPreview(null);
      setImportPreviewError("所选 rclone.conf 文件内容为空");
      return;
    }
    setPreviewingImport(true);
    setImportPreviewError("");
    try {
      const preview = await api.previewNativeImport({
        config: content,
        configPassword: password,
      });
      setImportPreview(preview);
      const defaultSelected = preview.items
        .filter(
          (item) =>
            item.status === "ready" ||
            (importReplace && item.status === "conflict" && item.canReplace),
        )
        .map((item) => item.name);
      setSelectedImports(defaultSelected);
    } catch (err) {
      setImportPreview(null);
      setSelectedImports([]);
      setImportPreviewError(
        err instanceof Error ? err.message : "解析 rclone.conf 失败",
      );
    } finally {
      setPreviewingImport(false);
    }
  };

  const handleImportFileSelect = async (file?: File) => {
    if (!file) return;
    if (file.size === 0) {
      Toast.warning("所选 rclone.conf 文件为空");
      return;
    }
    if (file.size > 512 * 1024) {
      Toast.warning("请选择不超过 512 KiB 的 rclone.conf");
      return;
    }
    const text = await file.text();
    setImportFileName(`${file.name} (${Math.max(1, Math.ceil(file.size / 1024))} KiB)`);
    setImportContent(text);
    setImportPreview(null);
    setImportPreviewError("");
    await runImportPreview(text, importPassword);
  };

  const handleImportReplaceToggle = (checked: boolean) => {
    setImportReplace(checked);
    if (!importPreview) return;
    setSelectedImports((prev) => {
      const next = new Set(prev);
      for (const item of importPreview.items) {
        if (item.status === "conflict" && item.canReplace) {
          if (checked) next.add(item.name);
          else next.delete(item.name);
        }
      }
      return Array.from(next);
    });
  };

  const handleExecuteImport = async () => {
    if (!importContent.trim()) {
      Toast.warning("请先选择 rclone.conf 文件");
      return;
    }
    if (selectedImports.length === 0) {
      Toast.warning("请至少选择一个可导入的存储配置");
      return;
    }
    setImporting(true);
    try {
      const res = await api.importNativeStorage({
        config: importContent,
        configPassword: importPassword,
        selected: selectedImports,
        replace: importReplace,
      });
      const parts: string[] = [];
      if (res.imported?.length) parts.push(`新增 ${res.imported.length} 个`);
      if (res.replaced?.length) parts.push(`覆盖 ${res.replaced.length} 个`);
      Toast.success(`导入完成（${parts.join("，") || "已保存"}）`);
      await loadState();
      notifyParent();
      onSaved ? onSaved() : onClose();
    } catch (err) {
      errorToast(err);
    } finally {
      setImporting(false);
    }
  };

  const quickBackingSources = useMemo(() => {
    const items: Array<{ label: string; value: string; kind: "remote" | "local" }> = [];
    for (const r of state?.remotes || []) {
      if (r.name === savedRemoteName) continue;
      if (r.type === "local") continue;
      items.push({ label: `${r.name}:`, value: `${r.name}:`, kind: "remote" });
    }
    for (const root of state?.localRoots || []) {
      items.push({ label: root.path, value: root.path, kind: "local" });
    }
    return items;
  }, [state, savedRemoteName]);

  const hasConflictCandidates = Boolean(
    importPreview?.items.some((i) => i.status === "conflict" && i.canReplace),
  );

  const builtInDriverRemotes = useMemo(() => {
    const remotes = state?.remotes || [];
    const byLower = new Map<string, NativeRemoteItem>();
    for (const r of remotes) byLower.set(r.name.toLowerCase(), r);
    const memo = new Map<string, boolean>();
    const check = (name: string, stack = new Set<string>()): boolean => {
      const lower = name.toLowerCase();
      if (memo.has(lower)) return memo.get(lower)!;
      if (stack.has(lower)) return false;
      const cur = byLower.get(lower);
      if (!cur) return false;
      if (cur.type === "quark" || cur.type === "pan123") {
        memo.set(lower, true);
        return true;
      }
      stack.add(lower);
      let hit = false;
      if (wrapperTypes.has(cur.type as RemoteFormType)) {
        const ref = (cur.options?.remote || "").split(":")[0]?.trim();
        if (ref && ref !== "本地") hit = check(ref, stack);
      } else if (cur.type === "union" || cur.type === "combine") {
        const raw = cur.options?.upstreams || "";
        for (const tok of raw.split(/\s+/).filter(Boolean)) {
          const clean = cur.type === "combine" ? tok.slice(tok.indexOf("=") + 1) : tok;
          const ref = clean.replace(/^"|"$/g, "").split(":")[0]?.trim();
          if (ref && ref !== "本地" && check(ref, stack)) {
            hit = true;
            break;
          }
        }
      }
      stack.delete(lower);
      memo.set(lower, hit);
      return hit;
    };
    return remotes.filter((r) => check(r.name));
  }, [state]);

  const builtInDriverNames = useMemo(
    () => new Set(builtInDriverRemotes.map((r) => r.name)),
    [builtInDriverRemotes],
  );

  const exportableRemotes = useMemo(
    () =>
      (state?.remotes || []).filter(
        (r) => r.type !== "local" && !builtInDriverNames.has(r.name),
      ),
    [state, builtInDriverNames],
  );

  const browseParentPath = useMemo(() => {
    const clean = browsePath.replace(/\/+$/, "");
    if (!clean || clean === "/") return null;
    const idx = clean.lastIndexOf("/");
    return idx <= 0 ? "/" : clean.slice(0, idx);
  }, [browsePath]);

  const modalTitle =
    view === "local"
      ? "配置本地目录"
      : view === "import"
        ? "导入 rclone.conf"
        : view === "export"
          ? "导出 rclone.conf"
          : view === "browse"
            ? `浏览与恢复 · ${browsePath}`
            : draft.editing
              ? `编辑远程存储 · ${savedRemoteName}`
              : "添加远程存储";

  const secretPlaceholder = (key: string, emptyHint: string) =>
    draft.editing &&
    draft.configuredSecrets.includes(key) &&
    !draft.clearSecrets.includes(key)
      ? "已保存，留空保留原凭据"
      : emptyHint;

  return (
    <>
      <Editor
        title={modalTitle}
        visible
        width={view === "browse" ? 740 : view === "local" ? 580 : 660}
        busy={anyBusy}
        dirty={modalDirty}
        onClose={onClose}
        onSave={() => {
          if (view === "local") void saveEngineSettings();
          else if (view === "form") void handleSaveForm();
          else if (view === "import") void handleExecuteImport();
          else if (view === "export") void handleExecuteExport();
          else onClose();
        }}
        footer={({ close }) => {
          if (view === "local") {
            return (
              <div className="editor-actions">
                <Button type="tertiary" onClick={close} disabled={anyBusy}>
                  取消
                </Button>
                <Button
                  type="primary"
                  theme="solid"
                  loading={savingEngine}
                  disabled={!state}
                  onClick={() => void saveEngineSettings()}
                >
                  保存配置
                </Button>
              </div>
            );
          }
          if (view === "form") {
            return (
              <div className="editor-actions">
                <Button type="tertiary" onClick={close} disabled={anyBusy}>
                  取消
                </Button>
                <Button
                  type="tertiary"
                  icon={<IconChainStroked aria-hidden="true" />}
                  loading={testingForm}
                  disabled={savingForm}
                  onClick={() => void handleTestForm()}
                >
                  测试连接
                </Button>
                <Button
                  type="primary"
                  theme="solid"
                  loading={savingForm}
                  disabled={testingForm}
                  onClick={() => void handleSaveForm()}
                >
                  {draft.editing ? "保存修改" : "保存存储"}
                </Button>
              </div>
            );
          }
          if (view === "import") {
            return (
              <div className="editor-actions">
                <Button type="tertiary" onClick={close} disabled={anyBusy}>
                  取消
                </Button>
                <Button
                  type="primary"
                  theme="solid"
                  loading={importing}
                  disabled={previewingImport || selectedImports.length === 0}
                  onClick={() => void handleExecuteImport()}
                >
                  {selectedImports.length > 0
                    ? `导入所选存储（${selectedImports.length}）`
                    : "导入所选存储"}
                </Button>
              </div>
            );
          }
          if (view === "export") {
            return (
              <div className="editor-actions">
                <Button type="tertiary" onClick={close} disabled={anyBusy}>
                  取消
                </Button>
                <Button
                  type="primary"
                  theme="solid"
                  icon={<IconDownload aria-hidden="true" />}
                  loading={exporting}
                  disabled={selectedExports.length === 0}
                  onClick={() => void handleExecuteExport()}
                >
                  导出并下载（{selectedExports.length}）
                </Button>
              </div>
            );
          }
          if (view === "browse") {
            return (
              <div className="editor-actions">
                <Button type="tertiary" onClick={close} disabled={restoring}>
                  关闭
                </Button>
              </div>
            );
          }
          return (
            <div className="editor-actions">
              <Button type="tertiary" onClick={close} disabled={anyBusy}>
                关闭
              </Button>
            </div>
          );
        }}
      >
        {loading && !state ? (
          <div className="native-loading-panel">
            <Spin size="large" />
          </div>
        ) : loadError && !state ? (
          <InlineNotice
            role="alert"
            message={loadError}
            onRetry={() => void loadState()}
          />
        ) : view === "local" && state ? (
          <div className="editor-form native-panel">
            {!state.rcloneAvailable && (
              <InlineNotice
                role="alert"
                icon={<IconAlertTriangle aria-hidden="true" size="small" />}
                message={`内置 rclone 不可用：${state.rcloneError || "未找到可执行文件"}`}
              />
            )}

            <section className="native-panel-section">
              <div className="native-section-head">
                <div className="native-section-title">
                  <IconFolderStroked aria-hidden="true" size="small" />
                  <h3>飞牛本地授权目录</h3>
                  <Tag
                    size="small"
                    type="light"
                    color={state.localRoots.length > 0 ? "green" : "orange"}
                  >
                    {state.localRoots.length > 0
                      ? `已授权 ${state.localRoots.length} 个`
                      : "未授权"}
                  </Tag>
                  {state.rcloneVersion && (
                    <Tag size="small" type="light" color="cyan">
                      rclone {state.rcloneVersion}
                    </Tag>
                  )}
                </div>
                <Button
                  size="small"
                  type="tertiary"
                  theme="borderless"
                  icon={<IconRefresh aria-hidden="true" />}
                  loading={loading}
                  onClick={() => void loadState()}
                >
                  刷新目录
                </Button>
              </div>

              {state.localRoots.length > 0 ? (
                <div className="native-local-roots">
                  {state.localRoots.map((root) => (
                    <span
                      key={root.id}
                      className="native-local-chip mono"
                      title={root.path}
                    >
                      {root.path}
                    </span>
                  ))}
                </div>
              ) : (
                <div className="sync-method-banner is-warning">
                  <IconAlertTriangle aria-hidden="true" size="small" />
                  <span>
                    未检测到本地授权目录。如需同步或恢复到本地磁盘，请在飞牛「应用设置 → 文件夹访问权限」中为 OpenSync 授权文件夹后点击「刷新目录」。
                  </span>
                </div>
              )}

              <div className="field-hint">
                本地目录由飞牛系统统一授权管理；添加或调整权限后，点击上方「刷新目录」即可立即生效。
              </div>
            </section>

            <section className="native-panel-section">
              <div className="native-section-head">
                <div className="native-section-title">
                  <h3>内置引擎设置</h3>
                </div>
              </div>
              <div className="form-grid form-grid--three">
                <Field label="显示名称" required>
                  <Input
                    value={remark}
                    onChange={setRemark}
                    maxLength={200}
                    placeholder="内置存储"
                  />
                </Field>
                <Field
                  label="全局最大并发任务数"
                  hint="0 表示不限制；1 表示全局串行排队（0–50）"
                >
                  <Input
                    inputMode="numeric"
                    value={maxConcurrentJobs}
                    onChange={setMaxConcurrentJobs}
                    placeholder="0"
                  />
                </Field>
              </div>
            </section>
          </div>
        ) : view === "form" ? (
          <div className="editor-form">
            {!draft.editing && (
              <div className="native-quick-picks" role="group" aria-label="常用远程存储模板">
                <span className="muted">快捷模板：</span>
                {(
                  [
                    {
                      label: "OpenList / AList (WebDAV)",
                      apply: () => {
                        setDraft((prev) => ({
                          ...emptyDraft("webdav"),
                          name: prev.name || "openlist",
                          vendor: "other",
                          options: {
                            url: "http://127.0.0.1:5244/dav",
                          },
                        }));
                        setFieldErrors({});
                        setFormTestResult(null);
                      },
                    },
                    {
                      label: "夸克网盘",
                      apply: () => handleFormTypeChange("quark"),
                    },
                    {
                      label: "123云盘",
                      apply: () => handleFormTypeChange("pan123"),
                    },
                    {
                      label: "阿里云 OSS (S3)",
                      apply: () => {
                        setDraft((prev) => ({
                          ...emptyDraft("s3"),
                          name: prev.name,
                          provider: "Alibaba",
                        }));
                        setFieldErrors({});
                        setFormTestResult(null);
                      },
                    },
                    {
                      label: "MinIO / 自建 S3",
                      apply: () => {
                        setDraft((prev) => ({
                          ...emptyDraft("s3"),
                          name: prev.name,
                          provider: "Minio",
                          forcePathStyle: true,
                        }));
                        setFieldErrors({});
                        setFormTestResult(null);
                      },
                    },
                    {
                      label: "SMB 局域网共享",
                      apply: () => handleFormTypeChange("smb"),
                    },
                    {
                      label: "SFTP",
                      apply: () => handleFormTypeChange("sftp"),
                    },
                    {
                      label: "OneDrive",
                      apply: () => handleFormTypeChange("onedrive"),
                    },
                    {
                      label: "Google Drive",
                      apply: () => handleFormTypeChange("drive"),
                    },
                  ] as const
                ).map((preset) => (
                  <button
                    key={preset.label}
                    type="button"
                    className="native-quick-chip"
                    onClick={preset.apply}
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
            )}
            <div className="form-grid form-grid--two">
              <Field
                label="存储类型"
                required
                hint={draft.editing ? "已保存的存储类型不可变更" : undefined}
              >
                <Select
                  aria-label="存储类型"
                  value={draft.type}
                  disabled={draft.editing}
                  filter={(input, option) => {
                    const query = input.trim().toLowerCase();
                    if (!query) return true;
                    const label = String(option.label ?? "").toLowerCase();
                    const val = String(option.value ?? "").toLowerCase();
                    const groupLabel = (
                      remoteTypeGroupLabelByType.get(
                        option.value as RemoteFormType,
                      ) || ""
                    ).toLowerCase();
                    return (
                      label.includes(query) ||
                      val.includes(query) ||
                      groupLabel.includes(query)
                    );
                  }}
                  onChange={(v) => handleFormTypeChange(v as RemoteFormType)}
                >
                  {draft.editing && !creatableRemoteTypeSet.has(draft.type) && (
                    <Select.OptGroup key="existing" label="已配置类型">
                      <Select.Option
                        key={draft.type}
                        value={draft.type}
                        label={remoteTypeLabels[draft.type] || draft.type}
                      >
                        {remoteTypeLabels[draft.type] || draft.type}
                      </Select.Option>
                    </Select.OptGroup>
                  )}
                  {remoteTypeGroups.map((group) => (
                    <Select.OptGroup key={group.key} label={group.label}>
                      {group.types.map((t) => (
                        <Select.Option
                          key={t}
                          value={t}
                          label={remoteTypeLabels[t]}
                        >
                          {remoteTypeLabels[t]}
                        </Select.Option>
                      ))}
                    </Select.OptGroup>
                  ))}
                </Select>
              </Field>
              <Field
                label="存储名称"
                required
                error={fieldErrors.name}
                hint={
                  draft.editing
                    ? "改名后会自动更新关联存储和同步任务的路径"
                    : "用于任务路径前缀，如 /archive/..."
                }
              >
                <Input
                  value={draft.name}
                  disabled={savingForm}
                  maxLength={64}
                  onChange={(v) => {
                    setDraft((prev) => ({ ...prev, name: v }));
                    setFieldErrors((prev) => {
                      if (!prev.name) return prev;
                      const next = { ...prev };
                      delete next.name;
                      return next;
                    });
                  }}
                  placeholder="例如 archive"
                />
              </Field>
            </div>

            {draft.type === "s3" && (
              <>
                <Field label="服务商">
                  <Select
                    aria-label="服务商"
                    value={draft.provider}
                    onChange={(v) => handleProviderChange(String(v))}
                    optionList={Object.entries(s3Providers).map(([value, label]) => ({
                      value,
                      label,
                    }))}
                  />
                </Field>
                <Field
                  label="认证方式"
                  error={fieldErrors.env_auth}
                  hint="运行时凭据从应用进程环境变量读取；留空密钥可用于匿名公共桶"
                >
                  <Select
                    value={draft.authMode}
                    optionList={[
                      { value: "keys", label: "Access Key + Secret" },
                      { value: "runtime", label: "运行时凭据（env_auth）" },
                      { value: "anonymous", label: "匿名访问（公共桶）" },
                    ]}
                    onChange={(v) => handleStorageAuthModeChange(String(v))}
                  />
                </Field>
                <div className="form-grid form-grid--two">
                  <Field
                    label="S3 端点"
                    error={fieldErrors.endpoint}
                    hint="支持纯域名（如 oss-cn-hangzhou.aliyuncs.com）或 http(s):// 地址"
                  >
                    <Input
                      value={draft.options.endpoint || ""}
                      onChange={(v) => updateDraftOption("endpoint", v)}
                      placeholder={
                        s3EndpointPlaceholders[draft.provider] ||
                        s3EndpointPlaceholders.Other
                      }
                    />
                  </Field>
                  <Field
                    label="区域（Region）"
                    hint={
                      draft.provider === "Cloudflare"
                        ? "Cloudflare R2 通常填 auto"
                        : "可选，例如 oss-cn-hangzhou / us-east-1"
                    }
                  >
                    <Input
                      value={draft.options.region || ""}
                      onChange={(v) => updateDraftOption("region", v)}
                      placeholder={
                        draft.provider === "Cloudflare" ? "auto" : "例如 us-east-1"
                      }
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field
                    label="Access Key ID"
                    required={draft.authMode === "keys"}
                    error={fieldErrors.access_key_id}
                  >
                    <Input
                      value={draft.options.access_key_id || ""}
                      disabled={draft.authMode !== "keys"}
                      onChange={(v) => updateDraftOption("access_key_id", v)}
                      placeholder="填写访问密钥 ID"
                    />
                  </Field>
                  <Field
                    label="Secret Access Key"
                    required={draft.authMode === "keys" && !draft.editing}
                    error={fieldErrors.secret_access_key}
                  >
                    <Input
                      mode="password"
                      value={draft.options.secret_access_key || ""}
                      disabled={draft.authMode !== "keys"}
                      onChange={(v) => updateDraftOption("secret_access_key", v)}
                      placeholder={secretPlaceholder(
                        "secret_access_key",
                        "填写访问密钥 Secret",
                      )}
                    />
                  </Field>
                </div>
                <div className="native-checkbox-group">
                  <Checkbox
                    checked={draft.forcePathStyle}
                    onChange={(e) => {
                      setDraft((prev) => ({
                        ...prev,
                        forcePathStyle: Boolean(e.target.checked),
                      }));
                      setFormTestResult(null);
                    }}
                  >
                    使用 Path-Style 路径访问（自建 MinIO / Ceph / IP 端口端点建议开启）
                  </Checkbox>
                  <Checkbox
                    checked={draft.noCheckBucket}
                    onChange={(e) => {
                      setDraft((prev) => ({
                        ...prev,
                        noCheckBucket: Boolean(e.target.checked),
                      }));
                      setFormTestResult(null);
                    }}
                  >
                    不检查/创建存储桶（仅具备单桶权限的最小权限密钥建议开启）
                  </Checkbox>
                </div>
              </>
            )}

            {draft.type === "webdav" && (
              <>
                <Field
                  label="WebDAV 地址"
                  required
                  error={fieldErrors.url}
                  hint="自建 OpenList / AList 默认地址通常为 http://127.0.0.1:5244/dav（或 NAS 局域网 IP）"
                >
                  <Input
                    value={draft.options.url || ""}
                    onChange={(v) => updateDraftOption("url", v)}
                    placeholder="例如 http://127.0.0.1:5244/dav 或 https://dav.example.com/dav"
                  />
                </Field>
                <Field label="WebDAV 类型">
                  <Select
                    aria-label="WebDAV 类型"
                    value={draft.vendor}
                    onChange={(v) => {
                      setDraft((prev) => ({ ...prev, vendor: String(v) }));
                      setFormTestResult(null);
                    }}
                    optionList={Object.entries(webdavVendors).map(
                      ([value, label]) => ({ value, label }),
                    )}
                  />
                </Field>
                {draft.vendor === "other" && (
                  <div className="sync-method-banner">
                    <IconInfoCircle aria-hidden="true" size="small" />
                    <span>
                      接入 OpenList / AList（夸克、阿里云盘、百度网盘、115 等）或其他标准 WebDAV 时，作为目标存储创建同步任务会自动默认使用「<strong>仅按文件大小</strong>」比对，避免每次同步拉取大量远端资源到本地校验。
                    </span>
                  </div>
                )}
                <div className="form-grid form-grid--two">
                  <Field label="用户名">
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="可选"
                    />
                  </Field>
                  <Field label="密码">
                    <Input
                      mode="password"
                      value={draft.options.pass || ""}
                      onChange={(v) => updateDraftOption("pass", v)}
                      placeholder={secretPlaceholder("pass", "可选")}
                    />
                  </Field>
                </div>
                {draft.editing && draft.configuredSecrets.includes("pass") && (
                  <Checkbox
                    checked={draft.clearSecrets.includes("pass")}
                    onChange={(e) =>
                      toggleClearSecret("pass", Boolean(e.target.checked))
                    }
                  >
                    清除已保存的密码（改用无密码访问）
                  </Checkbox>
                )}
              </>
            )}

            {draft.type === "quark" && (
              <>
                <NativeStorageAuthWizard
                  remoteType="quark"
                  remoteLabel={remoteTypeLabels.quark}
                  remoteName={connectionName}
                  editing={draft.editing}
                  draftOptions={buildRemoteOptions(draft)}
                  clearSecrets={draft.clearSecrets}
                  hasExistingAuth={
                    Boolean(draft.options.cookie) ||
                    (draft.editing &&
                      draft.configuredSecrets.includes("cookie") &&
                      !draft.clearSecrets.includes("cookie"))
                  }
                  completedSession={
                    draft.authSessionId
                      ? {
                          sessionId: draft.authSessionId,
                          publicOptions: draft.authPublicOptions || {},
                          configuredSecrets: draft.authConfiguredSecrets || [],
                        }
                      : null
                  }
                  variant="quark_qr"
                  disabled={savingForm || testingForm}
                  onCompleted={handleAuthCompleted}
                />

                <Field
                  label="手动粘贴 Cookie（与上方扫码登录二选一）"
                  error={fieldErrors.cookie}
                  hint={
                    draft.editing &&
                    draft.configuredSecrets.includes("cookie") &&
                    !draft.clearSecrets.includes("cookie")
                      ? "已保存夸克网盘 Cookie；留空保留当前登录态，重新扫码或粘贴新 Cookie 将覆盖（同步期间自动续期 __puus）"
                      : "也可在浏览器登录 pan.quark.cn 后从开发者工具网络请求头复制 Cookie 粘贴到此处，同步期间自动续期 __puus"
                  }
                >
                  <TextArea
                    aria-label="夸克网盘 Cookie"
                    autosize={{ minRows: 2, maxRows: 4 }}
                    value={draft.options.cookie || ""}
                    onChange={(v) => updateDraftOption("cookie", v)}
                    placeholder={secretPlaceholder(
                      "cookie",
                      "例如 __pus=...; __puus=...（扫码登录后无需手动填写）",
                    )}
                  />
                </Field>

                <div className="form-grid form-grid--two">
                  <Field
                    label="根目录 ID（root_folder_id，可选）"
                    hint="留空默认为 0（网盘根目录）；可填入子目录 FID 仅同步该目录"
                  >
                    <Input
                      value={draft.options.root_folder_id || ""}
                      onChange={(v) => updateDraftOption("root_folder_id", v)}
                      placeholder="默认 0（网盘根目录）"
                    />
                  </Field>
                  <Field
                    label="账号昵称 / 备注（可选）"
                    hint="扫码登录后自动填入，仅用于列表展示"
                  >
                    <Input
                      value={draft.options.account_name || ""}
                      onChange={(v) => updateDraftOption("account_name", v)}
                      placeholder="例如 个人夸克网盘"
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "pan123" && (() => {
              const pan123Protocol =
                (draft.options.protocol || "").trim().toLowerCase() === "web"
                  ? "web"
                  : "open_api";
              return (
                <>
                  <Field
                    label="接入协议（protocol）"
                    hint={
                      pan123Protocol === "open_api"
                        ? "开放平台 OpenAPI：协议官方稳定、支持 V2 单步小文件上传与多片并发上传、可选直链下载，需在 123云盘开放平台申请凭证"
                        : "个人盘 Web 协议：无需开发者资质、支持 16/32 MiB 批量预签并发上传与严格分页一致性保护；建议优先粘贴 Web Token 避免 NAS IP 账密登录风控"
                    }
                  >
                    <Select
                      value={pan123Protocol}
                      onChange={(v) =>
                        updateDraftOption("protocol", String(v || "open_api"))
                      }
                      style={{ width: "100%" }}
                      optionList={[
                        {
                          value: "open_api",
                          label: "开放平台 OpenAPI（推荐，官方接口更稳定）",
                        },
                        {
                          value: "web",
                          label: "个人盘 Web 协议（免开发者资质）",
                        },
                      ]}
                    />
                  </Field>

                  {pan123Protocol === "open_api" ? (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field
                          label="Client ID"
                          error={fieldErrors.client_id}
                          hint="在 123云盘开放平台应用详情中获取；若直接填写下方 Access Token 可留空"
                        >
                          <Input
                            value={draft.options.client_id || ""}
                            onChange={(v) => updateDraftOption("client_id", v)}
                            placeholder="例如 your_client_id"
                          />
                        </Field>
                        <Field
                          label="Client Secret"
                          error={fieldErrors.client_secret}
                          hint="自动换取并持久化续期 Access Token，避免同 Client ID 反复签发踢下线"
                        >
                          <Input
                            mode="password"
                            value={draft.options.client_secret || ""}
                            onChange={(v) =>
                              updateDraftOption("client_secret", v)
                            }
                            placeholder={secretPlaceholder(
                              "client_secret",
                              "请输入 Client Secret",
                            )}
                          />
                        </Field>
                      </div>

                      <Field
                        label="Access Token（可选，与 Client ID/Secret 二选一）"
                        error={fieldErrors.access_token}
                        hint={
                          draft.editing &&
                          draft.configuredSecrets.includes("access_token") &&
                          !draft.clearSecrets.includes("access_token")
                            ? "已保存 Access Token。留空会沿用当前令牌，更换凭据时会重新获取。"
                            : "若已自行获取开放平台 Access Token 可直接粘贴，填写 Client ID/Secret 时无需手动填写"
                        }
                      >
                        <TextArea
                          aria-label="123云盘开放平台 Access Token"
                          autosize={{ minRows: 2, maxRows: 3 }}
                          value={draft.options.access_token || ""}
                          onChange={(v) => updateDraftOption("access_token", v)}
                          placeholder={secretPlaceholder(
                            "access_token",
                            "留空由 Client ID/Secret 自动获取，或直接粘贴 Access Token",
                          )}
                        />
                      </Field>
                    </>
                  ) : (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field
                          label="账号（手机号或邮箱）"
                          error={fieldErrors.web_user}
                          hint="用于个人盘 Web 登录；若已在下方直接粘贴 Web Token 可留空"
                        >
                          <Input
                            value={draft.options.web_user || ""}
                            onChange={(v) => updateDraftOption("web_user", v)}
                            placeholder="手机号或邮箱"
                          />
                        </Field>
                        <Field
                          label="账号密码"
                          error={fieldErrors.web_password}
                          hint="如遇异地登录风控或短信验证提示，请在官网验证后改用下方 Web Token"
                        >
                          <Input
                            mode="password"
                            value={draft.options.web_password || ""}
                            onChange={(v) =>
                              updateDraftOption("web_password", v)
                            }
                            placeholder={secretPlaceholder(
                              "web_password",
                              "请输入登录密码",
                            )}
                          />
                        </Field>
                      </div>

                      <Field
                        label="Web Access Token（推荐，可替代账密登录）"
                        error={fieldErrors.access_token}
                        hint={
                          draft.editing &&
                          draft.configuredSecrets.includes("access_token") &&
                          !draft.clearSecrets.includes("access_token")
                            ? "已保存 Web Access Token。留空会沿用当前登录态，更换账号或密码时会重新登录。"
                            : "在浏览器登录 yun.123pan.cn 后，从请求头 Authorization 复制 Bearer 后的 JWT 粘贴到此处，可有效规避密码登录风控"
                        }
                      >
                        <TextArea
                          aria-label="123云盘 Web Access Token"
                          autosize={{ minRows: 2, maxRows: 3 }}
                          value={draft.options.access_token || ""}
                          onChange={(v) => updateDraftOption("access_token", v)}
                          placeholder={secretPlaceholder(
                            "access_token",
                            "不含 Bearer 前缀的 JWT Token（与账号密码二选一）",
                          )}
                        />
                      </Field>
                    </>
                  )}

                  <div className="form-grid form-grid--two">
                    <Field
                      label="根目录 ID（root_folder_id，可选）"
                      error={fieldErrors.root_folder_id}
                      hint="留空默认为 0（网盘根目录）；可填入子目录数字 ID 仅同步该目录"
                    >
                      <Input
                        value={draft.options.root_folder_id || ""}
                        onChange={(v) => updateDraftOption("root_folder_id", v)}
                        placeholder="默认 0（网盘根目录）"
                      />
                    </Field>
                    <Field
                      label="账号昵称 / 备注（可选）"
                      hint="仅用于列表展示与路径归属标识"
                    >
                      <Input
                        value={draft.options.account_name || ""}
                        onChange={(v) => updateDraftOption("account_name", v)}
                        placeholder="例如 个人 123云盘"
                      />
                    </Field>
                  </div>
                </>
              );
            })()}

            {draft.type === "sftp" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="主机地址" required error={fieldErrors.host}>
                    <Input
                      value={draft.options.host || ""}
                      onChange={(v) => updateDraftOption("host", v)}
                      placeholder="例如 192.168.1.20 或 sftp.example.com"
                    />
                  </Field>
                  <Field label="端口" error={fieldErrors.port}>
                    <Input
                      inputMode="numeric"
                      value={draft.options.port || ""}
                      onChange={(v) => updateDraftOption("port", v)}
                      placeholder="留空默认 22"
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="用户名" required error={fieldErrors.user}>
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="SSH 登录用户名"
                    />
                  </Field>
                  <Field label="密码（与私钥二选一）" error={fieldErrors.pass}>
                    <Input
                      mode="password"
                      value={draft.options.pass || ""}
                      onChange={(v) => updateDraftOption("pass", v)}
                      placeholder={secretPlaceholder("pass", "登录密码")}
                    />
                  </Field>
                </div>
                <Field
                  label="SSH 私钥（PEM，可替代密码）"
                  hint={
                    draft.editing &&
                    draft.configuredSecrets.includes("key_pem") &&
                    !draft.clearSecrets.includes("key_pem")
                      ? "已保存 SSH 私钥；留空保留原私钥，粘贴新内容将覆盖"
                      : undefined
                  }
                >
                  <TextArea
                    aria-label="SSH 私钥（PEM，可替代密码）"
                    autosize={{ minRows: 2, maxRows: 5 }}
                    value={draft.options.key_pem || ""}
                    onChange={(v) => updateDraftOption("key_pem", v)}
                    placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;..."
                  />
                </Field>
                <Field label="SSH 私钥密码（可选）">
                  <Input
                    mode="password"
                    value={draft.options.key_file_pass || ""}
                    onChange={(v) => updateDraftOption("key_file_pass", v)}
                    placeholder={secretPlaceholder(
                      "key_file_pass",
                      "未加密私钥可留空",
                    )}
                  />
                </Field>
                <Field
                  label="SSH 主机公钥（known_hosts 格式，可多行）"
                  hint={
                    draft.editing
                      ? "修改主机或端口后留空将自动重新获取公钥；主机未变时留空则保留当前公钥"
                      : "留空时保存将记录首次连接获取的主机公钥，后续连接严格校验"
                  }
                >
                  <TextArea
                    aria-label="SSH 主机公钥（known_hosts 格式，可多行）"
                    autosize={{ minRows: 2, maxRows: 4 }}
                    value={draft.options.known_hosts || ""}
                    onChange={(v) => updateDraftOption("known_hosts", v)}
                    placeholder="可粘贴 ssh-keyscan 输出，或留空自动固定"
                  />
                </Field>
                {draft.editing &&
                  (draft.configuredSecrets.includes("pass") ||
                    draft.configuredSecrets.includes("key_pem") ||
                    draft.configuredSecrets.includes("key_file_pass")) && (
                    <div className="native-checkbox-group">
                      {draft.configuredSecrets.includes("pass") && (
                        <Checkbox
                          checked={draft.clearSecrets.includes("pass")}
                          onChange={(e) =>
                            toggleClearSecret("pass", Boolean(e.target.checked))
                          }
                        >
                          清除已保存的登录密码
                        </Checkbox>
                      )}
                      {draft.configuredSecrets.includes("key_pem") && (
                        <Checkbox
                          checked={draft.clearSecrets.includes("key_pem")}
                          onChange={(e) =>
                            toggleClearSecret("key_pem", Boolean(e.target.checked))
                          }
                        >
                          清除已保存的 SSH 私钥
                        </Checkbox>
                      )}
                      {draft.configuredSecrets.includes("key_file_pass") && (
                        <Checkbox
                          checked={draft.clearSecrets.includes("key_file_pass")}
                          onChange={(e) =>
                            toggleClearSecret(
                              "key_file_pass",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          清除已保存的 SSH 私钥密码
                        </Checkbox>
                      )}
                    </div>
                  )}
              </>
            )}

            {draft.type === "smb" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="主机地址" required error={fieldErrors.host}>
                    <Input
                      value={draft.options.host || ""}
                      onChange={(v) => updateDraftOption("host", v)}
                      placeholder="例如 192.168.1.30"
                    />
                  </Field>
                  <Field label="端口" error={fieldErrors.port}>
                    <Input
                      inputMode="numeric"
                      value={draft.options.port || ""}
                      onChange={(v) => updateDraftOption("port", v)}
                      placeholder="留空默认 445"
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="用户名" required error={fieldErrors.user}>
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="SMB 用户名"
                    />
                  </Field>
                  <Field label="密码">
                    <Input
                      mode="password"
                      value={draft.options.pass || ""}
                      onChange={(v) => updateDraftOption("pass", v)}
                      placeholder={secretPlaceholder("pass", "可选")}
                    />
                  </Field>
                </div>
                {draft.editing && draft.configuredSecrets.includes("pass") && (
                  <Checkbox
                    checked={draft.clearSecrets.includes("pass")}
                    onChange={(e) =>
                      toggleClearSecret("pass", Boolean(e.target.checked))
                    }
                  >
                    清除已保存的 SMB 密码
                  </Checkbox>
                )}
                <Field label="域 / 工作组（可选）">
                  <Input
                    value={draft.options.domain || ""}
                    onChange={(v) => updateDraftOption("domain", v)}
                    placeholder="例如 WORKGROUP"
                  />
                </Field>
              </>
            )}

            {draft.type === "ftp" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="FTP 主机地址" required error={fieldErrors.host}>
                    <Input
                      value={draft.options.host || ""}
                      onChange={(v) => updateDraftOption("host", v)}
                      placeholder="例如 192.168.1.40 或 ftp.example.com"
                    />
                  </Field>
                  <Field label="端口" error={fieldErrors.port}>
                    <Input
                      inputMode="numeric"
                      value={draft.options.port || ""}
                      onChange={(v) => updateDraftOption("port", v)}
                      placeholder="留空默认 21"
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="用户名" required error={fieldErrors.user}>
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="FTP 用户名"
                    />
                  </Field>
                  <Field label="密码">
                    <Input
                      mode="password"
                      value={draft.options.pass || ""}
                      onChange={(v) => updateDraftOption("pass", v)}
                      placeholder={secretPlaceholder("pass", "可选")}
                    />
                  </Field>
                </div>
                <div className="native-checkbox-group">
                  <Checkbox
                    checked={isBoolTrue(draft.options, "explicit_tls")}
                    onChange={(e) =>
                      toggleDraftBoolOption("explicit_tls", Boolean(e.target.checked))
                    }
                  >
                    使用显式 FTPS（AUTH TLS / explicit_tls）
                  </Checkbox>
                  <Checkbox
                    checked={isBoolTrue(draft.options, "tls")}
                    onChange={(e) =>
                      toggleDraftBoolOption("tls", Boolean(e.target.checked))
                    }
                  >
                    使用隐式 FTPS（tls，通常端口 990）
                  </Checkbox>
                </div>
              </>
            )}

            {draft.type === "http" && (
              <>
                <Field label="HTTP 源地址" required error={fieldErrors.url}>
                  <Input
                    value={draft.options.url || ""}
                    onChange={(v) => updateDraftOption("url", v)}
                    placeholder="例如 https://mirrors.example.com/files/"
                  />
                </Field>
                <div className="sync-method-banner">
                  <IconInfoCircle aria-hidden="true" size="small" />
                  <span>
                    HTTP 后端仅支持作为同步源（只读镜像拉取），不能作为目标目录写入。
                  </span>
                </div>
              </>
            )}

            {draft.type === "b2" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field
                    label="Account ID / Key ID"
                    required
                    error={fieldErrors.account}
                  >
                    <Input
                      value={draft.options.account || ""}
                      onChange={(v) => updateDraftOption("account", v)}
                      placeholder="Backblaze B2 Key ID"
                    />
                  </Field>
                  <Field
                    label="Application Key"
                    required={!draft.editing}
                    error={fieldErrors.key}
                  >
                    <Input
                      mode="password"
                      value={draft.options.key || ""}
                      onChange={(v) => updateDraftOption("key", v)}
                      placeholder={secretPlaceholder("key", "Application Key")}
                    />
                  </Field>
                </div>
                <Field label="自定义端点（可选）">
                  <Input
                    value={draft.options.endpoint || ""}
                    onChange={(v) => updateDraftOption("endpoint", v)}
                    placeholder="留空使用默认 Backblaze B2 API"
                  />
                </Field>
                <Checkbox
                  checked={isBoolTrue(draft.options, "hard_delete")}
                  onChange={(e) =>
                    toggleDraftBoolOption("hard_delete", Boolean(e.target.checked))
                  }
                >
                  删除文件时永久删除而非隐藏旧版本（hard_delete）
                </Checkbox>
              </>
            )}

            {oauthTypes.has(draft.type) && (
              <>
                {draft.type === "onedrive" && (
                  <Field
                    label="OneDrive 区域（region）"
                    hint="请在发起网页授权前确认账号所属区域（中国世纪互联版请选择 cn）"
                  >
                    <Select
                      aria-label="OneDrive 区域（region）"
                      value={draft.options.region || "global"}
                      optionList={[
                        { value: "global", label: "国际版 (global)" },
                        { value: "cn", label: "世纪互联 (cn)" },
                        { value: "us", label: "US Gov (us)" },
                        { value: "de", label: "德国 (de)" },
                      ]}
                      onChange={(v) => updateDraftOption("region", String(v))}
                    />
                  </Field>
                )}

                {draft.type === "drive" && (
                  <Field
                    label="访问权限范围（scope）"
                    hint="发起网页授权前可按需限定只读或仅应用文件权限"
                  >
                    <Select
                      aria-label="访问权限范围（scope）"
                      value={draft.options.scope || ""}
                      optionList={[
                        { value: "", label: "完整读写权限（drive，默认）" },
                        { value: "drive.readonly", label: "仅只读（drive.readonly）" },
                        { value: "drive.file", label: "仅应用创建的文件（drive.file）" },
                        { value: "drive.appfolder", label: "仅应用专属目录（drive.appfolder）" },
                      ]}
                      onChange={(v) => updateDraftOption("scope", String(v || ""))}
                    />
                  </Field>
                )}

                {draft.type === "hidrive" && (
                  <Field label="访问权限（scope_access）">
                    <Select
                      aria-label="访问权限（scope_access）"
                      value={draft.options.scope_access || "rw"}
                      optionList={[
                        { value: "rw", label: "读写权限（rw，默认）" },
                        { value: "ro", label: "仅只读（ro）" },
                      ]}
                      onChange={(v) => updateDraftOption("scope_access", String(v))}
                    />
                  </Field>
                )}

                <NativeStorageAuthWizard
                  remoteType={draft.type}
                  remoteLabel={remoteTypeLabels[draft.type] || draft.type}
                  remoteName={connectionName}
                  editing={draft.editing}
                  draftOptions={buildRemoteOptions(draft)}
                  clearSecrets={draft.clearSecrets}
                  hasExistingAuth={
                    Boolean(draft.options.token) ||
                    (draft.editing &&
                      draft.configuredSecrets.includes("token") &&
                      !draft.clearSecrets.includes("token"))
                  }
                  completedSession={
                    draft.authSessionId
                      ? {
                          sessionId: draft.authSessionId,
                          publicOptions: draft.authPublicOptions || {},
                          configuredSecrets: draft.authConfiguredSecrets || [],
                        }
                      : null
                  }
                  variant={draft.type === "jottacloud" ? "jottacloud" : "oauth"}
                  disabled={savingForm || testingForm}
                  onCompleted={handleAuthCompleted}
                />

                {fieldErrors.token && (
                  <div className="inline-error" role="alert">
                    {fieldErrors.token}
                  </div>
                )}

                {draft.type === "onedrive" && (
                  <div className="form-grid form-grid--two">
                    <Field label="Drive ID（授权后自动填入）">
                      <Input
                        value={draft.options.drive_id || ""}
                        onChange={(v) => updateDraftOption("drive_id", v)}
                        placeholder="完成上方网页授权后自动获取，也可手动填写"
                      />
                    </Field>
                    <Field label="Drive 类型">
                      <Select
                        value={draft.options.drive_type || ""}
                        placeholder="自动"
                        optionList={[
                          { value: "", label: "自动" },
                          { value: "personal", label: "个人版 (personal)" },
                          { value: "business", label: "商业版 (business)" },
                          { value: "documentLibrary", label: "SharePoint 文档库" },
                        ]}
                        onChange={(v) => updateDraftOption("drive_type", String(v || ""))}
                      />
                    </Field>
                  </div>
                )}
                {draft.type === "drive" && (
                  <>
                    <div className="form-grid form-grid--two">
                      <Field label="根目录 ID（root_folder_id，可选）">
                        <Input
                          value={draft.options.root_folder_id || ""}
                          onChange={(v) => updateDraftOption("root_folder_id", v)}
                          placeholder="留空从云端硬盘根目录开始"
                        />
                      </Field>
                      <Field label="共享云端硬盘 ID（team_drive，可选）">
                        <Input
                          value={draft.options.team_drive || ""}
                          onChange={(v) => updateDraftOption("team_drive", v)}
                          placeholder="完成上方授权向导选择团队盘时自动填入"
                        />
                      </Field>
                    </div>
                    <Field label="Service Account JSON 凭据（可选，替代网页授权）">
                      <TextArea
                        autosize={{ minRows: 2, maxRows: 4 }}
                        value={draft.options.service_account_credentials || ""}
                        onChange={(v) =>
                          updateDraftOption("service_account_credentials", v)
                        }
                        placeholder={secretPlaceholder(
                          "service_account_credentials",
                          '{"type":"service_account",...}',
                        )}
                      />
                    </Field>
                  </>
                )}
                {draft.type === "pcloud" && (
                  <>
                    <div className="form-grid form-grid--two">
                      <Field label="API 区域主机">
                        <Select
                          value={draft.options.hostname || "api.pcloud.com"}
                          optionList={[
                            { value: "api.pcloud.com", label: "美国区域 (api.pcloud.com)" },
                            { value: "eapi.pcloud.com", label: "欧洲区域 (eapi.pcloud.com)" },
                          ]}
                          onChange={(v) => updateDraftOption("hostname", String(v))}
                        />
                      </Field>
                      <Field label="根目录 ID（root_folder_id，可选）">
                        <Input
                          value={draft.options.root_folder_id || ""}
                          onChange={(v) => updateDraftOption("root_folder_id", v)}
                          placeholder="留空从根目录开始"
                        />
                      </Field>
                    </div>
                    <div className="form-grid form-grid--two">
                      <Field label="账号邮箱（username，可选，替代网页授权）">
                        <Input
                          value={draft.options.username || ""}
                          onChange={(v) => updateDraftOption("username", v)}
                          placeholder="user@example.com"
                        />
                      </Field>
                      <Field label="登录密码（password，可选）">
                        <Input
                          mode="password"
                          value={draft.options.password || ""}
                          onChange={(v) => updateDraftOption("password", v)}
                          placeholder={secretPlaceholder("password", "可选密码")}
                        />
                      </Field>
                    </div>
                  </>
                )}
                {draft.type === "box" && (
                  <>
                    <div className="form-grid form-grid--three">
                      <Field label="Box 账户类型">
                        <Select
                          value={draft.options.box_sub_type || "user"}
                          optionList={[
                            { value: "user", label: "个人账户 (user)" },
                            { value: "enterprise", label: "企业账户 (enterprise)" },
                          ]}
                          onChange={(v) => updateDraftOption("box_sub_type", String(v))}
                        />
                      </Field>
                      <Field label="根目录 ID（root_folder_id）">
                        <Input
                          value={draft.options.root_folder_id || ""}
                          onChange={(v) => updateDraftOption("root_folder_id", v)}
                          placeholder="默认 0"
                        />
                      </Field>
                      <Field label="归属用户（owned_by）">
                        <Input
                          value={draft.options.owned_by || ""}
                          onChange={(v) => updateDraftOption("owned_by", v)}
                          placeholder="可选用户邮箱"
                        />
                      </Field>
                    </div>
                  </>
                )}
                {draft.type === "yandex" && (
                  <div className="native-checkbox-group">
                    <Checkbox
                      checked={isBoolTrue(draft.options, "app_folder")}
                      onChange={(e) =>
                        toggleDraftBoolOption("app_folder", Boolean(e.target.checked))
                      }
                    >
                      仅限应用专属目录（app_folder）
                    </Checkbox>
                    <Checkbox
                      checked={isBoolTrue(draft.options, "hard_delete")}
                      onChange={(e) =>
                        toggleDraftBoolOption("hard_delete", Boolean(e.target.checked))
                      }
                    >
                      永久删除而非移入回收站（hard_delete）
                    </Checkbox>
                  </div>
                )}
                {draft.type === "jottacloud" && (
                  <div className="native-checkbox-group">
                    <Checkbox
                      checked={isBoolTrue(draft.options, "trashed_only")}
                      onChange={(e) =>
                        toggleDraftBoolOption("trashed_only", Boolean(e.target.checked))
                      }
                    >
                      仅列出回收站文件（trashed_only）
                    </Checkbox>
                    <Checkbox
                      checked={isBoolTrue(draft.options, "hard_delete")}
                      onChange={(e) =>
                        toggleDraftBoolOption("hard_delete", Boolean(e.target.checked))
                      }
                    >
                      永久删除而非移入回收站（hard_delete）
                    </Checkbox>
                    <Checkbox
                      checked={isBoolTrue(draft.options, "no_versions")}
                      onChange={(e) =>
                        toggleDraftBoolOption("no_versions", Boolean(e.target.checked))
                      }
                    >
                      覆盖时避免保留历史版本（no_versions）
                    </Checkbox>
                  </div>
                )}
                {draft.type === "hidrive" && (
                  <div className="form-grid form-grid--two">
                    <Field label="根目录前缀（root_prefix）">
                      <Input
                        value={draft.options.root_prefix || ""}
                        onChange={(v) => updateDraftOption("root_prefix", v)}
                        placeholder="默认 /"
                      />
                    </Field>
                    <Field label="自定义端点（endpoint，可选）">
                      <Input
                        value={draft.options.endpoint || ""}
                        onChange={(v) => updateDraftOption("endpoint", v)}
                        placeholder="留空使用默认 HiDrive API"
                      />
                    </Field>
                  </div>
                )}
                {draft.type === "huaweidrive" && (
                  <div className="form-grid form-grid--two">
                    <Field label="根目录 ID（root_folder_id，可选）">
                      <Input
                        value={draft.options.root_folder_id || ""}
                        onChange={(v) => updateDraftOption("root_folder_id", v)}
                        placeholder="留空使用根目录"
                      />
                    </Field>
                    <Field label="单页列举条数（list_chunk）">
                      <Input
                        inputMode="numeric"
                        value={draft.options.list_chunk || ""}
                        onChange={(v) => updateDraftOption("list_chunk", v)}
                        placeholder="默认 100"
                      />
                    </Field>
                  </div>
                )}

                <div className="advanced-options">
                  <button
                    type="button"
                    className={`advanced-options-toggle${oauthAdvancedOpen ? " is-open" : ""}`}
                    aria-expanded={oauthAdvancedOpen}
                    onClick={() => setOauthAdvancedOpen((o) => !o)}
                  >
                    <IconChevronRight
                      className="advanced-options-chevron"
                      aria-hidden="true"
                    />
                    <span>高级凭据设置（自定义 Client ID / 手动填入 Token，可选）</span>
                  </button>
                  {oauthAdvancedOpen && (
                    <div className="advanced-options-body">
                      <div className="form-grid form-grid--two">
                        <Field
                          label="自定义 Client ID（可选）"
                          hint="填写自建应用凭据后，点击上方授权按钮将使用您的专属应用"
                        >
                          <Input
                            value={draft.options.client_id || ""}
                            onChange={(v) => updateDraftOption("client_id", v)}
                            placeholder="留空使用内置应用"
                          />
                        </Field>
                        <Field label="自定义 Client Secret（可选）">
                          <Input
                            mode="password"
                            value={draft.options.client_secret || ""}
                            onChange={(v) => updateDraftOption("client_secret", v)}
                            placeholder={secretPlaceholder("client_secret", "可选")}
                          />
                        </Field>
                      </div>
                      <Field
                        label="手动填写 OAuth Token JSON（可选）"
                        hint="通常无需手动填写，直接使用上方图形化授权即可；如已有现成 JSON 令牌，也可直接粘贴"
                      >
                        <TextArea
                          aria-label="OAuth Token JSON"
                          autosize={{ minRows: 2, maxRows: 5 }}
                          value={draft.options.token || ""}
                          onChange={(v) => updateDraftOption("token", v)}
                          placeholder={secretPlaceholder(
                            "token",
                            '{"access_token":"...","refresh_token":"...","expiry":"..."}',
                          )}
                        />
                      </Field>
                    </div>
                  )}
                </div>
              </>
            )}

            {draft.type === "mega" && (
              <div className="form-grid form-grid--two">
                <Field label="MEGA 账号邮箱" required error={fieldErrors.user}>
                  <Input
                    value={draft.options.user || ""}
                    onChange={(v) => updateDraftOption("user", v)}
                    placeholder="user@example.com"
                  />
                </Field>
                <Field label="密码" required={!draft.editing} error={fieldErrors.pass}>
                  <Input
                    mode="password"
                    value={draft.options.pass || ""}
                    onChange={(v) => updateDraftOption("pass", v)}
                    placeholder={secretPlaceholder("pass", "MEGA 密码")}
                  />
                </Field>
              </div>
            )}

            {draft.type === "azureblob" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="存储账户名（account）" error={fieldErrors.account}>
                    <Input
                      value={draft.options.account || ""}
                      onChange={(v) => updateDraftOption("account", v)}
                      placeholder="Azure Storage Account"
                    />
                  </Field>
                  <Field label="访问密钥（key）">
                    <Input
                      mode="password"
                      value={draft.options.key || ""}
                      onChange={(v) => updateDraftOption("key", v)}
                      placeholder={secretPlaceholder("key", "Storage Account Key")}
                    />
                  </Field>
                </div>
                <Field label="SAS URL（可选，替代 Account + Key）">
                  <Input
                    mode="password"
                    value={draft.options.sas_url || ""}
                    onChange={(v) => updateDraftOption("sas_url", v)}
                    placeholder={secretPlaceholder("sas_url", "https://...?sv=...")}
                  />
                </Field>
                <div className="form-grid form-grid--three">
                  <Field label="Tenant ID（可选，Service Principal）">
                    <Input
                      value={draft.options.tenant || ""}
                      onChange={(v) => updateDraftOption("tenant", v)}
                      placeholder="Azure AD Tenant ID"
                    />
                  </Field>
                  <Field label="Client ID（可选）">
                    <Input
                      value={draft.options.client_id || ""}
                      onChange={(v) => updateDraftOption("client_id", v)}
                      placeholder="Application Client ID"
                    />
                  </Field>
                  <Field label="Client Secret（可选）">
                    <Input
                      mode="password"
                      value={draft.options.client_secret || ""}
                      onChange={(v) => updateDraftOption("client_secret", v)}
                      placeholder={secretPlaceholder("client_secret", "Client Secret")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="自定义端点（可选）">
                    <Input
                      value={draft.options.endpoint || ""}
                      onChange={(v) => updateDraftOption("endpoint", v)}
                      placeholder="留空使用默认 Azure Blob 端点"
                    />
                  </Field>
                  <Field label="访问层（access_tier）">
                    <Select
                      value={draft.options.access_tier || ""}
                      optionList={[
                        { value: "", label: "默认" },
                        { value: "hot", label: "Hot" },
                        { value: "cool", label: "Cool" },
                        { value: "cold", label: "Cold" },
                        { value: "archive", label: "Archive" },
                      ]}
                      onChange={(v) => updateDraftOption("access_tier", String(v || ""))}
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "azurefiles" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="存储账户名（account）" error={fieldErrors.account}>
                    <Input
                      value={draft.options.account || ""}
                      onChange={(v) => updateDraftOption("account", v)}
                      placeholder="Azure Storage Account"
                    />
                  </Field>
                  <Field label="访问密钥（key）">
                    <Input
                      mode="password"
                      value={draft.options.key || ""}
                      onChange={(v) => updateDraftOption("key", v)}
                      placeholder={secretPlaceholder("key", "Storage Account Key")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="SAS URL（可选，替代 Account + Key）">
                    <Input
                      mode="password"
                      value={draft.options.sas_url || ""}
                      onChange={(v) => updateDraftOption("sas_url", v)}
                      placeholder={secretPlaceholder("sas_url", "https://...?sv=...")}
                    />
                  </Field>
                  <Field label="连接字符串（connection_string，可选）">
                    <Input
                      mode="password"
                      value={draft.options.connection_string || ""}
                      onChange={(v) => updateDraftOption("connection_string", v)}
                      placeholder={secretPlaceholder("connection_string", "DefaultEndpointsProtocol=...")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="共享名称（share_name，可选）">
                    <Input
                      value={draft.options.share_name || ""}
                      onChange={(v) => updateDraftOption("share_name", v)}
                      placeholder="限定单个 Azure Files Share（可选）"
                    />
                  </Field>
                  <Field label="自定义端点（endpoint，可选）">
                    <Input
                      value={draft.options.endpoint || ""}
                      onChange={(v) => updateDraftOption("endpoint", v)}
                      placeholder="留空使用默认 Azure Files 端点"
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "googlecloudstorage" && (
              <>
                <NativeStorageAuthWizard
                  remoteType={draft.type}
                  remoteLabel={remoteTypeLabels[draft.type] || draft.type}
                  remoteName={connectionName}
                  editing={draft.editing}
                  draftOptions={buildRemoteOptions(draft)}
                  clearSecrets={draft.clearSecrets}
                  hasExistingAuth={
                    Boolean(draft.options.token) ||
                    (draft.editing &&
                      draft.configuredSecrets.includes("token") &&
                      !draft.clearSecrets.includes("token"))
                  }
                  completedSession={
                    draft.authSessionId
                      ? {
                          sessionId: draft.authSessionId,
                          publicOptions: draft.authPublicOptions || {},
                          configuredSecrets: draft.authConfiguredSecrets || [],
                        }
                      : null
                  }
                  variant="oauth"
                  disabled={savingForm || testingForm}
                  onCompleted={handleAuthCompleted}
                />
                <Field
                  label="Service Account JSON 凭据（可选，替代网页授权）"
                  error={fieldErrors.service_account_credentials}
                >
                  <TextArea
                    autosize={{ minRows: 2, maxRows: 5 }}
                    value={draft.options.service_account_credentials || ""}
                    onChange={(v) =>
                      updateDraftOption("service_account_credentials", v)
                    }
                    placeholder={secretPlaceholder(
                      "service_account_credentials",
                      '{"type":"service_account",...}',
                    )}
                  />
                </Field>
                <div className="form-grid form-grid--two">
                  <Field label="项目编号（project_number）">
                    <Input
                      value={draft.options.project_number || ""}
                      onChange={(v) => updateDraftOption("project_number", v)}
                      placeholder="列举/新建存储桶时需要"
                    />
                  </Field>
                  <Field label="区域（location）">
                    <Input
                      value={draft.options.location || ""}
                      onChange={(v) => updateDraftOption("location", v)}
                      placeholder="如 us / asia-east1"
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="OAuth Client ID（可选）">
                    <Input
                      value={draft.options.client_id || ""}
                      onChange={(v) => updateDraftOption("client_id", v)}
                      placeholder="可选 Client ID"
                    />
                  </Field>
                  <Field label="OAuth Client Secret（可选）">
                    <Input
                      mode="password"
                      value={draft.options.client_secret || ""}
                      onChange={(v) => updateDraftOption("client_secret", v)}
                      placeholder={secretPlaceholder("client_secret", "可选")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--three">
                  <Field label="对象 ACL（object_acl）">
                    <Input
                      value={draft.options.object_acl || ""}
                      onChange={(v) => updateDraftOption("object_acl", v)}
                      placeholder="如 private / projectPrivate"
                    />
                  </Field>
                  <Field label="存储桶 ACL（bucket_acl）">
                    <Input
                      value={draft.options.bucket_acl || ""}
                      onChange={(v) => updateDraftOption("bucket_acl", v)}
                      placeholder="如 private / authenticatedRead"
                    />
                  </Field>
                  <Field label="自定义端点（endpoint，可选）">
                    <Input
                      value={draft.options.endpoint || ""}
                      onChange={(v) => updateDraftOption("endpoint", v)}
                      placeholder="可选自定义 GCS URL"
                    />
                  </Field>
                </div>
                <div className="native-checkbox-group">
                  <Checkbox
                    checked={isBoolTrue(draft.options, "bucket_policy_only")}
                    onChange={(e) =>
                      toggleDraftBoolOption("bucket_policy_only", Boolean(e.target.checked))
                    }
                  >
                    统一存储桶级权限（bucket_policy_only）
                  </Checkbox>
                  <Checkbox
                    checked={isBoolTrue(draft.options, "anonymous")}
                    onChange={(e) =>
                      toggleDraftBoolOption("anonymous", Boolean(e.target.checked))
                    }
                  >
                    匿名访问公共存储桶（无需凭据，仅只读）
                  </Checkbox>
                </div>
              </>
            )}

            {draft.type === "swift" && (
              <>
                <Field
                  label="认证地址（auth）"
                  required={!(draft.options.storage_url || "").trim()}
                  error={fieldErrors.auth}
                >
                  <Input
                    value={draft.options.auth || ""}
                    onChange={(v) => updateDraftOption("auth", v)}
                    placeholder="https://identity.example.com/v3"
                  />
                </Field>
                <div className="form-grid form-grid--two">
                  <Field label="用户名（user）" error={fieldErrors.user}>
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="Swift 用户名"
                    />
                  </Field>
                  <Field label="密码 / API Key（key）">
                    <Input
                      mode="password"
                      value={draft.options.key || ""}
                      onChange={(v) => updateDraftOption("key", v)}
                      placeholder={secretPlaceholder("key", "密码或密钥")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--three">
                  <Field label="租户 / 项目（tenant）">
                    <Input
                      value={draft.options.tenant || ""}
                      onChange={(v) => updateDraftOption("tenant", v)}
                      placeholder="可选"
                    />
                  </Field>
                  <Field label="租户 ID（tenant_id）">
                    <Input
                      value={draft.options.tenant_id || ""}
                      onChange={(v) => updateDraftOption("tenant_id", v)}
                      placeholder="可选"
                    />
                  </Field>
                  <Field label="区域（region）">
                    <Input
                      value={draft.options.region || ""}
                      onChange={(v) => updateDraftOption("region", v)}
                      placeholder="可选"
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="域（domain）">
                    <Input
                      value={draft.options.domain || ""}
                      onChange={(v) => updateDraftOption("domain", v)}
                      placeholder="例如 default"
                    />
                  </Field>
                  <Field label="租户域（tenant_domain）">
                    <Input
                      value={draft.options.tenant_domain || ""}
                      onChange={(v) => updateDraftOption("tenant_domain", v)}
                      placeholder="例如 default"
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="直连 Storage URL（可选，免身份认证）">
                    <Input
                      value={draft.options.storage_url || ""}
                      onChange={(v) => updateDraftOption("storage_url", v)}
                      placeholder="https://swift.example.com/v1/AUTH_..."
                    />
                  </Field>
                  <Field label="直连 Auth Token（可选）">
                    <Input
                      mode="password"
                      value={draft.options.auth_token || ""}
                      onChange={(v) => updateDraftOption("auth_token", v)}
                      placeholder={secretPlaceholder("auth_token", "Auth Token")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--three">
                  <Field label="应用凭据 ID（application_credential_id）">
                    <Input
                      value={draft.options.application_credential_id || ""}
                      onChange={(v) =>
                        updateDraftOption("application_credential_id", v)
                      }
                      placeholder="可选"
                    />
                  </Field>
                  <Field label="应用凭据名（application_credential_name）">
                    <Input
                      value={draft.options.application_credential_name || ""}
                      onChange={(v) =>
                        updateDraftOption("application_credential_name", v)
                      }
                      placeholder="可选"
                    />
                  </Field>
                  <Field label="应用凭据密钥（application_credential_secret）">
                    <Input
                      mode="password"
                      value={draft.options.application_credential_secret || ""}
                      onChange={(v) =>
                        updateDraftOption("application_credential_secret", v)
                      }
                      placeholder={secretPlaceholder(
                        "application_credential_secret",
                        "Secret",
                      )}
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "oracleobjectstorage" && (
              <>
                <div className="form-grid form-grid--three">
                  <Field label="Namespace" required error={fieldErrors.namespace}>
                    <Input
                      value={draft.options.namespace || ""}
                      onChange={(v) => updateDraftOption("namespace", v)}
                      placeholder="Object Storage Namespace"
                    />
                  </Field>
                  <Field label="Region" required error={fieldErrors.region}>
                    <Input
                      value={draft.options.region || ""}
                      onChange={(v) => updateDraftOption("region", v)}
                      placeholder="如 us-ashburn-1 / ap-tokyo-1"
                    />
                  </Field>
                  <Field
                    label="认证方式（provider）"
                    hint="OCI 配置文件需在应用环境设置 RCLONE_OOS_CONFIG_FILE 的绝对路径，配置中的私钥也需使用绝对路径。实例主体仅适用于 OCI 实例。"
                  >
                    <Select
                      value={draft.options.provider || "env_auth"}
                      optionList={[
                        { value: "user_principal_auth", label: "OCI 配置文件（应用环境）" },
                        { value: "env_auth", label: "环境变量 (env_auth)" },
                        { value: "instance_principal_auth", label: "实例主体" },
                        { value: "no_auth", label: "无认证（公共读）" },
                      ]}
                      onChange={(v) => updateDraftOption("provider", String(v))}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--three">
                  <Field label="Compartment OCID（可选）">
                    <Input
                      value={draft.options.compartment || ""}
                      onChange={(v) => updateDraftOption("compartment", v)}
                      placeholder="ocid1.compartment..."
                    />
                  </Field>
                  <Field label="存储等级（storage_tier）">
                    <Select
                      value={draft.options.storage_tier || ""}
                      optionList={[
                        { value: "", label: "默认 (Standard)" },
                        { value: "Standard", label: "Standard" },
                        { value: "InfrequentAccess", label: "InfrequentAccess" },
                        { value: "Archive", label: "Archive" },
                      ]}
                      onChange={(v) => updateDraftOption("storage_tier", String(v || ""))}
                    />
                  </Field>
                  <Field label="自定义端点（endpoint，可选）">
                    <Input
                      value={draft.options.endpoint || ""}
                      onChange={(v) => updateDraftOption("endpoint", v)}
                      placeholder="留空使用区域默认端点"
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "qingstor" && (
              <>
                <Field
                  label="认证方式"
                  error={fieldErrors.env_auth}
                  hint="运行时凭据从应用进程环境变量读取；留空密钥可用于匿名访问"
                >
                  <Select
                    value={draft.authMode}
                    optionList={[
                      { value: "keys", label: "Access Key + Secret" },
                      { value: "runtime", label: "运行时凭据（env_auth）" },
                      { value: "anonymous", label: "匿名访问" },
                    ]}
                    onChange={(v) => handleStorageAuthModeChange(String(v))}
                  />
                </Field>
                <div className="form-grid form-grid--two">
                  <Field
                    label="Access Key ID"
                    required={draft.authMode === "keys"}
                    error={fieldErrors.access_key_id}
                  >
                    <Input
                      value={draft.options.access_key_id || ""}
                      disabled={draft.authMode !== "keys"}
                      onChange={(v) => updateDraftOption("access_key_id", v)}
                      placeholder="QingStor Access Key ID"
                    />
                  </Field>
                  <Field
                    label="Secret Access Key"
                    required={draft.authMode === "keys" && !draft.editing}
                    error={fieldErrors.secret_access_key}
                  >
                    <Input
                      mode="password"
                      value={draft.options.secret_access_key || ""}
                      disabled={draft.authMode !== "keys"}
                      onChange={(v) => updateDraftOption("secret_access_key", v)}
                      placeholder={secretPlaceholder("secret_access_key", "Secret Access Key")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="区域（zone，可选）">
                    <Input
                      value={draft.options.zone || ""}
                      onChange={(v) => updateDraftOption("zone", v)}
                      placeholder="如 pek3a / sh1a / gd2a"
                    />
                  </Field>
                  <Field label="自定义端点（endpoint，可选）">
                    <Input
                      value={draft.options.endpoint || ""}
                      onChange={(v) => updateDraftOption("endpoint", v)}
                      placeholder="默认 https://stor.qingcloud.com:443"
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "storj" && (
              <>
                <Field label="Access Grant（推荐）" error={fieldErrors.access_grant}>
                  <TextArea
                    autosize={{ minRows: 2, maxRows: 4 }}
                    value={draft.options.access_grant || ""}
                    onChange={(v) => updateDraftOption("access_grant", v)}
                    placeholder={secretPlaceholder(
                      "access_grant",
                      "粘贴 Storj Access Grant，或下方填写 Satellite + API Key + Passphrase",
                    )}
                  />
                </Field>
                <div className="form-grid form-grid--three">
                  <Field label="Satellite 地址（可选）">
                    <Input
                      value={draft.options.satellite_address || ""}
                      onChange={(v) => updateDraftOption("satellite_address", v)}
                      placeholder="us1.storj.io"
                    />
                  </Field>
                  <Field label="API Key（可选）">
                    <Input
                      mode="password"
                      value={draft.options.api_key || ""}
                      onChange={(v) => updateDraftOption("api_key", v)}
                      placeholder={secretPlaceholder("api_key", "API Key")}
                    />
                  </Field>
                  <Field label="加密短语（passphrase，可选）">
                    <Input
                      mode="password"
                      value={draft.options.passphrase || ""}
                      onChange={(v) => updateDraftOption("passphrase", v)}
                      placeholder={secretPlaceholder("passphrase", "Encryption Passphrase")}
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "koofr" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="服务地址（endpoint）" required error={fieldErrors.endpoint}>
                    <Input
                      value={draft.options.endpoint || "https://app.koofr.net"}
                      onChange={(v) => updateDraftOption("endpoint", v)}
                      placeholder="https://app.koofr.net"
                    />
                  </Field>
                  <Field label="提供方（provider）">
                    <Select
                      value={draft.options.provider || "koofr"}
                      optionList={[
                        { value: "koofr", label: "Koofr" },
                        { value: "digiboxx", label: "DigiBoxx" },
                        { value: "other", label: "其他兼容服务" },
                      ]}
                      onChange={(v) => updateDraftOption("provider", String(v))}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="用户名 / 邮箱（user）" required error={fieldErrors.user}>
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="user@example.com"
                    />
                  </Field>
                  <Field
                    label="应用专属密码（password）"
                    required={!draft.editing}
                    error={fieldErrors.password}
                  >
                    <Input
                      mode="password"
                      value={draft.options.password || ""}
                      onChange={(v) => updateDraftOption("password", v)}
                      placeholder={secretPlaceholder("password", "Koofr App Password")}
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "seafile" && (
              <>
                <Field label="Seafile 服务地址（url）" required error={fieldErrors.url}>
                  <Input
                    value={draft.options.url || ""}
                    onChange={(v) => updateDraftOption("url", v)}
                    placeholder="https://cloud.seafile.com"
                  />
                </Field>
                <div className="form-grid form-grid--two">
                  <Field label="登录邮箱 / 用户名（user）" required error={fieldErrors.user}>
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="user@example.com"
                    />
                  </Field>
                  <Field label="密码（pass）" error={fieldErrors.pass}>
                    <Input
                      mode="password"
                      value={draft.options.pass || ""}
                      onChange={(v) => updateDraftOption("pass", v)}
                      placeholder={secretPlaceholder("pass", "密码（或使用下方 2FA 获取 Auth Token）")}
                    />
                  </Field>
                </div>
                <NativeStorageAuthWizard
                  remoteType={draft.type}
                  remoteLabel={remoteTypeLabels[draft.type] || draft.type}
                  remoteName={connectionName}
                  editing={draft.editing}
                  draftOptions={buildRemoteOptions(draft)}
                  clearSecrets={draft.clearSecrets}
                  hasExistingAuth={
                    Boolean(draft.options.auth_token) ||
                    (draft.editing &&
                      draft.configuredSecrets.includes("auth_token") &&
                      !draft.clearSecrets.includes("auth_token"))
                  }
                  completedSession={
                    draft.authSessionId
                      ? {
                          sessionId: draft.authSessionId,
                          publicOptions: draft.authPublicOptions || {},
                          configuredSecrets: draft.authConfiguredSecrets || [],
                        }
                      : null
                  }
                  variant="seafile"
                  disabled={savingForm || testingForm}
                  onCompleted={handleAuthCompleted}
                />
                <div className="form-grid form-grid--three">
                  <Field label="Auth Token（可选，开启 2FA 时使用）">
                    <Input
                      mode="password"
                      value={draft.options.auth_token || ""}
                      onChange={(v) => updateDraftOption("auth_token", v)}
                      placeholder={secretPlaceholder("auth_token", "API Auth Token")}
                    />
                  </Field>
                  <Field label="限定资料库名称（library，可选）">
                    <Input
                      value={draft.options.library || ""}
                      onChange={(v) => updateDraftOption("library", v)}
                      placeholder="留空访问全部资料库"
                    />
                  </Field>
                  <Field label="加密资料库密码（library_key，可选）">
                    <Input
                      mode="password"
                      value={draft.options.library_key || ""}
                      onChange={(v) => updateDraftOption("library_key", v)}
                      placeholder={secretPlaceholder("library_key", "可选")}
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "pikpak" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="PikPak 账号（user）" required error={fieldErrors.user}>
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="邮箱或用户名"
                    />
                  </Field>
                  <Field label="密码（pass）" required={!draft.editing} error={fieldErrors.pass}>
                    <Input
                      mode="password"
                      value={draft.options.pass || ""}
                      onChange={(v) => updateDraftOption("pass", v)}
                      placeholder={secretPlaceholder("pass", "登录密码")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="根目录 ID（root_folder_id，可选）">
                    <Input
                      value={draft.options.root_folder_id || ""}
                      onChange={(v) => updateDraftOption("root_folder_id", v)}
                      placeholder="留空从根目录开始"
                    />
                  </Field>
                  <Field label="设备 ID（device_id，可选）">
                    <Input
                      value={draft.options.device_id || ""}
                      onChange={(v) => updateDraftOption("device_id", v)}
                      placeholder="可选"
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "protondrive" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="Proton 用户名" required error={fieldErrors.username}>
                    <Input
                      value={draft.options.username || ""}
                      onChange={(v) => updateDraftOption("username", v)}
                      placeholder="user@proton.me"
                    />
                  </Field>
                  <Field
                    label="登录密码"
                    required={!draft.editing}
                    error={fieldErrors.password}
                  >
                    <Input
                      mode="password"
                      value={draft.options.password || ""}
                      onChange={(v) => updateDraftOption("password", v)}
                      placeholder={secretPlaceholder("password", "Proton 登录密码")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="邮箱解密密码（mailbox_password，双密码账户）">
                    <Input
                      mode="password"
                      value={draft.options.mailbox_password || ""}
                      onChange={(v) => updateDraftOption("mailbox_password", v)}
                      placeholder={secretPlaceholder("mailbox_password", "可选")}
                    />
                  </Field>
                  <Field label="2FA TOTP 密钥（otp_secret_key，可选）">
                    <Input
                      mode="password"
                      value={draft.options.otp_secret_key || ""}
                      onChange={(v) => updateDraftOption("otp_secret_key", v)}
                      placeholder={secretPlaceholder("otp_secret_key", "Base32 2FA Secret")}
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "iclouddrive" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="Apple ID" required error={fieldErrors.apple_id}>
                    <Input
                      value={draft.options.apple_id || ""}
                      onChange={(v) => updateDraftOption("apple_id", v)}
                      placeholder="user@icloud.com"
                    />
                  </Field>
                  <Field
                    label="密码（password）"
                    required={!draft.editing}
                    error={fieldErrors.password}
                  >
                    <Input
                      mode="password"
                      value={draft.options.password || ""}
                      onChange={(v) => updateDraftOption("password", v)}
                      placeholder={secretPlaceholder("password", "Apple ID 密码")}
                    />
                  </Field>
                </div>
                <NativeStorageAuthWizard
                  remoteType={draft.type}
                  remoteLabel={remoteTypeLabels[draft.type] || draft.type}
                  remoteName={connectionName}
                  editing={draft.editing}
                  draftOptions={buildRemoteOptions(draft)}
                  clearSecrets={draft.clearSecrets}
                  hasExistingAuth={
                    Boolean(draft.options.trust_token || draft.options.cookies) ||
                    (draft.editing &&
                      (draft.configuredSecrets.includes("trust_token") ||
                        draft.configuredSecrets.includes("cookies")))
                  }
                  completedSession={
                    draft.authSessionId
                      ? {
                          sessionId: draft.authSessionId,
                          publicOptions: draft.authPublicOptions || {},
                          configuredSecrets: draft.authConfiguredSecrets || [],
                        }
                      : null
                  }
                  variant="icloud"
                  disabled={savingForm || testingForm}
                  onCompleted={handleAuthCompleted}
                />
                <div className="form-grid form-grid--two">
                  <Field label="Trust Token（验证后自动获取，可选）">
                    <Input
                      mode="password"
                      value={draft.options.trust_token || ""}
                      onChange={(v) => updateDraftOption("trust_token", v)}
                      placeholder={secretPlaceholder("trust_token", "完成上方登录验证后自动获取")}
                    />
                  </Field>
                  <Field label="Cookies（验证后自动获取，可选）">
                    <Input
                      mode="password"
                      value={draft.options.cookies || ""}
                      onChange={(v) => updateDraftOption("cookies", v)}
                      placeholder={secretPlaceholder("cookies", "完成上方登录验证后自动获取")}
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "filen" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="Filen 邮箱（email）" required error={fieldErrors.email}>
                    <Input
                      value={draft.options.email || ""}
                      onChange={(v) => updateDraftOption("email", v)}
                      placeholder="user@example.com"
                    />
                  </Field>
                  <Field
                    label="密码（password）"
                    required={!draft.editing}
                    error={fieldErrors.password}
                  >
                    <Input
                      mode="password"
                      value={draft.options.password || ""}
                      onChange={(v) => updateDraftOption("password", v)}
                      placeholder={secretPlaceholder("password", "Filen 登录密码")}
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field
                    label="API Key"
                    required={!draft.editing}
                    error={fieldErrors.api_key}
                  >
                    <Input
                      mode="password"
                      value={draft.options.api_key || ""}
                      onChange={(v) => updateDraftOption("api_key", v)}
                      placeholder={secretPlaceholder("api_key", "Filen API Key")}
                    />
                  </Field>
                  <Field label="根目录 UUID（base_folder_uuid，可选）">
                    <Input
                      value={draft.options.base_folder_uuid || ""}
                      onChange={(v) => updateDraftOption("base_folder_uuid", v)}
                      placeholder="留空为根文件夹"
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "mailru" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="Mail.ru 邮箱（user）" error={fieldErrors.user}>
                    <Input
                      value={draft.options.user || ""}
                      onChange={(v) => updateDraftOption("user", v)}
                      placeholder="user@mail.ru"
                    />
                  </Field>
                  <Field label="应用密码（pass）">
                    <Input
                      mode="password"
                      value={draft.options.pass || ""}
                      onChange={(v) => updateDraftOption("pass", v)}
                      placeholder={secretPlaceholder("pass", "Mail.ru 应用专用密码")}
                    />
                  </Field>
                </div>
                <Field label="或提供 OAuth Token JSON（可选）">
                  <TextArea
                    autosize={{ minRows: 2, maxRows: 4 }}
                    value={draft.options.token || ""}
                    onChange={(v) => updateDraftOption("token", v)}
                    placeholder={secretPlaceholder("token", '{"access_token":...}')}
                  />
                </Field>
              </>
            )}

            {draft.type === "fichier" && (
              <>
                <div className="form-grid form-grid--two">
                  <Field label="1Fichier API Key" error={fieldErrors.api_key}>
                    <Input
                      mode="password"
                      value={draft.options.api_key || ""}
                      onChange={(v) => updateDraftOption("api_key", v)}
                      placeholder={secretPlaceholder("api_key", "API Key（与共享目录二选一）")}
                    />
                  </Field>
                  <Field label="共享目录链接（shared_folder，可选）">
                    <Input
                      value={draft.options.shared_folder || ""}
                      onChange={(v) => updateDraftOption("shared_folder", v)}
                      placeholder="https://1fichier.com/dir/..."
                    />
                  </Field>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="文件解压/下载密码（file_password，可选）">
                    <Input
                      mode="password"
                      value={draft.options.file_password || ""}
                      onChange={(v) => updateDraftOption("file_password", v)}
                      placeholder={secretPlaceholder("file_password", "可选")}
                    />
                  </Field>
                  <Field label="目录密码（folder_password，可选）">
                    <Input
                      mode="password"
                      value={draft.options.folder_password || ""}
                      onChange={(v) => updateDraftOption("folder_password", v)}
                      placeholder={secretPlaceholder("folder_password", "可选")}
                    />
                  </Field>
                </div>
              </>
            )}

            {draft.type === "gofile" && (
              <div className="form-grid form-grid--three">
                <Field label="Access Token（API Key）" error={fieldErrors.access_token}>
                  <Input
                    mode="password"
                    value={draft.options.access_token || ""}
                    onChange={(v) => updateDraftOption("access_token", v)}
                    placeholder={secretPlaceholder("access_token", "Gofile Account Token")}
                  />
                </Field>
                <Field label="Root Folder ID（可选）">
                  <Input
                    value={draft.options.root_folder_id || ""}
                    onChange={(v) => updateDraftOption("root_folder_id", v)}
                    placeholder="根目录 ID"
                  />
                </Field>
                <Field label="Account ID（可选）">
                  <Input
                    value={draft.options.account_id || ""}
                    onChange={(v) => updateDraftOption("account_id", v)}
                    placeholder="可选"
                  />
                </Field>
              </div>
            )}

            {draft.type === "pixeldrain" && (
              <div className="form-grid form-grid--three">
                <Field label="API Key" error={fieldErrors.api_key}>
                  <Input
                    mode="password"
                    value={draft.options.api_key || ""}
                    onChange={(v) => updateDraftOption("api_key", v)}
                    placeholder={secretPlaceholder("api_key", "Pixeldrain API Key")}
                  />
                </Field>
                <Field label="根目录 / 列表 ID（root_folder_id）">
                  <Input
                    value={draft.options.root_folder_id || ""}
                    onChange={(v) => updateDraftOption("root_folder_id", v)}
                    placeholder="默认 me（个人文件系统）"
                  />
                </Field>
                <Field label="API 地址（api_url，可选）">
                  <Input
                    value={draft.options.api_url || ""}
                    onChange={(v) => updateDraftOption("api_url", v)}
                    placeholder="默认 https://pixeldrain.com/api"
                  />
                </Field>
              </div>
            )}

            {wrapperTypes.has(draft.type) && (
              <>
                <Field
                  label="底层存储路径"
                  required
                  error={fieldErrors.remote}
                  hint="格式为「存储名:子路径」（如 archive:bucket/data）或飞牛本地授权绝对路径"
                >
                  <Input
                    value={draft.options.remote || ""}
                    onChange={(v) => updateDraftOption("remote", v)}
                    placeholder="archive:bucket/private 或 /vol1/1000/backup"
                  />
                </Field>
                {quickBackingSources.length > 0 && (
                  <div className="native-quick-picks">
                    <span className="muted">快捷填入：</span>
                    {quickBackingSources.map((src) => (
                      <button
                        key={`${src.kind}-${src.value}`}
                        type="button"
                        className="native-quick-chip"
                        onClick={() => updateDraftOption("remote", src.value)}
                      >
                        {src.label}
                      </button>
                    ))}
                  </div>
                )}

                {draft.type === "crypt" && (
                  <>
                    <div className="form-grid form-grid--two">
                      <Field
                        label="加密密码"
                        required={!draft.editing}
                        error={fieldErrors.password}
                      >
                        <Input
                          mode="password"
                          value={draft.options.password || ""}
                          onChange={(v) => updateDraftOption("password", v)}
                          placeholder={secretPlaceholder("password", "输入加密密码")}
                        />
                      </Field>
                      <Field label="加密盐（可选）">
                        <Input
                          mode="password"
                          value={draft.options.password2 || ""}
                          onChange={(v) => updateDraftOption("password2", v)}
                          placeholder={secretPlaceholder(
                            "password2",
                            "可选，建议设置以增强安全性",
                          )}
                        />
                      </Field>
                    </div>
                    {draft.editing &&
                      draft.configuredSecrets.includes("password2") && (
                        <Checkbox
                          checked={draft.clearSecrets.includes("password2")}
                          onChange={(e) =>
                            toggleClearSecret(
                              "password2",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          清除已保存的加密盐
                        </Checkbox>
                      )}
                    <div className="sync-method-banner is-warning">
                      <IconAlertTriangle aria-hidden="true" size="small" />
                      <span>
                        请妥善保管加密密码和盐；修改已有 Crypt 密码会导致此前加密文件无法解密，恢复文件时必须使用完全相同的配置。
                      </span>
                    </div>
                  </>
                )}

                {draft.type === "chunker" && (
                  <div className="form-grid form-grid--two">
                    <Field label="分片大小（chunk_size）" hint="默认 2G，例如 100M / 1G">
                      <Input
                        value={draft.options.chunk_size || ""}
                        onChange={(v) => updateDraftOption("chunk_size", v)}
                        placeholder="2G"
                      />
                    </Field>
                    <Field label="哈希校验方式（hash_type）">
                      <Select
                        value={draft.options.hash_type || "md5"}
                        optionList={[
                          { value: "md5", label: "MD5" },
                          { value: "sha1", label: "SHA1" },
                          { value: "md5quick", label: "MD5 Quick" },
                          { value: "none", label: "不校验 (none)" },
                        ]}
                        onChange={(v) => updateDraftOption("hash_type", String(v))}
                      />
                    </Field>
                  </div>
                )}

                {draft.type === "compress" && (
                  <div className="form-grid form-grid--two">
                    <Field label="压缩算法（mode）">
                      <Select
                        value={draft.options.mode || "gzip"}
                        optionList={[
                          { value: "gzip", label: "Gzip（兼容好）" },
                          { value: "zstd", label: "Zstd（压缩率高/快）" },
                        ]}
                        onChange={(v) => updateDraftOption("mode", String(v))}
                      />
                    </Field>
                    <Field label="压缩级别（level）" hint="留空使用默认级别">
                      <Input
                        inputMode="numeric"
                        value={draft.options.level || ""}
                        onChange={(v) => updateDraftOption("level", v)}
                        placeholder="如 6"
                      />
                    </Field>
                  </div>
                )}

                {draft.type === "hasher" && (
                  <div className="form-grid form-grid--two">
                    <Field label="缓存哈希算法（hashes）">
                      <Input
                        value={draft.options.hashes || ""}
                        onChange={(v) => updateDraftOption("hashes", v)}
                        placeholder="如 md5,sha1"
                      />
                    </Field>
                    <Field label="缓存有效期（max_age）">
                      <Input
                        value={draft.options.max_age || ""}
                        onChange={(v) => updateDraftOption("max_age", v)}
                        placeholder="如 24h 或 off"
                      />
                    </Field>
                  </div>
                )}

                {draft.type === "archive" && (
                  <div className="sync-method-banner">
                    <IconInfoCircle aria-hidden="true" size="small" />
                    <span>
                      Archive 支持将压缩包（ZIP / TAR / 7z 等）挂载为只读虚拟目录进行浏览、校验和作为同步源，不支持写入。
                    </span>
                  </div>
                )}
              </>
            )}

            {draft.type === "combine" && (
              <>
                <Field
                  label="目录映射列表（upstreams）"
                  required
                  error={fieldErrors.upstreams}
                  hint="以空格分隔「目录名=存储名:路径」，将多个远端或本地目录挂载到同一虚拟根目录下，例如 photos=nas:/photos archive=s3:bucket"
                >
                  <TextArea
                    autosize={{ minRows: 2, maxRows: 4 }}
                    value={draft.options.upstreams || ""}
                    onChange={(v) => updateDraftOption("upstreams", v)}
                    placeholder="photos=remote1:photos docs=remote2:docs"
                  />
                </Field>
                {quickBackingSources.length > 0 && (
                  <div className="native-quick-picks">
                    <span className="muted">快捷追加映射：</span>
                    {quickBackingSources.map((src) => {
                      const rawBase =
                        src.kind === "local"
                          ? src.value.split("/").filter(Boolean).pop() || "local"
                          : src.value.replace(/:$/, "");
                      const cleanDir = rawBase.replace(/[^\p{L}\p{N}_-]/gu, "") || "dir";
                      return (
                        <button
                          key={`combine-${src.kind}-${src.value}`}
                          type="button"
                          className="native-quick-chip"
                          onClick={() => {
                            updateDraftOption(
                              "upstreams",
                              appendCombineUpstream(draft.options.upstreams || "", src),
                            );
                          }}
                        >
                          + {cleanDir}={src.label}
                        </button>
                      );
                    })}
                  </div>
                )}
              </>
            )}

            {draft.type === "union" && (
              <>
                <Field
                  label="上游存储列表（upstreams）"
                  required
                  error={fieldErrors.upstreams}
                  hint="多个上游以空格分隔，可带 :ro（只读）或 :nc（禁新建）后缀，如 remote1:bucket remote2:bucket:ro"
                >
                  <TextArea
                    autosize={{ minRows: 2, maxRows: 4 }}
                    value={draft.options.upstreams || ""}
                    onChange={(v) => updateDraftOption("upstreams", v)}
                    placeholder="archive1:data archive2:data:ro"
                  />
                </Field>
                {quickBackingSources.length > 0 && (
                  <div className="native-quick-picks">
                    <span className="muted">追加上游：</span>
                    {quickBackingSources.map((src) => (
                      <button
                        key={`${src.kind}-${src.value}`}
                        type="button"
                        className="native-quick-chip"
                        onClick={() => {
                          const cur = (draft.options.upstreams || "").trim();
                          const tok = src.value.includes(" ") ? `"${src.value}"` : src.value;
                          updateDraftOption("upstreams", cur ? `${cur} ${tok}` : tok);
                        }}
                      >
                        + {src.label}
                      </button>
                    ))}
                  </div>
                )}
                <div className="form-grid form-grid--three">
                  <Field label="写入策略（create_policy）">
                    <Select
                      value={draft.options.create_policy || "epmfs"}
                      optionList={[
                        { value: "epmfs", label: "epmfs（剩余最多）" },
                        { value: "mfs", label: "mfs（最大剩余）" },
                        { value: "ff", label: "ff（首个可用）" },
                        { value: "all", label: "all（写入全部）" },
                        { value: "rand", label: "rand（随机）" },
                      ]}
                      onChange={(v) => updateDraftOption("create_policy", String(v))}
                    />
                  </Field>
                  <Field label="操作策略（action_policy）">
                    <Select
                      value={draft.options.action_policy || "epall"}
                      optionList={[
                        { value: "epall", label: "epall（全部匹配）" },
                        { value: "all", label: "all（全部上游）" },
                        { value: "ff", label: "ff（首个找到）" },
                      ]}
                      onChange={(v) => updateDraftOption("action_policy", String(v))}
                    />
                  </Field>
                  <Field label="搜索策略（search_policy）">
                    <Select
                      value={draft.options.search_policy || "ff"}
                      optionList={[
                        { value: "ff", label: "ff（首个找到）" },
                        { value: "all", label: "all（合并全部）" },
                        { value: "newest", label: "newest（最新修改）" },
                      ]}
                      onChange={(v) => updateDraftOption("search_policy", String(v))}
                    />
                  </Field>
                </div>
              </>
            )}

            {/* Collapsible Advanced & Network Section */}
            <div className="advanced-options">
              <button
                type="button"
                className={`advanced-options-toggle${advancedOpen ? " is-open" : ""}`}
                aria-expanded={advancedOpen}
                onClick={() => setAdvancedOpen((o) => !o)}
              >
                <IconChevronRight
                  className="advanced-options-chevron"
                  aria-hidden="true"
                />
                <span>高级与网络选项</span>
              </button>
              {advancedOpen && (
                <div className="advanced-options-body">
                  {draft.type === "s3" && (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field label="存储类别（storage_class）">
                          <Input
                            value={draft.options.storage_class || ""}
                            onChange={(v) => updateDraftOption("storage_class", v)}
                            placeholder="如 STANDARD / IA / GLACIER"
                          />
                        </Field>
                        <Field label="访问控制（acl）">
                          <Input
                            value={draft.options.acl || ""}
                            onChange={(v) => updateDraftOption("acl", v)}
                            placeholder="如 private / public-read"
                          />
                        </Field>
                      </div>
                      <div className="form-grid form-grid--three">
                        <Field label="分片大小（chunk_size）">
                          <Input
                            value={draft.options.chunk_size || ""}
                            onChange={(v) => updateDraftOption("chunk_size", v)}
                            placeholder="默认 5M"
                          />
                        </Field>
                        <Field label="分片阈值（upload_cutoff）">
                          <Input
                            value={draft.options.upload_cutoff || ""}
                            onChange={(v) => updateDraftOption("upload_cutoff", v)}
                            placeholder="默认 200M"
                          />
                        </Field>
                        <Field label="分片并发（upload_concurrency）">
                          <Input
                            inputMode="numeric"
                            value={draft.options.upload_concurrency || ""}
                            onChange={(v) => updateDraftOption("upload_concurrency", v)}
                            placeholder="默认 4"
                          />
                        </Field>
                      </div>
                      <div className="form-grid form-grid--two">
                        <Field label="服务端加密（server_side_encryption）">
                          <Input
                            value={draft.options.server_side_encryption || ""}
                            onChange={(v) =>
                              updateDraftOption("server_side_encryption", v)
                            }
                            placeholder="如 AES256 / aws:kms"
                          />
                        </Field>
                        <Field label="KMS Key ID（可选）">
                          <Input
                            value={draft.options.sse_kms_key_id || ""}
                            onChange={(v) => updateDraftOption("sse_kms_key_id", v)}
                            placeholder="SSE-KMS 密钥 ID"
                          />
                        </Field>
                      </div>
                      <div className="form-grid form-grid--three">
                        <Field label="STS 临时会话令牌（session_token）">
                          <Input
                            mode="password"
                            value={draft.options.session_token || ""}
                            disabled={draft.authMode !== "keys"}
                            onChange={(v) => updateDraftOption("session_token", v)}
                            placeholder={secretPlaceholder("session_token", "可选")}
                          />
                        </Field>
                        <Field label="列举接口版本（list_version）">
                          <Select
                            value={draft.options.list_version || ""}
                            optionList={[
                              { value: "", label: "默认 (v2)" },
                              { value: "1", label: "v1（老旧兼容）" },
                              { value: "2", label: "v2" },
                            ]}
                            onChange={(v) => updateDraftOption("list_version", String(v || ""))}
                          />
                        </Field>
                        <Field label="上传校验算法（upload_checksum_algorithm）">
                          <Input
                            value={draft.options.upload_checksum_algorithm || ""}
                            onChange={(v) =>
                              updateDraftOption("upload_checksum_algorithm", v)
                            }
                            placeholder="如 CRC32 / SHA256"
                          />
                        </Field>
                      </div>
                      <div className="native-checkbox-group">
                        <Checkbox
                          checked={isBoolTrue(draft.options, "disable_http2")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "disable_http2",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          禁用 HTTP/2（部分自建对象存储兼容性问题时开启）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "v2_auth")}
                          onChange={(e) =>
                            toggleDraftBoolOption("v2_auth", Boolean(e.target.checked))
                          }
                        >
                          使用 v2 签名认证（老旧 S3 兼容服务）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "directory_markers")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "directory_markers",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          上传空目录时创建目录标记对象（directory_markers）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "leave_parts_on_error")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "leave_parts_on_error",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          失败时保留未完成分片（leave_parts_on_error，仅诊断用；跨进程断点续传请开启任务中的「断点续传」开关，此选项可能产生对象存储残留碎片）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "no_head")}
                          onChange={(e) =>
                            toggleDraftBoolOption("no_head", Boolean(e.target.checked))
                          }
                        >
                          跳过上传后 HEAD 校验（no_head）
                        </Checkbox>
                      </div>
                    </>
                  )}

                  {draft.type === "webdav" && (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field label="Bearer Token（可选）">
                          <Input
                            mode="password"
                            value={draft.options.bearer_token || ""}
                            onChange={(v) => updateDraftOption("bearer_token", v)}
                            placeholder={secretPlaceholder("bearer_token", "可选")}
                          />
                        </Field>
                        <Field label="最小请求间隔（pacer_min_sleep）">
                          <Input
                            value={draft.options.pacer_min_sleep || ""}
                            onChange={(v) => updateDraftOption("pacer_min_sleep", v)}
                            placeholder="如 10ms"
                          />
                        </Field>
                      </div>
                      <div className="form-grid form-grid--three">
                        <Field label="Nextcloud 分块大小（nextcloud_chunk_size）">
                          <Input
                            value={draft.options.nextcloud_chunk_size || ""}
                            onChange={(v) =>
                              updateDraftOption("nextcloud_chunk_size", v)
                            }
                            placeholder="如 10M"
                          />
                        </Field>
                        <Field label="自定义请求头（headers）">
                          <Input
                            value={draft.options.headers || ""}
                            onChange={(v) => updateDraftOption("headers", v)}
                            placeholder="Key,Value,Key2,Value2"
                          />
                        </Field>
                        <Field label="文件名编码（encoding）">
                          <Input
                            value={draft.options.encoding || ""}
                            onChange={(v) => updateDraftOption("encoding", v)}
                            placeholder="留空默认"
                          />
                        </Field>
                      </div>
                    </>
                  )}

                  {draft.type === "quark" && (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field
                          label="上传文件并发上限（upload_concurrency）"
                          error={fieldErrors.upload_concurrency}
                          hint="夸克 OSS 要求单文件分片顺序上传，此项控制多文件同时直传的并发上限（1–8，默认 4）"
                        >
                          <Input
                            inputMode="numeric"
                            value={draft.options.upload_concurrency || ""}
                            onChange={(v) =>
                              updateDraftOption("upload_concurrency", v)
                            }
                            placeholder="默认 4（1–8）"
                          />
                        </Field>
                        <Field
                          label="接口限流速率（tps_limit）"
                          error={fieldErrors.tps_limit}
                          hint="夸克元数据接口每秒最大请求数，默认 3.5（建议不超过 5）"
                        >
                          <Input
                            value={draft.options.tps_limit || ""}
                            onChange={(v) => updateDraftOption("tps_limit", v)}
                            placeholder="默认 3.5"
                          />
                        </Field>
                      </div>
                      <div className="form-grid form-grid--two">
                        <Field
                          label="分片大小上限（part_size，可选）"
                          hint="留空自动使用夸克服务端建议的分片大小"
                        >
                          <Input
                            value={draft.options.part_size || ""}
                            onChange={(v) => updateDraftOption("part_size", v)}
                            placeholder="例如 10M"
                          />
                        </Field>
                        <Field
                          label="自定义 User-Agent（可选）"
                          hint="留空使用内置夸克桌面客户端 UA"
                        >
                          <Input
                            value={draft.options.user_agent || ""}
                            onChange={(v) => updateDraftOption("user_agent", v)}
                            placeholder="留空默认"
                          />
                        </Field>
                      </div>
                    </>
                  )}

                  {draft.type === "pan123" && (() => {
                    const pan123Protocol =
                      (draft.options.protocol || "").trim().toLowerCase() === "web"
                        ? "web"
                        : "open_api";
                    return (
                      <>
                        <div className="form-grid form-grid--two">
                          <Field
                            label="分片上传并发数（upload_concurrency）"
                            error={fieldErrors.upload_concurrency}
                            hint="单文件大文件同时上传的分片并发数（1–8，默认 3）"
                          >
                            <Input
                              inputMode="numeric"
                              value={draft.options.upload_concurrency || ""}
                              onChange={(v) =>
                                updateDraftOption("upload_concurrency", v)
                              }
                              placeholder="默认 3（1–8）"
                            />
                          </Field>
                          <Field
                            label="接口限流速率（tps_limit，可选）"
                            error={fieldErrors.tps_limit}
                            hint="留空自动按 123云盘各接口安全 QPS 限流；可手动设为 1–20"
                          >
                            <Input
                              value={draft.options.tps_limit || ""}
                              onChange={(v) => updateDraftOption("tps_limit", v)}
                              placeholder="留空按接口自动限流"
                            />
                          </Field>
                        </div>

                        {pan123Protocol === "open_api" ? (
                          <>
                            <Checkbox
                              checked={draft.options.use_direct_link === "true"}
                              onChange={(e) =>
                                updateDraftOption(
                                  "use_direct_link",
                                  e.target.checked ? "true" : "",
                                )
                              }
                            >
                              优先使用开放平台直链下载（use_direct_link，需开通直链空间权益）
                            </Checkbox>
                            {draft.options.use_direct_link === "true" && (
                              <div className="form-grid form-grid--two">
                                <Field
                                  label="直链 URL 鉴权私钥（direct_link_key，可选）"
                                  hint="若直链空间开启了 URL 鉴权请填写私钥"
                                >
                                  <Input
                                    mode="password"
                                    value={draft.options.direct_link_key || ""}
                                    onChange={(v) =>
                                      updateDraftOption("direct_link_key", v)
                                    }
                                    placeholder={secretPlaceholder(
                                      "direct_link_key",
                                      "留空不签名",
                                    )}
                                  />
                                </Field>
                                <Field
                                  label="直链签名有效期（分钟）"
                                  error={fieldErrors.direct_link_ttl}
                                  hint="默认 30 分钟（1–10080）"
                                >
                                  <Input
                                    inputMode="numeric"
                                    value={draft.options.direct_link_ttl || ""}
                                    onChange={(v) =>
                                      updateDraftOption("direct_link_ttl", v)
                                    }
                                    placeholder="默认 30"
                                  />
                                </Field>
                              </div>
                            )}
                          </>
                        ) : (
                          <Field
                            label="Web 协议平台头（platform，可选）"
                            hint="留空默认 web"
                          >
                            <Input
                              value={draft.options.platform || ""}
                              onChange={(v) => updateDraftOption("platform", v)}
                              placeholder="默认 web"
                            />
                          </Field>
                        )}

                        <Field
                          label="自定义 User-Agent（可选）"
                          hint="留空使用内置桌面浏览器 UA"
                        >
                          <Input
                            value={draft.options.user_agent || ""}
                            onChange={(v) => updateDraftOption("user_agent", v)}
                            placeholder="留空默认"
                          />
                        </Field>
                      </>
                    );
                  })()}

                  {draft.type === "sftp" && (
                    <>
                      <div className="form-grid form-grid--three">
                        <Field label="空闲超时（idle_timeout）">
                          <Input
                            value={draft.options.idle_timeout || ""}
                            onChange={(v) => updateDraftOption("idle_timeout", v)}
                            placeholder="如 1m"
                          />
                        </Field>
                        <Field label="并发流数（concurrency）">
                          <Input
                            inputMode="numeric"
                            value={draft.options.concurrency || ""}
                            onChange={(v) => updateDraftOption("concurrency", v)}
                            placeholder="默认 64"
                          />
                        </Field>
                        <Field label="读写包大小（chunk_size）">
                          <Input
                            value={draft.options.chunk_size || ""}
                            onChange={(v) => updateDraftOption("chunk_size", v)}
                            placeholder="默认 32k"
                          />
                        </Field>
                      </div>
                      <div className="native-checkbox-group">
                        <Checkbox
                          checked={isBoolTrue(draft.options, "allow_hashcheck")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "allow_hashcheck",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          允许在 SFTP 服务端执行远端哈希命令（需要 SSH shell 权限；默认关闭以加固安全）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "use_insecure_cipher")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "use_insecure_cipher",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          兼容老旧 SSH 设备的弱加密套件（use_insecure_cipher）
                        </Checkbox>
                      </div>
                    </>
                  )}

                  {draft.type === "smb" && (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field label="Kerberos SPN（可选）">
                          <Input
                            value={draft.options.spn || ""}
                            onChange={(v) => updateDraftOption("spn", v)}
                            placeholder="cifs/server.example.com"
                          />
                        </Field>
                        <Field label="空闲超时（idle_timeout）">
                          <Input
                            value={draft.options.idle_timeout || ""}
                            onChange={(v) => updateDraftOption("idle_timeout", v)}
                            placeholder="如 1m"
                          />
                        </Field>
                      </div>
                      <div className="native-checkbox-group">
                        <Checkbox
                          checked={isBoolTrue(draft.options, "hide_special_share")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "hide_special_share",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          隐藏特殊共享（如 print$ / IPC$）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "case_insensitive")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "case_insensitive",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          路径不区分大小写（case_insensitive）
                        </Checkbox>
                      </div>
                    </>
                  )}

                  {draft.type === "ftp" && (
                    <>
                      <div className="form-grid form-grid--three">
                        <Field label="并发连接数（concurrency）">
                          <Input
                            inputMode="numeric"
                            value={draft.options.concurrency || ""}
                            onChange={(v) => updateDraftOption("concurrency", v)}
                            placeholder="0 为不限制"
                          />
                        </Field>
                        <Field label="空闲超时（idle_timeout）">
                          <Input
                            value={draft.options.idle_timeout || ""}
                            onChange={(v) => updateDraftOption("idle_timeout", v)}
                            placeholder="如 1m"
                          />
                        </Field>
                        <Field label="关闭连接超时（shut_timeout）">
                          <Input
                            value={draft.options.shut_timeout || ""}
                            onChange={(v) => updateDraftOption("shut_timeout", v)}
                            placeholder="如 1m"
                          />
                        </Field>
                      </div>
                      <div className="native-checkbox-group">
                        <Checkbox
                          checked={isBoolTrue(draft.options, "disable_tls13")}
                          onChange={(e) =>
                            toggleDraftBoolOption("disable_tls13", Boolean(e.target.checked))
                          }
                        >
                          禁用 TLS 1.3（兼容部分老旧 FTPS 服务器）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "writing_mdtm")}
                          onChange={(e) =>
                            toggleDraftBoolOption("writing_mdtm", Boolean(e.target.checked))
                          }
                        >
                          使用 MDTM 同步写入文件修改时间
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "force_list_hidden")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "force_list_hidden",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          强制使用 LIST -a 列举隐藏文件（force_list_hidden）
                        </Checkbox>
                      </div>
                    </>
                  )}

                  {draft.type === "http" && (
                    <>
                      <Field label="自定义 HTTP 请求头（headers）">
                        <Input
                          value={draft.options.headers || ""}
                          onChange={(v) => updateDraftOption("headers", v)}
                          placeholder="Authorization,Bearer xxx"
                        />
                      </Field>
                      <div className="native-checkbox-group">
                        <Checkbox
                          checked={isBoolTrue(draft.options, "no_head")}
                          onChange={(e) =>
                            toggleDraftBoolOption("no_head", Boolean(e.target.checked))
                          }
                        >
                          跳过 HEAD 探测请求（no_head）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "no_slash")}
                          onChange={(e) =>
                            toggleDraftBoolOption("no_slash", Boolean(e.target.checked))
                          }
                        >
                          URL 指向单文件而非目录时关闭尾部斜杠（no_slash）
                        </Checkbox>
                      </div>
                    </>
                  )}

                  {draft.type === "b2" && (
                    <div className="form-grid form-grid--three">
                      <Field label="分片大小（chunk_size）">
                        <Input
                          value={draft.options.chunk_size || ""}
                          onChange={(v) => updateDraftOption("chunk_size", v)}
                          placeholder="默认 96M"
                        />
                      </Field>
                      <Field label="大文件分片阈值（upload_cutoff）">
                        <Input
                          value={draft.options.upload_cutoff || ""}
                          onChange={(v) => updateDraftOption("upload_cutoff", v)}
                          placeholder="默认 200M"
                        />
                      </Field>
                      <Field label="自定义下载域名（download_url）">
                        <Input
                          value={draft.options.download_url || ""}
                          onChange={(v) => updateDraftOption("download_url", v)}
                          placeholder="如 https://cdn.example.com"
                        />
                      </Field>
                    </div>
                  )}

                  {draft.type === "onedrive" && (
                    <>
                      <div className="form-grid form-grid--three">
                        <Field label="组织租户 ID（tenant，可选）">
                          <Input
                            value={draft.options.tenant || ""}
                            onChange={(v) => updateDraftOption("tenant", v)}
                            placeholder="企业版多租户可选填写"
                          />
                        </Field>
                        <Field label="分片上传大小（chunk_size）">
                          <Input
                            value={draft.options.chunk_size || ""}
                            onChange={(v) => updateDraftOption("chunk_size", v)}
                            placeholder="默认 10M（320k 倍数）"
                          />
                        </Field>
                        <Field label="自定义 Auth URL（可选）">
                          <Input
                            value={draft.options.auth_url || ""}
                            onChange={(v) => updateDraftOption("auth_url", v)}
                            placeholder="可选"
                          />
                        </Field>
                      </div>
                      <div className="native-checkbox-group">
                        <Checkbox
                          checked={isBoolTrue(draft.options, "expose_onenote_files")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "expose_onenote_files",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          列出 OneNote 笔记本文件（只读）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "server_side_across_configs")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "server_side_across_configs",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          允许跨配置服务端直拷（server_side_across_configs）
                        </Checkbox>
                      </div>
                    </>
                  )}

                  {draft.type === "drive" && (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field label="分片大小（chunk_size）">
                          <Input
                            value={draft.options.chunk_size || ""}
                            onChange={(v) => updateDraftOption("chunk_size", v)}
                            placeholder="默认 8M"
                          />
                        </Field>
                        <Field label="分片阈值（upload_cutoff）">
                          <Input
                            value={draft.options.upload_cutoff || ""}
                            onChange={(v) => updateDraftOption("upload_cutoff", v)}
                            placeholder="默认 8M"
                          />
                        </Field>
                      </div>
                      <div className="native-checkbox-group">
                        <Checkbox
                          checked={isBoolTrue(draft.options, "shared_with_me")}
                          onChange={(e) =>
                            toggleDraftBoolOption("shared_with_me", Boolean(e.target.checked))
                          }
                        >
                          仅访问“与我共享”的文件（shared_with_me）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "trashed_only")}
                          onChange={(e) =>
                            toggleDraftBoolOption("trashed_only", Boolean(e.target.checked))
                          }
                        >
                          仅访问回收站内容（trashed_only）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "server_side_across_configs")}
                          onChange={(e) =>
                            toggleDraftBoolOption(
                              "server_side_across_configs",
                              Boolean(e.target.checked),
                            )
                          }
                        >
                          允许跨配置服务端复制（server_side_across_configs）
                        </Checkbox>
                      </div>
                    </>
                  )}

                  {draft.type === "dropbox" && (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field label="分片大小（chunk_size）">
                          <Input
                            value={draft.options.chunk_size || ""}
                            onChange={(v) => updateDraftOption("chunk_size", v)}
                            placeholder="默认 48M"
                          />
                        </Field>
                        <Field label="模拟团队成员（impersonate，可选）">
                          <Input
                            value={draft.options.impersonate || ""}
                            onChange={(v) => updateDraftOption("impersonate", v)}
                            placeholder="团队管理员代操作成员邮箱"
                          />
                        </Field>
                      </div>
                      <div className="native-checkbox-group">
                        <Checkbox
                          checked={isBoolTrue(draft.options, "shared_folders")}
                          onChange={(e) =>
                            toggleDraftBoolOption("shared_folders", Boolean(e.target.checked))
                          }
                        >
                          访问共享文件夹（shared_folders）
                        </Checkbox>
                        <Checkbox
                          checked={isBoolTrue(draft.options, "shared_files")}
                          onChange={(e) =>
                            toggleDraftBoolOption("shared_files", Boolean(e.target.checked))
                          }
                        >
                          访问共享文件（shared_files）
                        </Checkbox>
                      </div>
                    </>
                  )}

                  {draft.type === "mega" && (
                    <div className="native-checkbox-group">
                      <Checkbox
                        checked={isBoolTrue(draft.options, "hard_delete")}
                        onChange={(e) =>
                          toggleDraftBoolOption("hard_delete", Boolean(e.target.checked))
                        }
                      >
                        永久删除而非移入回收站（hard_delete）
                      </Checkbox>
                      <Checkbox
                        checked={isBoolTrue(draft.options, "use_https")}
                        onChange={(e) =>
                          toggleDraftBoolOption("use_https", Boolean(e.target.checked))
                        }
                      >
                        强制使用 HTTPS 传输文件流（use_https）
                      </Checkbox>
                    </div>
                  )}

                  {draft.type === "azureblob" && (
                    <>
                      <div className="form-grid form-grid--three">
                        <Field label="归档访问层（archive_tier）">
                          <Input
                            value={draft.options.archive_tier || ""}
                            onChange={(v) => updateDraftOption("archive_tier", v)}
                            placeholder="如 Archive"
                          />
                        </Field>
                        <Field label="分片大小（chunk_size）">
                          <Input
                            value={draft.options.chunk_size || ""}
                            onChange={(v) => updateDraftOption("chunk_size", v)}
                            placeholder="默认 4M"
                          />
                        </Field>
                        <Field label="分片阈值（upload_cutoff）">
                          <Input
                            value={draft.options.upload_cutoff || ""}
                            onChange={(v) => updateDraftOption("upload_cutoff", v)}
                            placeholder="默认 256M"
                          />
                        </Field>
                      </div>
                      <Checkbox
                        checked={isBoolTrue(draft.options, "no_check_container")}
                        onChange={(e) =>
                          toggleDraftBoolOption(
                            "no_check_container",
                            Boolean(e.target.checked),
                          )
                        }
                      >
                        不尝试创建或检查容器存在性（no_check_container）
                      </Checkbox>
                    </>
                  )}

                  {draft.type === "azurefiles" && (
                    <>
                      <div className="form-grid form-grid--three">
                        <Field label="Tenant ID（可选）">
                          <Input
                            value={draft.options.tenant || ""}
                            onChange={(v) => updateDraftOption("tenant", v)}
                            placeholder="Azure Tenant ID"
                          />
                        </Field>
                        <Field label="Client ID（可选）">
                          <Input
                            value={draft.options.client_id || ""}
                            onChange={(v) => updateDraftOption("client_id", v)}
                            placeholder="Client ID"
                          />
                        </Field>
                        <Field label="Client Secret（可选）">
                          <Input
                            mode="password"
                            value={draft.options.client_secret || ""}
                            onChange={(v) => updateDraftOption("client_secret", v)}
                            placeholder={secretPlaceholder("client_secret", "Optional")}
                          />
                        </Field>
                      </div>
                      <div className="form-grid form-grid--two">
                        <Field label="分片大小（chunk_size）">
                          <Input
                            value={draft.options.chunk_size || ""}
                            onChange={(v) => updateDraftOption("chunk_size", v)}
                            placeholder="默认 4M"
                          />
                        </Field>
                        <Field label="并发数（upload_concurrency）">
                          <Input
                            inputMode="numeric"
                            value={draft.options.upload_concurrency || ""}
                            onChange={(v) => updateDraftOption("upload_concurrency", v)}
                            placeholder="默认 16"
                          />
                        </Field>
                      </div>
                    </>
                  )}

                  {draft.type === "swift" && (
                    <div className="form-grid form-grid--two">
                      <Field label="身份认证版本（auth_version）">
                        <Select
                          value={draft.options.auth_version || ""}
                          optionList={[
                            { value: "", label: "自动探测" },
                            { value: "1", label: "v1" },
                            { value: "2", label: "v2" },
                            { value: "3", label: "v3" },
                          ]}
                          onChange={(v) => updateDraftOption("auth_version", String(v || ""))}
                        />
                      </Field>
                      <Field label="分段阈值（chunk_size）">
                        <Input
                          value={draft.options.chunk_size || ""}
                          onChange={(v) => updateDraftOption("chunk_size", v)}
                          placeholder="默认 5G"
                        />
                      </Field>
                    </div>
                  )}

                  {(draft.type === "oracleobjectstorage" || draft.type === "qingstor") && (
                    <div className="form-grid form-grid--three">
                      <Field label="分片大小（chunk_size）">
                        <Input
                          value={draft.options.chunk_size || ""}
                          onChange={(v) => updateDraftOption("chunk_size", v)}
                          placeholder="可选"
                        />
                      </Field>
                      <Field label="分片阈值（upload_cutoff）">
                        <Input
                          value={draft.options.upload_cutoff || ""}
                          onChange={(v) => updateDraftOption("upload_cutoff", v)}
                          placeholder="可选"
                        />
                      </Field>
                      <Field label="上传并发（upload_concurrency）">
                        <Input
                          inputMode="numeric"
                          value={draft.options.upload_concurrency || ""}
                          onChange={(v) => updateDraftOption("upload_concurrency", v)}
                          placeholder="可选"
                        />
                      </Field>
                    </div>
                  )}

                  {draft.type === "pikpak" && (
                    <div className="native-checkbox-group">
                      <Checkbox
                        checked={isBoolTrue(draft.options, "use_trash")}
                        onChange={(e) =>
                          toggleDraftBoolOption("use_trash", Boolean(e.target.checked))
                        }
                      >
                        删除时移至 PikPak 回收站（use_trash）
                      </Checkbox>
                      <Checkbox
                        checked={isBoolTrue(draft.options, "trashed_only")}
                        onChange={(e) =>
                          toggleDraftBoolOption("trashed_only", Boolean(e.target.checked))
                        }
                      >
                        仅列出回收站文件（trashed_only）
                      </Checkbox>
                    </div>
                  )}

                  {draft.type === "filen" && (
                    <div className="form-grid form-grid--two">
                      <Field label="Master Keys（可选，加速免交互认证）">
                        <Input
                          mode="password"
                          value={draft.options.master_keys || ""}
                          onChange={(v) => updateDraftOption("master_keys", v)}
                          placeholder={secretPlaceholder("master_keys", "可选")}
                        />
                      </Field>
                      <Field label="认证版本（auth_version）">
                        <Input
                          value={draft.options.auth_version || ""}
                          onChange={(v) => updateDraftOption("auth_version", v)}
                          placeholder="可选"
                        />
                      </Field>
                    </div>
                  )}

                  {draft.type === "chunker" && (
                    <div className="form-grid form-grid--two">
                      <Field label="分片命名格式（name_format）">
                        <Input
                          value={draft.options.name_format || ""}
                          onChange={(v) => updateDraftOption("name_format", v)}
                          placeholder="*.rclone_chunk.###"
                        />
                      </Field>
                      <Field label="元数据格式（meta_format）">
                        <Select
                          value={draft.options.meta_format || "simplejson"}
                          optionList={[
                            { value: "simplejson", label: "simplejson（默认）" },
                            { value: "none", label: "none（无元数据文件）" },
                          ]}
                          onChange={(v) => updateDraftOption("meta_format", String(v))}
                        />
                      </Field>
                    </div>
                  )}

                  {draft.type === "compress" && (
                    <Field label="内存缓存上限（ram_cache_limit）">
                      <Input
                        value={draft.options.ram_cache_limit || ""}
                        onChange={(v) => updateDraftOption("ram_cache_limit", v)}
                        placeholder="默认 20M"
                      />
                    </Field>
                  )}

                  {draft.type === "hasher" && (
                    <Field label="最大自动校验文件大小（auto_size）">
                      <Input
                        value={draft.options.auto_size || ""}
                        onChange={(v) => updateDraftOption("auto_size", v)}
                        placeholder="如 10M（0 禁用写入后自动补哈希）"
                      />
                    </Field>
                  )}

                  {draft.type === "union" && (
                    <div className="form-grid form-grid--two">
                      <Field label="可用空间缓存时长（cache_time）">
                        <Input
                          value={draft.options.cache_time || ""}
                          onChange={(v) => updateDraftOption("cache_time", v)}
                          placeholder="默认 120（秒）"
                        />
                      </Field>
                      <Field label="最小预留剩余空间（min_free_space）">
                        <Input
                          value={draft.options.min_free_space || ""}
                          onChange={(v) => updateDraftOption("min_free_space", v)}
                          placeholder="默认 1G"
                        />
                      </Field>
                    </div>
                  )}

                  {draft.type === "crypt" && (
                    <>
                      <div className="form-grid form-grid--two">
                        <Field label="文件名加密方式（filename_encryption）">
                          <Select
                            value={draft.options.filename_encryption || "standard"}
                            optionList={[
                              { value: "standard", label: "standard（AES 加密，默认）" },
                              { value: "obfuscate", label: "obfuscate（轻量混淆）" },
                              { value: "off", label: "off（不加密文件名）" },
                            ]}
                            onChange={(v) =>
                              updateDraftOption("filename_encryption", String(v))
                            }
                          />
                        </Field>
                        <Field label="文件名编码（filename_encoding）">
                          <Select
                            value={draft.options.filename_encoding || "base32"}
                            optionList={[
                              { value: "base32", label: "base32（默认，通用）" },
                              { value: "base64", label: "base64（更短文件名）" },
                              { value: "base32768", label: "base32768（最短，需 Unicode 支持）" },
                            ]}
                            onChange={(v) =>
                              updateDraftOption("filename_encoding", String(v))
                            }
                          />
                        </Field>
                      </div>
                      <div className="form-grid form-grid--two">
                        <Field label="密文后缀（suffix，可选）">
                          <Input
                            value={draft.options.suffix || ""}
                            onChange={(v) => updateDraftOption("suffix", v)}
                            placeholder="默认 .bin（仅非加密文件名模式生效）"
                          />
                        </Field>
                        <Field label="目录名加密（directory_name_encryption）">
                          <Select
                            value={draft.options.directory_name_encryption || "true"}
                            optionList={[
                              { value: "true", label: "开启（默认）" },
                              { value: "false", label: "关闭" },
                            ]}
                            onChange={(v) =>
                              updateDraftOption("directory_name_encryption", String(v))
                            }
                          />
                        </Field>
                      </div>
                      <Checkbox
                        checked={isBoolTrue(draft.options, "no_data_encryption")}
                        onChange={(e) =>
                          toggleDraftBoolOption(
                            "no_data_encryption",
                            Boolean(e.target.checked),
                          )
                        }
                      >
                        仅加密文件名、不加密文件内容（no_data_encryption）
                      </Checkbox>
                    </>
                  )}

                  {proxyCapableTypes.has(draft.type) && (
                    <Field
                      label="存储专属代理（proxy_url，可选）"
                      error={fieldErrors.proxy_url}
                      hint="留空则跟随系统设置中的全局代理"
                    >
                      <Input
                        value={draft.options.proxy_url || ""}
                        onChange={(v) => updateDraftOption("proxy_url", v)}
                        placeholder="如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080"
                      />
                    </Field>
                  )}

                  {tlsSkipCapableTypes.has(draft.type) && (
                    <Checkbox
                      checked={isBoolTrue(draft.options, "insecure_skip_verify")}
                      onChange={(e) =>
                        toggleDraftBoolOption(
                          "insecure_skip_verify",
                          Boolean(e.target.checked),
                        )
                      }
                    >
                      跳过 TLS 证书校验（--no-check-certificate，仅限内网自签名证书）
                    </Checkbox>
                  )}

                  <Field
                    label="单存储最大并发任务数（max_concurrent_jobs，可选）"
                    hint="0 或留空表示不单独限制（1–50）；用于防止网盘 429 限流或单源过载，加密/分片包装存储自动继承底层配额"
                  >
                    <Input
                      inputMode="numeric"
                      value={draft.options.max_concurrent_jobs || ""}
                      onChange={(v) => updateDraftOption("max_concurrent_jobs", v)}
                      placeholder="0（不限制）"
                    />
                  </Field>

                  <Field label="备注说明（description，可选）">
                    <Input
                      value={draft.options.description || ""}
                      onChange={(v) => updateDraftOption("description", v)}
                      placeholder="可选备注"
                    />
                  </Field>
                </div>
              )}
            </div>

            {formTestResult && (
              <div
                className={`native-test-banner ${
                  formTestResult.ok
                    ? formTestResult.warning
                      ? "is-warn"
                      : "is-ok"
                    : "is-error"
                }`}
                role="status"
              >
                {formTestResult.ok ? (
                  <IconTickCircle aria-hidden="true" size="small" />
                ) : (
                  <IconAlertTriangle aria-hidden="true" size="small" />
                )}
                <div className="native-test-banner-body">
                  <strong>
                    {formTestResult.message}（{formTestResult.latencyMs}ms）
                  </strong>
                  {formTestResult.warning && <span>{formTestResult.warning}</span>}
                </div>
              </div>
            )}
          </div>
        ) : view === "export" ? (
          <div className="editor-form">
            <div className="sync-method-banner">
              <IconInfoCircle aria-hidden="true" size="small" />
              <span>
                导出标准 rclone.conf 文件；本地底层目录会导出为当前绝对路径，迁移到其他设备后需调整路径。运行时认证仍需配置环境凭据。建议设置加密密码保护凭据。
              </span>
            </div>

            <Field label="配置文件加密密码（可选）" hint="留空导出明文 rclone.conf；填写密码则调用 rclone 原生加密">
              <Input
                mode="password"
                value={exportPassword}
                onChange={setExportPassword}
                placeholder="留空导出明文，或输入加密密码"
              />
            </Field>

            <div className="native-import-section">
              {builtInDriverRemotes.length > 0 && (
                <div className="native-test-banner" role="note">
                  <IconInfoCircle aria-hidden="true" size="small" />
                  <span>
                    已自动排除 OpenSync 内置驱动存储（{builtInDriverRemotes.map((r) => r.name).join("、")}），外部官方 rclone 暂不支持此类配置。
                  </span>
                </div>
              )}
              <div className="native-section-head">
                <div className="native-section-title">
                  <h3>选择要导出的存储</h3>
                  <Tag size="small" type="light" color="blue">
                    已选 {selectedExports.length} / {exportableRemotes.length}
                  </Tag>
                </div>
                <div className="native-section-actions">
                  <Button
                    size="small"
                    type="tertiary"
                    onClick={() =>
                      setSelectedExports(exportableRemotes.map((r) => r.name))
                    }
                  >
                    全选
                  </Button>
                  <Button
                    size="small"
                    type="tertiary"
                    onClick={() => setSelectedExports([])}
                  >
                    清空
                  </Button>
                </div>
              </div>
              <div className="native-remote-list">
                {exportableRemotes.map((item) => {
                  const checked = selectedExports.includes(item.name);
                  return (
                    <div key={item.name} className="native-import-row">
                      <Checkbox
                        checked={checked}
                        disabled={exporting}
                        onChange={(e) => {
                          const on = Boolean(e.target.checked);
                          setSelectedExports((prev) =>
                            on
                              ? Array.from(new Set([...prev, item.name]))
                              : prev.filter((n) => n !== item.name),
                          );
                        }}
                      />
                      <div className="native-remote-info">
                        <span className="native-remote-head">
                          <strong>{item.name}</strong>
                          <Tag size="small" type="light" color="blue">
                            {remoteTypeLabels[item.type] || item.type}
                          </Tag>
                        </span>
                        {item.summary && (
                          <span className="muted native-remote-summary">
                            {item.summary}
                          </span>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          </div>
        ) : view === "browse" ? (
          <div className="editor-form">
            <div className="native-browse-bar">
              <div className="native-browse-header">
                <Button
                  size="small"
                  type="tertiary"
                  icon={<IconArrowUp aria-hidden="true" />}
                  disabled={!browseParentPath || browsing || restoring}
                  onClick={() => browseParentPath && void loadBrowse(browseParentPath)}
                >
                  上一级
                </Button>
                <span className="native-browse-path mono" title={browsePath}>
                  {browsePath}
                </span>
                <div className="native-browse-header-actions">
                  <Button
                    size="small"
                    type={activeTransferCount > 0 ? "primary" : "tertiary"}
                    theme={activeTransferCount > 0 ? "light" : undefined}
                    onClick={() => setTransfersOpen((v) => !v)}
                  >
                    传输队列
                    {activeTransferCount > 0
                      ? ` (${activeTransferCount})`
                      : transfers.length > 0
                        ? ` (${transfers.length})`
                        : ""}
                  </Button>
                  <Button
                    size="small"
                    type="tertiary"
                    icon={<IconRefresh aria-hidden="true" />}
                    loading={browsing}
                    disabled={restoring}
                    onClick={() => void loadBrowse(browsePath, { fresh: true, full: browseNeedsFullList })}
                  >
                    刷新
                  </Button>
                </div>
              </div>
              {browsePath !== "/" && (
                <div className="native-browse-toolbar">
                  <Button
                    size="small"
                    type="tertiary"
                    icon={<IconPlusStroked aria-hidden="true" />}
                    disabled={browsing || anyBusy}
                    onClick={() => {
                      setMkdirName("");
                      setMkdirOpen(true);
                    }}
                  >
                    新建目录
                  </Button>
                  <input
                    ref={uploadInputRef}
                    type="file"
                    className="sr-only"
                    tabIndex={-1}
                    onChange={(e) =>
                      void handleUploadFileChange(e.target.files?.[0])
                    }
                  />
                  <Button
                    size="small"
                    type="tertiary"
                    icon={<IconUpload aria-hidden="true" />}
                    loading={uploadingFile}
                    disabled={browsing || anyBusy}
                    onClick={() => uploadInputRef.current?.click()}
                  >
                    上传文件
                  </Button>
                  <Button
                    size="small"
                    type="tertiary"
                    disabled={browsing || anyBusy}
                    onClick={() =>
                      trashOpen ? setTrashOpen(false) : void handleLoadTrash()
                    }
                  >
                    {trashOpen ? "返回目录" : "回收站"}
                  </Button>
                  <Button
                    size="small"
                    type="tertiary"
                    icon={<IconBarChartVStroked aria-hidden="true" />}
                    loading={sizing}
                    disabled={browsing || anyBusy}
                    onClick={() => void handleSizeBrowse()}
                  >
                    统计大小
                  </Button>
                </div>
              )}
            </div>

            {browsePath !== "/" && !trashOpen && (
              <div className="native-browse-filter-bar">
                <Input
                  size="small"
                  className="native-browse-search"
                  placeholder="搜索当前目录文件/文件夹…"
                  value={browseSearch}
                  showClear
                  onChange={setBrowseSearch}
                />
                <Select
                  size="small"
                  className="native-browse-sort"
                  value={browseSort}
                  optionList={[
                    { label: "名称 A→Z", value: "name_asc" },
                    { label: "名称 Z→A", value: "name_desc" },
                    { label: "体积从大到小", value: "size_desc" },
                    { label: "修改时间最新", value: "mtime_desc" },
                  ]}
                  onChange={(v) =>
                    setBrowseSort(
                      String(v) as "name_asc" | "name_desc" | "size_desc" | "mtime_desc",
                    )
                  }
                />
                {selectedBrowsePaths.length > 0 && (
                  <div className="native-browse-batch-actions">
                    <Button
                      size="small"
                      type="primary"
                      theme="light"
                      disabled={!state?.localRoots.length}
                      onClick={() => {
                        if (!restoreLocalRoot && state?.localRoots.length) {
                          setRestoreLocalRoot(state.localRoots[0].path);
                        }
                        setBatchRestoreOpen(true);
                      }}
                    >
                      批量恢复 ({selectedBrowsePaths.length})
                    </Button>
                    <Button
                      size="small"
                      type="tertiary"
                      onClick={() => {
                        setBatchMoveDstDir(browsePath);
                        setBatchMoveOpen(true);
                      }}
                    >
                      批量移动 ({selectedBrowsePaths.length})
                    </Button>
                    <Button
                      size="small"
                      type="danger"
                      theme="light"
                      onClick={handleBatchDeleteSelected}
                    >
                      批量删除 ({selectedBrowsePaths.length})
                    </Button>
                  </div>
                )}
              </div>
            )}

            {transfersOpen && (
              <div className="native-panel-section">
                <div className="native-section-head">
                  <div className="native-section-title">
                    <h3>后台文件传输队列</h3>
                    <Tag size="small" type="light" color="blue">
                      共 {transfers.length} 项
                    </Tag>
                  </div>
                  <div className="native-section-actions">
                    <Button
                      size="small"
                      type="tertiary"
                      theme="borderless"
                      onClick={async () => {
                        await api.clearCompletedNativeTransfers();
                        await refreshTransfers();
                      }}
                    >
                      清理已完成
                    </Button>
                    <Button
                      size="small"
                      type="tertiary"
                      theme="borderless"
                      onClick={() => setTransfersOpen(false)}
                    >
                      收起
                    </Button>
                  </div>
                </div>
                {transfers.length === 0 ? (
                  <div className="muted native-transfer-empty">
                    暂无后台传输任务；恢复和批量操作均会自动在此展示实时进度。
                  </div>
                ) : (
                  <div className="native-transfer-list">
                    {transfers.map((t) => (
                      <div key={t.id} className="native-transfer-item">
                        <div className="native-transfer-head">
                          <span className="native-transfer-name" title={t.label}>
                            {t.label}
                          </span>
                          <div className="native-transfer-status">
                            <Tag
                              size="small"
                              type="light"
                              color={
                                t.status === "completed"
                                  ? "green"
                                  : t.status === "failed"
                                    ? "red"
                                    : t.status === "cancelled"
                                      ? "grey"
                                      : "blue"
                              }
                            >
                              {t.status === "running"
                                ? `${Math.round(t.percent || 0)}%`
                                : t.status === "completed"
                                  ? "已完成"
                                  : t.status === "cancelled"
                                    ? "已取消"
                                    : "失败"}
                            </Tag>
                            {t.status === "running" && (
                              <Button
                                size="small"
                                type="danger"
                                theme="borderless"
                                onClick={async () => {
                                  try {
                                    await api.cancelNativeTransfer(t.id);
                                    await refreshTransfers();
                                  } catch (err) {
                                    errorToast(err);
                                  }
                                }}
                              >
                                取消
                              </Button>
                            )}
                          </div>
                        </div>
                        {t.status === "running" && (
                          <Progress
                            percent={Math.round(t.percent || 0)}
                            showInfo={false}
                            stroke="var(--semi-color-primary)"
                          />
                        )}
                        <div className="muted native-transfer-meta">
                          {t.totalBytes > 0 && (
                            <span>
                              {formatSize(t.doneBytes)} / {formatSize(t.totalBytes)}
                            </span>
                          )}
                          {t.speed > 0 && <span>{formatSize(t.speed)}/s</span>}
                          {t.totalItems > 1 && (
                            <span>
                              {t.completedItems} / {t.totalItems} 项
                            </span>
                          )}
                          {t.detail && <span>{t.detail}</span>}
                          {t.error && <span className="inline-error">{t.error}</span>}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}

            {sizeInfo && (
              <InlineNotice
                icon={<IconBarChartVStroked aria-hidden="true" size="small" />}
                message={`目录统计：共 ${sizeInfo.count} 个文件，总大小 ${formatSize(sizeInfo.bytes)}${sizeInfo.sizeless > 0 ? `（另有 ${sizeInfo.sizeless} 个无大小对象）` : ""}`}
              />
            )}

            {browseError && (
              <InlineNotice
                role="alert"
                message={browseError}
                onRetry={() => void loadBrowse(browsePath, { fresh: true, full: browseNeedsFullList })}
              />
            )}

            {trashOpen ? (
              trashLoading ? (
                <div className="native-loading-panel">
                  <Spin />
                </div>
              ) : trashItems.length === 0 ? (
                <div className="sync-method-banner">
                  <IconInfoCircle aria-hidden="true" size="small" />
                  <span>当前目录下未发现 .opensync-trash 回收站历史文件</span>
                </div>
              ) : (
                <div className="native-remote-list native-browse-list">
                  {trashItems.map((item) => (
                    <div key={item.trashPath} className="native-remote-row">
                      <div className="native-remote-info">
                        <span className="native-remote-head">
                          <IconFile aria-hidden="true" size="small" />
                          <span>{item.name}</span>
                          {item.bucketDate && (
                            <Tag size="small" type="light" color="orange">
                              {item.bucketDate}
                            </Tag>
                          )}
                        </span>
                        <span className="muted native-remote-summary" title={item.originalPath}>
                          原路径：{item.originalPath} · {formatSize(item.size)}
                        </span>
                      </div>
                      <div className="native-remote-actions">
                        <Button
                          size="small"
                          type="primary"
                          theme="light"
                          loading={restoringTrashPath === item.trashPath}
                          onClick={() => void handleRestoreTrashItem(item)}
                        >
                          还原到原路径
                        </Button>
                      </div>
                    </div>
                  ))}
                </div>
              )
            ) : browsing && !browsePaging ? (
              <div className="native-loading-panel">
                <Spin />
              </div>
            ) : browseData && filteredBrowseEntries.length === 0 && !browseData.nextCursor ? (
              <div className="sync-method-banner">
                <IconInfoCircle aria-hidden="true" size="small" />
                <span>{browseSearch.trim() ? "没有匹配的文件或目录" : "当前目录为空"}</span>
              </div>
            ) : browseData ? (
              <div className="native-remote-list native-browse-list">
                {filteredBrowseEntries.map((entry) => {
                  const checked = selectedBrowsePaths.includes(entry.path);
                  return (
                  <div key={entry.path} className="native-remote-row">
                    {browsePath !== "/" && (
                      <Checkbox
                        checked={checked}
                        aria-label={`选择 ${entry.name}`}
                        onChange={(e) => {
                          const on = Boolean(e.target.checked);
                          setSelectedBrowsePaths((prev) =>
                            on
                              ? prev.includes(entry.path)
                                ? prev
                                : [...prev, entry.path]
                              : prev.filter((p) => p !== entry.path),
                          );
                        }}
                      />
                    )}
                    <div className="native-remote-info">
                      <span className="native-remote-head">
                        {entry.isDir ? (
                          <button
                            type="button"
                            className="native-browse-link"
                            onClick={() => void loadBrowse(entry.path)}
                          >
                            <IconFolderStroked aria-hidden="true" size="small" />
                            <span>{entry.name}</span>
                          </button>
                        ) : (
                          <span className="native-browse-file">
                            <IconFile aria-hidden="true" size="small" />
                            <span>{entry.name}</span>
                          </span>
                        )}
                      </span>
                      <span className="muted native-remote-summary">
                        {entry.isDir ? "文件夹" : formatSize(entry.size)}
                        {entry.modTime > 0
                          ? ` · ${formatTimestamp(entry.modTime, true)}`
                          : ""}
                      </span>
                    </div>
                    {browsePath !== "/" && (
                      <div className="native-remote-actions">
                        {!entry.isDir && (
                          <Button
                            size="small"
                            type="tertiary"
                            icon={<IconDownload aria-hidden="true" />}
                            onClick={() => {
                              const link = document.createElement("a");
                              link.href = api.downloadNativeStorageUrl(entry.path);
                              link.download = entry.name;
                              document.body.appendChild(link);
                              link.click();
                              link.remove();
                            }}
                          >
                            下载
                          </Button>
                        )}
                        <Button
                          size="small"
                          type="tertiary"
                          icon={<IconDownload aria-hidden="true" />}
                          disabled={anyBusy || !state?.localRoots.length}
                          onClick={() => {
                            setRestoreEntry(entry);
                            if (!restoreLocalRoot && state?.localRoots.length) {
                              setRestoreLocalRoot(state.localRoots[0].path);
                            }
                          }}
                        >
                          恢复到本地
                        </Button>
                        <IconButton
                          label={`重命名/移动 ${entry.name}`}
                          size="small"
                          icon={<IconEditStroked aria-hidden="true" />}
                          disabled={anyBusy}
                          onClick={() => {
                            setMoveTarget(entry);
                            setMoveDstPath(entry.path);
                          }}
                        />
                        <IconButton
                          label={`删除 ${entry.name}`}
                          size="small"
                          danger
                          icon={<IconDeleteStroked aria-hidden="true" />}
                          disabled={anyBusy}
                          ariaBusy={deletingEntryPath === entry.path}
                          onClick={() => handleDeleteEntry(entry)}
                        />
                      </div>
                    )}
                  </div>
                  );
                })}
              </div>
            ) : null}

            {!trashOpen && !browseNeedsFullList && browseData?.nextCursor && (
              <Button
                size="small"
                type="tertiary"
                loading={browsePaging}
                disabled={browsing || anyBusy}
                onClick={() => void loadBrowse(browsePath, { cursor: browseData.nextCursor })}
              >
                加载更多
              </Button>
            )}

            {batchRestoreOpen && (
              <Modal
                visible
                centered
                width={480}
                title={`批量恢复到飞牛本地（${selectedBrowsePaths.length} 项）`}
                onCancel={() => setBatchRestoreOpen(false)}
                okText="加入恢复队列"
                onOk={() => void handleBatchRestoreSelected()}
              >
                <div className="editor-form">
                  <Field label="目标本地授权目录" required>
                    <Select
                      value={restoreLocalRoot}
                      optionList={(state?.localRoots || []).map((r) => ({
                        value: r.path,
                        label: r.path,
                      }))}
                      onChange={(v) => setRestoreLocalRoot(String(v))}
                    />
                  </Field>
                  <Field label="子目录（可选）">
                    <Input
                      value={restoreSubPath}
                      onChange={setRestoreSubPath}
                      placeholder="例如 restored/batch"
                    />
                  </Field>
                  <Checkbox
                    checked={restoreOverwrite}
                    onChange={(e) => setRestoreOverwrite(Boolean(e.target.checked))}
                  >
                    覆盖本地已存在的同名文件
                  </Checkbox>
                </div>
              </Modal>
            )}

            {batchMoveOpen && (
              <Modal
                visible
                centered
                width={460}
                title={`批量移动（${selectedBrowsePaths.length} 项）`}
                onCancel={() => setBatchMoveOpen(false)}
                okText="加入移动队列"
                onOk={() => void handleBatchMoveSelected()}
              >
                <div className="editor-form">
                  <Field label="目标目录路径" required hint="所选项将移动到该目录下">
                    <Input
                      value={batchMoveDstDir}
                      onChange={setBatchMoveDstDir}
                      placeholder="/remote/archive"
                    />
                  </Field>
                </div>
              </Modal>
            )}

            {restoreEntry && (
              <div className="native-panel-section">
                <div className="native-section-head">
                  <div className="native-section-title">
                    <h3>
                      恢复{restoreEntry.isDir ? "文件夹" : "文件"}：{restoreEntry.name}
                    </h3>
                  </div>
                  <Button
                    size="small"
                    type="tertiary"
                    theme="borderless"
                    disabled={restoring}
                    onClick={() => setRestoreEntry(null)}
                  >
                    取消
                  </Button>
                </div>
                <div className="form-grid form-grid--two">
                  <Field label="目标本地授权目录" required>
                    <Select
                      value={restoreLocalRoot}
                      optionList={(state?.localRoots || []).map((r) => ({
                        value: r.path,
                        label: r.path,
                      }))}
                      onChange={(v) => setRestoreLocalRoot(String(v))}
                    />
                  </Field>
                  <Field label="子目录（可选）" hint="留空直接恢复到授权目录根下">
                    <Input
                      value={restoreSubPath}
                      onChange={setRestoreSubPath}
                      placeholder="例如 restored/2026"
                    />
                  </Field>
                </div>
                <div className="native-section-head">
                  <Checkbox
                    checked={restoreOverwrite}
                    onChange={(e) => setRestoreOverwrite(Boolean(e.target.checked))}
                  >
                    覆盖本地已存在的同名文件
                  </Checkbox>
                  <Button
                    type="primary"
                    theme="solid"
                    loading={restoring}
                    disabled={!restoreLocalRoot}
                    onClick={() => void handleExecuteRestore()}
                  >
                    开始恢复
                  </Button>
                </div>
              </div>
            )}
          </div>
        ) : (
          /* Import rclone.conf view */
          <div className="editor-form">
            <Field
              label="选择 rclone.conf 文件"
              required
              hint="支持明文和加密的 rclone.conf（≤ 512 KiB），解析后可勾选需要导入的存储"
            >
              <div className="native-file-picker">
                <input
                  ref={fileInputRef}
                  type="file"
                  accept=".conf,text/plain"
                  aria-label="rclone.conf"
                  className="sr-only"
                  onChange={(e) =>
                    void handleImportFileSelect(e.target.files?.[0])
                  }
                />
                <Button
                  type="tertiary"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={anyBusy}
                >
                  {importFileName ? "重新选择文件" : "选择 rclone.conf"}
                </Button>
                <span className="native-file-name muted">
                  {importFileName || "尚未选择文件"}
                </span>
                {importFileName && (
                  <Button
                    size="small"
                    type="tertiary"
                    theme="borderless"
                    disabled={anyBusy}
                    onClick={() => {
                      setImportFileName("");
                      setImportContent("");
                      setImportPreview(null);
                      setImportPreviewError("");
                      setSelectedImports([]);
                      if (fileInputRef.current) fileInputRef.current.value = "";
                    }}
                  >
                    清除
                  </Button>
                )}
              </div>
            </Field>

            <Field label="配置文件密码（加密文件需要）">
              <div className="native-password-row">
                <Input
                  mode="password"
                  value={importPassword}
                  onChange={setImportPassword}
                  placeholder="未加密配置文件可留空"
                />
                <Button
                  type="primary"
                  theme="light"
                  loading={previewingImport}
                  disabled={!importContent || importing}
                  onClick={() =>
                    void runImportPreview(importContent, importPassword)
                  }
                >
                  解析配置
                </Button>
              </div>
            </Field>

            {importPreviewError && (
              <InlineNotice
                role="alert"
                icon={<IconAlertTriangle aria-hidden="true" size="small" />}
                message={importPreviewError}
              />
            )}

            {importPreview && (
              <div className="native-import-section">
                <div className="native-section-head">
                  <div className="native-section-title">
                    <h3>解析结果</h3>
                    <Tag size="small" type="light" color="blue">
                      共 {importPreview.items.length} 项
                    </Tag>
                    {importPreview.encrypted && (
                      <Tag size="small" type="light" color="green">
                        已解密
                      </Tag>
                    )}
                  </div>
                  {hasConflictCandidates && (
                    <Checkbox
                      checked={importReplace}
                      onChange={(e) =>
                        handleImportReplaceToggle(Boolean(e.target.checked))
                      }
                    >
                      允许覆盖同名同类型存储
                    </Checkbox>
                  )}
                </div>

                <div className="native-remote-list">
                  {importPreview.items.map((item) => {
                    const selectable =
                      item.status === "ready" ||
                      (importReplace &&
                        item.status === "conflict" &&
                        item.canReplace);
                    const checked = selectedImports.includes(item.name);
                    const statusColor =
                      item.status === "ready"
                        ? "green"
                        : item.status === "conflict"
                          ? "orange"
                          : "red";
                    const statusLabel =
                      item.status === "ready"
                        ? "新增"
                        : item.status === "conflict"
                          ? "覆盖同名"
                          : item.status === "incompatible"
                            ? "类型冲突"
                            : "无法导入";
                    return (
                      <div key={item.name} className="native-import-row">
                        <Checkbox
                          disabled={!selectable || importing}
                          checked={checked}
                          onChange={(e) => {
                            const on = Boolean(e.target.checked);
                            setSelectedImports((prev) =>
                              on
                                ? Array.from(new Set([...prev, item.name]))
                                : prev.filter((n) => n !== item.name),
                            );
                          }}
                        />
                        <div className="native-remote-info">
                          <span className="native-remote-head">
                            <strong>{item.name}</strong>
                            {item.type && (
                              <Tag size="small" type="light" color="blue">
                                {remoteTypeLabels[item.type] || item.type}
                              </Tag>
                            )}
                            <Tag size="small" type="light" color={statusColor}>
                              {statusLabel}
                            </Tag>
                          </span>
                          {item.summary && (
                            <span className="muted native-remote-summary">
                              {item.summary}
                            </span>
                          )}
                          {item.status === "conflict" && !importReplace && (
                            <span className="native-import-note">
                              已存在同名存储「{item.existingName || item.name}」，勾选上方“允许覆盖同名同类型存储”后可导入覆盖
                            </span>
                          )}
                          {item.warnings?.map((w, idx) => (
                            <span key={idx} className="native-import-note">
                              {w}
                            </span>
                          ))}
                          {item.error && (
                            <span className="native-import-error">
                              {item.error}
                            </span>
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>
            )}
          </div>
        )}
      </Editor>

      {mkdirOpen && (
        <Modal
          visible
          centered
          width={420}
          title={`新建目录 · ${browsePath}`}
          onCancel={() => {
            if (!mkdirBusy) setMkdirOpen(false);
          }}
          okText="创建"
          confirmLoading={mkdirBusy}
          onOk={() => void handleExecuteMkdir()}
        >
          <div className="editor-form">
            <Field label="目录名称" required>
              <Input
                value={mkdirName}
                onChange={setMkdirName}
                placeholder="例如 photos-2026"
              />
            </Field>
          </div>
        </Modal>
      )}

      {moveTarget && (
        <Modal
          visible
          centered
          width={460}
          title={`重命名/移动 · ${moveTarget.name}`}
          onCancel={() => {
            if (!moveBusy) setMoveTarget(null);
          }}
          okText="确认移动"
          confirmLoading={moveBusy}
          onOk={() => void handleExecuteMove()}
        >
          <div className="editor-form">
            <Field
              label="目标完整路径"
              required
              hint={`原路径：${moveTarget.path}`}
            >
              <Input
                value={moveDstPath}
                onChange={setMoveDstPath}
                placeholder="/remote/new-path"
              />
            </Field>
          </div>
        </Modal>
      )}

    </>
  );
}
