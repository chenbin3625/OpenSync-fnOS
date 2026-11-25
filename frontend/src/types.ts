export interface ApiResponse<T> {
  code: number;
  data: T;
  msg: string;
}

export interface PageData<T> {
  dataList: T[];
  count: number;
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
  scheduledFullSyncConfirm: number;
  fullSyncApprovalTimeout: number;
  engineHealthCheckInterval: number;
}

export interface EngineStorageStatus {
  mountPath: string;
  driver: string;
  status: string;
  disabled: boolean;
}

export interface EngineHealth {
  alistId: number;
  status: "unknown" | "online" | "offline" | string;
  latencyMs: number;
  lastCheckedAt: number;
  error?: string;
  storages?: EngineStorageStatus[];
  storagesKnown?: boolean;
}

export interface NativeRemoteDetail {
  type: string;
  summary?: string;
}

export interface NativeLocalRoot {
  path: string;
  id: string;
}

export interface NativeRemoteItem {
  name: string;
  type: string;
  summary: string;
  options: Record<string, string>;
  configuredSecrets: string[];
  usedByJobs: string[];
  usedByCrypts: string[];
}

export interface NativeStorageState {
  engineId: number;
  enabled: boolean;
  remark: string;
  maxConcurrentJobs: number;
  rcloneAvailable: boolean;
  rcloneError?: string;
  localRoots: NativeLocalRoot[];
  remotes: NativeRemoteItem[];
}

export interface NativeImportCandidate {
  name: string;
  type: string;
  status: "ready" | "conflict" | "incompatible" | "invalid";
  existingName?: string;
  existingType?: string;
  canReplace: boolean;
  summary?: string;
  options: Record<string, string>;
  configuredSecrets: string[];
  warnings?: string[];
  error?: string;
}

export interface NativeImportPreview {
  encrypted: boolean;
  items: NativeImportCandidate[];
}

export interface NativeImportResult {
  imported: string[];
  replaced: string[];
}

export interface NativeStorageTestResult {
  ok: boolean;
  latencyMs: number;
  message: string;
  warning?: string;
  dirsCount?: number;
}

export interface AlistItem {
  kind?: "rclone";
  remotes?: Record<string, string>;
  remoteDetails?: Record<string, NativeRemoteDetail>;
  id: number;
  remark?: string | null;
  url: string;
  userName: string;
  maxConcurrentJobs?: number;
  activeJobs?: number;
  queuedJobs?: number;
  health?: EngineHealth;
  createTime?: number;
}

export interface JobItem {
  id: number;
  enable: number;
  remark?: string | null;
  srcPath: string;
  dstPath: string;
  dstMap?: string | null;
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
  // Extended sync/filter/schedule/notify fields
  compareMode?: number;
  conflictPolicy?: number;
  trashEnable?: number | boolean;
  trashDir?: string | null;
  trashRetentionDays?: number;
  deleteEmptySrcDirs?: number | boolean;
  filterMode?: number;
  include?: string | null;
  minFileAgeMinutes?: number;
  olderThanDays?: number;
  newerThanDays?: number;
  copyConcurrency?: number;
  scanConcurrency?: number;
  maxRetries?: number;
  windowEnable?: number | boolean;
  windowStart?: string | null;
  windowEnd?: string | null;
  windowStopOutside?: number | boolean;
  notifyMode?: number;
  notifyIds?: string | number[] | null;
  // Runtime decorations returned by GET /svr/job
  running?: boolean;
  queued?: boolean;
  pendingApproval?: boolean;
  lastStatus?: number;
  createTime?: number;
  /** Next scheduled run (unix seconds); 0 or absent when not scheduled. */
  nextRunTime?: number;
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
  sumSize?: number;
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
  checkedFiles?: number;
  totalFiles?: number;
}

export interface FullSyncApprovalPath {
  path: string;
  isPath: boolean;
  count: number;
  size: number;
  action: "删除" | "移动" | string;
}

