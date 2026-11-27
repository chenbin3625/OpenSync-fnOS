import Tag from "@douyinfe/semi-ui/lib/es/tag";
import type { SyncStatus } from "../types";
import { syncStatusLabels, syncStatusTone } from "../lib/continuousSync";

export {
  formatContinuousSyncPlan,
  formatMinutesShort,
  formatRelativeSyncTime,
  formatSecondsShort,
  getContinuousSyncMeta,
  isSyncStatusError,
  jobStatusDotClass,
  parseJobExtraFlagsObject,
  resolveEffectiveSyncStatus,
  syncStatusLabels,
  syncStatusTone,
  type ContinuousSyncMeta,
  type SyncDirectionKey,
  type SyncStatusTone,
} from "../lib/continuousSync";

export function SyncStatusTag({ status }: { status?: SyncStatus | null }) {
  if (!status) return null;
  const tone = syncStatusTone(status.state);
  const color =
    tone === "danger"
      ? "red"
      : tone === "success"
        ? "green"
        : tone === "warning"
          ? "orange"
          : tone === "muted"
            ? "grey"
            : "blue";
  const title =
    status.error ||
    (status.lastSuccess > 0
      ? `最近成功同步：${new Date(status.lastSuccess * 1000).toLocaleString("zh-CN", { hour12: false })}`
      : "等待首次同步");
  return (
    <span className="sync-status-tag" title={title}>
      <Tag size="small" type="light" color={color}>
        {syncStatusLabels[status.state] || status.state}
      </Tag>
    </span>
  );
}
