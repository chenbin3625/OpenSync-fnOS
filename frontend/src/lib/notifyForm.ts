import type { NotifyItem } from "../types";

export const channelNames = [
  "自定义 Webhook",
  "Server酱",
  "钉钉机器人",
  "企业微信",
  "飞书 / Lark",
];
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
    method: 0,
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
function parseObject(value?: string): unknown {
  if (!value?.trim()) return undefined;
  if (value.includes("****")) return value;
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
  const params = JSON.parse(item.params || "{}");
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
  if ([0, 2, 4].includes(form.method)) {
    try {
      const url = new URL(form.url || "");
      if (!["http:", "https:"].includes(url.protocol))
        return "请输入有效的 HTTP / HTTPS Webhook URL";
    } catch {
      return "请输入有效的 Webhook URL";
    }
  }
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
