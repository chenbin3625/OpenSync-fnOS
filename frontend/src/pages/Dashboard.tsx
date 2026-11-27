import {
  useEffect,
  useId,
  useMemo,
  useState,
} from "react";
import { Link, useNavigate } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconActivity,
  IconAlertTriangle,
  IconBarChartVStroked,
  IconChevronRight,
  IconCloudStroked,
  IconRefresh,
  IconServerStroked,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import dayjs from "dayjs";
import { api } from "../api/client";
import {
  Header,
  InlineNotice,
  LoadingPanel,
  LoadState,
  SectionCard,
} from "../components/common";
import { BwLimitChip } from "../components/BwLimitChip";
import { describeBwLimit } from "../lib/bwLimit";
import {
  buildSpeedChartGeometry,
  deriveDashboardRealtime,
  formatAxisSpeed,
  formatMbps,
  formatSpeed,
  mergeSpeedHistory,
  safeSpeed,
  sortStorageQuotas,
  splitSpeedParts,
} from "../lib/dashboardMetrics";
import { isDeepEqual, useResource } from "../lib/hooks";
import { formatDisplayPath } from "../lib/syncPathPreview";
import { isLegacyOpenListJob } from "../lib/taskForm";
import {
  formatDuration,
  formatSize,
  taskItemStatusNames,
  taskTypeNames,
} from "./Home/homeUtils";
import { calcTaskDisplayPercent } from "./Home/taskRows";
import type {
  DashboardJobItem,
  DashboardSpeedSample,
} from "../types";
import {
  formatRelativeSyncTime,
  isSyncStatusError,
  resolveEffectiveSyncStatus,
  syncStatusTone,
} from "../components/SyncStatus";

type InsightTab = "trend" | "quota";
type RecentFeedFilter = "all" | "success" | "failed" | "delete";

const FAILED_STATUSES = [3, 5, 6, 7];
const SPEED_CHART_WIDTH = 600;
const SPEED_CHART_HEIGHT = 104;

function isJobFailed(job: DashboardJobItem): boolean {
  return Boolean(
    isSyncStatusError(job.syncStatus) ||
      (job.lastTask && FAILED_STATUSES.includes(job.lastTask.status)),
  );
}

function jobDotTone(job: DashboardJobItem): string {
  if (job.running && job.paused) return "kpi-dot kpi-dot--warning";
  if (job.running) return "kpi-dot kpi-dot--primary kpi-dot--pulse";
  if (job.queued) return "kpi-dot kpi-dot--primary";
  const effective = resolveEffectiveSyncStatus(job);
  const tone = syncStatusTone(effective.state);
  if (tone === "danger" || isJobFailed(job)) return "kpi-dot kpi-dot--danger";
  if (tone === "warning") return "kpi-dot kpi-dot--warning";
  if (tone === "primary") {
    return effective.state === "scanning" || effective.state === "syncing"
      ? "kpi-dot kpi-dot--primary kpi-dot--pulse"
      : "kpi-dot kpi-dot--primary";
  }
  if (tone === "success") return "kpi-dot kpi-dot--success";
  return "kpi-dot";
}

function formatSampleClock(seconds?: number | null): string {
  const ts = Number(seconds || 0);
  if (ts <= 0) return "--:--:--";
  return dayjs.unix(ts).format("HH:mm:ss");
}

function computeJobLiveMetrics(job: DashboardJobItem) {
  const liveDoneSize = Math.max(0, Number(job.liveDoneSize || 0));
  const liveTotalSize = Math.max(0, Number(job.liveTotalSize || 0));
  const liveScanFinish = job.liveScanFinish !== false;
  const liveScan = job.liveScan ?? null;
  const liveSpeed = job.paused ? 0 : safeSpeed(job.liveSpeed);
  const liveRemainTime = job.paused
    ? 0
    : Math.max(0, Number(job.liveRemainTime || 0));
  const liveTotal = job.liveNum
    ? (job.liveNum.wait || 0) +
      (job.liveNum.running || 0) +
      (job.liveNum.success || 0) +
      (job.liveNum.fail || 0) +
      (job.liveNum.other || 0)
    : 0;
  const scanTotalFiles = Number(liveScan?.totalFiles || 0);
  const scanCheckedFiles = Number(liveScan?.checkedFiles || 0);
  const isScanningOnly =
    !liveScanFinish && liveTotal === 0 && liveTotalSize === 0;
  const livePct = isScanningOnly
    ? scanTotalFiles > 0 && scanCheckedFiles > 0
      ? Math.min(99, Math.round((scanCheckedFiles / scanTotalFiles) * 100))
      : 0
    : calcTaskDisplayPercent({
        doneSize: liveDoneSize,
        totalSize: liveTotalSize,
        num: job.liveNum,
        scanFinish: liveScanFinish,
        fallbackPercent: job.liveProgress,
      });
  const progressLabel = job.paused
    ? "传输已暂停"
    : isScanningOnly
      ? scanTotalFiles > 0 && scanCheckedFiles > 0
        ? "正在核对文件"
        : "正在扫描目录"
      : !liveScanFinish
        ? "正在同步（扫描中）"
        : "正在同步";
  return {
    liveDoneSize,
    liveTotalSize,
    liveScanFinish,
    liveScan,
    liveSpeed,
    liveRemainTime,
    liveTotal,
    scanTotalFiles,
    scanCheckedFiles,
    isScanningOnly,
    livePct,
    progressLabel,
  };
}

