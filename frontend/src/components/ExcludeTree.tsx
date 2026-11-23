import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Button from "@douyinfe/semi-ui/lib/es/button";
import Tree from "@douyinfe/semi-ui/lib/es/tree";
import type { TreeNodeData } from "@douyinfe/semi-ui/lib/es/tree/interface";
import { IconRefresh } from "@douyinfe/semi-icons";
import {
  applyExcludeTreeCheck,
  buildExcludeCheckedKeys,
  initialExcludeExpandedKeys,
} from "../lib/excludeTree";
import { useAsyncTree } from "../lib/useAsyncTree";

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
  const lastClickedKeyRef = useRef<string | null>(null);
  const srcKey = srcPaths.join(",");
  // 刷新/重试必须从源目录根节点开始；从 "/" 开始会把已展开的子目录全部当成已删除而折叠
  const rootKeys = useMemo(
    () => initialExcludeExpandedKeys(srcPaths),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [srcKey],
  );
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
  } = useAsyncTree({
    engineId,
    onLoaded,
    onError,
    errorFallback: "目录加载失败",
    rootKeys,
  });

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
  const checkedKeys = useMemo(
    () => buildExcludeCheckedKeys(srcPaths, excludedPaths),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [srcKey, excludedPaths],
  );

  const handleCheck = useCallback(
    (keys: unknown) => {
      const checked = (Array.isArray(keys) ? keys : []).map(String);
      const clickedKey = lastClickedKeyRef.current;
      lastClickedKeyRef.current = null;
      onChange(
        applyExcludeTreeCheck(srcPaths, excludedPaths, checked, clickedKey),
      );
    },
    [srcPaths, excludedPaths, onChange],
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
          autoMergeValue={false}
          value={checkedKeys}
          onSelect={(selectedKey) => {
            lastClickedKeyRef.current = String(selectedKey);
          }}
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
          {loadFailed ? "重试" : "刷新目录"}
        </Button>
      </div>
    </div>
  );
}
