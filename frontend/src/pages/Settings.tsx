import { useEffect, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { IconSaveStroked } from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { Header, LoadState, SettingRow } from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import type { SystemSettings } from "../types";

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
    max: 20,
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
    key: "taskTimeout",
    label: "任务超时",
    min: 0,
    max: 8760,
    unit: "小时",
    tip: "单个任务运行超过该时长后自动标记为超时",
  },
  {
    key: "taskSave",
    label: "任务记录保留",
    min: 0,
    max: 3650,
    unit: "天",
    tip: "历史任务记录保留天数，0 表示不自动清理",
  },
] as const;
type SettingsForm = Record<(typeof fields)[number]["key"], string>;

const cardGroups = [
  {
    title: "任务执行",
    keys: [
      "copyConcurrency",
      "scanConcurrency",
      "maxRetries",
      "taskTimeout",
    ] as const,
  },
  {
    title: "历史记录",
    keys: ["taskSave"] as const,
  },
];

const fieldOf = (key: (typeof fields)[number]["key"]) =>
  fields.find((field) => field.key === key)!;

export default function Settings() {
  const resource = useResource((signal) => api.settings(signal));
  const [form, setForm] = useState<SettingsForm | null>(null);
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
        Toast.error(`${field.label}请输入 ${field.min}–${field.max} 的整数`);
        return;
      }
      values[field.key] = value;
    }
    void action.run(async () => {
      try {
        await api.saveSettings(values);
        Toast.success("配置已保存");
        await resource.refresh();
      } catch (err) {
        Toast.error(err instanceof Error ? err.message : "保存失败");
      }
    });
  };
  const input = (field: (typeof fields)[number]) => (
    <SettingRow
      key={field.key}
      label={field.label}
      variant="compact"
      tip={field.tip}
    >
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
    </SettingRow>
  );
  // 每个分组渲染成一张大卡片，条目靠间距区分，不用图标和小卡片分组。
  const card = (
    title: string,
    keys: readonly (typeof fields)[number]["key"][],
  ) => (
    <section className="settings-card card-base" key={title}>
      <div className="settings-card-head">
        <h2>{title}</h2>
      </div>
      <div className="settings-card-body">
        {keys.map((key) => (
          <div key={key}>{input(fieldOf(key))}</div>
        ))}
      </div>
    </section>
  );
  return (
    <div className="page settings-page">
      <Header
        title="设置"
        actions={
          <Button
            theme="solid"
            icon={<IconSaveStroked aria-hidden="true" />}
            loading={action.busy}
            disabled={!dirty || resource.loading || Boolean(resource.error)}
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
      />
      {form && !resource.loading && !resource.error && (
        <>
          {cardGroups.map((group) =>
            card(group.title, group.keys),
          )}
        </>
      )}
    </div>
  );
}
