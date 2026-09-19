import { useState } from "react";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconPlusStroked,
  IconEditStroked,
  IconDeleteStroked,
  IconCopyStroked,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  ActionMenu,
  Editor,
  Field,
  Header,
  LoadState,
  confirmDelete,
  errorToast,
} from "../components/common";
import { useAction, useResource } from "../lib/hooks";
import { formatTimestamp } from "../utils/date";
import type { AlistItem } from "../types";

export default function Engines() {
  const engines = useResource((signal) => api.engines(signal));
  const [editing, setEditing] = useState<AlistItem | null | undefined>();
  return (
    <div className="page">
      <Header
        title="引擎管理"
        actions={
          <Button
            theme="solid"
            icon={<IconPlusStroked aria-hidden="true" />}
            onClick={() => setEditing(null)}
          >
            添加引擎
          </Button>
        }
      />
      <LoadState
        loading={engines.loading}
        error={engines.error}
        retry={engines.refresh}
        empty={!engines.data?.length}
      />
      <div className="item-list">
        {!engines.loading &&
          !engines.error &&
          engines.data?.map((engine) => (
            <div className="engine-item" key={engine.id}>
              <div className="item-content">
                <h2>{engine.remark || engine.userName || `引擎 #${engine.id}`}</h2>
                <div className="mono muted">{engine.url}</div>
                <div className="item-meta">
                  账号 {engine.userName || "—"} · 创建于{" "}
                  {formatTimestamp(engine.createTime)}
                </div>
              </div>
              <div className="row-actions">
                <ActionMenu
                  actions={[
                    {
                      label: "复制引擎地址",
                      icon: <IconCopyStroked aria-hidden="true" />,
                      onClick: () => {
                        void navigator.clipboard
                          .writeText(engine.url)
                          .then(() => Toast.success("地址已复制"))
                          .catch(errorToast);
                      },
                    },
                    {
                      label: "编辑引擎",
                      icon: <IconEditStroked aria-hidden="true" />,
                      onClick: () => setEditing(engine),
                    },
                    {
                      label: "删除引擎",
                      icon: <IconDeleteStroked aria-hidden="true" />,
                      danger: true,
                      onClick: () =>
                        confirmDelete(
                          "删除此存储引擎？",
                          async () => {
                            await api.deleteEngine(engine.id);
                            await engines.refresh();
                          },
                          "关联同步任务需先删除；引擎中的文件不会被删除。",
                        ),
                    },
                  ]}
                />
              </div>
            </div>
          ))}
      </div>
      {editing !== undefined && (
        <EngineEditor
          engine={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            void engines.refresh();
          }}
        />
      )}
    </div>
  );
}
function EngineEditor({
  engine,
  onClose,
  onSaved,
}: {
  engine: AlistItem | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState({
    url: engine?.url || "",
    remark: engine?.remark || "",
    token: "",
  });
  const [dirty, setDirty] = useState(false),
    [error, setError] = useState("");
  const action = useAction();
  const change = (key: keyof typeof form, value: string) => {
    setDirty(true);
    setForm((f) => ({ ...f, [key]: value }));
  };
  const save = () => {
    try {
      const url = new URL(form.url);
      if (!["http:", "https:"].includes(url.protocol)) throw new Error();
    } catch {
      setError("请输入有效的 HTTP / HTTPS 引擎地址");
      return;
    }
    if (!engine && !form.token.trim()) {
      setError("请输入引擎 API Token");
      return;
    }
    void action.run(async () => {
      try {
        await api.saveEngine(
          { ...form, ...(engine ? { id: engine.id } : {}) },
          Boolean(engine),
        );
        Toast.success("引擎连接已保存");
        setDirty(false);
        onSaved();
      } catch (err) {
        setError(err instanceof Error ? err.message : "连接失败");
      }
    });
  };
  return (
    <Editor
      title={engine ? "编辑引擎" : "添加引擎"}
      visible
      busy={action.busy}
      dirty={dirty}
      onClose={onClose}
      onSave={save}
    >
      <div className="editor-form">
        {error && <Banner type="danger" description={error} closeIcon={null} />}
        <Field label="引擎地址" required>
          <Input
            value={form.url}
            onChange={(v) => change("url", v)}
            placeholder="http://192.168.1.10:5244"
          />
        </Field>
        <Field label="API Token" required={!engine}>
          <Input
            mode="password"
            value={form.token}
            onChange={(v) => change("token", v)}
            placeholder={engine ? "留空保留原 Token" : "OpenList / AList Token"}
          />
        </Field>
        <Field label="名称">
          <Input
            value={form.remark}
            onChange={(v) => change("remark", v)}
            maxLength={200}
          />
        </Field>
      </div>
    </Editor>
  );
}
