import { memo, useEffect, useId, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import DatePicker from "@douyinfe/semi-ui/lib/es/datePicker";
import Divider from "@douyinfe/semi-ui/lib/es/divider";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import SideSheet from "@douyinfe/semi-ui/lib/es/sideSheet";
import Table from "@douyinfe/semi-ui/lib/es/table";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconAlertTriangle,
  IconDeleteStroked,
  IconEyeOpenedStroked,
  IconPause,
  IconRefresh,
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
import { taskProgressPercent } from "./Home/taskRows";
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
  name: 360,
  size: 96,
  action: 76,
  status: 160,
} as const;

export const Realtime = memo(function Realtime({
  jobId,
  onTaskEnded,
  onApprovalDetected,
}: {
  jobId: number;
  /** Called once the stream/poll reports "no task running" (tab recycling). */
  onTaskEnded?: () => void;
  /** Called immediately when the live feed observes a full-sync approval prompt. */
  onApprovalDetected?: () => void;
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
    // A hidden tab closes the stream; that must not be mistaken for "the task
    // ended" and kick the user back to the overview tab. A deep link that never
    // saw a task also stays put (the tab is disabled anyway).
    if (document.visibilityState === "hidden") return;
    hadTask.current = false;
    onTaskEnded?.();
  }, [currentTask, taskLoading, taskError, onTaskEnded]);
  const hasApproval = Boolean(currentTask?.approval);
  const prevApproval = useRef(false);
  useEffect(() => {
    if (hasApproval && !prevApproval.current) {
      onApprovalDetected?.();
    }
    prevApproval.current = hasApproval;
  }, [hasApproval, onApprovalDetected]);
  if (!currentTask) {
    // The first poll used to flash "空空如也" before the task arrived, and a
    // failed first poll looked like "nothing running".
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
  const percent =
    total > 0 ? Math.min(100, (task.doneSize / total) * 100) : 0;
  const stop = () =>
    confirmStopTask(() =>
      action.run(async () => {
        await api.taskAction(task.taskId, "stop");
        await refreshCurrentTask();
      }),
    );
  return (
    <div className="execution-view flex-column">
      <div className="execution-top">
        <div className="execution-header">
          <h2>
            {task.scanFinish
              ? "正在同步"
              : task.firstSync
                ? "正在同步（扫描中）"
                : "正在扫描目录"}
          </h2>
          <Button
            type="danger"
            size="small"
            icon={<IconPause aria-hidden="true" />}
            onClick={stop}
            disabled={action.busy}
          >
            停止任务
          </Button>
        </div>
        <div className="execution-metrics">
          <span>{formatSize(task.speed)}/s</span>
          <Divider layout="vertical" margin={8} />
          <span>
            已完成 {formatSize(task.doneSize)} /{" "}
            {formatSize(task.doneSize + task.remainSize)}
          </span>
          <Divider layout="vertical" margin={8} />
          <span>已运行 {formatDuration(task.duration)}</span>
        </div>
        <Progress
          percent={Math.round(percent)}
          showInfo
          stroke="var(--semi-color-primary)"
        />
        {taskError && (
          // Polling keeps the last snapshot on failure; without this notice the
          // frozen numbers read as a stalled transfer.
          <InlineNotice
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message="连接中断，正在重试…"
          />
        )}
        {!task.scanFinish && task.scan && (
          <div className="scan-status">
            已扫描 {task.scan.scannedDirs} 个目录 · 待扫描{" "}
            {task.scan.remainingDirs} 个目录
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
  onOpenRealtime,
}: {
  jobId: number;
  currentTaskId?: number;
  onOpenRealtime: () => void;
}) {
  const [page, setPage] = useState(1);
  const [size, setSize] = useState(10);
  const [status, setStatus] = useState<number | undefined>();
  const [input, setInput] = useState("");
  const [keyword, setKeyword] = useState("");
  const [range, setRange] = useState<Date[]>([]);
  const [detail, setDetail] = useState<number | null>(null);
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
  const action = useAction();
  const rows = resource.data?.dataList || [];
  const totalCount = resource.data?.count || 0;
  const isMobile = useMediaQuery("(max-width: 640px)");
  // Deleting the last record on the last page left the table empty.
  useClampedPage(page, resource.data ? totalCount : null, size, setPage);
  const rangeLabelId = useId();
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
        <IconButton
          label="重试未完成项"
          icon={<IconRefresh aria-hidden="true" />}
          disabled={action.busy}
          onClick={() => retry(record)}
        />
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
          // The backend GetJobTaskList keyword filter only matches job_task.id;
          // promising "error keyword" here made every such search return 0.
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
            // The typed text and the applied filter must not diverge: applying
            // a status/date change also applies the current search text.
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
                    title: "耗时",
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
        <FileDetails taskId={detail} onClose={() => setDetail(null)} />
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

export const FileTable = memo(function FileTable({
  rows,
  loading = false,
}: {
  rows: TaskItem[];
  loading?: boolean;
}) {
  // The columns are static; building them inline on every render re-created the
  // render closures that Semi feeds into every cell.
  // Render only the layout that is visible: CSS hid one of the two, but React
  // still built a table and a card list for every row on every update.
  const isMobile = useMediaQuery("(max-width: 640px)");
  const columns = useMemo(
    () => [
      {
        title: "文件 / 目录",
        width: fileTableWidths.name,
        className: "file-col-name",
        render: (_: unknown, record: TaskItem) => (
          <div className="file-name">
            <Tooltip content={getTaskDisplayName(record)} position="topLeft">
              <strong>{getTaskDisplayName(record)}</strong>
            </Tooltip>
            <Tooltip
              content={record.dstPath || record.srcPath || "—"}
              position="topLeft"
            >
              <span className="mono muted">
                {record.dstPath || record.srcPath || "—"}
              </span>
            </Tooltip>
          </div>
        ),
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
          // 运行中：展示实时进度
          if (record.status === 1) {
            const pct = taskProgressPercent(record.progress);
            return (
              <div className="file-progress">
                <span className="file-progress-pct">{pct}%</span>
                <Progress
                  percent={pct}
                  showInfo={false}
                  stroke="var(--semi-color-primary)"
                />
              </div>
            );
          }
          // 其他状态：展示状态标签 + 错误提示图标
          return (
            <Status
              status={record.status}
              label={taskItemStatusNames[record.status]}
              error={record.errMsg}
            />
          );
        },
      },
    ],
    [],
  );
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
            // Loading only: errors are rendered (with retry) by the caller, so
            // no dead "重试" button is wired to a no-op here.
            <LoadingPanel />
          ) : !rows.length ? (
            <EmptyState />
          ) : (
            rows.map((record) => (
              <MobileFileRecord key={fileRowKey(record)} record={record} />
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
}: {
  record: TaskItem;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className={`mobile-record file-record${open ? " open" : ""}`}>
      <div
        className="record-summary"
        onClick={() => setOpen((v) => !v)}
      >
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
            <p className="mono">{record.srcPath || "—"}</p>
          </div>
          <div>
            <span>目标目录</span>
            <p className="mono">{record.dstPath || "—"}</p>
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
  onClose,
}: {
  taskId: number;
  onClose: () => void;
}) {
  const [page, setPage] = useState(1),
    [size, setSize] = useState(10);
  const [type, setType] = useState<number | undefined>(),
    [object, setObject] = useState<number | undefined>(),
    [hasError, setHasError] = useState<number | undefined>();
  const [input, setInput] = useState(""),
    [keyword, setKeyword] = useState("");
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
  const applyFilter =
    (setter: (value: number | undefined) => void) =>
    (value: number | undefined) => {
      setter(value);
      setKeyword(input.trim());
      setPage(1);
    };
  return (
    <SideSheet
      title="执行明细"
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
        </div>
        {resource.error ? (
          <LoadState
            loading={false}
            error={resource.error}
            retry={() => void resource.refresh()}
          />
        ) : (
          <FileTable
            rows={resource.data?.dataList || []}
            loading={resource.loading}
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
