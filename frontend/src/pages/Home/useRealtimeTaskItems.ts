import { useCallback, useEffect, useRef, useState } from "react";
import { jobGetTaskCurrent } from "../../api/job";
import type { CurrentTaskView, TaskItem } from "../../types";
import {
  getRealtimeTaskIdentity,
  mergeTaskItems,
  normalizeTaskItemPage,
  shouldReplaceRealtimeRows,
  shouldResetRealtimeSnapshot,
  type RealtimeTaskLoadKey,
} from "./taskRows";

// Running tab fetches from the server-paged endpoint too, but at a faster
// cadence than the DB-backed tabs so the UI stays reasonably responsive.
const RUNNING_POLL_INTERVAL_MS = 3000;

// Non-running tabs (success/fail/other/... ) fetch from the DB-backed server
// page; slowing them down avoids hammering the sqlite aggregation queries on
// very large task item sets while each tab is simply being watched.
const NON_RUNNING_POLL_INTERVAL_MS = 15000;

type RealtimeTaskItemsParams = {
  jobId: string;
  enabled: boolean;
  currentTask: CurrentTaskView | null;
  pageSize: number;
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
} {
  const [activeTab, setActiveTabValue] = useState(1);
  const [tabTaskList, setTabTaskList] = useState<TaskItem[]>([]);
  const [tabTaskTotal, setTabTaskTotal] = useState(0);
  const [tabTaskPage, setTabTaskPageValue] = useState(1);
  const [pageSize, setPageSizeValue] = useState(initialPageSize);
  const [tabLoading, setTabLoading] = useState(false);
  const requestRef = useRef(0);
  const lastLoadedRef = useRef<RealtimeTaskLoadKey | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const tabFetchingRef = useRef(false);
  const lastFetchKeyRef = useRef<string | null>(null);
  const lastFetchAtRef = useRef<number | null>(null);

  const setActiveTab = useCallback((status: number) => {
    setActiveTabValue(status);
    setTabTaskPageValue(1);
  }, []);

  const setTabTaskPage = useCallback((page: number) => {
    setTabTaskPageValue(page);
  }, []);

  const setPageSize = useCallback((size: number) => {
    setPageSizeValue(size);
    setTabTaskPageValue(1);
  }, []);

  useEffect(() => {
    if (!enabled || !jobId || !currentTask) {
      requestRef.current += 1;
      lastLoadedRef.current = null;
      lastFetchKeyRef.current = null;
      lastFetchAtRef.current = null;
      abortRef.current?.abort();
      setTabTaskList([]);
      setTabTaskTotal(0);
      setTabLoading(false);
      return;
    }

    const taskIdentity = getRealtimeTaskIdentity(currentTask);
    const lastLoaded = lastLoadedRef.current;
    const mustResetPage =
      !lastLoaded ||
      lastLoaded.status !== activeTab ||
      lastLoaded.taskIdentity !== taskIdentity;

    if (mustResetPage && tabTaskPage !== 1) {
      requestRef.current += 1;
      lastFetchKeyRef.current = null;
      lastFetchAtRef.current = null;
      abortRef.current?.abort();
      setTabTaskList([]);
      setTabTaskTotal(0);
      setTabLoading(true);
      setTabTaskPageValue(1);
      return;
    }

    const loadKey = { status: activeTab, taskIdentity, page: tabTaskPage, pageSize };
    const replaceRows = shouldReplaceRealtimeRows(lastLoaded, loadKey);
    const resetSnapshot = shouldResetRealtimeSnapshot(lastLoaded, loadKey);

    // All tabs (including running) fetch from the server-paged endpoint.
    // Running tab uses a faster poll interval so the UI stays responsive.
    const pollInterval = activeTab === 1
      ? RUNNING_POLL_INTERVAL_MS
      : NON_RUNNING_POLL_INTERVAL_MS;
    const fetchKey = `${loadKey.status}:${loadKey.taskIdentity}:${loadKey.page}:${pageSize}`;
    const now = Date.now();
    const changedView = lastFetchKeyRef.current !== fetchKey;
    if (
      !changedView &&
      lastFetchAtRef.current != null &&
      now - lastFetchAtRef.current < pollInterval
    ) {
      return;
    }
    // A changed tab/page/task must win immediately. Abort the stale browser
    // request; requestRef and the finally guard below prevent its completion
    // from clearing loading state owned by the newer request.
    if (tabFetchingRef.current) abortRef.current?.abort();
    lastFetchKeyRef.current = fetchKey;
    lastFetchAtRef.current = now;

    const requestID = ++requestRef.current;
    lastLoadedRef.current = loadKey;

    if (replaceRows) {
      if (resetSnapshot) setTabTaskList([]);
      if (resetSnapshot) setTabTaskTotal(0);
      setTabLoading(true);
    }

    const controller = new AbortController();
    abortRef.current = controller;

    async function loadTabTasks() {
      tabFetchingRef.current = true;
      try {
        const res = await jobGetTaskCurrent(
          {
            id: jobId,
            status: activeTab,
            pageSize,
            pageNum: tabTaskPage,
          },
          { silent: true, signal: controller.signal },
        );
        if (controller.signal.aborted || requestID !== requestRef.current)
          return;
        const { rows, total } = normalizeTaskItemPage(res.data);
        setTabTaskList((previous) =>
          replaceRows ? rows : mergeTaskItems(previous, rows),
        );
        setTabTaskTotal(total);
      } catch {
        if (controller.signal.aborted) return;
        if (requestID === requestRef.current && replaceRows) {
          if (resetSnapshot) setTabTaskList([]);
          if (resetSnapshot) setTabTaskTotal(0);
        }
      } finally {
        if (requestID === requestRef.current) {
          tabFetchingRef.current = false;
          setTabLoading(false);
        }
      }
    }

    loadTabTasks();
  }, [activeTab, currentTask, enabled, jobId, pageSize, tabTaskPage]);

  // Abort any in-flight tab fetch when the component unmounts (job switch /
  // tab teardown remounts TaskList via `key`).
  useEffect(
    () => () => {
      abortRef.current?.abort();
    },
    [],
  );

  useEffect(() => {
    const maxPage = Math.max(1, Math.ceil(tabTaskTotal / pageSize));
    if (tabTaskPage > maxPage) {
      setTabTaskPageValue(maxPage);
    }
  }, [pageSize, tabTaskPage, tabTaskTotal]);

  return {
    activeTab,
    setActiveTab,
    tabTaskList,
    tabTaskTotal,
    tabTaskPage,
    setTabTaskPage,
    pageSize,
    setPageSize,
    tabLoading,
  };
}
