import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconChainStroked,
  IconDeleteStroked,
  IconEditStroked,
  IconFolderStroked,
  IconInfoCircle,
  IconPlusStroked,
  IconRefresh,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import {
  Editor,
  Field,
  IconButton,
  InlineNotice,
  confirmAction,
  confirmDelete,
  errorToast,
} from "./common";
import { api } from "../api/client";
import type {
  AlistItem,
  NativeImportPreview,
  NativeRemoteItem,
  NativeStorageState,
  NativeStorageTestResult,
} from "../types";

type RemoteFormType = "s3" | "webdav" | "sftp" | "smb" | "crypt";
type PanelView = "overview" | "form" | "import";

const remoteTypeLabels: Record<string, string> = {
  s3: "S3 兼容存储",
  webdav: "WebDAV",
  sftp: "SFTP",
  smb: "SMB",
  crypt: "Crypt 加密",
  local: "本地目录别名",
};

const formTypes: RemoteFormType[] = ["s3", "webdav", "sftp", "smb", "crypt"];

const s3Providers: Record<string, string> = {
  Alibaba: "阿里云 OSS",
  TencentCOS: "腾讯云 COS",
  Cloudflare: "Cloudflare R2",
  Minio: "MinIO",
  AWS: "Amazon S3",
  Other: "Backblaze B2 / 其他 S3",
};

const s3EndpointPlaceholders: Record<string, string> = {
  Alibaba: "例如 oss-cn-hangzhou.aliyuncs.com",
  TencentCOS: "例如 cos.ap-guangzhou.myqcloud.com",
  Cloudflare: "例如 https://<account-id>.r2.cloudflarestorage.com",
  Minio: "例如 http://192.168.1.10:9000",
  AWS: "留空使用区域默认端点，或填写自定义端点",
  Other: "例如 s3.us-west-004.backblazeb2.com 或 https://...",
};

const webdavVendors: Record<string, string> = {
  other: "标准 WebDAV（自动读回比对）",
  nextcloud: "Nextcloud",
  owncloud: "ownCloud",
  rclone: "rclone / 支持修改时间的 WebDAV",
};

interface RemoteFormDraft {
  editing: boolean;
  type: RemoteFormType;
  name: string;
  provider: string;
  vendor: string;
  forcePathStyle: boolean;
  noCheckBucket: boolean;
  options: Record<string, string>;
  configuredSecrets: string[];
  clearSecrets: string[];
}

function emptyDraft(type: RemoteFormType = "s3"): RemoteFormDraft {
  return {
    editing: false,
    type,
    name: "",
    provider: "Alibaba",
    vendor: "other",
    forcePathStyle: false,
    noCheckBucket: false,
    options: {},
    configuredSecrets: [],
    clearSecrets: [],
  };
}

function draftFromRemote(item: NativeRemoteItem): RemoteFormDraft {
  const type = (
    formTypes.includes(item.type as RemoteFormType) ? item.type : "s3"
  ) as RemoteFormType;
  const opts = { ...(item.options || {}) };
  const provider = opts.provider || "Alibaba";
  const vendor = opts.vendor || "other";
  const forcePathStyle = opts.force_path_style === "true";
  const noCheckBucket = opts.no_check_bucket === "true";
  delete opts.provider;
  delete opts.vendor;
  delete opts.env_auth;
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
    options: sortedOpts,
    clearSecrets: [...d.clearSecrets].sort(),
  });
}

