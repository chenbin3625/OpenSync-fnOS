import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { Form } from "@douyinfe/semi-ui/lib/es/form";
import Input from "@douyinfe/semi-ui/lib/es/input";
import InputNumber from "@douyinfe/semi-ui/lib/es/inputNumber";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Pagination from "@douyinfe/semi-ui/lib/es/pagination";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconPlus,
  IconPlay,
  IconEdit,
  IconDelete,
  IconChevronLeft,
  IconSearch,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  Editor,
  EmptyState,
  Field,
  Header,
  IconButton,
  Info,
  LoadState,
  SettingRow,
  confirmDelete,
  errorToast,
} from "../components/common";
import { RemotePaths } from "../components/RemotePaths";
import { useAction, useResource } from "../lib/hooks";
import {
  buildJobPayload,
  defaultJobForm,
  jobToForm,
  validateJobForm,
  type JobForm,
} from "../lib/taskForm";
import {
  countJobPaths,
  cronFields,
  cronTypeNames,
  formatFileSizeRange,
  formatJobPaths,
  formatSchedule,
  formatSchedulePlan,
  getJobName,
  methodNames,
  methodOptions,
} from "./Home/homeUtils";
import { fileSizeUnitOptions } from "./Home/fileSizeUnits";
import type { AlistItem, JobItem } from "../types";
import { History, Realtime } from "./TaskExecution";

