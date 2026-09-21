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
      }
    } catch (err) {
      if (!controller.signal.aborted && id === requestRef.current)
        setError(getErrorMessage(err, "加载失败"));
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
  return { data, loading, error, refresh };
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
  const change = <K extends keyof T>(key: K, value: T[K]) => {
    setDirty(true);
    setForm((f) => ({ ...f, [key]: value }));
  };
  const patch = (values: Partial<T>) => {
    setDirty(true);
    setForm((f) => ({ ...f, ...values }));
  };
  return { form, dirty, change, patch, setForm, setDirty };
}
