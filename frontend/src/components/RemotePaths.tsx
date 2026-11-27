import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Highlight from "@douyinfe/semi-ui/lib/es/highlight";
import Input from "@douyinfe/semi-ui/lib/es/input";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import TreeSelect, {
  type RenderSelectedItem,
  type RenderSelectedItemInMultiple,
} from "@douyinfe/semi-ui/lib/es/treeSelect";
import {
  IconFolderStroked,
  IconPlusStroked,
  IconRefresh,
} from "@douyinfe/semi-icons";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { api } from "../api/client";
import { getErrorMessage } from "./common";
import { buildPathTreeData } from "../pages/Home/homeUtils";
import { formatDisplayPath } from "../lib/syncPathPreview";
import {
  buildCreatedFolderPlan,
  canCreateFolderUnderPath,
  isPathDisabledInPicker,
  mergeCreatedFolderSelection,
} from "../lib/taskForm";
import { useAsyncTree } from "../lib/useAsyncTree";
import { collectTreeKeys, splitDirPath } from "../lib/treeUtils";
import type { NativeRemoteItem } from "../types";

function seed(paths: string[]): TreeNodeData[] {
  const convert = (
    nodes: ReturnType<typeof buildPathTreeData>,
  ): TreeNodeData[] =>
    nodes.map((n) => {
      const val = String(n.value ?? "");
      const label =
        val && val !== "/"
          ? splitDirPath(formatDisplayPath(val, paths)).name
          : n.title;
      return {
        label,
        value: n.value,
        key: n.key,
        isLeaf: false,
        ...(n.children?.length ? { children: convert(n.children) } : {}),
      };
    });
  return convert(buildPathTreeData(paths));
}

/** 选择框内的路径：父路径弱化并可从左侧省略，最后一级目录名始终完整可见 */
function SelectedPath({
  path,
  siblingPaths,
}: {
  path: string;
  siblingPaths?: readonly string[];
}) {
  const displayPath = formatDisplayPath(path, siblingPaths);
  const { parent, name } = splitDirPath(displayPath);
  return (
    <span className="remote-paths-selected" title={displayPath}>
      {parent && (
        <span className="remote-paths-selected-parent">
          {/* 外层 rtl 让省略号出现在左侧，内层 ltr 保证斜杠顺序不被重排 */}
          <span dir="ltr">{parent}</span>
        </span>
      )}
      <span className="remote-paths-selected-name">{name}</span>
    </span>
  );
}

function mergeMissingNodes(
  base: TreeNodeData[],
  extra: TreeNodeData[],
): TreeNodeData[] {
  const result = base.map((node) => ({ ...node }));
  for (const item of extra) {
    const idx = result.findIndex((n) => n.key === item.key);
    if (idx === -1) {
      result.push(item);
    } else if (item.children?.length) {
      const existing = result[idx];
      result[idx] = {
        ...existing,
        isLeaf: false,
        children: mergeMissingNodes(existing.children || [], item.children),
      };
    }
  }
  return result;
}

function applyDisabledPickerNodes(
  nodes: TreeNodeData[],
  remotes?: readonly NativeRemoteItem[],
): TreeNodeData[] {
  return nodes.map((node) => {
    const nodePath = String(node.value ?? node.key ?? "");
    const disabled = isPathDisabledInPicker(nodePath, remotes);
    const children = node.children?.length
      ? applyDisabledPickerNodes(node.children, remotes)
      : node.children;
    if (disabled === Boolean(node.disabled) && children === node.children) {
      return node;
    }
    return {
      ...node,
      disabled,
      ...(children ? { children } : {}),
    };
  });
}

