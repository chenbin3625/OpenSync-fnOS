import { useCallback, useState } from "react";
import { jobGetTaskCurrent } from "../../api/job";
import { useResource } from "../../lib/hooks";
import type { CurrentTaskView, TaskItem } from "../../types";
import { getRealtimeTaskIdentity, normalizeTaskItemPage } from "./taskRows";

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
  retryTabTasks: () => void;
} {
  const [activeTab, setActiveTabValue] = useState(1);
  const [tabTaskPage, setTabTaskPageValue] = useState(1);
  const [pageSize, setPageSizeValue] = useState(initialPageSize);
  const taskIdentity = currentTask
    ? getRealtimeTaskIdentity(currentTask)
    : "";
  const queryKey = [
    jobId,
    taskIdentity,
    activeTab,
    tabTaskPage,
    pageSize,
  ].join(":");
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
      return {
        key: queryKey,
        ...normalizeTaskItemPage(response, requestIdentity),
      };
    },
    [enabled, queryKey],
    enabled && Boolean(taskIdentity),
  );
  const currentPage = resource.data?.key === queryKey ? resource.data : null;
  const pendingCurrentPage =
    enabled && Boolean(taskIdentity) && !currentPage && !resource.error;

  const setActiveTab = useCallback((status: number) => {
    setActiveTabValue(status);
    setTabTaskPageValue(1);
  }, []);
  const setTabTaskPage = useCallback((page: number) => {
    setTabTaskPageValue(Math.max(1, Math.trunc(page)));
  }, []);
  const setPageSize = useCallback((size: number) => {
    setPageSizeValue(Math.max(1, Math.trunc(size)));
    setTabTaskPageValue(1);
  }, []);
  const retryTabTasks = useCallback(() => {
    void resource.refresh();
  }, [resource.refresh]);

  return {
    activeTab,
    setActiveTab,
    tabTaskList: currentPage?.rows || [],
    tabTaskTotal: currentPage?.total || 0,
    tabTaskPage,
    setTabTaskPage,
    pageSize,
    setPageSize,
    tabLoading: resource.loading || pendingCurrentPage,
    tabError: resource.error,
    retryTabTasks,
  };
}
