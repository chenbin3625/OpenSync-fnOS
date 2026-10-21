import { useCallback, useEffect, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Tree from "@douyinfe/semi-ui/lib/es/tree";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { IconRefresh } from "@douyinfe/semi-icons";
import {
  initialExcludeExpandedKeys,
  normalizeExcludeRootPath,
} from "../lib/excludeTree";
import { useAsyncTree } from "../lib/useAsyncTree";

/**
 * 将绝对路径转为相对于某个 srcPath 的目录过滤路径。
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
  id,
  engineId,
  srcPaths,
  excludedPaths,
  onChange,
  "aria-label": ariaLabel,
  "aria-labelledby": ariaLabelledBy,
}: {
  id?: string;
  engineId?: number;
  srcPaths: string[];
  excludedPaths: string[];
  onChange: (paths: string[]) => void;
  "aria-label"?: string;
  "aria-labelledby"?: string;
}) {
  const [loadFailed, setLoadFailed] = useState(false);
  const onLoaded = useCallback(() => setLoadFailed(false), []);
  const onError = useCallback(() => setLoadFailed(true), []);
  const {
    treeData,
    expandedKeys,
    setExpandedKeys,
    // Controlled loaded keys: Semi marks a node loaded even when loadData
    // fails, so without this a failed directory can never be retried.
    loadedKeys,
    refreshing,
    loadChildren,
    loadData,
    refresh,
    resetTree,
  } = useAsyncTree({ engineId, onLoaded, onError, errorFallback: "目录加载失败" });

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
      <div
        id={id}
        className="exclude-tree-empty muted"
        role="group"
        aria-label={ariaLabel}
        aria-labelledby={ariaLabelledBy}
      >
        请先选择引擎和源目录
      </div>
    );
  }

  return (
    <div
      id={id}
      className="exclude-tree-field"
      role="group"
      aria-label={ariaLabel}
      aria-labelledby={ariaLabelledBy}
    >
      <div className="exclude-tree">
        <Tree
          treeData={treeData}
          multiple
          checkRelation="related"
          value={checkedKeys}
          onChange={handleCheck}
          loadData={loadData}
          // 受控 loadedKeys，让加载失败的节点可以重新请求；与 RemotePaths 一致
          loadedKeys={loadedKeys}
          // 异步返回的子目录在展开动画期间可能不渲染（见 RemotePaths 的注释）
          motion={false}
          expandedKeys={expandedKeys}
          onExpand={(keys) => setExpandedKeys(keys as string[])}
          filterTreeNode
          searchPlaceholder="搜索已加载目录"
          emptyContent="暂无子目录"
        />
      </div>
      <div className="exclude-tree-toolbar">
        <Button
          size="small"
          theme="borderless"
          icon={<IconRefresh aria-hidden="true" />}
          loading={refreshing}
          disabled={!engineId}
          onClick={() => void refresh()}
        >
          刷新目录
        </Button>
        {loadFailed && (
          <Button
            size="small"
            theme="borderless"
            icon={<IconRefresh aria-hidden="true" />}
            loading={refreshing}
            onClick={() => void refresh()}
          >
            重试加载
          </Button>
        )}
      </div>
    </div>
  );
}
