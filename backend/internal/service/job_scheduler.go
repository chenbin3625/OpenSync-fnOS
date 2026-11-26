package service

import (
	"errors"
	"fmt"
	"log"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"os"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

const defaultSchedulerTimeZone = "Asia/Shanghai"
const schedulerStopTimeout = 5 * time.Second

// maxJobIntervalMinutes caps interval schedules at 100 years (well below
// math.MaxInt64 / int64(time.Minute)) so time.Duration(interval)*time.Minute
// can never overflow into a non-positive duration and spin robfig/cron.
const maxJobIntervalMinutes int64 = 52560000

// intervalCatchUpDelay is how soon an overdue interval job runs after startup.
// The delay avoids starting every overdue job in the same second right after a
// restart (and after the engine becomes reachable).
const intervalCatchUpDelay = time.Minute

// intervalSchedule anchors a fixed-interval job to its last run instead of the
// moment the scheduler was created. @every restarts its countdown whenever the
// process restarts or the job is edited, so a job with a 24h interval could be
// postponed indefinitely by frequent restarts and never catch up. When the
// computed time is already in the past, the job runs once shortly after
// scheduling and then keeps the interval; missed intervals are not replayed one
// by one.
type intervalSchedule struct {
	interval time.Duration
	next     time.Time
	first    bool
}

func newIntervalSchedule(interval time.Duration, lastRun time.Time) *intervalSchedule {
	if interval <= 0 {
		interval = time.Minute
	}
	schedule := &intervalSchedule{interval: interval, first: true}
	if !lastRun.IsZero() {
		schedule.next = lastRun.Add(interval)
	}
	return schedule
}

// Next implements cron.Schedule.
func (schedule *intervalSchedule) Next(t time.Time) time.Time {
	interval := schedule.interval
	if interval <= 0 {
		interval = time.Minute
	}
	if schedule.first {
		schedule.first = false
		if !schedule.next.IsZero() {
			if schedule.next.After(t) {
				return schedule.next
			}
			return t.Add(intervalCatchUpDelay)
		}
	}
	return t.Add(interval)
}

// stopCronWithTimeout stops a cron scheduler and waits up to schedulerStopTimeout.
func stopCronWithTimeout(c *cron.Cron, label string) {
	ctx := c.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(schedulerStopTimeout):
		log.Printf("%s stop timed out after %s", label, schedulerStopTimeout)
	}
}

// Scheduler wraps robfig/cron for job scheduling
type Scheduler struct {
	cron    *cron.Cron
	entryID cron.EntryID
	mu      sync.Mutex
}

// NewScheduler creates a new scheduler
func NewScheduler() *Scheduler {
	c := cron.New(cron.WithSeconds(), cron.WithLocation(schedulerLocation()))
	c.Start()
	return &Scheduler{cron: c}
}

func newSchedulerStopped() *Scheduler {
	c := cron.New(cron.WithSeconds(), cron.WithLocation(schedulerLocation()))
	return &Scheduler{cron: c}
}

func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Start()
	}
}

func schedulerLocation() *time.Location {
	timeZone := strings.TrimSpace(os.Getenv("TZ"))
	if timeZone == "" {
		timeZone = defaultSchedulerTimeZone
	}

	loc, err := time.LoadLocation(timeZone)
	if err == nil {
		return loc
	}

	if timeZone != defaultSchedulerTimeZone {
		log.Printf("Failed to load timezone %q, falling back to %s: %v", timeZone, defaultSchedulerTimeZone, err)
	}
	loc, err = time.LoadLocation(defaultSchedulerTimeZone)
	if err == nil {
		return loc
	}

	log.Printf("Failed to load timezone %q, falling back to fixed UTC+8: %v", defaultSchedulerTimeZone, err)
	return time.FixedZone(defaultSchedulerTimeZone, 8*60*60)
}

// AddJob adds a scheduled job
// isCron: 0=interval, 1=cron, 2=manual only
func (s *Scheduler) AddJob(isCron int, jobData map[string]interface{}, fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if isCron == 2 || isCron == 3 {
		// Manual only, no scheduling
		return nil
	}

	entryID, err := s.addJobLocked(isCron, jobData, fn)
	if err != nil {
		return err
	}
	s.entryID = entryID

	// If disabled, remove the entry immediately (will be resumed later)
	enable := util.ToInt(jobData["enable"])
	if enable == 0 && s.entryID != 0 {
		s.cron.Remove(s.entryID)
		s.entryID = 0
	}

	return nil
}

// Pause pauses the scheduled job
func (s *Scheduler) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		return
	}
	if s.entryID != 0 {
		s.cron.Remove(s.entryID)
		s.entryID = 0
	}
}

