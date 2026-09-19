import { useEffect, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import DatePicker from "@douyinfe/semi-ui/lib/es/datePicker";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
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
  errorToast,
} from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import { historyRangeParams } from "../lib/historyFilters";
import { useRealtimeTask } from "./Home/useRealtimeTask";
import { useRealtimeTaskItems } from "./Home/useRealtimeTaskItems";
import {
  formatDuration,
  formatSize,
  getTaskDisplayName,
  taskItemStatusNames,
  taskItemStatusOptions,
  taskRecordStatusNames,
  taskTypeNames,
} from "./Home/homeUtils";
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
  const demoTask = demo && !currentTask ? {
    taskId: 99999,
    scanFinish: true,
    createTime: Math.floor(Date.now() / 1000) - 320,
    duration: 320,
    num: { wait: 1, running: 2, success: 1, fail: 1, other: 1 },
    size: { wait: 0, running: 0, success: 0, fail: 0, other: 0 },
    doneSize: 1536 * 1024 * 1024,
    remainSize: 640 * 1024 * 1024,
    speed: 12.5 * 1024 * 1024,
    speedAvg: 8.7 * 1024 * 1024,
    remainTime: 52,
    doingTask: [
      { id: 1, fileName: "2024-summer-vacation-photos-collection-final-v2.zip", srcPath: "/photos/2024/summer", dstPath: "/backup/photos/2024/summer", fileSize: 256 * 1024 * 1024, type: 1, status: 1, progress: 67 },
      { id: 2, fileName: "project-report-2024-Q3-department-review.pdf", srcPath: "/documents/reports", dstPath: "/backup/documents/reports", fileSize: 48 * 1024 * 1024, type: 1, status: 1, progress: 23 },
    ],
  } as unknown as NonNullable<typeof currentTask> : null;

  const demoItems: TaskItem[] = demo && !currentTask ? [
    { id: 1, fileName: "2024-summer-vacation-photos-collection-final-v2.zip", srcPath: "/photos/2024/summer", dstPath: "/backup/photos/2024/summer", fileSize: 256 * 1024 * 1024, type: 1, status: 1, progress: 67 },
    { id: 2, fileName: "project-report-2024-Q3-department-review.pdf", srcPath: "/documents/reports", dstPath: "/backup/documents/reports", fileSize: 48 * 1024 * 1024, type: 1, status: 1, progress: 23 },
    { id: 3, fileName: "家庭相册-全家福-20240815-高清原图.jpg", srcPath: "/photos/family", dstPath: "/backup/photos/family", fileSize: 18 * 1024 * 1024, type: 1, status: 0, progress: 0 },
    { id: 4, fileName: "system-backup-incremental-20240901.tar.gz", srcPath: "/system/backups", dstPath: "/backup/system", fileSize: 512 * 1024 * 1024, type: 1, status: 7, progress: 0, errMsg: "连接超时：目标引擎无响应，请检查引擎地址和网络连接" },
    { id: 5, fileName: "meeting-recording-2024-09-15-weekly-standup.mp4", srcPath: "/videos/meetings", dstPath: "/backup/videos/meetings", fileSize: 890 * 1024 * 1024, type: 1, status: 2, progress: 100 },
    { id: 6, fileName: "database-dump-20240910-prod.sql.gz", srcPath: "/database/backups", dstPath: "/backup/database", fileSize: 64 * 1024 * 1024, type: 1, status: 5, progress: 35, errMsg: "写入失败：目标磁盘空间不足" },
  ] : [];

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
  const tabItems =
    filteredDemo.length > 0 || demoItems.length > 0
      ? filteredDemo
      : activeTab === 1 ? items.pagedTabTaskList : items.tabTaskList;
  const tabTotal =
    demoItems.length > 0 ? filteredDemo.length : items.tabTaskTotal;
  const total = task.doneSize + task.remainSize;
  const percent =
    total > 0 ? Math.min(100, (task.doneSize / total) * 100) : 0;
  const stop = () =>
    Modal.confirm({
      width: 454,
      className: "fnos-confirm",
      title: "停止当前任务？",
      content: "已完成的文件不会撤销。",
      okText: "停止任务",
      cancelText: "取消",
      okButtonProps: { type: "danger", "aria-label": "停止任务" },
      cancelButtonProps: { "aria-label": "取消" },
      onOk: () =>
        action.run(async () => {
          try {
            await api.taskAction(task.taskId, "stop");
            Toast.success("已提交停止");
            await refreshCurrentTask();
          } catch (error) {
            errorToast(error);
            throw error;
          }
        }),
    });
  return (
    <div className="execution-view flex-column">
      <div className="execution-top">
      <div className="execution-header">
        <div>
          <h2>{task.scanFinish ? "正在同步" : "正在扫描目录"}</h2>
          <span className="muted">
            开始于 {time(task.createTime)} · 已运行{" "}
            {formatDuration(task.duration)}
          </span>
        </div>
        <Button
          type="danger"
          icon={<IconPause aria-hidden="true" />}
          onClick={stop}
          disabled={action.busy}
        >
          停止任务
        </Button>
      </div>
      <Progress percent={Math.round(percent)} showInfo strokeColor="var(--accent)" />
      <div className="execution-metrics">
        <div>
          <span>传输速度</span>
          <strong>
            {formatSize(task.speed)}
            <small>/s</small>
          </strong>
        </div>
        <div>
          <span>已完成</span>
          <strong>{formatSize(task.doneSize)}</strong>
        </div>
        <div>
          <span>剩余大小</span>
          <strong>{formatSize(task.remainSize)}</strong>
        </div>
        <div>
          <span>预计剩余</span>
          <strong>
            {task.remainTime > 0
              ? formatDuration(task.remainTime)
              : "—"}
          </strong>
        </div>
      </div>
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
            tab={`${tab.label} ${tab.key === activeTab ? tabTotal : task.num?.[tab.count] || 0}`}
          />
        ))}
      </Tabs>
      </div>
      <div className="execution-scroll">
      <FileTable
        rows={tabItems}
        loading={items.tabLoading}
        hideStatus
      />
      </div>
      <Pager
        total={tabTotal}
        page={items.tabTaskPage}
        size={10}
        onChange={items.setTabTaskPage}
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
  const now = Math.floor(Date.now() / 1000);
  const demoRecords: TaskRecord[] = demo ? [
    { id: 9001, status: 2, createTime: now - 86400, runTime: now - 86400 + 245, successNum: 128, failNum: 0, allNum: 128 },
    { id: 9002, status: 7, errMsg: "连接超时：引擎无响应", createTime: now - 172800, runTime: now - 172800 + 63, successNum: 45, failNum: 12, allNum: 57 },
    { id: 9003, status: 2, createTime: now - 259200, runTime: now - 259200 + 1820, successNum: 1024, failNum: 0, allNum: 1024 },
    { id: 9004, status: 7, errMsg: "目标路径不存在", createTime: now - 345600, runTime: now - 345600 + 5, successNum: 0, failNum: 3, allNum: 3 },
    { id: 9005, status: 2, createTime: now - 432000, runTime: now - 432000 + 480, successNum: 256, failNum: 0, allNum: 256 },
    { id: 9006, status: 8, createTime: now - 518400, runTime: now - 518400 + 120, successNum: 30, failNum: 0, allNum: 88 },
    { id: 9007, status: 2, createTime: now - 604800, runTime: now - 604800 + 3600, successNum: 2048, failNum: 0, allNum: 2048 },
    { id: 9008, status: 2, createTime: now - 691200, runTime: now - 691200 + 150, successNum: 64, failNum: 0, allNum: 64 },
    { id: 9009, status: 7, errMsg: "磁盘空间不足", createTime: now - 777600, runTime: now - 777600 + 12, successNum: 8, failNum: 5, allNum: 13 },
    { id: 9010, status: 2, createTime: now - 864000, runTime: now - 864000 + 920, successNum: 512, failNum: 0, allNum: 512 },
    { id: 9011, status: 2, createTime: now - 950400, runTime: now - 950400 + 60, successNum: 32, failNum: 0, allNum: 32 },
    { id: 9012, status: 7, errMsg: "认证失败：token 已过期", createTime: now - 1036800, runTime: now - 1036800 + 3, successNum: 0, failNum: 1, allNum: 1 },
  ] : [];

  const realRows = resource.data?.dataList || [];
  const showDemo = demo && !resource.loading && !resource.data;
  const rows = showDemo ? demoRecords : realRows;
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
  hideStatus = false,
}: {
  rows: TaskItem[];
  loading?: boolean;
  hideStatus?: boolean;
}) {
  return (
    <>
      <div className="desktop-data">
        <Table<TaskItem>
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
              width: 110,
              render: (_, record) =>
                record.isPath ? "目录" : formatSize(record.fileSize || 0),
            },
            {
              title: "操作",
              width: 80,
              render: (_, record) => {
                const name = taskTypeNames[record.type || 0] || "—";
                const cls = record.type === 1 ? "task-tag task-tag--danger" : "task-tag";
                return <span className={cls}>{name}</span>;
              },
            },
            ...(!hideStatus
              ? [
                  {
                    title: "状态",
                    width: 160,
                    render: (_: unknown, record: TaskItem) => (
                      <Status
                        status={record.status}
                        label={taskItemStatusNames[record.status]}
                        error={record.errMsg}
                      />
                    ),
                  },
                ]
              : []),
            {
              title: "进度",
              width: 150,
              render: (_, record) => {
                const status = record.status;
                // 失败状态：展示错误原因
                if ([5, 6, 7].includes(status) && record.errMsg) {
                  return (
                    <Tooltip content={record.errMsg} position="topLeft">
                      <span className="inline-error-text">{record.errMsg}</span>
                    </Tooltip>
                  );
                }
                // 仅运行中展示进度条
                if (status !== 1) return null;
                const pct = Math.max(0, Math.min(100, Number(record.progress) || 0));
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
            <details
              className="mobile-record file-record"
              key={record.id || index}
            >
              <summary>
                <div className="record-title">
                  <Tooltip content={getTaskDisplayName(record)} position="topLeft">
                    <strong>{getTaskDisplayName(record)}</strong>
                  </Tooltip>
                  {!hideStatus && (
                    <Status
                      status={record.status}
                      label={taskItemStatusNames[record.status]}
                    />
                  )}
                </div>
                <div className="record-footer">
                  <span className="muted">
                    {taskTypeNames[record.type || 0]} ·{" "}
                    {record.isPath ? "目录" : formatSize(record.fileSize || 0)}
                  </span>
                  {record.status === 1 && (
                    <span>{Number(record.progress) || 0}%</span>
                  )}
                </div>
                {record.status === 1 && (
                  <Progress
                    percent={Math.max(
                      0,
                      Math.min(100, Number(record.progress) || 0),
                    )}
                    showInfo={false}
                  />
                )}
                {[5, 6, 7].includes(record.status) && record.errMsg && (
                  <div className="inline-error-text muted">{record.errMsg}</div>
                )}
              </summary>
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
            </details>
          ))
        )}
      </div>
    </>
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
  const [status, setStatus] = useState<number | undefined>(),
    [type, setType] = useState<number | undefined>(),
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
          status,
          type,
          isPath: object,
          hasError,
          keyword,
        },
        signal,
      ),
    [taskId, page, size, status, type, object, hasError, keyword],
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
        <Select
          aria-label="文件状态"
          placeholder="全部状态"
          value={status === undefined ? "all" : String(status)}
          optionList={[
            { label: "全部状态", value: "all" },
            ...taskItemStatusOptions.map((o) => ({
              ...o,
              value: String(o.value),
            })),
          ]}
          onChange={(value) => {
            setStatus(value === "all" ? undefined : Number(value));
            setPage(1);
          }}
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
            setStatus(undefined);
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
      />
    </SideSheet>
  );
}

function Pager({
  total,
  page,
  size,
  onChange,
}: {
  total: number;
  page: number;
  size: number;
  onChange: (page: number) => void;
}) {
  return (
    <div className="table-pagination">
      <span className="muted">共 {total} 条</span>
      <Pagination
        total={total}
        currentPage={page}
        pageSize={size}
        size="small"
        hideOnSinglePage={false}
        onPageChange={onChange}
      />
    </div>
  );
}
