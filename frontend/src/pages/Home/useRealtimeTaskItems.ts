import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { jobGetTaskCurrent } from "../../api/job";
import type { CurrentTaskView, TaskItem, TaskNumKey } from "../../types";
import { getRealtimeTaskIdentity, normalizeTaskItemPage } from "./taskRows";

// Item pages refresh independently from the SSE summary. A refresh never
// replaces an identical request that is still in flight.
const ITEMS_POLL_INTERVAL_MS = 3000;

type RealtimeTaskItemsParams = {
  jobId: string;
  enabled: boolean;
  currentTask: CurrentTaskView | null;
  pageSize: number;
};

type RealtimeItemsState = {
  taskIdentity: string;
  activeTab: number;
  page: number;
  pageSize: number;
  rows: TaskItem[];
  total: number;
  loading: boolean;
  error?: string;
};

type RealtimeItemsAction =
  | { type: "task"; taskIdentity: string; total: number }
  | { type: "tab"; status: number; total: number }
  | { type: "page"; page: number }
  | { type: "pageSize"; pageSize: number }
  | { type: "start"; key: string }
  | { type: "success"; key: string; rows: TaskItem[]; total: number }
  | { type: "failure"; key: string; error: string }
  | { type: "retry" };

function stateKey(state: RealtimeItemsState): string {
  return `${state.taskIdentity}:${state.activeTab}:${state.page}:${state.pageSize}`;
}

function realtimeItemsReducer(
  state: RealtimeItemsState,
  action: RealtimeItemsAction,
): RealtimeItemsState {
  switch (action.type) {
    case "task":
      if (action.taskIdentity === state.taskIdentity) return state;
      return {
        ...state,
        taskIdentity: action.taskIdentity,
        page: 1,
        rows: [],
        total: action.total,
        loading: Boolean(action.taskIdentity),
        error: undefined,
      };
    case "tab":
      if (action.status === state.activeTab) return state;
      return {
        ...state,
        activeTab: action.status,
        page: 1,
        rows: [],
        total: action.total,
        loading: Boolean(state.taskIdentity),
        error: undefined,
      };
    case "page": {
      const page = Math.max(1, Math.trunc(action.page));
      if (page === state.page) return state;
      return {
        ...state,
        page,
        rows: [],
        loading: Boolean(state.taskIdentity),
        error: undefined,
      };
    }
    case "pageSize": {
      const pageSize = Math.max(1, Math.trunc(action.pageSize));
      if (pageSize === state.pageSize && state.page === 1) return state;
      return {
        ...state,
        page: 1,
        pageSize,
        rows: [],
        loading: Boolean(state.taskIdentity),
        error: undefined,
      };
    }
    case "start":
      if (action.key !== stateKey(state)) return state;
      return { ...state, loading: state.rows.length === 0, error: undefined };
    case "success": {
      if (action.key !== stateKey(state)) return state;
      const maxPage = Math.max(1, Math.ceil(action.total / state.pageSize));
      if (state.page > maxPage) {
        return {
          ...state,
          page: maxPage,
          rows: [],
          total: action.total,
          loading: true,
          error: undefined,
        };
      }
      return {
        ...state,
        rows: action.rows,
        total: action.total,
        loading: false,
        error: undefined,
      };
    }
    case "failure":
      if (action.key !== stateKey(state)) return state;
      return { ...state, loading: false, error: action.error };
    case "retry":
      return { ...state, loading: state.rows.length === 0, error: undefined };
  }
}

function statusTotal(task: CurrentTaskView | null, status: number): number {
  const keys: Partial<Record<number, TaskNumKey>> = {
    0: "wait",
    1: "running",
    2: "success",
    7: "fail",
    [-1]: "other",
  };
  const key = keys[status];
  return key ? Number(task?.num?.[key] || 0) : 0;
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : "实时任务加载失败";
}

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
  const taskIdentity = currentTask
    ? getRealtimeTaskIdentity(currentTask)
    : "";
  const [state, dispatch] = useReducer(realtimeItemsReducer, {
    taskIdentity: "",
    activeTab: 1,
    page: 1,
    pageSize: initialPageSize,
    rows: [],
    total: 0,
    loading: false,
  });
  const [refreshTick, setRefreshTick] = useState(0);
  const requestRef = useRef(0);
  const inFlightRef = useRef<{
    key: string;
    controller: AbortController;
    requestID: number;
  } | null>(null);

  useEffect(() => {
    dispatch({
      type: "task",
      taskIdentity,
      total: statusTotal(currentTask, state.activeTab),
    });
  }, [currentTask, state.activeTab, taskIdentity]);

  useEffect(() => {
    if (!enabled || !jobId || !taskIdentity) return undefined;
    const interval = setInterval(() => {
      if (document.visibilityState !== "hidden") {
        setRefreshTick((value) => value + 1);
      }
    }, ITEMS_POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [enabled, jobId, taskIdentity]);

  useEffect(() => {
    if (
      !enabled ||
      !jobId ||
      !taskIdentity ||
      state.taskIdentity !== taskIdentity
    ) {
      inFlightRef.current?.controller.abort();
      inFlightRef.current = null;
      return;
    }

    const key = stateKey(state);
    const inFlight = inFlightRef.current;
    if (inFlight?.key === key) return;
    inFlight?.controller.abort();

    const controller = new AbortController();
    const requestID = ++requestRef.current;
    inFlightRef.current = { key, controller, requestID };
    dispatch({ type: "start", key });

    async function loadTabTasks() {
      try {
        const res = await jobGetTaskCurrent(
          {
            id: jobId,
            status: state.activeTab,
            pageSize: state.pageSize,
            pageNum: state.page,
          },
          { silent: true, signal: controller.signal },
        );
        if (controller.signal.aborted) return;
        const { rows, total } = normalizeTaskItemPage(res.data);
        dispatch({ type: "success", key, rows, total });
      } catch (error) {
        if (!controller.signal.aborted) {
          dispatch({ type: "failure", key, error: errorMessage(error) });
        }
      } finally {
        if (inFlightRef.current?.requestID === requestID) {
          inFlightRef.current = null;
        }
      }
    }

    void loadTabTasks();
  }, [
    enabled,
    jobId,
    refreshTick,
    state.activeTab,
    state.page,
    state.pageSize,
    state.taskIdentity,
    taskIdentity,
  ]);

  useEffect(
    () => () => {
      inFlightRef.current?.controller.abort();
      inFlightRef.current = null;
    },
    [],
  );

  const setActiveTab = useCallback(
    (status: number) => {
      dispatch({ type: "tab", status, total: statusTotal(currentTask, status) });
    },
    [currentTask],
  );
  const setTabTaskPage = useCallback((page: number) => {
    dispatch({ type: "page", page });
  }, []);
  const setPageSize = useCallback((pageSize: number) => {
    dispatch({ type: "pageSize", pageSize });
  }, []);
  const retryTabTasks = useCallback(() => {
    dispatch({ type: "retry" });
    setRefreshTick((value) => value + 1);
  }, []);

  return {
    activeTab: state.activeTab,
    setActiveTab,
    tabTaskList: state.rows,
    tabTaskTotal: state.total,
    tabTaskPage: state.page,
    setTabTaskPage,
    pageSize: state.pageSize,
    setPageSize,
    tabLoading: state.loading,
    tabError: state.error,
    retryTabTasks,
  };
}
