import { useState } from "react";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import DatePicker from "@douyinfe/semi-ui/lib/es/datePicker";
import Empty from "@douyinfe/semi-ui/lib/es/empty";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Pagination from "@douyinfe/semi-ui/lib/es/pagination";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Table from "@douyinfe/semi-ui/lib/es/table";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconDelete,
  IconEyeOpened,
  IconPause,
  IconRefresh,
  IconSearch,
} from "@douyinfe/semi-icons";
import dayjs from "dayjs";
import { api } from "../api/client";
import {
  IconButton,
  LoadState,
  Status,
  confirmDelete,
  errorToast,
} from "../components/common";
import { useAction, useResource } from "../lib/hooks";
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
  const { currentTask, refreshCurrentTask } = useRealtimeTask(
    String(jobId),
    true,
  );
  const items = useRealtimeTaskItems({
    jobId: String(jobId),
    enabled: true,
    currentTask,
    pageSize: 20,
  });
  const action = useAction();
  if (!currentTask)
    return (
      <div className="execution-empty">
        <Empty
          image={<IconPause aria-hidden="true" size="extra-large" />}
          title="当前无运行中的任务"
        />
        <Button
          icon={<IconRefresh aria-hidden="true" />}
          onClick={() => void refreshCurrentTask()}
        >
          刷新
        </Button>
      </div>
    );
  const total = currentTask.doneSize + currentTask.remainSize;
  const percent =
    total > 0 ? Math.min(100, (currentTask.doneSize / total) * 100) : 0;
  const stop = () =>
    Modal.confirm({
      title: "停止当前任务？",
      content: "已完成的文件不会撤销。",
      okText: "停止任务",
      cancelText: "取消",
      onOk: () =>
        action.run(async () => {
          try {
            await api.taskAction(currentTask.taskId, "stop");
            Toast.success("已提交停止");
            await refreshCurrentTask();
          } catch (error) {
            errorToast(error);
            throw error;
          }
        }),
    });
  return (
    <div className="execution-view">
      <div className="execution-header">
        <div>
          <h2>{currentTask.scanFinish ? "正在同步" : "正在扫描目录"}</h2>
          <span className="muted">
            开始于 {time(currentTask.createTime)} · 已运行{" "}
            {formatDuration(currentTask.duration)}
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
      <Progress percent={Math.round(percent)} />
      <div className="execution-metrics">
        <div>
          <span>传输速度</span>
          <strong>
            {formatSize(currentTask.speed)}
            <small>/s</small>
          </strong>
        </div>
        <div>
          <span>已完成</span>
          <strong>{formatSize(currentTask.doneSize)}</strong>
        </div>
        <div>
          <span>剩余大小</span>
          <strong>{formatSize(currentTask.remainSize)}</strong>
        </div>
        <div>
          <span>预计剩余</span>
          <strong>
            {currentTask.remainTime > 0
              ? formatDuration(currentTask.remainTime)
              : "—"}
          </strong>
        </div>
      </div>
      {!currentTask.scanFinish && currentTask.scan && (
        <div className="scan-status">
          已扫描 {currentTask.scan.scannedDirs} 个目录 · 待扫描{" "}
          {currentTask.scan.remainingDirs} 个目录
        </div>
      )}
      <Tabs
        activeKey={String(items.activeTab)}
        onChange={(key) => items.setActiveTab(Number(key))}
        className="execution-tabs"
      >
        {statusTabs.map((tab) => (
          <Tabs.TabPane
            key={tab.key}
            itemKey={String(tab.key)}
            tab={`${tab.label} ${tab.key === items.activeTab ? items.tabTaskTotal : currentTask.num?.[tab.count] || 0}`}
          />
        ))}
      </Tabs>
      <FileTable
        rows={
          items.activeTab === 1 ? items.pagedTabTaskList : items.tabTaskList
        }
        loading={items.tabLoading}
      />
      <Pager
        total={items.tabTaskTotal}
        page={items.tabTaskPage}
        size={20}
        onChange={items.setTabTaskPage}
      />
    </div>
  );
}

