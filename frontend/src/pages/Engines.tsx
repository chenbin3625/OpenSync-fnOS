import { useContext, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Select from "@douyinfe/semi-ui/lib/es/select";
import Tabs from "@douyinfe/semi-ui/lib/es/tabs";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconPlusStroked,
  IconEditStroked,
  IconDeleteStroked,
  IconCopyStroked,
  IconFolderStroked,
} from "@douyinfe/semi-icons";
import { api, type LocalMapping } from "../api/client";
import { SessionContext } from "../App";
import {
  Editor,
  Field,
  Header,
  IconButton,
  LoadState,
  confirmDelete,
  errorToast,
} from "../components/common";
import { RemotePaths } from "../components/RemotePaths";
import { useAction, useResource } from "../lib/hooks";
import { authorizeDirectory, getHost } from "../lib/host";
import type { AlistItem } from "../types";

export default function Engines() {
  const [params, setParams] = useSearchParams();
  const local = params.get("view") === "local";
  const engines = useResource((signal) => api.engines(signal));
  const [editing, setEditing] = useState<AlistItem | null | undefined>();
  const tabs = (
    <Tabs
      activeKey={local ? "local" : "engine"}
      onChange={(view) => setParams(view === "local" ? { view } : {})}
    >
      <Tabs.TabPane tab="OpenList / AList" itemKey="engine" />
      <Tabs.TabPane tab="本地存储" itemKey="local" />
    </Tabs>
  );
  return (
    <div className="page">
      {!local && (
        <Header
          title="引擎管理"
          tabs={tabs}
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
      )}
      {local ? (
        <LocalStorage engines={engines.data || []} tabs={tabs} />
      ) : (
        <>
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
                  <div className="item-symbol">
                    <IconFolderStroked aria-hidden="true" />
                  </div>
                  <div className="item-content">
                    <h2>
                      {engine.remark || engine.userName || `引擎 #${engine.id}`}
                    </h2>
                    <div className="mono muted">{engine.url}</div>
                    <div className="muted">{engine.userName}</div>
                  </div>
                  <div className="row-actions">
                    <IconButton
                      aria-hidden="true"
                      label="复制引擎地址"
                      icon={<IconCopyStroked aria-hidden="true" />}
                      onClick={() => {
                        void navigator.clipboard
                          .writeText(engine.url)
                          .then(() => Toast.success("地址已复制"))
                          .catch(errorToast);
                      }}
                    />
                    <IconButton
                      aria-hidden="true"
                      label="编辑引擎"
                      icon={<IconEditStroked aria-hidden="true" />}
                      onClick={() => setEditing(engine)}
                    />
                    <IconButton
                      aria-hidden="true"
                      label="删除引擎"
                      danger
                      icon={<IconDeleteStroked aria-hidden="true" />}
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
        </>
      )}
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
function LocalStorage({
  engines,
  tabs,
}: {
  engines: AlistItem[];
  tabs: ReactNode;
}) {
  const { development } = useContext(SessionContext);
  const resource = useResource((signal) => api.storage(signal));
  const [editing, setEditing] = useState<LocalMapping | null | undefined>();
  const action = useAction();
  return (
    <div className="local-storage">
      <Header
        title="本地存储"
        tabs={tabs}
        actions={
          <Button
            icon={<IconFolderStroked aria-hidden="true" />}
            disabled={development || action.busy}
            onClick={() =>
              void action.run(async () => {
                try {
                  await authorizeDirectory();
                  await resource.refresh();
                } catch (error) {
                  errorToast(error);
                }
              })
            }
          >
            授权目录
          </Button>
        }
      />
      <LoadState
        loading={resource.loading}
        error={resource.error}
        retry={resource.refresh}
      />
      {resource.data && !resource.data.available && (
        <Banner
          type="info"
          description={resource.data.reason || "本地目录授权仅在飞牛环境中可用"}
          closeIcon={null}
        />
      )}
      <div className="authorized-paths">
        {resource.data?.paths.map((path) => (
          <div key={path} className="authorized-path">
            <IconFolderStroked aria-hidden="true" />
            <span className="mono">{path}</span>
            <IconButton
              aria-hidden="true"
              label="在文件管理器中打开"
              icon={<IconFolderStroked aria-hidden="true" />}
              onClick={() => {
                void getHost().openFileManager(path).catch(errorToast);
              }}
            />
          </div>
        ))}
      </div>
      <div className="section-heading">
        <h2>引擎路径映射</h2>
        <Button
          icon={<IconPlusStroked aria-hidden="true" />}
          disabled={!resource.data?.paths.length || !engines.length}
          onClick={() => setEditing(null)}
        >
          添加映射
        </Button>
      </div>
      <Banner
        type="info"
        description="目录授权仅授予 OpenSync；请同时在 OpenList / AList 中挂载同一目录。同步任务仍使用引擎路径。"
        closeIcon={null}
      />
      <div className="item-list">
        {resource.data?.mappings.map((mapping) => (
          <div className="mapping-item" key={mapping.path}>
            <div className="item-content">
              <h3>{mapping.remark || mapping.path}</h3>
              <div className="mono">{mapping.path}</div>
              <div className="muted mono">
                {engines.find((e) => e.id === mapping.alistId)?.remark ||
                  `引擎 #${mapping.alistId}`}{" "}
                → {mapping.virtualPath}
              </div>
            </div>
            <div className="row-actions">
              <IconButton
                aria-hidden="true"
                label="编辑映射"
                icon={<IconEditStroked aria-hidden="true" />}
                onClick={() => setEditing(mapping)}
              />
              <IconButton
                aria-hidden="true"
                label="移除映射"
                icon={<IconDeleteStroked aria-hidden="true" />}
                danger
                onClick={() =>
                  confirmDelete(
                    "移除此映射？",
                    async () => {
                      await api.deleteMapping(mapping.path);
                      await resource.refresh();
                    },
                    "只移除映射，不删除文件、不撤销系统授权、不改变同步任务。",
                  )
                }
              />
            </div>
          </div>
        ))}
      </div>
      {editing !== undefined && (
        <MappingEditor
          mapping={editing}
          paths={resource.data?.paths || []}
          engines={engines}
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
function MappingEditor({
  mapping,
  paths,
  engines,
  onClose,
  onSaved,
}: {
  mapping: LocalMapping | null;
  paths: string[];
  engines: AlistItem[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState<LocalMapping>(
    mapping || {
      path: paths[0] || "",
      alistId: engines[0]?.id || 0,
      virtualPath: "",
      remark: "",
    },
  );
  const [dirty, setDirty] = useState(false),
    [error, setError] = useState("");
  const action = useAction();
  const change = (values: Partial<LocalMapping>) => {
    setDirty(true);
    setForm((f) => ({ ...f, ...values }));
  };
  return (
    <Editor
      title={mapping ? "编辑路径映射" : "添加路径映射"}
      visible
      busy={action.busy}
      dirty={dirty}
      onClose={onClose}
      onSave={() => {
        if (!form.path || !form.virtualPath || !form.alistId) {
          setError("请选择授权目录、引擎及引擎路径");
          return;
        }
        void action.run(async () => {
          try {
            await api.saveMapping(form);
            Toast.success("映射已保存");
            setDirty(false);
            onSaved();
          } catch (err) {
            setError(err instanceof Error ? err.message : "保存失败");
          }
        });
      }}
    >
      <div className="editor-form">
        {error && <Banner type="danger" description={error} closeIcon={null} />}
        <Field label="授权目录" required>
          <Select
            value={form.path}
            disabled={Boolean(mapping)}
            optionList={paths.map((p) => ({ label: p, value: p }))}
            onChange={(v) => change({ path: String(v) })}
          />
        </Field>
        <Field label="存储引擎" required>
          <Select
            value={form.alistId}
            optionList={engines.map((e) => ({
              label: e.remark || e.url,
              value: e.id,
            }))}
            onChange={(v) => change({ alistId: Number(v), virtualPath: "" })}
          />
        </Field>
        <Field label="引擎虚拟目录" required>
          <RemotePaths
            key={form.alistId}
            engineId={form.alistId}
            multiple={false}
            value={form.virtualPath ? [form.virtualPath] : []}
            onChange={(v) => change({ virtualPath: v.at(-1) || "" })}
          />
        </Field>
        <Field label="名称">
          <Input
            value={form.remark}
            onChange={(remark) => change({ remark })}
          />
        </Field>
      </div>
    </Editor>
  );
}
