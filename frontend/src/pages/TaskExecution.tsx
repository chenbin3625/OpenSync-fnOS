import { memo, useEffect, useId, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import DatePicker from "@douyinfe/semi-ui/lib/es/datePicker";
import Divider from "@douyinfe/semi-ui/lib/es/divider";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Table from "@douyinfe/semi-ui/lib/es/table";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconAlertTriangle,
  IconChevronRight,
  IconDeleteStroked,
  IconDownload,
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
  confirmAction,
  confirmDelete,
  errorToast,
} from "../components/common";
import { describeBwLimit, formatBwRateHuman } from "../lib/bwLimit";
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
import type { TaskItem } from "../types";

const statusTabs = [
  { key: 0, label: "等待", count: "wait" },
  { key: 1, label: "运行中", count: "running" },
  { key: 2, label: "成功", count: "success" },
  { key: 7, label: "失败", count: "fail" },
  { key: -1, label: "其他", count: "other" },
] as const;

const recordStatusOptions = [
  { value: 2, label: "成功" },
  { value: 7, label: "已失败" },
  { value: 4, label: "已停止" },
  { value: 5, label: "超时" },
  { value: 6, label: "系统错误" },
  { value: 10, label: "重试中" },
  { value: -1, label: "其他状态" },
];

const bwOptions = [
  { label: "不限速", value: "off" },
  { label: "限速 5 MB/s", value: "5M" },
  { label: "限速 10 MB/s", value: "10M" },
  { label: "限速 20 MB/s", value: "20M" },
  { label: "限速 50 MB/s", value: "50M" },
  { label: "限速 100 MB/s", value: "100M" },
];

const fileTableWidths = {
  select: 40,
  name: 310,
  size: 92,
  action: 76,
  status: 152,
  time: 148,
  ops: 56,
} as const;

