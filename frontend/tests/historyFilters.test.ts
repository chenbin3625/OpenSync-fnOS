import { describe, expect, it } from "vitest";
import { historyRangeParams } from "../src/lib/historyFilters";

describe("history date filters", () => {
  it("uses an exclusive next-day boundary for the selected end date", () => {
    const start = new Date(2026, 8, 18, 15, 30);
    const end = new Date(2026, 8, 19, 9, 45);

    const result = historyRangeParams([start, end]);

    expect(result.startTime).toBe(Math.floor(new Date(2026, 8, 18).getTime() / 1000));
    expect(result.endTimeExclusive).toBe(
      Math.floor(new Date(2026, 8, 20).getTime() / 1000),
    );
  });

  it("sends both boundaries as local midnight / next-day midnight", () => {
    const start = new Date(2026, 0, 5, 23, 59, 59);
    const end = new Date(2026, 0, 5, 0, 0, 1);
    const result = historyRangeParams([start, end]);

    expect(result.startTime).toBe(
      Math.floor(new Date(2026, 0, 5).getTime() / 1000),
    );
    expect(result.endTimeExclusive).toBe(
      Math.floor(new Date(2026, 0, 6).getTime() / 1000),
    );
    expect(result.endTimeExclusive! - result.startTime!).toBe(86400);
  });

  it("omits everything for an empty range", () => {
    expect(historyRangeParams([])).toEqual({
      startTime: undefined,
      endTimeExclusive: undefined,
    });
  });
});
