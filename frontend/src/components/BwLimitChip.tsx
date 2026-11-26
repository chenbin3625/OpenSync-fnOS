import { useMemo } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Popover from "@douyinfe/semi-ui/lib/es/popover";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconChevronRight,
  IconClockStroked,
} from "@douyinfe/semi-icons";
import {
  describeBwLimit,
  formatBwRateHuman,
  formatBwTimelineSegLabel,
  formatHHMM,
  getDayTimelineSegments,
} from "../lib/bwLimit";

export interface BwLimitChipProps {
  expression?: string | null;
  now?: Date;
  onConfigure?: () => void;
}

export function BwLimitChip({
  expression,
  now,
  onConfigure,
}: BwLimitChipProps) {
  const currentNow = now ?? new Date();
  const info = useMemo(
    () => describeBwLimit(expression, { now: currentNow }),
    [expression, currentNow],
  );
  const todaySegments = useMemo(
    () =>
      info.hasSchedule
        ? getDayTimelineSegments(info.raw, currentNow.getDay())
        : [],
    [info.hasSchedule, info.raw, currentNow],
  );
  const distinctLimitedRates = useMemo(() => {
    const unique: string[] = [];
    for (const seg of todaySegments) {
      if (!seg.isUnlimited && !unique.includes(seg.rate)) {
        unique.push(seg.rate);
      }
    }
    return unique;
  }, [todaySegments]);

  const nowMin = currentNow.getHours() * 60 + currentNow.getMinutes();
  const nowPercent = (nowMin / 1440) * 100;

  const chipClass = [
    "dashboard-live-bw-chip",
    info.isCurrentlyLimited
      ? "is-limited"
      : info.hasSchedule
        ? "is-scheduled-idle"
        : "",
    onConfigure ? "is-interactive" : "",
  ]
    .filter(Boolean)
    .join(" ");

  const popoverContent = (
    <div className="bw-chip-popover">
      <div className="bw-chip-popover-head">
        <span className="bw-chip-popover-title">全局默认限速</span>
        <Tag
          size="small"
          type="light"
          color={info.isCurrentlyLimited ? "blue" : "green"}
        >
          {info.isCurrentlyLimited ? info.activeRateCompact : "当前不限速"}
        </Tag>
      </div>

      {!info.configured ? (
        <p className="bw-chip-popover-desc muted">
          未设置全局默认带宽上限，所有未单独配置限速的任务将以全速运行。
        </p>
      ) : !info.hasSchedule ? (
        <div className="bw-chip-popover-section">
          <div className="bw-chip-popover-kv">
            <span className="muted">生效规则</span>
            <strong>{info.activeRateHuman}</strong>
          </div>
          <p className="bw-chip-popover-desc muted">
            对所有未单独设置限速的同步任务、校验与恢复操作生效。
          </p>
        </div>
      ) : (
        <div className="bw-chip-popover-section">
          <div className="bw-chip-popover-kv">
            <span className="muted">当前生效</span>
            <strong>{info.activeRateHuman}</strong>
          </div>
          {info.nextChangeLabel && (
            <div className="bw-chip-popover-kv">
              <span className="muted">下次切换</span>
              <span className="bw-chip-popover-next">{info.nextChangeLabel}</span>
            </div>
          )}

          {info.rules.length > 0 && (
            <div className="bw-chip-popover-rules">
              {info.rules.map((rule, idx) => (
                <div
                  key={`${rule.daysLabel}-${rule.timeRange}-${idx}`}
                  className="bw-chip-popover-rule-row"
                >
                  <span className="bw-chip-popover-rule-time">
                    {rule.daysLabel} {rule.timeRange}
                    {rule.crossMidnight ? "（跨日）" : ""}
                  </span>
                  <span className="bw-chip-popover-rule-rate">
                    {rule.rateCompact}
                  </span>
                </div>
              ))}
              {info.defaultRateHuman && (
                <div className="bw-chip-popover-rule-row is-default">
                  <span className="muted">其余时段</span>
                  <span className="muted">{info.defaultRateHuman}</span>
                </div>
              )}
            </div>
          )}

          {info.isRawFallback && (
            <code className="bw-chip-popover-raw">{info.raw}</code>
          )}

          {todaySegments.length > 0 && (
            <div className="bw-chip-popover-timeline">
              <div className="bw-chip-popover-timeline-caption muted">
                <span>今日 24 小时限速分布</span>
                <span>当前 {formatHHMM(nowMin)}</span>
              </div>
              <div className="bw-timeline-track-wrap">
                <div
                  className="bw-timeline-bar bw-timeline-bar--compact"
                  aria-label="今日 24 小时带宽限速分布"
                >
                  {todaySegments.map((seg, idx) => {
                    const widthPct = ((seg.endMin - seg.startMin) / 1440) * 100;
                    const tier = seg.isUnlimited
                      ? -1
                      : distinctLimitedRates.indexOf(seg.rate) % 3;
                    const rangeLabel = `${formatHHMM(seg.startMin)}–${seg.endMin === 1440 ? "24:00" : formatHHMM(seg.endMin)}`;
                    const rateLabel = formatBwRateHuman(seg.rate);
                    return (
                      <Tooltip
                        key={`${seg.startMin}-${seg.endMin}-${idx}`}
                        content={`${rangeLabel} · ${rateLabel}`}
                      >
                        <div
                          className={`bw-timeline-seg ${seg.isUnlimited ? "is-unlimited" : `is-limited is-tier-${tier}`}`}
                          style={{ width: `${widthPct}%` }}
                        >
                          {widthPct >= 18 && (
                            <span className="bw-timeline-seg-text">
                              {formatBwTimelineSegLabel(seg.rate)}
                            </span>
                          )}
                        </div>
                      </Tooltip>
                    );
                  })}
                  <div
                    className="bw-timeline-now"
                    style={{ left: `${nowPercent}%` }}
                    aria-hidden="true"
                  />
                </div>
                <div className="bw-timeline-ticks" aria-hidden="true">
                  <span>00:00</span>
                  <span>06:00</span>
                  <span>12:00</span>
                  <span>18:00</span>
                  <span>24:00</span>
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      {onConfigure && (
        <div className="bw-chip-popover-foot">
          <Button
            theme="borderless"
            type="primary"
            size="small"
            className="bw-chip-popover-action"
            onClick={onConfigure}
          >
            <span>{info.configured ? "前往系统设置调整" : "前往系统设置配置"}</span>
            <IconChevronRight size="small" aria-hidden="true" />
          </Button>
        </div>
      )}
    </div>
  );

  return (
    <Popover
      content={popoverContent}
      trigger="hover"
      position="bottomRight"
      showArrow
      className="bw-chip-popover-shell"
    >
      <button
        type="button"
        className={chipClass}
        onClick={onConfigure}
        aria-label={info.tooltipSummary}
      >
        {info.hasSchedule ? (
          <IconClockStroked
            size="small"
            className="dashboard-live-bw-icon"
            aria-hidden="true"
          />
        ) : (
          <span
            className={`kpi-dot ${
              info.isCurrentlyLimited ? "kpi-dot--primary" : ""
            }`}
            aria-hidden="true"
          />
        )}
        <span className="dashboard-live-bw-text">{info.chipLabel}</span>
      </button>
    </Popover>
  );
}
