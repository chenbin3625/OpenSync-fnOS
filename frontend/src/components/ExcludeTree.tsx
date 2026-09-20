import { useCallback, useEffect, useRef, useState } from "react";
import Tree from "@douyinfe/semi-ui/lib/es/tree";
import Toast from "@douyinfe/semi-ui/lib/es/toast";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { api } from "../api/client";
import {
  initialExcludeExpandedKeys,
  normalizeExcludeRootPath,
} from "../lib/excludeTree";

/** 将 API 返回的子目录列表转为 TreeNodeData */
function toChildren(
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
function patchTree(
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
            ? { children: patchTree(n.children, key, children) }
            : {}),
        },
  );
}

/**
 * 将绝对路径转为相对于某个 srcPath 的 gitignore 格式路径。
 * 例如 srcPath="/data", abs="/data/photos/raw" → "photos/raw/"
 */
function toRelative(srcPath: string, abs: string): string {
  const root = normalizeExcludeRootPath(srcPath);
  let rel = abs.startsWith(root + "/")
    ? abs.slice(root.length + 1)
    : root === "/"
      ? abs.slice(1)
      : abs.startsWith(root)
      ? abs.slice(root.length)
      : abs;
  rel = rel.replace(/^\/+/, "");
  if (!rel.endsWith("/")) rel += "/";
  return rel;
}

export function ExcludeTree({
  engineId,
  srcPaths,
  excludedPaths,
  onChange,
}: {
  engineId?: number;
  srcPaths: string[];
  excludedPaths: string[];
  onChange: (paths: string[]) => void;
}) {
  const [treeData, setTreeData] = useState<TreeNodeData[]>([]);
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const controllerRef = useRef<AbortController | null>(null);
  const engineRef = useRef(engineId);
  engineRef.current = engineId;

  // 根据 srcPaths 初始化根节点
  useEffect(() => {
    controllerRef.current?.abort();
    controllerRef.current = new AbortController();

    if (!engineId || !srcPaths.length) {
      setTreeData([]);
      setExpandedKeys([]);
      return;
    }

    const rootPaths = initialExcludeExpandedKeys(srcPaths);
    const roots: TreeNodeData[] = rootPaths.map((p) => ({
      label: p,
      value: p,
      key: p,
      isLeaf: false,
    }));
    setTreeData(roots);
    setExpandedKeys(rootPaths);

    // 自动加载每个根节点的子目录
    for (const sp of rootPaths) {
      void loadChildren(sp, engineId, controllerRef.current.signal);
    }

    return () => controllerRef.current?.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [engineId, srcPaths.join(",")]);

  const loadChildren = useCallback(
    async (path: string, eid?: number, signal?: AbortSignal) => {
      const id = eid ?? engineId;
      if (!id) return;
      try {
        const result = await api.paths(id, path, signal);
        if (signal?.aborted || engineRef.current !== id) return;
        const children = toChildren(path, result || []);
        setTreeData((prev) => patchTree(prev, path, children));
      } catch (err) {
        if (!signal?.aborted && engineRef.current === id) {
          Toast.error(
            err instanceof Error ? err.message : "目录加载失败",
          );
        }
      }
    },
    [engineId],
  );

  const handleLoadData = useCallback(
    (node?: TreeNodeData) => {
      if (!node) return Promise.resolve();
      return loadChildren(
        String(node.key),
        engineId,
        controllerRef.current?.signal,
      );
    },
    [engineId, loadChildren],
  );

  // 将 excludedPaths 相对路径转换为绝对 key 用于勾选
  const checkedKeys = excludedPaths.flatMap((rel) =>
    srcPaths.map((sp) => {
      const root = normalizeExcludeRootPath(sp);
      const clean = rel.replace(/^\/+/, "").replace(/\/+$/, "");
      return root === "/" ? "/" + clean : root + "/" + clean;
    }),
  );

  const handleCheck = useCallback(
    (keys: unknown) => {
      const checked = (Array.isArray(keys) ? keys : []).map(String);
      // 将绝对路径转为相对路径去重
      const relSet = new Set<string>();
      for (const abs of checked) {
        for (const sp of srcPaths) {
          const root = normalizeExcludeRootPath(sp);
          if (
            (root === "/" && abs.startsWith("/")) ||
            abs.startsWith(root + "/") ||
            abs === root
          ) {
            relSet.add(toRelative(sp, abs));
            break;
          }
        }
      }
      onChange(Array.from(relSet));
    },
    [srcPaths, onChange],
  );

  if (!engineId || !srcPaths.length) {
    return (
      <div className="exclude-tree-empty muted">
        请先选择引擎和源目录
      </div>
    );
  }

  return (
    <div className="exclude-tree">
      <Tree
        treeData={treeData}
        multiple
        checkRelation="unRelated"
        value={checkedKeys}
        onChange={handleCheck}
        loadData={handleLoadData}
        expandedKeys={expandedKeys}
        onExpand={(keys) => setExpandedKeys(keys as string[])}
        filterTreeNode
        searchPlaceholder="搜索已加载目录"
        emptyContent="暂无子目录"
      />
    </div>
  );
}
