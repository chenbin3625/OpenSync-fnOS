package service

import (
	"context"
	"opensync/internal/native"
	"testing"
)

func TestJobTaskApplyGlobalBwLimitRespectsOverrides(t *testing.T) {
	// 1. Task inheriting global default (no job.bwLimit) should receive global update.
	inheriting := &JobTask{
		Job:          map[string]interface{}{"id": int64(1), "bwLimit": ""},
		rcController: native.NewRCController(""),
	}
	defer inheriting.rcController.Close()
	if err := inheriting.ApplyGlobalBwLimit(context.Background(), "08:00,5M 23:00,off"); err != nil {
		t.Fatalf("ApplyGlobalBwLimit on inheriting task failed: %v", err)
	}
	if got := inheriting.liveBwLimit(); got != "08:00,5M 23:00,off" {
		t.Fatalf("inheriting task liveBwLimit = %q, want %q", got, "08:00,5M 23:00,off")
	}

	// 2. Task with explicit per-job bwLimit (including "off") must NOT be overwritten.
	explicitOff := &JobTask{
		Job:          map[string]interface{}{"id": int64(2), "bwLimit": "off"},
		rcController: native.NewRCController("off"),
	}
	defer explicitOff.rcController.Close()
	if err := explicitOff.ApplyGlobalBwLimit(context.Background(), "5M"); err != nil {
		t.Fatalf("ApplyGlobalBwLimit on explicit-off task failed: %v", err)
	}
	if got := explicitOff.liveBwLimit(); got != "off" {
		t.Fatalf("explicit-off task liveBwLimit = %q, want off", got)
	}

	// 3. Task that was manually overridden live during this run must NOT be overwritten.
	manualLive := &JobTask{
		Job:          map[string]interface{}{"id": int64(3), "bwLimit": ""},
		rcController: native.NewRCController(""),
	}
	defer manualLive.rcController.Close()
	if err := manualLive.SetLiveBwLimit(context.Background(), "20M"); err != nil {
		t.Fatalf("SetLiveBwLimit failed: %v", err)
	}
	if err := manualLive.ApplyGlobalBwLimit(context.Background(), "5M"); err != nil {
		t.Fatalf("ApplyGlobalBwLimit after manual override failed: %v", err)
	}
	if got := manualLive.liveBwLimit(); got != "20M" {
		t.Fatalf("manually overridden task liveBwLimit = %q, want 20M", got)
	}
}
