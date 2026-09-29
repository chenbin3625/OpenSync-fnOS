/**
 * Wraps a refresh so bursts of triggers collapse into one run: calls are
 * ignored while a run is in flight and for `minIntervalMs` after the last one
 * started. Several parallel requests failing with 401 each dispatch the
 * session-expired event, and without this every one of them re-requested
 * /session.
 */
export function createGuardedRefresh(
  run: () => Promise<unknown>,
  minIntervalMs = 5000,
  now: () => number = Date.now,
) {
  let inFlight = false;
  let lastStart = -Infinity;
  return async (): Promise<boolean> => {
    if (inFlight || now() - lastStart < minIntervalMs) return false;
    inFlight = true;
    lastStart = now();
    try {
      await run();
    } finally {
      inFlight = false;
    }
    return true;
  };
}
