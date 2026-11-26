import { memo, useEffect, useId, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import DatePicker from "@douyinfe/semi-ui/lib/es/datePicker";
import Divider from "@douyinfe/semi-ui/lib/es/divider";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import SideSheet from "@douyinfe/semi-ui/lib/es/sideSheet";
import Table from "@douyinfe/semi-ui/lib/es/table";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconAlertTriangle,
  IconDeleteStroked,
  IconDownload,
  IconEyeOpenedStroked,
  IconPause,
  IconPlay,
  IconRefresh,
  IconStop,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  DataPager,
  EmptyState,
  FilterSelect,
  IconButton,
  InlineNotice,
  LoadingPanel,
  LoadState,
  SearchFilterInput,
  Status,
  confirmDelete,
  confirmStopTask,
  errorToast,
} from "../components/common";
import { useAction, useMediaQuery, useResource } from "../lib/hooks";
import { historyRangeParams } from "../lib/historyFilters";
import { useClampedPage } from "../lib/pagination";
import { formatDisplayPath } from "../lib/syncPathPreview";
import { useRealtimeTask } from "./Home/useRealtimeTask";
import { useRealtimeTaskItems } from "./Home/useRealtimeTaskItems";
import {
  formatDuration,
  formatSize,
  getTaskDisplayName,
  normalizeDateRange,
  taskItemStatusNames,
  taskRecordStatusNames,
  taskTypeNames,
} from "./Home/homeUtils";
import {
  calcTaskDisplayPercent,
  taskProgressPercent,
} from "./Home/taskRows";
import { formatTimestamp } from "../utils/date";
import type { TaskItem, TaskRecord } from "../types";

const statusTabs = [
  { key: 0, label: "等待", count: "wait" },
  { key: 1, label: "运行中", count: "running" },
  { key: 2, label: "成功", count: "success" },
  { key: 7, label: "失败", count: "fail" },
  { key: -1, label: "其他", count: "other" },
] as const;

const historyStatuses = [1, 2, 3, 4, 5, 6, 7, 8];

const fileTableWidths = {
  select: 40,
  name: 340,
  size: 96,
  action: 76,
  status: 160,
  ops: 56,
} as const;