export const SyncRecords = memo(function SyncRecords({
  jobId,
  refreshKey = 0,
  initialHasError,
  disableRetry = false,
  onTaskEnded,
}: {
  jobId: number;
  refreshKey?: number;
  initialHasError?: number;
  disableRetry?: boolean;
  onTaskEnded?: () => void;
}) {
  const {
    currentTask,
    error: taskError,
    refreshCurrentTask,
  } = useRealtimeTask(String(jobId), true);
  const liveItems = useRealtimeTaskItems({
    jobId: String(jobId),
    enabled: true,
    currentTask,
    pageSize: 10,
  });

  const [page, setPage] = useState(1);
  const [size, setSize] = useState(20);
  const [status, setStatus] = useState<number | undefined>();
  const [type, setType] = useState<number | undefined>();
  const [object, setObject] = useState<number | undefined>();
  const [hasError, setHasError] = useState<number | undefined>(initialHasError);
  const [input, setInput] = useState("");
  const [keyword, setKeyword] = useState("");
  const [range, setRange] = useState<Date[]>([]);
  const [selectedItemIds, setSelectedItemIds] = useState<number[]>([]);
  const [errorsExpanded, setErrorsExpanded] = useState(false);

  const action = useAction();
  const rangeLabelId = useId();

  const recordsResource = useResource(
    (signal) =>
      api.jobItems(
        jobId,
        {
          pageNum: page,
          pageSize: size,
          status,
          type,
          isPath: object,
          hasError,
          keyword,
          ...historyRangeParams(range),
        },
        signal,
      ),
    [jobId, page, size, status, type, object, hasError, keyword, range],
  );
  const refreshRecords = recordsResource.refresh;

  const hadTask = useRef(false);
  useEffect(() => {
    if (currentTask) {
      hadTask.current = true;
      return;
    }
    if (!hadTask.current || taskError) return;
    if (document.visibilityState === "hidden") return;
    hadTask.current = false;
    onTaskEnded?.();
    void refreshRecords(true);
  }, [currentTask, taskError, onTaskEnded, refreshRecords]);

  const prevTaskId = useRef<number | undefined>(currentTask?.taskId);
  useEffect(() => {
    if (prevTaskId.current === currentTask?.taskId) return;
    prevTaskId.current = currentTask?.taskId;
    void refreshRecords(true);
  }, [currentTask?.taskId, refreshRecords]);

  const prevRefreshKey = useRef(refreshKey);
  useEffect(() => {
    if (prevRefreshKey.current === refreshKey) return;
    prevRefreshKey.current = refreshKey;
    void refreshRecords(true);
  }, [refreshKey, refreshRecords]);

  const totalCount = recordsResource.data?.count || 0;
  useClampedPage(
    page,
    recordsResource.data ? totalCount : null,
    size,
    setPage,
  );

  useEffect(() => {
    setSelectedItemIds([]);
  }, [jobId, page, size, status, type, object, hasError, keyword, range]);

  useEffect(() => {
    setHasError(initialHasError);
    setPage(1);
  }, [jobId, initialHasError]);

  const currentBw = (currentTask?.liveBwLimit || "").trim();
  const currentBwInfo = useMemo(() => describeBwLimit(currentBw), [currentBw]);
  const bwSelectValue =
    !currentBw || currentBw.toLowerCase() === "off" ? "off" : currentBw;
  const hasCustomBwOption = !bwOptions.some((o) => o.value === bwSelectValue);
  const effectiveBwOptions = hasCustomBwOption
    ? [{ label: currentBwInfo.liveOptionLabel, value: bwSelectValue }, ...bwOptions]
    : bwOptions;

  const rows = recordsResource.data?.dataList || [];
  const recentErrors = recordsResource.data?.recentErrors || [];
  const latestRecentError = recentErrors[0];
  const selectedSet = useMemo(() => new Set(selectedItemIds), [selectedItemIds]);

  const applyFilter =
    <T,>(setter: (value: T | undefined) => void) =>
    (value: T | undefined) => {
      setter(value);
      setKeyword(input.trim());
      setPage(1);
    };

  const resetFilters = () => {
    setStatus(undefined);
    setType(undefined);
    setObject(undefined);
    setHasError(undefined);
    setInput("");
    setKeyword("");
    setRange([]);
    setPage(1);
  };

  const triggerExport = () => {
    const url = api.exportJobItemsUrl(jobId, {
      status,
      type,
      isPath: object,
      hasError,
      keyword,
      ...historyRangeParams(range),
    });
    const link = document.createElement("a");
    link.href = url;
    link.download = `opensync-job-${jobId}-items.csv`;
    document.body.appendChild(link);
    link.click();
    link.remove();
  };

  const retryItems = (itemIds: number[]) => {
    if (!itemIds.length) return;
    void action.run(async () => {
      try {
        await api.retryJobItems(jobId, itemIds);
        Toast.success(`已提交重试 ${itemIds.length} 项`);
        setSelectedItemIds([]);
        window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
        await Promise.all([refreshCurrentTask(), refreshRecords(true)]);
      } catch (err) {
        errorToast(err);
      }
    });
  };

  const dismissRecentError = (taskId: number) => {
    void action.run(async () => {
      try {
        await api.deleteTask(taskId);
        Toast.success("已清除异常记录");
        await refreshRecords(true);
      } catch (err) {
        errorToast(err);
      }
    });
  };

  const handleClearAll = () => {
    confirmDelete(
      "清空此任务的全部同步记录？",
      () =>
        action.run(async () => {
          const res = await api.clearJobHistory(jobId);
          setSelectedItemIds([]);
          Toast.success(`已清空 ${res.deleted} 条记录`);
          await refreshRecords(true);
        }),
      "正在进行的传输不会受影响，已完成的文件同步记录和异常记录将被彻底删除。",
    );
  };

  const stopLiveTask = () => {
    if (!currentTask) return;
    confirmAction({
      title: "中止本次同步批次？",
      content:
        "仅中止当前正在进行的传输批次，已完成的文件不会撤销；持续同步仍保持开启。",
      okText: "中止本次",
      okLabel: "中止本次同步",
      onOk: () =>
        action.run(async () => {
          try {
            await api.taskAction(currentTask.taskId, "stop");
            Toast.success("已提交中止本次同步");
            window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
            await refreshCurrentTask();
          } catch (err) {
            errorToast(err);
            throw err;
          }
        }),
    });
  };

  const togglePauseLiveTask = () => {
    if (!currentTask) return;
    const resuming = Boolean(currentTask.paused);
    void action.run(async () => {
      try {
        await api.taskAction(currentTask.taskId, resuming ? "resume" : "pause");
        Toast.success(resuming ? "已继续传输" : "已暂停传输");
        window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
        await refreshCurrentTask();
      } catch (err) {
        errorToast(err);
      }
    });
  };

  const updateLiveBwLimit = (rate: string) => {
    if (!currentTask) return;
    void action.run(async () => {
      try {
        await api.taskAction(currentTask.taskId, "bwlimit", undefined, {
          bwLimit: rate,
        });
        Toast.success(
          rate && rate !== "off"
            ? `实时限速已调整为 ${formatBwRateHuman(rate)}`
            : "已解除实时限速",
        );
        await refreshCurrentTask();
      } catch (err) {
        errorToast(err);
      }
    });
  };

  const renderLiveSection = () => {
    if (!currentTask) {
      if (!taskError) return null;
      return (
        <InlineNotice
          icon={<IconAlertTriangle aria-hidden="true" size="small" />}
          message="实时状态连接中断，正在重试…"
        />
      );
    }

    const totalBytes = currentTask.doneSize + currentTask.remainSize;
    const totalItems =
      (currentTask.num?.wait || 0) +
      (currentTask.num?.running || 0) +
      (currentTask.num?.success || 0) +
      (currentTask.num?.fail || 0) +
      (currentTask.num?.other || 0);
    const percent = calcTaskDisplayPercent({
      doneSize: currentTask.doneSize,
      totalSize: totalBytes,
      num: currentTask.num,
      scanFinish: currentTask.scanFinish,
    });
    const bwSelectControl = (
      <Select
        size="small"
        aria-label="运行中动态限速"
        className="execution-bw-select"
        value={bwSelectValue}
        optionList={effectiveBwOptions}
        disabled={action.busy}
        onChange={(v) => updateLiveBwLimit(String(v || "off"))}
      />
    );

    return (
      <section className="sync-records-live-panel" aria-label="当前同步进度">
        <div className="execution-top">
          <div className="execution-header">
            <h2 className="execution-header-title">
              <span>
                {currentTask.paused
                  ? "传输已暂停"
                  : currentTask.scanFinish
                    ? "正在同步"
                    : currentTask.firstSync
                      ? "正在同步（扫描中）"
                      : "正在扫描目录"}
              </span>
              {currentTask.paused && (
                <Tag size="small" color="orange" type="light">
                  已暂停
                </Tag>
              )}
            </h2>
            <div className="execution-header-actions">
              {hasCustomBwOption ? (
                <Tooltip content={currentBwInfo.tooltipSummary}>
                  <span>{bwSelectControl}</span>
                </Tooltip>
              ) : (
                bwSelectControl
              )}
              <Button
                type={currentTask.paused ? "primary" : "tertiary"}
                theme={currentTask.paused ? "solid" : "light"}
                size="small"
                icon={
                  currentTask.paused ? (
                    <IconPlay aria-hidden="true" />
                  ) : (
                    <IconPause aria-hidden="true" />
                  )
                }
                onClick={togglePauseLiveTask}
                disabled={action.busy}
              >
                {currentTask.paused ? "继续传输" : "暂停"}
              </Button>
              <Button
                type="danger"
                size="small"
                icon={<IconStop aria-hidden="true" />}
                onClick={stopLiveTask}
                disabled={action.busy}
              >
                中止本次同步
              </Button>
            </div>
          </div>
          <div className="execution-metrics">
            <span>实时 {formatSize(currentTask.speed)}/s</span>
            {currentTask.speedAvg > 0 && (
              <>
                <Divider layout="vertical" margin={8} />
                <span>均速 {formatSize(currentTask.speedAvg)}/s</span>
              </>
            )}
            <Divider layout="vertical" margin={8} />
            <span>
              已完成 {formatSize(currentTask.doneSize)} /{" "}
              {formatSize(totalBytes)}
              {totalItems > 0
                ? `（${currentTask.num?.success || 0} / ${totalItems} 项）`
                : ""}
            </span>
            <Divider layout="vertical" margin={8} />
            <span>已运行 {formatDuration(currentTask.duration)}</span>
            {currentTask.remainTime > 0 && !currentTask.paused && (
              <>
                <Divider layout="vertical" margin={8} />
                <span>预计剩余 {formatDuration(currentTask.remainTime)}</span>
              </>
            )}
          </div>
          <Progress
            percent={Math.round(percent)}
            showInfo
            stroke={
              currentTask.paused
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
          {!currentTask.scanFinish && currentTask.scan && (
            <div className="scan-status">
              已扫描 {currentTask.scan.scannedDirs} 个目录
              {currentTask.scan.totalFiles
                ? ` · 共 ${currentTask.scan.totalFiles} 个文件${
                    currentTask.scan.checkedFiles
                      ? `（已核对 ${currentTask.scan.checkedFiles}）`
                      : ""
                  }`
                : ` · 待扫描 ${currentTask.scan.remainingDirs} 个目录`}
            </div>
          )}
          <Tabs
            activeKey={String(liveItems.activeTab)}
            onChange={(key) => liveItems.setActiveTab(Number(key))}
            className="execution-tabs"
          >
            {statusTabs.map((t) => (
              <Tabs.TabPane
                key={t.key}
                itemKey={String(t.key)}
                tab={`${t.label} ${currentTask.num?.[t.count] || 0}`}
              />
            ))}
          </Tabs>
        </div>

        <div className="sync-records-live-list">
          {liveItems.tabError && liveItems.tabTaskList.length > 0 && (
            <InlineNotice
              icon={<IconAlertTriangle aria-hidden="true" size="small" />}
              message="明细连接中断，正在重试…"
            />
          )}
          {liveItems.tabError && liveItems.tabTaskList.length === 0 ? (
            <LoadState
              loading={false}
              error={liveItems.tabError}
              retry={liveItems.retryTabTasks}
            />
          ) : (
            <FileTable
              key={liveItems.tableKey}
              rows={liveItems.tabTaskList}
              loading={liveItems.tabLoading}
            />
          )}
          <DataPager
            total={liveItems.tabTaskTotal}
            page={liveItems.tabTaskPage}
            size={liveItems.pageSize}
            onChange={liveItems.setTabTaskPage}
            onSizeChange={liveItems.setPageSize}
          />
        </div>
      </section>
    );
  };

  return (
    <div className="sync-records-view history-view flex-column">
      <div className="sync-records-scroll history-scroll">
        {renderLiveSection()}

        {recentErrors.length > 0 && (
          <section className="recent-errors-panel" aria-label="近期同步异常">
            <div className="recent-errors-head">
              <button
                type="button"
                className="recent-errors-toggle"
                aria-expanded={errorsExpanded}
                onClick={() => setErrorsExpanded((prev) => !prev)}
              >
                <IconChevronRight
                  aria-hidden="true"
                  size="small"
                  className={`recent-errors-chevron${
                    errorsExpanded ? " is-open" : ""
                  }`}
                />
                <IconAlertTriangle
                  aria-hidden="true"
                  size="small"
                  className="recent-errors-icon"
                />
                <strong>近期同步异常（{recentErrors.length}）</strong>
                {!errorsExpanded && latestRecentError && (
                  <span className="recent-errors-preview muted">
                    最近（{formatTimestamp(latestRecentError.createTime, true)}）：
                    {latestRecentError.errMsg ||
                      taskRecordStatusNames[latestRecentError.status] ||
                      "同步失败"}
                  </span>
                )}
              </button>
              <span className="recent-errors-hint muted">
                {errorsExpanded ? "点击收起" : "点击展开详情"}
              </span>
            </div>
            {errorsExpanded && (
              <ul className="recent-errors-list">
                {recentErrors.map((err) => (
                  <li key={err.id} className="recent-error-row">
                    <div className="recent-error-meta">
                      <span className="recent-error-time muted">
                        {formatTimestamp(err.createTime, true)}
                      </span>
                      <Status
                        status={err.status}
                        label={
                          taskRecordStatusNames[err.status] ||
                          String(err.status)
                        }
                      />
                      <span className="recent-error-msg">
                        {err.errMsg || "同步执行异常"}
                      </span>
                    </div>
                    <IconButton
                      label="删除此异常记录"
                      size="small"
                      icon={<IconDeleteStroked aria-hidden="true" />}
                      danger
                      disabled={action.busy}
                      onClick={() => dismissRecentError(err.id)}
                    />
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}

        <div className="filter-bar sync-records-filter-bar">
          <SearchFilterInput
            ariaLabel="搜索同步记录"
            placeholder="文件 / 路径 / 错误"
            value={input}
            onChange={setInput}
            onSearch={(next) => {
              setKeyword(next);
              setPage(1);
            }}
          />
          <FilterSelect
            label="同步状态"
            value={status}
            options={recordStatusOptions}
            onChange={applyFilter(setStatus)}
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
          <span id={rangeLabelId} className="sr-only">
            同步时间范围
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
          <Button theme="borderless" onClick={resetFilters}>
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
              onClick={triggerExport}
            >
              导出 CSV
            </Button>
            {(totalCount > 0 || recentErrors.length > 0) && (
              <Button
                size="small"
                type="tertiary"
                disabled={action.busy}
                onClick={handleClearAll}
              >
                清空记录
              </Button>
            )}
          </div>
        </div>

        {recordsResource.data?.errorGroups &&
          recordsResource.data.errorGroups.length > 0 && (
            <div className="error-groups-bar">
              <span className="error-groups-label muted">失败原因聚类：</span>
              {recordsResource.data.errorGroups.slice(0, 8).map((g) => {
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
                    {g.reason.length > 36
                      ? `${g.reason.slice(0, 36)}…`
                      : g.reason}{" "}
                    ({g.count})
                  </button>
                );
              })}
            </div>
          )}

        {recordsResource.error ? (
          <LoadState
            loading={false}
            error={recordsResource.error}
            retry={() => void refreshRecords()}
          />
        ) : (
          <FileTable
            rows={rows}
            loading={recordsResource.loading}
            showTime
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
        total={totalCount}
        page={page}
        size={size}
        onChange={setPage}
        onSizeChange={(s) => {
          setSize(s);
          setPage(1);
        }}
      />
    </div>
  );
});

export function fileRowKey(record?: TaskItem): string {
  if (record?.id) return String(record.id);
  return [record?.type, record?.srcPath, record?.dstPath, record?.fileName]
    .map((part) => String(part ?? ""))
    .join("\u0000");
}

const retryableItemStatuses = new Set([0, 4, 5, 7, 10]);

export function isRetryableItem(record: TaskItem): boolean {
  return (
    typeof record.id === "number" &&
    record.id > 0 &&
    retryableItemStatuses.has(record.status)
  );
}

export const FileTable = memo(function FileTable({
  rows,
  loading = false,
  showTime = false,
  selectedIds,
  onToggleSelect,
  onToggleSelectAll,
  onRetrySingle,
  busy = false,
}: {
  rows: TaskItem[];
  loading?: boolean;
  showTime?: boolean;
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
    if (showTime) {
      base.push({
        title: "同步时间",
        width: fileTableWidths.time,
        className: "file-col-time",
        render: (_: unknown, record: TaskItem) =>
          record.createTime ? formatTimestamp(record.createTime, true) : "—",
      });
    }
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
    showTime,
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
                showTime={showTime}
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

const MobileFileRecord = memo(function MobileFileRecord({
  record,
  showTime = false,
  onRetrySingle,
  busy,
}: {
  record: TaskItem;
  showTime?: boolean;
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
            {showTime && record.createTime
              ? ` · ${formatTimestamp(record.createTime, true)}`
              : ""}
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

export { normalizeDateRange };
