import { describe, expect, it, vi } from "vitest";
import { createRemoteDirCache } from "../src/lib/remoteDirCache";

describe("remoteDirCache", () => {
  it("deduplicates concurrent requests and caches settled results", async () => {
    let resolveLoad!: (rows: { name: string }[]) => void;
    const loader = vi.fn(
      () =>
        new Promise<{ name: string }[]>((resolve) => {
          resolveLoad = resolve;
        }),
    );
    const cache = createRemoteDirCache(loader);

    const p1 = cache.fetchPaths(1, "/");
    const p2 = cache.fetchPaths(1, "/");
    expect(loader).toHaveBeenCalledTimes(1);

    resolveLoad([{ name: "Photos" }]);
    expect(await p1).toEqual([{ name: "Photos" }]);
    expect(await p2).toEqual([{ name: "Photos" }]);

    // Subsequent read hits cache
    expect(await cache.fetchPaths(1, "/")).toEqual([{ name: "Photos" }]);
    expect(loader).toHaveBeenCalledTimes(1);

    // Force refresh bypasses cache
    loader.mockResolvedValueOnce([{ name: "Photos" }, { name: "New" }]);
    expect(await cache.fetchPaths(1, "/", { force: true })).toEqual([
      { name: "Photos" },
      { name: "New" },
    ]);
    expect(loader).toHaveBeenCalledTimes(2);
  });

  it("keeps an in-flight request alive for remaining subscribers when one aborts", async () => {
    let observedSignal: AbortSignal | undefined;
    let resolveLoad!: (rows: { name: string }[]) => void;
    const loader = vi.fn((_id: number, _path: string, signal?: AbortSignal) => {
      observedSignal = signal;
      return new Promise<{ name: string }[]>((resolve) => {
        resolveLoad = resolve;
      });
    });
    const cache = createRemoteDirCache(loader);

    const c1 = new AbortController();
    const c2 = new AbortController();
    const p1 = cache.fetchPaths(1, "/Photos", { signal: c1.signal });
    const p2 = cache.fetchPaths(1, "/Photos", { signal: c2.signal });

    c1.abort();
    await expect(p1).rejects.toMatchObject({ name: "AbortError" });
    expect(observedSignal?.aborted).toBe(false);

    resolveLoad([{ name: "2025" }]);
    expect(await p2).toEqual([{ name: "2025" }]);
  });

  it("aborts the underlying request after a microtask once all subscribers abort", async () => {
    let observedSignal: AbortSignal | undefined;
    const loader = vi.fn((_id: number, _path: string, signal?: AbortSignal) => {
      observedSignal = signal;
      return new Promise<{ name: string }[]>(() => {});
    });
    const cache = createRemoteDirCache(loader);

    const controller = new AbortController();
    const pending = cache.fetchPaths(1, "/Slow", { signal: controller.signal });
    controller.abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await Promise.resolve();
    expect(observedSignal?.aborted).toBe(true);
  });

  it("ignores a slower pre-force request that resolves after a newer force refresh", async () => {
    let resolveSlow!: (rows: { name: string }[]) => void;
    let resolveFast!: (rows: { name: string }[]) => void;
    let call = 0;
    const loader = vi.fn(() => {
      call += 1;
      if (call === 1) {
        return new Promise<{ name: string }[]>((resolve) => {
          resolveSlow = resolve;
        });
      }
      return new Promise<{ name: string }[]>((resolve) => {
        resolveFast = resolve;
      });
    });
    const cache = createRemoteDirCache(loader);

    const pSlow = cache.fetchPaths(1, "/Photos");
    const pFast = cache.fetchPaths(1, "/Photos", { force: true });
    expect(loader).toHaveBeenCalledTimes(2);

    resolveFast([{ name: "fresh" }]);
    expect(await pFast).toEqual([{ name: "fresh" }]);

    // Older slower response settles after the forced refresh — it must not clobber cache.
    resolveSlow([{ name: "stale" }]);
    await pSlow;

    expect(await cache.fetchPaths(1, "/Photos")).toEqual([{ name: "fresh" }]);
    expect(loader).toHaveBeenCalledTimes(2);
  });

  it("does not populate cache from an in-flight request invalidated by invalidateEngine", async () => {
    let resolveOld!: (rows: { name: string }[]) => void;
    const loader = vi.fn(() => {
      return new Promise<{ name: string }[]>((resolve) => {
        resolveOld = resolve;
      });
    });
    const cache = createRemoteDirCache(loader);

    const pOld = cache.fetchPaths(2, "/Docs");
    cache.invalidateEngine(2);

    resolveOld([{ name: "stale-after-invalidate" }]);
    await pOld;

    loader.mockResolvedValueOnce([{ name: "fresh-after-invalidate" }]);
    expect(await cache.fetchPaths(2, "/Docs")).toEqual([
      { name: "fresh-after-invalidate" },
    ]);
    expect(loader).toHaveBeenCalledTimes(2);
  });

  it("notifies subscribers when forced refreshes update a directory", async () => {
    const loader = vi.fn().mockResolvedValueOnce([{ name: "Existing" }]);
    const cache = createRemoteDirCache(loader);
    const events: { engineId: number; path: string; entries: { name?: string }[] }[] = [];
    const unsubscribe = cache.subscribe?.((event) => events.push(event));

    await cache.fetchPaths(1, "/Cloud");
    loader.mockResolvedValueOnce([{ name: "Existing" }, { name: "Created" }]);
    await cache.fetchPaths(1, "/Cloud", { force: true });

    expect(events).toEqual([
      { engineId: 1, path: "/Cloud", entries: [{ name: "Existing" }] },
      {
        engineId: 1,
        path: "/Cloud",
        entries: [{ name: "Existing" }, { name: "Created" }],
      },
    ]);

    unsubscribe?.();
    loader.mockResolvedValueOnce([{ name: "AfterUnsub" }]);
    await cache.fetchPaths(1, "/Cloud", { force: true });
    expect(events).toHaveLength(2);
  });
});
