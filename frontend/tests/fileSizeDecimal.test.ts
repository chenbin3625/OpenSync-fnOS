import { describe, expect, it } from "vitest";
import {
  fileSizeToBytes,
  commitFileSizeInput,
  parseFileSizeInput,
  splitBytesToFileSize,
} from "../src/pages/Home/fileSizeUnits";
import { defaultJobForm, validateJobForm } from "../src/lib/taskForm";

// The editor used to round-trip every keystroke through Number(), so "1." became
// 1 and the next digit produced 12 — a 1.2 MB threshold was silently stored as
// 12 MB, leaving 1.2–12 MB files unsynced. These assert the value pipeline keeps
// fractional thresholds intact.
describe("fractional file size thresholds", () => {
  it("preserves an invalid blurred value for submit validation", () => {
    const minFileSize = commitFileSizeInput("invalid");
    expect(minFileSize).toBe("invalid");
    expect(validateJobForm({
      ...defaultJobForm(1),
      remark: "test",
      srcPath: ["/src/"],
      dstPath: ["/dst/"],
      minFileSize,
    })).toContain("文件大小");
  });
  it("round-trips a fractional MB threshold", () => {
    const bytes = fileSizeToBytes(1.2, "MB");
    expect(bytes).toBe(Math.round(1.2 * 1024 ** 2));
    expect(splitBytesToFileSize(bytes)).toEqual({ value: 1.2, unit: "MB" });
  });

  it("keeps sub-unit values below 1", () => {
    const bytes = fileSizeToBytes(0.5, "MB");
    expect(bytes).toBe(Math.round(0.5 * 1024 ** 2));
    // 0.5 MB is expressed in the largest unit that fits, i.e. KB.
    expect(splitBytesToFileSize(bytes)).toEqual({ value: 512, unit: "KB" });
  });

  it("treats blank or invalid input as no limit", () => {
    expect(fileSizeToBytes(0, "MB")).toBe(0);
    expect(fileSizeToBytes(Number.NaN, "MB")).toBe(0);
    expect(fileSizeToBytes(-5, "MB")).toBe(0);
  });

  it("does not confuse 1.2 with 12", () => {
    expect(fileSizeToBytes(1.2, "MB")).not.toBe(fileSizeToBytes(12, "MB"));
  });

  it("parses the raw decimal text kept by the editor", () => {
    expect(parseFileSizeInput("1.2")).toBe(1.2);
    expect(fileSizeToBytes("1.2", "MB")).toBe(Math.round(1.2 * 1024 ** 2));
  });
});

describe("byte-exact round trips", () => {
  // Opening a task and saving it unchanged must never rewrite min/maxFileSize.
  // splitBytesToFileSize therefore picks the largest unit that can represent
  // the byte count within two decimals, down to plain bytes.
  for (const bytes of [1500, 123456789, 1320702444, 1023]) {
    it(`round-trips ${bytes} bytes exactly`, () => {
      const split = splitBytesToFileSize(bytes);
      expect(fileSizeToBytes(split.value, split.unit)).toBe(bytes);
    });
  }

  it("degrades to bytes when no larger unit is exact", () => {
    expect(splitBytesToFileSize(1500)).toEqual({ value: 1500, unit: "B" });
  });

  it("keeps familiar unit choices when they are exact", () => {
    expect(splitBytesToFileSize(1.2 * 1024 ** 2)).toEqual({
      value: 1.2,
      unit: "MB",
    });
    expect(splitBytesToFileSize(512 * 1024)).toEqual({
      value: 512,
      unit: "KB",
    });
  });

  it("falls back to MB for an unknown unit", () => {
    expect(fileSizeToBytes(1, "XB")).toBe(1024 ** 2);
  });
});
