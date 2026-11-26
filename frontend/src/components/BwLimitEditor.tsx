import { useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Tooltip from "@douyinfe/semi-ui/lib/es/tooltip";
import {
  IconChevronRight,
  IconDeleteStroked,
  IconPlus,
} from "@douyinfe/semi-icons";
import { HelpTip } from "./common";
import {
  bwFixedPresets,
  bwSchedulePresets,
  bwSpeedUnits,
  bwWeekdayOptions,
  createBwRateValue,
  createDefaultScheduleRule,
  daysForPreset,
  detectBwModelIssues,
  estimateMbps,
  formatBwLimitSummary,
  formatBwRateHuman,
  formatBwTimelineSegLabel,
  formatHHMM,
  formatNextChangeTime,
  getDayPreset,
  getDayTimelineSegments,
  hasDifferentWeekdays,
  normalizeDays,
  parseBwLimit,
  parseHHMM,
  resolveBwLimitAt,
  serializeBwLimit,
  serializeRateValue,
  validateBwLimitExpression,
  type BwDayPreset,
  type BwLimitModel,
  type BwMode,
  type BwRateValue,
  type BwScheduleRule,
  type BwSpeedUnit,
} from "../lib/bwLimit";

export interface BwLimitEditorProps {
  value: string;
  onChange: (nextValue: string) => void;
  /** True in TaskEditor (adds "跟随全局" and uses "off" for explicit unlimited). */
  allowInherit?: boolean;
  /** Current global default expression, displayed when allowInherit && mode === "inherit". */
  globalDefault?: string;
  disabled?: boolean;
  compact?: boolean;
  onValidityChange?: (error: string | null) => void;
}

const MAX_SCHEDULE_RULES = 12;

const dayPresetOptions: Array<{ value: BwDayPreset; label: string }> = [
  { value: "daily", label: "每天" },
  { value: "weekdays", label: "工作日" },
  { value: "weekend", label: "周末" },
  { value: "custom", label: "自定义" },
];

function sanitizeSpeedInput(raw: string): string {
  // Allow positive integer or decimal input while typing.
  const cleaned = raw.replace(/[^0-9.]/g, "");
  const firstDot = cleaned.indexOf(".");
  if (firstDot < 0) return cleaned;
  return (
    cleaned.slice(0, firstDot + 1) +
    cleaned.slice(firstDot + 1).replace(/\./g, "")
  );
}

function SpeedInputPair({
  value,
  unit,
  disabled,
  ariaLabel,
  onValueChange,
  onUnitChange,
}: {
  value: string;
  unit: BwSpeedUnit;
  disabled?: boolean;
  ariaLabel: string;
  onValueChange: (next: string) => void;
  onUnitChange: (next: BwSpeedUnit) => void;
}) {
  const mbps = estimateMbps(value, unit);
  return (
    <div className="bw-speed-pair">
      <Input
        aria-label={ariaLabel}
        inputMode="decimal"
        className="bw-speed-number"
        value={value}
        placeholder="如 10"
        disabled={disabled}
        onChange={(v) => onValueChange(sanitizeSpeedInput(v))}
      />
      <Select
        aria-label={`${ariaLabel}单位`}
        className="bw-speed-unit"
        value={unit}
        disabled={disabled}
        optionList={bwSpeedUnits}
        onChange={(v) => onUnitChange(v as BwSpeedUnit)}
      />
      {mbps && <span className="bw-mbps-hint">{mbps}</span>}
    </div>
  );
}

function RateEditorInline({
  rate,
  allowUnlimited,
  disabled,
  labelPrefix,
  showAsymmetricToggle = true,
  onChange,
}: {
  rate: BwRateValue;
  allowUnlimited: boolean;
  disabled?: boolean;
  labelPrefix: string;
  showAsymmetricToggle?: boolean;
  onChange: (next: BwRateValue) => void;
}) {
  const update = (patch: Partial<BwRateValue>) => onChange({ ...rate, ...patch });

  if (allowUnlimited && rate.unlimited) {
    return (
      <div className="bw-rate-inline">
        <Select
          aria-label={`${labelPrefix}策略`}
          className="bw-rate-kind"
          value="unlimited"
          disabled={disabled}
          optionList={[
            { value: "unlimited", label: "不限速" },
            { value: "limited", label: "限速" },
          ]}
          onChange={(v) => {
            if (v === "limited") {
              update({ unlimited: false });
            }
          }}
        />
        <span className="muted bw-inline-note">全速传输</span>
      </div>
    );
  }

  return (
    <div className="bw-rate-stack">
      <div className="bw-rate-inline">
        {allowUnlimited && (
          <Select
            aria-label={`${labelPrefix}策略`}
            className="bw-rate-kind"
            value="limited"
            disabled={disabled}
            optionList={[
              { value: "unlimited", label: "不限速" },
              { value: "limited", label: "限速" },
            ]}
            onChange={(v) => {
              if (v === "unlimited") {
                update({ unlimited: true });
              }
            }}
          />
        )}
        {!rate.asymmetric ? (
          <>
            <SpeedInputPair
              ariaLabel={`${labelPrefix}速率`}
              value={rate.value}
              unit={rate.unit}
              disabled={disabled}
              onValueChange={(value) => update({ value })}
              onUnitChange={(unit) => update({ unit })}
            />
            {showAsymmetricToggle && (
              <button
                type="button"
                className="bw-link-toggle"
                disabled={disabled}
                onClick={() =>
                  update({
                    asymmetric: true,
                    uploadUnlimited: false,
                    uploadValue: rate.value || "5",
                    uploadUnit: rate.unit,
                    downloadUnlimited: false,
                    downloadValue: rate.value || "10",
                    downloadUnit: rate.unit,
                  })
                }
              >
                分上下行
              </button>
            )}
          </>
        ) : (
          showAsymmetricToggle && (
            <button
              type="button"
              className="bw-link-toggle"
              disabled={disabled}
              onClick={() =>
                update({
                  asymmetric: false,
                  value: !rate.uploadUnlimited
                    ? rate.uploadValue || "10"
                    : rate.downloadValue || "10",
                  unit: !rate.uploadUnlimited ? rate.uploadUnit : rate.downloadUnit,
                })
              }
            >
              合并上传下载
            </button>
          )
        )}
      </div>

      {rate.asymmetric && (
        <div className="bw-asym-grid">
          <div className="bw-asym-row">
            <span className="bw-asym-label">上传上限</span>
            <Select
              aria-label={`${labelPrefix}上传策略`}
              className="bw-rate-kind"
              value={rate.uploadUnlimited ? "unlimited" : "limited"}
              disabled={disabled}
              optionList={[
                { value: "limited", label: "限速" },
                { value: "unlimited", label: "不限" },
              ]}
              onChange={(v) => update({ uploadUnlimited: v === "unlimited" })}
            />
            {!rate.uploadUnlimited && (
              <SpeedInputPair
                ariaLabel={`${labelPrefix}上传速率`}
                value={rate.uploadValue}
                unit={rate.uploadUnit}
                disabled={disabled}
                onValueChange={(uploadValue) => update({ uploadValue })}
                onUnitChange={(uploadUnit) => update({ uploadUnit })}
              />
            )}
          </div>
          <div className="bw-asym-row">
            <span className="bw-asym-label">下载上限</span>
            <Select
              aria-label={`${labelPrefix}下载策略`}
              className="bw-rate-kind"
              value={rate.downloadUnlimited ? "unlimited" : "limited"}
              disabled={disabled}
              optionList={[
                { value: "limited", label: "限速" },
                { value: "unlimited", label: "不限" },
              ]}
              onChange={(v) => update({ downloadUnlimited: v === "unlimited" })}
            />
            {!rate.downloadUnlimited && (
              <SpeedInputPair
                ariaLabel={`${labelPrefix}下载速率`}
                value={rate.downloadValue}
                unit={rate.downloadUnit}
                disabled={disabled}
                onValueChange={(downloadValue) => update({ downloadValue })}
                onUnitChange={(downloadUnit) => update({ downloadUnit })}
              />
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function TimelinePreview({
  expression,
  now,
}: {
  expression: string;
  now: Date;
}) {
  const today = now.getDay();
  const [selectedDay, setSelectedDay] = useState<number>(today);
  const differsByDay = useMemo(() => hasDifferentWeekdays(expression), [expression]);
  const activeDay = differsByDay ? selectedDay : today;
  const resolved = useMemo(() => resolveBwLimitAt(expression, now), [expression, now]);
  const segments = useMemo(
    () => getDayTimelineSegments(expression, activeDay),
    [expression, activeDay],
  );
  const nowMin = now.getHours() * 60 + now.getMinutes();
  const nowPercent = (nowMin / 1440) * 100;

  // Assign distinct visual tiers for multiple different limited rates in the same day.
  const distinctLimitedRates = useMemo(() => {
    const unique: string[] = [];
    for (const seg of segments) {
      if (!seg.isUnlimited && !unique.includes(seg.rate)) {
        unique.push(seg.rate);
      }
    }
    return unique;
  }, [segments]);

  return (
    <div className="bw-timeline-card">
      <div className="bw-timeline-head">
        <div className="bw-timeline-status">
          <span className="bw-timeline-label">当前生效</span>
          <Tag
            size="small"
            color={resolved.isUnlimited ? "green" : "blue"}
            type="light"
          >
            {formatBwRateHuman(resolved.activeRate)}
          </Tag>
        </div>
        {resolved.nextChangeAt && resolved.nextRate && (
          <span className="bw-timeline-next muted">
            下次切换：{formatNextChangeTime(resolved.nextChangeAt, now)} →{" "}
            {formatBwRateHuman(resolved.nextRate)}
          </span>
        )}
      </div>

      {differsByDay && (
        <div className="bw-timeline-days" role="tablist" aria-label="按星期预览限速">
          {bwWeekdayOptions.map((wd) => (
            <button
              key={wd.value}
              type="button"
              role="tab"
              aria-selected={activeDay === wd.value}
              className={`bw-day-tab${activeDay === wd.value ? " is-active" : ""}${wd.value === today ? " is-today" : ""}`}
              onClick={() => setSelectedDay(wd.value)}
            >
              {wd.label}
              {wd.value === today ? "（今）" : ""}
            </button>
          ))}
        </div>
      )}

      <div className="bw-timeline-track-wrap">
        <div className="bw-timeline-bar" aria-label="24 小时带宽限速分布">
          {segments.map((seg, idx) => {
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
                  {widthPct >= 14 && (
                    <span className="bw-timeline-seg-text">
                      {formatBwTimelineSegLabel(seg.rate)}
                    </span>
                  )}
                </div>
              </Tooltip>
            );
          })}
          {activeDay === today && (
            <Tooltip content={`当前时间 ${formatHHMM(nowMin)}`}>
              <div
                className="bw-timeline-now"
                style={{ left: `${nowPercent}%` }}
                aria-hidden="true"
              />
            </Tooltip>
          )}
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
  );
}

export function BwLimitEditor({
  value,
  onChange,
  allowInherit = false,
  globalDefault = "",
  disabled = false,
  compact = false,
  onValidityChange,
}: BwLimitEditorProps) {
  const [model, setModel] = useState<BwLimitModel>(() =>
    parseBwLimit(value, { allowInherit }),
  );
  const [rawOpen, setRawOpen] = useState<boolean>(() => model.mode === "raw");
  const [rawDraft, setRawDraft] = useState<string>(value.trim());
  const [rawDraftError, setRawDraftError] = useState<string | null>(null);
  const [nowTick, setNowTick] = useState<Date>(() => new Date());

  const lastEmittedRef = useRef<string | null>(value);
  const onValidityRef = useRef(onValidityChange);
  onValidityRef.current = onValidityChange;

  // Keep local visual model synced when value changes externally (e.g. page load/reset).
  useEffect(() => {
    if (value === lastEmittedRef.current) return;
    lastEmittedRef.current = value;
    const parsed = parseBwLimit(value, { allowInherit });
    setModel(parsed);
    setRawDraft(value.trim());
    setRawDraftError(null);
    if (parsed.mode === "raw") {
      setRawOpen(true);
    }
  }, [value, allowInherit]);

  // Refresh the "current active rate" indicator once a minute while schedule preview is visible.
  useEffect(() => {
    if (model.mode !== "schedule" && model.mode !== "raw") return;
    const timer = window.setInterval(() => setNowTick(new Date()), 60_000);
    return () => window.clearInterval(timer);
  }, [model.mode]);

  const issues = useMemo(
    () => detectBwModelIssues(model, { allowInherit }),
    [model, allowInherit],
  );

  const compiledExpression = useMemo(
    () => serializeBwLimit(model, { allowInherit }),
    [model, allowInherit],
  );

  useEffect(() => {
    onValidityRef.current?.(issues.errors[0] ?? null);
  }, [issues.errors]);

  const commitModel = (next: BwLimitModel) => {
    setModel(next);
    const serialized = serializeBwLimit(next, { allowInherit });
    if (serialized !== null) {
      lastEmittedRef.current = serialized;
      setRawDraft(serialized);
      setRawDraftError(null);
      onChange(serialized);
    }
  };

  const switchMode = (nextMode: BwMode) => {
    if (disabled || nextMode === model.mode) return;
    const next: BwLimitModel = {
      ...model,
      mode: nextMode,
      rules:
        nextMode === "schedule" && model.rules.length === 0
          ? [createDefaultScheduleRule()]
          : model.rules,
    };
    commitModel(next);
  };

  const applySchedulePreset = (expression: string) => {
    if (disabled) return;
    const parsed = parseBwLimit(expression, { allowInherit });
    commitModel({
      ...model,
      mode: "schedule",
      defaultRate: parsed.defaultRate,
      rules: parsed.rules,
      raw: expression,
    });
  };

  const updateRule = (id: string, updater: (rule: BwScheduleRule) => BwScheduleRule) => {
    commitModel({
      ...model,
      rules: model.rules.map((r) => (r.id === id ? updater(r) : r)),
    });
  };

  const addRule = () => {
    if (disabled || model.rules.length >= MAX_SCHEDULE_RULES) return;
    // Choose a sensible next time slot based on existing rules.
    const existingStarts = new Set(model.rules.map((r) => r.startTime));
    let startTime = "19:00";
    let endTime = "23:00";
    let rateVal = "2";
    if (existingStarts.has("19:00")) {
      startTime = "00:00";
      endTime = "08:00";
      rateVal = "20";
    } else if (!existingStarts.has("08:00")) {
      startTime = "08:00";
      endTime = "23:00";
      rateVal = "5";
    }
    commitModel({
      ...model,
      rules: [
        ...model.rules,
        createDefaultScheduleRule({
          startTime,
          endTime,
          rate: createBwRateValue({ value: rateVal, unit: "M" }),
        }),
      ],
    });
  };

  const removeRule = (id: string) => {
    if (disabled || model.rules.length <= 1) return;
    commitModel({
      ...model,
      rules: model.rules.filter((r) => r.id !== id),
    });
  };

  const applyRawDraft = () => {
    const trimmed = rawDraft.trim();
    const err = validateBwLimitExpression(trimmed);
    if (err) {
      setRawDraftError(err);
      return;
    }
    setRawDraftError(null);
    lastEmittedRef.current = trimmed;
    const parsed = parseBwLimit(trimmed, { allowInherit });
    setModel(parsed);
    onChange(trimmed);
  };

  const modes: Array<{ value: BwMode; label: string }> = [
    ...(allowInherit ? [{ value: "inherit" as const, label: "跟随全局" }] : []),
    { value: "unlimited", label: "不限速" },
    { value: "fixed", label: "固定限速" },
    { value: "schedule", label: "定时限速" },
    ...(model.mode === "raw"
      ? [{ value: "raw" as const, label: "高级表达式" }]
      : []),
  ];

  const previewExpression =
    compiledExpression !== null ? compiledExpression : model.raw;
  const globalSummary = formatBwLimitSummary(globalDefault, {
    allowInherit: false,
    now: nowTick,
  });

  return (
    <div className={`bw-editor${compact ? " is-compact" : ""}`}>
      <div
        className="bw-mode-bar"
        role="radiogroup"
        aria-label="带宽限制模式"
      >
        {modes.map((m) => (
          <button
            key={m.value}
            type="button"
            role="radio"
            aria-checked={model.mode === m.value}
            disabled={disabled}
            className={`bw-mode-btn${model.mode === m.value ? " is-active" : ""}`}
            onClick={() => switchMode(m.value)}
          >
            {m.label}
          </button>
        ))}
      </div>

      {model.mode === "inherit" && (
        <div className="bw-info-panel">
          <div className="bw-info-row">
            <span>跟随系统「全局默认带宽限制」</span>
            <Tag
              size="small"
              color={globalDefault.trim() ? "blue" : "green"}
              type="light"
            >
              当前全局：{globalSummary}
            </Tag>
          </div>
          {globalDefault.trim() &&
            resolveBwLimitAt(globalDefault, nowTick).hasSchedule && (
              <TimelinePreview expression={globalDefault} now={nowTick} />
            )}
        </div>
      )}

      {model.mode === "unlimited" && (
        <div className="bw-info-panel muted">
          {allowInherit
            ? "此任务强制不限速，将忽略系统设置中的全局默认带宽限制。"
            : "默认不限制传输带宽，所有未单独设置限速的任务将以全速运行。"}
        </div>
      )}

      {model.mode === "fixed" && (
        <div className="bw-panel">
          <RateEditorInline
            rate={model.fixed}
            allowUnlimited={false}
            disabled={disabled}
            labelPrefix="固定限速"
            showAsymmetricToggle={false}
            onChange={(fixed) => commitModel({ ...model, fixed })}
          />

          {!model.fixed.asymmetric && (
            <div className="bw-preset-row">
              <span className="bw-preset-label">常用上限</span>
              <div className="schedule-chips">
                {bwFixedPresets.map((preset) => {
                  const active =
                    !model.fixed.unlimited &&
                    model.fixed.value.trim() === preset.value &&
                    model.fixed.unit === preset.unit;
                  return (
                    <button
                      key={preset.label}
                      type="button"
                      disabled={disabled}
                      className={`schedule-chip${active ? " is-active" : ""}`}
                      onClick={() =>
                        commitModel({
                          ...model,
                          fixed: {
                            ...model.fixed,
                            unlimited: false,
                            value: preset.value,
                            unit: preset.unit,
                          },
                        })
                      }
                    >
                      {preset.label}
                    </button>
                  );
                })}
              </div>
            </div>
          )}

          <div className="bw-fixed-footer">
            <Checkbox
              checked={model.fixed.asymmetric}
              disabled={disabled}
              onChange={(e) => {
                const asymmetric = Boolean(e.target.checked);
                commitModel({
                  ...model,
                  fixed: asymmetric
                    ? {
                        ...model.fixed,
                        asymmetric: true,
                        uploadUnlimited: false,
                        uploadValue: model.fixed.value || "5",
                        uploadUnit: model.fixed.unit,
                        downloadUnlimited: false,
                        downloadValue: model.fixed.value || "20",
                        downloadUnit: model.fixed.unit,
                      }
                    : {
                        ...model.fixed,
                        asymmetric: false,
                        value: !model.fixed.uploadUnlimited
                          ? model.fixed.uploadValue || "10"
                          : model.fixed.downloadValue || "10",
                        unit: !model.fixed.uploadUnlimited
                          ? model.fixed.uploadUnit
                          : model.fixed.downloadUnit,
                      },
                });
              }}
            >
              分别限制上传与下载速度（rclone 上传:下载 双通道限速）
            </Checkbox>
          </div>
        </div>
      )}

      {model.mode === "schedule" && (
        <div className="bw-panel bw-schedule-panel">
          <div className="bw-preset-row">
            <span className="bw-preset-label">快捷方案</span>
            <div className="schedule-chips">
              {bwSchedulePresets.map((preset) => {
                const active = previewExpression.trim() === preset.expression;
                return (
                  <Tooltip key={preset.id} content={preset.description}>
                    <button
                      type="button"
                      disabled={disabled}
                      className={`schedule-chip${active ? " is-active" : ""}`}
                      onClick={() => applySchedulePreset(preset.expression)}
                    >
                      {preset.label}
                    </button>
                  </Tooltip>
                );
              })}
            </div>
          </div>

          <div className="bw-rule-list">
            {model.rules.map((rule, index) => {
              const preset = getDayPreset(rule.days);
              const startMin = parseHHMM(rule.startTime);
              const endMin = parseHHMM(rule.endTime);
              const crossMidnight =
                startMin !== null && endMin !== null && endMin < startMin;
              return (
                <div key={rule.id} className="bw-rule-card">
                  <div className="bw-rule-top">
                    <div className="bw-rule-time-group">
                      <span className="bw-rule-index">#{index + 1}</span>
                      <Select
                        aria-label={`时段 ${index + 1} 生效周期`}
                        className="bw-day-preset-select"
                        value={preset}
                        disabled={disabled}
                        optionList={dayPresetOptions}
                        onChange={(v) => {
                          const nextPreset = v as BwDayPreset;
                          if (nextPreset === "custom") {
                            // Keep current days, or switch off full-week/weekday/weekend marker by keeping Monday if daily.
                            if (preset !== "custom") {
                              updateRule(rule.id, (r) => ({
                                ...r,
                                days: [1, 2, 3, 4, 5, 6],
                              }));
                            }
                            return;
                          }
                          updateRule(rule.id, (r) => ({
                            ...r,
                            days: daysForPreset(nextPreset),
                          }));
                        }}
                      />
                      <div className="bw-time-range">
                        <input
                          type="time"
                          aria-label={`时段 ${index + 1} 开始时间`}
                          className="bw-time-input"
                          value={rule.startTime}
                          disabled={disabled}
                          onChange={(e) =>
                            updateRule(rule.id, (r) => ({
                              ...r,
                              startTime: e.target.value,
                            }))
                          }
                        />
                        <span className="bw-time-sep">至</span>
                        <input
                          type="time"
                          aria-label={`时段 ${index + 1} 结束时间`}
                          className="bw-time-input"
                          value={rule.endTime}
                          disabled={disabled}
                          onChange={(e) =>
                            updateRule(rule.id, (r) => ({
                              ...r,
                              endTime: e.target.value,
                            }))
                          }
                        />
                      </div>
                      {crossMidnight && (
                        <Tag size="small" color="cyan" type="light">
                          跨午夜至次日
                        </Tag>
                      )}
                    </div>
                    {model.rules.length > 1 && (
                      <Button
                        type="danger"
                        theme="borderless"
                        size="small"
                        aria-label={`删除时段 ${index + 1}`}
                        icon={<IconDeleteStroked aria-hidden="true" />}
                        disabled={disabled}
                        onClick={() => removeRule(rule.id)}
                      />
                    )}
                  </div>

                  {preset === "custom" && (
                    <div
                      className="weekday-picker bw-rule-weekdays"
                      role="group"
                      aria-label={`时段 ${index + 1} 星期选择`}
                    >
                      {bwWeekdayOptions.map((wd) => {
                        const checked = rule.days.includes(wd.value);
                        return (
                          <button
                            key={wd.value}
                            type="button"
                            disabled={disabled}
                            aria-pressed={checked}
                            className={`weekday-chip${checked ? " is-active" : ""}`}
                            onClick={() => {
                              updateRule(rule.id, (r) => {
                                const nextDays = checked
                                  ? r.days.filter((d) => d !== wd.value)
                                  : normalizeDays([...r.days, wd.value]);
                                // Prevent emptying all days unless user explicitly wants to toggle another first
                                return nextDays.length > 0
                                  ? { ...r, days: nextDays }
                                  : r;
                              });
                            }}
                          >
                            周{wd.short}
                          </button>
                        );
                      })}
                    </div>
                  )}

                  <div className="bw-rule-rate-row">
                    <span className="bw-rule-rate-label">该时段</span>
                    <RateEditorInline
                      rate={rule.rate}
                      allowUnlimited
                      disabled={disabled}
                      labelPrefix={`时段 ${index + 1}`}
                      onChange={(rate) =>
                        updateRule(rule.id, (r) => ({ ...r, rate }))
                      }
                    />
                  </div>
                </div>
              );
            })}
          </div>

          <div className="bw-schedule-actions">
            <Button
              size="small"
              type="tertiary"
              icon={<IconPlus aria-hidden="true" />}
              disabled={disabled || model.rules.length >= MAX_SCHEDULE_RULES}
              onClick={addRule}
            >
              添加限速时段
            </Button>
            <span className="muted bw-rule-count">
              {model.rules.length}/{MAX_SCHEDULE_RULES} 个时段
            </span>
          </div>

          <div className="bw-default-rate-row">
            <span className="bw-default-rate-label">
              其余时段默认
              <HelpTip
                label="其余时段默认说明"
                content="不在上方任何限速时段内的时间（例如夜间闲时或周末）将按此速率运行。"
              />
            </span>
            <RateEditorInline
              rate={model.defaultRate}
              allowUnlimited
              disabled={disabled}
              labelPrefix="其余时段默认"
              onChange={(defaultRate) => commitModel({ ...model, defaultRate })}
            />
          </div>

          {issues.errors.length === 0 && (
            <TimelinePreview expression={previewExpression} now={nowTick} />
          )}
        </div>
      )}

      {model.mode === "raw" && (
        <div className="bw-panel">
          <div className="bw-raw-notice muted">
            当前规则为高级自定义 rclone 表达式，已原样保留；可直接编辑，或点击上方模式切换为可视化配置。
          </div>
          {validateBwLimitExpression(model.raw) === null && model.raw.trim() && (
            <TimelinePreview expression={model.raw} now={nowTick} />
          )}
        </div>
      )}

      {issues.errors.length > 0 && (
        <div className="bw-feedback is-error" role="alert">
          {issues.errors[0]}
        </div>
      )}
      {issues.errors.length === 0 && issues.warnings.length > 0 && (
        <div className="bw-feedback is-warning" role="status">
          {issues.warnings[0]}
        </div>
      )}

      <div className="bw-advanced">
        <button
          type="button"
          className={`bw-advanced-toggle${rawOpen ? " is-open" : ""}`}
          aria-expanded={rawOpen}
          onClick={() => setRawOpen((o) => !o)}
        >
          <IconChevronRight className="bw-advanced-chevron" aria-hidden="true" />
          <span>高级：rclone --bwlimit 表达式</span>
          <code className="bw-expr-pill">
            {previewExpression.trim()
              ? previewExpression.trim()
              : allowInherit
                ? "跟随全局"
                : "不限速"}
          </code>
        </button>
        {rawOpen && (
          <div className="bw-advanced-body">
            <div className="bw-raw-row">
              <Input
                aria-label="原始 rclone --bwlimit 表达式"
                className="mono"
                value={rawDraft}
                placeholder="如 10M、5M:20M、08:00,5M 23:00,off（留空清空）"
                showClear
                disabled={disabled}
                onChange={(v) => {
                  setRawDraft(v);
                  setRawDraftError(validateBwLimitExpression(v));
                  if (model.mode === "raw") {
                    const nextModel: BwLimitModel = { ...model, raw: v };
                    setModel(nextModel);
                    if (validateBwLimitExpression(v) === null) {
                      lastEmittedRef.current = v.trim();
                      onChange(v.trim());
                    }
                  }
                }}
                onEnterPress={applyRawDraft}
              />
              <Button
                size="small"
                type="tertiary"
                disabled={disabled || Boolean(rawDraftError)}
                onClick={applyRawDraft}
              >
                应用
              </Button>
            </div>
            {rawDraftError ? (
              <div className="bw-feedback is-error" role="alert">
                {rawDraftError}
              </div>
            ) : (
              <div className="bw-raw-hint muted">
                支持单速（10M、500K，单位为字节/秒）、上下行独立（上传:下载，如 5M:20M）与分时段规则（如 08:00,5M 23:00,off 或 Mon-09:00,5M Mon-18:00,off）。
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
