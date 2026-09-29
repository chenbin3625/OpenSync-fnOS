import { useEffect } from "react";

/** Largest valid page for `total` rows; an empty list still has page 1. */
export function maxPage(total: number, pageSize: number): number {
  if (!(pageSize > 0) || !(total > 0)) return 1;
  return Math.max(1, Math.ceil(total / pageSize));
}

/** Pulls `page` back into [1, maxPage] once the row count is known. */
export function clampPage(page: number, total: number, pageSize: number) {
  return Math.min(Math.max(1, Math.trunc(page) || 1), maxPage(total, pageSize));
}

/**
 * When rows disappear (deleted records, a running task draining a status tab)
 * the current page can end up past the last one and the table renders empty
 * while the pager still says there is data. `total` must be `null` while it is
 * unknown (loading), otherwise the 0 placeholder would snap every page to 1.
 */
export function useClampedPage(
  page: number,
  total: number | null,
  pageSize: number,
  setPage: (page: number) => void,
) {
  useEffect(() => {
    if (total === null) return;
    const next = clampPage(page, total, pageSize);
    if (next !== page) setPage(next);
  }, [page, total, pageSize, setPage]);
}
