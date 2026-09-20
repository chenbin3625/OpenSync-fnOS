import { useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { Form } from "@douyinfe/semi-ui/lib/es/form";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
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
import { useAction } from "../lib/hooks";
import {
  buildJobPayload,
  defaultJobForm,
  jobToForm,
  parseExcludeFolders,
  updateExcludeFolders,
  validateJobForm,
  validateJobFormStep,
  type JobForm,
} from "../lib/taskForm";
import {
  cronFields,
  cronTypeNames,
  formatSchedulePlan,
  methodOptions,
} from "./Home/homeUtils";
import { fileSizeUnitOptions } from "./Home/fileSizeUnits";
import type { AlistItem, JobItem } from "../types";

const taskEditorSteps = ["引擎与路径", "同步与调度", "文件过滤", "文件夹过滤"];
const taskEditorTabs = [
  { key: "engine", label: "引擎与路径" },
  { key: "sync", label: "同步与调度" },
  { key: "filter", label: "文件过滤" },
  { key: "folder", label: "文件夹过滤" },
];
const fileSizeLimitNote = (
  <span className="field-label-inline-note">
    <span>0 表示不限</span>
  </span>
);
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
  const [form, setForm] = useState<JobForm>(() =>
    job ? jobToForm(job) : defaultJobForm(engines[0]?.id),
  );
  const [dirty, setDirty] = useState(false);
  const isNew = !job;
  const [step, setStep] = useState(0);
  const [activeTab, setActiveTab] = useState("engine");
  const action = useAction();
  const lastStep = step === taskEditorSteps.length - 1;
  const activePanel = isNew
    ? ["engine", "sync", "filter", "folder"][step]
    : activeTab;
  const change = <K extends keyof JobForm>(key: K, value: JobForm[K]) => {
    setDirty(true);
    setForm((current) => ({ ...current, [key]: value }));
  };
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
                    <SettingRow label="源端缓存" variant="bordered">
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
                    <SettingRow label="目标缓存" variant="bordered">
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
                          value={form[field.name as keyof typeof rangesKeys]}
                          onChange={(value) =>
                            change(
                              field.name as keyof typeof rangesKeys,
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
                <div className="form-grid">
                  {(["min", "max"] as const).map((kind) => (
                    <Field
                      key={kind}
                      label={
                        <span className="field-label-with-note">
                          <span>
                            {kind === "min" ? "最小文件大小" : "最大文件大小"}
                          </span>
                          {fileSizeLimitNote}
                        </span>
                      }
                      ariaLabel={
                        kind === "min" ? "最小文件大小" : "最大文件大小"
                      }
                    >
                      <div className="unit-input">
                        <Input
                          inputMode="decimal"
                          value={String(form[`${kind}FileSize`])}
                          onChange={(value) => {
                            const n = Number(value);
                            change(
                              `${kind}FileSize`,
                              Number.isFinite(n) ? n : 0,
                            );
                          }}
                        />
                        <Select
                          aria-label={
                            kind === "min"
                              ? "最小文件大小单位"
                              : "最大文件大小单位"
                          }
                          value={form[`${kind}FileSizeUnit`]}
                          optionList={fileSizeUnitOptions}
                          onChange={(value) =>
                            change(`${kind}FileSizeUnit`, String(value))
                          }
                        />
                      </div>
                    </Field>
                  ))}
                </div>
                <Field label="文件类型过滤">
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
              </div>
            )}
          </div>
        </fieldset>
      </Form>
    </Editor>
  );
}

const rangesKeys = {
  second: true,
  minute: true,
  hour: true,
  day: true,
  month: true,
  day_of_week: true,
};
