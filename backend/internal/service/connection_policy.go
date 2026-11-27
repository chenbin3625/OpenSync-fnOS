package service

import (
	"context"
	"encoding/json"
	"fmt"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NativeConnectionPolicy is the OpenSync-owned policy attached to one named
// native remote. Zero values retain the historical task/system defaults.
type NativeConnectionPolicy struct {
	PollIntervalSec int                   `json:"pollIntervalSec"`
	WeeklySchedule  *WeeklyScheduleConfig `json:"weeklySchedule,omitempty"`
	BwLimit         string                `json:"bwLimit"`
	Paused          bool                  `json:"paused"`
}

// NativeConnectionPolicyInput replaces the policy for one named remote. The
// endpoint is intentionally separate from remote credential editing so a
// policy save cannot accidentally rewrite secrets.
type NativeConnectionPolicyInput struct {
	Name            string                `json:"name"`
	PollIntervalSec int                   `json:"pollIntervalSec"`
	WeeklySchedule  *WeeklyScheduleConfig `json:"weeklySchedule"`
	BwLimit         string                `json:"bwLimit"`
	Paused          bool                  `json:"paused"`
}

func parseConnectionPolicy(params map[string]string) (NativeConnectionPolicy, error) {
	var out NativeConnectionPolicy
	if params == nil {
		return out, nil
	}
	if raw := strings.TrimSpace(params[native.ConnectionPollIntervalKey]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || (n != 0 && (n < 10 || n > 86400)) {
			return out, fmt.Errorf("连接轮询周期需为 10–86400 秒，0 表示跟随默认值")
		}
		out.PollIntervalSec = n
	}
	if raw := strings.TrimSpace(params[native.ConnectionWeeklyScheduleKey]); raw != "" {
		var value interface{}
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return out, fmt.Errorf("连接周历配置格式无效")
		}
		ws, err := parseWeeklySchedule(value)
		if err != nil {
			return out, err
		}
		out.WeeklySchedule = ws
	}
	if raw := strings.TrimSpace(params[native.ConnectionBwLimitKey]); raw != "" {
		if err := native.ValidateBwLimit(raw); err != nil {
			return out, err
		}
		out.BwLimit = raw
	}
	out.Paused = strings.EqualFold(strings.TrimSpace(params[native.ConnectionPausedKey]), "true") || params[native.ConnectionPausedKey] == "1"
	return out, nil
}

func encodeConnectionPolicy(params map[string]string, policy NativeConnectionPolicy) error {
	if params == nil {
		return fmt.Errorf("连接配置为空")
	}
	delete(params, native.ConnectionPollIntervalKey)
	delete(params, native.ConnectionWeeklyScheduleKey)
	delete(params, native.ConnectionBwLimitKey)
	delete(params, native.ConnectionPausedKey)
	if policy.PollIntervalSec != 0 {
		params[native.ConnectionPollIntervalKey] = strconv.Itoa(policy.PollIntervalSec)
	}
	if policy.WeeklySchedule != nil {
		encoded, err := json.Marshal(policy.WeeklySchedule)
		if err != nil {
			return err
		}
		params[native.ConnectionWeeklyScheduleKey] = string(encoded)
	}
	if strings.TrimSpace(policy.BwLimit) != "" {
		params[native.ConnectionBwLimitKey] = strings.TrimSpace(policy.BwLimit)
	}
	if policy.Paused {
		params[native.ConnectionPausedKey] = "true"
	}
	return nil
}

func validateConnectionPolicy(policy NativeConnectionPolicy) error {
	if policy.PollIntervalSec != 0 && (policy.PollIntervalSec < 10 || policy.PollIntervalSec > 86400) {
		return fmt.Errorf("连接轮询周期需为 10–86400 秒，0 表示跟随默认值")
	}
	if policy.BwLimit != "" {
		if err := native.ValidateBwLimit(policy.BwLimit); err != nil {
			return err
		}
	}
	if policy.WeeklySchedule != nil {
		encoded, err := json.Marshal(policy.WeeklySchedule)
		if err != nil {
			return fmt.Errorf("连接周历配置格式无效")
		}
		var raw interface{}
		if err := json.Unmarshal(encoded, &raw); err != nil {
			return fmt.Errorf("连接周历配置格式无效")
		}
		if _, err := parseWeeklySchedule(raw); err != nil {
			return err
		}
	}
	return nil
}

