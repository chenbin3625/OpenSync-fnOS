import { useRef } from "react";
import { jobGetTaskCurrent } from "../../api/job";
import { useResource } from "../../lib/hooks";
import type {
  CurrentTaskData,
  CurrentTaskView,
  PageData,
  TaskItem,
} from "../../types";
import { calcRealtimeProgress, getRealtimeTaskIdentity } from "./taskRows";
import { useJobProgressStream } from "./useJobProgressStream";

function isCurrentTaskData(
  data: CurrentTaskData | PageData<TaskItem> | TaskItem[] | null,
): data is CurrentTaskData {
  return !!data && !Array.isArray(data) && "taskId" in data && "num" in data;
}

function toCurrentTaskView(
  data: CurrentTaskData,
  previous: CurrentTaskData | null,
): CurrentTaskView {
  return {
    ...data,
    doingTask: data.doingTask || [],
    ...calcRealtimeProgress(data, previous),
  };
}

/** Returns the previous distinct snapshot of the same task (for speed deltas). */
function previousSnapshot(
  ref: { current: CurrentTaskData | null },
  current: CurrentTaskData,
): CurrentTaskData | null {
  const previous = ref.current;
  ref.current = current;
  if (!previous || previous === current) return null;
  return getRealtimeTaskIdentity(previous) === getRealtimeTaskIdentity(current)
    ? previous
    : null;
}

export function useRealtimeTask(
  jobId: string,
  enabled: boolean,
): {
  currentTask: CurrentTaskView | null;
  /** True until the first response (or failure) for this job arrives. */
  loading: boolean;
  /** Last poll failure; cleared by the next successful poll. */
  error: string;
  refreshCurrentTask: () => Promise<void>;
} {
  // SSE is the primary source; the existing 3s poll below stays as the
  // fallback whenever EventSource is missing or reports an error. Polling is
  // paused while the stream is live so a running sync is not queried twice.
  const stream = useJobProgressStream(jobId, enabled);
  const pollingEnabled = enabled && Boolean(jobId) && !stream.live;
  const resource = useResource<CurrentTaskData | null>(
    async (signal) => {
      if (!enabled || !jobId) return null;
      const response = await jobGetTaskCurrent({ id: jobId }, { signal });
      return response && isCurrentTaskData(response) ? response : null;
    },
    [enabled, jobId],
    pollingEnabled,
  );

  const previousRef = useRef<CurrentTaskData | null>(null);
  // While the stream is live its snapshot (including an explicit null = "no
  // task running") wins. On fallback the poll is authoritative once it has
  // answered; before that, the last stream snapshot keeps the view stable.
  let snapshot: CurrentTaskData | null;
  if (stream.live) {
    snapshot = stream.hasFrame ? stream.data : resource.data;
  } else if (resource.loading) {
    snapshot = resource.data ?? (stream.hasFrame ? stream.data : null);
  } else if (resource.error) {
    snapshot = resource.data ?? (stream.hasFrame ? stream.data : null);
  } else {
    snapshot = resource.data;
  }

  const currentTask = snapshot
    ? toCurrentTaskView(snapshot, previousSnapshot(previousRef, snapshot))
    : null;
  if (!snapshot) previousRef.current = null;

  return {
    currentTask,
    // While the stream is opening, keep the spinner: otherwise the initial
    // "no task" state flashes before the first snapshot frame arrives.
    loading: snapshot ? false : stream.live ? !stream.hasFrame : resource.loading,
    error: stream.live ? "" : resource.error,
    refreshCurrentTask: async () => {
      await resource.refresh();
    },
  };
}
