package native

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RCController coordinates live runtime controls (pause/resume and dynamic
// bandwidth throttling) across both rclone subprocesses (via private loopback
// --rc + SIGSTOP/SIGCONT) and pure-Go resumable transfer workers.
type RCController struct {
	mu               sync.Mutex
	cond             *sync.Cond
	paused           atomic.Bool
	manualPaused     bool
	connectionPauses map[string]struct{}
	pausedSince      time.Time
	pausedAccum      time.Duration
	inheritsGlobal   bool
	bwLimit          string
	liveOverridden   bool
	hasSchedule      atomic.Bool
	lastEvalMin      atomic.Int64
	bytesPerSec      atomic.Int64
	upBytesPerSec    atomic.Int64
	downBytesPerSec  atomic.Int64
	activeProcs      map[int]*rcEndpoint
	globalRcloneKeys map[int]int
	nextProcKey      int
	activeGoCount    int
	globalGoKeys     map[int]int
	nextGoKey        int
	scheduleTimer    *time.Timer
	closed           bool
	throttleMu       sync.Mutex
	nextSendTime     time.Time
	nextUpSendTime   time.Time
	nextDownSendTime time.Time
}

type rcEndpoint struct {
	pid  int
	addr string
	user string
	pass string
}

// NewRCController creates a standalone live transfer controller initialized with initialBwLimit.
func NewRCController(initialBwLimit string) *RCController {
	c := &RCController{
		bwLimit:          strings.TrimSpace(initialBwLimit),
		activeProcs:      make(map[int]*rcEndpoint),
		globalRcloneKeys: make(map[int]int),
		globalGoKeys:     make(map[int]int),
		connectionPauses: make(map[string]struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	c.refreshLocalRateCacheLocked(time.Now())
	return c
}

// NewGlobalRCController creates a live transfer controller that shares the
// process-wide global bandwidth cap across all concurrent tasks and pairs.
func NewGlobalRCController() *RCController {
	c := &RCController{
		inheritsGlobal:   true,
		activeProcs:      make(map[int]*rcEndpoint),
		globalRcloneKeys: make(map[int]int),
		globalGoKeys:     make(map[int]int),
		connectionPauses: make(map[string]struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// InheritsGlobal reports whether this controller currently shares the global bandwidth budget.
func (c *RCController) InheritsGlobal() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inheritsGlobal
}

func (c *RCController) refreshLocalRateCacheLocked(now time.Time) {
	activeRate, _, hasSchedule := ResolveBwLimitAt(c.bwLimit, now)
	c.hasSchedule.Store(hasSchedule)
	c.lastEvalMin.Store(now.Unix() / 60)
	c.bytesPerSec.Store(parseEffectiveSingleRateBytes(activeRate))
	up, down := parseDirectionalRateBytes(activeRate)
	c.upBytesPerSec.Store(up)
	c.downBytesPerSec.Store(down)
}

// Close releases background timetable timers and detaches any remaining global
// bandwidth slots owned by this controller.
func (c *RCController) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	if c.scheduleTimer != nil {
		c.scheduleTimer.Stop()
		c.scheduleTimer = nil
	}
	rcloneKeys := make([]int, 0, len(c.globalRcloneKeys))
	for _, k := range c.globalRcloneKeys {
		rcloneKeys = append(rcloneKeys, k)
	}
	clear(c.globalRcloneKeys)
	goKeys := make([]int, 0, len(c.globalGoKeys))
	for _, k := range c.globalGoKeys {
		goKeys = append(goKeys, k)
	}
	clear(c.globalGoKeys)
	c.mu.Unlock()

	for _, k := range rcloneKeys {
		defaultGlobalBw.unregisterRclone(k)
	}
	for _, k := range goKeys {
		defaultGlobalBw.unregisterGo(k)
	}
}

// IsPaused reports whether transfers under this controller are currently paused.
func (c *RCController) IsPaused() bool {
	if c == nil {
		return false
	}
	return c.paused.Load()
}

// PausedDuration returns the total cumulative time spent paused under this controller.
func (c *RCController) PausedDuration() time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	total := c.pausedAccum
	if !c.pausedSince.IsZero() {
		total += time.Since(c.pausedSince)
	}
	return total
}

// BwLimit returns the currently configured live bandwidth limit expression.
func (c *RCController) BwLimit() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	inherits := c.inheritsGlobal
	spec := c.bwLimit
	c.mu.Unlock()
	if inherits {
		return defaultGlobalBw.Get()
	}
	return spec
}

// SplitBwRateForShare returns a fair per-stream share of a resolved bandwidth
// rate. It is exported for the service-level connection limiter so a shared
// connection cap is divided across active tasks before each task divides its
// own share across rclone and Go transfer streams.
func SplitBwRateForShare(activeRate string, divisor int) string {
	return splitBwRate(activeRate, divisor)
}

// EffectiveInitialRcloneBwLimit returns the --bwlimit argument a newly launched
// rclone process under this controller should start with so concurrent streams
// stay within the shared cap from the very first byte.
func (c *RCController) EffectiveInitialRcloneBwLimit(fallback string) string {
	if c == nil {
		return strings.TrimSpace(fallback)
	}
	c.mu.Lock()
	inherits := c.inheritsGlobal
	spec := c.bwLimit
	ownSlots := c.standaloneActiveSlotCountLocked()
	c.mu.Unlock()

	if inherits {
		return defaultGlobalBw.EstimateNextRcloneRate()
	}
	effective := strings.TrimSpace(spec)
	if effective == "" {
		effective = strings.TrimSpace(fallback)
	}
	if effective == "" {
		return ""
	}
	if ownSlots == 0 {
		return effective
	}
	activeRate, _, _ := ResolveBwLimitAt(effective, time.Now())
	if activeRate == "off" {
		return ""
	}
	return splitBwRate(activeRate, ownSlots+1)
}

func (c *RCController) standaloneActiveSlotCountLocked() int {
	if c.paused.Load() {
		return 0
	}
	count := c.activeGoCount
	for _, ep := range c.activeProcs {
		if ep.addr != "" {
			count++
		}
	}
	return count
}

func (c *RCController) rebalanceStandaloneAsyncLocked(now time.Time, newlyAttachedAddr string) {
	if c.inheritsGlobal || c.paused.Load() {
		return
	}
	rcCount := 0
	eps := make([]*rcEndpoint, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.addr != "" {
			rcCount++
			cp := *ep
			eps = append(eps, &cp)
		}
	}
	if rcCount == 0 {
		return
	}
	totalSlots := rcCount + c.activeGoCount
	if !c.liveOverridden && (c.bwLimit == "" || totalSlots <= 1) {
		return
	}
	activeRate, _, _ := ResolveBwLimitAt(c.bwLimit, now)
	perSlotRate := splitBwRate(activeRate, totalSlots)
	go func() {
		for _, ep := range eps {
			if newlyAttachedAddr != "" && ep.addr == newlyAttachedAddr {
				time.Sleep(200 * time.Millisecond)
			}
			_ = pushRcloneBwLimit(context.Background(), ep, perSlotRate)
		}
	}()
}

// EnterGoTransfer registers an active pure-Go transfer stream (such as Quark
// direct upload, S3 multipart upload, or resumable download) so concurrent
// rclone and Go streams dynamically share the bandwidth budget.
func (c *RCController) EnterGoTransfer() func() {
	if c == nil {
		return func() {}
	}
	c.mu.Lock()
	c.nextGoKey++
	localKey := c.nextGoKey
	c.activeGoCount++
	inherits := c.inheritsGlobal
	isPaused := c.paused.Load()
	if !inherits {
		c.rebalanceStandaloneAsyncLocked(time.Now(), "")
	}
	c.mu.Unlock()

	var gKey int
	if inherits {
		gKey = defaultGlobalBw.registerGo(isPaused)
		c.mu.Lock()
		if !c.closed && c.inheritsGlobal {
			c.globalGoKeys[localKey] = gKey
		} else {
			c.mu.Unlock()
			defaultGlobalBw.unregisterGo(gKey)
			gKey = 0
			c.mu.Lock()
		}
		c.mu.Unlock()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.activeGoCount > 0 {
				c.activeGoCount--
			}
			gk := c.globalGoKeys[localKey]
			delete(c.globalGoKeys, localKey)
			if !c.inheritsGlobal {
				c.rebalanceStandaloneAsyncLocked(time.Now(), "")
			}
			c.mu.Unlock()
			if gk > 0 {
				defaultGlobalBw.unregisterGo(gk)
			}
		})
	}
}

