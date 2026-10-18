import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { api } from "../api/client";
import { errorToast } from "../components/common";
import {
  collectTreeKeys,
  patchTreeNodes,
  sortPathsByDepth,
  toTreeChildren,
} from "./treeUtils";

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
  // 已加载节点由这里管理（受控 loadedKeys），而不是交给 Semi 内部记录：
  // Semi 在 loadData 结束后无论成败都会把节点标记为已加载，且之后永不重新请求，
  // 失败的节点、预置的路径节点和刷新都需要自己决定何时重新加载。
  const [loadedKeys, setLoadedKeys] = useState<Set<string>>(() => new Set());
  const [refreshing, setRefreshing] = useState(false);
  const controllerRef = useRef<AbortController | null>(null);
  const engineRef = useRef(engineId);
  engineRef.current = engineId;
  const expandedRef = useRef(expandedKeys);
  expandedRef.current = expandedKeys;

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
        setLoadedKeys((prev) => new Set(prev).add(path));
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

  /** 强制刷新：重新请求根目录和所有已展开的目录，拿到引擎上的最新子目录 */
  const refresh = useCallback(async () => {
    if (!engineId) return;
    const signal = controllerRef.current?.signal;
    setRefreshing(true);
    setLoadedKeys(new Set());
    try {
      // 父级先于子级加载；只有父级刷新后仍存在的目录才继续刷新，
      // 已在引擎上删除的目录不再请求，避免一串报错。
      const present = new Set(["/"]);
      const targets = sortPathsByDepth(new Set(["/", ...expandedRef.current]));
      for (const path of targets) {
        if (signal?.aborted) return;
        if (!present.has(path)) continue;
        const children = await loadChildren(path, engineId, signal);
        children?.forEach((child) => present.add(String(child.key)));
      }
      if (signal?.aborted) return;
      setExpandedKeys((prev) => prev.filter((key) => present.has(key)));
    } finally {
      if (!signal?.aborted) setRefreshing(false);
    }
  }, [engineId, loadChildren]);

  const resetTree = useCallback(
    (roots: TreeNodeData[], initialExpanded: string[]) => {
      controllerRef.current?.abort();
      controllerRef.current = new AbortController();
      setTreeData(roots);
      setExpandedKeys(initialExpanded);
      setLoadedKeys(new Set());
      setRefreshing(false);
      return controllerRef.current;
    },
    [],
  );

  useEffect(() => {
    return () => controllerRef.current?.abort();
  }, []);

  // 父级重新加载后可能移除了某些子目录，已加载记录只保留树中仍存在的节点
  const loadedKeyList = useMemo(() => {
    const present = collectTreeKeys(treeData);
    return [...loadedKeys].filter((key) => present.has(key));
  }, [treeData, loadedKeys]);

  return {
    treeData,
    expandedKeys,
    setExpandedKeys,
    loadedKeys: loadedKeyList,
    refreshing,
    loadChildren,
    loadData,
    refresh,
    resetTree,
  };
}
