import { useCallback, useEffect, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { IconAlertTriangle, IconRefresh, IconTickCircle } from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { errorToast } from "./common";
import { formatSize } from "../pages/Home/homeUtils";
import { formatTimestamp } from "../utils/date";
import type { BisyncConflict } from "../types";

const conflictTypeMeta: Record<
  string,
  { label: string; color: "amber" | "orange" | "red" | "blue" }
> = {
  content: { label: "内容冲突", color: "amber" },
  case: { label: "大小写冲突", color: "orange" },
  type: { label: "文件/目录冲突", color: "orange" },
  invalid_name: { label: "文件名不兼容", color: "red" },
};

export function BisyncConflictsModal({
  jobId,
  jobName,
  leftPath,
  rightPath,
  onClose,
  onResolved,
}: {
  jobId: number;
  jobName: string;
  leftPath: string;
  rightPath: string;
  onClose: () => void;
  onResolved?: () => void;
}) {
  const [loading, setLoading] = useState(true);
  const [includeResolved, setIncludeResolved] = useState(false);
  const [items, setItems] = useState<BisyncConflict[]>([]);
  const [busyId, setBusyId] = useState<number | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadFailed(false);
    try {
      const data = await api.getJobConflicts(jobId, includeResolved);
      setItems(Array.isArray(data) ? data : []);
    } catch (err) {
      setLoadFailed(true);
      errorToast(err);
    } finally {
      setLoading(false);
    }
  }, [jobId, includeResolved]);

  useEffect(() => {
    void load();
  }, [load]);

  const handleResolve = async (
    conflict: BisyncConflict,
    action: "keep_local" | "keep_remote" | "keep_both" | "dismiss",
  ) => {
    if (busyId !== null) return;
    setBusyId(conflict.id);
    try {
      await api.resolveJobConflict(jobId, conflict.id, action);
      const label =
        action === "keep_local"
          ? "已保留左侧版本并清理冲突副本"
          : action === "keep_remote"
            ? "已保留右侧版本并清理冲突副本"
            : action === "keep_both"
              ? "已确认保留双方文件"
              : "已标记处理";
      Toast.success(label);
      await load();
      onResolved?.();
    } catch (err) {
      errorToast(err);
    } finally {
      setBusyId(null);
    }
  };

  const unresolvedCount = items.filter((item) => !item.resolved).length;

  return (
    <Modal
      visible
      title={`双向同步冲突处理 · ${jobName}`}
      width={720}
      onCancel={onClose}
      footer={
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", width: "100%" }}>
          <Checkbox
            checked={includeResolved}
            onChange={(e) => setIncludeResolved(Boolean(e.target.checked))}
          >
            显示已处理记录
          </Checkbox>
          <div style={{ display: "flex", gap: 8 }}>
            <Button
              icon={<IconRefresh aria-hidden="true" />}
              disabled={loading || busyId !== null}
              onClick={() => void load()}
            >
              刷新
            </Button>
            <Button type="primary" theme="solid" onClick={onClose}>
              关闭
            </Button>
          </div>
        </div>
      }
    >
      <div style={{ marginBottom: 12, overflowWrap: "anywhere" }}>
        <div>左侧目录 <code>{leftPath}</code></div>
        <div>右侧目录 <code>{rightPath}</code></div>
      </div>
      {loading ? (
        <div style={{ padding: "36px 0", textAlign: "center" }}>
          <Spin />
        </div>
      ) : loadFailed ? (
        <div role="alert">读取冲突列表失败，请刷新重试。</div>
      ) : items.length === 0 ? (
        <div style={{ padding: "32px 16px", textAlign: "center", color: "var(--semi-color-text-2)" }}>
          <IconTickCircle
            size="extra-large"
            style={{ color: "var(--semi-color-success)", marginBottom: 8 }}
            aria-hidden="true"
          />
          <div style={{ fontWeight: 500, color: "var(--semi-color-text-0)", marginBottom: 4 }}>
            当前没有待处理的冲突
          </div>
          <div style={{ fontSize: 13 }}>
            基于 SQLite 三方合并基线，双向变更已自动协调一致。
          </div>
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: 12, maxHeight: "60vh", overflowY: "auto" }}>
          {unresolvedCount > 0 && (
            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: 8,
                padding: "8px 12px",
                borderRadius: 8,
                background: "var(--semi-color-warning-light-default)",
                color: "var(--semi-color-text-0)",
                fontSize: 13,
              }}
            >
              <IconAlertTriangle style={{ color: "var(--semi-color-warning)" }} aria-hidden="true" />
              <span>
                共 {unresolvedCount} 项待处理冲突。冲突副本已安全保留在两端，不会阻塞其余文件同步。
              </span>
            </div>
          )}
          {items.map((item) => {
            const meta = conflictTypeMeta[item.conflictType] || {
              label: item.conflictType,
              color: "amber" as const,
            };
            const isBusy = busyId === item.id;
            const canPickSide =
              !item.resolved &&
              item.conflictType === "content" &&
              Boolean(item.conflictCopyPath);
            return (
              <div
                key={item.id}
                style={{
                  border: "1px solid var(--semi-color-border)",
                  borderRadius: 10,
                  padding: "12px 14px",
                  display: "flex",
                  flexDirection: "column",
                  gap: 8,
                  opacity: item.resolved ? 0.65 : 1,
                }}
              >
                <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8, flexWrap: "wrap" }}>
                  <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
                    <Tag color={item.resolved ? "green" : meta.color} size="small">
                      {item.resolved ? "已处理" : meta.label}
                    </Tag>
                    <code style={{ fontWeight: 600, wordBreak: "break-all" }}>{item.relPath}</code>
                  </div>
                  <span style={{ fontSize: 12, color: "var(--semi-color-text-2)" }}>
                    {item.createdAt ? formatTimestamp(item.createdAt) : ""}
                  </span>
                </div>

                {item.detail && (
                  <div style={{ fontSize: 13, color: "var(--semi-color-text-1)" }}>{item.detail}</div>
                )}

                {item.conflictCopyPath && (
                  <div style={{ fontSize: 12, color: "var(--semi-color-text-2)" }}>
                    冲突副本：<code>{item.conflictCopyPath}</code>
                  </div>
                )}

                {(item.aSize > 0 || item.bSize > 0) && (
                  <div style={{ display: "flex", gap: 16, fontSize: 12, color: "var(--semi-color-text-2)", flexWrap: "wrap" }}>
                    <span>
                      左侧：{formatSize(item.aSize)}
                      {item.aModTime > 0 ? ` · ${formatTimestamp(item.aModTime)}` : ""}
                    </span>
                    <span>
                      右侧：{formatSize(item.bSize)}
                      {item.bModTime > 0 ? ` · ${formatTimestamp(item.bModTime)}` : ""}
                    </span>
                  </div>
                )}

                {!item.resolved && (
                  <div style={{ display: "flex", gap: 8, flexWrap: "wrap", paddingTop: 2 }}>
                    {canPickSide ? (
                      <>
                        <Button
                          size="small"
                          type="primary"
                          theme="light"
                          loading={isBusy}
                          disabled={busyId !== null && !isBusy}
                          onClick={() => void handleResolve(item, "keep_local")}
                        >
                          保留左侧版本
                        </Button>
                        <Button
                          size="small"
                          type="primary"
                          theme="light"
                          loading={isBusy}
                          disabled={busyId !== null && !isBusy}
                          onClick={() => void handleResolve(item, "keep_remote")}
                        >
                          保留右侧版本
                        </Button>
                        <Button
                          size="small"
                          type="tertiary"
                          loading={isBusy}
                          disabled={busyId !== null && !isBusy}
                          onClick={() => void handleResolve(item, "keep_both")}
                        >
                          两者都留
                        </Button>
                      </>
                    ) : (
                      <Button
                        size="small"
                        type="tertiary"
                        loading={isBusy}
                        disabled={busyId !== null && !isBusy}
                        onClick={() => void handleResolve(item, "dismiss")}
                      >
                        标记已处理
                      </Button>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </Modal>
  );
}