export const Realtime = memo(function Realtime({
  jobId,
  onTaskEnded,
}: {
  jobId: number;
  onTaskEnded?: () => void;
}) {
  const {
    currentTask,
    loading: taskLoading,
    error: taskError,
    refreshCurrentTask,
  } = useRealtimeTask(String(jobId), true);
  const items = useRealtimeTaskItems({
    jobId: String(jobId),
    enabled: true,
    currentTask,
    pageSize: 10,
  });
  const action = useAction();
  const hadTask = useRef(false);
  useEffect(() => {
    if (currentTask) {
      hadTask.current = true;
      return;
    }
    if (taskLoading || taskError || !hadTask.current) return;
    if (document.visibilityState === "hidden") return;
    hadTask.current = false;
    onTaskEnded?.();
  }, [currentTask, taskLoading, taskError, onTaskEnded]);
  if (!currentTask) {
    if (taskError)
      return (
        <LoadState
          loading={false}
          error={taskError}
          retry={refreshCurrentTask}
        />
      );
    if (taskLoading) return <LoadingPanel />;
    return <EmptyState />;
  }
  const task = currentTask;
  const activeTab = items.activeTab;
  const tabItems = items.tabTaskList;
  const tabTotal = items.tabTaskTotal;
  const total = task.doneSize + task.remainSize;
  const totalItems =
    (task.num?.wait || 0) +
    (task.num?.running || 0) +
    (task.num?.success || 0) +
    (task.num?.fail || 0) +
    (task.num?.other || 0);
  const percent = calcTaskDisplayPercent({
    doneSize: task.doneSize,
    totalSize: total,
    num: task.num,
    scanFinish: task.scanFinish,
  });
  const stop = () =>
    confirmStopTask(() =>
      action.run(async () => {
        await api.taskAction(task.taskId, "stop");
        window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
        await refreshCurrentTask();
      }),
    );
  const togglePause = () => {
    const resuming = Boolean(task.paused);
    void action.run(async () => {
      try {
        await api.taskAction(task.taskId, resuming ? "resume" : "pause");
        Toast.success(resuming ? "已继续传输" : "已暂停传输");
        window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
        await refreshCurrentTask();
      } catch (err) {
        errorToast(err);
      }
    });
  };
  const updateLiveBwLimit = (rate: string) =>
    void action.run(async () => {
      try {
        await api.taskAction(task.taskId, "bwlimit", undefined, { bwLimit: rate });
        Toast.success(rate && rate !== "off" ? `实时限速已调整为 ${rate}` : "已解除实时限速");
        await refreshCurrentTask();
      } catch (err) {
        errorToast(err);
      }
    });
  const currentBw = (task.liveBwLimit || "").trim();
  const bwOptions = [
    { label: "不限速", value: "off" },
    { label: "限速 5M/s", value: "5M" },
    { label: "限速 10M/s", value: "10M" },
    { label: "限速 20M/s", value: "20M" },
    { label: "限速 50M/s", value: "50M" },
    { label: "限速 100M/s", value: "100M" },
  ];
  const bwSelectValue =
    !currentBw || currentBw.toLowerCase() === "off"
      ? "off"
      : bwOptions.some((o) => o.value === currentBw)
        ? currentBw
        : currentBw;
  const effectiveBwOptions = bwOptions.some((o) => o.value === bwSelectValue)
    ? bwOptions
    : [{ label: `限速 ${bwSelectValue}`, value: bwSelectValue }, ...bwOptions];
  return (
    <div className="execution-view flex-column">
      <div className="execution-top">
        <div className="execution-header">
          <h2 className="execution-header-title">
            <span>
              {task.paused
                ? "传输已暂停"
                : task.scanFinish
                  ? "正在同步"
                  : task.firstSync
                    ? "正在同步（扫描中）"
                    : "正在扫描目录"}
            </span>
            {task.paused && (
              <Tag size="small" color="orange" type="light">
                已暂停
              </Tag>
            )}
          </h2>
          <div className="execution-header-actions">
            <Select
              size="small"
              aria-label="运行中动态限速"
              className="execution-bw-select"
              value={bwSelectValue}
              optionList={effectiveBwOptions}
              disabled={action.busy}
              onChange={(v) => updateLiveBwLimit(String(v || "off"))}
            />
            <Button
              type={task.paused ? "primary" : "tertiary"}
              theme={task.paused ? "solid" : "light"}
              size="small"
              icon={task.paused ? <IconPlay aria-hidden="true" /> : <IconPause aria-hidden="true" />}
              onClick={togglePause}
              disabled={action.busy}
            >
              {task.paused ? "继续传输" : "暂停"}
            </Button>
            <Button
              type="danger"
              size="small"
              icon={<IconStop aria-hidden="true" />}
              onClick={stop}
              disabled={action.busy}
            >
              停止任务
            </Button>
          </div>
        </div>
        <div className="execution-metrics">
          <span>实时 {formatSize(task.speed)}/s</span>
          {task.speedAvg > 0 && (
            <>
              <Divider layout="vertical" margin={8} />
              <span>均速 {formatSize(task.speedAvg)}/s</span>
            </>
          )}
          <Divider layout="vertical" margin={8} />
          <span>
            已完成 {formatSize(task.doneSize)} /{" "}
            {formatSize(task.doneSize + task.remainSize)}
            {totalItems > 0
              ? `（${task.num?.success || 0} / ${totalItems} 项）`
              : ""}
          </span>
          <Divider layout="vertical" margin={8} />
          <span>已运行 {formatDuration(task.duration)}</span>
          {task.remainTime > 0 && !task.paused && (
            <>
              <Divider layout="vertical" margin={8} />
              <span>预计剩余 {formatDuration(task.remainTime)}</span>
            </>
          )}
        </div>
        <Progress
          percent={Math.round(percent)}
          showInfo
          stroke={
            task.paused
              ? "var(--semi-color-warning)"
              : "var(--semi-color-primary)"
          }
        />
        {taskError && (
          <InlineNotice
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message="连接中断，正在重试…"
          />
        )}
        {!task.scanFinish && task.scan && (
          <div className="scan-status">
            已扫描 {task.scan.scannedDirs} 个目录
            {task.scan.totalFiles
              ? ` · 共 ${task.scan.totalFiles} 个文件${
                  task.scan.checkedFiles
                    ? `（已核对 ${task.scan.checkedFiles}）`
                    : ""
                }`
              : ` · 待扫描 ${task.scan.remainingDirs} 个目录`}
          </div>
        )}
        <Tabs
          activeKey={String(activeTab)}
          onChange={(key) => items.setActiveTab(Number(key))}
          className="execution-tabs"
        >
          {statusTabs.map((tab) => (
            <Tabs.TabPane
              key={tab.key}
              itemKey={String(tab.key)}
              tab={`${tab.label} ${task.num?.[tab.count] || 0}`}
            />
          ))}
        </Tabs>
      </div>
      <div className="execution-scroll">
        {items.tabError && tabItems.length > 0 && (
          <InlineNotice
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message="明细连接中断，正在重试…"
          />
        )}
        {items.tabError && tabItems.length === 0 ? (
          <LoadState
            loading={false}
            error={items.tabError}
            retry={items.retryTabTasks}
          />
        ) : (
          <FileTable
            key={items.tableKey}
            rows={tabItems}
            loading={items.tabLoading}
          />
        )}
      </div>
      <DataPager
        total={tabTotal}
        page={items.tabTaskPage}
        size={items.pageSize}
        onChange={items.setTabTaskPage}
        onSizeChange={items.setPageSize}
      />
    </div>
  );
});

