package service

import (
	"strings"

	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
)

// recoverNativeStorageJobs clears retry backoff for enabled jobs after a
// credential or endpoint update. Paused jobs are deliberately left untouched
// so reauthorization never changes the user's pause decision.
func recoverNativeStorageJobs(alistID int64, cfg native.Config, changedRemotes []string) {
	if alistID <= 0 || len(changedRemotes) == 0 {
		return
	}
	affected := cfg.AffectedRemotes(changedRemotes)
	if len(affected) == 0 {
		return
	}
	rows, err := mapper.GetJobListAll()
	if err != nil {
		return
	}
	for _, job := range rows {
		if util.ToInt64(job["alistId"]) != alistID ||
			util.ToInt(job["enable"]) != 1 ||
			!jobUsesAnyRemote(job, cfg, affected) {
			continue
		}
		state, err := mapper.GetSyncState(util.ToInt64(job["id"]))
		if err != nil || state.Error == "" {
			continue
		}
		errorType := state.ErrorType
		if errorType == "" {
			errorType = mapper.ClassifySyncError(state.Error).Type
		}
		if errorType != mapper.SyncErrorAuth {
			continue
		}
		if err := clearSyncErrorForEnabledJob(job); err != nil {
			continue
		}
		jobFSWatchers.ResetPollBackoff(util.ToInt64(job["id"]))
	}
}

// syncErrorForJob adds the endpoint that a generic rclone error can be
// associated with while keeping the persisted human-readable message intact.
func syncErrorForJob(job map[string]interface{}, message string) mapper.SyncError {
	info := mapper.ClassifySyncError(message)
	if info.Endpoint != "" || job == nil {
		return info
	}
	for _, raw := range append(parsePathList(job["srcPath"]), parsePathList(job["dstPath"])...) {
		trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "/")
		segment, _, _ := strings.Cut(trimmed, "/")
		if segment == "" {
			continue
		}
		isLocal := strings.EqualFold(segment, native.Local)
		if info.Side == "local" && isLocal {
			info.Endpoint = raw
			break
		}
		if info.Side == "remote" && !isLocal {
			info.Endpoint = segment
			break
		}
	}
	return info
}

func recordSyncErrorForJob(jobID int64, job map[string]interface{}, message string) error {
	message = redactSensitiveError(message)
	return mapper.RecordSyncError(jobID, message, syncErrorForJob(job, message))
}

func clearSyncErrorForEnabledJob(job map[string]interface{}) error {
	if util.ToInt(job["enable"]) != 1 {
		return nil
	}
	return mapper.ClearSyncError(util.ToInt64(job["id"]))
}
