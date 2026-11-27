package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/internal/pan123"
	"opensync/pkg/util"
)

func init() {
	native.RegisterTokenUpdateHook(persistRefreshedNativeTokens)
}

// persistRefreshedNativeTokens atomically writes rclone-refreshed OAuth/session
// tokens back into encrypted SQLite storage without logging secret values.
func persistRefreshedNativeTokens(updates map[string]native.TokenUpdate) {
	if len(updates) == 0 {
		return
	}
	alistMutationMu.Lock()
	defer alistMutationMu.Unlock()

	id, remark, _, c, err := loadExistingNativeRowFull()
	if err != nil || id == 0 || len(c) == 0 {
		return
	}
	var updatedNames []string
	for name, update := range updates {
		canonical, ok := c.FindKey(name)
		// Compare the entire original remote, including account, backend and
		// token fields. Apply a multi-field refresh atomically or skip it all.
		if !ok || update.Original == nil || !maps.Equal(c[canonical], update.Original) {
			continue
		}
		changed := false
		for k, v := range update.Fields {
			switch k {
			case "token", "client_access_token", "client_refresh_token", "cookie":
			case "access_token", "token_expires_at":
				if c[canonical]["type"] != "pan123" {
					continue
				}
			default:
				continue
			}
			if v != "" && c[canonical][k] != v {
				c[canonical][k] = v
				changed = true
			}
		}
		if changed {
			updatedNames = append(updatedNames, canonical)
		}
	}
	if len(updatedNames) == 0 {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	secret := string(data)
	if err := mapper.UpdateAlist(id, remark, native.EngineURL, &secret); err == nil {
		slices.Sort(updatedNames)
		// Never log token values; only remote names.
		fmt.Printf("OpenSync: persisted refreshed OAuth tokens for remotes %v\n", updatedNames)
	}
}

func nativeStorageConcurrencyLimits(c native.Config) map[string]int {
	limits := map[string]int{}
	if c != nil {
		for k, v := range c.StorageConcurrencyLimits() {
			limits[k] = v
		}
	}
	if localCap := config.GetSystemSettings().LocalMaxConcurrentJobs; localCap > 0 {
		limits[native.Local] = localCap
	}
	return limits
}

func refreshNativeStorageConcurrencyLimits(c native.Config) {
	engineQueue.RefreshStorageLimits(nativeStorageConcurrencyLimits(c))
}

type NativeStorageInput struct {
	ConfigPassword    string            `json:"configPassword"`
	Name              string            `json:"name"`
	Options           map[string]string `json:"options"`
	Config            string            `json:"config"`
	Replace           bool              `json:"replace"`
	MaxConcurrentJobs *int              `json:"maxConcurrentJobs,omitempty"`
	AuthSessionID     string            `json:"authSessionId,omitempty"`
}

type NativeEngineInput struct {
	Remark                 *string `json:"remark,omitempty"`
	MaxConcurrentJobs      *int    `json:"maxConcurrentJobs,omitempty"`
	LocalMaxConcurrentJobs *int    `json:"localMaxConcurrentJobs,omitempty"`
}

type NativeRemoteEditInput struct {
	Name          string            `json:"name"`
	NewName       string            `json:"newName,omitempty"`
	Options       map[string]string `json:"options"`
	ClearSecrets  []string          `json:"clearSecrets,omitempty"`
	AuthSessionID string            `json:"authSessionId,omitempty"`
}

type NativeImportInput struct {
	Config         string   `json:"config"`
	ConfigPassword string   `json:"configPassword"`
	Selected       []string `json:"selected,omitempty"`
	Replace        bool     `json:"replace"`
}

type NativeImportResult struct {
	Imported []string `json:"imported"`
	Replaced []string `json:"replaced"`
}

type NativeStorageTestInput struct {
	Name          string            `json:"name"`
	Options       map[string]string `json:"options,omitempty"`
	Editing       bool              `json:"editing,omitempty"`
	ClearSecrets  []string          `json:"clearSecrets,omitempty"`
	AuthSessionID string            `json:"authSessionId,omitempty"`
}

type NativeStorageTestResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Message   string `json:"message"`
	Warning   string `json:"warning,omitempty"`
	DirsCount int    `json:"dirsCount,omitempty"`
}

type NativeRemoteItem struct {
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	Summary           string            `json:"summary"`
	Options           map[string]string `json:"options"`
	ConfiguredSecrets []string          `json:"configuredSecrets"`
	UsedByJobs        []string          `json:"usedByJobs"`
	UsedByCrypts      []string          `json:"usedByCrypts"`
	MaxConcurrentJobs int               `json:"maxConcurrentJobs"`
	ActiveJobs        int               `json:"activeJobs"`
	QueuedJobs        int               `json:"queuedJobs"`
}

type NativeStorageState struct {
	EngineID               int64                  `json:"engineId"`
	Enabled                bool                   `json:"enabled"`
	Remark                 string                 `json:"remark"`
	MaxConcurrentJobs      int                    `json:"maxConcurrentJobs"`
	LocalMaxConcurrentJobs int                    `json:"localMaxConcurrentJobs"`
	LocalActiveJobs        int                    `json:"localActiveJobs"`
	LocalQueuedJobs        int                    `json:"localQueuedJobs"`
	RcloneAvailable        bool                   `json:"rcloneAvailable"`
	RcloneVersion          string                 `json:"rcloneVersion,omitempty"`
	RcloneError            string                 `json:"rcloneError,omitempty"`
	SupportedTypes         []string               `json:"supportedTypes"`
	LocalRoots             []native.LocalRootInfo `json:"localRoots"`
	Remotes                []NativeRemoteItem     `json:"remotes"`
}

type nativeTransferringItem struct {
	native.TransferItem
	srcBase string
	dstBase string
}

type nativeProgressSnapshot struct {
	src          string
	dst          string
	stats        *native.Stats
	transferring []nativeTransferringItem
}

func (snap *nativeProgressSnapshot) activeTransferring() []nativeTransferringItem {
	if snap == nil {
		return nil
	}
	if len(snap.transferring) > 0 {
		return snap.transferring
	}
	if snap.stats == nil || len(snap.stats.Transferring) == 0 {
		return nil
	}
	items := make([]nativeTransferringItem, len(snap.stats.Transferring))
	for i, ti := range snap.stats.Transferring {
		items[i] = nativeTransferringItem{
			TransferItem: ti,
			srcBase:      snap.src,
			dstBase:      snap.dst,
		}
	}
	return items
}

func defaultNativeRemark(remark string) string {
	remark = strings.TrimSpace(remark)
	if remark == "" {
		return "内置存储"
	}
	return remark
}

func loadExistingNativeRowFull() (int64, string, int, native.Config, error) {
	rows, err := mapper.GetAlistList()
	if err != nil {
		return 0, "内置存储", 1, nil, err
	}
	for _, row := range rows {
		if row["url"] == native.EngineURL {
			id := util.ToInt64(row["id"])
			stored, err := mapper.GetAlistByID(id)
			if err != nil {
				return 0, "内置存储", 1, nil, err
			}
			c, err := native.Decode(util.StringValue(stored["token"]))
			if err != nil {
				return 0, "内置存储", 1, nil, publicError(err.Error())
			}
			remark := defaultNativeRemark(util.StringValue(stored["remark"]))
			mc := util.ToInt(stored["maxConcurrentJobs"])
			return id, remark, mc, c, nil
		}
	}
	return 0, "内置存储", 1, native.Config{}, nil
}

func loadExistingNativeRow() (int64, native.Config, error) {
	id, _, _, c, err := loadExistingNativeRowFull()
	return id, c, err
}

func collectNativeRemoteReferences(alistID int64, c native.Config) (jobsByRemote map[string][]string, cryptsByRemote map[string][]string) {
	jobsByRemote = map[string][]string{}
	cryptsByRemote = map[string][]string{}
	addDep := func(target, owner string) {
		if target == "" || target == owner {
			return
		}
		if !slices.Contains(cryptsByRemote[target], owner) {
			cryptsByRemote[target] = append(cryptsByRemote[target], owner)
		}
	}
	for name, params := range c {
		typ := params["type"]
		if native.IsWrapperType(typ) {
			ref, _, _ := strings.Cut(params["remote"], ":")
			if target, ok := c.FindKey(ref); ok {
				addDep(target, name)
			}
		} else if typ == "union" {
			if ups, err := native.ParseUnionUpstreams(params["upstreams"]); err == nil {
				for _, up := range ups {
					ref, _, _ := strings.Cut(up.Target, ":")
					if target, ok := c.FindKey(ref); ok {
						addDep(target, name)
					}
				}
			}
		} else if typ == "combine" {
			if ups, err := native.ParseCombineUpstreams(params["upstreams"]); err == nil {
				for _, up := range ups {
					ref, _, _ := strings.Cut(up.Target, ":")
					if target, ok := c.FindKey(ref); ok {
						addDep(target, name)
					}
				}
			}
		}
	}
	for k := range cryptsByRemote {
		slices.Sort(cryptsByRemote[k])
	}
	if alistID <= 0 {
		return jobsByRemote, cryptsByRemote
	}
	jobs, err := mapper.GetJobListAll()
	if err != nil {
		return jobsByRemote, cryptsByRemote
	}
	for _, job := range jobs {
		if util.ToInt64(job["alistId"]) != alistID {
			continue
		}
		jobLabel := jobDisplayName(job)
		seen := map[string]bool{}
		for _, p := range append(parsePathList(job["srcPath"]), parsePathList(job["dstPath"])...) {
			seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
			if target, ok := c.FindKey(seg); ok && !seen[target] {
				seen[target] = true
				jobsByRemote[target] = append(jobsByRemote[target], jobLabel)
			}
		}
	}
	for k := range jobsByRemote {
		slices.Sort(jobsByRemote[k])
	}
	return jobsByRemote, cryptsByRemote
}

