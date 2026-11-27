import { useId, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconActivity,
  IconAlertTriangle,
  IconApps,
  IconChevronRight,
  IconClockStroked,
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
  EmptyState,
  IconButton,
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
  getContinuousSyncMeta,
  isSyncStatusError,
  resolveEffectiveSyncStatus,
  syncStatusTone,
  SyncStatusTag,
} from "../components/SyncStatus";
import { describeBwLimit } from "../lib/bwLimit";
import { formatSpeed, safeSpeed } from "../lib/dashboardMetrics";
import { useAction } from "../lib/hooks";
import { remoteTypeLabels } from "../lib/storageTypes";
import { formatDisplayPathList } from "../lib/syncPathPreview";
import {
  isLegacyOpenListJob,
  requiresFirstMirrorPreview,
  resolveRemoteChain,
} from "../lib/taskForm";
import {
  formatDuration,
  formatSchedulePlan,
  formatSize,
  getJobName,
  parseJobPathList,
} from "./Home/homeUtils";
import { calcTaskDisplayPercent } from "./Home/taskRows";
import { formatTimestamp } from "../utils/date";
import type {
  AlistItem,
  DashboardJobItem,
  EngineStorageStatus,
  JobItem,
  NativeLocalRoot,
  NativeRemoteItem,
} from "../types";

