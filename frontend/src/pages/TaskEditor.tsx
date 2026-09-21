import { useState } from "react";
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
  methodOptions,
} from "./Home/homeUtils";
import { fileSizeUnitOptions, type FileSizeUnit } from "./Home/fileSizeUnits";
import type { AlistItem, JobItem } from "../types";

type CronFieldName = "second" | "minute" | "hour" | "day" | "month" | "day_of_week";

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
  onClose,
  onSaved,
}: {
  job: JobItem | null;
  engines: AlistItem[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { form, dirty, change, patch, setForm, setDirty } = useEditorForm<JobForm>(() =>
    job ? jobToForm(job) : defaultJobForm(engines[0]?.id),
  );
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
    const validation = validateJobForm(form);
    if (validation) {
      Toast.warning(validation);
      return;
    }
    void action.run(async () => {
      try {
        await api.saveJob(buildJobPayload(form));
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
                      }));
                    }}
                  />
                </Field>
                <div className="form-grid">
                  <Field label="源路径" required>
                    <RemotePaths
                      key={`src-${form.alistId}`}
                      engineId={form.alistId}
                      value={form.srcPath}
                      onChange={(paths) => change("srcPath", paths)}
                      multiple={false}
                    />
                    <SettingRow
                      label="源端缓存"
                      variant="bordered"
                      tip="开启后优先使用引擎缓存目录列表，减少远程请求次数，提升同步速度"
                    >
                      <Switch
                        checked={form.useCacheS}
                        onChange={(value) => change("useCacheS", value)}
                      />
                    </SettingRow>
                  </Field>
                  <Field label="目标路径" required>
                    <RemotePaths
                      key={`dst-${form.alistId}`}
                      engineId={form.alistId}
                      value={form.dstPath}
                      onChange={(paths) => change("dstPath", paths)}
                      multiple={false}
                    />
                    <SettingRow
                      label="目标缓存"
                      variant="bordered"
                      tip="开启后优先使用引擎缓存目录列表，减少远程请求次数，提升同步速度"
                    >
                      <Switch
                        checked={form.useCacheT}
                        onChange={(value) => change("useCacheT", value)}
                      />
                    </SettingRow>
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
                      value={form.method}
                      optionList={methodOptions.map((method, value) => ({
                        value,
                        label: method.name,
                      }))}
                      onChange={(value) => change("method", Number(value))}
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
                      value={String(form.interval)}
                      onChange={(value) => {
                        const n = Number(value);
                        change("interval", Number.isFinite(n) ? n : 0);
                      }}
                    />
                  </Field>
                )}
                {form.isCron === 1 && (
                  <div className="cron-grid">
                    {cronFields.map((field) => (
                      <Field key={field.name} label={field.label}>
                        <Input
                          value={form[field.name as CronFieldName]}
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
                <div className="file-size-filters" aria-label="文件大小过滤">
                  {(["min", "max"] as const).map((kind) => (
                    <FileSizeFilterRow
                      key={kind}
                      kind={kind}
                      form={form}
                      change={change}
                      patch={patch}
                    />
                  ))}
                </div>
                <Field label="文件过滤">
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
                </Field>
                <Field label="系统目录过滤" hint="忽略操作系统和 NAS 的系统目录">
                  <div className="system-dir-groups">
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
}: {
  kind: "min" | "max";
  form: JobForm;
  change: <K extends keyof JobForm>(key: K, value: JobForm[K]) => void;
  patch: (values: Partial<JobForm>) => void;
}) {
  const config = fileSizeFilterDefaults[kind];
  const valueKey = `${kind}FileSize` as const;
  const unitKey = `${kind}FileSizeUnit` as const;
  const currentValue = Number(form[valueKey]);
  const enabled = currentValue > 0;
  const displayValue = enabled ? String(form[valueKey]) : String(config.value);
  const displayUnit = enabled ? form[unitKey] : config.unit;
  const controlLabel = `${config.label}文件大小`;

  return (
    <div className={`file-size-filter-row${enabled ? "" : " is-disabled"}`}>
      <Checkbox
        aria-label={`启用${config.label}指定大小的文件`}
        checked={enabled}
        onChange={(event) => {
          const checked = Boolean(event.target.checked);
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
          const n = Number(value);
          change(valueKey, (Number.isFinite(n) ? n : 0) as JobForm[typeof valueKey]);
        }}
      />
      <Select
        aria-label={`${controlLabel}单位`}
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