export const History = memo(function History({
  jobId,
  currentTaskId,
  refreshKey = 0,
  disableRetry = false,
  onOpenRealtime,
}: {
  jobId: number;
  currentTaskId?: number;
  refreshKey?: number;
  disableRetry?: boolean;
  onOpenRealtime: () => void;
}) {
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(10);
  const [status, setStatus] = useState<number | undefined>();
  const [input, setInput] = useState("");
  const [keyword, setKeyword] = useState("");
  const [range, setRange] = useState<Date[]>([]);
  const [detail, setDetail] = useState<number | null>(null);
  const [selectedTaskIds, setSelectedTaskIds] = useState<number[]>([]);
  const resource = useResource(
    (signal) =>
      api.history(
        jobId,
        {
          pageNum: page,
          pageSize: size,
          status,
          statusIn: status === undefined ? historyStatuses : undefined,
          keyword,
          ...historyRangeParams(range),
        },
        signal,
      ),
    [jobId, page, size, status, keyword, range],
  );
  const refreshHistory = resource.refresh;
  const prevCurrentTaskId = useRef(currentTaskId);
  useEffect(() => {
    if (prevCurrentTaskId.current === currentTaskId) return;
    prevCurrentTaskId.current = currentTaskId;
    void refreshHistory(true);
  }, [currentTaskId, refreshHistory]);
  const prevRefreshKey = useRef(refreshKey);
  useEffect(() => {
    if (prevRefreshKey.current === refreshKey) return;
    prevRefreshKey.current = refreshKey;
    void refreshHistory(true);
  }, [refreshKey, refreshHistory]);
  const action = useAction();
  const rows = resource.data?.dataList || [];
  const totalCount = resource.data?.count || 0;
  const isMobile = useMediaQuery("(max-width: 640px)");
  useClampedPage(page, resource.data ? totalCount : null, size, setPage);
  const rangeLabelId = useId();

  // Clear selections when page/job changes
  useEffect(() => {
    setSelectedTaskIds([]);
  }, [jobId, page, size, status, keyword, range]);

  const deletableRows = useMemo(
    () => rows.filter((r) => r.status !== 1),
    [rows],
  );
  const selectedSet = useMemo(() => new Set(selectedTaskIds), [selectedTaskIds]);
  const allDeletableSelected =
    deletableRows.length > 0 &&
    deletableRows.every((r) => selectedSet.has(r.id));

  const retry = (record: TaskRecord) =>
    void action.run(async () => {
      try {
        await api.taskAction(record.id, "retry");
        Toast.success("已提交重试");
        await resource.refresh(true);
      } catch (error) {
        errorToast(error);
      }
    });

  const handleBatchDelete = () => {
    if (!selectedTaskIds.length) return;
    confirmDelete(
      `删除选中的 ${selectedTaskIds.length} 条执行记录？`,
      () =>
        action.run(async () => {
          const res = await api.deleteTasksBatch(selectedTaskIds);
          setSelectedTaskIds([]);
          Toast.success(`已删除 ${res.deleted} 条记录`);
          await resource.refresh(true);
        }),
      "相关执行明细将一并清理，无法恢复。",
    );
  };

  const handleClearAll = () => {
    confirmDelete(
      "清空此任务的全部已完成历史记录？",
      () =>
        action.run(async () => {
          const res = await api.clearJobHistory(jobId);
          setSelectedTaskIds([]);
          Toast.success(`已清空 ${res.deleted} 条历史记录`);
          await resource.refresh(true);
        }),
      "正在运行的记录会保留，已完成的历史记录和明细将被彻底删除。",
    );
  };

  const renderCounts = (record: TaskRecord) => {
    if (record.status === 1) return "—";
    const all = record.allNum ?? 0;
    const ok = record.successNum ?? 0;
    const fail = record.failNum ?? 0;
    if (all === 0 && ok === 0 && fail === 0) return "0 项";
    return (
      <span className="history-counts">
        <span>
          {ok}/{all}
        </span>
        {fail > 0 && <span className="inline-error">（失败 {fail}）</span>}
      </span>
    );
  };

  const controls = (record: TaskRecord) =>
    record.status === 1 ? (
      <Button
        size="small"
        theme="borderless"
        disabled={record.id !== currentTaskId}
        onClick={onOpenRealtime}
      >
        查看实时进度
      </Button>
    ) : (
      <div className="row-actions">
        <IconButton
          label="查看执行明细"
          icon={<IconEyeOpenedStroked aria-hidden="true" />}
          onClick={() => setDetail(record.id)}
        />
        {!disableRetry && (
          <IconButton
            label={
              record.status === 4 || record.status === 5
                ? "继续上次中断 / 重试未完成项"
                : "重试未完成项"
            }
            icon={<IconRefresh aria-hidden="true" />}
            disabled={action.busy}
            onClick={() => retry(record)}
          />
        )}
        <IconButton
          label="删除执行记录"
          icon={<IconDeleteStroked aria-hidden="true" />}
          danger
          onClick={() =>
            confirmDelete("删除此执行记录？", () =>
              action.run(async () => {
                await api.deleteTask(record.id);
                await resource.refresh(true);
              }),
            )
          }
        />
      </div>
    );
  return (
    <div className="history-view flex-column">
      <div className="filter-bar">
        <SearchFilterInput
          ariaLabel="搜索执行记录（任务 ID）"
          placeholder="任务 ID"
          value={input}
          onChange={setInput}
          onSearch={(next) => {
            setKeyword(next);
            setPage(1);
          }}
        />
        <FilterSelect
          label="执行状态"
          allLabel="全部状态"
          value={status}
          options={historyStatuses.map((value) => ({
            value,
            label: taskRecordStatusNames[value],
          }))}
          onChange={(next) => {
            setStatus(next);
            setKeyword(input.trim());
            setPage(1);
          }}
        />
        <span id={rangeLabelId} className="sr-only">
          执行时间范围
        </span>
        <DatePicker
          type="dateRange"
          aria-labelledby={rangeLabelId}
          value={range}
          onChange={(value) => {
            setRange(normalizeDateRange(value));
            setKeyword(input.trim());
            setPage(1);
          }}
          showClear
        />
        <div className="history-toolbar-actions">
          {selectedTaskIds.length > 0 && (
            <Button
              size="small"
              type="danger"
              theme="light"
              icon={<IconDeleteStroked aria-hidden="true" />}
              disabled={action.busy}
              onClick={handleBatchDelete}
            >
              批量删除 ({selectedTaskIds.length})
            </Button>
          )}
          {totalCount > 0 && (
            <Button
              size="small"
              type="tertiary"
              disabled={action.busy}
              onClick={handleClearAll}
            >
              清空已完成
            </Button>
          )}
        </div>
      </div>
      <div className="history-scroll">
        <LoadState
          loading={false}
          error={resource.error}
          retry={resource.refresh}
        />
        {!resource.error &&
          (isMobile ? (
            <div className="mobile-data">
              {resource.loading ? (
                <LoadingPanel />
              ) : !rows.length ? (
                <EmptyState />
              ) : (
                rows.map((record) => (
                  <div className="mobile-record" key={record.id}>
                    <div className="record-title">
                      <strong>{formatTimestamp(record.createTime, true)}</strong>
                      <Status
                        status={record.status}
                        label={
                          taskRecordStatusNames[record.status] ||
                          String(record.status)
                        }
                        error={record.errMsg}
                      />
                    </div>
                    <div className="record-footer">
                      <span className="muted">
                        {record.status !== 1 && record.runTime && record.createTime
                          ? formatDuration(record.runTime - record.createTime)
                          : "—"}
                        {record.status !== 1 && record.allNum !== undefined
                          ? ` · ${record.successNum ?? 0}/${record.allNum} 项`
                          : ""}
                        {(record.sumSize ?? 0) > 0
                          ? ` · ${formatSize(record.sumSize || 0)}`
                          : ""}
                      </span>
                      {controls(record)}
                    </div>
                  </div>
                ))
              )}
            </div>
          ) : (
            <div className="desktop-data">
              <Table<TaskRecord>
                dataSource={rows}
                loading={resource.loading}
                pagination={false}
                rowKey="id"
                empty={<EmptyState />}
                columns={[
                  {
                    title: (
                      <Checkbox
                        aria-label="全选本页可删除记录"
                        checked={allDeletableSelected}
                        indeterminate={
                          selectedTaskIds.length > 0 && !allDeletableSelected
                        }
                        disabled={!deletableRows.length}
                        onChange={(e) =>
                          setSelectedTaskIds(
                            e.target.checked
                              ? deletableRows.map((r) => r.id)
                              : [],
                          )
                        }
                      />
                    ),
                    width: 44,
                    render: (_, record) =>
                      record.status === 1 ? null : (
                        <Checkbox
                          aria-label={`选择记录 #${record.id}`}
                          checked={selectedSet.has(record.id)}
                          onChange={(e) =>
                            setSelectedTaskIds((prev) =>
                              e.target.checked
                                ? [...prev, record.id]
                                : prev.filter((id) => id !== record.id),
                            )
                          }
                        />
                      ),
                  },
                  {
                    title: "开始时间",
                    dataIndex: "createTime",
                    render: (value) => formatTimestamp(value, true),
                  },
                  {
                    title: "状态",
                    render: (_, record) => (
                      <Status
                        status={record.status}
                        label={
                          taskRecordStatusNames[record.status] ||
                          String(record.status)
                        }
                        error={record.errMsg}
                      />
                    ),
                  },
                  {
                    title: "文件数",
                    width: 130,
                    render: (_, record) => renderCounts(record),
                  },
                  {
                    title: "传输大小",
                    width: 108,
                    render: (_, record) =>
                      record.status !== 1 && (record.sumSize ?? 0) > 0
                        ? formatSize(record.sumSize || 0)
                        : "—",
                  },
                  {
                    title: "耗时",
                    width: 108,
                    render: (_, record) =>
                      record.status !== 1 && record.runTime && record.createTime
                        ? formatDuration(record.runTime - record.createTime)
                        : "—",
                  },
                  {
                    title: "操作",
                    width: 140,
                    render: (_, record) => controls(record),
                  },
                ]}
              />
            </div>
          ))}
      </div>
      <DataPager
        total={totalCount}
        page={page}
        size={size}
        onChange={setPage}
        onSizeChange={(s) => {
          setSize(s);
          setPage(1);
        }}
      />
      {detail !== null && (
        <FileDetails
          taskId={detail}
          disableRetry={disableRetry}
          onClose={() => setDetail(null)}
          onRetried={() => void resource.refresh(true)}
        />
      )}
    </div>
  );
});

