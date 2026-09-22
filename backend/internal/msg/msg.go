package msg

import (
	"fmt"
	"strings"
	"sync"
)

// Message keys — constant values are lookup keys, not display text.
const (
	LostPart                = "LostPart"
	AlistNotFound           = "AlistNotFound"
	AlistInUse              = "AlistInUse"
	JobNotFound             = "JobNotFound"
	TaskNotFound            = "TaskNotFound"
	AlistConnectFail        = "AlistConnectFail"
	AlistURLInvalid         = "AlistURLInvalid"
	AddressIncorrect        = "AddressIncorrect"
	CodeNot200              = "CodeNot200"
	AlistUnAuth             = "AlistUnAuth"
	WithoutToken            = "WithoutToken"
	AlistTokenRequired      = "AlistTokenRequired"
	AlistExists             = "AlistExists"
	AlistNoCopyTask         = "AlistNoCopyTask"
	NotifyNotFound          = "NotifyNotFound"
	TaskMayDelete           = "TaskMayDelete"
	Src                     = "Src"
	Dst                     = "Dst"
	NoJobForRun             = "NoJobForRun"
	JobRunning              = "JobRunning"
	JobRunningCannotDelete  = "JobRunningCannotDelete"
	JobDeleteWaitTimeout    = "JobDeleteWaitTimeout"
	SyncPathOverlap         = "SyncPathOverlap"
	SrcPathNested           = "SrcPathNested"
	IntervalLost            = "IntervalLost"
	CronLost                = "CronLost"
	CannotResumeLostJob     = "CannotResumeLostJob"
	DisabledJobCannotRun    = "DisabledJobCannotRun"
	CannotDisableManualJob  = "CannotDisableManualJob"
	TaskNotRunningStop      = "TaskNotRunningStop"
	NoFailedTaskItems       = "NoFailedTaskItems"
	NotifyTestMsg           = "NotifyTestMsg"
	NotifyURLInvalid        = "NotifyURLInvalid"
	NotifyMethodInvalid     = "NotifyMethodInvalid"
	NotifyParamInvalid      = "NotifyParamInvalid"
	NotifySendFail          = "NotifySendFail"
	ListTooLarge            = "ListTooLarge"
	MinFileSizeInvalid      = "MinFileSizeInvalid"
	MaxFileSizeInvalid      = "MaxFileSizeInvalid"
	MinFileSizeGtMax        = "MinFileSizeGtMax"
	SettingsTaskTimeout     = "SettingsTaskTimeout"
	SettingsTaskSave        = "SettingsTaskSave"
	SettingsCopyConcurrency = "SettingsCopyConcurrency"
	SettingsScanConcurrency = "SettingsScanConcurrency"
	SettingsMaxRetries      = "SettingsMaxRetries"
	InternalError           = "InternalError"
	RequestTooLarge         = "RequestTooLarge"
	SSEConnLimit            = "SSEConnLimit"
	InvalidConfigParams     = "InvalidConfigParams"
)

// Format-string keys
const (
	FmtExcludeRulesUnsupported = "FmtExcludeRulesUnsupported"
	FmtMirrorDeleteGuard       = "FmtMirrorDeleteGuard"
	FmtScanError               = "FmtScanError"
	FmtAlistFailCodeReason     = "FmtAlistFailCodeReason"
	FmtNotifyError             = "FmtNotifyError"
	FmtSettingsRangeError      = "FmtSettingsRangeError"
)

var (
	mu            sync.RWMutex
	currentLocale = "zh"
)

