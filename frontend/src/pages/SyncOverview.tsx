import { useEffect, useId, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconCloudStroked,
  IconFolderStroked,
  IconPlusStroked,
  IconRefresh,
  IconServerStroked,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  DataPager,
  EmptyState,
  InlineNotice,
  SearchFilterInput,
  SectionCard,
  confirmAction,
  errorToast,
} from "../components/common";
import { remoteTypeLabels } from "../components/NativeStorageEditor";
import { TaskDetailDrawer } from "../components/TaskDetailDrawer";
import {
  formatRelativeSyncTime,
  getContinuousSyncMeta,
  isSyncStatusError,
  resolveEffectiveSyncStatus,
  SyncStatusTag,
} from "../components/SyncStatus";
import { formatSpeed } from "../lib/dashboardMetrics";
import { useAction, useResource } from "../lib/hooks";
import {
  formatDisplayPath,
  formatDisplayPathList,
} from "../lib/syncPathPreview";
import {
  getJobMountNames,
  isLegacyOpenListJob,
  jobHasCryptRemote,
  requiresFirstMirrorPreview,
  type TaskTab,
} from "../lib/taskForm";
import {
  formatSize,
  getJobName,
  parseJobPathList,
  taskItemStatusNames,
  taskTypeNames,
} from "./Home/homeUtils";
import { calcTaskDisplayPercent } from "./Home/taskRows";
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
];

const directionOptions = [
  { label: "全部方向", value: "all" },
  { label: "单向同步", value: "oneway" },
  { label: "双向同步", value: "bidirectional" },
  { label: "已开同步删除", value: "sync_delete" },
];

type BatchAction = "run" | "stop" | "enable" | "disable" | "run_all" | "stop_all";
type WorkbenchTab = "tasks" | "history" | "settings";
type HistoryFilter = "all" | "success" | "failed" | "delete";

const failedTaskStatuses = [3, 5, 6, 7];

