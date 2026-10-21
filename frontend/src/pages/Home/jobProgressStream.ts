import { apiBase } from "../../api/client";
import type { CurrentTaskData } from "../../types";
import {
  getRealtimeTaskIdentity,
  mergeTaskItemPatches,
} from "./taskRows";

/**
 * SSE transport + frame reducer for the live task progress stream.
 *
 * Contract (must stay in sync with the backend / the Playwright fake):
 * - `new EventSource(url)` is created by the browser; the URL contains
 *   `/app/opensync/svr/job/stream` and carries both `id` (what the Go handler
 *   reads: `c.Query("id")`) and `jobId` (stable query name used by the e2e
 *   fake). Keeping both means neither side has to change when one name moves.
 * - The server sends default `message` events (`es.onmessage`), never custom
 *   event names. Each `event.data` is the JSON envelope
 *   `{"code":200,"data":<jobCurrentPayload|null>,"msg":"success"}`.
 * - `data` is a full `jobCurrentPayload` for snapshots; patch frames carry the
 *   full counters plus `doingPatch` while `doingTask` is null. Patch entries
 *   are merged into the previous `doingTask` by the backend `streamKey`
 *   (id -> alistTaskId -> path triple).
 * - Reconnection is delegated to EventSource. `onerror` only flips the stream
 *   to "not live" so `useRealtimeTask` can fall back to the existing 3s poll;
 *   the browser keeps retrying and a later `onopen` flips it back.
 * - When EventSource is missing/throws the caller keeps polling only.
 */
export interface ProgressEnvelope {
  code: number;
  msg: string;
  data: unknown;
}

export function jobProgressStreamUrl(jobId: string | number): string {
  const id = encodeURIComponent(String(jobId));
  // `id` is the backend query parameter; `jobId` is the e2e-observable alias.
  return `${apiBase}/job/stream?id=${id}&jobId=${id}`;
}

export function parseProgressEnvelope(raw: unknown): ProgressEnvelope | null {
  if (typeof raw !== "string") return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return null;
    }
    const envelope = parsed as {
      code?: unknown;
      msg?: unknown;
      data?: unknown;
    };
    if (typeof envelope.code !== "number") return null;
    return {
      code: envelope.code,
      msg: typeof envelope.msg === "string" ? envelope.msg : "",
      data: envelope.data,
    };
  } catch {
    // Heartbeats/comments do not reach onmessage; any other non-JSON frame is
    // ignored instead of crashing the realtime view.
    return null;
  }
}

export function isJobCurrentPayload(data: unknown): data is CurrentTaskData {
  if (!data || typeof data !== "object" || Array.isArray(data)) return false;
  const payload = data as Partial<CurrentTaskData>;
  return (
    Number.isFinite(Number(payload.taskId)) &&
    Number(payload.taskId) > 0 &&
    !!payload.num &&
    typeof payload.num === "object"
  );
}

/**
 * Reduces one SSE data frame onto the previous payload.
 * - `null` data means "no task is running" (the stream sends a null snapshot).
 * - Full snapshots (array `doingTask`) replace the list.
 * - Patch frames merge their `doingPatch` entries into the previous list; the
 *   counters are already complete in a patch frame, so spreading the payload
 *   gives the UI fresh totals without waiting for the next snapshot.
 */
export function applyProgressFrame(
  previous: CurrentTaskData | null,
  payload: unknown,
): CurrentTaskData | null {
  if (payload === null) return null;
  if (!isJobCurrentPayload(payload)) {
    // Unknown shape (future/removed field): keep the last good view rather
    // than blanking the page; the next full snapshot will correct it.
    return previous;
  }
  const sameTask =
    !!previous &&
    getRealtimeTaskIdentity(previous) === getRealtimeTaskIdentity(payload);
  const base = sameTask ? previous : null;
  const doingTask = Array.isArray(payload.doingTask)
    ? payload.doingTask
    : mergeTaskItemPatches(base?.doingTask || [], payload.doingPatch);
  return {
    ...(base ?? {}),
    ...payload,
    doingTask,
    doingPatch: undefined,
  };
}

export interface JobProgressStreamState {
  live: boolean;
  reason?: "unsupported" | "error" | "opening" | "closed";
}

export interface JobProgressStreamOptions {
  jobId: string | number;
  onData: (task: CurrentTaskData | null) => void;
  onStateChange?: (state: JobProgressStreamState) => void;
}

export interface JobProgressStream {
  url: string;
  close: () => void;
}

export function isEventSourceSupported(): boolean {
  return typeof globalThis.EventSource === "function";
}

export function createJobProgressStream(
  options: JobProgressStreamOptions,
): JobProgressStream {
  const url = jobProgressStreamUrl(options.jobId);
  const Source = globalThis.EventSource;
  if (typeof Source !== "function") {
    options.onStateChange?.({ live: false, reason: "unsupported" });
    return { url, close: () => {} };
  }

  let closed = false;
  let live = false;
  let previous: CurrentTaskData | null = null;
  let source: EventSource;
  options.onStateChange?.({ live: false, reason: "opening" });
  try {
    source = new Source(url);
  } catch {
    // Some WebViews expose EventSource but throw on use; polling is the
    // fallback, exactly as if it were missing.
    options.onStateChange?.({ live: false, reason: "unsupported" });
    return { url, close: () => {} };
  }

  source.onopen = () => {
    if (closed || live) return;
    live = true;
    options.onStateChange?.({ live: true });
  };
  source.onmessage = (event: MessageEvent<string>) => {
    if (closed) return;
    const envelope = parseProgressEnvelope(event.data);
    if (!envelope) return;
    if (envelope.code !== 200) {
      options.onStateChange?.({ live: false, reason: "error" });
      return;
    }
    if (!live) {
      // A frame is proof the stream is open; some embeddings (and the e2e
      // EventSource fake) deliver a message without firing onopen first.
      live = true;
      options.onStateChange?.({ live: true });
    }
    previous = applyProgressFrame(previous, envelope.data);
    options.onData(previous);
  };
  source.onerror = () => {
    if (closed) return;
    live = false;
    // Do not close: EventSource retries by itself while readyState is
    // CONNECTING. Reporting not-live is enough to switch the UI to polling.
    options.onStateChange?.({ live: false, reason: "error" });
  };

  return {
    url,
    close: () => {
      closed = true;
      source.close();
      options.onStateChange?.({ live: false, reason: "closed" });
    },
  };
}
