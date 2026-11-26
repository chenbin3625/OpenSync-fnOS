import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconAlertTriangle,
  IconArrowRight,
  IconChevronRight,
  IconClockStroked,
  IconCloudStroked,
  IconCopy,
  IconDeleteStroked,
  IconEditStroked,
  IconEyeOpenedStroked,
  IconPause,
  IconPlay,
  IconPlusStroked,
  IconRefresh,
  IconServerStroked,
  IconSettingStroked,
  IconStop,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  ActionMenu,
  EmptyState,
  Header,
  IconButton,
  Info,
  InlineNotice,
  LoadingPanel,
  LoadState,
  SectionCard,
  confirmAction,
  confirmDelete,
  errorToast,
  type MenuAction,
} from "../components/common";
import { CheckModal } from "../components/CheckModal";
import { DryRunModal } from "../components/DryRunModal";
import {
  FullSyncApprovalModal,
  PendingApprovalBanner,
} from "../components/FullSyncApprovalModal";
import {
  SyncStatusTag,
  formatContinuousSyncPlan,
  formatMinutesShort,
  formatRelativeSyncTime,
  formatSecondsShort,
  getContinuousSyncMeta,
  resolveEffectiveSyncStatus,
  syncStatusLabels,
  syncStatusTone,
} from "../components/SyncStatus";
import { useAction, useResource } from "../lib/hooks";
import { describeBwLimit } from "../lib/bwLimit";
import { formatSpeed } from "../lib/dashboardMetrics";
import {
  buildSyncPathPreview,
  formatDisplayPath,
  formatDisplayPathList,
} from "../lib/syncPathPreview";
import {
  buildTaskSearchParams,
  cloneJobToForm,
  isLegacyOpenListJob,
  resolveJobSelection,
  resolveTaskTab,
  summarizeJobFilters,
  type JobForm,
} from "../lib/taskForm";
import type { TaskEditorSavedResult } from "./TaskEditor";
import {
  compareModeLabel,
  conflictPolicyLabel,
  formatDuration,
  formatSize,
  getJobName,
  parseJobPathList,
} from "./Home/homeUtils";
import { calcRealtimeProgress, calcTaskDisplayPercent } from "./Home/taskRows";
import { formatTimestamp } from "../utils/date";
import type {
  AlistItem,
  CurrentTaskData,
  JobItem,
  TaskRecord,
} from "../types";

const Realtime = lazy(() =>
  import("./TaskExecution").then((module) => ({ default: module.Realtime })),
);
const History = lazy(() =>
  import("./TaskExecution").then((module) => ({ default: module.History })),
);
const JobEditor = lazy(() => import("./TaskEditor"));
const currentTaskPollMs = 3000;
const currentTaskRealtimePollMs = 3000;
const currentTaskFollowUpMs = 1000;

