import { useId, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconActivity,
  IconAlertTriangle,
  IconApps,
  IconChevronRight,
  IconClockStroked,
  IconCloudStroked,
  IconCopy,
  IconDeleteStroked,
  IconEditStroked,
  IconFolderStroked,
  IconHistory,
  IconLockStroked,
  IconPause,
  IconPlay,
  IconPlusStroked,
  IconRefresh,
  IconServerStroked,
  IconStop,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  ActionMenu,
  DataPager,
  EmptyState,
  IconButton,
  InlineNotice,
  SearchFilterInput,
  SectionCard,
  confirmAction,
  errorToast,
  type MenuAction,
} from "../components/common";
import {
  formatMinutesShort,
  formatRelativeSyncTime,
  formatSecondsShort,
  formatSyncNextAction,
  getContinuousSyncMeta,
  isSyncStatusError,
  resolveSyncError,
  resolveEffectiveSyncStatus,
  syncStatusTone,
  SyncStatusTag,
} from "../components/SyncStatus";
import { describeBwLimit } from "../lib/bwLimit";
import { formatSpeed, safeSpeed } from "../lib/dashboardMetrics";
import { useAction, useResource } from "../lib/hooks";
import { remoteTypeLabels } from "../lib/storageTypes";
import {
  formatDisplayPath,
  formatDisplayPathList,
} from "../lib/syncPathPreview";
import {
  getJobConnectionScope,
  getJobMountNames,
  isLegacyOpenListJob,
  jobHasCryptRemote,
  requiresFirstMirrorPreview,
  resolveRemoteChain,
  type TaskTab,
} from "../lib/taskForm";
import {
  formatDuration,
  formatSchedulePlan,
  formatSize,
  getJobName,
  parseJobPathList,
  taskItemStatusNames,
  taskTypeNames,
} from "./Home/homeUtils";
import { calcTaskDisplayPercent } from "./Home/taskRows";
import { formatTimestamp } from "../utils/date";
import type {
  AlistItem,
  DashboardJobItem,
  EngineStorageStatus,
  JobItem,
  NativeAboutInfo,
  NativeLocalRoot,
  NativeRemoteItem,
  NativeStorageTestResult,
} from "../types";

const statusOptions = [
  { label: "全部状态", value: "all" },
  { label: "同步中", value: "running" },
  { label: "已同步", value: "synced" },
  { label: "排队中", value: "queued" },
  { label: "存在冲突", value: "conflict" },
  { label: "同步异常", value: "failed" },
  { label: "已暂停", value: "disabled" },
] as const;

const directionOptions = [
  { label: "全部方向", value: "all" },
  { label: "单向同步", value: "oneway" },
  { label: "双向同步", value: "bidirectional" },
  { label: "已开同步删除", value: "sync_delete" },
];

type StatusKey = (typeof statusOptions)[number]["value"];
type BatchAction = "run" | "stop" | "enable" | "disable";
type WorkbenchTab = "tasks" | "history" | "settings";
type HistoryFilter = "all" | "success" | "failed" | "delete";

const failedTaskStatuses = [3, 5, 6, 7];

interface SyncOverviewProps {
  jobs: JobItem[];
  liveJobs?: DashboardJobItem[];
  remotes: NativeRemoteItem[];
  localRoots?: NativeLocalRoot[];
  engines: AlistItem[];
  busy?: boolean;
  onAdd: (remote?: string) => void;
  taskHref: (jobId: number, tab: TaskTab, intent?: "mirrorPreview" | "conflicts") => string;
  onEdit?: (job: JobItem) => void;
  onClone?: (job: JobItem) => void;
  onToggle?: (job: JobItem) => void;
  onRun?: (job: JobItem) => void;
  onStop?: (job: JobItem, liveTaskId?: number) => void;
  onTogglePauseTransfer?: (taskId: number, paused: boolean) => void;
  onDelete?: (job: JobItem) => void;
  onCheck?: (job: JobItem) => void;
  onRepair: (remote: NativeRemoteItem) => void;
  onAddConnection?: () => void;
  onBrowseRemote?: (remote: NativeRemoteItem) => void;
  onLocalFolders: () => void;
  onNavigateEngines?: () => void;
  onJobsChanged: () => Promise<void>;
  onStorageChanged?: () => void;
}

interface JobClassification {
  running: boolean;
  synced: boolean;
  queued: boolean;
  conflict: boolean;
  failed: boolean;
  disabled: boolean;
}

function classifyJobStatus(job: JobItem): JobClassification {
  const sync = resolveEffectiveSyncStatus(job);
  const failed =
    isSyncStatusError(job.syncStatus) ||
    failedTaskStatuses.includes(job.lastStatus ?? 0);
  const running = Boolean(
    job.running || sync.state === "syncing" || sync.state === "scanning",
  );
  const synced =
    !job.running &&
    job.enable === 1 &&
    !failed &&
    (sync.state === "synced" || sync.state === "monitoring");
  return {
    running,
    synced,
    queued: Boolean(job.queued),
    conflict: (sync.conflictCount ?? 0) > 0,
    failed,
    disabled: job.enable !== 1,
  };
}

function matchesStatusFilter(
  cls: JobClassification,
  status: StatusKey,
): boolean {
  switch (status) {
    case "running":
      return cls.running;
    case "synced":
      return cls.synced;
    case "queued":
      return cls.queued;
    case "conflict":
      return cls.conflict;
    case "failed":
      return cls.failed;
    case "disabled":
      return cls.disabled;
    default:
      return true;
  }
}

function resolveRemoteHealthTone(
  remote: NativeRemoteItem,
  healthMap: Map<string, EngineStorageStatus>,
): { dot: "online" | "offline" | "unknown"; label: string } {
  const st = healthMap.get(remote.name.toLowerCase());
  if (!st) return { dot: "unknown", label: "未检测" };
  if (st.disabled) return { dot: "offline", label: "已停用" };
  const status = (st.status || "").trim().toLowerCase();
  if (status === "work" || status === "ok") {
    return { dot: "online", label: "在线" };
  }
  return { dot: "offline", label: st.status || "异常" };
}

function isStorageHealthy(st?: EngineStorageStatus): boolean {
  if (!st || st.disabled) return true;
  const s = (st.status || "").trim().toLowerCase();
  return s === "" || s === "work" || s === "ok";
}

function computeLiveMetrics(liveJob: DashboardJobItem) {
  const liveDoneSize = Math.max(0, Number(liveJob.liveDoneSize || 0));
  const liveTotalSize = Math.max(0, Number(liveJob.liveTotalSize || 0));
  const liveScanFinish = liveJob.liveScanFinish !== false;
  const liveScan = liveJob.liveScan ?? null;
  const liveSpeed = liveJob.paused ? 0 : safeSpeed(liveJob.liveSpeed);
  const liveRemainTime = liveJob.paused
    ? 0
    : Math.max(0, Number(liveJob.liveRemainTime || 0));
  const liveTotal = liveJob.liveNum
    ? (liveJob.liveNum.wait || 0) +
      (liveJob.liveNum.running || 0) +
      (liveJob.liveNum.success || 0) +
      (liveJob.liveNum.fail || 0) +
      (liveJob.liveNum.other || 0)
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
        num: liveJob.liveNum,
        scanFinish: liveScanFinish,
        fallbackPercent: liveJob.liveProgress,
      });
  const progressLabel = liveJob.paused
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

function formatRouteSummary(
  paths: string[],
  siblings: string[],
): { display: string; full: string } {
  const formatted = formatDisplayPathList(paths, siblings);
  const full = formatted.join("、") || "—";
  if (formatted.length <= 2) return { display: full, full };
  return {
    display: `${formatted[0]}、${formatted[1]} 等 ${formatted.length} 项`,
    full,
  };
}

function rowToneClass({
  legacy,
  running,
  paused,
  failed,
  warning,
  disabled,
  selected,
}: {
  legacy: boolean;
  running: boolean;
  paused: boolean;
  failed: boolean;
  warning: boolean;
  disabled: boolean;
  selected: boolean;
}): string {
  return [
    "sync-overview-row",
    running && !paused ? "is-running" : "",
    running && paused ? "is-paused-transfer" : "",
    !running && (failed || legacy) ? "is-failed" : "",
    !running && !failed && !legacy && warning ? "is-warning" : "",
    disabled ? "is-disabled" : "",
    selected ? "is-selected" : "",
  ]
    .filter(Boolean)
    .join(" ");
}

