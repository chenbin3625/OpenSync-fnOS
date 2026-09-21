import { describe, expect, it } from "vitest";
import {
  buildJobPayload,
  defaultJobForm,
  fileTypeFilterGroups,
  isExcludePatternEnabled,
  jobToForm,
  parseExcludeFolders,
  selectJob,
  systemDirFilterGroups,
  updateExcludeFolders,
  validateJobForm,
} from "../src/lib/taskForm";
import { methodNames, methodOptions } from "../src/pages/Home/homeUtils";
import {
  initialExcludeExpandedKeys,
  normalizeExcludeRootPath,
} from "../src/lib/excludeTree";
import * as taskFormModule from "../src/lib/taskForm";

describe("existing synchronization contracts", () => {
  it("keeps multi-source, multi-target and the two supported synchronization modes", () => {
    expect(methodNames).toEqual(["增量同步", "全量同步"]);
    expect(methodOptions.map((method) => method.description).join("\n")).toContain(
      "不会删除目标端多余文件",
    );
    expect(methodOptions.map((method) => method.description).join("\n")).toContain(
      "会删除目标端多余文件",
    );

    for (const method of [0, 1]) {
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

  it("expands selected source roots by default for folder filtering", () => {
    expect(initialExcludeExpandedKeys(["/Photos", "/Docs/"])).toEqual([
      "/Photos",
      "/Docs",
    ]);
    expect(normalizeExcludeRootPath("///")).toBe("/");
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
  it("excludes Synology recycle directories by default", () => {
    expect(defaultJobForm().exclude.split("\n")).toContain("\\#recycle/");
  });
  it("enables safe default file and folder filters", () => {
    const exclude = defaultJobForm().exclude;
    const enabledFileGroups = new Set(["system", "temporary", "lock"]);
    for (const group of fileTypeFilterGroups) {
      const selected = group.patterns.filter((pattern) =>
        isExcludePatternEnabled(exclude, pattern),
      );
      if (enabledFileGroups.has(group.key)) {
        expect(selected, group.label).toEqual([...group.patterns]);
      } else {
        expect(selected, group.label).toEqual([]);
      }
    }

    for (const group of systemDirFilterGroups) {
      expect(
        group.patterns.filter((pattern) =>
          isExcludePatternEnabled(exclude, pattern),
        ),
        group.label,
      ).toEqual([...group.patterns]);
    }
  });
  it("toggles file type presets without changing unrelated exclude rules", () => {
    const filters = taskFormModule as typeof taskFormModule & {
      updateExcludePatterns?: (
        exclude: string,
        patterns: string[],
        enabled: boolean,
      ) => string;
    };
    expect(filters.updateExcludePatterns).toBeTypeOf("function");

    const original = "keep-this/\n*.tmp\n# *.log\nphotos/raw/";
    const disabled = filters.updateExcludePatterns!(
      original,
      ["*.tmp", "*.temp"],
      false,
    );
    expect(disabled).toContain("# *.tmp");
    expect(disabled).toContain("keep-this/");
    expect(disabled).toContain("photos/raw/");

    const enabled = filters.updateExcludePatterns!(
      disabled,
      ["*.tmp", "*.temp"],
      true,
    );
    expect(enabled.split("\n")).toEqual(
      expect.arrayContaining(["*.tmp", "*.temp"]),
    );
  });
  it("adds custom file names and wildcard extension patterns separately", () => {
    const filters = taskFormModule as typeof taskFormModule & {
      addCustomFileTypeFilter?: (exclude: string, input: string) => string;
      parseCustomFileTypeFilters?: (
        exclude: string,
      ) => { pattern: string; enabled: boolean }[];
      parseOtherFileTypeFilters?: (
        exclude: string,
      ) => { pattern: string; enabled: boolean }[];
      updateExcludePatterns?: (
        exclude: string,
        patterns: string[],
        enabled: boolean,
      ) => string;
    };
    expect(filters.addCustomFileTypeFilter).toBeTypeOf("function");
    expect(filters.parseCustomFileTypeFilters).toBeTypeOf("function");
    expect(filters.parseOtherFileTypeFilters).toBeTypeOf("function");

    let added = filters.addCustomFileTypeFilter!(".DS_Store", "a.bcd");
    added = filters.addCustomFileTypeFilter!(added, "ipc-socket");
    added = filters.addCustomFileTypeFilter!(added, "*.DOC");
    expect(filters.parseOtherFileTypeFilters!(added)).toEqual([
      { pattern: "a.bcd", enabled: true },
      { pattern: "ipc-socket", enabled: true },
    ]);
    expect(isExcludePatternEnabled(added, "*.doc")).toBe(true);
    const disabled = filters.updateExcludePatterns!(added, ["a.bcd"], false);
    expect(filters.parseOtherFileTypeFilters!(disabled)).toEqual([
      { pattern: "a.bcd", enabled: false },
      { pattern: "ipc-socket", enabled: true },
    ]);
    expect(isExcludePatternEnabled(disabled, "*.doc")).toBe(true);
    expect(disabled).toContain(".DS_Store");
  });
  it("treats legacy manual file patterns and file names as other file filters", () => {
    const filters = taskFormModule as typeof taskFormModule & {
      parseOtherFileTypeFilters?: (
        exclude: string,
      ) => { pattern: string; enabled: boolean }[];
    };
    expect(filters.parseOtherFileTypeFilters).toBeTypeOf("function");

    expect(
      filters.parseOtherFileTypeFilters!(
        "keep-dir/\nreport.final\n*.pst\n# *.vmdk\n*.tmp\n.DS_Store",
      ),
    ).toEqual([
      { pattern: "report.final", enabled: true },
      { pattern: "*.pst", enabled: true },
      { pattern: "*.vmdk", enabled: false },
    ]);
  });
  it("round trips existing job identifiers, cache settings and Cron expressions", () => {
    const job = {
      id: 8,
      alistId: 7,
      enable: 0,
      srcPath: '["/a","/b"]',
      dstPath: '["/c"]',
      remark: "test",
      method: 1,
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
  it("normalizes unsupported legacy synchronization methods when editing", () => {
    const form = jobToForm({
      id: 9,
      alistId: 7,
      srcPath: '["/a"]',
      dstPath: '["/b"]',
      method: 2,
    });

    expect(form.method).toBe(0);
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
