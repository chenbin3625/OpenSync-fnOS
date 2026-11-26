import type { NotifyItem } from "../types";

export const channelNames = [
  "自定义 Webhook",
  "Server酱",
  "钉钉机器人",
  "企业微信",
  "飞书 / Lark",
  "Bark",
  "Telegram 机器人",
  "企业微信群机器人",
  "SMTP 邮件",
  "ntfy",
  "Gotify",
  "PushPlus",
];
/** Bark 未填写服务器地址时使用的官方服务。 */
export const defaultBarkServer = "https://api.day.app";
export const defaultTelegramApiBase = "https://api.telegram.org";
export const defaultNtfyServer = "https://ntfy.sh";
export const supportedWebhookMethods = ["POST", "GET", "PUT"] as const;

export const notifyEventModeOptions = [
  { value: "all", label: "全部事件" },
  { value: "errors_only", label: "仅异常 / 失败 / 待审批" },
  { value: "custom", label: "自定义事件" },
] as const;

export const notifyEventOptions = [
  { value: "success", label: "同步成功" },
  { value: "no_sync", label: "无需同步" },
  { value: "partial_fail", label: "部分失败" },
  { value: "fail", label: "同步失败" },
  { value: "stopped", label: "已停止" },
  { value: "timeout", label: "任务超时" },
  { value: "approval", label: "全量删除待审批" },
  { value: "engine_alert", label: "存储引擎健康告警" },
  { value: "check_alert", label: "定时一致性校验差异告警" },
  { value: "digest", label: "定时汇总摘要（日报/周报）" },
] as const;

export const notifyTemplateVariables = [
  { token: "{jobName}", label: "任务名称" },
  { token: "{status}", label: "执行状态" },
  { token: "{method}", label: "同步方式" },
  { token: "{allNum}", label: "总项数" },
  { token: "{successNum}", label: "成功数" },
  { token: "{failNum}", label: "失败数" },
  { token: "{sumSize}", label: "传输大小" },
  { token: "{duration}", label: "耗时" },
  { token: "{errSummary}", label: "失败原因摘要" },
  { token: "{deleteCount}", label: "待删数量" },
  { token: "{deleteSize}", label: "待删大小" },
  { token: "{time}", label: "当前时间" },
] as const;

/** 与后端 notifySendStatus* 常量一致。 */
export const notifySendStatus = { unknown: 0, success: 1, failed: 2 } as const;

export interface NotifyDeliveryState {
  tone: "muted" | "error";
  label: string;
  reason?: string;
}

// 任务完成通知在后台投递，失败此前只写进日志，界面看不到任何异常。这里把行里
// 记录的投递结果翻译成卡片上的一行提示：从未投递过就不显示，避免新建的渠道被
// 误认为出过问题。
export function notifyDeliveryState(
  item: Pick<NotifyItem, "lastSendStatus" | "lastSendError">,
): NotifyDeliveryState | null {
  const status = Number(item.lastSendStatus ?? notifySendStatus.unknown);
  if (status === notifySendStatus.failed) {
    const reason = (item.lastSendError || "").trim();
    return {
      tone: "error",
      label: "最近一次发送失败",
      ...(reason ? { reason } : {}),
    };
  }
  if (status === notifySendStatus.success) {
    return { tone: "muted", label: "最近一次发送成功" };
  }
  return null;
}

export interface NotifyForm {
  method: number;
  enable: boolean;
  notSendNull?: boolean;
  // Event filtering & custom templates
  eventMode?: string;
  events?: string[];
  titleTemplate?: string;
  contentTemplate?: string;
  // Webhook / DingTalk / Lark / WeComBot
  url?: string;
  httpMethod?: string;
  contentType?: string;
  needContent?: boolean;
  titleName?: string;
  contentName?: string;
  body?: string;
  headers?: string;
  // ServerChan
  sendKey?: string;
  version?: string;
  // WeCom App
  corpid?: string;
  corpsecret?: string;
  agentid?: string;
  touser?: string;
  // DingTalk / Lark signing secret
  secret?: string;
  // Bark / ntfy / Gotify
  server?: string;
  deviceKey?: string;
  group?: string;
  sound?: string;
  // Telegram (6)
  botToken?: string;
  chatId?: string;
  apiBase?: string;
  parseMode?: string;
  // WeCom Bot Webhook (7)
  msgtype?: string;
  // SMTP (8)
  host?: string;
  port?: string | number;
  username?: string;
  password?: string;
  from?: string;
  to?: string;
  tls?: string;
  // ntfy (9) / Gotify (10) / PushPlus (11)
  topic?: string;
  token?: string;
  priority?: string | number;
  tags?: string;
  template?: string;
}

