import { useCallback, useEffect, useState, type ReactNode } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Highlight from "@douyinfe/semi-ui/lib/es/highlight";
import TreeSelect, {
  type RenderSelectedItem,
} from "@douyinfe/semi-ui/lib/es/treeSelect";
import { IconRefresh } from "@douyinfe/semi-icons";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { buildPathTreeData } from "../pages/Home/homeUtils";
import { useAsyncTree } from "../lib/useAsyncTree";
import { splitDirPath } from "../lib/treeUtils";

function seed(paths: string[]): TreeNodeData[] {
  const convert = (
    nodes: ReturnType<typeof buildPathTreeData>,
  ): TreeNodeData[] =>
    nodes.map((n) => ({
      label: n.title,
      value: n.value,
      key: n.key,
      isLeaf: false,
      ...(n.children?.length ? { children: convert(n.children) } : {}),
    }));
  return convert(buildPathTreeData(paths));
}

/** 选择框内的路径：父路径弱化并可从左侧省略，最后一级目录名始终完整可见 */
function SelectedPath({ path }: { path: string }) {
  const { parent, name } = splitDirPath(path);
  return (
    <span className="remote-paths-selected" title={path}>
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

export function RemotePaths({
  id,
  engineId,
  value,
  onChange,
  // 目录选择器当前都是单选；保留 multiple 以便将来多选，但不再有"选父自动选子"
  // 的隐藏级联（没有任何调用方使用，曾经是死代码）。
  multiple = false,
  ariaLabel = "选择引擎目录",
  "aria-labelledby": ariaLabelledBy,
}: {
  id?: string;
  engineId?: number;
  value: string[];
  onChange: (paths: string[]) => void;
  multiple?: boolean;
  /** 同一表单中有多个目录选择框时，用不同名称让读屏用户区分 */
  ariaLabel?: string;
  "aria-labelledby"?: string;
}) {
  const [loadFailed, setLoadFailed] = useState(false);

  const onLoaded = useCallback(
    ({ path }: { path: string; children: TreeNodeData[] }) => {
      setLoadFailed(false);
      // 根目录加载完成后，只展开根节点（显示一级目录）
      if (path === "/") return ["/"];
    },
    [],
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
    resetTree(seed(value), []);
    if (engineId) void loadData();
  }, [engineId, resetTree, loadData]);

  // 节点名不换行，过长时可横向滚动；悬停显示完整路径
  const renderLabel = useCallback(
    (label?: ReactNode, node?: TreeNodeData, keyword?: string) => (
      <span className="remote-paths-node" title={String(node?.key ?? "")}>
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
    [],
  );

  const renderSelectedItem: RenderSelectedItem = multiple
    ? (node: TreeNodeData) => ({
        isRenderInTag: true,
        content: <SelectedPath path={String(node.value ?? node.key)} />,
      })
    : (node: TreeNodeData) => (
        <SelectedPath path={String(node.value ?? node.key)} />
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
        aria-label={ariaLabel}
        aria-labelledby={ariaLabelledBy}
        treeData={treeData}
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
        showClear
      />
      {loadFailed && (
        <Button
          className="remote-paths-retry"
          size="small"
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
