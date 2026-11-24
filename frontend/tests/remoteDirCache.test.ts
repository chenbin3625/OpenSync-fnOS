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
});