export function defaultNotifyForm(): NotifyForm {
  return {
    method: -1,
    enable: true,
    notSendNull: false,
    eventMode: "all",
    events: ["fail", "partial_fail", "timeout", "approval", "engine_alert"],
    titleTemplate: "",
    contentTemplate: "",
    httpMethod: "POST",
    contentType: "application/json",
    needContent: true,
    titleName: "title",
    contentName: "content",
    version: "v3",
    touser: "@all",
    msgtype: "text",
    port: "587",
    tls: "starttls",
    priority: "3",
    template: "txt",
  };
}

// The backend replaces a redacted headers/body value with the marker in full
// ("****" / "******"), never with JSON that merely contains it. Matching the
// whole trimmed value — instead of `includes("****")` — keeps the untouched
// round-trip working while malformed JSON such as {"A":"****" is rejected here
// rather than submitted as a bare string and silently ignored on save.
const redactedMarkers = ["****", "******"];

function trimText(value: unknown): string {
  return typeof value === "string"
    ? value.trim()
    : value === null || value === undefined
      ? ""
      : String(value).trim();
}

function asOptionalString(value: unknown): string {
  return value === null || value === undefined ? "" : String(value);
}

function isRedactedPlaceholder(value: string): boolean {
  return redactedMarkers.includes(value.trim());
}

function parseObject(value?: unknown): unknown {
  const trimmed = trimText(value);
  if (!trimmed) return undefined;
  if (isRedactedPlaceholder(trimmed)) return trimmed;
  const parsed: unknown = JSON.parse(trimmed);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
    throw new Error("请输入有效 JSON 对象");
  return parsed;
}

function buildSharedOptions(form: NotifyForm): Record<string, unknown> {
  const common: Record<string, unknown> = {
    notSendNull: Boolean(form.notSendNull),
  };
  const mode = trimText(form.eventMode);
  if (mode && mode !== "all") {
    common.eventMode = mode;
    if (mode === "custom") {
      common.events = Array.isArray(form.events) ? form.events : [];
    }
  }
  const titleTpl = trimText(form.titleTemplate);
  if (titleTpl) common.titleTemplate = titleTpl;
  const contentTpl = trimText(form.contentTemplate);
  if (contentTpl) common.contentTemplate = contentTpl;
  return common;
}

export function buildNotifyParams(form: NotifyForm) {
  const common = buildSharedOptions(form);
  switch (form.method) {
    case 0: {
      parseObject(form.body);
      return {
        ...common,
        url: trimText(form.url) || undefined,
        httpMethod: trimText(form.httpMethod) || "POST",
        contentType: trimText(form.contentType) || "application/json",
        needContent: form.needContent ?? true,
        titleName: trimText(form.titleName) || "title",
        contentName: trimText(form.contentName) || "content",
        body: trimText(form.body) || undefined,
        headers: parseObject(form.headers),
      };
    }
    case 1:
      return {
        ...common,
        sendKey:
          form.sendKey === null || form.sendKey === undefined
            ? undefined
            : String(form.sendKey),
        version: trimText(form.version) || "v3",
      };
    case 2:
    case 4:
      // 空字符串也要提交：后端据此关闭签名，而不是回填已保存的 secret。
      return {
        ...common,
        url: trimText(form.url) || undefined,
        secret: trimText(form.secret),
      };
    case 3:
      return {
        ...common,
        corpid:
          form.corpid === null || form.corpid === undefined
            ? undefined
            : String(form.corpid),
        corpsecret:
          form.corpsecret === null || form.corpsecret === undefined
            ? undefined
            : String(form.corpsecret),
        agentid:
          form.agentid === null || form.agentid === undefined
            ? undefined
            : String(form.agentid),
        touser: trimText(form.touser) || "@all",
      };
    case 5:
      return {
        ...common,
        server: trimText(form.server) || defaultBarkServer,
        deviceKey: trimText(form.deviceKey) || undefined,
        group: trimText(form.group) || undefined,
        sound: trimText(form.sound) || undefined,
      };
    case 6:
      return {
        ...common,
        botToken: trimText(form.botToken) || undefined,
        chatId: trimText(form.chatId) || undefined,
        apiBase: trimText(form.apiBase) || undefined,
        parseMode: trimText(form.parseMode) || undefined,
      };
    case 7:
      return {
        ...common,
        url: trimText(form.url) || undefined,
        msgtype: trimText(form.msgtype) || "text",
      };
    case 8: {
      const parsedPort = Number(trimText(form.port) || "587");
      return {
        ...common,
        host: trimText(form.host) || undefined,
        port: Number.isInteger(parsedPort) ? parsedPort : 587,
        username: trimText(form.username) || undefined,
        password:
          form.password === null || form.password === undefined
            ? undefined
            : String(form.password),
        from: trimText(form.from) || undefined,
        to: trimText(form.to) || undefined,
        tls: trimText(form.tls) || "starttls",
      };
    }
    case 9:
      return {
        ...common,
        server: trimText(form.server) || defaultNtfyServer,
        topic: trimText(form.topic) || undefined,
        token: trimText(form.token) || undefined,
        priority: trimText(form.priority) || undefined,
        tags: trimText(form.tags) || undefined,
      };
    case 10: {
      const p = Number(trimText(form.priority));
      return {
        ...common,
        server: trimText(form.server) || undefined,
        token: trimText(form.token) || undefined,
        priority: Number.isFinite(p) && trimText(form.priority) !== "" ? p : 5,
      };
    }
    case 11:
      return {
        ...common,
        token: trimText(form.token) || undefined,
        topic: trimText(form.topic) || undefined,
        template: trimText(form.template) || "txt",
      };
    default:
      throw new Error("通知渠道无效");
  }
}

