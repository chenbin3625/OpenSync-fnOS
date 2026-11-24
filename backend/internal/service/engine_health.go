package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strings"
	"sync"
	"time"
)

// EngineStorageStatus describes one mounted storage reported by OpenList/AList.
type EngineStorageStatus struct {
	MountPath string `json:"mountPath"`
	Driver    string `json:"driver"`
	Status    string `json:"status"`
	Disabled  bool   `json:"disabled"`
	Remark    string `json:"remark"`
}

// EngineHealth holds the latest health probe outcome for one storage engine.
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

// RemoveEngineHealth removes cached health state for a deleted engine.
func RemoveEngineHealth(alistID int64) {
	engineHealthMu.Lock()
	delete(engineHealthCache, alistID)
	engineHealthMu.Unlock()
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
			return fmt.Sprintf("Engine: %s\nAddress: %s\nStatus: Recovered Online (Latency: %s)", name, engineURL, detail)
		case "storage_error":
			return fmt.Sprintf("Engine: %s\nAddress: %s\nUnhealthy Mounts: %s", name, engineURL, detail)
		default:
			return fmt.Sprintf("Engine: %s\nAddress: %s\nStatus: Offline\nReason: %s", name, engineURL, detail)
		}
	}
	switch kind {
	case "online":
		return fmt.Sprintf("存储引擎：%s\n服务地址：%s\n当前状态：已恢复在线（响应延迟 %s）", name, engineURL, detail)
	case "storage_error":
		return fmt.Sprintf("存储引擎：%s\n服务地址：%s\n异常挂载：%s", name, engineURL, detail)
	default:
		return fmt.Sprintf("存储引擎：%s\n服务地址：%s\n当前状态：连接异常\n原因：%s", name, engineURL, detail)
	}
}

func probeEngineRow(ctx context.Context, alist map[string]interface{}, sendAlert bool) *EngineHealth {
	alistID := util.ToInt64(alist["id"])
	remark := util.StringValue(alist["remark"])
	urlStr := util.StringValue(alist["url"])
	token := util.StringValue(alist["token"])

	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	start := time.Now()
	client, err := alistDeps.NewAlistClientCtx(probeCtx, urlStr, token, alistID)
	latency := time.Since(start).Milliseconds()
	now := time.Now().Unix()

	if err != nil {
		errText := msg.T(msg.AlistConnectFail)
		if strings.TrimSpace(err.Error()) == msg.T(msg.AlistUnAuth) {
			errText = msg.T(msg.AlistUnAuth)
		}
		h := EngineHealth{
			AlistID:       alistID,
			Status:        "offline",
			LatencyMs:     latency,
			LastCheckedAt: now,
			Error:         errText,
			Storages:      []EngineStorageStatus{},
			StoragesKnown: false,
		}
		updateEngineHealthCache(alistID, remark, urlStr, h, sendAlert)
		return &h
	}
	defer client.Close()

	storages, known, storageErr := client.ListStoragesContext(probeCtx)
	if storageErr != nil {
		log.Printf("engine health storage list failed: alistID=%d: %v", alistID, storageErr)
	}
	if storages == nil {
		storages = []EngineStorageStatus{}
	}
	h := EngineHealth{
		AlistID:       alistID,
		Status:        "online",
		LatencyMs:     latency,
		LastCheckedAt: now,
		Storages:      storages,
		StoragesKnown: known,
	}
	updateEngineHealthCache(alistID, remark, urlStr, h, sendAlert)
	return &h
}

// CheckEngineHealth probes one engine immediately and updates the cache.
func CheckEngineHealth(ctx context.Context, alistID int64) (*EngineHealth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	alist, err := alistDeps.GetAlistByID(alistID)
	if err != nil {
		return nil, publicErrorIf(err, msg.T(msg.AlistNotFound))
	}
	return probeEngineRow(ctx, alist, false), nil
}

// CheckAllEnginesHealth probes all configured engines concurrently (bounded).
func CheckAllEnginesHealth(ctx context.Context) ([]*EngineHealth, error) {
	return checkAllEnginesHealthInternal(ctx, false)
}

func checkAllEnginesHealthInternal(ctx context.Context, sendAlert bool) ([]*EngineHealth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	list, err := mapper.GetAlistList()
	if err != nil {
		return nil, fmt.Errorf("get alist list for health check: %w", err)
	}
	results := make([]*EngineHealth, len(list))
	if len(list) == 0 {
		return results, nil
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, row := range list {
		i, row := i, row
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			results[i] = probeEngineRow(ctx, row, sendAlert)
		}()
	}
	wg.Wait()
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

// ListStoragesContext fetches mounted storages via /api/admin/storage/list.
// Non-admin tokens (HTTP 401/403 or business code 401/403) are handled
// gracefully with known=false and err=nil so regular users still see online status.
func (c *AlistClient) ListStoragesContext(ctx context.Context) ([]EngineStorageStatus, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := c.GetContext(ctx, "/api/admin/storage/list", map[string]string{
		"page":     "1",
		"per_page": "200",
	})
	if err != nil {
		// Non-admin or unsupported endpoint: degrade gracefully without error.
		var statusErr *alistStatusError
		if err.Error() == msg.T(msg.AlistUnAuth) {
			return nil, false, nil
		}
		if asErr := err; asErr != nil {
			if ok := extractAlistStatusError(asErr, &statusErr); ok {
				if statusErr.httpStatus == 401 || statusErr.httpStatus == 403 || statusErr.httpStatus == 404 ||
					statusErr.alistCode == 401 || statusErr.alistCode == 403 || statusErr.alistCode == 404 {
					return nil, false, nil
				}
			}
		}
		return nil, false, err
	}

	var raw struct {
		Content []struct {
			MountPath string `json:"mount_path"`
			Driver    string `json:"driver"`
			Status    string `json:"status"`
			Disabled  bool   `json:"disabled"`
			Remark    string `json:"remark"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false, nil
	}
	out := make([]EngineStorageStatus, 0, len(raw.Content))
	for _, item := range raw.Content {
		out = append(out, EngineStorageStatus{
			MountPath: item.MountPath,
			Driver:    item.Driver,
			Status:    item.Status,
			Disabled:  item.Disabled,
			Remark:    item.Remark,
		})
	}
	return out, true, nil
}

func extractAlistStatusError(err error, target **alistStatusError) bool {
	if se, ok := err.(*alistStatusError); ok {
		*target = se
		return true
	}
	return false
}
