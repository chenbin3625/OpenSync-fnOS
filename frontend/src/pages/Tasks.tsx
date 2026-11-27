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
  IconArrowLeft,
  IconDeleteStroked,
  IconFolderStroked,
  IconPlusStroked,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  EmptyState,
  Header,
  InlineNotice,
  LoadingPanel,
  LoadState,
  confirmAction,
  confirmDelete,
  errorToast,
} from "../components/common";
import { BisyncConflictsModal } from "../components/BisyncConflictsModal";
import { CheckModal } from "../components/CheckModal";
import { DryRunModal } from "../components/DryRunModal";
import { TaskOverviewPanel } from "../components/TaskOverviewPanel";
import { SyncOverview } from "./SyncOverview";
import {
  SyncStatusTag,
  resolveEffectiveSyncStatus,
} from "../components/SyncStatus";
import { useAction, useResource } from "../lib/hooks";
import {
  buildTaskSearchParams,
  cloneJobToForm,
  isLegacyOpenListJob,
  jobHasCryptRemote,
  requiresFirstMirrorPreview,
  resolveJobSelection,
  resolveTaskTab,
  type JobForm,
} from "../lib/taskForm";
import type { TaskEditorSavedResult } from "./TaskEditor";
import {
  getJobName,
  parseJobPathList,
} from "./Home/homeUtils";
import type {
  CurrentTaskData,
  JobItem,
  NativeRemoteItem,
} from "../types";

const Realtime = lazy(() =>
  import("./TaskExecution").then((module) => ({ default: module.Realtime })),
);
const History = lazy(() =>
  import("./TaskExecution").then((module) => ({ default: module.History })),
);
const JobEditor = lazy(() => import("./TaskEditor"));
const StorageEditor = lazy(() =>
  import("../components/NativeStorageEditor").then(({ NativeStorageEditor }) => ({
    default: NativeStorageEditor,
  })),
);
const currentTaskPollMs = 3000;
const currentTaskRealtimePollMs = 3000;
const currentTaskFollowUpMs = 1000;

