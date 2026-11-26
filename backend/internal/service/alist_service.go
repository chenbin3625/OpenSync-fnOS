package service

import (
	"context"
	"fmt"
	"net/url"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// alistRefMu serializes (validate storage exists -> insert/update job) against
	// (check job references -> remove storage) and native engine initialization.
	// Lock order: alistRefMu -> alistMutationMu.
	alistRefMu      sync.Mutex
	alistMutationMu sync.Mutex
)

const legacyOpenListJobDisabledMsg = "该任务关联旧版 OpenList 引擎，已不再支持并自动停用，请删除后重新创建"

// legacyOpenListAlistIDs returns the set of stored alist row IDs that do not
// belong to the built-in rclone engine (rclone://native).
func legacyOpenListAlistIDs() map[int64]bool {
	rows, err := mapper.GetAlistList()
	if err != nil {
		return nil
	}
	legacy := make(map[int64]bool)
	for _, row := range rows {
		if util.StringValue(row["url"]) != native.EngineURL {
			legacy[util.ToInt64(row["id"])] = true
		}
	}
	return legacy
}

// isLegacyOpenListAlistID reports whether alistID points to a stored legacy
// OpenList/AList engine row (url != rclone://native).
func isLegacyOpenListAlistID(alistID int64) bool {
	if alistID <= 0 {
		return false
	}
	row, err := alistDeps.GetAlistByID(alistID)
	if err != nil || row == nil {
		return false
	}
	return util.StringValue(row["url"]) != native.EngineURL
}

// EnsureNativeEngine guarantees the built-in rclone storage engine record exists
// in SQLite and returns its row ID.
func EnsureNativeEngine() (int64, error) {
	rows, err := mapper.GetAlistList()
	if err != nil {
		return 0, fmt.Errorf("get alist list: %w", err)
	}
	for _, row := range rows {
		if util.StringValue(row["url"]) == native.EngineURL {
			return util.ToInt64(row["id"]), nil
		}
	}

	alistRefMu.Lock()
	defer alistRefMu.Unlock()
	alistMutationMu.Lock()
	defer alistMutationMu.Unlock()

	rows, err = mapper.GetAlistList()
	if err != nil {
		return 0, fmt.Errorf("get alist list: %w", err)
	}
	for _, row := range rows {
		if util.StringValue(row["url"]) == native.EngineURL {
			return util.ToInt64(row["id"]), nil
		}
	}

	newID, err := mapper.AddAlistWithConcurrency("内置存储", native.EngineURL, "rclone", "{}", 1)
	if err != nil {
		// Handle possible race if UNIQUE constraint was hit concurrently.
		if retryRows, listErr := mapper.GetAlistList(); listErr == nil {
			for _, row := range retryRows {
				if util.StringValue(row["url"]) == native.EngineURL {
					return util.ToInt64(row["id"]), nil
				}
			}
		}
		return 0, fmt.Errorf("create built-in rclone engine: %w", err)
	}
	RemoveEngineHealth(newID)
	return newID, nil
}

// GetClientList returns the built-in rclone engine entry without credentials,
// enriched with live health status, remote metadata, and concurrency stats.
// Legacy external OpenList/AList rows are filtered out.
func GetClientList() ([]map[string]interface{}, error) {
	if _, err := EnsureNativeEngine(); err != nil {
		return nil, err
	}
	rawList, err := mapper.GetAlistList()
	if err != nil {
		return nil, fmt.Errorf("get alist list: %w", err)
	}
	clientList := make([]map[string]interface{}, 0, 1)
	for _, client := range rawList {
		if util.StringValue(client["url"]) != native.EngineURL {
			continue
		}
		decorateNative(client)
		delete(client, "token")
		alistID := util.ToInt64(client["id"])
		if _, ok := client["maxConcurrentJobs"]; !ok || client["maxConcurrentJobs"] == nil {
			client["maxConcurrentJobs"] = 0
		}
		active, queued := engineQueue.EngineStats(alistID)
		client["activeJobs"] = active
		client["queuedJobs"] = queued
		client["health"] = GetEngineHealth(alistID)
		clientList = append(clientList, client)
	}
	return clientList, nil
}

func validateHTTPURLHostPort(u *url.URL) bool {
	if u == nil {
		return false
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return false
	}
	if strings.HasSuffix(u.Host, ":") {
		return false
	}
	if portStr := u.Port(); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	return true
}

// RemoveClient refuses to delete the built-in rclone engine while still allowing
// cleanup of unused legacy rows if requested.
func RemoveClient(alistID int64) error {
	alistRefMu.Lock()
	defer alistRefMu.Unlock()
	alistMutationMu.Lock()
	defer alistMutationMu.Unlock()

	if row, err := mapper.GetAlistByID(alistID); err == nil && util.StringValue(row["url"]) == native.EngineURL {
		return publicError("内置存储引擎不可删除")
	}

	count, err := mapper.CountJobsByAlistID(alistID)
	if err != nil {
		return fmt.Errorf("count jobs by alist: %w", err)
	}
	if count > 0 {
		return publicError(msg.T(msg.AlistInUse))
	}
	if err := mapper.RemoveAlist(alistID); err != nil {
		return fmt.Errorf("remove alist: %w", err)
	}
	RemoveEngineHealth(alistID)
	return nil
}

// GetChildPath gets child directory paths for the path selector via built-in rclone.
func GetChildPath(ctx context.Context, alistID int64, path string) ([]map[string]string, error) {
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
	cfg, err := loadNative(alistID)
	if err != nil {
		return nil, publicErrorIf(err, msg.T(msg.AlistNotFound))
	}
	if cfg == nil {
		return nil, publicError("旧版 OpenList 引擎已不再支持，请使用内置存储路径")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	list, e := cfg.ListWithOptions(ctx, path, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
	if e != nil {
		return nil, publicError(e.Error())
	}
	return list, nil
}
