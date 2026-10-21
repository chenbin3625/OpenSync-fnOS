package service

import (
	"context"
	"fmt"
	"log"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strings"
	"sync"
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
)

type alistClientLoad struct {
	client   *AlistClient
	baseline *AlistClient
	epoch    uint64
	err      error
	done     chan struct{}
}

// GetClientList returns all alist entries without token
func GetClientList() ([]map[string]interface{}, error) {
	clientList, err := mapper.GetAlistList()
	if err != nil {
		return nil, fmt.Errorf("get alist list: %w", err)
	}
	for _, client := range clientList {
		delete(client, "token")
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
		baseline: alistClientList[alistID],
		epoch:    alistClientRemovals[alistID],
		done:     make(chan struct{}),
	}
	alistClientLoads[alistID] = load
	return load, true
}

func finishAlistClientLoad(alistID int64, load *alistClientLoad, client *AlistClient, err error) {
	var stale *AlistClient
	alistClientListMu.Lock()
	load.err = err
	if err == nil && client != nil && alistClientRemovals[alistID] != load.epoch {
		// The engine was removed while we were loading it.
		load.client = nil
		load.err = publicError(msg.T(msg.AlistNotFound))
		stale = client
	} else if err == nil && client != nil {
		current := alistClientList[alistID]
		if current != nil && current != load.baseline {
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
	close(load.done)
	if stale != nil {
		stale.Close()
	}
}

func storeAlistClient(alistID int64, client *AlistClient) {
	var previous *AlistClient
	alistClientListMu.Lock()
	previous = alistClientList[alistID]
	alistClientList[alistID] = client
	alistClientListMu.Unlock()
	if previous != nil && previous != client {
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
	return util.ValidateHTTPURL(rawURL, msg.T(msg.AlistURLInvalid))
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

	alistOld, err := mapper.GetAlistByID(alistID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.AlistNotFound)); err != nil {
			return err
		}
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
	remarkStr := ""
	remark, _ := alist["remark"]
	if remark != nil {
		remarkStr = fmt.Sprintf("%v", remark)
	}
	if err := mapper.UpdateAlist(alistID, remarkStr, urlStr, tokenPtr); err != nil {
		if client != nil {
			client.Close()
		}
		return fmt.Errorf("update alist: %w", err)
	}
	if client != nil {
		storeAlistClient(alistID, client)
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

	client, err := NewAlistClient(urlStr, token, 0)
	if err != nil {
		log.Printf("Failed to add alist client: %v", err)
		return publicError(msg.T(msg.AlistConnectFail))
	}

	remarkStr := ""
	if alist["remark"] != nil {
		remarkStr = fmt.Sprintf("%v", alist["remark"])
	}

	newID, err := mapper.AddAlist(remarkStr, urlStr, client.User, token)
	if err != nil {
		// The validated client is never cached on this path; release its
		// connection pool (and any HTTP/3 UDP socket) now instead of at GC.
		client.Close()
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return publicError(msg.T(msg.AlistExists))
		}
		return fmt.Errorf("add alist: %w", err)
	}

	client.AlistID = newID
	storeAlistClient(newID, client)
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
	markAlistClientRemoved(alistID)
	return nil
}

// markAlistClientRemoved evicts the cached client for alistID and bumps its
// removal epoch so an in-flight load discards the client it is building.
func markAlistClientRemoved(alistID int64) {
	alistClientListMu.Lock()
	alistClientRemovals[alistID]++
	alistClientListMu.Unlock()
	removeCachedAlistClient(alistID)
}

// TestClient tests connectivity to an existing AList engine by creating a fresh
// connection (bypassing the cache) so we know the engine is reachable right now.
func TestClient(ctx context.Context, alistID int64) error {
	alist, err := alistDeps.GetAlistByID(alistID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.AlistNotFound)); err != nil {
			return err
		}
	}
	// util.StringValue, not a bare type assertion: a DB row can carry a
	// non-string (or NULL) here, and an assertion would silently degrade the
	// token to "" and test the engine unauthenticated.
	url := util.StringValue(alist["url"])
	token := util.StringValue(alist["token"])
	if url == "" {
		return publicError(msg.T(msg.AlistURLInvalid))
	}
	client, err := alistDeps.NewAlistClientCtx(ctx, url, token, alistID)
	if err != nil {
		log.Printf("alist test failed: alistID=%d: %v", alistID, err)
		return publicError(msg.T(msg.AlistConnectFail))
	}
	client.Close()
	return nil
}

// GetChildPath gets child directory paths for path selector
func GetChildPath(ctx context.Context, alistID int64, path string) ([]map[string]string, error) {
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