/**
 * Stable row identity. Records without an id (realtime rows not yet saved)
 * fall back to every field that tells two entries apart — never the index,
 * which re-associated per-row state (an expanded mobile card) on page changes.
 */
export function fileRowKey(record?: TaskItem): string {
  if (record?.id) return String(record.id);
  return [record?.type, record?.srcPath, record?.dstPath, record?.fileName]
    .map((part) => String(part ?? ""))
    .join("\u0000");
}

function isRetryableItem(record: TaskItem): boolean {
  return (
    typeof record.id === "number" &&
    record.id > 0 &&
    record.status !== 2 &&
    record.status !== 1
  );
}

export const FileTable = memo(function FileTable({
  rows,
  loading = false,
  selectedIds,
  onToggleSelect,
  onToggleSelectAll,
  onRetrySingle,
  busy = false,
}: {
  rows: TaskItem[];
  loading?: boolean;
  selectedIds?: Set<number>;
  onToggleSelect?: (itemId: number, checked: boolean) => void;
  onToggleSelectAll?: (checked: boolean) => void;
  onRetrySingle?: (itemId: number) => void;
  busy?: boolean;
}) {
  const isMobile = useMediaQuery("(max-width: 640px)");
  const selectableRows = useMemo(() => rows.filter(isRetryableItem), [rows]);
  const allSelected =
    Boolean(selectedIds) &&
    selectableRows.length > 0 &&
    selectableRows.every((r) => selectedIds!.has(Number(r.id)));
  const someSelected =
    Boolean(selectedIds) &&
    selectableRows.some((r) => selectedIds!.has(Number(r.id))) &&
    !allSelected;

  const columns = useMemo(() => {
    const base: Array<Record<string, unknown>> = [];
    if (selectedIds && onToggleSelect && onToggleSelectAll) {
      base.push({
        title: (
          <Checkbox
            aria-label="全选本页可重试项"
            checked={allSelected}
            indeterminate={someSelected}
            disabled={!selectableRows.length}
            onChange={(e) => onToggleSelectAll(Boolean(e.target.checked))}
          />
        ),
        width: fileTableWidths.select,
        render: (_: unknown, record: TaskItem) =>
          isRetryableItem(record) ? (
            <Checkbox
              aria-label={`选择 ${getTaskDisplayName(record)}`}
              checked={selectedIds.has(Number(record.id))}
              onChange={(e) =>
                onToggleSelect(Number(record.id), Boolean(e.target.checked))
              }
            />
          ) : null,
      });
    }
    base.push(
      {
        title: "文件 / 目录",
        width: fileTableWidths.name,
        className: "file-col-name",
        render: (_: unknown, record: TaskItem) => {
          const itemPath =
            formatDisplayPath(record.dstPath || record.srcPath || "") || "—";
          return (
            <div className="file-name">
              <Tooltip content={getTaskDisplayName(record)} position="topLeft">
                <strong>{getTaskDisplayName(record)}</strong>
              </Tooltip>
              <Tooltip content={itemPath} position="topLeft">
                <span className="muted">{itemPath}</span>
              </Tooltip>
            </div>
          );
        },
      },
      {
        title: "大小",
        width: fileTableWidths.size,
        className: "file-col-size",
        render: (_: unknown, record: TaskItem) =>
          record.isPath ? "目录" : formatSize(record.fileSize || 0),
      },
      {
        title: "操作",
        width: fileTableWidths.action,
        className: "file-col-action",
        render: (_: unknown, record: TaskItem) => {
          const name = taskTypeNames[record.type || 0] || "—";
          return (
            <Tag
              size="small"
              type="light"
              color={record.type === 1 ? "red" : "blue"}
            >
              {name}
            </Tag>
          );
        },
      },
      {
        title: "状态",
        width: fileTableWidths.status,
        className: "file-col-status",
        render: (_: unknown, record: TaskItem) => {
          if (record.status === 1) {
            const pct = taskProgressPercent(record.progress);
            const hasDone =
              typeof record.doneBytes === "number" && record.doneBytes > 0;
            const hasSpeed =
              typeof record.speed === "number" && record.speed > 0;
            return (
              <div className="file-progress">
                <div className="file-progress-head">
                  <span className="file-progress-pct">{pct}%</span>
                  {(hasDone || hasSpeed) && (
                    <span className="file-progress-meta muted">
                      {hasDone ? formatSize(record.doneBytes!) : ""}
                      {hasDone && hasSpeed ? " · " : ""}
                      {hasSpeed ? `${formatSize(record.speed!)}/s` : ""}
                    </span>
                  )}
                </div>
                <Progress
                  percent={pct}
                  showInfo={false}
                  stroke="var(--semi-color-primary)"
                />
              </div>
            );
          }
          return (
            <Status
              status={record.status}
              label={taskItemStatusNames[record.status]}
              error={record.errMsg}
            />
          );
        },
      },
    );
    if (onRetrySingle) {
      base.push({
        title: "重试",
        width: fileTableWidths.ops,
        render: (_: unknown, record: TaskItem) =>
          isRetryableItem(record) ? (
            <IconButton
              label="仅重试此项"
              size="small"
              icon={<IconRefresh aria-hidden="true" />}
              disabled={busy}
              onClick={() => onRetrySingle(Number(record.id))}
            />
          ) : null,
      });
    }
    return base;
  }, [
    selectedIds,
    onToggleSelect,
    onToggleSelectAll,
    allSelected,
    someSelected,
    selectableRows.length,
    onRetrySingle,
    busy,
  ]);
  return (
    <>
      {!isMobile && (
        <div className="desktop-data">
          <Table<TaskItem>
            className="file-table"
            dataSource={rows}
            loading={loading}
            rowKey={fileRowKey}
            pagination={false}
            empty={<EmptyState />}
            columns={columns}
          />
        </div>
      )}
      {isMobile && (
        <div className="mobile-data">
          {loading ? (
            <LoadingPanel />
          ) : !rows.length ? (
            <EmptyState />
          ) : (
            rows.map((record) => (
              <MobileFileRecord
                key={fileRowKey(record)}
                record={record}
                onRetrySingle={onRetrySingle}
                busy={busy}
              />
            ))
          )}
        </div>
      )}
    </>
  );
});

