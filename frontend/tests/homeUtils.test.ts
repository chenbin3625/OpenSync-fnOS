import { describe, expect, it } from "vitest";
import { taskItemStatusNames } from "../src/pages/Home/homeUtils";

describe("task item status labels", () => {
  it("matches backend retry status codes", () => {
    expect(taskItemStatusNames[10]).toBe("等待重试中");
    expect(taskItemStatusNames[11]).toBe("等待重试前");
    const statusValues = Object.keys(taskItemStatusNames).map(Number);
    expect(statusValues).toContain(10);
    expect(statusValues).toContain(11);
  });
});
