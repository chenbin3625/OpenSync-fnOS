import { useEffect, useMemo, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import DatePicker from "@douyinfe/semi-ui/lib/es/datePicker";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Pagination from "@douyinfe/semi-ui/lib/es/pagination";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import SideSheet from "@douyinfe/semi-ui/lib/es/sideSheet";
import Table from "@douyinfe/semi-ui/lib/es/table";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconDeleteStroked,
  IconEyeOpenedStroked,
  IconPause,
  IconRefresh,
  IconSearchStroked,
} from "@douyinfe/semi-icons";
import dayjs from "dayjs";
import { api } from "../api/client";
import {
  IconButton,
  EmptyState,
  LoadState,
  Status,
  confirmDelete,
  confirmStopTask,
  errorToast,
} from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import { historyRangeParams } from "../lib/historyFilters";
import {
  createDemoTaskItems,
  createDemoTaskRecords,
  createDemoTaskView,
  pageDemoRows,
} from "./Home/demoTaskData";
import { useRealtimeTask } from "./Home/useRealtimeTask";
import { useRealtimeTaskItems } from "./Home/useRealtimeTaskItems";
import {
  formatDuration,
  formatSize,
  getTaskDisplayName,
  taskItemStatusNames,
  taskRecordStatusNames,
  taskTypeNames,
} from "./Home/homeUtils";
import { taskProgressPercent } from "./Home/taskRows";
import type { TaskItem, TaskRecord } from "../types";

const time = (value?: number) =>
  value ? dayjs.unix(value).format("YYYY-MM-DD HH:mm:ss") : "—";

const statusTabs = [
  { key: 0, label: "等待", count: "wait" },
  { key: 1, label: "运行中", count: "running" },
  { key: 2, label: "成功", count: "success" },
  { key: 7, label: "失败", count: "fail" },
  { key: -1, label: "其他", count: "other" },
] as const;

const fileTableWidths = {
  name: 360,
  size: 96,
  action: 76,
  status: 160,
} as const;

export function Realtime({ jobId }: { jobId: number }) {
  const demo = import.meta.env.DEV;
  const { currentTask, refreshCurrentTask } = useRealtimeTask(
    String(jobId),
    true,
  );
  const items = useRealtimeTaskItems({
    jobId: String(jobId),
    enabled: true,
    currentTask,
    pageSize: 10,
  });
  const action = useAction();

  // 演示数据
  const demoItems = useMemo(
    () => (demo && !currentTask ? createDemoTaskItems() : []),
    [currentTask, demo],
  );
  const demoTask =
    demo && !currentTask ? createDemoTaskView(undefined, demoItems) : null;

  const activeTask = currentTask || demoTask;
  if (!activeTask) {
    return <EmptyState />;
  }
  const task = activeTask;
  const activeTab = items.activeTab;
  const filteredDemo = demoItems.length > 0
    ? demoItems.filter((item) =>
        activeTab === -1
          ? ![0, 1, 2, 7].includes(item.status)
          : item.status === activeTab,
      )
    : [];
  const isDemo = filteredDemo.length > 0 || demoItems.length > 0;
  const pagedDemo = isDemo
    ? pageDemoRows(filteredDemo, items.tabTaskPage, items.pageSize)
    : [];
  const tabItems = isDemo ? pagedDemo : items.tabTaskList;
  const tabTotal = isDemo ? filteredDemo.length : items.tabTaskTotal;
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
        <h2>{task.scanFinish ? "正在同步" : task.firstSync ? "正在同步（扫描中）" : "正在扫描目录"}</h2>
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
        <span className="metric-sep" />
        <span>已完成 {formatSize(task.doneSize)} / {formatSize(task.doneSize + task.remainSize)}</span>
        <span className="metric-sep" />
        <span>已运行 {formatDuration(task.duration)}</span>
      </div>
      <Progress percent={Math.round(percent)} showInfo strokeColor="var(--accent)" />
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
      {items.tabError && tabItems.length === 0 ? (
        <LoadState
          loading={false}
          error={items.tabError}
          retry={items.retryTabTasks}
        />
      ) : (
        <FileTable
          rows={tabItems}
          loading={items.tabLoading}
        />
      )}
      </div>
      <Pager
        total={tabTotal}
        page={items.tabTaskPage}
        size={items.pageSize}
        onChange={items.setTabTaskPage}
        onSizeChange={items.setPageSize}
      />
    </div>
  );
}