var translations = map[string]map[string]string{
	"zh": {
		LostPart:                "入参不全",
		AlistNotFound:           "未找到alist，可能已经被删除",
		AlistInUse:              "该引擎仍被同步任务使用，请先删除相关同步任务",
		JobNotFound:             "未找到作业，可能已经被删除",
		TaskNotFound:            "未找到任务，可能已经被删除",
		AlistConnectFail:        "alist连接失败，请检查是否填写正确",
		AlistURLInvalid:         "AList地址必须是有效的 http 或 https URL",
		AddressIncorrect:        "alist地址格式有误",
		CodeNot200:              "状态码非200",
		AlistUnAuth:             "AList鉴权失败，可能是令牌已失效",
		WithoutToken:            "地址改变时令牌必填",
		AlistTokenRequired:      "AList令牌必填",
		AlistExists:             "该引擎已存在（相同地址和用户名）",
		AlistNoCopyTask:         "AList未返回复制任务，且目标文件不存在",
		NotifyNotFound:          "未找到通知配置，可能已经被删除",
		TaskMayDelete:           "任务未找到。可能是您手动到AList中删除了复制任务；或者Alist因手动/异常奔溃被重启，导致任务记录丢失",
		Src:                     "来源",
		Dst:                     "目标",
		NoJobForRun:             "没有可供执行的作业",
		JobRunning:              "当前有任务执行中，请稍后再试",
		JobRunningCannotDelete:  "当前同步任务正在执行中，不能删除",
		JobDeleteWaitTimeout:    "任务仍在停止中，请稍后重试删除",
		SyncPathOverlap:         "来源目录和目标目录不能相同或互相嵌套",
		SrcPathNested:           "来源目录之间不能互相嵌套，请去掉被上级目录覆盖的子目录",
		IntervalLost:            "创建间隔型作业时，间隔必填",
		CronLost:                "创建cron型任务时，至少有一项不为空",
		CannotResumeLostJob:     "作业不存在无法恢复，请删除后重新创建",
		DisabledJobCannotRun:    "禁用的作业不能运行",
		CannotDisableManualJob:  "不可禁用仅手动任务",
		TaskNotRunningStop:      "任务未在运行中，无法停止",
		NoFailedTaskItems:       "没有可重试的未完成项",
		NotifyTestMsg:           "这是一条由您自己发送的OpenSync测试消息，当你看到这条消息，说明你的配置是正确可用的。",
		NotifyURLInvalid:        "Webhook URL必须是有效的 HTTP 或 HTTPS URL",
		NotifyMethodInvalid:     "通知方式不支持",
		NotifyParamInvalid:      "通知配置参数不完整",
		NotifySendFail:          "通知发送失败，请检查配置或稍后重试",
		ListTooLarge:            "结果超出单次返回上限，请带上 pageNum 与 pageSize 分页查询",
		MinFileSizeInvalid:      "最小文件大小必须是大于等于0的整数",
		MaxFileSizeInvalid:      "最大文件大小必须是大于等于0的整数",
		MinFileSizeGtMax:        "最小文件大小不能大于最大文件大小",
		SettingsTaskTimeout:     "任务超时时间",
		SettingsTaskSave:        "历史任务保留",
		SettingsCopyConcurrency: "操作并发数",
		SettingsScanConcurrency: "扫描并发数",
		SettingsMaxRetries:      "最大重试次数",
		InternalError:           "操作失败，请检查引擎连接或查看服务日志",
		RequestTooLarge:         "请求内容过大",
		SSEConnLimit:            "连接数已达上限，请关闭其他页面后重试",
		InvalidConfigParams:     "配置参数无效",

		FmtExcludeRulesUnsupported: "以下过滤规则不受支持，已拒绝保存：%s。仅支持三种写法：文件名（可含 * 通配）、*.后缀、相对目录/",
		FmtMirrorDeleteGuard:       "为防止误删，已跳过本次全量同步的删除阶段：%s。请确认来源目录可正常列举后重试",
		FmtScanError:               "%s目录扫描失败，原因为: %s",
		FmtAlistFailCodeReason:     "AList返回%d错误，原因为：%s",
		FmtNotifyError:             "发送通知过程中失败，原因为：%s",
		FmtSettingsRangeError:      "%s必须在%d到%d之间",
	},
	"en": {
		LostPart:                "Missing required parameters",
		AlistNotFound:           "AList not found, it may have been deleted",
		AlistInUse:              "This engine is still used by sync jobs, please delete those jobs first",
		JobNotFound:             "Job not found, it may have been deleted",
		TaskNotFound:            "Task not found, it may have been deleted",
		AlistConnectFail:        "Failed to connect to AList, please check your settings",
		AlistURLInvalid:         "AList URL must be a valid HTTP or HTTPS URL",
		AddressIncorrect:        "Invalid AList address format",
		CodeNot200:              "Status code is not 200",
		AlistUnAuth:             "AList authentication failed, the token may have expired",
		WithoutToken:            "Token is required when the address changes",
		AlistTokenRequired:      "AList token is required",
		AlistExists:             "This engine already exists (same address and username)",
		AlistNoCopyTask:         "AList did not return a copy task and the target file does not exist",
		NotifyNotFound:          "Notification config not found, it may have been deleted",
		TaskMayDelete:           "Task not found. It may have been manually deleted in AList, or AList was restarted causing task records to be lost",
		Src:                     "Source",
		Dst:                     "Destination",
		NoJobForRun:             "No jobs available to run",
		JobRunning:              "A task is currently running, please try again later",
		JobRunningCannotDelete:  "Cannot delete while sync task is running",
		JobDeleteWaitTimeout:    "Task is still stopping, please try deleting again later",
		SyncPathOverlap:         "Source and destination directories cannot be the same or nested",
		SrcPathNested:           "Source directories cannot be nested within each other, please remove subdirectories covered by parent directories",
		IntervalLost:            "Interval is required for interval-based jobs",
		CronLost:                "At least one field must be non-empty for cron-based jobs",
		CannotResumeLostJob:     "Job does not exist and cannot be resumed, please delete and recreate",
		DisabledJobCannotRun:    "Disabled jobs cannot be run",
		CannotDisableManualJob:  "Cannot disable manual-only jobs",
		TaskNotRunningStop:      "Task is not running and cannot be stopped",
		NoFailedTaskItems:       "No failed items available to retry",
		NotifyTestMsg:           "This is a test message sent by OpenSync. If you see this, your configuration is working correctly.",
		NotifyURLInvalid:        "Webhook URL must be a valid HTTP or HTTPS URL",
		NotifyMethodInvalid:     "Notification method not supported",
		NotifyParamInvalid:      "Notification configuration parameters are incomplete",
		NotifySendFail:          "Failed to send notification, please check configuration or try again later",
		ListTooLarge:            "Result exceeds single-response limit, please use pageNum and pageSize for pagination",
		MinFileSizeInvalid:      "Minimum file size must be an integer >= 0",
		MaxFileSizeInvalid:      "Maximum file size must be an integer >= 0",
		MinFileSizeGtMax:        "Minimum file size cannot be greater than maximum file size",
		SettingsTaskTimeout:     "Task timeout",
		SettingsTaskSave:        "Task history retention",
		SettingsCopyConcurrency: "Copy concurrency",
		SettingsScanConcurrency: "Scan concurrency",
		SettingsMaxRetries:      "Max retries",
		InternalError:           "Operation failed, please check engine connection or view service logs",
		RequestTooLarge:         "Request body too large",
		SSEConnLimit:            "Connection limit reached, please close other pages and try again",
		InvalidConfigParams:     "Invalid configuration parameters",

		FmtExcludeRulesUnsupported: "The following filter rules are not supported and were rejected: %s. Only three formats are supported: filename (with * wildcard), *.extension, relative/directory/",
		FmtMirrorDeleteGuard:       "To prevent accidental deletion, the delete phase of this full sync was skipped: %s. Please verify the source directory can be listed correctly and try again",
		FmtScanError:               "%s directory scan failed: %s",
		FmtAlistFailCodeReason:     "AList returned error %d: %s",
		FmtNotifyError:             "Notification failed: %s",
		FmtSettingsRangeError:      "%s must be between %d and %d",
	},
}

