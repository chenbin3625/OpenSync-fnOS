import { useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { Form } from "@douyinfe/semi-ui/lib/es/form";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import { api } from "../api/client";
import {
  Editor,
  Field,
  SettingRow,
  errorToast,
} from "../components/common";
import { ExcludeTree } from "../components/ExcludeTree";
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

const taskEditorSteps = ["引擎与路径", "同步与调度", "文件过滤"];

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
  const [step, setStep] = useState(0);
  const action = useAction();
  const activeStep = taskEditorSteps[step];
  const lastStep = step === taskEditorSteps.length - 1;
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
    setStep((current) => Math.min(current + 1, taskEditorSteps.length - 1));
  };
  const previousStep = () => setStep((current) => Math.max(current - 1, 0));
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
        <div className="editor-actions task-editor-actions">
          {step > 0 && (
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
            onClick={lastStep ? save : nextStep}
          >
            {lastStep ? "保存任务配置" : "下一步"}
          </Button>
        </div>
      )}
    >
      <Form onSubmit={lastStep ? save : nextStep} className="editor-form">
        <fieldset disabled={action.busy}>
          <div className="task-editor-stepbar">
            <h3>{activeStep}</h3>
            <div className="task-editor-count" aria-label="当前步骤">
              步骤 <strong>{step + 1}</strong> / {taskEditorSteps.length}
            </div>
          </div>
          <div className="task-editor-step">
            {step === 0 && (
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
            {step === 1 && (
              <div className="form-section">
                <div className="form-grid">
                  <Field label="同步方式">
                    <Select
                      value={form.method}
                      optionList={methodOptions.map((method, value) => ({
                        value,
                        label: (
                          <Tooltip content={method.description}>
                            {method.name}
                          </Tooltip>
                        ),
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
            {step === 2 && (
              <div className="form-section">
                <div className="form-grid">
                  {(["min", "max"] as const).map((kind) => (
                    <Field
                      key={kind}
                      label={kind === "min" ? "最小文件大小" : "最大文件大小"}
                      hint="0 表示不限"
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
                <Field label="文件夹过滤" hint="勾选要忽略的文件夹，源和目标同时生效">
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
                <Field label="排除规则（.gitignore 格式）">
                  <TextArea
                    value={form.exclude}
                    onChange={(value) => change("exclude", value)}
                    rows={5}
                    className="mono"
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
