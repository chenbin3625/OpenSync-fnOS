import { useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconPlusStroked,
  IconEditStroked,
  IconDeleteStroked,
  IconChainStroked,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  Editor,
  Field,
  Header,
  IconButton,
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
  const [testingId, setTestingId] = useState<number | null>(null);
  const testEngine = (id: number) => {
    setTestingId(id);
    api
      .testEngine(id)
      .then(() => Toast.success("引擎连接正常"))
      .catch(errorToast)
      .finally(() => setTestingId(null));
  };
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
            <div className="engine-item card-base" key={engine.id}>
              <div className="item-content">
                <h2>{engine.remark || engine.userName || `引擎 #${engine.id}`}</h2>
                <div className="mono muted">{engine.url}</div>
                <div className="item-meta">
                  账号 {engine.userName || "—"} · 创建于{" "}
                  {formatTimestamp(engine.createTime)}
                </div>
              </div>
              <div className="row-actions">
                <IconButton
                  label="测试引擎"
                  icon={<IconChainStroked aria-hidden="true" />}
                  disabled={testingId === engine.id}
                  onClick={() => testEngine(engine.id)}
                />
                <IconButton
                  label="编辑引擎"
                  icon={<IconEditStroked aria-hidden="true" />}
                  onClick={() => setEditing(engine)}
                />
                <IconButton
                  label="删除引擎"
                  icon={<IconDeleteStroked aria-hidden="true" />}
                  danger
                  onClick={() =>
                    confirmDelete(
                      "删除此存储引擎？",
                      async () => {
                        await api.deleteEngine(engine.id);
                        await engines.refresh();
                      },
                      "关联同步任务需先删除；引擎中的文件不会被删除。",
                    )
                  }
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
  const [dirty, setDirty] = useState(false);
  const action = useAction();
  const change = (key: keyof typeof form, value: string) => {
    setDirty(true);
    setForm((f) => ({ ...f, [key]: value }));
  };
  const save = () => {
    if (!form.remark.trim()) {
      Toast.warning("请输入引擎名称");
      return;
    }
    try {
      const url = new URL(form.url);
      if (!["http:", "https:"].includes(url.protocol)) throw new Error();
    } catch {
      Toast.warning("请输入有效的 HTTP / HTTPS 引擎地址");
      return;
    }
    if (!engine && !form.token.trim()) {
      Toast.warning("请输入引擎 API Token");
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
        errorToast(err);
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
        <Field label="引擎名称" required>
          <Input
            value={form.remark}
            onChange={(v) => change("remark", v)}
            maxLength={200}
            placeholder="我的 NAS 引擎"
          />
        </Field>
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
      </div>
    </Editor>
  );
}
