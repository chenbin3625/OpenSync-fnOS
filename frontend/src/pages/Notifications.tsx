import { useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Checkbox from "@douyinfe/semi-ui/lib/es/checkbox";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconChevronRight,
  IconDeleteStroked,
  IconEditStroked,
  IconPlusStroked,
  IconSendStroked,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  Editor,
  Field,
  Header,
  IconButton,
  LoadState,
  ResourceCard,
  SettingRow,
  confirmDelete,
  errorToast,
} from "../components/common";
import { useAction, useEditorForm, useResource } from "../lib/hooks";
import {
  buildNotifyParams,
  channelNames,
  defaultBarkServer,
  defaultNtfyServer,
  defaultNotifyForm,
  defaultTelegramApiBase,
  getNotifyEndpointSummary,
  notifyDeliveryState,
  notifyEventModeOptions,
  notifyEventOptions,
  notifyTemplateVariables,
  notifyToForm,
  supportedWebhookMethods,
  validateNotifyForm,
  type NotifyForm,
} from "../lib/notifyForm";
import { formatTimestamp } from "../utils/date";
import type { NotifyItem } from "../types";

// 后台投递的结果只存在配置行上，不显示就等于没有反馈：令牌过期的渠道会一直安静
// 地失败，用户以为通知正常。
function DeliveryNote({ item }: { item: NotifyItem }) {
  const delivery = notifyDeliveryState(item);
  if (!delivery) return null;
  const time = Number(item.lastSendTime || 0);
  return (
    <div className={delivery.tone === "error" ? "inline-error" : "item-meta"}>
      {delivery.label}
      {time > 0 ? ` · ${formatTimestamp(time, true)}` : ""}
      {delivery.reason ? `：${delivery.reason}` : ""}
    </div>
  );
}

function getEventFilterBadge(params?: string): string | null {
  try {
    const parsed = JSON.parse(params || "{}") as Record<string, unknown>;
    if (parsed.eventMode === "errors_only") return "仅异常 / 失败";
    if (parsed.eventMode === "custom" && Array.isArray(parsed.events)) {
      return `自定义 ${parsed.events.length} 类事件`;
    }
    return null;
  } catch {
    return null;
  }
}

export default function Notifications() {
  const resource = useResource((signal) => api.notifications(signal));
  const action = useAction();
  const [editing, setEditing] = useState<NotifyItem | null | undefined>();
  const [testingIds, setTestingIds] = useState<Record<number, boolean>>({});
  const [togglingIds, setTogglingIds] = useState<Record<number, boolean>>({});
  const testingInFlight = useRef(new Set<number>());
  const togglingInFlight = useRef(new Set<number>());
  const testNotify = async (item: NotifyItem) => {
    if (testingInFlight.current.has(item.id)) return;
    testingInFlight.current.add(item.id);
    setTestingIds((prev) => ({ ...prev, [item.id]: true }));
    try {
      await api.testNotification({
        id: item.id,
        method: item.method,
        enable: item.enable,
        params: buildNotifyParams(notifyToForm(item)),
      });
      Toast.success("测试通知已发送");
    } catch (error) {
      errorToast(error);
    } finally {
      testingInFlight.current.delete(item.id);
      setTestingIds((prev) => {
        const next = { ...prev };
        delete next[item.id];
        return next;
      });
    }
  };
  const toggleNotify = async (id: number, checked: boolean) => {
    if (togglingInFlight.current.has(id)) return;
    togglingInFlight.current.add(id);
    setTogglingIds((prev) => ({ ...prev, [id]: true }));
    try {
      await api.toggleNotification(id, checked ? 1 : 0);
      Toast.success("通知状态已更新");
      await resource.refresh(true);
    } catch (error) {
      errorToast(error);
    } finally {
      togglingInFlight.current.delete(id);
      setTogglingIds((prev) => {
        const next = { ...prev };
        delete next[id];
        return next;
      });
    }
  };
  return (
    <div className="page">
      <Header
        title="通知配置"
        actions={
          <Button
            theme="solid"
            icon={<IconPlusStroked aria-hidden="true" />}
            onClick={() => setEditing(null)}
          >
            添加通知
          </Button>
        }
      />
      <LoadState
        loading={resource.loading}
        error={resource.error}
        retry={resource.refresh}
        empty={!resource.data?.length}
      >
        <div className="item-list">
          {resource.data?.map((item) => {
            const eventBadge = getEventFilterBadge(item.params);
            return (
              <ResourceCard
                key={item.id}
                className="notification-item"
                data-notify-id={item.id}
                title={
                  <span className="engine-title-row">
                    <span>
                      {channelNames[item.method] || `未知渠道 #${item.method}`}
                    </span>
                    {eventBadge && (
                      <Tag size="small" type="light" color="blue">
                        {eventBadge}
                      </Tag>
                    )}
                  </span>
                }
                subtitle={getNotifyEndpointSummary(item)}
                meta={<DeliveryNote item={item} />}
                actions={
                  <>
                    <Switch
                      aria-label={`启用${channelNames[item.method] || "通知"}`}
                      checked={item.enable === 1}
                      disabled={Boolean(togglingIds[item.id])}
                      onChange={(checked) =>
                        void toggleNotify(item.id, checked)
                      }
                    />
                    <IconButton
                      label="发送测试通知"
                      icon={<IconSendStroked aria-hidden="true" />}
                      disabled={Boolean(testingIds[item.id])}
                      onClick={() => void testNotify(item)}
                    />
                    <IconButton
                      label="编辑通知"
                      icon={<IconEditStroked aria-hidden="true" />}
                      onClick={() => setEditing(item)}
                    />
                    <IconButton
                      label="删除通知"
                      icon={<IconDeleteStroked aria-hidden="true" />}
                      danger
                      onClick={() =>
                        confirmDelete("删除此通知渠道？", () =>
                          action.run(async () => {
                            await api.deleteNotification(item.id);
                            await resource.refresh(true);
                          }),
                        )
                      }
                    />
                  </>
                }
              />
            );
          })}
        </div>
      </LoadState>
      {editing !== undefined && (
        <NotificationEditor
          item={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            void resource.refresh(true);
          }}
        />
      )}
    </div>
  );
}