export function History({ jobId }: { jobId: number }) {
  const [page, setPage] = useState(1),
    [size, setSize] = useState(20);
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
          startTime: range[0]
            ? Math.floor(range[0].getTime() / 1000)
            : undefined,
          endTime: range[1] ? Math.floor(range[1].getTime() / 1000) : undefined,
        },
        signal,
      ),
    [jobId, page, size, status, keyword, range],
  );
  const action = useAction();
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
  const rows = resource.data?.dataList || [];
  const controls = (record: TaskRecord) => (
    <div className="row-actions">
      <IconButton
        aria-hidden="true"
        label="查看执行明细"
        icon={<IconEyeOpened aria-hidden="true" />}
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
        icon={<IconDelete aria-hidden="true" />}
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
    <div className="history-view">
      <div className="filter-bar">
        <Input
          prefix={<IconSearch aria-hidden="true" />}
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
              icon={<IconSearch aria-hidden="true" />}
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
          type="dateTimeRange"
          value={range}
          onChange={(value) => {
            setRange(Array.isArray(value) ? value.map((v) => new Date(v)) : []);
            setPage(1);
          }}
          showClear
        />
        <IconButton
          aria-hidden="true"
          label="刷新历史"
          icon={<IconRefresh aria-hidden="true" />}
          onClick={() => void resource.refresh()}
        />
      </div>
      {resource.error && (
        <Banner type="danger" description={resource.error} closeIcon={null} />
      )}
      <div className="desktop-data">
        <Table<TaskRecord>
          dataSource={rows}
          loading={resource.loading}
          pagination={false}
          rowKey="id"
          empty={<Empty title="暂无历史任务" />}
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
          <Empty title="暂无历史任务" />
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
      <Pager
        total={resource.data?.count || 0}
        page={page}
        size={size}
        onChange={setPage}
        onSize={(value) => {
          setSize(value);
          setPage(1);
        }}
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
          dataSource={rows}
          loading={loading}
          rowKey={(record) =>
            String(record?.id || `${record?.srcPath}-${record?.dstPath}`)
          }
          pagination={false}
          empty={<Empty title="暂无文件记录" />}
          columns={[
            {
              title: "文件 / 目录",
              render: (_, record) => (
                <div className="file-name">
                  <strong title={getTaskDisplayName(record)}>
                    {getTaskDisplayName(record)}
                  </strong>
                  <span
                    className="mono muted"
                    title={record.dstPath || record.srcPath || ""}
                  >
                    {record.dstPath || record.srcPath || "—"}
                  </span>
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
              width: 70,
              render: (_, record) => taskTypeNames[record.type || 0] || "—",
            },
            {
              title: "状态",
              width: 160,
              render: (_, record) => (
                <Status
                  status={record.status}
                  label={taskItemStatusNames[record.status]}
                  error={record.errMsg}
                />
              ),
            },
            {
              title: "进度",
              width: 130,
              render: (_, record) => (
                <Progress
                  percent={Math.max(
                    0,
                    Math.min(100, Number(record.progress) || 0),
                  )}
                />
              ),
            },
          ]}
        />
      </div>
      <div className="mobile-data">
        {loading ? (
          <LoadState loading retry={() => {}} />
        ) : !rows.length ? (
          <Empty title="暂无文件记录" />
        ) : (
          rows.map((record, index) => (
            <details
              className="mobile-record file-record"
              key={record.id || index}
            >
              <summary>
                <div className="record-title">
                  <strong>{getTaskDisplayName(record)}</strong>
                  <Status
                    status={record.status}
                    label={taskItemStatusNames[record.status]}
                  />
                </div>
                <div className="record-footer">
                  <span className="muted">
                    {taskTypeNames[record.type || 0]} ·{" "}
                    {record.isPath ? "目录" : formatSize(record.fileSize || 0)}
                  </span>
                  <span>{Number(record.progress) || 0}%</span>
                </div>
                <Progress
                  percent={Math.max(
                    0,
                    Math.min(100, Number(record.progress) || 0),
                  )}
                  showInfo={false}
                />
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
                  <Banner
                    type="danger"
                    description={record.errMsg}
                    closeIcon={null}
                  />
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
  return (
    <Modal
      title="执行明细"
      visible
      onCancel={onClose}
      width={1050}
      className="editor-modal detail-modal"
      footer={null}
      centered
    >
      <div className="filter-bar">
        <Input
          aria-label="搜索文件明细"
          prefix={<IconSearch aria-hidden="true" />}
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
      {resource.error && (
        <Banner type="danger" description={resource.error} closeIcon={null} />
      )}
      <FileTable
        rows={resource.data?.dataList || []}
        loading={resource.loading}
      />
      <Pager
        total={resource.data?.count || 0}
        page={page}
        size={size}
        onChange={setPage}
        onSize={(value) => {
          setSize(value);
          setPage(1);
        }}
      />
    </Modal>
  );
}

function Pager({
  total,
  page,
  size,
  onChange,
  onSize,
}: {
  total: number;
  page: number;
  size: number;
  onChange: (page: number) => void;
  onSize?: (size: number) => void;
}) {
  return (
    <div className="table-pagination">
      <span className="muted">共 {total} 条</span>
      <Pagination
        total={total}
        currentPage={page}
        pageSize={size}
        size="small"
        showSizeChanger={!!onSize}
        pageSizeOpts={[10, 20, 50, 100]}
        onPageChange={onChange}
        onPageSizeChange={onSize}
      />
    </div>
  );
}
