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

function isPathWithin(key: string, dir: string): boolean {
  if (dir === "/" || key === dir) return true;
  return key.startsWith(dir.replace(/\/+$/, "") + "/");
}

/**
 * 刷新目录树：根节点和所有已展开目录按层级从浅到深重新加载，返回刷新后仍应保持展开的 key。
 *
 * - 只有父级刷新后仍存在的目录才继续请求，已在引擎上删除的目录不再请求，避免一串报错。
 * - load 返回 undefined 表示加载失败：此时无法确认它下面的目录是否还在，
 *   保留这些展开状态，不让一次失败把整棵树折叠。
 * - roots 是树的真实根节点（目录选择器为 "/"，文件夹过滤为各个源目录）。
 * - aborted 返回 true 时立即停止，返回 null，调用方不应再改动展开状态。
 */
export async function refreshTreePaths(
  roots: string[],
  expanded: string[],
  load: (path: string) => Promise<TreeNodeData[] | undefined>,
  aborted: () => boolean = () => false,
): Promise<string[] | null> {
  const present = new Set(roots);
  const failed: string[] = [];
  const underFailed = (key: string) => failed.some((dir) => isPathWithin(key, dir));
  for (const path of sortPathsByDepth(new Set([...roots, ...expanded]))) {
    if (aborted()) return null;
    if (!present.has(path) || underFailed(path)) continue;
    const children = await load(path);
    if (aborted()) return null;
    if (!children) {
      failed.push(path);
      continue;
    }
    children.forEach((child) => present.add(String(child.key)));
  }
  return expanded.filter((key) => present.has(key) || underFailed(key));
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
