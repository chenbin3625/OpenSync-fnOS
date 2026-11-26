import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useState,
} from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
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
  IconDeleteStroked,
  IconEyeOpenedStroked,
  IconPause,
  IconPlay,
  IconPlusStroked,
  IconRefresh,
  IconServerStroked,
  IconStop,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import dayjs from "dayjs";
import { api } from "../api/client";
import {
  EmptyState,
  Header,
  IconButton,
  InlineNotice,
  LoadState,
  LoadingPanel,
  SearchFilterInput,
  SectionCard,
  Status,
  confirmAction,
  confirmDelete,
  errorToast,
} from "../components/common";
import { DryRunModal } from "../components/DryRunModal";
import { FullSyncApprovalModal } from "../components/FullSyncApprovalModal";
import {
  buildSpeedChartGeometry,
  deriveDashboardRealtime,
  findNextScheduledJob,
  formatAxisSpeed,
  formatMbps,
  formatSpeed,
  mergeSpeedHistory,
  safeSpeed,
  sortStorageQuotas,
  splitSpeedParts,
} from "../lib/dashboardMetrics";
import { useAction, useResource } from "../lib/hooks";
import { formatDisplayPathList } from "../lib/syncPathPreview";
import {
  cloneJobToForm,
  findBatchMoveJobs,
  isLegacyOpenListJob,
} from "../lib/taskForm";
import {
  formatDuration,
  formatSize,
  methodLabel,
  parseJobPathList,
  taskRecordStatusNames,
} from "./Home/homeUtils";
import { calcTaskDisplayPercent } from "./Home/taskRows";
import { formatTimestamp } from "../utils/date";
import type {
  DashboardJobItem,
  DashboardSpeedSample,
  FullSyncApproval,
  JobItem,
} from "../types";
import { SyncStatusTag } from "../components/SyncStatus";
import type { TaskEditorSavedResult } from "./TaskEditor";

const JobEditor = lazy(() => import("./TaskEditor"));

type StatusFilter =
  | "all"
  | "running"
  | "queued"
  | "approval"
  | "failed"
  | "disabled";

type InsightTab = "trend" | "quota";

const FAILED_STATUSES = [3, 5, 6, 7];
const SPEED_CHART_WIDTH = 600;
const SPEED_CHART_HEIGHT = 104;

function isJobFailed(job: DashboardJobItem): boolean {
  return Boolean(job.lastTask && FAILED_STATUSES.includes(job.lastTask.status));
}

function methodTagColor(method: number): "blue" | "violet" | "cyan" | "grey" {
  if (method === 0) return "blue";
  if (method === 1) return "violet";
  if (method === 2) return "cyan";
  return "grey";
}

