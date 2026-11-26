import { useEffect, useRef, useState, type ChangeEvent } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconDownload,
  IconRefresh,
  IconSaveStroked,
  IconUpload,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { BwLimitEditor } from "../components/BwLimitEditor";
import { VersionLink } from "../components/VersionLink";
import {
  confirmAction,
  errorToast,
  Header,
  HelpTip,
  LoadState,
  SectionCard,
  SettingRow,
} from "../components/common";
import {
  formatBwLimitSummary,
  validateBwLimitExpression,
} from "../lib/bwLimit";
import { useAction, useResource } from "../lib/hooks";
import { getHost } from "../lib/host";
import { formatSize, normalizeSettingFieldValue } from "./Home/homeUtils";
import type {
  SystemBackupPayload,
  SystemImportSummary,
  SystemSettings,
} from "../types";

const fields = [
  {
    key: "copyConcurrency",
    label: "操作并发数",
    min: 1,
    max: 100,
    unit: "个",
    tip: "同时执行的复制、删除、移动操作数量",
  },
  {
    key: "scanConcurrency",
    label: "扫描并发数",
    min: 1,
    // Backend config.MaxScanConcurrency is 50 (config.go); a smaller UI cap
    // rejected values the server accepts.
    max: 50,
    unit: "个",
    tip: "同时扫描源端和目标端目录的请求数量",
  },
  {
    key: "maxRetries",
    label: "失败重试次数",
    min: 0,
    max: 10,
    unit: "次",
    tip: "单个文件操作失败后的最大重试次数",
  },
  {
    key: "scheduledFullSyncConfirm",
    label: "定时全量删除确认",
    min: 0,
    max: 1,
    unit: "",
    kind: "switch",
    tip: "开启后，定时全量同步完成文件同步后将目标端待删除项转入待确认清单，不阻塞同步任务；手动全量同步的删除始终需确认后才执行",
  },
  {
    key: "fullSyncApprovalTimeout",
    label: "删除审批超时",
    min: 0,
    max: 1440,
    unit: "分钟",
    tip: "待确认删除清单的有效期，超时未确认将自动过期并保留目标端文件（不影响已完成的同步任务）；0 表示永不过期直至手动处理",
  },
  {
    key: "taskTimeout",
    label: "任务超时",
    min: 0,
    max: 8760,
    unit: "小时",
    tip: "单个任务运行超过该时长后自动标记为超时",
  },
  {
    key: "engineHealthCheckInterval",
    label: "存储健康检查周期",
    min: 0,
    max: 1440,
    unit: "分钟",
    tip: "后台自动探测内置 rclone 与远程存储连通性的间隔，异常或恢复时自动发送告警；0 表示关闭定期检测",
  },
  {
    key: "taskSave",
    label: "任务记录保留",
    min: 0,
    max: 3650,
    unit: "天",
    tip: "历史任务记录保留天数，0 表示不自动清理",
  },
  {
    key: "defaultBwLimit",
    label: "全局默认带宽限制",
    kind: "text",
    placeholder: "如 10M 或 08:00,5M 23:00,off（留空不限速）",
    tip: "对所有未单独设置限速的同步任务、双向同步、一致性校验、恢复与离线下载生效；修改保存后将立即同步至正在运行的跟随任务（并发多任务时按单任务分别限速）",
  },
  {
    key: "proxyUrl",
    label: "全局代理地址",
    kind: "text",
    placeholder: "如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080",
    tip: "内置 rclone 引擎访问远程存储时使用的全局 HTTP / HTTPS / SOCKS5 代理（本地授权目录自动直连）",
  },
  {
    key: "localMaxConcurrentJobs",
    label: "本地磁盘最大并发任务",
    min: 0,
    max: 50,
    unit: "个",
    tip: "限制同时读写本地授权目录的任务数，防止多任务并发压满 NAS 硬盘 I/O（0 表示不限）",
  },
  {
    key: "minFreeSpaceMB",
    label: "目标磁盘最低预留空间",
    min: 0,
    max: 10485760,
    unit: "MB",
    tip: "任务启动前检查目标存储剩余空间，低于该值时自动中止以防写满系统盘（0 表示关闭检查）",
  },
  {
    key: "autoResumeInterrupted",
    label: "重启后自动续跑中断任务",
    min: 0,
    max: 1,
    unit: "",
    kind: "switch",
    tip: "开启后，NAS 重启或应用更新恢复运行时，自动重新继续上次异常中断的任务未完成项",
  },
  {
    key: "digestEnable",
    label: "开启定时汇总摘要通知",
    min: 0,
    max: 1,
    unit: "",
    kind: "switch",
    tip: "按日或按周汇总全部任务的执行次数、失败数与总传输量，推送到已启用的通知渠道",
  },
  {
    key: "digestSchedule",
    label: "摘要推送周期",
    kind: "select",
    options: [
      { label: "每日汇总", value: "daily" },
      { label: "每周汇总", value: "weekly" },
    ],
    tip: "选择每日汇总（统计过去 24 小时）或每周汇总（统计过去 7 天）",
  },
  {
    key: "digestHour",
    label: "摘要推送时间（小时）",
    min: 0,
    max: 23,
    unit: "时",
    tip: "每日/每周摘要推送的整点小时（0–23）",
  },
  {
    key: "digestWeekday",
    label: "每周摘要推送日",
    kind: "select",
    options: [
      { label: "周日", value: "0" },
      { label: "周一", value: "1" },
      { label: "周二", value: "2" },
      { label: "周三", value: "3" },
      { label: "周四", value: "4" },
      { label: "周五", value: "5" },
      { label: "周六", value: "6" },
    ],
    tip: "仅当推送周期为「每周汇总」时生效",
  },
  {
    key: "locale",
    label: "后端提示语言（Locale）",
    kind: "select",
    options: [
      { label: "简体中文 (zh-CN)", value: "zh-CN" },
      { label: "English (en-US)", value: "en-US" },
    ],
    tip: "切换服务端错误提示与系统通知消息的语言",
  },
] as const;
type SettingsForm = Record<(typeof fields)[number]["key"], string>;

