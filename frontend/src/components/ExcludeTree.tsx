import { useCallback, useEffect } from "react";
import Tree from "@douyinfe/semi-ui/lib/es/tree";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import {
  initialExcludeExpandedKeys,
  normalizeExcludeRootPath,
} from "../lib/excludeTree";
import { useAsyncTree } from "../lib/useAsyncTree";

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
  const {
    treeData,
    expandedKeys,
    setExpandedKeys,
    loadChildren,
    loadData,
    resetTree,
  } = useAsyncTree({ engineId, errorFallback: "目录加载失败" });

  // 根据 srcPaths 初始化根节点
  useEffect(() => {
    if (!engineId || !srcPaths.length) {
      resetTree([], []);
      return;
    }

    const rootKeys = initialExcludeExpandedKeys(srcPaths);
    const roots: TreeNodeData[] = rootKeys.map((p) => ({
      label: p,
      value: p,
      key: p,
      disabled: true,
      isLeaf: false,
    }));
    const controller = resetTree(roots, rootKeys);

    // 自动加载每个根节点的子目录（仅一级）
    for (const sp of rootKeys) {
      void loadChildren(sp, engineId, controller.signal);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [engineId, srcPaths.join(",")]);

  // 将 excludedPaths 相对路径转换为绝对 key 用于勾选
  const checkedKeys = excludedPaths.flatMap((rel) => {
    const clean = rel.replace(/^\/+/, "").replace(/\/+$/, "");
    if (!clean) return [];
    return srcPaths.map((sp) => {
      const root = normalizeExcludeRootPath(sp);
      return root === "/" ? "/" + clean : root + "/" + clean;
    });
  });

  const handleCheck = useCallback(
    (keys: unknown) => {
      const checked = (Array.isArray(keys) ? keys : []).map(String);
      // 将绝对路径转为相对路径去重
      const relSet = new Set<string>();
      for (const abs of checked) {
        for (const sp of srcPaths) {
          const root = normalizeExcludeRootPath(sp);
          if (abs === root) continue;
          if (
            (root === "/" && abs.startsWith("/")) ||
            abs.startsWith(root + "/")
          ) {
            relSet.add(toRelative(sp, abs));
            break;
          }
        }
      }
      // 去重：父目录已选中时移除子目录路径，只保留最顶层
      const sorted = Array.from(relSet).sort();
      const deduped: string[] = [];
      for (const p of sorted) {
        if (!deduped.some((ancestor) => p.startsWith(ancestor))) {
          deduped.push(p);
        }
      }
      onChange(deduped);
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
        checkRelation="related"
        value={checkedKeys}
        onChange={handleCheck}
        loadData={loadData}
        expandedKeys={expandedKeys}
        onExpand={(keys) => setExpandedKeys(keys as string[])}
        filterTreeNode
        searchPlaceholder="搜索已加载目录"
        emptyContent="暂无子目录"
      />
    </div>
  );
}
