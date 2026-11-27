package service

import (
	"opensync/internal/native"
	"opensync/pkg/util"
	"strings"
	"time"
)

const (
	WeeklySlotFull    = "full"
	WeeklySlotLimited = "limited"
	WeeklySlotPause   = "pause"
)

// WeeklyScheduleConfig defines a 7x24 weekly run/rate calendar stored inside
// job extraFlags under "weeklySchedule". Day indices follow Go time.Weekday
// (0=Sunday .. 6=Saturday), and each day contains 24 hourly cells (0..23).
type WeeklyScheduleConfig struct {
	Enabled     bool       `json:"enabled"`
	LimitRate   string     `json:"limitRate,omitempty"`
	StopOutside bool       `json:"stopOutside,omitempty"`
	Days        [][]string `json:"days,omitempty"`
}

// WeeklyScheduleState describes the effective schedule decision for a point in time.
type WeeklyScheduleState struct {
	Enabled     bool
	Mode        string // "", "full", "limited", "pause"
	LimitRate   string
	StopOutside bool
}

func parseWeeklySchedule(raw interface{}) (*WeeklyScheduleConfig, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, publicError("每周定时计划配置格式无效")
	}
	out := &WeeklyScheduleConfig{}
	var daysRaw interface{}
	hasDays := false
	for k, v := range m {
		switch k {
		case "enabled":
			if v == nil {
				continue
			}
			b, valid := extraFlagBool(v)
			if !valid {
				return nil, publicError("每周定时计划 enabled 需为布尔值")
			}
			out.Enabled = b
		case "stopOutside":
			if v == nil {
				continue
			}
			b, valid := extraFlagBool(v)
			if !valid {
				return nil, publicError("每周定时计划 stopOutside 需为布尔值")
			}
			out.StopOutside = b
		case "limitRate":
			s, valid := extraFlagString(v)
			if !valid {
				return nil, publicError("每周定时计划限速值需为字符串")
			}
			if s != "" {
				if err := native.ValidateBwLimit(s); err != nil {
					return nil, err
				}
			}
			out.LimitRate = s
		case "days":
			hasDays = true
			daysRaw = v
		default:
			return nil, publicError("每周定时计划包含不支持的配置项: " + k)
		}
	}

	if hasDays && daysRaw != nil {
		dayRows, validRows := daysRaw.([]interface{})
		if !validRows || len(dayRows) != 7 {
			return nil, publicError("每周定时计划 days 必须为 7×24 二维数组")
		}
		days := make([][]string, 7)
		for d := 0; d < 7; d++ {
			hoursRaw, validHours := dayRows[d].([]interface{})
			if !validHours || len(hoursRaw) != 24 {
				return nil, publicError("每周定时计划每天必须包含 24 个小时时段")
			}
			row := make([]string, 24)
			for h := 0; h < 24; h++ {
				cell, isStr := hoursRaw[h].(string)
				if !isStr {
					return nil, publicError("每周定时计划时段值仅支持 full、limited 或 pause")
				}
				mode := strings.ToLower(strings.TrimSpace(cell))
				switch mode {
				case WeeklySlotFull, WeeklySlotLimited, WeeklySlotPause:
					row[h] = mode
				default:
					return nil, publicError("每周定时计划时段值仅支持 full、limited 或 pause")
				}
			}
			days[d] = row
		}
		out.Days = days
	} else if out.Enabled {
		return nil, publicError("启用每周定时计划时必须提供 7×24 时段配置")
	}

	return out, nil
}

// WeeklyScheduleSlotAt resolves the schedule cell mode for cfg at now.
func WeeklyScheduleSlotAt(cfg *WeeklyScheduleConfig, now time.Time) (active bool, mode string, limitRate string) {
	if cfg == nil || !cfg.Enabled || len(cfg.Days) != 7 {
		return false, "", ""
	}
	if now.IsZero() {
		now = time.Now()
	}
	day := int(now.Weekday())
	hour := now.Hour()
	if day < 0 || day >= 7 || hour < 0 || hour >= 24 || len(cfg.Days[day]) != 24 {
		return false, "", ""
	}
	mode = cfg.Days[day][hour]
	switch mode {
	case WeeklySlotPause:
		return true, WeeklySlotPause, ""
	case WeeklySlotLimited:
		return true, WeeklySlotLimited, strings.TrimSpace(cfg.LimitRate)
	case WeeklySlotFull:
		return true, WeeklySlotFull, ""
	default:
		return false, "", ""
	}
}

// EvaluateWeeklySchedule resolves the active weekly schedule state for job at now.
func EvaluateWeeklySchedule(job map[string]interface{}, now time.Time) WeeklyScheduleState {
	if job == nil {
		return WeeklyScheduleState{}
	}
	extra, _, err := parseNativeExtraFlags(job["extraFlags"])
	if err != nil || extra.WeeklySchedule == nil || !extra.WeeklySchedule.Enabled {
		return WeeklyScheduleState{}
	}
	active, mode, rate := WeeklyScheduleSlotAt(extra.WeeklySchedule, now)
	if !active {
		return WeeklyScheduleState{}
	}
	state := WeeklyScheduleState{
		Enabled:     true,
		Mode:        mode,
		StopOutside: extra.WeeklySchedule.StopOutside,
	}
	switch mode {
	case WeeklySlotFull:
		state.LimitRate = "off"
	case WeeklySlotLimited:
		if rate != "" {
			state.LimitRate = rate
		} else {
			state.LimitRate = legacyEffectiveJobBwLimit(job)
		}
	}
	return state
}

// JobShouldStopOutsideWindow reports whether a running non-manual task should
// stop immediately when leaving its active run window (or entering a pause slot).
func JobShouldStopOutsideWindow(job map[string]interface{}) bool {
	if job == nil {
		return false
	}
	extra, _, err := parseNativeExtraFlags(job["extraFlags"])
	if err == nil && extra.WeeklySchedule != nil && extra.WeeklySchedule.Enabled {
		return extra.WeeklySchedule.StopOutside
	}
	return util.ToInt(job["windowEnable"]) == 1 && util.ToInt(job["windowStopOutside"]) == 1
}

// JobTaskInheritsGlobalBwLimit reports whether a job should receive global default
// bandwidth updates (not pinned by weeklySchedule or per-job bwLimit).
func jobTaskInheritsGlobalBwLimit(job map[string]interface{}) bool {
	if job == nil {
		return true
	}
	if EvaluateWeeklySchedule(job, time.Now()).Enabled {
		return false
	}
	return strings.TrimSpace(util.StringValue(job["bwLimit"])) == ""
}
