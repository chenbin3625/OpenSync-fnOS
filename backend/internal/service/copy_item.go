package service

import (
	"opensync/pkg/util"
	"regexp"
	"sync"
)

// CopyItem represents a queued or active file transfer item for progress reporting.
type CopyItem struct {
	mu          sync.RWMutex
	SrcPath     string
	DstPath     string
	FileName    string
	FileSize    interface{}
	CopyType    taskItemType
	IsPath      taskItemObject
	AlistTaskID string
	Status      taskStatus
	Progress    float64
	ErrMsg      *string
	CreateTime  int64
	DoingKey    int64
}

func (ci *CopyItem) setStatus(status taskStatus) {
	ci.mu.Lock()
	ci.Status = status
	ci.mu.Unlock()
}

func (ci *CopyItem) status() taskStatus {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.Status
}

func (ci *CopyItem) taskID() string {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.AlistTaskID
}

func (ci *CopyItem) progress() float64 {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.Progress
}

func (ci *CopyItem) countableWaitSize() int64 {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	if ci.CopyType == taskItemTypeDelete || ci.FileSize == nil {
		return 0
	}
	return util.ToInt64(ci.FileSize)
}

func (ci *CopyItem) toStreamItem() streamDoingItem {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return streamDoingItem{
		AlistTaskID: ci.AlistTaskID,
		FileName:    ci.FileName,
		SrcPath:     ci.SrcPath,
		DstPath:     ci.DstPath,
		FileSize:    util.ToInt64(ci.FileSize),
		Type:        ci.CopyType.Int(),
		Status:      ci.Status.Int(),
		Progress:    ci.Progress,
		CreateTime:  ci.CreateTime,
	}
}

func (ci *CopyItem) ToMap(taskID int64) map[string]interface{} {
	ci.mu.RLock()
	itemMap := ci.toJobTaskItemLocked(taskID).ToMap()
	itemMap["progress"] = ci.Progress
	ci.mu.RUnlock()
	return itemMap
}

func (ci *CopyItem) ToJobTaskItem(taskID int64) JobTaskItem {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.toJobTaskItemLocked(taskID)
}

func (ci *CopyItem) toJobTaskItemLocked(taskID int64) JobTaskItem {
	if ci.CopyType == taskItemTypeDelete {
		return NewDeleteJobTaskItem(taskID, ci.DstPath, ci.FileName, ci.FileSize,
			ci.Status, ci.ErrMsg, ci.IsPath, ci.CreateTime)
	}
	return NewCopyJobTaskItem(taskID, ci.SrcPath, ci.DstPath, ci.FileName, ci.FileSize,
		ci.AlistTaskID, ci.Status, ci.ErrMsg, ci.IsPath, ci.CopyType, ci.CreateTime)
}

var sensitiveErrorQueryPattern = regexp.MustCompile(`(?i)([?&](?:tempauth|sig|token|access_token|authorization|x-amz-signature|x-amz-security-token|x-amz-credential)=)[^&\s<>"']+`)

func redactSensitiveError(message string) string {
	return sensitiveErrorQueryPattern.ReplaceAllString(message, "$1****")
}

func redactSensitiveErrorPtr(errMsg *string) *string {
	if errMsg == nil {
		return nil
	}
	redacted := redactSensitiveError(*errMsg)
	if redacted == *errMsg {
		return errMsg
	}
	return &redacted
}
