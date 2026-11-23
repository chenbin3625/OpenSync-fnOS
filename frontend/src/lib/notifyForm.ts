import type { NotifyItem } from "../types";

export const channelNames = [
  "自定义 Webhook",
  "Server酱",
  "钉钉机器人",
  "企业微信",
  "飞书 / Lark",
  "Bark",
];
/** Bark 未填写服务器地址时使用的官方服务。 */
export const defaultBarkServer = "https://api.day.app";
export const supportedWebhookMethods = ["POST", "GET", "PUT"] as const;

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
  url?: string;
  httpMethod?: string;
  contentType?: string;
  needContent?: boolean;
  titleName?: string;
  contentName?: string;
  body?: string;
  headers?: string;
  sendKey?: string;
  version?: string;
  corpid?: string;
  corpsecret?: string;
  agentid?: string;
  touser?: string;
  /** 钉钉加签 / 飞书签名校验密钥，留空表示不签名。 */
  secret?: string;
  server?: string;
  deviceKey?: string;
  group?: string;
  sound?: string;
}
export function defaultNotifyForm(): NotifyForm {
  return {
    method: -1,
    enable: true,
    notSendNull: false,
    httpMethod: "POST",
    contentType: "application/json",
    needContent: true,
    titleName: "title",
    contentName: "content",
    version: "v3",
    touser: "@all",
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
export function buildNotifyParams(form: NotifyForm) {
  const common = { notSendNull: Boolean(form.notSendNull) };
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
  return {
    ...defaultNotifyForm(),
    ...params,
    method: item.method,
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
      (item.method === 5 ? defaultBarkServer : ""),
    deviceKey: asOptionalString(params.deviceKey),
    group: asOptionalString(params.group) || undefined,
    sound: asOptionalString(params.sound) || undefined,
    sendKey: asOptionalString(params.sendKey),
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
const httpsOnlyWebhookMethods = new Set([2, 4]);

export function validateNotifyForm(form: NotifyForm) {
  if (form.method < 0) return "请选择通知渠道";
  if (httpWebhookMethods.has(form.method) || httpsOnlyWebhookMethods.has(form.method)) {
    let url: URL;
    try {
      url = new URL(trimText(form.url));
    } catch {
      return "请输入有效的 Webhook URL";
    }
    if (httpsOnlyWebhookMethods.has(form.method)) {
      // DingTalk / Lark put the access token in the URL, so the backend rejects
      // plain HTTP for them (validateNotifyHTTPSURL); accepting it here only
      // produced a server error after the form said it was fine.
      if (url.protocol !== "https:")
        return "钉钉 / 飞书 Webhook 必须使用 HTTPS 地址";
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
  try {
    buildNotifyParams(form);
  } catch {
    return "请求体和请求头必须是有效 JSON 对象";
  }
  return "";
}