const statusOptions = [
  { label: "全部状态", value: "all" },
  { label: "同步中", value: "running" },
  { label: "已同步", value: "synced" },
  { label: "排队中", value: "queued" },
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
type BatchAction = "run" | "stop" | "enable" | "disable" | "run_all" | "stop_all";
const failedTaskStatuses = [3, 5, 6, 7];

interface JobClassification {
  running: boolean;
  synced: boolean;
  queued: boolean;
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
    case "failed":
      return cls.failed;
    case "disabled":
      return cls.disabled;
    default:
      return true;
  }
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

function isStorageHealthy(st?: EngineStorageStatus): boolean {
  if (!st || st.disabled) return true;
  const s = (st.status || "").trim().toLowerCase();
  return s === "" || s === "work" || s === "ok";
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
  kind: "remote" | "local" | "custom";
  title: string;
  jobs: JobItem[];
  remote?: NativeRemoteItem;
}

export function SyncOverview({
  jobs,
  liveJobs,
  remotes,
  localRoots,
  engines,
  busy = false,
  onAdd,
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
  onNavigateEngines,
  onJobsChanged,
}: {
  jobs: JobItem[];
  liveJobs?: DashboardJobItem[];
  remotes: NativeRemoteItem[];
  localRoots?: NativeLocalRoot[];
  engines: AlistItem[];
  busy?: boolean;
  onAdd: (remote?: string) => void;
  onEdit: (job: JobItem) => void;
  onClone: (job: JobItem) => void;
  onToggle: (job: JobItem) => void;
  onRun: (job: JobItem) => void;
  onStop: (job: JobItem, liveTaskId?: number) => void;
  onTogglePauseTransfer?: (taskId: number, paused: boolean) => void;
  onDelete: (job: JobItem) => void;
  onCheck: (job: JobItem) => void;
  onRepair: (remote: NativeRemoteItem) => void;
  onLocalFolders: () => void;
  onNavigateEngines: () => void;
  onJobsChanged: () => Promise<void>;
}) {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const filterId = useId();
  const [keyword, setKeyword] = useState("");
  const [selected, setSelected] = useState<number[]>([]);
  const action = useAction();
  const storage = params.get("storage") || "all";
  const status: StatusKey =
    statusOptions.find(({ value }) => value === params.get("status"))?.value ??
    "all";
  const direction =
    directionOptions.find(({ value }) => value === params.get("direction"))
      ?.value ?? "all";
  const search = keyword.trim().toLowerCase();
  const filtered = Boolean(
    search || storage !== "all" || status !== "all" || direction !== "all",
  );
  const allBusy = busy || action.busy;

  const setFilter = (key: string, value: string) => {
    setSelected([]);
    const next = new URLSearchParams(params);
    if (value === "all") next.delete(key);
    else next.set(key, value);
    setParams(next, { replace: true });
  };

  const resetFilters = () => {
    setKeyword("");
    setSelected([]);
    setParams({}, { replace: true });
  };

  const liveJobMap = useMemo(() => {
    const map = new Map<number, DashboardJobItem>();
    for (const item of liveJobs || []) map.set(item.id, item);
    return map;
  }, [liveJobs]);

  const storageStatusMap = useMemo(() => {
    const map = new Map<string, EngineStorageStatus>();
    const health = engines[0]?.health;
    for (const st of health?.storages || []) {
      map.set(st.mountPath.toLowerCase(), st);
    }
    return map;
  }, [engines]);

  const statusCounts = useMemo(() => {
    const counts = {
      all: jobs.length,
      running: 0,
      synced: 0,
      queued: 0,
      failed: 0,
      disabled: 0,
    };
    for (const job of jobs) {
      const cls = classifyJobStatus(job);
      if (cls.running) counts.running += 1;
      if (cls.synced) counts.synced += 1;
      if (cls.queued) counts.queued += 1;
      if (cls.failed) counts.failed += 1;
      if (cls.disabled) counts.disabled += 1;
    }
    return counts;
  }, [jobs]);

  const storageNames = new Set(["本地", ...remotes.map((remote) => remote.name)]);
  const jobsWithMounts = jobs.map((job) => {
    const mounts = [
      ...parseJobPathList(job.srcPath),
      ...parseJobPathList(job.dstPath),
    ]
      .map((path) => path.split("/").filter(Boolean)[0])
      .filter(Boolean);
    mounts.forEach((mount) => storageNames.add(mount));
    return { job, mounts };
  });

  const storageMatches = (name: string) =>
    name.toLowerCase() === storage.toLowerCase();
  if (storage !== "all" && ![...storageNames].some(storageMatches)) {
    storageNames.add(storage);
  }

  const visible = jobsWithMounts.filter(({ job, mounts }) => {
    if (storage !== "all" && !mounts.some(storageMatches)) return false;
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
  const anyEnabled = jobs.some(
    (job) => job.enable === 1 && !isLegacyOpenListJob(job, engines),
  );
  const anyActive = jobs.some((job) => job.running || job.queued);

  const runBatch = (operation: BatchAction, label: string) => {
    if (
      operation === "enable" &&
      selectedJobs.some(requiresFirstMirrorPreview)
    ) {
      Toast.warning("所选任务包含待预览的镜像。请先逐个确认镜像影响。");
      return;
    }
    const execute = () =>
      action.run(async () => {
        try {
          const ids =
            operation === "run_all" || operation === "stop_all"
              ? undefined
              : selectedIds;
          const result = await api.batchJobAction(operation, ids);
          if (result.failed > 0) {
            Toast.warning(
              `${label}完成：成功 ${result.succeeded} 个，失败 ${result.failed} 个`,
            );
          } else {
            Toast.success(`${label}完成（共 ${result.succeeded} 个任务）`);
          }
          setSelected([]);
          await onJobsChanged();
        } catch (err) {
          errorToast(err);
        }
      });
    if (
      operation === "stop" ||
      operation === "stop_all" ||
      operation === "disable"
    ) {
      confirmAction({
        title: `确认执行「${label}」？`,
        content:
          operation === "disable"
            ? "暂停持续同步将停止监控并中止正在进行的传输，后续可随时恢复并自动补齐变更。"
            : "将中止目标任务当前的传输批次，已完成的文件不会撤销，持续同步保持开启。",
        okText: "确认",
        onOk: execute,
      });
      return;
    }
    void execute();
  };

  const local: SyncGroup = { kind: "local", title: "本地目录", jobs: [] };
  const custom: SyncGroup = { kind: "custom", title: "其他同步", jobs: [] };
  const groups = new Map<string, SyncGroup>(
    remotes.map((remote) => [
      remote.name.toLowerCase(),
      { kind: "remote", title: remote.name, jobs: [], remote },
    ]),
  );

  for (const { job, mounts: paths } of visible) {
    const mounts = new Set(
      paths
        .map((mount) => mount.toLowerCase())
        .filter((mount) => mount !== "本地"),
    );
    const [mount] = mounts;
    const legacy = isLegacyOpenListJob(job, engines);
    const group =
      !legacy && mounts.size === 1 && mount ? groups.get(mount) : undefined;
    (group ?? (!legacy && mounts.size === 0 ? local : custom)).jobs.push(job);
  }

  const visibleGroups = [...groups.values(), local, custom].filter(
    (group) => group.jobs.length || (!filtered && group.remote),
  );

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
                      pill.tone && pill.count > 0 ? `is-${pill.tone}` : "",
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
                  disabled={allBusy || !anyEnabled}
                  onClick={() => runBatch("run_all", "全部立即同步")}
                >
                  全部立即同步
                </Button>
                <Button
                  size="small"
                  type="danger"
                  theme="light"
                  icon={<IconStop aria-hidden="true" />}
                  disabled={allBusy || !anyActive}
                  onClick={() => runBatch("stop_all", "全部中止")}
                >
                  全部中止
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
                  event.target.checked ? selectable.map((job) => job.id) : [],
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
              aria-labelledby={`${filterId}-storage`}
              value={[...storageNames].find(storageMatches) ?? "all"}
              onChange={(value) => setFilter("storage", String(value))}
              optionList={[
                { label: "全部存储", value: "all" },
                ...[...storageNames].map((name) => ({
                  label: name === "本地" ? "本地目录" : name,
                  value: name,
                })),
              ]}
            />
            <span className="sr-only" id={`${filterId}-storage`}>
              按存储筛选
            </span>
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
            {filtered && (
              <Button size="small" theme="borderless" onClick={resetFilters}>
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
                  disabled={allBusy}
                  onClick={() => runBatch("run", "批量立即同步")}
                >
                  批量立即同步
                </Button>
                <Button
                  size="small"
                  type="danger"
                  theme="light"
                  disabled={allBusy}
                  onClick={() => runBatch("stop", "批量中止传输")}
                >
                  批量中止传输
                </Button>
                <Button
                  size="small"
                  type="tertiary"
                  disabled={allBusy}
                  onClick={() => runBatch("enable", "批量恢复同步")}
                >
                  批量恢复同步
                </Button>
                <Button
                  size="small"
                  type="tertiary"
                  disabled={allBusy}
                  onClick={() => runBatch("disable", "批量暂停同步")}
                >
                  批量暂停同步
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
            <Button size="small" type="tertiary" onClick={resetFilters}>
              清空筛选条件
            </Button>
          </div>
        ) : (
          <EmptyState
            action={
              !localRoots?.length ? (
                <div className="empty-guide">
                  <span className="muted">
                    尚未配置存储连接或本地授权目录，先配置存储后即可创建同步任务
                  </span>
                  <div className="sync-empty-cta-row">
                    <Button
                      type="primary"
                      theme="solid"
                      onClick={onNavigateEngines}
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
          ? storageStatusMap.get(`/${group.remote.name.toLowerCase()}`)
          : group.kind === "local"
            ? storageStatusMap.get("/本地")
            : undefined;
        const healthy = storageStatus ? isStorageHealthy(storageStatus) : true;
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
            key={group.remote ? `remote:${group.remote.name}` : group.title}
            title={
              <div className="sync-group-title">
                <span className="sync-group-icon">{groupIcon}</span>
                <h2>{group.title}</h2>
                {group.remote && (
                  <Tag
                    size="small"
                    type="light"
                    color={group.remote.type === "crypt" ? "violet" : "blue"}
                  >
                    {remoteTypeLabels[group.remote.type] || group.remote.type}
                  </Tag>
                )}
                {group.kind === "local" && localRoots && (
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
              group.remote && (
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
                onEdit={() => onEdit(job)}
                onClone={() => onClone(job)}
                onToggle={() => onToggle(job)}
                onRun={() => onRun(job)}
                onStop={(liveTaskId) => onStop(job, liveTaskId)}
                onTogglePauseTransfer={onTogglePauseTransfer}
                onDelete={() => onDelete(job)}
                onCheck={() => onCheck(job)}
                onRepair={onRepair}
                onLocalFolders={onLocalFolders}
              />
            ))}
          </SectionCard>
        );
      })}
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
  const details = `/tasks?jobId=${job.id}&tab=overview`;
  const effectivePaused = Boolean(liveJob?.paused ?? job.paused);
  const effectiveRunning = Boolean(job.running || liveJob?.running);
  const liveMetrics = liveJob && effectiveRunning ? computeLiveMetrics(liveJob) : null;

  const srcList = parseJobPathList(job.srcPath);
  const dstList = parseJobPathList(job.dstPath);
  const displaySiblings = [...srcList, ...dstList];
  const srcSummary = formatRouteSummary(srcList, displaySiblings);
  const dstSummary = formatRouteSummary(dstList, displaySiblings);

  const isEncryptedDst = dstList.some((path) => {
    const seg = path.replace(/^\/+/, "").split("/")[0];
    if (!seg || seg === "本地") return false;
    return resolveRemoteChain(seg, remotes).hasCrypt;
  });

  const needsAuth =
    groupRemote &&
    /登录状态已失效|认证失败|令牌已过期/.test(status.error);
  const localPermission =
    /本地.*(?:无权限|未授权|权限不足|权限被拒绝)|未授权(?:的)?本地|无法读取本地授权目录/.test(
      status.error,
    );
  const lastSuccess = formatRelativeSyncTime(status.lastSuccess);
  const waitingForRetry =
    status.state === "error" &&
    !needsAuth &&
    !localPermission &&
    (status.retryAt ?? 0) > Date.now() / 1000;
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
          onClick: () => onNavigate(`/tasks?jobId=${job.id}&tab=records`),
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
                onClick: () => onNavigate(`${details}&conflicts=1`),
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
          <Link to={details} className="sync-overview-name" title={getJobName(job)}>
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
              {meta.directionKey !== "bidirectional" && meta.syncDelete && (
                <Tag size="small" type="light" color="orange">
                  同步删除
                </Tag>
              )}
              {isEncryptedDst && (
                <Tag size="small" type="light" color="green">
                  加密
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
                to={`/tasks?jobId=${job.id}&tab=records`}
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
                <span className="sync-overview-sub muted">等待自动重试</span>
              ) : status.failures > 1 ? (
                <span className="sync-overview-error">
                  连续失败 {status.failures} 次
                </span>
              ) : null}
            </div>
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
            </div>
            {status.state === "waiting_window" && job.windowEnable && (
              <div className="sync-overview-warn">
                将在 {job.windowStart || "00:00"}–{job.windowEnd || "06:00"} 恢复
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
              (needsAuth && groupRemote ? (
                <Button
                  size="small"
                  type="warning"
                  theme="light"
                  onClick={() => onRepair(groupRemote)}
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
              ) : null)}
            {Boolean(status.conflictCount) && (
              <Link
                className="sync-overview-cta-link is-warning"
                to={`${details}&conflicts=1`}
              >
                查看冲突
              </Link>
            )}
            {mirrorNeedsReview && (
              <Link
                className="sync-overview-cta-link is-primary"
                to={`${details}&mirrorPreview=1`}
              >
                预览并启动
              </Link>
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
                    onClick={() => onNavigate(`/tasks?jobId=${job.id}&tab=records`)}
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
              <ActionMenu size="small" actions={menuActions} disabled={busy} />
            </>
          )}
        </div>
      </div>
    </div>
  );
}