export default function Tasks() {
  const [params, setParams] = useSearchParams();
  const page = Math.max(1, Number(params.get("page")) || 1);
  const selectedId = Number(params.get("jobId")) || null;
  const tab = ["overview", "realtime", "history"].includes(
    params.get("tab") || "",
  )
    ? params.get("tab")!
    : "overview";
  const jobs = useResource((signal) => api.jobs(page, signal), [page]);
  const engines = useResource((signal) => api.engines(signal));
  const [editor, setEditor] = useState<{ open: boolean; job: JobItem | null }>({
    open: false,
    job: null,
  });
  const [search, setSearch] = useState("");
  const [mobileDetail, setMobileDetail] = useState(Boolean(selectedId));
  const actions = useAction();
  const list = jobs.data?.dataList || [];
  const selected = list.find((j) => j.id === selectedId) || list[0];
  const update = (values: Record<string, string | number | null>) => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(values))
      value === null ? next.delete(key) : next.set(key, String(value));
    setParams(next);
  };
  useEffect(() => {
    if (!jobs.data) return;
    const maximum = Math.max(1, Math.ceil(jobs.data.count / 12));
    if (page > maximum) {
      const next = new URLSearchParams(params);
      next.set("page", String(maximum));
      next.delete("jobId");
      setParams(next, { replace: true });
    }
  }, [jobs.data, page]);
  useEffect(() => {
    setMobileDetail(Boolean(selectedId));
  }, [selectedId]);
  const action = (operation: () => Promise<unknown>, message: string) =>
    void actions.run(async () => {
      try {
        await operation();
        Toast.success(message);
        await jobs.refresh();
      } catch (err) {
        errorToast(err);
      }
    });
  return (
    <div className="page task-page">
      <Header
        title="任务管理"
        tabs={
          <Tabs
            activeKey={tab}
            onChange={(key) => update({ tab: key })}
            keepDOM={false}
          >
            <Tabs.TabPane tab="总览" itemKey="overview" />
            <Tabs.TabPane tab="实时任务" itemKey="realtime" />
            <Tabs.TabPane tab="历史任务" itemKey="history" />
          </Tabs>
        }
        actions={
          <>
            <Button
              icon={<IconPlay aria-hidden="true" />}
              disabled={actions.busy || !jobs.data?.count}
              onClick={() =>
                action(() => api.jobAction({}), "已提交执行全部任务")
              }
            >
              执行全部
            </Button>
            <Button
              theme="solid"
              icon={<IconPlus aria-hidden="true" />}
              onClick={() => setEditor({ open: true, job: null })}
            >
              新建任务
            </Button>
          </>
        }
      />
      <div
        className={`task-workspace ${mobileDetail ? "show-task-detail" : ""}`}
        data-empty={
          (!jobs.loading && !jobs.error && !jobs.data?.count) || undefined
        }
      >
        {!jobs.loading && !jobs.error && !jobs.data?.count ? (
          <EmptyState />
        ) : (
          <>
            <section className="task-list-pane" aria-label="同步任务列表">
              <div className="task-search">
                <Input
                  prefix={<IconSearch aria-hidden="true" />}
                  placeholder="筛选当前页任务"
                  aria-label="筛选任务"
                  value={search}
                  onChange={setSearch}
                  showClear
                />
              </div>
              <LoadState
                loading={jobs.loading}
                error={jobs.error}
                retry={jobs.refresh}
                empty={!list.length}
              />
              {!jobs.loading && !jobs.error && (
                <div className="task-list">
                  {list
                    .filter((j) =>
                      `${getJobName(j)} ${j.srcPath} ${j.dstPath}`
                        .toLowerCase()
                        .includes(search.toLowerCase()),
                    )
                    .map((job) => (
                      <button
                        key={job.id}
                        type="button"
                        className={`task-list-item ${selected?.id === job.id ? "selected" : ""}`}
                        aria-pressed={selected?.id === job.id}
                        onClick={() => {
                          update({ jobId: job.id });
                          setMobileDetail(true);
                        }}
                      >
                        <div className="task-item-heading">
                          <strong title={getJobName(job)}>
                            {getJobName(job)}
                          </strong>
                          <Tag
                            size="small"
                            color={job.enable ? "green" : "grey"}
                          >
                            {job.enable ? "已启用" : "已暂停"}
                          </Tag>
                        </div>
                        <div className="task-item-meta">
                          <span>{methodNames[job.method]}</span>
                          <span>{formatSchedule(job)}</span>
                        </div>
                        <div
                          className="mono task-item-path"
                          title={formatJobPaths(job.srcPath)}
                        >
                          {formatJobPaths(job.srcPath)}
                        </div>
                      </button>
                    ))}
                  {list.length > 0 &&
                    !list.some((j) =>
                      `${getJobName(j)} ${j.srcPath} ${j.dstPath}`
                        .toLowerCase()
                        .includes(search.toLowerCase()),
                    ) && <EmptyState />}
                </div>
              )}
              {!!jobs.data?.count && (
                <div className="pane-pagination">
                  <Pagination
                    total={jobs.data.count}
                    currentPage={page}
                    pageSize={12}
                    size="small"
                    onPageChange={(p) => {
                      update({ page: p, jobId: null });
                      setSearch("");
                    }}
                  />
                </div>
              )}
            </section>
            <section className="task-detail-pane">
              {selected && !jobs.error ? (
                <>
                  <div className="mobile-detail-heading">
                    <Button
                      icon={<IconChevronLeft aria-hidden="true" />}
                      theme="borderless"
                      onClick={() => {
                        setMobileDetail(false);
                        update({ jobId: null });
                      }}
                    >
                      任务列表
                    </Button>
                  </div>
                  <div className="task-tab-content">
                    {tab === "overview" && (
                      <Overview
                        job={selected}
                        engines={engines.data || []}
                        busy={actions.busy}
                        onRun={() =>
                          action(
                            () => api.jobAction({ id: String(selected.id) }),
                            "已提交执行",
                          )
                        }
                        onToggle={() =>
                          action(
                            () =>
                              api.jobAction({
                                id: String(selected.id),
                                pause: selected.enable === 1,
                              }),
                            "任务状态已更新",
                          )
                        }
                        onEdit={() => setEditor({ open: true, job: selected })}
                        onDelete={() =>
                          confirmDelete(
                            "删除此同步任务？",
                            async () => {
                              await api.deleteJob(selected.id);
                              await jobs.refresh();
                              setMobileDetail(false);
                            },
                            "此任务的历史执行记录将一并删除。",
                          )
                        }
                      />
                    )}
                    {tab === "realtime" && (
                      <Realtime key={selected.id} jobId={selected.id} />
                    )}
                    {tab === "history" && (
                      <History key={selected.id} jobId={selected.id} />
                    )}
                  </div>
                </>
              ) : (
                <div className="task-empty-detail">
                  {jobs.error ? "任务加载失败" : <EmptyState />}
                </div>
              )}
            </section>
          </>
        )}
      </div>
      {editor.open && (
        <JobEditor
          job={editor.job}
          engines={engines.data || []}
          onClose={() => setEditor({ open: false, job: null })}
          onSaved={() => {
            setEditor({ open: false, job: null });
            void jobs.refresh();
          }}
        />
      )}
    </div>
  );
}

