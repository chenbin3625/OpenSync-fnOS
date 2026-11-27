import { useCallback, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconAlertTriangle,
  IconBarChartVStroked,
  IconChainStroked,
  IconChevronRight,
  IconDeleteStroked,
  IconDownload,
  IconEditStroked,
  IconFolderStroked,
  IconInfoCircle,
  IconKey,
  IconLockStroked,
  IconPlusStroked,
  IconRefresh,
  IconUpload,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { CryptRecoveryModal } from "../components/CryptRecoveryModal";
import {
  NativeStorageEditor,
  remoteTypeLabels,
  type NativeEditorMode,
} from "../components/NativeStorageEditor";
import { QuickCryptModal } from "../components/QuickCryptModal";
import {
  Header,
  IconButton,
  InlineNotice,
  LoadState,
  ResourceCard,
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
  const [deletingName, setDeletingName] = useState<string | null>(null);
  const [jobCryptExpanded, setJobCryptExpanded] = useState(false);
  const [cryptRecoveryRemoteName, setCryptRecoveryRemoteName] = useState<
    string | null
  >(null);

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
    deletingName !== null;

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
                </>
              }
            />

            {/* Cards 2..N: Remote storage cards (excluding job-managed crypt channels) */}
            {(() => {
              const allRemotes = state.remotes || [];
              const isJobManagedCrypt = (r: NativeRemoteItem) =>
                r.type === "crypt" && r.options?.managed_by_job === "true";
              const regularRemotes = allRemotes.filter(
                (r) => !isJobManagedCrypt(r),
              );
              const managedJobCryptRemotes = allRemotes.filter(isJobManagedCrypt);

              const renderRemoteCard = (item: NativeRemoteItem) => {
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
                      state.remotes.find((r) => r.name === name)?.type ===
                      "crypt",
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
                const jobManagedLabel = item.options?.managed_job_label;

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
                              <IconLockStroked
                                aria-hidden="true"
                                size="extra-small"
                              />
                              <span>
                                {remoteTypeLabels[item.type] || item.type}
                              </span>
                            </span>
                          ) : (
                            remoteTypeLabels[item.type] || item.type
                          )}
                        </Tag>
                        {item.type === "crypt" &&
                          item.options?.managed_by_job === "true" && (
                            <Tag size="small" type="light" color="cyan">
                              {jobManagedLabel
                                ? `任务：${jobManagedLabel}`
                                : "任务向导创建"}
                            </Tag>
                          )}
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
                              navigate(
                                `/sync?storage=${encodeURIComponent(item.name)}`,
                              )
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
                            {allRefsAreCrypt
                              ? "已关联加密存储"
                              : "被其他存储引用"}
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
                    subtitle={
                      item.summary || remoteTypeLabels[item.type] || item.type
                    }
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
                        {item.type === "crypt" && (
                          <div className="crypt-card-guidance-actions">
                            <Button
                              size="small"
                              type="tertiary"
                              icon={<IconKey aria-hidden="true" />}
                              disabled={anyBusy}
                              onClick={() => setCryptRecoveryRemoteName(item.name)}
                            >
                              导出密钥恢复包
                            </Button>
                            <Button
                              size="small"
                              type="tertiary"
                              icon={<IconFolderStroked aria-hidden="true" />}
                              disabled={anyBusy}
                              onClick={() =>
                                setModal({
                                  mode: "browse",
                                  initialBrowsePath: `/${item.name}`,
                                })
                              }
                            >
                              从加密存储一键还原到本地
                            </Button>
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
                        {item.type === "crypt" && (
                          <IconButton
                            label={`导出 ${item.name} 密钥恢复包`}
                            icon={<IconKey aria-hidden="true" />}
                            disabled={anyBusy}
                            onClick={() => setCryptRecoveryRemoteName(item.name)}
                          />
                        )}
                        {canEncrypt && (
                          <IconButton
                            label={`基于 ${item.name} 创建加密存储`}
                            icon={<IconLockStroked aria-hidden="true" />}
                            disabled={anyBusy}
                            onClick={() => setCryptSourceRemote(item)}
                          />
                        )}
                        <IconButton
                          label={`编辑 ${item.name}`}
                          icon={<IconEditStroked aria-hidden="true" />}
                          disabled={anyBusy}
                          onClick={() =>
                            setModal({ mode: "form", editRemote: item })
                          }
                        />
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
              };

              return (
                <>
                  {regularRemotes.map(renderRemoteCard)}
                  {managedJobCryptRemotes.length > 0 && (
                    <div
                      className="managed-job-crypt-section"
                      role="region"
                      aria-label="任务专属加密通道（可展开查看/管理）"
                    >
                      <button
                        type="button"
                        className="managed-job-crypt-toggle"
                        aria-expanded={jobCryptExpanded}
                        onClick={() => setJobCryptExpanded((v) => !v)}
                      >
                        <span className="managed-job-crypt-toggle-left">
                          <IconChevronRight
                            aria-hidden="true"
                            size="small"
                            className={`managed-job-crypt-chevron${
                              jobCryptExpanded ? " is-open" : ""
                            }`}
                          />
                          <IconLockStroked aria-hidden="true" size="small" />
                          <span>任务专属加密通道（可展开查看/管理）</span>
                          <Tag size="small" type="light" color="violet">
                            {managedJobCryptRemotes.length} 个
                          </Tag>
                        </span>
                        <span className="muted managed-job-crypt-toggle-hint">
                          {jobCryptExpanded ? "收起" : "展开管理"}
                        </span>
                      </button>
                      {jobCryptExpanded && (
                        <div className="managed-job-crypt-list">
                          {managedJobCryptRemotes.map(renderRemoteCard)}
                        </div>
                      )}
                    </div>
                  )}
                </>
              );
            })()}

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
          onSaved={(savedName) => {
            if (modal.editRemote && savedName && savedName !== modal.editRemote.name) {
              setTestResults({});
              setAboutResults({});
              window.dispatchEvent(new CustomEvent("opensync:jobs-changed"));
            }
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
            onOpenRestore={(cryptName) =>
              setModal({ mode: "browse", initialBrowsePath: `/${cryptName}` })
            }
            onCreated={async ({ cryptName }) => {
              Toast.success(`已创建加密存储「${cryptName}」`);
              setCryptSourceRemote(null);
              await refreshAll(true);
            }}
          />
        );
      })()}

      {cryptRecoveryRemoteName && (
        <CryptRecoveryModal
          remoteName={cryptRecoveryRemoteName}
          onClose={() => setCryptRecoveryRemoteName(null)}
          onOpenRestore={(name) =>
            setModal({ mode: "browse", initialBrowsePath: `/${name}` })
          }
        />
      )}

    </div>
  );
}
