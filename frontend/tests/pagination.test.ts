import { describe, expect, it } from "vitest";
import { clampPage, maxPage } from "../src/lib/pagination";

describe("page clamping", () => {
  it("pulls a page past the end back to the last page", () => {
    // 21 rows at 10/page, page 3 open; two rows get deleted.
    expect(clampPage(3, 19, 10)).toBe(2);
    expect(clampPage(5, 41, 20)).toBe(3);
  });
  it("keeps pages that are still in range", () => {
    expect(clampPage(2, 20, 10)).toBe(2);
    expect(clampPage(1, 1, 10)).toBe(1);
  });
  it("falls back to page 1 for an empty list", () => {
    expect(clampPage(4, 0, 10)).toBe(1);
    expect(maxPage(0, 10)).toBe(1);
  });
  it("normalises invalid input", () => {
    expect(clampPage(0, 50, 10)).toBe(1);
    expect(clampPage(-3, 50, 10)).toBe(1);
    expect(clampPage(Number.NaN, 50, 10)).toBe(1);
    expect(clampPage(2.7, 50, 10)).toBe(2);
    expect(maxPage(50, 0)).toBe(1);
  });
});
