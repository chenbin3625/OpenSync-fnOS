import { useCallback, useEffect, useRef, useState } from "react";
import Banner from "@douyinfe/semi-ui/lib/es/banner";
import Button from "@douyinfe/semi-ui/lib/es/button";
import TreeSelect from "@douyinfe/semi-ui/lib/es/treeSelect";
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
  const [error, setError] = useState("");
  const controller = useRef<AbortController | null>(null);
  const engineRef = useRef(engineId);
  engineRef.current = engineId;
  const load = useCallback(
    async (node?: TreeNodeData) => {
      if (!engineId) return;
      const signal = controller.current?.signal;
      const path = String(node?.value || "/");
      setError("");
      try {
        const result = await api.paths(engineId, path, signal);
        if (signal?.aborted || engineRef.current !== engineId) return;
        const children = (result || []).map((n) => {
          const name = n.name || n.path || "";
          const full = path === "/" ? "/" + name : path + "/" + name;
          return { label: name, value: full, key: full, isLeaf: false };
        });
        setNodes((prev) => update(prev, path, children));
      } catch (err) {
        if (!signal?.aborted && engineRef.current === engineId)
          setError(err instanceof Error ? err.message : "目录加载失败");
      }
    },
    [engineId],
  );
  useEffect(() => {
    controller.current = new AbortController();
    setNodes(seed(value));
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
        checkRelation="unRelated"
        disabled={!engineId}
        filterTreeNode
        searchPlaceholder="搜索已加载目录"
        placeholder="选择目录"
        loadData={load}
        onChange={(next) =>
          onChange(
            (Array.isArray(next) ? next : next ? [next] : []).map(String),
          )
        }
        style={{ width: "100%" }}
        dropdownStyle={{ maxWidth: "calc(100vw - 32px)" }}
        maxTagCount={2}
        showClear
      />
      {error && (
        <Banner
          type="danger"
          closeIcon={null}
          description={
            <span>
              {error}{" "}
              <Button size="small" onClick={() => void load()}>
                重试
              </Button>
            </span>
          }
        />
      )}
    </div>
  );
}