function buildRemoteOptions(draft: RemoteFormDraft): Record<string, string> {
  const settings: Record<string, string> = { type: draft.type };
  const secretKeys = new Set([
    "pass",
    "secret_access_key",
    "password",
    "password2",
    "key_file_pass",
  ]);
  for (const [k, rawVal] of Object.entries(draft.options)) {
    if (rawVal === undefined || rawVal === null) continue;
    if (k === "key_pem") {
      const normalized = rawVal.trim().replace(/\r?\n/g, "\\n");
      if (normalized) settings[k] = normalized;
    } else if (k === "known_hosts") {
      if (rawVal.trim()) settings[k] = rawVal.trim();
    } else if (secretKeys.has(k)) {
      if (rawVal !== "") settings[k] = rawVal;
    } else if (rawVal.trim() !== "") {
      settings[k] = rawVal.trim();
    }
  }
  if (draft.type === "s3") {
    settings.provider = draft.provider || "Other";
    settings.env_auth = "false";
    if (draft.forcePathStyle) settings.force_path_style = "true";
    if (draft.noCheckBucket) settings.no_check_bucket = "true";
  }
  if (draft.type === "webdav" && draft.vendor) {
    settings.vendor = draft.vendor;
  }
  return settings;
}

function validateRemoteDraft(
  draft: RemoteFormDraft,
  requireName: boolean,
): Record<string, string> {
  const errs: Record<string, string> = {};
  const name = draft.name.trim();
  if (requireName) {
    if (!name) {
      errs.name = "请输入存储名称";
    } else if (
      name === "本地" ||
      name.toLowerCase() === "rclone_config" ||
      name.toLowerCase().startsWith("_opensync_")
    ) {
      errs.name = "不能使用系统保留名称";
    }
  }

  const hasSecret = (key: string) =>
    Boolean(draft.options[key]) ||
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

  if (draft.type === "s3") {
    if (!(draft.options.access_key_id || "").trim()) {
      errs.access_key_id = "请输入 Access Key ID";
    }
    if (!hasSecret("secret_access_key")) {
      errs.secret_access_key = "请输入 Secret Access Key";
    }
  } else if (draft.type === "webdav") {
    const rawUrl = (draft.options.url || "").trim();
    if (!rawUrl) {
      errs.url = "请输入 WebDAV 地址";
    } else {
      try {
        const parsed = new URL(rawUrl);
        if (!["http:", "https:"].includes(parsed.protocol)) {
          errs.url = "请输入有效的 HTTP / HTTPS 地址";
        }
      } catch {
        errs.url = "请输入有效的 HTTP / HTTPS 地址";
      }
    }
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
  } else if (draft.type === "smb") {
    if (!(draft.options.host || "").trim()) {
      errs.host = "请输入 SMB 主机地址";
    }
    if (!(draft.options.user || "").trim()) {
      errs.user = "请输入用户名";
    }
    checkPort(draft.options.port);
  } else if (draft.type === "crypt") {
    if (!(draft.options.remote || "").trim()) {
      errs.remote = "请输入底层存储路径";
    }
    if (!hasSecret("password")) {
      errs.password = "请输入加密密码";
    }
  }
  return errs;
}

