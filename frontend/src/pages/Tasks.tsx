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
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconAlertTriangle,
  IconChevronRightStroked,
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
  confirmStopTask,
  errorToast,
  type MenuAction,
} from "../components/common";
import { CheckModal } from "../components/CheckModal";
import { DryRunModal } from "../components/DryRunModal";
import {
  FullSyncApprovalModal,
  PendingApprovalBanner,
} from "../components/FullSyncApprovalModal";
import { useAction, useResource } from "../lib/hooks";
import { formatBwLimitSummary } from "../lib/bwLimit";
import {
  buildSyncPathPreview,
  formatDisplayPath,
  formatDisplayPathList,
} from "../lib/syncPathPreview";
import {
  buildTaskSearchParams,
  cloneJobToForm,
  isLegacyOpenListJob,
  parseTaskTab,
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
  formatNextRun,
  formatSize,
  formatSchedulePlan,
  getJobName,
  methodLabel,
  parseJobPathList,
} from "./Home/homeUtils";
import { formatTimestamp } from "../utils/date";
import { SyncStatusTag } from "../components/SyncStatus";
import type { AlistItem, JobItem, TaskRecord } from "../types";

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
  const explicitTab = parseTaskTab(rawTab);
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
    hasCurrentTask || (!currentTaskSettled && Boolean(selected?.running));
  const activeApproval = pendingApproval.data ?? null;
  const approvalExecuting = activeApproval?.status === "executing";
  const refreshCurrentTask = currentTask.refresh;
  const refreshPendingApproval = pendingApproval.refresh;
  const requestedApprovalOpen = params.get("approval") === "1";

  // Reset modal open state when switching jobs.
  useEffect(() => {
    setApprovalModalOpen(false);
  }, [selectedJobId]);

  // Honor ?approval=1 (from the global banner) once when approval data arrives,
  // then clean the query flag so closing the modal does not re-open it.
  useEffect(() => {
    if (!requestedApprovalOpen || !activeApproval) return;
    setApprovalModalOpen(true);
    const next = new URLSearchParams(params);
    next.delete("approval");
    setParams(next, { replace: true });
  }, [requestedApprovalOpen, activeApproval, params, setParams]);

  // Refresh pending approval whenever a running task finishes, and refresh
  // jobs/history whenever background deletion completes.
  const prevTaskIdRef = useRef<number | null>(null);
  useEffect(() => {
    const curId = currentTaskForSelected?.taskId ?? null;
    if (prevTaskIdRef.current !== null && curId === null) {
      void refreshPendingApproval(true);
      void jobs.refresh(true);
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
  useEffect(() => {
    if (explicitTab === "realtime" && tab !== "realtime") {
      const next = new URLSearchParams(params);
      next.set("tab", tab);
      setParams(next, { replace: true });
      return;
    }
    if (explicitTab === null && tab === "realtime" && hasCurrentTask) {
      const next = new URLSearchParams(params);
      next.set("tab", "realtime");
      setParams(next, { replace: true });
    }
  }, [explicitTab, tab, hasCurrentTask, params, setParams]);
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
    update({ tab: "overview" });
  }, [update, refreshPendingApproval]);
  const handleOpenRealtime = useCallback(
    () => update({ tab: "realtime" }),
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
  const triggerRunJob = (jobId: number, method: number) => {
    const startJob = async () => {
      await api.jobAction({ id: String(jobId) });
      Toast.success("已提交执行");
      update({ tab: null }, { replace: true });
      await refreshJobs();
    };
    if (method === 2) {
      confirmAction({
        title: "确认启动移动同步？",
        content:
          "移动模式会在文件复制到目标目录并校验后删除源目录中的对应文件，是否确认立即执行？",
        okText: "确认移动",
        onOk: () =>
          actions.run(async () => {
            try {
              await startJob();
            } catch (err) {
              errorToast(err);
              throw err;
            }
          }),
      });
      return;
    }
    void actions.run(async () => {
      try {
        await startJob();
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
                  <Tooltip content="当前没有运行中的任务" position="top">
                    <span>实时任务</span>
                  </Tooltip>
                ) : (
                  "实时任务"
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
                    running={!!currentTaskForSelected}
                    paused={Boolean(
                      currentTaskForSelected?.paused ?? selected.paused,
                    )}
                    runKey={String(currentTaskForSelected?.taskId ?? "idle")}
                    onRun={() => triggerRunJob(selected.id, selected.method)}
                    onDryRun={() => setDryRunOpen(true)}
                    onCheck={() => setCheckOpen(true)}
                    onResyncBisync={() => {
                      confirmAction({
                        title: "重建双向同步基线（--resync）？",
                        content:
                          "适用于首次运行异常中断或基线清单丢失后的恢复。确认后将立即执行一次带 --resync 的双向同步，以消除基线不一致问题。",
                        okText: "立即重建并同步",
                        onOk: () =>
                          actions.run(async () => {
                            try {
                              await api.resetBisyncJob(selected.id, true);
                              Toast.success("已触发双向基线重建同步");
                              update({ tab: null }, { replace: true });
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
                      confirmStopTask(() =>
                        actions.run(async () => {
                          await api.taskAction(taskId, "stop");
                          await refreshJobs();
                        }),
                      );
                    }}
                    onToggle={() => {
                      const pausing = selected.enable === 1;
                      if (pausing && currentTaskForSelected) {
                        confirmAction({
                          title: "停用任务并中止当前执行？",
                          content:
                            "关闭开关会同时中止正在进行的同步，已完成的文件不会撤销。",
                          okText: "停用并中止",
                          onOk: () =>
                            actions.run(async () => {
                              try {
                                await api.jobAction({
                                  id: String(selected.id),
                                  pause: true,
                                });
                                Toast.success("任务状态已更新");
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
                        "任务状态已更新",
                      );
                    }}
                    onEdit={() => setEditor({ open: true, job: selected })}
                    onDelete={() => {
                      const deletingId = selected.id;
                      confirmDelete(
                        "删除此同步任务？",
                        () =>
                          actions.run(async () => {
                            clearTimeout(followUp.current);
                            await api.deleteJob(deletingId);
                            deletedJobIdsRef.current.add(deletingId);
                            update({ jobId: null });
                            await refreshJobs({ skipCurrentTask: true });
                          }),
                        "此任务的历史执行记录将一并删除。",
                      );
                    }}
                  />
                )}
                {tab === "realtime" && (
                  <Suspense fallback={<LoadingPanel />}>
                    <Realtime
                      key={selected.id}
                      jobId={selected.id}
                      onTaskEnded={handleRealtimeEnded}
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
          onRunReal={() => triggerRunJob(selected.id, selected.method)}
        />
      )}
      {checkOpen && selected && (
        <CheckModal
          jobId={selected.id}
          jobName={getJobName(selected)}
          hasCryptRemote={hasCryptRemote}
          onClose={() => setCheckOpen(false)}
          onRepairStarted={() => {
            update({ tab: null }, { replace: true });
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
                  tab: result.runNow ? null : "overview",
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
                if (result.runNow && createdId) {
                  triggerRunJob(createdId, result.method);
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
  running,
  paused,
  runKey,
  onRun,
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
  running: boolean;
  paused: boolean;
  runKey: string;
  onRun: () => void;
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
    if (obj.watchEnable) list.push("实时监听");
    if (obj.directStream) list.push("直接流式同步");
    if (obj.missedCatchUp) list.push("漏跑补跑");
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
      ? /^(?:off|0)$/i.test(job.bwLimit.trim())
        ? "不限速（覆盖全局）"
        : `限速 ${formatBwLimitSummary(job.bwLimit, { allowInherit: true })}`
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
  const runActionLabel = running
    ? "停止任务"
    : !enabled
      ? "任务未启用，请先启用"
      : "启动任务";

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

  return (
    <div className="overview">
      <SectionCard
        className="overview-card"
        headClassName="overview-head"
        title={
          <span className="engine-title-row">
            <h2 title={getJobName(job)}>{getJobName(job)}</h2>
            {legacy && (
              <Tag size="small" type="solid" color="red">
                旧版已停用
              </Tag>
            )}
            {!legacy && running && paused && (
              <Tag size="small" type="light" color="orange">
                已暂停
              </Tag>
            )}
            {!legacy && job.queued && (
              <Tag size="small" type="light" color="cyan">
                排队等待中
              </Tag>
            )}
            {!legacy && <SyncStatusTag status={job.syncStatus} />}
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
                {job.isCron !== 2 && (
                  <Switch
                    aria-label="启用任务"
                    checked={enabled}
                    disabled={busy}
                    onChange={() => onToggle()}
                  />
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
                {running && (
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
                )}
                <Tooltip content={runActionLabel} position="top">
                  <Button
                    aria-label={runActionLabel}
                    icon={
                      running ? (
                        <IconStop aria-hidden="true" />
                      ) : (
                        <IconPlay aria-hidden="true" />
                      )
                    }
                    type={running ? "danger" : "tertiary"}
                    theme="borderless"
                    disabled={busy || (!running && !enabled)}
                    onClick={running ? onStop : onRun}
                  />
                </Tooltip>
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
            message="该任务关联旧版 OpenList 引擎，升级后已自动停用且无法运行或编辑。请删除此任务后，在「存储管理」接入对应存储并重新创建同步任务。"
          />
        )}
        <div className="overview-flow">
          <div className="flow-node">
            <div className="flow-name">
              <IconServerStroked aria-hidden="true" />
              源目录
            </div>
            <div className="flow-sub flow-paths">
              {formatDisplayPathList(srcList, displaySiblings).join("\n") || "—"}
            </div>
          </div>
          <IconChevronRightStroked className="flow-arrow" aria-hidden="true" />
          <div className="flow-node">
            <div className="flow-name" title={engineName}>
              <IconCloudStroked aria-hidden="true" />
              目标目录
            </div>
            <div className="flow-sub flow-paths">
              {formatDisplayPathList(dstList, displaySiblings).join("\n") || "—"}
            </div>
          </div>
          <div className="flow-node flow-next">
            <div className="flow-name">下次执行</div>
            <div className="flow-sub">
              {legacy ? "已停用（需删除重建）" : formatNextRun(job)}
            </div>
          </div>
        </div>
        <div className="overview-sections">
          <section className="info-section">
            <h3>规则与调度</h3>
            <Info label="执行引擎">
              <span className="overview-engine">
                <span>{legacy ? "旧版 OpenList" : engineName}</span>
                <span className="muted">
                  {legacy ? "已不再支持，请删除重建" : "内置 rclone"}
                </span>
              </span>
            </Info>
            <Info label="同步方式">{methodLabel(job.method)}</Info>
            <Info label="执行计划">{formatSchedulePlan(job)}</Info>
            {job.syncStatus && (
              <Info label="同步状态">
                <div className="sync-status-summary">
                  <span>
                    {job.syncStatus.pendingChanges < 0
                      ? "待完整核对"
                      : `待处理 ${job.syncStatus.pendingChanges} 个文件`}
                    {job.syncStatus.lastSuccess > 0
                      ? ` · 最近成功 ${formatTimestamp(job.syncStatus.lastSuccess)}`
                      : ""}
                  </span>
                  {job.syncStatus.error && (
                    <div className="inline-error" role="status">
                      {job.syncStatus.error}
                    </div>
                  )}
                </div>
              </Info>
            )}
            {Boolean(job.windowEnable) && (
              <Info label="运行窗口">
                每日 {job.windowStart || "00:00"} – {job.windowEnd || "06:00"}
                {job.windowStopOutside ? "（超出窗口自动停止）" : ""}
              </Info>
            )}
            {job.method === 2
              ? Number(job.compareMode || 0) !== 0 && (
                  <Info label="文件比对">{compareModeLabel(job.compareMode)}</Info>
                )
              : (Number(job.compareMode || 0) !== 0 ||
                  Number(job.conflictPolicy || 0) !== 0) && (
                  <Info label="比对与冲突">
                    {compareModeLabel(job.compareMode)} ·{" "}
                    {conflictPolicyLabel(job.conflictPolicy)}
                  </Info>
                )}
            {job.method !== 2 && Boolean(job.trashEnable) && (
              <Info label={job.method === 3 ? "双端回收站" : "目标回收站"}>
                已开启（目录 {job.trashDir || ".opensync-trash"}
                {Number(job.trashRetentionDays ?? 30) > 0
                  ? `，保留 ${job.trashRetentionDays ?? 30} 天`
                  : "，永久保留"}
                ）
              </Info>
            )}
            {job.method === 2 && Boolean(job.deleteEmptySrcDirs) && (
              <Info label="空目录清理">移动完成后自动清理空源子目录</Info>
            )}
            {showPathMapping && (
              <Info label="目标映射">
                <div className="overview-path-mapping">
                  {pathPreview.items.map((item) => (
                    <div key={item.key}>
                      {formatDisplayPath(item.srcDir, displaySiblings)} →{" "}
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
            <Info label="最近一次执行">
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
  if (!record) return "暂无执行记录";

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