// T returns the translated string for key in the current locale.
// Falls back to zh, then to the key itself.
func T(key string) string {
	mu.RLock()
	locale := currentLocale
	mu.RUnlock()

	if m, ok := translations[locale]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if locale != "zh" {
		if m, ok := translations["zh"]; ok {
			if v, ok := m[key]; ok {
				return v
			}
		}
	}
	return key
}

// SetLocale sets the active locale (e.g. "zh", "en").
func SetLocale(locale string) {
	mu.Lock()
	currentLocale = locale
	mu.Unlock()
}

// Locale returns the current locale.
func Locale() string {
	mu.RLock()
	defer mu.RUnlock()
	return currentLocale
}

// --- Format functions ---

func ExcludeRulesUnsupported(rules []string) string {
	return fmt.Sprintf(T(FmtExcludeRulesUnsupported), strings.Join(rules, ", "))
}

func MirrorDeleteGuard(reason string) string {
	return fmt.Sprintf(T(FmtMirrorDeleteGuard), reason)
}

func ScanError(srcOrDst, reason string) string {
	return fmt.Sprintf(T(FmtScanError), srcOrDst, reason)
}

func AlistFailCodeReason(code int, message string) string {
	return fmt.Sprintf(T(FmtAlistFailCodeReason), code, message)
}

func NotifyError(reason string) string {
	return fmt.Sprintf(T(FmtNotifyError), reason)
}

func SettingsRangeError(name string, min, max int) string {
	return fmt.Sprintf(T(FmtSettingsRangeError), name, min, max)
}
