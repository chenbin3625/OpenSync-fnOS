import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconActivity,
  IconAlertTriangle,
  IconArrowRight,
  IconBarChartVStroked,
  IconChevronRight,
  IconClockStroked,
  IconCloudStroked,
  IconCopy,
  IconEyeOpenedStroked,
  IconPause,
  IconPlay,
  IconPlusStroked,
  IconRefresh,
  IconServerStroked,
} from "@douyinfe/semi-icons";
import dayjs from "dayjs";
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
import { FullSyncApprovalModal } from "../components/FullSyncApprovalModal";
import { useAction, useResource } from "../lib/hooks";
import { cloneJobToForm, findBatchMoveJobs } from "../lib/taskForm";
import {
  formatSize,
  methodLabel,
  parseJobPathList,
  taskRecordStatusNames,
} from "./Home/homeUtils";
import { formatTimestamp } from "../utils/date";
import type { DashboardJobItem, FullSyncApproval, JobItem } from "../types";
import type { TaskEditorSavedResult } from "./TaskEditor";

const JobEditor = lazy(() => import("./TaskEditor"));

type StatusFilter =
  | "all"
  | "running"
  | "queued"
  | "approval"
  | "failed"
  | "disabled";

const FAILED_STATUSES = [3, 5, 6, 7];

function isJobFailed(job: DashboardJobItem): boolean {
  return Boolean(job.lastTask && FAILED_STATUSES.includes(job.lastTask.status));
}

function methodTagColor(method: number): "blue" | "violet" | "cyan" | "grey" {
  if (method === 0) return "blue";
  if (method === 1) return "violet";
  if (method === 2) return "cyan";
  return "grey";
}

function jobDotTone(job: DashboardJobItem): string {
  if (job.pendingApproval) return "kpi-dot kpi-dot--warning";
  if (job.running) return "kpi-dot kpi-dot--primary kpi-dot--pulse";
  if (job.queued) return "kpi-dot kpi-dot--primary";
  if (isJobFailed(job)) return "kpi-dot kpi-dot--danger";
  if (job.enable !== 1) return "kpi-dot";
  if (job.lastTask && (job.lastTask.status === 2 || job.lastTask.status === 8))
    return "kpi-dot kpi-dot--success";
  return "kpi-dot";
}

function formatShortTime(seconds?: number | null): string {
  const ts = Number(seconds || 0);
  if (ts <= 0) return "—";
  const d = dayjs.unix(ts);
  const sameYear = d.year() === dayjs().year();
  return d.format(sameYear ? "MM-DD HH:mm" : "YYYY-MM-DD HH:mm");
}

