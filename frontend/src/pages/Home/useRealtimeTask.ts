import { useEffect, useRef, useState } from "react";
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

export type SnapshotHistory = {
  current: CurrentTaskData | null;
  previous: CurrentTaskData | null;
};

/**
 * Purely reads the previous distinct snapshot of the same task from committed
 * history without mutating any ref during render (StrictMode safe).
 */
export function selectPreviousSnapshot(
  history: SnapshotHistory,
  snapshot: CurrentTaskData | null,
): CurrentTaskData | null {
  if (!snapshot) return null;
  const candidate =
    history.current === snapshot ? history.previous : history.current;
  if (!candidate) return null;
  return getRealtimeTaskIdentity(candidate) === getRealtimeTaskIdentity(snapshot)
    ? candidate
    : null;
}

/**
 * Advances committed snapshot history inside useEffect after render commits.
 */
export function advanceSnapshotHistory(
  history: SnapshotHistory,
  snapshot: CurrentTaskData | null,
): SnapshotHistory {
  if (!snapshot) return { current: null, previous: null };
  if (history.current === snapshot) return history;
  const previous =
    history.current &&
    getRealtimeTaskIdentity(history.current) === getRealtimeTaskIdentity(snapshot)
      ? history.current
      : null;
  return { previous, current: snapshot };
}

type SnapshotRecord = {
  jobKey: string;
  data: CurrentTaskData | null;
  settled: boolean;
};

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
  const jobKey = enabled && jobId ? jobId : "";
  // SSE is the primary source; the existing 3s poll below stays as the
  // fallback whenever EventSource is missing or reports an error. Polling is
  // paused while the stream is live so a running sync is not queried twice.
  const stream = useJobProgressStream(jobId, enabled);
  const pollingEnabled = enabled && Boolean(jobId) && !stream.live;

  const seqRef = useRef(0);
  const appliedSeqRef = useRef(0);
  const [snapshotState, setSnapshotState] = useState<SnapshotRecord>({
    jobKey: "",
    data: null,
    settled: false,
  });
  const [pollErrorState, setPollErrorState] = useState<{
    jobKey: string;
    message: string;
  }>({
    jobKey: "",
    message: "",
  });

  const resource = useResource<CurrentTaskData | null>(
    async (signal) => {
      if (!enabled || !jobId) return null;
      const startSeq = ++seqRef.current;
      try {
        const response = await jobGetTaskCurrent({ id: jobId }, { signal });
        const next = response && isCurrentTaskData(response) ? response : null;
        if (!signal.aborted) {
          setPollErrorState({ jobKey: jobId, message: "" });
          if (startSeq >= appliedSeqRef.current) {
            appliedSeqRef.current = startSeq;
            setSnapshotState({ jobKey: jobId, data: next, settled: true });
          }
        }
        return next;
      } catch (err) {
        if (!signal.aborted) {
          setPollErrorState({
            jobKey: jobId,
            message: err instanceof Error ? err.message : "加载失败",
          });
        }
        throw err;
      }
    },
    [enabled, jobId],
    pollingEnabled,
  );

  // Record every incoming SSE frame as the latest authoritative snapshot so a
  // subsequent transient SSE drop never rewinds to an older `resource.data`.
  useEffect(() => {
    if (!jobKey || !stream.hasFrame) return;
    const seq = ++seqRef.current;
    appliedSeqRef.current = seq;
    setSnapshotState({ jobKey, data: stream.data, settled: true });
  }, [jobKey, stream.hasFrame, stream.frameSeq, stream.data]);

  useEffect(() => {
    if (!jobKey) {
      setSnapshotState({ jobKey: "", data: null, settled: false });
      setPollErrorState({ jobKey: "", message: "" });
    }
  }, [jobKey]);

  // On a transient SSE drop (live: true -> false) for the active job, trigger
  // an immediate silent poll instead of waiting up to 3s or trusting stale
  // `resource.data`.
  const liveStateRef = useRef<{ jobKey: string; live: boolean }>({
    jobKey: "",
    live: false,
  });
  const refreshResource = resource.refresh;
  useEffect(() => {
    const prev = liveStateRef.current;
    liveStateRef.current = { jobKey, live: stream.live };
    if (jobKey && prev.jobKey === jobKey && prev.live && !stream.live) {
      void refreshResource(true);
    }
  }, [jobKey, stream.live, refreshResource]);

  const hasCurrentKey = Boolean(jobKey);
  const snapshot =
    hasCurrentKey && snapshotState.jobKey === jobKey
      ? snapshotState.data
      : null;
  const settled =
    hasCurrentKey && snapshotState.jobKey === jobKey && snapshotState.settled;
  const pollError =
    hasCurrentKey && pollErrorState.jobKey === jobKey
      ? pollErrorState.message
      : "";
  const error = stream.live ? "" : pollError;

  const historyRef = useRef<SnapshotHistory>({
    current: null,
    previous: null,
  });
  const previousForCalc = selectPreviousSnapshot(historyRef.current, snapshot);
  useEffect(() => {
    historyRef.current = advanceSnapshotHistory(historyRef.current, snapshot);
  }, [snapshot]);

  const currentTask = snapshot
    ? toCurrentTaskView(snapshot, previousForCalc)
    : null;

  return {
    currentTask,
    // While the stream/poll is opening, keep the spinner: otherwise the
    // initial "no task" state flashes before the first snapshot arrives.
    loading: hasCurrentKey ? (snapshot ? false : !settled && !error) : false,
    error,
    refreshCurrentTask: async () => {
      await resource.refresh();
    },
  };
}
