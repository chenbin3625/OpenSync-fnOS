package service

import (
	"context"
	"fmt"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
	"strings"
	"sync"
	"time"
)

// EngineStorageStatus describes one local or remote storage reported by the built-in engine.
type EngineStorageStatus struct {
	MountPath string `json:"mountPath"`
	Driver    string `json:"driver"`
	Status    string `json:"status"`
	Disabled  bool   `json:"disabled"`
	Remark    string `json:"remark"`
}

// EngineHealth holds the latest health probe outcome for the storage engine.
type EngineHealth struct {
	AlistID       int64                 `json:"alistId"`
	Status        string                `json:"status"` // "unknown" | "online" | "offline"
	LatencyMs     int64                 `json:"latencyMs"`
	LastCheckedAt int64                 `json:"lastCheckedAt"`
	Error         string                `json:"error,omitempty"`
	Storages      []EngineStorageStatus `json:"storages"`
	StoragesKnown bool                  `json:"storagesKnown"`
}

var (
	engineHealthMu      sync.RWMutex
	engineHealthCache   = make(map[int64]EngineHealth)
	engineHealthStopCh  chan struct{}
	engineHealthDoneCh  chan struct{}
	engineHealthStarted bool
	engineHealthLastRun time.Time
)

func defaultEngineHealth(alistID int64) EngineHealth {
	return EngineHealth{
		AlistID:  alistID,
		Status:   "unknown",
		Storages: []EngineStorageStatus{},
	}
}

// GetEngineHealth returns the cached health snapshot for alistID.
func GetEngineHealth(alistID int64) EngineHealth {
	engineHealthMu.RLock()
	h, ok := engineHealthCache[alistID]
	engineHealthMu.RUnlock()
	if !ok {
		return defaultEngineHealth(alistID)
	}
	if h.Storages == nil {
		h.Storages = []EngineStorageStatus{}
	}
	return h
}

// RemoveEngineHealth invalidates health and quota snapshots after engine changes.
func RemoveEngineHealth(alistID int64) {
	engineHealthMu.Lock()
	delete(engineHealthCache, alistID)
	engineHealthMu.Unlock()
	invalidateDashboardStorageQuotas()
}

func isStorageHealthy(st EngineStorageStatus) bool {
	if st.Disabled {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(st.Status))
	return status == "" || status == "work" || status == "ok" || status == "healthy"
}

func updateEngineHealthCache(alistID int64, name, engineURL string, next EngineHealth, sendAlert bool) {
	if next.Storages == nil {
		next.Storages = []EngineStorageStatus{}
	}
	engineHealthMu.Lock()
	prev, hadPrev := engineHealthCache[alistID]
	engineHealthCache[alistID] = next
	engineHealthMu.Unlock()

	if !sendAlert || !hadPrev {
		return
	}

	displayName := strings.TrimSpace(name)
	if displayName == "" {
		displayName = engineURL
	}

	// Online -> Offline transition
	if prev.Status == "online" && next.Status == "offline" {
		title := fmt.Sprintf("OpenSync - %s: %s", displayName, msg.T(msg.NotifyEngineOfflineTitle))
		content := buildEngineAlertContent(displayName, engineURL, next.Error, "offline")
		QueueSystemAlertNotification("engine_alert", title, content, map[string]string{
			"engineName": displayName,
			"engineUrl":  engineURL,
			"status":     "offline",
			"detail":     next.Error,
		})
		return
	}

	// Offline -> Online recovery
	if prev.Status == "offline" && next.Status == "online" {
		title := fmt.Sprintf("OpenSync - %s: %s", displayName, msg.T(msg.NotifyEngineOnlineTitle))
		content := buildEngineAlertContent(displayName, engineURL, fmt.Sprintf("%dms", next.LatencyMs), "online")
		QueueSystemAlertNotification("engine_alert", title, content, map[string]string{
			"engineName": displayName,
			"engineUrl":  engineURL,
			"status":     "online",
			"detail":     fmt.Sprintf("latency %dms", next.LatencyMs),
		})
		return
	}

	// Newly broken storage mount detection
	if next.Status == "online" && next.StoragesKnown {
		prevErrMounts := make(map[string]struct{})
		for _, st := range prev.Storages {
			if !isStorageHealthy(st) {
				prevErrMounts[st.MountPath] = struct{}{}
			}
		}
		var newlyBroken []string
		for _, st := range next.Storages {
			if !isStorageHealthy(st) {
				if _, already := prevErrMounts[st.MountPath]; !already {
					newlyBroken = append(newlyBroken, fmt.Sprintf("%s (%s)", st.MountPath, st.Status))
				}
			}
		}
		if len(newlyBroken) > 0 {
			detail := strings.Join(newlyBroken, "; ")
			title := fmt.Sprintf("OpenSync - %s: %s", displayName, msg.T(msg.NotifyEngineStorageErrTitle))
			content := buildEngineAlertContent(displayName, engineURL, detail, "storage_error")
			QueueSystemAlertNotification("engine_alert", title, content, map[string]string{
				"engineName": displayName,
				"engineUrl":  engineURL,
				"status":     "storage_error",
				"detail":     detail,
			})
		}
	}
}

