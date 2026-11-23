import { useEffect, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Switch from "@douyinfe/semi-ui/lib/es/switch";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import { IconSaveStroked } from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { VersionLink } from "../components/VersionLink";
import {
  confirmAction,
  errorToast,
  Header,
  LoadState,
  SectionCard,
  SettingRow,
} from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import { getHost } from "../lib/host";
import { normalizeSettingFieldValue } from "./Home/homeUtils";
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
    // Backend config.MaxScanConcurrency is 50 (config.go); a smaller UI cap
    // rejected values the server accepts.
    max: 50,
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
    key: "scheduledFullSyncConfirm",
    label: "定时全量删除确认",
    min: 0,
    max: 1,
    unit: "",
    kind: "switch",
    tip: "开启后，定时全量同步在删除目标端文件前暂停并等待确认；手动执行在删除或移动前始终会确认",
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
      "scheduledFullSyncConfirm",
    ] as const,
  },
  {
    title: "历史记录",
    keys: ["taskSave"] as const,
  },
];

const fieldOf = (key: (typeof fields)[number]["key"]) =>
  fields.find((field) => field.key === key)!;

function formatSettingFieldValue(
  field: (typeof fields)[number],
  value: unknown,
): string {
  return normalizeSettingFieldValue(
    value,
    "kind" in field && field.kind === "switch",
  );
}

export function toSettingsForm(
  data: Partial<SystemSettings>,
): SettingsForm {
  return Object.fromEntries(
    fields.map((field) => [
      field.key,
      formatSettingFieldValue(field, data[field.key]),
    ]),
  ) as SettingsForm;
}

export function isSettingsDirty(
  form: SettingsForm | null,
  data: Partial<SystemSettings> | null | undefined,
): boolean {
  if (!form || !data) return false;
  const baseline = toSettingsForm(data);
  return fields.some(({ key }) => form[key] !== baseline[key]);
}

export default function Settings() {
  const resource = useResource((signal) => api.settings(signal));
  const [form, setForm] = useState<SettingsForm | null>(null);
  const action = useAction();
  useEffect(() => {
    if (resource.data) setForm(toSettingsForm(resource.data));
  }, [resource.data]);
  const dirty = isSettingsDirty(form, resource.data);
  const skipNextGuardRef = useRef(false);
  useEffect(() => {
    if (!dirty) return;
    const host = getHost();
    if (!host.isStandaloneWeb) {
      void host
        .setExitPageTips({ title: "尚未保存", content: "是否放弃当前修改？" })
        .catch(() => {});
    }
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    const onDocumentClick = (event: MouseEvent) => {
      if (skipNextGuardRef.current) {
        skipNextGuardRef.current = false;
        return;
      }
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      ) {
        return;
      }
      const target = event.target as Element | null;
      if (!target) return;
      const anchor = target.closest("a[href]") as HTMLAnchorElement | null;
      const menuToggle = target.closest("button.nav-menu-toggle") as HTMLButtonElement | null;
      let navElement: HTMLElement | null = null;
      if (anchor) {
        if (anchor.target && anchor.target !== "_self") return;
        if (anchor.hasAttribute("download")) return;
        try {
          const dest = new URL(anchor.href, window.location.href);
          if (dest.origin !== window.location.origin) return;
          if (
            dest.pathname + dest.search ===
            window.location.pathname + window.location.search
          ) {
            return;
          }
          navElement = anchor;
        } catch {
          return;
        }
      } else if (menuToggle) {
        navElement = menuToggle;
      }
      if (!navElement) return;
      event.preventDefault();
      event.stopPropagation();
      const targetElement = navElement;
      confirmAction({
        title: "放弃未保存的修改？",
        content: "当前设置尚未保存，离开后修改将丢失。",
        okText: "放弃",
        onOk: () => {
          skipNextGuardRef.current = true;
          targetElement.click();
          skipNextGuardRef.current = false;
        },
      });
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    document.addEventListener("click", onDocumentClick, true);
    return () => {
      window.removeEventListener("beforeunload", onBeforeUnload);
      document.removeEventListener("click", onDocumentClick, true);
      if (!host.isStandaloneWeb) void host.setExitPageTips().catch(() => {});
    };
  }, [dirty]);
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
        Toast.warning(`${field.label}请输入 ${field.min}–${field.max} 的整数`);
        return;
      }
      values[field.key] = value;
    }
    void action.run(async () => {
      try {
        await api.saveSettings(values);
        Toast.success("配置已保存");
        // Silent reload: a full loading state would replace the whole form
        // with a spinner right after a successful save.
        await resource.refresh(true);
      } catch (err) {
        errorToast(err, "保存失败");
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
      {"kind" in field && field.kind === "switch" ? (
        <Switch
          aria-label={field.label}
          checked={form![field.key] === "1"}
          disabled={action.busy}
          onChange={(checked) => {
            setForm((f) => f && { ...f, [field.key]: checked ? "1" : "0" });
          }}
        />
      ) : (
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
      )}
    </SettingRow>
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
      >
        {form &&
          cardGroups.map((group) => (
            <SectionCard
              key={group.title}
              className="settings-card"
              headClassName="settings-card-head"
              bodyClassName="settings-card-body"
              title={group.title}
            >
              {group.keys.map((key) => (
                <div key={key}>{input(fieldOf(key))}</div>
              ))}
            </SectionCard>
          ))}
      </LoadState>
      <div className="mobile-version">
        <VersionLink />
      </div>
    </div>
  );
}