// GetNativeStorageState returns the full management state for the native
// rclone engine: availability, engine settings, fnOS-authorized local roots,
// and redacted details + reference counts for each configured remote.
func GetNativeStorageState(ctx context.Context) (*NativeStorageState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := EnsureNativeEngine(); err != nil {
		return nil, err
	}
	id, remark, mc, c, err := loadExistingNativeRowFull()
	if err != nil {
		return nil, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rcloneVer, rcloneErr := native.Version(checkCtx)
	localActive, localQueued := engineQueue.StorageStats(native.Local)
	state := &NativeStorageState{
		EngineID:               id,
		Enabled:                id > 0,
		Remark:                 remark,
		MaxConcurrentJobs:      mc,
		LocalMaxConcurrentJobs: config.GetSystemSettings().LocalMaxConcurrentJobs,
		LocalActiveJobs:        localActive,
		LocalQueuedJobs:        localQueued,
		RcloneAvailable:        rcloneErr == nil,
		RcloneVersion:          rcloneVer,
		SupportedTypes:         native.SupportedRemoteTypes(),
		LocalRoots:             native.AuthorizedRoots(),
		Remotes:                make([]NativeRemoteItem, 0, len(c)),
	}
	if rcloneErr != nil {
		state.RcloneError = rcloneErr.Error()
	}
	jobsByRemote, cryptsByRemote := collectNativeRemoteReferences(id, c)
	for _, name := range slices.Sorted(maps.Keys(c)) {
		params := c[name]
		pub, secrets := native.RedactParams(params)
		usedJobs := jobsByRemote[name]
		if usedJobs == nil {
			usedJobs = []string{}
		}
		usedCrypts := cryptsByRemote[name]
		if usedCrypts == nil {
			usedCrypts = []string{}
		}
		activeJobs, queuedJobs := engineQueue.StorageStats(name)
		remoteCap, _ := strconv.Atoi(strings.TrimSpace(params["max_concurrent_jobs"]))
		state.Remotes = append(state.Remotes, NativeRemoteItem{
			Name:              name,
			Type:              params["type"],
			Summary:           native.RemoteSummary(params),
			Options:           pub,
			ConfiguredSecrets: secrets,
			UsedByJobs:        usedJobs,
			UsedByCrypts:      usedCrypts,
			MaxConcurrentJobs: remoteCap,
			ActiveJobs:        activeJobs,
			QueuedJobs:        queuedJobs,
		})
	}
	return state, nil
}

// SaveNativeEngine enables the native engine (if not yet created) and updates
// its remark and maxConcurrentJobs limit without touching stored remotes.
func SaveNativeEngine(ctx context.Context, in NativeEngineInput) (*NativeStorageState, error) {
	var nextRemark string
	hasRemark := false
	if in.Remark != nil {
		trimmed := strings.TrimSpace(*in.Remark)
		if trimmed == "" {
			return nil, publicError("请输入引擎名称")
		}
		if len([]rune(trimmed)) > 200 {
			return nil, publicError("引擎名称不能超过 200 个字符")
		}
		nextRemark = trimmed
		hasRemark = true
	}
	if in.MaxConcurrentJobs != nil {
		if *in.MaxConcurrentJobs < 0 || *in.MaxConcurrentJobs > 50 {
			return nil, publicError("最大并发任务数必须在 0–50 之间")
		}
	}
	if in.LocalMaxConcurrentJobs != nil {
		if *in.LocalMaxConcurrentJobs < 0 || *in.LocalMaxConcurrentJobs > 50 {
			return nil, publicError("本地存储最大并发任务数必须在 0–50 之间")
		}
	}

	alistRefMu.Lock()
	alistMutationMu.Lock()
	id, existingRemark, _, existingCfg, err := loadExistingNativeRowFull()
	if err != nil {
		alistMutationMu.Unlock()
		alistRefMu.Unlock()
		return nil, err
	}
	if !hasRemark {
		nextRemark = existingRemark
	}
	if id == 0 {
		mc := 1
		if in.MaxConcurrentJobs != nil {
			mc = *in.MaxConcurrentJobs
		}
		newID, addErr := mapper.AddAlistWithConcurrency(nextRemark, native.EngineURL, "rclone", "{}", mc)
		alistMutationMu.Unlock()
		alistRefMu.Unlock()
		if addErr != nil {
			return nil, addErr
		}
		if in.LocalMaxConcurrentJobs != nil {
			localCap := *in.LocalMaxConcurrentJobs
			if cfgErr := config.UpdateSystemSettingsWith(func(cur config.SystemSettings) config.SystemSettings {
				cur.LocalMaxConcurrentJobs = localCap
				return cur
			}); cfgErr != nil {
				return nil, publicError(cfgErr.Error())
			}
			refreshNativeStorageConcurrencyLimits(existingCfg)
		}
		RemoveEngineHealth(newID)
		return GetNativeStorageState(ctx)
	}
	updateErr := mapper.UpdateAlistWithConcurrency(id, nextRemark, native.EngineURL, nil, in.MaxConcurrentJobs)
	alistMutationMu.Unlock()
	alistRefMu.Unlock()
	if updateErr != nil {
		return nil, updateErr
	}
	if in.MaxConcurrentJobs != nil {
		engineQueue.RefreshLimit(id, *in.MaxConcurrentJobs)
	}
	if in.LocalMaxConcurrentJobs != nil {
		localCap := *in.LocalMaxConcurrentJobs
		if cfgErr := config.UpdateSystemSettingsWith(func(cur config.SystemSettings) config.SystemSettings {
			cur.LocalMaxConcurrentJobs = localCap
			return cur
		}); cfgErr != nil {
			return nil, publicError(cfgErr.Error())
		}
		refreshNativeStorageConcurrencyLimits(existingCfg)
	}
	return GetNativeStorageState(ctx)
}

func normalizeSFTPEndpoint(p map[string]string) (host, port string) {
	host = strings.ToLower(strings.Trim(strings.TrimSpace(p["host"]), "[]"))
	port = strings.TrimSpace(p["port"])
	if port == "" {
		port = "22"
	}
	return host, port
}

// mergeEditRemoteParams merges edited form options over an existing remote:
// omitted secret fields keep their previously stored values (marking already-
// obscured fields in skipObscure so Prepare never double-obscures them), and
// SFTP known_hosts stays pinned unless host/port changed or explicitly cleared.
func mergeEditRemoteParams(oldParams, incoming map[string]string, clearSecrets []string) (map[string]string, map[string]bool, error) {
	merged := native.NormalizeParams(incoming)
	oldType := oldParams["type"]
	if merged["type"] == "" {
		merged["type"] = oldType
	} else if merged["type"] != oldType {
		return nil, nil, publicError("同名存储的类型不能修改")
	}
	clearSet := map[string]bool{}
	for _, k := range clearSecrets {
		k = strings.TrimSpace(k)
		if k != "" {
			clearSet[k] = true
		}
	}
	explicitToken := merged["access_token"] != ""
	skipObscure := map[string]bool{}
	for _, key := range native.SecretFields {
		if merged[key] == "" && !clearSet[key] && oldParams[key] != "" {
			merged[key] = oldParams[key]
			if native.IsObscuredFieldForType(oldType, key) {
				skipObscure[key] = true
			}
		}
	}
	if oldType == "pan123" {
		protocol := pan123.NormalizeProtocol(merged["protocol"])
		changed := protocol != pan123.NormalizeProtocol(oldParams["protocol"])
		if protocol == pan123.ProtocolWeb {
			changed = changed || merged["web_user"] != oldParams["web_user"] || merged["web_password"] != oldParams["web_password"]
		} else {
			changed = changed || merged["client_id"] != oldParams["client_id"] || merged["client_secret"] != oldParams["client_secret"]
		}
		if changed && !explicitToken {
			delete(merged, "access_token")
		}
		if changed || clearSet["access_token"] || explicitToken {
			if !explicitToken || merged["token_expires_at"] == oldParams["token_expires_at"] {
				delete(merged, "token_expires_at")
			}
		} else if merged["token_expires_at"] == "" && oldParams["token_expires_at"] != "" {
			merged["token_expires_at"] = oldParams["token_expires_at"]
		}
	}
	if oldType == "sftp" && merged["known_hosts"] == "" && !clearSet["known_hosts"] && oldParams["known_hosts"] != "" {
		oldHost, oldPort := normalizeSFTPEndpoint(oldParams)
		newHost, newPort := normalizeSFTPEndpoint(merged)
		if oldHost == newHost && oldPort == newPort {
			merged["known_hosts"] = oldParams["known_hosts"]
		}
	}
	return merged, skipObscure, nil
}

func precheckAdditionsAgainstExisting(existing, additions native.Config, replace bool) error {
	for name, params := range additions {
		if matched, ok := existing.FindKey(name); ok {
			if !replace {
				return publicError(fmt.Sprintf("存储 %q 已存在，请使用其他名称或选择编辑", matched))
			}
			if existing[matched]["type"] != params["type"] {
				return publicError("同名存储的类型不能修改")
			}
		}
	}
	return nil
}

func SaveNativeStorage(ctx context.Context, in NativeStorageInput) error {
	if in.MaxConcurrentJobs != nil {
		if *in.MaxConcurrentJobs < 0 || *in.MaxConcurrentJobs > 50 {
			return publicError("最大并发任务数必须在 0–50 之间")
		}
	}
	additions := native.Config{}
	var skipObscure map[string]map[string]bool
	authSessionID := strings.TrimSpace(in.AuthSessionID)
	if in.Config != "" {
		if len(in.Options) != 0 {
			return publicError("请选择表单或配置文件其中一种方式")
		}
		var e error
		additions, e = native.ImportSelected(ctx, in.Config, in.ConfigPassword, nil)
		if e != nil {
			return publicError(e.Error())
		}
	} else if len(in.Options) != 0 {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return publicError("请输入存储名称")
		}
		clean := native.NormalizeParams(in.Options)
		if clean["type"] == "" {
			return publicError("请选择存储类型")
		}
		merged, skipped, mergeErr := mergeAuthSessionIntoParams(authSessionID, clean, nil, nil)
		if mergeErr != nil {
			return mergeErr
		}
		additions[name] = merged
		if len(skipped) > 0 {
			skipObscure = map[string]map[string]bool{name: skipped}
		}
	} else if strings.TrimSpace(in.Name) != "" {
		return publicError("请填写存储配置参数")
	}

	// Read a snapshot outside locks so Prepare (which may dial SFTP for TOFU
	// host key pinning) never blocks engine/job mutations on network I/O.
	_, existingSnapshot, err := loadExistingNativeRow()
	if err != nil {
		return err
	}
	if len(additions) > 0 {
		if err := precheckAdditionsAgainstExisting(existingSnapshot, additions, in.Replace); err != nil {
			return err
		}
		if err := native.PrepareWithSkipObscure(ctx, existingSnapshot, additions, in.Config == "", skipObscure); err != nil {
			return publicError(err.Error())
		}
	}

	alistRefMu.Lock()
	defer alistRefMu.Unlock()
	alistMutationMu.Lock()
	defer alistMutationMu.Unlock()

	id, remark, _, c, e := loadExistingNativeRowFull()
	if e != nil {
		return e
	}

	replaced := false
	var replacedNames []string
	if in.Replace && len(additions) > 0 {
		// Match case-insensitively to align with Config.Validate, and preserve
		// the canonical stored key so existing jobs referencing that remote name
		// keep working without path breakage.
		normalized := native.Config{}
		for name, params := range additions {
			if matched, ok := c.FindKey(name); ok {
				if c[matched]["type"] != params["type"] {
					return publicError("同名存储的类型不能修改")
				}
				delete(c, matched)
				normalized[matched] = params
				replaced = true
				replacedNames = append(replacedNames, matched)
			} else {
				normalized[name] = params
			}
		}
		additions = normalized
	}
	c, e = c.Merge(additions)
	if e != nil {
		return publicError(e.Error())
	}

	// If existing remotes were replaced, ensure jobs affected by the replaced
	// remotes remain valid and non-overlapping against the updated configuration.
	if replaced && id > 0 {
		if err := validateExistingNativeJobs(id, c, replacedNames); err != nil {
			return err
		}
	}

	data, e := json.Marshal(c)
	if e != nil {
		return e
	}
	secret := string(data)
	if id == 0 {
		mc := 1
		if in.MaxConcurrentJobs != nil {
			mc = *in.MaxConcurrentJobs
		}
		id, e = mapper.AddAlistWithConcurrency(remark, native.EngineURL, "rclone", secret, mc)
	} else {
		e = mapper.UpdateAlistWithConcurrency(id, remark, native.EngineURL, &secret, in.MaxConcurrentJobs)
		if e == nil && in.MaxConcurrentJobs != nil {
			engineQueue.RefreshLimit(id, *in.MaxConcurrentJobs)
		}
	}
	if e == nil && id > 0 {
		refreshNativeStorageConcurrencyLimits(c)
		RemoveEngineHealth(id)
	}
	if e == nil && authSessionID != "" {
		native.ConsumeAuthSession(authSessionID)
	}
	return e
}

