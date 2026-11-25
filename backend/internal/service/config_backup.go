package service

import (
	"encoding/json"
	"fmt"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strings"
	"time"
)

const backupFormatVersion = 1

// SystemBackupPayload is the JSON schema for configuration export/import.
type SystemBackupPayload struct {
	Version       int                      `json:"version"`
	ExportedAt    int64                    `json:"exportedAt"`
	HasSecrets    bool                     `json:"hasSecrets"`
	Settings      *config.SystemSettings   `json:"settings,omitempty"`
	Engines       []map[string]interface{} `json:"engines"`
	Notifications []map[string]interface{} `json:"notifications"`
	Jobs          []map[string]interface{} `json:"jobs"`
}

// ExportSystemBackup exports system settings, storage engines, notification
// channels, and sync jobs. Credentials are redacted unless includeSecrets is true.
func ExportSystemBackup(includeSecrets bool) (*SystemBackupPayload, error) {
	settings := config.GetSystemSettings()

	rawEngines, err := mapper.GetAlistList()
	if err != nil {
		return nil, fmt.Errorf("export engines: %w", err)
	}
	engines := make([]map[string]interface{}, 0, len(rawEngines))
	for _, row := range rawEngines {
		item := map[string]interface{}{
			"id":                util.ToInt64(row["id"]),
			"remark":            util.StringValue(row["remark"]),
			"url":               util.StringValue(row["url"]),
			"userName":          util.StringValue(row["userName"]),
			"maxConcurrentJobs": util.ToInt(row["maxConcurrentJobs"]),
		}
		rawURL := util.StringValue(row["url"])
		tokenVal := util.StringValue(row["token"])
		if includeSecrets || isNativeEmptyConfig(rawURL, tokenVal) {
			item["token"] = tokenVal
		} else {
			item["token"] = ""
		}
		engines = append(engines, item)
	}

	rawNotifies, err := mapper.GetNotifyList(false)
	if err != nil {
		return nil, fmt.Errorf("export notifications: %w", err)
	}
	notifies := make([]map[string]interface{}, 0, len(rawNotifies))
	for _, row := range rawNotifies {
		method, err := parseNotifyMethod(row["method"])
		if err != nil {
			method = -1
		}
		paramsStr := fmt.Sprintf("%v", row["params"])
		if !includeSecrets {
			paramsStr = redactNotifyParams(method, paramsStr)
		}
		notifies = append(notifies, map[string]interface{}{
			"id":     util.ToInt64(row["id"]),
			"enable": util.ToInt(row["enable"]),
			"method": method,
			"params": paramsStr,
		})
	}

	rawJobs, err := mapper.GetJobListAll()
	if err != nil {
		return nil, fmt.Errorf("export jobs: %w", err)
	}
	jobs := make([]map[string]interface{}, 0, len(rawJobs))
	for _, row := range rawJobs {
		cloned := cloneJobConfig(row)
		delete(cloned, "createTime")
		jobs = append(jobs, cloned)
	}

	return &SystemBackupPayload{
		Version:       backupFormatVersion,
		ExportedAt:    time.Now().Unix(),
		HasSecrets:    includeSecrets,
		Settings:      &settings,
		Engines:       engines,
		Notifications: notifies,
		Jobs:          jobs,
	}, nil
}

