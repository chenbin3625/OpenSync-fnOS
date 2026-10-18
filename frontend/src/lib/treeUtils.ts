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

/**
 * 递归更新树中指定节点的 children。
 *
 * 同名子节点保留已有的子树：父级重新加载（首次加载慢于用户展开、强制刷新）
 * 时，不能把已经展开加载过的下级目录冲掉，否则 Tree 仍认为它们已加载，
 * 不会再请求，表现为节点展开却是空的。
 */
export function patchTreeNodes(
  nodes: TreeNodeData[],
  key: string,
  children: TreeNodeData[],
): TreeNodeData[] {
  return nodes.map((n) => {
    if (n.key === key) {
      const previous = new Map(
        (n.children || []).map((child) => [child.key, child]),
      );
      const merged = children.map((child) => {
        const kept = previous.get(child.key)?.children;
        return kept ? { ...child, children: kept } : child;
      });
      return { ...n, children: merged, isLeaf: merged.length === 0 };
    }
    return n.children
      ? { ...n, children: patchTreeNodes(n.children, key, children) }
      : n;
  });
}

/** 收集树中所有节点 key */
export function collectTreeKeys(nodes: TreeNodeData[]): Set<string> {
  const keys = new Set<string>();
  const walk = (list: TreeNodeData[]) => {
    for (const n of list) {
      keys.add(String(n.key));
      if (n.children) walk(n.children);
    }
  };
  walk(nodes);
  return keys;
}

/** 按层级从浅到深排序，刷新时父级先于子级加载 */
export function sortPathsByDepth(paths: Iterable<string>): string[] {
  const depth = (p: string) => (p === "/" ? 0 : p.split("/").length - 1);
  return [...paths].sort((a, b) => depth(a) - depth(b) || a.localeCompare(b));
}

/** 拆分路径为「父路径」和「最后一级名称」，用于选择框里突出显示目录名 */
export function splitDirPath(path: string): { parent: string; name: string } {
  const clean = path.length > 1 ? path.replace(/\/+$/, "") : path;
  if (!clean || clean === "/") return { parent: "", name: "/" };
  const index = clean.lastIndexOf("/");
  return {
    parent: index <= 0 ? "/" : clean.slice(0, index + 1),
    name: clean.slice(index + 1),
  };
}