function formatScheduleHint(job: DashboardJobItem): string {
  if (job.nextRunTime > 0) return `下次 ${formatShortTime(job.nextRunTime)}`;
  if (job.enable !== 1) return "已停用";
  if (job.isCron === 2) return "仅手动触发";
  if (job.isCron === 0 && job.interval > 0) return `每 ${job.interval} 分钟`;
  return "定时计划";
}

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
  const [approvalModal, setApprovalModal] = useState<{
    jobId: number;
    jobName: string;
    approval: FullSyncApproval;
    loading?: boolean;
  } | null>(null);
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

  const statusCounts = useMemo(() => {
    let running = 0;
    let queued = 0;
    let approval = 0;
    let failed = 0;
    let disabled = 0;
    for (const job of allJobs) {
      if (job.running) running += 1;
      if (job.queued) queued += 1;
      if (job.pendingApproval) approval += 1;
      if (isJobFailed(job)) failed += 1;
      if (job.enable !== 1) disabled += 1;
    }
    return {
      all: allJobs.length,
      running,
      queued,
      approval,
      failed,
      disabled,
    };
  }, [allJobs]);

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
      if (statusFilter === "failed" && !isJobFailed(job)) return false;
      if (statusFilter === "disabled" && job.enable === 1) return false;
      if (kw) {
        const hay = `${job.remark || ""} #${job.id} ${job.alistName} ${job.srcPath} ${job.dstPath}`.toLowerCase();
        if (!hay.includes(kw)) return false;
      }
      return true;
    });
  }, [allJobs, engineFilter, methodFilter, statusFilter, keyword]);

  const hasActiveFilters =
    Boolean(keyword.trim()) ||
    engineFilter !== "all" ||
    methodFilter !== "all" ||
    statusFilter !== "all";

  const resetFilters = () => {
    setSearchInput("");
    setKeyword("");
    setEngineFilter("all");
    setMethodFilter("all");
    setStatusFilter("all");
  };

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

  const openJobDetail = (job: DashboardJobItem) => {
    const tab = job.running ? "realtime" : "overview";
    navigate(`/tasks?jobId=${job.id}&tab=${tab}`);
  };

  const openApprovalModal = (
    jobId: number,
    jobName: string,
    seed: FullSyncApproval,
  ) => {
    const needsFetch = !seed.paths || seed.paths.length === 0;
    setApprovalModal({
      jobId,
      jobName,
      approval: seed,
      loading: needsFetch,
    });
    if (needsFetch) {
      void api
        .pendingApproval(jobId)
        .then((fresh) => {
          setApprovalModal((prev) => {
            if (!prev || prev.jobId !== jobId) return prev;
            if (!fresh) return null;
            return { ...prev, approval: fresh, loading: false };
          });
        })
        .catch(() => {
          setApprovalModal((prev) =>
            prev && prev.jobId === jobId ? { ...prev, loading: false } : prev,
          );
        });
    }
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
    const moveJobs = findBatchMoveJobs(act, allJobs, ids);
    if (moveJobs.length > 0) {
      const names = moveJobs
        .slice(0, 3)
        .map((j) => (j.remark || "").trim() || `任务 #${j.id}`)
        .join("、");
      const suffix = moveJobs.length > 3 ? ` 等 ${moveJobs.length} 个` : "";
      confirmAction({
        title: `确认执行「${label || "批量运行"}」？`,
        content: `本次将启动移动模式任务（${names}${suffix}）。移动模式会在文件复制到目标目录并校验后删除源目录中的对应文件，是否确认立即执行？`,
        okText: "确认移动并启动",
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

  const statusPills: Array<{
    key: StatusFilter;
    label: string;
    count: number;
    tone?: "warning" | "danger";
    alwaysShow?: boolean;
  }> = [
    { key: "all", label: "全部", count: statusCounts.all, alwaysShow: true },
    {
      key: "running",
      label: "运行中",
      count: statusCounts.running,
      alwaysShow: true,
    },
    {
      key: "queued",
      label: "排队中",
      count: statusCounts.queued,
    },
    {
      key: "approval",
      label: "待审批",
      count: statusCounts.approval,
      tone: statusCounts.approval > 0 ? "warning" : undefined,
    },
    {
      key: "failed",
      label: "最近失败",
      count: statusCounts.failed,
      tone: statusCounts.failed > 0 ? "danger" : undefined,
      alwaysShow: true,
    },
    {
      key: "disabled",
      label: "已停用",
      count: statusCounts.disabled,
      alwaysShow: true,
    },
  ];

  const kpi = data?.kpi;
  const recent = data?.recent7d;
  const disabledJobsCount = kpi
    ? Math.max(0, kpi.totalJobs - kpi.enabledJobs)
    : 0;
  const enabledPct =
    kpi && kpi.totalJobs > 0
      ? Math.round((kpi.enabledJobs / kpi.totalJobs) * 100)
      : 0;
  const activeRunningCount = kpi ? kpi.runningJobs : 0;
  const liveRunPct =
    kpi && kpi.totalJobs > 0
      ? Math.min(100, (activeRunningCount / kpi.totalJobs) * 100)
      : 0;
  const liveApprovalPct =
    kpi && kpi.totalJobs > 0
      ? Math.min(100 - liveRunPct, (kpi.pendingApprovals / kpi.totalJobs) * 100)
      : 0;
  const liveQueuedPct =
    kpi && kpi.totalJobs > 0
      ? Math.min(
          100 - liveRunPct - liveApprovalPct,
          (kpi.queuedJobs / kpi.totalJobs) * 100,
        )
      : 0;
  const onlinePct =
    kpi && kpi.totalEngines > 0
      ? (kpi.onlineEngines / kpi.totalEngines) * 100
      : 0;
  const offlinePct =
    kpi && kpi.totalEngines > 0
      ? (kpi.offlineEngines / kpi.totalEngines) * 100
      : 0;
  const unknownEnginesCount = kpi
    ? Math.max(0, kpi.totalEngines - kpi.onlineEngines - kpi.offlineEngines)
    : 0;
  const recentSuccessPct =
    recent && recent.totalRuns > 0
      ? (recent.successRuns / recent.totalRuns) * 100
      : 0;
  const recentFailPct =
    recent && recent.totalRuns > 0
      ? (recent.failRuns / recent.totalRuns) * 100
      : 0;
  const recentStoppedPct =
    recent && recent.totalRuns > 0
      ? (recent.stoppedRuns / recent.totalRuns) * 100
      : 0;

  const liveCardTargetFilter: StatusFilter = kpi?.pendingApprovals
    ? "approval"
    : kpi?.runningJobs
      ? "running"
      : kpi?.queuedJobs
        ? "queued"
        : "running";

  const emptyJobAction =
    !engines.loading && !engines.error && (engines.data?.length ?? 0) === 0 ? (
      <div className="empty-guide">
        <span className="muted">
          还没有配置存储引擎，先添加引擎即可创建同步任务
        </span>
        <Button
          type="primary"
          theme="solid"
          onClick={() => navigate("/engines")}
        >
          前往添加存储引擎
        </Button>
      </div>
    ) : !engines.loading ? (
      <Button
        type="primary"
        theme="solid"
        icon={<IconPlusStroked aria-hidden="true" />}
        onClick={() =>
          setEditorState({ open: true, job: null, initialDraft: null })
        }
      >
        新建同步任务
      </Button>
    ) : undefined;

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
                !((kpi?.runningJobs || 0) + (kpi?.queuedJobs || 0))
              }
              onClick={() => runBatch("stop_all", undefined, "停止全部运行中任务")}
            >
              全部停止
            </Button>
            <span className="dashboard-header-divider" aria-hidden="true" />
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
        {data && kpi && recent && (
          <div className="dashboard-content">
            {data.pendingApprovals.length > 0 && (
              <div className="approval-alert-banner" role="alert">
                <div className="approval-alert-head">
                  <span className="approval-alert-icon" aria-hidden="true">
                    <IconAlertTriangle />
                  </span>
                  <div className="approval-alert-copy">
                    <strong>
                      有 {data.pendingApprovals.length} 个全量同步任务存在待确认删除项
                    </strong>
                    <span className="approval-alert-sub">
                      文件同步已完成且不受阻塞，目标端多余文件将保留至您确认后再执行清理。
                    </span>
                  </div>
                </div>
                <div className="approval-alert-list">
                  {data.pendingApprovals.map((item) => {
                    const matchedJob = allJobs.find((j) => j.id === item.jobId);
                    const executing = item.status === "executing";
                    const seedApproval: FullSyncApproval = matchedJob?.pendingApproval
                      ? {
                          ...matchedJob.pendingApproval,
                          jobId: item.jobId,
                          jobName: item.jobName,
                          taskId: matchedJob.pendingApproval.taskId || item.taskId,
                        }
                      : {
                          jobId: item.jobId,
                          jobName: item.jobName,
                          taskId: item.taskId,
                          deleteCount: item.deleteCount,
                          deleteSize: item.deleteSize,
                          pathCount: item.pathCount,
                          paths: [],
                          status: item.status,
                          createdAt: item.createdAt,
                          expiresAt: item.expiresAt,
                          planChanged: item.planChanged,
                          lastError: item.lastError,
                        };
                    return (
                      <div
                        key={`${item.jobId}-${item.taskId}`}
                        className="approval-alert-row"
                      >
                        <div className="approval-alert-item-main">
                          <strong>{item.jobName}</strong>
                          <Tag size="small" type="light" color="grey">
                            任务 #{item.taskId}
                          </Tag>
                          <span className="approval-alert-impact">
                            {executing ? (
                              "正在后台校验并执行删除…"
                            ) : (
                              <>
                                待确认删除 <strong>{item.deleteCount} 项</strong>
                                {item.deleteSize > 0
                                  ? ` · ${formatSize(item.deleteSize)}`
                                  : ""}
                              </>
                            )}
                          </span>
                        </div>
                        <Button
                          size="small"
                          type="danger"
                          theme="solid"
                          disabled={executing}
                          onClick={() =>
                            openApprovalModal(
                              item.jobId,
                              item.jobName,
                              seedApproval,
                            )
                          }
                        >
                          {executing ? "正在删除…" : "查看并审批"}
                        </Button>
                      </div>
                    );
                  })}
                </div>
              </div>
            )}

            <div className="dashboard-kpi-grid">
              <button
                type="button"
                className="dashboard-kpi-card"
                onClick={() => setStatusFilter("all")}
              >
                <div className="dashboard-kpi-head">
                  <span className="dashboard-kpi-label">同步任务</span>
                  <span
                    className="dashboard-kpi-icon dashboard-kpi-icon--primary"
                    aria-hidden="true"
                  >
                    <IconCloudStroked />
                  </span>
                </div>
                <div className="dashboard-kpi-value-row">
                  <span className="dashboard-kpi-value">{kpi.totalJobs}</span>
                </div>
                {kpi.totalJobs > 0 && (
                  <div className="dashboard-kpi-bar" aria-hidden="true">
                    <span
                      className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--primary"
                      style={{ width: `${enabledPct}%` }}
                    />
                  </div>
                )}
                <div className="dashboard-kpi-foot">
                  <span className="dashboard-kpi-stat">
                    <span className="kpi-dot kpi-dot--primary" aria-hidden="true" />
                    已启用 {kpi.enabledJobs}
                  </span>
                  <span className="dashboard-kpi-stat">
                    <span className="kpi-dot" aria-hidden="true" />
                    已停用 {disabledJobsCount}
                  </span>
                </div>
              </button>

              <button
                type="button"
                className={[
                  "dashboard-kpi-card",
                  kpi.pendingApprovals > 0 ? "is-warning" : "",
                  statusFilter === liveCardTargetFilter ? "is-active" : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                onClick={() =>
                  setStatusFilter((prev) =>
                    prev === liveCardTargetFilter ? "all" : liveCardTargetFilter,
                  )
                }
              >
                <div className="dashboard-kpi-head">
                  <span className="dashboard-kpi-label">实时执行</span>
                  <span
                    className={`dashboard-kpi-icon ${
                      kpi.pendingApprovals > 0
                        ? "dashboard-kpi-icon--warning"
                        : kpi.runningJobs > 0
                          ? "dashboard-kpi-icon--primary"
                          : ""
                    }`}
                    aria-hidden="true"
                  >
                    <IconActivity />
                  </span>
                </div>
                <div className="dashboard-kpi-value-row">
                  <span className="dashboard-kpi-value">{kpi.runningJobs}</span>
                  {kpi.queuedJobs > 0 && (
                    <span className="dashboard-kpi-pill">
                      排队 {kpi.queuedJobs}
                    </span>
                  )}
                </div>
                {kpi.totalJobs > 0 && (
                  <div className="dashboard-kpi-bar" aria-hidden="true">
                    {liveRunPct > 0 && (
                      <span
                        className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--primary"
                        style={{ width: `${liveRunPct}%` }}
                      />
                    )}
                    {liveApprovalPct > 0 && (
                      <span
                        className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--warning"
                        style={{ width: `${liveApprovalPct}%` }}
                      />
                    )}
                    {liveQueuedPct > 0 && (
                      <span
                        className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--muted"
                        style={{ width: `${liveQueuedPct}%` }}
                      />
                    )}
                  </div>
                )}
                <div className="dashboard-kpi-foot">
                  {kpi.pendingApprovals > 0 ? (
                    <span className="dashboard-kpi-stat is-warning">
                      <span
                        className="kpi-dot kpi-dot--warning"
                        aria-hidden="true"
                      />
                      {kpi.pendingApprovals} 个待审批
                    </span>
                  ) : kpi.runningJobs > 0 ? (
                    <span className="dashboard-kpi-stat is-primary">
                      <span
                        className="kpi-dot kpi-dot--primary kpi-dot--pulse"
                        aria-hidden="true"
                      />
                      正在同步 · 无待审批
                    </span>
                  ) : kpi.queuedJobs > 0 ? (
                    <span className="dashboard-kpi-stat is-primary">
                      <span
                        className="kpi-dot kpi-dot--primary"
                        aria-hidden="true"
                      />
                      {kpi.queuedJobs} 个排队等待中
                    </span>
                  ) : (
                    <span className="dashboard-kpi-stat">
                      <span className="kpi-dot" aria-hidden="true" />
                      当前空闲 · 无待审批
                    </span>
                  )}
                </div>
              </button>

              <button
                type="button"
                className={[
                  "dashboard-kpi-card",
                  kpi.offlineEngines > 0 ? "is-danger" : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                onClick={() => navigate("/engines")}
              >
                <div className="dashboard-kpi-head">
                  <span className="dashboard-kpi-label">
                    存储引擎健康
                    <IconChevronRight
                      size="small"
                      className="dashboard-kpi-link-arrow"
                      aria-hidden="true"
                    />
                  </span>
                  <span
                    className={`dashboard-kpi-icon ${
                      kpi.offlineEngines > 0
                        ? "dashboard-kpi-icon--danger"
                        : kpi.totalEngines > 0 &&
                            kpi.onlineEngines === kpi.totalEngines
                          ? "dashboard-kpi-icon--success"
                          : ""
                    }`}
                    aria-hidden="true"
                  >
                    <IconServerStroked />
                  </span>
                </div>
                <div className="dashboard-kpi-value-row">
                  <span className="dashboard-kpi-value">
                    {kpi.onlineEngines}
                  </span>
                  <span className="dashboard-kpi-denom">
                    / {kpi.totalEngines}
                  </span>
                </div>
                {kpi.totalEngines > 0 && (
                  <div className="dashboard-kpi-bar" aria-hidden="true">
                    {onlinePct > 0 && (
                      <span
                        className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--success"
                        style={{ width: `${onlinePct}%` }}
                      />
                    )}
                    {offlinePct > 0 && (
                      <span
                        className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--danger"
                        style={{ width: `${offlinePct}%` }}
                      />
                    )}
                  </div>
                )}
                <div className="dashboard-kpi-foot">
                  {kpi.totalEngines === 0 ? (
                    <span className="dashboard-kpi-stat">
                      <span className="kpi-dot" aria-hidden="true" />
                      尚未配置存储引擎
                    </span>
                  ) : kpi.offlineEngines > 0 ? (
                    <span className="dashboard-kpi-stat is-danger">
                      <span
                        className="kpi-dot kpi-dot--danger"
                        aria-hidden="true"
                      />
                      {kpi.offlineEngines} 个引擎连接异常
                    </span>
                  ) : unknownEnginesCount > 0 ? (
                    <span className="dashboard-kpi-stat">
                      <span className="kpi-dot" aria-hidden="true" />
                      {unknownEnginesCount} 个引擎待检测
                    </span>
                  ) : (
                    <span className="dashboard-kpi-stat is-success">
                      <span
                        className="kpi-dot kpi-dot--success"
                        aria-hidden="true"
                      />
                      全部引擎连接正常
                    </span>
                  )}
                </div>
              </button>

              {(() => {
                const recentCardBody = (
                  <>
                    <div className="dashboard-kpi-head">
                      <span className="dashboard-kpi-label">近 7 天同步</span>
                      <span
                        className="dashboard-kpi-icon dashboard-kpi-icon--primary"
                        aria-hidden="true"
                      >
                        <IconBarChartVStroked />
                      </span>
                    </div>
                    <div className="dashboard-kpi-value-row">
                      <span className="dashboard-kpi-value">
                        {formatSize(recent.totalBytes)}
                      </span>
                    </div>
                    {recent.totalRuns > 0 && (
                      <div className="dashboard-kpi-bar" aria-hidden="true">
                        {recentSuccessPct > 0 && (
                          <span
                            className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--success"
                            style={{ width: `${recentSuccessPct}%` }}
                          />
                        )}
                        {recentFailPct > 0 && (
                          <span
                            className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--danger"
                            style={{ width: `${recentFailPct}%` }}
                          />
                        )}
                        {recentStoppedPct > 0 && (
                          <span
                            className="dashboard-kpi-bar-seg dashboard-kpi-bar-seg--muted"
                            style={{ width: `${recentStoppedPct}%` }}
                          />
                        )}
                      </div>
                    )}
                    <div className="dashboard-kpi-foot">
                      <span>执行 {recent.totalRuns} 次</span>
                      <span
                        className={
                          recent.failRuns > 0
                            ? "dashboard-kpi-stat is-success"
                            : undefined
                        }
                      >
                        成功 {recent.successRuns}
                      </span>
                      {recent.failRuns > 0 && (
                        <span className="dashboard-kpi-stat is-danger">
                          异常 {recent.failRuns}
                        </span>
                      )}
                      <span>{recent.totalFiles} 文件</span>
                    </div>
                  </>
                );
                return recent.failRuns > 0 ? (
                  <button
                    type="button"
                    className={[
                      "dashboard-kpi-card",
                      statusFilter === "failed" ? "is-active" : "",
                    ]
                      .filter(Boolean)
                      .join(" ")}
                    onClick={() =>
                      setStatusFilter((prev) =>
                        prev === "failed" ? "all" : "failed",
                      )
                    }
                  >
                    {recentCardBody}
                  </button>
                ) : (
                  <div className="dashboard-kpi-card">{recentCardBody}</div>
                );
              })()}
            </div>

            <SectionCard
              className="dashboard-jobs-card"
              title={
                <div className="dashboard-jobs-title">
                  <h2>全部同步任务</h2>
                  <span className="dashboard-count-pill">
                    {hasActiveFilters
                      ? `${filteredJobs.length} / ${allJobs.length}`
                      : `共 ${allJobs.length} 项`}
                  </span>
                </div>
              }
              extra={
                <div
                  className="dashboard-status-pills"
                  role="group"
                  aria-label="按运行状态筛选"
                >
                  {statusPills
                    .filter(
                      (pill) =>
                        pill.alwaysShow ||
                        pill.count > 0 ||
                        statusFilter === pill.key,
                    )
                    .map((pill) => (
                      <button
                        key={pill.key}
                        type="button"
                        aria-pressed={statusFilter === pill.key}
                        className={[
                          "dashboard-status-pill",
                          statusFilter === pill.key ? "is-active" : "",
                          pill.tone ? `is-${pill.tone}` : "",
                        ]
                          .filter(Boolean)
                          .join(" ")}
                        onClick={() => setStatusFilter(pill.key)}
                      >
                        <span>{pill.label}</span>
                        <span className="dashboard-status-pill-count">
                          {pill.count}
                        </span>
                      </button>
                    ))}
                </div>
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
                <span
                  className="dashboard-filters-divider"
                  aria-hidden="true"
                />
                <SearchFilterInput
                  ariaLabel="搜索任务名称、路径或引擎"
                  placeholder="搜索任务名 / 路径 / 引擎"
                  value={searchInput}
                  onChange={(next) => {
                    setSearchInput(next);
                    setKeyword(next);
                  }}
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
                {hasActiveFilters && (
                  <Button
                    size="small"
                    theme="borderless"
                    type="tertiary"
                    onClick={resetFilters}
                  >
                    重置筛选
                  </Button>
                )}
              </div>

              {selectedIds.length > 0 && (
                <div className="batch-action-bar">
                  <span className="batch-action-summary">
                    已选中 <strong>{selectedIds.length}</strong> 个任务
                  </span>
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

              {!allJobs.length ? (
                <EmptyState action={emptyJobAction} />
              ) : !filteredJobs.length ? (
                <div className="dashboard-filter-empty" role="status">
                  <span className="muted">没有符合当前筛选条件的同步任务</span>
                  <Button size="small" type="tertiary" onClick={resetFilters}>
                    重置筛选条件
                  </Button>
                </div>
              ) : (
                <div className="dashboard-job-list">
                  {filteredJobs.map((job) => {
                    const jobName = job.remark || `同步任务 #${job.id}`;
                    const srcList = parseJobPathList(job.srcPath);
                    const dstList = parseJobPathList(job.dstPath);
                    const srcSummary =
                      (srcList[0] || "—") +
                      (srcList.length > 1 ? ` 等 ${srcList.length} 项` : "");
                    const dstSummary =
                      (dstList[0] || "—") +
                      (dstList.length > 1 ? ` 等 ${dstList.length} 项` : "");
                    const fullRouteTitle = `${srcList.join(", ") || "—"} → ${dstList.join(", ") || "—"}`;
                    const isSelected = selectedSet.has(job.id);
                    const failed = isJobFailed(job);
                    const livePct = Math.min(
                      100,
                      Math.max(0, Math.round(job.liveProgress || 0)),
                    );
                    const liveTotal = job.liveNum
                      ? (job.liveNum.wait || 0) +
                        (job.liveNum.running || 0) +
                        (job.liveNum.success || 0) +
                        (job.liveNum.fail || 0) +
                        (job.liveNum.other || 0)
                      : 0;

                    const rowClassName = [
                      "dashboard-job-row",
                      isSelected ? "is-selected" : "",
                      job.pendingApproval
                        ? "is-approval"
                        : job.running
                          ? "is-running"
                          : failed
                            ? "is-failed"
                            : "",
                      job.enable !== 1 && !job.running ? "is-disabled" : "",
                    ]
                      .filter(Boolean)
                      .join(" ");

                    return (
                      <div key={job.id} className={rowClassName}>
                        <div className="dashboard-job-check">
                          <Checkbox
                            aria-label={`选择 ${jobName}`}
                            checked={isSelected}
                            onChange={(e) =>
                              toggleSelect(job.id, Boolean(e.target.checked))
                            }
                          />
                        </div>

                        <div
                          className="dashboard-job-info"
                          onClick={() => openJobDetail(job)}
                        >
                          <div className="dashboard-job-title-line">
                            <span className="dashboard-job-name-wrap">
                              <span
                                className={jobDotTone(job)}
                                aria-hidden="true"
                              />
                              <button
                                type="button"
                                className="dashboard-job-title-btn"
                                title={jobName}
                                onClick={(e) => {
                                  e.stopPropagation();
                                  openJobDetail(job);
                                }}
                              >
                                {jobName}
                              </button>
                            </span>
                            <Tag
                              size="small"
                              type="light"
                              color={methodTagColor(job.method)}
                            >
                              {methodLabel(job.method)}
                            </Tag>
                            {job.pendingApproval && (
                              <Tag size="small" type="solid" color="orange">
                                {job.pendingApproval.status === "executing"
                                  ? "正在删除"
                                  : "待确认删除"}
                              </Tag>
                            )}
                            {job.queued && (
                              <Tag size="small" type="light" color="cyan">
                                排队等待中
                              </Tag>
                            )}
                            {job.enable !== 1 && (
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
                            <span
                              className="dashboard-meta-engine"
                              title={job.alistName || `引擎 #${job.alistId}`}
                            >
                              <IconServerStroked size="small" aria-hidden="true" />
                              <span>{job.alistName || `引擎 #${job.alistId}`}</span>
                            </span>
                            <span
                              className="dashboard-path-route"
                              title={fullRouteTitle}
                            >
                              <span className="dashboard-path-seg">
                                {srcSummary}
                              </span>
                              <IconArrowRight
                                size="small"
                                className="dashboard-path-arrow"
                                aria-hidden="true"
                              />
                              <span className="dashboard-path-seg">
                                {dstSummary}
                              </span>
                            </span>
                          </div>
                        </div>

                        <div
                          className="dashboard-job-status-col"
                          onClick={() =>
                            !job.running && job.pendingApproval
                              ? openApprovalModal(
                                  job.id,
                                  jobName,
                                  job.pendingApproval,
                                )
                              : openJobDetail(job)
                          }
                        >
                          {job.running ? (
                            <div className="dashboard-job-progress">
                              <div className="dashboard-job-progress-head">
                                <span className="dashboard-job-progress-label">
                                  正在同步
                                </span>
                                <span className="dashboard-job-progress-pct">
                                  {livePct}%
                                </span>
                              </div>
                              <Progress
                                percent={livePct}
                                showInfo={false}
                                stroke="var(--semi-color-primary)"
                              />
                              <div className="dashboard-job-progress-sub">
                                {liveTotal > 0 ? (
                                  <>
                                    <span>
                                      已完成 {job.liveNum?.success ?? 0} /{" "}
                                      {liveTotal} 项
                                    </span>
                                    {(job.liveNum?.fail ?? 0) > 0 && (
                                      <span className="inline-error">
                                        失败 {job.liveNum?.fail}
                                      </span>
                                    )}
                                  </>
                                ) : (
                                  <span>正在扫描文件列表…</span>
                                )}
                              </div>
                            </div>
                          ) : job.pendingApproval ? (
                            <div className="dashboard-job-approval-note">
                              <span className="dashboard-job-approval-title">
                                <IconAlertTriangle
                                  size="small"
                                  aria-hidden="true"
                                />
                                {job.pendingApproval.status === "executing"
                                  ? "正在后台执行删除…"
                                  : `待确认删除 ${job.pendingApproval.deleteCount} 项${
                                      job.pendingApproval.deleteSize > 0
                                        ? ` · ${formatSize(job.pendingApproval.deleteSize)}`
                                        : ""
                                    }`}
                              </span>
                              <span className="dashboard-job-approval-action">
                                {job.pendingApproval.status === "executing"
                                  ? "不影响后续同步任务"
                                  : "同步已完成 · 点击查看清单并审批"}
                              </span>
                            </div>
                          ) : (
                            <>
                              {job.lastTask ? (
                                <div className="dashboard-job-last">
                                  <Status
                                    status={job.lastTask.status}
                                    label={
                                      taskRecordStatusNames[
                                        job.lastTask.status
                                      ] || String(job.lastTask.status)
                                    }
                                    error={job.lastTask.errMsg}
                                  />
                                  <span
                                    className="muted"
                                    title={formatTimestamp(
                                      job.lastTask.createTime,
                                      true,
                                    )}
                                  >
                                    {formatShortTime(job.lastTask.createTime)}
                                  </span>
                                </div>
                              ) : (
                                <div className="dashboard-job-last">
                                  <span className="muted">暂无执行记录</span>
                                </div>
                              )}
                              <div className="dashboard-job-schedule">
                                <span
                                  className="dashboard-job-schedule-item"
                                  title={
                                    job.nextRunTime > 0
                                      ? formatTimestamp(job.nextRunTime, true)
                                      : undefined
                                  }
                                >
                                  <IconClockStroked
                                    size="small"
                                    aria-hidden="true"
                                  />
                                  {formatScheduleHint(job)}
                                </span>
                                {job.lastTask &&
                                  (job.lastTask.allNum !== undefined ||
                                    (job.lastTask.sumSize ?? 0) > 0) && (
                                    <span className="dashboard-job-last-metrics">
                                      {job.lastTask.allNum !== undefined
                                        ? `${job.lastTask.successNum ?? 0}/${job.lastTask.allNum} 项`
                                        : ""}
                                      {job.lastTask.allNum !== undefined &&
                                      (job.lastTask.sumSize ?? 0) > 0
                                        ? " · "
                                        : ""}
                                      {(job.lastTask.sumSize ?? 0) > 0
                                        ? formatSize(job.lastTask.sumSize || 0)
                                        : ""}
                                    </span>
                                  )}
                              </div>
                            </>
                          )}
                        </div>

                        <div className="row-actions dashboard-job-actions">
                          <IconButton
                            label="模拟运行预览"
                            icon={<IconEyeOpenedStroked aria-hidden="true" />}
                            disabled={action.busy}
                            onClick={() =>
                              setDryRunJob({ id: job.id, name: jobName })
                            }
                          />
                          <IconButton
                            label={
                              job.running || job.queued ? "停止任务" : "启动任务"
                            }
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
                          <IconButton
                            label="查看任务详情"
                            icon={<IconChevronRight aria-hidden="true" />}
                            onClick={() => openJobDetail(job)}
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

      {approvalModal && (
        <FullSyncApprovalModal
          approval={approvalModal.approval}
          jobName={approvalModal.jobName}
          loading={Boolean(approvalModal.loading)}
          busy={action.busy}
          onClose={() => setApprovalModal(null)}
          onApprove={() =>
            action.run(async () => {
              const taskId = approvalModal.approval.taskId;
              if (!taskId) return;
              try {
                await api.taskAction(taskId, "approve");
                setApprovalModal(null);
                Toast.success("已提交后台校验并执行删除");
                await refreshAll(true);
              } catch (err) {
                errorToast(err);
              }
            })
          }
          onReject={() =>
            action.run(async () => {
              const taskId = approvalModal.approval.taskId;
              if (!taskId) return;
              try {
                await api.taskAction(taskId, "reject");
                setApprovalModal(null);
                Toast.success("已跳过本次删除，目标端文件已保留");
                await refreshAll(true);
              } catch (err) {
                errorToast(err);
              }
            })
          }
        />
      )}

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
              const createdId = result.createdId;
              const goToCreated = () => {
                if (createdId) {
                  navigate(`/tasks?jobId=${createdId}&tab=overview`);
                }
              };
              if (result.runNow && createdId) {
                const startCreated = () =>
                  action.run(async () => {
                    try {
                      await api.jobAction({ id: String(createdId) });
                      Toast.success("已提交执行");
                    } catch (err) {
                      errorToast(err);
                    }
                    await refreshAll(true);
                    goToCreated();
                  });
                if (result.method === 2) {
                  confirmAction({
                    title: "确认启动移动同步？",
                    content:
                      "移动模式会在文件复制到目标目录并校验后删除源目录中的对应文件，是否确认立即执行？",
                    okText: "确认移动",
                    onOk: startCreated,
                    onCancel: () => {
                      void refreshAll(true).then(goToCreated);
                    },
                  });
                  return;
                }
                void startCreated();
                return;
              }
              void refreshAll(true).then(goToCreated);
            }}
          />
        </Suspense>
      )}
    </div>
  );
}
