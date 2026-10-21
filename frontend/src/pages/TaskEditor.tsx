import { useId, useRef, useState, type MutableRefObject } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { Form } from "@douyinfe/semi-ui/lib/es/form";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import { IconHelpCircleStroked } from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  Editor,
  Field,
  SettingRow,
  errorToast,
} from "../components/common";
import { ExcludeTree } from "../components/ExcludeTree";
import { FileTypeFilter } from "../components/FileTypeFilter";
import { RemotePaths } from "../components/RemotePaths";
import { useAction, useEditorForm } from "../lib/hooks";
import {
  buildJobPayload,
  defaultJobForm,
  jobToForm,
  parseExcludeFolders,
  systemDirFilterGroups,
  updateExcludeFolders,
  validateJobForm,
  validateJobFormStep,
  isExcludePatternEnabled,
  updateExcludePatterns,
  type JobForm,
} from "../lib/taskForm";
import {
  cronFields,
  cronTypeNames,
  formatSchedulePlan,
  legacyMoveMethodLabel,
  methodOptions,
} from "./Home/homeUtils";
import {
  commitFileSizeInput,
  fileSizeUnitOptions,
  parseFileSizeInput,
  type FileSizeUnit,
} from "./Home/fileSizeUnits";
import type { AlistItem, JobItem } from "../types";

type CronFieldName = "second" | "minute" | "hour" | "day" | "month" | "day_of_week";
type FileSizeDraftKey = "minFileSize" | "maxFileSize";
type FileSizeDrafts = Partial<Record<FileSizeDraftKey, string>>;

// The interval input is edited as text: keeping the raw draft lets the user
// clear the field or type a partial number without it being rewritten to 0.
function parseIntervalInput(value: string): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? Math.max(0, Math.trunc(parsed)) : 0;
}

