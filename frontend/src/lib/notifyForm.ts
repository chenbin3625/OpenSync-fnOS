import type { NotifyItem } from "../types";

export const channelNames = [
  "自定义 Webhook",
  "Server酱",
  "钉钉机器人",
  "企业微信",
  "飞书 / Lark",
];
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

function isRedactedPlaceholder(value: string): boolean {
  return redactedMarkers.includes(value.trim());
}

function parseObject(value?: string): unknown {
  if (!value?.trim()) return undefined;
  if (isRedactedPlaceholder(value)) return value.trim();
  const parsed: unknown = JSON.parse(value);
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
        url: form.url,
        httpMethod: form.httpMethod || "POST",
        contentType: form.contentType || "application/json",
        needContent: form.needContent ?? true,
        titleName: form.titleName || "title",
        contentName: form.contentName || "content",
        body: form.body?.trim() || undefined,
        headers: parseObject(form.headers),
      };
    }
    case 1:
      return {
        ...common,
        sendKey: form.sendKey,
        version: form.version || "v3",
      };
    case 2:
    case 4:
      return { ...common, url: form.url };
    case 3:
      return {
        ...common,
        corpid: form.corpid,
        corpsecret: form.corpsecret,
        agentid: form.agentid,
        touser: form.touser || "@all",
      };
    default:
      throw new Error("通知渠道无效");
  }
}
export function notifyToForm(
  item: Pick<NotifyItem, "id" | "method" | "enable" | "params">,
): NotifyForm {
  const params = (() => {
    try {
      return JSON.parse(item.params || "{}");
    } catch {
      return {};
    }
  })();
  return {
    ...defaultNotifyForm(),
    ...params,
    method: item.method,
    httpMethod: params.method || params.httpMethod || "POST",
    enable: item.enable === 1,
    url: params.url || params.webhook || "",
    corpid: params.corpid || params.corpId || "",
    corpsecret: params.corpsecret || params.corpSecret || "",
    agentid: params.agentid || params.agentId || "",
    touser: params.touser || params.toUser || "@all",
    body:
      typeof params.body === "object"
        ? JSON.stringify(params.body, null, 2)
        : params.body || "",
    headers:
      typeof params.headers === "object"
        ? JSON.stringify(params.headers, null, 2)
        : params.headers || "",
  };
}
export function validateNotifyForm(form: NotifyForm) {
  if (form.method < 0) return "请选择通知渠道";
  if ([0, 2, 4].includes(form.method)) {
    try {
      const url = new URL(form.url || "");
      if (!["http:", "https:"].includes(url.protocol))
        return "请输入有效的 HTTP / HTTPS Webhook URL";
    } catch {
      return "请输入有效的 Webhook URL";
    }
  }
  if (
    form.method === 0 &&
    !supportedWebhookMethods.includes(
      (form.httpMethod || "POST").trim().toUpperCase() as (typeof supportedWebhookMethods)[number],
    )
  )
    return "请选择受支持的 HTTP 方法";
  if (form.method === 1 && !form.sendKey?.trim())
    return "请输入 Server酱 SendKey";
  if (
    form.method === 3 &&
    (!form.corpid?.trim() || !form.corpsecret?.trim() || !form.agentid?.trim())
  )
    return "请填写企业 ID、Secret 和 AgentId";
  try {
    buildNotifyParams(form);
  } catch {
    return "请求体和请求头必须是有效 JSON 对象";
  }
  return "";
}
