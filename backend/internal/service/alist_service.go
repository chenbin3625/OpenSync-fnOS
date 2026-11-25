package service

import (
	"context"
	"fmt"
	"log"
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
	alistClientList   = make(map[int64]*AlistClient)
	alistClientListMu sync.RWMutex
	alistClientLoads  = make(map[int64]*alistClientLoad)
	// alistClientRemovals counts RemoveClient calls per id. A load that read
	// the DB row before the row was deleted would otherwise cache a client for
	// an id that no longer exists; finishAlistClientLoad compares epochs and
	// discards such a client instead.
	alistClientRemovals = make(map[int64]uint64)
	// alistClientUpdates counts UpdateClient cache commits per id so a load that
	// started before UpdateClient cannot overwrite or outlive the new credentials
	// even if the new client was concurrently evicted.
	alistClientUpdates = make(map[int64]uint64)
	// alistMutationMu serializes DB mutations (AddClient, UpdateClient,
	// RemoveClient) with their corresponding in-memory cache updates and
	// finishAlistClientLoad. Lock order: alistRefMu -> alistMutationMu -> alistClientListMu.
	alistMutationMu sync.Mutex
)

type alistClientLoad struct {
	client      *AlistClient
	baseline    *AlistClient
	epoch       uint64
	updateEpoch uint64
	err         error
	done        chan struct{}
}

// GetClientList returns all alist entries without token, enriched with live
// health status and concurrency queue stats.
func GetClientList() ([]map[string]interface{}, error) {
	clientList, err := mapper.GetAlistList()
	if err != nil {
		return nil, fmt.Errorf("get alist list: %w", err)
	}
	for _, client := range clientList {
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
	}
	return clientList, nil
}

func GetClientByIDContext(ctx context.Context, alistID int64) (*AlistClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	alistClientListMu.RLock()
	client, ok := alistClientList[alistID]
	alistClientListMu.RUnlock()
	if ok {
		return client, nil
	}

	load, owner := beginAlistClientLoad(alistID)
	if !owner {
		return awaitAlistClientLoad(ctx, load)
	}

	alist, err := alistDeps.GetAlistByID(alistID)
	if err != nil {
		finishAlistClientLoad(alistID, load, nil, err)
		return nil, alistClientLoadError(err)
	}

	// The load is shared by every concurrent caller for this id, so it must
	// not inherit the owner's cancellation: one aborted request would fail all
	// waiters with a connect error although their own contexts are fine. The
	// load runs detached, bounded by alistValidationTimeout, and every caller
	// (owner included) only waits on it with its own context.
	//
	// Resolve the dependency now instead of inside the goroutine: tests swap
	// the package-level alistDeps when they return, and a detached goroutine
	// reading it later is a data race.
	newClient := alistDeps.NewAlistClientCtx
	go runAlistClientLoad(context.WithoutCancel(ctx), alistID, alist, load, newClient)
	return awaitAlistClientLoad(ctx, load)
}

func runAlistClientLoad(ctx context.Context, alistID int64, alist map[string]interface{}, load *alistClientLoad, newClient func(context.Context, string, string, int64) (*AlistClient, error)) {
	ctx, cancel := context.WithTimeout(ctx, alistValidationTimeout)
	defer cancel()
	client, err := newClient(
		ctx,
		fmt.Sprintf("%v", alist["url"]),
		fmt.Sprintf("%v", alist["token"]),
		alistID,
	)
	if err != nil {
		finishAlistClientLoad(alistID, load, nil, err)
		return
	}
	finishAlistClientLoad(alistID, load, client, nil)
}

func awaitAlistClientLoad(ctx context.Context, load *alistClientLoad) (*AlistClient, error) {
	select {
	case <-load.done:
	case <-ctx.Done():
		return nil, alistClientLoadError(ctx.Err())
	}
	if load.err != nil {
		return nil, alistClientLoadError(load.err)
	}
	return load.client, nil
}

func beginAlistClientLoad(alistID int64) (*alistClientLoad, bool) {
	alistClientListMu.Lock()
	defer alistClientListMu.Unlock()

	if client, ok := alistClientList[alistID]; ok {
		load := &alistClientLoad{client: client, done: make(chan struct{})}
		close(load.done)
		return load, false
	}
	if load, ok := alistClientLoads[alistID]; ok {
		return load, false
	}

	load := &alistClientLoad{
		baseline:    alistClientList[alistID],
		epoch:       alistClientRemovals[alistID],
		updateEpoch: alistClientUpdates[alistID],
		done:        make(chan struct{}),
	}
	alistClientLoads[alistID] = load
	return load, true
}