function NotificationEditor({
  item,
  onClose,
  onSaved,
}: {
  item: NotifyItem | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { form, dirty, change, setForm, setDirty } = useEditorForm<NotifyForm>(() =>
    item ? notifyToForm(item) : defaultNotifyForm(),
  );
  const [templateOpen, setTemplateOpen] = useState(() =>
    Boolean(
      item &&
        (notifyToForm(item).titleTemplate || notifyToForm(item).contentTemplate),
    ),
  );
  const action = useAction();
  const submit = (test: boolean) => {
    const validation = validateNotifyForm(form);
    if (validation) {
      Toast.warning(validation);
      return;
    }
    void action.run(async () => {
      try {
        const data = {
          ...(item ? { id: item.id } : {}),
          method: form.method,
          enable: form.enable ? 1 : 0,
          params: buildNotifyParams(form),
        };
        if (test) {
          await api.testNotification(data);
          Toast.success("测试通知已发送");
        } else {
          await api.saveNotification(data, Boolean(item));
          setDirty(false);
          Toast.success("通知已保存");
          onSaved();
        }
      } catch (err) {
        errorToast(err);
      }
    });
  };
  const selectedEvents = form.events || [];
  const toggleEvent = (ev: string, checked: boolean) => {
    const next = checked
      ? selectedEvents.includes(ev)
        ? selectedEvents
        : [...selectedEvents, ev]
      : selectedEvents.filter((v) => v !== ev);
    change("events", next);
  };
  const insertTemplateToken = (token: string) => {
    change("contentTemplate", `${form.contentTemplate || ""}${token}`);
  };
  return (
    <Editor
      title={item ? "编辑通知" : "添加通知"}
      visible
      width={540}
      busy={action.busy}
      dirty={dirty}
      onClose={onClose}
      onSave={() => submit(false)}
      footer={({ close }) => (
        <div className="editor-actions">
          <Button
            type="tertiary"
            className="editor-actions-start"
            icon={<IconSendStroked aria-hidden="true" />}
            disabled={action.busy}
            onClick={() => submit(true)}
          >
            发送测试通知
          </Button>
          <Button type="tertiary" onClick={close} disabled={action.busy}>
            取消
          </Button>
          <Button
            type="primary"
            theme="solid"
            loading={action.busy}
            onClick={() => submit(false)}
          >
            保存
          </Button>
        </div>
      )}
    >
      <div className="editor-form">
        <Field label="通知渠道" required>
          <Select
            value={form.method >= 0 ? form.method : undefined}
            placeholder="请选择通知渠道"
            optionList={channelNames.map((label, value) => ({ label, value }))}
            onChange={(value) => {
              setDirty(true);
              const method = Number(value);
              setForm((current) => ({
                ...defaultNotifyForm(),
                method,
                enable: current.enable,
                notSendNull: current.notSendNull,
                eventMode: current.eventMode,
                events: current.events,
                titleTemplate: current.titleTemplate,
                contentTemplate: current.contentTemplate,
                ...(method === 5 ? { server: defaultBarkServer } : {}),
                ...(method === 9 ? { server: defaultNtfyServer } : {}),
                ...(method === 10 ? { priority: "5" } : {}),
              }));
            }}
          />
        </Field>

        {[0, 2, 4, 7].includes(form.method) && (
          <Field
            label="Webhook URL"
            required
            hint={
              form.method === 7
                ? "在企业微信群聊中“添加群机器人”获取的 HTTPS Webhook 地址"
                : undefined
            }
          >
            <Input
              value={form.url || ""}
              placeholder={
                form.method === 7
                  ? "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
                  : undefined
              }
              onChange={(v) => change("url", v)}
            />
          </Field>
        )}

        {form.method === 7 && (
          <Field label="消息格式">
            <Select
              value={form.msgtype || "text"}
              optionList={[
                { label: "纯文本 (text)", value: "text" },
                { label: "Markdown", value: "markdown" },
              ]}
              onChange={(v) => change("msgtype", String(v))}
            />
          </Field>
        )}

        {[2, 4].includes(form.method) && (
          <Field
            label={form.method === 2 ? "加签 Secret" : "签名校验 Secret"}
            hint={
              form.method === 2
                ? "机器人安全设置开启“加签”时填写，以 SEC 开头；留空则不签名"
                : "机器人安全设置开启“签名校验”时填写；留空则不签名"
            }
          >
            <Input
              mode="password"
              value={form.secret || ""}
              onChange={(v) => change("secret", v)}
            />
          </Field>
        )}

        {form.method === 0 && (
          <>
            <div className="form-grid form-grid--two">
              <Field label="HTTP 方法">
                <Select
                  value={form.httpMethod}
                  optionList={supportedWebhookMethods.map((value) => ({
                    label: value,
                    value,
                  }))}
                  onChange={(v) => change("httpMethod", String(v))}
                />
              </Field>
              <Field label="内容类型">
                <Select
                  value={form.contentType}
                  optionList={[
                    "application/json",
                    "application/x-www-form-urlencoded",
                  ].map((value) => ({ label: value, value }))}
                  onChange={(v) => change("contentType", String(v))}
                />
              </Field>
            </div>
            <div className="form-grid form-grid--two">
              <Field label="标题字段名">
                <Input
                  value={form.titleName || ""}
                  onChange={(v) => change("titleName", v)}
                />
              </Field>
              <Field label="正文字段名">
                <Input
                  value={form.contentName || ""}
                  onChange={(v) => change("contentName", v)}
                />
              </Field>
            </div>
            <SettingRow label="包含正文" variant="bordered">
              <Switch
                aria-label="包含正文"
                checked={form.needContent ?? true}
                onChange={(v) => change("needContent", v)}
              />
            </SettingRow>
            <Field label="请求头 JSON">
              <TextArea
                autosize={{ minRows: 3, maxRows: 6 }}
                value={form.headers || ""}
                onChange={(v) => change("headers", v)}
              />
            </Field>
            <Field label="请求体 JSON">
              <TextArea
                autosize={{ minRows: 4, maxRows: 10 }}
                value={form.body || ""}
                placeholder={'{"title":"{title}","content":"{content}"}'}
                onChange={(v) => change("body", v)}
              />
            </Field>
          </>
        )}

        {form.method === 1 && (
          <>
            <Field label="SendKey" required>
              <Input
                mode="password"
                value={form.sendKey || ""}
                onChange={(v) => change("sendKey", v)}
              />
            </Field>
            <Field label="API 版本">
              <Select
                value={form.version || "v3"}
                optionList={[
                  { label: "v3", value: "v3" },
                  { label: "v1", value: "v1" },
                ]}
                onChange={(v) => change("version", String(v))}
              />
            </Field>
          </>
        )}

        {form.method === 3 && (
          <>
            {(
              [
                { key: "corpid", label: "企业 ID", secret: false },
                { key: "corpsecret", label: "应用 Secret", secret: true },
                { key: "agentid", label: "AgentId", secret: false },
                { key: "touser", label: "接收成员", secret: false },
              ] as const
            ).map((field) => (
              <Field
                key={field.key}
                label={field.label}
                required={field.key !== "touser"}
              >
                <Input
                  mode={field.secret ? "password" : undefined}
                  value={form[field.key] || ""}
                  onChange={(v) => change(field.key, v)}
                />
              </Field>
            ))}
          </>
        )}

        {form.method === 5 && (
          <>
            <Field label="服务器地址" hint="使用自建 Bark 服务时修改">
              <Input
                value={form.server || ""}
                placeholder={defaultBarkServer}
                onChange={(v) => change("server", v)}
              />
            </Field>
            <Field label="Device Key" required>
              <Input
                mode="password"
                value={form.deviceKey || ""}
                onChange={(v) => change("deviceKey", v)}
              />
            </Field>
            <div className="form-grid form-grid--two">
              <Field label="分组">
                <Input
                  value={form.group || ""}
                  onChange={(v) => change("group", v)}
                />
              </Field>
              <Field label="提示音">
                <Input
                  value={form.sound || ""}
                  placeholder="默认"
                  onChange={(v) => change("sound", v)}
                />
              </Field>
            </div>
          </>
        )}

        {form.method === 6 && (
          <>
            <Field label="Bot Token" required hint="由 @BotFather 生成的机器人令牌">
              <Input
                mode="password"
                value={form.botToken || ""}
                placeholder="123456789:AAH..."
                onChange={(v) => change("botToken", v)}
              />
            </Field>
            <Field label="Chat ID" required hint="个人用户 ID 或群组/频道 ID（如 -100...）">
              <Input
                value={form.chatId || ""}
                placeholder="123456789"
                onChange={(v) => change("chatId", v)}
              />
            </Field>
            <div className="form-grid form-grid--two">
              <Field label="API 地址（可选）" hint="使用自建反代时填写">
                <Input
                  value={form.apiBase || ""}
                  placeholder={defaultTelegramApiBase}
                  onChange={(v) => change("apiBase", v)}
                />
              </Field>
              <Field label="解析模式（可选）">
                <Select
                  value={form.parseMode || ""}
                  optionList={[
                    { label: "纯文本（默认）", value: "" },
                    { label: "HTML", value: "HTML" },
                    { label: "Markdown", value: "Markdown" },
                  ]}
                  onChange={(v) => change("parseMode", String(v))}
                />
              </Field>
            </div>
          </>
        )}

        {form.method === 8 && (
          <>
            <div className="form-grid form-grid--two">
              <Field label="SMTP 服务器" required>
                <Input
                  value={form.host || ""}
                  placeholder="smtp.qq.com"
                  onChange={(v) => change("host", v)}
                />
              </Field>
              <Field label="端口" required>
                <Input
                  inputMode="numeric"
                  value={String(form.port ?? "587")}
                  placeholder="587"
                  onChange={(v) => change("port", v)}
                />
              </Field>
            </div>
            <div className="form-grid form-grid--two">
              <Field label="登录用户名">
                <Input
                  value={form.username || ""}
                  placeholder="user@example.com"
                  onChange={(v) => change("username", v)}
                />
              </Field>
              <Field label="授权码 / 密码">
                <Input
                  mode="password"
                  value={form.password || ""}
                  onChange={(v) => change("password", v)}
                />
              </Field>
            </div>
            <div className="form-grid form-grid--two">
              <Field label="发件人邮箱" required>
                <Input
                  value={form.from || ""}
                  placeholder="opensync@example.com"
                  onChange={(v) => change("from", v)}
                />
              </Field>
              <Field label="加密方式">
                <Select
                  value={form.tls || "starttls"}
                  optionList={[
                    { label: "STARTTLS (587)", value: "starttls" },
                    { label: "SSL / TLS (465)", value: "ssl" },
                    { label: "无加密 (25)", value: "none" },
                  ]}
                  onChange={(v) => change("tls", String(v))}
                />
              </Field>
            </div>
            <Field label="收件人邮箱" required hint="多个收件人请用英文逗号分隔">
              <Input
                value={form.to || ""}
                placeholder="admin@example.com, ops@example.com"
                onChange={(v) => change("to", v)}
              />
            </Field>
          </>
        )}

        {form.method === 9 && (
          <>
            <Field label="服务器地址" hint="支持自建 ntfy 服务">
              <Input
                value={form.server || ""}
                placeholder={defaultNtfyServer}
                onChange={(v) => change("server", v)}
              />
            </Field>
            <div className="form-grid form-grid--two">
              <Field label="Topic 主题" required>
                <Input
                  value={form.topic || ""}
                  placeholder="opensync-alerts"
                  onChange={(v) => change("topic", v)}
                />
              </Field>
              <Field label="优先级 (1–5)">
                <Select
                  value={String(form.priority || "3")}
                  optionList={[
                    { label: "1 · 最低", value: "1" },
                    { label: "2 · 低", value: "2" },
                    { label: "3 · 默认", value: "3" },
                    { label: "4 · 高", value: "4" },
                    { label: "5 · 紧急", value: "5" },
                  ]}
                  onChange={(v) => change("priority", String(v))}
                />
              </Field>
            </div>
            <div className="form-grid form-grid--two">
              <Field label="Access Token（可选）">
                <Input
                  mode="password"
                  value={form.token || ""}
                  placeholder="tk_..."
                  onChange={(v) => change("token", v)}
                />
              </Field>
              <Field label="标签（可选）" hint="逗号分隔，如 bell,nas">
                <Input
                  value={form.tags || ""}
                  placeholder="bell,floppy_disk"
                  onChange={(v) => change("tags", v)}
                />
              </Field>
            </div>
          </>
        )}

        {form.method === 10 && (
          <>
            <Field label="Gotify 服务器地址" required>
              <Input
                value={form.server || ""}
                placeholder="http://192.168.1.10:8080"
                onChange={(v) => change("server", v)}
              />
            </Field>
            <div className="form-grid form-grid--two">
              <Field label="App Token" required>
                <Input
                  mode="password"
                  value={form.token || ""}
                  onChange={(v) => change("token", v)}
                />
              </Field>
              <Field label="消息优先级 (0–10)">
                <Input
                  inputMode="numeric"
                  value={String(form.priority ?? "5")}
                  placeholder="5"
                  onChange={(v) => change("priority", v)}
                />
              </Field>
            </div>
          </>
        )}

        {form.method === 11 && (
          <>
            <Field label="PushPlus Token" required>
              <Input
                mode="password"
                value={form.token || ""}
                onChange={(v) => change("token", v)}
              />
            </Field>
            <div className="form-grid form-grid--two">
              <Field label="群组编码（可选）" hint="一对多推送时填写 Topic">
                <Input
                  value={form.topic || ""}
                  onChange={(v) => change("topic", v)}
                />
              </Field>
              <Field label="消息模板">
                <Select
                  value={form.template || "txt"}
                  optionList={[
                    { label: "纯文本 (txt)", value: "txt" },
                    { label: "Markdown", value: "markdown" },
                    { label: "HTML", value: "html" },
                  ]}
                  onChange={(v) => change("template", String(v))}
                />
              </Field>
            </div>
          </>
        )}

        <Field label="触发事件过滤">
          <Select
            value={form.eventMode || "all"}
            optionList={notifyEventModeOptions.map((o) => ({
              label: o.label,
              value: o.value,
            }))}
            onChange={(v) => change("eventMode", String(v))}
          />
        </Field>

        {form.eventMode === "custom" && (
          <div className="notify-event-grid" role="group" aria-label="选择通知事件">
            {notifyEventOptions.map((ev) => (
              <Checkbox
                key={ev.value}
                checked={selectedEvents.includes(ev.value)}
                onChange={(e) => toggleEvent(ev.value, Boolean(e.target.checked))}
              >
                {ev.label}
              </Checkbox>
            ))}
          </div>
        )}

        {(!form.eventMode || form.eventMode === "all") && (
          <SettingRow label="无变更时不发送" variant="bordered">
            <Switch
              aria-label="无变更时不发送"
              checked={Boolean(form.notSendNull)}
              onChange={(v) => change("notSendNull", v)}
            />
          </SettingRow>
        )}

        <div className="advanced-options">
          <button
            type="button"
            className={`advanced-options-toggle${templateOpen ? " is-open" : ""}`}
            aria-expanded={templateOpen}
            onClick={() => setTemplateOpen((open) => !open)}
          >
            <IconChevronRight
              className="advanced-options-chevron"
              aria-hidden="true"
            />
            <span>自定义标题 / 正文模板（可选）</span>
          </button>
          {templateOpen && (
            <div className="advanced-options-body">
              <Field
                label="标题模板"
                hint="留空则使用系统默认格式；支持变量占位符"
              >
                <Input
                  value={form.titleTemplate || ""}
                  placeholder="OpenSync [{status}] {jobName}"
                  onChange={(v) => change("titleTemplate", v)}
                />
              </Field>
              <Field label="正文模板">
                <TextArea
                  autosize={{ minRows: 3, maxRows: 8 }}
                  value={form.contentTemplate || ""}
                  placeholder="任务：{jobName}\n状态：{status}\n成功/总数：{successNum}/{allNum}\n耗时：{duration}"
                  onChange={(v) => change("contentTemplate", v)}
                />
              </Field>
              <div className="template-var-chips">
                {notifyTemplateVariables.map((item) => (
                  <button
                    key={item.token}
                    type="button"
                    className="schedule-chip"
                    title={`点击插入 ${item.token}`}
                    onClick={() => insertTemplateToken(item.token)}
                  >
                    {item.label} {item.token}
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </Editor>
  );
}
