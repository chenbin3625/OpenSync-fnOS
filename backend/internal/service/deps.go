package service

import (
	"opensync/internal/mapper"
)

// AlistDeps groups the external dependencies used by the storage service layer.
// Production code accesses them through the package-level alistDeps pointer;
// tests replace the whole struct via SetAlistDepsForTest.
type AlistDeps struct {
	GetAlistByID func(int64) (map[string]interface{}, error)
}

// JobDeps groups the external dependencies used by the job / task layer.
type JobDeps struct {
	GetEnableJobList              func() ([]map[string]interface{}, error)
	GetJobListAll                 func() ([]map[string]interface{}, error)
	LastJobTaskCreateTime         func(int64) (int64, error)
	PersistJobTaskItems           func([]map[string]interface{}) error
	ForEachJobTaskItemsByStatuses func(int64, []int, int, func([]map[string]interface{}) error) error
	CountJobTaskItemsByStatuses   func(int64, []int) (int64, error)
}

// NotifyDeps groups the external dependencies used by the notification layer.
type NotifyDeps struct {
	RecordSendOutcome func(int64, int, int64, string) error
}

var alistDeps *AlistDeps
var jobDeps *JobDeps
var notifyDeps *NotifyDeps

func init() {
	alistDeps = newDefaultAlistDeps()
	jobDeps = newDefaultJobDeps()
	notifyDeps = newDefaultNotifyDeps()
}

func newDefaultAlistDeps() *AlistDeps {
	return &AlistDeps{
		GetAlistByID: mapper.GetAlistByID,
	}
}

func newDefaultJobDeps() *JobDeps {
	return &JobDeps{
		GetEnableJobList:              mapper.GetEnableJobList,
		GetJobListAll:                 mapper.GetJobListAll,
		LastJobTaskCreateTime:         mapper.GetLastJobTaskCreateTime,
		PersistJobTaskItems:           mapper.AddJobTaskItemMany,
		ForEachJobTaskItemsByStatuses: mapper.ForEachJobTaskItemsByStatuses,
		CountJobTaskItemsByStatuses:   mapper.CountJobTaskItemsByStatuses,
	}
}

func newDefaultNotifyDeps() *NotifyDeps {
	return &NotifyDeps{
		RecordSendOutcome: mapper.UpdateNotifySendResult,
	}
}

// SetAlistDepsForTest replaces alist deps and returns a restore function.
func SetAlistDepsForTest(d *AlistDeps) func() {
	old := alistDeps
	alistDeps = d
	return func() { alistDeps = old }
}

// SetJobDepsForTest replaces job deps and returns a restore function.
func SetJobDepsForTest(d *JobDeps) func() {
	old := jobDeps
	jobDeps = d
	return func() { jobDeps = old }
}

// SetNotifyDepsForTest replaces notify deps and returns a restore function.
func SetNotifyDepsForTest(d *NotifyDeps) func() {
	old := notifyDeps
	notifyDeps = d
	return func() { notifyDeps = old }
}
