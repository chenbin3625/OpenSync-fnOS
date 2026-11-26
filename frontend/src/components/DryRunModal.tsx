import { useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Table from "@douyinfe/semi-ui/lib/es/table";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconAlertTriangle,
  IconRefresh,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { EmptyState, InlineNotice, errorToast } from "./common";
import { formatSize, methodLabel } from "../pages/Home/homeUtils";
import { formatDisplayPath } from "../lib/syncPathPreview";
import type { DryRunItem, DryRunResult } from "../types";

const actionMeta: Record<
  string,
  { label: string; color: "blue" | "orange" | "red" | "purple" | "cyan" | "grey" }
> = {
  copy: { label: "新增复制", color: "blue" },
  update: { label: "覆盖更新", color: "orange" },
  delete: { label: "删除", color: "red" },
  move: { label: "移动", color: "purple" },
  mkdir: { label: "创建目录", color: "cyan" },
  skip: { label: "跳过", color: "grey" },
};

export function DryRunModal({
  jobId,
  jobName,
  onClose,
  onEnableMirror,
}: {
  jobId: number;
  jobName: string;
  onClose: () => void;
  onEnableMirror: () => Promise<void>;
}) {
  const [result, setResult] = useState<DryRunResult | null>(null);
  const [starting, setStarting] = useState(true);
  const [enabling, setEnabling] = useState(false);
  const [error, setError] = useState("");
  const [filterAction, setFilterAction] = useState("all");
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const pollAbortRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(true);
  const cancelledRef = useRef(false);
  const runGenRef = useRef(0);
  const startingRef = useRef(false);

  const stopPolling = () => {
    if (pollRef.current !== null) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
    if (pollAbortRef.current) {
      pollAbortRef.current.abort();
      pollAbortRef.current = null;
    }
  };

  const start = async () => {
    const gen = ++runGenRef.current;
    cancelledRef.current = false;
    stopPolling();
    startingRef.current = true;
    setStarting(true);
    setError("");
    setFilterAction("all");
    try {
      const initial = await api.startDryRun(jobId);
      if (runGenRef.current === gen) {
        startingRef.current = false;
      }
      if (!mountedRef.current || cancelledRef.current || runGenRef.current !== gen) {
        if ((!mountedRef.current || cancelledRef.current) && initial?.status === "running") {
          void api.cancelDryRun(jobId).catch(() => {
            /* best-effort cleanup after late start response */
          });
        }
        return;
      }
      setResult(initial);
      setStarting(false);
      if (initial.status === "running") {
        pollRef.current = setInterval(async () => {
          if (!mountedRef.current || cancelledRef.current || runGenRef.current !== gen) {
            stopPolling();
            return;
          }
          pollAbortRef.current?.abort();
          const controller = new AbortController();
          pollAbortRef.current = controller;
          try {
            const latest = await api.getDryRun(jobId, controller.signal);
            if (
              controller.signal.aborted ||
              !mountedRef.current ||
              cancelledRef.current ||
              runGenRef.current !== gen
            ) {
              return;
            }
            if (latest) {
              setResult(latest);
              if (latest.status !== "running") stopPolling();
            }
          } catch {
            /* transient poll error ignored */
          }
        }, 1200);
      }
    } catch (err) {
      if (runGenRef.current === gen) {
        startingRef.current = false;
      }
      if (!mountedRef.current || cancelledRef.current || runGenRef.current !== gen) {
        return;
      }
      setStarting(false);
      setError(err instanceof Error ? err.message : "启动镜像预览失败");
    }
  };

  useEffect(() => {
    mountedRef.current = true;
    let active = true;
    queueMicrotask(() => {
      if (active) void start();
    });
    return () => {
      active = false;
      mountedRef.current = false;
      cancelledRef.current = true;
      runGenRef.current += 1;
      stopPolling();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId]);

  const handleCancel = async () => {
    if (enabling) return;
    cancelledRef.current = true;
    runGenRef.current += 1;
    stopPolling();
    if (startingRef.current || result?.status === "running") {
      startingRef.current = false;
      try {
        await api.cancelDryRun(jobId);
      } catch (err) {
        errorToast(err);
      }
    }
    onClose();
  };

  const items = result?.items || [];
  const filteredItems = useMemo(() => {
    if (filterAction === "all") return items;
    return items.filter((item) => item.action === filterAction);
  }, [items, filterAction]);

  const isRunning = starting || result?.status === "running";
  const hasChanges =
    result &&
    result.copyCount +
      result.updateCount +
      result.deleteCount +
      result.moveCount +
      result.mkdirCount >
      0;

  return (
    <Modal
      visible
      width={780}
      centered
      className="editor-modal dry-run-modal"
      title={`镜像影响预览 · ${jobName}`}
      maskClosable={!enabling}
      closable={!enabling}
      onCancel={() => void handleCancel()}
      footer={
        <div className="editor-actions">
          <Button
            type="tertiary"
            className="editor-actions-start"
            icon={<IconRefresh aria-hidden="true" />}
            disabled={isRunning || enabling}
            onClick={() => void start()}
          >
            重新预览
          </Button>
          <Button type="tertiary" onClick={() => void handleCancel()} disabled={enabling}>
            {isRunning ? "中止预览" : "关闭"}
          </Button>
          {!isRunning && result?.status === "completed" && !error && (
            <Button
              type="danger"
              theme="solid"
              loading={enabling}
              onClick={async () => {
                setEnabling(true);
                try {
                  await onEnableMirror();
                } catch (err) {
                  errorToast(err);
                } finally {
                  setEnabling(false);
                }
              }}
            >
              确认并启动镜像
            </Button>
          )}
        </div>
      }
    >
      <div className="dry-run-body">
        <div className="dry-run-note">
          预览期间任务保持暂停。启动后会重新检查目录，并清理目标端独有的文件。
        </div>

        {error && (
          <InlineNotice
            role="alert"
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message={error}
            onRetry={() => void start()}
          />
        )}

        {isRunning && (
          <div className="dry-run-progress">
            <Spin size="small" />
            <span>
              正在只读扫描并比对目录
              {result ? `（已扫描 ${result.scannedDirs} 个目录）` : "…"}
            </span>
          </div>
        )}

        {result && result.status === "failed" && result.error && (
          <InlineNotice
            role="alert"
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message={`镜像预览失败：${result.error}`}
          />
        )}

        {result && (
          <>
            <div className="dry-run-kpis">
              <div className="dry-run-kpi">
                <span className="dry-run-kpi-label">同步方式</span>
                <strong>{methodLabel(result.method)}</strong>
                <span className="muted">扫描 {result.scannedDirs} 目录</span>
              </div>
              <div className="dry-run-kpi">
                <span className="dry-run-kpi-label">新增复制</span>
                <strong>{result.copyCount}</strong>
                <span className="muted">{formatSize(result.copySize)}</span>
              </div>
              <div className="dry-run-kpi">
                <span className="dry-run-kpi-label">覆盖更新</span>
                <strong>{result.updateCount}</strong>
                <span className="muted">{formatSize(result.updateSize)}</span>
              </div>
              {(result.method === 1 || result.method === 3) && (
                <div className="dry-run-kpi is-danger">
                  <span className="dry-run-kpi-label">预计删除</span>
                  <strong>{result.deleteCount}</strong>
                  <span className="muted">{formatSize(result.deleteSize)}</span>
                </div>
              )}
              {result.method === 2 && (
                <div className="dry-run-kpi">
                  <span className="dry-run-kpi-label">预计移动</span>
                  <strong>{result.moveCount}</strong>
                  <span className="muted">{formatSize(result.moveSize)}</span>
                </div>
              )}
              <div className="dry-run-kpi">
                <span className="dry-run-kpi-label">创建目录 / 跳过</span>
                <strong>
                  {result.mkdirCount} / {result.skipCount}
                </strong>
                <span className="muted">
                  共 {result.totalItems} 项变更明细
                </span>
              </div>
            </div>

            {result.status === "completed" && !hasChanges && (
              <div className="dry-run-clean">
                <IconTickCircle aria-hidden="true" />
                <span>源端与目标端已一致，本次执行预计无需同步任何变更。</span>
              </div>
            )}

            {result.itemsTruncated && (
              <div className="field-hint">
                变更条目过多，当前仅展示前 {items.length} 条（总计 {result.totalItems} 条）；上方汇总统计仍覆盖全部扫描结果。
              </div>
            )}

            {items.length > 0 && (
              <>
                <Tabs
                  size="small"
                  activeKey={filterAction}
                  onChange={setFilterAction}
                >
                  <Tabs.TabPane tab={`全部 (${items.length})`} itemKey="all" />
                  {(["copy", "update", "move", "delete", "mkdir", "skip"] as const).map(
                    (key) => {
                      const count = items.filter((i) => i.action === key).length;
                      if (!count) return null;
                      return (
                        <Tabs.TabPane
                          key={key}
                          tab={`${actionMeta[key].label} (${count})`}
                          itemKey={key}
                        />
                      );
                    },
                  )}
                </Tabs>
                <div className="dry-run-table-wrap">
                  <Table<DryRunItem>
                    size="small"
                    pagination={{ pageSize: 20, size: "small" }}
                    rowKey={(record) =>
                      `${record?.action}:${record?.srcPath}:${record?.dstPath}:${record?.fileName}`
                    }
                    dataSource={filteredItems}
                    empty={<EmptyState />}
                    columns={[
                      {
                        title: "动作",
                        width: 96,
                        render: (_, record) => {
                          const meta = actionMeta[record.action] || {
                            label: record.action,
                            color: "grey" as const,
                          };
                          return (
                            <Tag size="small" type="light" color={meta.color}>
                              {meta.label}
                            </Tag>
                          );
                        },
                      },
                      {
                        title: "文件 / 路径",
                        render: (_, record) => (
                          <div className="file-name">
                            <Tooltip content={record.fileName} position="topLeft">
                              <strong>{record.fileName}</strong>
                            </Tooltip>
                            <span className="muted">
                              {formatDisplayPath(
                                record.dstPath || record.srcPath || "",
                              ) || "—"}
                            </span>
                          </div>
                        ),
                      },
                      {
                        title: "大小",
                        width: 92,
                        render: (_, record) =>
                          record.isPath ? "目录" : formatSize(record.fileSize || 0),
                      },
                      {
                        title: "说明",
                        width: 140,
                        render: (_, record) => (
                          <span className="muted">{record.reason || "—"}</span>
                        ),
                      },
                    ]}
                  />
                </div>
              </>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}
