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
	mu             sync.Mutex
	cond           *sync.Cond
	paused         atomic.Bool
	pausedSince    time.Time
	pausedAccum    time.Duration
	bwLimit        string
	liveOverridden bool
	hasSchedule    atomic.Bool
	lastEvalMin    atomic.Int64
	bytesPerSec    atomic.Int64
	activeProcs    map[int]*rcEndpoint
	nextProcKey    int
	scheduleTimer  *time.Timer
	closed         bool
	throttleMu     sync.Mutex
	nextSendTime   time.Time
}

type rcEndpoint struct {
	pid  int
	addr string
	user string
	pass string
}

// NewRCController creates a live transfer controller initialized with initialBwLimit.
func NewRCController(initialBwLimit string) *RCController {
	c := &RCController{
		bwLimit:     strings.TrimSpace(initialBwLimit),
		activeProcs: make(map[int]*rcEndpoint),
	}
	c.cond = sync.NewCond(&c.mu)
	now := time.Now()
	activeRate, _, hasSchedule := ResolveBwLimitAt(c.bwLimit, now)
	c.hasSchedule.Store(hasSchedule)
	c.lastEvalMin.Store(now.Unix() / 60)
	c.bytesPerSec.Store(parseEffectiveSingleRateBytes(activeRate))
	return c
}

// Close releases background timetable timers owned by this controller.
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
	c.mu.Unlock()
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
	defer c.mu.Unlock()
	return c.bwLimit
}

// Attach registers a running rclone process and its private RC endpoint.
// The returned detach function must be called once the process exits.
func (c *RCController) Attach(pid int, rcAddr, rcUser, rcPass string) func() {
	if c == nil {
		return func() {}
	}
	c.mu.Lock()
	c.nextProcKey++
	key := c.nextProcKey
	ep := &rcEndpoint{pid: pid, addr: rcAddr, user: rcUser, pass: rcPass}
	c.activeProcs[key] = ep
	shouldPause := c.paused.Load()
	shouldPushLive := c.liveOverridden
	spec := c.bwLimit
	if shouldPushLive && !c.closed {
		c.armScheduleTimerLocked(time.Now())
	}
	c.mu.Unlock()

	if shouldPause && pid > 0 {
		_ = pauseProcess(pid)
	}
	if shouldPushLive && rcAddr != "" && !shouldPause {
		activeRate, _, _ := ResolveBwLimitAt(spec, time.Now())
		go func() {
			// Give rclone's RC HTTP listener a brief moment to bind before pushing the live rate.
			time.Sleep(200 * time.Millisecond)
			_ = pushRcloneBwLimit(context.Background(), ep, activeRate)
		}()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.activeProcs, key)
			c.mu.Unlock()
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
	if c.paused.Swap(true) {
		c.mu.Unlock()
		return nil
	}
	c.pausedSince = time.Now()
	pids := make([]int, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.pid > 0 {
			pids = append(pids, ep.pid)
		}
	}
	c.mu.Unlock()

	var firstErr error
	for _, pid := range pids {
		if err := pauseProcess(pid); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Resume continues previously paused transfers.
func (c *RCController) Resume() error {
	if c == nil {
		return errors.New("任务不支持恢复")
	}
	c.mu.Lock()
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
	shouldSyncRate := c.liveOverridden
	spec := c.bwLimit
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
	if shouldSyncRate && len(eps) > 0 {
		activeRate, _, _ := ResolveBwLimitAt(spec, time.Now())
		go func() {
			time.Sleep(150 * time.Millisecond)
			for _, ep := range eps {
				_ = pushRcloneBwLimit(context.Background(), ep, activeRate)
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

// SetBwLimit validates and applies a new bandwidth limit live across all active
// rclone RC endpoints and Go resumable transfer workers. Supports single rates,
// asymmetric up:down limits, and full rclone timetables; empty/"off"/"0" removes the cap.
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
	activeRate, _, hasSchedule := ResolveBwLimitAt(storedSpec, now)

	c.mu.Lock()
	c.bwLimit = storedSpec
	c.liveOverridden = true
	c.hasSchedule.Store(hasSchedule)
	c.lastEvalMin.Store(now.Unix() / 60)
	c.bytesPerSec.Store(parseEffectiveSingleRateBytes(activeRate))
	eps := make([]*rcEndpoint, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.addr != "" {
			cp := *ep
			eps = append(eps, &cp)
		}
	}
	isPaused := c.paused.Load()
	if !c.closed {
		c.armScheduleTimerLocked(now)
	}
	c.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	var errs []error
	for _, ep := range eps {
		// If the process is currently SIGSTOP'd, skip unreachable RC and sync on Resume().
		if isPaused {
			continue
		}
		if err := pushRcloneBwLimit(ctx, ep, activeRate); err != nil {
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
	if c.closed || !c.liveOverridden {
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
	if c.closed || !c.liveOverridden {
		c.mu.Unlock()
		return
	}
	activeRate, _, hasSchedule := ResolveBwLimitAt(c.bwLimit, now)
	c.hasSchedule.Store(hasSchedule)
	c.lastEvalMin.Store(now.Unix() / 60)
	c.bytesPerSec.Store(parseEffectiveSingleRateBytes(activeRate))
	eps := make([]*rcEndpoint, 0, len(c.activeProcs))
	for _, ep := range c.activeProcs {
		if ep.addr != "" {
			cp := *ep
			eps = append(eps, &cp)
		}
	}
	isPaused := c.paused.Load()
	c.armScheduleTimerLocked(now)
	c.mu.Unlock()

	if isPaused {
		return
	}
	for _, ep := range eps {
		_ = pushRcloneBwLimit(context.Background(), ep, activeRate)
	}
}

// ThrottleBytes paces pure-Go resumable transfers to obey live bytesPerSec limits,
// dynamically re-evaluating scheduled timetables on minute boundaries.
func (c *RCController) ThrottleBytes(ctx context.Context, n int) error {
	if c == nil || n <= 0 {
		return ctx.Err()
	}
	if c.hasSchedule.Load() {
		curMin := time.Now().Unix() / 60
		if c.lastEvalMin.Load() != curMin {
			c.mu.Lock()
			if c.lastEvalMin.Load() != curMin {
				now := time.Now()
				activeRate, _, _ := ResolveBwLimitAt(c.bwLimit, now)
				c.bytesPerSec.Store(parseEffectiveSingleRateBytes(activeRate))
				c.lastEvalMin.Store(curMin)
			}
			c.mu.Unlock()
		}
	}
	bps := c.bytesPerSec.Load()
	if bps <= 0 {
		return ctx.Err()
	}
	dur := time.Duration(float64(n) / float64(bps) * float64(time.Second))
	if dur <= 0 {
		return ctx.Err()
	}
	c.throttleMu.Lock()
	now := time.Now()
	if c.nextSendTime.Before(now) {
		c.nextSendTime = now
	}
	c.nextSendTime = c.nextSendTime.Add(dur)
	wait := time.Until(c.nextSendTime)
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