export default function Dashboard() {
  const navigate = useNavigate();
  const chartGradientId = useId();
  const dashboard = useResource((signal) => api.dashboard(signal));
  const engines = useResource((signal) => api.engines(signal));
  const nativeStorage = useResource((signal) => api.nativeStorageState(signal));

  const [trendRange, setTrendRange] = useState<7 | 30>(7);
  const [insightTab, setInsightTab] = useState<InsightTab>("trend");
  const [feedFilter, setFeedFilter] = useState<RecentFeedFilter>("all");
  const [hoveredSampleIndex, setHoveredSampleIndex] = useState<number | null>(
    null,
  );
  const [localSpeedHistory, setLocalSpeedHistory] = useState<
    DashboardSpeedSample[]
  >([]);
  const [bwNowTick, setBwNowTick] = useState<Date>(() => new Date());

  const recentFeed = useResource(
    (signal) =>
      api.recentTaskItems(
        {
          pageNum: 1,
          pageSize: 12,
          status: feedFilter === "success" ? 2 : undefined,
          type: feedFilter === "delete" ? 1 : undefined,
          hasError: feedFilter === "failed" ? 1 : undefined,
        },
        signal,
      ),
    [feedFilter],
  );

  const refreshAll = async () => {
    await Promise.all([
      dashboard.refresh(),
      engines.refresh(),
      nativeStorage.refresh(true),
      recentFeed.refresh(true),
    ]);
  };

  const data = dashboard.data;
  const allJobs = data?.jobs || [];

  // Record rolling speed history on each dashboard snapshot only when the
  // server does not supply speedHistory (legacy fixtures), avoiding a
  // redundant second render on every 2s poll.
  useEffect(() => {
    if (!data) return;
    const rt = data.realtime;
    if (rt && rt.speedHistory && rt.speedHistory.length > 0) return;
    const fallbackSpeed = (data.jobs || []).reduce(
      (sum, j) => sum + (j.running && !j.paused ? safeSpeed(j.liveSpeed) : 0),
      0,
    );
    const currentSpeed = rt ? safeSpeed(rt.totalSpeed) : fallbackSpeed;
    const sampleTs = rt?.sampledAt || Math.floor(Date.now() / 1000);
    setLocalSpeedHistory((prev) => {
      const next = mergeSpeedHistory(prev, undefined, sampleTs, currentSpeed);
      return isDeepEqual(prev, next) ? prev : next;
    });
  }, [data]);

  const hasLiveActivity =
    (data?.kpi?.runningJobs || 0) > 0 ||
    (data?.realtime?.activeTransfers || 0) > 0;

  // Adaptive background refresh: 2s while transferring for live rate updates,
  // 5s when idle to stay light on NAS resources.
  useEffect(() => {
    const pollMs = hasLiveActivity ? 2000 : 5000;
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void dashboard.refresh(true);
        if (hasLiveActivity) void recentFeed.refresh(true);
      }
    }, pollMs);
    return () => window.clearInterval(timer);
  }, [dashboard.refresh, recentFeed.refresh, hasLiveActivity]);

  useEffect(() => {
    const onJobsChanged = () => {
      void dashboard.refresh(true);
      void engines.refresh(true);
      void nativeStorage.refresh(true);
      void recentFeed.refresh(true);
    };
    window.addEventListener("opensync:jobs-changed", onJobsChanged);
    return () =>
      window.removeEventListener("opensync:jobs-changed", onJobsChanged);
  }, [
    dashboard.refresh,
    engines.refresh,
    nativeStorage.refresh,
    recentFeed.refresh,
  ]);

  const realtime = useMemo(
    () => deriveDashboardRealtime(data, localSpeedHistory),
    [data, localSpeedHistory],
  );

  const speedChart = useMemo(
    () =>
      buildSpeedChartGeometry(
        realtime.speedHistory,
        SPEED_CHART_WIDTH,
        SPEED_CHART_HEIGHT,
      ),
    [realtime.speedHistory],
  );

  const globalBwInfo = useMemo(
    () => describeBwLimit(realtime.globalBwLimit, { now: bwNowTick }),
    [realtime.globalBwLimit, bwNowTick],
  );

  useEffect(() => {
    if (!globalBwInfo.hasSchedule) return;
    const timer = window.setInterval(() => setBwNowTick(new Date()), 60_000);
    return () => window.clearInterval(timer);
  }, [globalBwInfo.hasSchedule]);

  const runningJobsList = useMemo(
    () =>
      allJobs
        .filter((j) => j.running && !isLegacyOpenListJob(j, engines.data))
        .sort((a, b) => {
          if (Boolean(a.paused) !== Boolean(b.paused)) {
            return a.paused ? 1 : -1;
          }
          // Keep row order stable across 2s speed polls so active tasks never
          // jump or swap positions as instantaneous speeds fluctuate.
          return a.id - b.id;
        }),
    [allJobs, engines.data],
  );

  const syncSummary = useMemo(() => {
    let monitoring = 0;
    let synced = 0;
    let pendingCount = 0;
    for (const j of allJobs) {
      if (isLegacyOpenListJob(j, engines.data) || j.enable !== 1) continue;
      monitoring += 1;
      const st = resolveEffectiveSyncStatus(j);
      if (st.state === "synced") synced += 1;
      if (st.pendingChanges > 0) pendingCount += st.pendingChanges;
    }
    return { monitoring, synced, pendingCount };
  }, [allJobs, engines.data]);

  const failedJobsCount = allJobs.filter(isJobFailed).length;
  const legacyJobCount = allJobs.filter((job) => isLegacyOpenListJob(job, engines.data)).length;
  const openJobDetail = (job: DashboardJobItem) => {
    navigate(`/sync?jobId=${job.id}`);
  };
  const openTaskList = (status = "all") => {
    navigate(status === "all" ? "/sync" : `/sync?status=${status}`);
  };

  const kpi = data?.kpi;
  const recent = data?.recent7d;
  const disabledJobsCount = kpi
    ? Math.max(0, kpi.totalJobs - kpi.enabledJobs)
    : 0;
  const enabledPct =
    kpi && kpi.totalJobs > 0
      ? Math.round((kpi.enabledJobs / kpi.totalJobs) * 100)
      : 0;

  const primaryEngine = engines.data?.[0];
  const storageHealthList = primaryEngine?.health?.storagesKnown
    ? primaryEngine.health.storages || []
    : [];
  const totalStorages =
    storageHealthList.length > 0
      ? storageHealthList.length
      : kpi?.totalEngines ?? 0;
  const unhealthyStorages =
    storageHealthList.length > 0
      ? storageHealthList.filter((st) => {
          if (st.disabled) return false;
          const s = (st.status || "").trim().toLowerCase();
          return s !== "" && s !== "work" && s !== "ok";
        }).length
      : kpi?.offlineEngines ?? 0;
  const healthyStorages =
    storageHealthList.length > 0
      ? Math.max(0, totalStorages - unhealthyStorages)
      : kpi?.onlineEngines ?? 0;
  const unknownStoragesCount =
    storageHealthList.length > 0
      ? 0
      : kpi
        ? Math.max(0, kpi.totalEngines - kpi.onlineEngines - kpi.offlineEngines)
        : 0;
  const onlinePct =
    totalStorages > 0 ? (healthyStorages / totalStorages) * 100 : 0;
  const offlinePct =
    totalStorages > 0 ? (unhealthyStorages / totalStorages) * 100 : 0;
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
  const recentSuccessRate =
    recent && recent.totalRuns > 0
      ? Math.round((recent.successRuns / recent.totalRuns) * 100)
      : 0;

  const sortedQuotas = useMemo(
    () => sortStorageQuotas(data?.storageQuotas),
    [data?.storageQuotas],
  );
  const hasTrendData = Boolean(data?.dailyTrend && data.dailyTrend.length > 0);
  const hasQuotaData = sortedQuotas.length > 0;
  const hasRightInsight = hasTrendData || hasQuotaData;
  const effectiveInsightTab: InsightTab =
    insightTab === "quota" && hasQuotaData
      ? "quota"
      : hasTrendData
        ? "trend"
        : "quota";
  const highestQuotaPercent = sortedQuotas[0]?.percent ?? 0;

  const speedParts = splitSpeedParts(realtime.totalSpeed);
  const mbpsLabel = formatMbps(realtime.totalSpeed);

  const liveCardTargetFilter = kpi?.runningJobs
    ? "running"
    : kpi?.queuedJobs
      ? "queued"
      : "running";

  const liveBarPct = realtime.isTransferring
    ? realtime.overallPercent > 0
      ? realtime.overallPercent
      : kpi && kpi.totalJobs > 0
        ? Math.min(100, Math.round((kpi.runningJobs / kpi.totalJobs) * 100))
        : 100
    : realtime.isAllPaused
      ? realtime.overallPercent > 0
        ? realtime.overallPercent
        : 100
      : kpi && kpi.totalJobs > 0 && kpi.queuedJobs > 0
        ? Math.min(100, Math.round((kpi.queuedJobs / kpi.totalJobs) * 100))
        : 0;

  const hoveredPoint =
    hoveredSampleIndex !== null &&
    hoveredSampleIndex >= 0 &&
    hoveredSampleIndex < speedChart.points.length
      ? speedChart.points[hoveredSampleIndex]
      : null;
  const latestPoint =
    speedChart.points.length > 0
      ? speedChart.points[speedChart.points.length - 1]
      : null;

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
              onClick={() => void refreshAll()}
            >
              刷新
            </Button>
          </div>
        }
      />

      <LoadState
        loading={dashboard.loading && !data}
        error={data ? "" : dashboard.error}
        retry={dashboard.refresh}
      >
        {data && kpi && recent && (
          <div className="dashboard-content">
            {dashboard.refreshError && (
              <InlineNotice
                role="alert"
                icon={<IconAlertTriangle aria-hidden="true" size="small" />}
                message={`实时数据刷新失败，正在保留上次快照：${dashboard.refreshError}`}
                onRetry={() => void dashboard.refresh(true)}
              />
            )}
            {legacyJobCount > 0 && (
              <InlineNotice
                role="alert"
                icon={<IconAlertTriangle aria-hidden="true" size="small" />}
                message={`检测到 ${legacyJobCount} 个旧版 OpenList 同步任务已自动停用。当前版本已移除 OpenList 引擎，请删除旧任务后在「存储管理」接入对应存储并重新创建。`}
              />
            )}

            {allJobs.length === 0 && (() => {
              const localRoots = nativeStorage.data?.localRoots || [];
              const remotes = nativeStorage.data?.remotes || [];
              const localReady = localRoots.length > 0;
              const remoteReady = remotes.length > 0;
              const localPreview = localRoots
                .slice(0, 2)
                .map((r) => r.path)
                .join("、");
              const remotePreview = remotes
                .slice(0, 3)
                .map((r) => r.name)
                .join("、");
              return (
                <SectionCard
                  className="dashboard-onboarding-card"
                  data-testid="dashboard-onboarding"
                  title="3 步完成首次同步配置"
                  extra={
                    <span className="muted">
                      参考 Cloud Sync 快速接入本地与云端存储
                    </span>
                  }
                >
                  <div className="dashboard-onboarding-grid">
                    <div
                      className={`dashboard-onboarding-step${localReady ? " is-done" : ""}`}
                    >
                      <div className="dashboard-onboarding-step__head">
                        <span className="dashboard-onboarding-step__num">1</span>
                        <div className="dashboard-onboarding-step__title">
                          <strong>确认飞牛本地授权目录</strong>
                          <Tag
                            color={localReady ? "green" : "amber"}
                            size="small"
                            type="light"
                          >
                            {localReady
                              ? `已授权 ${localRoots.length} 个目录`
                              : "待授权目录"}
                          </Tag>
                        </div>
                      </div>
                      <p className="dashboard-onboarding-step__desc muted">
                        {localReady
                          ? `可用目录：${localPreview}${localRoots.length > 2 ? " 等" : ""}`
                          : "请先在飞牛桌面「应用管理 → OpenSync → 文件权限」中授予需要备份的共享目录访问权限。"}
                      </p>
                      <Button
                        size="small"
                        type="tertiary"
                        onClick={() => navigate("/sync?storage=__local__&view=settings&action=local-folders")}
                      >
                        {localReady ? "查看授权目录" : "检查授权状态"}
                      </Button>
                    </div>

                    <div
                      className={`dashboard-onboarding-step${remoteReady ? " is-done" : ""}`}
                    >
                      <div className="dashboard-onboarding-step__head">
                        <span className="dashboard-onboarding-step__num">2</span>
                        <div className="dashboard-onboarding-step__title">
                          <strong>添加云盘或远程存储</strong>
                          <Tag
                            color={remoteReady ? "green" : "grey"}
                            size="small"
                            type="light"
                          >
                            {remoteReady
                              ? `已连接 ${remotes.length} 个`
                              : "本地互备可跳过"}
                          </Tag>
                        </div>
                      </div>
                      <p className="dashboard-onboarding-step__desc muted">
                        {remoteReady
                          ? `已接入：${remotePreview}${remotes.length > 3 ? " 等" : ""}`
                          : "支持阿里云盘、OneDrive、WebDAV（OpenList）、S3、SFTP 等云存储连接。"}
                      </p>
                      <Button
                        size="small"
                        type="tertiary"
                        onClick={() => navigate("/sync?action=add-connection")}
                      >
                        添加远程连接
                      </Button>
                    </div>

                    <div className="dashboard-onboarding-step">
                      <div className="dashboard-onboarding-step__head">
                        <span className="dashboard-onboarding-step__num">3</span>
                        <div className="dashboard-onboarding-step__title">
                          <strong>创建第一个同步任务</strong>
                          <Tag color="blue" size="small" type="light">
                            最后一步
                          </Tag>
                        </div>
                      </div>
                      <p className="dashboard-onboarding-step__desc muted">
                        选定来源与目标目录、同步方向（连续守护/双向/镜像）以及删除与冲突保护策略即可开始备份。
                      </p>
                      <Button
                        size="small"
                        type="primary"
                        theme="solid"
                        onClick={() => navigate("/sync?action=new-task")}
                      >
                        新建同步任务
                      </Button>
                    </div>
                  </div>
                </SectionCard>
              );
            })()}

            {/* 4 Core KPI Cards */}
            <div className="dashboard-kpi-grid">
              {/* Card 1: Real-time Speed */}
              <button
                type="button"
                className={[
                  "dashboard-kpi-card",
                  realtime.isAllPaused ? "is-warning" : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                onClick={() => openTaskList(liveCardTargetFilter)}
              >
                <div className="dashboard-kpi-head">
                  <span className="dashboard-kpi-label">实时速率</span>
                  <span
                    className={`dashboard-kpi-icon ${
                      realtime.isAllPaused
                        ? "dashboard-kpi-icon--warning"
                        : realtime.isTransferring
                          ? "dashboard-kpi-icon--primary"
                          : ""
                    }`}
                    aria-hidden="true"
                  >
                    <IconActivity />
                  </span>
                </div>
                <div className="dashboard-kpi-value-row">
                  <span className="dashboard-kpi-value">
                    {speedParts.value}
                  </span>
                  <span className="dashboard-kpi-unit">{speedParts.unit}</span>
                  {realtime.isTransferring ? (
                    <span className="dashboard-kpi-pill">
                      运行 {kpi.runningJobs + realtime.activeTransfers}
                    </span>
                  ) : realtime.isAllPaused ? (
                    <span className="dashboard-kpi-pill is-warning">
                      已暂停 {realtime.pausedJobs}
                    </span>
                  ) : kpi.queuedJobs > 0 ? (
                    <span className="dashboard-kpi-pill">
                      排队 {kpi.queuedJobs}
                    </span>
                  ) : null}
                </div>
                <div className="dashboard-kpi-bar" aria-hidden="true">
                  {liveBarPct > 0 && (
                    <span
                      className={`dashboard-kpi-bar-seg ${
                        realtime.isAllPaused
                          ? "dashboard-kpi-bar-seg--warning"
                          : realtime.isTransferring
                            ? "dashboard-kpi-bar-seg--primary"
                            : "dashboard-kpi-bar-seg--muted"
                      }`}
                      style={{ width: `${liveBarPct}%` }}
                    />
                  )}
                </div>
                <div className="dashboard-kpi-foot dashboard-kpi-foot--live">
                  {realtime.isTransferring ? (
                    <>
                      <span className="dashboard-kpi-stat is-primary">
                        <span
                          className="kpi-dot kpi-dot--primary kpi-dot--pulse"
                          aria-hidden="true"
                        />
                        峰值 {formatSpeed(realtime.peakSpeed)}
                      </span>
                      {realtime.estimatedRemainTime > 0 && (
                        <span
                          className="dashboard-kpi-eta"
                          title={`剩余 ${formatDuration(realtime.estimatedRemainTime)}`}
                        >
                          剩余 {formatDuration(realtime.estimatedRemainTime)}
                        </span>
                      )}
                    </>
                  ) : realtime.isAllPaused ? (
                    <span className="dashboard-kpi-stat is-warning">
                      <span
                        className="kpi-dot kpi-dot--warning"
                        aria-hidden="true"
                      />
                      已暂停 {realtime.pausedJobs} 个任务
                    </span>
                  ) : kpi.queuedJobs > 0 ? (
                    <span className="dashboard-kpi-stat is-primary">
                      <span
                        className="kpi-dot kpi-dot--primary"
                        aria-hidden="true"
                      />
                      {kpi.queuedJobs} 个排队等待中
                    </span>
                  ) : globalBwInfo.configured ? (
                    <Tooltip content={globalBwInfo.tooltipSummary}>
                      <span
                        className={`dashboard-kpi-stat dashboard-kpi-stat--truncate${
                          globalBwInfo.isCurrentlyLimited ? " is-primary" : ""
                        }`}
                      >
                        <span
                          className={`kpi-dot${
                            globalBwInfo.isCurrentlyLimited
                              ? " kpi-dot--primary"
                              : globalBwInfo.hasSchedule
                                ? " kpi-dot--success"
                                : ""
                          }`}
                          aria-hidden="true"
                        />
                        <span className="dashboard-kpi-stat-text">
                          {syncSummary.monitoring > 0
                            ? `监控 ${syncSummary.monitoring} 项 · ${globalBwInfo.kpiLabel}`
                            : `当前空闲 · ${globalBwInfo.kpiLabel}`}
                        </span>
                      </span>
                    </Tooltip>
                  ) : syncSummary.monitoring > 0 ? (
                    <span className="dashboard-kpi-stat is-success">
                      <span className="kpi-dot kpi-dot--success" aria-hidden="true" />
                      持续监控 {syncSummary.monitoring} 个任务
                    </span>
                  ) : (
                    <span className="dashboard-kpi-stat">
                      <span className="kpi-dot" aria-hidden="true" />
                      当前空闲 · 无活跃传输
                    </span>
                  )}
                </div>
              </button>

              {/* Card 2: Sync Jobs Inventory */}
              <button
                type="button"
                className="dashboard-kpi-card"
                onClick={() => openTaskList()}
              >
                <div className="dashboard-kpi-head">
                  <span className="dashboard-kpi-label">持续同步</span>
                  <span
                    className="dashboard-kpi-icon dashboard-kpi-icon--primary"
                    aria-hidden="true"
                  >
                    <IconCloudStroked />
                  </span>
                </div>
                <div className="dashboard-kpi-value-row">
                  <span className="dashboard-kpi-value">
                    {kpi.enabledJobs}
                  </span>
                  <span className="dashboard-kpi-denom">
                    / {kpi.totalJobs}
                  </span>
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
                    <span
                      className="kpi-dot kpi-dot--success"
                      aria-hidden="true"
                    />
                    已同步 {syncSummary.synced}
                  </span>
                  <span className="dashboard-kpi-stat">
                    <span className="kpi-dot" aria-hidden="true" />
                    已暂停 {disabledJobsCount}
                  </span>
                  {failedJobsCount > 0 && (
                    <span className="dashboard-kpi-stat is-danger">
                      异常 {failedJobsCount}
                    </span>
                  )}
                </div>
              </button>

              {/* Card 3: Storage Health */}
              <button
                type="button"
                className={[
                  "dashboard-kpi-card",
                  unhealthyStorages > 0 ? "is-danger" : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                onClick={() => navigate("/sync?view=settings")}
              >
                <div className="dashboard-kpi-head">
                  <span className="dashboard-kpi-label">
                    存储健康
                    <IconChevronRight
                      size="small"
                      className="dashboard-kpi-link-arrow"
                      aria-hidden="true"
                    />
                  </span>
                  <span
                    className={`dashboard-kpi-icon ${
                      unhealthyStorages > 0
                        ? "dashboard-kpi-icon--danger"
                        : totalStorages > 0 &&
                            healthyStorages === totalStorages
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
                    {healthyStorages}
                  </span>
                  <span className="dashboard-kpi-denom">
                    / {totalStorages}
                  </span>
                </div>
                {totalStorages > 0 && (
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
                  {totalStorages === 0 ? (
                    <span className="dashboard-kpi-stat">
                      <span className="kpi-dot" aria-hidden="true" />
                      前往配置本地与远程存储
                    </span>
                  ) : unhealthyStorages > 0 ? (
                    <span className="dashboard-kpi-stat is-danger">
                      <span
                        className="kpi-dot kpi-dot--danger"
                        aria-hidden="true"
                      />
                      {unhealthyStorages} 个存储状态异常
                    </span>
                  ) : unknownStoragesCount > 0 ? (
                    <span className="dashboard-kpi-stat">
                      <span className="kpi-dot" aria-hidden="true" />
                      存储待检测
                    </span>
                  ) : (
                    <span className="dashboard-kpi-stat is-success">
                      <span
                        className="kpi-dot kpi-dot--success"
                        aria-hidden="true"
                      />
                      全部存储连接正常
                    </span>
                  )}
                </div>
              </button>

              {/* Card 4: Recent 7d Volume & Success */}
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
                      {recent.totalRuns > 0 && (
                        <span
                          className={`dashboard-kpi-pill${
                            recent.failRuns > 0
                              ? " is-warning"
                              : recent.successRuns > 0
                                ? " is-success"
                                : " is-muted"
                          }`}
                        >
                          成功率 {recentSuccessRate}%
                        </span>
                      )}
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
                      {recent.failRuns > 0 ? (
                        <span className="dashboard-kpi-stat is-danger">
                          异常 {recent.failRuns}
                        </span>
                      ) : (
                        <span
                          className={`dashboard-kpi-stat${recent.successRuns > 0 ? " is-success" : ""}`}
                        >
                          成功 {recent.successRuns}
                        </span>
                      )}
                      <span>{recent.totalFiles} 文件</span>
                    </div>
                  </>
                );
                return recent.failRuns > 0 ? (
                  <button
                    type="button"
                    className="dashboard-kpi-card"
                    onClick={() => openTaskList("failed")}
                  >
                    {recentCardBody}
                  </button>
                ) : (
                  <div className="dashboard-kpi-card">{recentCardBody}</div>
                );
              })()}
            </div>

            {/* Core Live Speed Monitor + Key Insights */}
            <div
              className={`dashboard-monitor-grid${hasRightInsight ? "" : " is-single"}`}
            >
              {/* Left: Real-time Speed Monitor */}
              <SectionCard
                className="dashboard-live-card"
                bodyClassName="dashboard-live-body"
                title={
                  <div className="dashboard-live-title">
                    <h2>实时传输速率</h2>
                    <span
                      className={`dashboard-live-badge${
                        realtime.isTransferring
                          ? " is-active"
                          : realtime.isAllPaused
                            ? " is-paused"
                            : ""
                      }`}
                    >
                      <span
                        className={`kpi-dot ${
                          realtime.isTransferring
                            ? "kpi-dot--primary kpi-dot--pulse"
                            : realtime.isAllPaused
                              ? "kpi-dot--warning"
                              : ""
                        }`}
                        aria-hidden="true"
                      />
                      {realtime.isTransferring
                        ? "实时监测"
                        : realtime.isAllPaused
                          ? "已暂停"
                          : "当前空闲"}
                    </span>
                  </div>
                }
                extra={
                  <div className="dashboard-live-head-extra">
                    {hoveredPoint ? (
                      <span className="dashboard-live-hover-readout">
                        {formatSampleClock(hoveredPoint.ts)} ·{" "}
                        <strong>{formatSpeed(hoveredPoint.speed)}</strong>
                      </span>
                    ) : (
                      <>
                        {realtime.activeTransfers > 0 && (
                          <Tag size="small" type="light" color="blue">
                            后台传输 {realtime.activeTransfers}
                          </Tag>
                        )}
                        <BwLimitChip
                          expression={realtime.globalBwLimit}
                          now={bwNowTick}
                          onConfigure={() => navigate("/settings")}
                        />
                      </>
                    )}
                  </div>
                }
              >
                {/* Hero Speed Readout + 3 Key Metrics */}
                <div className="dashboard-live-hero">
                  <div className="dashboard-live-speed-main">
                    <div className="dashboard-live-speed-value">
                      <span className="dashboard-live-speed-num">
                        {speedParts.value}
                      </span>
                      <span className="dashboard-live-speed-unit">
                        {speedParts.unit}
                      </span>
                    </div>
                    <div className="dashboard-live-speed-sub muted">
                      {mbpsLabel ? (
                        <>
                          <span>≈ {mbpsLabel}</span>
                          {realtime.totalSpeedAvg > 0 && (
                            <span>
                              · 均速 {formatSpeed(realtime.totalSpeedAvg)}
                            </span>
                          )}
                        </>
                      ) : realtime.isTransferring ? (
                        <span>正在建立连接或扫描目录…</span>
                      ) : realtime.isAllPaused ? (
                        <span>所有活跃同步任务均已暂停</span>
                      ) : (
                        <span>有任务运行时自动切换为 2 秒高频采样</span>
                      )}
                    </div>
                  </div>

                  <div className="dashboard-live-stats">
                    <div className="dashboard-live-stat-box">
                      <span className="dashboard-live-stat-label muted">
                        窗口峰值
                      </span>
                      <strong className="dashboard-live-stat-val">
                        {formatSpeed(realtime.peakSpeed)}
                      </strong>
                    </div>
                    <div className="dashboard-live-stat-box">
                      <span className="dashboard-live-stat-label muted">
                        {realtime.isTransferring || realtime.isAllPaused
                          ? "本次传输"
                          : "已同步状态"}
                      </span>
                      <strong className="dashboard-live-stat-val">
                        {realtime.isTransferring || realtime.isAllPaused
                          ? realtime.totalBytes > 0
                            ? `${formatSize(realtime.totalDoneBytes)} / ${formatSize(realtime.totalBytes)}`
                            : "扫描统计中"
                          : syncSummary.monitoring > 0
                            ? `${syncSummary.synced} / ${syncSummary.monitoring} 已同步`
                            : "暂无启用"}
                      </strong>
                    </div>
                    <div className="dashboard-live-stat-box">
                      <span className="dashboard-live-stat-label muted">
                        {realtime.isTransferring || realtime.isAllPaused
                          ? "预计剩余"
                          : "持续监控"}
                      </span>
                      <strong className="dashboard-live-stat-val">
                        {realtime.isTransferring
                          ? realtime.estimatedRemainTime > 0
                            ? formatDuration(realtime.estimatedRemainTime)
                            : "计算中"
                          : realtime.isAllPaused
                            ? "已暂停"
                            : syncSummary.monitoring > 0
                              ? `${syncSummary.monitoring} 个任务`
                              : "全部已暂停"}
                      </strong>
                    </div>
                  </div>
                </div>

                {/* Live SVG Throughput Waveform */}
                <div
                  className="dashboard-speed-chart"
                  role="img"
                  aria-label={`实时传输速率波形图，当前速率 ${formatSpeed(realtime.totalSpeed)}`}
                  onMouseLeave={() => setHoveredSampleIndex(null)}
                >
                  <div className="dashboard-speed-yaxis" aria-hidden="true">
                    <span>{formatAxisSpeed(speedChart.yMax)}</span>
                    <span>{formatAxisSpeed(speedChart.yMid)}</span>
                    <span>0</span>
                  </div>
                  <div className="dashboard-speed-canvas">
                    <svg
                      viewBox={`0 0 ${SPEED_CHART_WIDTH} ${SPEED_CHART_HEIGHT}`}
                      preserveAspectRatio="none"
                      className="dashboard-speed-svg"
                      aria-hidden="true"
                    >
                      <defs>
                        <linearGradient
                          id={chartGradientId}
                          x1="0"
                          y1="0"
                          x2="0"
                          y2="1"
                        >
                          <stop
                            offset="0%"
                            stopColor="var(--semi-color-primary)"
                            stopOpacity="0.28"
                          />
                          <stop
                            offset="100%"
                            stopColor="var(--semi-color-primary)"
                            stopOpacity="0.02"
                          />
                        </linearGradient>
                      </defs>
                      <line
                        x1="0"
                        y1="10"
                        x2={SPEED_CHART_WIDTH}
                        y2="10"
                        className="dashboard-speed-gridline"
                        vectorEffect="non-scaling-stroke"
                      />
                      <line
                        x1="0"
                        y1={(10 + (SPEED_CHART_HEIGHT - 6)) / 2}
                        x2={SPEED_CHART_WIDTH}
                        y2={(10 + (SPEED_CHART_HEIGHT - 6)) / 2}
                        className="dashboard-speed-gridline"
                        vectorEffect="non-scaling-stroke"
                      />
                      <line
                        x1="0"
                        y1={SPEED_CHART_HEIGHT - 6}
                        x2={SPEED_CHART_WIDTH}
                        y2={SPEED_CHART_HEIGHT - 6}
                        className="dashboard-speed-baseline"
                        vectorEffect="non-scaling-stroke"
                      />
                      {speedChart.areaPath && (
                        <path
                          d={speedChart.areaPath}
                          fill={`url(#${chartGradientId})`}
                        />
                      )}
                      {speedChart.linePath && (
                        <path
                          d={speedChart.linePath}
                          fill="none"
                          stroke={
                            realtime.isAllPaused
                              ? "var(--semi-color-warning)"
                              : "var(--semi-color-primary)"
                          }
                          strokeWidth="2"
                          strokeLinejoin="round"
                          strokeLinecap="round"
                          vectorEffect="non-scaling-stroke"
                        />
                      )}
                    </svg>

                    {/* Crisp non-distorted live & hover indicator dots */}
                    {latestPoint && speedChart.hasSignal && (
                      <span
                        className={`dashboard-speed-dot${realtime.isTransferring ? " is-live" : ""}`}
                        style={{
                          left: `${(latestPoint.x / SPEED_CHART_WIDTH) * 100}%`,
                          top: `${(latestPoint.y / SPEED_CHART_HEIGHT) * 100}%`,
                        }}
                        aria-hidden="true"
                      />
                    )}
                    {hoveredPoint && (
                      <span
                        className="dashboard-speed-dot is-hover"
                        style={{
                          left: `${(hoveredPoint.x / SPEED_CHART_WIDTH) * 100}%`,
                          top: `${(hoveredPoint.y / SPEED_CHART_HEIGHT) * 100}%`,
                        }}
                        aria-hidden="true"
                      />
                    )}

                    {/* Interactive hover hit zones */}
                    {speedChart.points.length > 0 && (
                      <div className="dashboard-speed-hitlayer">
                        {speedChart.points.map((pt, idx) => (
                          <span
                            key={idx}
                            className="dashboard-speed-hit"
                            style={{
                              left: `${(pt.x / SPEED_CHART_WIDTH) * 100}%`,
                            }}
                            onMouseEnter={() => setHoveredSampleIndex(idx)}
                          />
                        ))}
                      </div>
                    )}

                    {!speedChart.hasSignal && !realtime.isTransferring && (
                      <div className="dashboard-speed-idle-pill">
                        当前无活跃传输
                      </div>
                    )}
                  </div>
                </div>

                {/* Active Tasks Breakdown OR Idle Operational Summary */}
                {runningJobsList.length > 0 ? (
                  <div className="dashboard-live-tasks">
                    <div className="dashboard-live-tasks-head">
                      <span className="muted">
                        活跃同步任务（{runningJobsList.length}）
                      </span>
                      {runningJobsList.length > 4 && (
                        <Button
                          size="small"
                          theme="borderless"
                          onClick={() => openTaskList("running")}
                        >
                          查看全部
                        </Button>
                      )}
                    </div>
                    <div className="dashboard-live-task-list">
                      {runningJobsList.slice(0, 4).map((job) => {
                        const jobName = job.remark || `同步任务 #${job.id}`;
                        const m = computeJobLiveMetrics(job);
                        return (
                          <div key={job.id} className="dashboard-live-task-row">
                            <div
                              className="dashboard-live-task-main"
                              onClick={() => openJobDetail(job)}
                            >
                              <div className="dashboard-live-task-top">
                                <span className="dashboard-live-task-name-wrap">
                                  <span
                                    className={jobDotTone(job)}
                                    aria-hidden="true"
                                  />
                                  <button
                                    type="button"
                                    className="dashboard-live-task-name"
                                    title={jobName}
                                    onClick={(e) => {
                                      e.stopPropagation();
                                      openJobDetail(job);
                                    }}
                                  >
                                    {jobName}
                                  </button>
                                </span>
                                <span className="dashboard-live-task-metrics">
                                  <strong
                                    className={`dashboard-live-task-speed${
                                      job.paused ? " is-paused" : ""
                                    }`}
                                  >
                                    {job.paused
                                      ? "已暂停"
                                      : m.liveSpeed > 0
                                        ? formatSpeed(m.liveSpeed)
                                        : m.isScanningOnly
                                          ? "扫描中"
                                          : "0 B/s"}
                                  </strong>
                                  <span className="dashboard-live-task-pct">
                                    {m.livePct}%
                                  </span>
                                  {m.liveRemainTime > 0 && !job.paused && (
                                    <span className="muted">
                                      剩余 {formatDuration(m.liveRemainTime)}
                                    </span>
                                  )}
                                </span>
                              </div>
                              <Progress
                                percent={m.livePct}
                                showInfo={false}
                                stroke={
                                  job.paused
                                    ? "var(--semi-color-warning)"
                                    : "var(--semi-color-primary)"
                                }
                              />
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                ) : (
                  <div className="dashboard-live-idle-foot">
                    <div className="dashboard-live-idle-item">
                      <IconRefresh
                        size="small"
                        className="muted"
                        aria-hidden="true"
                      />
                      {syncSummary.monitoring > 0 ? (
                        <span className="muted">
                          <strong>{syncSummary.monitoring}</strong> 个持续同步任务正在实时监控变更
                          {syncSummary.synced > 0
                            ? `（${syncSummary.synced} 个已同步）`
                            : ""}
                          {syncSummary.pendingCount > 0
                            ? ` · 待处理 ${syncSummary.pendingCount} 项`
                            : ""}
                        </span>
                      ) : (
                        <span className="muted">
                          当前没有运行中的持续同步任务，可恢复已有任务或新建同步
                        </span>
                      )}
                    </div>
                    <div className="dashboard-live-idle-item">
                      {failedJobsCount > 0 ? (
                        <button
                          type="button"
                          className="dashboard-live-idle-alert is-danger"
                          onClick={() => openTaskList("failed")}
                        >
                          <IconAlertTriangle size="small" aria-hidden="true" />
                          <span>
                            {failedJobsCount} 个任务同步异常，点击排查
                          </span>
                        </button>
                      ) : (
                        <span className="dashboard-live-idle-ok">
                          <IconTickCircle size="small" aria-hidden="true" />
                          <span>所有已启用同步任务健康运行</span>
                        </span>
                      )}
                    </div>
                  </div>
                )}
              </SectionCard>

              {/* Right: Trend / Storage Quota Insight Card */}
              {hasRightInsight && (
                <SectionCard
                  className="dashboard-insight-card"
                  bodyClassName="dashboard-insight-body"
                  title={
                    hasTrendData && hasQuotaData ? (
                      <div
                        className="dashboard-insight-switcher"
                        role="tablist"
                        aria-label="看板洞察视图"
                      >
                        <button
                          type="button"
                          role="tab"
                          aria-selected={effectiveInsightTab === "trend"}
                          className={`dashboard-insight-tab${
                            effectiveInsightTab === "trend" ? " is-active" : ""
                          }`}
                          onClick={() => setInsightTab("trend")}
                        >
                          同步趋势
                        </button>
                        <button
                          type="button"
                          role="tab"
                          aria-selected={effectiveInsightTab === "quota"}
                          className={`dashboard-insight-tab${
                            effectiveInsightTab === "quota" ? " is-active" : ""
                          }`}
                          onClick={() => setInsightTab("quota")}
                        >
                          <span>存储配额</span>
                          {highestQuotaPercent >= 80 && (
                            <span
                              className={`kpi-dot ${
                                highestQuotaPercent >= 90
                                  ? "kpi-dot--danger"
                                  : "kpi-dot--warning"
                              }`}
                              aria-hidden="true"
                            />
                          )}
                        </button>
                      </div>
                    ) : effectiveInsightTab === "trend" ? (
                      `近 ${trendRange} 天同步趋势`
                    ) : (
                      "存储空间配额"
                    )
                  }
                  extra={
                    effectiveInsightTab === "trend" ? (
                      <div
                        className="dashboard-trend-tabs"
                        role="group"
                        aria-label="趋势时间范围"
                      >
                        {([7, 30] as const).map((days) => (
                          <button
                            key={days}
                            type="button"
                            aria-pressed={trendRange === days}
                            className={`dashboard-trend-tab${
                              trendRange === days ? " is-active" : ""
                            }`}
                            onClick={() => setTrendRange(days)}
                          >
                            {days} 天
                          </button>
                        ))}
                      </div>
                    ) : (
                      <Button
                        size="small"
                        theme="borderless"
                        onClick={() => navigate("/sync?view=settings")}
                      >
                        存储管理
                      </Button>
                    )
                  }
                >
                  {effectiveInsightTab === "trend" &&
                    data.dailyTrend &&
                    (() => {
                      const series = data.dailyTrend.slice(-trendRange);
                      const maxBytes = Math.max(
                        1,
                        ...series.map((d) => d.bytes || 0),
                      );
                      const maxRuns = Math.max(
                        1,
                        ...series.map((d) => d.runs || 0),
                      );
                      const useBytes = series.some((d) => d.bytes > 0);
                      const totalPeriodBytes = series.reduce(
                        (s, d) => s + (d.bytes || 0),
                        0,
                      );
                      const totalPeriodRuns = series.reduce(
                        (s, d) => s + (d.runs || 0),
                        0,
                      );
                      const totalPeriodSuccess = series.reduce(
                        (s, d) => s + (d.successRuns || 0),
                        0,
                      );
                      const totalPeriodFail = series.reduce(
                        (s, d) => s + (d.failRuns || 0),
                        0,
                      );
                      const periodSuccessRate =
                        totalPeriodRuns > 0
                          ? Math.round(
                              (totalPeriodSuccess / totalPeriodRuns) * 100,
                            )
                          : 0;
                      return (
                        <>
                          <div className="dashboard-trend-summary">
                            <div className="dashboard-trend-stat">
                              <span className="muted">近 {trendRange} 天传输</span>
                              <strong>{formatSize(totalPeriodBytes)}</strong>
                            </div>
                            <div className="dashboard-trend-stat">
                              <span className="muted">执行 / 成功率</span>
                              <strong>
                                {totalPeriodRuns} 次
                                {totalPeriodRuns > 0
                                  ? ` · ${periodSuccessRate}%`
                                  : ""}
                              </strong>
                            </div>
                            {totalPeriodFail > 0 && (
                              <div className="dashboard-trend-stat">
                                <span className="muted">异常次数</span>
                                <strong className="inline-error">
                                  {totalPeriodFail} 次
                                </strong>
                              </div>
                            )}
                          </div>
                          <div
                            className={`dashboard-trend-chart${
                              trendRange === 30 ? " is-dense" : ""
                            }`}
                            role="img"
                            aria-label={`近 ${trendRange} 天同步趋势图`}
                          >
                            {series.map((day, idx) => {
                              const rawVal = useBytes ? day.bytes : day.runs;
                              const maxVal = useBytes ? maxBytes : maxRuns;
                              const pct =
                                rawVal > 0
                                  ? Math.max(
                                      10,
                                      Math.round((rawVal / maxVal) * 100),
                                    )
                                  : 4;
                              const tone =
                                day.failRuns > 0
                                  ? "is-danger"
                                  : day.runs > 0
                                    ? "is-primary"
                                    : "is-idle";
                              const showLabel =
                                trendRange === 7 ||
                                idx === 0 ||
                                idx === series.length - 1;
                              return (
                                <div
                                  key={day.date}
                                  className="dashboard-trend-col"
                                  title={`${day.date}：${day.runs} 次（成功 ${day.successRuns} / 异常 ${day.failRuns}），${day.files} 文件，${formatSize(day.bytes)}`}
                                >
                                  <div className="dashboard-trend-track">
                                    <div
                                      className={`dashboard-trend-bar ${tone}`}
                                      style={{ height: `${pct}%` }}
                                    />
                                  </div>
                                  <span
                                    className={`dashboard-trend-label muted${
                                      showLabel ? "" : " is-hidden"
                                    }`}
                                    aria-hidden={!showLabel}
                                  >
                                    {day.date.slice(5)}
                                  </span>
                                </div>
                              );
                            })}
                          </div>
                          <div className="dashboard-trend-meta muted">
                            <span className="dashboard-trend-legend">
                              <span className="dashboard-kpi-stat">
                                <span
                                  className="kpi-dot kpi-dot--primary"
                                  aria-hidden="true"
                                />
                                正常
                              </span>
                              <span className="dashboard-kpi-stat">
                                <span
                                  className="kpi-dot kpi-dot--danger"
                                  aria-hidden="true"
                                />
                                含异常
                              </span>
                            </span>
                            <span>
                              {useBytes ? "柱高按传输量" : "柱高按执行次数"}
                            </span>
                          </div>
                        </>
                      );
                    })()}

                  {effectiveInsightTab === "quota" && hasQuotaData && (
                    <div className="dashboard-quota-list">
                      {sortedQuotas.slice(0, 5).map((q) => {
                        const stroke =
                          q.percent >= 90
                            ? "var(--semi-color-danger)"
                            : q.percent >= 75
                              ? "var(--semi-color-warning)"
                              : "var(--semi-color-primary)";
                        return (
                          <div key={q.name} className="dashboard-quota-item">
                            <div className="dashboard-quota-head">
                              <strong
                                className="dashboard-quota-name"
                                title={q.name}
                              >
                                {q.name}
                              </strong>
                              <span className="dashboard-quota-usage muted">
                                <strong className="dashboard-quota-pct">
                                  {q.percent}%
                                </strong>{" "}
                                · {formatSize(q.usedBytes)} /{" "}
                                {formatSize(q.totalBytes)}
                                {q.free !== undefined
                                  ? `（剩 ${formatSize(q.free)}）`
                                  : ""}
                              </span>
                            </div>
                            <Progress
                              percent={q.percent}
                              showInfo={false}
                              stroke={stroke}
                            />
                          </div>
                        );
                      })}
                    </div>
                  )}
                </SectionCard>
              )}
            </div>

            {(allJobs.length > 0 ||
              (recentFeed.data?.count ?? 0) > 0 ||
              Boolean(recentFeed.error)) && (
              <SectionCard
                className="dashboard-recent-card"
                data-testid="dashboard-recent-feed"
                title="最近同步动态"
                extra={
                  <div className="dashboard-recent-head-actions">
                    <div
                      className="dashboard-recent-filters"
                      role="group"
                      aria-label="同步动态筛选"
                    >
                      {(
                        [
                          { id: "all", label: "全部" },
                          { id: "success", label: "成功" },
                          { id: "failed", label: "异常" },
                          { id: "delete", label: "删除" },
                        ] as const
                      ).map((opt) => (
                        <Button
                          key={opt.id}
                          size="small"
                          theme={feedFilter === opt.id ? "solid" : "borderless"}
                          type={feedFilter === opt.id ? "primary" : "tertiary"}
                          onClick={() => setFeedFilter(opt.id)}
                        >
                          {opt.label}
                        </Button>
                      ))}
                    </div>
                    <Link to="/sync" className="dashboard-panel-link">
                      进入工作台
                      <IconChevronRight aria-hidden="true" />
                    </Link>
                  </div>
                }
              >
                {recentFeed.error && !recentFeed.data && (
                  <InlineNotice
                    role="alert"
                    message={recentFeed.error}
                    retrying={recentFeed.loading}
                    onRetry={() => void recentFeed.refresh(true)}
                  />
                )}
                {recentFeed.data?.stats && recentFeed.data.stats.allNum > 0 && (
                  <div className="dashboard-recent-stats muted">
                    <span>共 {recentFeed.data.stats.allNum} 条记录</span>
                    <span>成功 {recentFeed.data.stats.successNum}</span>
                    {recentFeed.data.stats.errorNum > 0 && (
                      <span className="dashboard-recent-stat--danger">
                        异常 {recentFeed.data.stats.errorNum}
                      </span>
                    )}
                    {recentFeed.data.stats.deleteNum > 0 && (
                      <span>删除 {recentFeed.data.stats.deleteNum}</span>
                    )}
                    <span>已传输 {formatSize(recentFeed.data.stats.sumSize)}</span>
                  </div>
                )}
                {recentFeed.data?.dataList?.length ? (
                  <div className="dashboard-recent-list">
                    {recentFeed.data.dataList.map((item) => {
                      const hasError = Boolean(item.errMsg) || item.status === 7;
                      const isDelete = item.type === 1;
                      const statusTone = hasError
                        ? "red"
                        : item.status === 2
                          ? "green"
                          : item.status === 1
                            ? "blue"
                            : "grey";
                      const statusText =
                        taskItemStatusNames[item.status] || "已处理";
                      const typeText =
                        item.type !== undefined
                          ? taskTypeNames[item.type] || ""
                          : "";
                      const title =
                        item.fileName ||
                        item.srcPath ||
                        item.dstPath ||
                        "未命名条目";
                      return (
                        <Link
                          key={`${item.taskId ?? 0}-${item.id ?? title}-${item.createTime ?? 0}`}
                          to={`/sync?jobId=${item.jobId}&tab=records`}
                          className="dashboard-recent-row"
                        >
                          <div className="dashboard-recent-row__main">
                            <div className="dashboard-recent-row__title">
                              <Tag color={statusTone} size="small" type="light">
                                {statusText}
                              </Tag>
                              {typeText && (
                                <Tag
                                  color={isDelete ? "amber" : "grey"}
                                  size="small"
                                  type="light"
                                >
                                  {typeText}
                                </Tag>
                              )}
                              <strong title={title}>{title}</strong>
                            </div>
                            <span
                              className="dashboard-recent-row__meta muted"
                              title={`${item.jobName} · ${item.srcPath || ""} → ${item.dstPath || ""}`}
                            >
                              {item.jobName}
                              {item.srcPath
                                ? ` · ${formatDisplayPath(item.srcPath)}`
                                : ""}
                              {item.dstPath
                                ? ` → ${formatDisplayPath(item.dstPath)}`
                                : ""}
                            </span>
                            {item.errMsg && (
                              <span
                                className="dashboard-recent-row__error"
                                title={item.errMsg}
                              >
                                {item.errMsg}
                              </span>
                            )}
                          </div>
                          <div className="dashboard-recent-row__side muted">
                            <span>
                              {item.isPath === 1
                                ? "目录"
                                : formatSize(item.fileSize || 0)}
                            </span>
                            <span>
                              {formatRelativeSyncTime(item.createTime)}
                            </span>
                          </div>
                        </Link>
                      );
                    })}
                  </div>
                ) : (
                  <p className="dashboard-recent-empty muted">
                    {feedFilter === "all"
                      ? "暂无最近文件同步动态，任务执行后将在此实时显示。"
                      : "当前筛选下暂无文件记录。"}
                  </p>
                )}
              </SectionCard>
            )}

          </div>
        )}
      </LoadState>

    </div>
  );
}
