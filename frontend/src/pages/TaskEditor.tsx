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
import { BwLimitEditor } from "../components/BwLimitEditor";
import { ExcludeTree } from "../components/ExcludeTree";
import { FileTypeFilter } from "../components/FileTypeFilter";
import { QuickCryptModal } from "../components/QuickCryptModal";
import { RemotePaths } from "../components/RemotePaths";
import { SyncPathPreview } from "../components/SyncPathPreview";
import {
  formatContinuousSyncPlan,
  type SyncDirectionKey,
} from "../components/SyncStatus";
import { useAction, useEditorForm, useResource } from "../lib/hooks";
import { channelNames, getNotifyEndpointSummary } from "../lib/notifyForm";
import {
  analyzeDstEncryption,
  replaceDstPathWithEncrypted,
} from "../lib/quickCrypt";
import {
  createRemoteDirCache,
  RemoteDirCacheProvider,
  type RemoteDirCache,
} from "../lib/remoteDirCache";
import {
  applyTaskScenarioPreset,
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
  taskScenarioPresets,
  updateExcludeFolders,
  updateExcludePatterns,
  validateJobFormFields,
  validateManualSyncPath,
  type CompareModeSource,
  type JobFieldErrorKey,
  type JobForm,
} from "../lib/taskForm";
import { formatDisplayPath } from "../lib/syncPathPreview";
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
  NativeRemoteItem,
} from "../types";

export { parseIntervalInput };
export { hasPendingFileSizeDraftChange, mergeFileSizeDrafts };

export interface TaskEditorSavedResult {
  createdId?: number;
  mirrorPreviewId?: number;
}

