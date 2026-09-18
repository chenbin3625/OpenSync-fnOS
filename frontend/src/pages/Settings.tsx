import { useEffect, useState } from "react";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { IconSaveStroked } from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { Header, LoadState, SettingRow } from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import type { SystemSettings } from "../types";

const fields = [
  { key: "copyConcurrency", label: "复制并发数", min: 1, max: 100, unit: "" },
  { key: "scanConcurrency", label: "扫描并发数", min: 1, max: 20, unit: "" },
  { key: "maxRetries", label: "失败重试次数", min: 0, max: 10, unit: "次" },
  { key: "taskTimeout", label: "任务超时", min: 0, max: 8760, unit: "小时" },
  { key: "taskSave", label: "任务记录保留", min: 0, max: 3650, unit: "天" },
] as const;
type SettingsForm = Record<(typeof fields)[number]["key"], string>;

const cardGroups = [
  {
    title: "任务执行",
    note: "并发数越大占用的系统资源越多，请按硬件配置调整。",
    keys: [
      "copyConcurrency",
      "scanConcurrency",
      "maxRetries",
      "taskTimeout",
    ] as const,
  },
  {
    title: "历史记录",
    note: "超时或保留时间为 0 时，不设置相应限制。",
    keys: ["taskSave"] as const,
  },
];

const fieldOf = (key: (typeof fields)[number]["key"]) =>
  fields.find((field) => field.key === key)!;

export default function Settings() {
  const resource = useResource((signal) => api.settings(signal));
  const [form, setForm] = useState<SettingsForm | null>(null),
    [error, setError] = useState("");
  const action = useAction();
  useEffect(() => {
    if (resource.data)
      setForm(
        Object.fromEntries(
          fields.map(({ key }) => [key, String(resource.data![key])]),
        ) as SettingsForm,
      );
  }, [resource.data]);
  const dirty = Boolean(
    form &&
      resource.data &&
      fields.some(({ key }) => form[key] !== String(resource.data![key])),
  );
  const save = () => {
    if (!form || !resource.data) return;
    const values: SystemSettings = { ...resource.data };
    for (const field of fields) {
      const input = form[field.key].trim();
      const value = Number(input);
      if (
        !/^\d+$/.test(input) ||
        !Number.isSafeInteger(value) ||
        value < field.min ||
        value > field.max
      ) {
        setError(`${field.label}请输入 ${field.min}–${field.max} 的整数`);
        return;
      }
      values[field.key] = value;
    }
    void action.run(async () => {
      try {
        setError("");
        await api.saveSettings(values);
        Toast.success("配置已保存");
        await resource.refresh();
      } catch (err) {
        setError(err instanceof Error ? err.message : "保存失败");
      }
    });
  };
  const input = (field: (typeof fields)[number]) => (
    <SettingRow key={field.key} label={field.label}>
      <div className="numeric-setting">
        <Input
          aria-label={field.label}
          inputMode="numeric"
          value={form![field.key]}
          disabled={action.busy}
          onChange={(value) => {
            setError("");
            setForm((f) => f && { ...f, [field.key]: value });
          }}
        />
        <span>{field.unit}</span>
      </div>
    </SettingRow>
  );
  // 每个分组渲染成一张大卡片，条目靠间距区分，不用图标和小卡片分组。
  const card = (
    title: string,
    note: string,
    keys: readonly (typeof fields)[number]["key"][],
  ) => (
    <section className="settings-card" key={title}>
      <div className="settings-card-head">
        <h2>{title}</h2>
      </div>
      <div className="settings-card-body">{keys.map((key) => input(fieldOf(key)))}</div>
      <div className="muted settings-card-note">{note}</div>
    </section>
  );
  return (
    <div className="page settings-page">
      <Header
        title="系统设置"
        actions={
          <Button
            theme="solid"
            icon={<IconSaveStroked aria-hidden="true" />}
            loading={action.busy}
            disabled={!dirty || resource.loading || Boolean(resource.error)}
            onClick={save}
          >
            保存设置
          </Button>
        }
      />
      <LoadState
        loading={resource.loading}
        error={resource.error}
        retry={resource.refresh}
      />
      {error && <Banner type="danger" description={error} closeIcon={null} />}
      {form && !resource.loading && !resource.error && (
        <>
          {cardGroups.map((group) =>
            card(group.title, group.note, group.keys),
          )}
        </>
      )}
    </div>
  );
}