function Overview({
  job,
  engines,
  busy,
  onRun,
  onToggle,
  onEdit,
  onDelete,
}: {
  job: JobItem;
  engines: AlistItem[];
  busy: boolean;
  onRun: () => void;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const engine = engines.find((e) => e.id === job.alistId);
  return (
    <div className="overview">
      <div className="task-summary">
        <div className="summary-title">
          <h2 title={getJobName(job)}>{getJobName(job)}</h2>
          <Tag color="blue">{methodNames[job.method]}</Tag>
          <Tag color={job.enable ? "green" : "grey"}>
            {job.enable ? "已启用" : "已暂停"}
          </Tag>
        </div>
        <div className="summary-meta">
          <span>源目录 {countJobPaths(job.srcPath)} 个</span>
          <span>目标目录 {countJobPaths(job.dstPath)} 个</span>
          <span>{formatSchedule(job)}</span>
        </div>
        <div className="summary-actions">
          {job.isCron !== 2 && (
            <SettingRow label="任务开关">
              <Switch
                aria-label="切换任务启用状态"
                checked={job.enable === 1}
                disabled={busy}
                onChange={onToggle}
              />
            </SettingRow>
          )}
          <Button
            icon={<IconPlay aria-hidden="true" />}
            theme="solid"
            disabled={busy}
            onClick={onRun}
          >
            手动执行
          </Button>
          <IconButton
            aria-hidden="true"
            label="编辑任务"
            icon={<IconEdit aria-hidden="true" />}
            onClick={onEdit}
            disabled={busy}
          />
          <IconButton
            aria-hidden="true"
            label="删除任务"
            icon={<IconDelete aria-hidden="true" />}
            onClick={onDelete}
            disabled={busy}
            danger
          />
        </div>
      </div>
      <div className="overview-sections">
        <section className="info-section">
          <h3>存储与路径</h3>
          <Info label="存储引擎">
            {engine ? engine.remark || engine.userName : `引擎 #${job.alistId}`}
          </Info>
          <Info label="源目录" mono>
            {formatJobPaths(job.srcPath, "\n")}
          </Info>
          <Info label="目标目录" mono>
            {formatJobPaths(job.dstPath, "\n")}
          </Info>
          <Info label="源端缓存">{job.useCacheS ? "已开启" : "已关闭"}</Info>
          <Info label="目标缓存">{job.useCacheT ? "已开启" : "已关闭"}</Info>
        </section>
        <section className="info-section">
          <h3>规则与调度</h3>
          <Info label="同步方式">{methodNames[job.method]}</Info>
          <Info label="执行计划">{formatSchedulePlan(job)}</Info>
          <Info label="文件大小">
            {formatFileSizeRange(job.minFileSize, job.maxFileSize) || "不限制"}
          </Info>
          <Info label="排除规则">
            <details className="exclude-details">
              <summary>{job.exclude ? "查看规则" : "无排除规则"}</summary>
              {job.exclude && <pre>{job.exclude}</pre>}
            </details>
          </Info>
        </section>
      </div>
    </div>
  );
}

function JobEditor({
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
  const [error, setError] = useState("");
  const action = useAction();
  const change = <K extends keyof JobForm>(key: K, value: JobForm[K]) => {
    setDirty(true);
    setForm((f) => ({ ...f, [key]: value }));
  };
  const save = () => {
    const validation = validateJobForm(form);
    setError(validation);
    if (validation) return;
    void action.run(async () => {
      try {
        await api.saveJob(buildJobPayload(form));
        setDirty(false);
        Toast.success(job ? "编辑成功，下次任务生效" : "任务已创建");
        onSaved();
      } catch (err) {
        setError(err instanceof Error ? err.message : "保存失败");
      }
    });
  };
  return (
    <Editor
      title={job ? "编辑同步任务" : "新建同步任务"}
      visible
      busy={action.busy}
      onClose={onClose}
      onSave={save}
      dirty={dirty}
      saveLabel="保存任务配置"
    >
      <Form onSubmit={save} className="editor-form">
        <fieldset disabled={action.busy}>
          {error && (
            <Banner type="danger" description={error} closeIcon={null} />
          )}
          <div className="form-section">
            <h3>引擎与路径</h3>
            <Field label="存储引擎" required>
              <Select
                value={form.alistId}
                placeholder="请选择引擎"
                optionList={engines.map((e) => ({
                  label: `${e.remark || e.userName} · ${e.url}`,
                  value: e.id,
                }))}
                onChange={(value) => {
                  setDirty(true);
                  setForm((f) => ({
                    ...f,
                    alistId: Number(value),
                    srcPath: [],
                    dstPath: [],
                  }));
                }}
                style={{ width: "100%" }}
              />
            </Field>
            <div className="form-grid">
              <Field label="源目录" required>
                <RemotePaths
                  key={`src-${form.alistId}`}
                  engineId={form.alistId}
                  value={form.srcPath}
                  onChange={(paths) => change("srcPath", paths)}
                />
                <SettingRow label="源端缓存">
                  <Switch
                    checked={form.useCacheS}
                    onChange={(value) => change("useCacheS", value)}
                  />
                </SettingRow>
              </Field>
              <Field label="目标目录" required>
                <RemotePaths
                  key={`dst-${form.alistId}`}
                  engineId={form.alistId}
                  value={form.dstPath}
                  onChange={(paths) => change("dstPath", paths)}
                />
                <SettingRow label="目标缓存">
                  <Switch
                    checked={form.useCacheT}
                    onChange={(value) => change("useCacheT", value)}
                  />
                </SettingRow>
              </Field>
            </div>
            <Field label="任务备注">
              <Input
                value={form.remark}
                onChange={(value) => change("remark", value)}
                placeholder="相册每日备份"
              />
            </Field>
          </div>
          <div className="form-section">
            <h3>同步与调度</h3>
            <div className="form-grid">
              <Field label="同步方式">
                <Select
                  value={form.method}
                  style={{ width: "100%" }}
                  optionList={methodOptions.map((m, value) => ({
                    value,
                    label: <Tooltip content={m.description}>{m.name}</Tooltip>,
                  }))}
                  onChange={(value) => change("method", Number(value))}
                />
              </Field>
              <Field label="调度方式">
                <Select
                  value={form.isCron}
                  style={{ width: "100%" }}
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
                <InputNumber
                  min={1}
                  value={form.interval}
                  onChange={(value) => change("interval", Number(value))}
                  style={{ width: "100%" }}
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
                        change(field.name as keyof typeof rangesKeys, value)
                      }
                    />
                  </Field>
                ))}
              </div>
            )}
            <div className="schedule-preview">{formatSchedulePlan(form)}</div>
          </div>
          <div className="form-section">
            <h3>文件过滤</h3>
            <div className="form-grid">
              {(["min", "max"] as const).map((kind) => (
                <Field
                  key={kind}
                  label={kind === "min" ? "最小文件大小" : "最大文件大小"}
                  hint="0 表示不限"
                >
                  <div className="unit-input">
                    <InputNumber
                      min={0}
                      step={0.1}
                      value={form[`${kind}FileSize`]}
                      onChange={(value) =>
                        change(`${kind}FileSize`, Number(value))
                      }
                    />
                    <Select
                      aria-label={
                        kind === "min" ? "最小文件大小单位" : "最大文件大小单位"
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
            <Field label="排除规则（.gitignore 格式）">
              <TextArea
                value={form.exclude}
                onChange={(value) => change("exclude", value)}
                rows={5}
                className="mono"
              />
            </Field>
          </div>
          <div className="form-section">
            <h3>任务状态</h3>
            <SettingRow label="任务启用状态">
              <Switch
                disabled={form.isCron === 2}
                checked={form.isCron === 2 || form.enable}
                onChange={(value) => change("enable", value)}
              />
            </SettingRow>
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
