import { useCallback, useEffect, useRef, useState } from "react";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { api } from "../api/client";
import { errorToast } from "../components/common";
import { toTreeChildren, patchTreeNodes } from "./treeUtils";

export interface UseAsyncTreeOptions {
  /** 引擎 ID，变化时重置树 */
  engineId?: number;
  /** 加载完成后的回调，可用于自动展开子节点等 */
  onLoaded?: (event: {
    path: string;
    children: TreeNodeData[];
  }) => string[] | void;
  /** 加载出错后的回调 */
  onError?: (err: unknown) => void;
  /** 错误提示的 fallback 文案 */
  errorFallback?: string;
}

export function useAsyncTree({
  engineId,
  onLoaded,
  onError,
  errorFallback = "目录加载失败",
}: UseAsyncTreeOptions) {
  const [treeData, setTreeData] = useState<TreeNodeData[]>([]);
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const controllerRef = useRef<AbortController | null>(null);
  const engineRef = useRef(engineId);
  engineRef.current = engineId;

  const loadChildren = useCallback(
    async (
      path: string,
      eid?: number,
      signal?: AbortSignal,
    ): Promise<TreeNodeData[] | undefined> => {
      const id = eid ?? engineId;
      if (!id) return;
      try {
        const result = await api.paths(id, path, signal);
        if (signal?.aborted || engineRef.current !== id) return;
        const children = toTreeChildren(path, result || []);
        setTreeData((prev) => patchTreeNodes(prev, path, children));
        const extraKeys = onLoaded?.({ path, children });
        if (extraKeys?.length) {
          setExpandedKeys((prev) =>
            Array.from(new Set([...prev, ...extraKeys])),
          );
        }
        return children;
      } catch (err) {
        if (signal?.aborted || engineRef.current !== id) return;
        onError?.(err);
        errorToast(err, errorFallback);
      }
    },
    [engineId, onLoaded, onError, errorFallback],
  );

  const loadData = useCallback(
    async (node?: TreeNodeData) => {
      const path = node ? String(node.key) : "/";
      await loadChildren(
        path,
        engineId,
        controllerRef.current?.signal,
      );
    },
    [engineId, loadChildren],
  );

  const resetTree = useCallback(
    (roots: TreeNodeData[], initialExpanded: string[]) => {
      controllerRef.current?.abort();
      controllerRef.current = new AbortController();
      setTreeData(roots);
      setExpandedKeys(initialExpanded);
      return controllerRef.current;
    },
    [],
  );

  useEffect(() => {
    return () => controllerRef.current?.abort();
  }, []);

  return {
    treeData,
    expandedKeys,
    setExpandedKeys,
    loadChildren,
    loadData,
    resetTree,
  };
}