// UpdateNativeStorage edits and optionally renames an existing remote, preserving
// omitted secret fields and re-validating dependent jobs.
func UpdateNativeStorage(ctx context.Context, in NativeRemoteEditInput) error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return publicError("请指定要编辑的存储名称")
	}
	if len(in.Options) == 0 {
		return publicError("请填写存储配置参数")
	}

	_, existingSnapshot, err := loadExistingNativeRow()
	if err != nil {
		return err
	}
	canonicalName, ok := existingSnapshot.FindKey(name)
	if !ok {
		return publicError(fmt.Sprintf("存储 %q 不存在", name))
	}
	mergedParams, skipFields, err := mergeEditRemoteParams(existingSnapshot[canonicalName], in.Options, in.ClearSecrets)
	if err != nil {
		return err
	}
	authSessionID := strings.TrimSpace(in.AuthSessionID)
	mergedParams, skipFields, err = mergeAuthSessionIntoParams(authSessionID, mergedParams, in.ClearSecrets, skipFields)
	if err != nil {
		return err
	}
	additions := native.Config{canonicalName: mergedParams}
	skipObscure := map[string]map[string]bool{canonicalName: skipFields}
	if err := native.PrepareWithSkipObscure(ctx, existingSnapshot, additions, true, skipObscure); err != nil {
		return publicError(err.Error())
	}

	if newName := strings.TrimSpace(in.NewName); newName != "" && newName != canonicalName {
		if _, err := renameNativeStorage(NativeRenameInput{OldName: canonicalName, NewName: newName}, existingSnapshot[canonicalName], additions[canonicalName]); err != nil {
			return err
		}
		if authSessionID != "" {
			native.ConsumeAuthSession(authSessionID)
		}
		return nil
	}

	alistRefMu.Lock()
	defer alistRefMu.Unlock()
	alistMutationMu.Lock()
	defer alistMutationMu.Unlock()

	id, remark, _, c, err := loadExistingNativeRowFull()
	if err != nil {
		return err
	}
	if id == 0 {
		return publicError("未找到内置存储引擎")
	}
	liveName, exists := c.FindKey(canonicalName)
	if !exists {
		return publicError(fmt.Sprintf("存储 %q 已被删除", canonicalName))
	}
	if c[liveName]["type"] != additions[canonicalName]["type"] {
		return publicError("同名存储的类型不能修改")
	}
	c[liveName] = additions[canonicalName]
	if err := c.Validate(); err != nil {
		return publicError(err.Error())
	}
	if err := validateExistingNativeJobs(id, c, []string{liveName}); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	secret := string(data)
	if err := mapper.UpdateAlist(id, remark, native.EngineURL, &secret); err != nil {
		return err
	}
	refreshNativeStorageConcurrencyLimits(c)
	RemoveEngineHealth(id)
	if authSessionID != "" {
		native.ConsumeAuthSession(authSessionID)
	}
	return nil
}

// PreviewNativeImport inspects an uploaded rclone.conf against the currently
// stored native configuration without mutating state or dialing remote hosts.
func PreviewNativeImport(ctx context.Context, in NativeImportInput) (*native.ImportPreview, error) {
	if strings.TrimSpace(strings.TrimPrefix(in.Config, "\uFEFF")) == "" {
		return nil, publicError("请先选择非空的 rclone.conf 文件")
	}
	_, existingSnapshot, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	preview, err := native.InspectImport(ctx, in.Config, in.ConfigPassword, existingSnapshot)
	if err != nil {
		return nil, publicError(err.Error())
	}
	return preview, nil
}