function parseStoredNotifyParams(raw?: string | null): Record<string, unknown> {
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return {};
    }
    const clean: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(parsed)) {
      if (value !== null && value !== undefined) clean[key] = value;
    }
    return clean;
  } catch {
    return {};
  }
}

export function notifyToForm(
  item: Pick<NotifyItem, "id" | "method" | "enable" | "params">,
): NotifyForm {
  const params = parseStoredNotifyParams(item.params);
  const events = Array.isArray(params.events)
    ? params.events.map((v) => String(v))
    : defaultNotifyForm().events;
  return {
    ...defaultNotifyForm(),
    ...params,
    method: item.method,
    eventMode: asOptionalString(params.eventMode) || "all",
    events,
    titleTemplate: asOptionalString(params.titleTemplate),
    contentTemplate: asOptionalString(params.contentTemplate),
    httpMethod:
      asOptionalString(params.method) ||
      asOptionalString(params.httpMethod) ||
      "POST",
    contentType: asOptionalString(params.contentType) || "application/json",
    needContent:
      params.needContent === undefined ? true : Boolean(params.needContent),
    notSendNull: Boolean(params.notSendNull),
    titleName: asOptionalString(params.titleName) || "title",
    contentName: asOptionalString(params.contentName) || "content",
    version: asOptionalString(params.version) || "v3",
    enable: item.enable === 1,
    url: asOptionalString(params.url) || asOptionalString(params.webhook),
    corpid: asOptionalString(params.corpid) || asOptionalString(params.corpId),
    corpsecret:
      asOptionalString(params.corpsecret) || asOptionalString(params.corpSecret),
    agentid:
      asOptionalString(params.agentid) || asOptionalString(params.agentId),
    touser:
      asOptionalString(params.touser) ||
      asOptionalString(params.toUser) ||
      "@all",
    secret: asOptionalString(params.secret),
    server:
      asOptionalString(params.server) ||
      (item.method === 5
        ? defaultBarkServer
        : item.method === 9
          ? defaultNtfyServer
          : ""),
    deviceKey: asOptionalString(params.deviceKey),
    group: asOptionalString(params.group) || undefined,
    sound: asOptionalString(params.sound) || undefined,
    sendKey: asOptionalString(params.sendKey),
    botToken: asOptionalString(params.botToken),
    chatId: asOptionalString(params.chatId),
    apiBase: asOptionalString(params.apiBase),
    parseMode: asOptionalString(params.parseMode),
    msgtype: asOptionalString(params.msgtype) || "text",
    host: asOptionalString(params.host),
    port: params.port !== undefined ? String(params.port) : "587",
    username: asOptionalString(params.username),
    password: asOptionalString(params.password),
    from: asOptionalString(params.from),
    to: asOptionalString(params.to),
    tls: asOptionalString(params.tls) || "starttls",
    topic: asOptionalString(params.topic),
    token: asOptionalString(params.token),
    priority:
      params.priority !== undefined
        ? String(params.priority)
        : item.method === 10
          ? "5"
          : "3",
    tags: asOptionalString(params.tags),
    template: asOptionalString(params.template) || "txt",
    body:
      params.body && typeof params.body === "object"
        ? JSON.stringify(params.body, null, 2)
        : asOptionalString(params.body),
    headers:
      params.headers && typeof params.headers === "object"
        ? JSON.stringify(params.headers, null, 2)
        : asOptionalString(params.headers),
  };
}

/** Methods whose webhook URL is allowed to be plain HTTP (custom webhook). */
const httpWebhookMethods = new Set([0]);
/** Methods whose URL carries a credential and therefore requires TLS. */
const httpsOnlyWebhookMethods = new Set([2, 4, 7]);

