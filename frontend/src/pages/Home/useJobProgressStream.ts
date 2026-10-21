import { useEffect, useRef, useState } from "react";
import type { CurrentTaskData } from "../../types";
import {
  createJobProgressStream,
  isEventSourceSupported,
} from "./jobProgressStream";

/**
 * React lifecycle wrapper around the SSE progress stream.
 *
 * Policy: SSE is the primary realtime source; the caller keeps the existing 3s
 * poll as a fallback. The stream is closed on unmount, on job switch and while
 * the page is hidden (a suspended tab keeps no NAS connection open). When
 * EventSource is unavailable, or while it is not `live` (construct failure,
 * error while the browser retries), `live` stays false and the caller polls.
 */
export interface JobProgressStreamResult {
  /** Latest payload from SSE, or null when the stream says no task runs. */
  data: CurrentTaskData | null;
  /** True once any SSE message arrived (distinguishes "no task" from "no frame"). */
  hasFrame: boolean;
  /** True while the EventSource is open and the UI should not poll. */
  live: boolean;
  /** False when the environment has no usable EventSource. */
  supported: boolean;
}

function isDocumentHidden(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

export function useJobProgressStream(
  jobId: string,
  enabled: boolean,
): JobProgressStreamResult {
  const [data, setData] = useState<CurrentTaskData | null>(null);
  const [hasFrame, setHasFrame] = useState(false);
  const [live, setLive] = useState(false);
  const [visible, setVisible] = useState(() => !isDocumentHidden());
  const supported = isEventSourceSupported();
  const jobRef = useRef(jobId);

  useEffect(() => {
    if (typeof document === "undefined") return;
    const onVisibilityChange = () => setVisible(!isDocumentHidden());
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, []);

  useEffect(() => {
    // A new job must not keep rendering the previous task's snapshot; a hidden
    // page only closes the connection and keeps the last snapshot so the view
    // does not blank out while suspended.
    const jobChanged = jobRef.current !== jobId;
    jobRef.current = jobId;
    setLive(false);
    if (jobChanged) {
      setData(null);
      setHasFrame(false);
    }
    if (!enabled || !jobId || !visible || !supported) return;

    const stream = createJobProgressStream({
      jobId,
      onData: (task) => {
        setData(task);
        setHasFrame(true);
      },
      onStateChange: (state) => setLive(state.live),
    });
    return () => {
      stream.close();
    };
  }, [enabled, jobId, visible, supported]);

  return {
    data,
    hasFrame,
    live: live && supported && visible,
    supported,
  };
}