interface SyncOverviewProps {
  jobs: JobItem[];
  remotes: NativeRemoteItem[];
  localRoots?: NativeLocalRoot[];
  engines: AlistItem[];
  onAdd: (remote?: string) => void;
  onRepair: (remote: NativeRemoteItem) => void;
  onAddConnection?: () => void;
  onBrowseRemote?: (remote: NativeRemoteItem) => void;
  onLocalFolders: () => void;
  onEditJob?: (job: JobItem) => void;
  onCloneJob?: (job: JobItem) => void;
  onJobsChanged: () => Promise<void>;
  onStorageChanged?: () => void;
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

export function SyncOverview({
  jobs,
  remotes,
  localRoots = [],
  engines,
  onAdd,
  onRepair,
  onAddConnection,
  onBrowseRemote,
  onLocalFolders,
  onEditJob,
  onCloneJob,
  onJobsChanged,
}: SyncOverviewProps) {
  const [params, setParams] = useSearchParams();
  const filterId = useId();
  const [keyword, setKeyword] = useState("");
  const [selected, setSelected] = useState<number[]>([]);
  const [workbenchTab, setWorkbenchTab] = useState<WorkbenchTab>("tasks");
  const [historyFilter, setHistoryFilter] = useState<HistoryFilter>("all");
  const [historyKeyword, setHistoryKeyword] = useState("");
  const [historyPage, setHistoryPage] = useState(1);
  const [drawer, setDrawer] = useState<{ jobId: number; tab: TaskTab } | null>(
    null,
  );
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
  const status =
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

  const setFilter = (key: string, value: string) => {
    setSelected([]);
    const next = new URLSearchParams(params);
    if (value === "all") next.delete(key);
    else next.set(key, value);
    setParams(next, { replace: true });
    if (key === "storage") setHistoryPage(1);
  };

  const resetFilters = () => {
    setKeyword("");
    setSelected([]);
    setHistoryPage(1);
    setParams({}, { replace: true });
  };

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

  const dashboardSnapshot = useResource(
    (signal) =>
      jobs.length > 0 ? api.dashboard(signal) : Promise.resolve(null),
    [jobs.length > 0],
  );

  const hasRunningJobs = useMemo(
    () =>
      jobs.some((j) => j.running && !j.paused) ||
      Boolean(dashboardSnapshot.data?.kpi?.runningJobs),
    [jobs, dashboardSnapshot.data?.kpi?.runningJobs],
  );

  useEffect(() => {
    if (!hasRunningJobs) return;
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void dashboardSnapshot.refresh(true);
      }
    }, 5_000);
    return () => window.clearInterval(timer);
  }, [hasRunningJobs, dashboardSnapshot.refresh]);

  useEffect(() => {
    if (jobs.length === 0) return;
    const onChanged = () => void dashboardSnapshot.refresh(true);
    window.addEventListener("opensync:jobs-changed", onChanged);
    return () => window.removeEventListener("opensync:jobs-changed", onChanged);
  }, [jobs.length, dashboardSnapshot.refresh]);

  const liveJobById = useMemo(() => {
    const map = new Map<number, DashboardJobItem>();
    for (const j of dashboardSnapshot.data?.jobs || []) {
      map.set(j.id, j);
    }
    return map;
  }, [dashboardSnapshot.data]);

  const storageHistoryParam =
    storage === "all" || storage === "__other__"
      ? undefined
      : storage === "本地"
        ? "__local__"
        : storage;

  const recentHistory = useResource(
    (signal) =>
      workbenchTab === "history"
        ? api.recentTaskItems(
            {
              pageNum: historyPage,
              pageSize: 20,
              storage: storageHistoryParam,
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
      historyFilter,
      historyKeyword,
    ],
  );

  const jobsWithMounts = useMemo(
    () => jobs.map((job) => ({ job, mounts: getJobMountNames(job) })),
    [jobs],
  );

  const storageNames = useMemo(() => {
    const names = new Set(["本地", ...remotes.map((remote) => remote.name)]);
    for (const { mounts } of jobsWithMounts) {
      for (const mount of mounts) names.add(mount);
    }
    if (storage !== "all" && storage !== "__local__" && storage !== "__other__") {
      const exists = [...names].some(
        (name) => name.toLowerCase() === storage.toLowerCase(),
      );
      if (!exists) names.add(storage);
    }
    return names;
  }, [remotes, jobsWithMounts, storage]);

  const storageMatches = (name: string) =>
    name.toLowerCase() === storage.toLowerCase();

  const isLocalOnlyJob = (job: JobItem, mounts: string[]) => {
    const legacy = isLegacyOpenListJob(job, engines);
    if (legacy) return false;
    const remoteMounts = mounts.filter((m) => m !== "本地");
    return remoteMounts.length === 0;
  };

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

    for (const { job, mounts } of jobsWithMounts) {
      const legacy = isLegacyOpenListJob(job, engines);
      const sync = resolveEffectiveSyncStatus(job);
      const hasErr =
        !legacy &&
        (isSyncStatusError(job.syncStatus) ||
          failedTaskStatuses.includes(job.lastStatus ?? 0));
      const isRun = Boolean(job.running && !job.paused);
      const conflicts = sync.conflictCount ?? 0;

      const remoteMounts = Array.from(
        new Set(
          mounts
            .map((m) => m.toLowerCase())
            .filter((m) => m !== "本地" && byRemote.has(m)),
        ),
      );

      if (!legacy && mounts.every((m) => m === "本地") && mounts.length > 0) {
        localStats.total += 1;
        if (isRun) localStats.running += 1;
        if (hasErr) localStats.errors += 1;
        localStats.conflicts += conflicts;
        continue;
      }

      if (!legacy && remoteMounts.length >= 1) {
        for (const r of remoteMounts) {
          const entry = byRemote.get(r);
          if (!entry) continue;
          entry.total += 1;
          if (isRun) entry.running += 1;
          if (hasErr) entry.errors += 1;
          entry.conflicts += conflicts;
        }
        continue;
      }

      otherStats.total += 1;
      if (isRun) otherStats.running += 1;
      if (hasErr) otherStats.errors += 1;
      otherStats.conflicts += conflicts;
    }

    return { byRemote, localStats, otherStats };
  }, [jobsWithMounts, remotes, engines]);

  const visible = jobsWithMounts.filter(({ job, mounts }) => {
    if (storage === "__local__" || storage === "本地") {
      if (!isLocalOnlyJob(job, mounts)) return false;
    } else if (storage === "__other__") {
      const legacy = isLegacyOpenListJob(job, engines);
      const remoteSet = new Set(remotes.map((r) => r.name.toLowerCase()));
      const remoteMounts = mounts.filter(
        (m) => m !== "本地" && remoteSet.has(m.toLowerCase()),
      );
      if (!legacy && (mounts.every((m) => m === "本地") || remoteMounts.length === 1)) {
        return false;
      }
    } else if (storage !== "all" && !mounts.some(storageMatches)) {
      return false;
    }

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

    const sync = resolveEffectiveSyncStatus(job);
    const failed =
      isSyncStatusError(job.syncStatus) ||
      failedTaskStatuses.includes(job.lastStatus ?? 0);
    switch (status) {
      case "running":
        return Boolean(
          job.running || sync.state === "syncing" || sync.state === "scanning",
        );
      case "synced":
        return (
          !job.running &&
          job.enable === 1 &&
          !failed &&
          (sync.state === "synced" || sync.state === "monitoring")
        );
      case "queued":
        return Boolean(job.queued);
      case "conflict":
        return (sync.conflictCount ?? 0) > 0;
      case "failed":
        return failed;
      case "disabled":
        return job.enable !== 1;
      default:
        return true;
    }
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
    if (operation === "enable" && selectedJobs.some(requiresFirstMirrorPreview)) {
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
    if (operation === "stop" || operation === "stop_all" || operation === "disable") {
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

  const localGroup = {
    key: "__local__",
    title:
      storage === "__local__" ? "本地目录互备" : "本地目录",
    jobs: [] as JobItem[],
    remote: undefined as NativeRemoteItem | undefined,
  };
  const customGroup = {
    key: "__other__",
    title: "其他同步",
    jobs: [] as JobItem[],
    remote: undefined as NativeRemoteItem | undefined,
  };
  const remoteGroups = new Map(
    remotes.map((remote) => [
      remote.name.toLowerCase(),
      {
        key: remote.name,
        title: remote.name,
        jobs: [] as JobItem[],
        remote,
      },
    ]),
  );

  for (const { job, mounts: paths } of visible) {
    const nonLocalMounts = new Set(
      paths.map((mount) => mount.toLowerCase()).filter((mount) => mount !== "本地"),
    );
    const [mount] = nonLocalMounts;
    const legacy = isLegacyOpenListJob(job, engines);
    const targetRemoteGroup =
      !legacy && nonLocalMounts.size === 1 && mount
        ? remoteGroups.get(mount)
        : undefined;
    (targetRemoteGroup ?? (!legacy && nonLocalMounts.size === 0 ? localGroup : customGroup))
      .jobs.push(job);
  }

  const visibleGroups = [...remoteGroups.values(), localGroup, customGroup].filter(
    (group) => {
      if (group.jobs.length > 0) return true;
      if (otherFiltersActive) return false;
      if (storage === "all") return Boolean(group.remote);
      if (group.remote) return storageMatches(group.remote.name);
      if (group.key === "__local__" && (storage === "__local__" || storage === "本地")) {
        return true;
      }
      return false;
    },
  );

  const selectedRemote = useMemo(
    () =>
      storage !== "all" && storage !== "__local__" && storage !== "__other__"
        ? remotes.find((r) => r.name.toLowerCase() === storage.toLowerCase()) ??
          null
        : null,
    [remotes, storage],
  );

  const drawerJob = useMemo(
    () =>
      drawer ? jobs.find((j) => j.id === drawer.jobId) ?? null : null,
    [drawer, jobs],
  );

  const handleTestRemote = async (name: string) => {
    setTestingName(name);
    try {
      const res = await api.testNativeStorage({ name });
      setTestResults((prev) => ({ ...prev, [name]: res }));
      if (res.ok && res.warning) Toast.warning(`${name}：${res.message}`);
      else if (res.ok) Toast.success(`${name}：${res.message}（${res.latencyMs}ms）`);
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

  const storageSelectValue =
    storage === "__local__"
      ? "__local__"
      : storage === "__other__"
        ? "__other__"
        : [...storageNames].find(storageMatches) ?? "all";

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
                onClick={() => setFilter("storage", remote.name)}
              >
                <div className="sync-connection-item__main">
                  <span
                    className={`connection-health-dot is-${health.dot}`}
                    title={health.label}
                    aria-hidden="true"
                  />
                  <div className="sync-connection-item__copy">
                    <span className="sync-connection-item__name" title={remote.name}>
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
              {selectedRemote && (() => {
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
            <div className="sync-connection-banner__actions">
              {selectedRemote ? (
                <>
                  <Button
                    size="small"
                    type="tertiary"
                    loading={testingName === selectedRemote.name}
                    onClick={() => void handleTestRemote(selectedRemote.name)}
                  >
                    测试连接
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
                    type="tertiary"
                    onClick={() => onRepair(selectedRemote)}
                  >
                    编辑连接
                  </Button>
                  <Button
                    size="small"
                    type="primary"
                    onClick={() => onAdd(selectedRemote.name)}
                  >
                    添加同步任务
                  </Button>
                </>
              ) : (
                <>
                  <Button size="small" type="tertiary" onClick={onLocalFolders}>
                    管理授权目录
                  </Button>
                  <Button size="small" type="primary" onClick={() => onAdd()}>
                    添加同步任务
                  </Button>
                </>
              )}
            </div>
          </div>
        )}

        <Tabs
          className="sync-workbench-tabs"
          activeKey={workbenchTab}
          onChange={(key) => setWorkbenchTab(key as WorkbenchTab)}
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
                <div className="sync-list-filters">
                  <Checkbox
                    aria-label="全选当前列表任务"
                    checked={fullySelected}
                    indeterminate={selectedIds.length > 0 && !fullySelected}
                    disabled={!selectable.length || action.busy}
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
                    value={storageSelectValue}
                    onChange={(value) => setFilter("storage", String(value))}
                    optionList={[
                      { label: "全部存储", value: "all" },
                      { label: "本地目录互备", value: "__local__" },
                      ...[...storageNames]
                        .filter((name) => name !== "本地")
                        .map((name) => ({ label: name, value: name })),
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
                    optionList={statusOptions}
                  />
                  <span className="sr-only" id={`${filterId}-status`}>
                    按运行状态筛选
                  </span>
                  {filtered && (
                    <Button
                      size="small"
                      theme="borderless"
                      onClick={resetFilters}
                    >
                      重置筛选
                    </Button>
                  )}
                </div>
                {selectedIds.length ? (
                  <div className="batch-action-bar">
                    <span className="batch-action-summary">
                      已选中 <strong>{selectedIds.length}</strong> 个任务
                    </span>
                    <div className="batch-action-buttons">
                      <Button
                        size="small"
                        disabled={action.busy}
                        onClick={() => runBatch("run", "批量立即同步")}
                      >
                        批量立即同步
                      </Button>
                      <Button
                        size="small"
                        type="danger"
                        disabled={action.busy}
                        onClick={() => runBatch("stop", "批量中止传输")}
                      >
                        批量中止传输
                      </Button>
                      <Button
                        size="small"
                        disabled={action.busy}
                        onClick={() => runBatch("enable", "批量恢复同步")}
                      >
                        批量恢复同步
                      </Button>
                      <Button
                        size="small"
                        disabled={action.busy}
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
                ) : (
                  <div className="sync-list-global-actions">
                    <Button
                      size="small"
                      disabled={action.busy || !anyEnabled}
                      onClick={() => runBatch("run_all", "全部立即同步")}
                    >
                      全部立即同步
                    </Button>
                    <Button
                      size="small"
                      type="danger"
                      disabled={action.busy || !anyActive}
                      onClick={() => runBatch("stop_all", "全部中止")}
                    >
                      全部中止
                    </Button>
                  </div>
                )}
              </div>
            )}

            {!visibleGroups.length &&
              (filtered ? (
                <p className="muted" role="status">
                  没有符合当前筛选条件的同步任务
                </p>
              ) : (
                <EmptyState />
              ))}

            {visibleGroups.map((group) => (
              <SectionCard
                key={group.remote ? `remote:${group.remote.name}` : group.key}
                title={group.title}
                className="sync-connection-group"
                bodyClassName="sync-connection-body"
                extra={
                  group.remote && (
                    <Button
                      size="small"
                      type="tertiary"
                      onClick={() => onAdd(group.remote?.name)}
                    >
                      添加同步任务
                    </Button>
                  )
                }
              >
                {!group.jobs.length && (
                  <p className="muted">此连接还没有同步任务</p>
                )}
                {group.jobs.map((job) => {
                  const jobStatus = resolveEffectiveSyncStatus(job);
                  const meta = getContinuousSyncMeta(job);
                  const legacy = isLegacyOpenListJob(job, engines);
                  const isCrypt = jobHasCryptRemote(job, engines, remotes);
                  const details = `/tasks?jobId=${job.id}&tab=overview`;
                  const needsAuth =
                    group.remote &&
                    /登录状态已失效|认证失败|令牌已过期/.test(jobStatus.error);
                  const localPermission =
                    /本地.*(?:无权限|未授权|权限不足|权限被拒绝)|未授权(?:的)?本地|无法读取本地授权目录/.test(
                      jobStatus.error,
                    );
                  const lastSuccess = formatRelativeSyncTime(
                    jobStatus.lastSuccess,
                  );
                  const waitingForRetry =
                    jobStatus.state === "error" &&
                    !needsAuth &&
                    !localPermission &&
                    (jobStatus.retryAt ?? 0) > Date.now() / 1000;
                  const mirrorNeedsReview = requiresFirstMirrorPreview(job);
                  const liveJob = liveJobById.get(job.id);
                  const showLiveProgress = Boolean(
                    !legacy && liveJob?.running && !liveJob.paused,
                  );
                  const liveTotal =
                    (liveJob?.liveDoneSize || 0) + (liveJob?.liveTotalSize || 0);
                  const livePct = showLiveProgress
                    ? calcTaskDisplayPercent({
                        doneSize: liveJob?.liveDoneSize || 0,
                        totalSize: Math.max(
                          liveTotal,
                          liveJob?.liveTotalSize || 0,
                        ),
                        num: undefined,
                        scanFinish: liveJob?.liveScanFinish,
                      })
                    : 0;

                  return (
                    <div className="sync-overview-row" key={job.id}>
                      {!legacy && (
                        <Checkbox
                          aria-label={`选择任务 ${getJobName(job)}`}
                          checked={selectedIds.includes(job.id)}
                          disabled={action.busy}
                          onChange={(event) =>
                            setSelected((previous) =>
                              event.target.checked
                                ? [...previous, job.id]
                                : previous.filter((id) => id !== job.id),
                            )
                          }
                        />
                      )}
                      <div className="sync-overview-task">
                        <div>
                          <Link to={details}>{getJobName(job)}</Link>
                          {legacy ? (
                            <span className="muted">旧版任务已停用</span>
                          ) : waitingForRetry ? (
                            <span className="muted">等待重试</span>
                          ) : job.isCron !== 3 &&
                            jobStatus.state === "monitoring" ? (
                            failedTaskStatuses.includes(job.lastStatus ?? 0) ? (
                              <span className="sync-overview-error">
                                上次同步异常
                              </span>
                            ) : (
                              <span className="muted">
                                {job.isCron === 2 ? "仅手动" : "等待计划"}
                              </span>
                            )
                          ) : (
                            <SyncStatusTag status={jobStatus} />
                          )}
                          {!legacy && (
                            <Tag size="small" type="light" color="grey">
                              {meta.directionLabel}
                            </Tag>
                          )}
                          {!legacy && meta.directionKey !== "bidirectional" && (
                            <Tag
                              size="small"
                              type="light"
                              color={meta.syncDelete ? "amber" : "green"}
                            >
                              {meta.syncDelete ? "同步删除" : "保留目标"}
                            </Tag>
                          )}
                          {isCrypt && (
                            <Tag size="small" type="light" color="violet">
                              端到端加密
                            </Tag>
                          )}
                          {jobStatus.watchDegraded && (
                            <Tag size="small" type="light" color="amber">
                              监听降级轮询
                            </Tag>
                          )}
                        </div>
                        <p className="sync-overview-path">
                          {formatDisplayPathList(parseJobPathList(job.srcPath)).join(
                            "、",
                          )}{" "}
                          {meta.arrow}{" "}
                          {formatDisplayPathList(parseJobPathList(job.dstPath)).join(
                            "、",
                          )}
                        </p>
                        {showLiveProgress && (
                          <div className="sync-row-live-progress">
                            <Progress
                              percent={livePct}
                              showInfo={false}
                              size="small"
                              stroke="var(--semi-color-primary)"
                            />
                            <span className="muted">
                              {(liveJob?.liveSpeed || 0) > 0
                                ? formatSpeed(liveJob?.liveSpeed)
                                : "扫描中"}{" "}
                              · {livePct}%
                            </span>
                          </div>
                        )}
                        {lastSuccess && (
                          <div className="muted">最近成功 {lastSuccess}</div>
                        )}
                        {jobStatus.state === "error" && jobStatus.error && (
                          <div className="sync-overview-error">
                            {jobStatus.error}
                          </div>
                        )}
                      </div>
                      {!legacy && (
                        <div className="sync-overview-actions">
                          {jobStatus.state === "error" &&
                            (needsAuth && group.remote ? (
                              <Button
                                size="small"
                                onClick={() => onRepair(group.remote!)}
                              >
                                重新授权
                              </Button>
                            ) : localPermission ? (
                              <Button size="small" onClick={onLocalFolders}>
                                查看本地授权
                              </Button>
                            ) : null)}
                          {Boolean(jobStatus.conflictCount) && (
                            <Link to={`${details}&conflicts=1`}>查看冲突</Link>
                          )}
                          {mirrorNeedsReview && (
                            <Link to={`${details}&mirrorPreview=1`}>
                              预览并启动
                            </Link>
                          )}
                          <Button
                            size="small"
                            type="tertiary"
                            onClick={() =>
                              setDrawer({ jobId: job.id, tab: "overview" })
                            }
                          >
                            详情
                          </Button>
                          <Button
                            size="small"
                            theme="borderless"
                            onClick={() =>
                              setDrawer({ jobId: job.id, tab: "history" })
                            }
                          >
                            记录
                          </Button>
                        </div>
                      )}
                    </div>
                  );
                })}
              </SectionCard>
            ))}
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
                            <Button
                              size="small"
                              theme="borderless"
                              onClick={() =>
                                setDrawer({ jobId: matchJob.id, tab: "history" })
                              }
                            >
                              查看任务
                            </Button>
                          ) : (
                            <Link to={`/tasks?jobId=${item.jobId}&tab=history`}>
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
                      onClick={() => void handleAboutStorage(selectedRemote.name)}
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
                      编辑配置
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
                      <div key={root.id || root.path} className="sync-local-root-item">
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
                    <Button size="small" type="tertiary" onClick={onLocalFolders}>
                      本地授权目录
                    </Button>
                    {onAddConnection && (
                      <Button
                        size="small"
                        type="primary"
                        onClick={onAddConnection}
                      >
                        添加远程连接
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
                              loading={testingName === remote.name}
                              onClick={() => void handleTestRemote(remote.name)}
                            >
                              测试
                            </Button>
                            {onBrowseRemote && (
                              <Button
                                size="small"
                                type="tertiary"
                                onClick={() => onBrowseRemote(remote)}
                              >
                                浏览
                              </Button>
                            )}
                            <Button
                              size="small"
                              type="tertiary"
                              onClick={() => onRepair(remote)}
                            >
                              配置
                            </Button>
                          </div>
                        </div>
                      );
                    })}
                  </div>
                ) : (
                  <p className="muted">
                    尚未添加远程存储连接，点击右上角「添加远程连接」开始配置。
                  </p>
                )}
              </SectionCard>
            )}
          </div>
        )}
      </div>

      <TaskDetailDrawer
        job={drawerJob}
        initialTab={drawer?.tab ?? "overview"}
        engines={engines}
        remotes={remotes}
        onClose={() => setDrawer(null)}
        onEdit={(job) => {
          setDrawer(null);
          onEditJob?.(job);
        }}
        onClone={(job) => {
          setDrawer(null);
          onCloneJob?.(job);
        }}
        onJobsChanged={onJobsChanged}
      />
    </div>
  );
}
