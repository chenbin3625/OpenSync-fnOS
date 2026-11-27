import { lazy, Suspense, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import SideSheet from "@douyinfe/semi-ui/lib/es/sideSheet";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { IconExternalOpen } from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  confirmAction,
  confirmDelete,
  errorToast,
  LoadingPanel,
} from "./common";
import { BisyncConflictsModal } from "./BisyncConflictsModal";
import { CheckModal } from "./CheckModal";
import { DryRunModal } from "./DryRunModal";
import { resolveEffectiveSyncStatus } from "./SyncStatus";
import { TaskOverviewPanel } from "./TaskOverviewPanel";
import { useAction, useMediaQuery, useResource } from "../lib/hooks";
import {
  isLegacyOpenListJob,
  jobHasCryptRemote,
  requiresFirstMirrorPreview,
  type TaskTab,
} from "../lib/taskForm";
import { getJobName, parseJobPathList } from "../pages/Home/homeUtils";
import type { AlistItem, JobItem, NativeRemoteItem } from "../types";

const Realtime = lazy(() =>
  import("../pages/TaskExecution").then((module) => ({
    default: module.Realtime,
  })),
);
const History = lazy(() =>
  import("../pages/TaskExecution").then((module) => ({
    default: module.History,
  })),
);

interface TaskDetailDrawerProps {
  job: JobItem | null;
  initialTab?: TaskTab;
  engines: AlistItem[];
  remotes: NativeRemoteItem[];
  onClose: () => void;
  onEdit: (job: JobItem) => void;
  onClone: (job: JobItem) => void;
  onJobsChanged: () => Promise<void>;
}

