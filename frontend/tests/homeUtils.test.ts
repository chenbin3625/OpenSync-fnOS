import { describe, expect, it } from "vitest";
import { taskItemStatusNames, taskItemStatusOptions } from "../src/pages/Home/homeUtils";

describe("task item status labels", () => {
  it("matches backend retry status codes", () => {
    expect(taskItemStatusNames[10]).toBe("等待重试中");
    expect(taskItemStatusNames[11]).toBe("等待重试前");
    expect(taskItemStatusOptions.map((option) => option.value)).toContain(10);
    expect(taskItemStatusOptions.map((option) => option.value)).toContain(11);
  });
});
