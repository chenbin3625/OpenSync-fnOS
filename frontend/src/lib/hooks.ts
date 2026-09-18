import { useCallback, useEffect, useRef, useState } from "react";

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
        setError(err instanceof Error ? err.message : "加载失败");
    } finally {
      if (id === requestRef.current) {
        abortRef.current = null;
        if (!silent) setLoading(false);
      }
    }
  }, []);
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
  }, [...dependencies, poll, refresh]);
  return { data, loading, error, refresh };
}

export function useAction() {
  const inFlight = useRef(false);
  const [busy, setBusy] = useState(false);
  return {
    busy,
    run: async (action: () => Promise<unknown>) => {
      if (inFlight.current) return;
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