// ImportNativeStorage imports selected sections from an uploaded rclone.conf.
func ImportNativeStorage(ctx context.Context, in NativeImportInput) (*NativeImportResult, error) {
	if strings.TrimSpace(strings.TrimPrefix(in.Config, "\uFEFF")) == "" {
		return nil, publicError("请先选择非空的 rclone.conf 文件")
	}
	additions, err := native.ImportSelected(ctx, in.Config, in.ConfigPassword, in.Selected)
	if err != nil {
		return nil, publicError(err.Error())
	}
	_, existingSnapshot, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	if err := precheckAdditionsAgainstExisting(existingSnapshot, additions, in.Replace); err != nil {
		return nil, err
	}
	if err := native.Prepare(ctx, existingSnapshot, additions, false); err != nil {
		return nil, publicError(err.Error())
	}

	alistRefMu.Lock()
	defer alistRefMu.Unlock()
	alistMutationMu.Lock()
	defer alistMutationMu.Unlock()

	id, remark, _, c, err := loadExistingNativeRowFull()
	if err != nil {
		return nil, err
	}
	result := &NativeImportResult{
		Imported: []string{},
		Replaced: []string{},
	}
	normalized := native.Config{}
	for name, params := range additions {
		if matched, ok := c.FindKey(name); ok {
			if !in.Replace {
				return nil, publicError(fmt.Sprintf("存储 %q 已存在，请勾选覆盖同名存储后重试", matched))
			}
			if c[matched]["type"] != params["type"] {
				return nil, publicError("同名存储的类型不能修改")
			}
			delete(c, matched)
			normalized[matched] = params
			result.Replaced = append(result.Replaced, matched)
		} else {
			normalized[name] = params
			result.Imported = append(result.Imported, name)
		}
	}
	merged, err := c.Merge(normalized)
	if err != nil {
		return nil, publicError(err.Error())
	}
	if len(result.Replaced) > 0 && id > 0 {
		if err := validateExistingNativeJobs(id, merged, result.Replaced); err != nil {
			return nil, err
		}
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	secret := string(data)
	if id == 0 {
		id, err = mapper.AddAlistWithConcurrency(remark, native.EngineURL, "rclone", secret, 1)
	} else {
		err = mapper.UpdateAlist(id, remark, native.EngineURL, &secret)
	}
	if err != nil {
		return nil, err
	}
	RemoveEngineHealth(id)
	slices.Sort(result.Imported)
	slices.Sort(result.Replaced)
	return result, nil
}

// TestNativeStorage verifies connectivity either for an already stored remote
// (when Options is empty) or for an unsaved form draft (when Options is given).
func TestNativeStorage(ctx context.Context, in NativeStorageTestInput) (*NativeStorageTestResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id, _, _, existing, err := loadExistingNativeRowFull()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if err := native.Available(probeCtx); err != nil {
		return &NativeStorageTestResult{
			OK:        false,
			LatencyMs: time.Since(start).Milliseconds(),
			Message:   err.Error(),
		}, nil
	}

	testCfg := native.Config{}
	for k, v := range existing {
		testCfg[k] = maps.Clone(v)
	}
	var targetName string
	if len(in.Options) == 0 {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, publicError("请指定要测试的存储名称")
		}
		if name == native.Local {
			roots := native.AuthorizedRoots()
			if len(roots) == 0 {
				return &NativeStorageTestResult{
					OK:        false,
					LatencyMs: time.Since(start).Milliseconds(),
					Message:   "当前未检测到飞牛授权目录，请在应用设置中授权文件夹",
				}, nil
			}
			return &NativeStorageTestResult{
				OK:        true,
				LatencyMs: time.Since(start).Milliseconds(),
				Message:   fmt.Sprintf("已检测到 %d 个本地授权目录", len(roots)),
				DirsCount: len(roots),
			}, nil
		}
		matched, ok := testCfg.FindKey(name)
		if !ok {
			return nil, publicError(fmt.Sprintf("存储 %q 不存在", name))
		}
		targetName = matched
	} else {
		name := strings.TrimSpace(in.Name)
		if in.Editing {
			if name == "" {
				return nil, publicError("请指定要测试的存储名称")
			}
			matched, ok := testCfg.FindKey(name)
			if !ok {
				return nil, publicError(fmt.Sprintf("存储 %q 不存在", name))
			}
			mergedParams, skipFields, mergeErr := mergeEditRemoteParams(testCfg[matched], in.Options, in.ClearSecrets)
			if mergeErr != nil {
				return nil, mergeErr
			}
			mergedParams, skipFields, mergeErr = mergeAuthSessionIntoParams(in.AuthSessionID, mergedParams, in.ClearSecrets, skipFields)
			if mergeErr != nil {
				return nil, mergeErr
			}
			additions := native.Config{matched: mergedParams}
			if err := native.PrepareWithSkipObscure(probeCtx, testCfg, additions, true, map[string]map[string]bool{matched: skipFields}); err != nil {
				return nil, publicError(err.Error())
			}
			testCfg[matched] = additions[matched]
			targetName = matched
		} else {
			if name == "" {
				// Allow testing a draft form before the user enters a name.
				name = "preview_storage"
				for i := 1; ; i++ {
					if _, exists := testCfg.FindKey(name); !exists {
						break
					}
					name = fmt.Sprintf("preview_storage_%d", i)
				}
			} else if matched, exists := testCfg.FindKey(name); exists {
				// Replace only in the temporary in-memory test config.
				delete(testCfg, matched)
			}
			clean := native.NormalizeParams(in.Options)
			if clean["type"] == "" {
				return nil, publicError("请选择存储类型")
			}
			mergedParams, skipFields, mergeErr := mergeAuthSessionIntoParams(in.AuthSessionID, clean, nil, nil)
			if mergeErr != nil {
				return nil, mergeErr
			}
			additions := native.Config{name: mergedParams}
			var skipMap map[string]map[string]bool
			if len(skipFields) > 0 {
				skipMap = map[string]map[string]bool{name: skipFields}
			}
			if err := native.PrepareWithSkipObscure(probeCtx, testCfg, additions, true, skipMap); err != nil {
				return nil, publicError(err.Error())
			}
			testCfg[name] = additions[name]
			targetName = name
		}
		if err := testCfg.Validate(); err != nil {
			return nil, publicError(err.Error())
		}
	}

	params := testCfg[targetName]
	probePath := "/" + targetName
	if params["type"] == "s3" && id > 0 && (len(in.Options) == 0 || in.Editing) {
		if bucketPath, ok := collectS3ProbeBuckets(id, testCfg)[targetName]; ok {
			probePath = bucketPath
		}
	}
	entries, listErr := testCfg.ListWithOptions(probeCtx, probePath, native.SessionOptions{
		ProxyURL:            globalNativeProxyURL(),
		DisableTokenPersist: len(in.Options) > 0,
	})
	latency := time.Since(start).Milliseconds()
	if listErr != nil {
		if params["type"] == "s3" && params["no_check_bucket"] == "true" && strings.Contains(listErr.Error(), "403") {
			return &NativeStorageTestResult{
				OK:        true,
				LatencyMs: latency,
				Message:   "端点连接正常，当前密钥无列桶权限（403）",
				Warning:   "单桶权限密钥无法列出全部存储桶，保存后可在同步任务中手动输入 /存储名/存储桶/目录",
			}, nil
		}
		return &NativeStorageTestResult{
			OK:        false,
			LatencyMs: latency,
			Message:   listErr.Error(),
		}, nil
	}
	return &NativeStorageTestResult{
		OK:        true,
		LatencyMs: latency,
		Message:   fmt.Sprintf("连接正常，可读取 %d 个目录/存储桶", len(entries)),
		DirsCount: len(entries),
	}, nil
}

func jobUsesAnyRemote(job map[string]interface{}, c native.Config, affected map[string]bool) bool {
	if len(affected) == 0 {
		return true
	}
	for _, p := range append(parsePathList(job["srcPath"]), parsePathList(job["dstPath"])...) {
		seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
		if canonical, ok := c.FindKey(seg); ok && affected[canonical] {
			return true
		}
	}
	return false
}

func validateExistingNativeJobs(alistID int64, c native.Config, changedRemotes []string) error {
	rows, err := mapper.GetJobListAll()
	if err != nil {
		return err
	}
	var affectedSet map[string]bool
	if len(changedRemotes) > 0 {
		affectedSet = c.AffectedRemotes(changedRemotes)
	}
	type target struct {
		virtual string
		ep      native.Endpoint
	}
	var allTargets []target
	var affectedJobs []map[string]interface{}
	for _, job := range rows {
		if util.ToInt64(job["alistId"]) != alistID {
			continue
		}
		if jobUsesAnyRemote(job, c, affectedSet) {
			affectedJobs = append(affectedJobs, job)
			continue
		}
		// Unrelated jobs (e.g. pointing to a temporarily unplugged local drive)
		// must not block editing an unrelated remote's credentials; still record
		// any currently resolvable writable targets so affected jobs cannot
		// collide with them.
		for _, v := range writablePathsForJob(job) {
			if ep, err := c.Resolve(v); err == nil {
				allTargets = append(allTargets, target{v, ep})
			}
		}
	}
	for _, job := range affectedJobs {
		if err := validateNativeSyncPaths(c, job); err != nil {
			return publicError(fmt.Sprintf("替换配置后任务「%s」路径失效：%s", jobDisplayName(job), err.Error()))
		}
		for _, v := range writablePathsForJob(job) {
			ep, err := c.Resolve(v)
			if err != nil {
				return publicError(fmt.Sprintf("替换配置后任务「%s」写入路径失效：%s", jobDisplayName(job), err.Error()))
			}
			for _, used := range allTargets {
				if native.EndpointsConflict(used.ep, ep) {
					return publicError(fmt.Sprintf("替换配置后任务「%s」与已有写入目录重叠：%s", jobDisplayName(job), used.virtual))
				}
			}
			allTargets = append(allTargets, target{v, ep})
		}
	}
	return nil
}

func jobDisplayName(job map[string]interface{}) string {
	if s := strings.TrimSpace(util.StringValue(job["remark"])); s != "" {
		return s
	}
	return fmt.Sprintf("#%d", util.ToInt64(job["id"]))
}