const taskEditorTabs = [
  { key: "engine", label: "引擎与路径" },
  { key: "sync", label: "同步与调度" },
  { key: "filter", label: "文件过滤" },
  { key: "folder", label: "文件夹过滤" },
];
const taskEditorSteps = taskEditorTabs.map((tab) => tab.label);
const fileSizeFilterDefaults = {
  min: { label: "排除小于", value: 10, unit: "KB" },
  max: { label: "排除大于", value: 10, unit: "GB" },
} satisfies Record<"min" | "max", {
  label: string;
  value: number;
  unit: FileSizeUnit;
}>;
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
  engines,
  enginesError,
  onRetryEngines,
  onClose,
  onSaved,
}: {
  job: JobItem | null;
  engines: AlistItem[];
  /** Load error of the engine list; the select would otherwise just be empty. */
  enginesError?: string;
  onRetryEngines?: () => void;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { form, dirty, change, patch, setForm, setDirty } = useEditorForm<JobForm>(() =>
    job ? jobToForm(job) : defaultJobForm(engines[0]?.id),
  );
  const fileSizeDrafts = useRef<FileSizeDrafts>({});
  const [intervalDraft, setIntervalDraft] = useState<string | null>(null);
  // Legacy backend method 2 ("move") is not part of the current two-mode UI,
  // but opening and saving such a job must not silently rewrite it to 0
  // (incremental copy). Until the user touches the select, keep the stored
  // value in both the display and the payload.
  const [methodTouched, setMethodTouched] = useState(false);
  const legacyMethod = job?.method === 2;
  const isNew = !job;
  const [step, setStep] = useState(0);
  const [activeTab, setActiveTab] = useState("engine");
  const action = useAction();
  const lastStep = step === taskEditorSteps.length - 1;
  const activePanel = isNew
    ? taskEditorTabs[step].key
    : activeTab;
  const nextStep = () => {
    const validation = validateJobFormStep(form, step);
    if (validation) {
      Toast.warning(validation);
      return;
    }
    setStep((s) => Math.min(s + 1, taskEditorSteps.length - 1));
  };
  const previousStep = () => setStep((s) => Math.max(s - 1, 0));
  const save = () => {
    const formToSave: JobForm = {
      ...form,
      minFileSize: fileSizeDrafts.current.minFileSize ?? form.minFileSize,
      maxFileSize: fileSizeDrafts.current.maxFileSize ?? form.maxFileSize,
    };
    const validation = validateJobForm(formToSave);
    if (validation) {
      Toast.warning(validation);
      return;
    }
    const payload = buildJobPayload(formToSave);
    if (legacyMethod && !methodTouched && form.method !== 2) {
      // taskForm's normalizeJobMethod may still have folded 2 to 0; the page
      // owns the "don't lose the stored mode" guarantee until the user edits
      // the select.
      payload.method = 2;
    }
    void action.run(async () => {
      try {
        await api.saveJob(payload);
        fileSizeDrafts.current = {};
        setDirty(false);
        Toast.success(job ? "编辑成功，下次任务生效" : "任务已创建");
        onSaved();
      } catch (err) {
        errorToast(err);
      }
    });
  };
  return (
    <Editor
      title={job ? "编辑任务" : "新建任务"}
      visible
      busy={action.busy}
      onClose={onClose}
      onSave={save}
      dirty={dirty}
      footer={({ close }) => (
        <div className={`editor-actions${isNew ? " task-editor-actions" : ""}`}>
          {isNew && step > 0 && (
            <Button onClick={previousStep} disabled={action.busy}>
              上一步
            </Button>
          )}
          <Button onClick={close} disabled={action.busy}>
            取消
          </Button>
          <Button
            type="primary"
            theme="solid"
            loading={action.busy}
            onClick={isNew && !lastStep ? nextStep : save}
          >
            {isNew && !lastStep ? "下一步" : "保存"}
          </Button>
        </div>
      )}
    >
      <Form onSubmit={isNew && !lastStep ? nextStep : save} className="editor-form">
        <fieldset disabled={action.busy}>
          {isNew ? (
            <div className="task-editor-stepbar">
              <h3>{taskEditorSteps[step]}</h3>
              <div className="task-editor-count" aria-label="当前步骤">
                步骤 <strong>{step + 1}</strong> / {taskEditorSteps.length}
              </div>
            </div>
          ) : (
            <Tabs
              type="line"
              activeKey={activeTab}
              onChange={(key) => setActiveTab(key)}
            >
              {taskEditorTabs.map((tab) => (
                <Tabs.TabPane tab={tab.label} itemKey={tab.key} key={tab.key} />
              ))}
            </Tabs>
          )}
          <div className="task-editor-step">
            {activePanel === "engine" && (
              <div className="form-section">
                <Field label="任务名称" required>
                  <Input
                    value={form.remark}
                    onChange={(value) => change("remark", value)}
                    placeholder="相册每日备份"
                  />
                </Field>
                <Field label="存储引擎" required>
                  <Select
                    value={form.alistId}
                    placeholder="请选择引擎"
                    optionList={engines.map((engine) => ({
                      label: `${engine.remark || engine.userName} · ${engine.url}`,
                      value: engine.id,
                    }))}
                    onChange={(value) => {
                      setDirty(true);
                      setForm((current) => ({
                        ...current,
                        alistId: Number(value),
                        srcPath: [],
                        dstPath: [],
                        // 文件夹过滤记录的是旧引擎源目录下的相对路径，换引擎后不再对应
                        exclude: updateExcludeFolders(current.exclude, []),
                      }));
                    }}
                  />
                </Field>
                {enginesError && (
                  <div className="inline-error" role="alert">
                    存储引擎加载失败：{enginesError}
                    {onRetryEngines && (
                      <Button size="small" theme="borderless" onClick={onRetryEngines}>
                        重试
                      </Button>
                    )}
                  </div>
                )}
                <div className="form-grid">
                  <Field label="源路径" required>
                    <div className="field-group" role="group">
                      <RemotePaths
                        key={`src-${form.alistId}`}
                        engineId={form.alistId}
                        value={form.srcPath}
                        onChange={(paths) => change("srcPath", paths)}
                        multiple={false}
                        ariaLabel="选择源目录"
                      />
                      <SettingRow
                        label="源端缓存"
                        variant="bordered"
                        tip="开启后优先使用引擎缓存目录列表，减少远程请求次数，提升同步速度"
                      >
                        <Switch
                          aria-label="源端缓存"
                          checked={form.useCacheS}
                          onChange={(value) => change("useCacheS", value)}
                        />
                      </SettingRow>
                    </div>
                  </Field>
                  <Field label="目标路径" required>
                    <div className="field-group" role="group">
                      <RemotePaths
                        key={`dst-${form.alistId}`}
                        engineId={form.alistId}
                        value={form.dstPath}
                        onChange={(paths) => change("dstPath", paths)}
                        multiple={false}
                        ariaLabel="选择目标目录"
                      />
                      <SettingRow
                        label="目标缓存"
                        variant="bordered"
                        tip="开启后优先使用引擎缓存目录列表，减少远程请求次数，提升同步速度"
                      >
                        <Switch
                          aria-label="目标缓存"
                          checked={form.useCacheT}
                          onChange={(value) => change("useCacheT", value)}
                        />
                      </SettingRow>
                    </div>
                  </Field>
                </div>
              </div>
            )}
            {activePanel === "sync" && (
              <div className="form-section">
                <div className="form-grid">
                  <Field
                    label={
                      <span className="field-label-with-tip">
                        同步方式
                        <Tooltip content={syncMethodTip} trigger="hover">
                          <span
                            className="field-tip-icon"
                            aria-label="同步方式说明"
                            tabIndex={0}
                          >
                            <IconHelpCircleStroked aria-hidden="true" />
                          </span>
                        </Tooltip>
                      </span>
                    }
                    ariaLabel="同步方式"
                  >
                    <Select
                      value={
                        legacyMethod && !methodTouched ? 2 : form.method
                      }
                      optionList={[
                        ...methodOptions.map((method, value) => ({
                          value,
                          label: method.name,
                        })),
                        ...(legacyMethod || form.method === 2
                          ? [
                              {
                                value: 2,
                                label: legacyMoveMethodLabel,
                              },
                            ]
                          : []),
                      ]}
                      onChange={(value) => {
                        setMethodTouched(true);
                        change("method", Number(value));
                      }}
                    />
                  </Field>
                  <Field label="调度方式">
                    <Select
                      value={form.isCron}
                      optionList={cronTypeNames.map((label, value) => ({
                        value,
                        label,
                      }))}
                      onChange={(value) => change("isCron", Number(value))}
                    />
                  </Field>
                </div>
                {form.isCron === 0 && (
                  <Field label="执行间隔（分钟）">
                    <Input
                      inputMode="numeric"
                      value={intervalDraft ?? String(form.interval)}
                      onChange={(value) => {
                        // 保留原始输入，清空或输入到一半时不被强制改写成 0
                        setIntervalDraft(value);
                        change("interval", parseIntervalInput(value));
                      }}
                      onBlur={() => setIntervalDraft(null)}
                    />
                  </Field>
                )}
                {form.isCron === 1 && (
                  <div className="cron-grid">
                    {cronFields.map((field) => (
                      <Field key={field.name} label={field.label}>
                        <Input
                          value={form[field.name as CronFieldName]}
                          placeholder={field.placeholder}
                          onChange={(value) =>
                            change(
                              field.name as CronFieldName,
                              value,
                            )
                          }
                        />
                      </Field>
                    ))}
                  </div>
                )}
                <div className="schedule-preview">
                  {formatSchedulePlan(form)}
                </div>
              </div>
            )}
            {activePanel === "filter" && (
              <div className="form-section">
                <div className="file-size-filters" role="group" aria-label="文件大小过滤">
                  {(["min", "max"] as const).map((kind) => (
                    <FileSizeFilterRow
                      key={kind}
                      kind={kind}
                      form={form}
                      change={change}
                      patch={patch}
                      drafts={fileSizeDrafts}
                    />
                  ))}
                </div>
                <Field
                  label="文件过滤"
                  hint="仅支持文件名和文件后缀；文件目录请在“文件夹过滤”中配置"
                >
                  <FileTypeFilter
                    value={form.exclude}
                    onChange={(value) => change("exclude", value)}
                  />
                </Field>
              </div>
            )}
            {activePanel === "folder" && (
              <div className="form-section">
                <Field
                  label="文件夹过滤"
                  hint="仅针对源目录，勾选要忽略的子文件夹"
                >
                  {/* ExcludeTree forwards id/aria-*; the group keeps the
                      visible Field label associated with the tree region. */}
                  <div className="field-group" role="group">
                    <ExcludeTree
                    engineId={form.alistId}
                    srcPaths={form.srcPath}
                    excludedPaths={parseExcludeFolders(form.exclude)}
                      onChange={(folders) =>
                        change(
                          "exclude",
                          updateExcludeFolders(form.exclude, folders),
                        )
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
                            {group.patterns.join("  ")}
                          </span>
                        </div>
                      );
                    })}
                  </div>
                </Field>
              </div>
            )}
          </div>
        </fieldset>
      </Form>
    </Editor>
  );
}