export function RemotePaths({
  id,
  engineId,
  value,
  onChange,
  // 多选只负责保留用户明确勾选的目录，不做“选父自动选子”的隐藏级联。
  multiple = false,
  remotes,
  initialExpandedRoot,
  ariaLabel,
  "aria-labelledby": ariaLabelledBy,
}: {
  id?: string;
  engineId?: number;
  value: string[];
  onChange: (paths: string[]) => void;
  multiple?: boolean;
  remotes?: readonly NativeRemoteItem[];
  initialExpandedRoot?: string;
  /** 同一表单中有多个目录选择框时，用不同名称让读屏用户区分 */
  ariaLabel?: string;
  "aria-labelledby"?: string;
}) {
  const [loadFailed, setLoadFailed] = useState(false);
  const [mkdirOpen, setMkdirOpen] = useState(false);
  const [mkdirParent, setMkdirParent] = useState("");
  const [mkdirName, setMkdirName] = useState("");
  const [mkdirBusy, setMkdirBusy] = useState(false);
  const [mkdirError, setMkdirError] = useState("");
  const mkdirInputRef = useRef<HTMLInputElement | null>(null);
  const mountedRef = useRef(true);
  const mkdirEpochRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const onLoaded = useCallback(
    ({ path, children }: { path: string; children: TreeNodeData[] }) => {
      setLoadFailed(false);
      if (path === "/") {
        if (initialExpandedRoot) return ["/", initialExpandedRoot];
        if (children.length === 1 && String(children[0]?.key) === "/本地") {
          return ["/", "/本地"];
        }
        return ["/"];
      }
      if (path === initialExpandedRoot) return [path];
    },
    [initialExpandedRoot],
  );

  const onError = useCallback(() => setLoadFailed(true), []);

  const {
    treeData,
    expandedKeys,
    setExpandedKeys,
    loadedKeys,
    refreshing,
    loadChildren,
    loadData,
    refresh,
    resetTree,
  } = useAsyncTree({ engineId, onLoaded, onError, errorFallback: "目录加载失败" });

  const handleChange = useCallback(
    (next: unknown) => {
      const selected = (Array.isArray(next) ? next : next ? [next] : []).map(String);
      onChange(selected);
    },
    [onChange],
  );

  useEffect(() => {
    mkdirEpochRef.current += 1;
    setMkdirOpen(false);
    setMkdirParent("");
    setMkdirName("");
    setMkdirBusy(false);
    setMkdirError("");
    const controller = resetTree(seed(value), []);
    if (engineId && initialExpandedRoot) {
      void loadData().then(() => {
        if (!controller.signal.aborted) return loadData({ key: initialExpandedRoot });
      });
    } else if (engineId) {
      void loadData().then((children) => {
        if (
          !controller.signal.aborted &&
          Array.isArray(children) &&
          children.length === 1 &&
          String(children[0]?.key) === "/本地"
        ) {
          return loadData({ key: "/本地" });
        }
      });
    }
  }, [engineId, resetTree, loadData, initialExpandedRoot]);

  // Keep selected paths (including newly created folders or unexpanded saved paths)
  // present in treeData so TreeSelect renders and removes their tags reliably,
  // while disabling non-syncable synthetic roots (/, /本地, bucketless object stores).
  const displayTreeData = useMemo(() => {
    const merged = value.length ? mergeMissingNodes(treeData, seed(value)) : treeData;
    return applyDisabledPickerNodes(merged, remotes);
  }, [treeData, value, remotes]);

  const allTreeKeys = useMemo(
    () => Array.from(collectTreeKeys(displayTreeData)),
    [displayTreeData],
  );

  const defaultMkdirParent = useMemo(() => {
    for (let i = value.length - 1; i >= 0; i -= 1) {
      if (canCreateFolderUnderPath(value[i], remotes)) {
        return value[i];
      }
    }
    if (
      initialExpandedRoot &&
      allTreeKeys.includes(initialExpandedRoot) &&
      canCreateFolderUnderPath(initialExpandedRoot, remotes)
    ) {
      return initialExpandedRoot;
    }
    for (let i = expandedKeys.length - 1; i >= 0; i -= 1) {
      const key = expandedKeys[i];
      if (allTreeKeys.includes(key) && canCreateFolderUnderPath(key, remotes)) {
        return key;
      }
    }
    const queue = [...displayTreeData];
    while (queue.length > 0) {
      const current = queue.shift()!;
      const key = String(current.key ?? current.value ?? "");
      if (!current.disabled && canCreateFolderUnderPath(key, remotes)) {
        return key;
      }
      if (current.children?.length) {
        queue.push(...current.children);
      }
    }
    return "";
  }, [
    value,
    remotes,
    initialExpandedRoot,
    allTreeKeys,
    expandedKeys,
    displayTreeData,
  ]);

  const openMkdirPanel = useCallback(
    (parentPath: string) => {
      if (!parentPath || mkdirBusy) return;
      setMkdirParent(parentPath);
      setMkdirError("");
      setMkdirOpen((wasOpen) => {
        if (!wasOpen) setMkdirName("");
        return true;
      });
      window.requestAnimationFrame(() => {
        mkdirInputRef.current?.focus();
      });
    },
    [mkdirBusy],
  );

  const closeMkdirPanel = useCallback(() => {
    if (mkdirBusy) return;
    setMkdirOpen(false);
    setMkdirName("");
    setMkdirError("");
  }, [mkdirBusy]);

  const submitMkdir = useCallback(async () => {
    if (!engineId || !mkdirParent || mkdirBusy) return;
    const plan = buildCreatedFolderPlan(mkdirParent, mkdirName);
    if (plan.error) {
      setMkdirError(plan.error);
      mkdirInputRef.current?.focus();
      return;
    }
    const activeEngine = engineId;
    const epoch = mkdirEpochRef.current;
    setMkdirBusy(true);
    setMkdirError("");
    try {
      await api.mkdirNativeStorage(plan.targetPath);
      if (!mountedRef.current || epoch !== mkdirEpochRef.current) return;
      for (const dir of plan.loadPaths) {
        await loadChildren(dir, activeEngine, undefined, { force: true });
        if (!mountedRef.current || epoch !== mkdirEpochRef.current) return;
      }
      setExpandedKeys((prev) =>
        Array.from(new Set([...prev, ...plan.expandPaths])),
      );
      onChange(mergeCreatedFolderSelection(value, plan.targetPath, multiple));
      Toast.success(`已创建文件夹 ${plan.clean}`);
      setMkdirOpen(false);
      setMkdirName("");
      setMkdirError("");
    } catch (err) {
      if (!mountedRef.current || epoch !== mkdirEpochRef.current) return;
      setMkdirError(getErrorMessage(err, "创建文件夹失败"));
      mkdirInputRef.current?.focus();
    } finally {
      if (mountedRef.current && epoch === mkdirEpochRef.current) {
        setMkdirBusy(false);
      }
    }
  }, [
    engineId,
    mkdirParent,
    mkdirName,
    mkdirBusy,
    loadChildren,
    setExpandedKeys,
    onChange,
    value,
    multiple,
  ]);

  // 节点名不换行，过长时可横向滚动；悬停显示完整路径，并在支持建目录的节点右侧提供快捷新建按钮
  const renderLabel = useCallback(
    (label?: ReactNode, node?: TreeNodeData, keyword?: string) => {
      const nodePath = String(node?.key ?? "");
      const displayPath = formatDisplayPath(nodePath, allTreeKeys);
      const canMkdir =
        Boolean(engineId) &&
        !node?.disabled &&
        canCreateFolderUnderPath(nodePath, remotes);
      const isMkdirTarget = mkdirOpen && mkdirParent === nodePath;
      return (
        <span className="remote-paths-row">
          <span className="remote-paths-node" title={displayPath}>
            {keyword && typeof label === "string" ? (
              <Highlight
                highlightClassName="semi-tree-option-highlight"
                sourceString={label}
                searchWords={[keyword]}
              />
            ) : (
              label
            )}
          </span>
          {canMkdir && (
            <button
              type="button"
              className={`remote-paths-row-mkdir${isMkdirTarget ? " is-active" : ""}`}
              title={`在 ${displayPath} 下新建文件夹`}
              aria-label={`在 ${displayPath} 下新建文件夹`}
              disabled={mkdirBusy}
              onMouseDown={(event) => {
                event.preventDefault();
                event.stopPropagation();
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.stopPropagation();
                }
              }}
              onKeyPress={(event) => event.stopPropagation()}
              onClick={(event) => {
                event.preventDefault();
                event.stopPropagation();
                openMkdirPanel(nodePath);
              }}
            >
              <IconPlusStroked size="small" aria-hidden="true" />
            </button>
          )}
        </span>
      );
    },
    [
      allTreeKeys,
      engineId,
      remotes,
      mkdirOpen,
      mkdirParent,
      mkdirBusy,
      openMkdirPanel,
    ],
  );

  // 使用 Semi 原生浅蓝 Tag（color="blue" type="light"）+ 文件夹图标，
  // “+N” 悬浮层里的标签（挂在 body 下）也能保持一致。
  const renderSelectedTag: RenderSelectedItemInMultiple = (node, { onClose }) => {
    const path = String(node.value ?? node.key);
    const displayPath = formatDisplayPath(path, value);
    return {
      isRenderInTag: false,
      content: (
        <Tag
          key={path}
          className="remote-paths-tag"
          color="blue"
          type="light"
          size="large"
          prefixIcon={<IconFolderStroked size="small" aria-hidden="true" />}
          // 与 Semi 自带标签一致：选择框禁用时不能删除
          closable={Boolean(engineId)}
          onClose={onClose}
          // 标签聚焦后按 Delete / Backspace 可移除，读屏需要知道是哪个目录
          aria-label={`已选目录 ${displayPath}`}
        >
          <SelectedPath path={path} siblingPaths={value} />
        </Tag>
      ),
    };
  };
  const renderSelectedItem: RenderSelectedItem = multiple
    ? renderSelectedTag
    : (node: TreeNodeData) => (
        <SelectedPath
          path={String(node.value ?? node.key)}
          siblingPaths={value}
        />
      );

  const displayMkdirParent = mkdirParent
    ? formatDisplayPath(mkdirParent, allTreeKeys)
    : "";

  const bottomSlot = mkdirOpen ? (
    <div
      className="remote-paths-mkdir-panel"
      onMouseDown={(event) => event.stopPropagation()}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          closeMkdirPanel();
        }
      }}
    >
      <div className="remote-paths-mkdir-header">
        <span className="remote-paths-mkdir-parent" title={displayMkdirParent}>
          新建文件夹于 <strong>{displayMkdirParent}</strong>
        </span>
      </div>
      <div className="remote-paths-mkdir-controls">
        <Input
          ref={mkdirInputRef}
          size="small"
          aria-label="新文件夹名称"
          placeholder="输入文件夹名称（如 Backup）"
          value={mkdirName}
          disabled={mkdirBusy}
          onChange={(nextName) => {
            setMkdirName(nextName);
            if (mkdirError) setMkdirError("");
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              event.stopPropagation();
              void submitMkdir();
            }
          }}
        />
        <div className="remote-paths-mkdir-actions">
          <Button
            size="small"
            type="primary"
            theme="solid"
            loading={mkdirBusy}
            disabled={!mkdirName.trim()}
            onMouseDown={(event) => event.preventDefault()}
            onClick={() => void submitMkdir()}
          >
            创建
          </Button>
          <Button
            size="small"
            type="tertiary"
            disabled={mkdirBusy}
            onMouseDown={(event) => event.preventDefault()}
            onClick={closeMkdirPanel}
          >
            取消
          </Button>
        </div>
      </div>
      {mkdirError && (
        <div className="remote-paths-mkdir-error" role="alert">
          {mkdirError}
        </div>
      )}
    </div>
  ) : (
    <div className="remote-paths-toolbar">
      <Button
        size="small"
        theme="borderless"
        icon={<IconPlusStroked aria-hidden="true" />}
        disabled={!engineId || !defaultMkdirParent}
        // 阻止按下时抢走焦点，避免下拉框随之关闭
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => {
          if (defaultMkdirParent) openMkdirPanel(defaultMkdirParent);
        }}
      >
        新建文件夹
      </Button>
      <Button
        size="small"
        theme="borderless"
        icon={<IconRefresh aria-hidden="true" />}
        loading={refreshing}
        disabled={!engineId}
        // 阻止按下时抢走焦点，避免下拉框随之关闭
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => void refresh()}
      >
        刷新目录
      </Button>
    </div>
  );

  return (
    <div id={id} className="remote-paths">
      <TreeSelect
        aria-label={ariaLabel || (ariaLabelledBy ? undefined : "选择引擎目录")}
        aria-labelledby={ariaLabel ? undefined : ariaLabelledBy}
        treeData={displayTreeData}
        value={multiple ? value : value[0]}
        multiple={multiple}
        treeNodeLabelProp="value"
        checkRelation="unRelated"
        disabled={!engineId}
        filterTreeNode
        searchPlaceholder="搜索已加载目录"
        placeholder="选择目录"
        loadData={loadData}
        loadedKeys={loadedKeys}
        // 展开动画期间若异步加载的子目录返回，Semi 会继续渲染动画缓存的旧列表，
        // 新加载的目录不显示；目录树按需加载，关闭动画
        motion={false}
        expandedKeys={expandedKeys}
        onExpand={(keys) => setExpandedKeys(keys as string[])}
        onVisibleChange={(visible) => {
          if (!visible && !mkdirBusy) {
            setMkdirOpen(false);
            setMkdirError("");
          }
        }}
        onChange={handleChange}
        renderLabel={renderLabel}
        renderSelectedItem={renderSelectedItem}
        outerBottomSlot={bottomSlot}
        dropdownClassName="remote-paths-dropdown"
        dropdownStyle={{ maxWidth: "calc(100vw - 32px)" }}
        maxTagCount={multiple ? 2 : undefined}
        // 超出 2 个时悬停 “+N” 查看其余目录，不必展开目录树去找
        showRestTagsPopover={multiple}
        restTagsPopoverProps={{ className: "remote-paths-rest-popover" }}
        showClear
      />
      {loadFailed && (
        <Button
          className="remote-paths-retry"
          size="small"
          theme="borderless"
          icon={<IconRefresh aria-hidden="true" />}
          loading={refreshing}
          onClick={() => void refresh()}
        >
          重试
        </Button>
      )}
    </div>
  );
}
