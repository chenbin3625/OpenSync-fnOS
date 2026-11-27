import {
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import { Form } from "@douyinfe/semi-ui/lib/es/form";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconArrowLeft,
  IconArrowRight,
  IconChevronRight,
  IconInfoCircle,
  IconLockStroked,
  IconRefresh,
  IconTick,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  Editor,
  Field,
  InlineNotice,
  SettingRow,
  confirmAction,
  errorToast,
} from "../components/common";
import { NativeStorageEditor } from "../components/NativeStorageEditor";
import { ExcludeTree } from "../components/ExcludeTree";
import { ExpertTuningDrawer } from "../components/ExpertTuningDrawer";
import { FileTypeFilter } from "../components/FileTypeFilter";
import {
  analyzeInlineCryptTarget,
  createInitialInlineCryptDraft,
  InlineCryptPanel,
  syncInlineCryptTarget,
  validateInlineCryptDraft,
  type InlineCryptDraft,
} from "../components/InlineCryptPanel";
import { QuickCryptModal } from "../components/QuickCryptModal";
import { RemotePaths } from "../components/RemotePaths";
import { SyncPathPreview } from "../components/SyncPathPreview";
import {
  formatContinuousSyncPlan,
  type SyncDirectionKey,
} from "../components/SyncStatus";
import { WeeklyScheduleGrid } from "../components/WeeklyScheduleGrid";
import {
  CloudRemoteFolderPicker,
  NasLocalFolderPicker,
} from "../components/WizardFolderPickers";
import { useAction, useEditorForm, useResource } from "../lib/hooks";
import { channelNames, getNotifyEndpointSummary } from "../lib/notifyForm";
import {
  analyzeDstEncryption,
  buildQuickCryptOptions,
  replaceDstPathWithEncrypted,
} from "../lib/quickCrypt";
import {
  createRemoteDirCache,
  RemoteDirCacheProvider,
  type RemoteDirCache,
} from "../lib/remoteDirCache";
import {
  buildSyncPathPreview,
  formatDisplayPath,
} from "../lib/syncPathPreview";
import {
  buildJobPayload,
  defaultJobForm,
  findQuarkRemotesForPaths,
  findStandardWebDAVRemotesForPaths,
  isExcludePatternEnabled,
  jobToForm,
  parseExcludeFolders,
  resolveAutoCompareModeForDst,
  shouldClearExcludeFoldersOnSrcChange,
  systemDirFilterGroups,
  updateExcludeFolders,
  updateExcludePatterns,
  validateJobFormFields,
  validateManualSyncPath,
  type CompareModeSource,
  type JobFieldErrorKey,
  type JobForm,
} from "../lib/taskForm";
import {
  applyWizardEndpoints,
  canSwitchBackToWizard,
  extractRemoteRootAndSubPath,
  extractWizardPaths,
  formatNasDisplayPath,
  inferOneWayDeleteMode,
  inferWizardDirection,
  isWizardCompatibleExistingJob,
} from "../lib/taskWizard";
import {
  compareModeOptions,
  conflictPolicyOptions,
  filterModeOptions,
  notifyModeOptions,
  parseIntervalInput,
} from "./Home/homeUtils";
import {
  commitFileSizeInput,
  fileSizeUnitOptions,
  hasPendingFileSizeDraftChange,
  mergeFileSizeDrafts,
  parseFileSizeInput,
  type FileSizeDrafts,
  type FileSizeUnit,
} from "./Home/fileSizeUnits";
import type {
  AlistItem,
  FilterTestResult,
  JobItem,
  NativeExtraFlags,
  NativeRemoteItem,
  OneWayDeleteMode,
  SyncDirectionMode,
} from "../types";

export { parseIntervalInput };
export { hasPendingFileSizeDraftChange, mergeFileSizeDrafts };

export interface TaskEditorSavedResult {
  createdId?: number;
  mirrorPreviewId?: number;
}

const wizardEditorTabs = [
  { key: "folders", label: "选择同步文件夹", optional: false },
  { key: "safety", label: "同步方向与安全", optional: false },
  { key: "plan", label: "过滤规则与同步计划", optional: true },
] as const;

const freeEditorTabs = [
  { key: "basic", label: "基础与路径", optional: false },
  { key: "schedule", label: "同步设置", optional: false },
  { key: "filter", label: "过滤规则", optional: true },
] as const;

const pollIntervalPresets = [
  { label: "30 秒", value: 30 },
  { label: "1 分钟", value: 60 },
  { label: "5 分钟", value: 300 },
  { label: "15 分钟", value: 900 },
  { label: "1 小时", value: 3600 },
] as const;

const auditIntervalPresets = [
  { label: "1 小时", value: 60 },
  { label: "6 小时", value: 360 },
  { label: "12 小时", value: 720 },
  { label: "每天", value: 1440 },
] as const;

const fileSizeFilterDefaults = {
  min: { label: "排除小于", value: 10, unit: "KB" },
  max: { label: "排除大于", value: 10, unit: "GB" },
} satisfies Record<
  "min" | "max",
  {
    label: string;
    value: number;
    unit: FileSizeUnit;
  }
>;

const freeDirectionCards = [
  {
    key: "oneway",
    title: "单向同步",
    route: "源目录 → 目标目录",
    desc: "将源目录的新增和修改同步到目标目录，支持本地和远端自由组合",
    icon: IconArrowRight,
  },
  {
    key: "bidirectional",
    title: "双向同步",
    route: "目录 A ⇄ 目录 B",
    desc: "双端的新增、修改和删除会互相同步，检测到冲突时可在冲突中心处理",
    icon: IconRefresh,
  },
] as const;

const wizardDirectionCards: Array<{
  key: SyncDirectionMode;
  title: string;
  route: string;
  desc: string;
  icon: typeof IconArrowRight;
}> = [
  {
    key: "bidirectional",
    title: "双向同步",
    route: "NAS ⇄ 云端",
    desc: "本地与云端任意一侧新增、修改或删除文件，均会自动保持双端一致",
    icon: IconRefresh,
  },
  {
    key: "upload",
    title: "仅上传本地更改",
    route: "NAS → 云端",
    desc: "将 NAS 本地目录持续备份到云端存储，云端手动整理不反向影响 NAS",
    icon: IconArrowRight,
  },
  {
    key: "download",
    title: "仅下载云端更改",
    route: "云端 → NAS",
    desc: "将云端存储变更持续同步回 NAS 本地目录，不会修改或删除云端原文件",
    icon: IconArrowLeft,
  },
];

const oneWayDeleteModeCards: Array<{
  key: OneWayDeleteMode;
  title: string;
  badge?: string;
  desc: string;
  danger?: boolean;
}> = [
  {
    key: "none",
    title: "不删除目标端文件",
    badge: "默认推荐",
    desc: "增量备份模式：只追加与更新文件，永远保留目标端已有文件，误删风险最低",
  },
  {
    key: "tracked",
    title: "仅同步本任务产生的删除",
    desc: "基线追踪删除：仅清理曾由本任务同步过去且在源端被删的文件，不影响目标端原有其他内容",
  },
  {
    key: "mirror",
    title: "完全镜像清理目标多余文件",
    desc: "镜像同步模式：严格保证目标端与源端完全一致，清理目标端所有多余文件（首次启用先预览影响）",
    danger: true,
  },
];

function parsePositiveIntDraft(raw: string): number {
  const trimmed = raw.trim();
  if (!trimmed) return 0;
  if (!/^\d+$/.test(trimmed)) return -1;
  return Number(trimmed);
}

type TaskEditorProps = {
  job: JobItem | null;
  initialDraft?: JobForm;
  initialRemote?: string;
  engines: AlistItem[];
  existingJobs?: JobItem[];
  enginesError?: string;
  onRetryEngines?: () => void;
  onNavigateEngines?: () => void;
  onStorageChanged?: () => void;
  onClose: () => void;
  onSaved: (result: TaskEditorSavedResult) => void;
};

