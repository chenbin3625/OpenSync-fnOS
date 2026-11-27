import { useEffect, useState, type KeyboardEvent } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import type { WeeklyScheduleConfig, WeeklySlotMode } from "../types";
import {
  applyWeeklyScheduleTemplate,
  cloneWeeklySchedule,
  countWeeklyScheduleSlots,
  createDefaultWeeklySchedule,
  cycleWeeklySlotMode,
  formatWeeklyScheduleSummary,
  hasLimitedWeeklySlots,
  validateWeeklySchedule,
  WEEKLY_DAY_LABELS,
  WEEKLY_DISPLAY_DAYS,
  WEEKLY_HOUR_COUNT,
  WEEKLY_LIMIT_RATE_PRESETS,
  WEEKLY_SCHEDULE_TEMPLATES,
  WEEKLY_SLOT_LABELS,
} from "../lib/weeklySchedule";

type BrushMode = "smart" | WeeklySlotMode;

const BRUSH_OPTIONS: Array<{ value: BrushMode; label: string }> = [
  { value: "smart", label: "点击切换三态" },
  { value: "full", label: "涂抹：全速" },
  { value: "limited", label: "涂抹：限速" },
  { value: "pause", label: "涂抹：暂停" },
];

export function WeeklyScheduleGrid({
  value,
  onChange,
  error,
}: {
  value?: WeeklyScheduleConfig;
  onChange: (next: WeeklyScheduleConfig) => void;
  error?: string;
}) {
  const schedule = value ?? createDefaultWeeklySchedule(false);
  const [brush, setBrush] = useState<BrushMode>("smart");
  const [dragMode, setDragMode] = useState<WeeklySlotMode | null>(null);

  useEffect(() => {
    if (!dragMode) return;
    const stopDrag = () => setDragMode(null);
    window.addEventListener("pointerup", stopDrag);
    window.addEventListener("pointercancel", stopDrag);
    return () => {
      window.removeEventListener("pointerup", stopDrag);
      window.removeEventListener("pointercancel", stopDrag);
    };
  }, [dragMode]);

  const commit = (next: WeeklyScheduleConfig) => {
    onChange(cloneWeeklySchedule(next));
  };

  const setEnabled = (enabled: boolean) => {
    commit({
      ...schedule,
      enabled,
    });
  };

  const applyCell = (dayIdx: number, hourIdx: number, mode: WeeklySlotMode) => {
    const next = cloneWeeklySchedule(schedule);
    next.days[dayIdx][hourIdx] = mode;
    if (mode === "limited" && !next.limitRate.trim()) {
      next.limitRate = "5M";
    }
    onChange(next);
  };

  const handleCellPointerDown = (dayIdx: number, hourIdx: number) => {
    const current = schedule.days[dayIdx]?.[hourIdx] ?? "full";
    const targetMode: WeeklySlotMode =
      brush === "smart" ? cycleWeeklySlotMode(current) : brush;
    setDragMode(targetMode);
    applyCell(dayIdx, hourIdx, targetMode);
  };

  const handleCellPointerEnter = (dayIdx: number, hourIdx: number) => {
    if (!dragMode) return;
    const current = schedule.days[dayIdx]?.[hourIdx] ?? "full";
    if (current === dragMode) return;
    applyCell(dayIdx, hourIdx, dragMode);
  };

  const handleCellKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    dayIdx: number,
    hourIdx: number,
  ) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    const current = schedule.days[dayIdx]?.[hourIdx] ?? "full";
    const targetMode: WeeklySlotMode =
      brush === "smart" ? cycleWeeklySlotMode(current) : brush;
    applyCell(dayIdx, hourIdx, targetMode);
  };

  const toggleDayRow = (dayIdx: number) => {
    const row = schedule.days[dayIdx] ?? [];
    const first = row[0] ?? "full";
    const allSame = row.every((cell) => cell === first);
    const targetMode: WeeklySlotMode =
      brush === "smart"
        ? allSame
          ? cycleWeeklySlotMode(first)
          : "full"
        : brush;
    const next = cloneWeeklySchedule(schedule);
    next.days[dayIdx] = Array.from(
      { length: WEEKLY_HOUR_COUNT },
      () => targetMode,
    );
    onChange(next);
  };

  const toggleHourColumn = (hourIdx: number) => {
    const first = schedule.days[WEEKLY_DISPLAY_DAYS[0]]?.[hourIdx] ?? "full";
    const allSame = WEEKLY_DISPLAY_DAYS.every(
      (dayIdx) => schedule.days[dayIdx]?.[hourIdx] === first,
    );
    const targetMode: WeeklySlotMode =
      brush === "smart"
        ? allSame
          ? cycleWeeklySlotMode(first)
          : "full"
        : brush;
    const next = cloneWeeklySchedule(schedule);
    for (let d = 0; d < 7; d++) {
      next.days[d][hourIdx] = targetMode;
    }
    onChange(next);
  };

  const counts = countWeeklyScheduleSlots(schedule);
  const limitedActive = hasLimitedWeeklySlots(schedule);
  const effectiveError = error || validateWeeklySchedule(schedule);

  return (
    <div className="weekly-schedule-panel">
      <div
        className="weekly-mode-switch"
        role="radiogroup"
        aria-label="同步运行计划模式"
      >
        <button
          type="button"
          role="radio"
          aria-checked={!schedule.enabled}
          className={`weekly-mode-option${!schedule.enabled ? " is-selected" : ""}`}
          onClick={() => setEnabled(false)}
        >
          <span className="weekly-mode-title">持续实时同步（全天候）</span>
          <span className="weekly-mode-desc">
            7×24 小时实时监听并全速同步所有文件变动
          </span>
        </button>
        <button
          type="button"
          role="radio"
          aria-checked={schedule.enabled}
          className={`weekly-mode-option${schedule.enabled ? " is-selected" : ""}`}
          onClick={() => setEnabled(true)}
        >
          <span className="weekly-mode-title">按周计划调度</span>
          <span className="weekly-mode-desc">
            自定义 7×24 小时全速、限速与暂停时段
          </span>
        </button>
      </div>

      {!schedule.enabled ? (
        <div className="sync-method-banner" role="status">
          <span>
            {formatWeeklyScheduleSummary(schedule)}；若需夜间全速或工作时间避让带宽，可切换为「按周计划调度」。
          </span>
        </div>
      ) : (
        <div className="weekly-schedule-editor">
          <div className="weekly-toolbar">
            <div
              className="weekly-templates"
              role="group"
              aria-label="周计划快捷模板"
            >
              <span className="weekly-toolbar-label">快捷模板：</span>
              {WEEKLY_SCHEDULE_TEMPLATES.map((tpl) => (
                <Button
                  key={tpl.key}
                  size="small"
                  theme="light"
                  title={tpl.desc}
                  onClick={() => commit(applyWeeklyScheduleTemplate(tpl.key, schedule))}
                >
                  {tpl.label}
                </Button>
              ))}
            </div>
            <div
              className="weekly-brushes"
              role="group"
              aria-label="周历涂抹画笔"
            >
              {BRUSH_OPTIONS.map((opt) => (
                <button
                  key={opt.value}
                  type="button"
                  className={`schedule-chip weekly-brush-chip${brush === opt.value ? " is-active" : ""}${opt.value !== "smart" ? ` is-${opt.value}` : ""}`}
                  aria-pressed={brush === opt.value}
                  onClick={() => setBrush(opt.value)}
                >
                  {opt.value !== "smart" && (
                    <span
                      className={`weekly-dot is-${opt.value}`}
                      aria-hidden="true"
                    />
                  )}
                  <span>{opt.label}</span>
                </button>
              ))}
            </div>
          </div>

          <div className="weekly-legend-row">
            <span className="weekly-legend-item">
              <span className="weekly-dot is-full" aria-hidden="true" />
              全速 ({counts.full}h)
            </span>
            <span className="weekly-legend-item">
              <span className="weekly-dot is-limited" aria-hidden="true" />
              限速 ({counts.limited}h)
            </span>
            <span className="weekly-legend-item">
              <span className="weekly-dot is-pause" aria-hidden="true" />
              暂停 ({counts.pause}h)
            </span>
            <span className="muted weekly-legend-tip">
              点击或按住拖动涂抹时段格子，点击星期/小时标题可整行/整列切换
            </span>
          </div>

          <div className="weekly-grid-scroll">
            <div
              className="weekly-grid"
              role="grid"
              aria-label="7×24 小时同步周历"
            >
              <div className="weekly-grid-header-row" role="row">
                <div className="weekly-grid-corner" role="columnheader">
                  星期 \ 时
                </div>
                {Array.from({ length: WEEKLY_HOUR_COUNT }, (_, hourIdx) => (
                  <button
                    key={hourIdx}
                    type="button"
                    role="columnheader"
                    className="weekly-hour-header"
                    title={`切换全部星期 ${String(hourIdx).padStart(2, "0")}:00 时段`}
                    onClick={() => toggleHourColumn(hourIdx)}
                  >
                    {String(hourIdx).padStart(2, "0")}
                  </button>
                ))}
              </div>

              {WEEKLY_DISPLAY_DAYS.map((dayIdx) => {
                const dayLabel = WEEKLY_DAY_LABELS[dayIdx];
                return (
                  <div
                    key={dayIdx}
                    className="weekly-grid-row"
                    role="row"
                  >
                    <button
                      type="button"
                      role="rowheader"
                      className="weekly-day-header"
                      title={`切换${dayLabel}全天时段`}
                      onClick={() => toggleDayRow(dayIdx)}
                    >
                      {dayLabel}
                    </button>
                    {Array.from({ length: WEEKLY_HOUR_COUNT }, (_, hourIdx) => {
                      const mode: WeeklySlotMode =
                        schedule.days[dayIdx]?.[hourIdx] ?? "full";
                      const hourLabel = `${String(hourIdx).padStart(2, "0")}:00`;
                      const stateLabel = WEEKLY_SLOT_LABELS[mode];
                      return (
                        <button
                          key={hourIdx}
                          type="button"
                          role="gridcell"
                          data-day={dayIdx}
                          data-hour={hourIdx}
                          data-mode={mode}
                          aria-label={`${dayLabel} ${hourLabel} ${stateLabel}`}
                          title={`${dayLabel} ${hourLabel} · ${stateLabel}`}
                          className={`weekly-cell is-${mode}`}
                          onPointerDown={(e) => {
                            e.preventDefault();
                            handleCellPointerDown(dayIdx, hourIdx);
                          }}
                          onPointerEnter={() =>
                            handleCellPointerEnter(dayIdx, hourIdx)
                          }
                          onKeyDown={(e) => handleCellKeyDown(e, dayIdx, hourIdx)}
                        />
                      );
                    })}
                  </div>
                );
              })}
            </div>
          </div>

          <div className="weekly-controls-footer">
            <div className="weekly-rate-row">
              <div className="weekly-rate-field">
                <label
                  htmlFor="weekly-limit-rate"
                  className="weekly-rate-label"
                >
                  限速时段带宽上限
                  {limitedActive && (
                    <Tag size="small" color="amber" type="light">
                      已选 {counts.limited} 个限速时段
                    </Tag>
                  )}
                </label>
                <div className="weekly-rate-input-group">
                  <Input
                    id="weekly-limit-rate"
                    aria-label="限速时段速率"
                    value={schedule.limitRate}
                    placeholder="如 5M / 500K"
                    onChange={(v) =>
                      onChange({
                        ...schedule,
                        limitRate: v,
                      })
                    }
                  />
                  <div className="schedule-chips">
                    {WEEKLY_LIMIT_RATE_PRESETS.map((preset) => (
                      <button
                        key={preset}
                        type="button"
                        className={`schedule-chip${schedule.limitRate.trim().toUpperCase() === preset ? " is-active" : ""}`}
                        onClick={() =>
                          onChange({
                            ...schedule,
                            limitRate: preset,
                          })
                        }
                      >
                        {preset}
                      </button>
                    ))}
                  </div>
                </div>
              </div>
              <div className="weekly-stop-row">
                <span className="weekly-stop-copy">
                  <strong>进入暂停时段时立即中断正在传输的文件</strong>
                  <span className="muted">
                    关闭则允许当前文件传输完毕后再挂起队列
                  </span>
                </span>
                <Switch
                  aria-label="暂停时段立即中断正在传输的文件"
                  checked={schedule.stopOutside}
                  onChange={(checked) =>
                    onChange({
                      ...schedule,
                      stopOutside: checked,
                    })
                  }
                />
              </div>
            </div>
            {effectiveError && (
              <div className="field-error" role="alert">
                {effectiveError}
              </div>
            )}
            <div className="weekly-summary-bar" role="status">
              {formatWeeklyScheduleSummary(schedule)}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