export default function Tasks() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const jobIdParam = params.get("jobId");
  const parsedJobId = jobIdParam === null ? null : Number(jobIdParam);
  const selectedId =
    jobIdParam === null
      ? null
      : Number.isInteger(parsedJobId) && (parsedJobId as number) > 0
        ? parsedJobId
        : -1;
  const rawTab = params.get("tab");
  const jobs = useResource((signal) => api.jobMenu(signal));
  const engines = useResource((signal) => api.engines(signal));
  const [editor, setEditor] = useState<{
    open: boolean;
    job: JobItem | null;
    initialDraft?: JobForm;
  }>({
    open: false,
    job: null,
  });
  const [dryRunOpen, setDryRunOpen] = useState(false);
  const [checkOpen, setCheckOpen] = useState(false);
  const [approvalModalOpen, setApprovalModalOpen] = useState(false);
  const [historyRefreshKey, setHistoryRefreshKey] = useState(0);
  const [pendingCreatedId, setPendingCreatedId] = useState<number | null>(null);
  const mobileJobButtons = useRef<Array<HTMLButtonElement | null>>([]);
  const actions = useAction();
  const list = jobs.data?.dataList || [];
  const { job: selected, replaceWith } = resolveJobSelection(list, selectedId, {
    pendingId: pendingCreatedId,
    refreshing: jobs.loading,
  });
  useEffect(() => {
    if (replaceWith === null) return;
    const next = new URLSearchParams(params);
    next.set("jobId", String(replaceWith));
    setParams(next, { replace: true });
  }, [replaceWith, params, setParams]);
  const selectedJobId = selected?.id;
  const deletedJobIdsRef = useRef<Set<number>>(new Set());
  const hasCryptRemote = useMemo(() => {
    if (!selected || !engines.data) return false;
    const eng = engines.data.find((e) => e.id === selected.alistId) ?? engines.data[0];
    if (!eng) return false;
    const detailsByLower = new Map<string, { type?: string; hasCrypt?: string }>();
    for (const [k, v] of Object.entries(eng.remoteDetails || {})) {
      detailsByLower.set(k.toLowerCase(), v);
    }
    const typesByLower = new Map<string, string>();
    for (const [k, v] of Object.entries(eng.remotes || {})) {
      typesByLower.set(k.toLowerCase(), v);
    }
    const paths = [
      ...parseJobPathList(selected.srcPath),
      ...parseJobPathList(selected.dstPath),
    ];
    return paths.some((p) => {
      const seg = p.replace(/^\/+/, "").split("/")[0]?.toLowerCase();
      if (!seg) return false;
      const detail = detailsByLower.get(seg);
      if (detail?.hasCrypt === "true" || detail?.type === "crypt") return true;
      return typesByLower.get(seg) === "crypt";
    });
  }, [selected, engines.data]);
  const [snapshotState, setSnapshotState] = useState<{
    jobId: number;
    ok: boolean;
  } | null>(null);
  const currentTask = useResource(
    async (signal) => {
      if (!selectedJobId || deletedJobIdsRef.current.has(selectedJobId)) {
        return null;
      }
      try {
        const result = await api.current(selectedJobId, signal);
        if (!signal.aborted) {
          setSnapshotState({ jobId: selectedJobId, ok: true });
        }
        return result;
      } catch (err) {
        if (!signal.aborted) {
          setSnapshotState({ jobId: selectedJobId, ok: false });
        }
        throw err;
      }
    },
    [selectedJobId],
  );
  const pendingApproval = useResource(
    async (signal) => {
      if (!selectedJobId || deletedJobIdsRef.current.has(selectedJobId)) {
        return null;
      }
      return api.pendingApproval(selectedJobId, signal);
    },
    [selectedJobId],
  );
  const currentTaskForSelected =
    selectedJobId && snapshotState?.jobId === selectedJobId
      ? currentTask.data
      : null;
  const hasCurrentTask = !!currentTaskForSelected;
  const currentTaskSettled =
    !!selectedJobId &&
    snapshotState?.jobId === selectedJobId &&
    snapshotState.ok &&
    !currentTask.loading &&
    !currentTask.error;
  const tab = resolveTaskTab(rawTab, {
    settled: currentTaskSettled,
    hasCurrentTask,
    jobRunning: Boolean(selected?.running),
  });
  const realtimeTab = tab === "realtime";
  const realtimeAvailable =
    hasCurrentTask ||
    realtimeTab ||
    (!currentTaskSettled && Boolean(selected?.running));
  const activeApproval = pendingApproval.data ?? null;
  const approvalExecuting = activeApproval?.status === "executing";
  const refreshCurrentTask = currentTask.refresh;
  const refreshPendingApproval = pendingApproval.refresh;
  const requestedApprovalOpen = params.get("approval") === "1";

  useEffect(() => {
    setApprovalModalOpen(false);
  }, [selectedJobId]);

  useEffect(() => {
    if (!requestedApprovalOpen || !activeApproval) return;
    setApprovalModalOpen(true);
    const next = new URLSearchParams(params);
    next.delete("approval");
    setParams(next, { replace: true });
  }, [requestedApprovalOpen, activeApproval, params, setParams]);

  const prevTaskIdRef = useRef<number | null>(null);
  useEffect(() => {
    const curId = currentTaskForSelected?.taskId ?? null;
    if (prevTaskIdRef.current !== null && curId === null) {
      void refreshPendingApproval(true);
      void jobs.refresh(true);
      setHistoryRefreshKey((k) => k + 1);
      window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    }
    prevTaskIdRef.current = curId;
  }, [currentTaskForSelected?.taskId, refreshPendingApproval, jobs.refresh]);

  const prevExecutingRef = useRef(false);
  useEffect(() => {
    if (prevExecutingRef.current && !approvalExecuting) {
      setHistoryRefreshKey((k) => k + 1);
      void jobs.refresh(true);
      window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    }
    prevExecutingRef.current = approvalExecuting;
  }, [approvalExecuting, jobs.refresh]);

  useEffect(() => {
    if (!activeApproval && approvalModalOpen) {
      setApprovalModalOpen(false);
    }
  }, [activeApproval, approvalModalOpen]);

  const prevRealtimeTab = useRef(realtimeTab);
  useEffect(() => {
    const enteredRealtime = !prevRealtimeTab.current && realtimeTab;
    prevRealtimeTab.current = realtimeTab;
    if (
      enteredRealtime &&
      selectedJobId &&
      !deletedJobIdsRef.current.has(selectedJobId)
    ) {
      void refreshCurrentTask(true);
    }
  }, [realtimeTab, selectedJobId, refreshCurrentTask]);

  useEffect(() => {
    if (!selectedJobId) {
      setSnapshotState(null);
      return;
    }
    const interval = setInterval(
      () => {
        if (
          document.visibilityState !== "hidden" &&
          !deletedJobIdsRef.current.has(selectedJobId)
        ) {
          void refreshCurrentTask(true);
          void refreshPendingApproval(true);
          void jobs.refresh(true);
        }
      },
      approvalExecuting
        ? 1500
        : realtimeTab
          ? currentTaskRealtimePollMs
          : currentTaskPollMs,
    );
    return () => {
      clearInterval(interval);
    };
  }, [
    selectedJobId,
    realtimeTab,
    approvalExecuting,
    refreshCurrentTask,
    refreshPendingApproval,
    jobs.refresh,
  ]);

  const followUp = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(followUp.current), []);
  const update = useCallback(
    (
      values: Record<string, string | number | null>,
      options?: { replace?: boolean },
    ) => {
      setParams((prev) => {
        const next = new URLSearchParams(prev);
        for (const [key, value] of Object.entries(values))
          value === null ? next.delete(key) : next.set(key, String(value));
        return next;
      }, options);
    },
    [setParams],
  );
  const selectMobileJob = useCallback(
    (job: JobItem) => {
      setParams((prev) =>
        buildTaskSearchParams(job, {
          onTasksPage: true,
          currentSearch: prev.toString(),
          activeJobId: selected?.id,
        }),
      );
    },
    [selected?.id, setParams],
  );
  const handleRealtimeEnded = useCallback(() => {
    void refreshPendingApproval(true);
    void jobs.refresh(true);
    setHistoryRefreshKey((k) => k + 1);
  }, [refreshPendingApproval, jobs.refresh]);
  const handleOpenRealtime = useCallback(
    () => update({ tab: "realtime" }),
    [update],
  );
  const handleOpenOverview = useCallback(
    () => update({ tab: "overview" }),
    [update],
  );
  const handleOpenHistory = useCallback(
    () => update({ tab: "history" }),
    [update],
  );
  const refreshJobs = async (options?: { skipCurrentTask?: boolean }) => {
    await jobs.refresh(true);
    window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    if (options?.skipCurrentTask) {
      clearTimeout(followUp.current);
      return;
    }
    await Promise.all([
      currentTask.refresh(true),
      pendingApproval.refresh(true),
    ]);
    clearTimeout(followUp.current);
    followUp.current = setTimeout(() => {
      void refreshCurrentTask(true);
      void refreshPendingApproval(true);
    }, currentTaskFollowUpMs);
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
  const triggerSyncNow = (jobId: number) => {
    void actions.run(async () => {
      try {
        await api.jobAction({ id: String(jobId) });
        Toast.success("已触发立即同步");
        await refreshJobs();
      } catch (err) {
        errorToast(err);
      }
    });
  };
  const emptyAction =
    !engines.loading && !engines.error && (engines.data?.length ?? 0) === 0 ? (
      <div className="empty-guide">
        <span className="muted">
          存储服务尚未就绪，请先前往存储管理检查本地目录或添加远程存储
        </span>
        <Button
          type="primary"
          theme="solid"
          onClick={() => navigate("/engines")}
        >
          前往存储管理
        </Button>
      </div>
    ) : !engines.loading ? (
      <Button
        type="primary"
        theme="solid"
        icon={<IconPlusStroked aria-hidden="true" />}
        onClick={() => setEditor({ open: true, job: null })}
      >
        新建同步任务
      </Button>
    ) : undefined;
  const approvePendingDelete = (selectedPaths?: string[]) =>
    actions.run(async () => {
      if (!activeApproval?.taskId) return;
      try {
        await api.taskAction(
          activeApproval.taskId,
          "approve",
          undefined,
          selectedPaths && selectedPaths.length > 0
            ? { paths: selectedPaths }
            : undefined,
        );
        setApprovalModalOpen(false);
        Toast.success(
          selectedPaths && selectedPaths.length > 0
            ? `已提交后台校验并删除选中的 ${selectedPaths.length} 项`
            : "已提交后台校验并执行删除",
        );
        await refreshJobs();
      } catch (err) {
        errorToast(err);
      }
    });
  const excludePendingDelete = (selectedPaths: string[]) =>
    actions.run(async () => {
      if (!activeApproval?.taskId || !selectedPaths.length) return;
      try {
        const res = await api.excludeApprovalPaths(
          activeApproval.taskId,
          selectedPaths,
        );
        setApprovalModalOpen(false);
        Toast.success(`已将 ${res.excludedCount} 项加入任务排除规则`);
        await refreshJobs();
      } catch (err) {
        errorToast(err);
      }
    });
  const rejectPendingDelete = () =>
    actions.run(async () => {
      if (!activeApproval?.taskId) return;
      try {
        await api.taskAction(activeApproval.taskId, "reject");
        setApprovalModalOpen(false);
        Toast.success("已跳过本次删除，目标端文件已保留");
        await refreshJobs();
      } catch (err) {
        errorToast(err);
      }
    });

  return (
    <div className="page task-page">
      {selected && activeApproval && approvalModalOpen && (
        <FullSyncApprovalModal
          approval={activeApproval}
          jobName={getJobName(selected)}
          busy={actions.busy}
          onClose={() => setApprovalModalOpen(false)}
          onApprove={approvePendingDelete}
          onExclude={excludePendingDelete}
          onReject={rejectPendingDelete}
        />
      )}
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
                !realtimeAvailable ? (
                  <Tooltip content="当前没有正在传输的批次" position="top">
                    <span>实时任务</span>
                  </Tooltip>
                ) : (
                  <span className="realtime-tab-label">
                    {hasCurrentTask && (
                      <span
                        className="kpi-dot kpi-dot--primary kpi-dot--pulse"
                        aria-hidden="true"
                      />
                    )}
                    <span>实时任务</span>
                  </span>
                )
              }
              itemKey="realtime"
              disabled={!realtimeAvailable}
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
      {engines.error && !engines.loading && (
        <InlineNotice
          className="engine-load-error"
          role="alert"
          message={`存储配置加载失败：${engines.error}`}
          onRetry={() => void engines.refresh()}
        />
      )}
      {list.length > 1 && (
        <div
          className="mobile-job-bar"
          role="tablist"
          aria-label="任务列表"
          onKeyDown={(event) => {
            if (
              !["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)
            )
              return;
            event.preventDefault();
            const index = Math.max(
              0,
              list.findIndex((job) => job.id === selected?.id),
            );
            const nextIndex =
              event.key === "Home"
                ? 0
                : event.key === "End"
                  ? list.length - 1
                  : event.key === "ArrowLeft"
                    ? Math.max(0, index - 1)
                    : Math.min(list.length - 1, index + 1);
            const nextJob = list[nextIndex];
            if (nextJob) {
              selectMobileJob(nextJob);
              window.requestAnimationFrame(() =>
                mobileJobButtons.current[nextIndex]?.focus(),
              );
            }
          }}
        >
          {list.map((job, index) => (
            <button
              key={job.id}
              type="button"
              role="tab"
              aria-selected={selected?.id === job.id}
              tabIndex={selected?.id === job.id ? 0 : -1}
              ref={(element) => {
                mobileJobButtons.current[index] = element;
              }}
              className={`mobile-job-chip${selected?.id === job.id ? " active" : ""}`}
              onClick={() => selectMobileJob(job)}
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
          <EmptyState action={emptyAction} />
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
                {activeApproval && (
                  <PendingApprovalBanner
                    approval={activeApproval}
                    busy={actions.busy}
                    onInspect={() => setApprovalModalOpen(true)}
                    onReject={() => void rejectPendingDelete()}
                  />
                )}
                {tab === "overview" && (
                  <Overview
                    job={selected}
                    engines={engines.data || []}
                    busy={actions.busy}
                    currentTask={currentTaskForSelected}
                    paused={Boolean(
                      currentTaskForSelected?.paused ?? selected.paused,
                    )}
                    hasPendingApproval={Boolean(activeApproval)}
                    runKey={String(currentTaskForSelected?.taskId ?? "idle")}
                    onRun={() => triggerSyncNow(selected.id)}
                    onOpenRealtime={handleOpenRealtime}
                    onOpenApproval={() => setApprovalModalOpen(true)}
                    onDryRun={() => setDryRunOpen(true)}
                    onCheck={() => setCheckOpen(true)}
                    onResyncBisync={() => {
                      confirmAction({
                        title: "重建双向同步基线（--resync）？",
                        content:
                          "适用于首次运行异常中断、存储更换或基线清单丢失后的恢复。确认后将立即执行一次带 --resync 的双向同步，重建基线并恢复持续同步。",
                        okText: "立即重建并同步",
                        onOk: () =>
                          actions.run(async () => {
                            try {
                              await api.resetBisyncJob(selected.id, true);
                              Toast.success("已触发双向基线重建同步");
                              await refreshJobs();
                            } catch (err) {
                              errorToast(err);
                              throw err;
                            }
                          }),
                      });
                    }}
                    onClone={() =>
                      setEditor({
                        open: true,
                        job: null,
                        initialDraft: cloneJobToForm(selected),
                      })
                    }
                    onTogglePause={() => {
                      if (!currentTaskForSelected) return;
                      const { taskId } = currentTaskForSelected;
                      const resuming = Boolean(currentTaskForSelected.paused);
                      action(
                        () => api.taskAction(taskId, resuming ? "resume" : "pause"),
                        resuming ? "已继续传输" : "已暂停传输",
                      );
                    }}
                    onStop={() => {
                      if (!currentTaskForSelected) return;
                      const { taskId } = currentTaskForSelected;
                      confirmAction({
                        title: "中止本次同步批次？",
                        content:
                          "仅中止当前正在进行的传输批次，已完成的文件不会撤销；持续同步仍保持开启。如需完全停止，请点击「暂停同步」。",
                        okText: "中止本次",
                        okLabel: "中止本次同步",
                        onOk: () =>
                          actions.run(async () => {
                            try {
                              await api.taskAction(taskId, "stop");
                              Toast.success("已提交中止本次同步");
                              await refreshJobs();
                            } catch (err) {
                              errorToast(err);
                              throw err;
                            }
                          }),
                      });
                    }}
                    onToggle={() => {
                      const pausing = selected.enable === 1;
                      if (pausing && currentTaskForSelected) {
                        confirmAction({
                          title: "暂停持续同步并中止当前传输？",
                          content:
                            "暂停后将停止本地与远端监控，并中止当前正在进行的传输批次（已完成的文件不会撤销），后续可随时恢复并自动补齐期间变更。",
                          okText: "暂停同步",
                          onOk: () =>
                            actions.run(async () => {
                              try {
                                await api.jobAction({
                                  id: String(selected.id),
                                  pause: true,
                                });
                                Toast.success("持续同步已暂停");
                                await refreshJobs();
                              } catch (err) {
                                errorToast(err);
                                throw err;
                              }
                            }),
                        });
                        return;
                      }
                      action(
                        () =>
                          api.jobAction({
                            id: String(selected.id),
                            pause: pausing,
                          }),
                        pausing ? "持续同步已暂停" : "持续同步已恢复",
                      );
                    }}
                    onEdit={() => setEditor({ open: true, job: selected })}
                    onDelete={() => {
                      const deletingId = selected.id;
                      confirmDelete(
                        "删除此持续同步任务？",
                        () =>
                          actions.run(async () => {
                            clearTimeout(followUp.current);
                            await api.deleteJob(deletingId);
                            deletedJobIdsRef.current.add(deletingId);
                            update({ jobId: null });
                            await refreshJobs({ skipCurrentTask: true });
                          }),
                        "此任务的历史同步记录与同步状态将一并删除。",
                      );
                    }}
                  />
                )}
                {tab === "realtime" && (
                  <Suspense fallback={<LoadingPanel />}>
                    <Realtime
                      key={selected.id}
                      jobId={selected.id}
                      syncStatus={resolveEffectiveSyncStatus(selected)}
                      onTaskEnded={handleRealtimeEnded}
                      onOpenOverview={handleOpenOverview}
                      onOpenHistory={handleOpenHistory}
                      onSyncNow={
                        selected.enable === 1 && !isLegacyOpenListJob(selected, engines.data)
                          ? () => triggerSyncNow(selected.id)
                          : undefined
                      }
                    />
                  </Suspense>
                )}
                {tab === "history" && (
                  <Suspense fallback={<LoadingPanel />}>
                    <History
                      key={selected.id}
                      jobId={selected.id}
                      currentTaskId={currentTaskForSelected?.taskId}
                      refreshKey={historyRefreshKey}
                      disableRetry={
                        isLegacyOpenListJob(selected, engines.data) ||
                        selected.method === 3
                      }
                      onOpenRealtime={handleOpenRealtime}
                    />
                  </Suspense>
                )}
              </div>
            ) : jobs.loading || jobs.error || pendingCreatedId !== null ? null : (
              <div className="task-empty-detail">
                <EmptyState />
              </div>
            )}
          </section>
        )}
      </div>
      {dryRunOpen && selected && (
        <DryRunModal
          jobId={selected.id}
          jobName={getJobName(selected)}
          onClose={() => setDryRunOpen(false)}
          onRunReal={() => triggerSyncNow(selected.id)}
        />
      )}
      {checkOpen && selected && (
        <CheckModal
          jobId={selected.id}
          jobName={getJobName(selected)}
          hasCryptRemote={hasCryptRemote}
          onClose={() => setCheckOpen(false)}
          onRepairStarted={() => {
            void refreshJobs();
          }}
        />
      )}
      {editor.open && (
        <Suspense fallback={<LoadingPanel />}>
          <JobEditor
            job={editor.job}
            initialDraft={editor.initialDraft}
            engines={engines.data || []}
            existingJobs={list}
            enginesError={engines.error}
            onRetryEngines={() => void engines.refresh()}
            onNavigateEngines={() => {
              setEditor({ open: false, job: null });
              navigate("/engines");
            }}
            onClose={() => setEditor({ open: false, job: null })}
            onSaved={(result: TaskEditorSavedResult) => {
              setEditor({ open: false, job: null });
              const createdId = result.createdId;
              if (createdId) {
                setPendingCreatedId(createdId);
                update({
                  jobId: createdId,
                  tab: "overview",
                });
              }
              void (async () => {
                try {
                  if (createdId) {
                    const fresh = await api.jobMenu();
                    jobs.setData(fresh);
                    window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
                  } else {
                    await refreshJobs();
                  }
                } catch {
                  await refreshJobs({ skipCurrentTask: Boolean(createdId) });
                } finally {
                  if (createdId) {
                    setPendingCreatedId((cur) =>
                      cur === createdId ? null : cur,
                    );
                  }
                }
              })();
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
  currentTask,
  paused,
  hasPendingApproval,
  runKey,
  onRun,
  onOpenRealtime,
  onOpenApproval,
  onDryRun,
  onCheck,
  onResyncBisync,
  onClone,
  onTogglePause,
  onStop,
  onToggle,
  onEdit,
  onDelete,
}: {
  job: JobItem;
  engines: AlistItem[];
  busy: boolean;
  currentTask: CurrentTaskData | null;
  paused: boolean;
  hasPendingApproval: boolean;
  runKey: string;
  onRun: () => void;
  onOpenRealtime: () => void;
  onOpenApproval: () => void;
  onDryRun: () => void;
  onCheck: () => void;
  onResyncBisync: () => void;
  onClone: () => void;
  onTogglePause: () => void;
  onStop: () => void;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const latestExecution = useResource(
    (signal) => api.history(job.id, { pageNum: 1, pageSize: 1 }, signal),
    [job.id],
  );
  const refreshLatest = latestExecution.refresh;
  const firstRunKey = useRef(runKey);
  useEffect(() => {
    if (firstRunKey.current === runKey) return;
    firstRunKey.current = runKey;
    void refreshLatest(true);
  }, [runKey, refreshLatest]);

  const legacy = isLegacyOpenListJob(job, engines);
  const engine = legacy
    ? undefined
    : (engines.find((e) => e.id === job.alistId) ?? engines[0]);
  const engineName = legacy
    ? "旧版 OpenList（已失效）"
    : engine?.remark || "内置存储";
  const enabled = !legacy && job.enable === 1;
  const running = Boolean(currentTask);
  const meta = useMemo(() => getContinuousSyncMeta(job), [job]);
  const effectiveStatus = useMemo(
    () =>
      resolveEffectiveSyncStatus({
        enable: job.enable,
        running,
        paused,
        pendingApproval: hasPendingApproval,
        syncStatus: job.syncStatus,
      }),
    [job.enable, running, paused, hasPendingApproval, job.syncStatus],
  );
  const statusTone = syncStatusTone(effectiveStatus.state);

  const liveMetrics = useMemo(() => {
    if (!currentTask) return null;
    const calc = calcRealtimeProgress(currentTask, null);
    const totalBytes = calc.doneSize + calc.remainSize;
    const totalItems =
      (currentTask.num?.wait || 0) +
      (currentTask.num?.running || 0) +
      (currentTask.num?.success || 0) +
      (currentTask.num?.fail || 0) +
      (currentTask.num?.other || 0);
    const percent = calcTaskDisplayPercent({
      doneSize: calc.doneSize,
      totalSize: totalBytes,
      num: currentTask.num,
      scanFinish: currentTask.scanFinish,
    });
    return {
      ...calc,
      totalBytes,
      totalItems,
      successItems: currentTask.num?.success || 0,
      failItems: currentTask.num?.fail || 0,
      percent,
    };
  }, [currentTask]);

  const filterSummaries = useMemo(() => summarizeJobFilters(job), [job]);
  const extraFlagLabels = useMemo(() => {
    const raw = job.extraFlags;
    if (!raw) return [] as string[];
    let obj: Record<string, unknown> | null = null;
    if (typeof raw === "string") {
      try {
        obj = JSON.parse(raw) as Record<string, unknown>;
      } catch {
        obj = null;
      }
    } else if (typeof raw === "object") {
      obj = raw as Record<string, unknown>;
    }
    if (!obj) return [] as string[];
    const list: string[] = [];
    if (obj.noUpdateModtime) list.push("跳过仅时间更新");
    if (obj.inplace) list.push("原地写入");
    if (obj.checkFirst) list.push("先比对后传输");
    if (obj.s3NoHead) list.push("跳过 S3 HEAD");
    if (obj.ignoreSize) list.push("忽略大小差异");
    if (obj.trackRenames) list.push("追踪重命名");
    if (obj.useMmap) list.push("mmap 缓冲");
    if (obj.resumeEnable) list.push("断点续传");
    if (obj.directStream) list.push("直接流式同步");
    if (Number(obj.autoRetryMax || 0) > 0) {
      list.push(`自动重试 ${obj.autoRetryMax} 次`);
    }
    if (Number(obj.postSyncJobId || 0) > 0) {
      list.push(`串联任务 #${obj.postSyncJobId}`);
    }
    if (typeof obj.checkSchedule === "string" && obj.checkSchedule.trim()) {
      const m: Record<string, string> = {
        daily: "每日校验",
        weekly: "每周校验",
        monthly: "每月校验",
      };
      list.push(m[obj.checkSchedule.trim()] || obj.checkSchedule.trim());
    }
    if (typeof obj.maxTransfer === "string" && obj.maxTransfer.trim()) {
      list.push(`上限 ${obj.maxTransfer.trim()}`);
    }
    if (typeof obj.cutoffMode === "string" && obj.cutoffMode.trim()) {
      list.push(`截断 ${obj.cutoffMode.trim()}`);
    }
    if (typeof obj.maxDuration === "string" && obj.maxDuration.trim()) {
      list.push(`限时 ${obj.maxDuration.trim()}`);
    }
    if (typeof obj.orderBy === "string" && obj.orderBy.trim()) {
      list.push(`排序 ${obj.orderBy.trim()}`);
    }
    if (Number(obj.maxBacklog || 0) > 0) list.push(`队列上限 ${obj.maxBacklog}`);
    if (typeof obj.bufferSize === "string" && obj.bufferSize.trim()) {
      list.push(`缓冲 ${obj.bufferSize.trim()}`);
    }
    if (typeof obj.multiThreadCutoff === "string" && obj.multiThreadCutoff.trim()) {
      list.push(`多线程阈值 ${obj.multiThreadCutoff.trim()}`);
    }
    if (typeof obj.multiThreadChunkSize === "string" && obj.multiThreadChunkSize.trim()) {
      list.push(`多线程块 ${obj.multiThreadChunkSize.trim()}`);
    }
    if (typeof obj.userAgent === "string" && obj.userAgent.trim()) {
      list.push("自定义 UA");
    }
    if (typeof obj.ioTimeout === "string" && obj.ioTimeout.trim()) {
      list.push(`I/O超时 ${obj.ioTimeout.trim()}`);
    }
    if (typeof obj.conTimeout === "string" && obj.conTimeout.trim()) {
      list.push(`连接超时 ${obj.conTimeout.trim()}`);
    }
    if (typeof obj.bisyncResyncMode === "string" && obj.bisyncResyncMode.trim()) {
      list.push(`基线策略 ${obj.bisyncResyncMode.trim()}`);
    }
    if (Number(obj.bisyncMaxDelete || 0) > 0) {
      list.push(`最多删除 ${obj.bisyncMaxDelete}%`);
    }
    if (obj.bisyncForce) list.push("强制(--force)");
    return list;
  }, [job.extraFlags]);

  const throttleLabels = [
    Number(job.copyConcurrency || 0) > 0 ? `操作并发 ${job.copyConcurrency}` : "",
    Number(job.scanConcurrency || 0) > 0 ? `扫描并发 ${job.scanConcurrency}` : "",
    job.maxRetries !== undefined && Number(job.maxRetries) >= 0
      ? `重试 ${job.maxRetries} 次`
      : "",
    job.bwLimit?.trim()
      ? (() => {
          const trimmed = job.bwLimit.trim();
          if (/^(?:off|0)$/i.test(trimmed)) return "不限速（覆盖全局）";
          const bwInfo = describeBwLimit(trimmed);
          return bwInfo.hasSchedule
            ? bwInfo.chipLabel
            : `限速 ${bwInfo.activeRateCompact}`;
        })()
      : "",
    Number(job.tpsLimit || 0) > 0 ? `TPS ${job.tpsLimit}/s` : "",
    Number(job.tpsLimitBurst || 0) > 0 ? `突发 ${job.tpsLimitBurst}` : "",
    job.modifyWindow?.trim() ? `时间容差 ${job.modifyWindow.trim()}` : "",
    Number(job.multiThreadStreams || 0) > 0
      ? `${job.multiThreadStreams} 线程/文件`
      : "",
    Boolean(job.fastList) ? "fast-list" : "",
    Boolean(job.createEmptySrcDirs) ? "同步空目录" : "",
    Boolean(job.followSymlinks) ? "跟随符号链接" : "",
    ...extraFlagLabels,
  ].filter(Boolean);

  const srcList = parseJobPathList(job.srcPath);
  const dstList = parseJobPathList(job.dstPath);
  const displaySiblings = [...srcList, ...dstList];
  const pathPreview = useMemo(
    () =>
      buildSyncPathPreview({
        srcPaths: srcList,
        dstPaths: dstList,
        storedDstMap: job.dstMap,
        method: job.method,
      }),
    [job.srcPath, job.dstPath, job.dstMap, job.method],
  );
  const showPathMapping =
    pathPreview.items.length > 1 ||
    pathPreview.items.some((item) => Boolean(item.appendedSuffix));

  const menu: MenuAction[] = legacy
    ? [
        {
          label: "删除",
          icon: <IconDeleteStroked aria-hidden="true" />,
          onClick: onDelete,
          danger: true,
        },
      ]
    : [
        {
          label: "模拟运行预览",
          icon: <IconEyeOpenedStroked aria-hidden="true" />,
          onClick: onDryRun,
        },
        {
          label: "数据一致性校验",
          icon: <IconTickCircle aria-hidden="true" />,
          onClick: onCheck,
        },
        ...(job.method === 3
          ? [
              {
                label: "重建双向基线（--resync）",
                icon: <IconRefresh aria-hidden="true" />,
                onClick: onResyncBisync,
              },
            ]
          : []),
        {
          label: "复制任务",
          icon: <IconCopy aria-hidden="true" />,
          onClick: onClone,
        },
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

  const notifyBindingText = (() => {
    const mode = Number(job.notifyMode ?? 0);
    if (mode === 2) return "已关闭此任务通知";
    if (mode === 1) {
      let count = 0;
      if (Array.isArray(job.notifyIds)) count = job.notifyIds.length;
      else if (typeof job.notifyIds === "string") {
        try {
          const arr = JSON.parse(job.notifyIds);
          if (Array.isArray(arr)) count = arr.length;
        } catch {
          count = 0;
        }
      }
      return `已绑定 ${count} 个指定通知渠道`;
    }
    return "";
  })();

  const srcRoleLabel =
    meta.directionKey === "bidirectional"
      ? "左侧目录"
      : meta.directionKey === "upload"
        ? "本地目录"
        : meta.directionKey === "download"
          ? "远端目录"
          : "源目录";
  const dstRoleLabel =
    meta.directionKey === "bidirectional"
      ? "右侧目录"
      : meta.directionKey === "upload"
        ? "远端目录"
        : meta.directionKey === "download"
          ? "本地目录"
          : "目标目录";

  const statusHeadline = (() => {
    if (running) {
      if (paused) return "本次传输已暂停";
      if (currentTask && !currentTask.scanFinish) {
        return currentTask.firstSync ? "正在同步（扫描中）" : "正在扫描变更";
      }
      return "正在同步变更";
    }
    switch (effectiveStatus.state) {
      case "synced":
        return "已同步";
      case "monitoring":
        return "正在持续监控变更";
      case "pending":
        return effectiveStatus.pendingChanges > 0
          ? `检测到 ${effectiveStatus.pendingChanges} 项待同步`
          : "等待执行完整对账";
      case "scanning":
        return "正在检测目录变更";
      case "waiting_window":
        return "等待进入运行窗口";
      case "waiting_approval":
        return "存在待确认删除项";
      case "paused":
        return "持续同步已暂停";
      case "needs_rebuild":
        return "双向同步基线需要重建";
      case "error":
        return effectiveStatus.failures > 1
          ? `同步异常（连续失败 ${effectiveStatus.failures} 次）`
          : "同步异常";
      default:
        return syncStatusLabels[effectiveStatus.state] || "监控中";
    }
  })();

  const statusSubtitle = (() => {
    if (running) {
      if (paused) return "当前传输流已暂停，可随时继续传输或中止本次批次";
      return "检测到变更，正在自动同步至目标端，无需手动干预";
    }
    switch (effectiveStatus.state) {
      case "synced":
        return effectiveStatus.lastSuccess > 0
          ? `双端已一致 · 最近同步 ${formatRelativeSyncTime(effectiveStatus.lastSuccess)}`
          : "双端已一致，持续监控新增与修改";
      case "monitoring":
        return "本地实时监听与远端周期检测已就绪，发现变更后自动同步";
      case "pending":
        return `等待文件写入静默（${formatSecondsShort(meta.debounceSec)}）后自动开始传输`;
      case "scanning":
        return "正在比对本地与远端目录快照";
      case "waiting_window":
        return job.windowEnable
          ? `当前不在允许窗口（${job.windowStart || "00:00"}–${job.windowEnd || "06:00"}），窗口恢复后自动补齐`
          : "当前处于窗口外，窗口恢复后自动补齐";
      case "waiting_approval":
        return "新增与修改已同步完成，请确认目标端多余文件的删除清单";
      case "paused":
        return "恢复同步后将自动对账并补齐暂停期间产生的变更";
      case "needs_rebuild":
        return "存储端点或配置发生变动，重建双向基线后可恢复自动同步";
      case "error":
        return "系统将按退避策略自动重试，你也可以修复存储后点击立即重试";
      default:
        return "";
    }
  })();

  const pendingSummary =
    effectiveStatus.pendingChanges < 0
      ? "待完整核对"
      : effectiveStatus.pendingChanges > 0
        ? `${effectiveStatus.pendingChanges} 项待同步`
        : "已全部同步";

  return (
    <div className="overview">
      <SectionCard
        className="overview-card"
        headClassName="overview-head"
        title={
          <span className="engine-title-row">
            <h2 title={getJobName(job)}>{getJobName(job)}</h2>
            {legacy ? (
              <Tag size="small" type="solid" color="red">
                旧版已停用
              </Tag>
            ) : (
              <>
                <Tag
                  size="small"
                  type="light"
                  color={meta.directionKey === "bidirectional" ? "violet" : "blue"}
                >
                  {meta.shortLabel}
                </Tag>
                {meta.directionKey !== "bidirectional" && meta.syncDelete && (
                  <Tag size="small" type="light" color="orange">
                    同步删除
                  </Tag>
                )}
                {job.queued && (
                  <Tag size="small" type="light" color="cyan">
                    排队等待中
                  </Tag>
                )}
                <SyncStatusTag status={effectiveStatus} />
              </>
            )}
          </span>
        }
        extra={
          <div className="overview-head-actions">
            {legacy ? (
              <Button
                size="small"
                type="danger"
                theme="solid"
                icon={<IconDeleteStroked aria-hidden="true" />}
                disabled={busy}
                onClick={onDelete}
              >
                删除任务
              </Button>
            ) : (
              <>
                <Button
                  size="small"
                  type={enabled ? "tertiary" : "primary"}
                  theme={enabled ? "light" : "solid"}
                  icon={
                    enabled ? (
                      <IconPause aria-hidden="true" />
                    ) : (
                      <IconPlay aria-hidden="true" />
                    )
                  }
                  disabled={busy}
                  onClick={onToggle}
                >
                  {enabled ? "暂停同步" : "恢复同步"}
                </Button>
                {running ? (
                  <>
                    <IconButton
                      label={paused ? "继续传输" : "暂停传输"}
                      icon={
                        paused ? (
                          <IconPlay aria-hidden="true" />
                        ) : (
                          <IconPause aria-hidden="true" />
                        )
                      }
                      disabled={busy}
                      onClick={onTogglePause}
                    />
                    <IconButton
                      label="中止本次同步"
                      danger
                      icon={<IconStop aria-hidden="true" />}
                      disabled={busy}
                      onClick={onStop}
                    />
                  </>
                ) : (
                  <Tooltip
                    content={enabled ? "立即检测并同步一次" : "请先恢复持续同步"}
                    position="top"
                  >
                    <Button
                      size="small"
                      type="tertiary"
                      theme="light"
                      icon={<IconRefresh aria-hidden="true" />}
                      disabled={busy || !enabled}
                      onClick={onRun}
                    >
                      立即同步
                    </Button>
                  </Tooltip>
                )}
                <IconButton
                  label="模拟运行预览（只读）"
                  icon={<IconEyeOpenedStroked aria-hidden="true" />}
                  disabled={busy}
                  onClick={onDryRun}
                />
                <IconButton
                  label="数据一致性校验（rclone check）"
                  icon={<IconTickCircle aria-hidden="true" />}
                  disabled={busy}
                  onClick={onCheck}
                />
                <IconButton
                  label="编辑任务"
                  icon={<IconEditStroked aria-hidden="true" />}
                  disabled={busy}
                  onClick={onEdit}
                />
                <ActionMenu actions={menu} disabled={busy} />
              </>
            )}
          </div>
        }
      >
        {legacy && (
          <InlineNotice
            role="alert"
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message="该任务关联旧版 OpenList 引擎，升级后已自动停用且无法运行或编辑。请删除此任务后，在「存储管理」接入对应存储并重新创建持续同步任务。"
          />
        )}

        {/* Cloud Sync Style 2-Endpoint Visual Route */}
        <div className="sync-route-card">
          <div className="sync-route-node">
            <div className="flow-name">
              {meta.directionKey === "download" ? (
                <IconCloudStroked aria-hidden="true" />
              ) : (
                <IconServerStroked aria-hidden="true" />
              )}
              {srcRoleLabel}
            </div>
            <div className="flow-sub flow-paths">
              {formatDisplayPathList(srcList, displaySiblings).join("\n") || "—"}
            </div>
          </div>

          <div className="sync-route-connector" aria-label={meta.directionLabel}>
            <span className="sync-route-arrow" aria-hidden="true">
              {meta.arrow}
            </span>
            <span className="sync-route-badge">{meta.shortLabel}</span>
            <span className="sync-route-policy muted">
              {meta.directionKey === "bidirectional"
                ? "双端保持一致"
                : meta.syncDelete
                  ? "同步删除"
                  : "保留目标文件"}
            </span>
          </div>

          <div className="sync-route-node">
            <div className="flow-name" title={engineName}>
              {meta.directionKey === "download" ? (
                <IconServerStroked aria-hidden="true" />
              ) : (
                <IconCloudStroked aria-hidden="true" />
              )}
              {dstRoleLabel}
            </div>
            <div className="flow-sub flow-paths">
              {formatDisplayPathList(dstList, displaySiblings).join("\n") || "—"}
            </div>
          </div>
        </div>

        {/* Cloud Sync Continuous Status Card */}
        {!legacy && (
          <div className={`continuous-status-card is-${statusTone}`}>
            <div className="continuous-status-head">
              <div className="continuous-status-main">
                <span
                  className={`continuous-status-indicator is-${statusTone}${
                    running && !paused ? " is-pulse" : ""
                  }`}
                  aria-hidden="true"
                >
                  {statusTone === "success" ? (
                    <IconTickCircle size="large" />
                  ) : statusTone === "danger" ? (
                    <IconAlertTriangle size="large" />
                  ) : statusTone === "warning" ? (
                    <IconClockStroked size="large" />
                  ) : statusTone === "muted" ? (
                    <IconPause size="large" />
                  ) : (
                    <IconRefresh size="large" />
                  )}
                </span>
                <div className="continuous-status-copy">
                  <div className="continuous-status-title">{statusHeadline}</div>
                  {statusSubtitle && (
                    <div className="continuous-status-sub">{statusSubtitle}</div>
                  )}
                </div>
              </div>
              <div className="continuous-status-cta">
                {running ? (
                  <Button
                    size="small"
                    type="primary"
                    theme="light"
                    icon={<IconChevronRight aria-hidden="true" />}
                    iconPosition="right"
                    onClick={onOpenRealtime}
                  >
                    查看传输明细
                  </Button>
                ) : effectiveStatus.state === "needs_rebuild" ? (
                  <Button
                    size="small"
                    type="danger"
                    theme="solid"
                    icon={<IconRefresh aria-hidden="true" />}
                    disabled={busy}
                    onClick={onResyncBisync}
                  >
                    重建双向基线
                  </Button>
                ) : effectiveStatus.state === "error" && enabled ? (
                  <Button
                    size="small"
                    type="danger"
                    theme="light"
                    icon={<IconRefresh aria-hidden="true" />}
                    disabled={busy}
                    onClick={onRun}
                  >
                    立即重试
                  </Button>
                ) : effectiveStatus.state === "waiting_approval" && hasPendingApproval ? (
                  <Button
                    size="small"
                    type="warning"
                    theme="solid"
                    disabled={busy}
                    onClick={onOpenApproval}
                  >
                    查看待确认删除
                  </Button>
                ) : !enabled ? (
                  <Button
                    size="small"
                    type="primary"
                    theme="solid"
                    icon={<IconPlay aria-hidden="true" />}
                    disabled={busy}
                    onClick={onToggle}
                  >
                    恢复同步
                  </Button>
                ) : null}
              </div>
            </div>

            {currentTask && liveMetrics && (
              <div className="continuous-live-progress">
                <div className="continuous-live-progress-meta">
                  <span className="continuous-live-speed">
                    {paused
                      ? "已暂停"
                      : liveMetrics.speed > 0
                        ? formatSpeed(liveMetrics.speed)
                        : currentTask.scanFinish
                          ? "0 B/s"
                          : "扫描比对中"}
                  </span>
                  <span className="muted">
                    已完成 {formatSize(liveMetrics.doneSize)} /{" "}
                    {formatSize(liveMetrics.totalBytes)}
                    {liveMetrics.totalItems > 0
                      ? `（${liveMetrics.successItems} / ${liveMetrics.totalItems} 项）`
                      : ""}
                  </span>
                  {liveMetrics.failItems > 0 && (
                    <span className="inline-error">
                      失败 {liveMetrics.failItems}
                    </span>
                  )}
                  {!paused && liveMetrics.remainTime > 0 && (
                    <span className="muted">
                      剩余 {formatDuration(liveMetrics.remainTime)}
                    </span>
                  )}
                  <strong className="continuous-live-pct">
                    {liveMetrics.percent}%
                  </strong>
                </div>
                <Progress
                  percent={liveMetrics.percent}
                  showInfo={false}
                  stroke={
                    paused
                      ? "var(--semi-color-warning)"
                      : "var(--semi-color-primary)"
                  }
                />
                {!currentTask.scanFinish && currentTask.scan && (
                  <div className="continuous-live-scan muted">
                    已扫描 {currentTask.scan.scannedDirs} 个目录
                    {currentTask.scan.totalFiles
                      ? ` · 共 ${currentTask.scan.totalFiles} 个文件${
                          currentTask.scan.checkedFiles
                            ? `（已核对 ${currentTask.scan.checkedFiles}）`
                            : ""
                        }`
                      : ` · 待扫描 ${currentTask.scan.remainingDirs} 个目录`}
                  </div>
                )}
              </div>
            )}

            {effectiveStatus.error && (
              <div className="continuous-status-error" role="alert">
                <IconAlertTriangle aria-hidden="true" size="small" />
                <span>{effectiveStatus.error}</span>
              </div>
            )}

            <div className="continuous-status-metrics">
              <div className="continuous-metric">
                <span className="continuous-metric-label">待同步变更</span>
                <strong className="continuous-metric-value">
                  {pendingSummary}
                </strong>
              </div>
              <div className="continuous-metric">
                <span className="continuous-metric-label">最近同步成功</span>
                <strong
                  className="continuous-metric-value"
                  title={
                    effectiveStatus.lastSuccess > 0
                      ? formatTimestamp(effectiveStatus.lastSuccess, true)
                      : undefined
                  }
                >
                  {effectiveStatus.lastSuccess > 0
                    ? formatRelativeSyncTime(effectiveStatus.lastSuccess)
                    : "尚未同步"}
                </strong>
              </div>
              <div className="continuous-metric">
                <span className="continuous-metric-label">远端检测周期</span>
                <strong
                  className="continuous-metric-value"
                  title={
                    effectiveStatus.lastPoll > 0
                      ? `最近检测：${formatTimestamp(effectiveStatus.lastPoll, true)}`
                      : undefined
                  }
                >
                  每 {formatSecondsShort(meta.pollIntervalSec)}
                </strong>
              </div>
              <div className="continuous-metric">
                <span className="continuous-metric-label">完整核对 / 静默</span>
                <strong className="continuous-metric-value">
                  {formatMinutesShort(meta.auditIntervalMin)} ·{" "}
                  {formatSecondsShort(meta.debounceSec)}
                </strong>
              </div>
            </div>
          </div>
        )}

        <div className="overview-sections">
          <section className="info-section">
            <h3>同步规则与配置</h3>
            <Info label="同步方向">
              <span className="overview-engine">
                <span>{meta.directionLabel}</span>
                {meta.directionKey !== "bidirectional" && (
                  <span className="muted">
                    {meta.syncDelete ? "同步删除目标端多余文件" : "不删除目标端文件"}
                  </span>
                )}
              </span>
            </Info>
            <Info label="监控节奏">{formatContinuousSyncPlan(meta)}</Info>
            {Boolean(job.windowEnable) && (
              <Info label="运行窗口">
                每日 {job.windowStart || "00:00"} – {job.windowEnd || "06:00"}
                {job.windowStopOutside ? "（超出窗口自动中止传输）" : "（窗口外暂挂，恢复后补齐）"}
              </Info>
            )}
            {(Number(job.compareMode || 0) !== 0 ||
              Number(job.conflictPolicy || 0) !== 0) && (
              <Info label="比对与冲突">
                {compareModeLabel(job.compareMode)} ·{" "}
                {conflictPolicyLabel(job.conflictPolicy)}
              </Info>
            )}
            {Boolean(job.trashEnable) && (
              <Info label={job.method === 3 ? "双端回收站" : "目标回收站"}>
                已开启（目录 {job.trashDir || ".opensync-trash"}
                {Number(job.trashRetentionDays ?? 30) > 0
                  ? `，保留 ${job.trashRetentionDays ?? 30} 天`
                  : "，永久保留"}
                ）
              </Info>
            )}
            {showPathMapping && (
              <Info label="目标映射">
                <div className="overview-path-mapping">
                  {pathPreview.items.map((item) => (
                    <div key={item.key}>
                      {formatDisplayPath(item.srcDir, displaySiblings)}{" "}
                      <IconArrowRight
                        size="extra-small"
                        aria-hidden="true"
                        style={{ verticalAlign: "middle", margin: "0 4px" }}
                      />{" "}
                      {formatDisplayPath(item.resolvedDst, displaySiblings)}
                    </div>
                  ))}
                </div>
              </Info>
            )}
            <Info label="过滤规则">
              {filterSummaries.length > 0 ? (
                <div className="overview-summary-tags">
                  {filterSummaries.map((text) => (
                    <Tag key={text} size="small" type="light" color="grey">
                      {text}
                    </Tag>
                  ))}
                </div>
              ) : (
                <span className="muted">未启用过滤</span>
              )}
            </Info>
            {throttleLabels.length > 0 && (
              <Info label="并发与调优">{throttleLabels.join(" · ")}</Info>
            )}
            {notifyBindingText && (
              <Info label="通知绑定">{notifyBindingText}</Info>
            )}
            <Info label="最近同步批次">
              <LatestExecution
                record={latestExecution.data?.dataList?.[0]}
                loading={latestExecution.loading}
                error={latestExecution.error}
              />
            </Info>
          </section>
        </div>
      </SectionCard>
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
  if (!record) return "暂无同步批次记录";

  const duration =
    record.runTime && record.createTime
      ? formatDuration(record.runTime - record.createTime)
      : "";

  return (
    <div className="latest-execution">
      <span>{formatTimestamp(record.createTime)}</span>
      {duration && <span>{duration}</span>}
      {record.allNum !== undefined && (
        <span className="muted">
          {record.successNum ?? 0}/{record.allNum} 项
          {(record.sumSize ?? 0) > 0 ? ` · ${formatSize(record.sumSize || 0)}` : ""}
        </span>
      )}
    </div>
  );
}
