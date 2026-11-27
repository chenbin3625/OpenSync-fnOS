package service

import (
	"fmt"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strconv"
	"strings"
	"time"
)

// parseTimeWindowClock validates and normalizes an "HH:MM" clock string,
// returning its canonical "HH:MM" form and minute-of-day (0..1439).
func parseTimeWindowClock(raw string) (string, int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", 0, false
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) != 2 {
		return "", 0, false
	}
	hour, errH := strconv.Atoi(parts[0])
	minute, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return "", 0, false
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), hour*60 + minute, true
}

// ValidateTimeWindow validates a job's allowed run window settings.
func ValidateTimeWindow(enable int, startRaw, endRaw string) (string, string, error) {
	startTrim := strings.TrimSpace(startRaw)
	endTrim := strings.TrimSpace(endRaw)
	if enable == 0 {
		if startTrim == "" && endTrim == "" {
			return "", "", nil
		}
		if startTrim != "" {
			norm, _, ok := parseTimeWindowClock(startTrim)
			if !ok {
				return "", "", publicError(msg.T(msg.TimeWindowInvalid))
			}
			startTrim = norm
		}
		if endTrim != "" {
			norm, _, ok := parseTimeWindowClock(endTrim)
			if !ok {
				return "", "", publicError(msg.T(msg.TimeWindowInvalid))
			}
			endTrim = norm
		}
		return startTrim, endTrim, nil
	}

	startNorm, startMin, okStart := parseTimeWindowClock(startTrim)
	endNorm, endMin, okEnd := parseTimeWindowClock(endTrim)
	if !okStart || !okEnd || startMin == endMin {
		return "", "", publicError(msg.T(msg.TimeWindowInvalid))
	}
	return startNorm, endNorm, nil
}

// InTimeWindow reports whether now (evaluated in schedulerLocation()) falls
// within [start, end). Supports cross-midnight windows such as "23:00"-"07:00".
func InTimeWindow(now time.Time, start, end string) bool {
	_, startMin, okStart := parseTimeWindowClock(start)
	_, endMin, okEnd := parseTimeWindowClock(end)
	if !okStart || !okEnd || startMin == endMin {
		return true
	}
	localNow := now.In(schedulerLocation())
	curMin := localNow.Hour()*60 + localNow.Minute()
	if startMin < endMin {
		return curMin >= startMin && curMin < endMin
	}
	// Cross-midnight window (e.g. 23:00 -> 07:00)
	return curMin >= startMin || curMin < endMin
}

// JobInTimeWindow reports whether a job is currently permitted to run under its
// configured weekly schedule (when enabled) or legacy daily time window.
func JobInTimeWindow(job map[string]interface{}, now time.Time) bool {
	if job == nil {
		return true
	}
	if ws := EvaluateWeeklySchedule(job, now); ws.Enabled {
		return ws.Mode != WeeklySlotPause
	}
	if util.ToInt(job["windowEnable"]) != 1 {
		return true
	}
	start := util.StringValue(job["windowStart"])
	end := util.StringValue(job["windowEnd"])
	return InTimeWindow(now, start, end)
}
