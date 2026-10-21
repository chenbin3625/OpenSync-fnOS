import { describe, expect, it } from "vitest";
import { formatTimestamp } from "../src/utils/date";
import { formatDuration } from "../src/pages/Home/homeUtils";

// The old version of this file asserted substrings of Tasks.tsx (e.g.
// `not.toContain("<Status")`). That broke on any refactor and proved nothing
// about behaviour, so it now tests the formatting the overview actually
// renders for "最近一次执行".
describe("task overview latest execution", () => {
  it("shows a dash when there is no execution timestamp", () => {
    expect(formatTimestamp(undefined)).toBe("—");
    expect(formatTimestamp(null)).toBe("—");
    expect(formatTimestamp(0)).toBe("—");
  });

  it("formats the execution start time without seconds", () => {
    expect(formatTimestamp(1_800_000_000)).toMatch(
      /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/,
    );
  });

  it("formats the duration between createTime and runTime", () => {
    expect(formatDuration(125)).toBe("2分 5秒");
    expect(formatDuration(0)).toBe("0秒");
  });
});
