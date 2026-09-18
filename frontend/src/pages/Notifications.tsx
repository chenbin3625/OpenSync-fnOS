import { useState } from "react";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
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
  ActionMenu,
  Editor,
  Field,
  Header,
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
  validateNotifyForm,
  type NotifyForm,
} from "../lib/notifyForm";
import type { NotifyItem } from "../types";

export default function Notifications() {
  const resource = useResource((signal) => api.notifications(signal));
  const [editing, setEditing] = useState<NotifyItem | null | undefined>();
  const action = useAction();
  const perform = (operation: () => Promise<unknown>, text: string) =>
    void action.run(async () => {
      try {
        await operation();
        Toast.success(text);
        await resource.refresh();
      } catch (error) {
        errorToast(error);
      }
    });
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
            <div className="notification-item" key={item.id}>
              <div className="item-content">
                <h2>
                  {channelNames[item.method]}{" "}
                  <Tag size="small" color={item.enable ? "green" : "grey"}>
                    {item.enable ? "已启用" : "已关闭"}
                  </Tag>
                </h2>
                <div className="item-meta">
                  通知 #{item.id} · 创建于 {formatTimestamp(item.createTime)}
                </div>
              </div>
              <div className="row-actions">
                <Switch
                  aria-label={`通知 ${item.id} 开关`}
                  checked={item.enable === 1}
                  disabled={action.busy}
                  onChange={(checked) =>
                    perform(
                      () => api.toggleNotification(item.id, checked ? 1 : 0),
                      "通知状态已更新",
                    )
                  }
                />
                <ActionMenu
                  disabled={action.busy}
                  actions={[
                    {
                      label: "发送测试通知",
                      icon: <IconSendStroked aria-hidden="true" />,
                      onClick: () =>
                        perform(
                          () =>
                            api.testNotification({
                              id: item.id,
                              method: item.method,
                              enable: item.enable,
                              params: buildNotifyParams(notifyToForm(item)),
                            }),
                          "测试通知已发送",
                        ),
                    },
                    {
                      label: "编辑通知",
                      icon: <IconEditStroked aria-hidden="true" />,
                      onClick: () => setEditing(item),
                    },
                    {
                      label: "删除通知",
                      icon: <IconDeleteStroked aria-hidden="true" />,
                      danger: true,
                      onClick: () =>
                        confirmDelete("删除此通知渠道？", async () => {
                          await api.deleteNotification(item.id);
                          await resource.refresh();
                        }),
                    },
                  ]}
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
  const [dirty, setDirty] = useState(false),
    [error, setError] = useState("");
  const action = useAction();
  const change = <K extends keyof NotifyForm>(key: K, value: NotifyForm[K]) => {
    setDirty(true);
    setForm((f) => ({ ...f, [key]: value }));
  };
  const submit = (test: boolean) => {
    const validation = validateNotifyForm(form);
    setError(validation);
    if (validation) return;
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
        setError(err instanceof Error ? err.message : "操作失败");
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
    >
      <div className="editor-form">
        {error && <Banner type="danger" description={error} closeIcon={null} />}
        <Field label="通知渠道" required>
          <Select
            value={form.method}
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
                  optionList={["POST", "GET", "PUT", "PATCH", "DELETE"].map(
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
            <SettingRow label="包含正文">
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
        <SettingRow label="启用通知">
          <Switch
            aria-label="启用通知"
            checked={form.enable}
            onChange={(v) => change("enable", v)}
          />
        </SettingRow>
        <SettingRow label="无变更时不发送">
          <Switch
            aria-label="无变更时不发送"
            checked={Boolean(form.notSendNull)}
            onChange={(v) => change("notSendNull", v)}
          />
        </SettingRow>
          <Button
            icon={<IconSendStroked aria-hidden="true" />}
          disabled={action.busy}
          onClick={() => submit(true)}
        >
          发送测试通知
        </Button>
      </div>
    </Editor>
  );
}
