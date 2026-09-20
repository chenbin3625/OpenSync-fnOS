import { jobGetTaskCurrent } from "../../api/job";
import { useResource } from "../../lib/hooks";
import type {
  CurrentTaskData,
  CurrentTaskView,
  PageData,
  TaskItem,
} from "../../types";
import { calcRealtimeProgress } from "./taskRows";

function isCurrentTaskData(
  data: CurrentTaskData | PageData<TaskItem> | TaskItem[] | null,
): data is CurrentTaskData {
  return !!data && !Array.isArray(data) && "taskId" in data && "num" in data;
}

export function toCurrentTaskView(data: CurrentTaskData): CurrentTaskView {
  return {
    ...data,
    doingTask: data.doingTask || [],
    ...calcRealtimeProgress(data, null),
  };
}

export function useRealtimeTask(
  jobId: string,
  enabled: boolean,
): {
  currentTask: CurrentTaskView | null;
  refreshCurrentTask: () => Promise<void>;
} {
  const resource = useResource(
    async (signal) => {
      if (!enabled || !jobId) return null;
      const response = await jobGetTaskCurrent({ id: jobId }, { signal });
      return response && isCurrentTaskData(response)
        ? toCurrentTaskView(response)
        : null;
    },
    [enabled, jobId],
    enabled && Boolean(jobId),
  );

  return {
    currentTask: resource.data,
    refreshCurrentTask: async () => {
      await resource.refresh();
    },
  };
}