func finishAlistClientLoad(alistID int64, load *alistClientLoad, client *AlistClient, err error) {
	var stale *AlistClient
	alistMutationMu.Lock()
	alistClientListMu.Lock()
	load.err = err
	if err == nil && client != nil && alistClientRemovals[alistID] != load.epoch {
		// The engine was removed while we were loading it.
		load.client = nil
		load.err = publicError(msg.T(msg.AlistNotFound))
		stale = client
	} else if err == nil && client != nil {
		current := alistClientList[alistID]
		if alistClientUpdates[alistID] != load.updateEpoch {
			// UpdateClient mutated this engine while we were loading.
			if current != nil {
				load.client = current
			} else {
				load.client = nil
				load.err = publicError(msg.T(msg.AlistConnectFail))
			}
			stale = client
		} else if current != nil && current != load.baseline {
			// A newer client was stored (e.g. via UpdateClient) while we were
			// loading; keep the fresh one and discard the stale client we built.
			load.client = current
			stale = client
		} else {
			alistClientList[alistID] = client
			load.client = client
		}
	} else {
		load.client = client
	}
	delete(alistClientLoads, alistID)
	alistClientListMu.Unlock()
	alistMutationMu.Unlock()
	close(load.done)
	if stale != nil {
		stale.Close()
	}
}

func swapCachedAlistClient(alistID int64, client *AlistClient) *AlistClient {
	alistClientListMu.Lock()
	previous := alistClientList[alistID]
	alistClientList[alistID] = client
	alistClientUpdates[alistID]++
	alistClientListMu.Unlock()
	if previous == client {
		return nil
	}
	return previous
}

func storeAlistClient(alistID int64, client *AlistClient) {
	if previous := swapCachedAlistClient(alistID, client); previous != nil {
		previous.Close()
	}
}

func removeCachedAlistClient(alistID int64) {
	var previous *AlistClient
	alistClientListMu.Lock()
	previous = alistClientList[alistID]
	delete(alistClientList, alistID)
	alistClientListMu.Unlock()
	if previous != nil {
		previous.Close()
	}
}

// removeCachedAlistClientIf evicts the cached client for alistID only when it
// is still client. A task holding an old client whose token was just replaced
// via UpdateClient gets a 401; evicting by id alone would drop (and close) the
// new, valid client instead of the stale one.
func removeCachedAlistClientIf(alistID int64, client *AlistClient) {
	alistClientListMu.Lock()
	current, ok := alistClientList[alistID]
	evict := ok && current == client
	if evict {
		delete(alistClientList, alistID)
	}
	alistClientListMu.Unlock()
	if evict {
		client.Close()
	}
}

func alistClientLoadError(err error) error {
	if err == nil {
		return nil
	}
	// "Not found" is already a sanitized, actionable public message — pass it
	// through so a stale client ID gets a meaningful error. Everything else may
	// include internal host/IP/port details: log it and return only a generic
	// message so network topology is not leaked through the API response.
	if err.Error() == msg.T(msg.AlistNotFound) {
		return publicError(msg.T(msg.AlistNotFound))
	}
	log.Printf("alist client load failed: %v", err)
	return publicError(msg.T(msg.AlistConnectFail))
}

func normalizeAlistInput(alist map[string]interface{}) (string, error) {
	remark, _ := alist["remark"]
	if remark != nil {
		if s, ok := remark.(string); ok && strings.TrimSpace(s) == "" {
			alist["remark"] = nil
		}
	}

	urlStr := strings.TrimRight(fmt.Sprintf("%v", alist["url"]), "/")
	if err := validateAlistURL(urlStr); err != nil {
		return "", publicError(err.Error())
	}
	alist["url"] = urlStr
	return urlStr, nil
}

