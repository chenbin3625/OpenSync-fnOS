package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"opensync/pkg/util"
	"strings"
)

type moveStatClient interface {
	FreshFileStatContext(context.Context, string, string, int) (FileStat, error)
}

type moveSnapshot struct {
	source FileStat
	target FileStat
}

func validMoveMD5(value string) string {
	value = normalizeMD5(value)
	if len(value) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

// FreshFileStatContext reads the actual refreshed list entry. /fs/get has no
// refresh flag, and reading it after a refresh would depend on cache coherence.
func (c *AlistClient) FreshFileStatContext(ctx context.Context, dir, name string, interval int) (FileStat, error) {
	if err := validateRemoveRequest(dir, []string{name}); err != nil {
		return FileStat{}, err
	}
	req := alistListRequest{Path: dir, Refresh: true, Page: 1, PerPage: alistDeps.FileListPageSize}
	seen := 0
	for {
		if err := c.CheckWaitContext(ctx, dir, interval); err != nil {
			return FileStat{}, err
		}
		data, err := c.PostContext(ctx, "/api/fs/list", req, nil)
		if err != nil {
			return FileStat{}, err
		}
		var page struct {
			Content []struct {
				Name     string          `json:"name"`
				IsDir    bool            `json:"is_dir"`
				Size     *int64          `json:"size"`
				Modified json.RawMessage `json:"modified"`
				HashInfo json.RawMessage `json:"hash_info"`
				Hashinfo string          `json:"hashinfo"`
			} `json:"content"`
			Total *int `json:"total"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return FileStat{}, fmt.Errorf("fresh list decode: %w", err)
		}
		if page.Total == nil {
			return FileStat{}, errors.New("fresh list missing pagination total")
		}
		seen += len(page.Content)
		if seen > maxFileListEntries || (page.Total != nil && (*page.Total < seen || *page.Total > maxFileListEntries)) {
			return FileStat{}, errors.New("fresh list returned an inconsistent total")
		}
		for _, entry := range page.Content {
			if entry.Name == name {
				stat := FileStat{Exists: true, IsDir: entry.IsDir, Modified: parseModified(entry.Modified), MD5: validMoveMD5(md5FromHashFields(entry.HashInfo, entry.Hashinfo))}
				if entry.Size != nil {
					stat.SizeKnown, stat.Size = true, *entry.Size
				}
				return stat, nil
			}
		}
		if seen == *page.Total {
			return FileStat{}, nil
		}
		if len(page.Content) == 0 {
			return FileStat{}, errors.New("fresh list returned an incomplete directory")
		}
		if req.Page == 1 {
			req.PerPage = len(page.Content)
		}
		req.Page++
		req.Refresh = false
	}
}

func (ci *CopyItem) snapshotMove(runtime copyItemRuntime, client copyItemClient) (moveSnapshot, error) {
	if ci.IsPath == taskItemPath {
		return moveSnapshot{}, errors.New("move only removes individual files")
	}
	if err := validateRemoveRequest(ci.SrcPath, []string{ci.FileName}); err != nil {
		return moveSnapshot{}, err
	}
	if syncPathsOverlap([]string{ci.SrcPath}, []string{ci.DstPath}) {
		return moveSnapshot{}, errors.New("move source and destination overlap")
	}
	statClient, ok := client.(moveStatClient)
	if !ok {
		return moveSnapshot{}, errors.New("move requires fresh file metadata")
	}
	source, err := statClient.FreshFileStatContext(runtime.context(), ci.SrcPath, ci.FileName, util.ToInt(runtime.jobConfig()["scanIntervalS"]))
	if err != nil {
		return moveSnapshot{}, fmt.Errorf("move source verification: %w", err)
	}
	size, known := ci.expectedSize()
	if !source.Exists || source.IsDir || !source.SizeKnown || source.Size < 0 || source.Modified <= 0 || !known || source.Size != size {
		return moveSnapshot{}, errors.New("move source is missing, changed or lacks size/modification metadata; source retained")
	}
	target, err := statClient.FreshFileStatContext(runtime.context(), ci.DstPath, ci.FileName, util.ToInt(runtime.jobConfig()["scanIntervalT"]))
	if err != nil {
		return moveSnapshot{}, fmt.Errorf("move destination snapshot: %w", err)
	}
	if target.Exists && target.IsDir {
		return moveSnapshot{}, errors.New("move destination is a directory; source retained")
	}
	if runtime.isBreak() || runtime.context().Err() != nil {
		return moveSnapshot{}, context.Canceled
	}
	return moveSnapshot{source: source, target: target}, nil
}

func (ci *CopyItem) failMove(runtime copyItemRuntime, err error) {
	if runtime.isBreak() || errors.Is(err, context.Canceled) || runtime.context().Err() != nil {
		ci.setStatus(taskStatusStopped)
	} else {
		ci.setFailure(err)
	}
}

func (ci *CopyItem) completeTransfer(runtime copyItemRuntime, client copyItemClient, snapshot moveSnapshot, copyConfirmed bool) {
	if ci.CopyType == taskItemTypeTransfer {
		ci.setProgress(taskStatusRunning, 99, nil)
		if err := ci.removeVerifiedMoveSource(runtime, client, snapshot, copyConfirmed); err != nil {
			ci.failMove(runtime, err)
			return
		}
	}
	ci.setProgress(taskStatusSuccess, 100, nil)
}

func sameMoveSource(source, current FileStat) bool {
	return current.Exists && !current.IsDir && current.SizeKnown && current.Size == source.Size && current.Modified == source.Modified && current.MD5 == source.MD5
}

func (ci *CopyItem) verifyMoveTarget(ctx context.Context, statClient moveStatClient, source FileStat) (FileStat, error) {
	target, err := statClient.FreshFileStatContext(ctx, ci.DstPath, ci.FileName, util.ToInt(ci.copyRuntime().jobConfig()["scanIntervalT"]))
	if err != nil {
		return FileStat{}, fmt.Errorf("move destination verification: %w", err)
	}
	if !target.Exists || target.IsDir || !target.SizeKnown || target.Size != source.Size ||
		(source.MD5 != "" && target.MD5 != "" && !strings.EqualFold(source.MD5, target.MD5)) {
		return FileStat{}, errors.New("move destination is missing or does not match the copied file; source retained")
	}
	return target, nil
}

// Once copying has succeeded, retry only removal. Re-copying here could
// overwrite a destination that another writer changed after the transfer.
func (ci *CopyItem) removeVerifiedMoveSource(runtime copyItemRuntime, client copyItemClient, snapshot moveSnapshot, copyConfirmed bool) error {
	source := snapshot.source
	statClient := client.(moveStatClient)
	ctx := runtime.context()
	for attempt := 0; attempt <= runtimeTaskLimits().MaxRetries; attempt++ {
		if runtime.isBreak() || ctx.Err() != nil {
			return context.Canceled
		}
		target, err := ci.verifyMoveTarget(ctx, statClient, source)
		if err != nil {
			return err
		}
		// No task ID is not proof that a synchronous copy ran. Require new
		// destination metadata or matching content hashes before removing source.
		before := snapshot.target
		if !copyConfirmed && before.Exists &&
			!(source.MD5 != "" && source.MD5 == target.MD5) &&
			!(before.SizeKnown && before.Size != target.Size) &&
			!(before.Modified > 0 && target.Modified > 0 && before.Modified != target.Modified) {
			return errors.New("move copy returned no task ID and destination is unchanged; source retained")
		}
		current, err := statClient.FreshFileStatContext(ctx, ci.SrcPath, ci.FileName, util.ToInt(runtime.jobConfig()["scanIntervalS"]))
		if err != nil {
			return fmt.Errorf("move source recheck: %w", err)
		}
		if !sameMoveSource(source, current) {
			return errors.New("move source changed during transfer; refusing removal")
		}
		if runtime.isBreak() || ctx.Err() != nil {
			return context.Canceled
		}
		removeErr := client.DeleteFileContext(ctx, ci.SrcPath, []string{ci.FileName}, 0)
		if runtime.isBreak() || ctx.Err() != nil {
			return context.Canceled
		}
		after, err := statClient.FreshFileStatContext(ctx, ci.SrcPath, ci.FileName, util.ToInt(runtime.jobConfig()["scanIntervalS"]))
		if err != nil {
			return fmt.Errorf("move removal confirmation: %w", err)
		}
		if !after.Exists {
			if _, err := ci.verifyMoveTarget(ctx, statClient, source); err != nil {
				return err
			}
			if runtime.isBreak() || ctx.Err() != nil {
				return context.Canceled
			}
			return nil
		}
		if !sameMoveSource(source, after) {
			return errors.New("move source changed after removal request; refusing retry")
		}
		if removeErr == nil {
			return errors.New("move source still exists or reappeared after successful removal; refusing retry")
		}
		if attempt == runtimeTaskLimits().MaxRetries {
			return fmt.Errorf("move source removal failed: %w", removeErr)
		}
		if !runtime.waitForBreak(jobDeps.CopyRetryDelay(attempt)) {
			return context.Canceled
		}
	}
	return errors.New("move source removal failed")
}