// Resume resumes a paused job by re-adding it
func (s *Scheduler) Resume(isCron int, jobData map[string]interface{}, fn func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if isCron == 2 || isCron == 3 {
		return nil
	}
	if s.cron == nil {
		return errors.New(msg.T(msg.CannotResumeLostJob))
	}
	if s.entryID != 0 {
		return nil
	}

	entryID, err := s.addJobLocked(isCron, jobData, fn)
	if err != nil {
		return err
	}
	s.entryID = entryID
	return nil
}

func (s *Scheduler) addJobLocked(isCron int, jobData map[string]interface{}, fn func()) (cron.EntryID, error) {
	if s.cron == nil {
		return 0, errors.New(msg.T(msg.CannotResumeLostJob))
	}
	if isCron == 0 {
		interval := util.ToInt64(jobData["interval"])
		if interval <= 0 || interval > maxJobIntervalMinutes {
			return 0, errors.New(msg.T(msg.IntervalLost))
		}
		schedule := newIntervalSchedule(time.Duration(interval)*time.Minute, s.lastRunTime(jobData))
		return s.cron.Schedule(schedule, cron.FuncJob(fn)), nil
	}
	spec, err := buildCronSpec(jobData)
	if err != nil {
		return 0, err
	}
	if spec == "" {
		return 0, errors.New(msg.T(msg.CronLost))
	}
	entryID, err := s.cron.AddFunc(spec, fn)
	if err != nil {
		return 0, err
	}
	log.Printf("Built cron spec: %q", spec)
	return entryID, nil
}

// lastRunTime returns the start time of the job's most recent task (zero when
// it never ran or the lookup fails).
func (s *Scheduler) lastRunTime(jobData map[string]interface{}) time.Time {
	jobID := util.ToInt64(jobData["id"])
	if jobID <= 0 || jobDeps == nil || jobDeps.LastJobTaskCreateTime == nil {
		return time.Time{}
	}
	unix, err := jobDeps.LastJobTaskCreateTime(jobID)
	if err != nil {
		log.Printf("Failed to read the last run time of job %d; scheduling from now: %v", jobID, err)
		return time.Time{}
	}
	if unix <= 0 {
		return time.Time{}
	}
	return time.Unix(unix, 0)
}

// Next reports when the scheduled entry will next run. ok is false for manual
// jobs, disabled jobs, or a stopped scheduler.
func (s *Scheduler) Next() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil || s.entryID == 0 {
		return time.Time{}, false
	}
	entry := s.cron.Entry(s.entryID)
	if entry.ID == 0 || entry.Next.IsZero() {
		return time.Time{}, false
	}
	return entry.Next, true
}

// Stop shuts down the scheduler
func (s *Scheduler) Stop() {
	s.mu.Lock()
	c := s.cron
	s.cron = nil
	s.entryID = 0
	s.mu.Unlock()

	if c == nil {
		return
	}
	stopCronWithTimeout(c, "scheduler")
}

// buildCronSpec builds a cron expression from job data
// Format: second minute hour day month dayOfWeek (robfig/cron with seconds)
func buildCronSpec(jobData map[string]interface{}) (string, error) {
	fields := []string{"second", "minute", "hour", "day", "month", "day_of_week"}
	parts := make([]string, 6)
	hasValue := false

	for i, field := range fields {
		val := ""
		if v, ok := jobData[field]; ok && v != nil {
			val = strings.TrimSpace(fmt.Sprintf("%v", v))
		}
		if val == "" {
			// An empty second would become "*" and fire every second; the UI
			// always sends one, so an API caller leaving it out means "on the
			// minute". It does not count as a value: an all-empty spec stays
			// rejected as missing.
			if field == "second" {
				parts[i] = "0"
			} else {
				parts[i] = "*"
			}
		} else {
			if !isSafeCronField(val) {
				return "", errors.New(msg.T(msg.CronLost))
			}
			parts[i] = val
			if field != "second" || val != "0" {
				hasValue = true
			}
		}
	}

	if !hasValue {
		return "", nil
	}

	// robfig/cron format: second minute hour dayOfMonth month dayOfWeek.
	// day_of_week is a free-text cron field on the frontend, so users enter
	// cron-standard values directly (0=Sunday..6=Saturday) and no conversion
	// is needed. The values are passed through unchanged.
	return strings.Join(parts, " "), nil
}

var jobCronParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func validateCronSpec(spec string) error {
	_, err := jobCronParser.Parse(spec)
	return err
}

func isSafeCronField(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '*', '/', ',', '-':
			continue
		default:
			return false
		}
	}
	return true
}
