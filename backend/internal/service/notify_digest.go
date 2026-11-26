package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/pkg/util"
)

// BuildDigestSummary aggregates execution metrics over the requested period
// ("daily" = last 24h, "weekly" = last 7 days) and formats a concise report.
func BuildDigestSummary(period string) (title, content string, vars map[string]string, err error) {
	now := time.Now().In(schedulerLocation())
	label := "每日"
	window := 24 * time.Hour
	if period == "weekly" {
		label = "每周"
		window = 7 * 24 * time.Hour
	}
	since := now.Add(-window).Unix()

	var totalRuns, successRuns, failRuns, stoppedRuns, totalFiles, totalBytes int64
	if err := mapper.ForEachRecentTaskWithTime(since, func(_ int64, status int, taskNum string) error {
		totalRuns++
		switch status {
		case 2, 8:
			successRuns++
		case 3, 5, 6, 7, 10:
			failRuns++
		case 4:
			stoppedRuns++
		}
		if parsed := parseTaskNumJSON(taskNum); parsed != nil {
			totalFiles += util.ToInt64(parsed["successNum"])
			totalBytes += util.ToInt64(parsed["sumSize"])
		}
		return nil
	}); err != nil {
		return "", "", nil, err
	}

	jobs, _ := mapper.GetJobListAll()
	enabledCount := 0
	for _, j := range jobs {
		if util.ToInt(j["enable"]) == 1 {
			enabledCount++
		}
	}

	title = fmt.Sprintf("[OpenSync] %s备份摘要 (%s)", label, now.Format("2006-01-02"))
	content = fmt.Sprintf(
		"统计周期：%s（截至 %s）\n启用任务：%d / %d\n执行次数：%d（成功 %d / 失败或异常 %d / 中止 %d）\n同步文件：%d 个\n传输总量：%s",
		label,
		now.Format("2006-01-02 15:04"),
		enabledCount, len(jobs),
		totalRuns, successRuns, failRuns, stoppedRuns,
		totalFiles,
		util.ConvertBytes(totalBytes),
	)
	vars = map[string]string{
		"period":      period,
		"totalRuns":   fmt.Sprintf("%d", totalRuns),
		"successRuns": fmt.Sprintf("%d", successRuns),
		"failRuns":    fmt.Sprintf("%d", failRuns),
		"totalFiles":  fmt.Sprintf("%d", totalFiles),
		"totalBytes":  util.ConvertBytes(totalBytes),
	}
	return title, content, vars, nil
}

// SendDigestNow immediately builds and queues a digest summary notification.
func SendDigestNow(period string) error {
	if period != "weekly" {
		period = "daily"
	}
	title, content, vars, err := BuildDigestSummary(period)
	if err != nil {
		return err
	}
	QueueSystemAlertNotification("digest", title, content, vars)
	return nil
}

// StartDigestScheduler runs a background ticker that dispatches daily or weekly
// summary notifications according to SystemSettings (digestEnable / digestSchedule /
// digestHour / digestWeekday).
func StartDigestScheduler() func() {
	ctx, cancel := context.WithCancel(context.Background())
	var (
		mu          sync.Mutex
		lastSentKey string
	)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				settings := config.GetSystemSettings()
				if settings.DigestEnable != 1 {
					continue
				}
				schedule := settings.DigestSchedule
				if schedule == "" {
					schedule = "daily"
				}
				locNow := now.In(schedulerLocation())
				if locNow.Hour() != settings.DigestHour {
					continue
				}
				if schedule == "weekly" && int(locNow.Weekday()) != settings.DigestWeekday {
					continue
				}
				slotKey := fmt.Sprintf("%s-%s-%d", schedule, locNow.Format("2006-01-02"), locNow.Hour())
				mu.Lock()
				alreadySent := lastSentKey == slotKey
				if !alreadySent {
					lastSentKey = slotKey
				}
				mu.Unlock()
				if alreadySent {
					continue
				}
				if err := SendDigestNow(schedule); err != nil {
					log.Printf("Digest notification failed: %v", err)
				} else {
					log.Printf("Queued %s backup digest notification", schedule)
				}
			}
		}
	}()
	return cancel
}
