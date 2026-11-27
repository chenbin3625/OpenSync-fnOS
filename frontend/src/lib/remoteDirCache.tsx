import { createContext, useContext, useRef, type ReactNode } from "react";
import { api } from "../api/client";

export type RemoteDirEntry = { name?: string; path?: string };

interface InFlightRequest {
  promise: Promise<RemoteDirEntry[]>;
  controller: AbortController;
  subscribers: number;
}

export type RemoteDirUpdateEvent = {
  engineId: number;
  path: string;
  entries: RemoteDirEntry[];
};

export interface RemoteDirCache {
  fetchPaths: (
    engineId: number,
    path: string,
    options?: { signal?: AbortSignal; force?: boolean },
  ) => Promise<RemoteDirEntry[]>;
  invalidateEngine: (engineId?: number) => void;
  subscribe?: (listener: (event: RemoteDirUpdateEvent) => void) => () => void;
}

function normalizeCachePath(path: string): string {
  const trimmed = String(path || "/").trim();
  if (!trimmed || trimmed === "/") return "/";
  return `/${trimmed.replace(/^\/+/, "").replace(/\/+$/, "")}`.replace(/\/+/g, "/");
}

function cacheKey(engineId: number, path: string): string {
  return `${engineId}:${normalizeCachePath(path)}`;
}

function abortError(): Error {
  const error = new Error("Aborted");
  error.name = "AbortError";
  return error;
}

function waitWithSignal<T>(promise: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(abortError());
  return new Promise<T>((resolve, reject) => {
    const onAbort = () => reject(abortError());
    signal.addEventListener("abort", onAbort, { once: true });
    promise.then(
      (value) => {
        signal.removeEventListener("abort", onAbort);
        resolve(value);
      },
      (err) => {
        signal.removeEventListener("abort", onAbort);
        reject(err);
      },
    );
  });
}

export function createRemoteDirCache(
  loader: (
    engineId: number,
    path: string,
    signal?: AbortSignal,
  ) => Promise<RemoteDirEntry[]> = api.paths,
): RemoteDirCache {
  const entries = new Map<string, RemoteDirEntry[]>();
  const inFlight = new Map<string, InFlightRequest>();
  const keyVersion = new Map<string, number>();
  const engineVersion = new Map<number, number>();
  const listeners = new Set<(event: RemoteDirUpdateEvent) => void>();
  let allVersion = 0;
  let seq = 0;

  const startFetch = (
    key: string,
    engineId: number,
    path: string,
    signal?: AbortSignal,
  ): Promise<RemoteDirEntry[]> => {
    if (signal?.aborted) return Promise.reject(abortError());

    let flight = inFlight.get(key);
    if (!flight) {
      const controller = new AbortController();
      const requestVersion = ++seq;
      keyVersion.set(key, requestVersion);
      const promise = loader(engineId, path, controller.signal).then(
        (result) => {
          const normalized = result || [];
          const isLatest =
            !controller.signal.aborted &&
            requestVersion >= (keyVersion.get(key) ?? 0) &&
            requestVersion > (engineVersion.get(engineId) ?? 0) &&
            requestVersion > allVersion;
          if (isLatest) {
            entries.set(key, normalized);
            for (const listener of listeners) {
              listener({ engineId, path, entries: normalized });
            }
          }
          if (inFlight.get(key)?.controller === controller) {
            inFlight.delete(key);
          }
          return normalized;
        },
        (err) => {
          if (inFlight.get(key)?.controller === controller) {
            inFlight.delete(key);
          }
          throw err;
        },
      );
      flight = { promise, controller, subscribers: 0 };
      inFlight.set(key, flight);
    }

    const activeFlight = flight;
    activeFlight.subscribers += 1;
    let released = false;
    const release = () => {
      if (released) return;
      released = true;
      activeFlight.subscribers -= 1;
      // Defer abort by a microtask so React StrictMode's synchronous
      // unmount-then-remount cycle reuses the in-flight request instead of
      // aborting and firing a duplicate fetch.
      queueMicrotask(() => {
        if (
          activeFlight.subscribers <= 0 &&
          inFlight.get(key) === activeFlight
        ) {
          inFlight.delete(key);
          activeFlight.controller.abort();
        }
      });
    };

    const onAbort = () => release();
    signal?.addEventListener("abort", onAbort, { once: true });

    return waitWithSignal(activeFlight.promise, signal).finally(() => {
      signal?.removeEventListener("abort", onAbort);
      release();
    });
  };

  return {
    fetchPaths(engineId, path, options) {
      const normalized = normalizeCachePath(path);
      const key = cacheKey(engineId, normalized);
      if (options?.force) {
        keyVersion.set(key, ++seq);
        entries.delete(key);
        const existing = inFlight.get(key);
        if (existing) {
          inFlight.delete(key);
        }
        return startFetch(key, engineId, normalized, options.signal);
      }
      if (entries.has(key)) {
        return Promise.resolve(entries.get(key)!);
      }
      return startFetch(key, engineId, normalized, options?.signal);
    },
    invalidateEngine(engineId) {
      if (engineId === undefined) {
        allVersion = ++seq;
        entries.clear();
        for (const flight of inFlight.values()) flight.controller.abort();
        inFlight.clear();
        return;
      }
      engineVersion.set(engineId, ++seq);
      const prefix = `${engineId}:`;
      for (const key of [...entries.keys()]) {
        if (key.startsWith(prefix)) entries.delete(key);
      }
      for (const [key, flight] of [...inFlight.entries()]) {
        if (key.startsWith(prefix)) {
          flight.controller.abort();
          inFlight.delete(key);
        }
      }
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

const RemoteDirCacheContext = createContext<RemoteDirCache | null>(null);

export function RemoteDirCacheProvider({
  cache,
  children,
}: {
  cache?: RemoteDirCache;
  children: ReactNode;
}) {
  const cacheRef = useRef<RemoteDirCache | null>(null);
  if (!cache && !cacheRef.current) {
    cacheRef.current = createRemoteDirCache();
  }
  return (
    <RemoteDirCacheContext value={cache ?? cacheRef.current}>
      {children}
    </RemoteDirCacheContext>
  );
}

export function useRemoteDirCache(): RemoteDirCache | null {
  return useContext(RemoteDirCacheContext);
}