// Attach registers a running rclone process and its private RC endpoint.
// The returned detach function must be called once the process exits.
func (c *RCController) Attach(pid int, rcAddr, rcUser, rcPass string) func() {
	if c == nil {
		return func() {}
	}
	now := time.Now()
	c.mu.Lock()
	c.nextProcKey++
	key := c.nextProcKey
	ep := &rcEndpoint{pid: pid, addr: rcAddr, user: rcUser, pass: rcPass}
	c.activeProcs[key] = ep
	shouldPause := c.paused.Load()
	inherits := c.inheritsGlobal
	if !inherits && !c.closed && (c.liveOverridden || (c.bwLimit != "" && c.standaloneActiveSlotCountLocked() > 1)) {
		c.armScheduleTimerLocked(now)
	}
	if !inherits {
		c.rebalanceStandaloneAsyncLocked(now, rcAddr)
	}
	c.mu.Unlock()

	if shouldPause && pid > 0 {
		_ = pauseProcess(pid)
	}
	if inherits && rcAddr != "" {
		gKey := defaultGlobalBw.registerRclone(ep, shouldPause)
		c.mu.Lock()
		if !c.closed && c.inheritsGlobal {
			c.globalRcloneKeys[key] = gKey
		} else {
			c.mu.Unlock()
			defaultGlobalBw.unregisterRclone(gKey)
			c.mu.Lock()
		}
		c.mu.Unlock()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.activeProcs, key)
			gKey := c.globalRcloneKeys[key]
			delete(c.globalRcloneKeys, key)
			if !c.inheritsGlobal {
				c.rebalanceStandaloneAsyncLocked(time.Now(), "")
			}
			c.mu.Unlock()
			if gKey > 0 {
				defaultGlobalBw.unregisterRclone(gKey)
			}
		})
	}
}