export interface FullSyncApproval {
  jobId?: number;
  jobName?: string;
  taskId?: number;
  deleteCount: number;
  deleteSize: number;
  pathCount: number;
  paths: FullSyncApprovalPath[];
  status?: "pending" | "executing" | string;
  createdAt?: number;
  expiresAt?: number;
  planChanged?: boolean;
  lastError?: string;
}

export interface CurrentTaskData {
  taskId: number;
  scanFinish: boolean;
  approval?: FullSyncApproval | null;
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

export interface DashboardKPI {
  totalJobs: number;
  enabledJobs: number;
  runningJobs: number;
  queuedJobs: number;
  pendingApprovals: number;
  totalEngines: number;
  onlineEngines: number;
  offlineEngines: number;
}

export interface DashboardRecent7d {
  totalRuns: number;
  successRuns: number;
  failRuns: number;
  stoppedRuns: number;
  totalFiles: number;
  totalBytes: number;
}

export interface DashboardPendingApproval {
  jobId: number;
  jobName: string;
  taskId: number;
  deleteCount: number;
  deleteSize: number;
  pathCount: number;
  status?: "pending" | "executing" | string;
  createdAt?: number;
  expiresAt?: number;
  planChanged?: boolean;
  lastError?: string;
}

export interface DashboardJobItem {
  id: number;
  remark?: string | null;
  enable: number;
  alistId: number;
  alistName: string;
  method: number;
  isCron: number;
  interval: number;
  srcPath: string;
  dstPath: string;
  running: boolean;
  queued: boolean;
  currentTaskId: number;
  pendingApproval?: FullSyncApproval | null;
  liveNum?: Record<TaskNumKey, number> | null;
  liveProgress: number;
  nextRunTime: number;
  windowEnable?: number;
  windowStart?: string | null;
  windowEnd?: string | null;
  lastTask?: {
    id: number;
    status: number;
    createTime: number;
    runTime: number;
    successNum?: number;
    failNum?: number;
    allNum?: number;
    sumSize?: number;
    duration?: number;
    errMsg?: string | null;
  } | null;
}

export interface DashboardOverview {
  kpi: DashboardKPI;
  recent7d: DashboardRecent7d;
  pendingApprovals: DashboardPendingApproval[];
  jobs: DashboardJobItem[];
}

export interface BatchJobActionResult {
  action: string;
  succeeded: number;
  failed: number;
  results: Array<{ jobId: number; ok: boolean; error?: string }>;
}

export interface DryRunItem {
  action: "copy" | "update" | "delete" | "move" | "mkdir" | "skip" | string;
  srcPath?: string;
  dstPath?: string;
  fileName: string;
  fileSize: number;
  isPath: boolean;
  reason?: string;
}

export interface DryRunResult {
  jobId: number;
  status: "running" | "completed" | "failed" | "cancelled" | string;
  method: number;
  startedAt: number;
  finishedAt?: number;
  scannedDirs: number;
  copyCount: number;
  copySize: number;
  updateCount: number;
  updateSize: number;
  deleteCount: number;
  deleteSize: number;
  moveCount: number;
  moveSize: number;
  mkdirCount: number;
  skipCount: number;
  totalItems: number;
  itemsTruncated: boolean;
  items: DryRunItem[];
  error?: string;
}

export interface FilterTestResult {
  willSync: boolean;
  reason: string;
  matchedRule: string;
}

export interface SystemBackupPayload {
  version: number;
  exportedAt: number;
  hasSecrets: boolean;
  settings?: SystemSettings;
  engines: Record<string, unknown>[];
  notifications: Record<string, unknown>[];
  jobs: Record<string, unknown>[];
}

export interface SystemImportSummary {
  settingsUpdated: boolean;
  enginesCreated: number;
  enginesUpdated: number;
  enginesSkipped: number;
  notifiesCreated: number;
  notifiesUpdated: number;
  notifiesSkipped: number;
  jobsCreated: number;
  jobsUpdated: number;
  jobsSkipped: number;
  warnings: string[];
}

export interface AppLogsResponse {
  lines: string[];
  total: number;
  fileSize: number;
  source: string;
}