const cardGroups = [
  {
    title: "任务执行与安全保护",
    keys: [
      "copyConcurrency",
      "scanConcurrency",
      "maxRetries",
      "taskTimeout",
      "localMaxConcurrentJobs",
      "minFreeSpaceMB",
      "autoResumeInterrupted",
      "scheduledFullSyncConfirm",
      "fullSyncApprovalTimeout",
    ] as const,
  },
  {
    title: "传输限速与网络代理",
    keys: ["defaultBwLimit", "proxyUrl"] as const,
  },
  {
    title: "定时汇总摘要通知",
    keys: [
      "digestEnable",
      "digestSchedule",
      "digestHour",
      "digestWeekday",
    ] as const,
  },
  {
    title: "监控、历史与语言",
    keys: ["engineHealthCheckInterval", "taskSave", "locale"] as const,
  },
];

const fieldOf = (key: (typeof fields)[number]["key"]) =>
  fields.find((field) => field.key === key)!;

function formatSettingFieldValue(
  field: (typeof fields)[number],
  value: unknown,
): string {
  if ("kind" in field && field.kind === "select") {
    if (field.key === "digestSchedule") {
      return typeof value === "string" && value.trim() ? value.trim() : "daily";
    }
    if (field.key === "digestWeekday") {
      return value === undefined || value === null || value === ""
        ? "1"
        : String(value);
    }
    if (field.key === "locale") {
      return typeof value === "string" && value.trim() ? value.trim() : "zh-CN";
    }
  }
  if (
    !("kind" in field) &&
    (value === undefined || value === null || value === "")
  ) {
    return "0";
  }
  return normalizeSettingFieldValue(
    value,
    "kind" in field && field.kind === "switch",
  );
}

export function toSettingsForm(
  data: Partial<SystemSettings>,
): SettingsForm {
  return Object.fromEntries(
    fields.map((field) => [
      field.key,
      formatSettingFieldValue(field, data[field.key]),
    ]),
  ) as SettingsForm;
}

export function isSettingsDirty(
  form: SettingsForm | null,
  data: Partial<SystemSettings> | null | undefined,
): boolean {
  if (!form || !data) return false;
  const baseline = toSettingsForm(data);
  return fields.some(({ key }) => form[key] !== baseline[key]);
}

