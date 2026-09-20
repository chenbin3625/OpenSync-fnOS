import { describe, expect, it } from "vitest";
import {
  buildJobPayload,
  defaultJobForm,
  jobToForm,
  parseExcludeFolders,
  selectJob,
  updateExcludeFolders,
  validateJobForm,
} from "../src/lib/taskForm";
import { methodNames } from "../src/pages/Home/homeUtils";

describe("existing synchronization contracts", () => {
  it("keeps multi-source, multi-target and all three synchronization modes", () => {
    expect(methodNames).toEqual(["仅新增", "全同步", "移动模式"]);

    for (const method of [0, 1, 2]) {
      const form = {
        ...defaultJobForm(7),
        method,
        srcPath: ["/Photos", "/Docs"],
        dstPath: ["/Backup", "/Archive"],
      };
      expect(buildJobPayload(form)).toMatchObject({
        alistId: 7,
        method,
        srcPath: form.srcPath,
        dstPath: form.dstPath,
      });
    }
  });
  it("keeps manual-only jobs enabled and translates file size units", () => {
    const payload = buildJobPayload({
      ...defaultJobForm(7),
      isCron: 2,
      enable: false,
      minFileSize: 2,
      minFileSizeUnit: "GB",
    });
    expect(payload.enable).toBe(1);
    expect(payload.minFileSize).toBe(2 * 1024 ** 3);
  });
  it("round trips existing job identifiers, cache settings and Cron expressions", () => {
    const job = {
      id: 8,
      alistId: 7,
      enable: 0,
      srcPath: '["/a","/b"]',
      dstPath: '["/c"]',
      remark: "test",
      method: 2,
      isCron: 1,
      interval: 33,
      useCacheS: 1,
      useCacheT: 0,
      scanIntervalS: 77,
      scanIntervalT: 88,
      minFileSize: 1024,
      maxFileSize: 4096,
      second: "0",
      minute: "*/5",
      hour: "*",
      day: "*",
      month: "*",
      day_of_week: "1-5",
      exclude: "*.tmp",
    };
    expect(buildJobPayload(jobToForm(job))).toMatchObject({
      ...job,
      srcPath: ["/a", "/b"],
      dstPath: ["/c"],
    });
  });
  it("rejects missing paths, inverted sizes and invalid Cron ranges", () => {
    const namedForm = { ...defaultJobForm(7), remark: "测试任务" };
    expect(validateJobForm(namedForm)).toContain("源目录");
    const form = { ...namedForm, srcPath: ["/a"], dstPath: ["/b"] };
    expect(
      validateJobForm({ ...form, minFileSize: 4, maxFileSize: 2 }),
    ).toContain("最大");
    expect(validateJobForm({ ...form, hour: "25" })).toContain("时");
    expect(validateJobForm(form)).toBe("");
  });
  it("does not substitute another task for an explicit unknown task id", () => {
    const jobs = [{ id: 1 }, { id: 2 }];
    expect(selectJob(jobs, null)).toEqual({ id: 1 });
    expect(selectJob(jobs, 999)).toBeUndefined();
  });

  it("updates generated folder exclude rules without changing manual rules", () => {
    const manualRules = "*.tmp\n!important.tmp";
    const withFolders = updateExcludeFolders(manualRules, [
      "photos/raw/",
      "cache/",
    ]);

    expect(withFolders).toContain(manualRules);
    expect(parseExcludeFolders(withFolders)).toEqual([
      "photos/raw/",
      "cache/",
    ]);
    expect(updateExcludeFolders(withFolders, ["logs/"])).toBe(
      "*.tmp\n!important.tmp\n\n# --- 文件夹过滤（自动生成，请勿手动编辑）---\nlogs/\n# --- 文件夹过滤结束 ---",
    );
    expect(updateExcludeFolders(withFolders, [])).toBe(manualRules);
  });
});
