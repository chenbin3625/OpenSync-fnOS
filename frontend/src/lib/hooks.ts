import { useCallback, useEffect, useRef, useState, type SetStateAction } from "react";
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
  const [data, setDataState] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  // Failure of a silent (background/polling) refresh, exposed separately so a
  // consumer can keep rendering stale data with a "reconnecting" notice. It is
  // also written to `error` for backwards compatibility: the realtime view
  // (TaskExecution) relies on `error` to show that notice while data exists.
  const [refreshError, setRefreshError] = useState("");
  const setData = useCallback((value: SetStateAction<T | null>) => {
    setDataState(value);
    setError("");
    setRefreshError("");
  }, []);
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
        setDataState((prev) => (isDeepEqual(prev, result) ? prev : result));
        setError((prev) => (prev ? "" : prev));
        setRefreshError((prev) => (prev ? "" : prev));
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
    return () => {
      abortRef.current?.abort();
      abortRef.current = null;
      requestRef.current++;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [depsKey, refresh]);
  // Polling is a separate effect: tying it to the fetch effect made a poll
  // toggle (the realtime stream disconnecting/reconnecting) clear the table and
  // refetch, so the view flashed even though the data had not changed.
  useEffect(() => {
    if (!poll) return;
    const interval = setInterval(() => {
      if (document.visibilityState !== "hidden") void refresh(true);
    }, 3000);
    return () => clearInterval(interval);
  }, [poll, refresh]);
  // setData is for callers that fetch outside the hook (the manual
  // "检查更新" request) and must publish the result without a second round trip.
  return { data, loading, error, refreshError, refresh, setData };
}

/** Tracks a CSS media query, e.g. useMediaQuery("(max-width: 640px)"). */
export function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(
    () =>
      typeof window !== "undefined" &&
      typeof window.matchMedia === "function" &&
      window.matchMedia(query).matches,
  );
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }
    const media = window.matchMedia(query);
    const update = () => setMatches(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [query]);
  return matches;
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

export function isDeepEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (
    typeof a !== "object" ||
    typeof b !== "object" ||
    a === null ||
    b === null
  ) {
    return false;
  }
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) {
      return false;
    }
    for (let i = 0; i < a.length; i += 1) {
      if (!isDeepEqual(a[i], b[i])) return false;
    }
    return true;
  }
  if (a instanceof Date || b instanceof Date) {
    return (
      a instanceof Date &&
      b instanceof Date &&
      Object.is(a.getTime(), b.getTime())
    );
  }
  if (a instanceof RegExp || b instanceof RegExp) {
    return (
      a instanceof RegExp &&
      b instanceof RegExp &&
      a.source === b.source &&
      a.flags === b.flags
    );
  }
  const objA = a as Record<string, unknown>;
  const objB = b as Record<string, unknown>;
  const keys = new Set([...Object.keys(objA), ...Object.keys(objB)]);
  for (const key of keys) {
    if (!isDeepEqual(objA[key], objB[key])) return false;
  }
  return true;
}

export function useEditorForm<T extends object>(
  initial: T | (() => T),
) {
  const [form, setFormState] = useState<T>(initial);
  const [dirty, setDirty] = useState(false);
  // Snapshot of the form as it was opened, used to keep "dirty" honest: simply
  // re-selecting the current value (or typing and reverting it) is not an
  // unsaved change and must not trigger the "放弃修改？" / beforeunload prompts.
  const initialRef = useRef(form);
  const formRef = useRef(form);
  formRef.current = form;

  const setForm = useCallback((action: SetStateAction<T>) => {
    const next =
      typeof action === "function"
        ? (action as (prev: T) => T)(formRef.current)
        : action;
    formRef.current = next;
    setFormState(next);
  }, []);

  const change = useCallback(<K extends keyof T>(key: K, value: T[K]) => {
    const next = { ...formRef.current, [key]: value };
    formRef.current = next;
    setFormState(next);
    setDirty(!isDeepEqual(next, initialRef.current));
  }, []);

  const patch = useCallback((values: Partial<T>) => {
    const next = { ...formRef.current, ...values };
    formRef.current = next;
    setFormState(next);
    setDirty(!isDeepEqual(next, initialRef.current));
  }, []);

  return { form, dirty, change, patch, setForm, setDirty };
}