export default function TaskEditor({
  job,
  initialDraft,
  initialRemote,
  engines,
  existingJobs,
  enginesError,
  onRetryEngines,
  onNavigateEngines,
  onStorageChanged,
  onClose,
  onSaved,
}: TaskEditorProps) {
  const nativeStorage = useResource((signal) => api.nativeStorageState(signal));
  const defaultEngineId =
    engines[0]?.id || nativeStorage.data?.engineId || undefined;
  const seedForm = useMemo(
    () =>
      initialDraft
        ? {
            ...initialDraft,
            isCron: 3,
            method: initialDraft.method === 2 ? 0 : initialDraft.method,
            alistId: initialDraft.alistId || defaultEngineId,
          }
        : job
          ? { ...jobToForm(job), alistId: job.alistId || defaultEngineId }
          : defaultJobForm(defaultEngineId),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  // Determine initial mode: brand new tasks & 1:1 NAS<->Cloud tasks open in
  // Standard 3-Step Wizard; cloud-to-cloud, multi-src/dst, local-to-local,
  // move mode, or legacy non-continuous jobs automatically enter Free Mode.
  const [isFreeMode, setIsFreeMode] = useState<boolean>(() =>
    Boolean((job || initialDraft) && !isWizardCompatibleExistingJob(seedForm)),
  );

  const { form, dirty, change, patch, setForm, setDirty } =
    useEditorForm<JobForm>(() => seedForm);
  useEffect(() => {
    if (!form.alistId && defaultEngineId) {
      setForm((current) =>
        current.alistId ? current : { ...current, alistId: defaultEngineId },
      );
    }
  }, [defaultEngineId, form.alistId, setForm]);

  const initialPaths = useMemo(
    () => extractWizardPaths(seedForm),
    [seedForm],
  );
  const [wizardNasPath, setWizardNasPath] = useState<string>(
    initialPaths.nasPath,
  );
  const [wizardRemotePath, setWizardRemotePath] = useState<string>(
    initialPaths.remotePath,
  );
  const [wizardDirection, setWizardDirection] = useState<SyncDirectionMode>(
    () =>
      job || initialDraft ? inferWizardDirection(seedForm) : "upload",
  );
  const [wizardDeleteMode, setWizardDeleteMode] = useState<OneWayDeleteMode>(
    () => inferOneWayDeleteMode(seedForm),
  );
  const [inlineCryptDraft, setInlineCryptDraft] = useState<InlineCryptDraft>(
    () =>
      createInitialInlineCryptDraft(
        initialPaths.remotePath,
        nativeStorage.data?.remotes,
      ),
  );
  const [createdInlineCryptName, setCreatedInlineCryptName] = useState("");
  const inlineCryptDirtyNameRef = useRef(false);

  const [addingStorage, setAddingStorage] = useState(false);
  const [preferredRemote, setPreferredRemote] = useState(initialRemote);
  const [nativeTarget, setNativeTarget] = useState("");
  const dirCacheRef = useRef<RemoteDirCache | null>(null);
  if (!dirCacheRef.current) {
    dirCacheRef.current = createRemoteDirCache();
  }
  const [remoteTreeEpoch, setRemoteTreeEpoch] = useState(0);
  const [quickCryptOpen, setQuickCryptOpen] = useState(false);
  const notifications = useResource((signal) => api.notifications(signal));
  const systemSettings = useResource((signal) => api.settings(signal));
  const [bwLimitError, setBwLimitError] = useState<string | null>(null);
  const [fileSizeDrafts, setFileSizeDrafts] = useState<FileSizeDrafts>({});
  const [sizeEnabled, setSizeEnabled] = useState<{ min: boolean; max: boolean }>(
    () => ({
      min: (parseFileSizeInput(seedForm.minFileSize) ?? 0) > 0,
      max: (parseFileSizeInput(seedForm.maxFileSize) ?? 0) > 0,
    }),
  );

  // Draft strings for continuous sync numeric inputs
  const [pollDraft, setPollDraft] = useState<string>(() =>
    String(seedForm.extraFlags?.syncPollIntervalSec || 60),
  );
  const [auditDraft, setAuditDraft] = useState<string>(() =>
    String(seedForm.extraFlags?.syncAuditIntervalMin || 1440),
  );
  const [debounceDraft, setDebounceDraft] = useState<string>(() =>
    String(seedForm.extraFlags?.watchDebounceSec || 30),
  );

  const effectiveDirty =
    dirty ||
    Boolean(initialDraft) ||
    (!isFreeMode && inlineCryptDraft.enabled) ||
    hasPendingFileSizeDraftChange(form, fileSizeDrafts);

  const [policyOpen, setPolicyOpen] = useState<boolean>(() =>
    Boolean(
      seedForm.compareMode || seedForm.conflictPolicy || seedForm.trashEnable,
    ),
  );
  const [compareModeSource, setCompareModeSource] = useState<CompareModeSource>(
    () => (seedForm.compareMode === 0 ? "default" : "manual"),
  );
  // Keep rclone expert options collapsed by default to eliminate visual noise.
  const [advancedOpen, setAdvancedOpen] = useState<boolean>(false);
  const [filterTesterOpen, setFilterTesterOpen] = useState<boolean>(false);
  const [scheduleCadenceOpen, setScheduleCadenceOpen] = useState<boolean>(() =>
    Boolean(
      seedForm.extraFlags?.syncPollIntervalSec ||
        seedForm.extraFlags?.syncAuditIntervalMin ||
        seedForm.extraFlags?.watchDebounceSec ||
        seedForm.windowEnable ||
        seedForm.notifyMode === 1,
    ),
  );

  // Filter interactive tester state
  const [testPath, setTestPath] = useState("2026/photo.jpg");
  const [testSizeMB, setTestSizeMB] = useState("2");
  const [testAgeMinutes, setTestAgeMinutes] = useState("60");
  const [testIsDir, setTestIsDir] = useState(false);
  const [testingFilter, setTestingFilter] = useState(false);
  const [testResult, setTestResult] = useState<FilterTestResult | null>(null);

  const isNew = !job;
  const currentTabs = isFreeMode ? freeEditorTabs : wizardEditorTabs;
  const [step, setStep] = useState(0);
  const [activeTab, setActiveTab] = useState<string>(() =>
    isFreeMode ? "basic" : "folders",
  );
  const [validatedSteps, setValidatedSteps] = useState<Set<number>>(
    () => new Set(),
  );
  const action = useAction();
  const activePanel = isNew ? currentTabs[step].key : activeTab;

  // Panels stay mounted once visited so switching tabs keeps directory trees & expansion state.
  const [visitedPanels, setVisitedPanels] = useState<string[]>([activePanel]);
  useEffect(() => {
    setVisitedPanels((previous) =>
      previous.includes(activePanel) ? previous : [...previous, activePanel],
    );
  }, [activePanel]);
  const panelMounted = (panel: string) =>
    panel === activePanel || visitedPanels.includes(panel);
  const panelStyle = (panel: string) =>
    panel === activePanel ? undefined : { display: "none" };

  const scrollRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = 0;
  }, [activePanel, isFreeMode]);

  // Keep inline crypt suggested names / reuse candidates synced with remotes & selected cloud folder
  useEffect(() => {
    setInlineCryptDraft((prev) => {
      const synced = syncInlineCryptTarget(
        prev,
        wizardRemotePath,
        nativeStorage.data?.remotes,
      );
      if (inlineCryptDirtyNameRef.current) {
        return {
          ...synced,
          values: { ...synced.values, name: prev.values.name },
        };
      }
      return synced;
    });
  }, [wizardRemotePath, nativeStorage.data?.remotes]);

  const source = form.srcPath[0];
  const target = form.dstPath[0];
  const [sourceRoot, targetRoot] = [source, target].map(
    (path) => path?.split("/").filter(Boolean)[0],
  );
  const localPath =
    sourceRoot === "本地" ? source : targetRoot === "本地" ? target : undefined;
  const remoteRoot = sourceRoot === "本地" ? targetRoot : sourceRoot;

  const generatedName = useMemo(() => {
    if (!isNew || initialDraft) return "";
    if (!isFreeMode) {
      if (!wizardNasPath || !wizardRemotePath) return "";
      const cleanNas = formatNasDisplayPath(
        wizardNasPath,
        nativeStorage.data?.localRoots,
      );
      const nasLeaf = cleanNas.split("/").filter(Boolean).at(-1) || cleanNas;
      const { remoteName } = extractRemoteRootAndSubPath(wizardRemotePath);
      return nasLeaf && remoteName ? `${nasLeaf} · ${remoteName}` : "";
    }
    if (source && target && localPath && remoteRoot && remoteRoot !== "本地") {
      return `${formatDisplayPath(localPath).split("/").filter(Boolean).at(-1)} · ${remoteRoot}`;
    }
    return "";
  }, [
    isNew,
    initialDraft,
    isFreeMode,
    wizardNasPath,
    wizardRemotePath,
    nativeStorage.data?.localRoots,
    source,
    target,
    localPath,
    remoteRoot,
  ]);

  const mergedForm = useMemo<JobForm>(() => {
    const merged = mergeFileSizeDrafts(form, fileSizeDrafts);
    const effective: JobForm = !isFreeMode
      ? {
          ...merged,
          extraFlags: {
            ...merged.extraFlags,
            syncDirectionMode: wizardDirection,
            oneWayDeleteMode:
              wizardDirection === "bidirectional"
                ? ""
                : wizardDeleteMode,
          },
        }
      : merged;
    return generatedName && !effective.remark.trim()
      ? { ...effective, remark: generatedName }
      : effective;
  }, [
    form,
    fileSizeDrafts,
    isFreeMode,
    wizardDirection,
    wizardDeleteMode,
    generatedName,
  ]);

  const validationOptions = useMemo(
    () => ({
      storedDstMap: job?.dstMap,
      existingJobs,
      sizeEnabled,
      remotes: nativeStorage.data?.remotes,
      mode: (isFreeMode ? "free" : "wizard") as "free" | "wizard",
    }),
    [
      job?.dstMap,
      existingJobs,
      sizeEnabled,
      nativeStorage.data?.remotes,
      isFreeMode,
    ],
  );
  const validation = useMemo(
    () => validateJobFormFields(mergedForm, validationOptions),
    [mergedForm, validationOptions],
  );

  const inlineCryptErrors = useMemo(
    () =>
      isFreeMode
        ? {}
        : validateInlineCryptDraft(
            inlineCryptDraft,
            wizardRemotePath,
            wizardDirection,
            nativeStorage.data?.remotes,
          ),
    [
      isFreeMode,
      inlineCryptDraft,
      wizardRemotePath,
      wizardDirection,
      nativeStorage.data?.remotes,
    ],
  );

  const inlineCryptStepError = useMemo(() => {
    const errs = Object.values(inlineCryptErrors).filter(Boolean);
    return errs[0] ?? "";
  }, [inlineCryptErrors]);

  const effectiveStepErrors = useMemo<[string, string, string]>(() => {
    if (isFreeMode) {
      return [
        validation.stepErrors[0],
        validation.stepErrors[1] || bwLimitError || "",
        validation.stepErrors[2],
      ];
    }
    const step0 = !wizardNasPath
      ? "请在左侧选择 NAS 本地文件夹"
      : !wizardRemotePath
        ? "请在右侧选择云端存储文件夹"
        : validation.stepErrors[0];
    const step1 = validation.stepErrors[1] || inlineCryptStepError || "";
    const step2 = validation.stepErrors[2] || bwLimitError || "";
    return [step0, step1, step2];
  }, [
    isFreeMode,
    validation.stepErrors,
    bwLimitError,
    wizardNasPath,
    wizardRemotePath,
    inlineCryptStepError,
  ]);

  const markStepValidated = (stepIndex: number) => {
    setValidatedSteps((prev) => {
      if (prev.has(stepIndex)) return prev;
      const next = new Set(prev);
      next.add(stepIndex);
      return next;
    });
  };

  const scrollToFirstError = () => {
    window.requestAnimationFrame(() => {
      const el = scrollRef.current?.querySelector<HTMLElement>(
        ".field.has-error, .field-error, .wizard-picker-card.has-error",
      );
      el?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    });
  };

  const showStepError = (stepIndex: number, key: JobFieldErrorKey): string => {
    const msg = validation.fields[key];
    if (!msg) return "";
    if (validatedSteps.has(stepIndex)) return msg;
    if (key === "fileSizeRange") return msg;
    if (key === "dstPath" && form.method === 3 && form.dstPath.length > 1) {
      return msg;
    }
    if (key === "srcPath" && form.method === 3 && form.srcPath.length > 1) {
      return msg;
    }
    return "";
  };

  const goToStep = (targetStep: number) => {
    if (targetStep <= step) {
      setStep(targetStep);
      return;
    }
    for (let i = 0; i < targetStep; i++) {
      const err = effectiveStepErrors[i];
      if (err) {
        const bwStepIdx = isFreeMode ? 1 : 2;
        if (i === bwStepIdx && (validation.fields.bwLimit || bwLimitError)) {
          setAdvancedOpen(true);
        }
        markStepValidated(i);
        Toast.warning(err);
        setStep(i);
        scrollToFirstError();
        return;
      }
    }
    setStep(targetStep);
  };

  const nextStep = () => {
    const err = effectiveStepErrors[step];
    if (err) {
      const bwStepIdx = isFreeMode ? 1 : 2;
      if (step === bwStepIdx && (validation.fields.bwLimit || bwLimitError)) {
        setAdvancedOpen(true);
      }
      markStepValidated(step);
      Toast.warning(err);
      scrollToFirstError();
      return;
    }
    setStep((s) => Math.min(s + 1, currentTabs.length - 1));
  };

  const previousStep = () => setStep((s) => Math.max(s - 1, 0));

  const applyWizardStateToForm = (
    nextDir: SyncDirectionMode,
    nextDelMode: OneWayDeleteMode,
    nextNas: string,
    nextRemote: string,
    extraPatch?: Partial<JobForm>,
  ) => {
    const endpoints = applyWizardEndpoints(
      nextDir,
      nextDelMode,
      nextNas,
      nextRemote,
    );
    const excludeReset = shouldClearExcludeFoldersOnSrcChange(
      form.srcPath,
      endpoints.srcPath,
      form.exclude,
    );
    const remotes = nativeStorage.data?.remotes;
    const prevMatched = findStandardWebDAVRemotesForPaths(form.dstPath, remotes);
    const shouldAutoCompare =
      (isNew && !initialDraft) ||
      compareModeSource === "auto" ||
      (compareModeSource === "default" && prevMatched.length === 0);
    let nextCompareMode = form.compareMode;
    if (shouldAutoCompare) {
      const resolved = resolveAutoCompareModeForDst({
        dstPaths: endpoints.dstPath,
        remotes,
        currentCompareMode: form.compareMode,
        source: compareModeSource,
      });
      if (resolved.source !== compareModeSource) {
        setCompareModeSource(resolved.source);
      }
      nextCompareMode = resolved.compareMode;
    }

    const nextConflictPolicy =
      nextDir === "bidirectional" && form.conflictPolicy === 0
        ? 2
        : form.conflictPolicy;

    patch({
      ...endpoints,
      conflictPolicy: nextConflictPolicy,
      compareMode: nextCompareMode,
      ...(excludeReset ? { exclude: updateExcludeFolders(form.exclude, []) } : {}),
      extraFlags: {
        ...form.extraFlags,
        syncDirectionMode: nextDir,
        oneWayDeleteMode: nextDir === "bidirectional" ? "" : nextDelMode,
      },
      ...extraPatch,
    });
    if (excludeReset) {
      Toast.info("源目录已更换，已自动清空原有文件夹过滤规则");
    }
  };

  const handleWizardNasSelect = (nextNas: string) => {
    setWizardNasPath(nextNas);
    applyWizardStateToForm(
      wizardDirection,
      wizardDeleteMode,
      nextNas,
      wizardRemotePath,
    );
  };

  const handleWizardRemoteSelect = (nextRemote: string) => {
    setWizardRemotePath(nextRemote);
    if (createdInlineCryptName) setCreatedInlineCryptName("");
    applyWizardStateToForm(
      wizardDirection,
      wizardDeleteMode,
      wizardNasPath,
      nextRemote,
    );
  };

  const handleWizardDirectionChange = (nextDir: SyncDirectionMode) => {
    setWizardDirection(nextDir);
    if (nextDir === "download" && inlineCryptDraft.enabled) {
      setInlineCryptDraft((prev) => ({ ...prev, enabled: false }));
    }
    applyWizardStateToForm(
      nextDir,
      wizardDeleteMode,
      wizardNasPath,
      wizardRemotePath,
    );
  };

  const handleWizardDeleteModeChange = (nextDelMode: OneWayDeleteMode) => {
    setWizardDeleteMode(nextDelMode);
    applyWizardStateToForm(
      wizardDirection,
      nextDelMode,
      wizardNasPath,
      wizardRemotePath,
    );
  };

  const switchToFreeMode = () => {
    setIsFreeMode(true);
    if (step === 2 || activeTab === "plan") {
      setStep(1);
      setActiveTab("schedule");
    } else {
      setStep(0);
      setActiveTab("basic");
    }
    if (isNew && !seedForm.extraFlags.syncDirectionMode) {
      change("extraFlags", {
        ...form.extraFlags,
        syncDirectionMode: "",
        oneWayDeleteMode: "",
      });
    }
  };

  const switchToWizardMode = () => {
    if (!canSwitchBackToWizard(form)) {
      Toast.warning(
        "标准向导仅支持单个「NAS 本地 ↔ 云端存储」目录组合，请先将路径调整为 1 对 1 后再切换",
      );
      return;
    }
    const { nasPath, remotePath } = extractWizardPaths(form);
    const dir = inferWizardDirection(form);
    const del = inferOneWayDeleteMode(form);
    setWizardNasPath(nasPath);
    setWizardRemotePath(remotePath);
    setWizardDirection(dir);
    setWizardDeleteMode(del);
    setIsFreeMode(false);
    setStep(0);
    setActiveTab("folders");
    if (nasPath || remotePath) {
      applyWizardStateToForm(dir, del, nasPath, remotePath);
    }
  };

  const save = () => {
    const invalidStep = effectiveStepErrors.findIndex(Boolean);
    if (invalidStep >= 0) {
      const bwStepIdx = isFreeMode ? 1 : 2;
      if (invalidStep === bwStepIdx && (validation.fields.bwLimit || bwLimitError)) {
        setAdvancedOpen(true);
      }
      setValidatedSteps(new Set([0, 1, 2]));
      Toast.warning(effectiveStepErrors[invalidStep]);
      if (isNew) setStep(invalidStep);
      else setActiveTab(currentTabs[invalidStep].key);
      scrollToFirstError();
      return;
    }

    const persist = () =>
      action.run(async () => {
        try {
          let finalForm = mergedForm;
          let createdCryptNow = false;

          if (!isFreeMode && inlineCryptDraft.enabled && wizardDirection !== "download") {
            let finalRemote = wizardRemotePath;
            const targetInfo = analyzeInlineCryptTarget(
              wizardRemotePath,
              nativeStorage.data?.remotes,
            );
            if (!targetInfo.alreadyEncrypted && targetInfo.supported) {
              if (
                inlineCryptDraft.mode === "reuse" &&
                inlineCryptDraft.reuseEncryptedPath
              ) {
                finalRemote = inlineCryptDraft.reuseEncryptedPath;
              } else {
                const cryptName = inlineCryptDraft.values.name.trim();
                if (createdInlineCryptName !== cryptName) {
                  const options = buildQuickCryptOptions(
                    {
                      remoteName: targetInfo.remoteName,
                      subPath: targetInfo.subPath,
                    },
                    inlineCryptDraft.values,
                    { managedByJob: true },
                  );
                  await api.saveNativeStorage({
                    name: cryptName,
                    options,
                  });
                  setCreatedInlineCryptName(cryptName);
                  createdCryptNow = true;
                  dirCacheRef.current?.invalidateEngine(finalForm.alistId);
                  try {
                    const fresh = await api.nativeStorageState();
                    nativeStorage.setData(fresh);
                  } catch {
                    void nativeStorage.refresh(true);
                  }
                }
                finalRemote = `/${cryptName}`;
              }

              const updatedEndpoints = applyWizardEndpoints(
                wizardDirection,
                wizardDeleteMode,
                wizardNasPath,
                finalRemote,
              );
              finalForm = {
                ...finalForm,
                ...updatedEndpoints,
              };
            }
          }

          const pathsChanged =
            finalForm.alistId !== seedForm.alistId ||
            JSON.stringify(finalForm.srcPath) !==
              JSON.stringify(seedForm.srcPath) ||
            JSON.stringify(finalForm.dstPath) !==
              JSON.stringify(seedForm.dstPath);
          const reviewMirror =
            finalForm.isCron === 3 &&
            finalForm.method === 1 &&
            (!job || job.method !== 1 || pathsChanged);

          const payload = buildJobPayload(
            reviewMirror ? { ...finalForm, enable: false } : finalForm,
          );
          const result = await api.saveJob(payload);
          if (createdCryptNow) {
            onStorageChanged?.();
          }
          setFileSizeDrafts({});
          setDirty(false);
          const createdId =
            isNew && result && typeof result.id === "number" && result.id > 0
              ? result.id
              : undefined;
          Toast.success(
            reviewMirror
              ? "镜像任务已保存，请先预览影响"
              : job
                ? "任务设置已保存"
                : finalForm.enable
                  ? "持续同步任务已创建并开始监控"
                  : "持续同步任务已创建（当前暂停）",
          );
          onSaved({
            createdId,
            mirrorPreviewId: reviewMirror ? createdId ?? job?.id : undefined,
          });
        } catch (err) {
          errorToast(err);
        }
      });

    const pathsChanged =
      mergedForm.alistId !== seedForm.alistId ||
      JSON.stringify(mergedForm.srcPath) !== JSON.stringify(seedForm.srcPath) ||
      JSON.stringify(mergedForm.dstPath) !== JSON.stringify(seedForm.dstPath);

    if (job?.method === 3 && mergedForm.method === 3 && pathsChanged) {
      confirmAction({
        title: "保存新的双向同步目录？",
        content: "下次运行会比对新目录中的现有文件，建立新的双向同步记录。",
        okText: "确认保存",
        onOk: persist,
      });
      return;
    }
    void persist();
  };

  const handleDirectionSelect = (direction: SyncDirectionKey) => {
    if (direction === activeFreeDirection) return;
    if (direction === "bidirectional") {
      patch({
        method: 3,
        conflictPolicy: form.conflictPolicy === 0 ? 2 : form.conflictPolicy,
      });
      return;
    }
    change("method", 0);
  };

  const handleSrcPathChange = (nextPaths: string[]) => {
    if (
      shouldClearExcludeFoldersOnSrcChange(
        form.srcPath,
        nextPaths,
        form.exclude,
      )
    ) {
      patch({
        srcPath: nextPaths,
        exclude: updateExcludeFolders(form.exclude, []),
      });
      Toast.info("源目录已更换，已自动清空原有文件夹过滤规则");
      return;
    }
    change("srcPath", nextPaths);
  };

  const handleDstPathChange = (
    nextPaths: string[],
    remotesOverride?: readonly NativeRemoteItem[],
  ) => {
    const remotes = remotesOverride ?? nativeStorage.data?.remotes;
    const prevMatched = findStandardWebDAVRemotesForPaths(form.dstPath, remotes);
    const shouldAutoResolve =
      (isNew && !initialDraft) ||
      compareModeSource === "auto" ||
      (compareModeSource === "default" && prevMatched.length === 0);
    if (shouldAutoResolve) {
      const resolved = resolveAutoCompareModeForDst({
        dstPaths: nextPaths,
        remotes,
        currentCompareMode: form.compareMode,
        source: compareModeSource,
      });
      if (resolved.source !== compareModeSource) {
        setCompareModeSource(resolved.source);
      }
      if (resolved.compareMode !== form.compareMode) {
        patch({ dstPath: nextPaths, compareMode: resolved.compareMode });
        return;
      }
    }
    change("dstPath", nextPaths);
  };

  const dstEncryption = useMemo(
    () =>
      analyzeDstEncryption(
        form.dstPath,
        nativeStorage.data?.remotes,
        form.method,
      ),
    [form.dstPath, nativeStorage.data?.remotes, form.method],
  );

  const handleQuickCryptReuse = ({
    targetSyncPath,
    encryptedSyncPath,
    cryptName,
  }: {
    targetSyncPath: string;
    encryptedSyncPath: string;
    cryptName: string;
  }) => {
    const nextDstPaths = replaceDstPathWithEncrypted(
      form.dstPath,
      targetSyncPath,
      encryptedSyncPath,
    );
    handleDstPathChange(nextDstPaths);
    setQuickCryptOpen(false);
    Toast.success(`已切换为已有加密存储「${cryptName}」`);
  };

  const handleQuickCryptCreated = async ({
    targetSyncPath,
    encryptedSyncPath,
    cryptName,
  }: {
    targetSyncPath: string;
    encryptedSyncPath: string;
    cryptName: string;
  }) => {
    let nextRemotes: readonly NativeRemoteItem[] | undefined =
      nativeStorage.data?.remotes;
    try {
      const fresh = await api.nativeStorageState();
      nativeStorage.setData(fresh);
      nextRemotes = fresh.remotes;
    } catch {
      void nativeStorage.refresh(true);
    }
    dirCacheRef.current?.invalidateEngine(form.alistId);
    setRemoteTreeEpoch((n) => n + 1);
    const nextDstPaths = replaceDstPathWithEncrypted(
      form.dstPath,
      targetSyncPath,
      encryptedSyncPath,
    );
    handleDstPathChange(nextDstPaths, nextRemotes);
    setQuickCryptOpen(false);
    Toast.success(`已创建加密存储「${cryptName}」并切换为任务目标`);
  };

  useEffect(() => {
    if (
      !isNew ||
      initialDraft ||
      !nativeStorage.data?.remotes?.length ||
      form.dstPath.length === 0
    ) {
      return;
    }
    const resolved = resolveAutoCompareModeForDst({
      dstPaths: form.dstPath,
      remotes: nativeStorage.data.remotes,
      currentCompareMode: form.compareMode,
      source: compareModeSource,
    });
    if (resolved.source !== compareModeSource) {
      setCompareModeSource(resolved.source);
    }
    if (resolved.compareMode !== form.compareMode) {
      setForm((current) =>
        current.compareMode === resolved.compareMode
          ? current
          : { ...current, compareMode: resolved.compareMode },
      );
    }
  }, [
    isNew,
    initialDraft,
    nativeStorage.data?.remotes,
    form.dstPath,
    form.compareMode,
    compareModeSource,
    setForm,
  ]);

  const matchedDstStandardWebDAVNames = useMemo(
    () =>
      findStandardWebDAVRemotesForPaths(
        form.dstPath,
        nativeStorage.data?.remotes,
      ),
    [nativeStorage.data?.remotes, form.dstPath],
  );
  const matchedStandardWebDAVNames = useMemo(
    () =>
      findStandardWebDAVRemotesForPaths(
        [...form.srcPath, ...form.dstPath],
        nativeStorage.data?.remotes,
      ),
    [nativeStorage.data?.remotes, form.srcPath, form.dstPath],
  );
  const matchedQuarkNames = useMemo(
    () =>
      findQuarkRemotesForPaths(
        [...form.srcPath, ...form.dstPath],
        nativeStorage.data?.remotes,
      ),
    [nativeStorage.data?.remotes, form.srcPath, form.dstPath],
  );

  const wizardPathConflict = useMemo(() => {
    if (isFreeMode || !wizardNasPath || !wizardRemotePath) return "";
    const endpoints = applyWizardEndpoints(
      wizardDirection,
      wizardDeleteMode,
      wizardNasPath,
      wizardRemotePath,
    );
    const preview = buildSyncPathPreview({
      srcPaths: endpoints.srcPath,
      dstPaths: endpoints.dstPath,
      storedDstMap: job?.dstMap,
      method: endpoints.method,
      existingJobs,
      currentJobId: job?.id,
      engineId: form.alistId,
    });
    return preview.conflicts[0] || "";
  }, [
    isFreeMode,
    wizardNasPath,
    wizardRemotePath,
    wizardDirection,
    wizardDeleteMode,
    job?.dstMap,
    job?.id,
    existingJobs,
    form.alistId,
  ]);

  const activeFreeDirection = form.method === 3 ? "bidirectional" : "oneway";
  const preferredRoot = preferredRemote ? `/${preferredRemote}` : undefined;
  const refreshFolders = () => {
    dirCacheRef.current?.invalidateEngine(form.alistId);
    setRemoteTreeEpoch((epoch) => epoch + 1);
    void nativeStorage.refresh();
  };
  const legacySchedule = Boolean(job && form.isCron !== 3);
  const srcMultiple = form.method !== 3 || form.srcPath.length > 1;
  const dstMultiple =
    (form.method !== 3 && form.method !== 2) || form.dstPath.length > 1;
  const isDestructiveDirection = form.method !== 0;

  const srcFieldLabel =
    activeFreeDirection === "bidirectional" ? "左侧同步目录（单选）" : "源目录";
  const dstFieldLabel =
    activeFreeDirection === "bidirectional" ? "右侧同步目录（单选）" : "目标目录";

  const directionSummaryText =
    activeFreeDirection === "bidirectional"
      ? "双端文件的新增、修改和删除会互相同步；首次运行时将自动建立双向基线，建议保留「保留双方冲突副本」与双端回收站。"
      : form.method === 2
        ? "此任务会在文件移动到目标目录后删除源文件。"
        : form.method === 1
          ? "镜像会清理目标端独有的文件，包括任务创建前已存在的文件。首次开启镜像时先查看镜像影响预览，再确认启动。"
          : "单向持续增量：源端新增与修改的文件会持续同步至目标端，不会删除目标端已有文件，适合安全备份。";

  const effectivePollSec = form.extraFlags?.syncPollIntervalSec || 60;
  const effectiveAuditMin = form.extraFlags?.syncAuditIntervalMin || 1440;
  const effectiveDebounceSec = form.extraFlags?.watchDebounceSec || 30;

  const activePolicyCount =
    (form.compareMode !== 0 ? 1 : 0) +
    (form.conflictPolicy !== 0 ? 1 : 0) +
    (form.trashEnable ? 1 : 0);

  const runFilterTest = async () => {
    const samplePath = testPath.trim();
    if (!samplePath) {
      Toast.warning("请输入要测试的示例文件路径");
      return;
    }
    setTestingFilter(true);
    try {
      const payload = buildJobPayload(mergedForm);
      const sizeBytes = Math.max(
        0,
        Math.round((Number(testSizeMB) || 0) * 1024 * 1024),
      );
      const ageMinutes = Math.max(0, Math.trunc(Number(testAgeMinutes) || 0));
      const res = await api.testJobFilter({
        exclude: payload.exclude || "",
        include: payload.include || "",
        filterMode: payload.filterMode,
        minFileSize: payload.minFileSize,
        maxFileSize: payload.maxFileSize,
        minFileAgeMinutes: payload.minFileAgeMinutes,
        olderThanDays: payload.olderThanDays,
        newerThanDays: payload.newerThanDays,
        trashEnable: payload.trashEnable,
        trashDir: payload.trashDir,
        samplePath,
        sampleSize: sizeBytes,
        sampleAgeMinutes: ageMinutes,
        sampleIsDir: testIsDir,
      });
      setTestResult(res);
    } catch (err) {
      errorToast(err);
    } finally {
      setTestingFilter(false);
    }
  };

  const excludedFolders = useMemo(
    () => parseExcludeFolders(form.exclude),
    [form.exclude],
  );

  const sizeGroupError =
    showStepError(2, "fileSizeRange") ||
    showStepError(2, "minFileSize") ||
    showStepError(2, "maxFileSize");

  const primaryActionIsNext = isNew && step === 0;
  const step2OptionalBtnLabel = isFreeMode ? "过滤规则" : "同步计划与过滤";

  const nasDisplaySummary = wizardNasPath
    ? formatNasDisplayPath(wizardNasPath, nativeStorage.data?.localRoots)
    : "";

  const wizardDirectionArrow =
    wizardDirection === "bidirectional"
      ? "⇄"
      : wizardDirection === "download"
        ? "←"
        : "→";

  const renderContinuousCadenceControls = (stepIndex: number) => (
    <>
      <div className="continuous-sync-panel">
        {matchedQuarkNames.length > 0 && effectivePollSec < 300 && (
          <div className="sync-method-banner is-warning" role="note">
            <IconAlertTriangle aria-hidden="true" size="small" />
            <span>
              当前任务包含夸克网盘（{matchedQuarkNames.join("、")}
              ），建议将「远端检测间隔」设为 300 秒（5 分钟）及以上，降低触发接口限流（429）的概率。
            </span>
          </div>
        )}
        <Field
          label="远端检测间隔（秒）"
          hint="默认 60 秒（10–86400），用于周期发现远端变更及重启后补齐"
          error={showStepError(stepIndex, "interval")}
        >
          <div className="field-group">
            <Input
              aria-label="远端检测间隔（秒）"
              inputMode="numeric"
              value={pollDraft}
              placeholder="60"
              onChange={(v) => {
                setPollDraft(v);
                const parsed = parsePositiveIntDraft(v);
                change("extraFlags", {
                  ...form.extraFlags,
                  syncPollIntervalSec: parsed,
                });
              }}
              onBlur={() => {
                if (!pollDraft.trim()) {
                  setPollDraft("60");
                  change("extraFlags", {
                    ...form.extraFlags,
                    syncPollIntervalSec: 0,
                  });
                }
              }}
            />
            <div
              className="schedule-chips"
              role="group"
              aria-label="常用远端检测间隔"
            >
              {pollIntervalPresets.map((preset) => {
                const active = effectivePollSec === preset.value;
                return (
                  <button
                    key={preset.value}
                    type="button"
                    className={`schedule-chip${active ? " is-active" : ""}`}
                    onClick={() => {
                      setPollDraft(String(preset.value));
                      change("extraFlags", {
                        ...form.extraFlags,
                        syncPollIntervalSec: preset.value,
                      });
                    }}
                  >
                    每 {preset.label}
                  </button>
                );
              })}
            </div>
          </div>
        </Field>

        <div className="form-grid form-grid--two">
          <Field label="完整核对间隔（分钟）" hint="默认 1440 分钟（1–10080）">
            <div className="field-group">
              <Input
                aria-label="完整核对间隔（分钟）"
                inputMode="numeric"
                value={auditDraft}
                placeholder="1440"
                onChange={(v) => {
                  setAuditDraft(v);
                  const parsed = parsePositiveIntDraft(v);
                  change("extraFlags", {
                    ...form.extraFlags,
                    syncAuditIntervalMin: parsed,
                  });
                }}
                onBlur={() => {
                  if (!auditDraft.trim()) {
                    setAuditDraft("1440");
                    change("extraFlags", {
                      ...form.extraFlags,
                      syncAuditIntervalMin: 0,
                    });
                  }
                }}
              />
              <div
                className="schedule-chips"
                role="group"
                aria-label="常用完整核对间隔"
              >
                {auditIntervalPresets.map((preset) => {
                  const active = effectiveAuditMin === preset.value;
                  return (
                    <button
                      key={preset.value}
                      type="button"
                      className={`schedule-chip${active ? " is-active" : ""}`}
                      onClick={() => {
                        setAuditDraft(String(preset.value));
                        change("extraFlags", {
                          ...form.extraFlags,
                          syncAuditIntervalMin: preset.value,
                        });
                      }}
                    >
                      {preset.label}
                    </button>
                  );
                })}
              </div>
            </div>
          </Field>
          <Field
            label="本地变动防抖（秒）"
            hint="连续写入后静默 2–3600 秒再合并触发，默认 30 秒"
          >
            <Input
              aria-label="本地变动防抖（秒）"
              inputMode="numeric"
              value={debounceDraft}
              placeholder="30"
              onChange={(v) => {
                setDebounceDraft(v);
                const parsed = parsePositiveIntDraft(v);
                change("extraFlags", {
                  ...form.extraFlags,
                  watchDebounceSec: parsed,
                });
              }}
              onBlur={() => {
                if (!debounceDraft.trim()) {
                  setDebounceDraft("30");
                  change("extraFlags", {
                    ...form.extraFlags,
                    watchDebounceSec: 0,
                  });
                }
              }}
            />
          </Field>
        </div>

        <div className="schedule-preview-card" role="status">
          <div className="schedule-preview-plan">
            {formatContinuousSyncPlan({
              pollIntervalSec: effectivePollSec,
              auditIntervalMin: effectiveAuditMin,
              debounceSec: effectiveDebounceSec,
            })}
          </div>
          <div className="muted">
            本地授权目录变更通过实时监听自动捕获；服务重启或离开/恢复运行窗口后会自动对账补齐。
          </div>
        </div>
      </div>
    </>
  );

  const renderWindowAndNotifyControls = (stepIndex: number) => (
    <>
      {!form.extraFlags?.weeklySchedule?.enabled && (
        <>
          <SettingRow
            label="限制允许运行时间窗口"
            variant="bordered"
            tip="开启后，持续同步仅在指定时段内传输（支持跨午夜，如 23:00 至 06:00）；窗口外产生的变更会暂挂，进入窗口后自动补齐。"
          >
            <Switch
              aria-label="限制允许运行时间窗口"
              checked={form.windowEnable}
              onChange={(value) => change("windowEnable", value)}
            />
          </SettingRow>

          {form.windowEnable && (
            <div className="form-section">
              <div className="form-grid form-grid--two">
                <Field
                  label="开始时间 (HH:MM)"
                  error={showStepError(stepIndex, "window")}
                >
                  <Input
                    value={form.windowStart}
                    placeholder="23:00"
                    onChange={(v) => change("windowStart", v)}
                  />
                </Field>
                <Field label="结束时间 (HH:MM)">
                  <Input
                    value={form.windowEnd}
                    placeholder="06:00"
                    onChange={(v) => change("windowEnd", v)}
                  />
                </Field>
              </div>
              <SettingRow
                label="超出时间窗口时自动中止运行中的传输"
                variant="bordered"
                tip="关闭则允许已在窗口内启动的批次传输完毕；开启则在离开窗口后自动中止，待下次窗口恢复续传"
              >
                <Switch
                  aria-label="超出时间窗口时自动中止运行中的传输"
                  checked={form.windowStopOutside}
                  onChange={(v) => change("windowStopOutside", v)}
                />
              </SettingRow>
            </div>
          )}
        </>
      )}

      <Field label="任务通知绑定">
        <Select
          value={form.notifyMode}
          optionList={notifyModeOptions.map((o) => ({
            label: o.label,
            value: o.value,
          }))}
          onChange={(v) => change("notifyMode", Number(v))}
        />
      </Field>

      {form.notifyMode === 1 && (
        <Field
          label="选择通知渠道"
          error={showStepError(stepIndex, "notifyIds")}
          hint="该任务的异常与完成通知仅发送到所选渠道"
        >
          <Select
            multiple
            value={form.notifyIds}
            placeholder={
              notifications.data?.length
                ? "请选择一个或多个通知渠道"
                : "暂无可用通知渠道，请先在通知配置页面添加"
            }
            optionList={(notifications.data || []).map((n) => ({
              value: n.id,
              label: `#${n.id} ${channelNames[n.method] || "未知渠道"} · ${getNotifyEndpointSummary(n)}`,
            }))}
            onChange={(vals) =>
              change(
                "notifyIds",
                Array.isArray(vals)
                  ? vals.map(Number).filter(Number.isFinite)
                  : [],
              )
            }
          />
        </Field>
      )}
    </>
  );

  const renderFilterCoreFields = (collapseTester: boolean) => (
    <>
      <Field
        label="过滤模式"
        hint="排除模式会同步除黑名单外的所有文件；白名单模式仅同步命中包含规则且未被排除的文件"
      >
        <Select
          value={form.filterMode}
          optionList={filterModeOptions.map((o) => ({
            label: o.label,
            value: o.value,
          }))}
          onChange={(v) => change("filterMode", Number(v))}
        />
      </Field>

      {form.filterMode === 1 && (
        <Field
          label="白名单包含规则"
          required
          error={showStepError(2, "include")}
          hint="每行一条文件名或后缀规则（如 *.jpg、*.mp4、README.md）；目录过滤与系统目录排除依然生效"
        >
          <TextArea
            autosize={{ minRows: 3, maxRows: 8 }}
            value={form.include}
            placeholder={"*.jpg\n*.png\n*.mp4"}
            onChange={(v) => change("include", v)}
          />
        </Field>
      )}

      <div className="filter-section-block">
        <div className="filter-section-title">文件大小过滤</div>
        <div
          className="file-size-filters"
          role="group"
          aria-label="文件大小过滤"
        >
          {(["min", "max"] as const).map((kind) => (
            <FileSizeFilterRow
              key={kind}
              kind={kind}
              enabled={sizeEnabled[kind]}
              onToggle={(checked) => {
                const config = fileSizeFilterDefaults[kind];
                const valueKey = `${kind}FileSize` as const;
                const unitKey = `${kind}FileSizeUnit` as const;
                const currentVal = parseFileSizeInput(form[valueKey]) ?? 0;
                setSizeEnabled((prev) => ({
                  ...prev,
                  [kind]: checked,
                }));
                setFileSizeDrafts((prev) => {
                  if (prev[valueKey] === undefined) return prev;
                  const next = { ...prev };
                  delete next[valueKey];
                  return next;
                });
                patch({
                  [valueKey]: checked
                    ? currentVal > 0
                      ? currentVal
                      : config.value
                    : 0,
                  [unitKey]: checked
                    ? form[unitKey] || config.unit
                    : config.unit,
                });
              }}
              form={form}
              change={change}
              drafts={fileSizeDrafts}
              setDrafts={setFileSizeDrafts}
            />
          ))}
        </div>
        {sizeGroupError && (
          <div className="field-error" role="alert">
            {sizeGroupError}
          </div>
        )}
      </div>

      {!collapseTester && (
        <div className="filter-section-block">
          <div className="filter-section-title">文件时间过滤</div>
          <div className="form-grid form-grid--three">
            <Field
              label="最小稳定时间（分钟）"
              hint="跳过刚修改的文件，避免同步未写完的大文件（0 关闭）"
            >
              <Input
                inputMode="numeric"
                value={String(form.minFileAgeMinutes)}
                onChange={(v) =>
                  change(
                    "minFileAgeMinutes",
                    Math.max(0, Math.trunc(Number(v) || 0)),
                  )
                }
              />
            </Field>
            <Field
              label="仅同步早于（天）"
              hint="冷数据归档：只同步 N 天前修改的文件（0 不限）"
            >
              <Input
                inputMode="numeric"
                value={String(form.olderThanDays)}
                onChange={(v) =>
                  change(
                    "olderThanDays",
                    Math.max(0, Math.trunc(Number(v) || 0)),
                  )
                }
              />
            </Field>
            <Field
              label="仅同步最近（天）"
              hint="只同步最近 N 天内修改的文件（0 不限）"
            >
              <Input
                inputMode="numeric"
                value={String(form.newerThanDays)}
                onChange={(v) =>
                  change(
                    "newerThanDays",
                    Math.max(0, Math.trunc(Number(v) || 0)),
                  )
                }
              />
            </Field>
          </div>
        </div>
      )}

      <Field
        label="文件过滤"
        hint="仅支持文件名和文件后缀；目录请在下方「文件夹过滤」中配置"
      >
        <FileTypeFilter
          value={form.exclude}
          onChange={(value) => change("exclude", value)}
        />
      </Field>

      <Field
        label="文件夹过滤"
        hint="仅针对源目录，勾选要忽略的子文件夹；单层目录名（如 cache/）会匹配所有源目录下的同名目录"
      >
        <div className="field-group" role="group">
          <ExcludeTree
            engineId={form.alistId}
            srcPaths={form.srcPath}
            excludedPaths={excludedFolders}
            onChange={(folders) =>
              change("exclude", updateExcludeFolders(form.exclude, folders))
            }
          />
        </div>
      </Field>

      <Field label="系统目录过滤" hint="忽略操作系统和 NAS 的系统目录">
        <div className="system-dir-groups" role="group">
          {systemDirFilterGroups.map((group) => {
            const selected = group.patterns.filter((p) =>
              isExcludePatternEnabled(form.exclude, p),
            );
            const allSelected = selected.length === group.patterns.length;
            return (
              <div className="system-dir-group" key={group.key}>
                <Checkbox
                  checked={allSelected}
                  indeterminate={selected.length > 0 && !allSelected}
                  onChange={(event) =>
                    change(
                      "exclude",
                      updateExcludePatterns(
                        form.exclude,
                        group.patterns,
                        Boolean(event.target.checked),
                      ),
                    )
                  }
                >
                  {group.label}
                </Checkbox>
                <span className="system-dir-patterns">
                  {group.patterns.join(" · ")}
                </span>
              </div>
            );
          })}
        </div>
      </Field>

      {collapseTester ? (
        <div className="advanced-options">
          <button
            type="button"
            className={`advanced-options-toggle${filterTesterOpen ? " is-open" : ""}`}
            aria-expanded={filterTesterOpen}
            onClick={() => setFilterTesterOpen((o) => !o)}
          >
            <IconChevronRight
              className="advanced-options-chevron"
              aria-hidden="true"
            />
            <span>规则实时测试器与文件时间过滤</span>
          </button>
          {filterTesterOpen && (
            <div className="advanced-options-body">
              <div className="filter-section-block">
                <div className="filter-section-title">文件时间过滤</div>
                <div className="form-grid form-grid--three">
                  <Field
                    label="最小稳定时间（分钟）"
                    hint="跳过刚修改的文件，避免同步未写完的大文件（0 关闭）"
                  >
                    <Input
                      inputMode="numeric"
                      value={String(form.minFileAgeMinutes)}
                      onChange={(v) =>
                        change(
                          "minFileAgeMinutes",
                          Math.max(0, Math.trunc(Number(v) || 0)),
                        )
                      }
                    />
                  </Field>
                  <Field
                    label="仅同步早于（天）"
                    hint="冷数据归档：只同步 N 天前修改的文件（0 不限）"
                  >
                    <Input
                      inputMode="numeric"
                      value={String(form.olderThanDays)}
                      onChange={(v) =>
                        change(
                          "olderThanDays",
                          Math.max(0, Math.trunc(Number(v) || 0)),
                        )
                      }
                    />
                  </Field>
                  <Field
                    label="仅同步最近（天）"
                    hint="只同步最近 N 天内修改的文件（0 不限）"
                  >
                    <Input
                      inputMode="numeric"
                      value={String(form.newerThanDays)}
                      onChange={(v) =>
                        change(
                          "newerThanDays",
                          Math.max(0, Math.trunc(Number(v) || 0)),
                        )
                      }
                    />
                  </Field>
                </div>
              </div>

              <div className="filter-tester">
                <div className="filter-tester-head">
                  <strong>规则实时测试器</strong>
                  <span className="muted">
                    输入示例相对路径和属性，立即验证是否会被当前过滤规则同步
                  </span>
                </div>
                <div className="filter-tester-grid">
                  <Input
                    aria-label="示例相对路径"
                    placeholder="如 2026/photo.jpg 或 cache/tmp.log"
                    value={testPath}
                    onChange={setTestPath}
                  />
                  <Input
                    aria-label="示例文件大小 MB"
                    inputMode="decimal"
                    suffix="MB"
                    value={testSizeMB}
                    onChange={setTestSizeMB}
                  />
                  <Input
                    aria-label="距上次修改分钟数"
                    inputMode="numeric"
                    suffix="分钟前"
                    value={testAgeMinutes}
                    onChange={setTestAgeMinutes}
                  />
                  <Button
                    type="primary"
                    theme="light"
                    loading={testingFilter}
                    onClick={() => void runFilterTest()}
                  >
                    测试匹配
                  </Button>
                </div>
                <div className="filter-tester-foot">
                  <Checkbox
                    checked={testIsDir}
                    onChange={(e) => setTestIsDir(Boolean(e.target.checked))}
                  >
                    作为目录测试
                  </Checkbox>
                  {testResult && (
                    <div
                      className={`filter-tester-result${testResult.willSync ? " is-sync" : " is-skip"}`}
                      role="status"
                    >
                      {testResult.willSync ? (
                        <IconTickCircle aria-hidden="true" size="small" />
                      ) : (
                        <IconAlertTriangle aria-hidden="true" size="small" />
                      )}
                      <span>
                        {testResult.willSync ? "将会同步" : "不会同步"}：
                        {testResult.reason}
                      </span>
                    </div>
                  )}
                </div>
              </div>
            </div>
          )}
        </div>
      ) : (
        <div className="filter-tester">
          <div className="filter-tester-head">
            <strong>规则实时测试器</strong>
            <span className="muted">
              输入示例相对路径和属性，立即验证是否会被当前过滤规则同步
            </span>
          </div>
          <div className="filter-tester-grid">
            <Input
              aria-label="示例相对路径"
              placeholder="如 2026/photo.jpg 或 cache/tmp.log"
              value={testPath}
              onChange={setTestPath}
            />
            <Input
              aria-label="示例文件大小 MB"
              inputMode="decimal"
              suffix="MB"
              value={testSizeMB}
              onChange={setTestSizeMB}
            />
            <Input
              aria-label="距上次修改分钟数"
              inputMode="numeric"
              suffix="分钟前"
              value={testAgeMinutes}
              onChange={setTestAgeMinutes}
            />
            <Button
              type="primary"
              theme="light"
              loading={testingFilter}
              onClick={() => void runFilterTest()}
            >
              测试匹配
            </Button>
          </div>
          <div className="filter-tester-foot">
            <Checkbox
              checked={testIsDir}
              onChange={(e) => setTestIsDir(Boolean(e.target.checked))}
            >
              作为目录测试
            </Checkbox>
            {testResult && (
              <div
                className={`filter-tester-result${testResult.willSync ? " is-sync" : " is-skip"}`}
                role="status"
              >
                {testResult.willSync ? (
                  <IconTickCircle aria-hidden="true" size="small" />
                ) : (
                  <IconAlertTriangle aria-hidden="true" size="small" />
                )}
                <span>
                  {testResult.willSync ? "将会同步" : "不会同步"}：
                  {testResult.reason}
                </span>
              </div>
            )}
          </div>
        </div>
      )}
    </>
  );

  return (
    <RemoteDirCacheProvider cache={dirCacheRef.current}>
      <Editor
        title={
          job
            ? legacySchedule
              ? "编辑同步任务"
              : "编辑持续同步任务"
            : initialDraft
              ? "复制同步任务"
              : "新建持续同步任务"
        }
        visible={!addingStorage}
        busy={action.busy}
        onClose={onClose}
        onSave={save}
        dirty={effectiveDirty}
        width={isFreeMode ? 620 : 760}
        className="task-editor-modal"
        footer={({ close }) => (
          <div className="editor-actions">
            {isNew && step > 0 && (
              <Button
                type="tertiary"
                className="editor-actions-start"
                onClick={previousStep}
                disabled={action.busy}
              >
                上一步
              </Button>
            )}
            <Button type="tertiary" onClick={close} disabled={action.busy}>
              取消
            </Button>
            {isNew && step === 1 && (
              <Button
                type="primary"
                theme="light"
                onClick={nextStep}
                disabled={action.busy}
              >
                {step2OptionalBtnLabel}
              </Button>
            )}
            <Button
              type="primary"
              theme="solid"
              loading={action.busy}
              onClick={primaryActionIsNext ? nextStep : save}
            >
              {primaryActionIsNext ? "下一步" : "保存"}
            </Button>
          </div>
        )}
      >
        <Form
          onSubmit={primaryActionIsNext ? nextStep : save}
          className="editor-form task-editor-form"
        >
          <fieldset disabled={action.busy}>
            {isNew ? (
              <div className="task-editor-nav task-editor-nav--steps">
                <div className="task-editor-stepbar">
                  <h3>{currentTabs[step].label}</h3>
                  <div className="task-editor-count" aria-label="当前步骤">
                    步骤 <strong>{step + 1}</strong> / {currentTabs.length}
                  </div>
                </div>
                <nav
                  className="task-editor-stepper"
                  aria-label="任务配置步骤"
                >
                  {currentTabs.map((tab, index) => {
                    const isCurrent = index === step;
                    const isDone =
                      index < step && !effectiveStepErrors[index];
                    return (
                      <button
                        key={tab.key}
                        type="button"
                        className={`task-step-btn${isCurrent ? " is-active" : ""}${isDone ? " is-done" : ""}`}
                        aria-current={isCurrent ? "step" : undefined}
                        onClick={() => goToStep(index)}
                      >
                        <span className="task-step-num" aria-hidden="true">
                          {isDone ? <IconTick size="extra-small" /> : index + 1}
                        </span>
                        <span className="task-step-label">{tab.label}</span>
                        {tab.optional && (
                          <span className="task-step-optional">可选</span>
                        )}
                      </button>
                    );
                  })}
                </nav>
                <div className="task-editor-progress" aria-hidden="true">
                  <span
                    style={{
                      width: `${((step + 1) / currentTabs.length) * 100}%`,
                    }}
                  />
                </div>
              </div>
            ) : (
              <div className="task-editor-nav">
                <Tabs
                  type="line"
                  activeKey={activeTab}
                  onChange={(key) => setActiveTab(key)}
                >
                  {currentTabs.map((tab) => (
                    <Tabs.TabPane
                      tab={tab.label}
                      itemKey={tab.key}
                      key={tab.key}
                    />
                  ))}
                </Tabs>
              </div>
            )}

            <div className="task-editor-step" ref={scrollRef}>
              {/* ==================== STANDARD WIZARD MODE ==================== */}
              {!isFreeMode && panelMounted("folders") && (
                <div className="form-section" style={panelStyle("folders")}>
                  <div className="wizard-mode-bar">
                    <span className="muted">
                      标准向导 · 面向最常见的「NAS 本地文件夹 ↔ 远程网盘文件夹」1 对 1 同步
                    </span>
                    <Button
                      size="small"
                      type="tertiary"
                      onClick={switchToFreeMode}
                    >
                      切换为自由多对多 / 云对云模式
                    </Button>
                  </div>

                  {enginesError && (
                    <InlineNotice
                      role="alert"
                      message={`存储配置加载失败：${enginesError}`}
                      onRetry={onRetryEngines}
                    />
                  )}

                  {!enginesError && !form.alistId && (
                    <div className="engine-empty-guide" role="status">
                      <IconInfoCircle aria-hidden="true" size="small" />
                      <span>
                        内置存储尚未就绪，请先前往存储管理检查本地目录或远程存储配置。
                      </span>
                      {onNavigateEngines && (
                        <Button
                          size="small"
                          type="primary"
                          theme="light"
                          onClick={onNavigateEngines}
                        >
                          前往存储管理
                        </Button>
                      )}
                    </div>
                  )}

                  <div className="wizard-folder-split">
                    <NasLocalFolderPicker
                      engineId={form.alistId}
                      localRoots={nativeStorage.data?.localRoots}
                      value={wizardNasPath}
                      onChange={handleWizardNasSelect}
                      onRefresh={refreshFolders}
                      error={
                        validatedSteps.has(0) && !wizardNasPath
                          ? "请选择 NAS 本地文件夹"
                          : wizardDirection !== "download"
                            ? showStepError(0, "srcPath")
                            : showStepError(0, "dstPath")
                      }
                      epoch={remoteTreeEpoch}
                    />

                    <CloudRemoteFolderPicker
                      engineId={form.alistId}
                      remotes={nativeStorage.data?.remotes}
                      preferredRemote={preferredRemote}
                      value={wizardRemotePath}
                      onChange={handleWizardRemoteSelect}
                      onAddStorage={() => setAddingStorage(true)}
                      onRefresh={refreshFolders}
                      error={
                        validatedSteps.has(0) && !wizardRemotePath
                          ? "请选择云端存储文件夹"
                          : wizardDirection === "download"
                            ? showStepError(0, "srcPath")
                            : showStepError(0, "dstPath")
                      }
                      epoch={remoteTreeEpoch}
                    />
                  </div>

                  {(nasDisplaySummary || wizardRemotePath) && (
                    <div className="wizard-pair-summary" role="status">
                      <span className="wizard-pair-endpoint">
                        <strong>NAS</strong> {nasDisplaySummary || "未选择"}
                      </span>
                      <span className="wizard-pair-arrow" aria-hidden="true">
                        {wizardDirectionArrow}
                      </span>
                      <span className="wizard-pair-endpoint">
                        <strong>云端</strong> {wizardRemotePath || "未选择"}
                      </span>
                    </div>
                  )}

                  {wizardPathConflict && (
                    <div className="sync-method-banner is-warning" role="alert">
                      <IconAlertTriangle aria-hidden="true" size="small" />
                      <span>{wizardPathConflict}</span>
                    </div>
                  )}

                  {matchedStandardWebDAVNames.length > 0 && (
                    <div
                      className="sync-method-banner webdav-compare-tip"
                      role="status"
                    >
                      <IconInfoCircle aria-hidden="true" size="small" />
                      <span>
                        已选择标准 WebDAV（{matchedStandardWebDAVNames.join("、")}
                        ），已默认启用「仅按文件大小」比对，避免大流量远端哈希校验。
                      </span>
                    </div>
                  )}

                  <Field
                    label="任务名称"
                    required
                    error={showStepError(0, "remark")}
                    fieldId="remark"
                  >
                    <Input
                      value={form.remark}
                      onChange={(value) => change("remark", value)}
                      placeholder={
                        generatedName || "例如：Photos · PhotoCloud"
                      }
                    />
                  </Field>
                </div>
              )}

              {!isFreeMode && panelMounted("safety") && (
                <div className="form-section" style={panelStyle("safety")}>
                  <div className="wizard-route-pill" role="status">
                    <span>NAS {nasDisplaySummary || "未选择"}</span>
                    <span className="wizard-pair-arrow" aria-hidden="true">
                      {wizardDirectionArrow}
                    </span>
                    <span>云端 {wizardRemotePath || "未选择"}</span>
                  </div>

                  <Field label="同步方向" ariaLabel="同步方向">
                    <div
                      className="wizard-direction-grid"
                      role="radiogroup"
                      aria-label="同步方向"
                    >
                      {wizardDirectionCards.map((card) => {
                        const selected = wizardDirection === card.key;
                        return (
                          <button
                            key={card.key}
                            type="button"
                            role="radio"
                            aria-checked={selected}
                            className={`sync-direction-card${selected ? " is-selected" : ""}`}
                            onClick={() =>
                              handleWizardDirectionChange(card.key)
                            }
                          >
                            <div className="sync-direction-card-head">
                              <span className="sync-direction-card-icon">
                                <card.icon aria-hidden="true" />
                              </span>
                              <span className="sync-direction-card-title">
                                {card.title}
                              </span>
                              <span className="sync-direction-card-route">
                                {card.route}
                              </span>
                            </div>
                            <div className="sync-direction-card-desc">
                              {card.desc}
                            </div>
                          </button>
                        );
                      })}
                    </div>
                  </Field>

                  {wizardDirection !== "bidirectional" && (
                    <Field label="目标端删除策略" ariaLabel="单向删除策略">
                      <div className="field-group">
                        <div
                          className="delete-policy-list"
                          role="radiogroup"
                          aria-label="单向删除策略"
                        >
                          {oneWayDeleteModeCards.map((opt) => {
                            const selected = wizardDeleteMode === opt.key;
                            return (
                              <button
                                key={opt.key}
                                type="button"
                                role="radio"
                                aria-checked={selected}
                                className={`delete-policy-card${selected ? " is-selected" : ""}${opt.danger ? " is-danger" : ""}`}
                                onClick={() =>
                                  handleWizardDeleteModeChange(opt.key)
                                }
                              >
                                <div className="delete-policy-head">
                                  <span
                                    className={`delete-policy-radio${selected ? " is-checked" : ""}`}
                                    aria-hidden="true"
                                  />
                                  <span className="delete-policy-title">
                                    {opt.title}
                                  </span>
                                  {opt.badge && (
                                    <Tag size="small" color="green" type="light">
                                      {opt.badge}
                                    </Tag>
                                  )}
                                </div>
                                <div className="delete-policy-desc">
                                  {opt.desc}
                                </div>
                              </button>
                            );
                          })}
                        </div>
                        {wizardDeleteMode === "mirror" && (
                          <div
                            className="sync-method-banner is-warning"
                            role="status"
                          >
                            <IconAlertTriangle aria-hidden="true" size="small" />
                            <span>
                              镜像模式会清理目标端多余的已有文件。首次保存或修改路径后将处于待确认状态，先通过模拟运行预览清理清单再启动。
                            </span>
                          </div>
                        )}
                      </div>
                    </Field>
                  )}

                  <InlineCryptPanel
                    draft={inlineCryptDraft}
                    onChange={(nextDraft) => {
                      if (
                        nextDraft.values.name !== inlineCryptDraft.values.name
                      ) {
                        inlineCryptDirtyNameRef.current = true;
                      }
                      setInlineCryptDraft(nextDraft);
                    }}
                    remotePath={wizardRemotePath}
                    direction={wizardDirection}
                    remotes={nativeStorage.data?.remotes}
                    showErrors={validatedSteps.has(1)}
                    createdCryptName={createdInlineCryptName}
                  />

                  <div className="wizard-safety-card">
                    <SettingRow
                      label="启用删除回收站保护"
                      variant="bordered"
                      tip="开启后，删除或覆盖目标文件前先移至独立回收站子目录，防止误删或勒索同步覆盖后无法恢复"
                    >
                      <Switch
                        aria-label="启用删除回收站保护"
                        checked={form.trashEnable}
                        onChange={(v) => change("trashEnable", v)}
                      />
                    </SettingRow>
                    {form.trashEnable && (
                      <div className="form-grid form-grid--two wizard-trash-fields">
                        <Field
                          label="回收站目录名"
                          error={showStepError(1, "trashDir")}
                          hint="自动从同步扫描中排除"
                        >
                          <Input
                            value={form.trashDir}
                            placeholder=".opensync-trash"
                            onChange={(v) => change("trashDir", v)}
                          />
                        </Field>
                        <Field
                          label="回收站保留天数"
                          hint="0 表示永久保留，不自动清理"
                        >
                          <Input
                            inputMode="numeric"
                            value={String(form.trashRetentionDays)}
                            placeholder="30"
                            onChange={(v) =>
                              change(
                                "trashRetentionDays",
                                Math.max(0, Math.trunc(Number(v) || 0)),
                              )
                            }
                          />
                        </Field>
                      </div>
                    )}
                  </div>

                  <div className="form-grid form-grid--two">
                    <Field label="文件比对方式">
                      <Select
                        value={form.compareMode}
                        optionList={compareModeOptions.map((o) => ({
                          label: o.label,
                          value: o.value,
                        }))}
                        onChange={(v) => {
                          setCompareModeSource("manual");
                          change("compareMode", Number(v));
                        }}
                      />
                    </Field>
                    <Field label="同名冲突处理">
                      <Select
                        value={form.conflictPolicy}
                        optionList={conflictPolicyOptions.map((o) => ({
                          label:
                            wizardDirection === "bidirectional"
                              ? [
                                  "保留较新版本（另一版本删除）",
                                  "保留源端版本（目标版本删除）",
                                  "保留双方冲突副本（推荐）",
                                ][o.value]
                              : o.label,
                          value: o.value,
                        }))}
                        onChange={(v) => change("conflictPolicy", Number(v))}
                      />
                    </Field>
                  </div>
                </div>
              )}

              {!isFreeMode && panelMounted("plan") && (
                <div className="form-section" style={panelStyle("plan")}>
                  <SettingRow
                    label="启用持续同步"
                    variant="bordered"
                    tip="开启后按下方实时或周历计划运行；关闭则创建后先保持暂停状态。"
                  >
                    <Switch
                      aria-label="启用持续同步"
                      checked={form.enable}
                      onChange={(value) => change("enable", value)}
                    />
                  </SettingRow>

                  <Field
                    label="同步运行计划（7×24 周历调度）"
                    error={showStepError(2, "weeklySchedule")}
                  >
                    <WeeklyScheduleGrid
                      value={form.extraFlags?.weeklySchedule}
                      onChange={(ws) =>
                        change("extraFlags", {
                          ...form.extraFlags,
                          weeklySchedule: ws,
                        })
                      }
                      error={showStepError(2, "weeklySchedule")}
                    />
                  </Field>

                  <div className="advanced-options">
                    <button
                      type="button"
                      className={`advanced-options-toggle${scheduleCadenceOpen ? " is-open" : ""}`}
                      aria-expanded={scheduleCadenceOpen}
                      onClick={() => setScheduleCadenceOpen((o) => !o)}
                    >
                      <IconChevronRight
                        className="advanced-options-chevron"
                        aria-hidden="true"
                      />
                      <span>轮询频率、防抖与任务通知绑定</span>
                      {(form.notifyMode === 1 ||
                        form.extraFlags?.syncPollIntervalSec ||
                        form.extraFlags?.syncAuditIntervalMin) && (
                        <Tag size="small" color="blue" type="light">
                          已自定义
                        </Tag>
                      )}
                    </button>
                    {scheduleCadenceOpen && (
                      <div className="advanced-options-body">
                        {renderContinuousCadenceControls(2)}
                        {renderWindowAndNotifyControls(2)}
                      </div>
                    )}
                  </div>

                  <div className="wizard-filter-section">
                    <h4 className="wizard-section-heading">过滤规则（可选）</h4>
                    {renderFilterCoreFields(true)}
                  </div>

                  <ExpertTuningDrawer
                    form={form}
                    open={advancedOpen}
                    onToggleOpen={() => setAdvancedOpen((o) => !o)}
                    change={change}
                    setForm={setForm}
                    setDirty={setDirty}
                    setCompareModeSource={setCompareModeSource}
                    bwLimitError={bwLimitError}
                    setBwLimitError={setBwLimitError}
                    bwLimitFieldError={showStepError(2, "bwLimit")}
                    systemDefaultBwLimit={
                      systemSettings.data?.defaultBwLimit || ""
                    }
                    existingJobs={existingJobs}
                    showPresetsOutside
                  />
                </div>
              )}

              {/* ==================== FREE / MULTI-PATH MODE ==================== */}
              {isFreeMode && panelMounted("basic") && (
                <div className="form-section" style={panelStyle("basic")}>
                  <div className="wizard-mode-bar">
                    <span className="muted">
                      高级自由模式 · 支持多源多目标、云对云及本地对本地路径自由组合
                    </span>
                    <Button
                      size="small"
                      type="tertiary"
                      onClick={switchToWizardMode}
                    >
                      返回标准向导模式
                    </Button>
                  </div>

                  <Field
                    label="任务名称"
                    required
                    error={showStepError(0, "remark")}
                    fieldId="remark"
                  >
                    <Input
                      value={form.remark}
                      onChange={(value) => change("remark", value)}
                      placeholder={
                        generatedName || "例如：相册持续同步到夸克网盘"
                      }
                    />
                  </Field>

                  {enginesError && (
                    <InlineNotice
                      role="alert"
                      message={`存储配置加载失败：${enginesError}`}
                      onRetry={onRetryEngines}
                    />
                  )}

                  {!enginesError && !form.alistId && (
                    <div className="engine-empty-guide" role="status">
                      <IconInfoCircle aria-hidden="true" size="small" />
                      <span>
                        内置存储尚未就绪，请先前往存储管理检查本地目录或远程存储配置。
                      </span>
                      {onNavigateEngines && (
                        <Button
                          size="small"
                          type="primary"
                          theme="light"
                          onClick={onNavigateEngines}
                        >
                          前往存储管理
                        </Button>
                      )}
                    </div>
                  )}

                  {isNew && (
                    <div className="field-group">
                      <Button
                        size="small"
                        theme="light"
                        onClick={() => setAddingStorage(true)}
                      >
                        添加网盘连接
                      </Button>
                      {preferredRemote && (
                        <span className="muted">优先浏览 {preferredRemote}</span>
                      )}
                      {nativeStorage.data &&
                        !nativeStorage.data.localRoots?.length && (
                          <InlineNotice
                            role="status"
                            message="需要同步 NAS 目录时，请在飞牛「应用设置 → 文件夹访问权限」中授权后刷新目录。"
                            action={
                              <Button size="small" onClick={refreshFolders}>
                                刷新目录
                              </Button>
                            }
                          />
                        )}
                    </div>
                  )}

                  <Field label="同步方向" ariaLabel="同步方向">
                    <div className="field-group">
                      <div
                        className="sync-direction-grid"
                        role="radiogroup"
                        aria-label="同步方向"
                      >
                        {freeDirectionCards.map((card) => {
                          const selected = activeFreeDirection === card.key;
                          return (
                            <button
                              key={card.key}
                              type="button"
                              role="radio"
                              aria-checked={selected}
                              className={`sync-direction-card${selected ? " is-selected" : ""}`}
                              onClick={() => handleDirectionSelect(card.key)}
                            >
                              <div className="sync-direction-card-head">
                                <span className="sync-direction-card-icon">
                                  <card.icon aria-hidden="true" />
                                </span>
                                <span className="sync-direction-card-title">
                                  {card.title}
                                </span>
                                <span className="sync-direction-card-route">
                                  {card.route}
                                </span>
                              </div>
                              <div className="sync-direction-card-desc">
                                {card.desc}
                              </div>
                            </button>
                          );
                        })}
                      </div>
                      <div
                        className={`sync-method-banner${isDestructiveDirection ? " is-warning" : ""}`}
                        role={isDestructiveDirection ? "status" : undefined}
                      >
                        {isDestructiveDirection ? (
                          <IconAlertTriangle aria-hidden="true" size="small" />
                        ) : (
                          <IconInfoCircle aria-hidden="true" size="small" />
                        )}
                        <span>{directionSummaryText}</span>
                      </div>
                    </div>
                  </Field>

                  {activeFreeDirection !== "bidirectional" &&
                    form.method !== 2 &&
                    !legacySchedule && (
                      <SettingRow
                        label="镜像对齐"
                        variant="bordered"
                        tip="开启后，目标端独有的文件会被清理，沿用删除保护与回收站。关闭时保留这些文件。"
                      >
                        <Switch
                          aria-label="镜像对齐"
                          checked={form.method === 1}
                          onChange={(v) => change("method", v ? 1 : 0)}
                        />
                      </SettingRow>
                    )}

                  <div className="form-grid">
                    <Field
                      label={srcFieldLabel}
                      required
                      error={showStepError(0, "srcPath")}
                      fieldId="srcPath"
                    >
                      <RemotePaths
                        key={`src-${form.alistId}-${srcMultiple ? "m" : "s"}-${remoteTreeEpoch}`}
                        engineId={form.alistId}
                        value={form.srcPath}
                        onChange={handleSrcPathChange}
                        multiple={srcMultiple}
                        initialExpandedRoot={
                          nativeStorage.data?.localRoots?.length
                            ? "/本地"
                            : undefined
                        }
                        remotes={nativeStorage.data?.remotes}
                        ariaLabel="选择源目录"
                      />
                    </Field>
                    <Field
                      label={dstFieldLabel}
                      required
                      error={showStepError(0, "dstPath")}
                      fieldId="dstPath"
                    >
                      <RemotePaths
                        key={`dst-${form.alistId}-${dstMultiple ? "m" : "s"}-${remoteTreeEpoch}`}
                        engineId={form.alistId}
                        value={form.dstPath}
                        onChange={handleDstPathChange}
                        multiple={dstMultiple}
                        initialExpandedRoot={preferredRoot}
                        remotes={nativeStorage.data?.remotes}
                        ariaLabel="选择目标目录"
                      />
                    </Field>
                  </div>

                  <Field
                    label="手动输入路径"
                    hint="支持本地↔本地、本地↔远端、远端↔远端任意组合；仅有单桶权限或目录尚未创建时可直接输入路径。"
                  >
                    <div className="field-group">
                      <Input
                        value={nativeTarget}
                        onChange={setNativeTarget}
                        placeholder="/存储名称/存储桶/子目录"
                      />
                      <div className="native-section-actions">
                        <Button
                          type="tertiary"
                          disabled={!nativeTarget.trim()}
                          onClick={() => {
                            const check = validateManualSyncPath(
                              nativeTarget,
                              nativeStorage.data?.remotes,
                              { role: "src", method: form.method },
                            );
                            if (check.error) {
                              Toast.warning(check.error);
                              return;
                            }
                            handleSrcPathChange(
                              form.method === 3
                                ? [check.normalized]
                                : Array.from(
                                    new Set([
                                      ...form.srcPath,
                                      check.normalized,
                                    ]),
                                  ),
                            );
                            setNativeTarget("");
                          }}
                        >
                          添加到源路径
                        </Button>
                        <Button
                          disabled={!nativeTarget.trim()}
                          onClick={() => {
                            const check = validateManualSyncPath(
                              nativeTarget,
                              nativeStorage.data?.remotes,
                              { role: "dst", method: form.method },
                            );
                            if (check.error) {
                              Toast.warning(check.error);
                              return;
                            }
                            handleDstPathChange(
                              form.method === 3
                                ? [check.normalized]
                                : Array.from(
                                    new Set([
                                      ...form.dstPath,
                                      check.normalized,
                                    ]),
                                  ),
                            );
                            setNativeTarget("");
                          }}
                        >
                          添加到目标路径
                        </Button>
                      </div>
                    </div>
                  </Field>

                  {matchedDstStandardWebDAVNames.length > 0 &&
                    form.compareMode === 1 && (
                      <div
                        className="sync-method-banner webdav-compare-tip"
                        role="status"
                      >
                        <IconInfoCircle aria-hidden="true" size="small" />
                        <span>
                          目标存储为标准 WebDAV（
                          {matchedDstStandardWebDAVNames.join("、")}
                          ，如 OpenList / AList），已默认使用「
                          <strong>仅按文件大小</strong>
                          」比对，避免拉取大量远端资源到本地校验。
                        </span>
                      </div>
                    )}
                  {matchedStandardWebDAVNames.length > 0 &&
                    form.compareMode === 2 && (
                      <div
                        className="sync-method-banner is-warning webdav-compare-tip"
                        role="status"
                      >
                        <IconAlertTriangle aria-hidden="true" size="small" />
                        <span>
                          已选择标准 WebDAV 存储（
                          {matchedStandardWebDAVNames.join("、")}
                          ，如 OpenList / AList）。当前「大小 + MD5」比对方式会拉取远端文件到本地校验并消耗大量流量，建议切换为{" "}
                          <strong>仅按文件大小</strong>。
                        </span>
                        <Button
                          size="small"
                          type="primary"
                          theme="light"
                          onClick={() => {
                            setCompareModeSource("manual");
                            change("compareMode", 1);
                            setPolicyOpen(true);
                            Toast.success("已将文件比对方式设为「仅大小」");
                          }}
                        >
                          设为仅大小
                        </Button>
                      </div>
                    )}
                  {matchedStandardWebDAVNames.length > 0 &&
                    form.compareMode === 0 && (
                      <div
                        className="sync-method-banner webdav-compare-tip"
                        role="status"
                      >
                        <IconInfoCircle aria-hidden="true" size="small" />
                        <span>
                          已选择标准 WebDAV 存储（
                          {matchedStandardWebDAVNames.join("、")}
                          ，如 OpenList / AList）。执行时默认比对会自动按{" "}
                          <strong>仅大小</strong>{" "}
                          比对以避免拉取远端资源到本地，也可显式设为{" "}
                          <strong>仅按文件大小</strong>。
                        </span>
                        <Button
                          size="small"
                          type="primary"
                          theme="light"
                          onClick={() => {
                            setCompareModeSource("manual");
                            change("compareMode", 1);
                            setPolicyOpen(true);
                            Toast.success("已将文件比对方式设为「仅大小」");
                          }}
                        >
                          设为仅大小
                        </Button>
                      </div>
                    )}
                  {dstEncryption.encryptable.length > 0 && (
                    <div
                      className="sync-method-banner quick-crypt-banner"
                      role="status"
                    >
                      <IconLockStroked aria-hidden="true" size="small" />
                      <span>
                        {dstEncryption.encryptable.length === 1 ? (
                          <>
                            目标目录{" "}
                            <code>{dstEncryption.encryptable[0].syncPath}</code>{" "}
                            当前以明文存储在远端。可快捷创建独立 Crypt 加密存储并自动切换为加密目标。
                          </>
                        ) : (
                          <>
                            已选 {dstEncryption.encryptable.length}{" "}
                            个未加密远端目标目录。可快捷创建独立 Crypt 加密存储并自动切换为加密目标。
                          </>
                        )}
                      </span>
                      <Button
                        size="small"
                        type="primary"
                        theme="light"
                        icon={<IconLockStroked aria-hidden="true" />}
                        onClick={() => setQuickCryptOpen(true)}
                      >
                        启用端到端加密
                      </Button>
                    </div>
                  )}
                  {dstEncryption.encryptable.length === 0 &&
                    dstEncryption.encrypted.length > 0 && (
                      <div
                        className="sync-method-banner is-success quick-crypt-banner"
                        role="status"
                      >
                        <IconLockStroked aria-hidden="true" size="small" />
                        <span>
                          已启用端到端加密（目标加密存储：
                          <strong>
                            {Array.from(
                              new Set(
                                dstEncryption.encrypted.map(
                                  (item) => item.remoteName,
                                ),
                              ),
                            ).join("、")}
                          </strong>
                          ），文件将在上传前于本地加密。
                        </span>
                      </div>
                    )}
                  <SyncPathPreview
                    srcPaths={form.srcPath}
                    dstPaths={form.dstPath}
                    storedDstMap={job?.dstMap}
                    method={form.method}
                    existingJobs={existingJobs}
                    currentJobId={job?.id}
                    engineId={form.alistId}
                    onKeepFirstSrc={
                      form.method === 3 && form.srcPath.length > 1
                        ? () => handleSrcPathChange(form.srcPath.slice(0, 1))
                        : undefined
                    }
                    onKeepFirstDst={
                      form.method === 3 && form.dstPath.length > 1
                        ? () => handleDstPathChange(form.dstPath.slice(0, 1))
                        : undefined
                    }
                  />

                  <div className="advanced-options">
                    <button
                      type="button"
                      className={`advanced-options-toggle${policyOpen ? " is-open" : ""}`}
                      aria-expanded={policyOpen}
                      onClick={() => setPolicyOpen((open) => !open)}
                    >
                      <IconChevronRight
                        className="advanced-options-chevron"
                        aria-hidden="true"
                      />
                      <span>同步策略与回收站</span>
                      {activePolicyCount > 0 && (
                        <Tag size="small" color="blue" type="light">
                          已定制 {activePolicyCount} 项
                        </Tag>
                      )}
                    </button>
                    {policyOpen && (
                      <div className="advanced-options-body">
                        <Field label="文件比对方式">
                          <Select
                            value={form.compareMode}
                            optionList={compareModeOptions.map((o) => ({
                              label: o.label,
                              value: o.value,
                            }))}
                            onChange={(v) => {
                              setCompareModeSource("manual");
                              change("compareMode", Number(v));
                            }}
                          />
                        </Field>
                        <Field label="同名冲突处理">
                          <Select
                            value={form.conflictPolicy}
                            optionList={conflictPolicyOptions.map((o) => ({
                              label:
                                form.method === 3
                                  ? [
                                      "保留较新版本（另一版本删除）",
                                      "保留源端版本（目标版本删除）",
                                      "保留双方冲突副本（推荐）",
                                    ][o.value]
                                  : o.label,
                              value: o.value,
                            }))}
                            onChange={(v) => change("conflictPolicy", Number(v))}
                          />
                        </Field>
                        <SettingRow
                          label={
                            form.method === 3
                              ? "启用双端回收站（软删除）"
                              : "启用目标端回收站（软删除）"
                          }
                          variant="bordered"
                          tip={
                            form.method === 3
                              ? "开启后，双向同步删除或覆盖旧文件时先移至对应端的回收站子目录，避免误删不可恢复（选择「保留双方副本」时不触发覆盖备份）"
                              : "开启后，同步删除或覆盖旧文件时先移至目标目录下的回收站子目录，避免误删不可恢复"
                          }
                        >
                          <Switch
                            aria-label="启用目标端回收站"
                            checked={form.trashEnable}
                            onChange={(v) => change("trashEnable", v)}
                          />
                        </SettingRow>
                        {form.trashEnable && (
                          <div className="form-grid form-grid--two">
                            <Field
                              label="回收站目录名"
                              error={showStepError(0, "trashDir")}
                              hint="自动从同步扫描中排除"
                            >
                              <Input
                                value={form.trashDir}
                                placeholder=".opensync-trash"
                                onChange={(v) => change("trashDir", v)}
                              />
                            </Field>
                            <Field
                              label="回收站保留天数"
                              hint="0 表示永久保留，不自动清理"
                            >
                              <Input
                                inputMode="numeric"
                                value={String(form.trashRetentionDays)}
                                placeholder="30"
                                onChange={(v) =>
                                  change(
                                    "trashRetentionDays",
                                    Math.max(0, Math.trunc(Number(v) || 0)),
                                  )
                                }
                              />
                            </Field>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                </div>
              )}

              {isFreeMode && panelMounted("schedule") && (
                <div className="form-section" style={panelStyle("schedule")}>
                  {form.isCron !== 2 && (
                    <SettingRow
                      label={legacySchedule ? "启用任务" : "启用持续同步"}
                      variant="bordered"
                      tip={
                        legacySchedule
                          ? "按现有执行计划运行。"
                          : "保存后开始监听并检查两端目录。"
                      }
                    >
                      <Switch
                        aria-label={legacySchedule ? "启用任务" : "启用持续同步"}
                        checked={form.enable}
                        onChange={(value) => change("enable", value)}
                      />
                    </SettingRow>
                  )}

                  {legacySchedule ? (
                    <InlineNotice
                      message={
                        form.isCron === 2
                          ? "此任务按原配置手动执行。"
                          : "此任务按原配置定时执行。"
                      }
                    />
                  ) : (
                    <>
                      {renderContinuousCadenceControls(1)}
                      <Field
                        label="周历时段限速 / 暂停调度（可选）"
                        error={showStepError(1, "weeklySchedule")}
                      >
                        <WeeklyScheduleGrid
                          value={form.extraFlags?.weeklySchedule}
                          onChange={(ws) =>
                            change("extraFlags", {
                              ...form.extraFlags,
                              weeklySchedule: ws,
                            })
                          }
                          error={showStepError(1, "weeklySchedule")}
                        />
                      </Field>
                    </>
                  )}

                  {renderWindowAndNotifyControls(1)}

                  <ExpertTuningDrawer
                    form={form}
                    open={advancedOpen}
                    onToggleOpen={() => setAdvancedOpen((o) => !o)}
                    change={change}
                    setForm={setForm}
                    setDirty={setDirty}
                    setCompareModeSource={setCompareModeSource}
                    bwLimitError={bwLimitError}
                    setBwLimitError={setBwLimitError}
                    bwLimitFieldError={showStepError(1, "bwLimit")}
                    systemDefaultBwLimit={
                      systemSettings.data?.defaultBwLimit || ""
                    }
                    existingJobs={existingJobs}
                  />
                </div>
              )}

              {isFreeMode && panelMounted("filter") && (
                <div className="form-section" style={panelStyle("filter")}>
                  {renderFilterCoreFields(false)}
                </div>
              )}
            </div>
          </fieldset>
        </Form>
      </Editor>
      {addingStorage && (
        <NativeStorageEditor
          mode="form"
          onClose={() => setAddingStorage(false)}
          onSaved={(savedName) => {
            setAddingStorage(false);
            if (savedName) setPreferredRemote(savedName);
            refreshFolders();
            onStorageChanged?.();
          }}
        />
      )}
      {quickCryptOpen && dstEncryption.encryptable.length > 0 && (
        <QuickCryptModal
          targets={dstEncryption.encryptable}
          remotes={nativeStorage.data?.remotes || []}
          isEditingJob={!isNew}
          onClose={() => setQuickCryptOpen(false)}
          onReuseExisting={handleQuickCryptReuse}
          onCreated={handleQuickCryptCreated}
        />
      )}
    </RemoteDirCacheProvider>
  );
}

function FileSizeFilterRow({
  kind,
  enabled,
  onToggle,
  form,
  change,
  drafts,
  setDrafts,
}: {
  kind: "min" | "max";
  enabled: boolean;
  onToggle: (checked: boolean) => void;
  form: JobForm;
  change: <K extends keyof JobForm>(key: K, value: JobForm[K]) => void;
  drafts: FileSizeDrafts;
  setDrafts: Dispatch<SetStateAction<FileSizeDrafts>>;
}) {
  const config = fileSizeFilterDefaults[kind];
  const valueKey = `${kind}FileSize` as const;
  const unitKey = `${kind}FileSizeUnit` as const;
  const draft = drafts[valueKey] ?? null;
  const editing = draft !== null;
  const displayValue = editing
    ? draft
    : enabled
      ? String(form[valueKey])
      : String(config.value);
  const displayUnit = enabled ? form[unitKey] : config.unit;
  const controlLabel = `${config.label}文件大小`;
  const unitLabelId = useId();

  const clearDraft = () =>
    setDrafts((prev) => {
      if (prev[valueKey] === undefined) return prev;
      const next = { ...prev };
      delete next[valueKey];
      return next;
    });

  const commitInput = (raw: string) => {
    clearDraft();
    if (raw.trim() === "") {
      change(valueKey, "");
      return;
    }
    change(valueKey, commitFileSizeInput(raw));
  };

  return (
    <div className={`file-size-filter-row${enabled ? "" : " is-disabled"}`}>
      <Checkbox
        aria-label={`启用${config.label}指定大小的文件`}
        checked={enabled}
        onChange={(event) => onToggle(Boolean(event.target.checked))}
      />
      <span className="file-size-filter-copy">{config.label}</span>
      <Input
        aria-label={controlLabel}
        inputMode="decimal"
        disabled={!enabled}
        value={displayValue}
        onChange={(value) => {
          setDrafts((prev) => ({ ...prev, [valueKey]: value }));
        }}
        onBlur={(event) =>
          commitInput((event.target as HTMLInputElement).value ?? "")
        }
      />
      <span id={unitLabelId} className="sr-only">
        {controlLabel}单位
      </span>
      <Select
        aria-labelledby={unitLabelId}
        disabled={!enabled}
        value={displayUnit}
        optionList={fileSizeUnitOptions}
        onChange={(value) =>
          change(unitKey, String(value) as JobForm[typeof unitKey])
        }
      />
      <span className="file-size-filter-copy">的文件</span>
    </div>
  );
}
