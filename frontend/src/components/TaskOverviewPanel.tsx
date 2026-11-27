import { useEffect, useMemo, useRef } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Progress from "@douyinfe/semi-ui/lib/es/progress";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconAlertTriangle,
  IconArrowRight,
  IconChevronRight,
  IconClockStroked,
  IconCopy,
  IconDeleteStroked,
  IconEditStroked,
  IconFolderStroked,
  IconPause,
  IconPlay,
  IconRefresh,
  IconStop,
  IconTickCircle,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  ActionMenu,
  IconButton,
  Info,
  InlineNotice,
  SectionCard,
  type MenuAction,
} from "./common";
import {
  SyncStatusTag,
  formatContinuousSyncPlan,
  formatMinutesShort,
  formatRelativeSyncTime,
  formatSecondsShort,
  formatSyncNextAction,
  getContinuousSyncMeta,
  parseJobExtraFlagsObject,
  resolveEffectiveSyncStatus,
  resolveSyncError,
  syncStatusLabels,
  syncStatusTone,
} from "./SyncStatus";
import { useResource } from "../lib/hooks";
import { describeBwLimit } from "../lib/bwLimit";
import { formatSpeed } from "../lib/dashboardMetrics";
import { formatWeeklyScheduleSummary } from "../lib/weeklySchedule";
import {
  buildSyncPathPreview,
  formatDisplayPath,
  formatDisplayPathList,
} from "../lib/syncPathPreview";
import { isLegacyOpenListJob, summarizeJobFilters } from "../lib/taskForm";
import {
  compareModeLabel,
  conflictPolicyLabel,
  formatDuration,
  formatSize,
  getJobName,
  parseJobPathList,
} from "../pages/Home/homeUtils";
import { calcRealtimeProgress, calcTaskDisplayPercent } from "../pages/Home/taskRows";
import { formatTimestamp } from "../utils/date";
import type { AlistItem, CurrentTaskData, JobItem, TaskRecord } from "../types";

export interface TaskOverviewPanelProps {
  job: JobItem;
  engines: AlistItem[];
  busy: boolean;
  currentTask: CurrentTaskData | null;
  paused: boolean;
  runKey: string;
  onRun: () => void;
  onOpenRecords?: () => void;
  onOpenFailedRecords?: () => void;
  onRepairConnection?: () => void;
  onLocalFolders?: () => void;
  onOpenRealtime?: () => void;
  onCheck: () => void;
  onOpenConflicts: () => void;
  onClone: () => void;
  onTogglePause: () => void;
  onStop: () => void;
  onToggle: () => void;
  onEdit: () => void;
  onDelete: () => void;
}