func validateAlistURL(rawURL string) error {
	if err := util.ValidateHTTPURL(rawURL, msg.T(msg.AlistURLInvalid)); err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !validateHTTPURLHostPort(u) {
		return fmt.Errorf("%s", msg.T(msg.AlistURLInvalid))
	}
	return nil
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

func normalizeAlistToken(alist map[string]interface{}, required bool) (string, bool, error) {
	token, ok := alist["token"]
	if !ok || token == nil {
		if required {
			return "", false, publicError(msg.T(msg.AlistTokenRequired))
		}
		delete(alist, "token")
		return "", false, nil
	}
	tokenStr := strings.TrimSpace(fmt.Sprintf("%v", token))
	if tokenStr == "" || tokenStr == "<nil>" {
		if required {
			return "", false, publicError(msg.T(msg.AlistTokenRequired))
		}
		delete(alist, "token")
		return "", false, nil
	}
	alist["token"] = tokenStr
	return tokenStr, true, nil
}

func normalizeAlistMaxConcurrentJobs(alist map[string]interface{}) (int, bool, error) {
	raw, ok := alist["maxConcurrentJobs"]
	if !ok || raw == nil {
		return 0, false, nil
	}
	n, valid := parseStrictNonNegativeInt(raw)
	if !valid || n < 0 || n > 50 {
		return 0, false, publicError(msg.T(msg.MaxConcurrentJobsInvalid))
	}
	return n, true, nil
}

func parseStrictNonNegativeInt(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, v >= 0
	case int64:
		return int(v), v >= 0
	case float64:
		if v != float64(int64(v)) || v < 0 {
			return 0, false
		}
		return int(v), true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, true
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// UpdateClient updates an AList client
func UpdateClient(alist map[string]interface{}) error {
	alistID := util.ToInt64(alist["id"])
	urlStr, err := normalizeAlistInput(alist)
	if err != nil {
		return err
	}

	token, hasToken, err := normalizeAlistToken(alist, false)
	if err != nil {
		return err
	}

	maxConcurrentJobs, hasMaxConcurrent, err := normalizeAlistMaxConcurrentJobs(alist)
	if err != nil {
		return err
	}

	alistOld, err := mapper.GetAlistByID(alistID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.AlistNotFound)); err != nil {
			return err
		}
	}

	if alistOld["url"] == "rclone://native" {
		return publicError("请通过原生存储设置修改引擎")
	}
	oldURL := fmt.Sprintf("%v", alistOld["url"])
	var client *AlistClient
	if oldURL != urlStr || hasToken {
		if !hasToken {
			return publicError(msg.T(msg.WithoutToken))
		}
		// Validate with alistID=0: a bad new token makes /api/me answer 401, and
		// checkAlistCode would evict the cached client for alistID — the one
		// that still works with the old, stored credentials. The id is only
		// attached once validation succeeded.
		client, err = alistDeps.NewAlistClient(urlStr, token, 0)
		if err != nil {
			log.Printf("alist client update failed: %v", err)
			return publicError(msg.T(msg.AlistConnectFail))
		}
		client.AlistID = alistID
	}

	var tokenPtr *string
	if hasToken {
		tokenPtr = &token
	}
	var maxConcurrentPtr *int
	if hasMaxConcurrent {
		maxConcurrentPtr = &maxConcurrentJobs
	}
	remarkStr := ""
	remark, _ := alist["remark"]
	if remark != nil {
		remarkStr = fmt.Sprintf("%v", remark)
	}
	alistMutationMu.Lock()
	currentRow, err := mapper.GetAlistByID(alistID)
	if err != nil {
		alistMutationMu.Unlock()
		if client != nil {
			client.Close()
		}
		return publicErrorIf(err, msg.T(msg.AlistNotFound))
	}
	currentOldURL := fmt.Sprintf("%v", currentRow["url"])
	if (currentOldURL != urlStr || hasToken) && client == nil {
		alistMutationMu.Unlock()
		return publicError(msg.T(msg.WithoutToken))
	}
	if err := mapper.UpdateAlistWithConcurrency(alistID, remarkStr, urlStr, tokenPtr, maxConcurrentPtr); err != nil {
		alistMutationMu.Unlock()
		if client != nil {
			client.Close()
		}
		return fmt.Errorf("update alist: %w", err)
	}
	var previous *AlistClient
	if client != nil {
		previous = swapCachedAlistClient(alistID, client)
	}
	alistMutationMu.Unlock()
	if previous != nil {
		previous.Close()
	}
	if hasMaxConcurrent {
		engineQueue.RefreshLimit(alistID, maxConcurrentJobs)
	}
	return nil
}

// AddClient adds a new AList client
func AddClient(alist map[string]interface{}) error {
	urlStr, err := normalizeAlistInput(alist)
	if err != nil {
		return err
	}
	token, _, err := normalizeAlistToken(alist, true)
	if err != nil {
		return err
	}
	maxConcurrentJobs, _, err := normalizeAlistMaxConcurrentJobs(alist)
	if err != nil {
		return err
	}

	client, err := alistDeps.NewAlistClient(urlStr, token, 0)
	if err != nil {
		log.Printf("Failed to add alist client: %v", err)
		return publicError(msg.T(msg.AlistConnectFail))
	}

	remarkStr := ""
	if alist["remark"] != nil {
		remarkStr = fmt.Sprintf("%v", alist["remark"])
	}

	alistMutationMu.Lock()
	newID, err := mapper.AddAlistWithConcurrency(remarkStr, urlStr, client.User, token, maxConcurrentJobs)
	if err != nil {
		alistMutationMu.Unlock()
		// The validated client is never cached on this path; release its
		// connection pool (and any HTTP/3 UDP socket) now instead of at GC.
		client.Close()
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return publicError(msg.T(msg.AlistExists))
		}
		return fmt.Errorf("add alist: %w", err)
	}

	client.AlistID = newID
	previous := swapCachedAlistClient(newID, client)
	alistMutationMu.Unlock()
	if previous != nil {
		previous.Close()
	}
	return nil
}

