package native

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

const minutesPerDay = 24 * 60
const minutesPerWeek = 7 * minutesPerDay

type bwTimetableEntry struct {
	weekMin int // 0..10079 (Sunday 00:00 .. Saturday 23:59)
	rate    string
}

func parseWeekdayPrefix(name string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "sun":
		return int(time.Sunday), true
	case "mon":
		return int(time.Monday), true
	case "tue":
		return int(time.Tuesday), true
	case "wed":
		return int(time.Wednesday), true
	case "thu":
		return int(time.Thursday), true
	case "fri":
		return int(time.Friday), true
	case "sat":
		return int(time.Saturday), true
	default:
		return 0, false
	}
}

func normalizeBwRatePart(part string) string {
	trimmed := strings.TrimSpace(part)
	if trimmed == "" || trimmed == "0" || strings.EqualFold(trimmed, "off") {
		return "off"
	}
	return trimmed
}

// NormalizeSingleBwRate normalizes a single rate or up:down pair into a canonical
// rclone /core/bwlimit rate string ("off", "10M", "5M:20M", "5M:off", etc.).
func NormalizeSingleBwRate(spec string) string {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" || trimmed == "0" || strings.EqualFold(trimmed, "off") {
		return "off"
	}
	if up, down, ok := strings.Cut(trimmed, ":"); ok {
		u := normalizeBwRatePart(up)
		d := normalizeBwRatePart(down)
		if u == "off" && d == "off" {
			return "off"
		}
		return u + ":" + d
	}
	return trimmed
}

func parseBwTimetableEntries(spec string) ([]bwTimetableEntry, bool) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil, false
	}
	tokens := strings.Fields(trimmed)
	if len(tokens) == 1 && !strings.Contains(tokens[0], ",") {
		return nil, false
	}
	rawEntries := make([]bwTimetableEntry, 0, len(tokens)*7)
	for _, tok := range tokens {
		timeSpec, rateSpec, ok := strings.Cut(tok, ",")
		if !ok {
			return nil, false
		}
		weekday := -1
		hhmm := timeSpec
		if dashIdx := strings.IndexByte(timeSpec, '-'); dashIdx >= 0 {
			wd, valid := parseWeekdayPrefix(timeSpec[:dashIdx])
			if !valid {
				return nil, false
			}
			weekday = wd
			hhmm = timeSpec[dashIdx+1:]
		}
		hhStr, mmStr, ok := strings.Cut(hhmm, ":")
		if !ok {
			return nil, false
		}
		hh, err1 := strconv.Atoi(hhStr)
		mm, err2 := strconv.Atoi(mmStr)
		if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
			return nil, false
		}
		dayMin := hh*60 + mm
		rate := NormalizeSingleBwRate(rateSpec)
		if weekday >= 0 {
			rawEntries = append(rawEntries, bwTimetableEntry{
				weekMin: weekday*minutesPerDay + dayMin,
				rate:    rate,
			})
		} else {
			for wd := 0; wd < 7; wd++ {
				rawEntries = append(rawEntries, bwTimetableEntry{
					weekMin: wd*minutesPerDay + dayMin,
					rate:    rate,
				})
			}
		}
	}
	if len(rawEntries) == 0 {
		return nil, false
	}
	slices.SortStableFunc(rawEntries, func(a, b bwTimetableEntry) int {
		return a.weekMin - b.weekMin
	})
	// Deduplicate identical weekly timestamps keeping the last entry (matching rclone).
	entries := make([]bwTimetableEntry, 0, len(rawEntries))
	for _, e := range rawEntries {
		if len(entries) > 0 && entries[len(entries)-1].weekMin == e.weekMin {
			entries[len(entries)-1].rate = e.rate
			continue
		}
		entries = append(entries, e)
	}
	return entries, true
}

// ResolveBwLimitAt evaluates an rclone --bwlimit specification (single rate or
// timetable) at timestamp now. It returns:
//   - activeRate: single rclone rate or up:down pair ("off" when unlimited),
//     safe to pass directly to rclone RC /core/bwlimit;
//   - nextChange: next wall-clock boundary where the effective rate changes
//     (zero if static or all slots share the same rate);
//   - hasSchedule: true when spec is a timetable rather than a single static rate.
func ResolveBwLimitAt(spec string, now time.Time) (activeRate string, nextChange time.Time, hasSchedule bool) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return "off", time.Time{}, false
	}
	entries, isTimetable := parseBwTimetableEntries(trimmed)
	if !isTimetable {
		return NormalizeSingleBwRate(trimmed), time.Time{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	nowWeekMin := int(now.Weekday())*minutesPerDay + now.Hour()*60 + now.Minute()
	activeIdx := len(entries) - 1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].weekMin <= nowWeekMin {
			activeIdx = i
			break
		}
	}
	activeRate = entries[activeIdx].rate

	for step := 1; step <= len(entries); step++ {
		idx := (activeIdx + step) % len(entries)
		if strings.EqualFold(entries[idx].rate, activeRate) {
			continue
		}
		targetWeekMin := entries[idx].weekMin
		targetWD := targetWeekMin / minutesPerDay
		targetDayMin := targetWeekMin % minutesPerDay
		daysAhead := (targetWD - int(now.Weekday()) + 7) % 7
		candidate := time.Date(
			now.Year(), now.Month(), now.Day()+daysAhead,
			targetDayMin/60, targetDayMin%60, 0, 0, now.Location(),
		)
		if !candidate.After(now) {
			candidate = time.Date(
				now.Year(), now.Month(), now.Day()+daysAhead+7,
				targetDayMin/60, targetDayMin%60, 0, 0, now.Location(),
			)
		}
		nextChange = candidate
		break
	}
	return activeRate, nextChange, true
}

// ResolveBwLimitValue returns the active single-rate value for a bandwidth
// specification at now. It is useful to combine nested OpenSync limits while
// keeping timetable evaluation in one place.
func ResolveBwLimitValue(spec string, now time.Time) string {
	active, _, _ := ResolveBwLimitAt(spec, now)
	return active
}

// ParseBwLimitBytesPerSecAt evaluates rate (single rate, up:down, or timetable)
// at now and returns the effective bytes-per-second limit (0 = unlimited).
func ParseBwLimitBytesPerSecAt(rate string, now time.Time) int64 {
	activeRate, _, _ := ResolveBwLimitAt(rate, now)
	return parseEffectiveSingleRateBytes(activeRate)
}

func parseEffectiveSingleRateBytes(activeRate string) int64 {
	s := strings.TrimSpace(activeRate)
	if s == "" || strings.EqualFold(s, "off") || s == "0" {
		return 0
	}
	// For asymmetric up:down rates, use the smaller non-zero cap conservatively.
	if up, down, ok := strings.Cut(s, ":"); ok {
		a := parseSingleRateBytes(up)
		b := parseSingleRateBytes(down)
		if a > 0 && b > 0 {
			if a < b {
				return a
			}
			return b
		}
		if a > 0 {
			return a
		}
		return b
	}
	return parseSingleRateBytes(s)
}