export { normalizeDateRange };

const MobileFileRecord = memo(function MobileFileRecord({
  record,
  onRetrySingle,
  busy,
}: {
  record: TaskItem;
  onRetrySingle?: (itemId: number) => void;
  busy?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className={`mobile-record file-record${open ? " open" : ""}`}>
      <div className="record-summary" onClick={() => setOpen((v) => !v)}>
        <div className="record-title">
          <button
            type="button"
            className="record-title-toggle"
            aria-expanded={open}
            aria-label={`${getTaskDisplayName(record)}，${open ? "收起详情" : "展开详情"}`}
            onClick={(e) => {
              e.stopPropagation();
              setOpen((v) => !v);
            }}
          >
            <Tooltip content={getTaskDisplayName(record)} position="topLeft">
              <strong>{getTaskDisplayName(record)}</strong>
            </Tooltip>
          </button>
          <Status
            status={record.status}
            label={taskItemStatusNames[record.status]}
            error={record.errMsg}
          />
        </div>
        <div className="record-footer">
          <span className="muted">
            {taskTypeNames[record.type || 0]} ·{" "}
            {record.isPath ? "目录" : formatSize(record.fileSize || 0)}
          </span>
          {record.status === 1 && (
            <span>{taskProgressPercent(record.progress)}%</span>
          )}
          {onRetrySingle && isRetryableItem(record) && (
            <Button
              size="small"
              theme="borderless"
              disabled={busy}
              onClick={(e) => {
                e.stopPropagation();
                onRetrySingle(Number(record.id));
              }}
            >
              重试
            </Button>
          )}
        </div>
        {record.status === 1 && (
          <Progress
            percent={taskProgressPercent(record.progress)}
            stroke="var(--semi-color-primary)"
            showInfo={false}
          />
        )}
      </div>
      {open && (
        <div className="file-expanded">
          <div>
            <span>源目录</span>
            <p className="file-expanded-path">
              {formatDisplayPath(record.srcPath || "") || "—"}
            </p>
          </div>
          <div>
            <span>目标目录</span>
            <p className="file-expanded-path">
              {formatDisplayPath(record.dstPath || "") || "—"}
            </p>
          </div>
          {record.errMsg && (
            <div className="inline-error">
              <span>错误信息</span>
              <p>{record.errMsg}</p>
            </div>
          )}
        </div>
      )}
    </div>
  );
});

