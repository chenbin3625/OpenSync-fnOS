import { useCallback, useEffect, useRef, useState } from "react";
import { getErrorMessage } from "../components/common";

export function useResource<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  dependencies: unknown[] = [],
  poll = false,
) {
  const fetchRef = useRef(fetcher);
  fetchRef.current = fetcher;
  const abortRef = useRef<AbortController | null>(null);
  const requestRef = useRef(0);
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  // Failure of a silent (background/polling) refresh, exposed separately so a
  // consumer can keep rendering stale data with a "reconnecting" notice. It is
  // also written to `error` for backwards compatibility: the realtime view
  // (TaskExecution) relies on `error` to show that notice while data exists.
  const [refreshError, setRefreshError] = useState("");
  const refresh = useCallback(async (silent = false) => {
    if (silent && abortRef.current) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const id = ++requestRef.current;
    if (!silent) {
      setLoading(true);
      setError("");
    }
    try {
      const result = await fetchRef.current(controller.signal);
      if (!controller.signal.aborted && id === requestRef.current) {
        setData(result);
        setError("");
        setRefreshError("");
      }
    } catch (err) {
      if (!controller.signal.aborted && id === requestRef.current) {
        const message = getErrorMessage(err, "加载失败");
        setRefreshError(message);
        // Kept identical to the previous behaviour so the realtime view keeps
        // showing its "连接中断" notice; pages that want to keep stale data can
        // read `refreshError` instead of `error`.
        setError(message);
      }
    } finally {
      if (id === requestRef.current) {
        abortRef.current = null;
        if (!silent) setLoading(false);
      }
    }
  }, []);
  const depsKey = JSON.stringify(dependencies);
  useEffect(() => {
    setData(null);
    void refresh();
    const interval = poll
      ? setInterval(() => {
          if (document.visibilityState !== "hidden") void refresh(true);
        }, 3000)
      : undefined;
    return () => {
      abortRef.current?.abort();
      abortRef.current = null;
      requestRef.current++;
      clearInterval(interval);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [depsKey, poll, refresh]);
  // setData is for callers that fetch outside the hook (the manual
  // "检查更新" request) and must publish the result without a second round trip.
  return { data, loading, error, refreshError, refresh, setData };
}

/** Returned when a run was dropped because another action was still in flight. */
export const actionSkipped = Symbol("actionSkipped");

export function useAction() {
  const inFlight = useRef(false);
  const [busy, setBusy] = useState(false);
  return {
    busy,
    // Returns actionSkipped when the call was dropped, so callers can avoid
    // reporting success for work that never ran: double-clicking delete used to
    // skip the second request and still toast "已删除".
    run: async (action: () => Promise<unknown>) => {
      if (inFlight.current) return actionSkipped;
      inFlight.current = true;
      setBusy(true);
      try {
        return await action();
      } finally {
        inFlight.current = false;
        setBusy(false);
      }
    },
  };
}

export function useEditorForm<T extends object>(
  initial: T | (() => T),
) {
  const [form, setForm] = useState<T>(initial);
  const [dirty, setDirty] = useState(false);
  // Snapshot of the form as it was opened, used to keep "dirty" honest: simply
  // re-selecting the current value (or typing and reverting it) is not an
  // unsaved change and must not trigger the "放弃修改？" / beforeunload prompts.
  const initialRef = useRef(form);
  const isPristine = (value: T) =>
    (Object.keys(initialRef.current) as (keyof T)[]).every((key) =>
      Object.is(value[key], initialRef.current[key]),
    );
  const change = <K extends keyof T>(key: K, value: T[K]) => {
    const next = { ...form, [key]: value };
    setForm(next);
    setDirty(!isPristine(next));
  };
  const patch = (values: Partial<T>) => {
    const next = { ...form, ...values };
    setForm(next);
    setDirty(!isPristine(next));
  };
  return { form, dirty, change, patch, setForm, setDirty };
}
