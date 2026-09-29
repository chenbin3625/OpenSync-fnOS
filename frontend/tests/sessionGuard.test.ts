import { describe, expect, it, vi } from "vitest";
import { createGuardedRefresh } from "../src/lib/sessionGuard";

describe("session-expired refresh guard", () => {
  it("ignores triggers while a refresh is in flight", async () => {
    let finish!: () => void;
    const run = vi.fn(
      () => new Promise<void>((resolve) => (finish = resolve)),
    );
    const refresh = createGuardedRefresh(run, 0);

    const first = refresh();
    // Several parallel requests failing with 401 each fire the event.
    expect(await refresh()).toBe(false);
    expect(await refresh()).toBe(false);
    finish();
    expect(await first).toBe(true);
    expect(run).toHaveBeenCalledTimes(1);
  });
  it("allows at most one refresh per interval", async () => {
    let now = 0;
    const run = vi.fn(async () => {});
    const refresh = createGuardedRefresh(run, 5000, () => now);

    expect(await refresh()).toBe(true);
    now = 4999;
    expect(await refresh()).toBe(false);
    now = 5000;
    expect(await refresh()).toBe(true);
    expect(run).toHaveBeenCalledTimes(2);
  });
  it("releases the guard when the refresh fails", async () => {
    const run = vi.fn().mockRejectedValueOnce(new Error("x")).mockResolvedValue(undefined);
    const refresh = createGuardedRefresh(run, 0);

    await expect(refresh()).rejects.toThrow("x");
    expect(await refresh()).toBe(true);
  });
});