// ImportSystemBackup restores exported settings, engines, notification channels,
// and jobs while remapping alistId and notifyIds cleanly.
func ImportSystemBackup(payload SystemBackupPayload, conflictMode string) (map[string]interface{}, error) {
	conflictMode = strings.ToLower(strings.TrimSpace(conflictMode))
	if conflictMode == "" {
		conflictMode = "skip"
	}
	if conflictMode != "skip" && conflictMode != "overwrite" {
		return nil, publicError(msg.T(msg.BackupConflictModeInvalid))
	}
	if payload.Version != 0 && payload.Version != backupFormatVersion {
		return nil, publicError(msg.T(msg.BackupPayloadInvalid))
	}

	warnings := make([]string, 0)
	summary := map[string]interface{}{
		"settingsUpdated": false,
		"enginesCreated":  0,
		"enginesUpdated":  0,
		"enginesSkipped":  0,
		"notifiesCreated": 0,
		"notifiesUpdated": 0,
		"notifiesSkipped": 0,
		"jobsCreated":     0,
		"jobsUpdated":     0,
		"jobsSkipped":     0,
		"warnings":        warnings,
	}

	// 1. Settings
	if payload.Settings != nil {
		if err := config.UpdateSystemSettings(*payload.Settings); err != nil {
			warnings = append(warnings, fmt.Sprintf("系统设置未导入：%v", err))
		} else {
			summary["settingsUpdated"] = true
		}
	}

	// 2. Engines (match by url + userName)
	existingEngines, err := mapper.GetAlistList()
	if err != nil {
		return nil, fmt.Errorf("load existing engines: %w", err)
	}
	engineByKey := make(map[string]map[string]interface{}, len(existingEngines))
	for _, row := range existingEngines {
		key := engineBackupKey(util.StringValue(row["url"]), util.StringValue(row["userName"]))
		engineByKey[key] = row
	}
	engineIDMap := make(map[int64]int64)
	enginesCreated, enginesUpdated, enginesSkipped := 0, 0, 0

	for _, item := range payload.Engines {
		oldID := util.ToInt64(item["id"])
		rawURL := strings.TrimRight(strings.TrimSpace(util.StringValue(item["url"])), "/")
		userName := strings.TrimSpace(util.StringValue(item["userName"]))
		remark := strings.TrimSpace(util.StringValue(item["remark"]))
		token := strings.TrimSpace(util.StringValue(item["token"]))
		maxConcurrent := util.ToInt(item["maxConcurrentJobs"])
		if maxConcurrent < 0 || maxConcurrent > 50 {
			maxConcurrent = 0
		}
		if err := nativeBackupKey(rawURL, userName, token); err != nil {
			warnings = append(warnings, fmt.Sprintf("跳过无效地址的引擎 %q", rawURL))
			enginesSkipped++
			continue
		}
		if rawURL == "rclone://native" && remark == "" {
			remark = "原生备份"
		}
		key := engineBackupKey(rawURL, userName)
		if existing, exists := engineByKey[key]; exists {
			existingID := util.ToInt64(existing["id"])
			if oldID > 0 {
				engineIDMap[oldID] = existingID
			}
			if conflictMode == "overwrite" {
				var tokenPtr *string
				if token != "" && !isMaskedSecretValue(token) {
					t := token
					tokenPtr = &t
				}
				mc := maxConcurrent
				if err := mapper.UpdateAlistWithConcurrency(existingID, remark, rawURL, tokenPtr, &mc); err != nil {
					warnings = append(warnings, fmt.Sprintf("更新引擎 %q 失败：%v", rawURL, err))
					enginesSkipped++
				} else {
					removeCachedAlistClient(existingID)
					engineQueue.RefreshLimit(existingID, maxConcurrent)
					enginesUpdated++
				}
			} else {
				enginesSkipped++
			}
			continue
		}
		if token == "" || isMaskedSecretValue(token) {
			warnings = append(warnings, fmt.Sprintf("引擎 %q (%s) 不包含有效令牌，已跳过导入（请导出时勾选包含凭据或手动添加）", remark, rawURL))
			enginesSkipped++
			continue
		}
		newID, err := mapper.AddAlistWithConcurrency(remark, rawURL, userName, token, maxConcurrent)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("导入引擎 %q 失败：%v", rawURL, err))
			enginesSkipped++
			continue
		}
		if oldID > 0 {
			engineIDMap[oldID] = newID
		}
		engineByKey[key] = map[string]interface{}{"id": newID, "url": rawURL, "userName": userName}
		enginesCreated++
	}

	// 3. Notifications
	existingNotifies, err := mapper.GetNotifyList(false)
	if err != nil {
		return nil, fmt.Errorf("load existing notifications: %w", err)
	}
	notifyBySig := make(map[string]int64, len(existingNotifies))
	for _, row := range existingNotifies {
		notifyBySig[notifyBackupSignature(row)] = util.ToInt64(row["id"])
	}
	notifyIDMap := make(map[int64]int64)
	notifiesCreated, notifiesUpdated, notifiesSkipped := 0, 0, 0

	for _, item := range payload.Notifications {
		oldID := util.ToInt64(item["id"])
		method, err := parseNotifyMethod(item["method"])
		if err != nil {
			warnings = append(warnings, "跳过不支持的通知渠道配置")
			notifiesSkipped++
			continue
		}
		paramsMap, err := notifyParamsValue(item["params"])
		if err != nil {
			warnings = append(warnings, "跳过格式无效的通知渠道配置")
			notifiesSkipped++
			continue
		}
		if err := rejectRedactedNotifyParams(paramsMap); err != nil {
			warnings = append(warnings, fmt.Sprintf("通知渠道 #%d 包含脱敏占位符，已跳过导入（请导出时包含凭据或手动配置）", oldID))
			notifiesSkipped++
			continue
		}
		if err := normalizeNotifyWebhookBody(paramsMap); err != nil {
			warnings = append(warnings, fmt.Sprintf("通知渠道 #%d 自定义 Body 无效，已跳过", oldID))
			notifiesSkipped++
			continue
		}
		if err := validateNotifyParams(method, paramsMap); err != nil {
			warnings = append(warnings, fmt.Sprintf("通知渠道 #%d 参数无效，已跳过：%v", oldID, err))
			notifiesSkipped++
			continue
		}
		enable := 1
		if rawEnable, ok := item["enable"]; ok && rawEnable != nil {
			if parsed, err := normalizeNotifyEnable(rawEnable); err == nil {
				enable = parsed
			}
		}
		encoded, _ := json.Marshal(paramsMap)
		rec := map[string]interface{}{
			"enable": enable,
			"method": method,
			"params": string(encoded),
		}
		sig := notifyBackupSignature(rec)
		if existingID, exists := notifyBySig[sig]; exists {
			if oldID > 0 {
				notifyIDMap[oldID] = existingID
			}
			if conflictMode == "overwrite" {
				rec["id"] = existingID
				if err := mapper.EditNotify(rec); err != nil {
					warnings = append(warnings, fmt.Sprintf("更新通知渠道 #%d 失败：%v", oldID, err))
					notifiesSkipped++
				} else {
					notifiesUpdated++
				}
			} else {
				notifiesSkipped++
			}
			continue
		}
		newID, err := mapper.AddNotify(rec)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("导入通知渠道 #%d 失败：%v", oldID, err))
			notifiesSkipped++
			continue
		}
		if oldID > 0 {
			notifyIDMap[oldID] = newID
		}
		notifyBySig[sig] = newID
		notifiesCreated++
	}

	// 4. Jobs
	existingJobs, err := mapper.GetJobListAll()
	if err != nil {
		return nil, fmt.Errorf("load existing jobs: %w", err)
	}
	jobByKey := make(map[string]map[string]interface{}, len(existingJobs))
	for _, row := range existingJobs {
		key := jobBackupKey(row)
		jobByKey[key] = row
	}
	jobsCreated, jobsUpdated, jobsSkipped := 0, 0, 0

	for _, rawJob := range payload.Jobs {
		job := cloneJobConfig(rawJob)
		delete(job, "id")
		delete(job, "createTime")
		oldAlistID := util.ToInt64(job["alistId"])
		mappedAlistID, ok := engineIDMap[oldAlistID]
		if !ok || mappedAlistID <= 0 {
			warnings = append(warnings, fmt.Sprintf("任务 %q 引用的引擎 #%d 未导入或未匹配到相同引擎，已跳过（为防止误操作目标存储，不会按旧 ID 自动绑定）", util.StringValue(job["remark"]), oldAlistID))
			jobsSkipped++
			continue
		}
		job["alistId"] = mappedAlistID

		// Remap notifyIds if notifyMode == 1. Never fall back to notifyMode=0
		// ("all enabled channels") when specified channels could not be
		// imported: that would broadcast job details to unintended recipients.
		if util.ToInt(job["notifyMode"]) == 1 {
			oldIDs, err := parseNotifyIDList(job["notifyIds"])
			if err != nil || len(oldIDs) == 0 {
				job["notifyMode"] = 2
				job["notifyIds"] = nil
				warnings = append(warnings, fmt.Sprintf("任务 %q 指定的通知渠道无效或为空，已关闭该任务通知，请手动重新配置", util.StringValue(job["remark"])))
			} else {
				remapped := make([]int64, 0, len(oldIDs))
				for _, nid := range oldIDs {
					if mapped, ok := notifyIDMap[nid]; ok {
						remapped = append(remapped, mapped)
					}
				}
				if len(remapped) > 0 {
					enc, _ := json.Marshal(remapped)
					job["notifyIds"] = string(enc)
					if len(remapped) < len(oldIDs) {
						warnings = append(warnings, fmt.Sprintf("任务 %q 部分指定的通知渠道未导入，已仅保留成功匹配的 %d/%d 个渠道", util.StringValue(job["remark"]), len(remapped), len(oldIDs)))
					}
				} else {
					job["notifyMode"] = 2
					job["notifyIds"] = nil
					warnings = append(warnings, fmt.Sprintf("任务 %q 指定的通知渠道均未导入，已自动关闭该任务通知以防误发，请手动重新绑定", util.StringValue(job["remark"])))
				}
			}
		}

		if err := CleanJobInput(job); err != nil {
			warnings = append(warnings, fmt.Sprintf("任务 %q 配置清理失败，已跳过：%v", util.StringValue(job["remark"]), err))
			jobsSkipped++
			continue
		}
		if err := ValidateJobInput(job); err != nil {
			warnings = append(warnings, fmt.Sprintf("任务 %q 校验失败，已跳过：%v", util.StringValue(job["remark"]), err))
			jobsSkipped++
			continue
		}

		key := jobBackupKey(job)
		if existing, exists := jobByKey[key]; exists {
			if conflictMode == "overwrite" {
				job["id"] = util.ToInt64(existing["id"])
				if err := EditJobClient(job); err != nil {
					warnings = append(warnings, fmt.Sprintf("覆盖任务 %q 失败：%v", util.StringValue(job["remark"]), err))
					jobsSkipped++
				} else {
					jobsUpdated++
				}
			} else {
				jobsSkipped++
			}
			continue
		}
		if _, err := CreateJobClient(job); err != nil {
			warnings = append(warnings, fmt.Sprintf("导入任务 %q 失败：%v", util.StringValue(job["remark"]), err))
			jobsSkipped++
			continue
		}
		jobByKey[key] = job
		jobsCreated++
	}

	summary["enginesCreated"] = enginesCreated
	summary["enginesUpdated"] = enginesUpdated
	summary["enginesSkipped"] = enginesSkipped
	summary["notifiesCreated"] = notifiesCreated
	summary["notifiesUpdated"] = notifiesUpdated
	summary["notifiesSkipped"] = notifiesSkipped
	summary["jobsCreated"] = jobsCreated
	summary["jobsUpdated"] = jobsUpdated
	summary["jobsSkipped"] = jobsSkipped
	summary["warnings"] = warnings
	return summary, nil
}

func engineBackupKey(rawURL, userName string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(rawURL), "/")) + "\x00" + strings.TrimSpace(userName)
}

func notifyBackupSignature(row map[string]interface{}) string {
	return fmt.Sprintf("%d\x00%s", util.ToInt(row["method"]), strings.TrimSpace(util.StringValue(row["params"])))
}

func jobBackupKey(job map[string]interface{}) string {
	return fmt.Sprintf("%d\x00%s\x00%s",
		util.ToInt64(job["alistId"]),
		util.StringValue(job["srcPath"]),
		util.StringValue(job["dstPath"]),
	)
}
