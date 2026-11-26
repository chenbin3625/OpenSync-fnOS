import { useCallback, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Modal from "@douyinfe/semi-ui/lib/es/modal";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconBarChartVStroked,
  IconChainStroked,
  IconDeleteStroked,
  IconDownload,
  IconEditStroked,
  IconFolderStroked,
  IconInfoCircle,
  IconLockStroked,
  IconPlusStroked,
  IconRefresh,
  IconSettingStroked,
  IconUpload,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import {
  NativeStorageEditor,
  remoteTypeLabels,
  validateRemoteName,
  type NativeEditorMode,
} from "../components/NativeStorageEditor";
import { QuickCryptModal } from "../components/QuickCryptModal";
import {
  Field,
  Header,
  IconButton,
  InlineNotice,
  LoadState,
  ResourceCard,
  confirmAction,
  confirmDelete,
  errorToast,
} from "../components/common";
import { useResource } from "../lib/hooks";
import {
  buildStorageCryptTarget,
  canCreateCryptFromRemote,
} from "../lib/quickCrypt";
import { formatSize } from "./Home/homeUtils";
import { formatTimestamp } from "../utils/date";
import type {
  EngineStorageStatus,
  NativeAboutInfo,
  NativeRemoteItem,
  NativeStorageTestResult,
} from "../types";

function isStorageHealthy(st?: EngineStorageStatus): boolean {
  if (!st) return true;
  if (st.disabled) return true;
  const s = (st.status || "").trim().toLowerCase();
  return s === "" || s === "work" || s === "ok";
}

interface StorageModalState {
  mode: NativeEditorMode;
  editRemote?: NativeRemoteItem | null;
  initialBrowsePath?: string;
}

export default function Engines() {
  const navigate = useNavigate();
  const nativeState = useResource((signal) => api.nativeStorageState(signal));
  const engines = useResource((signal) => api.engines(signal));

  const [modal, setModal] = useState<StorageModalState | null>(null);
  const [cryptSourceRemote, setCryptSourceRemote] =
    useState<NativeRemoteItem | null>(null);
  const [checkingAll, setCheckingAll] = useState(false);
  const [testingName, setTestingName] = useState<string | null>(null);
  const [testResults, setTestResults] = useState<
    Record<string, NativeStorageTestResult>
  >({});
  const [aboutLoadingName, setAboutLoadingName] = useState<string | null>(null);
  const [aboutResults, setAboutResults] = useState<
    Record<string, NativeAboutInfo>
  >({});
  const [cleaningName, setCleaningName] = useState<string | null>(null);
  const [deletingName, setDeletingName] = useState<string | null>(null);
  const [renameTarget, setRenameTarget] = useState<string | null>(null);
  const [renameNewName, setRenameNewName] = useState("");
  const [renaming, setRenaming] = useState(false);

  const state = nativeState.data;
  const nativeEngine = engines.data?.find(
    (engine) => engine.kind === "rclone" || engine.url === "rclone://native",
  ) ?? engines.data?.[0];
  const health = nativeEngine?.health;

  const storageStatusMap = useMemo(() => {
    const map = new Map<string, EngineStorageStatus>();
    for (const st of health?.storages || []) {
      map.set(st.mountPath, st);
    }
    return map;
  }, [health?.storages]);

  const exportableRemotes = useMemo(
    () => (state?.remotes || []).filter((r) => r.type !== "local"),
    [state?.remotes],
  );

  const anyBusy =
    checkingAll ||
    testingName !== null ||
    aboutLoadingName !== null ||
    cleaningName !== null ||
    deletingName !== null ||
    renaming;

  const refreshAll = useCallback(
    async (silent = true) => {
      await Promise.all([
        nativeState.refresh(silent),
        engines.refresh(silent),
      ]);
    },
    [nativeState.refresh, engines.refresh],
  );

  const refreshAllHealth = async () => {
    setCheckingAll(true);
    try {
      await api.engineHealth({ refresh: 1 });
      await refreshAll(true);
      Toast.success("已完成全部存储连通性检查");
    } catch (err) {
      errorToast(err);
    } finally {
      setCheckingAll(false);
    }
  };

  const handleTestRemote = async (name: string) => {
    setTestingName(name);
    try {
      const res = await api.testNativeStorage({ name });
      setTestResults((prev) => ({ ...prev, [name]: res }));
      if (res.ok && res.warning) {
        Toast.warning(`${name}：${res.message}`);
      } else if (res.ok) {
        Toast.success(`${name}：${res.message}（${res.latencyMs}ms）`);
      } else {
        Toast.error(`${name}：${res.message}`);
      }
      await refreshAll(true);
    } catch (err) {
      errorToast(err);
    } finally {
      setTestingName(null);
    }
  };

  const handleAboutStorage = async (name: string) => {
    setAboutLoadingName(name);
    try {
      const info = await api.aboutNativeStorage(name);
      setAboutResults((prev) => ({ ...prev, [name]: info }));
      if (!info.supported) {
        Toast.info(info.message || `${name} 不支持空间用量查询`);
      }
    } catch (err) {
      errorToast(err);
    } finally {
      setAboutLoadingName(null);
    }
  };

  const handleCleanupRemote = (name: string) => {
    confirmAction({
      title: `清理远程存储「${name}」回收站与碎片？`,
      content:
        "将调用 rclone cleanup 清空远端回收站或残留的分片上传碎片（具体行为取决于远端存储）。此操作不可撤销。",
      okText: "确认清理",
      onOk: async () => {
        setCleaningName(name);
        try {
          await api.cleanupNativeStorage(name);
          Toast.success(`已完成「${name}」清理`);
        } catch (err) {
          errorToast(err);
        } finally {
          setCleaningName(null);
        }
      },
    });
  };

  const handleDeleteRemote = (item: NativeRemoteItem) => {
    if (item.usedByCrypts.length > 0) {
      const allCrypt = item.usedByCrypts.every(
        (name) =>
          (state?.remotes || []).find((r) => r.name === name)?.type === "crypt",
      );
      Toast.warning(
        allCrypt
          ? `存储「${item.name}」正被加密存储（${item.usedByCrypts.join("、")}）引用，请先删除或修改对应加密存储`
          : `存储「${item.name}」正被其他存储（${item.usedByCrypts.join("、")}）引用，请先删除或修改对应存储`,
      );
      return;
    }
    if (item.usedByJobs.length > 0) {
      Toast.warning(
        `存储「${item.name}」正被同步任务（${item.usedByJobs.join("、")}）使用，请先修改或删除关联任务`,
      );
      return;
    }
    confirmDelete(
      `删除远程存储「${item.name}」？`,
      async () => {
        setDeletingName(item.name);
        try {
          await api.deleteNativeStorage(item.name);
          setTestResults((prev) => {
            const copy = { ...prev };
            delete copy[item.name];
            return copy;
          });
          setAboutResults((prev) => {
            const copy = { ...prev };
            delete copy[item.name];
            return copy;
          });
          Toast.success(`已删除存储「${item.name}」`);
          await refreshAll(true);
        } catch (err) {
          errorToast(err);
        } finally {
          setDeletingName(null);
        }
      },
      "仅删除 OpenSync 中保存的连接配置，不会删除远端文件。",
    );
  };

  const handleExecuteRename = async () => {
    if (!renameTarget) return;
    const trimmed = renameNewName.trim();
    const nameErr = validateRemoteName(trimmed);
    if (nameErr) {
      Toast.warning(nameErr);
      return;
    }
    setRenaming(true);
    try {
      const res = await api.renameNativeStorage({
        oldName: renameTarget,
        newName: trimmed,
      });
      Toast.success(
        res.updatedJobs > 0
          ? `已重命名为「${res.newName}」，并自动更新了 ${res.updatedJobs} 个关联任务`
          : `已重命名为「${res.newName}」`,
      );
      setRenameTarget(null);
      setRenameNewName("");
      setTestResults({});
      setAboutResults({});
      await refreshAll(true);
      window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
    } catch (err) {
      errorToast(err);
    } finally {
      setRenaming(false);
    }
  };

  const renderAboutSummary = (info?: NativeAboutInfo) => {
    if (!info) return null;
    if (!info.supported) {
      return (
        <div className="native-about-line muted">
          {info.message || "该存储不支持空间用量统计"}
        </div>
      );
    }
    const parts: string[] = [];
    if (info.used !== undefined) parts.push(`已用 ${formatSize(info.used)}`);
    if (info.free !== undefined) parts.push(`可用 ${formatSize(info.free)}`);
    if (info.total !== undefined) parts.push(`总计 ${formatSize(info.total)}`);
    if (info.trashed !== undefined && info.trashed > 0) {
      parts.push(`回收站 ${formatSize(info.trashed)}`);
    }
    return (
      <div className="native-about-line muted">
        空间配额：{parts.join(" · ") || "无数据"}
      </div>
    );
  };

  const localStatus = storageStatusMap.get("/本地");
  const localActiveJobs = Number(state?.localActiveJobs || 0);
  const localQueuedJobs = Number(state?.localQueuedJobs || 0);
  const maxConcurrent = Number(state?.maxConcurrentJobs || 0);

  return (
    <div className="page">
      <Header
        title="存储管理"
        actions={
          <div className="dashboard-header-actions">
            <Button
              type="tertiary"
              icon={<IconRefresh aria-hidden="true" />}
              loading={checkingAll}
              disabled={!state || anyBusy}
              onClick={() => void refreshAllHealth()}
            >
              刷新状态
            </Button>
            <Button
              type="tertiary"
              icon={<IconUpload aria-hidden="true" />}
              disabled={!state || anyBusy}
              onClick={() => setModal({ mode: "import" })}
            >
              导入配置
            </Button>
            {exportableRemotes.length > 0 && (
              <Button
                type="tertiary"
                icon={<IconDownload aria-hidden="true" />}
                disabled={!state || anyBusy}
                onClick={() => setModal({ mode: "export" })}
              >
                导出配置
              </Button>
            )}
            <Button
              icon={<IconFolderStroked aria-hidden="true" />}
              disabled={nativeState.loading && !state}
              onClick={() => setModal({ mode: "local" })}
            >
              配置本地目录
            </Button>
            <Button
              theme="solid"
              icon={<IconPlusStroked aria-hidden="true" />}
              disabled={nativeState.loading && !state}
              onClick={() => setModal({ mode: "form", editRemote: null })}
            >
              添加远程存储
            </Button>
          </div>
        }
      />

      <LoadState
        loading={nativeState.loading && !state}
        error={nativeState.error}
        retry={() => void refreshAll(false)}
      >
        {state && (
          <div className="item-list">
            {!state.rcloneAvailable && (
              <InlineNotice
                role="alert"
                icon={<IconAlertTriangle aria-hidden="true" size="small" />}
                message={`内置 rclone 不可用：${state.rcloneError || "未找到可执行文件"}`}
              />
            )}

            {/* Card 1 (always shown): Local authorized directories */}
            <ResourceCard
              className="engine-item storage-card storage-card--local"
              title={
                <span className="engine-title-row">
                  <span>本地目录</span>
                  <Tag size="small" type="light" color="blue">
                    本地授权目录
                  </Tag>
                  {!state.rcloneAvailable ? (
                    <Tag size="small" type="light" color="red">
                      引擎不可用
                    </Tag>
                  ) : state.localRoots.length > 0 ? (
                    <Tag size="small" type="light" color="green">
                      已授权 {state.localRoots.length} 个
                    </Tag>
                  ) : (
                    <Tag size="small" type="light" color="orange">
                      未授权目录
                    </Tag>
                  )}
                  {state.rcloneVersion && (
                    <Tag size="small" type="light" color="cyan">
                      rclone {state.rcloneVersion}
                    </Tag>
                  )}
                  {maxConcurrent > 0 && (
                    <Tag size="small" type="light" color="blue">
                      {maxConcurrent === 1
                        ? "全局串行排队"
                        : `全局最多 ${maxConcurrent} 并发`}
                    </Tag>
                  )}
                  {Number(state.localMaxConcurrentJobs || 0) > 0 && (
                    <Tag size="small" type="light" color="violet">
                      本地最多 {state.localMaxConcurrentJobs} 并发
                    </Tag>
                  )}
                  {(localActiveJobs > 0 || localQueuedJobs > 0) && (
                    <Tag size="small" type="light" color="cyan">
                      运行 {localActiveJobs}
                      {localQueuedJobs > 0 ? ` · 排队 ${localQueuedJobs}` : ""}
                    </Tag>
                  )}
                </span>
              }
              subtitle={
                state.localRoots.length > 0 ? (
                  <div className="native-local-roots storage-card-roots">
                    {state.localRoots.map((root) => (
                      <span
                        key={root.id}
                        className="native-local-chip mono"
                        title={root.path}
                      >
                        {root.path}
                      </span>
                    ))}
                  </div>
                ) : (
                  "请在飞牛「应用设置 → 文件夹访问权限」中为 OpenSync 授权文件夹"
                )
              }
              meta={
                <div className="engine-meta-stack">
                  <div className="item-meta">
                    内置 rclone 引擎
                    {state.remark && state.remark !== "内置存储"
                      ? `（${state.remark}）`
                      : ""}
                    {health?.lastCheckedAt
                      ? ` · 最近检测 ${formatTimestamp(health.lastCheckedAt, true)}`
                      : ""}
                  </div>
                  {localStatus && !isStorageHealthy(localStatus) && state.localRoots.length > 0 && (
                    <div className="inline-error">{localStatus.status}</div>
                  )}
                  {renderAboutSummary(aboutResults["本地"])}
                </div>
              }
              actions={
                <>
                  <IconButton
                    label="浏览本地目录"
                    icon={<IconFolderStroked aria-hidden="true" />}
                    disabled={state.localRoots.length === 0 || anyBusy}
                    onClick={() =>
                      setModal({ mode: "browse", initialBrowsePath: "/本地" })
                    }
                  />
                  <IconButton
                    label={
                      aboutLoadingName === "本地"
                        ? "查询空间中…"
                        : "查看本地空间配额"
                    }
                    icon={<IconBarChartVStroked aria-hidden="true" />}
                    disabled={state.localRoots.length === 0 || anyBusy}
                    ariaBusy={aboutLoadingName === "本地"}
                    onClick={() => void handleAboutStorage("本地")}
                  />
                  <IconButton
                    label="配置本地目录"
                    icon={<IconSettingStroked aria-hidden="true" />}
                    disabled={anyBusy}
                    onClick={() => setModal({ mode: "local" })}
                  />
                </>
              }
            />

            {/* Cards 2..N: One card per configured remote storage */}
            {state.remotes.map((item) => {
              const st = storageStatusMap.get(`/${item.name}`);
              const healthy = st ? isStorageHealthy(st) : undefined;
              const testRes = testResults[item.name];
              const aboutRes = aboutResults[item.name];
              const activeJobs = Number(item.activeJobs || 0);
              const queuedJobs = Number(item.queuedJobs || 0);
              const isLocked =
                item.usedByJobs.length > 0 || item.usedByCrypts.length > 0;
              const allRefsAreCrypt =
                item.usedByCrypts.length > 0 &&
                item.usedByCrypts.every(
                  (name) =>
                    state.remotes.find((r) => r.name === name)?.type === "crypt",
                );
              const lockHint =
                item.usedByCrypts.length > 0
                  ? allRefsAreCrypt
                    ? `正被加密存储 ${item.usedByCrypts.join("、")} 引用`
                    : `正被其他存储 ${item.usedByCrypts.join("、")} 引用`
                  : item.usedByJobs.length > 0
                    ? `正被任务 ${item.usedByJobs.join("、")} 使用`
                    : "";
              const canEncrypt = canCreateCryptFromRemote(item, state.remotes);

              return (
                <ResourceCard
                  key={item.name}
                  className="engine-item storage-card"
                  title={
                    <span className="engine-title-row">
                      <span>{item.name}</span>
                      <Tag
                        size="small"
                        type="light"
                        color={item.type === "crypt" ? "violet" : "blue"}
                      >
                        {item.type === "crypt" ? (
                          <span className="storage-type-tag-inner">
                            <IconLockStroked aria-hidden="true" size="extra-small" />
                            <span>{remoteTypeLabels[item.type] || item.type}</span>
                          </span>
                        ) : (
                          remoteTypeLabels[item.type] || item.type
                        )}
                      </Tag>
                      {testRes ? (
                        <Tag
                          size="small"
                          type="light"
                          color={
                            testRes.ok
                              ? testRes.warning
                                ? "orange"
                                : "green"
                              : "red"
                          }
                        >
                          {testRes.ok
                            ? `正常 · ${testRes.latencyMs}ms`
                            : "连接异常"}
                        </Tag>
                      ) : healthy === true ? (
                        <Tag size="small" type="light" color="green">
                          正常
                        </Tag>
                      ) : healthy === false ? (
                        <Tag size="small" type="light" color="red">
                          连接异常
                        </Tag>
                      ) : (
                        <Tag size="small" type="light" color="grey">
                          未检测
                        </Tag>
                      )}
                      {item.usedByJobs.length > 0 && (
                        <Tag
                          size="small"
                          type="light"
                          color="cyan"
                          className="engine-clickable-tag"
                          onClick={() =>
                            navigate(`/dashboard?storage=${encodeURIComponent(item.name)}`)
                          }
                        >
                          {item.usedByJobs.length} 个任务
                        </Tag>
                      )}
                      {Number(item.maxConcurrentJobs || 0) > 0 && (
                        <Tag size="small" type="light" color="violet">
                          单存储限 {item.maxConcurrentJobs} 并发
                        </Tag>
                      )}
                      {item.usedByCrypts.length > 0 && (
                        <Tag size="small" type="light" color="violet">
                          {allRefsAreCrypt ? "已关联加密存储" : "被其他存储引用"}
                        </Tag>
                      )}
                      {(activeJobs > 0 || queuedJobs > 0) && (
                        <Tag size="small" type="light" color="cyan">
                          运行 {activeJobs}
                          {queuedJobs > 0 ? ` · 排队 ${queuedJobs}` : ""}
                        </Tag>
                      )}
                    </span>
                  }
                  subtitle={item.summary || (remoteTypeLabels[item.type] || item.type)}
                  meta={
                    <div className="engine-meta-stack">
                      {health && health.lastCheckedAt > 0 && (
                        <div className="item-meta">
                          挂载路径 /{item.name} · 最近检测{" "}
                          {formatTimestamp(health.lastCheckedAt, true)}
                        </div>
                      )}
                      {healthy === false && st?.status && !testRes && (
                        <div className="inline-error">{st.status}</div>
                      )}
                      {renderAboutSummary(aboutRes)}
                      {testRes && (
                        <div
                          className={`native-inline-test ${
                            testRes.ok
                              ? testRes.warning
                                ? "is-warn"
                                : "is-ok"
                              : "is-error"
                          }`}
                        >
                          <span>
                            {testRes.message}（{testRes.latencyMs}ms）
                          </span>
                          {testRes.warning && (
                            <span className="native-inline-test-sub">
                              {testRes.warning}
                            </span>
                          )}
                        </div>
                      )}
                    </div>
                  }
                  actions={
                    <>
                      <IconButton
                        label={
                          testingName === item.name
                            ? "测试中…"
                            : `测试连接 ${item.name}`
                        }
                        icon={<IconChainStroked aria-hidden="true" />}
                        disabled={anyBusy}
                        ariaBusy={testingName === item.name}
                        onClick={() => void handleTestRemote(item.name)}
                      />
                      <IconButton
                        label={`浏览与恢复 ${item.name}`}
                        icon={<IconFolderStroked aria-hidden="true" />}
                        disabled={anyBusy}
                        onClick={() =>
                          setModal({
                            mode: "browse",
                            initialBrowsePath: `/${item.name}`,
                          })
                        }
                      />
                      <IconButton
                        label={
                          aboutLoadingName === item.name
                            ? "查询空间中…"
                            : `查看空间 ${item.name}`
                        }
                        icon={<IconBarChartVStroked aria-hidden="true" />}
                        disabled={anyBusy}
                        ariaBusy={aboutLoadingName === item.name}
                        onClick={() => void handleAboutStorage(item.name)}
                      />
                      {canEncrypt && (
                        <IconButton
                          label={`基于 ${item.name} 创建加密存储`}
                          icon={<IconLockStroked aria-hidden="true" />}
                          disabled={anyBusy}
                          onClick={() => setCryptSourceRemote(item)}
                        />
                      )}
                      {item.type !== "local" && (
                        <IconButton
                          label={
                            cleaningName === item.name
                              ? "清理中…"
                              : `清理回收站与碎片 ${item.name}`
                          }
                          icon={<IconRefresh aria-hidden="true" />}
                          disabled={anyBusy}
                          ariaBusy={cleaningName === item.name}
                          onClick={() => handleCleanupRemote(item.name)}
                        />
                      )}
                      <IconButton
                        label={`重命名 ${item.name}`}
                        icon={<IconSettingStroked aria-hidden="true" />}
                        disabled={anyBusy}
                        onClick={() => {
                          setRenameTarget(item.name);
                          setRenameNewName(item.name);
                        }}
                      />
                      {item.type !== "local" && (
                        <IconButton
                          label={`编辑 ${item.name}`}
                          icon={<IconEditStroked aria-hidden="true" />}
                          disabled={anyBusy}
                          onClick={() =>
                            setModal({ mode: "form", editRemote: item })
                          }
                        />
                      )}
                      <IconButton
                        label={
                          isLocked
                            ? `无法删除：${lockHint}`
                            : `删除 ${item.name}`
                        }
                        icon={<IconDeleteStroked aria-hidden="true" />}
                        danger
                        disabled={anyBusy}
                        ariaBusy={deletingName === item.name}
                        onClick={() => handleDeleteRemote(item)}
                      />
                    </>
                  }
                />
              );
            })}

            {state.remotes.length === 0 && (
              <div className="sync-method-banner">
                <IconInfoCircle aria-hidden="true" size="small" />
                <span>
                  尚未添加远程存储。可直接在不同本地授权目录之间同步，或点击右上角「添加远程存储」/「导入配置」接入 S3、WebDAV（含 OpenList / AList）、SFTP、SMB、OneDrive、Google Drive 等常见云盘与对象存储。
                </span>
              </div>
            )}
          </div>
        )}
      </LoadState>

      {modal && (
        <NativeStorageEditor
          engine={nativeEngine}
          mode={modal.mode}
          editRemote={modal.editRemote}
          initialBrowsePath={modal.initialBrowsePath}
          onClose={() => setModal(null)}
          onChanged={() => void refreshAll(true)}
          onSaved={() => {
            setModal(null);
            void refreshAll(true);
          }}
        />
      )}

      {cryptSourceRemote && state && (() => {
        const target = buildStorageCryptTarget(cryptSourceRemote, state.remotes);
        return (
          <QuickCryptModal
            mode="storage"
            targets={[target]}
            remotes={state.remotes}
            requireSubDir={target.requiresSubDir}
            remoteTypeLabel={
              remoteTypeLabels[cryptSourceRemote.type] || cryptSourceRemote.type
            }
            subDirPlaceholder={
              target.leafBackend === "smb"
                ? "例如 share/encrypted"
                : target.requiresSubDir
                  ? "例如 my-bucket/encrypted"
                  : "例如 encrypted"
            }
            onClose={() => setCryptSourceRemote(null)}
            onCreated={async ({ cryptName }) => {
              Toast.success(`已创建加密存储「${cryptName}」`);
              setCryptSourceRemote(null);
              await refreshAll(true);
            }}
          />
        );
      })()}

      {renameTarget && (
        <Modal
          visible
          centered
          width={440}
          title={`重命名存储 · ${renameTarget}`}
          onCancel={() => {
            if (!renaming) setRenameTarget(null);
          }}
          okText="确认重命名"
          confirmLoading={renaming}
          onOk={() => void handleExecuteRename()}
        >
          <div className="editor-form">
            <Field
              label="新存储名称"
              required
              hint="重命名将自动级联更新关联的加密存储及同步任务的源/目标路径"
            >
              <Input
                value={renameNewName}
                maxLength={64}
                onChange={setRenameNewName}
                placeholder="输入新的存储名称"
              />
            </Field>
          </div>
        </Modal>
      )}
    </div>
  );
}