function FileSizeFilterRow({
  kind,
  form,
  change,
  patch,
  drafts,
}: {
  kind: "min" | "max";
  form: JobForm;
  change: <K extends keyof JobForm>(key: K, value: JobForm[K]) => void;
  patch: (values: Partial<JobForm>) => void;
  drafts: MutableRefObject<FileSizeDrafts>;
}) {
  const config = fileSizeFilterDefaults[kind];
  const valueKey = `${kind}FileSize` as const;
  const unitKey = `${kind}FileSizeUnit` as const;
  const parsedValue = parseFileSizeInput(form[valueKey]);
  const currentValue = parsedValue ?? 0;
  const [draft, setDraft] = useState<string | null>(null);
  const editing = draft !== null;
  const enabled = editing || parsedValue === null || currentValue > 0;
  const displayValue = editing
    ? draft
    : enabled
      ? String(form[valueKey])
      : String(config.value);
  const displayUnit = enabled ? form[unitKey] : config.unit;
  const controlLabel = `${config.label}文件大小`;
  const unitLabelId = useId();

  const commitInput = (raw: string) => {
    setDraft(null);
    delete drafts.current[valueKey];
    change(valueKey, commitFileSizeInput(raw));
  };

  return (
    <div className={`file-size-filter-row${enabled ? "" : " is-disabled"}`}>
      <Checkbox
        aria-label={`启用${config.label}指定大小的文件`}
        checked={enabled}
        onChange={(event) => {
          const checked = Boolean(event.target.checked);
          setDraft(null);
          delete drafts.current[valueKey];
          patch({
            [valueKey]: checked
              ? currentValue > 0
                ? currentValue
                : config.value
              : 0,
            [unitKey]: checked ? displayUnit : config.unit,
          });
        }}
      />
      <span className="file-size-filter-copy">{config.label}</span>
      <Input
        aria-label={controlLabel}
        inputMode="decimal"
        disabled={!enabled}
        value={displayValue}
        onChange={(value) => {
          setDraft(value);
          drafts.current[valueKey] = value;
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