function jobDotTone(job: DashboardJobItem, legacy = false): string {
  if (legacy) return "kpi-dot kpi-dot--danger";
  if (job.pendingApproval) return "kpi-dot kpi-dot--warning";
  if (job.running && job.paused) return "kpi-dot kpi-dot--warning";
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

function formatSampleClock(seconds?: number | null): string {
  const ts = Number(seconds || 0);
  if (ts <= 0) return "--:--:--";
  return dayjs.unix(ts).format("HH:mm:ss");
}

function formatScheduleHint(job: DashboardJobItem): string {
  if (job.isCron === 3) return job.enable === 1 ? "持续同步" : "持续同步已暂停";
  if (job.nextRunTime > 0) return `下次 ${formatShortTime(job.nextRunTime)}`;
  if (job.enable !== 1) return "已停用";
  if (job.isCron === 2) return "仅手动触发";
  if (job.isCron === 0 && job.interval > 0) return `每 ${job.interval} 分钟`;
  return "定时计划";
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
  const [searchParams] = useSearchParams();
  const chartGradientId = useId();
  const dashboard = useResource((signal) => api.dashboard(signal));
  const jobsList = useResource((signal) => api.jobMenu(signal));
  const engines = useResource((signal) => api.engines(signal));
  const action = useAction();

  const [searchInput, setSearchInput] = useState("");
  const [keyword, setKeyword] = useState("");
  const [storageFilter, setStorageFilter] = useState<string>(
    () => searchParams.get("storage") || "all",
  );
  const [trendRange, setTrendRange] = useState<7 | 30>(7);
  const [insightTab, setInsightTab] = useState<InsightTab>("trend");
  const [hoveredSampleIndex, setHoveredSampleIndex] = useState<number | null>(
    null,
  );
  const [localSpeedHistory, setLocalSpeedHistory] = useState<
    DashboardSpeedSample[]
  >([]);

  useEffect(() => {
    const sp = searchParams.get("storage");
    setStorageFilter(sp || "all");
  }, [searchParams]);

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

  const data = dashboard.data;
  const allJobs = data?.jobs || [];

  // Record rolling speed history on each dashboard snapshot so the live curve
  // works smoothly with both server-side history and legacy fixtures.
  useEffect(() => {
    if (!data) return;
    const rt = data.realtime;
    const fallbackSpeed = (data.jobs || []).reduce(
      (sum, j) => sum + (j.running && !j.paused ? safeSpeed(j.liveSpeed) : 0),
      0,
    );
    const currentSpeed = rt ? safeSpeed(rt.totalSpeed) : fallbackSpeed;
    const sampleTs = rt?.sampledAt || Math.floor(Date.now() / 1000);
    setLocalSpeedHistory((prev) =>
      mergeSpeedHistory(prev, rt?.speedHistory, sampleTs, currentSpeed),
    );
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
      }
    }, pollMs);
    return () => window.clearInterval(timer);
  }, [dashboard.refresh, hasLiveActivity]);

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

  const runningJobsList = useMemo(
    () =>
      allJobs
        .filter((j) => j.running && !isLegacyOpenListJob(j, engines.data))
        .sort((a, b) => {
          if (Boolean(a.paused) !== Boolean(b.paused)) {
            return a.paused ? 1 : -1;
          }
          return safeSpeed(b.liveSpeed) - safeSpeed(a.liveSpeed);
        }),
    [allJobs, engines.data],
  );

  const nextScheduledJob = useMemo(
    () => findNextScheduledJob(allJobs),
    [allJobs],
  );

  const watchingCount = useMemo(
    () => allJobs.filter((j) => j.watching && j.enable === 1).length,
    [allJobs],
  );

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

  const storageFilterOptions = useMemo(() => {
    const names = new Set<string>(["本地"]);
    for (const eng of engines.data || []) {
      for (const name of Object.keys(eng.remotes || {})) {
        if (name) names.add(name);
      }
    }
    for (const job of allJobs) {
      for (const p of [
        ...parseJobPathList(job.srcPath),
        ...parseJobPathList(job.dstPath),
      ]) {
        const seg = p.replace(/^\/+/, "").split("/")[0];
        if (seg) names.add(seg);
      }
    }
    return [
      { label: "全部存储", value: "all" },
      ...[...names]
        .sort((a, b) =>
          a === "本地" ? -1 : b === "本地" ? 1 : a.localeCompare(b),
        )
        .map((name) => ({
          label: name === "本地" ? "本地目录" : name,
          value: name,
        })),
    ];
  }, [engines.data, allJobs]);

  const filteredJobs = useMemo(() => {
    const kw = keyword.trim().toLowerCase();
    return allJobs.filter((job) => {
      if (storageFilter !== "all") {
        const paths = [
          ...parseJobPathList(job.srcPath),
          ...parseJobPathList(job.dstPath),
        ];
        const usesStorage = paths.some(
          (p) =>
            p.replace(/^\/+/, "").split("/")[0]?.toLowerCase() ===
            storageFilter.toLowerCase(),
        );
        if (!usesStorage) return false;
      }
      if (methodFilter !== "all" && String(job.method) !== methodFilter)
        return false;
      if (statusFilter === "running" && !job.running) return false;
      if (statusFilter === "queued" && !job.queued) return false;
      if (statusFilter === "approval" && !job.pendingApproval) return false;
      if (statusFilter === "failed" && !isJobFailed(job)) return false;
      if (statusFilter === "disabled" && job.enable === 1) return false;
      if (kw) {
        const hay =
          `${job.remark || ""} #${job.id} ${job.srcPath} ${job.dstPath}`.toLowerCase();
        if (!hay.includes(kw)) return false;
      }
      return true;
    });
  }, [allJobs, storageFilter, methodFilter, statusFilter, keyword]);

  const hasActiveFilters =
    Boolean(keyword.trim()) ||
    storageFilter !== "all" ||
    methodFilter !== "all" ||
    statusFilter !== "all";

  const resetFilters = () => {
    setSearchInput("");
    setKeyword("");
    setStorageFilter("all");
    setMethodFilter("all");
    setStatusFilter("all");
  };

  const selectedSet = useMemo(() => new Set(selectedIds), [selectedIds]);
  const legacyJobCount = useMemo(
    () => allJobs.filter((j) => isLegacyOpenListJob(j, engines.data)).length,
    [allJobs, engines.data],
  );
  const selectableVisibleJobs = useMemo(
    () =>
      filteredJobs.filter((j) => !isLegacyOpenListJob(j, engines.data)),
    [filteredJobs, engines.data],
  );
  const visibleAllSelected =
    selectableVisibleJobs.length > 0 &&
    selectableVisibleJobs.every((j) => selectedSet.has(j.id));
  const visibleSomeSelected =
    selectableVisibleJobs.some((j) => selectedSet.has(j.id)) &&
    !visibleAllSelected;

  const toggleSelectAllVisible = (checked: boolean) => {
    if (checked) {
      setSelectedIds(selectableVisibleJobs.map((j) => j.id));
    } else {
      const visible = new Set(selectableVisibleJobs.map((j) => j.id));
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

  const handleDeleteLegacy = (job: DashboardJobItem) => {
    const jobName = job.remark || `同步任务 #${job.id}`;
    confirmDelete(
      `删除旧版 OpenList 任务「${jobName}」？`,
      () =>
        action.run(async () => {
          await api.deleteJob(job.id);
          setSelectedIds((prev) => prev.filter((id) => id !== job.id));
          Toast.success("已删除旧版任务，请使用内置存储重新创建");
          await refreshAll(true);
        }),
      "此任务关联已移除的旧版 OpenList 引擎且历史执行记录将一并删除。",
    );
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

  const handleTogglePauseOne = (job: DashboardJobItem) => {
    if (!job.currentTaskId) return;
    const resuming = Boolean(job.paused);
    void action.run(async () => {
      try {
        await api.taskAction(job.currentTaskId, resuming ? "resume" : "pause");
        Toast.success(resuming ? "已继续传输" : "已暂停传输");
        await refreshAll(true);
      } catch (err) {
        errorToast(err);
      }
    });
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

  const liveCardTargetFilter: StatusFilter = kpi?.runningJobs
    ? "running"
    : kpi?.queuedJobs
      ? "queued"
      : kpi?.pendingApprovals
        ? "approval"
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

  const emptyJobAction =
    !engines.loading && !engines.error && (engines.data?.length ?? 0) === 0 ? (
      <div className="empty-guide">
        <span className="muted">
          存储服务尚未就绪，请前往存储管理检查本地目录或添加远程存储
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
              onClick={() =>
                runBatch("run_all", undefined, "运行全部已启用任务")
              }
            >
              全部运行
            </Button>
            <Button
              type="danger"
              theme="light"
              icon={<IconStop aria-hidden="true" />}
              disabled={
                action.busy ||
                !((kpi?.runningJobs || 0) + (kpi?.queuedJobs || 0))
              }
              onClick={() =>
                runBatch("stop_all", undefined, "停止全部运行中任务")
              }
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
            {legacyJobCount > 0 && (
              <InlineNotice
                role="alert"
                icon={<IconAlertTriangle aria-hidden="true" size="small" />}
                message={`检测到 ${legacyJobCount} 个旧版 OpenList 同步任务已自动停用。当前版本已移除 OpenList 引擎，请删除旧任务后在「存储管理」接入对应存储并重新创建。`}
              />
            )}

            {data.pendingApprovals.length > 0 && (
              <div className="approval-alert-banner" role="alert">
                <div className="approval-alert-head">
                  <span className="approval-alert-icon" aria-hidden="true">
                    <IconAlertTriangle />
                  </span>
                  <div className="approval-alert-copy">
                    <strong>
                      有 {data.pendingApprovals.length}{" "}
                      个全量同步任务存在待确认删除项
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
                    const seedApproval: FullSyncApproval =
                      matchedJob?.pendingApproval
                        ? {
                            ...matchedJob.pendingApproval,
                            jobId: item.jobId,
                            jobName: item.jobName,
                            taskId:
                              matchedJob.pendingApproval.taskId || item.taskId,
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

            {/* 4 Core KPI Cards */}
            <div className="dashboard-kpi-grid">
              {/* Card 1: Real-time Speed */}
              <button
                type="button"
                className={[
                  "dashboard-kpi-card",
                  realtime.isAllPaused ? "is-warning" : "",
                  statusFilter === liveCardTargetFilter ? "is-active" : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                onClick={() =>
                  setStatusFilter((prev) =>
                    prev === liveCardTargetFilter
                      ? "all"
                      : liveCardTargetFilter,
                  )
                }
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
                <div className="dashboard-kpi-foot">
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
                        <span>
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
                  ) : (
                    <span className="dashboard-kpi-stat">
                      <span className="kpi-dot" aria-hidden="true" />
                      当前空闲
                      {realtime.globalBwLimit
                        ? ` · 限速 ${realtime.globalBwLimit}`
                        : " · 无活跃传输"}
                    </span>
                  )}
                </div>
              </button>

              {/* Card 2: Sync Jobs Inventory */}
              <button
                type="button"
                className={[
                  "dashboard-kpi-card",
                  statusFilter === "all" && !hasActiveFilters ? "" : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
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
                  <span className="dashboard-kpi-value">
                    {kpi.enabledJobs}
                  </span>
                  <span className="dashboard-kpi-denom">
                    / {kpi.totalJobs}
                  </span>
                  {kpi.pendingApprovals > 0 && (
                    <span className="dashboard-kpi-pill is-warning">
                      待审批 {kpi.pendingApprovals}
                    </span>
                  )}
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
                      className="kpi-dot kpi-dot--primary"
                      aria-hidden="true"
                    />
                    已启用 {kpi.enabledJobs}
                  </span>
                  <span className="dashboard-kpi-stat">
                    <span className="kpi-dot" aria-hidden="true" />
                    已停用 {disabledJobsCount}
                  </span>
                  {statusCounts.failed > 0 && (
                    <span className="dashboard-kpi-stat is-danger">
                      失败 {statusCounts.failed}
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
                onClick={() => navigate("/engines")}
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
                          className={`dashboard-kpi-pill${recent.failRuns > 0 ? " is-warning" : " is-success"}`}
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
                        <span className="dashboard-kpi-stat is-success">
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
                        <span className="dashboard-live-bw-chip">
                          {realtime.globalBwLimit
                            ? `全局限速 ${realtime.globalBwLimit}`
                            : "全局不限速"}
                        </span>
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
                          : "下次计划"}
                      </span>
                      <strong className="dashboard-live-stat-val">
                        {realtime.isTransferring || realtime.isAllPaused
                          ? realtime.totalBytes > 0
                            ? `${formatSize(realtime.totalDoneBytes)} / ${formatSize(realtime.totalBytes)}`
                            : "扫描统计中"
                          : nextScheduledJob
                            ? formatShortTime(nextScheduledJob.nextRunTime)
                            : "无定时计划"}
                      </strong>
                    </div>
                    <div className="dashboard-live-stat-box">
                      <span className="dashboard-live-stat-label muted">
                        {realtime.isTransferring || realtime.isAllPaused
                          ? "预计剩余"
                          : "持续监听"}
                      </span>
                      <strong className="dashboard-live-stat-val">
                        {realtime.isTransferring
                          ? realtime.estimatedRemainTime > 0
                            ? formatDuration(realtime.estimatedRemainTime)
                            : "计算中"
                          : realtime.isAllPaused
                            ? "已暂停"
                            : watchingCount > 0
                              ? `${watchingCount} 个任务`
                              : "未开启"}
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
                        y1={SPEED_CHART_HEIGHT / 2}
                        x2={SPEED_CHART_WIDTH}
                        y2={SPEED_CHART_HEIGHT / 2}
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
                            key={`${pt.ts}-${idx}`}
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
                          onClick={() => setStatusFilter("running")}
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
                            <div className="dashboard-live-task-actions">
                              {job.currentTaskId > 0 && (
                                <IconButton
                                  size="small"
                                  label={job.paused ? "继续传输" : "暂停传输"}
                                  icon={
                                    job.paused ? (
                                      <IconPlay aria-hidden="true" />
                                    ) : (
                                      <IconPause aria-hidden="true" />
                                    )
                                  }
                                  disabled={action.busy}
                                  onClick={() => handleTogglePauseOne(job)}
                                />
                              )}
                              <IconButton
                                size="small"
                                label="停止任务"
                                danger
                                icon={<IconStop aria-hidden="true" />}
                                disabled={action.busy}
                                onClick={() => handleStopOne(job)}
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
                      <IconClockStroked
                        size="small"
                        className="muted"
                        aria-hidden="true"
                      />
                      {nextScheduledJob ? (
                        <button
                          type="button"
                          className="dashboard-live-idle-link"
                          onClick={() => openJobDetail(nextScheduledJob)}
                        >
                          <span>
                            下次计划：
                            <strong>
                              {nextScheduledJob.remark ||
                                `同步任务 #${nextScheduledJob.id}`}
                            </strong>
                          </span>
                          <span className="muted">
                            {formatShortTime(nextScheduledJob.nextRunTime)}
                          </span>
                        </button>
                      ) : (
                        <span className="muted">
                          暂无待触发的定时计划，可点击「全部运行」或单任务启动
                        </span>
                      )}
                    </div>
                    <div className="dashboard-live-idle-item">
                      {statusCounts.failed > 0 ? (
                        <button
                          type="button"
                          className="dashboard-live-idle-alert is-danger"
                          onClick={() => setStatusFilter("failed")}
                        >
                          <IconAlertTriangle size="small" aria-hidden="true" />
                          <span>
                            {statusCounts.failed} 个任务最近执行异常，点击排查
                          </span>
                        </button>
                      ) : (
                        <span className="dashboard-live-idle-ok">
                          <IconTickCircle size="small" aria-hidden="true" />
                          <span>所有已启用任务运行健康</span>
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
                        onClick={() => navigate("/engines")}
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

            {/* All Sync Tasks List */}
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
                  disabled={!selectableVisibleJobs.length}
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
                  ariaLabel="搜索任务名称或路径"
                  placeholder="搜索任务名 / 路径"
                  value={searchInput}
                  onChange={(next) => {
                    setSearchInput(next);
                    setKeyword(next);
                  }}
                  onSearch={setKeyword}
                />
                <Select
                  aria-label="按存储筛选"
                  value={storageFilter}
                  onChange={(v) => setStorageFilter(String(v))}
                  optionList={storageFilterOptions}
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
                    { label: "双向同步", value: "3" },
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
                      onClick={() =>
                        runBatch("enable", selectedIds, "批量启用")
                      }
                    >
                      批量启用
                    </Button>
                    <Button
                      size="small"
                      type="tertiary"
                      disabled={action.busy}
                      onClick={() =>
                        runBatch("disable", selectedIds, "批量停用")
                      }
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
                    const displaySiblings = [...srcList, ...dstList];
                    const displaySrcList = formatDisplayPathList(
                      srcList,
                      displaySiblings,
                    );
                    const displayDstList = formatDisplayPathList(
                      dstList,
                      displaySiblings,
                    );
                    const srcSummary =
                      (displaySrcList[0] || "—") +
                      (displaySrcList.length > 1
                        ? ` 等 ${displaySrcList.length} 项`
                        : "");
                    const dstSummary =
                      (displayDstList[0] || "—") +
                      (displayDstList.length > 1
                        ? ` 等 ${displayDstList.length} 项`
                        : "");
                    const fullRouteTitle = `${displaySrcList.join(", ") || "—"} → ${displayDstList.join(", ") || "—"}`;
                    const isSelected = selectedSet.has(job.id);
                    const legacy = isLegacyOpenListJob(job, engines.data);
                    const failed = isJobFailed(job);
                    const m = computeJobLiveMetrics(job);

                    const rowClassName = [
                      "dashboard-job-row",
                      isSelected ? "is-selected" : "",
                      legacy
                        ? "is-failed"
                        : job.pendingApproval
                          ? "is-approval"
                          : job.running
                            ? "is-running"
                            : failed
                              ? "is-failed"
                              : "",
                      legacy || (job.enable !== 1 && !job.running)
                        ? "is-disabled"
                        : "",
                    ]
                      .filter(Boolean)
                      .join(" ");

                    return (
                      <div key={job.id} className={rowClassName}>
                        <div className="dashboard-job-check">
                          <Checkbox
                            aria-label={
                              legacy
                                ? `${jobName}（旧版已停用，请删除重建）`
                                : `选择 ${jobName}`
                            }
                            checked={!legacy && isSelected}
                            disabled={legacy}
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
                                className={jobDotTone(job, legacy)}
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
                            {legacy && (
                              <Tag size="small" type="solid" color="red">
                                旧版已停用
                              </Tag>
                            )}
                            {!legacy && job.pendingApproval && (
                              <Tag size="small" type="solid" color="orange">
                                {job.pendingApproval.status === "executing"
                                  ? "正在删除"
                                  : "待确认删除"}
                              </Tag>
                            )}
                            {!legacy && job.running && job.paused && (
                              <Tag size="small" type="light" color="orange">
                                已暂停
                              </Tag>
                            )}
                            {!legacy && job.queued && (
                              <Tag size="small" type="light" color="cyan">
                                排队等待中
                              </Tag>
                            )}
                            {!legacy && job.syncStatus && (
                              <SyncStatusTag status={job.syncStatus} />
                            )}
                            {!legacy && !job.syncStatus && job.watching && (
                              <Tag size="small" type="light" color="green">
                                实时监听中
                              </Tag>
                            )}
                            {!legacy && job.checkRunning && (
                              <Tag size="small" type="light" color="blue">
                                后台校验中
                              </Tag>
                            )}
                            {!legacy && job.enable !== 1 && (
                              <Tag size="small" type="light" color="grey">
                                已停用
                              </Tag>
                            )}
                            {!legacy && job.windowEnable === 1 && (
                              <Tag size="small" type="light" color="violet">
                                窗口 {job.windowStart}–{job.windowEnd}
                              </Tag>
                            )}
                          </div>

                          <div className="dashboard-job-meta">
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
                            !legacy && !job.running && job.pendingApproval
                              ? openApprovalModal(
                                  job.id,
                                  jobName,
                                  job.pendingApproval,
                                )
                              : openJobDetail(job)
                          }
                        >
                          {legacy ? (
                            <div className="dashboard-job-approval-note">
                              <span className="dashboard-job-approval-title">
                                <IconAlertTriangle
                                  size="small"
                                  aria-hidden="true"
                                />
                                旧版 OpenList 任务已停用
                              </span>
                              <span className="dashboard-job-approval-action">
                                已不再支持，请删除后重新创建
                              </span>
                            </div>
                          ) : job.running ? (
                            <div className="dashboard-job-progress">
                              <div className="dashboard-job-progress-head">
                                <span className="dashboard-job-progress-label">
                                  {m.progressLabel}
                                </span>
                                <span className="dashboard-job-progress-right">
                                  {!job.paused && m.liveSpeed > 0 && (
                                    <span className="dashboard-job-speed">
                                      {formatSpeed(m.liveSpeed)}
                                    </span>
                                  )}
                                  <span className="dashboard-job-progress-pct">
                                    {m.livePct}%
                                  </span>
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
                              <div className="dashboard-job-progress-sub">
                                {m.liveTotal > 0 || m.liveTotalSize > 0 ? (
                                  <>
                                    {m.liveTotalSize > 0 && (
                                      <span>
                                        {formatSize(m.liveDoneSize)} /{" "}
                                        {formatSize(m.liveTotalSize)}
                                      </span>
                                    )}
                                    {m.liveTotal > 0 && (
                                      <span>
                                        已完成 {job.liveNum?.success ?? 0} /{" "}
                                        {m.liveTotal} 项
                                      </span>
                                    )}
                                    {!job.paused && m.liveRemainTime > 0 && (
                                      <span>
                                        剩余 {formatDuration(m.liveRemainTime)}
                                      </span>
                                    )}
                                    {!m.liveScanFinish &&
                                      m.scanTotalFiles > 0 &&
                                      m.scanCheckedFiles > 0 && (
                                        <span>
                                          已核对 {m.scanCheckedFiles} /{" "}
                                          {m.scanTotalFiles}
                                        </span>
                                      )}
                                    {(job.liveNum?.fail ?? 0) > 0 && (
                                      <span className="inline-error">
                                        失败 {job.liveNum?.fail}
                                      </span>
                                    )}
                                  </>
                                ) : !m.liveScanFinish && m.liveScan ? (
                                  <span>
                                    {m.scanTotalFiles > 0 &&
                                    m.scanCheckedFiles > 0
                                      ? `已核对 ${m.scanCheckedFiles} / ${m.scanTotalFiles} 个文件`
                                      : `已扫描 ${m.liveScan.scannedDirs} 个目录${
                                          m.scanTotalFiles > 0
                                            ? ` · ${m.scanTotalFiles} 个文件`
                                            : ""
                                        }`}
                                  </span>
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
                          {legacy ? (
                            <IconButton
                              label="删除旧版任务"
                              icon={<IconDeleteStroked aria-hidden="true" />}
                              danger
                              disabled={action.busy}
                              onClick={() => handleDeleteLegacy(job)}
                            />
                          ) : (
                            <>
                              <IconButton
                                label="模拟运行预览"
                                icon={
                                  <IconEyeOpenedStroked aria-hidden="true" />
                                }
                                disabled={action.busy}
                                onClick={() =>
                                  setDryRunJob({ id: job.id, name: jobName })
                                }
                              />
                              {job.running && job.currentTaskId > 0 && (
                                <IconButton
                                  label={job.paused ? "继续传输" : "暂停传输"}
                                  icon={
                                    job.paused ? (
                                      <IconPlay aria-hidden="true" />
                                    ) : (
                                      <IconPause aria-hidden="true" />
                                    )
                                  }
                                  disabled={action.busy}
                                  onClick={() => handleTogglePauseOne(job)}
                                />
                              )}
                              <IconButton
                                label={
                                  job.running || job.queued
                                    ? "停止任务"
                                    : "启动任务"
                                }
                                icon={
                                  job.running || job.queued ? (
                                    <IconStop aria-hidden="true" />
                                  ) : (
                                    <IconPlay aria-hidden="true" />
                                  )
                                }
                                danger={job.running || job.queued}
                                disabled={
                                  action.busy ||
                                  (!job.running &&
                                    !job.queued &&
                                    job.enable !== 1)
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
                            </>
                          )}
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
          onApprove={(selectedPaths) =>
            action.run(async () => {
              const taskId = approvalModal.approval.taskId;
              if (!taskId) return;
              try {
                await api.taskAction(
                  taskId,
                  "approve",
                  undefined,
                  selectedPaths && selectedPaths.length > 0
                    ? { paths: selectedPaths }
                    : undefined,
                );
                setApprovalModal(null);
                Toast.success(
                  selectedPaths && selectedPaths.length > 0
                    ? `已提交后台校验并删除选中的 ${selectedPaths.length} 项`
                    : "已提交后台校验并执行删除",
                );
                await refreshAll(true);
              } catch (err) {
                errorToast(err);
              }
            })
          }
          onExclude={(selectedPaths) =>
            action.run(async () => {
              const taskId = approvalModal.approval.taskId;
              if (!taskId || !selectedPaths.length) return;
              try {
                const res = await api.excludeApprovalPaths(
                  taskId,
                  selectedPaths,
                );
                setApprovalModal(null);
                Toast.success(`已将 ${res.excludedCount} 项加入任务排除规则`);
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
