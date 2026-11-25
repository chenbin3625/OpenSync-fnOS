import { useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconChainStroked,
  IconChevronDown,
  IconChevronRight,
  IconDeleteStroked,
  IconEditStroked,
  IconPlusStroked,
  IconRefresh,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { NativeStorageEditor } from "../components/NativeStorageEditor";
import {
  Editor,
  Field,
  Header,
  IconButton,
  LoadState,
  ResourceCard,
  confirmDelete,
  errorToast,
} from "../components/common";
import { useAction, useEditorForm, useResource } from "../lib/hooks";
import { formatTimestamp } from "../utils/date";
import type { AlistItem, EngineStorageStatus } from "../types";

function isStorageHealthy(st: EngineStorageStatus): boolean {
  if (st.disabled) return true;
  const s = (st.status || "").trim().toLowerCase();
  return s === "" || s === "work" || s === "ok";
}

export default function Engines() {
  const engines = useResource((signal) => api.engines(signal));
  const action = useAction();
  const [editing, setEditing] = useState<AlistItem | null | undefined>();
  const [testingId, setTestingId] = useState<number | null>(null);
  const [nativeOpen, setNativeOpen] = useState(false);
  const nativeEngine = engines.data?.find((engine) => engine.kind === "rclone");
  const [checkingAll, setCheckingAll] = useState(false);
  const [expandedStorages, setExpandedStorages] = useState<
    Record<number, boolean>
  >({});

  const testEngine = (id: number) => {
    setTestingId(id);
    api
      .testEngine(id)
      .then((health) => {
        const latency = health?.latencyMs ? `（${health.latencyMs}ms）` : "";
        const storageList = health?.storagesKnown ? health.storages || [] : [];
        const unhealthy = storageList.filter((st) => !isStorageHealthy(st)).length;
        if (unhealthy > 0) {
          Toast.warning(
            `引擎在线${latency}，但有 ${unhealthy} 个存储挂载异常`,
          );
          setExpandedStorages((prev) => ({ ...prev, [id]: true }));
        } else {
          const storages =
            health?.storagesKnown && storageList.length > 0
              ? `，已读取 ${storageList.length} 个挂载点`
              : "";
          Toast.success(`引擎连接正常${latency}${storages}`);
        }
        void engines.refresh(true);
      })
      .catch((err) => {
        errorToast(err);
        void engines.refresh(true);
      })
      .finally(() => setTestingId(null));
  };

  const refreshAllHealth = async () => {
    setCheckingAll(true);
    try {
      await api.engineHealth({ refresh: 1 });
      await engines.refresh(true);
      Toast.success("已完成全部存储引擎健康检查");
    } catch (err) {
      errorToast(err);
    } finally {
      setCheckingAll(false);
    }
  };

  return (
    <div className="page">
      <Header
        title="引擎管理"
        actions={
          <div className="dashboard-header-actions">
            {Boolean(engines.data?.length) && (
              <Button
                type="tertiary"
                icon={<IconRefresh aria-hidden="true" />}
                loading={checkingAll}
                onClick={() => void refreshAllHealth()}
              >
                刷新健康状态
              </Button>
            )}
            <Button
              disabled={engines.loading && !engines.data}
              onClick={() => setNativeOpen(true)}
            >
              {nativeEngine ? "配置原生存储" : "添加原生存储"}
            </Button>
            <Button
              theme="solid"
              icon={<IconPlusStroked aria-hidden="true" />}
              onClick={() => setEditing(null)}
            >
              添加引擎
            </Button>
          </div>
        }
      />
      <LoadState
        loading={engines.loading}
        error={engines.error}
        retry={engines.refresh}
        empty={!engines.data?.length}
      >
        <div className="item-list">
          {engines.data?.map((engine) => {
            const health = engine.health;
            const storages = health?.storages || [];
            const unhealthyCount = storages.filter(
              (st) => !isStorageHealthy(st),
            ).length;
            const expanded = Boolean(expandedStorages[engine.id]);
            const maxConcurrent = Number(engine.maxConcurrentJobs || 0);
            const activeJobs = Number(engine.activeJobs || 0);
            const queuedJobs = Number(engine.queuedJobs || 0);

            return (
              <ResourceCard
                key={engine.id}
                className="engine-item"
                title={
                  <span className="engine-title-row">
                    <span>
                      {engine.remark || engine.userName || `引擎 #${engine.id}`}
                    </span>
                    {health?.status === "online" ? (
                      <Tag size="small" type="light" color="green">
                        在线 · {health.latencyMs}ms
                      </Tag>
                    ) : health?.status === "offline" ? (
                      <Tag size="small" type="light" color="red">
                        连接异常
                      </Tag>
                    ) : (
                      <Tag size="small" type="light" color="grey">
                        未检测
                      </Tag>
                    )}
                    {maxConcurrent > 0 && (
                      <Tag size="small" type="light" color="blue">
                        {maxConcurrent === 1
                          ? "串行排队"
                          : `最多 ${maxConcurrent} 并发`}
                      </Tag>
                    )}
                    {(activeJobs > 0 || queuedJobs > 0) && (
                      <Tag size="small" type="light" color="cyan">
                        运行 {activeJobs}
                        {queuedJobs > 0 ? ` · 排队 ${queuedJobs}` : ""}
                      </Tag>
                    )}
                    {unhealthyCount > 0 && (
                      <Tag size="small" type="solid" color="red">
                        {unhealthyCount} 个挂载异常
                      </Tag>
                    )}
                  </span>
                }
                subtitle={
                  engine.kind === "rclone"
                    ? (() => {
                        const names = Object.keys(engine.remotes || {}).sort((a, b) =>
                          a.localeCompare(b),
                        );
                        return names.length > 0
                          ? ["本地授权目录", ...names].join(" · ")
                          : "本地授权目录（未配置远端存储）";
                      })()
                    : engine.url
                }
                meta={
                  <div className="engine-meta-stack">
                    <div className="item-meta">
                      {engine.kind === "rclone"
                        ? "内置 rclone 引擎"
                        : `账号 ${engine.userName || "—"}`}
                      {health?.lastCheckedAt
                        ? ` · 最近检测 ${formatTimestamp(health.lastCheckedAt, true)}`
                        : ""}
                    </div>
                    {health?.status === "offline" && health.error && (
                      <div className="inline-error">{health.error}</div>
                    )}
                    {health?.storagesKnown && storages.length > 0 && (
                      <div className="engine-storages">
                        <button
                          type="button"
                          className="engine-storages-toggle"
                          aria-expanded={expanded}
                          onClick={() =>
                            setExpandedStorages((prev) => ({
                              ...prev,
                              [engine.id]: !prev[engine.id],
                            }))
                          }
                        >
                          {expanded ? (
                            <IconChevronDown size="small" aria-hidden="true" />
                          ) : (
                            <IconChevronRight size="small" aria-hidden="true" />
                          )}
                          <span>
                            挂载存储（{storages.length} 个
                            {unhealthyCount > 0
                              ? `，${unhealthyCount} 项异常`
                              : "，全部正常"}
                            ）
                          </span>
                        </button>
                        {expanded && (
                          <div className="engine-storage-list">
                            {storages.map((st) => {
                              const ok = isStorageHealthy(st);
                              return (
                                <div
                                  key={`${st.mountPath}-${st.driver}`}
                                  className={`engine-storage-chip${ok ? "" : " is-error"}`}
                                >
                                  <strong>{st.mountPath}</strong>
                                  <span className="muted">{st.driver}</span>
                                  <Tag
                                    size="small"
                                    type="light"
                                    color={
                                      st.disabled
                                        ? "grey"
                                        : ok
                                          ? "green"
                                          : "red"
                                    }
                                  >
                                    {st.disabled
                                      ? "已禁用"
                                      : ok
                                        ? "正常"
                                        : st.status}
                                  </Tag>
                                </div>
                              );
                            })}
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                }
                actions={
                  <>
                    <IconButton
                      label={testingId === engine.id ? "测试中…" : "测试引擎"}
                      icon={<IconChainStroked aria-hidden="true" />}
                      disabled={testingId !== null}
                      onClick={() => testEngine(engine.id)}
                    />
                    <IconButton
                      label="编辑引擎"
                      icon={<IconEditStroked aria-hidden="true" />}
                      onClick={() => engine.kind==="rclone"?setNativeOpen(true):setEditing(engine)}
                    />
                    <IconButton
                      label="删除引擎"
                      icon={<IconDeleteStroked aria-hidden="true" />}
                      danger
                      onClick={() =>
                        confirmDelete(
                          "删除此存储引擎？",
                          () =>
                            action.run(async () => {
                              await api.deleteEngine(engine.id);
                              // Silent: the card list must not flash to a spinner
                              // after the confirm dialog has already closed.
                              await engines.refresh(true);
                            }),
                          "关联同步任务需先删除；引擎中的文件不会被删除。",
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
      {nativeOpen && (
        <NativeStorageEditor
          engine={nativeEngine}
          onClose={() => setNativeOpen(false)}
          onChanged={() => void engines.refresh(true)}
          onSaved={() => {
            setNativeOpen(false);
            void engines.refresh(true);
          }}
        />
      )}
      {editing !== undefined && (
        <EngineEditor
          engine={editing}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            void engines.refresh(true);
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
  const { form, dirty, change, setDirty } = useEditorForm({
    url: engine?.url || "",
    remark: engine?.remark || "",
    token: "",
    maxConcurrentJobs: String(engine?.maxConcurrentJobs ?? 0),
  });
  const action = useAction();
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
    const maxJobsRaw = form.maxConcurrentJobs.trim() || "0";
    const maxConcurrentJobs = Number(maxJobsRaw);
    if (
      !/^\d+$/.test(maxJobsRaw) ||
      !Number.isInteger(maxConcurrentJobs) ||
      maxConcurrentJobs < 0 ||
      maxConcurrentJobs > 50
    ) {
      Toast.warning("最大并发任务数请输入 0–50 的整数");
      return;
    }
    void action.run(async () => {
      try {
        await api.saveEngine(
          {
            url: form.url.trim(),
            remark: form.remark.trim(),
            token: form.token,
            maxConcurrentJobs,
            ...(engine ? { id: engine.id } : {}),
          },
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
        <Field
          label="最大并发任务数"
          hint="0 表示不限制；设为 1 表示同引擎下的多任务按先入先出（FIFO）串行排队，避免并发打满网盘或触发限流"
        >
          <Input
            inputMode="numeric"
            value={form.maxConcurrentJobs}
            onChange={(v) => change("maxConcurrentJobs", v)}
            placeholder="0"
          />
        </Field>
      </div>
    </Editor>
  );
}
