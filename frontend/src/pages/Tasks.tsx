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
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconArrowLeft,
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
import { TaskDetailDrawer } from "../components/TaskDetailDrawer";
import { TaskOverviewPanel } from "../components/TaskOverviewPanel";
import { SyncOverview } from "./SyncOverview";
import { jobStatusDotClass } from "../components/SyncStatus";
import { useAction, useResource } from "../lib/hooks";
import {
  buildTaskSearchParams,
  cloneJobToForm,
  getJobMountNames,
  isLegacyOpenListJob,
  jobHasCryptRemote,
  requiresFirstMirrorPreview,
  resolveJobSelection,
  resolveTaskTab,
  type JobForm,
  type TaskTab,
} from "../lib/taskForm";
import type { TaskEditorSavedResult } from "./TaskEditor";
import {
  getJobName,
  parseJobPathList,
} from "./Home/homeUtils";
import type {
  JobItem,
  NativeRemoteItem,
} from "../types";

const SyncRecords = lazy(() =>
  import("./TaskExecution").then((module) => ({ default: module.SyncRecords })),
);
const JobEditor = lazy(() => import("./TaskEditor"));
const StorageEditor = lazy(() =>
  import("../components/NativeStorageEditor").then(({ NativeStorageEditor }) => ({
    default: NativeStorageEditor,
  })),
);
const currentTaskPollMs = 3000;
const currentTaskRecordsPollMs = 3000;
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
  const dashboard = useResource(
    (signal) =>
      overview ? api.dashboard(signal).catch(() => null) : Promise.resolve(null),
    [overview],
    overview,
  );
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
  const [checkJob, setCheckJob] = useState<JobItem | null>(null);
  const [recordsRefreshKey, setRecordsRefreshKey] = useState(0);
  const [pendingCreatedId, setPendingCreatedId] = useState<number | null>(null);
  const mobileJobButtons = useRef<Array<HTMLButtonElement | null>>([]);
  const actions = useAction();
  const list = jobs.data?.dataList || [];
  const drawerJob = overview ? list.find((job) => job.id === selectedId) : undefined;
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
  const detailJob = drawerJob ?? selected;
  const selectedJobId = detailJob?.id;
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
  const tab = resolveTaskTab(rawTab);
  const recordsTab = tab === "records";
  const refreshCurrentTask = currentTask.refresh;

  const prevTaskIdRef = useRef<number | null>(null);
  useEffect(() => {
    const curId = currentTaskForSelected?.taskId ?? null;
    if (prevTaskIdRef.current !== null && curId === null) {
      void jobs.refresh(true);
      setRecordsRefreshKey((k) => k + 1);
      window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    }
    prevTaskIdRef.current = curId;
  }, [currentTaskForSelected?.taskId, jobs.refresh]);

  const prevRecordsTab = useRef(recordsTab);
  useEffect(() => {
    const enteredRecords = !prevRecordsTab.current && recordsTab;
    prevRecordsTab.current = recordsTab;
    if (
      enteredRecords &&
      selectedJobId &&
      !deletedJobIdsRef.current.has(selectedJobId)
    ) {
      void refreshCurrentTask(true);
    }
  }, [recordsTab, selectedJobId, refreshCurrentTask]);

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
      recordsTab ? currentTaskRecordsPollMs : currentTaskPollMs,
    );
    return () => {
      clearInterval(interval);
    };
  }, [selectedJobId, recordsTab, refreshCurrentTask, jobs.refresh]);

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
  const entryAction = overview ? params.get("action") : null;
  const entryStorage = params.get("storage");
  useEffect(() => {
    if (!drawerJob || entryStorage || storage.loading) return;
    const mounts = getJobMountNames(drawerJob);
    const remoteMounts = mounts.filter((mount) => mount !== "本地");
    const remote = remoteMounts.length === 1
      ? storage.data?.remotes.find((item) => item.name.toLowerCase() === remoteMounts[0].toLowerCase())
      : undefined;
    const scope = remote?.name ?? (mounts.length && !remoteMounts.length ? "__local__" : "__other__");
    update({ storage: scope }, { replace: true });
  }, [drawerJob, entryStorage, storage.loading, storage.data, update]);

  const workbenchTaskHref = (jobId: number, taskTab: TaskTab, intent?: "mirrorPreview" | "conflicts") => {
    const next = new URLSearchParams(params);
    next.set("jobId", String(jobId));
    next.set("storage", entryStorage ?? "all");
    next.set("tab", taskTab);
    next.delete("mirrorPreview");
    next.delete("conflicts");
    if (intent) next.set(intent, "1");
    return `/sync?${next}`;
  };
  const closeWorkbenchTask = () => update({
    jobId: null,
    tab: null,
    mirrorPreview: null,
    conflicts: null,
  }, { replace: true });
  const fullPageParams = new URLSearchParams(params);
  fullPageParams.delete("mirrorPreview");
  fullPageParams.delete("conflicts");

  useEffect(() => {
    if (!entryAction || engines.loading || storage.loading) return;
    if (entryAction === "new-task") {
      const initialRemote = storage.data?.remotes.find(
        (remote) => remote.name.toLowerCase() === entryStorage?.toLowerCase(),
      )?.name;
      setEditor({ open: true, job: null, initialRemote });
    } else if (entryAction === "add-connection") {
      setRepair({ mode: "form" });
    } else if (entryAction === "local-folders") {
      setRepair({ mode: "local" });
    }
    update({ action: null }, { replace: true });
  }, [entryAction, entryStorage, engines.loading, storage.loading, storage.data, update]);
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
  const handleTaskEnded = useCallback(() => {
    void jobs.refresh(true);
    setRecordsRefreshKey((k) => k + 1);
  }, [jobs.refresh]);
  const handleOpenRecords = useCallback(
    () => update({ tab: "records" }),
    [update],
  );
  const hasOverviewRunning = useMemo(
    () =>
      overview &&
      (list.some((j) => j.running && !j.paused) ||
        Boolean(dashboard.data?.kpi?.runningJobs)),
    [overview, list, dashboard.data?.kpi?.runningJobs],
  );
  useEffect(() => {
    if (!hasOverviewRunning) return;
    const interval = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void dashboard.refresh(true);
        void jobs.refresh(true);
      }
    }, 5000);
    return () => window.clearInterval(interval);
  }, [hasOverviewRunning, dashboard.refresh, jobs.refresh]);

  const refreshJobs = async (options?: { skipCurrentTask?: boolean }) => {
    await jobs.refresh(true);
    window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    if (overview) {
      await dashboard.refresh(true);
    }
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
        await refreshJobs({ skipCurrentTask: !detailJob });
      } catch (err) {
        errorToast(err);
      }
    });
  const triggerSyncNow = (jobId: number) =>
    action(() => api.jobAction({ id: String(jobId) }), "已触发立即同步");
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
            await refreshJobs({ skipCurrentTask: !detailJob });
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
  const handleToggleJob = (job: JobItem, running = Boolean(job.running)) => {
    if (requiresFirstMirrorPreview(job)) {
      if (overview) {
        navigate(workbenchTaskHref(job.id, "overview", "mirrorPreview"));
      } else {
        update({ mirrorPreview: 1 });
      }
      return;
    }
    toggleSync(job, running);
  };
  const handleCloneJob = (job: JobItem) =>
    setEditor({
      open: true,
      job: null,
      initialDraft: cloneJobToForm(job),
    });
  const handleTogglePauseTransfer = (taskId: number, paused: boolean) => {
    const resuming = Boolean(paused);
    action(
      () => api.taskAction(taskId, resuming ? "resume" : "pause"),
      resuming ? "已继续传输" : "已暂停传输",
    );
  };
  const handleStopJob = (job: JobItem, taskId?: number) => {
    confirmAction({
      title: "中止本次同步批次？",
      content:
        "仅中止当前正在进行的传输批次，已完成的文件不会撤销；持续同步仍保持开启。如需完全停止，请点击「暂停同步」。",
      okText: "中止本次",
      okLabel: "中止本次同步",
      onOk: () =>
        actions.run(async () => {
          try {
            if (taskId && taskId > 0) {
              await api.taskAction(taskId, "stop");
            } else {
              await api.batchJobAction("stop", [job.id]);
            }
            Toast.success("已提交中止本次同步");
            await refreshJobs({ skipCurrentTask: !detailJob });
          } catch (err) {
            errorToast(err);
            throw err;
          }
        }),
    });
  };
  const handleDeleteJob = (job: JobItem) => {
    const deletingId = job.id;
    confirmDelete(
      "删除此持续同步任务？",
      () =>
        actions.run(async () => {
          clearTimeout(followUp.current);
          await api.deleteJob(deletingId);
          deletedJobIdsRef.current.add(deletingId);
          if (drawerJob?.id === deletingId) {
            closeWorkbenchTask();
          } else if (!overview && selected?.id === deletingId) {
            update({ jobId: null });
          }
          await refreshJobs({ skipCurrentTask: true });
        }),
      "此任务的历史同步记录与同步状态将一并删除。",
    );
  };
  const mirrorPreview = Boolean(
    detailJob && detailJob.method === 1 && detailJob.isCron === 3 &&
    detailJob.enable !== 1 && params.get("mirrorPreview") === "1",
  );
  const conflictPreview = Boolean(detailJob?.method === 3 && params.get("conflicts") === "1");
  const closeMirrorPreview = () => update({ mirrorPreview: null }, { replace: true });
  const taskDetails = detailJob && (tab === "overview" ? (
    <TaskOverviewPanel
      job={detailJob}
      engines={engines.data || []}
      busy={actions.busy}
      currentTask={currentTaskForSelected}
      paused={Boolean(currentTaskForSelected?.paused ?? detailJob.paused)}
      runKey={String(currentTaskForSelected?.taskId ?? "idle")}
      onRun={() => triggerSyncNow(detailJob.id)}
      onOpenRecords={handleOpenRecords}
      onCheck={() => setCheckJob(detailJob)}
      onOpenConflicts={() => update({ conflicts: 1 })}
      onClone={() => {
        if (overview) closeWorkbenchTask();
        handleCloneJob(detailJob);
      }}
      onTogglePause={() => {
        if (!currentTaskForSelected) return;
        handleTogglePauseTransfer(currentTaskForSelected.taskId, Boolean(currentTaskForSelected.paused));
      }}
      onStop={() => handleStopJob(detailJob, currentTaskForSelected?.taskId)}
      onToggle={() => handleToggleJob(detailJob, Boolean(currentTaskForSelected || detailJob.running))}
      onEdit={() => {
        if (overview) closeWorkbenchTask();
        setEditor({ open: true, job: detailJob });
      }}
      onDelete={() => handleDeleteJob(detailJob)}
    />
  ) : (
    <Suspense fallback={<LoadingPanel />}>
      <SyncRecords
        key={detailJob.id}
        jobId={detailJob.id}
        refreshKey={recordsRefreshKey}
        disableRetry={isLegacyOpenListJob(detailJob, engines.data) || detailJob.method === 3}
        onTaskEnded={handleTaskEnded}
      />
    </Suspense>
  ));
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
              liveJobs={dashboard.data?.jobs}
              remotes={storage.data?.remotes || []}
              localRoots={storage.data?.localRoots || []}
              engines={engines.data || []}
              busy={actions.busy}
              onAdd={(initialRemote) => setEditor({ open: true, job: null, initialRemote })}
              taskHref={workbenchTaskHref}
              onEdit={(job) => setEditor({ open: true, job })}
              onClone={handleCloneJob}
              onToggle={handleToggleJob}
              onRun={(job) => triggerSyncNow(job.id)}
              onStop={handleStopJob}
              onTogglePauseTransfer={handleTogglePauseTransfer}
              onDelete={handleDeleteJob}
              onCheck={(job) => setCheckJob(job)}
              onRepair={(remote) => setRepair({ mode: "form", remote })}
              onAddConnection={() => setRepair({ mode: "form", remote: null })}
              onBrowseRemote={(remote) => setRepair({ mode: "browse", remote })}
              onLocalFolders={() => setRepair({ mode: "local" })}
              onNavigateEngines={() => navigate("/engines")}
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
            <Tabs.TabPane
              tab={
                <span className="realtime-tab-label">
                  {hasCurrentTask && (
                    <span
                      className="kpi-dot kpi-dot--primary kpi-dot--pulse"
                      aria-hidden="true"
                    />
                  )}
                  <span>同步记录</span>
                </span>
              }
              itemKey="records"
            />
          </Tabs>
        }
        actions={
          <Button
            theme="borderless"
            icon={<IconArrowLeft aria-hidden="true" />}
            aria-label="返回任务列表"
                onClick={() => {
                  const backParams = new URLSearchParams(params);
                  for (const key of ["jobId", "tab", "mirrorPreview", "conflicts"]) backParams.delete(key);
                  navigate({ pathname: "/sync", search: backParams.toString() });
                }}
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
              <span className={jobStatusDotClass(job)} aria-hidden="true" />
              <span>{getJobName(job)}</span>
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
              <div className="task-tab-content">{taskDetails}</div>
            ) : jobs.loading || jobs.error || pendingCreatedId !== null ? null : (
              <div className="task-empty-detail">
                <EmptyState />
              </div>
            )}
          </section>
        )}
      </div>
      </>}
      {drawerJob && (
        <TaskDetailDrawer
          key={drawerJob.id}
          title={getJobName(drawerJob)}
          tab={tab}
          onTabChange={(taskTab) => update({ tab: taskTab }, { replace: true })}
          fullPageHref={`/tasks?${fullPageParams}`}
          onClose={closeWorkbenchTask}
        >
          {taskDetails}
        </TaskDetailDrawer>
      )}
      {mirrorPreview && detailJob && (
        <DryRunModal
          jobId={detailJob.id}
          jobName={getJobName(detailJob)}
          onClose={closeMirrorPreview}
          onEnableMirror={async () => {
            await api.jobAction({ id: String(detailJob.id), pause: false });
            closeMirrorPreview();
            Toast.success("镜像已启用");
            await refreshJobs();
          }}
        />
      )}
      {checkJob && (
        <CheckModal
          jobId={checkJob.id}
          jobName={getJobName(checkJob)}
          hasCryptRemote={jobHasCryptRemote(checkJob, engines.data, storage.data?.remotes)}
          onClose={() => setCheckJob(null)}
          onRepairStarted={() => {
            void refreshJobs({ skipCurrentTask: !detailJob });
          }}
        />
      )}
      {conflictPreview && detailJob && (
        <BisyncConflictsModal
          jobId={detailJob.id}
          jobName={getJobName(detailJob)}
          leftPath={parseJobPathList(detailJob.srcPath)[0] ?? ""}
          rightPath={parseJobPathList(detailJob.dstPath)[0] ?? ""}
          onClose={() => update({ conflicts: null }, { replace: true })}
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
            onSaved={(savedName) => {
              if (overview && repair.mode === "form" && savedName) {
                if (!repair.remote) {
                  update({ storage: savedName, view: null, status: null, direction: null });
                } else if (repair.remote.name.toLowerCase() === entryStorage?.toLowerCase()) {
                  update({ storage: savedName }, { replace: true });
                }
              }
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
              if (overview && (createdId || mirrorPreviewId)) {
                const targetId = createdId ?? mirrorPreviewId;
                window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
                if (targetId) navigate(workbenchTaskHref(targetId, "overview", mirrorPreviewId ? "mirrorPreview" : undefined));
                void jobs.refresh();
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
                    await refreshJobs({ skipCurrentTask: overview });
                  }
                } catch {
                  await refreshJobs({
                    skipCurrentTask: Boolean(createdId) || overview,
                  });
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
