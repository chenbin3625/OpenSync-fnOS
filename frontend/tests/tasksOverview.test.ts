import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";

const tasksSource = () =>
  readFileSync(new URL("../src/pages/Tasks.tsx", import.meta.url), "utf8");

describe("task overview", () => {
  it("shows latest execution time and duration instead of file size and exclude rules", () => {
    const source = tasksSource();

    expect(source).toContain('label="最近一次执行"');
    expect(source).toContain("api.history");
    expect(source).toContain("formatTimestamp(record.createTime)");
    expect(source).toContain("formatDuration(record.runTime - record.createTime)");
    expect(source).not.toContain('label="文件大小"');
    expect(source).not.toContain('label="排除规则"');
    expect(source).not.toContain("<Status");
    expect(source).not.toContain("taskRecordStatusNames");
    expect(source).not.toContain("成功 ${success}");
  });
});
