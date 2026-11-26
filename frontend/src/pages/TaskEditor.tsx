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
  IconChevronRight,
  IconInfoCircle,
  IconTick,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  Editor,
  Field,
  HelpTip,
  InlineNotice,
  SettingRow,
  errorToast,
} from "../components/common";
import { ExcludeTree } from "../components/ExcludeTree";
import { FileTypeFilter } from "../components/FileTypeFilter";
import { RemotePaths } from "../components/RemotePaths";
import { SyncPathPreview } from "../components/SyncPathPreview";
import { useAction, useEditorForm, useResource } from "../lib/hooks";
import { channelNames, getNotifyEndpointSummary } from "../lib/notifyForm";
import { RemoteDirCacheProvider } from "../lib/remoteDirCache";
import {
  applyCronPresetMode,
  computeNextCronRuns,
  cronFieldRanges,
  customCronPresets,
  detectCronPresetMode,
  formatCronRunTime,
  formatIntervalHuman,
  formatWeekdayCronExpression,
  intervalInputToMinutes,
  intervalPresets,
  intervalUnitOptions,
  parseWeekdaySelection,
  splitIntervalMinutes,
  validateCronFields,
  weekdayOptions,
  type CronFieldKey,
  type IntervalUnit,
  type SchedulePresetMode,
} from "../lib/schedule";
import {
  buildJobPayload,
  defaultJobForm,
  isExcludePatternEnabled,
  jobToForm,
  parseExcludeFolders,
  shouldClearExcludeFoldersOnSrcChange,
  systemDirFilterGroups,
  updateExcludeFolders,
  updateExcludePatterns,
  validateJobFormFields,
  type JobFieldErrorKey,
  type JobForm,
} from "../lib/taskForm";
import {
  compareModeOptions,
  conflictPolicyOptions,
  cronFields,
  cronTypeNames,
  filterModeOptions,
  formatSchedulePlan,
  methodOptions,
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
import type { AlistItem, FilterTestResult, JobItem } from "../types";

export { parseIntervalInput };
export { hasPendingFileSizeDraftChange, mergeFileSizeDrafts };

export interface TaskEditorSavedResult {
  createdId?: number;
  runNow?: boolean;
  method: number;
}

const taskEditorTabs = [
  { key: "basic", label: "基础与路径", optional: false },
  { key: "schedule", label: "执行计划", optional: false },
  { key: "filter", label: "过滤规则", optional: true },
] as const;

const schedulePresetOptions: Array<{ value: SchedulePresetMode; label: string }> = [
  { value: "daily", label: "每天" },
  { value: "weekly", label: "每周" },
  { value: "monthly", label: "每月" },
  { value: "custom", label: "自定义 Cron" },
];

const hourOptions = Array.from({ length: 24 }, (_, i) => ({
  value: String(i),
  label: `${String(i).padStart(2, "0")} 时`,
}));

const minuteOptions = Array.from({ length: 60 }, (_, i) => ({
  value: String(i),
  label: `${String(i).padStart(2, "0")} 分`,
}));

const monthDayOptions = Array.from({ length: 31 }, (_, i) => ({
  value: String(i + 1),
  label: `每月 ${i + 1} 日`,
}));

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

const syncMethodTip = (
  <div className="sync-method-tip">
    {methodOptions.map((method) => (
      <div key={method.name}>
        <strong>{method.name}</strong>：{method.description}
      </div>
    ))}
  </div>
);

export default function TaskEditor({
  job,
  initialDraft,
  engines,
  existingJobs,
  enginesError,
  onRetryEngines,
  onNavigateEngines,
  onClose,
  onSaved,
}: {
  job: JobItem | null;
  /** Optional pre-populated draft for cloning an existing job. */
  initialDraft?: JobForm;
  engines: AlistItem[];
  existingJobs?: JobItem[];
  /** Load error of the engine list; the select would otherwise just be empty. */
  enginesError?: string;
  onRetryEngines?: () => void;
  onNavigateEngines?: () => void;
  onClose: () => void;
  onSaved: (result: TaskEditorSavedResult) => void;
}) {
  const defaultEngineId = engines[0]?.id;
  const seedForm = useMemo(
    () =>
      initialDraft
        ? { ...initialDraft, alistId: initialDraft.alistId || defaultEngineId }
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
  const nativeStorage = useResource((signal) => api.nativeStorageState(signal));
  const [nativeTarget, setNativeTarget] = useState("");
  const notifications = useResource((signal) => api.notifications(signal));
  const [fileSizeDrafts, setFileSizeDrafts] = useState<FileSizeDrafts>({});
  const [sizeEnabled, setSizeEnabled] = useState<{ min: boolean; max: boolean }>(
    () => ({
      min: (parseFileSizeInput(seedForm.minFileSize) ?? 0) > 0,
      max: (parseFileSizeInput(seedForm.maxFileSize) ?? 0) > 0,
    }),
  );
  const effectiveDirty =
    dirty ||
    Boolean(initialDraft) ||
    hasPendingFileSizeDraftChange(form, fileSizeDrafts);
  const methodLabelId = useId();
  const initialInterval = useMemo(
    () => splitIntervalMinutes(seedForm.interval),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );
  const [intervalUnit, setIntervalUnit] = useState<IntervalUnit>(
    initialInterval.unit,
  );
  const [intervalDraft, setIntervalDraft] = useState<string | null>(null);
  const [cronPresetMode, setCronPresetMode] = useState<SchedulePresetMode>(() =>
    detectCronPresetMode(seedForm),
  );
  const [policyOpen, setPolicyOpen] = useState<boolean>(() =>
    Boolean(
      seedForm.compareMode ||
        seedForm.conflictPolicy ||
        seedForm.trashEnable ||
        seedForm.deleteEmptySrcDirs,
    ),
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
        seedForm.extraFlags?.bisyncResyncMode ||
        Number(seedForm.extraFlags?.bisyncMaxDelete || 0) > 0 ||
        seedForm.extraFlags?.bisyncForce,
    ),
  );
  const [runAfterCreate, setRunAfterCreate] = useState(false);

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

  // Panels stay mounted once visited: switching tabs used to unmount them, so
  // every return to basic/filter re-fetched directory trees and dropped expansion state.
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

  // 标签栏/步骤条固定在顶部，只有下方内容区滚动。切换标签或步骤时回到内容顶部。
  const scrollRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = 0;
  }, [activePanel]);

  const mergedForm = useMemo(
    () => mergeFileSizeDrafts(form, fileSizeDrafts),
    [form, fileSizeDrafts],
  );
  const validationOptions = useMemo(
    () => ({
      storedDstMap: job?.dstMap,
      existingJobs,
      sizeEnabled,
    }),
    [job?.dstMap, existingJobs, sizeEnabled],
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
    // Range/size errors and cron errors in custom mode are helpful immediately once non-empty
    if (validatedSteps.has(stepIndex)) return msg;
    if (key === "fileSizeRange") return msg;
    if (
      key === "dstPath" &&
      (form.method === 2 || form.method === 3) &&
      form.dstPath.length > 1
    ) {
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
      const err = validation.stepErrors[i];
      if (err) {
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
    const err = validation.stepErrors[step];
    if (err) {
      markStepValidated(step);
      Toast.warning(err);
      scrollToFirstError();
      return;
    }
    setStep((s) => Math.min(s + 1, taskEditorTabs.length - 1));
  };

  const previousStep = () => setStep((s) => Math.max(s - 1, 0));

  const save = () => {
    const invalidStep = validation.stepErrors.findIndex(Boolean);
    if (invalidStep >= 0) {
      setValidatedSteps(new Set([0, 1, 2]));
      Toast.warning(validation.stepErrors[invalidStep]);
      if (isNew) setStep(invalidStep);
      else setActiveTab(taskEditorTabs[invalidStep].key);
      scrollToFirstError();
      return;
    }
    const payload = buildJobPayload(mergedForm);
    void action.run(async () => {
      try {
        const result = await api.saveJob(payload);
        setFileSizeDrafts({});
        setDirty(false);
        const createdId =
          isNew && result && typeof result.id === "number" && result.id > 0
            ? result.id
            : undefined;
        Toast.success(job ? "编辑成功，下次任务生效" : "任务已创建");
        onSaved({
          createdId,
          runNow: isNew && runAfterCreate && (mergedForm.isCron === 2 || mergedForm.enable),
          method: mergedForm.method,
        });
      } catch (err) {
        errorToast(err);
      }
    });
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

  // Detect standard WebDAV (vendor=other / OpenList / AList) in selected paths
  // so we can recommend "size-only" compare mode to avoid full read-back checks.
  const matchedStandardWebDAVNames = useMemo(() => {
    const remotes = nativeStorage.data?.remotes;
    if (!remotes?.length) return [] as string[];
    const standardDavMap = new Map<string, string>();
    for (const remote of remotes) {
      if (remote.type !== "webdav") continue;
      const vendor = (remote.options?.vendor || "").trim().toLowerCase();
      if (!vendor || vendor === "other") {
        standardDavMap.set(remote.name.toLowerCase(), remote.name);
      }
    }
    if (standardDavMap.size === 0) return [] as string[];
    const hits = new Set<string>();
    for (const p of [...form.srcPath, ...form.dstPath]) {
      const seg = p.replace(/^\/+/, "").split("/")[0]?.toLowerCase();
      if (seg && standardDavMap.has(seg)) {
        hits.add(standardDavMap.get(seg)!);
      }
    }
    return Array.from(hits);
  }, [nativeStorage.data?.remotes, form.srcPath, form.dstPath]);

  const srcMultiple = form.method !== 3 || form.srcPath.length > 1;
  const dstMultiple =
    (form.method !== 2 && form.method !== 3) || form.dstPath.length > 1;
  const activeMethodOption = methodOptions[form.method] ?? methodOptions[0];
  const isDestructiveMethod =
    form.method === 1 || form.method === 2 || form.method === 3;

  // Schedule preview data
  const cronErrors = useMemo(
    () => (form.isCron === 1 ? validateCronFields(form) : {}),
    [form],
  );
  const cronErrorMessage = Object.values(cronErrors)[0] ?? "";
  const upcomingRuns = useMemo(() => {
    if (form.isCron !== 1 || cronErrorMessage) return [];
    return computeNextCronRuns(form, 3);
  }, [
    form.isCron,
    form.second,
    form.minute,
    form.hour,
    form.day,
    form.month,
    form.day_of_week,
    cronErrorMessage,
  ]);

  const intervalDisplayValue = useMemo(() => {
    if (intervalDraft !== null) return intervalDraft;
    const factor =
      intervalUnitOptions.find((u) => u.value === intervalUnit)?.factor ?? 1;
    if (!Number.isFinite(form.interval) || form.interval <= 0) return "";
    const scaled = form.interval / factor;
    return Number.isInteger(scaled) ? String(scaled) : String(form.interval);
  }, [intervalDraft, intervalUnit, form.interval]);

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
    (form.extraFlags?.bisyncResyncMode?.trim() ? 1 : 0) +
    (Number(form.extraFlags?.bisyncMaxDelete || 0) > 0 ? 1 : 0) +
    (form.extraFlags?.bisyncForce ? 1 : 0) +
    (Number(form.copyConcurrency || 0) > 0 ? 1 : 0) +
    (Number(form.scanConcurrency || 0) > 0 ? 1 : 0) +
    (Number(form.maxRetries) >= 0 ? 1 : 0);
  const activePolicyCount =
    (form.compareMode !== 0 ? 1 : 0) +
    (form.method !== 2 && form.conflictPolicy !== 0 ? 1 : 0) +
    (form.method !== 2 && form.trashEnable ? 1 : 0) +
    (form.method === 2 && form.deleteEmptySrcDirs ? 1 : 0);

  const runFilterTest = async () => {
    const samplePath = testPath.trim();
    if (!samplePath) {
      Toast.warning("请输入要测试的示例文件路径");
      return;
    }
    setTestingFilter(true);
    try {
      const payload = buildJobPayload(mergedForm);
      const sizeBytes = Math.max(0, Math.round((Number(testSizeMB) || 0) * 1024 * 1024));
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

  const selectedWeekdays = useMemo(
    () => parseWeekdaySelection(form.day_of_week) ?? [0],
    [form.day_of_week],
  );

  const sizeGroupError =
    showStepError(2, "fileSizeRange") ||
    showStepError(2, "minFileSize") ||
    showStepError(2, "maxFileSize");

  const primaryActionIsNext = isNew && step === 0;

  return (
    <RemoteDirCacheProvider>
      <Editor
        title={job ? "编辑任务" : initialDraft ? "复制任务" : "新建任务"}
        visible
        busy={action.busy}
        onClose={onClose}
        onSave={save}
        dirty={effectiveDirty}
        width={600}
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
                      placeholder="相册每日备份"
                    />
                  </Field>

                  {enginesError && (
                    <InlineNotice
                      role="alert"
                      message={`存储配置加载失败：${enginesError}`}
                      onRetry={onRetryEngines}
                    />
                  )}

                  {!enginesError && engines.length === 0 && (
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

                  <Field
                    label={
                      <span className="field-label-with-tip">
                        <span id={methodLabelId}>同步方式</span>
                        <HelpTip content={syncMethodTip} label="同步方式说明" />
                      </span>
                    }
                    ariaLabel="同步方式"
                  >
                    <div className="field-group" role="group">
                      <Select
                        aria-labelledby={methodLabelId}
                        value={form.method}
                        optionList={methodOptions.map((method, value) => ({
                          value,
                          label: method.name,
                        }))}
                        onChange={(value) => {
                          change("method", Number(value));
                        }}
                      />
                      <div
                        className={`sync-method-banner${isDestructiveMethod ? " is-warning" : ""}`}
                        role={isDestructiveMethod ? "status" : undefined}
                      >
                        {isDestructiveMethod ? (
                          <IconAlertTriangle aria-hidden="true" size="small" />
                        ) : (
                          <IconInfoCircle aria-hidden="true" size="small" />
                        )}
                        <span>{activeMethodOption.description}</span>
                      </div>
                    </div>
                  </Field>

                  <div className="form-grid">
                    <Field
                      label={form.method === 3 ? "源路径（单选）" : "源路径"}
                      required
                      error={showStepError(0, "srcPath")}
                      fieldId="srcPath"
                    >
                      <RemotePaths
                        key={`src-${form.alistId}-${srcMultiple ? "m" : "s"}`}
                        engineId={form.alistId}
                        value={form.srcPath}
                        onChange={handleSrcPathChange}
                        multiple={srcMultiple}
                        ariaLabel="选择源目录"
                      />
                    </Field>
                    <Field
                      label={
                        form.method === 2 || form.method === 3
                          ? "目标路径（单选）"
                          : "目标路径"
                      }
                      required
                      error={showStepError(0, "dstPath")}
                      fieldId="dstPath"
                    >
                      <RemotePaths
                        key={`dst-${form.alistId}-${dstMultiple ? "m" : "s"}`}
                        engineId={form.alistId}
                        value={form.dstPath}
                        onChange={(paths) => change("dstPath", paths)}
                        multiple={dstMultiple}
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
                          disabled={(() => {
                            const t = nativeTarget.trim();
                            return (
                              !t.startsWith("/") ||
                              t === "/" ||
                              t.split("/").filter(Boolean).length === 0
                            );
                          })()}
                          onClick={() => {
                            const cleaned = nativeTarget.trim().replace(/\/+$/, "");
                            if (!cleaned || cleaned === "/") return;
                            handleSrcPathChange(
                              form.method === 3
                                ? [cleaned]
                                : Array.from(new Set([...form.srcPath, cleaned])),
                            );
                            setNativeTarget("");
                          }}
                        >
                          添加到源路径
                        </Button>
                        <Button
                          disabled={(() => {
                            const t = nativeTarget.trim();
                            return (
                              !t.startsWith("/") ||
                              t === "/" ||
                              t.split("/").filter(Boolean).length === 0
                            );
                          })()}
                          onClick={() => {
                            const cleaned = nativeTarget.trim().replace(/\/+$/, "");
                            if (!cleaned || cleaned === "/") return;
                            change(
                              "dstPath",
                              form.method === 2 || form.method === 3
                                ? [cleaned]
                                : Array.from(new Set([...form.dstPath, cleaned])),
                            );
                            setNativeTarget("");
                          }}
                        >
                          添加到目标路径
                        </Button>
                      </div>
                    </div>
                  </Field>
                  {matchedStandardWebDAVNames.length > 0 && form.compareMode !== 1 && (
                    <div className="sync-method-banner is-warning webdav-compare-tip" role="status">
                      <IconAlertTriangle aria-hidden="true" size="small" />
                      <span>
                        已选择标准 WebDAV 存储（{matchedStandardWebDAVNames.join("、")}，如 OpenList / AList）。标准 WebDAV 默认比对会触发文件读回校验并消耗流量，建议将「文件比对方式」切换为 <strong>仅大小</strong>。
                      </span>
                      <Button
                        size="small"
                        type="primary"
                        theme="light"
                        onClick={() => {
                          change("compareMode", 1);
                          setPolicyOpen(true);
                          Toast.success("已将文件比对方式设为「仅大小」");
                        }}
                      >
                        设为仅大小
                      </Button>
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
                    onKeepFirstDst={
                      (form.method === 2 || form.method === 3) &&
                      form.dstPath.length > 1
                        ? () => change("dstPath", form.dstPath.slice(0, 1))
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
                            onChange={(v) => change("compareMode", Number(v))}
                          />
                        </Field>
                        {form.method === 2 ? (
                          <div className="sync-method-banner">
                            <IconAlertTriangle aria-hidden="true" size="small" />
                            <span>
                              移动模式为保护源数据安全，目标端若已存在不同内容的文件将自动跳过并保留源文件，不执行覆盖、冲突副本或目标回收站操作。
                            </span>
                          </div>
                        ) : (
                          <>
                            <Field label="同名冲突处理">
                              <Select
                                value={form.conflictPolicy}
                                optionList={conflictPolicyOptions.map((o) => ({
                                  label: o.label,
                                  value: o.value,
                                }))}
                                onChange={(v) =>
                                  change("conflictPolicy", Number(v))
                                }
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
                                  : "开启后，全量删除或覆盖旧文件时先移至目标目录下的回收站子目录，避免误删不可恢复"
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
                          </>
                        )}
                        {form.method === 2 && (
                          <SettingRow
                            label="移动完成后清理空源子目录"
                            variant="bordered"
                            tip="仅清理本次扫描过且全部文件已移空的源子目录，不会删除源根目录本身"
                          >
                            <Switch
                              aria-label="移动完成后清理空源子目录"
                              checked={form.deleteEmptySrcDirs}
                              onChange={(v) => change("deleteEmptySrcDirs", v)}
                            />
                          </SettingRow>
                        )}
                      </div>
                    )}
                  </div>
                </div>
              )}

              {panelMounted("schedule") && (
                <div className="form-section" style={panelStyle("schedule")}>
                  <Field label="调度方式">
                    <Select
                      value={form.isCron}
                      optionList={[1, 0, 2].map((value) => ({
                        value,
                        label: cronTypeNames[value],
                      }))}
                      onChange={(value) => {
                        const nextIsCron = Number(value);
                        if (nextIsCron === 0) {
                          const split = splitIntervalMinutes(
                            form.interval > 0 ? form.interval : 1440,
                          );
                          setIntervalUnit(split.unit);
                          setIntervalDraft(null);
                          if (form.interval <= 0) {
                            patch({ isCron: 0, interval: 1440 });
                            return;
                          }
                        } else if (nextIsCron === 1) {
                          setCronPresetMode(detectCronPresetMode(form));
                        }
                        change("isCron", nextIsCron);
                      }}
                    />
                  </Field>

                  {form.isCron === 0 && (
                    <Field
                      label="执行间隔"
                      error={showStepError(1, "interval")}
                      fieldId="interval"
                    >
                      <div className="field-group" role="group">
                        <div className="interval-control">
                          <Input
                            aria-label="执行间隔数值"
                            inputMode="numeric"
                            value={intervalDisplayValue}
                            placeholder="输入正整数"
                            onChange={(value) => {
                              setIntervalDraft(value);
                              change(
                                "interval",
                                intervalInputToMinutes(value, intervalUnit),
                              );
                            }}
                            onBlur={() => {
                              if (
                                intervalDraft !== null &&
                                /^\d+$/.test(intervalDraft.trim())
                              ) {
                                setIntervalDraft(null);
                              }
                            }}
                          />
                          <Select
                            aria-label="执行间隔单位"
                            value={intervalUnit}
                            optionList={intervalUnitOptions.map((u) => ({
                              value: u.value,
                              label: u.label,
                            }))}
                            onChange={(next) => {
                              const nextUnit = String(next) as IntervalUnit;
                              const rawText =
                                intervalDraft ?? intervalDisplayValue;
                              setIntervalUnit(nextUnit);
                              change(
                                "interval",
                                intervalInputToMinutes(rawText, nextUnit),
                              );
                            }}
                          />
                        </div>
                        <div
                          className="schedule-chips"
                          role="group"
                          aria-label="常用执行间隔"
                        >
                          {intervalPresets.map((preset) => {
                            const presetMins = intervalInputToMinutes(
                              String(preset.value),
                              preset.unit,
                            );
                            const active =
                              intervalDraft === null &&
                              form.interval === presetMins;
                            return (
                              <button
                                key={preset.label}
                                type="button"
                                className={`schedule-chip${active ? " is-active" : ""}`}
                                onClick={() => {
                                  setIntervalUnit(preset.unit);
                                  setIntervalDraft(null);
                                  change("interval", presetMins);
                                }}
                              >
                                每 {preset.label}
                              </button>
                            );
                          })}
                        </div>
                      </div>
                    </Field>
                  )}

                  {form.isCron === 1 && (
                    <div className="schedule-cron-section">
                      <div
                        className="schedule-preset-bar"
                        role="group"
                        aria-label="定时周期"
                      >
                        {schedulePresetOptions.map((option) => (
                          <button
                            key={option.value}
                            type="button"
                            className={`schedule-preset-btn${cronPresetMode === option.value ? " is-active" : ""}`}
                            aria-pressed={cronPresetMode === option.value}
                            onClick={() => {
                              setCronPresetMode(option.value);
                              patch(applyCronPresetMode(form, option.value));
                            }}
                          >
                            {option.label}
                          </button>
                        ))}
                      </div>

                      {cronPresetMode === "daily" && (
                        <div className="schedule-time-row">
                          <Field label="执行小时">
                            <Select
                              aria-label="执行小时"
                              value={String(Number(form.hour || 0))}
                              optionList={hourOptions}
                              onChange={(v) =>
                                patch({
                                  second: "0",
                                  hour: String(v),
                                  day: "*",
                                  month: "*",
                                  day_of_week: "*",
                                })
                              }
                            />
                          </Field>
                          <Field label="执行分钟">
                            <Select
                              aria-label="执行分钟"
                              value={String(Number(form.minute || 0))}
                              optionList={minuteOptions}
                              onChange={(v) =>
                                patch({
                                  second: "0",
                                  minute: String(v),
                                  day: "*",
                                  month: "*",
                                  day_of_week: "*",
                                })
                              }
                            />
                          </Field>
                        </div>
                      )}

                      {cronPresetMode === "weekly" && (
                        <div className="form-section">
                          <Field label="每周执行日">
                            <div
                              className="weekday-picker"
                              role="group"
                              aria-label="选择星期"
                            >
                              {weekdayOptions.map((day) => {
                                const selected = selectedWeekdays.includes(
                                  day.value,
                                );
                                return (
                                  <button
                                    key={day.value}
                                    type="button"
                                    className={`weekday-chip${selected ? " is-active" : ""}`}
                                    aria-pressed={selected}
                                    onClick={() => {
                                      const next = selected
                                        ? selectedWeekdays.length > 1
                                          ? selectedWeekdays.filter(
                                              (d) => d !== day.value,
                                            )
                                          : selectedWeekdays
                                        : [...selectedWeekdays, day.value];
                                      patch({
                                        second: "0",
                                        day: "*",
                                        month: "*",
                                        day_of_week:
                                          formatWeekdayCronExpression(next),
                                      });
                                    }}
                                  >
                                    {day.label}
                                  </button>
                                );
                              })}
                            </div>
                          </Field>
                          <div className="schedule-time-row">
                            <Field label="执行小时">
                              <Select
                                aria-label="执行小时"
                                value={String(Number(form.hour || 0))}
                                optionList={hourOptions}
                                onChange={(v) =>
                                  patch({ second: "0", hour: String(v) })
                                }
                              />
                            </Field>
                            <Field label="执行分钟">
                              <Select
                                aria-label="执行分钟"
                                value={String(Number(form.minute || 0))}
                                optionList={minuteOptions}
                                onChange={(v) =>
                                  patch({ second: "0", minute: String(v) })
                                }
                              />
                            </Field>
                          </div>
                        </div>
                      )}

                      {cronPresetMode === "monthly" && (
                        <div className="form-section">
                          <div className="schedule-time-row schedule-time-row--monthly">
                            <Field label="每月日期">
                              <Select
                                aria-label="每月日期"
                                value={String(Number(form.day || 1))}
                                optionList={monthDayOptions}
                                onChange={(v) =>
                                  patch({
                                    second: "0",
                                    day: String(v),
                                    month: "*",
                                    day_of_week: "*",
                                  })
                                }
                              />
                            </Field>
                            <Field label="执行小时">
                              <Select
                                aria-label="执行小时"
                                value={String(Number(form.hour || 0))}
                                optionList={hourOptions}
                                onChange={(v) =>
                                  patch({ second: "0", hour: String(v) })
                                }
                              />
                            </Field>
                            <Field label="执行分钟">
                              <Select
                                aria-label="执行分钟"
                                value={String(Number(form.minute || 0))}
                                optionList={minuteOptions}
                                onChange={(v) =>
                                  patch({ second: "0", minute: String(v) })
                                }
                              />
                            </Field>
                          </div>
                          {Number(form.day) >= 29 && (
                            <div className="field-hint">
                              提示：没有 {form.day} 日的月份（如 2 月）将自动跳过。
                            </div>
                          )}
                        </div>
                      )}

                      {cronPresetMode === "custom" && (
                        <div className="form-section">
                          <div
                            className="schedule-chips"
                            role="group"
                            aria-label="常用 Cron 模板"
                          >
                            {customCronPresets.map((preset) => (
                              <button
                                key={preset.label}
                                type="button"
                                className="schedule-chip"
                                onClick={() => patch(preset.fields)}
                              >
                                {preset.label}
                              </button>
                            ))}
                          </div>
                          <div className="cron-grid">
                            {cronFields.map((field) => {
                              const key = field.name as CronFieldKey;
                              const rangeHint = cronFieldRanges[key][3];
                              const err = cronErrors[key];
                              return (
                                <Field
                                  key={field.name}
                                  label={
                                    <span className="cron-field-label">
                                      <span>{field.label}</span>
                                      <span className="cron-field-range">
                                        {rangeHint}
                                      </span>
                                    </span>
                                  }
                                  ariaLabel={field.label}
                                  error={
                                    validatedSteps.has(1) ? err : undefined
                                  }
                                  fieldId={key}
                                >
                                  <Input
                                    value={form[key]}
                                    placeholder={field.placeholder}
                                    onChange={(value) => change(key, value)}
                                  />
                                </Field>
                              );
                            })}
                          </div>
                        </div>
                      )}
                    </div>
                  )}

                  <div
                    className={`schedule-preview-card${
                      (form.isCron === 1 && cronErrorMessage) ||
                      (form.isCron === 0 && validation.fields.interval)
                        ? " is-invalid"
                        : ""
                    }`}
                    aria-live="polite"
                  >
                    {form.isCron === 1 && cronErrorMessage ? (
                      <div className="schedule-preview-error" role="alert">
                        <IconAlertTriangle aria-hidden="true" size="small" />
                        <span>{cronErrorMessage}</span>
                      </div>
                    ) : form.isCron === 0 && validation.fields.interval ? (
                      <div className="schedule-preview-error" role="alert">
                        <IconAlertTriangle aria-hidden="true" size="small" />
                        <span>{validation.fields.interval}</span>
                      </div>
                    ) : (
                      <>
                        <div className="schedule-preview-plan">
                          {form.isCron === 0
                            ? formatIntervalHuman(form.interval)
                            : formatSchedulePlan(form)}
                        </div>
                        {form.isCron === 1 && upcomingRuns.length > 0 && (
                          <div className="schedule-preview-runs">
                            <span className="muted">预计接下来执行：</span>
                            <div className="schedule-preview-run-list">
                              {upcomingRuns.map((date) => (
                                <span key={date.getTime()}>
                                  {formatCronRunTime(date)}
                                </span>
                              ))}
                            </div>
                          </div>
                        )}
                        {form.isCron === 1 && upcomingRuns.length === 0 && (
                          <div className="schedule-preview-error" role="alert">
                            <IconAlertTriangle
                              aria-hidden="true"
                              size="small"
                            />
                            <span>
                              该表达式在未来 5 年内没有匹配的执行时间，请检查日期与月份组合
                            </span>
                          </div>
                        )}
                        {form.isCron === 0 && (
                          <div className="muted">
                            保存后按设定间隔自动调度，也可在任务总览随时手动执行。
                          </div>
                        )}
                      </>
                    )}
                  </div>

                  {form.isCron !== 2 && (
                    <SettingRow
                      label="启用定时调度"
                      variant="bordered"
                      tip="关闭后仅保存任务配置，不会按计划自动执行；可稍后在任务总览页开启"
                    >
                      <Switch
                        aria-label="启用定时调度"
                        checked={form.enable}
                        onChange={(value) => change("enable", value)}
                      />
                    </SettingRow>
                  )}

                  {isNew && (form.isCron === 2 || form.enable) && (
                    <div className="run-after-create">
                      <Checkbox
                        checked={runAfterCreate}
                        onChange={(event) =>
                          setRunAfterCreate(Boolean(event.target.checked))
                        }
                      >
                        创建后立即执行一次
                      </Checkbox>
                      {runAfterCreate && form.method === 2 && (
                        <span className="field-hint">
                          移动模式将在确认后开始执行
                        </span>
                      )}
                    </div>
                  )}

                  <SettingRow
                    label="限制允许运行时间窗口"
                    variant="bordered"
                    tip="开启后，定时调度仅在指定时段内启动（支持跨午夜，如 23:00 至 06:00），方便错峰利用夜间闲时带宽"
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
                        label="超出时间窗口时自动中止运行中的任务"
                        variant="bordered"
                        tip="关闭则允许已在窗口内启动的任务运行完毕；开启则在离开窗口后自动停止任务"
                      >
                        <Switch
                          aria-label="超出时间窗口时自动中止运行中的任务"
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
                      hint="该任务的完成与审批通知仅发送到所选渠道"
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
                        <div className="form-grid form-grid--three">
                          <Field label="操作并发覆盖" hint="0 = 跟随全局设置 (1–100)">
                            <Input
                              inputMode="numeric"
                              value={String(form.copyConcurrency)}
                              onChange={(v) =>
                                change(
                                  "copyConcurrency",
                                  Math.max(0, Math.min(100, Math.trunc(Number(v) || 0))),
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
                                  Math.max(0, Math.min(50, Math.trunc(Number(v) || 0))),
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
                                  change("maxRetries", Math.max(-1, Math.min(10, n)));
                                }
                              }}
                            />
                          </Field>
                        </div>
                        <Field
                          label="带宽限制（--bwlimit）"
                              hint="留空跟随系统设置；支持单速（如 10M）或分时段（如 08:00,5M 23:00,off）"
                            >
                              <div className="field-group">
                                <Input
                                  value={form.bwLimit}
                                  placeholder="如 10M 或 08:00,5M 23:00,off"
                                  showClear
                                  onChange={(v) => change("bwLimit", v)}
                                />
                                <div className="schedule-chips">
                                  {[
                                    { label: "跟随全局", value: "" },
                                    { label: "5M", value: "5M" },
                                    { label: "20M", value: "20M" },
                                    { label: "50M", value: "50M" },
                                    { label: "日间限 5M", value: "08:00,5M 23:00,off" },
                                  ].map((preset) => (
                                    <button
                                      key={preset.label}
                                      type="button"
                                      className={`schedule-chip${form.bwLimit.trim() === preset.value ? " is-active" : ""}`}
                                      onClick={() => change("bwLimit", preset.value)}
                                    >
                                      {preset.label}
                                    </button>
                                  ))}
                                </div>
                              </div>
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
                                      Number.isFinite(n) && n >= 0 ? Math.min(1000, n) : 0,
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
                                      Math.max(0, Math.min(1000, Math.trunc(Number(v) || 0))),
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
                                      Math.max(0, Math.min(64, Math.trunc(Number(v) || 0))),
                                    )
                                  }
                                />
                              </Field>
                            </div>
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
                                onChange={(e) =>
                                  change("extraFlags", {
                                    ...form.extraFlags,
                                    trackRenames: Boolean(e.target.checked),
                                  })
                                }
                              >
                                启用重命名追踪，避免重命名后重传（--track-renames）
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
                                      maxBacklog: Math.max(0, Math.trunc(Number(v) || 0)),
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
                            {form.method === 3 && (
                              <>
                                <div className="form-grid form-grid--two">
                                  <Field
                                    label="双向基线冲突策略（--resync-mode）"
                                    hint="首次同步或重建基线 (--resync) 时生效"
                                  >
                                    <Select
                                      value={form.extraFlags?.bisyncResyncMode || ""}
                                      optionList={[
                                        { label: "默认 (newer，保留最新修改)", value: "" },
                                        { label: "path1（以左侧为准）", value: "path1" },
                                        { label: "path2（以右侧为准）", value: "path2" },
                                        { label: "newer（保留最新修改）", value: "newer" },
                                        { label: "older（保留较旧文件）", value: "older" },
                                        { label: "larger（保留较大文件）", value: "larger" },
                                        { label: "smaller（保留较小文件）", value: "smaller" },
                                      ]}
                                      onChange={(v) =>
                                        change("extraFlags", {
                                          ...form.extraFlags,
                                          bisyncResyncMode: String(v || ""),
                                        })
                                      }
                                    />
                                  </Field>
                                  <Field
                                    label="单次删除比例上限（--max-delete，%）"
                                    hint="0 表示使用 rclone 默认值（50%），1–100 按删除占比触发安全保护"
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
                                    超限删除仍强制执行（--force，谨慎启用）
                                  </Checkbox>
                                </div>
                              </>
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
                    {/* ExcludeTree forwards id/aria-*; the group keeps the
                        visible Field label associated with the tree region. */}
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
    // 保留空输入而不是静默回退到 0 导致勾选框被自动取消
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
