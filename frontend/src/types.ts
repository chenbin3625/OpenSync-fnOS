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
  engineHealthCheckInterval: number;
  defaultBwLimit?: string;
  proxyUrl?: string;
  localMaxConcurrentJobs?: number;
  minFreeSpaceMB?: number;
  autoResumeInterrupted?: number;
  digestEnable?: number;
  digestSchedule?: string;
  digestHour?: number;
  digestWeekday?: number;
  locale?: string;
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
  hasCrypt?: string;
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
  maxConcurrentJobs?: number;
  activeJobs?: number;
  queuedJobs?: number;
  pollIntervalSec?: number;
  weeklySchedule?: WeeklyScheduleConfig;
  bwLimit?: string;
  paused?: boolean;
}

export interface NativeStorageState {
  engineId: number;
  enabled: boolean;
  remark: string;
  maxConcurrentJobs: number;
  localMaxConcurrentJobs?: number;
  localActiveJobs?: number;
  localQueuedJobs?: number;
  rcloneAvailable: boolean;
  rcloneVersion?: string;
  rcloneError?: string;
  supportedTypes?: string[];
  localRoots: NativeLocalRoot[];
  remotes: NativeRemoteItem[];
}

export interface NativeTransferTask {
  id: string;
  kind: string;
  label: string;
  status: "running" | "completed" | "failed" | "cancelled" | string;
  doneBytes: number;
  totalBytes: number;
  speed: number;
  percent: number;
  completedItems: number;
  totalItems: number;
  startedAt: number;
  finishedAt: number;
  error?: string;
  detail?: string;
}

export interface NativeTrashItem {
  trashPath: string;
  originalPath: string;
  bucketDate: string;
  version: string;
  name: string;
  isDir: boolean;
  size: number;
  modTime: number;
}

export interface NativeRenameResult {
  oldName: string;
  newName: string;
  updatedJobs: number;
}

export interface NativeExportResult {
  content: string;
  encrypted: boolean;
  filename: string;
}

export interface NativeOpenListDiscoverCandidate {
  endpoint: string;
  baseUrl: string;
  reachable: boolean;
  statusCode?: number;
  authRequired: boolean;
  latencyMs: number;
  serverType?: string;
  error?: string;
}

export interface NativeOpenListSuggestedConfig {
  name: string;
  type: string;
  vendor: string;
  url: string;
  compareMode: number;
  compareModeLabel: string;
  recommendationNote: string;
}

export interface NativeOpenListDiscoverResult {
  found: boolean;
  message: string;
  suggestedConfig?: NativeOpenListSuggestedConfig;
  candidates: NativeOpenListDiscoverCandidate[];
  checkedAt: number;
}

export interface NativeCryptRecoveryBundle {
  version: number;
  generatedAt: number;
  remoteName: string;
  underlyingRemote: string;
  underlyingSubPath?: string;
  remote: string;
  filenameEncryption: string;
  filenameEncoding: string;
  directoryNameEncryption: string;
  noDataEncryption: boolean;
  suffix?: string;
  hasPassword: boolean;
  hasPassword2: boolean;
  managedByJob: boolean;
  managedJobLabel?: string;
  rcloneSnippetTemplate: string;
  rcloneCommand: string;
  recoverySteps: string[];
  warning: string;
}

export interface NativeCryptRecoveryResult {
  name: string;
  passwordChecked: boolean;
  passwordVerified?: boolean;
  password2Checked: boolean;
  password2Verified?: boolean;
  verifyMessage?: string;
  bundle: NativeCryptRecoveryBundle;
  recoveryText: string;
}

export interface NativeAboutInfo {
  supported: boolean;
  total?: number;
  used?: number;
  free?: number;
  trashed?: number;
  other?: number;
  message?: string;
}

export interface NativeDirEntry {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  modTime: number;
  md5?: string;
}

export interface NativeBrowseResult {
  path: string;
  entries: NativeDirEntry[];
  nextCursor?: string;
}

export interface NativeSizeInfo {
  count: number;
  bytes: number;
  sizeless: number;
}

export type SyncDirectionMode = "bidirectional" | "upload" | "download";

export type OneWayDeleteMode = "none" | "tracked" | "mirror";

export type WeeklySlotMode = "full" | "limited" | "pause";

export interface WeeklyScheduleConfig {
  enabled: boolean;
  limitRate: string;
  stopOutside: boolean;
  /** Indexed by day of week (0 = 周日 .. 6 = 周六) x 24 hours (0..23). */
  days: WeeklySlotMode[][];
}

