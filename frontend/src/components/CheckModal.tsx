import { useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Spin from "@douyinfe/semi-ui/lib/es/spin";
import Table from "@douyinfe/semi-ui/lib/es/table";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import {
  IconAlertTriangle,
  IconDownload,
  IconRefresh,
  IconTickCircle,
  IconWrench,
} from "@douyinfe/semi-icons";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { api } from "../api/client";
import { EmptyState, InlineNotice, confirmAction, errorToast } from "./common";
import type { JobCheckResult } from "../types";

type CheckTab = "differ" | "missingOnDst" | "missingOnSrc" | "errors";

export function CheckModal({
  jobId,
  jobName,
  hasCryptRemote = false,
  onClose,
  onRepairStarted,
}: {
  jobId: number;
  jobName: string;
  hasCryptRemote?: boolean;
  onClose: () => void;
  onRepairStarted?: () => void;
}) {
  const [checkMode, setCheckMode] = useState<"check" | "download" | "cryptcheck">("check");
  const [result, setResult] = useState<JobCheckResult | null>(null);
  const [starting, setStarting] = useState(false);
  const [repairing, setRepairing] = useState(false);
  const [error, setError] = useState("");
  const [activeTab, setActiveTab] = useState<CheckTab>("differ");
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const pollAbortRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(true);
  const runGenRef = useRef(0);

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

  const startPolling = (gen: number) => {
    stopPolling();
    pollRef.current = setInterval(async () => {
      if (!mountedRef.current || runGenRef.current !== gen) {
        stopPolling();
        return;
      }
      pollAbortRef.current?.abort();
      const controller = new AbortController();
      pollAbortRef.current = controller;
      try {
        const latest = await api.getJobCheck(jobId, controller.signal);
        if (
          controller.signal.aborted ||
          !mountedRef.current ||
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
  };

  const startCheck = async (mode: "check" | "download" | "cryptcheck") => {
    const gen = ++runGenRef.current;
    stopPolling();
    setStarting(true);
    setError("");
    try {
      const initial = await api.startJobCheck(
        jobId,
        mode === "download",
        mode === "cryptcheck",
      );
      if (!mountedRef.current || runGenRef.current !== gen) return;
      setResult(initial);
      setStarting(false);
      if (initial.status === "running") {
        startPolling(gen);
      }
    } catch (err) {
      if (!mountedRef.current || runGenRef.current !== gen) return;
      setStarting(false);
      setError(err instanceof Error ? err.message : "启动一致性校验失败");
    }
  };

  useEffect(() => {
    mountedRef.current = true;
    // Check if a verification session already exists before starting a new one.
    const gen = ++runGenRef.current;
    const controller = new AbortController();
    void (async () => {
      try {
        const existing = await api.getJobCheck(jobId, controller.signal);
        if (controller.signal.aborted || !mountedRef.current || runGenRef.current !== gen) {
          return;
        }
        if (existing) {
          setResult(existing);
          setCheckMode(
            existing.cryptCheck || existing.mode === "cryptcheck"
              ? "cryptcheck"
              : existing.download
                ? "download"
                : "check",
          );
          if (existing.status === "running") {
            startPolling(gen);
          }
          return;
        }
      } catch {
        /* ignore initial lookup failure */
      }
      if (!controller.signal.aborted && mountedRef.current && runGenRef.current === gen) {
        void startCheck("check");
      }
    })();
    return () => {
      mountedRef.current = false;
      runGenRef.current += 1;
      controller.abort();
      stopPolling();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId]);

  // Closing the modal keeps a running check active in the background so users
  // can continue navigating without losing verification progress.
  const handleDismiss = () => {
    runGenRef.current += 1;
    stopPolling();
    onClose();
  };

  const handleCancelCheck = async () => {
    runGenRef.current += 1;
    stopPolling();
    if (result?.status === "running") {
      try {
        await api.cancelJobCheck(jobId);
        Toast.info("已中止一致性校验");
      } catch (err) {
        errorToast(err);
      }
    }
    onClose();
  };

  const handleExport = () => {
    const link = document.createElement("a");
    link.href = api.exportJobCheckUrl(jobId);
    link.download = `check-report-job-${jobId}.csv`;
    document.body.appendChild(link);
    link.click();
    link.remove();
  };

  const handleRepair = () => {
    if (!result) return;
    const count = result.differCount + result.missingOnDstCount;
    confirmAction({
      title: `一键修复 ${count} 个差异/缺失文件？`,
      content:
        "将仅对校验发现的「内容差异」与「目标缺失」文件执行强制覆盖同步，不会删除目标端多余文件。",
      okText: "立即修复",
      onOk: async () => {
        setRepairing(true);
        try {
          const res = await api.repairJobCheck(jobId);
          Toast.success(`已提交定向修复（共 ${res.repairCount} 个文件），请在实时任务查看进度`);
          window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
          onRepairStarted?.();
          handleDismiss();
        } catch (err) {
          errorToast(err);
        } finally {
          setRepairing(false);
        }
      },
    });
  };

  const isRunning = starting || result?.status === "running";
  const issueTotal = result
    ? result.differCount +
      result.missingOnDstCount +
      result.missingOnSrcCount +
      result.errorCount
    : 0;

  const activeRows = useMemo(() => {
    if (!result) return [];
    const list =
      activeTab === "differ"
        ? result.differ
        : activeTab === "missingOnDst"
          ? result.missingOnDst
          : activeTab === "missingOnSrc"
            ? result.missingOnSrc
            : result.errors;
    return (list || []).map((path, idx) => ({ id: `${activeTab}-${idx}`, path }));
  }, [result, activeTab]);

  // Auto-select first non-empty tab when results arrive.
  useEffect(() => {
    if (!result || result.status === "running") return;
    if (result.differ.length > 0) setActiveTab("differ");
    else if (result.missingOnDst.length > 0) setActiveTab("missingOnDst");
    else if (result.missingOnSrc.length > 0) setActiveTab("missingOnSrc");
    else if (result.errors.length > 0) setActiveTab("errors");
  }, [result?.status, result?.finishedAt]);

  return (
    <Modal
      visible
      width={780}
      centered
      className="editor-modal dry-run-modal"
      title={`数据一致性校验 · ${jobName}`}
      onCancel={handleDismiss}
      footer={
        <div className="editor-actions editor-actions--wrap">
          <Button
            type="tertiary"
            className="editor-actions-start"
            icon={<IconRefresh aria-hidden="true" />}
            disabled={isRunning || repairing}
            onClick={() => void startCheck(checkMode)}
          >
            重新校验
          </Button>
          {result && result.status === "completed" && (
            <>
              <Button
                type="tertiary"
                icon={<IconDownload aria-hidden="true" />}
                onClick={handleExport}
              >
                导出 CSV
              </Button>
              {result.differCount + result.missingOnDstCount > 0 && (
                <Button
                  type="primary"
                  theme="solid"
                  icon={<IconWrench aria-hidden="true" />}
                  loading={repairing}
                  onClick={handleRepair}
                >
                  一键修复 ({result.differCount + result.missingOnDstCount})
                </Button>
              )}
            </>
          )}
          {isRunning && (
            <Button
              type="danger"
              theme="light"
              onClick={() => void handleCancelCheck()}
            >
              中止校验
            </Button>
          )}
          <Button type="tertiary" onClick={handleDismiss}>
            {isRunning ? "后台继续" : "关闭"}
          </Button>
        </div>
      }
    >
      <div className="dry-run-body">
        <div className="dry-run-note check-modal-toolbar">
          <span>
            调用 rclone {checkMode === "cryptcheck" ? "cryptcheck" : "check"} 只读比对源目录与目标目录的文件校验信息，验证备份完整性，不会修改任何文件。
          </span>
          <div className="native-checkbox-group">
            <Checkbox
              checked={checkMode === "download"}
              disabled={isRunning}
              onChange={(e) =>
                setCheckMode(Boolean(e.target.checked) ? "download" : "check")
              }
            >
              逐字节下载校验（--download，适合无哈希远端，耗时更长）
            </Checkbox>
            {hasCryptRemote && (
              <Checkbox
                checked={checkMode === "cryptcheck"}
                disabled={isRunning}
                onChange={(e) =>
                  setCheckMode(Boolean(e.target.checked) ? "cryptcheck" : "check")
                }
              >
                Crypt 密文校验（rclone cryptcheck，核验加密远端密文哈希与一致性）
              </Checkbox>
            )}
          </div>
        </div>

        {error && (
          <InlineNotice
            role="alert"
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message={error}
            onRetry={() => void startCheck(checkMode)}
          />
        )}

        {isRunning && (
          <div className="dry-run-progress">
            <Spin size="small" />
            <span>
              正在执行一致性校验
              {result && result.totalFiles > 0
                ? `（已核验 ${result.checkedFiles} / ${result.totalFiles} 个文件）`
                : result && result.checkedFiles > 0
                  ? `（已核验 ${result.checkedFiles} 个文件）`
                  : "…"}
            </span>
          </div>
        )}

        {result && result.status === "failed" && result.error && (
          <InlineNotice
            role="alert"
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message={`一致性校验失败：${result.error}`}
          />
        )}

        {result && (
          <>
            <div className="dry-run-kpis">
              <div className="dry-run-kpi">
                <span className="dry-run-kpi-label">校验模式</span>
                <strong>
                  {result.cryptCheck || result.mode === "cryptcheck"
                    ? "Crypt 密文"
                    : result.download
                      ? "逐字节"
                      : "哈希/大小"}
                </strong>
                <span className="muted">
                  已核验 {result.checkedFiles} 项
                </span>
              </div>
              <div className="dry-run-kpi">
                <span className="dry-run-kpi-label">完全一致</span>
                <strong>{result.matchedCount}</strong>
                <span className="muted">源目标匹配</span>
              </div>
              <div className={`dry-run-kpi${result.differCount > 0 ? " is-danger" : ""}`}>
                <span className="dry-run-kpi-label">内容差异</span>
                <strong>{result.differCount}</strong>
                <span className="muted">大小或哈希不同</span>
              </div>
              <div className={`dry-run-kpi${result.missingOnDstCount > 0 ? " is-danger" : ""}`}>
                <span className="dry-run-kpi-label">目标缺失</span>
                <strong>{result.missingOnDstCount}</strong>
                <span className="muted">仅源端存在</span>
              </div>
              <div className="dry-run-kpi">
                <span className="dry-run-kpi-label">目标多余</span>
                <strong>{result.missingOnSrcCount}</strong>
                <span className="muted">仅目标端存在</span>
              </div>
              <div className={`dry-run-kpi${result.errorCount > 0 ? " is-danger" : ""}`}>
                <span className="dry-run-kpi-label">读取错误</span>
                <strong>{result.errorCount}</strong>
                <span className="muted">无法比对</span>
              </div>
            </div>

            {result.status === "completed" && issueTotal === 0 && (
              <div className="dry-run-clean">
                <IconTickCircle aria-hidden="true" />
                <span>
                  校验通过：全部 {result.matchedCount} 个文件源端与目标端完全一致。
                </span>
              </div>
            )}

            {result.truncated && (
              <div className="field-hint">
                差异明细过多，单类列表仅展示前 500 条；上方汇总统计覆盖全部校验结果。
              </div>
            )}

            {issueTotal > 0 && (
              <>
                <Tabs
                  size="small"
                  activeKey={activeTab}
                  onChange={(k) => setActiveTab(k as CheckTab)}
                >
                  <Tabs.TabPane
                    tab={`内容差异 (${result.differCount})`}
                    itemKey="differ"
                  />
                  <Tabs.TabPane
                    tab={`目标缺失 (${result.missingOnDstCount})`}
                    itemKey="missingOnDst"
                  />
                  <Tabs.TabPane
                    tab={`目标多余 (${result.missingOnSrcCount})`}
                    itemKey="missingOnSrc"
                  />
                  <Tabs.TabPane
                    tab={`读取错误 (${result.errorCount})`}
                    itemKey="errors"
                  />
                </Tabs>
                <div className="dry-run-table-wrap">
                  <Table<{ id: string; path: string }>
                    size="small"
                    pagination={{ pageSize: 20, size: "small" }}
                    rowKey="id"
                    dataSource={activeRows}
                    empty={<EmptyState />}
                    columns={[
                      {
                        title: "类别",
                        width: 110,
                        render: () => {
                          const map: Record<
                            CheckTab,
                            { label: string; color: "red" | "orange" | "blue" }
                          > = {
                            differ: { label: "内容差异", color: "red" },
                            missingOnDst: { label: "目标缺失", color: "orange" },
                            missingOnSrc: { label: "目标多余", color: "blue" },
                            errors: { label: "读取错误", color: "red" },
                          };
                          const item = map[activeTab];
                          return (
                            <Tag size="small" type="light" color={item.color}>
                              {item.label}
                            </Tag>
                          );
                        },
                      },
                      {
                        title: "文件路径 / 详情",
                        dataIndex: "path",
                        render: (text: string) => (
                          <span className="mono">{text}</span>
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
