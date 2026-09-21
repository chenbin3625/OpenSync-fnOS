import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";

/** 将 API 返回的子目录列表转为 TreeNodeData */
export function toTreeChildren(
  parentPath: string,
  items: { name?: string; path?: string }[],
): TreeNodeData[] {
  return (items || []).map((n) => {
    const name = n.name || n.path || "";
    const full =
      parentPath === "/" ? "/" + name : parentPath + "/" + name;
    return { label: name, value: full, key: full, isLeaf: false };
  });
}

/** 递归更新树中指定节点的 children */
export function patchTreeNodes(
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
            ? { children: patchTreeNodes(n.children, key, children) }
            : {}),
        },
  );
}