function FileDetails({
  taskId,
  disableRetry = false,
  onClose,
  onRetried,
}: {
  taskId: number;
  disableRetry?: boolean;
  onClose: () => void;
  onRetried?: () => void;
}) {
  const [page, setPage] = useState(1),
    [size, setSize] = useState(10);
  const [type, setType] = useState<number | undefined>(),
    [object, setObject] = useState<number | undefined>(),
    [hasError, setHasError] = useState<number | undefined>();
  const [input, setInput] = useState(""),
    [keyword, setKeyword] = useState("");
  const [selectedItemIds, setSelectedItemIds] = useState<number[]>([]);
  const action = useAction();

  const resource = useResource(
    (signal) =>
      api.items(
        taskId,
        {
          pageNum: page,
          pageSize: size,
          type,
          isPath: object,
          hasError,
          keyword,
        },
        signal,
      ),
    [taskId, page, size, type, object, hasError, keyword],
  );
  useClampedPage(
    page,
    resource.data ? resource.data.count || 0 : null,
    size,
    setPage,
  );
  useEffect(() => {
    setSelectedItemIds([]);
  }, [taskId, page, size, type, object, hasError, keyword]);

  const rows = resource.data?.dataList || [];
  const selectedSet = useMemo(() => new Set(selectedItemIds), [selectedItemIds]);

  const applyFilter =
    (setter: (value: number | undefined) => void) =>
    (value: number | undefined) => {
      setter(value);
      setKeyword(input.trim());
      setPage(1);
    };

  const triggerExport = (format: "csv" | "txt") => {
    const url = api.exportTaskItemsUrl(taskId, format, {
      type,
      isPath: object,
      hasError,
      keyword,
    });
    const link = document.createElement("a");
    link.href = url;
    link.download = `opensync-task-${taskId}.${format}`;
    document.body.appendChild(link);
    link.click();
    link.remove();
  };

  const retryItems = (itemIds: number[]) => {
    if (!itemIds.length) return;
    void action.run(async () => {
      try {
        await api.taskAction(taskId, "retry", itemIds);
        Toast.success(`已提交重试 ${itemIds.length} 项`);
        setSelectedItemIds([]);
        onRetried?.();
      } catch (err) {
        errorToast(err);
      }
    });
  };

  return (
    <SideSheet
      title={`执行明细 · 任务 #${taskId}`}
      visible
      onCancel={onClose}
      placement="bottom"
      height="100%"
      className="detail-drawer"
    >
      <div className="detail-drawer-body">
        <div className="filter-bar">
          <SearchFilterInput
            ariaLabel="搜索文件明细"
            placeholder="文件 / 路径 / 错误"
            value={input}
            onChange={setInput}
            onSearch={(next) => {
              setKeyword(next);
              setPage(1);
            }}
          />
          <FilterSelect
            label="操作类型"
            value={type}
            options={[
              { label: "复制/创建", value: 0 },
              { label: "删除", value: 1 },
              { label: "移动", value: 2 },
              { label: "移动（校验后删源）", value: 3 },
            ]}
            onChange={applyFilter(setType)}
          />
          <FilterSelect
            label="对象类型"
            value={object}
            options={[
              { label: "文件", value: 0 },
              { label: "目录", value: 1 },
            ]}
            onChange={applyFilter(setObject)}
          />
          <FilterSelect
            label="错误信息"
            value={hasError}
            options={[
              { label: "有错误信息", value: 1 },
              { label: "无错误信息", value: 0 },
            ]}
            onChange={applyFilter(setHasError)}
          />
          <Button
            theme="borderless"
            onClick={() => {
              setType(undefined);
              setObject(undefined);
              setHasError(undefined);
              setInput("");
              setKeyword("");
              setPage(1);
            }}
          >
            重置
          </Button>
          <div className="history-toolbar-actions">
            {!disableRetry && selectedItemIds.length > 0 && (
              <Button
                size="small"
                theme="solid"
                icon={<IconRefresh aria-hidden="true" />}
                loading={action.busy}
                onClick={() => retryItems(selectedItemIds)}
              >
                重试选中项 ({selectedItemIds.length})
              </Button>
            )}
            <Button
              size="small"
              type="tertiary"
              icon={<IconDownload aria-hidden="true" />}
              onClick={() => triggerExport("csv")}
            >
              导出 CSV
            </Button>
            <Button
              size="small"
              type="tertiary"
              icon={<IconDownload aria-hidden="true" />}
              onClick={() => triggerExport("txt")}
            >
              导出 TXT
            </Button>
          </div>
        </div>
        {resource.data?.errorGroups && resource.data.errorGroups.length > 0 && (
          <div className="error-groups-bar">
            <span className="error-groups-label muted">失败原因聚类：</span>
            {resource.data.errorGroups.slice(0, 8).map((g) => {
              const active = keyword === g.reason && hasError === 1;
              return (
                <button
                  key={g.reason}
                  type="button"
                  className={`schedule-chip${active ? " is-active" : ""}`}
                  title={g.reason}
                  onClick={() => {
                    if (active) {
                      setInput("");
                      setKeyword("");
                      setHasError(undefined);
                    } else {
                      setInput(g.reason);
                      setKeyword(g.reason);
                      setHasError(1);
                    }
                    setPage(1);
                  }}
                >
                  {g.reason.length > 36 ? `${g.reason.slice(0, 36)}…` : g.reason} ({g.count})
                </button>
              );
            })}
          </div>
        )}
        {resource.error ? (
          <LoadState
            loading={false}
            error={resource.error}
            retry={() => void resource.refresh()}
          />
        ) : (
          <FileTable
            rows={rows}
            loading={resource.loading}
            selectedIds={disableRetry ? undefined : selectedSet}
            onToggleSelect={
              disableRetry
                ? undefined
                : (id, checked) =>
                    setSelectedItemIds((prev) =>
                      checked
                        ? prev.includes(id)
                          ? prev
                          : [...prev, id]
                        : prev.filter((item) => item !== id),
                    )
            }
            onToggleSelectAll={
              disableRetry
                ? undefined
                : (checked) =>
                    setSelectedItemIds(
                      checked
                        ? rows.filter(isRetryableItem).map((r) => Number(r.id))
                        : [],
                    )
            }
            onRetrySingle={disableRetry ? undefined : (id) => retryItems([id])}
            busy={action.busy}
          />
        )}
      </div>
      <DataPager
        total={resource.data?.count || 0}
        page={page}
        size={size}
        onChange={setPage}
        onSizeChange={(s) => {
          setSize(s);
          setPage(1);
        }}
      />
    </SideSheet>
  );
}