// DeleteNativeStorage removes a single named rclone remote from the native
// engine after verifying no crypt remote or sync job still references it.
func DeleteNativeStorage(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return publicError("请指定要删除的存储名称")
	}

	alistRefMu.Lock()
	defer alistRefMu.Unlock()
	alistMutationMu.Lock()
	defer alistMutationMu.Unlock()

	id, remark, _, c, err := loadExistingNativeRowFull()
	if err != nil {
		return err
	}
	if id == 0 {
		return publicError("未找到内置存储引擎")
	}

	target, ok := c.FindKey(name)
	if !ok {
		return publicError(fmt.Sprintf("存储 %q 不存在", name))
	}

	// Prevent breaking composite remotes (crypt/chunker/compress/hasher/union) that layer on top of this remote.
	for otherName, params := range c {
		if otherName == target {
			continue
		}
		typ := params["type"]
		if native.IsWrapperType(typ) {
			ref, _, _ := strings.Cut(params["remote"], ":")
			if strings.EqualFold(ref, target) {
				return publicError(fmt.Sprintf("存储 %q 正被组合存储 %q（%s）引用，请先删除对应存储", target, otherName, typ))
			}
		} else if typ == "union" {
			if ups, err := native.ParseUnionUpstreams(params["upstreams"]); err == nil {
				for _, up := range ups {
					ref, _, _ := strings.Cut(up.Target, ":")
					if strings.EqualFold(ref, target) {
						return publicError(fmt.Sprintf("存储 %q 正被合并存储 %q（union）引用，请先修改或删除对应存储", target, otherName))
					}
				}
			}
		} else if typ == "combine" {
			if ups, err := native.ParseCombineUpstreams(params["upstreams"]); err == nil {
				for _, up := range ups {
					ref, _, _ := strings.Cut(up.Target, ":")
					if strings.EqualFold(ref, target) {
						return publicError(fmt.Sprintf("存储 %q 正被组合存储 %q（combine）引用，请先修改或删除对应存储", target, otherName))
					}
				}
			}
		}
	}

	// Prevent deleting a remote referenced by any existing native job.
	jobs, err := mapper.GetJobListAll()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if util.ToInt64(job["alistId"]) != id {
			continue
		}
		for _, p := range append(parsePathList(job["srcPath"]), parsePathList(job["dstPath"])...) {
			seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
			if strings.EqualFold(seg, target) {
				return publicError(fmt.Sprintf("存储 %q 正被同步任务「%s」使用，请先修改或删除关联任务", target, jobDisplayName(job)))
			}
		}
	}

	delete(c, target)
	if err := c.Validate(); err != nil {
		return publicError(err.Error())
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	secret := string(data)
	if err := mapper.UpdateAlist(id, remark, native.EngineURL, &secret); err != nil {
		return err
	}
	refreshNativeStorageConcurrencyLimits(c)
	RemoveEngineHealth(id)
	return nil
}

func nativeConfig(row map[string]interface{}) (native.Config, error) {
	if row == nil || row["url"] != native.EngineURL {
		return nil, nil
	}
	c, e := native.Decode(util.StringValue(row["token"]))
	if e != nil {
		return nil, publicError(e.Error())
	}
	return c, nil
}
func loadNative(id int64) (native.Config, error) {
	row, e := alistDeps.GetAlistByID(id)
	if e != nil {
		return nil, e
	}
	return nativeConfig(row)
}

func validateNativeJob(job, row map[string]interface{}) error {
	c, e := nativeConfig(row)
	if c == nil || e != nil {
		return e
	}
	if err := validateNativeSyncPaths(c, job); err != nil {
		return err
	}
	return validateNativeDestinations(c, job)
}

// Compare all resolved sources with all final destinations, including paths
// reached through different remote names for the same physical storage.
func validateNativeSyncPaths(c native.Config, job map[string]interface{}) error {
	method := util.ToInt(job["method"])
	if method < 0 || method > 3 {
		return publicError("不支持的同步方式")
	}
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	if method == 3 && (len(srcPaths) != 1 || len(dstPaths) != 1) {
		return publicError("双向同步仅支持单个源目录与单个目标目录")
	}
	sources := make([]native.Endpoint, 0, len(srcPaths))
	for _, src := range srcPaths {
		ep, err := c.Resolve(src)
		if err != nil {
			return publicError(err.Error())
		}
		if method == 3 && ep.ReadOnly {
			return publicError("双向同步的两端均需支持读写，不能使用只读存储（如 HTTP / Archive）")
		}
		if method == 2 && ep.ReadOnly {
			return publicError("移动模式需要删除源文件，不能使用只读存储（如 HTTP / Archive）作为源")
		}
		sources = append(sources, ep)
	}
	for _, dst := range resolvedDstPathsForJob(job) {
		ep, err := c.Resolve(dst)
		if err != nil {
			return publicError(err.Error())
		}
		if ep.ReadOnly {
			return publicError("只读存储（如 HTTP / Archive）不能作为同步目标")
		}
		for _, src := range sources {
			if native.EndpointsConflict(src, ep) {
				return publicError("同步源与目标实际目录重叠")
			}
		}
	}
	return nil
}

// validateNativeDestinations runs under alistRefMu with the ordinary job path checks.
func validateNativeDestinations(c native.Config, job map[string]interface{}) error {
	rows, err := mapper.GetJobListAll()
	if err != nil {
		return err
	}
	type target struct {
		virtual string
		ep      native.Endpoint
	}
	var targets []target
	for _, other := range rows {
		if util.ToInt64(other["id"]) == util.ToInt64(job["id"]) || util.ToInt64(other["alistId"]) != util.ToInt64(job["alistId"]) {
			continue
		}
		for _, v := range writablePathsForJob(other) {
			if ep, e := c.Resolve(v); e == nil {
				targets = append(targets, target{v, ep})
			}
		}
	}
	for _, task := range activeJobTasks() {
		if util.ToInt64(task.Job["id"]) == util.ToInt64(job["id"]) || util.ToInt64(task.Job["alistId"]) != util.ToInt64(job["alistId"]) {
			continue
		}
		cfg := task.nativeConfig
		if cfg == nil {
			cfg = c
		}
		for _, v := range writablePathsForJob(task.Job) {
			if ep, err := cfg.Resolve(v); err == nil {
				targets = append(targets, target{v, ep})
			}
		}
	}
	for _, v := range writablePathsForJob(job) {
		ep, e := c.Resolve(v)
		if e != nil {
			return publicError(e.Error())
		}
		for _, used := range targets {
			if native.EndpointsConflict(used.ep, ep) {
				return publicError(fmt.Sprintf("同步写入目录与已配置任务的写入目录重叠：%s", used.virtual))
			}
		}
		targets = append(targets, target{v, ep})
	}
	return nil
}

type nativeItemKey struct{ src, dst string }

// nativeRetryFilter matches both individual file retry rows (isPath=0) and
// directory-level retry rows (isPath=1 or empty fileName) against candidate
// source/destination file paths.
type nativeRetryFilter struct {
	mu          sync.Mutex
	files       map[nativeItemKey]bool
	dirs        []nativeItemKey
	matchedDirs map[int]bool
}

func newNativeRetryFilter() *nativeRetryFilter {
	return &nativeRetryFilter{
		files:       make(map[nativeItemKey]bool),
		matchedDirs: make(map[int]bool),
	}
}

func cleanNativeRetryPath(base, name string) string {
	base = strings.TrimSpace(base)
	name = strings.TrimSpace(name)
	if base == "" && name == "" {
		return ""
	}
	joined := path.Clean(path.Join(base, name))
	if joined == "." || joined == "/" {
		return ""
	}
	return joined
}

func (f *nativeRetryFilter) addRow(r map[string]interface{}) {
	if f == nil || r == nil {
		return
	}
	isDir := taskItemObjectFromValue(r["isPath"]) == taskItemPath
	srcRaw := strings.TrimSpace(util.StringValue(r["srcPath"]))
	dstRaw := strings.TrimSpace(util.StringValue(r["dstPath"]))
	name := strings.TrimSpace(util.StringValue(r["fileName"]))
	if isDir || name == "" {
		srcDir := cleanNativeRetryPath(srcRaw, name)
		dstDir := cleanNativeRetryPath(dstRaw, name)
		if srcDir == "" && dstDir == "" {
			return
		}
		f.mu.Lock()
		f.dirs = append(f.dirs, nativeItemKey{src: srcDir, dst: dstDir})
		f.mu.Unlock()
		return
	}
	srcFile := cleanNativeRetryPath(srcRaw, name)
	dstFile := cleanNativeRetryPath(dstRaw, name)
	if srcFile == "" && dstFile == "" {
		return
	}
	f.mu.Lock()
	f.files[nativeItemKey{src: srcFile, dst: dstFile}] = true
	f.mu.Unlock()
}

func (f *nativeRetryFilter) empty() bool {
	if f == nil {
		return true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.files) == 0 && len(f.dirs) == 0
}

func pathOverlapsDir(target, dir string) bool {
	if dir == "" || target == "" {
		return true
	}
	return target == dir || strings.HasPrefix(target, dir+"/") || strings.HasPrefix(dir, target+"/")
}

func pathWithinDir(filePath, dir string) bool {
	if dir == "" {
		return true
	}
	return filePath == dir || strings.HasPrefix(filePath, dir+"/")
}

