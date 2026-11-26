import Tag from "@douyinfe/semi-ui/lib/es/tag";
import type { SyncStatus } from "../types";

export const syncStatusLabels: Record<SyncStatus["state"], string> = {
  monitoring: "监控中",
  pending: "等待同步",
  scanning: "扫描中",
  syncing: "同步中",
  synced: "已同步",
  waiting_window: "等待运行窗口",
  waiting_approval: "等待删除确认",
  paused: "同步已暂停",
  error: "同步异常",
  needs_rebuild: "待重建基线",
};

export function SyncStatusTag({ status }: { status?: SyncStatus }) {
  if (!status) return null;
  const color =
    status.state === "error" || status.state === "needs_rebuild"
      ? "red"
      : status.state === "synced"
        ? "green"
        : status.state === "paused" ||
            status.state === "waiting_approval" ||
            status.state === "waiting_window"
          ? "orange"
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
