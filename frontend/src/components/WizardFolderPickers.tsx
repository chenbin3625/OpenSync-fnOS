import {
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import {
  IconChevronDown,
  IconChevronRight,
  IconCloudStroked,
  IconFolderStroked,
  IconLockStroked,
  IconPlus,
  IconRefresh,
} from "@douyinfe/semi-icons";
import { api } from "../api/client";
import { getErrorMessage, InlineNotice } from "./common";
import { useRemoteDirCache, type RemoteDirCache } from "../lib/remoteDirCache";
import { isContainerRequiredBackend } from "../lib/quickCrypt";
import {
  resolveRemoteChain,
  validateManualSyncPath,
} from "../lib/taskForm";
import {
  extractFallbackLocalRoots,
  extractRemoteRootAndSubPath,
  formatLocalRootDisplayItems,
  formatNasDisplayPath,
  normalizeWizardFolderChildren,
  type WizardFolderChildNode,
} from "../lib/taskWizard";
import type { NativeLocalRoot, NativeRemoteItem } from "../types";

type FolderChildNode = WizardFolderChildNode;

const REMOTE_TYPE_LABELS: Record<string, string> = {
  s3: "S3 对象存储",
  webdav: "WebDAV",
  onedrive: "OneDrive",
  drive: "Google Drive",
  dropbox: "Dropbox",
  quark: "夸克网盘",
  pan123: "123 云盘",
  sftp: "SFTP",
  smb: "SMB",
  ftp: "FTP",
  crypt: "加密存储",
  http: "HTTP 只读",
};

function formatRemoteTypeBadge(type: string): string {
  return REMOTE_TYPE_LABELS[type] || type.toUpperCase();
}

function validateNewFolderName(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "请输入新文件夹名称";
  if (
    trimmed === "." ||
    trimmed === ".." ||
    /[\\/:*?"<>|\u0000-\u001f]/.test(trimmed)
  ) {
    return "文件夹名称不能包含 / \\ : * ? \" < > | 或 . / ..";
  }
  return "";
}

function useLazyFolderMap(
  engineId: number | undefined,
  dirCache: RemoteDirCache | null,
  epoch?: number,
) {
  const [childrenMap, setChildrenMap] = useState<
    Record<string, FolderChildNode[]>
  >({});
  const [loadingMap, setLoadingMap] = useState<Record<string, boolean>>({});
  const [errorMap, setErrorMap] = useState<Record<string, string>>({});

  useEffect(() => {
    setChildrenMap({});
    setLoadingMap({});
    setErrorMap({});
  }, [engineId, epoch]);

  const loadFolderChildren = useCallback(
    async (parentPath: string, force = false): Promise<FolderChildNode[]> => {
      if (!engineId) return [];
      const cleanParent =
        parentPath === "/" ? "/" : `/${parentPath.replace(/^\/+|\/+$/g, "")}`;
      setLoadingMap((prev) => ({ ...prev, [cleanParent]: true }));
      setErrorMap((prev) => {
        if (!prev[cleanParent]) return prev;
        const next = { ...prev };
        delete next[cleanParent];
        return next;
      });
      try {
        const entries = dirCache
          ? await dirCache.fetchPaths(engineId, cleanParent, { force })
          : await api.paths(engineId, cleanParent);
        const list = normalizeWizardFolderChildren(cleanParent, entries);
        setChildrenMap((prev) => ({ ...prev, [cleanParent]: list }));
        return list;
      } catch (err) {
        setErrorMap((prev) => ({
          ...prev,
          [cleanParent]: getErrorMessage(err, "目录加载失败"),
        }));
        return [];
      } finally {
        setLoadingMap((prev) => ({ ...prev, [cleanParent]: false }));
      }
    },
    [engineId, dirCache],
  );

  return { childrenMap, loadingMap, errorMap, loadFolderChildren };
}

interface SubfolderRowsProps {
  parentPath: string;
  depth: number;
  selectedPath: string;
  expandedKeys: Set<string>;
  childrenMap: Record<string, FolderChildNode[]>;
  loadingMap: Record<string, boolean>;
  errorMap: Record<string, string>;
  onToggleExpand: (path: string) => void;
  onSelectPath: (path: string) => void;
  onRetryFolder: (path: string) => void;
}

function SubfolderRows({
  parentPath,
  depth,
  selectedPath,
  expandedKeys,
  childrenMap,
  loadingMap,
  errorMap,
  onToggleExpand,
  onSelectPath,
  onRetryFolder,
}: SubfolderRowsProps) {
  const isLoading = loadingMap[parentPath];
  const loadError = errorMap[parentPath];
  const children = childrenMap[parentPath];
  const padLeft = 10 + depth * 16;

  if (isLoading && !children) {
    return (
      <div className="wizard-tree-status" style={{ paddingLeft: padLeft }}>
        加载目录中...
      </div>
    );
  }
  if (loadError && !children) {
    return (
      <div
        className="wizard-tree-status is-error"
        style={{ paddingLeft: padLeft }}
      >
        <span>{loadError}</span>
        <button
          type="button"
          className="wizard-inline-link"
          onClick={() => onRetryFolder(parentPath)}
        >
          重试
        </button>
      </div>
    );
  }
  if (!children) return null;
  if (children.length === 0) {
    return (
      <div className="wizard-tree-status" style={{ paddingLeft: padLeft }}>
        暂无下级子目录
      </div>
    );
  }

  return (
    <>
      {children.map((child) => {
        const expanded = expandedKeys.has(child.fullPath);
        const selected = selectedPath === child.fullPath;
        return (
          <div key={child.fullPath} className="wizard-tree-node">
            <div
              className={`wizard-tree-row${selected ? " is-selected" : ""}`}
              style={{ paddingLeft: padLeft }}
            >
              <button
                type="button"
                className="wizard-tree-expand"
                aria-label={`${expanded ? "折叠" : "展开"} ${child.name}`}
                aria-expanded={expanded}
                onClick={() => onToggleExpand(child.fullPath)}
              >
                {expanded ? (
                  <IconChevronDown size="small" aria-hidden="true" />
                ) : (
                  <IconChevronRight size="small" aria-hidden="true" />
                )}
              </button>
              <button
                type="button"
                className="wizard-tree-select"
                aria-pressed={selected}
                onClick={() => onSelectPath(child.fullPath)}
              >
                <IconFolderStroked size="small" aria-hidden="true" />
                <span className="wizard-tree-label">{child.name}</span>
              </button>
            </div>
            {expanded && (
              <SubfolderRows
                parentPath={child.fullPath}
                depth={depth + 1}
                selectedPath={selectedPath}
                expandedKeys={expandedKeys}
                childrenMap={childrenMap}
                loadingMap={loadingMap}
                errorMap={errorMap}
                onToggleExpand={onToggleExpand}
                onSelectPath={onSelectPath}
                onRetryFolder={onRetryFolder}
              />
            )}
          </div>
        );
      })}
    </>
  );
}

export function NasLocalFolderPicker({
  engineId,
  localRoots,
  value,
  onChange,
  onRefresh,
  error,
  epoch,
}: {
  engineId?: number;
  localRoots?: readonly NativeLocalRoot[];
  value: string;
  onChange: (nextPath: string) => void;
  onRefresh: () => void;
  error?: string;
  epoch?: number;
}) {
  const dirCache = useRemoteDirCache();
  const { childrenMap, loadingMap, errorMap, loadFolderChildren } =
    useLazyFolderMap(engineId, dirCache, epoch);
  const [expandedKeys, setExpandedKeys] = useState<Set<string>>(() => new Set());
  const [fallbackRoots, setFallbackRoots] = useState<NativeLocalRoot[]>([]);

  useEffect(() => {
    if (localRoots && localRoots.length > 0) {
      setFallbackRoots([]);
      return;
    }
    if (!engineId) return;
    let cancelled = false;
    void loadFolderChildren("/本地").then((entries) => {
      if (!cancelled) {
        setFallbackRoots(extractFallbackLocalRoots(entries));
      }
    });
    return () => {
      cancelled = true;
    };
  }, [engineId, localRoots, loadFolderChildren]);

  const effectiveRoots = useMemo<NativeLocalRoot[]>(() => {
    if (localRoots && localRoots.length > 0) {
      return [...localRoots];
    }
    return fallbackRoots;
  }, [localRoots, fallbackRoots]);

  const displayRoots = useMemo(
    () => formatLocalRootDisplayItems(effectiveRoots),
    [effectiveRoots],
  );

  // Auto-expand ancestor directories of an initially selected NAS path (when editing)
  useEffect(() => {
    if (!value || !engineId) return;
    const segs = value.replace(/^\/+/, "").split("/").filter(Boolean);
    if (segs[0] !== "本地" || segs.length < 2) return;
    const ancestors: string[] = [];
    for (let i = 2; i < segs.length; i++) {
      ancestors.push("/" + segs.slice(0, i).join("/"));
    }
    if (ancestors.length === 0) return;
    setExpandedKeys((prev) => {
      const next = new Set(prev);
      ancestors.forEach((a) => next.add(a));
      return next;
    });
    ancestors.forEach((a) => {
      if (!childrenMap[a]) void loadFolderChildren(a);
    });
  }, [value, engineId, childrenMap, loadFolderChildren]);

  const handleToggleExpand = (path: string) => {
    setExpandedKeys((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
        void loadFolderChildren(path);
      }
      return next;
    });
  };

  const selectedLabel = value
    ? formatNasDisplayPath(value, effectiveRoots)
    : "";

  return (
    <section
      className={`wizard-picker-card${error ? " has-error" : ""}`}
      aria-label="NAS 本地文件夹"
    >
      <header className="wizard-picker-head">
        <div className="wizard-picker-title">
          <IconFolderStroked aria-hidden="true" />
          <span>NAS 本地文件夹</span>
        </div>
        <Button
          size="small"
          type="tertiary"
          icon={<IconRefresh aria-hidden="true" />}
          onClick={onRefresh}
        >
          刷新目录
        </Button>
      </header>

      <div className="wizard-picker-selected" aria-live="polite">
        <span className="wizard-picker-selected-label">已选：</span>
        {selectedLabel ? (
          <strong className="wizard-picker-selected-path">{selectedLabel}</strong>
        ) : (
          <span className="muted">请在下方选择一个本地授权目录</span>
        )}
      </div>

      <div className="wizard-picker-body">
        {displayRoots.length === 0 ? (
          errorMap["/本地"] ? (
            <div className="local-root-auth-notice">
              <InlineNotice
                role="status"
                message={errorMap["/本地"]}
                action={
                  <Button
                    size="small"
                    onClick={() => void loadFolderChildren("/本地", true)}
                  >
                    重试
                  </Button>
                }
              />
            </div>
          ) : (
            <div className="local-root-auth-notice">
              <InlineNotice
                role="status"
                message="尚未检测到 NAS 本地授权目录，请在飞牛「应用设置 → 文件夹访问权限」中授权后点击右上角「刷新目录」。"
              />
            </div>
          )
        ) : (
          <div className="wizard-folder-tree" role="tree" aria-label="NAS 授权目录列表">
            {displayRoots.map((root) => {
              const expanded = expandedKeys.has(root.virtualPath);
              const selected = value === root.virtualPath;
              return (
                <div key={root.id} className="wizard-tree-node">
                  <div
                    className={`wizard-tree-row is-root${selected ? " is-selected" : ""}`}
                  >
                    <button
                      type="button"
                      className="wizard-tree-expand"
                      aria-label={`${expanded ? "折叠" : "展开"} ${root.label}`}
                      aria-expanded={expanded}
                      onClick={() => handleToggleExpand(root.virtualPath)}
                    >
                      {expanded ? (
                        <IconChevronDown size="small" aria-hidden="true" />
                      ) : (
                        <IconChevronRight size="small" aria-hidden="true" />
                      )}
                    </button>
                    <button
                      type="button"
                      className="wizard-tree-select"
                      data-virtual-path={root.virtualPath}
                      aria-pressed={selected}
                      onClick={() => onChange(root.virtualPath)}
                    >
                      <IconFolderStroked size="small" aria-hidden="true" />
                      <span className="wizard-tree-label">{root.label}</span>
                    </button>
                  </div>
                  {expanded && (
                    <SubfolderRows
                      parentPath={root.virtualPath}
                      depth={1}
                      selectedPath={value}
                      expandedKeys={expandedKeys}
                      childrenMap={childrenMap}
                      loadingMap={loadingMap}
                      errorMap={errorMap}
                      onToggleExpand={handleToggleExpand}
                      onSelectPath={onChange}
                      onRetryFolder={(p) => void loadFolderChildren(p, true)}
                    />
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
      {error && (
        <div className="field-error wizard-picker-error" role="alert">
          {error}
        </div>
      )}
    </section>
  );
}

export function CloudRemoteFolderPicker({
  engineId,
  remotes,
  preferredRemote,
  value,
  onChange,
  onAddStorage,
  onRefresh,
  error,
  epoch,
}: {
  engineId?: number;
  remotes?: readonly NativeRemoteItem[];
  preferredRemote?: string;
  value: string;
  onChange: (nextPath: string) => void;
  onAddStorage: () => void;
  onRefresh: () => void;
  error?: string;
  epoch?: number;
}) {
  const dirCache = useRemoteDirCache();
  const { childrenMap, loadingMap, errorMap, loadFolderChildren } =
    useLazyFolderMap(engineId, dirCache, epoch);
  const [expandedKeys, setExpandedKeys] = useState<Set<string>>(() => new Set());
  const [mkdirTargetRemote, setMkdirTargetRemote] = useState<string | null>(
    null,
  );
  const [newFolderName, setNewFolderName] = useState("");
  const [mkdirBusy, setMkdirBusy] = useState(false);
  const [mkdirError, setMkdirError] = useState("");
  const [manualOpen, setManualOpen] = useState(false);
  const [manualSubPath, setManualSubPath] = useState("");

  const remoteList = useMemo(
    () =>
      [...(remotes || [])].sort((a, b) => a.name.localeCompare(b.name, "zh-Hans-CN")),
    [remotes],
  );

  // Auto-expand preferredRemote or current selected remote's ancestor path
  useEffect(() => {
    if (!engineId) return;
    const toExpand = new Set<string>();
    if (preferredRemote && remoteList.some((r) => r.name === preferredRemote)) {
      toExpand.add(`/${preferredRemote}`);
    }
    if (value && !value.startsWith("/本地")) {
      const segs = value.replace(/^\/+/, "").split("/").filter(Boolean);
      for (let i = 1; i < segs.length; i++) {
        toExpand.add("/" + segs.slice(0, i).join("/"));
      }
      if (segs.length >= 1) {
        toExpand.add(`/${segs[0]}`);
      }
    }
    if (toExpand.size === 0) return;
    setExpandedKeys((prev) => {
      const next = new Set(prev);
      toExpand.forEach((p) => next.add(p));
      return next;
    });
    toExpand.forEach((p) => {
      if (!childrenMap[p]) void loadFolderChildren(p);
    });
  }, [engineId, preferredRemote, remoteList, value, childrenMap, loadFolderChildren]);

  const handleToggleRemoteOrFolder = (path: string) => {
    setExpandedKeys((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
        void loadFolderChildren(path);
      }
      return next;
    });
  };

  const selectedMeta = useMemo(
    () => extractRemoteRootAndSubPath(value),
    [value],
  );

  const selectedChain = useMemo(
    () =>
      selectedMeta.remoteName && remoteList.length > 0
        ? resolveRemoteChain(selectedMeta.remoteName, remoteList, new Set(), selectedMeta.subPath)
        : null,
    [selectedMeta, remoteList],
  );

  const getMkdirParentForRemote = (remote: NativeRemoteItem): string => {
    if (selectedMeta.remoteName.toLowerCase() === remote.name.toLowerCase() && value) {
      return value;
    }
    return `/${remote.name}`;
  };

  const handleOpenMkdir = (remote: NativeRemoteItem) => {
    setMkdirTargetRemote(remote.name);
    setNewFolderName("");
    setMkdirError("");
    setExpandedKeys((prev) => {
      const next = new Set(prev);
      next.add(`/${remote.name}`);
      return next;
    });
    void loadFolderChildren(`/${remote.name}`);
  };

  const handleConfirmMkdir = async (remote: NativeRemoteItem) => {
    const parent = getMkdirParentForRemote(remote);
    const parentSegs = parent.replace(/^\/+/, "").split("/").filter(Boolean);
    const containerRequired = isContainerRequiredBackend(remote.type);
    const chain = resolveRemoteChain(remote.name, remoteList);
    if (containerRequired && !chain.accumulatedPrefix && parentSegs.length < 2) {
      setMkdirError("对象存储根目录下请输入要创建的存储桶名（如 my-bucket），或先选定一个存储桶");
    }
    const nameErr = validateNewFolderName(newFolderName);
    if (nameErr) {
      setMkdirError(nameErr);
      return;
    }
    const fullNewPath = `${parent.replace(/\/+$/, "")}/${newFolderName.trim()}`;
    setMkdirBusy(true);
    setMkdirError("");
    try {
      await api.mkdirNativeStorage(fullNewPath);
      dirCache?.invalidateEngine(engineId);
      setExpandedKeys((prev) => {
        const next = new Set(prev);
        next.add(parent);
        return next;
      });
      await loadFolderChildren(parent, true);
      onChange(fullNewPath);
      setMkdirTargetRemote(null);
      setNewFolderName("");
      Toast.success(`已创建远端文件夹：${fullNewPath}`);
    } catch (err) {
      setMkdirError(getErrorMessage(err, "创建远端文件夹失败"));
    } finally {
      setMkdirBusy(false);
    }
  };

  const applyManualPath = () => {
    const raw = manualSubPath.trim();
    if (!raw) return;
    const normalizedInput = raw.startsWith("/") ? raw : `/${raw}`;
    const check = validateManualSyncPath(normalizedInput, remotes, { role: "dst" });
    if (check.error) {
      Toast.warning(check.error);
      return;
    }
    onChange(check.normalized);
    setManualSubPath("");
    setManualOpen(false);
  };

  return (
    <section
      className={`wizard-picker-card${error ? " has-error" : ""}`}
      aria-label="云端存储文件夹"
    >
      <header className="wizard-picker-head">
        <div className="wizard-picker-title">
          <IconCloudStroked aria-hidden="true" />
          <span>云端存储文件夹</span>
        </div>
        <div className="wizard-picker-actions">
          <Button
            size="small"
            theme="light"
            icon={<IconPlus aria-hidden="true" />}
            onClick={onAddStorage}
          >
            新建远端连接
          </Button>
          <Button
            size="small"
            type="tertiary"
            icon={<IconRefresh aria-hidden="true" />}
            aria-label="刷新云端存储列表"
            onClick={onRefresh}
          />
        </div>
      </header>

      <div className="wizard-picker-selected" aria-live="polite">
        <span className="wizard-picker-selected-label">已选：</span>
        {value ? (
          <strong className="wizard-picker-selected-path">
            {value}
            {selectedChain?.hasCrypt && (
              <Tag size="small" color="green" type="light" style={{ marginLeft: 6 }}>
                已加密
              </Tag>
            )}
          </strong>
        ) : (
          <span className="muted">请选择远端网盘及其目标文件夹</span>
        )}
      </div>

      <div className="wizard-picker-body">
        {remoteList.length === 0 ? (
          <div className="wizard-remote-empty">
            <p className="muted">
              尚未添加任何远程网盘或对象存储，添加后即可浏览并选择同步目录。
            </p>
            <Button
              size="small"
              type="primary"
              theme="light"
              onClick={onAddStorage}
            >
              添加网盘连接
            </Button>
          </div>
        ) : (
          <div
            className="wizard-remote-groups"
            role="tree"
            aria-label="云端存储连接列表"
          >
            {remoteList.map((remote) => {
              const rootPath = `/${remote.name}`;
              const expanded = expandedKeys.has(rootPath);
              const rootSelected = value === rootPath;
              const chain = resolveRemoteChain(remote.name, remoteList);
              const needsSubdir =
                isContainerRequiredBackend(chain.leafBackend) &&
                !chain.accumulatedPrefix;
              const isMkdirHere = mkdirTargetRemote === remote.name;
              const mkdirParent = getMkdirParentForRemote(remote);

              return (
                <div key={remote.name} className="wizard-remote-group">
                  <div
                    className={`wizard-tree-row is-remote-root${rootSelected ? " is-selected" : ""}`}
                  >
                    <button
                      type="button"
                      className="wizard-tree-expand"
                      aria-label={`${expanded ? "折叠" : "展开"} ${remote.name}`}
                      aria-expanded={expanded}
                      onClick={() => handleToggleRemoteOrFolder(rootPath)}
                    >
                      {expanded ? (
                        <IconChevronDown size="small" aria-hidden="true" />
                      ) : (
                        <IconChevronRight size="small" aria-hidden="true" />
                      )}
                    </button>
                    <button
                      type="button"
                      className="wizard-tree-select"
                      data-remote-name={remote.name}
                      aria-pressed={rootSelected}
                      onClick={() => {
                        if (!expanded) {
                          setExpandedKeys((prev) => {
                            const next = new Set(prev);
                            next.add(rootPath);
                            return next;
                          });
                          void loadFolderChildren(rootPath);
                        }
                        if (!needsSubdir) {
                          onChange(rootPath);
                        }
                      }}
                    >
                      {chain.hasCrypt ? (
                        <IconLockStroked size="small" aria-hidden="true" />
                      ) : (
                        <IconCloudStroked size="small" aria-hidden="true" />
                      )}
                      <span className="wizard-tree-label">{remote.name}</span>
                      <Tag size="small" type="light" color={chain.hasCrypt ? "green" : "grey"}>
                        {formatRemoteTypeBadge(remote.type)}
                      </Tag>
                    </button>
                    <Button
                      size="small"
                      type="tertiary"
                      className="wizard-mkdir-trigger"
                      onClick={() =>
                        isMkdirHere
                          ? setMkdirTargetRemote(null)
                          : handleOpenMkdir(remote)
                      }
                    >
                      新建文件夹
                    </Button>
                  </div>

                  {isMkdirHere && (
                    <div className="wizard-mkdir-inline">
                      <div className="wizard-mkdir-parent muted">
                        所在目录：{mkdirParent}
                      </div>
                      <div className="wizard-mkdir-input-row">
                        <Input
                          size="small"
                          aria-label="新建云端文件夹名称"
                          placeholder="输入新文件夹名称"
                          value={newFolderName}
                          onChange={(v) => {
                            setNewFolderName(v);
                            if (mkdirError) setMkdirError("");
                          }}
                          onEnterPress={() => void handleConfirmMkdir(remote)}
                        />
                        <Button
                          size="small"
                          type="primary"
                          loading={mkdirBusy}
                          onClick={() => void handleConfirmMkdir(remote)}
                        >
                          创建
                        </Button>
                        <Button
                          size="small"
                          type="tertiary"
                          disabled={mkdirBusy}
                          onClick={() => setMkdirTargetRemote(null)}
                        >
                          取消
                        </Button>
                      </div>
                      {mkdirError && (
                        <div className="field-error" role="alert">
                          {mkdirError}
                        </div>
                      )}
                    </div>
                  )}

                  {expanded && (
                    <div className="wizard-remote-children">
                      {needsSubdir &&
                        selectedMeta.remoteName.toLowerCase() !==
                          remote.name.toLowerCase() && (
                          <div className="wizard-tree-status">
                            请选择或新建存储桶 / 子目录
                          </div>
                        )}
                      <SubfolderRows
                        parentPath={rootPath}
                        depth={1}
                        selectedPath={value}
                        expandedKeys={expandedKeys}
                        childrenMap={childrenMap}
                        loadingMap={loadingMap}
                        errorMap={errorMap}
                        onToggleExpand={handleToggleRemoteOrFolder}
                        onSelectPath={onChange}
                        onRetryFolder={(p) => void loadFolderChildren(p, true)}
                      />
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>

      {remoteList.length > 0 && (
        <div className="wizard-remote-footer">
          <button
            type="button"
            className="wizard-inline-link"
            onClick={() => setManualOpen((o) => !o)}
          >
            {manualOpen ? "收起手动路径输入" : "目录未创建或无列目录权限？手动输入云端路径"}
          </button>
          {manualOpen && (
            <div className="wizard-manual-remote-row">
              <Input
                size="small"
                aria-label="手动输入云端路径"
                placeholder="/存储名称/存储桶/子目录"
                value={manualSubPath}
                onChange={setManualSubPath}
                onEnterPress={applyManualPath}
              />
              <Button size="small" theme="light" onClick={applyManualPath}>
                确定
              </Button>
            </div>
          )}
        </div>
      )}

      {error && (
        <div className="field-error wizard-picker-error" role="alert">
          {error}
        </div>
      )}
    </section>
  );
}