function validateHttpUrl(raw: string, label: string, requireHttps = false): string {
  try {
    const url = new URL(raw);
    if (requireHttps) {
      if (url.protocol !== "https:") return `${label}必须使用 HTTPS 地址`;
    } else if (!["http:", "https:"].includes(url.protocol)) {
      return `请输入有效的 HTTP / HTTPS ${label}`;
    }
    return "";
  } catch {
    return `请输入有效的${label}`;
  }
}

export function validateNotifyForm(form: NotifyForm) {
  if (form.method < 0 || form.method >= channelNames.length)
    return "请选择通知渠道";
  if (form.eventMode === "custom" && (!form.events || form.events.length === 0)) {
    return "自定义事件模式下请至少选择一个通知事件";
  }
  if (httpWebhookMethods.has(form.method) || httpsOnlyWebhookMethods.has(form.method)) {
    let url: URL;
    try {
      url = new URL(trimText(form.url));
    } catch {
      return "请输入有效的 Webhook URL";
    }
    if (httpsOnlyWebhookMethods.has(form.method)) {
      if (url.protocol !== "https:") {
        return form.method === 7
          ? "企业微信群机器人 Webhook 必须使用 HTTPS 地址"
          : "钉钉 / 飞书 Webhook 必须使用 HTTPS 地址";
      }
    } else if (!["http:", "https:"].includes(url.protocol)) {
      return "请输入有效的 HTTP / HTTPS Webhook URL";
    }
  }
  if (
    form.method === 0 &&
    !supportedWebhookMethods.includes(
      (trimText(form.httpMethod) || "POST").toUpperCase() as (typeof supportedWebhookMethods)[number],
    )
  )
    return "请选择受支持的 HTTP 方法";
  if (form.method === 1 && !trimText(form.sendKey))
    return "请输入 Server酱 SendKey";
  if (
    form.method === 3 &&
    (!trimText(form.corpid) || !trimText(form.corpsecret) || !trimText(form.agentid))
  )
    return "请填写企业 ID、Secret 和 AgentId";
  if (form.method === 5) {
    if (!trimText(form.deviceKey)) return "请输入 Bark Device Key";
    const server = trimText(form.server);
    if (server) {
      try {
        const url = new URL(server);
        if (!["http:", "https:"].includes(url.protocol))
          return "请输入有效的 HTTP / HTTPS 服务器地址";
      } catch {
        return "请输入有效的 Bark 服务器地址";
      }
    }
  }
  if (form.method === 6) {
    if (!trimText(form.botToken)) return "请输入 Telegram Bot Token";
    if (!trimText(form.chatId)) return "请输入 Telegram Chat ID";
    const apiBase = trimText(form.apiBase);
    if (apiBase) {
      const err = validateHttpUrl(apiBase, "API 地址");
      if (err) return err;
    }
  }
  if (form.method === 8) {
    if (!trimText(form.host)) return "请输入 SMTP 服务器地址";
    const port = Number(trimText(form.port) || "587");
    if (!Number.isInteger(port) || port < 1 || port > 65535)
      return "SMTP 端口必须在 1–65535 之间";
    if (!trimText(form.from)) return "请输入发件人邮箱";
    if (!trimText(form.to)) return "请输入收件人邮箱";
  }
  if (form.method === 9) {
    if (!trimText(form.topic)) return "请输入 ntfy 主题（Topic）";
    const server = trimText(form.server);
    if (server) {
      const err = validateHttpUrl(server, "服务器地址");
      if (err) return err;
    }
  }
  if (form.method === 10) {
    if (!trimText(form.server)) return "请输入 Gotify 服务器地址";
    const err = validateHttpUrl(trimText(form.server), "服务器地址");
    if (err) return err;
    if (!trimText(form.token)) return "请输入 Gotify App Token";
  }
  if (form.method === 11) {
    if (!trimText(form.token)) return "请输入 PushPlus Token";
  }
  try {
    buildNotifyParams(form);
  } catch {
    return "请求体和请求头必须是有效 JSON 对象";
  }
  return "";
}

export function getNotifyEndpointSummary(item: Pick<NotifyItem, "method" | "params">): string {
  try {
    const parsed = JSON.parse(item.params || "{}") as Record<string, unknown>;
    switch (item.method) {
      case 6:
        return parsed.chatId ? `Chat ID: ${String(parsed.chatId)}` : "Telegram Bot";
      case 8:
        return parsed.host
          ? `${String(parsed.host)}:${String(parsed.port || 587)} → ${String(parsed.to || "")}`
          : "—";
      case 9:
        return `${String(parsed.server || defaultNtfyServer)}/${String(parsed.topic || "")}`;
      case 10:
        return String(parsed.server || "—");
      case 11:
        return parsed.topic ? `群组 ${String(parsed.topic)}` : "PushPlus 推送";
      default:
        return String(parsed.url || parsed.webhook || parsed.server || "—");
    }
  } catch {
    return "—";
  }
}
