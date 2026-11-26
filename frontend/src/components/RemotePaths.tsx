import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Highlight from "@douyinfe/semi-ui/lib/es/highlight";
import Tag from "@douyinfe/semi-ui/lib/es/tag";
import TreeSelect, {
  type RenderSelectedItem,
  type RenderSelectedItemInMultiple,
} from "@douyinfe/semi-ui/lib/es/treeSelect";
import { IconFolderStroked, IconRefresh } from "@douyinfe/semi-icons";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { buildPathTreeData } from "../pages/Home/homeUtils";
import { formatDisplayPath } from "../lib/syncPathPreview";
import { isPathDisabledInPicker } from "../lib/taskForm";
import { useAsyncTree } from "../lib/useAsyncTree";
import { splitDirPath } from "../lib/treeUtils";
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
  storageRoot,
  ariaLabel,
  "aria-labelledby": ariaLabelledBy,
}: {
  id?: string;
  engineId?: number;
  value: string[];
  onChange: (paths: string[]) => void;
  multiple?: boolean;
  remotes?: readonly NativeRemoteItem[];
  storageRoot?: string;
  /** 同一表单中有多个目录选择框时，用不同名称让读屏用户区分 */
  ariaLabel?: string;
  "aria-labelledby"?: string;
}) {
  const [loadFailed, setLoadFailed] = useState(false);

  const onLoaded = useCallback(
    ({ path }: { path: string; children: TreeNodeData[] }) => {
      setLoadFailed(false);
      // 根目录加载完成后，只展开根节点（显示一级目录）
      if (path === "/") return storageRoot ? ["/", storageRoot] : ["/"];
      if (path === storageRoot) return [path];
    },
    [storageRoot],
  );

  const onError = useCallback(() => setLoadFailed(true), []);

  const {
    treeData,
    expandedKeys,
    setExpandedKeys,
    loadedKeys,
    refreshing,
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
    const controller = resetTree(seed(value), []);
    if (engineId && storageRoot) {
      void loadData().then(() => {
        if (!controller.signal.aborted) return loadData({ key: storageRoot });
      });
    } else if (engineId) {
      void loadData();
    }
  }, [engineId, resetTree, loadData, storageRoot]);

  // Keep manually added paths (e.g. non-existent native target subdirectories)
  // present in treeData so TreeSelect renders and removes their tags reliably,
  // while disabling non-syncable synthetic roots (/, /本地, bucketless object stores).
  const displayTreeData = useMemo(() => {
    const merged = value.length ? mergeMissingNodes(treeData, seed(value)) : treeData;
    const scoped = storageRoot
      ? merged.flatMap((node) =>
          (node.key === "/" ? node.children || [] : [node]).filter(
            (child) => child.key === storageRoot,
          ),
        )
      : merged;
    return applyDisabledPickerNodes(scoped, remotes);
  }, [treeData, value, remotes, storageRoot]);

  // 节点名不换行，过长时可横向滚动；悬停显示完整路径
  const renderLabel = useCallback(
    (label?: ReactNode, node?: TreeNodeData, keyword?: string) => (
      <span
        className="remote-paths-node"
        title={formatDisplayPath(String(node?.key ?? ""), value)}
      >
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
    ),
    [value],
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

  const refreshButton = (
    <div className="remote-paths-toolbar">
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
        onChange={handleChange}
        renderLabel={renderLabel}
        renderSelectedItem={renderSelectedItem}
        outerBottomSlot={refreshButton}
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
