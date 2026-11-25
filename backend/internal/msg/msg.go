package msg

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

// Message keys — constant values are lookup keys, not display text.
const (
	LostPart                = "LostPart"
	AlistNotFound           = "AlistNotFound"
	AlistInUse              = "AlistInUse"
	JobNotFound             = "JobNotFound"
	JobExists               = "JobExists"
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
	DstPathNested           = "DstPathNested"
	SrcPathNested           = "SrcPathNested"
	IntervalLost            = "IntervalLost"
	CronLost                = "CronLost"
	CannotResumeLostJob     = "CannotResumeLostJob"
	DisabledJobCannotRun    = "DisabledJobCannotRun"
	CannotDisableManualJob  = "CannotDisableManualJob"
	TaskNotRunningStop        = "TaskNotRunningStop"
	FullSyncPending           = "FullSyncPending"
	FullSyncApprovalExecuting = "FullSyncApprovalExecuting"
	NoFailedTaskItems         = "NoFailedTaskItems"
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
	SettingsMaxRetries          = "SettingsMaxRetries"
	SettingsSchedConfirm        = "SettingsSchedConfirm"
	SettingsApprovalTimeout     = "SettingsApprovalTimeout"
	SettingsHealthCheckInterval = "SettingsHealthCheckInterval"
	SettingsDefaultBwLimit      = "SettingsDefaultBwLimit"
	SettingsProxyURL            = "SettingsProxyURL"
	InternalError               = "InternalError"
	RequestTooLarge             = "RequestTooLarge"
	SSEConnLimit                = "SSEConnLimit"
	SSEStreamLimit              = "SSEStreamLimit"
	InvalidConfigParams         = "InvalidConfigParams"
	ConfigSaveFail              = "ConfigSaveFail"
	ConfigChangedOnDisk         = "ConfigChangedOnDisk"
	CredentialUnreadable        = "CredentialUnreadable"
	VersionCheckFail            = "VersionCheckFail"
	// Gateway identity/origin rejections, used by platform.GatewayRequired.
	CrossSiteWriteRejected = "CrossSiteWriteRejected"
	GatewayLoginRequired   = "GatewayLoginRequired"
	AdminOnly              = "AdminOnly"

	// Extended job / queue / filter / backup / dry-run / history messages
	ScanIntervalInvalid      = "ScanIntervalInvalid"
	CopyConcurrencyInvalid   = "CopyConcurrencyInvalid"
	ScanConcurrencyInvalid   = "ScanConcurrencyInvalid"
	MaxRetriesInvalid        = "MaxRetriesInvalid"
	CompareModeInvalid       = "CompareModeInvalid"
	ConflictPolicyInvalid    = "ConflictPolicyInvalid"
	TrashDirInvalid          = "TrashDirInvalid"
	TrashRetentionInvalid    = "TrashRetentionInvalid"
	FilterModeInvalid        = "FilterModeInvalid"
	TimeFilterInvalid        = "TimeFilterInvalid"
	TimeFilterRangeConflict  = "TimeFilterRangeConflict"
	TimeWindowInvalid        = "TimeWindowInvalid"
	NotifyModeInvalid        = "NotifyModeInvalid"
	NotifyIdsInvalid         = "NotifyIdsInvalid"
	MaxConcurrentJobsInvalid = "MaxConcurrentJobsInvalid"
	BatchActionInvalid       = "BatchActionInvalid"
	BackupPayloadInvalid     = "BackupPayloadInvalid"
	BackupConflictModeInvalid = "BackupConflictModeInvalid"
	DryRunAlreadyRunning     = "DryRunAlreadyRunning"
	DryRunNotFound           = "DryRunNotFound"
	CheckAlreadyRunning      = "CheckAlreadyRunning"
	CheckNotFound            = "CheckNotFound"
	TaskRunningCannotDelete  = "TaskRunningCannotDelete"
	NoRetryItemSelected      = "NoRetryItemSelected"
	BwLimitInvalid           = "BwLimitInvalid"
	ProxyURLInvalid          = "ProxyURLInvalid"
	TPSLimitInvalid          = "TPSLimitInvalid"
	TPSLimitBurstInvalid     = "TPSLimitBurstInvalid"
	ModifyWindowInvalid      = "ModifyWindowInvalid"
	MultiThreadStreamsInvalid = "MultiThreadStreamsInvalid"
	ExtraFlagsInvalid        = "ExtraFlagsInvalid"

	// Localized notification labels
	NotifyApprovalTitle         = "NotifyApprovalTitle"
	NotifyApprovalContent       = "NotifyApprovalContent"
	NotifyEngineOfflineTitle    = "NotifyEngineOfflineTitle"
	NotifyEngineOnlineTitle     = "NotifyEngineOnlineTitle"
	NotifyEngineStorageErrTitle = "NotifyEngineStorageErrTitle"
	NotifyStatusSuccess         = "NotifyStatusSuccess"
	NotifyStatusNoSync          = "NotifyStatusNoSync"
	NotifyStatusPartialFail     = "NotifyStatusPartialFail"
	NotifyStatusFail            = "NotifyStatusFail"
	NotifyStatusStopped         = "NotifyStatusStopped"
	NotifyStatusTimeout         = "NotifyStatusTimeout"
	NotifyStatusUnknown         = "NotifyStatusUnknown"
	NotifyMethodIncremental     = "NotifyMethodIncremental"
	NotifyMethodFull            = "NotifyMethodFull"
	NotifyMethodMove            = "NotifyMethodMove"
	NotifyMethodBisync          = "NotifyMethodBisync"
)

