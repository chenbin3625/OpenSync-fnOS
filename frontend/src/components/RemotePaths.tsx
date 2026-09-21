import { useCallback, useEffect, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import TreeSelect from "@douyinfe/semi-ui/lib/es/treeSelect";
import { IconRefresh } from "@douyinfe/semi-icons";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { buildPathTreeData } from "../pages/Home/homeUtils";
import { useAsyncTree } from "../lib/useAsyncTree";

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

export function RemotePaths({
  id,
  engineId,
  value,
  onChange,
  multiple = true,
}: {
  id?: string;
  engineId?: number;
  value: string[];
  onChange: (paths: string[]) => void;
  multiple?: boolean;
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
    loadData,
    resetTree,
  } = useAsyncTree({ engineId, onLoaded, onError, errorFallback: "目录加载失败" });

  const nodesRef = useRef(treeData);
  nodesRef.current = treeData;

  // 在树中查找节点
  const findNode = useCallback(
    (key: string, tree: TreeNodeData[]): TreeNodeData | undefined => {
      for (const n of tree) {
        if (n.key === key) return n;
        if (n.children) {
          const found = findNode(key, n.children);
          if (found) return found;
        }
      }
    },
    [],
  );

  // 选中二级节点时，自动选中其已加载的三级子节点
  const handleChange = useCallback(
    (next: unknown) => {
      const selected = (Array.isArray(next) ? next : next ? [next] : []).map(String);
      if (!multiple) {
        onChange(selected);
        return;
      }
      // 找出新增的选中项
      const added = selected.filter((v) => !value.includes(v));
      const extra: string[] = [];
      for (const key of added) {
        const node = findNode(key, nodesRef.current);
        if (node?.children) {
          for (const child of node.children) {
            const childKey = String(child.key);
            if (!selected.includes(childKey) && !extra.includes(childKey)) {
              extra.push(childKey);
            }
          }
        }
      }
      onChange([...selected, ...extra]);
    },
    [value, multiple, onChange, findNode],
  );

  useEffect(() => {
    resetTree(seed(value), []);
    if (engineId) void loadData();
  }, [engineId, resetTree, loadData]);

  return (
    <div id={id} className="remote-paths">
      <TreeSelect
        aria-label="选择引擎目录"
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
        expandedKeys={expandedKeys}
        onExpand={(keys) => setExpandedKeys(keys as string[])}
        onChange={handleChange}
        dropdownStyle={{ maxWidth: "calc(100vw - 32px)" }}
        maxTagCount={2}
        showClear
      />
      {loadFailed && (
        <Button
          className="remote-paths-retry"
          size="small"
          icon={<IconRefresh aria-hidden="true" />}
          onClick={() => void loadData()}
        >
          重试
        </Button>
      )}
    </div>
  );
}
