import { Link } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import { EmptyState, SectionCard } from "../components/common";
import {
  formatRelativeSyncTime,
  getContinuousSyncMeta,
  resolveEffectiveSyncStatus,
  SyncStatusTag,
} from "../components/SyncStatus";
import { formatDisplayPathList } from "../lib/syncPathPreview";
import { requiresFirstMirrorPreview } from "../lib/taskForm";
import { getJobName, parseJobPathList } from "./Home/homeUtils";
import type { JobItem, NativeRemoteItem } from "../types";

export function SyncOverview({
  jobs,
  remotes,
  busy,
  onAdd,
  onToggle,
  onRetry,
  onRepair,
  onLocalFolders,
}: {
  jobs: JobItem[];
  remotes: NativeRemoteItem[];
  busy: boolean;
  onAdd: (remote?: string) => void;
  onToggle: (job: JobItem) => void;
  onRetry: (job: JobItem) => void;
  onRepair: (remote: NativeRemoteItem) => void;
  onLocalFolders: () => void;
}) {
  const local = { title: "本地目录", jobs: [] as JobItem[], remote: undefined };
  const custom = { title: "其他同步", jobs: [] as JobItem[], remote: undefined };
  const groups = new Map(remotes.map((remote) => [
    remote.name.toLowerCase(),
    { title: remote.name, jobs: [] as JobItem[], remote },
  ]));
  for (const job of jobs) {
    const mounts = new Set(
      [...parseJobPathList(job.srcPath), ...parseJobPathList(job.dstPath)]
        .map((path) => path.split("/").filter(Boolean)[0]?.toLowerCase())
        .filter((mount) => mount && mount !== "本地"),
    );
    const [mount] = mounts;
    const group = !job.legacyOpenList && mounts.size === 1 && mount ? groups.get(mount) : undefined;
    (group ?? (!job.legacyOpenList && mounts.size === 0 ? local : custom)).jobs.push(job);
  }
  const visibleGroups = [...groups.values(), ...[local, custom].filter((group) => group.jobs.length)];
  if (!visibleGroups.length) {
    return <EmptyState />;
  }
  return (
    <div className="sync-overview-groups">
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
            const details = `/tasks?jobId=${job.id}&tab=overview`;
            const needsAuth = group.remote && /登录状态已失效|认证失败|令牌已过期/.test(status.error);
            const localPermission = /本地.*(?:无权限|未授权|权限不足|权限被拒绝)|未授权(?:的)?本地|无法读取本地授权目录/.test(status.error);
            const lastSuccess = formatRelativeSyncTime(status.lastSuccess);
            const waitingForRetry = status.state === "error" && !needsAuth && !localPermission &&
              (status.retryAt ?? 0) > Date.now() / 1000;
            const mirrorNeedsReview = requiresFirstMirrorPreview(job);
            return (
              <div className="sync-overview-row" key={job.id}>
                <div className="sync-overview-task">
                  <div>
                    <Link to={details}>{getJobName(job)}</Link>
                    {job.legacyOpenList ? (
                      <span className="muted">旧版任务已停用</span>
                    ) : waitingForRetry ? (
                      <span className="muted">等待重试</span>
                    ) : job.isCron !== 3 && status.state === "monitoring" ? (
                      <span className="muted">{job.isCron === 2 ? "仅手动" : "等待计划"}</span>
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
                {!job.legacyOpenList && (
                  <div className="sync-overview-actions">
                    {status.state === "error" && (needsAuth && group.remote ? (
                      <Button size="small" onClick={() => onRepair(group.remote)}>重新授权</Button>
                    ) : localPermission ? (
                      <Button size="small" onClick={onLocalFolders}>查看本地授权</Button>
                    ) : job.enable === 1 && !job.running ? (
                      <Button size="small" disabled={busy} onClick={() => onRetry(job)}>立即重试</Button>
                    ) : null)}
                    {Boolean(status.conflictCount) && (
                      <Link to={`${details}&conflicts=1`}>查看冲突</Link>
                    )}
                    {job.isCron === 2 ? (
                      <Link to={details}>查看任务</Link>
                    ) : mirrorNeedsReview ? (
                      <Link to={`${details}&mirrorPreview=1`}>预览并启动</Link>
                    ) : (
                      <Button size="small" type="tertiary" disabled={busy} onClick={() => onToggle(job)}>
                        {job.enable === 1 ? "暂停同步" : "恢复同步"}
                      </Button>
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
