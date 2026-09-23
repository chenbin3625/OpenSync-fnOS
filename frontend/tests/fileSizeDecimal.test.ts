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
