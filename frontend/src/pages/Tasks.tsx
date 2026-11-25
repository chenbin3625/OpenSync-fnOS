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
  IconChevronRightStroked,
  IconCloudStroked,
  IconCopy,
  IconDeleteStroked,
  IconEditStroked,
  IconEyeOpenedStroked,
  IconPause,
  IconPlay,
  IconPlusStroked,
  IconServerStroked,
  IconSettingStroked,
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
import { buildSyncPathPreview } from "../lib/syncPathPreview";
import {
  cloneJobToForm,
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
  const requestedTab = ["overview", "realtime", "history"].includes(
    params.get("tab") || "",
  )
    ? params.get("tab")!
    : "overview";
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
  const hasCurrentTask = !!currentTask.data;
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
    const curId = currentTask.data?.taskId ?? null;
    if (prevTaskIdRef.current !== null && curId === null) {
      void refreshPendingApproval(true);
      void jobs.refresh(true);
      window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    }
    prevTaskIdRef.current = curId;
  }, [currentTask.data?.taskId, refreshPendingApproval, jobs.refresh]);

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

  const realtimeTab = requestedTab === "realtime";
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
  ]);
  const currentTaskSettled =
    !!selectedJobId &&
    snapshotState?.jobId === selectedJobId &&
    snapshotState.ok &&
    !currentTask.loading &&
    !currentTask.error;
  const tab = resolveTaskTab(requestedTab, {
    settled: currentTaskSettled,
    hasCurrentTask,
  });
  useEffect(() => {
    if (tab === requestedTab) return;
    const next = new URLSearchParams(params);
    next.set("tab", tab);
    setParams(next, { replace: true });
  }, [tab, requestedTab, params, setParams]);
  const followUp = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(followUp.current), []);
  const update = useCallback(
    (values: Record<string, string | number | null>) => {
      const next = new URLSearchParams(params);
      for (const [key, value] of Object.entries(values))
        value === null ? next.delete(key) : next.set(key, String(value));
      setParams(next);
    },
    [params, setParams],
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
    if (method === 2) {
      confirmAction({
        title: "确认启动移动同步？",
        content:
          "移动模式会在文件复制到目标目录并校验后删除源目录中的对应文件，是否确认立即执行？",
        okText: "确认移动",
        onOk: () =>
          actions.run(async () => {
            try {
              await api.jobAction({ id: String(jobId) });
              Toast.success("已提交执行");
              await refreshJobs();
            } catch (err) {
              errorToast(err);
              throw err;
            }
          }),
      });
      return;
    }
    action(() => api.jobAction({ id: String(jobId) }), "已提交执行");
  };
  const emptyAction =
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
        onClick={() => setEditor({ open: true, job: null })}
      >
        新建同步任务
      </Button>
    ) : undefined;
  const approvePendingDelete = () =>
    actions.run(async () => {
      if (!activeApproval?.taskId) return;
      try {
        await api.taskAction(activeApproval.taskId, "approve");
        setApprovalModalOpen(false);
        Toast.success("已提交后台校验并执行删除");
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
      {engines.error && !engines.loading && (
        <InlineNotice
          className="engine-load-error"
          role="alert"
          message={`存储引擎加载失败：${engines.error}`}
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
              update({ jobId: nextJob.id });
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
                    running={!!currentTask.data}
                    runKey={String(currentTask.data?.taskId ?? "idle")}
                    onRun={() => triggerRunJob(selected.id, selected.method)}
                    onDryRun={() => setDryRunOpen(true)}
                    onCheck={() => setCheckOpen(true)}
                    onClone={() =>
                      setEditor({
                        open: true,
                        job: null,
                        initialDraft: cloneJobToForm(selected),
                      })
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
                    onToggle={() => {
                      const pausing = selected.enable === 1;
                      if (pausing && currentTask.data) {
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
                      currentTaskId={currentTask.data?.taskId}
                      refreshKey={historyRefreshKey}
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
          onClose={() => setCheckOpen(false)}
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
                update({ jobId: createdId, tab: "overview" });
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
  runKey,
  onRun,
  onDryRun,
  onCheck,
  onClone,
  onStop,
  onToggle,
  onEdit,
  onDelete,
}: {
  job: JobItem;
  engines: AlistItem[];
  busy: boolean;
  running: boolean;
  runKey: string;
  onRun: () => void;
  onDryRun: () => void;
  onCheck: () => void;
  onClone: () => void;
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
  const engine = engines.find((e) => e.id === job.alistId);
  const engineName = engine
    ? engine.remark || engine.userName
    : `引擎 #${job.alistId}`;
  const isNativeEngine =
    engine?.kind === "rclone" || engine?.url === "rclone://native";
  const engineHost = engine
    ? isNativeEngine
      ? "rclone"
      : engineHostOf(engine.url)
    : "";
  const enabled = job.enable === 1;
  const filterSummaries = useMemo(() => summarizeJobFilters(job), [job]);
  const cacheLabels = [
    job.useCacheS ? "源端缓存" : "",
    job.useCacheT ? "目标缓存" : "",
  ].filter(Boolean);
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
    return list;
  }, [job.extraFlags]);
  const throttleLabels = [
    Number(job.scanIntervalS || 0) > 0 ? `源扫描间隔 ${job.scanIntervalS}s` : "",
    Number(job.scanIntervalT || 0) > 0 ? `目标扫描间隔 ${job.scanIntervalT}s` : "",
    Number(job.copyConcurrency || 0) > 0 ? `操作并发 ${job.copyConcurrency}` : "",
    Number(job.scanConcurrency || 0) > 0 ? `扫描并发 ${job.scanConcurrency}` : "",
    job.maxRetries !== undefined && Number(job.maxRetries) >= 0
      ? `重试 ${job.maxRetries} 次`
      : "",
    job.bwLimit?.trim() ? `限速 ${job.bwLimit.trim()}` : "",
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
  const pathPreview = useMemo(
    () =>
      buildSyncPathPreview({
        srcPaths: parseJobPathList(job.srcPath),
        dstPaths: parseJobPathList(job.dstPath),
        storedDstMap: job.dstMap,
        method: job.method,
      }),
    [job.srcPath, job.dstPath, job.dstMap, job.method],
  );
  const showPathMapping =
    pathPreview.items.length > 1 ||
    pathPreview.items.some((item) => Boolean(item.appendedSuffix));
  const menu: MenuAction[] = [
    {
      label: "模拟运行预览",
      icon: <IconEyeOpenedStroked aria-hidden="true" />,
      onClick: onDryRun,
    },
    ...(isNativeEngine
      ? [
          {
            label: "数据一致性校验",
            icon: <IconTickCircle aria-hidden="true" />,
            onClick: onCheck,
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
            {job.queued && (
              <Tag size="small" type="light" color="cyan">
                同引擎排队等待中
              </Tag>
            )}
          </span>
        }
        extra={
          <div className="overview-head-actions">
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
            {isNativeEngine && (
              <IconButton
                label="数据一致性校验（rclone check）"
                icon={<IconTickCircle aria-hidden="true" />}
                disabled={busy}
                onClick={onCheck}
              />
            )}
            <Tooltip content={runActionLabel} position="top">
              <Button
                aria-label={runActionLabel}
                icon={
                  running ? (
                    <IconPause aria-hidden="true" />
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
          </div>
        }
      >
        <div className="overview-flow">
          <div className="flow-node">
            <div className="flow-name">
              <IconServerStroked aria-hidden="true" />
              源目录
            </div>
            <div className="flow-sub flow-paths">
              {parseJobPathList(job.srcPath).join("\n") || "—"}
            </div>
          </div>
          <IconChevronRightStroked className="flow-arrow" aria-hidden="true" />
          <div className="flow-node">
            <div className="flow-name" title={engineHost || engineName}>
              <IconCloudStroked aria-hidden="true" />
              目标目录
            </div>
            <div className="flow-sub flow-paths">
              {parseJobPathList(job.dstPath).join("\n") || "—"}
            </div>
          </div>
          <div className="flow-node flow-next">
            <div className="flow-name">下次执行</div>
            <div className="flow-sub">{formatNextRun(job)}</div>
          </div>
        </div>
        <div className="overview-sections">
          <section className="info-section">
            <h3>规则与调度</h3>
            <Info label="存储引擎">
              <span className="overview-engine">
                <span>{engineName}</span>
                {engineHost && <span className="muted">{engineHost}</span>}
              </span>
            </Info>
            <Info label="同步方式">{methodLabel(job.method)}</Info>
            <Info label="执行计划">{formatSchedulePlan(job)}</Info>
            {Boolean(job.windowEnable) && (
              <Info label="运行窗口">
                每日 {job.windowStart || "00:00"} – {job.windowEnd || "06:00"}
                {job.windowStopOutside ? "（超出窗口自动停止）" : ""}
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
              <Info label="目标回收站">
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
                      {item.srcDir} → {item.resolvedDst}
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
            {cacheLabels.length > 0 && (
              <Info label="目录缓存">{cacheLabels.join("、")}</Info>
            )}
            {throttleLabels.length > 0 && (
              <Info label="并发与限流">{throttleLabels.join(" · ")}</Info>
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

/** 引擎卡片只展示主机名，完整地址留给悬浮提示。 */
const engineHostOf = (url: string) => {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
};
