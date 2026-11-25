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
  confirmAction,
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
  const seedForm = useMemo(
    () =>
      initialDraft
        ? initialDraft
        : job
          ? jobToForm(job)
          : defaultJobForm(engines[0]?.id),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );
  const { form, dirty, change, patch, setForm, setDirty } =
    useEditorForm<JobForm>(() => seedForm);
  const nativeEngine = engines.find((engine) => engine.id === form.alistId)?.kind === "rclone";
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
      seedForm.useCacheS ||
        seedForm.useCacheT ||
        seedForm.scanIntervalS ||
        seedForm.scanIntervalT ||
        seedForm.copyConcurrency ||
        seedForm.scanConcurrency ||
        seedForm.maxRetries >= 0,
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
      form.method === 2 &&
      form.dstPath.length > 1
    ) {
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

  const handleEngineChange = (nextValue: unknown) => {
    const nextEngineId = Number(nextValue);
    if (nextEngineId === form.alistId) return;
    const applySwitch = () => {
      setDirty(true);
      setForm((current) => ({
        ...current,
        alistId: nextEngineId,
        ...(engines.find((engine) => engine.id === nextEngineId)?.kind === "rclone"
          ? { method: 0, trashEnable: false, conflictPolicy: 0, useCacheS: false, useCacheT: false, scanIntervalS: 0, scanIntervalT: 0 }
          : {}),
        srcPath: [],
        dstPath: [],
        // 文件夹过滤记录的是旧引擎源目录下的相对路径，换引擎后不再对应
        exclude: updateExcludeFolders(current.exclude, []),
      }));
    };
    const hasDependentSelection =
      form.srcPath.length > 0 ||
      form.dstPath.length > 0 ||
      parseExcludeFolders(form.exclude).length > 0;
    if (hasDependentSelection) {
      confirmAction({
        title: "切换存储引擎？",
        content:
          "切换引擎后，当前已选的源路径、目标路径和文件夹过滤规则会被清空。",
        okText: "切换",
        onOk: applySwitch,
      });
      return;
    }
    applySwitch();
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

  const dstMultiple = form.method !== 2 || form.dstPath.length > 1;
  const activeMethodOption = methodOptions[form.method] ?? methodOptions[0];
  const isDestructiveMethod = form.method === 1 || form.method === 2;

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
    (form.useCacheS ? 1 : 0) +
    (form.useCacheT ? 1 : 0) +
    (Number(form.scanIntervalS || 0) > 0 ? 1 : 0) +
    (Number(form.scanIntervalT || 0) > 0 ? 1 : 0) +
    (Number(form.copyConcurrency || 0) > 0 ? 1 : 0) +
    (Number(form.scanConcurrency || 0) > 0 ? 1 : 0) +
    (Number(form.maxRetries) >= 0 ? 1 : 0);
  const activePolicyCount =
    (form.compareMode !== 0 ? 1 : 0) +
    (form.conflictPolicy !== 0 ? 1 : 0) +
    (form.trashEnable ? 1 : 0) +
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
                  <div className="form-grid form-grid--two">
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
                    <Field
                      label="存储引擎"
                      required
                      error={showStepError(0, "alistId")}
                      fieldId="alistId"
                    >
                      <Select
                        value={form.alistId}
                        placeholder={
                          engines.length ? "请选择引擎" : "暂无可用存储引擎"
                        }
                        disabled={!engines.length}
                        optionList={engines.map((engine) => ({
                          label: engine.kind==="rclone" ? `${engine.remark || "原生备份"} · rclone` : `${engine.remark || engine.userName} · ${engine.url}`,
                          value: engine.id,
                        }))}
                        onChange={handleEngineChange}
                      />
                    </Field>
                  </div>

                  {enginesError && (
                    <InlineNotice
                      role="alert"
                      message={`存储引擎加载失败：${enginesError}`}
                      onRetry={onRetryEngines}
                    />
                  )}

                  {!enginesError && engines.length === 0 && (
                    <div className="engine-empty-guide" role="status">
                      <IconInfoCircle aria-hidden="true" size="small" />
                      <span>
                        还未添加存储引擎，请先添加引擎后再配置同步路径。
                      </span>
                      {onNavigateEngines && (
                        <Button
                          size="small"
                          type="primary"
                          theme="light"
                          onClick={onNavigateEngines}
                        >
                          前往引擎管理
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
                        optionList={(nativeEngine ? methodOptions.slice(0,1) : methodOptions).map((method, value) => ({
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
                      label="源路径"
                      required
                      error={showStepError(0, "srcPath")}
                      fieldId="srcPath"
                    >
                      <RemotePaths
                        key={`src-${form.alistId}`}
                        engineId={form.alistId}
                        value={form.srcPath}
                        onChange={handleSrcPathChange}
                        multiple
                        ariaLabel="选择源目录"
                      />
                    </Field>
                    <Field
                      label={form.method === 2 ? "目标路径（单选）" : "目标路径"}
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

                  {nativeEngine && (
                    <Field label="输入目标路径" hint="仅有存储桶权限或目标文件夹尚不存在时，可直接添加路径。源目录请从本地授权目录中选择。">
                      <div className="field-group">
                        <Input value={nativeTarget} onChange={setNativeTarget} placeholder="/存储名称/存储桶/备份目录" />
                        <Button
                          disabled={(() => {
                            const t = nativeTarget.trim();
                            return !t.startsWith("/") || t === "/" || t.split("/").filter(Boolean).length === 0;
                          })()}
                          onClick={() => {
                            const cleaned = nativeTarget.trim().replace(/\/+$/, "");
                            if (!cleaned || cleaned === "/") return;
                            change("dstPath", Array.from(new Set([...form.dstPath, cleaned])));
                            setNativeTarget("");
                          }}
                        >
                          添加路径
                        </Button>
                      </div>
                    </Field>
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
                      form.method === 2 && form.dstPath.length > 1
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
                        <Field label="同名冲突处理">
                          <Select
                            value={form.conflictPolicy}
                            optionList={(nativeEngine ? conflictPolicyOptions.slice(0,2) : conflictPolicyOptions).map((o) => ({
                              label: o.label,
                              value: o.value,
                            }))}
                            onChange={(v) =>
                              change("conflictPolicy", Number(v))
                            }
                          />
                        </Field>
                        <SettingRow
                          label="启用目标端回收站（软删除）"
                          variant="bordered"
                          tip="开启后，全量删除或覆盖旧文件时先移至目标目录下的回收站子目录，避免误删不可恢复"
                        >
                          <Switch
                            aria-label="启用目标端回收站"
                            disabled={nativeEngine}
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
                      <span>高级选项（目录缓存、防风控限流与并发覆盖）</span>
                      {activeAdvancedCount > 0 && (
                        <Tag size="small" color="blue" type="light">
                          已配置 {activeAdvancedCount} 项
                        </Tag>
                      )}
                    </button>
                    {advancedOpen && (
                      <div className="advanced-options-body">
                        <SettingRow
                          label="源端缓存"
                          variant="bordered"
                          tip="开启后优先使用引擎缓存目录列表，减少远程请求次数，提升同步速度"
                        >
                          <Switch
                            aria-label="源端缓存"
                            disabled={nativeEngine}
                            checked={form.useCacheS}
                            onChange={(value) => change("useCacheS", value)}
                          />
                        </SettingRow>
                        <SettingRow
                          label="目标缓存"
                          variant="bordered"
                          tip="开启后优先使用引擎缓存目录列表，减少远程请求次数，提升同步速度"
                        >
                          <Switch
                            aria-label="目标缓存"
                            disabled={nativeEngine}
                            checked={form.useCacheT}
                            onChange={(value) => change("useCacheT", value)}
                          />
                        </SettingRow>
                        <div className="form-grid form-grid--two">
                          <Field
                            label="源端扫描间隔（秒）"
                            hint="0–300，每次列目录后延迟，防网盘触发 QPS 风控"
                          >
                            <Input
                              inputMode="numeric"
                              disabled={nativeEngine}
                              value={String(form.scanIntervalS)}
                              onChange={(v) =>
                                change(
                                  "scanIntervalS",
                                  Math.max(0, Math.min(300, Math.trunc(Number(v) || 0))),
                                )
                              }
                            />
                          </Field>
                          <Field
                            label="目标端扫描间隔（秒）"
                            hint="0–300，每次列目录后延迟"
                          >
                            <Input
                              inputMode="numeric"
                              disabled={nativeEngine}
                              value={String(form.scanIntervalT)}
                              onChange={(v) =>
                                change(
                                  "scanIntervalT",
                                  Math.max(0, Math.min(300, Math.trunc(Number(v) || 0))),
                                )
                              }
                            />
                          </Field>
                        </div>
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