// alistRefMu serializes (validate alist exists → insert job) against
// (check job references → remove alist), closing the TOCTOU window where a job
// could be created for an alist that RemoveClient is deleting.
var alistRefMu sync.Mutex

// RemoveClient removes an AList client
func RemoveClient(alistID int64) error {
	alistRefMu.Lock()
	defer alistRefMu.Unlock()
	alistMutationMu.Lock()
	var previous *AlistClient
	defer func() {
		alistMutationMu.Unlock()
		if previous != nil {
			previous.Close()
		}
	}()
	count, err := mapper.CountJobsByAlistID(alistID)
	if err != nil {
		return fmt.Errorf("count jobs by alist: %w", err)
	}
	if count > 0 {
		return publicError(msg.T(msg.AlistInUse))
	}

	// Delete the row before dropping the cache: in the other order a
	// concurrent GetClientByIDContext could read the still-present row and
	// re-cache a client for the id being removed. The removal epoch covers a
	// load that read the row just before the delete.
	if err := mapper.RemoveAlist(alistID); err != nil {
		return fmt.Errorf("remove alist: %w", err)
	}
	previous = markAlistClientRemovedLocked(alistID)
	RemoveEngineHealth(alistID)
	return nil
}

// markAlistClientRemoved evicts the cached client for alistID and bumps its
// removal epoch so an in-flight load discards the client it is building.
func markAlistClientRemoved(alistID int64) {
	if previous := markAlistClientRemovedLocked(alistID); previous != nil {
		previous.Close()
	}
}

func markAlistClientRemovedLocked(alistID int64) *AlistClient {
	alistClientListMu.Lock()
	alistClientRemovals[alistID]++
	previous := alistClientList[alistID]
	delete(alistClientList, alistID)
	alistClientListMu.Unlock()
	return previous
}

// TestClient tests connectivity to an existing AList engine by creating a fresh
// connection (bypassing the cache) so we know the engine is reachable right now.
func TestClient(ctx context.Context, alistID int64) error {
	_, err := TestClientWithHealth(ctx, alistID)
	return err
}

// TestClientWithHealth probes connectivity and mounted storages on an existing
// AList engine, updates the health cache, and returns the EngineHealth snapshot.
func TestClientWithHealth(ctx context.Context, alistID int64) (*EngineHealth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	alist, err := alistDeps.GetAlistByID(alistID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.AlistNotFound)); err != nil {
			return nil, err
		}
	}
	// util.StringValue, not a bare type assertion: a DB row can carry a
	// non-string (or NULL) here, and an assertion would silently degrade the
	// token to "" and test the engine unauthenticated.
	urlStr := util.StringValue(alist["url"])
	if urlStr == "" {
		return nil, publicError(msg.T(msg.AlistURLInvalid))
	}
	row := make(map[string]interface{}, len(alist)+1)
	for k, v := range alist {
		row[k] = v
	}
	if util.ToInt64(row["id"]) == 0 && alistID > 0 {
		row["id"] = alistID
	}
	health := probeEngineRow(ctx, row, false)
	if health.Status != "online" {
		log.Printf("alist test failed: alistID=%d: %s", alistID, health.Error)
		if urlStr == "rclone://native" && strings.TrimSpace(health.Error) != "" {
			return health, publicError(health.Error)
		}
		if health.Error == msg.T(msg.AlistUnAuth) {
			return health, publicError(msg.T(msg.AlistUnAuth))
		}
		return health, publicError(msg.T(msg.AlistConnectFail))
	}
	return health, nil
}

// GetChildPath gets child directory paths for path selector
func GetChildPath(ctx context.Context, alistID int64, path string) ([]map[string]string, error) {
	cfg, err := loadNative(alistID)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		list, e := cfg.ListWithOptions(ctx, path, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
		if e != nil {
			return nil, publicError(e.Error())
		}
		return list, nil
	}
	client, err := GetClientByIDContext(ctx, alistID)
	if err != nil {
		return nil, err
	}
	result, err := client.FilePathList(ctx, path)
	if err != nil {
		log.Printf("alist path list failed: alistID=%d path=%q: %v", alistID, path, err)
		return nil, publicError(msg.T(msg.AlistConnectFail))
	}
	return result, nil
}
