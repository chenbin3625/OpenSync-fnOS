export function normalizeExcludeRootPath(path: string): string {
  const normalized = String(path || "").trim().replace(/\/+$/, "");
  return normalized || "/";
}

export function initialExcludeExpandedKeys(srcPaths: string[]): string[] {
  return Array.from(new Set(srcPaths.map(normalizeExcludeRootPath)));
}
