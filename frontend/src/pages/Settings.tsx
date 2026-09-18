import { useEffect, useState } from "react";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import InputNumber from "@douyinfe/semi-ui/lib/es/inputNumber";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { IconSave } from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { Header, LoadState, SettingRow } from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import type { SystemSettings } from "../types";

export default function Settings() {
  const resource = useResource((signal) => api.settings(signal));
  const [form, setForm] = useState<SystemSettings | null>(null),
    [error, setError] = useState("");
  const action = useAction();
  useEffect(() => {
    if (resource.data) setForm(resource.data);
  }, [resource.data]);
  const dirty = Boolean(
    form &&
      resource.data &&
      JSON.stringify(form) !== JSON.stringify(resource.data),
  );
  return (
    <div className="page settings-page">
      <Header title="系统设置" />
      <LoadState
        loading={resource.loading}
        error={resource.error}
        retry={resource.refresh}
      />
      {error && <Banner type="danger" description={error} closeIcon={null} />}
      {form && !resource.loading && !resource.error && (
        <>
          <section className="settings-section">
            <h2>任务执行</h2>
            {(
              [
                {
                  key: "copyConcurrency",
                  label: "复制并发数",
                  min: 1,
                  max: 100,
                  unit: "",
                },
                {
                  key: "scanConcurrency",
                  label: "扫描并发数",
                  min: 1,
                  max: 20,
                  unit: "",
                },
                {
                  key: "maxRetries",
                  label: "失败重试次数",
                  min: 0,
                  max: 10,
                  unit: "次",
                },
                {
                  key: "taskTimeout",
                  label: "任务超时",
                  min: 0,
                  max: 8760,
                  unit: "小时",
                },
              ] as const
            ).map((field) => (
              <SettingRow key={field.key} label={field.label}>
                <div className="numeric-setting">
                  <InputNumber
                    aria-label={field.label}
                    value={form[field.key]}
                    min={field.min}
                    max={field.max}
                    precision={0}
                    disabled={action.busy}
                    onChange={(value) => {
                      if (typeof value === "number")
                        setForm((f) => f && { ...f, [field.key]: value });
                    }}
                  />
                  <span>{field.unit}</span>
                </div>
              </SettingRow>
            ))}
          </section>
          <section className="settings-section">
            <h2>历史记录</h2>
            <SettingRow label="任务记录保留">
              <div className="numeric-setting">
                <InputNumber
                  aria-label="任务记录保留"
                  value={form.taskSave}
                  min={0}
                  max={3650}
                  precision={0}
                  disabled={action.busy}
                  onChange={(value) => {
                    if (typeof value === "number")
                      setForm((f) => f && { ...f, taskSave: value });
                  }}
                />
                <span>天</span>
              </div>
            </SettingRow>
            <div className="muted setting-note">
              超时或保留时间为 0 时，不设置相应限制。
            </div>
          </section>
          <div className="settings-actions">
            <Button
              disabled={!dirty || action.busy}
              onClick={() => setForm(resource.data)}
            >
              还原
            </Button>
            <Button
              theme="solid"
              icon={<IconSave aria-hidden="true" />}
              loading={action.busy}
              disabled={!dirty}
              onClick={() =>
                void action.run(async () => {
                  try {
                    setError("");
                    await api.saveSettings(form);
                    Toast.success("配置已保存");
                    await resource.refresh();
                  } catch (err) {
                    setError(err instanceof Error ? err.message : "保存失败");
                  }
                })
              }
            >
              保存设置
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