func connectionPolicyForConfig(cfg native.Config, name string) (NativeConnectionPolicy, string, bool, error) {
	if cfg == nil || strings.EqualFold(name, native.Local) {
		return NativeConnectionPolicy{}, "", false, nil
	}
	canonical, ok := cfg.FindKey(name)
	if !ok {
		return NativeConnectionPolicy{}, "", false, nil
	}
	policy, err := parseConnectionPolicy(cfg[canonical])
	return policy, canonical, policy.BwLimit != "" || policy.Paused, err
}

func connectionPoliciesForJob(job map[string]interface{}, cfg native.Config) map[string]NativeConnectionPolicy {
	policies := make(map[string]NativeConnectionPolicy)
	if job == nil || cfg == nil {
		return policies
	}
	for _, name := range extractJobStorageNamesWithDeps(job, cfg) {
		policy, canonical, active, err := connectionPolicyForConfig(cfg, name)
		if err != nil || !active {
			continue
		}
		policies[strings.ToLower(canonical)] = policy
	}
	return policies
}

func connectionPolicyDefaultForJob(job map[string]interface{}, cfg native.Config) NativeConnectionPolicy {
	var out NativeConnectionPolicy
	if job == nil || cfg == nil {
		return out
	}
	names := extractJobStorageNamesWithDeps(job, cfg)
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	for _, name := range names {
		policy, _, _, err := connectionPolicyForConfig(cfg, name)
		if err != nil {
			continue
		}
		if out.PollIntervalSec == 0 && policy.PollIntervalSec > 0 {
			out.PollIntervalSec = policy.PollIntervalSec
		}
		if out.WeeklySchedule == nil && policy.WeeklySchedule != nil {
			out.WeeklySchedule = policy.WeeklySchedule
		}
	}
	return out
}

// applyConnectionDefaults copies inherited poll/week-calendar defaults into a
// runtime job snapshot. Persisted task extraFlags remain untouched, so a later
// policy change immediately affects jobs whose fields are still explicit-zero.
func applyConnectionDefaults(job map[string]interface{}, cfg native.Config) map[string]interface{} {
	if job == nil || cfg == nil {
		return job
	}
	policy := connectionPolicyDefaultForJob(job, cfg)
	if policy.PollIntervalSec == 0 && policy.WeeklySchedule == nil {
		return job
	}
	extra, _, err := parseNativeExtraFlags(job["extraFlags"])
	if err != nil {
		return job
	}
	if extra.SyncPollIntervalSec == 0 && policy.PollIntervalSec > 0 {
		extra.SyncPollIntervalSec = policy.PollIntervalSec
	}
	if extra.WeeklySchedule == nil && policy.WeeklySchedule != nil {
		extra.WeeklySchedule = policy.WeeklySchedule
	}
	if extra.isZero() {
		return job
	}
	canonical, err := json.Marshal(extra)
	if err != nil {
		return job
	}
	copy := cloneJobConfig(job)
	copy["extraFlags"] = string(canonical)
	if extra.WeeklySchedule != nil && policy.WeeklySchedule != nil {
		if original, _, _ := parseNativeExtraFlags(job["extraFlags"]); original.WeeklySchedule == nil {
			copy["__connectionWeeklyInherited"] = true
		}
	}
	return copy
}

type connectionBandwidthGroup struct {
	mu     sync.Mutex
	key    string
	spec   string
	system string
	paused bool
	tasks  map[*JobTask]struct{}
	timer  *time.Timer
}

func newConnectionBandwidthGroup(key string, policy NativeConnectionPolicy) *connectionBandwidthGroup {
	return &connectionBandwidthGroup{
		key:    key,
		spec:   policy.BwLimit,
		paused: policy.Paused,
		tasks:  make(map[*JobTask]struct{}),
	}
}

