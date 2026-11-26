package native

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type throttleDirection int

const (
	throttleAny throttleDirection = iota
	throttleUpload
	throttleDownload
)

const minSplitRateBytes int64 = 1024 // 1 KiB/s floor so rclone never receives 0 (unlimited)

// parseDirectionalRateBytes parses a single resolved rclone rate (e.g. "5M",
// "5.5M:off", "off:10M", "5M:20M", "off") into (uploadBytesPerSec, downloadBytesPerSec).
// A zero value means unlimited in that direction.
func parseDirectionalRateBytes(activeRate string) (upBytes, downBytes int64) {
	s := strings.TrimSpace(activeRate)
	if s == "" || strings.EqualFold(s, "off") || s == "0" {
		return 0, 0
	}
	if up, down, ok := strings.Cut(s, ":"); ok {
		return parseSingleRateBytes(up), parseSingleRateBytes(down)
	}
	v := parseSingleRateBytes(s)
	return v, v
}

func formatBytesRate(bps int64) string {
	if bps <= 0 {
		return "off"
	}
	if bps < minSplitRateBytes {
		bps = minSplitRateBytes
	}
	return strconv.FormatInt(bps, 10) + "B"
}

// splitBwRate divides a resolved single rclone rate (or up:down pair) evenly
// across divisor active transfer streams. Each non-off direction is floored at
// 1 KiB/s so dividing a small cap never silently drops to 0 ("off").
func splitBwRate(activeRate string, divisor int) string {
	norm := NormalizeSingleBwRate(activeRate)
	if norm == "off" || divisor <= 1 {
		return norm
	}
	if upStr, downStr, hasColon := strings.Cut(norm, ":"); hasColon {
		upBps := parseSingleRateBytes(upStr)
		downBps := parseSingleRateBytes(downStr)
		outUp := "off"
		if upBps > 0 {
			outUp = formatBytesRate(max(minSplitRateBytes, upBps/int64(divisor)))
		}
		outDown := "off"
		if downBps > 0 {
			outDown = formatBytesRate(max(minSplitRateBytes, downBps/int64(divisor)))
		}
		if outUp == "off" && outDown == "off" {
			return "off"
		}
		return outUp + ":" + outDown
	}
	bps := parseSingleRateBytes(norm)
	if bps <= 0 {
		return "off"
	}
	return formatBytesRate(max(minSplitRateBytes, bps/int64(divisor)))
}

type globalRcloneSlot struct {
	ep         *rcEndpoint
	paused     bool
	lastPushed string
}

type globalGoSlot struct {
	paused bool
}

type globalBwManager struct {
	mu            sync.Mutex
	spec          string
	rcloneSlots   map[int]*globalRcloneSlot
	goSlots       map[int]*globalGoSlot
	nextKey       int
	scheduleTimer *time.Timer

	// Shared Go transfer pacing clocks across all global-inheriting Go workers.
	throttleMu   sync.Mutex
	nextAnySend  time.Time
	nextUpSend   time.Time
	nextDownSend time.Time
}

func newGlobalBwManager() *globalBwManager {
	return &globalBwManager{
		rcloneSlots: make(map[int]*globalRcloneSlot),
		goSlots:     make(map[int]*globalGoSlot),
	}
}

var defaultGlobalBw = newGlobalBwManager()

// SetGlobalBwLimit updates the process-wide shared bandwidth cap (single rate,
// up:down pair, timetable, or empty/"off" for unlimited) and immediately
// rebalances all active global-inheriting rclone and Go transfer streams.
func SetGlobalBwLimit(spec string) error {
	return defaultGlobalBw.Set(context.Background(), spec)
}

// GetGlobalBwLimit returns the currently configured global bandwidth limit spec.
func GetGlobalBwLimit() string {
	return defaultGlobalBw.Get()
}

func (g *globalBwManager) Get() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.spec
}

