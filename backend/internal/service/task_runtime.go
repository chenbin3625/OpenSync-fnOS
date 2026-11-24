package service

import (
	"errors"
	"opensync/internal/config"
	"opensync/pkg/util"
	"time"
)

const (
	retryTaskItemBatchSize = 500
	persistBatchSize       = 100
	persistFlushInterval   = 500 * time.Millisecond
	maxScanListRetries     = 2
)

var errScanAborted = errors.New("scan aborted")

type taskRuntimeLimits struct {
	CopyConcurrency int
	ScanConcurrency int
	MaxRetries      int
}

func runtimeTaskLimits() taskRuntimeLimits {
	return taskRuntimeLimitsFromServer(config.GetConfig().Server)
}

// taskRuntimeLimitsForJob merges per-job overrides on top of global settings.
// copyConcurrency=0 / scanConcurrency=0 / maxRetries=-1 (or absent) inherit global.
func taskRuntimeLimitsForJob(job map[string]interface{}) taskRuntimeLimits {
	limits := runtimeTaskLimits()
	if job == nil {
		return limits
	}
	if cc := util.ToInt(job["copyConcurrency"]); cc >= config.MinCopyConcurrency && cc <= config.MaxCopyConcurrency {
		limits.CopyConcurrency = cc
	}
	if sc := util.ToInt(job["scanConcurrency"]); sc >= config.MinScanConcurrency && sc <= config.MaxScanConcurrency {
		limits.ScanConcurrency = sc
	}
	if rawMR, ok := job["maxRetries"]; ok && rawMR != nil {
		if mr, valid := strictJobInt(rawMR); valid && mr >= config.MinMaxRetries && mr <= config.MaxRetryAttempts {
			limits.MaxRetries = mr
		}
	}
	return limits
}

func taskRuntimeLimitsFromServer(server config.ServerConfig) taskRuntimeLimits {
	return taskRuntimeLimits{
		CopyConcurrency: intInRangeOrDefault(
			server.CopyConcurrency,
			config.MinCopyConcurrency,
			config.MaxCopyConcurrency,
			config.DefaultCopyConcurrency,
		),
		ScanConcurrency: intInRangeOrDefault(
			server.ScanConcurrency,
			config.MinScanConcurrency,
			config.MaxScanConcurrency,
			config.DefaultScanConcurrency,
		),
		MaxRetries: intInRangeOrDefault(
			server.MaxRetries,
			config.MinMaxRetries,
			config.MaxRetryAttempts,
			config.DefaultMaxRetries,
		),
	}
}

func intInRangeOrDefault(value, minValue, maxValue, defaultValue int) int {
	if value < minValue {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