// markPairMatch records any directory retry entry that overlaps a configured
// sync pair so legitimately empty retried directories do not cause false errors.
func (f *nativeRetryFilter) markPairMatch(pairSrc, pairDst string) {
	if f == nil {
		return
	}
	pairSrc = path.Clean(strings.TrimSpace(pairSrc))
	pairDst = path.Clean(strings.TrimSpace(pairDst))
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, d := range f.dirs {
		if pathOverlapsDir(pairSrc, d.src) && pathOverlapsDir(pairDst, d.dst) {
			f.matchedDirs[i] = true
		}
	}
}

func (f *nativeRetryFilter) matchesAndConsume(srcFile, dstFile string) bool {
	if f == nil {
		return true
	}
	srcFile = path.Clean(strings.TrimSpace(srcFile))
	dstFile = path.Clean(strings.TrimSpace(dstFile))
	f.mu.Lock()
	defer f.mu.Unlock()
	key := nativeItemKey{src: srcFile, dst: dstFile}
	if f.files[key] {
		delete(f.files, key)
		return true
	}
	for i, d := range f.dirs {
		if pathWithinDir(srcFile, d.src) && pathWithinDir(dstFile, d.dst) {
			f.matchedDirs[i] = true
			return true
		}
	}
	return false
}

func (f *nativeRetryFilter) hasUnmatched() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.files) > 0 {
		return true
	}
	return len(f.dirs) > 0 && len(f.matchedDirs) < len(f.dirs)
}

type nativePairsOutcome struct {
	err                  error
	hadFileFailure       bool
	untrackedPairFailure bool
}

func runNativePairs(ctx context.Context, c native.Config, job map[string]interface{}, dry bool, selected *nativeRetryFilter, onEvent func(string, string, native.Event)) nativePairsOutcome {
	return runNativePairsWithCallbacks(ctx, nil, c, job, dry, selected, onEvent, nil)
}

func runNativePairsWithTask(ctx context.Context, jt *JobTask, c native.Config, job map[string]interface{}, dry bool, selected *nativeRetryFilter, onEvent func(string, string, native.Event)) nativePairsOutcome {
	return runNativePairsWithCallbacks(ctx, jt, c, job, dry, selected, onEvent, nil)
}

func runNativePairsWithCallbacks(ctx context.Context, jt *JobTask, c native.Config, job map[string]interface{}, dry bool, selected *nativeRetryFilter, onEvent func(string, string, native.Event), onPairDone func(string, string, error)) nativePairsOutcome {
	backupDir := ""
	if !dry {
		backupDir = nativeTrashBackupSubdir(jt, job)
	}
	baseOpts := buildNativeCopyOptions(job, dry, backupDir)
	if jt != nil {
		baseOpts.Controller = jt.controller()
		if jt.ForceOverwrite || jt.SyncPartial {
			baseOpts.ForceOverwrite = true
		}
		if len(jt.OnlyFiles) > 0 {
			only := make([]native.PendingFile, 0, len(jt.OnlyFiles))
			for _, rel := range jt.OnlyFiles {
				rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
				if rel != "" && rel != "." && !strings.HasPrefix(rel, "../") {
					only = append(only, native.PendingFile{Path: rel})
				}
			}
			if len(only) > 0 {
				baseOpts.OnlyFiles = only
			}
		}
	}

	type pairTarget struct{ src, dst string }
	var pairs []pairTarget
	for _, src := range parsePathList(job["srcPath"]) {
		for _, base := range parsePathList(job["dstPath"]) {
			pairs = append(pairs, pairTarget{src: src, dst: jobDstForSrc(job, src, base)})
		}
	}

	var (
		out     nativePairsOutcome
		mu      sync.Mutex
		errs    []error
		eventMu sync.Mutex
	)

	runOne := func(pt pairTarget) (runErr error) {
		pairFileFailed := false
		defer func() {
			if onPairDone != nil {
				eventMu.Lock()
				onPairDone(pt.src, pt.dst, runErr)
				eventMu.Unlock()
			}
			if runErr != nil && ctx.Err() == nil {
				if jt != nil && jt.SyncPartial && !jt.SyncFullScan && util.ToInt(job["method"]) == 1 {
					if err := mapper.EnqueueSyncChanges(util.ToInt64(job["id"]), nil, true); err != nil {
						runErr = errors.Join(runErr, err)
					}
				}
				mu.Lock()
				errs = append(errs, runErr)
				if !pairFileFailed {
					out.untrackedPairFailure = true
				}
				mu.Unlock()
			}
		}()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := baseOpts.Controller.WaitIfPaused(ctx); err != nil {
			return err
		}
		pairOptions := baseOpts
		if jt != nil && jt.SyncPartial {
			pairOptions.OnlyFiles = nil
			prefix := strings.TrimRight(pt.src, "/") + "/"
			for _, changed := range jt.SyncChangedFiles {
				if strings.HasPrefix(changed, prefix) {
					rel := strings.TrimPrefix(changed, prefix)
					if rel != "" {
						pairOptions.OnlyFiles = append(pairOptions.OnlyFiles, native.PendingFile{Path: rel})
					}
				}
			}
			if len(pairOptions.OnlyFiles) == 0 {
				return nil
			}
		}
		if jt != nil && jt.SyncPartial && len(pairOptions.OnlyFiles) > 0 {
			// Refresh only affected parent directories. This keeps selective runs
			// subject to the same filters and source metadata checks as a full scan.
			dirs := make(map[string]map[string]native.DirEntry)
			var files []native.PendingFile
			for _, pf := range pairOptions.OnlyFiles {
				dir := path.Dir(pf.Path)
				entries, loaded := dirs[dir]
				if !loaded {
					listed, _, err := c.ListDirEntries(ctx, path.Join(pt.src, dir), native.SessionOptions{ProxyURL: globalNativeProxyURL()})
					if err != nil {
						return err
					}
					entries = make(map[string]native.DirEntry, len(listed))
					for _, e := range listed {
						entries[e.Name] = e
					}
					dirs[dir] = entries
				}
				e, exists := entries[path.Base(pf.Path)]
				if !exists || e.IsDir {
					if !jt.SyncFullScan {
						if err := mapper.EnqueueSyncChanges(util.ToInt64(job["id"]), nil, true); err != nil {
							return err
						}
					}
					continue
				}
				if exclude := buildJobExcludeMatcher(job); exclude != nil && exclude.matchesListed(pf.Path, false) {
					continue
				}
				if include := buildJobIncludeMatcher(job); include != nil && !include.matchesListed(pf.Path) {
					continue
				}
				if !jobAllowsFileMeta(job, FileMetadata{Size: e.Size, Modified: e.ModTime}, time.Now()) {
					continue
				}
				files = append(files, native.PendingFile{Path: pf.Path, Size: e.Size})
			}
			if len(files) == 0 {
				return nil
			}
			pairOptions.OnlyFiles = files
		}
		if selected != nil {
			selected.markPairMatch(pt.src, pt.dst)
			pairOptions.Include = func(p string, st os.FileInfo) bool {
				if st.IsDir() {
					return baseOpts.Include(p, st)
				}
				if !baseOpts.Include(p, st) {
					return false
				}
				return selected.matchesAndConsume(path.Join(pt.src, p), path.Join(pt.dst, p))
			}
		}
		e := c.Copy(ctx, pt.src, pt.dst, pairOptions, func(v native.Event) {
			if v.Failed {
				pairFileFailed = true
				mu.Lock()
				out.hadFileFailure = true
				mu.Unlock()
			}
			eventMu.Lock()
			onEvent(pt.src, pt.dst, v)
			eventMu.Unlock()
		})
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e
		}
		return nil
	}

	// Use bounded concurrent pair execution for multi-pair live runs without
	// targeted retry filters; keep sequential order for dry-run and single-pair.
	if !dry && selected == nil && len(pairs) > 1 {
		workers := min(nativePairMaxConcurrency, len(pairs))
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		for _, pt := range pairs {
			pt := pt
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				_ = runOne(pt)
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			out.err = ctx.Err()
			return out
		}
	} else {
		for _, pt := range pairs {
			if e := runOne(pt); e != nil {
				if ctx.Err() != nil {
					out.err = ctx.Err()
					return out
				}
				if dry {
					out.err = e
					return out
				}
			}
		}
	}

	if selected != nil && selected.hasUnmatched() && len(errs) == 0 {
		out.err = errors.New("部分重试文件已移动、被过滤或不属于当前任务")
		out.untrackedPairFailure = true
		return out
	}
	if len(errs) > 0 {
		out.err = errors.Join(errs...)
	}
	return out
}

// checkDestinationFreeSpace enforces the global minFreeSpaceMB safety floor
// before a task writes to any destination storage that reports free capacity.
func checkDestinationFreeSpace(ctx context.Context, cfg native.Config, job map[string]interface{}) error {
	minMB := config.GetSystemSettings().MinFreeSpaceMB
	if minMB <= 0 {
		return nil
	}
	minBytes := int64(minMB) * 1024 * 1024
	checked := make(map[string]struct{})
	for _, dst := range parsePathList(job["dstPath"]) {
		seg, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(dst), "/"), "/")
		if seg == "" {
			continue
		}
		if _, done := checked[seg]; done {
			continue
		}
		checked[seg] = struct{}{}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		info, err := cfg.About(probeCtx, seg, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
		cancel()
		if err != nil || info == nil || !info.Supported || info.Free == nil {
			continue
		}
		if *info.Free < minBytes {
			return fmt.Errorf("目标存储 %q 剩余空间不足（当前可用 %s，低于安全阈值 %d MB），已中止同步保护磁盘", seg, util.ConvertBytes(*info.Free), minMB)
		}
	}
	return nil
}

