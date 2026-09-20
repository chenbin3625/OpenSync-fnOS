import { lazy, Suspense, useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconChevronRightStroked,
  IconCloudStroked,
  IconPause,
  IconPlay,
  IconPlusStroked,
  IconServerStroked,
  IconSettingStroked,
  IconDeleteStroked,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  ActionMenu,
  EmptyState,
  Header,
  Info,
  LoadState,
  confirmDelete,
  confirmStopTask,
  errorToast,
  type MenuAction,
} from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import { selectJob } from "../lib/taskForm";
import {
  formatDuration,
  formatSchedule,
  formatSchedulePlan,
  getJobName,
  methodNames,
  parseJobPathList,
} from "./Home/homeUtils";
import { formatTimestamp } from "../utils/date";
import type { AlistItem, JobItem, TaskRecord } from "../types";

const Realtime = lazy(() =>
  import("./TaskExecution").then((module) => ({ default: module.Realtime })),
);
const History = lazy(() =>
  import("./TaskExecution").then((module) => ({ default: module.History })),
);
const JobEditor = lazy(() => import("./TaskEditor"));

export default function Tasks() {
  const [params, setParams] = useSearchParams();
  const selectedId = Number(params.get("jobId")) || null;
  const tab = ["overview", "realtime", "history"].includes(
    params.get("tab") || "",
  )
    ? params.get("tab")!
    : "overview";
  const jobs = useResource((signal) => api.jobMenu(signal));
  const engines = useResource((signal) => api.engines(signal));
  const [editor, setEditor] = useState<{ open: boolean; job: JobItem | null }>({
    open: false,
    job: null,
  });
  const actions = useAction();
  const list = jobs.data?.dataList || [];
  const selected = selectJob(list, selectedId);
  const currentTask = useResource(
    (signal) =>
      selected ? api.current(selected.id, signal) : Promise.resolve(null),
    [selected?.id],
  );
  const hasCurrentTask = import.meta.env.DEV || !!currentTask.data;
  const update = (values: Record<string, string | number | null>) => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(values))
      value === null ? next.delete(key) : next.set(key, String(value));
    setParams(next);
  };
  const refreshJobs = async () => {
    await jobs.refresh();
    await currentTask.refresh();
    window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
  };
  const action = (operation: () => Promise<unknown>, message: string) =>
    void actions.run(async () => {
      try {
        await operation();
        Toast.success(message);
        await refreshJobs();
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
            <Tabs.TabPane tab="历史任务" itemKey="history" />
            <Tabs.TabPane
              tab={
                !hasCurrentTask ? (
                  <Tooltip content="当前没有运行中的任务" position="top">
                    <span>实时任务</span>
                  </Tooltip>
                ) : (
                  "实时任务"
                )
              }
              itemKey="realtime"
              disabled={!hasCurrentTask}
            />
          </Tabs>
        }
        actions={
          tab === "overview" ? (
              <Button
                theme="solid"
                icon={<IconPlusStroked aria-hidden="true" />}
                onClick={() => setEditor({ open: true, job: null })}
              >
                新建任务
              </Button>
          ) : undefined
        }
      />
      {list.length > 1 && (
        <div className="mobile-job-bar" role="tablist" aria-label="任务列表">
          {list.map((job) => (
            <button
              key={job.id}
              type="button"
              role="tab"
              aria-selected={selected?.id === job.id}
              className={`mobile-job-chip${selected?.id === job.id ? " active" : ""}`}
              onClick={() => update({ jobId: job.id })}
            >
              {getJobName(job)}
            </button>
          ))}
        </div>
      )}
      <div
        className="task-workspace flex-column"
        data-empty={
          (!jobs.loading && !jobs.error && !jobs.data?.count) || undefined
        }
      >
        {!jobs.loading && !jobs.error && !jobs.data?.count ? (
          <EmptyState />
        ) : (
          <section className="task-detail-pane flex-column">
            <LoadState
              loading={jobs.loading}
              error={jobs.error}
              retry={jobs.refresh}
              empty={!list.length}
            />
            {selected && !jobs.error ? (
              <div className="task-tab-content">
                {tab === "overview" && (
                  <Overview
                    job={selected}
                    engines={engines.data || []}
                    busy={actions.busy}
                    running={!!currentTask.data}
                    onRun={() =>
                      action(
                        () => api.jobAction({ id: String(selected.id) }),
                        "已提交执行",
                      )
                    }
                    onStop={() => {
                      if (!currentTask.data) return;
                      const { taskId } = currentTask.data;
                      confirmStopTask(() =>
                        actions.run(async () => {
                          await api.taskAction(taskId, "stop");
                          await refreshJobs();
                        }),
                      );
                    }}
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
                          update({ jobId: null });
                          await refreshJobs();
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
            ) : (
              <div className="task-empty-detail">
                {jobs.error ? "任务加载失败" : <EmptyState />}
              </div>
            )}
          </section>
        )}
      </div>
      {editor.open && (
        <Suspense fallback={null}>
          <JobEditor
            job={editor.job}
            engines={engines.data || []}
            onClose={() => setEditor({ open: false, job: null })}
            onSaved={() => {
              setEditor({ open: false, job: null });
              void refreshJobs();
            }}
          />
        </Suspense>
      )}
    </div>
  );
}

function Overview({
  job,
  engines,
  busy,
  running,
  onRun,
  onStop,
  onToggle,
  onEdit,
  onDelete,
}: {
  job: JobItem;
  engines: AlistItem[];
  busy: boolean;
  running: boolean;
  onRun: () => void;
  onStop: () => void;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const latestExecution = useResource(
    (signal) =>
      api.history(job.id, { pageNum: 1, pageSize: 1 }, signal),
    [job.id],
  );
  const engine = engines.find((e) => e.id === job.alistId);
  const engineName = engine
    ? engine.remark || engine.userName
    : `引擎 #${job.alistId}`;
  const engineHost = engine ? engineHostOf(engine.url) : "";
  const enabled = job.enable === 1;
  const menu: MenuAction[] = [
    {
      label: "设置",
      icon: <IconSettingStroked aria-hidden="true" />,
      onClick: onEdit,
    },
    {
      label: "删除",
      icon: <IconDeleteStroked aria-hidden="true" />,
      onClick: onDelete,
      danger: true,
    },
  ];
  return (
    <div className="overview">
      <section className="overview-card card-base">
        <div className="overview-head">
          <h2 title={getJobName(job)}>{getJobName(job)}</h2>
          <div className="overview-head-actions">
            {job.isCron !== 2 && (
              <Switch
                aria-label="启用任务"
                checked={enabled}
                disabled={busy}
                onChange={() => onToggle()}
              />
            )}
            <Tooltip content={running ? "停止任务" : "启动任务"} position="top">
              <Button
                aria-label={running ? "停止任务" : "启动任务"}
                icon={
                  running ? (
                    <IconPause aria-hidden="true" />
                  ) : (
                    <IconPlay aria-hidden="true" />
                  )
                }
                type={running ? "danger" : "tertiary"}
                theme="borderless"
                size="small"
                disabled={busy}
                onClick={running ? onStop : onRun}
              />
            </Tooltip>
            <ActionMenu actions={menu} disabled={busy} />
          </div>
        </div>
        <div className="overview-flow">
          <div className="flow-node">
            <div className="flow-name">
              <IconServerStroked aria-hidden="true" />
              源目录
            </div>
            <div className="flow-sub mono">
              {parseJobPathList(job.srcPath).join("\n") || "—"}
            </div>
          </div>
          <IconChevronRightStroked className="flow-arrow" aria-hidden="true" />
          <div className="flow-node">
            <div className="flow-name" title={engineHost || engineName}>
              <IconCloudStroked aria-hidden="true" />
              目标目录
            </div>
            <div className="flow-sub mono">
              {parseJobPathList(job.dstPath).join("\n") || "—"}
            </div>
          </div>
          <div className="flow-node flow-next">
            <div className="flow-name">下次执行</div>
            <div className="flow-sub">{formatSchedule(job)}</div>
          </div>
        </div>
        <div className="overview-sections">
          <section className="info-section">
            <h3>规则与调度</h3>
            <Info label="同步方式">{methodNames[job.method]}</Info>
            <Info label="执行计划">{formatSchedulePlan(job)}</Info>
            <Info label="最近一次执行">
              <LatestExecution
                record={latestExecution.data?.dataList?.[0]}
                loading={latestExecution.loading}
                error={latestExecution.error}
              />
            </Info>
          </section>
        </div>
      </section>
    </div>
  );
}

function LatestExecution({
  record,
  loading,
  error,
}: {
  record?: TaskRecord;
  loading: boolean;
  error?: string;
}) {
  if (loading) return "加载中";
  if (error) return "加载失败";
  if (!record) return "暂无执行记录";

  const duration =
    record.runTime && record.createTime
      ? formatDuration(record.runTime - record.createTime)
      : "";

  return (
    <div className="latest-execution">
      <span>{formatTimestamp(record.createTime)}</span>
      {duration && <span>{duration}</span>}
    </div>
  );
}

/** 引擎卡片只展示主机名，完整地址留给悬浮提示。 */
const engineHostOf = (url: string) => {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
};