export function NativeStorageEditor({
  engine,
  onClose,
  onChanged,
}: {
  engine?: AlistItem;
  onClose: () => void;
  onSaved?: () => void;
  onChanged?: () => void;
}) {
  const [view, setView] = useState<PanelView>("overview");
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [state, setState] = useState<NativeStorageState | null>(null);

  // Overview engine settings
  const [remark, setRemark] = useState(engine?.remark || "原生备份");
  const [maxConcurrentJobs, setMaxConcurrentJobs] = useState(
    String(engine?.maxConcurrentJobs ?? 1),
  );
  const [savingEngine, setSavingEngine] = useState(false);
  const [deletingName, setDeletingName] = useState<string | null>(null);
  const [testingRemoteName, setTestingRemoteName] = useState<string | null>(null);
  const [remoteTestResults, setRemoteTestResults] = useState<
    Record<string, NativeStorageTestResult>
  >({});

  // Single remote add/edit form
  const [draft, setDraft] = useState<RemoteFormDraft>(() => emptyDraft("s3"));
  const [initialDraftSnap, setInitialDraftSnap] = useState(() =>
    snapshotDraft(emptyDraft("s3")),
  );
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [savingForm, setSavingForm] = useState(false);
  const [testingForm, setTestingForm] = useState(false);
  const [formTestResult, setFormTestResult] =
    useState<NativeStorageTestResult | null>(null);

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
      setRemark(next.remark || "原生备份");
      setMaxConcurrentJobs(String(next.maxConcurrentJobs ?? 1));
    } catch (err) {
      if (signal?.aborted) return;
      setLoadError(err instanceof Error ? err.message : "加载原生存储配置失败");
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void loadState(controller.signal);
    return () => controller.abort();
  }, [loadState]);

  const engineSettingsDirty = useMemo(() => {
    if (!state) return false;
    return (
      remark.trim() !== (state.remark || "原生备份") ||
      (maxConcurrentJobs.trim() || "0") !== String(state.maxConcurrentJobs ?? 1)
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
    deletingName !== null ||
    testingRemoteName !== null;

  const modalDirty =
    view === "overview"
      ? engineSettingsDirty
      : view === "form"
        ? formDirty
        : importDirty;

  // --- Engine actions ---
  const saveEngineSettings = async () => {
    const trimmedRemark = remark.trim();
    if (!trimmedRemark) {
      Toast.warning("请输入引擎名称");
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
      const wasEnabled = Boolean(state?.enabled);
      const updated = await api.saveNativeEngine({
        remark: trimmedRemark,
        maxConcurrentJobs: parsedMc,
      });
      setState(updated);
      setRemark(updated.remark || "原生备份");
      setMaxConcurrentJobs(String(updated.maxConcurrentJobs ?? 1));
      Toast.success(wasEnabled ? "引擎设置已保存" : "原生备份引擎已启用");
      notifyParent();
    } catch (err) {
      errorToast(err);
    } finally {
      setSavingEngine(false);
    }
  };

  // --- Overview remote actions ---
  const openAddForm = (type: RemoteFormType = "s3") => {
    const next = emptyDraft(type);
    setDraft(next);
    setInitialDraftSnap(snapshotDraft(next));
    setFieldErrors({});
    setFormTestResult(null);
    setView("form");
  };

  const openEditForm = (item: NativeRemoteItem) => {
    const next = draftFromRemote(item);
    setDraft(next);
    setInitialDraftSnap(snapshotDraft(next));
    setFieldErrors({});
    setFormTestResult(null);
    setView("form");
  };

  const openImportView = () => {
    setImportFileName("");
    setImportContent("");
    setImportPassword("");
    setImportPreview(null);
    setImportPreviewError("");
    setImportReplace(false);
    setSelectedImports([]);
    if (fileInputRef.current) fileInputRef.current.value = "";
    setView("import");
  };

  const backToOverview = () => {
    const leave = () => {
      setFieldErrors({});
      setFormTestResult(null);
      setView("overview");
    };
    if ((view === "form" && formDirty) || (view === "import" && importDirty)) {
      confirmAction({
        title: "返回存储列表？",
        content: "当前表单中的未保存内容将被放弃。",
        okText: "放弃并返回",
        onOk: leave,
      });
      return;
    }
    leave();
  };

  const handleTestSavedRemote = async (remoteName: string) => {
    setTestingRemoteName(remoteName);
    try {
      const res = await api.testNativeStorage({ name: remoteName });
      setRemoteTestResults((prev) => ({ ...prev, [remoteName]: res }));
      if (res.ok && res.warning) {
        Toast.warning(res.message);
      } else if (res.ok) {
        Toast.success(`${remoteName}：${res.message}（${res.latencyMs}ms）`);
      } else {
        Toast.error(`${remoteName}：${res.message}`);
      }
    } catch (err) {
      errorToast(err);
    } finally {
      setTestingRemoteName(null);
    }
  };

  const handleDeleteRemote = (item: NativeRemoteItem) => {
    if (item.usedByCrypts.length > 0) {
      Toast.warning(
        `存储「${item.name}」正被加密存储（${item.usedByCrypts.join("、")}）引用，请先删除或修改对应加密存储`,
      );
      return;
    }
    if (item.usedByJobs.length > 0) {
      Toast.warning(
        `存储「${item.name}」正被同步任务（${item.usedByJobs.join("、")}）使用，请先修改或删除关联任务`,
      );
      return;
    }
    confirmDelete(
      `删除原生存储「${item.name}」？`,
      async () => {
        setDeletingName(item.name);
        try {
          await api.deleteNativeStorage(item.name);
          setRemoteTestResults((prev) => {
            const copy = { ...prev };
            delete copy[item.name];
            return copy;
          });
          await loadState();
          notifyParent();
        } finally {
          setDeletingName(null);
        }
      },
      "仅删除 OpenSync 中保存的连接配置，不会删除远端文件。",
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
    setDraft((prev) => ({
      ...emptyDraft(nextType),
      name: prev.name,
    }));
    setFieldErrors({});
    setFormTestResult(null);
  };

  const handleProviderChange = (nextProvider: string) => {
    setDraft((prev) => ({
      ...prev,
      provider: nextProvider,
      // MinIO typically requires path-style addressing; pre-check on new forms.
      forcePathStyle: !prev.editing ? nextProvider === "Minio" : prev.forcePathStyle,
    }));
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
        name: draft.name.trim(),
        options: buildRemoteOptions(draft),
        editing: draft.editing,
        clearSecrets: draft.clearSecrets.length > 0 ? draft.clearSecrets : undefined,
      });
      setFormTestResult(res);
    } catch (err) {
      errorToast(err);
    } finally {
      setTestingForm(false);
    }
  };

  const handleSaveForm = async () => {
    const errs = validateRemoteDraft(draft, true);
    if (Object.keys(errs).length > 0) {
      setFieldErrors(errs);
      Toast.warning(Object.values(errs)[0]);
      return;
    }
    setSavingForm(true);
    try {
      const trimmedName = draft.name.trim();
      const options = buildRemoteOptions(draft);
      if (draft.editing) {
        await api.updateNativeStorage({
          name: trimmedName,
          options,
          clearSecrets:
            draft.clearSecrets.length > 0 ? draft.clearSecrets : undefined,
        });
        Toast.success(`存储「${trimmedName}」已更新`);
      } else {
        await api.saveNativeStorage({
          name: trimmedName,
          options,
        });
        Toast.success(`存储「${trimmedName}」已添加`);
      }
      setRemoteTestResults((prev) => {
        const copy = { ...prev };
        delete copy[trimmedName];
        return copy;
      });
      await loadState();
      notifyParent();
      setView("overview");
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
      setRemoteTestResults({});
      await loadState();
      notifyParent();
      setView("overview");
    } catch (err) {
      errorToast(err);
    } finally {
      setImporting(false);
    }
  };

  const cryptQuickSources = useMemo(() => {
    const items: Array<{ label: string; value: string; kind: "remote" | "local" }> = [];
    for (const r of state?.remotes || []) {
      if (draft.editing && r.name === draft.name) continue;
      if (r.type === "local") continue;
      items.push({ label: `${r.name}:`, value: `${r.name}:`, kind: "remote" });
    }
    for (const root of state?.localRoots || []) {
      items.push({ label: root.path, value: root.path, kind: "local" });
    }
    return items;
  }, [state, draft.editing, draft.name]);

  const hasConflictCandidates = Boolean(
    importPreview?.items.some((i) => i.status === "conflict" && i.canReplace),
  );

  const modalTitle =
    view === "overview"
      ? "原生备份存储"
      : view === "import"
        ? "导入 rclone.conf"
        : draft.editing
          ? `编辑存储 · ${draft.name}`
          : "添加远端存储";

  return (
    <Editor
      title={modalTitle}
      visible
      width={640}
      busy={anyBusy}
      dirty={modalDirty}
      onClose={onClose}
      onSave={() => {
        if (view === "form") void handleSaveForm();
        else if (view === "import") void handleExecuteImport();
        else onClose();
      }}
      footer={({ close }) => {
        if (view === "form") {
          return (
            <div className="editor-actions">
              <Button
                type="tertiary"
                className="editor-actions-start"
                onClick={backToOverview}
                disabled={anyBusy}
              >
                返回列表
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
              <Button
                type="tertiary"
                className="editor-actions-start"
                onClick={backToOverview}
                disabled={anyBusy}
              >
                返回列表
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
      ) : view === "overview" && state ? (
        <div className="editor-form native-panel">
          {!state.rcloneAvailable && (
            <InlineNotice
              role="alert"
              icon={<IconAlertTriangle aria-hidden="true" size="small" />}
              message={`内置 rclone 不可用：${state.rcloneError || "未找到可执行文件"}`}
            />
          )}

          {/* Section 1: Engine status + concurrency + fnOS authorized local directories */}
          <section className="native-panel-section">
            <div className="native-section-head">
              <div className="native-section-title">
                <h3>原生备份引擎</h3>
                <Tag
                  size="small"
                  type="light"
                  color={state.enabled ? "green" : "grey"}
                >
                  {state.enabled ? "已启用" : "未启用"}
                </Tag>
              </div>
              <Button
                size="small"
                type="primary"
                theme={state.enabled ? "light" : "solid"}
                loading={savingEngine}
                disabled={state.enabled && !engineSettingsDirty}
                onClick={() => void saveEngineSettings()}
              >
                {state.enabled ? "保存引擎设置" : "启用原生引擎"}
              </Button>
            </div>

            <div className="form-grid form-grid--two">
              <Field label="引擎名称" required>
                <Input
                  value={remark}
                  onChange={setRemark}
                  maxLength={200}
                  placeholder="原生备份"
                />
              </Field>
              <Field
                label="最大并发任务数"
                hint="0 不限；1 表示串行排队（0–50）"
              >
                <Input
                  inputMode="numeric"
                  value={maxConcurrentJobs}
                  onChange={setMaxConcurrentJobs}
                  placeholder="1"
                />
              </Field>
            </div>

            <div className="native-local-block">
              <div className="native-local-head">
                <span className="native-local-title">
                  <IconFolderStroked aria-hidden="true" size="small" />
                  <span>飞牛本地授权目录</span>
                  <Tag
                    size="small"
                    type="light"
                    color={state.localRoots.length > 0 ? "green" : "orange"}
                  >
                    {state.localRoots.length > 0
                      ? `已授权 ${state.localRoots.length} 个`
                      : "未授权"}
                  </Tag>
                </span>
                <Button
                  size="small"
                  type="tertiary"
                  theme="borderless"
                  icon={<IconRefresh aria-hidden="true" />}
                  loading={loading}
                  onClick={() => void loadState()}
                >
                  刷新
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
                    未检测到本地授权目录。原生备份的源目录必须来自本地授权目录，请在飞牛「应用设置 → 文件夹访问权限」中为 OpenSync 授权文件夹后点击刷新。
                  </span>
                </div>
              )}
            </div>
          </section>

          {/* Section 2: Configured remote storages */}
          <section className="native-panel-section">
            <div className="native-section-head">
              <div className="native-section-title">
                <h3>远端存储配置</h3>
                <Tag size="small" type="light" color="blue">
                  {state.remotes.length} 个
                </Tag>
              </div>
              <div className="native-section-actions">
                <Button
                  size="small"
                  type="tertiary"
                  disabled={anyBusy}
                  onClick={openImportView}
                >
                  导入 rclone.conf
                </Button>
                <Button
                  size="small"
                  type="primary"
                  theme="solid"
                  icon={<IconPlusStroked aria-hidden="true" />}
                  disabled={anyBusy}
                  onClick={() => openAddForm("s3")}
                >
                  添加存储
                </Button>
              </div>
            </div>

            {state.remotes.length === 0 ? (
              <div className="sync-method-banner">
                <IconInfoCircle aria-hidden="true" size="small" />
                <span>
                  尚未配置远端存储。若仅需将本地目录备份到其他本机磁盘或外接硬盘，启用上方「原生备份引擎」并授权目标目录即可；如需备份到对象存储、WebDAV、SFTP、SMB 或加密库，请点击「添加存储」或导入 rclone.conf。
                </span>
              </div>
            ) : (
              <div className="native-remote-list">
                {state.remotes.map((item) => {
                  const testRes = remoteTestResults[item.name];
                  const isLocked =
                    item.usedByJobs.length > 0 || item.usedByCrypts.length > 0;
                  const lockHint =
                    item.usedByCrypts.length > 0
                      ? `正被加密存储 ${item.usedByCrypts.join("、")} 引用`
                      : item.usedByJobs.length > 0
                        ? `正被任务 ${item.usedByJobs.join("、")} 使用`
                        : "";
                  return (
                    <div key={item.name} className="native-remote-row">
                      <div className="native-remote-info">
                        <span className="native-remote-head">
                          <strong>{item.name}</strong>
                          <Tag size="small" type="light" color="blue">
                            {remoteTypeLabels[item.type] || item.type}
                          </Tag>
                          {item.usedByJobs.length > 0 && (
                            <Tag size="small" type="light" color="cyan">
                              {item.usedByJobs.length} 个任务
                            </Tag>
                          )}
                          {item.usedByCrypts.length > 0 && (
                            <Tag size="small" type="light" color="violet">
                              被 Crypt 引用
                            </Tag>
                          )}
                        </span>
                        {item.summary && (
                          <span
                            className="muted native-remote-summary"
                            title={item.summary}
                          >
                            {item.summary}
                          </span>
                        )}
                        {testRes && (
                          <div
                            className={`native-inline-test ${
                              testRes.ok
                                ? testRes.warning
                                  ? "is-warn"
                                  : "is-ok"
                                : "is-error"
                            }`}
                          >
                            <span>
                              {testRes.message}（{testRes.latencyMs}ms）
                            </span>
                            {testRes.warning && (
                              <span className="native-inline-test-sub">
                                {testRes.warning}
                              </span>
                            )}
                          </div>
                        )}
                      </div>
                      <div className="native-remote-actions">
                        <IconButton
                          label={
                            testingRemoteName === item.name
                              ? "测试中…"
                              : `测试存储 ${item.name}`
                          }
                          size="small"
                          icon={<IconChainStroked aria-hidden="true" />}
                          disabled={anyBusy}
                          ariaBusy={testingRemoteName === item.name}
                          onClick={() => void handleTestSavedRemote(item.name)}
                        />
                        {item.type !== "local" && (
                          <IconButton
                            label={`编辑存储 ${item.name}`}
                            size="small"
                            icon={<IconEditStroked aria-hidden="true" />}
                            disabled={anyBusy}
                            onClick={() => openEditForm(item)}
                          />
                        )}
                        <IconButton
                          label={
                            isLocked
                              ? `无法删除：${lockHint}`
                              : `删除存储 ${item.name}`
                          }
                          size="small"
                          danger
                          icon={<IconDeleteStroked aria-hidden="true" />}
                          disabled={anyBusy}
                          ariaBusy={deletingName === item.name}
                          onClick={() => handleDeleteRemote(item)}
                        />
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </section>
        </div>
      ) : view === "form" ? (
        <div className="editor-form">
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
                optionList={formTypes.map((t) => ({
                  value: t,
                  label: remoteTypeLabels[t],
                }))}
                onChange={(v) => handleFormTypeChange(v as RemoteFormType)}
              />
            </Field>
            <Field
              label="存储名称"
              required
              error={fieldErrors.name}
              hint={
                draft.editing
                  ? "编辑时名称固定，关联任务无需修改路径"
                  : "用于任务路径前缀，如 /archive/..."
              }
            >
              <Input
                value={draft.name}
                disabled={draft.editing}
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
                  required
                  error={fieldErrors.access_key_id}
                >
                  <Input
                    value={draft.options.access_key_id || ""}
                    onChange={(v) => updateDraftOption("access_key_id", v)}
                    placeholder="填写访问密钥 ID"
                  />
                </Field>
                <Field
                  label="Secret Access Key"
                  required={!draft.editing}
                  error={fieldErrors.secret_access_key}
                >
                  <Input
                    mode="password"
                    value={draft.options.secret_access_key || ""}
                    onChange={(v) => updateDraftOption("secret_access_key", v)}
                    placeholder={
                      draft.editing &&
                      draft.configuredSecrets.includes("secret_access_key")
                        ? "已保存，留空保留原密钥"
                        : "填写访问密钥 Secret"
                    }
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
              <Field label="WebDAV 地址" required error={fieldErrors.url}>
                <Input
                  value={draft.options.url || ""}
                  onChange={(v) => updateDraftOption("url", v)}
                  placeholder="例如 https://dav.example.com/dav"
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
                    placeholder={
                      draft.editing &&
                      draft.configuredSecrets.includes("pass") &&
                      !draft.clearSecrets.includes("pass")
                        ? "已保存，留空保留原密码"
                        : "可选"
                    }
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
              {draft.vendor === "other" && (
                <div className="sync-method-banner">
                  <IconInfoCircle aria-hidden="true" size="small" />
                  <span>
                    标准 WebDAV 默认会读回目标内容比对以避免漏掉同大小更新；若服务端为 Nextcloud / ownCloud / rclone，建议选择对应类型。
                  </span>
                </div>
              )}
            </>
          )}

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
                <Field
                  label="密码（与私钥二选一）"
                  error={fieldErrors.pass}
                >
                  <Input
                    mode="password"
                    value={draft.options.pass || ""}
                    onChange={(v) => updateDraftOption("pass", v)}
                    placeholder={
                      draft.editing &&
                      draft.configuredSecrets.includes("pass") &&
                      !draft.clearSecrets.includes("pass")
                        ? "已保存，留空保留原密码"
                        : "登录密码"
                    }
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
                  placeholder={
                    draft.editing &&
                    draft.configuredSecrets.includes("key_file_pass") &&
                    !draft.clearSecrets.includes("key_file_pass")
                      ? "已保存，留空保留原私钥密码"
                      : "未加密私钥可留空"
                  }
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
                  draft.configuredSecrets.includes("key_pem")) && (
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
                    placeholder={
                      draft.editing &&
                      draft.configuredSecrets.includes("pass") &&
                      !draft.clearSecrets.includes("pass")
                        ? "已保存，留空保留原密码"
                        : "可选"
                    }
                  />
                </Field>
              </div>
              <Field label="域 / 工作组（可选）">
                <Input
                  value={draft.options.domain || ""}
                  onChange={(v) => updateDraftOption("domain", v)}
                  placeholder="例如 WORKGROUP"
                />
              </Field>
            </>
          )}

          {draft.type === "crypt" && (
            <>
              <Field
                label="底层存储路径"
                required
                error={fieldErrors.remote}
                hint="格式为「存储名:子路径」（如 archive:bucket/private）或飞牛本地授权绝对路径"
              >
                <Input
                  value={draft.options.remote || ""}
                  onChange={(v) => updateDraftOption("remote", v)}
                  placeholder="archive:bucket/private 或 /vol1/1000/backup"
                />
              </Field>
              {cryptQuickSources.length > 0 && (
                <div className="native-quick-picks">
                  <span className="muted">快捷填入：</span>
                  {cryptQuickSources.map((src) => (
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
                    placeholder={
                      draft.editing &&
                      draft.configuredSecrets.includes("password")
                        ? "已保存，留空保留原加密密码"
                        : "输入加密密码"
                    }
                  />
                </Field>
                <Field label="加密盐（可选）">
                  <Input
                    mode="password"
                    value={draft.options.password2 || ""}
                    onChange={(v) => updateDraftOption("password2", v)}
                    placeholder={
                      draft.editing &&
                      draft.configuredSecrets.includes("password2") &&
                      !draft.clearSecrets.includes("password2")
                        ? "已保存，留空保留原加密盐"
                        : "可选，建议设置以增强安全性"
                    }
                  />
                </Field>
              </div>
              {draft.editing &&
                draft.configuredSecrets.includes("password2") && (
                  <Checkbox
                    checked={draft.clearSecrets.includes("password2")}
                    onChange={(e) =>
                      toggleClearSecret("password2", Boolean(e.target.checked))
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
  );
}
