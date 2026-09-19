import { useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import TextArea from "@douyinfe/semi-ui/lib/es/input/textarea";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconPlusStroked,
  IconEditStroked,
  IconDeleteStroked,
  IconSendStroked,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  Editor,
  Field,
  Header,
  IconButton,
  LoadState,
  SettingRow,
  confirmDelete,
  errorToast,
} from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import { formatTimestamp } from "../utils/date";
import {
  buildNotifyParams,
  channelNames,
  defaultNotifyForm,
  notifyToForm,
  supportedWebhookMethods,
  validateNotifyForm,
  type NotifyForm,
} from "../lib/notifyForm";
import type { NotifyItem } from "../types";

export default function Notifications() {
  const resource = useResource((signal) => api.notifications(signal));
  const [editing, setEditing] = useState<NotifyItem | null | undefined>();
  const [testingId, setTestingId] = useState<number | null>(null);
  const [togglingId, setTogglingId] = useState<number | null>(null);
  const testNotify = async (item: NotifyItem) => {
    setTestingId(item.id);
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
      setTestingId(null);
    }
  };
  const toggleNotify = async (id: number, checked: boolean) => {
    setTogglingId(id);
    try {
      await api.toggleNotification(id, checked ? 1 : 0);
      Toast.success("通知状态已更新");
      await resource.refresh();
    } catch (error) {
      errorToast(error);
    } finally {
      setTogglingId(null);
    }
  };
  return (
    <div className="page">
      <Header
        title="通知配置"
        actions={
          <>
            <Button
              theme="solid"
              icon={<IconPlusStroked aria-hidden="true" />}
              onClick={() => setEditing(null)}
            >
              添加通知
            </Button>
          </>
        }
      />
      <LoadState
        loading={resource.loading}
        error={resource.error}
        retry={resource.refresh}
        empty={!resource.data?.length}
      />
      <div className="item-list">
        {!resource.loading &&
          !resource.error &&
          resource.data?.map((item) => (
            <div className="notification-item card-base" key={item.id}>
              <div className="item-content">
                <h2>
                  {channelNames[item.method]}
                </h2>
                <div className="item-meta">
                  通知 #{item.id} · 创建于 {formatTimestamp(item.createTime)}
                </div>
              </div>
              <div className="row-actions">
                <Switch
                  aria-label={`通知 ${item.id} 开关`}
                  checked={item.enable === 1}
                  disabled={togglingId === item.id}
                  onChange={(checked) =>
                    void toggleNotify(item.id, checked)
                  }
                />
                <IconButton
                  label="发送测试通知"
                  icon={<IconSendStroked aria-hidden="true" />}
                  disabled={testingId === item.id}
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
                    confirmDelete("删除此通知渠道？", async () => {
                      await api.deleteNotification(item.id);
                      await resource.refresh();
                    })
                  }
                />
              </div>
            </div>
          ))}
      </div>
      {editing !== undefined && (
        <NotificationEditor
          item={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            void resource.refresh();
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
  const [form, setForm] = useState<NotifyForm>(() =>
    item ? notifyToForm(item) : defaultNotifyForm(),
  );
  const [dirty, setDirty] = useState(false);
  const action = useAction();
  const change = <K extends keyof NotifyForm>(key: K, value: NotifyForm[K]) => {
    setDirty(true);
    setForm((f) => ({ ...f, [key]: value }));
  };
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
  return (
    <Editor
      title={item ? "编辑通知" : "添加通知"}
      visible
      busy={action.busy}
      dirty={dirty}
      onClose={onClose}
      onSave={() => submit(false)}
      footer={({ close }) => (
        <div className="editor-actions task-editor-actions">
          <Button
            icon={<IconSendStroked aria-hidden="true" />}
            disabled={action.busy}
            onClick={() => submit(true)}
          >
            发送测试通知
          </Button>
          <Button onClick={close} disabled={action.busy}>
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
              setForm({ ...defaultNotifyForm(), method: Number(value) });
            }}
          />
        </Field>
        {[0, 2, 4].includes(form.method) && (
          <Field label="Webhook URL" required>
            <Input value={form.url || ""} onChange={(v) => change("url", v)} />
          </Field>
        )}
        {form.method === 0 && (
          <>
            <div className="form-grid">
              <Field label="HTTP 方法">
                <Select
                  value={form.httpMethod}
                  optionList={supportedWebhookMethods.map(
                    (value) => ({ label: value, value }),
                  )}
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
            <div className="form-grid">
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
        <SettingRow label="启用通知" variant="bordered">
          <Switch
            aria-label="启用通知"
            checked={form.enable}
            onChange={(v) => change("enable", v)}
          />
        </SettingRow>
        <SettingRow label="无变更时不发送" variant="bordered">
          <Switch
            aria-label="无变更时不发送"
            checked={Boolean(form.notSendNull)}
            onChange={(v) => change("notSendNull", v)}
          />
        </SettingRow>
      </div>
    </Editor>
  );
}