func (g *connectionBandwidthGroup) setPolicy(policy NativeConnectionPolicy) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.spec = strings.TrimSpace(policy.BwLimit)
	g.paused = policy.Paused
	g.armTimerLocked()
	tasks := make([]*JobTask, 0, len(g.tasks))
	for task := range g.tasks {
		tasks = append(tasks, task)
	}
	g.mu.Unlock()
	g.rebalance(tasks)
}

func (g *connectionBandwidthGroup) setSystemLimit(rate string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.system = strings.TrimSpace(rate)
	tasks := make([]*JobTask, 0, len(g.tasks))
	for task := range g.tasks {
		tasks = append(tasks, task)
	}
	g.mu.Unlock()
	g.rebalance(tasks)
}

func (g *connectionBandwidthGroup) register(task *JobTask) func() {
	if g == nil || task == nil {
		return func() {}
	}
	g.mu.Lock()
	g.tasks[task] = struct{}{}
	g.armTimerLocked()
	tasks := make([]*JobTask, 0, len(g.tasks))
	for item := range g.tasks {
		tasks = append(tasks, item)
	}
	g.mu.Unlock()
	g.rebalance(tasks)
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.tasks, task)
			tasks := make([]*JobTask, 0, len(g.tasks))
			for item := range g.tasks {
				tasks = append(tasks, item)
			}
			g.mu.Unlock()
			task.clearConnectionBwLimit(g.key)
			g.rebalance(tasks)
		})
	}
}

func (g *connectionBandwidthGroup) armTimerLocked() {
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	active, next, scheduled := native.ResolveBwLimitAt(g.spec, time.Now())
	if g.system != "" {
		_, systemNext, systemScheduled := native.ResolveBwLimitAt(g.system, time.Now())
		if !scheduled || (systemScheduled && (next.IsZero() || (!systemNext.IsZero() && systemNext.Before(next)))) {
			next = systemNext
		}
		scheduled = scheduled || systemScheduled
		_ = active
	}
	if !scheduled || next.IsZero() {
		return
	}
	wait := time.Until(next)
	if wait < time.Second {
		wait = time.Second
	}
	g.timer = time.AfterFunc(wait, func() {
		g.mu.Lock()
		tasks := make([]*JobTask, 0, len(g.tasks))
		for task := range g.tasks {
			tasks = append(tasks, task)
		}
		g.armTimerLocked()
		g.mu.Unlock()
		g.rebalance(tasks)
	})
}

func (g *connectionBandwidthGroup) rebalance(tasks []*JobTask) {
	if g == nil {
		return
	}
	g.mu.Lock()
	spec, system, paused := g.spec, g.system, g.paused
	g.mu.Unlock()
	activeTasks := 0
	for _, task := range tasks {
		if task != nil && !task.isBreak() && !task.isPaused() {
			activeTasks++
		}
	}
	if activeTasks == 0 {
		activeTasks = 1
	}
	perTask := ""
	if spec != "" {
		rate := minBandwidthRate(spec, system, time.Now())
		perTask = native.SplitBwRateForShare(rate, activeTasks)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, task := range tasks {
		if task == nil {
			continue
		}
		_ = task.applyConnectionBwLimit(ctx, g.key, perTask)
		if paused {
			_ = task.rcController.PauseForConnection(g.key)
		} else {
			_ = task.rcController.ResumeForConnection(g.key)
		}
	}
}

func minBandwidthRate(first, second string, now time.Time) string {
	first = strings.TrimSpace(first)
	second = strings.TrimSpace(second)
	if first == "" {
		if second == "" {
			return ""
		}
		first = second
	} else if second == "" {
		return native.ResolveBwLimitValue(first, now)
	}
	left := native.ResolveBwLimitValue(first, now)
	right := native.ResolveBwLimitValue(second, now)
	if left == "off" {
		return right
	}
	if right == "off" {
		return left
	}
	if native.ParseBwLimitBytesPerSec(left) <= native.ParseBwLimitBytesPerSec(right) {
		return left
	}
	return right
}

var connectionGroups = struct {
	sync.Mutex
	items map[string]*connectionBandwidthGroup
}{items: make(map[string]*connectionBandwidthGroup)}

func getConnectionBandwidthGroup(name string, policy NativeConnectionPolicy) *connectionBandwidthGroup {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" || (policy.BwLimit == "" && !policy.Paused) {
		return nil
	}
	connectionGroups.Lock()
	g := connectionGroups.items[key]
	if g == nil {
		g = newConnectionBandwidthGroup(key, policy)
		g.system = native.GetGlobalBwLimit()
		connectionGroups.items[key] = g
		connectionGroups.Unlock()
		return g
	}
	connectionGroups.Unlock()
	g.mu.Lock()
	g.spec = strings.TrimSpace(policy.BwLimit)
	g.paused = policy.Paused
	g.mu.Unlock()
	return g
}