func (jt *JobTask) runNative() {
	defer jt.cancelTaskRuntime()
	defer func() {
		if r := recover(); r != nil {
			_ = jt.persistRemainingTaskItems()
			jt.finishFailedTask("原生备份执行异常")
		}
	}()
	// A newly published task must not validate between another request's
	// conflict check and row insertion. Otherwise both could claim the same
	// old target while an edit and task startup are crossing.
	alistRefMu.Lock()
	validationErr := validateNativeSyncPaths(jt.nativeConfig, jt.Job)
	if validationErr == nil {
		validationErr = validateNativeDestinations(jt.nativeConfig, jt.Job)
	}
	alistRefMu.Unlock()
	if validationErr != nil {
		jt.finishFailedTask(validationErr.Error())
		return
	}
	if err := checkDestinationFreeSpace(jt.context(), jt.nativeConfig, jt.Job); err != nil {
		jt.finishFailedTask(err.Error())
		return
	}
	var selected *nativeRetryFilter
	if jt.hasRetrySource() {
		selected = newNativeRetryFilter()
		collect := func(rows []map[string]interface{}) error {
			for _, r := range rows {
				selected.addRow(r)
			}
			return jt.context().Err()
		}
		var err error
		if jt.RetryJobItems && len(jt.RetryItemIDs) > 0 {
			err = mapper.ForEachJobTaskItemsByJobAndIDs(jt.jobIDSnapshot(), jt.RetryItemIDs, retryableStatusValues(), retryTaskItemBatchSize, collect)
		} else if len(jt.RetryItemIDs) > 0 {
			err = mapper.ForEachJobTaskItemsByIDs(jt.RetrySourceTaskID, jt.RetryItemIDs, retryableStatusValues(), retryTaskItemBatchSize, collect)
		} else {
			err = mapper.ForEachJobTaskItemsByStatuses(jt.RetrySourceTaskID, retryableStatusValues(), retryTaskItemBatchSize, collect)
		}
		if err != nil || selected.empty() {
			jt.finishFailedTask("无法读取原生备份重试清单")
			return
		}
	}

	method := util.ToInt(jt.Job["method"])
	if jt.SyncFullScan && method == 0 && len(jt.SyncChangedFiles) > 0 {
		jt.SyncPartial = true
		err := jt.copyContinuousChanges()
		jt.SyncPartial = false
		if err != nil {
			if jt.context().Err() == nil {
				jt.setFatalError(err.Error())
			}
			jt.ScanFinish.Store(true)
			jt.finishSubmittedTask(jt.persistRemainingTaskItems())
			return
		}
	}
	if jt.SyncPartial && method == 1 {
		method = 0
	}
	// Targeted repair runs (from consistency check one-click repair) always copy
	// only the differing/missing files without running destructive mirror/move phases.
	if len(jt.OnlyFiles) > 0 && jt.ForceOverwrite {
		method = 0
	}
	switch method {
	case 1:
		err := jt.runNativeFullSync(selected)
		jt.ScanFinish.Store(true)
		jt.Waiting.reset()
		jt.nativeStats.Store(nil)
		if jt.context().Err() == nil {
			jt.drained.Store(true)
		}
		persist := jt.persistRemainingTaskItems()
		if err != nil && !jt.hasAnyFailedItem() && jt.context().Err() == nil {
			jt.finishFailedTask(err.Error())
			return
		}
		jt.finishSubmittedTask(persist)
		return

	case 2:
		err := jt.runNativeMove(selected)
		jt.ScanFinish.Store(true)
		jt.Waiting.reset()
		jt.nativeStats.Store(nil)
		if jt.context().Err() == nil {
			jt.drained.Store(true)
		}
		persist := jt.persistRemainingTaskItems()
		if err != nil && !jt.hasAnyFailedItem() && jt.context().Err() == nil {
			jt.finishFailedTask(err.Error())
			return
		}
		jt.finishSubmittedTask(persist)
		return

	case 3:
		err := jt.runNativeBisync()
		jt.ScanFinish.Store(true)
		jt.Waiting.reset()
		jt.nativeStats.Store(nil)
		if jt.context().Err() == nil {
			jt.drained.Store(true)
		}
		persist := jt.persistRemainingTaskItems()
		if err != nil && !jt.hasAnyFailedItem() && jt.context().Err() == nil {
			jt.finishFailedTask(err.Error())
			return
		}
		jt.finishSubmittedTask(persist)
		return
	}

	// Method 0: Incremental copy with multi-pair progress aggregation & waiting queue.
	type pairProgress struct {
		scannedDirs  int64
		scannedFiles int64
		checkedFiles int64
		bytes        int64
		totalBytes   int64
		transfers    int64
		totalTrans   int64
		speed        float64
		scanDone     bool
		done         bool
		completed    map[string]struct{}
		transferring []native.TransferItem
		src, dst     string
	}
	totalPairs := len(parsePathList(jt.Job["srcPath"])) * len(parsePathList(jt.Job["dstPath"]))
	extraFlags, _, _ := parseNativeExtraFlags(jt.Job["extraFlags"])
	pairState := map[nativeItemKey]*pairProgress{}
	getPair := func(src, dst string) *pairProgress {
		k := nativeItemKey{src, dst}
		ps := pairState[k]
		if ps == nil {
			ps = &pairProgress{src: src, dst: dst, completed: make(map[string]struct{})}
			pairState[k] = ps
		}
		return ps
	}
	publishAggregateStats := func(lastSrc, lastDst string) {
		var agg native.Stats
		var totalDirs, totalFiles, totalChecked int64
		var transferring []nativeTransferringItem
		finishedCheckPairs := 0
		for _, ps := range pairState {
			totalDirs += ps.scannedDirs
			totalFiles += ps.scannedFiles
			totalChecked += ps.checkedFiles
			agg.Bytes += ps.bytes
			agg.TotalBytes += ps.totalBytes
			agg.Transfers += ps.transfers
			agg.TotalTransfers += ps.totalTrans
			agg.Speed += ps.speed
			for _, ti := range ps.transferring {
				agg.Transferring = append(agg.Transferring, ti)
				transferring = append(transferring, nativeTransferringItem{
					TransferItem: ti,
					srcBase:      ps.src,
					dstBase:      ps.dst,
				})
			}
			pairCheckDone := ps.done ||
				(ps.scanDone && !extraFlags.DirectStream && (ps.scannedFiles == 0 || ps.checkedFiles >= ps.scannedFiles || ps.totalTrans >= ps.scannedFiles))
			if pairCheckDone {
				finishedCheckPairs++
			}
		}
		if totalPairs > 0 && finishedCheckPairs >= totalPairs {
			jt.ScanFinish.Store(true)
		}
		jt.ScanTotalDirs.Store(totalDirs)
		jt.ScanDoneDirs.Store(totalDirs)
		jt.ScanTotalFiles.Store(totalFiles)
		jt.ScanCheckedFiles.Store(totalChecked)
		jt.nativeStats.Store(&nativeProgressSnapshot{
			src:          lastSrc,
			dst:          lastDst,
			stats:        &agg,
			transferring: transferring,
		})
		jt.notifyProgressChange()
	}

	outcome := runNativePairsWithCallbacks(jt.context(), jt, jt.nativeConfig, jt.Job, false, selected, func(src, dst string, v native.Event) {
		ps := getPair(src, dst)
		if len(v.PendingFiles) > 0 {
			jt.populateNativeWaiting(src, dst, v.PendingFiles)
		}
		if v.ScanDirs > 0 || v.ScanFiles > 0 || v.ScanDone {
			if v.ScanDirs > ps.scannedDirs {
				ps.scannedDirs = v.ScanDirs
			}
			if v.ScanFiles > ps.scannedFiles {
				ps.scannedFiles = v.ScanFiles
			}
			if v.ScanDone {
				ps.scanDone = true
			}
			publishAggregateStats(src, dst)
			return
		}
		if v.Stats != nil {
			if v.Stats.Bytes > 0 || v.Stats.Transfers > 0 || len(v.Stats.Transferring) > 0 {
				jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
			}
			checked := v.Stats.Checks
			if ps.scannedFiles > 0 && checked > ps.scannedFiles {
				checked = ps.scannedFiles
			}
			if checked > ps.checkedFiles {
				ps.checkedFiles = checked
			}
			ps.bytes = v.Stats.Bytes
			ps.totalBytes = v.Stats.TotalBytes
			ps.transfers = v.Stats.Transfers
			ps.totalTrans = v.Stats.TotalTransfers
			ps.speed = v.Stats.Speed
			ps.transferring = filterCompletedTransferring(v.Stats.Transferring, ps.completed)
			publishAggregateStats(src, dst)
			return
		}
		if v.File == "" {
			return
		}
		jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
		rel := path.Clean(v.File)
		ps.completed[rel] = struct{}{}
		if len(ps.transferring) > 0 {
			filtered := filterCompletedTransferring(ps.transferring, ps.completed)
			if len(filtered) != len(ps.transferring) {
				ps.transferring = filtered
				publishAggregateStats(src, dst)
			}
		}
		srcDir := path.Join(src, path.Dir(rel))
		dstDir := path.Join(dst, path.Dir(rel))
		base := path.Base(rel)
		jt.Waiting.removeMatching(srcDir, dstDir, base)
		status := taskStatusSuccess
		var itemErr *string
		if v.Failed {
			text := strings.TrimSpace(v.Error)
			if text == "" {
				text = "rclone 未完成此文件的传输"
			}
			itemErr = &text
			status = taskStatusFailed
		}
		jt.appendFinish(NewCopyJobTaskItem(jt.TaskID, srcDir, dstDir, base, v.Size, "", status, itemErr, taskItemFile, taskItemTypeCopy, time.Now().Unix()))
	}, func(src, dst string, pairErr error) {
		ps := getPair(src, dst)
		ps.done = true
		ps.scanDone = true
		ps.speed = 0
		ps.transferring = nil
		if ps.scannedFiles > ps.checkedFiles {
			ps.checkedFiles = ps.scannedFiles
		}
		if pairErr == nil {
			if ps.totalBytes > ps.bytes {
				ps.bytes = ps.totalBytes
			}
			if ps.totalTrans > ps.transfers {
				ps.transfers = ps.totalTrans
			}
		}
		publishAggregateStats(src, dst)
	})
	if jt.context().Err() == nil {
		cleanupNativeExpiredTrash(jt.context(), jt.nativeConfig, jt.TaskID, jt.Job)
	} else {
		var fallbackPairs [][2]string
		for _, src := range parsePathList(jt.Job["srcPath"]) {
			for _, base := range parsePathList(jt.Job["dstPath"]) {
				fallbackPairs = append(fallbackPairs, [2]string{src, jobDstForSrc(jt.Job, src, base)})
			}
		}
		jt.recordInterruptedPendingItems(fallbackPairs)
	}
	jt.ScanFinish.Store(true)
	jt.Waiting.reset()
	jt.nativeStats.Store(nil)
	if jt.context().Err() == nil {
		jt.drained.Store(true)
	}
	persist := jt.persistRemainingTaskItems()
	if outcome.err != nil && outcome.untrackedPairFailure && jt.context().Err() == nil {
		jt.finishFailedTask(outcome.err.Error())
		return
	}
	jt.finishSubmittedTask(persist)
}

