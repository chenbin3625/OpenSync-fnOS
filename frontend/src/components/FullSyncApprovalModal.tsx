import Button from "@douyinfe/semi-ui/lib/es/button";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import { IconAlertTriangle } from "@douyinfe/semi-icons";
import { formatSize } from "../pages/Home/homeUtils";
import { formatTimestamp } from "../utils/date";
import type { FullSyncApproval } from "../types";

export function PendingApprovalBanner({
  approval,
  busy,
  onInspect,
  onReject,
}: {
  approval: FullSyncApproval;
  busy: boolean;
  onInspect: () => void;
  onReject: () => void;
}) {
  const executing = approval.status === "executing";
  return (
    <div
      className={`task-approval-banner${executing ? " is-executing" : ""}`}
      role={executing ? "status" : "alert"}
    >
      <div className="task-approval-banner-main">
        <span className="task-approval-banner-icon" aria-hidden="true">
          {executing ? <Spin size="small" /> : <IconAlertTriangle />}
        </span>
        <div className="task-approval-banner-copy">
          <div className="task-approval-banner-title">
            <strong>
              {executing
                ? "正在后台核对并执行目标端删除…"
                : `待确认删除目标端 ${approval.deleteCount} 项${
                    approval.deleteSize > 0
                      ? `（${formatSize(approval.deleteSize)}）`
                      : ""
                  }`}
            </strong>
            {approval.taskId ? (
              <Tag size="small" type="light" color="grey">
                任务 #{approval.taskId}
              </Tag>
            ) : null}
          </div>
          <span className="task-approval-banner-sub">
            {executing
              ? "删除操作在后台执行，不会阻塞后续同步任务。"
              : `文件同步已完成且不受阻塞；目标端多余文件将保留至您确认后再删除${
                  approval.expiresAt && approval.expiresAt > 0
                    ? `（将于 ${formatTimestamp(approval.expiresAt)} 自动过期）`
                    : ""
                }。`}
          </span>
          {!executing && approval.planChanged && (
            <span className="task-approval-banner-warn">
              目标端待删除内容在等待期间发生了变化，已更新为最新清单，请重新核对。
            </span>
          )}
          {!executing && approval.lastError && (
            <span className="task-approval-banner-error">
              上次删除核对未完成：{approval.lastError}
            </span>
          )}
        </div>
      </div>
      {!executing && (
        <div className="task-approval-banner-actions">
          <Button
            size="small"
            type="tertiary"
            disabled={busy}
            onClick={onReject}
          >
            跳过删除
          </Button>
          <Button
            size="small"
            type="danger"
            theme="solid"
            disabled={busy}
            onClick={onInspect}
          >
            查看清单并审批
          </Button>
        </div>
      )}
    </div>
  );
}

export function FullSyncApprovalModal({
  approval,
  jobName,
  loading = false,
  busy,
  onClose,
  onApprove,
  onReject,
}: {
  approval: FullSyncApproval;
  jobName?: string;
  loading?: boolean;
  busy: boolean;
  onClose: () => void;
  onApprove: () => Promise<unknown>;
  onReject: () => Promise<unknown>;
}) {
  const visiblePaths = approval.paths || [];
  const remaining = Math.max(0, approval.pathCount - visiblePaths.length);
  const executing = approval.status === "executing";
  const taskId = approval.taskId;
  return (
    <Modal
      visible
      width={540}
      className="fnos-confirm full-sync-approval-modal"
      centered
      closable={!busy}
      closeOnEsc={!busy}
      maskClosable={!busy}
      onCancel={onClose}
      title={jobName ? `确认删除目标端文件 · ${jobName}` : "确认删除目标端多余文件"}
      footer={
        <div className="editor-actions">
          <Button
            type="tertiary"
            className="editor-actions-start"
            onClick={onClose}
            disabled={busy}
          >
            稍后处理
          </Button>
          <Button
            type="tertiary"
            onClick={() => void onReject()}
            disabled={busy || executing}
          >
            跳过本次删除
          </Button>
          <Button
            type="danger"
            theme="solid"
            loading={busy || executing}
            disabled={loading}
            onClick={() => void onApprove()}
          >
            {executing ? "正在删除…" : "确认删除"}
          </Button>
        </div>
      }
    >
      <div className="full-sync-approval">
        <p className="full-sync-approval-lead">
          {taskId ? `任务 #${taskId} 的` : "本次"}文件同步已完成（未阻塞同步任务），预计删除目标端{" "}
          <strong>{approval.deleteCount} 个文件或目录</strong>
          {approval.deleteSize > 0
            ? `（${formatSize(approval.deleteSize)}）`
            : ""}
          。
        </p>
        {approval.planChanged && (
          <div className="approval-modal-notice is-warning" role="alert">
            目标端待删除内容在等待期间发生了变化，系统已自动取消上次删除并刷新最新清单，请重新核对后再确认。
          </div>
        )}
        {approval.lastError && (
          <div className="approval-modal-notice is-error" role="alert">
            上次核对失败（未删除任何文件）：{approval.lastError}
          </div>
        )}
        <p className="field-hint">
          确认后将在后台再次核对源端与目标端状态再执行清理，不影响后续同步任务；若源端已恢复对应文件将自动跳过，若开启回收站将移入回收站。
        </p>
        {loading && visiblePaths.length === 0 ? (
          <div className="full-sync-approval-loading">
            <Spin size="small" />
            <span className="muted">正在加载待删除路径清单…</span>
          </div>
        ) : (
          <div className="full-sync-approval-list" role="list">
            {visiblePaths.map((item) => (
              <div
                className="full-sync-approval-item"
                role="listitem"
                key={`${item.action}:${item.path}`}
              >
                <Tag
                  size="small"
                  type="light"
                  color={item.action === "删除" ? "red" : "blue"}
                >
                  {item.action}
                </Tag>
                <span className="full-sync-approval-path">{item.path}</span>
                <span className="muted">
                  {item.isPath
                    ? `${item.count} 项${item.size > 0 ? ` · ${formatSize(item.size)}` : ""}`
                    : formatSize(item.size)}
                </span>
              </div>
            ))}
          </div>
        )}
        {remaining > 0 && (
          <div className="field-hint">还有 {remaining} 个路径未展开。</div>
        )}
        <div className="field-hint">
          {approval.expiresAt && approval.expiresAt > 0
            ? `此确认清单将于 ${formatTimestamp(approval.expiresAt, true)} 过期，超时未确认将自动保留目标端文件。`
            : "点击「稍后处理」可先关闭窗口，清单将保留直至您确认或跳过。"}
        </div>
      </div>
    </Modal>
  );
}
