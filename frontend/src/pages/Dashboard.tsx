import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconCopy,
  IconEyeOpenedStroked,
  IconPause,
  IconPlay,
  IconPlusStroked,
  IconRefresh,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  EmptyState,
  Header,
  IconButton,
  LoadState,
  LoadingPanel,
  SearchFilterInput,
  SectionCard,
  Status,
  confirmAction,
  errorToast,
} from "../components/common";
import { DryRunModal } from "../components/DryRunModal";
import { useAction, useResource } from "../lib/hooks";
import { cloneJobToForm } from "../lib/taskForm";
import {
  formatSize,
  methodLabel,
  parseJobPathList,
  taskRecordStatusNames,
} from "./Home/homeUtils";
import { formatTimestamp } from "../utils/date";
import type { DashboardJobItem, JobItem } from "../types";
import type { TaskEditorSavedResult } from "./TaskEditor";

const JobEditor = lazy(() => import("./TaskEditor"));

type StatusFilter =
  | "all"
  | "running"
  | "queued"
  | "approval"
  | "failed"
  | "disabled";

export default function Dashboard() {
  const navigate = useNavigate();
  const dashboard = useResource((signal) => api.dashboard(signal));
  const jobsList = useResource((signal) => api.jobMenu(signal));
  const engines = useResource((signal) => api.engines(signal));
  const action = useAction();

  const [searchInput, setSearchInput] = useState("");
  const [keyword, setKeyword] = useState("");
  const [engineFilter, setEngineFilter] = useState<string>("all");
  const [methodFilter, setMethodFilter] = useState<string>("all");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [selectedIds, setSelectedIds] = useState<number[]>([]);
  const [dryRunJob, setDryRunJob] = useState<{ id: number; name: string } | null>(
    null,
  );
  const [editorState, setEditorState] = useState<{
    open: boolean;
    job: JobItem | null;
    initialDraft?: ReturnType<typeof cloneJobToForm> | null;
  }>({ open: false, job: null, initialDraft: null });

  const refreshAll = useCallback(
    async (silent = true) => {
      await Promise.all([
        dashboard.refresh(silent),
        jobsList.refresh(silent),
        engines.refresh(silent),
      ]);
      window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    },
    [dashboard.refresh, jobsList.refresh, engines.refresh],
  );

  // Periodic background refresh when page is visible
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void dashboard.refresh(true);
      }
    }, 5000);
    return () => window.clearInterval(timer);
  }, [dashboard.refresh]);

  const data = dashboard.data;
  const allJobs = data?.jobs || [];
  const fullJobMap = useMemo(() => {
    const map = new Map<number, JobItem>();
    for (const j of jobsList.data?.dataList || []) map.set(j.id, j);
    return map;
  }, [jobsList.data]);

  const filteredJobs = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    return allJobs.filter((job) => {
      if (engineFilter !== "all" && String(job.alistId) !== engineFilter)
        return false;
      if (methodFilter !== "all" && String(job.method) !== methodFilter)
        return false;
      if (statusFilter === "running" && !job.running) return false;
      if (statusFilter === "queued" && !job.queued) return false;
      if (statusFilter === "approval" && !job.pendingApproval) return false;
      if (
        statusFilter === "failed" &&
        (!job.lastTask || ![3, 5, 6, 7].includes(job.lastTask.status))
      )
        return false;
      if (statusFilter === "disabled" && job.enable === 1) return false;
      if (kw) {
        const hay = `${job.remark || ""} #${job.id} ${job.alistName} ${job.srcPath} ${job.dstPath}`.toLowerCase();
        if (!hay.includes(kw)) return false;
      }
      return true;
    });
  }, [allJobs, engineFilter, methodFilter, statusFilter, keyword]);

  const selectedSet = useMemo(() => new Set(selectedIds), [selectedIds]);
  const visibleAllSelected =
    filteredJobs.length > 0 &&
    filteredJobs.every((j) => selectedSet.has(j.id));
  const visibleSomeSelected =
    filteredJobs.some((j) => selectedSet.has(j.id)) && !visibleAllSelected;

  const toggleSelectAllVisible = (checked: boolean) => {
    if (checked) {
      setSelectedIds(filteredJobs.map((j) => j.id));
    } else {
      const visible = new Set(filteredJobs.map((j) => j.id));
      setSelectedIds((prev) => prev.filter((id) => !visible.has(id)));
    }
  };

  const toggleSelect = (id: number, checked: boolean) => {
    setSelectedIds((prev) =>
      checked
        ? prev.includes(id)
          ? prev
          : [...prev, id]
        : prev.filter((item) => item !== id),
    );
  };

  const runBatch = (
    act: "run" | "stop" | "enable" | "disable" | "run_all" | "stop_all",
    ids?: number[],
    label?: string,
  ) => {
    const execute = () =>
      action.run(async () => {
        try {
          const res = await api.batchJobAction(act, ids);
          if (res.failed > 0) {
            Toast.warning(
              `${label || "批量操作"}完成：成功 ${res.succeeded} 个，失败 ${res.failed} 个`,
            );
          } else {
            Toast.success(
              `${label || "批量操作"}完成（共 ${res.succeeded} 个任务）`,
            );
          }
          setSelectedIds([]);
          await refreshAll(true);
        } catch (err) {
          errorToast(err);
        }
      });

    if (act === "stop" || act === "stop_all" || act === "disable") {
      confirmAction({
        title: `确认执行「${label}」？`,
        content:
          act === "disable"
            ? "停用任务将同时中止其中正在运行的同步任务。"
            : "将中止目标任务的当前执行，已完成的文件不会撤销。",
        okText: "确认",
        onOk: execute,
      });
      return;
    }
    void execute();
  };

  const handleClone = (jobId: number) => {
    const full = fullJobMap.get(jobId);
    if (!full) {
      Toast.warning("正在加载任务配置，请稍后重试");
      return;
    }
    setEditorState({
      open: true,
      job: null,
      initialDraft: cloneJobToForm(full),
    });
  };

  const handleRunOne = (job: DashboardJobItem) => {
    const startNow = () =>
      action.run(async () => {
        try {
          await api.jobAction({ id: String(job.id) });
          Toast.success("已提交执行");
          await refreshAll(true);
        } catch (err) {
          errorToast(err);
        }
      });
    if (job.method === 2) {
      confirmAction({
        title: "确认启动移动同步？",
        content:
          "移动模式会在文件复制到目标目录并校验后删除源目录中的对应文件，是否确认立即执行？",
        okText: "确认移动",
        onOk: startNow,
      });
      return;
    }
    void startNow();
  };

  const handleStopOne = (job: DashboardJobItem) => {
    confirmAction({
      title: "停止当前任务？",
      content: "已完成的文件不会撤销。",
      okText: "停止",
      onOk: () =>
        action.run(async () => {
          try {
            if (job.currentTaskId) {
              await api.taskAction(job.currentTaskId, "stop");
            } else {
              await api.jobAction({
                id: String(job.id),
                pause: true,
                abort: true,
              });
            }
            Toast.success("已提交停止");
            await refreshAll(true);
          } catch (err) {
            errorToast(err);
          }
        }),
    });
  };

  return (
    <div className="page dashboard-page">
      <Header
        title="总览看板"
        actions={
          <div className="dashboard-header-actions">
            <Button
              type="tertiary"
              icon={<IconRefresh aria-hidden="true" />}
              loading={dashboard.loading}
              onClick={() => void refreshAll(false)}
            >
              刷新
            </Button>
            <Button
              type="tertiary"
              icon={<IconPlay aria-hidden="true" />}
              disabled={action.busy || !allJobs.length}
              onClick={() => runBatch("run_all", undefined, "运行全部已启用任务")}
            >
              全部运行
            </Button>
            <Button
              type="danger"
              theme="light"
              icon={<IconPause aria-hidden="true" />}
              disabled={
                action.busy ||
                !((data?.kpi.runningJobs || 0) + (data?.kpi.queuedJobs || 0))
              }
              onClick={() => runBatch("stop_all", undefined, "停止全部运行中任务")}
            >
              全部停止
            </Button>
            <Button
              theme="solid"
              icon={<IconPlusStroked aria-hidden="true" />}
              onClick={() =>
                setEditorState({ open: true, job: null, initialDraft: null })
              }
            >
              新建任务
            </Button>
          </div>
        }
      />

      <LoadState
        loading={dashboard.loading && !data}
        error={dashboard.error}
        retry={dashboard.refresh}
      >
        {data && (
          <div className="dashboard-content">
            {data.pendingApprovals.length > 0 && (
              <div className="approval-alert-banner" role="alert">
                <div className="approval-alert-head">
                  <IconAlertTriangle aria-hidden="true" />
                  <strong>
                    有 {data.pendingApprovals.length} 个全量同步任务正在等待删除审批
                  </strong>
                </div>
                <div className="approval-alert-list">
                  {data.pendingApprovals.map((item) => (
                    <div
                      key={`${item.jobId}-${item.taskId}`}
                      className="approval-alert-row"
                    >
                      <span>
                        <strong>{item.jobName}</strong>（任务 #{item.taskId}）：预计删除{" "}
                        {item.deleteCount} 项
                        {item.deleteSize > 0
                          ? `（${formatSize(item.deleteSize)}）`
                          : ""}
                      </span>
                      <Button
                        size="small"
                        type="danger"
                        theme="solid"
                        onClick={() =>
                          navigate(`/tasks?jobId=${item.jobId}&tab=overview`)
                        }
                      >
                        立即前往审批
                      </Button>
                    </div>
                  ))}
                </div>
              </div>
            )}

            <div className="dashboard-kpi-grid">
              <div className="dashboard-kpi-card">
                <span className="dashboard-kpi-label">同步任务</span>
                <div className="dashboard-kpi-value">{data.kpi.totalJobs}</div>
                <span className="muted">
                  已启用 {data.kpi.enabledJobs} · 已停用{" "}
                  {Math.max(0, data.kpi.totalJobs - data.kpi.enabledJobs)}
                </span>
              </div>
              <div className="dashboard-kpi-card">
                <span className="dashboard-kpi-label">实时执行</span>
                <div className="dashboard-kpi-value">
                  {data.kpi.runningJobs}
                  {data.kpi.queuedJobs > 0 && (
                    <span className="dashboard-kpi-sub">
                      {" "}
                      / 排队 {data.kpi.queuedJobs}
                    </span>
                  )}
                </div>
                <span className="muted">
                  {data.kpi.pendingApprovals > 0
                    ? `${data.kpi.pendingApprovals} 个待审批`
                    : "无待审批项"}
                </span>
              </div>
              <div className="dashboard-kpi-card">
                <span className="dashboard-kpi-label">存储引擎健康</span>
                <div className="dashboard-kpi-value">
                  {data.kpi.onlineEngines} / {data.kpi.totalEngines}
                </div>
                <span
                  className={
                    data.kpi.offlineEngines > 0 ? "inline-error" : "muted"
                  }
                >
                  {data.kpi.offlineEngines > 0
                    ? `${data.kpi.offlineEngines} 个引擎连接异常`
                    : "引擎状态正常"}
                </span>
              </div>
              <div className="dashboard-kpi-card">
                <span className="dashboard-kpi-label">近 7 天同步</span>
                <div className="dashboard-kpi-value">
                  {formatSize(data.recent7d.totalBytes)}
                </div>
                <span className="muted">
                  执行 {data.recent7d.totalRuns} 次（成功 {data.recent7d.successRuns} ·
                  异常 {data.recent7d.failRuns}）· {data.recent7d.totalFiles} 文件
                </span>
              </div>
            </div>

            <SectionCard
              className="dashboard-jobs-card"
              title="全部同步任务"
              extra={
                <span className="muted">
                  显示 {filteredJobs.length} / {allJobs.length} 项
                </span>
              }
            >
              <div className="dashboard-filters">
                <Checkbox
                  aria-label="全选当前列表任务"
                  checked={visibleAllSelected}
                  indeterminate={visibleSomeSelected}
                  disabled={!filteredJobs.length}
                  onChange={(e) =>
                    toggleSelectAllVisible(Boolean(e.target.checked))
                  }
                >
                  全选
                </Checkbox>
                <SearchFilterInput
                  ariaLabel="搜索任务名称、路径或引擎"
                  placeholder="搜索任务名 / 路径 / 引擎"
                  value={searchInput}
                  onChange={setSearchInput}
                  onSearch={setKeyword}
                />
                <Select
                  aria-label="按存储引擎筛选"
                  value={engineFilter}
                  onChange={(v) => setEngineFilter(String(v))}
                  optionList={[
                    { label: "全部引擎", value: "all" },
                    ...(engines.data || []).map((e) => ({
                      label: e.remark || e.userName || `引擎 #${e.id}`,
                      value: String(e.id),
                    })),
                  ]}
                />
                <Select
                  aria-label="按同步方式筛选"
                  value={methodFilter}
                  onChange={(v) => setMethodFilter(String(v))}
                  optionList={[
                    { label: "全部方式", value: "all" },
                    { label: "增量同步", value: "0" },
                    { label: "全量同步", value: "1" },
                    { label: "移动", value: "2" },
                  ]}
                />
                <Select
                  aria-label="按运行状态筛选"
                  value={statusFilter}
                  onChange={(v) => setStatusFilter(String(v) as StatusFilter)}
                  optionList={[
                    { label: "全部状态", value: "all" },
                    { label: "运行中", value: "running" },
                    { label: "排队中", value: "queued" },
                    { label: "待审批", value: "approval" },
                    { label: "最近失败", value: "failed" },
                    { label: "已停用", value: "disabled" },
                  ]}
                />
              </div>

              {selectedIds.length > 0 && (
                <div className="batch-action-bar">
                  <span>已选中 {selectedIds.length} 个任务</span>
                  <div className="batch-action-buttons">
                    <Button
                      size="small"
                      theme="solid"
                      disabled={action.busy}
                      onClick={() => runBatch("run", selectedIds, "批量运行")}
                    >
                      批量运行
                    </Button>
                    <Button
                      size="small"
                      type="danger"
                      theme="light"
                      disabled={action.busy}
                      onClick={() => runBatch("stop", selectedIds, "批量停止")}
                    >
                      批量停止
                    </Button>
                    <Button
                      size="small"
                      type="tertiary"
                      disabled={action.busy}
                      onClick={() => runBatch("enable", selectedIds, "批量启用")}
                    >
                      批量启用
                    </Button>
                    <Button
                      size="small"
                      type="tertiary"
                      disabled={action.busy}
                      onClick={() => runBatch("disable", selectedIds, "批量停用")}
                    >
                      批量停用
                    </Button>
                    <Button
                      size="small"
                      theme="borderless"
                      onClick={() => setSelectedIds([])}
                    >
                      取消选择
                    </Button>
                  </div>
                </div>
              )}

              {!filteredJobs.length ? (
                <EmptyState />
              ) : (
                <div className="dashboard-job-list">
                  {filteredJobs.map((job) => {
                    const jobName = job.remark || `同步任务 #${job.id}`;
                    const srcList = parseJobPathList(job.srcPath);
                    const dstList = parseJobPathList(job.dstPath);
                    return (
                      <div key={job.id} className="dashboard-job-row">
                        <div className="dashboard-job-check">
                          <Checkbox
                            aria-label={`选择 ${jobName}`}
                            checked={selectedSet.has(job.id)}
                            onChange={(e) =>
                              toggleSelect(job.id, Boolean(e.target.checked))
                            }
                          />
                        </div>
                        <div
                          className="dashboard-job-main"
                          onClick={() =>
                            navigate(
                              `/tasks?jobId=${job.id}&tab=${job.running ? "realtime" : "overview"}`,
                            )
                          }
                        >
                          <div className="dashboard-job-title-line">
                            <strong>{jobName}</strong>
                            <Tag size="small" type="light" color="blue">
                              {methodLabel(job.method)}
                            </Tag>
                            {job.pendingApproval && (
                              <Tag size="small" type="solid" color="orange">
                                待审批删除
                              </Tag>
                            )}
                            {job.running && !job.pendingApproval && (
                              <Tag size="small" type="light" color="blue">
                                运行中 {Math.round(job.liveProgress || 0)}%
                              </Tag>
                            )}
                            {job.queued && (
                              <Tag size="small" type="light" color="cyan">
                                排队等待中
                              </Tag>
                            )}
                            {job.enable !== 1 && job.isCron !== 2 && (
                              <Tag size="small" type="light" color="grey">
                                已停用
                              </Tag>
                            )}
                            {job.windowEnable === 1 && (
                              <Tag size="small" type="light" color="violet">
                                窗口 {job.windowStart}–{job.windowEnd}
                              </Tag>
                            )}
                          </div>
                          <div className="dashboard-job-meta">
                            <span>{job.alistName || `引擎 #${job.alistId}`}</span>
                            <span>
                              {srcList[0] || "—"}
                              {srcList.length > 1 ? ` 等 ${srcList.length} 项` : ""} →{" "}
                              {dstList[0] || "—"}
                            </span>
                            {job.nextRunTime > 0 && (
                              <span>
                                下次 {formatTimestamp(job.nextRunTime, true)}
                              </span>
                            )}
                          </div>
                          {job.running && (
                            <div className="dashboard-job-progress">
                              <Progress
                                percent={Math.round(job.liveProgress || 0)}
                                showInfo={false}
                                stroke="var(--semi-color-primary)"
                              />
                            </div>
                          )}
                          {!job.running && job.lastTask && (
                            <div className="dashboard-job-last">
                              <Status
                                status={job.lastTask.status}
                                label={
                                  taskRecordStatusNames[job.lastTask.status] ||
                                  String(job.lastTask.status)
                                }
                                error={job.lastTask.errMsg}
                              />
                              <span className="muted">
                                {formatTimestamp(job.lastTask.createTime, true)}
                                {job.lastTask.allNum !== undefined
                                  ? ` · ${job.lastTask.successNum ?? 0}/${job.lastTask.allNum} 项`
                                  : ""}
                                {(job.lastTask.sumSize ?? 0) > 0
                                  ? ` · ${formatSize(job.lastTask.sumSize || 0)}`
                                  : ""}
                              </span>
                            </div>
                          )}
                        </div>
                        <div className="row-actions">
                          <IconButton
                            label="模拟运行预览"
                            icon={<IconEyeOpenedStroked aria-hidden="true" />}
                            disabled={action.busy}
                            onClick={() =>
                              setDryRunJob({ id: job.id, name: jobName })
                            }
                          />
                          <IconButton
                            label={job.running || job.queued ? "停止任务" : "启动任务"}
                            icon={
                              job.running || job.queued ? (
                                <IconPause aria-hidden="true" />
                              ) : (
                                <IconPlay aria-hidden="true" />
                              )
                            }
                            danger={job.running || job.queued}
                            disabled={
                              action.busy ||
                              (!job.running && !job.queued && job.enable !== 1)
                            }
                            onClick={() =>
                              job.running || job.queued
                                ? handleStopOne(job)
                                : handleRunOne(job)
                            }
                          />
                          <IconButton
                            label="复制任务"
                            icon={<IconCopy aria-hidden="true" />}
                            disabled={action.busy}
                            onClick={() => handleClone(job.id)}
                          />
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </SectionCard>
          </div>
        )}
      </LoadState>

      {dryRunJob && (
        <DryRunModal
          jobId={dryRunJob.id}
          jobName={dryRunJob.name}
          onClose={() => setDryRunJob(null)}
          onRunReal={() => {
            const target = allJobs.find((j) => j.id === dryRunJob.id);
            if (target) handleRunOne(target);
          }}
        />
      )}

      {editorState.open && (
        <Suspense fallback={<LoadingPanel />}>
          <JobEditor
            job={editorState.job}
            initialDraft={editorState.initialDraft || undefined}
            engines={engines.data || []}
            existingJobs={jobsList.data?.dataList || []}
            enginesError={engines.error}
            onRetryEngines={() => void engines.refresh()}
            onNavigateEngines={() => {
              setEditorState({ open: false, job: null, initialDraft: null });
              navigate("/engines");
            }}
            onClose={() =>
              setEditorState({ open: false, job: null, initialDraft: null })
            }
            onSaved={(result: TaskEditorSavedResult) => {
              setEditorState({ open: false, job: null, initialDraft: null });
              void refreshAll(true).then(() => {
                if (result.createdId) {
                  navigate(`/tasks?jobId=${result.createdId}&tab=overview`);
                }
              });
            }}
          />
        </Suspense>
      )}
    </div>
  );
}