export function History({ jobId }: { jobId: number }) {
  const demo = import.meta.env.DEV;
  const [page, setPage] = useState(1),
    [size, setSize] = useState(10);
  const [status, setStatus] = useState<number | undefined>(),
    [input, setInput] = useState(""),
    [keyword, setKeyword] = useState("");
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
          keyword,
          ...historyRangeParams(range),
        },
        signal,
      ),
    [jobId, page, size, status, keyword, range],
  );
  const action = useAction();

  // 演示数据
  const demoRecords = useMemo(
    () => (demo ? createDemoTaskRecords() : []),
    [demo],
  );

  const realRows = resource.data?.dataList || [];
  const showDemo = demo && !resource.loading && !resource.data;
  const rows = showDemo ? pageDemoRows(demoRecords, page, size) : realRows;
  const totalCount = showDemo ? demoRecords.length : (resource.data?.count || 0);
  const retry = (record: TaskRecord) =>
    void action.run(async () => {
      try {
        await api.taskAction(record.id, "retry");
        Toast.success("已提交重试");
        await resource.refresh();
      } catch (error) {
        errorToast(error);
      }
    });
  const controls = (record: TaskRecord) => (
    <div className="row-actions">
      <IconButton
        aria-hidden="true"
        label="查看执行明细"
        icon={<IconEyeOpenedStroked aria-hidden="true" />}
        onClick={() => setDetail(record.id)}
      />
      <IconButton
        aria-hidden="true"
        label="重试未完成项"
        icon={<IconRefresh aria-hidden="true" />}
        disabled={action.busy}
        onClick={() => retry(record)}
      />
      <IconButton
        aria-hidden="true"
        label="删除执行记录"
        icon={<IconDeleteStroked aria-hidden="true" />}
        danger
        onClick={() =>
          confirmDelete("删除此执行记录？", async () => {
            await api.deleteTask(record.id);
            await resource.refresh();
          })
        }
      />
    </div>
  );
  return (
    <div className="history-view flex-column">
      <div className="filter-bar">
        <Input
          prefix={<IconSearchStroked aria-hidden="true" />}
          aria-label="搜索执行记录"
          placeholder="任务 / 错误关键词"
          value={input}
          showClear
          onChange={(value) => {
            setInput(value);
            if (!value) {
              setKeyword("");
              setPage(1);
            }
          }}
          onEnterPress={() => {
            setKeyword(input.trim());
            setPage(1);
          }}
          suffix={
            <IconButton
              aria-hidden="true"
              label="搜索记录"
              icon={<IconSearchStroked aria-hidden="true" />}
              onClick={() => {
                setKeyword(input.trim());
                setPage(1);
              }}
            />
          }
        />
        <Select
          aria-label="执行状态"
          value={status === undefined ? "all" : String(status)}
          optionList={[
            { label: "全部状态", value: "all" },
            ...[2, 3, 4, 5, 6, 7, 8].map((value) => ({
              value: String(value),
              label: taskRecordStatusNames[value],
            })),
          ]}
          onChange={(value) => {
            setStatus(value === "all" ? undefined : Number(value));
            setPage(1);
          }}
        />
        <DatePicker
          type="dateRange"
          value={range}
          onChange={(value) => {
            setRange(Array.isArray(value) ? value.map((v) => new Date(v)) : []);
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
      {!resource.error && (
      <>
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
              render: (value) => time(value),
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
                record.runTime && record.createTime
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
      <div className="mobile-data">
        {resource.loading ? (
          <LoadState loading retry={resource.refresh} />
        ) : !rows.length ? (
          <EmptyState />
        ) : (
          rows.map((record) => (
            <div className="mobile-record" key={record.id}>
              <div className="record-title">
                <strong>{time(record.createTime)}</strong>
                <Status
                  status={record.status}
                  label={taskRecordStatusNames[record.status]}
                  error={record.errMsg}
                />
              </div>
              <div className="record-footer">
                <span className="muted">
                  {record.runTime && record.createTime
                    ? formatDuration(record.runTime - record.createTime)
                    : "—"}
                </span>
                {controls(record)}
              </div>
            </div>
          ))
        )}
      </div>
      </>
      )}
      </div>
      <Pager
        total={totalCount}
        page={page}
        size={size}
        onChange={setPage}
        onSizeChange={(s) => { setSize(s); setPage(1); }}
      />
      {detail !== null && (
        <FileDetails taskId={detail} onClose={() => setDetail(null)} />
      )}
    </div>
  );
}

export function FileTable({
  rows,
  loading = false,
}: {
  rows: TaskItem[];
  loading?: boolean;
}) {
  return (
    <>
      <div className="desktop-data">
        <Table<TaskItem>
          className="file-table"
          dataSource={rows}
          loading={loading}
          rowKey={(record) =>
            String(record?.id || `${record?.srcPath}-${record?.dstPath}`)
          }
          pagination={false}
          empty={<EmptyState />}
          columns={[
            {
              title: "文件 / 目录",
              width: fileTableWidths.name,
              className: "file-col-name",
              render: (_, record) => (
                <div className="file-name">
                  <Tooltip content={getTaskDisplayName(record)} position="topLeft">
                    <strong>
                      {getTaskDisplayName(record)}
                    </strong>
                  </Tooltip>
                  <Tooltip content={record.dstPath || record.srcPath || "—"} position="topLeft">
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
              render: (_, record) =>
                record.isPath ? "目录" : formatSize(record.fileSize || 0),
            },
            {
              title: "操作",
              width: fileTableWidths.action,
              className: "file-col-action",
              render: (_, record) => {
                const name = taskTypeNames[record.type || 0] || "—";
                const cls = record.type === 1 ? "task-tag task-tag--danger" : "task-tag";
                return <span className={cls}>{name}</span>;
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
                        strokeColor="var(--accent)"
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
          ]}
        />
      </div>
      <div className="mobile-data">
        {loading ? (
          <LoadState loading retry={() => {}} />
        ) : !rows.length ? (
          <EmptyState />
        ) : (
          rows.map((record, index) => (
            <MobileFileRecord key={record.id || index} record={record} />
          ))
        )}
      </div>
    </>
  );
}

function MobileFileRecord({ record }: { record: TaskItem }) {
  const [open, setOpen] = useState(false);
  return (
    <div className={`mobile-record file-record${open ? " open" : ""}`}>
      <div
        className="record-summary"
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            setOpen((v) => !v);
          }
        }}
      >
        <div className="record-title">
          <Tooltip content={getTaskDisplayName(record)} position="topLeft">
            <strong>{getTaskDisplayName(record)}</strong>
          </Tooltip>
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
}

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
    true,
  );
  useEffect(() => {
    if (resource.error) Toast.error(resource.error);
  }, [resource.error]);
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
        <Input
          aria-label="搜索文件明细"
          prefix={<IconSearchStroked aria-hidden="true" />}
          placeholder="文件 / 路径 / 错误"
          value={input}
          onChange={(value) => {
            setInput(value);
            if (!value) {
              setKeyword("");
              setPage(1);
            }
          }}
          onEnterPress={() => {
            setKeyword(input.trim());
            setPage(1);
          }}
          showClear
        />
        {[
          {
            label: "操作类型",
            value: type,
            set: setType,
            options: [
              { label: "复制/创建", value: 0 },
              { label: "删除", value: 1 },
              { label: "移动", value: 2 },
            ],
          },
          {
            label: "对象类型",
            value: object,
            set: setObject,
            options: [
              { label: "文件", value: 0 },
              { label: "目录", value: 1 },
            ],
          },
          {
            label: "错误信息",
            value: hasError,
            set: setHasError,
            options: [
              { label: "有错误信息", value: 1 },
              { label: "无错误信息", value: 0 },
            ],
          },
        ].map((filter) => (
          <Select
            key={filter.label}
            aria-label={filter.label}
            value={filter.value === undefined ? "all" : String(filter.value)}
            optionList={[
              { label: "全部" + filter.label, value: "all" },
              ...filter.options.map((o) => ({ ...o, value: String(o.value) })),
            ]}
            onChange={(value) => {
              filter.set(value === "all" ? undefined : Number(value));
              setPage(1);
            }}
          />
        ))}
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
      <FileTable
        rows={resource.data?.dataList || []}
        loading={resource.loading}
      />
      </div>
      <Pager
        total={resource.data?.count || 0}
        page={page}
        size={size}
        onChange={setPage}
        onSizeChange={(s) => { setSize(s); setPage(1); }}
      />
    </SideSheet>
  );
}

function Pager({
  total,
  page,
  size,
  onChange,
  onSizeChange,
}: {
  total: number;
  page: number;
  size: number;
  onChange: (page: number) => void;
  onSizeChange?: (size: number) => void;
}) {
  return (
    <div className="table-pagination">
      <span className="muted">共 {total} 条</span>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <Pagination
          total={total}
          currentPage={page}
          pageSize={size}
          size="small"
          hideOnSinglePage={false}
          onPageChange={onChange}
        />
        {onSizeChange && (
          <Select
            value={size}
            onChange={(v) => onSizeChange(v as number)}
            size="small"
            style={{ width: 108 }}
            optionList={[10, 20, 50, 100].map((n) => ({
              value: n,
              label: `${n} 条/页`,
            }))}
          />
        )}
      </div>
    </div>
  );
}