export function TaskOverviewPanel({
  job,
  engines,
  busy,
  currentTask,
  paused,
  runKey,
  onRun,
  onOpenRecords,
  onOpenFailedRecords,
  onRepairConnection,
  onLocalFolders,
  onOpenRealtime,
  onCheck,
  onOpenConflicts,
  onClone,
  onTogglePause,
  onStop,
  onToggle,
  onEdit,
  onDelete,
}: TaskOverviewPanelProps) {
  const latestExecution = useResource(
    (signal) => api.history(job.id, { pageNum: 1, pageSize: 1 }, signal),
    [job.id],
  );
  const refreshLatest = latestExecution.refresh;
  const firstRunKey = useRef(runKey);
  useEffect(() => {
    if (firstRunKey.current === runKey) return;
    firstRunKey.current = runKey;
    void refreshLatest(true);
  }, [runKey, refreshLatest]);

  const legacy = isLegacyOpenListJob(job, engines);
  const engine = legacy
    ? undefined
    : (engines.find((e) => e.id === job.alistId) ?? engines[0]);
  const engineName = legacy
    ? "旧版 OpenList（已失效）"
    : engine?.remark || "内置存储";
  const enabled = !legacy && job.enable === 1;
  const running = Boolean(currentTask);
  const meta = useMemo(() => getContinuousSyncMeta(job), [job]);
  const extraFlags = useMemo(
    () => parseJobExtraFlagsObject(job.extraFlags),
    [job.extraFlags],
  );
  const oneWayTrackedDelete =
    meta.directionKey !== "bidirectional" &&
    job.method !== 1 &&
    extraFlags.oneWayDeleteMode === "tracked";
  const weeklyEnabled = Boolean(extraFlags.weeklySchedule?.enabled);
  const effectiveStatus = useMemo(
    () =>
      resolveEffectiveSyncStatus({
        enable: job.enable,
        running,
        paused,
        syncStatus: job.syncStatus,
      }),
    [job.enable, running, paused, job.syncStatus],
  );
  const statusTone = syncStatusTone(effectiveStatus.state);
  const syncError = resolveSyncError(effectiveStatus);
  const nextAction = formatSyncNextAction(effectiveStatus);

  const liveMetrics = useMemo(() => {
    if (!currentTask) return null;
    const calc = calcRealtimeProgress(currentTask, null);
    const totalBytes = calc.doneSize + calc.remainSize;
    const totalItems =
      (currentTask.num?.wait || 0) +
      (currentTask.num?.running || 0) +
      (currentTask.num?.success || 0) +
      (currentTask.num?.fail || 0) +
      (currentTask.num?.other || 0);
    const percent = calcTaskDisplayPercent({
      doneSize: calc.doneSize,
      totalSize: totalBytes,
      num: currentTask.num,
      scanFinish: currentTask.scanFinish,
    });
    return {
      ...calc,
      totalBytes,
      totalItems,
      successItems: currentTask.num?.success || 0,
      failItems: currentTask.num?.fail || 0,
      percent,
    };
  }, [currentTask]);

  const filterSummaries = useMemo(() => summarizeJobFilters(job), [job]);
  const extraFlagLabels = useMemo(() => {
    const raw = job.extraFlags;
    if (!raw) return [] as string[];
    let obj: Record<string, unknown> | null = null;
    if (typeof raw === "string") {
      try {
        obj = JSON.parse(raw) as Record<string, unknown>;
      } catch {
        obj = null;
      }
    } else if (typeof raw === "object") {
      obj = raw as Record<string, unknown>;
    }
    if (!obj) return [] as string[];
    const list: string[] = [];
    if (obj.noUpdateModtime) list.push("跳过仅时间更新");
    if (obj.inplace) list.push("原地写入");
    if (obj.checkFirst) list.push("先比对后传输");
    if (obj.s3NoHead) list.push("跳过 S3 HEAD");
    if (obj.ignoreSize) list.push("忽略大小差异");
    if (obj.trackRenames) list.push("追踪重命名");
    if (obj.useMmap) list.push("mmap 缓冲");
    if (obj.resumeEnable) list.push("断点续传");
    if (obj.directStream) list.push("直接流式同步");
    if (Number(obj.autoRetryMax || 0) > 0) {
      list.push(`自动重试 ${obj.autoRetryMax} 次`);
    }
    if (Number(obj.postSyncJobId || 0) > 0) {
      list.push(`串联任务 #${obj.postSyncJobId}`);
    }
    if (typeof obj.checkSchedule === "string" && obj.checkSchedule.trim()) {
      const m: Record<string, string> = {
        daily: "每日校验",
        weekly: "每周校验",
        monthly: "每月校验",
      };
      list.push(m[obj.checkSchedule.trim()] || obj.checkSchedule.trim());
    }
    if (typeof obj.maxTransfer === "string" && obj.maxTransfer.trim()) {
      list.push(`上限 ${obj.maxTransfer.trim()}`);
    }
    if (typeof obj.cutoffMode === "string" && obj.cutoffMode.trim()) {
      list.push(`截断 ${obj.cutoffMode.trim()}`);
    }
    if (typeof obj.maxDuration === "string" && obj.maxDuration.trim()) {
      list.push(`限时 ${obj.maxDuration.trim()}`);
    }
    if (typeof obj.orderBy === "string" && obj.orderBy.trim()) {
      list.push(`排序 ${obj.orderBy.trim()}`);
    }
    if (Number(obj.maxBacklog || 0) > 0) list.push(`队列上限 ${obj.maxBacklog}`);
    if (typeof obj.bufferSize === "string" && obj.bufferSize.trim()) {
      list.push(`缓冲 ${obj.bufferSize.trim()}`);
    }
    if (typeof obj.multiThreadCutoff === "string" && obj.multiThreadCutoff.trim()) {
      list.push(`多线程阈值 ${obj.multiThreadCutoff.trim()}`);
    }
    if (typeof obj.multiThreadChunkSize === "string" && obj.multiThreadChunkSize.trim()) {
      list.push(`多线程块 ${obj.multiThreadChunkSize.trim()}`);
    }
    if (typeof obj.userAgent === "string" && obj.userAgent.trim()) {
      list.push("自定义 UA");
    }
    if (typeof obj.ioTimeout === "string" && obj.ioTimeout.trim()) {
      list.push(`I/O超时 ${obj.ioTimeout.trim()}`);
    }
    if (typeof obj.conTimeout === "string" && obj.conTimeout.trim()) {
      list.push(`连接超时 ${obj.conTimeout.trim()}`);
    }
    if (Number(obj.bisyncMaxDelete || 0) > 0) {
      list.push(`最多删除 ${obj.bisyncMaxDelete}%`);
    }
    if (obj.bisyncForce) list.push("超限强删");
    return list;
  }, [job.extraFlags]);

  const throttleLabels = [
    Number(job.copyConcurrency || 0) > 0 ? `操作并发 ${job.copyConcurrency}` : "",
    Number(job.scanConcurrency || 0) > 0 ? `扫描并发 ${job.scanConcurrency}` : "",
    job.maxRetries !== undefined && Number(job.maxRetries) >= 0
      ? `重试 ${job.maxRetries} 次`
      : "",
    job.bwLimit?.trim()
      ? (() => {
          const trimmed = job.bwLimit.trim();
          if (/^(?:off|0)$/i.test(trimmed)) return "不限速（覆盖全局）";
          const bwInfo = describeBwLimit(trimmed);
          return bwInfo.hasSchedule
            ? bwInfo.chipLabel
            : `限速 ${bwInfo.activeRateCompact}`;
        })()
      : "",
    Number(job.tpsLimit || 0) > 0 ? `TPS ${job.tpsLimit}/s` : "",
    Number(job.tpsLimitBurst || 0) > 0 ? `突发 ${job.tpsLimitBurst}` : "",
    job.modifyWindow?.trim() ? `时间容差 ${job.modifyWindow.trim()}` : "",
    Number(job.multiThreadStreams || 0) > 0
      ? `${job.multiThreadStreams} 线程/文件`
      : "",
    Boolean(job.fastList) ? "fast-list" : "",
    Boolean(job.createEmptySrcDirs) ? "同步空目录" : "",
    Boolean(job.followSymlinks) ? "跟随符号链接" : "",
    ...extraFlagLabels,
  ].filter(Boolean);

  const srcList = parseJobPathList(job.srcPath);
  const dstList = parseJobPathList(job.dstPath);
  const displaySiblings = [...srcList, ...dstList];
  const pathPreview = useMemo(
    () =>
      buildSyncPathPreview({
        srcPaths: srcList,
        dstPaths: dstList,
        storedDstMap: job.dstMap,
        method: job.method,
      }),
    [job.srcPath, job.dstPath, job.dstMap, job.method],
  );
  const showPathMapping =
    pathPreview.items.length > 1 ||
    pathPreview.items.some((item) => Boolean(item.appendedSuffix));

  const menu: MenuAction[] = legacy
    ? [
        {
          label: "删除",
          icon: <IconDeleteStroked aria-hidden="true" />,
          onClick: onDelete,
          danger: true,
        },
      ]
    : [
        ...(job.method === 3
          ? [
              {
                label:
                  (effectiveStatus.conflictCount || 0) > 0
                    ? `冲突处理 (${effectiveStatus.conflictCount})`
                    : "冲突处理",
                icon: <IconAlertTriangle aria-hidden="true" />,
                onClick: onOpenConflicts,
              },
            ]
          : []),
        {
          label: "复制任务",
          icon: <IconCopy aria-hidden="true" />,
          onClick: onClone,
        },
        {
          label: "删除",
          icon: <IconDeleteStroked aria-hidden="true" />,
          onClick: onDelete,
          danger: true,
        },
      ];

  const notifyBindingText = (() => {
    const mode = Number(job.notifyMode ?? 0);
    if (mode === 2) return "已关闭此任务通知";
    if (mode === 1) {
      let count = 0;
      if (Array.isArray(job.notifyIds)) count = job.notifyIds.length;
      else if (typeof job.notifyIds === "string") {
        try {
          const arr = JSON.parse(job.notifyIds);
          if (Array.isArray(arr)) count = arr.length;
        } catch {
          count = 0;
        }
      }
      return `已绑定 ${count} 个指定通知渠道`;
    }
    return "";
  })();

  const srcRoleLabel =
    meta.directionKey === "bidirectional" ? "左侧目录" : "源目录";
  const dstRoleLabel =
    meta.directionKey === "bidirectional" ? "右侧目录" : "目标目录";

  const statusHeadline = (() => {
    if (running) {
      if (paused) return "本次传输已暂停";
      if (currentTask && !currentTask.scanFinish) {
        return currentTask.firstSync ? "正在同步（扫描中）" : "正在扫描变更";
      }
      return "正在同步变更";
    }
    switch (effectiveStatus.state) {
      case "synced":
        return "已同步";
      case "monitoring":
        return "正在持续监控变更";
      case "pending":
        return effectiveStatus.pendingChanges > 0
          ? `检测到 ${effectiveStatus.pendingChanges} 项待同步`
          : "等待执行完整对账";
      case "scanning":
        return "正在检测目录变更";
      case "waiting_window":
        return "等待进入运行窗口";
      case "idle":
        return "按周计划待命中";
      case "paused":
        if (effectiveStatus.connectionPaused) {
          return enabled ? "所属连接已暂停" : "任务与所属连接均已暂停";
        }
        return "持续同步已暂停";
      case "error":
        return effectiveStatus.failures > 1
          ? `同步异常（连续失败 ${effectiveStatus.failures} 次）`
          : "同步异常";
      default:
        return syncStatusLabels[effectiveStatus.state] || "监控中";
    }
  })();

  const statusSubtitle = (() => {
    if (running) {
      if (paused) return "当前传输流已暂停，可随时继续传输或中止本次批次";
      return "检测到变更，正在自动同步至目标端，无需手动干预";
    }
    switch (effectiveStatus.state) {
      case "synced":
        return effectiveStatus.lastSuccess > 0
          ? `双端已一致 · 最近同步 ${formatRelativeSyncTime(effectiveStatus.lastSuccess)}`
          : "双端已一致，持续监控新增与修改";
      case "monitoring":
        return "本地实时监听与远端周期检测已就绪，发现变更后自动同步";
      case "pending":
        return `等待文件写入静默（${formatSecondsShort(meta.debounceSec)}）后自动开始传输`;
      case "scanning":
        return "正在比对本地与远端目录快照";
      case "waiting_window":
        if (effectiveStatus.schedulePaused || weeklyEnabled) {
          return "当前处于每周定时计划暂停时段（暂停触发，进入运行/限速时段后自动恢复）";
        }
        return job.windowEnable
          ? `当前不在允许窗口（${job.windowStart || "00:00"}–${job.windowEnd || "06:00"}），窗口恢复后自动补齐`
          : "当前处于窗口外，窗口恢复后自动补齐";
      case "idle":
        return formatWeeklyScheduleSummary(extraFlags.weeklySchedule);
      case "paused":
        if (effectiveStatus.connectionPaused) {
          return enabled
            ? "所属连接已暂停，在连接设置中恢复后将继续自动同步"
            : "所属连接与当前任务均已暂停；恢复连接时不会自动唤醒手动暂停的任务";
        }
        return "恢复同步后将自动对账并补齐暂停期间产生的变更";
      case "error":
        if (syncError.type === "auth") {
          return "更新远端连接凭据后，系统会自动补齐待同步变更";
        }
        if (syncError.type === "local_permission") {
          return "授权飞牛本地目录后，系统会自动补齐待同步变更";
        }
        if (syncError.type === "rate_limited") {
          return nextAction || "系统会按限流退避策略自动重试";
        }
        return nextAction || "系统将按退避策略自动重试，你也可以查看失败记录逐项重试";
      default:
        return "";
    }
  })();

  const pendingSummary =
    effectiveStatus.pendingChanges < 0
      ? "待完整核对"
      : effectiveStatus.pendingChanges > 0
        ? `${effectiveStatus.pendingChanges} 项待同步`
        : "已全部同步";

  return (
    <div className="overview">
      <SectionCard
        className="overview-card"
        headClassName="overview-head"
        title={
          <span className="engine-title-row">
            <h2 title={getJobName(job)}>{getJobName(job)}</h2>
            {legacy ? (
              <Tag size="small" type="solid" color="red">
                旧版已停用
              </Tag>
            ) : (
              <>
                <Tag
                  size="small"
                  type="light"
                  color={meta.directionKey === "bidirectional" ? "violet" : "blue"}
                >
                  {meta.directionLabel}
                </Tag>
                {meta.directionKey !== "bidirectional" && meta.syncDelete && (
                  <Tag size="small" type="light" color="orange">
                    同步删除
                  </Tag>
                )}
                {job.queued && (
                  <Tag size="small" type="light" color="cyan">
                    排队等待中
                  </Tag>
                )}
                <SyncStatusTag status={effectiveStatus} />
              </>
            )}
          </span>
        }
        extra={
          <div className="overview-head-actions">
            {legacy ? (
              <Button
                size="small"
                type="danger"
                theme="solid"
                icon={<IconDeleteStroked aria-hidden="true" />}
                disabled={busy}
                onClick={onDelete}
              >
                删除任务
              </Button>
            ) : (
              <>
                <Button
                  size="small"
                  type={enabled ? "tertiary" : "primary"}
                  theme={enabled ? "light" : "solid"}
                  icon={
                    enabled ? (
                      <IconPause aria-hidden="true" />
                    ) : (
                      <IconPlay aria-hidden="true" />
                    )
                  }
                  disabled={busy}
                  onClick={onToggle}
                >
                  {enabled ? "暂停同步" : "恢复同步"}
                </Button>
                {running ? (
                  <>
                    <IconButton
                      label={paused ? "继续传输" : "暂停传输"}
                      icon={
                        paused ? (
                          <IconPlay aria-hidden="true" />
                        ) : (
                          <IconPause aria-hidden="true" />
                        )
                      }
                      disabled={busy}
                      onClick={onTogglePause}
                    />
                    <IconButton
                      label="中止本次同步"
                      danger
                      icon={<IconStop aria-hidden="true" />}
                      disabled={busy}
                      onClick={onStop}
                    />
                  </>
                ) : (
                  <Tooltip
                    content={enabled ? "立即检测并同步一次" : "请先恢复持续同步"}
                    position="top"
                  >
                    <Button
                      size="small"
                      type="tertiary"
                      theme="light"
                      icon={<IconRefresh aria-hidden="true" />}
                      disabled={busy || !enabled}
                      onClick={onRun}
                    >
                      立即同步
                    </Button>
                  </Tooltip>
                )}
                <IconButton
                  label="数据一致性校验（rclone check）"
                  icon={<IconTickCircle aria-hidden="true" />}
                  disabled={busy}
                  onClick={onCheck}
                />
                <IconButton
                  label="编辑任务"
                  icon={<IconEditStroked aria-hidden="true" />}
                  disabled={busy}
                  onClick={onEdit}
                />
                <ActionMenu actions={menu} disabled={busy} />
              </>
            )}
          </div>
        }
      >
        {legacy && (
          <InlineNotice
            role="alert"
            icon={<IconAlertTriangle aria-hidden="true" size="small" />}
            message="该任务关联旧版 OpenList 引擎，升级后已自动停用且无法运行或编辑。请删除此任务后，在「存储管理」接入对应存储并重新创建持续同步任务。"
          />
        )}

        <div className="sync-route-card">
          <div className="sync-route-node">
            <div className="flow-name">
              <IconFolderStroked aria-hidden="true" />
              {srcRoleLabel}
            </div>
            <div className="flow-sub flow-paths">
              {formatDisplayPathList(srcList, displaySiblings).join("\n") || "—"}
            </div>
          </div>

          <div className="sync-route-connector" aria-label={meta.directionLabel}>
            <span className="sync-route-arrow" aria-hidden="true">
              {meta.arrow}
            </span>
            <span className="sync-route-badge">{meta.directionLabel}</span>
            <span className="sync-route-policy muted">
              {meta.directionKey === "bidirectional"
                ? "双端保持一致"
                : meta.syncDelete
                  ? "同步删除"
                  : "保留目标文件"}
            </span>
          </div>

          <div className="sync-route-node">
            <div className="flow-name" title={engineName}>
              <IconFolderStroked aria-hidden="true" />
              {dstRoleLabel}
            </div>
            <div className="flow-sub flow-paths">
              {formatDisplayPathList(dstList, displaySiblings).join("\n") || "—"}
            </div>
          </div>
        </div>

        {!legacy && (
          <div className={`continuous-status-card is-${statusTone}`}>
            <div className="continuous-status-head">
              <div className="continuous-status-main">
                <span
                  className={`continuous-status-indicator is-${statusTone}${
                    running && !paused ? " is-pulse" : ""
                  }`}
                  aria-hidden="true"
                >
                  {statusTone === "success" ? (
                    <IconTickCircle size="large" />
                  ) : statusTone === "danger" ? (
                    <IconAlertTriangle size="large" />
                  ) : statusTone === "warning" ? (
                    <IconClockStroked size="large" />
                  ) : statusTone === "muted" ? (
                    <IconPause size="large" />
                  ) : (
                    <IconRefresh size="large" />
                  )}
                </span>
                <div className="continuous-status-copy">
                  <div className="continuous-status-title">{statusHeadline}</div>
                  {statusSubtitle && (
                    <div className="continuous-status-sub">{statusSubtitle}</div>
                  )}
                </div>
              </div>
              <div className="continuous-status-cta">
                {running ? (
                  <Button
                    size="small"
                    type="primary"
                    theme="light"
                    icon={<IconChevronRight aria-hidden="true" />}
                    iconPosition="right"
                    onClick={onOpenRecords ?? onOpenRealtime}
                  >
                    查看传输明细
                  </Button>
                ) : effectiveStatus.state === "error" && syncError.type === "auth" && onRepairConnection ? (
                  <Button
                    size="small"
                    type="warning"
                    theme="light"
                    icon={<IconRefresh aria-hidden="true" />}
                    disabled={busy}
                    onClick={onRepairConnection}
                  >
                    重新授权
                  </Button>
                ) : effectiveStatus.state === "error" && syncError.type === "local_permission" && onLocalFolders ? (
                  <Button
                    size="small"
                    type="warning"
                    theme="light"
                    icon={<IconFolderStroked aria-hidden="true" />}
                    disabled={busy}
                    onClick={onLocalFolders}
                  >
                    查看本地授权
                  </Button>
                ) : effectiveStatus.state === "error" &&
                  syncError.type !== "rate_limited" &&
                  syncError.type !== "auth" &&
                  syncError.type !== "local_permission" &&
                  onOpenFailedRecords &&
                  job.method !== 3 ? (
                  <Button
                    size="small"
                    type="warning"
                    theme="light"
                    icon={<IconChevronRight aria-hidden="true" />}
                    iconPosition="right"
                    onClick={onOpenFailedRecords}
                  >
                    查看失败记录
                  </Button>
                ) : effectiveStatus.state === "error" && enabled ? (
                  <Button
                    size="small"
                    type="danger"
                    theme="light"
                    icon={<IconRefresh aria-hidden="true" />}
                    disabled={busy}
                    onClick={onRun}
                  >
                    立即重试
                  </Button>
                ) : (effectiveStatus.conflictCount || 0) > 0 && job.method === 3 ? (
                  <Button
                    size="small"
                    type="warning"
                    theme="solid"
                    icon={<IconAlertTriangle aria-hidden="true" />}
                    disabled={busy}
                    onClick={onOpenConflicts}
                  >
                    处理 {effectiveStatus.conflictCount} 项冲突
                  </Button>
                ) : !enabled ? (
                  <Button
                    size="small"
                    type="primary"
                    theme="solid"
                    icon={<IconPlay aria-hidden="true" />}
                    disabled={busy}
                    onClick={onToggle}
                  >
                    恢复同步
                  </Button>
                ) : null}
              </div>
            </div>

            {currentTask && liveMetrics && (
              <div className="continuous-live-progress">
                <div className="continuous-live-progress-meta">
                  <span className="continuous-live-speed">
                    {paused
                      ? "已暂停"
                      : liveMetrics.speed > 0
                        ? formatSpeed(liveMetrics.speed)
                        : currentTask.scanFinish
                          ? "0 B/s"
                          : "扫描比对中"}
                  </span>
                  <span className="muted">
                    已完成 {formatSize(liveMetrics.doneSize)} /{" "}
                    {formatSize(liveMetrics.totalBytes)}
                    {liveMetrics.totalItems > 0
                      ? `（${liveMetrics.successItems} / ${liveMetrics.totalItems} 项）`
                      : ""}
                  </span>
                  {liveMetrics.failItems > 0 && (
                    <span className="inline-error">
                      失败 {liveMetrics.failItems}
                    </span>
                  )}
                  {!paused && liveMetrics.remainTime > 0 && (
                    <span className="muted">
                      剩余 {formatDuration(liveMetrics.remainTime)}
                    </span>
                  )}
                  <strong className="continuous-live-pct">
                    {liveMetrics.percent}%
                  </strong>
                </div>
                <Progress
                  percent={liveMetrics.percent}
                  showInfo={false}
                  stroke={
                    paused
                      ? "var(--semi-color-warning)"
                      : "var(--semi-color-primary)"
                  }
                />
                {!currentTask.scanFinish && currentTask.scan && (
                  <div className="continuous-live-scan muted">
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
              </div>
            )}

            {effectiveStatus.watchDegraded && (
              <InlineNotice
                role="alert"
                icon={<IconAlertTriangle aria-hidden="true" size="small" />}
                message={
                  effectiveStatus.watchDegradedReason?.trim()
                    ? `实时文件监听已降级为定期轮询：${effectiveStatus.watchDegradedReason}`
                    : "实时文件监听已降级为定期轮询（inotify 上限或目录限制），变更检测可能存在数分钟延迟。"
                }
              />
            )}

            {effectiveStatus.error && (
              <div className="continuous-status-error" role="alert">
                <IconAlertTriangle aria-hidden="true" size="small" />
                <span>{effectiveStatus.error}</span>
              </div>
            )}

            <div className="continuous-status-metrics">
              <div className="continuous-metric">
                <span className="continuous-metric-label">待同步变更</span>
                <strong className="continuous-metric-value">
                  {pendingSummary}
                </strong>
              </div>
              <div className="continuous-metric">
                <span className="continuous-metric-label">最近同步成功</span>
                <strong
                  className="continuous-metric-value"
                  title={
                    effectiveStatus.lastSuccess > 0
                      ? formatTimestamp(effectiveStatus.lastSuccess, true)
                      : undefined
                  }
                >
                  {effectiveStatus.lastSuccess > 0
                    ? formatRelativeSyncTime(effectiveStatus.lastSuccess)
                    : "尚未同步"}
                </strong>
              </div>
              <div className="continuous-metric">
                <span className="continuous-metric-label">远端检测周期</span>
                <strong
                  className="continuous-metric-value"
                  title={
                    effectiveStatus.lastPoll > 0
                      ? `最近检测：${formatTimestamp(effectiveStatus.lastPoll, true)}`
                      : undefined
                  }
                >
                  每 {formatSecondsShort(meta.pollIntervalSec)}
                  {(effectiveStatus.pollBackoffStreak || 0) > 0 &&
                    (effectiveStatus.effectivePollSec || 0) > meta.pollIntervalSec &&
                    `（退避每 ${formatSecondsShort(effectiveStatus.effectivePollSec || meta.pollIntervalSec)}）`}
                </strong>
              </div>
              <div className="continuous-metric">
                <span className="continuous-metric-label">完整核对 / 静默</span>
                <strong className="continuous-metric-value">
                  {formatMinutesShort(meta.auditIntervalMin)} ·{" "}
                  {formatSecondsShort(meta.debounceSec)}
                </strong>
              </div>
              <div className="continuous-metric">
                <span className="continuous-metric-label">下一次动作</span>
                <strong className="continuous-metric-value" title={nextAction || undefined}>
                  {nextAction || (effectiveStatus.state === "paused" ? "暂停中" : "等待触发")}
                </strong>
              </div>
            </div>
          </div>
        )}

        <div className="overview-sections">
          <section className="info-section">
            <h3>同步规则与配置</h3>
            <Info label="同步方向">
              <span className="overview-engine">
                <span>{meta.directionLabel}</span>
                {meta.directionKey !== "bidirectional" && (
                  <span className="muted">
                    {oneWayTrackedDelete
                      ? "基线追踪删除（仅清理本任务同步过的文件）"
                      : meta.syncDelete
                        ? "同步删除目标端多余文件"
                        : "不删除目标端文件"}
                  </span>
                )}
              </span>
            </Info>
            <Info label="监控节奏">{formatContinuousSyncPlan(meta)}</Info>
            {weeklyEnabled && (
              <Info label="每周计划">
                <span>{formatWeeklyScheduleSummary(extraFlags.weeklySchedule)}</span>
                {effectiveStatus.scheduleLimited && (
                  <Tag size="small" type="light" color="orange" style={{ marginLeft: 8 }}>
                    当前时段限速 {effectiveStatus.effectiveBwLimit || effectiveStatus.weeklyLimitRate || extraFlags.weeklySchedule?.limitRate || ""}
                  </Tag>
                )}
                {effectiveStatus.schedulePaused && (
                  <Tag size="small" type="light" color="grey" style={{ marginLeft: 8 }}>
                    当前时段暂停
                  </Tag>
                )}
              </Info>
            )}
            {Boolean(job.windowEnable) && (
              <Info label="运行窗口">
                每日 {job.windowStart || "00:00"} – {job.windowEnd || "06:00"}
                {job.windowStopOutside ? "（超出窗口自动中止传输）" : "（窗口外暂挂，恢复后补齐）"}
              </Info>
            )}
            {(Number(job.compareMode || 0) !== 0 ||
              Number(job.conflictPolicy || 0) !== 0) && (
              <Info label="比对与冲突">
                {compareModeLabel(job.compareMode)} ·{" "}
                {conflictPolicyLabel(job.conflictPolicy)}
              </Info>
            )}
            {Boolean(job.trashEnable) && (
              <Info label={job.method === 3 ? "双端回收站" : "目标回收站"}>
                已开启（目录 {job.trashDir || ".opensync-trash"}
                {Number(job.trashRetentionDays ?? 30) > 0
                  ? `，保留 ${job.trashRetentionDays ?? 30} 天`
                  : "，永久保留"}
                ）
              </Info>
            )}
            {showPathMapping && (
              <Info label="目标映射">
                <div className="overview-path-mapping">
                  {pathPreview.items.map((item) => (
                    <div key={item.key}>
                      {formatDisplayPath(item.srcDir, displaySiblings)}{" "}
                      <IconArrowRight
                        size="extra-small"
                        aria-hidden="true"
                        style={{ verticalAlign: "middle", margin: "0 4px" }}
                      />{" "}
                      {formatDisplayPath(item.resolvedDst, displaySiblings)}
                    </div>
                  ))}
                </div>
              </Info>
            )}
            <Info label="过滤规则">
              {filterSummaries.length > 0 ? (
                <div className="overview-summary-tags">
                  {filterSummaries.map((text) => (
                    <Tag key={text} size="small" type="light" color="grey">
                      {text}
                    </Tag>
                  ))}
                </div>
              ) : (
                <span className="muted">未启用过滤</span>
              )}
            </Info>
            {throttleLabels.length > 0 && (
              <Info label="并发与调优">{throttleLabels.join(" · ")}</Info>
            )}
            {notifyBindingText && (
              <Info label="通知绑定">{notifyBindingText}</Info>
            )}
            <Info label="最近同步批次">
              <LatestExecution
                record={latestExecution.data?.dataList?.[0]}
                loading={latestExecution.loading}
                error={latestExecution.error}
              />
            </Info>
          </section>
        </div>
      </SectionCard>
    </div>
  );
}

function LatestExecution({
  record,
  loading,
  error,
}: {
  record?: TaskRecord;
  loading: boolean;
  error?: string;
}) {
  if (loading) return "加载中";
  if (error) return "加载失败";
  if (!record) return "暂无同步批次记录";

  const duration =
    record.runTime && record.createTime
      ? formatDuration(record.runTime - record.createTime)
      : "";

  return (
    <div className="latest-execution">
      <span>{formatTimestamp(record.createTime)}</span>
      {duration && <span>{duration}</span>}
      {record.allNum !== undefined && (
        <span className="muted">
          {record.successNum ?? 0}/{record.allNum} 项
          {(record.sumSize ?? 0) > 0 ? ` · ${formatSize(record.sumSize || 0)}` : ""}
        </span>
      )}
    </div>
  );
}