func refreshConnectionPolicyJobs(name string, cfg native.Config, resumed bool) {
	targetKey := strings.ToLower(strings.TrimSpace(name))
	if targetKey == "" || cfg == nil {
		return
	}
	jobClientListMu.RLock()
	clients := make([]*JobClient, 0, len(jobClientList))
	for _, client := range jobClientList {
		clients = append(clients, client)
	}
	jobClientListMu.RUnlock()

	for _, client := range clients {
		if client == nil || client.isClosing() {
			continue
		}
		jobID := client.idSnapshot()
		rawJob, err := mapper.GetJobByID(jobID)
		if err != nil || rawJob == nil {
			continue
		}
		matched := false
		for _, dep := range extractJobStorageNamesWithDeps(rawJob, cfg) {
			if strings.ToLower(strings.TrimSpace(dep)) == targetKey {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		client.replaceJobConfig(rawJob, client.schedulerSnapshot())
		if util.ToInt(rawJob["enable"]) == 1 {
			jobFSWatchers.SyncJob(client)
			if resumed && !connectionPolicyUsesPausedRemote(rawJob, cfg) {
				jobFSWatchers.ResetPollBackoff(jobID)
			}
		}
	}
}

func updateConnectionBandwidthGroup(name string, policy NativeConnectionPolicy) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return
	}
	connectionGroups.Lock()
	g := connectionGroups.items[key]
	if g == nil && (policy.BwLimit != "" || policy.Paused) {
		g = newConnectionBandwidthGroup(key, policy)
		g.system = native.GetGlobalBwLimit()
		connectionGroups.items[key] = g
	}
	connectionGroups.Unlock()
	if g != nil {
		g.setPolicy(policy)
		jobClientListMu.RLock()
		clients := make([]*JobClient, 0, len(jobClientList))
		for _, client := range jobClientList {
			clients = append(clients, client)
		}
		jobClientListMu.RUnlock()
		for _, client := range clients {
			if client == nil {
				continue
			}
			task := client.currentTask()
			if task == nil {
				continue
			}
			cfg, cfgErr := loadNative(util.ToInt64(task.Job["alistId"]))
			if cfgErr != nil {
				continue
			}
			for key := range connectionPoliciesForJob(task.Job, cfg) {
				if key == strings.ToLower(strings.TrimSpace(name)) {
					task.attachConnectionGroup(g)
					break
				}
			}
		}
	}
}

func applyConnectionGroupsSystemLimit(rate string) {
	connectionGroups.Lock()
	groups := make([]*connectionBandwidthGroup, 0, len(connectionGroups.items))
	for _, group := range connectionGroups.items {
		groups = append(groups, group)
	}
	connectionGroups.Unlock()
	for _, group := range groups {
		group.setSystemLimit(rate)
	}
}

func connectionPolicyStatusForRemote(item *NativeRemoteItem, policy NativeConnectionPolicy) {
	if item == nil {
		return
	}
	item.PollIntervalSec = policy.PollIntervalSec
	item.WeeklySchedule = policy.WeeklySchedule
	item.BwLimit = policy.BwLimit
	item.Paused = policy.Paused
}

func connectionPolicyUsesPausedRemote(job map[string]interface{}, cfg native.Config) bool {
	for _, policy := range connectionPoliciesForJob(job, cfg) {
		if policy.Paused {
			return true
		}
	}
	return false
}

func connectionPolicyPausedForJob(job map[string]interface{}) bool {
	if job == nil {
		return false
	}
	cfg, err := loadNative(util.ToInt64(job["alistId"]))
	return err == nil && connectionPolicyUsesPausedRemote(job, cfg)
}