func (g *globalBwManager) Set(ctx context.Context, spec string) error {
	trimmed := strings.TrimSpace(spec)
	if err := ValidateBwLimit(trimmed); err != nil {
		return err
	}
	if trimmed == "0" || strings.EqualFold(trimmed, "off") {
		trimmed = ""
	}
	now := time.Now()
	g.mu.Lock()
	g.spec = trimmed
	g.armScheduleTimerLocked(now)
	pushes := g.planRebalanceLocked(now)
	g.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	var firstErr error
	for _, p := range pushes {
		if err := pushRcloneBwLimit(ctx, p.ep, p.rate); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type rcRatePush struct {
	ep   *rcEndpoint
	rate string
}

func (g *globalBwManager) activeSlotCountsLocked() (rcloneActive, goActive int) {
	for _, s := range g.rcloneSlots {
		if !s.paused && s.ep != nil && s.ep.addr != "" {
			rcloneActive++
		}
	}
	for _, s := range g.goSlots {
		if !s.paused {
			goActive++
		}
	}
	return rcloneActive, goActive
}

func (g *globalBwManager) planRebalanceLocked(now time.Time) []rcRatePush {
	activeRate, _, _ := ResolveBwLimitAt(g.spec, now)
	rcloneActive, goActive := g.activeSlotCountsLocked()
	total := rcloneActive + goActive
	if total <= 0 {
		return nil
	}
	perSlotRate := splitBwRate(activeRate, total)
	var pushes []rcRatePush
	for _, s := range g.rcloneSlots {
		if s.paused || s.ep == nil || s.ep.addr == "" {
			continue
		}
		if s.lastPushed == perSlotRate {
			continue
		}
		s.lastPushed = perSlotRate
		cp := *s.ep
		pushes = append(pushes, rcRatePush{ep: &cp, rate: perSlotRate})
	}
	return pushes
}

func (g *globalBwManager) rebalanceAsync(now time.Time) {
	g.mu.Lock()
	pushes := g.planRebalanceLocked(now)
	g.mu.Unlock()
	if len(pushes) == 0 {
		return
	}
	go func() {
		for _, p := range pushes {
			_ = pushRcloneBwLimit(context.Background(), p.ep, p.rate)
		}
	}()
}

func (g *globalBwManager) armScheduleTimerLocked(now time.Time) {
	if g.scheduleTimer != nil {
		g.scheduleTimer.Stop()
		g.scheduleTimer = nil
	}
	_, nextChange, hasSchedule := ResolveBwLimitAt(g.spec, now)
	if !hasSchedule || nextChange.IsZero() {
		return
	}
	wait := time.Until(nextChange)
	if wait < time.Second {
		wait = time.Second
	}
	g.scheduleTimer = time.AfterFunc(wait, func() {
		now := time.Now()
		g.mu.Lock()
		g.armScheduleTimerLocked(now)
		pushes := g.planRebalanceLocked(now)
		g.mu.Unlock()
		for _, p := range pushes {
			_ = pushRcloneBwLimit(context.Background(), p.ep, p.rate)
		}
	})
}

// EstimateNextRcloneRate computes the initial --bwlimit value for a new
// global-inheriting rclone process about to start, so it starts capped at its
// fair share even before its RC listener binds.
func (g *globalBwManager) EstimateNextRcloneRate() string {
	now := time.Now()
	g.mu.Lock()
	spec := g.spec
	rcloneActive, goActive := g.activeSlotCountsLocked()
	g.mu.Unlock()

	if spec == "" {
		return ""
	}
	activeRate, _, _ := ResolveBwLimitAt(spec, now)
	if activeRate == "off" {
		return ""
	}
	return splitBwRate(activeRate, rcloneActive+goActive+1)
}

func (g *globalBwManager) registerRclone(ep *rcEndpoint, paused bool) int {
	if ep == nil || ep.addr == "" {
		return 0
	}
	now := time.Now()
	cp := *ep
	g.mu.Lock()
	g.nextKey++
	key := g.nextKey
	g.rcloneSlots[key] = &globalRcloneSlot{ep: &cp, paused: paused}
	g.armScheduleTimerLocked(now)
	pushes := g.planRebalanceLocked(now)
	g.mu.Unlock()

	if len(pushes) > 0 {
		go func() {
			for _, p := range pushes {
				if p.ep.addr == cp.addr {
					// Give the newly launched rclone RC listener a brief moment to bind.
					time.Sleep(200 * time.Millisecond)
				}
				if err := pushRcloneBwLimit(context.Background(), p.ep, p.rate); err != nil && p.ep.addr == cp.addr {
					// One retry in case rclone took slightly longer to open its RC socket.
					time.Sleep(400 * time.Millisecond)
					_ = pushRcloneBwLimit(context.Background(), p.ep, p.rate)
				}
			}
		}()
	}
	return key
}

func (g *globalBwManager) unregisterRclone(key int) {
	if key <= 0 {
		return
	}
	g.mu.Lock()
	delete(g.rcloneSlots, key)
	pushes := g.planRebalanceLocked(time.Now())
	g.mu.Unlock()
	if len(pushes) > 0 {
		go func() {
			for _, p := range pushes {
				_ = pushRcloneBwLimit(context.Background(), p.ep, p.rate)
			}
		}()
	}
}

func (g *globalBwManager) setRclonePaused(key int, paused bool) {
	if key <= 0 {
		return
	}
	now := time.Now()
	g.mu.Lock()
	slot, ok := g.rcloneSlots[key]
	if !ok {
		g.mu.Unlock()
		return
	}
	slot.paused = paused
	if !paused {
		// Force push on resume because the process was SIGSTOP'd while rates may have changed.
		slot.lastPushed = ""
	}
	pushes := g.planRebalanceLocked(now)
	g.mu.Unlock()

	if len(pushes) > 0 {
		go func() {
			if !paused {
				time.Sleep(150 * time.Millisecond)
			}
			for _, p := range pushes {
				_ = pushRcloneBwLimit(context.Background(), p.ep, p.rate)
			}
		}()
	}
}

func (g *globalBwManager) registerGo(paused bool) int {
	now := time.Now()
	g.mu.Lock()
	g.nextKey++
	key := g.nextKey
	g.goSlots[key] = &globalGoSlot{paused: paused}
	g.armScheduleTimerLocked(now)
	pushes := g.planRebalanceLocked(now)
	g.mu.Unlock()

	if len(pushes) > 0 {
		go func() {
			for _, p := range pushes {
				_ = pushRcloneBwLimit(context.Background(), p.ep, p.rate)
			}
		}()
	}
	return key
}

func (g *globalBwManager) unregisterGo(key int) {
	if key <= 0 {
		return
	}
	g.mu.Lock()
	delete(g.goSlots, key)
	pushes := g.planRebalanceLocked(time.Now())
	g.mu.Unlock()
	if len(pushes) > 0 {
		go func() {
			for _, p := range pushes {
				_ = pushRcloneBwLimit(context.Background(), p.ep, p.rate)
			}
		}()
	}
}

func (g *globalBwManager) setGoPaused(key int, paused bool) {
	if key <= 0 {
		return
	}
	g.mu.Lock()
	if slot, ok := g.goSlots[key]; ok {
		slot.paused = paused
	}
	pushes := g.planRebalanceLocked(time.Now())
	g.mu.Unlock()
	if len(pushes) > 0 {
		go func() {
			for _, p := range pushes {
				_ = pushRcloneBwLimit(context.Background(), p.ep, p.rate)
			}
		}()
	}
}

// effectiveGoBytesPerSec returns the aggregate bytes/sec budget shared by all
// active global-inheriting Go transfer streams in the requested direction.
func (g *globalBwManager) effectiveGoBytesPerSec(now time.Time, dir throttleDirection) int64 {
	g.mu.Lock()
	spec := g.spec
	rcloneActive, goActive := g.activeSlotCountsLocked()
	g.mu.Unlock()

	if spec == "" {
		return 0
	}
	activeRate, _, _ := ResolveBwLimitAt(spec, now)
	var totalBps int64
	switch dir {
	case throttleUpload:
		totalBps, _ = parseDirectionalRateBytes(activeRate)
	case throttleDownload:
		_, totalBps = parseDirectionalRateBytes(activeRate)
	default:
		totalBps = parseEffectiveSingleRateBytes(activeRate)
	}
	if totalBps <= 0 {
		return 0
	}
	if goActive <= 0 {
		goActive = 1
	}
	totalSlots := rcloneActive + goActive
	if totalSlots <= goActive {
		return totalBps
	}
	share := (totalBps * int64(goActive)) / int64(totalSlots)
	if share < minSplitRateBytes {
		share = minSplitRateBytes
	}
	return share
}

func (g *globalBwManager) throttle(ctx context.Context, n int, dir throttleDirection) error {
	if n <= 0 {
		return ctx.Err()
	}
	now := time.Now()
	bps := g.effectiveGoBytesPerSec(now, dir)
	if bps <= 0 {
		return ctx.Err()
	}
	dur := time.Duration(float64(n) / float64(bps) * float64(time.Second))
	if dur <= 0 {
		return ctx.Err()
	}

	g.throttleMu.Lock()
	var clock *time.Time
	switch dir {
	case throttleUpload:
		clock = &g.nextUpSend
	case throttleDownload:
		clock = &g.nextDownSend
	default:
		clock = &g.nextAnySend
	}
	now = time.Now()
	if clock.Before(now) {
		*clock = now
	}
	*clock = clock.Add(dur)
	wait := time.Until(*clock)
	g.throttleMu.Unlock()

	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ResetGlobalBwLimitForTest resets the singleton global limiter state in tests.
func ResetGlobalBwLimitForTest() {
	defaultGlobalBw.mu.Lock()
	if defaultGlobalBw.scheduleTimer != nil {
		defaultGlobalBw.scheduleTimer.Stop()
		defaultGlobalBw.scheduleTimer = nil
	}
	defaultGlobalBw.spec = ""
	defaultGlobalBw.rcloneSlots = make(map[int]*globalRcloneSlot)
	defaultGlobalBw.goSlots = make(map[int]*globalGoSlot)
	defaultGlobalBw.mu.Unlock()

	defaultGlobalBw.throttleMu.Lock()
	defaultGlobalBw.nextAnySend = time.Time{}
	defaultGlobalBw.nextUpSend = time.Time{}
	defaultGlobalBw.nextDownSend = time.Time{}
	defaultGlobalBw.throttleMu.Unlock()
}

// GlobalActiveStreamCount returns active (rclone, go) inheriting stream counts.
func GlobalActiveStreamCount() (rcloneActive, goActive int) {
	defaultGlobalBw.mu.Lock()
	defer defaultGlobalBw.mu.Unlock()
	return defaultGlobalBw.activeSlotCountsLocked()
}

// DescribeSplitBwRateForDebug is an internal helper for diagnostics/tests.
func DescribeSplitBwRateForDebug(spec string, divisor int, at time.Time) string {
	active, _, _ := ResolveBwLimitAt(spec, at)
	return fmt.Sprintf("%s/%d=%s", active, divisor, splitBwRate(active, divisor))
}
