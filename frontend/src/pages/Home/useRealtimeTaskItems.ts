import { useCallback, useEffect, useRef, useState } from "react";
import { jobGetTaskCurrent } from "../../api/job";
import { useResource } from "../../lib/hooks";
import { useClampedPage } from "../../lib/pagination";
import type {
  CurrentTaskView,
  RealtimeTaskItemPage,
  TaskItem,
} from "../../types";
import {
  getRealtimeTaskIdentity,
  normalizeTaskItemPage,
  realtimeTaskPageMatches,
} from "./taskRows";

type RealtimeTaskItemsParams = {
  jobId: string;
  enabled: boolean;
  currentTask: CurrentTaskView | null;
  pageSize: number;
};

type RealtimeTaskPage = {
  key: string;
  rows: TaskItem[];
  total: number;
  /** The reply did not answer this query (stale task identity, other page). */
  mismatched?: boolean;
};

export function useRealtimeTaskItems({
  jobId,
  enabled,
  currentTask,
  pageSize: initialPageSize,
}: RealtimeTaskItemsParams): {
  activeTab: number;
  setActiveTab: (status: number) => void;
  tabTaskList: TaskItem[];
  tabTaskTotal: number;
  tabTaskPage: number;
  setTabTaskPage: (page: number) => void;
  pageSize: number;
  setPageSize: (size: number) => void;
  tabLoading: boolean;
  tabError?: string;
  tableKey: string;
  retryTabTasks: () => void;
} {
  const [activeTab, setActiveTabValue] = useState(1);
  const [tabTaskPage, setTabTaskPageValue] = useState(1);
  const [pageSize, setPageSizeValue] = useState(initialPageSize);
  // 切换状态 Tab 时自增，参与 queryKey，强制重新发起明细请求并重建表格，
  // 避免复用上一个 Tab 的响应或表格内部状态。
  const [refreshToken, setRefreshToken] = useState(0);
  const taskIdentity = currentTask
    ? getRealtimeTaskIdentity(currentTask)
    : "";
  const queryKey = [
    jobId,
    taskIdentity,
    activeTab,
    tabTaskPage,
    pageSize,
    refreshToken,
  ].join(":");
  // A new task reuses the same status tab, but never the old page number:
  // page N of the previous task usually does not exist, so asking for it only
  // produced an empty page that useClampedPage then pulled back to page 1.
  useEffect(() => {
    setTabTaskPageValue(1);
  }, [taskIdentity]);
  const requestIdentity = currentTask
    ? {
        taskId: currentTask.taskId,
        createTime: currentTask.createTime,
        status: activeTab,
        pageNum: tabTaskPage,
        pageSize,
      }
    : undefined;
  const resource = useResource<RealtimeTaskPage | null>(
    async (signal) => {
      if (!enabled || !jobId || !taskIdentity) {
        return { key: queryKey, rows: [], total: 0 };
      }
      const response = await jobGetTaskCurrent(
        {
          id: jobId,
          expectedTaskId: currentTask?.taskId,
          expectedCreateTime: currentTask?.createTime,
          status: activeTab,
          pageSize,
          pageNum: tabTaskPage,
        },
        { signal },
      );
      if (
        response &&
        !Array.isArray(response) &&
        "dataList" in response &&
        !realtimeTaskPageMatches(response as RealtimeTaskItemPage, requestIdentity)
      ) {
        // A stale or mismatched reply (the backend switched task identity
        // under the request) says nothing about this page. Treating it as
        // total=0 cleared the table and clamped the user back to page 1.
        return { key: queryKey, rows: [], total: 0, mismatched: true };
      }
      return {
        key: queryKey,
        ...normalizeTaskItemPage(response, requestIdentity),
      };
    },
    [enabled, queryKey],
    enabled && Boolean(taskIdentity),
  );
  // The last page that answered the current query: kept across a mismatched
  // reply, and across a page change of the same task/tab while it loads, so
  // the table does not blank out and the pager keeps its position.
  const lastGood = useRef<RealtimeTaskPage | null>(null);
  const sameListKey = (key: string) =>
    key.split(":").filter((_, i) => i !== 3).join(":") ===
    queryKey.split(":").filter((_, i) => i !== 3).join(":");
  const fetched = resource.data?.key === queryKey ? resource.data : null;
  if (fetched && !fetched.mismatched) lastGood.current = fetched;
  const fallback =
    lastGood.current && sameListKey(lastGood.current.key) ? lastGood.current : null;
  const currentPage = fetched && !fetched.mismatched ? fetched : null;
  const shownPage = currentPage || fallback;
  // A mismatched reply re-polls; until a matching page arrives the view keeps
  // showing the previous data as "loading".
  const pendingCurrentPage =
    enabled && Boolean(taskIdentity) && !currentPage && !resource.error;

  const setActiveTab = useCallback((status: number) => {
    setActiveTabValue(status);
    setTabTaskPageValue(1);
    setRefreshToken((token) => token + 1);
  }, []);
  const setTabTaskPage = useCallback((page: number) => {
    setTabTaskPageValue(Math.max(1, Math.trunc(page)));
  }, []);
  const setPageSize = useCallback((size: number) => {
    setPageSizeValue(Math.max(1, Math.trunc(size)));
    setTabTaskPageValue(1);
  }, []);
  // Items leave a status tab while the task runs, so the total shrinks under a
  // page the user left open.
  // Only a reply that matched this exact query may clamp the page; a stale
  // one, or the fallback from another page, says nothing about the total.
  useClampedPage(
    tabTaskPage,
    currentPage ? currentPage.total : null,
    pageSize,
    setTabTaskPageValue,
  );
  const retryTabTasks = useCallback(() => {
    void resource.refresh();
  }, [resource.refresh]);
  // A mismatched reply usually means the task identity is being swapped; ask
  // again shortly (silently) instead of waiting for an unrelated change.
  const mismatchedKey = fetched?.mismatched ? fetched.key : null;
  const refreshTabTasks = resource.refresh;
  useEffect(() => {
    if (!mismatchedKey) return;
    const timer = setTimeout(() => void refreshTabTasks(true), 1000);
    return () => clearTimeout(timer);
  }, [mismatchedKey, refreshTabTasks]);

  return {
    activeTab,
    setActiveTab,
    tabTaskList: shownPage?.rows || [],
    tabTaskTotal: shownPage?.total || 0,
    tabTaskPage,
    setTabTaskPage,
    pageSize,
    setPageSize,
    tabLoading: resource.loading || pendingCurrentPage,
    tabError: resource.error,
    tableKey: queryKey,
    retryTabTasks,
  };
}
