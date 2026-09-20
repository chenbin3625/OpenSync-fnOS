import { useCallback, useEffect, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import TreeSelect from "@douyinfe/semi-ui/lib/es/treeSelect";
import { IconRefresh } from "@douyinfe/semi-icons";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { api } from "../api/client";
import { buildPathTreeData } from "../pages/Home/homeUtils";

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
function update(
  nodes: TreeNodeData[],
  key: string,
  children: TreeNodeData[],
): TreeNodeData[] {
  return nodes.map((n) =>
    n.key === key
      ? { ...n, children, isLeaf: children.length === 0 }
      : {
          ...n,
          ...(n.children
            ? { children: update(n.children, key, children) }
            : {}),
        },
  );
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
  const [nodes, setNodes] = useState<TreeNodeData[]>(() => seed(value));
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const [loadFailed, setLoadFailed] = useState(false);
  const controller = useRef<AbortController | null>(null);
  const engineRef = useRef(engineId);
  const nodesRef = useRef(nodes);
  engineRef.current = engineId;
  nodesRef.current = nodes;

  const load = useCallback(
    async (node?: TreeNodeData) => {
      if (!engineId) return;
      const signal = controller.current?.signal;
      const path = String(node?.value || "/");
      try {
        const result = await api.paths(engineId, path, signal);
        if (signal?.aborted || engineRef.current !== engineId) return;
        setLoadFailed(false);
        const children = (result || []).map((n) => {
          const name = n.name || n.path || "";
          const full = path === "/" ? "/" + name : path + "/" + name;
          return { label: name, value: full, key: full, isLeaf: false };
        });
        setNodes((prev) => update(prev, path, children));
        // 根目录加载完成后，只展开根节点（显示一级目录）
        if (path === "/") {
          setExpandedKeys(["/"]);
        }
      } catch (err) {
        if (!signal?.aborted && engineRef.current === engineId) {
          const message = err instanceof Error ? err.message : "目录加载失败";
          setLoadFailed(true);
          Toast.error({ content: message, duration: 5 });
        }
      }
    },
    [engineId],
  );

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
    controller.current = new AbortController();
    setNodes(seed(value));
    setExpandedKeys([]);
    if (engineId) void load();
    return () => controller.current?.abort();
  }, [engineId, load]);

  return (
    <div id={id} className="remote-paths">
      <TreeSelect
        aria-label="选择引擎目录"
        treeData={nodes}
        value={multiple ? value : value[0]}
        multiple={multiple}
        treeNodeLabelProp="value"
        checkRelation="unRelated"
        disabled={!engineId}
        filterTreeNode
        searchPlaceholder="搜索已加载目录"
        placeholder="选择目录"
        loadData={load}
        expandedKeys={expandedKeys}
        onExpand={(keys) => setExpandedKeys(keys as string[])}
        onChange={handleChange}
        dropdownStyle={{ maxWidth: "calc(100vw - 32px)" }}
        maxTagCount={2}
        showClear
      />
      {loadFailed && (
        <Button
          size="small"
          icon={<IconRefresh aria-hidden="true" />}
          onClick={() => void load()}
          style={{ marginTop: 8 }}
        >
          重试
        </Button>
      )}
    </div>
  );
}