function dotToneClass(
  job: JobItem,
  legacy: boolean,
  effectivePaused: boolean,
): string {
  if (legacy) return "kpi-dot kpi-dot--danger";
  if (job.running && effectivePaused) return "kpi-dot kpi-dot--warning";
  if (job.running) return "kpi-dot kpi-dot--primary kpi-dot--pulse";
  if (job.queued) return "kpi-dot kpi-dot--primary";
  const status = resolveEffectiveSyncStatus(job);
  const tone = syncStatusTone(status.state);
  if (tone === "danger") return "kpi-dot kpi-dot--danger";
  if (tone === "warning") return "kpi-dot kpi-dot--warning";
  if (tone === "success") return "kpi-dot kpi-dot--success";
  if (tone === "primary") {
    return status.state === "scanning" || status.state === "syncing"
      ? "kpi-dot kpi-dot--primary kpi-dot--pulse"
      : "kpi-dot kpi-dot--primary";
  }
  return "kpi-dot";
}

interface SyncGroup {
  key: string;
  kind: "remote" | "local" | "custom";
  title: string;
  jobs: JobItem[];
  remote?: NativeRemoteItem;
}

export function SyncOverview({
  jobs,
  liveJobs,
  remotes,
  localRoots = [],
  engines,
  busy = false,
  onAdd,
  taskHref,
  onEdit,
  onClone,
  onToggle,
  onRun,
  onStop,
  onTogglePauseTransfer,
  onDelete,
  onCheck,
  onRepair,
  onAddConnection,
  onBrowseRemote,
  onLocalFolders,
  onNavigateEngines,
  onJobsChanged,
}: SyncOverviewProps) {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const filterId = useId();
  const [keyword, setKeyword] = useState("");
  const [selected, setSelected] = useState<number[]>([]);
  const [historyFilter, setHistoryFilter] = useState<HistoryFilter>("all");
  const [historyKeyword, setHistoryKeyword] = useState("");
  const [historyPage, setHistoryPage] = useState(1);
  const [testingName, setTestingName] = useState<string | null>(null);
  const [testResults, setTestResults] = useState<
    Record<string, NativeStorageTestResult>
  >({});
  const [aboutLoadingName, setAboutLoadingName] = useState<string | null>(null);
  const [aboutResults, setAboutResults] = useState<
    Record<string, NativeAboutInfo>
  >({});
  const action = useAction();

  const storage = params.get("storage") || "all";
  const rawView = params.get("view");
  const workbenchTab: WorkbenchTab = rawView === "history" || rawView === "settings"
    ? rawView
    : "tasks";
  const status: StatusKey =
    statusOptions.find(({ value }) => value === params.get("status"))?.value ??
    "all";
  const direction =
    directionOptions.find(({ value }) => value === params.get("direction"))
      ?.value ?? "all";
  const search = keyword.trim().toLowerCase();
  const otherFiltersActive = Boolean(
    search || status !== "all" || direction !== "all",
  );
  const filtered = Boolean(otherFiltersActive || storage !== "all");
  const allBusy = busy || action.busy;

  const setFilter = (key: string, value: string) => {
    setSelected([]);
    const next = new URLSearchParams(params);
    if (value === "all") next.delete(key);
    else next.set(key, value);
    if (key === "storage") {
      next.delete("status");
      next.delete("direction");
      next.delete("jobId");
      next.delete("tab");
      next.delete("mirrorPreview");
      next.delete("conflicts");
      setKeyword("");
      setHistoryPage(1);
    }
    setParams(next, { replace: key !== "storage" });
  };

  const resetFilters = () => {
    setKeyword("");
    setSelected([]);
    setHistoryPage(1);
    const next = new URLSearchParams(params);
    next.delete("status");
    next.delete("direction");
    setParams(next, { replace: true });
  };
  const selectView = (view: string) => {
    const next = new URLSearchParams(params);
    if (view === "tasks") next.delete("view");
    else next.set("view", view);
    setParams(next);
  };

  const liveJobMap = useMemo(() => {
    const map = new Map<number, DashboardJobItem>();
    for (const item of liveJobs || []) map.set(item.id, item);
    return map;
  }, [liveJobs]);

  const healthMap = useMemo(() => {
    const map = new Map<string, EngineStorageStatus>();
    const nativeEngine =
      engines.find((e) => e.kind === "rclone" || e.url === "rclone://native") ??
      engines[0];
    for (const st of nativeEngine?.health?.storages || []) {
      const mount = st.mountPath.replace(/^\/+/, "").split("/")[0]?.trim();
      if (mount) map.set(mount.toLowerCase(), st);
      map.set(st.mountPath.toLowerCase(), st);
    }
    return map;
  }, [engines]);

  const jobsWithMounts = useMemo(() => {
    return jobs.map((job) => {
      const mounts = getJobMountNames(job);
      return {
        job,
        mounts: mounts.map((name) => name.toLowerCase()),
        group: getJobConnectionScope(job, remotes, engines, mounts).toLowerCase(),
      };
    });
  }, [jobs, remotes, engines]);

  const storageMatches = (name: string) =>
    name.toLowerCase() === storage.toLowerCase();
  const localScope = storage === "__local__" || storage === "本地";
  const scopedJobs = jobsWithMounts.filter(({ mounts, group }) => {
    if (storage === "all") return true;
    if (localScope) return group === "__local__";
    if (storage === "__other__") return group === "__other__";
    return mounts.some(storageMatches);
  });

  const statusCounts = (() => {
    const counts = {
      all: scopedJobs.length,
      running: 0,
      synced: 0,
      queued: 0,
      conflict: 0,
      failed: 0,
      disabled: 0,
    };
    for (const { job } of scopedJobs) {
      const cls = classifyJobStatus(job);
      if (cls.running) counts.running += 1;
      if (cls.synced) counts.synced += 1;
      if (cls.queued) counts.queued += 1;
      if (cls.conflict) counts.conflict += 1;
      if (cls.failed) counts.failed += 1;
      if (cls.disabled) counts.disabled += 1;
    }
    return counts;
  })();

  const storageHistoryParam =
    storage === "all" || storage === "__other__"
      ? undefined
      : localScope
        ? "__local__"
        : storage;
  const historyJobIds = storage === "__other__"
    ? scopedJobs.map(({ job }) => job.id)
    : undefined;

  const recentHistory = useResource(
    (signal) =>
      workbenchTab === "history" && historyJobIds?.length !== 0
        ? api.recentTaskItems(
            {
              pageNum: historyPage,
              pageSize: 20,
              storage: storageHistoryParam,
              jobIds: historyJobIds,
              status: historyFilter === "success" ? 2 : undefined,
              type: historyFilter === "delete" ? 1 : undefined,
              hasError: historyFilter === "failed" ? 1 : undefined,
              keyword: historyKeyword.trim() || undefined,
            },
            signal,
          )
        : Promise.resolve(null),
    [
      workbenchTab,
      historyPage,
      storageHistoryParam,
      historyJobIds,
      historyFilter,
      historyKeyword,
    ],
  );

  // Counts across all jobs for the left connection rail
  const connectionStats = useMemo(() => {
    const byRemote = new Map<
      string,
      { total: number; running: number; errors: number; conflicts: number }
    >();
    for (const remote of remotes) {
      byRemote.set(remote.name.toLowerCase(), {
        total: 0,
        running: 0,
        errors: 0,
        conflicts: 0,
      });
    }
    const localStats = { total: 0, running: 0, errors: 0, conflicts: 0 };
    const otherStats = { total: 0, running: 0, errors: 0, conflicts: 0 };

    for (const { job, mounts, group } of jobsWithMounts) {
      const legacy = isLegacyOpenListJob(job, engines);
      const sync = resolveEffectiveSyncStatus(job);
      const hasErr =
        !legacy &&
        (isSyncStatusError(job.syncStatus) ||
          failedTaskStatuses.includes(job.lastStatus ?? 0));
      const isRun = Boolean(job.running && !job.paused);
      const conflicts = sync.conflictCount ?? 0;

      if (group === "__local__") {
        localStats.total += 1;
        if (isRun) localStats.running += 1;
        if (hasErr) localStats.errors += 1;
        localStats.conflicts += conflicts;
        continue;
      }

      if (!legacy) {
        for (const r of mounts) {
          const entry = byRemote.get(r);
          if (!entry) continue;
          entry.total += 1;
          if (isRun) entry.running += 1;
          if (hasErr) entry.errors += 1;
          entry.conflicts += conflicts;
        }
      }

      if (group === "__other__") {
        otherStats.total += 1;
        if (isRun) otherStats.running += 1;
        if (hasErr) otherStats.errors += 1;
        otherStats.conflicts += conflicts;
      }
    }

    return { byRemote, localStats, otherStats };
  }, [jobsWithMounts, remotes, engines]);

  const visible = scopedJobs.filter(({ job }) => {
    const searchable = `${getJobName(job)} #${job.id} ${job.srcPath} ${job.dstPath}`;
    if (search && !searchable.toLowerCase().includes(search)) return false;

    if (direction !== "all") {
      const meta = getContinuousSyncMeta(job);
      const matches =
        direction === "sync_delete"
          ? meta.syncDelete && meta.directionKey !== "bidirectional"
          : meta.directionKey === direction;
      if (!matches) return false;
    }
    return matchesStatusFilter(classifyJobStatus(job), status);
  });

  const selectable = visible
    .map(({ job }) => job)
    .filter((job) => !isLegacyOpenListJob(job, engines));
  const selectedJobs = selectable.filter((job) => selected.includes(job.id));
  const selectedIds = selectedJobs.map((job) => job.id);
  const fullySelected =
    selectable.length > 0 && selectable.length === selectedIds.length;
  const batchTargets: Record<BatchAction, number[]> = {
    run: [],
    stop: [],
    enable: [],
    disable: [],
  };
  for (const job of selectedIds.length ? selectedJobs : selectable) {
    if (job.running || job.queued) batchTargets.stop.push(job.id);
    else if (job.enable === 1) batchTargets.run.push(job.id);
    if (job.enable === 1 && job.isCron !== 2) batchTargets.disable.push(job.id);
    if (job.enable !== 1 && !requiresFirstMirrorPreview(job)) {
      batchTargets.enable.push(job.id);
    }
  }
  const allConnectionsBatch = !filtered && selectedIds.length === 0;
  const batchLabel = allConnectionsBatch ? "全部" : "当前列表";
  const connectionName = storageHistoryParam === "__local__"
    ? "本地目录互备"
    : storage === "__other__"
      ? "其他 / 跨端"
      : storage;
  const batchScope = selectedIds.length
    ? "已勾选任务"
    : allConnectionsBatch
      ? "全部连接"
      : storage === "all"
        ? "当前筛选结果"
        : `「${connectionName}」的当前列表`;

  const runBatch = (operation: BatchAction, label: string) => {
    const ids = batchTargets[operation];
    if (allBusy || ids.length === 0) return;
    const execute = () =>
      action.run(async () => {
        try {
          const result = await api.batchJobAction(operation, ids);
          if (result.failed > 0) {
            Toast.warning(
              `${label}：已提交 ${result.succeeded} 个，失败 ${result.failed} 个`,
            );
          } else {
            Toast.success(`已提交${label}（${result.succeeded} 个任务）`);
          }
          setSelected([]);
          await onJobsChanged();
        } catch (err) {
          errorToast(err);
        }
      });
    const target = `${batchScope}中的 ${ids.length} 个任务`;
    const confirmation = operation === "stop"
      ? `将中止${target}正在运行或排队的批次。已完成的文件和任务启用状态会保留。`
      : operation === "disable"
        ? `将暂停${target}的自动同步，并中止它们正在运行或排队的批次。`
        : operation === "run" && allConnectionsBatch
          ? `将立即触发${target}。各任务按已配置的同步策略运行。`
          : null;
    if (confirmation) {
      confirmAction({
        title: `确认执行「${label}」？`,
        content: confirmation,
        okText: "确认",
        onOk: execute,
      });
      return;
    }
    void execute();
  };

  const localGroup: SyncGroup = {
    key: "__local__",
    kind: "local",
    title: "本地目录",
    jobs: [],
  };
  const customGroup: SyncGroup = {
    key: "__other__",
    kind: "custom",
    title: "其他同步",
    jobs: [],
  };
  const remoteGroups = new Map<string, SyncGroup>(
    remotes.map((remote) => [
      remote.name.toLowerCase(),
      {
        key: remote.name,
        kind: "remote",
        title: remote.name,
        jobs: [],
        remote,
      },
    ]),
  );

  for (const { job, group } of visible) {
    const targetGroup = group === "__local__"
      ? localGroup
      : remoteGroups.get(group) ?? customGroup;
    targetGroup.jobs.push(job);
  }

  const visibleGroups = [
    ...remoteGroups.values(),
    localGroup,
    customGroup,
  ].filter((group) => {
    if (group.jobs.length > 0) return true;
    if (otherFiltersActive) return false;
    if (storage === "all") return Boolean(group.remote);
    if (group.remote) return storageMatches(group.remote.name);
    if (
      group.key === "__local__" &&
      (storage === "__local__" || storage === "本地")
    ) {
      return true;
    }
    return false;
  });

  const selectedRemote = useMemo(
    () =>
      storage !== "all" && storage !== "__local__" && storage !== "__other__"
        ? remotes.find((r) => r.name.toLowerCase() === storage.toLowerCase()) ??
          null
        : null,
    [remotes, storage],
  );

  const handleTestRemote = async (name: string) => {
    setTestingName(name);
    try {
      const res = await api.testNativeStorage({ name });
      setTestResults((prev) => ({ ...prev, [name]: res }));
      if (res.ok && res.warning) Toast.warning(`${name}：${res.message}`);
      else if (res.ok)
        Toast.success(`${name}：${res.message}（${res.latencyMs}ms）`);
      else Toast.error(`${name}：${res.message}`);
    } catch (err) {
      errorToast(err);
    } finally {
      setTestingName(null);
    }
  };

  const handleAboutStorage = async (name: string) => {
    setAboutLoadingName(name);
    try {
      const info = await api.aboutNativeStorage(name);
      setAboutResults((prev) => ({ ...prev, [name]: info }));
      if (!info.supported) {
        Toast.info(info.message || `${name} 不支持空间用量查询`);
      }
    } catch (err) {
      errorToast(err);
    } finally {
      setAboutLoadingName(null);
    }
  };

  const statusPills: Array<{
    key: StatusKey;
    label: string;
    count: number;
    dot?: string;
    tone?: "warning" | "danger";
    alwaysShow?: boolean;
  }> = [
    { key: "all", label: "全部", count: statusCounts.all, alwaysShow: true },
    {
      key: "running",
      label: "同步中",
      count: statusCounts.running,
      dot: "kpi-dot kpi-dot--primary kpi-dot--pulse",
    },
    {
      key: "queued",
      label: "排队中",
      count: statusCounts.queued,
      dot: "kpi-dot kpi-dot--primary",
    },
    {
      key: "synced",
      label: "已同步",
      count: statusCounts.synced,
      dot: "kpi-dot kpi-dot--success",
      alwaysShow: true,
    },
    {
      key: "conflict",
      label: "存在冲突",
      count: statusCounts.conflict,
      dot: "kpi-dot kpi-dot--warning",
      tone: "warning",
    },
    {
      key: "failed",
      label: "同步异常",
      count: statusCounts.failed,
      dot: "kpi-dot kpi-dot--danger",
      tone: "danger",
    },
    {
      key: "disabled",
      label: "已暂停",
      count: statusCounts.disabled,
      dot: "kpi-dot",
    },
  ];

  return (
    <div className="sync-workbench" data-testid="sync-workbench">
      <aside className="sync-connections-rail" aria-label="存储连接导航">
        <div className="sync-connections-rail__head">
          <span className="sync-connections-rail__title">存储连接</span>
          {onAddConnection && (
            <Button
              size="small"
              type="tertiary"
              icon={<IconPlusStroked aria-hidden="true" />}
              onClick={onAddConnection}
            >
              添加连接
            </Button>
          )}
        </div>
        <div className="sync-connections-list" role="list">
          <button
            type="button"
            className={`sync-connection-item${storage === "all" ? " is-active" : ""}`}
            data-testid="connection-item-all"
            aria-pressed={storage === "all"}
            onClick={() => setFilter("storage", "all")}
          >
            <div className="sync-connection-item__main">
              <IconServerStroked aria-hidden="true" />
              <div className="sync-connection-item__copy">
                <span className="sync-connection-item__name">全部连接</span>
                <span className="sync-connection-item__sub muted">
                  {remotes.length} 个远程 · {jobs.length} 个任务
                </span>
              </div>
            </div>
          </button>

          {remotes.map((remote) => {
            const stat = connectionStats.byRemote.get(remote.name.toLowerCase());
            const health = resolveRemoteHealthTone(remote, healthMap);
            const active = storageMatches(remote.name);
            return (
              <button
                key={remote.name}
                type="button"
                className={`sync-connection-item${active ? " is-active" : ""}`}
                data-testid={`connection-item-${remote.name}`}
                aria-pressed={active}
                onClick={() => setFilter("storage", remote.name)}
              >
                <div className="sync-connection-item__main">
                  <span
                    className={`connection-health-dot is-${health.dot}`}
                    title={health.label}
                    aria-hidden="true"
                  />
                  <div className="sync-connection-item__copy">
                    <span
                      className="sync-connection-item__name"
                      title={remote.name}
                    >
                      {remote.name}
                    </span>
                    <span className="sync-connection-item__sub muted">
                      {remoteTypeLabels[remote.type] || remote.type}
                      {remote.type === "crypt" ? " · 加密" : ""}
                    </span>
                  </div>
                </div>
                <div className="sync-connection-item__badges">
                  {(stat?.errors ?? 0) > 0 && (
                    <span className="connection-badge is-danger">
                      {stat?.errors}
                    </span>
                  )}
                  <span className="connection-count muted">
                    {stat?.total ?? 0}
                  </span>
                </div>
              </button>
            );
          })}

          <button
            type="button"
            className={`sync-connection-item${
              storage === "__local__" || storage === "本地" ? " is-active" : ""
            }`}
            data-testid="connection-item-local"
            aria-pressed={localScope}
            onClick={() => setFilter("storage", "__local__")}
          >
            <div className="sync-connection-item__main">
              <IconFolderStroked aria-hidden="true" />
              <div className="sync-connection-item__copy">
                <span className="sync-connection-item__name">本地目录互备</span>
                <span className="sync-connection-item__sub muted">
                  {localRoots.length > 0
                    ? `${localRoots.length} 个授权目录`
                    : "本地共享目录"}
                </span>
              </div>
            </div>
            <div className="sync-connection-item__badges">
              {connectionStats.localStats.errors > 0 && (
                <span className="connection-badge is-danger">
                  {connectionStats.localStats.errors}
                </span>
              )}
              <span className="connection-count muted">
                {connectionStats.localStats.total}
              </span>
            </div>
          </button>

          {connectionStats.otherStats.total > 0 && (
            <button
              type="button"
              className={`sync-connection-item${
                storage === "__other__" ? " is-active" : ""
              }`}
              data-testid="connection-item-other"
              aria-pressed={storage === "__other__"}
              onClick={() => setFilter("storage", "__other__")}
            >
              <div className="sync-connection-item__main">
                <IconCloudStroked aria-hidden="true" />
                <div className="sync-connection-item__copy">
                  <span className="sync-connection-item__name">其他 / 跨端</span>
                  <span className="sync-connection-item__sub muted">
                    多云互备与历史任务
                  </span>
                </div>
              </div>
              <span className="connection-count muted">
                {connectionStats.otherStats.total}
              </span>
            </button>
          )}
        </div>
      </aside>

      <div className="sync-workbench-main">
        {(selectedRemote || storage === "__local__" || storage === "本地") && (
          <div className="sync-connection-banner">
            <div className="sync-connection-banner__info">
              <strong>
                {selectedRemote ? selectedRemote.name : "本地目录互备"}
              </strong>
              {selectedRemote ? (
                <Tag size="small" type="light" color="blue">
                  {remoteTypeLabels[selectedRemote.type] || selectedRemote.type}
                </Tag>
              ) : (
                <Tag size="small" type="light" color="grey">
                  {localRoots.length} 个已授权目录
                </Tag>
              )}
              {selectedRemote &&
                (() => {
                  const h = resolveRemoteHealthTone(selectedRemote, healthMap);
                  return (
                    <Tag
                      size="small"
                      type="light"
                      color={
                        h.dot === "online"
                          ? "green"
                          : h.dot === "offline"
                            ? "red"
                            : "grey"
                      }
                    >
                      {h.label}
                    </Tag>
                  );
                })()}
            </div>
          </div>
        )}

        <Tabs
          className="sync-workbench-tabs"
          activeKey={workbenchTab}
          onChange={selectView}
          keepDOM={false}
        >
          <Tabs.TabPane tab="任务列表" itemKey="tasks" />
          <Tabs.TabPane tab="同步历史" itemKey="history" />
          <Tabs.TabPane tab="连接设置" itemKey="settings" />
        </Tabs>

        {workbenchTab === "tasks" && (
          <div className="sync-overview-groups">
            {(jobs.length > 0 || filtered) && (
              <div className="sync-list-controls">
                <div className="sync-list-summary-bar">
                  <div
                    className="sync-status-pills"
                    role="group"
                    aria-label="同步状态概览"
                  >
                    {statusPills
                      .filter(
                        (pill) =>
                          pill.alwaysShow ||
                          pill.count > 0 ||
                          status === pill.key,
                      )
                      .map((pill) => (
                        <button
                          key={pill.key}
                          type="button"
                          aria-pressed={status === pill.key}
                          className={[
                            "dashboard-status-pill",
                            status === pill.key ? "is-active" : "",
                            pill.tone && pill.count > 0
                              ? `is-${pill.tone}`
                              : "",
                          ]
                            .filter(Boolean)
                            .join(" ")}
                          onClick={() => setFilter("status", pill.key)}
                        >
                          {pill.dot && (
                            <span className={pill.dot} aria-hidden="true" />
                          )}
                          <span>{pill.label}</span>
                          <span className="dashboard-status-pill-count">
                            {pill.count}
                          </span>
                        </button>
                      ))}
                  </div>
                  {!selectedIds.length && (
                    <div className="sync-list-global-actions">
                      <Button
                        size="small"
                        type="tertiary"
                        theme="light"
                        icon={<IconRefresh aria-hidden="true" />}
                        disabled={allBusy || !batchTargets.run.length}
                        onClick={() => runBatch("run", `${batchLabel}立即同步`)}
                      >
                        {batchLabel}立即同步（{batchTargets.run.length}）
                      </Button>
                      <Button
                        size="small"
                        type="danger"
                        theme="light"
                        icon={<IconStop aria-hidden="true" />}
                        disabled={allBusy || !batchTargets.stop.length}
                        onClick={() => runBatch("stop", `${batchLabel}中止`)}
                      >
                        {batchLabel}中止（{batchTargets.stop.length}）
                      </Button>
                    </div>
                  )}
                </div>

                <div className="sync-list-filters">
                  <Checkbox
                    aria-label="全选当前列表任务"
                    checked={fullySelected}
                    indeterminate={selectedIds.length > 0 && !fullySelected}
                    disabled={!selectable.length || allBusy}
                    onChange={(event) =>
                      setSelected(
                        event.target.checked
                          ? selectable.map((job) => job.id)
                          : [],
                      )
                    }
                  >
                    全选
                  </Checkbox>
                  <span className="sync-filters-divider" aria-hidden="true" />
                  <SearchFilterInput
                    ariaLabel="搜索任务名称或路径"
                    placeholder="搜索任务名称或路径"
                    value={keyword}
                    onChange={(value) => {
                      setKeyword(value);
                      setSelected([]);
                    }}
                    onSearch={setKeyword}
                  />
                  <Select
                    aria-labelledby={`${filterId}-direction`}
                    value={direction}
                    onChange={(value) => setFilter("direction", String(value))}
                    optionList={directionOptions}
                  />
                  <span className="sr-only" id={`${filterId}-direction`}>
                    按同步方向筛选
                  </span>
                  <Select
                    aria-labelledby={`${filterId}-status`}
                    value={status}
                    onChange={(value) => setFilter("status", String(value))}
                    optionList={[...statusOptions]}
                  />
                  <span className="sr-only" id={`${filterId}-status`}>
                    按运行状态筛选
                  </span>
                  {otherFiltersActive && (
                    <Button
                      size="small"
                      theme="borderless"
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
                        disabled={allBusy || !batchTargets.run.length}
                        onClick={() => runBatch("run", "批量立即同步")}
                      >
                        批量立即同步（{batchTargets.run.length}）
                      </Button>
                      <Button
                        size="small"
                        type="danger"
                        theme="light"
                        disabled={allBusy || !batchTargets.stop.length}
                        onClick={() => runBatch("stop", "批量中止传输")}
                      >
                        批量中止传输（{batchTargets.stop.length}）
                      </Button>
                      <Button
                        size="small"
                        type="tertiary"
                        disabled={allBusy || !batchTargets.enable.length}
                        onClick={() => runBatch("enable", "批量恢复同步")}
                      >
                        批量恢复同步（{batchTargets.enable.length}）
                      </Button>
                      <Button
                        size="small"
                        type="tertiary"
                        disabled={allBusy || !batchTargets.disable.length}
                        onClick={() => runBatch("disable", "批量暂停同步")}
                      >
                        批量暂停同步（{batchTargets.disable.length}）
                      </Button>
                      <Button
                        size="small"
                        theme="borderless"
                        onClick={() => setSelected([])}
                      >
                        取消选择
                      </Button>
                    </div>
                  </div>
                )}
              </div>
            )}

            {!visibleGroups.length &&
              (filtered ? (
                <div className="sync-filter-empty" role="status">
                  <span className="muted">没有符合当前筛选条件的同步任务</span>
                  <Button
                    size="small"
                    type="tertiary"
                    onClick={resetFilters}
                  >
                    清空筛选条件
                  </Button>
                </div>
              ) : (
                <EmptyState
                  action={
                    !localRoots.length ? (
                      <div className="empty-guide">
                        <span className="muted">
                          尚未配置存储连接或本地授权目录，先配置存储后即可创建同步任务
                        </span>
                        <div className="sync-empty-cta-row">
                          <Button
                            type="primary"
                            theme="solid"
                            onClick={() => selectView("settings")}
                          >
                            前往存储管理
                          </Button>
                        </div>
                      </div>
                    ) : (
                      <div className="empty-guide">
                        <span className="muted">
                          还没有同步任务，新建持续同步任务即可自动守护文件夹变更
                        </span>
                        <Button
                          type="primary"
                          theme="solid"
                          icon={<IconPlusStroked aria-hidden="true" />}
                          onClick={() => onAdd()}
                        >
                          创建第一个同步任务
                        </Button>
                      </div>
                    )
                  }
                />
              ))}

            {visibleGroups.map((group) => {
              const storageStatus = group.remote
                ? healthMap.get(group.remote.name.toLowerCase())
                : group.kind === "local"
                  ? healthMap.get("本地") ?? healthMap.get("/本地")
                  : undefined;
              const healthy = storageStatus
                ? isStorageHealthy(storageStatus)
                : true;
              const groupIcon =
                group.kind === "local" ? (
                  <IconFolderStroked aria-hidden="true" />
                ) : group.remote?.type === "crypt" ? (
                  <IconLockStroked aria-hidden="true" />
                ) : group.remote ? (
                  <IconServerStroked aria-hidden="true" />
                ) : (
                  <IconApps aria-hidden="true" />
                );

              return (
                <SectionCard
                  key={group.remote ? `remote:${group.remote.name}` : group.key}
                  title={
                    <div className="sync-group-title">
                      <span className="sync-group-icon">{groupIcon}</span>
                      <h2>{group.title}</h2>
                      {group.remote && (
                        <Tag
                          size="small"
                          type="light"
                          color={
                            group.remote.type === "crypt" ? "violet" : "blue"
                          }
                        >
                          {remoteTypeLabels[group.remote.type] ||
                            group.remote.type}
                        </Tag>
                      )}
                      {group.kind === "local" && (
                        <Tag
                          size="small"
                          type="light"
                          color={localRoots.length > 0 ? "blue" : "orange"}
                        >
                          {localRoots.length > 0
                            ? `已授权 ${localRoots.length} 个目录`
                            : "未授权目录"}
                        </Tag>
                      )}
                      {!healthy && (
                        <Tag size="small" type="light" color="red">
                          连接异常
                        </Tag>
                      )}
                      <span className="sync-group-count">
                        {group.jobs.length} 个任务
                      </span>
                      {group.remote?.summary && (
                        <span
                          className="sync-group-summary muted"
                          title={group.remote.summary}
                        >
                          {group.remote.summary}
                        </span>
                      )}
                    </div>
                  }
                  className="sync-connection-group"
                  bodyClassName="sync-connection-body"
                  extra={
                    group.remote && storage === "all" && (
                      <Button
                        size="small"
                        type="tertiary"
                        icon={<IconPlusStroked aria-hidden="true" />}
                        onClick={() => onAdd(group.remote?.name)}
                      >
                        添加同步任务
                      </Button>
                    )
                  }
                >
                  {!group.jobs.length && (
                    <div className="sync-connection-empty">
                      <span className="muted">此连接还没有同步任务</span>
                    </div>
                  )}
                  {group.jobs.map((job) => (
                    <SyncTaskRow
                      key={job.id}
                      job={job}
                      liveJob={liveJobMap.get(job.id)}
                      groupRemote={group.remote}
                      remotes={remotes}
                      engines={engines}
                      busy={allBusy}
                      checked={selectedIds.includes(job.id)}
                      onSelect={(isChecked) =>
                        setSelected((previous) =>
                          isChecked
                            ? previous.includes(job.id)
                              ? previous
                              : [...previous, job.id]
                            : previous.filter((id) => id !== job.id),
                        )
                      }
                      onNavigate={(path) => navigate(path)}
                      taskHref={(tab, intent) => taskHref(job.id, tab, intent)}
                      onEdit={() => onEdit?.(job)}
                      onClone={() => onClone?.(job)}
                      onToggle={() => onToggle?.(job)}
                      onRun={() => onRun?.(job)}
                      onStop={(liveTaskId) => onStop?.(job, liveTaskId)}
                      onTogglePauseTransfer={onTogglePauseTransfer}
                      onDelete={() => onDelete?.(job)}
                      onCheck={() => onCheck?.(job)}
                      onRepair={onRepair}
                      onLocalFolders={onLocalFolders}
                    />
                  ))}
                </SectionCard>
              );
            })}
          </div>
        )}

        {workbenchTab === "history" && (
          <SectionCard
            className="sync-history-card"
            data-testid="sync-workbench-history"
            title={
              selectedRemote
                ? `${selectedRemote.name} · 同步历史`
                : storage === "__local__" || storage === "本地"
                  ? "本地目录互备 · 同步历史"
                  : "全部连接 · 同步历史"
            }
            extra={
              <Button
                size="small"
                type="tertiary"
                icon={<IconRefresh aria-hidden="true" />}
                loading={recentHistory.loading}
                onClick={() => void recentHistory.refresh(true)}
              >
                刷新
              </Button>
            }
          >
            <div className="sync-history-toolbar">
              <div
                className="dashboard-recent-filters"
                role="group"
                aria-label="同步历史筛选"
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
                    theme={historyFilter === opt.id ? "solid" : "borderless"}
                    type={historyFilter === opt.id ? "primary" : "tertiary"}
                    onClick={() => {
                      setHistoryFilter(opt.id);
                      setHistoryPage(1);
                    }}
                  >
                    {opt.label}
                  </Button>
                ))}
              </div>
              <SearchFilterInput
                ariaLabel="搜索历史文件名或路径"
                placeholder="搜索文件名、路径或错误信息"
                value={historyKeyword}
                onChange={(val) => {
                  setHistoryKeyword(val);
                  setHistoryPage(1);
                }}
                onSearch={(val) => {
                  setHistoryKeyword(val);
                  setHistoryPage(1);
                }}
              />
            </div>

            {recentHistory.error && !recentHistory.data && (
              <InlineNotice
                role="alert"
                message={recentHistory.error}
                retrying={recentHistory.loading}
                onRetry={() => void recentHistory.refresh(true)}
              />
            )}

            {recentHistory.data?.stats && recentHistory.data.stats.allNum > 0 && (
              <div className="dashboard-recent-stats muted">
                <span>共 {recentHistory.data.stats.allNum} 条记录</span>
                <span>成功 {recentHistory.data.stats.successNum}</span>
                {recentHistory.data.stats.errorNum > 0 && (
                  <span className="dashboard-recent-stat--danger">
                    异常 {recentHistory.data.stats.errorNum}
                  </span>
                )}
                {recentHistory.data.stats.deleteNum > 0 && (
                  <span>删除 {recentHistory.data.stats.deleteNum}</span>
                )}
                <span>已传输 {formatSize(recentHistory.data.stats.sumSize)}</span>
              </div>
            )}

            {recentHistory.data?.dataList?.length ? (
              <>
                <div className="dashboard-recent-list">
                  {recentHistory.data.dataList.map((item) => {
                    const hasErr = Boolean(item.errMsg) || item.status === 7;
                    const isDelete = item.type === 1;
                    const statusTone = hasErr
                      ? "red"
                      : item.status === 2
                        ? "green"
                        : item.status === 1
                          ? "blue"
                          : "grey";
                    const title =
                      item.fileName ||
                      item.srcPath ||
                      item.dstPath ||
                      "未命名条目";
                    const matchJob = jobs.find((j) => j.id === item.jobId);
                    return (
                      <div
                        key={`${item.taskId ?? 0}-${item.id ?? title}-${item.createTime ?? 0}`}
                        className="dashboard-recent-row is-interactive"
                      >
                        <div className="dashboard-recent-row__main">
                          <div className="dashboard-recent-row__title">
                            <Tag color={statusTone} size="small" type="light">
                              {taskItemStatusNames[item.status] || "已处理"}
                            </Tag>
                            {item.type !== undefined && (
                              <Tag
                                color={isDelete ? "amber" : "grey"}
                                size="small"
                                type="light"
                              >
                                {taskTypeNames[item.type] || "处理"}
                              </Tag>
                            )}
                            <strong title={title}>{title}</strong>
                          </div>
                          <span className="dashboard-recent-row__meta muted">
                            {item.jobName}
                            {item.srcPath
                              ? ` · ${formatDisplayPath(item.srcPath)}`
                              : ""}
                            {item.dstPath
                              ? ` → ${formatDisplayPath(item.dstPath)}`
                              : ""}
                          </span>
                          {item.errMsg && (
                            <span className="dashboard-recent-row__error">
                              {item.errMsg}
                            </span>
                          )}
                        </div>
                        <div className="dashboard-recent-row__side">
                          <span className="muted">
                            {item.isPath === 1
                              ? "目录"
                              : formatSize(item.fileSize || 0)}
                          </span>
                          <span className="muted">
                            {formatRelativeSyncTime(item.createTime)}
                          </span>
                          {matchJob ? (
                            <Link to={taskHref(matchJob.id, "records")}>查看任务</Link>
                          ) : (
                            <Link to={`/tasks?jobId=${item.jobId}&tab=records`}>
                              查看任务
                            </Link>
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
                <DataPager
                  total={recentHistory.data.count}
                  page={historyPage}
                  size={20}
                  onChange={setHistoryPage}
                />
              </>
            ) : (
              <p className="dashboard-recent-empty muted">
                当前连接下暂无历史文件传输记录。
              </p>
            )}
          </SectionCard>
        )}

        {workbenchTab === "settings" && (
          <div
            className="sync-connection-settings"
            data-testid="sync-workbench-settings"
          >
            {selectedRemote ? (
              <SectionCard
                title={`${selectedRemote.name} · 连接信息与运维`}
                extra={
                  <div className="sync-settings-actions">
                    <Button
                      size="small"
                      type="tertiary"
                      loading={testingName === selectedRemote.name}
                      onClick={() => void handleTestRemote(selectedRemote.name)}
                    >
                      测试连接
                    </Button>
                    <Button
                      size="small"
                      type="tertiary"
                      loading={aboutLoadingName === selectedRemote.name}
                      onClick={() =>
                        void handleAboutStorage(selectedRemote.name)
                      }
                    >
                      查询容量
                    </Button>
                    {onBrowseRemote && (
                      <Button
                        size="small"
                        type="tertiary"
                        onClick={() => onBrowseRemote(selectedRemote)}
                      >
                        浏览与恢复
                      </Button>
                    )}
                    <Button
                      size="small"
                      type="primary"
                      onClick={() => onRepair(selectedRemote)}
                    >
                      编辑连接
                    </Button>
                  </div>
                }
              >
                <div className="sync-settings-summary">
                  <div>
                    <span className="muted">存储类型：</span>
                    <strong>
                      {remoteTypeLabels[selectedRemote.type] ||
                        selectedRemote.type}
                    </strong>
                  </div>
                  {selectedRemote.summary && (
                    <div>
                      <span className="muted">配置摘要：</span>
                      <span>{selectedRemote.summary}</span>
                    </div>
                  )}
                  {testResults[selectedRemote.name] && (
                    <InlineNotice
                      icon={
                        testResults[selectedRemote.name].ok ? undefined : (
                          <IconAlertTriangle aria-hidden="true" size="small" />
                        )
                      }
                      message={`${testResults[selectedRemote.name].message}${
                        testResults[selectedRemote.name].ok
                          ? `（${testResults[selectedRemote.name].latencyMs}ms）`
                          : ""
                      }`}
                    />
                  )}
                  {(() => {
                    const about = aboutResults[selectedRemote.name];
                    const total = about?.total ?? 0;
                    const used = about?.used ?? 0;
                    if (!about?.supported || total <= 0) return null;
                    return (
                      <div className="sync-settings-quota">
                        <div className="dashboard-quota-head">
                          <span>存储空间用量</span>
                          <span className="muted">
                            已用 {formatSize(used)} / {formatSize(total)}
                          </span>
                        </div>
                        <Progress
                          percent={Math.min(
                            100,
                            Math.round((used / total) * 100),
                          )}
                          showInfo={false}
                        />
                      </div>
                    );
                  })()}
                </div>
              </SectionCard>
            ) : storage === "__local__" || storage === "本地" ? (
              <SectionCard
                title="本地授权目录"
                extra={
                  <Button size="small" type="primary" onClick={onLocalFolders}>
                    管理授权目录
                  </Button>
                }
              >
                {localRoots.length > 0 ? (
                  <div className="sync-local-roots-list">
                    {localRoots.map((root) => (
                      <div
                        key={root.id || root.path}
                        className="sync-local-root-item"
                      >
                        <IconFolderStroked aria-hidden="true" />
                        <strong>{root.path}</strong>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="muted">
                    尚未检测到飞牛桌面授权目录，请点击右上角「管理授权目录」查看引导。
                  </p>
                )}
              </SectionCard>
            ) : (
              <SectionCard
                title="全部存储连接"
                extra={
                  <div className="sync-settings-actions">
                    {onNavigateEngines && (
                      <Button
                        size="small"
                        type="tertiary"
                        onClick={onNavigateEngines}
                      >
                        高级存储管理
                      </Button>
                    )}
                  </div>
                }
              >
                {remotes.length > 0 ? (
                  <div className="sync-all-connections-grid">
                    {remotes.map((remote) => {
                      const h = resolveRemoteHealthTone(remote, healthMap);
                      return (
                        <div
                          key={remote.name}
                          className="sync-all-connection-card"
                        >
                          <div className="sync-all-connection-card__head">
                            <strong>{remote.name}</strong>
                            <Tag
                              size="small"
                              type="light"
                              color={
                                h.dot === "online"
                                  ? "green"
                                  : h.dot === "offline"
                                    ? "red"
                                    : "grey"
                              }
                            >
                              {h.label}
                            </Tag>
                          </div>
                          <span className="muted">
                            {remoteTypeLabels[remote.type] || remote.type}
                            {remote.summary ? ` · ${remote.summary}` : ""}
                          </span>
                          <div className="sync-settings-actions">
                            <Button
                              size="small"
                              type="tertiary"
                              aria-label={`打开 ${remote.name} 的连接设置`}
                              onClick={() => setFilter("storage", remote.name)}
                            >
                              连接设置
                            </Button>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                ) : (
                  <p className="muted">
                    尚未添加远程存储连接。
                  </p>
                )}
              </SectionCard>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function SyncTaskRow({
  job,
  liveJob,
  groupRemote,
  remotes,
  engines,
  busy,
  checked,
  onSelect,
  onNavigate,
  taskHref,
  onEdit,
  onClone,
  onToggle,
  onRun,
  onStop,
  onTogglePauseTransfer,
  onDelete,
  onCheck,
  onRepair,
  onLocalFolders,
}: {
  job: JobItem;
  liveJob?: DashboardJobItem;
  groupRemote?: NativeRemoteItem;
  remotes: NativeRemoteItem[];
  engines: AlistItem[];
  busy: boolean;
  checked: boolean;
  onSelect: (checked: boolean) => void;
  onNavigate: (path: string) => void;
  taskHref: (tab: TaskTab, intent?: "mirrorPreview" | "conflicts") => string;
  onEdit: () => void;
  onClone: () => void;
  onToggle: () => void;
  onRun: () => void;
  onStop: (liveTaskId?: number) => void;
  onTogglePauseTransfer?: (taskId: number, paused: boolean) => void;
  onDelete: () => void;
  onCheck: () => void;
  onRepair: (remote: NativeRemoteItem) => void;
  onLocalFolders: () => void;
}) {
  const status = resolveEffectiveSyncStatus(job);
  const meta = getContinuousSyncMeta(job);
  const legacy = isLegacyOpenListJob(job, engines);
  const details = taskHref("overview");
  const records = taskHref("records");
  const effectivePaused = Boolean(liveJob?.paused ?? job.paused);
  const effectiveRunning = Boolean(job.running || liveJob?.running);
  const liveMetrics =
    liveJob && effectiveRunning ? computeLiveMetrics(liveJob) : null;

  const srcList = parseJobPathList(job.srcPath);
  const dstList = parseJobPathList(job.dstPath);
  const displaySiblings = [...srcList, ...dstList];
  const srcSummary = formatRouteSummary(srcList, displaySiblings);
  const dstSummary = formatRouteSummary(dstList, displaySiblings);

  const isCrypt =
    jobHasCryptRemote(job, engines, remotes) ||
    dstList.some((path) => {
      const seg = path.replace(/^\/+/, "").split("/")[0];
      if (!seg || seg === "本地") return false;
      return resolveRemoteChain(seg, remotes).hasCrypt;
    });

  const errorInfo = resolveSyncError(status);
  const needsAuth = errorInfo.type === "auth";
  const localPermission = errorInfo.type === "local_permission";
  const recoveryRemote = (() => {
    const candidates = remotes.filter((remote) =>
      getJobMountNames(job).some(
        (mount) => mount.toLowerCase() === remote.name.toLowerCase(),
      ),
    );
    if (errorInfo.endpoint) {
      return (
        candidates.find(
          (remote) =>
            remote.name.toLowerCase() === errorInfo.endpoint.toLowerCase(),
        ) ?? null
      );
    }
    return groupRemote ?? (candidates.length === 1 ? candidates[0] : null);
  })();
  const lastSuccess = formatRelativeSyncTime(status.lastSuccess);
  const nextAction = formatSyncNextAction(status);
  const waitingForRetry =
    status.state === "error" &&
    !needsAuth &&
    !localPermission &&
    (status.retryAt ?? 0) > Date.now() / 1000;
  const failedRecords =
    status.state === "error" &&
    job.method !== 3 &&
    (errorInfo.type === "transfer" ||
      errorInfo.type === "connection" ||
      errorInfo.type === "remote_permission");
  const mirrorNeedsReview = requiresFirstMirrorPreview(job);
  const failed =
    status.state === "error" ||
    (job.isCron !== 3 && failedTaskStatuses.includes(job.lastStatus ?? 0));
  const hasWarning =
    mirrorNeedsReview ||
    Boolean(status.conflictCount) ||
    status.state === "waiting_window";

  const bwLabel = (() => {
    const trimmed = job.bwLimit?.trim();
    if (!trimmed) return "";
    if (/^(?:off|0)$/i.test(trimmed)) return "不限速";
    const info = describeBwLimit(trimmed);
    return info.hasSchedule ? info.chipLabel : `限速 ${info.activeRateCompact}`;
  })();

  const cadenceLabel =
    job.isCron === 3
      ? `每 ${formatSecondsShort(meta.pollIntervalSec)} 检测 · 静默 ${formatSecondsShort(meta.debounceSec)} · ${formatMinutesShort(meta.auditIntervalMin)}核对`
      : job.isCron === 2
        ? "手动触发任务"
        : formatSchedulePlan(job);

  const menuActions: MenuAction[] = legacy
    ? [
        {
          label: "删除任务",
          icon: <IconDeleteStroked aria-hidden="true" />,
          onClick: onDelete,
          danger: true,
        },
      ]
    : [
        {
          label: "查看详情",
          icon: <IconChevronRight aria-hidden="true" />,
          onClick: () => onNavigate(details),
        },
        {
          label: "同步记录",
          icon: <IconHistory aria-hidden="true" />,
          onClick: () => onNavigate(records),
        },
        {
          label: "数据一致性校验",
          icon: <IconTickCircle aria-hidden="true" />,
          onClick: onCheck,
        },
        ...(job.method === 3
          ? [
              {
                label:
                  (status.conflictCount || 0) > 0
                    ? `冲突处理 (${status.conflictCount})`
                    : "冲突处理",
                icon: <IconAlertTriangle aria-hidden="true" />,
                onClick: () => onNavigate(taskHref("overview", "conflicts")),
              },
            ]
          : []),
        {
          label: "复制任务",
          icon: <IconCopy aria-hidden="true" />,
          onClick: onClone,
        },
        {
          label: "删除任务",
          icon: <IconDeleteStroked aria-hidden="true" />,
          onClick: onDelete,
          danger: true,
        },
      ];

  return (
    <div
      className={rowToneClass({
        legacy,
        running: effectiveRunning,
        paused: effectivePaused,
        failed,
        warning: hasWarning,
        disabled: job.enable !== 1,
        selected: checked,
      })}
    >
      {!legacy && (
        <div className="sync-overview-check">
          <Checkbox
            aria-label={`选择任务 ${getJobName(job)}`}
            checked={checked}
            disabled={busy}
            onChange={(event) => onSelect(Boolean(event.target.checked))}
          />
        </div>
      )}

      <div className="sync-overview-task">
        <div className="sync-overview-title-line">
          <span
            className={dotToneClass(job, legacy, effectivePaused)}
            aria-hidden="true"
          />
          <Link
            to={details}
            className="sync-overview-name"
            title={getJobName(job)}
          >
            {getJobName(job)}
          </Link>
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
                {meta.directionLabel}
              </Tag>
              {meta.directionKey !== "bidirectional" && (
                <Tag
                  size="small"
                  type="light"
                  color={meta.syncDelete ? "orange" : "green"}
                >
                  {meta.syncDelete ? "同步删除" : "保留目标"}
                </Tag>
              )}
              {isCrypt && (
                <Tag size="small" type="light" color="violet">
                  端到端加密
                </Tag>
              )}
              {status.watchDegraded && (
                <Tag size="small" type="light" color="amber">
                  监听降级轮询
                </Tag>
              )}
              {job.queued && (
                <Tag size="small" type="light" color="cyan">
                  排队等待中
                </Tag>
              )}
            </>
          )}
        </div>

        <div
          className="sync-overview-route"
          title={`${srcSummary.full} ${meta.arrow} ${dstSummary.full}`}
        >
          <span className="sync-overview-path-pill">{srcSummary.display}</span>
          <span
            className={`sync-overview-route-arrow${
              meta.directionKey === "bidirectional" ? " is-bi" : ""
            }`}
            aria-hidden="true"
          >
            {meta.arrow}
          </span>
          <span className="sync-overview-path-pill">{dstSummary.display}</span>
        </div>

        <div className="sync-overview-meta">
          <span className="sync-overview-meta-item">
            <IconClockStroked size="extra-small" aria-hidden="true" />
            <span>{cadenceLabel}</span>
          </span>
          {Boolean(job.windowEnable) && (
            <span className="sync-overview-meta-chip">
              窗口 {job.windowStart || "00:00"}–{job.windowEnd || "06:00"}
            </span>
          )}
          {Boolean(job.trashEnable) && (
            <span className="sync-overview-meta-chip">回收站</span>
          )}
          {bwLabel && <span className="sync-overview-meta-chip">{bwLabel}</span>}
        </div>
      </div>

      <div className="sync-overview-status">
        {legacy ? (
          <div className="sync-overview-error">
            旧版 OpenList 任务已停用，请删除后使用内置存储重建
          </div>
        ) : effectiveRunning && liveMetrics ? (
          <div className="sync-overview-progress">
            <div className="sync-overview-progress-head">
              <span
                className={`sync-overview-progress-label${
                  effectivePaused ? " is-paused" : ""
                }`}
              >
                {liveMetrics.progressLabel}
              </span>
              <span className="sync-overview-progress-right">
                {!effectivePaused && liveMetrics.liveSpeed > 0 && (
                  <span className="sync-overview-speed">
                    {formatSpeed(liveMetrics.liveSpeed)}
                  </span>
                )}
                <span className="sync-overview-pct">{liveMetrics.livePct}%</span>
              </span>
            </div>
            <Progress
              percent={liveMetrics.livePct}
              showInfo={false}
              stroke={
                effectivePaused
                  ? "var(--semi-color-warning)"
                  : "var(--semi-color-primary)"
              }
            />
            <div className="sync-overview-progress-sub">
              {liveMetrics.liveTotalSize > 0 ? (
                <span>
                  {formatSize(liveMetrics.liveDoneSize)} /{" "}
                  {formatSize(liveMetrics.liveTotalSize)}
                </span>
              ) : !liveMetrics.liveScanFinish && liveMetrics.liveScan ? (
                <span>
                  已扫描 {liveMetrics.liveScan.scannedDirs} 目录
                  {liveMetrics.scanTotalFiles > 0
                    ? ` · ${liveMetrics.scanTotalFiles} 文件`
                    : ""}
                </span>
              ) : (
                <span>正在准备传输…</span>
              )}
              {liveMetrics.liveTotal > 0 && (
                <span>
                  {liveJob?.liveNum?.success ?? 0}/{liveMetrics.liveTotal} 项
                </span>
              )}
              {!effectivePaused && liveMetrics.liveRemainTime > 0 && (
                <span>剩余 {formatDuration(liveMetrics.liveRemainTime)}</span>
              )}
            </div>
          </div>
        ) : effectiveRunning ? (
          <div className="sync-overview-status-stack">
            <div className="sync-overview-status-line">
              <SyncStatusTag status={status} />
              <Link
                to={records}
                className="sync-overview-inline-link"
              >
                查看实时进度
              </Link>
            </div>
            {lastSuccess && (
              <div className="sync-overview-sub muted">
                上次成功 {lastSuccess}
              </div>
            )}
          </div>
        ) : status.state === "error" ? (
          <div className="sync-overview-status-stack">
            <div className="sync-overview-status-line">
              <SyncStatusTag status={status} />
              {waitingForRetry ? (
                <span className="sync-overview-sub muted">{nextAction || "等待自动重试"}</span>
              ) : status.failures > 1 ? (
                <span className="sync-overview-error">
                  连续失败 {status.failures} 次
                </span>
              ) : null}
            </div>
            <div className="sync-overview-sub muted">
              {lastSuccess ? `最近成功 ${lastSuccess}` : "尚未成功同步"}
            </div>
            {(status.pendingChanges !== 0 || nextAction) && (
              <div className="sync-overview-sub muted">
                {status.pendingChanges < 0
                  ? "待完整核对"
                  : status.pendingChanges > 0
                    ? `${status.pendingChanges} 项待同步`
                    : "没有待同步变更"}
                {nextAction ? ` · ${nextAction}` : ""}
              </div>
            )}
            {status.error && (
              <div className="sync-overview-error" title={status.error}>
                {status.error}
              </div>
            )}
          </div>
        ) : job.isCron !== 3 && status.state === "monitoring" ? (
          <div className="sync-overview-status-stack">
            <div className="sync-overview-status-line">
              {failedTaskStatuses.includes(job.lastStatus ?? 0) ? (
                <Tag size="small" type="light" color="red">
                  上次同步异常
                </Tag>
              ) : (
                <Tag size="small" type="light" color="grey">
                  {job.isCron === 2 ? "仅手动" : "等待计划"}
                </Tag>
              )}
            </div>
            <div className="sync-overview-sub muted">
              {lastSuccess ? `最近成功 ${lastSuccess}` : "尚未执行同步"}
            </div>
          </div>
        ) : (
          <div className="sync-overview-status-stack">
            <div className="sync-overview-status-line">
              <SyncStatusTag status={status} />
              {status.pendingChanges > 0 && (
                <span className="sync-overview-pending">
                  {status.pendingChanges} 项待同步
                </span>
              )}
              {status.pendingChanges < 0 && (
                <span className="sync-overview-pending">待完整核对</span>
              )}
            </div>
            <div
              className="sync-overview-sub muted"
              title={
                status.lastSuccess > 0
                  ? formatTimestamp(status.lastSuccess, true)
                  : undefined
              }
            >
              {lastSuccess ? `最近成功 ${lastSuccess}` : "尚未执行同步"}
              {liveJob?.lastTask &&
              (liveJob.lastTask.allNum !== undefined ||
                (liveJob.lastTask.sumSize ?? 0) > 0)
                ? ` · ${liveJob.lastTask.successNum ?? 0}/${liveJob.lastTask.allNum ?? 0} 项`
                : ""}
              {nextAction ? ` · ${nextAction}` : ""}
            </div>
            {status.state === "waiting_window" && job.windowEnable && (
              <div className="sync-overview-warn">
                将在 {job.windowStart || "00:00"}–{job.windowEnd || "06:00"}{" "}
                恢复
              </div>
            )}
            {mirrorNeedsReview && (
              <div className="sync-overview-warn">首次启用镜像需预览确认</div>
            )}
            {Boolean(status.conflictCount) && (
              <div className="sync-overview-warn">
                存在 {status.conflictCount} 项冲突待处理
              </div>
            )}
          </div>
        )}
      </div>

      <div className="sync-overview-actions">
        {!legacy && (
          <>
            {status.state === "error" &&
              (needsAuth && recoveryRemote ? (
                <Button
                  size="small"
                  type="warning"
                  theme="light"
                  onClick={() => onRepair(recoveryRemote)}
                >
                  重新授权
                </Button>
              ) : localPermission ? (
                <Button
                  size="small"
                  type="warning"
                  theme="light"
                  onClick={onLocalFolders}
                >
                  查看本地授权
                </Button>
              ) : failedRecords ? (
                <Link
                  className="sync-overview-cta-link is-warning"
                  to={`/tasks?jobId=${job.id}&tab=records&hasError=1`}
                >
                  查看失败记录并重试
                </Link>
              ) : null)}
            {Boolean(status.conflictCount) && (
              <Link
                className="sync-overview-cta-link is-warning"
                to={taskHref("overview", "conflicts")}
              >
                查看冲突
              </Link>
            )}
            {mirrorNeedsReview && (
              <button
                type="button"
                className="sync-overview-cta-link is-primary"
                onClick={onToggle}
                disabled={busy}
              >
                预览并启动
              </button>
            )}
          </>
        )}

        <div className="sync-overview-row-btns">
          {legacy ? (
            <IconButton
              size="small"
              label="删除旧版任务"
              icon={<IconDeleteStroked aria-hidden="true" />}
              danger
              disabled={busy}
              onClick={onDelete}
            />
          ) : (
            <>
              <Button
                size="small"
                type="tertiary"
                onClick={() => onNavigate(details)}
              >
                详情
              </Button>
              {effectiveRunning ? (
                <>
                  {liveJob &&
                    liveJob.currentTaskId > 0 &&
                    onTogglePauseTransfer && (
                      <IconButton
                        size="small"
                        label={effectivePaused ? "继续传输" : "暂停传输"}
                        icon={
                          effectivePaused ? (
                            <IconPlay aria-hidden="true" />
                          ) : (
                            <IconPause aria-hidden="true" />
                          )
                        }
                        disabled={busy}
                        onClick={() =>
                          onTogglePauseTransfer(
                            liveJob.currentTaskId,
                            effectivePaused,
                          )
                        }
                      />
                    )}
                  <IconButton
                    size="small"
                    label="中止本次同步"
                    icon={<IconStop aria-hidden="true" />}
                    danger
                    disabled={busy}
                    onClick={() => onStop(liveJob?.currentTaskId)}
                  />
                  <IconButton
                    size="small"
                    label="查看实时进度"
                    icon={<IconActivity aria-hidden="true" />}
                    onClick={() => onNavigate(records)}
                  />
                </>
              ) : (
                <>
                  <IconButton
                    size="small"
                    label={job.enable === 1 ? "立即同步" : "请先恢复持续同步"}
                    icon={<IconRefresh aria-hidden="true" />}
                    disabled={busy || job.enable !== 1}
                    onClick={onRun}
                  />
                  <IconButton
                    size="small"
                    label={job.enable === 1 ? "暂停同步" : "恢复同步"}
                    icon={
                      job.enable === 1 ? (
                        <IconPause aria-hidden="true" />
                      ) : (
                        <IconPlay aria-hidden="true" />
                      )
                    }
                    disabled={busy}
                    onClick={onToggle}
                  />
                </>
              )}
              <IconButton
                size="small"
                label="编辑任务"
                icon={<IconEditStroked aria-hidden="true" />}
                disabled={busy}
                onClick={onEdit}
              />
              <ActionMenu size="small" actions={menuActions} disabled={busy} zIndex={999} />
            </>
          )}
        </div>
      </div>
    </div>
  );
}