export interface NativeExtraFlags {
  noUpdateModtime?: boolean;
  inplace?: boolean;
  checkFirst?: boolean;
  s3NoHead?: boolean;
  ignoreSize?: boolean;
  trackRenames?: boolean;
  useMmap?: boolean;
  maxTransfer?: string;
  cutoffMode?: string;
  maxDuration?: string;
  orderBy?: string;
  maxBacklog?: number;
  bufferSize?: string;
  multiThreadCutoff?: string;
  multiThreadChunkSize?: string;
  userAgent?: string;
  ioTimeout?: string;
  conTimeout?: string;
  lowLevelRetries?: number;
  retriesSleep?: string;
  resumeEnable?: boolean;
  resumeMinSizeMB?: number;
  watchEnable?: boolean;
  watchDebounceSec?: number;
  syncPollIntervalSec?: number;
  syncAuditIntervalMin?: number;
  autoRetryMax?: number;
  autoRetryDelayMin?: number;
  postSyncJobId?: number;
  checkSchedule?: string;
  missedCatchUp?: boolean;
  directStream?: boolean;
  bisyncMaxDelete?: number;
  bisyncForce?: boolean;
  syncDirectionMode?: SyncDirectionMode | "";
  oneWayDeleteMode?: OneWayDeleteMode | "";
  weeklySchedule?: WeeklyScheduleConfig;
}

export interface BisyncConflict {
  id: number;
  jobId: number;
  relPath: string;
  conflictType: "content" | "case" | "type" | "invalid_name" | string;
  conflictCopyPath: string;
  winnerSide: "A" | "B" | "" | string;
  aSize: number;
  aModTime: number;
  bSize: number;
  bModTime: number;
  detail: string;
  resolved: boolean;
  createdAt: number;
}

export interface SyncStatus {
  state:
    | "monitoring"
    | "idle"
    | "pending"
    | "scanning"
    | "syncing"
    | "synced"
    | "waiting_window"
    | "paused"
    | "error";
  pendingChanges: number;
  conflictCount?: number;
  lastSuccess: number;
  lastPoll: number;
  nextPoll: number;
  error: string;
  failures: number;
  retryAt?: number;
  errorType?: SyncErrorType;
  errorSide?: SyncErrorSide;
  errorEndpoint?: string;
  direction?: string;
  directionMode?: string;
  watchDegraded?: boolean;
  watchDegradedReason?: string;
  pollIntervalSec?: number;
  effectivePollSec?: number;
  pollBackoffStreak?: number;
  auditIntervalMin?: number;
  debounceSec?: number;
  scheduleState?: string;
  scheduleLimited?: boolean;
  schedulePaused?: boolean;
  weeklyLimitRate?: string;
  effectiveBwLimit?: string;
  connectionPaused?: boolean;
}

export type SyncErrorType =
  | "auth"
  | "local_permission"
  | "rate_limited"
  | "remote_permission"
  | "connection"
  | "transfer"
  | "state"
  | string;

export type SyncErrorSide = "local" | "remote" | "system" | "unknown" | string;