export function TaskDetailDrawer({
  job,
  initialTab = "overview",
  engines,
  remotes,
  onClose,
  onEdit,
  onClone,
  onJobsChanged,
}: TaskDetailDrawerProps) {
  const isMobile = useMediaQuery("(max-width: 720px)");
  const [tab, setTab] = useState<TaskTab>(initialTab);
  const [historyRefreshKey, setHistoryRefreshKey] = useState(0);
  const [checkOpen, setCheckOpen] = useState(false);
  const [conflictsOpen, setConflictsOpen] = useState(false);
  const [mirrorPreviewOpen, setMirrorPreviewOpen] = useState(false);
  const actions = useAction();

  useEffect(() => {
    setTab(initialTab);
    setCheckOpen(false);
    setConflictsOpen(false);
    setMirrorPreviewOpen(false);
  }, [job?.id, initialTab]);

  const currentTask = useResource(
    (signal) =>
      job && !isLegacyOpenListJob(job, engines)
        ? api.current(job.id, signal)
        : Promise.resolve(null),
    [job?.id, engines.length],
  );

  useEffect(() => {
    if (!job || tab !== "overview") return;
    if (!job.running && !currentTask.data) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void currentTask.refresh(true);
      }
    }, 3000);
    return () => window.clearInterval(timer);
  }, [job?.id, job?.running, currentTask.data, currentTask.refresh, tab]);

  if (!job) return null;

  const currentForJob =
    currentTask.data?.taskId && !isLegacyOpenListJob(job, engines)
      ? currentTask.data
      : null;
  const legacy = isLegacyOpenListJob(job, engines);
  const hasCryptRemote = jobHasCryptRemote(job, engines, remotes);

  const runAction = (operation: () => Promise<unknown>, message: string) =>
    void actions.run(async () => {
      try {
        await operation();
        Toast.success(message);
        await onJobsChanged();
        await currentTask.refresh(true);
        setHistoryRefreshKey((k) => k + 1);
      } catch (err) {
        errorToast(err);
      }
    });

  const handleToggle = () => {
    if (requiresFirstMirrorPreview(job)) {
      setMirrorPreviewOpen(true);
      return;
    }
    const pausing = job.enable === 1;
    const label = job.isCron === 3 ? "持续同步" : "任务";
    const toggle = () => api.jobAction({ id: String(job.id), pause: pausing });
    if (pausing && (currentForJob || job.running)) {
      confirmAction({
        title: `暂停${label}并中止当前传输？`,
        content: "已完成的文件会保留。恢复同步后会检查并补齐暂停期间的变更。",
        okText: "暂停同步",
        onOk: () =>
          actions.run(async () => {
            await toggle();
            Toast.success(`${label}已暂停`);
            await onJobsChanged();
            await currentTask.refresh(true);
          }),
      });
      return;
    }
    runAction(toggle, pausing ? `${label}已暂停` : `${label}已恢复`);
  };

  return (
    <SideSheet
      visible
      placement="right"
      width={isMobile ? "100%" : 740}
      className="task-detail-drawer"
      title={
        <div className="task-drawer-title">
          <span className="task-drawer-title__name">{getJobName(job)}</span>
          <Link
            to={`/tasks?jobId=${job.id}&tab=${tab}`}
            className="task-drawer-fullpage-link"
          >
            <Button
              size="small"
              theme="borderless"
              icon={<IconExternalOpen aria-hidden="true" />}
              iconPosition="right"
            >
              完整页面打开
            </Button>
          </Link>
        </div>
      }
      onCancel={onClose}
    >
      <div className="task-drawer-body">
        <Tabs
          activeKey={tab}
          onChange={(key) => setTab(key as TaskTab)}
          keepDOM={false}
        >
          <Tabs.TabPane tab="任务概览" itemKey="overview" />
          <Tabs.TabPane tab="实时明细" itemKey="realtime" />
          <Tabs.TabPane tab="历史记录" itemKey="history" />
        </Tabs>

        <div className="task-drawer-content">
          {tab === "overview" && (
            <TaskOverviewPanel
              job={job}
              engines={engines}
              busy={actions.busy}
              currentTask={currentForJob}
              paused={Boolean(currentForJob?.paused ?? job.paused)}
              runKey={String(currentForJob?.taskId ?? "idle")}
              onRun={() =>
                runAction(() => api.jobAction({ id: String(job.id) }), "已触发立即同步")
              }
              onOpenRealtime={() => setTab("realtime")}
              onCheck={() => setCheckOpen(true)}
              onOpenConflicts={() => setConflictsOpen(true)}
              onClone={() => {
                onClose();
                onClone(job);
              }}
              onTogglePause={() => {
                if (!currentForJob) return;
                const resuming = Boolean(currentForJob.paused);
                runAction(
                  () =>
                    api.taskAction(
                      currentForJob.taskId,
                      resuming ? "resume" : "pause",
                    ),
                  resuming ? "已继续传输" : "已暂停传输",
                );
              }}
              onStop={() => {
                if (!currentForJob) return;
                const { taskId } = currentForJob;
                confirmAction({
                  title: "中止本次同步批次？",
                  content:
                    "仅中止当前正在进行的传输批次，已完成的文件不会撤销；持续同步仍保持开启。",
                  okText: "中止本次",
                  onOk: () =>
                    actions.run(async () => {
                      await api.taskAction(taskId, "stop");
                      Toast.success("已提交中止本次同步");
                      await onJobsChanged();
                      await currentTask.refresh(true);
                    }),
                });
              }}
              onToggle={handleToggle}
              onEdit={() => onEdit(job)}
              onDelete={() =>
                confirmDelete(
                  "删除此持续同步任务？",
                  () =>
                    actions.run(async () => {
                      await api.deleteJob(job.id);
                      onClose();
                      await onJobsChanged();
                    }),
                  "此任务的历史同步记录与同步状态将一并删除。",
                )
              }
            />
          )}

          {tab === "realtime" && (
            <Suspense fallback={<LoadingPanel />}>
              <Realtime
                key={job.id}
                jobId={job.id}
                syncStatus={resolveEffectiveSyncStatus(job)}
                onTaskEnded={() => {
                  void onJobsChanged();
                  setHistoryRefreshKey((k) => k + 1);
                }}
                onOpenOverview={() => setTab("overview")}
                onOpenHistory={() => setTab("history")}
                onSyncNow={
                  job.enable === 1 && !legacy
                    ? () =>
                        runAction(
                          () => api.jobAction({ id: String(job.id) }),
                          "已触发立即同步",
                        )
                    : undefined
                }
              />
            </Suspense>
          )}

          {tab === "history" && (
            <Suspense fallback={<LoadingPanel />}>
              <History
                key={job.id}
                jobId={job.id}
                currentTaskId={currentForJob?.taskId}
                refreshKey={historyRefreshKey}
                disableRetry={legacy || job.method === 3}
                onOpenRealtime={() => setTab("realtime")}
              />
            </Suspense>
          )}
        </div>
      </div>

      {mirrorPreviewOpen && (
        <DryRunModal
          jobId={job.id}
          jobName={getJobName(job)}
          onClose={() => setMirrorPreviewOpen(false)}
          onEnableMirror={async () => {
            await api.jobAction({ id: String(job.id), pause: false });
            setMirrorPreviewOpen(false);
            Toast.success("镜像已启用");
            await onJobsChanged();
          }}
        />
      )}

      {checkOpen && (
        <CheckModal
          jobId={job.id}
          jobName={getJobName(job)}
          hasCryptRemote={hasCryptRemote}
          onClose={() => setCheckOpen(false)}
          onRepairStarted={() => {
            void onJobsChanged();
          }}
        />
      )}

      {conflictsOpen && (
        <BisyncConflictsModal
          jobId={job.id}
          jobName={getJobName(job)}
          leftPath={parseJobPathList(job.srcPath)[0] ?? ""}
          rightPath={parseJobPathList(job.dstPath)[0] ?? ""}
          onClose={() => setConflictsOpen(false)}
          onResolved={() => {
            void onJobsChanged();
          }}
        />
      )}
    </SideSheet>
  );
}
