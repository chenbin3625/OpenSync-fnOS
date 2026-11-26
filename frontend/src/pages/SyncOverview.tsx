import { useId, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { api } from "../api/client";
import {
  EmptyState,
  SearchFilterInput,
  SectionCard,
  confirmAction,
  errorToast,
} from "../components/common";
import {
  formatRelativeSyncTime,
  getContinuousSyncMeta,
  isSyncStatusError,
  resolveEffectiveSyncStatus,
  SyncStatusTag,
} from "../components/SyncStatus";
import { useAction } from "../lib/hooks";
import { formatDisplayPathList } from "../lib/syncPathPreview";
import { isLegacyOpenListJob, requiresFirstMirrorPreview } from "../lib/taskForm";
import { getJobName, parseJobPathList } from "./Home/homeUtils";
import type { AlistItem, JobItem, NativeRemoteItem } from "../types";

const statusOptions = [
  { label: "全部状态", value: "all" },
  { label: "同步中", value: "running" },
  { label: "已同步", value: "synced" },
  { label: "排队中", value: "queued" },
  { label: "同步异常", value: "failed" },
  { label: "已暂停", value: "disabled" },
];

const directionOptions = [
  { label: "全部方向", value: "all" },
  { label: "双向同步", value: "bidirectional" },
  { label: "单向上传", value: "upload" },
  { label: "单向下载", value: "download" },
  { label: "自定义单向", value: "custom" },
  { label: "已开同步删除", value: "sync_delete" },
];

type BatchAction = "run" | "stop" | "enable" | "disable" | "run_all" | "stop_all";
const failedTaskStatuses = [3, 5, 6, 7];

export function SyncOverview({
  jobs,
  remotes,
  engines,
  onAdd,
  onRepair,
  onLocalFolders,
  onJobsChanged,
}: {
  jobs: JobItem[];
  remotes: NativeRemoteItem[];
  engines: AlistItem[];
  onAdd: (remote?: string) => void;
  onRepair: (remote: NativeRemoteItem) => void;
  onLocalFolders: () => void;
  onJobsChanged: () => Promise<void>;
}) {
  const [params, setParams] = useSearchParams();
  const filterId = useId();
  const [keyword, setKeyword] = useState("");
  const [selected, setSelected] = useState<number[]>([]);
  const action = useAction();
  const storage = params.get("storage") || "all";
  const status = statusOptions.find(({ value }) => value === params.get("status"))?.value ?? "all";
  const direction = directionOptions.find(({ value }) => value === params.get("direction"))?.value ?? "all";
  const search = keyword.trim().toLowerCase();
  const filtered = Boolean(search || storage !== "all" || status !== "all" || direction !== "all");
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
  const storageNames = new Set(["本地", ...remotes.map((remote) => remote.name)]);
  const jobsWithMounts = jobs.map((job) => {
    const mounts = [...parseJobPathList(job.srcPath), ...parseJobPathList(job.dstPath)]
      .map((path) => path.split("/").filter(Boolean)[0])
      .filter(Boolean);
    mounts.forEach((mount) => storageNames.add(mount));
    return { job, mounts };
  });
  const storageMatches = (name: string) => name.toLowerCase() === storage.toLowerCase();
  if (storage !== "all" && ![...storageNames].some(storageMatches)) {
    storageNames.add(storage);
  }
  const visible = jobsWithMounts.filter(({ job, mounts }) => {
    if (storage !== "all" && !mounts.some(storageMatches)) return false;
    const searchable = `${getJobName(job)} #${job.id} ${job.srcPath} ${job.dstPath}`;
    if (search && !searchable.toLowerCase().includes(search)) return false;
    if (direction !== "all") {
      const meta = getContinuousSyncMeta(job);
      const matches = direction === "sync_delete"
        ? meta.syncDelete && meta.directionKey !== "bidirectional"
        : meta.directionKey === direction;
      if (!matches) return false;
    }
    const sync = resolveEffectiveSyncStatus(job);
    const failed = isSyncStatusError(job.syncStatus) || failedTaskStatuses.includes(job.lastStatus ?? 0);
    switch (status) {
      case "running":
        return Boolean(job.running || sync.state === "syncing" || sync.state === "scanning");
      case "synced":
        return !job.running && job.enable === 1 && !failed &&
          (sync.state === "synced" || sync.state === "monitoring");
      case "queued":
        return Boolean(job.queued);
      case "failed":
        return failed;
      case "disabled":
        return job.enable !== 1;
      default:
        return true;
    }
  });
  const selectable = visible.map(({ job }) => job).filter((job) => !isLegacyOpenListJob(job, engines));
  const selectedJobs = selectable.filter((job) => selected.includes(job.id));
  const selectedIds = selectedJobs.map((job) => job.id);
  const fullySelected = selectable.length > 0 && selectable.length === selectedIds.length;
  const anyEnabled = jobs.some((job) => job.enable === 1 && !isLegacyOpenListJob(job, engines));
  const anyActive = jobs.some((job) => job.running || job.queued);
  const runBatch = (operation: BatchAction, label: string) => {
    if (operation === "enable" && selectedJobs.some(requiresFirstMirrorPreview)) {
      Toast.warning("所选任务包含待预览的镜像。请先逐个确认镜像影响。");
      return;
    }
    const execute = () => action.run(async () => {
      try {
        const ids = operation === "run_all" || operation === "stop_all" ? undefined : selectedIds;
        const result = await api.batchJobAction(operation, ids);
        if (result.failed > 0) {
          Toast.warning(`${label}完成：成功 ${result.succeeded} 个，失败 ${result.failed} 个`);
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
        content: operation === "disable"
          ? "暂停持续同步将停止监控并中止正在进行的传输，后续可随时恢复并自动补齐变更。"
          : "将中止目标任务当前的传输批次，已完成的文件不会撤销，持续同步保持开启。",
        okText: "确认",
        onOk: execute,
      });
      return;
    }
    void execute();
  };

  const local = { title: "本地目录", jobs: [] as JobItem[], remote: undefined };
  const custom = { title: "其他同步", jobs: [] as JobItem[], remote: undefined };
  const groups = new Map(remotes.map((remote) => [
    remote.name.toLowerCase(),
    { title: remote.name, jobs: [] as JobItem[], remote },
  ]));
  for (const { job, mounts: paths } of visible) {
    const mounts = new Set(paths.map((mount) => mount.toLowerCase()).filter((mount) => mount !== "本地"));
    const [mount] = mounts;
    const legacy = isLegacyOpenListJob(job, engines);
    const group = !legacy && mounts.size === 1 && mount ? groups.get(mount) : undefined;
    (group ?? (!legacy && mounts.size === 0 ? local : custom)).jobs.push(job);
  }
  const visibleGroups = [...groups.values(), local, custom].filter(
    (group) => group.jobs.length || (!filtered && group.remote),
  );
  return (
    <div className="sync-overview-groups">
      {(jobs.length > 0 || filtered) && (
        <div className="sync-list-controls">
          <div className="sync-list-filters">
            <Checkbox
              aria-label="全选当前列表任务"
              checked={fullySelected}
              indeterminate={selectedIds.length > 0 && !fullySelected}
              disabled={!selectable.length || action.busy}
              onChange={(event) => setSelected(event.target.checked ? selectable.map((job) => job.id) : [])}
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
              value={[...storageNames].find(storageMatches) ?? "all"}
              onChange={(value) => setFilter("storage", String(value))}
              optionList={[
                { label: "全部存储", value: "all" },
                ...[...storageNames].map((name) => ({ label: name === "本地" ? "本地目录" : name, value: name })),
              ]}
            />
            <span className="sr-only" id={`${filterId}-storage`}>按存储筛选</span>
            <Select
              aria-labelledby={`${filterId}-direction`}
              value={direction}
              onChange={(value) => setFilter("direction", String(value))}
              optionList={directionOptions}
            />
            <span className="sr-only" id={`${filterId}-direction`}>按同步方向筛选</span>
            <Select
              aria-labelledby={`${filterId}-status`}
              value={status}
              onChange={(value) => setFilter("status", String(value))}
              optionList={statusOptions}
            />
            <span className="sr-only" id={`${filterId}-status`}>按运行状态筛选</span>
            {filtered && <Button size="small" theme="borderless" onClick={resetFilters}>重置筛选</Button>}
          </div>
          {selectedIds.length ? (
            <div className="batch-action-bar">
              <span className="batch-action-summary">已选中 <strong>{selectedIds.length}</strong> 个任务</span>
              <div className="batch-action-buttons">
                <Button size="small" disabled={action.busy} onClick={() => runBatch("run", "批量立即同步")}>批量立即同步</Button>
                <Button size="small" type="danger" disabled={action.busy} onClick={() => runBatch("stop", "批量中止传输")}>批量中止传输</Button>
                <Button size="small" disabled={action.busy} onClick={() => runBatch("enable", "批量恢复同步")}>批量恢复同步</Button>
                <Button size="small" disabled={action.busy} onClick={() => runBatch("disable", "批量暂停同步")}>批量暂停同步</Button>
                <Button size="small" theme="borderless" onClick={() => setSelected([])}>取消选择</Button>
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
      {!visibleGroups.length && (
        filtered ? <p className="muted" role="status">没有符合当前筛选条件的同步任务</p> : <EmptyState />
      )}
      {visibleGroups.map((group) => (
        <SectionCard
          key={group.remote ? `remote:${group.remote.name}` : group.title}
          title={group.title}
          className="sync-connection-group"
          bodyClassName="sync-connection-body"
          extra={group.remote && (
            <Button size="small" type="tertiary" onClick={() => onAdd(group.remote?.name)}>
              添加同步任务
            </Button>
          )}
        >
          {!group.jobs.length && <p className="muted">此连接还没有同步任务</p>}
          {group.jobs.map((job) => {
            const status = resolveEffectiveSyncStatus(job);
            const { arrow } = getContinuousSyncMeta(job);
            const legacy = isLegacyOpenListJob(job, engines);
            const details = `/tasks?jobId=${job.id}&tab=overview`;
            const needsAuth = group.remote && /登录状态已失效|认证失败|令牌已过期/.test(status.error);
            const localPermission = /本地.*(?:无权限|未授权|权限不足|权限被拒绝)|未授权(?:的)?本地|无法读取本地授权目录/.test(status.error);
            const lastSuccess = formatRelativeSyncTime(status.lastSuccess);
            const waitingForRetry = status.state === "error" && !needsAuth && !localPermission &&
              (status.retryAt ?? 0) > Date.now() / 1000;
            const mirrorNeedsReview = requiresFirstMirrorPreview(job);
            return (
              <div className="sync-overview-row" key={job.id}>
                {!legacy && (
                  <Checkbox
                    aria-label={`选择任务 ${getJobName(job)}`}
                    checked={selectedIds.includes(job.id)}
                    disabled={action.busy}
                    onChange={(event) => setSelected((previous) => event.target.checked
                      ? [...previous, job.id]
                      : previous.filter((id) => id !== job.id))}
                  />
                )}
                <div className="sync-overview-task">
                  <div>
                    <Link to={details}>{getJobName(job)}</Link>
                    {legacy ? (
                      <span className="muted">旧版任务已停用</span>
                    ) : waitingForRetry ? (
                      <span className="muted">等待重试</span>
                    ) : job.isCron !== 3 && status.state === "monitoring" ? (
                      failedTaskStatuses.includes(job.lastStatus ?? 0) ? (
                        <span className="sync-overview-error">上次同步异常</span>
                      ) : (
                        <span className="muted">{job.isCron === 2 ? "仅手动" : "等待计划"}</span>
                      )
                    ) : (
                      <SyncStatusTag status={status} />
                    )}
                  </div>
                  <p className="sync-overview-path">
                    {formatDisplayPathList(parseJobPathList(job.srcPath)).join("、")} {arrow} {formatDisplayPathList(parseJobPathList(job.dstPath)).join("、")}
                  </p>
                  {lastSuccess && <div className="muted">最近成功 {lastSuccess}</div>}
                  {status.state === "error" && status.error && <div className="sync-overview-error">{status.error}</div>}
                </div>
                {!legacy && (
                  <div className="sync-overview-actions">
                    {status.state === "error" && (needsAuth && group.remote ? (
                      <Button size="small" onClick={() => onRepair(group.remote)}>重新授权</Button>
                    ) : localPermission ? (
                      <Button size="small" onClick={onLocalFolders}>查看本地授权</Button>
                    ) : null)}
                    {Boolean(status.conflictCount) && (
                      <Link to={`${details}&conflicts=1`}>查看冲突</Link>
                    )}
                    {mirrorNeedsReview && (
                      <Link to={`${details}&mirrorPreview=1`}>预览并启动</Link>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </SectionCard>
      ))}
    </div>
  );
}
