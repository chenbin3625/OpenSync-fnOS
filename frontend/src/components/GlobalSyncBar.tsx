import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { api } from "../api/client";
import { confirmAction, errorToast } from "./common";
import { isSyncStatusError, resolveEffectiveSyncStatus } from "./SyncStatus";
import { formatSpeed } from "../lib/dashboardMetrics";
import { useResource } from "../lib/hooks";
import type { JobItem } from "../types";

interface GlobalSyncBarProps {
  jobs: JobItem[];
  variant?: "sidebar" | "mobile";
}

export function GlobalSyncBar({
  jobs,
  variant = "sidebar",
}: GlobalSyncBarProps) {
  const [toggling, setToggling] = useState(false);
  const activeJobs = useMemo(
    () => jobs.filter((job) => !job.legacyOpenList),
    [jobs],
  );
  const hasJobs = jobs.length > 0;
  const hasActiveCandidates = activeJobs.length > 0;

  const dashboard = useResource(
    (signal) =>
      hasActiveCandidates ? api.dashboard(signal) : Promise.resolve(null),
    [hasActiveCandidates],
  );

  const summary = useMemo(() => {
    let runningCount = 0;
    let monitoringCount = 0;
    let errorCount = 0;
    let conflictCount = 0;
    let watchDegradedCount = 0;
    let hasPausable = false;
    let hasResumable = false;

    for (const job of activeJobs) {
      const syncStatus = resolveEffectiveSyncStatus(job);
      if (job.running && !job.paused) {
        runningCount += 1;
      } else if (
        job.enable === 1 &&
        (job.isCron === 3 ||
          syncStatus.state === "monitoring" ||
          syncStatus.state === "idle" ||
          syncStatus.state === "pending" ||
          syncStatus.state === "scanning" ||
          syncStatus.state === "syncing" ||
          syncStatus.state === "waiting_window")
      ) {
        monitoringCount += 1;
      }

      if (
        isSyncStatusError(syncStatus) ||
        (job.syncStatus?.failures ?? 0) > 0
      ) {
        errorCount += 1;
      }
      conflictCount += job.syncStatus?.conflictCount ?? 0;
      if (job.syncStatus?.watchDegraded) {
        watchDegradedCount += 1;
      }

      if ((job.isCron !== 2 && job.enable === 1) || job.running) {
        hasPausable = true;
      } else if (job.isCron !== 2 && job.enable !== 1) {
        hasResumable = true;
      }
    }

    const totalSpeed = dashboard.data?.realtime?.totalSpeed ?? 0;
    const activeFromDashboard = dashboard.data?.realtime?.activeJobs;
    const effectiveRunning =
      typeof activeFromDashboard === "number" && activeFromDashboard > runningCount
        ? activeFromDashboard
        : runningCount;

    let dotTone: "running" | "monitoring" | "paused" | "idle" = "idle";
    let statusLabel = "待命空闲";
    if (effectiveRunning > 0 || totalSpeed > 0) {
      dotTone = "running";
      statusLabel = totalSpeed > 0 ? formatSpeed(totalSpeed) : "正在同步";
    } else if (monitoringCount > 0) {
      dotTone = "monitoring";
      statusLabel = "守护运行中";
    } else if (!hasPausable && hasResumable) {
      dotTone = "paused";
      statusLabel = "全局已暂停";
    }

    return {
      runningCount: effectiveRunning,
      monitoringCount,
      errorCount,
      conflictCount,
      watchDegradedCount,
      hasPausable,
      hasResumable,
      totalSpeed,
      dotTone,
      statusLabel,
    };
  }, [activeJobs, dashboard.data]);

  const shouldPoll =
    hasActiveCandidates &&
    (summary.runningCount > 0 || summary.monitoringCount > 0);

  useEffect(() => {
    if (!hasActiveCandidates) return;
    const refresh = () => void dashboard.refresh(true);
    window.addEventListener("opensync:jobs-changed", refresh);
    return () => window.removeEventListener("opensync:jobs-changed", refresh);
  }, [hasActiveCandidates, dashboard.refresh]);

  useEffect(() => {
    if (!shouldPoll) return;
    const interval = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void dashboard.refresh(true);
      }
    }, 8_000);
    return () => window.clearInterval(interval);
  }, [shouldPoll, dashboard.refresh]);

  if (!hasJobs || !hasActiveCandidates) return null;

  const handleToggleAll = () => {
    if (toggling) return;
    if (summary.hasPausable) {
      confirmAction({
        title: "全部暂停同步？",
        content:
          "将暂停所有自动守护与定时同步任务，并中止正在运行的传输；手动任务需单独再次触发。",
        okText: "全部暂停",
        onOk: async () => {
          setToggling(true);
          try {
            const res = await api.batchJobAction("disable_all");
            Toast.success(`已全部暂停（处理 ${res.succeeded} 个任务）`);
            window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
            void dashboard.refresh(true);
          } catch (err) {
            errorToast(err, "全部暂停失败");
          } finally {
            setToggling(false);
          }
        },
      });
      return;
    }

    if (summary.hasResumable) {
      confirmAction({
        title: "全部恢复同步？",
        content:
          "将重新启用所有自动同步任务；尚未完成首次影响预览的镜像任务将被安全跳过。",
        okText: "全部恢复",
        onOk: async () => {
          setToggling(true);
          try {
            const res = await api.batchJobAction("enable_all");
            const skippedCount = res.skipped ?? 0;
            if (skippedCount > 0) {
              const names = (res.skippedJobs || [])
                .map((item) => item.jobName)
                .filter(Boolean)
                .join("、");
              Toast.warning(
                `已恢复 ${res.succeeded} 个任务；${skippedCount} 个镜像任务未预览已跳过${names ? `：${names}` : ""}`,
              );
            } else {
              Toast.success(`已恢复 ${res.succeeded} 个同步任务`);
            }
            window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
            void dashboard.refresh(true);
          } catch (err) {
            errorToast(err, "全部恢复失败");
          } finally {
            setToggling(false);
          }
        },
      });
    }
  };

  return (
    <section
      className={`global-sync-bar global-sync-bar--${variant}`}
      aria-label="全局同步状态栏"
      data-testid="global-sync-bar"
    >
      <div className="global-sync-bar__head">
        <div className="global-sync-bar__status">
          <span
            className={`global-sync-dot global-sync-dot--${summary.dotTone}`}
            aria-hidden="true"
          />
          <span className="global-sync-bar__label">{summary.statusLabel}</span>
        </div>
        {(summary.hasPausable || summary.hasResumable) && (
          <Button
            size="small"
            theme="borderless"
            type={summary.hasPausable ? "tertiary" : "primary"}
            loading={toggling}
            className="global-sync-bar__action"
            onClick={handleToggleAll}
          >
            {summary.hasPausable ? "全部暂停" : "全部恢复"}
          </Button>
        )}
      </div>

      <div className="global-sync-bar__pills">
        {summary.runningCount > 0 && (
          <Link to="/sync?status=running" className="global-sync-pill is-running">
            运行 {summary.runningCount}
          </Link>
        )}
        {summary.monitoringCount > 0 && (
          <Link to="/sync" className="global-sync-pill">
            守护 {summary.monitoringCount}
          </Link>
        )}
        {summary.errorCount > 0 && (
          <Link to="/sync?status=failed" className="global-sync-pill is-danger">
            异常 {summary.errorCount}
          </Link>
        )}
        {summary.conflictCount > 0 && (
          <Link
            to="/sync?status=conflict"
            className="global-sync-pill is-warning"
          >
            冲突 {summary.conflictCount}
          </Link>
        )}
        {summary.watchDegradedCount > 0 && (
          <Link to="/sync" className="global-sync-pill is-warning">
            监听降级 {summary.watchDegradedCount}
          </Link>
        )}
        {summary.runningCount === 0 &&
          summary.monitoringCount === 0 &&
          summary.errorCount === 0 &&
          summary.conflictCount === 0 &&
          summary.watchDegradedCount === 0 && (
            <span className="global-sync-pill is-muted">
              共 {activeJobs.length} 个任务
            </span>
          )}
      </div>
    </section>
  );
}