// Format-string keys
const (
	FmtExcludeRulesUnsupported = "FmtExcludeRulesUnsupported"
	FmtIncludeRulesUnsupported = "FmtIncludeRulesUnsupported"
	FmtMirrorDeleteGuard       = "FmtMirrorDeleteGuard"
	FmtUnsafeDeleteTarget      = "FmtUnsafeDeleteTarget"
	FmtScanError               = "FmtScanError"
	FmtJobDstConflict          = "FmtJobDstConflict"
	FmtFullSyncApprovalTimeout = "FmtFullSyncApprovalTimeout"
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
		JobExists:               "相同来源、目标和引擎的作业已存在",
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
		DstPathNested:           "目标目录之间不能相同或互相嵌套（包括多个来源各自同步到的子目录），请调整来源或目标目录",
		SrcPathNested:           "来源目录之间不能互相嵌套，请去掉被上级目录覆盖的子目录",
		IntervalLost:            "创建间隔型作业时，间隔必填",
		CronLost:                "创建cron型任务时，至少有一项不为空",
		CannotResumeLostJob:     "作业不存在无法恢复，请删除后重新创建",
		DisabledJobCannotRun:    "禁用的作业不能运行",
		CannotDisableManualJob:  "不可禁用仅手动任务",
		TaskNotRunningStop:        "任务未在运行中，无法停止",
		FullSyncPending:           "当前没有待确认的删除项",
		FullSyncApprovalExecuting: "待确认删除正在后台执行中，请勿重复操作",
		NoFailedTaskItems:         "没有可重试的未完成项",
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
		SettingsMaxRetries:          "最大重试次数",
		SettingsSchedConfirm:        "定时全量同步删除确认",
		SettingsApprovalTimeout:     "全量同步审批超时",
		SettingsHealthCheckInterval: "引擎健康检查间隔",
		SettingsDefaultBwLimit:      "默认传输限速",
		SettingsProxyURL:            "全局代理地址",
		InternalError:               "操作失败，请检查引擎连接或查看服务日志",
		RequestTooLarge:             "请求内容过大",
		SSEConnLimit:                "连接数已达上限，请关闭其他页面后重试",
		SSEStreamLimit:              "该任务的进度订阅数已达上限，请关闭其他页面后重试",
		InvalidConfigParams:         "配置参数无效",
		ConfigSaveFail:              "配置保存失败，请查看服务日志",
		ConfigChangedOnDisk:         "配置文件已被手动修改（%s），为避免覆盖您的修改已拒绝保存，请重启应用后重试",
		CredentialUnreadable:        "已保存的凭据无法解密（secret.key 可能已丢失或被替换），请删除该配置后重新添加",
		VersionCheckFail:            "暂时无法检查最新版本",
		CrossSiteWriteRejected:      "不允许跨站操作",
		GatewayLoginRequired:        "请从飞牛桌面打开应用",
		AdminOnly:                   "仅管理员可使用此应用",

		ScanIntervalInvalid:      "扫描间隔必须是 0 到 300 之间的整数（秒）",
		CopyConcurrencyInvalid:   "操作并发数必须是 0（跟随全局）或 1 到 100 之间的整数",
		ScanConcurrencyInvalid:   "扫描并发数必须是 0（跟随全局）或 1 到 50 之间的整数",
		MaxRetriesInvalid:        "最大重试次数必须是 -1（跟随全局）或 0 到 10 之间的整数",
		CompareModeInvalid:       "文件比对方式不支持",
		ConflictPolicyInvalid:    "文件冲突策略不支持",
		TrashDirInvalid:          "回收站目录名无效（不能包含路径穿越、绝对路径或非法字符）",
		TrashRetentionInvalid:    "回收站保留天数必须是 0 到 3650 之间的整数",
		FilterModeInvalid:        "过滤模式不支持",
		TimeFilterInvalid:        "时间过滤参数必须是大于等于 0 的整数",
		TimeFilterRangeConflict:  "时间过滤范围冲突：仅同步早于 N 天的天数不能大于等于仅同步最近 M 天的天数",
		TimeWindowInvalid:        "允许运行时间窗口格式无效，请使用 HH:MM 且开始与结束时间不能相同",
		NotifyModeInvalid:        "通知绑定模式不支持",
		NotifyIdsInvalid:         "指定的通知渠道列表无效",
		MaxConcurrentJobsInvalid: "单引擎最大并发任务数必须是 0 到 50 之间的整数",
		BatchActionInvalid:       "批量操作类型不支持",
		BackupPayloadInvalid:     "导入的配置备份数据格式无效",
		BackupConflictModeInvalid: "导入冲突处理模式仅支持 skip 或 overwrite",
		DryRunAlreadyRunning:     "该任务已有模拟运行正在进行中",
		DryRunNotFound:           "未找到该任务的模拟运行记录",
		CheckAlreadyRunning:      "该任务已有校验任务正在进行中",
		CheckNotFound:            "未找到该任务的校验记录",
		TaskRunningCannotDelete:  "正在运行中的任务记录不能删除",
		NoRetryItemSelected:      "未选中可重试的失败项",
		BwLimitInvalid:           "限速格式无效，支持如 10M、500K、10M:2M 或分时段规则 08:00,5M 23:00,off",
		ProxyURLInvalid:          "代理地址无效，仅支持 http://、https://、socks5:// 或 socks5h:// URL",
		TPSLimitInvalid:          "每秒请求限流（TPS）必须是 0 到 1000 之间的数值",
		TPSLimitBurstInvalid:     "突发请求配额必须是 0 到 1000 之间的整数",
		ModifyWindowInvalid:      "时间精度容差格式无效，请使用如 1s、2s、100ms、1m（最大 24h）",
		MultiThreadStreamsInvalid: "单文件多线程流数必须是 0 到 64 之间的整数",
		ExtraFlagsInvalid:        "扩展传输参数格式无效或包含不受支持的选项",

		NotifyApprovalTitle:         "待确认删除",
		NotifyApprovalContent:       "任务「%s」已完成同步，检测到目标端存在 %d 个待删除项（%d 个文件，%d 个目录），不影响后续同步任务。请在 OpenSync 中查看清单并确认或跳过删除。",
		NotifyEngineOfflineTitle:    "存储引擎连接异常",
		NotifyEngineOnlineTitle:     "存储引擎已恢复在线",
		NotifyEngineStorageErrTitle: "存储引擎挂载状态异常",
		NotifyStatusSuccess:         "同步成功",
		NotifyStatusNoSync:          "无需同步",
		NotifyStatusPartialFail:     "部分失败",
		NotifyStatusFail:            "执行失败",
		NotifyStatusStopped:         "已停止",
		NotifyStatusTimeout:         "执行超时",
		NotifyStatusUnknown:         "未知状态",
		NotifyMethodIncremental:     "增量同步",
		NotifyMethodFull:            "全量同步",
		NotifyMethodMove:            "移动同步",
		NotifyMethodBisync:          "双向同步",

		FmtExcludeRulesUnsupported: "以下过滤规则不受支持，已拒绝保存：%s。仅支持三种写法：文件名（可含 * 通配）、*.后缀、相对目录/",
		FmtIncludeRulesUnsupported: "以下白名单规则不受支持，已拒绝保存：%s。仅支持文件名（可含 * 通配）或 *.后缀",
		FmtMirrorDeleteGuard:       "为防止误删，已跳过本次全量同步的删除阶段：%s。请确认来源和目标目录可正常访问后重试",
		FmtUnsafeDeleteTarget:      "已拒绝删除 %q（位于 %q）：名称不是单个文件或目录",
		FmtScanError:               "%s目录扫描失败，原因为: %s",
		FmtJobDstConflict:          "目标目录 %s 已被其他同步任务使用，两个任务会互相覆盖或删除对方写入的文件，请调整目标目录",
		FmtFullSyncApprovalTimeout: "全量同步确认等待超时，本次只执行了新增和更新，未删除目标端多余文件；请确认后手动执行一次全量同步",
		FmtAlistFailCodeReason:     "AList返回%d错误，原因为：%s",
		FmtNotifyError:             "发送通知过程中失败，原因为：%s",
		FmtSettingsRangeError:      "%s必须在%d到%d之间",
	},
	"en": {
		LostPart:                "Missing required parameters",
		AlistNotFound:           "AList not found, it may have been deleted",
		AlistInUse:              "This engine is still used by sync jobs, please delete those jobs first",
		JobNotFound:             "Job not found, it may have been deleted",
		JobExists:               "A job with the same source, destination and engine already exists",
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
		DstPathNested:           "Destination directories cannot be the same or nested, including the subdirectories each source is synced into",
		SrcPathNested:           "Source directories cannot be nested within each other, please remove subdirectories covered by parent directories",
		IntervalLost:            "Interval is required for interval-based jobs",
		CronLost:                "At least one field must be non-empty for cron-based jobs",
		CannotResumeLostJob:     "Job does not exist and cannot be resumed, please delete and recreate",
		DisabledJobCannotRun:    "Disabled jobs cannot be run",
		CannotDisableManualJob:  "Cannot disable manual-only jobs",
		TaskNotRunningStop:        "Task is not running and cannot be stopped",
		FullSyncPending:           "No pending deletions awaiting approval",
		FullSyncApprovalExecuting: "Pending deletions are already executing",
		NoFailedTaskItems:         "No failed items available to retry",
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
		SettingsMaxRetries:          "Max retries",
		SettingsSchedConfirm:        "Scheduled full-sync deletion confirmation",
		SettingsApprovalTimeout:     "Full-sync approval timeout",
		SettingsHealthCheckInterval: "Engine health check interval",
		SettingsDefaultBwLimit:      "Default bandwidth limit",
		SettingsProxyURL:            "Global proxy URL",
		InternalError:               "Operation failed, please check engine connection or view service logs",
		RequestTooLarge:             "Request body too large",
		SSEConnLimit:                "Connection limit reached, please close other pages and try again",
		SSEStreamLimit:              "Too many progress streams for this task, please close other pages and try again",
		InvalidConfigParams:         "Invalid configuration parameters",
		ConfigSaveFail:              "Failed to save configuration, please check the service logs",
		ConfigChangedOnDisk:         "The configuration file was edited manually (%s); refusing to overwrite those changes. Restart the app and try again",
		CredentialUnreadable:        "The stored credential cannot be decrypted (secret.key may have been lost or replaced), please delete this entry and add it again",
		VersionCheckFail:            "Unable to check for the latest version right now",
		CrossSiteWriteRejected:      "Cross-site operations are not allowed",
		GatewayLoginRequired:        "Please open this app from the fnOS desktop",
		AdminOnly:                   "Only administrators can use this app",

		ScanIntervalInvalid:       "Scan interval must be an integer between 0 and 300 seconds",
		CopyConcurrencyInvalid:    "Copy concurrency must be 0 (follow global) or between 1 and 100",
		ScanConcurrencyInvalid:    "Scan concurrency must be 0 (follow global) or between 1 and 50",
		MaxRetriesInvalid:         "Max retries must be -1 (follow global) or between 0 and 10",
		CompareModeInvalid:        "Unsupported file comparison mode",
		ConflictPolicyInvalid:     "Unsupported conflict policy",
		TrashDirInvalid:           "Invalid trash directory name (path traversal, absolute paths, and illegal characters are not allowed)",
		TrashRetentionInvalid:     "Trash retention days must be an integer between 0 and 3650",
		FilterModeInvalid:         "Unsupported filter mode",
		TimeFilterInvalid:         "Time filter values must be integers >= 0",
		TimeFilterRangeConflict:   "Conflicting time filter range: olderThanDays must be less than newerThanDays",
		TimeWindowInvalid:         "Invalid time window: use HH:MM format and distinct start/end times",
		NotifyModeInvalid:         "Unsupported notification binding mode",
		NotifyIdsInvalid:          "Invalid notification channel ID list",
		MaxConcurrentJobsInvalid:  "Max concurrent jobs per engine must be between 0 and 50",
		BatchActionInvalid:        "Unsupported batch action",
		BackupPayloadInvalid:      "Invalid configuration backup payload",
		BackupConflictModeInvalid: "Backup conflict mode must be skip or overwrite",
		DryRunAlreadyRunning:      "A dry-run preview is already running for this job",
		DryRunNotFound:            "No dry-run preview found for this job",
		CheckAlreadyRunning:       "A verification check is already running for this job",
		CheckNotFound:             "No verification check found for this job",
		TaskRunningCannotDelete:   "Cannot delete a task while it is running",
		NoRetryItemSelected:       "No retryable failed items selected",
		BwLimitInvalid:            "Invalid bandwidth limit format (e.g. 10M, 500K, 10M:2M, or schedule 08:00,5M 23:00,off)",
		ProxyURLInvalid:           "Invalid proxy URL; only http://, https://, socks5://, or socks5h:// URLs are supported",
		TPSLimitInvalid:           "TPS limit must be a number between 0 and 1000",
		TPSLimitBurstInvalid:      "TPS burst must be an integer between 0 and 1000",
		ModifyWindowInvalid:       "Invalid modify window duration (e.g. 1s, 2s, 100ms, 1m; max 24h)",
		MultiThreadStreamsInvalid: "Multi-thread streams must be an integer between 0 and 64",
		ExtraFlagsInvalid:         "Invalid or unsupported extra transfer options",

		NotifyApprovalTitle:         "Deletions Awaiting Approval",
		NotifyApprovalContent:       "Job \"%s\" finished syncing with %d destination items pending deletion (%d files, %d folders). Syncing is not blocked; open OpenSync to review and confirm or skip deletion.",
		NotifyEngineOfflineTitle:    "Storage Engine Offline",
		NotifyEngineOnlineTitle:     "Storage Engine Recovered",
		NotifyEngineStorageErrTitle: "Storage Mount Alert",
		NotifyStatusSuccess:         "Succeeded",
		NotifyStatusNoSync:          "No Changes",
		NotifyStatusPartialFail:     "Partially Failed",
		NotifyStatusFail:            "Failed",
		NotifyStatusStopped:         "Stopped",
		NotifyStatusTimeout:         "Timed Out",
		NotifyStatusUnknown:         "Unknown",
		NotifyMethodIncremental:     "Incremental Sync",
		NotifyMethodFull:            "Full Sync",
		NotifyMethodMove:            "Move Sync",
		NotifyMethodBisync:          "Bidirectional Sync",

		FmtExcludeRulesUnsupported: "The following filter rules are not supported and were rejected: %s. Only three formats are supported: filename (with * wildcard), *.extension, relative/directory/",
		FmtIncludeRulesUnsupported: "The following whitelist rules are not supported and were rejected: %s. Only filename (with * wildcard) or *.extension are supported",
		FmtMirrorDeleteGuard:       "To prevent accidental deletion, the delete phase of this full sync was skipped: %s. Please verify both source and destination directories are accessible and try again",
		FmtUnsafeDeleteTarget:      "Refused to delete %q in %q: the name is not a single file or directory",
		FmtScanError:               "%s directory scan failed: %s",
		FmtJobDstConflict:          "Destination directory %s is already used by another sync job; the two jobs would overwrite or delete each other's files, please adjust the destination",
		FmtFullSyncApprovalTimeout: "Full sync approval timed out; this run only copied new and changed files and deleted nothing. Review the plan and run a full sync again",
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

// SetLocale sets the active locale (e.g. "zh", "en"). Values are normalized
// (whitespace trimmed, lowercased, region suffix dropped) so configuration
// input such as "EN", "en-US" or "zh_CN" behaves as expected; an unsupported
// locale falls back to "zh" with a warning instead of silently returning
// untranslated keys.
func SetLocale(locale string) {
	mu.Lock()
	currentLocale = normalizeLocale(locale)
	mu.Unlock()
}

// normalizeLocale maps a configured locale onto the supported translation
// tables, returning "zh" for anything that is not recognized.
func normalizeLocale(locale string) string {
	normalized := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(normalized, "-_."); i >= 0 {
		normalized = normalized[:i]
	}
	if normalized == "" {
		return "zh"
	}
	if _, ok := translations[normalized]; ok {
		return normalized
	}
	log.Printf("Warning: unsupported locale %q, falling back to zh", locale)
	return "zh"
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

func IncludeRulesUnsupported(rules []string) string {
	return fmt.Sprintf(T(FmtIncludeRulesUnsupported), strings.Join(rules, ", "))
}

func MirrorDeleteGuard(reason string) string {
	return fmt.Sprintf(T(FmtMirrorDeleteGuard), reason)
}

func UnsafeDeleteTarget(fileName, dir string) string {
	return fmt.Sprintf(T(FmtUnsafeDeleteTarget), fileName, dir)
}

func ScanError(srcOrDst, reason string) string {
	return fmt.Sprintf(T(FmtScanError), srcOrDst, reason)
}

// JobDstConflict reports a destination root shared with another job.
func JobDstConflict(path string) string {
	return fmt.Sprintf(T(FmtJobDstConflict), path)
}

// FullSyncApprovalTimeout reports that a full sync ran copies only because the
// destructive plan was never confirmed.
func FullSyncApprovalTimeout() string {
	return T(FmtFullSyncApprovalTimeout)
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
