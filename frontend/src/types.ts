export interface ApiResponse<T> {
  code: number;
  data: T;
  msg: string;
}

export interface PageData<T> {
  dataList: T[];
  count: number;
  // Set by the backend when a request without pageNum/pageSize was capped at
  // its unpaged row limit, so dataList holds fewer rows than count. Every call
  // in src/api paginates, which is why this should never arrive; it is typed
  // and checked so a future unpaginated caller fails loudly instead of
  // silently rendering a short list as if it were complete.
  truncated?: boolean;
}

export interface RealtimeTaskItemPage extends PageData<TaskItem> {
  taskId: number;
  createTime: number;
  status: number;
  pageNum: number;
  pageSize: number;
  stale: boolean;
}

export interface SystemSettings {
  taskTimeout: number;
  taskSave: number;
  copyConcurrency: number;
  scanConcurrency: number;
  maxRetries: number;
}

export interface AlistItem {
  id: number;
  remark?: string | null;
  url: string;
  userName: string;
  createTime?: number;
}

export interface JobItem {
  id: number;
  enable: number;
  remark?: string | null;
  srcPath: string;
  dstPath: string;
  alistId: number;
  useCacheT: number | boolean;
  scanIntervalT?: number;
  useCacheS: number | boolean;
  scanIntervalS?: number;
  method: number;
  interval: number;
  isCron: number;
  month?: string | null;
  day?: string | null;
  day_of_week?: string | null;
  hour?: string | null;
  minute?: string | null;
  second?: string | null;
  exclude?: string | null;
  minFileSize?: number;
  maxFileSize?: number;
  createTime?: number;
}

export interface TreeNode {
  title: string;
  value: string;
  key: string;
  isLeaf?: boolean;
  children?: TreeNode[];
}

export interface TaskRecord {
  id: number;
  status: number;
  errMsg?: string | null;
  runTime?: number;
  successNum?: number;
  failNum?: number;
  allNum?: number;
  createTime?: number;
}

export interface TaskItem {
  id?: number | string;
  taskId?: number;
  srcPath?: string | null;
  dstPath?: string | null;
  isPath?: number;
  fileName?: string | null;
  fileSize?: number | null;
  type?: number;
  alistTaskId?: string | null;
  status: number;
  progress?: number | string;
  errMsg?: string | null;
  createTime?: number;
}

export type TaskNumKey = "wait" | "running" | "success" | "fail" | "other";

export interface ScanProgress {
  scannedDirs: number;
  remainingDirs: number;
  totalDirs: number;
}

export interface CurrentTaskData {
  taskId: number;
  scanFinish: boolean;
  scan?: ScanProgress;
  doingTask?: TaskItem[];
  doingPatch?: TaskItem[];
  createTime: number;
  duration: number;
  firstSync?: number | null;
  num: Record<TaskNumKey, number>;
  size: Record<TaskNumKey, number>;
  doneSize?: number;
  remainSize?: number;
  speed?: number;
  speedAvg?: number;
  remainTime?: number;
}

export interface CurrentTaskView extends CurrentTaskData {
  remainSize: number;
  doneSize: number;
  speed: number;
  speedAvg: number;
  remainTime: number;
}

export interface NotifyItem {
  id: number;
  enable: number;
  method: number;
  params: string;
  /** 最近一次任务完成通知的投递结果：0 未投递过、1 成功、2 失败。 */
  lastSendStatus?: number;
  lastSendTime?: number;
  /** 失败原因，后端已脱敏（URL 只保留 scheme://host）。 */
  lastSendError?: string | null;
  createTime?: number;
}