const taskEditorTabs = [
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

const directionCards = [
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
  const defaultEngineId = engines[0]?.id || nativeStorage.data?.engineId || undefined;
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
  const { form, dirty, change, patch, setForm, setDirty } =
    useEditorForm<JobForm>(() => seedForm);
  useEffect(() => {
    if (!form.alistId && defaultEngineId) {
      setForm((current) =>
        current.alistId ? current : { ...current, alistId: defaultEngineId },
      );
    }
  }, [defaultEngineId, form.alistId, setForm]);
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

  // Draft strings for continuous sync numeric inputs so users can clear & retype cleanly
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
    hasPendingFileSizeDraftChange(form, fileSizeDrafts);

  const [policyOpen, setPolicyOpen] = useState<boolean>(() =>
    Boolean(
      seedForm.compareMode ||
        seedForm.conflictPolicy ||
        seedForm.trashEnable,
    ),
  );
  const [compareModeSource, setCompareModeSource] = useState<CompareModeSource>(
    () => (seedForm.compareMode === 0 ? "default" : "manual"),
  );
  const [advancedOpen, setAdvancedOpen] = useState<boolean>(() =>
    Boolean(
      seedForm.copyConcurrency ||
        seedForm.scanConcurrency ||
        seedForm.maxRetries >= 0 ||
        seedForm.bwLimit ||
        seedForm.tpsLimit ||
        seedForm.tpsLimitBurst ||
        seedForm.modifyWindow ||
        seedForm.fastList ||
        seedForm.multiThreadStreams ||
        seedForm.createEmptySrcDirs ||
        seedForm.followSymlinks ||
        seedForm.extraFlags?.noUpdateModtime ||
        seedForm.extraFlags?.inplace ||
        seedForm.extraFlags?.checkFirst ||
        seedForm.extraFlags?.s3NoHead ||
        seedForm.extraFlags?.ignoreSize ||
        seedForm.extraFlags?.trackRenames ||
        seedForm.extraFlags?.useMmap ||
        seedForm.extraFlags?.maxTransfer ||
        seedForm.extraFlags?.cutoffMode ||
        seedForm.extraFlags?.maxDuration ||
        seedForm.extraFlags?.orderBy ||
        Number(seedForm.extraFlags?.maxBacklog || 0) > 0 ||
        seedForm.extraFlags?.bufferSize ||
        seedForm.extraFlags?.multiThreadCutoff ||
        seedForm.extraFlags?.multiThreadChunkSize ||
        seedForm.extraFlags?.userAgent ||
        seedForm.extraFlags?.ioTimeout ||
        seedForm.extraFlags?.conTimeout ||
        Number(seedForm.extraFlags?.lowLevelRetries || 0) > 0 ||
        seedForm.extraFlags?.retriesSleep ||
        seedForm.extraFlags?.resumeEnable ||
        Number(seedForm.extraFlags?.autoRetryMax || 0) > 0 ||
        Number(seedForm.extraFlags?.postSyncJobId || 0) > 0 ||
        seedForm.extraFlags?.checkSchedule ||
        seedForm.extraFlags?.directStream ||
        Number(seedForm.extraFlags?.bisyncMaxDelete || 0) > 0 ||
        seedForm.extraFlags?.bisyncForce,
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
  const [step, setStep] = useState(0);
  const [activeTab, setActiveTab] = useState<string>("basic");
  const [validatedSteps, setValidatedSteps] = useState<Set<number>>(
    () => new Set(),
  );
  const action = useAction();
  const activePanel = isNew ? taskEditorTabs[step].key : activeTab;

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
  }, [activePanel]);

  const source = form.srcPath[0];
  const target = form.dstPath[0];
  const [sourceRoot, targetRoot] = [source, target].map(
    (path) => path?.split("/").filter(Boolean)[0],
  );
  const localPath = sourceRoot === "本地" ? source : targetRoot === "本地" ? target : undefined;
  const remoteRoot = sourceRoot === "本地" ? targetRoot : sourceRoot;
  const generatedName =
    isNew && !initialDraft && source && target && localPath && remoteRoot && remoteRoot !== "本地"
      ? `${formatDisplayPath(localPath).split("/").filter(Boolean).at(-1)} · ${remoteRoot}`
      : "";
  const mergedForm = useMemo(() => {
    const merged = mergeFileSizeDrafts(form, fileSizeDrafts);
    return generatedName && !merged.remark.trim() ? { ...merged, remark: generatedName } : merged;
  }, [form, fileSizeDrafts, generatedName]);
  const validationOptions = useMemo(
    () => ({
      storedDstMap: job?.dstMap,
      existingJobs,
      sizeEnabled,
      remotes: nativeStorage.data?.remotes,
    }),
    [job?.dstMap, existingJobs, sizeEnabled, nativeStorage.data?.remotes],
  );
  const validation = useMemo(
    () => validateJobFormFields(mergedForm, validationOptions),
    [mergedForm, validationOptions],
  );

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
      const target = scrollRef.current?.querySelector<HTMLElement>(
        ".field.has-error, .field-error",
      );
      target?.scrollIntoView({ block: "nearest", behavior: "smooth" });
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
      const err =
        validation.stepErrors[i] || (i === 1 && bwLimitError ? bwLimitError : "");
      if (err) {
        if (i === 1 && (validation.fields.bwLimit || bwLimitError)) {
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
    const err =
      validation.stepErrors[step] ||
      (step === 1 && bwLimitError ? bwLimitError : "");
    if (err) {
      if (step === 1 && (validation.fields.bwLimit || bwLimitError)) {
        setAdvancedOpen(true);
      }
      markStepValidated(step);
      Toast.warning(err);
      scrollToFirstError();
      return;
    }
    setStep((s) => Math.min(s + 1, taskEditorTabs.length - 1));
  };

  const previousStep = () => setStep((s) => Math.max(s - 1, 0));

  const save = () => {
    const stepErrorsWithBw = [
      validation.stepErrors[0],
      validation.stepErrors[1] || bwLimitError || "",
      validation.stepErrors[2],
    ];
    const invalidStep = stepErrorsWithBw.findIndex(Boolean);
    if (invalidStep >= 0) {
      if (invalidStep === 1 && (validation.fields.bwLimit || bwLimitError)) {
        setAdvancedOpen(true);
      }
      setValidatedSteps(new Set([0, 1, 2]));
      Toast.warning(stepErrorsWithBw[invalidStep]);
      if (isNew) setStep(invalidStep);
      else setActiveTab(taskEditorTabs[invalidStep].key);
      scrollToFirstError();
      return;
    }
    const pathsChanged =
      mergedForm.alistId !== seedForm.alistId ||
      JSON.stringify(mergedForm.srcPath) !== JSON.stringify(seedForm.srcPath) ||
      JSON.stringify(mergedForm.dstPath) !== JSON.stringify(seedForm.dstPath);
    const reviewMirror = mergedForm.isCron === 3 && mergedForm.method === 1 &&
      (!job || job.method !== 1 || pathsChanged);
    const payload = buildJobPayload(reviewMirror ? { ...mergedForm, enable: false } : mergedForm);
    const persist = () => action.run(async () => {
      try {
        const result = await api.saveJob(payload);
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
              : mergedForm.enable
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
    if (direction === activeDirection) return;
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
    if (shouldClearExcludeFoldersOnSrcChange(form.srcPath, nextPaths, form.exclude)) {
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

  const activeDirection = form.method === 3 ? "bidirectional" : "oneway";
  const preferredRoot = preferredRemote ? `/${preferredRemote}` : undefined;
  const refreshFolders = () => {
    dirCacheRef.current?.invalidateEngine(form.alistId);
    setRemoteTreeEpoch((epoch) => epoch + 1);
    void nativeStorage.refresh();
  };
  const legacySchedule = Boolean(job && form.isCron !== 3);
  const srcMultiple = form.method !== 3 || form.srcPath.length > 1;
  const dstMultiple = (form.method !== 3 && form.method !== 2) || form.dstPath.length > 1;
  const isDestructiveDirection = form.method !== 0;

  const srcFieldLabel =
    activeDirection === "bidirectional" ? "左侧同步目录（单选）" : "源目录";
  const dstFieldLabel =
    activeDirection === "bidirectional" ? "右侧同步目录（单选）" : "目标目录";

  const directionSummaryText =
    activeDirection === "bidirectional"
      ? "双端文件的新增、修改和删除会互相同步；首次运行时将自动建立双向基线，建议保留「保留双方冲突副本」与双端回收站。"
      : form.method === 2
        ? "此任务会在文件移动到目标目录后删除源文件。"
        : form.method === 1
        ? "镜像会清理目标端独有的文件，包括任务创建前已存在的文件。首次开启镜像时先查看镜像影响预览，再确认启动。"
        : "单向持续增量：源端新增与修改的文件会持续同步至目标端，不会删除目标端已有文件，适合安全备份。";

  const effectivePollSec = form.extraFlags?.syncPollIntervalSec || 60;
  const effectiveAuditMin = form.extraFlags?.syncAuditIntervalMin || 1440;
  const effectiveDebounceSec = form.extraFlags?.watchDebounceSec || 30;

  const activeAdvancedCount =
    (form.bwLimit.trim() ? 1 : 0) +
    (Number(form.tpsLimit || 0) > 0 ? 1 : 0) +
    (Number(form.tpsLimitBurst || 0) > 0 ? 1 : 0) +
    (form.modifyWindow.trim() ? 1 : 0) +
    (form.fastList ? 1 : 0) +
    (Number(form.multiThreadStreams || 0) > 0 ? 1 : 0) +
    (form.createEmptySrcDirs ? 1 : 0) +
    (form.followSymlinks ? 1 : 0) +
    (form.extraFlags?.noUpdateModtime ? 1 : 0) +
    (form.extraFlags?.inplace ? 1 : 0) +
    (form.extraFlags?.checkFirst ? 1 : 0) +
    (form.extraFlags?.s3NoHead ? 1 : 0) +
    (form.extraFlags?.ignoreSize ? 1 : 0) +
    (form.extraFlags?.trackRenames ? 1 : 0) +
    (form.extraFlags?.useMmap ? 1 : 0) +
    (form.extraFlags?.maxTransfer?.trim() ? 1 : 0) +
    (form.extraFlags?.cutoffMode?.trim() ? 1 : 0) +
    (form.extraFlags?.maxDuration?.trim() ? 1 : 0) +
    (form.extraFlags?.orderBy?.trim() ? 1 : 0) +
    (Number(form.extraFlags?.maxBacklog || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.bufferSize?.trim() ? 1 : 0) +
    (form.extraFlags?.multiThreadCutoff?.trim() ? 1 : 0) +
    (form.extraFlags?.multiThreadChunkSize?.trim() ? 1 : 0) +
    (form.extraFlags?.userAgent?.trim() ? 1 : 0) +
    (form.extraFlags?.ioTimeout?.trim() ? 1 : 0) +
    (form.extraFlags?.conTimeout?.trim() ? 1 : 0) +
    (Number(form.extraFlags?.lowLevelRetries || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.retriesSleep?.trim() ? 1 : 0) +
    (form.extraFlags?.resumeEnable ? 1 : 0) +
    (Number(form.extraFlags?.autoRetryMax || 0) > 0 ? 1 : 0) +
    (Number(form.extraFlags?.postSyncJobId || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.checkSchedule?.trim() ? 1 : 0) +
    (form.extraFlags?.directStream ? 1 : 0) +
    (Number(form.extraFlags?.bisyncMaxDelete || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.bisyncForce ? 1 : 0) +
    (Number(form.copyConcurrency || 0) > 0 ? 1 : 0) +
    (Number(form.scanConcurrency || 0) > 0 ? 1 : 0) +
    (Number(form.maxRetries) >= 0 ? 1 : 0);

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

  return (
    <RemoteDirCacheProvider cache={dirCacheRef.current}>
      <Editor
        title={job ? legacySchedule ? "编辑同步任务" : "编辑持续同步任务" : initialDraft ? "复制同步任务" : "新建持续同步任务"}
        visible={!addingStorage}
        busy={action.busy}
        onClose={onClose}
        onSave={save}
        dirty={effectiveDirty}
        width={620}
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
                过滤规则
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
                  <h3>{taskEditorTabs[step].label}</h3>
                  <div className="task-editor-count" aria-label="当前步骤">
                    步骤 <strong>{step + 1}</strong> / {taskEditorTabs.length}
                  </div>
                </div>
                <nav
                  className="task-editor-stepper"
                  aria-label="任务配置步骤"
                >
                  {taskEditorTabs.map((tab, index) => {
                    const isCurrent = index === step;
                    const isDone =
                      index < step && !validation.stepErrors[index];
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
                      width: `${((step + 1) / taskEditorTabs.length) * 100}%`,
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
                  {taskEditorTabs.map((tab) => (
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
              {panelMounted("basic") && (
                <div className="form-section" style={panelStyle("basic")}>
                  <Field
                    label="任务名称"
                    required
                    error={showStepError(0, "remark")}
                    fieldId="remark"
                  >
                    <Input
                      value={form.remark}
                      onChange={(value) => change("remark", value)}
                      placeholder={generatedName || "例如：相册持续同步到夸克网盘"}
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
                      {preferredRemote && <span className="muted">优先浏览 {preferredRemote}</span>}
                      {nativeStorage.data && !nativeStorage.data.localRoots?.length && (
                        <InlineNotice
                          role="status"
                          message="需要同步 NAS 目录时，请在飞牛「应用设置 → 文件夹访问权限」中授权后刷新目录。"
                          action={<Button size="small" onClick={refreshFolders}>刷新目录</Button>}
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
                        {directionCards.map((card) => {
                          const selected = activeDirection === card.key;
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

                  {activeDirection !== "bidirectional" && form.method !== 2 && !legacySchedule && (
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
                        initialExpandedRoot={nativeStorage.data?.localRoots?.length ? "/本地" : undefined}
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
                                : Array.from(new Set([...form.srcPath, check.normalized])),
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
                                : Array.from(new Set([...form.dstPath, check.normalized])),
                            );
                            setNativeTarget("");
                          }}
                        >
                          添加到目标路径
                        </Button>
                      </div>
                    </div>
                  </Field>

                  {matchedDstStandardWebDAVNames.length > 0 && form.compareMode === 1 && (
                    <div className="sync-method-banner webdav-compare-tip" role="status">
                      <IconInfoCircle aria-hidden="true" size="small" />
                      <span>
                        目标存储为标准 WebDAV（{matchedDstStandardWebDAVNames.join("、")}，如 OpenList / AList），已默认使用「<strong>仅按文件大小</strong>」比对，避免拉取大量远端资源到本地校验。
                      </span>
                    </div>
                  )}
                  {matchedStandardWebDAVNames.length > 0 && form.compareMode === 2 && (
                    <div className="sync-method-banner is-warning webdav-compare-tip" role="status">
                      <IconAlertTriangle aria-hidden="true" size="small" />
                      <span>
                        已选择标准 WebDAV 存储（{matchedStandardWebDAVNames.join("、")}，如 OpenList / AList）。当前「大小 + MD5」比对方式会拉取远端文件到本地校验并消耗大量流量，建议切换为 <strong>仅按文件大小</strong>。
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
                  {matchedStandardWebDAVNames.length > 0 && form.compareMode === 0 && (
                    <div className="sync-method-banner webdav-compare-tip" role="status">
                      <IconInfoCircle aria-hidden="true" size="small" />
                      <span>
                        已选择标准 WebDAV 存储（{matchedStandardWebDAVNames.join("、")}，如 OpenList / AList）。执行时默认比对会自动按 <strong>仅大小</strong> 比对以避免拉取远端资源到本地，也可显式设为 <strong>仅按文件大小</strong>。
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
                    <div className="sync-method-banner quick-crypt-banner" role="status">
                      <IconLockStroked aria-hidden="true" size="small" />
                      <span>
                        {dstEncryption.encryptable.length === 1 ? (
                          <>
                            目标目录 <code>{dstEncryption.encryptable[0].syncPath}</code>{" "}
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
                                dstEncryption.encrypted.map((item) => item.remoteName),
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

              {panelMounted("schedule") && (
                <div className="form-section" style={panelStyle("schedule")}>
                  {form.isCron !== 2 && <SettingRow
                    label={legacySchedule ? "启用任务" : "启用持续同步"}
                    variant="bordered"
                    tip={legacySchedule ? "按现有执行计划运行。" : "保存后开始监听并检查两端目录。"}
                  >
                    <Switch
                      aria-label={legacySchedule ? "启用任务" : "启用持续同步"}
                      checked={form.enable}
                      onChange={(value) => change("enable", value)}
                    />
                  </SettingRow>}

                  {legacySchedule ? (
                    <InlineNotice message={form.isCron === 2 ? "此任务按原配置手动执行。" : "此任务按原配置定时执行。"} />
                  ) : <><div className="continuous-sync-panel">
                    {matchedQuarkNames.length > 0 && effectivePollSec < 300 && (
                      <div className="sync-method-banner is-warning" role="note">
                        <IconAlertTriangle aria-hidden="true" size="small" />
                        <span>
                          当前任务包含夸克网盘（{matchedQuarkNames.join("、")}），建议将「远端检测间隔」设为 300 秒（5 分钟）及以上，降低触发接口限流（429）的概率。
                        </span>
                      </div>
                    )}
                    <Field
                      label="远端检测间隔（秒）"
                      hint="默认 60 秒（10–86400），用于周期发现远端变更及重启后补齐"
                      error={showStepError(1, "interval")}
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
                      <Field
                        label="完整核对间隔（分钟）"
                        hint="默认 1440 分钟（每天一次），全面核对双端一致性"
                      >
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
                        label="变动合并等待（秒）"
                        hint="默认 30 秒（2–3600），连续写入停止后再触发同步"
                      >
                        <Input
                          aria-label="变动合并等待（秒）"
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
                  </div>

                  <div
                    className={`schedule-preview-card${validation.fields.interval ? " is-invalid" : ""}`}
                    aria-live="polite"
                  >
                    {validation.fields.interval ? (
                      <div className="schedule-preview-error" role="alert">
                        <IconAlertTriangle aria-hidden="true" size="small" />
                        <span>{validation.fields.interval}</span>
                      </div>
                    ) : (
                      <>
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
                      </>
                    )}
                  </div>

                  </>}
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
                          error={showStepError(1, "window")}
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
                      error={showStepError(1, "notifyIds")}
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

                  <div className="advanced-options">
                    <button
                      type="button"
                      className={`advanced-options-toggle${advancedOpen ? " is-open" : ""}`}
                      aria-expanded={advancedOpen}
                      onClick={() => setAdvancedOpen((open) => !open)}
                    >
                      <IconChevronRight
                        className="advanced-options-chevron"
                        aria-hidden="true"
                      />
                      <span>高级选项（并发、限速、流控与 rclone 调优）</span>
                      {activeAdvancedCount > 0 && (
                        <Tag size="small" color="blue" type="light">
                          已配置 {activeAdvancedCount} 项
                        </Tag>
                      )}
                    </button>
                    {advancedOpen && (
                      <div className="advanced-options-body">
                        <div className="advanced-option-group">
                          <div className="advanced-option-group-title">场景预设与传输增强</div>
                          <Field
                            label="一键场景预设"
                            hint="点击自动填入推荐的断点续传、并发与重试参数，应用后仍可按需微调"
                          >
                            <div className="schedule-chips">
                              {taskScenarioPresets.map((preset) => (
                                <button
                                  key={preset.key}
                                  type="button"
                                  className="schedule-chip"
                                  title={preset.desc}
                                  onClick={() => {
                                    if (preset.key === "cold_archive") {
                                      setCompareModeSource("manual");
                                    }
                                    setForm((prev) =>
                                      applyTaskScenarioPreset(prev, preset.key),
                                    );
                                    setDirty(true);
                                    Toast.success(`已应用「${preset.label}」推荐配置`);
                                  }}
                                >
                                  {preset.label}
                                </button>
                              ))}
                            </div>
                          </Field>
                          <SettingRow
                            label="开启大文件断点续传"
                            variant="bordered"
                            tip="支持远端到本地下载（分片续传）与本地到 S3 分片上传续传；中断后再次运行或重试将从已传字节/分片继续"
                          >
                            <Switch
                              aria-label="开启大文件断点续传"
                              checked={Boolean(form.extraFlags?.resumeEnable)}
                              onChange={(v) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  resumeEnable: v,
                                })
                              }
                            />
                          </SettingRow>
                          {form.extraFlags?.resumeEnable && (
                            <Field
                              label="断点续传最小文件阈值（MB）"
                              hint="0 表示默认 50 MB；仅超过该大小的文件走断点续传通道，小文件继续高速并发传输"
                            >
                              <Input
                                inputMode="numeric"
                                value={String(form.extraFlags?.resumeMinSizeMB || 0)}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    resumeMinSizeMB: Math.max(
                                      0,
                                      Math.min(10240, Math.trunc(Number(v) || 0)),
                                    ),
                                  })
                                }
                              />
                            </Field>
                          )}
                        </div>

                        <div className="advanced-option-group">
                          <div className="advanced-option-group-title">容错重试与联动策略</div>
                          <div className="form-grid form-grid--three">
                            <Field
                              label="弱网底层重试（--low-level-retries）"
                              hint="0 默认 10 次（0–50）"
                            >
                              <Input
                                inputMode="numeric"
                                value={String(form.extraFlags?.lowLevelRetries || 0)}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    lowLevelRetries: Math.max(
                                      0,
                                      Math.min(50, Math.trunc(Number(v) || 0)),
                                    ),
                                  })
                                }
                              />
                            </Field>
                            <Field
                              label="重试间隔（--retries-sleep）"
                              hint="如 5s / 30s / 1m（留空默认）"
                            >
                              <Input
                                value={form.extraFlags?.retriesSleep || ""}
                                placeholder="如 10s / 30s"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    retriesSleep: v,
                                  })
                                }
                              />
                            </Field>
                            <Field
                              label="定时一致性校验"
                              hint="周期后台校验并在发现差异时告警"
                            >
                              <Select
                                value={form.extraFlags?.checkSchedule || ""}
                                optionList={[
                                  { label: "关闭", value: "" },
                                  { label: "每日自动校验", value: "daily" },
                                  { label: "每周自动校验", value: "weekly" },
                                  { label: "每月自动校验", value: "monthly" },
                                ]}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    checkSchedule: String(v || ""),
                                  })
                                }
                              />
                            </Field>
                          </div>
                          <div className="form-grid form-grid--three">
                            <Field
                              label="失败自动重试次数"
                              hint="0 关闭；1–10 次，仅重跑失败项"
                            >
                              <Input
                                inputMode="numeric"
                                value={String(form.extraFlags?.autoRetryMax || 0)}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    autoRetryMax: Math.max(
                                      0,
                                      Math.min(10, Math.trunc(Number(v) || 0)),
                                    ),
                                  })
                                }
                              />
                            </Field>
                            <Field
                              label="自动重试延迟（分钟）"
                              hint="默认 5 分钟（1–1440）"
                            >
                              <Input
                                inputMode="numeric"
                                value={String(form.extraFlags?.autoRetryDelayMin || 0)}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    autoRetryDelayMin: Math.max(
                                      0,
                                      Math.min(1440, Math.trunc(Number(v) || 0)),
                                    ),
                                  })
                                }
                              />
                            </Field>
                            <Field
                              label="成功后串联触发任务"
                              hint="同步批次完成后自动触发另一个任务"
                            >
                              <Select
                                value={Number(form.extraFlags?.postSyncJobId || 0)}
                                optionList={[
                                  { label: "不串联", value: 0 },
                                  ...(existingJobs || [])
                                    .filter((j) => j.id !== form.id)
                                    .map((j) => ({
                                      label: j.remark?.trim() || `任务 #${j.id}`,
                                      value: j.id,
                                    })),
                                ]}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    postSyncJobId: Math.max(
                                      0,
                                      Math.trunc(Number(v) || 0),
                                    ),
                                  })
                                }
                              />
                            </Field>
                          </div>
                          <div className="native-checkbox-group">
                            <Checkbox
                              checked={Boolean(form.extraFlags?.directStream)}
                              onChange={(e) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  directStream: Boolean(e.target.checked),
                                })
                              }
                            >
                              海量文件直接流式同步（跳过前端预扫描与待传清单，大幅降低内存与扫描耗时）
                            </Checkbox>
                          </div>
                        </div>

                        <div className="advanced-option-group">
                          <div className="advanced-option-group-title">并发与带宽流控</div>
                          <div className="form-grid form-grid--three">
                            <Field label="操作并发覆盖" hint="0 = 跟随全局设置 (1–100)">
                              <Input
                                inputMode="numeric"
                                value={String(form.copyConcurrency)}
                                onChange={(v) =>
                                  change(
                                    "copyConcurrency",
                                    Math.max(
                                      0,
                                      Math.min(100, Math.trunc(Number(v) || 0)),
                                    ),
                                  )
                                }
                              />
                            </Field>
                            <Field label="扫描并发覆盖" hint="0 = 跟随全局设置 (1–50)">
                              <Input
                                inputMode="numeric"
                                value={String(form.scanConcurrency)}
                                onChange={(v) =>
                                  change(
                                    "scanConcurrency",
                                    Math.max(
                                      0,
                                      Math.min(50, Math.trunc(Number(v) || 0)),
                                    ),
                                  )
                                }
                              />
                            </Field>
                            <Field label="失败重试覆盖" hint="-1 = 跟随全局 (0–10)">
                              <Input
                                inputMode="numeric"
                                value={String(form.maxRetries)}
                                onChange={(v) => {
                                  const trimmed = v.trim();
                                  if (trimmed === "-" || trimmed === "") {
                                    change("maxRetries", -1);
                                    return;
                                  }
                                  const n = Math.trunc(Number(trimmed));
                                  if (Number.isFinite(n)) {
                                    change(
                                      "maxRetries",
                                      Math.max(-1, Math.min(10, n)),
                                    );
                                  }
                                }}
                              />
                            </Field>
                          </div>
                          <Field
                            label="带宽限制"
                            fieldId="bwLimit"
                            error={bwLimitError || showStepError(1, "bwLimit")}
                            hint="留空跟随系统全局默认；支持单任务固定限速、定时分时段限速或强制不限速"
                          >
                            <BwLimitEditor
                              allowInherit
                              compact
                              globalDefault={systemSettings.data?.defaultBwLimit || ""}
                              value={form.bwLimit}
                              onChange={(v) => change("bwLimit", v)}
                              onValidityChange={setBwLimitError}
                            />
                          </Field>
                          <div className="form-grid form-grid--two">
                            <Field
                              label="API 限流（--tpslimit）"
                              hint="每秒请求上限（0–1000），0 表示不限（防云盘 429）"
                            >
                              <Input
                                inputMode="decimal"
                                value={String(form.tpsLimit)}
                                onChange={(v) => {
                                  const n = Number(v);
                                  change(
                                    "tpsLimit",
                                    Number.isFinite(n) && n >= 0
                                      ? Math.min(1000, n)
                                      : 0,
                                  );
                                }}
                              />
                            </Field>
                            <Field
                              label="API 突发上限（--tpslimit-burst）"
                              hint="0–1000，0 表示使用 rclone 默认值"
                            >
                              <Input
                                inputMode="numeric"
                                value={String(form.tpsLimitBurst)}
                                onChange={(v) =>
                                  change(
                                    "tpsLimitBurst",
                                    Math.max(
                                      0,
                                      Math.min(1000, Math.trunc(Number(v) || 0)),
                                    ),
                                  )
                                }
                              />
                            </Field>
                          </div>
                          <div className="form-grid form-grid--two">
                            <Field
                              label="时间精度容差（--modify-window）"
                              hint="留空默认；FAT32/SMB/部分网盘可填 1s 或 2s"
                            >
                              <Input
                                value={form.modifyWindow}
                                placeholder="如 1s / 2s / 1m"
                                showClear
                                onChange={(v) => change("modifyWindow", v)}
                              />
                            </Field>
                            <Field
                              label="单文件多线程流数（--multi-thread-streams）"
                              hint="0 使用默认（4）；1–64，加速单一大文件传输"
                            >
                              <Input
                                inputMode="numeric"
                                value={String(form.multiThreadStreams)}
                                onChange={(v) =>
                                  change(
                                    "multiThreadStreams",
                                    Math.max(
                                      0,
                                      Math.min(64, Math.trunc(Number(v) || 0)),
                                    ),
                                  )
                                }
                              />
                            </Field>
                          </div>
                        </div>

                        <div className="advanced-option-group">
                          <div className="advanced-option-group-title">rclone 底层行为与性能调优</div>
                          <SettingRow
                            label="快速列表（--fast-list）"
                            variant="bordered"
                            tip="在支持的远端（S3 / B2 / GCS / Azure 等）批量递归列目录，大幅减少 API 事务次数，略微增加内存占用"
                          >
                            <Switch
                              aria-label="快速列表"
                              checked={form.fastList}
                              onChange={(v) => change("fastList", v)}
                            />
                          </SettingRow>
                          <SettingRow
                            label="同步空子目录（--create-empty-src-dirs）"
                            variant="bordered"
                            tip="在目标端保留源目录中不含任何文件的空目录结构"
                          >
                            <Switch
                              aria-label="同步空子目录"
                              checked={form.createEmptySrcDirs}
                              onChange={(v) => change("createEmptySrcDirs", v)}
                            />
                          </SettingRow>
                          <SettingRow
                            label="跟随本地符号链接"
                            variant="bordered"
                            tip="开启后跟随源端本地授权目录内指向同授权目录内部的符号链接（跨出授权目录的链接仍会被安全忽略）"
                          >
                            <Switch
                              aria-label="跟随本地符号链接"
                              checked={form.followSymlinks}
                              onChange={(v) => change("followSymlinks", v)}
                            />
                          </SettingRow>
                          <div className="native-checkbox-group">
                            <Checkbox
                              checked={Boolean(form.extraFlags?.noUpdateModtime)}
                              onChange={(e) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  noUpdateModtime: Boolean(e.target.checked),
                                })
                              }
                            >
                              跳过仅修改时间变更的元数据更新（--no-update-modtime）
                            </Checkbox>
                            <Checkbox
                              checked={Boolean(form.extraFlags?.inplace)}
                              onChange={(e) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  inplace: Boolean(e.target.checked),
                                })
                              }
                            >
                              原地覆盖目标文件，不创建临时文件（--inplace）
                            </Checkbox>
                            <Checkbox
                              checked={Boolean(form.extraFlags?.checkFirst)}
                              onChange={(e) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  checkFirst: Boolean(e.target.checked),
                                })
                              }
                            >
                              先完成全部差异比对再传输（--check-first）
                            </Checkbox>
                            <Checkbox
                              checked={Boolean(form.extraFlags?.s3NoHead)}
                              onChange={(e) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  s3NoHead: Boolean(e.target.checked),
                                })
                              }
                            >
                              跳过 S3 上传后 HEAD 校验以减少 API 调用（--s3-no-head）
                            </Checkbox>
                            <Checkbox
                              checked={Boolean(form.extraFlags?.ignoreSize)}
                              onChange={(e) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  ignoreSize: Boolean(e.target.checked),
                                })
                              }
                            >
                              仅按修改时间比对，忽略文件大小变化（--ignore-size）
                            </Checkbox>
                            <Checkbox
                              checked={Boolean(form.extraFlags?.trackRenames)}
                              disabled={form.method !== 1 && form.method !== 3}
                              onChange={(e) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  trackRenames: Boolean(e.target.checked),
                                })
                              }
                            >
                              启用重命名追踪，避免重命名后重传（--track-renames，开启同步删除或双向同步时生效）
                            </Checkbox>
                            <Checkbox
                              checked={Boolean(form.extraFlags?.useMmap)}
                              onChange={(e) =>
                                change("extraFlags", {
                                  ...form.extraFlags,
                                  useMmap: Boolean(e.target.checked),
                                })
                              }
                            >
                              使用 mmap 零拷贝传输缓冲（--use-mmap）
                            </Checkbox>
                          </div>
                          <div className="form-grid form-grid--three">
                            <Field
                              label="单次传输总量上限（--max-transfer）"
                              hint="留空不限，支持如 50G / 500M"
                            >
                              <Input
                                value={form.extraFlags?.maxTransfer || ""}
                                placeholder="如 100G / 500M"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    maxTransfer: v,
                                  })
                                }
                              />
                            </Field>
                            <Field label="达限截断模式（--cutoff-mode）">
                              <Select
                                value={form.extraFlags?.cutoffMode || ""}
                                optionList={[
                                  { label: "默认 (hard)", value: "" },
                                  { label: "hard（立即停止）", value: "hard" },
                                  { label: "soft（传完当前队列）", value: "soft" },
                                  { label: "cautious（不再发起新传输）", value: "cautious" },
                                ]}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    cutoffMode: String(v || ""),
                                  })
                                }
                              />
                            </Field>
                            <Field
                              label="最大运行时长（--max-duration）"
                              hint="超时后平滑收尾退出，如 2h"
                            >
                              <Input
                                value={form.extraFlags?.maxDuration || ""}
                                placeholder="如 2h / 30m"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    maxDuration: v,
                                  })
                                }
                              />
                            </Field>
                          </div>
                          <div className="form-grid form-grid--three">
                            <Field label="传输优先级排序（--order-by）">
                              <Select
                                value={form.extraFlags?.orderBy || ""}
                                optionList={[
                                  { label: "默认顺序", value: "" },
                                  { label: "小文件优先 (size,ascending)", value: "size,ascending" },
                                  { label: "大文件优先 (size,descending)", value: "size,descending" },
                                  { label: "最旧修改优先 (modtime,ascending)", value: "modtime,ascending" },
                                  { label: "最新修改优先 (modtime,descending)", value: "modtime,descending" },
                                  { label: "文件名升序 (name,ascending)", value: "name,ascending" },
                                  { label: "文件名降序 (name,descending)", value: "name,descending" },
                                ]}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    orderBy: String(v || ""),
                                  })
                                }
                              />
                            </Field>
                            <Field
                              label="预扫描队列上限（--max-backlog）"
                              hint="0 使用默认；控制内存排队数"
                            >
                              <Input
                                inputMode="numeric"
                                value={String(form.extraFlags?.maxBacklog || 0)}
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    maxBacklog: Math.max(
                                      0,
                                      Math.trunc(Number(v) || 0),
                                    ),
                                  })
                                }
                              />
                            </Field>
                            <Field
                              label="读写缓冲区（--buffer-size）"
                              hint="留空默认 16M，设为 0 禁用"
                            >
                              <Input
                                value={form.extraFlags?.bufferSize || ""}
                                placeholder="如 16M / 32M / 0"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    bufferSize: v,
                                  })
                                }
                              />
                            </Field>
                          </div>
                          <div className="form-grid form-grid--three">
                            <Field
                              label="多线程分流阈值（--multi-thread-cutoff）"
                              hint="默认 250M"
                            >
                              <Input
                                value={form.extraFlags?.multiThreadCutoff || ""}
                                placeholder="如 250M / 100M"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    multiThreadCutoff: v,
                                  })
                                }
                              />
                            </Field>
                            <Field
                              label="多线程分块（--multi-thread-chunk-size）"
                              hint="默认 64M"
                            >
                              <Input
                                value={form.extraFlags?.multiThreadChunkSize || ""}
                                placeholder="如 64M / 32M"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    multiThreadChunkSize: v,
                                  })
                                }
                              />
                            </Field>
                            <Field label="User-Agent（--user-agent）">
                              <Input
                                value={form.extraFlags?.userAgent || ""}
                                placeholder="可选 HTTP User-Agent"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    userAgent: v,
                                  })
                                }
                              />
                            </Field>
                          </div>
                          <div className="form-grid form-grid--two">
                            <Field label="I/O 空闲超时（--timeout）" hint="默认 5m">
                              <Input
                                value={form.extraFlags?.ioTimeout || ""}
                                placeholder="如 5m / 30s"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    ioTimeout: v,
                                  })
                                }
                              />
                            </Field>
                            <Field label="连接超时（--contimeout）" hint="默认 1m">
                              <Input
                                value={form.extraFlags?.conTimeout || ""}
                                placeholder="如 1m / 15s"
                                showClear
                                onChange={(v) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    conTimeout: v,
                                  })
                                }
                              />
                            </Field>
                          </div>
                        </div>

                        {form.method === 3 && (
                          <div className="advanced-option-group">
                            <div className="advanced-option-group-title">双向同步保护策略</div>
                            <div className="form-grid form-grid--two">
                              <Field
                                label="单次删除比例上限（%）"
                                hint="0 表示使用默认值（50%），1–100 按单端删除占比触发安全保护，防止误删全盘"
                              >
                                <Input
                                  inputMode="numeric"
                                  value={String(form.extraFlags?.bisyncMaxDelete || 0)}
                                  onChange={(v) =>
                                    change("extraFlags", {
                                      ...form.extraFlags,
                                      bisyncMaxDelete: Math.max(
                                        0,
                                        Math.min(100, Math.trunc(Number(v) || 0)),
                                      ),
                                    })
                                  }
                                />
                              </Field>
                            </div>
                            <div className="native-checkbox-group">
                              <Checkbox
                                checked={Boolean(form.extraFlags?.bisyncForce)}
                                onChange={(e) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    bisyncForce: Boolean(e.target.checked),
                                  })
                                }
                              >
                                超限删除仍强制执行（谨慎启用）
                              </Checkbox>
                            </div>
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                </div>
              )}

              {panelMounted("filter") && (
                <div className="form-section" style={panelStyle("filter")}>
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
                            const currentVal =
                              parseFileSizeInput(form[valueKey]) ?? 0;
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
                          change(
                            "exclude",
                            updateExcludeFolders(form.exclude, folders),
                          )
                        }
                      />
                    </div>
                  </Field>

                  <Field
                    label="系统目录过滤"
                    hint="忽略操作系统和 NAS 的系统目录"
                  >
                    <div className="system-dir-groups" role="group">
                      {systemDirFilterGroups.map((group) => {
                        const selected = group.patterns.filter((p) =>
                          isExcludePatternEnabled(form.exclude, p),
                        );
                        const allSelected =
                          selected.length === group.patterns.length;
                        return (
                          <div className="system-dir-group" key={group.key}>
                            <Checkbox
                              checked={allSelected}
                              indeterminate={
                                selected.length > 0 && !allSelected
                              }
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
