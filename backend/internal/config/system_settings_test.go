package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConfigFileReadsRuntimeSystemSettings(t *testing.T) {
	withTempConfigDir(t)

	writeConfigForTest(t, `[opensync]
task_save=30
task_timeout=12
copy_concurrency=13
scan_concurrency=17
max_retries=4
`)

	cfg := GetConfig()
	if cfg.Server.TaskSave != 30 {
		t.Fatalf("TaskSave = %d, want 30", cfg.Server.TaskSave)
	}
	if cfg.Server.Timeout != 12 {
		t.Fatalf("Timeout = %d, want 12", cfg.Server.Timeout)
	}
	if cfg.Server.CopyConcurrency != 13 {
		t.Fatalf("CopyConcurrency = %d, want 13", cfg.Server.CopyConcurrency)
	}
	if cfg.Server.ScanConcurrency != 17 {
		t.Fatalf("ScanConcurrency = %d, want 17", cfg.Server.ScanConcurrency)
	}
	if cfg.Server.MaxRetries != 4 {
		t.Fatalf("MaxRetries = %d, want 4", cfg.Server.MaxRetries)
	}
}

func TestUpdateSystemSettingsPersistsAndUpdatesMemory(t *testing.T) {
	withTempConfigDir(t)
	_ = GetConfig()

	settings := SystemSettings{
		TaskTimeout:     48,
		TaskSave:        15,
		CopyConcurrency: 11,
		ScanConcurrency: 19,
		MaxRetries:      3,
	}
	if err := UpdateSystemSettings(settings); err != nil {
		t.Fatalf("UpdateSystemSettings() error: %v", err)
	}

	got := GetSystemSettings()
	if got != settings {
		t.Fatalf("GetSystemSettings() = %#v, want %#v", got, settings)
	}

	content, err := os.ReadFile(filepath.Join(ConfigDir(), "config.ini"))
	if err != nil {
		t.Fatalf("ReadFile(config.ini) error: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"task_timeout=48",
		"task_save=15",
		"copy_concurrency=11",
		"scan_concurrency=19",
		"max_retries=3",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config.ini missing %q in:\n%s", want, text)
		}
	}
}

func TestUpdateSystemSettingsRejectsScanConcurrencyAboveLimit(t *testing.T) {
	withTempConfigDir(t)
	_ = GetConfig()

	before := GetSystemSettings()
	err := UpdateSystemSettings(SystemSettings{
		TaskTimeout:     before.TaskTimeout,
		TaskSave:        before.TaskSave,
		CopyConcurrency: before.CopyConcurrency,
		ScanConcurrency: MaxScanConcurrency + 1,
		MaxRetries:      before.MaxRetries,
	})
	if err == nil {
		t.Fatalf("UpdateSystemSettings() error = nil, want validation error")
	}
	after := GetSystemSettings()
	if after != before {
		t.Fatalf("settings changed after invalid update: got %#v, want %#v", after, before)
	}
}

func TestUpdateSystemSettingsRejectsMaxRetriesAboveTen(t *testing.T) {
	withTempConfigDir(t)
	_ = GetConfig()

	before := GetSystemSettings()
	err := UpdateSystemSettings(SystemSettings{
		TaskTimeout:     before.TaskTimeout,
		TaskSave:        before.TaskSave,
		CopyConcurrency: before.CopyConcurrency,
		ScanConcurrency: before.ScanConcurrency,
		MaxRetries:      11,
	})
	if err == nil {
		t.Fatalf("UpdateSystemSettings() error = nil, want validation error")
	}
	after := GetSystemSettings()
	if after != before {
		t.Fatalf("settings changed after invalid update: got %#v, want %#v", after, before)
	}
}

func TestSystemSettingsCanBeReadWhileUpdated(t *testing.T) {
	withTempConfigDir(t)
	_ = GetConfig()

	start := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			settings := SystemSettings{
				TaskTimeout:     24 + i%5,
				TaskSave:        15 + i%7,
				CopyConcurrency: 3 + i%5,
				ScanConcurrency: 2 + i%4,
				MaxRetries:      i % 4,
			}
			if err := UpdateSystemSettings(settings); err != nil {
				t.Errorf("UpdateSystemSettings() error: %v", err)
				return
			}
		}
	}()

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 1000; j++ {
				_ = GetSystemSettings()
			}
		}()
	}

	close(start)
	wg.Wait()
}