export default function Settings() {
  const resource = useResource((signal) => api.settings(signal));
  const [form, setForm] = useState<SettingsForm | null>(null);
  const [bwLimitError, setBwLimitError] = useState<string | null>(null);
  const action = useAction();

  // Backup / Restore state
  const [includeSecrets, setIncludeSecrets] = useState(false);
  const [conflictMode, setConflictMode] = useState<"skip" | "overwrite">("skip");
  const [importing, setImporting] = useState(false);
  const [importSummary, setImportSummary] = useState<SystemImportSummary | null>(
    null,
  );
  const fileInputRef = useRef<HTMLInputElement>(null);

  // Built-in logs viewer state
  const [logLevel, setLogLevel] = useState("all");
  const [logLines, setLogLines] = useState(200);
  const [logKeywordInput, setLogKeywordInput] = useState("");
  const [logKeyword, setLogKeyword] = useState("");
  const logs = useResource(
    (signal) =>
      api.systemLogs(
        {
          lines: logLines,
          level: logLevel === "all" ? "" : logLevel,
          keyword: logKeyword,
        },
        signal,
      ),
    [logLines, logLevel, logKeyword],
  );

  useEffect(() => {
    if (resource.data) setForm(toSettingsForm(resource.data));
  }, [resource.data]);
  const dirty = isSettingsDirty(form, resource.data);
  const skipNextGuardRef = useRef(false);
  useEffect(() => {
    if (!dirty) return;
    const host = getHost();
    if (!host.isStandaloneWeb) {
      void host
        .setExitPageTips({ title: "尚未保存", content: "是否放弃当前修改？" })
        .catch(() => {});
    }
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    const onDocumentClick = (event: MouseEvent) => {
      if (skipNextGuardRef.current) {
        skipNextGuardRef.current = false;
        return;
      }
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      ) {
        return;
      }
      const target = event.target as Element | null;
      if (!target) return;
      const anchor = target.closest("a[href]") as HTMLAnchorElement | null;
      const menuToggle = target.closest(
        "button.nav-menu-toggle",
      ) as HTMLButtonElement | null;
      let navElement: HTMLElement | null = null;
      if (anchor) {
        if (anchor.target && anchor.target !== "_self") return;
        if (anchor.hasAttribute("download")) return;
        try {
          const dest = new URL(anchor.href, window.location.href);
          if (dest.origin !== window.location.origin) return;
          if (
            dest.pathname + dest.search ===
            window.location.pathname + window.location.search
          ) {
            return;
          }
          navElement = anchor;
        } catch {
          return;
        }
      } else if (menuToggle) {
        navElement = menuToggle;
      }
      if (!navElement) return;
      event.preventDefault();
      event.stopPropagation();
      const targetElement = navElement;
      confirmAction({
        title: "放弃未保存的修改？",
        content: "当前设置尚未保存，离开后修改将丢失。",
        okText: "放弃",
        onOk: () => {
          skipNextGuardRef.current = true;
          targetElement.click();
          skipNextGuardRef.current = false;
        },
      });
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    document.addEventListener("click", onDocumentClick, true);
    return () => {
      window.removeEventListener("beforeunload", onBeforeUnload);
      document.removeEventListener("click", onDocumentClick, true);
      if (!host.isStandaloneWeb) void host.setExitPageTips().catch(() => {});
    };
  }, [dirty]);

  const save = () => {
    if (!form || !resource.data) return;
    if (bwLimitError) {
      Toast.warning(bwLimitError);
      return;
    }
    const values: SystemSettings = { ...resource.data };
    for (const field of fields) {
      const input = form[field.key].trim();
      if ("kind" in field && field.kind === "text") {
        if (field.key === "defaultBwLimit") {
          const bwErr = validateBwLimitExpression(input);
          if (bwErr) {
            Toast.warning(bwErr);
            return;
          }
        }
        if (field.key === "proxyUrl" && input) {
          try {
            const parsed = new URL(input);
            if (
              !["http:", "https:", "socks5:", "socks5h:"].includes(
                parsed.protocol,
              ) ||
              !parsed.hostname
            ) {
              Toast.warning(
                "全局代理地址仅支持 http://、https://、socks5:// 或 socks5h://",
              );
              return;
            }
          } catch {
            Toast.warning("全局代理地址格式无效");
            return;
          }
        }
        values[field.key] = input;
        continue;
      }
      if ("kind" in field && field.kind === "select") {
        if (field.key === "digestWeekday") {
          values.digestWeekday = Number(input) || 0;
        } else if (field.key === "digestSchedule") {
          values.digestSchedule = input || "daily";
        } else if (field.key === "locale") {
          values.locale = input || "zh-CN";
        }
        continue;
      }
      const value = Number(input);
      if (
        !/^\d+$/.test(input) ||
        !Number.isSafeInteger(value) ||
        value < field.min ||
        value > field.max
      ) {
        Toast.warning(`${field.label}请输入 ${field.min}–${field.max} 的整数`);
        return;
      }
      values[field.key] = value;
    }
    void action.run(async () => {
      try {
        await api.saveSettings(values);
        Toast.success("配置已保存");
        await resource.refresh(true);
      } catch (err) {
        errorToast(err, "保存失败");
      }
    });
  };

  const sendDigestNow = () => {
    const period =
      form?.digestSchedule === "weekly" ? "weekly" : ("daily" as const);
    void action.run(async () => {
      try {
        await api.sendDigestNow(period);
        Toast.success(
          period === "weekly"
            ? "已推送一次每周备份摘要通知"
            : "已推送一次每日备份摘要通知",
        );
      } catch (err) {
        errorToast(err, "推送摘要失败");
      }
    });
  };

  const handleExport = () => {
    const link = document.createElement("a");
    link.href = api.exportConfigUrl(includeSecrets);
    link.download = `opensync-backup-${new Date().toISOString().slice(0, 10)}.json`;
    document.body.appendChild(link);
    link.click();
    link.remove();
  };

  const handleImportFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    if (file.size > 10 * 1024 * 1024) {
      Toast.warning("配置文件过大（限制 10MB 以内）");
      return;
    }
    setImporting(true);
    try {
      const text = await file.text();
      const parsed = JSON.parse(text) as SystemBackupPayload;
      if (!parsed || typeof parsed !== "object") {
        throw new Error("文件内容不是有效的 JSON 对象");
      }
      const summary = await api.importConfig(parsed, conflictMode);
      setImportSummary(summary);
      Toast.success("配置导入完成");
      await resource.refresh(true);
      window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    } catch (err) {
      errorToast(err, "导入配置失败");
    } finally {
      setImporting(false);
    }
  };

  const handleDownloadLogs = () => {
    const link = document.createElement("a");
    link.href = api.downloadSystemLogsUrl({
      lines: 2000,
      level: logLevel === "all" ? "" : logLevel,
      keyword: logKeyword,
    });
    link.download = `opensync-logs-${new Date().toISOString().slice(0, 10)}.log`;
    document.body.appendChild(link);
    link.click();
    link.remove();
  };

  const input = (field: (typeof fields)[number]) => {
    if (field.key === "defaultBwLimit") {
      const currentVal = form![field.key];
      const summary = formatBwLimitSummary(currentVal, { allowInherit: false });
      const isLimited = Boolean(currentVal.trim());
      return (
        <div key={field.key} className="settings-bw-block">
          <div className="settings-bw-head">
            <div className="settings-bw-title">
              <span className="setting-row-label">
                {field.label}
                {field.tip && (
                  <HelpTip content={field.tip} label={`${field.label}说明`} />
                )}
              </span>
              <Tag
                size="small"
                color={isLimited ? "blue" : "green"}
                type="light"
              >
                {summary}
              </Tag>
            </div>
            <p className="settings-bw-desc muted">
              作为系统全局默认规则，对所有未单独配置限速的同步任务、双向同步、一致性校验、文件恢复与离线下载生效；保存后将实时推送至正在运行的跟随任务（同时运行多个任务时按单任务分别限速）。
            </p>
          </div>
          <BwLimitEditor
            value={currentVal}
            disabled={action.busy}
            onChange={(value) => {
              setForm((f) => f && { ...f, defaultBwLimit: value });
            }}
            onValidityChange={setBwLimitError}
          />
        </div>
      );
    }
    return (
      <SettingRow
        key={field.key}
        label={field.label}
        variant="compact"
        tip={field.tip}
      >
        {"kind" in field && field.kind === "switch" ? (
          <Switch
            aria-label={field.label}
            checked={form![field.key] === "1"}
            disabled={action.busy}
            onChange={(checked) => {
              setForm((f) => f && { ...f, [field.key]: checked ? "1" : "0" });
            }}
          />
        ) : "kind" in field && field.kind === "select" ? (
          <Select
            aria-label={field.label}
            value={form![field.key]}
            disabled={action.busy}
            style={{ width: 180 }}
            optionList={[...field.options]}
            onChange={(value) => {
              setForm((f) => f && { ...f, [field.key]: String(value ?? "") });
            }}
          />
        ) : "kind" in field && field.kind === "text" ? (
          <div className="text-setting">
            <Input
              aria-label={field.label}
              value={form![field.key]}
              placeholder={field.placeholder}
              showClear
              disabled={action.busy}
              onChange={(value) => {
                setForm((f) => f && { ...f, [field.key]: value });
              }}
            />
          </div>
        ) : (
          <div className="numeric-setting">
            <Input
              aria-label={field.label}
              inputMode="numeric"
              value={form![field.key]}
              disabled={action.busy}
              onChange={(value) => {
                setForm((f) => f && { ...f, [field.key]: value });
              }}
            />
            <span>{field.unit}</span>
          </div>
        )}
      </SettingRow>
    );
  };

  return (
    <div className="page settings-page">
      <Header
        title="设置"
        actions={
          <Button
            theme="solid"
            icon={<IconSaveStroked aria-hidden="true" />}
            loading={action.busy}
            disabled={(!dirty && !bwLimitError) || resource.loading || Boolean(resource.error)}
            onClick={save}
          >
            保存
          </Button>
        }
      />
      <LoadState
        loading={resource.loading}
        error={resource.error}
        retry={resource.refresh}
      >
        {form &&
          cardGroups.map((group) => (
            <SectionCard
              key={group.title}
              className="settings-card"
              headClassName="settings-card-head"
              bodyClassName="settings-card-body"
              title={group.title}
              extra={
                group.title === "定时汇总摘要通知" ? (
                  <Button
                    size="small"
                    type="tertiary"
                    disabled={action.busy}
                    onClick={sendDigestNow}
                  >
                    立即发送一次摘要
                  </Button>
                ) : undefined
              }
            >
              {group.keys.map((key) => (
                <div key={key}>{input(fieldOf(key))}</div>
              ))}
            </SectionCard>
          ))}

        <SectionCard
          className="settings-card"
          headClassName="settings-card-head"
          bodyClassName="settings-card-body settings-ops-body"
          title="配置备份与恢复"
        >
          <div className="settings-backup-block">
            <div className="settings-backup-row">
              <div className="settings-backup-desc">
                <strong>导出配置</strong>
                <span className="muted">
                  导出系统设置、存储配置、通知渠道与同步任务为 JSON 文件，便于迁移或留存副本。
                </span>
                <Checkbox
                  checked={includeSecrets}
                  onChange={(e) => setIncludeSecrets(Boolean(e.target.checked))}
                >
                  同时导出敏感凭据（远程存储密钥、通知密钥与密码）
                </Checkbox>
              </div>
              <Button
                icon={<IconDownload aria-hidden="true" />}
                onClick={handleExport}
              >
                导出配置 JSON
              </Button>
            </div>
            <div className="settings-backup-row">
              <div className="settings-backup-desc">
                <strong>导入配置</strong>
                <span className="muted">
                  从备份 JSON 恢复配置，系统会自动重新映射存储配置与通知渠道 ID。
                </span>
              </div>
              <div className="settings-backup-controls">
                <Select
                  aria-label="同名配置冲突处理方式"
                  value={conflictMode}
                  style={{ width: 148 }}
                  optionList={[
                    { label: "跳过已存在项", value: "skip" },
                    { label: "覆盖已存在项", value: "overwrite" },
                  ]}
                  onChange={(v) => setConflictMode(v as "skip" | "overwrite")}
                />
                <input
                  ref={fileInputRef}
                  type="file"
                  accept=".json,application/json"
                  style={{ display: "none" }}
                  onChange={(e) => void handleImportFile(e)}
                />
                <Button
                  icon={<IconUpload aria-hidden="true" />}
                  loading={importing}
                  onClick={() => fileInputRef.current?.click()}
                >
                  导入配置 JSON
                </Button>
              </div>
            </div>
          </div>
        </SectionCard>

        <SectionCard
          className="settings-card"
          headClassName="settings-card-head"
          bodyClassName="settings-card-body settings-ops-body"
          title="应用运行日志"
          extra={
            <div className="settings-log-meta">
              {logs.data && (
                <span className="muted">
                  共 {logs.data.total} 行
                  {logs.data.fileSize > 0
                    ? ` · ${formatSize(logs.data.fileSize)}`
                    : ""}
                </span>
              )}
            </div>
          }
        >
          <div className="settings-log-toolbar">
            <Select
              aria-label="日志级别"
              value={logLevel}
              style={{ width: 120 }}
              optionList={[
                { label: "全部级别", value: "all" },
                { label: "仅错误", value: "error" },
                { label: "警告", value: "warn" },
                { label: "信息", value: "info" },
              ]}
              onChange={(v) => setLogLevel(String(v))}
            />
            <Select
              aria-label="显示行数"
              value={logLines}
              style={{ width: 120 }}
              optionList={[100, 200, 500, 1000].map((n) => ({
                label: `最近 ${n} 行`,
                value: n,
              }))}
              onChange={(v) => setLogLines(Number(v))}
            />
            <Input
              aria-label="日志关键词过滤"
              placeholder="输入关键词回车搜索"
              value={logKeywordInput}
              showClear
              onChange={(v) => {
                setLogKeywordInput(v);
                if (!v) setLogKeyword("");
              }}
              onEnterPress={() => setLogKeyword(logKeywordInput.trim())}
            />
            <Button
              type="tertiary"
              icon={<IconRefresh aria-hidden="true" />}
              loading={logs.loading}
              onClick={() => void logs.refresh(true)}
            >
              刷新
            </Button>
            <Button
              type="tertiary"
              icon={<IconDownload aria-hidden="true" />}
              onClick={handleDownloadLogs}
            >
              下载日志
            </Button>
          </div>
          <div className="log-viewer-box" role="log" aria-live="polite">
            {logs.error ? (
              <div className="inline-error">{logs.error}</div>
            ) : !logs.data?.lines?.length ? (
              <div className="muted">暂无匹配的日志记录</div>
            ) : (
              <pre className="log-viewer-content">
                {logs.data.lines.join("\n")}
              </pre>
            )}
          </div>
        </SectionCard>
      </LoadState>

      {importSummary && (
        <Modal
          visible
          centered
          width={480}
          title="配置导入结果"
          onCancel={() => setImportSummary(null)}
          footer={
            <Button
              theme="solid"
              type="primary"
              onClick={() => setImportSummary(null)}
            >
              知道了
            </Button>
          }
        >
          <div className="import-summary">
            <div>
              系统设置：{importSummary.settingsUpdated ? "已更新" : "未变更"}
            </div>
            <div>
              存储配置：新增 {importSummary.enginesCreated} · 更新{" "}
              {importSummary.enginesUpdated} · 跳过 {importSummary.enginesSkipped}
            </div>
            <div>
              通知渠道：新增 {importSummary.notifiesCreated} · 更新{" "}
              {importSummary.notifiesUpdated} · 跳过{" "}
              {importSummary.notifiesSkipped}
            </div>
            <div>
              同步任务：新增 {importSummary.jobsCreated} · 更新{" "}
              {importSummary.jobsUpdated} · 跳过 {importSummary.jobsSkipped}
            </div>
            {importSummary.warnings?.length > 0 && (
              <div className="import-summary-warnings">
                <strong>注意事项</strong>
                <ul>
                  {importSummary.warnings.map((w, idx) => (
                    <li key={idx}>{w}</li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        </Modal>
      )}

      <div className="mobile-version">
        <VersionLink />
      </div>
    </div>
  );
}