export default function Tasks({ overview = false }: { overview?: boolean }) {
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
  const jobs = useResource((signal) => api.jobMenu(signal), [], overview);
  const engines = useResource((signal) => api.engines(signal));
  const storage = useResource(
    (signal) => overview ? api.nativeStorageState(signal) : Promise.resolve(null),
    [overview],
  );
  const [repair, setRepair] = useState<
    | { mode: "local" | "form" | "browse"; remote?: NativeRemoteItem | null }
    | null
  >(null);
  const [editor, setEditor] = useState<{
    open: boolean;
    job: JobItem | null;
    initialDraft?: JobForm;
    initialRemote?: string;
  }>({
    open: false,
    job: null,
  });
  const [checkOpen, setCheckOpen] = useState(false);
  const [conflictsOpen, setConflictsOpen] = useState(false);
  const [historyRefreshKey, setHistoryRefreshKey] = useState(0);
  const [pendingCreatedId, setPendingCreatedId] = useState<number | null>(null);
  const mobileJobButtons = useRef<Array<HTMLButtonElement | null>>([]);
  const actions = useAction();
  const list = jobs.data?.dataList || [];
  const { job: selected, replaceWith } = resolveJobSelection(overview ? [] : list, selectedId, {
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
  const hasCryptRemote = useMemo(
    () => Boolean(selected && jobHasCryptRemote(selected, engines.data)),
    [selected, engines.data],
  );
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
          setSnapshotState((prev) =>
            prev && prev.jobId === selectedJobId && prev.ok
              ? prev
              : { jobId: selectedJobId, ok: true },
          );
        }
        return result;
      } catch (err) {
        if (!signal.aborted) {
          setSnapshotState((prev) =>
            prev && prev.jobId === selectedJobId && !prev.ok
              ? prev
              : { jobId: selectedJobId, ok: false },
          );
        }
        throw err;
      }
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
  const refreshCurrentTask = currentTask.refresh;

  const prevTaskIdRef = useRef<number | null>(null);
  useEffect(() => {
    const curId = currentTaskForSelected?.taskId ?? null;
    if (prevTaskIdRef.current !== null && curId === null) {
      void jobs.refresh(true);
      setHistoryRefreshKey((k) => k + 1);
      window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    }
    prevTaskIdRef.current = curId;
  }, [currentTaskForSelected?.taskId, jobs.refresh]);

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
          void jobs.refresh(true);
        }
      },
      realtimeTab ? currentTaskRealtimePollMs : currentTaskPollMs,
    );
    return () => {
      clearInterval(interval);
    };
  }, [selectedJobId, realtimeTab, refreshCurrentTask, jobs.refresh]);

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
    void jobs.refresh(true);
    setHistoryRefreshKey((k) => k + 1);
  }, [jobs.refresh]);
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
    await currentTask.refresh(true);
    clearTimeout(followUp.current);
    followUp.current = setTimeout(() => {
      void refreshCurrentTask(true);
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
  const toggleSync = (job: JobItem, running = Boolean(job.running)) => {
    const pausing = job.enable === 1;
    const label = job.isCron === 3 ? "持续同步" : "任务";
    const toggle = () => api.jobAction({ id: String(job.id), pause: pausing });
    if (pausing && running) {
      confirmAction({
        title: `暂停${label}并中止当前传输？`,
        content: "已完成的文件会保留。恢复同步后会检查并补齐暂停期间的变更。",
        okText: "暂停同步",
        onOk: () => actions.run(async () => {
          try {
            await toggle();
            Toast.success(`${label}已暂停`);
            await refreshJobs();
          } catch (err) {
            errorToast(err);
            throw err;
          }
        }),
      });
      return;
    }
    action(toggle, pausing ? `${label}已暂停` : `${label}已恢复`);
  };
  const mirrorPreview = Boolean(
    selected && selected.method === 1 && selected.isCron === 3 &&
    selected.enable !== 1 && params.get("mirrorPreview") === "1",
  );
  const conflictPreview = Boolean(selected?.method === 3 && params.get("conflicts") === "1");
  const closeMirrorPreview = () => update({ mirrorPreview: null }, { replace: true });
  return (
    <div className="page task-page">
      {overview ? (
        <>
          <Header
            title="同步任务"
            actions={
              <Button
                type="primary"
                theme="solid"
                icon={<IconPlusStroked aria-hidden="true" />}
                onClick={() => setEditor({ open: true, job: null })}
              >
                新建同步任务
              </Button>
            }
          />
          {storage.error && (
            <InlineNotice message={storage.error} role="alert" onRetry={storage.refresh} />
          )}
          {jobs.error && (
            <InlineNotice message={jobs.error} role="alert" onRetry={jobs.refresh} />
          )}
          {(jobs.loading && !jobs.data) || (storage.loading && !storage.data) ? (
            <LoadingPanel />
          ) : (jobs.data || storage.data) && (
            <SyncOverview
              jobs={list}
              remotes={storage.data?.remotes || []}
              localRoots={storage.data?.localRoots || []}
              engines={engines.data || []}
              onAdd={(initialRemote) => setEditor({ open: true, job: null, initialRemote })}
              onRepair={(remote) => setRepair({ mode: "form", remote })}
              onAddConnection={() => setRepair({ mode: "form", remote: null })}
              onBrowseRemote={(remote) => setRepair({ mode: "browse", remote })}
              onLocalFolders={() => setRepair({ mode: "local" })}
              onEditJob={(job) => setEditor({ open: true, job })}
              onCloneJob={(job) =>
                setEditor({ open: true, job: null, initialDraft: cloneJobToForm(job) })
              }
              onJobsChanged={() => refreshJobs({ skipCurrentTask: true })}
              onStorageChanged={() => {
                void storage.refresh(true);
                void engines.refresh(true);
              }}
            />
          )}
        </>
      ) : <>
      <Header
        title="任务详情"
        tabs={
          <Tabs
            activeKey={tab}
            onChange={(key) => update({ tab: key })}
            keepDOM={false}
          >
            <Tabs.TabPane tab="任务详情" itemKey="overview" />
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
          <Button
            theme="borderless"
            icon={<IconArrowLeft aria-hidden="true" />}
            aria-label="返回任务列表"
            onClick={() => navigate("/sync")}
          />
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
          <EmptyState />
        ) : (
          <section className="task-detail-pane flex-column">
            {jobs.data && jobs.refreshError && (
              <InlineNotice
                className="engine-load-error"
                role="alert"
                message={`任务列表刷新失败：${jobs.refreshError}`}
                onRetry={() => void jobs.refresh(true)}
              />
            )}
            <LoadState
              loading={jobs.loading && !jobs.data}
              error={jobs.data ? "" : jobs.error}
              retry={jobs.refresh}
              empty={!list.length}
            />
            {selected && (jobs.data || !jobs.error) ? (
              <div className="task-tab-content">
                {tab === "overview" && (
                  <TaskOverviewPanel
                    job={selected}
                    engines={engines.data || []}
                    busy={actions.busy}
                    currentTask={currentTaskForSelected}
                    paused={Boolean(
                      currentTaskForSelected?.paused ?? selected.paused,
                    )}
                    runKey={String(currentTaskForSelected?.taskId ?? "idle")}
                    onRun={() => triggerSyncNow(selected.id)}
                    onOpenRealtime={handleOpenRealtime}
                    onCheck={() => setCheckOpen(true)}
                    onOpenConflicts={() => setConflictsOpen(true)}
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
                      if (requiresFirstMirrorPreview(selected)) {
                        update({ mirrorPreview: 1 });
                        return;
                      }
                      toggleSync(selected, Boolean(currentTaskForSelected || selected.running));
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
      </>}
      {mirrorPreview && selected && (
        <DryRunModal
          jobId={selected.id}
          jobName={getJobName(selected)}
          onClose={closeMirrorPreview}
          onEnableMirror={async () => {
            await api.jobAction({ id: String(selected.id), pause: false });
            closeMirrorPreview();
            Toast.success("镜像已启用");
            await refreshJobs();
          }}
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
      {(conflictsOpen || conflictPreview) && selected && (
        <BisyncConflictsModal
          jobId={selected.id}
          jobName={getJobName(selected)}
          leftPath={parseJobPathList(selected.srcPath)[0] ?? ""}
          rightPath={parseJobPathList(selected.dstPath)[0] ?? ""}
          onClose={() => {
            setConflictsOpen(false);
            if (conflictPreview) update({ conflicts: null }, { replace: true });
          }}
          onResolved={() => {
            void refreshJobs();
          }}
        />
      )}
      {repair && (
        <Suspense fallback={<LoadingPanel />}>
          <StorageEditor
            engine={engines.data?.find((e) => e.kind === "rclone") || engines.data?.[0]}
            mode={repair.mode}
            editRemote={repair.remote ?? null}
            onClose={() => setRepair(null)}
            onChanged={() => {
              void storage.refresh(true);
              void engines.refresh(true);
            }}
            onSaved={() => {
              setRepair(null);
              void storage.refresh(true);
              void refreshJobs({ skipCurrentTask: true });
            }}
          />
        </Suspense>
      )}
      {editor.open && (
        <Suspense fallback={<LoadingPanel />}>
          <JobEditor
            job={editor.job}
            initialDraft={editor.initialDraft}
            initialRemote={editor.initialRemote}
            engines={engines.data || []}
            existingJobs={list}
            enginesError={engines.error}
            onRetryEngines={() => void engines.refresh()}
            onStorageChanged={() => {
              void engines.refresh(true);
              if (overview) void storage.refresh(true);
            }}
            onNavigateEngines={() => {
              setEditor({ open: false, job: null });
              navigate("/engines");
            }}
            onClose={() => setEditor({ open: false, job: null })}
            onSaved={(result: TaskEditorSavedResult) => {
              setEditor({ open: false, job: null });
              const createdId = result.createdId;
              const mirrorPreviewId = result.mirrorPreviewId;
              if (overview && createdId) {
                window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
                navigate(`/tasks?jobId=${createdId}&tab=overview${mirrorPreviewId ? "&mirrorPreview=1" : ""}`);
                return;
              }
              if (createdId) {
                setPendingCreatedId(createdId);
                update({
                  jobId: createdId,
                  tab: "overview",
                  mirrorPreview: mirrorPreviewId ? 1 : null,
                });
              } else if (mirrorPreviewId) {
                update({ mirrorPreview: 1 });
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