// ResumeAllProcesses ensures any SIGSTOP'd child processes receive SIGCONT so
// cancellation/termination never leaves a stopped child hanging.
func (c *RCController) ResumeAllProcesses() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.manualPaused = false
	clear(c.connectionPauses)
	pids := make([]int, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.pid > 0 {
			pids = append(pids, ep.pid)
		}
	}
	if !c.pausedSince.IsZero() {
		c.pausedAccum += time.Since(c.pausedSince)
		c.pausedSince = time.Time{}
	}
	c.paused.Store(false)
	c.cond.Broadcast()
	c.mu.Unlock()
	for _, pid := range pids {
		_ = resumeProcess(pid)
	}
}

// Pause suspends active transfers (both rclone subprocesses and Go workers).
func (c *RCController) Pause() error {
	if c == nil {
		return errors.New("任务不支持暂停")
	}
	c.mu.Lock()
	c.manualPaused = true
	if c.paused.Load() {
		c.mu.Unlock()
		return nil
	}
	c.paused.Store(true)
	c.pausedSince = time.Now()
	c.cond.Broadcast()
	pids := make([]int, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.pid > 0 {
			pids = append(pids, ep.pid)
		}
	}
	rcloneKeys := make([]int, 0, len(c.globalRcloneKeys))
	for _, k := range c.globalRcloneKeys {
		rcloneKeys = append(rcloneKeys, k)
	}
	goKeys := make([]int, 0, len(c.globalGoKeys))
	for _, k := range c.globalGoKeys {
		goKeys = append(goKeys, k)
	}
	c.mu.Unlock()

	for _, k := range rcloneKeys {
		defaultGlobalBw.setRclonePaused(k, true)
	}
	for _, k := range goKeys {
		defaultGlobalBw.setGoPaused(k, true)
	}

	var firstErr error
	for _, pid := range pids {
		if err := pauseProcess(pid); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// PauseForConnection adds a connection-level pause without changing the
// task's manual pause state. Multiple connections may pause one task and each
// connection must release only its own pause token.
func (c *RCController) PauseForConnection(connection string) error {
	if c == nil {
		return errors.New("任务不支持暂停")
	}
	connection = strings.TrimSpace(connection)
	if connection == "" {
		return errors.New("连接名称为空")
	}
	c.mu.Lock()
	if c.connectionPauses == nil {
		c.connectionPauses = make(map[string]struct{})
	}
	if _, exists := c.connectionPauses[connection]; exists {
		c.mu.Unlock()
		return nil
	}
	c.connectionPauses[connection] = struct{}{}
	if c.paused.Load() {
		c.mu.Unlock()
		return nil
	}
	c.paused.Store(true)
	c.pausedSince = time.Now()
	c.cond.Broadcast()
	pids := make([]int, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.pid > 0 {
			pids = append(pids, ep.pid)
		}
	}
	rcloneKeys := make([]int, 0, len(c.globalRcloneKeys))
	for _, k := range c.globalRcloneKeys {
		rcloneKeys = append(rcloneKeys, k)
	}
	goKeys := make([]int, 0, len(c.globalGoKeys))
	for _, k := range c.globalGoKeys {
		goKeys = append(goKeys, k)
	}
	c.mu.Unlock()
	for _, k := range rcloneKeys {
		defaultGlobalBw.setRclonePaused(k, true)
	}
	for _, k := range goKeys {
		defaultGlobalBw.setGoPaused(k, true)
	}
	var firstErr error
	for _, pid := range pids {
		if err := pauseProcess(pid); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ResumeForConnection releases only a connection-level pause. A manual task
// pause or another paused connection keeps the transfer suspended.
func (c *RCController) ResumeForConnection(connection string) error {
	if c == nil {
		return errors.New("任务不支持恢复")
	}
	connection = strings.TrimSpace(connection)
	if connection == "" {
		return errors.New("连接名称为空")
	}
	c.mu.Lock()
	if _, exists := c.connectionPauses[connection]; !exists {
		c.mu.Unlock()
		return nil
	}
	delete(c.connectionPauses, connection)
	if c.manualPaused || len(c.connectionPauses) > 0 || !c.paused.Load() {
		c.mu.Unlock()
		return nil
	}
	c.paused.Store(false)
	if !c.pausedSince.IsZero() {
		c.pausedAccum += time.Since(c.pausedSince)
		c.pausedSince = time.Time{}
	}
	c.cond.Broadcast()
	pids := make([]int, 0, len(c.activeProcs))
	eps := make([]*rcEndpoint, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.pid > 0 {
			pids = append(pids, ep.pid)
		}
		if ep.addr != "" {
			cp := *ep
			eps = append(eps, &cp)
		}
	}
	inherits := c.inheritsGlobal
	rcloneKeys := make([]int, 0, len(c.globalRcloneKeys))
	for _, k := range c.globalRcloneKeys {
		rcloneKeys = append(rcloneKeys, k)
	}
	goKeys := make([]int, 0, len(c.globalGoKeys))
	for _, k := range c.globalGoKeys {
		goKeys = append(goKeys, k)
	}
	shouldSyncRate := !inherits && (c.liveOverridden || (c.bwLimit != "" && c.standaloneActiveSlotCountLocked() > 1))
	spec := c.bwLimit
	totalSlots := max(1, c.standaloneActiveSlotCountLocked())
	c.mu.Unlock()
	var firstErr error
	for _, pid := range pids {
		if err := resumeProcess(pid); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if inherits {
		for _, k := range rcloneKeys {
			defaultGlobalBw.setRclonePaused(k, false)
		}
		for _, k := range goKeys {
			defaultGlobalBw.setGoPaused(k, false)
		}
	} else if shouldSyncRate && len(eps) > 0 {
		activeRate, _, _ := ResolveBwLimitAt(spec, time.Now())
		perSlotRate := splitBwRate(activeRate, totalSlots)
		go func() {
			time.Sleep(150 * time.Millisecond)
			for _, ep := range eps {
				_ = pushRcloneBwLimit(context.Background(), ep, perSlotRate)
			}
		}()
	}
	return firstErr
}

// Resume continues previously paused transfers.
func (c *RCController) Resume() error {
	if c == nil {
		return errors.New("任务不支持恢复")
	}
	c.mu.Lock()
	c.manualPaused = false
	if len(c.connectionPauses) > 0 || !c.paused.Load() {
		c.mu.Unlock()
		return nil
	}
	wasPaused := c.paused.Swap(false)
	if !c.pausedSince.IsZero() {
		c.pausedAccum += time.Since(c.pausedSince)
		c.pausedSince = time.Time{}
	}
	pids := make([]int, 0, len(c.activeProcs))
	eps := make([]*rcEndpoint, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.pid > 0 {
			pids = append(pids, ep.pid)
		}
		if ep.addr != "" {
			cp := *ep
			eps = append(eps, &cp)
		}
	}
	inherits := c.inheritsGlobal
	rcloneKeys := make([]int, 0, len(c.globalRcloneKeys))
	for _, k := range c.globalRcloneKeys {
		rcloneKeys = append(rcloneKeys, k)
	}
	goKeys := make([]int, 0, len(c.globalGoKeys))
	for _, k := range c.globalGoKeys {
		goKeys = append(goKeys, k)
	}
	shouldSyncRate := !inherits && (c.liveOverridden || (c.bwLimit != "" && c.standaloneActiveSlotCountLocked() > 1))
	spec := c.bwLimit
	totalSlots := max(1, c.standaloneActiveSlotCountLocked())
	c.cond.Broadcast()
	c.mu.Unlock()

	if !wasPaused {
		return nil
	}
	var firstErr error
	for _, pid := range pids {
		if err := resumeProcess(pid); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if inherits {
		for _, k := range rcloneKeys {
			defaultGlobalBw.setRclonePaused(k, false)
		}
		for _, k := range goKeys {
			defaultGlobalBw.setGoPaused(k, false)
		}
	} else if shouldSyncRate && len(eps) > 0 {
		activeRate, _, _ := ResolveBwLimitAt(spec, time.Now())
		perSlotRate := splitBwRate(activeRate, totalSlots)
		go func() {
			time.Sleep(150 * time.Millisecond)
			for _, ep := range eps {
				_ = pushRcloneBwLimit(context.Background(), ep, perSlotRate)
			}
		}()
	}
	return firstErr
}

// WaitIfPaused blocks pure-Go transfer workers while paused until resumed or ctx ends.
func (c *RCController) WaitIfPaused(ctx context.Context) error {
	if c == nil || !c.paused.Load() {
		return ctx.Err()
	}
	stop := context.AfterFunc(ctx, func() {
		c.mu.Lock()
		c.cond.Broadcast()
		c.mu.Unlock()
	})
	defer stop()

	c.mu.Lock()
	defer c.mu.Unlock()
	for c.paused.Load() && ctx.Err() == nil {
		c.cond.Wait()
	}
	return ctx.Err()
}

// SetBwLimit validates and applies a new per-task bandwidth limit live across
// all active rclone RC endpoints and Go resumable transfer workers. Calling this
// detaches a global-inheriting controller from the shared global pool so other
// tasks immediately reclaim its share.
func (c *RCController) SetBwLimit(ctx context.Context, rate string) error {
	if c == nil {
		return errors.New("任务不支持动态限速")
	}
	trimmed := strings.TrimSpace(rate)
	if err := ValidateBwLimit(trimmed); err != nil {
		return err
	}
	storedSpec := trimmed
	if storedSpec == "0" || strings.EqualFold(storedSpec, "off") {
		storedSpec = ""
	}
	now := time.Now()
	activeRate, _, _ := ResolveBwLimitAt(storedSpec, now)

	c.mu.Lock()
	wasInheriting := c.inheritsGlobal
	c.inheritsGlobal = false
	var detachRcloneKeys, detachGoKeys []int
	if wasInheriting {
		for _, k := range c.globalRcloneKeys {
			detachRcloneKeys = append(detachRcloneKeys, k)
		}
		clear(c.globalRcloneKeys)
		for _, k := range c.globalGoKeys {
			detachGoKeys = append(detachGoKeys, k)
		}
		clear(c.globalGoKeys)
	}
	c.bwLimit = storedSpec
	c.liveOverridden = true
	c.refreshLocalRateCacheLocked(now)
	eps := make([]*rcEndpoint, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.addr != "" {
			cp := *ep
			eps = append(eps, &cp)
		}
	}
	totalSlots := max(1, len(eps)+c.activeGoCount)
	isPaused := c.paused.Load()
	if !c.closed {
		c.armScheduleTimerLocked(now)
	}
	c.mu.Unlock()

	for _, k := range detachRcloneKeys {
		defaultGlobalBw.unregisterRclone(k)
	}
	for _, k := range detachGoKeys {
		defaultGlobalBw.unregisterGo(k)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	perSlotRate := splitBwRate(activeRate, totalSlots)
	var errs []error
	for _, ep := range eps {
		// If the process is currently SIGSTOP'd, skip unreachable RC and sync on Resume().
		if isPaused {
			continue
		}
		if err := pushRcloneBwLimit(ctx, ep, perSlotRate); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 && len(errs) == len(eps) {
		return errs[0]
	}
	return nil
}

func (c *RCController) armScheduleTimerLocked(now time.Time) {
	if c.scheduleTimer != nil {
		c.scheduleTimer.Stop()
		c.scheduleTimer = nil
	}
	if c.closed || c.inheritsGlobal || !c.liveOverridden {
		return
	}
	_, nextChange, hasSchedule := ResolveBwLimitAt(c.bwLimit, now)
	if !hasSchedule || nextChange.IsZero() {
		return
	}
	wait := time.Until(nextChange)
	if wait < time.Second {
		wait = time.Second
	}
	c.scheduleTimer = time.AfterFunc(wait, c.applyScheduledSlot)
}

func (c *RCController) applyScheduledSlot() {
	now := time.Now()
	c.mu.Lock()
	if c.closed || c.inheritsGlobal || !c.liveOverridden {
		c.mu.Unlock()
		return
	}
	c.refreshLocalRateCacheLocked(now)
	activeRate, _, _ := ResolveBwLimitAt(c.bwLimit, now)
	eps := make([]*rcEndpoint, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.addr != "" {
			cp := *ep
			eps = append(eps, &cp)
		}
	}
	totalSlots := max(1, len(eps)+c.activeGoCount)
	isPaused := c.paused.Load()
	c.armScheduleTimerLocked(now)
	c.mu.Unlock()

	if isPaused {
		return
	}
	perSlotRate := splitBwRate(activeRate, totalSlots)
	for _, ep := range eps {
		_ = pushRcloneBwLimit(context.Background(), ep, perSlotRate)
	}
}

// ThrottleBytes paces pure-Go resumable transfers to obey live bytesPerSec limits,
// dynamically re-evaluating scheduled timetables on minute boundaries.
func (c *RCController) ThrottleBytes(ctx context.Context, n int) error {
	return c.throttleDirected(ctx, n, throttleAny)
}

// ThrottleUploadBytes paces pure-Go upload workers using the upload rate cap
// (so asymmetric rates like "5.5M:off" throttle uploads without throttling downloads).
func (c *RCController) ThrottleUploadBytes(ctx context.Context, n int) error {
	return c.throttleDirected(ctx, n, throttleUpload)
}

// ThrottleDownloadBytes paces pure-Go download workers using the download rate cap
// (so asymmetric rates like "5.5M:off" leave downloads unlimited).
func (c *RCController) ThrottleDownloadBytes(ctx context.Context, n int) error {
	return c.throttleDirected(ctx, n, throttleDownload)
}

func (c *RCController) throttleDirected(ctx context.Context, n int, dir throttleDirection) error {
	if c == nil || n <= 0 {
		return ctx.Err()
	}
	c.mu.Lock()
	inherits := c.inheritsGlobal
	c.mu.Unlock()
	if inherits {
		return defaultGlobalBw.throttle(ctx, n, dir)
	}
	if c.hasSchedule.Load() {
		curMin := time.Now().Unix() / 60
		if c.lastEvalMin.Load() != curMin {
			c.mu.Lock()
			if c.lastEvalMin.Load() != curMin {
				c.refreshLocalRateCacheLocked(time.Now())
			}
			c.mu.Unlock()
		}
	}
	var bps int64
	switch dir {
	case throttleUpload:
		bps = c.upBytesPerSec.Load()
	case throttleDownload:
		bps = c.downBytesPerSec.Load()
	default:
		bps = c.bytesPerSec.Load()
	}
	if bps <= 0 {
		return ctx.Err()
	}
	// If this standalone controller also has concurrent rclone processes, share
	// its per-task cap proportionally with the Go workers.
	c.mu.Lock()
	rcActive := 0
	for _, ep := range c.activeProcs {
		if ep.addr != "" {
			rcActive++
		}
	}
	goActive := max(1, c.activeGoCount)
	c.mu.Unlock()
	if rcActive > 0 {
		bps = max(minSplitRateBytes, (bps*int64(goActive))/int64(rcActive+goActive))
	}
	dur := time.Duration(float64(n) / float64(bps) * float64(time.Second))
	if dur <= 0 {
		return ctx.Err()
	}
	c.throttleMu.Lock()
	var clock *time.Time
	switch dir {
	case throttleUpload:
		clock = &c.nextUpSendTime
	case throttleDownload:
		clock = &c.nextDownSendTime
	default:
		clock = &c.nextSendTime
	}
	now := time.Now()
	if clock.Before(now) {
		*clock = now
	}
	*clock = clock.Add(dur)
	wait := time.Until(*clock)
	c.throttleMu.Unlock()

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

func pushRcloneBwLimit(ctx context.Context, ep *rcEndpoint, rate string) error {
	if ep == nil || ep.addr == "" {
		return nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"rate": rate})
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://"+ep.addr+"/core/bwlimit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if ep.user != "" || ep.pass != "" {
		req.SetBasicAuth(ep.user, ep.pass)
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("实时限速请求失败：%w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rclone 实时限速返回状态码 %d", resp.StatusCode)
	}
	return nil
}

// ParseBwLimitBytesPerSec parses an rclone rate like "10M", "500K", "10M:2M"
// or a timetable like "08:00,5M 23:00,off" (evaluated at now) into bytes per second
// (0 = unlimited).
func ParseBwLimitBytesPerSec(rate string) int64 {
	return ParseBwLimitBytesPerSecAt(rate, time.Now())
}

func parseSingleRateBytes(spec string) int64 {
	s := strings.ToLower(strings.TrimSpace(spec))
	if s == "" || s == "off" || s == "0" {
		return 0
	}
	mult := float64(1024) // rclone --bwlimit bare numbers default to KiB/s
	hasUnit := false
	if strings.HasSuffix(s, "b") {
		if len(s) >= 2 && (s[len(s)-2] >= '0' && s[len(s)-2] <= '9') {
			mult = 1
			hasUnit = true
			s = s[:len(s)-1]
		} else {
			s = strings.TrimSuffix(s, "b")
		}
	}
	if !hasUnit {
		s = strings.TrimSuffix(s, "i")
		if len(s) > 0 {
			switch s[len(s)-1] {
			case 'b':
				mult = 1
				s = s[:len(s)-1]
			case 'k':
				mult = 1024
				s = s[:len(s)-1]
			case 'm':
				mult = 1024 * 1024
				s = s[:len(s)-1]
			case 'g':
				mult = 1024 * 1024 * 1024
				s = s[:len(s)-1]
			case 't':
				mult = 1024 * 1024 * 1024 * 1024
				s = s[:len(s)-1]
			case 'p':
				mult = 1024 * 1024 * 1024 * 1024 * 1024
				s = s[:len(s)-1]
			}
		}
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil || val <= 0 {
		return 0
	}
	return int64(val * mult)
}

func prepareRCArgsAndEnv() (addr, user, pass string, args []string, env []string, ok bool) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", "", "", nil, nil, false
	}
	addr = ln.Addr().String()
	_ = ln.Close()

	var ub [8]byte
	var pb [16]byte
	_, _ = rand.Read(ub[:])
	_, _ = rand.Read(pb[:])
	user = "osrc_" + hex.EncodeToString(ub[:])
	pass = hex.EncodeToString(pb[:])

	args = []string{"--rc", "--rc-addr", addr}
	env = []string{"RCLONE_RC_USER=" + user, "RCLONE_RC_PASS=" + pass}
	return addr, user, pass, args, env, true
}