export interface JobCheckResult {
  jobId: number;
  status: "running" | "completed" | "failed" | "cancelled" | string;
  mode?: "check" | "download" | "cryptcheck" | string;
  download: boolean;
  cryptCheck?: boolean;
  startedAt: number;
  finishedAt: number;
  checkedFiles: number;
  totalFiles: number;
  matchedCount: number;
  differCount: number;
  missingOnSrcCount: number;
  missingOnDstCount: number;
  errorCount: number;
  differ: string[];
  missingOnSrc: string[];
  missingOnDst: string[];
  errors: string[];
  truncated: boolean;
  error?: string;
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

export interface NativeAuthQuestionExample {
  value: string;
  help: string;
}

export interface NativeAuthQuestion {
  state: string;
  name: string;
  help: string;
  default?: string;
  examples?: NativeAuthQuestionExample[];
  required?: boolean;
  isPassword?: boolean;
  exclusive?: boolean;
  type?: string;
  error?: string;
}

export interface NativeAuthStepSnapshot {
  sessionId: string;
  type: string;
  status:
    | "oauth"
    | "qrcode"
    | "question"
    | "completed"
    | "failed"
    | "cancelled"
    | string;
  authUrl?: string;
  redirectUri?: string;
  qrCodeUrl?: string;
  qrCodeImage?: string;
  qrStatus?: "waiting" | "expired" | string;
  question?: NativeAuthQuestion;
  publicOptions?: Record<string, string>;
  configuredSecrets?: string[];
  error?: string;
  expiresAt: number;
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
  // Native rclone tuning fields
  bwLimit?: string | null;
  tpsLimit?: number;
  tpsLimitBurst?: number;
  modifyWindow?: string | null;
  fastList?: number | boolean;
  multiThreadStreams?: number;
  createEmptySrcDirs?: number | boolean;
  followSymlinks?: number | boolean;
  extraFlags?: string | NativeExtraFlags | null;
  // Runtime decorations returned by GET /svr/job
  syncStatus?: SyncStatus;
  legacyOpenList?: boolean;
  running?: boolean;
  paused?: boolean;
  queued?: boolean;
  watching?: boolean;
  checkRunning?: boolean;
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
  doneBytes?: number;
  speed?: number;
  errMsg?: string | null;
  createTime?: number;
}

export interface TaskErrorGroup {
  reason: string;
  count: number;
}

export interface JobRecentTaskError {
  id: number;
  jobId: number;
  status: number;
  errMsg: string;
  runTime: number;
  createTime: number;
}

export interface TaskItemPageData extends PageData<TaskItem> {
  errorGroups?: TaskErrorGroup[];
  recentErrors?: JobRecentTaskError[];
}

export interface RecentTaskItem extends TaskItem {
  jobId: number;
  jobName: string;
  jobMethod: number;
}

export interface RecentTaskItemStats {
  allNum: number;
  waitNum: number;
  runningNum: number;
  successNum: number;
  failNum: number;
  otherNum: number;
  deleteNum: number;
  errorNum: number;
  sumSize: number;
}

export interface RecentTaskItemsPageData {
  dataList: RecentTaskItem[];
  count: number;
  pageNum: number;
  pageSize: number;
  stats: RecentTaskItemStats;
}

export type TaskNumKey = "wait" | "running" | "success" | "fail" | "other";

export interface ScanProgress {
  scannedDirs: number;
  remainingDirs: number;
  totalDirs: number;
  checkedFiles?: number;
  totalFiles?: number;
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
  paused?: boolean;
  liveBwLimit?: string;
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

export interface DashboardJobItem {
  syncStatus?: SyncStatus;
  id: number;
  remark?: string | null;
  enable: number;
  alistId: number;
  alistName: string;
  legacyOpenList?: boolean;
  method: number;
  isCron: number;
  interval: number;
  srcPath: string;
  dstPath: string;
  running: boolean;
  paused?: boolean;
  queued: boolean;
  watching?: boolean;
  checkRunning?: boolean;
  currentTaskId: number;
  liveNum?: Record<TaskNumKey, number> | null;
  liveProgress: number;
  liveDoneSize?: number;
  liveTotalSize?: number;
  liveSpeed?: number;
  liveSpeedAvg?: number;
  liveRemainTime?: number;
  liveBwLimit?: string;
  liveScanFinish?: boolean;
  liveScan?: ScanProgress | null;
  nextRunTime: number;
  windowEnable?: number;
  windowStart?: string | null;
  windowEnd?: string | null;
  extraFlags?: string | NativeExtraFlags | null;
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

export interface DashboardDailyTrend {
  date: string;
  runs: number;
  successRuns: number;
  failRuns: number;
  files: number;
  bytes: number;
}

export interface DashboardQuotaItem {
  name: string;
  type: string;
  supported: boolean;
  total?: number;
  used?: number;
  free?: number;
  message?: string;
}

export interface DashboardSpeedSample {
  ts: number;
  speed: number;
}

export interface DashboardRealtime {
  totalSpeed: number;
  totalSpeedAvg: number;
  peakSpeed: number;
  activeJobs: number;
  pausedJobs: number;
  activeTransfers: number;
  totalDoneBytes: number;
  totalRemainBytes: number;
  estimatedRemainTime: number;
  globalBwLimit?: string;
  sampledAt: number;
  speedHistory: DashboardSpeedSample[];
}

export interface DashboardOverview {
  kpi: DashboardKPI;
  realtime?: DashboardRealtime;
  recent7d: DashboardRecent7d;
  jobs: DashboardJobItem[];
  dailyTrend?: DashboardDailyTrend[];
  storageQuotas?: DashboardQuotaItem[];
}

export interface BatchJobSkippedItem {
  jobId: number;
  jobName: string;
  reason: string;
}

export interface BatchJobActionResult {
  action: string;
  succeeded: number;
  failed: number;
  skipped?: number;
  skippedJobs?: BatchJobSkippedItem[];
  results: Array<{
    jobId: number;
    ok: boolean;
    skipped?: boolean;
    error?: string;
  }>;
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