func buildEngineAlertContent(name, engineURL, detail, kind string) string {
	if msg.Locale() == "en" {
		switch kind {
		case "online":
			return fmt.Sprintf("Storage Engine: %s\nAddress: %s\nStatus: Recovered Online (Latency: %s)", name, engineURL, detail)
		case "storage_error":
			return fmt.Sprintf("Storage Engine: %s\nAddress: %s\nUnhealthy Storages: %s", name, engineURL, detail)
		default:
			return fmt.Sprintf("Storage Engine: %s\nAddress: %s\nStatus: Offline\nReason: %s", name, engineURL, detail)
		}
	}
	switch kind {
	case "online":
		return fmt.Sprintf("存储引擎：%s\n服务地址：%s\n当前状态：已恢复在线（响应延迟 %s）", name, engineURL, detail)
	case "storage_error":
		return fmt.Sprintf("存储引擎：%s\n服务地址：%s\n异常存储：%s", name, engineURL, detail)
	default:
		return fmt.Sprintf("存储引擎：%s\n服务地址：%s\n当前状态：连接异常\n原因：%s", name, engineURL, detail)
	}
}

func probeEngineRow(ctx context.Context, alist map[string]interface{}, sendAlert bool) *EngineHealth {
	alistID := util.ToInt64(alist["id"])
	urlStr := util.StringValue(alist["url"])
	if urlStr != native.EngineURL {
		h := EngineHealth{
			AlistID:       alistID,
			Status:        "offline",
			LastCheckedAt: time.Now().Unix(),
			Error:         "旧版 OpenList 引擎已不再支持",
			Storages:      []EngineStorageStatus{},
			StoragesKnown: false,
		}
		updateEngineHealthCache(alistID, util.StringValue(alist["remark"]), urlStr, h, false)
		return &h
	}
	probe, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return nativeHealth(probe, alist, sendAlert)
}

// CheckEngineHealth probes one engine immediately and updates the cache.
func CheckEngineHealth(ctx context.Context, alistID int64) (*EngineHealth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if alistID <= 0 {
		id, err := EnsureNativeEngine()
		if err != nil {
			return nil, err
		}
		alistID = id
	}
	alist, err := alistDeps.GetAlistByID(alistID)
	if err != nil {
		return nil, publicErrorIf(err, msg.T(msg.AlistNotFound))
	}
	return probeEngineRow(ctx, alist, false), nil
}

// CheckAllEnginesHealth probes the built-in rclone engine and all configured storages.
func CheckAllEnginesHealth(ctx context.Context) ([]*EngineHealth, error) {
	return checkAllEnginesHealthInternal(ctx, false)
}

func checkAllEnginesHealthInternal(ctx context.Context, sendAlert bool) ([]*EngineHealth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := EnsureNativeEngine(); err != nil {
		return nil, fmt.Errorf("ensure native engine for health check: %w", err)
	}
	list, err := mapper.GetAlistList()
	if err != nil {
		return nil, fmt.Errorf("get alist list for health check: %w", err)
	}
	nativeRows := make([]map[string]interface{}, 0, 1)
	for _, row := range list {
		if util.StringValue(row["url"]) == native.EngineURL {
			nativeRows = append(nativeRows, row)
		}
	}
	results := make([]*EngineHealth, len(nativeRows))
	for i, row := range nativeRows {
		if ctx.Err() != nil {
			break
		}
		results[i] = probeEngineRow(ctx, row, sendAlert)
	}
	return results, nil
}

// StartEngineHealthChecker starts the periodic background engine health monitor.
func StartEngineHealthChecker() {
	engineHealthMu.Lock()
	if engineHealthStarted {
		engineHealthMu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	engineHealthStopCh = stopCh
	engineHealthDoneCh = doneCh
	engineHealthStarted = true
	engineHealthLastRun = time.Time{}
	engineHealthMu.Unlock()

	go runEngineHealthCheckerLoop(stopCh, doneCh)
}

// StopEngineHealthChecker stops the background engine health monitor gracefully.
func StopEngineHealthChecker() {
	engineHealthMu.Lock()
	if !engineHealthStarted {
		engineHealthMu.Unlock()
		return
	}
	stopCh := engineHealthStopCh
	doneCh := engineHealthDoneCh
	engineHealthStarted = false
	engineHealthStopCh = nil
	engineHealthDoneCh = nil
	engineHealthMu.Unlock()

	close(stopCh)
	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
	}
}

func runEngineHealthCheckerLoop(stopCh <-chan struct{}, doneCh chan<- struct{}) {
	defer close(doneCh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stopCh
		cancel()
	}()

	// Initial warm-up check shortly after startup so UI sees live status quickly.
	initTimer := time.NewTimer(5 * time.Second)
	defer initTimer.Stop()
	select {
	case <-stopCh:
		return
	case <-initTimer.C:
		if config.GetSystemSettings().EngineHealthCheckInterval > 0 {
			_, _ = checkAllEnginesHealthInternal(ctx, false)
			engineHealthMu.Lock()
			engineHealthLastRun = time.Now()
			engineHealthMu.Unlock()
		}
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			intervalMin := config.GetSystemSettings().EngineHealthCheckInterval
			if intervalMin <= 0 {
				continue
			}
			engineHealthMu.Lock()
			due := engineHealthLastRun.IsZero() || time.Since(engineHealthLastRun) >= time.Duration(intervalMin)*time.Minute
			engineHealthMu.Unlock()
			if !due {
				continue
			}
			_, _ = checkAllEnginesHealthInternal(ctx, true)
			engineHealthMu.Lock()
			engineHealthLastRun = time.Now()
			engineHealthMu.Unlock()
		}
	}
}
