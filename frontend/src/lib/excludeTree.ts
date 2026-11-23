export function normalizeExcludeRootPath(path: string): string {
  const normalized = String(path || "").trim().replace(/\/+$/, "");
  return normalized || "/";
}

export function initialExcludeExpandedKeys(srcPaths: string[]): string[] {
  return Array.from(new Set(srcPaths.map(normalizeExcludeRootPath)));
}

/**
 * 将绝对路径转为相对于某个 srcPath 的目录过滤路径。
 * 例如 srcPath="/data", abs="/data/photos/raw" → "photos/raw/"
 */
export function toExcludeRelativePath(srcPath: string, abs: string): string {
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

function toExcludeAbsKey(root: string, cleanRel: string): string {
  return root === "/" ? "/" + cleanRel : root + "/" + cleanRel;
}

function matchesExcludeRoot(root: string, abs: string): boolean {
  if (abs === root) return true;
  return root === "/" ? abs.startsWith("/") : abs.startsWith(root + "/");
}

/**
 * 将相对过滤路径展开为每个源目录下的绝对树节点 key。
 */
export function buildExcludeCheckedKeys(
  srcPaths: string[],
  excludedPaths: string[],
): string[] {
  const roots = initialExcludeExpandedKeys(srcPaths);
  return excludedPaths.flatMap((rel) => {
    const clean = rel.replace(/^\/+/, "").replace(/\/+$/, "");
    if (!clean) return [];
    return roots.map((root) => toExcludeAbsKey(root, clean));
  });
}

/**
 * 根据 Tree 的勾选结果计算去重后的相对过滤路径列表。
 *
 * - 始终跳过禁用根节点本身（配合 autoMergeValue={false}，选中全部一级子节点时不会清空）。
 * - 多源目录场景下，同一相对路径会在每个源目录下生成节点；当用户点击某个源目录树中的节点时，
 *   只从该源目录的勾选集合派生相对路径，避免另一个未操作的源目录把刚取消的规则重新加回。
 * - 若未提供 clickedKey，则通过与 prevExcludedPaths 对比移除在任一源目录下已取消的规则。
 */
export function applyExcludeTreeCheck(
  srcPaths: string[],
  prevExcludedPaths: string[],
  nextCheckedKeys: string[],
  clickedKey?: string | null,
): string[] {
  const roots = initialExcludeExpandedKeys(srcPaths);
  const clickedRoot =
    clickedKey != null
      ? [...roots]
          .sort((a, b) => b.length - a.length)
          .find((root) => matchesExcludeRoot(root, clickedKey))
      : undefined;

  const relSet = new Set<string>();
  if (clickedRoot) {
    for (const abs of nextCheckedKeys) {
      if (abs === clickedRoot) continue;
      if (matchesExcludeRoot(clickedRoot, abs)) {
        relSet.add(toExcludeRelativePath(clickedRoot, abs));
      }
    }
  } else {
    const nextSet = new Set(nextCheckedKeys);
    const removedAbsKeysByRoot = new Map<string, string[]>();
    for (const rel of prevExcludedPaths) {
      const clean = rel.replace(/^\/+/, "").replace(/\/+$/, "");
      if (!clean) continue;
      const missingInSomeRoot = roots.some(
        (root) => !nextSet.has(toExcludeAbsKey(root, clean)),
      );
      if (!missingInSomeRoot) continue;
      for (const root of roots) {
        const key = toExcludeAbsKey(root, clean);
        if (nextSet.has(key)) {
          const list = removedAbsKeysByRoot.get(root) ?? [];
          list.push(key);
          removedAbsKeysByRoot.set(root, list);
        }
      }
    }

    for (const abs of nextCheckedKeys) {
      for (const root of roots) {
        if (abs === root) continue;
        if (!matchesExcludeRoot(root, abs)) continue;
        const removedPrefixes = removedAbsKeysByRoot.get(root);
        if (
          removedPrefixes?.some(
            (prefix) => abs === prefix || abs.startsWith(prefix + "/"),
          )
        ) {
          break;
        }
        relSet.add(toExcludeRelativePath(root, abs));
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
  return deduped;
}