// copyContinuousChanges preserves known source edits even when a directory
// event or audit also requires a complete mirror plan. In particular, size-only
// WebDAV comparison must not discard a file known to have changed locally.
func (jt *JobTask) copyContinuousChanges() error {
	out := runNativePairsWithTask(jt.context(), jt, jt.nativeConfig, jt.Job, false, nil, func(src, dst string, v native.Event) {
		if v.Stats != nil {
			stats := *v.Stats
			jt.nativeStats.Store(&nativeProgressSnapshot{src: src, dst: dst, stats: &stats})
			jt.notifyProgressChange()
			return
		}
		if v.File == "" || v.Skipped {
			return
		}
		jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
		status := taskStatusSuccess
		var itemErr *string
		if v.Failed {
			status = taskStatusFailed
			message := v.Error
			itemErr = &message
		}
		jt.appendFinish(NewCopyJobTaskItem(jt.TaskID, path.Join(src, path.Dir(v.File)), path.Join(dst, path.Dir(v.File)), path.Base(v.File), v.Size, "", status, itemErr, taskItemFile, taskItemTypeCopy, time.Now().Unix()))
	})
	jt.nativeStats.Store(nil)
	if out.err != nil {
		return out.err
	}
	if out.hadFileFailure {
		return errors.New("部分已变更文件传输失败，已保留待同步记录")
	}
	return nil
}

func decorateNative(row map[string]interface{}) {
	if row["url"] != native.EngineURL {
		return
	}
	row["kind"] = "rclone"
	c, e := nativeConfig(row)
	if e != nil {
		return
	}
	types := map[string]string{}
	details := map[string]map[string]string{}
	for k, p := range c {
		types[k] = p["type"]
		info := map[string]string{
			"type":    p["type"],
			"summary": native.RemoteSummary(p),
		}
		if c.RemoteHasCrypt(k) {
			info["hasCrypt"] = "true"
		}
		details[k] = info
	}
	row["remotes"] = types
	row["remoteDetails"] = details
}

// collectS3ProbeBuckets returns a map of S3 remote name -> sample bucket path
// (e.g. "/my_s3/my-bucket") configured by existing native jobs, so health checks
// can probe bucket-scoped S3 credentials that lack ListAllMyBuckets permission.
func collectS3ProbeBuckets(alistID int64, c native.Config) map[string]string {
	buckets := map[string]string{}
	if alistID <= 0 {
		return buckets
	}
	jobs, err := mapper.GetJobListAll()
	if err != nil {
		return buckets
	}
	for _, job := range jobs {
		if util.ToInt64(job["alistId"]) != alistID {
			continue
		}
		for _, raw := range append(parsePathList(job["dstPath"]), parsePathList(job["srcPath"])...) {
			name, sub, _ := strings.Cut(strings.TrimPrefix(raw, "/"), "/")
			canonical, ok := c.FindKey(name)
			if !ok || c[canonical]["type"] != "s3" || sub == "" {
				continue
			}
			bucket, _, _ := strings.Cut(sub, "/")
			if bucket != "" {
				if _, exists := buckets[canonical]; !exists {
					buckets[canonical] = "/" + canonical + "/" + bucket
				}
			}
		}
	}
	return buckets
}

func nativeHealth(ctx context.Context, row map[string]interface{}, sendAlert bool) *EngineHealth {
	start := time.Now()
	alistID := util.ToInt64(row["id"])
	h := EngineHealth{AlistID: alistID, Status: "online", LastCheckedAt: start.Unix(), StoragesKnown: true, Storages: []EngineStorageStatus{}}
	c, e := nativeConfig(row)
	if e == nil {
		e = native.Available(ctx)
	}
	if e != nil {
		h.Status = "offline"
		h.Error = e.Error()
	} else {
		localStatus := "work"
		if native.AuthorizedCount() == 0 {
			localStatus = "未授权本地目录"
		}
		h.Storages = append(h.Storages, EngineStorageStatus{
			MountPath: "/" + native.Local,
			Driver:    "local",
			Status:    localStatus,
		})

		names := slices.Sorted(maps.Keys(c))
		if len(names) > 0 {
			s3Buckets := collectS3ProbeBuckets(alistID, c)
			results := make([]EngineStorageStatus, len(names))
			sem := make(chan struct{}, 4)
			var wg sync.WaitGroup
			for i, name := range names {
				i, name := i, name
				wg.Add(1)
				go func() {
					defer wg.Done()
					select {
					case sem <- struct{}{}:
						defer func() { <-sem }()
					case <-ctx.Done():
						results[i] = EngineStorageStatus{MountPath: "/" + name, Driver: c[name]["type"], Status: "检测超时"}
						return
					}
					probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					defer cancel()
					params := c[name]
					status := "work"
					probeOpts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}
					if params["type"] == "s3" {
						if bucketPath, hasBucket := s3Buckets[name]; hasBucket {
							// Probe the configured bucket directly when available so
							// bucket-scoped IAM credentials without ListAllMyBuckets pass.
							if _, err := c.ListWithOptions(probeCtx, bucketPath, probeOpts); err != nil {
								status = err.Error()
							}
						} else if params["no_check_bucket"] == "true" {
							// Bucket-scoped S3 with no_check_bucket and no bound job yet:
							// tolerate 403 ListBuckets errors, while still surfacing auth or
							// network errors so broken endpoints/credentials are visible.
							if _, err := c.ListWithOptions(probeCtx, "/"+name, probeOpts); err != nil && !strings.Contains(err.Error(), "403") {
								status = err.Error()
							}
						} else if _, err := c.ListWithOptions(probeCtx, "/"+name, probeOpts); err != nil {
							status = err.Error()
						}
					} else if _, err := c.ListWithOptions(probeCtx, "/"+name, probeOpts); err != nil {
						status = err.Error()
					}
					results[i] = EngineStorageStatus{MountPath: "/" + name, Driver: params["type"], Status: status}
				}()
			}
			wg.Wait()
			h.Storages = append(h.Storages, results...)
		}
	}
	h.LatencyMs = time.Since(start).Milliseconds()
	updateEngineHealthCache(h.AlistID, defaultNativeRemark(util.StringValue(row["remark"])), native.EngineURL, h, sendAlert)
	return &h
}

func nativeBackupKey(url, user, secret string) error {
	if url != native.EngineURL {
		return fmt.Errorf("不支持的旧版引擎地址")
	}
	if user != "rclone" {
		return fmt.Errorf("无效的内置存储引擎")
	}
	if strings.TrimSpace(secret) != "" && !isMaskedSecretValue(secret) && !native.ConfigContainsRedactedSecrets(secret) {
		_, e := native.Decode(secret)
		return e
	}
	return nil
}

// isNativeEmptyConfig reports whether a stored native engine config contains
// no remote credentials (pure local authorized-directory mode: "{}").
func isNativeEmptyConfig(rawURL, token string) bool {
	if rawURL != native.EngineURL {
		return false
	}
	c, err := native.Decode(token)
	return err == nil && len(c) == 0
}
